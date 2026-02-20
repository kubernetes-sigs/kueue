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

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/record"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/cmd/experimental/priority-boost-controller/pkg/constants"
	controllerpkg "sigs.k8s.io/kueue/cmd/experimental/priority-boost-controller/pkg/controller"
)

func TestReconcileUsesWorkloadEvictionStats(t *testing.T) {
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
						Count:  4,
					},
				},
			},
		},
	}

	cl := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(wl).WithObjects(wl).Build()
	reconciler := controllerpkg.NewPriorityBoostReconciler(cl, record.NewFakeRecorder(32))

	req := ctrl.Request{
		NamespacedName: types.NamespacedName{Name: wl.Name, Namespace: wl.Namespace},
	}
	if _, err := reconciler.Reconcile(ctx, req); err != nil {
		t.Fatalf("Reconcile failed: %v", err)
	}

	var updated kueue.Workload
	if err := cl.Get(ctx, types.NamespacedName{Name: wl.Name, Namespace: wl.Namespace}, &updated); err != nil {
		t.Fatalf("Get workload failed: %v", err)
	}
	// 4 preemptions / 2 = 2
	if got, want := updated.Annotations[constants.PriorityBoostAnnotationKey], "2"; got != want {
		t.Fatalf("Unexpected annotation value: got %q, want %q", got, want)
	}
}
