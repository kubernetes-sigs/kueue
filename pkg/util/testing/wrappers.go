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

package testing

import (
	corev1 "k8s.io/api/core/v1"
	nodev1 "k8s.io/api/node/v1"
	resourcev1 "k8s.io/api/resource/v1"
	schedulingv1 "k8s.io/api/scheduling/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	utilResource "sigs.k8s.io/kueue/pkg/util/resource"
)

// PriorityClassWrapper wraps a PriorityClass.
type PriorityClassWrapper struct {
	schedulingv1.PriorityClass
}

// MakePriorityClass creates a wrapper for a PriorityClass.
func MakePriorityClass(name string) *PriorityClassWrapper {
	return &PriorityClassWrapper{schedulingv1.PriorityClass{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		}},
	}
}

// PriorityValue update value of PriorityClass。
func (p *PriorityClassWrapper) PriorityValue(v int32) *PriorityClassWrapper {
	p.Value = v
	return p
}

// Obj returns the inner PriorityClass.
func (p *PriorityClassWrapper) Obj() *schedulingv1.PriorityClass {
	return &p.PriorityClass
}

// RuntimeClassWrapper wraps a RuntimeClass.
type RuntimeClassWrapper struct{ nodev1.RuntimeClass }

// MakeRuntimeClass creates a wrapper for a Runtime.
func MakeRuntimeClass(name, handler string) *RuntimeClassWrapper {
	return &RuntimeClassWrapper{nodev1.RuntimeClass{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		},
		Handler: handler,
	}}
}

// PodOverhead adds an Overhead to the RuntimeClass.
func (rc *RuntimeClassWrapper) PodOverhead(resources corev1.ResourceList) *RuntimeClassWrapper {
	rc.Overhead = &nodev1.Overhead{
		PodFixed: resources,
	}
	return rc
}

// Obj returns the inner flavor.
func (rc *RuntimeClassWrapper) Obj() *nodev1.RuntimeClass {
	return &rc.RuntimeClass
}

type LimitRangeWrapper struct{ corev1.LimitRange }

func MakeLimitRange(name, namespace string) *LimitRangeWrapper {
	return &LimitRangeWrapper{
		LimitRange: corev1.LimitRange{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: namespace,
			},
			Spec: corev1.LimitRangeSpec{
				Limits: []corev1.LimitRangeItem{
					{
						Type:                 corev1.LimitTypeContainer,
						Max:                  corev1.ResourceList{},
						Min:                  corev1.ResourceList{},
						Default:              corev1.ResourceList{},
						DefaultRequest:       corev1.ResourceList{},
						MaxLimitRequestRatio: corev1.ResourceList{},
					},
				},
			},
		},
	}
}

func (lr *LimitRangeWrapper) WithType(t corev1.LimitType) *LimitRangeWrapper {
	lr.Spec.Limits[0].Type = t
	return lr
}

func (lr *LimitRangeWrapper) WithValue(member string, t corev1.ResourceName, q string) *LimitRangeWrapper {
	target := lr.Spec.Limits[0].Max
	switch member {
	case "Min":
		target = lr.Spec.Limits[0].Min
	case "DefaultRequest":
		target = lr.Spec.Limits[0].DefaultRequest
	case "Default":
		target = lr.Spec.Limits[0].Default
	case "Max":
	case "MaxLimitRequestRatio":
		target = lr.Spec.Limits[0].MaxLimitRequestRatio
	// nothing
	default:
		panic("Unexpected member " + member)
	}
	target[t] = resource.MustParse(q)
	return lr
}

func (lr *LimitRangeWrapper) Obj() *corev1.LimitRange {
	return &lr.LimitRange
}

// ContainerWrapper wraps a corev1.Container.
type ContainerWrapper struct{ corev1.Container }

// MakeContainer wraps a ContainerWrapper with an empty ResourceList.
func MakeContainer() *ContainerWrapper {
	return &ContainerWrapper{
		corev1.Container{
			Resources: corev1.ResourceRequirements{
				Requests: corev1.ResourceList{},
			},
		},
	}
}

// Obj returns the inner corev1.Container.
func (c *ContainerWrapper) Obj() *corev1.Container {
	return &c.Container
}

// Name sets the name of the container.
func (c *ContainerWrapper) Name(name string) *ContainerWrapper {
	c.Container.Name = name
	return c
}

// Image sets the image of the container.
func (c *ContainerWrapper) Image(image string) *ContainerWrapper {
	c.Container.Image = image
	return c
}

// WithResourceReq appends a resource request to the container.
func (c *ContainerWrapper) WithResourceReq(resourceName corev1.ResourceName, quantity string) *ContainerWrapper {
	requests := utilResource.MergeResourceListKeepFirst(c.Resources.Requests, corev1.ResourceList{
		resourceName: resource.MustParse(quantity),
	})
	c.Resources.Requests = requests

	return c
}

// WithResourceLimit appends a resource limit to the container.
func (c *ContainerWrapper) WithResourceLimit(resourceName corev1.ResourceName, quantity string) *ContainerWrapper {
	limits := utilResource.MergeResourceListKeepFirst(c.Resources.Limits, corev1.ResourceList{
		resourceName: resource.MustParse(quantity),
	})
	c.Resources.Limits = limits

	return c
}

// WithEnvVar appends a env variable to the container.
func (c *ContainerWrapper) WithEnvVar(envVar corev1.EnvVar) *ContainerWrapper {
	c.Env = append(c.Env, envVar)
	return c
}

// AsSidecar makes the container a sidecar when used as an Init Container.
func (c *ContainerWrapper) AsSidecar() *ContainerWrapper {
	c.RestartPolicy = ptr.To(corev1.ContainerRestartPolicyAlways)

	return c
}

type PodTemplateWrapper struct {
	corev1.PodTemplate
}

func MakePodTemplate(name, namespace string) *PodTemplateWrapper {
	return &PodTemplateWrapper{
		corev1.PodTemplate{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: namespace,
			},
		},
	}
}

func (p *PodTemplateWrapper) Obj() *corev1.PodTemplate {
	return &p.PodTemplate
}

func (p *PodTemplateWrapper) Clone() *PodTemplateWrapper {
	return &PodTemplateWrapper{PodTemplate: *p.DeepCopy()}
}

func (p *PodTemplateWrapper) Label(k, v string) *PodTemplateWrapper {
	if p.Labels == nil {
		p.Labels = make(map[string]string)
	}
	p.Labels[k] = v
	return p
}

func (p *PodTemplateWrapper) Containers(containers ...corev1.Container) *PodTemplateWrapper {
	p.Template.Spec.Containers = containers
	return p
}

func (p *PodTemplateWrapper) NodeSelector(k, v string) *PodTemplateWrapper {
	if p.Template.Spec.NodeSelector == nil {
		p.Template.Spec.NodeSelector = make(map[string]string)
	}
	p.Template.Spec.NodeSelector[k] = v
	return p
}

func (p *PodTemplateWrapper) Toleration(toleration corev1.Toleration) *PodTemplateWrapper {
	p.Template.Spec.Tolerations = append(p.Template.Spec.Tolerations, toleration)
	return p
}

func (p *PodTemplateWrapper) PriorityClass(pc string) *PodTemplateWrapper {
	p.Template.Spec.PriorityClassName = pc
	return p
}

func (p *PodTemplateWrapper) RequiredDuringSchedulingIgnoredDuringExecution(nodeSelectorTerms []corev1.NodeSelectorTerm) *PodTemplateWrapper {
	if p.Template.Spec.Affinity == nil {
		p.Template.Spec.Affinity = &corev1.Affinity{}
	}
	if p.Template.Spec.Affinity.NodeAffinity == nil {
		p.Template.Spec.Affinity.NodeAffinity = &corev1.NodeAffinity{}
	}
	if p.Template.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution == nil {
		p.Template.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution = &corev1.NodeSelector{}
	}
	p.Template.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms = append(
		p.Template.Spec.Affinity.NodeAffinity.RequiredDuringSchedulingIgnoredDuringExecution.NodeSelectorTerms,
		nodeSelectorTerms...,
	)
	return p
}

func (p *PodTemplateWrapper) ControllerReference(gvk schema.GroupVersionKind, name, uid string) *PodTemplateWrapper {
	AppendOwnerReference(&p.PodTemplate, gvk, name, uid, ptr.To(true), ptr.To(true))
	return p
}

type NamespaceWrapper struct {
	corev1.Namespace
}

func MakeNamespaceWrapper(name string) *NamespaceWrapper {
	return &NamespaceWrapper{
		corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: name,
			},
		},
	}
}

func (w *NamespaceWrapper) Clone() *NamespaceWrapper {
	return &NamespaceWrapper{Namespace: *w.DeepCopy()}
}

func (w *NamespaceWrapper) Obj() *corev1.Namespace {
	return &w.Namespace
}

func (w *NamespaceWrapper) GenerateName(generateName string) *NamespaceWrapper {
	w.Namespace.GenerateName = generateName
	return w
}

func (w *NamespaceWrapper) Label(k, v string) *NamespaceWrapper {
	if w.Labels == nil {
		w.Labels = make(map[string]string)
	}
	w.Labels[k] = v
	return w
}

func AppendOwnerReference(obj client.Object, gvk schema.GroupVersionKind, name, uid string, controller, blockDeletion *bool) {
	obj.SetOwnerReferences(append(obj.GetOwnerReferences(), metav1.OwnerReference{
		APIVersion:         gvk.GroupVersion().String(),
		Kind:               gvk.Kind,
		Name:               name,
		UID:                types.UID(uid),
		Controller:         controller,
		BlockOwnerDeletion: blockDeletion,
	}))
}

type EventRecordWrapper struct {
	EventRecord
}

func MakeEventRecord(namespace, name, reason, eventType string) *EventRecordWrapper {
	return &EventRecordWrapper{
		EventRecord: EventRecord{
			Key:       types.NamespacedName{Namespace: namespace, Name: name},
			Reason:    reason,
			EventType: eventType,
		},
	}
}

func (e *EventRecordWrapper) Message(message string) *EventRecordWrapper {
	e.EventRecord.Message = message
	return e
}

func (e *EventRecordWrapper) Obj() EventRecord {
	return e.EventRecord
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
	req := resourcev1.DeviceRequest{
		Name: requestName,
		Exactly: &resourcev1.ExactDeviceRequest{
			DeviceClassName: deviceClassName,
			AllocationMode:  resourcev1.DeviceAllocationModeExactCount,
			Count:           count,
		},
	}
	b.spec.Devices.Requests = append(b.spec.Devices.Requests, req)
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
			b.spec.Devices.Requests[lastIdx].Exactly.AdminAccess = ptr.To(enabled)
		}
	}
	return b
}

// WithDeviceConstraints adds device constraints to the spec
func (b *ResourceClaimSpecBuilder) WithDeviceConstraints(requestNames []string, matchAttribute string) *ResourceClaimSpecBuilder {
	constraint := resourcev1.DeviceConstraint{
		Requests:       requestNames,
		MatchAttribute: ptr.To(resourcev1.FullyQualifiedName(matchAttribute)),
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
	req := resourcev1.DeviceRequest{
		Name: requestName,
		FirstAvailable: []resourcev1.DeviceSubRequest{{
			Name:            "sub1",
			DeviceClassName: deviceClassName,
		}},
	}
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
