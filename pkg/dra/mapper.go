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
	"slices"

	corev1 "k8s.io/api/core/v1"
	resourcev1 "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/util/sets"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta2"
)

// deviceClassCounterConfig holds counter configuration for a specific DeviceClass.
type deviceClassCounterConfig struct {
	quotaResource  corev1.ResourceName
	driver         string
	counterName    string
	deviceSelector resourcev1.DeviceSelector
}

// ResourceMapper provides device class to logical resource name mapping
// based on Configuration API DRA settings. Initialized once at startup, immutable during runtime.
type ResourceMapper struct {
	deviceClassToResource map[corev1.ResourceName]corev1.ResourceName
	deviceClassCounters   map[corev1.ResourceName][]deviceClassCounterConfig
}

// NewResourceMapper creates a new empty ResourceMapper instance.
func NewResourceMapper() *ResourceMapper {
	return &ResourceMapper{
		deviceClassToResource: make(map[corev1.ResourceName]corev1.ResourceName),
		deviceClassCounters:   make(map[corev1.ResourceName][]deviceClassCounterConfig),
	}
}

// Lookup returns the logical resource name for a device class.
// For DeviceClasses with counter sources, the quota resource name is on each
// counter config instead.
func (m *ResourceMapper) Lookup(deviceClass corev1.ResourceName) (corev1.ResourceName, bool) {
	if m == nil {
		return "", false
	}
	logicalResource, found := m.deviceClassToResource[deviceClass]
	return logicalResource, found
}

// getCounterConfigs returns the counter configurations for a DeviceClass, or nil if
// the DeviceClass does not use counter-based quota.
func (m *ResourceMapper) getCounterConfigs(deviceClass corev1.ResourceName) []deviceClassCounterConfig {
	return m.deviceClassCounters[deviceClass]
}

// CounterBasedResourceNames returns the quota resources configured with a
// counter source.
func (m *ResourceMapper) CounterBasedResourceNames() []corev1.ResourceName {
	if m == nil {
		return nil
	}
	resourceNames := sets.New[corev1.ResourceName]()
	for _, configs := range m.deviceClassCounters {
		for _, config := range configs {
			resourceNames.Insert(config.quotaResource)
		}
	}
	result := resourceNames.UnsortedList()
	slices.Sort(result)
	return result
}

// PopulateFromConfiguration populates the mapper from Configuration API device class mappings.
func (m *ResourceMapper) PopulateFromConfiguration(mappings []configapi.DeviceClassMapping) error {
	if mappings == nil {
		return nil
	}
	dcToResource := make(map[corev1.ResourceName]corev1.ResourceName)
	dcCounters := make(map[corev1.ResourceName][]deviceClassCounterConfig)
	for _, mapping := range mappings {
		isCounter := len(mapping.Sources) > 0 && mapping.Sources[0].Counter != nil
		for _, deviceClassName := range mapping.DeviceClassNames {
			if _, exists := dcToResource[deviceClassName]; !exists {
				dcToResource[deviceClassName] = mapping.Name
			}
			if isCounter {
				c := mapping.Sources[0].Counter
				dcCounters[deviceClassName] = append(dcCounters[deviceClassName], deviceClassCounterConfig{
					quotaResource:  mapping.Name,
					driver:         c.Driver,
					counterName:    c.Name,
					deviceSelector: c.DeviceSelector,
				})
			}
		}
	}
	m.deviceClassToResource = dcToResource
	m.deviceClassCounters = dcCounters
	return nil
}
