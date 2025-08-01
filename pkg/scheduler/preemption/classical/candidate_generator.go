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

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/utils/clock"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/cache"
	"sigs.k8s.io/kueue/pkg/resources"
	"sigs.k8s.io/kueue/pkg/workload"
)

type candidateIterator struct {
	candidates                        []*candidateElem
	runIndex                          int
	frsNeedPreemption                 sets.Set[resources.FlavorResource]
	snapshot                          *cache.Snapshot
	NoCandidateFromOtherQueues        bool
	NoCandidateForHierarchicalReclaim bool
	hierarchicalReclaimCtx            *HierarchicalPreemptionCtx
}

type candidateElem struct {
	wl *workload.Info
	// lca of this queue and cq (queue to which the new workload is submitted)
	lca *cache.CohortSnapshot
	// candidates above priority threshold cannot be preempted if at the same time
	// cq would borrow from other queues/cohorts
	preemptionVariant preemptionVariant
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

// assume a prefix of the elements has condition WorkloadEvicted = true
func splitEvicted(workloads []*candidateElem) ([]*candidateElem, []*candidateElem) {
	firstFalse := sort.Search(len(workloads), func(i int) bool {
		return !meta.IsStatusConditionTrue(workloads[i].wl.Obj.Status.Conditions, kueue.WorkloadEvicted)
	})
	return workloads[:firstFalse], workloads[firstFalse:]
}

// NewCandidateIterator creates a new iterator that yields candidate workloads for preemption
// The iterator can be used to perform two independent runs over the list of candidates:
// with and without borrowing. The runs are independent which means that the same candidates
// might be returned for both, but note that the candidates with borrrowing are a subset of
// candidates without borrowing.
func NewCandidateIterator(hierarchicalReclaimCtx *HierarchicalPreemptionCtx, frsNeedPreemption sets.Set[resources.FlavorResource], snapshot *cache.Snapshot, clock clock.Clock, ordering func(logr.Logger, *workload.Info, *workload.Info, kueue.ClusterQueueReference, time.Time) bool) *candidateIterator {
	sameQueueCandidates := collectSameQueueCandidates(hierarchicalReclaimCtx)
	hierarchyCandidates, priorityCandidates := collectCandidatesForHierarchicalReclaim(hierarchicalReclaimCtx)
	sort.Slice(sameQueueCandidates, func(i, j int) bool {
		return ordering(hierarchicalReclaimCtx.Log, sameQueueCandidates[i].wl, sameQueueCandidates[j].wl, hierarchicalReclaimCtx.Cq.Name, clock.Now())
	})
	sort.Slice(priorityCandidates, func(i, j int) bool {
		return ordering(hierarchicalReclaimCtx.Log, priorityCandidates[i].wl, priorityCandidates[j].wl, hierarchicalReclaimCtx.Cq.Name, clock.Now())
	})
	sort.Slice(hierarchyCandidates, func(i, j int) bool {
		return ordering(hierarchicalReclaimCtx.Log, hierarchyCandidates[i].wl, hierarchyCandidates[j].wl, hierarchicalReclaimCtx.Cq.Name, clock.Now())
	})

	evictedHierarchicalReclaimCandidates, nonEvictedHierarchicalReclaimCandidates := splitEvicted(hierarchyCandidates)
	evictedSTCandidates, nonEvictedSTCandidates := splitEvicted(priorityCandidates)
	evictedSameQueueCandidates, nonEvictedSameQueueCandidates := splitEvicted(sameQueueCandidates)
	allCandidates := make([]*candidateElem, 0, len(hierarchyCandidates)+len(priorityCandidates)+len(sameQueueCandidates))
	allCandidates = append(allCandidates, evictedHierarchicalReclaimCandidates...)
	allCandidates = append(allCandidates, evictedSTCandidates...)
	allCandidates = append(allCandidates, evictedSameQueueCandidates...)
	allCandidates = append(allCandidates, nonEvictedHierarchicalReclaimCandidates...)
	allCandidates = append(allCandidates, nonEvictedSTCandidates...)
	allCandidates = append(allCandidates, nonEvictedSameQueueCandidates...)
	return &candidateIterator{
		runIndex:                          0,
		frsNeedPreemption:                 frsNeedPreemption,
		snapshot:                          snapshot,
		candidates:                        allCandidates,
		NoCandidateFromOtherQueues:        len(hierarchyCandidates) == 0 && len(priorityCandidates) == 0,
		NoCandidateForHierarchicalReclaim: len(hierarchyCandidates) == 0,
		hierarchicalReclaimCtx:            hierarchicalReclaimCtx,
	}
}

// Next allows to iterate over the ordered sequence of candidates, with the reason
// for eviction returned together with a candidate.
func (c *candidateIterator) Next(borrow bool) (*workload.Info, string) {
	if c.runIndex >= len(c.candidates) {
		return nil, ""
	}
	candidate := c.candidates[c.runIndex]
	c.runIndex++
	if !c.candidateIsValid(candidate, borrow) {
		return c.Next(borrow)
	}
	return candidate.wl, candidate.preemptionVariant.PreemptionReason()
}

// candidateIsValid checks if candidate is valid,
// as eg. some candidates can only be considered without borrowing
// Also, preemption of candidates might invalidate other candidates
func (c *candidateIterator) candidateIsValid(candidate *candidateElem, borrow bool) bool {
	if c.hierarchicalReclaimCtx.Cq.Name == candidate.wl.ClusterQueue {
		return true
	}
	if borrow && candidate.preemptionVariant == ReclaimWithoutBorrowing {
		return false
	}
	cq := c.snapshot.ClusterQueue(candidate.wl.ClusterQueue)
	if cache.IsWithinNominalInResources(cq, c.frsNeedPreemption) {
		return false
	}
	// we don't go all the way to the root but only to the lca node
	for node := range cq.PathParentToRoot() {
		if node == candidate.lca {
			break
		}
		if cache.IsWithinNominalInResources(node, c.frsNeedPreemption) {
			return false
		}
	}
	return true
}

// Reset moves the candidate iterator back to the starting position.
// It is required to reset the iterator before each run.
func (c *candidateIterator) Reset() {
	c.runIndex = 0
}
