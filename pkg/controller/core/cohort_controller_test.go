/*
Copyright 2024 The Kubernetes Authors.

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
	"testing"

	"github.com/google/go-cmp/cmp"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"sigs.k8s.io/kueue/pkg/cache"
	"sigs.k8s.io/kueue/pkg/queue"
	"sigs.k8s.io/kueue/pkg/resources"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
)

func TestCohortReconcileCohortNotFoundDelete(t *testing.T) {
	cl := utiltesting.NewClientBuilder().Build()
	ctx := context.Background()
	cache := cache.New(cl)
	qManager := queue.NewManager(cl, cache)
	reconciler := NewCohortReconciler(cl, cache, qManager)

	cohort := utiltesting.MakeCohort("cohort").Obj()
	_ = cache.AddOrUpdateCohort(cohort)
	qManager.AddOrUpdateCohort(ctx, cohort)
	snapshot, err := cache.Snapshot(ctx)
	if err != nil {
		t.Fatalf("unexpected error while building snapshot: %v", err)
	}
	if _, ok := snapshot.Cohorts["cohort"]; !ok {
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
	if _, ok := snapshot.Cohorts["cohort"]; ok {
		t.Fatal("unexpected Cohort in snapshot")
	}
}

func TestCohortReconcileCohortNotFoundIdempotentDelete(t *testing.T) {
	cl := utiltesting.NewClientBuilder().
		Build()
	ctx := context.Background()
	cache := cache.New(cl)
	qManager := queue.NewManager(cl, cache)
	reconciler := NewCohortReconciler(cl, cache, qManager)

	snapshot, err := cache.Snapshot(ctx)
	if err != nil {
		t.Fatalf("unexpected error while building snapshot: %v", err)
	}
	if _, ok := snapshot.Cohorts["cohort"]; ok {
		t.Fatal("unexpected Cohort in snapshot")
	}

	cohort := utiltesting.MakeCohort("cohort").Obj()
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
	if _, ok := snapshot.Cohorts["cohort"]; ok {
		t.Fatal("unexpected Cohort in snapshot")
	}
}

func TestCohortReconcileCycleNoError(t *testing.T) {
	cohortA := utiltesting.MakeCohort("cohort-a").Parent("cohort-b").Obj()
	cohortB := utiltesting.MakeCohort("cohort-b").Parent("cohort-a").Obj()
	cl := utiltesting.NewClientBuilder().
		WithObjects(cohortA, cohortB).
		Build()
	ctx := context.Background()
	cache := cache.New(cl)
	qManager := queue.NewManager(cl, cache)
	reconciler := NewCohortReconciler(cl, cache, qManager)

	// no cycle when creating first cohort
	if _, err := reconciler.Reconcile(
		ctx,
		reconcile.Request{NamespacedName: client.ObjectKeyFromObject(cohortA)},
	); err != nil {
		t.Fatal("unexpected error")
	}

	// cycle added, no error
	if _, err := reconciler.Reconcile(
		ctx,
		reconcile.Request{NamespacedName: client.ObjectKeyFromObject(cohortB)},
	); err != nil {
		t.Fatal("unexpected error when adding cycle", err)
	}

	// remove cycle, no error
	if err := cl.Get(ctx, client.ObjectKeyFromObject(cohortB), cohortB); err != nil {
		t.Fatal("unexpected error")
	}
	cohortB.Spec.Parent = "cohort-c"
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

func TestCohortReconcileErrorOtherThanNotFoundNotDeleted(t *testing.T) {
	ctx := context.Background()
	funcs := interceptor.Funcs{
		Get: func(ctx context.Context, client client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
			return errors.New("error")
		},
	}
	cl := utiltesting.NewClientBuilder().WithInterceptorFuncs(funcs).Build()

	cache := cache.New(cl)
	qManager := queue.NewManager(cl, cache)
	reconciler := NewCohortReconciler(cl, cache, qManager)
	cohort := utiltesting.MakeCohort("cohort").Obj()
	_ = cache.AddOrUpdateCohort(cohort)
	qManager.AddOrUpdateCohort(ctx, cohort)
	snapshot, err := cache.Snapshot(ctx)
	if err != nil {
		t.Fatalf("unexpected error while building snapshot: %v", err)
	}
	if _, ok := snapshot.Cohorts["cohort"]; !ok {
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
	if _, ok := snapshot.Cohorts["cohort"]; !ok {
		t.Fatal("expected Cohort in snapshot")
	}
}

func TestCohortReconcileLifecycle(t *testing.T) {
	ctx := context.Background()
	cohort := utiltesting.MakeCohort("cohort").ResourceGroup(
		utiltesting.MakeFlavorQuotas("red").Resource("cpu", "10").FlavorQuotas,
	).Obj()
	cl := utiltesting.NewClientBuilder().WithObjects(cohort).Build()
	cache := cache.New(cl)
	qManager := queue.NewManager(cl, cache)
	reconciler := NewCohortReconciler(cl, cache, qManager)

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
		cohortSnap, ok := snapshot.Cohorts["cohort"]
		if !ok {
			t.Fatal("expected Cohort in snapshot")
		}

		wantQuotas := resources.FlavorResourceQuantities{
			{Flavor: "red", Resource: "cpu"}: 10_000,
		}
		if diff := cmp.Diff(wantQuotas, cohortSnap.ResourceNode.SubtreeQuota); diff != "" {
			t.Fatalf("unexpected quota (-want +got) %s", diff)
		}
	}

	// update
	{
		if err := cl.Get(ctx, client.ObjectKeyFromObject(cohort), cohort); err != nil {
			t.Fatal("unexpected error")
		}
		cohort.Spec.ResourceGroups[0] = utiltesting.ResourceGroup(
			utiltesting.MakeFlavorQuotas("red").Resource("cpu", "5").FlavorQuotas,
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
		cohortSnap, ok := snapshot.Cohorts["cohort"]
		if !ok {
			t.Fatal("expected Cohort in snapshot")
		}

		wantQuotas := resources.FlavorResourceQuantities{
			{Flavor: "red", Resource: "cpu"}: 5_000,
		}
		if diff := cmp.Diff(wantQuotas, cohortSnap.ResourceNode.SubtreeQuota); diff != "" {
			t.Fatalf("unexpected quota (-want +got) %s", diff)
		}
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
		if _, ok := snapshot.Cohorts["cohort"]; ok {
			t.Fatal("unexpected Cohort in snapshot")
		}
	}
}

func TestCohortReconcilerFilters(t *testing.T) {
	cl := utiltesting.NewClientBuilder().
		Build()
	cache := cache.New(cl)
	qManager := queue.NewManager(cl, cache)
	reconciler := NewCohortReconciler(cl, cache, qManager)

	t.Run("delete returns true", func(t *testing.T) {
		if !reconciler.Delete(event.DeleteEvent{}) {
			t.Fatal("expected delete to return true")
		}
	})

	t.Run("create returns true", func(t *testing.T) {
		if !reconciler.Create(event.CreateEvent{}) {
			t.Fatal("expected create to return true")
		}
	})

	t.Run("generic returns true", func(t *testing.T) {
		if !reconciler.Generic(event.GenericEvent{}) {
			t.Fatal("expected generic to return true")
		}
	})

	cases := map[string]struct {
		old  client.Object
		new  client.Object
		want bool
	}{
		"old wrong type returns false": {
			old:  utiltesting.MakeClusterQueue("cq").Obj(),
			new:  utiltesting.MakeCohort("cohort").Obj(),
			want: false,
		},
		"new wrong type returns false": {
			old:  utiltesting.MakeCohort("cohort").Obj(),
			new:  utiltesting.MakeClusterQueue("cq").Obj(),
			want: false,
		},
		"unchanged returns false": {
			old: utiltesting.MakeCohort("cohort").ResourceGroup(
				utiltesting.MakeFlavorQuotas("red").Resource("cpu", "5").FlavorQuotas,
			).Obj(),
			new: utiltesting.MakeCohort("cohort").ResourceGroup(
				utiltesting.MakeFlavorQuotas("red").Resource("cpu", "5").FlavorQuotas,
			).Obj(),
			want: false,
		},
		"changed resource returns true": {
			old: utiltesting.MakeCohort("cohort").ResourceGroup(
				utiltesting.MakeFlavorQuotas("red").Resource("cpu", "5").FlavorQuotas,
			).Obj(),
			new: utiltesting.MakeCohort("cohort").ResourceGroup(
				utiltesting.MakeFlavorQuotas("red").Resource("cpu", "10").FlavorQuotas,
			).Obj(),
			want: true,
		},
		"adding parent returns true": {
			old:  utiltesting.MakeCohort("cohort").Obj(),
			new:  utiltesting.MakeCohort("cohort").Parent("parent").Obj(),
			want: true,
		},
		"changing parent returns true": {
			old:  utiltesting.MakeCohort("cohort").Parent("old").Obj(),
			new:  utiltesting.MakeCohort("cohort").Parent("new").Obj(),
			want: true,
		},
		"deleting parent returns true": {
			old:  utiltesting.MakeCohort("cohort").Parent("parent").Obj(),
			new:  utiltesting.MakeCohort("cohort").Obj(),
			want: true,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			event := event.UpdateEvent{
				ObjectOld: tc.old,
				ObjectNew: tc.new,
			}
			if reconciler.Update(event) != tc.want {
				t.Fatalf("expected %v, got %v", tc.want, !tc.want)
			}
		})
	}
}
