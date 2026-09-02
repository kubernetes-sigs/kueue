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
	"fmt"

	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"sigs.k8s.io/controller-runtime/pkg/client"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/cache/hierarchy"
	"sigs.k8s.io/kueue/pkg/cache/scheduler/simulator"
	"sigs.k8s.io/kueue/pkg/workload"
)

// Simulation is a function encapsulating simulation logic.
// The body of the function is provided with a ClusterSimulator object,
// which allows performing simulation-scoped mutations on the snapshotted cluster state.
type Simulation func(*ClusterSimulator)

// ClusterSimulator is a companion object allowing performing mutations on a snapshot.
// Is is supplied to the Simulation by the Simulate function.
// All operations performed on the snapshot by the ClusterSimulator are scoped to the Simulation
// and will be reverted when the Simulate function finishes.
type ClusterSimulator struct {
	clusterState      *hierarchy.Manager[*ClusterQueueSnapshot, *CohortSnapshot]
	simulatorSnapshot simulator.SimulatorSnapshot

	simulatedPreemptions map[workloadKey]preemption
	restoreUsage         func()
}

type workloadKey = client.ObjectKey

type preemption struct {
	target *workload.Info
	revert func() error
}

func newClusterSimulator(snapshot *Snapshot) *ClusterSimulator {
	return &ClusterSimulator{
		clusterState:         &snapshot.Manager,
		simulatorSnapshot:    snapshot.SimulatorSnapshot,
		simulatedPreemptions: make(map[workloadKey]preemption),
	}
}

func Simulate(ctx context.Context, snap *Snapshot, simulate Simulation) error {
	return snap.SimulatorSnapshot.Simulate(ctx, func() {
		simulator := newClusterSimulator(snap)
		simulate(simulator)
		simulator.finalize()
	})
}

func (s *ClusterSimulator) finalize() {
	s.RestoreUsage()
	for _, preemption := range s.simulatedPreemptions {
		s.addWorkload(preemption.target)
	}
	clear(s.simulatedPreemptions)
}

func (s *ClusterSimulator) PreemptWorkload(ctx context.Context, candidate *workload.Info) error {
	wlKey := client.ObjectKeyFromObject(candidate.Obj)
	revert, err := s.simulatorSnapshot.PreemptWorkload(ctx, wlKey)
	if err != nil {
		return fmt.Errorf("failed to preempt workload %s: %w", wlKey, err)
	}
	s.removeWorkload(candidate)
	s.simulatedPreemptions[wlKey] = preemption{
		target: candidate,
		revert: revert,
	}
	return nil
}

func (s *ClusterSimulator) RestoreWorkload(target *workload.Info) error {
	wlKey := client.ObjectKeyFromObject(target.Obj)
	preemption, preempted := s.simulatedPreemptions[wlKey]
	if !preempted {
		// Nothing to do.
		return nil
	}
	if err := preemption.revert(); err != nil {
		return err
	}
	s.addWorkload(target)
	delete(s.simulatedPreemptions, wlKey)
	return nil
}

// RestoreSnapshot tries to restore snapshot. If it fails, it stops and returns an error.
func (s *ClusterSimulator) RestoreSnapshot(targets sets.Set[types.NamespacedName]) (err error) {
	reverted := []workloadKey{}
	for wlKey, preemption := range s.simulatedPreemptions {
		if !targets.Has(wlKey) {
			continue
		}
		err = preemption.revert()
		if err != nil {
			break
		}
		s.addWorkload(preemption.target)
		reverted = append(reverted, wlKey)
	}
	for _, wlKey := range reverted {
		delete(s.simulatedPreemptions, wlKey)
	}
	return
}

// RemoveUsage modifies the snapshot by removing the usage
// corresponding to the list of workloads from workloads' respective
// ClusterQueues. Subsequent calls modify the state to reflect the latest invocation.
func (s *ClusterSimulator) RemoveUsage(workloads []*workload.Info) {
	// Reset the simulated usage removal.
	s.RestoreUsage()

	type cqUsage struct {
		cq    kueue.ClusterQueueReference
		usage workload.Usage
	}
	cqUsages := make([]cqUsage, 0, len(workloads))
	for _, w := range workloads {
		cqUsages = append(cqUsages, cqUsage{cq: w.ClusterQueue, usage: w.Usage()})
	}
	for _, cqUsage := range cqUsages {
		s.clusterState.ClusterQueue(cqUsage.cq).RemoveUsage(cqUsage.usage)
	}
	s.restoreUsage = func() {
		for _, cqUsage := range cqUsages {
			s.clusterState.ClusterQueue(cqUsage.cq).AddUsage(cqUsage.usage)
		}
	}
}

// RestoreUsage restores the snapshot's usage to its iriginal state.
func (s *ClusterSimulator) RestoreUsage() {
	if s.restoreUsage != nil {
		s.restoreUsage()
		s.restoreUsage = nil
	}
}

// removeWorkload removes a workload from its corresponding ClusterQueue and
// updates resource usage.
func (s *ClusterSimulator) removeWorkload(wl *workload.Info) {
	cq := s.clusterState.ClusterQueue(wl.ClusterQueue)
	delete(cq.Workloads, workload.Key(wl.Obj))
	cq.RemoveUsage(wl.Usage())
}

// addWorkload adds a workload to its corresponding ClusterQueue and
// updates resource usage.
func (s *ClusterSimulator) addWorkload(wl *workload.Info) {
	cq := s.clusterState.ClusterQueue(wl.ClusterQueue)
	cq.Workloads[workload.Key(wl.Obj)] = wl
	cq.AddUsage(wl.Usage())
}
