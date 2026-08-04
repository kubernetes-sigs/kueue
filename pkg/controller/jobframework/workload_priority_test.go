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

package jobframework

import (
	"context"
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
)

// The workloads of one job are written one after another, so a class edited in
// between could otherwise give each of them a different value, and later
// reconciles would not repair that because they compare the class name only.
func TestUpdateWorkloadPrioritiesResolvesTheClassOnce(t *testing.T) {
	ctx, _ := utiltesting.ContextWithLog(t)

	job := testingjob.MakeJob("job", "ns").WorkloadPriorityClass("high").Obj()
	highClass := utiltestingapi.MakeWorkloadPriorityClass("high").PriorityValue(100).Obj()
	first := utiltestingapi.MakeWorkload("first", "ns").
		WorkloadPriorityClassRef("low").Priority(10).Obj()
	second := utiltestingapi.MakeWorkload("second", "ns").
		WorkloadPriorityClassRef("low").Priority(10).Obj()

	// Every read of the class reports a different value, which stands in for an
	// administrator editing it while the workloads are being written.
	reads := 0
	cl := utiltesting.NewClientBuilder(batchv1.AddToScheme, kueue.AddToScheme).
		WithObjects(job, highClass, first, second).
		WithInterceptorFuncs(interceptor.Funcs{
			Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				if err := c.Get(ctx, key, obj, opts...); err != nil {
					return err
				}
				if class, ok := obj.(*kueue.WorkloadPriorityClass); ok && class.Name == "high" {
					reads++
					class.Value = int32(100 * reads)
				}
				return nil
			},
		}).
		Build()

	live := make([]*kueue.Workload, 0, 2)
	for _, name := range []string{"first", "second"} {
		wl := &kueue.Workload{}
		if err := cl.Get(ctx, types.NamespacedName{Namespace: "ns", Name: name}, wl); err != nil {
			t.Fatalf("getting workload %s: %v", name, err)
		}
		live = append(live, wl)
	}

	if err := updateWorkloadPriorities(ctx, cl, &utiltesting.EventRecorder{}, job, nil, live...); err != nil {
		t.Fatalf("updateWorkloadPriorities: %v", err)
	}

	got := make([]int32, 0, len(live))
	for _, wl := range live {
		if wl.Spec.Priority == nil {
			t.Fatalf("workload %s has no priority", wl.Name)
		}
		got = append(got, *wl.Spec.Priority)
	}
	if got[0] != got[1] {
		t.Errorf("workloads of one job settled on different priorities: %v", got)
	}
	if got[0] != 100 {
		t.Errorf("priority = %d, want the value read before the class changed (100)", got[0])
	}
	if live[0].Spec.PriorityClassRef == live[1].Spec.PriorityClassRef {
		t.Error("both workloads point at one priorityClassRef, so editing either would move both")
	}
}

// A quota-reserved workload with no priorityClassRef must be left untouched on the
// ordinary (non-slice) path too: the API server refuses to add a ref once quota is
// reserved, so attempting the update would wedge the reconcile. The guard lives in
// the shared helper, which the ordinary Job and LeaderWorkerSet paths reach
// directly through UpdateWorkloadPriority.
func TestUpdateWorkloadPrioritiesLeavesReservedNoRefWorkload(t *testing.T) {
	ctx, _ := utiltesting.ContextWithLog(t)

	job := testingjob.MakeJob("job", "ns").WorkloadPriorityClass("high").Obj()
	highClass := utiltestingapi.MakeWorkloadPriorityClass("high").PriorityValue(100).Obj()
	reserved := utiltestingapi.MakeWorkload("reserved", "ns").
		Condition(metav1.Condition{
			Type:               kueue.WorkloadQuotaReserved,
			Status:             metav1.ConditionTrue,
			Reason:             "AdmittedByTest",
			Message:            "reserved",
			LastTransitionTime: metav1.Now(),
		}).Obj()

	// The fake client does not run the Workload CEL rules, so if the guard is lost
	// this update would succeed here and set the ref, which the assertions catch.
	updates := 0
	cl := utiltesting.NewClientBuilder(batchv1.AddToScheme, kueue.AddToScheme).
		WithObjects(job, highClass, reserved).
		WithInterceptorFuncs(interceptor.Funcs{
			Update: func(ctx context.Context, c client.WithWatch, obj client.Object, opts ...client.UpdateOption) error {
				if _, ok := obj.(*kueue.Workload); ok {
					updates++
				}
				return c.Update(ctx, obj, opts...)
			},
		}).
		Build()

	wl := &kueue.Workload{}
	if err := cl.Get(ctx, types.NamespacedName{Namespace: "ns", Name: "reserved"}, wl); err != nil {
		t.Fatalf("getting workload: %v", err)
	}

	if err := UpdateWorkloadPriority(ctx, cl, &utiltesting.EventRecorder{}, job, wl, nil); err != nil {
		t.Fatalf("UpdateWorkloadPriority: %v", err)
	}

	if updates != 0 {
		t.Errorf("workload was updated %d times, want 0: a priorityClassRef cannot be added after quota reservation", updates)
	}
	persisted := &kueue.Workload{}
	if err := cl.Get(ctx, types.NamespacedName{Namespace: "ns", Name: "reserved"}, persisted); err != nil {
		t.Fatalf("re-getting workload: %v", err)
	}
	if persisted.Spec.PriorityClassRef != nil {
		t.Errorf("priorityClassRef = %v, want nil (left unchanged)", persisted.Spec.PriorityClassRef)
	}
}
