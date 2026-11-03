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

package webhook

import (
	"context"
	"testing"

	"github.com/google/go-cmp/cmp"
	admissionv1 "k8s.io/api/admission/v1"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
)

func TestOldObjectFromContext(t *testing.T) {
	oldPod := &corev1.Pod{Name: "pod", Namespace: "ns", Labels: map[string]string{"key": "value"}}
	testCases := map[string]struct {
		ctx  func(context.Context) context.Context
		want *corev1.Pod
	}{
		"no admission request": {
			ctx: func(ctx context.Context) context.Context { return ctx },
		},
		"create request": {
			ctx: func(ctx context.Context) context.Context {
				return admission.NewContextWithRequest(ctx, admission.Request{Operation: admissionv1.Create})
			},
		},
		"update request": {
			ctx: func(ctx context.Context) context.Context {
				return utiltesting.ContextWithUpdateRequest(ctx, t, oldPod)
			},
			want: oldPod,
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			got, err := OldObjectFromContext[corev1.Pod](tc.ctx(t.Context()))
			if err != nil {
				t.Fatalf("Unexpected error: %v", err)
			}
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("Unexpected old object (-want,+got):\n%s", diff)
			}
		})
	}
}
