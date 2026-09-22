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

	"sigs.k8s.io/controller-runtime/pkg/log"

	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/scheduler/flavorassigner"
	"sigs.k8s.io/kueue/pkg/scheduler/preemption"
	"sigs.k8s.io/kueue/pkg/workload"
)

func simulateSchedule(
	ctx context.Context,
	simulator schedulingSimulator,
	cq *schdcache.ClusterQueueSnapshot,
	initialAssignment flavorassigner.Assignment,
) (assignment flavorassigner.Assignment, targets []*preemption.Target, fits bool) {
	assignment, targets, fits = simulator.Schedule(ctx, initialAssignment)
	if features.Enabled(features.UnadmittedWorkloadsObservability) {
		assignment.ResolveNoFitReason(cq)
	}
	return
}

type schedulingSimulator interface {
	Schedule(
		ctx context.Context,
		initialAssignment flavorassigner.Assignment,
	) (assignment flavorassigner.Assignment, targets []*preemption.Target, fits bool)
}

var _ schedulingSimulator = &kueueInternalSimulator{}

type kueueInternalSimulator struct {
	wl                          *workload.Info
	snapshot                    *schdcache.Snapshot
	preemptor                   *preemption.Preemptor
	preemptionStrategiesFactory preemption.PreemptionStrategiesFactory
	flavorAssigner              *flavorassigner.FlavorAssigner
}

func (s *kueueInternalSimulator) Schedule(
	ctx context.Context,
	initialAssignment flavorassigner.Assignment,
) (flavorassigner.Assignment, []*preemption.Target, bool) {
	log := log.FromContext(ctx)
	cq := s.snapshot.ClusterQueue(s.wl.ClusterQueue)
	assignment := initialAssignment

	if assignment.RepresentativeMode() != flavorassigner.NoFit {
		s.flavorAssigner.AssignTopology(ctx, log, &assignment)
	}

	arm := assignment.RepresentativeMode()

	if arm == flavorassigner.Preempt {
		strategies := s.preemptionStrategiesFactory(ctx, &assignment)
		faPreemptionTargets := s.preemptor.GetTargetsWithStrategy(ctx, strategies)
		if len(faPreemptionTargets) > 0 {
			updateAssignmentForTAS(ctx, s.snapshot, cq, s.wl, &assignment, faPreemptionTargets...)
			return assignment, faPreemptionTargets, true
		}
	}

	updateAssignmentForTAS(ctx, s.snapshot, cq, s.wl, &assignment)
	return assignment, nil, arm == flavorassigner.Fit

}

var _ schedulingSimulator = &kueueInternalSimulator{}

type schedulerLibrarySimulator struct {
	wl                          *workload.Info
	snapshot                    *schdcache.Snapshot
	preemptor                   *preemption.Preemptor
	preemptionStrategiesFactory preemption.PreemptionStrategiesFactory
}

func (s *schedulerLibrarySimulator) Schedule(
	ctx context.Context,
	initialAssignment flavorassigner.Assignment,
) (assignment flavorassigner.Assignment, targets []*preemption.Target, fits bool) {
	assignment = initialAssignment

	if assignment.RepresentativeMode() == flavorassigner.NoFit {
		return
	}

	// strategies := s.preemptionStrategiesFactory(ctx, &assignment).Materialize()
	// for _, candidates := range strategies {
	// 	schedulingResult := s.snapshot.SimulatorSnapshot.ScheduleWorklad(wl, candidates, s.snapshot.preemptedTargets)
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
