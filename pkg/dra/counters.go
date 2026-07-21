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

func GetCounterResourcesForWorkload(
	ctx context.Context,
	cl client.Client,
	sliceCache *ResourceSliceCache,
	mapper *ResourceMapper,
	wl *kueue.Workload,
) (map[kueue.PodSetReference]corev1.ResourceList, field.ErrorList) {
	log := ctrl.LoggerFrom(ctx)
	log.V(3).Info("Processing counter resources for workload")

	perPodSet := make(map[kueue.PodSetReference]corev1.ResourceList)
	var allErrs field.ErrorList
	classCache := make(map[string]*resourcev1.DeviceClass)

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
					fmt.Errorf("failed to get claim spec for ResourceClaimTemplate %s: %w", *prc.ResourceClaimTemplateName, err),
				))
				return nil, allErrs
			}
			if spec == nil {
				continue
			}

			for reqIdx, req := range spec.Devices.Requests {
				if req.Exactly == nil {
					continue
				}
				deviceClass := corev1.ResourceName(req.Exactly.DeviceClassName)
				counterConfigs := mapper.getCounterConfigs(deviceClass)
				if len(counterConfigs) == 0 {
					continue
				}

				reqPath := field.NewPath("spec", "podSets").Index(i).Child("template", "spec", "resourceClaims").Index(j).Child("devices", "requests").Index(reqIdx)

				classSelectors, requestSelectors, errs := prepareCounterCharge(ctx, cl, req.Exactly.DeviceClassName, req.Exactly.Selectors, classCache, reqPath)
				if len(errs) > 0 {
					allErrs = append(allErrs, errs...)
					return nil, allErrs
				}

				for _, cc := range counterConfigs {
					log.V(4).Info("Processing counter charge", "podSet", ps.Name, "deviceClass", deviceClass, "quotaResource", cc.quotaResource, "count", req.Exactly.Count)

					charges, errs := processCounterCharge(ctx, sliceCache, &cc, cc.quotaResource, classSelectors, requestSelectors, req.Exactly.Count, reqPath)
					if len(errs) > 0 {
						allErrs = append(allErrs, errs...)
						return nil, allErrs
					}
					for resName, qty := range charges {
						existing := aggregated[resName]
						existing.Add(qty)
						aggregated[resName] = existing
					}
					log.V(4).Info("Counter charge computed", "podSet", ps.Name, "deviceClass", deviceClass, "charges", charges)
				}
			}
		}

		if len(aggregated) > 0 {
			perPodSet[ps.Name] = aggregated
			log.V(3).Info("Counter resources aggregated for podSet", "podSet", ps.Name, "resources", aggregated)
		}
	}

	return perPodSet, nil
}

func prepareCounterCharge(
	ctx context.Context,
	cl client.Client,
	deviceClassName string,
	selectors []resourcev1.DeviceSelector,
	classCache map[string]*resourcev1.DeviceClass,
	reqPath *field.Path,
) ([]dracel.CompilationResult, []dracel.CompilationResult, field.ErrorList) {
	classSelectors, classErr := resolveAndCompileDeviceClass(ctx, cl, deviceClassName, classCache, reqPath, 0)
	if classErr != nil {
		return nil, nil, field.ErrorList{classErr}
	}

	requestSelectors, compErrs := compileCELSelectors(selectors, reqPath.Child("exactly", "selectors"), "CEL compilation failed")
	if len(compErrs) > 0 {
		return nil, nil, compErrs
	}

	return classSelectors, requestSelectors, nil
}

// matchDevicesForSource compiles a source config's deviceSelector, lists
// ResourceSlices for its driver, and returns matching devices.
func matchDevicesForSource(
	ctx context.Context,
	sliceCache *ResourceSliceCache,
	driver DriverReference,
	deviceSelector resourcev1.DeviceSelector,
	classSelectors []dracel.CompilationResult,
	requestSelectors []dracel.CompilationResult,
	reqPath *field.Path,
) ([]resourcev1.Device, field.ErrorList) {
	log := ctrl.LoggerFrom(ctx)

	selectorSelectors, compErrs := compileCELSelectors(
		[]resourcev1.DeviceSelector{deviceSelector},
		reqPath, "deviceSelector CEL compilation failed",
	)
	if len(compErrs) > 0 {
		return nil, compErrs
	}

	sliceList, err := sliceCache.ListByDriver(ctx, driver)
	if err != nil {
		return nil, field.ErrorList{field.InternalError(reqPath, fmt.Errorf("failed to list ResourceSlices: %w", err))}
	}
	pools := groupSlicesByPool(sliceList, string(driver))
	log.V(4).Info("Listed ResourceSlices for device matching", "driver", driver, "pools", len(pools), "slices", len(sliceList))

	return matchDevicesWithSelectors(ctx, pools, string(driver), selectorSelectors, classSelectors, requestSelectors, reqPath)
}

// safeChargeValue converts a driver-published resource.Quantity to a safe int64
// charge value. Negative values are clamped to 0 with a log message. If count > 1
// the value is multiplied using saturating arithmetic.
func safeChargeValue(log logr.Logger, value resource.Quantity, driver, dimension string, count int64) int64 {
	intVal := utilmath.SafeValue(value)
	if value.Sign() < 0 {
		log.V(0).Info("Unexpected negative device value from driver, clamping to 0",
			"driver", driver, "dimension", dimension, "value", value.String())
		intVal = 0
	}
	if count > 1 {
		intVal = utilmath.SaturatingMul(intVal, count)
	}
	return intVal
}

func processCounterCharge(
	ctx context.Context,
	sliceCache *ResourceSliceCache,
	counterConfig *deviceClassCounterConfig,
	quotaResource corev1.ResourceName,
	classSelectors []dracel.CompilationResult,
	requestSelectors []dracel.CompilationResult,
	count int64,
	reqPath *field.Path,
) (corev1.ResourceList, field.ErrorList) {
	log := ctrl.LoggerFrom(ctx)

	matched, errs := matchDevicesForSource(ctx, sliceCache, counterConfig.driver, counterConfig.deviceSelector, classSelectors, requestSelectors, reqPath)
	if len(errs) > 0 {
		return nil, errs
	}
	log.V(4).Info("Matched devices for counter charge", "matched", len(matched), "requested", count)

	if int64(len(matched)) < count {
		// Cluster-state shortage: retryable until matching ResourceSlices appear.
		return nil, field.ErrorList{field.InternalError(
			reqPath,
			fmt.Errorf("insufficient matching devices for counter driver: %d device(s) match but %d requested",
				len(matched), count),
		)}
	}

	charges := computeCounterCharges(log, counterConfig, quotaResource, matched, count)
	if len(charges) > 0 {
		return charges, nil
	}

	// No consumesCounters yet on the matched devices: another cluster-state
	// condition that can clear as ResourceSlices are updated, so retry with backoff.
	return nil, field.ErrorList{field.InternalError(
		reqPath,
		fmt.Errorf("matched devices have no consumesCounters entry for counter %q", counterConfig.counterName),
	)}
}

func matchDevicesWithSelectors(
	ctx context.Context,
	pools map[string]*poolInfo,
	driver string,
	selectorSelectors []dracel.CompilationResult,
	classSelectors []dracel.CompilationResult,
	requestSelectors []dracel.CompilationResult,
	reqPath *field.Path,
) ([]resourcev1.Device, field.ErrorList) {
	log := ctrl.LoggerFrom(ctx)
	var allMatched []resourcev1.Device
	for poolName, pool := range pools {
		if !pool.isComplete() {
			log.V(4).Info("Skipping incomplete pool", "pool", poolName)
			continue
		}
		devices := pool.collectDevices()
		log.V(4).Info("Evaluating devices in pool", "pool", poolName, "deviceCount", len(devices))
		for i := range devices {
			dev := &devices[i]
			celDev := dracel.Device{
				Driver:     driver,
				Attributes: dev.Attributes,
				Capacity:   dev.Capacity,
			}
			if selectorMatch, err := evaluateSelectorsOnDevice(ctx, selectorSelectors, celDev); !selectorMatch {
				if err != nil {
					return nil, field.ErrorList{field.InternalError(reqPath, fmt.Errorf("CEL evaluation error in deviceSelector for device %s: %w", dev.Name, err))}
				}
				continue
			}
			if classMatch, err := evaluateSelectorsOnDevice(ctx, classSelectors, celDev); !classMatch {
				if err != nil {
					return nil, field.ErrorList{field.InternalError(reqPath, fmt.Errorf("CEL evaluation error in DeviceClass selector for device %s: %w", dev.Name, err))}
				}
				continue
			}
			if requestMatch, err := evaluateSelectorsOnDevice(ctx, requestSelectors, celDev); requestMatch {
				allMatched = append(allMatched, *dev)
			} else if err != nil {
				return nil, field.ErrorList{field.InternalError(reqPath, fmt.Errorf("CEL evaluation error in request selector for device %s: %w", dev.Name, err))}
			}
		}
	}

	return allMatched, nil
}

type poolInfo struct {
	name               string
	generation         int64
	resourceSliceCount int64
	slices             []resourcev1.ResourceSlice
}

func (p *poolInfo) isComplete() bool {
	return int64(len(p.slices)) == p.resourceSliceCount
}

func (p *poolInfo) collectDevices() []resourcev1.Device {
	var devices []resourcev1.Device
	for i := range p.slices {
		devices = append(devices, p.slices[i].Spec.Devices...)
	}
	return devices
}

func groupSlicesByPool(slices []resourcev1.ResourceSlice, driver string) map[string]*poolInfo {
	pools := make(map[string]*poolInfo)

	for i := range slices {
		slice := &slices[i]
		if slice.Spec.Driver != driver {
			continue
		}

		poolName := slice.Spec.Pool.Name
		gen := slice.Spec.Pool.Generation

		existing, ok := pools[poolName]
		if !ok {
			pools[poolName] = &poolInfo{
				name:               poolName,
				generation:         gen,
				resourceSliceCount: slice.Spec.Pool.ResourceSliceCount,
				slices:             []resourcev1.ResourceSlice{*slice},
			}
			continue
		}

		if gen > existing.generation {
			pools[poolName] = &poolInfo{
				name:               poolName,
				generation:         gen,
				resourceSliceCount: slice.Spec.Pool.ResourceSliceCount,
				slices:             []resourcev1.ResourceSlice{*slice},
			}
		} else if gen == existing.generation {
			existing.slices = append(existing.slices, *slice)
		}
	}

	return pools
}

func computeCounterCharges(
	log logr.Logger,
	counterConfig *deviceClassCounterConfig,
	quotaResource corev1.ResourceName,
	matched []resourcev1.Device,
	count int64,
) corev1.ResourceList {
	var maxValue resource.Quantity
	found := false

	for i := range matched {
		dev := &matched[i]
		for _, consumption := range dev.ConsumesCounters {
			if v, ok := consumption.Counters[counterConfig.counterName]; ok {
				if !found || v.Value.Cmp(maxValue) > 0 {
					maxValue = v.Value.DeepCopy()
					found = true
				}
			}
		}
	}

	if !found {
		return nil
	}

	intVal := safeChargeValue(log, maxValue, string(counterConfig.driver), counterConfig.counterName, count)
	return corev1.ResourceList{quotaResource: *resource.NewQuantity(intVal, maxValue.Format)}
}
