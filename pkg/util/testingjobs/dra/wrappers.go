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
	resourcev1 "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// DeviceClassWrapper wraps a resourcev1.DeviceClass.
type DeviceClassWrapper struct {
	resourcev1.DeviceClass
}

// MakeDeviceClass creates a DeviceClassWrapper with the provided name.
func MakeDeviceClass(name string) *DeviceClassWrapper {
	return &DeviceClassWrapper{
		DeviceClass: resourcev1.DeviceClass{
			ObjectMeta: metav1.ObjectMeta{Name: name},
		},
	}
}

// Obj returns the inner DeviceClass.
func (d *DeviceClassWrapper) Obj() *resourcev1.DeviceClass {
	return &d.DeviceClass
}

// GeneratedName sets the generated name prefix and clears the name.
func (d *DeviceClassWrapper) GeneratedName(name string) *DeviceClassWrapper {
	d.GenerateName = name
	d.Name = ""
	return d
}

// CreationTimestamp sets the creation timestamp.
func (d *DeviceClassWrapper) CreationTimestamp(timestamp metav1.Time) *DeviceClassWrapper {
	d.ObjectMeta.CreationTimestamp = timestamp
	return d
}

// ExtendedResourceName sets the extended resource name.
func (d *DeviceClassWrapper) ExtendedResourceName(name string) *DeviceClassWrapper {
	d.Spec.ExtendedResourceName = new(name)
	return d
}

// CELSelector adds a CEL selector.
func (d *DeviceClassWrapper) CELSelector(expression string) *DeviceClassWrapper {
	d.Spec.Selectors = append(d.Spec.Selectors, resourcev1.DeviceSelector{
		CEL: &resourcev1.CELDeviceSelector{Expression: expression},
	})
	return d
}

// DeviceRequestWrapper wraps a resourcev1.DeviceRequest.
type DeviceRequestWrapper struct {
	resourcev1.DeviceRequest
}

// MakeDeviceRequest creates an exact-count DeviceRequestWrapper.
func MakeDeviceRequest(name, deviceClassName string, count int64) *DeviceRequestWrapper {
	return &DeviceRequestWrapper{
		DeviceRequest: resourcev1.DeviceRequest{
			Name: name,
			Exactly: &resourcev1.ExactDeviceRequest{
				DeviceClassName: deviceClassName,
				AllocationMode:  resourcev1.DeviceAllocationModeExactCount,
				Count:           count,
			},
		},
	}
}

// Obj returns the inner DeviceRequest.
func (d *DeviceRequestWrapper) Obj() resourcev1.DeviceRequest {
	return d.DeviceRequest
}

// AllocationModeAll requests all matching devices.
func (d *DeviceRequestWrapper) AllocationModeAll() *DeviceRequestWrapper {
	if d.Exactly != nil {
		d.Exactly.AllocationMode = resourcev1.DeviceAllocationModeAll
		d.Exactly.Count = 0
	}
	return d
}

// CELSelector adds a CEL selector to the exact request.
func (d *DeviceRequestWrapper) CELSelector(expression string) *DeviceRequestWrapper {
	if d.Exactly != nil {
		d.Exactly.Selectors = append(d.Exactly.Selectors, resourcev1.DeviceSelector{
			CEL: &resourcev1.CELDeviceSelector{Expression: expression},
		})
	}
	return d
}

// AdminAccess sets admin access on the exact request.
func (d *DeviceRequestWrapper) AdminAccess(enabled bool) *DeviceRequestWrapper {
	if d.Exactly != nil {
		d.Exactly.AdminAccess = new(enabled)
	}
	return d
}

// CapacityRequests sets the requested device capacities on the exact request.
func (d *DeviceRequestWrapper) CapacityRequests(requests map[string]string) *DeviceRequestWrapper {
	if d.Exactly == nil {
		return d
	}

	quantities := make(map[resourcev1.QualifiedName]resource.Quantity, len(requests))
	for name, value := range requests {
		quantities[resourcev1.QualifiedName(name)] = resource.MustParse(value)
	}
	d.Exactly.Capacity = &resourcev1.CapacityRequirements{Requests: quantities}
	return d
}

// FirstAvailableRequest replaces the exact request with prioritized subrequests.
func (d *DeviceRequestWrapper) FirstAvailableRequest(subrequests ...resourcev1.DeviceSubRequest) *DeviceRequestWrapper {
	d.Exactly = nil
	d.DeviceRequest.FirstAvailable = append(d.DeviceRequest.FirstAvailable, subrequests...)
	return d
}
