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

package v1beta2

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type CheckState string

const (
	// CheckStateRetry means that the check cannot pass at this moment, back off (possibly
	// allowing other to try, unblock quota) and retry.
	// A workload having at least one check in this state will be evicted if admitted and
	// will not be considered for admission while the check is in this state.
	CheckStateRetry CheckState = "Retry"

	// CheckStateRejected means that the check will not pass in the near future. It is not worth
	// to retry.
	// A workload having at least one check in this state will be evicted if admitted and deactivated.
	CheckStateRejected CheckState = "Rejected"

	// CheckStatePending means that the check still hasn't been performed and the state can be
	// 1. Unknown, the condition was added by kueue and its controller was not able to evaluate it.
	// 2. Set by its controller and reevaluated after quota is reserved.
	CheckStatePending CheckState = "Pending"

	// CheckStateReady means that the check has passed.
	// A workload having all its checks ready, and quota reserved can begin execution.
	CheckStateReady CheckState = "Ready"
)

// AdmissionCheckSpec defines the desired state of AdmissionCheck
type AdmissionCheckSpec struct {
	// controllerName identifies the controller that processes the AdmissionCheck,
	// not necessarily a Kubernetes Pod or Deployment name. Cannot be empty.
	// +required
	// +kubebuilder:validation:XValidation:rule="self == oldSelf", message="field is immutable"
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:MinLength=1
	ControllerName string `json:"controllerName"` //nolint:kubeapilinter // ignore omitempty

	// parameters identifies a configuration with additional parameters for the
	// check.
	// +optional
	Parameters *AdmissionCheckParametersReference `json:"parameters,omitempty"`
}

type AdmissionCheckParametersReference struct {
	// apiGroup is the group for the resource being referenced.
	// +required
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:Pattern="^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$"
	APIGroup string `json:"apiGroup,omitempty"`
	// kind is the type of the resource being referenced.
	// +required
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:Pattern="^(?i)[a-z]([-a-z0-9]*[a-z0-9])?$"
	Kind string `json:"kind,omitempty"`
	// name is the name of the resource being referenced.
	// +required
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:Pattern="^[a-z0-9]([-a-z0-9]*[a-z0-9])?$"
	Name string `json:"name,omitempty"`
}

// AdmissionCheckStatus defines the observed state of AdmissionCheck
type AdmissionCheckStatus struct {
	// conditions hold the latest available observations of the AdmissionCheck
	// current state.
	// This is limited to at most 16 separate conditions.
	// +optional
	// +listType=map
	// +listMapKey=type
	// +patchStrategy=merge
	// +patchMergeKey=type
	// +kubebuilder:validation:MaxItems=16
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`
}

const (
	// AdmissionCheckActive indicates that the controller of the admission check is
	// ready to evaluate the checks states
	AdmissionCheckActive string = "Active"
)

// +genclient
// +genclient:nonNamespaced
// +kubebuilder:object:root=true
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster

// AdmissionCheck is the Schema for the admissionchecks API
type AdmissionCheck struct {
	metav1.TypeMeta `json:",inline"`
	// metadata is the metadata of the AdmissionCheck.
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// spec is the specification of the AdmissionCheck.
	// +optional
	Spec AdmissionCheckSpec `json:"spec"` //nolint:kubeapilinter // spec should not be a pointer

	// status is the status of the AdmissionCheck.
	// +optional
	Status AdmissionCheckStatus `json:"status,omitempty"` //nolint:kubeapilinter // status should not be a pointer
}

// +kubebuilder:object:root=true

// AdmissionCheckList contains a list of AdmissionCheck
type AdmissionCheckList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []AdmissionCheck `json:"items"`
}

func init() {
	SchemeBuilder.Register(&AdmissionCheck{}, &AdmissionCheckList{})
}
