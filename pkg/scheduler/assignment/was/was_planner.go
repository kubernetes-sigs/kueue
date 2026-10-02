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

package was

import (
	"context"

	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	corev1 "k8s.io/api/core/v1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	schedcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/cache/scheduler/simulator"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/scheduler/assignment"
	"sigs.k8s.io/kueue/pkg/scheduler/flavorassigner"
	"sigs.k8s.io/kueue/pkg/util/tas"
	"sigs.k8s.io/kueue/pkg/workload"
)

// NewPlanner returns an impelmentation of the assignment Planner
// that utilizes the Sheduler Simulation Library to determine Pod placement.
func NewPlanner(
	wl *workload.Info,
	snapshot *schedcache.Snapshot,
) assignment.Planner {
	return &wasPlanner{wl: wl, snapshot: snapshot}
}

var _ assignment.Planner = (*wasPlanner)(nil)

type wasPlanner struct {
	wl       *workload.Info
	snapshot *schedcache.Snapshot
}

func (p *wasPlanner) Plan(ctx context.Context, initialAssignment *flavorassigner.Assignment, _ ...assignment.PlannerOption) (plan assignment.Plan) {
	log := ctrl.LoggerFrom(ctx)
	plan = assignment.Plan{
		Assignment: initialAssignment,
	}

	if plan.Assignment.RepresentativeMode() == flavorassigner.Preempt {
		// Preemtpions are not supported on this path yet.
		// If a workload requires preemptions, it is to be demoted to NoFit.
		setNoFit(plan.Assignment, kueue.WorkloadQuotaReservedReasonWaitingForQuota)
	}
	if plan.Assignment.RepresentativeMode() == flavorassigner.NoFit {
		return
	}

	cq := p.snapshot.ClusterQueue(p.wl.ClusterQueue)
	tasRequests := plan.Assignment.WorkloadsTopologyRequests(log, p.wl, cq)
	pods := listPodsToProcess(p.wl, tasRequests)
	schedulingResult := p.snapshot.SchedulerSimulator.ScheduleWorkload(ctx, pods)
	allotments, failures := calculateAllotments(pods, schedulingResult.PodPlacements)

	// Process topology assignments for PodSets with successfully calculated allotments
	tasAssignment := p.calculateTopologyAssignment(ctx, tasRequests, allotments)
	plan.Assignment.UpdateForTASResult(log, cq, p.wl, tasAssignment)

	// Record information for failed assignments
	for i, psAssignment := range plan.Assignment.PodSets {
		// If given pod set is being processed and
		// it was not successfully assigned, record the failure in the Assignment.
		if failure, found := failures[psAssignment.Name]; found {
			reasons, err := failure.Summarize()
			plan.Assignment.PodSets[i].Status = *flavorassigner.NewStatus(reasons...).
				WithError(err).
				WithNoFitReason(kueue.WorkloadQuotaReservedReasonTopologyPlacementFailed)
		}
	}

	// If the TAS assignment failed explicitly or does not cover all
	// processed PodSets, it means the workload does not fit.
	if tasAssignment.Failure() != nil || len(failures) > 0 {
		setNoFit(plan.Assignment, kueue.WorkloadQuotaReservedReasonTopologyPlacementFailed)
	}
	return
}

func setNoFit(a *flavorassigner.Assignment, reason string) {
	a.SetRepresentativeMode(flavorassigner.NoFit)
	a.NoFitReason = reason
}

// TODO (1): We need a way to create virtual pods here
// to pass to ScheduleWorkload.
// TODO (2): We need virtual PodGroups to be created
// for the virtual pods for ScheduleWorkload to work.
// This method should take tasRequests into account,
// making sure it returns all pods we need to process for this workload.
func listPodsToProcess(wl *workload.Info, tasRequests schedcache.WorkloadTASRequests) []*corev1.Pod {
	return nil
}

// calculateAllotments groups the PodPlacements by PodSet
// and calculate Node allotments for each PodSet.
// If any Pod fails to be scheduled, the whole PodSet reports the failrue
// and is ommited form the allotments list.
func calculateAllotments(pods []*corev1.Pod, podPlacements simulator.PodPlacements) (allotments tas.Allotments, failures tas.AllotmentFailures) {
	allotments = make(tas.Allotments)
	failures = make(tas.AllotmentFailures)
	podsByKey := map[client.ObjectKey]*corev1.Pod{}
	for _, pod := range pods {
		podsByKey[client.ObjectKeyFromObject(pod)] = pod
	}
	for podRef, placement := range podPlacements {
		pod := podsByKey[podRef]
		psRef := kueue.PodSetReference(pod.Labels[constants.PodSetLabel])
		switch {
		case placement.IsError:
			failures.RecordError(psRef, client.ObjectKeyFromObject(pod), placement.Error, placement.FailureReasons)
		case !placement.IsSuccess:
			failures.RecordFailure(psRef, client.ObjectKeyFromObject(pod), placement.FailureReasons)
		default:
			allotments.RecordAllotment(psRef, placement.Node)
		}
	}

	// We only consider allotments that did not fail for any pod as valid
	for psRef := range failures {
		delete(allotments, psRef)
	}

	return allotments, failures
}

func (p *wasPlanner) calculateTopologyAssignment(
	ctx context.Context,
	tasRequests schedcache.WorkloadTASRequests,
	allotments tas.Allotments,
) schedcache.TASAssignmentsResult {
	cq := p.snapshot.ClusterQueue(p.wl.ClusterQueue)
	tasAssignment := make(schedcache.TASAssignmentsResult, len(allotments))
	for flavor, requests := range tasRequests.OrderedIterator() {
		tasFlavorCache := cq.TASFlavors[flavor]
		for _, psRequests := range requests {
			psRef := kueue.PodSetReference(psRequests.PodSet.Name)
			if allotment, found := allotments[psRef]; found {
				tasAssignment[psRef] = *tasFlavorCache.BuildTopologyAssignmentsForPodSet(
					ctx,
					psRequests.PodSet,
					psRequests.Flavor,
					allotment,
				)
			}
		}
	}
	return tasAssignment
}
