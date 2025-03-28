/*
Copyright 2023 The Kubernetes Authors.

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
	"sigs.k8s.io/kueue/pkg/metrics"
	"sigs.k8s.io/kueue/pkg/resources"
	"sigs.k8s.io/kueue/pkg/scheduler/flavorassigner"
	"sigs.k8s.io/kueue/pkg/util/heap"
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
	fsStrategies      []fsStrategy

	// stubs
	applyPreemption func(ctx context.Context, w *kueue.Workload, reason, message string) error
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

func candidatesOnlyFromQueue(candidates []*workload.Info, clusterQueue string) []*workload.Info {
	result := make([]*workload.Info, 0, len(candidates))
	for _, wi := range candidates {
		if wi.ClusterQueue == clusterQueue {
			result = append(result, wi)
		}
	}
	return result
}

func candidatesFromCQOrUnderThreshold(candidates []*workload.Info, clusterQueue string, threshold int32) []*workload.Info {
	result := make([]*workload.Info, 0, len(candidates))
	for _, wi := range candidates {
		if wi.ClusterQueue == clusterQueue || priority.Priority(wi.Obj) < threshold {
			result = append(result, wi)
		}
	}
	return result
}

type Target struct {
	WorkloadInfo *workload.Info
	Reason       string
}

// GetTargets returns the list of workloads that should be evicted in
// order to make room for wl.
func (p *Preemptor) GetTargets(log logr.Logger, wl workload.Info, assignment flavorassigner.Assignment, snapshot *cache.Snapshot) []*Target {
	frsNeedPreemption := flavorResourcesNeedPreemption(assignment)
	requests := assignment.TotalRequestsFor(&wl)
	return p.getTargets(log, wl, requests, frsNeedPreemption, snapshot)
}

func (p *Preemptor) getTargets(log logr.Logger, wl workload.Info, requests resources.FlavorResourceQuantities,
	frsNeedPreemption sets.Set[resources.FlavorResource], snapshot *cache.Snapshot) []*Target {
	cq := snapshot.ClusterQueues[wl.ClusterQueue]
	candidates := p.findCandidates(wl.Obj, cq, frsNeedPreemption)
	if len(candidates) == 0 {
		return nil
	}
	sort.Slice(candidates, candidatesOrdering(candidates, cq.Name, p.clock.Now()))

	sameQueueCandidates := candidatesOnlyFromQueue(candidates, wl.ClusterQueue)

	// To avoid flapping, Kueue only allows preemption of workloads from the same
	// queue if borrowing. Preemption of workloads from queues can happen only
	// if not borrowing at the same time. Kueue prioritizes preemption of
	// workloads from the other queues (that borrowed resources) first, before
	// trying to preempt more own workloads and borrow at the same time.
	if len(sameQueueCandidates) == len(candidates) {
		// There is no possible preemption of workloads from other queues,
		// so we'll try borrowing.
		return minimalPreemptions(log, requests, cq, snapshot, frsNeedPreemption, candidates, true, nil)
	}

	borrowWithinCohort, thresholdPrio := canBorrowWithinCohort(cq, wl.Obj)
	if p.enableFairSharing {
		return p.fairPreemptions(log, wl, requests, snapshot, frsNeedPreemption, candidates, thresholdPrio)
	}
	// There is a potential of preemption of workloads from the other queue in the
	// cohort. We proceed with borrowing only if the dedicated policy
	// (borrowWithinCohort) is enabled. This ensures the preempted workloads
	// have lower priority, and so they will not preempt the preemptor when
	// requeued.
	if borrowWithinCohort {
		if !queueUnderNominalInResourcesNeedingPreemption(frsNeedPreemption, cq) {
			// It can only preempt workloads from another CQ if they are strictly under allowBorrowingBelowPriority.
			candidates = candidatesFromCQOrUnderThreshold(candidates, wl.ClusterQueue, *thresholdPrio)
		}
		return minimalPreemptions(log, requests, cq, snapshot, frsNeedPreemption, candidates, true, thresholdPrio)
	}

	// Only try preemptions in the cohort, without borrowing, if the target clusterqueue is still
	// under nominal quota for all resources.
	if queueUnderNominalInResourcesNeedingPreemption(frsNeedPreemption, cq) {
		if targets := minimalPreemptions(log, requests, cq, snapshot, frsNeedPreemption, candidates, false, nil); len(targets) > 0 {
			return targets
		}
	}

	// Final attempt. This time only candidates from the same queue, but
	// with borrowing.
	return minimalPreemptions(log, requests, cq, snapshot, frsNeedPreemption, sameQueueCandidates, true, nil)
}

// canBorrowWithinCohort returns whether the behavior is enabled for the ClusterQueue and the threshold priority to use.
func canBorrowWithinCohort(cq *cache.ClusterQueueSnapshot, wl *kueue.Workload) (bool, *int32) {
	borrowWithinCohort := cq.Preemption.BorrowWithinCohort
	if borrowWithinCohort == nil || borrowWithinCohort.Policy == kueue.BorrowWithinCohortPolicyNever {
		return false, nil
	}
	threshold := priority.Priority(wl)
	if borrowWithinCohort.MaxPriorityThreshold != nil && *borrowWithinCohort.MaxPriorityThreshold < threshold {
		threshold = *borrowWithinCohort.MaxPriorityThreshold + 1
	}
	return true, &threshold
}

var HumanReadablePreemptionReasons = map[string]string{
	kueue.InClusterQueueReason:                "prioritization in the ClusterQueue",
	kueue.InCohortReclamationReason:           "reclamation within the cohort",
	kueue.InCohortFairSharingReason:           "fair sharing within the cohort",
	kueue.InCohortReclaimWhileBorrowingReason: "reclamation within the cohort while borrowing",
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
			message := fmt.Sprintf("Preempted to accommodate a workload (UID: %s) due to %s", preemptor.Obj.UID, HumanReadablePreemptionReasons[target.Reason])
			err := p.applyPreemption(ctx, target.WorkloadInfo.Obj, target.Reason, message)
			if err != nil {
				errCh.SendErrorWithCancel(err, cancel)
				return
			}

			log.V(3).Info("Preempted", "targetWorkload", klog.KObj(target.WorkloadInfo.Obj), "reason", target.Reason, "message", message, "targetClusterQueue", klog.KRef("", target.WorkloadInfo.ClusterQueue))
			p.recorder.Eventf(target.WorkloadInfo.Obj, corev1.EventTypeNormal, "Preempted", message)
			metrics.ReportPreemption(preemptor.ClusterQueue, target.Reason, target.WorkloadInfo.ClusterQueue)
		} else {
			log.V(3).Info("Preemption ongoing", "targetWorkload", klog.KObj(target.WorkloadInfo.Obj))
		}
		successfullyPreempted.Add(1)
	})
	return int(successfullyPreempted.Load()), errCh.ReceiveError()
}

func (p *Preemptor) applyPreemptionWithSSA(ctx context.Context, w *kueue.Workload, reason, message string) error {
	w = w.DeepCopy()
	workload.SetEvictedCondition(w, kueue.WorkloadEvictedByPreemption, message)
	workload.ResetChecksOnEviction(w, p.clock.Now())
	workload.SetPreemptedCondition(w, reason, message)
	return workload.ApplyAdmissionStatus(ctx, p.client, w, true)
}

// minimalPreemptions implements a heuristic to find a minimal set of Workloads
// to preempt.
// The heuristic first removes candidates, in the input order, while their
// ClusterQueues are still borrowing resources and while the incoming Workload
// doesn't fit in the quota.
// Once the Workload fits, the heuristic tries to add Workloads back, in the
// reverse order in which they were removed, while the incoming Workload still
// fits.
func minimalPreemptions(log logr.Logger, requests resources.FlavorResourceQuantities, cq *cache.ClusterQueueSnapshot, snapshot *cache.Snapshot, frsNeedPreemption sets.Set[resources.FlavorResource], candidates []*workload.Info, allowBorrowing bool, allowBorrowingBelowPriority *int32) []*Target {
	if logV := log.V(5); logV.Enabled() {
		logV.Info("Simulating preemption", "candidates", workload.References(candidates), "resourcesRequiringPreemption", frsNeedPreemption, "allowBorrowing", allowBorrowing, "allowBorrowingBelowPriority", allowBorrowingBelowPriority)
	}
	// Simulate removing all candidates from the ClusterQueue and cohort.
	var targets []*Target
	fits := false
	for _, candWl := range candidates {
		candCQ := snapshot.ClusterQueues[candWl.ClusterQueue]
		reason := kueue.InClusterQueueReason
		if cq != candCQ {
			if !cqIsBorrowing(candCQ, frsNeedPreemption) {
				continue
			}
			reason = kueue.InCohortReclamationReason
			if allowBorrowingBelowPriority != nil {
				if priority.Priority(candWl.Obj) >= *allowBorrowingBelowPriority {
					// We set allowBorrowing=false if there is a candidate with priority
					// exceeding allowBorrowingBelowPriority added to targets.
					//
					// We need to be careful mutating allowBorrowing. We rely on the
					// fact that once there is a candidate exceeding the priority added
					// to targets, then at least one such candidate is present in the
					// final set of targets (after the second phase of the function).
					//
					// This is true, because the candidates are ordered according
					// to priorities (from lowest to highest, using candidatesOrdering),
					// and the last added target is not removed in the second phase of
					// the function.
					allowBorrowing = false
				} else {
					reason = kueue.InCohortReclaimWhileBorrowingReason
				}
			}
		}
		snapshot.RemoveWorkload(candWl)
		targets = append(targets, &Target{
			WorkloadInfo: candWl,
			Reason:       reason,
		})
		if workloadFits(requests, cq, allowBorrowing) {
			fits = true
			break
		}
	}
	if !fits {
		restoreSnapshot(snapshot, targets)
		return nil
	}
	targets = fillBackWorkloads(targets, requests, cq, snapshot, allowBorrowing)
	restoreSnapshot(snapshot, targets)
	return targets
}

func fillBackWorkloads(targets []*Target, requests resources.FlavorResourceQuantities, cq *cache.ClusterQueueSnapshot, snapshot *cache.Snapshot, allowBorrowing bool) []*Target {
	// In the reverse order, check if any of the workloads can be added back.
	for i := len(targets) - 2; i >= 0; i-- {
		snapshot.AddWorkload(targets[i].WorkloadInfo)
		if workloadFits(requests, cq, allowBorrowing) {
			// O(1) deletion: copy the last element into index i and reduce size.
			targets[i] = targets[len(targets)-1]
			targets = targets[:len(targets)-1]
		} else {
			snapshot.RemoveWorkload(targets[i].WorkloadInfo)
		}
	}
	return targets
}

func restoreSnapshot(snapshot *cache.Snapshot, targets []*Target) {
	for _, t := range targets {
		snapshot.AddWorkload(t.WorkloadInfo)
	}
}

type fsStrategy func(preemptorNewShare, preempteeOldShare, preempteeNewShare int) bool

// lessThanOrEqualToFinalShare implements Rule S2-a in https://sigs.k8s.io/kueue/keps/1714-fair-sharing#choosing-workloads-from-clusterqueues-for-preemption
func lessThanOrEqualToFinalShare(preemptorNewShare, _, preempteeNewShare int) bool {
	return preemptorNewShare <= preempteeNewShare
}

// lessThanInitialShare implements rule S2-b in https://sigs.k8s.io/kueue/keps/1714-fair-sharing#choosing-workloads-from-clusterqueues-for-preemption
func lessThanInitialShare(preemptorNewShare, preempteeOldShare, _ int) bool {
	return preemptorNewShare < preempteeOldShare
}

// parseStrategies converts an array of strategies into the functions to the used by the algorithm.
// This function takes advantage of the properties of the preemption algorithm and the strategies.
// The number of functions returned might not match the input slice.
func parseStrategies(s []config.PreemptionStrategy) []fsStrategy {
	if len(s) == 0 {
		return []fsStrategy{lessThanOrEqualToFinalShare, lessThanInitialShare}
	}
	strategies := make([]fsStrategy, len(s))
	for i, strategy := range s {
		switch strategy {
		case config.LessThanOrEqualToFinalShare:
			strategies[i] = lessThanOrEqualToFinalShare
		case config.LessThanInitialShare:
			strategies[i] = lessThanInitialShare
		}
	}
	return strategies
}

func (p *Preemptor) fairPreemptions(log logr.Logger, wl workload.Info, requests resources.FlavorResourceQuantities, snapshot *cache.Snapshot, frsNeedPreemption sets.Set[resources.FlavorResource], candidates []*workload.Info, allowBorrowingBelowPriority *int32) []*Target {
	if logV := log.V(5); logV.Enabled() {
		logV.Info("Simulating fair preemption", "candidates", workload.References(candidates), "resourcesRequiringPreemption", frsNeedPreemption, "allowBorrowingBelowPriority", allowBorrowingBelowPriority)
	}
	cqHeap := cqHeapFromCandidates(candidates, false, snapshot)
	nominatedCQ := snapshot.ClusterQueues[wl.ClusterQueue]
	newNominatedShareValue, _ := nominatedCQ.DominantResourceShareWith(requests)
	var targets []*Target
	fits := false
	var retryCandidates []*workload.Info
	for cqHeap.Len() > 0 && !fits {
		candCQ := cqHeap.Pop()

		if candCQ.cq == nominatedCQ {
			candWl := candCQ.workloads[0]
			snapshot.RemoveWorkload(candWl)
			targets = append(targets, &Target{
				WorkloadInfo: candWl,
				Reason:       kueue.InClusterQueueReason,
			})
			if workloadFits(requests, nominatedCQ, true) {
				fits = true
				break
			}
			newNominatedShareValue, _ = nominatedCQ.DominantResourceShareWith(requests)
			candCQ.workloads = candCQ.workloads[1:]
			if len(candCQ.workloads) > 0 {
				candCQ.share, _ = candCQ.cq.DominantResourceShare()
				cqHeap.PushIfNotPresent(candCQ)
			}
			continue
		}

		for i, candWl := range candCQ.workloads {
			belowThreshold := allowBorrowingBelowPriority != nil && priority.Priority(candWl.Obj) < *allowBorrowingBelowPriority
			newCandShareVal, _ := candCQ.cq.DominantResourceShareWithout(candWl.FlavorResourceUsage())
			strategy := p.fsStrategies[0](newNominatedShareValue, candCQ.share, newCandShareVal)
			if belowThreshold || strategy {
				snapshot.RemoveWorkload(candWl)
				reason := kueue.InCohortFairSharingReason
				if !strategy {
					reason = kueue.InCohortReclaimWhileBorrowingReason
				}

				targets = append(targets, &Target{
					WorkloadInfo: candWl,
					Reason:       reason,
				})
				if workloadFits(requests, nominatedCQ, true) {
					fits = true
					break
				}
				candCQ.workloads = candCQ.workloads[i+1:]
				if len(candCQ.workloads) > 0 && cqIsBorrowing(candCQ.cq, frsNeedPreemption) {
					candCQ.share = newCandShareVal
					cqHeap.PushIfNotPresent(candCQ)
				}
				// Might need to pick a different CQ due to changing values.
				break
			} else {
				retryCandidates = append(retryCandidates, candCQ.workloads[i])
			}
		}
	}
	if !fits && len(p.fsStrategies) > 1 {
		// Try next strategy if the previous strategy wasn't enough
		cqHeap = cqHeapFromCandidates(retryCandidates, true, snapshot)

		for cqHeap.Len() > 0 && !fits {
			candCQ := cqHeap.Pop()
			// Due to API validation, we can only reach here if the second strategy is LessThanInitialShare,
			// in which case the last parameter for the strategy function is irrelevant.
			if p.fsStrategies[1](newNominatedShareValue, candCQ.share, 0) {
				// The criteria doesn't depend on the preempted workload, so just preempt the first candidate.
				candWl := candCQ.workloads[0]
				snapshot.RemoveWorkload(candWl)
				targets = append(targets, &Target{
					WorkloadInfo: candWl,
					Reason:       kueue.InCohortFairSharingReason,
				})
				if workloadFits(requests, nominatedCQ, true) {
					fits = true
				}
				// No requeueing because there doesn't seem to be an scenario where
				// it's possible to apply rule S2-b more than once in a CQ.
			}
		}
	}
	if !fits {
		restoreSnapshot(snapshot, targets)
		return nil
	}
	targets = fillBackWorkloads(targets, requests, nominatedCQ, snapshot, true)
	restoreSnapshot(snapshot, targets)
	return targets
}

type candidateCQ struct {
	cq        *cache.ClusterQueueSnapshot
	workloads []*workload.Info
	share     int
}

func cqHeapFromCandidates(candidates []*workload.Info, firstOnly bool, snapshot *cache.Snapshot) *heap.Heap[candidateCQ] {
	cqHeap := heap.New(
		func(c *candidateCQ) string {
			return c.cq.Name
		},
		func(c1, c2 *candidateCQ) bool {
			return c1.share > c2.share
		},
	)
	for _, cand := range candidates {
		candCQ := cqHeap.GetByKey(cand.ClusterQueue)
		if candCQ == nil {
			cq := snapshot.ClusterQueues[cand.ClusterQueue]
			share, _ := cq.DominantResourceShare()
			candCQ = &candidateCQ{
				cq:        cq,
				share:     share,
				workloads: []*workload.Info{cand},
			}
			cqHeap.PushOrUpdate(candCQ)
		} else if !firstOnly {
			candCQ.workloads = append(candCQ.workloads, cand)
		}
	}
	return cqHeap
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

	if cq.Preemption.WithinClusterQueue != kueue.PreemptionPolicyNever {
		considerSamePrio := (cq.Preemption.WithinClusterQueue == kueue.PreemptionPolicyLowerOrNewerEqualPriority)
		preemptorTS := p.workloadOrdering.GetQueueOrderTimestamp(wl)

		for _, candidateWl := range cq.Workloads {
			candidatePriority := priority.Priority(candidateWl.Obj)
			if candidatePriority > wlPriority {
				continue
			}

			if candidatePriority == wlPriority && !(considerSamePrio && preemptorTS.Before(p.workloadOrdering.GetQueueOrderTimestamp(candidateWl.Obj))) {
				continue
			}

			if !workloadUsesResources(candidateWl, frsNeedPreemption) {
				continue
			}
			candidates = append(candidates, candidateWl)
		}
	}

	if cq.HasParent() && cq.Preemption.ReclaimWithinCohort != kueue.PreemptionPolicyNever {
		onlyLowerPriority := cq.Preemption.ReclaimWithinCohort != kueue.PreemptionPolicyAny
		for _, cohortCQ := range cq.Parent().Root().SubtreeClusterQueues() {
			if cq == cohortCQ || !cqIsBorrowing(cohortCQ, frsNeedPreemption) {
				// Can't reclaim quota from itself or ClusterQueues that are not borrowing.
				continue
			}
			for _, candidateWl := range cohortCQ.Workloads {
				if onlyLowerPriority && priority.Priority(candidateWl.Obj) >= priority.Priority(wl) {
					continue
				}
				if !workloadUsesResources(candidateWl, frsNeedPreemption) {
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

func workloadUsesResources(wl *workload.Info, frsNeedPreemption sets.Set[resources.FlavorResource]) bool {
	for _, ps := range wl.TotalRequests {
		for res, flv := range ps.Flavors {
			if frsNeedPreemption.Has(resources.FlavorResource{Flavor: flv, Resource: res}) {
				return true
			}
		}
	}
	return false
}

// workloadFits determines if the workload requests would fit given the
// requestable resources and simulated usage of the ClusterQueue and its cohort,
// if it belongs to one.
func workloadFits(requests resources.FlavorResourceQuantities, cq *cache.ClusterQueueSnapshot, allowBorrowing bool) bool {
	for fr, v := range requests {
		if !allowBorrowing && cq.BorrowingWith(fr, v) {
			return false
		}
		if v > cq.Available(fr) {
			return false
		}
	}
	return true
}

func queueUnderNominalInResourcesNeedingPreemption(frsNeedPreemption sets.Set[resources.FlavorResource], cq *cache.ClusterQueueSnapshot) bool {
	for fr := range frsNeedPreemption {
		if cq.ResourceNode.Usage[fr] >= cq.QuotaFor(fr).Nominal {
			return false
		}
	}
	return true
}

// candidatesOrdering criteria:
// 0. Workloads already marked for preemption first.
// 1. Workloads from other ClusterQueues in the cohort before the ones in the
// same ClusterQueue as the preemptor.
// 2. Workloads with lower priority first.
// 3. Workloads admitted more recently first.
func candidatesOrdering(candidates []*workload.Info, cq string, now time.Time) func(int, int) bool {
	return func(i, j int) bool {
		a := candidates[i]
		b := candidates[j]
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
}

func quotaReservationTime(wl *kueue.Workload, now time.Time) time.Time {
	cond := meta.FindStatusCondition(wl.Status.Conditions, kueue.WorkloadQuotaReserved)
	if cond == nil || cond.Status != metav1.ConditionTrue {
		// The condition wasn't populated yet, use the current time.
		return now
	}
	return cond.LastTransitionTime.Time
}
