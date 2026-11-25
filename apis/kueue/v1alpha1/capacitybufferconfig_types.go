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

	// CapacityBufferSchedulingGate keeps Pods unschedulable until Kueue deletes the CapacityBuffer of their PodSet.
	CapacityBufferSchedulingGate = "kueue.x-k8s.io/capacity-buffer"

	// CapacityBufferGatedAnnotation asks Kueue to add CapacityBufferSchedulingGate to a PodSet,
	// because PodSetUpdate cannot set scheduling gates.
	CapacityBufferGatedAnnotation = "kueue.x-k8s.io/capacity-buffer-gated"
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
	// the quota reservation, so it also applies when the CapacityBuffers cannot
	// be created. With Topology Aware Scheduling, it also limits the wait for the
	// topology assignment after the check is Ready. That wait is measured from
	// the time the check became Ready.
	//
	// If unset, Kueue waits indefinitely.
	//
	// +optional
	// +kubebuilder:validation:Minimum=1
	TimeoutSeconds *int32 `json:"timeoutSeconds,omitempty"`

	// retryStrategy defines how long Kueue waits before it requeues a workload
	// that was evicted after timeoutSeconds.
	// If null, backoffBaseSeconds is 60 and backoffMaxSeconds is 1800.
	//
	// +optional
	// +kubebuilder:default={backoffBaseSeconds:60,backoffMaxSeconds:1800}
	RetryStrategy *CapacityBufferRetryStrategy `json:"retryStrategy,omitempty"`
}

// CapacityBufferRetryStrategy defines the backoff for requeuing a workload
// after its CapacityBuffers did not fit existing capacity within timeoutSeconds.
// +kubebuilder:validation:XValidation:rule="!has(self.backoffBaseSeconds) || !has(self.backoffMaxSeconds) || self.backoffBaseSeconds <= self.backoffMaxSeconds",message="backoffBaseSeconds must be less than or equal to backoffMaxSeconds"
type CapacityBufferRetryStrategy struct {
	// backoffBaseSeconds is the base of the exponential backoff for requeuing
	// a workload after a timeout. The delays are about b*2^(n-1), where n is
	// the number of retries.
	//
	// It must be at least 1, since Kueue retries without a limit, and at most
	// backoffMaxSeconds. Defaults to 60.
	// +optional
	// +kubebuilder:default=60
	// +kubebuilder:validation:Minimum=1
	BackoffBaseSeconds *int32 `json:"backoffBaseSeconds,omitempty"`

	// backoffMaxSeconds is the maximum backoff.
	//
	// It must be at least 1. Defaults to 1800.
	// +optional
	// +kubebuilder:default=1800
	// +kubebuilder:validation:Minimum=1
	BackoffMaxSeconds *int32 `json:"backoffMaxSeconds,omitempty"`
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
