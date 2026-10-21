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

package workload

import (
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/component-base/featuregate"
	testingclock "k8s.io/utils/clock/testing"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
)

func TestSyncAdmittedCondition(t *testing.T) {
	testTime := time.Now().Truncate(time.Second)
	cases := map[string]struct {
		featureGates map[featuregate.Feature]bool

		admission        *kueue.Admission
		checkStates      []kueue.AdmissionCheckState
		conditions       []metav1.Condition
		pastAdmittedTime int32

		wantConditions   []metav1.Condition
		wantChange       bool
		wantAdmittedTime int32
	}{
		"empty": {
			wantConditions: utiltesting.ExplicitStatusConditions(metav1.Condition{
				Type:               kueue.WorkloadAdmitted,
				Status:             metav1.ConditionFalse,
				Reason:             kueue.WorkloadAdmittedReasonNoReservation,
				ObservedGeneration: 1,
			}),
			wantChange: utiltesting.WantChangeWithExplicitStatus(false),
		},
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
					Type:               kueue.WorkloadAdmitted,
					Status:             metav1.ConditionTrue,
					Reason:             "Admitted",
					ObservedGeneration: 1,
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
			wantConditions: append([]metav1.Condition{
				{
					Type:   kueue.WorkloadQuotaReserved,
					Status: metav1.ConditionTrue,
				},
			}, utiltesting.UnadmittedConditions(metav1.Condition{
				Type:               kueue.WorkloadAdmitted,
				Status:             metav1.ConditionFalse,
				Reason:             kueue.WorkloadAdmittedReasonUnsatisfiedAdmissionChecks,
				ObservedGeneration: 1,
			},
			)...),
			wantChange: utiltesting.WantChangeWithObservability(false),
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
					Type:               kueue.WorkloadAdmitted,
					Status:             metav1.ConditionTrue,
					Reason:             "Admitted",
					ObservedGeneration: 1,
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
					Type:               kueue.WorkloadAdmitted,
					Status:             metav1.ConditionTrue,
					LastTransitionTime: metav1.NewTime(testTime.Add(-time.Second)),
				},
			},
			wantConditions: []metav1.Condition{
				{
					Type:               kueue.WorkloadAdmitted,
					Status:             metav1.ConditionFalse,
					Reason:             kueue.WorkloadAdmittedReasonNoReservation,
					ObservedGeneration: 1,
				},
			},
			wantChange:       true,
			wantAdmittedTime: 1,
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
					Type:               kueue.WorkloadAdmitted,
					Status:             metav1.ConditionTrue,
					LastTransitionTime: metav1.NewTime(testTime.Add(-time.Second)),
				},
			},
			wantConditions: []metav1.Condition{
				{
					Type:   kueue.WorkloadQuotaReserved,
					Status: metav1.ConditionTrue,
				},
				{
					Type:               kueue.WorkloadAdmitted,
					Status:             metav1.ConditionFalse,
					Reason:             UnadmittedWorkloadReasonWithFallback(kueue.WorkloadAdmittedReasonUnsatisfiedAdmissionChecks, "UnsatisfiedChecks"),
					ObservedGeneration: 1,
				},
			},
			wantChange:       true,
			wantAdmittedTime: 1,
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
					Type:               kueue.WorkloadAdmitted,
					Status:             metav1.ConditionTrue,
					LastTransitionTime: metav1.NewTime(testTime.Add(-time.Second)),
				},
			},
			wantConditions: []metav1.Condition{
				{
					Type:               kueue.WorkloadAdmitted,
					Status:             metav1.ConditionFalse,
					Reason:             UnadmittedWorkloadReasonWithFallback(kueue.WorkloadAdmittedReasonNoReservation, "NoReservationUnsatisfiedChecks"),
					ObservedGeneration: 1,
				},
			},
			wantChange: true,
		},
		"reservation lost with past admitted time (set)": {
			conditions: []metav1.Condition{
				{
					Type:               kueue.WorkloadAdmitted,
					Status:             metav1.ConditionTrue,
					LastTransitionTime: metav1.NewTime(testTime.Add(-time.Second)),
				},
			},
			wantConditions: []metav1.Condition{
				{
					Type:               kueue.WorkloadAdmitted,
					Status:             metav1.ConditionFalse,
					Reason:             kueue.WorkloadAdmittedReasonNoReservation,
					ObservedGeneration: 1,
				},
			},
			wantChange:       true,
			wantAdmittedTime: 1,
		},
		"reservation lost with past admitted time (add)": {
			conditions: []metav1.Condition{
				{
					Type:               kueue.WorkloadAdmitted,
					Status:             metav1.ConditionTrue,
					LastTransitionTime: metav1.NewTime(testTime.Add(-time.Second)),
				},
			},
			pastAdmittedTime: 1,
			wantConditions: []metav1.Condition{
				{
					Type:               kueue.WorkloadAdmitted,
					Status:             metav1.ConditionFalse,
					Reason:             kueue.WorkloadAdmittedReasonNoReservation,
					ObservedGeneration: 1,
				},
			},
			wantChange:       true,
			wantAdmittedTime: 2,
		},
		"pending delayed topology request; flip from Admitted=true": {
			featureGates: map[featuregate.Feature]bool{features.TopologyAwareScheduling: true},
			admission: &kueue.Admission{
				PodSetAssignments: []kueue.PodSetAssignment{
					{
						Name:                   kueue.DefaultPodSetName,
						Count:                  new(int32(1)),
						DelayedTopologyRequest: new(kueue.DelayedTopologyRequestStatePending),
					},
				},
			},
			checkStates: []kueue.AdmissionCheckState{
				{
					Name:  "check1",
					State: kueue.CheckStateReady,
				},
			},
			conditions: []metav1.Condition{
				{
					Type:   kueue.WorkloadAdmitted,
					Status: metav1.ConditionTrue,
				},
				{
					Type:   kueue.WorkloadQuotaReserved,
					Status: metav1.ConditionTrue,
				},
			},
			wantConditions: []metav1.Condition{
				{
					Type:               kueue.WorkloadAdmitted,
					Status:             metav1.ConditionFalse,
					Reason:             kueue.WorkloadAdmittedReasonPendingDelayedTopologyRequests,
					ObservedGeneration: 1,
				},
				{
					Type:   kueue.WorkloadQuotaReserved,
					Status: metav1.ConditionTrue,
				},
			},
			wantChange: true,
		},
		"pending delayed topology request; already Admitted=false": {
			featureGates: map[featuregate.Feature]bool{features.TopologyAwareScheduling: true},
			admission: &kueue.Admission{
				PodSetAssignments: []kueue.PodSetAssignment{
					{
						Name:                   kueue.DefaultPodSetName,
						Count:                  new(int32(1)),
						DelayedTopologyRequest: new(kueue.DelayedTopologyRequestStatePending),
					},
				},
			},
			checkStates: []kueue.AdmissionCheckState{
				{
					Name:  "check1",
					State: kueue.CheckStateReady,
				},
			},
			conditions: []metav1.Condition{
				{
					Type:   kueue.WorkloadQuotaReserved,
					Status: metav1.ConditionTrue,
				},
				{
					Type:               kueue.WorkloadAdmitted,
					Status:             metav1.ConditionFalse,
					ObservedGeneration: 1,
				},
			},
			wantConditions: []metav1.Condition{
				{
					Type:   kueue.WorkloadQuotaReserved,
					Status: metav1.ConditionTrue,
				},
				{
					Type:               kueue.WorkloadAdmitted,
					Status:             metav1.ConditionFalse,
					Reason:             UnadmittedWorkloadReasonWithFallback(kueue.WorkloadAdmittedReasonPendingDelayedTopologyRequests, ""),
					ObservedGeneration: 1,
				},
			},
			wantChange: utiltesting.WantChangeWithObservability(false),
		},
		"reservation, checks not ready with observability": {
			featureGates: map[featuregate.Feature]bool{features.UnadmittedWorkloadsObservability: true},
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
				{
					Type:               kueue.WorkloadAdmitted,
					Status:             metav1.ConditionFalse,
					Reason:             kueue.WorkloadAdmittedReasonUnsatisfiedAdmissionChecks,
					ObservedGeneration: 1,
				},
			},
			wantChange: true,
		},
		"reservation lost with observability": {
			featureGates: map[featuregate.Feature]bool{features.UnadmittedWorkloadsObservability: true},
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
					Type:               kueue.WorkloadAdmitted,
					Status:             metav1.ConditionTrue,
					LastTransitionTime: metav1.NewTime(testTime.Add(-time.Second)),
				},
			},
			wantConditions: []metav1.Condition{
				{
					Type:               kueue.WorkloadAdmitted,
					Status:             metav1.ConditionFalse,
					Reason:             kueue.WorkloadAdmittedReasonNoReservation,
					ObservedGeneration: 1,
				},
			},
			wantChange:       true,
			wantAdmittedTime: 1,
		},
		"check lost with observability": {
			featureGates: map[featuregate.Feature]bool{features.UnadmittedWorkloadsObservability: true},
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
					Type:               kueue.WorkloadAdmitted,
					Status:             metav1.ConditionTrue,
					LastTransitionTime: metav1.NewTime(testTime.Add(-time.Second)),
				},
			},
			wantConditions: []metav1.Condition{
				{
					Type:   kueue.WorkloadQuotaReserved,
					Status: metav1.ConditionTrue,
				},
				{
					Type:               kueue.WorkloadAdmitted,
					Status:             metav1.ConditionFalse,
					Reason:             kueue.WorkloadAdmittedReasonUnsatisfiedAdmissionChecks,
					ObservedGeneration: 1,
				},
			},
			wantChange:       true,
			wantAdmittedTime: 1,
		},
		"reservation and check lost with observability": {
			featureGates: map[featuregate.Feature]bool{features.UnadmittedWorkloadsObservability: true},
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
					Type:               kueue.WorkloadAdmitted,
					Status:             metav1.ConditionTrue,
					LastTransitionTime: metav1.NewTime(testTime.Add(-time.Second)),
				},
			},
			wantConditions: []metav1.Condition{
				{
					Type:               kueue.WorkloadAdmitted,
					Status:             metav1.ConditionFalse,
					Reason:             kueue.WorkloadAdmittedReasonNoReservation,
					ObservedGeneration: 1,
				},
			},
			wantChange: true,
		},
		"no reservation, no condition initially": {
			checkStates: []kueue.AdmissionCheckState{
				{
					Name:  "check1",
					State: kueue.CheckStatePending,
				},
			},
			wantConditions: utiltesting.ExplicitStatusConditions(metav1.Condition{
				Type:               kueue.WorkloadAdmitted,
				Status:             metav1.ConditionFalse,
				Reason:             kueue.WorkloadAdmittedReasonNoReservation,
				ObservedGeneration: 1,
			}),
			wantChange: utiltesting.WantChangeWithExplicitStatus(false),
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGatesDuringTest(t, tc.featureGates)
			builder := utiltestingapi.MakeWorkload("foo", "bar").
				Admission(tc.admission).
				AdmissionChecks(tc.checkStates...).
				Conditions(tc.conditions...).
				Generation(1)
			if tc.pastAdmittedTime > 0 {
				builder = builder.PastAdmittedTime(tc.pastAdmittedTime)
			}
			wl := builder.Obj()

			gotChange := SyncAdmittedCondition(wl, testTime)

			if gotChange != tc.wantChange {
				t.Errorf("Unexpected change status, expecting %v", tc.wantChange)
			}

			if diff := cmp.Diff(tc.wantConditions, wl.Status.Conditions, cmpopts.IgnoreFields(metav1.Condition{}, "LastTransitionTime", "Message")); diff != "" {
				t.Errorf("Unexpected conditions after sync (- want/+ got):\n%s", diff)
			}

			if tc.wantAdmittedTime > 0 {
				if wl.Status.AccumulatedPastExecutionTimeSeconds == nil {
					t.Fatalf("Expecting AccumulatedPastExecutionTimeSeconds not to be nil")
				}

				if diff := cmp.Diff(tc.wantAdmittedTime, *wl.Status.AccumulatedPastExecutionTimeSeconds); diff != "" {
					t.Errorf("Unexpected AccumulatedPastExecutionTimeSeconds (- want/+ got):\n%s", diff)
				}
			}
		})
	}
}

func TestGetMaxRetryTime(t *testing.T) {
	baseTime := time.Date(2025, 1, 1, 12, 0, 0, 0, time.UTC)

	cases := map[string]struct {
		checkStates   []kueue.AdmissionCheckState
		wantRetryTime metav1.Time
	}{
		"no admission checks": {
			checkStates:   []kueue.AdmissionCheckState{},
			wantRetryTime: metav1.NewTime(time.Time{}),
		},
		"admission check with nil RequeueAfterSeconds": {
			checkStates: []kueue.AdmissionCheckState{
				{
					Name:                "check1",
					State:               kueue.CheckStateRetry,
					LastTransitionTime:  metav1.NewTime(baseTime),
					RequeueAfterSeconds: nil,
				},
			},
			wantRetryTime: metav1.NewTime(time.Time{}),
		},
		"admission check with zero LastTransitionTime": {
			checkStates: []kueue.AdmissionCheckState{
				{
					Name:                "check1",
					State:               kueue.CheckStateRetry,
					LastTransitionTime:  metav1.NewTime(time.Time{}),
					RequeueAfterSeconds: new(int32(60)),
				},
			},
			wantRetryTime: metav1.NewTime(time.Time{}),
		},
		"single admission check with valid retry time": {
			checkStates: []kueue.AdmissionCheckState{
				{
					Name:                "check1",
					State:               kueue.CheckStateRetry,
					LastTransitionTime:  metav1.NewTime(baseTime),
					RequeueAfterSeconds: new(int32(60)),
				},
			},
			wantRetryTime: metav1.NewTime(baseTime.Add(60 * time.Second)),
		},
		"multiple admission checks, returns max retry time": {
			checkStates: []kueue.AdmissionCheckState{
				{
					Name:                "check1",
					State:               kueue.CheckStateRetry,
					LastTransitionTime:  metav1.NewTime(baseTime),
					RequeueAfterSeconds: new(int32(60)),
				},
				{
					Name:                "check2",
					State:               kueue.CheckStateRetry,
					LastTransitionTime:  metav1.NewTime(baseTime),
					RequeueAfterSeconds: new(int32(120)),
				},
				{
					Name:                "check3",
					State:               kueue.CheckStateRetry,
					LastTransitionTime:  metav1.NewTime(baseTime),
					RequeueAfterSeconds: new(int32(30)),
				},
			},
			wantRetryTime: metav1.NewTime(baseTime.Add(120 * time.Second)),
		},
		"multiple checks with mixed nil and valid values": {
			checkStates: []kueue.AdmissionCheckState{
				{
					Name:                "check1",
					State:               kueue.CheckStateRetry,
					LastTransitionTime:  metav1.NewTime(baseTime),
					RequeueAfterSeconds: nil,
				},
				{
					Name:                "check2",
					State:               kueue.CheckStateRetry,
					LastTransitionTime:  metav1.NewTime(baseTime),
					RequeueAfterSeconds: new(int32(90)),
				},
				{
					Name:                "check3",
					State:               kueue.CheckStateRetry,
					LastTransitionTime:  metav1.NewTime(time.Time{}),
					RequeueAfterSeconds: new(int32(180)),
				},
			},
			wantRetryTime: metav1.NewTime(baseTime.Add(90 * time.Second)),
		},
		"checks with different base times": {
			checkStates: []kueue.AdmissionCheckState{
				{
					Name:                "check1",
					State:               kueue.CheckStateRetry,
					LastTransitionTime:  metav1.NewTime(baseTime),
					RequeueAfterSeconds: new(int32(60)),
				},
				{
					Name:                "check2",
					State:               kueue.CheckStateRetry,
					LastTransitionTime:  metav1.NewTime(baseTime.Add(30 * time.Second)),
					RequeueAfterSeconds: new(int32(60)),
				},
			},
			wantRetryTime: metav1.NewTime(baseTime.Add(90 * time.Second)),
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			wl := utiltestingapi.MakeWorkload("foo", "bar").
				AdmissionChecks(tc.checkStates...).
				Obj()

			gotRetryTime := GetMaxRetryTime(wl)

			if !gotRetryTime.Equal(&tc.wantRetryTime) {
				t.Errorf("GetMaxRetryTime() = %v, want %v", gotRetryTime, tc.wantRetryTime)
			}
		})
	}
}

func TestUpdateAdmissionCheckRequeueState(t *testing.T) {
	cases := map[string]struct {
		retryCount         int32
		backoffBaseSeconds int32
		backoffMaxSeconds  int32

		wantRequeueAfterSeconds int32
	}{
		"first retry waits for the base": {
			backoffBaseSeconds:      60,
			backoffMaxSeconds:       1800,
			wantRequeueAfterSeconds: 60,
		},
		"wait doubles with every retry": {
			retryCount:              2,
			backoffBaseSeconds:      60,
			backoffMaxSeconds:       1800,
			wantRequeueAfterSeconds: 240,
		},
		"wait is capped at the max": {
			retryCount:              6,
			backoffBaseSeconds:      60,
			backoffMaxSeconds:       1800,
			wantRequeueAfterSeconds: 1800,
		},
		"negative base produces a negative requeue time": {
			retryCount:              2,
			backoffBaseSeconds:      -60,
			backoffMaxSeconds:       1800,
			wantRequeueAfterSeconds: -240,
		},
		"negative max removes the cap": {
			retryCount:              5,
			backoffBaseSeconds:      60,
			backoffMaxSeconds:       -1800,
			wantRequeueAfterSeconds: 1920,
		},
		"base greater than max is used uncapped for the first retry": {
			backoffBaseSeconds:      7200,
			backoffMaxSeconds:       3600,
			wantRequeueAfterSeconds: 7200,
		},
		"base greater than max is capped from the second retry": {
			retryCount:              1,
			backoffBaseSeconds:      7200,
			backoffMaxSeconds:       3600,
			wantRequeueAfterSeconds: 3600,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			acState := &kueue.AdmissionCheckState{RetryCount: &tc.retryCount}

			UpdateAdmissionCheckRequeueState(acState, tc.backoffBaseSeconds, tc.backoffMaxSeconds, testingclock.NewFakeClock(time.Now()))

			if diff := cmp.Diff(&tc.wantRequeueAfterSeconds, acState.RequeueAfterSeconds); diff != "" {
				t.Errorf("Unexpected RequeueAfterSeconds (- want/+ got):\n%s", diff)
			}
		})
	}
}
