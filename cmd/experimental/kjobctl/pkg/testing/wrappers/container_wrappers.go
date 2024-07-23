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
)

// ContainerWrapper wraps a Container.
type ContainerWrapper struct{ corev1.Container }

// MakeContainer creates a wrapper for a Container
func MakeContainer(name, image string) *ContainerWrapper {
	return &ContainerWrapper{
		Container: corev1.Container{Name: name, Image: image},
	}
}

// Obj returns the inner Container.
func (c *ContainerWrapper) Obj() *corev1.Container {
	return &c.Container
}

// Command set command.
func (c *ContainerWrapper) Command(command ...string) *ContainerWrapper {
	c.Container.Command = command
	return c
}

// WithRequest add Request  to the ResourceList.
func (c *ContainerWrapper) WithRequest(resourceName corev1.ResourceName, quantity resource.Quantity) *ContainerWrapper {
	if c.Resources.Requests == nil {
		c.Resources.Requests = corev1.ResourceList{}
	}
	c.Resources.Requests[resourceName] = quantity
	return c
}

// WithResources set Resources.
func (c *ContainerWrapper) WithResources(resourceRequirements corev1.ResourceRequirements) *ContainerWrapper {
	c.Container.Resources = resourceRequirements
	return c
}

// WithEnvVar add EnvVar to Env.
func (c *ContainerWrapper) WithEnvVar(envVar corev1.EnvVar) *ContainerWrapper {
	c.Container.Env = append(c.Container.Env, envVar)
	return c
}

// WithVolumeMount add VolumeMount to VolumeMounts.
func (c *ContainerWrapper) WithVolumeMount(volumeMount corev1.VolumeMount) *ContainerWrapper {
	c.Container.VolumeMounts = append(c.Container.VolumeMounts, volumeMount)
	return c
}
