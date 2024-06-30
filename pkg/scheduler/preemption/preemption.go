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

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/client-go/tools/record"
	"k8s.io/client-go/util/workqueue"
	"k8s.io/klog/v2"
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
	client   client.Client
	recorder record.EventRecorder

	workloadOrdering  workload.Ordering
	enableFairSharing bool
	fsStrategies      []fsStrategy

	// stubs
	applyPreemption func(context.Context, *kueue.Workload, string, string) error
}

func New(cl client.Client, workloadOrdering workload.Ordering, recorder record.EventRecorder, fs config.FairSharing) *Preemptor {
	p := &Preemptor{
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

// GetTargets returns the list of workloads that should be evicted in order to make room for wl.
func (p *Preemptor) GetTargets(wl workload.Info, assignment flavorassigner.Assignment, snapshot *cache.Snapshot) []*workload.Info {
	resPerFlv := resourcesRequiringPreemption(assignment)
	cq := snapshot.ClusterQueues[wl.ClusterQueue]

	candidates := findCandidates(wl.Obj, p.workloadOrdering, cq, resPerFlv)
	if len(candidates) == 0 {
		return nil
	}
	sort.Slice(candidates, candidatesOrdering(candidates, cq.Name, time.Now()))

	sameQueueCandidates := candidatesOnlyFromQueue(candidates, wl.ClusterQueue)
	wlReq := assignment.TotalRequestsFor(&wl)

	// To avoid flapping, Kueue only allows preemption of workloads from the same
	// queue if borrowing. Preemption of workloads from queues can happen only
	// if not borrowing at the same time. Kueue prioritizes preemption of
	// workloads from the other queues (that borrowed resources) first, before
	// trying to preempt more own workloads and borrow at the same time.
	if len(sameQueueCandidates) == len(candidates) {
		// There is no possible preemption of workloads from other queues,
		// so we'll try borrowing.
		return minimalPreemptions(wlReq, cq, snapshot, resPerFlv, candidates, true, nil)
	}

	borrowWithinCohort, thresholdPrio := canBorrowWithinCohort(cq, wl.Obj)
	if p.enableFairSharing {
		return p.fairPreemptions(&wl, assignment, snapshot, resPerFlv, candidates, thresholdPrio)
	}
	// There is a potential of preemption of workloads from the other queue in the
	// cohort. We proceed with borrowing only if the dedicated policy
	// (borrowWithinCohort) is enabled. This ensures the preempted workloads
	// have lower priority, and so they will not preempt the preemptor when
	// requeued.
	if borrowWithinCohort {
		if !queueUnderNominalInAllRequestedResources(wlReq, cq) {
			// It can only preempt workloads from another CQ if they are strictly under allowBorrowingBelowPriority.
			candidates = candidatesFromCQOrUnderThreshold(candidates, wl.ClusterQueue, *thresholdPrio)
		}
		return minimalPreemptions(wlReq, cq, snapshot, resPerFlv, candidates, true, thresholdPrio)
	}

	// Only try preemptions in the cohort, without borrowing, if the target clusterqueue is still
	// under nominal quota for all resources.
	if queueUnderNominalInAllRequestedResources(wlReq, cq) {
		if targets := minimalPreemptions(wlReq, cq, snapshot, resPerFlv, candidates, false, nil); len(targets) > 0 {
			return targets
		}
	}

	// Final attempt. This time only candidates from the same queue, but
	// with borrowing.
	return minimalPreemptions(wlReq, cq, snapshot, resPerFlv, sameQueueCandidates, true, nil)
}

// canBorrowWithinCohort returns whether the behavior is enabled for the ClusterQueue and the threshold priority to use.
func canBorrowWithinCohort(cq *cache.ClusterQueue, wl *kueue.Workload) (bool, *int32) {
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

// IssuePreemptions marks the target workloads as evicted.
func (p *Preemptor) IssuePreemptions(ctx context.Context, preemptor *workload.Info, targets []*workload.Info, cq *cache.ClusterQueue) (int, error) {
	log := ctrl.LoggerFrom(ctx)
	errCh := routine.NewErrorChannel()
	ctx, cancel := context.WithCancel(ctx)
	var successfullyPreempted int64
	defer cancel()
	workqueue.ParallelizeUntil(ctx, parallelPreemptions, len(targets), func(i int) {
		target := targets[i]
		if !meta.IsStatusConditionTrue(target.Obj.Status.Conditions, kueue.WorkloadEvicted) {
			origin := "ClusterQueue"
			reason := "InClusterQueue"
			if cq.Name != target.ClusterQueue {
				origin = "cohort"
				reason = "InCohort"
			}

			message := fmt.Sprintf("Preempted to accommodate a workload (UID: %s) in the %s", preemptor.Obj.UID, origin)
			err := p.applyPreemption(ctx, target.Obj, reason, message)
			if err != nil {
				errCh.SendErrorWithCancel(err, cancel)
				return
			}

			log.V(3).Info("Preempted", "targetWorkload", klog.KObj(target.Obj), "reason", reason, "message", message)
			p.recorder.Eventf(target.Obj, corev1.EventTypeNormal, "Preempted", message)
			metrics.ReportEvictedWorkloads(target.ClusterQueue, kueue.WorkloadEvictedByPreemption)
		} else {
			log.V(3).Info("Preemption ongoing", "targetWorkload", klog.KObj(target.Obj))
		}
		atomic.AddInt64(&successfullyPreempted, 1)
	})
	return int(successfullyPreempted), errCh.ReceiveError()
}

func (p *Preemptor) applyPreemptionWithSSA(ctx context.Context, w *kueue.Workload, reason, message string) error {
	w = w.DeepCopy()
	workload.SetEvictedCondition(w, kueue.WorkloadEvictedByPreemption, message)
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
func minimalPreemptions(wlReq resources.FlavorResourceQuantities, cq *cache.ClusterQueue, snapshot *cache.Snapshot, resPerFlv resourcesPerFlavor, candidates []*workload.Info, allowBorrowing bool, allowBorrowingBelowPriority *int32) []*workload.Info {
	// Simulate removing all candidates from the ClusterQueue and cohort.
	var targets []*workload.Info
	fits := false
	for _, candWl := range candidates {
		candCQ := snapshot.ClusterQueues[candWl.ClusterQueue]
		if cq != candCQ && !cqIsBorrowing(candCQ, resPerFlv) {
			continue
		}
		if cq != candCQ && allowBorrowingBelowPriority != nil && priority.Priority(candWl.Obj) >= *allowBorrowingBelowPriority {
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
		}
		snapshot.RemoveWorkload(candWl)
		targets = append(targets, candWl)
		if workloadFits(wlReq, cq, allowBorrowing) {
			fits = true
			break
		}
	}
	if !fits {
		restoreSnapshot(snapshot, targets)
		return nil
	}
	targets = fillBackWorkloads(targets, wlReq, cq, snapshot, allowBorrowing)
	restoreSnapshot(snapshot, targets)
	return targets
}

func fillBackWorkloads(targets []*workload.Info, wlReq resources.FlavorResourceQuantities, cq *cache.ClusterQueue, snapshot *cache.Snapshot, allowBorrowing bool) []*workload.Info {
	// In the reverse order, check if any of the workloads can be added back.
	for i := len(targets) - 2; i >= 0; i-- {
		snapshot.AddWorkload(targets[i])
		if workloadFits(wlReq, cq, allowBorrowing) {
			// O(1) deletion: copy the last element into index i and reduce size.
			targets[i] = targets[len(targets)-1]
			targets = targets[:len(targets)-1]
		} else {
			snapshot.RemoveWorkload(targets[i])
		}
	}
	return targets
}

func restoreSnapshot(snapshot *cache.Snapshot, targets []*workload.Info) {
	for _, t := range targets {
		snapshot.AddWorkload(t)
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

func (p *Preemptor) fairPreemptions(wl *workload.Info, assignment flavorassigner.Assignment, snapshot *cache.Snapshot, resPerFlv resourcesPerFlavor, candidates []*workload.Info, allowBorrowingBelowPriority *int32) []*workload.Info {
	cqHeap := cqHeapFromCandidates(candidates, false, snapshot)
	nominatedCQ := snapshot.ClusterQueues[wl.ClusterQueue]
	wlReq := assignment.TotalRequestsFor(wl)
	newNominatedShareValue, _ := nominatedCQ.DominantResourceShareWith(wlReq)
	var targets []*workload.Info
	fits := false
	var retryCandidates []*workload.Info
	for cqHeap.Len() > 0 && !fits {
		candCQ := cqHeap.Pop()

		if candCQ.cq == nominatedCQ {
			candWl := candCQ.workloads[0]
			snapshot.RemoveWorkload(candWl)
			targets = append(targets, candWl)
			if workloadFits(wlReq, nominatedCQ, true) {
				fits = true
				break
			}
			newNominatedShareValue, _ = nominatedCQ.DominantResourceShareWith(wlReq)
			candCQ.workloads = candCQ.workloads[1:]
			if len(candCQ.workloads) > 0 {
				candCQ.share, _ = candCQ.cq.DominantResourceShare()
				cqHeap.PushIfNotPresent(candCQ)
			}
			continue
		}

		for i, candWl := range candCQ.workloads {
			belowThreshold := allowBorrowingBelowPriority != nil && priority.Priority(candWl.Obj) < *allowBorrowingBelowPriority
			newCandShareVal, _ := candCQ.cq.DominantResourceShareWithout(candWl)
			if belowThreshold || p.fsStrategies[0](newNominatedShareValue, candCQ.share, newCandShareVal) {
				snapshot.RemoveWorkload(candWl)
				targets = append(targets, candWl)
				if workloadFits(wlReq, nominatedCQ, true) {
					fits = true
					break
				}
				candCQ.workloads = candCQ.workloads[i+1:]
				if len(candCQ.workloads) > 0 && cqIsBorrowing(candCQ.cq, resPerFlv) {
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
				targets = append(targets, candWl)
				if workloadFits(wlReq, nominatedCQ, true) {
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
	targets = fillBackWorkloads(targets, wlReq, nominatedCQ, snapshot, true)
	restoreSnapshot(snapshot, targets)
	return targets
}

type candidateCQ struct {
	cq        *cache.ClusterQueue
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

type resourcesPerFlavor map[kueue.ResourceFlavorReference]sets.Set[corev1.ResourceName]

func resourcesRequiringPreemption(assignment flavorassigner.Assignment) resourcesPerFlavor {
	resPerFlavor := make(resourcesPerFlavor)
	for _, ps := range assignment.PodSets {
		for res, flvAssignment := range ps.Flavors {
			// assignments with NoFit mode wouldn't enter the preemption path.
			if flvAssignment.Mode != flavorassigner.Preempt {
				continue
			}
			if resPerFlavor[flvAssignment.Name] == nil {
				resPerFlavor[flvAssignment.Name] = sets.New(res)
			} else {
				resPerFlavor[flvAssignment.Name].Insert(res)
			}
		}
	}
	return resPerFlavor
}

// findCandidates obtains candidates for preemption within the ClusterQueue and
// cohort that respect the preemption policy and are using a resource that the
// preempting workload needs.
func findCandidates(wl *kueue.Workload, wo workload.Ordering, cq *cache.ClusterQueue, resPerFlv resourcesPerFlavor) []*workload.Info {
	var candidates []*workload.Info
	wlPriority := priority.Priority(wl)

	if cq.Preemption.WithinClusterQueue != kueue.PreemptionPolicyNever {
		considerSamePrio := (cq.Preemption.WithinClusterQueue == kueue.PreemptionPolicyLowerOrNewerEqualPriority)
		preemptorTS := wo.GetQueueOrderTimestamp(wl)

		for _, candidateWl := range cq.Workloads {
			candidatePriority := priority.Priority(candidateWl.Obj)
			if candidatePriority > wlPriority {
				continue
			}

			if candidatePriority == wlPriority && !(considerSamePrio && preemptorTS.Before(wo.GetQueueOrderTimestamp(candidateWl.Obj))) {
				continue
			}

			if !workloadUsesResources(candidateWl, resPerFlv) {
				continue
			}
			candidates = append(candidates, candidateWl)
		}
	}

	if cq.Cohort != nil && cq.Preemption.ReclaimWithinCohort != kueue.PreemptionPolicyNever {
		for cohortCQ := range cq.Cohort.Members {
			if cq == cohortCQ || !cqIsBorrowing(cohortCQ, resPerFlv) {
				// Can't reclaim quota from itself or ClusterQueues that are not borrowing.
				continue
			}
			onlyLowerPrio := true
			if cq.Preemption.ReclaimWithinCohort == kueue.PreemptionPolicyAny {
				onlyLowerPrio = false
			}
			for _, candidateWl := range cohortCQ.Workloads {
				if onlyLowerPrio && priority.Priority(candidateWl.Obj) >= priority.Priority(wl) {
					continue
				}
				if !workloadUsesResources(candidateWl, resPerFlv) {
					continue
				}
				candidates = append(candidates, candidateWl)
			}
		}
	}
	return candidates
}

func cqIsBorrowing(cq *cache.ClusterQueue, resPerFlv resourcesPerFlavor) bool {
	if cq.Cohort == nil {
		return false
	}
	for _, rg := range cq.ResourceGroups {
		for _, fQuotas := range rg.Flavors {
			fUsage := cq.Usage[fQuotas.Name]
			for rName := range resPerFlv[fQuotas.Name] {
				if fUsage[rName] > fQuotas.Resources[rName].Nominal {
					return true
				}
			}
		}
	}
	return false
}

func workloadUsesResources(wl *workload.Info, resPerFlv resourcesPerFlavor) bool {
	for _, ps := range wl.TotalRequests {
		for res, flv := range ps.Flavors {
			if resPerFlv[flv].Has(res) {
				return true
			}
		}
	}
	return false
}

// workloadFits determines if the workload requests would fit given the
// requestable resources and simulated usage of the ClusterQueue and its cohort,
// if it belongs to one.
func workloadFits(wlReq resources.FlavorResourceQuantities, cq *cache.ClusterQueue, allowBorrowing bool) bool {
	for _, rg := range cq.ResourceGroups {
		for _, flvQuotas := range rg.Flavors {
			flvReq, found := wlReq[flvQuotas.Name]
			if !found {
				// Workload doesn't request this flavor.
				continue
			}
			cqResUsage := cq.Usage[flvQuotas.Name]
			for rName, rReq := range flvReq {
				resource := flvQuotas.Resources[rName]

				if cq.Cohort == nil || !allowBorrowing {
					if cqResUsage[rName]+rReq > resource.Nominal {
						return false
					}
				} else {
					// When resource.BorrowingLimit == nil there is no borrowing
					// limit, so we can skip the check.
					if resource.BorrowingLimit != nil {
						if cqResUsage[rName]+rReq > resource.Nominal+*resource.BorrowingLimit {
							return false
						}
					}
				}

				if cq.Cohort != nil {
					cohortResUsage := cq.UsedCohortQuota(flvQuotas.Name, rName)
					requestableQuota := cq.RequestableCohortQuota(flvQuotas.Name, rName)
					if cohortResUsage+rReq > requestableQuota {
						return false
					}
				}
			}
		}
	}
	return true
}

func queueUnderNominalInAllRequestedResources(wlReq resources.FlavorResourceQuantities, cq *cache.ClusterQueue) bool {
	for _, rg := range cq.ResourceGroups {
		for _, flvQuotas := range rg.Flavors {
			flvReq, found := wlReq[flvQuotas.Name]
			if !found {
				// Workload doesn't request this flavor.
				continue
			}
			cqResUsage := cq.Usage[flvQuotas.Name]
			for rName := range flvReq {
				if cqResUsage[rName] >= flvQuotas.Resources[rName].Nominal {
					return false
				}
			}
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
