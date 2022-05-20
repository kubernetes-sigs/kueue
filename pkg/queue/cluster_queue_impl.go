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
	"k8s.io/apimachinery/pkg/util/sets"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	"sigs.k8s.io/kueue/pkg/util/heap"
	"sigs.k8s.io/kueue/pkg/workload"
)

// ClusterQueueImpl is the base implementation of ClusterQueue interface.
// It can be inherited and overwritten by other class.
type ClusterQueueImpl struct {
	// QueueingStrategy indicates the queueing strategy of the workloads
	// across the queues in this ClusterQueue.
	QueueingStrategy kueue.QueueingStrategy

	heap   heap.Heap
	cohort string
}

func newClusterQueueImpl(keyFunc func(obj interface{}) string, lessFunc func(a, b interface{}) bool) *ClusterQueueImpl {
	return &ClusterQueueImpl{
		heap: heap.New(keyFunc, lessFunc),
	}
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
	for _, info := range q.items {
		if c.pushIfNotPresent(info) {
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
	return c.heap.PushIfNotPresent(info)
}

func (c *ClusterQueueImpl) PushOrUpdate(info *workload.Info) {
	c.heap.PushOrUpdate(info)
}

func (c *ClusterQueueImpl) Delete(w *kueue.Workload) {
	c.heap.Delete(workload.Key(w))
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

	info := c.heap.Pop()
	if info == nil {
		return nil
	}
	return info.(*workload.Info)
}

func (c *ClusterQueueImpl) Pending() int32 {
	return int32(c.heap.Len())
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

func (c *ClusterQueueImpl) Info(key string) *workload.Info {
	info := c.heap.GetByKey(key)
	if info == nil {
		return nil
	}
	return info.(*workload.Info)
}
