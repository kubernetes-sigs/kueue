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
	"errors"
	"fmt"

	corev1 "k8s.io/api/core/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	schedcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/cache/scheduler/simulator"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/scheduler/assignment"
	"sigs.k8s.io/kueue/pkg/scheduler/flavorassigner"
	"sigs.k8s.io/kueue/pkg/workload"
)

// NewPlanner returns an implementation of the assignment Planner
// that utilizes the Scheduler Simulation Library to determine Pod placement.
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
	plan = assignment.Plan{
		Assignment: initialAssignment,
	}
	if plan.Assignment.RepresentativeMode() == flavorassigner.NoFit {
		return
	}
	if plan.Assignment.RepresentativeMode() == flavorassigner.Preempt {
		plan.Error = errors.New("WAS Planner does not support preemptions")
		return
	}

	cq := p.snapshot.ClusterQueue(p.wl.ClusterQueue)
	pods, err := plan.Assignment.CandidateVirtualPods(p.wl, cq)
	if err != nil {
		plan.Error = fmt.Errorf("failed to virtualize workload: %w", err)
		return
	}
	if len(pods) == 0 {
		return
	}

	// ScheduleWorkload will always return a failed result here for wasScheduler,
	// until Virtual Pod Groups are implemented and added to the ClusterSnapshot.
	schedulingResult := p.snapshot.SchedulerSimulator.ScheduleWorkload(ctx, pods)
	if schedulingResult.Error != nil {
		plan.Error = fmt.Errorf("failed to schedule workload: %w", schedulingResult.Error)
		return
	}

	placementBreakdown, failures := groupByPodSet(pods, schedulingResult.PodPlacements)

	// Record the scheduling failures of Pods that failed to secure any Placement.
	for psRef, summary := range failures {
		plan.Assignment.ResolvePodSetFailure(
			psRef,
			flavorassigner.NoFit,
			summary.asStatus().WithNoFitReason(kueue.WorkloadQuotaReservedReasonSimulationFailed),
		)
	}
	// If any failures occurred, we can already confirm the assignment to be a no-fit.
	if len(failures) > 0 {
		setNoFit(plan.Assignment, kueue.WorkloadQuotaReservedReasonSimulationFailed)
		return
	}

	// Process topology assignments for PodSets with successfully calculated allotments.
	log := ctrl.LoggerFrom(ctx)
	tasRequests := plan.Assignment.WorkloadsTopologyRequests(log, p.wl, cq)
	tasAssignment := p.calculateTopologyAssignment(ctx, tasRequests, placementBreakdown)

	// Record topology assignments failures.
	tasFails := tasAssignment.Failures()
	for _, failure := range tasFails {
		plan.Assignment.ResolvePodSetFailure(
			failure.PodSetName,
			flavorassigner.NoFit,
			flavorassigner.NewStatus(failure.Reason).WithNoFitReason(kueue.WorkloadQuotaReservedReasonTopologyPlacementFailed),
		)
	}
	if len(tasFails) > 0 {
		setNoFit(plan.Assignment, kueue.WorkloadQuotaReservedReasonTopologyPlacementFailed)
		return
	}

	// Update assignment for a successful TAS Result
	plan.Assignment.UpdateForTASResult(log, cq, p.wl, tasAssignment)
	return
}

func setNoFit(a *flavorassigner.Assignment, reason string) {
	a.SetRepresentativeMode(flavorassigner.NoFit)
	a.NoFitReason = reason
}

// groupByPodSet groups the PodPlacements by PodSet
// and counts Pods of the PodSet per Node assigned.
// If any Pod fails to be scheduled, the whole PodSet reports the failure
// and no healthy allotments are reported, as we will immediately branch into a failure case.
func groupByPodSet(
	pods []*corev1.Pod,
	placements simulator.PodPlacements,
) (breakdown placementBreakdown, failures failuresBreakdown) {
	breakdown = make(placementBreakdown)
	failures = make(failuresBreakdown)
	podsByKey := map[client.ObjectKey]*corev1.Pod{}
	for _, pod := range pods {
		podsByKey[client.ObjectKeyFromObject(pod)] = pod
	}
	for podRef, placement := range placements {
		pod := podsByKey[podRef]
		psRef := kueue.PodSetReference(pod.Labels[constants.PodSetLabel])
		if placement.IsSuccess() {
			breakdown.countPodPlacement(psRef, placement.Node)
		} else {
			failures.add(psRef, podRef, placement.Error, placement.Reasons)
		}
	}
	if len(failures) > 0 {
		breakdown = nil
	}
	return breakdown, failures
}

func (p *wasPlanner) calculateTopologyAssignment(
	ctx context.Context,
	tasRequests schedcache.WorkloadTASRequests,
	placementBreakdown placementBreakdown,
) schedcache.TASAssignmentsResult {
	cq := p.snapshot.ClusterQueue(p.wl.ClusterQueue)
	tasAssignment := make(schedcache.TASAssignmentsResult, len(placementBreakdown))
	for flavor, requests := range tasRequests {
		tasFlavorCache := cq.TASFlavors[flavor]
		for _, psRequests := range requests {
			psRef := psRequests.PodSet.Name
			if podsPerNode, psRequested := placementBreakdown[psRef]; psRequested {
				tasAssignment[psRef] = *tasFlavorCache.BuildTopologyAssignmentsForPodSet(
					ctx,
					psRequests.PodSet,
					psRequests.Flavor,
					podsPerNode,
				)
			}
		}
	}
	return tasAssignment
}
