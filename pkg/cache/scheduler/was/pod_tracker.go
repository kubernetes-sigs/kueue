//go:build !exclude_scheduler_library

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

package was

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

// podTracker maintains pod state for scheduler plugins that need
// existing pod information.
type podTracker struct {
	sync.RWMutex
	pods         podsByKey
	workloadPods podsByWorkload
}

func (m podsByWorkload) getPodsForWorkload(wlKey client.ObjectKey) []*corev1.Pod {
	if len(m) == 0 {
		return nil
	}
	if podSet, ok := m[wlKey]; ok {
		return slices.Collect(maps.Values(podSet))
	}
	return nil
}

func (m podsByWorkload) recordPod(wlKey client.ObjectKey, podKey client.ObjectKey, pod *corev1.Pod) {
	if m == nil {
		return
	}
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

func (t *podTracker) snapshot() (allPods []*corev1.Pod, workloadPods podsByWorkload) {
	t.RLock()
	defer t.RUnlock()

	allPods = slices.Collect(maps.Values(t.pods))
	workloadPods = podsByWorkload{}
	for k, v := range t.workloadPods {
		workloadPods[k] = maps.Clone(v)
	}
	return
}

func (t *podTracker) track(pod *corev1.Pod) {
	t.Lock()
	defer t.Unlock()

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

func (t *podTracker) untrack(key client.ObjectKey) {
	t.Lock()
	defer t.Unlock()

	if pod, ok := t.pods[key]; ok {
		t.clearPod(key, pod)
	}
}

func (t *podTracker) clearPod(podKey client.ObjectKey, pod *corev1.Pod) {
	delete(t.pods, podKey)

	wl := pod.Annotations[kueue.WorkloadAnnotation]
	if wl != "" {
		wlKey := client.ObjectKey{Namespace: pod.Namespace, Name: wl}
		t.workloadPods.forgetPod(wlKey, podKey)
	}
}

func (t *podTracker) savePod(podKey client.ObjectKey, pod *corev1.Pod) {
	t.pods[podKey] = pod

	wl := pod.Annotations[kueue.WorkloadAnnotation]
	if wl != "" {
		wlKey := client.ObjectKey{Namespace: pod.Namespace, Name: wl}
		t.workloadPods.recordPod(wlKey, podKey, pod)
	}
}
