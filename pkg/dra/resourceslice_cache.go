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

	resourcev1 "k8s.io/api/resource/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// DriverReference is the name of a DRA driver as it appears in
// ResourceSlice.Spec.Driver (e.g., "gpu.nvidia.com").
type DriverReference string

const allDriversKey DriverReference = ""

// ResourceSliceCache is a request-scoped cache for ResourceSlice listings.
// It is created once per workload reconciliation and passed to both the CEL
// validation and counter processing paths to avoid duplicate API calls and
// ensure a consistent snapshot of ResourceSlices within a single reconciliation.
type ResourceSliceCache struct {
	client client.Client
	// cachedSlices maps a DriverReference to its cached ResourceSlice listing.
	// The sentinel key allDriversKey ("") stores the result of an unfiltered
	// listing used as a fallback for DeviceClasses without counter sources.
	cachedSlices map[DriverReference][]resourcev1.ResourceSlice
}

// NewResourceSliceCache creates a new ResourceSliceCache.
func NewResourceSliceCache(cl client.Client) *ResourceSliceCache {
	return &ResourceSliceCache{
		client:       cl,
		cachedSlices: make(map[DriverReference][]resourcev1.ResourceSlice),
	}
}

// ListByDriver returns ResourceSlices for the given driver, using the spec.driver
// field index for efficient listing. Results are cached for subsequent calls with
// the same driver within the same reconciliation.
func (c *ResourceSliceCache) ListByDriver(ctx context.Context, driver DriverReference) ([]resourcev1.ResourceSlice, error) {
	if slices, ok := c.cachedSlices[driver]; ok {
		return slices, nil
	}
	if allSlices, ok := c.cachedSlices[allDriversKey]; ok {
		var filtered []resourcev1.ResourceSlice
		for i := range allSlices {
			if DriverReference(allSlices[i].Spec.Driver) == driver {
				filtered = append(filtered, allSlices[i])
			}
		}
		c.cachedSlices[driver] = filtered
		return filtered, nil
	}
	var sliceList resourcev1.ResourceSliceList
	if err := c.client.List(ctx, &sliceList, client.MatchingFields{"spec.driver": string(driver)}); err != nil {
		return nil, err
	}
	c.cachedSlices[driver] = sliceList.Items
	return sliceList.Items, nil
}

// ListAll returns all ResourceSlices in the cluster without filtering.
// Used as a fallback when the driver is not known (e.g., whole-device
// DeviceClasses without counter sources). Results are cached for subsequent
// calls within the same reconciliation.
func (c *ResourceSliceCache) ListAll(ctx context.Context) ([]resourcev1.ResourceSlice, error) {
	if slices, ok := c.cachedSlices[allDriversKey]; ok {
		return slices, nil
	}
	var sliceList resourcev1.ResourceSliceList
	if err := c.client.List(ctx, &sliceList); err != nil {
		return nil, err
	}
	c.cachedSlices[allDriversKey] = sliceList.Items
	return sliceList.Items, nil
}
