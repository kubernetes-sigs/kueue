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
	"sync"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
)

const (
	DefaultDRAConfigName = "default"
)

var (
	globalMapper     *ResourceMapper
	globalMapperOnce sync.Once
)

// ResourceMapper provides thread-safe device class to logical resource name mapping
// based on DynamicResourceAllocationConfig custom resource.
type ResourceMapper struct {
	mu sync.RWMutex
	// internally we use corev1.ResourceName for device class and logical resource name to shield
	// the implementation from API changes
	deviceClassToResource map[corev1.ResourceName]corev1.ResourceName
}

// newDRAResourceMapper creates a new empty ResourceMapper instance.
func newDRAResourceMapper() *ResourceMapper {
	return &ResourceMapper{
		deviceClassToResource: make(map[corev1.ResourceName]corev1.ResourceName),
	}
}

// loadFromConfig loads the device class mappings from a DynamicResourceAllocationConfig CR
// using the provided Kubernetes client. It fetches the singleton "default" config.
func (m *ResourceMapper) loadFromConfig(ctx context.Context, cl client.Client) error {
	var config kueuealpha.DynamicResourceAllocationConfig
	err := cl.Get(ctx, types.NamespacedName{Name: DefaultDRAConfigName}, &config)
	if err != nil {
		return err
	}

	return m.updateFromConfig(ctx, &config)
}

// lookup performs thread-safe device class to logical resource name conversion.
// Returns (logicalResourceName, true) on success or ("", false) when the device class is not mapped.
func (m *ResourceMapper) lookup(deviceClass corev1.ResourceName) (corev1.ResourceName, bool) {
	m.mu.RLock()
	defer m.mu.RUnlock()

	logicalResource, found := m.deviceClassToResource[deviceClass]
	return logicalResource, found
}

// updateFromConfig updates the internal mapping from a DynamicResourceAllocationConfig.
// This method is thread-safe and rebuilds the entire mapping from scratch to avoid stale entries.
func (m *ResourceMapper) updateFromConfig(ctx context.Context, draConfig *kueuealpha.DynamicResourceAllocationConfig) error {
	if draConfig == nil {
		return nil
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	// Rebuild from scratch to avoid stale entries
	newMapping := make(map[corev1.ResourceName]corev1.ResourceName)

	for _, resource := range draConfig.Spec.Resources {
		for _, deviceClassName := range resource.DeviceClassNames {
			newMapping[corev1.ResourceName(deviceClassName)] = corev1.ResourceName(resource.Name)
		}
	}

	m.deviceClassToResource = newMapping
	return nil
}

// Mapper returns the singleton DRA mapper instance initializing the mapper lazily on first access.
func Mapper() *ResourceMapper {
	globalMapperOnce.Do(func() {
		globalMapper = newDRAResourceMapper()
	})
	return globalMapper
}

// UpdateMapperFromConfig updates the global DRA mapper from a DynamicResourceAllocationConfig.
// This is typically called by the DRA controller when the config changes.
func UpdateMapperFromConfig(ctx context.Context, config *kueuealpha.DynamicResourceAllocationConfig) error {
	mapper := Mapper()
	return mapper.updateFromConfig(ctx, config)
}

// LookupResourceFor performs a device class lookup using the global DRA mapper.
// Returns the logical resource name and true if found, empty string and false otherwise.
func LookupResourceFor(deviceClass corev1.ResourceName) (corev1.ResourceName, bool) {
	mapper := Mapper()
	return mapper.lookup(deviceClass)
}
