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
	"fmt"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/validation/field"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	utilresource "sigs.k8s.io/kueue/pkg/util/resource"
)

func processContainerExtendedResources(
	cache *DeviceClassCache,
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

		deviceClassName, found := cache.GetDeviceClass(resourceName)
		if !found {
			continue
		}

		logicalResource, found := Mapper().lookup(corev1.ResourceName(deviceClassName))
		if !found {
			errs = append(errs, field.NotFound(
				containerPath.Child("resources", "requests", string(resourceName)),
				fmt.Sprintf("extended resource %q maps to DeviceClass %q which is not configured in deviceClassMappings", resourceName, deviceClassName),
			))
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

		replaced.Insert(resourceName)
		result = utilresource.MergeResourceListKeepSum(result, corev1.ResourceList{
			logicalResource: *resource.NewQuantity(qty, resource.DecimalSI),
		})
	}
	return result, replaced, errs
}

// GetResourceRequestsFromExtendedResources converts extended resource requests to DRA logical resources.
func GetResourceRequestsFromExtendedResources(wl *kueue.Workload, cache *DeviceClassCache) (
	map[kueue.PodSetReference]corev1.ResourceList,
	map[kueue.PodSetReference]sets.Set[corev1.ResourceName],
	field.ErrorList,
) {
	if cache == nil {
		return nil, nil, nil
	}

	perPodSet := make(map[kueue.PodSetReference]corev1.ResourceList)
	replacedExtendedResources := make(map[kueue.PodSetReference]sets.Set[corev1.ResourceName])
	var allErrs field.ErrorList

	for i := range wl.Spec.PodSets {
		ps := &wl.Spec.PodSets[i]
		replaced := sets.New[corev1.ResourceName]()

		var maxInitResources corev1.ResourceList
		for j, container := range ps.Template.Spec.InitContainers {
			containerPath := field.NewPath("spec", "podSets").Index(i).Child("template", "spec", "initContainers").Index(j)
			initRes, initReplaced, errs := processContainerExtendedResources(cache, container, containerPath)
			allErrs = append(allErrs, errs...)
			replaced = replaced.Union(initReplaced)
			maxInitResources = utilresource.MergeResourceListKeepMax(maxInitResources, initRes)
		}

		var sumRegularResources corev1.ResourceList
		for j, container := range ps.Template.Spec.Containers {
			containerPath := field.NewPath("spec", "podSets").Index(i).Child("template", "spec", "containers").Index(j)
			containerRes, containerReplaced, errs := processContainerExtendedResources(cache, container, containerPath)
			allErrs = append(allErrs, errs...)
			replaced = replaced.Union(containerReplaced)
			sumRegularResources = utilresource.MergeResourceListKeepSum(sumRegularResources, containerRes)
		}

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
func HasExtendedResourcesBackedByDRA(wl *kueue.Workload, cache *DeviceClassCache) bool {
	if cache == nil {
		return false
	}

	for i := range wl.Spec.PodSets {
		ps := &wl.Spec.PodSets[i]
		for _, container := range append(ps.Template.Spec.InitContainers, ps.Template.Spec.Containers...) {
			for resourceName, quantity := range container.Resources.Requests {
				if !quantity.IsZero() && utilresource.IsExtendedResourceName(resourceName) {
					if _, found := cache.GetDeviceClass(resourceName); found {
						return true
					}
				}
			}
		}
	}
	return false
}
