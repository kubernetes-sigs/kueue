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

package podtracker

import (
	"fmt"
	"sync"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	testingpod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
)

func TestWorkloadMapping(t *testing.T) {
	basicPod := testingpod.MakePod("pod", "ns").Annotation(kueue.WorkloadAnnotation, "wl").Obj()

	testCases := map[string]struct {
		operation func(*Tracker)
		want      podsByWorkload
	}{
		"add pod with workload annotation": {
			operation: func(tracker *Tracker) {
				tracker.Track(basicPod)
			},
			want: podsByWorkload{
				types.NamespacedName{Namespace: "ns", Name: "wl"}: podsByKey{
					types.NamespacedName{Namespace: "ns", Name: "pod"}: basicPod,
				},
			},
		},
		"remove pod": {
			operation: func(tracker *Tracker) {
				tracker.Track(testingpod.MakePod("pod1", "ns").Annotation(kueue.WorkloadAnnotation, "wl1").Obj())
				tracker.Track(testingpod.MakePod("pod2", "ns").Annotation(kueue.WorkloadAnnotation, "wl1").Obj())
				tracker.Track(testingpod.MakePod("pod3", "ns").Annotation(kueue.WorkloadAnnotation, "wl2").Obj())
				tracker.Track(testingpod.MakePod("pod4", "ns").Annotation(kueue.WorkloadAnnotation, "wl2").Obj())
				tracker.Untrack(types.NamespacedName{Namespace: "ns", Name: "pod1"})
			},
			want: podsByWorkload{
				types.NamespacedName{Namespace: "ns", Name: "wl1"}: podsByKey{
					types.NamespacedName{Namespace: "ns", Name: "pod2"}: testingpod.MakePod("pod2", "ns").Annotation(kueue.WorkloadAnnotation, "wl1").Obj(),
				},
				types.NamespacedName{Namespace: "ns", Name: "wl2"}: podsByKey{
					types.NamespacedName{Namespace: "ns", Name: "pod3"}: testingpod.MakePod("pod3", "ns").Annotation(kueue.WorkloadAnnotation, "wl2").Obj(),
					types.NamespacedName{Namespace: "ns", Name: "pod4"}: testingpod.MakePod("pod4", "ns").Annotation(kueue.WorkloadAnnotation, "wl2").Obj(),
				},
			},
		},
		"remove all pods": {
			operation: func(tracker *Tracker) {
				tracker.Track(testingpod.MakePod("pod1", "ns").Annotation(kueue.WorkloadAnnotation, "wl1").Obj())
				tracker.Track(testingpod.MakePod("pod2", "ns").Annotation(kueue.WorkloadAnnotation, "wl1").Obj())
				tracker.Track(testingpod.MakePod("pod3", "ns").Annotation(kueue.WorkloadAnnotation, "wl2").Obj())
				tracker.Untrack(types.NamespacedName{Namespace: "ns", Name: "pod1"})
				tracker.Untrack(types.NamespacedName{Namespace: "ns", Name: "pod2"})
				tracker.Untrack(types.NamespacedName{Namespace: "ns", Name: "pod3"})
			},
			want: podsByWorkload{},
		},
		"update pod workload annotation": {
			operation: func(tracker *Tracker) {
				tracker.Track(testingpod.MakePod("pod1", "ns").Annotation(kueue.WorkloadAnnotation, "wl1").Obj())
				tracker.Track(testingpod.MakePod("pod1", "ns").Annotation(kueue.WorkloadAnnotation, "wl2").Obj())
			},
			want: podsByWorkload{
				types.NamespacedName{Namespace: "ns", Name: "wl2"}: podsByKey{
					types.NamespacedName{Namespace: "ns", Name: "pod1"}: testingpod.MakePod("pod1", "ns").Annotation(kueue.WorkloadAnnotation, "wl2").Obj(),
				},
			},
		},
		"update unassigned pod to have workload annotation": {
			operation: func(tracker *Tracker) {
				tracker.Track(testingpod.MakePod("pod1", "ns").Annotation("", "").Obj())
				tracker.Track(testingpod.MakePod("pod1", "ns").Annotation(kueue.WorkloadAnnotation, "wl1").Obj())
			},
			want: podsByWorkload{
				types.NamespacedName{Namespace: "ns", Name: "wl1"}: podsByKey{
					types.NamespacedName{Namespace: "ns", Name: "pod1"}: testingpod.MakePod("pod1", "ns").Annotation(kueue.WorkloadAnnotation, "wl1").Obj(),
				},
			},
		},
		"update pod from workload annotation to unassigned": {
			operation: func(tracker *Tracker) {
				tracker.Track(testingpod.MakePod("pod1", "ns").Annotation(kueue.WorkloadAnnotation, "wl1").Obj())
				tracker.Track(testingpod.MakePod("pod1", "ns").Annotation("", "").Obj())
			},
			want: podsByWorkload{},
		},
		"add pod with empty workload annotation": {
			operation: func(tracker *Tracker) {
				tracker.Track(testingpod.MakePod("pod1", "ns").Annotation(kueue.WorkloadAnnotation, "").Obj())
			},
			want: podsByWorkload{},
		},
		"nil pod": {
			operation: func(tracker *Tracker) {
				tracker.Track(nil)
			},
			want: podsByWorkload{},
		},
		"workload and pod names are scoped to their namespace": {
			operation: func(tracker *Tracker) {
				tracker.Track(basicPod)
				tracker.Track(testingpod.MakePod("pod", "other").Annotation(kueue.WorkloadAnnotation, "wl").Obj())
			},
			want: podsByWorkload{
				{Namespace: "ns", Name: "wl"}: {
					{Namespace: "ns", Name: "pod"}: basicPod,
				},
				{Namespace: "other", Name: "wl"}: {
					{Namespace: "other", Name: "pod"}: testingpod.MakePod("pod", "other").Annotation(kueue.WorkloadAnnotation, "wl").Obj(),
				},
			},
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			tracker := NewTracker()
			tc.operation(tracker)

			if diff := cmp.Diff(tc.want, tracker.activePodsTracker.workloadPods, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("Unexpected pod assignments (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestTrackerClone(t *testing.T) {
	pod := testingpod.MakePod("pod", "ns").NodeName("node").Annotation(kueue.WorkloadAnnotation, "wl").Obj()
	otherPod := testingpod.MakePod("other", "ns").Annotation(kueue.WorkloadAnnotation, "wl").Obj()
	unmanagedPod := testingpod.MakePod("unmanaged", "ns").Obj()
	wlKey := types.NamespacedName{Namespace: "ns", Name: "wl"}
	podKey := types.NamespacedName{Namespace: "ns", Name: "pod"}
	cases := map[string]struct {
		initialPods []*corev1.Pod
		operation   func(*Tracker)
		wantPods    []*corev1.Pod
		wantWLPods  []*corev1.Pod
	}{
		"clone of an empty tracker can track pods": {
			operation:  func(tracker *Tracker) { tracker.Track(pod) },
			wantPods:   []*corev1.Pod{pod},
			wantWLPods: []*corev1.Pod{pod},
		},
		"untracking a pod leaves other workload members": {
			initialPods: []*corev1.Pod{pod, otherPod, unmanagedPod},
			operation:   func(tracker *Tracker) { tracker.Untrack(podKey) },
			wantPods:    []*corev1.Pod{otherPod, unmanagedPod},
			wantWLPods:  []*corev1.Pod{otherPod},
		},
		"removing a workload preserves unmanaged pods": {
			initialPods: []*corev1.Pod{pod, otherPod, unmanagedPod},
			operation:   func(tracker *Tracker) { tracker.RemoveWorkload(wlKey) },
			wantPods:    []*corev1.Pod{unmanagedPod},
		},
		"removing an unknown workload preserves pods": {
			initialPods: []*corev1.Pod{unmanagedPod},
			operation:   func(tracker *Tracker) { tracker.RemoveWorkload(wlKey) },
			wantPods:    []*corev1.Pod{unmanagedPod},
		},
		"same-name pod replacement refreshes the identity and index": {
			initialPods: []*corev1.Pod{pod},
			operation: func(tracker *Tracker) {
				tracker.Track(testingpod.MakePod("pod", "ns").UID("replacement").NodeName("other-node").Obj())
			},
			wantPods: []*corev1.Pod{testingpod.MakePod("pod", "ns").UID("replacement").NodeName("other-node").Obj()},
		},
	}
	cmpOpts := cmp.Options{cmpopts.EquateEmpty(), cmpopts.SortSlices(func(a, b *corev1.Pod) bool {
		return a.Namespace+"/"+a.Name < b.Namespace+"/"+b.Name
	})}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			tracker := NewTracker()
			for _, pod := range tc.initialPods {
				tracker.Track(pod)
			}
			wantOriginalWLPods := tracker.PodsForWorkload(wlKey)
			clone := tracker.Clone()
			tc.operation(clone)
			if diff := cmp.Diff(tc.wantPods, clone.Pods(), cmpOpts...); diff != "" {
				t.Errorf("Unexpected cloned pods (-want,+got):\n%s", diff)
			}
			if diff := cmp.Diff(tc.wantWLPods, clone.PodsForWorkload(wlKey), cmpOpts...); diff != "" {
				t.Errorf("Unexpected cloned workload pods (-want,+got):\n%s", diff)
			}
			if diff := cmp.Diff(tc.initialPods, tracker.Pods(), cmpOpts...); diff != "" {
				t.Errorf("Clone mutation changed original pods (-want,+got):\n%s", diff)
			}
			if diff := cmp.Diff(wantOriginalWLPods, tracker.PodsForWorkload(wlKey), cmpOpts...); diff != "" {
				t.Errorf("Clone mutation changed original workload index (-want,+got):\n%s", diff)
			}
			// Apply the same change to the source and check that its old snapshot
			// keeps both its Pod map and Workload index intact.
			snapshot := tracker.Clone()
			wantWLPods := snapshot.PodsForWorkload(wlKey)
			tc.operation(tracker)
			if diff := cmp.Diff(tc.initialPods, snapshot.Pods(), cmpOpts...); diff != "" {
				t.Errorf("Original mutation changed snapshot pods (-want,+got):\n%s", diff)
			}
			if diff := cmp.Diff(wantWLPods, snapshot.PodsForWorkload(wlKey), cmpOpts...); diff != "" {
				t.Errorf("Original mutation changed snapshot workload index (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestTrackerConcurrentAccess(t *testing.T) {
	tracker := NewTracker()
	var workers sync.WaitGroup
	for i := range 8 {
		workers.Go(func() {
			wlKey := types.NamespacedName{Namespace: "ns", Name: fmt.Sprintf("wl-%d", i)}
			pod := testingpod.MakePod(fmt.Sprintf("pod-%d", i), "ns").Annotation(kueue.WorkloadAnnotation, wlKey.Name).Obj()
			for range 20 {
				tracker.Track(pod)
				tracker.Pods()
				tracker.PodsForWorkload(wlKey)
				tracker.Clone().RemoveWorkload(wlKey)
				tracker.Untrack(types.NamespacedName{Namespace: pod.Namespace, Name: pod.Name})
				tracker.Track(pod)
				tracker.RemoveWorkload(wlKey)
			}
		})
	}
	workers.Wait()
	if pods := tracker.Pods(); len(pods) != 0 {
		t.Errorf("Expected no remaining pods, got %v", pods)
	}
}

func TestTrackPodDeepCopy(t *testing.T) {
	tracker := NewTracker()

	pod := testingpod.MakePod("pod1", "ns").Annotation(kueue.WorkloadAnnotation, "wl1").Obj()
	tracker.Track(pod)

	// Mutate the pod object that was passed into TrackPod
	pod.Annotations[kueue.WorkloadAnnotation] = "mutated-wl"

	want := podsByWorkload{
		types.NamespacedName{Namespace: "ns", Name: "wl1"}: podsByKey{
			types.NamespacedName{Namespace: "ns", Name: "pod1"}: testingpod.MakePod("pod1", "ns").Annotation(kueue.WorkloadAnnotation, "wl1").Obj(),
		},
	}

	if diff := cmp.Diff(want, tracker.activePodsTracker.workloadPods); diff != "" {
		t.Errorf("TrackPod did not deep copy pod (-want,+got):\n%s", diff)
	}
}

func TestUntrackPodReturnsNodeName(t *testing.T) {
	tracker := NewTracker()

	podKey := types.NamespacedName{Namespace: "ns", Name: "pod1"}
	pod := testingpod.MakePod("pod1", "ns").NodeName("node-a").Annotation(kueue.WorkloadAnnotation, "wl1").Obj()
	tracker.Track(pod)

	if got := tracker.Untrack(podKey); got != "node-a" {
		t.Errorf("UntrackPod() = %q, want %q", got, "node-a")
	}
	if got := tracker.Untrack(podKey); got != "" {
		t.Errorf("second UntrackPod() = %q, want empty string", got)
	}
}
