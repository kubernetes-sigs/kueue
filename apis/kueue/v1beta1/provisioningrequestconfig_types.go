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

const (
	// ProvisioningRequestControllerName is the name used by the Provisioning
	// Request admission check controller.
	ProvisioningRequestControllerName = "kueue.x-k8s.io/provisioning-request"
)

// ProvisioningRequestConfigSpec defines the desired state of ProvisioningRequestConfig
type ProvisioningRequestConfigSpec struct {
	// ProvisioningClassName describes the different modes of provisioning the resources.
	// Check autoscaling.x-k8s.io ProvisioningRequestSpec.ProvisioningClassName for details.
	//
	// +kubebuilder:validation:Required
	// +kubebuilder:validation:Pattern=`^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$`
	// +kubebuilder:validation:MaxLength=253
	ProvisioningClassName string `json:"provisioningClassName"`

	// Parameters contains all other parameters classes may require.
	//
	// +optional
	// +kubebuilder:validation:MaxProperties=100
	Parameters map[string]Parameter `json:"parameters,omitempty"`

	// managedResources contains the list of resources managed by the autoscaling.
	//
	// If empty, all resources are considered managed.
	//
	// If not empty, the ProvisioningRequest will contain only the podsets that are
	// requesting at least one of them.
	//
	// If none of the workloads podsets is requesting at least a managed resource,
	// the workload is considered ready.
	//
	// +optional
	// +listType=set
	// +kubebuilder:validation:MaxItems=100
	ManagedResources []corev1.ResourceName `json:"managedResources,omitempty"`

	// retryStrategy defines strategy for retrying ProvisioningRequest.
	// If null, then the default configuration is applied with the following parameter values:
	// backoffLimitCount:  3
	// backoffBaseSeconds: 60 - 1 min
	// backoffMaxSeconds:  1800 - 30 mins
	//
	// To switch off retry mechanism
	// set retryStrategy.backoffLimitCount to 0.
	//
	// +optional
	// +kubebuilder:default={backoffLimitCount:3,backoffBaseSeconds:60,backoffMaxSeconds:1800}
	RetryStrategy *ProvisioningRequestRetryStrategy `json:"retryStrategy,omitempty"`
}

type ProvisioningRequestRetryStrategy struct {
	// BackoffLimitCount defines the maximum number of re-queuing retries.
	// Once the number is reached, the workload is deactivated (`.spec.activate`=`false`).
	//
	// Every backoff duration is about "b*2^(n-1)+Rand" where:
	// - "b" represents the base set by "BackoffBaseSeconds" parameter,
	// - "n" represents the "workloadStatus.requeueState.count",
	// - "Rand" represents the random jitter.
	// During this time, the workload is taken as an inadmissible and
	// other workloads will have a chance to be admitted.
	// By default, the consecutive requeue delays are around: (60s, 120s, 240s, ...).
	//
	// Defaults to 3.
	// +optional
	// +kubebuilder:default=3
	BackoffLimitCount *int32 `json:"backoffLimitCount,omitempty"`

	// BackoffBaseSeconds defines the base for the exponential backoff for
	// re-queuing an evicted workload.
	//
	// Defaults to 60.
	// +optional
	// +kubebuilder:default=60
	BackoffBaseSeconds *int32 `json:"backoffBaseSeconds,omitempty"`

	// BackoffMaxSeconds defines the maximum backoff time to re-queue an evicted workload.
	//
	// Defaults to 1800.
	// +optional
	// +kubebuilder:default=1800
	BackoffMaxSeconds *int32 `json:"backoffMaxSeconds,omitempty"`
}

// Parameter is limited to 255 characters.
// +kubebuilder:validation:MaxLength=255
type Parameter string

// +genclient
// +genclient:nonNamespaced
// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:resource:scope=Cluster

// ProvisioningRequestConfig is the Schema for the provisioningrequestconfig API
type ProvisioningRequestConfig struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec ProvisioningRequestConfigSpec `json:"spec,omitempty"`
}

// +kubebuilder:object:root=true

// ProvisioningRequestConfigList contains a list of ProvisioningRequestConfig
type ProvisioningRequestConfigList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ProvisioningRequestConfig `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ProvisioningRequestConfig{}, &ProvisioningRequestConfigList{})
}
