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
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	// CapacityBufferControllerName is the name used by the CapacityBuffer
	// admission check controller.
	CapacityBufferControllerName = "kueue.x-k8s.io/capacity-buffer"
)

// CapacityBufferConfigSpec defines the desired state of CapacityBufferConfig
type CapacityBufferConfigSpec struct {
	// managedResources contains the list of resources managed by this check.
	//
	// If empty, all resources are considered managed.
	//
	// If not empty, CapacityBuffers are created only for the podsets that are
	// requesting at least one of them, one CapacityBuffer per podset.
	//
	// If none of the workload's podsets is requesting at least a managed resource,
	// the workload is considered ready.
	//
	// +optional
	// +listType=set
	// +kubebuilder:validation:MaxItems=100
	ManagedResources []corev1.ResourceName `json:"managedResources,omitempty"`

	// timeoutSeconds is how long Kueue waits for the CapacityBuffers to reach
	// FitsExistingCapacity before setting the AdmissionCheck to Retry, which
	// evicts the workload and releases its quota. The timeout is measured from
	// the creation of the CapacityBuffers.
	//
	// If unset, Kueue waits indefinitely.
	//
	// +optional
	// +kubebuilder:validation:Minimum=1
	TimeoutSeconds *int32 `json:"timeoutSeconds,omitempty"`
}

// +genclient
// +genclient:nonNamespaced
// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:resource:scope=Cluster,shortName={cbc}

// CapacityBufferConfig is the Schema for the capacitybufferconfig API
type CapacityBufferConfig struct {
	metav1.TypeMeta `json:",inline"`
	// metadata is the standard object metadata.
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// spec is the specification of the CapacityBufferConfig.
	// +optional
	Spec CapacityBufferConfigSpec `json:"spec"`
}

// +kubebuilder:object:root=true

// CapacityBufferConfigList contains a list of CapacityBufferConfig
type CapacityBufferConfigList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []CapacityBufferConfig `json:"items"`
}
