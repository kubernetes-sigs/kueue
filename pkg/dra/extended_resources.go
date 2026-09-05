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
	resourcehelpers "k8s.io/component-helpers/resource"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
	utilresource "sigs.k8s.io/kueue/pkg/util/resource"
	"sigs.k8s.io/kueue/pkg/workload"
)

// NeedsDRAReconcile returns true if the workload needs DRA processing in Reconcile.
// For extended resources, checks the provided cache to confirm the resource
// is backed by a DeviceClass before triggering DRA reconciliation.
func NeedsDRAReconcile(wl *kueue.Workload, erCache *ExtendedResourceCache) bool {
	if workload.IsOnHold(wl) {
		return false
	}
	if !features.Enabled(features.KueueDRAIntegration) {
		return features.Enabled(features.KueueDRARejectWorkloadsWhenDRADisabled) && workload.HasDRA(wl)
	}
	if workload.HasDRA(wl) {
		return true
	}
	// erCache is always set when the gate is enabled; nil only occurs in tests
	// that don't configure the full DRA stack.
	if !features.Enabled(features.KueueDRAIntegrationExtendedResource) || erCache == nil {
		return false
	}
	for i := range wl.Spec.PodSets {
		ps := &wl.Spec.PodSets[i]
		for _, containers := range [][]corev1.Container{ps.Template.Spec.InitContainers, ps.Template.Spec.Containers} {
			for _, c := range containers {
				for name, qty := range c.Resources.Requests {
					if !qty.IsZero() && utilresource.IsExtendedResourceName(name) && erCache.Has(name) {
						return true
					}
				}
			}
		}
	}
	return false
}

// selectedDeviceClass returns the DeviceClass Kubernetes uses when more than one
// declares the same extendedResourceName. Quoting the field's documentation:
//
//	It should be unique among all the device classes in a cluster. If two device
//	classes have the same name, then the class created later is picked to satisfy
//	a pod's extended resource requests. If two classes are created at the same
//	time, then the name of the class lexicographically sorted first is picked.
//
// Ties are the common case, since a creation timestamp carries seconds. items
// is non-empty and already filtered to one extendedResourceName.
func selectedDeviceClass(items []resourceapi.DeviceClass) *resourceapi.DeviceClass {
	selected := &items[0]
	for i := range items[1:] {
		candidate := &items[i+1]
		switch candidate.CreationTimestamp.Compare(selected.CreationTimestamp.Time) {
		case 1:
			selected = candidate
		case 0:
			if candidate.Name < selected.Name {
				selected = candidate
			}
		}
	}
	return selected
}

// extendedResourceRequests extracts a container's positive extended resource requests,
// keyed by their original (unmapped) resource name. A zero or negative quantity is
// dropped here rather than merged into a logical quota key later, since a negative
// value could otherwise cancel out part of another resource's charge under the same
// key (e.g. a ResourceClaimTemplate). Quantities are not validated here:
// the integer-only rule only applies to resources that turn out to be DRA-backed, which
// isn't known until a DeviceClass is resolved for the name later in
// ResolveExtendedResourceQuota. Validating here would reject fractional requests for
// extended resources that aren't DRA-backed at all, which the standard (non-DRA) quota
// path accepts.
func extendedResourceRequests(container corev1.Container) corev1.ResourceList {
	result := corev1.ResourceList{}

	for resourceName, quantity := range container.Resources.Requests {
		if quantity.Sign() <= 0 || !utilresource.IsExtendedResourceName(resourceName) {
			continue
		}
		result[resourceName] = quantity
	}
	return result
}

// resolveQuotaKey looks up the DeviceClasses backing resourceName by
// spec.extendedResourceName, selects the one the scheduler would allocate from, and
// returns that class's deviceClassMappings entry as the quota key; otherwise
// resourceName itself is used. Returns "" if resourceName is not DRA-backed (no
// matching DeviceClass).
func resolveQuotaKey(
	ctx context.Context,
	cl client.Client,
	mapper *ResourceMapper,
	resourceName corev1.ResourceName,
	path *field.Path,
) (corev1.ResourceName, field.ErrorList) {
	log := ctrl.LoggerFrom(ctx)
	log.V(4).Info("Checking extended resource for DRA backing", "resource", resourceName)

	var dcList resourceapi.DeviceClassList
	if err := cl.List(ctx, &dcList, client.MatchingFields{
		"spec.extendedResourceName": string(resourceName),
	}); err != nil {
		return "", field.ErrorList{field.InternalError(
			path.Child("resources", "requests", string(resourceName)),
			fmt.Errorf("failed to list DeviceClasses for extended resource %q: %w", resourceName, err),
		)}
	}

	if len(dcList.Items) == 0 {
		log.V(4).Info("No DeviceClass found, not a DRA-backed extended resource", "resource", resourceName)
		return "", nil
	}

	// The class the scheduler will allocate from, not whichever List returned first.
	selected := selectedDeviceClass(dcList.Items)

	// Determine the quota key. If the DeviceClass is also in deviceClassMappings,
	// use the mapped logical name to unify quota with the ResourceClaimTemplate path.
	// Otherwise, use the extendedResourceName directly.
	quotaKey := resourceName
	var errs field.ErrorList
	if logicalName, found := mapper.Lookup(corev1.ResourceName(selected.Name)); found {
		quotaKey = logicalName
		if features.Enabled(features.KueueDRAIntegrationPartitionableDevices) && len(mapper.getCounterConfigs(corev1.ResourceName(selected.Name))) > 0 {
			errs = append(errs, field.Invalid(
				path,
				resourceName,
				fmt.Sprintf(
					"extended resource %s resolves to DeviceClass %s with counters configured;"+
						" use ResourceClaimTemplates with CEL selectors for counter-based quota",
					resourceName, selected.Name,
				),
			))
		} else if features.Enabled(features.KueueDRAIntegrationConsumableCapacity) && len(mapper.getCapacityConfigs(corev1.ResourceName(selected.Name))) > 0 {
			errs = append(errs, field.Invalid(
				path,
				resourceName,
				fmt.Sprintf(
					"extended resource %s resolves to DeviceClass %s with capacity sources configured;"+
						" use ResourceClaimTemplates with capacity.requests for capacity-based quota",
					resourceName, selected.Name,
				),
			))
		}
	}
	if len(errs) > 0 {
		return "", errs
	}

	log.V(4).Info("Resolved extended resource to DRA quota key",
		"resource", resourceName, "quotaKey", quotaKey, "deviceClass", selected.Name)
	return quotaKey, nil
}

// containerExtendedResourceRequests pairs a container's positive extended resource
// requests, keyed by original (unmapped) resource name, with the field path used to
// report errors against that container.
type containerExtendedResourceRequests struct {
	path      *field.Path
	resources corev1.ResourceList
	// Carried so the total can be taken the way the Pod's own is: a restartable
	// init container runs alongside the rest and adds to them.
	restartPolicy *corev1.ContainerRestartPolicy
}

// ResolveExtendedResourceQuota converts extended resource requests across all PodSets
// into DRA logical quota resources. Per PodSet each original name is aggregated with
// `resourcehelpers.PodRequests` (overhead excluded; sidecars add to the app-container
// total, they are not maxed as ordinary inits), and its quota key is resolved from that
// name's own total, so two names sharing a key cannot collapse into each other.
func ResolveExtendedResourceQuota(ctx context.Context, cl client.Client, mapper *ResourceMapper, wl *kueue.Workload) (
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
		podSetPath := field.NewPath("spec", "podSets").Index(i).Child("template", "spec")

		collect := func(containers []corev1.Container, pathSegment string) []containerExtendedResourceRequests {
			var entries []containerExtendedResourceRequests
			for j, container := range containers {
				res := extendedResourceRequests(container)
				if len(res) == 0 {
					continue
				}
				entries = append(entries, containerExtendedResourceRequests{
					path:          podSetPath.Child(pathSegment).Index(j),
					resources:     res,
					restartPolicy: container.RestartPolicy,
				})
			}
			return entries
		}

		initEntries := collect(ps.Template.Spec.InitContainers, "initContainers")
		regularEntries := collect(ps.Template.Spec.Containers, "containers")

		// The field path of the first container an original resource name is seen in,
		// for error reporting once that name is resolved below.
		firstPath := map[corev1.ResourceName]*field.Path{}
		charged := func(entries []containerExtendedResourceRequests) []corev1.Container {
			out := make([]corev1.Container, 0, len(entries))
			for _, e := range entries {
				for name := range e.resources {
					if _, ok := firstPath[name]; !ok {
						firstPath[name] = e.path
					}
				}
				out = append(out, corev1.Container{
					RestartPolicy: e.restartPolicy,
					Resources:     corev1.ResourceRequirements{Requests: e.resources},
				})
			}
			return out
		}
		initCharged := charged(initEntries)
		regularCharged := charged(regularEntries)
		// PodRequests adds a sidecar to the regular containers rather than maxing it against them.
		podRequests := resourcehelpers.PodRequests(
			&corev1.Pod{Spec: corev1.PodSpec{InitContainers: initCharged, Containers: regularCharged}},
			resourcehelpers.PodResourcesOptions{ExcludeOverhead: true})

		aggregated := corev1.ResourceList{}
		replaced := sets.New[corev1.ResourceName]()
		for resourceName, quantity := range podRequests {
			quotaKey, errs := resolveQuotaKey(ctx, cl, mapper, resourceName, firstPath[resourceName])
			if len(errs) > 0 {
				allErrs = append(allErrs, errs...)
				continue
			}
			if quotaKey == "" {
				continue
			}

			// resourceName is confirmed DRA-backed: now hold it to the
			// integer-only rule, checked per container rather than on the
			// aggregate above, so two invalid fractional requests (e.g. two
			// 500m requests summing to a valid 1) can't hide each other.
			var intErrs field.ErrorList
			for _, entries := range [][]containerExtendedResourceRequests{initEntries, regularEntries} {
				for _, e := range entries {
					qty, ok := e.resources[resourceName]
					if !ok {
						continue
					}
					if _, ok := qty.AsInt64(); !ok {
						intErrs = append(intErrs, field.Invalid(
							e.path.Child("resources", "requests", string(resourceName)),
							qty.String(),
							"extended resource quantity must be an integer",
						))
					}
				}
			}
			if len(intErrs) > 0 {
				allErrs = append(allErrs, intErrs...)
				continue
			}

			// Each container's quantity passed the integer check above, but their
			// sum can still overflow int64 (e.g. two containers requesting 9e18
			// each), so the aggregate needs its own check rather than assuming ok.
			intQty, ok := quantity.AsInt64()
			if !ok {
				allErrs = append(allErrs, field.Invalid(
					firstPath[resourceName].Child("resources", "requests", string(resourceName)),
					quantity.String(),
					"total extended resource quantity overflows int64",
				))
				continue
			}
			replaced.Insert(resourceName)
			aggregated = utilresource.MergeResourceListKeepSum(aggregated, corev1.ResourceList{quotaKey: *resource.NewQuantity(intQty, resource.DecimalSI)})
		}

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
