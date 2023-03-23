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
	"k8s.io/utils/field"

	testingutil "sigs.k8s.io/kueue/pkg/util/testing"
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

func TestTotalRequest(t *testing.T) {
	containers := []corev1.Container{
		{
			Resources: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceCPU:    apiresource.MustParse("1"),
					corev1.ResourceMemory: apiresource.MustParse("2Gi"),
				},
			},
		},
		{
			Resources: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceCPU: apiresource.MustParse("1500m"),
					"example.com/gpu":  apiresource.MustParse("2"),
				},
			},
		},
		{
			Resources: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceCPU:    apiresource.MustParse("4"),
					corev1.ResourceMemory: apiresource.MustParse("2Gi"),
				},
			},
		},
		{
			Resources: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{
					corev1.ResourceCPU: apiresource.MustParse("1500m"),
					"example.com/gpu":  apiresource.MustParse("2"),
				},
			},
		},
	}
	cases := map[string]struct {
		podSpec *corev1.PodSpec
		want    corev1.ResourceList
	}{
		"sum up main containers": {
			podSpec: &corev1.PodSpec{
				Containers: containers[:2],
			},
			want: corev1.ResourceList{
				corev1.ResourceCPU:    apiresource.MustParse("2500m"),
				corev1.ResourceMemory: apiresource.MustParse("2Gi"),
				"example.com/gpu":     apiresource.MustParse("2"),
			},
		},
		"one init wants more": {
			podSpec: &corev1.PodSpec{
				InitContainers: containers[2:],
				Containers:     containers[:2],
			},
			want: corev1.ResourceList{
				corev1.ResourceCPU:    apiresource.MustParse("4000m"),
				corev1.ResourceMemory: apiresource.MustParse("2Gi"),
				"example.com/gpu":     apiresource.MustParse("2"),
			},
		},
		"adds overhead": {
			podSpec: &corev1.PodSpec{
				InitContainers: containers[2:],
				Containers:     containers[:2],
				Overhead: corev1.ResourceList{
					corev1.ResourceCPU:    apiresource.MustParse("1"),
					corev1.ResourceMemory: apiresource.MustParse("1Gi"),
					"example.com/gpu":     apiresource.MustParse("1"),
				},
			},
			want: corev1.ResourceList{
				corev1.ResourceCPU:    apiresource.MustParse("5000m"),
				corev1.ResourceMemory: apiresource.MustParse("3Gi"),
				"example.com/gpu":     apiresource.MustParse("3"),
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			result := TotalRequests(tc.podSpec)
			if diff := cmp.Diff(tc.want, result); diff != "" {
				t.Errorf("Unexpected result (-want,+got):\n%s", diff)
			}
		})
	}
}
func TestValidate(t *testing.T) {
	podSpec := &corev1.PodSpec{
		Containers: []corev1.Container{
			{
				Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:             apiresource.MustParse("1"),
						corev1.ResourceMemory:          apiresource.MustParse("2Gi"),
						"example.com/mainContainerGpu": apiresource.MustParse("2"),
					},
				},
			},
			{
				Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceCPU: apiresource.MustParse("1500m"),
						"example.com/gpu":  apiresource.MustParse("2"),
					},
				},
			},
		},
		InitContainers: []corev1.Container{
			{
				Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:    apiresource.MustParse("4"),
						corev1.ResourceMemory: apiresource.MustParse("2Gi"),
					},
				},
			},
			{
				Resources: corev1.ResourceRequirements{
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:             apiresource.MustParse("1500m"),
						"example.com/gpu":              apiresource.MustParse("2"),
						"example.com/initContainerGpu": apiresource.MustParse("2"),
					},
				},
			},
		},
		Overhead: corev1.ResourceList{
			corev1.ResourceCPU:    apiresource.MustParse("1"),
			corev1.ResourceMemory: apiresource.MustParse("1Gi"),
			"example.com/gpu":     apiresource.MustParse("1"),
		},
	}
	cases := map[string]struct {
		summary Summary
		want    []string
	}{
		"empty": {
			summary: Summary{},
			want:    []string{},
		},
		"init container over": {
			summary: Summarize(*testingutil.MakeLimitRange("", "").
				WithType(corev1.LimitTypeContainer).
				WithValue("Max", "example.com/initContainerGpu", "1").
				Obj()),
			want: []string{
				violateMaxMessage(field.NewPath("testPodSet", "initContainers").Index(1), "example.com/initContainerGpu"),
			},
		},
		"init container under": {
			summary: Summarize(*testingutil.MakeLimitRange("", "").
				WithType(corev1.LimitTypeContainer).
				WithValue("Min", "example.com/initContainerGpu", "3").
				Obj()),
			want: []string{
				violateMinMessage(field.NewPath("testPodSet", "initContainers").Index(1), "example.com/initContainerGpu"),
			},
		},
		"container over": {
			summary: Summarize(*testingutil.MakeLimitRange("", "").
				WithType(corev1.LimitTypeContainer).
				WithValue("Max", "example.com/mainContainerGpu", "1").
				Obj()),
			want: []string{
				violateMaxMessage(field.NewPath("testPodSet", "containers").Index(0), "example.com/mainContainerGpu"),
			},
		},
		"container under": {
			summary: Summarize(*testingutil.MakeLimitRange("", "").
				WithType(corev1.LimitTypeContainer).
				WithValue("Min", "example.com/mainContainerGpu", "3").
				Obj()),
			want: []string{
				violateMinMessage(field.NewPath("testPodSet", "containers").Index(0), "example.com/mainContainerGpu"),
			},
		},
		"pod over": {
			summary: Summarize(*testingutil.MakeLimitRange("", "").
				WithType(corev1.LimitTypePod).
				WithValue("Max", corev1.ResourceCPU, "4").
				Obj()),
			want: []string{
				violateMaxMessage(field.NewPath("testPodSet"), string(corev1.ResourceCPU)),
			},
		},
		"pod under": {
			summary: Summarize(*testingutil.MakeLimitRange("", "").
				WithType(corev1.LimitTypePod).
				WithValue("Min", corev1.ResourceCPU, "6").
				Obj()),
			want: []string{
				violateMinMessage(field.NewPath("testPodSet"), string(corev1.ResourceCPU)),
			},
		},
		"multiple": {
			summary: Summarize(
				*testingutil.MakeLimitRange("", "").
					WithType(corev1.LimitTypePod).
					WithValue("Max", corev1.ResourceCPU, "4").
					Obj(),
				*testingutil.MakeLimitRange("", "").
					WithType(corev1.LimitTypeContainer).
					WithValue("Min", "example.com/mainContainerGpu", "3").
					Obj(),
				*testingutil.MakeLimitRange("", "").
					WithType(corev1.LimitTypeContainer).
					WithValue("Max", "example.com/initContainerGpu", "1").
					Obj(),
			),
			want: []string{
				violateMaxMessage(field.NewPath("testPodSet", "initContainers").Index(1), "example.com/initContainerGpu"),
				violateMinMessage(field.NewPath("testPodSet", "containers").Index(0), "example.com/mainContainerGpu"),
				violateMaxMessage(field.NewPath("testPodSet"), string(corev1.ResourceCPU)),
			},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			result := tc.summary.ValidatePodSpec(podSpec, field.NewPath("testPodSet"))
			if diff := cmp.Diff(tc.want, result); diff != "" {
				t.Errorf("Unexpected result (-want,+got):\n%s", diff)
			}
		})
	}
}
