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
	"maps"
	"math"
	"slices"
)

// evaluateGreedyAssignment simulates placement of a (leaderCount, sliceCount) request on the given domains.
// It returns whether the request fits, how many domains greedy algorithm uses and what would be the last
// used domain (with and without leader)
func evaluateGreedyAssignment(s *TASFlavorSnapshot, domains []*domain, sliceCount int32, leaderCount int32) (bool, int32, *domain, *domain) {
	var selectedDomainsCount int32
	var sortedWithoutLeader, sortedWithLeader []*domain
	var lastDomain, lastDomainWithLeader *domain
	remainingSliceCount := sliceCount
	remainingLeaderCount := leaderCount
	idx := 0
	if leaderCount > 0 {
		sortedWithLeader = s.sortedDomainsWithLeader(domains, false)
		for ; remainingLeaderCount > 0 && idx < len(sortedWithLeader) && s.domainStateOf(sortedWithLeader[idx]).leaderCount > 0; idx++ {
			selectedDomainsCount++
			lastDomainWithLeader = sortedWithLeader[idx]
			remainingLeaderCount -= s.domainStateOf(sortedWithLeader[idx]).leaderCount
			remainingSliceCount -= s.domainStateOf(sortedWithLeader[idx]).sliceCountWithLeader
		}
		sortedWithoutLeader = s.sortedDomains(sortedWithLeader[idx:], false)
	} else {
		sortedWithoutLeader = s.sortedDomains(domains, false)
	}

	if remainingLeaderCount > 0 {
		return false, 0, nil, nil
	}

	for idx = 0; remainingSliceCount > 0 && idx < len(sortedWithoutLeader) && s.domainStateOf(sortedWithoutLeader[idx]).sliceCount > 0; idx++ {
		selectedDomainsCount++
		lastDomain = sortedWithoutLeader[idx]
		remainingSliceCount -= s.domainStateOf(sortedWithoutLeader[idx]).sliceCount
	}
	if remainingSliceCount > 0 {
		return false, 0, nil, nil
	}
	return true, selectedDomainsCount, lastDomainWithLeader, lastDomain
}

// The balance threshold value is maximum possible minimum number of slices placed on a domain in a balanced placement solution.
func balanceThresholdValue(s *TASFlavorSnapshot, sliceCount int32, selectedDomainsCount int32, lastDomainWithLeader *domain, lastDomain *domain) int32 {
	threshold := sliceCount / selectedDomainsCount
	if lastDomainWithLeader != nil {
		threshold = min(threshold, s.domainStateOf(lastDomainWithLeader).sliceCountWithLeader)
	}
	if lastDomain != nil {
		threshold = min(threshold, s.domainStateOf(lastDomain).sliceCount)
	}
	return threshold
}

// selectOptimalDomainSetToFit finds a subset of the provided domains that can accommodate
// the request (sliceCount, leaderCount). It uses dynamic programming to find a combination
// of domains that can fit the requested number of leaders and slices, using the minimum number
// of domains possible (as determined by a greedy assignment) and having the minimum total capacity.
func selectOptimalDomainSetToFit(s *TASFlavorSnapshot, domains []*domain, sliceCount int32, leaderCount int32, sliceSize int32, prioritizeByEntropy bool) []*domain {
	fit, optimalNumberOfDomains, _, _ := evaluateGreedyAssignment(s, domains, sliceCount, leaderCount)
	if !fit {
		return nil
	}

	orderedDomains := slices.Clone(domains)
	if prioritizeByEntropy {
		slices.SortFunc(orderedDomains, s.compareDomainCapacityAndEntropy)
	} else {
		slices.SortFunc(orderedDomains, compareDomainLevelValues)
	}

	// domain_placements[i][j][k] stores a list of domains that uses 'i' domains with
	// ('j' leaders and 'k' workers) left to fit
	domainPlacements := make([]map[int32]map[int32][]*domain, optimalNumberOfDomains+1)
	for i := range domainPlacements {
		domainPlacements[i] = make(map[int32]map[int32][]*domain)
	}
	domainPlacements[0][leaderCount] = map[int32][]*domain{sliceCount * sliceSize: {}}

	for _, d := range orderedDomains {
		domainState := s.domainStateOf(d)
		for i := optimalNumberOfDomains; i > 0; i-- {
			for _, beforeLeader := range slices.Sorted(maps.Keys(domainPlacements[i-1])) {
				for _, beforePods := range slices.Sorted(maps.Keys(domainPlacements[i-1][beforeLeader])) {
					beforePlacement := domainPlacements[i-1][beforeLeader][beforePods]
					if beforeLeader <= 0 && beforePods <= 0 {
						continue
					}
					newPlacement := make([]*domain, len(beforePlacement), len(beforePlacement)+1)
					copy(newPlacement, beforePlacement)
					newPlacement = append(newPlacement, d)
					// Case 1: Pick this domain with leader
					if beforeLeader > 0 && domainState.leaderCount > 0 {
						afterLeader := beforeLeader - domainState.leaderCount
						afterPods := beforePods - domainState.podCountWithLeader
						if domainPlacements[i][afterLeader] == nil {
							domainPlacements[i][afterLeader] = make(map[int32][]*domain)
						}
						if _, alreadyThere := domainPlacements[i][afterLeader][afterPods]; !alreadyThere {
							domainPlacements[i][afterLeader][afterPods] = newPlacement
						}
					}
					// Case 2: Pick this domain without leader
					if domainState.sliceCount > 0 {
						afterPods := beforePods - domainState.podCount
						if domainPlacements[i][beforeLeader] == nil {
							domainPlacements[i][beforeLeader] = make(map[int32][]*domain)
						}
						if _, alreadyThere := domainPlacements[i][beforeLeader][afterPods]; !alreadyThere {
							domainPlacements[i][beforeLeader][afterPods] = newPlacement
						}
					}
				}
			}
		}
	}

	bestLeaderPlacement := domainPlacements[optimalNumberOfDomains][0]
	bestSlice := int32(-1 << 31) // minus infinity
	var bestSlicePlacement []*domain

	for _, slicesLeft := range slices.Sorted(maps.Keys(bestLeaderPlacement)) {
		if slicesLeft > bestSlice && slicesLeft <= 0 {
			bestSlice = slicesLeft
			bestSlicePlacement = bestLeaderPlacement[slicesLeft]
		}
	}
	return bestSlicePlacement
}

func placeSlicesOnDomainsBalanced(s *TASFlavorSnapshot, domains []*domain, sliceCount int32, leaderCount int32, sliceSize int32, threshold int32) ([]*domain, string) {
	resultDomains := selectOptimalDomainSetToFit(s, domains, sliceCount, leaderCount, sliceSize, false)
	if resultDomains == nil {
		return nil, "TAS Balanced Placement: Cannot find optimal domain set to fit the request"
	}
	if sliceCount < int32(len(resultDomains))*threshold {
		return nil, "TAS Balanced Placement: Not enough slices to meet the threshold"
	}
	resultDomains = s.sortedDomainsWithLeader(resultDomains, false)
	extraSlicesLeft := sliceCount - int32(len(resultDomains))*threshold
	leadersLeft := leaderCount
	var extraSlicesToTake int32
	for _, domain := range resultDomains {
		domainState := s.domainStateOf(domain)
		switch {
		case leadersLeft > 0:
			extraSlicesToTake = min(domainState.sliceCountWithLeader-threshold, extraSlicesLeft)
			domainState.leaderCount = 1
			leadersLeft--
		case extraSlicesLeft > 0:
			extraSlicesToTake = min(domainState.sliceCount-threshold, extraSlicesLeft)
			domainState.leaderCount = 0
		default:
			domainState.leaderCount = 0
			extraSlicesToTake = 0
		}
		domainState.podCount = (threshold + extraSlicesToTake) * sliceSize
		domainState.sliceCount = (threshold + extraSlicesToTake)
		domainState.sliceCountWithLeader = domainState.sliceCount
		domainState.podCountWithLeader = domainState.podCount - domainState.leaderCount
		extraSlicesLeft -= extraSlicesToTake
	}
	if extraSlicesLeft > 0 || leadersLeft > 0 {
		return nil, "TAS Balanced Placement: Not all slices or leaders could be placed"
	}
	return resultDomains, ""
}

func (s *TASFlavorSnapshot) calculateDomainsEntropy(domains []*domain) float64 {
	if len(domains) == 0 {
		return 0.0
	}

	var total int32
	for _, d := range domains {
		total += s.domainStateOf(d).podCount
	}

	if total == 0 {
		return 0.0
	}

	var entropy float64
	totalF := float64(total)
	for _, d := range domains {
		if podCount := s.domainStateOf(d).podCount; podCount > 0 {
			pI := float64(podCount) / totalF
			entropy += -pI * math.Log2(pI)
		}
	}
	return entropy
}

func (s *TASFlavorSnapshot) compareDomainCapacityAndEntropy(a, b *domain) int {
	if r := s.domainStateOf(b).leaderCount - s.domainStateOf(a).leaderCount; r != 0 {
		return int(r)
	}
	if r := s.domainStateOf(b).sliceCountWithLeader - s.domainStateOf(a).sliceCountWithLeader; r != 0 {
		return int(r)
	}
	aEntropy := s.calculateDomainsEntropy(a.children)
	bEntropy := s.calculateDomainsEntropy(b.children)
	if bEntropy > aEntropy {
		return 1
	}
	if bEntropy < aEntropy {
		return -1
	}
	return compareDomainLevelValues(a, b)
}

// findBestDomainsForBalancedPlacement evaluates domains for balanced placement.
// It returns the best set of domains and the balance threshold.
// A threshold greater than zero means balanced placement is possible.
func findBestDomainsForBalancedPlacement(s *TASFlavorSnapshot, params *topologyAssignmentParameters) ([]*domain, int32) {
	// check if balanced placement is possible: look one level above the preferred level
	// see if any (single) domain on that level fits the request and compute for each of
	// them the balance threshold value
	sliceCount := params.count / params.sliceSize
	var requestedLevelDomainsToConsider [][]*domain
	if params.requestedLevelIdx == 0 {
		requestedLevelDomainsToConsider = [][]*domain{slices.Collect(maps.Values(s.domainsPerLevel[0]))}
	} else {
		higherLevelDomains := slices.Collect(maps.Values(s.domainsPerLevel[params.requestedLevelIdx-1]))
		slices.SortFunc(higherLevelDomains, compareDomainLevelValues)
		for _, higherLevelDomain := range higherLevelDomains {
			requestedLevelDomainsToConsider = append(requestedLevelDomainsToConsider, higherLevelDomain.children)
		}
	}

	var bestThreshold int32
	var bestDomainCountOnRequestedLevel int32
	var currFitDomain []*domain

	for _, requestedLevelSiblingDomains := range requestedLevelDomainsToConsider {
		candidateDomains := s.cloneDomains(requestedLevelSiblingDomains)
		lowerLevelDomains := getLowerLevelDomains(s, candidateDomains, params.requestedLevelIdx, params.sliceLevelIdx)
		fits, selectedDomainsCount, lastDomainWithLeader, lastDomain := evaluateGreedyAssignment(s, lowerLevelDomains, sliceCount, params.leaderCount)
		if !fits {
			continue
		}
		threshold := balanceThresholdValue(s, sliceCount, selectedDomainsCount, lastDomainWithLeader, lastDomain)
		thresholdWithLeaderReservation := threshold
		if params.leaderCount > 0 && lastDomain != nil {
			thresholdWithLeaderReservation = min(threshold, s.domainStateOf(lastDomain).sliceCountWithLeader)
		}
		if threshold >= bestThreshold {
			s.pruneDomainsBelowThreshold(candidateDomains, threshold, params.sliceSize, params.sliceLevelIdx, params.requestedLevelIdx, params.leaderCount > 0)
			fitsAfterPruning, requestedLevelDomainCount, _, _ := evaluateGreedyAssignment(s, candidateDomains, sliceCount, params.leaderCount)
			if !fitsAfterPruning && thresholdWithLeaderReservation < threshold {
				// Retry with a lower threshold that reserves leader capacity.
				if thresholdWithLeaderReservation <= 0 || thresholdWithLeaderReservation < bestThreshold {
					continue
				}
				threshold = thresholdWithLeaderReservation
				candidateDomains = s.cloneDomains(requestedLevelSiblingDomains)
				s.pruneDomainsBelowThreshold(candidateDomains, threshold, params.sliceSize, params.sliceLevelIdx, params.requestedLevelIdx, params.leaderCount > 0)
				fitsAfterPruning, requestedLevelDomainCount, _, _ = evaluateGreedyAssignment(s, candidateDomains, sliceCount, params.leaderCount)
			}
			if !fitsAfterPruning {
				continue
			}
			if threshold > bestThreshold || (threshold == bestThreshold && requestedLevelDomainCount < bestDomainCountOnRequestedLevel) {
				bestThreshold = threshold
				bestDomainCountOnRequestedLevel = requestedLevelDomainCount
				currFitDomain = candidateDomains
			}
		}
	}
	return currFitDomain, bestThreshold
}

// applyBalancedPlacementAlgorithm applies the balanced placement algorithm to determine domain assignments
// on the requested level(s) and returns the selected domains, the starting level index, and
// failure reason.
func applyBalancedPlacementAlgorithm(s *TASFlavorSnapshot, params *topologyAssignmentParameters, bestThreshold int32, currFitDomain []*domain) ([]*domain, int, string) {
	sliceCount := params.count / params.sliceSize
	var fitLevelIdx int
	if params.requestedLevelIdx < params.sliceLevelIdx {
		resultDomains := selectOptimalDomainSetToFit(s, currFitDomain, sliceCount, params.leaderCount, params.sliceSize, true)
		if resultDomains == nil {
			return nil, 0, "TAS Balanced Placement: Cannot find optimal domain set to fit the request"
		}
		currFitDomain = s.lowerLevelDomains(resultDomains)
		fitLevelIdx = params.requestedLevelIdx + 1
	} else {
		fitLevelIdx = params.requestedLevelIdx
	}
	var reason string
	currFitDomain, reason = placeSlicesOnDomainsBalanced(s, currFitDomain, sliceCount, params.leaderCount, params.sliceSize, bestThreshold)
	if len(reason) > 0 {
		return nil, 0, reason
	}
	return currFitDomain, fitLevelIdx, ""
}

func getLowerLevelDomains(s *TASFlavorSnapshot, domains []*domain, levelIdx, sliceLevelIdx int) []*domain {
	if levelIdx < sliceLevelIdx {
		return s.lowerLevelDomains(domains)
	}
	return domains
}

func (s *TASFlavorSnapshot) clearState(d *domain) {
	st := s.domainStateOf(d)
	*st = domainState{affinityScore: st.affinityScore}
	for _, child := range d.children {
		s.clearState(child)
	}
}

func (s *TASFlavorSnapshot) clearLeaderCapacity(d *domain) {
	domainState := s.domainStateOf(d)
	domainState.podCountWithLeader = 0
	domainState.sliceCountWithLeader = 0
	domainState.leaderCount = 0
	for _, child := range d.children {
		s.clearLeaderCapacity(child)
	}
}

// cloneDomains deep-copies the given domain subtrees, giving every copied
// domain its own per-snapshot state slot initialized from the original's
// current state. This lets the balanced-placement algorithm speculatively
// mutate (e.g. prune) the copies without corrupting the state of the shared
// domains, which other candidate sets and the fallback path still need.
func (s *TASFlavorSnapshot) cloneDomains(domains []*domain) []*domain {
	result := make([]*domain, len(domains))
	for i, d := range domains {
		result[i] = s.cloneDomain(d, nil)
	}
	return result
}

func (s *TASFlavorSnapshot) cloneDomain(d *domain, parent *domain) *domain {
	clone := s.shallowCloneWithState(d)
	clone.parent = parent
	clone.children = make([]*domain, len(d.children))
	for i, child := range d.children {
		clone.children[i] = s.cloneDomain(child, clone)
	}
	return clone
}

func (s *TASFlavorSnapshot) pruneDomainNodeBelowThreshold(d *domain, threshold int32, leaderRequired bool) {
	domainState := s.domainStateOf(d)
	if domainState.sliceCount < threshold {
		s.clearState(d)
		return
	}
	// The domain can still be used for workers, but not as the leader host at this threshold.
	if leaderRequired && domainState.leaderCount > 0 && domainState.sliceCountWithLeader < threshold {
		s.clearLeaderCapacity(d)
	}
}

func (s *TASFlavorSnapshot) pruneDomainsBelowThreshold(domains []*domain, threshold int32, sliceSize int32, sliceLevelIdx int, level int, leaderRequired bool) {
	for _, d := range domains {
		for _, c := range d.children {
			s.pruneDomainNodeBelowThreshold(c, threshold, leaderRequired)
		}
	}
	for _, d := range domains {
		s.fillInCountsHelper(d, sliceSize, sliceLevelIdx, level, nil, leaderRequired)
		s.pruneDomainNodeBelowThreshold(d, threshold, leaderRequired)
	}
}
