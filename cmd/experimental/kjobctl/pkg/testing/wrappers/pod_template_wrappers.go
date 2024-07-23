/*
Copyright 2024 The Kubernetes Authors.

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

package wrappers

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// PodTemplateWrapper wraps a PodTemplate.
type PodTemplateWrapper struct{ corev1.PodTemplate }

// MakePodTemplate creates a wrapper for a PodTemplate.
func MakePodTemplate(name, ns string) *PodTemplateWrapper {
	return &PodTemplateWrapper{
		PodTemplate: corev1.PodTemplate{
			ObjectMeta: metav1.ObjectMeta{Name: name, Namespace: ns},
		},
	}
}

// Obj returns the inner PodTemplate.
func (p *PodTemplateWrapper) Obj() *corev1.PodTemplate {
	return &p.PodTemplate
}

// Clone clone PodTemplateWrapper.
func (p *PodTemplateWrapper) Clone() *PodTemplateWrapper {
	return &PodTemplateWrapper{
		PodTemplate: *p.PodTemplate.DeepCopy(),
	}
}

// WithInitContainer add container to the pod template.
func (p *PodTemplateWrapper) WithInitContainer(container corev1.Container) *PodTemplateWrapper {
	p.PodTemplate.Template.Spec.InitContainers = append(p.PodTemplate.Template.Spec.InitContainers, container)
	return p
}

// WithContainer add container to the pod template.
func (p *PodTemplateWrapper) WithContainer(container corev1.Container) *PodTemplateWrapper {
	p.PodTemplate.Template.Spec.Containers = append(p.PodTemplate.Template.Spec.Containers, container)
	return p
}

// WithVolume add volume to the pod template.
func (p *PodTemplateWrapper) WithVolume(name, localObjectReferenceName string) *PodTemplateWrapper {
	p.Template.Spec.Volumes = append(p.Template.Spec.Volumes, corev1.Volume{
		Name: name,
		VolumeSource: corev1.VolumeSource{
			ConfigMap: &corev1.ConfigMapVolumeSource{
				LocalObjectReference: corev1.LocalObjectReference{
					Name: localObjectReferenceName,
				},
			},
		},
	})
	return p
}

// WithEnvVar add volume to the pod template.
func (p *PodTemplateWrapper) WithEnvVar(envVar corev1.EnvVar) *PodTemplateWrapper {
	for index := range p.Template.Spec.InitContainers {
		p.Template.Spec.InitContainers[index].Env =
			append(p.Template.Spec.InitContainers[index].Env, envVar)
	}
	for index := range p.Template.Spec.Containers {
		p.Template.Spec.Containers[index].Env =
			append(p.Template.Spec.Containers[index].Env, envVar)
	}
	return p
}

// WithVolumeMount add volume mount to pod templates.
func (p *PodTemplateWrapper) WithVolumeMount(volumeMount corev1.VolumeMount) *PodTemplateWrapper {
	for index := range p.Template.Spec.InitContainers {
		p.Template.Spec.InitContainers[index].VolumeMounts =
			append(p.Template.Spec.InitContainers[index].VolumeMounts, volumeMount)
	}
	for index := range p.Template.Spec.Containers {
		p.Template.Spec.Containers[index].VolumeMounts =
			append(p.Template.Spec.Containers[index].VolumeMounts, volumeMount)
	}
	return p
}

// Command set command to primary pod templates.
func (p *PodTemplateWrapper) Command(command []string) *PodTemplateWrapper {
	if len(p.Template.Spec.Containers) > 0 {
		p.Template.Spec.Containers[0].Command = command
	}
	return p
}

// WithRequest set command to primary pod templates.
func (p *PodTemplateWrapper) WithRequest(key corev1.ResourceName, value resource.Quantity) *PodTemplateWrapper {
	if len(p.Template.Spec.Containers) > 0 {
		if p.Template.Spec.Containers[0].Resources.Requests == nil {
			p.Template.Spec.Containers[0].Resources.Requests = make(corev1.ResourceList)
		}
		p.Template.Spec.Containers[0].Resources.Requests[key] = value
	}
	return p
}

// TTY set tty=true on primary pod templates.
func (p *PodTemplateWrapper) TTY() *PodTemplateWrapper {
	if len(p.Template.Spec.Containers) > 0 {
		p.Template.Spec.Containers[0].TTY = true
	}
	return p
}

// Stdin set stdin=true on primary pod templates.
func (p *PodTemplateWrapper) Stdin() *PodTemplateWrapper {
	if len(p.Template.Spec.Containers) > 0 {
		p.Template.Spec.Containers[0].Stdin = true
	}
	return p
}
