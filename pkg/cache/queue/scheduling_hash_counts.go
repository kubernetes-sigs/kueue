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

package queue

import (
	"sync"

	"sigs.k8s.io/kueue/pkg/workload"
)

// schedulingHashCounts keeps incremental per-status reference counts of the
// scheduling equivalence hashes of pending workloads, so reporting the
// pending_scheduling_hashes metric doesn't scan the pending workloads on
// every report. The paired maps are encapsulated so additions and removals
// stay consistent, and bucket transitions go through the move/update helpers
// so both sides change in a single critical section.
//
// The struct carries its own lock so the metric read paths don't take the
// ClusterQueue-wide lock. Mutations are additionally serialized by their
// callers holding ClusterQueue.rwm for writing; readers may observe the state
// between two helper calls made under ClusterQueue.rwm, which is an
// acceptable momentary skew for a metric.
type schedulingHashCounts struct {
	rwm sync.RWMutex
	// active counts hashes of heap workloads.
	active map[workload.EquivalenceHash]int
	// inadmissible counts hashes of workloads in inadmissibleWorkloads.
	inadmissible map[workload.EquivalenceHash]int
	// inflight counts the hashes of the workloads in PendingWorkloads.inflight.
	// They are folded into the counts at read time because Pop moves a
	// workload out of the heap while it is still being scheduled. It is a
	// count map rather than a single hash because fair sharing refill lets
	// the scheduler hold more than one workload inflight per ClusterQueue.
	inflight map[workload.EquivalenceHash]int
}

func newSchedulingHashCounts() *schedulingHashCounts {
	return &schedulingHashCounts{
		active:       make(map[workload.EquivalenceHash]int),
		inadmissible: make(map[workload.EquivalenceHash]int),
		inflight:     make(map[workload.EquivalenceHash]int),
	}
}

func validSchedulingHash(hash workload.EquivalenceHash) bool {
	return hash != "" && hash != workload.SchedulingHashUnknown
}

func addSchedulingHash(counts map[workload.EquivalenceHash]int, wInfo *workload.Info) {
	if validSchedulingHash(wInfo.SchedulingHash) {
		counts[wInfo.SchedulingHash]++
	}
}

func removeSchedulingHash(counts map[workload.EquivalenceHash]int, wInfo *workload.Info) {
	if !validSchedulingHash(wInfo.SchedulingHash) {
		return
	}
	hash := wInfo.SchedulingHash
	counts[hash]--
	if counts[hash] <= 0 {
		delete(counts, hash)
	}
}

func (s *schedulingHashCounts) addActive(wInfo *workload.Info) {
	s.rwm.Lock()
	defer s.rwm.Unlock()
	addSchedulingHash(s.active, wInfo)
}

func (s *schedulingHashCounts) removeActive(wInfo *workload.Info) {
	s.rwm.Lock()
	defer s.rwm.Unlock()
	removeSchedulingHash(s.active, wInfo)
}

func (s *schedulingHashCounts) addInadmissible(wInfo *workload.Info) {
	s.rwm.Lock()
	defer s.rwm.Unlock()
	addSchedulingHash(s.inadmissible, wInfo)
}

func (s *schedulingHashCounts) removeInadmissible(wInfo *workload.Info) {
	s.rwm.Lock()
	defer s.rwm.Unlock()
	removeSchedulingHash(s.inadmissible, wInfo)
}

// updateActive replaces oldInfo's hash with newInfo's in the active bucket;
// oldInfo may be nil when there was no previous heap copy.
func (s *schedulingHashCounts) updateActive(oldInfo, newInfo *workload.Info) {
	s.rwm.Lock()
	defer s.rwm.Unlock()
	if oldInfo != nil {
		removeSchedulingHash(s.active, oldInfo)
	}
	addSchedulingHash(s.active, newInfo)
}

// updateInadmissible replaces oldInfo's hash with newInfo's in the
// inadmissible bucket.
func (s *schedulingHashCounts) updateInadmissible(oldInfo, newInfo *workload.Info) {
	s.rwm.Lock()
	defer s.rwm.Unlock()
	removeSchedulingHash(s.inadmissible, oldInfo)
	addSchedulingHash(s.inadmissible, newInfo)
}

// moveToActive moves wInfo's hash from the inadmissible to the active bucket.
func (s *schedulingHashCounts) moveToActive(wInfo *workload.Info) {
	s.rwm.Lock()
	defer s.rwm.Unlock()
	removeSchedulingHash(s.inadmissible, wInfo)
	addSchedulingHash(s.active, wInfo)
}

// moveToInadmissible moves wInfo's hash from the active to the inadmissible
// bucket.
func (s *schedulingHashCounts) moveToInadmissible(wInfo *workload.Info) {
	s.rwm.Lock()
	defer s.rwm.Unlock()
	removeSchedulingHash(s.active, wInfo)
	addSchedulingHash(s.inadmissible, wInfo)
}

// moveActiveToInflight removes wInfo's hash from the active bucket and adds it
// to the inflight bucket, mirroring Pop handing the workload to the scheduler.
func (s *schedulingHashCounts) moveActiveToInflight(wInfo *workload.Info) {
	s.rwm.Lock()
	defer s.rwm.Unlock()
	removeSchedulingHash(s.active, wInfo)
	addSchedulingHash(s.inflight, wInfo)
}

// removeInflight forgets wInfo's inflight hash, mirroring its entry in
// PendingWorkloads.inflight being cleared.
func (s *schedulingHashCounts) removeInflight(wInfo *workload.Info) {
	s.rwm.Lock()
	defer s.rwm.Unlock()
	removeSchedulingHash(s.inflight, wInfo)
}

// pendingLens returns the number of unique active and inadmissible hashes,
// folding the inflight hashes into the active count when they are not already
// counted there.
func (s *schedulingHashCounts) pendingLens() (int, int) {
	s.rwm.RLock()
	defer s.rwm.RUnlock()
	active := len(s.active)
	for hash := range s.inflight {
		if s.active[hash] == 0 {
			active++
		}
	}
	return active, len(s.inadmissible)
}

// pendingUnionLen returns the number of unique hashes across the active,
// inflight, and inadmissible buckets, for inactive ClusterQueues which report
// all their pending workloads as inadmissible. The union is computed lazily
// because this read path is cold (stopped ClusterQueues only), while
// maintaining a dedicated union map would cost every hot-path mutation.
func (s *schedulingHashCounts) pendingUnionLen() int {
	s.rwm.RLock()
	defer s.rwm.RUnlock()
	result := len(s.inadmissible)
	for hash := range s.active {
		if _, ok := s.inadmissible[hash]; !ok {
			result++
		}
	}
	for hash := range s.inflight {
		if s.active[hash] == 0 && s.inadmissible[hash] == 0 {
			result++
		}
	}
	return result
}

// PendingSchedulingHashes returns the number of unique known scheduling hashes
// for active and inadmissible pending workloads. It only takes the
// schedulingHashCounts lock, so metric reports don't contend on the
// ClusterQueue-wide lock. When SchedulingEquivalenceHashing is disabled the
// counts are naturally empty, because hashes are never recorded.
func (c *ClusterQueue) PendingSchedulingHashes() (int, int) {
	return c.workloads.schedulingHashes.pendingLens()
}

// pendingSchedulingHashesForInactiveClusterQueue reports all pending hashes
// as inadmissible, mirroring how inactive ClusterQueues report their pending
// workloads.
func (c *ClusterQueue) pendingSchedulingHashesForInactiveClusterQueue() (int, int) {
	return 0, c.workloads.schedulingHashes.pendingUnionLen()
}
