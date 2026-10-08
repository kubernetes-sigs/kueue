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
	"math"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/component-base/featuregate"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	qcache "sigs.k8s.io/kueue/pkg/cache/queue"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/metrics"
	"sigs.k8s.io/kueue/pkg/resources"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	testingmetrics "sigs.k8s.io/kueue/pkg/util/testing/metrics"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
)

func TestCohortReconcileCohortNotFoundDelete(t *testing.T) {
	cl := utiltesting.NewClientBuilder().Build()
	ctx, _ := utiltesting.ContextWithLog(t)
	cache := schdcache.New(cl)
	qManager := qcache.NewManagerForUnitTests(cl, cache)
	reconciler := NewCohortReconciler(cl, cache, qManager)

	cohort := utiltestingapi.MakeCohort("cohort").Obj()
	_ = cache.AddOrUpdateCohort(cohort)
	qManager.AddOrUpdateCohort(ctx, cohort)
	snapshot, err := cache.Snapshot(ctx)
	if err != nil {
		t.Fatalf("unexpected error while building snapshot: %v", err)
	}
	if cohortSnap := snapshot.Cohort("cohort"); cohortSnap == nil {
		t.Fatal("expected Cohort in snapshot")
	}

	if _, err := reconciler.Reconcile(
		ctx,
		reconcile.Request{NamespacedName: client.ObjectKeyFromObject(cohort)},
	); err != nil {
		t.Fatal("unexpected error")
	}

	snapshot, err = cache.Snapshot(ctx)
	if err != nil {
		t.Fatalf("unexpected error while building snapshot: %v", err)
	}
	if cohortSnap := snapshot.Cohort("cohort"); cohortSnap != nil {
		t.Fatal("unexpected Cohort in snapshot")
	}
}

func TestCohortReconcileCohortNotFoundIdempotentDelete(t *testing.T) {
	cl := utiltesting.NewClientBuilder().
		Build()
	ctx, _ := utiltesting.ContextWithLog(t)
	cache := schdcache.New(cl)
	qManager := qcache.NewManagerForUnitTests(cl, cache)
	reconciler := NewCohortReconciler(cl, cache, qManager)

	snapshot, err := cache.Snapshot(ctx)
	if err != nil {
		t.Fatalf("unexpected error while building snapshot: %v", err)
	}
	if cohortSnap := snapshot.Cohort("cohort"); cohortSnap != nil {
		t.Fatal("unexpected Cohort in snapshot")
	}

	cohort := utiltestingapi.MakeCohort("cohort").Obj()
	if _, err := reconciler.Reconcile(
		ctx,
		reconcile.Request{NamespacedName: client.ObjectKeyFromObject(cohort)},
	); err != nil {
		t.Fatal("unexpected error")
	}

	snapshot, err = cache.Snapshot(ctx)
	if err != nil {
		t.Fatalf("unexpected error while building snapshot: %v", err)
	}
	if cohortSnap := snapshot.Cohort("cohort"); cohortSnap != nil {
		t.Fatal("unexpected Cohort in snapshot")
	}
}

func TestCohortReconcileCycleReturnsSuccess(t *testing.T) {
	cohortA := utiltestingapi.MakeCohort("cohort-a").Parent("cohort-b").Obj()
	cohortB := utiltestingapi.MakeCohort("cohort-b").Parent("cohort-a").Obj()
	cl := utiltesting.NewClientBuilder().
		WithObjects(cohortA, cohortB).
		WithStatusSubresource(&kueue.Cohort{}).
		Build()
	ctx, _ := utiltesting.ContextWithLog(t)
	cache := schdcache.New(cl)
	qManager := qcache.NewManagerForUnitTests(cl, cache)
	reconciler := NewCohortReconciler(cl, cache, qManager)

	// no cycle when creating first cohort
	if _, err := reconciler.Reconcile(
		ctx,
		reconcile.Request{NamespacedName: client.ObjectKeyFromObject(cohortA)},
	); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Cycles are persisted in the scheduler cache and handled without retrying.
	if _, err := reconciler.Reconcile(
		ctx,
		reconcile.Request{NamespacedName: client.ObjectKeyFromObject(cohortB)},
	); err != nil {
		t.Fatalf("unexpected error when adding cycle: %v", err)
	}

	// remove cycle, no error
	if err := cl.Get(ctx, client.ObjectKeyFromObject(cohortB), cohortB); err != nil {
		t.Fatal("unexpected error")
	}
	cohortB.Spec.ParentName = "cohort-c"
	if err := cl.Update(ctx, cohortB); err != nil {
		t.Fatal("unexpected error updating cohort", err)
	}
	if _, err := reconciler.Reconcile(
		ctx,
		reconcile.Request{NamespacedName: client.ObjectKeyFromObject(cohortB)},
	); err != nil {
		t.Fatal("unexpected error")
	}
}

func TestCohortReconcileCycleCacheSnapshotBehavior(t *testing.T) {
	// This test verifies the cache Snapshot state during and after a cohort cycle.
	// When AddOrUpdateCohort errors (cycle detected), the reconciler returns early
	// without calling qManager.AddOrUpdateCohort. The observable effect is that
	// cyclic cohorts are excluded from cache.Snapshot until the cycle is resolved.
	cohortA := utiltestingapi.MakeCohort("cohort-a").Parent("cohort-b").Obj()
	cohortB := utiltestingapi.MakeCohort("cohort-b").Parent("cohort-a").Obj()
	cl := utiltesting.NewClientBuilder().
		WithObjects(cohortA, cohortB).
		WithStatusSubresource(&kueue.Cohort{}).
		Build()
	ctx, _ := utiltesting.ContextWithLog(t)
	cache := schdcache.New(cl)
	qManager := qcache.NewManagerForUnitTests(cl, cache)
	reconciler := NewCohortReconciler(cl, cache, qManager)

	// Reconcile cohort-a: A -> B, no cycle yet (B is implicit).
	if _, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(cohortA)}); err != nil {
		t.Fatalf("unexpected error reconciling cohort-a: %v", err)
	}

	// Reconcile cohort-b: B -> A closes the cycle. The reconciler handles the
	// scheduler-cache cycle error without retrying or updating the queue manager.
	if _, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(cohortB)}); err != nil {
		t.Fatalf("unexpected error when adding cycle: %v", err)
	}

	// During cycle: Snapshot excludes both cohorts.
	snapshot, err := cache.Snapshot(ctx)
	if err != nil {
		t.Fatalf("unexpected error building snapshot during cycle: %v", err)
	}
	if snap := snapshot.Cohort("cohort-a"); snap != nil {
		t.Error("cohort-a should be excluded from Snapshot while in a cycle")
	}
	if snap := snapshot.Cohort("cohort-b"); snap != nil {
		t.Error("cohort-b should be excluded from Snapshot while in a cycle")
	}

	// Resolve the cycle: point cohort-b at an unrelated cohort-c.
	if err := cl.Get(ctx, client.ObjectKeyFromObject(cohortB), cohortB); err != nil {
		t.Fatalf("unexpected error fetching cohort-b: %v", err)
	}
	cohortB.Spec.ParentName = "cohort-c"
	if err := cl.Update(ctx, cohortB); err != nil {
		t.Fatalf("unexpected error updating cohort-b: %v", err)
	}
	// Reconcile cohort-b after resolution: cache succeeds, qManager.AddOrUpdateCohort is called.
	if _, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(cohortB)}); err != nil {
		t.Fatalf("unexpected error reconciling cohort-b after cycle resolution: %v", err)
	}

	// After resolution: A -> B -> C, no cycle. Both cohorts appear in Snapshot.
	snapshot, err = cache.Snapshot(ctx)
	if err != nil {
		t.Fatalf("unexpected error building snapshot after cycle resolution: %v", err)
	}
	if snap := snapshot.Cohort("cohort-a"); snap == nil {
		t.Error("cohort-a should appear in Snapshot after cycle resolution")
	}
	if snap := snapshot.Cohort("cohort-b"); snap == nil {
		t.Error("cohort-b should appear in Snapshot after cycle resolution")
	}
}

func TestCohortReconcileErrorOtherThanNotFoundNotDeleted(t *testing.T) {
	ctx, _ := utiltesting.ContextWithLog(t)
	funcs := interceptor.Funcs{
		Get: func(ctx context.Context, client client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			return errors.New("error")
		},
	}
	cl := utiltesting.NewClientBuilder().WithInterceptorFuncs(funcs).Build()

	cache := schdcache.New(cl)
	qManager := qcache.NewManagerForUnitTests(cl, cache)
	reconciler := NewCohortReconciler(cl, cache, qManager)
	cohort := utiltestingapi.MakeCohort("cohort").Obj()
	_ = cache.AddOrUpdateCohort(cohort)
	qManager.AddOrUpdateCohort(ctx, cohort)
	snapshot, err := cache.Snapshot(ctx)
	if err != nil {
		t.Fatalf("unexpected error while building snapshot: %v", err)
	}
	if cohortSnap := snapshot.Cohort("cohort"); cohortSnap == nil {
		t.Fatal("expected Cohort in snapshot")
	}

	if _, err := reconciler.Reconcile(
		ctx,
		reconcile.Request{NamespacedName: client.ObjectKeyFromObject(cohort)},
	); err == nil {
		t.Fatal("expected error")
	}

	snapshot, err = cache.Snapshot(ctx)
	if err != nil {
		t.Fatalf("unexpected error while building snapshot: %v", err)
	}
	if cohortSnap := snapshot.Cohort("cohort"); cohortSnap == nil {
		t.Fatal("expected Cohort in snapshot")
	}
}

func TestCohortReconcileLifecycle(t *testing.T) {
	ctx, _ := utiltesting.ContextWithLog(t)
	cohort := utiltestingapi.MakeCohort("cohort").ResourceGroup(
		*utiltestingapi.MakeFlavorQuotas("red").Resource("cpu", "10").Obj(),
	).Obj()
	cl := utiltesting.NewClientBuilder().WithObjects(cohort).WithStatusSubresource(&kueue.Cohort{}).Build()
	cache := schdcache.New(cl)
	qManager := qcache.NewManagerForUnitTests(cl, cache)
	reconciler := NewCohortReconciler(cl, cache, qManager)
	labels := map[string]string{"cohort": cohort.Name, "flavor": "red", "resource": "cpu", "replica_role": "standalone"}

	// create
	{
		if _, err := reconciler.Reconcile(
			ctx,
			reconcile.Request{NamespacedName: client.ObjectKeyFromObject(cohort)},
		); err != nil {
			t.Fatal("unexpected error")
		}

		snapshot, err := cache.Snapshot(ctx)
		if err != nil {
			t.Fatalf("unexpected error while building snapshot: %v", err)
		}
		cohortSnap := snapshot.Cohort("cohort")
		if cohortSnap == nil {
			t.Fatal("expected Cohort in snapshot")
		}

		wantQuotas := resources.FlavorResourceQuantities{
			{Flavor: "red", Resource: "cpu"}: resources.NewAmount(10_000),
		}
		if diff := cmp.Diff(wantQuotas, cohortSnap.ResourceNode.SubtreeQuota); diff != "" {
			t.Fatalf("unexpected quota (-want +got) %s", diff)
		}

		cnq := testingmetrics.CollectFilteredGaugeVec(metrics.CohortSubtreeQuota, labels)
		if cnq == nil {
			t.Fatal("expected metric value")
		}
		wantCNQ := []testingmetrics.MetricDataPoint{
			{Labels: labels, Value: 10},
		}
		checkMetricDataPoints(t, cnq, wantCNQ)
	}

	// update
	{
		if err := cl.Get(ctx, client.ObjectKeyFromObject(cohort), cohort); err != nil {
			t.Fatal("unexpected error")
		}
		cohort.Spec.ResourceGroups[0] = utiltestingapi.ResourceGroup(
			*utiltestingapi.MakeFlavorQuotas("red").Resource("cpu", "5").Obj(),
		)
		if err := cl.Update(ctx, cohort); err != nil {
			t.Fatal("unexpected error updating cohort", err)
		}
		if _, err := reconciler.Reconcile(
			ctx,
			reconcile.Request{NamespacedName: client.ObjectKeyFromObject(cohort)},
		); err != nil {
			t.Fatal("unexpected error")
		}

		snapshot, err := cache.Snapshot(ctx)
		if err != nil {
			t.Fatalf("unexpected error while building snapshot: %v", err)
		}
		cohortSnap := snapshot.Cohort("cohort")
		if cohortSnap == nil {
			t.Fatal("expected Cohort in snapshot")
		}

		wantQuotas := resources.FlavorResourceQuantities{
			{Flavor: "red", Resource: "cpu"}: resources.NewAmount(5_000),
		}
		if diff := cmp.Diff(wantQuotas, cohortSnap.ResourceNode.SubtreeQuota); diff != "" {
			t.Fatalf("unexpected quota (-want +got) %s", diff)
		}

		cnq := testingmetrics.CollectFilteredGaugeVec(metrics.CohortSubtreeQuota, labels)
		if cnq == nil {
			t.Fatal("expected metric value")
		}
		wantCNQ := []testingmetrics.MetricDataPoint{
			{Labels: labels, Value: 5},
		}
		checkMetricDataPoints(t, cnq, wantCNQ)
	}

	// delete
	{
		if err := cl.Delete(ctx, cohort); err != nil {
			t.Fatal("unexpected error during deletion")
		}
		if _, err := reconciler.Reconcile(
			ctx,
			reconcile.Request{NamespacedName: client.ObjectKeyFromObject(cohort)},
		); err != nil {
			t.Fatal("unexpected error")
		}

		snapshot, err := cache.Snapshot(ctx)
		if err != nil {
			t.Fatalf("unexpected error while building snapshot: %v", err)
		}
		if cohortSnap := snapshot.Cohort("cohort"); cohortSnap != nil {
			t.Fatal("unexpected Cohort in snapshot")
		}

		cnq := testingmetrics.CollectFilteredGaugeVec(metrics.CohortSubtreeQuota, labels)
		if cnq == nil {
			t.Fatal("expected metric value")
		}
		wantCNQ := []testingmetrics.MetricDataPoint{}
		checkMetricDataPoints(t, cnq, wantCNQ)
	}
}

// TestCohortEventHandlersUpdateCacheWithoutReconcile checks that the event
// handlers alone keep the Cohort and its quota in the cache. On a follower,
// WithLeadingManager does not call Reconcile, so this is what a warm
// follower holds when it takes the lease.
func TestCohortEventHandlersUpdateCacheWithoutReconcile(t *testing.T) {
	ctx, _ := utiltesting.ContextWithLog(t)
	cl := utiltesting.NewClientBuilder().Build()
	cache := schdcache.New(cl)
	qManager := qcache.NewManagerForUnitTests(cl, cache)
	reconciler := NewCohortReconciler(cl, cache, qManager)

	borrower := utiltestingapi.MakeClusterQueue("borrower").
		Cohort("root").
		ResourceGroup(*utiltestingapi.MakeFlavorQuotas("red").Resource("cpu", "0").Obj()).
		Obj()
	if err := cache.AddClusterQueue(ctx, borrower); err != nil {
		t.Fatalf("AddClusterQueue() = %v", err)
	}

	subtreeQuota := func() resources.FlavorResourceQuantities {
		t.Helper()
		snapshot, err := cache.Snapshot(ctx)
		if err != nil {
			t.Fatalf("Snapshot() = %v", err)
		}
		cohortSnap := snapshot.Cohort("root")
		if cohortSnap == nil {
			t.Fatal("expected Cohort root in snapshot")
		}
		return cohortSnap.ResourceNode.SubtreeQuota
	}
	red := resources.FlavorResource{Flavor: "red", Resource: "cpu"}

	root := utiltestingapi.MakeCohort("root").ResourceGroup(
		*utiltestingapi.MakeFlavorQuotas("red").Resource("cpu", "10").Obj(),
	).Obj()
	reconciler.Create(event.TypedCreateEvent[*kueue.Cohort]{Object: root})
	if got, want := subtreeQuota()[red], resources.NewAmount(10_000); !got.Equal(want) {
		t.Fatalf("after create, subtree quota = %v, want %v", got, want)
	}

	updated := root.DeepCopy()
	updated.Spec.ResourceGroups[0] = utiltestingapi.ResourceGroup(
		*utiltestingapi.MakeFlavorQuotas("red").Resource("cpu", "5").Obj(),
	)
	if !reconciler.Update(event.TypedUpdateEvent[*kueue.Cohort]{ObjectOld: root, ObjectNew: updated}) {
		t.Fatal("expected the quota update to be processed")
	}
	if got, want := subtreeQuota()[red], resources.NewAmount(5_000); !got.Equal(want) {
		t.Fatalf("after update, subtree quota = %v, want %v", got, want)
	}

	reconciler.Delete(event.TypedDeleteEvent[*kueue.Cohort]{Object: updated})
	// The ClusterQueue still names the Cohort, so it stays as an implicit
	// Cohort without quota of its own.
	if got := subtreeQuota()[red]; got.CmpInt64(0) != 0 {
		t.Fatalf("after delete, subtree quota = %v, want 0", got)
	}

	// The leader's Reconcile runs after the handler for the same delete.
	if _, err := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(root)}); err != nil {
		t.Fatalf("Reconcile() after delete = %v", err)
	}
	if got := subtreeQuota()[red]; got.CmpInt64(0) != 0 {
		t.Fatalf("after Reconcile, subtree quota = %v, want 0", got)
	}
}

func checkMetricDataPoints(t *testing.T, got, want []testingmetrics.MetricDataPoint) {
	if diff := cmp.Diff(want, got, cmpopts.SortSlices(func(a, b testingmetrics.MetricDataPoint) bool { return a.Less(&b) })); diff != "" {
		t.Fatalf("unexpected metrics (-want +got) %s", diff)
	}
}

func TestCohortReconcilerFilters(t *testing.T) {
	cl := utiltesting.NewClientBuilder().
		Build()
	cache := schdcache.New(cl)
	qManager := qcache.NewManagerForUnitTests(cl, cache)
	reconciler := NewCohortReconciler(cl, cache, qManager)

	t.Run("delete returns true", func(t *testing.T) {
		if !reconciler.Delete(event.TypedDeleteEvent[*kueue.Cohort]{}) {
			t.Fatal("expected delete to return true")
		}
	})

	t.Run("create returns true", func(t *testing.T) {
		if !reconciler.Create(event.TypedCreateEvent[*kueue.Cohort]{}) {
			t.Fatal("expected create to return true")
		}
	})

	t.Run("generic returns true", func(t *testing.T) {
		if !reconciler.Generic(event.TypedGenericEvent[*kueue.Cohort]{}) {
			t.Fatal("expected generic to return true")
		}
	})

	cases := map[string]struct {
		featureGates map[featuregate.Feature]bool
		old          *kueue.Cohort
		new          *kueue.Cohort
		want         bool
	}{
		"unchanged returns false": {
			old: utiltestingapi.MakeCohort("cohort").ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas("red").Resource("cpu", "5").Obj(),
			).Obj(),
			new: utiltestingapi.MakeCohort("cohort").ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas("red").Resource("cpu", "5").Obj(),
			).Obj(),
			want: false,
		},
		"changed resource returns true": {
			old: utiltestingapi.MakeCohort("cohort").ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas("red").Resource("cpu", "5").Obj(),
			).Obj(),
			new: utiltestingapi.MakeCohort("cohort").ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas("red").Resource("cpu", "10").Obj(),
			).Obj(),
			want: true,
		},
		"adding parent returns true": {
			old:  utiltestingapi.MakeCohort("cohort").Obj(),
			new:  utiltestingapi.MakeCohort("cohort").Parent("parent").Obj(),
			want: true,
		},
		"changing parent returns true": {
			old:  utiltestingapi.MakeCohort("cohort").Parent("old").Obj(),
			new:  utiltestingapi.MakeCohort("cohort").Parent("new").Obj(),
			want: true,
		},
		"deleting parent returns true": {
			old:  utiltestingapi.MakeCohort("cohort").Parent("parent").Obj(),
			new:  utiltestingapi.MakeCohort("cohort").Obj(),
			want: true,
		},
		"adding weight returns true": {
			old:  utiltestingapi.MakeCohort("cohort").Obj(),
			new:  utiltestingapi.MakeCohort("cohort").FairWeight(resource.MustParse("1")).Obj(),
			want: true,
		},
		"deleting weight returns true": {
			old:  utiltestingapi.MakeCohort("cohort").FairWeight(resource.MustParse("1")).Obj(),
			new:  utiltestingapi.MakeCohort("cohort").Obj(),
			want: true,
		},
		"updating weight returns true": {
			old:  utiltestingapi.MakeCohort("cohort").FairWeight(resource.MustParse("1")).Obj(),
			new:  utiltestingapi.MakeCohort("cohort").FairWeight(resource.MustParse("2")).Obj(),
			want: true,
		},
		"updating status.effectiveQuotas with DynamicQuotaOrchestration enabled returns true": {
			featureGates: map[featuregate.Feature]bool{features.DynamicQuotaOrchestration: true},
			old: utiltestingapi.MakeCohort("cohort").ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas("red").Resource("cpu", "5").Obj(),
			).Obj(),
			new: utiltestingapi.MakeCohort("cohort").ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas("red").Resource("cpu", "5").Obj(),
			).EffectiveQuotas(
				*utiltestingapi.MakeFlavorQuotas("red").Resource("cpu", "10").Obj(),
			).Obj(),
			want: true,
		},
		"updating status.effectiveQuotas with DynamicQuotaOrchestration disabled returns false": {
			featureGates: map[featuregate.Feature]bool{features.DynamicQuotaOrchestration: false},
			old: utiltestingapi.MakeCohort("cohort").ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas("red").Resource("cpu", "5").Obj(),
			).Obj(),
			new: utiltestingapi.MakeCohort("cohort").ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas("red").Resource("cpu", "5").Obj(),
			).EffectiveQuotas(
				*utiltestingapi.MakeFlavorQuotas("red").Resource("cpu", "10").Obj(),
			).Obj(),
			want: false,
		},
		"updating unrelated status returns false": {
			featureGates: map[featuregate.Feature]bool{features.DynamicQuotaOrchestration: true},
			old: utiltestingapi.MakeCohort("cohort").ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas("red").Resource("cpu", "5").Obj(),
			).Obj(),
			new: func() *kueue.Cohort {
				c := utiltestingapi.MakeCohort("cohort").ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("red").Resource("cpu", "5").Obj(),
				).Obj()
				c.Status.FairSharing = &kueue.FairSharingStatus{WeightedShare: 10}
				return c
			}(),
			want: false,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGatesDuringTest(t, tc.featureGates)
			e := event.TypedUpdateEvent[*kueue.Cohort]{
				ObjectOld: tc.old,
				ObjectNew: tc.new,
			}
			if reconciler.Update(e) != tc.want {
				t.Fatalf("expected %v, got %v", tc.want, !tc.want)
			}
		})
	}
}

// TestUpdateCohortStatusIfChanged verifies the recomputed Cohort status and that the status is
// written to the API server only when it changes.
func TestUpdateCohortStatusIfChanged(t *testing.T) {
	cases := map[string]struct {
		fairSharingEnabled      bool
		cohort                  *kueue.Cohort
		setupCache              func(ctx context.Context, t *testing.T, cache *schdcache.Cache)
		cohortStatus            kueue.CohortStatus
		wantCohortStatus        kueue.CohortStatus
		wantStatusUpdates       int
		wantWeightedShareMetric *float64
	}{
		"fair sharing disabled and status unchanged": {},
		"fair sharing disabled clears stale weighted share": {
			cohortStatus:      kueue.CohortStatus{FairSharing: &kueue.FairSharingStatus{WeightedShare: 3}},
			wantStatusUpdates: 1,
		},
		"fair sharing enabled and weighted share unchanged": {
			fairSharingEnabled:      true,
			cohortStatus:            kueue.CohortStatus{FairSharing: &kueue.FairSharingStatus{}},
			wantCohortStatus:        kueue.CohortStatus{FairSharing: &kueue.FairSharingStatus{}},
			wantWeightedShareMetric: new(0.0),
		},
		"fair sharing enabled populates weighted share": {
			fairSharingEnabled:      true,
			wantCohortStatus:        kueue.CohortStatus{FairSharing: &kueue.FairSharingStatus{}},
			wantStatusUpdates:       1,
			wantWeightedShareMetric: new(0.0),
		},
		"fair sharing enabled with zero weight borrowing cohort reports NaN metric and MaxInt64 status": {
			fairSharingEnabled: true,
			cohort: utiltestingapi.MakeCohort("cohort").
				Parent("root").
				FairWeight(resource.MustParse("0")).
				Obj(),
			setupCache: func(ctx context.Context, t *testing.T, cache *schdcache.Cache) {
				_, log := utiltesting.ContextWithLog(t)
				cache.AddOrUpdateResourceFlavor(log, utiltestingapi.MakeResourceFlavor("red").Obj())
				root := utiltestingapi.MakeCohort("root").ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("red").Resource(corev1.ResourceCPU, "10").Obj(),
				).Obj()
				if err := cache.AddOrUpdateCohort(root); err != nil {
					t.Fatalf("Inserting root cohort in cache: %v", err)
				}
				cq := utiltestingapi.MakeClusterQueue("cq").
					Cohort("cohort").
					FairWeight(resource.MustParse("0")).
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("red").Resource(corev1.ResourceCPU, "0").Obj()).
					Obj()
				if err := cache.AddClusterQueue(ctx, cq); err != nil {
					t.Fatalf("Inserting clusterQueue in cache: %v", err)
				}
				now := time.Now()
				wl := utiltestingapi.MakeWorkload("wl", "ns").
					Request(corev1.ResourceCPU, "2").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "red", "2").
							Obj()).
						Obj(), now).
					AdmittedAt(true, now).
					Obj()
				if !cache.AddOrUpdateWorkload(ctx, log, wl) {
					t.Fatal("Failed adding workload to cache")
				}
			},
			wantCohortStatus:        kueue.CohortStatus{FairSharing: &kueue.FairSharingStatus{WeightedShare: math.MaxInt64}},
			wantStatusUpdates:       1,
			wantWeightedShareMetric: new(math.NaN()),
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)
			cohort := tc.cohort
			if cohort == nil {
				cohort = utiltestingapi.MakeCohort("cohort").ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("red").Resource("cpu", "10").Obj(),
				).Obj()
			}
			cohort.Status = tc.cohortStatus
			metrics.ClearCohortMetrics(kueue.CohortReference(cohort.Name))
			t.Cleanup(func() {
				metrics.ClearCohortMetrics(kueue.CohortReference(cohort.Name))
			})
			var statusUpdates int
			cl := utiltesting.NewClientBuilder().
				WithObjects(cohort).
				WithStatusSubresource(cohort).
				WithInterceptorFuncs(interceptor.Funcs{SubResourceUpdate: utiltesting.CountSubResourceUpdates(&statusUpdates)}).
				Build()
			cache := schdcache.New(cl, schdcache.WithFairSharing(tc.fairSharingEnabled))
			if err := cache.AddOrUpdateCohort(cohort); err != nil {
				t.Fatalf("Inserting cohort in cache: %v", err)
			}
			if tc.setupCache != nil {
				tc.setupCache(ctx, t, cache)
			}
			qManager := qcache.NewManagerForUnitTests(cl, cache)
			reconciler := NewCohortReconciler(cl, cache, qManager, CohortReconcilerWithFairSharing(tc.fairSharingEnabled))

			if err := reconciler.updateCohortStatusIfChanged(ctx, cohort); err != nil {
				t.Fatalf("Updating cohort status: %v", err)
			}
			if diff := cmp.Diff(tc.wantCohortStatus, cohort.Status); diff != "" {
				t.Errorf("unexpected CohortStatus (-want,+got):\n%s", diff)
			}
			if statusUpdates != tc.wantStatusUpdates {
				t.Errorf("unexpected number of status updates: want %d, got %d", tc.wantStatusUpdates, statusUpdates)
			}
			if tc.wantWeightedShareMetric != nil {
				dps := testingmetrics.CollectFilteredGaugeVec(metrics.CohortWeightedShare, map[string]string{
					"cohort":       cohort.Name,
					"replica_role": "standalone",
				})
				if len(dps) != 1 {
					t.Fatalf("expected 1 CohortWeightedShare metric data point, got %d", len(dps))
				}
				if diff := cmp.Diff(*tc.wantWeightedShareMetric, dps[0].Value, cmpopts.EquateNaNs()); diff != "" {
					t.Errorf("unexpected CohortWeightedShare metric value (-want,+got):\n%s", diff)
				}
			}
		})
	}
}
