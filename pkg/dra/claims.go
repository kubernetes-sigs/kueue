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
	"errors"
	"fmt"
	"strconv"

	corev1 "k8s.io/api/core/v1"
	resourcev1 "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/validation/field"
	dracel "k8s.io/dynamic-resource-allocation/cel"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/resources"
	utilresource "sigs.k8s.io/kueue/pkg/util/resource"
)

// celDeviceRequest tracks a device request that has CEL selectors,
// along with the requested count, for validation against actual devices.
type celDeviceRequest struct {
	index     int
	count     int64
	selectors []resourcev1.DeviceSelector
}

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

		if err := validateCELSelectors(req.Exactly.Selectors, field.NewPath("devices", "requests").Index(i).Child("exactly", "selectors")); err != nil {
			allErrs = append(allErrs, field.Invalid(field.NewPath("devices", "requests").Index(i).Child("exactly", "selectors"), nil, err.Error()))
			return nil, allErrs
		}

		switch {
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

			// Validate CEL selectors against actual devices in the cluster.
			celBasePath := field.NewPath("spec", "podSets").Index(i).Child("template", "spec", "resourceClaims").Index(j)
			if celErrs := validateCELSelectorsAgainstDevices(ctx, cl, spec, celBasePath); len(celErrs) > 0 {
				for _, celErr := range celErrs {
					allErrs = append(allErrs, &field.Error{
						Type:     celErr.Type,
						Field:    celErr.Field,
						BadValue: celErr.BadValue,
						Detail:   fmt.Sprintf("ResourceClaimTemplate %s: %s", *prc.ResourceClaimTemplateName, celErr.Detail),
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

// validateCELSelectors compiles each CEL expression in the given selectors using
// the upstream DRA CEL compiler. This catches invalid CEL syntax, type errors,
// and other compilation issues before quota admission.
func validateCELSelectors(selectors []resourcev1.DeviceSelector, fldPath *field.Path) error {
	if len(selectors) == 0 {
		return nil
	}

	compiler := dracel.GetCompiler(dracel.Features{})
	var errs []error
	for i, sel := range selectors {
		if sel.CEL == nil {
			continue
		}
		result := compiler.CompileCELExpression(sel.CEL.Expression, dracel.Options{})
		if result.Error != nil {
			errs = append(errs, field.Invalid(
				fldPath.Index(i).Child("cel", "expression"),
				sel.CEL.Expression,
				fmt.Sprintf("CEL compilation failed: %s", result.Error.Detail),
			))
		}
	}
	return errors.Join(errs...)
}

// extractCELRequests returns the device requests from a claim spec that have CEL selectors.
func extractCELRequests(claimSpec *resourcev1.ResourceClaimSpec) []celDeviceRequest {
	if claimSpec == nil {
		return nil
	}
	var result []celDeviceRequest
	for i, req := range claimSpec.Devices.Requests {
		if req.Exactly == nil || len(req.Exactly.Selectors) == 0 {
			continue
		}
		hasCEL := false
		for _, sel := range req.Exactly.Selectors {
			if sel.CEL != nil {
				hasCEL = true
				break
			}
		}
		if hasCEL {
			result = append(result, celDeviceRequest{
				index:     i,
				count:     req.Exactly.Count,
				selectors: req.Exactly.Selectors,
			})
		}
	}
	return result
}

// validateCELSelectorsAgainstDevices lists all ResourceSlices in the cluster and
// evaluates the CEL selectors from each request against the actual devices.
// If fewer devices match than requested, it returns field errors indicating the
// workload is unsatisfiable, preventing quota from being consumed by workloads
// whose pods can never be scheduled.
func validateCELSelectorsAgainstDevices(ctx context.Context, cl client.Client, claimSpec *resourcev1.ResourceClaimSpec, basePath *field.Path) field.ErrorList {
	celReqs := extractCELRequests(claimSpec)
	if len(celReqs) == 0 {
		return nil
	}

	var sliceList resourcev1.ResourceSliceList
	if err := cl.List(ctx, &sliceList); err != nil {
		return field.ErrorList{field.InternalError(basePath, fmt.Errorf("failed to list ResourceSlices: %w", err))}
	}

	// Build CEL device representations from ResourceSlices.
	var clusterDevices []dracel.Device
	for i := range sliceList.Items {
		slice := &sliceList.Items[i]
		for j := range slice.Spec.Devices {
			dev := &slice.Spec.Devices[j]
			clusterDevices = append(clusterDevices, dracel.Device{
				Driver:     slice.Spec.Driver,
				Attributes: dev.Attributes,
				Capacity:   dev.Capacity,
			})
		}
	}

	compiler := dracel.GetCompiler(dracel.Features{})
	var allErrs field.ErrorList

	for _, cr := range celReqs {
		// Compile all CEL selectors for this request.
		var compiled []dracel.CompilationResult
		for _, sel := range cr.selectors {
			if sel.CEL == nil {
				continue
			}
			result := compiler.CompileCELExpression(sel.CEL.Expression, dracel.Options{})
			if result.Error != nil {
				allErrs = append(allErrs, field.Invalid(
					basePath.Child("devices", "requests").Index(cr.index).Child("exactly", "selectors"),
					sel.CEL.Expression,
					fmt.Sprintf("CEL compilation failed: %s", result.Error.Detail),
				))
				continue
			}
			compiled = append(compiled, result)
		}
		if len(compiled) == 0 {
			continue
		}

		// Evaluate against all cluster devices.
		var matchCount int64
		for _, dev := range clusterDevices {
			allMatch := true
			for _, comp := range compiled {
				matches, _, err := comp.DeviceMatches(ctx, dev)
				if err != nil || !matches {
					allMatch = false
					break
				}
			}
			if allMatch {
				matchCount++
			}
		}

		if matchCount < cr.count {
			allErrs = append(allErrs, field.Invalid(
				basePath.Child("devices", "requests").Index(cr.index).Child("exactly", "selectors"),
				nil,
				fmt.Sprintf("CEL selectors match only %d device(s) in the cluster but %d requested; workload cannot be satisfied", matchCount, cr.count),
			))
		}
	}

	return allErrs
}
