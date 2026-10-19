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

	utilqueue "sigs.k8s.io/kueue/pkg/util/queue"
	"sigs.k8s.io/kueue/pkg/workload"
)

// localQueueCounts holds the number of pending workloads per LocalQueue, so
// the LocalQueue metrics don't scan every pending workload of the
// ClusterQueue. The calls sit next to the schedulingHashCounts calls in the
// PendingWorkloads helpers.
//
// Like schedulingHashCounts, the struct carries its own lock so it stays
// consistent without relying on the callers' locks, and the metric read path
// doesn't take the ClusterQueue-wide lock.
type localQueueCounts struct {
	rwm sync.RWMutex
	// active counts workloads in the heap and inflight workloads, matching
	// the active pending status of the LocalQueue metrics.
	active map[utilqueue.LocalQueueReference]int
	// inadmissible counts workloads in inadmissibleWorkloads.
	inadmissible map[utilqueue.LocalQueueReference]int
}

func newLocalQueueCounts() *localQueueCounts {
	return &localQueueCounts{
		active:       make(map[utilqueue.LocalQueueReference]int),
		inadmissible: make(map[utilqueue.LocalQueueReference]int),
	}
}

func addLocalQueueCount(counts map[utilqueue.LocalQueueReference]int, wInfo *workload.Info) {
	counts[utilqueue.KeyFromWorkload(wInfo.Obj)]++
}

func removeLocalQueueCount(counts map[utilqueue.LocalQueueReference]int, wInfo *workload.Info) {
	lqRef := utilqueue.KeyFromWorkload(wInfo.Obj)
	counts[lqRef]--
	if counts[lqRef] <= 0 {
		delete(counts, lqRef)
	}
}

func (c *localQueueCounts) addActive(wInfo *workload.Info) {
	c.rwm.Lock()
	defer c.rwm.Unlock()
	addLocalQueueCount(c.active, wInfo)
}

func (c *localQueueCounts) removeActive(wInfo *workload.Info) {
	c.rwm.Lock()
	defer c.rwm.Unlock()
	removeLocalQueueCount(c.active, wInfo)
}

func (c *localQueueCounts) addInadmissible(wInfo *workload.Info) {
	c.rwm.Lock()
	defer c.rwm.Unlock()
	addLocalQueueCount(c.inadmissible, wInfo)
}

func (c *localQueueCounts) removeInadmissible(wInfo *workload.Info) {
	c.rwm.Lock()
	defer c.rwm.Unlock()
	removeLocalQueueCount(c.inadmissible, wInfo)
}

// updateActive replaces oldInfo with newInfo in the active counts; oldInfo may
// be nil when there was no previous heap copy. The LocalQueue of the two
// copies can differ.
func (c *localQueueCounts) updateActive(oldInfo, newInfo *workload.Info) {
	c.rwm.Lock()
	defer c.rwm.Unlock()
	if oldInfo != nil {
		removeLocalQueueCount(c.active, oldInfo)
	}
	addLocalQueueCount(c.active, newInfo)
}

// updateInadmissible replaces oldInfo with newInfo in the inadmissible counts.
func (c *localQueueCounts) updateInadmissible(oldInfo, newInfo *workload.Info) {
	c.rwm.Lock()
	defer c.rwm.Unlock()
	removeLocalQueueCount(c.inadmissible, oldInfo)
	addLocalQueueCount(c.inadmissible, newInfo)
}

func (c *localQueueCounts) moveToActive(wInfo *workload.Info) {
	c.rwm.Lock()
	defer c.rwm.Unlock()
	removeLocalQueueCount(c.inadmissible, wInfo)
	addLocalQueueCount(c.active, wInfo)
}

func (c *localQueueCounts) moveToInadmissible(wInfo *workload.Info) {
	c.rwm.Lock()
	defer c.rwm.Unlock()
	removeLocalQueueCount(c.active, wInfo)
	addLocalQueueCount(c.inadmissible, wInfo)
}

// pendingInLocalQueue returns the number of active and inadmissible pending
// workloads in the LocalQueue. Active includes inflight workloads.
func (c *localQueueCounts) pendingInLocalQueue(lqRef utilqueue.LocalQueueReference) (int, int) {
	c.rwm.RLock()
	defer c.rwm.RUnlock()
	return c.active[lqRef], c.inadmissible[lqRef]
}
