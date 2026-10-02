/*
Copyright 2025 The Kubernetes Authors.

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

package v1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	leaderworkerset "sigs.k8s.io/lws/api/leaderworkerset/v1"
)

const (
	// SetNameLabelKey records the DisaggregatedSet name that resources belong to.
	// Applied to LWS and Service objects in the same namespace as the DisaggregatedSet.
	SetNameLabelKey string = "disaggregatedset.x-k8s.io/name"

	// RoleLabelKey records which role the resource belongs to (e.g. "prefill", "decode").
	// Applied to LWS and Service objects in the same namespace as the DisaggregatedSet.
	RoleLabelKey string = "disaggregatedset.x-k8s.io/role"

	// SliceLabelKey records which slice the resource belongs to.
	SliceLabelKey string = "disaggregatedset.x-k8s.io/slice"

	// RevisionLabelKey records the revision hash for the resource.
	// Applied to LWS and Service objects in the same namespace as the DisaggregatedSet.
	RevisionLabelKey string = "disaggregatedset.x-k8s.io/revision"

	// InitialReplicasAnnotationKey stores the initial replica count at rollout start.
	InitialReplicasAnnotationKey string = "disaggregatedset.x-k8s.io/initial-replicas"
)

// NOTE: json tags are required.  Any new fields you add must have json tags for the fields to be serialized.

// RoleScalingMode controls the source of the replica count for a role.
// +kubebuilder:validation:Enum=Static;External
type RoleScalingMode string

const (
	// RoleScalingStatic uses the inline .spec.replicas value on the role.
	RoleScalingStatic RoleScalingMode = "Static"

	// RoleScalingExternal delegates replicas to an external autoscaler via an
	// auto-created DisaggregatedSetRoleScaler named "<disaggregatedset>-<role>".
	// .spec.replicas on the role is ignored.
	RoleScalingExternal RoleScalingMode = "External"
)

// RoleScaling configures how replicas are determined for a role. Sub-struct
// (not a bare enum) so future per-role scaling policies can be added without
// a v2 API bump.
type RoleScaling struct {
	// Mode controls the source of the replica count. Static (default) uses
	// inline spec.replicas; External uses the auto-created scaler CR.
	// +kubebuilder:default=Static
	// +optional
	Mode RoleScalingMode `json:"mode,omitempty"`
}

// DisaggregatedRoleSpec defines the configuration for a disaggregated role.
// This structure embeds LeaderWorkerSetTemplateSpec from sigs.k8s.io/lws, with validation
// to reject unsupported fields (RolloutStrategy.Type must be RollingUpdate,
// RolloutStrategy.RollingUpdateConfiguration.Partition must not be set).
type DisaggregatedRoleSpec struct {
	// Name is the unique identifier for this role.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	// +required
	Name string `json:"name"`

	// Scaling configures how replicas are determined. Omit for inline Static
	// scaling (default). When set to External, the DisaggregatedSet controller
	// auto-creates a DisaggregatedSetRoleScaler and reads its spec.replicas.
	// +optional
	Scaling *RoleScaling `json:"scaling,omitempty"`

	// LeaderWorkerSetTemplateSpec defines the LWS template for this role.
	// Note: Spec.RolloutStrategy.Type must be RollingUpdate (or empty) and
	// Spec.RolloutStrategy.RollingUpdateConfiguration.Partition must not be set.
	// DisaggregatedSet handles rollouts across roles.
	leaderworkerset.LeaderWorkerSetTemplateSpec `json:",inline"`
}

// DisaggregatedSetSpec defines the desired state of DisaggregatedSet.
//
// The all-or-nothing replicas rule (either every role has replicas > 0, or
// every role has replicas == 0) applies only to non-External roles. External
// roles are exempt because their effective replicas live outside the DS spec —
// they are driven via DisaggregatedSetRoleScaler.spec.replicas.
// +kubebuilder:validation:XValidation:rule="self.roles.filter(r, !has(r.scaling) || r.scaling.mode != 'External').all(r, !has(r.spec.replicas) || r.spec.replicas == 0) || self.roles.filter(r, !has(r.scaling) || r.scaling.mode != 'External').all(r, has(r.spec.replicas) && r.spec.replicas > 0)",message="replicas must be zero for all non-External roles or non-zero for all non-External roles"
type DisaggregatedSetSpec struct {
	// Roles defines the list of roles (at least 2 required).
	// Each role has a unique name and its own configuration.
	// +listType=map
	// +listMapKey=name
	// +kubebuilder:validation:MinItems=2
	// +kubebuilder:validation:MaxItems=10
	// +required
	Roles []DisaggregatedRoleSpec `json:"roles"`

	// Slices is the number of independent copies of the whole role topology.
	// Each slice is a complete set of all roles that rolls out independently.
	// Changing Slices scales copies up or down and does not trigger a rollout.
	// +optional
	// +kubebuilder:default=1
	// +kubebuilder:validation:Minimum=1
	// +kubebuilder:validation:Maximum=100
	Slices *int32 `json:"slices,omitempty"`

	// PlacementPolicy controls how a slice's roles are co-located and how the
	// DisaggregatedSet's slices are spread across topology domains. When set, the
	// controller injects pod affinity and anti-affinity into the managed
	// LeaderWorkerSet pod templates. Placement is applied when a LeaderWorkerSet is
	// created, so changing it takes effect on the next rollout.
	// +optional
	PlacementPolicy *PlacementPolicy `json:"placementPolicy,omitempty"`
}

// PlacementType selects the DisaggregatedSet placement guarantee.
type PlacementType string

const (
	// PlacementNone injects no affinity. This is the default.
	PlacementNone PlacementType = "None"
	// PlacementExclusiveSlice co-locates a slice's roles in one topology domain and
	// spreads this DisaggregatedSet's slices across domains. Other DisaggregatedSets
	// may share a domain.
	PlacementExclusiveSlice PlacementType = "ExclusiveSlice"
	// PlacementExclusiveTopology is ExclusiveSlice plus domain exclusivity: a domain
	// holds at most one slice across all DisaggregatedSets (a 1:1 domain-to-slice mapping).
	PlacementExclusiveTopology PlacementType = "ExclusiveTopology"
)

// PlacementPolicy controls topology placement of a DisaggregatedSet's slices.
type PlacementPolicy struct {
	// Type selects the placement guarantee. Defaults to None.
	// +optional
	// +kubebuilder:default=None
	// +kubebuilder:validation:Enum=None;ExclusiveSlice;ExclusiveTopology
	Type PlacementType `json:"type,omitempty"`

	// Topology is the node-label key that defines a domain, used as the affinity
	// topologyKey. Required when Type is not None.
	// +optional
	Topology string `json:"topology,omitempty"`
}

// RoleStatus defines the observed state of a single role.
type RoleStatus struct {
	// Name is the name of the role (matches spec.roles[].name).
	// +required
	Name string `json:"name"`

	// Replicas is the total number of replicas for this role.
	// +optional
	Replicas int32 `json:"replicas,omitempty"`

	// ReadyReplicas is the number of ready replicas for this role.
	// +optional
	ReadyReplicas int32 `json:"readyReplicas,omitempty"`

	// UpdatedReplicas is the number of replicas updated to the latest revision.
	// +optional
	UpdatedReplicas int32 `json:"updatedReplicas,omitempty"`
}

// DisaggregatedSetStatus defines the observed state of DisaggregatedSet.
type DisaggregatedSetStatus struct {
	// For Kubernetes API conventions, see:
	// https://github.com/kubernetes/community/blob/master/contributors/devel/sig-architecture/api-conventions.md#typical-status-properties

	// RoleStatuses contains the status for each role.
	// The order matches spec.roles.
	// +listType=map
	// +listMapKey=name
	// +optional
	RoleStatuses []RoleStatus `json:"roleStatuses,omitempty"`

	// conditions represent the current state of the DisaggregatedSet resource.
	// Each condition has a unique type and reflects the status of a specific aspect of the resource.
	//
	// Standard condition types include:
	// - "Available": the resource is fully functional
	// - "Progressing": the resource is being created or updated
	// - "Degraded": the resource failed to reach or maintain its desired state
	//
	// The status of each condition is one of True, False, or Unknown.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +k8s:deepcopy-gen:interfaces=k8s.io/apimachinery/pkg/runtime.Object

// DisaggregatedSet is the Schema for the disaggregatedsets API
type DisaggregatedSet struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// spec defines the desired state of DisaggregatedSet
	// +required
	Spec DisaggregatedSetSpec `json:"spec"`

	// status defines the observed state of DisaggregatedSet
	// +optional
	Status DisaggregatedSetStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// DisaggregatedSetList contains a list of DisaggregatedSet
type DisaggregatedSetList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []DisaggregatedSet `json:"items"`
}

func init() {
	SchemeBuilder.Register(&DisaggregatedSet{}, &DisaggregatedSetList{})
}
