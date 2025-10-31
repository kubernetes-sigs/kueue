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
func evaluateGreedyAssignment(domains []*domain, sliceCount int32, leaderCount int32) (bool, int32, *domain, *domain) {
	var selectedDomainsCount int32
	var sortedWithoutLeader, sortedWithLeader []*domain
	var lastDomain, lastDomainWithLeader *domain
	remainingSliceCount := sliceCount
	remainingLeaderCount := leaderCount
	idx := 0
	if leaderCount > 0 {
		sortedWithLeader = sortedDomainsWithLeader(domains, false)
		for ; remainingLeaderCount > 0 && idx < len(sortedWithLeader) && sortedWithLeader[idx].leaderState > 0; idx++ {
			selectedDomainsCount++
			lastDomainWithLeader = sortedWithLeader[idx]
			remainingLeaderCount -= sortedWithLeader[idx].leaderState
			remainingSliceCount -= sortedWithLeader[idx].sliceStateWithLeader
		}
		sortedWithoutLeader = sortedDomains(sortedWithLeader[idx:], false)
	} else {
		sortedWithoutLeader = sortedDomains(domains, false)
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
		threshold = min(threshold, lastDomain.sliceStateWithLeader)
	}
	return threshold
}

func selectOptimalDomainSetToFit(domains []*domain, sliceCount int32, leaderCount int32, sliceSize int32, priorizeByEntropy bool) []*domain {
	fit, optimalNumberOfDomains, _, _ := evaluateGreedyAssignment(domains, sliceCount, leaderCount)
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

func placeSlicesOnDomainsBalanced(domains []*domain, sliceCount int32, leaderCount int32, sliceSize int32, threshold int32) ([]*domain, string) {
	resultDomains := selectOptimalDomainSetToFit(domains, sliceCount, leaderCount, sliceSize, false)
	if resultDomains == nil {
		return nil, "TAS Balanced Placement: Cannot find optimal domain set to fit the request"
	}
	if sliceCount < int32(len(resultDomains))*threshold {
		return nil, "TAS Balanced Placement: Not enough slices to meet the threshold"
	}
	resultDomains = sortedDomainsWithLeader(resultDomains, false)
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
		domain.sliceStateWithLeader = domain.sliceState - domain.leaderState
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
	// Create a temporary struct to hold domains and their calculated entropy
	// to avoid re-calculation during sort.
	type domainWithEntropy struct {
		d       *domain
		entropy float64
	}

	domainsWithEntropy := make([]domainWithEntropy, 0, len(domains))
	for _, d := range domains {
		childrenCapacities := make([]int32, len(d.children))
		for i, child := range d.children {
			childrenCapacities[i] = child.state
		}
		domainsWithEntropy = append(domainsWithEntropy, domainWithEntropy{d: d, entropy: calculateEntropy(childrenCapacities)})
	}

	// Sort by capacity (desc), then by entropy (desc).
	slices.SortFunc(domainsWithEntropy, func(a, b domainWithEntropy) int {
		if r := b.d.leaderState - a.d.leaderState; r != 0 {
			return int(r)
		}
		if r := b.d.sliceStateWithLeader - a.d.sliceStateWithLeader; r != 0 {
			return int(r)
		}
		if b.entropy > a.entropy {
			return 1
		}
		if b.entropy < a.entropy {
			return -1
		}
		return 0
	})

	for i := range domainsWithEntropy {
		domains[i] = domainsWithEntropy[i].d
	}
}

// findBestDomainsForBalancedPlacement evaluates domains for balanced placement.
// It returns the best set of domains, the balance threshold, and whether a balanced placement is possible.
func findBestDomainsForBalancedPlacement(s *TASFlavorSnapshot, levelIdx, sliceLevelIdx int, count, leaderCount, sliceSize int32) ([]*domain, int32, bool) {
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
	var bestRequestedLevelDomainCount int32
	var currFitDomain []*domain
	useBalancedPlacement := false

	for _, requestedLevelSiblingDomains := range requestedLevelDomainsToConsider {
		domainsToBalance := getDomainsToBalance(s, requestedLevelSiblingDomains, levelIdx, sliceLevelIdx)
		fits, selectedDomainsCount, lastDomainWithLeader, lastDomain := evaluateGreedyAssignment(domainsToBalance, sliceCount, leaderCount)
		if !fits {
			continue
		}
		threshold := balanceThresholdValue(sliceCount, selectedDomainsCount, lastDomainWithLeader, lastDomain)
		if threshold >= bestThreshold {
			s.pruneDomainsBelowThreshold(requestedLevelSiblingDomains, threshold, sliceSize, sliceLevelIdx, levelIdx)
			_, requestedLevelDomainCount, _, _ := evaluateGreedyAssignment(requestedLevelSiblingDomains, sliceCount, leaderCount)
			if threshold > bestThreshold || (threshold == bestThreshold && requestedLevelDomainCount < bestRequestedLevelDomainCount) {
				bestThreshold = threshold
				bestRequestedLevelDomainCount = requestedLevelDomainCount
				currFitDomain = requestedLevelSiblingDomains
				useBalancedPlacement = true
			}
		}
	}
	return currFitDomain, bestThreshold, useBalancedPlacement
}

func getDomainsToBalance(s *TASFlavorSnapshot, domains []*domain, levelIdx, sliceLevelIdx int) []*domain {
	if levelIdx < sliceLevelIdx {
		return s.lowerLevelDomains(domains)
	}
	return domains
}
