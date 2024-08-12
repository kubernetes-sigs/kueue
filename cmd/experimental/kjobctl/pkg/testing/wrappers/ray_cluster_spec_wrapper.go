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
	rayv1 "github.com/ray-project/kuberay/ray-operator/apis/ray/v1"
	corev1 "k8s.io/api/core/v1"
)

// RayClusterSpecWrapper wraps a RayClusterSpec.
type RayClusterSpecWrapper struct{ rayv1.RayClusterSpec }

// MakeRayClusterSpec creates a wrapper for a RayClusterSpec
func MakeRayClusterSpec() *RayClusterSpecWrapper {
	return &RayClusterSpecWrapper{}
}

// FromRayClusterSpec creates a wrapper for a RayClusterSpec.
func FromRayClusterSpec(spec rayv1.RayClusterSpec) *RayClusterSpecWrapper {
	return &RayClusterSpecWrapper{
		RayClusterSpec: spec,
	}
}

// Obj returns the inner RayClusterSpec.
func (w *RayClusterSpecWrapper) Obj() *rayv1.RayClusterSpec {
	return &w.RayClusterSpec
}

// Clone RayClusterSpecWrapper.
func (w *RayClusterSpecWrapper) Clone() *RayClusterSpecWrapper {
	return &RayClusterSpecWrapper{
		RayClusterSpec: *w.RayClusterSpec.DeepCopy(),
	}
}

// HeadGroupSpec add worker group to the ray cluster spec.
func (w *RayClusterSpecWrapper) HeadGroupSpec(spec rayv1.HeadGroupSpec) *RayClusterSpecWrapper {
	w.RayClusterSpec.HeadGroupSpec = spec
	return w
}

// WithWorkerGroupSpec add worker group to the ray cluster spec.
func (w *RayClusterSpecWrapper) WithWorkerGroupSpec(spec rayv1.WorkerGroupSpec) *RayClusterSpecWrapper {
	w.RayClusterSpec.WorkerGroupSpecs = append(w.RayClusterSpec.WorkerGroupSpecs, spec)

	return w
}

// WithVolume add volume to the ray cluster spec.
func (w *RayClusterSpecWrapper) WithVolume(name, localObjectReferenceName string) *RayClusterSpecWrapper {
	headGroupSpec := &w.RayClusterSpec.HeadGroupSpec
	headGroupSpec.Template.Spec.Volumes =
		append(headGroupSpec.Template.Spec.Volumes, corev1.Volume{
			Name: name,
			VolumeSource: corev1.VolumeSource{
				ConfigMap: &corev1.ConfigMapVolumeSource{
					LocalObjectReference: corev1.LocalObjectReference{
						Name: localObjectReferenceName,
					},
				},
			},
		})

	for index := range w.RayClusterSpec.WorkerGroupSpecs {
		workerGroupSpec := &w.RayClusterSpec.WorkerGroupSpecs[index]
		workerGroupSpec.Template.Spec.Volumes =
			append(workerGroupSpec.Template.Spec.Volumes, corev1.Volume{
				Name: name,
				VolumeSource: corev1.VolumeSource{
					ConfigMap: &corev1.ConfigMapVolumeSource{
						LocalObjectReference: corev1.LocalObjectReference{
							Name: localObjectReferenceName,
						},
					},
				},
			})
	}

	return w
}

// WithEnvVar add volume to the ray cluster spec.
func (w *RayClusterSpecWrapper) WithEnvVar(envVar corev1.EnvVar) *RayClusterSpecWrapper {
	headGroupSpec := &w.RayClusterSpec.HeadGroupSpec
	for j := range headGroupSpec.Template.Spec.InitContainers {
		container := &headGroupSpec.Template.Spec.InitContainers[j]
		container.Env = append(container.Env, envVar)
	}
	for j := range headGroupSpec.Template.Spec.Containers {
		container := &headGroupSpec.Template.Spec.Containers[j]
		container.Env = append(container.Env, envVar)
	}

	for i := range w.RayClusterSpec.WorkerGroupSpecs {
		workerGroupSpec := &w.RayClusterSpec.WorkerGroupSpecs[i]
		for j := range workerGroupSpec.Template.Spec.InitContainers {
			container := &workerGroupSpec.Template.Spec.InitContainers[j]
			container.Env = append(container.Env, envVar)
		}
		for j := range workerGroupSpec.Template.Spec.Containers {
			container := &workerGroupSpec.Template.Spec.Containers[j]
			container.Env = append(container.Env, envVar)
		}
	}

	return w
}

// WithVolumeMount add volume mount to pod templates.
func (w *RayClusterSpecWrapper) WithVolumeMount(volumeMount corev1.VolumeMount) *RayClusterSpecWrapper {
	headGroupSpec := &w.RayClusterSpec.HeadGroupSpec
	for j := range headGroupSpec.Template.Spec.InitContainers {
		container := &headGroupSpec.Template.Spec.InitContainers[j]
		container.VolumeMounts = append(container.VolumeMounts, volumeMount)
	}
	for j := range headGroupSpec.Template.Spec.Containers {
		container := &headGroupSpec.Template.Spec.Containers[j]
		container.VolumeMounts = append(container.VolumeMounts, volumeMount)
	}

	for i := range w.RayClusterSpec.WorkerGroupSpecs {
		workerGroupSpec := &w.RayClusterSpec.WorkerGroupSpecs[i]
		for j := range workerGroupSpec.Template.Spec.InitContainers {
			container := &workerGroupSpec.Template.Spec.InitContainers[j]
			container.VolumeMounts = append(container.VolumeMounts, volumeMount)
		}
		for j := range workerGroupSpec.Template.Spec.Containers {
			container := &workerGroupSpec.Template.Spec.Containers[j]
			container.VolumeMounts = append(container.VolumeMounts, volumeMount)
		}
	}

	return w
}

// Replicas set Replicas on WorkerGroupSpec.
func (w *RayClusterSpecWrapper) Replicas(groupName string, replicas int32) *RayClusterSpecWrapper {
	for i := range w.RayClusterSpec.WorkerGroupSpecs {
		if w.RayClusterSpec.WorkerGroupSpecs[i].GroupName == groupName {
			w.RayClusterSpec.WorkerGroupSpecs[i].Replicas = &replicas
			break
		}
	}

	return w
}

// MinReplicas set MinReplicas on WorkerGroupSpec.
func (w *RayClusterSpecWrapper) MinReplicas(groupName string, minReplicas int32) *RayClusterSpecWrapper {
	for i := range w.RayClusterSpec.WorkerGroupSpecs {
		if w.RayClusterSpec.WorkerGroupSpecs[i].GroupName == groupName {
			w.RayClusterSpec.WorkerGroupSpecs[i].MinReplicas = &minReplicas
			break
		}
	}

	return w
}

// MaxReplicas set MaxReplicas on WorkerGroupSpec.
func (w *RayClusterSpecWrapper) MaxReplicas(groupName string, maxReplicas int32) *RayClusterSpecWrapper {
	for i := range w.RayClusterSpec.WorkerGroupSpecs {
		if w.RayClusterSpec.WorkerGroupSpecs[i].GroupName == groupName {
			w.RayClusterSpec.WorkerGroupSpecs[i].MaxReplicas = &maxReplicas
			break
		}
	}

	return w
}
