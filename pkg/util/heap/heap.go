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

// heap.Interface implementation inspired by
// https://github.com/kubernetes/kubernetes/blob/master/pkg/scheduler/internal/heap/heap.go
package heap

import (
	"container/heap"
	"sort"
)

// lessFunc is a function that receives two items and returns true if the first
// item should be placed before the second one when the list is sorted.
type lessFunc func(a, b interface{}) bool

// KeyFunc is a function type to get the key from an object.
type keyFunc func(obj interface{}) string

type heapItem struct {
	obj   interface{}
	index int
}

type itemKeyValue struct {
	key string
	obj interface{}
}

// data is an internal struct that implements the standard heap interface
// and keeps the data stored in the heap.
type data struct {
	// items is a map from key of the objects to the objects and their index
	items map[string]*heapItem
	// keys keeps the keys of the objects ordered according to the heap invariant.
	keys     []string
	keyFunc  keyFunc
	lessFunc lessFunc
}

var _ = heap.Interface(&data{})

// Less compares two objects and returns true if the first one should go
// in front of the second one in the heap.
func (h *data) Less(i, j int) bool {
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
func (h *data) Len() int {
	return len(h.keys)
}

// Swap implements swapping of two elements in the heap. This is a part of standard
// heap interface and should never be called directly.
func (h *data) Swap(i, j int) {
	h.keys[i], h.keys[j] = h.keys[j], h.keys[i]
	h.items[h.keys[i]].index = i
	h.items[h.keys[j]].index = j
}

// Push is supposed to be called by heap.Push only.
func (h *data) Push(kv interface{}) {
	keyValue := kv.(*itemKeyValue)
	h.items[keyValue.key] = &heapItem{
		obj:   keyValue.obj,
		index: len(h.keys),
	}
	h.keys = append(h.keys, keyValue.key)
}

// Pop is supposed to be called by heap.Pop only.
func (h *data) Pop() interface{} {
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
type Heap struct {
	data data
}

// PushOrUpdate inserts an item to the queue.
// The item will be updated if it already exists.
func (h *Heap) PushOrUpdate(obj interface{}) {
	key := h.data.keyFunc(obj)
	if _, exists := h.data.items[key]; exists {
		h.data.items[key].obj = obj
		heap.Fix(&h.data, h.data.items[key].index)
	} else {
		heap.Push(&h.data, &itemKeyValue{key, obj})
	}
}

// PushIfNotPresent inserts an item to the queue. If an item with
// the key is present in the map, no changes is made to the item.
func (h *Heap) PushIfNotPresent(obj interface{}) (added bool) {
	key := h.data.keyFunc(obj)
	if _, exists := h.data.items[key]; exists {
		return false
	}

	heap.Push(&h.data, &itemKeyValue{key, obj})
	return true
}

// Delete removes an item.
func (h *Heap) Delete(key string) {
	item, exists := h.data.items[key]
	if !exists {
		return
	}
	heap.Remove(&h.data, item.index)
}

// Pop returns the head of the heap and removes it.
func (h *Heap) Pop() interface{} {
	return heap.Pop(&h.data)
}

// Get returns the requested item, exists, error.
func (h *Heap) Get(obj interface{}) (item interface{}) {
	key := h.data.keyFunc(obj)
	return h.GetByKey(key)
}

// GetByKey returns the requested item, or sets exists=false.
func (h *Heap) GetByKey(key string) interface{} {
	item, exists := h.data.items[key]
	if !exists {
		return nil
	}
	return item.obj
}

// Len returns the number of items in the heap.
func (h *Heap) Len() int {
	return h.data.Len()
}

// List returns a sorted list of all the items.
func (h *Heap) List() []interface{} {
	sort.Sort(&h.data)
	list := make([]interface{}, 0, h.Len())
	for _, item := range h.data.items {
		list = append(list, item.obj)
	}
	return list
}

// New returns a Heap which can be used to queue up items to process.
func New(keyFn keyFunc, lessFn lessFunc) Heap {
	return Heap{
		data: data{
			items:    map[string]*heapItem{},
			keyFunc:  keyFn,
			lessFunc: lessFn,
		},
	}
}
