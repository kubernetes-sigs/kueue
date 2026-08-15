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
	"iter"
	"maps"
	"sync"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/sets"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/metrics"
	"sigs.k8s.io/kueue/pkg/util/heap"
	utilqueue "sigs.k8s.io/kueue/pkg/util/queue"
	"sigs.k8s.io/kueue/pkg/util/resourcegroups"
	"sigs.k8s.io/kueue/pkg/workload"
)

type PendingWorkloads struct {
	sync.RWMutex
	customLabels *metrics.CustomLabels

	// active workloads are workloads that are ready to be admitted
	active        heap.Heap[workload.Info, workload.Reference]
	activeTracker *metrics.LabelValsTracker

	// inadmissible are workloads that have been tried at least once and couldn't be admitted.
	//
	// Invariant: a pending workload is tracked in at most one of active workloads heap,
	// inadmissible workloads list, or inflight at any time, and contributes to
	// pendingResourcesTotal exactly once while in active workloads heap or inadmissible workloads list.
	// All transitions between these places must go through the helpers next to
	// addPendingResources (pushActiveIfNotPresent, pushOrUpdateActive,
	// removeActive, insertInadmissible, removeInadmissible,
	// moveToActive, moveToInadmissible) so the accounting
	// cannot drift.
	inadmissible        inadmissibleWorkloads
	inadmissibleTracker *metrics.LabelValsTracker

	// inflight holds workloads the scheduler has popped and not yet returned.
	// While a key is here the ClusterQueue refuses to re-add it (PushOrUpdate is
	// a no-op), so the scheduler's copy is the one that comes back; updates still
	// reach the LocalQueue copy, see Get below. Fair sharing refill pops more than
	// once per ClusterQueue per cycle, hence a map.
	//
	// Invariant: every popped workload must leave, by requeue, delete, or
	// ForgetInflight. Only deleting the LocalQueue releases keys in bulk, and
	// nothing reclaims one on its own, so a leftover key silently keeps its
	// workload out of scheduling until the object is deleted.
	inflight map[workload.Reference]*workload.Info

	// schedulingHashes tracks the scheduling equivalence hashes of pending
	// workloads for the pending_scheduling_hashes metric.
	schedulingHashes *schedulingHashCounts

	// pendingResourcesTotal is the incremental sum of TotalRequests across workloads
	// in active workloads heap and inadmissible workloads list (not inflight). Updated at each mutation site so
	// pendingResources() is O(1) rather than O(N).
	// Configured resources are seeded at 0 by Update() so they appear in metrics
	// even when no workloads are pending; stale zero entries are pruned on Update().
	pendingResourcesTotal map[corev1.ResourceName]int64
}

// Get returns the workload.Info for the key, wherever it is held:
// the active heap, inadmissible list once the Workload has been tried and could not be admitted,
// or inflight while the scheduler is processing it.
//
// An update to the inflight Workload reaches the LocalQueue but not the heap, since
// PushOrUpdate deliberately drops it in favour of the newer copy the scheduler requeues at
// the end of the cycle. The inflight lookup is still worth doing, because the LocalQueue copy
// is what AddFromLocalQueue replays if the ClusterQueue is reactivated.
//
// Users of this method should not modify the returned object.
func (c *PendingWorkloads) Get(key workload.Reference) *workload.Info {
	c.RLock()
	defer c.RUnlock()

	if wInfo, ok := c.inflight[key]; ok {
		return wInfo
	}
	if wInfo := c.active.GetByKey(key); wInfo != nil {
		return wInfo
	}
	if wInfo := c.inadmissible.get(key); wInfo != nil {
		return wInfo
	}
	return nil
}

// GetActive returns the workload.Info for the key from the active workloads heap.
func (p *PendingWorkloads) GetActive(key workload.Reference) *workload.Info {
	p.RLock()
	defer p.RUnlock()
	return p.active.GetByKey(key)
}

// PopActive pops the first workload on the active workloads heap.
func (p *PendingWorkloads) PopActive() *workload.Info {
	p.Lock()
	defer p.Unlock()

	if p.active.Len() == 0 {
		// The scheduler may pop several workloads per cycle; already-inflight
		// workloads are cleared per-key on requeue/delete, so we must not
		// wipe them when a later pop finds the heap empty.
		return nil
	}

	wl := p.active.Pop()
	metrics.UntrackWorkload(p.customLabels, p.activeTracker, wl.Obj)
	p.subtractPendingResources(wl)
	wl.LastEvaluatedGeneration = wl.Obj.Generation
	p.setInflight(wl)
	return wl
}

func (p *PendingWorkloads) activeIterator() iter.Seq[*workload.Info] {
	return func(yield func(*workload.Info) bool) {
		for _, w := range p.active.List() {
			if !yield(w) {
				return
			}
		}
	}
}

// pushActiveIfNotPresent pushes wInfo onto the active workloads heap and accounts for its
// pending resources, unless the workload is already tracked in the active workloads heap, in
// the inadmissible workloads list, or as inflight. Inflight workloads are skipped
// because the scheduler owns their placement until requeue or deletion, and an
// active workloads heap copy would double-count its resources next to the inflight one.
// Returns true if the workload was pushed.
func (p *PendingWorkloads) PushActiveIfNotPresent(wInfo *workload.Info) bool {
	p.Lock()
	defer p.Unlock()

	key := workloadKey(wInfo)
	if _, ok := p.inflight[key]; ok {
		return false
	}
	if p.inadmissible.hasKey(key) {
		return false
	}
	if !p.addActive(wInfo) {
		return false
	}
	p.addPendingResources(wInfo)
	metrics.TrackWorkload(p.customLabels, p.activeTracker, wInfo.Obj)
	return true
}

// pushOrUpdateActive pushes wInfo onto the active workloads heap, replacing any previous copy.
// The old copy's resources are subtracted before the new copy's are added,
// because the requests may have changed.
func (p *PendingWorkloads) PushOrUpdateActive(wInfo *workload.Info) {
	p.Lock()
	defer p.Unlock()

	old := p.active.GetByKey(workload.Key(wInfo.Obj))
	if old != nil {
		p.subtractPendingResources(old)
		metrics.UntrackWorkload(p.customLabels, p.activeTracker, old.Obj)
	}
	p.updateActive(old, wInfo)
	p.addPendingResources(wInfo)
	metrics.TrackWorkload(p.customLabels, p.activeTracker, wInfo.Obj)
}

// removeActive removes the workload from the active workloads heap and subtracts its pending
// resources, if the active workloads heap holds it.
func (p *PendingWorkloads) RemoveActive(key workload.Reference) {
	p.Lock()
	defer p.Unlock()

	if old := p.active.GetByKey(key); old != nil {
		p.deleteActive(key, old)
		p.subtractPendingResources(old)
		metrics.UntrackWorkload(p.customLabels, p.activeTracker, old.Obj)
	}
}

// HasInflight checks if the provided reference matches an inflight workload.
func (p *PendingWorkloads) HasInflight(ref workload.Reference) bool {
	p.RLock()
	defer p.RUnlock()
	_, ok := p.inflight[ref]
	return ok
}

// ForgetInflightByKey forgets the inflight workload that matches the provided reference.
func (p *PendingWorkloads) ForgetInflightByKey(ref workload.Reference) {
	p.Lock()
	defer p.Unlock()
	p.clearInflight(ref)
}

// ForgetInflightFromLocalQueue forgets every inflight workload that belongs to
// the given LocalQueue.
func (p *PendingWorkloads) ForgetInflightFromLocalQueue(lqRef utilqueue.LocalQueueReference) {
	p.Lock()
	defer p.Unlock()
	for key, wl := range p.inflight {
		if utilqueue.KeyFromWorkload(wl.Obj) == lqRef {
			p.clearInflight(key)
		}
	}
}

// hasActive reports whether the active workloads heap holds any workload.
func (p *PendingWorkloads) hasActive() bool {
	p.RLock()
	defer p.RUnlock()
	return p.active.Len() > 0
}

func (p *PendingWorkloads) GetInadmissible(key workload.Reference) *workload.Info {
	p.RLock()
	defer p.RUnlock()
	return p.inadmissible.get(key)
}

func (p *PendingWorkloads) UpdateInadmissible(key workload.Reference, oldInfo, newInfo *workload.Info) {
	p.Lock()
	defer p.Unlock()
	p.updateInadmissible(key, oldInfo, newInfo)
	metrics.UntrackWorkload(p.customLabels, p.inadmissibleTracker, oldInfo.Obj)
	metrics.TrackWorkload(p.customLabels, p.inadmissibleTracker, newInfo.Obj)
}

func (p *PendingWorkloads) InsertInadmissible(key workload.Reference, wInfo *workload.Info) {
	p.Lock()
	defer p.Unlock()
	p.addInadmissible(key, wInfo)
	p.addPendingResources(wInfo)
	metrics.TrackWorkload(p.customLabels, p.inadmissibleTracker, wInfo.Obj)
}

func (p *PendingWorkloads) RemoveFromInadmissible(key workload.Reference, wInfo *workload.Info) {
	p.Lock()
	defer p.Unlock()
	p.deleteInadmissible(key, wInfo)
	p.subtractPendingResources(wInfo)
	metrics.UntrackWorkload(p.customLabels, p.inadmissibleTracker, wInfo.Obj)
}

// RebuildActiveHeap re-establishes the active workloads heap invariant across all entries.
func (p *PendingWorkloads) RebuildActiveHeap() {
	p.Lock()
	defer p.Unlock()
	p.active.Init()
}

func (p *PendingWorkloads) addPendingResources(wInfo *workload.Info) {
	for _, ps := range wInfo.TotalRequests {
		if ps.Requests != nil {
			ps.Requests.ForEach(func(name corev1.ResourceName, q int64) {
				p.pendingResourcesTotal[name] += q
			})
		}
	}
}

func (p *PendingWorkloads) subtractPendingResources(wInfo *workload.Info) {
	for _, ps := range wInfo.TotalRequests {
		if ps.Requests != nil {
			ps.Requests.ForEach(func(name corev1.ResourceName, q int64) {
				p.pendingResourcesTotal[name] -= q
			})
		}
	}
}

// UpdateConfiguredResources seeds pendingResourcesTotal with 0 for newly configured
// resources so they appear in metrics even when no workloads are pending, and prunes
// zero entries for resources removed from the effective resource groups.
func (p *PendingWorkloads) UpdateConfiguredResources(apiCQ *kueue.ClusterQueue) {
	p.Lock()
	defer p.Unlock()

	newConfigured := sets.New[corev1.ResourceName]()
	for _, rg := range resourcegroups.EffectiveResourceGroups(apiCQ) {
		for _, fq := range rg.Flavors {
			for _, r := range fq.Resources {
				newConfigured.Insert(r.Name)
				if _, exists := p.pendingResourcesTotal[r.Name]; !exists {
					p.pendingResourcesTotal[r.Name] = 0
				}
			}
		}
	}
	for r, v := range p.pendingResourcesTotal {
		if v == 0 && !newConfigured.Has(r) {
			delete(p.pendingResourcesTotal, r)
		}
	}
}

// MoveToActive moves a workload from the inadmissible workloads list onto the active workloads heap.
// The workload stays pending throughout, so
// pendingResourcesTotal is unchanged. The workload is pushed before the
// inadmissible entry is deleted, so a failed push (the active workloads heap unexpectedly
// already holding a copy) keeps the workload tracked as inadmissible instead
// of dropping it. Returns true if the workload was moved.
func (p *PendingWorkloads) MoveToActive(key workload.Reference, wInfo *workload.Info) bool {
	p.Lock()
	defer p.Unlock()

	if !p.moveInadmissibleToActive(key, wInfo) {
		return false
	}
	metrics.TrackWorkload(p.customLabels, p.activeTracker, wInfo.Obj)
	metrics.UntrackWorkload(p.customLabels, p.inadmissibleTracker, wInfo.Obj)
	return true
}

// MoveToInadmissible moves a workload from the active workloads heap into
// the inadmissible workloads list. The workload stays pending throughout, so
// pendingResourcesTotal is unchanged.
func (p *PendingWorkloads) MoveToInadmissible(key workload.Reference, wInfo *workload.Info) {
	p.Lock()
	defer p.Unlock()

	p.moveActiveToInadmissible(key, wInfo)
	metrics.UntrackWorkload(p.customLabels, p.activeTracker, wInfo.Obj)
	metrics.TrackWorkload(p.customLabels, p.inadmissibleTracker, wInfo.Obj)
}

// PendingResources returns the total resources requested by all pending workloads,
// aggregated by resource name. Pending workloads have not yet been assigned to flavors.
func (p *PendingWorkloads) PendingResources() map[corev1.ResourceName]int64 {
	p.RLock()
	defer p.RUnlock()

	result := maps.Clone(p.pendingResourcesTotal)
	for _, wl := range p.inflight {
		for _, ps := range wl.TotalRequests {
			if ps.Requests != nil {
				ps.Requests.ForEach(func(name corev1.ResourceName, q int64) {
					result[name] += q
				})
			}
		}
	}
	return result
}

// PendingBreakdown returns the number of active and inadmissible pending workloads.
func (p *PendingWorkloads) PendingBreakdown() (*metrics.LabelValsTracker, *metrics.LabelValsTracker) {
	p.RLock()
	defer p.RUnlock()
	return p.pendingActive(), p.pendingInadmissible()
}

// pendingActive returns the number of active pending workloads,
// workloads that are in the admission queue.
func (p *PendingWorkloads) pendingActive() *metrics.LabelValsTracker {
	result := metrics.Copy(p.activeTracker)
	for _, wl := range p.inflight {
		metrics.TrackWorkload(p.customLabels, result, wl.Obj)
	}
	return result
}

// pendingInadmissible returns the number of inadmissible pending workloads,
// workloads that were already tried and are waiting for cluster conditions
// to change to potentially become admissible.
func (p *PendingWorkloads) pendingInadmissible() *metrics.LabelValsTracker {
	return metrics.Copy(p.inadmissibleTracker)
}

// PendingActiveInLocalQueue returns the number of active pending workloads in LocalQueue,
// workloads that are in the admission queue.
func (p *PendingWorkloads) PendingActiveInLocalQueue(lqRef utilqueue.LocalQueueReference) (active int) {
	p.RLock()
	defer p.RUnlock()
	for _, wl := range p.active.List() {
		wlLqKey := utilqueue.KeyFromWorkload(wl.Obj)
		if wlLqKey == lqRef {
			active++
		}
	}
	for _, wl := range p.inflight {
		if utilqueue.KeyFromWorkload(wl.Obj) == lqRef {
			active++
		}
	}
	return
}

// PendingInadmissibleInLocalQueue returns the number of inadmissible pending workloads in LocalQueue,
// workloads that were already tried and are waiting for cluster conditions
// to change to potentially become admissible.
func (p *PendingWorkloads) PendingInadmissibleInLocalQueue(lqRef utilqueue.LocalQueueReference) (inadmissible int) {
	p.RLock()
	defer p.RUnlock()
	for _, wl := range p.inadmissible {
		wlLqKey := utilqueue.KeyFromWorkload(wl.Obj)
		if wlLqKey == lqRef {
			inadmissible++
		}
	}
	return
}

// PendingBreakdownInLocalQueue returns LabelValsTrackers for active and inadmissible
// pending workloads in the given LocalQueue, keyed by workload custom label values.
func (p *PendingWorkloads) PendingBreakdownInLocalQueue(lqRef utilqueue.LocalQueueReference) (*metrics.LabelValsTracker, *metrics.LabelValsTracker) {
	p.RLock()
	defer p.RUnlock()

	active := metrics.NewLabelValsTracker()
	for _, wl := range p.active.List() {
		if utilqueue.KeyFromWorkload(wl.Obj) == lqRef {
			metrics.TrackWorkload(p.customLabels, active, wl.Obj)
		}
	}
	for _, wl := range p.inflight {
		if utilqueue.KeyFromWorkload(wl.Obj) == lqRef {
			metrics.TrackWorkload(p.customLabels, active, wl.Obj)
		}
	}

	inadmissible := metrics.NewLabelValsTracker()
	for _, wl := range p.inadmissible {
		if utilqueue.KeyFromWorkload(wl.Obj) == lqRef {
			metrics.TrackWorkload(p.customLabels, inadmissible, wl.Obj)
		}
	}
	return active, inadmissible
}

// DumpActive produces a dump of the current active workloads of
// this ClusterQueue. It returns false if the queue is empty,
// otherwise returns true.
func (p *PendingWorkloads) DumpActive() ([]workload.Reference, bool) {
	p.RLock()
	defer p.RUnlock()

	if p.active.Len() == 0 {
		return nil, false
	}
	elements := make([]workload.Reference, p.active.Len())
	for i, info := range p.active.List() {
		elements[i] = workload.Key(info.Obj)
	}
	return elements, true
}

func (p *PendingWorkloads) DumpInadmissible() ([]workload.Reference, bool) {
	p.RLock()
	defer p.RUnlock()

	if p.inadmissible.empty() {
		return nil, false
	}
	elements := make([]workload.Reference, 0, p.inadmissible.len())
	for _, info := range p.inadmissible {
		elements = append(elements, workload.Key(info.Obj))
	}
	return elements, true
}

// DumpInflight produces a dump of the workloads this ClusterQueue has handed to
// the scheduler and not got back. It returns false if there are none.
func (p *PendingWorkloads) DumpInflight() ([]workload.Reference, bool) {
	p.RLock()
	defer p.RUnlock()

	if len(p.inflight) == 0 {
		return nil, false
	}
	elements := make([]workload.Reference, 0, len(p.inflight))
	for key := range p.inflight {
		elements = append(elements, key)
	}
	return elements, true
}

// DumpAll returns all pending workloads (active heap + inadmissible list + inflight).
// The returned order is non-deterministic; callers should sort if needed.
func (p *PendingWorkloads) DumpAll() []*workload.Info {
	p.RLock()
	defer p.RUnlock()

	totalLen := p.active.Len() + p.inadmissible.len() + len(p.inflight)
	elements := make([]*workload.Info, 0, totalLen)
	elements = append(elements, p.active.List()...)
	for _, e := range p.inadmissible {
		elements = append(elements, e)
	}
	for _, wl := range p.inflight {
		elements = append(elements, wl)
	}
	return elements
}

func (p *PendingWorkloads) clearInflight(ref workload.Reference) {
	if wInfo, ok := p.inflight[ref]; ok {
		delete(p.inflight, ref)
		p.schedulingHashes.removeInflight(wInfo)
	}
}

func (p *PendingWorkloads) setInflight(wl *workload.Info) {
	p.inflight[workloadKey(wl)] = wl
	p.schedulingHashes.moveActiveToInflight(wl)
}

func (p *PendingWorkloads) addActive(wInfo *workload.Info) bool {
	if p.active.PushIfNotPresent(wInfo) {
		p.schedulingHashes.addActive(wInfo)
		return true
	}
	return false
}

func (p *PendingWorkloads) updateActive(old, wInfo *workload.Info) {
	p.active.PushOrUpdate(wInfo)
	p.schedulingHashes.updateActive(old, wInfo)
}

func (p *PendingWorkloads) deleteActive(key workload.Reference, wInfo *workload.Info) {
	p.active.Delete(key)
	p.schedulingHashes.removeActive(wInfo)
}

func (p *PendingWorkloads) addInadmissible(key workload.Reference, wInfo *workload.Info) {
	p.inadmissible.insert(key, wInfo)
	p.schedulingHashes.addInadmissible(wInfo)
}

func (p *PendingWorkloads) updateInadmissible(key workload.Reference, oldInfo, newInfo *workload.Info) {
	p.inadmissible.insert(key, newInfo)
	p.schedulingHashes.updateInadmissible(oldInfo, newInfo)
}

func (p *PendingWorkloads) deleteInadmissible(key workload.Reference, wInfo *workload.Info) {
	p.inadmissible.delete(key)
	p.schedulingHashes.removeInadmissible(wInfo)
}

func (p *PendingWorkloads) moveInadmissibleToActive(key workload.Reference, wInfo *workload.Info) bool {
	if p.active.PushIfNotPresent(wInfo) {
		p.schedulingHashes.moveToActive(wInfo)
		p.inadmissible.delete(key)
		return true
	}
	return false
}

func (p *PendingWorkloads) moveActiveToInadmissible(key workload.Reference, wInfo *workload.Info) {
	p.active.Delete(key)
	p.schedulingHashes.moveToInadmissible(wInfo)
	p.inadmissible.insert(key, wInfo)
}
