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
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/cache"
	"sigs.k8s.io/kueue/pkg/constants"
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

func (p *Preemptor) Do(ctx context.Context, wl workload.Info, assignment flavorassigner.Assignment, snapshot *cache.Snapshot) (int, error) {
	log := ctrl.LoggerFrom(ctx)

	resPerFlv := resourcesRequiringPreemption(assignment)
	cq := snapshot.ClusterQueues[wl.ClusterQueue]

	candidates := findCandidates(wl.Obj, cq, resPerFlv)
	if len(candidates) == 0 {
		log.V(2).Info("Workload requires preemption, but there are no candidate workloads allowed for preemption", "preemptionReclaimWithinCohort", cq.Preemption.ReclaimWithinCohort, "preemptionWithinClusterQueue", cq.Preemption.WithinClusterQueue)
		return 0, nil
	}
	sort.Slice(candidates, candidatesOrdering(candidates, cq.Name, time.Now()))

	targets := minimalPreemptions(&wl, assignment, snapshot, resPerFlv, candidates)

	if len(targets) == 0 {
		log.V(2).Info("Workload requires preemption, but there are not enough candidate workloads allowed for preemption")
		return 0, nil
	}

	return p.issuePreemptions(ctx, targets, cq)
}

func (p *Preemptor) issuePreemptions(ctx context.Context, targets []*workload.Info, cq *cache.ClusterQueue) (int, error) {
	log := ctrl.LoggerFrom(ctx)
	errCh := routine.NewErrorChannel()
	ctx, cancel := context.WithCancel(ctx)
	var successfullyPreempted int64
	defer cancel()
	workqueue.ParallelizeUntil(ctx, parallelPreemptions, len(targets), func(i int) {
		target := targets[i]
		err := p.applyPreemption(ctx, workload.BaseSSAWorkload(target.Obj))
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
		atomic.AddInt64(&successfullyPreempted, 1)
	})
	return int(successfullyPreempted), errCh.ReceiveError()
}

func (p *Preemptor) applyPreemptionWithSSA(ctx context.Context, w *kueue.Workload) error {
	return p.client.Status().Patch(ctx, w, client.Apply, client.FieldOwner(constants.AdmissionName))
}

// minimalPreemptions implements a heuristic to find a minimal set of Workloads
// to preempt.
// The heuristic first removes candidates, in the input order, while their
// ClusterQueues are still borrowing resources and while the incoming Workload
// doesn't fit in the quota.
// Once the Worklod fits, the heuristic tries to add Workloads back, in the
// reverse order in which they were removed, while the incoming Workload still
// fits.
func minimalPreemptions(wl *workload.Info, assignment flavorassigner.Assignment, snapshot *cache.Snapshot, resPerFlv resourcesPerFlavor, candidates []*workload.Info) []*workload.Info {
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
		snapshot.RemoveWorkload(candWl)
		targets = append(targets, candWl)
		if workloadFits(wlReq, cq) {
			fits = true
			break
		}
	}
	if !fits {
		return nil
	}
	// In the reverse order, check if any of the workloads can be added back.
	for i := len(targets) - 2; i >= 0; i-- {
		snapshot.AddWorkload(targets[i])
		if workloadFits(wlReq, cq) {
			// O(1) deletion: copy the last element into index i and reduce size.
			targets[i] = targets[len(targets)-1]
			targets = targets[:len(targets)-1]
		} else {
			snapshot.RemoveWorkload(targets[i])
		}
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
	cqs := sets.New(cq)
	if cq.Cohort != nil && cq.Preemption.ReclaimWithinCohort != kueue.PreemptionPolicyNever {
		cqs = cq.Cohort.Members
	}
	if cq.Preemption.WithinClusterQueue == kueue.PreemptionPolicyNever {
		cqs.Delete(cq)
	}
	for cohortCQ := range cqs {
		onlyLowerPrio := true
		if cq != cohortCQ {
			if !cqIsBorrowing(cohortCQ, resPerFlv) {
				// Can't reclaim quota from ClusterQueues that are not borrowing.
				continue
			}
			if cq.Preemption.ReclaimWithinCohort == kueue.PreemptionPolicyAny {
				onlyLowerPrio = false
			}
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
	return candidates
}

func cqIsBorrowing(cq *cache.ClusterQueue, resPerFlv resourcesPerFlavor) bool {
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

// workloadFits determines if the workload requests would fits given the
// requestable resources and simulated usage of the ClusterQueue and its cohort,
// if it belongs to one.
// These two checks are a simplification compared to flavorassigner.fitsFlavorsLimits,
// because there is no borrowing when doing preemptions.
func workloadFits(wlReq cache.FlavorResourceQuantities, cq *cache.ClusterQueue) bool {
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
				if cqResUsage[rName]+rReq > flvQuotas.Resources[rName].Nominal {
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
// 3. Workloads admited more recently first.
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
		return admisionTime(a.Obj, now).Before(admisionTime(b.Obj, now))
	}
}

func admisionTime(wl *kueue.Workload, now time.Time) time.Time {
	cond := meta.FindStatusCondition(wl.Status.Conditions, kueue.WorkloadAdmitted)
	if cond == nil || cond.Status != metav1.ConditionTrue {
		// The condition wasn't populated yet, use the current time.
		return now
	}
	return cond.LastTransitionTime.Time
}
