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
	"context"
	"iter"
	"time"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/util/sets"
	"sigs.k8s.io/controller-runtime/pkg/log"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/scheduler/preemption/classical"
	"sigs.k8s.io/kueue/pkg/scheduler/preemption/policy"
	cmputil "sigs.k8s.io/kueue/pkg/util/cmp"
	utiltas "sigs.k8s.io/kueue/pkg/util/tas"
	"sigs.k8s.io/kueue/pkg/workload"
)

type candidateIterator interface {
	Reset()
	Next(borrow bool) (candidate *workload.Info, evictReason string)
}

// classicalPreemptionStrategy implements a heuristic to find a minimal set of Workloads
// to preempt.
// The heuristic first removes candidates, in the input order, while their
// ClusterQueues are still borrowing resources and while the incoming Workload
// doesn't fit in the quota.
func classicalPreemptionStrategy(ctx context.Context, preemptor *Preemptor, preemptionCtx *preemptionCtx) iter.Seq[PreemptionStrategy] {
	log := log.FromContext(ctx)
	hierarchicalReclaimCtx := &classical.HierarchicalPreemptionCtx{
		Log:               log,
		Wl:                preemptionCtx.preemptor.Obj,
		Cq:                preemptionCtx.preemptorCQ,
		FrsNeedPreemption: preemptionCtx.frsNeedPreemption,
		Requests:          preemptionCtx.workloadUsage.Quota.Assigned,
		WorkloadOrdering:  preemptor.workloadOrdering,
	}
	candidatesGenerator := classical.NewCandidateIterator(
		hierarchicalReclaimCtx,
		preemptor.enabledAfs,
		preemptionCtx.frsNeedPreemption,
		preemptionCtx.snapshot,
		preemptor.clock,
		policy.CandidatesOrdering,
	)
	var attemptPossibleOpts []preemptionAttemptOpts
	borrowWithinCohortForbidden, _ := classical.IsBorrowingWithinCohortForbidden(preemptionCtx.preemptorCQ)
	// We have three types of candidates:
	// 1. Hierarchy candidates. Candidates over which the incoming workload has a
	//    hierarchical advantage (it is closer to the quota used by the candidate).
	//    We can preempt such candidates regardless of their priority.
	// 2. Priority candidates. Candidates over which there is no hiearchical advantage
	//    but the possibility to preempt is determined based on priorities.
	//    We respect the BorrowWithinCohort configuration only for these candidates.
	// 3. Same queue candidates.
	// We can only preempt a priority candidate with priority > MaxPriorityThreshold
	// if the target CQ is not borrowing (by the definition of the MaxPriorityThreshold).
	// We sometimes need to consider both options allowBorrowing = true and false
	// (because with false we have more candidates but cannot use borrowing).
	// The order in which the options are considered is arbitrary and the condition
	// in which we try allowBorrowing=false before true is to keep compatibility with
	// previous versions.
	switch {
	case candidatesGenerator.NoCandidateFromOtherQueues() || (borrowWithinCohortForbidden && !queueUnderNominalInResourcesNeedingPreemption(preemptionCtx)):
		attemptPossibleOpts = []preemptionAttemptOpts{{true}}
	case borrowWithinCohortForbidden && candidatesGenerator.NoCandidateForHierarchicalReclaim():
		attemptPossibleOpts = []preemptionAttemptOpts{{false}, {true}}
	default:
		attemptPossibleOpts = []preemptionAttemptOpts{{true}, {false}}
	}

	return func(yieldStrategy func(PreemptionStrategy) bool) {
		for _, opts := range attemptPossibleOpts {
			allowBorrowing := opts.borrowing
			candidateIter := func(yieldCandidate func(*Target) bool) {
				cont := iterateOverCandidates(ctx, preemptionCtx, candidatesGenerator, allowBorrowing, yieldCandidate)
				if cont && features.Enabled(features.ConfigurablePreemptions) {
					preemptionCtx.configurableEvaluator.FindCandidates(
						preemptionCtx.snapshot,
						preemptionCtx.frsNeedPreemption,
						func() bool { return workloadQuotaFits(preemptionCtx, allowBorrowing) },
						yieldCandidate,
					)
				}
			}
			if !yieldStrategy(newPreemptionStrategy(candidateIter, allowBorrowing, preemptionCtx)) {
				return
			}
		}
		// WAS performs topology preemption in its scheduler simulator.
		if len(preemptionCtx.tasRequests) == 0 || features.Enabled(features.SchedulerLibraryDeepIntegration) {
			return
		}
		for _, opts := range attemptPossibleOpts {
			preferred := topologyCandidates(ctx, preemptionCtx, candidatesGenerator, opts.borrowing)
			if preferred == nil {
				continue
			}
			iterator := classical.NewUncachedCandidateIterator(
				hierarchicalReclaimCtx, preemptor.enabledAfs, preemptionCtx.frsNeedPreemption,
				preemptionCtx.snapshot, preemptor.clock,
				func(log logr.Logger, afsEnabled bool, a, b *workload.Info, cq kueue.ClusterQueueReference, now time.Time) int {
					return policy.CandidatesOrderingWithTieBreaker(log, afsEnabled, a, b, cq, now, func() int {
						return cmputil.CompareBool(preferred.Has(workload.Key(b.Obj)), preferred.Has(workload.Key(a.Obj)))
					})
				},
			)
			candidateIter := func(yieldCandidate func(*Target) bool) {
				iterateOverCandidates(ctx, preemptionCtx, iterator, opts.borrowing, yieldCandidate)
			}
			if !yieldStrategy(newPreemptionStrategy(candidateIter, opts.borrowing, preemptionCtx)) {
				return
			}
		}
	}
}

func iterateOverCandidates(
	ctx context.Context,
	preemptionCtx *preemptionCtx,
	iterator candidateIterator,
	allowBorrowing bool,
	yield func(*Target) bool,
) (cont bool) {
	yield = policy.YieldFromSnapshot(preemptionCtx.snapshot, yield)
	iterator.Reset()
	for candidateWl, reason := iterator.Next(allowBorrowing); candidateWl != nil; candidateWl, reason = iterator.Next(allowBorrowing) {
		candidate := &Target{
			WorkloadInfo: candidateWl,
			Reason:       reason,
			WorkloadCq:   preemptionCtx.snapshot.ClusterQueue(candidateWl.ClusterQueue),
		}
		if !yield(candidate) {
			return
		}
	}
	return true
}

// topologyCandidates finds one placement after temporarily removing all
// initially eligible candidates. This optimistic placement only guides ordering:
// the iterator rechecks eligibility as victims are removed in the actual attempt.
// Otherwise, newest-first removal can exhaust a queue's borrowed quota while
// leaving an older worker in every domain that could fit the incoming workload.
func topologyCandidates(ctx context.Context, pCtx *preemptionCtx, iterator candidateIterator, allowBorrowing bool) sets.Set[workload.Reference] {
	var candidates []*workload.Info
	iterator.Reset()
	for candidate, _ := iterator.Next(allowBorrowing); candidate != nil; candidate, _ = iterator.Next(allowBorrowing) {
		candidates = append(candidates, candidate)
	}
	if len(candidates) == 0 {
		return nil
	}
	restore := pCtx.snapshot.SimulateWorkloadRemoval(candidates)
	assignments := pCtx.preemptorCQ.FindTopologyAssignmentsForWorkload(ctx, pCtx.tasRequests, schdcache.WithWorkloadInfo(&pCtx.preemptor))
	restore()
	if assignments.Failure() != nil {
		return nil
	}
	domains := make(map[kueue.ResourceFlavorReference]sets.Set[utiltas.TopologyDomainID])
	for _, assignment := range assignments {
		if domains[assignment.Flavor] == nil {
			domains[assignment.Flavor] = sets.New[utiltas.TopologyDomainID]()
		}
		for _, domain := range assignment.TopologyAssignment.Domains {
			domains[assignment.Flavor].Insert(utiltas.DomainID(domain.Values))
		}
	}
	preferred := sets.New[workload.Reference]()
	for _, candidate := range candidates {
		for flavor, usage := range candidate.TASUsage() {
			for _, domain := range usage {
				if domains[flavor].Has(utiltas.DomainID(domain.Values)) {
					preferred.Insert(workload.Key(candidate.Obj))
				}
			}
		}
	}
	if len(preferred) == 0 {
		return nil
	}
	return preferred
}
