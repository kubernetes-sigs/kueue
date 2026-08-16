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

package queue

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func makeLimitRange(name, ns string, limitType corev1.LimitType) *corev1.LimitRange {
	return &corev1.LimitRange{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ns,
		},
		Spec: corev1.LimitRangeSpec{
			Limits: []corev1.LimitRangeItem{
				{
					Type: limitType,
				},
			},
		},
	}
}

func TestLimitRanges(t *testing.T) {
	cases := map[string]struct {
		operations func(lrs *LimitRanges)
		ns         string
		want       []corev1.LimitRange
	}{
		"add container LimitRange": {
			operations: func(lrs *LimitRanges) {
				lrs.AddOrUpdate(makeLimitRange("lr-container", "ns1", corev1.LimitTypeContainer))
			},
			ns:   "ns1",
			want: []corev1.LimitRange{*makeLimitRange("lr-container", "ns1", corev1.LimitTypeContainer)},
		},
		"add pod LimitRange": {
			operations: func(lrs *LimitRanges) {
				lrs.AddOrUpdate(makeLimitRange("lr-pod", "ns1", corev1.LimitTypePod))
			},
			ns:   "ns1",
			want: []corev1.LimitRange{*makeLimitRange("lr-pod", "ns1", corev1.LimitTypePod)},
		},
		"ignore non-container/pod LimitRange": {
			operations: func(lrs *LimitRanges) {
				lrs.AddOrUpdate(makeLimitRange("lr-pvc", "ns1", corev1.LimitTypePersistentVolumeClaim))
			},
			ns:   "ns1",
			want: nil,
		},
		"update LimitRange": {
			operations: func(lrs *LimitRanges) {
				lrs.AddOrUpdate(makeLimitRange("lr1", "ns1", corev1.LimitTypeContainer))
				lrs.Update(
					makeLimitRange("lr1", "ns1", corev1.LimitTypeContainer),
					makeLimitRange("lr1", "ns1", corev1.LimitTypePod),
				)
			},
			ns:   "ns1",
			want: []corev1.LimitRange{*makeLimitRange("lr1", "ns1", corev1.LimitTypePod)},
		},
		"update LimitRange to non-container/pod removes it": {
			operations: func(lrs *LimitRanges) {
				lrs.AddOrUpdate(makeLimitRange("lr1", "ns1", corev1.LimitTypeContainer))
				lrs.Update(
					makeLimitRange("lr1", "ns1", corev1.LimitTypeContainer),
					makeLimitRange("lr1", "ns1", corev1.LimitTypePersistentVolumeClaim),
				)
			},
			ns:   "ns1",
			want: nil,
		},
		"delete LimitRange": {
			operations: func(lrs *LimitRanges) {
				lr := makeLimitRange("lr-container", "ns1", corev1.LimitTypeContainer)
				lrs.AddOrUpdate(lr)
				lrs.Delete(lr)
			},
			ns:   "ns1",
			want: nil,
		},
		"get for non-existent namespace": {
			operations: func(lrs *LimitRanges) {
				lrs.AddOrUpdate(makeLimitRange("lr-container", "ns1", corev1.LimitTypeContainer))
			},
			ns:   "ns2",
			want: nil,
		},
		"returned LimitRanges are deep copies": {
			operations: func(lrs *LimitRanges) {
				lrs.AddOrUpdate(makeLimitRange("lr-container", "ns1", corev1.LimitTypeContainer))
				got := lrs.GetForNamespace("ns1")
				if len(got) > 0 {
					got[0].Name = "mutated-name"
				}
			},
			ns:   "ns1",
			want: []corev1.LimitRange{*makeLimitRange("lr-container", "ns1", corev1.LimitTypeContainer)},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			lrs := newLimitRanges()
			tc.operations(lrs)
			got := lrs.GetForNamespace(tc.ns)
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("Unexpected LimitRanges (-want,+got):\n%s", diff)
			}
		})
	}
}
