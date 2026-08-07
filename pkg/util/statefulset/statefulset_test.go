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

package statefulset

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	podconstants "sigs.k8s.io/kueue/pkg/controller/jobs/pod/constants"
)

func TestUngatePod(t *testing.T) {
	currentStatefulSet := &appsv1.StatefulSet{
		Status: appsv1.StatefulSetStatus{
			CurrentRevision: "current",
			UpdateRevision:  "current",
		},
	}
	updatingStatefulSet := currentStatefulSet.DeepCopy()
	updatingStatefulSet.Status.UpdateRevision = "update"

	testCases := map[string]struct {
		statefulSet *appsv1.StatefulSet
		pod         *corev1.Pod
		force       bool
		wantChanged bool
		wantGates   []corev1.PodSchedulingGate
	}{
		"current revision is unchanged": {
			statefulSet: currentStatefulSet,
			pod:         podWithGates("current", podconstants.SchedulingGateName),
			wantGates:   []corev1.PodSchedulingGate{{Name: podconstants.SchedulingGateName}},
		},
		"current revision during rollout is ungated": {
			statefulSet: updatingStatefulSet,
			pod:         podWithGates("current", podconstants.SchedulingGateName),
			wantChanged: true,
		},
		"current revision topology gate is removed during rollout": {
			statefulSet: updatingStatefulSet,
			pod:         podWithGates("current", kueue.TopologySchedulingGate),
			wantChanged: true,
		},
		"all current revision gates are removed during rollout": {
			statefulSet: updatingStatefulSet,
			pod: podWithGates("current",
				podconstants.SchedulingGateName,
				kueue.TopologySchedulingGate,
			),
			wantChanged: true,
		},
		"force ungates current revision": {
			statefulSet: currentStatefulSet,
			pod:         podWithGates("current", podconstants.SchedulingGateName),
			force:       true,
			wantChanged: true,
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			if changed := UngatePod(tc.statefulSet, tc.pod, tc.force); changed != tc.wantChanged {
				t.Errorf("UngatePod() changed = %t, want %t", changed, tc.wantChanged)
			}
			if diff := cmp.Diff(tc.wantGates, tc.pod.Spec.SchedulingGates, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("SchedulingGates after UngatePod() (-want,+got):\n%s", diff)
			}
			if diff := cmp.Diff([]string{podconstants.PodFinalizer}, tc.pod.Finalizers); diff != "" {
				t.Errorf("Finalizers after UngatePod() (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestFinalizePod(t *testing.T) {
	deletionTimestamp := metav1.Now()
	testCases := map[string]struct {
		pod            *corev1.Pod
		wantChanged    bool
		wantFinalizers []string
	}{
		"active Pod keeps the legacy finalizer": {
			pod: &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
				Finalizers: []string{podconstants.PodFinalizer},
			}},
			wantFinalizers: []string{podconstants.PodFinalizer},
		},
		"succeeded Pod removes the legacy finalizer": {
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{Finalizers: []string{podconstants.PodFinalizer}},
				Status:     corev1.PodStatus{Phase: corev1.PodSucceeded},
			},
			wantChanged: true,
		},
		"failed Pod removes the legacy finalizer": {
			pod: &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{Finalizers: []string{podconstants.PodFinalizer}},
				Status:     corev1.PodStatus{Phase: corev1.PodFailed},
			},
			wantChanged: true,
		},
		"deleting Pod removes the legacy finalizer": {
			pod: &corev1.Pod{ObjectMeta: metav1.ObjectMeta{
				DeletionTimestamp: &deletionTimestamp,
				Finalizers:        []string{podconstants.PodFinalizer},
			}},
			wantChanged: true,
		},
		"terminal Pod without the legacy finalizer is unchanged": {
			pod: &corev1.Pod{Status: corev1.PodStatus{Phase: corev1.PodSucceeded}},
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			if changed := FinalizePod(tc.pod); changed != tc.wantChanged {
				t.Errorf("FinalizePod() changed = %t, want %t", changed, tc.wantChanged)
			}
			if diff := cmp.Diff(tc.wantFinalizers, tc.pod.Finalizers, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("Finalizers after FinalizePod() (-want,+got):\n%s", diff)
			}
		})
	}
}

func podWithGates(revision string, gates ...string) *corev1.Pod {
	pod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Labels: map[string]string{
				appsv1.ControllerRevisionHashLabelKey: revision,
			},
			Finalizers: []string{podconstants.PodFinalizer},
		},
	}
	for _, gate := range gates {
		pod.Spec.SchedulingGates = append(pod.Spec.SchedulingGates, corev1.PodSchedulingGate{Name: gate})
	}
	return pod
}
