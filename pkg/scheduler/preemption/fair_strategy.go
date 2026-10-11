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
	"fmt"
	"iter"
	"slices"

	"github.com/go-logr/logr"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/log"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/scheduler/preemption/fairsharing"
	"sigs.k8s.io/kueue/pkg/scheduler/preemption/policy"
	"sigs.k8s.io/kueue/pkg/util/logging"
	"sigs.k8s.io/kueue/pkg/workload"
)

// fairPreemptionStrategy finds workloads to preempt using fair sharing rules.
//
// While picking candidates, it adds the incoming workload's usage to the
// preemptor queue so share comparisons reflect the state after admission. It
// temporarily removes this usage each time it yields a candidate so the caller
// can test if the workload fits.
//
// It yields candidates in up to three steps:
//  1. First fair sharing rule (usually LessThanOrEqualToFinalShare). If an own
//     workload is evicted and lowers the preemptor's share, this step can run
//     again when FairSharingReevaluatePreemptionCandidates is enabled.
//  2. Second fair sharing rule (LessThanInitialShare), if configured, for
//     candidates skipped in step 1.
//  3. Configurable preemption rules, if enabled.
//
// The returned strategy provides a validate function. Callers can use it to
// verify that a proposed set of preemption targets still satisfies at least one
// fair sharing rule against the snapshot state.
func fairPreemptionStrategy(
	ctx context.Context,
	preemptor *Preemptor,
	preemptionCtx *preemptionCtx,
	fsStrategies []fairsharing.Strategy,
) iter.Seq[PreemptionStrategy] {
	log := log.FromContext(ctx)
	allowBorrowing := true

	candidateWls := preemptor.findCandidates(log, preemptionCtx.preemptor.Obj, preemptionCtx.preemptorCQ, preemptionCtx.frsNeedPreemption)
	if noCandidates(preemptionCtx, candidateWls) {
		return func(yieldStrategy func(PreemptionStrategy) bool) {}
	}
	slices.SortFunc(candidateWls, preemptor.candidatesOrdering(log, preemptionCtx.preemptorCQ.Name))
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
		yieldAndRecord := func(t *Target) bool {
			yieldedCandidates = append(yieldedCandidates, t)
			return yieldCandidate(t)
		}

		// The incoming Workload's usage stays simulated while the candidates are
		// picked, because the DominantResourceShare values have to account for it.
		// This is hidden from the consumer, as we revert the simulated addition for the duration of the yield.
		yieldWithoutSimulatedUsage := func(t *Target) bool {
			revert := preemptionCtx.preemptorCQ.SimulateUsageRemoval(preemptionCtx.workloadUsage)
			defer revert()
			return yieldAndRecord(t)
		}

		revertSimulation := preemptionCtx.preemptorCQ.SimulateUsageAddition(preemptionCtx.workloadUsage)

		candidateWls, cont = iterateWithFirstFsStrategy(log, preemptionCtx, candidateWls, fsStrategies[0], func(t *Target) bool {
			if t.WorkloadInfo.ClusterQueue == preemptionCtx.preemptorCQ.Name {
				targetsInPreemptorCQ = true
			}
			return yieldWithoutSimulatedUsage(t)
		})

		if cont && features.Enabled(features.FairSharingReevaluatePreemptionCandidates) && targetsInPreemptorCQ {
			// If "targets" contains workload from the same CQ as the preemptor, it means
			// that DRS of the preemptor was decreased during the first run, and we can run
			// the same strategy again with remaining candidates as they have chance to
			// succeed now.
			// No need to run the strategy a third time as first run already iterated
			// though whole tree and removed all the preemptor's workloads.
			candidateWls, cont = iterateWithFirstFsStrategy(log, preemptionCtx, candidateWls, fsStrategies[0], yieldWithoutSimulatedUsage)
		}

		if cont && len(fsStrategies) > 1 {
			if logV := log.V(6); logV.Enabled() {
				logV.Info("First fair sharing strategy failed, trying second strategy",
					"preemptingWorkload", klog.KObj(preemptionCtx.preemptor.Obj),
					"targets", logging.GetObjectReferences(yieldedCandidates),
					"retryCandidates", workload.References(candidateWls))
			}
			cont = iterateWithSecondFsStrategy(log, preemptionCtx, candidateWls, yieldWithoutSimulatedUsage)
		}

		revertSimulation()

		if cont && features.Enabled(features.ConfigurablePreemptions) {
			interrupted := preemptionCtx.configurableEvaluator.FindCandidates(
				preemptionCtx.snapshot,
				&preemptionCtx.preemptor,
				preemptionCtx.frsNeedPreemption,
				func() bool { return workloadQuotaFits(preemptionCtx, allowBorrowing) },
				yieldAndRecord,
			)
			cont = !interrupted
		}

		if cont && log.V(6).Enabled() {
			log.V(6).Info("All fair sharing candidates exhausted",
				"preemptingWorkload", klog.KObj(preemptionCtx.preemptor.Obj),
				"targets", logging.GetObjectReferences(yieldedCandidates))
		}
	}

	return func(yieldStrategy func(PreemptionStrategy) bool) {
		yieldStrategy(newPreemptionStrategy(
			candidatesIter,
			allowBorrowing,
			preemptionCtx,
			withVerify(func(targets []*Target) (bool, string) {
				return verifyFairSharingTargets(preemptionCtx, targets, fsStrategies)
			}),
		))
	}
}

// verifyFairSharingTargets checks that every cross-ClusterQueue target in targets
// still satisfies at least one configured FairSharing strategy when evaluated
// against the final post-fill-back state (incoming workload admitted, all
// surviving targets removed).
//
// During candidate selection, preempting an intra-CQ workload lowers the
// preemptor's simulated DominantResourceShare, which can make a cross-CQ
// target appear fair. If fillBackWorkloads later restores that intra-CQ workload
// (or if it is immediately re-admitted in the next scheduling cycle), the
// preemptor's actual post-preemption share is higher than the share used to
// justify the cross-CQ preemption, causing a preemption loop between queues (#14543).
func verifyFairSharingTargets(preemptionCtx *preemptionCtx, targets []*Target, fsStrategies []fairsharing.Strategy) (bool, string) {
	if !features.Enabled(features.FairSharingVerifyFinalTargets) {
		return true, ""
	}

	revertSimulation := preemptionCtx.preemptorCQ.SimulateUsageAddition(preemptionCtx.workloadUsage)
	defer revertSimulation()

	withinNominal := features.Enabled(features.FairSharingPreemptWithinNominal) &&
		queueWithinNominalInResourcesNeedingPreemption(preemptionCtx)

	for _, t := range targets {
		if passed, reason := verifyFairSharingTarget(preemptionCtx, t, fsStrategies, withinNominal); !passed {
			return false, reason
		}
	}
	return true, ""
}

// verifyFairSharingTarget checks if evicting target t from its ClusterQueue is justified under
// at least one of the configured fair sharing strategies in the final state.
// It computes the preemptor's final share, the target queue's final share (TargetNew),
// and temporarily simulates restoring t's usage to compute TargetOld.
func verifyFairSharingTarget(
	preemptionCtx *preemptionCtx,
	t *Target,
	strategies []fairsharing.Strategy,
	withinNominal bool,
) (bool, string) {
	if t.Reason == kueue.InClusterQueueReason ||
		t.Reason == kueue.ConfigurablePreemptionReason ||
		(t.Reason == kueue.InCohortReclamationReason && withinNominal) {
		return true, ""
	}

	preemptorNode, targetNode := fairsharing.AlmostLCAs(preemptionCtx.preemptorCQ, t.WorkloadCq)
	preemptorShare := fairsharing.PreemptorNewShare(preemptorNode.DominantResourceShare())
	newShare := fairsharing.TargetNewShare(targetNode.DominantResourceShare())

	revert := t.WorkloadCq.SimulateUsageAddition(t.WorkloadInfo.Usage())
	oldShare := fairsharing.TargetOldShare(targetNode.DominantResourceShare())
	revert()

	for _, strategy := range strategies {
		if strategy(preemptorShare, oldShare, newShare) {
			return true, ""
		}
	}

	reason := fmt.Sprintf("target %s in %s violates fair sharing (preemptorShare=%s, targetOldShare=%s, targetNewShare=%s)",
		klog.KObj(t.WorkloadInfo.Obj),
		klog.KRef("", string(t.WorkloadCq.Name)),
		schdcache.DRS(preemptorShare).PreciseWeightedShareSerialized(),
		schdcache.DRS(oldShare).PreciseWeightedShareSerialized(),
		schdcache.DRS(newShare).PreciseWeightedShareSerialized())
	return false, reason
}

func noCandidates(preemptionCtx *preemptionCtx, candidates []*workload.Info) bool {
	// TODO(#15893): remove the configurable candidates phase from the Fair Sharing
	// algorithm once ConfigurablePreemption covers Fair Sharing and the two become
	// mutually exclusive.
	//
	// The configurable candidates are only evaluated once the strategies failed, so
	// their emptiness isn't known here; the presence of a rule is enough to keep going.
	return len(candidates) == 0 && (!features.Enabled(features.ConfigurablePreemptions) || !preemptionCtx.configurableEvaluator.HasRules())
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
	yield = policy.YieldFromSnapshot(preemptionCtx.snapshot, yield)
	ordering := fairsharing.MakeClusterQueueOrdering(preemptionCtx.preemptorCQ, candidates, preemptionCtx.frsNeedPreemption, log, preemptionCtx.clock)
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
			if !yield(&Target{WorkloadInfo: candWl, Reason: kueue.InClusterQueueReason, WorkloadCq: candCQ.GetTargetCq()}) {
				return
			}
			continue
		}

		if preemptorWithinNominal {
			candWl := candCQ.PopWorkload()
			if !yield(&Target{WorkloadInfo: candWl, Reason: kueue.InCohortReclamationReason, WorkloadCq: candCQ.GetTargetCq()}) {
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
				if !yield(&Target{WorkloadInfo: candWl, Reason: kueue.InCohortFairSharingReason, WorkloadCq: candCQ.GetTargetCq()}) {
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
	yield = policy.YieldFromSnapshot(preemptionCtx.snapshot, yield)
	ordering := fairsharing.MakeClusterQueueOrdering(preemptionCtx.preemptorCQ, retryCandidates, preemptionCtx.frsNeedPreemption, log, preemptionCtx.clock)
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
			if !yield(&Target{WorkloadInfo: candWl, Reason: kueue.InCohortFairSharingReason, WorkloadCq: candCQ.GetTargetCq()}) {
				return false
			}
		}
		// There doesn't seem to be an scenario where
		// it's possible to apply rule S2-b more than once in a CQ.
		ordering.DropQueue(candCQ)
	}
	return true
}
