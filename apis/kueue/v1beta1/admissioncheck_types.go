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

const (
	// The check cannot pass at this moment, back off (possibly
	// allowing other to try, unblock quota) and retry.
	// A workload having at least one check in the state,
	// will be evicted if admitted will not be considered
	// for admission.
	CheckStateRetry = "Retry"

	// The check will not pass in the near future. It is not worth
	// to retry.
	//NOTE: The admission behaviour is currently the same as for retry,
	// we can consider marking the workload as "Finished" with a failure
	// description.
	CheckStateRejected = "Rejected"

	// The state can be
	// 1. Unknown, the condition was added by kueue and its controller was not able to evaluate it.
	// 2. Set by its controller and reevaluated after quota is reserved.
	CheckStatePending = "Pending"

	// The check has passed.
	// A workload having all its checks ready, and quota reserved can begin execution.
	CheckStateReady = "Ready"
)

// AdmissionCheckSpec defines the desired state of AdmissionCheck
type AdmissionCheckSpec struct {
	// ControllerName identifies the controller managing this condition.
	// +optional
	ControllerName string `json:"controllerName"`

	// RetryDelayMinutes specifies the duration in minutes between two checks for the same
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
