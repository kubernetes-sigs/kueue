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

package scheduler

import (
	"context"
	"slices"

	"sigs.k8s.io/controller-runtime/pkg/log"

	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/scheduler/flavorassigner"
	"sigs.k8s.io/kueue/pkg/scheduler/preemption"
	"sigs.k8s.io/kueue/pkg/workload"
)

type schedulingSimulation interface {
	Run(
		ctx context.Context,
		initialAssignment flavorassigner.Assignment,
		preemptedTargets []*preemption.Target,
		preemptionPlanFactory preemption.PreemptionPlanFactory,
	) (assignment flavorassigner.Assignment, targets []*preemption.Target, fits bool)
}

type classicalSimulation struct {
	flvAssigner *flavorassigner.FlavorAssigner
	snap        *schdcache.Snapshot
	preemptor   *preemption.Preemptor
	wl          *workload.Info
}

func newClassicalSimulation(
	flvAssigner *flavorassigner.FlavorAssigner,
	snap *schdcache.Snapshot,
	preemptor *preemption.Preemptor,
	wl *workload.Info,
) schedulingSimulation {
	return &classicalSimulation{
		flvAssigner: flvAssigner,
		snap:        snap,
		preemptor:   preemptor,
		wl:          wl,
	}
}

func (s *classicalSimulation) Run(
	ctx context.Context,
	initialAssignment flavorassigner.Assignment,
	preemptedTargets []*preemption.Target,
	preemptionPlanFactory preemption.PreemptionPlanFactory,
) (assignment flavorassigner.Assignment, targets []*preemption.Target, fits bool) {
	log := log.FromContext(ctx)
	cq := s.snap.ClusterQueue(s.wl.ClusterQueue)

	defer func() {
		if features.Enabled(features.UnadmittedWorkloadsObservability) {
			assignment.ResolveNoFitReason(cq)
		}
		updateAssignmentForTAS(ctx, s.snap, cq, s.wl, &assignment, targets)
	}()

	assignment = initialAssignment
	if assignment.RepresentativeMode() != flavorassigner.NoFit {
		s.flvAssigner.AssignTopology(ctx, log, &assignment)
	}

	arm := assignment.RepresentativeMode()
	if arm == flavorassigner.Fit {
		return assignment, preemptedTargets, true
	}

	if arm == flavorassigner.Preempt {
		preemptionPlan := preemptionPlanFactory(ctx, &assignment)
		faPreemptionTargets := s.preemptor.GetTargetsUsingPlan(ctx, preemptionPlan)
		if len(faPreemptionTargets) > 0 {
			targets = slices.Concat(preemptedTargets, faPreemptionTargets)
			return assignment, targets, true
		}
	}
	return
}

type schedulerLibrarySimulation struct{}

func newSchedulerLibrarySimulation() schedulingSimulation {
	return &schedulerLibrarySimulation{}
}

func (s *schedulerLibrarySimulation) Run(
	ctx context.Context,
	initialAssignment flavorassigner.Assignment,
	preemptedTargets []*preemption.Target,
	preemptionPlanFactory preemption.PreemptionPlanFactory,
) (assignment flavorassigner.Assignment, targets []*preemption.Target, fits bool) {
	panic("not implemented")
}
