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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"sigs.k8s.io/kueue/cmd/experimental/kjobctl/apis/v1alpha1"
)

// RayJobTemplateWrapper wraps a RayJobTemplate.
type RayJobTemplateWrapper struct{ v1alpha1.RayJobTemplate }

// MakeRayJobTemplate creates a wrapper for a RayJobTemplate
func MakeRayJobTemplate(name, ns string) *RayJobTemplateWrapper {
	return &RayJobTemplateWrapper{
		RayJobTemplate: v1alpha1.RayJobTemplate{
			ObjectMeta: metav1.ObjectMeta{
				Name:      name,
				Namespace: ns,
			},
		},
	}
}

// Obj returns the inner RayJobTemplate.
func (w *RayJobTemplateWrapper) Obj() *v1alpha1.RayJobTemplate {
	return &w.RayJobTemplate
}

// WithWorkerGroupSpec add worker group to the ray cluster template.
func (w *RayJobTemplateWrapper) WithWorkerGroupSpec(spec rayv1.WorkerGroupSpec) *RayJobTemplateWrapper {
	if w.Template.Spec.RayClusterSpec == nil {
		w.Template.Spec.RayClusterSpec = &rayv1.RayClusterSpec{}
	}

	w.Template.Spec.RayClusterSpec.WorkerGroupSpecs = append(w.Template.Spec.RayClusterSpec.WorkerGroupSpecs, spec)

	return w
}

// Clone RayJobTemplateWrapper.
func (w *RayJobTemplateWrapper) Clone() *RayJobTemplateWrapper {
	return &RayJobTemplateWrapper{
		RayJobTemplate: *w.RayJobTemplate.DeepCopy(),
	}
}

// WithVolume add volume to the job template.
func (w *RayJobTemplateWrapper) WithVolume(name, localObjectReferenceName string) *RayJobTemplateWrapper {
	if w.Template.Spec.RayClusterSpec == nil {
		w.Template.Spec.RayClusterSpec = &rayv1.RayClusterSpec{}
	}

	headGroupSpec := &w.Template.Spec.RayClusterSpec.HeadGroupSpec
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

	for index := range w.Template.Spec.RayClusterSpec.WorkerGroupSpecs {
		workerGroupSpec := &w.Template.Spec.RayClusterSpec.WorkerGroupSpecs[index]
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

// WithEnvVar add volume to the job template.
func (w *RayJobTemplateWrapper) WithEnvVar(envVar corev1.EnvVar) *RayJobTemplateWrapper {
	if w.Template.Spec.RayClusterSpec == nil {
		w.Template.Spec.RayClusterSpec = &rayv1.RayClusterSpec{}
	}

	headGroupSpec := &w.Template.Spec.RayClusterSpec.HeadGroupSpec
	for j := range headGroupSpec.Template.Spec.InitContainers {
		container := &headGroupSpec.Template.Spec.InitContainers[j]
		container.Env = append(container.Env, envVar)
	}
	for j := range headGroupSpec.Template.Spec.Containers {
		container := &headGroupSpec.Template.Spec.Containers[j]
		container.Env = append(container.Env, envVar)
	}

	for i := range w.Template.Spec.RayClusterSpec.WorkerGroupSpecs {
		workerGroupSpec := &w.Template.Spec.RayClusterSpec.WorkerGroupSpecs[i]
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
func (w *RayJobTemplateWrapper) WithVolumeMount(volumeMount corev1.VolumeMount) *RayJobTemplateWrapper {
	if w.Template.Spec.RayClusterSpec == nil {
		w.Template.Spec.RayClusterSpec = &rayv1.RayClusterSpec{}
	}

	headGroupSpec := &w.Template.Spec.RayClusterSpec.HeadGroupSpec
	for j := range headGroupSpec.Template.Spec.InitContainers {
		container := &headGroupSpec.Template.Spec.InitContainers[j]
		container.VolumeMounts = append(container.VolumeMounts, volumeMount)
	}
	for j := range headGroupSpec.Template.Spec.Containers {
		container := &headGroupSpec.Template.Spec.Containers[j]
		container.VolumeMounts = append(container.VolumeMounts, volumeMount)
	}

	for i := range w.Template.Spec.RayClusterSpec.WorkerGroupSpecs {
		workerGroupSpec := &w.Template.Spec.RayClusterSpec.WorkerGroupSpecs[i]
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

// Entrypoint set entrypoint.
func (w *RayJobTemplateWrapper) Entrypoint(entrypoint string) *RayJobTemplateWrapper {
	w.Template.Spec.Entrypoint = entrypoint
	return w
}

// Replicas set Replicas on WorkerGroupSpec.
func (w *RayJobTemplateWrapper) Replicas(groupName string, replicas int32) *RayJobTemplateWrapper {
	if w.Template.Spec.RayClusterSpec == nil {
		return w
	}

	for i := range w.Template.Spec.RayClusterSpec.WorkerGroupSpecs {
		if w.Template.Spec.RayClusterSpec.WorkerGroupSpecs[i].GroupName == groupName {
			w.Template.Spec.RayClusterSpec.WorkerGroupSpecs[i].Replicas = &replicas
			break
		}
	}

	return w
}

// MinReplicas set MinReplicas on WorkerGroupSpec.
func (w *RayJobTemplateWrapper) MinReplicas(groupName string, minReplicas int32) *RayJobTemplateWrapper {
	if w.Template.Spec.RayClusterSpec == nil {
		return w
	}

	for i := range w.Template.Spec.RayClusterSpec.WorkerGroupSpecs {
		if w.Template.Spec.RayClusterSpec.WorkerGroupSpecs[i].GroupName == groupName {
			w.Template.Spec.RayClusterSpec.WorkerGroupSpecs[i].MinReplicas = &minReplicas
			break
		}
	}

	return w
}

// MaxReplicas set MaxReplicas on WorkerGroupSpec.
func (w *RayJobTemplateWrapper) MaxReplicas(groupName string, maxReplicas int32) *RayJobTemplateWrapper {
	if w.Template.Spec.RayClusterSpec == nil {
		return w
	}

	for i := range w.Template.Spec.RayClusterSpec.WorkerGroupSpecs {
		if w.Template.Spec.RayClusterSpec.WorkerGroupSpecs[i].GroupName == groupName {
			w.Template.Spec.RayClusterSpec.WorkerGroupSpecs[i].MaxReplicas = &maxReplicas
			break
		}
	}

	return w
}
