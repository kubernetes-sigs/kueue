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
	"errors"
	"fmt"
	"slices"
	"strings"

	"github.com/go-logr/logr"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/resources"
	utilmaps "sigs.k8s.io/kueue/pkg/util/maps"
	utiltas "sigs.k8s.io/kueue/pkg/util/tas"
)

var (
	errCodeAssumptionsViolated = errors.New("code assumptions violated")
)

// domain holds the static information about placement of a topology
// domain in the hierarchy of topology domains.
type domain struct {
	// sortName indicates name used for sorting when two domains can fit
	// the same number of pods.
	// Example for domain corresponding to "rack2" in "block1" the value is
	// "rack2 <domainID>" (where domainID is "block1,rack2").
	sortName string

	// id is the globally unique id of the domain
	id utiltas.TopologyDomainID

	// parentID is the global ID of the parent domain
	parentID utiltas.TopologyDomainID

	// childIDs at the global IDs of the child domains
	childIDs []utiltas.TopologyDomainID
}

type domainByID map[utiltas.TopologyDomainID]*domain
type statePerDomain map[utiltas.TopologyDomainID]int32

type TASFlavorSnapshot struct {
	log logr.Logger

	// topologyName indicates the name of the topology specified in the
	// ResourceFlavor spec.topologyName field.
	topologyName string

	// levelKeys denotes the ordered list of topology keys set as label keys
	// on the Topology object
	levelKeys []string

	// freeCapacityPerLeafDomain stores the free capacity per domain, only for the
	// lowest level of topology
	freeCapacityPerLeafDomain map[utiltas.TopologyDomainID]resources.Requests

	// levelValuesPerDomain stores the mapping from domain ID back to the
	// ordered list of values. It stores the information for all levels.
	levelValuesPerDomain map[utiltas.TopologyDomainID][]string

	// domainsPerLevel stores the static tree information
	domainsPerLevel []domainByID

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

func newTASFlavorSnapshot(log logr.Logger, topologyName string, levels []string) *TASFlavorSnapshot {
	snapshot := &TASFlavorSnapshot{
		log:                       log,
		topologyName:              topologyName,
		levelKeys:                 slices.Clone(levels),
		freeCapacityPerLeafDomain: make(map[utiltas.TopologyDomainID]resources.Requests),
		levelValuesPerDomain:      make(map[utiltas.TopologyDomainID][]string),
		domainsPerLevel:           make([]domainByID, len(levels)),
		state:                     make(statePerDomain),
	}
	return snapshot
}

// initialize prepares the domainsPerLevel tree structure. This structure holds
// for a given the list of topology domains with additional static and dynamic
// information. This function initializes the static information which
// represents the edges to the parent and child domains. This structure is
// reused for multiple workloads during a single scheduling cycle.
func (s *TASFlavorSnapshot) initialize() {
	levelCount := len(s.levelKeys)
	lastLevelIdx := levelCount - 1
	for levelIdx := range s.levelKeys {
		s.domainsPerLevel[levelIdx] = make(domainByID)
	}
	for childID := range s.freeCapacityPerLeafDomain {
		childDomain := &domain{
			sortName: s.sortName(lastLevelIdx, childID),
			id:       childID,
		}
		s.domainsPerLevel[lastLevelIdx][childID] = childDomain
		parentFound := false
		var parent *domain
		for levelIdx := lastLevelIdx - 1; levelIdx >= 0 && !parentFound; levelIdx-- {
			parentValues := s.levelValuesPerDomain[childID][:levelIdx+1]
			parentID := utiltas.DomainID(parentValues)
			s.levelValuesPerDomain[parentID] = parentValues
			parent, parentFound = s.domainsPerLevel[levelIdx][parentID]
			if !parentFound {
				parent = &domain{
					sortName: s.sortName(levelIdx, parentID),
					id:       parentID,
				}
				s.domainsPerLevel[levelIdx][parentID] = parent
			}
			childDomain.parentID = parentID
			parent.childIDs = append(parent.childIDs, childID)
			childID = parentID
		}
	}
}

func (s *TASFlavorSnapshot) sortName(levelIdx int, domainID utiltas.TopologyDomainID) string {
	levelValues := s.levelValuesPerDomain[domainID]
	if len(levelValues) <= levelIdx {
		s.log.Error(errCodeAssumptionsViolated, "invalid invocation of sortName",
			"count", len(levelValues),
			"levelIdx", levelIdx,
			"domainID", domainID,
			"levelValuesPerDomain", s.levelValuesPerDomain,
			"freeCapacityPerDomain", s.freeCapacityPerLeafDomain)
	}
	// we prefix with the node label value to make it ordered naturally, but
	// append also domain ID as the node label value may not be globally unique.
	return s.levelValuesPerDomain[domainID][levelIdx] + " " + string(domainID)
}

func (s *TASFlavorSnapshot) addCapacity(domainID utiltas.TopologyDomainID, capacity resources.Requests) {
	s.initializeFreeCapacityPerDomain(domainID)
	s.freeCapacityPerLeafDomain[domainID].Add(capacity)
}

func (s *TASFlavorSnapshot) addUsage(domainID utiltas.TopologyDomainID, usage resources.Requests) {
	s.initializeFreeCapacityPerDomain(domainID)
	s.freeCapacityPerLeafDomain[domainID].Sub(usage)
}

func (s *TASFlavorSnapshot) initializeFreeCapacityPerDomain(domainID utiltas.TopologyDomainID) {
	if _, found := s.freeCapacityPerLeafDomain[domainID]; !found {
		s.freeCapacityPerLeafDomain[domainID] = resources.Requests{}
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
//	c) build the assignment for the lowest level in the hierarchy
func (s *TASFlavorSnapshot) FindTopologyAssignment(
	topologyRequest *kueue.PodSetTopologyRequest,
	requests resources.Requests,
	count int32) (*kueue.TopologyAssignment, string) {
	required := topologyRequest.Required != nil
	key := levelKey(topologyRequest)
	if key == nil {
		return nil, "topology level not specified"
	}
	levelIdx, found := s.resolveLevelIdx(*key)
	if !found {
		return nil, fmt.Sprintf("no requested topology level: %s", *key)
	}
	// phase 1 - determine the number of pods which can fit in each topology domain
	s.fillInCounts(requests)

	// phase 2a: determine the level at which the assignment is done along with
	// the domains which can accommodate all pods
	fitLevelIdx, currFitDomain, reason := s.findLevelWithFitDomains(levelIdx, required, count)
	if len(reason) > 0 {
		return nil, reason
	}

	// phase 2b: traverse the tree down level-by-level optimizing the number of
	// topology domains at each level
	currFitDomain = s.updateCountsToMinimum(currFitDomain, count)
	for levelIdx := fitLevelIdx; levelIdx+1 < len(s.domainsPerLevel); levelIdx++ {
		lowerFitDomains := s.lowerLevelDomains(levelIdx, currFitDomain)
		sortedLowerDomains := s.sortedDomains(lowerFitDomains)
		currFitDomain = s.updateCountsToMinimum(sortedLowerDomains, count)
	}
	return s.buildAssignment(currFitDomain), ""
}

func (s *TASFlavorSnapshot) HasLevel(r *kueue.PodSetTopologyRequest) bool {
	key := levelKey(r)
	if key == nil {
		return false
	}
	_, found := s.resolveLevelIdx(*key)
	return found
}

func (s *TASFlavorSnapshot) resolveLevelIdx(levelKey string) (int, bool) {
	levelIdx := slices.Index(s.levelKeys, levelKey)
	if levelIdx == -1 {
		return levelIdx, false
	}
	return levelIdx, true
}

func levelKey(topologyRequest *kueue.PodSetTopologyRequest) *string {
	if topologyRequest.Required != nil {
		return topologyRequest.Required
	} else if topologyRequest.Preferred != nil {
		return topologyRequest.Preferred
	}
	return nil
}

func (s *TASFlavorSnapshot) findLevelWithFitDomains(levelIdx int, required bool, count int32) (int, []*domain, string) {
	domains := s.domainsPerLevel[levelIdx]
	if len(domains) == 0 {
		return 0, nil, fmt.Sprintf("no topology domains at level: %s", s.levelKeys[levelIdx])
	}
	levelDomains := utilmaps.Values(domains)
	sortedDomain := s.sortedDomains(levelDomains)
	topDomain := sortedDomain[0]
	if s.state[topDomain.id] < count {
		if required {
			return 0, nil, s.notFitMessage(s.state[topDomain.id], count)
		}
		if levelIdx > 0 {
			return s.findLevelWithFitDomains(levelIdx-1, required, count)
		}
		lastIdx := 0
		remainingCount := count - s.state[sortedDomain[lastIdx].id]
		for remainingCount > 0 && lastIdx < len(sortedDomain)-1 && s.state[sortedDomain[lastIdx].id] > 0 {
			lastIdx++
			remainingCount -= s.state[sortedDomain[lastIdx].id]
		}
		if remainingCount > 0 {
			return 0, nil, s.notFitMessage(count-remainingCount, count)
		}
		return 0, sortedDomain[:lastIdx+1], ""
	}
	return levelIdx, []*domain{topDomain}, ""
}

func (s *TASFlavorSnapshot) updateCountsToMinimum(domains []*domain, count int32) []*domain {
	result := make([]*domain, 0)
	remainingCount := count
	for _, domain := range domains {
		if s.state[domain.id] >= remainingCount {
			s.state[domain.id] = remainingCount
			result = append(result, domain)
			return result
		}
		remainingCount -= s.state[domain.id]
		result = append(result, domain)
	}
	s.log.Error(errCodeAssumptionsViolated, "unexpected remainingCount",
		"remainingCount", remainingCount,
		"count", count,
		"levelValuesPerDomain", s.levelValuesPerDomain,
		"freeCapacityPerDomain", s.freeCapacityPerLeafDomain)
	return nil
}

func (s *TASFlavorSnapshot) buildAssignment(domains []*domain) *kueue.TopologyAssignment {
	assignment := kueue.TopologyAssignment{
		Levels:  s.levelKeys,
		Domains: make([]kueue.TopologyDomainAssignment, 0),
	}
	for _, domain := range domains {
		assignment.Domains = append(assignment.Domains, kueue.TopologyDomainAssignment{
			Values: s.levelValuesPerDomain[domain.id],
			Count:  s.state[domain.id],
		})
	}
	return &assignment
}

func (s *TASFlavorSnapshot) lowerLevelDomains(levelIdx int, domains []*domain) []*domain {
	result := make([]*domain, 0, len(domains))
	for _, info := range domains {
		for _, childDomainID := range info.childIDs {
			childDomain := s.domainsPerLevel[levelIdx+1][childDomainID]
			result = append(result, childDomain)
		}
	}
	return result
}

func (s *TASFlavorSnapshot) sortedDomains(domains []*domain) []*domain {
	result := make([]*domain, len(domains))
	copy(result, domains)
	slices.SortFunc(result, func(a, b *domain) int {
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
	for domainID, capacity := range s.freeCapacityPerLeafDomain {
		s.state[domainID] = requests.CountIn(capacity)
	}
	lastLevelIdx := len(s.domainsPerLevel) - 1
	for levelIdx := lastLevelIdx - 1; levelIdx >= 0; levelIdx-- {
		for _, info := range s.domainsPerLevel[levelIdx] {
			for _, childDomainID := range info.childIDs {
				s.state[info.id] += s.state[childDomainID]
			}
		}
	}
}

func (s *TASFlavorSnapshot) notFitMessage(fitCount, totalCount int32) string {
	if fitCount == 0 {
		return fmt.Sprintf("topology %q doesn't allow to fit any of %v pod(s)", s.topologyName, totalCount)
	}
	return fmt.Sprintf("topology %q allows to fit only %v out of %v pod(s)", s.topologyName, fitCount, totalCount)
}
