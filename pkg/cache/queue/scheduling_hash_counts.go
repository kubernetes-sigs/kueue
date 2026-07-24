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
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/workload"
)

// schedulingHashCounts keeps incremental per-status reference counts of the
// scheduling equivalence hashes of pending workloads, so reporting the
// pending_scheduling_hashes metric doesn't scan the pending workloads on
// every report. The paired maps are encapsulated so additions and removals
// stay consistent. ClusterQueue.rwm protects all access to this state.
type schedulingHashCounts struct {
	// active counts hashes of heap workloads; the inflight workload is folded
	// in at read time because Pop moves it out of the heap while it is still
	// being scheduled.
	active map[workload.EquivalenceHash]int
	// inadmissible counts hashes of workloads in inadmissibleWorkloads.
	inadmissible map[workload.EquivalenceHash]int
}

func newSchedulingHashCounts() *schedulingHashCounts {
	return &schedulingHashCounts{
		active:       make(map[workload.EquivalenceHash]int),
		inadmissible: make(map[workload.EquivalenceHash]int),
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
	addSchedulingHash(s.active, wInfo)
}

func (s *schedulingHashCounts) removeActive(wInfo *workload.Info) {
	removeSchedulingHash(s.active, wInfo)
}

func (s *schedulingHashCounts) addInadmissible(wInfo *workload.Info) {
	addSchedulingHash(s.inadmissible, wInfo)
}

func (s *schedulingHashCounts) removeInadmissible(wInfo *workload.Info) {
	removeSchedulingHash(s.inadmissible, wInfo)
}

// activeLen returns the number of unique active hashes, folding in the
// inflight workload's hash when it is not already counted.
func (s *schedulingHashCounts) activeLen(inflight *workload.Info) int {
	result := len(s.active)
	if inflight != nil && validSchedulingHash(inflight.SchedulingHash) && s.active[inflight.SchedulingHash] == 0 {
		result++
	}
	return result
}

// inadmissibleLen returns the number of unique inadmissible hashes.
func (s *schedulingHashCounts) inadmissibleLen() int {
	return len(s.inadmissible)
}

// pendingUnionLen returns the number of unique hashes across the active and
// inadmissible buckets, for inactive ClusterQueues which report all their
// pending workloads as inadmissible. The union is computed lazily because
// this read path is cold (stopped ClusterQueues only), while maintaining a
// dedicated union map would cost every hot-path mutation.
func (s *schedulingHashCounts) pendingUnionLen(inflight *workload.Info) int {
	result := len(s.inadmissible)
	for hash := range s.active {
		if _, ok := s.inadmissible[hash]; !ok {
			result++
		}
	}
	if inflight != nil && validSchedulingHash(inflight.SchedulingHash) &&
		s.active[inflight.SchedulingHash] == 0 && s.inadmissible[inflight.SchedulingHash] == 0 {
		result++
	}
	return result
}

// PendingSchedulingHashes returns the number of unique known scheduling hashes
// for active and inadmissible pending workloads.
func (c *ClusterQueue) PendingSchedulingHashes() (int, int) {
	if !features.Enabled(features.SchedulingEquivalenceHashing) {
		return 0, 0
	}
	c.rwm.RLock()
	defer c.rwm.RUnlock()
	return c.schedulingHashes.activeLen(c.inflight), c.schedulingHashes.inadmissibleLen()
}

// pendingSchedulingHashesForInactiveClusterQueue reports all pending hashes
// as inadmissible, mirroring how inactive ClusterQueues report their pending
// workloads.
func (c *ClusterQueue) pendingSchedulingHashesForInactiveClusterQueue() (int, int) {
	if !features.Enabled(features.SchedulingEquivalenceHashing) {
		return 0, 0
	}
	c.rwm.RLock()
	defer c.rwm.RUnlock()
	return 0, c.schedulingHashes.pendingUnionLen(c.inflight)
}
