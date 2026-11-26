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

package config

import (
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/component-base/featuregate"
	testingclock "k8s.io/utils/clock/testing"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/resources"
	preemptexpectations "sigs.k8s.io/kueue/pkg/scheduler/preemption/expectations"
	"sigs.k8s.io/kueue/pkg/scheduler/preemption/limits"
	"sigs.k8s.io/kueue/pkg/scheduler/preemption/policy"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingalpha "sigs.k8s.io/kueue/pkg/util/testing/v1alpha1"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/pkg/workload"
)

func TestPreemptionEvaluatorPreemptionLimits(t *testing.T) {
	const windowSeconds = 60
	now := time.Now()
	clusterQueues := []*kueue.ClusterQueue{
		utiltestingapi.MakeClusterQueue("a").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
				Resource(corev1.ResourceCPU, "3").Obj()).
			Obj(),
		utiltestingapi.MakeClusterQueue("b").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
				Resource(corev1.ResourceCPU, "3").Obj()).
			Obj(),
	}
	fr := resources.FlavorResource{Flavor: "default", Resource: corev1.ResourceCPU}
	// preemptionConfig selects the low tier workloads of any ClusterQueue unconditionally,
	// and the other ones while the quota is insufficient, which it always is in this test.
	preemptionConfig := utiltestingalpha.MakePreemptionConfig("test").
		Rule("low-tier-rule", kueuealpha.Always,
			utiltestingalpha.MakeCandidateSelector(kueuealpha.AnyClusterQueue).
				LabelSelector(&metav1.LabelSelector{MatchLabels: map[string]string{"tier": "low"}}).Obj(),
		).
		Rule("any-cluster-queue-rule", kueuealpha.InsufficientQuota,
			utiltestingalpha.MakeCandidateSelector(kueuealpha.AnyClusterQueue).Obj(),
		).
		Obj()
	unitWl := *utiltestingapi.MakeWorkload("unit", "").Request(corev1.ResourceCPU, "1")
	globalLimit := utiltestingalpha.MakePreemptionLimit("global", kueuealpha.GlobalLimitAggregation).
		UID("global-uid").
		WindowSeconds(windowSeconds)
	perPreempteeCQLimit := utiltestingalpha.MakePreemptionLimit("per-preemptee-cq", kueuealpha.ByPreempteeCQLimitAggregation).
		UID("per-preemptee-cq-uid").
		WindowSeconds(windowSeconds)
	globalConsumed := policy.NewConsumedLimit(globalLimit.Obj(), policy.GlobalAggregationKey)
	perPreempteeCQAConsumed := policy.NewConsumedLimit(perPreempteeCQLimit.Obj(), "a")
	perPreempteeCQBConsumed := policy.NewConsumedLimit(perPreempteeCQLimit.Obj(), "b")

	cases := map[string]struct {
		disableLimits    bool
		noSnapshot       bool
		preemptionLimits []kueuealpha.PreemptionLimit
		admitted         []kueue.Workload
		// unobservedEvictions are the names of the admitted workloads whose eviction
		// was issued but not observed yet.
		unobservedEvictions []string
		// findCandidatesTwice checks the candidates of a second FindCandidates call
		// of the same evaluator, rather than those of the first one.
		findCandidatesTwice bool
		want                map[string]policy.ConsumedLimits
	}{
		"matching limits are consumed": {
			preemptionLimits: []kueuealpha.PreemptionLimit{*globalLimit.Clone().Limit(10).Obj(), *perPreempteeCQLimit.Clone().Limit(10).Obj()},
			admitted: []kueue.Workload{
				*unitWl.Clone().Name("a1").SimpleReserveQuota("a", "default", now).Obj(),
				*unitWl.Clone().Name("b1").SimpleReserveQuota("b", "default", now).Obj(),
			},
			want: map[string]policy.ConsumedLimits{
				"/a1": {"global": globalConsumed, "per-preemptee-cq": perPreempteeCQAConsumed},
				"/b1": {"global": globalConsumed, "per-preemptee-cq": perPreempteeCQBConsumed},
			},
		},
		"limit of 0 blocks every candidate": {
			preemptionLimits: []kueuealpha.PreemptionLimit{*globalLimit.Clone().Limit(0).Obj()},
			admitted: []kueue.Workload{
				*unitWl.Clone().Name("a1").SimpleReserveQuota("a", "default", now).Obj(),
				*unitWl.Clone().Name("b1").SimpleReserveQuota("b", "default", now).Obj(),
			},
			want: map[string]policy.ConsumedLimits{},
		},
		"candidate exceeding a limit is skipped, and later candidates are still yielded": {
			preemptionLimits: []kueuealpha.PreemptionLimit{*perPreempteeCQLimit.Clone().Limit(1).Obj()},
			admitted: []kueue.Workload{
				*unitWl.Clone().Name("a1").SimpleReserveQuota("a", "default", now).Obj(),
				*unitWl.Clone().Name("a2").SimpleReserveQuota("a", "default", now).Obj(),
				*unitWl.Clone().Name("b1").SimpleReserveQuota("b", "default", now).Obj(),
			},
			want: map[string]policy.ConsumedLimits{
				"/a1": {"per-preemptee-cq": perPreempteeCQAConsumed},
				"/b1": {"per-preemptee-cq": perPreempteeCQBConsumed},
			},
		},
		"limits not selecting the PreemptionConfig are neither enforced nor attached": {
			preemptionLimits: []kueuealpha.PreemptionLimit{
				*utiltestingalpha.MakePreemptionLimit("other-configs", kueuealpha.GlobalLimitAggregation).UID("other-configs-uid").Limit(0).WindowSeconds(windowSeconds).
					ConfigSelector(metav1.LabelSelector{MatchLabels: map[string]string{"tier": "other"}}).Obj(),
			},
			admitted: []kueue.Workload{
				*unitWl.Clone().Name("a1").SimpleReserveQuota("a", "default", now).Obj(),
			},
			want: map[string]policy.ConsumedLimits{
				"/a1": nil,
			},
		},
		"candidates whose preemption is in progress are yielded without limits and take no budget": {
			// a2 comes while the budget is partly used, and a4 once it is exhausted.
			preemptionLimits: []kueuealpha.PreemptionLimit{*globalLimit.Clone().Limit(2).Obj()},
			admitted: []kueue.Workload{
				*unitWl.Clone().Name("a1").SimpleReserveQuota("a", "default", now).Obj(),
				*unitWl.Clone().Name("a2").SimpleReserveQuota("a", "default", now).Obj(),
				*unitWl.Clone().Name("a3").SimpleReserveQuota("a", "default", now).Obj(),
				*unitWl.Clone().Name("a4").SimpleReserveQuota("a", "default", now).Obj(),
			},
			unobservedEvictions: []string{"a2", "a4"},
			want: map[string]policy.ConsumedLimits{
				"/a1": {"global": globalConsumed},
				"/a2": nil,
				"/a3": {"global": globalConsumed},
				"/a4": nil,
			},
		},
		"budget is shared by the triggers of one call": {
			preemptionLimits: []kueuealpha.PreemptionLimit{*globalLimit.Clone().Limit(1).Obj()},
			admitted: []kueue.Workload{
				*unitWl.Clone().Name("a1").Label("tier", "low").SimpleReserveQuota("a", "default", now).Obj(),
				*unitWl.Clone().Name("a2").SimpleReserveQuota("a", "default", now).Obj(),
			},
			want: map[string]policy.ConsumedLimits{
				"/a1": {"global": globalConsumed},
			},
		},
		"budget starts afresh at each FindCandidates call": {
			preemptionLimits: []kueuealpha.PreemptionLimit{*globalLimit.Clone().Limit(1).Obj()},
			admitted: []kueue.Workload{
				*unitWl.Clone().Name("a1").SimpleReserveQuota("a", "default", now).Obj(),
				*unitWl.Clone().Name("a2").SimpleReserveQuota("a", "default", now).Obj(),
			},
			findCandidatesTwice: true,
			want: map[string]policy.ConsumedLimits{
				"/a1": {"global": globalConsumed},
			},
		},
		"limits are neither enforced nor attached when the ConfigurablePreemptionLimits feature is disabled": {
			disableLimits:    true,
			preemptionLimits: []kueuealpha.PreemptionLimit{*globalLimit.Clone().Limit(0).Obj()},
			admitted: []kueue.Workload{
				*unitWl.Clone().Name("a1").SimpleReserveQuota("a", "default", now).Obj(),
				*unitWl.Clone().Name("a2").SimpleReserveQuota("a", "default", now).Obj(),
			},
			want: map[string]policy.ConsumedLimits{
				"/a1": nil,
				"/a2": nil,
			},
		},
		"no candidates are yielded without a snapshot of the PreemptionLimits": {
			noSnapshot:       true,
			preemptionLimits: []kueuealpha.PreemptionLimit{*globalLimit.Clone().Limit(10).Obj()},
			admitted: []kueue.Workload{
				*unitWl.Clone().Name("a1").SimpleReserveQuota("a", "default", now).Obj(),
			},
			want: map[string]policy.ConsumedLimits{},
		},
		"no candidates are yielded when a PreemptionLimit has an invalid selector": {
			preemptionLimits: []kueuealpha.PreemptionLimit{
				*utiltestingalpha.MakePreemptionLimit("invalid", kueuealpha.GlobalLimitAggregation).UID("invalid-uid").Limit(10).WindowSeconds(windowSeconds).
					PreemptorClusterQueueSelector(&metav1.LabelSelector{
						MatchExpressions: []metav1.LabelSelectorRequirement{{Key: "tier", Operator: "invalid"}},
					}).Obj(),
			},
			admitted: []kueue.Workload{
				*unitWl.Clone().Name("a1").SimpleReserveQuota("a", "default", now).Obj(),
			},
			want: map[string]policy.ConsumedLimits{},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			// Given
			features.SetFeatureGatesDuringTest(t, map[featuregate.Feature]bool{
				features.ConfigurablePreemptions:      true,
				features.ConfigurablePreemptionLimits: !tc.disableLimits,
			})
			ctx, log := utiltesting.ContextWithLog(t)
			for i := range tc.admitted {
				tc.admitted[i].UID = types.UID(tc.admitted[i].Name)
			}
			cl := utiltesting.NewClientBuilder().
				WithObjects(preemptionConfig).
				WithLists(&kueue.WorkloadList{Items: tc.admitted}).
				Build()
			cqCache := schdcache.New(cl)
			cqCache.AddOrUpdateResourceFlavor(log, utiltestingapi.MakeResourceFlavor("default").Obj())
			for _, cq := range clusterQueues {
				if err := cqCache.AddClusterQueue(ctx, cq); err != nil {
					t.Fatalf("Couldn't add ClusterQueue to cache: %v", err)
				}
			}
			// The scheduling cycle has no snapshot if it was taken while the
			// ConfigurablePreemptionLimits feature was disabled.
			var preemptionLimits *limits.PreemptionLimitsSnapshot
			if !tc.noSnapshot {
				tracker := limits.NewPreemptionLimitTracker()
				for i := range tc.preemptionLimits {
					tracker.Sync(&tc.preemptionLimits[i])
				}
				preemptionLimits = tracker.Snapshot(now)
			}
			preemptionExpectations := preemptexpectations.New()
			for _, name := range tc.unobservedEvictions {
				preemptionExpectations.ExpectUIDs(log, types.NamespacedName{Name: name}, []types.UID{types.UID(name)})
			}
			preemptor := workload.NewInfo(log, unitWl.Clone().Name("a_incoming").Obj())
			preemptor.ClusterQueue = "a"
			evaluator := NewEvaluatorForPreemptionConfig(ctx, log, testingclock.NewFakeClock(now), cl, preemptionLimits, preemptionExpectations, preemptionConfig.Name, preemptor, candidatesByName)

			// When
			calls := 1
			if tc.findCandidatesTwice {
				calls = 2
			}
			var got map[string]policy.ConsumedLimits
			for range calls {
				snapshot, err := cqCache.Snapshot(ctx)
				if err != nil {
					t.Fatalf("unexpected error while building snapshot: %v", err)
				}
				got = map[string]policy.ConsumedLimits{}
				evaluator.FindCandidates(snapshot, sets.New(fr), func() bool { return false }, func(target *policy.Target) bool {
					got[string(workload.Key(target.WorkloadInfo.Obj))] = target.ConfigurablePreemptionReasonData.ConsumedLimits
					return true
				})
			}

			// Then
			if diff := cmp.Diff(tc.want, got, cmp.AllowUnexported(policy.ConsumedLimit{})); diff != "" {
				t.Errorf("Unexpected consumed limits (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestPreemptionEvaluatorPreemptionLimitsRetryAt(t *testing.T) {
	const windowSeconds = 60
	now := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	clusterQueues := []*kueue.ClusterQueue{
		utiltestingapi.MakeClusterQueue("a").Label("cq", "a").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
				Resource(corev1.ResourceCPU, "3").
				Resource(corev1.ResourceMemory, "3").Obj()).Obj(),
		utiltestingapi.MakeClusterQueue("b").Label("cq", "b").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "3").Obj()).Obj(),
		utiltestingapi.MakeClusterQueue("c").Label("cq", "c").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "3").Obj()).Obj(),
	}
	fr := resources.FlavorResource{Flavor: "default", Resource: corev1.ResourceCPU}
	frMem := resources.FlavorResource{Flavor: "default", Resource: corev1.ResourceMemory}
	emptyConfig := utiltestingalpha.MakePreemptionConfig("empty").Obj()
	multiTriggerConfig := utiltestingalpha.MakePreemptionConfig("test").
		Rule("low-tier-rule", kueuealpha.Always,
			utiltestingalpha.MakeCandidateSelector(kueuealpha.AnyClusterQueue).
				LabelSelector(&metav1.LabelSelector{MatchLabels: map[string]string{"tier": "low"}}).Obj(),
		).
		Rule("any-cluster-queue-rule", kueuealpha.InsufficientQuota,
			utiltestingalpha.MakeCandidateSelector(kueuealpha.AnyClusterQueue).Obj(),
		).Obj()
	unitWl := *utiltestingapi.MakeWorkload("unit", "").Request(corev1.ResourceCPU, "1")
	perPreempteeCQLimit := utiltestingalpha.MakePreemptionLimit("per-preemptee-cq", kueuealpha.ByPreempteeCQLimitAggregation).
		UID("per-preemptee-cq-uid").
		Limit(1).WindowSeconds(windowSeconds).Obj()
	perPreempteeCQAConsumed := policy.ConsumedLimits{"per-preemptee-cq": policy.NewConsumedLimit(perPreempteeCQLimit, "a")}
	perPreempteeCQBConsumed := policy.ConsumedLimits{"per-preemptee-cq": policy.NewConsumedLimit(perPreempteeCQLimit, "b")}
	perPreempteeCQCConsumed := policy.ConsumedLimits{"per-preemptee-cq": policy.NewConsumedLimit(perPreempteeCQLimit, "c")}
	zeroBLimit := utiltestingalpha.MakePreemptionLimit("zero-b", kueuealpha.ByPreempteeCQLimitAggregation).
		UID("zero-b-uid").
		Limit(0).WindowSeconds(windowSeconds).
		PreempteeClusterQueueSelector(&metav1.LabelSelector{MatchLabels: map[string]string{"cq": "b"}}).Obj()

	type recordedEvent struct {
		ts       time.Time
		consumed policy.ConsumedLimits
	}

	cases := map[string]struct {
		config           *kueuealpha.PreemptionConfig // Nil leaves the evaluator nil.
		preemptionLimits []kueuealpha.PreemptionLimit
		recordedEvents   []recordedEvent
		admitted         []kueue.Workload
		maxYields        int
		// frsPerCall, when set, runs FindCandidates once per FlavorResource instead of once for fr.
		frsPerCall   []resources.FlavorResource
		wantYielded  []string
		wantRetryAt  *time.Time
		wantBlocking *policy.BlockingPreemptionLimit
	}{
		"nil evaluator returns nil": {},
		"evaluator without rules returns nil": {
			config:           emptyConfig,
			preemptionLimits: []kueuealpha.PreemptionLimit{*perPreempteeCQLimit},
			recordedEvents: []recordedEvent{
				{ts: now.Add(-20 * time.Second), consumed: perPreempteeCQAConsumed},
			},
			admitted: []kueue.Workload{
				*unitWl.Clone().Name("a1").SimpleReserveQuota("a", "default", now).Obj(),
			},
		},
		"skipped candidate updates RetryAt and BlockingLimit while later allowed candidate is still yielded": {
			config:           multiTriggerConfig,
			preemptionLimits: []kueuealpha.PreemptionLimit{*perPreempteeCQLimit},
			recordedEvents: []recordedEvent{
				{ts: now.Add(-20 * time.Second), consumed: perPreempteeCQAConsumed},
			},
			admitted: []kueue.Workload{
				*unitWl.Clone().Name("a1").SimpleReserveQuota("a", "default", now).Obj(),
				*unitWl.Clone().Name("b1").SimpleReserveQuota("b", "default", now).Obj(),
			},
			wantYielded: []string{"/b1"},
			wantRetryAt: new(now.Add(40 * time.Second)),
			wantBlocking: &policy.BlockingPreemptionLimit{
				Limit:       "per-preemptee-cq",
				Aggregation: kueuealpha.ByPreempteeCQLimitAggregation,
				RetryAt:     new(now.Add(40 * time.Second)),
			},
		},
		"earlier RetryAt from Always trigger is preserved when InsufficientQuota expires later": {
			config:           multiTriggerConfig,
			preemptionLimits: []kueuealpha.PreemptionLimit{*perPreempteeCQLimit},
			recordedEvents: []recordedEvent{
				{ts: now.Add(-45 * time.Second), consumed: perPreempteeCQAConsumed},
				{ts: now.Add(-20 * time.Second), consumed: perPreempteeCQBConsumed},
			},
			admitted: []kueue.Workload{
				*unitWl.Clone().Name("a1").Label("tier", "low").SimpleReserveQuota("a", "default", now).Obj(),
				*unitWl.Clone().Name("b1").SimpleReserveQuota("b", "default", now).Obj(),
			},
			wantRetryAt: new(now.Add(15 * time.Second)),
			wantBlocking: &policy.BlockingPreemptionLimit{
				Limit:       "per-preemptee-cq",
				Aggregation: kueuealpha.ByPreempteeCQLimitAggregation,
				RetryAt:     new(now.Add(15 * time.Second)),
			},
		},
		"later RetryAt from Always trigger is lowered when InsufficientQuota expires earlier": {
			config:           multiTriggerConfig,
			preemptionLimits: []kueuealpha.PreemptionLimit{*perPreempteeCQLimit},
			recordedEvents: []recordedEvent{
				{ts: now.Add(-20 * time.Second), consumed: perPreempteeCQAConsumed},
				{ts: now.Add(-45 * time.Second), consumed: perPreempteeCQBConsumed},
			},
			admitted: []kueue.Workload{
				*unitWl.Clone().Name("a1").Label("tier", "low").SimpleReserveQuota("a", "default", now).Obj(),
				*unitWl.Clone().Name("b1").SimpleReserveQuota("b", "default", now).Obj(),
			},
			wantRetryAt: new(now.Add(15 * time.Second)),
			wantBlocking: &policy.BlockingPreemptionLimit{
				Limit:       "per-preemptee-cq",
				Aggregation: kueuealpha.ByPreempteeCQLimitAggregation,
				RetryAt:     new(now.Add(15 * time.Second)),
			},
		},
		"interrupted yield preserves RetryAt and BlockingLimit from candidates evaluated before halting": {
			config:           multiTriggerConfig,
			preemptionLimits: []kueuealpha.PreemptionLimit{*perPreempteeCQLimit},
			recordedEvents: []recordedEvent{
				{ts: now.Add(-20 * time.Second), consumed: perPreempteeCQAConsumed},
				{ts: now.Add(-50 * time.Second), consumed: perPreempteeCQCConsumed},
			},
			admitted: []kueue.Workload{
				*unitWl.Clone().Name("a1").SimpleReserveQuota("a", "default", now).Obj(),
				*unitWl.Clone().Name("b1").SimpleReserveQuota("b", "default", now).Obj(),
				*unitWl.Clone().Name("c1").SimpleReserveQuota("c", "default", now).Obj(),
			},
			maxYields:   1,
			wantYielded: []string{"/b1"},
			wantRetryAt: new(now.Add(40 * time.Second)),
			wantBlocking: &policy.BlockingPreemptionLimit{
				Limit:       "per-preemptee-cq",
				Aggregation: kueuealpha.ByPreempteeCQLimitAggregation,
				RetryAt:     new(now.Add(40 * time.Second)),
			},
		},
		"limit of 0 on one candidate does not mask expiring limit on another candidate": {
			config: multiTriggerConfig,
			preemptionLimits: []kueuealpha.PreemptionLimit{
				*perPreempteeCQLimit,
				*zeroBLimit,
			},
			recordedEvents: []recordedEvent{
				{ts: now.Add(-45 * time.Second), consumed: perPreempteeCQAConsumed},
			},
			admitted: []kueue.Workload{
				*unitWl.Clone().Name("a1").SimpleReserveQuota("a", "default", now).Obj(),
				*unitWl.Clone().Name("b1").SimpleReserveQuota("b", "default", now).Obj(),
			},
			wantRetryAt: new(now.Add(15 * time.Second)),
			wantBlocking: &policy.BlockingPreemptionLimit{
				Limit:       "per-preemptee-cq",
				Aggregation: kueuealpha.ByPreempteeCQLimitAggregation,
				RetryAt:     new(now.Add(15 * time.Second)),
			},
		},
		"all skipped candidates hitting limit of 0 record non-expiring BlockingLimit and nil RetryAt": {
			config:           multiTriggerConfig,
			preemptionLimits: []kueuealpha.PreemptionLimit{*zeroBLimit},
			admitted: []kueue.Workload{
				*unitWl.Clone().Name("b1").SimpleReserveQuota("b", "default", now).Obj(),
			},
			wantBlocking: &policy.BlockingPreemptionLimit{
				Limit:       "zero-b",
				Aggregation: kueuealpha.ByPreempteeCQLimitAggregation,
			},
		},
		"RetryAt from earlier FindCandidates call is preserved when subsequent call hits no limits": {
			config:           multiTriggerConfig,
			preemptionLimits: []kueuealpha.PreemptionLimit{*perPreempteeCQLimit},
			recordedEvents: []recordedEvent{
				{ts: now.Add(-45 * time.Second), consumed: perPreempteeCQBConsumed},
			},
			admitted: []kueue.Workload{
				*unitWl.Clone().Name("b1").SimpleReserveQuota("b", "default", now).Obj(),
				*utiltestingapi.MakeWorkload("a1", "").Request(corev1.ResourceMemory, "1").SimpleReserveQuota("a", "default", now).Obj(),
			},
			frsPerCall:  []resources.FlavorResource{fr, frMem},
			wantYielded: []string{"/a1"},
			wantRetryAt: new(now.Add(15 * time.Second)),
			wantBlocking: &policy.BlockingPreemptionLimit{
				Limit:       "per-preemptee-cq",
				Aggregation: kueuealpha.ByPreempteeCQLimitAggregation,
				RetryAt:     new(now.Add(15 * time.Second)),
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGatesDuringTest(t, map[featuregate.Feature]bool{
				features.ConfigurablePreemptions:      true,
				features.ConfigurablePreemptionLimits: true,
			})
			ctx, log := utiltesting.ContextWithLog(t)
			for i := range tc.admitted {
				tc.admitted[i].UID = types.UID(tc.admitted[i].Name)
			}
			builder := utiltesting.NewClientBuilder().WithLists(&kueue.WorkloadList{Items: tc.admitted})
			if tc.config != nil {
				builder = builder.WithObjects(tc.config)
			}
			cl := builder.Build()
			cqCache := schdcache.New(cl)
			cqCache.AddOrUpdateResourceFlavor(log, utiltestingapi.MakeResourceFlavor("default").Obj())
			for _, cq := range clusterQueues {
				if err := cqCache.AddClusterQueue(ctx, cq); err != nil {
					t.Fatalf("Couldn't add ClusterQueue to cache: %v", err)
				}
			}

			tracker := limits.NewPreemptionLimitTracker()
			for i := range tc.preemptionLimits {
				tracker.Sync(&tc.preemptionLimits[i])
			}
			for _, ev := range tc.recordedEvents {
				tracker.Record(ev.ts, ev.consumed)
			}
			limitsSnapshot := tracker.Snapshot(now)

			var evaluator *PreemptionEvaluator
			if tc.config != nil {
				preemptor := workload.NewInfo(log, unitWl.Clone().Name("a_incoming").Obj())
				preemptor.ClusterQueue = "a"
				evaluator = NewEvaluatorForPreemptionConfig(ctx, log, testingclock.NewFakeClock(now), cl, limitsSnapshot, preemptexpectations.New(), tc.config.Name, preemptor, candidatesByName)
			}

			frsPerCall := tc.frsPerCall
			if len(frsPerCall) == 0 {
				frsPerCall = []resources.FlavorResource{fr}
			}
			var gotYielded []string
			for _, callFR := range frsPerCall {
				snapshot, err := cqCache.Snapshot(ctx)
				if err != nil {
					t.Fatalf("unexpected error while building snapshot: %v", err)
				}
				evaluator.FindCandidates(snapshot, sets.New(callFR), func() bool { return false }, func(target *policy.Target) bool {
					gotYielded = append(gotYielded, string(workload.Key(target.WorkloadInfo.Obj)))
					return tc.maxYields == 0 || len(gotYielded) < tc.maxYields
				})
			}

			if diff := cmp.Diff(tc.wantYielded, gotYielded, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("FindCandidates() yielded (-want,+got):\n%s", diff)
			}
			if diff := cmp.Diff(tc.wantRetryAt, evaluator.RetryAt()); diff != "" {
				t.Errorf("evaluator.RetryAt() (-want,+got):\n%s", diff)
			}
			if diff := cmp.Diff(tc.wantBlocking, evaluator.BlockingLimit()); diff != "" {
				t.Errorf("evaluator.BlockingLimit() (-want,+got):\n%s", diff)
			}
		})
	}
}
