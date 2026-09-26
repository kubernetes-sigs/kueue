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
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/sets"
	corev1helpers "k8s.io/component-helpers/scheduling/corev1"
	"k8s.io/klog/v2"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	utilresource "sigs.k8s.io/kueue/pkg/util/resource"
	utiltaints "sigs.k8s.io/kueue/pkg/util/taints"
)

// maxResourcesPerFlavor mirrors the CapacityProvider API validation limit.
const maxResourcesPerFlavor = 64

// inputs is a consistent snapshot of everything the capacity computation depends on.
type inputs struct {
	// providers are all local-capacity CapacityProviders, used for overlap detection.
	providers []kueuealpha.CapacityProvider
	flavors   map[kueuealpha.ResourceFlavorReference]*kueue.ResourceFlavor
	nodes     []corev1.Node
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
// is Ready, schedulable and not being deleted, and has no scheduling taint that
// the flavor neither tolerates nor declares in nodeTaints.
//
// Flavors with no eligible nodes are omitted; Dynamic Quota Orchestration treats
// unreported resources of an orchestrated flavor as zero.
//
// Each node may count toward at most one flavor across all local-capacity
// providers. Overlaps make the result misconfigured, so that DQO keeps the last
// good quota instead of distributing double-counted capacity.
func computeCapacity(provider *kueuealpha.CapacityProvider, in *inputs) result {
	ownFlavors := make([]kueuealpha.ResourceFlavorReference, 0, len(provider.Spec.OrchestratedFlavors))
	for _, f := range provider.Spec.OrchestratedFlavors {
		ownFlavors = append(ownFlavors, f.Name)
	}

	// Validate the provider's own flavors.
	var missing []string
	for _, name := range ownFlavors {
		if _, ok := in.flavors[name]; !ok {
			missing = append(missing, string(name))
		}
	}
	if len(missing) > 0 {
		return result{misconfigured: fmt.Sprintf("ResourceFlavors not found: %s", strings.Join(missing, ", "))}
	}

	// Collect every flavor claimed by any local-capacity provider; a flavor
	// claimed twice would be reported twice.
	claimedBy := make(map[kueuealpha.ResourceFlavorReference][]string)
	for _, p := range in.providers {
		for _, f := range p.Spec.OrchestratedFlavors {
			claimedBy[f.Name] = append(claimedBy[f.Name], p.Name)
		}
	}
	for _, name := range ownFlavors {
		if owners := claimedBy[name]; len(owners) > 1 {
			slices.Sort(owners)
			return result{misconfigured: fmt.Sprintf("ResourceFlavor %q is orchestrated by multiple local-capacity CapacityProviders: %s", name, strings.Join(owners, ", "))}
		}
	}
	allFlavors := sets.KeySet(claimedBy)
	ownSet := sets.New(ownFlavors...)

	totals := make(map[kueuealpha.ResourceFlavorReference]corev1.ResourceList, len(ownFlavors))
	counted := make(map[kueuealpha.ResourceFlavorReference]int, len(ownFlavors))
	excluded := make(map[string]int)
	for i := range in.nodes {
		node := &in.nodes[i]
		var matched []kueuealpha.ResourceFlavorReference
		for _, name := range sets.List(allFlavors) {
			if rf, ok := in.flavors[name]; ok && nodeMatchesFlavor(node, rf) {
				matched = append(matched, name)
			}
		}
		if !slices.ContainsFunc(matched, ownSet.Has) {
			continue
		}
		if len(matched) > 1 {
			return result{misconfigured: fmt.Sprintf("Node %q matches multiple orchestrated ResourceFlavors: %s", node.Name, joinFlavors(matched))}
		}
		if reason := ineligibleReason(node, in.flavors[matched[0]]); reason != "" {
			excluded[reason]++
			continue
		}
		totals[matched[0]] = utilresource.MergeResourceListKeepSum(totals[matched[0]], node.Status.Allocatable)
		counted[matched[0]]++
	}

	capacity := &kueuealpha.CapacityProviderNormalizedCapacity{
		Flavors: []kueuealpha.CapacityProviderNormalizedCapacityFlavor{},
	}
	for _, name := range sets.List(ownSet) {
		resources := totals[name]
		if len(resources) == 0 {
			continue
		}
		if len(resources) > maxResourcesPerFlavor {
			return result{misconfigured: fmt.Sprintf("ResourceFlavor %q has %d resources, more than the maximum of %d", name, len(resources), maxResourcesPerFlavor)}
		}
		capacity.Flavors = append(capacity.Flavors, kueuealpha.CapacityProviderNormalizedCapacityFlavor{
			Name:      name,
			Resources: resources,
		})
	}
	return result{capacity: capacity, summary: summarize(sets.List(ownSet), counted, excluded)}
}

func nodeMatchesFlavor(node *corev1.Node, rf *kueue.ResourceFlavor) bool {
	return labels.SelectorFromSet(rf.Spec.NodeLabels).Matches(labels.Set(node.Labels))
}

// ineligibleReason returns why a node matching a flavor must not be counted, or "" if it is eligible.
func ineligibleReason(node *corev1.Node, rf *kueue.ResourceFlavor) string {
	switch {
	case !node.DeletionTimestamp.IsZero():
		return "Deleting"
	case node.Spec.Unschedulable:
		return "Unschedulable"
	case !isNodeReady(node):
		return "NotReady"
	}
	isUnexpectedSchedulingTaint := func(t *corev1.Taint) bool {
		if !utiltaints.IsSchedulingTaint(t) {
			return false
		}
		// Taints declared by the flavor are expected on its nodes: workloads
		// assigned the flavor are required to tolerate them.
		return !slices.ContainsFunc(rf.Spec.NodeTaints, func(nt corev1.Taint) bool { return nt.MatchTaint(t) })
	}
	if _, untolerated := corev1helpers.FindMatchingUntoleratedTaint(klog.Background(), node.Spec.Taints, rf.Spec.Tolerations, isUnexpectedSchedulingTaint, true); untolerated {
		return "UntoleratedTaint"
	}
	return ""
}

func isNodeReady(node *corev1.Node) bool {
	for _, c := range node.Status.Conditions {
		if c.Type == corev1.NodeReady {
			return c.Status == corev1.ConditionTrue
		}
	}
	return false
}

func joinFlavors(flavors []kueuealpha.ResourceFlavorReference) string {
	names := make([]string, len(flavors))
	for i, f := range flavors {
		names[i] = string(f)
	}
	return strings.Join(names, ", ")
}

// summarize produces e.g. "h100: 10 nodes; a100: 0 nodes; excluded: NotReady=1".
func summarize(flavors []kueuealpha.ResourceFlavorReference, counted map[kueuealpha.ResourceFlavorReference]int, excluded map[string]int) string {
	parts := make([]string, 0, len(flavors)+1)
	for _, name := range flavors {
		parts = append(parts, fmt.Sprintf("%s: %d nodes", name, counted[name]))
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
