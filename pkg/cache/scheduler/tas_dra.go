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

package scheduler

import (
	corev1 "k8s.io/api/core/v1"

	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/resources"
	utiltas "sigs.k8s.io/kueue/pkg/util/tas"
)

// DRADelegation records extended resources left to the per-node device check rather than
// counted against a domain's capacity, and the request as it stood before that. A device
// plugin can publish the same name, so kube-scheduler decides this per node too.
type DRADelegation struct {
	// Resources are the extended resources zeroed out of the request because a
	// DeviceClass can supply them.
	Resources []corev1.ResourceName
	// Undelegated is the request with Resources kept, for nodes that publish them.
	Undelegated resources.Requests
}

// podRequests is what one Pod asks for, with the delegation that lets the count vary by
// node. The workers and the leader each have their own.
type podRequests struct {
	requests resources.Requests
	// delegation is nil when the Pod asks for no DRA-backed extended resource.
	delegation *DRADelegation
}

// forLeaf is the request to count against leaf. A leaf publishing the delegated resources
// supplies them itself, so they are counted rather than left to the device check. Domains
// above the leaf have no node to ask and keep the delegated request.
func (p podRequests) forLeaf(leaf *leafDomain) resources.Requests {
	if p.delegation == nil || !leaf.advertisesAll(p.delegation.Resources) {
		return p.requests
	}
	return p.delegation.Undelegated
}

// newPodRequests is what a single Pod of the PodSet asks for, its own Pod count included.
func newPodRequests(podSetRequests TASPodSetRequests) podRequests {
	requests := podSetRequests.SinglePodRequests.Clone()
	requests.Add(resources.OnePodRequest)
	p := podRequests{requests: requests}

	delegation := podSetRequests.DRADelegation
	if !features.Enabled(features.KueueDRADeviceFeasibility) || delegation == nil {
		return p
	}
	undelegated := delegation.Undelegated.Clone()
	undelegated.Add(resources.OnePodRequest)
	p.delegation = &DRADelegation{Resources: delegation.Resources, Undelegated: undelegated}
	return p
}

// DomainAdvertises reports whether the domain's nodes publish every one of the resources.
// A domain above the leaf level answers false, leaving them to the device check.
func (s *TASFlavorSnapshot) DomainAdvertises(domainID utiltas.TopologyDomainID, names []corev1.ResourceName) bool {
	leaf := s.leaves[domainID]
	return leaf != nil && leaf.advertisesAll(names)
}
