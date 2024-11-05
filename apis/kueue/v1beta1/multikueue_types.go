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

package v1beta1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

const (
	MultiKueueConfigSecretKey = "kubeconfig"
	MultiKueueClusterActive   = "Active"

	// MultiKueueOriginLabel is a label used to track the creator
	// of multikueue remote objects.
	MultiKueueOriginLabel = "kueue.x-k8s.io/multikueue-origin"

	// MultiKueueControllerName is the name used by the MultiKueue
	// admission check controller.
	MultiKueueControllerName = "kueue.x-k8s.io/multikueue"
)

type LocationType string

const (
	// PathLocationType is the path on the disk of kueue-controller-manager.
	PathLocationType LocationType = "Path"

	// SecretLocationType is the name of the secret inside the namespace in which the kueue controller
	// manager is running. The config should be stored in the "kubeconfig" key.
	SecretLocationType LocationType = "Secret"
)

type KubeConfig struct {
	// Location of the KubeConfig.
	//
	// If LocationType is Secret then Location is the name of the secret inside the namespace in
	// which the kueue controller manager is running. The config should be stored in the "kubeconfig" key.
	Location string `json:"location"`

	// Type of the KubeConfig location.
	//
	// +kubebuilder:default=Secret
	// +kubebuilder:validation:Enum=Secret;Path
	LocationType LocationType `json:"locationType"`
}

type MultiKueueClusterSpec struct {
	// Information how to connect to the cluster.
	KubeConfig KubeConfig `json:"kubeConfig"`
}

type MultiKueueClusterStatus struct {
	// +optional
	// +listType=map
	// +listMapKey=type
	// +patchStrategy=merge
	// +patchMergeKey=type
	Conditions []metav1.Condition `json:"conditions,omitempty" patchStrategy:"merge" patchMergeKey:"type"`
}

// +genclient
// +genclient:nonNamespaced
// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:subresource:status
// +kubebuilder:resource:scope=Cluster

// MultiKueueCluster is the Schema for the multikueue API
type MultiKueueCluster struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   MultiKueueClusterSpec   `json:"spec,omitempty"`
	Status MultiKueueClusterStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// MultiKueueClusterList contains a list of MultiKueueCluster
type MultiKueueClusterList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []MultiKueueCluster `json:"items"`
}

// MultiKueueConfigSpec defines the desired state of MultiKueueConfig
type MultiKueueConfigSpec struct {
	// List of MultiKueueClusters names where the workloads from the ClusterQueue should be distributed.
	//
	// +listType=set
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=10
	Clusters []string `json:"clusters"`
}

// +genclient
// +genclient:nonNamespaced
// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:resource:scope=Cluster

// MultiKueueConfig is the Schema for the multikueue API
type MultiKueueConfig struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec MultiKueueConfigSpec `json:"spec,omitempty"`
}

// +kubebuilder:object:root=true

// MultiKueueConfigList contains a list of MultiKueueConfig
type MultiKueueConfigList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []MultiKueueConfig `json:"items"`
}

func init() {
	SchemeBuilder.Register(&MultiKueueConfig{}, &MultiKueueConfigList{}, &MultiKueueCluster{}, &MultiKueueClusterList{})
}
