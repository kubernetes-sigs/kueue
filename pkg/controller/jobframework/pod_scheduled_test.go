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

package jobframework

import (
	"context"
	"errors"
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	testingclock "k8s.io/utils/clock/testing"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
)

func TestHasPodScheduledTrue(t *testing.T) {
	t.Parallel()
	cases := map[string]struct {
		conds []corev1.PodCondition
		want  bool
	}{
		"scheduled": {
			conds: []corev1.PodCondition{{Type: corev1.PodScheduled, Status: corev1.ConditionTrue}},
			want:  true,
		},
		"unscheduled": {
			conds: []corev1.PodCondition{{Type: corev1.PodScheduled, Status: corev1.ConditionFalse, Reason: "Unschedulable"}},
			want:  false,
		},
		"missing condition": {
			want: false,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if got := HasPodScheduledTrue(tc.conds); got != tc.want {
				t.Fatalf("HasPodScheduledTrue() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestAllListedPodsScheduled(t *testing.T) {
	t.Parallel()
	pod := func(scheduled bool) corev1.Pod {
		status := corev1.ConditionFalse
		if scheduled {
			status = corev1.ConditionTrue
		}
		return corev1.Pod{
			Status: corev1.PodStatus{
				Conditions: []corev1.PodCondition{{Type: corev1.PodScheduled, Status: status}},
			},
		}
	}
	cases := map[string]struct {
		pods     []corev1.Pod
		minCount int
		want     bool
	}{
		"all scheduled": {
			pods:     []corev1.Pod{pod(true), pod(true)},
			minCount: 2,
			want:     true,
		},
		"one unscheduled": {
			pods:     []corev1.Pod{pod(true), pod(false)},
			minCount: 2,
			want:     false,
		},
		"not enough pods": {
			pods:     []corev1.Pod{pod(true)},
			minCount: 2,
			want:     false,
		},
		"ignores terminating pods for count": {
			pods: func() []corev1.Pod {
				terminating := pod(true)
				now := metav1.Now()
				terminating.DeletionTimestamp = &now
				return []corev1.Pod{terminating, pod(true)}
			}(),
			minCount: 2,
			want:     false,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if got := AllListedPodsScheduled(tc.pods, tc.minCount); got != tc.want {
				t.Fatalf("AllListedPodsScheduled() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestPodsScheduledBySelector(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	pod := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "p1",
			Namespace: "ns",
			Labels:    map[string]string{"job": "j1"},
		},
		Status: corev1.PodStatus{
			Conditions: []corev1.PodCondition{{Type: corev1.PodScheduled, Status: corev1.ConditionTrue}},
		},
	}
	cl := fake.NewClientBuilder().WithObjects(&pod).Build()

	got, err := PodsScheduledBySelector(ctx, cl, "ns", "", 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got {
		t.Fatalf("empty selector should return false, got true")
	}
	got, err = PodsScheduledBySelector(ctx, cl, "ns", "job=j1", 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if !got {
		t.Fatalf("expected scheduled pod, got false")
	}
	got, err = PodsScheduledBySelector(ctx, cl, "ns", "job=missing", 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got {
		t.Fatalf("expected missing selector to return false, got true")
	}
}

func TestPodsScheduledBySelectorUnscheduledPod(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	pod := corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "p1",
			Namespace: "ns",
			Labels:    map[string]string{"job": "j1"},
		},
		Status: corev1.PodStatus{
			Conditions: []corev1.PodCondition{{Type: corev1.PodScheduled, Status: corev1.ConditionFalse}},
		},
	}
	cl := fake.NewClientBuilder().WithObjects(&pod).Build()
	got, err := PodsScheduledBySelector(ctx, cl, "ns", "job=j1", 1)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got {
		t.Fatalf("expected unscheduled pod to return false, got true")
	}
}

func TestPodsScheduledBySelectorInvalidSelector(t *testing.T) {
	t.Parallel()
	ctx := t.Context()
	cl := fake.NewClientBuilder().Build()
	_, err := PodsScheduledBySelector(ctx, cl, "ns", "not=a=valid", 1)
	if err == nil {
		t.Fatal("expected error for invalid selector")
	}
}

type podsScheduledErrorJob struct {
	podsReadyTestJob
	scheduledErr error
}

func (j *podsScheduledErrorJob) PodsScheduled(context.Context, client.Client) (bool, error) {
	return false, j.scheduledErr
}

func TestGeneratePodsReadyConditionPodsScheduledError(t *testing.T) {
	t.Parallel()
	now := time.Now()
	clock := testingclock.NewFakeClock(now)
	ctx := t.Context()
	cl := fake.NewClientBuilder().Build()

	wl := &kueue.Workload{
		ObjectMeta: metav1.ObjectMeta{Name: "wl", Namespace: "ns"},
		Status: kueue.WorkloadStatus{
			Admission: &kueue.Admission{},
			Conditions: []metav1.Condition{
				{Type: kueue.WorkloadAdmitted, Status: metav1.ConditionTrue},
			},
		},
	}

	job := &podsScheduledErrorJob{
		podsReadyTestJob: podsReadyTestJob{
			obj: &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: "job", Namespace: "ns"}},
		},
		scheduledErr: errors.New("list failed"),
	}

	cond := generatePodsReadyCondition(ctx, cl, job, wl, clock)
	if cond.Reason != kueue.WorkloadWaitForStart {
		t.Fatalf("reason = %q, want %q", cond.Reason, kueue.WorkloadWaitForStart)
	}
}

func TestGeneratePodsReadyConditionPodsScheduledErrorPreservesCondition(t *testing.T) {
	t.Parallel()
	now := time.Now()
	clock := testingclock.NewFakeClock(now)
	ctx := t.Context()
	cl := fake.NewClientBuilder().Build()

	existing := metav1.Condition{
		Type:               kueue.WorkloadPodsReady,
		Status:             metav1.ConditionFalse,
		Reason:             kueue.WorkloadWaitForStart,
		Message:            "waiting",
		LastTransitionTime: metav1.NewTime(now.Add(-time.Minute)),
	}
	wl := &kueue.Workload{
		ObjectMeta: metav1.ObjectMeta{Name: "wl", Namespace: "ns"},
		Status: kueue.WorkloadStatus{
			Admission: &kueue.Admission{},
			Conditions: []metav1.Condition{
				{Type: kueue.WorkloadAdmitted, Status: metav1.ConditionTrue},
				existing,
			},
		},
	}

	job := &podsScheduledErrorJob{
		podsReadyTestJob: podsReadyTestJob{
			obj: &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: "job", Namespace: "ns"}},
		},
		scheduledErr: errors.New("list failed"),
	}

	cond := generatePodsReadyCondition(ctx, cl, job, wl, clock)
	if cond != existing {
		t.Fatalf("condition = %+v, want %+v", cond, existing)
	}
}
