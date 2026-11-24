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
	"cmp"
	"testing"
	"time"

	gocmp "github.com/google/go-cmp/cmp"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	"sigs.k8s.io/kueue/pkg/scheduler/preemption/policy"
	utiltestingalpha "sigs.k8s.io/kueue/pkg/util/testing/v1alpha1"
)

type aggregationEvents = map[policy.PreemptionLimitAggregationKey][]time.Time

type eventsByLimit = map[policy.PreemptionLimitReference]aggregationEvents

func snapshotForTest(preemptionLimits []kueuealpha.PreemptionLimit, recordedEvents eventsByLimit) *PreemptionLimitsSnapshot {
	snapshot := &PreemptionLimitsSnapshot{
		limitByName:   make(map[policy.PreemptionLimitReference]*kueuealpha.PreemptionLimit, len(preemptionLimits)),
		recorded:      make(preemptionCounts, len(preemptionLimits)),
		earliestEvent: make(map[policy.PreemptionLimitReference]map[policy.PreemptionLimitAggregationKey]time.Time, len(preemptionLimits)),
	}
	for i := range preemptionLimits {
		ref := policy.PreemptionLimitReference(preemptionLimits[i].Name)
		snapshot.limitByName[ref] = &preemptionLimits[i]
		for key, timestamps := range recordedEvents[ref] {
			for _, ts := range timestamps {
				snapshot.record(ref, key, 1, ts)
			}
		}
	}
	return snapshot
}

func TestPreemptionLimitsSnapshotAllowance(t *testing.T) {
	now := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	perCQLimit := utiltestingalpha.MakePreemptionLimit("per-cq", kueuealpha.ByPreempteeCQLimitAggregation).
		Limit(3).WindowSeconds(60).Obj()
	perCQRef := policy.PreemptionLimitReference(perCQLimit.Name)

	cases := map[string]struct {
		limit  policy.PreemptionLimitReference
		events eventsByLimit
		want   int32
	}{
		"allowance is the limit minus the recorded preemptions": {
			events: eventsByLimit{
				"per-cq": {"cq-a": {now.Add(-30 * time.Second)}},
			},
			want: 2,
		},
		"allowance is clamped to 0 when recorded preemptions exceed the limit": {
			events: eventsByLimit{
				"per-cq": {"cq-a": {now.Add(-40 * time.Second), now.Add(-30 * time.Second), now.Add(-20 * time.Second), now.Add(-10 * time.Second)}},
			},
			want: 0,
		},
		"allowance returns the current limit for the proper aggregation key": {
			events: eventsByLimit{
				"per-cq": {"cq-b": {now, now}},
			},
			want: 3,
		},
		"limit missing from the snapshot has 0 allowance": {
			limit: "missing",
			want:  0,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			snapshot := snapshotForTest([]kueuealpha.PreemptionLimit{*perCQLimit}, tc.events)
			limit := cmp.Or(tc.limit, perCQRef)
			if got := snapshot.allowance(limit, "cq-a"); got != tc.want {
				t.Errorf("allowance() = %d, want %d", got, tc.want)
			}
		})
	}
}

func TestPreemptionLimitsSnapshotAggregation(t *testing.T) {
	perCQLimit := utiltestingalpha.MakePreemptionLimit("per-cq", kueuealpha.ByPreempteeCQLimitAggregation).
		Limit(3).WindowSeconds(60).Obj()
	snapshot := snapshotForTest([]kueuealpha.PreemptionLimit{*perCQLimit}, nil)

	cases := map[string]struct {
		limit policy.PreemptionLimitReference
		want  kueuealpha.PreemptionLimitAggregation
	}{
		"limit in snapshot returns its aggregation": {
			limit: "per-cq",
			want:  kueuealpha.ByPreempteeCQLimitAggregation,
		},
		"limit missing from snapshot returns empty aggregation": {
			limit: "missing",
			want:  "",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := snapshot.aggregation(tc.limit); got != tc.want {
				t.Errorf("aggregation(%q) = %q, want %q", tc.limit, got, tc.want)
			}
		})
	}
}

func TestPreemptionLimitsSnapshotRecord(t *testing.T) {
	now := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	globalLimit := utiltestingalpha.MakePreemptionLimit("global", kueuealpha.GlobalLimitAggregation).
		Limit(5).WindowSeconds(60).Obj()
	perCQLimit := utiltestingalpha.MakePreemptionLimit("per-cq", kueuealpha.ByPreempteeCQLimitAggregation).
		Limit(3).WindowSeconds(60).Obj()
	globalConsumed := policy.NewConsumedLimit(globalLimit, policy.GlobalAggregationKey)
	perCQConsumed := func(cq policy.PreemptionLimitAggregationKey) policy.ConsumedLimit {
		return policy.NewConsumedLimit(perCQLimit, cq)
	}

	cases := map[string]struct {
		events eventsByLimit
		// recorded are the preemptions recorded in the snapshot after it was taken.
		recorded      []policy.ConsumedLimits
		wantAllowance map[policy.PreemptionLimitReference]policy.PreemptionLimitAggregationCounts
	}{
		"preemptions recorded in the snapshot are counted in allowance along with initial events": {
			events: eventsByLimit{
				"per-cq": {"cq-a": {now.Add(-30 * time.Second)}},
			},
			recorded: []policy.ConsumedLimits{{"per-cq": perCQConsumed("cq-a")}},
			wantAllowance: map[policy.PreemptionLimitReference]policy.PreemptionLimitAggregationCounts{
				"per-cq": {"cq-a": 1},
			},
		},
		"preemptions recorded in the snapshot are counted only for their aggregation key": {
			recorded: []policy.ConsumedLimits{{"per-cq": perCQConsumed("cq-a")}},
			wantAllowance: map[policy.PreemptionLimitReference]policy.PreemptionLimitAggregationCounts{
				"per-cq": {"cq-a": 2, "cq-b": 3},
			},
		},
		"multiple consumed limits are all counted in allowance": {
			events: eventsByLimit{
				"global": {policy.GlobalAggregationKey: {now.Add(-30 * time.Second)}},
			},
			recorded: []policy.ConsumedLimits{
				{"global": globalConsumed, "per-cq": perCQConsumed("cq-a")},
				{"global": globalConsumed, "per-cq": perCQConsumed("cq-b")},
			},
			wantAllowance: map[policy.PreemptionLimitReference]policy.PreemptionLimitAggregationCounts{
				"global": {policy.GlobalAggregationKey: 2},
				"per-cq": {"cq-a": 2, "cq-b": 2},
			},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			snapshot := snapshotForTest([]kueuealpha.PreemptionLimit{*globalLimit, *perCQLimit}, tc.events)
			for _, consumed := range tc.recorded {
				snapshot.Record(now, consumed)
			}
			for limit, keys := range tc.wantAllowance {
				for key, want := range keys {
					if got := snapshot.allowance(limit, key); got != want {
						t.Errorf("allowance(%q, %q) = %d, want %d", limit, key, got, want)
					}
				}
			}
		})
	}
}

func TestPreemptionLimitsSnapshotNextTimeSlot(t *testing.T) {
	now := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	global := utiltestingalpha.MakePreemptionLimit("global", kueuealpha.GlobalLimitAggregation).
		WindowSeconds(60)
	globalOne := global.Clone().Limit(1).Obj()
	globalConsumed := policy.ConsumedLimits{"global": policy.NewConsumedLimit(globalOne, policy.GlobalAggregationKey)}

	cases := map[string]struct {
		limits   []kueuealpha.PreemptionLimit
		events   []time.Time
		recorded []time.Time
		limit    policy.PreemptionLimitReference
		want     *time.Time
	}{
		"earliest active event expires after window": {
			limits: []kueuealpha.PreemptionLimit{*globalOne},
			events: []time.Time{now.Add(-40 * time.Second), now.Add(-10 * time.Second)},
			limit:  "global",
			want:   new(now.Add(20 * time.Second)),
		},
		"later event recorded in snapshot keeps earlier slot time": {
			limits:   []kueuealpha.PreemptionLimit{*globalOne},
			events:   []time.Time{now.Add(-20 * time.Second)},
			recorded: []time.Time{now.Add(5 * time.Second)},
			limit:    "global",
			want:     new(now.Add(40 * time.Second)),
		},
		"earlier event recorded in snapshot lowers slot time": {
			limits:   []kueuealpha.PreemptionLimit{*globalOne},
			events:   []time.Time{now.Add(-10 * time.Second)},
			recorded: []time.Time{now.Add(-25 * time.Second)},
			limit:    "global",
			want:     new(now.Add(35 * time.Second)),
		},
		"event recorded in empty snapshot sets slot time": {
			limits:   []kueuealpha.PreemptionLimit{*globalOne},
			recorded: []time.Time{now.Add(5 * time.Second)},
			limit:    "global",
			want:     new(now.Add(65 * time.Second)),
		},
		"no recorded events returns nil": {
			limits: []kueuealpha.PreemptionLimit{*globalOne},
			limit:  "global",
		},
		"limit of 0 never frees a slot": {
			limits: []kueuealpha.PreemptionLimit{*global.Clone().Limit(0).Obj()},
			events: []time.Time{now.Add(-20 * time.Second)},
			limit:  "global",
		},
		"unknown limit returns nil": {
			limits: []kueuealpha.PreemptionLimit{*globalOne},
			events: []time.Time{now.Add(-20 * time.Second)},
			limit:  "missing",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			var recorded eventsByLimit
			if len(tc.events) > 0 {
				recorded = eventsByLimit{"global": {policy.GlobalAggregationKey: tc.events}}
			}
			snapshot := snapshotForTest(tc.limits, recorded)
			for _, ts := range tc.recorded {
				snapshot.Record(ts, globalConsumed)
			}
			if diff := gocmp.Diff(tc.want, snapshot.nextTimeSlot(tc.limit, policy.GlobalAggregationKey)); diff != "" {
				t.Errorf("nextTimeSlot() (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestPreemptionLimitsSnapshotWithinLimits(t *testing.T) {
	now := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	perCQLimit := utiltestingalpha.MakePreemptionLimit("per-cq", kueuealpha.ByPreempteeCQLimitAggregation).
		Limit(2).WindowSeconds(60).Obj()
	globalLimit := utiltestingalpha.MakePreemptionLimit("global", kueuealpha.GlobalLimitAggregation).
		Limit(2).WindowSeconds(60).Obj()
	missingLimit := utiltestingalpha.MakePreemptionLimit("missing", kueuealpha.GlobalLimitAggregation).
		Limit(2).WindowSeconds(60).Obj()
	globalConsumed := policy.NewConsumedLimit(globalLimit, policy.GlobalAggregationKey)
	perCQConsumed := func(cq policy.PreemptionLimitAggregationKey) policy.ConsumedLimit {
		return policy.NewConsumedLimit(perCQLimit, cq)
	}

	cases := map[string]struct {
		events eventsByLimit
		// recorded are the preemptions recorded in the snapshot after it was taken.
		recorded         []policy.ConsumedLimits
		nilSnapshot      bool
		preemptions      []policy.ConsumedLimits
		wantWithinLimits bool
	}{
		"preemptions within the remaining limit are allowed": {
			events: eventsByLimit{
				"per-cq": {"cq-a": {now.Add(-30 * time.Second)}},
			},
			preemptions:      []policy.ConsumedLimits{{"per-cq": perCQConsumed("cq-a")}},
			wantWithinLimits: true,
		},
		"preemptions together exceeding the remaining limit are blocked": {
			events: eventsByLimit{
				"per-cq": {"cq-a": {now.Add(-30 * time.Second)}},
			},
			preemptions:      []policy.ConsumedLimits{{"per-cq": perCQConsumed("cq-a")}, {"per-cq": perCQConsumed("cq-a")}},
			wantWithinLimits: false,
		},
		"preemptions recorded in the snapshot are counted against the limit": {
			recorded:         []policy.ConsumedLimits{{"per-cq": perCQConsumed("cq-a")}, {"per-cq": perCQConsumed("cq-a")}},
			preemptions:      []policy.ConsumedLimits{{"per-cq": perCQConsumed("cq-a")}},
			wantWithinLimits: false,
		},
		"different aggregation keys are counted independently": {
			recorded:         []policy.ConsumedLimits{{"per-cq": perCQConsumed("cq-a")}},
			preemptions:      []policy.ConsumedLimits{{"per-cq": perCQConsumed("cq-b")}, {"per-cq": perCQConsumed("cq-b")}, {"per-cq": perCQConsumed("cq-a")}},
			wantWithinLimits: true,
		},
		"preemption is blocked when any of its consumed limits is exhausted": {
			recorded:         []policy.ConsumedLimits{{"global": globalConsumed}, {"global": globalConsumed}},
			preemptions:      []policy.ConsumedLimits{{"per-cq": perCQConsumed("cq-a"), "global": globalConsumed}},
			wantWithinLimits: false,
		},
		"preemption consuming a limit missing from the snapshot is blocked": {
			preemptions:      []policy.ConsumedLimits{{"missing": policy.NewConsumedLimit(missingLimit, policy.GlobalAggregationKey)}},
			wantWithinLimits: false,
		},
		"preemption is blocked when the snapshot is nil": {
			nilSnapshot:      true,
			preemptions:      []policy.ConsumedLimits{{"per-cq": perCQConsumed("cq-a")}},
			wantWithinLimits: false,
		},
		"preemption consuming no limits is blocked when the snapshot is nil": {
			nilSnapshot:      true,
			preemptions:      []policy.ConsumedLimits{{}},
			wantWithinLimits: false,
		},
		// True when there are no preemptions to block.
		"zero preemptions are allowed when the snapshot is nil": {
			nilSnapshot:      true,
			wantWithinLimits: true,
		},
		"zero preemptions are allowed": {
			wantWithinLimits: true,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			var snapshot *PreemptionLimitsSnapshot
			if !tc.nilSnapshot {
				snapshot = snapshotForTest([]kueuealpha.PreemptionLimit{*perCQLimit, *globalLimit}, tc.events)
				for _, consumed := range tc.recorded {
					snapshot.Record(now, consumed)
				}
			}
			if got := snapshot.WithinLimits(tc.preemptions); got != tc.wantWithinLimits {
				t.Errorf("WithinLimits(%v) = %v, want %v", tc.preemptions, got, tc.wantWithinLimits)
			}
		})
	}
}
