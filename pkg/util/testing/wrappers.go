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
	rbacv1 "k8s.io/api/rbac/v1"
	resourcev1 "k8s.io/api/resource/v1"
	schedulingv1 "k8s.io/api/scheduling/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
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
		Name: name},
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
		Name:    name,
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
		Name:      name,
		Namespace: namespace,
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
	}
}

func (lr *LimitRangeWrapper) WithType(t corev1.LimitType) *LimitRangeWrapper {
	lr.Spec.Limits[0].Type = t
	return lr
}

// LimitTypes replaces the LimitRange's items with one bare item per given type,
// or clears them when called with no arguments.
func (lr *LimitRangeWrapper) LimitTypes(types ...corev1.LimitType) *LimitRangeWrapper {
	items := make([]corev1.LimitRangeItem, len(types))
	for i, t := range types {
		items[i] = corev1.LimitRangeItem{Type: t}
	}
	lr.Spec.Limits = items
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

func (c *ContainerWrapper) ImagePullPolicy(policy corev1.PullPolicy) *ContainerWrapper {
	c.Container.ImagePullPolicy = policy
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

// Port appends a container port, exposed on the host when host is non-zero.
func (c *ContainerWrapper) Port(container, host int32, protocol corev1.Protocol) *ContainerWrapper {
	c.Ports = append(c.Ports, corev1.ContainerPort{
		ContainerPort: container,
		HostPort:      host,
		Protocol:      protocol,
	})
	return c
}

// WithEnvVar appends a env variable to the container.
func (c *ContainerWrapper) WithEnvVar(envVar corev1.EnvVar) *ContainerWrapper {
	c.Env = append(c.Env, envVar)
	return c
}

// AsSidecar makes the container a sidecar when used as an Init Container.
func (c *ContainerWrapper) AsSidecar() *ContainerWrapper {
	c.RestartPolicy = new(corev1.ContainerRestartPolicyAlways)

	return c
}

func (c *ContainerWrapper) VolumeMount(name, mountPath string) *ContainerWrapper {
	c.VolumeMounts = append(c.VolumeMounts, corev1.VolumeMount{
		Name:      name,
		MountPath: mountPath,
	})
	return c
}

func (c *ContainerWrapper) Command(cmd ...string) *ContainerWrapper {
	c.Container.Command = cmd
	return c
}

type PodTemplateWrapper struct {
	corev1.PodTemplate
}

func MakePodTemplate(name, namespace string) *PodTemplateWrapper {
	return &PodTemplateWrapper{
		corev1.PodTemplate{
			Name:      name,
			Namespace: namespace,
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

func (p *PodTemplateWrapper) PreferredDuringSchedulingIgnoredDuringExecution(preferredSchedulingTerms []corev1.PreferredSchedulingTerm) *PodTemplateWrapper {
	if p.Template.Spec.Affinity == nil {
		p.Template.Spec.Affinity = &corev1.Affinity{}
	}
	if p.Template.Spec.Affinity.NodeAffinity == nil {
		p.Template.Spec.Affinity.NodeAffinity = &corev1.NodeAffinity{}
	}
	p.Template.Spec.Affinity.NodeAffinity.PreferredDuringSchedulingIgnoredDuringExecution = append(
		p.Template.Spec.Affinity.NodeAffinity.PreferredDuringSchedulingIgnoredDuringExecution,
		preferredSchedulingTerms...,
	)
	return p
}

func (p *PodTemplateWrapper) RequiredNodeSelectorRequirement(key string, op corev1.NodeSelectorOperator, values ...string) *PodTemplateWrapper {
	return p.RequiredDuringSchedulingIgnoredDuringExecution([]corev1.NodeSelectorTerm{
		{
			MatchExpressions: []corev1.NodeSelectorRequirement{
				{
					Key:      key,
					Operator: op,
					Values:   values,
				},
			},
		},
	})
}

func (p *PodTemplateWrapper) PreferredNodeSelectorRequirement(weight int32, key string, op corev1.NodeSelectorOperator, values ...string) *PodTemplateWrapper {
	return p.PreferredDuringSchedulingIgnoredDuringExecution([]corev1.PreferredSchedulingTerm{
		{
			Weight: weight,
			Preference: corev1.NodeSelectorTerm{
				MatchExpressions: []corev1.NodeSelectorRequirement{
					{
						Key:      key,
						Operator: op,
						Values:   values,
					},
				},
			},
		},
	})
}

func (p *PodTemplateWrapper) ControllerReference(gvk schema.GroupVersionKind, name, uid string) *PodTemplateWrapper {
	AppendOwnerReference(&p.PodTemplate, gvk, name, uid, new(true), new(true))
	return p
}

type NamespaceWrapper struct {
	corev1.Namespace
}

func MakeNamespaceWrapper(name string) *NamespaceWrapper {
	return &NamespaceWrapper{
		corev1.Namespace{
			Name: name,
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
		Key:       types.NamespacedName{Namespace: namespace, Name: name},
		Reason:    reason,
		EventType: eventType,
	}
}

func (e *EventRecordWrapper) Message(message string) *EventRecordWrapper {
	e.EventRecord.Message = message
	return e
}

func (e *EventRecordWrapper) Obj() EventRecord {
	return e.EventRecord
}

type SecretWrapper struct{ corev1.Secret }

func MakeSecret(name, ns string) *SecretWrapper {
	return &SecretWrapper{
		corev1.Secret{
			Name:      name,
			Namespace: ns,
		}}
}

func (s *SecretWrapper) Obj() *corev1.Secret {
	return &s.Secret
}

func (s *SecretWrapper) Data(key string, value []byte) *SecretWrapper {
	if s.Secret.Data == nil {
		s.Secret.Data = make(map[string][]byte)
	}
	s.Secret.Data[key] = value
	return s
}

type RoleWrapper struct{ rbacv1.Role }

func MakeRole(name, ns string) *RoleWrapper {
	return &RoleWrapper{
		rbacv1.Role{
			Name:      name,
			Namespace: ns,
		},
	}
}

func (r *RoleWrapper) Obj() *rbacv1.Role {
	return &r.Role
}

func (r *RoleWrapper) Rule(apiGroups, resources, verbs []string) *RoleWrapper {
	r.Rules = append(r.Rules, rbacv1.PolicyRule{
		APIGroups: apiGroups,
		Resources: resources,
		Verbs:     verbs,
	})
	return r
}

type RoleBindingWrapper struct{ rbacv1.RoleBinding }

func MakeRoleBinding(name, ns string) *RoleBindingWrapper {
	return &RoleBindingWrapper{
		rbacv1.RoleBinding{
			Name:      name,
			Namespace: ns,
		},
	}
}

func (rb *RoleBindingWrapper) Obj() *rbacv1.RoleBinding {
	return &rb.RoleBinding
}

func (rb *RoleBindingWrapper) RoleRef(apiGroup, kind, name string) *RoleBindingWrapper {
	rb.RoleBinding.RoleRef = rbacv1.RoleRef{
		APIGroup: apiGroup,
		Kind:     kind,
		Name:     name,
	}
	return rb
}

func (rb *RoleBindingWrapper) Subject(kind, name, namespace string) *RoleBindingWrapper {
	rb.Subjects = append(rb.Subjects, rbacv1.Subject{
		Kind:      kind,
		Name:      name,
		Namespace: namespace,
	})
	return rb
}

type ClusterRoleBindingWrapper struct{ rbacv1.ClusterRoleBinding }

func MakeClusterRoleBinding(name string) *ClusterRoleBindingWrapper {
	return &ClusterRoleBindingWrapper{
		Name: name,
	}
}

func (crb *ClusterRoleBindingWrapper) Obj() *rbacv1.ClusterRoleBinding {
	return &crb.ClusterRoleBinding
}

func (crb *ClusterRoleBindingWrapper) RoleRef(apiGroup, kind, name string) *ClusterRoleBindingWrapper {
	crb.ClusterRoleBinding.RoleRef = rbacv1.RoleRef{
		APIGroup: apiGroup,
		Kind:     kind,
		Name:     name,
	}
	return crb
}

// UserSubject adds a User subject. User subjects must carry the rbac.authorization.k8s.io API
// group; a subject without it matches nothing and the binding silently grants no access.
func (crb *ClusterRoleBindingWrapper) UserSubject(name string) *ClusterRoleBindingWrapper {
	crb.Subjects = append(crb.Subjects, rbacv1.Subject{
		Kind:     rbacv1.UserKind,
		APIGroup: rbacv1.GroupName,
		Name:     name,
	})
	return crb
}

type PreferredSchedulingTermsWrapper struct {
	terms []corev1.PreferredSchedulingTerm
}

func MakePreferredSchedulingTerms() *PreferredSchedulingTermsWrapper {
	return &PreferredSchedulingTermsWrapper{}
}

func (w *PreferredSchedulingTermsWrapper) Term(weight int32, key string, op corev1.NodeSelectorOperator, values ...string) *PreferredSchedulingTermsWrapper {
	w.terms = append(w.terms, corev1.PreferredSchedulingTerm{
		Weight: weight,
		Preference: corev1.NodeSelectorTerm{
			MatchExpressions: []corev1.NodeSelectorRequirement{
				{
					Key:      key,
					Operator: op,
					Values:   values,
				},
			},
		},
	})
	return w
}

func (w *PreferredSchedulingTermsWrapper) Obj() []corev1.PreferredSchedulingTerm {
	return w.terms
}

type NodeSelectorTermsWrapper struct {
	terms []corev1.NodeSelectorTerm
}

func MakeNodeSelectorTerms() *NodeSelectorTermsWrapper {
	return &NodeSelectorTermsWrapper{}
}

func (w *NodeSelectorTermsWrapper) Term(key string, op corev1.NodeSelectorOperator, values ...string) *NodeSelectorTermsWrapper {
	w.terms = append(w.terms, corev1.NodeSelectorTerm{
		MatchExpressions: []corev1.NodeSelectorRequirement{
			{
				Key:      key,
				Operator: op,
				Values:   values,
			},
		},
	})
	return w
}

func (w *NodeSelectorTermsWrapper) Obj() []corev1.NodeSelectorTerm {
	return w.terms
}

type DeviceTaintRuleWrapper struct{ resourcev1.DeviceTaintRule }

// MakeDeviceTaintRule creates a rule that taints every device in the cluster NoSchedule
// with the given key. Narrow it with Driver, Pool and Device.
func MakeDeviceTaintRule(name, key string) *DeviceTaintRuleWrapper {
	return &DeviceTaintRuleWrapper{
		resourcev1.DeviceTaintRule{
			Name: name,
			Spec: resourcev1.DeviceTaintRuleSpec{
				DeviceSelector: &resourcev1.DeviceTaintSelector{},
				Taint: resourcev1.DeviceTaint{
					Key:    key,
					Effect: resourcev1.DeviceTaintEffectNoSchedule,
				},
			},
		},
	}
}

func (w *DeviceTaintRuleWrapper) Driver(driver string) *DeviceTaintRuleWrapper {
	w.Spec.DeviceSelector.Driver = new(driver)
	return w
}

func (w *DeviceTaintRuleWrapper) Pool(pool string) *DeviceTaintRuleWrapper {
	w.Spec.DeviceSelector.Pool = new(pool)
	return w
}

func (w *DeviceTaintRuleWrapper) Device(device string) *DeviceTaintRuleWrapper {
	w.Spec.DeviceSelector.Device = new(device)
	return w
}

func (w *DeviceTaintRuleWrapper) Effect(effect resourcev1.DeviceTaintEffect) *DeviceTaintRuleWrapper {
	w.Spec.Taint.Effect = effect
	return w
}

// NoSelector drops the selector entirely, which the API describes as selecting no
// devices and resourceslice/tracker treats as selecting all of them.
func (w *DeviceTaintRuleWrapper) NoSelector() *DeviceTaintRuleWrapper {
	w.Spec.DeviceSelector = nil
	return w
}

func (w *DeviceTaintRuleWrapper) Obj() *resourcev1.DeviceTaintRule {
	return &w.DeviceTaintRule
}

type ResourceSliceWrapper struct{ resourcev1.ResourceSlice }

func MakeResourceSlice(name, driver string) *ResourceSliceWrapper {
	return &ResourceSliceWrapper{
		resourcev1.ResourceSlice{
			Name: name,
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

func (w *ResourceSliceWrapper) DeviceTaint(key string, effect resourcev1.DeviceTaintEffect) *ResourceSliceWrapper {
	if len(w.Spec.Devices) > 0 {
		last := &w.Spec.Devices[len(w.Spec.Devices)-1]
		last.Taints = append(last.Taints, resourcev1.DeviceTaint{Key: key, Effect: effect})
	}
	return w
}

func (w *ResourceSliceWrapper) NodeName(name string) *ResourceSliceWrapper {
	w.Spec.NodeName = &name
	return w
}

func (w *ResourceSliceWrapper) Obj() *resourcev1.ResourceSlice {
	return &w.ResourceSlice
}
