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
	"slices"

	"gopkg.in/inf.v0"
	corev1 "k8s.io/api/core/v1"
	apiresource "k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/validation/field"
	resourcehelpers "k8s.io/component-helpers/resource"

	"sigs.k8s.io/kueue/pkg/resources"
	"sigs.k8s.io/kueue/pkg/util/resource"
)

const (
	RequestsMustNotBeAboveLimitRangeMaxMessage   = "requests must not be above the limitRange max"
	RequestsMustNotBeBelowLimitRangeMinMessage   = "requests must not be below the limitRange min"
	LimitsMustNotExceedLimitRangeMaxRatioMessage = "limit to request ratio must not be above the limitRange maxLimitRequestRatio"
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
		if resNames := resources.NewRequestsFromResourceList(cMax).GreaterKeysRL(containerRange.Max); len(resNames) > 0 {
			allErrs = append(allErrs, field.Invalid(containerPath, resNames, RequestsMustNotBeAboveLimitRangeMaxMessage))
		}
		if resNames := resources.NewRequestsFromResourceList(containerRange.Min).GreaterKeys(resources.NewRequestsFromResourceList(cMin)); len(resNames) > 0 {
			allErrs = append(allErrs, field.Invalid(containerPath, resNames, RequestsMustNotBeBelowLimitRangeMinMessage))
		}
		if resNames := keysOverLimitRequestRatio(containerRange.MaxLimitRequestRatio, res.Requests, res.Limits); len(resNames) > 0 {
			allErrs = append(allErrs, field.Invalid(containerPath, resNames, LimitsMustNotExceedLimitRangeMaxRatioMessage))
		}
	}
	return allErrs
}

// keysOverLimitRequestRatio returns the sorted resource names whose limit to
// request ratio is above the maxLimitRequestRatio. Resources without a non-zero
// request and limit are skipped.
func keysOverLimitRequestRatio(maxRatios, requests, limits corev1.ResourceList) []corev1.ResourceName {
	var resNames []corev1.ResourceName
	for name, maxRatio := range maxRatios {
		req, reqFound := requests[name]
		lim, limFound := limits[name]
		if !reqFound || !limFound || req.IsZero() || lim.IsZero() {
			continue
		}
		if limitRequestRatioExceeded(maxRatio, req, lim) {
			resNames = append(resNames, name)
		}
	}
	slices.Sort(resNames)
	return resNames
}

// limitRequestRatioExceeded reports whether lim is above req * maxRatio.
// Like the LimitRanger admission plugin in Kubernetes 1.35-1.37, it first rounds
// the request and limit up to milli units (whole units if any of the values
// would overflow), and the ratio up to milli units (whole units if it would
// overflow). The rounded values are then compared exactly instead of with
// float64 division, so the result does not depend on floating-point error.
// TODO: drop the milli rounding once the minimum supported Kubernetes version
// is 1.38, where LimitRanger compares the exact values (kubernetes/kubernetes#141348).
func limitRequestRatioExceeded(maxRatio, req, lim apiresource.Quantity) bool {
	valueScale := apiresource.Milli
	if req.Value() > apiresource.MaxMilliValue || lim.Value() > apiresource.MaxMilliValue || maxRatio.Value() > apiresource.MaxMilliValue {
		valueScale = 0
	}
	ratioScale := apiresource.Milli
	if maxRatio.Value() > apiresource.MaxMilliValue {
		ratioScale = 0
	}
	req, lim, maxRatio = roundedUp(req, valueScale), roundedUp(lim, valueScale), roundedUp(maxRatio, ratioScale)
	allowed := new(inf.Dec).Mul(req.AsDec(), maxRatio.AsDec())
	return lim.AsDec().Cmp(allowed) > 0
}

// roundedUp returns a copy of q rounded up to the given scale.
func roundedUp(q apiresource.Quantity, scale apiresource.Scale) apiresource.Quantity {
	q = q.DeepCopy()
	q.RoundUp(scale)
	return q
}

// ValidatePodSpec verifies if the provided podSpec (ps) first into the boundaries of the summary (s).
func (s Summary) ValidatePodSpec(ps *corev1.PodSpec, path *field.Path) field.ErrorList {
	var allErrs field.ErrorList
	allErrs = append(allErrs, s.validatePodSpecContainers(ps.InitContainers, path.Child("initContainers"))...)
	allErrs = append(allErrs, s.validatePodSpecContainers(ps.Containers, path.Child("containers"))...)
	if podRange, found := s[corev1.LimitTypePod]; found {
		total := resources.NewRequestsFromPodSpec(ps)
		// Like the LimitRanger admission plugin, hold the pod limits to Max as well,
		// otherwise the Workload is admitted but its Pods are rejected at creation.
		podLimits := resourcehelpers.PodLimits(&corev1.Pod{Spec: *ps}, resourcehelpers.PodResourcesOptions{ExcludeOverhead: true})
		pMax := resources.NewRequestsFromResourceList(resource.MergeResourceListKeepMax(resources.PodRequests(ps), podLimits))
		if resNames := pMax.GreaterKeysRL(podRange.Max); len(resNames) > 0 {
			allErrs = append(allErrs, field.Invalid(path, resNames, RequestsMustNotBeAboveLimitRangeMaxMessage))
		}
		if resNames := resources.NewRequestsFromResourceList(podRange.Min).GreaterKeys(total); len(resNames) > 0 {
			allErrs = append(allErrs, field.Invalid(path, resNames, RequestsMustNotBeBelowLimitRangeMinMessage))
		}
		// LimitRanger leaves overhead out of both sides of the ratio.
		podRequests := resourcehelpers.PodRequests(&corev1.Pod{Spec: *ps}, resourcehelpers.PodResourcesOptions{ExcludeOverhead: true})
		if resNames := keysOverLimitRequestRatio(podRange.MaxLimitRequestRatio, podRequests, podLimits); len(resNames) > 0 {
			allErrs = append(allErrs, field.Invalid(path, resNames, LimitsMustNotExceedLimitRangeMaxRatioMessage))
		}
	}
	return allErrs
}
