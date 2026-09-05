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

package simulation

import (
	"context"
	"fmt"
	"maps"
	"slices"

	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/cache/hierarchy"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/cache/scheduler/simulator"
	"sigs.k8s.io/kueue/pkg/workload"
)

// Simulation is a function encapsulating simulation logic.
// The body of the function is provided with a ClusterSimulator object,
// which allows performing simulation-scoped mutations on the snapshotted cluster state.
type Simulation func(*SimulationContext) error

// SimulationContext represents the snapshotted state of the cluster
// and allows mutating it in the scope of the running simulation.
// It is supplied to the Simulation by the Simulate function.
// All operations performed on the snapshot by the SimulationContext are scoped to the Simulation
// and will be reverted when the Simulate function finishes.
type SimulationContext struct {
	cacheSnapshot     *hierarchy.Manager[*schdcache.ClusterQueueSnapshot, *schdcache.CohortSnapshot]
	simulatorSnapshot simulator.SimulatorSnapshot

	simulatedPreemptions  map[workloadKey]preemption
	restoreUsageCallbacks []func()
}

type workloadKey = client.ObjectKey

type preemption struct {
	target *workload.Info
	revert func() error
}

func newSimulationContext(snapshot *schdcache.Snapshot) *SimulationContext {
	return &SimulationContext{
		cacheSnapshot:         &snapshot.Manager,
		simulatorSnapshot:     snapshot.SimulatorSnapshot,
		simulatedPreemptions:  make(map[workloadKey]preemption),
		restoreUsageCallbacks: make([]func(), 0),
	}
}

// Simulate allows running a simulation on the snapshotted state.
// The state of the snapshot is always reverted after the simulation finishes.
// Returns an error if the simulation fails or the simulation function returns an error.
// Only one simulation can be ran at the time.
func Simulate(ctx context.Context, snapshot *schdcache.Snapshot, simulate Simulation) error {
	return snapshot.SimulatorSnapshot.Simulate(ctx, func() error {
		simCtx := newSimulationContext(snapshot)
		defer simCtx.clear()
		return simulate(simCtx)
	})
}

// SimulateNested allows running a nested simulation inisde of a closure passed to Simulate.
// Returns an error if the simulation function returns an error
// or if it fails to restore the context to its original state.
func SimulateNested(parentCtx *SimulationContext, simulate Simulation) error {
	childCtx := parentCtx.childContext()
	if simErr := simulate(childCtx); simErr != nil {
		return simErr
	}
	if restoreErr := childCtx.restoreWorkloads(); restoreErr != nil {
		return restoreErr
	}
	childCtx.clear()
	return nil
}

// PreemptWorkload preempts a workload in the scope of the context.
func (s *SimulationContext) PreemptWorkload(ctx context.Context, candidate *workload.Info) error {
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

// RestoreWorkload tries to restore preempted workloads as listed.
// If no targets are provided, it will attempt to restore all preempted workloads.
// If it fails, it stops and returns an error.
func (s *SimulationContext) RestoreWorkload(target types.NamespacedName) error {
	return s.restoreWorkloads(target)
}

// RemoveUsage modifies the snapshot by removing the usage
// corresponding to the list of workloads from workloads' respective
// ClusterQueues.
func (s *SimulationContext) RemoveUsage(workloads []*workload.Info) {
	type cqUsage struct {
		cq    kueue.ClusterQueueReference
		usage workload.Usage
	}
	cqUsages := make([]cqUsage, 0, len(workloads))
	for _, w := range workloads {
		cqUsages = append(cqUsages, cqUsage{cq: w.ClusterQueue, usage: w.Usage()})
	}
	for _, cqUsage := range cqUsages {
		s.cacheSnapshot.ClusterQueue(cqUsage.cq).RemoveUsage(cqUsage.usage)
	}
	s.restoreUsageCallbacks = append(s.restoreUsageCallbacks, func() {
		for _, cqUsage := range cqUsages {
			s.cacheSnapshot.ClusterQueue(cqUsage.cq).AddUsage(cqUsage.usage)
		}
	})
}

func (s *SimulationContext) ClusterQueue(ref kueue.ClusterQueueReference) *schdcache.ClusterQueueSnapshot {
	return s.cacheSnapshot.ClusterQueue(ref)
}

// childContext returns a new context for running a nested simulation.
func (s *SimulationContext) childContext() *SimulationContext {
	return &SimulationContext{
		cacheSnapshot:         s.cacheSnapshot,
		simulatorSnapshot:     s.simulatorSnapshot,
		simulatedPreemptions:  make(map[workloadKey]preemption),
		restoreUsageCallbacks: make([]func(), 0),
	}
}

// removeWorkload removes a workload from its corresponding ClusterQueue and
// updates resource usage.
func (s *SimulationContext) removeWorkload(wl *workload.Info) {
	cq := s.cacheSnapshot.ClusterQueue(wl.ClusterQueue)
	delete(cq.Workloads, workload.Key(wl.Obj))
	cq.RemoveUsage(wl.Usage())
}

// RestoreWorkload tries to restore preempted workloads as listed.
// If no targets are provided, it will attempt to restore all preempted workloads.
// If it fails, it stops and returns an error.
func (s *SimulationContext) restoreWorkloads(targets ...types.NamespacedName) error {
	if len(targets) == 0 {
		targets = slices.Collect(maps.Keys(s.simulatedPreemptions))
	}
	for _, target := range targets {
		preemption, preempted := s.simulatedPreemptions[target]
		if !preempted {
			continue
		}
		if err := preemption.revert(); err != nil {
			return err
		}
		s.addWorkload(preemption.target)
		delete(s.simulatedPreemptions, target)
	}
	return nil
}

// addWorkload adds a workload to its corresponding ClusterQueue and
// updates resource usage.
func (s *SimulationContext) addWorkload(wl *workload.Info) {
	cq := s.cacheSnapshot.ClusterQueue(wl.ClusterQueue)
	cq.Workloads[workload.Key(wl.Obj)] = wl
	cq.AddUsage(wl.Usage())
}

func (s *SimulationContext) restoreUsage() {
	for _, restoreFn := range s.restoreUsageCallbacks {
		restoreFn()
	}
	clear(s.restoreUsageCallbacks)
}

// Clear clears the context, reverting all changes made within its scope.
func (s *SimulationContext) clear() {
	s.restoreUsage()
	for _, preemption := range s.simulatedPreemptions {
		s.addWorkload(preemption.target)
	}
	clear(s.simulatedPreemptions)
}
