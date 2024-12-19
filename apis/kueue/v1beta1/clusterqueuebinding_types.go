package v1beta1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type Subject struct {
	// +kubebuilder:validation:Enum={Group,User,ServiceAccount}
	Kind      string `json:"kind,omitempty"`
	Name      string `json:"name,omitempty"`
	Namespace string `json:"namespace,omitempty"`
}

type ClusterQueueBindingSpec struct {
	// clusterQueue is a reference to a clusterQueue that backs this localQueue.
	ClusterQueue ClusterQueueReference `json:"clusterQueue,omitempty"`

	// subjects is a list of references to the objects that are allowed to access the localQueue.
	Subjects []Subject `json:"subjects,omitempty"`
}

// ClusterQueueBindingStatus defines the observed state of ClusterQueueBinding
type ClusterQueueBindingStatus struct{}

// +kubebuilder:object:root=true
// +kubebuilder:subresource:status

// ClusterQueueBinding is the Schema for the clusterqueuebindings API
type ClusterQueueBinding struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   ClusterQueueBindingSpec   `json:"spec,omitempty"`
	Status ClusterQueueBindingStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// ClusterQueueBindingList contains a list of ClusterQueueBinding
type ClusterQueueBindingList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []ClusterQueueBinding `json:"items"`
}

func init() {
	SchemeBuilder.Register(&ClusterQueueBinding{}, &ClusterQueueBindingList{})
}
