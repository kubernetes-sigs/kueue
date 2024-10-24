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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kueuebeta "sigs.k8s.io/kueue/apis/kueue/v1beta1"
)

// CohortSpec defines the desired state of Cohort
type CohortSpec struct {
	// Parent references the name of the Cohort's parent, if
	// any. It satisfies one of three cases:
	// 1) Unset. This Cohort is the root of its Cohort tree.
	// 2) References a non-existent Cohort. We use default Cohort (no borrowing/lending limits).
	// 3) References an existent Cohort.
	//
	// If a cycle is created, we disable all members of the
	// Cohort, including ClusterQueues, until the cycle is
	// removed.  We prevent further admission while the cycle
	// exists.
	//
	//+kubebuilder:validation:MaxLength=253
	//+kubebuilder:validation:Pattern="^[a-z0-9]([-a-z0-9]*[a-z0-9])?(\\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*$"
	//
	Parent string `json:"parent,omitempty"`

	// ResourceGroups describes groupings of Resources and
	// Flavors.  Each ResourceGroup defines a list of Resources
	// and a list of Flavors which provide quotas for these
	// Resources. Each Resource and each Flavor may only form part
	// of one ResourceGroup.  There may be up to 16 ResourceGroups
	// within a Cohort.
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
	ResourceGroups []kueuebeta.ResourceGroup `json:"resourceGroups,omitempty"`
}

//+kubebuilder:object:root=true
//+kubebuilder:resource:scope=Cluster

// Cohort is the Schema for the cohorts API. Using Hierarchical
// Cohorts (any Cohort which has a parent) with Fair Sharing
// results in undefined behavior in 0.9
type Cohort struct {
	metav1.TypeMeta   `json:",inline"`
	metav1.ObjectMeta `json:"metadata,omitempty"`

	Spec CohortSpec `json:"spec,omitempty"`
}

//+kubebuilder:object:root=true

// CohortList contains a list of Cohort
type CohortList struct {
	metav1.TypeMeta `json:",inline"`
	metav1.ListMeta `json:"metadata,omitempty"`
	Items           []Cohort `json:"items"`
}

func init() {
	SchemeBuilder.Register(&Cohort{}, &CohortList{})
}
