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

package preemption

import (
	"slices"

	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/cache/scheduler/simulator"
	"sigs.k8s.io/kueue/pkg/workload"
)

// HasPendingPreemption reports whether the preemption of the Workload was
// issued, but its eviction has not been observed yet.
func (p *Preemptor) HasPendingPreemption(wl *kueue.Workload) bool {
	return p.preemptionExpectations != nil &&
		slices.Contains(p.preemptionExpectations.ExpectedUIDs(client.ObjectKeyFromObject(wl)), wl.UID)
}

// CommittedVictimsFilter is the PreemptionFilter of the search for preemption
// targets, which runs with the committed victims removed from the snapshot (see
// Snapshot.SimulateCommittedVictimsRemoval). When scheduler-library unpreempts
// them, to check whether the Workload fits without waiting for them, the filter
// puts them back into the snapshot. Other Workloads go to the wrapped filter.
type CommittedVictimsFilter struct {
	simulator.PreemptionFilter
	snapshot *schdcache.Snapshot
}

var _ simulator.PreemptionFilter = (*CommittedVictimsFilter)(nil)

// NewCommittedVictimsFilter wraps the filter to handle the committed victims of the snapshot.
func NewCommittedVictimsFilter(snapshot *schdcache.Snapshot, filter simulator.PreemptionFilter) *CommittedVictimsFilter {
	return &CommittedVictimsFilter{PreemptionFilter: filter, snapshot: snapshot}
}

func (f *CommittedVictimsFilter) PreemptWorkload(wlKey workload.Reference) {
	if wl, found := f.snapshot.CommittedVictims().Workloads[wlKey]; found {
		if f.snapshot.HasWorkload(wl) {
			f.snapshot.RemoveWorkload(wl)
		}
		return
	}
	f.PreemptionFilter.PreemptWorkload(wlKey)
}

func (f *CommittedVictimsFilter) UnpreemptWorkload(wlKey workload.Reference) {
	if wl, found := f.snapshot.CommittedVictims().Workloads[wlKey]; found {
		if !f.snapshot.HasWorkload(wl) {
			f.snapshot.AddWorkload(wl)
		}
		return
	}
	f.PreemptionFilter.UnpreemptWorkload(wlKey)
}

// Reset removes again the committed victims left unpreempted.
// scheduler-library does not guarantee the final state of the filter
// when the Workload does not fit.
func (f *CommittedVictimsFilter) Reset() {
	for wlKey := range f.snapshot.CommittedVictims().Workloads {
		f.PreemptWorkload(wlKey)
	}
}
