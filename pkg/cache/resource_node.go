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
	"maps"

	corev1 "k8s.io/api/core/v1"

	"sigs.k8s.io/kueue/pkg/hierarchy"
	"sigs.k8s.io/kueue/pkg/resources"
)

type ResourceNode struct {
	// Quotas are the ResourceQuotas specified for the current
	// node.
	Quotas map[resources.FlavorResource]ResourceQuota
	// SubtreeQuota is the sum of the node's quota, as well as
	// resources available from its children, constrained by
	// LendingLimits.
	SubtreeQuota resources.FlavorResourceQuantities
	// Usage is the quantity which counts against this node's
	// SubtreeQuota. For ClusterQueues, this is simply its
	// usage. For Cohorts, this is the sum of childrens'
	// usages past childrens' guaranteedQuotas.
	Usage resources.FlavorResourceQuantities
}

func NewResourceNode() ResourceNode {
	return ResourceNode{
		Quotas:       make(map[resources.FlavorResource]ResourceQuota),
		SubtreeQuota: make(resources.FlavorResourceQuantities),
		Usage:        make(resources.FlavorResourceQuantities),
	}
}

// Clone clones the mutable field Usage, while returning copies to
// Quota and SubtreeQuota (these are replaced with new maps upon update).
func (r ResourceNode) Clone() ResourceNode {
	return ResourceNode{
		Quotas:       r.Quotas,
		SubtreeQuota: r.SubtreeQuota,
		Usage:        maps.Clone(r.Usage),
	}
}

// guaranteedQuota is the capacity which will not be lent the node's
// Cohort.
func (r ResourceNode) guaranteedQuota(fr resources.FlavorResource) int64 {
	if lendingLimit := r.Quotas[fr].LendingLimit; lendingLimit != nil {
		return max(0, r.SubtreeQuota[fr]-*lendingLimit)
	}
	return 0
}

type hierarchicalResourceNode interface {
	getResourceNode() ResourceNode

	HasParent() bool
	parentHRN() hierarchicalResourceNode
}

// available determines how much capacity remains for the current
// node, taking into account usage and BorrowingLimits. It finds remaining
// capacity which is stored locally. If the node has a parent, it
// queries the parent's capacity, limiting this amount by the borrowing
// limit - and by how much capacity the node is storing/using in its parent.
//
// This function may return a negative number in the case of
// overadmission - e.g. capacity was removed or the node moved to
// another Cohort.
func available(node hierarchicalResourceNode, fr resources.FlavorResource) int64 {
	r := node.getResourceNode()
	if !node.HasParent() {
		return r.SubtreeQuota[fr] - r.Usage[fr]
	}
	localAvailable := max(0, r.guaranteedQuota(fr)-r.Usage[fr])
	parentAvailable := available(node.parentHRN(), fr)

	if borrowingLimit := r.Quotas[fr].BorrowingLimit; borrowingLimit != nil {
		storedInParent := r.SubtreeQuota[fr] - r.guaranteedQuota(fr)
		usedInParent := max(0, r.Usage[fr]-r.guaranteedQuota(fr))
		withMaxFromParent := storedInParent - usedInParent + *borrowingLimit
		parentAvailable = min(withMaxFromParent, parentAvailable)
	}
	return localAvailable + parentAvailable
}

// potentialAvailable returns the maximum capacity available to this node,
// assuming no usage, while respecting BorrowingLimits.
func potentialAvailable(node hierarchicalResourceNode, fr resources.FlavorResource) int64 {
	r := node.getResourceNode()
	if !node.HasParent() {
		return r.SubtreeQuota[fr]
	}
	available := r.guaranteedQuota(fr) + potentialAvailable(node.parentHRN(), fr)
	if borrowingLimit := r.Quotas[fr].BorrowingLimit; borrowingLimit != nil {
		maxWithBorrowing := r.SubtreeQuota[fr] + *borrowingLimit
		available = min(maxWithBorrowing, available)
	}
	return available
}

// addUsage adds usage to the current node, and bubbles up usage to
// its Cohort when usage exceeds guaranteedQuota.
func addUsage(node hierarchicalResourceNode, fr resources.FlavorResource, val int64) {
	r := node.getResourceNode()
	localAvailable := max(0, r.guaranteedQuota(fr)-r.Usage[fr])
	r.Usage[fr] += val
	if node.HasParent() && val > localAvailable {
		deltaParentUsage := val - localAvailable
		addUsage(node.parentHRN(), fr, deltaParentUsage)
	}
}

// removeUsage removes usage from the current node, and removes usage
// past guaranteedQuota that it was storing in its Cohort.
func removeUsage(node hierarchicalResourceNode, fr resources.FlavorResource, val int64) {
	r := node.getResourceNode()
	usageStoredInParent := r.Usage[fr] - r.guaranteedQuota(fr)
	r.Usage[fr] -= val
	if usageStoredInParent <= 0 || !node.HasParent() {
		return
	}
	deltaParentUsage := min(val, usageStoredInParent)
	removeUsage(node.parentHRN(), fr, deltaParentUsage)
}

// calculateLendable aggregates capacity for resources across all
// FlavorResources.
func (r ResourceNode) calculateLendable() map[corev1.ResourceName]int64 {
	lendable := make(map[corev1.ResourceName]int64, len(r.SubtreeQuota))
	for fr, q := range r.SubtreeQuota {
		lendable[fr.Resource] += q
	}
	return lendable
}

func updateClusterQueueResourceNode(cq *clusterQueue) {
	cq.AllocatableResourceGeneration += 1
	cq.resourceNode.SubtreeQuota = make(resources.FlavorResourceQuantities, len(cq.resourceNode.Quotas))
	for fr, quota := range cq.resourceNode.Quotas {
		cq.resourceNode.SubtreeQuota[fr] = quota.Nominal
	}
}

// updateCohortTreeResources traverses the Cohort tree from the root
// to accumulate SubtreeQuota and Usage. It returns an error if the
// provided Cohort has a cycle.
func updateCohortTreeResources(cohort *cohort, cycleChecker hierarchy.CycleChecker) error {
	if cycleChecker.HasCycle(cohort) {
		return errors.New("cohort has a cycle")
	}
	updateCohortResourceNode(cohort.getRootUnsafe())
	return nil
}

// updateCohortResourceNode traverses the Cohort tree to accumulate
// SubtreeQuota and Usage. It should usually be called via
// updateCohortTree, which starts at the root and includes
// a cycle check.
func updateCohortResourceNode(cohort *cohort) {
	cohort.resourceNode.SubtreeQuota = make(resources.FlavorResourceQuantities, len(cohort.resourceNode.SubtreeQuota))
	cohort.resourceNode.Usage = make(resources.FlavorResourceQuantities, len(cohort.resourceNode.Usage))

	for fr, quota := range cohort.resourceNode.Quotas {
		cohort.resourceNode.SubtreeQuota[fr] = quota.Nominal
	}
	for _, child := range cohort.ChildCohorts() {
		updateCohortResourceNode(child)
		accumulateFromChild(cohort, child)
	}
	for _, child := range cohort.ChildCQs() {
		updateClusterQueueResourceNode(child)
		accumulateFromChild(cohort, child)
	}
}

func accumulateFromChild(parent *cohort, child hierarchicalResourceNode) {
	for fr, childQuota := range child.getResourceNode().SubtreeQuota {
		parent.resourceNode.SubtreeQuota[fr] += childQuota - child.getResourceNode().guaranteedQuota(fr)
	}
	for fr, childUsage := range child.getResourceNode().Usage {
		parent.resourceNode.Usage[fr] += max(0, childUsage-child.getResourceNode().guaranteedQuota(fr))
	}
}
