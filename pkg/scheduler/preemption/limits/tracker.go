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

package limits

import (
	"slices"
	"sort"
	"sync"
	"time"

	"k8s.io/apimachinery/pkg/api/equality"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	"sigs.k8s.io/kueue/pkg/scheduler/preemption/policy"
)

// pruneGracePeriod delays pruning past the window, so that readers whose
// "now" lags slightly behind still find every event they count.
const pruneGracePeriod = time.Minute

// events holds preemption timestamps in ascending order.
type events []time.Time

// insert adds ts while keeping the events sorted. Timestamps usually arrive in
// order, so the insertion point is searched from the tail.
func (e events) insert(ts time.Time) events {
	i := len(e)
	for i > 0 && e[i-1].After(ts) {
		i--
	}
	return slices.Insert(e, i, ts)
}

// eventsAfter returns the events strictly after cutoff. The result shares e's backing array.
func (e events) eventsAfter(cutoff time.Time) events {
	i := sort.Search(len(e), func(i int) bool { return e[i].After(cutoff) })
	return e[i:]
}

// pruneBefore reslices e to drop events before cutoff without copying. Dropped
// elements stay in memory until a later insert reallocates the slice.
func (e events) pruneBefore(cutoff time.Time) events {
	i := sort.Search(len(e), func(i int) bool { return !e[i].Before(cutoff) })
	return e[i:]
}

// limitState tracks preemption events grouped by aggregation key for a single
// PreemptionLimit, alongside the PreemptionLimit they count for and its
// generation when they were last reset.
type limitState struct {
	// limit is the PreemptionLimit as last synced. It is replaced, never
	// modified, so that snapshots can share it.
	limit                  *kueuealpha.PreemptionLimit
	resetGeneration        int64
	eventsByAggregationKey map[policy.PreemptionLimitAggregationKey]events
}

// newLimitState returns the state of limit, without events.
func newLimitState(limit *kueuealpha.PreemptionLimit) limitState {
	return limitState{
		limit:                  trackedCopy(limit),
		resetGeneration:        limit.Generation,
		eventsByAggregationKey: make(map[policy.PreemptionLimitAggregationKey]events),
	}
}

// trackedCopy returns a copy of the parts of limit the tracker relies on,
// leaving out the status and the other metadata.
func trackedCopy(limit *kueuealpha.PreemptionLimit) *kueuealpha.PreemptionLimit {
	return &kueuealpha.PreemptionLimit{
		Name: limit.Name, UID: limit.UID, Generation: limit.Generation,
		Spec: *limit.Spec.DeepCopy(),
	}
}

// isCompatibleWith reports whether the tracked state can continue to be used
// for limit, i.e. whether limit has the same UID, aggregation, and selectors that
// this state was initialized with.
func (ls limitState) isCompatibleWith(limit *kueuealpha.PreemptionLimit) bool {
	return ls.limit.UID == limit.UID &&
		ls.limit.Spec.Aggregation == limit.Spec.Aggregation &&
		equality.Semantic.DeepEqual(ls.limit.Spec.ConfigSelector, limit.Spec.ConfigSelector) &&
		equality.Semantic.DeepEqual(ls.limit.Spec.PreemptorClusterQueueSelector, limit.Spec.PreemptorClusterQueueSelector) &&
		equality.Semantic.DeepEqual(ls.limit.Spec.PreempteeClusterQueueSelector, limit.Spec.PreempteeClusterQueueSelector)
}

func (ls limitState) isLimitRelaxed(limit *kueuealpha.PreemptionLimit) bool {
	return limit.Spec.Limit > ls.limit.Spec.Limit ||
		limit.Spec.WindowSeconds < ls.limit.Spec.WindowSeconds
}

// counts returns the number of events within the window (windowStart, now] of
// the tracked PreemptionLimit, by aggregation key. Aggregation keys with 0 events are
// omitted.
func (ls limitState) counts(now time.Time) policy.PreemptionLimitAggregationCounts {
	counts := make(policy.PreemptionLimitAggregationCounts)
	cutoff := windowStart(now, ls.limit)
	for aggregationKey, e := range ls.eventsByAggregationKey {
		if count := len(e.eventsAfter(cutoff)); count > 0 {
			counts[aggregationKey] = int32(count)
		}
	}
	return counts
}

// PreemptionLimitTracker records the timestamps of confirmed configurable preemptions for
// each PreemptionLimit and aggregation key. It is safe for concurrent use.
type PreemptionLimitTracker struct {
	mu     sync.RWMutex
	limits map[policy.PreemptionLimitReference]limitState
}

// NewPreemptionLimitTracker creates an empty PreemptionLimitTracker.
func NewPreemptionLimitTracker() *PreemptionLimitTracker {
	return &PreemptionLimitTracker{
		limits: make(map[policy.PreemptionLimitReference]limitState),
	}
}

// Record records a preemption at ts for every PreemptionLimit and aggregation key
// pair in consumedLimits. Pairs that are stale for the tracked PreemptionLimit
// (see ConsumedLimit.IsStaleFor) are skipped, so that late records neither
// resurrect a deleted PreemptionLimit nor count against a recreated one or one
// whose aggregation or selectors changed. As a tradeoff, preemptions before the
// first Sync of a new PreemptionLimit are lost.
func (t *PreemptionLimitTracker) Record(ts time.Time, consumedLimits policy.ConsumedLimits) {
	t.mu.Lock()
	defer t.mu.Unlock()
	for limit, consumed := range consumedLimits {
		ls, found := t.limits[limit]
		if found && !consumed.IsStaleFor(ls.limit.UID, ls.resetGeneration) {
			ls.eventsByAggregationKey[consumed.AggregationKey] = ls.eventsByAggregationKey[consumed.AggregationKey].insert(ts)
		}
	}
}

// windowStart returns the exclusive start of the window of limit ending at now.
func windowStart(now time.Time, limit *kueuealpha.PreemptionLimit) time.Time {
	return now.Add(-time.Duration(limit.Spec.WindowSeconds) * time.Second)
}

// Snapshot returns a point-in-time snapshot of the tracked PreemptionLimits
// and their preemptions within each limit's window (windowStart, now].
//
// Because both the PreemptionLimit specs and their recorded events are read
// together from the tracker, a spec change only takes effect in snapshots once
// Sync has applied it (along with any event reset).
func (t *PreemptionLimitTracker) Snapshot(now time.Time) *PreemptionLimitsSnapshot {
	t.mu.RLock()
	defer t.mu.RUnlock()
	snapshot := &PreemptionLimitsSnapshot{
		limitByName:   make(map[policy.PreemptionLimitReference]*kueuealpha.PreemptionLimit, len(t.limits)),
		recorded:      make(preemptionCounts, len(t.limits)),
		earliestEvent: make(map[policy.PreemptionLimitReference]map[policy.PreemptionLimitAggregationKey]time.Time, len(t.limits)),
	}
	for ref, ls := range t.limits {
		snapshot.limitByName[ref] = ls.limit
		cutoff := windowStart(now, ls.limit)
		for aggregationKey, e := range ls.eventsByAggregationKey {
			if inWindow := e.eventsAfter(cutoff); len(inWindow) > 0 {
				snapshot.record(ref, aggregationKey, int32(len(inWindow)), inWindow[0])
			}
		}
	}
	return snapshot
}

// Counts returns the number of preemptions recorded for limit within its
// window (windowStart, now], by aggregation key. Aggregation keys with 0 events are
// omitted, and an untracked limit has no counts.
func (t *PreemptionLimitTracker) Counts(now time.Time, limit policy.PreemptionLimitReference) policy.PreemptionLimitAggregationCounts {
	t.mu.RLock()
	defer t.mu.RUnlock()
	ls, found := t.limits[limit]
	if !found {
		return nil
	}
	return ls.counts(now)
}

// Sync updates the tracker's in-memory representation of limit:
//   - untracked limit: initializes tracking state with no recorded events;
//   - recreated limit (new UID), or aggregation or selector change: updates the
//     stored limit, clears recorded events, and records limit.Generation as
//     the last reset generation;
//   - limit or window change only: updates the stored limit while preserving
//     recorded events and the last reset generation.
//
// Sync returns true when existing limit configuration was updated in a way
// which required to count limits from the clear state or it allows now to
// potentially fit more workloads.
func (t *PreemptionLimitTracker) Sync(limit *kueuealpha.PreemptionLimit) bool {
	ref := policy.PreemptionLimitReference(limit.Name)
	t.mu.Lock()
	defer t.mu.Unlock()
	ls, found := t.limits[ref]
	if !found || !ls.isCompatibleWith(limit) {
		t.limits[ref] = newLimitState(limit)
		return found
	}

	limitRelaxed := ls.isLimitRelaxed(limit)

	ls.limit = trackedCopy(limit)
	t.limits[ref] = ls

	return limitRelaxed
}

// Prune drops recorded events of limit that are older than its window ending at
// now plus pruneGracePeriod, and removes aggregation keys left without events.
// If limit is not tracked, Prune is a no-op.
func (t *PreemptionLimitTracker) Prune(now time.Time, limit policy.PreemptionLimitReference) {
	t.mu.Lock()
	defer t.mu.Unlock()
	ls, found := t.limits[limit]
	if !found {
		return
	}
	cutoff := windowStart(now, ls.limit).Add(-pruneGracePeriod)
	for aggregationKey, e := range ls.eventsByAggregationKey {
		if e = e.pruneBefore(cutoff); len(e) == 0 {
			delete(ls.eventsByAggregationKey, aggregationKey)
		} else {
			ls.eventsByAggregationKey[aggregationKey] = e
		}
	}
}

// Forget stops tracking limit, dropping all its events.
// Returns true if limit was present and got successfully removed.
func (t *PreemptionLimitTracker) Forget(limit policy.PreemptionLimitReference) bool {
	t.mu.Lock()
	defer t.mu.Unlock()

	_, found := t.limits[limit]
	if found {
		delete(t.limits, limit)
	}

	return found
}
