/*
Copyright 2023 The Kubernetes Authors.

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

package limitrange

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
	apiresource "k8s.io/apimachinery/pkg/api/resource"
)

var (
	ar1    = apiresource.MustParse("1")
	ar2    = apiresource.MustParse("2")
	ar500m = apiresource.MustParse("500m")
	ar1Gi  = apiresource.MustParse("1Gi")
	ar2Gi  = apiresource.MustParse("2Gi")
)

func TestSummarize(t *testing.T) {

	cases := map[string]struct {
		ranges   []corev1.LimitRange
		expected Summary
	}{
		"empty": {
			ranges:   []corev1.LimitRange{},
			expected: map[corev1.LimitType]corev1.LimitRangeItem{},
		},
		"podDefaults": {
			ranges: []corev1.LimitRange{
				{
					Spec: corev1.LimitRangeSpec{
						Limits: []corev1.LimitRangeItem{
							{
								Type: corev1.LimitTypePod,
								Default: corev1.ResourceList{
									corev1.ResourceCPU: ar2,
								},
								DefaultRequest: corev1.ResourceList{
									corev1.ResourceCPU:    ar500m,
									corev1.ResourceMemory: ar1Gi,
								},
							},
						},
					},
				},
				{
					Spec: corev1.LimitRangeSpec{
						Limits: []corev1.LimitRangeItem{
							{
								Type: corev1.LimitTypePod,
								Default: corev1.ResourceList{
									corev1.ResourceMemory: ar2Gi,
								},
								DefaultRequest: corev1.ResourceList{
									corev1.ResourceCPU: ar1,
								},
							},
						},
					},
				},
			},
			expected: map[corev1.LimitType]corev1.LimitRangeItem{
				corev1.LimitTypePod: {
					Default: corev1.ResourceList{
						corev1.ResourceCPU:    ar2,
						corev1.ResourceMemory: ar2Gi,
					},
					DefaultRequest: corev1.ResourceList{
						corev1.ResourceCPU:    ar500m,
						corev1.ResourceMemory: ar1Gi,
					},
				},
			},
		},
		"limits": {
			ranges: []corev1.LimitRange{
				{
					Spec: corev1.LimitRangeSpec{
						Limits: []corev1.LimitRangeItem{
							{
								Type: corev1.LimitTypePod,
								Max: corev1.ResourceList{
									corev1.ResourceCPU: ar2,
								},
								Min: corev1.ResourceList{
									corev1.ResourceCPU:    ar500m,
									corev1.ResourceMemory: ar1Gi,
								},
								MaxLimitRequestRatio: corev1.ResourceList{
									corev1.ResourceCPU: ar2,
								},
							},
						},
					},
				},
				{
					Spec: corev1.LimitRangeSpec{
						Limits: []corev1.LimitRangeItem{
							{
								Type: corev1.LimitTypePod,
								Max: corev1.ResourceList{
									corev1.ResourceMemory: ar2Gi,
								},
								Min: corev1.ResourceList{
									corev1.ResourceCPU: ar1,
								},
								MaxLimitRequestRatio: corev1.ResourceList{
									corev1.ResourceCPU: ar500m,
								},
							},
						},
					},
				},
			},
			expected: Summary{
				corev1.LimitTypePod: {
					Max: corev1.ResourceList{
						corev1.ResourceCPU:    ar2,
						corev1.ResourceMemory: ar2Gi,
					},
					Min: corev1.ResourceList{
						corev1.ResourceCPU:    ar1,
						corev1.ResourceMemory: ar1Gi,
					},
					MaxLimitRequestRatio: corev1.ResourceList{
						corev1.ResourceCPU: ar500m,
					},
				},
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			result := Summarize(tc.ranges...)
			if diff := cmp.Diff(tc.expected, result); diff != "" {
				t.Errorf("Unexpected result (-want,+got):\n%s", diff)
			}
		})
	}
}
