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

package simulator

import (
	"context"
	"iter"

	corev1 "k8s.io/api/core/v1"
)

// SchedulingSimulator acts as a factory for SimulatorSnapshots.
// It also tracks all existing Pods (even those not managed by Kueue),
// to ensure they're included in the snapshots.
// This interface is purposed to control Kueue-WAS integration.
// The "default" (non-WAS) implementation may trivialize some methods.
type SchedulingSimulator interface {
	Snapshot(ctx context.Context, nodes []*corev1.Node) (SimulatorSnapshot, error)
	// TrackPod notifies the simulator that a pod is running on a node.
	TrackPod(ctx context.Context, pod *corev1.Pod)
	// UntrackPod notifies the simulator that a pod has been removed.
	UntrackPod(ctx context.Context, key WorkloadKey)
}

// SimulatorSnapshot allows running simulations on a snapshotted cluster state.
// This interface is purposed to control Kueue-WAS integration.
// The default (non-WAS) implementation may trivialize some methods.
type SimulatorSnapshot interface {
	// Simulate executes the provided function.
	// After the simulation ends, any changes made to the snapshot state
	// via its built-in methods will be reverted.
	// The default implementation runs the method directly, as it disallows mutations on the snapshot.
	Simulate(ctx context.Context, fn func()) error
	// FindFeasibleNodes returns all candidates that can be scheduled
	// with the given requirements, based on the current state of the snapshot.
	FindFeasibleNodes(ctx context.Context, candidates iter.Seq[Candidate], requirements *PodRequirements, stats *NodeExclusionStats) ([]MatchedCandidate, error)
	// PreemptWorkload preempts the given workload, returning a function that reverts the preemption.
	// When run inside Simulate, any changes made by the method or the returned revert function
	// will be reverted regardless of their outcome (error vs success).
	// The default implementation does not perform any logic here.
	PreemptWorkload(ctx context.Context, wlKey WorkloadKey) (revert func() error, err error)
	// ScheduleWorkload finds Pod assignments and required preemptions' targets to fit the workload on the cluster.
	// It pulls preemption targets from the preemptionCandidates slice.
	// It simulates the preemptions of workloads listed in preemptedWorkloads.
	// The result is the bindings of Pods to Nodes for the calculated placement
	// and the information on which workloads need preempting to make the placement feasible.
	ScheduleWorkload(
		ctx context.Context,
		wlKey WorkloadKey,
		preemptionCandidates []WorkloadKey,
		preemptedWorkloads []WorkloadKey,
	) (SchedulingResult, PreemptionResult, error)
}

func AsCandidates[C Candidate](seq iter.Seq[C]) iter.Seq[Candidate] {
	return func(yield func(Candidate) bool) {
		for candidate := range seq {
			if !yield(candidate) {
				return
			}
		}
	}
}
