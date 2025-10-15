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
	"maps"

	corev1 "k8s.io/api/core/v1"
	nodev1 "k8s.io/api/node/v1"
	schedulingv1 "k8s.io/api/scheduling/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
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

// ProvisioningRequestConfigWrapper wraps a ProvisioningRequestConfig
type ProvisioningRequestConfigWrapper struct {
	kueue.ProvisioningRequestConfig
}

// MakeProvisioningRequestConfig creates a wrapper for a ProvisioningRequestConfig.
func MakeProvisioningRequestConfig(name string) *ProvisioningRequestConfigWrapper {
	return &ProvisioningRequestConfigWrapper{kueue.ProvisioningRequestConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name: name,
		}},
	}
}

func (prc *ProvisioningRequestConfigWrapper) ProvisioningClass(pc string) *ProvisioningRequestConfigWrapper {
	prc.Spec.ProvisioningClassName = pc
	return prc
}

func (prc *ProvisioningRequestConfigWrapper) Parameters(parameters map[string]kueue.Parameter) *ProvisioningRequestConfigWrapper {
	if prc.Spec.Parameters == nil {
		prc.Spec.Parameters = make(map[string]kueue.Parameter, len(parameters))
	}

	maps.Copy(prc.Spec.Parameters, parameters)

	return prc
}

func (prc *ProvisioningRequestConfigWrapper) WithParameter(key string, value kueue.Parameter) *ProvisioningRequestConfigWrapper {
	if prc.Spec.Parameters == nil {
		prc.Spec.Parameters = make(map[string]kueue.Parameter, 1)
	}

	prc.Spec.Parameters[key] = value
	return prc
}

func (prc *ProvisioningRequestConfigWrapper) ManagedResources(r []corev1.ResourceName) *ProvisioningRequestConfigWrapper {
	prc.Spec.ManagedResources = r
	return prc
}

func (prc *ProvisioningRequestConfigWrapper) WithManagedResource(managedResource corev1.ResourceName) *ProvisioningRequestConfigWrapper {
	prc.Spec.ManagedResources = append(prc.Spec.ManagedResources, managedResource)
	return prc
}

func (prc *ProvisioningRequestConfigWrapper) RetryStrategy(retryStrategy *kueue.ProvisioningRequestRetryStrategy) *ProvisioningRequestConfigWrapper {
	prc.Spec.RetryStrategy = retryStrategy
	return prc
}

func (prc *ProvisioningRequestConfigWrapper) PodSetUpdate(update kueue.ProvisioningRequestPodSetUpdates) *ProvisioningRequestConfigWrapper {
	prc.Spec.PodSetUpdates = &update
	return prc
}

func (prc *ProvisioningRequestConfigWrapper) BaseBackoff(backoffBaseSeconds int32) *ProvisioningRequestConfigWrapper {
	if prc.Spec.RetryStrategy == nil {
		prc.Spec.RetryStrategy = &kueue.ProvisioningRequestRetryStrategy{}
	}

	prc.Spec.RetryStrategy.BackoffBaseSeconds = &backoffBaseSeconds
	return prc
}

func (prc *ProvisioningRequestConfigWrapper) MaxBackoff(backoffMaxSeconds int32) *ProvisioningRequestConfigWrapper {
	if prc.Spec.RetryStrategy == nil {
		prc.Spec.RetryStrategy = &kueue.ProvisioningRequestRetryStrategy{}
	}

	prc.Spec.RetryStrategy.BackoffMaxSeconds = &backoffMaxSeconds
	return prc
}

func (prc *ProvisioningRequestConfigWrapper) RetryLimit(backoffLimitCount int32) *ProvisioningRequestConfigWrapper {
	if prc.Spec.RetryStrategy == nil {
		prc.Spec.RetryStrategy = &kueue.ProvisioningRequestRetryStrategy{}
	}

	prc.Spec.RetryStrategy.BackoffLimitCount = &backoffLimitCount
	return prc
}

func (prc *ProvisioningRequestConfigWrapper) PodSetMergePolicy(mode kueue.ProvisioningRequestConfigPodSetMergePolicy) *ProvisioningRequestConfigWrapper {
	prc.Spec.PodSetMergePolicy = &mode
	return prc
}

func (prc *ProvisioningRequestConfigWrapper) Clone() *ProvisioningRequestConfigWrapper {
	return &ProvisioningRequestConfigWrapper{ProvisioningRequestConfig: *prc.DeepCopy()}
}

func (prc *ProvisioningRequestConfigWrapper) Obj() *kueue.ProvisioningRequestConfig {
	return &prc.ProvisioningRequestConfig
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
