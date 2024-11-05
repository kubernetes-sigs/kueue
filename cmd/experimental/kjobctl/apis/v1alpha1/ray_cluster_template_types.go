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
	rayv1 "github.com/ray-project/kuberay/ray-operator/apis/ray/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// RayClusterTemplateSpec describes the data a raycluster should have when created from a template
type RayClusterTemplateSpec struct {
	// Standard object's metadata.
	//
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// Specification of the desired behavior of the raycluster.
	//
	// +kubebuilder:validation:Required
	Spec rayv1.RayClusterSpec `json:"spec"`
}

// +genclient
// +kubebuilder:object:root=true

// RayClusterTemplate is the Schema for the rayclustertemplate API
type RayClusterTemplate struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// Template defines rayclusters that will be created from this raycluster template.
	//
	// +kubebuilder:validation:Required
	Template RayClusterTemplateSpec `json:"template,omitempty"`
}

// +kubebuilder:object:root=true

// RayClusterTemplateList contains a list of RayClusterTemplate
type RayClusterTemplateList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []RayClusterTemplate `json:"items"`
}

func init() {
	SchemeBuilder.Register(&RayClusterTemplate{}, &RayClusterTemplateList{})
}
