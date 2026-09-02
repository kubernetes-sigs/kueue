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

// AmountQuantity returns a in the format the API reports name in, applying the
// resource's scale before any narrowing, and reports whether the value fitted.
//
// CPU amounts are held in milliCPU, so one past int64 there can still be an
// ordinary Quantity once the scale is applied: 10P of CPU is 10^19 milliCPU and
// 10^16 cores. Beyond the scale the Quantity range is the limit. Its documented
// magnitude is int64 in the unit it carries, and a value past that is capped
// here rather than handed to a serializer that can drop the exponent. Whether
// ParseQuantity accepts the digits establishes neither, so it decides nothing.
//
// Everything that fits an int64 goes through ResourceQuantity, so the BinarySI
// resources keep the format they are reported in today.
func (f *ResourceFormatter) AmountQuantity(name corev1.ResourceName, a Amount) (resource.Quantity, bool) {
	if v, ok := a.AsInt64(); ok {
		return f.ResourceQuantity(name, v), true
	}
	if name == corev1.ResourceCPU {
		// Past int64 in milli, exact only where the amount is a whole number of
		// cores and that number fits an int64.
		if cores, ok := a.wholeCores(); ok {
			return *resource.NewQuantity(cores, resource.DecimalSI), true
		}
		return *resource.NewMilliQuantity(a.AsSaturatedInt64(), resource.DecimalSI), false
	}
	return f.ResourceQuantity(name, a.AsSaturatedInt64()), false
}

func (f *ResourceFormatter) AmountQuantityString(name corev1.ResourceName, a Amount) string {
	q, _ := f.AmountQuantity(name, a)
	return q.String()
}
