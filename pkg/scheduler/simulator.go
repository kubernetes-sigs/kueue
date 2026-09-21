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

type schedulingSimulator interface {
	Schedule(
		ctx context.Context,
		initialAssignment flavorassigner.Assignment,
		preemptedTargets []*preemption.Target,
	) (assignment flavorassigner.Assignment, targets []*preemption.Target, fits bool)
}

var _ schedulingSimulator = &kueueInternalSimulator{}

type kueueInternalSimulator struct {
	wl                    *workload.Info
	snapshot              *schdcache.Snapshot
	preemptor             *preemption.Preemptor
	preemptionPlanFactory preemption.PreemptionPlanFactory
	flavorAssigner        *flavorassigner.FlavorAssigner
}

func (s *kueueInternalSimulator) Schedule(
	ctx context.Context,
	initialAssignment flavorassigner.Assignment,
	preemptedTargets []*preemption.Target,
) (assignment flavorassigner.Assignment, targets []*preemption.Target, fits bool) {
	log := log.FromContext(ctx)
	cq := s.snapshot.ClusterQueue(s.wl.ClusterQueue)
	assignment = initialAssignment

	defer func() {
		if features.Enabled(features.UnadmittedWorkloadsObservability) {
			assignment.ResolveNoFitReason(cq)
		}
		updateAssignmentForTAS(ctx, s.snapshot, cq, s.wl, &assignment, targets)
	}()

	if assignment.RepresentativeMode() != flavorassigner.NoFit {
		s.flavorAssigner.AssignTopology(ctx, log, &assignment)
	}

	arm := assignment.RepresentativeMode()
	if arm == flavorassigner.Fit {
		return assignment, preemptedTargets, true
	}

	if arm == flavorassigner.Preempt {
		preemptionPlan := s.preemptionPlanFactory(ctx, &assignment)
		faPreemptionTargets := s.preemptor.GetTargetsUsingPlan(ctx, preemptionPlan)
		if len(faPreemptionTargets) > 0 {
			targets = slices.Concat(preemptedTargets, faPreemptionTargets)
			return assignment, targets, true
		}
	}
	return
}

var _ schedulingSimulator = &kueueInternalSimulator{}

type schedulerLibrarySimulator struct {
	wl                    *workload.Info
	snapshot              *schdcache.Snapshot
	preemptor             *preemption.Preemptor
	preemptionPlanFactory preemption.PreemptionPlanFactory
}

func (s *schedulerLibrarySimulator) Schedule(
	ctx context.Context,
	initialAssignment flavorassigner.Assignment,
	preemptedTargets []*preemption.Target,
) (assignment flavorassigner.Assignment, targets []*preemption.Target, fits bool) {
	cq := s.snapshot.ClusterQueue(s.wl.ClusterQueue)
	assignment = initialAssignment

	defer func() {
		if features.Enabled(features.UnadmittedWorkloadsObservability) {
			assignment.ResolveNoFitReason(cq)
		}
	}()

	if assignment.RepresentativeMode() == flavorassigner.NoFit {
		return
	}

	// strategies := s.preemptionPlanFactory(ctx, &assignment).Materialize()
	// for _, candidates := range strategies {
	// 	schedulingResult := s.snapshot.SimulatorSnapshot.ScheduleWorklad(wl, candidates, preemptedTargets)
	// 	if schedulingResult.Fits() {
	// 		fits = true
	// 		assignment.UpdateForSchedLibTAS(schedulingResult.PodBindings)
	// 		if schedulingResult.PreemptionsRequired() {
	// 			targets = slices.Concat(preemptedTargets, schedulingResult.PreemptionTargets)
	// 		}
	// 		return
	// 	}
	// }

	return
}
