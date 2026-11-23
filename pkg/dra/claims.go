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

	corev1 "k8s.io/api/core/v1"
	resourcev1 "k8s.io/api/resource/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/apimachinery/pkg/util/validation/field"
	dracel "k8s.io/dynamic-resource-allocation/cel"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
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
// ResourceClaimSpec. Returns field errors for unsupported request features
// (FirstAvailable, AdminAccess, AllocationMode All).
func countDevicesPerClass(claimSpec *resourcev1.ResourceClaimSpec) (resources.Requests, field.ErrorList) {
	out := resources.NewRequests()
	if claimSpec == nil {
		return out, nil
	}

	var allErrs field.ErrorList

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

		if req.Exactly == nil {
			allErrs = append(allErrs, field.Invalid(devicesRequestsPath.Index(i), nil, "Exactly must be set if FirstAvailable is nil"))
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
		// Counts are user-controlled up to MaxInt64, and the sum is exact.
		out.Set(dc, out.ResourceValue(dc).AddInt64(q))
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
// When skipDeviceClassCheck is true (on a MultiKueue manager), it does not check that the DeviceClasses exist,
// because they need to exist only on the worker clusters.
func GetResourceRequestsForResourceClaimTemplates(
	ctx context.Context,
	cl client.Client,
	sliceCache *ResourceSliceCache,
	mapper *ResourceMapper,
	wl *kueue.Workload,
	skipDeviceClassCheck bool) (map[kueue.PodSetReference]corev1.ResourceList, field.ErrorList) {
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

			// Validate the claim against the DeviceClasses and devices in the cluster.
			claimPath := field.NewPath("spec", "podSets").Index(i).Child("template", "spec", "resourceClaims").Index(j)
			var deviceErrs field.ErrorList
			if !skipDeviceClassCheck {
				deviceErrs = validateDeviceClassesExist(ctx, cl, spec, claimPath)
			}
			if len(deviceErrs) == 0 {
				deviceErrs = validateCELSelectorsAgainstDevices(ctx, cl, sliceCache, mapper, spec, claimPath)
			}
			if len(deviceErrs) > 0 {
				for _, deviceErr := range deviceErrs {
					allErrs = append(allErrs, &field.Error{
						Type:     deviceErr.Type,
						Field:    deviceErr.Field,
						BadValue: deviceErr.BadValue,
						Detail:   fmt.Sprintf("ResourceClaimTemplate %s: %s", *prc.ResourceClaimTemplateName, deviceErr.Detail),
					})
				}
				return nil, allErrs
			}

			for dc, qty := range deviceCounts.Iter() {
				logical, found := mapper.Lookup(dc)
				if !found {
					allErrs = append(allErrs, field.NotFound(
						field.NewPath("spec", "podSets").Index(i).Child("template", "spec", "resourceClaims").Index(j).Child("resourceClaimTemplateName"),
						fmt.Sprintf("DeviceClass %s is not mapped in DRA configuration for podset %s", dc, ps.Name),
					))
					return nil, allErrs
				}
				if features.Enabled(features.KueueDRAIntegrationPartitionableDevices) && len(mapper.getCounterConfigs(dc)) > 0 {
					continue
				}
				if features.Enabled(features.KueueDRAIntegrationConsumableCapacity) && len(mapper.getCapacityConfigs(dc)) > 0 {
					continue
				}
				aggregated = utilresource.MergeResourceListKeepSum(aggregated, corev1.ResourceList{logical: resource.MustParse(qty.String())})
			}
		}

		if len(aggregated) > 0 {
			perPodSet[ps.Name] = aggregated
		}
	}

	return perPodSet, nil
}

// validateDeviceClassesExist returns an error for each DeviceClass the claim names that
// does not exist, because kube-scheduler fails the whole claim when one is missing.
func validateDeviceClassesExist(ctx context.Context, cl client.Client, claimSpec *resourcev1.ResourceClaimSpec, basePath *field.Path) field.ErrorList {
	var allErrs field.ErrorList
	reqsPath := basePath.Child("devices", "requests")
	for i, req := range claimSpec.Devices.Requests {
		if req.Exactly != nil {
			if err := validateDeviceClassExists(ctx, cl, req.Exactly.DeviceClassName, reqsPath.Index(i).Child("exactly", "deviceClassName")); err != nil {
				allErrs = append(allErrs, err)
			}
		}
		for k, sub := range req.FirstAvailable {
			if err := validateDeviceClassExists(ctx, cl, sub.DeviceClassName, reqsPath.Index(i).Child("firstAvailable").Index(k).Child("deviceClassName")); err != nil {
				allErrs = append(allErrs, err)
			}
		}
	}
	return allErrs
}

func validateDeviceClassExists(ctx context.Context, cl client.Client, className string, fldPath *field.Path) *field.Error {
	if className == "" {
		return nil
	}
	err := cl.Get(ctx, client.ObjectKey{Name: className}, &resourcev1.DeviceClass{})
	switch {
	case err == nil:
		return nil
	case apierrors.IsNotFound(err):
		return &field.Error{Type: field.ErrorTypeNotFound, Field: fldPath.String(), BadValue: className, Detail: "DeviceClass does not exist"}
	default:
		return field.InternalError(fldPath, fmt.Errorf("failed to get DeviceClass %s: %w", className, err))
	}
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

// buildDeviceListFromSlices extracts, in listing order, the devices from the
// pools that the DRA allocator can allocate from.
func buildDeviceListFromSlices(ctx context.Context, slices []resourcev1.ResourceSlice) []dracel.Device {
	var devices []dracel.Device
	for _, slice := range eligibleSlices(ctx, slices) {
		for j := range slice.Spec.Devices {
			dev := &slice.Spec.Devices[j]
			devices = append(devices, dracel.Device{
				Driver:                   slice.Spec.Driver,
				AllowMultipleAllocations: dev.AllowMultipleAllocations,
				Attributes:               dev.Attributes,
				Capacity:                 dev.Capacity,
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

// validateCELSelectorsAgainstDevices lists all ResourceSlices in the cluster and
// evaluates the CEL selectors from each request against the actual devices.
// For each request, it resolves the DeviceClassName to its DeviceClass and uses
// the class selectors to pre-filter devices before evaluating the request's own
// CEL selectors. This avoids running CEL against devices that belong to
// unrelated drivers or classes.
// If the requests cannot all get distinct matching devices, it returns field
// errors that keep the workload from reserving quota for pods that cannot be
// scheduled.
func validateCELSelectorsAgainstDevices(
	ctx context.Context,
	cl client.Client,
	sliceCache *ResourceSliceCache,
	mapper *ResourceMapper,
	claimSpec *resourcev1.ResourceClaimSpec,
	basePath *field.Path,
) field.ErrorList {
	celReqs := extractCELRequests(claimSpec)
	if len(celReqs) == 0 {
		return nil
	}

	var collectedSlices []resourcev1.ResourceSlice
	needsAll := false
	for _, cr := range celReqs {
		if len(mapper.getCounterConfigs(corev1.ResourceName(cr.deviceClassName))) == 0 && len(mapper.getCapacityConfigs(corev1.ResourceName(cr.deviceClassName))) == 0 {
			needsAll = true
			break
		}
	}
	if needsAll {
		slices, err := sliceCache.ListAll(ctx)
		if err != nil {
			return field.ErrorList{field.InternalError(basePath, fmt.Errorf("failed to list ResourceSlices: %w", err))}
		}
		collectedSlices = slices
	} else {
		seenDrivers := sets.New[DriverReference]()
		for _, cr := range celReqs {
			dc := corev1.ResourceName(cr.deviceClassName)
			for _, cc := range mapper.getCounterConfigs(dc) {
				if seenDrivers.Has(cc.driver) {
					continue
				}
				seenDrivers.Insert(cc.driver)
				slices, err := sliceCache.ListByDriver(ctx, cc.driver)
				if err != nil {
					return field.ErrorList{field.InternalError(basePath, fmt.Errorf("failed to list ResourceSlices for driver %s: %w", cc.driver, err))}
				}
				collectedSlices = append(collectedSlices, slices...)
			}
			for _, capCfg := range mapper.getCapacityConfigs(dc) {
				if seenDrivers.Has(DriverReference(capCfg.driver)) {
					continue
				}
				seenDrivers.Insert(DriverReference(capCfg.driver))
				slices, err := sliceCache.ListByDriver(ctx, DriverReference(capCfg.driver))
				if err != nil {
					return field.ErrorList{field.InternalError(basePath, fmt.Errorf("failed to list ResourceSlices for driver %s: %w", capCfg.driver, err))}
				}
				collectedSlices = append(collectedSlices, slices...)
			}
		}
	}

	return newCELDeviceMatcher(cl, buildDeviceListFromSlices(ctx, collectedSlices), celReqs, basePath).check(ctx)
}

// MergeDRAResources merges src into dst, summing resource quantities for
// PodSets that appear in both maps. Returns the (possibly newly allocated) dst.
func MergeDRAResources(dst, src map[kueue.PodSetReference]corev1.ResourceList) map[kueue.PodSetReference]corev1.ResourceList {
	for podSetName, resources := range src {
		if existing, ok := dst[podSetName]; ok {
			dst[podSetName] = utilresource.MergeResourceListKeepSum(existing, resources)
		} else {
			if dst == nil {
				dst = make(map[kueue.PodSetReference]corev1.ResourceList)
			}
			dst[podSetName] = resources
		}
	}
	return dst
}
