/*
Copyright 2023 The Kubernetes Authors.

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

package workload

import (
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
)

func TestSyncAdmittedCondition(t *testing.T) {
	cases := map[string]struct {
		checkStates    []kueue.AdmissionCheckState
		conditions     []metav1.Condition
		wantConditions []metav1.Condition
		wantChange     bool
	}{
		"empty": {},
		"reservation no checks": {
			conditions: []metav1.Condition{
				{
					Type:   kueue.WorkloadQuotaReserved,
					Status: metav1.ConditionTrue,
				},
			},
			wantConditions: []metav1.Condition{
				{
					Type:   kueue.WorkloadQuotaReserved,
					Status: metav1.ConditionTrue,
				},
				{
					Type:   kueue.WorkloadAdmitted,
					Status: metav1.ConditionTrue,
					Reason: "Admitted",
				},
			},
			wantChange: true,
		},
		"reservation, checks not ready": {
			checkStates: []kueue.AdmissionCheckState{
				{
					Name:  "check1",
					State: kueue.CheckStatePending,
				},
				{
					Name:  "check2",
					State: kueue.CheckStateReady,
				},
			},
			conditions: []metav1.Condition{
				{
					Type:   kueue.WorkloadQuotaReserved,
					Status: metav1.ConditionTrue,
				},
			},
			wantConditions: []metav1.Condition{
				{
					Type:   kueue.WorkloadQuotaReserved,
					Status: metav1.ConditionTrue,
				},
			},
		},
		"reservation, checks ready": {
			checkStates: []kueue.AdmissionCheckState{
				{
					Name:  "check1",
					State: kueue.CheckStateReady,
				},
				{
					Name:  "check2",
					State: kueue.CheckStateReady,
				},
			},
			conditions: []metav1.Condition{
				{
					Type:   kueue.WorkloadQuotaReserved,
					Status: metav1.ConditionTrue,
				},
			},
			wantConditions: []metav1.Condition{
				{
					Type:   kueue.WorkloadQuotaReserved,
					Status: metav1.ConditionTrue,
				},
				{
					Type:   kueue.WorkloadAdmitted,
					Status: metav1.ConditionTrue,
					Reason: "Admitted",
				},
			},
			wantChange: true,
		},
		"reservation lost": {
			checkStates: []kueue.AdmissionCheckState{
				{
					Name:  "check1",
					State: kueue.CheckStateReady,
				},
				{
					Name:  "check2",
					State: kueue.CheckStateReady,
				},
			},
			conditions: []metav1.Condition{
				{
					Type:   kueue.WorkloadAdmitted,
					Status: metav1.ConditionTrue,
				},
			},
			wantConditions: []metav1.Condition{
				{
					Type:   kueue.WorkloadAdmitted,
					Status: metav1.ConditionFalse,
					Reason: "NoReservation",
				},
			},
			wantChange: true,
		},
		"check lost": {
			checkStates: []kueue.AdmissionCheckState{
				{
					Name:  "check1",
					State: kueue.CheckStateReady,
				},
				{
					Name:  "check2",
					State: kueue.CheckStatePending,
				},
			},
			conditions: []metav1.Condition{
				{
					Type:   kueue.WorkloadQuotaReserved,
					Status: metav1.ConditionTrue,
				},
				{
					Type:   kueue.WorkloadAdmitted,
					Status: metav1.ConditionTrue,
				},
			},
			wantConditions: []metav1.Condition{
				{
					Type:   kueue.WorkloadQuotaReserved,
					Status: metav1.ConditionTrue,
				},
				{
					Type:   kueue.WorkloadAdmitted,
					Status: metav1.ConditionFalse,
					Reason: "NoChecks",
				},
			},
			wantChange: true,
		},
		"reservation and check lost": {
			checkStates: []kueue.AdmissionCheckState{
				{
					Name:  "check1",
					State: kueue.CheckStateReady,
				},
				{
					Name:  "check2",
					State: kueue.CheckStatePending,
				},
			},
			conditions: []metav1.Condition{
				{
					Type:   kueue.WorkloadAdmitted,
					Status: metav1.ConditionTrue,
				},
			},
			wantConditions: []metav1.Condition{
				{
					Type:   kueue.WorkloadAdmitted,
					Status: metav1.ConditionFalse,
					Reason: "NoReservationNoChecks",
				},
			},
			wantChange: true,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			wl := &kueue.Workload{
				Status: kueue.WorkloadStatus{
					AdmissionChecks: tc.checkStates,
					Conditions:      tc.conditions,
				},
			}

			gotChange := SyncAdmittedCondition(wl)

			if gotChange != tc.wantChange {
				t.Errorf("Unexpected change status, expecting %v", tc.wantChange)
			}

			if diff := cmp.Diff(tc.wantConditions, wl.Status.Conditions, cmpopts.IgnoreFields(metav1.Condition{}, "LastTransitionTime", "Message")); diff != "" {
				t.Errorf("Unexpected conditions after sync (- want/+ got):\n%s", diff)
			}
		})
	}
}

func TestSetCheckState(t *testing.T) {
	t0 := metav1.NewTime(time.Now().Add(-5 * time.Second))
	t1 := metav1.NewTime(time.Now())
	ps1Updates := kueue.PodSetUpdate{
		Name: "ps1",
		Labels: map[string]string{
			"l1": "l1v",
		},
		Annotations: map[string]string{
			"a1": "a1v",
		},
		NodeSelector: map[string]string{
			"ns1": "ms1v",
		},
		Tolerations: []corev1.Toleration{
			{
				Key:               "t1",
				Operator:          corev1.TolerationOpEqual,
				Value:             "t1v",
				Effect:            corev1.TaintEffectNoSchedule,
				TolerationSeconds: ptr.To[int64](5),
			},
		},
	}

	cases := map[string]struct {
		origStates []kueue.AdmissionCheckState
		state      kueue.AdmissionCheckState
		wantStates []kueue.AdmissionCheckState
	}{
		"add new check": {
			origStates: []kueue.AdmissionCheckState{},
			state: kueue.AdmissionCheckState{
				Name:               "check1",
				State:              kueue.CheckStatePending,
				LastTransitionTime: *t0.DeepCopy(),
				Message:            "msg1",
				PodSetUpdates:      []kueue.PodSetUpdate{*ps1Updates.DeepCopy()},
			},
			wantStates: []kueue.AdmissionCheckState{
				{
					Name:               "check1",
					State:              kueue.CheckStatePending,
					LastTransitionTime: *t0.DeepCopy(),
					Message:            "msg1",
					PodSetUpdates:      []kueue.PodSetUpdate{*ps1Updates.DeepCopy()},
				},
			},
		},
		"update check": {
			origStates: []kueue.AdmissionCheckState{
				{
					Name:               "check1",
					State:              kueue.CheckStatePending,
					LastTransitionTime: *t0.DeepCopy(),
					Message:            "msg1",
					PodSetUpdates:      nil,
				},
				{
					Name:               "check2",
					State:              kueue.CheckStatePending,
					LastTransitionTime: *t0.DeepCopy(),
					Message:            "msg1",
					PodSetUpdates:      nil,
				},
			},
			state: kueue.AdmissionCheckState{
				Name:               "check1",
				State:              kueue.CheckStateReady,
				LastTransitionTime: *t1.DeepCopy(),
				Message:            "msg2",
				PodSetUpdates:      []kueue.PodSetUpdate{*ps1Updates.DeepCopy()},
			},
			wantStates: []kueue.AdmissionCheckState{
				{
					Name:               "check1",
					State:              kueue.CheckStateReady,
					LastTransitionTime: *t1.DeepCopy(),
					Message:            "msg2",
					PodSetUpdates:      []kueue.PodSetUpdate{*ps1Updates.DeepCopy()},
				},
				{
					Name:               "check2",
					State:              kueue.CheckStatePending,
					LastTransitionTime: *t0.DeepCopy(),
					Message:            "msg1",
					PodSetUpdates:      nil,
				},
			},
		},
		"add new check, no transition tim": {
			origStates: []kueue.AdmissionCheckState{},
			state: kueue.AdmissionCheckState{
				Name:          "check1",
				State:         kueue.CheckStatePending,
				Message:       "msg1",
				PodSetUpdates: []kueue.PodSetUpdate{*ps1Updates.DeepCopy()},
			},
			wantStates: []kueue.AdmissionCheckState{
				{
					Name:          "check1",
					State:         kueue.CheckStatePending,
					Message:       "msg1",
					PodSetUpdates: []kueue.PodSetUpdate{*ps1Updates.DeepCopy()},
				},
			},
		},
		"update check, no transition time": {
			origStates: []kueue.AdmissionCheckState{
				{
					Name:               "check1",
					State:              kueue.CheckStatePending,
					LastTransitionTime: *t0.DeepCopy(),
					Message:            "msg1",
					PodSetUpdates:      nil,
				},
				{
					Name:               "check2",
					State:              kueue.CheckStatePending,
					LastTransitionTime: *t0.DeepCopy(),
					Message:            "msg1",
					PodSetUpdates:      nil,
				},
			},
			state: kueue.AdmissionCheckState{
				Name:          "check1",
				State:         kueue.CheckStateReady,
				Message:       "msg2",
				PodSetUpdates: []kueue.PodSetUpdate{*ps1Updates.DeepCopy()},
			},
			wantStates: []kueue.AdmissionCheckState{
				{
					Name:          "check1",
					State:         kueue.CheckStateReady,
					Message:       "msg2",
					PodSetUpdates: []kueue.PodSetUpdate{*ps1Updates.DeepCopy()},
				},
				{
					Name:               "check2",
					State:              kueue.CheckStatePending,
					LastTransitionTime: *t0.DeepCopy(),
					Message:            "msg1",
					PodSetUpdates:      nil,
				},
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			gotStates := tc.origStates

			SetAdmissionCheckState(&gotStates, tc.state)

			opts := []cmp.Option{}
			if tc.state.LastTransitionTime.IsZero() {
				opts = append(opts, cmpopts.IgnoreFields(kueue.AdmissionCheckState{}, "LastTransitionTime"), cmpopts.EquateApproxTime(time.Second))

				if updatedCheck := FindAdmissionCheck(gotStates, tc.state.Name); updatedCheck == nil {
					t.Error("Cannot find the updated check state")
				} else {
					if diff := cmp.Diff(metav1.NewTime(time.Now()), updatedCheck.LastTransitionTime, opts...); diff != "" {
						t.Errorf("Unexpected LastTransitionTime (- want/+ got):\n%s", diff)
					}
				}
			}

			if diff := cmp.Diff(tc.wantStates, gotStates, opts...); diff != "" {
				t.Errorf("Unexpected conditions after sync (- want/+ got):\n%s", diff)
			}
		})
	}
}
