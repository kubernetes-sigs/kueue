/*
Copyright 2024 The Kubernetes Authors.

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

import (
	"fmt"
	"slices"
	"strings"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/resources"
	utiltas "sigs.k8s.io/kueue/pkg/util/tas"
)

type info struct {
	// count is a temporary state of the topology domain during the topology
	// assignment algorithm.
	//
	// In the first phase of the algorithm (traversal to the top the topology to
	// determine the level to fit the workload) it denotes the number of pods
	// which can fit in a given domain.
	//
	// In the second phase of the algorithm (traversal to the bottom to
	// determine the actual assignments) it denotes the number of pods actually
	// assigned to the given domain.
	count int32

	// sortName indicates name used for sorting when two domains can fit
	// the same number of pods
	sortName string

	// id is the globally unique id of the domain
	id utiltas.TopologyDomainID

	// parentID is the global ID of the parent domain
	parentID utiltas.TopologyDomainID

	// childIDs at the global IDs of the child domains
	childIDs []utiltas.TopologyDomainID
}

type infoPerDomain map[utiltas.TopologyDomainID]*info

type TASFlavorSnapshot struct {
	levelKeys             []string
	freeCapacityPerDomain map[utiltas.TopologyDomainID]resources.Requests
	levelValuesPerDomain  map[utiltas.TopologyDomainID][]string
	infoPerLevel          []infoPerDomain
}

func newTASFlavorSnapshot(levels []string) *TASFlavorSnapshot {
	labelsCopy := slices.Clone(levels)
	snapshot := &TASFlavorSnapshot{
		levelKeys:             labelsCopy,
		freeCapacityPerDomain: make(map[utiltas.TopologyDomainID]resources.Requests),
		levelValuesPerDomain:  make(map[utiltas.TopologyDomainID][]string),
		infoPerLevel:          make([]infoPerDomain, len(levels)),
	}
	return snapshot
}

// initialize prepares the infoPerLevel tree structure which is used during the
// algorithm for multiple workloads.
func (s *TASFlavorSnapshot) initialize() {
	levelCount := len(s.levelKeys)
	lastLevelIdx := levelCount - 1
	for levelIdx := 0; levelIdx < len(s.levelKeys); levelIdx++ {
		s.infoPerLevel[levelIdx] = make(map[utiltas.TopologyDomainID]*info)
	}
	for childID := range s.freeCapacityPerDomain {
		childInfo := &info{
			sortName: s.sortName(lastLevelIdx, childID),
			id:       childID,
			childIDs: make([]utiltas.TopologyDomainID, 0),
		}
		s.infoPerLevel[lastLevelIdx][childID] = childInfo
		parentFound := false
		var parentInfo *info
		for levelIdx := lastLevelIdx - 1; levelIdx >= 0 && !parentFound; levelIdx-- {
			parentValues := s.levelValuesPerDomain[childID][:levelIdx+1]
			parentID := utiltas.DomainID(parentValues)
			s.levelValuesPerDomain[parentID] = parentValues
			parentInfo, parentFound = s.infoPerLevel[levelIdx][parentID]
			if !parentFound {
				parentInfo = &info{
					sortName: s.sortName(levelIdx, parentID),
					id:       parentID,
					childIDs: make([]utiltas.TopologyDomainID, 0),
				}
				s.infoPerLevel[levelIdx][parentID] = parentInfo
			}
			childInfo.parentID = parentID
			parentInfo.childIDs = append(parentInfo.childIDs, childID)
			childID = parentID
		}
	}
}

func (s *TASFlavorSnapshot) sortName(levelIdx int, domainID utiltas.TopologyDomainID) string {
	levelValues := s.levelValuesPerDomain[domainID]
	if len(levelValues) <= levelIdx {
		panic(fmt.Sprintf("levelValues count=%d, levelIdx=%d, domainID=%v, levelValuesPerDomain=%v, freeCapacityPerDomain=%v",
			len(levelValues), levelIdx, domainID, s.levelValuesPerDomain, s.freeCapacityPerDomain))
	}
	// we prefix with the node label value to make it ordered naturally, but
	// append also domain ID as the node label value may not be globally unique.
	return s.levelValuesPerDomain[domainID][levelIdx] + " " + string(domainID)
}

func (s *TASFlavorSnapshot) addCapacity(domainID utiltas.TopologyDomainID, capacity resources.Requests) {
	s.ensureDomainExists(domainID)
	s.freeCapacityPerDomain[domainID].Add(capacity)
}

func (s *TASFlavorSnapshot) addUsage(domainID utiltas.TopologyDomainID, usage resources.Requests) {
	s.ensureDomainExists(domainID)
	s.freeCapacityPerDomain[domainID].Sub(usage)
}

func (s *TASFlavorSnapshot) ensureDomainExists(domainID utiltas.TopologyDomainID) {
	if _, found := s.freeCapacityPerDomain[domainID]; !found {
		s.freeCapacityPerDomain[domainID] = resources.Requests{}
	}
}

// Algorithm steps:
// 1. determine pod counts at each topology domain at the lowest level
// 2. bubble up the pod counts to the top level
// 3. select the domain at requested level with count >= requestedCount
// 4. step down level-by-level optimizing the number of used domains at each level
func (s *TASFlavorSnapshot) FindTopologyAssignment(
	topologyRequest *kueue.PodSetTopologyRequest,
	requests resources.Requests,
	count int32) *kueue.TopologyAssignment {
	required := topologyRequest.Required != nil
	levelIdx, found := s.resolveLevelIdx(topologyRequest)
	if !found {
		return nil
	}
	s.fillInCounts(requests)
	fitLevelIdx, currFitInfos := s.findLevelWithFitInfos(levelIdx, required, count)
	if len(currFitInfos) == 0 {
		return nil
	}
	currFitInfos = s.minLevelInfos(currFitInfos, count)
	for currLevelIdx := fitLevelIdx; currLevelIdx+1 < len(s.infoPerLevel); currLevelIdx++ {
		lowerFitInfos := s.lowerLevelInfos(currLevelIdx, currFitInfos)
		sortedLowerInfos := s.sortedInfos(lowerFitInfos)
		currFitInfos = s.minLevelInfos(sortedLowerInfos, count)
	}
	return s.buildAssignment(currFitInfos)
}

func (s *TASFlavorSnapshot) resolveLevelIdx(
	topologyRequest *kueue.PodSetTopologyRequest) (int, bool) {
	var levelKey string
	if topologyRequest.Required != nil {
		levelKey = *topologyRequest.Required
	} else if topologyRequest.Preferred != nil {
		levelKey = *topologyRequest.Preferred
	}
	levelIdx := slices.Index(s.levelKeys, levelKey)
	if levelIdx < 0 {
		return levelIdx, false
	}
	return levelIdx, true
}

func (s *TASFlavorSnapshot) findLevelWithFitInfos(levelIdx int, required bool, count int32) (int, []*info) {
	levelInfos := s.infosForLevel(levelIdx)
	if len(levelInfos) == 0 {
		return 0, nil
	}
	sortedInfos := s.sortedInfos(levelInfos)
	topInfo := sortedInfos[0]
	if topInfo.count < count {
		if required {
			return 0, nil
		} else if levelIdx > 0 {
			return s.findLevelWithFitInfos(levelIdx-1, required, count)
		}
		lastIdx := 0
		remainingCount := count - sortedInfos[lastIdx].count
		for remainingCount > 0 && lastIdx < len(sortedInfos)-1 {
			lastIdx++
			remainingCount -= sortedInfos[lastIdx].count
		}
		if remainingCount > 0 {
			return 0, nil
		}
		return 0, sortedInfos[:lastIdx+1]
	}
	return levelIdx, []*info{topInfo}
}

func (s *TASFlavorSnapshot) minLevelInfos(infos []*info, count int32) []*info {
	result := make([]*info, 0)
	remainingCount := count
	for i := 0; i < len(infos); i++ {
		info := infos[i]
		if info.count >= remainingCount {
			info.count = remainingCount
			result = append(result, info)
			return result
		} else if info.count > 0 {
			remainingCount -= info.count
			result = append(result, info)
		}
	}
	panic("unexpected remaining count")
}

func (s *TASFlavorSnapshot) buildAssignment(infos []*info) *kueue.TopologyAssignment {
	assignment := kueue.TopologyAssignment{
		Levels:  s.levelKeys,
		Domains: make([]kueue.TopologyDomainAssignment, 0),
	}
	for i := 0; i < len(infos); i++ {
		assignment.Domains = append(assignment.Domains, kueue.TopologyDomainAssignment{
			Values: s.asLevelValues(infos[i].id),
			Count:  infos[i].count,
		})
	}
	return &assignment
}

func (s *TASFlavorSnapshot) asLevelValues(domainID utiltas.TopologyDomainID) []string {
	result := make([]string, len(s.levelKeys))
	for i := range s.levelKeys {
		result[i] = s.levelValuesPerDomain[domainID][i]
	}
	return result
}

func (s *TASFlavorSnapshot) lowerLevelInfos(levelIdx int, infos []*info) []*info {
	result := make([]*info, 0, len(infos))
	for _, info := range infos {
		for _, childDomainID := range info.childIDs {
			if childDomain := s.infoPerLevel[levelIdx+1][childDomainID]; childDomain != nil {
				result = append(result, childDomain)
			}
		}
	}
	return result
}

func (s *TASFlavorSnapshot) infosForLevel(levelIdx int) []*info {
	infosMap := s.infoPerLevel[levelIdx]
	result := make([]*info, len(infosMap))
	index := 0
	for _, info := range infosMap {
		result[index] = info
		index++
	}
	return result
}

func (s *TASFlavorSnapshot) sortedInfos(infos []*info) []*info {
	result := make([]*info, len(infos))
	copy(result, infos)
	slices.SortFunc(result, func(a, b *info) int {
		switch {
		case a.count == b.count:
			return strings.Compare(a.sortName, b.sortName)
		case a.count > b.count:
			return -1
		default:
			return 1
		}
	})
	return result
}

func (s *TASFlavorSnapshot) fillInCounts(requests resources.Requests) {
	s.fillInLeafCounts(requests)
	s.fillInInnerCounts()
}

func (s *TASFlavorSnapshot) fillInInnerCounts() {
	lastLevelIdx := len(s.infoPerLevel) - 1
	for levelIdx := lastLevelIdx - 1; levelIdx >= 0; levelIdx-- {
		for _, info := range s.infoPerLevel[levelIdx] {
			info.count = 0
			for _, childDomainID := range info.childIDs {
				info.count += s.infoPerLevel[levelIdx+1][childDomainID].count
			}
		}
	}
}

func (tasRf *TASFlavorSnapshot) fillInLeafCounts(requests resources.Requests) {
	lastLevelIdx := len(tasRf.infoPerLevel) - 1
	for domainID, capacity := range tasRf.freeCapacityPerDomain {
		tasRf.infoPerLevel[lastLevelIdx][domainID].count = requests.CountIn(capacity)
	}
}
