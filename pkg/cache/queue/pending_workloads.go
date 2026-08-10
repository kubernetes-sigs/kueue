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

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/metrics"
	"sigs.k8s.io/kueue/pkg/util/heap"
	utilqueue "sigs.k8s.io/kueue/pkg/util/queue"
	"sigs.k8s.io/kueue/pkg/workload"
)

type pendingWorkloads struct {
	customLabels *metrics.CustomLabels

	// active workloads are workloads that are ready to be admitted
	active        heap.Heap[workload.Info, workload.Reference]
	activeTracker *metrics.LabelValsTracker

	// inadmissible are workloads that have been tried at least once and couldn't be admitted.
	//
	// Invariant: a pending workload is tracked in exactly one of active workloads heap,
	// inadmissible workloads list, or inflight at any time, and contributes to
	// pendingResourcesTotal exactly once while in active workloads heap or inadmissible workloads list.
	// All transitions between these places must go through the helpers next to
	// addPendingResources (pushActiveIfNotPresent, pushOrUpdateActive,
	// removeActive, insertInadmissible, removeInadmissible,
	// moveToActive, moveToInadmissible) so the accounting
	// cannot drift.
	inadmissible        inadmissibleWorkloads
	inadmissibleTracker *metrics.LabelValsTracker

	// inflight is non-nil when a workload has been popped by the scheduler but
	// not yet requeued or deleted.
	inflight *workload.Info

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

func (p *pendingWorkloads) popActive() *workload.Info {
	if p.active.Len() == 0 {
		p.inflight = nil
		p.schedulingHashes.clearInflight()
		return nil
	}

	wl := p.active.Pop()
	metrics.UntrackWorkload(p.customLabels, p.activeTracker, wl.Obj)
	p.schedulingHashes.moveActiveToInflight(wl)
	p.subtractPendingResources(wl)
	p.inflight = wl
	p.inflight.LastEvaluatedGeneration = p.inflight.Obj.Generation
	return p.inflight
}

func (p *pendingWorkloads) getActive(key workload.Reference) *workload.Info {
	return p.active.GetByKey(key)
}

func (p *pendingWorkloads) activeIterator() iter.Seq[*workload.Info] {
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
// the inadmissible workloads list, or as inflight. The inflight workload is skipped
// because the scheduler owns its placement until requeue or deletion, and an
// active workloads heap copy would double-count its resources next to the inflight one.
// Returns true if the workload was pushed.
func (p *pendingWorkloads) pushActiveIfNotPresent(wInfo *workload.Info) bool {
	key := workloadKey(wInfo)
	if p.inflight != nil && workloadKey(p.inflight) == key {
		return false
	}
	if p.inadmissible.hasKey(key) {
		return false
	}
	if !p.active.PushIfNotPresent(wInfo) {
		return false
	}
	p.addPendingResources(wInfo)
	p.schedulingHashes.addActive(wInfo)
	metrics.TrackWorkload(p.customLabels, p.activeTracker, wInfo.Obj)
	return true
}

// pushOrUpdateActive pushes wInfo onto the active workloads heap, replacing any previous copy.
// The old copy's resources are subtracted before the new copy's are added,
// because the requests may have changed.
func (p *pendingWorkloads) pushOrUpdateActive(wInfo *workload.Info) {
	old := p.active.GetByKey(workload.Key(wInfo.Obj))
	if old != nil {
		p.subtractPendingResources(old)
		metrics.UntrackWorkload(p.customLabels, p.activeTracker, old.Obj)
	}
	p.active.PushOrUpdate(wInfo)
	p.addPendingResources(wInfo)
	p.schedulingHashes.updateActive(old, wInfo)
	metrics.TrackWorkload(p.customLabels, p.activeTracker, wInfo.Obj)
}

// removeActive removes the workload from the active workloads heap and subtracts its pending
// resources, if the active workloads heap holds it.
func (p *pendingWorkloads) removeActive(key workload.Reference) {
	if old := p.active.GetByKey(key); old != nil {
		p.active.Delete(key)
		p.subtractPendingResources(old)
		p.schedulingHashes.removeActive(old)
		metrics.UntrackWorkload(p.customLabels, p.activeTracker, old.Obj)
	}
}

func (p *pendingWorkloads) isInflight(key workload.Reference) bool {
	return p.inflight != nil && workloadKey(p.inflight) == key
}

func (p *pendingWorkloads) getInadmissible(key workload.Reference) *workload.Info {
	return p.inadmissible.get(key)
}

func (p *pendingWorkloads) updateInadmissible(key workload.Reference, oldInfo, newInfo *workload.Info) {
	p.inadmissible.insert(key, newInfo)
	p.schedulingHashes.updateInadmissible(oldInfo, newInfo)
	metrics.UntrackWorkload(p.customLabels, p.inadmissibleTracker, oldInfo.Obj)
	metrics.TrackWorkload(p.customLabels, p.inadmissibleTracker, newInfo.Obj)
}

func (p *pendingWorkloads) insertInadmissible(key workload.Reference, wInfo *workload.Info) {
	p.inadmissible.insert(key, wInfo)
	p.addPendingResources(wInfo)
	p.schedulingHashes.addInadmissible(wInfo)
	metrics.TrackWorkload(p.customLabels, p.inadmissibleTracker, wInfo.Obj)
}

func (p *pendingWorkloads) removeFromInadmissible(key workload.Reference, wInfo *workload.Info) {
	p.inadmissible.delete(key)
	p.subtractPendingResources(wInfo)
	p.schedulingHashes.removeInadmissible(wInfo)
	metrics.UntrackWorkload(p.customLabels, p.inadmissibleTracker, wInfo.Obj)
}

// rebuildAll rebuilds the active workloads heap. Must be called with lock held.
func (p *pendingWorkloads) rebuildAll() {
	for w := range p.activeIterator() {
		p.active.PushOrUpdate(w)
	}
}

func (p *pendingWorkloads) rebuildLocalQueue(lqName string) {
	for wl := range p.activeIterator() {
		if string(wl.Obj.Spec.QueueName) == lqName {
			p.active.PushOrUpdate(wl)
		}
	}
}

func (p *pendingWorkloads) addPendingResources(wInfo *workload.Info) {
	for _, ps := range wInfo.TotalRequests {
		if ps.Requests != nil {
			ps.Requests.ForEach(func(name corev1.ResourceName, q int64) {
				p.pendingResourcesTotal[name] += q
			})
		}
	}
}

func (p *pendingWorkloads) subtractPendingResources(wInfo *workload.Info) {
	for _, ps := range wInfo.TotalRequests {
		if ps.Requests != nil {
			ps.Requests.ForEach(func(name corev1.ResourceName, q int64) {
				p.pendingResourcesTotal[name] -= q
			})
		}
	}
}

// updateConfiguredResources seeds pendingResourcesTotal with 0 for newly configured
// resources so they appear in metrics even when no workloads are pending, and prunes
// zero entries for resources removed from the spec.
func (p *pendingWorkloads) updateConfiguredResources(apiCQ *kueue.ClusterQueue) {
	newConfigured := sets.New[corev1.ResourceName]()
	for _, rg := range apiCQ.Spec.ResourceGroups {
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

// moveToActive moves a workload from the inadmissible workloads list onto the active workloads heap.
// The workload stays pending throughout, so
// pendingResourcesTotal is unchanged. The workload is pushed before the
// inadmissible entry is deleted, so a failed push (the active workloads heap unexpectedly
// already holding a copy) keeps the workload tracked as inadmissible instead
// of dropping it. Returns true if the workload was moved.
func (p *pendingWorkloads) moveToActive(key workload.Reference, wInfo *workload.Info) bool {
	if !p.active.PushIfNotPresent(wInfo) {
		return false
	}
	metrics.TrackWorkload(p.customLabels, p.activeTracker, wInfo.Obj)

	p.schedulingHashes.moveToActive(wInfo)

	p.inadmissible.delete(key)
	metrics.UntrackWorkload(p.customLabels, p.inadmissibleTracker, wInfo.Obj)
	return true
}

// moveToInadmissible moves a workload from the active workloads heap into
// the inadmissible workloads list. The workload stays pending throughout, so
// pendingResourcesTotal is unchanged.
func (p *pendingWorkloads) moveToInadmissible(key workload.Reference, wInfo *workload.Info) {
	p.active.Delete(key)
	metrics.UntrackWorkload(p.customLabels, p.activeTracker, wInfo.Obj)

	p.schedulingHashes.moveToInadmissible(wInfo)

	p.inadmissible.insert(key, wInfo)
	metrics.TrackWorkload(p.customLabels, p.inadmissibleTracker, wInfo.Obj)
}

func (p *pendingWorkloads) forgetInflightByKey(key workload.Reference) {
	if p.inflight != nil && workload.Key(p.inflight.Obj) == key {
		p.inflight = nil
		p.schedulingHashes.clearInflight()
	}
}

// pendingResources returns the total resources requested by all pending workloads,
// aggregated by resource name. Pending workloads have not yet been assigned to flavors.
func (p *pendingWorkloads) pendingResources() map[corev1.ResourceName]int64 {
	result := maps.Clone(p.pendingResourcesTotal)
	if p.inflight != nil {
		for _, ps := range p.inflight.TotalRequests {
			if ps.Requests != nil {
				ps.Requests.ForEach(func(name corev1.ResourceName, q int64) {
					result[name] += q
				})
			}
		}
	}
	return result
}

// pendingActive returns the number of active pending workloads,
// workloads that are in the admission queue.
func (p *pendingWorkloads) pendingActive() *metrics.LabelValsTracker {
	result := metrics.Copy(p.activeTracker)
	if p.inflight != nil {
		metrics.TrackWorkload(p.customLabels, result, p.inflight.Obj)
	}
	return result
}

// pendingInadmissible returns the number of inadmissible pending workloads,
// workloads that were already tried and are waiting for cluster conditions
// to change to potentially become admissible.
func (p *pendingWorkloads) pendingInadmissible() *metrics.LabelValsTracker {
	return metrics.Copy(p.inadmissibleTracker)
}

// pendingBreakdown returns the number of active and inadmissible pending workloads.
func (p *pendingWorkloads) pendingBreakdown() (*metrics.LabelValsTracker, *metrics.LabelValsTracker) {
	return p.pendingActive(), p.pendingInadmissible()
}

// pendingActiveInLocalQueue returns the number of active pending workloads in LocalQueue,
// workloads that are in the admission queue.
func (p *pendingWorkloads) pendingActiveInLocalQueue(lqRef utilqueue.LocalQueueReference) (active int) {
	for _, wl := range p.active.List() {
		wlLqKey := utilqueue.KeyFromWorkload(wl.Obj)
		if wlLqKey == lqRef {
			active++
		}
	}
	if p.inflight != nil && utilqueue.KeyFromWorkload(p.inflight.Obj) == lqRef {
		active++
	}
	return
}

// pendingInadmissibleInLocalQueue returns the number of inadmissible pending workloads in LocalQueue,
// workloads that were already tried and are waiting for cluster conditions
// to change to potentially become admissible.
func (p *pendingWorkloads) pendingInadmissibleInLocalQueue(lqRef utilqueue.LocalQueueReference) (inadmissible int) {
	for _, wl := range p.inadmissible {
		wlLqKey := utilqueue.KeyFromWorkload(wl.Obj)
		if wlLqKey == lqRef {
			inadmissible++
		}
	}
	return
}

// dumpActive produces a dump of the current active workloads of
// this ClusterQueue. It returns false if the queue is empty,
// otherwise returns true.
func (p *pendingWorkloads) dumpActive() ([]workload.Reference, bool) {
	if p.active.Len() == 0 {
		return nil, false
	}
	elements := make([]workload.Reference, p.active.Len())
	for i, info := range p.active.List() {
		elements[i] = workload.Key(info.Obj)
	}
	return elements, true
}

func (p *pendingWorkloads) dumpInadmissible() ([]workload.Reference, bool) {
	if p.inadmissible.empty() {
		return nil, false
	}
	elements := make([]workload.Reference, 0, p.inadmissible.len())
	for _, info := range p.inadmissible {
		elements = append(elements, workload.Key(info.Obj))
	}
	return elements, true
}

// dumpAll returns all pending workloads (active heap + inadmissible list + inflight).
// The returned order is non-deterministic; callers should sort if needed.
func (p *pendingWorkloads) dumpAll() []*workload.Info {
	totalLen := p.active.Len() + p.inadmissible.len()
	elements := make([]*workload.Info, 0, totalLen)
	elements = append(elements, p.active.List()...)
	for _, e := range p.inadmissible {
		elements = append(elements, e)
	}
	if p.inflight != nil {
		elements = append(elements, p.inflight)
	}
	return elements
}
