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

package core

import (
	"context"
	"errors"
	"fmt"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	testingclock "k8s.io/utils/clock/testing"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	qcache "sigs.k8s.io/kueue/pkg/cache/queue"
	preemptexpectations "sigs.k8s.io/kueue/pkg/scheduler/preemption/expectations"
	"sigs.k8s.io/kueue/pkg/scheduler/preemption/limits"
	"sigs.k8s.io/kueue/pkg/scheduler/preemption/policy"
	"sigs.k8s.io/kueue/pkg/util/roletracker"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingalpha "sigs.k8s.io/kueue/pkg/util/testing/v1alpha1"
	utiltestingbeta "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
)

func TestPreemptionLimitReconcile(t *testing.T) {
	now := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	lastUpdateTime := metav1.NewTime(now)
	errTest := errors.New("test error")
	allClusterQueues := sets.New[kueue.ClusterQueueReference]("cq-a", "cq-b", "cq-c", "cq-d")
	limitBase := utiltestingalpha.MakePreemptionLimit("limit", kueuealpha.ByPreemptorCQLimitAggregation).Generation(1).Limit(10).WindowSeconds(60)
	limit := limitBase.Clone().Obj()
	otherLimit := utiltestingalpha.MakePreemptionLimit("other-limit", kueuealpha.GlobalLimitAggregation).Generation(1).WindowSeconds(60).Obj()
	// recorded holds the preemptions of "limit" recorded before it is
	// reconciled, by timestamp.
	type recorded map[time.Time]policy.ConsumedLimits
	limitConsumed := func(v policy.PreemptionLimitAggregationKey) policy.ConsumedLimits {
		return policy.ConsumedLimits{"limit": policy.NewConsumedLimit(limit, v)}
	}
	seeded := recorded{
		now.Add(-time.Hour):        limitConsumed("cq-a"),
		now.Add(-30 * time.Second): limitConsumed("cq-a"),
		now:                        limitConsumed("cq-b"),
	}
	multiplePreemptions := recorded{
		now.Add(-7 * time.Second): limitConsumed("cq-b"),
		now.Add(-6 * time.Second): limitConsumed("cq-d"),
		now.Add(-5 * time.Second): limitConsumed("cq-a"),
		now.Add(-4 * time.Second): limitConsumed("cq-c"),
		now.Add(-3 * time.Second): limitConsumed("cq-d"),
		now.Add(-2 * time.Second): limitConsumed("cq-a"),
		now.Add(-1 * time.Second): limitConsumed("cq-c"),
		now:                       limitConsumed("cq-c"),
	}
	overMaxPreemptions := make(recorded, preemptionLimitTopAggregatedCountsMaxItems+2)
	overMaxPreemptions[now] = limitConsumed("high")
	overMaxPreemptions[now.Add(-time.Millisecond)] = limitConsumed("high")
	overMaxCounts := policy.PreemptionLimitAggregationCounts{"high": 2}
	for i := range preemptionLimitTopAggregatedCountsMaxItems {
		aggregationKey := policy.PreemptionLimitAggregationKey(fmt.Sprintf("cq-%04d", i))
		overMaxPreemptions[now.Add(-time.Duration(i+2)*time.Millisecond)] = limitConsumed(aggregationKey)
		overMaxCounts[aggregationKey] = 1
	}
	wantTopCounts := make([]kueuealpha.PreemptionLimitTopAggregatedCount, 0, preemptionLimitTopAggregatedCountsMaxItems)
	wantTopCounts = append(wantTopCounts, kueuealpha.PreemptionLimitTopAggregatedCount{Key: "high", Value: 2})
	for i := range preemptionLimitTopAggregatedCountsMaxItems - 1 {
		wantTopCounts = append(wantTopCounts, kueuealpha.PreemptionLimitTopAggregatedCount{
			Key:   fmt.Sprintf("cq-%04d", i),
			Value: 1,
		})
	}

	cases := map[string]struct {
		// role is the role of the replica, the leader unless set.
		role string
		// limit is "limit" when it is reconciled, or nil once deleted.
		limit       *kueuealpha.PreemptionLimit
		recorded    recorded
		opts        []PreemptionLimitReconcilerOption
		clientFuncs interceptor.Funcs
		wantResult  reconcile.Result
		wantErr     error
		// wantCounts are the preemptions within the window of "limit".
		wantCounts policy.PreemptionLimitAggregationCounts
		// wantLimitStatus, if set, is the status of "limit" after the reconcile.
		wantLimitStatus           *kueuealpha.PreemptionLimitStatus
		wantRequeuedClusterQueues *sets.Set[kueue.ClusterQueueReference]

		// Simulate a case when PreemptionLimit was deleted and reconcile already
		// handled this deletion by removing in-memory state.
		simulateForgottenLimit bool
	}{
		"custom prune interval overrides RequeueAfter": {
			limit:      limit,
			recorded:   seeded,
			opts:       []PreemptionLimitReconcilerOption{WithPreemptionLimitPruneInterval(100 * time.Millisecond)},
			wantResult: reconcile.Result{RequeueAfter: 100 * time.Millisecond},
			wantCounts: policy.PreemptionLimitAggregationCounts{"cq-a": 1, "cq-b": 1},
		},
		"unchanged limit keeps its events and reports them in its status": {
			limit:      limit,
			recorded:   seeded,
			wantResult: reconcile.Result{RequeueAfter: preemptionLimitPruneInterval},
			wantCounts: policy.PreemptionLimitAggregationCounts{"cq-a": 1, "cq-b": 1},
			wantLimitStatus: &kueuealpha.PreemptionLimitStatus{
				Window: &kueuealpha.PreemptionLimitWindowStatus{
					LastUpdateTime: lastUpdateTime,
					TopAggregatedCounts: []kueuealpha.PreemptionLimitTopAggregatedCount{
						{Key: "cq-a", Value: 1},
						{Key: "cq-b", Value: 1},
					},
				},
			},
			wantRequeuedClusterQueues: new(sets.New[kueue.ClusterQueueReference]()),
		},
		"limit and window increase keep the events": {
			limit:      limitBase.Clone().Generation(2).Limit(20).WindowSeconds(2 * 3600).Obj(),
			recorded:   seeded,
			wantResult: reconcile.Result{RequeueAfter: preemptionLimitPruneInterval},
			wantCounts: policy.PreemptionLimitAggregationCounts{"cq-a": 2, "cq-b": 1},
			wantLimitStatus: &kueuealpha.PreemptionLimitStatus{
				Window: &kueuealpha.PreemptionLimitWindowStatus{
					LastUpdateTime: lastUpdateTime,
					TopAggregatedCounts: []kueuealpha.PreemptionLimitTopAggregatedCount{
						{Key: "cq-a", Value: 2},
						{Key: "cq-b", Value: 1},
					},
				},
			},
			wantRequeuedClusterQueues: &allClusterQueues,
		},
		"limit decrease keeps the events": {
			limit:      limitBase.Clone().Generation(2).Limit(5).Obj(),
			recorded:   seeded,
			wantResult: reconcile.Result{RequeueAfter: preemptionLimitPruneInterval},
			wantCounts: policy.PreemptionLimitAggregationCounts{"cq-a": 1, "cq-b": 1},
			wantLimitStatus: &kueuealpha.PreemptionLimitStatus{
				Window: &kueuealpha.PreemptionLimitWindowStatus{
					LastUpdateTime: lastUpdateTime,
					TopAggregatedCounts: []kueuealpha.PreemptionLimitTopAggregatedCount{
						{Key: "cq-a", Value: 1},
						{Key: "cq-b", Value: 1},
					},
				},
			},
			wantRequeuedClusterQueues: new(sets.New[kueue.ClusterQueueReference]()),
		},
		"aggregation change resets the events": {
			limit:      limitBase.Clone().Generation(2).Aggregation(kueuealpha.ByPreempteeCQLimitAggregation).Obj(),
			recorded:   seeded,
			wantResult: reconcile.Result{RequeueAfter: preemptionLimitPruneInterval},
			wantLimitStatus: &kueuealpha.PreemptionLimitStatus{
				Window: &kueuealpha.PreemptionLimitWindowStatus{
					LastUpdateTime: lastUpdateTime,
				},
			},
			wantRequeuedClusterQueues: &allClusterQueues,
		},
		"selector change resets the events": {
			limit: limitBase.Clone().
				Generation(2).
				ConfigSelector(metav1.LabelSelector{MatchLabels: map[string]string{"tier": "gold"}}).
				Obj(),
			recorded:   seeded,
			wantResult: reconcile.Result{RequeueAfter: preemptionLimitPruneInterval},
			wantLimitStatus: &kueuealpha.PreemptionLimitStatus{
				Window: &kueuealpha.PreemptionLimitWindowStatus{
					LastUpdateTime: lastUpdateTime,
				},
			},
			wantRequeuedClusterQueues: &allClusterQueues,
		},
		"no events within the window produce empty status counts": {
			limit:      limit,
			recorded:   recorded{now.Add(-time.Hour): limitConsumed("cq-a")},
			wantResult: reconcile.Result{RequeueAfter: preemptionLimitPruneInterval},
			wantLimitStatus: &kueuealpha.PreemptionLimitStatus{
				Window: &kueuealpha.PreemptionLimitWindowStatus{
					LastUpdateTime: lastUpdateTime,
				},
			},
			wantRequeuedClusterQueues: new(sets.New[kueue.ClusterQueueReference]()),
		},
		"status counts are sorted by value descending and then by key ascending": {
			limit:      limit,
			recorded:   multiplePreemptions,
			wantResult: reconcile.Result{RequeueAfter: preemptionLimitPruneInterval},
			wantCounts: policy.PreemptionLimitAggregationCounts{"cq-a": 2, "cq-b": 1, "cq-c": 3, "cq-d": 2},
			wantLimitStatus: &kueuealpha.PreemptionLimitStatus{
				Window: &kueuealpha.PreemptionLimitWindowStatus{
					LastUpdateTime: lastUpdateTime,
					TopAggregatedCounts: []kueuealpha.PreemptionLimitTopAggregatedCount{
						{Key: "cq-c", Value: 3},
						{Key: "cq-a", Value: 2},
						{Key: "cq-d", Value: 2},
						{Key: "cq-b", Value: 1},
					},
				},
			},
		},
		"status counts exceeding max items are truncated to top 100 after sorting": {
			limit:      limit,
			recorded:   overMaxPreemptions,
			wantResult: reconcile.Result{RequeueAfter: preemptionLimitPruneInterval},
			wantCounts: overMaxCounts,
			wantLimitStatus: &kueuealpha.PreemptionLimitStatus{
				Window: &kueuealpha.PreemptionLimitWindowStatus{
					LastUpdateTime:      lastUpdateTime,
					TopAggregatedCounts: wantTopCounts,
				},
			},
		},
		"deleted limit is forgotten": {
			recorded:                  seeded,
			wantRequeuedClusterQueues: &allClusterQueues,
		},
		// It is possible for a previously scheduled reconcile to be executed after a limit
		// was already deleted and state was removed. In this case there should be no additional handling.
		"should do nothing for previously deleted limit without a state": {
			simulateForgottenLimit:    true,
			wantRequeuedClusterQueues: new(sets.New[kueue.ClusterQueueReference]()),
		},
		"follower tracks the limit but leaves its status to the leader": {
			role:       roletracker.RoleFollower,
			limit:      limit,
			recorded:   seeded,
			wantResult: reconcile.Result{RequeueAfter: preemptionLimitPruneInterval},
			wantCounts: policy.PreemptionLimitAggregationCounts{"cq-a": 1, "cq-b": 1},
			// The status stays as it was created: without any window.
			wantLimitStatus:           &kueuealpha.PreemptionLimitStatus{},
			wantRequeuedClusterQueues: new(sets.New[kueue.ClusterQueueReference]()),
		},
		"follower applies a changed spec": {
			role:       roletracker.RoleFollower,
			limit:      limitBase.Clone().Generation(2).Aggregation(kueuealpha.ByPreempteeCQLimitAggregation).Obj(),
			recorded:   seeded,
			wantResult: reconcile.Result{RequeueAfter: preemptionLimitPruneInterval},
			// The aggregation changed, so the events no longer count toward the limit.
			wantLimitStatus:           &kueuealpha.PreemptionLimitStatus{},
			wantRequeuedClusterQueues: &allClusterQueues,
		},
		"follower forgets a deleted limit": {
			role:                      roletracker.RoleFollower,
			recorded:                  seeded,
			wantRequeuedClusterQueues: &allClusterQueues,
		},
		"follower does not fail when the status cannot be updated": {
			role:     roletracker.RoleFollower,
			limit:    limit,
			recorded: seeded,
			clientFuncs: interceptor.Funcs{
				SubResourceUpdate: func(context.Context, client.Client, string, client.Object, ...client.SubResourceUpdateOption) error {
					return errTest
				},
			},
			wantResult:                reconcile.Result{RequeueAfter: preemptionLimitPruneInterval},
			wantCounts:                policy.PreemptionLimitAggregationCounts{"cq-a": 1, "cq-b": 1},
			wantRequeuedClusterQueues: new(sets.New[kueue.ClusterQueueReference]()),
		},
		"standalone replica updates the status": {
			role:       roletracker.RoleStandalone,
			limit:      limit,
			recorded:   seeded,
			wantResult: reconcile.Result{RequeueAfter: preemptionLimitPruneInterval},
			wantCounts: policy.PreemptionLimitAggregationCounts{"cq-a": 1, "cq-b": 1},
			wantLimitStatus: &kueuealpha.PreemptionLimitStatus{
				Window: &kueuealpha.PreemptionLimitWindowStatus{
					LastUpdateTime: lastUpdateTime,
					TopAggregatedCounts: []kueuealpha.PreemptionLimitTopAggregatedCount{
						{Key: "cq-a", Value: 1},
						{Key: "cq-b", Value: 1},
					},
				},
			},
			wantRequeuedClusterQueues: new(sets.New[kueue.ClusterQueueReference]()),
		},
		"get error other than NotFound is returned and the events are kept": {
			limit:    limit,
			recorded: seeded,
			clientFuncs: interceptor.Funcs{
				Get: func(context.Context, client.WithWatch, client.ObjectKey, client.Object, ...client.GetOption) error {
					return errTest
				},
			},
			wantErr:                   errTest,
			wantCounts:                policy.PreemptionLimitAggregationCounts{"cq-a": 1, "cq-b": 1},
			wantRequeuedClusterQueues: new(sets.New[kueue.ClusterQueueReference]()),
		},
		"status update error is returned and the events are kept": {
			limit:    limit,
			recorded: seeded,
			clientFuncs: interceptor.Funcs{
				SubResourceUpdate: func(context.Context, client.Client, string, client.Object, ...client.SubResourceUpdateOption) error {
					return errTest
				},
			},
			wantErr:                   errTest,
			wantCounts:                policy.PreemptionLimitAggregationCounts{"cq-a": 1, "cq-b": 1},
			wantRequeuedClusterQueues: new(sets.New[kueue.ClusterQueueReference]()),
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)
			simulateErrorFromNowOn := false
			cl := utiltesting.NewClientBuilder().
				WithObjects(limit.DeepCopy(), otherLimit.DeepCopy()).
				WithStatusSubresource(&kueuealpha.PreemptionLimit{}).
				WithInterceptorFuncs(interceptor.Funcs{
					Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
						if simulateErrorFromNowOn && tc.clientFuncs.Get != nil {
							return tc.clientFuncs.Get(ctx, c, key, obj, opts...)
						}
						return c.Get(ctx, key, obj, opts...)
					},
					SubResourceUpdate: func(ctx context.Context, c client.Client, subResourceName string, obj client.Object, opts ...client.SubResourceUpdateOption) error {
						if simulateErrorFromNowOn && tc.clientFuncs.SubResourceUpdate != nil {
							return tc.clientFuncs.SubResourceUpdate(ctx, c, subResourceName, obj, opts...)
						}
						return c.SubResource(subResourceName).Update(ctx, obj, opts...)
					},
				}).
				Build()

			preemptionExpectations := preemptexpectations.New()
			qManager, requeuer := qcache.NewManagerForUnitTestsWithRequeuer(cl, nil, qcache.WithPreemptionExpectations(preemptionExpectations))
			for cq := range allClusterQueues {
				if err := qManager.AddClusterQueue(ctx, utiltestingbeta.MakeClusterQueue(string(cq)).Obj()); err != nil {
					t.Fatalf("Failed adding clusterQueue %s: %v", string(cq), err)
				}
			}

			tracker := limits.NewPreemptionLimitTracker()
			role := tc.role
			if role == "" {
				role = roletracker.RoleLeader
			}
			roleTracker := roletracker.NewFakeRoleTracker(role)
			reconcilerOpts := append([]PreemptionLimitReconcilerOption{WithPreemptionLimitClock(testingclock.NewFakeClock(now))}, tc.opts...)
			reconciler := NewPreemptionLimitReconciler(cl, tracker, roleTracker, qManager, reconcilerOpts...)
			// Preemptions are only recorded for the reconciled PreemptionLimits.
			for _, l := range []*kueuealpha.PreemptionLimit{limit, otherLimit} {
				if _, err := reconciler.Reconcile(ctx, reconcile.Request{Name: l.Name}); err != nil {
					t.Fatalf("Reconcile() of PreemptionLimit %q failed: %v", l.Name, err)
				}
			}
			for ts, consumedLimits := range tc.recorded {
				tracker.Record(ts, consumedLimits)
			}
			tracker.Record(now, policy.ConsumedLimits{"other-limit": policy.NewConsumedLimit(otherLimit, policy.GlobalAggregationKey)})

			// Clean up queued workloads if there were some before testing
			requeuer.ProcessRequeues(ctx)

			if tc.limit == nil {
				if err := cl.Delete(ctx, limit); err != nil {
					t.Fatalf("Failed to delete PreemptionLimit: %v", err)
				}
				if tc.simulateForgottenLimit {
					tracker.Forget(policy.PreemptionLimitReference(limit.Name))
				}
			} else if tc.limit != limit {
				var current kueuealpha.PreemptionLimit
				if err := cl.Get(ctx, client.ObjectKey{Name: "limit"}, &current); err != nil {
					t.Fatalf("Failed to get PreemptionLimit before update: %v", err)
				}
				updated := tc.limit.DeepCopy()
				updated.ResourceVersion = current.ResourceVersion
				if err := cl.Update(ctx, updated); err != nil {
					t.Fatalf("Failed to update PreemptionLimit: %v", err)
				}
			}
			simulateErrorFromNowOn = true

			gotResult, gotErr := reconciler.Reconcile(ctx, reconcile.Request{Name: "limit"})
			if diff := cmp.Diff(tc.wantErr, gotErr, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("Unexpected Reconcile() error (-want,+got):\n%s", diff)
			}
			if diff := cmp.Diff(tc.wantResult, gotResult); diff != "" {
				t.Errorf("Unexpected Reconcile() result (-want,+got):\n%s", diff)
			}
			if diff := cmp.Diff(tc.wantCounts, tracker.Counts(now, "limit"), cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("Unexpected counts of the reconciled limit (-want,+got):\n%s", diff)
			}
			wantOtherCounts := policy.PreemptionLimitAggregationCounts{policy.GlobalAggregationKey: 1}
			if diff := cmp.Diff(wantOtherCounts, tracker.Counts(now, "other-limit")); diff != "" {
				t.Errorf("Unexpected counts of the other limit (-want,+got):\n%s", diff)
			}
			if tc.wantLimitStatus != nil {
				var got kueuealpha.PreemptionLimit
				if err := cl.Get(ctx, client.ObjectKey{Name: "limit"}, &got); err != nil {
					t.Fatalf("Failed to get PreemptionLimit: %v", err)
				}
				if diff := cmp.Diff(*tc.wantLimitStatus, got.Status); diff != "" {
					t.Errorf("Unexpected PreemptionLimitStatus (-want,+got):\n%s", diff)
				}
			}
			if tc.wantRequeuedClusterQueues != nil {
				gotRequeuedClusterQueue := requeuer.NotifiedClusterQueues()
				if diff := cmp.Diff(*tc.wantRequeuedClusterQueues, gotRequeuedClusterQueue); diff != "" {
					t.Errorf("Unexpected requeued cluster queues (-want,+got):\n%s", diff)
				}
			}
		})
	}
}

func TestPreemptionLimitSetupWithManagerInvalidOptions(t *testing.T) {
	for name, opt := range map[string]PreemptionLimitReconcilerOption{
		"zero interval":     WithPreemptionLimitPruneInterval(0),
		"negative interval": WithPreemptionLimitPruneInterval(-time.Second),
		"nil clock":         WithPreemptionLimitClock(nil),
	} {
		t.Run(name, func(t *testing.T) {
			cl := utiltesting.NewClientBuilder().Build()
			reconciler := NewPreemptionLimitReconciler(
				cl,
				limits.NewPreemptionLimitTracker(),
				nil,
				nil,
				opt,
			)
			if err := reconciler.SetupWithManager(nil); err == nil {
				t.Errorf("Expected SetupWithManager() to fail for %s, got nil", name)
			}
		})
	}
}
