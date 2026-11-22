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

// celDeviceRequest tracks a device request that has CEL selectors, for validation
// against actual devices.
type celDeviceRequest struct {
	index          int
	firstAvailable bool
	// allDevices marks an adminAccess request in allocationMode All.
	allDevices bool
	// alternatives holds the exactly request, or the firstAvailable entries in order.
	alternatives []celAlternative
}

// celAlternative is one way to satisfy a celDeviceRequest.
type celAlternative struct {
	firstAvailableIndex int
	count               int64
	deviceClassName     string
	selectors           []resourcev1.DeviceSelector
}

// selectorsPath returns the field path of alt's selectors within the request at reqPath.
func (r celDeviceRequest) selectorsPath(reqPath *field.Path, alt celAlternative) *field.Path {
	if r.firstAvailable {
		return reqPath.Child("firstAvailable").Index(alt.firstAvailableIndex).Child("selectors")
	}
	return reqPath.Child("exactly", "selectors")
}

func isAdminAccessRequest(req *resourcev1.ExactDeviceRequest) bool {
	return req.AdminAccess != nil && *req.AdminAccess
}

// claimCharges is the quota cost of one ResourceClaimSpec.
type claimCharges struct {
	// perDeviceClass counts Exactly requests by DeviceClass; the caller maps them.
	perDeviceClass resources.Requests
	// perLogicalResource holds firstAvailable charges, which arrive mapped.
	perLogicalResource map[corev1.ResourceName]resources.Amount
}

// chargesForClaimSpec classifies every request in the provided ResourceClaimSpec
// and returns what it costs. Returns field errors for unsupported request features
// (AllocationMode All, and FirstAvailable unless the prioritized-list gate is on).
// AdminAccess requests and requests for excluded DeviceClasses are skipped (zero quota).
func chargesForClaimSpec(claimSpec *resourcev1.ResourceClaimSpec, mapper *ResourceMapper) (claimCharges, field.ErrorList) {
	charges := claimCharges{
		perDeviceClass:     resources.NewRequests(),
		perLogicalResource: map[corev1.ResourceName]resources.Amount{},
	}
	if claimSpec == nil {
		return charges, nil
	}

	var allErrs field.ErrorList

	devicesRequestsPath := field.NewPath("devices", "requests")

	for i, req := range claimSpec.Devices.Requests {
		// A request is exactly or firstAvailable; the latter needs its gate.
		var dcName string
		var q int64
		if req.FirstAvailable != nil {
			if alternativesExcluded(req.FirstAvailable, mapper) {
				for k := range req.FirstAvailable {
					selectorsPath := devicesRequestsPath.Index(i).Child("firstAvailable").Index(k).Child("selectors")
					if err := validateCELSelectors(req.FirstAvailable[k].Selectors, selectorsPath); err != nil {
						allErrs = append(allErrs, field.Invalid(selectorsPath, nil, err.Error()))
						return claimCharges{}, allErrs
					}
				}
				continue
			}
			if !features.Enabled(features.KueueDRAIntegrationPrioritizedList) {
				allErrs = append(allErrs, field.Invalid(devicesRequestsPath.Index(i), nil, "FirstAvailable device selection is not supported"))
				return claimCharges{}, allErrs
			}
			logical, count, errs := chargeForFirstAvailable(&claimSpec.Devices.Requests[i], mapper, devicesRequestsPath.Index(i))
			if len(errs) > 0 {
				return claimCharges{}, append(allErrs, errs...)
			}
			charges.perLogicalResource[logical] = charges.perLogicalResource[logical].AddInt64(count)
			continue
		}

		if req.Exactly == nil {
			allErrs = append(allErrs, field.Invalid(devicesRequestsPath.Index(i), nil, "Exactly must be set if FirstAvailable is nil"))
			return claimCharges{}, allErrs
		}

		selectorsPath := devicesRequestsPath.Index(i).Child("exactly", "selectors")
		if err := validateCELSelectors(req.Exactly.Selectors, selectorsPath); err != nil {
			allErrs = append(allErrs, field.Invalid(selectorsPath, nil, err.Error()))
			return claimCharges{}, allErrs
		}

		if mapper.isExcluded(corev1.ResourceName(req.Exactly.DeviceClassName)) {
			continue
		}

		switch {
		case isAdminAccessRequest(req.Exactly):
			continue
		case req.Exactly.AllocationMode == resourcev1.DeviceAllocationModeAll:
			allErrs = append(
				allErrs,
				field.Invalid(devicesRequestsPath.Index(i).Child("exactly", "allocationMode"), resourcev1.DeviceAllocationModeAll, "AllocationMode 'All' is not supported"),
			)
			return claimCharges{}, allErrs
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
			return claimCharges{}, allErrs
		}

		dc := corev1.ResourceName(dcName)
		if dc == "" {
			continue
		}
		// Counts are user-controlled up to MaxInt64, and the sum is exact.
		charges.perDeviceClass.Set(dc, charges.perDeviceClass.ResourceValue(dc).AddInt64(q))
	}
	return charges, nil
}

// alternativesExcluded reports whether every alternative of a firstAvailable request
// names an excluded DeviceClass, so Kueue charges nothing whichever one is allocated.
func alternativesExcluded(alternatives []resourcev1.DeviceSubRequest, mapper *ResourceMapper) bool {
	if len(alternatives) == 0 {
		return false
	}
	for i := range alternatives {
		if !mapper.isExcluded(corev1.ResourceName(alternatives[i].DeviceClassName)) {
			return false
		}
	}
	return true
}

// chargeForFirstAvailable returns the logical resource a firstAvailable request is
// charged on and the count every one of its alternatives asks for. Only then does the
// charge equal whichever alternative kube-scheduler allocates, so a request whose
// alternatives differ in count or in logical resource is refused.
func chargeForFirstAvailable(req *resourcev1.DeviceRequest, mapper *ResourceMapper, reqPath *field.Path) (corev1.ResourceName, int64, field.ErrorList) {
	// An empty list has no count to charge.
	if len(req.FirstAvailable) == 0 {
		return "", 0, field.ErrorList{field.Required(reqPath.Child("firstAvailable"), "must list at least one alternative")}
	}

	var (
		logical corev1.ResourceName
		shared  int64
	)
	for i := range req.FirstAvailable {
		sub := &req.FirstAvailable[i]
		subPath := reqPath.Child("firstAvailable").Index(i)
		mapped, count, errs := chargeForAlternative(sub, mapper, subPath)
		if len(errs) > 0 {
			return "", 0, errs
		}
		switch {
		case logical == "":
			logical, shared = mapped, count
		case mapped != logical:
			return "", 0, field.ErrorList{field.Invalid(subPath.Child("deviceClassName"), corev1.ResourceName(sub.DeviceClassName),
				fmt.Sprintf("every alternative must map to %q, this one maps to %q", logical, mapped))}
		case count != shared:
			return "", 0, field.ErrorList{field.Invalid(subPath.Child("count"), sub.Count,
				fmt.Sprintf("every alternative must have count %d, this one has %d", shared, count))}
		}
	}
	return logical, shared, nil
}

// chargeForAlternative returns the logical resource one alternative maps to and the
// device count it asks for, refusing every form Kueue does not charge.
func chargeForAlternative(sub *resourcev1.DeviceSubRequest, mapper *ResourceMapper, subPath *field.Path) (corev1.ResourceName, int64, field.ErrorList) {
	if sub.AllocationMode != resourcev1.DeviceAllocationModeExactCount {
		return "", 0, field.ErrorList{field.NotSupported(subPath.Child("allocationMode"), sub.AllocationMode,
			[]resourcev1.DeviceAllocationMode{resourcev1.DeviceAllocationModeExactCount})}
	}
	count := sub.Count
	if count <= 0 {
		return "", 0, field.ErrorList{field.Invalid(subPath.Child("count"), sub.Count, "must be greater than zero")}
	}
	if sub.DeviceClassName == "" {
		return "", 0, field.ErrorList{field.Required(subPath.Child("deviceClassName"), "")}
	}
	if err := validateCELSelectors(sub.Selectors, subPath.Child("selectors")); err != nil {
		return "", 0, field.ErrorList{field.Invalid(subPath.Child("selectors"), nil, err.Error())}
	}

	dc := corev1.ResourceName(sub.DeviceClassName)
	mapped, found := mapper.Lookup(dc)
	if !found {
		return "", 0, field.ErrorList{field.NotFound(subPath.Child("deviceClassName"), dc)}
	}
	// The counter and capacity paths read Exactly requests only, so an alternative
	// on one of those mappings would be charged nothing rather than too little.
	if len(mapper.getCounterConfigs(dc)) > 0 || len(mapper.getCapacityConfigs(dc)) > 0 {
		return "", 0, field.ErrorList{field.Invalid(subPath.Child("deviceClassName"), dc,
			"a counter-backed or capacity-backed DeviceClass is not supported for firstAvailable")}
	}
	return mapped, count, nil
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
// When skipDeviceCheck is true (for example, on a MultiKueue manager), CEL selector syntax
// compilation still runs, while the ResourceSlice device availability check is skipped.
//
// If at least one DeviceClass is not present in the DRA configuration or if unsupported DRA
// features are detected, the function returns field errors.
func GetResourceRequestsForResourceClaimTemplates(
	ctx context.Context,
	cl client.Client,
	sliceCache *ResourceSliceCache,
	mapper *ResourceMapper,
	wl *kueue.Workload,
	skipDeviceCheck bool) (map[kueue.PodSetReference]corev1.ResourceList, field.ErrorList) {
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

			charges, fieldErrs := chargesForClaimSpec(spec, mapper)
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
			if !skipDeviceCheck {
				claimPath := field.NewPath("spec", "podSets").Index(i).Child("template", "spec", "resourceClaims").Index(j)
				deviceErrs := validateDeviceClassesExist(ctx, cl, spec, claimPath)
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
			}

			// firstAvailable charges arrive mapped. Emit them as whole units like the
			// Exactly count below; a formatter would read a resource named cpu in milli.
			for logical, amount := range charges.perLogicalResource {
				aggregated = utilresource.MergeResourceListKeepSum(aggregated, corev1.ResourceList{logical: resource.MustParse(amount.String())})
			}

			for dc, qty := range charges.perDeviceClass.Iter() {
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

// extractCELRequests returns the device requests from a claim spec that have CEL selectors,
// leaving out requests for excluded DeviceClasses. A firstAvailable request needs a
// CEL selector on every alternative.
func extractCELRequests(claimSpec *resourcev1.ResourceClaimSpec, mapper *ResourceMapper) []celDeviceRequest {
	if claimSpec == nil {
		return nil
	}
	hasCEL := func(sel resourcev1.DeviceSelector) bool { return sel.CEL != nil }
	var result []celDeviceRequest
	for i, req := range claimSpec.Devices.Requests {
		if req.FirstAvailable != nil {
			if !features.Enabled(features.KueueDRAIntegrationPrioritizedList) || alternativesExcluded(req.FirstAvailable, mapper) {
				continue
			}
			// kube-scheduler can fall back to any alternative, so the request is
			// checked only when every alternative has a CEL selector to check.
			alternatives := make([]celAlternative, 0, len(req.FirstAvailable))
			for j, sub := range req.FirstAvailable {
				if !slices.ContainsFunc(sub.Selectors, hasCEL) {
					alternatives = nil
					break
				}
				alternatives = append(alternatives, celAlternative{
					firstAvailableIndex: j,
					count:               sub.Count,
					deviceClassName:     sub.DeviceClassName,
					selectors:           sub.Selectors,
				})
			}
			if len(alternatives) > 0 {
				result = append(result, celDeviceRequest{index: i, firstAvailable: true, alternatives: alternatives})
			}
			continue
		}
		if req.Exactly == nil || !slices.ContainsFunc(req.Exactly.Selectors, hasCEL) || mapper.isExcluded(corev1.ResourceName(req.Exactly.DeviceClassName)) {
			continue
		}
		result = append(result, celDeviceRequest{
			index:      i,
			allDevices: req.Exactly.AllocationMode == resourcev1.DeviceAllocationModeAll,
			alternatives: []celAlternative{{
				count:           req.Exactly.Count,
				deviceClassName: req.Exactly.DeviceClassName,
				selectors:       req.Exactly.Selectors,
			}},
		})
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
// For each request alternative, it resolves the DeviceClassName to its DeviceClass and uses
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
	celReqs := extractCELRequests(claimSpec, mapper)
	if len(celReqs) == 0 {
		return nil
	}

	var celAlternatives []celAlternative
	for _, cr := range celReqs {
		celAlternatives = append(celAlternatives, cr.alternatives...)
	}

	var collectedSlices []resourcev1.ResourceSlice
	needsAll := false
	for _, alt := range celAlternatives {
		if len(mapper.getCounterConfigs(corev1.ResourceName(alt.deviceClassName))) == 0 && len(mapper.getCapacityConfigs(corev1.ResourceName(alt.deviceClassName))) == 0 {
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
		for _, alt := range celAlternatives {
			dc := corev1.ResourceName(alt.deviceClassName)
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
