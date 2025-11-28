/*
Copyright 2024 The Kubernetes Authors.

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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	clientcmdv1 "k8s.io/client-go/tools/clientcmd/api/v1"
)

// ClusterProfileSpec defines the desired state of ClusterProfile.
type ClusterProfileSpec struct {
	// DisplayName defines a human-readable name of the ClusterProfile
	// +optional
	DisplayName string `json:"displayName,omitempty"`

	// ClusterManager defines which cluster manager owns this ClusterProfile resource
	// +required
	ClusterManager ClusterManager `json:"clusterManager"`
}

// ClusterManager defines which cluster manager owns this ClusterProfile resource.
// A cluster manager is a system that centralizes the administration, coordination,
// and operation of multiple clusters across various infrastructures.
// Examples of cluster managers include Open Cluster Management, AZ Fleet, Karmada, and Clusternet.
//
// This field is immutable.
// It's recommended that each cluster manager instance should set a different values to this field.
// In addition, it's recommended that a predefined label with key "x-k8s.io/cluster-manager"
// should be added by the cluster manager upon creation. See constant LabelClusterManagerKey.
// The value of the label should be the same as the name of the cluster manager.
// The purpose of this label is to make filter clusters from different cluster managers easier.
//
// +kubebuilder:validation:XValidation:rule="self == oldSelf",message="ClusterManager is immutable"
type ClusterManager struct {
	// Name defines the name of the cluster manager
	// +required
	Name string `json:"name"`
}

// ClusterProfileStatus defines the observed state of ClusterProfile.
type ClusterProfileStatus struct {
	// Conditions contains the different condition statuses for this cluster.
	// +optional
	Conditions []metav1.Condition `json:"conditions"`

	// Version defines the version information of the cluster.
	// +optional
	Version ClusterVersion `json:"version,omitempty"`

	// Properties defines cluster characteristics through a list of Property objects.
	// Each Property can be one of:
	// 1. A ClusterProperty resource (as defined in KEP-2149)
	// 2. Custom information from cluster manager implementations
	// Property names support both:
	// - Standard names from ClusterProperty resources
	// - Custom names defined by cluster managers
	// +optional
	Properties []Property `json:"properties,omitempty"`

	// CredentialProviders is a list of cluster access providers that can provide access
	// information for clusters.
	// Deprecated: Use AccessProviders instead. If both AccessProviders and CredentialProviders are provided, both are used. In case they specify a provider with the same name, the one in AccessProviders is preferred.
	// +optional
	// +deprecated
	CredentialProviders []CredentialProvider `json:"credentialProviders,omitempty"`

	// AccessProviders is a list of cluster access providers that can provide access
	// information for clusters.
	// +optional
	AccessProviders []AccessProvider `json:"accessProviders,omitempty"`
}

// AccessProvider defines how to access the cluster.
// It contains the name of the provider name and the cluster connection details.
// The name is used to identify different access info types, such as "kubeconfig" or "oidc".
// The Cluster field contains the actual cluster connection details, such as server address,
// certificate authority data, and authentication information.
type AccessProvider struct {
	Name    string              `json:"name"`
	Cluster clientcmdv1.Cluster `json:"cluster,omitempty"`
}

type CredentialProvider = AccessProvider

// ClusterVersion represents version information about the cluster.
type ClusterVersion struct {
	// Kubernetes is the kubernetes version of the cluster.
	// +optional
	Kubernetes string `json:"kubernetes,omitempty"`
}

// Property defines the data structure to represent a property of a cluster.
// It contains a name/value pair and the last observed time of the property on the cluster.
// This property can store various configurable details and metrics of a cluster,
// which may include information such as the entry point of the cluster, types of nodes, location, etc. according to KEP 4322.
type Property struct {
	// Name is the name of a property resource on cluster. It's a well-known
	// or customized name to identify the property.
	// +kubebuilder:validation:MaxLength=253
	// +kubebuilder:validation:MinLength=1
	// +required
	Name string `json:"name"`

	// Value is a property-dependent string
	// +kubebuilder:validation:MaxLength=1024
	// +kubebuilder:validation:MinLength=1
	// +required
	Value string `json:"value"`

	// LastObservedTime is the last time the property was observed on the corresponding cluster.
	// The value is the timestamp when the property was observed not the time when the property was updated in the cluster-profile.
	// +kubebuilder:validation:Type=string
	// +kubebuilder:validation:Format=date-time
	// +optional
	LastObservedTime metav1.Time `json:"lastObservedTime,omitempty"`
}

// Predefined healthy conditions indicate the cluster is in a good state or not.
// The condition and states conforms to metav1.Condition format.
// States are True/False/Unknown.
const (
	// ClusterConditionControlPlaneHealthy means the controlplane of the cluster is in a healthy state.
	// If the control plane is not healthy, then the status condition will be "False".
	ClusterConditionControlPlaneHealthy string = "ControlPlaneHealthy"
)

const (
	// LabelClusterManagerKey is used to indicate the name of the cluster manager that a ClusterProfile belongs to.
	// The value of the label MUST be the same as the name of the cluster manager.
	// The purpose of this label is to make filter clusters from different cluster managers easier.
	LabelClusterManagerKey = "x-k8s.io/cluster-manager"

	// LabelClusterSetKey is used on a namespace to indicate the clusterset that a ClusterProfile belongs to.
	// If a cluster inventory represents a ClusterSet,
	// all its ClusterProfile objects MUST be part of the same clusterSet and namespace must be used as the grouping mechanism.
	// The namespace MUST have LabelClusterSet and the value as the name of the clusterSet.
	LabelClusterSetKey = "multicluster.x-k8s.io/clusterset"
)

//+genclient
//+kubebuilder:object:root=true
//+kubebuilder:subresource:status
//+kubebuilder:resource:scope=Namespaced

// ClusterProfile represents a single cluster in a multi-cluster deployment.
type ClusterProfile struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	// +required
	Spec ClusterProfileSpec `json:"spec"`

	// +optional
	Status ClusterProfileStatus `json:"status,omitempty"`
}

//+kubebuilder:object:root=true

// ClusterProfileList contains a list of ClusterProfile.
type ClusterProfileList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ClusterProfile `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ClusterProfile{}, &ClusterProfileList{})
}
