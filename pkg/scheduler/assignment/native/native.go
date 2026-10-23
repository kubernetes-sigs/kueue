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

	"github.com/go-logr/logr"
	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/log"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/scheduler/assignment"
	"sigs.k8s.io/kueue/pkg/scheduler/flavorassigner"
	"sigs.k8s.io/kueue/pkg/scheduler/preemption"
	"sigs.k8s.io/kueue/pkg/workload"
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

	if asgn.RepresentativeMode() != flavorassigner.NoFit {
		if features.Enabled(features.TopologyAwareScheduling) &&
			features.Enabled(features.FlavorFungibility) &&
			p.shouldTryNextTASFlavor(cq) {
			p.assignTopologyWithFlavorRetry(ctx, log, asgn)
		} else {
			p.assigner.AssignTopology(ctx, log, asgn)
		}
	}

	arm := asgn.RepresentativeMode()

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

func (p *nativePlanner) assignTopologyWithFlavorRetry(
	ctx context.Context,
	log logr.Logger,
	asgn *flavorassigner.Assignment,
) {
	var counts []int32
	if len(asgn.PodSets) > 0 {
		counts = make([]int32, len(asgn.PodSets))
		for i := range asgn.PodSets {
			counts[i] = asgn.PodSets[i].Count
		}
	}

	maxAttempts := 2
	if cq := p.snapshot.ClusterQueue(p.wl.ClusterQueue); cq != nil {
		flavorCount := 0
		for _, rg := range cq.ResourceGroups {
			flavorCount += len(rg.Flavors)
		}
		if flavorCount+1 > maxAttempts {
			maxAttempts = flavorCount + 1
		}
	}

	tasModes := make(flavorassigner.TASFlavorModes)
	for attempt := 0; attempt < maxAttempts; attempt++ {
		retry := p.assigner.AssignTopologyWithModes(ctx, log, asgn, tasModes)
		if !retry || asgn.RepresentativeMode() == flavorassigner.NoFit {
			return
		}
		prevAssignment := *asgn
		*asgn = p.assigner.AssignFlavorsWithTASModes(ctx, log, counts, tasModes)
		if asgn.RepresentativeMode() == flavorassigner.NoFit {
			// All flavors were TAS-downgraded to noFit. Restore the pre-retry
			// assignment and let AssignTopologyWithModes complete normally
			// (nil tasModes = no retry short-circuit) so TAS failures are
			// resolved via updateMode, leaving NoFitReason unset as the
			// scheduler expects (it resolves it after planning).
			*asgn = prevAssignment
			p.assigner.AssignTopologyWithModes(ctx, log, asgn, nil)
			return
		}
		if flavorsEqual(asgn, &prevAssignment) {
			// Flavor selection converged: no alternative flavor is available.
			// Restore the pre-retry assignment and run AssignTopologyWithModes
			// to completion (nil tasModes = no retry short-circuit) so that
			// TAS failures are resolved via updateMode rather than
			// ResolveNoFitReason, preserving the correct NoFitReason == "".
			*asgn = prevAssignment
			p.assigner.AssignTopologyWithModes(ctx, log, asgn, nil)
			return
		}
	}
	log.V(2).Info("TAS flavor retry loop reached maximum attempts", "maxAttempts", maxAttempts)
}

func flavorsEqual(a, b *flavorassigner.Assignment) bool {
	if a == nil || b == nil || len(a.PodSets) != len(b.PodSets) {
		return a == b
	}
	for i := range a.PodSets {
		psA := &a.PodSets[i]
		psB := &b.PodSets[i]
		if psA.Name != psB.Name || len(psA.Flavors) != len(psB.Flavors) {
			return false
		}
		for res, flvA := range psA.Flavors {
			flvB, ok := psB.Flavors[res]
			if !ok || flvA == nil || flvB == nil || flvA.Name != flvB.Name {
				return false
			}
		}
	}
	return true
}

func (p *nativePlanner) shouldTryNextTASFlavor(cq *schdcache.ClusterQueueSnapshot) bool {
	if cq == nil {
		return false
	}
	if cq.FlavorFungibility.WhenCanPreempt != kueue.TryNextFlavor {
		return false
	}
	if len(p.wl.NominationMapping) > 0 {
		return false
	}
	if workload.HasUnhealthyNodes(p.wl.Obj) {
		return false
	}
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
			tasResult = cq.FindTopologyAssignmentsForWorkload(
				ctx,
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
			tasResult = cq.FindTopologyAssignmentsForWorkload(
				ctx,
				tasRequests,
				schdcache.WithSimulateEmpty(true),
				schdcache.WithWorkloadInfo(p.wl),
			)
		}
		asgn.UpdateForTASResult(log, cq, p.wl, tasResult)
	}
}
