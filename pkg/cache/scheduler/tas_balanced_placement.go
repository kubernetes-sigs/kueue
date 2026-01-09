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

	utilslices "sigs.k8s.io/kueue/pkg/util/slices"
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
		for ; remainingLeaderCount > 0 && idx < len(sortedWithLeader) && sortedWithLeader[idx].leaderState > 0; idx++ {
			selectedDomainsCount++
			lastDomainWithLeader = sortedWithLeader[idx]
			remainingLeaderCount -= sortedWithLeader[idx].leaderState
			remainingSliceCount -= sortedWithLeader[idx].sliceStateWithLeader
		}
		sortedWithoutLeader = s.sortedDomains(sortedWithLeader[idx:], false)
	} else {
		sortedWithoutLeader = s.sortedDomains(domains, false)
	}

	if remainingLeaderCount > 0 {
		return false, 0, nil, nil
	}

	for idx = 0; remainingSliceCount > 0 && idx < len(sortedWithoutLeader) && sortedWithoutLeader[idx].sliceState > 0; idx++ {
		selectedDomainsCount++
		lastDomain = sortedWithoutLeader[idx]
		remainingSliceCount -= sortedWithoutLeader[idx].sliceState
	}
	if remainingSliceCount > 0 {
		return false, 0, nil, nil
	}
	return true, selectedDomainsCount, lastDomainWithLeader, lastDomain
}

// The balance threshold value is maximum possible minimum number of slices placed on a domain in a balanced placement solution.
func balanceThresholdValue(sliceCount int32, selectedDomainsCount int32, lastDomainWithLeader *domain, lastDomain *domain) int32 {
	threshold := sliceCount / selectedDomainsCount
	if lastDomainWithLeader != nil {
		threshold = min(threshold, lastDomainWithLeader.sliceStateWithLeader)
	}
	if lastDomain != nil {
		// TODO: https://github.com/kubernetes-sigs/kueue/issues/7494
		// we don't have min(threshold, lastDomain.sliceState) here because
		// later we prune all nodes with sliceStateWithLeader < threshold
		// while pruning we don't know which node will host the leader
		// so we have to leave space on each node
		threshold = min(threshold, lastDomain.sliceStateWithLeader)
	}
	return threshold
}

// selectOptimalDomainSetToFit finds a subset of the provided domains that can accommodate
// the request (sliceCount, leaderCount). It uses dynamic programming to find a combination
// of domains that can fit the requested number of leaders and slices, using the minimum number
// of domains possible (as determined by a greedy assignment) and having the minimum total capacity.
func selectOptimalDomainSetToFit(s *TASFlavorSnapshot, domains []*domain, sliceCount int32, leaderCount int32, sliceSize int32, priorizeByEntropy bool) []*domain {
	fit, optimalNumberOfDomains, _, _ := evaluateGreedyAssignment(s, domains, sliceCount, leaderCount)
	if !fit {
		return nil
	}

	if priorizeByEntropy {
		sortDomainsByCapacityAndEntropy(domains)
	}

	// domain_placements[i][j][k] stores a list of domains that uses 'i' domains with
	// ('j' leaders and 'k' workers) left to fit
	domainPlacements := make([]map[int32]map[int32][]*domain, optimalNumberOfDomains+1)
	for i := range domainPlacements {
		domainPlacements[i] = make(map[int32]map[int32][]*domain)
	}
	domainPlacements[0][leaderCount] = map[int32][]*domain{sliceCount * sliceSize: {}}

	for _, d := range domains {
		for i := optimalNumberOfDomains; i > 0; i-- {
			for _, beforeLeader := range slices.Sorted(maps.Keys(domainPlacements[i-1])) {
				for _, beforeState := range slices.Sorted(maps.Keys(domainPlacements[i-1][beforeLeader])) {
					beforePlacement := domainPlacements[i-1][beforeLeader][beforeState]
					if beforeLeader <= 0 && beforeState <= 0 {
						continue
					}
					newPlacement := make([]*domain, len(beforePlacement), len(beforePlacement)+1)
					copy(newPlacement, beforePlacement)
					newPlacement = append(newPlacement, d)
					// Case 1: Pick this domain with leader
					if beforeLeader > 0 && d.leaderState > 0 {
						afterLeader := beforeLeader - d.leaderState
						afterState := beforeState - d.stateWithLeader
						if domainPlacements[i][afterLeader] == nil {
							domainPlacements[i][afterLeader] = make(map[int32][]*domain)
						}
						if _, alreadyThere := domainPlacements[i][afterLeader][afterState]; !alreadyThere {
							domainPlacements[i][afterLeader][afterState] = newPlacement
						}
					}
					// Case 2: Pick this domain without leader
					if d.sliceState > 0 {
						afterState := beforeState - d.state
						if domainPlacements[i][beforeLeader] == nil {
							domainPlacements[i][beforeLeader] = make(map[int32][]*domain)
						}
						if _, alreadyThere := domainPlacements[i][beforeLeader][afterState]; !alreadyThere {
							domainPlacements[i][beforeLeader][afterState] = newPlacement
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
		switch {
		case leadersLeft > 0:
			extraSlicesToTake = min(domain.sliceStateWithLeader-threshold, extraSlicesLeft)
			domain.leaderState = 1
			leadersLeft--
		case extraSlicesLeft > 0:
			extraSlicesToTake = min(domain.sliceState-threshold, extraSlicesLeft)
			domain.leaderState = 0
		default:
			domain.leaderState = 0
			extraSlicesToTake = 0
		}
		domain.state = (threshold + extraSlicesToTake) * sliceSize
		domain.sliceState = (threshold + extraSlicesToTake)
		domain.sliceStateWithLeader = domain.sliceState
		domain.stateWithLeader = domain.state - domain.leaderState
		extraSlicesLeft -= extraSlicesToTake
	}
	if extraSlicesLeft > 0 || leadersLeft > 0 {
		return nil, "TAS Balanced Placement: Not all slices or leaders could be placed"
	}
	return resultDomains, ""
}

func calculateEntropy(blockSizes []int32) float64 {
	if len(blockSizes) == 0 {
		return 0.0
	}

	var total int32
	for _, size := range blockSizes {
		total += size
	}

	if total == 0 {
		return 0.0
	}

	var entropy float64
	totalF := float64(total)
	for _, size := range blockSizes {
		if size > 0 {
			pI := float64(size) / totalF
			entropy += -pI * math.Log2(pI)
		}
	}
	return entropy
}

func sortDomainsByCapacityAndEntropy(domains []*domain) {
	slices.SortFunc(domains, func(a, b *domain) int {
		if a.affinityScore != b.affinityScore {
			if a.affinityScore > b.affinityScore {
				return -1
			}
			return 1
		}

		if r := b.leaderState - a.leaderState; r != 0 {
			return int(r)
		}
		if r := b.sliceStateWithLeader - a.sliceStateWithLeader; r != 0 {
			return int(r)
		}
		aChildrenCapacities := utilslices.Map(a.children, func(d **domain) int32 { return (*d).state })
		bChildrenCapacities := utilslices.Map(b.children, func(d **domain) int32 { return (*d).state })
		aEntropy := calculateEntropy(aChildrenCapacities)
		bEntropy := calculateEntropy(bChildrenCapacities)
		if bEntropy > aEntropy {
			return 1
		}
		if bEntropy < aEntropy {
			return -1
		}
		return 0
	})
}

// findBestDomainsForBalancedPlacement evaluates domains for balanced placement.
// It returns the best set of domains, the balance threshold, and whether a balanced placement is possible.
func findBestDomainsForBalancedPlacement(s *TASFlavorSnapshot, levelIdx, sliceLevelIdx int, count, leaderCount, sliceSize int32) ([]*domain, int32) {
	// check if balanced placement is possible: look one level above the preferred level
	// see if any (single) domain on that level fits the request and compute for each of
	// them the balance threshold value
	sliceCount := count / sliceSize
	var requestedLevelDomainsToConsider [][]*domain
	if levelIdx == 0 {
		requestedLevelDomainsToConsider = [][]*domain{slices.Collect(maps.Values(s.domainsPerLevel[0]))}
	} else {
		for _, higherLevelDomain := range slices.Collect(maps.Values(s.domainsPerLevel[levelIdx-1])) {
			requestedLevelDomainsToConsider = append(requestedLevelDomainsToConsider, higherLevelDomain.children)
		}
	}

	var bestThreshold int32
	var bestDomainCountOnRequestedLevel int32
	var currFitDomain []*domain

	for _, requestedLevelSiblingDomains := range requestedLevelDomainsToConsider {
		lowerLevelDomains := getLowerLevelDomains(s, requestedLevelSiblingDomains, levelIdx, sliceLevelIdx)
		fits, selectedDomainsCount, lastDomainWithLeader, lastDomain := evaluateGreedyAssignment(s, lowerLevelDomains, sliceCount, leaderCount)
		if !fits {
			continue
		}
		threshold := balanceThresholdValue(sliceCount, selectedDomainsCount, lastDomainWithLeader, lastDomain)
		if threshold >= bestThreshold {
			s.pruneDomainsBelowThreshold(requestedLevelSiblingDomains, threshold, sliceSize, sliceLevelIdx, levelIdx)
			_, requestedLevelDomainCount, _, _ := evaluateGreedyAssignment(s, requestedLevelSiblingDomains, sliceCount, leaderCount)
			if threshold > bestThreshold || (threshold == bestThreshold && requestedLevelDomainCount < bestDomainCountOnRequestedLevel) {
				bestThreshold = threshold
				bestDomainCountOnRequestedLevel = requestedLevelDomainCount
				currFitDomain = requestedLevelSiblingDomains
			}
		}
	}
	return currFitDomain, bestThreshold
}

// applyBalancedPlacementAlgorithm applies the balanced placement algorithm to determine domain assignments
// on the requested level(s)
func applyBalancedPlacementAlgorithm(s *TASFlavorSnapshot, levelIdx, sliceLevelIdx int, count, leaderCount, sliceSize, bestThreshold int32, currFitDomain []*domain) ([]*domain, int, string) {
	sliceCount := count / sliceSize
	var fitLevelIdx int
	if levelIdx < sliceLevelIdx {
		resultDomains := selectOptimalDomainSetToFit(s, currFitDomain, sliceCount, leaderCount, sliceSize, true)
		if resultDomains == nil {
			return nil, 0, "TAS Balanced Placement: Cannot find optimal domain set to fit the request"
		}
		currFitDomain = s.lowerLevelDomains(resultDomains)
		fitLevelIdx = levelIdx + 1
	} else {
		fitLevelIdx = levelIdx
	}
	var reason string
	currFitDomain, reason = placeSlicesOnDomainsBalanced(s, currFitDomain, sliceCount, leaderCount, sliceSize, bestThreshold)
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

func clearState(d *domain) {
	d.state = int32(0)
	d.sliceState = int32(0)
	d.stateWithLeader = int32(0)
	d.sliceStateWithLeader = int32(0)
	d.leaderState = int32(0)
	for _, child := range d.children {
		clearState(child)
	}
}

func (s *TASFlavorSnapshot) pruneDomainsBelowThreshold(domains []*domain, threshold int32, sliceSize int32, sliceLevelIdx int, level int) {
	for _, d := range domains {
		for _, c := range d.children {
			if c.sliceStateWithLeader < threshold {
				clearState(c)
			}
		}
	}
	for _, d := range domains {
		d.state, d.sliceState, d.stateWithLeader, d.sliceStateWithLeader, d.leaderState, d.affinityScore = s.fillInCountsHelper(d, sliceSize, sliceLevelIdx, level)
		if d.sliceStateWithLeader < threshold {
			clearState(d)
		}
	}
}
