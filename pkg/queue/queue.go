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
	"fmt"

	"k8s.io/apimachinery/pkg/labels"

	kueue "sigs.k8s.io/kueue/api/v1alpha1"
	"sigs.k8s.io/kueue/pkg/workload"
)

// Key is the key used to index the queue.
func Key(q *kueue.Queue) string {
	return fmt.Sprintf("%s/%s", q.Namespace, q.Name)
}

// keyForWorkload is the key to find the queue for the workload in the index.
func keyForWorkload(w *kueue.QueuedWorkload) string {
	return fmt.Sprintf("%s/%s", w.Namespace, w.Spec.QueueName)
}

// Queue is the internal implementation of kueue.Queue.
type Queue struct {
	Priority          int64
	Capacity          string
	NamespaceSelector labels.Selector

	heap heapImpl
}

func newQueue() *Queue {
	return &Queue{
		heap: heapImpl{
			less:  creationFIFO,
			items: make(map[string]*heapItem),
		},
	}
}

func (q *Queue) setProperties(apiQueue *kueue.Queue) error {
	q.Capacity = string(apiQueue.Spec.Capacity)
	return nil
}

func (q *Queue) PushIfNotPresent(info *workload.Info) bool {
	item := q.heap.items[info.Obj.Name]
	if item != nil {
		return false
	}
	heap.Push(&q.heap, *info)
	return true
}

func (q *Queue) PushOrUpdate(w *kueue.QueuedWorkload) {
	item := q.heap.items[w.Name]
	info := *workload.NewInfo(w)
	if item == nil {
		heap.Push(&q.heap, info)
		return
	}
	item.obj = info
	heap.Fix(&q.heap, item.index)
}

func (q *Queue) Delete(w *kueue.QueuedWorkload) {
	item := q.heap.items[w.Name]
	if item != nil {
		heap.Remove(&q.heap, item.index)
	}
}
func (q *Queue) Pop() *workload.Info {
	if q.heap.Len() == 0 {
		return nil
	}
	w := heap.Pop(&q.heap).(workload.Info)
	return &w
}

func creationFIFO(a, b workload.Info) bool {
	return a.Obj.CreationTimestamp.Before(&b.Obj.CreationTimestamp)
}

// heap.Interface implementation inspired by
// https://github.com/kubernetes/kubernetes/blob/master/pkg/scheduler/internal/heap/heap.go

type lessFunc func(a, b workload.Info) bool

type heapItem struct {
	obj   workload.Info
	index int
}

type heapImpl struct {
	// items is a map from key of the objects to the objects and their index
	items map[string]*heapItem
	// heap keeps the keys of the objects ordered according to the heap invariant.
	heap []string
	less lessFunc
}

func (h *heapImpl) Len() int {
	return len(h.heap)
}

func (h *heapImpl) Less(i, j int) bool {
	a := h.items[h.heap[i]]
	b := h.items[h.heap[j]]
	return h.less(a.obj, b.obj)
}

func (h *heapImpl) Swap(i, j int) {
	h.heap[i], h.heap[j] = h.heap[j], h.heap[i]
	h.items[h.heap[i]].index = i
	h.items[h.heap[j]].index = j
}

func (h *heapImpl) Push(x interface{}) {
	wInfo := x.(workload.Info)
	key := wInfo.Obj.Name
	h.items[key] = &heapItem{
		obj:   wInfo,
		index: len(h.heap),
	}
	h.heap = append(h.heap, key)
}

func (h *heapImpl) Pop() interface{} {
	key := h.heap[len(h.heap)-1]
	h.heap = h.heap[:len(h.heap)-1]
	obj := h.items[key].obj
	delete(h.items, key)
	return obj
}
