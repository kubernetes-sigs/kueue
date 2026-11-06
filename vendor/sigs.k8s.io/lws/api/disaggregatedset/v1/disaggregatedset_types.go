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
	"k8s.io/apimachinery/pkg/util/intstr"
	leaderworkerset "sigs.k8s.io/lws/api/leaderworkerset/v1"
)

const (
	// RevisionHashVersion identifies the current revision hash algorithm, which
	// covers all generated LeaderWorkerSet fields that require a rollout.
	RevisionHashVersion = "v2"
	// RevisionHashVersionAnnotationKey records which revision hash algorithm a
	// DisaggregatedSet uses. Its absence identifies a legacy set.
	RevisionHashVersionAnnotationKey = "disaggregatedset.x-k8s.io/revision-hash-version"

	// SetNameLabelKey records the DisaggregatedSet name that resources belong to.
	// Applied to LWS and Service objects in the same namespace as the DisaggregatedSet.
	SetNameLabelKey string = "disaggregatedset.x-k8s.io/name"

	// RoleLabelKey records which role the resource belongs to (e.g. "prefill", "decode").
	// Applied to LWS and Service objects in the same namespace as the DisaggregatedSet.
	RoleLabelKey string = "disaggregatedset.x-k8s.io/role"
	// SubRoleLabelKey is controller-owned and applied to every Pod in an
	// assigned replica group.
	SubRoleLabelKey string = "disaggregatedset.x-k8s.io/subrole"

	// SliceLabelKey records which slice the resource belongs to.
	SliceLabelKey string = "disaggregatedset.x-k8s.io/slice"

	// RevisionLabelKey records the revision hash for the resource.
	// Applied to LWS and Service objects in the same namespace as the DisaggregatedSet.
	RevisionLabelKey string = "disaggregatedset.x-k8s.io/revision"

	// SetNameEnv is injected into every container of Pods managed by a DisaggregatedSet.
	SetNameEnv string = "DISAGGREGATEDSET_NAME"

	// RoleEnv is injected into every container of Pods managed by a DisaggregatedSet.
	RoleEnv string = "DISAGGREGATEDSET_ROLE"

	// SliceEnv is injected into every container of Pods managed by a DisaggregatedSet.
	SliceEnv string = "DISAGGREGATEDSET_SLICE"

	// RevisionEnv is injected into every container of Pods managed by a DisaggregatedSet.
	RevisionEnv string = "DISAGGREGATEDSET_REVISION"

	// InitialReplicasAnnotationKey records this LWS revision's intended replica
	// count. While the revision is current, the controller keeps it aligned with
	// the target. When a newer revision makes it old, the value freezes and is
	// used as its drain baseline. It can therefore differ from the current Spec
	// after an interrupted rollout. The annotation is deleted with the LWS after
	// the old revision finishes draining. A missing, non-integer, or negative
	// value is treated as unset.
	InitialReplicasAnnotationKey string = "disaggregatedset.x-k8s.io/initial-replicas"

	// MaxUnavailableAnnotationKey is a controller-owned DisaggregatedSet annotation
	// mapping role names to their last observed, resolved maxUnavailable counts.
	// Removed roles retain these counts until their LWS objects have been deleted.
	MaxUnavailableAnnotationKey string = "disaggregatedset.x-k8s.io/max-unavailable"

	// InitialSubRoleReplicasAnnotationKey records the revision's intended
	// sub-role counts as a JSON object, frozen while the revision is old.
	InitialSubRoleReplicasAnnotationKey string = "disaggregatedset.x-k8s.io/initial-subrole-replicas"
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

// RolloutScaleDownPolicy controls when demand-driven target replica reductions are
// considered during an active rolling update. Both policies allow scale-up and
// old-revision retirement within the rollout's availability and surge bounds.
// For roles still present, reductions preserve effective desired - maxUnavailable
// Ready replicas, or avoid worsening a shortfall if fewer are currently Ready.
// Deferred External roles use at least the target's current Spec as effective
// desired while old replicas of that role remain.
// +kubebuilder:validation:Enum=Deferred;Immediate
type RolloutScaleDownPolicy string

const (
	// RolloutScaleDownDeferred preserves the existing rollout behavior. Scale-up
	// proceeds within rollout bounds; demand-driven target reductions wait until
	// old revisions finish draining. Old replicas may still drain during the
	// rollout. Renamed-role surplus recovery uses the same target availability
	// and scheduled-startup protection as other target reductions.
	// The surge ceiling is max(initial old replicas, effective desired) + maxSurge.
	// External roles retain their target Spec clamp while old replicas overlap.
	RolloutScaleDownDeferred RolloutScaleDownPolicy = "Deferred"

	// RolloutScaleDownImmediate considers scale-down during the rollout with a
	// surge ceiling of desired + maxSurge. For roles still present in the target,
	// old and target reductions use max(0, desired - maxUnavailable) as their
	// availability floor. Safe old-revision drains are preferred; target
	// replicas may shrink when old revisions must remain. Availability and
	// startup protection can delay reductions: Immediate does not mean instant.
	RolloutScaleDownImmediate RolloutScaleDownPolicy = "Immediate"
)

// DisaggregatedSetScalingPolicy configures how replica target changes interact
// with DisaggregatedSet reconciliation.
type DisaggregatedSetScalingPolicy struct {
	// ScaleDownDuringRollout controls demand-driven target replica reductions.
	// Deferred waits until old revisions finish draining. Immediate considers
	// reductions during the rollout, preferring safe old-revision drains.
	// Both policies allow scale-up and old-revision drains within rollout bounds.
	// +optional
	// +kubebuilder:default=Deferred
	ScaleDownDuringRollout RolloutScaleDownPolicy `json:"scaleDownDuringRollout,omitempty"`
}

// DisaggregatedSubRoleSpec defines a scaling and routing pool sharing its parent's templates.
// +kubebuilder:validation:XValidation:rule="!has(self.scaling) || self.scaling.mode != 'External' || !has(self.replicas)",message="replicas must be omitted when scaling.mode is External"
type DisaggregatedSubRoleSpec struct {
	// Name is unique within the parent role.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?$`
	// +required
	Name string `json:"name"`
	// Replicas counts groups (Static default: 1); omit it for External scaling.
	// +optional
	// +kubebuilder:validation:Minimum=0
	Replicas *int32 `json:"replicas,omitempty"`
	// Scaling selects this pool's replica source.
	// +optional
	Scaling *RoleScaling `json:"scaling,omitempty"`

	// MaxSurge overrides the parent's rollout surge budget for this sub-role.
	// Accepts a non-negative integer or a percentage up to 100%, rounded up
	// against this sub-role's replica target. Omitted values inherit the parent;
	// with no parent rollout configuration, the default is 0.
	// +optional
	// +kubebuilder:validation:XIntOrString
	MaxSurge *intstr.IntOrString `json:"maxSurge,omitempty"`

	// MaxUnavailable overrides the parent's rollout unavailability budget for
	// this sub-role. Accepts a non-negative integer or a percentage up to 100%,
	// rounded down against this sub-role's replica target. Omitted values inherit
	// the parent; with no parent rollout configuration, the default is 1. Zero is explicit.
	// +optional
	// +kubebuilder:validation:XIntOrString
	MaxUnavailable *intstr.IntOrString `json:"maxUnavailable,omitempty"`
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

	// SubRoles partitions one LWS into pools sharing its templates.
	// Child targets sum to parent replicas; omit parent scaling. Parent spec.replicas is ignored.
	// +optional
	// +listType=map
	// +listMapKey=name
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=32
	SubRoles []DisaggregatedSubRoleSpec `json:"subRoles,omitempty"`

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
// every role has replicas == 0) applies only to non-External, unpartitioned
// roles. External targets come from scalers; sub-roles may independently scale
// to zero without pausing sibling routing pools.
// +kubebuilder:validation:XValidation:rule="self.roles.filter(r, (!has(r.subRoles) || size(r.subRoles) == 0) && (!has(r.scaling) || r.scaling.mode != 'External')).all(r, !has(r.spec.replicas) || r.spec.replicas == 0) || self.roles.filter(r, (!has(r.subRoles) || size(r.subRoles) == 0) && (!has(r.scaling) || r.scaling.mode != 'External')).all(r, has(r.spec.replicas) && r.spec.replicas > 0)",message="replicas must be zero for all non-External unpartitioned roles or non-zero for all non-External unpartitioned roles"
type DisaggregatedSetSpec struct {
	// Roles defines the list of roles (at least 1 required).
	// Each role has a unique name and its own configuration.
	// +listType=map
	// +listMapKey=name
	// +kubebuilder:validation:MinItems=1
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

	// ScalingPolicy controls scale-down during rolling updates. Omit it to
	// preserve the default Deferred behavior.
	// +optional
	ScalingPolicy *DisaggregatedSetScalingPolicy `json:"scalingPolicy,omitempty"`

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

// SubRoleStatus defines the observed state of a sub-role within its parent.
type SubRoleStatus struct {
	// Name is the sub-role name, without the parent prefix.
	// +required
	Name string `json:"name"`

	// DesiredReplicas is the number of replica groups the controller is driving
	// this sub-role toward, totalled across slices: its replicas, or the target of
	// its DisaggregatedSetRoleScaler when its scaling mode is External. It is unset
	// while that target is unknown.
	// +optional
	DesiredReplicas *int32 `json:"desiredReplicas,omitempty"`

	// Replicas is the number of replica groups assigned to this sub-role.
	// +optional
	Replicas int32 `json:"replicas,omitempty"`

	// ReadyReplicas counts Ready groups with a coherent whole-group assignment.
	// +optional
	ReadyReplicas int32 `json:"readyReplicas,omitempty"`

	// UpdatedReplicas counts groups belonging to the latest revision.
	// +optional
	UpdatedReplicas int32 `json:"updatedReplicas,omitempty"`
}

// RoleStatus defines the observed state of a single parent role.
type RoleStatus struct {
	// Name is the parent role name.
	// +required
	Name string `json:"name"`

	// DesiredReplicas is the number of replicas the controller is driving this role
	// toward, totalled across slices: its replicas, or the target of its
	// DisaggregatedSetRoleScaler when its scaling mode is External. A partitioned
	// role reports the sum of its sub-role targets. It is unset while any of those
	// targets is unknown, for example before the scaler exists, so it is never
	// mistaken for a requested zero. The Available condition is judged against it.
	// +optional
	DesiredReplicas *int32 `json:"desiredReplicas,omitempty"`

	// Replicas is the total number of replicas for this role.
	// +optional
	Replicas int32 `json:"replicas,omitempty"`

	// ReadyReplicas is the number of ready replicas for this role.
	// Sub-role readiness requires a coherent assignment across the whole group.
	// +optional
	ReadyReplicas int32 `json:"readyReplicas,omitempty"`

	// UpdatedReplicas is the number of replicas updated to the latest revision.
	// +optional
	UpdatedReplicas int32 `json:"updatedReplicas,omitempty"`

	// SubRoleStatuses lists the current sub-roles in spec order.
	// Removed sub-roles are omitted, but their groups remain in the parent counts
	// while draining. Do not add child counts to their parent aggregate.
	// +listType=map
	// +listMapKey=name
	// +optional
	SubRoleStatuses []SubRoleStatus `json:"subRoleStatuses,omitempty"`
}

// DisaggregatedSetStatus defines the observed state of DisaggregatedSet.
type DisaggregatedSetStatus struct {
	// For Kubernetes API conventions, see:
	// https://github.com/kubernetes/community/blob/master/contributors/devel/sig-architecture/api-conventions.md#typical-status-properties

	// observedGeneration is the most recent generation observed for this DisaggregatedSet.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// RoleStatuses lists current parent roles in spec order, with nested sub-role statuses.
	// Parent counts include historical layouts and may exceed the visible child sum.
	// Removed roles are omitted even while their groups drain.
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
	//
	// The status of each condition is one of True, False, or Unknown.
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// DisaggregatedSetConditionType is a valid value for DisaggregatedSetStatus.Conditions[].Type.
type DisaggregatedSetConditionType string

// These are built-in conditions of a DisaggregatedSet.
const (
	// DisaggregatedSetAvailable means every role has reached its desired replica
	// count (across all slices), with all of those replicas ready and updated to
	// the current revision. A role with a desired replica count of 0 (the
	// all-roles-paused state) is satisfied once it has fully drained to 0.
	DisaggregatedSetAvailable DisaggregatedSetConditionType = "Available"

	// DisaggregatedSetProgressing means at least one role has not yet reached its
	// desired replica count, or has replicas that are not ready or not updated to
	// the current revision.
	DisaggregatedSetProgressing DisaggregatedSetConditionType = "Progressing"
	// DisaggregatedSetSubRolesAssigned means every live group of a partitioned
	// role has a consistent assignment on all of its observed members.
	DisaggregatedSetSubRolesAssigned DisaggregatedSetConditionType = "SubRolesAssigned"
)

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:shortName={dset,disaggset}
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
