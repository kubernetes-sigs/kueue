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
	// This line will always return an error until virtual PodGroups
	// are being properly handled and added to the ClusterSnapshot.
	pods, err := plan.Assignment.CandidateVirtualPods(p.wl, cq)
	if err != nil {
		plan.Error = fmt.Errorf("failed to virtualize workload: %w", err)
		return
	}

	schedulingResult := p.snapshot.SchedulerSimulator.ScheduleWorkload(ctx, pods)
	if schedulingResult.Error != nil {
		plan.Error = fmt.Errorf("failed to schedule workload: %w", schedulingResult.Error)
		return
	}

	allotments, failures := calculateAllotments(pods, schedulingResult.PodPlacements)

	// Record the scheduling failures of Pods that failed to secure any Placement.
	for psRef, failSummary := range failures {
		plan.Assignment.ResolvePodSetFailure(
			psRef,
			flavorassigner.NoFit,
			failSummary.asStatus(),
		)
	}
	if len(failures) > 0 {
		setNoFit(plan.Assignment)
		return
	}

	// Process topology assignments for PodSets with successfully calculated allotments.
	log := ctrl.LoggerFrom(ctx)
	tasRequests := plan.Assignment.WorkloadsTopologyRequests(log, p.wl, cq)
	tasAssignment := p.calculateTopologyAssignment(ctx, tasRequests, allotments)

	// Record topology assignments failures.
	for failure := range tasAssignment.Failures() {
		plan.Assignment.ResolvePodSetFailure(
			failure.PodSetName,
			flavorassigner.NoFit,
			*flavorassigner.NewStatus(failure.Reason),
		)
	}

	if tasAssignment.Failure() != nil {
		setNoFit(plan.Assignment)
		return
	}

	// Update assignment for a successful TAS Result
	plan.Assignment.UpdateForTASResult(log, cq, p.wl, tasAssignment)
	return
}

func setNoFit(a *flavorassigner.Assignment) {
	a.SetRepresentativeMode(flavorassigner.NoFit)
	a.NoFitReason = kueue.WorkloadQuotaReservedReasonTopologyPlacementFailed
}

// calculateAllotments groups the PodPlacements by PodSet
// and calculate Node allotments for each PodSet.
// If any Pod fails to be scheduled, the whole PodSet reports the failure
// and is omitted from the allotments list.
func calculateAllotments(
	pods []*corev1.Pod,
	podPlacements simulator.PodPlacements,
) (allots allotments, fails allotmentFailures) {
	allots = make(allotments)
	fails = make(allotmentFailures)
	podsByKey := map[client.ObjectKey]*corev1.Pod{}
	for _, pod := range pods {
		podsByKey[client.ObjectKeyFromObject(pod)] = pod
	}
	for podRef, placement := range podPlacements {
		pod := podsByKey[podRef]
		psRef := kueue.PodSetReference(pod.Labels[constants.PodSetLabel])
		switch {
		case placement.IsError():
			fails.recordError(psRef, client.ObjectKeyFromObject(pod), placement.AsError(), placement.Reasons())
		case !placement.IsSuccess():
			fails.recordFailure(psRef, client.ObjectKeyFromObject(pod), placement.Reasons())
		default:
			allots.recordAllotment(psRef, placement.Node())
		}
	}

	// We only consider allotments that did not fail for any pod as valid
	for psRef := range fails {
		delete(allots, psRef)
	}

	return allots, fails
}

func (p *wasPlanner) calculateTopologyAssignment(
	ctx context.Context,
	tasRequests schedcache.WorkloadTASRequests,
	allotments allotments,
) schedcache.TASAssignmentsResult {
	cq := p.snapshot.ClusterQueue(p.wl.ClusterQueue)
	tasAssignment := make(schedcache.TASAssignmentsResult, len(allotments))
	for flavor, requests := range tasRequests.OrderedIterator() {
		tasFlavorCache := cq.TASFlavors[flavor]
		for _, psRequests := range requests {
			psRef := psRequests.PodSet.Name
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
