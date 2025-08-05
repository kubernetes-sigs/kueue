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
	"testing"

	"github.com/google/go-cmp/cmp"
	"k8s.io/apimachinery/pkg/api/resource"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/cache"
	"sigs.k8s.io/kueue/pkg/queue"
	"sigs.k8s.io/kueue/pkg/resources"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
)

func TestCohortReconcileCohortNotFoundDelete(t *testing.T) {
	cl := utiltesting.NewClientBuilder().Build()
	ctx := t.Context()
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
	ctx := t.Context()
	cache := cache.New(cl)
	qManager := queue.NewManager(cl, cache)
	reconciler := NewCohortReconciler(cl, cache, qManager)

	snapshot, err := cache.Snapshot(ctx)
	if err != nil {
		t.Fatalf("unexpected error while building snapshot: %v", err)
	}
	if cohortSnap := snapshot.Cohort("cohort"); cohortSnap != nil {
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
	if cohortSnap := snapshot.Cohort("cohort"); cohortSnap != nil {
		t.Fatal("unexpected Cohort in snapshot")
	}
}

func TestCohortReconcileCycleNoError(t *testing.T) {
	cohortA := utiltesting.MakeCohort("cohort-a").Parent("cohort-b").Obj()
	cohortB := utiltesting.MakeCohort("cohort-b").Parent("cohort-a").Obj()
	cl := utiltesting.NewClientBuilder().
		WithObjects(cohortA, cohortB).
		WithStatusSubresource(&kueue.Cohort{}).
		Build()
	ctx := t.Context()
	cache := cache.New(cl)
	qManager := queue.NewManager(cl, cache)
	reconciler := NewCohortReconciler(cl, cache, qManager)

	// no cycle when creating first cohort
	if _, err := reconciler.Reconcile(
		ctx,
		reconcile.Request{NamespacedName: client.ObjectKeyFromObject(cohortA)},
	); err != nil {
		t.Fatalf("unexpected error: %v", err)
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

func TestCohortReconcileErrorOtherThanNotFoundNotDeleted(t *testing.T) {
	ctx := t.Context()
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
	ctx := t.Context()
	cohort := utiltesting.MakeCohort("cohort").ResourceGroup(
		*utiltesting.MakeFlavorQuotas("red").Resource("cpu", "10").Obj(),
	).Obj()
	cl := utiltesting.NewClientBuilder().WithObjects(cohort).WithStatusSubresource(&kueue.Cohort{}).Build()
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
		cohortSnap := snapshot.Cohort("cohort")
		if cohortSnap == nil {
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
			*utiltesting.MakeFlavorQuotas("red").Resource("cpu", "5").Obj(),
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
		if cohortSnap := snapshot.Cohort("cohort"); cohortSnap != nil {
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
		old  *kueue.Cohort
		new  *kueue.Cohort
		want bool
	}{
		"unchanged returns false": {
			old: utiltesting.MakeCohort("cohort").ResourceGroup(
				*utiltesting.MakeFlavorQuotas("red").Resource("cpu", "5").Obj(),
			).Obj(),
			new: utiltesting.MakeCohort("cohort").ResourceGroup(
				*utiltesting.MakeFlavorQuotas("red").Resource("cpu", "5").Obj(),
			).Obj(),
			want: false,
		},
		"changed resource returns true": {
			old: utiltesting.MakeCohort("cohort").ResourceGroup(
				*utiltesting.MakeFlavorQuotas("red").Resource("cpu", "5").Obj(),
			).Obj(),
			new: utiltesting.MakeCohort("cohort").ResourceGroup(
				*utiltesting.MakeFlavorQuotas("red").Resource("cpu", "10").Obj(),
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
		"adding weight returns true": {
			old:  utiltesting.MakeCohort("cohort").Obj(),
			new:  utiltesting.MakeCohort("cohort").FairWeight(resource.MustParse("1")).Obj(),
			want: true,
		},
		"deleting weight returns true": {
			old:  utiltesting.MakeCohort("cohort").FairWeight(resource.MustParse("1")).Obj(),
			new:  utiltesting.MakeCohort("cohort").Obj(),
			want: true,
		},
		"updating weight returns true": {
			old:  utiltesting.MakeCohort("cohort").FairWeight(resource.MustParse("1")).Obj(),
			new:  utiltesting.MakeCohort("cohort").FairWeight(resource.MustParse("2")).Obj(),
			want: true,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
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
