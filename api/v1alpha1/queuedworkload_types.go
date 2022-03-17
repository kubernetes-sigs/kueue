/*
Copyright 2022 The Kubernetes Authors.

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
	"k8s.io/apimachinery/pkg/types"
)

// QueuedWorkloadSpec defines the desired state of QueuedWorkload
type QueuedWorkloadSpec struct {
	// workload that requested these resources.
	Workload WorkloadReference `json:"workload"`

	// pods is a list of sets of homogeneous pods, each described by a Pod spec
	// and a count.
	//
	// +listType=map
	// +listMapKey=name
	PodSets []PodSet `json:"pods,omitempty"`

	// queueName is the name of the queue the QueuedWorkload is associated with.
	QueueName string `json:"queueName"`

	// admission holds the parameters of the admission of the workload by a ClusterQueue.
	Admission *Admission `json:"admission,omitempty"`
}

type Admission struct {
	// clusterQueue is the name of the ClusterQueue that admitted this workload.
	ClusterQueue ClusterQueueReference `json:"clusterQueue"`

	// podSetFlavors hold the admission results for each of the .spec.podSets entries.
	// +listType=map
	// +listMapKey=name
	PodSetFlavors []PodSetFlavors `json:"podSetFlavors"`
}

type PodSetFlavors struct {
	// Name is the name of the podSet. It should match one of the names in .spec.podSets.
	// +kubebuilder:default=main
	Name string `json:"name"`

	// Flavors are the flavors assigned to the workload for each resource.
	Flavors map[corev1.ResourceName]string `json:"flavors,omitempty"`
}

type WorkloadReference struct {
	// apiVersion of the referent. It includes the API group, if applicable.
	APIVersion string `json:"apiVersion,omitempty"`

	// kind of the referent.
	Kind string `json:"kind,omitempty"`

	// name of the referent.
	Name string `json:"name,omitempty"`

	// uid of the referent.
	UID types.UID `json:"uid,omitempty"`
}

type PodSet struct {
	// name is the PodSet name.
	// +kubebuilder:default=main
	Name string `json:"name"`

	// spec is the Pod spec.
	Spec corev1.PodSpec `json:"spec"`

	// count is the number of pods for the spec.
	Count int32 `json:"count"`
}

// QueuedWorkloadStatus defines the observed state of QueuedWorkload
type QueuedWorkloadStatus struct {
	// conditions hold the latest available observations of the QueuedWorkload
	// current state.
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []QueuedWorkloadCondition `json:"conditions,omitempty"`
}

type QueuedWorkloadCondition struct {
	// type of condition could be:
	//
	// Admitted: the QueuedWorkload was admitted through a ClusterQueue.
	//
	// Finished: the associated workload finished running (failed or succeeded).
	Type QueuedWorkloadConditionType `json:"type"`

	// status could be True, False or Unknown.
	Status corev1.ConditionStatus `json:"status"`

	// lastProbeTime is the last time the condition was checked.
	// +optional
	LastProbeTime metav1.Time `json:"lastProbeTime,omitempty"`

	// lastTransitionTime is the last time the condition transit from one status
	// to another.
	// +optional
	LastTransitionTime metav1.Time `json:"lastTransitionTime,omitempty"`

	// reason is a brief reason for the condition's last transition.
	// +optional
	Reason string `json:"reason,omitempty"`

	// message is a human readable message indicating details about last
	// transition.
	// +optional
	Message string `json:"message,omitempty"`
}

type QueuedWorkloadConditionType string

const (
	// QueuedWorkloadAdmitted means that the QueuedWorkload was admitted by a ClusterQueue.
	QueuedWorkloadAdmitted QueuedWorkloadConditionType = "Admitted"

	// QueuedWorkloadFinished means that the workload associated to the
	// ResourceClaim finished running (failed or succeeded).
	QueuedWorkloadFinished QueuedWorkloadConditionType = "Finished"
)

//+kubebuilder:object:root=true
//+kubebuilder:resource:shortName={workload,qw}
//+kubebuilder:subresource:status

// QueuedWorkload is the Schema for the queuedworkloads API
type QueuedWorkload struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   QueuedWorkloadSpec   `json:"spec,omitempty"`
	Status QueuedWorkloadStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// QueuedWorkloadList contains a list of ResourceClaim
type QueuedWorkloadList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []QueuedWorkload `json:"items"`
}

func init() {
	SchemeBuilder.Register(&QueuedWorkload{}, &QueuedWorkloadList{})
}
