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

// WorkerGroupSpecWrapper wraps a WorkerGroupSpec.
type WorkerGroupSpecWrapper struct{ rayv1.WorkerGroupSpec }

// MakeWorkerGroupSpec creates a wrapper for a WorkerGroupSpec
func MakeWorkerGroupSpec(groupName string) *WorkerGroupSpecWrapper {
	return &WorkerGroupSpecWrapper{WorkerGroupSpec: rayv1.WorkerGroupSpec{GroupName: groupName}}
}

// Obj returns the inner WorkerGroupSpec.
func (w *WorkerGroupSpecWrapper) Obj() *rayv1.WorkerGroupSpec {
	return &w.WorkerGroupSpec
}

// Clone WorkerGroupSpecWrapper.
func (w *WorkerGroupSpecWrapper) Clone() *WorkerGroupSpecWrapper {
	return &WorkerGroupSpecWrapper{
		WorkerGroupSpec: *w.WorkerGroupSpec.DeepCopy(),
	}
}

// WithInitContainer add init container to the pod template.
func (w *WorkerGroupSpecWrapper) WithInitContainer(container corev1.Container) *WorkerGroupSpecWrapper {
	w.Template.Spec.InitContainers = append(w.Template.Spec.InitContainers, container)
	return w
}

// WithContainer add container to the pod template.
func (w *WorkerGroupSpecWrapper) WithContainer(container corev1.Container) *WorkerGroupSpecWrapper {
	w.Template.Spec.Containers = append(w.Template.Spec.Containers, container)
	return w
}

// WithVolume add volume to the WorkerGroupSpec.
func (w *WorkerGroupSpecWrapper) WithVolume(name, localObjectReferenceName string) *WorkerGroupSpecWrapper {
	w.Template.Spec.Volumes = append(w.Template.Spec.Volumes, corev1.Volume{
		Name: name,
		VolumeSource: corev1.VolumeSource{
			ConfigMap: &corev1.ConfigMapVolumeSource{
				LocalObjectReference: corev1.LocalObjectReference{
					Name: localObjectReferenceName,
				},
			},
		},
	})

	return w
}

// Replicas set replicas on WorkerGroupSpec.
func (w *WorkerGroupSpecWrapper) Replicas(replicas int32) *WorkerGroupSpecWrapper {
	w.WorkerGroupSpec.Replicas = &replicas
	return w
}

// MinReplicas set minReplicas on WorkerGroupSpec.
func (w *WorkerGroupSpecWrapper) MinReplicas(minReplicas int32) *WorkerGroupSpecWrapper {
	w.WorkerGroupSpec.MinReplicas = &minReplicas
	return w
}

// MaxReplicas set maxReplicas on WorkerGroupSpec.
func (w *WorkerGroupSpecWrapper) MaxReplicas(maxReplicas int32) *WorkerGroupSpecWrapper {
	w.WorkerGroupSpec.MaxReplicas = &maxReplicas
	return w
}
