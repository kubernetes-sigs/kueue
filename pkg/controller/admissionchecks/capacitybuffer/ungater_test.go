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

package capacitybuffer

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	testingpod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
)

func TestUngater(t *testing.T) {
	const otherGate = "example.com/other"
	cases := map[string]struct {
		pod       *corev1.Pod
		wantGates []corev1.PodSchedulingGate
	}{
		"removes only the CapacityBuffer gate": {
			pod:       testingpod.MakePod("pod", testNamespace).Gate(otherGate, kueuealpha.CapacityBufferSchedulingGate).Obj(),
			wantGates: []corev1.PodSchedulingGate{{Name: otherGate}},
		},
		"keeps a Pod without the CapacityBuffer gate": {
			pod:       testingpod.MakePod("pod", testNamespace).Gate(otherGate).Obj(),
			wantGates: []corev1.PodSchedulingGate{{Name: otherGate}},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)
			k8sClient := fake.NewClientBuilder().WithScheme(newScheme()).WithObjects(tc.pod).Build()
			ungater := NewUngater(k8sClient, nil)

			if _, err := ungater.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(tc.pod)}); err != nil {
				t.Fatalf("Unexpected reconcile error: %v", err)
			}
			got := &corev1.Pod{}
			if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(tc.pod), got); err != nil {
				t.Fatalf("Getting the Pod: %v", err)
			}
			if diff := cmp.Diff(tc.wantGates, got.Spec.SchedulingGates); diff != "" {
				t.Errorf("Unexpected scheduling gates (-want/+got):\n%s", diff)
			}
		})
	}

	t.Run("ignores a deleted Pod", func(t *testing.T) {
		ctx, _ := utiltesting.ContextWithLog(t)
		ungater := NewUngater(fake.NewClientBuilder().WithScheme(newScheme()).Build(), nil)
		missing := testingpod.MakePod("missing", testNamespace).Obj()
		if _, err := ungater.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(missing)}); err != nil {
			t.Errorf("Unexpected reconcile error: %v", err)
		}
	})
}

func TestHasCapacityBufferGate(t *testing.T) {
	if !hasCapacityBufferGate(testingpod.MakePod("pod", testNamespace).Gate(kueuealpha.CapacityBufferSchedulingGate).Obj()) {
		t.Error("hasCapacityBufferGate() = false for a gated Pod")
	}
	if hasCapacityBufferGate(testingpod.MakePod("pod", testNamespace).Obj()) {
		t.Error("hasCapacityBufferGate() = true for a Pod without the gate")
	}
}
