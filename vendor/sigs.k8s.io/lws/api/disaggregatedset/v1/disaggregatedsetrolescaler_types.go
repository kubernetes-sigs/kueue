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
)

// DisaggregatedSetRoleScalerReady is set to True when the scaler is bound to a
// live DisaggregatedSet + role and its status reflects the observed
// LeaderWorkerSet(s).
const DisaggregatedSetRoleScalerReady string = "Ready"

// DisaggregatedSetRoleScalerSpec is the desired state written by an external
// autoscaler via the /scale subresource. The (DS, role) association is derived
// from the scaler's controller ownerReference and its role label.
type DisaggregatedSetRoleScalerSpec struct {
	// Replicas is the desired replica count for the role. The controller seeds
	// this at scaler creation — 1 for a fresh role (0 would deadlock vanilla
	// HPA in ScalingDisabled), or the LWS's current replica count for a
	// Static→External flip so the role does not silently drain to zero.
	// External autoscalers overwrite it on their first tick.
	//
	// Non-pointer with a default because kube-apiserver's CRD /scale handler
	// extracts .spec.replicas at read time and errors ("the spec replicas
	// field does not exist") when the JSONPath resolves to nothing. HPA reads
	// /scale before its first write; a missing field would deadlock the loop.
	// +kubebuilder:default=0
	// +kubebuilder:validation:Minimum=0
	Replicas int32 `json:"replicas"`
}

// DisaggregatedSetRoleScalerStatus is the observed state written back by the
// DisaggregatedSet controller.
type DisaggregatedSetRoleScalerStatus struct {
	// Replicas is the observed replica count for this role — LWS groups
	// (== leader pods), aggregated across all revisions currently present.
	// Read by HPA as the "current" count for its ratio math.
	// +optional
	Replicas int32 `json:"replicas,omitempty"`

	// Selector is a label selector (in string form) matching one pod per LWS
	// group (the leader), across all revisions:
	//   disaggregatedset.x-k8s.io/name=<ds>,disaggregatedset.x-k8s.io/role=<role>,leaderworkerset.sigs.k8s.io/worker-index=0
	// Leader-only so HPA's per-pod-metric averaging divides by group count
	// (matching spec.replicas that HPA writes), keeping the ratio math
	// consistent for leaderWorkerTemplate.size > 1. Aggregate across
	// revisions so HPA observes the serving fleet during a rolling update.
	// +optional
	Selector string `json:"selector,omitempty"`

	// ObservedGeneration is the .metadata.generation the status reflects.
	// +optional
	ObservedGeneration int64 `json:"observedGeneration,omitempty"`

	// Conditions expose scaler-level state (Ready).
	// +listType=map
	// +listMapKey=type
	// +optional
	Conditions []metav1.Condition `json:"conditions,omitempty"`
}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:subresource:scale:specpath=.spec.replicas,statuspath=.status.replicas,selectorpath=.status.selector
// +kubebuilder:resource:shortName=dsrs
// +kubebuilder:printcolumn:name="Desired",type=integer,JSONPath=`.spec.replicas`
// +kubebuilder:printcolumn:name="Current",type=integer,JSONPath=`.status.replicas`
// +kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

// DisaggregatedSetRoleScaler exposes the /scale subresource for a single role
// of a DisaggregatedSet. Instances are auto-created by the DisaggregatedSet
// controller for every role with scaling.mode: External and are named
// "<disaggregatedset>-<role>". External autoscalers (HPA, KEDA, or any
// /scale-aware controller) write spec.replicas; the DisaggregatedSet controller
// reads it and drives the role's LeaderWorkerSet.
type DisaggregatedSetRoleScaler struct {
	metav1.TypeMeta `json:",inline"`

	// metadata is a standard object metadata
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// spec defines the desired state of DisaggregatedSetRoleScaler
	// +optional
	Spec DisaggregatedSetRoleScalerSpec `json:"spec,omitempty"`

	// status defines the observed state of DisaggregatedSetRoleScaler
	// +optional
	Status DisaggregatedSetRoleScalerStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// DisaggregatedSetRoleScalerList contains a list of DisaggregatedSetRoleScaler.
type DisaggregatedSetRoleScalerList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []DisaggregatedSetRoleScaler `json:"items"`
}

func init() {
	SchemeBuilder.Register(&DisaggregatedSetRoleScaler{}, &DisaggregatedSetRoleScalerList{})
}
