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
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/cache"
	"sigs.k8s.io/kueue/pkg/scheduler/flavorassigner"
	"sigs.k8s.io/kueue/pkg/util/priority"
	"sigs.k8s.io/kueue/pkg/util/routine"
	"sigs.k8s.io/kueue/pkg/workload"
)

const parallelPreemptions = 8

type Preemptor struct {
	client   client.Client
	recorder record.EventRecorder

	// stubs
	applyPreemption func(context.Context, *kueue.Workload) error
}

func New(cl client.Client, recorder record.EventRecorder) *Preemptor {
	p := &Preemptor{
		client:   cl,
		recorder: recorder,
	}
	p.applyPreemption = p.applyPreemptionWithSSA
	return p
}

func (p *Preemptor) OverrideApply(f func(context.Context, *kueue.Workload) error) {
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

// GetTargets returns the list of workloads that should be evicted in order to make room for wl.
func (p *Preemptor) GetTargets(wl workload.Info, assignment flavorassigner.Assignment, snapshot *cache.Snapshot) []*workload.Info {
	resPerFlv := resourcesRequiringPreemption(assignment)
	cq := snapshot.ClusterQueues[wl.ClusterQueue]

	candidates := findCandidates(wl.Obj, cq, resPerFlv)
	if len(candidates) == 0 {
		return nil
	}
	sort.Slice(candidates, candidatesOrdering(candidates, cq.Name, time.Now()))

	sameQueueCandidates := candidatesOnlyFromQueue(candidates, wl.ClusterQueue)

	// To avoid flapping, Kueue only allows preemption of workloads from the same
	// queue if borrowing. Preemption of workloads from queues can happen only
	// if not borrowing at the same time. Kueue prioritizes preemption of
	// workloads from the other queues (that borrowed resources) first, before
	// trying to preempt more own workloads and borrow at the same time.

	if len(sameQueueCandidates) == len(candidates) {
		// There is no possible preemption of workloads from other queues,
		// so we'll try borrowing.
		return minimalPreemptions(&wl, assignment, snapshot, resPerFlv, candidates, true, nil)
	}

	// There is a risk of preemption of workloads from the other queue in the
	// cohort. We proceed with borrowing only if the dedicated policy
	// (borrowWithinCohort) is enabled. This ensures the preempted workloads
	// have lower priority, and so they will not preempt the preemptor when
	// requeued.
	borrowWithinCohort := cq.Preemption.BorrowWithinCohort
	if borrowWithinCohort != nil && borrowWithinCohort.Policy != kueue.BorrowWithinCohortPolicyNever {
		allowBorrowingBelowPriority := ptr.To(priority.Priority(wl.Obj))
		if borrowWithinCohort.MaxPriorityThreshold != nil && *borrowWithinCohort.MaxPriorityThreshold < *allowBorrowingBelowPriority {
			allowBorrowingBelowPriority = ptr.To(*borrowWithinCohort.MaxPriorityThreshold + 1)
		}
		return minimalPreemptions(&wl, assignment, snapshot, resPerFlv, candidates, true, allowBorrowingBelowPriority)
	}
	targets := minimalPreemptions(&wl, assignment, snapshot, resPerFlv, candidates, false, nil)
	if len(targets) == 0 {
		// Another attempt. This time only candidates from the same queue, but
		// with borrowing. The previous attempt didn't try borrowing and had broader
		// scope of preemption.
		targets = minimalPreemptions(&wl, assignment, snapshot, resPerFlv, sameQueueCandidates, true, nil)
	}
	return targets
}

// IssuePreemptions marks the target workloads as evicted.
func (p *Preemptor) IssuePreemptions(ctx context.Context, targets []*workload.Info, cq *cache.ClusterQueue) (int, error) {
	log := ctrl.LoggerFrom(ctx)
	errCh := routine.NewErrorChannel()
	ctx, cancel := context.WithCancel(ctx)
	var successfullyPreempted int64
	defer cancel()
	workqueue.ParallelizeUntil(ctx, parallelPreemptions, len(targets), func(i int) {
		target := targets[i]
		if !meta.IsStatusConditionTrue(target.Obj.Status.Conditions, kueue.WorkloadEvicted) {
			err := p.applyPreemption(ctx, target.Obj)
			if err != nil {
				errCh.SendErrorWithCancel(err, cancel)
				return
			}

			origin := "ClusterQueue"
			if cq.Name != target.ClusterQueue {
				origin = "cohort"
			}
			log.V(3).Info("Preempted", "targetWorkload", klog.KObj(target.Obj))
			p.recorder.Eventf(target.Obj, corev1.EventTypeNormal, "Preempted", "Preempted by another workload in the %s", origin)
		} else {
			log.V(3).Info("Preemption ongoing", "targetWorkload", klog.KObj(target.Obj))
		}
		atomic.AddInt64(&successfullyPreempted, 1)
	})
	return int(successfullyPreempted), errCh.ReceiveError()
}

func (p *Preemptor) applyPreemptionWithSSA(ctx context.Context, w *kueue.Workload) error {
	w = w.DeepCopy()
	workload.SetEvictedCondition(w, kueue.WorkloadEvictedByPreemption, "Preempted to accommodate a higher priority Workload")
	return workload.ApplyAdmissionStatus(ctx, p.client, w, false)
}

// minimalPreemptions implements a heuristic to find a minimal set of Workloads
// to preempt.
// The heuristic first removes candidates, in the input order, while their
// ClusterQueues are still borrowing resources and while the incoming Workload
// doesn't fit in the quota.
// Once the Workload fits, the heuristic tries to add Workloads back, in the
// reverse order in which they were removed, while the incoming Workload still
// fits.
func minimalPreemptions(wl *workload.Info, assignment flavorassigner.Assignment, snapshot *cache.Snapshot, resPerFlv resourcesPerFlavor, candidates []*workload.Info, allowBorrowing bool, allowBorrowingBelowPriority *int32) []*workload.Info {
	wlReq := totalRequestsForAssignment(wl, assignment)
	cq := snapshot.ClusterQueues[wl.ClusterQueue]

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
		// Reset changes to the snapshot.
		for _, t := range targets {
			snapshot.AddWorkload(t)
		}
		return nil
	}

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
	// Reset changes to the snapshot.
	for _, t := range targets {
		snapshot.AddWorkload(t)
	}

	return targets
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
func findCandidates(wl *kueue.Workload, cq *cache.ClusterQueue, resPerFlv resourcesPerFlavor) []*workload.Info {
	var candidates []*workload.Info
	wlPriority := priority.Priority(wl)

	if cq.Preemption.WithinClusterQueue != kueue.PreemptionPolicyNever {
		considerSamePrio := (cq.Preemption.WithinClusterQueue == kueue.PreemptionPolicyLowerOrNewerEqualPriority)
		preemptorTS := workload.GetQueueOrderTimestamp(wl)

		for _, candidateWl := range cq.Workloads {
			candidatePriority := priority.Priority(candidateWl.Obj)
			if candidatePriority > wlPriority {
				continue
			}

			if candidatePriority == wlPriority && !(considerSamePrio && preemptorTS.Before(workload.GetQueueOrderTimestamp(candidateWl.Obj))) {
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

func totalRequestsForAssignment(wl *workload.Info, assignment flavorassigner.Assignment) cache.FlavorResourceQuantities {
	usage := make(cache.FlavorResourceQuantities)
	for i, ps := range wl.TotalRequests {
		for res, q := range ps.Requests {
			flv := assignment.PodSets[i].Flavors[res].Name
			resUsage := usage[flv]
			if resUsage == nil {
				resUsage = make(map[corev1.ResourceName]int64)
				usage[flv] = resUsage
			}
			resUsage[res] += q
		}
	}
	return usage
}

// workloadFits determines if the workload requests would fit given the
// requestable resources and simulated usage of the ClusterQueue and its cohort,
// if it belongs to one.
func workloadFits(wlReq cache.FlavorResourceQuantities, cq *cache.ClusterQueue, allowBorrowing bool) bool {
	for _, rg := range cq.ResourceGroups {
		for _, flvQuotas := range rg.Flavors {
			flvReq, found := wlReq[flvQuotas.Name]
			if !found {
				// Workload doesn't request this flavor.
				continue
			}
			cqResUsage := cq.Usage[flvQuotas.Name]
			var cohortResUsage, cohortResRequestable map[corev1.ResourceName]int64
			if cq.Cohort != nil {
				cohortResUsage = cq.Cohort.Usage[flvQuotas.Name]
				cohortResRequestable = cq.Cohort.RequestableResources[flvQuotas.Name]
			}
			for rName, rReq := range flvReq {
				limit := flvQuotas.Resources[rName].Nominal

				// No need to check whether cohort is nil here because when borrowingLimit
				// is non nil, cohort is always non nil.
				if allowBorrowing && flvQuotas.Resources[rName].BorrowingLimit != nil {
					limit += *flvQuotas.Resources[rName].BorrowingLimit
				}

				if cqResUsage[rName]+rReq > limit {
					return false
				}

				if cq.Cohort != nil && cohortResUsage[rName]+rReq > cohortResRequestable[rName] {
					return false
				}
			}
		}
	}
	return true
}

// candidatesOrdering criteria:
// 1. Workloads from other ClusterQueues in the cohort before the ones in the
// same ClusterQueue as the preemptor.
// 2. Workloads with lower priority first.
// 3. Workloads admitted more recently first.
func candidatesOrdering(candidates []*workload.Info, cq string, now time.Time) func(int, int) bool {
	return func(i, j int) bool {
		a := candidates[i]
		b := candidates[j]
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
		return quotaReservationTime(b.Obj, now).Before(quotaReservationTime(a.Obj, now))
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
