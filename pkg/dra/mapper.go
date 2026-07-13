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
	corev1 "k8s.io/api/core/v1"
	resourcev1 "k8s.io/api/resource/v1"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta2"
	"sigs.k8s.io/kueue/pkg/resources"
)

// deviceClassCounterConfig holds counter configuration for a specific DeviceClass.
type deviceClassCounterConfig struct {
	driver         string
	counterName    string
	deviceSelector resourcev1.DeviceSelector
}

// ResourceMapper provides device class to logical resource name mapping
// based on Configuration API DRA settings. Initialized once at startup, immutable during runtime.
type ResourceMapper struct {
	deviceClassToResource map[corev1.ResourceName]corev1.ResourceName
	deviceClassCounters   map[corev1.ResourceName]*deviceClassCounterConfig
}

// NewResourceMapper creates a new empty ResourceMapper instance.
func NewResourceMapper() *ResourceMapper {
	return &ResourceMapper{
		deviceClassToResource: make(map[corev1.ResourceName]corev1.ResourceName),
		deviceClassCounters:   make(map[corev1.ResourceName]*deviceClassCounterConfig),
	}
}

// Lookup returns the logical resource name for a device class.
func (m *ResourceMapper) Lookup(deviceClass corev1.ResourceName) (corev1.ResourceName, bool) {
	if m == nil {
		return "", false
	}
	logicalResource, found := m.deviceClassToResource[deviceClass]
	return logicalResource, found
}

// getCounterConfig returns the counter configuration for a DeviceClass, or nil if
// the DeviceClass does not use counter-based quota.
func (m *ResourceMapper) getCounterConfig(deviceClass corev1.ResourceName) *deviceClassCounterConfig {
	return m.deviceClassCounters[deviceClass]
}

// PopulateFromConfiguration populates the mapper from Configuration API device class mappings.
func (m *ResourceMapper) PopulateFromConfiguration(mappings []configapi.DeviceClassMapping) error {
	if mappings == nil {
		return nil
	}
	dcToResource := make(map[corev1.ResourceName]corev1.ResourceName)
	dcCounters := make(map[corev1.ResourceName]*deviceClassCounterConfig)
	for _, mapping := range mappings {
		if len(mapping.Sources) > 0 && mapping.Sources[0].Counter != nil {
			resources.RegisterBinaryFormattedResource(mapping.Name)
		}
		for _, deviceClassName := range mapping.DeviceClassNames {
			dcToResource[deviceClassName] = mapping.Name
			if len(mapping.Sources) > 0 && mapping.Sources[0].Counter != nil {
				c := mapping.Sources[0].Counter
				dcCounters[deviceClassName] = &deviceClassCounterConfig{
					driver:         c.Driver,
					counterName:    c.Name,
					deviceSelector: c.DeviceSelector,
				}
			}
		}
	}
	m.deviceClassToResource = dcToResource
	m.deviceClassCounters = dcCounters
	return nil
}
