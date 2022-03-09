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

	kueue "sigs.k8s.io/kueue/api/v1alpha1"
	"sigs.k8s.io/kueue/pkg/cache"
	"sigs.k8s.io/kueue/pkg/queue"
	"sigs.k8s.io/kueue/pkg/util/routine"
	"sigs.k8s.io/kueue/pkg/workload"
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
	// admitted by a clusterQueue or a cohort (if borrowing).
	// This is because there can be other workloads deeper in a queue whose head
	// got admitted that should be scheduled before the heads of other queues.
	usedClusterQueue := sets.NewString()
	usedCohorts := sets.NewString()
	admittedWorkloads := sets.NewString()
	for _, e := range entries {
		if usedClusterQueue.Has(e.ClusterQueue) {
			continue
		}
		c := snapshot.ClusterQueues[e.ClusterQueue]
		if len(e.borrows) > 0 && c.Cohort != nil && usedCohorts.Has(c.Cohort.Name) {
			continue
		}
		usedClusterQueue.Insert(e.ClusterQueue)
		log := log.WithValues("queuedWorkload", klog.KObj(e.Obj), "clusterQueue", klog.KRef("", e.ClusterQueue))
		if err := s.admit(ctrl.LoggerInto(ctx, log), &e); err != nil {
			log.Error(err, "Failed admitting workload by clusterQueue")
		} else {
			admittedWorkloads.Insert(workload.Key(e.Obj))
		}
		// Even if there was a failure, we shouldn't admit other workloads to this
		// cohort.
		if c.Cohort != nil {
			usedCohorts.Insert(c.Cohort.Name)
		}
	}

	// 6. Requeue the heads that were not scheduled.
	for _, w := range headWorkloads {
		if admittedWorkloads.Has(workload.Key(w.Obj)) {
			continue
		}
		if s.queues.RequeueWorkload(ctx, &w) {
			log.V(2).Info("Workload re-queued", "queuedWorkload", klog.KObj(w.Obj), "queue", klog.KRef(w.Obj.Namespace, w.Obj.Spec.QueueName))
		}
	}
}

// entry holds requirements for a workload to be admitted by a clusterQueue.
type entry struct {
	// workload.Info holds the workload from the API as well as resource usage
	// and flavors assigned.
	workload.Info
	// borrows is the resources that the workload would need to borrow from the
	// cohort if it was scheduled in the clusterQueue.
	borrows cache.Resources
}

// nominate returns the workloads with their requirements (resource flavors, borrowing) if
// they were admitted by the clusterQueues in the snapshot.
func (s *Scheduler) nominate(ctx context.Context, workloads []workload.Info, snap cache.Snapshot) []entry {
	log := ctrl.LoggerFrom(ctx)
	entries := make([]entry, 0, len(workloads))
	for _, w := range workloads {
		log := log.WithValues("queuedWorkload", klog.KObj(w.Obj), "clusterQueue", klog.KRef("", w.ClusterQueue))
		cq := snap.ClusterQueues[w.ClusterQueue]
		if cq == nil {
			log.V(3).Info("ClusterQueue not found when calculating workload admission requirements")
			continue
		}
		ns := corev1.Namespace{}
		if err := s.client.Get(ctx, types.NamespacedName{Name: w.Obj.Namespace}, &ns); err != nil {
			log.Error(err, "Looking up namespace")
			continue
		}
		if !cq.NamespaceSelector.Matches(labels.Set(ns.Labels)) {
			log.V(2).Info("Workload namespace doesn't match clusterQueue selector")
			continue
		}
		e := entry{Info: w}
		if !e.assignFlavors(log, cq) {
			log.V(2).Info("Workload didn't fit in remaining clusterQueue even when borrowing")
			continue
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
func (e *entry) assignFlavors(log logr.Logger, cq *cache.ClusterQueue) bool {
	flavoredRequests := make([]workload.PodSetResources, 0, len(e.TotalRequests))
	wUsed := make(cache.Resources)
	wBorrows := make(cache.Resources)
	for i, podSet := range e.TotalRequests {
		flavors := make(map[corev1.ResourceName]string, len(podSet.Requests))
		for resName, reqVal := range podSet.Requests {
			rFlavor, borrow := findFlavorForResource(log, resName, reqVal, wUsed[resName], cq, &e.Obj.Spec.PodSets[i].Spec)
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
			Name:            e.Obj.Spec.PodSets[i].Name,
			ResourceFlavors: e.TotalRequests[i].Flavors,
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
		log.Error(err, "Admitting workload and assigning flavors")
		log.V(2).Info("Re-queueing")
		s.queues.RequeueWorkload(ctx, &e.Info)
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
	cq *cache.ClusterQueue,
	spec *corev1.PodSpec) (string, int64) {
	// We will only check against the flavors' labels for the resource.
	selector := flavorSelector(spec, cq.LabelKeys[name])
	for _, flavor := range cq.RequestableResources[name] {
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
		ok, borrow := fitsFlavorLimits(name, val+wUsed[flavor.Name], cq, &flavor)
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
func fitsFlavorLimits(name corev1.ResourceName, val int64, cq *cache.ClusterQueue, flavor *cache.FlavorInfo) (bool, int64) {
	used := cq.UsedResources[name][flavor.Name]
	if used+val > flavor.Ceiling {
		// Past borrowing limit.
		return false, 0
	}
	cohortUsed := used
	cohortTotal := flavor.Guaranteed
	if cq.Cohort != nil {
		cohortUsed = cq.Cohort.UsedResources[name][flavor.Name]
		cohortTotal = cq.Cohort.RequestableResources[name][flavor.Name]
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
