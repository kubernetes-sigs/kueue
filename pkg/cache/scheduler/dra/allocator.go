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
	"sync"

	resourceapi "k8s.io/api/resource/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	"k8s.io/apimachinery/pkg/util/sets"
	utilfeature "k8s.io/apiserver/pkg/util/feature"
	dracel "k8s.io/dynamic-resource-allocation/cel"
	"k8s.io/dynamic-resource-allocation/structured"
	"k8s.io/kubernetes/pkg/scheduler/framework/plugins/dynamicresources"
	schedulerfeature "k8s.io/kubernetes/pkg/scheduler/framework/plugins/feature"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// CELCache holds the compiled device selectors, keyed by the expression, so one survives
// the Checker that compiled it and is compiled once for the process rather than once
// per scheduling cycle. The zero value is ready to use and safe to share.
type CELCache struct {
	once  sync.Once
	value *dracel.Cache
}

func (c *CELCache) get() *dracel.Cache {
	c.once.Do(func() {
		// Read on first use, not at construction, so the gates are parsed by now.
		fts := schedulerfeature.NewSchedulerFeaturesFromGates(utilfeature.DefaultFeatureGate)
		// The same size kube-scheduler's dynamicresources plugin uses.
		c.value = dracel.NewCache(10, dracel.Features{
			EnableConsumableCapacity: fts.EnableDRAConsumableCapacity,
			EnableListTypeAttributes: fts.EnableDRAListTypeAttributes,
		})
	})
	return c.value
}

// lazyAllocator builds the snapshot's allocator on first use. A Pod's claims go to
// Allocate rather than the constructor, so one allocator serves the whole snapshot
// and the device state cannot shift mid-cycle.
type lazyAllocator struct {
	build func(context.Context) (structured.Allocator, error)
	once  sync.Once
	value structured.Allocator
	err   error
}

func (l *lazyAllocator) get(ctx context.Context) (structured.Allocator, error) {
	l.once.Do(func() {
		l.value, l.err = l.build(ctx)
	})
	return l.value, l.err
}

func (c *Checker) buildAllocator(ctx context.Context) (structured.Allocator, error) {
	var sliceList resourceapi.ResourceSliceList
	if err := c.cl.List(ctx, &sliceList); err != nil {
		return nil, fmt.Errorf("listing ResourceSlices: %w", err)
	}
	deviceSlices := make([]*resourceapi.ResourceSlice, len(sliceList.Items))
	for i := range sliceList.Items {
		deviceSlices[i] = &sliceList.Items[i]
	}

	deviceSlices, err := applyDeviceTaintRules(ctx, c.cl, c.deviceTaintRules, deviceSlices)
	if err != nil {
		return nil, err
	}

	// Configured from the Kubernetes DRA gates rather than the Kueue ones, by the same
	// call kube-scheduler makes, so the two allocators stay in step.
	draFeatures := dynamicresources.AllocatorFeatures(schedulerfeature.NewSchedulerFeaturesFromGates(utilfeature.DefaultFeatureGate))

	// The allocated state is read with the allocator's own setting rather than the gate, so
	// the two cannot disagree on whether a shared device is partly or wholly consumed.
	allocatedState, err := buildAllocatedState(ctx, c.cl, draFeatures.ConsumableCapacity)
	if err != nil {
		return nil, fmt.Errorf("building allocated device state: %w", err)
	}

	classLister, err := newDeviceClassCache(ctx, c.cl)
	if err != nil {
		return nil, fmt.Errorf("listing DeviceClasses: %w", err)
	}

	allocator, err := structured.NewAllocator(ctx, draFeatures, allocatedState, classLister, deviceSlices, c.celCache.get())
	if err != nil {
		return nil, fmt.Errorf("creating DRA allocator: %w", err)
	}
	return allocator, nil
}

func buildAllocatedState(ctx context.Context, cl client.Client, consumableCapacity bool) (structured.AllocatedState, error) {
	allocatedDevices := sets.New[structured.DeviceID]()
	allocatedSharedDeviceIDs := sets.New[structured.SharedDeviceID]()
	aggregatedCapacity := structured.NewConsumedCapacityCollection()
	var claims resourceapi.ResourceClaimList
	if err := cl.List(ctx, &claims); err != nil {
		return structured.AllocatedState{}, fmt.Errorf("listing ResourceClaims: %w", err)
	}

	for i := range claims.Items {
		claim := &claims.Items[i]
		if claim.Status.Allocation == nil {
			continue
		}
		for _, result := range claim.Status.Allocation.Devices.Results {
			if ptr.Deref(result.AdminAccess, false) {
				continue
			}
			deviceID := structured.MakeDeviceID(result.Driver, result.Pool, result.Device)
			if consumableCapacity && result.ShareID != nil {
				sharedID := structured.MakeSharedDeviceID(deviceID, result.ShareID)
				allocatedSharedDeviceIDs.Insert(sharedID)
				if result.ConsumedCapacity != nil {
					cc := structured.NewDeviceConsumedCapacity(deviceID, result.ConsumedCapacity)
					aggregatedCapacity.Insert(cc)
				}
				continue
			}
			allocatedDevices.Insert(deviceID)
		}
	}

	return structured.AllocatedState{
		AllocatedDevices:         allocatedDevices,
		AllocatedSharedDeviceIDs: allocatedSharedDeviceIDs,
		AggregatedCapacity:       aggregatedCapacity,
	}, nil
}

// deviceClassCache serves the DeviceClasses read when the allocator was built. The
// upstream lister takes no context yet is called during allocation, so every class is
// resolved up front.
type deviceClassCache struct {
	all    []*resourceapi.DeviceClass
	byName map[string]*resourceapi.DeviceClass
}

func newDeviceClassCache(ctx context.Context, cl client.Client) (*deviceClassCache, error) {
	var deviceClasses resourceapi.DeviceClassList
	if err := cl.List(ctx, &deviceClasses); err != nil {
		return nil, err
	}
	cache := &deviceClassCache{
		all:    make([]*resourceapi.DeviceClass, len(deviceClasses.Items)),
		byName: make(map[string]*resourceapi.DeviceClass, len(deviceClasses.Items)),
	}
	for i := range deviceClasses.Items {
		dc := &deviceClasses.Items[i]
		cache.all[i] = dc
		cache.byName[dc.Name] = dc
	}
	return cache, nil
}

func (l *deviceClassCache) List() ([]*resourceapi.DeviceClass, error) {
	return l.all, nil
}

func (l *deviceClassCache) Get(className string) (*resourceapi.DeviceClass, error) {
	dc, found := l.byName[className]
	if !found {
		return nil, apierrors.NewNotFound(resourceapi.Resource("deviceclasses"), className)
	}
	return dc, nil
}
