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
	"container/heap"
	"time"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/utils/clock"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/resources"
	"sigs.k8s.io/kueue/pkg/workload"
)

const (
	numSegments         = 6
	minHeapMergeBuckets = 4
)

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
	segmentHeap  bucketHeap
	segmentReady bool
	useHeap      bool
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
	heapOrder                int
}

// bucketHeap merges the current candidate of each ClusterQueue bucket. The
// heap order is stable when the workload comparator ties, matching the linear
// scan it replaces.
type bucketHeap struct {
	buckets []*bucket
	cmp     schdcache.CandidateOrderComparator
}

func (h bucketHeap) Len() int {
	return len(h.buckets)
}

func (h bucketHeap) Less(i, j int) bool {
	cmp := h.cmp(h.buckets[i].top(), h.buckets[j].top())
	if cmp != 0 {
		return cmp < 0
	}
	return h.buckets[i].heapOrder < h.buckets[j].heapOrder
}

func (h bucketHeap) Swap(i, j int) {
	h.buckets[i], h.buckets[j] = h.buckets[j], h.buckets[i]
}

func (h *bucketHeap) Push(x any) {
	h.buckets = append(h.buckets, x.(*bucket))
}

func (h *bucketHeap) Pop() any {
	last := len(h.buckets) - 1
	b := h.buckets[last]
	h.buckets[last] = nil
	h.buckets = h.buckets[:last]
	return b
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

// buildBuckets turns a list of classified ClusterQueues into their evicted and
// non-evicted buckets, one bucket each per CQ.
func buildBuckets(classes []classifiedClusterQueue) (evicted, nonEvicted []*bucket) {
	for _, cc := range classes {
		if len(cc.sortedWorkloads) == 0 {
			continue
		}
		ev, nonEv := cc.sortedWorkloads[:cc.evictedCount], cc.sortedWorkloads[cc.evictedCount:]
		if len(ev) > 0 {
			evicted = append(evicted, newBucket(cc, ev))
		}
		if len(nonEv) > 0 {
			nonEvicted = append(nonEvicted, newBucket(cc, nonEv))
		}
	}
	return evicted, nonEvicted
}

func newBucket(cc classifiedClusterQueue, sortedWorkloads []*workload.Info) *bucket {
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
		if !c.segmentReady {
			c.segmentReady = true
			c.useHeap = len(c.segments[c.segmentIndex]) >= minHeapMergeBuckets
			if c.useHeap {
				c.initSegmentHeap(borrow)
			}
		}
		if c.useHeap {
			for c.segmentHeap.Len() > 0 {
				b := heap.Pop(&c.segmentHeap).(*bucket)
				variant, ok := c.advanceToEligible(b, borrow)
				if !ok {
					continue
				}

				// A bucket's head can change while earlier preemptions update the
				// snapshot. Reinsert it when its new head is no longer the minimum.
				if c.segmentHeap.Len() > 0 && c.cmp(b.top(), c.segmentHeap.buckets[0].top()) > 0 {
					heap.Push(&c.segmentHeap, b)
					continue
				}

				wl := b.top()
				b.advance()
				if _, ok := c.advanceToEligible(b, borrow); ok {
					heap.Push(&c.segmentHeap, b)
				}
				return wl, variant.PreemptionReason()
			}
		} else if wl, variant, ok := c.nextFromSegment(c.segments[c.segmentIndex], borrow); ok {
			return wl, variant.PreemptionReason()
		}
		c.segmentIndex++
		c.segmentReady = false
	}
	return nil, ""
}

// nextFromSegment returns the next valid candidate from a single segment by
// scanning each bucket head. This has lower fixed cost than a heap for the
// small segments selected by minHeapMergeBuckets.
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

func (c *cacheCandidateIterator) initSegmentHeap(borrow bool) {
	buckets := c.segments[c.segmentIndex]
	if cap(c.segmentHeap.buckets) < len(buckets) {
		c.segmentHeap.buckets = make([]*bucket, 0, len(buckets))
	} else {
		c.segmentHeap.buckets = c.segmentHeap.buckets[:0]
	}
	c.segmentHeap.cmp = c.cmp
	for i, b := range buckets {
		b.heapOrder = i
		if _, ok := c.advanceToEligible(b, borrow); ok {
			c.segmentHeap.buckets = append(c.segmentHeap.buckets, b)
		}
	}
	heap.Init(&c.segmentHeap)
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
		cachedWorkload := b.top()
		if _, ok := b.cq.Workloads[workload.Key(cachedWorkload.Obj)]; !ok {
			b.advance()
			continue
		}
		variant := classifyPreemptionVariant(c.hierarchicalReclaimCtx, cachedWorkload, b.hasHierarchicalAdvantage)
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
	c.segmentReady = false
	c.useHeap = false
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
