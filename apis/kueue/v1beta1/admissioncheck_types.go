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

type AdmissionCheckState string

const (
	CheckStateRetry    AdmissionCheckState = "Retry"
	CheckStateRejected AdmissionCheckState = "Rejected"

	CheckStateUnknown  AdmissionCheckState = "Unknown"
	CheckStateAccepted AdmissionCheckState = "Accepted"
)

// AdmissionCheckSpec defines the desired state of AdmissionCheck
type AdmissionCheckSpec struct {
	// ControllerName identifies the controller managing this condition.
	// +optional
	ControllerName string `json:"controllerName"`

	// RetryDelayMinutes specifies the duration in minutes between tow checks for the same
	// workload.
	// +optional
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
}

//+kubebuilder:object:root=true
//+kubebuilder:subresource:status
//+kubebuilder:resource:scope=Cluster

// AdmissionCheck is the Schema for the admissionchecks API
type AdmissionCheck struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   AdmissionCheckSpec   `json:"spec,omitempty"`
	Status AdmissionCheckStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// AdmissionCheckList contains a list of AdmissionCheck
type AdmissionCheckList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []AdmissionCheck `json:"items"`
}

func init() {
	SchemeBuilder.Register(&AdmissionCheck{}, &AdmissionCheckList{})
}
