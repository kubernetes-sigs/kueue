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
// All methods are safe for concurrent use.
// A Tracker must not be copied after first use; use Clone instead.
type Tracker struct {
	mu                   sync.RWMutex
	activePodsTracker    *tracker
	preempteePodsTracker *tracker
}

func NewTracker() *Tracker {
	return &Tracker{
		activePodsTracker:    newTracker(),
		preempteePodsTracker: newTracker(),
	}
}

type tracker struct {
	pods         podsByKey
	workloadPods podsByWorkload
}

func newTracker() *tracker {
	return &tracker{
		pods:         make(podsByKey),
		workloadPods: make(podsByWorkload),
	}
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

// PodsForWorkload returns the Pods annotated with the given Workload name in
// its namespace, in unspecified order.
// Only lists pods not marked as Committed Preemption Victims.
// Returned Pods are read-only.
func (t *Tracker) PodsForWorkload(wlKey client.ObjectKey) []*corev1.Pod {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return t.activePodsTracker.workloadPods[wlKey].toSlice()
}

// PreempteePods returns Pods designated for preemption,
// grouped by the Workload they belonged to.
// Returned Pods are read-only.
func (t *Tracker) PreempteePods() map[client.ObjectKey][]*corev1.Pod {
	t.mu.RLock()
	defer t.mu.RUnlock()

	preemptees := make(map[client.ObjectKey][]*corev1.Pod, len(t.preempteePodsTracker.workloadPods))
	for wl, pods := range t.preempteePodsTracker.workloadPods {
		preemptees[wl] = pods.toSlice()
	}
	return preemptees
}

// Pods returns all tracked Pods in unspecified order.
// Only lists pods not marked as Committed Preemption Victims.
// Returned Pods are read-only.
func (t *Tracker) Pods() []*corev1.Pod {
	t.mu.RLock()
	defer t.mu.RUnlock()

	return t.activePodsTracker.pods.toSlice()
}

// Clone returns an independent tracker with the current Pod state. Its indexes
// can be changed without affecting the original. Read-only Pod objects are
// shared; Track deep-copies incoming Pods before storing them.
func (t *Tracker) Clone() *Tracker {
	t.mu.RLock()
	defer t.mu.RUnlock()

	return &Tracker{
		activePodsTracker:    t.activePodsTracker.clone(),
		preempteePodsTracker: t.preempteePodsTracker.clone(),
	}
}

// RemoveWorkload removes all Pods indexed under the given Workload.
// This only changes tracked state; it does not delete Kubernetes objects.
// This operation only removes Workload's active Pods. Pods marked as
// Committed Preemption Victims persist until explicitly deleted.
func (t *Tracker) RemoveWorkload(wlKey client.ObjectKey) {
	t.mu.Lock()
	defer t.mu.Unlock()

	t.activePodsTracker.removeWorkload(wlKey)
}

// Track adds or replaces a Pod by namespace and name, updating its Workload
// index. The input is deep-copied and may be modified by the caller afterward.
// A nil Pod is ignored. Lifecycle and capacity filtering belong to the caller.
// Returns a pointer to the previous version of the Pod as identified by the ObjectKey.
// If the previous version of the Pod was a Committed Preemption Victim:
//   - if the new Pod version represents the same object (matching UID): the Pod is updated in place,
//   - if the input Pod is a new object (different UID): the old Pod version is purged
//     and the new version placed as a standard Pod (not a committed victim).
func (t *Tracker) Track(pod *corev1.Pod) (oldPod *corev1.Pod) {
	t.mu.Lock()
	defer t.mu.Unlock()

	if pod == nil {
		return nil
	}
	pod = pod.DeepCopy()
	key := client.ObjectKeyFromObject(pod)

	var currentTracker, targetTracker *tracker

	targetTracker = t.activePodsTracker
	if preemptee, isPreemptee := t.preempteePodsTracker.pods[key]; isPreemptee {
		oldPod = preemptee
		currentTracker = t.preempteePodsTracker
		if preemptee.UID == pod.UID {
			targetTracker = t.preempteePodsTracker
		}
	}
	if active, isActive := t.activePodsTracker.pods[key]; isActive {
		oldPod = active
		currentTracker = t.activePodsTracker
	}

	if currentTracker != nil {
		currentTracker.clearPod(key, oldPod)
	}
	targetTracker.trackOrUpdate(key, pod)
	return
}

func (t *tracker) trackOrUpdate(key client.ObjectKey, pod *corev1.Pod) {
	if t.pods == nil {
		t.pods = make(podsByKey)
	}
	t.pods[key] = pod

	wl := pod.Annotations[kueue.WorkloadAnnotation]
	if wl != "" {
		if t.workloadPods == nil {
			t.workloadPods = make(podsByWorkload)
		}
		wlKey := client.ObjectKey{Namespace: pod.Namespace, Name: wl}
		t.workloadPods.recordPod(wlKey, key, pod)
	}
}

// Untrack removes a Pod and returns its last observed node name, or an empty
// string if the Pod was not tracked or had no assigned node.
// Untracking a pod removes it from the Tracker,
// regardless of whether it is an active Pod or a Committed Preemption Victim.
func (t *Tracker) Untrack(key client.ObjectKey) string {
	t.mu.Lock()
	defer t.mu.Unlock()

	if node := t.activePodsTracker.untrack(key); node != "" {
		return node
	}
	if node := t.preempteePodsTracker.untrack(key); node != "" {
		return node
	}
	return ""
}

// MarkPodsPreemption marks the Pods currently reporting under the Workload as
// Committed Preemption Victims. This operation is not reversible - once a Pod
// is considered a victim, it will remain so until deleted.
// This only changes tracked state; it does not affect real Kubernetes objects.
func (t *Tracker) MarkPodsPreemption(wlKey client.ObjectKey) {
	t.mu.Lock()
	defer t.mu.Unlock()

	transferPods(t.preempteePodsTracker, t.activePodsTracker, wlKey)
}

func (t *tracker) clone() *tracker {
	copyTracker := &tracker{
		pods:         maps.Clone(t.pods),
		workloadPods: make(podsByWorkload, len(t.workloadPods)),
	}
	for k, v := range t.workloadPods {
		copyTracker.workloadPods[k] = maps.Clone(v)
	}
	return copyTracker
}

func (t *tracker) removeWorkload(wlKey client.ObjectKey) {
	if pods, ok := t.workloadPods[wlKey]; ok {
		for podKey := range pods {
			delete(t.pods, podKey)
		}
		delete(t.workloadPods, wlKey)
	}
}

func (t *tracker) untrack(key client.ObjectKey) string {
	if pod, ok := t.pods[key]; ok {
		t.clearPod(key, pod)
		return pod.Spec.NodeName
	}
	return ""
}

func (t *tracker) clearPod(podKey client.ObjectKey, pod *corev1.Pod) {
	delete(t.pods, podKey)

	wl := pod.Annotations[kueue.WorkloadAnnotation]
	if wl != "" {
		wlKey := client.ObjectKey{Namespace: pod.Namespace, Name: wl}
		t.workloadPods.forgetPod(wlKey, podKey)
	}
}

func (t *tracker) copyPods(wlKey client.ObjectKey, pods podsByKey) {
	maps.Copy(t.pods, pods)
	if _, ok := t.workloadPods[wlKey]; !ok {
		t.workloadPods[wlKey] = make(podsByKey)
	}
	maps.Copy(t.workloadPods[wlKey], pods)
}

func transferPods(dst, src *tracker, wlKey client.ObjectKey) {
	pods, hasPods := src.workloadPods[wlKey]
	if !hasPods {
		return
	}
	dst.copyPods(wlKey, pods)
	src.removeWorkload(wlKey)
}
