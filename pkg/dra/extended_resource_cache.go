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
	"k8s.io/apimachinery/pkg/util/sets"
)

// ExtendedResourceCache tracks which extended resource names are backed by DRA
// DeviceClasses. Maps each resource name to the set of DeviceClass names that
// declare it, so that deleting one DeviceClass doesn't remove the resource if
// another DeviceClass still backs it.
type ExtendedResourceCache struct {
	lock      sync.RWMutex
	resources map[corev1.ResourceName]sets.Set[string]
}

// NewExtendedResourceCache creates a new ExtendedResourceCache.
func NewExtendedResourceCache() *ExtendedResourceCache {
	return &ExtendedResourceCache{
		resources: make(map[corev1.ResourceName]sets.Set[string]),
	}
}

// Has returns true if the resource name is backed by at least one DRA DeviceClass.
func (c *ExtendedResourceCache) Has(name corev1.ResourceName) bool {
	c.lock.RLock()
	defer c.lock.RUnlock()
	return len(c.resources[name]) > 0
}

// Add registers that a DeviceClass backs the given extended resource name.
func (c *ExtendedResourceCache) Add(resourceName corev1.ResourceName, deviceClassName string) {
	c.lock.Lock()
	defer c.lock.Unlock()
	if c.resources[resourceName] == nil {
		c.resources[resourceName] = sets.New[string]()
	}
	c.resources[resourceName].Insert(deviceClassName)
}

// Remove unregisters a DeviceClass from the given extended resource name.
// The resource name is removed from the cache only when no DeviceClasses back it.
func (c *ExtendedResourceCache) Remove(resourceName corev1.ResourceName, deviceClassName string) {
	c.lock.Lock()
	defer c.lock.Unlock()
	if s, ok := c.resources[resourceName]; ok {
		s.Delete(deviceClassName)
		if s.Len() == 0 {
			delete(c.resources, resourceName)
		}
	}
}
