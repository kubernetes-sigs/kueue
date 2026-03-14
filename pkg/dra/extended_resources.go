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

package dra

import (
	"context"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	resourceapi "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	utilresource "sigs.k8s.io/kueue/pkg/util/resource"
)

func processContainerExtendedResources(
	ctx context.Context,
	cl client.Client,
	container corev1.Container,
	containerPath *field.Path,
) (corev1.ResourceList, sets.Set[corev1.ResourceName], field.ErrorList) {
	result := corev1.ResourceList{}
	replaced := sets.New[corev1.ResourceName]()
	var errs field.ErrorList

	for resourceName, quantity := range container.Resources.Requests {
		if quantity.IsZero() || !utilresource.IsExtendedResourceName(resourceName) {
			continue
		}

		var dcList resourceapi.DeviceClassList
		if err := cl.List(ctx, &dcList, client.MatchingFields{
			"spec.extendedResourceName": string(resourceName),
		}); err != nil {
			errs = append(errs, field.InternalError(
				containerPath.Child("resources", "requests", string(resourceName)),
				fmt.Errorf("failed to list DeviceClasses for extended resource %q: %w", resourceName, err),
			))
			continue
		}

		if len(dcList.Items) == 0 {
			// No matching DeviceClass, not a DRA-backed extended resource.
			continue
		}

		qty, ok := quantity.AsInt64()
		if !ok {
			errs = append(errs, field.Invalid(
				containerPath.Child("resources", "requests", string(resourceName)),
				quantity.String(),
				"extended resource quantity must be an integer",
			))
			continue
		}

		// Determine the quota key. If the DeviceClass is also in deviceClassMappings,
		// use the mapped logical name to unify quota with the ResourceClaimTemplate path.
		// Otherwise, use the extendedResourceName directly.
		quotaKey := resourceName
		for _, dc := range dcList.Items {
			if logicalName, found := Mapper().lookup(corev1.ResourceName(dc.Name)); found {
				quotaKey = logicalName
				break
			}
		}
		replaced.Insert(resourceName)
		result = utilresource.MergeResourceListKeepSum(result, corev1.ResourceList{
			quotaKey: *resource.NewQuantity(qty, resource.DecimalSI),
		})
	}
	return result, replaced, errs
}

// GetResourceRequestsFromExtendedResources converts extended resource requests to DRA logical resources.
func GetResourceRequestsFromExtendedResources(ctx context.Context, cl client.Client, wl *kueue.Workload) (
	map[kueue.PodSetReference]corev1.ResourceList,
	map[kueue.PodSetReference]sets.Set[corev1.ResourceName],
	field.ErrorList,
) {
	if cl == nil {
		return nil, nil, nil
	}

	perPodSet := make(map[kueue.PodSetReference]corev1.ResourceList)
	replacedExtendedResources := make(map[kueue.PodSetReference]sets.Set[corev1.ResourceName])
	var allErrs field.ErrorList

	for i := range wl.Spec.PodSets {
		ps := &wl.Spec.PodSets[i]
		replaced := sets.New[corev1.ResourceName]()
		podSetPath := field.NewPath("spec", "podSets").Index(i).Child("template", "spec")

		processContainers := func(containers []corev1.Container, pathSegment string, merge func(a, b corev1.ResourceList) corev1.ResourceList) corev1.ResourceList {
			var result corev1.ResourceList
			for j, container := range containers {
				containerPath := podSetPath.Child(pathSegment).Index(j)
				res, containerReplaced, errs := processContainerExtendedResources(ctx, cl, container, containerPath)
				allErrs = append(allErrs, errs...)
				replaced = replaced.Union(containerReplaced)
				result = merge(result, res)
			}
			return result
		}

		maxInitResources := processContainers(ps.Template.Spec.InitContainers, "initContainers", utilresource.MergeResourceListKeepMax)
		sumRegularResources := processContainers(ps.Template.Spec.Containers, "containers", utilresource.MergeResourceListKeepSum)

		aggregated := utilresource.MergeResourceListKeepMax(maxInitResources, sumRegularResources)
		if len(aggregated) > 0 {
			perPodSet[ps.Name] = aggregated
		}
		if replaced.Len() > 0 {
			replacedExtendedResources[ps.Name] = replaced
		}
	}

	if len(allErrs) > 0 {
		return nil, nil, allErrs
	}
	return perPodSet, replacedExtendedResources, nil
}

// HasExtendedResourcesBackedByDRA returns true if any container requests a DRA-backed extended resource.
func HasExtendedResourcesBackedByDRA(ctx context.Context, cl client.Client, wl *kueue.Workload) (bool, error) {
	if cl == nil {
		return false, nil
	}

	for i := range wl.Spec.PodSets {
		ps := &wl.Spec.PodSets[i]
		for _, container := range append(ps.Template.Spec.InitContainers, ps.Template.Spec.Containers...) {
			for resourceName, quantity := range container.Resources.Requests {
				if !quantity.IsZero() && utilresource.IsExtendedResourceName(resourceName) {
					var dcList resourceapi.DeviceClassList
					if err := cl.List(ctx, &dcList, client.MatchingFields{
						"spec.extendedResourceName": string(resourceName),
					}); err != nil {
						return false, fmt.Errorf("failed to list DeviceClasses for extended resource %q: %w", resourceName, err)
					}
					if len(dcList.Items) > 0 {
						return true, nil
					}
				}
			}
		}
	}
	return false, nil
}
