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
	"k8s.io/apimachinery/pkg/runtime"
)

// DeviceClassWrapper wraps a resourcev1.DeviceClass.
type DeviceClassWrapper struct {
	resourcev1.DeviceClass
}

// MakeDeviceClass creates a DeviceClassWrapper with the provided name.
func MakeDeviceClass(name string) *DeviceClassWrapper {
	return &DeviceClassWrapper{
		Name: name,
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
		Name: name,
		Exactly: &resourcev1.ExactDeviceRequest{
			DeviceClassName: deviceClassName,
			AllocationMode:  resourcev1.DeviceAllocationModeExactCount,
			Count:           count,
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
	d.FirstAvailable = append(d.FirstAvailable, subrequests...)
	return d
}

// ResourceClaimSpecBuilder provides a common interface for building ResourceClaimSpec
type ResourceClaimSpecBuilder struct {
	spec resourcev1.ResourceClaimSpec
}

// NewResourceClaimSpecBuilder creates a new ResourceClaimSpecBuilder with default values
func NewResourceClaimSpecBuilder() *ResourceClaimSpecBuilder {
	return &ResourceClaimSpecBuilder{
		spec: resourcev1.ResourceClaimSpec{
			Devices: resourcev1.DeviceClaim{
				Requests: []resourcev1.DeviceRequest{},
			},
		},
	}
}

// DeviceRequest adds a basic device request with the specified name and device class
func (b *ResourceClaimSpecBuilder) DeviceRequest(requestName, deviceClassName string, count int64) *ResourceClaimSpecBuilder {
	b.spec.Devices.Requests = append(b.spec.Devices.Requests, MakeDeviceRequest(requestName, deviceClassName, count).Obj())
	return b
}

// AllocationModeAll sets the AllocationMode to All for the last device request
func (b *ResourceClaimSpecBuilder) AllocationModeAll() *ResourceClaimSpecBuilder {
	if len(b.spec.Devices.Requests) > 0 {
		lastIdx := len(b.spec.Devices.Requests) - 1
		if b.spec.Devices.Requests[lastIdx].Exactly != nil {
			b.spec.Devices.Requests[lastIdx].Exactly.AllocationMode = resourcev1.DeviceAllocationModeAll
			b.spec.Devices.Requests[lastIdx].Exactly.Count = 0
		}
	}
	return b
}

// WithCELSelectors adds CEL selectors to the last device request
func (b *ResourceClaimSpecBuilder) WithCELSelectors(expression string) *ResourceClaimSpecBuilder {
	if len(b.spec.Devices.Requests) > 0 {
		lastIdx := len(b.spec.Devices.Requests) - 1
		if b.spec.Devices.Requests[lastIdx].Exactly != nil {
			b.spec.Devices.Requests[lastIdx].Exactly.Selectors = []resourcev1.DeviceSelector{{
				CEL: &resourcev1.CELDeviceSelector{
					Expression: expression,
				},
			}}
		}
	}
	return b
}

// WithAdminAccess sets AdminAccess on the last device request
func (b *ResourceClaimSpecBuilder) WithAdminAccess(enabled bool) *ResourceClaimSpecBuilder {
	if len(b.spec.Devices.Requests) > 0 {
		lastIdx := len(b.spec.Devices.Requests) - 1
		if b.spec.Devices.Requests[lastIdx].Exactly != nil {
			b.spec.Devices.Requests[lastIdx].Exactly.AdminAccess = new(enabled)
		}
	}
	return b
}

func (b *ResourceClaimSpecBuilder) WithCapacityRequests(requests map[string]string) *ResourceClaimSpecBuilder {
	if len(b.spec.Devices.Requests) > 0 {
		lastIdx := len(b.spec.Devices.Requests) - 1
		if b.spec.Devices.Requests[lastIdx].Exactly != nil {
			reqs := make(map[resourcev1.QualifiedName]resource.Quantity, len(requests))
			for k, v := range requests {
				reqs[resourcev1.QualifiedName(k)] = resource.MustParse(v)
			}
			b.spec.Devices.Requests[lastIdx].Exactly.Capacity = &resourcev1.CapacityRequirements{
				Requests: reqs,
			}
		}
	}
	return b
}

// WithDeviceConstraints adds device constraints to the spec
func (b *ResourceClaimSpecBuilder) WithDeviceConstraints(requestNames []string, matchAttribute string) *ResourceClaimSpecBuilder {
	constraint := resourcev1.DeviceConstraint{
		Requests:       requestNames,
		MatchAttribute: new(resourcev1.FullyQualifiedName(matchAttribute)),
	}
	b.spec.Devices.Constraints = append(b.spec.Devices.Constraints, constraint)
	return b
}

// WithDeviceConfig adds device configuration to the spec
func (b *ResourceClaimSpecBuilder) WithDeviceConfig(requestName, driver string, parameters []byte) *ResourceClaimSpecBuilder {
	config := resourcev1.DeviceClaimConfiguration{
		Requests: []string{requestName},
		DeviceConfiguration: resourcev1.DeviceConfiguration{
			Opaque: &resourcev1.OpaqueDeviceConfiguration{
				Driver:     driver,
				Parameters: runtime.RawExtension{Raw: parameters},
			},
		},
	}
	b.spec.Devices.Config = append(b.spec.Devices.Config, config)
	return b
}

// FirstAvailableRequest adds a FirstAvailable device request
func (b *ResourceClaimSpecBuilder) FirstAvailableRequest(requestName, deviceClassName string) *ResourceClaimSpecBuilder {
	req := MakeDeviceRequest(requestName, deviceClassName, 1).
		FirstAvailableRequest(resourcev1.DeviceSubRequest{
			Name:            "sub1",
			DeviceClassName: deviceClassName,
		}).
		Obj()
	b.spec.Devices.Requests = append(b.spec.Devices.Requests, req)
	return b
}

// Build returns the built ResourceClaimSpec
func (b *ResourceClaimSpecBuilder) Build() resourcev1.ResourceClaimSpec {
	return b.spec
}

// ResourceClaimTemplateWrapper wraps a resourcev1.ResourceClaimTemplate
type ResourceClaimTemplateWrapper struct {
	resourcev1.ResourceClaimTemplate
}

// MakeResourceClaimTemplate creates a ResourceClaimTemplateWrapper with basic metadata
func MakeResourceClaimTemplate(name, namespace string) *ResourceClaimTemplateWrapper {
	return &ResourceClaimTemplateWrapper{
		resourcev1.ResourceClaimTemplate{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: namespace,
			},
			Spec: resourcev1.ResourceClaimTemplateSpec{
				Spec: NewResourceClaimSpecBuilder().Build(),
			},
		},
	}
}

// DeviceRequest adds a basic device request with the specified name and device class
func (r *ResourceClaimTemplateWrapper) DeviceRequest(requestName, deviceClassName string, count int64) *ResourceClaimTemplateWrapper {
	builder := NewResourceClaimSpecBuilder()
	builder.spec = r.Spec.Spec
	builder.DeviceRequest(requestName, deviceClassName, count)
	r.Spec.Spec = builder.Build()
	return r
}

// AllocationModeAll sets the AllocationMode to All for the last device request
func (r *ResourceClaimTemplateWrapper) AllocationModeAll() *ResourceClaimTemplateWrapper {
	builder := NewResourceClaimSpecBuilder()
	builder.spec = r.Spec.Spec
	builder.AllocationModeAll()
	r.Spec.Spec = builder.Build()
	return r
}

// WithCELSelectors adds CEL selectors to the last device request
func (r *ResourceClaimTemplateWrapper) WithCELSelectors(expression string) *ResourceClaimTemplateWrapper {
	builder := NewResourceClaimSpecBuilder()
	builder.spec = r.Spec.Spec
	builder.WithCELSelectors(expression)
	r.Spec.Spec = builder.Build()
	return r
}

// WithAdminAccess sets AdminAccess on the last device request
func (r *ResourceClaimTemplateWrapper) WithAdminAccess(enabled bool) *ResourceClaimTemplateWrapper {
	builder := NewResourceClaimSpecBuilder()
	builder.spec = r.Spec.Spec
	builder.WithAdminAccess(enabled)
	r.Spec.Spec = builder.Build()
	return r
}

func (r *ResourceClaimTemplateWrapper) WithCapacityRequests(requests map[string]string) *ResourceClaimTemplateWrapper {
	builder := NewResourceClaimSpecBuilder()
	builder.spec = r.Spec.Spec
	builder.WithCapacityRequests(requests)
	r.Spec.Spec = builder.Build()
	return r
}

// WithDeviceConstraints adds device constraints to the template
func (r *ResourceClaimTemplateWrapper) WithDeviceConstraints(requestNames []string, matchAttribute string) *ResourceClaimTemplateWrapper {
	builder := NewResourceClaimSpecBuilder()
	builder.spec = r.Spec.Spec
	builder.WithDeviceConstraints(requestNames, matchAttribute)
	r.Spec.Spec = builder.Build()
	return r
}

// WithDeviceConfig adds device configuration to the template
func (r *ResourceClaimTemplateWrapper) WithDeviceConfig(requestName, driver string, parameters []byte) *ResourceClaimTemplateWrapper {
	builder := NewResourceClaimSpecBuilder()
	builder.spec = r.Spec.Spec
	builder.WithDeviceConfig(requestName, driver, parameters)
	r.Spec.Spec = builder.Build()
	return r
}

// FirstAvailableRequest adds a FirstAvailable device request
func (r *ResourceClaimTemplateWrapper) FirstAvailableRequest(requestName, deviceClassName string) *ResourceClaimTemplateWrapper {
	builder := NewResourceClaimSpecBuilder()
	builder.spec = r.Spec.Spec
	builder.FirstAvailableRequest(requestName, deviceClassName)
	r.Spec.Spec = builder.Build()
	return r
}

// Obj returns the underlying ResourceClaimTemplate
func (r *ResourceClaimTemplateWrapper) Obj() *resourcev1.ResourceClaimTemplate {
	return &r.ResourceClaimTemplate
}

// ResourceClaimWrapper wraps a resourcev1.ResourceClaim
type ResourceClaimWrapper struct{ resourcev1.ResourceClaim }

// MakeResourceClaim creates a ResourceClaimWrapper with basic metadata
func MakeResourceClaim(name, namespace string) *ResourceClaimWrapper {
	return &ResourceClaimWrapper{
		resourcev1.ResourceClaim{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: namespace,
			},
			Spec: NewResourceClaimSpecBuilder().Build(),
		},
	}
}

// DeviceRequest adds a basic device request with the specified name and device class
func (r *ResourceClaimWrapper) DeviceRequest(requestName, deviceClassName string, count int64) *ResourceClaimWrapper {
	builder := NewResourceClaimSpecBuilder()
	builder.spec = r.Spec
	builder.DeviceRequest(requestName, deviceClassName, count)
	r.Spec = builder.Build()
	return r
}

// AllocationModeAll sets the AllocationMode to All for the last device request
func (r *ResourceClaimWrapper) AllocationModeAll() *ResourceClaimWrapper {
	builder := NewResourceClaimSpecBuilder()
	builder.spec = r.Spec
	builder.AllocationModeAll()
	r.Spec = builder.Build()
	return r
}

// WithCELSelectors adds CEL selectors to the last device request
func (r *ResourceClaimWrapper) WithCELSelectors(expression string) *ResourceClaimWrapper {
	builder := NewResourceClaimSpecBuilder()
	builder.spec = r.Spec
	builder.WithCELSelectors(expression)
	r.Spec = builder.Build()
	return r
}

// WithAdminAccess sets AdminAccess on the last device request
func (r *ResourceClaimWrapper) WithAdminAccess(enabled bool) *ResourceClaimWrapper {
	builder := NewResourceClaimSpecBuilder()
	builder.spec = r.Spec
	builder.WithAdminAccess(enabled)
	r.Spec = builder.Build()
	return r
}

// WithDeviceConstraints adds device constraints to the claim
func (r *ResourceClaimWrapper) WithDeviceConstraints(requestNames []string, matchAttribute string) *ResourceClaimWrapper {
	builder := NewResourceClaimSpecBuilder()
	builder.spec = r.Spec
	builder.WithDeviceConstraints(requestNames, matchAttribute)
	r.Spec = builder.Build()
	return r
}

// WithDeviceConfig adds device configuration to the claim
func (r *ResourceClaimWrapper) WithDeviceConfig(requestName, driver string, parameters []byte) *ResourceClaimWrapper {
	builder := NewResourceClaimSpecBuilder()
	builder.spec = r.Spec
	builder.WithDeviceConfig(requestName, driver, parameters)
	r.Spec = builder.Build()
	return r
}

// FirstAvailableRequest adds a FirstAvailable device request
func (r *ResourceClaimWrapper) FirstAvailableRequest(requestName, deviceClassName string) *ResourceClaimWrapper {
	builder := NewResourceClaimSpecBuilder()
	builder.spec = r.Spec
	builder.FirstAvailableRequest(requestName, deviceClassName)
	r.Spec = builder.Build()
	return r
}

// Obj returns the underlying ResourceClaim
func (r *ResourceClaimWrapper) Obj() *resourcev1.ResourceClaim {
	return &r.ResourceClaim
}

type ResourceSliceWrapper struct{ resourcev1.ResourceSlice }

func MakeResourceSlice(name, driver string) *ResourceSliceWrapper {
	return &ResourceSliceWrapper{
		resourcev1.ResourceSlice{
			ObjectMeta: metav1.ObjectMeta{
				Name: name,
			},
			Spec: resourcev1.ResourceSliceSpec{
				Driver: driver,
				Pool: resourcev1.ResourcePool{
					Name:               "default-pool",
					Generation:         1,
					ResourceSliceCount: 1,
				},
				NodeName: new("fake-node"),
			},
		},
	}
}

func (w *ResourceSliceWrapper) Pool(name string, generation int64, sliceCount int64) *ResourceSliceWrapper {
	w.Spec.Pool = resourcev1.ResourcePool{
		Name:               name,
		Generation:         generation,
		ResourceSliceCount: sliceCount,
	}
	return w
}

func (w *ResourceSliceWrapper) Device(name string) *ResourceSliceWrapper {
	w.Spec.Devices = append(w.Spec.Devices, resourcev1.Device{
		Name:       name,
		Attributes: make(map[resourcev1.QualifiedName]resourcev1.DeviceAttribute),
	})
	return w
}

func (w *ResourceSliceWrapper) Attribute(name, value string) *ResourceSliceWrapper {
	if len(w.Spec.Devices) > 0 {
		last := &w.Spec.Devices[len(w.Spec.Devices)-1]
		last.Attributes[resourcev1.QualifiedName(name)] = resourcev1.DeviceAttribute{StringValue: new(value)}
	}
	return w
}

func (w *ResourceSliceWrapper) CounterConsumption(counterSet, counterName, value string) *ResourceSliceWrapper {
	if len(w.Spec.Devices) > 0 {
		last := &w.Spec.Devices[len(w.Spec.Devices)-1]
		last.ConsumesCounters = append(last.ConsumesCounters, resourcev1.DeviceCounterConsumption{
			CounterSet: counterSet,
			Counters:   map[string]resourcev1.Counter{counterName: {Value: resource.MustParse(value)}},
		})
	}
	return w
}

func (w *ResourceSliceWrapper) DeviceCapacity(name, value string, policy *resourcev1.CapacityRequestPolicy) *ResourceSliceWrapper {
	if len(w.Spec.Devices) > 0 {
		last := &w.Spec.Devices[len(w.Spec.Devices)-1]
		if last.Capacity == nil {
			last.Capacity = make(map[resourcev1.QualifiedName]resourcev1.DeviceCapacity)
		}
		last.Capacity[resourcev1.QualifiedName(name)] = resourcev1.DeviceCapacity{
			Value:         resource.MustParse(value),
			RequestPolicy: policy,
		}
	}
	return w
}

func (w *ResourceSliceWrapper) AllowMultipleAllocations(allow bool) *ResourceSliceWrapper {
	if len(w.Spec.Devices) > 0 {
		last := &w.Spec.Devices[len(w.Spec.Devices)-1]
		last.AllowMultipleAllocations = &allow
	}
	return w
}

func (w *ResourceSliceWrapper) Obj() *resourcev1.ResourceSlice {
	return &w.ResourceSlice
}
