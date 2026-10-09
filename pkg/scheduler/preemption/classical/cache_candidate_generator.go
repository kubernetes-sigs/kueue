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
	"sigs.k8s.io/kueue/pkg/resources"
	"sigs.k8s.io/kueue/pkg/workload"
)

const numSegments = 6

// cacheCandidateIterator is the cache-backed implementation of CandidateIterator.
// It consumes a per-CQ sorted order from CandidateOrderCache through a
// lazy segment/bucket k-way merge.
type cacheCandidateIterator struct {
	// segments holds, for each of the numSegments consumption stages, the list
	// of per-ClusterQueue buckets that contribute candidates to that stage.
	// Within a segment every bucket belongs to the same category, so the
	// segment's output is the ordered merge of its buckets.
	segments     [numSegments][]*bucket
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

	sorted []*workload.Info
	idx    int
}

func (b *bucket) empty() bool {
	return b.idx >= len(b.sorted)
}

func (b *bucket) top() *workload.Info {
	return b.sorted[b.idx]
}

func (b *bucket) advance() {
	b.idx++
}

// drain marks the bucket exhausted (used by whole-bucket pruning).
func (b *bucket) drain() {
	b.idx = len(b.sorted)
}

// reset rewinds to the starting position for the next borrow run.
func (b *bucket) reset() {
	b.idx = 0
}

// buildBuckets turns a list of classified ClusterQueues into their evicted and
// non-evicted buckets, one bucket each per CQ.
func buildBuckets(classes []classifiedClusterQueue) (evicted, nonEvicted []*bucket) {
	for _, cc := range classes {
		if len(cc.sorted) == 0 {
			continue
		}
		ev, nonEv := cc.sorted[:cc.evictedCount], cc.sorted[cc.evictedCount:]
		if len(ev) > 0 {
			evicted = append(evicted, newBucket(cc, ev))
		}
		if len(nonEv) > 0 {
			nonEvicted = append(nonEvicted, newBucket(cc, nonEv))
		}
	}
	return evicted, nonEvicted
}

func newBucket(cc classifiedClusterQueue, sorted []*workload.Info) *bucket {
	return &bucket{
		cq:                       cc.cq,
		lca:                      cc.lca,
		isSameQueue:              cc.isSameQueue,
		hasHierarchicalAdvantage: cc.hasHierarchicalAdvantage,
		sorted:                   sorted,
	}
}

// noRealCandidate reports whether none of these classes holds a preemptible
// workload, i.e. every workload classifies as Never.
func noRealCandidate(ctx *HierarchicalPreemptionCtx, classes []classifiedClusterQueue) bool {
	for _, cc := range classes {
		for _, wl := range cc.sorted {
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
	attachSorted(cache, cmp, sameQueueClasses, hierarchyClasses, priorityClasses)

	evictedHierarchy, nonEvictedHierarchy := buildBuckets(hierarchyClasses)
	evictedPriority, nonEvictedPriority := buildBuckets(priorityClasses)
	evictedSameQueue, nonEvictedSameQueue := buildBuckets(sameQueueClasses)

	noHierarchyCandidate := noRealCandidate(hierarchicalReclaimCtx, hierarchyClasses)

	return &cacheCandidateIterator{
		segments: [numSegments][]*bucket{
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
func (c *cacheCandidateIterator) nextFromSegment(buckets []*bucket, borrow bool) (*workload.Info, preemptionVariant, bool) {
	var bestBucket *bucket
	var bestVariant preemptionVariant
	for _, b := range buckets {
		variant, ok := c.advanceToEligible(b, borrow)
		if !ok {
			continue
		}
		if bestBucket == nil || c.cmp(b.top(), bestBucket.top()) < 0 {
			bestBucket = b
			bestVariant = variant
		}
	}
	if bestBucket == nil {
		return nil, Never, false
	}
	wl := bestBucket.top()
	bestBucket.advance()
	return wl, bestVariant, true
}

// advanceToEligible skips candidates that cannot be returned in the current run
// and leaves the first eligible candidate at the bucket head.
func (c *cacheCandidateIterator) advanceToEligible(b *bucket, borrow bool) (preemptionVariant, bool) {
	if b.empty() {
		return Never, false
	}
	// Whole-bucket pruning: once a target CQ (or a node on its path up
	// to the lca) is back within nominal, none of its remaining
	// candidates can be preempted, so drop the entire bucket.
	if !b.isSameQueue && c.cqIsPruned(b) {
		b.drain()
		return Never, false
	}

	for !b.empty() {
		variant := classifyPreemptionVariant(c.hierarchicalReclaimCtx, b.top(), b.hasHierarchicalAdvantage)
		if variant != Never && (!borrow || variant != ReclaimWithoutBorrowing) {
			return variant, true
		}
		b.advance()
	}
	return Never, false
}

func (c *cacheCandidateIterator) cqIsPruned(b *bucket) bool {
	if schdcache.IsWithinNominalInResources(b.cq, c.frsNeedPreemption) {
		return true
	}
	for node := range b.cq.PathParentToRoot() {
		if node == b.lca {
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

// attachSorted warms the cycle-level ordering for every distinct classified
// ClusterQueue concurrently, then fills in each class's sorted slice from the
// cache. Warming first makes the subsequent Get calls cache hits; because the
// entries are cycle-scoped, a CQ warmed for one preemptor is already warm for
// the next. The sorted slice is cache-owned and read-only; the classes are
// mutated in place by index.
func attachSorted(cache *schdcache.CandidateOrderCache, cmp schdcache.CandidateOrderComparator, groups ...[]classifiedClusterQueue) {
	seen := make(map[kueue.ClusterQueueReference]struct{})
	var cqs []*schdcache.ClusterQueueSnapshot
	add := func(cq *schdcache.ClusterQueueSnapshot) {
		if _, ok := seen[cq.Name]; ok {
			return
		}
		seen[cq.Name] = struct{}{}
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
			g[i].sorted, g[i].evictedCount = cache.Get(g[i].cq, cmp)
		}
	}
}
