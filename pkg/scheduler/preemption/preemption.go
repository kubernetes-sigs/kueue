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
	"slices"
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
	preemptioncommon "sigs.k8s.io/kueue/pkg/scheduler/preemption/common"
	"sigs.k8s.io/kueue/pkg/scheduler/preemption/fairsharing"
	"sigs.k8s.io/kueue/pkg/util/expectations"
	"sigs.k8s.io/kueue/pkg/util/logging"
	"sigs.k8s.io/kueue/pkg/util/priority"
	"sigs.k8s.io/kueue/pkg/util/roletracker"
	"sigs.k8s.io/kueue/pkg/util/routine"
	"sigs.k8s.io/kueue/pkg/workload"
	workloadevict "sigs.k8s.io/kueue/pkg/workload/evict"
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

type preemptionCtx struct {
	clock             clock.Clock
	preemptor         workload.Info
	preemptorCQ       *schdcache.ClusterQueueSnapshot
	snapshot          *schdcache.Snapshot
	workloadUsage     workload.Usage
	tasRequests       schdcache.WorkloadTASRequests
	frsNeedPreemption sets.Set[resources.FlavorResource]
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

type Target struct {
	WorkloadInfo *workload.Info
	Reason       string
	WorkloadCq   *schdcache.ClusterQueueSnapshot
}

// ensures that Target implements ObjectRefProvider interface at compile time
var _ logging.ObjectRefProvider = (*Target)(nil)

// GetObject implements the ObjectRefProvider interface.
func (t *Target) GetObject() client.Object {
	return t.WorkloadInfo.Obj
}

func (p *Preemptor) GetPreemptionPlanFactory(
	wl workload.Info,
	snapshot *schdcache.Snapshot,
) PreemptionPlanFactory {
	return func(ctx context.Context, assignment *flavorassigner.Assignment) PreemptionPlan {
		return p.getPreemptionPlan(ctx, p.buildContext(ctx, wl, *assignment, snapshot))
	}
}

func (p *Preemptor) getPreemptionPlan(ctx context.Context, preemptionCtx *preemptionCtx) PreemptionPlan {
	if p.enableFairSharing {
		return FairPreemptionPlan(ctx, p, preemptionCtx, p.fsStrategies)
	}
	return ClassicalPreemptionPlan(ctx, p, preemptionCtx)
}

func (p *Preemptor) GetTargetsUsingPlan(ctx context.Context, plan PreemptionPlan) []*Target {
	return p.getTargets(ctx, plan)
}

// GetTargets returns the list of workloads that should be evicted in
// order to make room for wl.
func (p *Preemptor) GetTargets(
	ctx context.Context,
	wl workload.Info,
	assignment flavorassigner.Assignment,
	snapshot *schdcache.Snapshot,
) []*Target {
	pCtx := p.buildContext(ctx, wl, assignment, snapshot)
	return p.getTargets(ctx, p.getPreemptionPlan(ctx, pCtx))
}

func (p *Preemptor) getTargets(ctx context.Context, plan PreemptionPlan) []*Target {
	if plan.Type == FairPreemptions {
		return p.fairPreemptions(ctx, plan)
	}
	return p.classicalPreemptions(ctx, plan)
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
	}
}

var HumanReadablePreemptionReasons = map[string]string{
	kueue.InClusterQueueReason:                "prioritization in the ClusterQueue",
	kueue.InCohortReclamationReason:           "reclamation within the cohort",
	kueue.InCohortFairSharingReason:           "Fair Sharing within the cohort",
	kueue.InCohortReclaimWhileBorrowingReason: "reclamation within the cohort while borrowing",
	"": "UNKNOWN",
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

		message := preemptionMessage(preemptor.Obj, target.Reason, preemptorPath, preempteePath)
		wlCopy := target.WorkloadInfo.Obj.DeepCopy()
		exposeLqMetrics := cache.ShouldExposeLocalQueueMetricsForWorkload(log, wlCopy)
		err := workloadevict.Evict(
			ctx, p.client, p.recorder, wlCopy, kueue.WorkloadEvictedByPreemption, message, "", p.clock, exposeLqMetrics, p.roleTracker, p.customLabels,
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

type preemptionAttemptOpts struct {
	borrowing bool
}

// classicalPreemptions implements a heuristic to find a minimal set of Workloads
// to preempt.
// The heuristic first removes candidates, in the input order, while their
// ClusterQueues are still borrowing resources and while the incoming Workload
// doesn't fit in the quota.
// Once the Workload fits, the heuristic tries to add Workloads back, in the
// reverse order in which they were removed, while the incoming Workload still
// fits
func (p *Preemptor) classicalPreemptions(ctx context.Context, plan PreemptionPlan) []*Target {
	preemptionCtx := plan.pCtx
	for strategy, opts := range plan.Strategies {
		var targets []*Target
		for target := range strategy {
			preemptionCtx.snapshot.RemoveWorkload(target.WorkloadInfo)
			targets = append(targets, target)
			if workloadFits(ctx, preemptionCtx, opts.Borrowing) {
				targets = fillBackWorkloads(ctx, preemptionCtx, targets, opts.Borrowing)
				restoreSnapshot(preemptionCtx.snapshot, targets)
				return targets
			}
		}
		restoreSnapshot(preemptionCtx.snapshot, targets)
	}
	return nil
}

func fillBackWorkloads(ctx context.Context, preemptionCtx *preemptionCtx, targets []*Target, allowBorrowing bool) []*Target {
	// In the reverse order, check if any of the workloads can be added back.
	for i := len(targets) - 2; i >= 0; i-- {
		preemptionCtx.snapshot.AddWorkload(targets[i].WorkloadInfo)
		if workloadFits(ctx, preemptionCtx, allowBorrowing) {
			// O(1) deletion: copy the last element into index i and reduce size.
			targets[i] = targets[len(targets)-1]
			targets = targets[:len(targets)-1]
		} else {
			preemptionCtx.snapshot.RemoveWorkload(targets[i].WorkloadInfo)
		}
	}
	return targets
}

func restoreSnapshot(snapshot *schdcache.Snapshot, targets []*Target) {
	for _, t := range targets {
		snapshot.AddWorkload(t.WorkloadInfo)
	}
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

func (p *Preemptor) fairPreemptions(ctx context.Context, plan PreemptionPlan) []*Target {
	preemptionCtx := plan.pCtx
	log := log.FromContext(ctx)
	// DRS values must include incoming workload.
	revertSimulation := preemptionCtx.preemptorCQ.SimulateUsageAddition(preemptionCtx.workloadUsage)
	targets, fits := tryStrategies(ctx, preemptionCtx, plan)
	revertSimulation()

	if !fits {
		if logV := log.V(6); logV.Enabled() {
			logV.Info("All fair sharing strategies failed",
				"preemptingWorkload", klog.KObj(preemptionCtx.preemptor.Obj),
				"targets", logging.GetObjectReferences(targets))
		}
		restoreSnapshot(preemptionCtx.snapshot, targets)
		return nil
	}
	targets = fillBackWorkloads(ctx, preemptionCtx, targets, true)
	restoreSnapshot(preemptionCtx.snapshot, targets)

	if logV := log.V(6); logV.Enabled() {
		logV.Info("Fair sharing strategies succeeded",
			"preemptingWorkload", klog.KObj(preemptionCtx.preemptor.Obj),
			"targets", logging.GetObjectReferences(targets))
	}
	return targets
}

func tryStrategies(ctx context.Context, preemptionCtx *preemptionCtx, plan PreemptionPlan) (targets []*Target, fits bool) {
	for strategy := range plan.Strategies {
		for target := range strategy {
			targets = append(targets, target)
			if workloadFitsForFairSharing(ctx, preemptionCtx) {
				fits = true
				return
			}
		}
	}
	return
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
	policy kueue.PreemptionPolicy,
	frsNeedPreemption sets.Set[resources.FlavorResource],
	workloadOrdering workload.Ordering,
) []*workload.Info {
	var candidates []*workload.Info
	for _, candidateWl := range workloadsToFilter {
		if !preemptioncommon.SatisfiesPreemptionPolicy(
			log,
			wl,
			candidateWl.Obj,
			workloadOrdering,
			policy) {
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

// workloadFits determines if the workload requests would fit given the
// requestable resources and simulated usage of the ClusterQueue and its cohort,
// if it belongs to one.
func workloadFits(ctx context.Context, preemptionCtx *preemptionCtx, allowBorrowing bool) bool {
	for fr, v := range preemptionCtx.workloadUsage.Quota.Assigned {
		if !allowBorrowing && preemptionCtx.preemptorCQ.BorrowingWith(fr, v) {
			return false
		}
		if v.Cmp(preemptionCtx.preemptorCQ.Available(fr)) > 0 {
			return false
		}
	}
	tasResult := preemptionCtx.preemptorCQ.FindTopologyAssignmentsForWorkload(
		ctx,
		preemptionCtx.tasRequests,
		schdcache.WithWorkload(preemptionCtx.preemptor.Obj),
	)
	return tasResult.Failure() == nil
}

// workloadFitsForFairSharing is a lightweight wrapper around
// workloadFits, as we need to remove, and then add back, the usage of
// the incoming workload, as FairSharing adds this usage at the start
// of processing for accurate DominantResourceShare calculations.
func workloadFitsForFairSharing(ctx context.Context, preemptionCtx *preemptionCtx) bool {
	revertSimulation := preemptionCtx.preemptorCQ.SimulateUsageRemoval(preemptionCtx.workloadUsage)
	res := workloadFits(ctx, preemptionCtx, true)
	revertSimulation()
	return res
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
