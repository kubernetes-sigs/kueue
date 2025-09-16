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

package cache

import "sort"

func numberOfDomainsToFitGreedily(domains []*domain, sliceCount int32, leaderCount int32) (bool, int32, *domain, *domain) {
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

	fits, selectedDomainsCount, lastDomainWithLeader, lastDomain := numberOfDomainsToFitGreedily(domainsToBalance, sliceCount, leaderCount)
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
func selectOptimalDomainSetToFit(domains []*domain, count int32, leaderCount int32) []*domain {
	fit, optimalNumber, _, _ := numberOfDomainsToFitGreedily(domains, count, leaderCount)
	if !fit { // no fit
		return nil
	}
	// domain_placements[i][j][k] stores a list of domains that uses 'i' domains with 'j' leaders and 'k' slices left to fit
	domainPlacements := make([]map[int32]map[int32][]*domain, optimalNumber+1)
	for i := range domainPlacements {
		domainPlacements[i] = make(map[int32]map[int32][]*domain)
	}
	domainPlacements[0][leaderCount] = map[int32][]*domain{count: {}}

	for _, d := range domains {
		for j := optimalNumber; j > 0; j-- {
			sortedBeforeLeaderKeys := make([]int, 0, len(domainPlacements[j-1]))
			for k := range domainPlacements[j-1] {
				sortedBeforeLeaderKeys = append(sortedBeforeLeaderKeys, int(k))
			}
			sort.Sort(sort.Reverse(sort.IntSlice(sortedBeforeLeaderKeys)))

			for _, beforeLeaderInt := range sortedBeforeLeaderKeys {
				beforeLeader := int32(beforeLeaderInt)
				slicesMap := domainPlacements[j-1][beforeLeader]

				sortedBeforeSliceKeys := make([]int, 0, len(slicesMap))
				for k := range slicesMap {
					sortedBeforeSliceKeys = append(sortedBeforeSliceKeys, int(k))
				}
				sort.Sort(sort.Reverse(sort.IntSlice(sortedBeforeSliceKeys)))

				for _, beforeSliceInt := range sortedBeforeSliceKeys {
					beforeSlice := int32(beforeSliceInt)
					beforePlacement := slicesMap[beforeSlice]

					if beforeLeader <= 0 && beforeSlice <= 0 {
						continue
					}
					newPlacement := make([]*domain, len(beforePlacement), len(beforePlacement)+1)
					copy(newPlacement, beforePlacement)
					newPlacement = append(newPlacement, d)
					// Case 1: Pick this domain with leader
					if beforeLeader > 0 && d.leaderState > 0 {
						afterLeader := beforeLeader - d.leaderState
						afterSlice := beforeSlice - d.sliceStateWithLeader
						if domainPlacements[j][afterLeader] == nil {
							domainPlacements[j][afterLeader] = make(map[int32][]*domain)
						}
						if _, alreadyThere := domainPlacements[j][afterLeader][afterSlice]; !alreadyThere {
							domainPlacements[j][afterLeader][afterSlice] = newPlacement
						}
					}
					// Case 2: Pick this domain without leader
					if d.sliceState > 0 {
						afterSlice := beforeSlice - d.sliceState
						if domainPlacements[j][beforeLeader] == nil {
							domainPlacements[j][beforeLeader] = make(map[int32][]*domain)
						}
						if _, alreadyThere := domainPlacements[j][beforeLeader][afterSlice]; !alreadyThere {
							domainPlacements[j][beforeLeader][afterSlice] = newPlacement
						}
					}
				}
			}
		}
	}
	var bestSlicePlacement []*domain
	bestLeader := int32(-1 << 31)
	var bestLeaderPlacement map[int32][]*domain

	sortedAfterLeaderKeys := make([]int, 0, len(domainPlacements[optimalNumber]))
	for k := range domainPlacements[optimalNumber] {
		sortedAfterLeaderKeys = append(sortedAfterLeaderKeys, int(k))
	}
	sort.Ints(sortedAfterLeaderKeys)

	for _, leadersLeftInt := range sortedAfterLeaderKeys {
		leadersLeft := int32(leadersLeftInt)
		if leadersLeft > bestLeader && leadersLeft <= 0 {
			bestLeader = leadersLeft
			bestLeaderPlacement = domainPlacements[optimalNumber][leadersLeft]
		}
	}
	bestSlice := int32(-1 << 31) // minus infinity

	sortedAfterSliceKeys := make([]int, 0, len(bestLeaderPlacement))
	for k := range bestLeaderPlacement {
		sortedAfterSliceKeys = append(sortedAfterSliceKeys, int(k))
	}
	sort.Ints(sortedAfterSliceKeys)

	for _, slicesLeftInt := range sortedAfterSliceKeys {
		slicesLeft := int32(slicesLeftInt)
		if slicesLeft > bestSlice && slicesLeft <= 0 {
			bestSlice = slicesLeft
			bestSlicePlacement = bestLeaderPlacement[slicesLeft]
		}
	}
	return bestSlicePlacement
}

func placeSlicesOnDomainsBalanced(domains []*domain, sliceCount int32, leaderCount int32, sliceSize int32, threshold int32) []*domain {
	resultDomains := selectOptimalDomainSetToFit(domains, sliceCount, leaderCount)
	if resultDomains == nil {
		return nil
	}
	if sliceCount < int32(len(resultDomains))*threshold {
		return nil
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
		extraSlicesLeft -= extraSlicesToTake
	}
	return resultDomains
}
