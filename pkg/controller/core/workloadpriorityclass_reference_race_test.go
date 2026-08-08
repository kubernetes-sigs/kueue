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

// This reconcile is queued by a Workload event, so nothing orders its read of
// the class against the class controller's own pass. Read from a cache, that
// value can be one this replica took before the class moved, and by the time
// the write lands the pass that would have repaired it has already looked at a
// workload that was still correct and left it alone. Nothing is queued after
// that: the write only changed a number, which the predicate filters.
func TestReferenceReconcileDoesNotWriteAClassValueItReadEarlier(t *testing.T) {
	ctx, _ := utiltesting.ContextWithLog(t)

	live := utiltestingapi.MakeWorkloadPriorityClass("high").PriorityValue(200).Obj()
	wl := utiltestingapi.MakeWorkload("wl", "ns").WorkloadPriorityClassRef("high").Priority(200).Obj()

	classReads := 0
	var cl client.WithWatch
	builder := utiltesting.NewClientBuilder().
		WithObjects(live, wl).
		WithIndex(&kueue.Workload{}, indexer.WorkloadPriorityClassKey, indexer.IndexWorkloadPriorityClass).
		WithInterceptorFuncs(interceptor.Funcs{
			Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				if err := c.Get(ctx, key, obj, opts...); err != nil {
					return err
				}
				class, ok := obj.(*kueue.WorkloadPriorityClass)
				if !ok {
					return nil
				}
				classReads++
				if classReads == 1 {
					// The reference reconcile's read, taken before this replica
					// saw the class move.
					class.Value = 100
					// While it holds that, the class controller runs on the value
					// the store now has.
					sweep := NewWorkloadPriorityClassReconciler(cl, roletracker.NewFakeRoleTracker("leader"))
					if _, err := sweep.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Name: "high"}}); err != nil {
						t.Errorf("class sweep: %v", err)
					}
				}
				return nil
			},
		})
	cl = builder.Build()
	// The API server itself, which the interceptor above does not sit in front of.
	apiReader := utiltesting.NewClientBuilder().WithObjects(live, wl).Build()

	ref := NewWorkloadPriorityClassReferenceReconciler(cl, apiReader, roletracker.NewFakeRoleTracker("leader"))
	if _, err := ref.Reconcile(ctx, ctrl.Request{NamespacedName: types.NamespacedName{Namespace: "ns", Name: "wl"}}); err != nil {
		t.Fatalf("reference reconcile: %v", err)
	}

	var got kueue.Workload
	if err := cl.Get(ctx, client.ObjectKey{Namespace: "ns", Name: "wl"}, &got); err != nil {
		t.Fatal(err)
	}
	t.Logf("class reads: %d; workload priority = %d, class value = 200", classReads, ptr.Deref(got.Spec.Priority, -1))
	if ptr.Deref(got.Spec.Priority, -1) != 200 {
		t.Errorf("STALE WRITE WON: workload left at %d", ptr.Deref(got.Spec.Priority, -1))
	}
}
