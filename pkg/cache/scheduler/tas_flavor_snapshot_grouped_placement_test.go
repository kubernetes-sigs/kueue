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
	"fmt"
	"testing"

	"github.com/go-logr/logr"

	"sigs.k8s.io/kueue/pkg/features"
	utiltas "sigs.k8s.io/kueue/pkg/util/tas"
)

type groupedPlacementDomainState struct {
	capacity           int32
	capacityWithLeader int32
	leaderFits         bool
}

func groupedPlacementDomainStates(maxCapacity int32) []groupedPlacementDomainState {
	domainStates := []groupedPlacementDomainState{}
	for capacity := int32(0); capacity <= maxCapacity; capacity++ {
		domainStates = append(domainStates, groupedPlacementDomainState{capacity: capacity})
		for capacityWithLeader := int32(0); capacityWithLeader <= capacity; capacityWithLeader++ {
			domainStates = append(domainStates, groupedPlacementDomainState{
				capacity:           capacity,
				capacityWithLeader: capacityWithLeader,
				leaderFits:         true,
			})
		}
	}
	return domainStates
}

// A grouped placement fits when at least one leader-capable domain has a
// capacity penalty no greater than the workers' available slack.
func groupedPlacementFits(configuration []groupedPlacementDomainState, totalCapacity, requiredCapacity int32) bool {
	for _, state := range configuration {
		if state.leaderFits && totalCapacity-(state.capacity-state.capacityWithLeader) >= requiredCapacity {
			return true
		}
	}
	return false
}

func groupedPlacementCapacity(configuration []groupedPlacementDomainState) int32 {
	totalCapacity := int32(0)
	for _, state := range configuration {
		totalCapacity += state.capacity
	}
	return totalCapacity
}

func TestFindLevelWithFitDomainsPreservesFeasibleGroupedPlacements(t *testing.T) {
	domainStates := groupedPlacementDomainStates(3)
	for _, unconstrained := range []bool{false, true} {
		t.Run(fmt.Sprintf("unconstrained=%t", unconstrained), func(t *testing.T) {
			if unconstrained {
				features.SetFeatureGateDuringTest(t, features.TASProfileMixed, true)
			}
			const domainCount = 3
			configuration := make([]groupedPlacementDomainState, domainCount)
			var checkConfigurations func(int)
			checkConfigurations = func(domainIndex int) {
				if domainIndex < domainCount {
					for _, state := range domainStates {
						configuration[domainIndex] = state
						checkConfigurations(domainIndex + 1)
					}
					return
				}

				checkFindLevelConfiguration(t, configuration, unconstrained)
			}
			checkConfigurations(0)
		})
	}
}

func checkFindLevelConfiguration(t *testing.T, configuration []groupedPlacementDomainState, unconstrained bool) {
	t.Helper()

	totalCapacity := groupedPlacementCapacity(configuration)

	for requiredCapacity := int32(0); requiredCapacity <= totalCapacity; requiredCapacity++ {
		if !groupedPlacementFits(configuration, totalCapacity, requiredCapacity) {
			continue
		}

		levelDomains := make(domainByID, len(configuration))
		for i, state := range configuration {
			id := utiltas.TopologyDomainID(fmt.Sprintf("domain-%d", i))
			levelDomains[id] = &domain{
				id:                   id,
				state:                state.capacity,
				stateWithLeader:      state.capacityWithLeader,
				sliceState:           state.capacity,
				sliceStateWithLeader: state.capacityWithLeader,
				levelValues:          []string{string(id)},
			}
			if state.leaderFits {
				levelDomains[id].leaderState = 1
			}
		}

		snapshot := TASFlavorSnapshot{
			log:             logr.Discard(),
			levelKeys:       []string{"hostname"},
			domainsPerLevel: []domainByID{levelDomains},
		}
		state := findTopologyAssignmentState{
			topologyAssignmentParameters: topologyAssignmentParameters{
				count:             requiredCapacity,
				leaderCount:       1,
				sliceSize:         1,
				requestedLevelIdx: 0,
				unconstrained:     unconstrained,
			},
			stats: &tasExclusionStats{},
		}
		level, result, reason := snapshot.findLevelWithFitDomains(0, &state)
		if reason != "" || result == nil || level != 0 {
			t.Fatalf("feasible domain selection failed: unconstrained=%t, count=%d, configuration=%+v, level=%d, reason=%q", unconstrained, requiredCapacity, configuration, level, reason)
		}

		selectedCapacity := int32(0)
		for _, selectedDomain := range result {
			selectedCapacity += selectedDomain.state
		}
		leaderPlacementFits := false
		for _, selectedDomain := range result {
			if selectedDomain.leaderState > 0 && selectedCapacity-(selectedDomain.state-selectedDomain.stateWithLeader) >= requiredCapacity {
				leaderPlacementFits = true
				break
			}
		}
		if !leaderPlacementFits {
			t.Fatalf("selected domains cannot preserve worker capacity after placing leader: unconstrained=%t, count=%d, configuration=%+v", unconstrained, requiredCapacity, configuration)
		}
	}
}

func TestUpdateCountsToMinimumGenericPreservesFeasibleGroupedPlacements(t *testing.T) {
	domainStates := groupedPlacementDomainStates(4)

	for _, unconstrained := range []bool{false, true} {
		for _, slicesEnabled := range []bool{false, true} {
			t.Run(fmt.Sprintf("unconstrained=%t/slicesEnabled=%t", unconstrained, slicesEnabled), func(t *testing.T) {
				if unconstrained {
					features.SetFeatureGateDuringTest(t, features.TASProfileMixed, true)
				}
				const domainCount = 3
				configuration := make([]groupedPlacementDomainState, domainCount)
				var checkConfigurations func(int)
				checkConfigurations = func(domainIndex int) {
					if domainIndex < domainCount {
						for _, state := range domainStates {
							configuration[domainIndex] = state
							checkConfigurations(domainIndex + 1)
						}
						return
					}

					checkGroupedPlacementConfiguration(t, configuration, unconstrained, slicesEnabled)
				}
				checkConfigurations(0)
			})
		}
	}
}

func checkGroupedPlacementConfiguration(t *testing.T, configuration []groupedPlacementDomainState, unconstrained, slicesEnabled bool) {
	t.Helper()

	sliceSize := int32(1)
	if slicesEnabled {
		sliceSize = 2
	}

	totalCapacity := groupedPlacementCapacity(configuration)

	for requiredCapacity := int32(0); requiredCapacity <= totalCapacity; requiredCapacity++ {
		if !groupedPlacementFits(configuration, totalCapacity, requiredCapacity) {
			continue
		}

		domains := make([]*domain, len(configuration))
		original := make(map[*domain]groupedPlacementDomainState, len(configuration))
		for i, state := range configuration {
			domains[i] = &domain{
				id:                   utiltas.TopologyDomainID(fmt.Sprintf("domain-%d", i)),
				state:                state.capacity * sliceSize,
				stateWithLeader:      state.capacityWithLeader * sliceSize,
				sliceState:           state.capacity,
				sliceStateWithLeader: state.capacityWithLeader,
				levelValues:          []string{fmt.Sprintf("domain-%d", i)},
			}
			if state.leaderFits {
				domains[i].leaderState = 1
			}
			original[domains[i]] = groupedPlacementDomainState{
				capacity:           domains[i].state,
				capacityWithLeader: domains[i].stateWithLeader,
				leaderFits:         state.leaderFits,
			}
		}

		count := requiredCapacity * sliceSize
		snapshot := TASFlavorSnapshot{log: logr.Discard()}
		result := snapshot.updateCountsToMinimumGeneric(domains, count, 1, sliceSize, unconstrained, slicesEnabled)
		if result == nil {
			t.Fatalf("feasible placement returned nil: slicesEnabled=%t, count=%d, configuration=%+v", slicesEnabled, count, configuration)
		}

		assignedWorkers := int32(0)
		assignedLeaders := int32(0)
		seen := make(map[*domain]struct{}, len(result))
		for _, assignedDomain := range result {
			if _, found := seen[assignedDomain]; found {
				t.Fatalf("domain returned more than once: slicesEnabled=%t, count=%d, configuration=%+v", slicesEnabled, count, configuration)
			}
			seen[assignedDomain] = struct{}{}

			capacity, found := original[assignedDomain]
			if !found {
				t.Fatalf("unknown domain returned: slicesEnabled=%t, count=%d, configuration=%+v, domain=%s", slicesEnabled, count, configuration, assignedDomain.id)
			}
			if assignedDomain.state < 0 || assignedDomain.leaderState < 0 {
				t.Fatalf(
					"negative assignment returned: slicesEnabled=%t, count=%d, configuration=%+v, domain=%s, assignedWorkers=%d, assignedLeaders=%d",
					slicesEnabled,
					count,
					configuration,
					assignedDomain.id,
					assignedDomain.state,
					assignedDomain.leaderState,
				)
			}
			if assignedDomain.state > capacity.capacity {
				t.Fatalf(
					"worker assignment exceeds capacity: slicesEnabled=%t, count=%d, configuration=%+v, domain=%s, assigned=%d, capacity=%d",
					slicesEnabled,
					count,
					configuration,
					assignedDomain.id,
					assignedDomain.state,
					capacity.capacity,
				)
			}
			if assignedDomain.leaderState > 0 && assignedDomain.state > capacity.capacityWithLeader {
				t.Fatalf(
					"worker assignment exceeds capacity with leader: slicesEnabled=%t, count=%d, configuration=%+v, domain=%s, assigned=%d, capacityWithLeader=%d",
					slicesEnabled,
					count,
					configuration,
					assignedDomain.id,
					assignedDomain.state,
					capacity.capacityWithLeader,
				)
			}
			if assignedDomain.leaderState > 0 && !capacity.leaderFits {
				t.Fatalf("leader assigned to infeasible domain: slicesEnabled=%t, count=%d, configuration=%+v, domain=%s", slicesEnabled, count, configuration, assignedDomain.id)
			}
			if slicesEnabled && assignedDomain.state%sliceSize != 0 {
				t.Fatalf("worker assignment is not slice-aligned: count=%d, configuration=%+v, domain=%s, assigned=%d", count, configuration, assignedDomain.id, assignedDomain.state)
			}
			assignedWorkers += assignedDomain.state
			assignedLeaders += assignedDomain.leaderState
		}

		if assignedWorkers != count || assignedLeaders != 1 {
			t.Fatalf(
				"incomplete grouped placement: slicesEnabled=%t, count=%d, configuration=%+v, assignedWorkers=%d, assignedLeaders=%d",
				slicesEnabled,
				count,
				configuration,
				assignedWorkers,
				assignedLeaders,
			)
		}
	}
}
