// Copyright The Kubernetes Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package afs

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	utilqueue "sigs.k8s.io/kueue/pkg/util/queue"
)

func TestSub(t *testing.T) {
	lqKey := utilqueue.NewLocalQueueReference("ns", "lq")

	cases := map[string]struct {
		push        corev1.ResourceList
		sub         corev1.ResourceList
		wantPending bool
		wantPenalty corev1.ResourceList
	}{
		"subtracting the pushed amount clears the entry": {
			push:        corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("4")},
			sub:         corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("4")},
			wantPending: false,
			wantPenalty: corev1.ResourceList{},
		},
		"partial subtraction keeps the remainder": {
			push:        corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("4")},
			sub:         corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1")},
			wantPending: true,
			wantPenalty: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("3")},
		},
		"subtracting more than pushed clamps at zero and clears the entry": {
			push:        corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("2")},
			sub:         corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("4")},
			wantPending: false,
			wantPenalty: corev1.ResourceList{},
		},
		"subtracting without a prior push does not leave a negative entry": {
			sub:         corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("4")},
			wantPending: false,
			wantPenalty: corev1.ResourceList{},
		},
		"clamping applies per resource": {
			push: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("4")},
			sub: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("1"),
				corev1.ResourceMemory: resource.MustParse("1Gi"),
			},
			wantPending: true,
			wantPenalty: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("3"),
				corev1.ResourceMemory: resource.MustParse("0"),
			},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			m := NewPenaltyMap()
			if tc.push != nil {
				m.Push(lqKey, tc.push.DeepCopy())
			}
			m.Sub(lqKey, tc.sub.DeepCopy())

			if got := m.HasPendingFor(lqKey); got != tc.wantPending {
				t.Errorf("HasPendingFor() = %v, want %v", got, tc.wantPending)
			}
			got := m.Peek(lqKey)
			if len(got) != len(tc.wantPenalty) {
				t.Fatalf("Peek() = %v, want %v", got, tc.wantPenalty)
			}
			for name, want := range tc.wantPenalty {
				if v, ok := got[name]; !ok || v.Cmp(want) != 0 {
					t.Errorf("Peek()[%s] = %s, want %s", name, v.String(), want.String())
				}
			}
		})
	}
}
