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

	// PodSetUnconstrainedTopologyAnnotation indicates that a PodSet does not have any topology requirements.
	// Kueue admits the PodSet if there's enough free capacity available.
	// Recommended for PodSets that don't need low-latency or high-throughput pod-to-pod communication,
	// but want to leverage TAS capabilities improve accuracy of admitting jobs
	//
	// +kubebuilder:validation:Type=boolean
	PodSetUnconstrainedTopologyAnnotation = "kueue.x-k8s.io/podset-unconstrained-topology"

	// PodSetSliceRequiredTopologyAnnotation indicates that a PodSet requires
	// Topology Aware Scheduling, and requires scheduling each PodSet slice on nodes
	// within the topology domain corresponding to the topology level
	// indicated by the annotation value (e.g. within a rack or within a block).
	PodSetSliceRequiredTopologyAnnotation = "kueue.x-k8s.io/podset-slice-required-topology"

	// PodSetSliceSizeAnnotation describes the requested size of a podset slice
	// for which Kueue finds a requested topology domain.
	//
	// This annotation is required if `kueue.x-k8s.io/podset-slice-required-topology`
	// is defined.
	PodSetSliceSizeAnnotation = "kueue.x-k8s.io/podset-slice-size"

	// TopologySchedulingGate is used to delay scheduling of a Pod until the
	// nodeSelectors corresponding to the assigned topology domain are injected
	// into the Pod. For the Pod-based integrations the gate is added in webhook
	// during the Pod creation.
	TopologySchedulingGate = "kueue.x-k8s.io/topology"

	// WorkloadAnnotation is an annotation set on the Job's PodTemplate to
	// indicate the name of the admitted Workload corresponding to the Job. The
	// annotation is set when starting the Job, and removed on stopping the Job.
	WorkloadAnnotation = "kueue.x-k8s.io/workload"

	// TASLabel is a label set on the Job's PodTemplate to indicate that the
	// PodSet is admitted using TopologyAwareScheduling, and all Pods created
	// from the Job's PodTemplate also have the label. For the Pod-based
	// integrations the label is added in webhook during the Pod creation.
	TASLabel = "kueue.x-k8s.io/tas"

	// PodGroupPodIndexLabel is a label set on the Pod's metadata belonging
	// to a Pod group. It indicates the Pod's index within the group.
	PodGroupPodIndexLabel = "kueue.x-k8s.io/pod-group-pod-index"

	// PodGroupPodIndexLabelAnnotation is an annotation on the Pod's metadata
	// belonging to a Pod group. It indicates a label name used to retrieve
	// the Pod's index within the group.
	PodGroupPodIndexLabelAnnotation = "kueue.x-k8s.io/pod-group-pod-index-label"

	// NodeToReplaceAnnotation is an annotation on a Workload. It holds a
	// name of a failed node running at least one pod of this workload.
	NodeToReplaceAnnotation = "alpha.kueue.x-k8s.io/node-to-replace"

	// PodSetGroupName is an annotation indicating the name of the group of PodSets. PodSet Group
	// is a unit flavor assignment and topology domain fitting.
	PodSetGroupName = "kueue.x-k8s.io/podset-group-name"
)

// TopologySpec defines the desired state of Topology
type TopologySpec struct {
	// levels define the levels of topology.
	//
	// +required
	// +listType=atomic
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=16
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="field is immutable"
	// +kubebuilder:validation:XValidation:rule="size(self.filter(i, size(self.filter(j, j == i)) > 1)) == 0",message="must be unique"
	// +kubebuilder:validation:XValidation:rule="size(self.filter(i, i.nodeLabel == 'kubernetes.io/hostname')) == 0 || self[size(self) - 1].nodeLabel == 'kubernetes.io/hostname'",message="the kubernetes.io/hostname label can only be used at the lowest level of topology"
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

	// +kubebuilder:validation:Required
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
