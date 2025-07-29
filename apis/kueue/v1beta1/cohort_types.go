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

package v1beta1

import (
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

// CohortSpec defines the desired state of Cohort
type CohortSpec struct {
	// ParentName references the name of the Cohort's parent, if
	// any. It satisfies one of three cases:
	// 1) Unset. This Cohort is the root of its Cohort tree.
	// 2) References a non-existent Cohort. We use default Cohort (no borrowing/lending limits).
	// 3) References an existent Cohort.
	//
	// If a cycle is created, we disable all members of the
	// Cohort, including ClusterQueues, until the cycle is
	// removed.  We prevent further admission while the cycle
	// exists.
	ParentName CohortReference `json:"parentName,omitempty"`

	// ResourceGroups describes groupings of Resources and
	// Flavors.  Each ResourceGroup defines a list of Resources
	// and a list of Flavors which provide quotas for these
	// Resources. Each Resource and each Flavor may only form part
	// of one ResourceGroup.  There may be up to 16 ResourceGroups
	// within a Cohort.
	//
	// Please note that nominalQuota defined at the Cohort level
	// represents additional resources on top of those defined by
	// ClusterQueues within the Cohort. The Cohort's nominalQuota
	// may be thought of as a shared pool for the ClusterQueues
	// within it. Additionally, this quota may also be lent out to
	// parent Cohort(s), subject to LendingLimit.
	//
	// BorrowingLimit limits how much members of this Cohort
	// subtree can borrow from the parent subtree.
	//
	// LendingLimit limits how much members of this Cohort subtree
	// can lend to the parent subtree.
	//
	// Borrowing and Lending limits must only be set when the
	// Cohort has a parent.  Otherwise, the Cohort create/update
	// will be rejected by the webhook.
	//
	//+listType=atomic
	//+kubebuilder:validation:MaxItems=16
	ResourceGroups []ResourceGroup `json:"resourceGroups,omitempty"`

	// fairSharing defines the properties of the Cohort when
	// participating in FairSharing. The values are only relevant
	// if FairSharing is enabled in the Kueue configuration.
	// +optional
	FairSharing *FairSharing `json:"fairSharing,omitempty"`
}

// CohortStatus defines the observed state of Cohort.
type CohortStatus struct {
	// fairSharing contains the current state for this Cohort
	// when participating in Fair Sharing.
	// The is recorded only when Fair Sharing is enabled in the Kueue configuration.
	// +optional
	FairSharing *FairSharingStatus `json:"fairSharing,omitempty"`
}

// +genclient
// +kubebuilder:object:root=true
// +kubebuilder:storageversion
// +kubebuilder:resource:scope=Cluster
// +kubebuilder:subresource:status

// Cohort defines the Cohorts API.
//
// Hierarchical Cohorts (any Cohort which has a parent) are compatible
// with Fair Sharing as of v0.11. Using these features together in
// V0.9 and V0.10 is unsupported, and results in undefined behavior.
type Cohort struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec   CohortSpec   `json:"spec,omitempty"`
	Status CohortStatus `json:"status,omitempty"`
}

// +kubebuilder:object:root=true

// CohortList contains a list of Cohort
type CohortList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Cohort `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Cohort{}, &CohortList{})
}
