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

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1alpha2"
	"sigs.k8s.io/kueue/pkg/util/heap"
	"sigs.k8s.io/kueue/pkg/workload"
)

// ClusterQueueImpl is the base implementation of ClusterQueue interface.
// It can be inherited and overwritten by other class.
type ClusterQueueImpl struct {
	heap              heap.Heap
	cohort            string
	namespaceSelector labels.Selector

	// inadmissibleWorkloads are workloads that have been tried at least once and couldn't be admitted.
	inadmissibleWorkloads map[string]*workload.Info
}

func newClusterQueueImpl(keyFunc func(obj interface{}) string, lessFunc func(a, b interface{}) bool) *ClusterQueueImpl {
	return &ClusterQueueImpl{
		heap:                  heap.New(keyFunc, lessFunc),
		inadmissibleWorkloads: make(map[string]*workload.Info),
	}
}

var _ ClusterQueue = &ClusterQueueImpl{}

func (c *ClusterQueueImpl) Update(apiCQ *kueue.ClusterQueue) error {
	c.cohort = apiCQ.Spec.Cohort
	nsSelector, err := metav1.LabelSelectorAsSelector(apiCQ.Spec.NamespaceSelector)
	if err != nil {
		return err
	}
	c.namespaceSelector = nsSelector
	return nil
}

func (c *ClusterQueueImpl) Cohort() string {
	return c.cohort
}

func (c *ClusterQueueImpl) AddFromLocalQueue(q *LocalQueue) bool {
	added := false
	for _, info := range q.items {
		if c.heap.PushIfNotPresent(info) {
			added = true
		}
	}
	return added
}

func (c *ClusterQueueImpl) PushOrUpdate(wInfo *workload.Info) {
	key := workload.Key(wInfo.Obj)
	oldInfo := c.inadmissibleWorkloads[key]
	if oldInfo != nil {
		// update in place if the workload was inadmissible and didn't change
		// to potentially become admissible.
		if equality.Semantic.DeepEqual(oldInfo.Obj.Spec, wInfo.Obj.Spec) {
			c.inadmissibleWorkloads[key] = wInfo
			return
		}
		// otherwise move or update in place in the queue.
		delete(c.inadmissibleWorkloads, key)
	}
	c.heap.PushOrUpdate(wInfo)
}

func (c *ClusterQueueImpl) Delete(w *kueue.Workload) {
	key := workload.Key(w)
	delete(c.inadmissibleWorkloads, key)
	c.heap.Delete(key)
}

func (c *ClusterQueueImpl) DeleteFromLocalQueue(q *LocalQueue) {
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

func (c *ClusterQueueImpl) RequeueIfNotPresent(wInfo *workload.Info, reason RequeueReason) bool {
	// By default, the only reason we don't requeue immediately is if the workload doesn't
	// match the CQ's namespace selector.
	return c.requeueIfNotPresent(wInfo, reason != RequeueReasonNamespaceMismatch)
}

// requeueIfNotPresent inserts a workload that cannot be admitted into
// ClusterQueue, unless it is already in the queue. If immediate is true,
// the workload will be pushed back to heap directly. If not,
// the workload will be put into the inadmissibleWorkloads.
func (c *ClusterQueueImpl) requeueIfNotPresent(wInfo *workload.Info, immediate bool) bool {
	key := workload.Key(wInfo.Obj)
	if immediate {
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
// If at least one workload is moved, returns true. Otherwise returns false.
func (c *ClusterQueueImpl) QueueInadmissibleWorkloads(ctx context.Context, client client.Client) bool {
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

func (c *ClusterQueueImpl) Pending() int {
	return c.PendingActive() + c.PendingInadmissible()
}

func (c *ClusterQueueImpl) PendingActive() int {
	return c.heap.Len()
}

func (c *ClusterQueueImpl) PendingInadmissible() int {
	return len(c.inadmissibleWorkloads)
}

func (c *ClusterQueueImpl) Pop() *workload.Info {
	if c.heap.Len() == 0 {
		return nil
	}

	info := c.heap.Pop()
	if info == nil {
		return nil
	}
	return info.(*workload.Info)
}

func (c *ClusterQueueImpl) Dump() (sets.String, bool) {
	if c.heap.Len() == 0 {
		return sets.NewString(), false
	}
	elements := make(sets.String, c.heap.Len())
	for _, e := range c.heap.List() {
		info := e.(*workload.Info)
		elements.Insert(info.Obj.Name)
	}
	return elements, true
}

func (c *ClusterQueueImpl) DumpInadmissible() (sets.String, bool) {
	if len(c.inadmissibleWorkloads) == 0 {
		return sets.NewString(), false
	}
	elements := make(sets.String, len(c.inadmissibleWorkloads))
	for _, info := range c.inadmissibleWorkloads {
		elements.Insert(info.Obj.Name)
	}
	return elements, true
}

func (c *ClusterQueueImpl) Info(key string) *workload.Info {
	info := c.heap.GetByKey(key)
	if info == nil {
		return nil
	}
	return info.(*workload.Info)
}
