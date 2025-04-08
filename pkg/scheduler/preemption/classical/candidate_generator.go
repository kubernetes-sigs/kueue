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

package classical

import (
	"sort"
	"time"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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

type candidateIterator struct {
	candidates                  [][]*candidateElem
	runIndex                    map[bool]*candidateIndex
	frsNeedPreemption           sets.Set[resources.FlavorResource]
	snapshot                    *cache.Snapshot
	AnyCandidateFromOtherQueues bool
	hierarchicalReclaimCtx      *HierarchicalPreemptionCtx
}

type candidateElem struct {
	wl *workload.Info
	// lca of this queue and cq (queue to which the new workload is submitted)
	lca *cache.CohortSnapshot
	// candidates above priority threshold cannot be preempted if at the same time
	// cq would borrow from other queues/cohorts
	onlyWithoutBorrowing bool
}

// candidatesOrdering criteria:
// 0. Workloads already marked for preemption first.
// 1. Workloads from other ClusterQueues in the cohort before the ones in the
// same ClusterQueue as the preemptor.
// 2. Workloads with lower priority first.
// 3. Workloads admitted more recently first.
func CandidatesOrdering(candidates []*workload.Info, cq kueue.ClusterQueueReference, now time.Time) func(int, int) bool {
	return func(i, j int) bool {
		a := candidates[i]
		b := candidates[j]
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

func quotaReservationTime(wl *kueue.Workload, now time.Time) time.Time {
	cond := meta.FindStatusCondition(wl.Status.Conditions, kueue.WorkloadQuotaReserved)
	if cond == nil || cond.Status != metav1.ConditionTrue {
		// The condition wasn't populated yet, use the current time.
		return now
	}
	return cond.LastTransitionTime.Time
}

// Need a separate function for candidateElem data type
// in the future we will modify the ordering to take into
// account the distance in the cohort hierarchy tree
func candidateElemsOrdering(candidates []*candidateElem, cq kueue.ClusterQueueReference, now time.Time) func(int, int) bool {
	// Adapt the candidatesOrdering function to work with candidateElem
	adaptedOrdering := func(i, j int) bool {
		a := candidates[i].wl
		b := candidates[j].wl
		return CandidatesOrdering([]*workload.Info{a, b}, cq, now)(0, 1)
	}
	return adaptedOrdering
}

// Returns if resource requests fit in quota on this node
// and the amount of resources that exceed the guaranteed
// quota on this node. It is assumed that subsequent call
// to this function will be on nodes parent with remainingRequests
func workloadFitsInQuota(node *cache.ResourceNode, requests resources.FlavorResourceQuantities) (bool, resources.FlavorResourceQuantities) {
	var guaranteed int64
	fits := true
	remainingRequests := make(resources.FlavorResourceQuantities, len(requests))
	for fr, v := range requests {
		if node.Usage[fr]+v > node.SubtreeQuota[fr] {
			fits = false
		}
		guaranteed = 0
		if lendingLimit := node.Quotas[fr].LendingLimit; lendingLimit != nil {
			guaranteed = max(0, node.SubtreeQuota[fr]-*lendingLimit)
		}
		remainingRequests[fr] = v - max(0, guaranteed-node.Usage[fr])
	}
	return fits, remainingRequests
}

func WorkloadUsesResources(wl *workload.Info, frsNeedPreemption sets.Set[resources.FlavorResource]) bool {
	for _, ps := range wl.TotalRequests {
		for res, flv := range ps.Flavors {
			if frsNeedPreemption.Has(resources.FlavorResource{Flavor: flv, Resource: res}) {
				return true
			}
		}
	}
	return false
}

// assume a prefix of the elements has predicate = true
func split(workloads []*candidateElem, predicate func(*candidateElem) bool) ([]*candidateElem, []*candidateElem) {
	firstFalse := sort.Search(len(workloads), func(i int) bool {
		return !predicate(workloads[i])
	})
	return workloads[:firstFalse], workloads[firstFalse:]
}

// NewCandidateIterator creates a new iterator that yields candidate workloads for preemption
// The iterator can be used to perform two independent runs over the list of candidates:
// with and without borrowing. The runs are independent which means that the same candidates
// might be returned for both, but note that the candidates with borrrowing are a subset of
// candidates without borrowing.
func NewCandidateIterator(hierarchicalReclaimCtx *HierarchicalPreemptionCtx, frsNeedPreemption sets.Set[resources.FlavorResource], snapshot *cache.Snapshot, clock clock.Clock) *candidateIterator {
	sameQueueCandidates := collectSameQueueCandidates(hierarchicalReclaimCtx)
	hierarchyCandidates, priorityCandidates := collectCandidatesForHierarchicalReclaim(hierarchicalReclaimCtx)
	sort.Slice(sameQueueCandidates, candidateElemsOrdering(sameQueueCandidates, hierarchicalReclaimCtx.Cq.Name, clock.Now()))
	sort.Slice(priorityCandidates, candidateElemsOrdering(priorityCandidates, hierarchicalReclaimCtx.Cq.Name, clock.Now()))
	sort.Slice(hierarchyCandidates, candidateElemsOrdering(hierarchyCandidates, hierarchicalReclaimCtx.Cq.Name, clock.Now()))
	isEvicted := func(candidate *candidateElem) bool {
		return meta.IsStatusConditionTrue(candidate.wl.Obj.Status.Conditions, kueue.WorkloadEvicted)
	}
	evictedHierarchicalReclaimCandidates, nonEvictedHierarchicalReclaimCandidates := split(hierarchyCandidates, isEvicted)
	evictedSTCandidates, nonEvictedSTCandidates := split(priorityCandidates, isEvicted)
	evictedSameQueueCandidates, nonEvictedSameQueueCandidates := split(sameQueueCandidates, isEvicted)
	return &candidateIterator{
		runIndex: map[bool]*candidateIndex{
			true:  {list: 0, element: 0},
			false: {list: 0, element: 0},
		},
		frsNeedPreemption: frsNeedPreemption,
		snapshot:          snapshot,
		candidates: [][]*candidateElem{evictedHierarchicalReclaimCandidates, evictedSTCandidates, evictedSameQueueCandidates,
			nonEvictedHierarchicalReclaimCandidates, nonEvictedSTCandidates, nonEvictedSameQueueCandidates},
		AnyCandidateFromOtherQueues: len(hierarchyCandidates) > 0 || len(priorityCandidates) > 0,
		hierarchicalReclaimCtx:      hierarchicalReclaimCtx,
	}
}

// Next allows to iterate over the ordered sequence of candidates, with the reason
// for eviction returned together with a candidate.
func (c *candidateIterator) Next(borrow bool) (*workload.Info, string) {
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
		return c.Next(borrow)
	}
	return candidate.wl, c.getPreemptionReason(candidate, borrow)
}

// candidateIsValid checks if candidate is valid,
// as eg. some candidates can only be considered without borrowing
// Also, preemption of candidates might invalidate other candidates
func (c *candidateIterator) candidateIsValid(candidate *candidateElem, borrow bool) bool {
	if c.hierarchicalReclaimCtx.Cq.Name == candidate.wl.ClusterQueue {
		return true
	}
	if borrow && candidate.onlyWithoutBorrowing {
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
	if candidate.wl.ClusterQueue == c.hierarchicalReclaimCtx.Cq.Name {
		return kueue.InClusterQueueReason
	}
	fits, _ := workloadFitsInQuota(&c.hierarchicalReclaimCtx.Cq.ResourceNode, c.hierarchicalReclaimCtx.Requests)
	if borrowRun && !fits {
		return kueue.InCohortReclaimWhileBorrowingReason
	}
	return kueue.InCohortReclamationReason
}
