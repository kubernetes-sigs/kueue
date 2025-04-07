/*
Copyright The Kubernetes Authors.

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
	// +kubebuilder:validation:XValidation:rule="self == oldSelf", message="field is immutable"
	ClusterQueue ClusterQueueReference `json:"clusterQueue,omitempty"`

	// stopPolicy - if set to a value different from None, the LocalQueue is considered Inactive,
	// no new reservation being made.
	//
	// Depending on its value, its associated workloads will:
	//
	// - None - Workloads are admitted
	// - HoldAndDrain - Admitted workloads are evicted and Reserving workloads will cancel the reservation.
	// - Hold - Admitted workloads will run to completion and Reserving workloads will cancel the reservation.
	//
	// +optional
	// +kubebuilder:validation:Enum=None;Hold;HoldAndDrain
	// +kubebuilder:default="None"
	StopPolicy *StopPolicy `json:"stopPolicy,omitempty"`
}

// ClusterQueueReference is the name of the ClusterQueue.
// +kubebuilder:validation:MaxLength=253
// +kubebuilder:validation:Pattern="^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$"
type ClusterQueueReference string

type LocalQueueFlavorStatus struct {
	// name of the flavor.
	// +required
	// +kubebuilder:validation:Required
	Name ResourceFlavorReference `json:"name"`

	// resources used in the flavor.
	// +listType=set
	// +kubebuilder:validation:MaxItems=16
	// +optional
	Resources []corev1.ResourceName `json:"resources,omitempty"`

	// nodeLabels are labels that associate the ResourceFlavor with Nodes that
	// have the same labels.
	// +mapType=atomic
	// +kubebuilder:validation:MaxProperties=8
	// +optional
	NodeLabels map[string]string `json:"nodeLabels,omitempty"`

	// nodeTaints are taints that the nodes associated with this ResourceFlavor
	// have.
	// +listType=atomic
	// +kubebuilder:validation:MaxItems=8
	// +optional
	NodeTaints []corev1.Taint `json:"nodeTaints,omitempty"`

	// topology is the topology that associated with this ResourceFlavor.
	//
	// This is an alpha field and requires enabling the TopologyAwareScheduling
	// feature gate.
	//
	// +optional
	Topology *TopologyInfo `json:"topology,omitempty"`
}

type TopologyInfo struct {
	// name is the name of the topology.
	//
	// +required
	// +kubebuilder:validation:Required
	Name TopologyReference `json:"name"`

	// levels define the levels of topology.
	//
	// +required
	// +listType=atomic
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=8
	Levels []string `json:"levels"`
}

// LocalQueueStatus defines the observed state of LocalQueue
type LocalQueueStatus struct {
	// PendingWorkloads is the number of Workloads in the LocalQueue not yet admitted to a ClusterQueue
	// +optional
	PendingWorkloads int32 `json:"pendingWorkloads"`

	// reservingWorkloads is the number of workloads in this LocalQueue
	// reserving quota in a ClusterQueue and that haven't finished yet.
	// +optional
	ReservingWorkloads int32 `json:"reservingWorkloads"`

	// admittedWorkloads is the number of workloads in this LocalQueue
	// admitted to a ClusterQueue and that haven't finished yet.
	// +optional
	AdmittedWorkloads int32 `json:"admittedWorkloads"`

	// Conditions hold the latest available observations of the LocalQueue
	// current state.
	// +optional
	// +listType=map
	// +listMapKey=type
	// +patchStrategy=merge
	// +patchMergeKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`

	// flavorsReservation are the reserved quotas, by flavor currently in use by the
	// workloads assigned to this LocalQueue.
	// +listType=map
	// +listMapKey=name
	// +kubebuilder:validation:MaxItems=16
	// +optional
	FlavorsReservation []LocalQueueFlavorUsage `json:"flavorsReservation"`

	// flavorsUsage are the used quotas, by flavor currently in use by the
	// workloads assigned to this LocalQueue.
	// +listType=map
	// +listMapKey=name
	// +kubebuilder:validation:MaxItems=16
	// +optional
	FlavorUsage []LocalQueueFlavorUsage `json:"flavorUsage"`

	// flavors lists all currently available ResourceFlavors in specified ClusterQueue.
	// +listType=map
	// +listMapKey=name
	// +kubebuilder:validation:MaxItems=16
	// +optional
	Flavors []LocalQueueFlavorStatus `json:"flavors,omitempty"`
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

// +genclient
// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="ClusterQueue",JSONPath=".spec.clusterQueue",type=string,description="Backing ClusterQueue"
// +kubebuilder:printcolumn:name="Pending Workloads",JSONPath=".status.pendingWorkloads",type=integer,description="Number of pending workloads"
// +kubebuilder:printcolumn:name="Admitted Workloads",JSONPath=".status.admittedWorkloads",type=integer,description="Number of admitted workloads that haven't finished yet."
// +kubebuilder:resource:shortName={queue,queues,lq}

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
