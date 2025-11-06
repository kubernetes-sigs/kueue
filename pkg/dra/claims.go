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
	"strconv"

	corev1 "k8s.io/api/core/v1"
	resourcev1 "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/resources"
	utilresource "sigs.k8s.io/kueue/pkg/util/resource"
)

// countDevicesPerClass returns a resources.Requests representing the
// total number of devices requested for each DeviceClass inside the provided
// ResourceClaimSpec. It validates that only supported DRA features are used
// and returns field errors if unsupported features are detected.
func countDevicesPerClass(claimSpec *resourcev1.ResourceClaimSpec) (resources.Requests, field.ErrorList) {
	out := resources.Requests{}
	if claimSpec == nil {
		return out, nil
	}

	var allErrs field.ErrorList

	// Check for unsupported device constraints
	if len(claimSpec.Devices.Constraints) > 0 {
		allErrs = append(allErrs, field.Invalid(field.NewPath("devices", "constraints"), nil, "device constraints (MatchAttribute) are not supported"))
		return nil, allErrs
	}

	// Check for unsupported device config
	if len(claimSpec.Devices.Config) > 0 {
		allErrs = append(allErrs, field.Invalid(field.NewPath("devices", "config"), nil, "device config is not supported"))
		return nil, allErrs
	}

	for i, req := range claimSpec.Devices.Requests {
		// v1 DeviceRequest has Exactly or FirstAvailable. For Step 1, we
		// preserve existing semantics by only supporting Exactly with Count.
		var dcName string
		var q int64
		if req.FirstAvailable != nil {
			allErrs = append(allErrs, field.Invalid(field.NewPath("devices", "requests").Index(i), nil, "FirstAvailable device selection is not supported"))
			return nil, allErrs
		}

		switch {
		case len(req.Exactly.Selectors) > 0:
			allErrs = append(allErrs, field.Invalid(field.NewPath("devices", "requests").Index(i).Child("exactly", "selectors"), nil, "CEL selectors are not supported"))
			return nil, allErrs
		case req.Exactly.AdminAccess != nil && *req.Exactly.AdminAccess:
			allErrs = append(allErrs, field.Invalid(field.NewPath("devices", "requests").Index(i).Child("exactly", "adminAccess"), nil, "AdminAccess is not supported"))
			return nil, allErrs
		case req.Exactly.AllocationMode == resourcev1.DeviceAllocationModeAll:
			allErrs = append(allErrs, field.Invalid(field.NewPath("devices", "requests").Index(i).Child("exactly", "allocationMode"), resourcev1.DeviceAllocationModeAll, "AllocationMode 'All' is not supported"))
			return nil, allErrs
		case req.Exactly.AllocationMode == resourcev1.DeviceAllocationModeExactCount:
			dcName = req.Exactly.DeviceClassName
			q = req.Exactly.Count
		default:
			allErrs = append(allErrs, field.Invalid(field.NewPath("devices", "requests").Index(i).Child("exactly", "allocationMode"), req.Exactly.AllocationMode, fmt.Sprintf("unsupported allocation mode: %s", req.Exactly.AllocationMode)))
			return nil, allErrs
		}

		dc := corev1.ResourceName(dcName)
		if dc == "" {
			continue
		}
		out[dc] += q
	}
	return out, nil
}

// getClaimSpec resolves the ResourceClaim(Template) referenced by the PodResourceClaim
// and returns its *ResourceClaimSpec. A nil spec and nil error mean the reference is
// empty (both name pointers are nil) and should be skipped.
func getClaimSpec(ctx context.Context, cl client.Client, namespace string, prc corev1.PodResourceClaim) (*resourcev1.ResourceClaimSpec, error) {
	switch {
	case prc.ResourceClaimTemplateName != nil:
		var tmpl resourcev1.ResourceClaimTemplate
		if err := cl.Get(ctx, client.ObjectKey{Namespace: namespace, Name: *prc.ResourceClaimTemplateName}, &tmpl); err != nil {
			return nil, err
		}
		return &tmpl.Spec.Spec, nil
	case prc.ResourceClaimName != nil:
		var claim resourcev1.ResourceClaim
		if err := cl.Get(ctx, client.ObjectKey{Namespace: namespace, Name: *prc.ResourceClaimName}, &claim); err != nil {
			return nil, err
		}
		return &claim.Spec, nil
	default:
		return nil, nil
	}
}

// GetResourceRequestsForResourceClaimTemplates walks all ResourceClaimTemplates referenced by each PodSet of the Workload,
// converts DeviceClass counts into logical resources using the provided lookup function and
// returns the aggregated quantities per PodSet.
//
// If at least one DeviceClass is not present in the DRA configuration or if unsupported DRA
// features are detected, the function returns field errors.
func GetResourceRequestsForResourceClaimTemplates(
	ctx context.Context,
	cl client.Client,
	wl *kueue.Workload) (map[kueue.PodSetReference]corev1.ResourceList, field.ErrorList) {
	perPodSet := make(map[kueue.PodSetReference]corev1.ResourceList)
	var allErrs field.ErrorList

	for i := range wl.Spec.PodSets {
		ps := &wl.Spec.PodSets[i]
		aggregated := corev1.ResourceList{}

		for j, prc := range ps.Template.Spec.ResourceClaims {
			if prc.ResourceClaimTemplateName == nil {
				continue
			}
			spec, err := getClaimSpec(ctx, cl, wl.Namespace, prc)
			if err != nil {
				allErrs = append(allErrs, field.InternalError(
					field.NewPath("spec", "podSets").Index(i).Child("template", "spec", "resourceClaims").Index(j),
					fmt.Errorf("failed to get claim spec for ResourceClaimTemplate %s in podset %s: %w", *prc.ResourceClaimTemplateName, ps.Name, err),
				))
				return nil, allErrs
			}
			if spec == nil {
				continue
			}

			deviceCounts, fieldErrs := countDevicesPerClass(spec)
			if len(fieldErrs) > 0 {
				// Prefix the field paths with the podset and resource claim context
				for _, fieldErr := range fieldErrs {
					allErrs = append(allErrs, &field.Error{
						Type:     fieldErr.Type,
						Field:    field.NewPath("spec", "podSets").Index(i).Child("template", "spec", "resourceClaims").Index(j).String() + "." + fieldErr.Field,
						BadValue: fieldErr.BadValue,
						Detail:   fmt.Sprintf("ResourceClaimTemplate %s: %s", *prc.ResourceClaimTemplateName, fieldErr.Detail),
					})
				}
				return nil, allErrs
			}

			for dc, qty := range deviceCounts {
				logical, found := Mapper().lookup(dc)
				if !found {
					allErrs = append(allErrs, field.NotFound(
						field.NewPath("spec", "podSets").Index(i).Child("template", "spec", "resourceClaims").Index(j).Child("resourceClaimTemplateName"),
						fmt.Sprintf("DeviceClass %s is not mapped in DRA configuration for podset %s", dc, ps.Name),
					))
					return nil, allErrs
				}
				aggregated = utilresource.MergeResourceListKeepSum(aggregated, corev1.ResourceList{logical: resource.MustParse(strconv.FormatInt(qty, 10))})
			}
		}

		if len(aggregated) > 0 {
			perPodSet[ps.Name] = aggregated
		}
	}

	return perPodSet, nil
}
