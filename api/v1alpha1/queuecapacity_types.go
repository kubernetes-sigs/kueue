/*
Copyright 2021 Google LLC.

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

// QueueCapacitySpec defines the desired state of QueueCapacity
type QueueCapacitySpec struct {
	// group that this QueueCapacity belongs to. QueueCapacities that belong to
	// the same group can borrow unsued guaranteed resources from each other.
	// Typically, a group represents a VM family (ex: spot VMs, Nvidia GPU VMs).
	// If empty, this QueueCapacity cannot borrow capacity from any other
	// QueueCapacity.
	Group string `json:"group,omitempty"`

	// requestableResources define the allowed total resource requests of running
	// workloads assigned to this QueueCapacity.
	RequestableResources ResourceCapacities `json:"requestableResources,omitempty"`

	// affinity added to the pods of the workloads assigned to this QueueCapacity.
	// This enforces pods to land on the VMs that this QueueCapacity manages the
	// usage for.
	// Typically, QueueCapacities that belong to the same group share all or
	// some of the rules defined here.
	// Example:
	// matchExpressions:
	// - key: cloud.google.com/gke-spot
	//   operator: Exists
	Affinity *corev1.NodeSelectorTerm `json:"affinity,omitempty"`

	// tolerations added to the pods of workloads assigned to this QueueCapacity.
	// This allows pods to land on the VMs that this QueueCapacity manages the
	// usage for.
	// Typically, QueueCapacities that belong to the same group share all or
	// some of the tolerations defined here.
	// +listType=map
	// +listMapKey=key
	Tolerations []corev1.Toleration `json:"tolerations,omitempty"`
}

type ResourceCapacities struct {
	// guaranteed amout of resouce requests that are available to be used by
	// running workloads assigned to this QueueCapacity.
	Guaranteed corev1.ResourceList `json:"guaranteed,omitempty"`

	// limit amout of resouce requests that could be in use by running workloads
	// assigned to this QueueCapacity. Some of the resources can be borrowed from
	// other QueueCapaties' unused guranteed resource requests.
	Limit corev1.ResourceList `json:"limit,omitempty"`
}

// QueueCapacityStatus defines the observed state of QueueCapacity
type QueueCapacityStatus struct {
}

//+kubebuilder:object:root=true
//+kubebuilder:resource:scope=Cluster
//+kubebuilder:subresource:status

// QueueCapacity is the Schema for the queuecapacities API
type QueueCapacity struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   QueueCapacitySpec   `json:"spec,omitempty"`
	Status QueueCapacityStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// QueueCapacityList contains a list of QueueCapacity
type QueueCapacityList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []QueueCapacity `json:"items"`
}

func init() {
	SchemeBuilder.Register(&QueueCapacity{}, &QueueCapacityList{})
}
