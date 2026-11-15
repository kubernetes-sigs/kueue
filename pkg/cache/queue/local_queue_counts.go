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

	"k8s.io/apimachinery/pkg/types"

	"sigs.k8s.io/kueue/pkg/metrics"
	utilqueue "sigs.k8s.io/kueue/pkg/util/queue"
	"sigs.k8s.io/kueue/pkg/workload"
)

// localQueueCounts tracks the pending workloads per LocalQueue, so the
// LocalQueue metrics don't scan every pending workload of the ClusterQueue.
// Like the ClusterQueue trackers in PendingWorkloads, each LocalQueue has a
// LabelValsTracker per status, keyed by the Workload custom label values, and
// its Total is the plain count. The calls sit next to the schedulingHashCounts
// calls in the PendingWorkloads helpers.
//
// Like schedulingHashCounts, the struct carries its own lock so it stays
// consistent without relying on the callers' locks, and the metric read path
// doesn't take the ClusterQueue-wide lock.
type localQueueCounts struct {
	rwm          sync.RWMutex
	customLabels *metrics.CustomLabels
	// active tracks workloads in the heap and inflight workloads, matching
	// the active pending status of the LocalQueue metrics.
	active map[utilqueue.LocalQueueReference]*metrics.LabelValsTracker
	// inadmissible tracks workloads in inadmissibleWorkloads.
	inadmissible map[utilqueue.LocalQueueReference]*metrics.LabelValsTracker
	// activeVariants and inadmissibleVariants count the pending Variants per
	// LocalQueue and Parent UID, with the same statuses as active and
	// inadmissible.
	activeVariants       map[utilqueue.LocalQueueReference]map[types.UID]int
	inadmissibleVariants map[utilqueue.LocalQueueReference]map[types.UID]int
}

func newLocalQueueCounts(cl *metrics.CustomLabels) *localQueueCounts {
	return &localQueueCounts{
		customLabels: cl,
		active:       make(map[utilqueue.LocalQueueReference]*metrics.LabelValsTracker),
		inadmissible: make(map[utilqueue.LocalQueueReference]*metrics.LabelValsTracker),

		activeVariants:       make(map[utilqueue.LocalQueueReference]map[types.UID]int),
		inadmissibleVariants: make(map[utilqueue.LocalQueueReference]map[types.UID]int),
	}
}

func (c *localQueueCounts) track(trackers map[utilqueue.LocalQueueReference]*metrics.LabelValsTracker, variants map[utilqueue.LocalQueueReference]map[types.UID]int, wInfo *workload.Info) {
	lqRef := utilqueue.KeyFromWorkload(wInfo.Obj)
	tracker, ok := trackers[lqRef]
	if !ok {
		tracker = metrics.NewLabelValsTracker()
		trackers[lqRef] = tracker
	}
	metrics.TrackWorkload(c.customLabels, tracker, wInfo.Obj)
	if uid := variantParentUID(wInfo); uid != "" {
		if variants[lqRef] == nil {
			variants[lqRef] = make(map[types.UID]int)
		}
		variants[lqRef][uid]++
	}
}

func (c *localQueueCounts) untrack(trackers map[utilqueue.LocalQueueReference]*metrics.LabelValsTracker, variants map[utilqueue.LocalQueueReference]map[types.UID]int, wInfo *workload.Info) {
	lqRef := utilqueue.KeyFromWorkload(wInfo.Obj)
	tracker, ok := trackers[lqRef]
	if !ok {
		return
	}
	metrics.UntrackWorkload(c.customLabels, tracker, wInfo.Obj)
	if tracker.Total() == 0 {
		delete(trackers, lqRef)
	}
	if lqVariants, ok := variants[lqRef]; ok {
		removeVariant(lqVariants, wInfo)
		if len(lqVariants) == 0 {
			delete(variants, lqRef)
		}
	}
}

func (c *localQueueCounts) addActive(wInfo *workload.Info) {
	c.rwm.Lock()
	defer c.rwm.Unlock()
	c.track(c.active, c.activeVariants, wInfo)
}

func (c *localQueueCounts) removeActive(wInfo *workload.Info) {
	c.rwm.Lock()
	defer c.rwm.Unlock()
	c.untrack(c.active, c.activeVariants, wInfo)
}

func (c *localQueueCounts) addInadmissible(wInfo *workload.Info) {
	c.rwm.Lock()
	defer c.rwm.Unlock()
	c.track(c.inadmissible, c.inadmissibleVariants, wInfo)
}

func (c *localQueueCounts) removeInadmissible(wInfo *workload.Info) {
	c.rwm.Lock()
	defer c.rwm.Unlock()
	c.untrack(c.inadmissible, c.inadmissibleVariants, wInfo)
}

// updateActive replaces oldInfo with newInfo in the active counts; oldInfo may
// be nil when there was no previous heap copy. The LocalQueue and the custom
// label values of the two copies can differ.
func (c *localQueueCounts) updateActive(oldInfo, newInfo *workload.Info) {
	c.rwm.Lock()
	defer c.rwm.Unlock()
	if oldInfo != nil {
		c.untrack(c.active, c.activeVariants, oldInfo)
	}
	c.track(c.active, c.activeVariants, newInfo)
}

// updateInadmissible replaces oldInfo with newInfo in the inadmissible counts.
func (c *localQueueCounts) updateInadmissible(oldInfo, newInfo *workload.Info) {
	c.rwm.Lock()
	defer c.rwm.Unlock()
	c.untrack(c.inadmissible, c.inadmissibleVariants, oldInfo)
	c.track(c.inadmissible, c.inadmissibleVariants, newInfo)
}

func (c *localQueueCounts) moveToActive(wInfo *workload.Info) {
	c.rwm.Lock()
	defer c.rwm.Unlock()
	c.untrack(c.inadmissible, c.inadmissibleVariants, wInfo)
	c.track(c.active, c.activeVariants, wInfo)
}

func (c *localQueueCounts) moveToInadmissible(wInfo *workload.Info) {
	c.rwm.Lock()
	defer c.rwm.Unlock()
	c.untrack(c.active, c.activeVariants, wInfo)
	c.track(c.inadmissible, c.inadmissibleVariants, wInfo)
}

// pendingInLocalQueue returns the number of active and inadmissible pending
// workloads in the LocalQueue, counting each Parent once. Active includes
// inflight workloads.
func (c *localQueueCounts) pendingInLocalQueue(lqRef utilqueue.LocalQueueReference) (int, int) {
	c.rwm.RLock()
	defer c.rwm.RUnlock()
	var active, inadmissible int
	if tracker, ok := c.active[lqRef]; ok {
		active = tracker.Total()
	}
	if tracker, ok := c.inadmissible[lqRef]; ok {
		inadmissible = tracker.Total()
	}
	// The active counts already include the inflight Variants.
	activeExcess, inadmissibleExcess := variantExcess(c.activeVariants[lqRef], c.inadmissibleVariants[lqRef], nil)
	return active - activeExcess, inadmissible - inadmissibleExcess
}

// pendingBreakdownInLocalQueue returns copies of the active and inadmissible
// trackers of the LocalQueue. Active includes inflight workloads.
func (c *localQueueCounts) pendingBreakdownInLocalQueue(lqRef utilqueue.LocalQueueReference) (*metrics.LabelValsTracker, *metrics.LabelValsTracker) {
	c.rwm.RLock()
	defer c.rwm.RUnlock()
	return copyNonZero(c.active[lqRef]), copyNonZero(c.inadmissible[lqRef])
}

// copyNonZero copies the tracker without the label values whose count dropped
// to zero, so the result matches a fresh count of the pending workloads.
func copyNonZero(tracker *metrics.LabelValsTracker) *metrics.LabelValsTracker {
	result := metrics.NewLabelValsTracker()
	if tracker == nil {
		return result
	}
	for labelVals, count := range tracker.Iter() {
		if count > 0 {
			result.Add(labelVals, count)
		}
	}
	return result
}
