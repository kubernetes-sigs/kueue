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

	configapi "sigs.k8s.io/kueue/apis/config/v1beta2"
)

var (
	globalMapper *ResourceMapper
)

// ResourceMapper provides device class to logical resource name mapping
// based on Configuration API DRA settings. Initialized once at startup, immutable during runtime.
// No locks needed due to startup-only initialization pattern.
type ResourceMapper struct {
	deviceClassToResource map[corev1.ResourceName]corev1.ResourceName
}

// newDRAResourceMapper creates a new empty ResourceMapper instance.
func newDRAResourceMapper() *ResourceMapper {
	return &ResourceMapper{
		deviceClassToResource: make(map[corev1.ResourceName]corev1.ResourceName),
	}
}

// Returns (logicalResourceName, true) on success or ("", false) when the device class is not mapped.
func (m *ResourceMapper) lookup(deviceClass corev1.ResourceName) (corev1.ResourceName, bool) {
	logicalResource, found := m.deviceClassToResource[deviceClass]
	return logicalResource, found
}

func (m *ResourceMapper) populateFromConfiguration(mappings []configapi.DeviceClassMapping) error {
	if mappings == nil {
		return nil
	}
	newMapping := make(map[corev1.ResourceName]corev1.ResourceName)
	for _, mapping := range mappings {
		for _, deviceClassName := range mapping.DeviceClassNames {
			newMapping[deviceClassName] = mapping.Name
		}
	}

	m.deviceClassToResource = newMapping
	return nil
}

// Mapper returns the singleton DRA mapper instance.
func Mapper() *ResourceMapper {
	if globalMapper == nil {
		globalMapper = newDRAResourceMapper()
	}
	return globalMapper
}

// CreateMapperFromConfiguration creates and populates the global DRA mapper from Configuration API.
// This is called ONCE during Kueue startup when configuration is loaded.
func CreateMapperFromConfiguration(mappings []configapi.DeviceClassMapping) error {
	return Mapper().populateFromConfiguration(mappings)
}
