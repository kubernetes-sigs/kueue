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

// +genclient
// +genclient:nonNamespaced
// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:resource:scope=Cluster,shortName={flavor,flavors,rf}

// ResourceFlavor is the Schema for the resourceflavors API.
type ResourceFlavor struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec ResourceFlavorSpec `json:"spec,omitempty"`
}

// TopologyReference is the name of the Topology.
// +kubebuilder:validation:MaxLength=253
// +kubebuilder:validation:Pattern="^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$"
type TopologyReference string

// ResourceFlavorSpec defines the desired state of the ResourceFlavor
// +kubebuilder:validation:XValidation:rule="!has(self.topologyName) || self.nodeLabels.size() >= 1", message="at least one nodeLabel is required when topology is set"
type ResourceFlavorSpec struct {
	// nodeLabels are labels that associate the ResourceFlavor with Nodes that
	// have the same labels.
	// When a Workload is admitted, its podsets can only get assigned
	// ResourceFlavors whose nodeLabels match the nodeSelector and nodeAffinity
	// fields.
	// Once a ResourceFlavor is assigned to a podSet, the ResourceFlavor's
	// nodeLabels should be injected into the pods of the Workload by the
	// controller that integrates with the Workload object.
	//
	// nodeLabels can be up to 8 elements.
	// +optional
	// +mapType=atomic
	// +kubebuilder:validation:MaxProperties=8
	NodeLabels map[string]string `json:"nodeLabels,omitempty"`

	// nodeTaints are taints that the nodes associated with this ResourceFlavor
	// have.
	// Workloads' podsets must have tolerations for these nodeTaints in order to
	// get assigned this ResourceFlavor during admission.
	//
	// An example of a nodeTaint is
	// cloud.provider.com/preemptible="true":NoSchedule
	//
	// nodeTaints can be up to 8 elements.
	//
	// +optional
	// +listType=atomic
	// +kubebuilder:validation:MaxItems=8
	// +kubebuilder:validation:XValidation:rule="self.all(x, x.effect in ['NoSchedule', 'PreferNoSchedule', 'NoExecute'])", message="supported taint effect values: 'NoSchedule', 'PreferNoSchedule', 'NoExecute'"
	NodeTaints []corev1.Taint `json:"nodeTaints,omitempty"`

	// tolerations are extra tolerations that will be added to the pods admitted in
	// the quota associated with this resource flavor.
	//
	// An example of a toleration is
	// cloud.provider.com/preemptible="true":NoSchedule
	//
	// tolerations can be up to 8 elements.
	//
	// +optional
	// +listType=atomic
	// +kubebuilder:validation:MaxItems=8
	// +kubebuilder:validation:XValidation:rule="self.all(x, !has(x.key) ? x.operator == 'Exists' : true)", message="operator must be Exists when 'key' is empty, which means 'match all values and all keys'"
	// +kubebuilder:validation:XValidation:rule="self.all(x, has(x.tolerationSeconds) ? x.effect == 'NoExecute' : true)", message="effect must be 'NoExecute' when 'tolerationSeconds' is set"
	// +kubebuilder:validation:XValidation:rule="self.all(x, !has(x.operator) || x.operator in ['Equal', 'Exists'])", message="supported toleration values: 'Equal'(default), 'Exists'"
	// +kubebuilder:validation:XValidation:rule="self.all(x, has(x.operator) && x.operator == 'Exists' ? !has(x.value) : true)", message="a value must be empty when 'operator' is 'Exists'"
	// +kubebuilder:validation:XValidation:rule="self.all(x, !has(x.effect) || x.effect in ['NoSchedule', 'PreferNoSchedule', 'NoExecute'])", message="supported taint effect values: 'NoSchedule', 'PreferNoSchedule', 'NoExecute'"
	Tolerations []corev1.Toleration `json:"tolerations,omitempty"`

	// topologyName indicates topology for the TAS ResourceFlavor.
	// When specified, it enables scraping of the topology information from the
	// nodes matching to the Resource Flavor node labels.
	//
	// +optional
	TopologyName *TopologyReference `json:"topologyName,omitempty"`
}

// +kubebuilder:object:root=true

// ResourceFlavorList contains a list of ResourceFlavor
type ResourceFlavorList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ResourceFlavor `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ResourceFlavor{}, &ResourceFlavorList{})
}
