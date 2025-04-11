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
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/utils/clock"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/cache"
	"sigs.k8s.io/kueue/pkg/resources"
	"sigs.k8s.io/kueue/pkg/workload"
)

type candidateIterator struct {
	candidates                         []*candidateElem
	RunIndex                           int
	frsNeedPreemption                  sets.Set[resources.FlavorResource]
	snapshot                           *cache.Snapshot
	AnyCandidateFromOtherQueues        bool
	AnyCandidateForHierarchicalReclaim bool
	hierarchicalReclaimCtx             *HierarchicalPreemptionCtx
}

type candidateElem struct {
	wl *workload.Info
	// lca of this queue and cq (queue to which the new workload is submitted)
	lca *cache.CohortSnapshot
	// candidates above priority threshold cannot be preempted if at the same time
	// cq would borrow from other queues/cohorts
	onlyWithoutBorrowing bool
	reclaim              bool
}

// Need a separate function for candidateElem data type
// in the future we will modify the ordering to take into
// account the distance in the cohort hierarchy tree
func candidateElemsOrdering(candidates []*candidateElem, cq kueue.ClusterQueueReference, now time.Time, ordering func([]*workload.Info, kueue.ClusterQueueReference, time.Time) func(int, int) bool) func(int, int) bool {
	// Adapt the candidatesOrdering function to work with candidateElem
	adaptedOrdering := func(i, j int) bool {
		a := candidates[i].wl
		b := candidates[j].wl
		return ordering([]*workload.Info{a, b}, cq, now)(0, 1)
	}
	return adaptedOrdering
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
func splitEvicted(workloads []*candidateElem, predicate func(*candidateElem) bool) ([]*candidateElem, []*candidateElem) {
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
func NewCandidateIterator(hierarchicalReclaimCtx *HierarchicalPreemptionCtx, frsNeedPreemption sets.Set[resources.FlavorResource], snapshot *cache.Snapshot, clock clock.Clock, ordering func([]*workload.Info, kueue.ClusterQueueReference, time.Time) func(int, int) bool) *candidateIterator {
	sameQueueCandidates := collectSameQueueCandidates(hierarchicalReclaimCtx)
	hierarchyCandidates, priorityCandidates := collectCandidatesForHierarchicalReclaim(hierarchicalReclaimCtx)
	sort.Slice(sameQueueCandidates, candidateElemsOrdering(sameQueueCandidates, hierarchicalReclaimCtx.Cq.Name, clock.Now(), ordering))
	sort.Slice(priorityCandidates, candidateElemsOrdering(priorityCandidates, hierarchicalReclaimCtx.Cq.Name, clock.Now(), ordering))
	sort.Slice(hierarchyCandidates, candidateElemsOrdering(hierarchyCandidates, hierarchicalReclaimCtx.Cq.Name, clock.Now(), ordering))
	isEvicted := func(candidate *candidateElem) bool {
		return meta.IsStatusConditionTrue(candidate.wl.Obj.Status.Conditions, kueue.WorkloadEvicted)
	}
	evictedHierarchicalReclaimCandidates, nonEvictedHierarchicalReclaimCandidates := splitEvicted(hierarchyCandidates, isEvicted)
	evictedSTCandidates, nonEvictedSTCandidates := splitEvicted(priorityCandidates, isEvicted)
	evictedSameQueueCandidates, nonEvictedSameQueueCandidates := splitEvicted(sameQueueCandidates, isEvicted)
	var allCandidates []*candidateElem
	allCandidates = append(allCandidates, evictedHierarchicalReclaimCandidates...)
	allCandidates = append(allCandidates, evictedSTCandidates...)
	allCandidates = append(allCandidates, evictedSameQueueCandidates...)
	allCandidates = append(allCandidates, nonEvictedHierarchicalReclaimCandidates...)
	allCandidates = append(allCandidates, nonEvictedSTCandidates...)
	allCandidates = append(allCandidates, nonEvictedSameQueueCandidates...)
	return &candidateIterator{
		RunIndex:                           0,
		frsNeedPreemption:                  frsNeedPreemption,
		snapshot:                           snapshot,
		candidates:                         allCandidates,
		AnyCandidateFromOtherQueues:        len(hierarchyCandidates) > 0 || len(priorityCandidates) > 0,
		AnyCandidateForHierarchicalReclaim: len(hierarchyCandidates) > 0,
		hierarchicalReclaimCtx:             hierarchicalReclaimCtx,
	}
}

// Next allows to iterate over the ordered sequence of candidates, with the reason
// for eviction returned together with a candidate.
func (c *candidateIterator) Next(borrow bool) (*workload.Info, string) {
	if c.RunIndex >= len(c.candidates) {
		return nil, ""
	}
	candidate := c.candidates[c.RunIndex]
	c.RunIndex++
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
	if cache.IsWithinNominalInResources(&cq.ResourceNode, c.frsNeedPreemption) {
		return false
	}
	// we don't go all the way to the root but only to the lca node
	for node := cq.Parent(); node != candidate.lca; node = node.Parent() {
		if cache.IsWithinNominalInResources(&node.ResourceNode, c.frsNeedPreemption) {
			return false
		}
	}
	return true
}

func (c *candidateIterator) getPreemptionReason(candidate *candidateElem, borrowRun bool) string {
	if candidate.wl.ClusterQueue == c.hierarchicalReclaimCtx.Cq.Name {
		return kueue.InClusterQueueReason
	}
	if borrowRun && !candidate.reclaim {
		return kueue.InCohortReclaimWhileBorrowingReason
	}
	return kueue.InCohortReclamationReason
}
