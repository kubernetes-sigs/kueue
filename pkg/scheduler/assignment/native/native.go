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

package native

import (
	"context"

	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/log"

	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/scheduler/assignment"
	"sigs.k8s.io/kueue/pkg/scheduler/flavorassigner"
	"sigs.k8s.io/kueue/pkg/scheduler/preemption"
	"sigs.k8s.io/kueue/pkg/workload"
	workloadevict "sigs.k8s.io/kueue/pkg/workload/evict"
)

// NewPlanner returns a native implementation of assignment.Planner
// that utilizes the internal Kueue topology-assignment and preemption logic.
// It is meant to emulate the real scheduler via internally defined heuristics.
func NewPlanner(
	wl *workload.Info,
	snapshot *schdcache.Snapshot,
	preemptor *preemption.Preemptor,
	assigner *flavorassigner.FlavorAssigner,
) assignment.Planner {
	return &nativePlanner{wl, snapshot, preemptor, assigner}
}

var _ assignment.Planner = (*nativePlanner)(nil)

type nativePlanner struct {
	wl        *workload.Info
	snapshot  *schdcache.Snapshot
	preemptor *preemption.Preemptor
	assigner  *flavorassigner.FlavorAssigner
}

func (p *nativePlanner) Plan(ctx context.Context, asgn *flavorassigner.Assignment, _ ...assignment.PlannerOption) assignment.Plan {
	log := log.FromContext(ctx)
	cq := p.snapshot.ClusterQueue(p.wl.ClusterQueue)
	preTASMode := asgn.RepresentativeMode()

	if asgn.RepresentativeMode() != flavorassigner.NoFit {
		p.assigner.AssignTopology(ctx, log, asgn)
	}

	arm := asgn.RepresentativeMode()
	if arm != flavorassigner.Fit && p.deferForResidualTASPods(ctx, asgn, preTASMode) {
		return assignment.Plan{Assignment: asgn}
	}

	if arm == flavorassigner.Preempt {
		strategies := p.preemptor.GetPreemptionStrategyIterator(ctx, *p.wl, p.snapshot, *asgn)
		faPreemptionTargets := p.preemptor.GetTargetsWithStrategy(ctx, strategies)
		if len(faPreemptionTargets) > 0 {
			p.updateAssignmentForTAS(ctx, cq, asgn, faPreemptionTargets)
			resolveNoFit(asgn, cq)
			return assignment.Plan{Assignment: asgn, PreemptionTargets: faPreemptionTargets}
		}
	}

	p.updateAssignmentForTAS(ctx, cq, asgn, nil)
	resolveNoFit(asgn, cq)
	return assignment.Plan{Assignment: asgn, PreemptionTargets: nil}
}

func (p *nativePlanner) deferForResidualTASPods(ctx context.Context, asgn *flavorassigner.Assignment, quotaMode flavorassigner.FlavorAssignmentMode) bool {
	log := log.FromContext(ctx)
	cq := p.snapshot.ClusterQueue(p.wl.ClusterQueue)
	if !features.Enabled(features.TopologyAwareScheduling) || quotaMode == flavorassigner.NoFit ||
		(!workload.IsExplicitlyRequestingTAS(p.wl.Obj.Spec.PodSets...) && !cq.IsTASOnly()) {
		return false
	}
	tasRequests := asgn.WorkloadsTopologyRequests(log, p.wl, cq)
	if len(tasRequests) == 0 {
		return false
	}
	for _, ps := range asgn.PodSets {
		if ps.Status.IsError() {
			return false
		}
	}
	restoreResidual, found := p.snapshot.SimulateResidualTASPodRelease(ctx, log, p.wl)
	defer restoreResidual()
	if !found {
		return false
	}
	result := asgn.FindTopologyAssignments(ctx, cq, tasRequests, schdcache.WithWorkloadInfo(p.wl))
	if quotaMode != flavorassigner.Fit || result.Failure() != nil {
		// Victims can release quota at different times. Probe with the normal
		// policy selector, but only wait if no additional eviction is needed.
		strategies := p.preemptor.GetPreemptionStrategyIterator(ctx, *p.wl, p.snapshot, *asgn)
		targets := p.preemptor.GetTargetsWithStrategy(ctx, strategies)
		if len(targets) == 0 {
			return false
		}
		victims := make([]*workload.Info, 0, len(targets))
		for _, target := range targets {
			if !workloadevict.IsEvicted(target.WorkloadInfo.Obj) {
				return false
			}
			victims = append(victims, target.WorkloadInfo)
		}
		restoreUsage := p.snapshot.SimulateWorkloadRemoval(victims)
		defer restoreUsage()
		restorePods := p.snapshot.SimulatePodRemoval(ctx, log, victims)
		defer restorePods()
		result = asgn.FindTopologyAssignments(ctx, cq, tasRequests, schdcache.WithWorkloadInfo(p.wl))
	}
	if result.Failure() != nil {
		return false
	}
	for i := range asgn.PodSets {
		asgn.PodSets[i].Status = flavorassigner.Status{}
	}
	asgn.NoFitReason = ""
	asgn.UpdateForTASResult(log, cq, p.wl, result)
	asgn.SetRepresentativeMode(flavorassigner.DeferredFit)
	asgn.WaitingForResidualTASPods = true
	return true
}

func (p *nativePlanner) updateAssignmentForTAS(
	ctx context.Context,
	cq *schdcache.ClusterQueueSnapshot,
	asgn *flavorassigner.Assignment,
	targets []*preemption.Target,
) {
	log := log.FromContext(ctx)

	if features.Enabled(features.TopologyAwareScheduling) && asgn.RepresentativeMode() == flavorassigner.Preempt &&
		(workload.IsExplicitlyRequestingTAS(p.wl.Obj.Spec.PodSets...) || cq.IsTASOnly()) && !workload.HasTopologyAssignmentWithUnhealthyNode(p.wl.Obj) {
		tasRequests := asgn.WorkloadsTopologyRequests(log, p.wl, cq)
		var tasResult schdcache.TASAssignmentsResult
		log = log.WithValues("workload", klog.KRef(p.wl.Obj.Namespace, p.wl.Obj.Name))

		if len(targets) > 0 {
			var targetWorkloads []*workload.Info
			for _, target := range targets {
				targetWorkloads = append(targetWorkloads, target.WorkloadInfo)
			}
			revertUsage := p.snapshot.SimulateWorkloadRemoval(targetWorkloads)
			// Freeing the victims' quota is not enough. Until the simulator is told,
			// it still reports their Pods and their nodes still look occupied.
			revertPods := p.snapshot.SimulatePodRemoval(ctx, log, targetWorkloads)
			tasResult = asgn.FindTopologyAssignments(
				ctx,
				cq,
				tasRequests,
				schdcache.WithWorkloadInfo(p.wl),
			)
			revertPods()
			revertUsage()
		} else {
			// In this scenario we don't have any preemption candidates, yet we need
			// to reserve the TAS resources to avoid the situation when a lower
			// priority workload further in the queue gets admitted and preempted
			// in the next scheduling cycle by the waiting workload. To obtain
			// a TAS assignment for reserving the resources we run the algorithm
			// assuming the cluster is empty.
			tasResult = asgn.FindTopologyAssignments(
				ctx,
				cq,
				tasRequests,
				schdcache.WithSimulateEmpty(true),
				schdcache.WithWorkloadInfo(p.wl),
			)
		}
		asgn.UpdateForTASResult(log, cq, p.wl, tasResult)
	}
}

func resolveNoFit(asgn *flavorassigner.Assignment, cq *schdcache.ClusterQueueSnapshot) {
	if features.Enabled(features.UnadmittedWorkloadsObservability) {
		asgn.ResolveNoFitReason(cq)
	}
}
