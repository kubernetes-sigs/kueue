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
	"container/heap"

	"k8s.io/apimachinery/pkg/util/sets"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	"sigs.k8s.io/kueue/pkg/workload"
)

// ClusterQueueImpl is the base implementation of ClusterQueue interface.
// It can be inherited and overwritten by other class.
type ClusterQueueImpl struct {
	// QueueingStrategy indicates the queueing strategy of the workloads
	// across the queues in this ClusterQueue.
	QueueingStrategy kueue.QueueingStrategy

	heap   heapImpl
	cohort string
}

var _ ClusterQueue = &ClusterQueueImpl{}

func (c *ClusterQueueImpl) Update(apiCQ *kueue.ClusterQueue) {
	c.QueueingStrategy = apiCQ.Spec.QueueingStrategy
	c.cohort = apiCQ.Spec.Cohort
}

func (c *ClusterQueueImpl) Cohort() string {
	return c.cohort
}

func (c *ClusterQueueImpl) AddFromQueue(q *Queue) bool {
	added := false
	for _, w := range q.items {
		if c.pushIfNotPresent(w) {
			added = true
		}
	}
	return added
}

func (c *ClusterQueueImpl) DeleteFromQueue(q *Queue) {
	for _, w := range q.items {
		c.Delete(w.Obj)
	}
}

// pushIfNotPresent pushes the workload to ClusterQueue.
// If the workload is already present, returns false. Otherwise returns true.
func (c *ClusterQueueImpl) pushIfNotPresent(info *workload.Info) bool {
	item := c.heap.items[workload.Key(info.Obj)]
	if item != nil {
		return false
	}
	heap.Push(&c.heap, *info)
	return true
}

func (c *ClusterQueueImpl) PushOrUpdate(w *kueue.Workload) {
	item := c.heap.items[workload.Key(w)]
	info := *workload.NewInfo(w)
	if item == nil {
		heap.Push(&c.heap, info)
		return
	}
	item.obj = info
	heap.Fix(&c.heap, item.index)
}

func (c *ClusterQueueImpl) Delete(w *kueue.Workload) {
	item := c.heap.items[workload.Key(w)]
	if item != nil {
		heap.Remove(&c.heap, item.index)
	}
}

func (c *ClusterQueueImpl) RequeueIfNotPresent(wInfo *workload.Info, _ bool) bool {
	return c.pushIfNotPresent(wInfo)
}

func (c *ClusterQueueImpl) QueueInadmissibleWorkloads() bool {
	return false
}

func (c *ClusterQueueImpl) Pop() *workload.Info {
	if c.heap.Len() == 0 {
		return nil
	}
	w := heap.Pop(&c.heap).(workload.Info)
	return &w
}

func (c *ClusterQueueImpl) Pending() int32 {
	return int32(len(c.heap.heap))
}

func (c *ClusterQueueImpl) Dump() (sets.String, bool) {
	if len(c.heap.items) == 0 {
		return sets.NewString(), false
	}
	elements := make(sets.String, len(c.heap.items))
	for _, e := range c.heap.items {
		elements.Insert(e.obj.Obj.Name)
	}
	return elements, true
}

func (c *ClusterQueueImpl) Info(key string) *workload.Info {
	item := c.heap.items[key]
	if item != nil {
		return &item.obj
	}
	return nil
}
