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
	"slices"
)

func simulateGreedy(domains []*domain, sliceCount int32, leaderCount int32) (bool, int32, *domain, *domain) {
	var selectedDomainsCount int32
	var sorted []*domain
	remainingSliceCount := sliceCount
	remainingLeaderCount := leaderCount
	var lastDomain *domain
	var lastDomainWithLeader *domain
	idx := 0
	var remainingSorted []*domain
	if leaderCount > 0 {
		sorted = sortedDomainsWithLeader(domains, false)
		for ; remainingLeaderCount > 0 && idx < len(sorted) && sorted[idx].leaderState > 0; idx++ {
			selectedDomainsCount++
			lastDomainWithLeader = sorted[idx]
			remainingLeaderCount -= sorted[idx].leaderState
			remainingSliceCount -= sorted[idx].sliceStateWithLeader
		}
		remainingSorted = sortedDomains(sorted[idx:], false)
	} else {
		remainingSorted = sortedDomains(domains, false)
	}

	if remainingLeaderCount > 0 {
		return false, 0, nil, nil
	}

	for idx = 0; remainingSliceCount > 0 && idx < len(remainingSorted) && remainingSorted[idx].sliceState > 0; idx++ {
		selectedDomainsCount++
		lastDomain = remainingSorted[idx]
		remainingSliceCount -= remainingSorted[idx].sliceState
	}
	if remainingSliceCount > 0 {
		return false, 0, nil, nil
	}
	return true, selectedDomainsCount, lastDomainWithLeader, lastDomain
}

// the balance threshold value is maximum possible minimum number of slices placed on a domain in a balanced placement solution
// to find the value, we greedily pick the domains (starting from the largest one) to accommodate the request and simulate placing
// the requested pods evenly on the selected domains.
func balanceThresholdValue(startingDomain *domain, sliceCount int32, leaderCount int32, balanceOnChildren bool) (int32, bool) {
	var lastDomain *domain
	var lastDomainWithLeader *domain
	var domainsToBalance []*domain
	if sliceCount == 0 && leaderCount == 0 {
		return 0, true
	}
	if startingDomain.sliceStateWithLeader < sliceCount || startingDomain.leaderState < leaderCount {
		return 0, false
	}
	// verify if the request fits on this domain and return what is the threshold on the selected level
	if balanceOnChildren {
		domainsToBalance = startingDomain.children
	} else {
		domainsToBalance = startingDomain.grandchildren()
	}

	fits, selectedDomainsCount, lastDomainWithLeader, lastDomain := simulateGreedy(domainsToBalance, sliceCount, leaderCount)
	if !fits {
		return 0, false
	}
	threshold := sliceCount / selectedDomainsCount
	if lastDomainWithLeader != nil {
		threshold = min(threshold, lastDomainWithLeader.sliceStateWithLeader)
	}
	if lastDomain != nil {
		threshold = min(threshold, lastDomain.sliceState)
	}
	return threshold, true
}

func selectOptimalDomainSetToFit(domains []*domain, sliceCount int32, leaderCount int32, sliceSize int32) []*domain {
	fit, optimalNumberOfDomains, _, _ := simulateGreedy(domains, sliceCount, leaderCount)
	if !fit {
		return nil
	}
	// domain_placements[i][j][k] stores a list of domains that uses 'i' domains with
	// 'j' leaders and 'k' pods left to fit
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

	bestLeader := int32(-1 << 31) // minus infinity
	var bestLeaderPlacement map[int32][]*domain

	for j := range slices.Sorted(maps.Keys(domainPlacements[optimalNumberOfDomains])) {
		leadersLeft := int32(j)
		if leadersLeft > bestLeader && leadersLeft <= 0 {
			bestLeader = leadersLeft
			bestLeaderPlacement = domainPlacements[optimalNumberOfDomains][leadersLeft]
		}
	}
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
	resultDomains := selectOptimalDomainSetToFit(domains, sliceCount, leaderCount, sliceSize)
	if resultDomains == nil {
		return nil, "TAS Balanced Placement Error: Cannot find optimal domain set to fit"
	}
	if sliceCount < int32(len(resultDomains))*threshold {
		return nil, "TAS Balanced Placement Error: Not enough slices to meet the threshold"
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
		return nil, "TAS Balanced Placement Error: Not all slices or leaders could be placed"
	}
	return resultDomains, ""
}
