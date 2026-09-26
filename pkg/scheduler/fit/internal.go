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

package fit

import (
	"context"

	"k8s.io/klog/v2"
	"sigs.k8s.io/controller-runtime/pkg/log"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/scheduler/flavorassigner"
	"sigs.k8s.io/kueue/pkg/scheduler/preemption"
	"sigs.k8s.io/kueue/pkg/workload"
)

// NewInternalFitFinder returns an implementation for the fit Finder
// that utilizes the internal Kueue topology-assignment and preemption logic.
// It is meant to emulate the real scheduler via internally defined heuristics.
func NewInternalFitFinder(
	wl *workload.Info,
	snapshot *schdcache.Snapshot,
	preemptor *preemption.Preemptor,
	assigner *flavorassigner.FlavorAssigner,
) Finder {
	return &internalFitFinder{wl, snapshot, preemptor, assigner}
}

var _ Finder = (*internalFitFinder)(nil)

type internalFitFinder struct {
	wl        *workload.Info
	snapshot  *schdcache.Snapshot
	preemptor *preemption.Preemptor
	assigner  *flavorassigner.FlavorAssigner
}

func (f *internalFitFinder) FindFit(ctx context.Context, assignment *flavorassigner.Assignment, _ ...option) Result {
	log := log.FromContext(ctx)
	cq := f.snapshot.ClusterQueue(f.wl.ClusterQueue)

	if assignment.RepresentativeMode() != flavorassigner.NoFit {
		f.assigner.AssignTopology(ctx, log, assignment)
	}

	arm := assignment.RepresentativeMode()

	if arm == flavorassigner.Preempt {
		strategies := f.preemptor.GetPreemptionStrategyIterator(ctx, *f.wl, f.snapshot, *assignment)
		faPreemptionTargets := f.preemptor.GetTargetsWithStrategy(ctx, strategies)
		if len(faPreemptionTargets) > 0 {
			f.updateAssignmentForTAS(ctx, cq, assignment, faPreemptionTargets)
			resolveNoFit(assignment, cq)
			return Result{assignment, faPreemptionTargets}
		}
	}

	f.updateAssignmentForTAS(ctx, cq, assignment, nil)
	resolveNoFit(assignment, cq)
	return Result{assignment, nil}
}

func (f *internalFitFinder) updateAssignmentForTAS(
	ctx context.Context,
	cq *schdcache.ClusterQueueSnapshot,
	assignment *flavorassigner.Assignment,
	targets []*preemption.Target,
) {
	log := log.FromContext(ctx)

	if features.Enabled(features.TopologyAwareScheduling) && assignment.RepresentativeMode() == flavorassigner.Preempt &&
		(workload.IsExplicitlyRequestingTAS(f.wl.Obj.Spec.PodSets...) || cq.IsTASOnly()) && !workload.HasTopologyAssignmentWithUnhealthyNode(f.wl.Obj) {
		tasRequests := assignment.WorkloadsTopologyRequests(log, f.wl, cq)
		var tasResult schdcache.TASAssignmentsResult
		log = log.WithValues("workload", klog.KRef(f.wl.Obj.Namespace, f.wl.Obj.Name))

		if len(targets) > 0 {
			var targetWorkloads []*workload.Info
			for _, target := range targets {
				targetWorkloads = append(targetWorkloads, target.WorkloadInfo)
			}
			revertUsage := f.snapshot.SimulateWorkloadRemoval(targetWorkloads)
			// Freeing the victims' quota is not enough. Until the simulator is told,
			// it still reports their Pods and their nodes still look occupied.
			revertPods := f.snapshot.SimulatePodRemoval(ctx, log, targetWorkloads)
			tasResult = cq.FindTopologyAssignmentsForWorkload(
				ctx,
				tasRequests,
				schdcache.WithWorkloadInfo(f.wl),
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
				schdcache.WithWorkloadInfo(f.wl),
			)
		}
		assignment.UpdateForTASResult(log, cq, f.wl, tasResult)
	}
}

func resolveNoFit(assignment *flavorassigner.Assignment, cq *schdcache.ClusterQueueSnapshot) {
	if features.Enabled(features.UnadmittedWorkloadsObservability) {
		assignment.ResolveNoFitReason(cq)
	}
}
