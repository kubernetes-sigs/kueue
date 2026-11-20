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

	"k8s.io/klog/v2"
	ctrl "sigs.k8s.io/controller-runtime"

	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/workload"
)

// CommittedVictims are the Workloads and Pods whose eviction is committed:
// they are going away, so a placement may count on their capacity only by
// waiting for them.
type CommittedVictims struct {
	// Workloads still hold their quota.
	Workloads map[workload.Reference]*workload.Info
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
