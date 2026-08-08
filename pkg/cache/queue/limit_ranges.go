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

package queue

import (
	"sync"

	corev1 "k8s.io/api/core/v1"
)

// LimitRanges stores LimitRange objects, indexed by their namespace and name.
type LimitRanges struct {
	sync.RWMutex
	// store maps namespace -> limit range name -> LimitRange
	store map[string]map[string]*corev1.LimitRange
}

func newLimitRanges() *LimitRanges {
	return &LimitRanges{
		store: make(map[string]map[string]*corev1.LimitRange),
	}
}

func hasContainerOrPodType(lr *corev1.LimitRange) bool {
	for i := range lr.Spec.Limits {
		t := lr.Spec.Limits[i].Type
		if t == corev1.LimitTypeContainer || t == corev1.LimitTypePod {
			return true
		}
	}
	return false
}

// AddOrUpdate inserts or updates a LimitRange in the cache, or deletes it if it does not
// contain Container or Pod type limits.
func (l *LimitRanges) AddOrUpdate(lr *corev1.LimitRange) {
	l.Lock()
	defer l.Unlock()
	if !hasContainerOrPodType(lr) {
		l.deleteWithoutLock(lr)
		return
	}
	ns := lr.Namespace
	if l.store[ns] == nil {
		l.store[ns] = make(map[string]*corev1.LimitRange)
	}
	l.store[ns][lr.Name] = lr
}

func (l *LimitRanges) Update(oldLr, newLr *corev1.LimitRange) {
	l.AddOrUpdate(newLr)
}

func (l *LimitRanges) Delete(lr *corev1.LimitRange) {
	l.Lock()
	defer l.Unlock()
	l.deleteWithoutLock(lr)
}

func (l *LimitRanges) deleteWithoutLock(lr *corev1.LimitRange) {
	ns := lr.Namespace
	if l.store[ns] != nil {
		delete(l.store[ns], lr.Name)
		if len(l.store[ns]) == 0 {
			delete(l.store, ns)
		}
	}
}

func (l *LimitRanges) GetForNamespace(ns string) []corev1.LimitRange {
	l.RLock()
	defer l.RUnlock()
	if len(l.store[ns]) == 0 {
		return nil
	}
	res := make([]corev1.LimitRange, 0, len(l.store[ns]))
	for _, lr := range l.store[ns] {
		res = append(res, *lr)
	}
	return res
}
