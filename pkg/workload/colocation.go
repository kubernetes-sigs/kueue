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

package workload

import (
	"slices"

	"k8s.io/apimachinery/pkg/types"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	utiltas "sigs.k8s.io/kueue/pkg/util/tas"
)

// ColocationPlan is internal scheduling state, never persisted to the API.
type ColocationPlan struct {
	Hash       EquivalenceHash
	Queue      kueue.ClusterQueueReference
	Flavor     kueue.ResourceFlavorReference
	Assignment *utiltas.TopologyAssignment
	Victims    map[Reference]ColocationVictim
	Cancelled  bool
}

type ColocationVictim struct {
	UID    types.UID
	Reason string
}

// Matches compares domain counts without depending on assignment output order.
func (p *ColocationPlan) Matches(ta *utiltas.TopologyAssignment) bool {
	if ta == nil || p.Assignment == nil || !slices.Equal(ta.Levels, p.Assignment.Levels) || len(ta.Domains) != len(p.Assignment.Domains) {
		return false
	}
	for _, ad := range ta.Domains {
		if !slices.ContainsFunc(p.Assignment.Domains, func(bd utiltas.TopologyDomainAssignment) bool {
			return ad.Count == bd.Count && slices.Equal(ad.Values, bd.Values)
		}) {
			return false
		}
	}
	return true
}
