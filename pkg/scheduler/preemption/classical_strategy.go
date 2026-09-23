package preemption

import (
	"context"
	"iter"

	"sigs.k8s.io/controller-runtime/pkg/log"
	"sigs.k8s.io/kueue/pkg/scheduler/preemption/classical"
	preemptioncommon "sigs.k8s.io/kueue/pkg/scheduler/preemption/common"
)

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
		preemptioncommon.CandidatesOrdering,
	)
	var attemptPossibleOpts []preemptionAttemptOpts
	borrowWithinCohortForbidden, _ := classical.IsBorrowingWithinCohortForbidden(preemptionCtx.preemptorCQ)
	// We have three types of candidates:
	// 1. Hierarchy candidates. Candidates over which the incoming workload has a
	// 	  hierarchical advantage (it is closer to the quota used by the candidate).
	//    We can preempt such candidates regardless of their priority.
	// 2. Priority candidates. Candidates over which there is no hiearchical advantage
	//    but the possibility to preempt is determined based on priorities.
	// 	  We respect the BorrowWithinCohort configuration only for these candidates.
	// 3. Same queue candidates.
	// We can only preempt a priority candidate with priority > MaxPriorityThreshold
	// if the target CQ is not borrowing (by the definition of the MaxPriorityThreshold).
	// We sometimes need to consider both options allowBorrowing = true and false
	// (because with false we have more candidates but cannot use borrowing).
	// The order in which the options are considered is arbitrary and the condition
	// in which we try allowBorrowing=false before true is to keep compatibility with
	// previous versions.
	switch {
	case candidatesGenerator.NoCandidateFromOtherQueues || (borrowWithinCohortForbidden && !queueUnderNominalInResourcesNeedingPreemption(preemptionCtx)):
		attemptPossibleOpts = []preemptionAttemptOpts{{true}}
	case borrowWithinCohortForbidden && candidatesGenerator.NoCandidateForHierarchicalReclaim:
		attemptPossibleOpts = []preemptionAttemptOpts{{false}, {true}}
	default:
		attemptPossibleOpts = []preemptionAttemptOpts{{true}, {false}}
	}

	return func(yieldStrategy func(PreemptionStrategy) bool) {
		for _, opts := range attemptPossibleOpts {
			allowBorrowing := opts.borrowing

			candidateIter := func(yieldCandidate func(*Target) bool) {
				candidatesGenerator.Reset()
				for candidateWl, reason := candidatesGenerator.Next(allowBorrowing); candidateWl != nil; candidateWl, reason = candidatesGenerator.Next(allowBorrowing) {
					candidate := &Target{candidateWl, reason, preemptionCtx.snapshot.ClusterQueue(candidateWl.ClusterQueue)}
					preemptionCtx.snapshot.RemoveWorkload(candidateWl)
					if !yieldCandidate(candidate) {
						return
					}
				}
			}

			if !yieldStrategy(PreemptionStrategy{candidateIter, allowBorrowing, preemptionCtx}) {
				return
			}
		}
	}
}
