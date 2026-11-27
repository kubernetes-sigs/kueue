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
	log                 logr.Logger
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

			// Respect DropQueue: a caller may drop the queue without
			// popping a workload, which would otherwise loop forever.
			for !t.prunedClusterQueues.Has(t.preemptorCq) && targetCq.HasWorkload() {
				if !yield(targetCq) {
					return
				}
			}
			return
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
// TargetClusterQueue.  It collects the children with the highest DRS
// and returns the target whose next candidate workload comes first in
// the candidates ordering, entering each such Cohort to find its
// target, so that the choice doesn't depend on map iteration order and
// respects priority.  The return of nil doesn't mean that there are no
// more candidate ClusterQueues; an iteration may have only pruned nodes
// from the tree.
func (t *TargetClusterQueueOrdering) nextTarget(parent *schdcache.CohortSnapshot) *TargetClusterQueue {
	var highestCqs []*schdcache.ClusterQueueSnapshot
	var highestCohorts []*schdcache.CohortSnapshot
	highestDrs := schdcache.NegativeDRS()
	keepHighest := func(drs schdcache.DRS) bool {
		cmp := schdcache.CompareDRS(drs, highestDrs)
		if cmp > 0 {
			highestDrs = drs
			highestCqs = highestCqs[:0]
			highestCohorts = highestCohorts[:0]
		}
		return cmp >= 0
	}

	for _, cq := range parent.ChildCQs() {
		if t.prunedClusterQueues.Has(cq) {
			continue
		}

		drs := cq.DominantResourceShare()
		// we can't prune the preemptor ClusterQueue itself,
		// until it runs out of candidates.
		if (!t.isBorrowing(drs) && cq != t.preemptorCq) || !t.hasWorkload(cq) {
			t.prunedClusterQueues.Insert(cq)
			if logV := t.log.V(6); logV.Enabled() {
				logV.Info("Pruning ClusterQueue during Target ordering", "clusterQueue", klog.KRef("", string(cq.GetName())), "drs", drs)
			}
		} else if keepHighest(drs) {
			highestCqs = append(highestCqs, cq)
		}
	}

	for _, cohort := range parent.ChildCohorts() {
		if t.prunedCohorts.Has(cohort) {
			continue
		}

		drs := cohort.DominantResourceShare()

		// we prune a Cohort when it is no longer borrowing.
		// Even when not borrowing, we can't prune a
		// Cohort on path from preemptor ClusterQueue to
		// root, as there may be imbalance within some
		// subtree, or a possible preemption within Preemptor
		// CQ itself.  We will only prune such a Cohort if all
		// of its children have been pruned.
		if !t.isBorrowing(drs) && !t.onPathFromRootToPreemptorCQ(cohort) {
			t.prunedCohorts.Insert(cohort)
			if logV := t.log.V(6); logV.Enabled() {
				logV.Info("Pruning Cohort during Target ordering", "cohort", klog.KRef("", string(cohort.GetName())), "drs", drs)
			}
		} else if keepHighest(drs) {
			highestCohorts = append(highestCohorts, cohort)
		}
	}

	// None of the children are valid candidates (i.e. all
	// children pruned), so this Cohort is pruned.
	if len(highestCqs) == 0 && len(highestCohorts) == 0 {
		t.prunedCohorts.Insert(parent)
		if logV := t.log.V(6); logV.Enabled() {
			logV.Info("Pruning Cohort during Target ordering as all children are pruned", "cohort", klog.KRef("", string(parent.GetName())))
		}
		return nil
	}

	highestCq := t.bestTargetOf(highestCqs, highestCohorts)
	if highestCq == nil {
		// The highest-DRS Cohorts got pruned. A lower-DRS child may
		// still have a target.
		return nil
	}
	if logV := t.log.V(6); logV.Enabled() {
		logV.Info("Selected ClusterQueue during Target ordering",
			"cohort", klog.KRef("", string(parent.GetName())),
			"clusterQueue", klog.KRef("", string(highestCq.GetName())), "childDRS", highestDrs)
	}
	return &TargetClusterQueue{ordering: t, targetCq: highestCq}
}

// bestTargetOf finds the next target in each of the Cohorts and
// returns, among these targets and the ClusterQueues, the one whose
// next candidate workload comes first in the candidates ordering.  A
// Cohort wins a tie with a ClusterQueue.  It returns nil when there
// are no ClusterQueues and all of the Cohorts got pruned instead.
func (t *TargetClusterQueueOrdering) bestTargetOf(
	cqs []*schdcache.ClusterQueueSnapshot,
	cohorts []*schdcache.CohortSnapshot,
) *schdcache.ClusterQueueSnapshot {
	var highestCq *schdcache.ClusterQueueSnapshot
	var currentCandWl *workload.Info
	now := t.clock.Now()
	consider := func(cq *schdcache.ClusterQueueSnapshot) {
		newCandWl := t.clusterQueueToTarget[cq.GetName()][0]
		if currentCandWl == nil ||
			policy.CandidatesOrdering(t.log, false, newCandWl, currentCandWl, t.preemptorCq.Name, now) < 0 {
			highestCq, currentCandWl = cq, newCandWl
		}
	}

	for _, cohort := range cohorts {
		if target := t.nextTargetWithin(cohort); target != nil {
			consider(target.targetCq)
		}
	}
	for _, cq := range cqs {
		consider(cq)
	}
	return highestCq
}

// nextTargetWithin returns the next target in the subtree of the
// Cohort, or nil if the Cohort got pruned.
func (t *TargetClusterQueueOrdering) nextTargetWithin(cohort *schdcache.CohortSnapshot) *TargetClusterQueue {
	for !t.prunedCohorts.Has(cohort) {
		if target := t.nextTarget(cohort); target != nil {
			return target
		}
	}
	return nil
}
