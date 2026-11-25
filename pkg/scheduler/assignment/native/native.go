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
	"cmp"
	"context"
	"slices"

	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/log"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/scheduler/assignment"
	"sigs.k8s.io/kueue/pkg/scheduler/flavorassigner"
	"sigs.k8s.io/kueue/pkg/scheduler/preemption"
	"sigs.k8s.io/kueue/pkg/util/priority"
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

// Plan builds an assignment plan using kueue-native algorithms.
// As of right now, this implementation is expected to never return an Error-Plan.
func (p *nativePlanner) Plan(ctx context.Context, asgn *flavorassigner.Assignment, _ ...assignment.PlannerOption) assignment.Plan {
	log := log.FromContext(ctx)
	cq := p.snapshot.ClusterQueue(p.wl.ClusterQueue)
	preTASMode := asgn.RepresentativeMode()

	if asgn.RepresentativeMode() != flavorassigner.NoFit {
		p.assigner.AssignTopology(ctx, log, asgn)
	}

	arm := asgn.RepresentativeMode()
	if plan, applied := p.colocate(ctx, asgn); applied {
		return plan
	}
	if arm != flavorassigner.Fit && p.deferForResidualTASPods(ctx, asgn, preTASMode) {
		return assignment.Plan{Assignment: asgn}
	}

	if arm == flavorassigner.Preempt {
		strategies := p.preemptor.GetPreemptionStrategyIterator(ctx, *p.wl, p.snapshot, *asgn)
		faPreemptionTargets := p.preemptor.GetTargetsWithStrategy(ctx, strategies)
		if len(faPreemptionTargets) > 0 {
			p.updateAssignmentForTAS(ctx, cq, asgn, faPreemptionTargets)
			return assignment.Plan{Assignment: asgn, PreemptionTargets: faPreemptionTargets}
		}
	}

	p.updateAssignmentForTAS(ctx, cq, asgn, nil)
	return assignment.Plan{Assignment: asgn, PreemptionTargets: nil}
}

func (p *nativePlanner) colocate(ctx context.Context, asgn *flavorassigner.Assignment) (assignment.Plan, bool) {
	cq := p.snapshot.ClusterQueue(p.wl.ClusterQueue)
	if !p.preemptor.CanColocate(p.wl, cq) || asgn.RepresentativeMode() == flavorassigner.NoFit {
		return assignment.Plan{}, false
	}
	probe := *asgn
	probe.PodSets = slices.Clone(asgn.PodSets)
	if len(probe.PodSets) != 1 || probe.PodSets[0].Count <= 0 {
		return assignment.Plan{}, false
	}
	probe.PodSets[0].TopologyAssignment = nil
	logger := log.FromContext(ctx)
	requests := probe.WorkloadsTopologyRequests(logger, p.wl, cq)
	if len(requests) != 1 {
		return assignment.Plan{}, false
	}
	var flavor kueue.ResourceFlavorReference
	for name := range requests {
		flavor = name
	}
	for _, assigned := range probe.PodSets[0].Flavors {
		if assigned != nil && assigned.Name != flavor {
			return assignment.Plan{}, false
		}
	}
	baseline := probe.FindTopologyAssignments(ctx, cq, requests, schdcache.WithWorkloadInfo(p.wl), schdcache.WithPriorityColocation())
	psName := p.wl.Obj.Spec.PodSets[0].Name
	if ordinary := asgn.PodSets[0].TopologyAssignment; asgn.RepresentativeMode() == flavorassigner.Fit && ordinary != nil {
		scores := cq.TASFlavors[flavor]
		requested := p.wl.TotalRequests[0].Requests
		if baseline.Failure() != nil ||
			slices.Compare(scores.ColocationScores(p.wl, requested, baseline[psName].TopologyAssignment),
				scores.ColocationScores(p.wl, requested, ordinary)) <= 0 {
			baseline = schdcache.TASAssignmentsResult{psName: {TopologyAssignment: ordinary, Flavor: flavor}}
		}
	}
	if state := p.wl.ColocationPlan; state != nil {
		if state.Cancelled {
			return assignment.Plan{}, false
		}
		if state.Hash != p.wl.SchedulingHash || state.Queue != p.wl.ClusterQueue || state.Flavor != flavor {
			p.cancelColocation()
			return assignment.Plan{}, false
		}
		var remaining []*preemption.Target
		for _, queue := range p.snapshot.ClusterQueues() {
			for key, identity := range state.Victims {
				if victim := queue.Workloads[key]; victim != nil && victim.Obj.UID == identity.UID {
					if priority.EffectivePriority(logger, victim.Obj) >= priority.EffectivePriority(logger, p.wl.Obj) {
						p.cancelColocation()
						return assignment.Plan{}, false
					}
					remaining = append(remaining, &preemption.Target{
						WorkloadInfo: victim, WorkloadCq: queue,
						Reason: identity.Reason,
					})
				}
			}
		}
		slices.SortFunc(remaining, func(a, b *preemption.Target) int {
			return cmp.Compare(workload.Key(a.WorkloadInfo.Obj), workload.Key(b.WorkloadInfo.Obj))
		})
		if !p.preemptor.ColocationTargetsEligible(ctx, p.wl, p.snapshot, probe, remaining) {
			p.cancelColocation()
			return assignment.Plan{}, false
		}
		victims := make([]*workload.Info, 0, len(remaining))
		for _, target := range remaining {
			victims = append(victims, target.WorkloadInfo)
		}
		restore := p.snapshot.SimulateRemovals(ctx, logger, victims)
		restoreResidual, hasResidual := p.snapshot.SimulateResidualTASPodRelease(ctx, logger, p.wl)
		pinned := probe.FindTopologyAssignments(ctx, cq, requests, schdcache.WithWorkloadInfo(p.wl), schdcache.WithPinnedColocation(state.Assignment))
		valid := pinned.Failure() == nil && state.Matches(pinned[psName].TopologyAssignment)
		if valid && len(remaining) > 0 && baseline.Failure() == nil {
			scores := cq.TASFlavors[flavor]
			requested := p.wl.TotalRequests[0].Requests
			valid = slices.Compare(scores.ColocationScores(p.wl, requested, state.Assignment),
				scores.ColocationScores(p.wl, requested, baseline[psName].TopologyAssignment)) > 0
		}
		restoreResidual()
		restore()
		if !valid {
			p.cancelColocation()
			return assignment.Plan{}, false
		}
		probe.UpdateForTASResult(logger, cq, p.wl, pinned)
		mode := flavorassigner.Fit
		if len(remaining) > 0 {
			mode = flavorassigner.Preempt
		} else {
			switch cq.Fits(probe.Usage) {
			case schdcache.FitsCheckOk:
			case schdcache.FitsCheckNoTAS:
				if !hasResidual {
					p.cancelColocation()
					return assignment.Plan{}, false
				}
				mode = flavorassigner.DeferredFit
			default:
				p.cancelColocation()
				return assignment.Plan{}, false
			}
		}
		for i := range asgn.PodSets {
			asgn.PodSets[i].Status = flavorassigner.Status{}
		}
		asgn.NoFitReason = ""
		asgn.UpdateForTASResult(logger, cq, p.wl, pinned)
		asgn.ColocationPlan = state
		asgn.SetRepresentativeMode(mode)
		asgn.WaitingForResidualTASPods = mode == flavorassigner.DeferredFit
		return assignment.Plan{Assignment: asgn, PreemptionTargets: remaining}, true
	}
	if asgn.RepresentativeMode() != flavorassigner.Fit || baseline.Failure() != nil {
		return assignment.Plan{}, false
	}
	// A zero-victim colocation improvement is the baseline, not a reason to evict.
	asgn.UpdateForTASResult(logger, cq, p.wl, baseline)
	improved, targets, state := p.preemptor.ColocationTargets(ctx, p.wl, p.snapshot, probe, requests, baseline)
	if len(targets) > 0 {
		asgn.UpdateForTASResult(logger, cq, p.wl, improved)
		asgn.SetRepresentativeMode(flavorassigner.Preempt)
		asgn.ColocationPlan = state
	}
	return assignment.Plan{Assignment: asgn, PreemptionTargets: targets}, true
}

func (p *nativePlanner) cancelColocation() {
	state := *p.wl.ColocationPlan
	state.Cancelled = true
	p.wl.ColocationPlan = &state
}

func (p *nativePlanner) deferForResidualTASPods(ctx context.Context, asgn *flavorassigner.Assignment, quotaMode flavorassigner.FlavorAssignmentMode) bool {
	log := log.FromContext(ctx)
	cq := p.snapshot.ClusterQueue(p.wl.ClusterQueue)
	if !features.Enabled(features.TASResidualPodAccounting) || !features.Enabled(features.TopologyAwareScheduling) || quotaMode == flavorassigner.NoFit ||
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
		restore := p.snapshot.SimulateRemovals(ctx, log, victims)
		defer restore()
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
			revertRemoval := p.snapshot.SimulateRemovals(ctx, log, targetWorkloads)
			tasResult = asgn.FindTopologyAssignments(
				ctx,
				cq,
				tasRequests,
				schdcache.WithWorkloadInfo(p.wl),
			)
			revertRemoval()
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
