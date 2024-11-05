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

const (
	// PodSetRequiredTopologyAnnotation indicates that a PodSet requires
	// Topology Aware Scheduling, and requires scheduling all pods on nodes
	// within the same topology domain corresponding to the topology level
	// indicated by the annotation value (e.g. within a rack or within a block).
	PodSetRequiredTopologyAnnotation = "kueue.x-k8s.io/podset-required-topology"

	// PodSetPreferredTopologyAnnotation indicates that a PodSet requires
	// Topology Aware Scheduling, but scheduling all pods within pods on nodes
	// within the same topology domain is a preference rather than requirement.
	//
	// The levels are evaluated one-by-one going up from the level indicated by
	// the annotation. If the PodSet cannot fit within a given topology domain
	// then the next topology level up is considered. If the PodSet cannot fit
	// at the highest topology level, then it gets admitted as distributed
	// among multiple topology domains.
	PodSetPreferredTopologyAnnotation = "kueue.x-k8s.io/podset-preferred-topology"

	// TopologySchedulingGate is used to delay scheduling of a Pod until the
	// nodeSelectors corresponding to the assigned topology domain are injected
	// into the Pod. For the Pod-based integrations the gate is added in webhook
	// during the Pod creation.
	TopologySchedulingGate = "kueue.x-k8s.io/topology"

	// WorkloadAnnotation is an annotation set on the Job's PodTemplate to
	// indicate the name of the admitted Workload corresponding to the Job. The
	// annotation is set when starting the Job, and removed on stopping the Job.
	WorkloadAnnotation = "kueue.x-k8s.io/workload"

	// PodSetLabel is a label set on the Job's PodTemplate to indicate the name
	// of the PodSet of the admitted Workload corresponding to the PodTemplate.
	// The label is set when starting the Job, and removed on stopping the Job.
	PodSetLabel = "kueue.x-k8s.io/podset"

	// TASLabel is a label set on the Job's PodTemplate to indicate that the
	// PodSet is admitted using TopologyAwareScheduling, and all Pods created
	// from the Job's PodTemplate also have the label. For the Pod-based
	// integrations the label is added in webhook during the Pod creation.
	TASLabel = "kueue.x-k8s.io/tas"
)

// TopologySpec defines the desired state of Topology
type TopologySpec struct {
	// levels define the levels of topology.
	//
	// +required
	// +listType=atomic
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=8
	Levels []TopologyLevel `json:"levels,omitempty"`
}

// TopologyLevel defines the desired state of TopologyLevel
type TopologyLevel struct {
	// nodeLabel indicates the name of the node label for a specific topology
	// level.
	//
	// Examples:
	// - cloud.provider.com/topology-block
	// - cloud.provider.com/topology-rack
	//
	// +required
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=316
	// +kubebuilder:validation:Pattern=`^([a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*/)?(([A-Za-z0-9][-A-Za-z0-9_.]*)?[A-Za-z0-9])$`
	NodeLabel string `json:"nodeLabel"`
}

// +genclient
// +genclient:nonNamespaced
// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:resource:scope=Cluster

// Topology is the Schema for the topology API
type Topology struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec TopologySpec `json:"spec,omitempty"`
}

//+kubebuilder:object:root=true

// TopologyList contains a list of Topology
type TopologyList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Topology `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Topology{}, &TopologyList{})
}
