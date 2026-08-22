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

package v1alpha1

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	DynamicQuotaOrchestratorActive      = "Active"
	DynamicQuotaOrchestratorDistributed = "Distributed"
)

type DynamicQuotaOrchestratorSpec struct {
	// capacityDiscovery specifies capacity aggregation.
	//
	// +required
	CapacityDiscovery CapacityDiscovery `json:"capacityDiscovery"`

	// capacityDistribution specifies how aggregated capacity is distributed.
	// When omitted, the DQO is discovery-only: it reports aggregated capacity
	// but does not write effectiveQuota status.
	//
	// +optional
	CapacityDistribution *CapacityDistribution `json:"capacityDistribution,omitempty"`
}

type CapacityDiscovery struct {
	// providers lists CapacityProvider objects consumed by this DQO.
	//
	// +listType=map
	// +listMapKey=name
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=8
	Providers []CapacityDiscoveryProviderContribution `json:"providers"`
}

type CapacityDiscoveryProviderContribution struct {
	// name identifies a CapacityProvider.
	//
	// +required
	Name CapacityProviderName `json:"name"`

	// usableCapacityPercent specifies the contribution of discovered capacity.
	// It defaults to 100.
	//
	// +optional
	// +kubebuilder:default=100
	// +kubebuilder:validation:Minimum=0
	// +kubebuilder:validation:Maximum=10000
	UsableCapacityPercent *int32 `json:"usableCapacityPercent,omitempty"`
}

type CapacityDistribution struct {
	// subtreeRootRef identifies the root of the quota subtree.
	//
	// +required
	SubtreeRootRef CapacityDistributionSubtreeRootRef `json:"subtreeRootRef"`
}

type SubtreeRootRefType string

const (
	ClusterQueueSubtreeRootRefType SubtreeRootRefType = "ClusterQueue"
	CohortSubtreeRootRefType       SubtreeRootRefType = "Cohort"
)

type CapacityDistributionSubtreeRootRef struct {
	// kind indicates the kind of the quota node, i.e. ClusterQueue or Cohort.
	//
	// +required
	// +kubebuilder:validation:Enum=ClusterQueue;Cohort
	Kind SubtreeRootRefType `json:"kind"`

	// name indicates the name of the quota node, i.e. ClusterQueue or Cohort.
	//
	// +required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:Pattern="^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$"
	Name string `json:"name"`
}

// +kubebuilder:validation:MinLength=1
// +kubebuilder:validation:MaxLength=253
// +kubebuilder:validation:Pattern="^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$"
type CapacityProviderName string

// ResourceFlavorReference is the name of the ResourceFlavor.
// +kubebuilder:validation:MaxLength=253
// +kubebuilder:validation:Pattern="^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$"
type ResourceFlavorReference string

type DynamicQuotaOrchestratorStatus struct {
	// aggregatedCapacity is the capacity aggregated from the referenced providers.
	//
	// +optional
	AggregatedCapacity *AggregatedCapacity `json:"aggregatedCapacity,omitempty"`

	// conditions represents the current state of the DQO.
	//
	// +optional
	// +listType=map
	// +listMapKey=type
	// +patchStrategy=merge
	// +patchMergeKey=type
	// +kubebuilder:validation:MaxItems=16
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`
}

type AggregatedCapacity struct {
	// flavors contains capacity per flavor and resource.
	//
	// +listType=map
	// +listMapKey=name
	// +kubebuilder:validation:MaxItems=128
	Flavors []AggregatedCapacityFlavor `json:"flavors"`

	// lastUpdateTime is the time at which the snapshot was last updated.
	//
	// +required
	// +kubebuilder:validation:Type=string
	// +kubebuilder:validation:Format=date-time
	LastUpdateTime metav1.Time `json:"lastUpdateTime"`
}

type AggregatedCapacityFlavor struct {
	// name identifies the ResourceFlavor whose capacity is reported.
	//
	// +required
	Name ResourceFlavorReference `json:"name"`

	// resources contains total capacity by resource name.
	//
	// +required
	// +kubebuilder:validation:XValidation:rule="size(self) >= 1 && size(self) <= 64",message="resource capacity must have between 1 and 64 entries"
	// +kubebuilder:validation:XValidation:rule="self.all(r, type(self[r]) == string ? quantity(self[r]).sign() >= 0 : self[r] >= 0)",message="resource capacity must be non-negative"
	Resources corev1.ResourceList `json:"resources"`
}

// +genclient
// +genclient:nonNamespaced
// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,shortName={dqo}

// DynamicQuotaOrchestrator is the Schema for the dynamicquotaorchestrators API
type DynamicQuotaOrchestrator struct {
	metav1.TypeMeta `json:",inline"`
	// metadata is the metadata of the DynamicQuotaOrchestrator.
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// spec is the specification of the DynamicQuotaOrchestrator.
	// +optional
	Spec DynamicQuotaOrchestratorSpec `json:"spec,omitempty"`

	// status is the status of the DynamicQuotaOrchestrator.
	// +optional
	Status DynamicQuotaOrchestratorStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// DynamicQuotaOrchestratorList contains a list of DynamicQuotaOrchestrator
type DynamicQuotaOrchestratorList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []DynamicQuotaOrchestrator `json:"items"`
}
