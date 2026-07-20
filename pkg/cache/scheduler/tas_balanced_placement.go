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

	"sigs.k8s.io/kueue/pkg/cache/scheduler/simulator"
)

// evaluateGreedyAssignment simulates placement of a (leaderCount, sliceCount) request on the given domains.
// It returns whether the request fits, how many domains greedy algorithm uses and what would be the last
// used domain (with and without leader)
func evaluateGreedyAssignment(s *TASFlavorSnapshot, domains []*simulator.Domain, sliceCount int32, leaderCount int32) (bool, int32, *simulator.Domain, *simulator.Domain) {
	var selectedDomainsCount int32
	var sortedWithoutLeader, sortedWithLeader []*simulator.Domain
	var lastDomain, lastDomainWithLeader *simulator.Domain
	remainingSliceCount := sliceCount
	remainingLeaderCount := leaderCount
	idx := 0
	if leaderCount > 0 {
		sortedWithLeader = s.sortedDomainsWithLeader(domains, false)
		for ; remainingLeaderCount > 0 && idx < len(sortedWithLeader) && sortedWithLeader[idx].LeaderState > 0; idx++ {
			selectedDomainsCount++
			lastDomainWithLeader = sortedWithLeader[idx]
			remainingLeaderCount -= sortedWithLeader[idx].LeaderState
			remainingSliceCount -= sortedWithLeader[idx].SliceStateWithLeader
		}
		sortedWithoutLeader = s.sortedDomains(sortedWithLeader[idx:], false)
	} else {
		sortedWithoutLeader = s.sortedDomains(domains, false)
	}

	if remainingLeaderCount > 0 {
		return false, 0, nil, nil
	}

	for idx = 0; remainingSliceCount > 0 && idx < len(sortedWithoutLeader) && sortedWithoutLeader[idx].SliceState > 0; idx++ {
		selectedDomainsCount++
		lastDomain = sortedWithoutLeader[idx]
		remainingSliceCount -= sortedWithoutLeader[idx].SliceState
	}
	if remainingSliceCount > 0 {
		return false, 0, nil, nil
	}
	return true, selectedDomainsCount, lastDomainWithLeader, lastDomain
}

// The balance threshold value is maximum possible minimum number of slices placed on a domain in a balanced placement solution.
func balanceThresholdValue(sliceCount int32, selectedDomainsCount int32, lastDomainWithLeader *simulator.Domain, lastDomain *simulator.Domain) int32 {
	threshold := sliceCount / selectedDomainsCount
	if lastDomainWithLeader != nil {
		threshold = min(threshold, lastDomainWithLeader.SliceStateWithLeader)
	}
	if lastDomain != nil {
		threshold = min(threshold, lastDomain.SliceState)
	}
	return threshold
}

// selectOptimalDomainSetToFit finds a subset of the provided domains that can accommodate
// the request (sliceCount, leaderCount). It uses dynamic programming to find a combination
// of domains that can fit the requested number of leaders and slices, using the minimum number
// of domains possible (as determined by a greedy assignment) and having the minimum total capacity.
func selectOptimalDomainSetToFit(s *TASFlavorSnapshot, domains []*simulator.Domain, sliceCount int32, leaderCount int32, sliceSize int32, prioritizeByEntropy bool) []*simulator.Domain {
	fit, optimalNumberOfDomains, _, _ := evaluateGreedyAssignment(s, domains, sliceCount, leaderCount)
	if !fit {
		return nil
	}

	orderedDomains := slices.Clone(domains)
	if prioritizeByEntropy {
		slices.SortFunc(orderedDomains, compareDomainCapacityAndEntropy)
	} else {
		slices.SortFunc(orderedDomains, compareDomainLevelValues)
	}

	// domain_placements[i][j][k] stores a list of domains that uses 'i' domains with
	// ('j' leaders and 'k' workers) left to fit
	domainPlacements := make([]map[int32]map[int32][]*simulator.Domain, optimalNumberOfDomains+1)
	for i := range domainPlacements {
		domainPlacements[i] = make(map[int32]map[int32][]*simulator.Domain)
	}
	domainPlacements[0][leaderCount] = map[int32][]*simulator.Domain{sliceCount * sliceSize: {}}

	for _, d := range orderedDomains {
		for i := optimalNumberOfDomains; i > 0; i-- {
			for _, beforeLeader := range slices.Sorted(maps.Keys(domainPlacements[i-1])) {
				for _, beforeState := range slices.Sorted(maps.Keys(domainPlacements[i-1][beforeLeader])) {
					beforePlacement := domainPlacements[i-1][beforeLeader][beforeState]
					if beforeLeader <= 0 && beforeState <= 0 {
						continue
					}
					newPlacement := make([]*simulator.Domain, len(beforePlacement), len(beforePlacement)+1)
					copy(newPlacement, beforePlacement)
					newPlacement = append(newPlacement, d)
					// Case 1: Pick this domain with leader
					if beforeLeader > 0 && d.LeaderState > 0 {
						afterLeader := beforeLeader - d.LeaderState
						afterState := beforeState - d.StateWithLeader
						if domainPlacements[i][afterLeader] == nil {
							domainPlacements[i][afterLeader] = make(map[int32][]*simulator.Domain)
						}
						if _, alreadyThere := domainPlacements[i][afterLeader][afterState]; !alreadyThere {
							domainPlacements[i][afterLeader][afterState] = newPlacement
						}
					}
					// Case 2: Pick this domain without leader
					if d.SliceState > 0 {
						afterState := beforeState - d.State
						if domainPlacements[i][beforeLeader] == nil {
							domainPlacements[i][beforeLeader] = make(map[int32][]*simulator.Domain)
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
	var bestSlicePlacement []*simulator.Domain

	for _, slicesLeft := range slices.Sorted(maps.Keys(bestLeaderPlacement)) {
		if slicesLeft > bestSlice && slicesLeft <= 0 {
			bestSlice = slicesLeft
			bestSlicePlacement = bestLeaderPlacement[slicesLeft]
		}
	}
	return bestSlicePlacement
}

func placeSlicesOnDomainsBalanced(s *TASFlavorSnapshot, domains []*simulator.Domain, sliceCount int32, leaderCount int32, sliceSize int32, threshold int32) ([]*simulator.Domain, string) {
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
			extraSlicesToTake = min(domain.SliceStateWithLeader-threshold, extraSlicesLeft)
			domain.LeaderState = 1
			leadersLeft--
		case extraSlicesLeft > 0:
			extraSlicesToTake = min(domain.SliceState-threshold, extraSlicesLeft)
			domain.LeaderState = 0
		default:
			domain.LeaderState = 0
			extraSlicesToTake = 0
		}
		domain.State = (threshold + extraSlicesToTake) * sliceSize
		domain.SliceState = (threshold + extraSlicesToTake)
		domain.SliceStateWithLeader = domain.SliceState
		domain.StateWithLeader = domain.State - domain.LeaderState
		extraSlicesLeft -= extraSlicesToTake
	}
	if extraSlicesLeft > 0 || leadersLeft > 0 {
		return nil, "TAS Balanced Placement: Not all slices or leaders could be placed"
	}
	return resultDomains, ""
}

func calculateDomainsEntropy(domains []*simulator.Domain) float64 {
	if len(domains) == 0 {
		return 0.0
	}

	var total int32
	for _, d := range domains {
		total += d.State
	}

	if total == 0 {
		return 0.0
	}

	var entropy float64
	totalF := float64(total)
	for _, d := range domains {
		if d.State > 0 {
			pI := float64(d.State) / totalF
			entropy += -pI * math.Log2(pI)
		}
	}
	return entropy
}

func compareDomainCapacityAndEntropy(a, b *simulator.Domain) int {
	if r := b.LeaderState - a.LeaderState; r != 0 {
		return int(r)
	}
	if r := b.SliceStateWithLeader - a.SliceStateWithLeader; r != 0 {
		return int(r)
	}
	aEntropy := calculateDomainsEntropy(a.Children)
	bEntropy := calculateDomainsEntropy(b.Children)
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
func findBestDomainsForBalancedPlacement(s *TASFlavorSnapshot, params *topologyAssignmentParameters) ([]*simulator.Domain, int32) {
	// check if balanced placement is possible: look one level above the preferred level
	// see if any (single) domain on that level fits the request and compute for each of
	// them the balance threshold value
	sliceCount := params.count / params.sliceSize
	var requestedLevelDomainsToConsider [][]*simulator.Domain
	if params.requestedLevelIdx == 0 {
		requestedLevelDomainsToConsider = [][]*simulator.Domain{slices.Collect(maps.Values(s.domainsPerLevel[0]))}
	} else {
		higherLevelDomains := slices.Collect(maps.Values(s.domainsPerLevel[params.requestedLevelIdx-1]))
		slices.SortFunc(higherLevelDomains, compareDomainLevelValues)
		for _, higherLevelDomain := range higherLevelDomains {
			requestedLevelDomainsToConsider = append(requestedLevelDomainsToConsider, higherLevelDomain.Children)
		}
	}

	var bestThreshold int32
	var bestDomainCountOnRequestedLevel int32
	var currFitDomain []*simulator.Domain

	for _, requestedLevelSiblingDomains := range requestedLevelDomainsToConsider {
		candidateDomains := cloneDomains(requestedLevelSiblingDomains)
		lowerLevelDomains := getLowerLevelDomains(s, candidateDomains, params.requestedLevelIdx, params.sliceLevelIdx)
		fits, selectedDomainsCount, lastDomainWithLeader, lastDomain := evaluateGreedyAssignment(s, lowerLevelDomains, sliceCount, params.leaderCount)
		if !fits {
			continue
		}
		threshold := balanceThresholdValue(sliceCount, selectedDomainsCount, lastDomainWithLeader, lastDomain)
		thresholdWithLeaderReservation := threshold
		if params.leaderCount > 0 && lastDomain != nil {
			thresholdWithLeaderReservation = min(threshold, lastDomain.SliceStateWithLeader)
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
				candidateDomains = cloneDomains(requestedLevelSiblingDomains)
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
func applyBalancedPlacementAlgorithm(s *TASFlavorSnapshot, params *topologyAssignmentParameters, bestThreshold int32, currFitDomain []*simulator.Domain) ([]*simulator.Domain, int, string) {
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

func getLowerLevelDomains(s *TASFlavorSnapshot, domains []*simulator.Domain, levelIdx, sliceLevelIdx int) []*simulator.Domain {
	if levelIdx < sliceLevelIdx {
		return s.lowerLevelDomains(domains)
	}
	return domains
}

func clearState(d *simulator.Domain) {
	d.State = int32(0)
	d.SliceState = int32(0)
	d.StateWithLeader = int32(0)
	d.SliceStateWithLeader = int32(0)
	d.LeaderState = int32(0)
	for _, child := range d.Children {
		clearState(child)
	}
}

func clearLeaderCapacity(d *simulator.Domain) {
	d.StateWithLeader = int32(0)
	d.SliceStateWithLeader = int32(0)
	d.LeaderState = int32(0)
	for _, child := range d.Children {
		clearLeaderCapacity(child)
	}
}

func cloneDomains(domains []*simulator.Domain) []*simulator.Domain {
	result := make([]*simulator.Domain, len(domains))
	for i, d := range domains {
		result[i] = cloneDomain(d, nil)
	}
	return result
}

func cloneDomain(d *simulator.Domain, parent *simulator.Domain) *simulator.Domain {
	clone := *d
	clone.Parent = parent
	clone.Children = make([]*simulator.Domain, len(d.Children))
	for i, child := range d.Children {
		clone.Children[i] = cloneDomain(child, &clone)
	}
	return &clone
}

func pruneDomainNodeBelowThreshold(d *simulator.Domain, threshold int32, leaderRequired bool) {
	if d.SliceState < threshold {
		clearState(d)
		return
	}
	// The domain can still be used for workers, but not as the leader host at this threshold.
	if leaderRequired && d.LeaderState > 0 && d.SliceStateWithLeader < threshold {
		clearLeaderCapacity(d)
	}
}

func (s *TASFlavorSnapshot) pruneDomainsBelowThreshold(domains []*simulator.Domain, threshold int32, sliceSize int32, sliceLevelIdx int, level int, leaderRequired bool) {
	for _, d := range domains {
		for _, c := range d.Children {
			pruneDomainNodeBelowThreshold(c, threshold, leaderRequired)
		}
	}
	for _, d := range domains {
		s.fillInCountsHelper(d, sliceSize, sliceLevelIdx, level, nil, leaderRequired)
		pruneDomainNodeBelowThreshold(d, threshold, leaderRequired)
	}
}
