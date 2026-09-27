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

package util_test

import (
	"context"
	"testing"

	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	"sigs.k8s.io/kueue/test/util"
)

func TestPodNamesByGate(t *testing.T) {
	const (
		namespace = "test"
		gateName  = "example.com/gate"
	)
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme() error = %v", err)
	}
	client := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		&corev1.Pod{Name: "gated", Namespace: namespace, Spec: corev1.PodSpec{SchedulingGates: []corev1.PodSchedulingGate{{Name: gateName}}}},
		&corev1.Pod{Name: "other-gate", Namespace: namespace, Spec: corev1.PodSpec{SchedulingGates: []corev1.PodSchedulingGate{{Name: "example.com/other"}}}},
		&corev1.Pod{Name: "ungated", Namespace: namespace},
	).Build()

	testCases := map[string]struct {
		get  func(context.Context, string) ([]string, error)
		want []string
	}{
		"gated": {
			get: func(ctx context.Context, gate string) ([]string, error) {
				return util.GatedPodNames(ctx, client, namespace, gate)
			},
			want: []string{"gated"},
		},
		"ungated": {
			get: func(ctx context.Context, gate string) ([]string, error) {
				return util.UngatedPodNames(ctx, client, namespace, gate)
			},
			want: []string{"other-gate", "ungated"},
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			got, err := tc.get(t.Context(), gateName)
			if err != nil {
				t.Fatalf("Pod names error = %v", err)
			}
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("Pod names (-want,+got):\n%s", diff)
			}
		})
	}
}
