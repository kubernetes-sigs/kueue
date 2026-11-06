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
	klog "k8s.io/klog/v2"
	"k8s.io/utils/clock"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/resources"
	"sigs.k8s.io/kueue/pkg/scheduler/preemption/policy"
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

	frsNeedPreemption sets.Set[resources.FlavorResource]

	clusterQueueToTarget map[kueue.ClusterQueueReference][]*workload.Info

	// pruned nodes are nodes which we are certain will never
	// yield more preemption target candidates. We use this set to
	// determine our stopping condition: once the rootCohort is in
	// the prunedCohorts list, we will not find any more
	// preemption target candidates.
	prunedClusterQueues sets.Set[*schdcache.ClusterQueueSnapshot]
	prunedCohorts       sets.Set[*schdcache.CohortSnapshot]
	// exhaustedLCAs are the Cohorts for which IterAtLCA won't yield
	// any more targets.
	exhaustedLCAs sets.Set[*schdcache.CohortSnapshot]
	log           logr.Logger
}

func MakeClusterQueueOrdering(
	cq *schdcache.ClusterQueueSnapshot,
	candidates []*workload.Info,
	frsNeedPreemption sets.Set[resources.FlavorResource],
	log logr.Logger,
	clk clock.Clock,
) TargetClusterQueueOrdering {
	t := TargetClusterQueueOrdering{
		clock: clk,

		preemptorCq:        cq,
		preemptorAncestors: sets.New[*schdcache.CohortSnapshot](),
		frsNeedPreemption:  frsNeedPreemption,

		clusterQueueToTarget: make(map[kueue.ClusterQueueReference][]*workload.Info),

		prunedClusterQueues: sets.New[*schdcache.ClusterQueueSnapshot](),
		prunedCohorts:       sets.New[*schdcache.CohortSnapshot](),
		exhaustedLCAs:       sets.New[*schdcache.CohortSnapshot](),
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

// Iter traverses globally from the root Cohort down, selecting the
// node with the highest DominantResourceShare at every level.
func (t *TargetClusterQueueOrdering) Iter() iter.Seq[*TargetClusterQueue] {
	return t.iterCohorts(func(yield func(*schdcache.CohortSnapshot) bool) {
		yield(t.preemptorCq.Parent().Root())
	}, false)
}

// IterAtLCA yields only the target ClusterQueues whose
// LeastCommonAncestor with the preemptor ClusterQueue is lca, by highest
// DominantResourceShare. The subtree on the path to the preemptor and the
// preemptor ClusterQueue itself are skipped. The same pruning as in Iter
// applies: a ClusterQueue, or a Cohort below lca, which is within its
// nominal quota is never entered, so any candidate behind it is protected.
//
// Once all targets at lca are exhausted, further calls for the same lca
// yield nothing. Stopping the iteration early keeps the remaining targets
// for a later call.
func (t *TargetClusterQueueOrdering) IterAtLCA(lca *schdcache.CohortSnapshot) iter.Seq[*TargetClusterQueue] {
	return t.iterCohorts(func(yield func(*schdcache.CohortSnapshot) bool) {
		yield(lca)
	}, true)
}

// iterCohorts walks the given Cohorts in order and, for each of them,
// yields target ClusterQueues from its subtree until the subtree is
// pruned. When atLCA is set, only the subtrees which don't contain the
// preemptor ClusterQueue are considered, see IterAtLCA.
func (t *TargetClusterQueueOrdering) iterCohorts(cohorts iter.Seq[*schdcache.CohortSnapshot], atLCA bool) iter.Seq[*TargetClusterQueue] {
	return func(yield func(v *TargetClusterQueue) bool) {
		// handle CQ without Cohort case.
		if !t.preemptorCq.HasParent() {
			if atLCA {
				return
			}
			targetCq := &TargetClusterQueue{
				ordering: t,
				targetCq: t.preemptorCq,
			}

			// Respect DropQueue: a caller may drop the queue without
			// popping a workload, which would otherwise loop forever.
			for !t.prunedClusterQueues.Has(t.preemptorCq) && targetCq.HasWorkload() {
				if !yield(targetCq) {
					return
				}
			}
			return
		}

		done := t.prunedCohorts.Has
		next := t.nextTarget
		if atLCA {
			done = t.exhaustedLCAs.Has
			next = t.nextTargetAtLCA
		}
		for cohort := range cohorts {
			// we stop once we have marked the cohort as pruned.
			for !done(cohort) {
				targetCq := next(cohort)

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
}

// nextTargetAtLCA is nextTarget restricted to the children of lca which
// are not on the path to the preemptor ClusterQueue. Below lca it
// delegates to nextTarget, as those subtrees can't contain the
// preemptor. Once all of those children are pruned, lca is marked as
// exhausted, but not pruned, as it may still hold targets for other
// LeastCommonAncestors.
func (t *TargetClusterQueueOrdering) nextTargetAtLCA(lca *schdcache.CohortSnapshot) *TargetClusterQueue {
	highestCq, highestCqDrs := t.highestChildCq(lca, func(cq *schdcache.ClusterQueueSnapshot) bool {
		return cq == t.preemptorCq
	}, func(cq *schdcache.ClusterQueueSnapshot, drs schdcache.DRS) bool {
		return !t.isBorrowing(drs) || !t.hasWorkload(cq)
	})
	highestCohort, highestCohortDrs := t.highestChildCohort(lca, func(cohort *schdcache.CohortSnapshot) bool {
		return t.onPathFromRootToPreemptorCQ(cohort)
	})
	if highestCohort == nil && highestCq == nil {
		t.exhaustedLCAs.Insert(lca)
		return nil
	}
	return t.selectTarget(highestCohort, highestCohortDrs, highestCq, highestCqDrs)
}

// highestChildCq returns the child ClusterQueue of cohort with the
// highest DominantResourceShare, breaking ties with the candidates
// ordering. Children for which skip returns true are ignored, and the
// ones for which prune returns true are pruned.
func (t *TargetClusterQueueOrdering) highestChildCq(
	cohort *schdcache.CohortSnapshot,
	skip func(*schdcache.ClusterQueueSnapshot) bool,
	prune func(*schdcache.ClusterQueueSnapshot, schdcache.DRS) bool,
) (*schdcache.ClusterQueueSnapshot, schdcache.DRS) {
	var highestCq *schdcache.ClusterQueueSnapshot
	highestCqDrs := schdcache.NegativeDRS()
	for _, cq := range cohort.ChildCQs() {
		if t.prunedClusterQueues.Has(cq) || skip(cq) {
			continue
		}
		drs := cq.DominantResourceShare()
		switch {
		case prune(cq, drs):
			t.prunedClusterQueues.Insert(cq)
			if logV := t.log.V(6); logV.Enabled() {
				logV.Info("Pruning ClusterQueue during Target ordering", "clusterQueue", cq.GetName(), "drs", drs)
			}
		case schdcache.CompareDRS(drs, highestCqDrs) == 0:
			newCandWl := t.clusterQueueToTarget[cq.GetName()][0]
			currentCandWl := t.clusterQueueToTarget[highestCq.GetName()][0]
			if policy.CandidatesOrdering(t.log, false, newCandWl, currentCandWl, t.preemptorCq.Name, t.clock.Now()) < 0 {
				highestCq = cq
			}
		case schdcache.CompareDRS(drs, highestCqDrs) == 1:
			highestCqDrs = drs
			highestCq = cq
		}
	}
	return highestCq, highestCqDrs
}

// highestChildCohort returns the child Cohort of cohort with the highest
// DominantResourceShare. Children for which skip returns true are
// ignored; a child which isn't borrowing is pruned, unless it is on the
// path to the preemptor ClusterQueue.
func (t *TargetClusterQueueOrdering) highestChildCohort(
	cohort *schdcache.CohortSnapshot,
	skip func(*schdcache.CohortSnapshot) bool,
) (*schdcache.CohortSnapshot, schdcache.DRS) {
	var highestCohort *schdcache.CohortSnapshot
	highestCohortDrs := schdcache.NegativeDRS()
	for _, child := range cohort.ChildCohorts() {
		if t.prunedCohorts.Has(child) || skip(child) {
			continue
		}
		drs := child.DominantResourceShare()
		// we prune a Cohort when it is no longer borrowing.
		// Even when not borrowing, we can't prune a
		// Cohort on path from preemptor ClusterQueue to
		// root, as there may be imbalance within some
		// subtree, or a possible preemption within Preemptor
		// CQ itself.  We will only prune such a Cohort if all
		// of its children have been pruned.
		if !t.isBorrowing(drs) && !t.onPathFromRootToPreemptorCQ(child) {
			t.prunedCohorts.Insert(child)
			if logV := t.log.V(6); logV.Enabled() {
				logV.Info("Pruning Cohort during Target ordering", "cohort", child.GetName(), "drs", drs)
			}
		} else if schdcache.CompareDRS(drs, highestCohortDrs) >= 0 {
			highestCohortDrs = drs
			highestCohort = child
		}
	}
	return highestCohort, highestCohortDrs
}

// selectTarget descends into highestCohort, or returns highestCq, whichever
// has the higher DominantResourceShare. At least one of them is set.
func (t *TargetClusterQueueOrdering) selectTarget(
	highestCohort *schdcache.CohortSnapshot, highestCohortDrs schdcache.DRS,
	highestCq *schdcache.ClusterQueueSnapshot, highestCqDrs schdcache.DRS,
) *TargetClusterQueue {
	// we use >= because, as a tiebreak, choosing the Cohort seems
	// slightly more fair, as we can choose the most unfair node
	// within that Cohort.
	if schdcache.CompareDRS(highestCohortDrs, highestCqDrs) >= 0 {
		if logV := t.log.V(6); logV.Enabled() {
			logV.Info("Selected Cohort with highest DRS", "cohort", highestCohort.GetName(), "drs", highestCohortDrs)
		}
		return t.nextTarget(highestCohort)
	}
	if logV := t.log.V(6); logV.Enabled() {
		logV.Info("Selected ClusterQueue with highest DRS", "clusterQueue", highestCq.GetName(), "drs", highestCqDrs)
	}
	return &TargetClusterQueue{
		ordering: t,
		targetCq: highestCq,
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

func (t *TargetClusterQueueOrdering) candidateBorrowsFromLCA(cq *schdcache.ClusterQueueSnapshot, fr resources.FlavorResource) bool {
	if !cq.Borrowing(fr) {
		return false
	}
	for cohort := range cq.PathParentToRoot() {
		if t.preemptorAncestors.Has(cohort) {
			break
		}
		if !cohort.Borrowing(fr) {
			return false
		}
	}
	return true
}

func (t *TargetClusterQueueOrdering) candidateUsesBorrowedResource(cq *schdcache.ClusterQueueSnapshot, wl *workload.Info) bool {
	if cq == t.preemptorCq {
		return true
	}
	for _, ps := range wl.TotalRequests {
		for res, flv := range ps.Flavors {
			fr := resources.FlavorResource{Flavor: flv, Resource: res}
			if len(t.frsNeedPreemption) > 0 && !t.frsNeedPreemption.Has(fr) {
				continue
			}
			if t.candidateBorrowsFromLCA(cq, fr) {
				return true
			}
		}
	}
	return false
}

func (t *TargetClusterQueueOrdering) hasWorkload(cq *schdcache.ClusterQueueSnapshot) bool {
	cqName := cq.GetName()
	for len(t.clusterQueueToTarget[cqName]) > 0 {
		if t.candidateUsesBorrowedResource(cq, t.clusterQueueToTarget[cqName][0]) {
			return true
		}
		if logV := t.log.V(6); logV.Enabled() {
			logV.Info("Skipping candidate workload whose flavor-resources are not borrowed",
				"clusterQueue", cqName,
				"workload", klog.KObj(t.clusterQueueToTarget[cqName][0].Obj))
		}
		t.clusterQueueToTarget[cqName] = t.clusterQueueToTarget[cqName][1:]
	}
	return false
}

func (t *TargetClusterQueueOrdering) isBorrowing(drs schdcache.DRS) bool {
	if len(t.frsNeedPreemption) > 0 {
		return drs.IsBorrowingOnAny(t.frsNeedPreemption)
	}
	return drs.IsBorrowing()
}

// nextTarget is a recursive algorithm for finding the next
// TargetClusterQueue.  It finds the child with the highest DRS,
// returning it if it is a ClusterQueue, or entering the recursive
// call if it is a Cohort.  The return of nil doesn't mean that there
// are no more candidate ClusterQueues; an iteration may have only
// pruned nodes from the tree.
func (t *TargetClusterQueueOrdering) nextTarget(cohort *schdcache.CohortSnapshot) *TargetClusterQueue {
	// we can't prune the preemptor ClusterQueue itself,
	// until it runs out of candidates.
	highestCq, highestCqDrs := t.highestChildCq(cohort, func(*schdcache.ClusterQueueSnapshot) bool { return false }, func(cq *schdcache.ClusterQueueSnapshot, drs schdcache.DRS) bool {
		return (!t.isBorrowing(drs) && cq != t.preemptorCq) || !t.hasWorkload(cq)
	})
	highestCohort, highestCohortDrs := t.highestChildCohort(cohort, func(*schdcache.CohortSnapshot) bool { return false })

	// None of the children are valid candidates (i.e. all
	// children pruned), so this Cohort is pruned.
	if highestCohort == nil && highestCq == nil {
		t.prunedCohorts.Insert(cohort)
		if logV := t.log.V(6); logV.Enabled() {
			logV.Info("Pruning Cohort during Target ordering as all children are pruned", "cohort", cohort.GetName())
		}
		return nil
	}
	return t.selectTarget(highestCohort, highestCohortDrs, highestCq, highestCqDrs)
}
