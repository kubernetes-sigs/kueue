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

package v1beta1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// +genclient
// +genclient:nonNamespaced
// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:printcolumn:name="Value",JSONPath=".value",type=integer,description="Value of workloadPriorityClass's Priority"

// WorkloadPriorityClass is the Schema for the workloadPriorityClass API
type WorkloadPriorityClass struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// value represents the integer value of this workloadPriorityClass. This is the actual priority that workloads
	// receive when jobs have the name of this class in their workloadPriorityClass label.
	// Changing the value of workloadPriorityClass doesn't affect the priority of workloads that were already created.
	Value int32 `json:"value"`

	// description is an arbitrary string that usually provides guidelines on
	// when this workloadPriorityClass should be used.
	// +optional
	Description string `json:"description,omitempty"`
}

// +kubebuilder:object:root=true

// WorkloadPriorityClassList contains a list of WorkloadPriorityClass
type WorkloadPriorityClassList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []WorkloadPriorityClass `json:"items"`
}

func init() {
	SchemeBuilder.Register(&WorkloadPriorityClass{}, &WorkloadPriorityClassList{})
}
