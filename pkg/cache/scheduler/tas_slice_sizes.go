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

// placeChunks puts every chunk listed in sizes into one of the candidate
// domains and returns how many pods each domain ends up with.
//
// A chunk is never split, which is the same contract the scalar slice size
// already provides. Chunks may share a domain, so a domain's remaining capacity
// is reduced by each chunk it takes rather than being used up by the first one.
//
// Chunks are walked largest first. Placing a chunk of one before a chunk of
// four can leave the four with nowhere to go even though a valid packing
// existed. Largest-first does not make the result optimal -- this is bin
// packing, and a greedy pass can still miss a valid arrangement -- but it keeps
// the cost proportional to chunk count times domain count.
//
// capacities must already be in the caller's preferred domain order, so that
// the active placement mode decides which fitting domain is tried first.
//
// On failure it returns the size of the chunk that could not be placed.
func placeChunks(sizes, capacities []int32) (assigned []int32, unplaced int32, ok bool) {
	if len(sizes) == 0 {
		return nil, 0, false
	}
	remaining := slices.Clone(capacities)
	assigned = make([]int32, len(capacities))
	for _, chunk := range chunksLargestFirst(sizes) {
		idx := slices.IndexFunc(remaining, func(free int32) bool { return free >= chunk })
		if idx < 0 {
			return nil, chunk, false
		}
		remaining[idx] -= chunk
		assigned[idx] += chunk
	}
	return assigned, 0, true
}

// chunksLargestFirst returns the chunk sizes in descending order.
func chunksLargestFirst(sizes []int32) []int32 {
	ordered := slices.Clone(sizes)
	slices.SortFunc(ordered, func(a, b int32) int { return cmp.Compare(b, a) })
	return ordered
}

// outermostSliceSizes returns the chunk list of the first constraint layer, or
// nil when that layer uses the scalar size. Reads through the shared helper so
// Workloads persisted with the legacy slice fields are handled too.
func outermostSliceSizes(tr *kueue.PodSetTopologyRequest) []int32 {
	if !features.Enabled(features.TASExactTopologyDistribution) {
		return nil
	}
	constraints := utiltas.PodSetSliceRequiredTopologyConstraints(tr)
	if len(constraints) == 0 {
		return nil
	}
	return constraints[0].Sizes
}

// findSliceSizesDomains places the chunk list at the level of the outermost
// constraint layer and expands the result down to the leaves.
//
// Aggregate capacity does not prove that a chunk list fits: domains with room
// for two, two and four pods total eight slots but cannot hold [3, 3, 2],
// because no domain has room for a second three. Feasibility is therefore
// decided by actually placing the chunks rather than by comparing sums.
//
// It returns the leaves that make up the assignment, or a reason explaining why
// the chunks could not be placed.
func (s *TASFlavorSnapshot) findSliceSizesDomains(
	sizes []int32,
	state *findTopologyAssignmentState,
) ([]*domain, string) {
	sizesLevelIdx := state.sliceLevelIdx
	levelDomains := slices.Collect(maps.Values(s.domainsPerLevel[sizesLevelIdx]))

	reason := ""
	for _, scope := range s.sliceSizesCandidateScopes(state) {
		candidates := s.sortedDomains(domainsWithinScope(levelDomains, scope), state.unconstrained)
		if len(candidates) == 0 {
			reason = fmt.Sprintf("topology slice sizes have no eligible domains at level %s%s",
				s.levelKeys[sizesLevelIdx], describeScope(scope))
			continue
		}

		capacities := make([]int32, len(candidates))
		for i, d := range candidates {
			capacities[i] = s.domainStateOf(d).podCount
		}

		assigned, unplaced, ok := placeChunks(sizes, capacities)
		if !ok {
			reason = sliceSizesNotFitReason(sizes, capacities, unplaced, scope)
			continue
		}

		// Only commit once a scope has placed every chunk, so a rejected
		// candidate leaves no assigned counts behind for the next one to trip
		// over.
		selected := make([]*domain, 0, len(candidates))
		for i, d := range candidates {
			if assigned[i] == 0 {
				continue
			}
			s.domainStateOf(d).podCount = assigned[i]
			selected = append(selected, d)
		}

		// Below this level Kueue uses its existing capacity-based placement, so
		// push each domain's assigned count down to the leaves.
		return s.expandDomainsToLeaves(selected, sizesLevelIdx, state), ""
	}
	return nil, reason
}

// sliceSizesNotFitReason explains which chunk could not be placed and how much
// room was actually available, so a user can tell an under-provisioned cluster
// from a fragmented one.
func sliceSizesNotFitReason(sizes, capacities []int32, unplaced int32, scope *domain) string {
	largestFree := int32(0)
	if len(capacities) > 0 {
		largestFree = slices.Max(capacities)
	}
	return fmt.Sprintf(
		"topology slice sizes do not fit: chunk of %d pods could not be placed, largest free domain holds %d pods (sizes %v, free capacity %v)%s",
		unplaced, largestFree, sizes, capacities, describeScope(scope))
}

// sliceSizesCandidateScopes returns the enclosing domains to try, in TAS order.
// With podset-required-topology every domain at that level is a separate scope
// and the chunks must all be placed below one of them. Without it there is a
// single scope covering the whole flavor.
func (s *TASFlavorSnapshot) sliceSizesCandidateScopes(state *findTopologyAssignmentState) []*domain {
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

// expandDomainsToLeaves walks each selected domain's assigned pod count down to
// the leaves using the existing placement, so that below the chunk level
// nothing about this feature applies.
func (s *TASFlavorSnapshot) expandDomainsToLeaves(
	selected []*domain,
	fromLevelIdx int,
	state *findTopologyAssignmentState,
) []*domain {
	current := selected
	for levelIdx := fromLevelIdx; levelIdx < len(s.domainsPerLevel)-1; levelIdx++ {
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

// placeChunksInChildren spreads one parent domain's chunks over that domain's
// children. It is the inner-layer counterpart of findSliceSizesDomains: the
// layer above has already decided how many pods this parent holds, and the
// chunk list says how those pods are cut up below it.
//
// The layer above may have put more than one of its own chunks in this parent,
// so the list is repeated to cover every pod the parent holds.
func (s *TASFlavorSnapshot) placeChunksInChildren(
	parent *domain,
	sizes []int32,
	state *findTopologyAssignmentState,
) ([]*domain, string) {
	parentPods := s.domainStateOf(parent).podCount
	chunks, reason := repeatChunksToCover(sizes, parentPods)
	if len(reason) > 0 {
		return nil, reason
	}

	children := s.sortedDomains(parent.children, state.unconstrained)
	capacities := make([]int32, len(children))
	for i, d := range children {
		capacities[i] = s.domainStateOf(d).podCount
	}

	assigned, unplaced, ok := placeChunks(chunks, capacities)
	if !ok {
		return nil, sliceSizesNotFitReason(chunks, capacities, unplaced, parent)
	}

	selected := make([]*domain, 0, len(children))
	for i, d := range children {
		if assigned[i] == 0 {
			continue
		}
		s.domainStateOf(d).podCount = assigned[i]
		selected = append(selected, d)
	}
	return selected, ""
}

// repeatChunksToCover repeats the chunk list as many times as it takes to
// account for every pod the parent domain holds.
//
// The layer above cuts its region into chunks of sum(sizes) pods each, and this
// list describes how one of those chunks is subdivided. Greedy placement at the
// layer above may well have put several of them in the same domain, in which
// case the same cut applies to each one.
func repeatChunksToCover(sizes []int32, pods int32) ([]int32, string) {
	var sum int32
	for _, sz := range sizes {
		sum += sz
	}
	if sum <= 0 {
		return nil, fmt.Sprintf("topology slice sizes %v must be positive", sizes)
	}
	if pods%sum != 0 {
		return nil, fmt.Sprintf("topology slice sizes %v sum to %d, which does not divide the %d pods assigned to the enclosing domain",
			sizes, sum, pods)
	}
	repeats := int(pods / sum)
	chunks := make([]int32, 0, len(sizes)*repeats)
	for range repeats {
		chunks = append(chunks, sizes...)
	}
	return chunks, ""
}
