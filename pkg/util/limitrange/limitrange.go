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
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/validation/field"
	resourcehelpers "k8s.io/component-helpers/resource"

	"sigs.k8s.io/kueue/pkg/util/resource"
)

const (
	RequestsMustNotBeAboveLimitRangeMaxMessage = "requests must not be above the limitRange max"
	RequestsMustNotBeBelowLimitRangeMinMessage = "requests must not be below the limitRange min"
)

type Summary map[corev1.LimitType]corev1.LimitRangeItem

// Summarize summarizes the provides ranges by:
// 1. keeping the lowest values for Max and MaxLimitReqestRatio limits
// 2. keeping the highest values for Min limits
// 3. keeping the first encountered values for Default and DefaultRequests
func Summarize(ranges ...corev1.LimitRange) Summary {
	ret := make(Summary, 3)
	for i := range ranges {
		for _, item := range ranges[i].Spec.Limits {
			ret[item.Type] = addToSummary(ret[item.Type], item)
		}
	}
	return ret
}

func addToSummary(summary, item corev1.LimitRangeItem) corev1.LimitRangeItem {
	summary.Max = resource.MergeResourceListKeepMin(summary.Max, item.Max)
	summary.Min = resource.MergeResourceListKeepMax(summary.Min, item.Min)

	summary.Default = resource.MergeResourceListKeepFirst(summary.Default, item.Default)
	summary.DefaultRequest = resource.MergeResourceListKeepFirst(summary.DefaultRequest, item.DefaultRequest)

	summary.MaxLimitRequestRatio = resource.MergeResourceListKeepMin(summary.MaxLimitRequestRatio, item.MaxLimitRequestRatio)
	return summary
}

func (s Summary) validatePodSpecContainers(containers []corev1.Container, path *field.Path) field.ErrorList {
	containerRange, found := s[corev1.LimitTypeContainer]
	if !found {
		return nil
	}
	var allErrs field.ErrorList
	for i := range containers {
		containerPath := path.Index(i)
		res := &containers[i].Resources
		cMin := resource.MergeResourceListKeepMin(res.Requests, res.Limits)
		cMax := resource.MergeResourceListKeepMax(res.Requests, res.Limits)
		if resNames := resource.GetGreaterKeys(cMax, containerRange.Max); len(resNames) > 0 {
			allErrs = append(allErrs, field.Invalid(containerPath, resNames, RequestsMustNotBeAboveLimitRangeMaxMessage))
		}
		if resNames := resource.GetGreaterKeys(containerRange.Min, cMin); len(resNames) > 0 {
			allErrs = append(allErrs, field.Invalid(containerPath, resNames, RequestsMustNotBeBelowLimitRangeMinMessage))
		}
	}
	return allErrs
}

// ValidatePodSpec verifies if the provided podSpec (ps) first into the boundaries of the summary (s).
func (s Summary) ValidatePodSpec(ps *corev1.PodSpec, path *field.Path) field.ErrorList {
	var allErrs field.ErrorList
	allErrs = append(allErrs, s.validatePodSpecContainers(ps.InitContainers, path.Child("initContainers"))...)
	allErrs = append(allErrs, s.validatePodSpecContainers(ps.Containers, path.Child("containers"))...)
	if podRange, found := s[corev1.LimitTypePod]; found {
		total := resourcehelpers.PodRequests(&corev1.Pod{Spec: *ps}, resourcehelpers.PodResourcesOptions{})
		if resNames := resource.GetGreaterKeys(total, podRange.Max); len(resNames) > 0 {
			allErrs = append(allErrs, field.Invalid(path, resNames, RequestsMustNotBeAboveLimitRangeMaxMessage))
		}
		if resNames := resource.GetGreaterKeys(podRange.Min, total); len(resNames) > 0 {
			allErrs = append(allErrs, field.Invalid(path, resNames, RequestsMustNotBeBelowLimitRangeMinMessage))
		}
	}
	return allErrs
}
