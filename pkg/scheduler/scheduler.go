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
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/client-go/tools/record"
	corev1helpers "k8s.io/component-helpers/scheduling/corev1"
	"k8s.io/component-helpers/scheduling/corev1/nodeaffinity"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/core/v1alpha1"
	"sigs.k8s.io/kueue/pkg/cache"
	"sigs.k8s.io/kueue/pkg/queue"
	"sigs.k8s.io/kueue/pkg/util/routine"
	"sigs.k8s.io/kueue/pkg/workload"
)

const (
	errCouldNotAdmitWL = "Could not admit workload and assigning flavors in apiserver"
)

type Scheduler struct {
	queues                  *queue.Manager
	cache                   *cache.Cache
	client                  client.Client
	recorder                record.EventRecorder
	admissionRoutineWrapper routine.Wrapper
}

func New(queues *queue.Manager, cache *cache.Cache, cl client.Client, recorder record.EventRecorder) *Scheduler {
	return &Scheduler{
		queues:                  queues,
		cache:                   cache,
		client:                  cl,
		recorder:                recorder,
		admissionRoutineWrapper: routine.DefaultWrapper,
	}
}

func (s *Scheduler) Start(ctx context.Context) {
	log := ctrl.LoggerFrom(ctx).WithName("scheduler")
	ctx = ctrl.LoggerInto(ctx, log)
	wait.UntilWithContext(ctx, s.schedule, 0)
}

func (s *Scheduler) setAdmissionRoutineWrapper(wrapper routine.Wrapper) {
	s.admissionRoutineWrapper = wrapper
}

func (s *Scheduler) schedule(ctx context.Context) {
	log := ctrl.LoggerFrom(ctx)

	// 1. Get the heads from the queues, including their desired clusterQueue.
	// This operation blocks while the queues are empty.
	headWorkloads := s.queues.Heads(ctx)
	// No elements means the program is finishing.
	if len(headWorkloads) == 0 {
		return
	}

	// 2. Take a snapshot of the cache.
	snapshot := s.cache.Snapshot()

	// 3. Calculate requirements for admitting workloads (resource flavors, borrowing).
	// (resource flavors, borrowing).
	entries := s.nominate(ctx, headWorkloads, snapshot)

	// 4. Sort entries based on borrowing and timestamps.
	sort.Sort(entryOrdering(entries))

	// 5. Admit entries, ensuring that no more than one workload gets
	// admitted by a cohort (if borrowing).
	// This is because there can be other workloads deeper in a clusterQueue whose
	// head got admitted that should be scheduled in the cohort before the heads
	// of other clusterQueues.
	usedCohorts := sets.NewString()
	for i := range entries {
		e := &entries[i]
		if e.status != nominated {
			continue
		}
		c := snapshot.ClusterQueues[e.ClusterQueue]
		if len(e.borrows) > 0 && c.Cohort != nil && usedCohorts.Has(c.Cohort.Name) {
			e.status = skipped
			e.inadmissibleReason = "cohort used in this cycle"
			continue
		}
		log := log.WithValues("workload", klog.KObj(e.Obj), "clusterQueue", klog.KRef("", e.ClusterQueue))
		if err := s.admit(ctrl.LoggerInto(ctx, log), e); err == nil {
			e.status = assumed
		} else {
			e.inadmissibleReason = fmt.Sprintf("Failed to admit workload: %v", err)
		}
		// Even if there was a failure, we shouldn't admit other workloads to this
		// cohort.
		if c.Cohort != nil {
			usedCohorts.Insert(c.Cohort.Name)
		}
	}

	// 6. Requeue the heads that were not scheduled.
	for _, e := range entries {
		log.V(3).Info("Workload evaluated for admission",
			"workload", klog.KObj(e.Obj),
			"clusterQueue", klog.KRef("", e.ClusterQueue),
			"status", e.status,
			"reason", e.inadmissibleReason)
		if e.status != assumed {
			s.requeueAndUpdate(log, ctx, e)
		}
	}
}

type entryStatus string

const (
	// indicates if the workload was nominated for admission.
	nominated entryStatus = "nominated"
	// indicates if the workload was nominated but skipped in this cycle.
	skipped entryStatus = "skipped"
	// indicates if the workload was assumed to have been admitted.
	assumed entryStatus = "assumed"
)

// entry holds requirements for a workload to be admitted by a clusterQueue.
type entry struct {
	// workload.Info holds the workload from the API as well as resource usage
	// and flavors assigned.
	workload.Info
	// borrows is the resources that the workload would need to borrow from the
	// cohort if it was scheduled in the clusterQueue.
	borrows            cache.Resources
	status             entryStatus
	inadmissibleReason string
}

// nominate returns the workloads with their requirements (resource flavors, borrowing) if
// they were admitted by the clusterQueues in the snapshot.
func (s *Scheduler) nominate(ctx context.Context, workloads []workload.Info, snap cache.Snapshot) []entry {
	log := ctrl.LoggerFrom(ctx)
	entries := make([]entry, 0, len(workloads))
	for _, w := range workloads {
		log := log.WithValues("workload", klog.KObj(w.Obj), "clusterQueue", klog.KRef("", w.ClusterQueue))
		cq := snap.ClusterQueues[w.ClusterQueue]
		ns := corev1.Namespace{}
		e := entry{Info: w}
		if cq == nil {
			e.inadmissibleReason = "ClusterQueue not found"
		} else if err := s.client.Get(ctx, types.NamespacedName{Name: w.Obj.Namespace}, &ns); err != nil {
			e.inadmissibleReason = fmt.Sprintf("Could not obtain workload namespace: %v", err)
		} else if !cq.NamespaceSelector.Matches(labels.Set(ns.Labels)) {
			e.inadmissibleReason = "Workload namespace doesn't match ClusterQueue selector"
		} else if !e.assignFlavors(log, snap.ResourceFlavors, cq) {
			e.inadmissibleReason = "Workload didn't fit in the remaining quota"
		} else {
			e.status = nominated
		}
		entries = append(entries, e)
	}
	return entries
}

// assignFlavors calculates the flavors that should be assigned to this entry
// if admitted by this clusterQueue, including details of how much it needs to
// borrow from the cohort.
// It returns whether the entry would fit. If it doesn't fit, the object is
// unmodified.
func (e *entry) assignFlavors(log logr.Logger, resourceFlavors map[string]*kueue.ResourceFlavor, cq *cache.ClusterQueue) bool {
	flavoredRequests := make([]workload.PodSetResources, 0, len(e.TotalRequests))
	wUsed := make(cache.Resources)
	wBorrows := make(cache.Resources)
	for i, podSet := range e.TotalRequests {
		flavors := make(map[corev1.ResourceName]string, len(podSet.Requests))
		for resName, reqVal := range podSet.Requests {
			rFlavor, borrow := findFlavorForResource(log, resName, reqVal, wUsed[resName], resourceFlavors, cq, &e.Obj.Spec.PodSets[i].Spec)
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

// admit sets the admitting clusterQueue and flavors into the workload of
// the entry, and asynchronously updates the object in the apiserver after
// assuming it in the cache.
func (s *Scheduler) admit(ctx context.Context, e *entry) error {
	log := ctrl.LoggerFrom(ctx)
	newWorkload := e.Obj.DeepCopy()
	admission := &kueue.Admission{
		ClusterQueue:  kueue.ClusterQueueReference(e.ClusterQueue),
		PodSetFlavors: make([]kueue.PodSetFlavors, len(e.TotalRequests)),
	}
	for i := range e.TotalRequests {
		admission.PodSetFlavors[i] = kueue.PodSetFlavors{
			Name:    e.Obj.Spec.PodSets[i].Name,
			Flavors: e.TotalRequests[i].Flavors,
		}
	}
	newWorkload.Spec.Admission = admission
	if err := s.cache.AssumeWorkload(newWorkload); err != nil {
		return err
	}
	log.V(2).Info("Workload assumed in the cache")

	s.admissionRoutineWrapper.Run(func() {
		err := s.client.Update(ctx, newWorkload)
		if err == nil {
			s.recorder.Eventf(newWorkload, corev1.EventTypeNormal, "Admitted", "Admitted by ClusterQueue %v", admission.ClusterQueue)
			log.V(2).Info("Workload successfully admitted and assigned flavors")
			return
		}
		// Ignore errors because the workload or clusterQueue could have been deleted
		// by an event.
		_ = s.cache.ForgetWorkload(newWorkload)
		if errors.IsNotFound(err) {
			log.V(2).Info("Workload not admitted because it was deleted")
			return
		}

		log.Error(err, errCouldNotAdmitWL)
		s.requeueAndUpdate(log, ctx, *e)
	})

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
	resourceFlavors map[string]*kueue.ResourceFlavor,
	cq *cache.ClusterQueue,
	spec *corev1.PodSpec) (string, int64) {
	// We will only check against the flavors' labels for the resource.
	selector := flavorSelector(spec, cq.LabelKeys[name])
	for _, flvLimit := range cq.RequestableResources[name] {
		flavor, exist := resourceFlavors[flvLimit.Name]
		if !exist {
			log.Error(nil, "Flavor not found", "Flavor", flvLimit.Name)
			continue
		}
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

		// Check considering the flavor usage by previous pod sets.
		ok, borrow := fitsFlavorLimits(name, val+wUsed[flavor.Name], cq, &flvLimit)
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
func fitsFlavorLimits(name corev1.ResourceName, val int64, cq *cache.ClusterQueue, flavor *cache.FlavorLimits) (bool, int64) {
	used := cq.UsedResources[name][flavor.Name]
	if flavor.Max != nil && used+val > *flavor.Max {
		// Past borrowing limit.
		return false, 0
	}
	cohortUsed := used
	cohortTotal := flavor.Min
	if cq.Cohort != nil {
		cohortUsed = cq.Cohort.UsedResources[name][flavor.Name]
		cohortTotal = cq.Cohort.RequestableResources[name][flavor.Name]
	}
	borrow := used + val - flavor.Min
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
// 1. request under min quota before borrowing.
// 2. FIFO on creation timestamp.
func (e entryOrdering) Less(i, j int) bool {
	a := e[i]
	b := e[j]
	// 1. Request under min quota.
	aMin := len(a.borrows) == 0
	bMin := len(b.borrows) == 0
	if aMin != bMin {
		return aMin
	}
	// 2. FIFO.
	return a.Obj.CreationTimestamp.Before(&b.Obj.CreationTimestamp)
}

func (s *Scheduler) requeueAndUpdate(log logr.Logger, ctx context.Context, e entry) {
	added := s.queues.RequeueWorkload(ctx, &e.Info, e.status != "")
	log.V(2).Info("Workload re-queued", "workload", klog.KObj(e.Obj), "queue", klog.KRef(e.Obj.Namespace, e.Obj.Spec.QueueName), "added", added, "status", e.status)

	if e.status == "" {
		err := workload.UpdateStatus(ctx, s.client, e.Obj, kueue.WorkloadAdmitted, corev1.ConditionFalse, "Pending", e.inadmissibleReason)
		if err != nil {
			log.Error(err, "Could not update Workload status")
		}
		s.recorder.Eventf(e.Obj, corev1.EventTypeNormal, "Pending", e.inadmissibleReason)
	}
}
