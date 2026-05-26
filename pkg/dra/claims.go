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
	"slices"
	"strconv"

	corev1 "k8s.io/api/core/v1"
	resourcev1 "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/validation/field"
	dracel "k8s.io/dynamic-resource-allocation/cel"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/resources"
	utilresource "sigs.k8s.io/kueue/pkg/util/resource"
)

// celCache is a package-level CEL compilation cache that avoids recompiling
// the same CEL expressions on every reconciliation. Thread-safe.
var celCache = dracel.NewCache(256, dracel.Features{})

// celDeviceRequest tracks a device request that has CEL selectors,
// along with the requested count, for validation against actual devices.
type celDeviceRequest struct {
	index           int
	count           int64
	deviceClassName string
	selectors       []resourcev1.DeviceSelector
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

	devicesRequestsPath := field.NewPath("devices", "requests")

	for i, req := range claimSpec.Devices.Requests {
		// v1 DeviceRequest has Exactly or FirstAvailable. For Step 1, we
		// preserve existing semantics by only supporting Exactly with Count.
		var dcName string
		var q int64
		if req.FirstAvailable != nil {
			allErrs = append(allErrs, field.Invalid(devicesRequestsPath.Index(i), nil, "FirstAvailable device selection is not supported"))
			return nil, allErrs
		}

		selectorsPath := devicesRequestsPath.Index(i).Child("exactly", "selectors")
		if err := validateCELSelectors(req.Exactly.Selectors, selectorsPath); err != nil {
			allErrs = append(allErrs, field.Invalid(selectorsPath, nil, err.Error()))
			return nil, allErrs
		}

		switch {
		case req.Exactly.AdminAccess != nil && *req.Exactly.AdminAccess:
			allErrs = append(allErrs, field.Invalid(devicesRequestsPath.Index(i).Child("exactly", "adminAccess"), nil, "AdminAccess is not supported"))
			return nil, allErrs
		case req.Exactly.AllocationMode == resourcev1.DeviceAllocationModeAll:
			allErrs = append(
				allErrs,
				field.Invalid(devicesRequestsPath.Index(i).Child("exactly", "allocationMode"), resourcev1.DeviceAllocationModeAll, "AllocationMode 'All' is not supported"),
			)
			return nil, allErrs
		case req.Exactly.AllocationMode == resourcev1.DeviceAllocationModeExactCount:
			dcName = req.Exactly.DeviceClassName
			q = req.Exactly.Count
		default:
			allErrs = append(
				allErrs,
				field.Invalid(
					devicesRequestsPath.Index(i).Child("exactly", "allocationMode"),
					req.Exactly.AllocationMode,
					fmt.Sprintf("unsupported allocation mode: %s", req.Exactly.AllocationMode),
				),
			)
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
	_, compErrs := compileCELSelectors(selectors, fldPath, "CEL compilation failed")
	return compErrs.ToAggregate()
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
		if slices.ContainsFunc(req.Exactly.Selectors, func(sel resourcev1.DeviceSelector) bool {
			return sel.CEL != nil
		}) {
			result = append(result, celDeviceRequest{
				index:           i,
				count:           req.Exactly.Count,
				deviceClassName: req.Exactly.DeviceClassName,
				selectors:       req.Exactly.Selectors,
			})
		}
	}
	return result
}

// compileCELSelectors compiles CEL expressions from selectors, skipping non-CEL selectors.
// Returns compiled results and any compilation errors encountered.
func compileCELSelectors(selectors []resourcev1.DeviceSelector, errPath *field.Path, errContext string) ([]dracel.CompilationResult, field.ErrorList) {
	var compiled []dracel.CompilationResult
	var allErrs field.ErrorList

	for i, sel := range selectors {
		if sel.CEL == nil {
			continue
		}
		result := celCache.GetOrCompile(sel.CEL.Expression)
		if result.Error != nil {
			allErrs = append(allErrs, field.Invalid(
				errPath.Index(i).Child("cel", "expression"),
				sel.CEL.Expression,
				fmt.Sprintf("%s: %s", errContext, result.Error.Detail),
			))
		} else {
			compiled = append(compiled, result)
		}
	}
	return compiled, allErrs
}

// evaluateSelectorsOnDevice checks if all compiled CEL selectors match the given device.
// Returns (allMatch bool, firstError error). If any selector fails to evaluate or returns
// false, allMatch will be false. The first evaluation error encountered is returned.
func evaluateSelectorsOnDevice(ctx context.Context, selectors []dracel.CompilationResult, dev dracel.Device) (bool, error) {
	for _, comp := range selectors {
		matches, _, err := comp.DeviceMatches(ctx, dev)
		if err != nil {
			return false, err
		}
		if !matches {
			return false, nil
		}
	}
	return true, nil
}

// buildDeviceListFromSlices extracts all devices from ResourceSlices into CEL device representations.
func buildDeviceListFromSlices(slices []resourcev1.ResourceSlice) []dracel.Device {
	var devices []dracel.Device
	for i := range slices {
		slice := &slices[i]
		for j := range slice.Spec.Devices {
			dev := &slice.Spec.Devices[j]
			devices = append(devices, dracel.Device{
				Driver:     slice.Spec.Driver,
				Attributes: dev.Attributes,
				Capacity:   dev.Capacity,
			})
		}
	}
	return devices
}

// resolveAndCompileDeviceClass fetches and compiles selectors for a DeviceClass, using cache.
// Returns compiled selectors and a field error if the class cannot be resolved or has invalid CEL.
func resolveAndCompileDeviceClass(
	ctx context.Context,
	cl client.Client,
	className string,
	cache map[string]*resourcev1.DeviceClass,
	basePath *field.Path,
	reqIndex int,
) ([]dracel.CompilationResult, *field.Error) {
	if className == "" {
		return nil, nil
	}

	dc, ok := cache[className]
	if !ok {
		dc = &resourcev1.DeviceClass{}
		if err := cl.Get(ctx, client.ObjectKey{Name: className}, dc); err != nil {
			return nil, field.InternalError(
				basePath.Child("devices", "requests").Index(reqIndex),
				fmt.Errorf("failed to get DeviceClass %s: %w", className, err),
			)
		}
		cache[className] = dc
	}

	compiled, errs := compileCELSelectors(
		dc.Spec.Selectors,
		basePath.Child("devices", "requests").Index(reqIndex),
		fmt.Sprintf("DeviceClass %s has invalid CEL selector", className),
	)
	if len(errs) > 0 {
		// A single class selector error is enough to reject the request.
		return nil, errs[0]
	}

	return compiled, nil
}

// countMatchingDevices evaluates devices against class and request selectors,
// marking matchedDevices devices and returning the match count.
// Devices already in the matchedDevices map are skipped to prevent double-counting.
func countMatchingDevices(ctx context.Context, devices []dracel.Device, classSelectors, requestSelectors []dracel.CompilationResult, matchedDevices sets.Set[int], needed int64) (int64, error) {
	var matchCount int64

	for devIdx, dev := range devices {
		if matchedDevices.Has(devIdx) {
			continue
		}

		classMatch, err := evaluateSelectorsOnDevice(ctx, classSelectors, dev)
		if err != nil {
			return matchCount, err
		}
		if !classMatch {
			continue
		}

		allMatch, err := evaluateSelectorsOnDevice(ctx, requestSelectors, dev)
		if err != nil {
			return matchCount, err
		}

		if allMatch {
			matchedDevices.Insert(devIdx)
			matchCount++
			if matchCount >= needed {
				break
			}
		}
	}

	return matchCount, nil
}

// validateCELSelectorsAgainstDevices lists all ResourceSlices in the cluster and
// evaluates the CEL selectors from each request against the actual devices.
// For each request, it resolves the DeviceClassName to its DeviceClass and uses
// the class selectors to pre-filter devices before evaluating the request's own
// CEL selectors. This avoids running CEL against devices that belong to
// unrelated drivers or classes.
// If fewer devices match than requested, it returns field errors indicating the
// workload is unsatisfiable, preventing quota from being matchedDevices by workloads
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

	clusterDevices := buildDeviceListFromSlices(sliceList.Items)
	classCache := make(map[string]*resourcev1.DeviceClass)
	matchedDevices := sets.New[int]()

	var allErrs field.ErrorList

	for _, cr := range celReqs {
		classSelectors, err := resolveAndCompileDeviceClass(ctx, cl, cr.deviceClassName, classCache, basePath, cr.index)
		if err != nil {
			allErrs = append(allErrs, err)
			continue
		}

		compiled, compErrs := compileCELSelectors(
			cr.selectors,
			basePath.Child("devices", "requests").Index(cr.index).Child("exactly", "selectors"),
			"CEL compilation failed",
		)
		if len(compErrs) > 0 {
			allErrs = append(allErrs, compErrs...)
			continue
		}
		if len(compiled) == 0 {
			continue
		}

		matchCount, evalErr := countMatchingDevices(ctx, clusterDevices, classSelectors, compiled, matchedDevices, cr.count)

		if evalErr != nil {
			ctrl.LoggerFrom(ctx).V(3).Info("CEL evaluation error encountered while matching devices",
				"deviceClassName", cr.deviceClassName, "error", evalErr)
			allErrs = append(allErrs, field.Invalid(
				basePath.Child("devices", "requests").Index(cr.index).Child("exactly", "selectors"),
				nil,
				fmt.Sprintf("CEL evaluation failed for DeviceClass %s: %v", cr.deviceClassName, evalErr),
			))
			continue
		}

		if matchCount < cr.count {
			allErrs = append(allErrs, field.Invalid(
				basePath.Child("devices", "requests").Index(cr.index).Child("exactly", "selectors"),
				nil,
				fmt.Sprintf("insufficient matching devices for CEL selector in DeviceClass %s: %d device(s) match in the cluster but %d requested",
					cr.deviceClassName, matchCount, cr.count),
			))
		}
	}

	return allErrs
}
