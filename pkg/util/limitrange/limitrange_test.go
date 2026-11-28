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

package limitrange

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/validation/field"

	"sigs.k8s.io/kueue/pkg/features"
	testingutil "sigs.k8s.io/kueue/pkg/util/testing"
)

var (
	ar1    = resource.MustParse("1")
	ar2    = resource.MustParse("2")
	ar500m = resource.MustParse("500m")
	ar1Gi  = resource.MustParse("1Gi")
	ar2Gi  = resource.MustParse("2Gi")
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
				*testingutil.MakeLimitRange("", "").
					WithType(corev1.LimitTypePod).
					WithValue("Default", corev1.ResourceCPU, ar2.String()).
					WithValue("DefaultRequest", corev1.ResourceCPU, ar500m.String()).
					WithValue("DefaultRequest", corev1.ResourceMemory, ar1Gi.String()).
					Obj(),
				*testingutil.MakeLimitRange("", "").
					WithType(corev1.LimitTypePod).
					WithValue("Default", corev1.ResourceMemory, ar2Gi.String()).
					WithValue("DefaultRequest", corev1.ResourceCPU, ar1.String()).
					Obj(),
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
				*testingutil.MakeLimitRange("", "").
					WithType(corev1.LimitTypePod).
					WithValue("Max", corev1.ResourceCPU, ar2.String()).
					WithValue("Min", corev1.ResourceCPU, ar500m.String()).
					WithValue("Min", corev1.ResourceMemory, ar1Gi.String()).
					WithValue("MaxLimitRequestRatio", corev1.ResourceCPU, ar2.String()).
					Obj(),
				*testingutil.MakeLimitRange("", "").
					WithType(corev1.LimitTypePod).
					WithValue("Max", corev1.ResourceMemory, ar2Gi.String()).
					WithValue("Min", corev1.ResourceCPU, ar1.String()).
					WithValue("MaxLimitRequestRatio", corev1.ResourceCPU, ar500m.String()).
					Obj(),
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
			if diff := cmp.Diff(tc.expected, result, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("Unexpected result (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestValidatePodSpec(t *testing.T) {
	podSpecPath := field.NewPath("spec").Child("podSets").Index(0).Child("template").Child("spec")
	podSpec := &corev1.PodSpec{
		Containers: []corev1.Container{
			*testingutil.MakeContainer().
				WithResourceReq(corev1.ResourceCPU, "1").
				WithResourceReq(corev1.ResourceMemory, "2Gi").
				WithResourceReq("example.com/mainContainerGpu", "2").
				Obj(),
			*testingutil.MakeContainer().
				WithResourceReq(corev1.ResourceCPU, "1.5").
				WithResourceReq("example.com/gpu", "2").
				Obj(),
		},
		InitContainers: []corev1.Container{
			*testingutil.MakeContainer().
				WithResourceReq(corev1.ResourceCPU, "4").
				WithResourceReq(corev1.ResourceMemory, "2Gi").
				Obj(),
			*testingutil.MakeContainer().
				WithResourceReq(corev1.ResourceCPU, "1.5").
				WithResourceReq("example.com/gpu", "2").
				WithResourceReq("example.com/initContainerGpu", "2").
				Obj(),
		},
		Overhead: corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("1"),
			corev1.ResourceMemory: resource.MustParse("1Gi"),
			"example.com/gpu":     resource.MustParse("1"),
		},
	}
	cases := map[string]struct {
		summary Summary
		podSpec *corev1.PodSpec
		want    field.ErrorList
	}{
		"empty": {
			summary: Summary{},
		},
		"init container over": {
			summary: Summarize(*testingutil.MakeLimitRange("", "").
				WithType(corev1.LimitTypeContainer).
				WithValue("Max", "example.com/initContainerGpu", "1").
				Obj()),
			want: field.ErrorList{
				field.Invalid(
					podSpecPath.Child("initContainers").Index(1),
					[]corev1.ResourceName{"example.com/initContainerGpu"},
					RequestsMustNotBeAboveLimitRangeMaxMessage,
				),
			},
		},
		"init container under": {
			summary: Summarize(*testingutil.MakeLimitRange("", "").
				WithType(corev1.LimitTypeContainer).
				WithValue("Min", "example.com/initContainerGpu", "3").
				Obj()),
			want: field.ErrorList{
				field.Invalid(
					podSpecPath.Child("initContainers").Index(1),
					[]corev1.ResourceName{"example.com/initContainerGpu"},
					RequestsMustNotBeBelowLimitRangeMinMessage,
				),
			},
		},
		"container over": {
			summary: Summarize(*testingutil.MakeLimitRange("", "").
				WithType(corev1.LimitTypeContainer).
				WithValue("Max", "example.com/mainContainerGpu", "1").
				Obj()),
			want: field.ErrorList{
				field.Invalid(
					podSpecPath.Child("containers").Index(0),
					[]corev1.ResourceName{"example.com/mainContainerGpu"},
					RequestsMustNotBeAboveLimitRangeMaxMessage,
				),
			},
		},
		"container under": {
			summary: Summarize(*testingutil.MakeLimitRange("", "").
				WithType(corev1.LimitTypeContainer).
				WithValue("Min", "example.com/mainContainerGpu", "3").
				Obj()),
			want: field.ErrorList{
				field.Invalid(
					podSpecPath.Child("containers").Index(0),
					[]corev1.ResourceName{"example.com/mainContainerGpu"},
					RequestsMustNotBeBelowLimitRangeMinMessage,
				),
			},
		},
		"pod over": {
			summary: Summarize(*testingutil.MakeLimitRange("", "").
				WithType(corev1.LimitTypePod).
				WithValue("Max", corev1.ResourceCPU, "4").
				Obj()),
			want: field.ErrorList{
				field.Invalid(podSpecPath, []corev1.ResourceName{corev1.ResourceCPU}, RequestsMustNotBeAboveLimitRangeMaxMessage),
			},
		},
		"pod under": {
			summary: Summarize(*testingutil.MakeLimitRange("", "").
				WithType(corev1.LimitTypePod).
				WithValue("Min", corev1.ResourceCPU, "6").
				Obj()),
			want: field.ErrorList{
				field.Invalid(podSpecPath, []corev1.ResourceName{corev1.ResourceCPU}, RequestsMustNotBeBelowLimitRangeMinMessage),
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
			want: field.ErrorList{
				field.Invalid(
					podSpecPath.Child("initContainers").Index(1),
					[]corev1.ResourceName{"example.com/initContainerGpu"},
					RequestsMustNotBeAboveLimitRangeMaxMessage,
				),
				field.Invalid(
					podSpecPath.Child("containers").Index(0),
					[]corev1.ResourceName{"example.com/mainContainerGpu"},
					RequestsMustNotBeBelowLimitRangeMinMessage,
				),
				field.Invalid(podSpecPath, []corev1.ResourceName{corev1.ResourceCPU}, RequestsMustNotBeAboveLimitRangeMaxMessage),
			},
		},
		"multiple valid": {
			summary: Summarize(
				*testingutil.MakeLimitRange("", "").
					WithType(corev1.LimitTypePod).
					WithValue("Max", corev1.ResourceCPU, "5").
					Obj(),
				*testingutil.MakeLimitRange("", "").
					WithType(corev1.LimitTypeContainer).
					WithValue("Min", "example.com/mainContainerGpu", "1").
					Obj(),
				*testingutil.MakeLimitRange("", "").
					WithType(corev1.LimitTypeContainer).
					WithValue("Max", "example.com/initContainerGpu", "2").
					Obj(),
			),
		},
		"container under with explicit zero": {
			summary: Summarize(*testingutil.MakeLimitRange("", "").
				WithType(corev1.LimitTypeContainer).
				WithValue("Min", "example.com/mainContainerGpu", "3").
				Obj()),
			podSpec: &corev1.PodSpec{
				Containers: []corev1.Container{
					*testingutil.MakeContainer().
						WithResourceReq("example.com/mainContainerGpu", "0").
						Obj(),
				},
			},
			want: field.ErrorList{
				field.Invalid(
					podSpecPath.Child("containers").Index(0),
					[]corev1.ResourceName{"example.com/mainContainerGpu"},
					RequestsMustNotBeBelowLimitRangeMinMessage,
				),
			},
		},
		"pod limits over": {
			summary: Summarize(*testingutil.MakeLimitRange("", "").
				WithType(corev1.LimitTypePod).
				WithValue("Max", corev1.ResourceCPU, "2").
				Obj()),
			podSpec: &corev1.PodSpec{
				Containers: []corev1.Container{
					*testingutil.MakeContainer().
						WithResourceReq(corev1.ResourceCPU, "500m").
						WithResourceLimit(corev1.ResourceCPU, "1500m").
						Obj(),
					*testingutil.MakeContainer().
						WithResourceReq(corev1.ResourceCPU, "500m").
						WithResourceLimit(corev1.ResourceCPU, "1500m").
						Obj(),
				},
			},
			want: field.ErrorList{
				field.Invalid(podSpecPath, []corev1.ResourceName{corev1.ResourceCPU}, RequestsMustNotBeAboveLimitRangeMaxMessage),
			},
		},
		"pod limits over with sidecar": {
			summary: Summarize(*testingutil.MakeLimitRange("", "").
				WithType(corev1.LimitTypePod).
				WithValue("Max", corev1.ResourceCPU, "2").
				Obj()),
			podSpec: &corev1.PodSpec{
				InitContainers: []corev1.Container{
					*testingutil.MakeContainer().
						AsSidecar().
						WithResourceReq(corev1.ResourceCPU, "500m").
						WithResourceLimit(corev1.ResourceCPU, "1").
						Obj(),
				},
				Containers: []corev1.Container{
					*testingutil.MakeContainer().
						WithResourceReq(corev1.ResourceCPU, "500m").
						WithResourceLimit(corev1.ResourceCPU, "1500m").
						Obj(),
				},
			},
			want: field.ErrorList{
				field.Invalid(podSpecPath, []corev1.ResourceName{corev1.ResourceCPU}, RequestsMustNotBeAboveLimitRangeMaxMessage),
			},
		},
		"pod limits within max": {
			summary: Summarize(*testingutil.MakeLimitRange("", "").
				WithType(corev1.LimitTypePod).
				WithValue("Max", corev1.ResourceCPU, "3").
				Obj()),
			podSpec: &corev1.PodSpec{
				Containers: []corev1.Container{
					*testingutil.MakeContainer().
						WithResourceReq(corev1.ResourceCPU, "500m").
						WithResourceLimit(corev1.ResourceCPU, "1500m").
						Obj(),
					*testingutil.MakeContainer().
						WithResourceReq(corev1.ResourceCPU, "500m").
						WithResourceLimit(corev1.ResourceCPU, "1500m").
						Obj(),
				},
			},
		},
		"pod limits within max exclude overhead": {
			summary: Summarize(*testingutil.MakeLimitRange("", "").
				WithType(corev1.LimitTypePod).
				WithValue("Max", corev1.ResourceCPU, "2").
				Obj()),
			podSpec: &corev1.PodSpec{
				Containers: []corev1.Container{
					*testingutil.MakeContainer().
						WithResourceReq(corev1.ResourceCPU, "500m").
						WithResourceLimit(corev1.ResourceCPU, "1500m").
						Obj(),
				},
				Overhead: corev1.ResourceList{
					corev1.ResourceCPU: resource.MustParse("1"),
				},
			},
		},
		"pod under with explicit zero": {
			summary: Summarize(*testingutil.MakeLimitRange("", "").
				WithType(corev1.LimitTypePod).
				WithValue("Min", corev1.ResourceCPU, "6").
				Obj()),
			podSpec: &corev1.PodSpec{
				Containers: []corev1.Container{
					*testingutil.MakeContainer().
						WithResourceReq(corev1.ResourceCPU, "0").
						Obj(),
				},
			},
			want: field.ErrorList{
				field.Invalid(
					podSpecPath,
					[]corev1.ResourceName{corev1.ResourceCPU},
					RequestsMustNotBeBelowLimitRangeMinMessage,
				),
			},
		},
		"container limit to request ratio over": {
			summary: Summarize(*testingutil.MakeLimitRange("", "").
				WithType(corev1.LimitTypeContainer).
				WithValue("MaxLimitRequestRatio", corev1.ResourceMemory, "2").
				WithValue("MaxLimitRequestRatio", corev1.ResourceCPU, "2").
				Obj()),
			podSpec: &corev1.PodSpec{
				Containers: []corev1.Container{
					*testingutil.MakeContainer().
						WithResourceReq(corev1.ResourceCPU, "1").
						WithResourceLimit(corev1.ResourceCPU, "4").
						WithResourceReq(corev1.ResourceMemory, "1Gi").
						WithResourceLimit(corev1.ResourceMemory, "3Gi").
						Obj(),
				},
			},
			want: field.ErrorList{
				field.Invalid(
					podSpecPath.Child("containers").Index(0),
					[]corev1.ResourceName{corev1.ResourceCPU, corev1.ResourceMemory},
					LimitsMustNotExceedLimitRangeMaxRatioMessage,
				),
			},
		},
		"init container limit to request ratio over": {
			summary: Summarize(*testingutil.MakeLimitRange("", "").
				WithType(corev1.LimitTypeContainer).
				WithValue("MaxLimitRequestRatio", corev1.ResourceCPU, "2").
				Obj()),
			podSpec: &corev1.PodSpec{
				InitContainers: []corev1.Container{
					*testingutil.MakeContainer().
						WithResourceReq(corev1.ResourceCPU, "1").
						WithResourceLimit(corev1.ResourceCPU, "3").
						Obj(),
				},
				Containers: []corev1.Container{
					*testingutil.MakeContainer().
						WithResourceReq(corev1.ResourceCPU, "1").
						WithResourceLimit(corev1.ResourceCPU, "2").
						Obj(),
				},
			},
			want: field.ErrorList{
				field.Invalid(
					podSpecPath.Child("initContainers").Index(0),
					[]corev1.ResourceName{corev1.ResourceCPU},
					LimitsMustNotExceedLimitRangeMaxRatioMessage,
				),
			},
		},
		"container limit to request ratio over fractional max": {
			summary: Summarize(*testingutil.MakeLimitRange("", "").
				WithType(corev1.LimitTypeContainer).
				WithValue("MaxLimitRequestRatio", corev1.ResourceCPU, "1500m").
				Obj()),
			podSpec: &corev1.PodSpec{
				Containers: []corev1.Container{
					*testingutil.MakeContainer().
						WithResourceReq(corev1.ResourceCPU, "1").
						WithResourceLimit(corev1.ResourceCPU, "2").
						Obj(),
				},
			},
			want: field.ErrorList{
				field.Invalid(
					podSpecPath.Child("containers").Index(0),
					[]corev1.ResourceName{corev1.ResourceCPU},
					LimitsMustNotExceedLimitRangeMaxRatioMessage,
				),
			},
		},
		"container limit to request ratio equal to max": {
			summary: Summarize(*testingutil.MakeLimitRange("", "").
				WithType(corev1.LimitTypeContainer).
				WithValue("MaxLimitRequestRatio", corev1.ResourceCPU, "2").
				Obj()),
			podSpec: &corev1.PodSpec{
				Containers: []corev1.Container{
					*testingutil.MakeContainer().
						WithResourceReq(corev1.ResourceCPU, "500m").
						WithResourceLimit(corev1.ResourceCPU, "1").
						Obj(),
				},
			},
		},
		"container limit to request ratio equal to fractional max": {
			summary: Summarize(*testingutil.MakeLimitRange("", "").
				WithType(corev1.LimitTypeContainer).
				WithValue("MaxLimitRequestRatio", corev1.ResourceCPU, "2.007").
				Obj()),
			podSpec: &corev1.PodSpec{
				Containers: []corev1.Container{
					*testingutil.MakeContainer().
						WithResourceReq(corev1.ResourceCPU, "1").
						WithResourceLimit(corev1.ResourceCPU, "2007m").
						Obj(),
				},
			},
		},
		"container limit to request ratio over sub-milli max": {
			summary: Summarize(*testingutil.MakeLimitRange("", "").
				WithType(corev1.LimitTypeContainer).
				WithValue("MaxLimitRequestRatio", corev1.ResourceCPU, "2.0001").
				Obj()),
			podSpec: &corev1.PodSpec{
				Containers: []corev1.Container{
					*testingutil.MakeContainer().
						WithResourceReq(corev1.ResourceCPU, "1").
						WithResourceLimit(corev1.ResourceCPU, "2001m").
						Obj(),
				},
			},
			want: field.ErrorList{
				field.Invalid(
					podSpecPath.Child("containers").Index(0),
					[]corev1.ResourceName{corev1.ResourceCPU},
					LimitsMustNotExceedLimitRangeMaxRatioMessage,
				),
			},
		},
		"container limit to request ratio equal to sub-milli max": {
			summary: Summarize(*testingutil.MakeLimitRange("", "").
				WithType(corev1.LimitTypeContainer).
				WithValue("MaxLimitRequestRatio", corev1.ResourceCPU, "2.0001").
				Obj()),
			podSpec: &corev1.PodSpec{
				Containers: []corev1.Container{
					*testingutil.MakeContainer().
						WithResourceReq(corev1.ResourceCPU, "1").
						WithResourceLimit(corev1.ResourceCPU, "2.0001").
						Obj(),
				},
			},
		},
		"container limit to request ratio over max with sub-milli request and limit": {
			summary: Summarize(*testingutil.MakeLimitRange("", "").
				WithType(corev1.LimitTypeContainer).
				WithValue("MaxLimitRequestRatio", corev1.ResourceCPU, "2").
				Obj()),
			podSpec: &corev1.PodSpec{
				Containers: []corev1.Container{
					*testingutil.MakeContainer().
						WithResourceReq(corev1.ResourceCPU, "300u").
						WithResourceLimit(corev1.ResourceCPU, "700u").
						Obj(),
				},
			},
			want: field.ErrorList{
				field.Invalid(
					podSpecPath.Child("containers").Index(0),
					[]corev1.ResourceName{corev1.ResourceCPU},
					LimitsMustNotExceedLimitRangeMaxRatioMessage,
				),
			},
		},
		"container limit to request ratio equal to max with sub-milli request and limit": {
			summary: Summarize(*testingutil.MakeLimitRange("", "").
				WithType(corev1.LimitTypeContainer).
				WithValue("MaxLimitRequestRatio", corev1.ResourceCPU, "2").
				Obj()),
			podSpec: &corev1.PodSpec{
				Containers: []corev1.Container{
					*testingutil.MakeContainer().
						WithResourceReq(corev1.ResourceCPU, "300u").
						WithResourceLimit(corev1.ResourceCPU, "600u").
						Obj(),
				},
			},
		},
		"container limit to request ratio over max rounded up to milli": {
			summary: Summarize(*testingutil.MakeLimitRange("", "").
				WithType(corev1.LimitTypeContainer).
				WithValue("MaxLimitRequestRatio", corev1.ResourceCPU, "2.0001").
				Obj()),
			podSpec: &corev1.PodSpec{
				Containers: []corev1.Container{
					*testingutil.MakeContainer().
						WithResourceReq(corev1.ResourceCPU, "1").
						WithResourceLimit(corev1.ResourceCPU, "2002m").
						Obj(),
				},
			},
			want: field.ErrorList{
				field.Invalid(
					podSpecPath.Child("containers").Index(0),
					[]corev1.ResourceName{corev1.ResourceCPU},
					LimitsMustNotExceedLimitRangeMaxRatioMessage,
				),
			},
		},
		"container limit to request ratio within max in whole units": {
			summary: Summarize(*testingutil.MakeLimitRange("", "").
				WithType(corev1.LimitTypeContainer).
				WithValue("MaxLimitRequestRatio", corev1.ResourceMemory, "2").
				Obj()),
			podSpec: &corev1.PodSpec{
				Containers: []corev1.Container{
					*testingutil.MakeContainer().
						WithResourceReq(corev1.ResourceMemory, "10Pi").
						WithResourceLimit(corev1.ResourceMemory, "20Pi").
						Obj(),
				},
			},
		},
		"container limit to request ratio over max in whole units": {
			summary: Summarize(*testingutil.MakeLimitRange("", "").
				WithType(corev1.LimitTypeContainer).
				WithValue("MaxLimitRequestRatio", corev1.ResourceMemory, "2").
				Obj()),
			podSpec: &corev1.PodSpec{
				Containers: []corev1.Container{
					*testingutil.MakeContainer().
						WithResourceReq(corev1.ResourceMemory, "10Pi").
						WithResourceLimit(corev1.ResourceMemory, "30Pi").
						Obj(),
				},
			},
			want: field.ErrorList{
				field.Invalid(
					podSpecPath.Child("containers").Index(0),
					[]corev1.ResourceName{corev1.ResourceMemory},
					LimitsMustNotExceedLimitRangeMaxRatioMessage,
				),
			},
		},
		"container limit to request ratio without limit": {
			summary: Summarize(*testingutil.MakeLimitRange("", "").
				WithType(corev1.LimitTypeContainer).
				WithValue("MaxLimitRequestRatio", corev1.ResourceCPU, "2").
				Obj()),
		},
		"pod limit to request ratio over": {
			summary: Summarize(*testingutil.MakeLimitRange("", "").
				WithType(corev1.LimitTypePod).
				WithValue("MaxLimitRequestRatio", corev1.ResourceCPU, "1500m").
				Obj()),
			podSpec: &corev1.PodSpec{
				Containers: []corev1.Container{
					*testingutil.MakeContainer().
						WithResourceReq(corev1.ResourceCPU, "500m").
						WithResourceLimit(corev1.ResourceCPU, "1").
						Obj(),
					*testingutil.MakeContainer().
						WithResourceReq(corev1.ResourceCPU, "500m").
						WithResourceLimit(corev1.ResourceCPU, "1").
						Obj(),
				},
			},
			want: field.ErrorList{
				field.Invalid(podSpecPath, []corev1.ResourceName{corev1.ResourceCPU}, LimitsMustNotExceedLimitRangeMaxRatioMessage),
			},
		},
		"pod limit to request ratio over exclude overhead": {
			summary: Summarize(*testingutil.MakeLimitRange("", "").
				WithType(corev1.LimitTypePod).
				WithValue("MaxLimitRequestRatio", corev1.ResourceCPU, "2").
				Obj()),
			podSpec: &corev1.PodSpec{
				Containers: []corev1.Container{
					*testingutil.MakeContainer().
						WithResourceReq(corev1.ResourceCPU, "1").
						WithResourceLimit(corev1.ResourceCPU, "4").
						Obj(),
				},
				Overhead: corev1.ResourceList{
					corev1.ResourceCPU: resource.MustParse("2"),
				},
			},
			want: field.ErrorList{
				field.Invalid(podSpecPath, []corev1.ResourceName{corev1.ResourceCPU}, LimitsMustNotExceedLimitRangeMaxRatioMessage),
			},
		},
		"pod limit to request ratio within max": {
			summary: Summarize(*testingutil.MakeLimitRange("", "").
				WithType(corev1.LimitTypePod).
				WithValue("MaxLimitRequestRatio", corev1.ResourceCPU, "3").
				Obj()),
			podSpec: &corev1.PodSpec{
				Containers: []corev1.Container{
					*testingutil.MakeContainer().
						WithResourceReq(corev1.ResourceCPU, "500m").
						WithResourceLimit(corev1.ResourceCPU, "1500m").
						Obj(),
					*testingutil.MakeContainer().
						WithResourceReq(corev1.ResourceCPU, "500m").
						WithResourceLimit(corev1.ResourceCPU, "1").
						Obj(),
				},
			},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.VectorizedResourceRequests, true)
			ps := tc.podSpec
			if ps == nil {
				ps = podSpec
			}
			result := tc.summary.ValidatePodSpec(ps, podSpecPath)
			if diff := cmp.Diff(tc.want, result); diff != "" {
				t.Errorf("Unexpected result (-want,+got):\n%s", diff)
			}
		})
	}
}
