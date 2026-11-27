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

package preemption

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/component-base/featuregate"
	clocktesting "k8s.io/utils/clock/testing"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/scheduler/flavorassigner"
	preemptexpectations "sigs.k8s.io/kueue/pkg/scheduler/preemption/expectations"
	"sigs.k8s.io/kueue/pkg/scheduler/preemption/limits"
	"sigs.k8s.io/kueue/pkg/scheduler/preemption/policy"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingalpha "sigs.k8s.io/kueue/pkg/util/testing/v1alpha1"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/pkg/workload"
)

func TestGetTargetsWithStrategyPreemptionLimitsRetryAt(t *testing.T) {
	now := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	preemptConfig := utiltestingalpha.MakePreemptionConfig("cfg").
		Rule("always-low", kueuealpha.Always,
			utiltestingalpha.MakeCandidateSelector(kueuealpha.WithinClusterQueue).
				LabelSelector(&metav1.LabelSelector{MatchLabels: map[string]string{"tier": "low"}}).Obj(),
		).
		Rule("quota-any", kueuealpha.InsufficientQuota,
			utiltestingalpha.MakeCandidateSelector(kueuealpha.AnyClusterQueue).Obj(),
		).Obj()
	const perCQLimitName = "per-cq"
	perCQLimit := utiltestingalpha.MakePreemptionLimit(perCQLimitName, kueuealpha.ByPreempteeCQLimitAggregation).
		UID("per-cq-uid").
		Limit(1).WindowSeconds(60).Obj()
	clusterQueues := []*kueue.ClusterQueue{
		utiltestingapi.MakeClusterQueue("cq-a").Cohort("cohort").
			Annotation(kueuealpha.PreemptionConfigNameAnnotation, "cfg").
			ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas("on-demand").Resource(corev1.ResourceCPU, "1").Obj(),
				*utiltestingapi.MakeFlavorQuotas("spot").Resource(corev1.ResourceCPU, "1").Obj(),
			).Obj(),
		utiltestingapi.MakeClusterQueue("cq-b").Cohort("cohort").
			ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas("on-demand").Resource(corev1.ResourceCPU, "1").Obj(),
				*utiltestingapi.MakeFlavorQuotas("spot").Resource(corev1.ResourceCPU, "1").Obj(),
			).Obj(),
	}
	admitted := []kueue.Workload{
		*utiltestingapi.MakeWorkload("v-a", "default").UID("v-a").Label("tier", "low").
			Request(corev1.ResourceCPU, "1").SimpleReserveQuota("cq-a", "on-demand", now).Obj(),
		*utiltestingapi.MakeWorkload("v-b", "default").UID("v-b").
			Request(corev1.ResourceCPU, "1").SimpleReserveQuota("cq-b", "on-demand", now).Obj(),
		*utiltestingapi.MakeWorkload("v-spot", "default").UID("v-spot").
			Request(corev1.ResourceCPU, "1").SimpleReserveQuota("cq-b", "spot", now).Obj(),
	}

	type searchAttempt struct {
		flavor           kueue.ResourceFlavorReference
		strategyAccepted []bool
	}

	cases := map[string]struct {
		recordedAt   map[policy.PreemptionLimitAggregationKey]time.Time
		attempts     []searchAttempt
		wantTargets  []string
		wantRetryAt  *time.Time
		wantBlocking *policy.BlockingPreemptionLimit
	}{
		"TakeRetryAt returns nil when no strategies run": {},
		"failed candidate search records earliest retry and TakeRetryAt clears it": {
			recordedAt: map[policy.PreemptionLimitAggregationKey]time.Time{
				"cq-a": now.Add(-45 * time.Second),
				"cq-b": now.Add(-20 * time.Second),
			},
			attempts: []searchAttempt{
				{flavor: "on-demand", strategyAccepted: []bool{true}},
			},
			wantRetryAt: new(now.Add(15 * time.Second)),
			wantBlocking: &policy.BlockingPreemptionLimit{
				Limit:       perCQLimitName,
				Aggregation: kueuealpha.ByPreempteeCQLimitAggregation,
				RetryAt:     new(now.Add(15 * time.Second)),
			},
		},
		"TakeRetryAt returns earliest across multiple failed searches": {
			recordedAt: map[policy.PreemptionLimitAggregationKey]time.Time{
				"cq-a": now.Add(-45 * time.Second),
				"cq-b": now.Add(-20 * time.Second),
			},
			attempts: []searchAttempt{
				{flavor: "on-demand", strategyAccepted: []bool{true}},
				{flavor: "spot", strategyAccepted: []bool{true}},
			},
			wantRetryAt: new(now.Add(15 * time.Second)),
			wantBlocking: &policy.BlockingPreemptionLimit{
				Limit:       perCQLimitName,
				Aggregation: kueuealpha.ByPreempteeCQLimitAggregation,
				RetryAt:     new(now.Add(15 * time.Second)),
			},
		},
		"rejected strategy verification preserves retry from skipped candidates": {
			recordedAt: map[policy.PreemptionLimitAggregationKey]time.Time{
				"cq-a": now.Add(-45 * time.Second),
			},
			attempts: []searchAttempt{
				{flavor: "on-demand", strategyAccepted: []bool{false}},
			},
			wantRetryAt: new(now.Add(15 * time.Second)),
			wantBlocking: &policy.BlockingPreemptionLimit{
				Limit:       perCQLimitName,
				Aggregation: kueuealpha.ByPreempteeCQLimitAggregation,
				RetryAt:     new(now.Add(15 * time.Second)),
			},
		},
		"failed earlier strategy does not set retry when subsequent strategy succeeds": {
			recordedAt: map[policy.PreemptionLimitAggregationKey]time.Time{
				"cq-a": now.Add(-45 * time.Second),
			},
			attempts: []searchAttempt{
				{flavor: "on-demand", strategyAccepted: []bool{false, true}},
			},
			wantTargets: []string{"default/v-b"},
			wantRetryAt: nil,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGatesDuringTest(t, map[featuregate.Feature]bool{
				features.ConfigurablePreemptions:      true,
				features.ConfigurablePreemptionLimits: true,
			})
			ctx, log := utiltesting.ContextWithLog(t)

			cl := utiltesting.NewClientBuilder().
				WithObjects(preemptConfig, perCQLimit).
				WithLists(&kueue.WorkloadList{Items: admitted}).
				Build()
			cqCache := schdcache.New(cl)
			cqCache.AddOrUpdateResourceFlavor(log, utiltestingapi.MakeResourceFlavor("on-demand").Obj())
			cqCache.AddOrUpdateResourceFlavor(log, utiltestingapi.MakeResourceFlavor("spot").Obj())
			for _, cq := range clusterQueues {
				if err := cqCache.AddClusterQueue(ctx, cq); err != nil {
					t.Fatalf("Couldn't add ClusterQueue to cache: %v", err)
				}
			}
			snapshot, err := cqCache.Snapshot(ctx)
			if err != nil {
				t.Fatalf("unexpected error while building snapshot: %v", err)
			}

			tracker := limits.NewPreemptionLimitTracker()
			tracker.Sync(perCQLimit)
			for cqName, ts := range tc.recordedAt {
				tracker.Record(ts, policy.ConsumedLimits{perCQLimitName: policy.NewConsumedLimit(perCQLimit, cqName)})
			}

			preemptor := New(cl, workload.Ordering{}, &utiltesting.EventRecorder{}, nil, false, clocktesting.NewFakeClock(now), nil, preemptexpectations.New(), nil, tracker)
			preemptor.SnapshotPreemptionLimits()

			incoming := workload.NewInfo(log, utiltestingapi.MakeWorkload("incoming", "default").UID("incoming").
				Request(corev1.ResourceCPU, "1").Obj())
			incoming.ClusterQueue = "cq-a"

			var gotTargets []string
			for _, attempt := range tc.attempts {
				assignment := singlePodSetAssignment(flavorassigner.ResourceAssignment{
					corev1.ResourceCPU: {Name: attempt.flavor, Mode: flavorassigner.Preempt},
				})
				baseStrategies := preemptor.GetPreemptionStrategyIterator(ctx, *incoming, snapshot, assignment)
				strategies := func(yield func(PreemptionStrategy) bool) {
					for base := range baseStrategies {
						for _, accepted := range attempt.strategyAccepted {
							s := base
							s.verify = func([]*Target) (bool, string) { return accepted, "" }
							if !yield(s) {
								return
							}
						}
					}
				}
				for _, target := range preemptor.GetTargetsWithStrategy(ctx, strategies) {
					gotTargets = append(gotTargets, string(workload.Key(target.WorkloadInfo.Obj)))
				}
			}

			if diff := cmp.Diff(tc.wantTargets, gotTargets, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("GetTargetsWithStrategy() targets (-want,+got):\n%s", diff)
			}
			if diff := cmp.Diff(tc.wantRetryAt, preemptor.TakeRetryAt()); diff != "" {
				t.Errorf("TakeRetryAt() (-want,+got):\n%s", diff)
			}
			if second := preemptor.TakeRetryAt(); second != nil {
				t.Errorf("Second TakeRetryAt() = %v, want nil", second)
			}
			if diff := cmp.Diff(tc.wantBlocking, preemptor.TakeBlockingLimit()); diff != "" {
				t.Errorf("TakeBlockingLimit() (-want,+got):\n%s", diff)
			}
			if secondBlocking := preemptor.TakeBlockingLimit(); secondBlocking != nil {
				t.Errorf("Second TakeBlockingLimit() = %v, want nil", secondBlocking)
			}
		})
	}
}

func TestIssuePreemptionsRecordsPreemptionLimits(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	rf := utiltestingapi.MakeResourceFlavor("default").Obj()
	cq := utiltestingapi.MakeClusterQueue("standalone").
		ResourceGroup(
			*utiltestingapi.MakeFlavorQuotas("default").
				Resource(corev1.ResourceCPU, "6").
				Obj(),
		).
		Obj()
	victim := utiltestingapi.MakeWorkload("victim", "default").
		UID("victim").
		ResourceVersion("1").
		Request(corev1.ResourceCPU, "2").
		ReserveQuotaAt(
			utiltestingapi.MakeAdmission("standalone").
				PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
					Assignment(corev1.ResourceCPU, "default", "2000m").
					Obj()).
				Obj(),
			now,
		)
	globalLimit := utiltestingalpha.MakePreemptionLimit("global", kueuealpha.GlobalLimitAggregation).UID("global-uid").Limit(1).WindowSeconds(60).Obj()
	perPreempteeWorkloadLimit := utiltestingalpha.MakePreemptionLimit("per-preemptee-workload", kueuealpha.ByPreempteeWorkloadLimitAggregation).
		UID("per-preemptee-workload-uid").Limit(1).WindowSeconds(60).Obj()
	consumedLimits := policy.ConsumedLimits{
		"global":                 policy.NewConsumedLimit(globalLimit, policy.GlobalAggregationKey),
		"per-preemptee-workload": policy.NewConsumedLimit(perPreempteeWorkloadLimit, "default/victim"),
	}
	configurableReasonData := &policy.ConfigurablePreemptionReasonData{
		ConfigName:     "config",
		ConsumedLimits: consumedLimits,
	}
	wantCounts := map[policy.PreemptionLimitReference]policy.PreemptionLimitAggregationCounts{
		"global":                 {policy.GlobalAggregationKey: 1},
		"per-preemptee-workload": {"default/victim": 1},
	}
	evictionErr := errors.New("simulated eviction error")

	cases := map[string]struct {
		disableLimits         bool
		victim                *kueue.Workload
		reason                string
		reasonData            *policy.ConfigurablePreemptionReasonData
		evictionErr           error
		issueTwice            bool
		wantCounts            map[policy.PreemptionLimitReference]policy.PreemptionLimitAggregationCounts
		wantWithinLimitsAfter bool
		wantNilSnapshotAfter  bool
	}{
		"configurable preemption is recorded": {
			victim:                victim.Clone().Obj(),
			reason:                kueue.ConfigurablePreemptionReason,
			reasonData:            configurableReasonData,
			wantCounts:            wantCounts,
			wantWithinLimitsAfter: false,
		},
		"configurable preemption is not recorded when the ConfigurablePreemptionLimits feature is disabled": {
			disableLimits:        true,
			victim:               victim.Clone().Obj(),
			reason:               kueue.ConfigurablePreemptionReason,
			reasonData:           configurableReasonData,
			wantNilSnapshotAfter: true,
		},
		"classical preemption is not recorded": {
			victim:                victim.Clone().Obj(),
			reason:                kueue.InClusterQueueReason,
			wantWithinLimitsAfter: true,
		},
		"failed eviction is not recorded": {
			victim:                victim.Clone().Obj(),
			reason:                kueue.ConfigurablePreemptionReason,
			reasonData:            configurableReasonData,
			evictionErr:           evictionErr,
			wantWithinLimitsAfter: true,
		},
		"already evicted victim is not recorded": {
			victim:                victim.Clone().EvictedAt(now).Obj(),
			reason:                kueue.ConfigurablePreemptionReason,
			reasonData:            configurableReasonData,
			wantWithinLimitsAfter: true,
		},
		"issuing the same preemption twice records it once": {
			victim:                victim.Clone().Obj(),
			reason:                kueue.ConfigurablePreemptionReason,
			reasonData:            configurableReasonData,
			issueTwice:            true,
			wantCounts:            wantCounts,
			wantWithinLimitsAfter: false,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGatesDuringTest(t, map[featuregate.Feature]bool{
				features.ConfigurablePreemptions:      true,
				features.ConfigurablePreemptionLimits: !tc.disableLimits,
				features.WorkloadRequestUseMergePatch: false,
			})
			ctx, log := utiltesting.ContextWithLog(t)
			cl := utiltesting.NewClientBuilder().
				WithObjects(tc.victim).
				WithStatusSubresource(&kueue.Workload{}).
				WithInterceptorFuncs(interceptor.Funcs{
					SubResourceApply: func(ctx context.Context, c client.Client, subResourceName string, applyConf runtime.ApplyConfiguration, opts ...client.SubResourceApplyOption) error {
						if tc.evictionErr != nil {
							return tc.evictionErr
						}
						return utiltesting.TreatSSAAsStrategicMergeForApplyConfiguration(ctx, c, subResourceName, applyConf, opts...)
					},
				}).
				Build()

			cqCache := schdcache.New(cl)
			cqCache.AddOrUpdateResourceFlavor(log, rf)
			if err := cqCache.AddClusterQueue(ctx, cq); err != nil {
				t.Fatalf("Couldn't add ClusterQueue to cache: %v", err)
			}
			snapshot, err := cqCache.Snapshot(ctx)
			if err != nil {
				t.Fatalf("unexpected error while building snapshot: %v", err)
			}

			// Only the PreemptionLimits synced by the PreemptionLimitReconciler are tracked.
			tracker := limits.NewPreemptionLimitTracker()
			tracker.Sync(globalLimit)
			tracker.Sync(perPreempteeWorkloadLimit)
			fakeClock := clocktesting.NewFakeClock(now)
			preemptor := New(cl, workload.Ordering{}, &utiltesting.EventRecorder{}, nil, false, fakeClock, nil, preemptexpectations.New(), nil, tracker)

			preemptorInfo := workload.NewInfo(log, utiltestingapi.MakeWorkload("preemptor", "default").
				UID("preemptor").
				Request(corev1.ResourceCPU, "2").
				Obj())
			preemptorInfo.ClusterQueue = "standalone"
			victimInfo := workload.NewInfo(log, tc.victim)
			victimInfo.ClusterQueue = "standalone"
			targets := []*Target{{
				WorkloadInfo:                     victimInfo,
				Reason:                           tc.reason,
				WorkloadCq:                       snapshot.ClusterQueue("standalone"),
				ConfigurablePreemptionReasonData: tc.reasonData,
			}}

			preemptor.SnapshotPreemptionLimits()
			fakeClock.Step(time.Second)
			_, _, err = preemptor.IssuePreemptions(ctx, cqCache, preemptorInfo, targets, snapshot.ClusterQueue("standalone"))
			if diff := cmp.Diff(tc.evictionErr, err, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("Unexpected IssuePreemptions() error (-want,+got):\n%s", diff)
			}
			if tc.issueTwice {
				// As in the next scheduling cycle, before the first eviction is observed.
				if _, _, err := preemptor.IssuePreemptions(ctx, cqCache, preemptorInfo, targets, snapshot.ClusterQueue("standalone")); err != nil {
					t.Fatalf("Second IssuePreemptions() failed: %v", err)
				}
			}

			evictedAt := fakeClock.Now()
			for _, limit := range []policy.PreemptionLimitReference{"global", "per-preemptee-workload"} {
				if diff := cmp.Diff(tc.wantCounts[limit], tracker.Counts(evictedAt, limit), cmpopts.EquateEmpty()); diff != "" {
					t.Errorf("Unexpected counts for PreemptionLimit %q (-want,+got):\n%s", limit, diff)
				}
			}
			if tc.wantNilSnapshotAfter {
				if preemptor.limitsSnapshot != nil {
					t.Errorf("Expected nil limitsSnapshot when feature is disabled, got %v", preemptor.limitsSnapshot)
				}
			} else {
				// Verify IssuePreemptions also updated the cycle limitsSnapshot.
				if got := preemptor.limitsSnapshot.WithinLimits([]policy.ConsumedLimits{consumedLimits}); got != tc.wantWithinLimitsAfter {
					t.Errorf("limitsSnapshot.WithinLimits() after IssuePreemptions = %v, want %v", got, tc.wantWithinLimitsAfter)
				}
			}
		})
	}
}

func TestIssuePreemptionsMultipleTargetsRecordsPreemptionLimits(t *testing.T) {
	features.SetFeatureGatesDuringTest(t, map[featuregate.Feature]bool{
		features.ConfigurablePreemptions:      true,
		features.ConfigurablePreemptionLimits: true,
		features.WorkloadRequestUseMergePatch: false,
	})
	ctx, log := utiltesting.ContextWithLog(t)
	now := time.Now().Truncate(time.Second)

	baseVictim := utiltestingapi.MakeWorkload("victim-1", "default").UID("victim-1").ResourceVersion("1").
		Request(corev1.ResourceCPU, "1").SimpleReserveQuota("standalone", "default", now)
	victim1 := baseVictim.Clone().Obj()
	victim2 := baseVictim.Clone().Name("victim-2").UID("victim-2").EvictedAt(now).Obj()

	cl := utiltesting.NewClientBuilder().
		WithObjects(victim1, victim2).
		WithStatusSubresource(&kueue.Workload{}).
		WithInterceptorFuncs(interceptor.Funcs{SubResourceApply: utiltesting.TreatSSAAsStrategicMergeForApplyConfiguration}).
		Build()

	limit := utiltestingalpha.MakePreemptionLimit("per-victim", kueuealpha.ByPreempteeWorkloadLimitAggregation).UID("limit-uid").Limit(1).WindowSeconds(60).Obj()
	tracker := limits.NewPreemptionLimitTracker()
	tracker.Sync(limit)
	fakeClock := clocktesting.NewFakeClock(now)
	preemptor := New(cl, workload.Ordering{}, &utiltesting.EventRecorder{}, nil, false, fakeClock, nil, preemptexpectations.New(), nil, tracker)

	cqSnap := &schdcache.ClusterQueueSnapshot{Name: "standalone"}
	preemptorInfo := workload.NewInfo(log, utiltestingapi.MakeWorkload("preemptor", "default").UID("preemptor").Obj())
	victim1Consumed := policy.ConsumedLimits{"per-victim": policy.NewConsumedLimit(limit, "default/victim-1")}
	victim2Consumed := policy.ConsumedLimits{"per-victim": policy.NewConsumedLimit(limit, "default/victim-2")}
	targets := []*Target{
		{
			WorkloadInfo:                     workload.NewInfo(log, victim1),
			Reason:                           kueue.ConfigurablePreemptionReason,
			WorkloadCq:                       cqSnap,
			ConfigurablePreemptionReasonData: &policy.ConfigurablePreemptionReasonData{ConfigName: "config", ConsumedLimits: victim1Consumed},
		},
		{
			WorkloadInfo:                     workload.NewInfo(log, victim2),
			Reason:                           kueue.ConfigurablePreemptionReason,
			WorkloadCq:                       cqSnap,
			ConfigurablePreemptionReasonData: &policy.ConfigurablePreemptionReasonData{ConfigName: "config", ConsumedLimits: victim2Consumed},
		},
	}

	preemptor.SnapshotPreemptionLimits()
	fakeClock.Step(time.Second)
	preempted, failed, err := preemptor.IssuePreemptions(ctx, schdcache.New(cl), preemptorInfo, targets, cqSnap)
	if preempted != 2 || failed != 0 || err != nil {
		t.Fatalf("IssuePreemptions() = (%d, %d, %v), want (2, 0, nil)", preempted, failed, err)
	}
	wantCounts := policy.PreemptionLimitAggregationCounts{"default/victim-1": 1}
	if diff := cmp.Diff(wantCounts, tracker.Counts(fakeClock.Now(), "per-victim"), cmpopts.EquateEmpty()); diff != "" {
		t.Errorf("Unexpected tracker counts (-want,+got):\n%s", diff)
	}
	if preemptor.limitsSnapshot.WithinLimits([]policy.ConsumedLimits{victim1Consumed}) {
		t.Errorf("limitsSnapshot.WithinLimits(victim-1) = true, want false")
	}
	if !preemptor.limitsSnapshot.WithinLimits([]policy.ConsumedLimits{victim2Consumed}) {
		t.Errorf("limitsSnapshot.WithinLimits(victim-2) = false, want true")
	}
}

func TestSnapshotPreemptionLimits(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	global := utiltestingalpha.MakePreemptionLimit("global", kueuealpha.GlobalLimitAggregation).UID("global-uid").Limit(1).WindowSeconds(60).Obj()
	fresh := utiltestingalpha.MakePreemptionLimit("fresh", kueuealpha.GlobalLimitAggregation).UID("fresh-uid").Limit(1).WindowSeconds(60).Obj()
	globalConsumed := policy.ConsumedLimits{"global": policy.NewConsumedLimit(global, policy.GlobalAggregationKey)}
	freshConsumed := policy.ConsumedLimits{"fresh": policy.NewConsumedLimit(fresh, policy.GlobalAggregationKey)}

	cases := map[string]struct {
		disableLimits    bool
		nilTracker       bool
		wantSnapshot     bool
		wantGlobalWithin bool
		wantFreshWithin  bool
	}{
		"snapshot holds the synced PreemptionLimits and the preemptions recorded within their windows": {
			wantSnapshot:     true,
			wantGlobalWithin: false,
			wantFreshWithin:  true,
		},
		"no snapshot when the ConfigurablePreemptionLimits feature is disabled": {
			disableLimits: true,
		},
		"no snapshot when tracker is nil": {
			nilTracker: true,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGatesDuringTest(t, map[featuregate.Feature]bool{
				features.ConfigurablePreemptions:      true,
				features.ConfigurablePreemptionLimits: !tc.disableLimits,
			})
			var tracker *limits.PreemptionLimitTracker
			if !tc.nilTracker {
				tracker = limits.NewPreemptionLimitTracker()
				tracker.Sync(global)
				tracker.Sync(fresh)
				tracker.Record(now.Add(-30*time.Second), policy.ConsumedLimits{"global": policy.NewConsumedLimit(global, policy.GlobalAggregationKey)})
				tracker.Record(now.Add(-90*time.Second), policy.ConsumedLimits{"fresh": policy.NewConsumedLimit(fresh, policy.GlobalAggregationKey)})
			}
			preemptor := New(
				utiltesting.NewClientBuilder().Build(),
				workload.Ordering{},
				&utiltesting.EventRecorder{},
				nil,
				false,
				clocktesting.NewFakeClock(now),
				nil,
				preemptexpectations.New(),
				nil,
				tracker,
			)
			// Left over from the previous scheduling cycle.
			preemptor.limitsSnapshot = &limits.PreemptionLimitsSnapshot{}

			preemptor.SnapshotPreemptionLimits()

			if got := preemptor.limitsSnapshot != nil; got != tc.wantSnapshot {
				t.Fatalf("limitsSnapshot != nil = %v, want %v", got, tc.wantSnapshot)
			}
			if tc.wantSnapshot {
				if got := preemptor.limitsSnapshot.WithinLimits([]policy.ConsumedLimits{globalConsumed}); got != tc.wantGlobalWithin {
					t.Errorf("limitsSnapshot.WithinLimits(global) = %v, want %v", got, tc.wantGlobalWithin)
				}
				if got := preemptor.limitsSnapshot.WithinLimits([]policy.ConsumedLimits{freshConsumed}); got != tc.wantFreshWithin {
					t.Errorf("limitsSnapshot.WithinLimits(fresh) = %v, want %v", got, tc.wantFreshWithin)
				}
			}
		})
	}
}

func TestWithinPreemptionLimits(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	global := utiltestingalpha.MakePreemptionLimit("global", kueuealpha.GlobalLimitAggregation).UID("global-uid").Limit(1).WindowSeconds(60).Obj()
	globalConsumed := policy.NewConsumedLimit(global, policy.GlobalAggregationKey)
	consumingGlobal := &Target{ConfigurablePreemptionReasonData: &policy.ConfigurablePreemptionReasonData{
		ConsumedLimits: policy.ConsumedLimits{"global": globalConsumed},
	}}
	consumingNone := &Target{ConfigurablePreemptionReasonData: &policy.ConfigurablePreemptionReasonData{}}

	cases := map[string]struct {
		noSnapshot       bool
		recorded         []policy.ConsumedLimits
		targets          []*Target
		wantWithinLimits bool
	}{
		"configurable target within remaining limit": {
			targets:          []*Target{consumingGlobal},
			wantWithinLimits: true,
		},
		"configurable targets together exceeding remaining limit": {
			targets:          []*Target{consumingGlobal, consumingGlobal},
			wantWithinLimits: false,
		},
		"configurable target exceeding limit after a recorded preemption": {
			recorded:         []policy.ConsumedLimits{{"global": globalConsumed}},
			targets:          []*Target{consumingGlobal},
			wantWithinLimits: false,
		},
		"classical target does not count toward configurable target limit": {
			targets:          []*Target{{Reason: kueue.InClusterQueueReason}, consumingGlobal},
			wantWithinLimits: true,
		},
		"classical target is allowed when limits snapshot is nil": {
			noSnapshot:       true,
			targets:          []*Target{{Reason: kueue.InClusterQueueReason}},
			wantWithinLimits: true,
		},
		"configurable target consuming no limits does not count": {
			targets:          []*Target{consumingNone, consumingGlobal},
			wantWithinLimits: true,
		},
		"configurable target consuming no limits is blocked when limits snapshot is nil": {
			noSnapshot:       true,
			targets:          []*Target{consumingNone},
			wantWithinLimits: false,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			preemptor := &Preemptor{}
			if !tc.noSnapshot {
				tracker := limits.NewPreemptionLimitTracker()
				tracker.Sync(global)
				preemptor.limitsSnapshot = tracker.Snapshot(now)
				for _, consumed := range tc.recorded {
					preemptor.limitsSnapshot.Record(now, consumed)
				}
			}
			if got := preemptor.WithinPreemptionLimits(tc.targets); got != tc.wantWithinLimits {
				t.Errorf("WithinPreemptionLimits() = %v, want %v", got, tc.wantWithinLimits)
			}
		})
	}
}
