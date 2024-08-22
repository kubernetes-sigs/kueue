/*
Copyright 2023 The Kubernetes Authors.

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
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"

	"sigs.k8s.io/kueue/pkg/constants"
	controllerconsts "sigs.k8s.io/kueue/pkg/controller/constants"
)

// PodWrapper wraps a Pod.
type PodWrapper struct {
	corev1.Pod
}

// MakePod creates a wrapper for a pod with a single container.
func MakePod(name, ns string) *PodWrapper {
	return &PodWrapper{corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:        name,
			Namespace:   ns,
			Annotations: make(map[string]string, 1),
		},
		Spec: corev1.PodSpec{
			RestartPolicy: corev1.RestartPolicyNever,
			Containers: []corev1.Container{
				{
					Name:      "c",
					Image:     "pause",
					Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{}},
				},
			},
			SchedulingGates: make([]corev1.PodSchedulingGate, 0),
		},
	}}
}

// Obj returns the inner Pod.
func (p *PodWrapper) Obj() *corev1.Pod {
	return &p.Pod
}

// Clone returns deep copy of the Pod.
func (p *PodWrapper) Clone() *PodWrapper {
	return &PodWrapper{Pod: *p.DeepCopy()}
}

// Queue updates the queue name of the Pod
func (p *PodWrapper) Queue(queue string) *PodWrapper {
	if p.Labels == nil {
		p.Labels = make(map[string]string)
	}
	p.Labels[controllerconsts.QueueLabel] = queue
	return p
}

// Label sets the label of the Pod
func (p *PodWrapper) Label(k, v string) *PodWrapper {
	if p.Labels == nil {
		p.Labels = make(map[string]string)
	}
	p.Labels[k] = v
	return p
}

func (p *PodWrapper) Annotation(key, content string) *PodWrapper {
	p.Annotations[key] = content
	return p
}

// ParentWorkload sets the parent-workload annotation
func (p *PodWrapper) ParentWorkload(parentWorkload string) *PodWrapper {
	p.Annotations[controllerconsts.ParentWorkloadAnnotation] = parentWorkload
	return p
}

// KueueSchedulingGate adds kueue scheduling gate to the Pod
func (p *PodWrapper) KueueSchedulingGate() *PodWrapper {
	if p.Spec.SchedulingGates == nil {
		p.Spec.SchedulingGates = make([]corev1.PodSchedulingGate, 0)
	}
	p.Spec.SchedulingGates = append(p.Spec.SchedulingGates, corev1.PodSchedulingGate{Name: "kueue.x-k8s.io/admission"})
	return p
}

// KueueFinalizer adds kueue finalizer to the Pod
func (p *PodWrapper) KueueFinalizer() *PodWrapper {
	if p.ObjectMeta.Finalizers == nil {
		p.ObjectMeta.Finalizers = make([]string, 0)
	}
	p.ObjectMeta.Finalizers = append(p.ObjectMeta.Finalizers, constants.ManagedByKueueLabel)
	return p
}

// NodeSelector adds a node selector to the Pod.
func (p *PodWrapper) NodeSelector(k, v string) *PodWrapper {
	if p.Spec.NodeSelector == nil {
		p.Spec.NodeSelector = make(map[string]string, 1)
	}

	p.Spec.NodeSelector[k] = v
	return p
}

// Request adds a resource request to the default container.
func (p *PodWrapper) Request(r corev1.ResourceName, v string) *PodWrapper {
	p.Spec.Containers[0].Resources.Requests[r] = resource.MustParse(v)
	return p
}

func (p *PodWrapper) Image(image string, args []string) *PodWrapper {
	p.Spec.Containers[0].Image = image
	p.Spec.Containers[0].Args = args
	return p
}

// OwnerReference adds a ownerReference to the default container.
func (p *PodWrapper) OwnerReference(ownerName string, ownerGVK schema.GroupVersionKind) *PodWrapper {
	p.ObjectMeta.OwnerReferences = append(
		p.ObjectMeta.OwnerReferences,
		metav1.OwnerReference{
			APIVersion: ownerGVK.GroupVersion().String(),
			Kind:       ownerGVK.Kind,
			Name:       ownerName,
			UID:        types.UID(ownerName),
			Controller: ptr.To(true),
		},
	)

	return p
}

// UID updates the uid of the Pod.
func (p *PodWrapper) UID(uid string) *PodWrapper {
	p.ObjectMeta.UID = types.UID(uid)
	return p
}

// StatusConditions updates status conditions of the Pod.
func (p *PodWrapper) StatusConditions(conditions ...corev1.PodCondition) *PodWrapper {
	p.Pod.Status.Conditions = conditions
	return p
}

// StatusPhase updates status phase of the Pod.
func (p *PodWrapper) StatusPhase(ph corev1.PodPhase) *PodWrapper {
	p.Pod.Status.Phase = ph
	return p
}

// PriorityClass updates the Pod priorityclass.
func (p *PodWrapper) PriorityClass(pc string) *PodWrapper {
	p.Spec.PriorityClassName = pc
	return p
}
