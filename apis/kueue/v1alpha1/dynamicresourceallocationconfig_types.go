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

// +genclient
// +kubebuilder:object:root=true
// +kubebuilder:storageversion

// DynamicResourceAllocationConfig is a singleton CRD that maps a logical resource name to one or more DeviceClasses
// in the cluster. Only one instance named "default" in the kueue-system namespace
// is allowed.
type DynamicResourceAllocationConfig struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec DynamicResourceAllocationConfigSpec `json:"spec"`
}

// DynamicResourceAllocationConfigSpec holds all resource to DeviceClass mappings.
type DynamicResourceAllocationConfigSpec struct {
	// Resources lists logical resources that Kueue will account.
	// +listType=map
	// +listMapKey=name
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=16
	Resources []DynamicResource `json:"resources"`
}

// DynamicResource describes a single logical resource and the DeviceClasses mapping. The resource name is used
// to quota in ClusterQueue.
type DynamicResource struct {
	// Name is referenced in ClusterQueue.nominalQuota and Workload status.
	Name corev1.ResourceName `json:"name"`

	// DeviceClassNames enumerates the DeviceClasses represented by this resource name.
	// +listType=set
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=8
	DeviceClassNames []corev1.ResourceName `json:"deviceClassNames"`
}

// +kubebuilder:object:root=true

// DynamicResourceAllocationConfigList satisfies the kubernetes runtime.Object
// interface (even though only one instance is expected).
type DynamicResourceAllocationConfigList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []DynamicResourceAllocationConfig `json:"items"`
}

func init() {
	SchemeBuilder.Register(
		&DynamicResourceAllocationConfig{},
		&DynamicResourceAllocationConfigList{},
	)
}
