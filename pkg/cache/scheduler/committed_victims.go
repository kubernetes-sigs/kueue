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
	"maps"
	"slices"

	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"

	"sigs.k8s.io/kueue/pkg/cache/scheduler/simulator"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/resources"
	"sigs.k8s.io/kueue/pkg/workload"
)

// CommittedVictims are the Workloads and Pods whose eviction is committed:
// they are going away, so a placement may count on their capacity only by
// waiting for them.
type CommittedVictims struct {
	// Workloads still hold their quota.
	Workloads map[workload.Reference]*workload.Info
	// Pods are going away while no Workload holding quota covers them.
	Pods []simulator.PodRef
	// PodsUsage is the physical usage of Pods, by node.
	PodsUsage map[string]resources.Requests
}

// AddCommittedVictims records Workloads whose eviction is committed. They keep
// their usage in the snapshot, but their Pods are removed from the scheduling
// simulator for the rest of the cycle; placement simulations get the victims
// passed instead. It is a no-op without SchedulerLibraryDeepIntegration.
func (s *Snapshot) AddCommittedVictims(ctx context.Context, victims ...*workload.Info) {
	if !features.Enabled(features.SchedulerLibraryDeepIntegration) || s.SchedulerSimulator == nil {
		return
	}
	log := ctrl.LoggerFrom(ctx)
	added := false
	for _, wl := range victims {
		key := workload.Key(wl.Obj)
		if _, found := s.committedVictims.Workloads[key]; found {
			continue
		}
		if s.committedVictims.Workloads == nil {
			s.committedVictims.Workloads = make(map[workload.Reference]*workload.Info)
		}
		s.committedVictims.Workloads[key] = wl
		added = true
		if _, err := s.SchedulerSimulator.PreemptWorkload(ctx, key); err != nil {
			// Keeping the Pods only makes the simulation stricter.
			log.V(3).Info("Could not remove a committed preemption victim from the scheduling simulator",
				"workload", klog.KObj(wl.Obj), "error", err)
		}
	}
	if added {
		s.forgetSimulatedFeasibility()
	}
}

// AddTerminatingPodsAsCommittedVictims records the residual TAS Pods being
// deleted (see TASResidualPodAccounting) as committed victims, e.g. the Pods
// of an evicted Workload which already released its quota. Like the Pods of
// the committed victims, they are removed from the scheduling simulator for
// the rest of the cycle, and passed to placement simulations instead. They
// hold no quota. It is a no-op without SchedulerLibraryDeepIntegration.
func (s *Snapshot) AddTerminatingPodsAsCommittedVictims(ctx context.Context) {
	if !features.Enabled(features.SchedulerLibraryDeepIntegration) || s.SchedulerSimulator == nil {
		return
	}
	podSimulator, ok := s.SchedulerSimulator.(simulator.PodPreemptingSimulator)
	if !ok {
		return
	}
	log := ctrl.LoggerFrom(ctx)
	for _, pod := range s.residualTASPods {
		if !pod.deleting {
			continue
		}
		ref := simulator.PodRef{Key: pod.podKey, UID: pod.podUID}
		// One by one, so that a Pod which cannot be removed does not affect the others.
		if _, err := podSimulator.PreemptPods(ctx, []simulator.PodRef{ref}); err != nil {
			// Keeping the Pod only makes the simulation stricter.
			log.V(3).Info("Could not remove a terminating Pod from the scheduling simulator", "pod", pod.podKey, "error", err)
			continue
		}
		s.committedVictims.Pods = append(s.committedVictims.Pods, ref)
		if s.committedVictims.PodsUsage == nil {
			s.committedVictims.PodsUsage = make(map[string]resources.Requests)
		}
		addResidualTASUsage(s.committedVictims.PodsUsage, pod.node, residualPodUsage(pod))
	}
	if len(s.committedVictims.Pods) > 0 {
		s.forgetSimulatedFeasibility()
	}
}

// HasCommittedVictims reports whether the snapshot has committed victims,
// Workloads or Pods.
func (s *Snapshot) HasCommittedVictims() bool {
	return len(s.committedVictims.Workloads) > 0 || len(s.committedVictims.Pods) > 0
}

// HasCommittedVictimPodsOnNode reports whether committed victim Pods run on the node.
func (s *Snapshot) HasCommittedVictimPodsOnNode(node string) bool {
	_, found := s.committedVictims.PodsUsage[node]
	return found
}

// CommittedVictimsScheduleOptions returns the options passing the committed
// victims, Workloads and Pods, to a placement simulation.
func (s *Snapshot) CommittedVictimsScheduleOptions() []simulator.ScheduleOption {
	var opts []simulator.ScheduleOption
	if len(s.committedVictims.Workloads) > 0 {
		opts = append(opts, simulator.WithCommittedPreemptionVictims(slices.Sorted(maps.Keys(s.committedVictims.Workloads))))
	}
	if len(s.committedVictims.Pods) > 0 {
		opts = append(opts, simulator.WithCommittedPreemptionPods(s.committedVictims.Pods))
	}
	return opts
}

// CommittedVictims returns the committed victims of the snapshot.
// The returned maps and slice must not be modified.
func (s *Snapshot) CommittedVictims() CommittedVictims {
	return s.committedVictims
}

// SimulateCommittedVictimsRemoval removes the committed victims from the
// snapshot, and returns a function which puts them back.
// Victims absent from the snapshot are skipped.
func (s *Snapshot) SimulateCommittedVictimsRemoval() func() {
	removed := make([]*workload.Info, 0, len(s.committedVictims.Workloads))
	for _, wl := range s.committedVictims.Workloads {
		if s.HasWorkload(wl) {
			s.RemoveWorkload(wl)
			removed = append(removed, wl)
		}
	}
	return func() {
		for _, wl := range removed {
			if !s.HasWorkload(wl) {
				s.AddWorkload(wl)
			}
		}
	}
}

// HasWorkload reports whether the Workload is present in its ClusterQueue.
func (s *Snapshot) HasWorkload(wl *workload.Info) bool {
	cq := s.ClusterQueue(wl.ClusterQueue)
	if cq == nil {
		return false
	}
	_, found := cq.Workloads[workload.Key(wl.Obj)]
	return found
}

// residualPodUsage is the physical usage charged for a residual TAS Pod.
func residualPodUsage(pod tasPodUsageValue) resources.Requests {
	usage := resources.NewRequests()
	usage.Add(pod.usage)
	usage.Add(resources.OnePodRequest)
	return usage
}
