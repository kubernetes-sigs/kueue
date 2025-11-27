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

// FairSharing contains the properties of the ClusterQueue or Cohort,
// when participating in FairSharing.
//
// Fair Sharing is compatible with Hierarchical Cohorts (any Cohort
// which has a parent) as of v0.11. Using these features together in
// V0.9 and V0.10 is unsupported, and results in undefined behavior.
type FairSharing struct {
	// weight gives a comparative advantage to this ClusterQueue
	// or Cohort when competing for unused resources in the
	// Cohort.  The share is based on the dominant resource usage
	// above nominal quotas for each resource, divided by the
	// weight.  Admission prioritizes scheduling workloads from
	// ClusterQueues and Cohorts with the lowest share and
	// preempting workloads from the ClusterQueues and Cohorts
	// with the highest share.  A zero weight implies infinite
	// share value, meaning that this Node will always be at
	// disadvantage against other ClusterQueues and Cohorts.
	// When not 0, Weight must be greater than 10^-9.
	// +kubebuilder:default=1
	Weight *resource.Quantity `json:"weight,omitempty"`
}

// FairSharingStatus contains the information about the current status of Fair Sharing.
type FairSharingStatus struct {
	// weightedShare represents the maximum of the ratios of usage
	// above nominal quota to the lendable resources in the
	// Cohort, among all the resources provided by the Node, and
	// divided by the weight.  If zero, it means that the usage of
	// the Node is below the nominal quota.  If the Node has a
	// weight of zero and is borrowing, this will return
	// 9223372036854775807, the maximum possible share value.
	WeightedShare int64 `json:"weightedShare"`

	// admissionFairSharingStatus represents information relevant to the Admission Fair Sharing
	// +optional
	AdmissionFairSharingStatus *AdmissionFairSharingStatus `json:"admissionFairSharingStatus,omitempty"`
}

type AdmissionFairSharingStatus struct {
	// consumedResources represents the aggregated usage of resources over time,
	// with decaying function applied.
	// The value is populated if usage consumption functionality is enabled in Kueue config.
	// +required
	ConsumedResources corev1.ResourceList `json:"consumedResources"`

	// lastUpdate is the time when share and consumed resources were updated.
	// +required
	LastUpdate metav1.Time `json:"lastUpdate"`
}

type AdmissionScope struct {
	// admissionMode indicates which mode for AdmissionFairSharing should be used
	// in the AdmissionScope. Possible values are:
	// - UsageBasedAdmissionFairSharing
	// - NoAdmissionFairSharing
	//
	// +required
	AdmissionMode AdmissionMode `json:"admissionMode"`
}

type AdmissionMode string

const (
	// AdmissionFairSharing based on usage, with QueuingStrategy as defined in CQ.
	UsageBasedAdmissionFairSharing AdmissionMode = "UsageBasedAdmissionFairSharing"

	// AdmissionFairSharing is disabled for this CQ
	NoAdmissionFairSharing AdmissionMode = "NoAdmissionFairSharing"
)
