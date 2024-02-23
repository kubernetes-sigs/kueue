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

package v1beta1

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
	// A workload having at least one check in this state will be evicted if admitted and
	// will be marked as Finished.
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
	// controllerName is name of the controller which will actually perform
	// the checks. This is the name with which controller identifies with,
	// not necessarily a K8S Pod or Deployment name. Cannot be empty.
	ControllerName string `json:"controllerName"`

	// RetryDelayMinutes specifies how long to keep the workload suspended
	// after a failed check (after it transitioned to False).
	// After that the check state goes to "Unknown".
	// The default is 15 min.
	// +optional
	// +kubebuilder:default=15
	RetryDelayMinutes *int64 `json:"retryDelayMinutes,omitempty"`

	// Parameters identifies the resource providing additional check parameters.
	// +optional
	Parameters *AdmissionCheckParametersReference `json:"parameters,omitempty"`
}

type AdmissionCheckParametersReference struct {
	// ApiGroup is the group for the resource being referenced.
	APIGroup string `json:"apiGroup"`
	// Kind is the type of the resource being referenced.
	Kind string `json:"kind"`
	// Name is the name of the resource being referenced.
	Name string `json:"name"`
}

// AdmissionCheckStatus defines the observed state of AdmissionCheck
type AdmissionCheckStatus struct {
	// conditions hold the latest available observations of the AdmissionCheck
	// current state.
	// +optional
	// +listType=map
	// +listMapKey=type
	// +patchStrategy=merge
	// +patchMergeKey=type
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
// +kubebuilder:storageversion
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster

// AdmissionCheck is the Schema for the admissionchecks API
type AdmissionCheck struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   AdmissionCheckSpec   `json:"spec,omitempty"`
	Status AdmissionCheckStatus `json:"status,omitempty"`
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
