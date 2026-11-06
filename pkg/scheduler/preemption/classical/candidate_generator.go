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
	"sigs.k8s.io/kueue/pkg/util/priority"
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

const (
	// numSegments is the fixed slot count shared by both iterator implementations:
	// an evicted half and a non-evicted half, each split into the three candidate
	// sources hierarchy/priority/same-queue.
	numSegments = 6
	// numEvictedGroups is the evicted half, i.e. the leading single-pass slots.
	numEvictedGroups = numSegments / 2
)

type candidateIterator struct {
	// segments is a fixed 6-slot layout: slots 0-2 hold the evicted candidates of
	// hierarchy/priority/same-queue, slots 3-5 their non-evicted candidates. Slots
	// may be nil. Next returns candidates slot by slot.
	segments     [numSegments][]*candidateElem
	segmentIndex int
	// singleRoundGroups is how many leading slots are walked with a single pass. The
	// rest are walked multi-round.
	singleRoundGroups int
	// cursor is the index into the current slot. bandStart is the index of the first
	// candidate returned in the current round, or -1 before one is returned.
	cursor    int
	bandStart int
	// roundHasRemainingCandidate records that the current round returned a partial
	// candidate, so another round runs to shed it further.
	roundHasRemainingCandidate        bool
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

// splitEvicted splits one sorted candidate slice into the two groups the 6-slot
// layout needs. Candidates arrive sorted by CandidatesOrdering, which puts the
// evicted ones first; a pending preemption target is NOT a sort key, so such
// candidates sit at their natural priority position among the non-evicted ones:
//
//	[ evicted | normal, by priority (pending-reclaim interleaved) ]
//
// nonEvicted is the whole non-evicted part, left in that natural order. evicted is
// the evicted prefix PLUS every pending-reclaim candidate, picked out of the
// non-evicted part by a linear scan. So a pending-reclaim candidate is placed in
// BOTH groups on purpose - it is the one candidate that lives at both ends of the
// layout:
//   - evicted feeds the single-pass slots (walked once, ahead of everything else),
//     which forces a victim that already has a reclaim target to be revisited first so
//     its existing target is reused instead of a fresh victim being chosen.
//   - nonEvicted feeds the multi-round slots, so after that first reuse the same victim
//     keeps taking part in the ordinary shedding loop for whatever is still owed below
//     the pending target - at its natural priority position, not ahead of lower-priority
//     victims.
//
// A pending candidate therefore appears in both groups by pointer; candidateElem is
// never mutated, so no copy is needed. Because the pending candidates are no longer
// contiguous with the evicted prefix, evicted is a freshly allocated slice rather
// than a reslice.
func splitEvicted(workloads []*candidateElem) (evicted, nonEvicted []*candidateElem) {
	firstNonEvicted := sort.Search(len(workloads), func(i int) bool {
		return !workloadevict.IsEvicted(workloads[i].wl.Obj)
	})
	nonEvicted = workloads[firstNonEvicted:]
	evicted = workloads[:firstNonEvicted:firstNonEvicted]
	for _, c := range nonEvicted {
		if c.wl.HasPendingPreemptionTarget {
			evicted = append(evicted, c)
		}
	}
	return evicted, nonEvicted
}

// buildSegments lays each sorted group into the fixed 6-slot layout: slots 0-2 are
// the evicted candidates of hierarchy/priority/same-queue, slots 3-5 their
// non-evicted candidates. A slot is empty when its group is absent.
func buildSegments(hierarchy, priority, sameQueue []*candidateElem) [numSegments][]*candidateElem {
	evictedH, nonEvictedH := splitEvicted(hierarchy)
	evictedP, nonEvictedP := splitEvicted(priority)
	evictedS, nonEvictedS := splitEvicted(sameQueue)
	return [numSegments][]*candidateElem{evictedH, evictedP, evictedS, nonEvictedH, nonEvictedP, nonEvictedS}
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
	sameQueueCandidates := collectSameQueueCandidates(hierarchicalReclaimCtx)
	hierarchyCandidates, priorityCandidates := collectCandidatesForHierarchicalReclaim(hierarchicalReclaimCtx)
	slices.SortFunc(sameQueueCandidates, func(a, b *candidateElem) int {
		return ordering(hierarchicalReclaimCtx.Log, enabledAfs, a.wl, b.wl, hierarchicalReclaimCtx.Cq.Name, clock.Now())
	})
	slices.SortFunc(priorityCandidates, func(a, b *candidateElem) int {
		return ordering(hierarchicalReclaimCtx.Log, enabledAfs, a.wl, b.wl, hierarchicalReclaimCtx.Cq.Name, clock.Now())
	})
	slices.SortFunc(hierarchyCandidates, func(a, b *candidateElem) int {
		return ordering(hierarchicalReclaimCtx.Log, enabledAfs, a.wl, b.wl, hierarchicalReclaimCtx.Cq.Name, clock.Now())
	})
	segments := buildSegments(hierarchyCandidates, priorityCandidates, sameQueueCandidates)
	noCandidateFromOtherQueues := len(hierarchyCandidates) == 0 && len(priorityCandidates) == 0

	// The leading numEvictedGroups slots (the evicted half) are always walked with a
	// single pass. With partial preemption on, the remaining slots (the non-evicted
	// half) need multiple rounds so a partial victim can be shed further across rounds;
	// with it off every slot is single-pass, degenerating to the original whole-job
	// forward scan.
	singleRoundGroups := numEvictedGroups
	if !features.Enabled(features.PartialPreemption) {
		singleRoundGroups = len(segments)
	}

	return &candidateIterator{
		segments:                          segments,
		singleRoundGroups:                 singleRoundGroups,
		bandStart:                         -1,
		frsNeedPreemption:                 frsNeedPreemption,
		snapshot:                          snapshot,
		noCandidateFromOtherQueues:        noCandidateFromOtherQueues,
		noCandidateForHierarchicalReclaim: len(hierarchyCandidates) == 0,
		hierarchicalReclaimCtx:            hierarchicalReclaimCtx,
	}
}

// inCurrentBand reports whether seg[i] has the same effective priority (base
// priority plus any PriorityBoost) as the first candidate returned in the
// current round. It returns true before that first candidate is returned
// (bandStart < 0). Banding on EffectivePriority keeps bands aligned with
// CandidatesOrdering, which sorts candidates by EffectivePriority too.
func (c *candidateIterator) inCurrentBand(seg []*candidateElem, i int) bool {
	if c.bandStart < 0 {
		return true
	}
	log := c.hierarchicalReclaimCtx.Log
	return priority.EffectivePriority(log, seg[i].wl.Obj) == priority.EffectivePriority(log, seg[c.bandStart].wl.Obj)
}

// Next returns the next candidate, walking the slots in order. When a slot is
// exhausted it advances to the next one and resets the per-slot state.
func (c *candidateIterator) Next(borrow bool) (*workload.Info, string) {
	for c.segmentIndex < len(c.segments) {
		seg := c.segments[c.segmentIndex]
		var current *workload.Info
		var reason string
		var ok bool
		if c.segmentIndex < c.singleRoundGroups {
			current, reason, ok = c.nextSingleRound(seg, borrow)
		} else {
			current, reason, ok = c.nextMultiRound(seg, borrow)
		}
		if ok {
			return current, reason
		}
		c.segmentIndex++
		c.cursor = 0
		c.bandStart = -1
		c.roundHasRemainingCandidate = false
	}
	return nil, ""
}

// nextSingleRound walks seg forward once, returning the next valid candidate, or
// ok=false when the slot is exhausted.
func (c *candidateIterator) nextSingleRound(seg []*candidateElem, borrow bool) (*workload.Info, string, bool) {
	for c.cursor < len(seg) {
		candidate := seg[c.cursor]
		c.cursor++
		current := c.currentWorkload(candidate)
		if current == nil || !c.candidateIsValid(current, candidate, borrow) {
			continue
		}
		return current, candidate.preemptionVariant.PreemptionReason(), true
	}
	return nil, "", false
}

// nextMultiRound returns the next candidate from a multi-round slot, or ok=false
// when the slot is drained. A round walks from the top of the slot until it leaves
// the current priority band, yielding every valid candidate in that band. If the
// round returned a partial-preemption candidate, it runs another round from the top
// (so that candidate is shed further) before advancing; otherwise the slot is drained.
// Processing a whole band to completion before advancing to the next (higher-priority)
// band is what lets getTargets shrink every victim in the lower band to its floor and
// then evict it whole before any higher-priority victim is touched.
func (c *candidateIterator) nextMultiRound(seg []*candidateElem, borrow bool) (*workload.Info, string, bool) {
	for {
		for c.cursor < len(seg) {
			if !c.inCurrentBand(seg, c.cursor) {
				break
			}
			candidate := seg[c.cursor]
			c.cursor++
			current := c.currentWorkload(candidate)
			if current == nil || !c.candidateIsValid(current, candidate, borrow) {
				continue
			}
			if c.bandStart < 0 {
				c.bandStart = c.cursor - 1
			}
			if current.IsPartialPreemptionJob {
				c.roundHasRemainingCandidate = true
			}
			return current, candidate.preemptionVariant.PreemptionReason(), true
		}
		switch {
		case c.roundHasRemainingCandidate:
			c.cursor = 0
			c.bandStart = -1
			c.roundHasRemainingCandidate = false
		case c.cursor >= len(seg):
			return nil, "", false
		default:
			c.bandStart = -1
		}
	}
}

// candidateIsValid checks if candidate is valid,
// as eg. some candidates can only be considered without borrowing
// Also, preemption of candidates might invalidate other candidates
func (c *candidateIterator) candidateIsValid(wl *workload.Info, candidate *candidateElem, borrow bool) bool {
	if c.hierarchicalReclaimCtx.Cq.Name == wl.ClusterQueue {
		return true
	}
	if borrow && candidate.preemptionVariant == ReclaimWithoutBorrowing {
		return false
	}
	cq := c.snapshot.ClusterQueue(wl.ClusterQueue)
	if schdcache.IsWithinNominalInResources(cq, c.frsNeedPreemption) {
		return false
	}
	// we don't go all the way to the root but only to the lca node
	for node := range cq.PathParentToRoot() {
		if node == candidate.lca {
			break
		}
		if schdcache.IsWithinNominalInResources(node, c.frsNeedPreemption) {
			return false
		}
	}
	return true
}

// Reset moves the candidate iterator back to the starting position so the next run
// (borrow / non-borrow) walks the same segments from the top. Segments carry no
// per-run state, so only the cursors and the round flag need clearing.
func (c *candidateIterator) Reset() {
	c.segmentIndex = 0
	c.cursor = 0
	c.bandStart = -1
	c.roundHasRemainingCandidate = false
}

func (c *candidateIterator) currentWorkload(candidate *candidateElem) *workload.Info {
	cq := c.snapshot.ClusterQueue(candidate.wl.ClusterQueue)
	if cq == nil {
		return nil
	}
	return cq.Workloads[workload.Key(candidate.wl.Obj)]
}
