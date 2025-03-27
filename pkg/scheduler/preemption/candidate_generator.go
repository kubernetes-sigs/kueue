/*
Copyright The Kubernetes Authors.

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

package preemption

import (
	"sort"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/utils/clock"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/cache"
	"sigs.k8s.io/kueue/pkg/resources"
	"sigs.k8s.io/kueue/pkg/util/priority"
	"sigs.k8s.io/kueue/pkg/workload"
)

type candidateIndex struct {
	list    int
	element int
}

type candidateElem struct {
	wl *workload.Info
	// lca of this queue and cq (queue to which the new workload is submitted)
	lca *cache.CohortSnapshot
	// candidates above priority threshold cannot be preempted if at the same time
	// cq would borrow from other queues/cohorts
	abovePriorityThreshold bool
}

type candidateIterator struct {
	candidates                  [][]*candidateElem
	runIndex                    map[bool]*candidateIndex
	frsNeedPreemption           sets.Set[resources.FlavorResource]
	snapshot                    *cache.Snapshot
	maxPriorityThreshold        *int32
	canBorrowInCohort           bool
	anyCandidateFromOtherQueues bool
	hierarchicalReclaimCtx      *hierarchicalPreemptionCtx
}

// Need a separate function for candidateElem data type
// in the future we will modify the ordering to take into
// account the distance in the cohort hierarchy tree
func candidateElemsOrdering(candidates []*candidateElem, cq kueue.ClusterQueueReference, now time.Time) func(int, int) bool {
	return func(i, j int) bool {
		a := candidates[i].wl
		b := candidates[j].wl
		aEvicted := meta.IsStatusConditionTrue(a.Obj.Status.Conditions, kueue.WorkloadEvicted)
		bEvicted := meta.IsStatusConditionTrue(b.Obj.Status.Conditions, kueue.WorkloadEvicted)
		if aEvicted != bEvicted {
			return aEvicted
		}
		aInCQ := a.ClusterQueue == cq
		bInCQ := b.ClusterQueue == cq
		if aInCQ != bInCQ {
			return !aInCQ
		}
		pa := priority.Priority(a.Obj)
		pb := priority.Priority(b.Obj)
		if pa != pb {
			return pa < pb
		}
		timeA := quotaReservationTime(a.Obj, now)
		timeB := quotaReservationTime(b.Obj, now)
		if !timeA.Equal(timeB) {
			return timeA.After(timeB)
		}
		// Arbitrary comparison for deterministic sorting.
		return a.Obj.UID < b.Obj.UID
	}
}

// assume a prefix of the elements has predicate = true
func split(workloads []*candidateElem, predicate func(*candidateElem) bool) ([]*candidateElem, []*candidateElem) {
	firstFalse := 0
	// Binary search to find the first element that doesn't satisfy the predicate.
	low := 0
	high := len(workloads)
	for low < high {
		mid := low + (high-low)/2
		if predicate(workloads[mid]) {
			low = mid + 1
		} else {
			high = mid
		}
	}
	firstFalse = low
	return workloads[:firstFalse], workloads[firstFalse:]
}

func NewCandidateIterator(preemptionCtx *preemptionCtx, workloadOrdering workload.Ordering, clock clock.Clock) *candidateIterator {
	var maxPriorityThreshold *int32
	sameQueueCandidates := []*candidateElem{}
	sTCandiates := []*candidateElem{}
	hierarchicalReclaimCandidates := []*candidateElem{}
	cq := preemptionCtx.preemptorCQ
	hierarchicalReclaimCtx := &hierarchicalPreemptionCtx{
		wl:                preemptionCtx.preemptor.Obj,
		cq:                cq,
		frsNeedPreemption: preemptionCtx.frsNeedPreemption,
		requests:          preemptionCtx.workloadUsage.Quota,
		workloadOrdering:  workloadOrdering,
	}

	if cq.Preemption.WithinClusterQueue != kueue.PreemptionPolicyNever {
		sameQueueCandidates = collectSameQueueCandidates(cq, &preemptionCtx.preemptor, preemptionCtx, workloadOrdering)
	}

	borrowWithinCohort := preemptionCtx.preemptorCQ.Preemption.BorrowWithinCohort
	if borrowWithinCohort != nil {
		maxPriorityThreshold = borrowWithinCohort.MaxPriorityThreshold
	} else {
		maxPriorityThreshold = nil
	}

	if cq.HasParent() && cq.Preemption.ReclaimWithinCohort != kueue.PreemptionPolicyNever {
		hierarchicalReclaimCandidates, sTCandiates = collectCandidatesForHierarchicalReclaim(
			hierarchicalReclaimCtx, maxPriorityThreshold,
		)
	}
	sort.Slice(sameQueueCandidates, candidateElemsOrdering(sameQueueCandidates, cq.Name, clock.Now()))
	sort.Slice(sTCandiates, candidateElemsOrdering(sTCandiates, cq.Name, clock.Now()))
	sort.Slice(hierarchicalReclaimCandidates, candidateElemsOrdering(hierarchicalReclaimCandidates, cq.Name, clock.Now()))
	isEvicted := func(candidate *candidateElem) bool {
		return meta.IsStatusConditionTrue(candidate.wl.Obj.Status.Conditions, kueue.WorkloadEvicted)
	}
	evictedHierarchicalReclaimCandidates, nonEvictedHierarchicalReclaimCandidates := split(hierarchicalReclaimCandidates, isEvicted)
	evictedSTCandidates, nonEvictedSTCandidates := split(sTCandiates, isEvicted)
	evictedSameQueueCandidates, nonEvictedSameQueueCandidates := split(sameQueueCandidates, isEvicted)
	return &candidateIterator{
		runIndex: map[bool]*candidateIndex{
			true:  {list: 0, element: 0},
			false: {list: 0, element: 0},
		},
		frsNeedPreemption: preemptionCtx.frsNeedPreemption,
		snapshot:          preemptionCtx.snapshot,
		candidates: [][]*candidateElem{evictedHierarchicalReclaimCandidates, evictedSTCandidates, evictedSameQueueCandidates,
			nonEvictedHierarchicalReclaimCandidates, nonEvictedSTCandidates, nonEvictedSameQueueCandidates},
		maxPriorityThreshold:        maxPriorityThreshold,
		canBorrowInCohort:           !(borrowWithinCohort == nil || borrowWithinCohort.Policy == kueue.BorrowWithinCohortPolicyNever),
		anyCandidateFromOtherQueues: len(hierarchicalReclaimCandidates) > 0 || len(sTCandiates) > 0,
		hierarchicalReclaimCtx:      hierarchicalReclaimCtx,
	}
}

func (c *candidateIterator) next(borrow bool) (*workload.Info, string) {
	index := c.runIndex[borrow]
	for ; index.list < len(c.candidates) && index.element >= len(c.candidates[index.list]); index.list++ {
		index.element = 0
	}
	if index.list >= len(c.candidates) {
		return nil, ""
	}
	candidate := c.candidates[index.list][index.element]
	index.element++
	if !c.candidateIsValid(candidate, borrow) {
		return c.next(borrow)
	}
	return candidate.wl, c.getPreemptionReason(candidate, borrow)
}

func (c *candidateIterator) candidateIsValid(candidate *candidateElem, borrow bool) bool {
	if c.hierarchicalReclaimCtx.cq.Name == candidate.wl.ClusterQueue {
		return true
	}
	if borrow && (!c.canBorrowInCohort || candidate.abovePriorityThreshold) {
		return false
	}
	cq := c.snapshot.ClusterQueue(candidate.wl.ClusterQueue)
	if cohortWithinNominalInResourcesNeedingPreemption(&cq.ResourceNode, c.frsNeedPreemption) {
		return false
	}
	// we don't go all the way to the root but only to the lca node
	for node := cq.Parent(); node.HasParent() && node != candidate.lca; node = node.Parent() {
		if cohortWithinNominalInResourcesNeedingPreemption(&node.ResourceNode, c.frsNeedPreemption) {
			return false
		}
	}
	return true
}

func (c *candidateIterator) getPreemptionReason(candidate *candidateElem, borrowRun bool) string {
	if candidate.wl.ClusterQueue == c.hierarchicalReclaimCtx.cq.Name {
		return kueue.InClusterQueueReason
	}
	if borrowRun && !workloadFitsInQuota(&c.hierarchicalReclaimCtx.cq.ResourceNode, c.hierarchicalReclaimCtx.requests) {
		return kueue.InCohortReclaimWhileBorrowingReason
	}
	return kueue.InCohortReclamationReason
}

func collectSameQueueCandidates(cq *cache.ClusterQueueSnapshot, wl *workload.Info, preemptionCtx *preemptionCtx, workloadOrdering workload.Ordering) []*candidateElem {
	var sameQueueCandidates []*candidateElem
	wlPriority := priority.Priority(wl.Obj)
	considerSamePrio := (cq.Preemption.WithinClusterQueue == kueue.PreemptionPolicyLowerOrNewerEqualPriority)
	preemptorTS := workloadOrdering.GetQueueOrderTimestamp(wl.Obj)

	for _, candidateWl := range cq.Workloads {
		candidatePriority := priority.Priority(candidateWl.Obj)
		if candidatePriority > wlPriority {
			continue
		}

		if candidatePriority == wlPriority && !(considerSamePrio && preemptorTS.Before(workloadOrdering.GetQueueOrderTimestamp(candidateWl.Obj))) {
			continue
		}

		if !workloadUsesResources(candidateWl, preemptionCtx.frsNeedPreemption) {
			continue
		}
		sameQueueCandidates = append(sameQueueCandidates,
			&candidateElem{
				wl:                     candidateWl,
				lca:                    nil,
				abovePriorityThreshold: false,
			})
	}
	return sameQueueCandidates
}
