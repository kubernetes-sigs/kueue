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
	"testing"

	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/core/indexer"
	"sigs.k8s.io/kueue/pkg/util/roletracker"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
)

// A live read is still a read. The class can move after it, and if the sweep
// that follows finds the workload already holding the value it is moving to, it
// writes nothing and leaves the resourceVersion alone, so the write this
// reconcile is still carrying lands on top of it.
func TestReferenceReconcileNoticesTheClassMovingUnderIt(t *testing.T) {
	ctx, _ := utiltesting.ContextWithLog(t)

	class := utiltestingapi.MakeWorkloadPriorityClass("high").PriorityValue(100).Obj()
	// Already carrying the value the class is about to move to.
	wl := utiltestingapi.MakeWorkload("wl", "ns").WorkloadPriorityClassRef("high").Priority(200).Obj()

	var store client.WithWatch
	moved := false
	builder := utiltesting.NewClientBuilder().
		WithObjects(class, wl).
		WithIndex(&kueue.Workload{}, indexer.WorkloadPriorityClassKey, indexer.IndexWorkloadPriorityClass).
		WithInterceptorFuncs(interceptor.Funcs{
			Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				if err := c.Get(ctx, key, obj, opts...); err != nil {
					return err
				}
				if _, ok := obj.(*kueue.WorkloadPriorityClass); ok && !moved {
					moved = true
					// The read above returned 100. The class moves now, and its
					// controller runs before this reconcile gets to its write.
					var live kueue.WorkloadPriorityClass
					if err := store.Get(ctx, client.ObjectKey{Name: "high"}, &live); err != nil {
						t.Fatalf("moving the class: %v", err)
					}
					live.Value = 200
					if err := store.Update(ctx, &live); err != nil {
						t.Fatalf("moving the class: %v", err)
					}
					sweep := NewWorkloadPriorityClassReconciler(store, roletracker.NewFakeRoleTracker("leader"))
					if _, err := sweep.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: "high"}}); err != nil {
						t.Errorf("class sweep: %v", err)
					}
				}
				return nil
			},
		})
	store = builder.Build()

	ref := NewWorkloadPriorityClassReferenceReconciler(store, store, roletracker.NewFakeRoleTracker("leader"))
	req := ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "ns", Name: "wl"}}
	// Driven the way the manager drives it: a result asking to come back does.
	passes := 0
	for {
		res, err := ref.Reconcile(ctx, req)
		if err != nil {
			t.Fatalf("reference reconcile: %v", err)
		}
		passes++
		if res.RequeueAfter == 0 || passes > 4 {
			break
		}
	}
	t.Logf("passes: %d", passes)

	var got kueue.Workload
	if err := store.Get(ctx, client.ObjectKey{Namespace: "ns", Name: "wl"}, &got); err != nil {
		t.Fatal(err)
	}
	t.Logf("class value = 200, workload priority = %d", ptr.Deref(got.Spec.Priority, -1))
	if ptr.Deref(got.Spec.Priority, -1) != 200 {
		t.Errorf("WINDOW OPEN: workload left at %d", ptr.Deref(got.Spec.Priority, -1))
	}
}
