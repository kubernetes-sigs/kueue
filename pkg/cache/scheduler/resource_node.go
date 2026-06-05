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

	"k8s.io/apimachinery/pkg/util/sets"

	"sigs.k8s.io/kueue/pkg/cache/hierarchy"
	"sigs.k8s.io/kueue/pkg/resources"
)

// resourceNode is the shared representation of Quotas and Usage, used
// by ClusterQueues and Cohorts.
type resourceNode struct {
	// Quotas are the ResourceQuotas specified for the current
	// node.
	Quotas map[resources.FlavorResource]ResourceQuota
	// SubtreeQuota is the sum of the node's quota, as well as
	// resources available from its children, constrained by
	// LendingLimits. It uses Amount because individual quotas (or their
	// aggregation) may saturate to Unlimited.
	SubtreeQuota resources.FlavorResourceQuantities
	// Usage is the quantity which counts against this node's
	// SubtreeQuota. For ClusterQueues, this is simply its
	// usage. For Cohorts, this is the sum of childrens'
	// usages past childrens' localQuota.
	Usage resources.FlavorResourceQuantities
}

func NewResourceNode() resourceNode {
	return resourceNode{
		Quotas:       make(map[resources.FlavorResource]ResourceQuota),
		SubtreeQuota: make(resources.FlavorResourceQuantities),
		Usage:        make(resources.FlavorResourceQuantities),
	}
}

// Clone clones the mutable field Usage, while returning copies to
// Quota and SubtreeQuota (these are replaced with new maps upon update).
func (r resourceNode) Clone() resourceNode {
	return resourceNode{
		Quotas:       r.Quotas,
		SubtreeQuota: r.SubtreeQuota,
		Usage:        maps.Clone(r.Usage),
	}
}

// localQuota is the capacity which is only visible to the subtree
// defined by this node due to lending limits. As a consequence,
// this capacity will never be lent out to the parent Cohort.
func (r resourceNode) localQuota(fr resources.FlavorResource) resources.Amount {
	if lendingLimit := r.Quotas[fr].LendingLimit; lendingLimit != nil {
		return resources.MaxAmount(resources.NewAmount(0), r.SubtreeQuota[fr].Sub(*lendingLimit))
	}
	return resources.NewAmount(0)
}

// hierarchicalResourceNode extends flatResourceNode
// with the ability to navigate to the parent node.
type hierarchicalResourceNode interface {
	flatResourceNode

	HasParent() bool
	parentHRN() hierarchicalResourceNode
}

// flatResourceNode abstracts over ClusterQueues and Cohorts,
// by providing access to the contained ResourceNode.
type flatResourceNode interface {
	getResourceNode() resourceNode
}

// LocalAvailable returns, for a given node and resource flavor,
// how much guaranteed quota in this flavor exceeds usage.
// This quota is available at this node but is not visible at its parent.
func LocalAvailable(node flatResourceNode, fr resources.FlavorResource) resources.Amount {
	r := node.getResourceNode()
	return resources.MaxAmount(resources.NewAmount(0), r.localQuota(fr).Sub(r.Usage[fr]))
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
func available(node hierarchicalResourceNode, fr resources.FlavorResource) resources.Amount {
	r := node.getResourceNode()
	if !node.HasParent() {
		return r.SubtreeQuota[fr].Sub(r.Usage[fr])
	}
	parentAvailable := available(node.parentHRN(), fr)

	if borrowingLimit := r.Quotas[fr].BorrowingLimit; borrowingLimit != nil {
		// All of these can be Unlimited; Amount methods propagate that.
		lq := r.localQuota(fr)
		storedInParent := r.SubtreeQuota[fr].Sub(lq)
		usedInParent := resources.MaxAmount(resources.NewAmount(0), r.Usage[fr].Sub(lq))
		withMaxFromParent := storedInParent.Sub(usedInParent).Add(*borrowingLimit)
		parentAvailable = resources.MinAmount(withMaxFromParent, parentAvailable)
	}
	return LocalAvailable(node, fr).Add(parentAvailable)
}

// potentialAvailable returns the maximum capacity available to this node,
// assuming no usage, while respecting BorrowingLimits.
//
// Uses saturating arithmetic so sums of large (potentially MaxAmount) quotas
// from this node and its ancestors never wrap around int64.
func potentialAvailable(node hierarchicalResourceNode, fr resources.FlavorResource) resources.Amount {
	r := node.getResourceNode()
	if !node.HasParent() {
		return r.SubtreeQuota[fr]
	}
	avail := r.localQuota(fr).Add(potentialAvailable(node.parentHRN(), fr))
	if borrowingLimit := r.Quotas[fr].BorrowingLimit; borrowingLimit != nil {
		maxWithBorrowing := r.SubtreeQuota[fr].Add(*borrowingLimit)
		avail = resources.MinAmount(maxWithBorrowing, avail)
	}
	return avail
}

// addUsage adds usage to the current node, and bubbles up usage to
// its Cohort when usage exceeds localQuota.
func addUsage(node hierarchicalResourceNode, fr resources.FlavorResource, val resources.Amount) {
	r := node.getResourceNode()
	localAvailable := LocalAvailable(node, fr)
	r.Usage[fr] = r.Usage[fr].Add(val)
	if node.HasParent() && val.Cmp(localAvailable) > 0 {
		deltaParentUsage := val.Sub(localAvailable)
		addUsage(node.parentHRN(), fr, deltaParentUsage)
	}
}

// removeUsage removes usage from the current node, and removes usage
// past localQuota that it was storing in its Cohort.
func removeUsage(node hierarchicalResourceNode, fr resources.FlavorResource, val resources.Amount) {
	r := node.getResourceNode()
	usageStoredInParent := r.Usage[fr].Sub(r.localQuota(fr))
	r.Usage[fr] = r.Usage[fr].Sub(val)
	if usageStoredInParent.CmpInt64(0) <= 0 || !node.HasParent() {
		return
	}
	deltaParentUsage := resources.MinAmount(val, usageStoredInParent)
	removeUsage(node.parentHRN(), fr, deltaParentUsage)
}

func updateClusterQueueResourceNode(cq *clusterQueue) {
	cq.AllocatableResourceGeneration++
	cq.resourceNode.SubtreeQuota = make(resources.FlavorResourceQuantities, len(cq.resourceNode.Quotas))
	for fr, quota := range cq.resourceNode.Quotas {
		cq.resourceNode.SubtreeQuota[fr] = quota.Nominal
	}
}

// updateCohortTreeResources traverses the Cohort tree from the root
// to accumulate SubtreeQuota and Usage. It returns an error if the
// provided Cohort has a cycle.
func updateCohortTreeResources(cohort *cohort) error {
	if hierarchy.HasCycle(cohort) {
		return ErrCohortHasCycle
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

// updateCohortTreeResourcesIfNoCycle only updates Cohort tree resources if there's no cycle. Purpose
// of this function is to reduce instances where errors from updateCohortTreeResources are silenced.
// Errors from event handlers are not retried, so its better to execute updateCohortResourceNode when no error
// would be produced.
func updateCohortTreeResourcesIfNoCycle(cohort *cohort) {
	if !hierarchy.HasCycle(cohort) {
		updateCohortResourceNode(cohort.getRootUnsafe())
	}
}

func accumulateFromChild(parent *cohort, child flatResourceNode) {
	for fr, childQuota := range child.getResourceNode().SubtreeQuota {
		delta := childQuota.Sub(child.getResourceNode().localQuota(fr))
		// Add saturates at MaxInt64 on overflow, so large children never wrap the parent SubtreeQuota negative.
		parent.resourceNode.SubtreeQuota[fr] = parent.resourceNode.SubtreeQuota[fr].Add(delta)
	}
	for fr, childUsage := range child.getResourceNode().Usage {
		delta := resources.MaxAmount(resources.NewAmount(0), childUsage.Sub(child.getResourceNode().localQuota(fr)))
		parent.resourceNode.Usage[fr] = parent.resourceNode.Usage[fr].Add(delta)
	}
}

// QuantitiesFitInQuota returns if resource requests fit in quota on this node
// and the amount of resources that exceed the guaranteed
// quota on this node. It is assumed that subsequent call
// to this function will be on nodes parent with remainingRequests
func QuantitiesFitInQuota(node flatResourceNode, requests resources.FlavorResourceQuantities) (bool, resources.FlavorResourceQuantities) {
	fits := true
	remainingRequests := make(resources.FlavorResourceQuantities, len(requests))
	for fr, v := range requests {
		if node.getResourceNode().SubtreeQuota[fr].Cmp(node.getResourceNode().Usage[fr].Add(v)) < 0 {
			fits = false
		}
		remainingRequests[fr] = resources.MaxAmount(resources.NewAmount(0), v.Sub(LocalAvailable(node, fr)))
	}
	return fits, remainingRequests
}

// IsWithinNominalInResources returns whether or not, the node quota usage exceeds its
// nominal quota in any resource flavor out of a set of resource flavours.
func IsWithinNominalInResources(node flatResourceNode, frs sets.Set[resources.FlavorResource]) bool {
	for fr := range frs {
		if node.getResourceNode().SubtreeQuota[fr].Cmp(node.getResourceNode().Usage[fr]) < 0 {
			return false
		}
	}
	return true
}
