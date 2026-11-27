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

var cmpTrackedLimits = []cmp.Option{
	cmp.AllowUnexported(limitState{}),
	cmpopts.EquateEmpty(),
}

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

// toTrackedLimits converts a limitEvents map into the internal tracker state map
// for unit test assertions, optionally overriding resetGeneration per limit.
func toTrackedLimits(want limitEvents, resetGenerations map[policy.PreemptionLimitReference]int64) map[policy.PreemptionLimitReference]limitState {
	res := make(map[policy.PreemptionLimitReference]limitState, len(want))
	for limit, aggEvents := range want {
		ref := policy.PreemptionLimitReference(limit.Name)
		ls := newLimitState(limit)
		if aggEvents != nil {
			ls.eventsByAggregationKey = aggEvents
		}
		if gen, ok := resetGenerations[ref]; ok {
			ls.resetGeneration = gen
		}
		res[ref] = ls
	}
	return res
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
			wantLimits := make(map[policy.PreemptionLimitReference]*limitSnapshot, len(tc.wantLimits))
			for _, l := range tc.wantLimits {
				ref := policy.PreemptionLimitReference(l.Name)
				ls := newLimitSnapshot(trackedCopy(l))
				if recorded := tc.wantRecorded[ref]; recorded != nil {
					ls.recorded = recorded
				}
				if earliest := tc.wantEarliestEvent[ref]; earliest != nil {
					ls.earliestEvent = earliest
				}
				wantLimits[ref] = ls
			}
			want := &PreemptionLimitsSnapshot{limits: wantLimits}
			if diff := cmp.Diff(want, got, cmp.AllowUnexported(PreemptionLimitsSnapshot{}, limitSnapshot{}), cmpopts.EquateEmpty()); diff != "" {
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
	t1 := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	t2 := t1.Add(time.Second)
	t3 := t2.Add(time.Second)
	globalLimitBase := utiltestingalpha.MakePreemptionLimit("global", kueuealpha.GlobalLimitAggregation).
		UID("global-uid").Generation(1).WindowSeconds(60)
	globalLimit := globalLimitBase.Clone().Obj()
	globalLimitGen2 := globalLimitBase.Clone().Generation(2).Obj()
	globalLimitGen3 := globalLimitBase.Clone().Generation(3).Limit(20).Obj()
	perCQLimitBase := utiltestingalpha.MakePreemptionLimit("per-cq", kueuealpha.ByPreemptorCQLimitAggregation).
		UID("per-cq-uid").Generation(1).WindowSeconds(60)
	perCQLimit := perCQLimitBase.Clone().Obj()
	perCQLimitGen2 := perCQLimitBase.Clone().Generation(2).Obj()
	perCQLimitGen3 := perCQLimitBase.Clone().Generation(3).Limit(20).Obj()
	oldPerCQLimit := perCQLimitBase.Clone().UID("old-uid").Obj()

	cases := map[string]struct {
		initial limitEvents
		// syncBeforeRecord, if set, is passed to Sync after initial setup and before Record.
		syncBeforeRecord []*kueuealpha.PreemptionLimit
		ts               time.Time
		consumed         policy.ConsumedLimits
		want             limitEvents
		wantResetGen     map[policy.PreemptionLimitReference]int64
	}{
		"nil consumed limits is a no-op": {
			initial: limitEvents{
				globalLimit: {policy.GlobalAggregationKey: {t1}},
			},
			ts: t2,
			want: limitEvents{
				globalLimit: {policy.GlobalAggregationKey: {t1}},
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
			want: limitEvents{
				globalLimit: {policy.GlobalAggregationKey: {t1}},
				perCQLimit:  {"cq-a": {t1}},
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
			want: limitEvents{
				globalLimit: {policy.GlobalAggregationKey: {t1}},
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
				"per-cq": policy.NewConsumedLimit(oldPerCQLimit, "cq-b"),
			},
			want: limitEvents{
				globalLimit: {policy.GlobalAggregationKey: {t2}},
				perCQLimit:  {"cq-a": {t1}},
			},
		},
		"limit resolved before its last reset is skipped": {
			initial: limitEvents{
				globalLimit:    nil,
				perCQLimitGen2: {"cq-a": {t1}},
			},
			ts: t2,
			consumed: policy.ConsumedLimits{
				"global": policy.NewConsumedLimit(globalLimit, policy.GlobalAggregationKey),
				"per-cq": policy.NewConsumedLimit(perCQLimit, "cq-b"),
			},
			want: limitEvents{
				globalLimit:    {policy.GlobalAggregationKey: {t2}},
				perCQLimitGen2: {"cq-a": {t1}},
			},
		},
		"limit resolved at or after its last reset is recorded": {
			initial: limitEvents{
				// Reset at generation 2, then updated at generation 3 by a limit change.
				globalLimitGen2: nil,
				perCQLimitGen2:  nil,
			},
			syncBeforeRecord: []*kueuealpha.PreemptionLimit{globalLimitGen3, perCQLimitGen3},
			ts:               t1,
			consumed: policy.ConsumedLimits{
				"global": policy.NewConsumedLimit(globalLimitGen3, policy.GlobalAggregationKey),
				"per-cq": policy.NewConsumedLimit(perCQLimitGen2, "cq-a"),
			},
			want: limitEvents{
				globalLimitGen3: {policy.GlobalAggregationKey: {t1}},
				perCQLimitGen3:  {"cq-a": {t1}},
			},
			wantResetGen: map[policy.PreemptionLimitReference]int64{
				"global": 2,
				"per-cq": 2,
			},
		},
		"new aggregation key of a recorded limit": {
			initial: limitEvents{
				perCQLimit: {"cq-a": {t1}},
			},
			ts:       t2,
			consumed: policy.ConsumedLimits{"per-cq": policy.NewConsumedLimit(perCQLimit, "cq-b")},
			want: limitEvents{
				perCQLimit: {"cq-a": {t1}, "cq-b": {t2}},
			},
		},
		"later event is appended": {
			initial: limitEvents{
				globalLimit: {policy.GlobalAggregationKey: {t1, t2}},
			},
			ts:       t3,
			consumed: policy.ConsumedLimits{"global": policy.NewConsumedLimit(globalLimit, policy.GlobalAggregationKey)},
			want: limitEvents{
				globalLimit: {policy.GlobalAggregationKey: {t1, t2, t3}},
			},
		},
		"earlier event is inserted in order": {
			initial: limitEvents{
				globalLimit: {policy.GlobalAggregationKey: {t1, t3}},
			},
			ts:       t2,
			consumed: policy.ConsumedLimits{"global": policy.NewConsumedLimit(globalLimit, policy.GlobalAggregationKey)},
			want: limitEvents{
				globalLimit: {policy.GlobalAggregationKey: {t1, t2, t3}},
			},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			tracker := newTrackerWithEvents(tc.initial)
			for _, l := range tc.syncBeforeRecord {
				tracker.Sync(l)
			}
			tracker.Record(tc.ts, tc.consumed)

			want := toTrackedLimits(tc.want, tc.wantResetGen)
			if diff := cmp.Diff(want, tracker.limits, cmpTrackedLimits...); diff != "" {
				t.Errorf("Unexpected tracked limits (-want,+got):\n%s", diff)
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
	limit10 := limitBase.Clone().Limit(10).Obj()
	otherLimit := utiltestingalpha.MakePreemptionLimit("global", kueuealpha.GlobalLimitAggregation).
		UID("global-uid").Generation(1).WindowSeconds(windowSeconds).Obj()
	recreatedLimit := limitBase.Clone().UID("old-uid").Obj()
	allSelectors := limitBase.Clone().
		ConfigSelector(*selector("tier", "gold")).
		PreemptorClusterQueueSelector(selector("side", "preemptor")).
		PreempteeClusterQueueSelector(selector("side", "preemptee")).
		Obj()
	limitIncreased := limitBase.Clone().Generation(2).Limit(10).Obj()
	limitDecreased := limitBase.Clone().Generation(2).Limit(5).Obj()
	limitAndWindowIncreased := limitBase.Clone().Generation(2).Limit(10).WindowSeconds(2 * windowSeconds).Obj()
	limitAndWindowDecreased := limitBase.Clone().Generation(2).Limit(5).WindowSeconds(10).Obj()
	windowIncreased := limitBase.Clone().Generation(2).WindowSeconds(windowSeconds + 1).Obj()
	windowHalved := limitBase.Clone().Generation(2).WindowSeconds(windowSeconds / 2).Obj()
	reaggregated := limitBase.Clone().Generation(2).Aggregation(kueuealpha.ByPreempteeCQLimitAggregation).Obj()
	configSelectorUpdated := limitBase.Clone().Generation(2).ConfigSelector(*selector("tier", "gold")).Obj()
	preemptorSelectorInitial := limitBase.Clone().PreemptorClusterQueueSelector(selector("side", "preemptor")).Obj()
	preemptorSelectorUpdated := limitBase.Clone().Generation(2).PreemptorClusterQueueSelector(selector("side", "other")).Obj()
	preempteeSelectorInitial := limitBase.Clone().PreempteeClusterQueueSelector(selector("side", "preemptee")).Obj()
	preempteeSelectorCleared := limitBase.Clone().Generation(2).Obj()

	cases := map[string]struct {
		initial limitEvents
		// synced, if set, is passed to Sync instead of limit.
		synced *kueuealpha.PreemptionLimit
		want   limitEvents
		// wantResetGen, when non-zero, overrides the expected resetGeneration for "per-cq"
		// (otherwise defaults to the synced limit's Generation).
		wantResetGen int64
		wantResult   bool
	}{
		"untracked limit starts being tracked": {
			want:       limitEvents{limit: nil},
			wantResult: false,
		},
		"untracked limit starts being tracked with its selectors": {
			synced:     allSelectors,
			want:       limitEvents{allSelectors: nil},
			wantResult: false,
		},
		"other limits are untouched": {
			initial: limitEvents{
				limit:      {"cq-a": {now}},
				otherLimit: {policy.GlobalAggregationKey: {now}},
			},
			synced: limitAndWindowIncreased,
			want: limitEvents{
				limitAndWindowIncreased: {"cq-a": {now}},
				otherLimit:              {policy.GlobalAggregationKey: {now}},
			},
			wantResetGen: 1,
			wantResult:   true,
		},
		"limit increase updates the limit, and keeps the events and the reset generation": {
			initial:      limitEvents{limit: {"cq-a": {expired, now}}},
			synced:       limitIncreased,
			want:         limitEvents{limitIncreased: {"cq-a": {expired, now}}},
			wantResetGen: 1,
			wantResult:   true,
		},
		"limit decrease updates the limit, and keeps the events and the reset generation": {
			initial:      limitEvents{limit10: {"cq-a": {expired, now}}},
			synced:       limitDecreased,
			want:         limitEvents{limitDecreased: {"cq-a": {expired, now}}},
			wantResetGen: 1,
			wantResult:   false,
		},
		"limit and window increase update the limit, and keep the events and the reset generation": {
			initial:      limitEvents{limit: {"cq-a": {expired, now}}},
			synced:       limitAndWindowIncreased,
			want:         limitEvents{limitAndWindowIncreased: {"cq-a": {expired, now}}},
			wantResetGen: 1,
			wantResult:   true,
		},
		"limit and window decrease update the limit, and keep the events and the reset generation": {
			initial:      limitEvents{limit10: {"cq-a": {expired, now}}},
			synced:       limitAndWindowDecreased,
			want:         limitEvents{limitAndWindowDecreased: {"cq-a": {expired, now}}},
			wantResetGen: 1,
			wantResult:   true,
		},
		"window increase updates the limit, and keeps the events and the reset generation": {
			initial:      limitEvents{limit: {"cq-a": {expired, now}}},
			synced:       windowIncreased,
			want:         limitEvents{windowIncreased: {"cq-a": {expired, now}}},
			wantResetGen: 1,
			wantResult:   false,
		},
		"window decrease updates the limit, and keeps the events and the reset generation": {
			initial:      limitEvents{limit: {"cq-a": {expired, now}}},
			synced:       windowHalved,
			want:         limitEvents{windowHalved: {"cq-a": {expired, now}}},
			wantResetGen: 1,
			wantResult:   true,
		},
		"unchanged selectors keep the events": {
			initial:    limitEvents{allSelectors: {"cq-a": {now}}},
			synced:     allSelectors,
			want:       limitEvents{allSelectors: {"cq-a": {now}}},
			wantResult: false,
		},
		"limit recreated with the same name resets the events": {
			initial:    limitEvents{recreatedLimit: {"cq-a": {now}}},
			want:       limitEvents{limit: nil},
			wantResult: true,
		},
		"aggregation change resets the events and updates the reset generation": {
			initial:    limitEvents{limit: {"cq-a": {now}}},
			synced:     reaggregated,
			want:       limitEvents{reaggregated: nil},
			wantResult: true,
		},
		"config selector change resets the events": {
			initial:    limitEvents{limit: {"cq-a": {now}}},
			synced:     configSelectorUpdated,
			want:       limitEvents{configSelectorUpdated: nil},
			wantResult: true,
		},
		"preemptor ClusterQueue selector change resets the events": {
			initial:    limitEvents{preemptorSelectorInitial: {"cq-a": {now}}},
			synced:     preemptorSelectorUpdated,
			want:       limitEvents{preemptorSelectorUpdated: nil},
			wantResult: true,
		},
		"preemptee ClusterQueue selector change resets the events": {
			initial:    limitEvents{preempteeSelectorInitial: {"cq-a": {now}}},
			synced:     preempteeSelectorCleared,
			want:       limitEvents{preempteeSelectorCleared: nil},
			wantResult: true,
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
			var resetGens map[policy.PreemptionLimitReference]int64
			if tc.wantResetGen != 0 {
				resetGens = map[policy.PreemptionLimitReference]int64{"per-cq": tc.wantResetGen}
			}
			want := toTrackedLimits(tc.want, resetGens)
			if diff := cmp.Diff(want, tracker.limits, cmpTrackedLimits...); diff != "" {
				t.Errorf("Unexpected tracked limits (-want,+got):\n%s", diff)
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

	cases := map[string]struct {
		initial limitEvents
		want    limitEvents
	}{
		"events before the cutoff are dropped, events at or after it are kept": {
			initial: limitEvents{limit: {"cq-a": {cutoff.Add(-time.Hour), expired, cutoff, now}}},
			want:    limitEvents{limit: {"cq-a": {cutoff, now}}},
		},
		"events within the grace period are kept": {
			initial: limitEvents{limit: {"cq-a": {withinGracePeriod}}},
			want:    limitEvents{limit: {"cq-a": {withinGracePeriod}}},
		},
		"aggregation key left without events is forgotten": {
			initial: limitEvents{limit: {"cq-a": {expired}, "cq-b": {now}}},
			want:    limitEvents{limit: {"cq-b": {now}}},
		},
		"limit left without events is still tracked": {
			initial: limitEvents{limit: {"cq-a": {expired}, "cq-b": {expired}}},
			want:    limitEvents{limit: nil},
		},
		"other limits are untouched": {
			initial: limitEvents{
				limit:      {"cq-a": {expired, now}},
				otherLimit: {policy.GlobalAggregationKey: {expired}},
			},
			want: limitEvents{
				limit:      {"cq-a": {now}},
				otherLimit: {policy.GlobalAggregationKey: {expired}},
			},
		},
		"untracked limit is a no-op": {
			initial: limitEvents{otherLimit: {policy.GlobalAggregationKey: {expired}}},
			want:    limitEvents{otherLimit: {policy.GlobalAggregationKey: {expired}}},
		},
		"doubled window keeps events within the wider cutoff": {
			initial: limitEvents{doubledWindowLimit: {"cq-a": {expiredWithDoubledWindow, withinDoubledWindow, now}}},
			want:    limitEvents{doubledWindowLimit: {"cq-a": {withinDoubledWindow, now}}},
		},
		"halved window drops events older than the shorter cutoff": {
			initial: limitEvents{halvedWindowLimit: {"cq-a": {expiredOnlyWithHalvedWindow, now}}},
			want:    limitEvents{halvedWindowLimit: {"cq-a": {now}}},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			tracker := newTrackerWithEvents(tc.initial)
			tracker.Prune(now, policy.PreemptionLimitReference(limit.Name))
			want := toTrackedLimits(tc.want, nil)
			if diff := cmp.Diff(want, tracker.limits, cmpTrackedLimits...); diff != "" {
				t.Errorf("Unexpected tracked limits (-want,+got):\n%s", diff)
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
		want       limitEvents
		wantResult bool
	}{
		"only the given limit is forgotten": {
			initial: limitEvents{
				forgottenLimit: {"cq-a": {now}, "cq-b": {now}},
				otherLimit:     {policy.GlobalAggregationKey: {now}},
			},
			want:       limitEvents{otherLimit: {policy.GlobalAggregationKey: {now}}},
			wantResult: true,
		},
		"unknown limit is a no-op": {
			initial:    limitEvents{otherLimit: {policy.GlobalAggregationKey: {now}}},
			want:       limitEvents{otherLimit: {policy.GlobalAggregationKey: {now}}},
			wantResult: false,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			tracker := newTrackerWithEvents(tc.initial)
			gotResult := tracker.Forget(policy.PreemptionLimitReference(forgottenLimit.Name))
			if diff := cmp.Diff(tc.wantResult, gotResult); diff != "" {
				t.Errorf("Unexpected result returned (-want,+got):\n%s", diff)
			}
			want := toTrackedLimits(tc.want, nil)
			if diff := cmp.Diff(want, tracker.limits, cmpTrackedLimits...); diff != "" {
				t.Errorf("Unexpected tracked limits (-want,+got):\n%s", diff)
			}
		})
	}
}
