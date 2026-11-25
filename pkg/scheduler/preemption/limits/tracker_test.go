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
	"sync"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	"sigs.k8s.io/kueue/pkg/scheduler/preemption/policy"
	utiltestingalpha "sigs.k8s.io/kueue/pkg/util/testing/v1alpha1"
)

type limitEvents map[*kueuealpha.PreemptionLimit]AggregationEvents

// newTrackerWithEvents creates a PreemptionLimitTracker populated via its public
// Sync and Record APIs with the given limits and recorded timestamps.
func newTrackerWithEvents(initial limitEvents) *PreemptionLimitTracker {
	tracker := NewPreemptionLimitTracker()
	for limit, aggEvents := range initial {
		tracker.Sync(limit)
		ref := policy.PreemptionLimitReference(limit.Name)
		for aggKey, timestamps := range aggEvents {
			consumed := policy.ConsumedLimits{ref: policy.NewConsumedLimit(limit, aggKey)}
			for _, ts := range timestamps {
				tracker.Record(ts, consumed)
			}
		}
	}
	return tracker
}

// selector returns a LabelSelector matching the label key=value.
func selector(key, value string) *metav1.LabelSelector {
	return &metav1.LabelSelector{MatchLabels: map[string]string{key: value}}
}

func TestPreemptionLimitTrackerSnapshot(t *testing.T) {
	now := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	cutoff1m := now.Add(-60 * time.Second)
	perCQLimit1m := utiltestingalpha.MakePreemptionLimit("per-cq", kueuealpha.ByPreemptorCQLimitAggregation).
		Generation(1).WindowSeconds(60).Obj()
	globalLimit1h := utiltestingalpha.MakePreemptionLimit("global", kueuealpha.GlobalLimitAggregation).
		Generation(1).WindowSeconds(3600).Obj()

	type earliestEvents = map[policy.PreemptionLimitReference]map[policy.PreemptionLimitAggregationKey]time.Time

	cases := map[string]struct {
		initial           limitEvents
		wantLimits        []*kueuealpha.PreemptionLimit
		wantRecorded      preemptionCounts
		wantEarliestEvent earliestEvents
	}{
		"empty tracker gives an empty snapshot": {},
		"events before the window are not counted": {
			initial: limitEvents{
				perCQLimit1m: {
					"cq-a": {cutoff1m.Add(-time.Hour), cutoff1m.Add(-time.Nanosecond), cutoff1m.Add(time.Nanosecond), now},
				},
			},
			wantLimits:        []*kueuealpha.PreemptionLimit{perCQLimit1m},
			wantRecorded:      preemptionCounts{"per-cq": {"cq-a": 2}},
			wantEarliestEvent: earliestEvents{"per-cq": {"cq-a": cutoff1m.Add(time.Nanosecond)}},
		},
		"event at the start of the window is not counted": {
			initial: limitEvents{
				perCQLimit1m: {"cq-a": {cutoff1m}},
			},
			wantLimits: []*kueuealpha.PreemptionLimit{perCQLimit1m},
		},
		"event at the end of the window is counted": {
			initial: limitEvents{
				perCQLimit1m: {"cq-a": {now}},
			},
			wantLimits:        []*kueuealpha.PreemptionLimit{perCQLimit1m},
			wantRecorded:      preemptionCounts{"per-cq": {"cq-a": 1}},
			wantEarliestEvent: earliestEvents{"per-cq": {"cq-a": now}},
		},
		"every aggregation key of a limit is counted": {
			initial: limitEvents{
				perCQLimit1m: {
					"cq-a": {now},
					"cq-b": {now.Add(-10 * time.Second), now},
				},
			},
			wantLimits:        []*kueuealpha.PreemptionLimit{perCQLimit1m},
			wantRecorded:      preemptionCounts{"per-cq": {"cq-a": 1, "cq-b": 2}},
			wantEarliestEvent: earliestEvents{"per-cq": {"cq-a": now, "cq-b": now.Add(-10 * time.Second)}},
		},
		"each limit counts within its own window": {
			initial: limitEvents{
				perCQLimit1m:  {"cq-a": {now.Add(-30 * time.Minute)}},
				globalLimit1h: {policy.GlobalAggregationKey: {now.Add(-2 * time.Hour), now.Add(-30 * time.Minute)}},
			},
			wantLimits:        []*kueuealpha.PreemptionLimit{globalLimit1h, perCQLimit1m},
			wantRecorded:      preemptionCounts{"global": {policy.GlobalAggregationKey: 1}},
			wantEarliestEvent: earliestEvents{"global": {policy.GlobalAggregationKey: now.Add(-30 * time.Minute)}},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			tracker := newTrackerWithEvents(tc.initial)
			got := tracker.Snapshot(now)
			wantLimitByName := make(map[policy.PreemptionLimitReference]*kueuealpha.PreemptionLimit, len(tc.wantLimits))
			for _, l := range tc.wantLimits {
				wantLimitByName[policy.PreemptionLimitReference(l.Name)] = trackedCopy(l)
			}
			want := &PreemptionLimitsSnapshot{
				limitByName:   wantLimitByName,
				recorded:      tc.wantRecorded,
				earliestEvent: tc.wantEarliestEvent,
			}
			if diff := cmp.Diff(want, got, cmp.AllowUnexported(PreemptionLimitsSnapshot{}), cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("Unexpected snapshot (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestPreemptionLimitTrackerCounts(t *testing.T) {
	now := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	cutoff1m := now.Add(-60 * time.Second)
	perCQLimit1m := utiltestingalpha.MakePreemptionLimit("per-cq", kueuealpha.ByPreemptorCQLimitAggregation).
		Generation(1).WindowSeconds(60).Obj()
	globalLimit1m := utiltestingalpha.MakePreemptionLimit("global", kueuealpha.GlobalLimitAggregation).
		Generation(1).WindowSeconds(60).Obj()

	cases := map[string]struct {
		initial limitEvents
		want    policy.PreemptionLimitAggregationCounts
	}{
		"only the counts of the given limit within its window are returned": {
			initial: limitEvents{
				perCQLimit1m: {
					"cq-a": {cutoff1m.Add(-time.Hour), cutoff1m.Add(-time.Nanosecond), cutoff1m, cutoff1m.Add(time.Nanosecond), now},
					"cq-b": {now},
				},
				globalLimit1m: {policy.GlobalAggregationKey: {now}},
			},
			want: policy.PreemptionLimitAggregationCounts{"cq-a": 2, "cq-b": 1},
		},
		"tracked limit without events has no counts": {
			initial: limitEvents{
				perCQLimit1m:  nil,
				globalLimit1m: {policy.GlobalAggregationKey: {now}},
			},
		},
		"untracked limit has no counts": {
			initial: limitEvents{
				globalLimit1m: {policy.GlobalAggregationKey: {now}},
			},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			tracker := newTrackerWithEvents(tc.initial)
			got := tracker.Counts(now, policy.PreemptionLimitReference(perCQLimit1m.Name))
			if diff := cmp.Diff(tc.want, got, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("Unexpected counts (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestPreemptionLimitTrackerRecord(t *testing.T) {
	const windowSeconds = 60
	window := windowSeconds * time.Second
	t1 := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	t2 := t1.Add(time.Second)
	t3 := t2.Add(time.Second)
	globalLimitBase := utiltestingalpha.MakePreemptionLimit("global", kueuealpha.GlobalLimitAggregation).
		UID("global-uid").Generation(1).Limit(10).WindowSeconds(windowSeconds)
	globalLimit := globalLimitBase.Clone().Obj()
	perCQLimitBase := utiltestingalpha.MakePreemptionLimit("per-cq", kueuealpha.ByPreemptorCQLimitAggregation).
		UID("per-cq-uid").Generation(1).Limit(10).WindowSeconds(windowSeconds)
	perCQLimit := perCQLimitBase.Clone().Obj()
	oldPerCQLimit := perCQLimitBase.Clone().UID("old-uid").Obj()

	cases := map[string]struct {
		initial limitEvents
		// syncBeforeRecord, if set, is passed to Sync after initial setup and before Record.
		syncBeforeRecord []*kueuealpha.PreemptionLimit
		ts               time.Time
		consumed         policy.ConsumedLimits
		wantCounts       map[policy.PreemptionLimitReference]policy.PreemptionLimitAggregationCounts
		// wantEarliestAfterT1, if set, checks the earliest remaining event for "global" after t1 expires.
		wantEarliestAfterT1 *time.Time
	}{
		"nil consumed limits is a no-op": {
			initial: limitEvents{
				globalLimit: {policy.GlobalAggregationKey: {t1}},
			},
			ts: t2,
			wantCounts: map[policy.PreemptionLimitReference]policy.PreemptionLimitAggregationCounts{
				"global": {policy.GlobalAggregationKey: 1},
			},
		},
		"every consumed limit is recorded": {
			initial: limitEvents{
				globalLimit: nil,
				perCQLimit:  nil,
			},
			ts: t1,
			consumed: policy.ConsumedLimits{
				"global": policy.NewConsumedLimit(globalLimit, policy.GlobalAggregationKey),
				"per-cq": policy.NewConsumedLimit(perCQLimit, "cq-a"),
			},
			wantCounts: map[policy.PreemptionLimitReference]policy.PreemptionLimitAggregationCounts{
				"global": {policy.GlobalAggregationKey: 1},
				"per-cq": {"cq-a": 1},
			},
		},
		"untracked limit is skipped": {
			initial: limitEvents{
				globalLimit: nil,
			},
			ts: t1,
			consumed: policy.ConsumedLimits{
				"global": policy.NewConsumedLimit(globalLimit, policy.GlobalAggregationKey),
				"per-cq": policy.NewConsumedLimit(perCQLimit, "cq-a"),
			},
			wantCounts: map[policy.PreemptionLimitReference]policy.PreemptionLimitAggregationCounts{
				"global": {policy.GlobalAggregationKey: 1},
				"per-cq": nil,
			},
		},
		"limit resolved before it was recreated is skipped": {
			initial: limitEvents{
				globalLimit: nil,
				perCQLimit:  {"cq-a": {t1}},
			},
			ts: t2,
			consumed: policy.ConsumedLimits{
				"global": policy.NewConsumedLimit(globalLimit, policy.GlobalAggregationKey),
				"per-cq": policy.NewConsumedLimit(oldPerCQLimit, "cq-a"),
			},
			wantCounts: map[policy.PreemptionLimitReference]policy.PreemptionLimitAggregationCounts{
				"global": {policy.GlobalAggregationKey: 1},
				"per-cq": {"cq-a": 1},
			},
		},
		"limit resolved before its last reset is skipped": {
			initial: limitEvents{
				globalLimit: nil,
				perCQLimitBase.Clone().Generation(2).Obj(): {"cq-a": {t1}},
			},
			ts: t2,
			consumed: policy.ConsumedLimits{
				"global": policy.NewConsumedLimit(globalLimit, policy.GlobalAggregationKey),
				"per-cq": policy.NewConsumedLimit(perCQLimit, "cq-a"),
			},
			wantCounts: map[policy.PreemptionLimitReference]policy.PreemptionLimitAggregationCounts{
				"global": {policy.GlobalAggregationKey: 1},
				"per-cq": {"cq-a": 1},
			},
		},
		"limit resolved at or after its last reset is recorded": {
			initial: limitEvents{
				// Reset at generation 2, then updated at generation 3 by a limit change (keeping resetGeneration at 2).
				globalLimitBase.Clone().Generation(2).Obj(): nil,
				perCQLimitBase.Clone().Generation(2).Obj():  nil,
			},
			syncBeforeRecord: []*kueuealpha.PreemptionLimit{
				globalLimitBase.Clone().Generation(3).Limit(20).Obj(),
				perCQLimitBase.Clone().Generation(3).Limit(20).Obj(),
			},
			ts: t1,
			consumed: policy.ConsumedLimits{
				"global": policy.NewConsumedLimit(globalLimitBase.Clone().Generation(3).Obj(), policy.GlobalAggregationKey),
				"per-cq": policy.NewConsumedLimit(perCQLimitBase.Clone().Generation(2).Obj(), "cq-a"),
			},
			wantCounts: map[policy.PreemptionLimitReference]policy.PreemptionLimitAggregationCounts{
				"global": {policy.GlobalAggregationKey: 1},
				"per-cq": {"cq-a": 1},
			},
		},
		"new aggregation key of a recorded limit": {
			initial: limitEvents{
				perCQLimit: {"cq-a": {t1}},
			},
			ts:       t2,
			consumed: policy.ConsumedLimits{"per-cq": policy.NewConsumedLimit(perCQLimit, "cq-b")},
			wantCounts: map[policy.PreemptionLimitReference]policy.PreemptionLimitAggregationCounts{
				"per-cq": {"cq-a": 1, "cq-b": 1},
			},
		},
		"later event is appended": {
			initial: limitEvents{
				globalLimit: {policy.GlobalAggregationKey: {t1, t2}},
			},
			ts:       t3,
			consumed: policy.ConsumedLimits{"global": policy.NewConsumedLimit(globalLimit, policy.GlobalAggregationKey)},
			wantCounts: map[policy.PreemptionLimitReference]policy.PreemptionLimitAggregationCounts{
				"global": {policy.GlobalAggregationKey: 3},
			},
			wantEarliestAfterT1: &t2,
		},
		"earlier event is inserted in order": {
			initial: limitEvents{
				globalLimit: {policy.GlobalAggregationKey: {t1, t3}},
			},
			ts:       t2,
			consumed: policy.ConsumedLimits{"global": policy.NewConsumedLimit(globalLimit, policy.GlobalAggregationKey)},
			wantCounts: map[policy.PreemptionLimitReference]policy.PreemptionLimitAggregationCounts{
				"global": {policy.GlobalAggregationKey: 3},
			},
			wantEarliestAfterT1: &t2,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			tracker := newTrackerWithEvents(tc.initial)
			for _, l := range tc.syncBeforeRecord {
				tracker.Sync(l)
			}
			tracker.Record(tc.ts, tc.consumed)

			for ref, want := range tc.wantCounts {
				got := tracker.Counts(t3, ref)
				if diff := cmp.Diff(want, got, cmpopts.EquateEmpty()); diff != "" {
					t.Errorf("Counts(%q) (-want,+got):\n%s", ref, diff)
				}
			}
			if tc.wantEarliestAfterT1 != nil {
				snap := tracker.Snapshot(t1.Add(window))
				wantSlot := tc.wantEarliestAfterT1.Add(window)
				if diff := cmp.Diff(&wantSlot, snap.nextTimeSlot("global", policy.GlobalAggregationKey)); diff != "" {
					t.Errorf("nextTimeSlot after t1 expired (-want,+got):\n%s", diff)
				}
			}
		})
	}
}

func TestPreemptionLimitTrackerRecordConcurrent(t *testing.T) {
	const goroutines, recordsPerGoroutine = 10, 100
	start := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	globalLimit := utiltestingalpha.MakePreemptionLimit("global", kueuealpha.GlobalLimitAggregation).
		Generation(1).WindowSeconds(60).Obj()
	globalRef := policy.PreemptionLimitReference(globalLimit.Name)
	consumed := policy.ConsumedLimits{globalRef: policy.NewConsumedLimit(globalLimit, policy.GlobalAggregationKey)}

	tracker := NewPreemptionLimitTracker()
	tracker.Sync(globalLimit)
	var wg sync.WaitGroup
	for g := range goroutines {
		wg.Go(func() {
			for i := range recordsPerGoroutine {
				tracker.Record(start.Add(time.Duration(g*recordsPerGoroutine+i)*time.Second), consumed)
			}
		})
	}
	wg.Wait()

	got := tracker.limits[globalRef].eventsByAggregationKey[policy.GlobalAggregationKey]
	if len(got) != goroutines*recordsPerGoroutine {
		t.Errorf("Recorded %d events, want %d", len(got), goroutines*recordsPerGoroutine)
	}
	if !slices.IsSortedFunc(got, time.Time.Compare) {
		t.Errorf("Events are not sorted: %v", got)
	}
}

func TestPreemptionLimitTrackerSync(t *testing.T) {
	const windowSeconds = 60
	now := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	expired := now.Add(-time.Hour)
	limitBase := utiltestingalpha.MakePreemptionLimit("per-cq", kueuealpha.ByPreemptorCQLimitAggregation).
		UID("per-cq-uid").Generation(1).WindowSeconds(windowSeconds)
	limit := limitBase.Clone().Obj()
	otherLimit := utiltestingalpha.MakePreemptionLimit("global", kueuealpha.GlobalLimitAggregation).
		UID("global-uid").Generation(1).WindowSeconds(windowSeconds).Obj()
	recreatedLimit := limitBase.Clone().UID("old-uid").Obj()
	allSelectors := limitBase.Clone().
		ConfigSelector(*selector("tier", "gold")).
		PreemptorClusterQueueSelector(selector("side", "preemptor")).
		PreempteeClusterQueueSelector(selector("side", "preemptee")).
		Obj()
	limitAndWindowIncreased := limitBase.Clone().Generation(2).Limit(10).WindowSeconds(2 * windowSeconds).Obj()
	windowHalved := limitBase.Clone().Generation(2).WindowSeconds(windowSeconds / 2).Obj()
	reaggregated := limitBase.Clone().Generation(2).Aggregation(kueuealpha.ByPreempteeCQLimitAggregation).Obj()

	cases := map[string]struct {
		initial limitEvents
		// synced, if set, is passed to Sync instead of limit.
		synced *kueuealpha.PreemptionLimit
		// wantTotalEvents is the expected number of retained events (including expired ones)
		// for "per-cq" under key "cq-a".
		wantTotalEvents int32
		// wantGen1Accepted indicates whether a record resolved against generation 1 of limit
		// should still be accepted after Sync (true when resetGeneration stayed 1).
		wantGen1Accepted bool
		wantOtherCount   int32
		wantResult       bool
	}{
		"untracked limit starts being tracked": {
			wantGen1Accepted: true,
			wantResult:       false,
		},
		"untracked limit starts being tracked with its selectors": {
			synced:           allSelectors,
			wantGen1Accepted: true,
			wantResult:       false,
		},
		"other limits are untouched": {
			initial: limitEvents{
				limit:      {"cq-a": {now}},
				otherLimit: {policy.GlobalAggregationKey: {now}},
			},
			synced:           limitAndWindowIncreased,
			wantTotalEvents:  1,
			wantGen1Accepted: true,
			wantOtherCount:   1,
			wantResult:       true,
		},
		"limit increase updates the limit, and keeps the events and the reset generation": {
			initial: limitEvents{
				limit: {"cq-a": {expired, now}},
			},
			synced:           limitBase.Clone().Generation(2).Limit(10).Obj(),
			wantTotalEvents:  2,
			wantGen1Accepted: true,
			wantResult:       true,
		},
		"limit decrease updates the limit, and keeps the events and the reset generation": {
			initial: limitEvents{
				limitBase.Clone().Limit(10).Obj(): {"cq-a": {expired, now}},
			},
			synced:           limitBase.Clone().Generation(2).Limit(5).Obj(),
			wantTotalEvents:  2,
			wantGen1Accepted: true,
			wantResult:       false,
		},
		"limit and window increase update the limit, and keep the events and the reset generation": {
			initial: limitEvents{
				limit: {"cq-a": {expired, now}},
			},
			synced:           limitAndWindowIncreased,
			wantTotalEvents:  2,
			wantGen1Accepted: true,
			wantResult:       true,
		},
		"limit and window decrease update the limit, and keep the events and the reset generation": {
			initial: limitEvents{
				limitBase.Clone().Limit(10).Obj(): {"cq-a": {expired, now}},
			},
			synced:           limitBase.Clone().Generation(2).Limit(5).WindowSeconds(10).Obj(),
			wantTotalEvents:  2,
			wantGen1Accepted: true,
			wantResult:       true,
		},
		"window increase updates the limit, and keeps the events and the reset generation": {
			initial: limitEvents{
				limit: {"cq-a": {expired, now}},
			},
			synced:           limitBase.Clone().Generation(2).WindowSeconds(windowSeconds + 1).Obj(),
			wantTotalEvents:  2,
			wantGen1Accepted: true,
			wantResult:       false,
		},
		"window decrease updates the limit, and keeps the events and the reset generation": {
			initial: limitEvents{
				limit: {"cq-a": {expired, now}},
			},
			synced:           windowHalved,
			wantTotalEvents:  2,
			wantGen1Accepted: true,
			wantResult:       true,
		},
		"unchanged selectors keep the events": {
			initial: limitEvents{
				allSelectors: {"cq-a": {now}},
			},
			synced:           allSelectors,
			wantTotalEvents:  1,
			wantGen1Accepted: true,
			wantResult:       false,
		},
		"limit recreated with the same name resets the events": {
			initial: limitEvents{
				recreatedLimit: {"cq-a": {now}},
			},
			wantTotalEvents:  0,
			wantGen1Accepted: true,
			wantResult:       true,
		},
		"aggregation change resets the events and updates the reset generation": {
			initial: limitEvents{
				limit: {"cq-a": {now}},
			},
			synced:           reaggregated,
			wantTotalEvents:  0,
			wantGen1Accepted: false,
			wantResult:       true,
		},
		"config selector change resets the events": {
			initial: limitEvents{
				limit: {"cq-a": {now}},
			},
			synced:           limitBase.Clone().Generation(2).ConfigSelector(*selector("tier", "gold")).Obj(),
			wantTotalEvents:  0,
			wantGen1Accepted: false,
			wantResult:       true,
		},
		"preemptor ClusterQueue selector change resets the events": {
			initial: limitEvents{
				limitBase.Clone().PreemptorClusterQueueSelector(selector("side", "preemptor")).Obj(): {"cq-a": {now}},
			},
			synced:           limitBase.Clone().Generation(2).PreemptorClusterQueueSelector(selector("side", "other")).Obj(),
			wantTotalEvents:  0,
			wantGen1Accepted: false,
			wantResult:       true,
		},
		"preemptee ClusterQueue selector change resets the events": {
			initial: limitEvents{
				limitBase.Clone().PreempteeClusterQueueSelector(selector("side", "preemptee")).Obj(): {"cq-a": {now}},
			},
			synced:           limitBase.Clone().Generation(2).Obj(),
			wantTotalEvents:  0,
			wantGen1Accepted: false,
			wantResult:       true,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			tracker := newTrackerWithEvents(tc.initial)
			synced := limit
			if tc.synced != nil {
				synced = tc.synced
			}
			gotResult := tracker.Sync(synced)
			if diff := cmp.Diff(tc.wantResult, gotResult); diff != "" {
				t.Errorf("Unexpected Sync return value (-want,+got):\n%s", diff)
			}

			snap := tracker.Snapshot(now)
			if diff := cmp.Diff(trackedCopy(synced), snap.limitByName["per-cq"]); diff != "" {
				t.Errorf("Unexpected synced limit in snapshot (-want,+got):\n%s", diff)
			}

			// Query Counts at expired so that windowStart is strictly before expired,
			// verifying all retained events (both expired and current).
			gotEvents := tracker.Counts(expired, "per-cq")["cq-a"]
			if gotEvents != tc.wantTotalEvents {
				t.Errorf("Retained events for per-cq/cq-a = %d, want %d", gotEvents, tc.wantTotalEvents)
			}

			gotOtherCount := tracker.Counts(now, "global")[policy.GlobalAggregationKey]
			if gotOtherCount != tc.wantOtherCount {
				t.Errorf("Counts for global = %d, want %d", gotOtherCount, tc.wantOtherCount)
			}

			// Probe resetGeneration through Record with a generation-1 ConsumedLimit.
			tracker.Record(now, policy.ConsumedLimits{"per-cq": policy.NewConsumedLimit(limit, "probe-cq")})
			gotGen1Accepted := tracker.Counts(now, "per-cq")["probe-cq"] == 1
			if gotGen1Accepted != tc.wantGen1Accepted {
				t.Errorf("Generation-1 record accepted = %v, want %v", gotGen1Accepted, tc.wantGen1Accepted)
			}
		})
	}
}

func TestPreemptionLimitTrackerPrune(t *testing.T) {
	const windowSeconds = 60
	now := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	window := windowSeconds * time.Second
	cutoff := now.Add(-window - pruneGracePeriod)
	expired := cutoff.Add(-time.Nanosecond)
	withinGracePeriod := now.Add(-window - pruneGracePeriod/2)
	limitBase := utiltestingalpha.MakePreemptionLimit("per-cq", kueuealpha.ByPreemptorCQLimitAggregation).
		Generation(1).WindowSeconds(windowSeconds)
	limit := limitBase.Clone().Obj()
	otherLimit := utiltestingalpha.MakePreemptionLimit("global", kueuealpha.GlobalLimitAggregation).
		Generation(1).WindowSeconds(windowSeconds).Obj()
	doubledWindowLimit := limitBase.Clone().Generation(2).WindowSeconds(2 * windowSeconds).Obj()
	halvedWindowLimit := limitBase.Clone().Generation(2).WindowSeconds(windowSeconds / 2).Obj()
	expiredWithDoubledWindow := now.Add(-2*window - pruneGracePeriod - time.Nanosecond)
	withinDoubledWindow := cutoff.Add(-time.Second)
	expiredOnlyWithHalvedWindow := now.Add(-window/2 - pruneGracePeriod - time.Second)
	// beforeAllEvents is earlier than every timestamp in this test, so Counts(beforeAllEvents, ...)
	// returns all unpruned events stored in the tracker.
	beforeAllEvents := now.Add(-24 * time.Hour)

	cases := map[string]struct {
		initial           limitEvents
		wantPerCQCounts   policy.PreemptionLimitAggregationCounts
		wantOtherCounts   policy.PreemptionLimitAggregationCounts
		wantPerCQTracked  bool
		wantEarliestCQA   *time.Time
		wantForgottenKeys []policy.PreemptionLimitAggregationKey
	}{
		"events before the cutoff are dropped, events at or after it are kept": {
			initial: limitEvents{
				limit: {"cq-a": {cutoff.Add(-time.Hour), expired, cutoff, now}},
			},
			wantPerCQCounts:  policy.PreemptionLimitAggregationCounts{"cq-a": 2},
			wantPerCQTracked: true,
			wantEarliestCQA:  &cutoff,
		},
		"events within the grace period are kept": {
			initial: limitEvents{
				limit: {"cq-a": {withinGracePeriod}},
			},
			wantPerCQCounts:  policy.PreemptionLimitAggregationCounts{"cq-a": 1},
			wantPerCQTracked: true,
			wantEarliestCQA:  &withinGracePeriod,
		},
		"aggregation key left without events is forgotten": {
			initial: limitEvents{
				limit: {"cq-a": {expired}, "cq-b": {now}},
			},
			wantPerCQCounts:   policy.PreemptionLimitAggregationCounts{"cq-b": 1},
			wantPerCQTracked:  true,
			wantForgottenKeys: []policy.PreemptionLimitAggregationKey{"cq-a"},
		},
		"limit left without events is still tracked": {
			initial: limitEvents{
				limit: {"cq-a": {expired}, "cq-b": {expired}},
			},
			wantPerCQTracked:  true,
			wantForgottenKeys: []policy.PreemptionLimitAggregationKey{"cq-a", "cq-b"},
		},
		"other limits are untouched": {
			initial: limitEvents{
				limit:      {"cq-a": {expired, now}},
				otherLimit: {policy.GlobalAggregationKey: {expired}},
			},
			wantPerCQCounts:  policy.PreemptionLimitAggregationCounts{"cq-a": 1},
			wantOtherCounts:  policy.PreemptionLimitAggregationCounts{policy.GlobalAggregationKey: 1},
			wantPerCQTracked: true,
			wantEarliestCQA:  &now,
		},
		"untracked limit is a no-op": {
			initial: limitEvents{
				otherLimit: {policy.GlobalAggregationKey: {expired}},
			},
			wantOtherCounts:  policy.PreemptionLimitAggregationCounts{policy.GlobalAggregationKey: 1},
			wantPerCQTracked: false,
		},
		"doubled window keeps events within the wider cutoff": {
			initial: limitEvents{
				doubledWindowLimit: {"cq-a": {expiredWithDoubledWindow, withinDoubledWindow, now}},
			},
			wantPerCQCounts:  policy.PreemptionLimitAggregationCounts{"cq-a": 2},
			wantPerCQTracked: true,
			wantEarliestCQA:  &withinDoubledWindow,
		},
		"halved window drops events older than the shorter cutoff": {
			initial: limitEvents{
				halvedWindowLimit: {"cq-a": {expiredOnlyWithHalvedWindow, now}},
			},
			wantPerCQCounts:  policy.PreemptionLimitAggregationCounts{"cq-a": 1},
			wantPerCQTracked: true,
			wantEarliestCQA:  &now,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			tracker := newTrackerWithEvents(tc.initial)
			perCQRef := policy.PreemptionLimitReference(limit.Name)
			tracker.Prune(now, perCQRef)

			if diff := cmp.Diff(tc.wantPerCQCounts, tracker.Counts(beforeAllEvents, perCQRef), cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("Unexpected remaining events for per-cq (-want,+got):\n%s", diff)
			}
			if diff := cmp.Diff(tc.wantOtherCounts, tracker.Counts(beforeAllEvents, "global"), cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("Unexpected remaining events for global (-want,+got):\n%s", diff)
			}

			snap := tracker.Snapshot(beforeAllEvents)
			gotTracked := snap.aggregation(perCQRef) != ""
			if gotTracked != tc.wantPerCQTracked {
				t.Errorf("per-cq tracked = %v, want %v", gotTracked, tc.wantPerCQTracked)
			}
			if tc.wantEarliestCQA != nil {
				gotEarliest := snap.earliestEvent[perCQRef]["cq-a"]
				if diff := cmp.Diff(*tc.wantEarliestCQA, gotEarliest); diff != "" {
					t.Errorf("Unexpected earliest remaining event for cq-a (-want,+got):\n%s", diff)
				}
			}
			for _, key := range tc.wantForgottenKeys {
				if _, exists := tracker.limits[perCQRef].eventsByAggregationKey[key]; exists {
					t.Errorf("Expected aggregation key %q to be deleted from tracker after Prune", key)
				}
			}
		})
	}
}

func TestPreemptionLimitTrackerSyncAndPruneConcurrent(t *testing.T) {
	const goroutines, recordsPerGoroutine = 10, 100
	const records = goroutines * recordsPerGoroutine
	const windowSeconds = 60
	start := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	window := windowSeconds * time.Second
	limit := utiltestingalpha.MakePreemptionLimit("per-cq", kueuealpha.ByPreemptorCQLimitAggregation).
		Generation(1).WindowSeconds(windowSeconds).Obj()
	otherLimit := utiltestingalpha.MakePreemptionLimit("global", kueuealpha.GlobalLimitAggregation).
		Generation(1).WindowSeconds(windowSeconds).Obj()
	limitRef := policy.PreemptionLimitReference(limit.Name)
	otherLimitRef := policy.PreemptionLimitReference(otherLimit.Name)
	consumed := policy.ConsumedLimits{
		limitRef:      policy.NewConsumedLimit(limit, "cq-a"),
		otherLimitRef: policy.NewConsumedLimit(otherLimit, policy.GlobalAggregationKey),
	}
	// Every recorded event is already expired, so Prune keeps dropping events from
	// limit while Sync updates its state and Record keeps adding to it.
	now := start.Add(records*time.Second + window + pruneGracePeriod)

	tracker := NewPreemptionLimitTracker()
	tracker.Sync(otherLimit)
	var wg sync.WaitGroup
	for g := range goroutines {
		wg.Go(func() {
			for i := range recordsPerGoroutine {
				tracker.Sync(limit)
				tracker.Prune(now, limitRef)
				tracker.Record(start.Add(time.Duration(g*recordsPerGoroutine+i)*time.Second), consumed)
			}
		})
	}
	wg.Wait()

	got := tracker.limits[otherLimitRef].eventsByAggregationKey[policy.GlobalAggregationKey]
	if len(got) != records {
		t.Errorf("Recorded %d events of the other limit, want %d", len(got), records)
	}
	if !slices.IsSortedFunc(got, time.Time.Compare) {
		t.Errorf("Events of the other limit are not sorted: %v", got)
	}
}

func TestPreemptionLimitTrackerForget(t *testing.T) {
	now := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	forgottenLimit := utiltestingalpha.MakePreemptionLimit("per-cq", kueuealpha.ByPreemptorCQLimitAggregation).
		Generation(1).WindowSeconds(60).Obj()
	otherLimit := utiltestingalpha.MakePreemptionLimit("global", kueuealpha.GlobalLimitAggregation).
		Generation(1).WindowSeconds(60).Obj()

	cases := map[string]struct {
		initial    limitEvents
		wantResult bool
	}{
		"only the given limit is forgotten": {
			initial: limitEvents{
				forgottenLimit: {"cq-a": {now}, "cq-b": {now}},
				otherLimit:     {policy.GlobalAggregationKey: {now}},
			},
			wantResult: true,
		},
		"unknown limit is a no-op": {
			initial: limitEvents{
				otherLimit: {policy.GlobalAggregationKey: {now}},
			},
			wantResult: false,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			tracker := newTrackerWithEvents(tc.initial)
			forgottenRef := policy.PreemptionLimitReference(forgottenLimit.Name)
			gotResult := tracker.Forget(forgottenRef)
			if diff := cmp.Diff(tc.wantResult, gotResult); diff != "" {
				t.Errorf("Unexpected result returned (-want,+got):\n%s", diff)
			}

			if got := tracker.Counts(now, forgottenRef); len(got) != 0 {
				t.Errorf("Counts for forgotten limit = %v, want empty", got)
			}
			// Verify the forgotten limit is no longer tracked by checking that Record is ignored.
			tracker.Record(now, policy.ConsumedLimits{forgottenRef: policy.NewConsumedLimit(forgottenLimit, "cq-a")})
			if got := tracker.Counts(now, forgottenRef); len(got) != 0 {
				t.Errorf("Counts for forgotten limit after Record = %v, want empty", got)
			}

			wantOther := policy.PreemptionLimitAggregationCounts{policy.GlobalAggregationKey: 1}
			if diff := cmp.Diff(wantOther, tracker.Counts(now, "global"), cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("Unexpected counts for other limit (-want,+got):\n%s", diff)
			}
		})
	}
}
