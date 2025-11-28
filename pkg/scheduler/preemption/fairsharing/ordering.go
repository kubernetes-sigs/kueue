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

package fairsharing

import (
	"iter"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/utils/clock"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	preemptioncommon "sigs.k8s.io/kueue/pkg/scheduler/preemption/common"
	"sigs.k8s.io/kueue/pkg/workload"
)

// TargetClusterQueueOrdering defines an ordering over ClusterQueues
// to consider during preemption.  This is done by traversing from
// root Cohort, selecting the child which satisfies the 3 conditions:
// 1. has candidate workloads
// 2. has DRS >= 0
// 3. has the highest DominantResourceShare of nodes passing filters 1 and 2.
//
// The same TargetClusterQueue may be returned multiple times in a
// row, if its AlmostLeastCommonAncestor's DominantResourceShare is
// still the highest.
//
// To guarantee progression, TargetClusterQueue.PopWorkload, or
// TargetClusterQueueOrdering.DropQueue must be called between each
// entry returned.
type TargetClusterQueueOrdering struct {
	clock       clock.Clock
	preemptorCq *schdcache.ClusterQueueSnapshot
	// ancestor Cohorts of the preemptor ClusterQueue.
	preemptorAncestors sets.Set[*schdcache.CohortSnapshot]

	clusterQueueToTarget map[kueue.ClusterQueueReference][]*workload.Info

	// pruned nodes are nodes which we are certain will never
	// yield more preemption target candidates. We use this set to
	// determine our stopping condition: once the rootCohort is in
	// the prunedCohorts list, we will not find any more
	// preemption target candidates.
	prunedClusterQueues sets.Set[*schdcache.ClusterQueueSnapshot]
	prunedCohorts       sets.Set[*schdcache.CohortSnapshot]
	log                 logr.Logger
}

func MakeClusterQueueOrdering(cq *schdcache.ClusterQueueSnapshot, candidates []*workload.Info, log logr.Logger, clk clock.Clock) TargetClusterQueueOrdering {
	t := TargetClusterQueueOrdering{
		clock: clk,

		preemptorCq:        cq,
		preemptorAncestors: sets.New[*schdcache.CohortSnapshot](),

		clusterQueueToTarget: make(map[kueue.ClusterQueueReference][]*workload.Info),

		prunedClusterQueues: sets.New[*schdcache.ClusterQueueSnapshot](),
		prunedCohorts:       sets.New[*schdcache.CohortSnapshot](),
		log:                 log,
	}

	for ancestor := range cq.PathParentToRoot() {
		t.preemptorAncestors.Insert(ancestor)
	}

	for _, candidate := range candidates {
		t.clusterQueueToTarget[candidate.ClusterQueue] = append(t.clusterQueueToTarget[candidate.ClusterQueue], candidate)
	}

	return t
}

func (t *TargetClusterQueueOrdering) Iter() iter.Seq[*TargetClusterQueue] {
	return func(yield func(v *TargetClusterQueue) bool) {
		// handle CQ without Cohort case.
		if !t.preemptorCq.HasParent() {
			targetCq := &TargetClusterQueue{
				ordering: t,
				targetCq: t.preemptorCq,
			}

			for targetCq.HasWorkload() {
				if !yield(targetCq) {
					return
				}
			}
		}

		root := t.preemptorCq.Parent().Root()
		// we stop once we have marked the root as pruned.
		for !t.prunedCohorts.Has(root) {
			targetCq := t.nextTarget(root)

			// an iteration which just pruned some node(s).
			if targetCq == nil {
				continue
			}
			if !yield(targetCq) {
				return
			}
		}
	}
}

// DropQueue indicates that we should no longer
// consider workloads from this Queue.
func (t *TargetClusterQueueOrdering) DropQueue(cq *TargetClusterQueue) {
	t.prunedClusterQueues.Insert(cq.targetCq)
}

func (t *TargetClusterQueueOrdering) onPathFromRootToPreemptorCQ(cohort *schdcache.CohortSnapshot) bool {
	return t.preemptorAncestors.Has(cohort)
}

func (t *TargetClusterQueueOrdering) hasWorkload(cq *schdcache.ClusterQueueSnapshot) bool {
	return len(t.clusterQueueToTarget[cq.GetName()]) > 0
}

// nextTarget is a recursive algorithm for finding the next
// TargetClusterQueue.  It finds the child with the highest DRS,
// returning it if it is a ClusterQueue, or entering the recursive
// call if it is a Cohort.  The return of nil doesn't mean that there
// are no more candidate ClusterQueues; an iteration may have only
// pruned nodes from the tree.
func (t *TargetClusterQueueOrdering) nextTarget(cohort *schdcache.CohortSnapshot) *TargetClusterQueue {
	var highestCq *schdcache.ClusterQueueSnapshot
	highestCqDrs := schdcache.NegativeDRS()
	for _, cq := range cohort.ChildCQs() {
		if t.prunedClusterQueues.Has(cq) {
			continue
		}

		drs := cq.DominantResourceShare()
		// we can't prune the preemptor ClusterQueue itself,
		// until it runs out of candidates.
		switch {
		case (drs.IsZero() && cq != t.preemptorCq) || !t.hasWorkload(cq):
			t.prunedClusterQueues.Insert(cq)
		case schdcache.CompareDRS(drs, highestCqDrs) == 0:
			newCandWl := t.clusterQueueToTarget[cq.GetName()][0]
			currentCandWl := t.clusterQueueToTarget[highestCq.GetName()][0]
			if preemptioncommon.CandidatesOrdering(t.log, false, newCandWl, currentCandWl, t.preemptorCq.Name, t.clock.Now()) < 0 {
				highestCq = cq
			}
		case schdcache.CompareDRS(drs, highestCqDrs) == 1:
			highestCqDrs = drs
			highestCq = cq
		}
	}

	var highestCohort *schdcache.CohortSnapshot = nil
	highestCohortDrs := schdcache.NegativeDRS()
	for _, cohort := range cohort.ChildCohorts() {
		if t.prunedCohorts.Has(cohort) {
			continue
		}

		drs := cohort.DominantResourceShare()

		// we prune a Cohort when it is no longer borrowing
		// (DRS=0). Even when not borrowing, we can't prune a
		// Cohort on path from preemptor ClusterQueue to
		// root, as there may be imbalance within some
		// subtree, or a possible preemption within Preemptor
		// CQ itself.  We will only prune such a Cohort if all
		// of its children have been pruned.
		if drs.IsZero() && !t.onPathFromRootToPreemptorCQ(cohort) {
			t.prunedCohorts.Insert(cohort)
		} else if schdcache.CompareDRS(drs, highestCohortDrs) >= 0 {
			highestCohortDrs = drs
			highestCohort = cohort
		}
	}

	// None of the children are valid candidates (i.e. all
	// children pruned), so this Cohort is pruned.
	if highestCohort == nil && highestCq == nil {
		t.prunedCohorts.Insert(cohort)
		return nil
	}

	// we use >= because, as a tiebreak, choosing the Cohort seems
	// slightly more fair, as we can choose the most unfair node
	// within that Cohort.
	if schdcache.CompareDRS(highestCohortDrs, highestCqDrs) >= 0 {
		return t.nextTarget(highestCohort)
	}
	return &TargetClusterQueue{
		ordering: t,
		targetCq: highestCq,
	}
}
