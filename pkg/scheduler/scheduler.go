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
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/wait"
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
}

func New(queues *queue.Manager, cache *capacity.Cache, cl client.Client) *Scheduler {
	return &Scheduler{
		queues:        queues,
		capacityCache: cache,
		client:        cl,
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
		cap := snapshot.Capacities[e.Capacity]
		if len(e.borrows) > 0 && cap.Cohort != nil && usedCohorts.Has(cap.Cohort.Name) {
			continue
		}
		usedCapacity.Insert(e.Capacity)
		s.assign(ctx, &e)
		assignedWorkloads.Insert(workload.Key(e.Obj))
		if cap.Cohort != nil {
			usedCohorts.Insert(cap.Cohort.Name)
		}
	}

	// 6. Requeue the heads that were not scheduled.
	for _, w := range headWorkloads {
		if assignedWorkloads.Has(workload.Key(w.Obj)) {
			continue
		}
		if s.queues.RequeueWorkload(ctx, &w) {
			log.V(2).Info("Workload requeued", "workload", klog.KObj(w.Obj), "queue", klog.KRef(w.Obj.Namespace, w.Obj.Spec.QueueName))
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
		cap := snap.Capacities[w.Capacity]
		if cap == nil {
			log.V(3).Info("Capacity not found when calculating workload assignments")
			continue
		}
		e := entry{Info: w}
		if !e.assignFlavors(cap) {
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
func (e *entry) assignFlavors(cap *capacity.Capacity) bool {
	flavoredRequests := make(map[string]workload.Resources, len(e.TotalRequests))
	wUsed := make(capacity.Resources)
	wBorrows := make(capacity.Resources)
	for psName, podSet := range e.TotalRequests {
		flavors := make(map[corev1.ResourceName]string, len(podSet.Requests))
		for resName, reqVal := range podSet.Requests {
			rFlavor, borrow := findFlavorForResource(resName, reqVal, cap, wUsed[resName])
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
		flavoredRequests[psName] = workload.Resources{
			Requests: podSet.Requests,
			Flavors:  flavors,
		}
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
func (s *Scheduler) assign(ctx context.Context, e *entry) {
	log := ctrl.LoggerFrom(ctx).WithValues("queuedWorkload", klog.KObj(e.Obj), "capacity", e.Capacity)
	newWorkload := e.Obj.DeepCopy()
	for i := range newWorkload.Spec.Pods {
		podSet := &newWorkload.Spec.Pods[i]
		podSet.AssignedFlavors = e.TotalRequests[podSet.Name].Flavors
	}
	newWorkload.Spec.AssignedCapacity = kueue.CapacityReference(e.Capacity)
	s.capacityCache.AssumeWorkload(newWorkload)
	log.V(2).Info("Workload assumed in the cache")

	go func() {
		err := s.client.Update(ctx, newWorkload)
		if err == nil {
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
}

// findFlavorForResources returns a flavor which can satisfy the resource request,
// given that wUsed is the usage of flavors by previous podsets.
// If it finds a flavor, also returns any borrowing required.
func findFlavorForResource(name corev1.ResourceName, val int64, cap *capacity.Capacity, wUsed map[string]int64) (string, int64) {
	for _, flavor := range cap.RequestableResources[name] {
		// Consider the usage assigned to previous pod sets.
		ok, borrow := canAssignFlavor(name, val+wUsed[flavor.Name], cap, &flavor)
		if ok {
			return flavor.Name, borrow
		}
	}
	return "", 0
}

// canAssignFlavor returns whether a requested resource fits in a specific flavor.
// If it fits, also returns any borrowing required.
func canAssignFlavor(name corev1.ResourceName, val int64, cap *capacity.Capacity, rFlavor *kueue.ResourceFlavor) (bool, int64) {
	ceiling := workload.ResourceValue(name, rFlavor.Quota.Ceiling)
	used := cap.UsedResources[name][rFlavor.Name]
	if used+val > ceiling {
		// Past borrowing limit.
		return false, 0
	}
	guaranteed := workload.ResourceValue(name, rFlavor.Quota.Guaranteed)
	cohortUsed := used
	cohortTotal := guaranteed
	if cap.Cohort != nil {
		cohortUsed = cap.Cohort.UsedResources[name][rFlavor.Name]
		cohortTotal = cap.Cohort.RequestableResources[name][rFlavor.Name]
	}
	borrow := used + val - guaranteed
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
