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
	"slices"

	corev1 "k8s.io/api/core/v1"

	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/resources"
	utiltas "sigs.k8s.io/kueue/pkg/util/tas"
)

// domain holds the static information about placement of a topology
// domain in the hierarchy of topology domains.
// Its per-snapshot mutable state lives in TASFlavorSnapshot.domainStates,
// addressed by idx.
type domain struct {
	// id identifies the domain within its topology level
	id utiltas.TopologyDomainID

	// idx is the dense index of the domain within its topologyTree, used to
	// address the per-snapshot state in TASFlavorSnapshot.domainStates.
	idx int

	// parent points to domain which is a parent in topology structure
	parent *domain

	// children points to domains which are children in topology structure
	children []*domain

	// levelValues stores the mapping from domain ID back to the
	// ordered list of values
	levelValues []string
}

// leafDomain extends the domain with static information for the lowest-level
// domain. Its per-snapshot mutable capacity data lives in
// TASFlavorSnapshot.leafCapacities, addressed by leafIdx.
type leafDomain struct {
	domain

	// leafIdx is the dense index of the leaf within its topologyTree, used
	// to address the per-snapshot capacity data in TASFlavorSnapshot.leafCapacities.
	leafIdx int

	// capacity is the static capacity of the leaf: the summed allocatable of
	// its nodes, before subtracting any usage.
	capacity resources.Requests

	// node at the leaf, if the lowest level is a node
	node *corev1.Node
}

type domainByID map[utiltas.TopologyDomainID]*domain
type leafDomainByID map[utiltas.TopologyDomainID]*leafDomain

// topologyTree holds the static topology structure derived from the node set
// at a given nodesCache generation: the domain hierarchy, the per-leaf static
// capacity and the node membership. It is built once per generation, cached
// on the TASFlavorCache, and shared read-only by every snapshot created from it.
// All mutable per-cycle values live on the TASFlavorSnapshot instead, so
// concurrent snapshots never share mutable state.
type topologyTree struct {
	// generation is the nodesCache generation the tree was built from.
	generation int64

	// levelKeys denotes the ordered list of topology keys set as label keys
	// on the Topology object
	levelKeys []string

	// leaves maps domainID to domains that are at the lowest level of topology structure
	leaves leafDomainByID

	// roots maps domainID to domains that are at the highest level of topology structure
	roots domainByID

	// domainCount is the number of allocated domains. Domain IDs are not unique
	// across levels, so it counts domains rather than distinct IDs.
	domainCount int

	// domainsPerLevel stores the static tree information
	domainsPerLevel []domainByID

	// virtualHostname is true when kubernetes.io/hostname was appended to the
	// levels because the Topology did not declare it, which happens only with
	// the TASNodeFeasibilityForAllLevels feature gate enabled. Every leaf is
	// then exactly one node, and the appended level is excluded from
	// topologyAssignment serialization.
	virtualHostname bool

	// nodes lists the nodes matching the flavor which the tree was built
	// from.
	nodes []*corev1.Node

	// nodeToDomain maps node names to their leaf domain ID, used to apply
	// per-node non-TAS usage onto snapshots.
	nodeToDomain map[string]utiltas.TopologyDomainID
}

// newTopologyTree builds the static topology structure from the given node
// set, read at the given nodesCache generation.
func newTopologyTree(levels []string, nodes []*corev1.Node, generation int64) *topologyTree {
	// Injecting the hostname level makes every leaf exactly one node, so
	// capacity and feasibility are evaluated per node instead of per declared
	// domain. Without the gate the lowest declared level stays the leaf.
	virtual := features.Enabled(features.TASNodeFeasibilityForAllLevels) &&
		(len(levels) == 0 || levels[len(levels)-1] != corev1.LabelHostname)
	if virtual {
		levels = append(slices.Clone(levels), corev1.LabelHostname)
	}

	domainsPerLevel := make([]domainByID, len(levels))
	for level := range levels {
		domainsPerLevel[level] = make(domainByID)
	}

	tree := &topologyTree{
		generation:      generation,
		levelKeys:       slices.Clone(levels),
		leaves:          make(leafDomainByID),
		roots:           make(domainByID),
		domainsPerLevel: domainsPerLevel,
		virtualHostname: virtual,
		nodes:           slices.Clip(slices.Clone(nodes)),
		nodeToDomain:    make(map[string]utiltas.TopologyDomainID, len(nodes)),
	}
	for _, node := range nodes {
		tree.nodeToDomain[node.Name] = tree.addNode(node)
	}
	tree.initialize()
	return tree
}

func (t *topologyTree) addNode(node *corev1.Node) utiltas.TopologyDomainID {
	levelValues := utiltas.LevelValues(t.levelKeys, node.Labels)
	var domainID utiltas.TopologyDomainID
	switch {
	case t.virtualHostname:
		// The injected level is internal, so key it by node name, which is
		// unique. Hostname labels are neither unique nor immutable, and two
		// nodes sharing one must not merge into a leaf with pooled capacity.
		levelValues[len(levelValues)-1] = node.Name
		domainID = utiltas.TopologyDomainID(node.Name)
	case t.leafIsNode():
		// A declared hostname level keeps the label, the user's contract.
		domainID = utiltas.TopologyDomainID(node.Labels[corev1.LabelHostname])
	default:
		domainID = utiltas.DomainID(levelValues)
	}
	if _, leafFound := t.leaves[domainID]; !leafFound {
		leaf := &leafDomain{
			domain:  domain{id: domainID, levelValues: levelValues},
			leafIdx: len(t.leaves),
		}
		if t.leafIsNode() {
			leaf.node = node
		}
		t.leaves[domainID] = leaf
	}
	capacity := resources.NewRequestsFromResourceList(node.Status.Allocatable)
	t.addCapacity(domainID, capacity)
	return domainID
}

// leafIsNode reports whether every leaf of the tree is exactly one node. That
// holds when the Topology declares kubernetes.io/hostname as its lowest level,
// and when the level was injected.
func (t *topologyTree) leafIsNode() bool {
	return len(t.levelKeys) > 0 && t.levelKeys[len(t.levelKeys)-1] == corev1.LabelHostname
}

// declaresHostnameLevel reports whether the Topology itself names the hostname
// level, as opposed to one having been injected.
func (t *topologyTree) declaresHostnameLevel() bool {
	return t.leafIsNode() && !t.virtualHostname
}

// explicitLowestLevel returns the lowest level the Topology CR declares,
// excluding the hostname level when that was injected.
func (t *topologyTree) explicitLowestLevel() string {
	if t.virtualHostname {
		return t.levelKeys[len(t.levelKeys)-2]
	}
	return t.levelKeys[len(t.levelKeys)-1]
}

func (t *topologyTree) highestLevel() string {
	return t.levelKeys[0]
}

func (t *topologyTree) indexDomain(dom *domain) {
	dom.idx = t.domainCount
	t.domainCount++
}

// initialize prepares the topology tree structure. This structure holds
// for a given the list of topology domains with additional static
// information. This function initializes the static information which
// represents the edges to the parent and child domains, and assigns each
// domain its dense index used to address per-snapshot state. This structure
// is reused by all snapshots until the node set changes.
func (t *topologyTree) initialize() {
	for _, leafDomain := range t.leaves {
		domain := &leafDomain.domain
		t.indexDomain(domain)
		t.domainsPerLevel[len(domain.levelValues)-1][domain.id] = domain
		t.initializeHelper(domain)
	}
}

// initializeHelper is a recursive helper for initialize() method
func (t *topologyTree) initializeHelper(dom *domain) {
	if len(dom.levelValues) == 1 {
		t.roots[dom.id] = dom
		return
	}
	parentValues := dom.levelValues[:len(dom.levelValues)-1]
	parentID := utiltas.DomainID(parentValues)
	parent, parentFound := t.domainsPerLevel[len(parentValues)-1][parentID]
	if !parentFound {
		// create parent
		parent = &domain{id: parentID, levelValues: parentValues}
		t.indexDomain(parent)

		t.domainsPerLevel[len(parentValues)-1][parentID] = parent
		t.initializeHelper(parent)
	}
	// connect parent and child
	dom.parent = parent
	parent.children = append(parent.children, dom)
}

func (t *topologyTree) addCapacity(domainID utiltas.TopologyDomainID, capacity resources.Requests) {
	if t.leaves[domainID].capacity == nil {
		t.leaves[domainID].capacity = resources.NewRequests()
	}
	t.leaves[domainID].capacity.Add(capacity)
}
