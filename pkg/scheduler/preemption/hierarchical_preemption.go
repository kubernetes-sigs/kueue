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

package preemption

import (
	"k8s.io/apimachinery/pkg/util/sets"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/cache"
	"sigs.k8s.io/kueue/pkg/resources"
	"sigs.k8s.io/kueue/pkg/util/priority"
	"sigs.k8s.io/kueue/pkg/workload"
)

type hierarchicalPreemptionCtx struct {
	wl                *kueue.Workload
	cq                *cache.ClusterQueueSnapshot
	frsNeedPreemption sets.Set[resources.FlavorResource]
	requests          resources.FlavorResourceQuantities
	workloadOrdering  workload.Ordering
}

func workloadFitsInQuota(node *cache.ResourceNode, requests resources.FlavorResourceQuantities) bool {
	for fr, v := range requests {
		if node.Usage[fr]+v > node.SubtreeQuota[fr] {
			return false
		}
	}
	return true
}

func getCandidatesFromCQ(cq *cache.ClusterQueueSnapshot, lca *cache.CohortSnapshot, ctx *hierarchicalPreemptionCtx, priorityThreshold *int32) []*candidateElem {
	candidates := []*candidateElem{}
	incomingWlPrio := priority.Priority(ctx.wl)
	preemptorTS := ctx.workloadOrdering.GetQueueOrderTimestamp(ctx.wl)
	for _, candidateWl := range cq.Workloads {
		if !workloadUsesResources(candidateWl, ctx.frsNeedPreemption) {
			continue
		}
		lowerPriority := incomingWlPrio > priority.Priority(candidateWl.Obj)
		newerEqualPriority := (incomingWlPrio == priority.Priority(candidateWl.Obj)) && preemptorTS.Before(ctx.workloadOrdering.GetQueueOrderTimestamp(candidateWl.Obj))
		if ctx.cq.Preemption.ReclaimWithinCohort == kueue.PreemptionPolicyLowerPriority && !lowerPriority {
			continue
		}
		if ctx.cq.Preemption.ReclaimWithinCohort == kueue.PreemptionPolicyLowerOrNewerEqualPriority && !(lowerPriority || newerEqualPriority) {
			continue
		}
		candidates = append(candidates,
			&candidateElem{
				wl:                     candidateWl,
				lca:                    lca,
				abovePriorityThreshold: priorityThreshold != nil && *priorityThreshold < priority.Priority(candidateWl.Obj),
			})
	}
	return candidates
}

func cohortWithinNominalInResourcesNeedingPreemption(node *cache.ResourceNode, frsNeedPreemption sets.Set[resources.FlavorResource]) bool {
	for fr := range frsNeedPreemption {
		if node.Usage[fr] > node.SubtreeQuota[fr] {
			return false
		}
	}
	return true
}

func calculatePriorityThreshold(ctx *hierarchicalPreemptionCtx, borrowWithinCohortThreshold *int32, wl *kueue.Workload, inST bool) *int32 {
	wlPriorityMinusOne := priority.Priority(wl) - 1
	switch {
	case ctx.cq.Preemption.ReclaimWithinCohort == kueue.PreemptionPolicyAny:
		if borrowWithinCohortThreshold == nil {
			return nil
		}
		threshold := *borrowWithinCohortThreshold
		if inST {
			threshold = min(threshold, wlPriorityMinusOne)
		}
		return &threshold
	case borrowWithinCohortThreshold != nil:
		threshold := min(wlPriorityMinusOne, *borrowWithinCohortThreshold)
		return &threshold
	default:
		return &wlPriorityMinusOne
	}
}

func collectCandidatesForHierarchicalReclaim(ctx *hierarchicalPreemptionCtx, borrowWithinCohortThreshold *int32) ([]*candidateElem, []*candidateElem) {
	var previousRoot *cache.CohortSnapshot
	var candidateList *[]*candidateElem
	trackingNode := ctx.cq.Parent()
	candidates := []*candidateElem{}
	sTcandidates := []*candidateElem{}
	inST := !workloadFitsInQuota(&ctx.cq.ResourceNode, ctx.requests)
	previousRoot = nil
	for {
		if inST {
			candidateList = &sTcandidates
		} else {
			candidateList = &candidates
		}
		priorityThreshold := calculatePriorityThreshold(ctx, borrowWithinCohortThreshold, ctx.wl, inST)
		collectCandidatesInSubtree(trackingNode, trackingNode, ctx, previousRoot, candidateList, priorityThreshold)
		if !trackingNode.HasParent() {
			break
		}
		inST = inST && !workloadFitsInQuota(&trackingNode.ResourceNode, ctx.requests)
		previousRoot = trackingNode
		trackingNode = trackingNode.Parent()
	}
	return candidates, sTcandidates
}

// visit the nodes in the hierarchy and collect the ones that exceed quota
// avoid subtrees that are within quota
// separately collect workloads inside/outside sT
func collectCandidatesInSubtree(node *cache.CohortSnapshot, startingNode *cache.CohortSnapshot, ctx *hierarchicalPreemptionCtx, forbiddenSubtree *cache.CohortSnapshot, result *[]*candidateElem, priorityThreshold *int32) {
	for _, childCohort := range node.ChildCohorts() {
		if forbiddenSubtree != nil && childCohort == forbiddenSubtree {
			continue
		}
		// don't look for candidates in subtrees that are not exceeding their quotas
		if cohortWithinNominalInResourcesNeedingPreemption(&childCohort.ResourceNode, ctx.frsNeedPreemption) {
			continue
		}
		collectCandidatesInSubtree(childCohort, startingNode, ctx, forbiddenSubtree, result, priorityThreshold)
	}
	for _, childCq := range node.ChildCQs() {
		if childCq == ctx.cq {
			continue
		}
		if !cohortWithinNominalInResourcesNeedingPreemption(&childCq.ResourceNode, ctx.frsNeedPreemption) {
			*result = append(*result, getCandidatesFromCQ(childCq, startingNode, ctx, priorityThreshold)...)
		}
	}
}
