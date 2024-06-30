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
	v1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// JobTemplateSpec describes the data a job should have when created from a template
type JobTemplateSpec struct {
	// Standard object's metadata.
	//
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// Specification of the desired behavior of the job.
	//
	// +kubebuilder:validation:Required
	Spec v1.JobSpec `json:"spec"`
}

// +genclient
// +kubebuilder:object:root=true

// JobTemplate is the Schema for the jobtemplate API
type JobTemplate struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// Template defines the jobs that will be created from this pod template.
	//
	// +kubebuilder:validation:Required
	Template JobTemplateSpec `json:"template,omitempty"`
}

// +kubebuilder:object:root=true

// JobTemplateList contains a list of JobTemplate
type JobTemplateList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`

	Items []JobTemplate `json:"items"`
}

func init() {
	SchemeBuilder.Register(&JobTemplate{}, &JobTemplateList{})
}
