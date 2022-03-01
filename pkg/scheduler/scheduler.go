/*
Copyright 2022 The Kubernetes Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package scheduler

import (
	"context"
	"fmt"
	"sort"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/record"
	corev1helpers "k8s.io/component-helpers/scheduling/corev1"
	"k8s.io/component-helpers/scheduling/corev1/nodeaffinity"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/api/v1alpha1"
	"sigs.k8s.io/kueue/pkg/capacity"
	"sigs.k8s.io/kueue/pkg/queue"
	"sigs.k8s.io/kueue/pkg/workload"
)

type Scheduler struct {
	queues        *queue.Manager
	capacityCache *capacity.Cache
	client        client.Client
	recorder      record.EventRecorder
}

func New(queues *queue.Manager, cache *capacity.Cache, cl client.Client, recorder record.EventRecorder) *Scheduler {
	return &Scheduler{
		queues:        queues,
		capacityCache: cache,
		client:        cl,
		recorder:      recorder,
	}
}

func (s *Scheduler) Start(ctx context.Context) {
	log := ctrl.LoggerFrom(ctx).WithName("scheduler")
	ctx = ctrl.LoggerInto(ctx, log)
	wait.UntilWithContext(ctx, s.schedule, 0)
}

func (s *Scheduler) schedule(ctx context.Context) {
	log, err := logr.FromContext(ctx)
	if err != nil {
		return
	}
	// 1. Get the heads from the queues, including their desired capacity.
	// This operation blocks while the queues are empty.
	headWorkloads := s.queues.Heads(ctx)
	// No elements means the program is finishing.
	if len(headWorkloads) == 0 {
		return
	}

	// 2. Take a snapshot of the capacities and their usage.
	snapshot := s.capacityCache.Snapshot()

	// 3. Calculate requirements for assigning workloads to capacities
	// (resource flavors, borrowing).
	entries := calculateRequirementsForAssignments(log, headWorkloads, snapshot)

	// 4. Sort entries based on borrowing and timestamps.
	sort.Sort(entryOrdering(entries))

	// 5. Assign to capacities, ensuring that no more than one workload gets
	// assigned to a capacity or a cohort (if borrowing).
	// This is because there can be other workloads deeper in a queue whose head
	// got assigned that should be scheduled before the heads of other queues.
	usedCapacity := sets.NewString()
	usedCohorts := sets.NewString()
	assignedWorkloads := sets.NewString()
	for _, e := range entries {
		if usedCapacity.Has(e.Capacity) {
			continue
		}
		c := snapshot.Capacities[e.Capacity]
		if len(e.borrows) > 0 && c.Cohort != nil && usedCohorts.Has(c.Cohort.Name) {
			continue
		}
		usedCapacity.Insert(e.Capacity)
		log := log.WithValues("queuedWorkload", klog.KObj(e.Obj), "capacity", e.Capacity)
		if err := s.assign(ctrl.LoggerInto(ctx, log), &e); err != nil {
			log.Error(err, "Failed assigning workload to capacity")
		} else {
			assignedWorkloads.Insert(workload.Key(e.Obj))
		}
		// Even if there was a failure, we shouldn't assign other workloads to this
		// cohort.
		if c.Cohort != nil {
			usedCohorts.Insert(c.Cohort.Name)
		}
	}

	// 6. Requeue the heads that were not scheduled.
	for _, w := range headWorkloads {
		if assignedWorkloads.Has(workload.Key(w.Obj)) {
			continue
		}
		if s.queues.RequeueWorkload(ctx, &w) {
			log.V(2).Info("Workload requeued", "queuedWorkload", klog.KObj(w.Obj), "queue", klog.KRef(w.Obj.Namespace, w.Obj.Spec.QueueName))
		}
	}
}

// entry holds requirements for a workload to be scheduled in a capacity.
type entry struct {
	// workload.Info holds the workload from the API as well as resource usage
	// and flavors assigned.
	workload.Info
	// borrows is the resources that the workload would need to borrow from the
	// cohort if it was scheduled in the capacity.
	borrows capacity.Resources
}

// calculateRequirementsForAssignments returns the workloads with their
// requirements (resource flavors, borrowing) if they were assigned to the
// capacities in the snapshot.
func calculateRequirementsForAssignments(log logr.Logger, workloads []workload.Info, snap capacity.Snapshot) []entry {
	entries := make([]entry, 0, len(workloads))
	for _, w := range workloads {
		log := log.WithValues("queuedWorkload", klog.KObj(w.Obj), "capacity", w.Capacity)
		c := snap.Capacities[w.Capacity]
		if c == nil {
			log.V(3).Info("Capacity not found when calculating workload assignments")
			continue
		}
		e := entry{Info: w}
		if !e.assignFlavors(log, c) {
			log.V(2).Info("Workload didn't fit in remaining capacity even when borrowing")
			continue
		}
		entries = append(entries, e)
	}
	return entries
}

// assignFlavors calculates the flavors that should be assigned to this entry
// if scheduled to this capacity, including details of how much it needs to
// borrow from the cohort.
// It returns whether the entry would fit. If it doesn't fit, the object is
// unmodified.
func (e *entry) assignFlavors(log logr.Logger, cap *capacity.Capacity) bool {
	flavoredRequests := make([]workload.PodSetResources, 0, len(e.TotalRequests))
	wUsed := make(capacity.Resources)
	wBorrows := make(capacity.Resources)
	for i, podSet := range e.TotalRequests {
		flavors := make(map[corev1.ResourceName]string, len(podSet.Requests))
		for resName, reqVal := range podSet.Requests {
			rFlavor, borrow := findFlavorForResource(log, resName, reqVal, wUsed[resName], cap, &e.Obj.Spec.Pods[i].Spec)
			if rFlavor == "" {
				return false
			}
			if borrow > 0 {
				if wBorrows[resName] == nil {
					wBorrows[resName] = make(map[string]int64)
				}
				// Don't accumulate borrowing. The returned `borrow` already considers
				// usage from previous pod sets.
				wBorrows[resName][rFlavor] = borrow
			}
			if wUsed[resName] == nil {
				wUsed[resName] = make(map[string]int64)
			}
			wUsed[resName][rFlavor] += reqVal
			flavors[resName] = rFlavor
		}
		flavoredRequests = append(flavoredRequests, workload.PodSetResources{
			Name:     podSet.Name,
			Requests: podSet.Requests,
			Flavors:  flavors,
		})
	}
	e.TotalRequests = flavoredRequests
	if len(wBorrows) > 0 {
		e.borrows = wBorrows
	}
	return true
}

// assign sets the assigned capacity and flavors into the workload of
// the entry, and asynchronously updates the object in the apiserver after
// assuming it in the cache.
func (s *Scheduler) assign(ctx context.Context, e *entry) error {
	log := ctrl.LoggerFrom(ctx)
	newWorkload := e.Obj.DeepCopy()
	for i := range newWorkload.Spec.Pods {
		podSet := &newWorkload.Spec.Pods[i]
		podSet.AssignedFlavors = e.TotalRequests[i].Flavors
	}
	newWorkload.Spec.AssignedCapacity = kueue.CapacityReference(e.Capacity)
	if err := s.capacityCache.AssumeWorkload(newWorkload); err != nil {
		return err
	}
	log.V(2).Info("Workload assumed in the cache")

	go func() {
		err := s.client.Update(ctx, newWorkload)
		if err == nil {
			s.recorder.Eventf(newWorkload, corev1.EventTypeNormal, "Assigned", fmt.Sprintf("Assigned to capacity %v", e.Capacity))
			log.V(2).Info("Successfully assigned capacity and resource flavors to workload")
			return
		}
		// Ignore errors because the workload or capacity could have been deleted
		// by an event.
		_ = s.capacityCache.ForgetWorkload(newWorkload)
		if errors.IsNotFound(err) {
			log.V(2).Info("Workload not assigned because it was deleted")
			return
		}
		log.Error(err, "Assigning capacity and resource flavors to workload")
		log.V(2).Info("Requeueing")
		s.queues.RequeueWorkload(ctx, &e.Info)
	}()

	return nil
}

// findFlavorForResources returns a flavor which can satisfy the resource request,
// given that wUsed is the usage of flavors by previous podsets.
// If it finds a flavor, also returns any borrowing required.
func findFlavorForResource(
	log logr.Logger,
	name corev1.ResourceName,
	val int64,
	wUsed map[string]int64,
	cap *capacity.Capacity,
	spec *corev1.PodSpec) (string, int64) {
	// We will only check against the flavors' labels for the resource.
	selector := flavorSelector(spec, cap.LabelKeys[name])
	for _, flavor := range cap.RequestableResources[name] {
		_, untolerated := corev1helpers.FindMatchingUntoleratedTaint(flavor.Taints, spec.Tolerations, func(t *corev1.Taint) bool {
			return t.Effect == corev1.TaintEffectNoSchedule || t.Effect == corev1.TaintEffectNoExecute
		})
		if untolerated {
			continue
		}
		if match, err := selector.Match(&corev1.Node{ObjectMeta: metav1.ObjectMeta{Labels: flavor.Labels}}); !match || err != nil {
			if err != nil {
				log.Error(err, "Matching workload affinity against flavor; no flavor assigned")
				return "", 0
			}
			continue
		}

		// Consider the usage assigned to previous pod sets.
		ok, borrow := fitsFlavorLimits(name, val+wUsed[flavor.Name], cap, &flavor)
		if ok {
			return flavor.Name, borrow
		}
	}
	return "", 0
}

func flavorSelector(spec *corev1.PodSpec, allowedKeys sets.String) nodeaffinity.RequiredNodeAffinity {
	// This function generally replicates the implementation of kube-scheduler's NodeAffintiy
	// Filter plugin as of v1.24.
	var specCopy corev1.PodSpec

	// Remove affinity constraints with irrelevant keys.
	if len(spec.NodeSelector) != 0 {
		specCopy.NodeSelector = map[string]string{}
		for k, v := range spec.NodeSelector {
			if allowedKeys.Has(k) {
				specCopy.NodeSelector[k] = v
			}
		}
	}

	affinity := spec.Affinity
	if affinity != nil && affinity.NodeAffinity != nil && affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution != nil {
		var termsCopy []corev1.NodeSelectorTerm
		for _, t := range affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms {
			var expCopy []corev1.NodeSelectorRequirement
			for _, e := range t.MatchExpressions {
				if allowedKeys.Has(e.Key) {
					expCopy = append(expCopy, e)
				}
			}
			// If a term becomes empty, it means node affinity matches any flavor since those terms are ORed,
			// and so matching gets reduced to spec.NodeSelector
			if len(expCopy) == 0 {
				termsCopy = nil
				break
			}
			termsCopy = append(termsCopy, corev1.NodeSelectorTerm{MatchExpressions: expCopy})
		}
		if len(termsCopy) != 0 {
			specCopy.Affinity = &corev1.Affinity{
				NodeAffinity: &corev1.NodeAffinity{
					RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{
						NodeSelectorTerms: termsCopy,
					},
				},
			}
		}
	}
	return nodeaffinity.GetRequiredNodeAffinity(&corev1.Pod{Spec: specCopy})
}

// fitsFlavorLimits returns whether a requested resource fits in a specific flavor's quota limits.
// If it fits, also returns any borrowing required.
func fitsFlavorLimits(name corev1.ResourceName, val int64, cap *capacity.Capacity, flavor *capacity.FlavorInfo) (bool, int64) {
	used := cap.UsedResources[name][flavor.Name]
	if used+val > flavor.Ceiling {
		// Past borrowing limit.
		return false, 0
	}
	cohortUsed := used
	cohortTotal := flavor.Guaranteed
	if cap.Cohort != nil {
		cohortUsed = cap.Cohort.UsedResources[name][flavor.Name]
		cohortTotal = cap.Cohort.RequestableResources[name][flavor.Name]
	}
	borrow := used + val - flavor.Guaranteed
	if borrow < 0 {
		borrow = 0
	}
	if cohortUsed+val > cohortTotal {
		// Doesn't fit even with borrowing.
		// TODO(PostMVP): preemption could help if borrow == 0
		return false, 0
	}
	return true, borrow
}

type entryOrdering []entry

func (e entryOrdering) Len() int {
	return len(e)
}

func (e entryOrdering) Swap(i, j int) {
	e[i], e[j] = e[j], e[i]
}

// Less is the ordering criteria:
// 1. guaranteed before borrowing.
// 2. FIFO on creation timestamp.
func (e entryOrdering) Less(i, j int) bool {
	a := e[i]
	b := e[j]
	// 1. Prefer guaranteed (not borrowing)
	aGuaranteed := len(a.borrows) == 0
	bGuaranteed := len(b.borrows) == 0
	if aGuaranteed != bGuaranteed {
		return aGuaranteed
	}
	// 2. FIFO
	return a.Obj.CreationTimestamp.Before(&b.Obj.CreationTimestamp)
}
