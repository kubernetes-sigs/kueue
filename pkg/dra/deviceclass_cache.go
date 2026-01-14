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
	"sync"

	corev1 "k8s.io/api/core/v1"
	resourceapi "k8s.io/api/resource/v1"
)

// DeviceClassCache maps extended resource names to DeviceClass names,
// derived from DeviceClass.spec.extendedResourceName.
type DeviceClassCache struct {
	mu                      sync.RWMutex
	extendedResourceToClass map[corev1.ResourceName]string // e.g., "example.com/gpu" -> "gpu.nvidia.com"
	classToExtendedResource map[string]corev1.ResourceName // reverse mapping
}

func NewDeviceClassCache() *DeviceClassCache {
	return &DeviceClassCache{
		extendedResourceToClass: make(map[corev1.ResourceName]string),
		classToExtendedResource: make(map[string]corev1.ResourceName),
	}
}

func (c *DeviceClassCache) AddOrUpdate(dc *resourceapi.DeviceClass) {
	c.mu.Lock()
	defer c.mu.Unlock()

	// Remove old mapping if exists
	if oldExtRes, found := c.classToExtendedResource[dc.Name]; found {
		delete(c.extendedResourceToClass, oldExtRes)
		delete(c.classToExtendedResource, dc.Name)
	}

	// Add new mapping if extendedResourceName is set
	if dc.Spec.ExtendedResourceName != nil && *dc.Spec.ExtendedResourceName != "" {
		extRes := corev1.ResourceName(*dc.Spec.ExtendedResourceName)
		c.extendedResourceToClass[extRes] = dc.Name
		c.classToExtendedResource[dc.Name] = extRes
	}
}

func (c *DeviceClassCache) Delete(dc *resourceapi.DeviceClass) {
	c.mu.Lock()
	defer c.mu.Unlock()

	if extRes, found := c.classToExtendedResource[dc.Name]; found {
		delete(c.extendedResourceToClass, extRes)
		delete(c.classToExtendedResource, dc.Name)
	}
}

func (c *DeviceClassCache) GetDeviceClass(extRes corev1.ResourceName) (string, bool) {
	if c == nil {
		return "", false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	className, found := c.extendedResourceToClass[extRes]
	return className, found
}

func (c *DeviceClassCache) GetExtendedResource(className string) (corev1.ResourceName, bool) {
	if c == nil {
		return "", false
	}
	c.mu.RLock()
	defer c.mu.RUnlock()
	extRes, found := c.classToExtendedResource[className]
	return extRes, found
}
