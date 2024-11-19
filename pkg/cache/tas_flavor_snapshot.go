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
	// id is the globally unique id of the domain
	id utiltas.TopologyDomainID

	// parentID is the global ID of the parent domain
	parent *domain

	// childIDs at the global IDs of the child domains
	children []*domain

	// state
	state int32

	// levelValues
	levelValues []string

	// only for leaves
	freeCapacity resources.Requests
}

type domainByID map[utiltas.TopologyDomainID]*domain

type TASFlavorSnapshot struct {
	log logr.Logger

	// topologyName indicates the name of the topology specified in the
	// ResourceFlavor spec.topologyName field.
	topologyName kueue.TopologyReference

	// levelKeys denotes the ordered list of topology keys set as label keys
	// on the Topology object
	levelKeys []string

	// leaves
	leaves domainByID

	roots domainByID

	nodes domainByID

	// domainsPerLevel stores the static tree information
	domainsPerLevel []domainByID
}

func newTASFlavorSnapshot(log logr.Logger, topologyName kueue.TopologyReference, levels []string) *TASFlavorSnapshot {
	domainsPerLevel := make([]domainByID, len(levels))
	for level := range levels {
		domainsPerLevel[level] = make(domainByID)
	}

	snapshot := &TASFlavorSnapshot{
		log:             log,
		topologyName:    topologyName,
		levelKeys:       slices.Clone(levels),
		leaves:          make(domainByID),
		nodes:           make(domainByID),
		roots:           make(domainByID),
		domainsPerLevel: domainsPerLevel,
	}
	return snapshot
}

func (s *TASFlavorSnapshot) addNode(levelValues []string, capacity resources.Requests, domainID utiltas.TopologyDomainID) {
	domain := domain{
		id:          domainID,
		levelValues: levelValues,
	}
	if _, found := s.leaves[domainID]; !found {
		s.leaves[domainID] = &domain
	}
	s.addCapacity(domainID, capacity)
}

// initialize prepares the domainsPerLevel tree structure. This structure holds
// for a given the list of topology domains with additional static and dynamic
// information. This function initializes the static information which
// represents the edges to the parent and child domains. This structure is
// reused for multiple workloads during a single scheduling cycle.
func (s *TASFlavorSnapshot) initialize() {
	for _, node := range s.leaves {
		s.nodes[node.id] = node
		s.domainsPerLevel[len(node.levelValues)-1][node.id] = node
		s.initializeHelper(node)
	}
}

// helper
func (s *TASFlavorSnapshot) initializeHelper(dom *domain) {
	if len(dom.levelValues) == 1 {
		s.roots[dom.id] = dom
		return
	}
	parentValues := dom.levelValues[:len(dom.levelValues)-1]
	parentID := utiltas.DomainID(parentValues)
	parent, parentFound := s.nodes[parentID]
	if !parentFound {
		// create parent
		parent = &domain{
			id:          parentID,
			levelValues: parentValues,
		}
		s.domainsPerLevel[len(parentValues)-1][parentID] = parent
		s.nodes[parentID] = parent
	}
	// connect parent and child
	dom.parent = parent
	parent.children = append(parent.children, dom)
	s.initializeHelper(parent)
}

func (s *TASFlavorSnapshot) addCapacity(domainID utiltas.TopologyDomainID, capacity resources.Requests) {
	if s.leaves[domainID].freeCapacity == nil {
		s.leaves[domainID].freeCapacity = resources.Requests{}
	}
	s.leaves[domainID].freeCapacity.Add(capacity)
}

func (s *TASFlavorSnapshot) addUsage(domainID utiltas.TopologyDomainID, usage resources.Requests) {
	s.leaves[domainID].freeCapacity.Sub(usage)
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
	if topDomain.state < count {
		if required {
			return 0, nil, s.notFitMessage(topDomain.state, count)
		}
		if levelIdx > 0 {
			return s.findLevelWithFitDomains(levelIdx-1, required, count)
		}
		lastIdx := 0
		remainingCount := count - sortedDomain[lastIdx].state
		for remainingCount > 0 && lastIdx < len(sortedDomain)-1 && sortedDomain[lastIdx].state > 0 {
			lastIdx++
			remainingCount -= sortedDomain[lastIdx].state
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
		if domain.state >= remainingCount {
			domain.state = remainingCount
			result = append(result, domain)
			return result
		}
		remainingCount -= domain.state
		result = append(result, domain)
	}
	s.log.Error(errCodeAssumptionsViolated, "unexpected remainingCount",
		"remainingCount", remainingCount,
		"count", count,
		"leaves", s.leaves)
	return nil
}

func (s *TASFlavorSnapshot) buildAssignment(domains []*domain) *kueue.TopologyAssignment {
	assignment := kueue.TopologyAssignment{
		Levels:  s.levelKeys,
		Domains: make([]kueue.TopologyDomainAssignment, 0),
	}
	for _, domain := range domains {
		assignment.Domains = append(assignment.Domains, kueue.TopologyDomainAssignment{
			Values: domain.levelValues,
			Count:  domain.state,
		})
	}
	return &assignment
}

func (s *TASFlavorSnapshot) lowerLevelDomains(levelIdx int, domains []*domain) []*domain {
	result := make([]*domain, 0, len(domains))
	for _, domain := range domains {
		result = append(result, domain.children...)
	}
	return result
}

func (s *TASFlavorSnapshot) sortedDomains(domains []*domain) []*domain {
	result := make([]*domain, len(domains))
	copy(result, domains)
	slices.SortFunc(result, func(a, b *domain) int {
		switch {
		case a.state == b.state:
			return strings.Compare(string(a.id), string(b.id))
		case a.state > b.state:
			return -1
		default:
			return 1
		}
	})
	return result
}

func (s *TASFlavorSnapshot) fillInCounts(requests resources.Requests) {
	for _, node := range s.nodes {
		// cleanup the state in case some remaining values are present from computing
		// assignments for previous PodSets.
		node.state = 0
	}
	for _, leaf := range s.leaves {
		leaf.state = requests.CountIn(leaf.freeCapacity)
	}
	for _, root := range s.roots {
		root.state = s.fillInCountsHelper(root)
	}
}

func (s *TASFlavorSnapshot) fillInCountsHelper(node *domain) int32 {
	// logic for a leaf
	if len(node.children) == 0 {
		return node.state
	}
	// logic for a parent
	childrenCapacity := int32(0)
	for _, child := range node.children {
		childrenCapacity += s.fillInCountsHelper(child)
	}
	node.state = childrenCapacity
	return childrenCapacity
}

func (s *TASFlavorSnapshot) notFitMessage(fitCount, totalCount int32) string {
	if fitCount == 0 {
		return fmt.Sprintf("topology %q doesn't allow to fit any of %v pod(s)", s.topologyName, totalCount)
	}
	return fmt.Sprintf("topology %q allows to fit only %v out of %v pod(s)", s.topologyName, fitCount, totalCount)
}
