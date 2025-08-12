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

package maps

import (
	"fmt"
	"maps"
	"slices"
	"sync"

	"k8s.io/apimachinery/pkg/util/sets"
)

// Copy copies all key/value pairs in src adding them to dst.
// When a key in src is already present in dst,
// the value in dst will be overwritten by the value associated
// with the key in src.
func Copy[K comparable, V any, S ~map[K]V](dst *S, src S) {
	if dst == nil {
		return
	}
	if *dst == nil && src != nil {
		*dst = make(S, len(src))
	}
	maps.Copy(*dst, src)
}

// HaveConflict checks if a and b have the same key, but different value
func HaveConflict[K comparable, V comparable, S ~map[K]V](a, b S) error {
	for k, av := range a {
		if bv, found := b[k]; found && av != bv {
			return fmt.Errorf("conflict for key=%v, value1=%v, value2=%v", k, av, bv)
		}
	}
	return nil
}

// Contains returns true if a contains all the keys in b with the same value
func Contains[K, V comparable, A ~map[K]V, B ~map[K]V](a A, b B) bool {
	for k, bv := range b {
		if av, found := a[k]; !found || av != bv {
			return false
		}
	}
	return true
}

// FilterKeys returns a sub-map containing only keys from the given list
func FilterKeys[K comparable, V any, M ~map[K]V](m M, k []K) M {
	if m == nil || len(k) == 0 {
		return nil
	}
	ret := make(M, len(k))
	for _, key := range k {
		if v, found := m[key]; found {
			ret[key] = v
		}
	}
	return ret
}

// DeepCopySets creates a deep copy of map[K]Set which would otherwise be referenced
func DeepCopySets[K comparable, T comparable](src map[K]sets.Set[T]) map[K]sets.Set[T] {
	c := make(map[K]sets.Set[T], len(src))
	for key, set := range src {
		c[key] = set.Clone()
	}
	return c
}

// SyncMap - generic RWMutex protected map.
type SyncMap[K comparable, V any] struct {
	lock sync.RWMutex
	m    map[K]V
}

func NewSyncMap[K comparable, V any](size int) *SyncMap[K, V] {
	return &SyncMap[K, V]{
		m: make(map[K]V, size),
	}
}

func (dwc *SyncMap[K, V]) Add(k K, v V) {
	dwc.lock.Lock()
	defer dwc.lock.Unlock()
	dwc.m[k] = v
}

func (dwc *SyncMap[K, V]) Get(k K) (V, bool) {
	dwc.lock.RLock()
	defer dwc.lock.RUnlock()
	v, found := dwc.m[k]
	return v, found
}

func (dwc *SyncMap[K, V]) Len() int {
	dwc.lock.RLock()
	defer dwc.lock.RUnlock()
	return len(dwc.m)
}

func (dwc *SyncMap[K, V]) Delete(k K) {
	dwc.lock.Lock()
	defer dwc.lock.Unlock()
	delete(dwc.m, k)
}

func (dwc *SyncMap[K, V]) Keys() []K {
	dwc.lock.RLock()
	defer dwc.lock.RUnlock()
	return slices.Collect(maps.Keys(dwc.m))
}
