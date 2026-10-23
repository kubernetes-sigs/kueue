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
	"cmp"
	"context"
	"fmt"
	"iter"
	"slices"
	"sort"
	"strings"
	"sync/atomic"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/tools/events"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"
	"k8s.io/utils/clock"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	config "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/metrics"
	"sigs.k8s.io/kueue/pkg/resources"
	"sigs.k8s.io/kueue/pkg/scheduler/flavorassigner"
	"sigs.k8s.io/kueue/pkg/scheduler/preemption/classical"
	configurable "sigs.k8s.io/kueue/pkg/scheduler/preemption/config"
	"sigs.k8s.io/kueue/pkg/scheduler/preemption/fairsharing"
	"sigs.k8s.io/kueue/pkg/scheduler/preemption/policy"
	"sigs.k8s.io/kueue/pkg/util/expectations"
	"sigs.k8s.io/kueue/pkg/util/logging"
	"sigs.k8s.io/kueue/pkg/util/priority"
	"sigs.k8s.io/kueue/pkg/util/roletracker"
	"sigs.k8s.io/kueue/pkg/util/routine"
	"sigs.k8s.io/kueue/pkg/workload"
	workloadevict "sigs.k8s.io/kueue/pkg/workload/evict"
	workloadpatching "sigs.k8s.io/kueue/pkg/workload/patching"
)

const parallelPreemptions = 8

type Preemptor struct {
	clock clock.Clock

	client   client.Client
	recorder events.EventRecorder

	workloadOrdering  workload.Ordering
	enableFairSharing bool
	fsStrategies      []fairsharing.Strategy

	enabledAfs             bool
	roleTracker            *roletracker.RoleTracker
	customLabels           *metrics.CustomLabels
	preemptionExpectations *expectations.Store
}

// PreemptionStrategy represents a singular set of ordered potential preemption candidates.
// One strategy maps to a single, isolated attempt at finding a possible preemption result.
type PreemptionStrategy struct {
	// candidates is a dynamic iterator over preemption candidates
	// in order of decreasig preemption appeal.
	candidates iter.Seq[*Target]
	// allowBorrowing determines wheteher borrowing is enabled in the scope of this strategy.
	allowBorrowing bool
	// pCtx represents the active preemption context.
	// The context is shared across all iterations of this strategy's candidates.
	// Warning: Eeach time a candidate is yielded, it is preempted from the active context.
	pCtx *preemptionCtx
	// verify checks whether the final target set satisfies this strategy's invariants.
	// If nil, any target set that fits is valid. It returns whether the target set is valid
	// and an optional failure reason.
	verify func(targets []*Target) (bool, string)
}

type strategyOption func(*PreemptionStrategy)

func withVerify(verify func(targets []*Target) (bool, string)) strategyOption {
	return func(ps *PreemptionStrategy) {
		ps.verify = verify
	}
}

func newPreemptionStrategy(
	candidates iter.Seq[*Target],
	allowBorrowing bool,
	pCtx *preemptionCtx,
	opts ...strategyOption,
) PreemptionStrategy {
	ps := PreemptionStrategy{
		candidates:     candidates,
		allowBorrowing: allowBorrowing,
		pCtx:           pCtx,
	}
	for _, opt := range opts {
		opt(&ps)
	}

	return ps
}

type preemptionCtx struct {
	clock                 clock.Clock
	preemptor             workload.Info
	preemptorCQ           *schdcache.ClusterQueueSnapshot
	snapshot              *schdcache.Snapshot
	workloadUsage         workload.Usage
	tasRequests           schdcache.WorkloadTASRequests
	frsNeedPreemption     sets.Set[resources.FlavorResource]
	configurableEvaluator *configurable.PreemptionEvaluator
}

func New(
	cl client.Client,
	workloadOrdering workload.Ordering,
	recorder events.EventRecorder,
	fs *config.FairSharing,
	enabledAfs bool,
	clock clock.Clock,
	tracker *roletracker.RoleTracker,
	preemptionExpectations *expectations.Store,
	customLabels *metrics.CustomLabels,
) *Preemptor {
	p := &Preemptor{
		clock:                  clock,
		client:                 cl,
		recorder:               recorder,
		workloadOrdering:       workloadOrdering,
		enableFairSharing:      fairsharing.Enabled(fs),
		fsStrategies:           parseStrategies(fs),
		enabledAfs:             enabledAfs,
		roleTracker:            tracker,
		customLabels:           customLabels,
		preemptionExpectations: preemptionExpectations,
	}
	return p
}

type Target = policy.Target

// PartialPreemptionTarget describes a partial-preemption reduction of a single PodSet.
type PartialPreemptionTarget = policy.PartialPreemptionTarget

// ensures that Target implements ObjectRefProvider interface at compile time
var _ logging.ObjectRefProvider = (*Target)(nil)

func (p *Preemptor) GetPreemptionStrategyIterator(
	ctx context.Context,
	wl workload.Info,
	snapshot *schdcache.Snapshot,
	assignment flavorassigner.Assignment,
) iter.Seq[PreemptionStrategy] {
	return p.getPreemptionStrategyIterator(ctx, p.buildContext(ctx, wl, assignment, snapshot))
}

func (p *Preemptor) getPreemptionStrategyIterator(ctx context.Context, preemptionCtx *preemptionCtx) iter.Seq[PreemptionStrategy] {
	if p.enableFairSharing {
		return fairPreemptionStrategy(ctx, p, preemptionCtx, p.fsStrategies)
	}
	return classicalPreemptionStrategy(ctx, p, preemptionCtx)
}

// GetTargetsWithStrategy returns the workloads to evict using the provided preemption strategies.
func (p *Preemptor) GetTargetsWithStrategy(ctx context.Context, strategies iter.Seq[PreemptionStrategy]) []*Target {
	return p.getTargets(ctx, strategies)
}

func (p *Preemptor) buildContext(
	ctx context.Context,
	wl workload.Info,
	assignment flavorassigner.Assignment,
	snapshot *schdcache.Snapshot,
) *preemptionCtx {
	log := log.FromContext(ctx)
	cq := snapshot.ClusterQueue(wl.ClusterQueue)

	var tasRequests schdcache.WorkloadTASRequests
	if features.Enabled(features.TopologyAwareScheduling) {
		tasRequests = assignment.WorkloadsTopologyRequests(log, &wl, cq)
	}

	return &preemptionCtx{
		clock:             p.clock,
		preemptor:         wl,
		preemptorCQ:       cq,
		snapshot:          snapshot,
		tasRequests:       tasRequests,
		frsNeedPreemption: flavorResourcesNeedPreemption(assignment),
		workloadUsage: workload.Usage{
			Quota: workload.ResourceUsage{
				Assigned: assignment.TotalRequestsFor(log, &wl),
			},
			TAS: wl.TASUsage(),
		},
		configurableEvaluator: p.newConfigurableEvaluator(ctx, log, cq, &wl),
	}
}

// Resolved once per attempt: both algorithms evaluate several triggers, and the
// PreemptionConfig must not be re-read for each of them.
// Returns nil if the ConfigurablePreemptions feature is disabled, or the ClusterQueue
// references no PreemptionConfig.
func (p *Preemptor) newConfigurableEvaluator(ctx context.Context, log logr.Logger, cq *schdcache.ClusterQueueSnapshot, preemptor *workload.Info) *configurable.PreemptionEvaluator {
	if !features.Enabled(features.ConfigurablePreemptions) || cq == nil || cq.PreemptionConfigName == nil {
		return nil
	}
	return configurable.NewEvaluatorForPreemptionConfig(ctx, log, p.client, *cq.PreemptionConfigName, preemptor, p.candidatesOrdering(log, cq.Name))
}

// candidatesOrdering returns the order in which the preemption candidates are
// considered for a preemptor of the given ClusterQueue.
func (p *Preemptor) candidatesOrdering(log logr.Logger, cq kueue.ClusterQueueReference) func(a, b *workload.Info) int {
	return func(a, b *workload.Info) int {
		return policy.CandidatesOrdering(log, p.enabledAfs, a, b, cq, p.clock.Now())
	}
}

var HumanReadablePreemptionReasons = map[string]string{
	kueue.InClusterQueueReason:                "prioritization in the ClusterQueue",
	kueue.InCohortReclamationReason:           "reclamation within the cohort",
	kueue.InCohortFairSharingReason:           "Fair Sharing within the cohort",
	kueue.InCohortReclaimWhileBorrowingReason: "reclamation within the cohort while borrowing",
	kueue.ConfigurablePreemptionReason:        "the configured preemption rules",
	"":                                        "UNKNOWN",
}

func priorityInfo(log logr.Logger, w *kueue.Workload) (effectivePri int64, basePri, boost int32) {
	basePri = priority.Priority(w)
	effectivePri = priority.EffectivePriority(log, w)
	boost = int32(effectivePri - int64(basePri))
	return effectivePri, basePri, boost
}

func preemptionMessage(preemptor *kueue.Workload, reason, preemptorPath, preempteePath string) string {
	wUID := cmp.Or(string(preemptor.UID), "UNKNOWN")
	uid := preemptor.Labels[constants.JobUIDLabel]
	jUID := cmp.Or(uid, "UNKNOWN")
	preemptorMsgPath := cmp.Or(preemptorPath, "UNKNOWN")
	preempteeMsgPath := cmp.Or(preempteePath, "UNKNOWN")
	return fmt.Sprintf(
		"Preempted to accommodate a workload (UID: %s, JobUID: %s) due to %s; preemptor path: %s; preemptee path: %s",
		wUID,
		jUID,
		HumanReadablePreemptionReasons[reason],
		preemptorMsgPath,
		preempteeMsgPath,
	)
}

func (p *Preemptor) SatisfyPreemptionExpectation(log logr.Logger, wl *kueue.Workload) {
	targetKey := types.NamespacedName{Name: wl.Name, Namespace: wl.Namespace}
	p.preemptionExpectations.ObservedUID(log, targetKey, wl.UID)
}

// IssuePreemptions marks the target workloads as evicted.
func (p *Preemptor) IssuePreemptions(
	ctx context.Context,
	cache *schdcache.Cache,
	preemptor *workload.Info,
	targets []*Target,
	snap *schdcache.ClusterQueueSnapshot,
) (preempted int, failedPreemptions int, exampleError error) {
	log := ctrl.LoggerFrom(ctx)
	errCh := routine.NewErrorChannel()
	ctx, cancel := context.WithCancel(ctx)
	var successfullyPreempted atomic.Int64
	var preemptionErrors atomic.Int64
	defer cancel()
	workqueue.ParallelizeUntil(ctx, parallelPreemptions, len(targets), func(i int) {
		target := targets[i]
		if target.PartialPreemption != nil {
			// Partial preemption: request the elastic victim to scale its PodSet down to
			// ReclaimTargetCount instead of evicting it. We only set the request (reclaimTargetCount);
			// the quota-accounted count follows down as the shed pods actually terminate, so
			// quota is not released here. The preemptor stays pending until the quota frees.
			pp := target.PartialPreemption
			if targetCount, ok := workload.ReclaimTargetCount(target.WorkloadInfo.Obj, pp.PodSet); ok && targetCount == pp.ReclaimTargetCount {
				// Even though do not update reclaimTargetCount here, we still need to update the
				// successfullyPreempted counter to avoid workload to enter inadmissibleWorks.
				successfullyPreempted.Add(1)
				return
			}
			wlCopy := target.WorkloadInfo.Obj.DeepCopy()
			if err := workloadpatching.PatchAdmissionStatus(ctx, p.client, wlCopy, p.clock, func(wl *kueue.Workload) (bool, error) {
				return workload.SetReclaimTargetCount(wl, pp.PodSet, pp.ReclaimTargetCount), nil
			}, workloadpatching.WithLooseOnApply()); err != nil {
				errCh.SendErrorWithCancel(err, cancel)
				preemptionErrors.Add(1)
				return
			}
			log.V(3).Info("Partial preemption: requested scale-down",
				"targetWorkload", klog.KObj(target.WorkloadInfo.Obj), "podSet", pp.PodSet,
				"ReclaimTargetCount", pp.ReclaimTargetCount, "preemptingWorkload", klog.KObj(preemptor.Obj))
			p.recorder.Eventf(target.WorkloadInfo.Obj, nil, corev1.EventTypeNormal, "PartiallyPreempted", "PartiallyPreempted",
				"Requested scale-down of PodSet %q to %d to accommodate workload %s",
				pp.PodSet, pp.ReclaimTargetCount, klog.KObj(preemptor.Obj))
			successfullyPreempted.Add(1)
			return
		}

		targetKey := types.NamespacedName{Name: target.WorkloadInfo.Obj.Name, Namespace: target.WorkloadInfo.Obj.Namespace}
		if workloadevict.IsEvicted(target.WorkloadInfo.Obj) {
			log.V(3).Info("Preemption ongoing", "targetWorkload", klog.KObj(target.WorkloadInfo.Obj), "preemptingWorkload", klog.KObj(preemptor.Obj))
			successfullyPreempted.Add(1)
			p.preemptionExpectations.ObservedUID(log, targetKey, target.WorkloadInfo.Obj.UID)
			return
		}
		if !p.preemptionExpectations.Satisfied(log, targetKey) {
			log.V(3).Info("Preemption already issued, waiting for observation",
				"targetWorkload", klog.KObj(target.WorkloadInfo.Obj),
				"preemptingWorkload", klog.KObj(preemptor.Obj))
			successfullyPreempted.Add(1)
			return
		}

		preemptorPath := buildCQPath(string(preemptor.ClusterQueue), snap)
		preempteePath := buildCQPath(string(target.WorkloadInfo.ClusterQueue), target.WorkloadCq)

		p.preemptionExpectations.ExpectUIDs(log, targetKey, []types.UID{target.WorkloadInfo.Obj.UID})

		message, underlyingCause := messageAndUnderlyingCause(log, target, preemptor, preemptorPath, preempteePath)

		wlCopy := target.WorkloadInfo.Obj.DeepCopy()
		exposeLqMetrics := cache.ShouldExposeLocalQueueMetricsForWorkload(log, wlCopy)
		err := workloadevict.Evict(
			ctx, p.client, p.recorder, wlCopy, kueue.WorkloadEvictedByPreemption, message, underlyingCause, p.clock, exposeLqMetrics, p.roleTracker, p.customLabels,
			workloadevict.WithCustomPrepare(func(wl *kueue.Workload) {
				workload.SetPreemptedCondition(wl, p.clock.Now(), target.Reason, message)
			}),
			workloadevict.WithLooseOnApply(), workloadevict.WithRetryOnConflict(),
		)
		if err != nil {
			p.preemptionExpectations.ObservedUID(log, targetKey, target.WorkloadInfo.Obj.UID)
			errCh.SendErrorWithCancel(err, cancel)
			preemptionErrors.Add(1)
			return
		}
		preemptorEffPri, preemptorBase, preemptorBoost := priorityInfo(log, preemptor.Obj)
		targetEffPri, targetBase, targetBoost := priorityInfo(log, target.WorkloadInfo.Obj)
		log.V(3).Info("Preempted", "targetWorkload", klog.KObj(target.WorkloadInfo.Obj), "preemptingWorkload", klog.KObj(preemptor.Obj), "preemptorUID", string(preemptor.Obj.UID),
			"preemptorJobUID", preemptor.Obj.Labels[constants.JobUIDLabel], "reason", target.Reason, "message", message, "targetClusterQueue", klog.KRef("", string(target.WorkloadInfo.ClusterQueue)),
			"preemptorPath", preemptorPath, "preempteePath", preempteePath,
			"preemptorEffectivePriority", preemptorEffPri, "preemptorBoost", preemptorBoost,
			"targetEffectivePriority", targetEffPri, "targetBoost", targetBoost)
		p.recorder.Eventf(target.WorkloadInfo.Obj, nil, corev1.EventTypeNormal, "Preempted", "Preempted",
			message+fmt.Sprintf("; preemptor effective priority: %d (base: %d, boost: %d); preemptee effective priority: %d (base: %d, boost: %d)",
				preemptorEffPri, preemptorBase, preemptorBoost, targetEffPri, targetBase, targetBoost))
		p.recorder.Eventf(preemptor.Obj, nil, corev1.EventTypeNormal, "PreemptedWorkload", "PreemptedWorkload",
			"Preempted workload %s (UID: %s) in ClusterQueue %s; preemptor effective priority: %d (base: %d, boost: %d); preemptee effective priority: %d (base: %d, boost: %d)",
			klog.KObj(target.WorkloadInfo.Obj), target.WorkloadInfo.Obj.UID, target.WorkloadInfo.ClusterQueue,
			preemptorEffPri, preemptorBase, preemptorBoost, targetEffPri, targetBase, targetBoost)
		workloadevict.ReportPreemption(preemptor.ClusterQueue, target.Reason, target.WorkloadInfo.ClusterQueue, p.roleTracker, p.customLabels)
		successfullyPreempted.Add(1)
	})
	return int(successfullyPreempted.Load()), int(preemptionErrors.Load()), errCh.ReceiveError()
}

func messageAndUnderlyingCause(
	log logr.Logger,
	target *Target,
	preemptor *workload.Info,
	preemptorPath string,
	preempteePath string,
) (string, kueue.EvictionUnderlyingCause) {
	if target.Reason == kueue.ConfigurablePreemptionReason {
		if target.ConfigurablePreemptionReasonData != nil {
			message := target.ConfigurablePreemptionReasonData.EvictionMessage(preemptor.Obj)
			underlyingCause := kueue.EvictionUnderlyingCause(target.ConfigurablePreemptionReasonData.ConfigName)

			return message, underlyingCause
		}
		log.Error(nil, "ConfigurablePreemptionReasonData is nil",
			"targetWorkload", klog.KObj(target.WorkloadInfo.Obj),
			"preemptingWorkload", klog.KObj(preemptor.Obj))
		// fallback to default behavior
	}

	message := preemptionMessage(preemptor.Obj, target.Reason, preemptorPath, preempteePath)
	return message, ""
}

type preemptionAttemptOpts struct {
	borrowing bool
}

// getTargets iterates over preemption strategies, each providing an ordered
// list of preemption candidates.
// It uses the context provided by the strategy, from which the yielded
// candidates are already removed, to determine if the workload fits after
// preempting the candidates so far.
// Once the Workload fits, the heuristic tries to add Workloads back, in the
// reverse order in which they were removed, while the incoming Workload still
// fits.
func (p *Preemptor) getTargets(ctx context.Context, strategies iter.Seq[PreemptionStrategy]) []*Target {
	log := log.FromContext(ctx)
	for strategy := range strategies {
		var targets []*Target
		for candidate := range strategy.candidates {
			var fitted bool
			targets, fitted = preemptCandidate(strategy.pCtx, targets, candidate,
				func() bool { return workloadFits(ctx, strategy.pCtx, strategy.allowBorrowing) })
			if fitted {
				targets = fillBackWorkloads(ctx, strategy.pCtx, targets, strategy.allowBorrowing)
				if strategy.verify != nil {
					if valid, reason := strategy.verify(targets); !valid {
						if logV := log.V(6); logV.Enabled() {
							logV.Info("Discarding preemption targets: strategy verification failed",
								"preemptingWorkload", klog.KObj(strategy.pCtx.preemptor.Obj),
								"targets", logging.GetObjectReferences(targets),
								"reason", reason)
						}
						break
					}
				}
				restoreSnapshot(strategy.pCtx.snapshot, targets)
				if logV := log.V(6); logV.Enabled() {
					logV.Info("Preemption succeeded",
						"preemptingWorkload", klog.KObj(strategy.pCtx.preemptor.Obj),
						"targets", logging.GetObjectReferences(targets))
				}
				return targets
			}
		}
		restoreSnapshot(strategy.pCtx.snapshot, targets)
	}
	if logV := log.V(6); logV.Enabled() {
		logV.Info("All preemption strategies failed")
	}
	return nil
}

func addOrReplaceTarget(targets []*Target, target *Target) []*Target {
	key := workload.Key(target.WorkloadInfo.Obj)
	for i, existing := range targets {
		if workload.Key(existing.WorkloadInfo.Obj) != key {
			continue
		}
		target.WorkloadInfo = existing.WorkloadInfo
		targets[i] = target
		return targets
	}
	return append(targets, target)
}

// preemptCandidate records the candidate as a preemption target and reports whether the
// preemptor now fits (per the fits closure). The candidate is yielded by the strategy already
// removed from the snapshot as a whole (see policy.YieldFromSnapshot).
//
// It prefers partial preemption. For an elastic candidate, it finds the largest target count that
// makes the preemptor fit, so it only reclaims the quota actually needed, and puts the reduced
// copy of the candidate back into the snapshot. If this candidate alone cannot make the preemptor
// fit, it is reduced to its minimum and later candidates may add more partial progress. If the
// preemptor still cannot fit after those candidates are considered, the strategy revisits this
// candidate; since it is already at its minimum, it is then handled as a whole-eviction candidate.
// With the PartialPreemption gate off, partialPreemptionInfoFor returns nil and this reduces to the
// original whole-eviction behavior.
//
// Candidates selected by configurable preemption rules are always evicted as a whole.
func preemptCandidate(preemptionCtx *preemptionCtx, targets []*Target, candidate *Target, fits func() bool) ([]*Target, bool) {
	if candidate.Reason != kueue.ConfigurablePreemptionReason {
		if pp, reduced := pendingPartialPreemption(candidate.WorkloadInfo); pp != nil {
			candidate.PartialPreemption = pp
			candidate.ReducedInfo = reduced
			preemptionCtx.snapshot.AddWorkload(reduced)
			return addOrReplaceTarget(targets, candidate), fits()
		}
		if ppInfo := partialPreemptionInfoFor(candidate.WorkloadInfo); ppInfo != nil {
			fitted := preemptCandidatePartially(preemptionCtx, candidate, ppInfo, fits)
			return addOrReplaceTarget(targets, candidate), fitted
		}
	}
	return addOrReplaceTarget(targets, candidate), fits()
}

// pendingPartialPreemption returns the in-flight partial preemption of the candidate, if its
// status already requests a reclaim target that the job has not reached yet, together with the
// reduced copy of the candidate that represents the requested state.
func pendingPartialPreemption(candidate *workload.Info) (*PartialPreemptionTarget, *workload.Info) {
	targetCounts := workload.ReclaimTargetCounts(candidate.Obj)
	if len(targetCounts) == 0 {
		return nil, nil
	}
	specCounts := workload.ExtractPodSetCountsFromWorkload(candidate.Obj)
	for podSet, count := range targetCounts {
		if specCount, ok := specCounts[podSet]; ok && specCount <= count {
			continue
		}
		return &PartialPreemptionTarget{
			PodSet:             podSet,
			ReclaimTargetCount: count,
		}, workload.ReducedInfoForPartialPreemption(candidate, targetCounts)
	}
	return nil, nil
}

// preemptCandidatePartially simulates partially preempting the candidate and records the largest
// target count that makes the preemptor fit. If the preemptor cannot fit even after reducing the
// candidate to its minimum, it leaves the candidate at the minimum and reports fitted=false so
// subsequent candidates can contribute additional partial progress. If those candidates are still
// insufficient, the strategy revisits this candidate and treats it as a whole-eviction candidate
// because it has already reached its minimum.
// The candidate must already be removed from the snapshot.
func preemptCandidatePartially(preemptionCtx *preemptionCtx, candidate *Target, ppInfo *partialPreemptionInfo, fits func() bool) bool {
	full := candidate.WorkloadInfo
	var reducedInfo *workload.Info
	applyReduction := func(targetCount int32) {
		if reducedInfo != nil {
			preemptionCtx.snapshot.RemoveWorkload(reducedInfo)
		}
		reducedInfo = workload.ReducedInfoForPartialPreemption(full,
			map[kueue.PodSetReference]int32{ppInfo.podSet: targetCount})
		preemptionCtx.snapshot.AddWorkload(reducedInfo)
	}

	reclaimableCount := ppInfo.usedCount - ppInfo.minTargetCount
	targetCount := ppInfo.minTargetCount
	fitted := false
	if reclaimableCount > 0 {
		firstFittingReduction := sort.Search(int(reclaimableCount), func(i int) bool {
			targetCount := ppInfo.usedCount - int32(i) - 1
			applyReduction(targetCount)
			return fits()
		})
		if firstFittingReduction < int(reclaimableCount) {
			targetCount = ppInfo.usedCount - int32(firstFittingReduction) - 1
			fitted = true
		}
	}
	// sort.Search may finish without evaluating the returned boundary, and the snapshot must
	// represent the target that will be issued.
	applyReduction(targetCount)

	candidate.PartialPreemption = &PartialPreemptionTarget{
		PodSet:             ppInfo.podSet,
		ReclaimTargetCount: targetCount,
	}
	candidate.ReducedInfo = reducedInfo
	return fitted
}

// applyPreemption simulates preempting the target in the snapshot: for a partial target it replaces
// the full workload with its reduced (scaled-down) copy, freeing only the reclaimed replicas; for a
// whole target it removes the workload entirely.
func applyPreemption(snapshot *schdcache.Snapshot, t *Target) {
	snapshot.RemoveWorkload(t.WorkloadInfo)
	if t.ReducedInfo != nil {
		snapshot.AddWorkload(t.ReducedInfo)
	}
}

// undoPreemption reverses applyPreemption, restoring the full workload in the snapshot.
func undoPreemption(snapshot *schdcache.Snapshot, t *Target) {
	if t.ReducedInfo != nil {
		snapshot.RemoveWorkload(t.ReducedInfo)
	}
	snapshot.AddWorkload(t.WorkloadInfo)
}

type partialPreemptionInfo struct {
	podSet         kueue.PodSetReference
	usedCount      int32
	minTargetCount int32
}

// partialPreemptionInfoFor returns the current effective used count and the lowest target that a
// partial-preemption decision can request. At most one reclaimable PodSet is supported because of
// the workload webhook's minCount validation.
func partialPreemptionInfoFor(candidate *workload.Info) *partialPreemptionInfo {
	if !workload.IsPartialPreemptionJob(candidate.Obj) {
		return nil
	}

	mins := make(map[kueue.PodSetReference]int32, len(candidate.Obj.Spec.PodSets))
	for i := range candidate.Obj.Spec.PodSets {
		ps := &candidate.Obj.Spec.PodSets[i]
		if ps.MinCount != nil {
			mins[ps.Name] = *ps.MinCount
		}
	}
	// Webhook limitations: At most one podset can have minCount.
	if len(mins) != 1 {
		return nil
	}

	maxReclaimedCount, hasMaxReclaimedCount := workload.PartialPreemptionMaxReclaimedCount(candidate.Obj)
	for _, psr := range candidate.TotalRequests {
		minCount, ok := mins[psr.Name]
		if !ok {
			continue
		}
		used := psr.Count
		if target, ok := workload.ReclaimTargetCount(candidate.Obj, psr.Name); ok && target < used {
			used = target
		}
		if used <= minCount {
			return nil
		}
		reclaimable := used - minCount
		if hasMaxReclaimedCount {
			reclaimable = min(reclaimable, maxReclaimedCount)
		}
		return &partialPreemptionInfo{
			podSet:         psr.Name,
			usedCount:      used,
			minTargetCount: used - reclaimable,
		}
	}
	return nil
}

func restoreSnapshot(snapshot *schdcache.Snapshot, targets []*Target) {
	for _, t := range targets {
		undoPreemption(snapshot, t)
	}
}

// fillBackWorkloads checks in the reverse order if any of the workloads can be added back.
func fillBackWorkloads(ctx context.Context, preemptionCtx *preemptionCtx, targets []*Target, allowBorrowing bool) []*Target {
	for i := len(targets) - 2; i >= 0; i-- {
		undoPreemption(preemptionCtx.snapshot, targets[i])
		if workloadFits(ctx, preemptionCtx, allowBorrowing) {
			// O(1) deletion: copy the last element into index i and reduce size.
			targets[i] = targets[len(targets)-1]
			targets = targets[:len(targets)-1]
		} else {
			applyPreemption(preemptionCtx.snapshot, targets[i])
		}
	}
	return targets
}

// parseStrategies converts the configured FairSharing preemption strategies into
// the functions to be used by the algorithm.
// This function takes advantage of the properties of the preemption algorithm and
// the fair sharing strategies.
// The number of functions returned might not match the input slice.
func parseStrategies(fs *config.FairSharing) []fairsharing.Strategy {
	if fs == nil || len(fs.PreemptionStrategies) == 0 {
		return []fairsharing.Strategy{fairsharing.LessThanOrEqualToFinalShare, fairsharing.LessThanInitialShare}
	}
	fsStrategies := make([]fairsharing.Strategy, len(fs.PreemptionStrategies))
	for i, strategy := range fs.PreemptionStrategies {
		switch strategy {
		case config.LessThanOrEqualToFinalShare:
			fsStrategies[i] = fairsharing.LessThanOrEqualToFinalShare
		case config.LessThanInitialShare:
			fsStrategies[i] = fairsharing.LessThanInitialShare
		}
	}
	return fsStrategies
}

// fsStrategyUnsatisfiable reports whether, given the preemptor's and the
// target's shares before any workload is removed, no candidate workload in
// the target ClusterQueue can pass either FairSharing strategy. When it
// returns true the per-candidate simulation in iterateWithFirstFsStrategy can
// be skipped, because every candidate is guaranteed to fail.
//
// A DominantResourceShare of +Inf means the node borrows while having a fair
// weight of 0, which the API defines as an infinite share. It cannot arise any
// other way, since a non-zero weight is validated to be greater than 10^-9.
// CompareDRS ranks such a node above every node that is not itself borrowing
// on a zero weight, so it returns 1. Both fair sharing strategies require the
// comparison to be <= 0 or < 0, so both fail:
//
//   - LessThanInitialShare compares against targetOldShare directly, which
//     this function already has.
//   - LessThanOrEqualToFinalShare compares against the target's share after
//     the candidate's usage is removed. Removing usage never changes the
//     target node's fair weight, and the lendable capacity the usage is
//     compared against is derived from quota alone, so the target's share
//     after removal is at most its share before removal. A target whose share
//     is not +Inf before removal is therefore not +Inf after removal either,
//     and CompareDRS returns 1 for it as well.
func fsStrategyUnsatisfiable(preemptorNewShare fairsharing.PreemptorNewShare, targetOldShare fairsharing.TargetOldShare) bool {
	return schdcache.DRS(preemptorNewShare).ZeroWeightBorrows() &&
		!schdcache.DRS(targetOldShare).ZeroWeightBorrows()
}

func flavorResourcesNeedPreemption(assignment flavorassigner.Assignment) sets.Set[resources.FlavorResource] {
	resPerFlavor := sets.New[resources.FlavorResource]()
	for _, ps := range assignment.PodSets {
		for res, flvAssignment := range ps.Flavors {
			if flvAssignment.Mode == flavorassigner.Preempt {
				resPerFlavor.Insert(resources.FlavorResource{Flavor: flvAssignment.Name, Resource: res})
			}
		}
	}
	return resPerFlavor
}

func findCandidatesForPolicy(
	log logr.Logger,
	wl *kueue.Workload,
	workloadsToFilter map[workload.Reference]*workload.Info,
	preemptionPolicy kueue.PreemptionPolicy,
	frsNeedPreemption sets.Set[resources.FlavorResource],
	workloadOrdering workload.Ordering,
) []*workload.Info {
	var candidates []*workload.Info
	for _, candidateWl := range workloadsToFilter {
		if !policy.SatisfiesPreemptionPolicy(
			log,
			wl,
			candidateWl.Obj,
			workloadOrdering,
			preemptionPolicy) {
			continue
		}

		if !classical.WorkloadUsesResources(candidateWl, frsNeedPreemption) {
			continue
		}
		candidates = append(candidates, candidateWl)
	}
	return candidates
}

// findCandidates obtains candidates for preemption within the ClusterQueue and
// cohort that respect the preemption policy and are using a resource that the
// preempting workload needs.
func (p *Preemptor) findCandidates(log logr.Logger, wl *kueue.Workload, cq *schdcache.ClusterQueueSnapshot, frsNeedPreemption sets.Set[resources.FlavorResource]) []*workload.Info {
	var candidates []*workload.Info

	if cq.Preemption.WithinClusterQueue != kueue.PreemptionPolicyNever {
		newCandidates := findCandidatesForPolicy(log, wl, cq.Workloads, cq.Preemption.WithinClusterQueue, frsNeedPreemption, p.workloadOrdering)
		candidates = append(candidates, newCandidates...)
	}

	if cq.HasParent() && cq.Preemption.ReclaimWithinCohort != kueue.PreemptionPolicyNever {
		for _, cohortCQ := range cq.Parent().Root().SubtreeClusterQueues() {
			if cq == cohortCQ || !cqIsBorrowing(cohortCQ, frsNeedPreemption) {
				// Can't reclaim quota from itself or ClusterQueues that are not borrowing.
				continue
			}
			newCandidates := findCandidatesForPolicy(log, wl, cohortCQ.Workloads, cq.Preemption.ReclaimWithinCohort, frsNeedPreemption, p.workloadOrdering)
			candidates = append(candidates, newCandidates...)
		}
	}
	return candidates
}

func cqIsBorrowing(cq *schdcache.ClusterQueueSnapshot, frsNeedPreemption sets.Set[resources.FlavorResource]) bool {
	if !cq.HasParent() {
		return false
	}
	for fr := range frsNeedPreemption {
		if cq.Borrowing(fr) {
			return true
		}
	}
	return false
}

// workloadFits determines if the workload can be admitted given the simulated usage
// of the snapshot: the quota must be available in the ClusterQueue and its cohort, if
// it belongs to one, and a topology assignment must be found if the workload requires
// one.
func workloadFits(ctx context.Context, preemptionCtx *preemptionCtx, allowBorrowing bool) bool {
	return workloadQuotaFits(preemptionCtx, allowBorrowing) && workloadTopologyFits(ctx, preemptionCtx)
}

// workloadQuotaFits determines if the quota requested by the workload is available in
// the ClusterQueue and its cohort, if it belongs to one.
func workloadQuotaFits(preemptionCtx *preemptionCtx, allowBorrowing bool) bool {
	for fr, v := range preemptionCtx.workloadUsage.Quota.Assigned {
		if !allowBorrowing && preemptionCtx.preemptorCQ.BorrowingWith(fr, v) {
			return false
		}
		if v.Cmp(preemptionCtx.preemptorCQ.Available(fr)) > 0 {
			return false
		}
	}
	return true
}

// workloadTopologyFits determines if a topology assignment can be found for the
// workload, given the simulated usage of the snapshot. It always succeeds if the
// workload has no topology requests.
func workloadTopologyFits(ctx context.Context, preemptionCtx *preemptionCtx) bool {
	tasResult := preemptionCtx.preemptorCQ.FindTopologyAssignmentsForWorkload(
		ctx,
		preemptionCtx.tasRequests,
		schdcache.WithWorkloadInfo(&preemptionCtx.preemptor),
	)
	return tasResult.Failure() == nil
}

// queueUnderNominalInResourcesNeedingPreemption checks whether the
// preemptor CQ's usage is strictly below nominal quota (usage < nominal)
// for all flavor-resources needing preemption.
func queueUnderNominalInResourcesNeedingPreemption(preemptionCtx *preemptionCtx) bool {
	for fr := range preemptionCtx.frsNeedPreemption {
		if preemptionCtx.preemptorCQ.QuotaFor(fr).Nominal.Cmp(preemptionCtx.preemptorCQ.ResourceNode.Usage[fr]) <= 0 {
			return false
		}
	}
	return true
}

// queueWithinNominalInRequestedResources checks whether the preemptor CQ's
// usage is at or below nominal quota (usage <= nominal) for all positive
// flavor-resource requests assigned to the incoming workload.
// The difference from queueUnderNominalInResourcesNeedingPreemption is that
// this treats usage exactly equal to nominal as "within nominal."
func queueWithinNominalInRequestedResources(preemptionCtx *preemptionCtx) bool {
	for fr, request := range preemptionCtx.workloadUsage.Quota.Assigned {
		if request.CmpInt64(0) <= 0 {
			continue
		}
		if preemptionCtx.preemptorCQ.Borrowing(fr) {
			return false
		}
	}
	return true
}

// buildCQPath constructs a path like "/parent/.../cq" for a given ClusterQueue snapshot.
func buildCQPath(cqName string, cqSnap *schdcache.ClusterQueueSnapshot) string {
	parts := []string{cqName}
	for ancestor := range cqSnap.PathParentToRoot() {
		parts = append(parts, string(ancestor.GetName()))
	}
	// Reverse the slice since we want parent first
	slices.Reverse(parts)
	return "/" + strings.Join(parts, "/")
}
