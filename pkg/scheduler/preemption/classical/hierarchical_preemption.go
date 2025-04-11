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
	"k8s.io/apimachinery/pkg/util/sets"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/cache"
	"sigs.k8s.io/kueue/pkg/resources"
	"sigs.k8s.io/kueue/pkg/util/priority"
	"sigs.k8s.io/kueue/pkg/workload"
)

type PreemptionPossibility string

const (
	PreemptionPossibilityNever            PreemptionPossibility = "never"
	PreemptionPossibilityWithoutBorrowing PreemptionPossibility = "withoutBorrowing"
	PreemptionPossibilityAlways           PreemptionPossibility = "always"
)

type HierarchicalPreemptionCtx struct {
	Wl                *kueue.Workload
	Cq                *cache.ClusterQueueSnapshot
	FrsNeedPreemption sets.Set[resources.FlavorResource]
	Requests          resources.FlavorResourceQuantities
	WorkloadOrdering  workload.Ordering
}

func IsBorrowingWithinCohortAllowed(cq *cache.ClusterQueueSnapshot) (bool, *int32) {
	borrowWithinCohort := cq.Preemption.BorrowWithinCohort
	if borrowWithinCohort == nil || borrowWithinCohort.Policy == kueue.BorrowWithinCohortPolicyNever {
		return false, nil
	}
	return true, borrowWithinCohort.MaxPriorityThreshold
}

// mayPreempt returns if ctx.Wl may preempt candidate wl
// Preemption possibility withoutBorrowing means that
// wl can only be preempted by ctx.Wl if ctx.Cq would
// not be borrowing any quota from its cohort
func mayPreempt(ctx *HierarchicalPreemptionCtx, wl *workload.Info, haveHierarchicalAdvantage bool) PreemptionPossibility {
	if !WorkloadUsesResources(wl, ctx.FrsNeedPreemption) {
		return PreemptionPossibilityNever
	}
	incomingPriority := priority.Priority(ctx.Wl)
	candidatePriority := priority.Priority(wl.Obj)
	if isNeverPreemptable(ctx, wl, incomingPriority, candidatePriority) {
		return PreemptionPossibilityNever
	}
	if wl.ClusterQueue == ctx.Cq.Name || haveHierarchicalAdvantage {
		return PreemptionPossibilityAlways
	}
	borrowWithinCohortAllowed, borrowWithinCohortThreshold := IsBorrowingWithinCohortAllowed(ctx.Cq)
	if !borrowWithinCohortAllowed {
		return PreemptionPossibilityWithoutBorrowing
	}
	if isAboveBorrowingThreshold(candidatePriority, incomingPriority, borrowWithinCohortThreshold, haveHierarchicalAdvantage) {
		return PreemptionPossibilityWithoutBorrowing
	}
	return PreemptionPossibilityAlways
}

func isNeverPreemptable(ctx *HierarchicalPreemptionCtx, wl *workload.Info, incomingPriority, candidatePriority int32) bool {
	var preemptionPolicy kueue.PreemptionPolicy
	if wl.ClusterQueue == ctx.Cq.Name {
		preemptionPolicy = ctx.Cq.Preemption.WithinClusterQueue
	} else {
		preemptionPolicy = ctx.Cq.Preemption.ReclaimWithinCohort
	}
	lowerPriority := incomingPriority > candidatePriority
	if preemptionPolicy == kueue.PreemptionPolicyLowerPriority {
		return !lowerPriority
	}
	if preemptionPolicy == kueue.PreemptionPolicyLowerOrNewerEqualPriority {
		preemptorTS := ctx.WorkloadOrdering.GetQueueOrderTimestamp(ctx.Wl)
		newerEqualPriority := (incomingPriority == candidatePriority) && preemptorTS.Before(ctx.WorkloadOrdering.GetQueueOrderTimestamp(wl.Obj))
		return !(lowerPriority || newerEqualPriority)
	}
	return preemptionPolicy != kueue.PreemptionPolicyAny
}

func isAboveBorrowingThreshold(candidatePriority, incomingPriority int32, borrowWithinCohortThreshold *int32, haveHierarchicalAdvantage bool) bool {
	if borrowWithinCohortThreshold == nil {
		if !haveHierarchicalAdvantage {
			return candidatePriority >= incomingPriority
		}
		return false
	}
	aboveThreshold := candidatePriority > *borrowWithinCohortThreshold
	if !haveHierarchicalAdvantage {
		aboveThreshold = aboveThreshold || candidatePriority >= incomingPriority
	}
	return aboveThreshold
}

func getCandidatesFromCQ(cq *cache.ClusterQueueSnapshot, lca *cache.CohortSnapshot, ctx *HierarchicalPreemptionCtx, hasHiearchicalAdvantage bool) []*candidateElem {
	candidates := []*candidateElem{}
	for _, candidateWl := range cq.Workloads {
		mayPreempt := mayPreempt(ctx, candidateWl, hasHiearchicalAdvantage)
		if mayPreempt == PreemptionPossibilityNever {
			continue
		}
		candidates = append(candidates,
			&candidateElem{
				wl:                   candidateWl,
				lca:                  lca,
				onlyWithoutBorrowing: mayPreempt == PreemptionPossibilityWithoutBorrowing,
				reclaim:              hasHiearchicalAdvantage,
			})
	}
	return candidates
}

func collectCandidatesForHierarchicalReclaim(ctx *HierarchicalPreemptionCtx) ([]*candidateElem, []*candidateElem) {
	hierarchyCandidates := []*candidateElem{}
	priorityCandidates := []*candidateElem{}
	if !ctx.Cq.HasParent() || ctx.Cq.Preemption.ReclaimWithinCohort == kueue.PreemptionPolicyNever {
		return hierarchyCandidates, priorityCandidates
	}
	var previousRoot *cache.CohortSnapshot
	var candidateList *[]*candidateElem
	var fits bool
	trackingNode := ctx.Cq.Parent()
	hasHierarchicalAdvantage, remainingRequests := cache.WorkloadFitsInQuota(&ctx.Cq.ResourceNode, ctx.Requests)
	for {
		if hasHierarchicalAdvantage {
			candidateList = &hierarchyCandidates
		} else {
			candidateList = &priorityCandidates
		}
		collectCandidatesInSubtree(ctx, trackingNode, trackingNode, previousRoot, hasHierarchicalAdvantage, candidateList)
		if !trackingNode.HasParent() {
			break
		}
		fits, remainingRequests = cache.WorkloadFitsInQuota(&trackingNode.ResourceNode, remainingRequests)
		hasHierarchicalAdvantage = hasHierarchicalAdvantage || fits
		previousRoot = trackingNode
		trackingNode = trackingNode.Parent()
	}
	return hierarchyCandidates, priorityCandidates
}

// visit the nodes in the hierarchy and collect the ones that exceed quota
// avoid subtrees that are within quota and the forbidden subtree
func collectCandidatesInSubtree(ctx *HierarchicalPreemptionCtx, node *cache.CohortSnapshot, startingNode *cache.CohortSnapshot, forbiddenSubtree *cache.CohortSnapshot, hasHierarchicalAdvantage bool, result *[]*candidateElem) {
	for _, childCohort := range node.ChildCohorts() {
		if childCohort == forbiddenSubtree {
			continue
		}
		// don't look for candidates in subtrees that are not exceeding their quotas
		if cache.IsWithinNominalInResources(&childCohort.ResourceNode, ctx.FrsNeedPreemption) {
			continue
		}
		collectCandidatesInSubtree(ctx, childCohort, startingNode, forbiddenSubtree, hasHierarchicalAdvantage, result)
	}
	for _, childCq := range node.ChildCQs() {
		if childCq == ctx.Cq {
			continue
		}
		if !cache.IsWithinNominalInResources(&childCq.ResourceNode, ctx.FrsNeedPreemption) {
			*result = append(*result, getCandidatesFromCQ(childCq, startingNode, ctx, hasHierarchicalAdvantage)...)
		}
	}
}

func collectSameQueueCandidates(ctx *HierarchicalPreemptionCtx) []*candidateElem {
	var sameQueueCandidates []*candidateElem
	if ctx.Cq.Preemption.WithinClusterQueue == kueue.PreemptionPolicyNever {
		return sameQueueCandidates
	}
	for _, candidateWl := range ctx.Cq.Workloads {
		if mayPreempt(ctx, candidateWl, false) == PreemptionPossibilityNever {
			continue
		}
		sameQueueCandidates = append(sameQueueCandidates,
			&candidateElem{
				wl:                   candidateWl,
				lca:                  nil,
				onlyWithoutBorrowing: false,
				reclaim:              false,
			})
	}
	return sameQueueCandidates
}

// getNodeHeight calculates the distance to the furthest leaf
func getNodeHeight(node *cache.CohortSnapshot) int {
	maxHeight := min(len(node.ChildCQs()), 1)
	for _, childCohort := range node.ChildCohorts() {
		maxHeight = max(maxHeight, getNodeHeight(childCohort)+1)
	}
	return maxHeight
}

// FindHeightOfLowestSubtreeThatFits returns height of a lowest subtree in the cohort
// that fits additional val of resource fr. If no such subtree exists, it returns
// height the whole cohort hierarchy. Note that height of a trivial subtree
// with only one node is 0. It also returns if the returned subtree is smaller than the whole cohort tree.
func FindHeightOfLowestSubtreeThatFits(c *cache.ClusterQueueSnapshot, fr resources.FlavorResource, val int64) (int, bool) {
	if !(c.BorrowingWith(fr, val) && c.HasParent()) {
		return 0, c.HasParent()
	}
	remaining := cache.CalculateRemaining(&c.ResourceNode, fr, val)
	var trackingNode *cache.CohortSnapshot
	for trackingNode = c.Parent(); trackingNode.HasParent(); trackingNode = trackingNode.Parent() {
		if trackingNode.ResourceNode.Usage[fr]+remaining <= trackingNode.ResourceNode.SubtreeQuota[fr] {
			break
		}
		remaining = cache.CalculateRemaining(&trackingNode.ResourceNode, fr, remaining)
	}
	return getNodeHeight(trackingNode), trackingNode.HasParent()
}
