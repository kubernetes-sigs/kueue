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
)

// classifyFn reports whether a live workload is an eligible candidate in the
// current run and returns its preemption variant when so.
type classifyFn func(wl *workload.Info) (preemptionVariant, bool)

// candidateBucket is the common interface for a per-ClusterQueue cursor within
// one segment. Two implementations exist: bucket (plain pass) and partialBucket
// (multi-round, used when the PartialPreemption feature gate is enabled).
type candidateBucket interface {
	empty() bool
	top() *workload.Info
	advance()
	// nextEligible positions the cursor on the next candidate that classify
	// accepts and returns its variant, or ok=false when the bucket is exhausted.
	nextEligible(classify classifyFn) (preemptionVariant, bool)
	// noteConsumed is called by nextFromSegment after a candidate has been
	// confirmed as valid (non-Never, passes borrow filter) and is about to be
	// returned to the caller. partialBucket uses this to set
	// roundHasRemainingCandidate only for entries that actually contribute to
	// preemption progress; bucket ignores it.
	noteConsumed(wl *workload.Info)
	drain()
	reset()
	getSameQueue() bool
	hierarchicalAdvantage() bool
	cqSnapshot() *schdcache.ClusterQueueSnapshot
	lcaSnapshot() *schdcache.CohortSnapshot
}

// cacheCandidateIterator is the cache-backed implementation of CandidateIterator.
// It consumes a per-CQ sorted order from CandidateOrderCache through a
// lazy segment/bucket k-way merge.
type cacheCandidateIterator struct {
	// segments holds, for each of the numSegments consumption stages, the list
	// of per-ClusterQueue buckets that contribute candidates to that stage.
	// Within a segment every bucket belongs to the same category, so the
	// segment's output is the ordered merge of its buckets.
	segments     [numSegments][]candidateBucket
	segmentIndex int
	cmp          schdcache.CandidateOrderComparator

	frsNeedPreemption                 sets.Set[resources.FlavorResource]
	noCandidateFromOtherQueues        bool
	noCandidateForHierarchicalReclaim bool
	hierarchicalReclaimCtx            *HierarchicalPreemptionCtx
}

func (c *cacheCandidateIterator) NoCandidateFromOtherQueues() bool {
	return c.noCandidateFromOtherQueues
}

func (c *cacheCandidateIterator) NoCandidateForHierarchicalReclaim() bool {
	return c.noCandidateForHierarchicalReclaim
}

// bucket is a single ClusterQueue's contribution to one segment: its
// per-ClusterQueue classification (cq, lca, isSameQueue,
// hasHierarchicalAdvantage) plus a forward cursor over that CQ's cycle-level
// ordered candidates.
//
// bucket is used only by cacheCandidateIterator.
type bucket struct {
	cq                       *schdcache.ClusterQueueSnapshot
	lca                      *schdcache.CohortSnapshot
	isSameQueue              bool
	hasHierarchicalAdvantage bool
	sortedWorkloads          []*workload.Info
	idx                      int
}

func (b *bucket) empty() bool {
	return b.idx >= len(b.sortedWorkloads)
}

// top returns the workload at the current cursor position. Callers must ensure
// idx points to a workload that is still present in the snapshot.
func (b *bucket) top() *workload.Info {
	return b.sortedWorkloads[b.idx]
}

// advance moves the bucket cursor to the next cached workload.
// Snapshot membership and eligibility are checked by advanceToEligible.
func (b *bucket) advance() {
	b.idx++
}

// drain marks the bucket exhausted (used by whole-bucket pruning).
func (b *bucket) drain() {
	b.idx = len(b.sortedWorkloads)
}

// reset rewinds to the starting position for the next borrow run.
func (b *bucket) reset() {
	b.idx = 0
}

// nextEligible walks forward, skipping entries removed from the snapshot and those
// classify rejects, and stops on the first eligible candidate.
func (b *bucket) nextEligible(classify classifyFn) (preemptionVariant, bool) {
	for b.idx < len(b.sortedWorkloads) {
		cached := b.sortedWorkloads[b.idx]
		if b.cq != nil {
			if _, ok := b.cq.Workloads[workload.Key(cached.Obj)]; !ok {
				b.idx++
				continue
			}
		}
		if variant, ok := classify(cached); ok {
			return variant, true
		}
		b.idx++
	}
	return Never, false
}

func (b *bucket) noteConsumed(_ *workload.Info)               {}
func (b *bucket) getSameQueue() bool                          { return b.isSameQueue }
func (b *bucket) hierarchicalAdvantage() bool                 { return b.hasHierarchicalAdvantage }
func (b *bucket) cqSnapshot() *schdcache.ClusterQueueSnapshot { return b.cq }
func (b *bucket) lcaSnapshot() *schdcache.CohortSnapshot      { return b.lca }

// partialBucket does a multi-round scan for partial-preemption workloads: each
// round walks the slice from the top, and if the round returned a partial
// candidate another round runs from the top to shed it further. Candidates are
// never skipped by their pending state — a victim that still carries a reclaim
// target keeps taking part in the shedding loop; eligibility is decided solely by
// the classify callback against the live snapshot state. As a lower band's
// candidates are fully preempted they vanish from the snapshot and the scan moves
// on to what remains.
//
// partialBucket is used only for non-evicted buckets when the PartialPreemption
// feature gate is enabled.
type partialBucket struct {
	cq                       *schdcache.ClusterQueueSnapshot
	lca                      *schdcache.CohortSnapshot
	isSameQueue              bool
	hasHierarchicalAdvantage bool

	sortedWorkloads []*workload.Info
	idx             int
	// bandStart is the index of the first candidate returned in the current round,
	// or -1 before one is returned. It anchors the priority band for inCurrentBand.
	bandStart int
	// roundHasRemainingCandidate records that the current round returned a partial
	// candidate, so another round runs from the top to shed it further.
	roundHasRemainingCandidate bool
	log                        logr.Logger
}

// empty reports true only when the cursor has reached the end and no partial
// candidate has been flagged for another round.
func (b *partialBucket) empty() bool {
	return b.idx >= len(b.sortedWorkloads) && !b.roundHasRemainingCandidate
}

// top returns the live-snapshot state of the current candidate, or nil if the
// workload has been removed from the snapshot (e.g. already fully preempted).
// The caller must check !empty() first. top is read-only.
func (b *partialBucket) top() *workload.Info {
	cached := b.sortedWorkloads[b.idx]
	if b.cq != nil {
		// nil when the workload was removed from the snapshot (already fully
		// preempted), which signals skip this entry.
		return b.cq.Workloads[workload.Key(cached.Obj)]
	}
	return cached
}

// advance moves the cursor one entry forward. It never skips or rolls over; that
// is nextEligible's job.
func (b *partialBucket) advance() {
	b.idx++
}

// inCurrentBand reports whether sortedWorkloads[i] has the same effective priority
// (base priority plus any PriorityBoost) as the first candidate returned in the
// current round. It returns true before that first candidate is returned
// (bandStart < 0). Banding on EffectivePriority keeps bands aligned with
// CandidatesOrdering, which sorts candidates by EffectivePriority too.
func (b *partialBucket) inCurrentBand(i int) bool {
	if b.bandStart < 0 {
		return true
	}
	return priority.EffectivePriority(b.log, b.sortedWorkloads[i].Obj) == priority.EffectivePriority(b.log, b.sortedWorkloads[b.bandStart].Obj)
}

// nextEligible positions the cursor on the next in-band candidate that classify
// accepts, returning its variant. It skips entries removed from the snapshot or
// that classify rejects. A round walks the current priority band from the top; when
// the band is exhausted it rolls over to a fresh round from the top if the round
// returned a partial candidate, otherwise the bucket is done. Grouping each round by
// priority band means a lower-priority band is fully preempted (every victim shrunk to
// its floor and then evicted whole) before any higher-priority band is touched.
func (b *partialBucket) nextEligible(classify classifyFn) (preemptionVariant, bool) {
	for {
		for b.idx < len(b.sortedWorkloads) && b.inCurrentBand(b.idx) {
			live := b.top()
			if live == nil {
				b.idx++
				continue
			}
			variant, ok := classify(live)
			if !ok {
				b.idx++
				continue
			}
			if b.bandStart < 0 {
				b.bandStart = b.idx
			}
			return variant, true
		}
		switch {
		case b.roundHasRemainingCandidate:
			b.idx = 0
			b.bandStart = -1
			b.roundHasRemainingCandidate = false
		case b.idx >= len(b.sortedWorkloads):
			return Never, false
		default:
			b.bandStart = -1
		}
	}
}

// noteConsumed flags another round when the returned candidate is a
// partial-preemption job. The rollover itself happens in the next nextEligible.
func (b *partialBucket) noteConsumed(wl *workload.Info) {
	if wl != nil && wl.IsPartialPreemptionJob {
		b.roundHasRemainingCandidate = true
	}
}

// drain permanently exhausts the bucket (whole-bucket pruning).
func (b *partialBucket) drain() {
	b.idx = len(b.sortedWorkloads)
	b.roundHasRemainingCandidate = false
}

// reset rewinds to the starting position for the next round or borrow run. The
// segment's shrink/evict phase is tracked by the iterator, so reset does not touch it.
func (b *partialBucket) reset() {
	b.idx = 0
	b.bandStart = -1
	b.roundHasRemainingCandidate = false
}

func (b *partialBucket) getSameQueue() bool                          { return b.isSameQueue }
func (b *partialBucket) hierarchicalAdvantage() bool                 { return b.hasHierarchicalAdvantage }
func (b *partialBucket) cqSnapshot() *schdcache.ClusterQueueSnapshot { return b.cq }
func (b *partialBucket) lcaSnapshot() *schdcache.CohortSnapshot      { return b.lca }

// buildBuckets turns classified ClusterQueues into their evicted and non-evicted
// buckets, one of each per CQ. Each CQ's candidates are already sorted by
// CandidatesOrdering, which puts the evicted ones first (evictedCount of them); a
// pending preemption target is NOT a sort key, so such candidates sit at their
// natural priority position among the non-evicted ones:
//
//	[ evicted | normal, by priority (pending-reclaim interleaved) ]
//
// The evicted bucket is always a single-pass bucket (walked once, ahead of the
// non-evicted ones). The non-evicted bucket is a multi-round partialBucket when the
// PartialPreemption gate is on, otherwise a single-pass bucket.
//
// When the gate is on, the two buckets deliberately OVERLAP on the pending-reclaim
// candidates: the evicted bucket covers the evicted prefix PLUS every pending-reclaim
// candidate (picked out of the tail by a linear scan), while the non-evicted bucket
// starts at evictedCount and keeps those pending candidates at their natural position.
// A pending-reclaim victim is thus the one candidate that lives in both buckets - it is
// visited first in the single-pass evicted bucket (so a victim that already has a
// reclaim target is picked up and its existing target reused, not a fresh victim), and
// then it keeps taking part in the ordinary multi-round shedding loop in the
// non-evicted bucket for whatever is still owed below that pending target - at its
// natural priority position, not ahead of lower-priority victims. The shared
// workload.Info is read-only, so no copy is needed; the evicted bucket's slice is
// freshly allocated because the pending candidates are no longer contiguous with the
// evicted prefix.
func buildBuckets(log logr.Logger, classes []classifiedClusterQueue) (evicted, nonEvicted []candidateBucket) {
	partial := features.Enabled(features.PartialPreemption)
	for _, cc := range classes {
		if len(cc.sortedWorkloads) == 0 {
			continue
		}
		noEvictedSorted := cc.sortedWorkloads[cc.evictedCount:]
		// Cap the capacity so append allocates a fresh backing array instead of
		// overwriting the non-evicted entries that follow the evicted prefix.
		evictedSorted := cc.sortedWorkloads[:cc.evictedCount:cc.evictedCount]
		if partial {
			for _, wl := range noEvictedSorted {
				if wl.HasPendingPreemptionTarget {
					evictedSorted = append(evictedSorted, wl)
				}
			}
		}
		if len(evictedSorted) > 0 {
			evicted = append(evicted, newBucket(log, cc, evictedSorted, false))
		}
		if len(noEvictedSorted) > 0 {
			nonEvicted = append(nonEvicted, newBucket(log, cc, noEvictedSorted, partial))
		}
	}
	return evicted, nonEvicted
}

// newBucket builds one bucket for the given sorted slice: a multi-round
// partialBucket when partial is set, otherwise a plain single-round bucket.
func newBucket(log logr.Logger, cc classifiedClusterQueue, sortedWorkloads []*workload.Info, partial bool) candidateBucket {
	if partial {
		return &partialBucket{
			cq:                       cc.cq,
			lca:                      cc.lca,
			isSameQueue:              cc.isSameQueue,
			hasHierarchicalAdvantage: cc.hasHierarchicalAdvantage,
			sortedWorkloads:          sortedWorkloads,
			bandStart:                -1,
			log:                      log,
		}
	}
	return &bucket{
		cq:                       cc.cq,
		lca:                      cc.lca,
		isSameQueue:              cc.isSameQueue,
		hasHierarchicalAdvantage: cc.hasHierarchicalAdvantage,
		sortedWorkloads:          sortedWorkloads,
	}
}

// noRealCandidate reports whether none of these classes holds a preemptible
// workload, i.e. every workload classifies as Never.
func noRealCandidate(ctx *HierarchicalPreemptionCtx, classes []classifiedClusterQueue) bool {
	for _, cc := range classes {
		for _, cachedWl := range cc.sortedWorkloads {
			wl, ok := cc.cq.Workloads[workload.Key(cachedWl.Obj)]
			if !ok {
				continue
			}
			if classifyPreemptionVariant(ctx, wl, cc.hasHierarchicalAdvantage) != Never {
				return false
			}
		}
	}
	return true
}

func newCacheCandidateIterator(
	hierarchicalReclaimCtx *HierarchicalPreemptionCtx,
	enabledAfs bool,
	frsNeedPreemption sets.Set[resources.FlavorResource],
	snapshot *schdcache.Snapshot,
	clock clock.Clock,
	ordering func(logr.Logger, bool, *workload.Info, *workload.Info, kueue.ClusterQueueReference, time.Time) int,
) *cacheCandidateIterator {
	now := clock.Now()
	preemptorCQ := hierarchicalReclaimCtx.Cq.Name
	cmp := func(a, b *workload.Info) int {
		return ordering(hierarchicalReclaimCtx.Log, enabledAfs, a, b, preemptorCQ, now)
	}
	cache := snapshot.CandidateOrder()

	sameQueueClasses := collectSameQueueCQ(hierarchicalReclaimCtx)
	hierarchyClasses, priorityClasses := collectHierarchicalReclaimCQs(hierarchicalReclaimCtx)
	attachSortedWorkloads(cache, cmp, sameQueueClasses, hierarchyClasses, priorityClasses)

	evictedHierarchy, nonEvictedHierarchy := buildBuckets(hierarchicalReclaimCtx.Log, hierarchyClasses)
	evictedPriority, nonEvictedPriority := buildBuckets(hierarchicalReclaimCtx.Log, priorityClasses)
	evictedSameQueue, nonEvictedSameQueue := buildBuckets(hierarchicalReclaimCtx.Log, sameQueueClasses)

	noHierarchyCandidate := noRealCandidate(hierarchicalReclaimCtx, hierarchyClasses)

	return &cacheCandidateIterator{
		segments: [numSegments][]candidateBucket{
			evictedHierarchy, evictedPriority, evictedSameQueue,
			nonEvictedHierarchy, nonEvictedPriority, nonEvictedSameQueue,
		},
		cmp:                               cmp,
		frsNeedPreemption:                 frsNeedPreemption,
		noCandidateFromOtherQueues:        noHierarchyCandidate && noRealCandidate(hierarchicalReclaimCtx, priorityClasses),
		noCandidateForHierarchicalReclaim: noHierarchyCandidate,
		hierarchicalReclaimCtx:            hierarchicalReclaimCtx,
	}
}

func (c *cacheCandidateIterator) Next(borrow bool) (*workload.Info, string) {
	for c.segmentIndex < numSegments {
		if wl, variant, ok := c.nextFromSegment(c.segments[c.segmentIndex], borrow); ok {
			return wl, variant.PreemptionReason()
		}
		c.segmentIndex++
	}
	return nil, ""
}

// nextFromSegment returns the next valid candidate from a single segment, merging
// the segment's per-CQ buckets on the fly. It selects the current minimum bucket
// head by CandidatesOrdering, scanning the buckets on each pop.
//
// TODO: each pop costs O(K), where K is the number of candidate ClusterQueues, so
// merging a segment is O(K×L) over its L candidates. Candidates are usually few, so
// this simple scan is enough; if needed we may later heap or merge-sort the bucket
// tops to lower the per-pop cost.
func (c *cacheCandidateIterator) nextFromSegment(seg []candidateBucket, borrow bool) (*workload.Info, preemptionVariant, bool) {
	var best candidateBucket
	var bestVariant preemptionVariant
	for _, b := range seg {
		variant, ok := c.advanceToEligible(b, borrow)
		if !ok {
			continue
		}
		if best == nil || c.cmp(b.top(), best.top()) < 0 {
			best = b
			bestVariant = variant
		}
	}
	if best == nil {
		return nil, Never, false
	}
	wl := best.top()
	best.advance()
	// advanceToEligible guarantees that the selected workload is valid, so note consumed.
	best.noteConsumed(wl)
	return wl, bestVariant, true
}

// advanceToEligible skips candidates that cannot be returned in the current run
// and leaves the first eligible candidate at the bucket head.
func (c *cacheCandidateIterator) advanceToEligible(b candidateBucket, borrow bool) (preemptionVariant, bool) {
	if b.empty() {
		return Never, false
	}
	// Whole-bucket pruning: once a target CQ (or a node on its path up
	// to the lca) is back within nominal, none of its remaining
	// candidates can be preempted, so drop the entire bucket.
	if !b.getSameQueue() && c.cqIsPruned(b) {
		b.drain()
		return Never, false
	}

	return b.nextEligible(func(wl *workload.Info) (preemptionVariant, bool) {
		variant := classifyPreemptionVariant(c.hierarchicalReclaimCtx, wl, b.hierarchicalAdvantage())
		if variant == Never {
			return Never, false
		}
		if borrow && variant == ReclaimWithoutBorrowing {
			return Never, false
		}
		return variant, true
	})
}

func (c *cacheCandidateIterator) cqIsPruned(b candidateBucket) bool {
	cq := b.cqSnapshot()
	if schdcache.IsWithinNominalInResources(cq, c.frsNeedPreemption) {
		return true
	}
	lca := b.lcaSnapshot()
	for node := range cq.PathParentToRoot() {
		if node == lca {
			break
		}
		if schdcache.IsWithinNominalInResources(node, c.frsNeedPreemption) {
			return true
		}
	}
	return false
}

func (c *cacheCandidateIterator) Reset() {
	c.segmentIndex = 0
	for _, seg := range c.segments {
		for _, b := range seg {
			b.reset()
		}
	}
}

// attachSortedWorkloads warms the cycle-level ordering for every distinct
// classified ClusterQueue concurrently, then fills in each class's
// sortedWorkloads slice from the cache. Warming first makes the subsequent Get
// calls cache hits.
func attachSortedWorkloads(cache *schdcache.CandidateOrderCache, cmp schdcache.CandidateOrderComparator, groups ...[]classifiedClusterQueue) {
	seen := sets.New[kueue.ClusterQueueReference]()
	var cqs []*schdcache.ClusterQueueSnapshot
	add := func(cq *schdcache.ClusterQueueSnapshot) {
		if seen.Has(cq.Name) {
			return
		}
		seen.Insert(cq.Name)
		cqs = append(cqs, cq)
	}
	for _, g := range groups {
		for i := range g {
			add(g[i].cq)
		}
	}
	cache.Warm(cqs, cmp)

	for _, g := range groups {
		for i := range g {
			g[i].sortedWorkloads, g[i].evictedCount = cache.Get(g[i].cq, cmp)
		}
	}
}
