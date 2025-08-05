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
	"sort"
	"sync/atomic"
	"time"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"
	"k8s.io/utils/clock"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	config "sigs.k8s.io/kueue/apis/config/v1beta1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/cache"
	"sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/resources"
	"sigs.k8s.io/kueue/pkg/scheduler/flavorassigner"
	"sigs.k8s.io/kueue/pkg/scheduler/preemption/classical"
	"sigs.k8s.io/kueue/pkg/scheduler/preemption/fairsharing"
	"sigs.k8s.io/kueue/pkg/util/priority"
	"sigs.k8s.io/kueue/pkg/util/routine"
	"sigs.k8s.io/kueue/pkg/workload"
)

const parallelPreemptions = 8

type Preemptor struct {
	clock clock.Clock

	client   client.Client
	recorder record.EventRecorder

	workloadOrdering  workload.Ordering
	enableFairSharing bool
	fsStrategies      []fairsharing.Strategy

	// stubs
	applyPreemption func(ctx context.Context, w *kueue.Workload, reason, message string) error
}

type preemptionCtx struct {
	log               logr.Logger
	preemptor         workload.Info
	preemptorCQ       *cache.ClusterQueueSnapshot
	snapshot          *cache.Snapshot
	workloadUsage     workload.Usage
	tasRequests       cache.WorkloadTASRequests
	frsNeedPreemption sets.Set[resources.FlavorResource]
}

func New(
	cl client.Client,
	workloadOrdering workload.Ordering,
	recorder record.EventRecorder,
	fs config.FairSharing,
	clock clock.Clock,
) *Preemptor {
	p := &Preemptor{
		clock:             clock,
		client:            cl,
		recorder:          recorder,
		workloadOrdering:  workloadOrdering,
		enableFairSharing: fs.Enable,
		fsStrategies:      parseStrategies(fs.PreemptionStrategies),
	}
	p.applyPreemption = p.applyPreemptionWithSSA
	return p
}

func (p *Preemptor) OverrideApply(f func(context.Context, *kueue.Workload, string, string) error) {
	p.applyPreemption = f
}

type Target struct {
	WorkloadInfo *workload.Info
	Reason       string
}

// GetTargets returns the list of workloads that should be evicted in
// order to make room for wl.
func (p *Preemptor) GetTargets(log logr.Logger, wl workload.Info, assignment flavorassigner.Assignment, snapshot *cache.Snapshot) []*Target {
	cq := snapshot.ClusterQueue(wl.ClusterQueue)
	tasRequests := assignment.WorkloadsTopologyRequests(&wl, cq)
	return p.getTargets(&preemptionCtx{
		log:               log,
		preemptor:         wl,
		preemptorCQ:       cq,
		snapshot:          snapshot,
		tasRequests:       tasRequests,
		frsNeedPreemption: flavorResourcesNeedPreemption(assignment),
		workloadUsage: workload.Usage{
			Quota: assignment.TotalRequestsFor(&wl),
			TAS:   wl.TASUsage(),
		},
	})
}

func (p *Preemptor) getTargets(preemptionCtx *preemptionCtx) []*Target {
	if p.enableFairSharing {
		return p.fairPreemptions(preemptionCtx, p.fsStrategies)
	}
	return p.classicalPreemptions(preemptionCtx)
}

var HumanReadablePreemptionReasons = map[string]string{
	kueue.InClusterQueueReason:                "prioritization in the ClusterQueue",
	kueue.InCohortReclamationReason:           "reclamation within the cohort",
	kueue.InCohortFairSharingReason:           "Fair Sharing within the cohort",
	kueue.InCohortReclaimWhileBorrowingReason: "reclamation within the cohort while borrowing",
	"": "UNKNOWN",
}

func preemptionMessage(preemptor *kueue.Workload, reason string) string {
	var wUID, jUID string
	if preemptor.UID == "" {
		wUID = "UNKNOWN"
	} else {
		wUID = string(preemptor.UID)
	}
	uid := preemptor.Labels[constants.JobUIDLabel]
	if uid == "" {
		jUID = "UNKNOWN"
	} else {
		jUID = uid
	}

	return fmt.Sprintf("Preempted to accommodate a workload (UID: %s, JobUID: %s) due to %s", wUID, jUID, HumanReadablePreemptionReasons[reason])
}

// IssuePreemptions marks the target workloads as evicted.
func (p *Preemptor) IssuePreemptions(ctx context.Context, preemptor *workload.Info, targets []*Target) (int, error) {
	log := ctrl.LoggerFrom(ctx)
	errCh := routine.NewErrorChannel()
	ctx, cancel := context.WithCancel(ctx)
	var successfullyPreempted atomic.Int64
	defer cancel()
	workqueue.ParallelizeUntil(ctx, parallelPreemptions, len(targets), func(i int) {
		target := targets[i]
		if !meta.IsStatusConditionTrue(target.WorkloadInfo.Obj.Status.Conditions, kueue.WorkloadEvicted) {
			message := preemptionMessage(preemptor.Obj, target.Reason)
			err := p.applyPreemption(ctx, target.WorkloadInfo.Obj, target.Reason, message)
			if err != nil {
				errCh.SendErrorWithCancel(err, cancel)
				return
			}

			log.V(3).Info("Preempted", "targetWorkload", klog.KObj(target.WorkloadInfo.Obj), "preemptingWorkload", klog.KObj(preemptor.Obj), "reason", target.Reason, "message", message, "targetClusterQueue", klog.KRef("", string(target.WorkloadInfo.ClusterQueue)))
			p.recorder.Eventf(target.WorkloadInfo.Obj, corev1.EventTypeNormal, "Preempted", message)
			workload.ReportPreemption(preemptor.ClusterQueue, target.Reason, target.WorkloadInfo.ClusterQueue)
		} else {
			log.V(3).Info("Preemption ongoing", "targetWorkload", klog.KObj(target.WorkloadInfo.Obj), "preemptingWorkload", klog.KObj(preemptor.Obj))
		}
		successfullyPreempted.Add(1)
	})
	return int(successfullyPreempted.Load()), errCh.ReceiveError()
}

func (p *Preemptor) applyPreemptionWithSSA(ctx context.Context, w *kueue.Workload, reason, message string) error {
	w = w.DeepCopy()
	workload.SetPreemptedCondition(w, reason, message)
	return workload.Evict(ctx, p.client, p.recorder, w, kueue.WorkloadEvictedByPreemption, "", message, p.clock)
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
func (p *Preemptor) classicalPreemptions(preemptionCtx *preemptionCtx) []*Target {
	hierarchicalReclaimCtx := &classical.HierarchicalPreemptionCtx{
		Log:               preemptionCtx.log,
		Wl:                preemptionCtx.preemptor.Obj,
		Cq:                preemptionCtx.preemptorCQ,
		FrsNeedPreemption: preemptionCtx.frsNeedPreemption,
		Requests:          preemptionCtx.workloadUsage.Quota,
		WorkloadOrdering:  p.workloadOrdering,
	}
	candidatesGenerator := classical.NewCandidateIterator(hierarchicalReclaimCtx, preemptionCtx.frsNeedPreemption, preemptionCtx.snapshot, p.clock, CandidatesOrdering)
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

	for _, attemptOpts := range attemptPossibleOpts {
		var targets []*Target
		candidatesGenerator.Reset()
		for candidate, reason := candidatesGenerator.Next(attemptOpts.borrowing); candidate != nil; candidate, reason = candidatesGenerator.Next(attemptOpts.borrowing) {
			preemptionCtx.snapshot.RemoveWorkload(candidate)
			targets = append(targets, &Target{
				WorkloadInfo: candidate,
				Reason:       reason,
			})
			if workloadFits(preemptionCtx, attemptOpts.borrowing) {
				targets = fillBackWorkloads(preemptionCtx, targets, attemptOpts.borrowing)
				restoreSnapshot(preemptionCtx.snapshot, targets)
				return targets
			}
		}
		restoreSnapshot(preemptionCtx.snapshot, targets)
	}
	return nil
}

func fillBackWorkloads(preemptionCtx *preemptionCtx, targets []*Target, allowBorrowing bool) []*Target {
	// In the reverse order, check if any of the workloads can be added back.
	for i := len(targets) - 2; i >= 0; i-- {
		preemptionCtx.snapshot.AddWorkload(targets[i].WorkloadInfo)
		if workloadFits(preemptionCtx, allowBorrowing) {
			// O(1) deletion: copy the last element into index i and reduce size.
			targets[i] = targets[len(targets)-1]
			targets = targets[:len(targets)-1]
		} else {
			preemptionCtx.snapshot.RemoveWorkload(targets[i].WorkloadInfo)
		}
	}
	return targets
}

func restoreSnapshot(snapshot *cache.Snapshot, targets []*Target) {
	for _, t := range targets {
		snapshot.AddWorkload(t.WorkloadInfo)
	}
}

// parseStrategies converts an array of strategies into the functions to the used by the algorithm.
// This function takes advantage of the properties of the preemption algorithm and the strategies.
// The number of functions returned might not match the input slice.
func parseStrategies(s []config.PreemptionStrategy) []fairsharing.Strategy {
	if len(s) == 0 {
		return []fairsharing.Strategy{fairsharing.LessThanOrEqualToFinalShare, fairsharing.LessThanInitialShare}
	}
	strategies := make([]fairsharing.Strategy, len(s))
	for i, strategy := range s {
		switch strategy {
		case config.LessThanOrEqualToFinalShare:
			strategies[i] = fairsharing.LessThanOrEqualToFinalShare
		case config.LessThanInitialShare:
			strategies[i] = fairsharing.LessThanInitialShare
		}
	}
	return strategies
}

// runFirstFsStrategy runs the first configured FairSharing strategy,
// and returns (fits, targets, retryCandidates) retryCandidates may be
// used if rule S2-b is configured.
func runFirstFsStrategy(preemptionCtx *preemptionCtx, candidates []*workload.Info, strategy fairsharing.Strategy) (bool, []*Target, []*workload.Info) {
	ordering := fairsharing.MakeClusterQueueOrdering(preemptionCtx.preemptorCQ, candidates)
	var targets []*Target
	var retryCandidates []*workload.Info
	for candCQ := range ordering.Iter() {
		if candCQ.InClusterQueuePreemption() {
			candWl := candCQ.PopWorkload()
			preemptionCtx.snapshot.RemoveWorkload(candWl)
			targets = append(targets, &Target{
				WorkloadInfo: candWl,
				Reason:       kueue.InClusterQueueReason,
			})
			if workloadFitsForFairSharing(preemptionCtx) {
				return true, targets, nil
			}
			continue
		}

		preemptorNewShare, targetOldShare := candCQ.ComputeShares()
		for candCQ.HasWorkload() {
			candWl := candCQ.PopWorkload()
			targetNewShare := candCQ.ComputeTargetShareAfterRemoval(candWl)
			if strategy(preemptorNewShare, targetOldShare, targetNewShare) {
				preemptionCtx.snapshot.RemoveWorkload(candWl)
				reason := kueue.InCohortFairSharingReason

				targets = append(targets, &Target{
					WorkloadInfo: candWl,
					Reason:       reason,
				})
				if workloadFitsForFairSharing(preemptionCtx) {
					return true, targets, nil
				}
				// Might need to pick a different CQ due to changing values.
				break
			} else {
				retryCandidates = append(retryCandidates, candWl)
			}
		}
	}
	return false, targets, retryCandidates
}

// runSecondFsStrategy implements Fair Sharing Rule S2-b. It returns
// (fits, targets).
func runSecondFsStrategy(retryCandidates []*workload.Info, preemptionCtx *preemptionCtx, targets []*Target) (bool, []*Target) {
	ordering := fairsharing.MakeClusterQueueOrdering(preemptionCtx.preemptorCQ, retryCandidates)
	for candCQ := range ordering.Iter() {
		preemptorNewShare, targetOldShare := candCQ.ComputeShares()
		// Due to API validation, we can only reach here if the second strategy is LessThanInitialShare,
		// in which case the last parameter for the strategy function is irrelevant.
		if fairsharing.LessThanInitialShare(preemptorNewShare, targetOldShare, 0) {
			// The criteria doesn't depend on the preempted workload, so just preempt the first candidate.
			candWl := candCQ.PopWorkload()
			preemptionCtx.snapshot.RemoveWorkload(candWl)
			targets = append(targets, &Target{
				WorkloadInfo: candWl,
				Reason:       kueue.InCohortFairSharingReason,
			})
			if workloadFitsForFairSharing(preemptionCtx) {
				return true, targets
			}
		}
		// There doesn't seem to be an scenario where
		// it's possible to apply rule S2-b more than once in a CQ.
		ordering.DropQueue(candCQ)
	}
	return false, targets
}

func (p *Preemptor) fairPreemptions(preemptionCtx *preemptionCtx, strategies []fairsharing.Strategy) []*Target {
	candidates := p.findCandidates(preemptionCtx.preemptor.Obj, preemptionCtx.preemptorCQ, preemptionCtx.frsNeedPreemption)
	if len(candidates) == 0 {
		return nil
	}
	sort.Slice(candidates, func(i, j int) bool {
		return CandidatesOrdering(preemptionCtx.log, candidates[i], candidates[j], preemptionCtx.preemptorCQ.Name, p.clock.Now())
	})
	if logV := preemptionCtx.log.V(5); logV.Enabled() {
		logV.Info("Simulating fair preemption", "candidates", workload.References(candidates), "resourcesRequiringPreemption", preemptionCtx.frsNeedPreemption.UnsortedList(), "preemptingWorkload", klog.KObj(preemptionCtx.preemptor.Obj))
	}

	// DRS values must include incoming workload.
	revertSimulation := preemptionCtx.preemptorCQ.SimulateUsageAddition(preemptionCtx.workloadUsage)

	fits, targets, retryCandidates := runFirstFsStrategy(preemptionCtx, candidates, strategies[0])
	if !fits && len(strategies) > 1 {
		fits, targets = runSecondFsStrategy(retryCandidates, preemptionCtx, targets)
	}

	revertSimulation()
	if !fits {
		restoreSnapshot(preemptionCtx.snapshot, targets)
		return nil
	}
	targets = fillBackWorkloads(preemptionCtx, targets, true)
	restoreSnapshot(preemptionCtx.snapshot, targets)
	return targets
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

// findCandidates obtains candidates for preemption within the ClusterQueue and
// cohort that respect the preemption policy and are using a resource that the
// preempting workload needs.
func (p *Preemptor) findCandidates(wl *kueue.Workload, cq *cache.ClusterQueueSnapshot, frsNeedPreemption sets.Set[resources.FlavorResource]) []*workload.Info {
	var candidates []*workload.Info
	wlPriority := priority.Priority(wl)
	preemptorTS := p.workloadOrdering.GetQueueOrderTimestamp(wl)

	if cq.Preemption.WithinClusterQueue != kueue.PreemptionPolicyNever {
		for _, candidateWl := range cq.Workloads {
			candidatePriority := priority.Priority(candidateWl.Obj)
			switch cq.Preemption.WithinClusterQueue {
			case kueue.PreemptionPolicyLowerPriority:
				if candidatePriority >= wlPriority {
					continue
				}
			case kueue.PreemptionPolicyLowerOrNewerEqualPriority:
				if candidatePriority > wlPriority {
					continue
				}
				if candidatePriority == wlPriority && !preemptorTS.Before(p.workloadOrdering.GetQueueOrderTimestamp(candidateWl.Obj)) {
					continue
				}
			}

			if !classical.WorkloadUsesResources(candidateWl, frsNeedPreemption) {
				continue
			}
			candidates = append(candidates, candidateWl)
		}
	}

	if cq.HasParent() && cq.Preemption.ReclaimWithinCohort != kueue.PreemptionPolicyNever {
		for _, cohortCQ := range cq.Parent().Root().SubtreeClusterQueues() {
			if cq == cohortCQ || !cqIsBorrowing(cohortCQ, frsNeedPreemption) {
				// Can't reclaim quota from itself or ClusterQueues that are not borrowing.
				continue
			}
			for _, candidateWl := range cohortCQ.Workloads {
				candidatePriority := priority.Priority(candidateWl.Obj)
				switch cq.Preemption.ReclaimWithinCohort {
				case kueue.PreemptionPolicyLowerPriority:
					if candidatePriority >= wlPriority {
						continue
					}
				case kueue.PreemptionPolicyLowerOrNewerEqualPriority:
					if candidatePriority > wlPriority {
						continue
					}
					if candidatePriority == wlPriority && !preemptorTS.Before(p.workloadOrdering.GetQueueOrderTimestamp(candidateWl.Obj)) {
						continue
					}
				}
				if !classical.WorkloadUsesResources(candidateWl, frsNeedPreemption) {
					continue
				}
				candidates = append(candidates, candidateWl)
			}
		}
	}
	return candidates
}

func cqIsBorrowing(cq *cache.ClusterQueueSnapshot, frsNeedPreemption sets.Set[resources.FlavorResource]) bool {
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
func workloadFits(preemptionCtx *preemptionCtx, allowBorrowing bool) bool {
	for fr, v := range preemptionCtx.workloadUsage.Quota {
		if !allowBorrowing && preemptionCtx.preemptorCQ.BorrowingWith(fr, v) {
			return false
		}
		if v > preemptionCtx.preemptorCQ.Available(fr) {
			return false
		}
	}
	tasResult := preemptionCtx.preemptorCQ.FindTopologyAssignmentsForWorkload(preemptionCtx.tasRequests)
	return tasResult.Failure() == nil
}

// workloadFitsForFairSharing is a lightweight wrapper around
// workloadFits, as we need to remove, and then add back, the usage of
// the incoming workload, as FairSharing adds this usage at the start
// of processing for accurate DominantResourceShare calculations.
func workloadFitsForFairSharing(preemptionCtx *preemptionCtx) bool {
	revertSimulation := preemptionCtx.preemptorCQ.SimulateUsageRemoval(preemptionCtx.workloadUsage)
	res := workloadFits(preemptionCtx, true)
	revertSimulation()
	return res
}

func queueUnderNominalInResourcesNeedingPreemption(preemptionCtx *preemptionCtx) bool {
	for fr := range preemptionCtx.frsNeedPreemption {
		if preemptionCtx.preemptorCQ.ResourceNode.Usage[fr] >= preemptionCtx.preemptorCQ.QuotaFor(fr).Nominal {
			return false
		}
	}
	return true
}

func resourceUsagePreemptionEnabled(a, b *workload.Info) bool {
	// If both workloads are in the same ClusterQueue, but different LocalQueues,
	// we can compare their LocalQueue usage.
	// If the LocalQueueUsage is not nil for both Workloads, it means the feature gate has been enabled, and the
	// AdmissionScope of the ClusterQueue is set to UsageBasedFairSharing. We inherit this information from the snapshot initialization.
	return a.ClusterQueue == b.ClusterQueue && a.Obj.Spec.QueueName != b.Obj.Spec.QueueName && a.LocalQueueFSUsage != nil && b.LocalQueueFSUsage != nil
}

// candidatesOrdering criteria:
// 0. Workloads already marked for preemption first.
// 1. Workloads from other ClusterQueues in the cohort before the ones in the
// same ClusterQueue as the preemptor.
// 2. (AdmissionFairSharing only) Workloads with lower LocalQueue's usage first
// 3. Workloads with lower priority first.
// 4. Workloads admitted more recently first.
func CandidatesOrdering(log logr.Logger, a, b *workload.Info, cq kueue.ClusterQueueReference, now time.Time) bool {
	aEvicted := meta.IsStatusConditionTrue(a.Obj.Status.Conditions, kueue.WorkloadEvicted)
	bEvicted := meta.IsStatusConditionTrue(b.Obj.Status.Conditions, kueue.WorkloadEvicted)
	if aEvicted != bEvicted {
		return aEvicted
	}
	aInCQ := a.ClusterQueue == cq
	bInCQ := b.ClusterQueue == cq
	if aInCQ != bInCQ {
		return !aInCQ
	}

	if features.Enabled(features.AdmissionFairSharing) && resourceUsagePreemptionEnabled(a, b) {
		if a.LocalQueueFSUsage != b.LocalQueueFSUsage {
			log.V(3).Info("Comparing workloads by LocalQueue fair sharing usage",
				"workloadA", klog.KObj(a.Obj), "queueA", a.Obj.Spec.QueueName, "usageA", a.LocalQueueFSUsage,
				"workloadB", klog.KObj(b.Obj), "queueB", b.Obj.Spec.QueueName, "usageB", b.LocalQueueFSUsage)
			return *a.LocalQueueFSUsage > *b.LocalQueueFSUsage
		}
	}
	pa := priority.Priority(a.Obj)
	pb := priority.Priority(b.Obj)
	if pa != pb {
		return pa < pb
	}
	timeA := quotaReservationTime(a.Obj, now)
	timeB := quotaReservationTime(b.Obj, now)
	if !timeA.Equal(timeB) {
		return timeA.After(timeB)
	}
	// Arbitrary comparison for deterministic sorting.
	return a.Obj.UID < b.Obj.UID
}

func quotaReservationTime(wl *kueue.Workload, now time.Time) time.Time {
	cond := meta.FindStatusCondition(wl.Status.Conditions, kueue.WorkloadQuotaReserved)
	if cond == nil || cond.Status != metav1.ConditionTrue {
		// The condition wasn't populated yet, use the current time.
		return now
	}
	return cond.LastTransitionTime.Time
}
