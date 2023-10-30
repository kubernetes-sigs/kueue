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
}

// Parameter is limited to 255 characters.
// +kubebuilder:validation:MaxLength=255
type Parameter string

//+genclient
//+genclient:nonNamespaced
//+kubebuilder:object:root=true
//+kubebuilder:storageversion
//+kubebuilder:resource:scope=Cluster

// ProvisioningRequestConfig is the Schema for the provisioningrequestconfig API
type ProvisioningRequestConfig struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec ProvisioningRequestConfigSpec `json:"spec,omitempty"`
}

//+kubebuilder:object:root=true

// ProvisioningRequestConfigList contains a list of ProvisioningRequestConfig
type ProvisioningRequestConfigList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ProvisioningRequestConfig `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ProvisioningRequestConfig{}, &ProvisioningRequestConfigList{})
}
