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
	"strconv"
	"strings"
	"sync/atomic"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/tools/events"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"
	"k8s.io/utils/clock"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/log"

	config "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	kueueconstants "sigs.k8s.io/kueue/pkg/constants"
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
	"sigs.k8s.io/kueue/pkg/workload/patching"
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

	partialPreemptionTimeout time.Duration
	enableOnDemandPreemption bool
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
	clock                    clock.Clock
	log                      logr.Logger
	preemptor                workload.Info
	preemptorCQ              *schdcache.ClusterQueueSnapshot
	snapshot                 *schdcache.Snapshot
	workloadUsage            workload.Usage
	tasRequests              schdcache.WorkloadTASRequests
	frsNeedPreemption        sets.Set[resources.FlavorResource]
	partialPreemptionTimeout time.Duration
	configurableEvaluator    *configurable.PreemptionEvaluator
	enableOnDemandPreemption bool
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
	partialPreemptionTimeout time.Duration,
	enableOnDemandPreemption bool,
) *Preemptor {
	return &Preemptor{
		clock:                    clock,
		client:                   cl,
		recorder:                 recorder,
		workloadOrdering:         workloadOrdering,
		enableFairSharing:        fairsharing.Enabled(fs),
		fsStrategies:             parseStrategies(fs),
		enabledAfs:               enabledAfs,
		partialPreemptionTimeout: partialPreemptionTimeout,
		enableOnDemandPreemption: enableOnDemandPreemption,
		roleTracker:              tracker,
		customLabels:             customLabels,
		preemptionExpectations:   preemptionExpectations,
	}
}

type Target = policy.Target

// ensures that Target implements ObjectRefProvider interface at compile time
var _ logging.ObjectRefProvider = (*Target)(nil)

func WorkloadReplacements(targets []*Target) []schdcache.WorkloadReplacement {
	replacements := make([]schdcache.WorkloadReplacement, 0, len(targets))
	for _, target := range targets {
		replacements = append(replacements, schdcache.WorkloadReplacement{Full: target.WorkloadInfo, Reduced: target.ReducedInfo})
	}
	return replacements
}

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
		clock:                    p.clock,
		log:                      log,
		preemptor:                wl,
		preemptorCQ:              cq,
		snapshot:                 snapshot,
		tasRequests:              tasRequests,
		frsNeedPreemption:        flavorResourcesNeedPreemption(assignment),
		partialPreemptionTimeout: p.partialPreemptionTimeout,
		workloadUsage: workload.Usage{
			Quota: workload.ResourceUsage{
				Assigned: assignment.TotalRequestsFor(log, &wl),
			},
			TAS: wl.TASUsage(),
		},
		configurableEvaluator:    p.newConfigurableEvaluator(ctx, log, cq, &wl),
		enableOnDemandPreemption: p.enableOnDemandPreemption,
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
		targetKey := types.NamespacedName{Name: target.WorkloadInfo.Obj.Name, Namespace: target.WorkloadInfo.Obj.Namespace}
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
			if err := patching.PatchAdmissionStatus(ctx, p.client, wlCopy, p.clock, func(wl *kueue.Workload) (bool, error) {
				updated := setReclaimTargetCount(wl, pp.PodSet, pp.ReclaimTargetCount)
				workload.SetPreemptedCondition(
					wl,
					p.clock.Now(),
					target.Reason,
					fmt.Sprintf(
						"Requested scale-down of PodSet %q to %d to accommodate workload %s",
						pp.PodSet,
						pp.ReclaimTargetCount,
						klog.KObj(preemptor.Obj),
					),
				)
				return updated, nil
			}, patching.WithLooseOnApply()); err != nil {
				errCh.SendErrorWithCancel(err, cancel)
				preemptionErrors.Add(1)
				return
			}
			log.V(3).Info("Partial preemption: requested scale-down",
				"targetWorkload", klog.KObj(target.WorkloadInfo.Obj), "podSet", pp.PodSet,
				"ReclaimTargetCount", pp.ReclaimTargetCount, "preemptingWorkload", klog.KObj(preemptor.Obj))
			p.recorder.Eventf(target.WorkloadInfo.Obj, nil, corev1.EventTypeNormal, "PartiallyPreempted",
				"PartiallyPreempted",
				"Requested scale-down of PodSet %q to %d to accommodate workload %s",
				string(pp.PodSet), pp.ReclaimTargetCount, klog.KObj(preemptor.Obj))
			successfullyPreempted.Add(1)
			return
		}
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
			// addOrReplaceTarget commits the planned target. A partial-preemption victim
			// can be revisited across iterator rounds: the first round reduces it, a later
			// round (once it is at its minimum) selects it for whole eviction. The slot
			// recorded on the reduced copy lets the later target replace the earlier one in
			// place instead of appending a duplicate for the same workload.
			targets = addOrReplaceTarget(targets, candidate)
			if workloadFits(ctx, strategy.pCtx, strategy.allowBorrowing) {
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

// addOrReplaceTarget commits target into the targets slice. If target's workload already
// occupies a slot (recorded on a reduced copy from an earlier iterator round), the existing
// entry is replaced in place and the original full workload.Info is preserved as the target's
// WorkloadInfo, so restoreSnapshot and the eventual eviction act on the full workload. Otherwise
// the target is appended and its slot is recorded on the reduced copy (if any) so a later round
// can find it. Using the slot stored on workload.Info avoids a per-attempt map on this hot path.
func addOrReplaceTarget(targets []*Target, target *Target) []*Target {
	if idx, ok := target.WorkloadInfo.GetPreemptionTargetSlot(); ok {
		target.WorkloadInfo = targets[idx].WorkloadInfo
		targets[idx] = target
		if target.ReducedInfo != nil {
			target.ReducedInfo.SetPreemptionTargetSlot(idx)
		}
		return targets
	}
	targets = append(targets, target)
	if target.ReducedInfo != nil {
		target.ReducedInfo.SetPreemptionTargetSlot(len(targets) - 1)
	}
	return targets
}

type preemptionPlan struct {
	target  *Target
	partial bool
	pending bool
}

// freedQuotaUsage returns the quota that committing this plan frees from the target ClusterQueue:
// only the reclaimed slice for a partial preemption, or the whole workload for a whole eviction.
// It is computed on demand so callers that don't gate on it (e.g. the classical path) pay nothing.
func (p preemptionPlan) freedQuotaUsage() workload.Usage {
	if p.target.ReducedInfo != nil {
		return reclaimedQuotaUsage(p.target.WorkloadInfo.Usage().Quota.Assigned, p.target.ReducedInfo.Usage().Quota.Assigned)
	}
	return p.target.WorkloadInfo.Usage()
}

// planPreemption decides how preempting candidate would proceed without committing it. It leaves the
// snapshot unchanged.
//
// It prefers partial preemption. For an elastic candidate, it finds the largest target count that
// makes the preemptor fit, so it only reclaims the quota actually needed. If this candidate alone
// cannot make the preemptor fit, it is reduced to its minimum and later candidates may add more
// partial progress. If the preemptor still cannot fit after those candidates are considered, a
// subsequent iterator round revisits this candidate; since it is already at its minimum, it is then
// handled as a whole-eviction candidate.
// With the PartialPreemption gate off, partialPreemptionInfoFor returns nil and this reduces to the
// original whole-eviction behavior.
func planPreemption(preemptionCtx *preemptionCtx, candidate *workload.Info, reason string, allowBorrowing, usageSimulated bool) preemptionPlan {
	if target, timedOut := pendingPartialPreemptionTarget(preemptionCtx, candidate, reason); timedOut {
		// Timed out: the real path falls back to whole eviction.
		return preemptionPlan{target: &Target{WorkloadInfo: candidate, Reason: reason}}
	} else if target != nil {
		// An in-flight partial reclaim; only the reclaimed slice will be freed.
		return preemptionPlan{target: target, partial: true, pending: true}
	}
	if ppInfo := partialPreemptionInfoFor(candidate); ppInfo != nil {
		var targetCount int32
		var reducedInfo *workload.Info
		if preemptionCtx.enableOnDemandPreemption {
			// searchPartialReduction leaves the snapshot unchanged; YieldFromSnapshot applies
			// the chosen reduction once when the target is yielded.
			targetCount, reducedInfo, _ = searchPartialReduction(preemptionCtx, candidate, ppInfo, allowBorrowing, usageSimulated)
		} else {
			// On-demand preemption is disabled: drop straight to the per-round floor instead of
			// searching for the smallest reduction that admits the preemptor. minTargetCount
			// already folds in the per-workload maxReclaimedCount cap.
			targetCount = ppInfo.minTargetCount
			reducedInfo = reducedInfoForPartialPreemption(candidate, ppInfo.podSet, targetCount)
		}
		return preemptionPlan{
			target: &Target{
				WorkloadInfo: candidate,
				Reason:       reason,
				PartialPreemption: &policy.PartialPreemptionTarget{
					PodSet:             ppInfo.podSet,
					ReclaimTargetCount: targetCount,
				},
				ReducedInfo: reducedInfo,
			},
			partial: true,
		}
	}
	// Whole eviction.
	return preemptionPlan{target: &Target{WorkloadInfo: candidate, Reason: reason}}
}

func pendingPartialPreemptionTarget(preemptionCtx *preemptionCtx, candidate *workload.Info, reason string) (*Target, bool) {
	if !candidate.IsPartialPreemptionJob || !candidate.HasPendingReclaimTarget || candidate.Obj.Status.Admission == nil {
		return nil, false
	}
	assignments := candidate.Obj.Status.Admission.PodSetAssignments
	for i := range assignments {
		psa := &assignments[i]
		if psa.ReclaimTargetCount == nil {
			continue
		}
		count := *psa.ReclaimTargetCount
		if specCount, ok := workload.PodSetSpecCount(candidate.Obj, psa.Name); ok && specCount <= count {
			continue
		}
		if partialPreemptionTimedOut(preemptionCtx, candidate, psa.Name, count) {
			return nil, true
		}
		// Only one PodSet carries a reclaim target per victim today (multi-minCount is not
		// supported yet), so reducing that single PodSet is enough.
		return &Target{
			WorkloadInfo: candidate,
			Reason:       reason,
			PartialPreemption: &policy.PartialPreemptionTarget{
				PodSet:             psa.Name,
				ReclaimTargetCount: count,
			},
			ReducedInfo: reducedInfoForPartialPreemption(candidate, psa.Name, count),
		}, false
	}
	return nil, false
}

func partialPreemptionTimedOut(preemptionCtx *preemptionCtx, candidate *workload.Info, podSet kueue.PodSetReference, reclaimTargetCount int32) bool {
	if preemptionCtx.partialPreemptionTimeout <= 0 {
		return false
	}
	preemptedCond := meta.FindStatusCondition(candidate.Obj.Status.Conditions, kueue.WorkloadPreempted)
	if preemptedCond == nil || preemptedCond.Status != metav1.ConditionTrue ||
		preemptionCtx.clock.Since(preemptedCond.LastTransitionTime.Time) < preemptionCtx.partialPreemptionTimeout {
		return false
	}
	preemptionCtx.log.V(3).Info("Partial preemption timed out, falling back to full eviction",
		"preemptor", klog.KObj(preemptionCtx.preemptor.Obj),
		"victim", klog.KObj(candidate.Obj),
		"podSet", podSet,
		"reclaimTargetCount", reclaimTargetCount,
		"timeout", preemptionCtx.partialPreemptionTimeout,
		"partialPreemptionStartTime", preemptedCond.LastTransitionTime)
	return true
}

// searchPartialReduction simulates reducing candidate's reclaimable PodSet to the largest target
// count (smallest reduction) that makes the preemptor fit, per the fits closure. If the preemptor
// cannot fit even after reducing the candidate to its minimum, it settles on the minimum and reports
// fitted=false so subsequent candidates can contribute additional partial progress. It returns the
// chosen target count, the corresponding reduced workload.Info, and whether the preemptor fits at
// that reduction.
func searchPartialReduction(preemptionCtx *preemptionCtx, candidate *workload.Info, ppInfo *partialPreemptionInfo, allowBorrowing, usageSimulated bool) (int32, *workload.Info, bool) {
	candidateCQ := preemptionCtx.snapshot.ClusterQueue(candidate.ClusterQueue)
	candidateQuotaUsage := candidate.Usage().Quota.Assigned

	// fitsAfterReduction reports whether the preemptor fits once candidate's PodSet is reduced to
	// targetCount. Instead of swapping the full workload for a reduced copy in the snapshot (which
	// would touch the ClusterQueue hierarchy and TAS bookkeeping on every binary-search step), it
	// subtracts only the reclaimed quota delta from candidate's ClusterQueue and reverts it after
	// the check. The full workload stays in the snapshot throughout.
	fitsAfterReduction := func(targetCount int32) bool {
		reducedInfo := reducedInfoForPartialPreemption(candidate, ppInfo.podSet, targetCount)
		reclaimedUsage := reclaimedQuotaUsage(candidateQuotaUsage, reducedInfo.Usage().Quota.Assigned)
		revertSimulation := candidateCQ.SimulateUsageRemoval(reclaimedUsage)
		fitted := workloadQuotaFits(preemptionCtx, allowBorrowing, usageSimulated)
		revertSimulation()
		return fitted
	}

	reclaimableCount := ppInfo.usedCount - ppInfo.minTargetCount
	targetCount := ppInfo.minTargetCount
	fitted := false
	if reclaimableCount > 0 {
		firstFittingReduction := sort.Search(int(reclaimableCount), func(i int) bool {
			return fitsAfterReduction(ppInfo.usedCount - int32(i) - 1)
		})
		if firstFittingReduction < int(reclaimableCount) {
			targetCount = ppInfo.usedCount - int32(firstFittingReduction) - 1
			fitted = true
		}
	}
	reducedInfo := reducedInfoForPartialPreemption(candidate, ppInfo.podSet, targetCount)
	return targetCount, reducedInfo, fitted
}

// reclaimedQuotaUsage returns the quota in full that is absent from reduced (full minus reduced).
func reclaimedQuotaUsage(full, reduced resources.FlavorResourceQuantities) workload.Usage {
	delta := make(resources.FlavorResourceQuantities, len(full))
	for fr, q := range full {
		if d := q.Sub(reduced[fr]); d.Sign() != 0 {
			delta[fr] = d
		}
	}
	return workload.Usage{Quota: workload.ResourceUsage{Assigned: delta}}
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

// revertPreemption is the inverse of applyPreemption: it removes the reduced copy of a partial target
// (if any) and adds the full workload back, restoring the snapshot to its pre-preemption state.
func revertPreemption(snapshot *schdcache.Snapshot, t *Target) {
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
// partial-preemption decision can request. The target incorporates maxReclaimedCount, so it is the
// lowest target for the current partial-preemption round rather than necessarily the PodSet minCount.
// At most one reclaimable PodSet is supported because of the workload webhook's minCount validation.
func partialPreemptionInfoFor(candidate *workload.Info) *partialPreemptionInfo {
	if !candidate.IsPartialPreemptionJob {
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

	maxReclaimedCount, hasMaxReclaimedCount := partialPreemptionMaxReclaimedCount(candidate.Obj)
	for _, psr := range candidate.TotalRequests {
		minCount, ok := mins[psr.Name]
		if !ok {
			continue
		}
		// TODO: TAS victims are not supported yet.
		if psr.TopologyRequest != nil {
			return nil
		}
		used := psr.Count
		if target, ok := workload.ReclaimTargetCount(candidate.Obj, psr.Name); ok && target < used {
			used = target
		}
		if used <= minCount {
			return nil
		}
		// maxReclaimedCount caps how many replicas a single round may reclaim. Folding it into
		// minTargetCount bounds the per-round floor for both modes: on-demand searches
		// [minTargetCount, used) for the smallest reduction that fits, and fixed-step drops
		// straight to minTargetCount.
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
		revertPreemption(snapshot, t)
	}
}

// fillBackWorkloads checks in the reverse order if any of the workloads can be added back.
func fillBackWorkloads(ctx context.Context, preemptionCtx *preemptionCtx, targets []*Target, allowBorrowing bool) []*Target {
	for i := len(targets) - 2; i >= 0; i-- {
		revertPreemption(preemptionCtx.snapshot, targets[i])
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
	return workloadQuotaFits(preemptionCtx, allowBorrowing, false) && workloadTopologyFits(ctx, preemptionCtx)
}

// workloadQuotaFits determines if the quota requested by the workload is available in
// the ClusterQueue and its cohort, if it belongs to one. usageSimulated tells whether
// the preemptor's own usage has already been added to the snapshot.
func workloadQuotaFits(preemptionCtx *preemptionCtx, allowBorrowing, usageSimulated bool) bool {
	for fr, v := range preemptionCtx.workloadUsage.Quota.Assigned {
		if usageSimulated {
			if !allowBorrowing && preemptionCtx.preemptorCQ.Borrowing(fr) {
				return false
			}
			if preemptionCtx.preemptorCQ.InDebt(fr) {
				return false
			}
			continue
		}
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

// queueWithinNominalInResourcesNeedingPreemption checks whether the
// preemptor CQ's usage is at or below nominal quota (usage <= nominal)
// for all flavor-resources needing preemption.
// The difference from queueUnderNominalInResourcesNeedingPreemption is
// that this treats usage exactly equal to nominal as "within nominal."
func queueWithinNominalInResourcesNeedingPreemption(preemptionCtx *preemptionCtx) bool {
	for fr := range preemptionCtx.frsNeedPreemption {
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

// reducedInfoForPartialPreemption returns a shallow copy of info with the named PodSet's usage scaled
// down to targetCount. The original is left untouched so the snapshot can restore it unchanged.
func reducedInfoForPartialPreemption(info *workload.Info, podSetName kueue.PodSetReference, targetCount int32) *workload.Info {
	reduced := *info
	reduced.TotalRequests = make([]workload.PodSetResources, len(info.TotalRequests))
	for idx := range info.TotalRequests {
		psr := info.TotalRequests[idx]
		if psr.Name == podSetName && targetCount >= 0 && targetCount < psr.Count {
			psr = *psr.ScaledTo(targetCount)
		}
		reduced.TotalRequests[idx] = psr
	}
	return &reduced
}

// setReclaimTargetCount records count as the named PodSet's requested partial-preemption target,
// reporting whether the value changed.
func setReclaimTargetCount(wl *kueue.Workload, podSetName kueue.PodSetReference, count int32) bool {
	if wl.Status.Admission == nil {
		return false
	}
	for i := range wl.Status.Admission.PodSetAssignments {
		psa := &wl.Status.Admission.PodSetAssignments[i]
		if psa.Name == podSetName {
			if ptr.Deref(psa.ReclaimTargetCount, -1) == count {
				return false
			}
			psa.ReclaimTargetCount = new(count)
			return true
		}
	}
	return false
}

// partialPreemptionMaxReclaimedCount reads the optional per-workload cap on how many replicas a single
// partial preemption may reclaim, returning false when the annotation is absent or malformed.
func partialPreemptionMaxReclaimedCount(wl *kueue.Workload) (int32, bool) {
	value := wl.GetAnnotations()[kueueconstants.PartialPreemptionMaxReclaimedCountAnnotation]
	count, err := strconv.ParseInt(value, 10, 32)
	if err != nil || count <= 0 {
		return 0, false
	}
	return int32(count), true
}
