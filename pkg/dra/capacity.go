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

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	resourcev1 "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/validation/field"
	dracel "k8s.io/dynamic-resource-allocation/cel"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	utilmath "sigs.k8s.io/kueue/pkg/util/math"
)

// DetermineCapacityResourcesForWorkload walks all ResourceClaimTemplates referenced
// by each PodSet, looks up capacity-based quota configs for each DeviceClass,
// and returns the aggregated capacity charges per PodSet.
func DetermineCapacityResourcesForWorkload(
	ctx context.Context,
	cl client.Client,
	sliceCache *ResourceSliceCache,
	mapper *ResourceMapper,
	wl *kueue.Workload,
) (map[kueue.PodSetReference]corev1.ResourceList, field.ErrorList) {
	log := ctrl.LoggerFrom(ctx)
	log.V(3).Info("Processing capacity resources for workload")

	perPodSet := make(map[kueue.PodSetReference]corev1.ResourceList)
	classCache := make(map[string]*resourcev1.DeviceClass)

	for i := range wl.Spec.PodSets {
		ps := &wl.Spec.PodSets[i]
		aggregated, errs := determineCapacityForPodSet(ctx, cl, sliceCache, mapper, ps, wl.Namespace, classCache, i)
		if len(errs) > 0 {
			return nil, errs
		}
		if len(aggregated) > 0 {
			perPodSet[ps.Name] = aggregated
			log.V(3).Info("Capacity resources aggregated for podSet", "podSet", ps.Name, "resources", aggregated)
		}
	}

	return perPodSet, nil
}

// determineCapacityForPodSet handles iterating ResourceClaims and Requests
// for one PodSet and returns the aggregated capacity charges.
func determineCapacityForPodSet(
	ctx context.Context,
	cl client.Client,
	sliceCache *ResourceSliceCache,
	mapper *ResourceMapper,
	ps *kueue.PodSet,
	wlNamespace string,
	classCache map[string]*resourcev1.DeviceClass,
	psIdx int,
) (corev1.ResourceList, field.ErrorList) {
	log := ctrl.LoggerFrom(ctx)
	aggregated := corev1.ResourceList{}

	for j, prc := range ps.Template.Spec.ResourceClaims {
		if prc.ResourceClaimTemplateName == nil {
			continue
		}
		spec, err := getClaimSpec(ctx, cl, wlNamespace, prc)
		if err != nil {
			return nil, field.ErrorList{field.InternalError(
				field.NewPath("spec", "podSets").Index(psIdx).Child("template", "spec", "resourceClaims").Index(j),
				fmt.Errorf("failed to get claim spec for ResourceClaimTemplate %s: %w", *prc.ResourceClaimTemplateName, err),
			)}
		}
		if spec == nil {
			log.V(2).Info("Unexpected nil spec after ResourceClaimTemplateName check", "claimIndex", j)
			continue
		}

		for reqIdx, req := range spec.Devices.Requests {
			if req.Exactly == nil {
				continue
			}
			deviceClass := corev1.ResourceName(req.Exactly.DeviceClassName)
			capacityConfigs := mapper.getCapacityConfigs(deviceClass)
			if len(capacityConfigs) == 0 {
				log.V(4).Info("No capacity configs for DeviceClass, skipping", "deviceClass", deviceClass)
				continue
			}

			quotaResource, found := mapper.Lookup(deviceClass)
			if !found {
				log.V(2).Info("Capacity configs exist but no quota resource mapping found", "deviceClass", deviceClass)
				continue
			}

			reqPath := field.NewPath("spec", "podSets").Index(psIdx).Child("template", "spec", "resourceClaims").Index(j).Child("devices", "requests").Index(reqIdx)

			classSelectors, requestSelectors, errs := prepareCounterCharge(ctx, cl, req.Exactly.DeviceClassName, req.Exactly.Selectors, classCache, reqPath)
			if len(errs) > 0 {
				return nil, errs
			}

			log.V(4).Info("Processing capacity charge", "podSet", ps.Name, "deviceClass", deviceClass, "quotaResource", quotaResource, "count", req.Exactly.Count)

			charges, errs := processCapacityCharge(ctx, sliceCache, capacityConfigs, quotaResource, req.Exactly, classSelectors, requestSelectors, reqPath)
			if len(errs) > 0 {
				return nil, errs
			}
			for resName, qty := range charges {
				existing := aggregated[resName]
				existing.Add(qty)
				aggregated[resName] = existing
			}
			log.V(4).Info("Capacity charge computed", "podSet", ps.Name, "deviceClass", deviceClass, "charges", charges)
		}
	}

	return aggregated, nil
}

// processCapacityCharge iterates capacity configs, matches devices, and computes
// charges for a single device request.
func processCapacityCharge(
	ctx context.Context,
	sliceCache *ResourceSliceCache,
	capacityConfigs []deviceClassCapacityConfig,
	quotaResource corev1.ResourceName,
	exactly *resourcev1.ExactDeviceRequest,
	classSelectors []dracel.CompilationResult,
	requestSelectors []dracel.CompilationResult,
	reqPath *field.Path,
) (corev1.ResourceList, field.ErrorList) {
	log := ctrl.LoggerFrom(ctx)
	result := corev1.ResourceList{}
	count := exactly.Count

	for _, capCfg := range capacityConfigs {
		matched, errs := matchDevicesForSource(ctx, sliceCache, DriverReference(capCfg.driver), capCfg.deviceSelector, classSelectors, requestSelectors, reqPath)
		if len(errs) > 0 {
			return nil, errs
		}
		log.V(4).Info("Matched devices for capacity charge", "matched", len(matched), "requested", count, "resource", capCfg.resourceName)

		if int64(len(matched)) < count {
			return nil, field.ErrorList{field.InternalError(
				reqPath,
				fmt.Errorf("insufficient matching devices for capacity driver: %d device(s) match but %d requested",
					len(matched), count),
			)}
		}

		// Resolve the explicit capacity request from the claim, if any.
		var explicitRequest *resource.Quantity
		if exactly.Capacity != nil {
			if reqVal, ok := exactly.Capacity.Requests[capCfg.resourceName]; ok {
				explicitRequest = &reqVal
			}
		}

		charges, errs := computeCapacityCharge(log, &capCfg, matched, count, explicitRequest, reqPath)
		if len(errs) > 0 {
			return nil, errs
		}
		if charges != nil {
			existing := result[quotaResource]
			existing.Add(*charges)
			result[quotaResource] = existing
		}
	}

	return result, nil
}

// computeCapacityCharge computes the quota charge for a single capacity config.
// For each matched device with the capacity dimension, it determines the base
// charge (explicit request, device Default, or full capacity), rounds it against
// the device's own RequestPolicy, and takes the maximum across all devices.
func computeCapacityCharge(
	log logr.Logger,
	capCfg *deviceClassCapacityConfig,
	matched []resourcev1.Device,
	count int64,
	explicitRequest *resource.Quantity,
	reqPath *field.Path,
) (*resource.Quantity, field.ErrorList) {
	var maxRounded resource.Quantity
	hasDimension := false
	hasValidCharge := false

	for i := range matched {
		dev := &matched[i]
		capDim, ok := dev.Capacity[capCfg.resourceName]
		if !ok {
			log.V(4).Info("Device has no capacity dimension, skipping",
				"device", dev.Name, "dimension", capCfg.resourceName)
			continue
		}
		hasDimension = true

		rounded, err := chargeForDevice(capDim, explicitRequest)
		if err != nil {
			log.V(4).Info("Device policy cannot satisfy request, skipping",
				"device", dev.Name, "dimension", capCfg.resourceName, "error", err)
			continue
		}

		if !hasValidCharge || rounded.Cmp(maxRounded) > 0 {
			maxRounded = rounded
		}
		hasValidCharge = true
	}

	if !hasDimension {
		return nil, field.ErrorList{field.InternalError(
			reqPath,
			fmt.Errorf("matched devices have no capacity dimension %q", capCfg.resourceName),
		)}
	}
	if !hasValidCharge {
		return nil, field.ErrorList{field.Invalid(
			reqPath, nil,
			fmt.Sprintf("capacity request cannot be satisfied by any matched device's RequestPolicy for dimension %q", capCfg.resourceName),
		)}
	}

	chargeValue := safeChargeValue(log, maxRounded, capCfg.driver, string(capCfg.resourceName), count)
	result := resource.NewQuantity(chargeValue, maxRounded.Format)
	return result, nil
}

// chargeForDevice determines the base charge for a single device and rounds
// it against the device's own RequestPolicy.
func chargeForDevice(capDim resourcev1.DeviceCapacity, explicitRequest *resource.Quantity) (resource.Quantity, error) {
	var base resource.Quantity
	switch {
	case explicitRequest != nil:
		base = explicitRequest.DeepCopy()
	case capDim.RequestPolicy != nil && capDim.RequestPolicy.Default != nil:
		base = capDim.RequestPolicy.Default.DeepCopy()
	default:
		base = capDim.Value.DeepCopy()
	}
	return roundToPolicy(base, capDim.RequestPolicy)
}

// roundToPolicy rounds a base charge according to a CapacityRequestPolicy.
// If the policy is nil, the value is returned as-is.
// ValidValues is checked first; if not set, ValidRange is used.
func roundToPolicy(base resource.Quantity, policy *resourcev1.CapacityRequestPolicy) (resource.Quantity, error) {
	if policy == nil {
		return base, nil
	}
	if len(policy.ValidValues) > 0 {
		return roundToValidValues(base, policy.ValidValues)
	}
	if policy.ValidRange != nil {
		return roundToValidRange(base, policy.ValidRange)
	}
	return base, nil
}

// roundToValidValues rounds a base charge up to the smallest valid value
// that is >= the base. Returns an error if the base exceeds all valid values.
// ValidValues is assumed to be sorted in ascending order per the API spec.
func roundToValidValues(base resource.Quantity, validValues []resource.Quantity) (resource.Quantity, error) {
	for _, v := range validValues {
		if v.Cmp(base) >= 0 {
			return v.DeepCopy(), nil
		}
	}
	return resource.Quantity{}, fmt.Errorf("request %s exceeds all valid values", base.String())
}

// roundToValidRange clamps a base charge to the range [Min, Max] and aligns
// to Min + n*Step if Step is set. Returns an error if the (possibly rounded)
// value exceeds Max.
func roundToValidRange(base resource.Quantity, vr *resourcev1.CapacityRequestPolicyRange) (resource.Quantity, error) {
	val := utilmath.SafeValue(base)

	// Clamp to Min.
	if vr.Min != nil {
		val = max(val, utilmath.SafeValue(*vr.Min))
	}

	// Align to Min + n*Step. Skip alignment when Min is nil since the
	// formula Min + n*Step is undefined without a base.
	if vr.Step != nil && vr.Min != nil {
		stepVal := utilmath.SafeValue(*vr.Step)
		if stepVal > 0 {
			minVal := utilmath.SafeValue(*vr.Min)
			offset := utilmath.SaturatingSub(val, minVal)
			if offset%stepVal != 0 {
				val = utilmath.SaturatingAdd(minVal, utilmath.SaturatingMul(utilmath.SaturatingAdd(offset/stepVal, 1), stepVal))
			}
		}
	}

	// Check Max.
	if vr.Max != nil {
		maxVal := utilmath.SafeValue(*vr.Max)
		if val > maxVal {
			return resource.Quantity{}, fmt.Errorf("rounded value %d exceeds max %d", val, maxVal)
		}
	}

	return *resource.NewQuantity(val, base.Format), nil
}
