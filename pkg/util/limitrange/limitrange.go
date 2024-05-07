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
	"fmt"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/utils/field"

	"sigs.k8s.io/kueue/pkg/util/resource"
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

func violateMaxMessage(path *field.Path, keys ...string) string {
	return fmt.Sprintf("the requests of %s[%s] exceeds the limits", path.String(), strings.Join(keys, ", "))
}
func violateMinMessage(path *field.Path, keys ...string) string {
	return fmt.Sprintf("the requests of %s[%s] are less than the limits", path.String(), strings.Join(keys, ", "))
}

func (s Summary) validatePodSpecContainers(containers []corev1.Container, path *field.Path) []string {
	containerRange, found := s[corev1.LimitTypeContainer]
	if !found {
		return nil
	}
	reasons := []string{}
	for i := range containers {
		res := &containers[i].Resources
		cMin := resource.MergeResourceListKeepMin(res.Requests, res.Limits)
		cMax := resource.MergeResourceListKeepMax(res.Requests, res.Limits)
		if list := resource.GetGreaterKeys(cMax, containerRange.Max); len(list) > 0 {
			reasons = append(reasons, violateMaxMessage(path.Index(i), list...))
		}
		if list := resource.GetGreaterKeys(containerRange.Min, cMin); len(list) > 0 {
			reasons = append(reasons, violateMinMessage(path.Index(i), list...))
		}
	}
	return reasons
}

// TotalRequests computes the total resource requests of a pod.
//
//	total = Max( Max(each InitContainerUse) , Sum(SidecarContainers) + Sum(Containers) ) + pod overhead
//
// where:
//
//	InitContainerUse(i) = Sum(SidecarContainers with index < i) + InitContainer(i)
func TotalRequests(ps *corev1.PodSpec) corev1.ResourceList {
	sumContainers := calculateMainContainersResources(ps.Containers)
	maxInitContainers := calculateInitContainersResources(ps.InitContainers)
	sumSidecarContainers := calculateSidecarContainersResources(ps.InitContainers)

	total := resource.MergeResourceListKeepMax(
		maxInitContainers,
		resource.MergeResourceListKeepSum(sumSidecarContainers, sumContainers),
	)
	// add the overhead
	total = resource.MergeResourceListKeepSum(total, ps.Overhead)
	return total
}

func calculateMainContainersResources(containers []corev1.Container) corev1.ResourceList {
	total := corev1.ResourceList{}
	for i := range containers {
		total = resource.MergeResourceListKeepSum(total, containers[i].Resources.Requests)
	}
	return total
}

func calculateInitContainersResources(initContainers []corev1.Container) corev1.ResourceList {
	maxInitContainerUsage := corev1.ResourceList{}
	sidecarRunningUsage := corev1.ResourceList{}
	for i := range initContainers {
		if isSidecarContainer(initContainers[i]) {
			sidecarRunningUsage = resource.MergeResourceListKeepSum(sidecarRunningUsage, initContainers[i].Resources.Requests)
		} else {
			initContainerUse := resource.MergeResourceListKeepSum(initContainers[i].Resources.Requests, sidecarRunningUsage)
			maxInitContainerUsage = resource.MergeResourceListKeepMax(maxInitContainerUsage, initContainerUse)
		}
	}
	return maxInitContainerUsage
}

func calculateSidecarContainersResources(initContainers []corev1.Container) corev1.ResourceList {
	total := corev1.ResourceList{}
	for i := range initContainers {
		if isSidecarContainer(initContainers[i]) {
			total = resource.MergeResourceListKeepSum(total, initContainers[i].Resources.Requests)
		}
	}
	return total
}

func isSidecarContainer(container corev1.Container) bool {
	return container.RestartPolicy != nil && *container.RestartPolicy == corev1.ContainerRestartPolicyAlways
}

// ValidatePodSpec verifies if the provided podSpec (ps) first into the boundaries of the summary (s).
func (s Summary) ValidatePodSpec(ps *corev1.PodSpec, path *field.Path) []string {
	reasons := []string{}
	reasons = append(reasons, s.validatePodSpecContainers(ps.InitContainers, path.Child("initContainers"))...)
	reasons = append(reasons, s.validatePodSpecContainers(ps.Containers, path.Child("containers"))...)
	if containerRange, found := s[corev1.LimitTypePod]; found {
		total := TotalRequests(ps)
		if list := resource.GetGreaterKeys(total, containerRange.Max); len(list) > 0 {
			reasons = append(reasons, violateMaxMessage(path, list...))
		}
		if list := resource.GetGreaterKeys(containerRange.Min, total); len(list) > 0 {
			reasons = append(reasons, violateMinMessage(path, list...))
		}
	}
	return reasons
}
