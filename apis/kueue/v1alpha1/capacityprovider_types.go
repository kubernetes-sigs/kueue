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

type CapacityProviderSpec struct {
	// orchestratedFlavors identifies the ResourceFlavors for which this provider may
	// publish capacity. DQO ignores entries in status.capacity.flavors whose
	// names are not listed here.
	//
	// +required
	// +listType=map
	// +listMapKey=name
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=64
	OrchestratedFlavors []CapacityProviderOrchestratedFlavor `json:"orchestratedFlavors"`

	// controllerName identifies the controller publishing capacity.
	// This field is immutable.
	//
	// +required
	// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="field is immutable"
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	ControllerName CapacityProviderControllerName `json:"controllerName"`

	// parameters optionally references implementation-specific configuration.
	// DQO does not read or validate the referenced object.
	//
	// +optional
	Parameters *CapacityProviderParametersReference `json:"parameters,omitempty"`
}

// CapacityProviderOrchestratedFlavor identifies a flavor managed by a provider.
// The container allows the mapping to be extended in a future API version.
type CapacityProviderOrchestratedFlavor struct {
	// name identifies the ResourceFlavor managed by this provider.
	//
	// +required
	Name ResourceFlavorReference `json:"name"`
}

type CapacityProviderParametersReference struct {
	// apiGroup is the group for the resource being referenced.
	// +required
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:Pattern="^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$"
	APIGroup string `json:"apiGroup"`

	// kind is the type of the resource being referenced.
	// +required
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:Pattern="^(?i)[a-z]([-a-z0-9]*[a-z0-9])?$"
	Kind string `json:"kind"`

	// name is the name of the resource being referenced.
	// +required
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:Pattern="^[a-z0-9]([-a-z0-9]*[a-z0-9])?$"
	Name string `json:"name"`
}

type CapacityProviderStatus struct {
	// capacity is the normalized capacity published by the provider.
	//
	// +optional
	Capacity *CapacityProviderNormalizedCapacity `json:"capacity,omitempty"`

	// conditions represents the current state of this provider.
	//
	// +optional
	// +listType=map
	// +listMapKey=type
	// +patchStrategy=merge
	// +patchMergeKey=type
	// +kubebuilder:validation:MaxItems=16
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`
}

type CapacityProviderNormalizedCapacity struct {
	// flavors contains capacity per flavor and resource.
	//
	// +required
	// +listType=map
	// +listMapKey=name
	// +kubebuilder:validation:MaxItems=64
	Flavors []CapacityProviderNormalizedCapacityFlavor `json:"flavors"`
}

type CapacityProviderNormalizedCapacityFlavor struct {
	// name identifies the ResourceFlavor whose capacity is reported.
	//
	// +required
	Name ResourceFlavorReference `json:"name"`

	// resources contains total capacity by resource name.
	//
	// +required
	// +kubebuilder:validation:XValidation:rule="size(self) >= 1 && size(self) <= 64",message="resource capacity must have between 1 and 64 entries"
	Resources corev1.ResourceList `json:"resources"`
}

// +kubebuilder:validation:MinLength=1
// +kubebuilder:validation:MaxLength=253
type CapacityProviderControllerName string

// CapacityProvider condition types and reasons
const (
	// CapacityProviderCapacitySynchronized indicates whether status.capacity is synchronized
	// with observations from the underlying capacity source.
	CapacityProviderCapacitySynchronized string = "CapacitySynchronized"

	// CapacityProviderReasonSynchronized indicates that capacity was successfully observed and published.
	CapacityProviderReasonSynchronized string = "Synchronized"

	// CapacityProviderReasonSourceUnavailable indicates that the controller cannot reach the capacity source.
	CapacityProviderReasonSourceUnavailable string = "SourceUnavailable"

	// CapacityProviderReasonInvalidCapacity indicates that observed capacity contained negative or corrupt quantities.
	CapacityProviderReasonInvalidCapacity string = "InvalidCapacity"

	// CapacityProviderReasonMisconfigured indicates that spec parameters or flavor mappings are invalid.
	CapacityProviderReasonMisconfigured string = "Misconfigured"
)

// +genclient
// +genclient:nonNamespaced
// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster,shortName={cp}

// CapacityProvider is the Schema for the capacityproviders API
type CapacityProvider struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   CapacityProviderSpec   `json:"spec,omitempty"`
	Status CapacityProviderStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// CapacityProviderList contains a list of CapacityProvider
type CapacityProviderList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []CapacityProvider `json:"items"`
}
