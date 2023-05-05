/*
Copyright 2023 The Kubernetes Authors.

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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// WorkloadSpec defines the desired state of Workload
type WorkloadSpec struct {
	// podSets is a list of sets of homogeneous pods, each described by a Pod spec
	// and a count.
	// There must be at least one element and at most 8.
	// podSets cannot be changed.
	//
	// +listType=map
	// +listMapKey=name
	// +kubebuilder:validation:MaxItems=8
	// +kubebuilder:validation:MinItems=1
	PodSets []PodSet `json:"podSets"`

	// queueName is the name of the LocalQueue the Workload is associated with.
	// queueName cannot be changed while .status.admission is not null.
	QueueName string `json:"queueName,omitempty"`

	// If specified, indicates the workload's priority.
	// "system-node-critical" and "system-cluster-critical" are two special
	// keywords which indicate the highest priorities with the former being
	// the highest priority. Any other name must be defined by creating a
	// PriorityClass object with that name. If not specified, the workload
	// priority will be default or zero if there is no default.
	PriorityClassName string `json:"priorityClassName,omitempty"`

	// Priority determines the order of access to the resources managed by the
	// ClusterQueue where the workload is queued.
	// The priority value is populated from PriorityClassName.
	// The higher the value, the higher the priority.
	// If priorityClassName is specified, priority must not be null.
	Priority *int32 `json:"priority,omitempty"`
}

type Admission struct {
	// clusterQueue is the name of the ClusterQueue that admitted this workload.
	ClusterQueue ClusterQueueReference `json:"clusterQueue"`

	// PodSetAssignments hold the admission results for each of the .spec.podSets entries.
	// +listType=map
	// +listMapKey=name
	PodSetAssignments []PodSetAssignment `json:"podSetAssignments"`
}

type PodSetAssignment struct {
	// Name is the name of the podSet. It should match one of the names in .spec.podSets.
	// +kubebuilder:default=main
	Name string `json:"name"`

	// Flavors are the flavors assigned to the workload for each resource.
	Flavors map[corev1.ResourceName]ResourceFlavorReference `json:"flavors,omitempty"`

	// resourceUsage keeps track of the total resources all the pods in the podset need to run.
	//
	// Beside what is provided in podSet's specs, this calculation takes into account
	// the LimitRange defaults and RuntimeClass overheads at the moment of admission.
	ResourceUsage corev1.ResourceList `json:"resourceUsage,omitempty"`
}

type PodSet struct {
	// name is the PodSet name.
	Name string `json:"name"`

	// template is the Pod template.
	//
	// The only allowed fields in template.metadata are labels and annotations.
	//
	// If requests are omitted for a container or initContainer,
	// they default to the limits if they are explicitly specified for the
	// container or initContainer.
	//
	// During admission, the rules in nodeSelector and
	// nodeAffinity.requiredDuringSchedulingIgnoredDuringExecution that match
	// the keys in the nodeLabels from the ResourceFlavors considered for this
	// Workload are used to filter the ResourceFlavors that can be assigned to
	// this podSet.
	Template corev1.PodTemplateSpec `json:"template"`

	// count is the number of pods for the spec.
	// +kubebuilder:validation:Minimum=1
	Count int32 `json:"count"`
}

// WorkloadStatus defines the observed state of Workload
type WorkloadStatus struct {
	// admission holds the parameters of the admission of the workload by a
	// ClusterQueue. admission can be set back to null, but its fields cannot be
	// changed once set.
	Admission *Admission `json:"admission,omitempty"`

	// conditions hold the latest available observations of the Workload
	// current state.
	//
	// The type of the condition could be:
	//
	// - Admitted: the Workload was admitted through a ClusterQueue.
	// - Finished: the associated workload finished running (failed or succeeded).
	// - PodsReady: at least `.spec.podSets[*].count` Pods are ready or have
	// succeeded.
	//
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`

	// reclaimablePods keeps track of the number pods within a podset for which
	// the resource reservation is no longer needed.
	// +optional
	// +listType=map
	// +listMapKey=name
	ReclaimablePods []ReclaimablePod `json:"reclaimablePods,omitempty"`
}

type ReclaimablePod struct {
	// name is the PodSet name.
	Name string `json:"name"`

	// count is the number of pods for which the requested resources are no longer needed.
	// +kubebuilder:validation:Minimum=0
	Count int32 `json:"count"`
}

const (
	// WorkloadAdmitted means that the Workload was admitted by a ClusterQueue.
	WorkloadAdmitted = "Admitted"

	// WorkloadFinished means that the workload associated to the
	// ResourceClaim finished running (failed or succeeded).
	WorkloadFinished = "Finished"

	// WorkloadPodsReady means that at least `.spec.podSets[*].count` Pods are
	// ready or have succeeded.
	WorkloadPodsReady = "PodsReady"

	// WorkloadEvicted means that the Workload was evicted by a ClusterQueue
	WorkloadEvicted = "Evicted"
)

const (
	// WorkloadEvictedByPreemption indicates that the workload was evicted
	// in order to free resources for a workload with a higher priority.
	WorkloadEvictedByPreemption = "Preempted"

	// WorkloadEvictedByPodsReadyTimeout indicates that the eviction took
	// place due to a PodsReady timeout.
	WorkloadEvictedByPodsReadyTimeout = "PodsReadyTimeout"
)

// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:subresource:status
// +kubebuilder:printcolumn:name="Queue",JSONPath=".spec.queueName",type=string,description="Name of the queue this workload was submitted to"
// +kubebuilder:printcolumn:name="Admitted by",JSONPath=".status.admission.clusterQueue",type=string,description="Name of the ClusterQueue that admitted this workload"
// +kubebuilder:printcolumn:name="Age",JSONPath=".metadata.creationTimestamp",type=date,description="Time this workload was created"
// +kubebuilder:resource:shortName={wl}

// Workload is the Schema for the workloads API
type Workload struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   WorkloadSpec   `json:"spec,omitempty"`
	Status WorkloadStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// WorkloadList contains a list of ResourceClaim
type WorkloadList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Workload `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Workload{}, &WorkloadList{})
}
