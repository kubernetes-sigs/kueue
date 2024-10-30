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

package core

import (
	"context"
	"fmt"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	testingclock "k8s.io/utils/clock/testing"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/cache"
	"sigs.k8s.io/kueue/pkg/queue"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
)

func TestAdmittedNotReadyWorkload(t *testing.T) {
	now := time.Now()
	minuteAgo := now.Add(-time.Minute)
	fakeClock := testingclock.NewFakeClock(now)

	testCases := map[string]struct {
		workload                   kueue.Workload
		waitForPodsReady           *waitForPodsReadyConfig
		wantCountingTowardsTimeout bool
		wantRecheckAfter           time.Duration
	}{
		"workload without Admitted condition; not counting": {
			workload: kueue.Workload{},
		},
		"workload with Admitted=True, no PodsReady; counting": {
			workload: kueue.Workload{
				Status: kueue.WorkloadStatus{
					Admission: &kueue.Admission{},
					Conditions: []metav1.Condition{
						{
							Type:               kueue.WorkloadAdmitted,
							Status:             metav1.ConditionTrue,
							LastTransitionTime: metav1.NewTime(minuteAgo),
						},
					},
				},
			},
			waitForPodsReady:           &waitForPodsReadyConfig{timeout: 5 * time.Minute},
			wantCountingTowardsTimeout: true,
			wantRecheckAfter:           4 * time.Minute,
		},
		"workload with Admitted=True, no PodsReady, but no timeout configured; not counting": {
			workload: kueue.Workload{
				Status: kueue.WorkloadStatus{
					Admission: &kueue.Admission{},
					Conditions: []metav1.Condition{
						{
							Type:               kueue.WorkloadAdmitted,
							Status:             metav1.ConditionTrue,
							LastTransitionTime: metav1.NewTime(minuteAgo),
						},
					},
				},
			},
		},
		"workload with Admitted=True, no PodsReady; timeout exceeded": {
			workload: kueue.Workload{
				Status: kueue.WorkloadStatus{
					Admission: &kueue.Admission{},
					Conditions: []metav1.Condition{
						{
							Type:               kueue.WorkloadAdmitted,
							Status:             metav1.ConditionTrue,
							LastTransitionTime: metav1.NewTime(now.Add(-7 * time.Minute)),
						},
					},
				},
			},
			waitForPodsReady:           &waitForPodsReadyConfig{timeout: 5 * time.Minute},
			wantCountingTowardsTimeout: true,
		},
		"workload with Admitted=True, PodsReady=False; counting since PodsReady.LastTransitionTime": {
			workload: kueue.Workload{
				Status: kueue.WorkloadStatus{
					Admission: &kueue.Admission{},
					Conditions: []metav1.Condition{
						{
							Type:               kueue.WorkloadAdmitted,
							Status:             metav1.ConditionTrue,
							LastTransitionTime: metav1.NewTime(minuteAgo),
						},
						{
							Type:               kueue.WorkloadPodsReady,
							Status:             metav1.ConditionFalse,
							LastTransitionTime: metav1.NewTime(now),
						},
					},
				},
			},
			waitForPodsReady:           &waitForPodsReadyConfig{timeout: 5 * time.Minute},
			wantCountingTowardsTimeout: true,
			wantRecheckAfter:           5 * time.Minute,
		},
		"workload with Admitted=Unknown; not counting": {
			workload: kueue.Workload{
				Status: kueue.WorkloadStatus{
					Admission: &kueue.Admission{},
					Conditions: []metav1.Condition{
						{
							Type:               kueue.WorkloadAdmitted,
							Status:             metav1.ConditionUnknown,
							LastTransitionTime: metav1.NewTime(minuteAgo),
						},
					},
				},
			},
			waitForPodsReady: &waitForPodsReadyConfig{timeout: 5 * time.Minute},
		},
		"workload with Admitted=False, not counting": {
			workload: kueue.Workload{
				Status: kueue.WorkloadStatus{
					Admission: &kueue.Admission{},
					Conditions: []metav1.Condition{
						{
							Type:               kueue.WorkloadAdmitted,
							Status:             metav1.ConditionUnknown,
							LastTransitionTime: metav1.NewTime(minuteAgo),
						},
					},
				},
			},
			waitForPodsReady: &waitForPodsReadyConfig{timeout: 5 * time.Minute},
		},
		"workload with Admitted=True, PodsReady=True; not counting": {
			workload: kueue.Workload{
				Status: kueue.WorkloadStatus{
					Admission: &kueue.Admission{},
					Conditions: []metav1.Condition{
						{
							Type:               kueue.WorkloadAdmitted,
							Status:             metav1.ConditionTrue,
							LastTransitionTime: metav1.NewTime(minuteAgo),
						},
						{
							Type:               kueue.WorkloadPodsReady,
							Status:             metav1.ConditionTrue,
							LastTransitionTime: metav1.NewTime(now),
						},
					},
				},
			},
			waitForPodsReady: &waitForPodsReadyConfig{timeout: 5 * time.Minute},
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			wRec := WorkloadReconciler{waitForPodsReady: tc.waitForPodsReady, clock: fakeClock}
			countingTowardsTimeout, recheckAfter := wRec.admittedNotReadyWorkload(&tc.workload)

			if tc.wantCountingTowardsTimeout != countingTowardsTimeout {
				t.Errorf("Unexpected countingTowardsTimeout, want=%v, got=%v", tc.wantCountingTowardsTimeout, countingTowardsTimeout)
			}
			if tc.wantRecheckAfter != recheckAfter {
				t.Errorf("Unexpected recheckAfter, want=%v, got=%v", tc.wantRecheckAfter, recheckAfter)
			}
		})
	}
}

func TestSyncCheckStates(t *testing.T) {
	now := metav1.NewTime(time.Now())
	cases := map[string]struct {
		states               []kueue.AdmissionCheckState
		list                 []string
		wantStates           []kueue.AdmissionCheckState
		wantChange           bool
		ignoreTransitionTime bool
	}{
		"nil conditions, nil list": {},
		"add to nil conditions": {
			list:       []string{"ac1", "ac2"},
			wantChange: true,
			wantStates: []kueue.AdmissionCheckState{
				{
					Name:  "ac1",
					State: kueue.CheckStatePending,
				},
				{
					Name:  "ac2",
					State: kueue.CheckStatePending,
				},
			},
			ignoreTransitionTime: true,
		},
		"add and remove": {
			states: []kueue.AdmissionCheckState{
				{
					Name:  "ac0",
					State: kueue.CheckStatePending,
				},
				{
					Name:  "ac1",
					State: kueue.CheckStatePending,
				},
			},
			list:       []string{"ac1", "ac2"},
			wantChange: true,
			wantStates: []kueue.AdmissionCheckState{
				{
					Name:  "ac1",
					State: kueue.CheckStatePending,
				},
				{
					Name:  "ac2",
					State: kueue.CheckStatePending,
				},
			},
			ignoreTransitionTime: true,
		},
		"cleanup": {
			states: []kueue.AdmissionCheckState{
				{
					Name:  "ac0",
					State: kueue.CheckStatePending,
				},
				{
					Name:  "ac1",
					State: kueue.CheckStatePending,
				},
			},
			wantChange: true,
		},
		"preserve conditions data": {
			states: []kueue.AdmissionCheckState{
				{
					Name:               "ac0",
					State:              kueue.CheckStateReady,
					Message:            "Message one",
					LastTransitionTime: *now.DeepCopy(),
				},
				{
					Name:  "ac1",
					State: kueue.CheckStatePending,
				},
			},
			list:       []string{"ac0", "ac1"},
			wantChange: false,
			wantStates: []kueue.AdmissionCheckState{
				{
					Name:               "ac0",
					State:              kueue.CheckStateReady,
					Message:            "Message one",
					LastTransitionTime: *now.DeepCopy(),
				},
				{
					Name:  "ac1",
					State: kueue.CheckStatePending,
				},
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			gotStates, gotShouldChange := syncAdmissionCheckConditions(tc.states, sets.New(tc.list...))

			if tc.wantChange != gotShouldChange {
				t.Errorf("Unexpected should change, want=%v", tc.wantChange)
			}

			var opts []cmp.Option
			if tc.ignoreTransitionTime {
				opts = append(opts, cmpopts.IgnoreFields(kueue.AdmissionCheckState{}, "LastTransitionTime"))
			}
			if diff := cmp.Diff(tc.wantStates, gotStates, opts...); diff != "" {
				t.Errorf("Unexpected conditions, (want-/got+): %s", diff)
			}
		})
	}
}

var (
	workloadCmpOpts = []cmp.Option{
		cmpopts.EquateEmpty(),
		cmpopts.IgnoreFields(
			kueue.Workload{}, "TypeMeta", "ObjectMeta.ResourceVersion",
		),
		cmpopts.IgnoreFields(metav1.Condition{}, "LastTransitionTime"),
		cmpopts.IgnoreFields(kueue.AdmissionCheckState{}, "LastTransitionTime"),
		cmpopts.IgnoreFields(kueue.RequeueState{}, "RequeueAt"),
		cmpopts.SortSlices(func(a, b metav1.Condition) bool { return a.Type < b.Type }),
	}
)

func TestReconcile(t *testing.T) {
	// the clock is primarily used with second rounded times
	// use the current time trimmed.
	testStartTime := time.Now().Truncate(time.Second)
	fakeClock := testingclock.NewFakeClock(testStartTime)

	cases := map[string]struct {
		workload       *kueue.Workload
		cq             *kueue.ClusterQueue
		lq             *kueue.LocalQueue
		wantWorkload   *kueue.Workload
		wantError      error
		wantEvents     []utiltesting.EventRecord
		wantResult     reconcile.Result
		reconcilerOpts []Option
	}{
		"assign Admission Checks from ClusterQueue.spec.AdmissionCheckStrategy": {
			workload: utiltesting.MakeWorkload("wl", "ns").
				ReserveQuota(utiltesting.MakeAdmission("cq").Assignment("cpu", "flavor1", "1").Obj()).
				Queue("queue").
				Obj(),
			cq: utiltesting.MakeClusterQueue("cq").
				AdmissionCheckStrategy(
					*utiltesting.MakeAdmissionCheckStrategyRule("ac1", "flavor1").Obj(),
					*utiltesting.MakeAdmissionCheckStrategyRule("ac2").Obj()).
				Obj(),
			lq: utiltesting.MakeLocalQueue("queue", "ns").ClusterQueue("cq").Obj(),
			wantWorkload: utiltesting.MakeWorkload("wl", "ns").
				ReserveQuota(utiltesting.MakeAdmission("cq").Assignment("cpu", "flavor1", "1").Obj()).
				Queue("queue").
				AdmissionChecks(
					kueue.AdmissionCheckState{
						Name:  "ac1",
						State: kueue.CheckStatePending,
					},
					kueue.AdmissionCheckState{
						Name:  "ac2",
						State: kueue.CheckStatePending,
					}).
				Obj(),
		},
		"assign Admission Checks from ClusterQueue.spec.AdmissionChecks": {
			workload: utiltesting.MakeWorkload("wl", "ns").
				ReserveQuota(utiltesting.MakeAdmission("cq").Assignment("cpu", "flavor1", "1").Obj()).
				Queue("queue").
				Obj(),
			cq: utiltesting.MakeClusterQueue("cq").
				AdmissionChecks("ac1", "ac2").
				Obj(),
			lq: utiltesting.MakeLocalQueue("queue", "ns").ClusterQueue("cq").Obj(),
			wantWorkload: utiltesting.MakeWorkload("wl", "ns").
				ReserveQuota(utiltesting.MakeAdmission("cq").Assignment("cpu", "flavor1", "1").Obj()).
				Queue("queue").
				AdmissionChecks(
					kueue.AdmissionCheckState{
						Name:  "ac1",
						State: kueue.CheckStatePending,
					},
					kueue.AdmissionCheckState{
						Name:  "ac2",
						State: kueue.CheckStatePending,
					}).
				Obj(),
		},
		"admit": {
			workload: utiltesting.MakeWorkload("wl", "ns").
				ReserveQuotaAt(utiltesting.MakeAdmission("q1").Obj(), testStartTime).
				AdmissionCheck(kueue.AdmissionCheckState{
					Name:  "check",
					State: kueue.CheckStateReady,
				}).
				Obj(),
			wantWorkload: utiltesting.MakeWorkload("wl", "ns").
				ReserveQuota(utiltesting.MakeAdmission("q1").Obj()).
				AdmissionCheck(kueue.AdmissionCheckState{
					Name:  "check",
					State: kueue.CheckStateReady,
				}).
				Condition(metav1.Condition{
					Type:    "Admitted",
					Status:  "True",
					Reason:  "Admitted",
					Message: "The workload is admitted",
				}).
				Obj(),
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Namespace: "ns", Name: "wl"},
					EventType: "Normal",
					Reason:    "Admitted",
					Message: fmt.Sprintf("Admitted by ClusterQueue q1, wait time since reservation was %.0fs",
						fakeClock.Since(metav1.NewTime(testStartTime).Time.Truncate(time.Second)).Seconds()),
				},
			},
		},
		"already admitted": {
			workload: utiltesting.MakeWorkload("wl", "ns").
				ReserveQuota(utiltesting.MakeAdmission("q1").Obj()).
				Admitted(true).
				AdmissionCheck(kueue.AdmissionCheckState{
					Name:  "check",
					State: kueue.CheckStateReady,
				}).
				Obj(),
			wantWorkload: utiltesting.MakeWorkload("wl", "ns").
				ReserveQuota(utiltesting.MakeAdmission("q1").Obj()).
				Admitted(true).
				AdmissionCheck(kueue.AdmissionCheckState{
					Name:  "check",
					State: kueue.CheckStateReady,
				}).
				Obj(),
		},
		"remove finalizer for finished workload": {
			workload: utiltesting.MakeWorkload("unit-test", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
				Condition(metav1.Condition{
					Type:   "Finished",
					Status: "True",
				}).
				DeletionTimestamp(testStartTime).
				Obj(),
			wantWorkload: nil,
		},
		"don't remove finalizer for owned finished workload": {
			workload: utiltesting.MakeWorkload("unit-test", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
				Condition(metav1.Condition{
					Type:   "Finished",
					Status: "True",
				}).
				ControllerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "job", "test-uid").
				DeletionTimestamp(testStartTime).
				Obj(),
			wantWorkload: utiltesting.MakeWorkload("unit-test", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
				Condition(metav1.Condition{
					Type:   "Finished",
					Status: "True",
				}).
				ControllerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "job", "test-uid").
				DeletionTimestamp(testStartTime).
				Obj(),
		},
		"unadmitted workload with rejected checks gets deactivated": {
			workload: utiltesting.MakeWorkload("wl", "ns").
				ControllerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "ownername", "owneruid").
				ReserveQuota(utiltesting.MakeAdmission("q1").Obj()).
				AdmissionCheck(kueue.AdmissionCheckState{
					Name:  "check",
					State: kueue.CheckStateRejected,
				}).
				Obj(),
			wantWorkload: utiltesting.MakeWorkload("wl", "ns").
				ControllerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "ownername", "owneruid").
				ReserveQuota(utiltesting.MakeAdmission("q1").Obj()).
				Active(false).
				AdmissionCheck(kueue.AdmissionCheckState{
					Name:  "check",
					State: kueue.CheckStateRejected,
				}).
				Obj(),
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Namespace: "ns", Name: "wl"},
					EventType: "Warning",
					Reason:    "AdmissionCheckRejected",
					Message:   "Deactivating workload because AdmissionCheck for check was Rejected: ",
				},
			},
		},
		"admitted workload with rejected checks gets deactivated": {
			workload: utiltesting.MakeWorkload("wl", "ns").
				ReserveQuota(utiltesting.MakeAdmission("q1").Obj()).
				Admitted(true).
				ControllerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "ownername", "owneruid").
				AdmissionCheck(kueue.AdmissionCheckState{
					Name:  "check",
					State: kueue.CheckStateRejected,
				}).
				Obj(),
			wantWorkload: utiltesting.MakeWorkload("wl", "ns").
				ReserveQuota(utiltesting.MakeAdmission("q1").Obj()).
				Admitted(true).
				Active(false).
				ControllerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "ownername", "owneruid").
				AdmissionCheck(kueue.AdmissionCheckState{
					Name:  "check",
					State: kueue.CheckStateRejected,
				}).
				Conditions(metav1.Condition{
					Type:    kueue.WorkloadQuotaReserved,
					Status:  metav1.ConditionTrue,
					Reason:  "AdmittedByTest",
					Message: "Admitted by ClusterQueue q1",
				},
					metav1.Condition{
						Type:    kueue.WorkloadAdmitted,
						Status:  metav1.ConditionTrue,
						Reason:  "ByTest",
						Message: "Admitted by ClusterQueue q1",
					}).
				Obj(),
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Namespace: "ns", Name: "wl"},
					EventType: "Warning",
					Reason:    "AdmissionCheckRejected",
					Message:   "Deactivating workload because AdmissionCheck for check was Rejected: ",
				},
			},
		},
		"workload with retry checks should be evicted and checks should be pending": {
			workload: utiltesting.MakeWorkload("wl", "ns").
				ReserveQuota(utiltesting.MakeAdmission("q1").Obj()).
				ControllerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "ownername", "owneruid").
				AdmissionChecks(kueue.AdmissionCheckState{
					Name:  "check-1",
					State: kueue.CheckStateRetry,
				}, kueue.AdmissionCheckState{
					Name:  "check-2",
					State: kueue.CheckStateReady,
				}).
				Obj(),
			wantWorkload: utiltesting.MakeWorkload("wl", "ns").
				ReserveQuota(utiltesting.MakeAdmission("q1").Obj()).
				ControllerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "ownername", "owneruid").
				AdmissionChecks(kueue.AdmissionCheckState{
					Name:    "check-1",
					State:   kueue.CheckStatePending,
					Message: "Reset to Pending after eviction. Previously: Retry",
				}, kueue.AdmissionCheckState{
					Name:    "check-2",
					State:   kueue.CheckStatePending,
					Message: "Reset to Pending after eviction. Previously: Ready",
				}).
				Condition(metav1.Condition{
					Type:    "Evicted",
					Status:  "True",
					Reason:  "AdmissionCheck",
					Message: "At least one admission check is false",
				}).
				Obj(),
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Namespace: "ns", Name: "wl"},
					EventType: "Normal",
					Reason:    "EvictedDueToAdmissionCheck",
					Message:   "At least one admission check is false",
				},
			},
		},
		"increment re-queue count": {
			reconcilerOpts: []Option{
				WithWaitForPodsReady(&waitForPodsReadyConfig{
					timeout:                     3 * time.Second,
					requeuingBackoffLimitCount:  ptr.To[int32](100),
					requeuingBackoffBaseSeconds: 10,
					requeuingBackoffJitter:      0,
					requeuingBackoffMaxDuration: time.Duration(3600) * time.Second,
				}),
			},
			workload: utiltesting.MakeWorkload("wl", "ns").
				ReserveQuota(utiltesting.MakeAdmission("q1").Obj()).
				AdmissionCheck(kueue.AdmissionCheckState{
					Name:  "check",
					State: kueue.CheckStateReady,
				}).
				Condition(metav1.Condition{ // Override LastTransitionTime
					Type:               kueue.WorkloadAdmitted,
					Status:             metav1.ConditionTrue,
					LastTransitionTime: metav1.NewTime(testStartTime.Add(-5 * time.Minute)),
					Reason:             "ByTest",
					Message:            "Admitted by ClusterQueue q1",
				}).
				Admitted(true).
				RequeueState(ptr.To[int32](3), nil).
				Generation(1).
				Obj(),
			wantWorkload: utiltesting.MakeWorkload("wl", "ns").
				ReserveQuota(utiltesting.MakeAdmission("q1").Obj()).
				Admitted(true).
				AdmissionCheck(kueue.AdmissionCheckState{
					Name:    "check",
					State:   kueue.CheckStatePending,
					Message: "Reset to Pending after eviction. Previously: Ready",
				}).
				Generation(1).
				Condition(metav1.Condition{
					Type:               kueue.WorkloadEvicted,
					Status:             metav1.ConditionTrue,
					Reason:             kueue.WorkloadEvictedByPodsReadyTimeout,
					Message:            "Exceeded the PodsReady timeout ns/wl",
					ObservedGeneration: 1,
				}).
				// 10s * 2^(4-1) = 80s
				RequeueState(ptr.To[int32](4), ptr.To(metav1.NewTime(testStartTime.Add(80*time.Second).Truncate(time.Second)))).
				Obj(),
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Name: "wl", Namespace: "ns"},
					EventType: corev1.EventTypeNormal,
					Reason:    "EvictedDueToPodsReadyTimeout",
					Message:   "Exceeded the PodsReady timeout ns/wl",
				},
			},
		},
		"trigger deactivation of workload when reaching backoffLimitCount": {
			reconcilerOpts: []Option{
				WithWaitForPodsReady(&waitForPodsReadyConfig{
					timeout:                    3 * time.Second,
					requeuingBackoffLimitCount: ptr.To[int32](1),
					requeuingBackoffJitter:     0,
				}),
			},
			workload: utiltesting.MakeWorkload("wl", "ns").
				ReserveQuota(utiltesting.MakeAdmission("q1").Obj()).
				AdmissionCheck(kueue.AdmissionCheckState{
					Name:  "check",
					State: kueue.CheckStateReady,
				}).
				Condition(metav1.Condition{ // Override LastTransitionTime
					Type:               kueue.WorkloadAdmitted,
					Status:             metav1.ConditionTrue,
					LastTransitionTime: metav1.NewTime(testStartTime.Add(-5 * time.Minute)),
					Reason:             "ByTest",
					Message:            "Admitted by ClusterQueue q1",
				}).
				Admitted(true).
				RequeueState(ptr.To[int32](1), ptr.To(metav1.NewTime(testStartTime.Add(1*time.Second).Truncate(time.Second)))).
				Obj(),
			wantWorkload: utiltesting.MakeWorkload("wl", "ns").
				ReserveQuota(utiltesting.MakeAdmission("q1").Obj()).
				Admitted(true).
				AdmissionCheck(kueue.AdmissionCheckState{
					Name:  "check",
					State: kueue.CheckStateReady,
				}).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadDeactivationTarget,
					Status:  metav1.ConditionTrue,
					Reason:  kueue.WorkloadRequeuingLimitExceeded,
					Message: "exceeding the maximum number of re-queuing retries",
				}).
				RequeueState(ptr.To[int32](1), ptr.To(metav1.NewTime(testStartTime.Add(1*time.Second).Truncate(time.Second)))).
				Obj(),
		},
		"wait time should be limited to backoffMaxSeconds": {
			reconcilerOpts: []Option{
				WithWaitForPodsReady(&waitForPodsReadyConfig{
					timeout:                     3 * time.Second,
					requeuingBackoffLimitCount:  ptr.To[int32](100),
					requeuingBackoffBaseSeconds: 10,
					requeuingBackoffJitter:      0,
					requeuingBackoffMaxDuration: time.Duration(7200) * time.Second,
				}),
			},
			workload: utiltesting.MakeWorkload("wl", "ns").
				ReserveQuota(utiltesting.MakeAdmission("q1").Obj()).
				AdmissionCheck(kueue.AdmissionCheckState{
					Name:  "check",
					State: kueue.CheckStateReady,
				}).
				Generation(1).
				Condition(metav1.Condition{ // Override LastTransitionTime
					Type:               kueue.WorkloadAdmitted,
					Status:             metav1.ConditionTrue,
					LastTransitionTime: metav1.NewTime(testStartTime.Add(-5 * time.Minute)),
					Reason:             "ByTest",
					Message:            "Admitted by ClusterQueue q1",
				}).
				Admitted(true).
				RequeueState(ptr.To[int32](10), ptr.To(metav1.NewTime(testStartTime.Add(1*time.Second).Truncate(time.Second)))).
				Obj(),
			wantWorkload: utiltesting.MakeWorkload("wl", "ns").
				ReserveQuota(utiltesting.MakeAdmission("q1").Obj()).
				Admitted(true).
				AdmissionCheck(kueue.AdmissionCheckState{
					Name:    "check",
					State:   kueue.CheckStatePending,
					Message: "Reset to Pending after eviction. Previously: Ready",
				}).
				Generation(1).
				Condition(metav1.Condition{
					Type:               kueue.WorkloadEvicted,
					Status:             metav1.ConditionTrue,
					Reason:             kueue.WorkloadEvictedByPodsReadyTimeout,
					Message:            "Exceeded the PodsReady timeout ns/wl",
					ObservedGeneration: 1,
				}).
				//  10s * 2^(11-1) = 10240s > requeuingBackoffMaxSeconds; then wait time should be limited to requeuingBackoffMaxSeconds
				RequeueState(ptr.To[int32](11), ptr.To(metav1.NewTime(testStartTime.Add(7200*time.Second).Truncate(time.Second)))).
				Obj(),
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Name: "wl", Namespace: "ns"},
					EventType: corev1.EventTypeNormal,
					Reason:    "EvictedDueToPodsReadyTimeout",
					Message:   "Exceeded the PodsReady timeout ns/wl",
				},
			},
		},
		"should set the WorkloadRequeued condition to true on re-activated": {
			workload: utiltesting.MakeWorkload("wl", "ns").
				Active(true).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadRequeued,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadEvictedByDeactivation,
					Message: "The workload is deactivated",
				}).
				Obj(),
			wantWorkload: utiltesting.MakeWorkload("wl", "ns").
				Active(true).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadRequeued,
					Status:  metav1.ConditionTrue,
					Reason:  kueue.WorkloadReactivated,
					Message: "The workload was reactivated",
				}).
				Obj(),
		},
		"should keep the WorkloadRequeued condition until the WaitForPodsReady backoff expires": {
			workload: utiltesting.MakeWorkload("wl", "ns").
				Active(true).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadRequeued,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadEvictedByPodsReadyTimeout,
					Message: "Exceeded the PodsReady timeout ns",
				}).
				RequeueState(ptr.To[int32](1), ptr.To(metav1.NewTime(testStartTime.Add(60*time.Second).Truncate(time.Second)))).
				Obj(),
			wantWorkload: utiltesting.MakeWorkload("wl", "ns").
				Active(true).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadRequeued,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadEvictedByPodsReadyTimeout,
					Message: "Exceeded the PodsReady timeout ns",
				}).
				RequeueState(ptr.To[int32](1), ptr.To(metav1.NewTime(testStartTime.Add(60*time.Second).Truncate(time.Second)))).
				Obj(),
			wantResult: reconcile.Result{RequeueAfter: time.Minute},
		},
		"should set the WorkloadRequeued condition when the WaitForPodsReady backoff expires": {
			workload: utiltesting.MakeWorkload("wl", "ns").
				Active(true).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadRequeued,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadEvictedByPodsReadyTimeout,
					Message: "Exceeded the PodsReady timeout ns",
				}).
				RequeueState(ptr.To[int32](1), ptr.To(metav1.NewTime(testStartTime.Truncate(time.Second)))).
				Obj(),
			wantWorkload: utiltesting.MakeWorkload("wl", "ns").
				Active(true).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadRequeued,
					Status:  metav1.ConditionTrue,
					Reason:  kueue.WorkloadBackoffFinished,
					Message: "The workload backoff was finished",
				}).
				RequeueState(ptr.To[int32](1), ptr.To(metav1.NewTime(testStartTime.Truncate(time.Second)))).
				Obj(),
		},
		"should keep the WorkloadRequeued condition until the AdmissionCheck backoff expires": {
			workload: utiltesting.MakeWorkload("wl", "ns").
				Active(true).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadRequeued,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadEvictedByAdmissionCheck,
					Message: "Exceeded the AdmissionCheck timeout ns",
				}).
				RequeueState(ptr.To[int32](1), ptr.To(metav1.NewTime(testStartTime.Add(60*time.Second).Truncate(time.Second)))).
				Obj(),
			wantWorkload: utiltesting.MakeWorkload("wl", "ns").
				Active(true).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadRequeued,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadEvictedByAdmissionCheck,
					Message: "Exceeded the AdmissionCheck timeout ns",
				}).
				RequeueState(ptr.To[int32](1), ptr.To(metav1.NewTime(testStartTime.Add(60*time.Second).Truncate(time.Second)))).
				Obj(),
			wantResult: reconcile.Result{RequeueAfter: time.Minute},
		},
		"should set the WorkloadRequeued condition when the AdmissionCheck backoff expires": {
			workload: utiltesting.MakeWorkload("wl", "ns").
				Active(true).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadRequeued,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadEvictedByAdmissionCheck,
					Message: "Exceeded the AdmissionCheck timeout ns",
				}).
				RequeueState(ptr.To[int32](1), ptr.To(metav1.NewTime(testStartTime.Truncate(time.Second)))).
				Obj(),
			wantWorkload: utiltesting.MakeWorkload("wl", "ns").
				Active(true).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadRequeued,
					Status:  metav1.ConditionTrue,
					Reason:  kueue.WorkloadBackoffFinished,
					Message: "The workload backoff was finished",
				}).
				RequeueState(ptr.To[int32](1), ptr.To(metav1.NewTime(testStartTime.Truncate(time.Second)))).
				Obj(),
		},
		"shouldn't set the WorkloadRequeued condition when backoff expires and workload finished": {
			workload: utiltesting.MakeWorkload("wl", "ns").
				Active(true).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadRequeued,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadEvictedByPodsReadyTimeout,
					Message: "Exceeded the PodsReady timeout ns",
				}).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadFinished,
					Status:  metav1.ConditionTrue,
					Reason:  "JobFinished",
					Message: "Job finished successfully",
				}).
				RequeueState(ptr.To[int32](1), ptr.To(metav1.NewTime(testStartTime.Truncate(time.Second)))).
				Obj(),
			wantWorkload: utiltesting.MakeWorkload("wl", "ns").
				Active(true).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadRequeued,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadEvictedByPodsReadyTimeout,
					Message: "Exceeded the PodsReady timeout ns",
				}).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadFinished,
					Status:  metav1.ConditionTrue,
					Reason:  "JobFinished",
					Message: "Job finished successfully",
				}).
				RequeueState(ptr.To[int32](1), ptr.To(metav1.NewTime(testStartTime.Truncate(time.Second)))).
				Obj(),
		},
		"should set the WorkloadRequeued condition to true on ClusterQueue started": {
			cq: utiltesting.MakeClusterQueue("cq").Obj(),
			lq: utiltesting.MakeLocalQueue("lq", "ns").ClusterQueue("cq").Obj(),
			workload: utiltesting.MakeWorkload("wl", "ns").
				Active(true).
				Queue("lq").
				Condition(metav1.Condition{
					Type:    kueue.WorkloadRequeued,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadEvictedByClusterQueueStopped,
					Message: "The ClusterQueue is stopped",
				}).
				Obj(),
			wantWorkload: utiltesting.MakeWorkload("wl", "ns").
				Active(true).
				Queue("lq").
				Condition(metav1.Condition{
					Type:    kueue.WorkloadRequeued,
					Status:  metav1.ConditionTrue,
					Reason:  kueue.WorkloadClusterQueueRestarted,
					Message: "The ClusterQueue was restarted after being stopped",
				}).
				Obj(),
		},
		"should set the WorkloadRequeued condition to true on LocalQueue started": {
			cq: utiltesting.MakeClusterQueue("cq").Obj(),
			lq: utiltesting.MakeLocalQueue("lq", "ns").ClusterQueue("cq").Obj(),
			workload: utiltesting.MakeWorkload("wl", "ns").
				Active(true).
				Queue("lq").
				Condition(metav1.Condition{
					Type:    kueue.WorkloadRequeued,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadEvictedByLocalQueueStopped,
					Message: "The LocalQueue is stopped",
				}).
				Obj(),
			wantWorkload: utiltesting.MakeWorkload("wl", "ns").
				Active(true).
				Queue("lq").
				Condition(metav1.Condition{
					Type:    kueue.WorkloadRequeued,
					Status:  metav1.ConditionTrue,
					Reason:  kueue.WorkloadLocalQueueRestarted,
					Message: "The LocalQueue was restarted after being stopped",
				}).
				Obj(),
		},
		"should set the Evicted condition with InactiveWorkload reason when the .spec.active=False": {
			workload: utiltesting.MakeWorkload("wl", "ns").Active(false).Obj(),
			wantWorkload: utiltesting.MakeWorkload("wl", "ns").
				Active(false).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadEvicted,
					Status:  metav1.ConditionTrue,
					Reason:  kueue.WorkloadEvictedByDeactivation,
					Message: "The workload is deactivated",
				}).
				Obj(),
		},
		"should set the Evicted condition with InactiveWorkload reason when the .spec.active=False and Admitted": {
			workload: utiltesting.MakeWorkload("wl", "ns").
				Active(false).
				ReserveQuota(utiltesting.MakeAdmission("q1").Obj()).
				Admitted(true).
				Obj(),
			wantWorkload: utiltesting.MakeWorkload("wl", "ns").
				Active(false).
				ReserveQuota(utiltesting.MakeAdmission("q1").Obj()).
				Admitted(true).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadEvicted,
					Status:  metav1.ConditionTrue,
					Reason:  kueue.WorkloadEvictedByDeactivation,
					Message: "The workload is deactivated",
				}).
				Obj(),
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Name: "wl", Namespace: "ns"},
					EventType: corev1.EventTypeNormal,
					Reason:    "EvictedDueToInactiveWorkload",
					Message:   "The workload is deactivated",
				},
			},
		},
		"should set the Evicted condition with InactiveWorkload reason when the .spec.active is False, Admitted, and the Workload has Evicted=False condition": {
			workload: utiltesting.MakeWorkload("wl", "ns").
				Active(false).
				ReserveQuota(utiltesting.MakeAdmission("q1").Obj()).
				Admitted(true).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadEvicted,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadEvictedByPodsReadyTimeout,
					Message: "Exceeded the PodsReady timeout ns",
				}).
				Obj(),
			wantWorkload: utiltesting.MakeWorkload("wl", "ns").
				Active(false).
				ReserveQuota(utiltesting.MakeAdmission("q1").Obj()).
				Admitted(true).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadEvicted,
					Status:  metav1.ConditionTrue,
					Reason:  kueue.WorkloadEvictedByDeactivation,
					Message: "The workload is deactivated",
				}).
				Obj(),
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Name: "wl", Namespace: "ns"},
					EventType: corev1.EventTypeNormal,
					Reason:    "EvictedDueToInactiveWorkload",
					Message:   "The workload is deactivated",
				},
			},
		},
		"should set the Evicted condition with InactiveWorkload reason, exceeded the maximum number of requeue retries" +
			"when the .spec.active is False, Admitted, the Workload has Evicted=False and DeactivationTarget=True condition": {
			reconcilerOpts: []Option{
				WithWaitForPodsReady(&waitForPodsReadyConfig{
					timeout:                     3 * time.Second,
					requeuingBackoffLimitCount:  ptr.To[int32](0),
					requeuingBackoffBaseSeconds: 10,
					requeuingBackoffJitter:      0,
				}),
			},
			workload: utiltesting.MakeWorkload("wl", "ns").
				Active(false).
				ReserveQuota(utiltesting.MakeAdmission("q1").Obj()).
				Admitted(true).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadPodsReady,
					Status:  metav1.ConditionFalse,
					Reason:  "PodsReady",
					Message: "Not all pods are ready or succeeded",
				}).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadDeactivationTarget,
					Status:  metav1.ConditionTrue,
					Reason:  kueue.WorkloadRequeuingLimitExceeded,
					Message: "exceeding the maximum number of re-queuing retries",
				}).
				Obj(),
			wantWorkload: utiltesting.MakeWorkload("wl", "ns").
				Active(false).
				ReserveQuota(utiltesting.MakeAdmission("q1").Obj()).
				Admitted(true).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadPodsReady,
					Status:  metav1.ConditionFalse,
					Reason:  "PodsReady",
					Message: "Not all pods are ready or succeeded",
				}).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadEvicted,
					Status:  metav1.ConditionTrue,
					Reason:  "InactiveWorkloadRequeuingLimitExceeded",
					Message: "The workload is deactivated due to exceeding the maximum number of re-queuing retries",
				}).
				// DeactivationTarget condition should be deleted in the real cluster, but the fake client doesn't allow us to do it.
				Condition(metav1.Condition{
					Type:    kueue.WorkloadDeactivationTarget,
					Status:  metav1.ConditionTrue,
					Reason:  kueue.WorkloadRequeuingLimitExceeded,
					Message: "exceeding the maximum number of re-queuing retries",
				}).
				Obj(),
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Name: "wl", Namespace: "ns"},
					EventType: corev1.EventTypeNormal,
					Reason:    "EvictedDueToInactiveWorkloadRequeuingLimitExceeded",
					Message:   "The workload is deactivated due to exceeding the maximum number of re-queuing retries",
				},
			},
		},
		"[backoffLimitCount: 100] should set the Evicted condition with InactiveWorkload reason, exceeded the maximum number of requeue retries" +
			"when the .spec.active is False, Admitted, the Workload has Evicted=False and DeactivationTarget=True condition, and the requeueState.count equals to backoffLimitCount": {
			reconcilerOpts: []Option{
				WithWaitForPodsReady(&waitForPodsReadyConfig{
					timeout:                     3 * time.Second,
					requeuingBackoffLimitCount:  ptr.To[int32](100),
					requeuingBackoffBaseSeconds: 10,
					requeuingBackoffJitter:      0,
				}),
			},
			workload: utiltesting.MakeWorkload("wl", "ns").
				Active(false).
				ReserveQuota(utiltesting.MakeAdmission("q1").Obj()).
				Admitted(true).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadPodsReady,
					Status:  metav1.ConditionFalse,
					Reason:  "PodsReady",
					Message: "Not all pods are ready or succeeded",
				}).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadDeactivationTarget,
					Status:  metav1.ConditionTrue,
					Reason:  kueue.WorkloadRequeuingLimitExceeded,
					Message: "exceeding the maximum number of re-queuing retries",
				}).
				RequeueState(ptr.To[int32](100), nil).
				Obj(),
			wantWorkload: utiltesting.MakeWorkload("wl", "ns").
				Active(false).
				ReserveQuota(utiltesting.MakeAdmission("q1").Obj()).
				Admitted(true).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadPodsReady,
					Status:  metav1.ConditionFalse,
					Reason:  "PodsReady",
					Message: "Not all pods are ready or succeeded",
				}).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadEvicted,
					Status:  metav1.ConditionTrue,
					Reason:  "InactiveWorkloadRequeuingLimitExceeded",
					Message: "The workload is deactivated due to exceeding the maximum number of re-queuing retries",
				}).
				// DeactivationTarget condition should be deleted in the real cluster, but the fake client doesn't allow us to do it.
				Condition(metav1.Condition{
					Type:    kueue.WorkloadDeactivationTarget,
					Status:  metav1.ConditionTrue,
					Reason:  "RequeuingLimitExceeded",
					Message: "exceeding the maximum number of re-queuing retries",
				}).
				// The requeueState should be reset in the real cluster, but the fake client doesn't allow us to do it.
				RequeueState(ptr.To[int32](100), nil).
				Obj(),
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Name: "wl", Namespace: "ns"},
					EventType: corev1.EventTypeNormal,
					Reason:    "EvictedDueToInactiveWorkloadRequeuingLimitExceeded",
					Message:   "The workload is deactivated due to exceeding the maximum number of re-queuing retries",
				},
			},
		},
		"should keep the previous eviction reason when the Workload is already evicted by other reason even though the Workload is deactivated.": {
			workload: utiltesting.MakeWorkload("wl", "ns").
				Active(false).
				ReserveQuota(utiltesting.MakeAdmission("q1").Obj()).
				Admitted(true).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadEvicted,
					Status:  metav1.ConditionTrue,
					Reason:  kueue.WorkloadEvictedByPodsReadyTimeout,
					Message: "Exceeded the PodsReady timeout ns",
				}).
				Obj(),
			wantWorkload: utiltesting.MakeWorkload("wl", "ns").
				Active(false).
				ReserveQuota(utiltesting.MakeAdmission("q1").Obj()).
				Admitted(true).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadEvicted,
					Status:  metav1.ConditionTrue,
					Reason:  kueue.WorkloadEvictedByPodsReadyTimeout,
					Message: "Exceeded the PodsReady timeout ns",
				}).
				Obj(),
		},
		"should set the Evicted condition with ClusterQueueStopped reason when the StopPolicy is HoldAndDrain": {
			cq: utiltesting.MakeClusterQueue("cq").StopPolicy(kueue.HoldAndDrain).Obj(),
			lq: utiltesting.MakeLocalQueue("lq", "ns").ClusterQueue("cq").Obj(),
			workload: utiltesting.MakeWorkload("wl", "ns").
				Active(true).
				ReserveQuota(utiltesting.MakeAdmission("cq").Obj()).
				Admitted(true).
				Queue("lq").
				Obj(),
			wantWorkload: utiltesting.MakeWorkload("wl", "ns").
				Active(true).
				ReserveQuota(utiltesting.MakeAdmission("cq").Obj()).
				Admitted(true).
				Queue("lq").
				Condition(metav1.Condition{
					Type:    kueue.WorkloadEvicted,
					Status:  metav1.ConditionTrue,
					Reason:  kueue.WorkloadEvictedByClusterQueueStopped,
					Message: "The ClusterQueue is stopped",
				}).
				Obj(),
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Name: "wl", Namespace: "ns"},
					EventType: corev1.EventTypeNormal,
					Reason:    "EvictedDueToClusterQueueStopped",
					Message:   "The ClusterQueue is stopped",
				},
			},
		},
		"should set the Evicted condition with LocalQueueStopped reason when the StopPolicy is HoldAndDrain": {
			cq: utiltesting.MakeClusterQueue("cq").Obj(),
			lq: utiltesting.MakeLocalQueue("lq", "ns").ClusterQueue("cq").StopPolicy(kueue.HoldAndDrain).Obj(),
			workload: utiltesting.MakeWorkload("wl", "ns").
				Active(true).
				ReserveQuota(utiltesting.MakeAdmission("cq").Obj()).
				Admitted(true).
				Queue("lq").
				Obj(),
			wantWorkload: utiltesting.MakeWorkload("wl", "ns").
				Active(true).
				ReserveQuota(utiltesting.MakeAdmission("cq").Obj()).
				Admitted(true).
				Queue("lq").
				Condition(metav1.Condition{
					Type:    kueue.WorkloadEvicted,
					Status:  metav1.ConditionTrue,
					Reason:  kueue.WorkloadEvictedByLocalQueueStopped,
					Message: "The LocalQueue is stopped",
				}).
				Obj(),
		},
		"should set the Inadmissible reason on QuotaReservation condition when the LocalQueue was deleted": {
			cq: utiltesting.MakeClusterQueue("cq").AdmissionChecks("check").Obj(),
			workload: utiltesting.MakeWorkload("wl", "ns").
				Active(true).
				ReserveQuota(utiltesting.MakeAdmission("cq").Obj()).
				AdmissionCheck(kueue.AdmissionCheckState{
					Name:  "check",
					State: kueue.CheckStatePending,
				}).
				Queue("lq").
				Obj(),
			wantWorkload: utiltesting.MakeWorkload("wl", "ns").
				Active(true).
				Admission(utiltesting.MakeAdmission("cq", "main").Obj()).
				AdmissionCheck(kueue.AdmissionCheckState{
					Name:  "check",
					State: kueue.CheckStatePending,
				}).
				Queue("lq").
				Condition(metav1.Condition{
					Type:    kueue.WorkloadQuotaReserved,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadInadmissible,
					Message: "LocalQueue lq is terminating or missing",
				}).
				Obj(),
		},
		"should set the Inadmissible reason on QuotaReservation condition when the LocalQueue was Hold": {
			cq: utiltesting.MakeClusterQueue("cq").AdmissionChecks("check").Obj(),
			lq: utiltesting.MakeLocalQueue("lq", "ns").ClusterQueue("cq").StopPolicy(kueue.Hold).Obj(),
			workload: utiltesting.MakeWorkload("wl", "ns").
				Active(true).
				ReserveQuota(utiltesting.MakeAdmission("cq").Obj()).
				AdmissionCheck(kueue.AdmissionCheckState{
					Name:  "check",
					State: kueue.CheckStatePending,
				}).
				Queue("lq").
				Obj(),
			wantWorkload: utiltesting.MakeWorkload("wl", "ns").
				Active(true).
				Admission(utiltesting.MakeAdmission("cq", "main").Obj()).
				AdmissionCheck(kueue.AdmissionCheckState{
					Name:  "check",
					State: kueue.CheckStatePending,
				}).
				Queue("lq").
				Condition(metav1.Condition{
					Type:    kueue.WorkloadQuotaReserved,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadInadmissible,
					Message: "LocalQueue lq is stopped",
				}).
				Obj(),
		},
		"should set the Inadmissible reason on QuotaReservation condition when the ClusterQueue was deleted": {
			lq: utiltesting.MakeLocalQueue("lq", "ns").ClusterQueue("cq").Obj(),
			workload: utiltesting.MakeWorkload("wl", "ns").
				Active(true).
				ReserveQuota(utiltesting.MakeAdmission("cq").Obj()).
				AdmissionCheck(kueue.AdmissionCheckState{
					Name:  "check",
					State: kueue.CheckStatePending,
				}).
				Queue("lq").
				Obj(),
			wantWorkload: utiltesting.MakeWorkload("wl", "ns").
				Active(true).
				Admission(utiltesting.MakeAdmission("cq", "main").Obj()).
				AdmissionCheck(kueue.AdmissionCheckState{
					Name:  "check",
					State: kueue.CheckStatePending,
				}).
				Queue("lq").
				Condition(metav1.Condition{
					Type:    kueue.WorkloadQuotaReserved,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadInadmissible,
					Message: "ClusterQueue cq is terminating or missing",
				}).
				Obj(),
		},
		"should set the Inadmissible reason on QuotaReservation condition when the ClusterQueue was Hold": {
			cq: utiltesting.MakeClusterQueue("cq").AdmissionChecks("check").StopPolicy(kueue.Hold).Obj(),
			lq: utiltesting.MakeLocalQueue("lq", "ns").ClusterQueue("cq").Obj(),
			workload: utiltesting.MakeWorkload("wl", "ns").
				Active(true).
				ReserveQuota(utiltesting.MakeAdmission("cq").Obj()).
				AdmissionCheck(kueue.AdmissionCheckState{
					Name:  "check",
					State: kueue.CheckStatePending,
				}).
				Queue("lq").
				Obj(),
			wantWorkload: utiltesting.MakeWorkload("wl", "ns").
				Active(true).
				Admission(utiltesting.MakeAdmission("cq", "main").Obj()).
				AdmissionCheck(kueue.AdmissionCheckState{
					Name:  "check",
					State: kueue.CheckStatePending,
				}).
				Queue("lq").
				Condition(metav1.Condition{
					Type:    kueue.WorkloadQuotaReserved,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadInadmissible,
					Message: "ClusterQueue cq is stopped",
				}).
				Obj(),
		},
		"should set status QuotaReserved conditions to False with reason Inadmissible if quota not reserved LocalQueue is not created": {
			workload: utiltesting.MakeWorkload("wl", "ns").
				Active(true).
				Queue("lq").
				Obj(),
			wantWorkload: utiltesting.MakeWorkload("wl", "ns").
				Active(true).
				Queue("lq").
				Condition(metav1.Condition{
					Type:    kueue.WorkloadQuotaReserved,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadInadmissible,
					Message: "LocalQueue lq doesn't exist",
				}).
				Obj(),
		},
		"should set status QuotaReserved conditions to False with reason Inadmissible if quota not reserved LocalQueue StopPolicy=Hold": {
			lq: utiltesting.MakeLocalQueue("lq", "ns").StopPolicy(kueue.Hold).Obj(),
			workload: utiltesting.MakeWorkload("wl", "ns").
				Active(true).
				Queue("lq").
				Obj(),
			wantWorkload: utiltesting.MakeWorkload("wl", "ns").
				Active(true).
				Queue("lq").
				Condition(metav1.Condition{
					Type:    kueue.WorkloadQuotaReserved,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadInadmissible,
					Message: "LocalQueue lq is inactive",
				}).
				Obj(),
		},
		"should set status QuotaReserved conditions to False with reason Inadmissible if quota not reserved LocalQueue StopPolicy=HoldAndDrain": {
			lq: utiltesting.MakeLocalQueue("lq", "ns").StopPolicy(kueue.HoldAndDrain).Obj(),
			workload: utiltesting.MakeWorkload("wl", "ns").
				Active(true).
				Queue("lq").
				Obj(),
			wantWorkload: utiltesting.MakeWorkload("wl", "ns").
				Active(true).
				Queue("lq").
				Condition(metav1.Condition{
					Type:    kueue.WorkloadQuotaReserved,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadInadmissible,
					Message: "LocalQueue lq is inactive",
				}).
				Obj(),
		},
		"should set status QuotaReserved conditions to False with reason Inadmissible if quota not reserved ClusterQueue is not created": {
			lq: utiltesting.MakeLocalQueue("lq", "ns").ClusterQueue("cq").StopPolicy(kueue.None).Obj(),
			workload: utiltesting.MakeWorkload("wl", "ns").
				Active(true).
				Queue("lq").
				Obj(),
			wantWorkload: utiltesting.MakeWorkload("wl", "ns").
				Active(true).
				Queue("lq").
				Condition(metav1.Condition{
					Type:    kueue.WorkloadQuotaReserved,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadInadmissible,
					Message: "ClusterQueue cq doesn't exist",
				}).
				Obj(),
		},
		"should set status QuotaReserved conditions to False with reason Inadmissible if quota not reserved ClusterQueue StopPolicy=Hold": {
			lq: utiltesting.MakeLocalQueue("lq", "ns").ClusterQueue("cq").StopPolicy(kueue.None).Obj(),
			cq: utiltesting.MakeClusterQueue("cq").StopPolicy(kueue.Hold).Obj(),
			workload: utiltesting.MakeWorkload("wl", "ns").
				Active(true).
				Queue("lq").
				Obj(),
			wantWorkload: utiltesting.MakeWorkload("wl", "ns").
				Active(true).
				Queue("lq").
				Condition(metav1.Condition{
					Type:    kueue.WorkloadQuotaReserved,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadInadmissible,
					Message: "ClusterQueue cq is inactive",
				}).
				Obj(),
		},

		"admitted workload with max execution time": {
			workload: utiltesting.MakeWorkload("wl", "ns").
				ReserveQuota(utiltesting.MakeAdmission("q1").Obj()).
				MaximumExecutionTimeSeconds(120).
				AdmittedAt(true, testStartTime.Add(-time.Minute)).
				ControllerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "ownername", "owneruid").
				Obj(),
			wantWorkload: utiltesting.MakeWorkload("wl", "ns").
				ReserveQuota(utiltesting.MakeAdmission("q1").Obj()).
				MaximumExecutionTimeSeconds(120).
				AdmittedAt(true, testStartTime.Add(-time.Minute)).
				ControllerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "ownername", "owneruid").
				Obj(),
			wantResult: reconcile.Result{RequeueAfter: time.Minute},
		},

		"admitted workload with max execution time - expired": {
			workload: utiltesting.MakeWorkload("wl", "ns").
				ReserveQuota(utiltesting.MakeAdmission("q1").Obj()).
				MaximumExecutionTimeSeconds(60).
				AdmittedAt(true, testStartTime.Add(-2*time.Minute)).
				ControllerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "ownername", "owneruid").
				Obj(),
			wantWorkload: utiltesting.MakeWorkload("wl", "ns").
				ReserveQuota(utiltesting.MakeAdmission("q1").Obj()).
				MaximumExecutionTimeSeconds(60).
				AdmittedAt(true, testStartTime.Add(-2*time.Minute)).
				ControllerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "ownername", "owneruid").
				Condition(metav1.Condition{
					Type:    kueue.WorkloadDeactivationTarget,
					Status:  metav1.ConditionTrue,
					Reason:  kueue.WorkloadMaximumExecutionTimeExceeded,
					Message: "exceeding the maximum execution time",
				}).
				Obj(),
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Namespace: "ns", Name: "wl"},
					EventType: "Warning",
					Reason:    "MaximumExecutionTimeExceeded",
					Message:   "The maximum execution time (60s) exceeded",
				},
			},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			objs := []client.Object{tc.workload}
			clientBuilder := utiltesting.NewClientBuilder().WithObjects(objs...).WithStatusSubresource(objs...).WithInterceptorFuncs(interceptor.Funcs{SubResourcePatch: utiltesting.TreatSSAAsStrategicMerge})
			cl := clientBuilder.Build()
			recorder := &utiltesting.EventRecorder{}

			cqCache := cache.New(cl)
			qManager := queue.NewManager(cl, cqCache)
			reconciler := NewWorkloadReconciler(cl, qManager, cqCache, recorder, tc.reconcilerOpts...)
			// use a fake clock with jitter = 0 to be able to assert on the requeueAt.
			reconciler.clock = fakeClock

			ctxWithLogger, _ := utiltesting.ContextWithLog(t)
			ctx, ctxCancel := context.WithCancel(ctxWithLogger)
			defer ctxCancel()

			if tc.cq != nil {
				if err := cl.Create(ctx, tc.cq); err != nil {
					t.Errorf("couldn't create the cluster queue: %v", err)
				}
				if err := qManager.AddClusterQueue(ctx, tc.cq); err != nil {
					t.Errorf("couldn't add the cluster queue to the cache: %v", err)
				}
			}

			if tc.lq != nil {
				if err := cl.Create(ctx, tc.lq); err != nil {
					t.Errorf("couldn't create the local queue: %v", err)
				}
				if err := qManager.AddLocalQueue(ctx, tc.lq); err != nil {
					t.Errorf("couldn't add the local queue to the cache: %v", err)
				}
			}

			gotResult, gotError := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(tc.workload)})

			if diff := cmp.Diff(tc.wantError, gotError); diff != "" {
				t.Errorf("unexpected reconcile error (-want/+got):\n%s", diff)
			}

			if diff := cmp.Diff(tc.wantResult, gotResult); diff != "" {
				t.Errorf("unexpected reconcile result (-want/+got):\n%s", diff)
			}

			gotWorkload := &kueue.Workload{}
			if err := cl.Get(ctx, client.ObjectKeyFromObject(tc.workload), gotWorkload); err != nil {
				if tc.wantWorkload != nil && !errors.IsNotFound(err) {
					t.Fatalf("Could not get Workloads after reconcile: %v", err)
				}
				gotWorkload = nil
			}
			if diff := cmp.Diff(tc.wantWorkload, gotWorkload, workloadCmpOpts...); diff != "" {
				t.Errorf("Workloads after reconcile (-want,+got):\n%s", diff)
			}
			if diff := cmp.Diff(tc.wantEvents, recorder.RecordedEvents); diff != "" {
				t.Errorf("unexpected events (-want/+got):\n%s", diff)
			}
		})
	}
}
