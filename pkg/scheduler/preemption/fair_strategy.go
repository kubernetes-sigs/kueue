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
	preemptioncommon "sigs.k8s.io/kueue/pkg/scheduler/preemption/common"
	"sigs.k8s.io/kueue/pkg/scheduler/preemption/fairsharing"
	"sigs.k8s.io/kueue/pkg/util/logging"
	"sigs.k8s.io/kueue/pkg/workload"
)

func fairPreemptionStrategy(
	ctx context.Context,
	preemptor *Preemptor,
	preemptionCtx *preemptionCtx,
	fsStrategies []fairsharing.Strategy,
) iter.Seq[PreemptionStrategy] {
	log := log.FromContext(ctx)
	allowBorrowing := true

	candidateWls := preemptor.findCandidates(log, preemptionCtx.preemptor.Obj, preemptionCtx.preemptorCQ, preemptionCtx.frsNeedPreemption)
	if len(candidateWls) == 0 {
		return func(yieldStrategy func(PreemptionStrategy) bool) {}
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

	candidatesIter := func(yieldCandidate func(*Target) bool) {
		var cont bool
		targetsInPreemptorCQ := false

		yieldedCandidates := make([]*Target, 0)

		// The incoming Workload's usage stays simulated while the candidates are
		// picked, because the DominantResourceShare values have to account for it.
		// This is hidden from the consumer, as we revert the simulated addition for the duration of the yield.
		wrapperYield := func(t *Target) bool {
			yieldedCandidates = append(yieldedCandidates, t)
			revert := preemptionCtx.preemptorCQ.SimulateUsageRemoval(preemptionCtx.workloadUsage)
			defer revert()
			return yieldCandidate(t)
		}

		revertSimulation := preemptionCtx.preemptorCQ.SimulateUsageAddition(preemptionCtx.workloadUsage)
		defer revertSimulation()

		candidateWls, cont = iterateWithFirstFsStrategy(log, preemptionCtx, candidateWls, fsStrategies[0], func(t *Target) bool {
			if t.WorkloadInfo.ClusterQueue == preemptionCtx.preemptorCQ.Name {
				targetsInPreemptorCQ = true
			}
			return wrapperYield(t)
		})

		if cont && features.Enabled(features.FairSharingReevaluatePreemptionCandidates) && targetsInPreemptorCQ {
			// If "targets" contains workload from the same CQ as the preemptor, it means
			// that DRS of the preemptor was decreased during the first run, and we can run
			// the same strategy again with remaining candidates as they have chance to
			// succeed now.
			// No need to run the strategy a third time as first run already iterated
			// though whole tree and removed all the preemptor's workloads.
			candidateWls, cont = iterateWithFirstFsStrategy(log, preemptionCtx, candidateWls, fsStrategies[0], wrapperYield)
		}

		if cont && len(fsStrategies) > 1 {
			if logV := log.V(6); logV.Enabled() {
				logV.Info("First fair sharing strategy failed, trying second strategy",
					"preemptingWorkload", klog.KObj(preemptionCtx.preemptor.Obj),
					"targets", logging.GetObjectReferences(yieldedCandidates),
					"retryCandidates", workload.References(candidateWls))
			}
			cont = iterateWithSecondFsStrategy(log, preemptionCtx, candidateWls, wrapperYield)
		}

		if cont && log.V(6).Enabled() {
			log.V(6).Info("All fair sharing candidates exhausted",
				"preemptingWorkload", klog.KObj(preemptionCtx.preemptor.Obj),
				"targets", logging.GetObjectReferences(yieldedCandidates))
		}
	}

	return func(yieldStrategy func(PreemptionStrategy) bool) {
		yieldStrategy(PreemptionStrategy{candidatesIter, allowBorrowing, preemptionCtx})
	}
}

// iterateWithFirstFsStrategy returns preemption candidates in an order based on
// the first configured FairSharing strategy,
// retryCandidates may be used if rule S2-b is configured.
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

// iterateWithSecondFsStrategy erturns preemption candidates in an order
// based on the Fair Sharing Rule S2-b.
func iterateWithSecondFsStrategy(
	log logr.Logger,
	preemptionCtx *preemptionCtx,
	retryCandidates []*workload.Info,
	yield func(*Target) bool,
) bool {
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
				return false
			}
		}
		// There doesn't seem to be an scenario where
		// it's possible to apply rule S2-b more than once in a CQ.
		ordering.DropQueue(candCQ)
	}
	return true
}
