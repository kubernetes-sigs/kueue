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
	"slices"

	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/sets"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	utiltas "sigs.k8s.io/kueue/pkg/util/tas"
	"sigs.k8s.io/kueue/pkg/workload"
)

// FlavorToSpreadTreeCount stores the number of matching Workloads by flavor,
// PodSet group, and domain. The empty domain ID stores the total count for the
// flavor and group.
type FlavorToSpreadTreeCount map[kueue.ResourceFlavorReference]PodSetGroupNameToTreeCount

// PodSetGroupNameToTreeCount stores topology spread counts for one flavor by
// PodSet group and domain.
type PodSetGroupNameToTreeCount map[utiltas.PodSetGroupKey]map[utiltas.TopologyDomainID]int32

func (c *ClusterQueueSnapshot) topologySpreadCounts(wl *workload.Info, requests WorkloadTASRequests) FlavorToSpreadTreeCount {
	if wl == nil || len(wl.TopologySpreading) == 0 {
		return nil
	}

	// A Workload being re-placed - an elastic scale-up, or a re-nomination
	// after its first pass - is already in the snapshot and matches its own
	// selector, so counting it would let it exhaust the share of the very
	// domain it is running in and ban itself from staying there.
	selfKey := workload.Key(wl.Obj)

	result := make(FlavorToSpreadTreeCount)
	for flavor, flavorRequests := range requests {
		tasFlavor := c.TASFlavors[flavor]
		if tasFlavor == nil {
			continue
		}

		groupCounts := make(PodSetGroupNameToTreeCount)
		for i := range flavorRequests {
			groupKey := utiltas.GroupKeyForPodSet(flavorRequests[i].PodSet)
			if wl.TopologySpreading[groupKey] != nil {
				groupCounts[groupKey] = make(map[utiltas.TopologyDomainID]int32)
			}
		}
		if len(groupCounts) == 0 {
			continue
		}

		for groupKey, counts := range groupCounts {
			spec := wl.TopologySpreading[groupKey]
			for key, existing := range c.Workloads {
				if key == selfKey {
					continue
				}
				if existing.Obj.Namespace != wl.Obj.Namespace || !spec.WorkloadLabelSelector.Matches(labels.Set(existing.Obj.Labels)) {
					continue
				}

				occupied := tasFlavor.occupiedDomainsForGroup(existing, flavor, groupKey, spec.Rules)
				if occupied.Len() == 0 {
					continue
				}
				counts[""]++
				for domainID := range occupied {
					counts[domainID]++
				}
			}
		}
		result[flavor] = groupCounts
	}
	if len(result) == 0 {
		return nil
	}
	return result
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
			fullValues, found := s.fullTopologyValues(psa.TopologyAssignment.Levels, domain.Values)
			if !found {
				continue
			}
			for _, rule := range rules {
				levelIdx := slices.Index(s.levelKeys, rule.Key)
				if levelIdx < 0 {
					continue
				}
				occupied.Insert(utiltas.DomainID(fullValues[:levelIdx+1]))
				// Also record occupancy of the parent domain (one level up):
				// this is the same "does this Workload's group touch this
				// domain's subtree" count, just truncated one level
				// shallower, so it doubles as the parent-scoped denominator
				// evaluateSpreadRule uses for rule's threshold check.
				if levelIdx > 0 {
					occupied.Insert(utiltas.DomainID(fullValues[:levelIdx]))
				}
			}
		}
	}
	return occupied
}

func (s *TASFlavorSnapshot) fullTopologyValues(assignmentLevels, values []string) ([]string, bool) {
	if slices.Equal(assignmentLevels, s.levelKeys) && len(values) == len(s.levelKeys) {
		return values, true
	}
	if s.isLowestLevelNode && len(assignmentLevels) == 1 && assignmentLevels[0] == s.lowestLevel() && len(values) == 1 {
		leaf, found := s.leaves[utiltas.DomainID(values)]
		if found {
			return leaf.levelValues, true
		}
	}
	return nil, false
}

func podSetAssignmentUsesFlavor(psa *kueue.PodSetAssignment, flavor kueue.ResourceFlavorReference) bool {
	for _, assignedFlavor := range psa.Flavors {
		if assignedFlavor == flavor {
			return true
		}
	}
	return false
}
