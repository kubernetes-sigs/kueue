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

package resources

import (
	"strings"
	"sync"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/sets"
)

// ResourceFormatter formats resource quantities using manager-specific rules.
// Register all binary-formatted resources before sharing a formatter with
// concurrent controller code.
type ResourceFormatter struct {
	mu                       sync.RWMutex
	binaryFormattedResources sets.Set[corev1.ResourceName]
}

// NewResourceFormatter creates a ResourceFormatter with no custom resource
// formatting rules.
func NewResourceFormatter() *ResourceFormatter {
	return &ResourceFormatter{binaryFormattedResources: sets.New[corev1.ResourceName]()}
}

// RegisterBinaryFormattedResource marks a resource name as byte-valued for display.
func (f *ResourceFormatter) RegisterBinaryFormattedResource(name corev1.ResourceName) {
	if f == nil {
		return
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.binaryFormattedResources == nil {
		f.binaryFormattedResources = sets.New[corev1.ResourceName]()
	}
	f.binaryFormattedResources.Insert(name)
}

func (f *ResourceFormatter) usesBinaryFormat(name corev1.ResourceName) bool {
	if f == nil {
		return false
	}
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.binaryFormattedResources.Has(name)
}

// ResourceQuantity returns v in the appropriate Kubernetes quantity format for name.
func (f *ResourceFormatter) ResourceQuantity(name corev1.ResourceName, v int64) resource.Quantity {
	switch name {
	case corev1.ResourceCPU:
		return *resource.NewMilliQuantity(v, resource.DecimalSI)
	case corev1.ResourceMemory, corev1.ResourceEphemeralStorage:
		return newCanonicalQuantity(v, resource.BinarySI)
	default:
		if strings.HasPrefix(string(name), corev1.ResourceHugePagesPrefix) || f.usesBinaryFormat(name) {
			return newCanonicalQuantity(v, resource.BinarySI)
		}
		return *resource.NewQuantity(v, resource.DecimalSI)
	}
}

func newCanonicalQuantity(v int64, preferredFormat resource.Format) resource.Quantity {
	preferred := *resource.NewQuantity(v, preferredFormat)
	final, err := resource.ParseQuantity(preferred.String())
	if err != nil {
		return preferred
	}
	return final
}

func (f *ResourceFormatter) ResourceQuantityString(name corev1.ResourceName, v int64) string {
	quantity := f.ResourceQuantity(name, v)
	return quantity.String()
}

func (f *ResourceFormatter) AmountQuantityString(name corev1.ResourceName, a Amount) string {
	if a.Equal(Unlimited) {
		return Unlimited.String()
	}
	return f.ResourceQuantityString(name, a.Int64())
}
