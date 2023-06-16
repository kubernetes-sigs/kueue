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
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// LocalQueueSpec defines the desired state of LocalQueue
type LocalQueueSpec struct {
	// clusterQueue is a reference to a clusterQueue that backs this localQueue.
	ClusterQueue ClusterQueueReference `json:"clusterQueue,omitempty"`
}

// ClusterQueueReference is the name of the ClusterQueue.
type ClusterQueueReference string

// LocalQueueStatus defines the observed state of LocalQueue
type LocalQueueStatus struct {
	// PendingWorkloads is the number of Workloads in the LocalQueue not yet admitted to a ClusterQueue
	// +optional
	PendingWorkloads int32 `json:"pendingWorkloads"`

	// AdmittedWorkloads is the number of workloads in this LocalQueue
	// admitted to a ClusterQueue and that haven't finished yet.
	// +optional
	AdmittedWorkloads int32 `json:"admittedWorkloads"`

	// Conditions hold the latest available observations of the LocalQueue
	// current state.
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions:omitempty"`

	// flavorUsage are the used quotas, by flavor currently in use by the
	// workloads assigned to this LocalQueue.
	// +listType=map
	// +listMapKey=name
	// +kubebuilder:validation:MaxItems=16
	// +optional
	FlavorUsage []LocalQueueFlavorUsage `json:"flavorUsage"`
}

const (
	// LocalQueueActive indicates that the ClusterQueue that backs the LocalQueue is active and
	// the LocalQueue can submit new workloads to its ClusterQueue.
	LocalQueueActive string = "Active"
)

type LocalQueueFlavorUsage struct {
	// name of the flavor.
	Name ResourceFlavorReference `json:"name"`

	// resources lists the quota usage for the resources in this flavor.
	// +listType=map
	// +listMapKey=name
	// +kubebuilder:validation:MaxItems=16
	Resources []LocalQueueResourceUsage `json:"resources"`
}

type LocalQueueResourceUsage struct {
	// name of the resource.
	Name corev1.ResourceName `json:"name"`

	// total is the total quantity of used quota.
	Total resource.Quantity `json:"total,omitempty"`
}

//+genclient
//+kubebuilder:object:root=true
//+kubebuilder:storageversion
//+kubebuilder:subresource:status
//+kubebuilder:printcolumn:name="ClusterQueue",JSONPath=".spec.clusterQueue",type=string,description="Backing ClusterQueue"
//+kubebuilder:printcolumn:name="Pending Workloads",JSONPath=".status.pendingWorkloads",type=integer,description="Number of pending workloads"
//+kubebuilder:printcolumn:name="Admitted Workloads",JSONPath=".status.admittedWorkloads",type=integer,description="Number of admitted workloads that haven't finished yet."
//+kubebuilder:resource:shortName={queue,queues}

// LocalQueue is the Schema for the localQueues API
type LocalQueue struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   LocalQueueSpec   `json:"spec,omitempty"`
	Status LocalQueueStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// LocalQueueList contains a list of LocalQueue
type LocalQueueList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []LocalQueue `json:"items"`
}

func init() {
	SchemeBuilder.Register(&LocalQueue{}, &LocalQueueList{})
}
