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

// topologyDomainNode holds the static information about placement of a topology
// domain in the hierarchy of topology domains.
type topologyDomainNode struct {
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

type nodePerDomain map[utiltas.TopologyDomainID]*topologyDomainNode
type statePerDomain map[utiltas.TopologyDomainID]int32

type TASFlavorSnapshot struct {
	levelKeys []string

	// freeCapacityPerDomain stores the free capacity per domain, only for the
	// lowest level of topology
	freeCapacityPerDomain map[utiltas.TopologyDomainID]resources.Requests

	// levelValuesPerDomain stores the mapping from domain ID back to the
	// ordered list of values. It stores the information for all levels.
	levelValuesPerDomain map[utiltas.TopologyDomainID][]string

	// nodePerLevel stores the static tree information
	nodePerLevel []nodePerDomain

	// statePerLevel is a temporary state of the topology domains during the
	// assignment algorithm.
	//
	// In the first phase of the algorithm (traversal to the top the topology to
	// determine the level to fit the workload) it denotes the number of pods
	// which can fit in a given domain.
	//
	// In the second phase of the algorithm (traversal to the bottom to
	// determine the actual assignments) it denotes the number of pods actually
	// assigned to the given domain.
	state statePerDomain
}

func newTASFlavorSnapshot(levels []string) *TASFlavorSnapshot {
	snapshot := &TASFlavorSnapshot{
		levelKeys:             slices.Clone(levels),
		freeCapacityPerDomain: make(map[utiltas.TopologyDomainID]resources.Requests),
		levelValuesPerDomain:  make(map[utiltas.TopologyDomainID][]string),
		nodePerLevel:          make([]nodePerDomain, len(levels)),
		state:                 make(statePerDomain),
	}
	return snapshot
}

// initialize prepares the nodePerLevel tree structure. This structure holds
// for a given the list of topology domains with additional static and dynamic
// information. This function initializes the static information which
// represents the edges to the parent and child domains. This structure is
// reused for multiple workloads during a single scheduling cycle.
func (s *TASFlavorSnapshot) initialize() {
	levelCount := len(s.levelKeys)
	lastLevelIdx := levelCount - 1
	for levelIdx := 0; levelIdx < len(s.levelKeys); levelIdx++ {
		s.nodePerLevel[levelIdx] = make(nodePerDomain)
	}
	for childID := range s.freeCapacityPerDomain {
		childNode := &topologyDomainNode{
			sortName: s.sortName(lastLevelIdx, childID),
			id:       childID,
		}
		s.nodePerLevel[lastLevelIdx][childID] = childNode
		parentFound := false
		var parent *topologyDomainNode
		for levelIdx := lastLevelIdx - 1; levelIdx >= 0 && !parentFound; levelIdx-- {
			parentValues := s.levelValuesPerDomain[childID][:levelIdx+1]
			parentID := utiltas.DomainID(parentValues)
			s.levelValuesPerDomain[parentID] = parentValues
			parent, parentFound = s.nodePerLevel[levelIdx][parentID]
			if !parentFound {
				parent = &topologyDomainNode{
					sortName: s.sortName(levelIdx, parentID),
					id:       parentID,
				}
				s.nodePerLevel[levelIdx][parentID] = parent
			}
			childNode.parentID = parentID
			parent.childIDs = append(parent.childIDs, childID)
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

// Algorithm overview:
// Phase 1:
//
//	determine pod counts for each topology domain. Start at the lowest level
//	and bubble up the numbers to the top level
//
// Phase 2:
//
//	a) select the domain at requested level with count >= requestedCount
//	b) traverse the structure down level-by-level optimizing the number of used
//	  domains at each level
func (s *TASFlavorSnapshot) FindTopologyAssignment(
	topologyRequest *kueue.PodSetTopologyRequest,
	requests resources.Requests,
	count int32) *kueue.TopologyAssignment {
	required := topologyRequest.Required != nil
	levelIdx, found := s.resolveLevelIdx(topologyRequest)
	if !found {
		return nil
	}
	// phase 1 - determine the number of pods which can fit in each topology domain
	s.fillInCounts(requests)

	// phase 2a: determine the level at which the assignment is done along with
	// the nodes which can fit the
	fitLevelIdx, currFitNodes := s.findLevelWithFitNodes(levelIdx, required, count)
	if len(currFitNodes) == 0 {
		return nil
	}

	// phase 2b: traverse the tree down level-by-level optimizing the number of
	// topology domains at each level
	currFitNodes = s.updateCountsToMinimum(currFitNodes, count)
	for levelIdx := fitLevelIdx; levelIdx+1 < len(s.nodePerLevel); levelIdx++ {
		lowerFitNodes := s.lowerLevelNodes(levelIdx, currFitNodes)
		sortedLowerNodes := s.sortedNodes(lowerFitNodes)
		currFitNodes = s.updateCountsToMinimum(sortedLowerNodes, count)
	}
	return s.buildAssignment(currFitNodes)
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

func (s *TASFlavorSnapshot) findLevelWithFitNodes(levelIdx int, required bool, count int32) (int, []*topologyDomainNode) {
	levelNodes := s.nodesForLevel(levelIdx)
	if len(levelNodes) == 0 {
		return 0, nil
	}
	sortedNodes := s.sortedNodes(levelNodes)
	topNode := sortedNodes[0]
	if s.state[topNode.id] < count {
		if required {
			return 0, nil
		} else if levelIdx > 0 {
			return s.findLevelWithFitNodes(levelIdx-1, required, count)
		}
		lastIdx := 0
		remainingCount := count - s.state[sortedNodes[lastIdx].id]
		for remainingCount > 0 && lastIdx < len(sortedNodes)-1 {
			lastIdx++
			remainingCount -= s.state[sortedNodes[lastIdx].id]
		}
		if remainingCount > 0 {
			return 0, nil
		}
		return 0, sortedNodes[:lastIdx+1]
	}
	return levelIdx, []*topologyDomainNode{topNode}
}

func (s *TASFlavorSnapshot) updateCountsToMinimum(nodes []*topologyDomainNode, count int32) []*topologyDomainNode {
	result := make([]*topologyDomainNode, 0)
	remainingCount := count
	for i := 0; i < len(nodes); i++ {
		node := nodes[i]
		if s.state[node.id] >= remainingCount {
			s.state[node.id] = remainingCount
			result = append(result, node)
			return result
		} else if s.state[node.id] > 0 {
			remainingCount -= s.state[node.id]
			result = append(result, node)
		}
	}
	panic("unexpected remaining count")
}

func (s *TASFlavorSnapshot) buildAssignment(nodes []*topologyDomainNode) *kueue.TopologyAssignment {
	assignment := kueue.TopologyAssignment{
		Levels:  s.levelKeys,
		Domains: make([]kueue.TopologyDomainAssignment, 0),
	}
	for i := 0; i < len(nodes); i++ {
		assignment.Domains = append(assignment.Domains, kueue.TopologyDomainAssignment{
			Values: s.asLevelValues(nodes[i].id),
			Count:  s.state[nodes[i].id],
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

func (s *TASFlavorSnapshot) lowerLevelNodes(levelIdx int, infos []*topologyDomainNode) []*topologyDomainNode {
	result := make([]*topologyDomainNode, 0, len(infos))
	for _, info := range infos {
		for _, childDomainID := range info.childIDs {
			if childDomain := s.nodePerLevel[levelIdx+1][childDomainID]; childDomain != nil {
				result = append(result, childDomain)
			}
		}
	}
	return result
}

func (s *TASFlavorSnapshot) nodesForLevel(levelIdx int) []*topologyDomainNode {
	infosMap := s.nodePerLevel[levelIdx]
	result := make([]*topologyDomainNode, len(infosMap))
	index := 0
	for _, info := range infosMap {
		result[index] = info
		index++
	}
	return result
}

func (s *TASFlavorSnapshot) sortedNodes(infos []*topologyDomainNode) []*topologyDomainNode {
	result := make([]*topologyDomainNode, len(infos))
	copy(result, infos)
	slices.SortFunc(result, func(a, b *topologyDomainNode) int {
		aCount := s.state[a.id]
		bCount := s.state[b.id]
		switch {
		case aCount == bCount:
			return strings.Compare(a.sortName, b.sortName)
		case aCount > bCount:
			return -1
		default:
			return 1
		}
	})
	return result
}

func (s *TASFlavorSnapshot) fillInCounts(requests resources.Requests) {
	for domainID, capacity := range s.freeCapacityPerDomain {
		s.state[domainID] = requests.CountIn(capacity)
	}
	lastLevelIdx := len(s.nodePerLevel) - 1
	for levelIdx := lastLevelIdx - 1; levelIdx >= 0; levelIdx-- {
		for _, info := range s.nodePerLevel[levelIdx] {
			for _, childDomainID := range info.childIDs {
				s.state[info.id] += s.state[childDomainID]
			}
		}
	}
}
