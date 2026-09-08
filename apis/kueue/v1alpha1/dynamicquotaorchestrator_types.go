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
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type DynamicQuotaOrchestratorSpec struct {
	// capacityDiscovery specifies capacity aggregation.
	//
	// +required
	CapacityDiscovery CapacityDiscovery `json:"capacityDiscovery"`

	// capacityDistribution specifies how aggregated capacity is distributed.
	// When omitted, the DQO is discovery-only: it reports aggregated capacity
	// but does not write effectiveQuotas status.
	//
	// +optional
	CapacityDistribution *CapacityDistribution `json:"capacityDistribution,omitempty"`
}

type CapacityDiscovery struct {
	// providers lists CapacityProvider objects consumed by this DQO.
	//
	// +required
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

	// effectiveCapacityMultiplier specifies the multiplier applied to the
	// discovered capacity from this provider. It defaults to 1.
	//
	// +optional
	// +kubebuilder:default=1
	// +kubebuilder:validation:XValidation:rule="type(self) == int ? self >= 0 : sign(quantity(self)) >= 0",message="effectiveCapacityMultiplier must be non-negative"
	EffectiveCapacityMultiplier *resource.Quantity `json:"effectiveCapacityMultiplier,omitempty"`
}

type CapacityDistribution struct {
	// subtreeRootQuotaRef identifies the root of the quota subtree.
	//
	// +required
	SubtreeRootQuotaRef CapacityDistributionSubtreeRootRef `json:"subtreeRootQuotaRef"`
}

type SubtreeRootRefKind string

const (
	ClusterQueueSubtreeRootRefKind SubtreeRootRefKind = "ClusterQueue"
	CohortSubtreeRootRefKind       SubtreeRootRefKind = "Cohort"
)

type CapacityDistributionSubtreeRootRef struct {
	// kind indicates the kind of the quota node, i.e. ClusterQueue or Cohort.
	//
	// +required
	// +kubebuilder:validation:Enum=ClusterQueue;Cohort
	Kind SubtreeRootRefKind `json:"kind"`

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

// TODO: once we graduate this API to beta, we should leverage beta API's ResourceFlavorReference type instead of Alpha one.
// ResourceFlavorReference is the name of the ResourceFlavor.
// +kubebuilder:validation:MaxLength=253
// +kubebuilder:validation:Pattern="^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$"
type ResourceFlavorReference string

type DynamicQuotaOrchestratorStatus struct {
	// effectiveCapacity is the capacity aggregated from the referenced providers.
	//
	// +optional
	EffectiveCapacity *EffectiveCapacity `json:"effectiveCapacity,omitempty"`

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

type EffectiveCapacity struct {
	// flavors contains capacity per flavor and resource.
	//
	// +required
	// +listType=map
	// +listMapKey=name
	// +kubebuilder:validation:MaxItems=128
	Flavors []EffectiveCapacityFlavor `json:"flavors"`
}

type EffectiveCapacityFlavor struct {
	// name identifies the ResourceFlavor whose capacity is reported.
	//
	// +required
	Name ResourceFlavorReference `json:"name"`

	// resources contains total capacity by resource name.
	//
	// +required
	// +kubebuilder:validation:XValidation:rule="size(self) >= 1 && size(self) <= 64",message="resource capacity must have between 1 and 64 entries"
	Resources corev1.ResourceList `json:"resources"`
}

// DynamicQuotaOrchestrator condition types and reasons
const (
	// DynamicQuotaOrchestratorEffectiveCapacityComputed indicates whether status.effectiveCapacity
	// is successfully aggregated from all referenced CapacityProviders.
	DynamicQuotaOrchestratorEffectiveCapacityComputed string = "EffectiveCapacityComputed"

	// DynamicQuotaOrchestratorReasonComputed indicates that effective capacity was aggregated successfully.
	DynamicQuotaOrchestratorReasonComputed string = "Computed"

	// DynamicQuotaOrchestratorReasonProviderNotReady indicates a referenced CapacityProvider does not have CapacitySynchronized=True.
	DynamicQuotaOrchestratorReasonProviderNotReady string = "ProviderNotReady"

	// DynamicQuotaOrchestratorReasonAggregationFailed indicates aggregation failed (e.g. multiplier application error, math overflow, or capacity limits exceeded).
	DynamicQuotaOrchestratorReasonAggregationFailed string = "AggregationFailed"

	// DynamicQuotaOrchestratorDistributed indicates whether status.effectiveQuotas has been
	// successfully computed and distributed across the referenced subtree.
	// This condition is only present when spec.capacityDistribution is configured.
	DynamicQuotaOrchestratorDistributed string = "Distributed"

	// DynamicQuotaOrchestratorReasonQuotasDistributed indicates all effective quotas were successfully applied.
	DynamicQuotaOrchestratorReasonQuotasDistributed string = "QuotasDistributed"

	// DynamicQuotaOrchestratorReasonEffectiveCapacityNotComputed indicates distribution was skipped because Phase 1 discovery is not ready.
	DynamicQuotaOrchestratorReasonEffectiveCapacityNotComputed string = "EffectiveCapacityNotComputed"

	// DynamicQuotaOrchestratorReasonConflictingDynamicQuotaOrchestrator indicates this DQO was deactivated by soft validation.
	DynamicQuotaOrchestratorReasonConflictingDynamicQuotaOrchestrator string = "ConflictingDynamicQuotaOrchestrator"

	// DynamicQuotaOrchestratorReasonEffectiveQuotasConflict indicates another controller owns status.effectiveQuotas on a target queue.
	DynamicQuotaOrchestratorReasonEffectiveQuotasConflict string = "EffectiveQuotasConflict"

	// DynamicQuotaOrchestratorReasonMisconfigured indicates a configuration or reference error in spec.
	DynamicQuotaOrchestratorReasonMisconfigured string = "Misconfigured"
)

// +genclient
// +genclient:nonNamespaced
// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,shortName={dqo}

// DynamicQuotaOrchestrator is the Schema for the dynamicquotaorchestrators API
type DynamicQuotaOrchestrator struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   DynamicQuotaOrchestratorSpec   `json:"spec,omitempty"`
	Status DynamicQuotaOrchestratorStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// DynamicQuotaOrchestratorList contains a list of DynamicQuotaOrchestrator
type DynamicQuotaOrchestratorList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []DynamicQuotaOrchestrator `json:"items"`
}
