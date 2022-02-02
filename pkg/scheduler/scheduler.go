/*
Copyright 2022 Google LLC.

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

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/wait"
	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"

	kueue "gke-internal.googlesource.com/gke-batch/kueue/api/v1alpha1"
	"gke-internal.googlesource.com/gke-batch/kueue/pkg/capacity"
	"gke-internal.googlesource.com/gke-batch/kueue/pkg/queue"
	"gke-internal.googlesource.com/gke-batch/kueue/pkg/workload"
)

type Scheduler struct {
	queues        *queue.Manager
	capacityCache *capacity.Cache
}

func New(queues *queue.Manager, cache *capacity.Cache) *Scheduler {
	return &Scheduler{
		queues:        queues,
		capacityCache: cache,
	}
}

func (s *Scheduler) Start(ctx context.Context) {
	ctx = logr.NewContext(ctx, ctrl.Log.WithName("scheduler"))
	wait.UntilWithContext(ctx, s.schedule, 0)
}

func (s *Scheduler) schedule(ctx context.Context) {
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
	// (resource types, borrowing).
	_ = calculateRequirementsForAssignments(ctx, headWorkloads, snapshot)
	// TODO: schedule
}

// entry holds requirements for a workload to be scheduled in a capacity.
type entry struct {
	// workload.Info holds the workload from the API as well as resource usage
	// and types assigned.
	workload.Info
	// borrows is the resouces that the workload would need to borrow from the
	// cohort if it was scheduled in the capacity.
	borrows capacity.Resources
}

// calculateRequirementsForAssignments returns the workloads with their
// requirements (resource types, borrowing) if they were assigned to the
// capacities in the snapshot.
func calculateRequirementsForAssignments(ctx context.Context, workloads []workload.Info, snap capacity.Snapshot) []entry {
	log := logr.FromContext(ctx)
	entries := make([]entry, 0, len(workloads))
	for _, w := range workloads {
		log := log.WithValues("queuedWorkload", klog.KObj(w.Obj), "capacity", w.Capacity)
		cap := snap.Capacities[w.Capacity]
		if cap == nil {
			log.V(3).Info("Capacity not found when calculating workload assingments")
			continue
		}
		e := entry{Info: w}
		if !e.assignTypes(cap) {
			log.V(2).Info("Workload didn't fit in remaining capacity even when borrowing")
		}
		entries = append(entries, e)
	}
	return entries
}

// assignTypes calculates the types that should be assigned to this entry
// if scheduled to this capacity, including details of how much it needs to
// borrow from the cohort.
// It returns whether the entry would fit. If it doesn't fit, the object is
// unmodified.
func (e *entry) assignTypes(cap *capacity.Capacity) bool {
	typedRequests := make(map[string]workload.Resources, len(e.TotalRequests))
	wUsed := make(capacity.Resources)
	wBorrows := make(capacity.Resources)
	for psName, podSet := range e.TotalRequests {
		types := make(map[corev1.ResourceName]string, len(podSet.Requests))
		for resName, reqVal := range podSet.Requests {
			rType, borrow := findTypeForResource(resName, reqVal, cap, wUsed[resName])
			if rType == "" {
				return false
			}
			if borrow > 0 {
				if wBorrows[resName] == nil {
					wBorrows[resName] = make(map[string]int64)
				}
				// Don't accumulate borrowing. The returned `borrow` already considers
				// usage from previous pod sets.
				wBorrows[resName][rType] = borrow
			}
			if wUsed[resName] == nil {
				wUsed[resName] = make(map[string]int64)
			}
			wUsed[resName][rType] += reqVal
			types[resName] = rType
		}
		typedRequests[psName] = workload.Resources{
			Requests: podSet.Requests,
			Types:    types,
		}
	}
	e.TotalRequests = typedRequests
	if len(wBorrows) > 0 {
		e.borrows = wBorrows
	}
	return true
}

// findTypeForResources returns a type which can satisfy the resource request,
// given that wUsed is the usage of types by previous podsets.
// If it finds a type, also returns any borrowing required.
func findTypeForResource(name corev1.ResourceName, val int64, cap *capacity.Capacity, wUsed map[string]int64) (string, int64) {
	for _, rType := range cap.RequestableResources[name] {
		// Consider the usage assigned to previous pod sets.
		ok, borrow := canAssignType(name, val+wUsed[rType.Name], cap, &rType)
		if ok {
			return rType.Name, borrow
		}
	}
	return "", 0
}

// canAssignType returns whether a requested resource fits in a specific type.
// If it fits, also returns any borrowing required.
func canAssignType(name corev1.ResourceName, val int64, cap *capacity.Capacity, rType *kueue.ResourceType) (bool, int64) {
	ceiling := workload.ResourceValue(name, rType.Quota.Ceiling)
	used := cap.UsedResources[name][rType.Name]
	if used+val > ceiling {
		// Past borrowing limit.
		return false, 0
	}
	guaranteed := workload.ResourceValue(name, rType.Quota.Guaranteed)
	cohortUsed := used
	cohortTotal := guaranteed
	if cap.Cohort != nil {
		cohortUsed = cap.Cohort.UsedResources[name][rType.Name]
		cohortTotal = cap.Cohort.RequestableResources[name][rType.Name]
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
