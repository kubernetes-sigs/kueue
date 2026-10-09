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
	"sort"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/util/parallelize"
	"sigs.k8s.io/kueue/pkg/workload"
	workloadevict "sigs.k8s.io/kueue/pkg/workload/evict"
)

type CandidateOrderComparator func(a, b *workload.Info) int

// CandidateOrderCache memoizes per snapshot cycle for ClusterQueue that queue's
// workloads sorted by the preemption CandidatesOrdering. One entry is reused
// across preemptors: the ordering's preemptor-dependent inputs are equal for any
// two workloads of the same CQ, so they do not affect the intra-CQ order.
type CandidateOrderCache struct {
	entries map[kueue.ClusterQueueReference]*candidateOrderEntry
}

type candidateOrderEntry struct {
	// workloads holds the CQ's workloads ordered by the comparator.
	workloads []*workload.Info
	// evictedCount is the length of the evicted prefix of workloads.
	evictedCount int
}

func newCandidateOrderCache() *CandidateOrderCache {
	return &CandidateOrderCache{
		entries: make(map[kueue.ClusterQueueReference]*candidateOrderEntry),
	}
}

// Get returns the CQ's workloads sorted by cmp together with the length of their
// evicted prefix, building the entry on first use and reusing it while membership
// is unchanged.
func (c *CandidateOrderCache) Get(cq *ClusterQueueSnapshot, cmp CandidateOrderComparator) ([]*workload.Info, int) {
	if e, ok := c.entries[cq.Name]; ok && matches(e, cq) {
		return e.workloads, e.evictedCount
	}
	e := buildCandidateOrderEntry(cq, cmp)
	c.entries[cq.Name] = e
	return e.workloads, e.evictedCount
}

// Warm builds the entries for the given ClusterQueues concurrently.
func (c *CandidateOrderCache) Warm(cqs []*ClusterQueueSnapshot, cmp CandidateOrderComparator) {
	var cqsToBuild []*ClusterQueueSnapshot
	for _, cq := range cqs {
		if e, ok := c.entries[cq.Name]; ok && matches(e, cq) {
			continue
		}
		cqsToBuild = append(cqsToBuild, cq)
	}
	if len(cqsToBuild) == 0 {
		return
	}
	entries := make([]*candidateOrderEntry, len(cqsToBuild))
	_ = parallelize.Until(context.Background(), len(cqsToBuild), func(j int) error {
		entries[j] = buildCandidateOrderEntry(cqsToBuild[j], cmp)
		return nil
	})
	for j, cq := range cqsToBuild {
		c.entries[cq.Name] = entries[j]
	}
}

func buildCandidateOrderEntry(cq *ClusterQueueSnapshot, cmp CandidateOrderComparator) *candidateOrderEntry {
	sorted := slices.AppendSeq(make([]*workload.Info, 0, len(cq.Workloads)), maps.Values(cq.Workloads))
	slices.SortFunc(sorted, cmp)
	return &candidateOrderEntry{workloads: sorted, evictedCount: evictedPrefixLen(sorted)}
}

func evictedPrefixLen(sorted []*workload.Info) int {
	return sort.Search(len(sorted), func(i int) bool {
		return !workloadevict.IsEvicted(sorted[i].Obj)
	})
}

func matches(e *candidateOrderEntry, cq *ClusterQueueSnapshot) bool {
	if len(e.workloads) != len(cq.Workloads) {
		return false
	}
	for _, wl := range e.workloads {
		if _, ok := cq.Workloads[workload.Key(wl.Obj)]; !ok {
			return false
		}
	}
	return true
}
