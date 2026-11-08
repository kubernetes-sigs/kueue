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

package localcapacity

import (
	"fmt"
	"maps"
	"slices"
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/sets"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	utilresource "sigs.k8s.io/kueue/pkg/util/resource"
	utiltas "sigs.k8s.io/kueue/pkg/util/tas"
)

// maxResourcesPerFlavor mirrors the CapacityProvider API validation limit.
// The provider reports every allocatable resource of a flavor's nodes, so nodes
// that advertise more than this many resources make the provider Misconfigured.
const maxResourcesPerFlavor = 64

// inputs is a consistent snapshot of everything the capacity computation depends on.
type inputs struct {
	// providers are all local-capacity CapacityProviders, used for overlap detection.
	providers []kueuealpha.CapacityProvider
	flavors   map[kueuealpha.ResourceFlavorReference]*kueue.ResourceFlavor
	// topologyLevels are the node labels of each Topology's levels.
	topologyLevels map[kueue.TopologyReference][]string
	nodes          []corev1.Node
}

type result struct {
	// capacity is the capacity to publish; only meaningful when misconfigured is empty.
	capacity *kueuealpha.CapacityProviderNormalizedCapacity
	// summary is a human-readable description of counted and excluded nodes.
	summary string
	// misconfigured, when non-empty, explains why capacity cannot be published.
	misconfigured string
}

// computeCapacity sums the allocatable resources of eligible nodes for each
// flavor orchestrated by the provider.
//
// A node counts toward a flavor when it matches all of the flavor's nodeLabels,
// has a label for every level of the flavor's Topology, if any, and is Ready,
// schedulable and not being deleted. Taints are not considered, because
// workloads may tolerate them on their own.
//
// Every orchestrated flavor is published, with empty resources when it has no
// eligible nodes; Dynamic Quota Orchestration treats unreported resources of an
// orchestrated flavor as zero.
//
// Each node may count toward at most one flavor across all local-capacity
// providers. Overlaps make the result misconfigured, so that DQO keeps the last
// good quota instead of distributing double-counted capacity.
func computeCapacity(provider *kueuealpha.CapacityProvider, in *inputs) result {
	// spec.parameters is reserved for a later stage. Rejecting it now keeps objects
	// created with arbitrary parameters from changing meaning after an upgrade.
	if provider.Spec.Parameters != nil {
		return result{misconfigured: fmt.Sprintf("spec.parameters is not supported by %s", ControllerName)}
	}

	ownFlavors := orchestratedFlavorNames(provider)
	if msg := validateOrchestratedFlavors(ownFlavors, in); msg != "" {
		return result{misconfigured: msg}
	}
	claimedBy := flavorClaims(in.providers)
	if msg := checkFlavorsClaimedOnce(ownFlavors, claimedBy); msg != "" {
		return result{misconfigured: msg}
	}

	ownSorted := sets.List(sets.New(ownFlavors...))
	sums, msg := sumEligibleNodes(ownSorted, sets.List(sets.KeySet(claimedBy)), in)
	if msg != "" {
		return result{misconfigured: msg}
	}
	capacity, msg := buildCapacity(ownSorted, sums.totals)
	if msg != "" {
		return result{misconfigured: msg}
	}
	return result{capacity: capacity, summary: summarize(ownSorted, sums.counted, sums.excluded)}
}

func orchestratedFlavorNames(provider *kueuealpha.CapacityProvider) []kueuealpha.ResourceFlavorReference {
	names := make([]kueuealpha.ResourceFlavorReference, 0, len(provider.Spec.OrchestratedFlavors))
	for _, f := range provider.Spec.OrchestratedFlavors {
		names = append(names, f.Name)
	}
	return names
}

// validateOrchestratedFlavors returns why the flavors cannot be used, or "" if
// every flavor exists and its Topology, if any, exists.
func validateOrchestratedFlavors(names []kueuealpha.ResourceFlavorReference, in *inputs) string {
	var missing []string
	for _, name := range names {
		if _, ok := in.flavors[name]; !ok {
			missing = append(missing, string(name))
		}
	}
	if len(missing) > 0 {
		return fmt.Sprintf("ResourceFlavors not found: %s", strings.Join(missing, ", "))
	}
	for _, name := range names {
		if topology := in.flavors[name].Spec.TopologyName; topology != nil {
			if _, ok := in.topologyLevels[*topology]; !ok {
				return fmt.Sprintf("Topology %q of ResourceFlavor %q not found", *topology, name)
			}
		}
	}
	return ""
}

// flavorClaims returns the names of the providers that orchestrate each flavor.
func flavorClaims(providers []kueuealpha.CapacityProvider) map[kueuealpha.ResourceFlavorReference][]string {
	claimedBy := make(map[kueuealpha.ResourceFlavorReference][]string)
	for _, p := range providers {
		for _, f := range p.Spec.OrchestratedFlavors {
			claimedBy[f.Name] = append(claimedBy[f.Name], p.Name)
		}
	}
	return claimedBy
}

// checkFlavorsClaimedOnce returns why the flavors are claimed by several
// providers, or "" if they are not. A flavor claimed twice would be reported twice.
func checkFlavorsClaimedOnce(names []kueuealpha.ResourceFlavorReference, claimedBy map[kueuealpha.ResourceFlavorReference][]string) string {
	for _, name := range names {
		if owners := claimedBy[name]; len(owners) > 1 {
			slices.Sort(owners)
			return fmt.Sprintf("ResourceFlavor %q is orchestrated by multiple local-capacity CapacityProviders: %s", name, strings.Join(owners, ", "))
		}
	}
	return ""
}

type nodeSums struct {
	// totals is the sum of the allocatable resources of the counted nodes of each flavor.
	totals map[kueuealpha.ResourceFlavorReference]corev1.ResourceList
	// counted is the number of counted nodes of each flavor.
	counted map[kueuealpha.ResourceFlavorReference]int
	// excluded is the number of nodes of the flavors that are not counted, by reason.
	excluded map[string]int
}

// sumEligibleNodes sums the allocatable resources of the eligible nodes of the
// own flavors. allFlavors are all flavors orchestrated by any provider, so that
// a node matching an own flavor and another orchestrated flavor is an overlap.
// It returns why the nodes cannot be summed, or "" if they can.
func sumEligibleNodes(own, allFlavors []kueuealpha.ResourceFlavorReference, in *inputs) (nodeSums, string) {
	ownSet := sets.New(own...)
	sums := nodeSums{
		totals:   make(map[kueuealpha.ResourceFlavorReference]corev1.ResourceList, len(own)),
		counted:  make(map[kueuealpha.ResourceFlavorReference]int, len(own)),
		excluded: make(map[string]int),
	}
	for i := range in.nodes {
		node := &in.nodes[i]
		var matched []kueuealpha.ResourceFlavorReference
		for _, name := range allFlavors {
			if rf, ok := in.flavors[name]; ok && nodeMatchesFlavor(node, rf, in.topologyLevels) {
				matched = append(matched, name)
			}
		}
		if !ownSet.HasAny(matched...) {
			continue
		}
		// Overlap is checked before eligibility on purpose: overlapping nodeLabels
		// are a configuration error whatever the node's current health.
		if len(matched) > 1 {
			return nodeSums{}, fmt.Sprintf("Node %q matches multiple orchestrated ResourceFlavors: %s", node.Name, joinFlavors(matched))
		}
		if reason := ineligibleReason(node); reason != "" {
			sums.excluded[reason]++
			continue
		}
		sums.totals[matched[0]] = utilresource.MergeResourceListKeepSum(sums.totals[matched[0]], node.Status.Allocatable)
		sums.counted[matched[0]]++
	}
	return sums, ""
}

// buildCapacity returns the capacity with a flavor, possibly without resources,
// for each of the sorted flavors. It returns why the capacity cannot be published, or "" if it can.
func buildCapacity(sorted []kueuealpha.ResourceFlavorReference, totals map[kueuealpha.ResourceFlavorReference]corev1.ResourceList) (*kueuealpha.CapacityProviderNormalizedCapacity, string) {
	capacity := &kueuealpha.CapacityProviderNormalizedCapacity{
		Flavors: []kueuealpha.CapacityProviderNormalizedCapacityFlavor{},
	}
	for _, name := range sorted {
		resources := totals[name]
		if resources == nil {
			resources = corev1.ResourceList{}
		}
		if len(resources) > maxResourcesPerFlavor {
			return nil, fmt.Sprintf("ResourceFlavor %q has %d resources, more than the maximum of %d", name, len(resources), maxResourcesPerFlavor)
		}
		capacity.Flavors = append(capacity.Flavors, kueuealpha.CapacityProviderNormalizedCapacityFlavor{
			Name:      name,
			Resources: resources,
		})
	}
	return capacity, ""
}

// nodeMatchesFlavor uses the same rule as TAS: a node of a TAS flavor must also
// have a label for every level of the flavor's Topology.
// A flavor whose Topology is missing matches no node. Its own provider reports
// it as misconfigured, so it must not cause overlaps for other providers.
func nodeMatchesFlavor(node *corev1.Node, rf *kueue.ResourceFlavor, topologyLevels map[kueue.TopologyReference][]string) bool {
	var levels []string
	if rf.Spec.TopologyName != nil {
		var ok bool
		if levels, ok = topologyLevels[*rf.Spec.TopologyName]; !ok {
			return false
		}
	}
	return utiltas.NodeMatchesFlavor(node.Labels, rf.Spec.NodeLabels, levels)
}

// ineligibleReason returns why a node matching a flavor must not be counted, or "" if it is eligible.
func ineligibleReason(node *corev1.Node) string {
	switch {
	case !node.DeletionTimestamp.IsZero():
		return "Deleting"
	case node.Spec.Unschedulable:
		return "Unschedulable"
	case !utiltas.IsNodeStatusConditionTrue(node.Status.Conditions, corev1.NodeReady):
		return "NotReady"
	}
	return ""
}

func joinFlavors(flavors []kueuealpha.ResourceFlavorReference) string {
	names := make([]string, len(flavors))
	for i, f := range flavors {
		names[i] = string(f)
	}
	return strings.Join(names, ", ")
}

// summarize produces e.g. "h100: 10 nodes; a100: 1 node; excluded: NotReady=1".
func summarize(flavors []kueuealpha.ResourceFlavorReference, counted map[kueuealpha.ResourceFlavorReference]int, excluded map[string]int) string {
	parts := make([]string, 0, len(flavors)+1)
	for _, name := range flavors {
		parts = append(parts, fmt.Sprintf("%s: %s", name, pluralizeNodes(counted[name])))
	}
	if len(excluded) > 0 {
		reasons := make([]string, 0, len(excluded))
		for _, reason := range slices.Sorted(maps.Keys(excluded)) {
			reasons = append(reasons, fmt.Sprintf("%s=%d", reason, excluded[reason]))
		}
		parts = append(parts, "excluded: "+strings.Join(reasons, ", "))
	}
	return strings.Join(parts, "; ")
}

func pluralizeNodes(n int) string {
	if n == 1 {
		return "1 node"
	}
	return fmt.Sprintf("%d nodes", n)
}
