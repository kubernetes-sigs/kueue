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

package classical

import (
	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/util/sets"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/resources"
	preemptioncommon "sigs.k8s.io/kueue/pkg/scheduler/preemption/common"
	"sigs.k8s.io/kueue/pkg/util/priority"
	"sigs.k8s.io/kueue/pkg/workload"
)

type preemptionVariant int

const (
	// Cannot be preempted
	Never preemptionVariant = iota
	// Candidate within the same CQ as the preemptor
	WithinCQ
	// Preemptor has preferential access to the resources needing preemption
	// over the candidate, because of its CQ position in the cohort topology.
	HiearchicalReclaim
	// Can only be preempted if preemptor CQ (after all preemptions and the
	// admission of the incoming workload) would not be borrowing any quota
	ReclaimWithoutBorrowing
	// Can be preemped even if preemptor CQ would be borrowing
	ReclaimWhileBorrowing
)

func (m preemptionVariant) PreemptionReason() string {
	switch m {
	case WithinCQ:
		return kueue.InClusterQueueReason
	case HiearchicalReclaim:
		return kueue.InCohortReclamationReason
	case ReclaimWhileBorrowing:
		return kueue.InCohortReclaimWhileBorrowingReason
	case ReclaimWithoutBorrowing:
		return kueue.InCohortReclamationReason
	}
	return "Unknown"
}

type HierarchicalPreemptionCtx struct {
	Log               logr.Logger
	Wl                *kueue.Workload
	Cq                *schdcache.ClusterQueueSnapshot
	FrsNeedPreemption sets.Set[resources.FlavorResource]
	Requests          resources.FlavorResourceQuantities
	WorkloadOrdering  workload.Ordering
}

func IsBorrowingWithinCohortForbidden(cq *schdcache.ClusterQueueSnapshot) (bool, *int32) {
	borrowWithinCohort := cq.Preemption.BorrowWithinCohort
	if borrowWithinCohort == nil || borrowWithinCohort.Policy == kueue.BorrowWithinCohortPolicyNever {
		return true, nil
	}
	return false, borrowWithinCohort.MaxPriorityThreshold
}

// classifyPreemptionVariant evaluates, based on config and priorities, the
// preemption type for a given candidate
func classifyPreemptionVariant(ctx *HierarchicalPreemptionCtx, wl *workload.Info, haveHierarchicalAdvantage bool) preemptionVariant {
	if !WorkloadUsesResources(wl, ctx.FrsNeedPreemption) {
		return Never
	}

	var preemptionPolicy kueue.PreemptionPolicy
	if wl.ClusterQueue == ctx.Cq.Name {
		preemptionPolicy = ctx.Cq.Preemption.WithinClusterQueue
	} else {
		preemptionPolicy = ctx.Cq.Preemption.ReclaimWithinCohort
	}

	if !preemptioncommon.SatisfiesPreemptionPolicy(ctx.Log, ctx.Wl, wl.Obj, ctx.WorkloadOrdering, preemptionPolicy) {
		return Never
	}

	if wl.ClusterQueue == ctx.Cq.Name {
		return WithinCQ
	}
	if haveHierarchicalAdvantage {
		return HiearchicalReclaim
	}
	borrowWithinCohortForbidden, borrowWithinCohortThreshold := IsBorrowingWithinCohortForbidden(ctx.Cq)
	if borrowWithinCohortForbidden {
		return ReclaimWithoutBorrowing
	}
	candidatePriority := priority.EffectivePriority(ctx.Log, wl.Obj)
	incomingPriority := priority.EffectivePriority(ctx.Log, ctx.Wl)
	if isAboveBorrowingThreshold(candidatePriority, incomingPriority, borrowWithinCohortThreshold) {
		return ReclaimWithoutBorrowing
	}
	return ReclaimWhileBorrowing
}

func isAboveBorrowingThreshold(candidatePriority, incomingPriority int64, borrowWithinCohortThreshold *int32) bool {
	if candidatePriority >= incomingPriority {
		return true
	}
	if borrowWithinCohortThreshold == nil {
		return false
	}
	return candidatePriority > int64(*borrowWithinCohortThreshold)
}

// classifiedClusterQueue is the per-ClusterQueue classification for one
// preemptor: which category the CQ falls into and the lca used for pruning. It
// carries no materialized workloads. The ordered candidates are the CQ's
// cycle-level sorted slice (owned by the CandidateOrderCache), attached later
// into sorted and consumed lazily.
type classifiedClusterQueue struct {
	cq                       *schdcache.ClusterQueueSnapshot
	lca                      *schdcache.CohortSnapshot
	hasHierarchicalAdvantage bool
	isSameQueue              bool
	sorted                   []*workload.Info
	evictedCount             int
}

func collectSameQueueCQ(ctx *HierarchicalPreemptionCtx) []classifiedClusterQueue {
	if ctx.Cq.Preemption.WithinClusterQueue == kueue.PreemptionPolicyNever {
		return nil
	}
	return []classifiedClusterQueue{{cq: ctx.Cq, isSameQueue: true}}
}

// collectHierarchicalReclaimCQs walks the cohort subtree and returns, for every
// over-nominal ClusterQueue the preemptor may reclaim from, its classification
// split into the hierarchy (preemptor has hierarchical advantage) and priority
// (advantage decided by priority) categories, each with its lca.
func collectHierarchicalReclaimCQs(ctx *HierarchicalPreemptionCtx) (hierarchy, priority []classifiedClusterQueue) {
	if !ctx.Cq.HasParent() || ctx.Cq.Preemption.ReclaimWithinCohort == kueue.PreemptionPolicyNever {
		return nil, nil
	}
	var previousSubtreeRoot *schdcache.CohortSnapshot
	var fits bool
	hasHierarchicalAdvantage, remainingRequests := schdcache.QuantitiesFitInQuota(ctx.Cq, ctx.Requests)
	for currentSubtreeRoot := range ctx.Cq.PathParentToRoot() {
		collectSubtreeCQs(ctx, currentSubtreeRoot, currentSubtreeRoot, previousSubtreeRoot, hasHierarchicalAdvantage, &hierarchy, &priority)
		fits, remainingRequests = schdcache.QuantitiesFitInQuota(currentSubtreeRoot, remainingRequests)
		// Once we find a subtree sT that fits the requests, we will look for workloads that use quota
		// of that subtree. The preemptor will have hierarchical advantage over all such workloads
		// because it belongs to subtree sT. For that reason variable hasHierarchicalAdvantage
		// remains true in subsequent iterations of the loop.
		hasHierarchicalAdvantage = hasHierarchicalAdvantage || fits
		previousSubtreeRoot = currentSubtreeRoot
	}
	return hierarchy, priority
}

// visit the nodes in the hierarchy and classify the CQs that exceed quota
// avoid subtrees that are within quota and the skipped subtree
func collectSubtreeCQs(
	ctx *HierarchicalPreemptionCtx,
	currentCohort *schdcache.CohortSnapshot,
	subtreeRoot *schdcache.CohortSnapshot,
	skipSubtree *schdcache.CohortSnapshot,
	hasHierarchicalAdvantage bool,
	hierarchy, priority *[]classifiedClusterQueue,
) {
	for _, childCohort := range currentCohort.ChildCohorts() {
		// we already processed this subtree
		if childCohort == skipSubtree {
			continue
		}
		// don't look for candidates in subtrees that are not exceeding their quotas
		if schdcache.IsWithinNominalInResources(childCohort, ctx.FrsNeedPreemption) {
			continue
		}
		collectSubtreeCQs(ctx, childCohort, subtreeRoot, skipSubtree, hasHierarchicalAdvantage, hierarchy, priority)
	}
	for _, childCq := range currentCohort.ChildCQs() {
		if childCq == ctx.Cq {
			continue
		}
		if !schdcache.IsWithinNominalInResources(childCq, ctx.FrsNeedPreemption) {
			cc := classifiedClusterQueue{cq: childCq, lca: subtreeRoot, hasHierarchicalAdvantage: hasHierarchicalAdvantage}
			if hasHierarchicalAdvantage {
				*hierarchy = append(*hierarchy, cc)
			} else {
				*priority = append(*priority, cc)
			}
		}
	}
}

// getNodeHeight calculates the distance to the furthest leaf
func getNodeHeight(node *schdcache.CohortSnapshot) int {
	maxHeight := min(node.ChildCount(), 1)
	for _, childCohort := range node.ChildCohorts() {
		maxHeight = max(maxHeight, getNodeHeight(childCohort)+1)
	}
	return maxHeight
}

// FindHeightOfLowestSubtreeThatFits returns height of a lowest subtree in the cohort
// that fits additional val of resource fr. If no such subtree exists, it returns
// height the whole cohort hierarchy. Note that height of a trivial subtree
// with only one node is 0. It also returns if the returned subtree is smaller than the whole cohort tree.
func FindHeightOfLowestSubtreeThatFits(c *schdcache.ClusterQueueSnapshot, fr resources.FlavorResource, val resources.Amount) (int, bool) {
	if !c.BorrowingWith(fr, val) || !c.HasParent() {
		return 0, c.HasParent()
	}
	remaining := val.Sub(schdcache.LocalAvailable(c, fr))
	for trackingNode := range c.PathParentToRoot() {
		if !trackingNode.BorrowingWith(fr, remaining) {
			return getNodeHeight(trackingNode), trackingNode.HasParent()
		}
		remaining = remaining.Sub(schdcache.LocalAvailable(trackingNode, fr))
	}
	// no fit found
	return getNodeHeight(c.Parent().Root()), false
}
