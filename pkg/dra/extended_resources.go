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
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
	utilresource "sigs.k8s.io/kueue/pkg/util/resource"
	"sigs.k8s.io/kueue/pkg/workload"
)

// NeedsDRAReconcile returns true if the workload needs DRA processing in Reconcile.
// Fast in-memory check with no API calls. Uses a broad format-based filter
// (IsExtendedResourceName) which may over-trigger for non-DRA extended resources,
// but this only costs one extra Reconcile that finds no DeviceClass and queues normally.
//
// Note: there is no DeviceClass watcher. If a DeviceClass is created after a workload
// was marked inadmissible, requeuing depends on the next QueueInadmissibleWorkloads event.
func NeedsDRAReconcile(wl *kueue.Workload) bool {
	if !features.Enabled(features.DynamicResourceAllocation) {
		return false
	}
	if workload.HasDRA(wl) {
		return true
	}
	if !features.Enabled(features.DRAExtendedResources) {
		return false
	}
	for i := range wl.Spec.PodSets {
		ps := &wl.Spec.PodSets[i]
		for _, containers := range [][]corev1.Container{ps.Template.Spec.InitContainers, ps.Template.Spec.Containers} {
			for _, c := range containers {
				for name, qty := range c.Resources.Requests {
					if !qty.IsZero() && utilresource.IsExtendedResourceName(name) {
						return true
					}
				}
			}
		}
	}
	return false
}

// resolveContainerExtendedResources converts DRA-backed extended resources in a
// container's requests into logical quota keys. For each extended resource, it looks
// up DeviceClasses by spec.extendedResourceName. If a DeviceClass is found in
// deviceClassMappings, the mapped name is used as quota key; otherwise the
// extendedResourceName itself is used. Returns the converted resources and the set
// of original resource names that were replaced (for double-count prevention).
func resolveContainerExtendedResources(
	ctx context.Context,
	cl client.Client,
	container corev1.Container,
	containerPath *field.Path,
) (corev1.ResourceList, sets.Set[corev1.ResourceName], field.ErrorList) {
	log := ctrl.LoggerFrom(ctx)
	result := corev1.ResourceList{}
	replaced := sets.New[corev1.ResourceName]()
	var errs field.ErrorList

	for resourceName, quantity := range container.Resources.Requests {
		if quantity.IsZero() || !utilresource.IsExtendedResourceName(resourceName) {
			continue
		}

		log.V(4).Info("Checking extended resource for DRA backing", "resource", resourceName, "quantity", quantity.String())

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
			log.V(4).Info("No DeviceClass found, not a DRA-backed extended resource", "resource", resourceName)
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

		log.V(4).Info("Resolved extended resource to DRA quota key",
			"resource", resourceName, "quotaKey", quotaKey, "quantity", qty,
			"deviceClass", dcList.Items[0].Name)

		replaced.Insert(resourceName)
		result = utilresource.MergeResourceListKeepSum(result, corev1.ResourceList{
			quotaKey: *resource.NewQuantity(qty, resource.DecimalSI),
		})
	}
	return result, replaced, errs
}

// ResolveExtendedResourceQuota converts extended resource requests across all PodSets
// into DRA logical quota resources. Per PodSet, init containers are aggregated with
// max (sequential) and regular containers with sum (concurrent), then combined with max.
func ResolveExtendedResourceQuota(ctx context.Context, cl client.Client, wl *kueue.Workload) (
	map[kueue.PodSetReference]corev1.ResourceList,
	map[kueue.PodSetReference]sets.Set[corev1.ResourceName],
	field.ErrorList,
) {
	if cl == nil {
		return nil, nil, nil
	}

	log := ctrl.LoggerFrom(ctx)
	perPodSet := make(map[kueue.PodSetReference]corev1.ResourceList)
	replacedExtendedResources := make(map[kueue.PodSetReference]sets.Set[corev1.ResourceName])
	var allErrs field.ErrorList

	for i := range wl.Spec.PodSets {
		ps := &wl.Spec.PodSets[i]
		replaced := sets.New[corev1.ResourceName]()
		podSetPath := field.NewPath("spec", "podSets").Index(i).Child("template", "spec")

		// Closure captures outer `replaced` set to accumulate across init and regular containers.
		resolveContainers := func(containers []corev1.Container, pathSegment string, merge func(a, b corev1.ResourceList) corev1.ResourceList) corev1.ResourceList {
			var result corev1.ResourceList
			for j, container := range containers {
				containerPath := podSetPath.Child(pathSegment).Index(j)
				res, containerReplaced, errs := resolveContainerExtendedResources(ctx, cl, container, containerPath)
				allErrs = append(allErrs, errs...)
				replaced = replaced.Union(containerReplaced)
				result = merge(result, res)
			}
			return result
		}

		maxInitResources := resolveContainers(ps.Template.Spec.InitContainers, "initContainers", utilresource.MergeResourceListKeepMax)
		sumRegularResources := resolveContainers(ps.Template.Spec.Containers, "containers", utilresource.MergeResourceListKeepSum)

		aggregated := utilresource.MergeResourceListKeepMax(maxInitResources, sumRegularResources)
		if len(aggregated) > 0 {
			log.V(4).Info("Resolved extended resources for PodSet", "podSet", ps.Name, "resources", aggregated)
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
