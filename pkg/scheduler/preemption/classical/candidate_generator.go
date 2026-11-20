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
	"maps"
	"slices"
	"sort"
	"time"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/utils/clock"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/resources"
	"sigs.k8s.io/kueue/pkg/workload"
	workloadevict "sigs.k8s.io/kueue/pkg/workload/evict"
)

// CandidateIterator is the common interface for iterating over preemption
// candidates. Two implementations exist: the flat-list iterator and the
// cache-based segment/bucket iterator.
type CandidateIterator interface {
	NoCandidateFromOtherQueues() bool
	NoCandidateForHierarchicalReclaim() bool
	Reset()
	Next(borrow bool) (*workload.Info, string)
}

type candidateIterator struct {
	candidates []*candidateElem
	// borrowCandidates holds the order used by the borrowing pass when
	// AdmissionFairSharing is enabled. A nil value means the borrowing pass uses
	// the same order as candidates.
	borrowCandidates                  []*candidateElem
	runIndex                          int
	frsNeedPreemption                 sets.Set[resources.FlavorResource]
	snapshot                          *schdcache.Snapshot
	noCandidateFromOtherQueues        bool
	noCandidateForHierarchicalReclaim bool
	hierarchicalReclaimCtx            *HierarchicalPreemptionCtx
}

func (c *candidateIterator) NoCandidateFromOtherQueues() bool {
	return c.noCandidateFromOtherQueues
}

func (c *candidateIterator) NoCandidateForHierarchicalReclaim() bool {
	return c.noCandidateForHierarchicalReclaim
}

type candidateElem struct {
	wl *workload.Info
	// lca of this queue and cq (queue to which the new workload is submitted)
	lca *schdcache.CohortSnapshot
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
		return !workloadevict.IsEvicted(workloads[i].wl.Obj)
	})
	return workloads[:firstFalse], workloads[firstFalse:]
}

// NewCandidateIterator creates a new iterator that yields candidate workloads for preemption
// The iterator can be used to perform two independent runs over the list of candidates:
// with and without borrowing. The runs are independent which means that the same candidates
// might be returned for both, but note that the candidates with borrowing are a subset of
// candidates without borrowing.
func NewCandidateIterator(
	hierarchicalReclaimCtx *HierarchicalPreemptionCtx,
	enabledAfs bool,
	frsNeedPreemption sets.Set[resources.FlavorResource],
	snapshot *schdcache.Snapshot,
	clock clock.Clock,
	ordering func(logr.Logger, bool, *workload.Info, *workload.Info, kueue.ClusterQueueReference, time.Time) int,
) CandidateIterator {
	if features.Enabled(features.ClassicalPreemptionCandidateOrderCache) {
		return newCacheCandidateIterator(hierarchicalReclaimCtx, enabledAfs, frsNeedPreemption, snapshot, clock, ordering)
	}
	return newCandidateIterator(hierarchicalReclaimCtx, enabledAfs, frsNeedPreemption, snapshot, clock, ordering)
}

func newCandidateIterator(
	hierarchicalReclaimCtx *HierarchicalPreemptionCtx,
	enabledAfs bool,
	frsNeedPreemption sets.Set[resources.FlavorResource],
	snapshot *schdcache.Snapshot,
	clock clock.Clock,
	ordering func(logr.Logger, bool, *workload.Info, *workload.Info, kueue.ClusterQueueReference, time.Time) int,
) *candidateIterator {
	// Use a single timestamp for all comparisons, like the cache-backed
	// iterator does, so that quotaReservationTime is evaluated consistently.
	now := clock.Now()
	preemptorCQ := hierarchicalReclaimCtx.Cq.Name
	cmp := func(a, b *candidateElem) int {
		return ordering(hierarchicalReclaimCtx.Log, enabledAfs, a.wl, b.wl, preemptorCQ, now)
	}
	sortCandidates := func(candidates []*candidateElem) []*candidateElem {
		return sortCandidateGroup(candidates, enabledAfs, cmp)
	}

	sameQueueCandidates := collectSameQueueCandidates(hierarchicalReclaimCtx)
	hierarchyCandidates, priorityCandidates := collectCandidatesForHierarchicalReclaim(hierarchicalReclaimCtx)

	candidates := evictedFirst(
		sortCandidates(hierarchyCandidates),
		sortCandidates(priorityCandidates),
		sortCandidates(sameQueueCandidates),
	)
	var borrowCandidates []*candidateElem
	if enabledAfs {
		// Only priorityCandidates can be ReclaimWithoutBorrowing: same-CQ
		// candidates are WithinCQ and hierarchy candidates are
		// HiearchicalReclaim. The cache-backed iterator skips them before
		// comparing the heads of the per-ClusterQueue buckets, so do the same
		// here, otherwise an ineligible head hides the next eligible candidate
		// of its ClusterQueue during the merge. Most cycles have no such
		// candidate, so avoid building a second order unless one is present.
		borrowPriorityCandidates := slices.DeleteFunc(slices.Clone(priorityCandidates), func(c *candidateElem) bool {
			return c.preemptionVariant == ReclaimWithoutBorrowing
		})
		if len(borrowPriorityCandidates) != len(priorityCandidates) {
			borrowCandidates = evictedFirst(
				hierarchyCandidates,
				sortCandidates(borrowPriorityCandidates),
				sameQueueCandidates,
			)
		}
	}

	return &candidateIterator{
		runIndex:                          0,
		frsNeedPreemption:                 frsNeedPreemption,
		snapshot:                          snapshot,
		candidates:                        candidates,
		borrowCandidates:                  borrowCandidates,
		noCandidateFromOtherQueues:        len(hierarchyCandidates) == 0 && len(priorityCandidates) == 0,
		noCandidateForHierarchicalReclaim: len(hierarchyCandidates) == 0,
		hierarchicalReclaimCtx:            hierarchicalReclaimCtx,
	}
}

// evictedFirst concatenates the groups with all evicted candidates first,
// preserving the relative order of the groups within each part.
func evictedFirst(groups ...[]*candidateElem) []*candidateElem {
	var evicted, nonEvicted []*candidateElem
	for _, group := range groups {
		evictedPrefix, rest := splitEvicted(group)
		evicted = append(evicted, evictedPrefix...)
		nonEvicted = append(nonEvicted, rest...)
	}
	return append(evicted, nonEvicted...)
}

// sortCandidateGroup orders one group of candidates for a single iterator pass.
// With AdmissionFairSharing the comparator is not transitive across
// ClusterQueues, so the candidates are merged per ClusterQueue instead of being
// sorted as one flat list. Otherwise a plain sort is enough.
func sortCandidateGroup(candidates []*candidateElem, enabledAfs bool, cmp func(a, b *candidateElem) int) []*candidateElem {
	if enabledAfs {
		sortPerClusterQueue(candidates, cmp)
		return candidates
	}
	slices.SortFunc(candidates, cmp)
	return candidates
}

// sortPerClusterQueue orders candidates across ClusterQueues while keeping the
// LocalQueue usage ordering within each of them. ClusterQueue names break ties
// between heads, so the merge is independent of map iteration order.
func sortPerClusterQueue(candidates []*candidateElem, cmp func(a, b *candidateElem) int) {
	byCQ := make(map[kueue.ClusterQueueReference][]*candidateElem, len(candidates))
	for _, candidate := range candidates {
		byCQ[candidate.wl.ClusterQueue] = append(byCQ[candidate.wl.ClusterQueue], candidate)
	}
	queues := make([][]*candidateElem, 0, len(byCQ))
	for _, cq := range slices.Sorted(maps.Keys(byCQ)) {
		slices.SortFunc(byCQ[cq], cmp)
		queues = append(queues, byCQ[cq])
	}
	for i := range candidates {
		best := 0
		for j := range queues {
			if cmp(queues[j][0], queues[best][0]) < 0 {
				best = j
			}
		}
		candidates[i] = queues[best][0]
		queues[best] = queues[best][1:]
		if len(queues[best]) == 0 {
			queues = slices.Delete(queues, best, best+1)
		}
	}
}

// Next allows to iterate over the ordered sequence of candidates, with the reason
// for eviction returned together with a candidate.
func (c *candidateIterator) Next(borrow bool) (*workload.Info, string) {
	candidates := c.candidates
	if borrow && c.borrowCandidates != nil {
		candidates = c.borrowCandidates
	}
	if c.runIndex >= len(candidates) {
		return nil, ""
	}
	candidate := candidates[c.runIndex]
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
	// The candidate cannot be preempted once its ClusterQueue, or a cohort
	// between it and the lca, is back within its nominal quota.
	return !isAnyNodeBelowAncestorWithinNominal(cq, candidate.lca, c.frsNeedPreemption)
}

// isAnyNodeBelowAncestorWithinNominal tells whether cq, or a cohort between cq
// and its ancestor, is within its nominal quota in the given resources. The
// ancestor itself is not checked.
func isAnyNodeBelowAncestorWithinNominal(cq *schdcache.ClusterQueueSnapshot, ancestor *schdcache.CohortSnapshot, frs sets.Set[resources.FlavorResource]) bool {
	if schdcache.IsWithinNominalInResources(cq, frs) {
		return true
	}
	// we don't go all the way to the root but only to the ancestor
	for node := range cq.PathParentToRoot() {
		if node == ancestor {
			break
		}
		if schdcache.IsWithinNominalInResources(node, frs) {
			return true
		}
	}
	return false
}

// Reset moves the candidate iterator back to the starting position.
// It is required to reset the iterator before each run.
func (c *candidateIterator) Reset() {
	c.runIndex = 0
}
