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

	batchv1 "k8s.io/api/batch/v1"
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
	"sigs.k8s.io/kueue/cmd/experimental/preemption-cost-controller/pkg/constants"
)

func TestComputeCost(t *testing.T) {
	reconciler := &PreemptionCostReconciler{
		enableRunningPhaseIncrement: true,
		runningPhaseIncrement:       2,
		maxCost:                     5,
	}
	job := &batchv1.Job{
		Status: batchv1.JobStatus{
			Active: 1,
		},
	}
	wl := &kueue.Workload{
		Status: kueue.WorkloadStatus{
			SchedulingStats: &kueue.SchedulingStats{
				Evictions: []kueue.WorkloadSchedulingStatsEviction{
					{
						Reason: kueue.WorkloadEvictedByPreemption,
						Count:  4,
					},
					{
						Reason: kueue.WorkloadEvictedByAdmissionCheck,
						Count:  10,
					},
				},
			},
		},
	}
	if got, want := reconciler.computeCost(job, wl), int32(5); got != want {
		t.Fatalf("Unexpected cost: got %d, want %d", got, want)
	}
}

func TestReconcileIdempotentUpdate(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	if err := batchv1.AddToScheme(scheme); err != nil {
		t.Fatalf("Failed adding batch/v1 to scheme: %v", err)
	}
	if err := kueue.AddToScheme(scheme); err != nil {
		t.Fatalf("Failed adding kueue to scheme: %v", err)
	}

	controller := true
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "job-1",
			Namespace: "ns",
		},
	}
	wl := &kueue.Workload{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "wl-1",
			Namespace: "ns",
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: batchv1.SchemeGroupVersion.String(),
					Kind:       "Job",
					Name:       job.Name,
					Controller: &controller,
				},
			},
		},
		Status: kueue.WorkloadStatus{
			SchedulingStats: &kueue.SchedulingStats{
				Evictions: []kueue.WorkloadSchedulingStatsEviction{
					{
						Reason: kueue.WorkloadEvictedByPreemption,
						Count:  3,
					},
				},
			},
		},
	}

	cl := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(wl).WithObjects(job, wl).Build()
	r := NewPreemptionCostReconciler(cl, record.NewFakeRecorder(32))
	req := types.NamespacedName{Name: job.Name, Namespace: job.Namespace}
	if _, err := r.Reconcile(ctx, ctrlRequest(req)); err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}

	var updated batchv1.Job
	if err := cl.Get(ctx, req, &updated); err != nil {
		t.Fatalf("Get job failed: %v", err)
	}
	if got, want := updated.Annotations[constants.PreemptionCostAnnotationKey], "3"; got != want {
		t.Fatalf("Unexpected annotation value: got %q, want %q", got, want)
	}

	if _, err := r.Reconcile(ctx, ctrlRequest(req)); err != nil {
		t.Fatalf("Second reconcile failed: %v", err)
	}
}

func TestReconcileConflictThenSuccess(t *testing.T) {
	ctx := context.Background()
	scheme := runtime.NewScheme()
	_ = batchv1.AddToScheme(scheme)
	_ = kueue.AddToScheme(scheme)

	controller := true
	job := &batchv1.Job{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "job-1",
			Namespace: "ns",
		},
	}
	wl := &kueue.Workload{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "wl-1",
			Namespace: "ns",
			OwnerReferences: []metav1.OwnerReference{
				{
					APIVersion: batchv1.SchemeGroupVersion.String(),
					Kind:       "Job",
					Name:       job.Name,
					Controller: &controller,
				},
			},
		},
		Status: kueue.WorkloadStatus{
			SchedulingStats: &kueue.SchedulingStats{
				Evictions: []kueue.WorkloadSchedulingStatsEviction{
					{
						Reason: kueue.WorkloadEvictedByPreemption,
						Count:  1,
					},
				},
			},
		},
	}

	baseClient := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(wl).WithObjects(job, wl).Build()
	conflictInjected := false
	cl := interceptor.NewClient(baseClient, interceptor.Funcs{
		Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
			if !conflictInjected {
				conflictInjected = true
				return apierrors.NewConflict(schema.GroupResource{Group: "batch", Resource: "jobs"}, obj.GetName(), nil)
			}
			return c.Patch(ctx, obj, patch, opts...)
		},
	})

	r := NewPreemptionCostReconciler(cl, record.NewFakeRecorder(32))
	req := types.NamespacedName{Name: job.Name, Namespace: job.Namespace}
	_, err := r.Reconcile(ctx, ctrlRequest(req))
	if err == nil {
		t.Fatalf("Expected first reconcile to fail with conflict")
	}
	_, err = r.Reconcile(ctx, ctrlRequest(req))
	if err != nil {
		t.Fatalf("Expected second reconcile to succeed, got: %v", err)
	}

	var updated batchv1.Job
	if err := baseClient.Get(ctx, req, &updated); err != nil {
		t.Fatalf("Get job failed: %v", err)
	}
	if got, want := updated.Annotations[constants.PreemptionCostAnnotationKey], "1"; got != want {
		t.Fatalf("Unexpected annotation value: got %q, want %q", got, want)
	}
}

func ctrlRequest(name types.NamespacedName) ctrl.Request {
	return ctrl.Request{NamespacedName: name}
}
