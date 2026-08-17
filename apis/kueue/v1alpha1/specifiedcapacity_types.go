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
	// SpecifiedCapacityControllerName is the name used by the Specified Capacity provider controller.
	SpecifiedCapacityControllerName CapacityProviderControllerName = "kueue.x-k8s.io/specified-capacity"
)

type SpecifiedCapacitySpec struct {
	// flavors contains capacity per flavor and resource.
	//
	// +listType=map
	// +listMapKey=name
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=64
	Flavors []SpecifiedCapacityFlavor `json:"flavors"`
}

type SpecifiedCapacityFlavor struct {
	// name is the name of the ResourceFlavor.
	//
	// +required
	Name ResourceFlavorReference `json:"name"`

	// resources contains total capacity by resource name.
	//
	// +required
	// +kubebuilder:validation:XValidation:rule="size(self) >= 1 && size(self) <= 64",message="resource capacity must have between 1 and 64 entries"
	// +kubebuilder:validation:XValidation:rule="self.all(r, type(self[r]) == string ? quantity(self[r]).sign() >= 0 : self[r] >= 0)",message="resource capacity must be non-negative"
	Resources corev1.ResourceList `json:"resources"`
}

// +genclient
// +genclient:nonNamespaced
// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:resource:scope=Cluster,shortName={scap}

// SpecifiedCapacity is the Schema for the specifiedcapacities API
type SpecifiedCapacity struct {
	metav1.TypeMeta `json:",inline"`
	// metadata is the metadata of the SpecifiedCapacity.
	// +optional
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// spec is the specification of the SpecifiedCapacity.
	// +optional
	Spec SpecifiedCapacitySpec `json:"spec,omitempty"`
}

// +kubebuilder:object:root=true

// SpecifiedCapacityList contains a list of SpecifiedCapacity
type SpecifiedCapacityList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []SpecifiedCapacity `json:"items"`
}
