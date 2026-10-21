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

	"k8s.io/apimachinery/pkg/util/sets"

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
	// sortedWorkloads holds the CQ's workloads ordered by the comparator.
	sortedWorkloads []*workload.Info
	// workloadKeys contains the workload membership used to build sortedWorkloads.
	workloadKeys sets.Set[workload.Reference]
	// evictedCount is the length of the evicted prefix of workloads.
	evictedCount int
}

func newCandidateOrderCache() *CandidateOrderCache {
	return &CandidateOrderCache{
		entries: make(map[kueue.ClusterQueueReference]*candidateOrderEntry),
	}
}

// Get returns the CQ's cached workload ordering together with the length of its
// evicted prefix, building the entry on first use and reusing it while the
// current workloads are covered by the cached ordering.
func (c *CandidateOrderCache) Get(cq *ClusterQueueSnapshot, cmp CandidateOrderComparator) ([]*workload.Info, int) {
	if e, ok := c.entries[cq.Name]; ok && matches(e, cq) {
		return e.sortedWorkloads, e.evictedCount
	}
	e := buildCandidateOrderEntry(cq, cmp)
	c.entries[cq.Name] = e
	return e.sortedWorkloads, e.evictedCount
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
	sortedWorkloads := slices.AppendSeq(make([]*workload.Info, 0, len(cq.Workloads)), maps.Values(cq.Workloads))
	slices.SortFunc(sortedWorkloads, cmp)
	return &candidateOrderEntry{
		sortedWorkloads: sortedWorkloads,
		workloadKeys:    sets.KeySet(cq.Workloads),
		evictedCount:    evictedPrefixLen(sortedWorkloads),
	}
}

func evictedPrefixLen(sortedWorkloads []*workload.Info) int {
	// sortedWorkloads must place evicted workloads before non-evicted workloads,
	// so the evicted workloads form a prefix searched by sort.Search.
	return sort.Search(len(sortedWorkloads), func(i int) bool {
		return !workloadevict.IsEvicted(sortedWorkloads[i].Obj)
	})
}

func matches(e *candidateOrderEntry, cq *ClusterQueueSnapshot) bool {
	// Within one scheduling cycle, scheduler mutations only remove and restore
	// existing Snapshot workloads; they don't replace values or add new keys.
	// A current subset can therefore reuse the ordering of the original set.
	if len(cq.Workloads) > len(e.workloadKeys) {
		return false
	}
	for key := range cq.Workloads {
		if !e.workloadKeys.Has(key) {
			return false
		}
	}
	return true
}
