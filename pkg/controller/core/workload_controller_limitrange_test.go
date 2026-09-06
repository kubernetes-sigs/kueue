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

package core

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
)

func TestLimitRangeConstraintsChanged(t *testing.T) {
	base := func() *corev1.LimitRange {
		return utiltesting.MakeLimitRange("limits", "ns").
			WithValue("Max", corev1.ResourceCPU, "2").
			WithValue("Min", corev1.ResourceCPU, "1").
			WithValue("DefaultRequest", corev1.ResourceCPU, "1").
			Obj()
	}
	cases := map[string]struct {
		mutate func(*corev1.LimitRange)
		want   bool
	}{
		"no change": {
			mutate: func(*corev1.LimitRange) {},
			want:   false,
		},
		"max changed": {
			mutate: func(lr *corev1.LimitRange) {
				lr.Spec.Limits[0].Max[corev1.ResourceCPU] = resource.MustParse("8")
			},
			want: true,
		},
		"min changed": {
			mutate: func(lr *corev1.LimitRange) {
				lr.Spec.Limits[0].Min[corev1.ResourceCPU] = resource.MustParse("500m")
			},
			want: true,
		},
		"maxLimitRequestRatio changed": {
			mutate: func(lr *corev1.LimitRange) {
				lr.Spec.Limits[0].MaxLimitRequestRatio[corev1.ResourceCPU] = resource.MustParse("2")
			},
			want: true,
		},
		"only defaultRequest changed": {
			mutate: func(lr *corev1.LimitRange) {
				lr.Spec.Limits[0].DefaultRequest[corev1.ResourceCPU] = resource.MustParse("2")
			},
			want: false,
		},
		"item type changed": {
			mutate: func(lr *corev1.LimitRange) {
				lr.Spec.Limits[0].Type = corev1.LimitTypePod
			},
			want: true,
		},
		"item added": {
			mutate: func(lr *corev1.LimitRange) {
				lr.Spec.Limits = append(lr.Spec.Limits, corev1.LimitRangeItem{Type: corev1.LimitTypePod})
			},
			want: true,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			oldLr := base()
			newLr := base()
			tc.mutate(newLr)
			if got := limitRangeConstraintsChanged(oldLr, newLr); got != tc.want {
				t.Errorf("limitRangeConstraintsChanged() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestLimitRangeConstraintsChangedOnDeletion(t *testing.T) {
	withConstraints := utiltesting.MakeLimitRange("limits", "ns").
		WithValue("Max", corev1.ResourceCPU, "2").Obj()
	defaultsOnly := utiltesting.MakeLimitRange("limits", "ns").
		WithValue("DefaultRequest", corev1.ResourceCPU, "1").Obj()

	if !limitRangeConstraintsChanged(withConstraints, nil) {
		t.Error("limitRangeConstraintsChanged(withConstraints, nil) = false, want true")
	}
	if !limitRangeConstraintsChanged(defaultsOnly, nil) {
		t.Error("limitRangeConstraintsChanged(defaultsOnly, nil) = false, want true")
	}
	if limitRangeConstraintsChanged(nil, nil) {
		t.Error("limitRangeConstraintsChanged(nil, nil) = true, want false")
	}
}
