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

package v1beta2

import (
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// LocalQueueName is the name of the LocalQueue.
// It must be a DNS (RFC 1123) and has the maximum length of 253 characters.
//
// +kubebuilder:validation:MaxLength=253
// +kubebuilder:validation:Pattern="^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$"
type LocalQueueName string

// LocalQueueSpec defines the desired state of LocalQueue
type LocalQueueSpec struct {
	// clusterQueue is a reference to a clusterQueue that backs this localQueue.
	// +kubebuilder:validation:XValidation:rule="self == oldSelf", message="field is immutable"
	// +optional
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

	// fairSharing defines the properties of the LocalQueue when
	// participating in AdmissionFairSharing.  The values are only relevant
	// if AdmissionFairSharing is enabled in the Kueue configuration.
	// +optional
	FairSharing *FairSharing `json:"fairSharing,omitempty"`
}

type TopologyInfo struct {
	// name is the name of the topology.
	//
	// +required
	Name TopologyReference `json:"name"`

	// levels define the levels of topology.
	//
	// +required
	// +listType=atomic
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=16
	// +kubebuilder:validation:items:MaxLength=317
	Levels []string `json:"levels,omitempty"`
}

// LocalQueueStatus defines the observed state of LocalQueue
type LocalQueueStatus struct {
	// conditions hold the latest available observations of the LocalQueue
	// current state.
	// conditions are limited to 16 items.
	// +optional
	// +listType=map
	// +listMapKey=type
	// +patchStrategy=merge
	// +patchMergeKey=type
	// +kubebuilder:validation:MaxItems=16
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`

	// pendingWorkloads is the number of Workloads in the LocalQueue not yet admitted to a ClusterQueue
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

	// flavorsReservation are the reserved quotas, by flavor currently in use by the
	// workloads assigned to this LocalQueue.
	// +listType=map
	// +listMapKey=name
	// +kubebuilder:validation:MaxItems=16
	// +optional
	FlavorsReservation []LocalQueueFlavorUsage `json:"flavorsReservation,omitempty"`

	// flavorsUsage are the used quotas, by flavor currently in use by the
	// workloads assigned to this LocalQueue.
	// +listType=map
	// +listMapKey=name
	// +kubebuilder:validation:MaxItems=16
	// +optional
	FlavorsUsage []LocalQueueFlavorUsage `json:"flavorsUsage,omitempty"`

	// fairSharing contains the information about the current status of fair sharing.
	// +optional
	FairSharing *LocalQueueFairSharingStatus `json:"fairSharing,omitempty"`
}

// LocalQueueFairSharingStatus contains the information about the current status of Fair Sharing.
type LocalQueueFairSharingStatus struct {
	// weightedShare represents the maximum of the ratios of usage
	// above nominal quota to the lendable resources in the
	// Cohort, among all the resources provided by the Node, and
	// divided by the weight.  If zero, it means that the usage of
	// the Node is below the nominal quota.  If the Node has a
	// weight of zero and is borrowing, this will return
	// 9223372036854775807, the maximum possible share value.
	// +required
	WeightedShare int64 `json:"weightedShare"`

	// admissionFairSharingStatus represents information relevant to the Admission Fair Sharing
	// +optional
	AdmissionFairSharingStatus *LocalQueueAdmissionFairSharingStatus `json:"admissionFairSharingStatus,omitempty"`
}

type LocalQueueAdmissionFairSharingStatus struct {
	// consumedResources represents the aggregated usage of resources over time,
	// with decaying function applied.
	// The value is populated if usage consumption functionality is enabled in Kueue config.
	// +required
	ConsumedResources corev1.ResourceList `json:"consumedResources"`

	// lastUpdate is the time when share and consumed resources were updated.
	// +required
	LastUpdate metav1.Time `json:"lastUpdate"`
}

const (
	// LocalQueueActive indicates that the ClusterQueue that backs the LocalQueue is active and
	// the LocalQueue can submit new workloads to its ClusterQueue.
	LocalQueueActive string = "Active"
)

type LocalQueueFlavorUsage struct {
	// name of the flavor.
	// +required
	Name ResourceFlavorReference `json:"name"`

	// resources lists the quota usage for the resources in this flavor.
	// +listType=map
	// +listMapKey=name
	// +kubebuilder:validation:MaxItems=16
	// +required
	Resources []LocalQueueResourceUsage `json:"resources,omitempty"`
}

type LocalQueueResourceUsage struct {
	// name of the resource.
	// +required
	Name corev1.ResourceName `json:"name,omitempty"`

	// total is the total quantity of used quota.
	// +optional
	Total resource.Quantity `json:"total,omitempty"`
}

// +genclient
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="ClusterQueue",JSONPath=".spec.clusterQueue",type=string,description="Backing ClusterQueue"
// +kubebuilder:printcolumn:name="Pending Workloads",JSONPath=".status.pendingWorkloads",type=integer,description="Number of pending workloads"
// +kubebuilder:printcolumn:name="Admitted Workloads",JSONPath=".status.admittedWorkloads",type=integer,description="Number of admitted workloads that haven't finished yet."
// +kubebuilder:resource:shortName={queue,queues,lq}

// LocalQueue is the Schema for the localQueues API
type LocalQueue struct {
	metav1.TypeMeta `json:",inline"`
	// metadata is the metadata of the LocalQueue.
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// spec is the specification of the LocalQueue.
	// +optional
	Spec LocalQueueSpec `json:"spec"`
	// status is the status of the LocalQueue.
	// +optional
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

func (*LocalQueue) Hub() {}
