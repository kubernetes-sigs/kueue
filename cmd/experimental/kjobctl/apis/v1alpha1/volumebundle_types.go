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

package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// VolumeBundleSpec defines the desired state of VolumeBundle
// +kubebuilder:validation:XValidation:rule="self.mountPoints.map(x, x.name in self.volumes.map(y, y.name))", message="mountPoint name must match a volume name"
type VolumeBundleSpec struct {
	// volumes is a set of volumes that will be added to all pods of the job.
	// +listType=map
	// +listMapKey=name
	// +kubebuilder:validation:MinItems=1
	Volumes []corev1.Volume `json:"volumes"`

	// containerVolumeMounts is a list of locations in each container of a pod where the volumes will be mounted.
	// +listType=map
	// +listMapKey=mountPath
	// +kubebuilder:validation:MinItems=1
	ContainerVolumeMounts []corev1.VolumeMount `json:"containerVolumeMounts"`

	// envVars are environment variables that refer to absolute paths in the container filesystem.
	// These key/value pairs will be available in containers as environment variables.
	// +optional
	// +kubebuilder:validation:XValidation:rule="self.all(x, x.name.matches('^[A-Za-z_][A-Za-z0-9_]*$') )", message="invalid environment variable name"
	EnvVars []corev1.EnvVar `json:"envVars,omitempty"`
}

// +genclient
// +kubebuilder:object:root=true

// VolumeBundle is the Schema for the volumebundles API
type VolumeBundle struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec VolumeBundleSpec `json:"spec,omitempty"`
}

// +kubebuilder:object:root=true

// VolumeBundleList contains a list of VolumeBundle
type VolumeBundleList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []VolumeBundle `json:"items"`
}

func init() {
	SchemeBuilder.Register(&VolumeBundle{}, &VolumeBundleList{})
}
