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

// Package podtracker maintains event-driven Pod state for the scheduler cache.
// It does not decide which Pods consume capacity or are covered by reservations.
package podtracker

import (
	"maps"
	"slices"
	"sync"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
)

type podsByKey map[client.ObjectKey]*corev1.Pod
type podsByWorkload map[client.ObjectKey]podsByKey

// Tracker stores Pods and indexes them by their Workload annotation.
// Its zero value is ready to use. All methods are safe for concurrent use.
// A Tracker must not be copied after first use; use Clone instead.
type Tracker struct {
	mu           sync.RWMutex
	pods         podsByKey
	workloadPods podsByWorkload
}

// Pod returns the latest observation for key, or nil if it is not tracked.
// The returned Pod is read-only.
func (t *Tracker) Pod(key client.ObjectKey) *corev1.Pod {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.pods[key]
}

// PodsForWorkload returns the Pods annotated with the given Workload name in
// its namespace, in unspecified order. Returned Pods are read-only.
func (t *Tracker) PodsForWorkload(wlKey client.ObjectKey) []*corev1.Pod {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.workloadPods[wlKey].toSlice()
}

func (m podsByWorkload) recordPod(wlKey client.ObjectKey, podKey client.ObjectKey, pod *corev1.Pod) {
	if _, ok := m[wlKey]; !ok {
		m[wlKey] = make(podsByKey)
	}
	m[wlKey][podKey] = pod
}
func (m podsByWorkload) forgetPod(wlKey client.ObjectKey, podKey client.ObjectKey) {
	if len(m) == 0 {
		return
	}
	delete(m[wlKey], podKey)
	if len(m[wlKey]) == 0 {
		delete(m, wlKey)
	}
}

func (p podsByKey) toSlice() []*corev1.Pod {
	if len(p) == 0 {
		return nil
	}
	return slices.Collect(maps.Values(p))
}

// Pods returns all tracked Pods in unspecified order. Returned Pods are read-only.
func (t *Tracker) Pods() []*corev1.Pod {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.pods.toSlice()
}

// Clone returns an independent tracker with the current Pod state. Its indexes
// can be changed without affecting the original. Read-only Pod objects are
// shared; Track deep-copies incoming Pods before storing them.
func (t *Tracker) Clone() *Tracker {
	t.mu.RLock()
	defer t.mu.RUnlock()

	copyTracker := &Tracker{
		pods:         maps.Clone(t.pods),
		workloadPods: make(podsByWorkload, len(t.workloadPods)),
	}
	for k, v := range t.workloadPods {
		copyTracker.workloadPods[k] = maps.Clone(v)
	}
	return copyTracker
}

// RemoveWorkload removes all Pods indexed under the given Workload.
// This only changes tracked state; it does not delete Kubernetes objects.
func (t *Tracker) RemoveWorkload(wlKey client.ObjectKey) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if pods, ok := t.workloadPods[wlKey]; ok {
		for podKey := range pods {
			delete(t.pods, podKey)
		}
		delete(t.workloadPods, wlKey)
	}
}

// Track adds or replaces a Pod by namespace and name, updating its Workload
// index. The input is deep-copied and may be modified by the caller afterward.
// A nil Pod is ignored. Lifecycle and capacity filtering belong to the caller.
func (t *Tracker) Track(pod *corev1.Pod) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if pod == nil {
		return
	}
	pod = pod.DeepCopy()
	key := client.ObjectKeyFromObject(pod)
	if oldPod, found := t.pods[key]; found {
		t.clearPod(key, oldPod)
	}
	t.savePod(key, pod)
}

// Untrack removes a Pod and returns its last observed node name, or an empty
// string if the Pod was not tracked or had no assigned node.
func (t *Tracker) Untrack(key client.ObjectKey) string {
	t.mu.Lock()
	defer t.mu.Unlock()

	if pod, ok := t.pods[key]; ok {
		t.clearPod(key, pod)
		return pod.Spec.NodeName
	}
	return ""
}

func (t *Tracker) clearPod(podKey client.ObjectKey, pod *corev1.Pod) {
	delete(t.pods, podKey)

	wl := pod.Annotations[kueue.WorkloadAnnotation]
	if wl != "" {
		wlKey := client.ObjectKey{Namespace: pod.Namespace, Name: wl}
		t.workloadPods.forgetPod(wlKey, podKey)
	}
}

func (t *Tracker) savePod(podKey client.ObjectKey, pod *corev1.Pod) {
	if t.pods == nil {
		t.pods = make(podsByKey)
	}
	t.pods[podKey] = pod

	wl := pod.Annotations[kueue.WorkloadAnnotation]
	if wl != "" {
		if t.workloadPods == nil {
			t.workloadPods = make(podsByWorkload)
		}
		wlKey := client.ObjectKey{Namespace: pod.Namespace, Name: wl}
		t.workloadPods.recordPod(wlKey, podKey, pod)
	}
}
