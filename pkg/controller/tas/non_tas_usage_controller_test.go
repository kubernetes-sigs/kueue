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

package tas

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/event"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	testingpod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
)

func TestBelongsToNonTASCache(t *testing.T) {
	tests := []struct {
		name string
		pod  *corev1.Pod
		want bool
	}{
		{
			name: "nil pod",
			pod:  nil,
			want: false,
		},
		{
			name: "scheduled non-TAS pod",
			pod:  testingpod.MakePod("pod", "ns").NodeName("node-a").Obj(),
			want: true,
		},
		{
			name: "unscheduled pod",
			pod:  testingpod.MakePod("pod", "ns").Obj(),
			want: false,
		},
		{
			name: "scheduled TAS pod",
			pod: testingpod.MakePod("pod", "ns").
				NodeName("node-a").
				Annotation(kueue.PodSetRequiredTopologyAnnotation, "rack").
				Obj(),
			want: false,
		},
		{
			name: "scheduled succeeded non-TAS pod",
			pod:  testingpod.MakePod("pod", "ns").NodeName("node-a").StatusPhase(corev1.PodSucceeded).Obj(),
			want: false,
		},
		{
			name: "scheduled failed non-TAS pod",
			pod:  testingpod.MakePod("pod", "ns").NodeName("node-a").StatusPhase(corev1.PodFailed).Obj(),
			want: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := belongsToNonTASCache(tc.pod); got != tc.want {
				t.Errorf("belongsToNonTASCache() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestNonTasUsageReconcilerCreateDelete(t *testing.T) {
	reconciler := &NonTasUsageReconciler{}

	tests := []struct {
		name string
		pod  *corev1.Pod
		want bool
	}{
		{
			name: "scheduled non-TAS pod",
			pod:  testingpod.MakePod("pod", "ns").NodeName("node-a").Obj(),
			want: true,
		},
		{
			name: "unscheduled pod",
			pod:  testingpod.MakePod("pod", "ns").Obj(),
			want: false,
		},
		{
			name: "scheduled TAS pod",
			pod: testingpod.MakePod("pod", "ns").
				NodeName("node-a").
				Annotation(kueue.PodSetRequiredTopologyAnnotation, "rack").
				Obj(),
			want: false,
		},
		{
			name: "terminated scheduled non-TAS pod",
			pod:  testingpod.MakePod("pod", "ns").NodeName("node-a").StatusPhase(corev1.PodSucceeded).Obj(),
			want: false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if got := reconciler.Create(event.TypedCreateEvent[*corev1.Pod]{Object: tc.pod}); got != tc.want {
				t.Errorf("Create() = %v, want %v", got, tc.want)
			}
			if got := reconciler.Delete(event.TypedDeleteEvent[*corev1.Pod]{Object: tc.pod}); got != tc.want {
				t.Errorf("Delete() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestShouldReconcilePodUpdate(t *testing.T) {
	tests := []struct {
		name   string
		oldPod *corev1.Pod
		newPod *corev1.Pod
		want   bool
	}{
		{
			name:   "ignores status-only churn for scheduled non-TAS pod",
			oldPod: testingpod.MakePod("pod", "ns").NodeName("node-a").StatusPhase(corev1.PodPending).Obj(),
			newPod: testingpod.MakePod("pod", "ns").NodeName("node-a").StatusPhase(corev1.PodRunning).Obj(),
			want:   false,
		},
		{
			name:   "reconciles when non-TAS pod gets scheduled",
			oldPod: testingpod.MakePod("pod", "ns").StatusPhase(corev1.PodPending).Obj(),
			newPod: testingpod.MakePod("pod", "ns").NodeName("node-a").StatusPhase(corev1.PodPending).Obj(),
			want:   true,
		},
		{
			name:   "reconciles when scheduled non-TAS pod terminates",
			oldPod: testingpod.MakePod("pod", "ns").NodeName("node-a").StatusPhase(corev1.PodRunning).Obj(),
			newPod: testingpod.MakePod("pod", "ns").NodeName("node-a").StatusPhase(corev1.PodSucceeded).Obj(),
			want:   true,
		},
		{
			name: "reconciles when scheduled non-TAS pod becomes TAS",
			oldPod: testingpod.MakePod("pod", "ns").
				NodeName("node-a").
				StatusPhase(corev1.PodRunning).
				Obj(),
			newPod: testingpod.MakePod("pod", "ns").
				NodeName("node-a").
				Annotation(kueue.PodSetRequiredTopologyAnnotation, "rack").
				StatusPhase(corev1.PodRunning).
				Obj(),
			want: true,
		},
		{
			name:   "reconciles when scheduled non-TAS pod becomes unscheduled",
			oldPod: testingpod.MakePod("pod", "ns").NodeName("node-a").StatusPhase(corev1.PodRunning).Obj(),
			newPod: testingpod.MakePod("pod", "ns").StatusPhase(corev1.PodRunning).Obj(),
			want:   true,
		},
		{
			name: "reconciles when TAS pod becomes scheduled non-TAS",
			oldPod: testingpod.MakePod("pod", "ns").
				NodeName("node-a").
				Annotation(kueue.PodSetRequiredTopologyAnnotation, "rack").
				StatusPhase(corev1.PodRunning).
				Obj(),
			newPod: testingpod.MakePod("pod", "ns").
				NodeName("node-a").
				StatusPhase(corev1.PodRunning).
				Obj(),
			want: true,
		},
		{
			name: "ignores TAS pod status-only churn",
			oldPod: testingpod.MakePod("pod", "ns").
				NodeName("node-a").
				Annotation(kueue.PodSetRequiredTopologyAnnotation, "rack").
				StatusPhase(corev1.PodPending).
				Obj(),
			newPod: testingpod.MakePod("pod", "ns").
				NodeName("node-a").
				Annotation(kueue.PodSetRequiredTopologyAnnotation, "rack").
				StatusPhase(corev1.PodRunning).
				Obj(),
			want: false,
		},
		{
			name:   "ignores unscheduled non-TAS update",
			oldPod: testingpod.MakePod("pod", "ns").StatusPhase(corev1.PodPending).Obj(),
			newPod: testingpod.MakePod("pod", "ns").StatusPhase(corev1.PodPending).Obj(),
			want:   false,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := shouldReconcilePodUpdate(tc.oldPod, tc.newPod)
			if got != tc.want {
				t.Errorf("shouldReconcilePodUpdate() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestNonTasUsageReconcilerUpdate(t *testing.T) {
	reconciler := &NonTasUsageReconciler{}
	oldPod := testingpod.MakePod("pod", "ns").NodeName("node-a").StatusPhase(corev1.PodRunning).Obj()
	newPod := testingpod.MakePod("pod", "ns").NodeName("node-a").StatusPhase(corev1.PodSucceeded).Obj()

	got := reconciler.Update(event.TypedUpdateEvent[*corev1.Pod]{
		ObjectOld: oldPod,
		ObjectNew: newPod,
	})
	if !got {
		t.Errorf("Update() = %v, want %v", got, true)
	}
}
