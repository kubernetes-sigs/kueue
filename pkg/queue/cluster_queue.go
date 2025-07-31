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
	"context"
	"sort"
	"sync"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/clock"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	config "sigs.k8s.io/kueue/apis/config/v1beta1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/hierarchy"
	afs "sigs.k8s.io/kueue/pkg/util/admissionfairsharing"
	"sigs.k8s.io/kueue/pkg/util/heap"
	utilmaps "sigs.k8s.io/kueue/pkg/util/maps"
	utilpriority "sigs.k8s.io/kueue/pkg/util/priority"
	utilqueue "sigs.k8s.io/kueue/pkg/util/queue"
	"sigs.k8s.io/kueue/pkg/workload"
)

type RequeueReason string

const (
	RequeueReasonFailedAfterNomination RequeueReason = "FailedAfterNomination"
	RequeueReasonNamespaceMismatch     RequeueReason = "NamespaceMismatch"
	RequeueReasonGeneric               RequeueReason = ""
	RequeueReasonPendingPreemption     RequeueReason = "PendingPreemption"
)

var (
	realClock = clock.RealClock{}
)

type ClusterQueue struct {
	hierarchy.ClusterQueue[*cohort]
	name              kueue.ClusterQueueReference
	heap              heap.Heap[workload.Info, workload.Reference]
	namespaceSelector labels.Selector
	active            bool

	// inadmissibleWorkloads are workloads that have been tried at least once and couldn't be admitted.
	inadmissibleWorkloads map[workload.Reference]*workload.Info

	// popCycle identifies the last call to Pop. It's incremented when calling Pop.
	// popCycle and queueInadmissibleCycle are used to track when there is a requeuing
	// of inadmissible workloads while a workload is being scheduled.
	popCycle int64

	// inflight indicates the workload that was last popped by scheduler.
	inflight *workload.Info

	// queueInadmissibleCycle stores the popId at the time when
	// QueueInadmissibleWorkloads is called.
	queueInadmissibleCycle int64

	lessFunc func(a, b *workload.Info) bool

	queueingStrategy kueue.QueueingStrategy

	rwm sync.RWMutex

	clock clock.Clock

	AdmissionScope *kueue.AdmissionScope
}

func (c *ClusterQueue) GetName() kueue.ClusterQueueReference {
	return c.name
}

func workloadKey(i *workload.Info) workload.Reference {
	return workload.Key(i.Obj)
}

func newClusterQueue(ctx context.Context, client client.Client, cq *kueue.ClusterQueue, wo workload.Ordering, afsConfig *config.AdmissionFairSharing, afsEntryPenalties *utilmaps.SyncMap[utilqueue.LocalQueueReference, corev1.ResourceList]) (*ClusterQueue, error) {
	enableAdmissionFs, fsResWeights := afs.ResourceWeights(cq.Spec.AdmissionScope, afsConfig)
	cqImpl := newClusterQueueImpl(ctx, client, wo, realClock, fsResWeights, enableAdmissionFs, afsEntryPenalties)
	err := cqImpl.Update(cq)
	if err != nil {
		return nil, err
	}
	return cqImpl, nil
}

func newClusterQueueImpl(ctx context.Context, client client.Client, wo workload.Ordering, clock clock.Clock, fsResWeights map[corev1.ResourceName]float64, enableAdmissionFs bool, afsEntryPenalties *utilmaps.SyncMap[utilqueue.LocalQueueReference, corev1.ResourceList]) *ClusterQueue {
	lessFunc := queueOrderingFunc(ctx, client, wo, fsResWeights, enableAdmissionFs, afsEntryPenalties)
	return &ClusterQueue{
		heap:                   *heap.New(workloadKey, lessFunc),
		inadmissibleWorkloads:  make(map[workload.Reference]*workload.Info),
		queueInadmissibleCycle: -1,
		lessFunc:               lessFunc,
		rwm:                    sync.RWMutex{},
		clock:                  clock,
	}
}

// Update updates the properties of this ClusterQueue.
func (c *ClusterQueue) Update(apiCQ *kueue.ClusterQueue) error {
	c.rwm.Lock()
	defer c.rwm.Unlock()
	c.name = kueue.ClusterQueueReference(apiCQ.Name)
	c.queueingStrategy = apiCQ.Spec.QueueingStrategy
	nsSelector, err := metav1.LabelSelectorAsSelector(apiCQ.Spec.NamespaceSelector)
	if err != nil {
		return err
	}
	c.namespaceSelector = nsSelector
	c.active = apimeta.IsStatusConditionTrue(apiCQ.Status.Conditions, kueue.ClusterQueueActive)
	return nil
}

// AddFromLocalQueue pushes all workloads belonging to this queue to
// the ClusterQueue. If at least one workload is added, returns true,
// otherwise returns false.
func (c *ClusterQueue) AddFromLocalQueue(q *LocalQueue) bool {
	c.rwm.Lock()
	defer c.rwm.Unlock()
	added := false
	for _, info := range q.items {
		if c.heap.PushIfNotPresent(info) {
			added = true
		}
	}
	return added
}

// PushOrUpdate pushes the workload to ClusterQueue.
// If the workload is already present, updates with the new one.
func (c *ClusterQueue) PushOrUpdate(wInfo *workload.Info) {
	c.rwm.Lock()
	defer c.rwm.Unlock()
	key := workload.Key(wInfo.Obj)
	c.forgetInflightByKey(key)
	oldInfo := c.inadmissibleWorkloads[key]
	if oldInfo != nil {
		// update in place if the workload was inadmissible and didn't change
		// to potentially become admissible, unless the Eviction status changed
		// which can affect the workloads order in the queue.
		if equality.Semantic.DeepEqual(oldInfo.Obj.Spec, wInfo.Obj.Spec) &&
			equality.Semantic.DeepEqual(oldInfo.Obj.Status.ReclaimablePods, wInfo.Obj.Status.ReclaimablePods) &&
			equality.Semantic.DeepEqual(apimeta.FindStatusCondition(oldInfo.Obj.Status.Conditions, kueue.WorkloadEvicted),
				apimeta.FindStatusCondition(wInfo.Obj.Status.Conditions, kueue.WorkloadEvicted)) &&
			equality.Semantic.DeepEqual(apimeta.FindStatusCondition(oldInfo.Obj.Status.Conditions, kueue.WorkloadRequeued),
				apimeta.FindStatusCondition(wInfo.Obj.Status.Conditions, kueue.WorkloadRequeued)) {
			c.inadmissibleWorkloads[key] = wInfo
			return
		}
		// otherwise move or update in place in the queue.
		delete(c.inadmissibleWorkloads, key)
	}
	if c.heap.GetByKey(key) == nil && !c.backoffWaitingTimeExpired(wInfo) {
		c.inadmissibleWorkloads[key] = wInfo
		return
	}
	c.heap.PushOrUpdate(wInfo)
}

func (c *ClusterQueue) RebuildLocalQueue(lqName string) {
	c.rwm.Lock()
	defer c.rwm.Unlock()
	for _, wl := range c.heap.List() {
		if string(wl.Obj.Spec.QueueName) == lqName {
			c.heap.PushOrUpdate(wl)
		}
	}
}

// backoffWaitingTimeExpired returns true if the current time is after the requeueAt
// and Requeued condition not present or equal True.
func (c *ClusterQueue) backoffWaitingTimeExpired(wInfo *workload.Info) bool {
	if apimeta.IsStatusConditionFalse(wInfo.Obj.Status.Conditions, kueue.WorkloadRequeued) {
		return false
	}
	if wInfo.Obj.Status.RequeueState == nil || wInfo.Obj.Status.RequeueState.RequeueAt == nil {
		return true
	}
	// It needs to verify the requeueAt by "Equal" function
	// since the "After" function evaluates the nanoseconds despite the metav1.Time is seconds level precision.
	return c.clock.Now().After(wInfo.Obj.Status.RequeueState.RequeueAt.Time) ||
		c.clock.Now().Equal(wInfo.Obj.Status.RequeueState.RequeueAt.Time)
}

// Delete removes the workload from ClusterQueue.
func (c *ClusterQueue) Delete(w *kueue.Workload) {
	c.rwm.Lock()
	defer c.rwm.Unlock()
	c.delete(w)
}

// delete removes the workload from ClusterQueue without lock.
func (c *ClusterQueue) delete(w *kueue.Workload) {
	key := workload.Key(w)
	delete(c.inadmissibleWorkloads, key)
	c.heap.Delete(key)
	c.forgetInflightByKey(key)
}

// DeleteFromLocalQueue removes all workloads belonging to this queue from
// the ClusterQueue.
func (c *ClusterQueue) DeleteFromLocalQueue(q *LocalQueue) {
	c.rwm.Lock()
	defer c.rwm.Unlock()
	for _, w := range q.items {
		key := workload.Key(w.Obj)
		if wl := c.inadmissibleWorkloads[key]; wl != nil {
			delete(c.inadmissibleWorkloads, key)
		}
	}
	for _, w := range q.items {
		c.delete(w.Obj)
	}
}

// requeueIfNotPresent inserts a workload that cannot be admitted into
// ClusterQueue, unless it is already in the queue. If immediate is true
// or if there was a call to QueueInadmissibleWorkloads after a call to Pop,
// the workload will be pushed back to heap directly. Otherwise, the workload
// will be put into the inadmissibleWorkloads.
func (c *ClusterQueue) requeueIfNotPresent(wInfo *workload.Info, immediate bool) bool {
	c.rwm.Lock()
	defer c.rwm.Unlock()
	key := workload.Key(wInfo.Obj)
	c.forgetInflightByKey(key)
	if c.backoffWaitingTimeExpired(wInfo) &&
		(immediate || c.queueInadmissibleCycle >= c.popCycle || wInfo.LastAssignment.PendingFlavors()) {
		// If the workload was inadmissible, move it back into the queue.
		inadmissibleWl := c.inadmissibleWorkloads[key]
		if inadmissibleWl != nil {
			wInfo = inadmissibleWl
			delete(c.inadmissibleWorkloads, key)
		}
		return c.heap.PushIfNotPresent(wInfo)
	}

	if c.inadmissibleWorkloads[key] != nil {
		return false
	}

	if data := c.heap.GetByKey(key); data != nil {
		return false
	}

	c.inadmissibleWorkloads[key] = wInfo

	return true
}

func (c *ClusterQueue) forgetInflightByKey(key workload.Reference) {
	if c.inflight != nil && workload.Key(c.inflight.Obj) == key {
		c.inflight = nil
	}
}

// QueueInadmissibleWorkloads moves all workloads from inadmissibleWorkloads to heap.
// If at least one workload is moved, returns true, otherwise returns false.
func (c *ClusterQueue) QueueInadmissibleWorkloads(ctx context.Context, client client.Client) bool {
	c.rwm.Lock()
	defer c.rwm.Unlock()
	c.queueInadmissibleCycle = c.popCycle
	if len(c.inadmissibleWorkloads) == 0 {
		return false
	}

	inadmissibleWorkloads := make(map[workload.Reference]*workload.Info)
	moved := false
	for key, wInfo := range c.inadmissibleWorkloads {
		ns := corev1.Namespace{}
		err := client.Get(ctx, types.NamespacedName{Name: wInfo.Obj.Namespace}, &ns)
		if err != nil || !c.namespaceSelector.Matches(labels.Set(ns.Labels)) || !c.backoffWaitingTimeExpired(wInfo) {
			inadmissibleWorkloads[key] = wInfo
		} else {
			moved = c.heap.PushIfNotPresent(wInfo) || moved
		}
	}

	c.inadmissibleWorkloads = inadmissibleWorkloads
	return moved
}

// Pending returns the total number of pending workloads.
func (c *ClusterQueue) Pending() int {
	c.rwm.RLock()
	defer c.rwm.RUnlock()
	return c.PendingActive() + c.PendingInadmissible()
}

// PendingActive returns the number of active pending workloads,
// workloads that are in the admission queue.
func (c *ClusterQueue) PendingActive() int {
	result := c.heap.Len()
	if c.inflight != nil {
		result++
	}
	return result
}

// PendingInadmissible returns the number of inadmissible pending workloads,
// workloads that were already tried and are waiting for cluster conditions
// to change to potentially become admissible.
func (c *ClusterQueue) PendingInadmissible() int {
	return len(c.inadmissibleWorkloads)
}

// Pop removes the head of the queue and returns it. It returns nil if the
// queue is empty.
func (c *ClusterQueue) Pop() *workload.Info {
	c.rwm.Lock()
	defer c.rwm.Unlock()
	c.popCycle++
	if c.heap.Len() == 0 {
		c.inflight = nil
		return nil
	}
	c.inflight = c.heap.Pop()
	return c.inflight
}

// Dump produces a dump of the current workloads in the heap of
// this ClusterQueue. It returns false if the queue is empty,
// otherwise returns true.
func (c *ClusterQueue) Dump() ([]workload.Reference, bool) {
	c.rwm.RLock()
	defer c.rwm.RUnlock()
	if c.heap.Len() == 0 {
		return nil, false
	}
	elements := make([]workload.Reference, c.heap.Len())
	for i, info := range c.heap.List() {
		elements[i] = workload.Key(info.Obj)
	}
	return elements, true
}

func (c *ClusterQueue) DumpInadmissible() ([]workload.Reference, bool) {
	c.rwm.RLock()
	defer c.rwm.RUnlock()
	if len(c.inadmissibleWorkloads) == 0 {
		return nil, false
	}
	elements := make([]workload.Reference, 0, len(c.inadmissibleWorkloads))
	for _, info := range c.inadmissibleWorkloads {
		elements = append(elements, workload.Key(info.Obj))
	}
	return elements, true
}

// Snapshot returns a copy of the current workloads in the heap of
// this ClusterQueue.
func (c *ClusterQueue) Snapshot() []*workload.Info {
	elements := c.totalElements()
	sort.Slice(elements, func(i, j int) bool {
		return c.lessFunc(elements[i], elements[j])
	})
	return elements
}

// Info returns workload.Info for the workload key.
// Users of this method should not modify the returned object.
func (c *ClusterQueue) Info(key workload.Reference) *workload.Info {
	c.rwm.RLock()
	defer c.rwm.RUnlock()
	return c.heap.GetByKey(key)
}

func (c *ClusterQueue) totalElements() []*workload.Info {
	c.rwm.RLock()
	defer c.rwm.RUnlock()
	totalLen := c.heap.Len() + len(c.inadmissibleWorkloads)
	elements := make([]*workload.Info, 0, totalLen)
	elements = append(elements, c.heap.List()...)
	for _, e := range c.inadmissibleWorkloads {
		elements = append(elements, e)
	}
	if c.inflight != nil {
		elements = append(elements, c.inflight)
	}
	return elements
}

// Active returns true if the queue is active
func (c *ClusterQueue) Active() bool {
	c.rwm.RLock()
	defer c.rwm.RUnlock()
	return c.active
}

// RequeueIfNotPresent inserts a workload that was not
// admitted back into the ClusterQueue. If the boolean is true,
// the workloads should be put back in the queue immediately,
// because we couldn't determine if the workload was admissible
// in the last cycle. If the boolean is false, the implementation might
// choose to keep it in temporary placeholder stage where it doesn't
// compete with other workloads, until cluster events free up quota.
// The workload should not be reinserted if it's already in the ClusterQueue.
// Returns true if the workload was inserted.
func (c *ClusterQueue) RequeueIfNotPresent(wInfo *workload.Info, reason RequeueReason) bool {
	if c.queueingStrategy == kueue.StrictFIFO {
		return c.requeueIfNotPresent(wInfo, reason != RequeueReasonNamespaceMismatch)
	}
	return c.requeueIfNotPresent(wInfo, reason == RequeueReasonFailedAfterNomination || reason == RequeueReasonPendingPreemption)
}

// queueOrderingFunc returns a function used by the clusterQueue heap algorithm
// to sort workloads. The function sorts workloads based on their priority.
// When priorities are equal, it uses the workload's creation or eviction
// time.
func queueOrderingFunc(ctx context.Context, c client.Client, wo workload.Ordering, fsResWeights map[corev1.ResourceName]float64, enableAdmissionFs bool, afsEntryPenalties *utilmaps.SyncMap[utilqueue.LocalQueueReference, corev1.ResourceList]) func(a, b *workload.Info) bool {
	log := ctrl.LoggerFrom(ctx)
	return func(a, b *workload.Info) bool {
		if enableAdmissionFs {
			lqAUsage, errA := a.CalcLocalQueueFSUsage(ctx, c, fsResWeights, afsEntryPenalties)
			lqBUsage, errB := b.CalcLocalQueueFSUsage(ctx, c, fsResWeights, afsEntryPenalties)
			switch {
			case errA != nil:
				log.V(2).Error(errA, "Error determining LocalQueue usage")
			case errB != nil:
				log.V(2).Error(errB, "Error determining LocalQueue usage")
			default:
				log.V(3).Info("Resource usage from LocalQueue", "LocalQueue", a.Obj.Spec.QueueName, "Usage", lqAUsage)
				log.V(3).Info("Resource usage from LocalQueue", "LocalQueue", b.Obj.Spec.QueueName, "Usage", lqBUsage)
				if lqAUsage != lqBUsage {
					return lqAUsage < lqBUsage
				}
			}
		}
		p1 := utilpriority.Priority(a.Obj)
		p2 := utilpriority.Priority(b.Obj)

		if p1 != p2 {
			return p1 > p2
		}

		tA := wo.GetQueueOrderTimestamp(a.Obj)
		tB := wo.GetQueueOrderTimestamp(b.Obj)
		return !tB.Before(tA)
	}
}

func (c *ClusterQueue) RebuildAll() {
	c.rwm.Lock()
	defer c.rwm.Unlock()
	for _, wl := range c.heap.List() {
		c.heap.PushOrUpdate(wl)
	}
}
