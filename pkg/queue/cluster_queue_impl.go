/*
Copyright 2022 The Kubernetes Authors.

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
	"k8s.io/apimachinery/pkg/util/sets"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/util/heap"
	"sigs.k8s.io/kueue/pkg/workload"
)

// clusterQueueBase is an incomplete base implementation of ClusterQueue
// interface. It can be inherited and overwritten by other types.
type clusterQueueBase struct {
	heap              heap.Heap
	cohort            string
	namespaceSelector labels.Selector

	// inadmissibleWorkloads are workloads that have been tried at least once and couldn't be admitted.
	inadmissibleWorkloads map[string]*workload.Info

	// popCycle identifies the last call to Pop. It's incremented when calling Pop.
	// popCycle and queueInadmissibleCycle are used to track when there is a requeueing
	// of inadmissible workloads while a workload is being scheduled.
	popCycle int64

	// queueInadmissibleCycle stores the popId at the time when
	// QueueInadmissibleWorkloads is called.
	queueInadmissibleCycle int64

	rwm sync.RWMutex
}

func newClusterQueueImpl(keyFunc func(obj interface{}) string, lessFunc func(a, b interface{}) bool) *clusterQueueBase {
	return &clusterQueueBase{
		heap:                   heap.New(keyFunc, lessFunc),
		inadmissibleWorkloads:  make(map[string]*workload.Info),
		queueInadmissibleCycle: -1,
		rwm:                    sync.RWMutex{},
	}
}

func (c *clusterQueueBase) Update(apiCQ *kueue.ClusterQueue) error {
	c.cohort = apiCQ.Spec.Cohort
	nsSelector, err := metav1.LabelSelectorAsSelector(apiCQ.Spec.NamespaceSelector)
	if err != nil {
		return err
	}
	c.namespaceSelector = nsSelector
	return nil
}

func (c *clusterQueueBase) Cohort() string {
	return c.cohort
}

func (c *clusterQueueBase) AddFromLocalQueue(q *LocalQueue) bool {
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

func (c *clusterQueueBase) PushOrUpdate(wInfo *workload.Info) {
	c.rwm.Lock()
	defer c.rwm.Unlock()
	key := workload.Key(wInfo.Obj)
	oldInfo := c.inadmissibleWorkloads[key]
	if oldInfo != nil {
		// update in place if the workload was inadmissible and didn't change
		// to potentially become admissible, unless the Eviction status changed
		// which can affect the workloads order in the queue.
		if equality.Semantic.DeepEqual(oldInfo.Obj.Spec, wInfo.Obj.Spec) &&
			equality.Semantic.DeepEqual(apimeta.FindStatusCondition(oldInfo.Obj.Status.Conditions, kueue.WorkloadEvicted),
				apimeta.FindStatusCondition(wInfo.Obj.Status.Conditions, kueue.WorkloadEvicted)) {
			c.inadmissibleWorkloads[key] = wInfo
			return
		}
		// otherwise move or update in place in the queue.
		delete(c.inadmissibleWorkloads, key)
	}
	c.heap.PushOrUpdate(wInfo)
}

func (c *clusterQueueBase) Delete(w *kueue.Workload) {
	key := workload.Key(w)
	delete(c.inadmissibleWorkloads, key)
	c.heap.Delete(key)
}

func (c *clusterQueueBase) DeleteFromLocalQueue(q *LocalQueue) {
	c.rwm.Lock()
	defer c.rwm.Unlock()
	for _, w := range q.items {
		key := workload.Key(w.Obj)
		if wl := c.inadmissibleWorkloads[key]; wl != nil {
			delete(c.inadmissibleWorkloads, key)
		}
	}
	for _, w := range q.items {
		c.Delete(w.Obj)
	}
}

// requeueIfNotPresent inserts a workload that cannot be admitted into
// ClusterQueue, unless it is already in the queue. If immediate is true
// or if there was a call to QueueInadmissibleWorkloads after a call to Pop,
// the workload will be pushed back to heap directly. Otherwise, the workload
// will be put into the inadmissibleWorkloads.
func (c *clusterQueueBase) requeueIfNotPresent(wInfo *workload.Info, immediate bool) bool {
	c.rwm.Lock()
	defer c.rwm.Unlock()
	key := workload.Key(wInfo.Obj)
	if immediate || c.queueInadmissibleCycle >= c.popCycle || wInfo.LastAssignment.PendingFlavors() {
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

// QueueInadmissibleWorkloads moves all workloads from inadmissibleWorkloads to heap.
// If at least one workload is moved, returns true, otherwise returns false.
func (c *clusterQueueBase) QueueInadmissibleWorkloads(ctx context.Context, client client.Client) bool {
	c.rwm.Lock()
	defer c.rwm.Unlock()
	c.queueInadmissibleCycle = c.popCycle
	if len(c.inadmissibleWorkloads) == 0 {
		return false
	}

	inadmissibleWorkloads := make(map[string]*workload.Info)
	moved := false
	for key, wInfo := range c.inadmissibleWorkloads {
		ns := corev1.Namespace{}
		err := client.Get(ctx, types.NamespacedName{Name: wInfo.Obj.Namespace}, &ns)
		if err != nil || !c.namespaceSelector.Matches(labels.Set(ns.Labels)) {
			inadmissibleWorkloads[key] = wInfo
		} else {
			moved = c.heap.PushIfNotPresent(wInfo) || moved
		}
	}

	c.inadmissibleWorkloads = inadmissibleWorkloads
	return moved
}

func (c *clusterQueueBase) Pending() int {
	c.rwm.RLock()
	defer c.rwm.RUnlock()
	return c.PendingActive() + c.PendingInadmissible()
}

func (c *clusterQueueBase) PendingActive() int {
	return c.heap.Len()
}

func (c *clusterQueueBase) PendingInadmissible() int {
	return len(c.inadmissibleWorkloads)
}

func (c *clusterQueueBase) Pop() *workload.Info {
	c.rwm.Lock()
	defer c.rwm.Unlock()
	c.popCycle++
	if c.heap.Len() == 0 {
		return nil
	}

	info := c.heap.Pop()
	return info.(*workload.Info)
}

func (c *clusterQueueBase) Dump() (sets.Set[string], bool) {
	c.rwm.RLock()
	defer c.rwm.RUnlock()
	if c.heap.Len() == 0 {
		return nil, false
	}
	elements := make(sets.Set[string], c.heap.Len())
	for _, e := range c.heap.List() {
		info := e.(*workload.Info)
		elements.Insert(workload.Key(info.Obj))
	}
	return elements, true
}

func (c *clusterQueueBase) DumpInadmissible() (sets.Set[string], bool) {
	c.rwm.RLock()
	defer c.rwm.RUnlock()
	if len(c.inadmissibleWorkloads) == 0 {
		return nil, false
	}
	elements := make(sets.Set[string], len(c.inadmissibleWorkloads))
	for _, info := range c.inadmissibleWorkloads {
		elements.Insert(workload.Key(info.Obj))
	}
	return elements, true
}

func (c *clusterQueueBase) Snapshot() []*workload.Info {
	elements := c.totalElements()
	sort.Slice(elements, func(i, j int) bool {
		return queueOrdering(elements[i], elements[j])
	})
	return elements
}

func (c *clusterQueueBase) Info(key string) *workload.Info {
	c.rwm.RLock()
	defer c.rwm.RUnlock()
	info := c.heap.GetByKey(key)
	if info == nil {
		return nil
	}
	return info.(*workload.Info)
}

func (c *clusterQueueBase) totalElements() []*workload.Info {
	c.rwm.RLock()
	defer c.rwm.RUnlock()
	totalLen := c.heap.Len() + len(c.inadmissibleWorkloads)
	elements := make([]*workload.Info, 0, totalLen)
	for _, e := range c.heap.List() {
		info := e.(*workload.Info)
		elements = append(elements, info)
	}
	for _, e := range c.inadmissibleWorkloads {
		elements = append(elements, e)
	}
	return elements
}
