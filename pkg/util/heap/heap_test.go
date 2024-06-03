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

// This file was copied from client-go/tools/cache/heap.go and modified
// for our non thread-safe heap

package heap

import (
	"testing"
)

func testHeapObjectKeyFunc(obj *testHeapObject) string {
	return obj.name
}

type testHeapObject struct {
	name string
	val  int
}

func mkHeapObj(name string, val int) *testHeapObject {
	return &testHeapObject{name: name, val: val}
}

func compareInts(obj1, obj2 *testHeapObject) bool {
	return obj1.val < obj2.val
}

// TestHeapBasic tests Heap invariant
func TestHeapBasic(t *testing.T) {
	h := New(testHeapObjectKeyFunc, compareInts)
	const amount = 500
	var i int

	for i = amount; i > 0; i-- {
		h.PushOrUpdate(mkHeapObj(string([]rune{'a', rune(i)}), i))
	}

	// Make sure that the numbers are popped in ascending order.
	prevNum := 0
	for i := 0; i < amount; i++ {
		obj := h.Pop()
		num := obj.val
		// All the items must be sorted.
		if prevNum > num {
			t.Errorf("got %v out of order, last was %v", obj, prevNum)
		}
		prevNum = num
	}
}

// Tests Heap.PushOrUpdate and ensures that heap invariant is preserved after adding items.
func TestHeap_Add(t *testing.T) {
	h := New(testHeapObjectKeyFunc, compareInts)
	h.PushOrUpdate(mkHeapObj("foo", 10))
	h.PushOrUpdate(mkHeapObj("bar", 1))
	h.PushOrUpdate(mkHeapObj("baz", 11))
	h.PushOrUpdate(mkHeapObj("zab", 30))
	h.PushOrUpdate(mkHeapObj("foo", 13)) // This updates "foo".

	item := h.Pop()
	if e, a := 1, item.val; a != e {
		t.Fatalf("expected %d, got %d", e, a)
	}
	item = h.Pop()
	if e, a := 11, item.val; a != e {
		t.Fatalf("expected %d, got %d", e, a)
	}
	h.Delete("baz")                      // Nothing is deleted.
	h.PushOrUpdate(mkHeapObj("foo", 14)) // foo is updated.
	item = h.Pop()
	if e, a := 14, item.val; a != e {
		t.Fatalf("expected %d, got %d", e, a)
	}
	item = h.Pop()
	if e, a := 30, item.val; a != e {
		t.Fatalf("expected %d, got %d", e, a)
	}
}

// TestHeap_PushIfNotPresent tests Heap.PushIfNotPresent and ensures that heap
// invariant is preserved after adding items.
func TestHeap_PushIfNotPresent(t *testing.T) {
	h := New(testHeapObjectKeyFunc, compareInts)
	_ = h.PushIfNotPresent(mkHeapObj("foo", 10))
	_ = h.PushIfNotPresent(mkHeapObj("bar", 1))
	_ = h.PushIfNotPresent(mkHeapObj("baz", 11))
	_ = h.PushIfNotPresent(mkHeapObj("zab", 30))
	_ = h.PushIfNotPresent(mkHeapObj("foo", 13)) // This is not added.

	if len := len(h.data.items); len != 4 {
		t.Errorf("unexpected number of items: %d", len)
	}
	if val := h.data.items["foo"].obj.val; val != 10 {
		t.Errorf("unexpected value: %d", val)
	}
	item := h.Pop()
	if e, a := 1, item.val; a != e {
		t.Fatalf("expected %d, got %d", e, a)
	}
	item = h.Pop()
	if e, a := 10, item.val; a != e {
		t.Fatalf("expected %d, got %d", e, a)
	}
	// bar is already popped. Let's add another one.
	_ = h.PushIfNotPresent(mkHeapObj("bar", 14))
	item = h.Pop()
	if e, a := 11, item.val; a != e {
		t.Fatalf("expected %d, got %d", e, a)
	}
	item = h.Pop()
	if e, a := 14, item.val; a != e {
		t.Fatalf("expected %d, got %d", e, a)
	}
}

// TestHeap_PushOrUpdate tests Heap.PushOrUpdate and ensures that heap
// invariant is preserved after adding/updating items.
func TestHeap_PushOrUpdate(t *testing.T) {
	h := New(testHeapObjectKeyFunc, compareInts)
	h.PushOrUpdate(mkHeapObj("foo", 100))
	h.PushOrUpdate(mkHeapObj("baz", 20))
	h.PushOrUpdate(mkHeapObj("foo", 1)) // This behaves as update.
	h.PushOrUpdate(mkHeapObj("zab", 8)) // This behaves as add.

	if len := len(h.data.items); len != 3 {
		t.Errorf("unexpected number of items: %d", len)
	}
	item := h.Pop()
	if e, a := 1, item.val; a != e {
		t.Fatalf("expected %d, got %d", e, a)
	}
	item = h.Pop()
	if e, a := 8, item.val; a != e {
		t.Fatalf("expected %d, got %d", e, a)
	}
}

// TestHeap_Delete tests Heap.Delete and ensures that heap invariant is
// preserved after deleting items.
func TestHeap_Delete(t *testing.T) {
	h := New(testHeapObjectKeyFunc, compareInts)
	h.PushOrUpdate(mkHeapObj("foo", 10))
	h.PushOrUpdate(mkHeapObj("bar", 1))
	h.PushOrUpdate(mkHeapObj("bal", 31))
	h.PushOrUpdate(mkHeapObj("baz", 11))

	// Delete head. Delete should work with "key" and doesn't care about the value.
	h.Delete("bar")
	item := h.Pop()
	if e, a := 10, item.val; a != e {
		t.Fatalf("expected %d, got %d", e, a)
	}
	h.PushOrUpdate(mkHeapObj("zab", 30))
	h.PushOrUpdate(mkHeapObj("faz", 30))
	len := h.data.Len()
	// Delete non-existing item.
	if h.Delete("non-existent"); len != h.data.Len() {
		t.Fatalf("Didn't expect any item removal")
	}
	// Delete tail.
	h.Delete("bal")
	// Delete one of the items with value 30.
	h.Delete("zab")
	item = h.Pop()
	if e, a := 11, item.val; a != e {
		t.Fatalf("expected %d, got %d", e, a)
	}
	item = h.Pop()
	if e, a := 30, item.val; a != e {
		t.Fatalf("expected %d, got %d", e, a)
	}
	if h.data.Len() != 0 {
		t.Fatalf("expected an empty heap.")
	}
}

// TestHeap_Update tests Heap.PushOrUpdate and ensures that heap invariant is
// preserved after adding items.
func TestHeap_Update(t *testing.T) {
	h := New(testHeapObjectKeyFunc, compareInts)
	h.PushOrUpdate(mkHeapObj("foo", 10))
	h.PushOrUpdate(mkHeapObj("bar", 1))
	h.PushOrUpdate(mkHeapObj("bal", 31))
	h.PushOrUpdate(mkHeapObj("baz", 11))

	// Update an item to a value that should push it to the head.
	h.PushOrUpdate(mkHeapObj("baz", 0))
	if h.data.keys[0] != "baz" || h.data.items["baz"].index != 0 {
		t.Fatalf("expected baz to be at the head")
	}
	item := h.Pop()
	if e, a := 0, item.val; a != e {
		t.Fatalf("expected %d, got %d", e, a)
	}
	// Update bar to push it farther back in the queue.
	h.PushOrUpdate(mkHeapObj("bar", 100))
	if h.data.keys[0] != "foo" || h.data.items["foo"].index != 0 {
		t.Fatalf("expected foo to be at the head")
	}
}

// TestHeap_GetByKey tests Heap.GetByKey and is very similar to TestHeap_Get.
func TestHeap_GetByKey(t *testing.T) {
	h := New(testHeapObjectKeyFunc, compareInts)
	h.PushOrUpdate(mkHeapObj("foo", 10))
	h.PushOrUpdate(mkHeapObj("bar", 1))
	h.PushOrUpdate(mkHeapObj("bal", 31))
	h.PushOrUpdate(mkHeapObj("baz", 11))

	obj := h.GetByKey("baz")
	if obj == nil || obj.val != 11 {
		t.Fatalf("unexpected error in getting element")
	}
	// Get non-existing object.
	if obj = h.GetByKey("non-existing"); obj != nil {
		t.Fatalf("didn't expect to get any object")
	}
}

// TestHeap_List tests Heap.List function.
func TestHeap_List(t *testing.T) {
	h := New(testHeapObjectKeyFunc, compareInts)
	list := h.List()
	if len(list) != 0 {
		t.Errorf("expected an empty list")
	}

	items := map[string]int{
		"foo": 10,
		"bar": 1,
		"bal": 30,
		"baz": 11,
		"faz": 30,
	}
	for k, v := range items {
		h.PushOrUpdate(mkHeapObj(k, v))
	}
	list = h.List()
	if len(list) != len(items) {
		t.Errorf("expected %d items, got %d", len(items), len(list))
	}
	for _, obj := range list {
		v, ok := items[obj.name]
		if !ok || v != obj.val {
			t.Errorf("unexpected item in the list: %v", obj)
		}
	}
}
