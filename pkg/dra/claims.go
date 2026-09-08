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
	"maps"
	"math"
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
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/resources"
	utilmath "sigs.k8s.io/kueue/pkg/util/math"
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

func isAdminAccessRequest(req *resourcev1.ExactDeviceRequest) bool {
	return req.AdminAccess != nil && *req.AdminAccess
}

// claimCharges is what one ResourceClaimSpec costs. An Exactly request is counted
// against its DeviceClass and mapped to a logical resource by the caller. The
// alternatives of a firstAvailable request can only be compared once they are
// mapped, so its charge arrives already resolved.
type claimCharges struct {
	perDeviceClass     resources.Requests
	perLogicalResource map[corev1.ResourceName]int64
}

// chargesForClaimSpec classifies every request in the provided ResourceClaimSpec
// and returns what it costs. Returns field errors for unsupported request features
// (AllocationMode All, and FirstAvailable unless the prioritized-list gate is on).
// AdminAccess requests are skipped (zero quota).
func chargesForClaimSpec(claimSpec *resourcev1.ResourceClaimSpec, mapper *ResourceMapper) (claimCharges, field.ErrorList) {
	charges := claimCharges{
		perDeviceClass:     resources.NewRequests(),
		perLogicalResource: map[corev1.ResourceName]int64{},
	}
	if claimSpec == nil {
		return charges, nil
	}

	var allErrs field.ErrorList

	devicesRequestsPath := field.NewPath("devices", "requests")

	for i, req := range claimSpec.Devices.Requests {
		// v1 DeviceRequest has Exactly or FirstAvailable. For Step 1, we
		// preserve existing semantics by only supporting Exactly with Count.
		var dcName string
		var q int64
		if req.FirstAvailable != nil {
			if req.Exactly != nil {
				allErrs = append(allErrs, field.Invalid(devicesRequestsPath.Index(i), nil, "exactly one of exactly or firstAvailable must be set"))
				return claimCharges{}, allErrs
			}
			if !features.Enabled(features.KueueDRAIntegrationPrioritizedList) {
				allErrs = append(allErrs, field.Invalid(devicesRequestsPath.Index(i), nil, "FirstAvailable device selection is not supported"))
				return claimCharges{}, allErrs
			}
			logical, count, errs := chargeForPrioritizedList(&claimSpec.Devices.Requests[i], mapper, devicesRequestsPath.Index(i))
			if len(errs) > 0 {
				return claimCharges{}, append(allErrs, errs...)
			}
			total, ok := utilmath.BoundedAdd(charges.perLogicalResource[logical], count)
			if !ok {
				allErrs = append(allErrs, field.Invalid(devicesRequestsPath.Index(i), count,
					fmt.Sprintf("the counts charged to logical resource %s do not add up to a bounded quota amount", logical)))
				return claimCharges{}, allErrs
			}
			charges.perLogicalResource[logical] = total
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
		// Device counts are user-controlled and effectively unbounded (the
		// apiserver accepts up to MaxInt64), so accumulate with a saturating add
		// (matching the scheduler's Amount arithmetic) rather than letting the
		// sum wrap to a negative count.
		charges.perDeviceClass.Set(dc, utilmath.SaturatingAdd(charges.perDeviceClass.ResourceValue(dc), q))
	}
	return charges, nil
}

// chargeForPrioritizedList returns the logical resource a firstAvailable request is
// charged on, and the largest count among its alternatives. The scheduler picks one
// alternative, so that count bounds whichever it picks. One count can only be a bound
// if every alternative maps to the same logical resource, which is what this refuses
// to assume.
func chargeForPrioritizedList(req *resourcev1.DeviceRequest, mapper *ResourceMapper, reqPath *field.Path) (corev1.ResourceName, int64, field.ErrorList) {
	// Nothing to take a maximum over, so no count bounds the request. Checked here
	// so the charge can never come back unresolved.
	if len(req.FirstAvailable) == 0 {
		return "", 0, field.ErrorList{field.Required(reqPath.Child("firstAvailable"), "must list at least one alternative")}
	}

	var (
		logical  corev1.ResourceName
		maxCount int64
	)
	for i := range req.FirstAvailable {
		sub := &req.FirstAvailable[i]
		subPath := reqPath.Child("firstAvailable").Index(i)

		// The apiserver defaults an unset mode to ExactCount and a zero count to
		// one. Objects that never went through it, in tests and fake clients,
		// arrive here unset, so read them the same way.
		if sub.AllocationMode != "" && sub.AllocationMode != resourcev1.DeviceAllocationModeExactCount {
			return "", 0, field.ErrorList{field.NotSupported(subPath.Child("allocationMode"), sub.AllocationMode,
				[]resourcev1.DeviceAllocationMode{resourcev1.DeviceAllocationModeExactCount})}
		}
		count := sub.Count
		if count == 0 {
			count = 1
		}
		if count < 0 {
			return "", 0, field.ErrorList{field.Invalid(subPath.Child("count"), sub.Count, "must not be negative")}
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

		if logical == "" {
			logical = mapped
		} else if mapped != logical {
			return "", 0, field.ErrorList{field.Invalid(subPath.Child("deviceClassName"), dc,
				fmt.Sprintf("every alternative must map to %q, this one maps to %q", logical, mapped))}
		}
		maxCount = max(maxCount, count)
	}
	return logical, maxCount, nil
}

// canonicalUnits converts a charge the way resources.ResourceValue does, so the
// number checked here is the one the queue reads, and reports whether that
// conversion was exact. Scaling a device count instead answers 2000m for 1.5
// CPU where the queue answers 1500m. An amount the unit cannot hold, a fraction
// of a device or a sub-milli CPU, is refused rather than rounded.
func canonicalUnits(name corev1.ResourceName, qty resource.Quantity) (int64, bool) {
	v := resources.ResourceValue(name, qty)
	rounded := *resource.NewQuantity(v, resource.DecimalSI)
	if name == corev1.ResourceCPU {
		rounded = *resource.NewMilliQuantity(v, resource.DecimalSI)
	}
	return v, qty.Cmp(rounded) == 0
}

// chargeFitsCanonicalUnits reports the charges that cannot reach the queue
// intact. Each PodSet's charge is multiplied by its count and the results are
// summed across PodSets, in the resource's canonical unit. Both of those steps
// saturate rather than fail, and math.MaxInt64 is the value the quota code
// reads as unlimited, so a count that reaches either is refused here while the
// number that was asked for is still known.
//
// The count is the one the PodSet asks for rather than the one left after a
// reclaim, so a request is refused on what it asks for. Admitting it and
// scaling down later is not open to it: PodSetResources scales by dividing the
// aggregate it holds, and one that saturated on the way in does not divide back
// to the value it came from.
//
// The bound covers the charges this function is given. A Pod requesting the
// same logical resource by name, and the counter and capacity charges merged in
// after this returns, are added later and are not bounded here.
func chargeFitsCanonicalUnits(podSets []kueue.PodSet, perPodSet map[kueue.PodSetReference]corev1.ResourceList) field.ErrorList {
	var errs field.ErrorList
	totals := map[corev1.ResourceName]int64{}
	for i := range podSets {
		ps := &podSets[i]
		psPath := field.NewPath("spec", "podSets").Index(i)
		// Sorted so a PodSet with more than one bad charge reports them in the
		// same order every time, rather than whichever the map hands over first.
		charged := perPodSet[ps.Name]
		for _, name := range slices.Sorted(maps.Keys(charged)) {
			qty := charged[name]
			// The charge is accumulated with Quantity.Add and can leave int64
			// behind, where Value wraps and reads back as an ordinary count.
			canonical, exact := canonicalUnits(name, qty)
			if canonical < 0 || canonical == math.MaxInt64 {
				errs = append(errs, field.Invalid(psPath, qty.String(),
					fmt.Sprintf("device count charged to logical resource %s is not a bounded quota amount", name)))
				continue
			}
			if !exact {
				errs = append(errs, field.Invalid(psPath, qty.String(),
					fmt.Sprintf("device count charged to logical resource %s is not representable in the units it is accounted in", name)))
				continue
			}
			scaled, ok := utilmath.BoundedMul(canonical, int64(ps.Count))
			if !ok {
				errs = append(errs, field.Invalid(psPath.Child("count"), ps.Count,
					fmt.Sprintf("device count charged to logical resource %s is not representable once multiplied by the podSet count", name)))
				continue
			}
			total, ok := utilmath.BoundedAdd(totals[name], scaled)
			if !ok {
				errs = append(errs, field.Invalid(field.NewPath("spec", "podSets"), name,
					fmt.Sprintf("total charged to logical resource %s across podSets is not representable as a bounded quota amount", name)))
				continue
			}
			totals[name] = total
		}
	}
	return errs
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
	sliceCache *ResourceSliceCache,
	mapper *ResourceMapper,
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

			// Validate CEL selectors against actual devices in the cluster.
			celBasePath := field.NewPath("spec", "podSets").Index(i).Child("template", "spec", "resourceClaims").Index(j)
			if celErrs := validateCELSelectorsAgainstDevices(ctx, cl, sliceCache, mapper, spec, celBasePath); len(celErrs) > 0 {
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

			// Already resolved to a logical resource, since the maximum over the
			// alternatives of a request can only be taken after the mapping.
			for logical, qty := range charges.perLogicalResource {
				aggregated = utilresource.MergeResourceListKeepSum(aggregated, corev1.ResourceList{logical: resource.MustParse(strconv.FormatInt(qty, 10))})
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
				aggregated = utilresource.MergeResourceListKeepSum(aggregated, corev1.ResourceList{logical: resource.MustParse(strconv.FormatInt(qty, 10))})
			}
		}

		if len(aggregated) > 0 {
			perPodSet[ps.Name] = aggregated
		}
	}

	// Only under the gate. perPodSet carries the Exactly charges too, and an
	// existing one that reaches the range would be refused here rather than
	// saturating as it does today, which is a change this gate is not entitled
	// to make while it is off. Narrowing further, to the keys an envelope
	// actually reached, needs the provenance preprocessing does not carry yet.
	if features.Enabled(features.KueueDRAIntegrationPrioritizedList) {
		if errs := chargeFitsCanonicalUnits(wl.Spec.PodSets, perPodSet); len(errs) > 0 {
			return nil, append(allErrs, errs...)
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

	clusterDevices := buildDeviceListFromSlices(collectedSlices)
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
			// Cluster-state shortage: retryable until matching ResourceSlices are published.
			allErrs = append(allErrs, field.InternalError(
				basePath.Child("devices", "requests").Index(cr.index).Child("exactly", "selectors"),
				fmt.Errorf("insufficient matching devices for CEL selector in DeviceClass %s: %d device(s) match in the cluster but %d requested",
					cr.deviceClassName, matchCount, cr.count),
			))
		}
	}

	return allErrs
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
