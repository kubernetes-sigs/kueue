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

package orderedgroups

// OrderedGroups represents a mapping of group keys to grouped items and tracks their insertion order for deterministic iteration.
type OrderedGroups[K comparable, V any] struct {
	groups         map[K][]V
	insertionOrder []K
}

func NewOrderedGroups[K comparable, V any]() *OrderedGroups[K, V] {
	return &OrderedGroups[K, V]{
		groups:         make(map[K][]V),
		insertionOrder: make([]K, 0),
	}
}

// Insert inserts an element to the group. If the group does not exist yet, tracks it's insertion order.
func (om *OrderedGroups[K, V]) Insert(key K, value V) {
	if _, ok := om.groups[key]; !ok {
		om.insertionOrder = append(om.insertionOrder, key)
	}
	om.groups[key] = append(om.groups[key], value)
}

// InOrder iterates over the collected groups in order of insertion.
func (om *OrderedGroups[K, V]) InOrder(yield func(K, []V) bool) {
	for _, k := range om.insertionOrder {
		if !yield(k, om.groups[k]) {
			break
		}
	}
}
