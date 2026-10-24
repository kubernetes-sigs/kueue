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
	wassim "sigs.k8s.io/kueue/pkg/cache/scheduler/was"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/scheduler/assignment"
	"sigs.k8s.io/kueue/pkg/scheduler/flavorassigner"
	"sigs.k8s.io/kueue/pkg/scheduler/preemption"
	"sigs.k8s.io/kueue/pkg/workload"
)

// NewPlanner returns an implementation of the assignment Planner
// that utilizes the Scheduler Simulation Library to determine Pod placement.
func NewPlanner(
	wl *workload.Info,
	snapshot *schedcache.Snapshot,
	preemptor *preemption.Preemptor,
) assignment.Planner {
	return &wasPlanner{wl: wl, snapshot: snapshot, preemptor: preemptor}
}

var _ assignment.Planner = (*wasPlanner)(nil)

type wasPlanner struct {
	wl        *workload.Info
	snapshot  *schedcache.Snapshot
	preemptor *preemption.Preemptor
}

func (p *wasPlanner) Plan(ctx context.Context, initialAssignment *flavorassigner.Assignment, _ ...assignment.PlannerOption) (plan assignment.Plan) {
	plan = assignment.Plan{
		Assignment: initialAssignment,
	}
	if plan.Assignment.RepresentativeMode() == flavorassigner.NoFit {
		return
	}

	vPgs, err := wassim.BuildVirtualPodGroups(p.wl.Obj)
	if err != nil {
		plan.Error = fmt.Errorf("virtualization error: unable to build virtual pod groups: %w", err)
		return
	}
	if vPgs == nil {
		return
	}

	cq := p.snapshot.ClusterQueue(p.wl.ClusterQueue)
	pods, err := plan.Assignment.CandidateVirtualPods(p.wl, cq, vPgs.LeafPGForPod)
	if err != nil {
		plan.Error = fmt.Errorf("virtualization error: unable to build virtual pods: %w", err)
		return
	}
	if len(pods) == 0 {
		return
	}

	revert, err := p.addPodGroups(ctx, vPgs)
	if err != nil {
		plan.Error = fmt.Errorf("virtualization error: %w", err)
		return
	}
	defer func() {
		if err := revert(); err != nil {
			plan.Error = errors.Join(plan.Error, fmt.Errorf("virtualization error: %w", err))
		}
	}()

	var placementSummary podSetPlacementSummary
	var failures podSetFailureSummary
	if plan.Assignment.RepresentativeMode() != flavorassigner.Preempt {
		placementSummary, failures, _, plan.Error = p.schedule(ctx, pods)
	} else {
		placementSummary, failures, plan.PreemptionTargets, plan.Error = p.scheduleWithPreemption(ctx, pods, *plan.Assignment)
	}
	if plan.Error != nil {
		return
	}

	if len(failures) > 0 {
		// Record the scheduling failures of Pods that failed to secure any Placement.
		recordSimulationFailures(plan.Assignment, failures)
		// If any failures occurred, we can already confirm the assignment to be a no-fit.
		plan.Assignment.SetRepresentativeMode(flavorassigner.NoFit)
		plan.Assignment.NoFitReason = kueue.WorkloadQuotaReservedReasonSimulationFailed
		return
	}

	// Process topology assignments for PodSets with successfully calculated allotments.
	log := ctrl.LoggerFrom(ctx)
	tasRequests := plan.Assignment.WorkloadsTopologyRequests(log, p.wl, cq)
	tasAssignment := p.calculateTopologyAssignment(ctx, tasRequests, placementSummary)
	plan.Assignment.UpdateForTASResult(log, cq, p.wl, tasAssignment)
	return
}

func (p *wasPlanner) schedule(
	ctx context.Context,
	pods []*corev1.Pod,
	opts ...simulator.ScheduleOption,
) (podSetPlacementSummary, podSetFailureSummary, []client.ObjectKey, error) {
	schedulingResult := p.snapshot.SchedulerSimulator.ScheduleWorkload(ctx, pods, opts...)
	if schedulingResult.Error != nil {
		return nil, nil, nil, fmt.Errorf("failed to schedule workload: %w", schedulingResult.Error)
	}
	placementSummary, failures := groupByPodSet(pods, schedulingResult.PodPlacements)
	return placementSummary, failures, schedulingResult.PreemptedWorkloads, nil
}

func (p *wasPlanner) scheduleWithPreemption(
	ctx context.Context,
	pods []*corev1.Pod,
	assignment flavorassigner.Assignment,
) (podSetPlacementSummary, podSetFailureSummary, []*preemption.Target, error) {
	for strategy := range p.preemptor.GetPreemptionStrategyIterator(ctx, *p.wl, p.snapshot, assignment) {
		placementSummary, failures, preemptedWorkloads, err := p.schedule(
			ctx,
			pods,
			simulator.WithPreemptionCandidates(strategy.Candidates()),
			simulator.WithPreemptionFilter(&strategy),
		)
		strategy.Reset()
		if err != nil {
			return nil, nil, nil, err
		}
		if len(failures) == 0 {
			if targets := strategy.Verify(preemptedWorkloads); len(targets) > 0 {
				return placementSummary, nil, targets, nil
			}
		}
	}

	// No preemption targets succeeded. Simulate against an empty cluster to check
	// if the workload can fit at all and, if so, reserve TAS resources while waiting
	// for quota so lower-priority workloads do not admit ahead of it.
	placementSummary, failures, _, err := p.schedule(ctx, pods, simulator.WithSimulateEmpty(true))
	if err != nil {
		return nil, nil, nil, err
	}
	return placementSummary, failures, nil, nil
}

func recordSimulationFailures(assignment *flavorassigner.Assignment, failures podSetFailureSummary) {
	for psRef, summary := range failures {
		assignment.ResolvePodSetFailure(
			psRef,
			flavorassigner.NoFit,
			summary.asStatus().WithNoFitReason(kueue.WorkloadQuotaReservedReasonSimulationFailed),
		)
	}
	if features.Enabled(features.UnadmittedWorkloadsObservability) {
		for psRef := range failures {
			psa := assignment.PodSetAssignmentByName(psRef)
			for _, flv := range psa.Flavors {
				psa.MarkFlavorAttempt(flv.Name, flavorassigner.NoFit, kueue.WorkloadQuotaReservedReasonSimulationFailed)
			}
		}
	}
}

// groupByPodSet groups the PodPlacements by PodSet
// and counts Pods of the PodSet per Node assigned.
// If any Pod fails to be scheduled, the whole PodSet reports the failure
// and no healthy allotments are reported, as we will immediately branch into a failure case.
func groupByPodSet(
	pods []*corev1.Pod,
	placements simulator.PodPlacements,
) (podSetPlacementSummary, podSetFailureSummary) {
	placementSummary := make(podSetPlacementSummary)
	failureSummary := make(podSetFailureSummary)
	podsByKey := map[client.ObjectKey]*corev1.Pod{}
	for _, pod := range pods {
		podsByKey[client.ObjectKeyFromObject(pod)] = pod
	}
	for podRef, placement := range placements {
		pod := podsByKey[podRef]
		psRef := kueue.PodSetReference(pod.Labels[constants.PodSetLabel])
		if placement.IsSuccess() {
			placementSummary.countPodPlacement(psRef, placement.Node)
		} else {
			failureSummary.add(psRef, podRef, placement.Error, placement.Reasons)
		}
	}
	if len(failureSummary) > 0 {
		placementSummary = nil
	}
	return placementSummary, failureSummary
}

func (p *wasPlanner) calculateTopologyAssignment(
	ctx context.Context,
	tasRequests schedcache.WorkloadTASRequests,
	placementSummary podSetPlacementSummary,
) schedcache.TASAssignmentsResult {
	cq := p.snapshot.ClusterQueue(p.wl.ClusterQueue)
	tasAssignment := make(schedcache.TASAssignmentsResult, len(placementSummary))
	for flavor, requests := range tasRequests {
		tasFlavorCache := cq.TASFlavors[flavor]
		for _, psRequests := range requests {
			psRef := psRequests.PodSet.Name
			if podsPerNode, psRequested := placementSummary[psRef]; psRequested {
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

func (p *wasPlanner) addPodGroups(ctx context.Context, vPgs *wassim.WorkloadVirtualPodGroups) (func() error, error) {
	for i, cpg := range vPgs.CompositePodGroups {
		if err := p.snapshot.SchedulerSimulator.AddCompositePodGroup(ctx, cpg); err != nil {
			for _, added := range vPgs.CompositePodGroups[:i] {
				_ = p.snapshot.SchedulerSimulator.RemoveCompositePodGroup(ctx, added)
			}
			return nil, fmt.Errorf("unable to add composite pod group %q to simulator: %w", cpg.Name, err)
		}
	}
	for i, pg := range vPgs.PodGroups {
		if err := p.snapshot.SchedulerSimulator.AddPodGroup(ctx, pg); err != nil {
			for _, added := range vPgs.PodGroups[:i] {
				_ = p.snapshot.SchedulerSimulator.RemovePodGroup(ctx, added)
			}
			for _, added := range vPgs.CompositePodGroups {
				_ = p.snapshot.SchedulerSimulator.RemoveCompositePodGroup(ctx, added)
			}
			return nil, fmt.Errorf("unable to add pod group %q to simulator: %w", pg.Name, err)
		}
	}

	revert := func() error {
		var errs []error
		for _, pg := range vPgs.PodGroups {
			if err := p.snapshot.SchedulerSimulator.RemovePodGroup(ctx, pg); err != nil {
				errs = append(errs, fmt.Errorf("unable to remove pod group %q from simulator: %w", pg.Name, err))
			}
		}
		for _, cpg := range vPgs.CompositePodGroups {
			if err := p.snapshot.SchedulerSimulator.RemoveCompositePodGroup(ctx, cpg); err != nil {
				errs = append(errs, fmt.Errorf("unable to remove composite pod group %q from simulator: %w", cpg.Name, err))
			}
		}
		return errors.Join(errs...)
	}

	return revert, nil
}
