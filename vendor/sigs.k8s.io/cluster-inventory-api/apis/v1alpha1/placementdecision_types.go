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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// Label keys used for PlacementDecision correlation and discovery.
const (
	// DecisionKeyLabel links all slices to the same decision when a decision spans multiple slices.
	// When multiple slices exist for one logical decision, the producer MUST set the same
	// decision-key on all slices.
	DecisionKeyLabel = "multicluster.x-k8s.io/decision-key"

	// DecisionIndexLabel indicates the index position of this slice when order matters.
	// If a scheduler needs to preserve the order of selected clusters and the result spans
	// multiple slices, it should label each PlacementDecision with this label where the
	// value starts at 0 and increments by 1.
	DecisionIndexLabel = "multicluster.x-k8s.io/decision-index"

	// PlacementKeyLabel links a decision to an originating workload when applicable.
	// Producers may set this label on PlacementDecision slices when the decision is workload scoped.
	// Decisions not tied to a workload need not set this label.
	PlacementKeyLabel = "multicluster.x-k8s.io/placement-key"
)

// ClusterProfileReference contains the identifying information of a ClusterProfile.
type ClusterProfileReference struct {
	// Name is the name of the ClusterProfile.
	// +required
	Name string `json:"name"`

	// Namespace is the namespace of the ClusterProfile.
	// If empty, the PlacementDecision's namespace is used.
	// +optional
	Namespace string `json:"namespace,omitempty"`
}

// ClusterDecision references a target ClusterProfile to apply workloads to.
type ClusterDecision struct {
	// ClusterProfileRef is a reference to the target ClusterProfile.
	// The reference must point to a valid ClusterProfile in the fleet.
	// +required
	ClusterProfileRef ClusterProfileReference `json:"clusterProfileRef"`

	// Reason is an optional explanation of why this cluster was chosen.
	// This can be useful for debugging and auditing placement decisions.
	// +optional
	Reason string `json:"reason,omitempty"`
}

//+genclient
//+kubebuilder:object:root=true
//+kubebuilder:resource:scope=Namespaced

// PlacementDecision publishes the set of clusters chosen by a scheduler at a point in time.
// It is a data-only resource that acts as the interface between schedulers and consumers.
// Schedulers write decisions using this format; consumers read from it.
//
// Following the EndpointSlice convention, a single scheduling decision can fan out to N
// PlacementDecision slices, each limited to 100 clusters. To correlate slices, producers
// MUST set the same multicluster.x-k8s.io/decision-key label on all slices when more than
// one slice exists.
type PlacementDecision struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// Decisions is the list of clusters chosen for this placement decision.
	// Up to 100 ClusterDecisions per object (slice) to stay well below the etcd limit.
	// +kubebuilder:validation:MinItems=0
	// +kubebuilder:validation:MaxItems=100
	// +required
	Decisions []ClusterDecision `json:"decisions"`

	// SchedulerName is the name of the scheduler that created this decision.
	// This is optional and can be used for debugging and auditing purposes.
	// +optional
	SchedulerName string `json:"schedulerName,omitempty"`
}

//+kubebuilder:object:root=true

// PlacementDecisionList contains a list of PlacementDecision.
type PlacementDecisionList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []PlacementDecision `json:"items"`
}

func init() {
	SchemeBuilder.Register(&PlacementDecision{}, &PlacementDecisionList{})
}
