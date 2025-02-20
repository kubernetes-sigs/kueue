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

import "k8s.io/apimachinery/pkg/api/resource"

// FairSharing contains the properties of the ClusterQueue or Cohort,
// when participating in FairSharing.
type FairSharing struct {
	// weight gives a comparative advantage to this ClusterQueue
	// or Cohort when competing for unused resources in the
	// Cohort.  The share is based on the dominant resource usage
	// above nominal quotas for each resource, divided by the
	// weight.  Admission prioritizes scheduling workloads from
	// ClusterQueues and Cohorts with the lowest share and
	// preempting workloads from the ClusterQueues and Cohorts
	// with the highest share.  A zero weight implies infinite
	// share value, meaning that this Node will always be at
	// disadvantage against other ClusterQueues and Cohorts.
	// +kubebuilder:default=1
	Weight *resource.Quantity `json:"weight,omitempty"`
}

type FairSharingStatus struct {
	// WeightedShare represent the maximum of the ratios of usage
	// above nominal quota to the lendable resources in the
	// Cohort, among all the resources provided by the Node, and
	// divided by the weight.  If zero, it means that the usage of
	// the Node is below the nominal quota.  If the Node has a
	// weight of zero, this will return 9223372036854775807, the
	// maximum possible share value.
	WeightedShare int64 `json:"weightedShare"`
}
