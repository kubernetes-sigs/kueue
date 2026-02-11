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

package integration

import (
	"context"
	"testing"

	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/cmd/experimental/preemption-cost-controller/pkg/constants"
	controllerpkg "sigs.k8s.io/kueue/cmd/experimental/preemption-cost-controller/pkg/controller"
)

func TestReconcileUsesWorkloadEvictionStats(t *testing.T) {
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
						Count:  2,
					},
				},
			},
		},
	}

	cl := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(wl).WithObjects(job, wl).Build()
	reconciler := controllerpkg.NewPreemptionCostReconciler(cl, record.NewFakeRecorder(32))

	req := ctrl.Request{
		NamespacedName: types.NamespacedName{Name: job.Name, Namespace: job.Namespace},
	}
	if _, err := reconciler.Reconcile(ctx, req); err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}

	var updated batchv1.Job
	if err := cl.Get(ctx, types.NamespacedName{Name: job.Name, Namespace: job.Namespace}, &updated); err != nil {
		t.Fatalf("Get job failed: %v", err)
	}
	if got, want := updated.Annotations[constants.PreemptionCostAnnotationKey], "2"; got != want {
		t.Fatalf("Unexpected annotation value: got %q, want %q", got, want)
	}
}
