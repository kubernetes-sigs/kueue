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

package v1alpha2

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
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
	// Name defines the name of the cluster manager.
	// It must be a valid Kubernetes label value.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=63
	// +kubebuilder:validation:XValidation:rule="!format.labelValue().validate(self).hasValue()",message="must be a valid Kubernetes label value"
	// +required
	Name string `json:"name"`
}

// ClusterProfileStatus defines the observed state of ClusterProfile.
type ClusterProfileStatus struct {
	// Conditions contains the different condition statuses for this cluster.
	// +optional
	// +listType=map
	// +listMapKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty"`

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
	// +listType=map
	// +listMapKey=name
	Properties []Property `json:"properties,omitempty"`

	// AccessProviders is a list of cluster access providers that can provide access
	// information for clusters.
	// +optional
	// +listType=map
	// +listMapKey=name
	// +kubebuilder:validation:MaxItems=64
	AccessProviders []AccessProvider `json:"accessProviders,omitempty"`
}

// AccessProvider describes one way a consumer can connect to the cluster.
type AccessProvider struct {
	// Name matches one of the consumer's configured access provider names.
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	// +required
	Name string `json:"name"`

	// Cluster contains the cluster connection details, such as the server
	// address and certificate authority data.
	// +required
	Cluster Cluster `json:"cluster"`
}

// Cluster contains the connection information a consumer uses to reach the
// Kubernetes API server.
type Cluster struct {
	// Server is the URL of the Kubernetes API server, for example,
	// "https://api.example.com:6443".
	// +kubebuilder:validation:MaxLength=2048
	// +kubebuilder:validation:XValidation:rule="isURL(self) && url(self).getHost() != ''",message="server must be a valid absolute URL with a host"
	// +required
	Server string `json:"server"`

	// TLSServerName is the server name used to verify the server certificate.
	// If empty, the hostname in Server is used.
	// +kubebuilder:validation:MaxLength=253
	// +optional
	TLSServerName string `json:"tls-server-name,omitempty"`

	// InsecureSkipTLSVerify disables server certificate verification.
	// Enabling this field makes HTTPS connections insecure.
	// +optional
	InsecureSkipTLSVerify bool `json:"insecure-skip-tls-verify,omitempty"`

	// CertificateAuthorityData contains PEM-encoded CA certificate bytes.
	// In YAML or JSON, these bytes must be base64-encoded.
	// +optional
	CertificateAuthorityData []byte `json:"certificate-authority-data,omitempty"`

	// ProxyURL is the proxy URL used for requests to the cluster. Supported
	// schemes are "http", "https", and "socks5". If empty, the ClusterProfile
	// does not specify a proxy.
	//
	// SOCKS5 proxying does not support SPDY streaming endpoints such as exec,
	// attach, and port-forward.
	// +kubebuilder:validation:MaxLength=2048
	// +kubebuilder:validation:XValidation:rule="self == '' || (isURL(self) && url(self).getScheme() in ['http', 'https', 'socks5'] && url(self).getHost() != '')",message="proxy-url must be a valid URL with scheme http, https, or socks5 and a host"
	// +optional
	ProxyURL string `json:"proxy-url,omitempty"`

	// DisableCompression disables response compression for requests to the
	// cluster. This can reduce CPU usage when network bandwidth is sufficient.
	// +optional
	DisableCompression bool `json:"disable-compression,omitempty"`

	// Extensions contains provider-specific cluster configuration. For example,
	// an extension named "client.authentication.k8s.io/exec" supplies
	// configuration to an exec credential plugin.
	// +optional
	// +listType=map
	// +listMapKey=name
	Extensions []NamedExtension `json:"extensions,omitempty"`
}

// NamedExtension associates a name with provider-specific configuration.
type NamedExtension struct {
	// Name identifies the extension, for example,
	// "client.authentication.k8s.io/exec".
	// +kubebuilder:validation:MinLength=1
	// +kubebuilder:validation:MaxLength=253
	// +required
	Name string `json:"name"`

	// Extension contains the provider-specific configuration.
	// +required
	Extension runtime.RawExtension `json:"extension"`
}

// ClusterVersion represents version information about the cluster.
type ClusterVersion struct {
	// Kubernetes is the kubernetes version of the cluster.
	// +optional
	Kubernetes string `json:"kubernetes,omitempty"`
}

// Property defines the data structure to represent a property of a cluster.
// It contains a name/value pair and the last observed time of the property on the cluster.
// This property can store various configurable details and metrics of a cluster,
// which may include information such as the entry point of the cluster, types of nodes, location,
// etc. according to KEP 4322.
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
	// The value is the timestamp when the property was observed not the time when the property
	// was updated in the cluster-profile.
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

	// LabelInventoryMemberIDKey correlates ClusterProfiles that represent the same member cluster.
	// When set, the value MUST be non-empty. The platform administrator SHOULD coordinate values
	// on a hub so that the same member cluster uses the same value and different member clusters
	// use different values. Cluster managers SHOULD keep the value unchanged while a ClusterProfile
	// continues to represent the same member cluster.
	LabelInventoryMemberIDKey = "multicluster.x-k8s.io/inventory-member-id"

	// LabelClusterSetKey is used on a namespace to indicate the clusterset that a ClusterProfile belongs to.
	// If a cluster inventory represents a ClusterSet,
	// all its ClusterProfile objects MUST be part of the same clusterSet
	// and namespace must be used as the grouping mechanism.
	// The namespace MUST have LabelClusterSet and the value as the name of the clusterSet.
	LabelClusterSetKey = "multicluster.x-k8s.io/clusterset"
)

//+genclient
//+kubebuilder:object:root=true
//+kubebuilder:subresource:status
//+kubebuilder:resource:scope=Namespaced,categories=multicluster
//+kubebuilder:printcolumn:name="Display Name",type=string,JSONPath=`.spec.displayName`
//+kubebuilder:printcolumn:name="Manager",type=string,JSONPath=`.spec.clusterManager.name`
//+kubebuilder:printcolumn:name="Version",type=string,JSONPath=`.status.version.kubernetes`
//+kubebuilder:printcolumn:name="Healthy",type=string,JSONPath=`.status.conditions[?(@.type=="ControlPlaneHealthy")].status`
//+kubebuilder:printcolumn:name="Age",type=date,JSONPath=`.metadata.creationTimestamp`

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
