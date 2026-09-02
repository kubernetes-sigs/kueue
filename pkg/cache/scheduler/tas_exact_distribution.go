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
	"cmp"
	"fmt"
	"maps"
	"slices"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
	utiltas "sigs.k8s.io/kueue/pkg/util/tas"
)

// noDomain marks an entry that has not been matched to a domain.
const noDomain = -1

// matchExactSizes assigns every entry of an exact distribution to a distinct
// domain. It returns a slice where result[i] is the index of the domain holding
// sizes[i], preserving the caller's list order so that assignment construction
// can lay out pod ranks in that order.
//
// Each domain takes at most one entry, so this is a simpler problem than bin
// packing: nothing is ever split. Working from the largest entry down matters,
// because otherwise a small entry could take the only domain big enough for a
// large one.
//
// Matching runs twice on purpose. The first pass answers "does this fit?" using
// one fixed rule, so feasibility depends only on capacity. The second builds the
// assignment actually used, choosing among fitting domains according to the
// active placement mode. Both rules find a matching whenever one exists, so the
// second pass cannot fail where the first succeeded; keeping them apart means a
// future placement mode cannot quietly make workloads unschedulable that used to
// be admitted.
//
// capacities must already be in the caller's preferred domain order: ties are
// broken towards the earlier entry, which is what makes the result deterministic.
func matchExactSizes(sizes, capacities []int32, preferSmallest bool) ([]int, bool) {
	if len(sizes) == 0 || len(sizes) > len(capacities) {
		return nil, false
	}
	order := entriesLargestFirst(sizes)
	if _, ok := assignEntries(sizes, capacities, order, true); !ok {
		return nil, false
	}
	return assignEntries(sizes, capacities, order, preferSmallest)
}

// entriesLargestFirst returns entry indexes ordered by descending size, with
// equal sizes kept in list order.
func entriesLargestFirst(sizes []int32) []int {
	order := make([]int, len(sizes))
	for i := range order {
		order[i] = i
	}
	slices.SortStableFunc(order, func(a, b int) int {
		if c := cmp.Compare(sizes[b], sizes[a]); c != 0 {
			return c
		}
		return cmp.Compare(a, b)
	})
	return order
}

// assignEntries walks entries in the given order and gives each one an unused
// domain that fits. preferSmallest picks the tightest fitting domain, otherwise
// the roomiest.
func assignEntries(sizes, capacities []int32, order []int, preferSmallest bool) ([]int, bool) {
	result := make([]int, len(sizes))
	for i := range result {
		result[i] = noDomain
	}
	used := make([]bool, len(capacities))

	for _, entry := range order {
		want := sizes[entry]
		best := noDomain
		for d, capacity := range capacities {
			if used[d] || capacity < want {
				continue
			}
			switch {
			case best == noDomain:
				best = d
			case preferSmallest && capacity < capacities[best]:
				best = d
			case !preferSmallest && capacity > capacities[best]:
				best = d
			}
		}
		if best == noDomain {
			return nil, false
		}
		used[best] = true
		result[entry] = best
	}
	return result, true
}

// exactDistributionSizes returns the sizes list of an exact-distribution
// request, or nil if the request does not use one. Reads through the shared
// helper so Workloads persisted with the legacy slice fields are handled too.
func exactDistributionSizes(tr *kueue.PodSetTopologyRequest) []int32 {
	if !features.Enabled(features.TASExactTopologyDistribution) {
		return nil
	}
	constraints := utiltas.PodSetSliceRequiredTopologyConstraints(tr)
	if len(constraints) != 1 {
		return nil
	}
	return constraints[0].Sizes
}

// findExactDistributionDomains selects one distinct domain per entry of the
// sizes list, sets each one's assigned pod count, and records which entry each
// domain holds so that assignment construction can preserve rank-block order.
//
// Aggregate capacity does not prove that a distribution fits: domains with room
// for 2, 2 and 4 pods total eight slots but cannot hold [1, 3, 4], because
// nothing has room for three once each entry has its own domain. Feasibility is
// therefore decided per candidate enclosing domain rather than against a sum.
//
// It returns the leaves that make up the assignment, or a reason explaining why
// the distribution could not be placed.
func (s *TASFlavorSnapshot) findExactDistributionDomains(
	sizes []int32,
	state *findTopologyAssignmentState,
) ([]*domain, string) {
	exactLevelIdx := state.sliceLevelIdx
	levelDomains := slices.Collect(maps.Values(s.domainsPerLevel[exactLevelIdx]))

	reason := ""
	for _, scope := range s.exactCandidateScopes(state) {
		candidates := s.sortedDomains(domainsWithinScope(levelDomains, scope), state.unconstrained)
		if len(candidates) < len(sizes) {
			reason = fmt.Sprintf("topology exact distribution needs more domains: requested %d, eligible %d%s",
				len(sizes), len(candidates), describeScope(scope))
			continue
		}

		capacities := make([]int32, len(candidates))
		for i, d := range candidates {
			capacities[i] = s.domainStateOf(d).podCount
		}

		matched, ok := matchExactSizes(sizes, capacities, useLeastFreeCapacityAlgorithm(state.unconstrained))
		if !ok {
			reason = fmt.Sprintf("topology exact distribution infeasible: sizes %v do not fit the eligible domain capacities %v%s",
				sizes, capacities, describeScope(scope))
			continue
		}

		// Only commit once a scope has matched, so a rejected candidate leaves
		// no assigned counts behind for the next one to trip over.
		state.exactLevelIdx = exactLevelIdx
		state.exactGroupOrder = make(map[utiltas.TopologyDomainID]int, len(sizes))
		selected := make([]*domain, 0, len(sizes))
		for entry, domainIdx := range matched {
			d := candidates[domainIdx]
			s.domainStateOf(d).podCount = sizes[entry]
			state.exactGroupOrder[utiltas.DomainID(d.levelValues)] = entry
			selected = append(selected, d)
		}

		// Below the exact level Kueue uses its existing capacity-based
		// placement, so push each domain's assigned count down to the leaves.
		return s.expandExactGroupsToLeaves(selected, state), ""
	}
	return nil, reason
}

// exactCandidateScopes returns the enclosing domains to try, in TAS order. With
// podset-required-topology every domain at that level is a separate scope and
// the selected domains must all descend from one of them. Without it there is a
// single scope covering the whole flavor, so the selected domains need not share
// a parent.
func (s *TASFlavorSnapshot) exactCandidateScopes(state *findTopologyAssignmentState) []*domain {
	if !state.required || state.requestedLevelIdx >= state.sliceLevelIdx {
		return []*domain{nil}
	}
	enclosing := slices.Collect(maps.Values(s.domainsPerLevel[state.requestedLevelIdx]))
	return s.sortedDomains(enclosing, state.unconstrained)
}

// domainsWithinScope keeps the domains descended from scope. A nil scope means
// no containment was requested, so every domain is eligible.
func domainsWithinScope(domains []*domain, scope *domain) []*domain {
	if scope == nil {
		return domains
	}
	scopeID := utiltas.DomainID(scope.levelValues)
	within := make([]*domain, 0, len(domains))
	for _, d := range domains {
		if utiltas.DomainID(d.levelValues).BelongsTo(scopeID) {
			within = append(within, d)
		}
	}
	return within
}

func describeScope(scope *domain) string {
	if scope == nil {
		return ""
	}
	return fmt.Sprintf(" within %s", utiltas.DomainID(scope.levelValues))
}

// expandExactGroupsToLeaves walks each selected domain's assigned pod count
// down to the leaves using the existing placement, so that below the exact
// level nothing about this feature applies.
func (s *TASFlavorSnapshot) expandExactGroupsToLeaves(
	selected []*domain,
	state *findTopologyAssignmentState,
) []*domain {
	current := selected
	for levelIdx := state.exactLevelIdx; levelIdx < len(s.domainsPerLevel)-1; levelIdx++ {
		next := make([]*domain, 0, len(current))
		for _, d := range current {
			assigned := s.domainStateOf(d).podCount
			if assigned == 0 {
				continue
			}
			children := s.sortedDomains(d.children, state.unconstrained)
			next = append(next, s.updateCountsToMinimumGeneric(children, assigned, 0, 1, state.unconstrained, false)...)
		}
		current = next
	}
	return current
}
