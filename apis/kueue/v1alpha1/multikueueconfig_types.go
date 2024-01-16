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
)

const (
	MultiKueueConfigSecretKey = "kubeconfig"
)

// MultiKueueConfigSpec defines the desired state of MultiKueueConfig
type MultiKueueConfigSpec struct {
	// clusters contains the list of configurations for using worker clusters.
	//
	// +listType=map
	// +listMapKey=name
	// +kubebuilder:validation:MinItems=1
	// +kubebuilder:validation:MaxItems=100
	Clusters []MultiKueueCluster `json:"clusters"`
}

type MultiKueueCluster struct {
	Name          string        `json:"name"`
	KubeconfigRef KubeconfigRef `json:"kubeconfigRef"`
}

type LocationType string

const (
	// Location is the name of the secret inside the namespace in which the kueue controller
	// manager is running. The config should be stored in the "kubeconfig" key.
	SecretLocationType LocationType = "Secret"
)

type KubeconfigRef struct {
	// Name of the cluster inside the given KubeConfig.
	Name string `json:"name"`

	// Location of the KubeConfig.
	Location string `json:"location"`

	// Type of the KubeConfig location.
	//
	// +kubebuilder:default=Secret
	// +kubebuilder:validation:Enum=Secret
	LocationType LocationType `json:"locationType"`
}

//+genclient
//+genclient:nonNamespaced
//+kubebuilder:object:root=true
//+kubebuilder:storageversion
//+kubebuilder:resource:scope=Cluster

// MultiKueueConfig is the Schema for the provisioningrequestconfig API
type MultiKueueConfig struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec MultiKueueConfigSpec `json:"spec,omitempty"`
}

//+kubebuilder:object:root=true

// MultiKueueConfigList contains a list of MultiKueueConfig
type MultiKueueConfigList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []MultiKueueConfig `json:"items"`
}

func init() {
	SchemeBuilder.Register(&MultiKueueConfig{}, &MultiKueueConfigList{})
}
