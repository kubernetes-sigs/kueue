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
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/sets"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
	utiltas "sigs.k8s.io/kueue/pkg/util/tas"
	"sigs.k8s.io/kueue/pkg/workload"
)

// PodSetGroupNameToTreeCount stores topology spread counts for one flavor by
// PodSet group. An entry present with an empty ByDomain means spreading
// applies to the group but nothing is admitted yet - distinct from an absent
// entry, which means spreading does not apply at all.
type PodSetGroupNameToTreeCount map[utiltas.PodSetGroupKey]*SpreadTreeCount

// SpreadTreeCount is how many Workloads matching a PodSet group's spreading
// selector already occupy each topology domain of one flavor.
type SpreadTreeCount struct {
	// Total is the number of matching Workloads, each counted once. It is not
	// the sum of ByDomain: a Workload occupies one domain per rule level, so
	// it contributes to several entries there but only one here. Total is the
	// denominator for a rule at the topology's root level.
	Total int32

	// ByDomain is the number of matching Workloads occupying each domain.
	ByDomain map[utiltas.TopologyDomainID]int32
}

// topologySpreadCountsForFlavor counts, for a single flavor, how many
// Workloads matching each spreading PodSet group's selector already occupy the
// flavor's domains. Returns nil when spreading applies to none of the PodSets
// requesting the flavor.
func (c *ClusterQueueSnapshot) topologySpreadCountsForFlavor(
	wl *workload.Info,
	flavor kueue.ResourceFlavorReference,
	flavorRequests FlavorTASRequests,
) PodSetGroupNameToTreeCount {
	// Gated in the callee rather than at the call site, so every current and
	// future caller is covered and the scan below never runs while the feature
	// is off.
	if !features.Enabled(features.TASTopologySpreading) {
		return nil
	}
	if wl == nil || len(wl.TopologySpreading) == 0 {
		return nil
	}
	tasFlavor := c.TASFlavors[flavor]
	if tasFlavor == nil {
		return nil
	}

	groupCounts := make(PodSetGroupNameToTreeCount)
	for i := range flavorRequests {
		groupKey := utiltas.GroupKeyForPodSet(flavorRequests[i].PodSet)
		if wl.TopologySpreading[groupKey] != nil {
			groupCounts[groupKey] = &SpreadTreeCount{ByDomain: make(map[utiltas.TopologyDomainID]int32)}
		}
	}
	if len(groupCounts) == 0 {
		return nil
	}

	// A Workload being re-placed - an elastic scale-up, or a re-nomination
	// after its first pass - is already in the snapshot and matches its own
	// selector, so counting it would let it exhaust the share of the very
	// domain it is running in and ban itself from staying there.
	selfKey := workload.Key(wl.Obj)

	for groupKey, counts := range groupCounts {
		spec := wl.TopologySpreading[groupKey]
		selector := spec.Selector()
		for key, existing := range c.Workloads {
			if key == selfKey {
				continue
			}
			if existing.Obj.Namespace != wl.Obj.Namespace || !selector.Matches(labels.Set(existing.Obj.Labels)) {
				continue
			}

			occupied := tasFlavor.occupiedDomainsForGroup(existing, flavor, groupKey, spec.Rules)
			if occupied.Len() == 0 {
				continue
			}
			counts.Total++
			for domainID := range occupied {
				counts.ByDomain[domainID]++
			}
		}
	}
	return groupCounts
}

func (s *TASFlavorSnapshot) occupiedDomainsForGroup(
	wl *workload.Info,
	flavor kueue.ResourceFlavorReference,
	groupKey utiltas.PodSetGroupKey,
	rules []utiltas.SpreadingRule,
) sets.Set[utiltas.TopologyDomainID] {
	occupied := sets.New[utiltas.TopologyDomainID]()
	for i := range wl.Obj.Spec.PodSets {
		ps := &wl.Obj.Spec.PodSets[i]
		if utiltas.GroupKeyForPodSet(ps) != groupKey {
			continue
		}
		psa := findPSA(wl.Obj, ps.Name)
		if psa == nil || psa.TopologyAssignment == nil || !podSetAssignmentUsesFlavor(psa, flavor) {
			continue
		}

		for domain := range utiltas.InternalSeqFrom(psa.TopologyAssignment) {
			// Resolved through the tree's own index rather than by rebuilding a
			// level-values path: a published assignment names the declared
			// levels, the hostname level, or the whole tree depending on the
			// Topology, and only the tree knows a domain's ID at each level -
			// a leaf's is the hostname or node name alone, not its full path.
			assignedDomain := s.domainForAssignmentValues(psa.TopologyAssignment.Levels, domain.Values)
			if assignedDomain == nil {
				continue
			}
			for _, rule := range rules {
				levelIdx, found := s.resolveLevelIdx(rule.TopologyKey)
				if !found {
					continue
				}
				ruleDomain := ancestorAtLevel(assignedDomain, levelIdx)
				if ruleDomain == nil {
					// The Workload is placed above this rule's level, so it
					// spans every domain there and pins none of them.
					continue
				}
				occupied.Insert(ruleDomain.id)
				// Also record the parent domain: evaluateSpreadRule needs its
				// occupancy as the denominator for this rule's threshold check.
				if parent := ruleDomain.parent; parent != nil {
					occupied.Insert(parent.id)
				}
			}
		}
	}
	return occupied
}

func podSetAssignmentUsesFlavor(psa *kueue.PodSetAssignment, flavor kueue.ResourceFlavorReference) bool {
	for _, assignedFlavor := range psa.Flavors {
		if assignedFlavor == flavor {
			return true
		}
	}
	return false
}
