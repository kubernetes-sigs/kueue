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

// heap.Interface implementation inspired by
// https://github.com/kubernetes/kubernetes/blob/master/pkg/scheduler/internal/heap/heap.go

package heap

import (
	"container/heap"
)

// lessFunc is a function that receives two items and returns true if the first
// item should be placed before the second one when the list is sorted.
type lessFunc[T any] func(a, b *T) bool

// KeyFunc is a function type to get the key from an object.
type keyFunc[T any] func(obj *T) string

type heapItem[T any] struct {
	obj   *T
	index int
}

type itemKeyValue[T any] struct {
	key string
	obj *T
}

// data is an internal struct that implements the standard heap interface
// and keeps the data stored in the heap.
type data[T any] struct {
	// items is a map from key of the objects to the objects and their index
	items map[string]*heapItem[T]
	// keys keeps the keys of the objects ordered according to the heap invariant.
	keys     []string
	keyFunc  keyFunc[T]
	lessFunc lessFunc[T]
}

// Less compares two objects and returns true if the first one should go
// in front of the second one in the heap.
func (h *data[T]) Less(i, j int) bool {
	if i > h.Len() || j > h.Len() {
		return false
	}
	a, ok := h.items[h.keys[i]]
	if !ok {
		return false
	}
	b, ok := h.items[h.keys[j]]
	if !ok {
		return false
	}
	return h.lessFunc(a.obj, b.obj)
}

// Len returns the number of items in the Heap.
func (h *data[T]) Len() int {
	return len(h.keys)
}

// Swap implements swapping of two elements in the heap. This is a part of standard
// heap interface and should never be called directly.
func (h *data[T]) Swap(i, j int) {
	h.keys[i], h.keys[j] = h.keys[j], h.keys[i]
	h.items[h.keys[i]].index = i
	h.items[h.keys[j]].index = j
}

// Push is supposed to be called by heap.Push only.
func (h *data[T]) Push(kv any) {
	keyValue := kv.(itemKeyValue[T])
	h.items[keyValue.key] = &heapItem[T]{
		obj:   keyValue.obj,
		index: len(h.keys),
	}
	h.keys = append(h.keys, keyValue.key)
}

// Pop is supposed to be called by heap.Pop only.
func (h *data[T]) Pop() any {
	key := h.keys[len(h.keys)-1]
	h.keys = h.keys[:len(h.keys)-1]
	item, ok := h.items[key]
	if !ok {
		// This is an error
		return nil
	}
	delete(h.items, key)
	return item.obj
}

// Heap is a producer/consumer queue that implements a heap data structure.
// It can be used to implement priority queues and similar data structures.
type Heap[T any] struct {
	data data[T]
}

// PushOrUpdate inserts an item to the queue.
// The item will be updated if it already exists.
func (h *Heap[T]) PushOrUpdate(obj *T) {
	key := h.data.keyFunc(obj)
	if _, exists := h.data.items[key]; exists {
		h.data.items[key].obj = obj
		heap.Fix(&h.data, h.data.items[key].index)
	} else {
		heap.Push(&h.data, itemKeyValue[T]{key, obj})
	}
}

// PushIfNotPresent inserts an item to the queue. If an item with
// the key is present in the map, no changes is made to the item.
func (h *Heap[T]) PushIfNotPresent(obj *T) (added bool) {
	key := h.data.keyFunc(obj)
	if _, exists := h.data.items[key]; exists {
		return false
	}

	heap.Push(&h.data, itemKeyValue[T]{key, obj})
	return true
}

// Delete removes an item.
func (h *Heap[T]) Delete(key string) {
	item, exists := h.data.items[key]
	if !exists {
		return
	}
	heap.Remove(&h.data, item.index)
}

// Pop returns the head of the heap and removes it.
func (h *Heap[T]) Pop() *T {
	return heap.Pop(&h.data).(*T)
}

// GetByKey returns the requested item, or sets exists=false.
func (h *Heap[T]) GetByKey(key string) *T {
	item, exists := h.data.items[key]
	if !exists {
		return nil
	}
	return item.obj
}

// Len returns the number of items in the heap.
func (h *Heap[T]) Len() int {
	return h.data.Len()
}

// List returns a list of all the items.
func (h *Heap[T]) List() []*T {
	list := make([]*T, 0, h.Len())
	for _, item := range h.data.items {
		list = append(list, item.obj)
	}
	return list
}

// New returns a Heap which can be used to queue up items to process.
func New[T any](keyFn keyFunc[T], lessFn lessFunc[T]) *Heap[T] {
	return &Heap[T]{
		data: data[T]{
			items:    make(map[string]*heapItem[T]),
			keyFunc:  keyFn,
			lessFunc: lessFn,
		},
	}
}
