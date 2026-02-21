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

package controller

import (
	"context"
	"testing"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/cmd/experimental/priority-boost-controller/pkg/constants"
)

func TestComputeBoost(t *testing.T) {
	reconciler := &PriorityBoostReconciler{
		maxBoost: 5,
	}
	wl := &kueue.Workload{
		Status: kueue.WorkloadStatus{
			SchedulingStats: &kueue.SchedulingStats{
				Evictions: []kueue.WorkloadSchedulingStatsEviction{
					{
						Reason: kueue.WorkloadEvictedByPreemption,
						Count:  8,
					},
					{
						Reason: kueue.WorkloadEvictedByAdmissionCheck,
						Count:  10,
					},
				},
			},
		},
	}
	// 8 preemptions / 2 = 4, capped at maxBoost=5 -> 4
	if got, want := reconciler.computeBoost(wl), int32(4); got != want {
		t.Fatalf("Unexpected boost: got %d, want %d", got, want)
	}
}

func TestComputeBoostMaxCapped(t *testing.T) {
	reconciler := &PriorityBoostReconciler{
		maxBoost: 3,
	}
	wl := &kueue.Workload{
		Status: kueue.WorkloadStatus{
			SchedulingStats: &kueue.SchedulingStats{
				Evictions: []kueue.WorkloadSchedulingStatsEviction{
					{
						Reason: kueue.WorkloadEvictedByPreemption,
						Count:  10,
					},
				},
			},
		},
	}
	// 10 / 2 = 5, capped at maxBoost=3 -> 3
	if got, want := reconciler.computeBoost(wl), int32(3); got != want {
		t.Fatalf("Unexpected boost: got %d, want %d", got, want)
	}
}

func TestReconcileIdempotentUpdate(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	if err := kueue.AddToScheme(scheme); err != nil {
		t.Fatalf("Failed adding kueue to scheme: %v", err)
	}

	wl := &kueue.Workload{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "wl-1",
			Namespace: "ns",
		},
		Status: kueue.WorkloadStatus{
			SchedulingStats: &kueue.SchedulingStats{
				Evictions: []kueue.WorkloadSchedulingStatsEviction{
					{
						Reason: kueue.WorkloadEvictedByPreemption,
						Count:  6,
					},
				},
			},
		},
	}

	cl := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(wl).WithObjects(wl).Build()
	r := NewPriorityBoostReconciler(cl, record.NewFakeRecorder(32))
	req := types.NamespacedName{Name: wl.Name, Namespace: wl.Namespace}
	if _, err := r.Reconcile(ctx, ctrlRequest(req)); err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}

	var updated kueue.Workload
	if err := cl.Get(ctx, req, &updated); err != nil {
		t.Fatalf("Get workload failed: %v", err)
	}
	// 6 / 2 = 3
	if got, want := updated.Annotations[constants.PriorityBoostAnnotationKey], "3"; got != want {
		t.Fatalf("Unexpected annotation value: got %q, want %q", got, want)
	}

	if _, err := r.Reconcile(ctx, ctrlRequest(req)); err != nil {
		t.Fatalf("Second reconcile failed: %v", err)
	}
}

func TestReconcileConflictThenSuccess(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	_ = kueue.AddToScheme(scheme)

	wl := &kueue.Workload{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "wl-1",
			Namespace: "ns",
		},
		Status: kueue.WorkloadStatus{
			SchedulingStats: &kueue.SchedulingStats{
				Evictions: []kueue.WorkloadSchedulingStatsEviction{
					{
						Reason: kueue.WorkloadEvictedByPreemption,
						Count:  4,
					},
				},
			},
		},
	}

	baseClient := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(wl).WithObjects(wl).Build()
	conflictInjected := false
	cl := interceptor.NewClient(baseClient, interceptor.Funcs{
		Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
			if !conflictInjected {
				conflictInjected = true
				return apierrors.NewConflict(schema.GroupResource{Group: "kueue.x-k8s.io", Resource: "workloads"}, obj.GetName(), nil)
			}
			return c.Patch(ctx, obj, patch, opts...)
		},
	})

	r := NewPriorityBoostReconciler(cl, record.NewFakeRecorder(32))
	req := types.NamespacedName{Name: wl.Name, Namespace: wl.Namespace}
	_, err := r.Reconcile(ctx, ctrlRequest(req))
	if err == nil {
		t.Fatalf("Expected first reconcile to fail with conflict")
	}
	_, err = r.Reconcile(ctx, ctrlRequest(req))
	if err != nil {
		t.Fatalf("Expected second reconcile to succeed, got: %v", err)
	}

	var updated kueue.Workload
	if err := baseClient.Get(ctx, req, &updated); err != nil {
		t.Fatalf("Get workload failed: %v", err)
	}
	// 4 / 2 = 2
	if got, want := updated.Annotations[constants.PriorityBoostAnnotationKey], "2"; got != want {
		t.Fatalf("Unexpected annotation value: got %q, want %q", got, want)
	}
}

func ctrlRequest(name types.NamespacedName) ctrl.Request {
	return ctrl.Request{NamespacedName: name}
}
