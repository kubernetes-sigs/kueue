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
	"slices"

	"github.com/go-logr/logr"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/log"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/scheduler/flavorassigner"
	"sigs.k8s.io/kueue/pkg/scheduler/preemption/classical"
	preemptioncommon "sigs.k8s.io/kueue/pkg/scheduler/preemption/common"
	"sigs.k8s.io/kueue/pkg/scheduler/preemption/fairsharing"
	"sigs.k8s.io/kueue/pkg/workload"
)

type PreemptionType string

const (
	FairPreemptions      PreemptionType = "Fair"
	ClassicalPreemptions PreemptionType = "Classical"
)

// PreemptionStrategy represents a singular set of ordered potential preemption candidates.
// One strategy maps to a signle, isolated attempt at finding a possible preemption result.
type PreemptionStrategy struct {
	Candidates iter.Seq[*Target]
	Borrowing  bool
}

// PreemptionPlan defines a set of alternate strategies to be attempted when finding a preemption result.
// Possible types: Fair and Classical preemption plan.
type PreemptionPlan struct {
	Strategies iter.Seq[PreemptionStrategy]
	Type       PreemptionType
	pCtx       *preemptionCtx
}

type PreemptionPlanFactory func(ctx context.Context, assignment *flavorassigner.Assignment) PreemptionPlan

func ClassicalPreemptionPlan(ctx context.Context, preemptor *Preemptor, preemptionCtx *preemptionCtx) PreemptionPlan {
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

	return PreemptionPlan{func(yieldStrategy func(PreemptionStrategy) bool) {
		for _, opts := range attemptPossibleOpts {
			allowBorrowing := opts.borrowing
			if !yieldStrategy(PreemptionStrategy{func(yieldCandidate func(*Target) bool) {
				candidatesGenerator.Reset()
				for candidateWl, reason := candidatesGenerator.Next(allowBorrowing); candidateWl != nil; candidateWl, reason = candidatesGenerator.Next(allowBorrowing) {
					if !yieldCandidate(&Target{candidateWl, reason, preemptionCtx.snapshot.ClusterQueue(candidateWl.ClusterQueue)}) {
						return
					}
				}
			}, allowBorrowing}) {
				return
			}
		}
	}, ClassicalPreemptions, preemptionCtx}
}

func FairPreemptionPlan(
	ctx context.Context,
	preemptor *Preemptor,
	preemptionCtx *preemptionCtx,
	fsStrategies []fairsharing.Strategy,
) PreemptionPlan {
	log := log.FromContext(ctx)
	allowBorrowing := true

	candidateWls := preemptor.findCandidates(log, preemptionCtx.preemptor.Obj, preemptionCtx.preemptorCQ, preemptionCtx.frsNeedPreemption)
	if len(candidateWls) == 0 {
		return PreemptionPlan{func(yieldStrategy func(PreemptionStrategy) bool) {}, FairPreemptions, preemptionCtx}
	}
	slices.SortFunc(candidateWls, func(a, b *workload.Info) int {
		return preemptioncommon.CandidatesOrdering(log, preemptor.enabledAfs, a, b, preemptionCtx.preemptorCQ.Name, preemptor.clock.Now())
	})
	if logV := log.V(5); logV.Enabled() {
		logV.Info(
			"Simulating fair preemption",
			"candidates",
			workload.References(candidateWls),
			"resourcesRequiringPreemption",
			preemptionCtx.frsNeedPreemption.UnsortedList(),
			"preemptingWorkload",
			klog.KObj(preemptionCtx.preemptor.Obj),
		)
	}

	return PreemptionPlan{func(yieldStrategy func(PreemptionStrategy) bool) {
		yieldStrategy(PreemptionStrategy{
			func(yieldCandidate func(*Target) bool) {
				var cont bool
				targetsInPreemptorCQ := false

				candidateWls, cont = iterateWithFirstFsStrategy(log, preemptionCtx, candidateWls, fsStrategies[0], func(t *Target) bool {
					if t.WorkloadInfo.ClusterQueue == preemptionCtx.preemptorCQ.Name {
						targetsInPreemptorCQ = true
					}
					return yieldCandidate(t)
				})

				if cont && features.Enabled(features.FairSharingReevaluatePreemptionCandidates) && targetsInPreemptorCQ {
					candidateWls, cont = iterateWithFirstFsStrategy(log, preemptionCtx, candidateWls, fsStrategies[0], yieldCandidate)
				}

				// Use the second fair sharing strategy.
				if cont && len(fsStrategies) > 1 {
					iterateWithSecondFsStrategy(log, preemptionCtx, candidateWls, yieldCandidate)
				}
			},
			allowBorrowing,
		})
	}, FairPreemptions, preemptionCtx}
}

func iterateWithFirstFsStrategy(
	log logr.Logger,
	preemptionCtx *preemptionCtx,
	candidates []*workload.Info,
	fsStrategy fairsharing.Strategy,
	yield func(*Target) bool,
) (retryCandidates []*workload.Info, cont bool) {
	ordering := fairsharing.MakeClusterQueueOrdering(preemptionCtx.preemptorCQ, candidates, log, preemptionCtx.clock)
	// If the preemptor CQ stays within nominal quota for the contested
	// resources (including the incoming workload, already simulated),
	// preemption is allowed regardless of DRS (nominal entitlement).
	// When true, all cross-CQ candidates are preempted unconditionally
	// (bypassing the strategy check), so no retryCandidates are produced
	// and iterateWithSecondFsStrategy has nothing to do.
	preemptorWithinNominal := features.Enabled(features.FairSharingPreemptWithinNominal) &&
		queueWithinNominalInResourcesNeedingPreemption(preemptionCtx)
	for candCQ := range ordering.Iter() {
		if candCQ.InClusterQueuePreemption() {
			candWl := candCQ.PopWorkload()
			preemptionCtx.snapshot.RemoveWorkload(candWl)
			if !yield(&Target{candWl, kueue.InClusterQueueReason, candCQ.GetTargetCq()}) {
				return
			}
			continue
		}

		if preemptorWithinNominal {
			candWl := candCQ.PopWorkload()
			preemptionCtx.snapshot.RemoveWorkload(candWl)
			if !yield(&Target{candWl, kueue.InCohortReclamationReason, candCQ.GetTargetCq()}) {
				return
			}
			continue
		}

		preemptorNewShare, targetOldShare := candCQ.ComputeShares()
		if fsStrategyUnsatisfiable(preemptorNewShare, targetOldShare) {
			// No candidate in this ClusterQueue can pass the strategy, so
			// skip the per-candidate simulation. The candidates are still
			// collected for rule S2-b, which recomputes the shares on a
			// snapshot that this loop may have changed in the meantime.
			if logV := log.V(4); logV.Enabled() {
				logV.Info("Skipping FairSharing strategy evaluation, no candidate can pass",
					"preemptorNewShare", schdcache.DRS(preemptorNewShare).PreciseWeightedShareSerialized(),
					"targetClusterQueue", klog.KRef("", string(candCQ.GetTargetCq().Name)),
					"targetOldShare", schdcache.DRS(targetOldShare).PreciseWeightedShareSerialized())
			}
			for candCQ.HasWorkload() {
				retryCandidates = append(retryCandidates, candCQ.PopWorkload())
			}
			continue
		}
		strategyLog := newFsStrategyLog(log, candCQ, preemptorNewShare, targetOldShare)
		for candCQ.HasWorkload() {
			candWl := candCQ.PopWorkload()
			targetNewShare := candCQ.ComputeTargetShareAfterRemoval(candWl)
			passed := fsStrategy(preemptorNewShare, targetOldShare, targetNewShare)
			strategyLog.record(candWl, targetNewShare, passed)
			if passed {
				preemptionCtx.snapshot.RemoveWorkload(candWl)
				if !yield(&Target{candWl, kueue.InCohortFairSharingReason, candCQ.GetTargetCq()}) {
					strategyLog.flush()
					return
				}
				// Might need to pick a different CQ due to changing values.
				break
			} else {
				retryCandidates = append(retryCandidates, candWl)
			}
		}
		strategyLog.flush()
	}
	return retryCandidates, true
}

func iterateWithSecondFsStrategy(
	log logr.Logger,
	preemptionCtx *preemptionCtx,
	retryCandidates []*workload.Info,
	yield func(*Target) bool,
) {
	ordering := fairsharing.MakeClusterQueueOrdering(preemptionCtx.preemptorCQ, retryCandidates, log, preemptionCtx.clock)
	for candCQ := range ordering.Iter() {
		preemptorNewShare, targetOldShare := candCQ.ComputeShares()
		passed := fairsharing.LessThanInitialShare(preemptorNewShare, targetOldShare, fairsharing.TargetNewShare{})
		// The criteria doesn't depend on the preempted workload, so just preempt the first candidate.
		candWl := candCQ.PopWorkload()
		if logV := log.V(4); logV.Enabled() {
			logV.Info("Evaluating FairSharing strategy",
				"preemptorNewShare", schdcache.DRS(preemptorNewShare).PreciseWeightedShareSerialized(),
				"targetClusterQueue", klog.KRef("", string(candCQ.GetTargetCq().Name)),
				"targetWorkload", klog.KObj(candWl.Obj),
				"targetOldShare", schdcache.DRS(targetOldShare).PreciseWeightedShareSerialized(),
				"strategyPassed", passed)
		}
		// Due to API validation, we can only reach here if the second strategy is LessThanInitialShare,
		// in which case the last parameter for the strategy function is irrelevant.
		if passed {
			preemptionCtx.snapshot.RemoveWorkload(candWl)
			if !yield(&Target{candWl, kueue.InCohortFairSharingReason, candCQ.GetTargetCq()}) {
				return
			}
		}
		// There doesn't seem to be an scenario where
		// it's possible to apply rule S2-b more than once in a CQ.
		ordering.DropQueue(candCQ)
	}
}
