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

package core

import (
	"testing"
	"time"

	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	testingclock "k8s.io/utils/clock/testing"
	"k8s.io/utils/ptr"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
)

func TestReconcileEviction(t *testing.T) {
	// the clock is primarily used with second rounded times
	// use the current time trimmed.
	now := time.Now().Truncate(time.Second)
	fakeClock := testingclock.NewFakeClock(now)

	cases := map[string]reconcileTestCase{
		"unadmitted workload with rejected checks gets deactivated": {
			workload: utiltestingapi.MakeWorkload("wl", "ns").
				ControllerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "ownername", "owneruid").
				ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").Obj(), now).
				AdmissionCheck(kueue.AdmissionCheckState{
					Name:  "check",
					State: kueue.CheckStateRejected,
				}).
				Obj(),
			wantWorkload: utiltestingapi.MakeWorkload("wl", "ns").
				ControllerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "ownername", "owneruid").
				ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").Obj(), now).
				AdmissionCheck(kueue.AdmissionCheckState{
					Name:  "check",
					State: kueue.CheckStateRejected,
				}).
				Conditions(
					metav1.Condition{
						Type:    kueue.WorkloadQuotaReserved,
						Status:  metav1.ConditionTrue,
						Reason:  "AdmittedByTest",
						Message: "Admitted by ClusterQueue q1",
					},
					metav1.Condition{
						Type:    kueue.WorkloadDeactivationTarget,
						Status:  metav1.ConditionTrue,
						Reason:  "AdmissionCheck",
						Message: `AdmissionCheck in Rejected state: "check"`,
					},
					metav1.Condition{
						Type:    kueue.WorkloadAdmitted,
						Status:  metav1.ConditionFalse,
						Reason:  kueue.WorkloadAdmittedReasonUnsatisfiedAdmissionChecks,
						Message: "The workload has not all checks ready",
					},
				).
				Obj(),
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Namespace: "ns", Name: "wl"},
					EventType: "Warning",
					Reason:    "AdmissionCheckRejected",
					Message:   `Deactivated due to AdmissionCheck in Rejected state: "check"`,
				},
			},
		},
		"admitted workload with rejected checks gets deactivated": {
			workload: utiltestingapi.MakeWorkload("wl", "ns").
				ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").Obj(), now).
				AdmittedAt(true, now).
				ControllerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "ownername", "owneruid").
				AdmissionCheck(kueue.AdmissionCheckState{
					Name:  "check",
					State: kueue.CheckStateRejected,
				}).
				Obj(),
			wantWorkload: utiltestingapi.MakeWorkload("wl", "ns").
				ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").Obj(), now).
				AdmittedAt(true, now).
				ControllerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "ownername", "owneruid").
				AdmissionCheck(kueue.AdmissionCheckState{
					Name:  "check",
					State: kueue.CheckStateRejected,
				}).
				Conditions(
					metav1.Condition{
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
					},
					metav1.Condition{
						Type:    kueue.WorkloadDeactivationTarget,
						Status:  metav1.ConditionTrue,
						Reason:  "AdmissionCheck",
						Message: `AdmissionCheck in Rejected state: "check"`,
					},
				).
				Obj(),
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Namespace: "ns", Name: "wl"},
					EventType: "Warning",
					Reason:    "AdmissionCheckRejected",
					Message:   `Deactivated due to AdmissionCheck in Rejected state: "check"`,
				},
			},
		},
		"admitted workload with rejected checks gets deactivated, with message": {
			workload: utiltestingapi.MakeWorkload("wl", "ns").
				ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").Obj(), now).
				AdmittedAt(true, now).
				ControllerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "ownername", "owneruid").
				AdmissionCheck(kueue.AdmissionCheckState{
					Name:    "check",
					State:   kueue.CheckStateRejected,
					Message: "quota exceeded",
				}).
				Obj(),
			wantWorkload: utiltestingapi.MakeWorkload("wl", "ns").
				ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").Obj(), now).
				AdmittedAt(true, now).
				ControllerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "ownername", "owneruid").
				AdmissionCheck(kueue.AdmissionCheckState{
					Name:    "check",
					State:   kueue.CheckStateRejected,
					Message: "quota exceeded",
				}).
				Conditions(
					metav1.Condition{
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
					},
					metav1.Condition{
						Type:    kueue.WorkloadDeactivationTarget,
						Status:  metav1.ConditionTrue,
						Reason:  "AdmissionCheck",
						Message: `AdmissionCheck in Rejected state: "check" (quota exceeded)`,
					},
				).
				Obj(),
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Namespace: "ns", Name: "wl"},
					EventType: "Warning",
					Reason:    "AdmissionCheckRejected",
					Message:   `Deactivated due to AdmissionCheck in Rejected state: "check" (quota exceeded)`,
				},
			},
		},
		"workload with deactivation target condition should be deactivated and admission checks reset": {
			workload: utiltestingapi.MakeWorkload("wl", "ns").
				ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").Obj(), now).
				AdmittedAt(true, now).
				Active(false).
				ControllerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "ownername", "owneruid").
				AdmissionChecks(
					kueue.AdmissionCheckState{
						Name:  "check-1",
						State: kueue.CheckStateRejected,
					},
					kueue.AdmissionCheckState{
						Name:  "check-2",
						State: kueue.CheckStateRetry,
					},
				).
				Conditions(metav1.Condition{
					Type:    kueue.WorkloadDeactivationTarget,
					Status:  metav1.ConditionTrue,
					Reason:  kueue.WorkloadEvictedByAdmissionCheck,
					Message: `AdmissionCheck in Rejected state: "check-1"`,
				}).
				Obj(),
			wantWorkload: utiltestingapi.MakeWorkload("wl", "ns").
				ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").Obj(), now).
				AdmittedAt(true, now).
				Active(false).
				ControllerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "ownername", "owneruid").
				AdmissionChecks(
					kueue.AdmissionCheckState{
						Name:    "check-1",
						State:   kueue.CheckStatePending,
						Message: "Reset to Pending after eviction. Previously: Rejected",
					},
					kueue.AdmissionCheckState{
						Name:       "check-2",
						State:      kueue.CheckStatePending,
						Message:    "Reset to Pending after eviction. Previously: Retry",
						RetryCount: new(int32(1)),
					},
				).
				Conditions(
					metav1.Condition{
						Type:    kueue.WorkloadEvicted,
						Status:  metav1.ConditionTrue,
						Reason:  "DeactivatedDueToAdmissionCheck",
						Message: `The workload is deactivated due to AdmissionCheck in Rejected state: "check-1"`,
					},
					// In a real cluster this condition would be removed but it cant be in the fake cluster
					metav1.Condition{
						Type:    kueue.WorkloadDeactivationTarget,
						Status:  metav1.ConditionTrue,
						Reason:  kueue.WorkloadEvictedByAdmissionCheck,
						Message: `AdmissionCheck in Rejected state: "check-1"`,
					},
				).
				SchedulingStatsEviction(
					kueue.WorkloadSchedulingStatsEviction{
						Reason:          "Deactivated",
						UnderlyingCause: "AdmissionCheck",
						Count:           1,
					},
				).
				Obj(),
			wantWorkloadUseMergePatch: utiltestingapi.MakeWorkload("wl", "ns").
				ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").Obj(), now).
				AdmittedAt(true, now).
				Active(false).
				ControllerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "ownername", "owneruid").
				AdmissionChecks(
					kueue.AdmissionCheckState{
						Name:    "check-1",
						State:   kueue.CheckStatePending,
						Message: "Reset to Pending after eviction. Previously: Rejected",
					},
					kueue.AdmissionCheckState{
						Name:       "check-2",
						State:      kueue.CheckStatePending,
						Message:    "Reset to Pending after eviction. Previously: Retry",
						RetryCount: new(int32(1)),
					},
				).
				Conditions(
					metav1.Condition{
						Type:    kueue.WorkloadEvicted,
						Status:  metav1.ConditionTrue,
						Reason:  "DeactivatedDueToAdmissionCheck",
						Message: `The workload is deactivated due to AdmissionCheck in Rejected state: "check-1"`,
					},
				).
				SchedulingStatsEviction(
					kueue.WorkloadSchedulingStatsEviction{
						Reason:          "Deactivated",
						UnderlyingCause: "AdmissionCheck",
						Count:           1,
					},
				).
				Obj(),
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Namespace: "ns", Name: "wl"},
					EventType: "Normal",
					Reason:    "EvictedDueToDeactivatedDueToAdmissionCheck",
					Message:   `The workload is deactivated due to AdmissionCheck in Rejected state: "check-1"`,
				},
			},
		},
		"workload with retry checks should be evicted and checks should be pending": {
			workload: utiltestingapi.MakeWorkload("wl", "ns").
				ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").Obj(), now).
				ControllerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "ownername", "owneruid").
				AdmissionChecks(kueue.AdmissionCheckState{
					Name:  "check-1",
					State: kueue.CheckStateRetry,
				}, kueue.AdmissionCheckState{
					Name:  "check-2",
					State: kueue.CheckStateReady,
				}).
				Obj(),
			wantWorkload: utiltestingapi.MakeWorkload("wl", "ns").
				ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").Obj(), now).
				ControllerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "ownername", "owneruid").
				AdmissionChecks(kueue.AdmissionCheckState{
					Name:       "check-1",
					State:      kueue.CheckStatePending,
					Message:    "Reset to Pending after eviction. Previously: Retry",
					RetryCount: new(int32(1)),
				}, kueue.AdmissionCheckState{
					Name:    "check-2",
					State:   kueue.CheckStatePending,
					Message: "Reset to Pending after eviction. Previously: Ready",
				}).
				Condition(metav1.Condition{
					Type:    "Evicted",
					Status:  "True",
					Reason:  "AdmissionCheck",
					Message: `Evicted due to AdmissionCheck in Retry state: "check-1"`,
				}).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadAdmitted,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadAdmittedReasonUnsatisfiedAdmissionChecks,
					Message: "The workload has not all checks ready",
				}).
				SchedulingStatsEviction(
					kueue.WorkloadSchedulingStatsEviction{
						Reason: kueue.WorkloadEvictedByAdmissionCheck,
						Count:  1,
					},
				).
				Obj(),
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Namespace: "ns", Name: "wl"},
					EventType: "Normal",
					Reason:    "EvictedDueToAdmissionCheck",
					Message:   `Evicted due to AdmissionCheck in Retry state: "check-1"`,
				},
			},
		},
		"workload with retry checks and unhealthy nodes should be evicted and checks should be pending": {
			workload: utiltestingapi.MakeWorkload("wl", "ns").
				ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").Obj(), now).
				ControllerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "ownername", "owneruid").
				AdmissionChecks(kueue.AdmissionCheckState{
					Name:  "check-1",
					State: kueue.CheckStateRetry,
				}, kueue.AdmissionCheckState{
					Name:  "check-2",
					State: kueue.CheckStateReady,
				}).
				Condition(metav1.Condition{
					Type:               kueue.WorkloadAdmitted,
					Status:             metav1.ConditionTrue,
					LastTransitionTime: metav1.NewTime(now),
					Reason:             "ByTest",
					Message:            "The workload is admitted",
				}).
				AdmittedAt(true, now).
				UnhealthyNodes("xyz").
				Obj(),
			wantWorkload: utiltestingapi.MakeWorkload("wl", "ns").
				ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").Obj(), now).
				ControllerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "ownername", "owneruid").
				AdmissionChecks(kueue.AdmissionCheckState{
					Name:       "check-1",
					State:      kueue.CheckStatePending,
					Message:    "Reset to Pending after eviction. Previously: Retry",
					RetryCount: new(int32(1)),
				}, kueue.AdmissionCheckState{
					Name:    "check-2",
					State:   kueue.CheckStatePending,
					Message: "Reset to Pending after eviction. Previously: Ready",
				}).
				Condition(metav1.Condition{
					Type:               kueue.WorkloadAdmitted,
					Status:             metav1.ConditionTrue,
					LastTransitionTime: metav1.NewTime(now),
					Reason:             "ByTest",
					Message:            "The workload is admitted",
				}).
				Condition(metav1.Condition{
					Type:    "Evicted",
					Status:  "True",
					Reason:  "AdmissionCheck",
					Message: `Evicted due to AdmissionCheck in Retry state: "check-1"`,
				}).
				AdmittedAt(true, now).
				UnhealthyNodes("xyz").
				SchedulingStatsEviction(
					kueue.WorkloadSchedulingStatsEviction{
						Reason: kueue.WorkloadEvictedByAdmissionCheck,
						Count:  1,
					},
				).
				Obj(),
			wantWorkloadUseMergePatch: utiltestingapi.MakeWorkload("wl", "ns").
				ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").Obj(), now).
				ControllerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "ownername", "owneruid").
				AdmissionChecks(kueue.AdmissionCheckState{
					Name:       "check-1",
					State:      kueue.CheckStatePending,
					Message:    "Reset to Pending after eviction. Previously: Retry",
					RetryCount: new(int32(1)),
				}, kueue.AdmissionCheckState{
					Name:    "check-2",
					State:   kueue.CheckStatePending,
					Message: "Reset to Pending after eviction. Previously: Ready",
				}).
				Condition(metav1.Condition{
					Type:               kueue.WorkloadAdmitted,
					Status:             metav1.ConditionTrue,
					LastTransitionTime: metav1.NewTime(now),
					Reason:             "ByTest",
					Message:            "The workload is admitted",
				}).
				Condition(metav1.Condition{
					Type:    "Evicted",
					Status:  "True",
					Reason:  "AdmissionCheck",
					Message: `Evicted due to AdmissionCheck in Retry state: "check-1"`,
				}).
				AdmittedAt(true, now).
				SchedulingStatsEviction(
					kueue.WorkloadSchedulingStatsEviction{
						Reason: kueue.WorkloadEvictedByAdmissionCheck,
						Count:  1,
					},
				).
				Obj(),
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Namespace: "ns", Name: "wl"},
					EventType: "Normal",
					Reason:    "EvictedDueToAdmissionCheck",
					Message:   `Evicted due to AdmissionCheck in Retry state: "check-1"`,
				},
			},
		},
		"workload with retry checks should be evicted and checks should be pending, with message": {
			workload: utiltestingapi.MakeWorkload("wl", "ns").
				ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").Obj(), now).
				ControllerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "ownername", "owneruid").
				AdmissionChecks(kueue.AdmissionCheckState{
					Name:    "check-1",
					State:   kueue.CheckStateRetry,
					Message: "infrastructure not prepared",
				}, kueue.AdmissionCheckState{
					Name:  "check-2",
					State: kueue.CheckStateReady,
				}, kueue.AdmissionCheckState{
					Name:    "check-3",
					State:   kueue.CheckStateRetry,
					Message: "budget exhausted",
				}).
				Obj(),
			wantWorkload: utiltestingapi.MakeWorkload("wl", "ns").
				ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").Obj(), now).
				ControllerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "ownername", "owneruid").
				AdmissionChecks(kueue.AdmissionCheckState{
					Name:       "check-1",
					State:      kueue.CheckStatePending,
					Message:    "Reset to Pending after eviction. Previously: Retry",
					RetryCount: new(int32(1)),
				}, kueue.AdmissionCheckState{
					Name:    "check-2",
					State:   kueue.CheckStatePending,
					Message: "Reset to Pending after eviction. Previously: Ready",
				}, kueue.AdmissionCheckState{
					Name:       "check-3",
					State:      kueue.CheckStatePending,
					Message:    "Reset to Pending after eviction. Previously: Retry",
					RetryCount: new(int32(1)),
				}).
				Condition(metav1.Condition{
					Type:    "Evicted",
					Status:  "True",
					Reason:  "AdmissionCheck",
					Message: `Evicted due to AdmissionChecks in Retry state: "check-1" (infrastructure not prepared); "check-3" (budget exhausted)`,
				}).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadAdmitted,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadAdmittedReasonUnsatisfiedAdmissionChecks,
					Message: "The workload has not all checks ready",
				}).
				SchedulingStatsEviction(
					kueue.WorkloadSchedulingStatsEviction{
						Reason: kueue.WorkloadEvictedByAdmissionCheck,
						Count:  1,
					},
				).
				Obj(),
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Namespace: "ns", Name: "wl"},
					EventType: "Normal",
					Reason:    "EvictedDueToAdmissionCheck",
					Message:   `Evicted due to AdmissionChecks in Retry state: "check-1" (infrastructure not prepared); "check-3" (budget exhausted)`,
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
			workload: utiltestingapi.MakeWorkload("wl", "ns").
				ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").Obj(), now).
				AdmissionCheck(kueue.AdmissionCheckState{
					Name:  "check",
					State: kueue.CheckStateReady,
				}).
				Condition(metav1.Condition{ // Override LastTransitionTime
					Type:               kueue.WorkloadAdmitted,
					Status:             metav1.ConditionTrue,
					LastTransitionTime: metav1.NewTime(now.Add(-5 * time.Minute)),
					Reason:             "ByTest",
					Message:            "Admitted by ClusterQueue q1",
				}).
				AdmittedAt(true, now).
				RequeueState(ptr.To[int32](1), new(metav1.NewTime(now.Add(-1*time.Second).Truncate(time.Second)))).
				Obj(),
			wantWorkload: utiltestingapi.MakeWorkload("wl", "ns").
				ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").Obj(), now).
				AdmittedAt(true, now).
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
				RequeueState(ptr.To[int32](1), new(metav1.NewTime(now.Add(1*time.Second).Truncate(time.Second)))).
				Obj(),
		},
		"should set the Evicted condition with Deactivated reason when the .spec.active=False": {
			workload: utiltestingapi.MakeWorkload("wl", "ns").Active(false).Obj(),
			wantWorkload: utiltestingapi.MakeWorkload("wl", "ns").
				Active(false).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadEvicted,
					Status:  metav1.ConditionTrue,
					Reason:  kueue.WorkloadDeactivated,
					Message: "The workload is deactivated",
				}).
				SchedulingStatsEviction(
					kueue.WorkloadSchedulingStatsEviction{
						Reason: kueue.WorkloadDeactivated,
						Count:  1,
					},
				).
				Obj(),
		},
		"should set the Evicted condition with Deactivated reason when the .spec.active=False and Admitted": {
			workload: utiltestingapi.MakeWorkload("wl", "ns").
				Active(false).
				ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").Obj(), now).
				AdmittedAt(true, now).
				Obj(),
			wantWorkload: utiltestingapi.MakeWorkload("wl", "ns").
				Active(false).
				ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").Obj(), now).
				AdmittedAt(true, now).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadEvicted,
					Status:  metav1.ConditionTrue,
					Reason:  kueue.WorkloadDeactivated,
					Message: "The workload is deactivated",
				}).
				SchedulingStatsEviction(
					kueue.WorkloadSchedulingStatsEviction{
						Reason: kueue.WorkloadDeactivated,
						Count:  1,
					},
				).
				Obj(),
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Name: "wl", Namespace: "ns"},
					EventType: corev1.EventTypeNormal,
					Reason:    "EvictedDueToDeactivated",
					Message:   "The workload is deactivated",
				},
			},
		},
		"should set the Evicted condition with Deactivated reason when the .spec.active is False, Admitted, and the Workload has Evicted=False condition": {
			workload: utiltestingapi.MakeWorkload("wl", "ns").
				Active(false).
				ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").Obj(), now).
				AdmittedAt(true, now).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadEvicted,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadEvictedByPodsReadyTimeout,
					Message: "Exceeded the PodsReady timeout ns",
				}).
				SchedulingStatsEviction(
					kueue.WorkloadSchedulingStatsEviction{
						Reason:          kueue.WorkloadEvictedByPodsReadyTimeout,
						UnderlyingCause: kueue.WorkloadWaitForStart,
						Count:           1,
					},
				).
				Obj(),
			wantWorkload: utiltestingapi.MakeWorkload("wl", "ns").
				Active(false).
				ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").Obj(), now).
				AdmittedAt(true, now).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadEvicted,
					Status:  metav1.ConditionTrue,
					Reason:  kueue.WorkloadDeactivated,
					Message: "The workload is deactivated",
				}).
				SchedulingStatsEviction(
					kueue.WorkloadSchedulingStatsEviction{
						Reason:          kueue.WorkloadEvictedByPodsReadyTimeout,
						UnderlyingCause: kueue.WorkloadWaitForStart,
						Count:           1,
					},
				).
				SchedulingStatsEviction(
					kueue.WorkloadSchedulingStatsEviction{
						Reason: kueue.WorkloadDeactivated,
						Count:  1,
					},
				).
				Obj(),
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Name: "wl", Namespace: "ns"},
					EventType: corev1.EventTypeNormal,
					Reason:    "EvictedDueToDeactivated",
					Message:   "The workload is deactivated",
				},
			},
		},
		"with reason PodsReady; should set the Evicted condition with Deactivated reason, exceeded the maximum number of requeue retries" +
			"when the .spec.active is False, Admitted, the Workload has Evicted=False and DeactivationTarget=True condition": {
			reconcilerOpts: []Option{
				WithWaitForPodsReady(&waitForPodsReadyConfig{
					timeout:                     3 * time.Second,
					requeuingBackoffLimitCount:  ptr.To[int32](0),
					requeuingBackoffBaseSeconds: 10,
					requeuingBackoffJitter:      0,
				}),
			},
			workload: utiltestingapi.MakeWorkload("wl", "ns").
				Active(false).
				ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").Obj(), now).
				AdmittedAt(true, now).
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
			wantWorkload: utiltestingapi.MakeWorkload("wl", "ns").
				Active(false).
				ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").Obj(), now).
				AdmittedAt(true, now).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadPodsReady,
					Status:  metav1.ConditionFalse,
					Reason:  "PodsReady",
					Message: "Not all pods are ready or succeeded",
				}).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadEvicted,
					Status:  metav1.ConditionTrue,
					Reason:  "DeactivatedDueToRequeuingLimitExceeded",
					Message: "The workload is deactivated due to exceeding the maximum number of re-queuing retries",
				}).
				// DeactivationTarget condition should be deleted in the real cluster, but the fake client doesn't allow us to do it.
				Condition(metav1.Condition{
					Type:    kueue.WorkloadDeactivationTarget,
					Status:  metav1.ConditionTrue,
					Reason:  kueue.WorkloadRequeuingLimitExceeded,
					Message: "exceeding the maximum number of re-queuing retries",
				}).
				SchedulingStatsEviction(
					kueue.WorkloadSchedulingStatsEviction{
						Reason:          "Deactivated",
						UnderlyingCause: "RequeuingLimitExceeded",
						Count:           1,
					},
				).
				Obj(),
			wantWorkloadUseMergePatch: utiltestingapi.MakeWorkload("wl", "ns").
				Active(false).
				ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").Obj(), now).
				AdmittedAt(true, now).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadPodsReady,
					Status:  metav1.ConditionFalse,
					Reason:  "PodsReady",
					Message: "Not all pods are ready or succeeded",
				}).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadEvicted,
					Status:  metav1.ConditionTrue,
					Reason:  "DeactivatedDueToRequeuingLimitExceeded",
					Message: "The workload is deactivated due to exceeding the maximum number of re-queuing retries",
				}).
				SchedulingStatsEviction(
					kueue.WorkloadSchedulingStatsEviction{
						Reason:          "Deactivated",
						UnderlyingCause: "RequeuingLimitExceeded",
						Count:           1,
					},
				).
				Obj(),
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Name: "wl", Namespace: "ns"},
					EventType: corev1.EventTypeNormal,
					Reason:    "EvictedDueToDeactivatedDueToRequeuingLimitExceeded",
					Message:   "The workload is deactivated due to exceeding the maximum number of re-queuing retries",
				},
			},
		},
		"with reason WaitForPodsStart; should set the Evicted condition with Deactivated reason, exceeded the maximum number of requeue retries" +
			"when the .spec.active is False, Admitted, the Workload has Evicted=False and DeactivationTarget=True condition": {
			reconcilerOpts: []Option{
				WithWaitForPodsReady(&waitForPodsReadyConfig{
					timeout:                     3 * time.Second,
					requeuingBackoffLimitCount:  ptr.To[int32](0),
					requeuingBackoffBaseSeconds: 10,
					requeuingBackoffJitter:      0,
				}),
			},
			workload: utiltestingapi.MakeWorkload("wl", "ns").
				Active(false).
				ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").Obj(), now).
				AdmittedAt(true, now).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadPodsReady,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadWaitForStart,
					Message: "Not all pods are ready or succeeded",
				}).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadDeactivationTarget,
					Status:  metav1.ConditionTrue,
					Reason:  kueue.WorkloadRequeuingLimitExceeded,
					Message: "exceeding the maximum number of re-queuing retries",
				}).
				Obj(),
			wantWorkload: utiltestingapi.MakeWorkload("wl", "ns").
				Active(false).
				ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").Obj(), now).
				AdmittedAt(true, now).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadPodsReady,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadWaitForStart,
					Message: "Not all pods are ready or succeeded",
				}).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadEvicted,
					Status:  metav1.ConditionTrue,
					Reason:  "DeactivatedDueToRequeuingLimitExceeded",
					Message: "The workload is deactivated due to exceeding the maximum number of re-queuing retries",
				}).
				// DeactivationTarget condition should be deleted in the real cluster, but the fake client doesn't allow us to do it.
				Condition(metav1.Condition{
					Type:    kueue.WorkloadDeactivationTarget,
					Status:  metav1.ConditionTrue,
					Reason:  kueue.WorkloadRequeuingLimitExceeded,
					Message: "exceeding the maximum number of re-queuing retries",
				}).
				SchedulingStatsEviction(
					kueue.WorkloadSchedulingStatsEviction{
						Reason:          "Deactivated",
						UnderlyingCause: "RequeuingLimitExceeded",
						Count:           1,
					},
				).
				Obj(),
			wantWorkloadUseMergePatch: utiltestingapi.MakeWorkload("wl", "ns").
				Active(false).
				ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").Obj(), now).
				AdmittedAt(true, now).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadPodsReady,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadWaitForStart,
					Message: "Not all pods are ready or succeeded",
				}).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadEvicted,
					Status:  metav1.ConditionTrue,
					Reason:  "DeactivatedDueToRequeuingLimitExceeded",
					Message: "The workload is deactivated due to exceeding the maximum number of re-queuing retries",
				}).
				SchedulingStatsEviction(
					kueue.WorkloadSchedulingStatsEviction{
						Reason:          "Deactivated",
						UnderlyingCause: "RequeuingLimitExceeded",
						Count:           1,
					},
				).
				Obj(),
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Name: "wl", Namespace: "ns"},
					EventType: corev1.EventTypeNormal,
					Reason:    "EvictedDueToDeactivatedDueToRequeuingLimitExceeded",
					Message:   "The workload is deactivated due to exceeding the maximum number of re-queuing retries",
				},
			},
		},
		"with reason PodsReady; [backoffLimitCount: 100] should set the Evicted condition with Deactivated reason, exceeded the maximum number of requeue retries" +
			"when the .spec.active is False, Admitted, the Workload has Evicted=False and DeactivationTarget=True condition, and the requeueState.count equals to backoffLimitCount": {
			reconcilerOpts: []Option{
				WithWaitForPodsReady(&waitForPodsReadyConfig{
					timeout:                     3 * time.Second,
					requeuingBackoffLimitCount:  ptr.To[int32](100),
					requeuingBackoffBaseSeconds: 10,
					requeuingBackoffJitter:      0,
				}),
			},
			workload: utiltestingapi.MakeWorkload("wl", "ns").
				Active(false).
				ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").Obj(), now).
				AdmittedAt(true, now).
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
			wantWorkload: utiltestingapi.MakeWorkload("wl", "ns").
				Active(false).
				ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").Obj(), now).
				AdmittedAt(true, now).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadPodsReady,
					Status:  metav1.ConditionFalse,
					Reason:  "PodsReady",
					Message: "Not all pods are ready or succeeded",
				}).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadEvicted,
					Status:  metav1.ConditionTrue,
					Reason:  "DeactivatedDueToRequeuingLimitExceeded",
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
				SchedulingStatsEviction(
					kueue.WorkloadSchedulingStatsEviction{
						Reason:          "Deactivated",
						UnderlyingCause: "RequeuingLimitExceeded",
						Count:           1,
					},
				).
				Obj(),
			wantWorkloadUseMergePatch: utiltestingapi.MakeWorkload("wl", "ns").
				Active(false).
				ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").Obj(), now).
				AdmittedAt(true, now).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadPodsReady,
					Status:  metav1.ConditionFalse,
					Reason:  "PodsReady",
					Message: "Not all pods are ready or succeeded",
				}).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadEvicted,
					Status:  metav1.ConditionTrue,
					Reason:  "DeactivatedDueToRequeuingLimitExceeded",
					Message: "The workload is deactivated due to exceeding the maximum number of re-queuing retries",
				}).
				SchedulingStatsEviction(
					kueue.WorkloadSchedulingStatsEviction{
						Reason:          "Deactivated",
						UnderlyingCause: "RequeuingLimitExceeded",
						Count:           1,
					},
				).
				Obj(),
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Name: "wl", Namespace: "ns"},
					EventType: corev1.EventTypeNormal,
					Reason:    "EvictedDueToDeactivatedDueToRequeuingLimitExceeded",
					Message:   "The workload is deactivated due to exceeding the maximum number of re-queuing retries",
				},
			},
		},
		"with reason WaitForPodsStart; [backoffLimitCount: 100] should set the Evicted condition with Deactivated reason, exceeded the maximum number of requeue retries" +
			"when the .spec.active is False, Admitted, the Workload has Evicted=False and DeactivationTarget=True condition, and the requeueState.count equals to backoffLimitCount": {
			reconcilerOpts: []Option{
				WithWaitForPodsReady(&waitForPodsReadyConfig{
					timeout:                     3 * time.Second,
					requeuingBackoffLimitCount:  ptr.To[int32](100),
					requeuingBackoffBaseSeconds: 10,
					requeuingBackoffJitter:      0,
				}),
			},
			workload: utiltestingapi.MakeWorkload("wl", "ns").
				Active(false).
				ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").Obj(), now).
				AdmittedAt(true, now).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadPodsReady,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadWaitForStart,
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
			wantWorkload: utiltestingapi.MakeWorkload("wl", "ns").
				Active(false).
				ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").Obj(), now).
				AdmittedAt(true, now).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadPodsReady,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadWaitForStart,
					Message: "Not all pods are ready or succeeded",
				}).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadEvicted,
					Status:  metav1.ConditionTrue,
					Reason:  "DeactivatedDueToRequeuingLimitExceeded",
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
				SchedulingStatsEviction(
					kueue.WorkloadSchedulingStatsEviction{
						Reason:          "Deactivated",
						UnderlyingCause: "RequeuingLimitExceeded",
						Count:           1,
					},
				).
				Obj(),
			wantWorkloadUseMergePatch: utiltestingapi.MakeWorkload("wl", "ns").
				Active(false).
				ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").Obj(), now).
				AdmittedAt(true, now).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadPodsReady,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadWaitForStart,
					Message: "Not all pods are ready or succeeded",
				}).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadEvicted,
					Status:  metav1.ConditionTrue,
					Reason:  "DeactivatedDueToRequeuingLimitExceeded",
					Message: "The workload is deactivated due to exceeding the maximum number of re-queuing retries",
				}).
				SchedulingStatsEviction(
					kueue.WorkloadSchedulingStatsEviction{
						Reason:          "Deactivated",
						UnderlyingCause: "RequeuingLimitExceeded",
						Count:           1,
					},
				).
				Obj(),
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Name: "wl", Namespace: "ns"},
					EventType: corev1.EventTypeNormal,
					Reason:    "EvictedDueToDeactivatedDueToRequeuingLimitExceeded",
					Message:   "The workload is deactivated due to exceeding the maximum number of re-queuing retries",
				},
			},
		},
		"should keep the previous eviction reason when the Workload is already evicted by other reason even though the Workload is deactivated.": {
			workload: utiltestingapi.MakeWorkload("wl", "ns").
				Active(false).
				ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").Obj(), now).
				AdmittedAt(true, now).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadEvicted,
					Status:  metav1.ConditionTrue,
					Reason:  kueue.WorkloadEvictedByPodsReadyTimeout,
					Message: "Exceeded the PodsReady timeout ns",
				}).
				Obj(),
			wantWorkload: utiltestingapi.MakeWorkload("wl", "ns").
				Active(false).
				ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").Obj(), now).
				AdmittedAt(true, now).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadEvicted,
					Status:  metav1.ConditionTrue,
					Reason:  kueue.WorkloadEvictedByPodsReadyTimeout,
					Message: "Exceeded the PodsReady timeout ns",
				}).
				Obj(),
		},
		"should set the Evicted condition with ClusterQueueStopped reason when the StopPolicy is HoldAndDrain": {
			cq: utiltestingapi.MakeClusterQueue("cq").StopPolicy(kueue.HoldAndDrain).Obj(),
			lq: utiltestingapi.MakeLocalQueue("lq", "ns").ClusterQueue("cq").Obj(),
			workload: utiltestingapi.MakeWorkload("wl", "ns").
				Active(true).
				ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").Obj(), now).
				AdmittedAt(true, now).
				Queue("lq").
				Obj(),
			wantWorkload: utiltestingapi.MakeWorkload("wl", "ns").
				Active(true).
				ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").Obj(), now).
				AdmittedAt(true, now).
				Queue("lq").
				Condition(metav1.Condition{
					Type:    kueue.WorkloadEvicted,
					Status:  metav1.ConditionTrue,
					Reason:  kueue.WorkloadEvictedByClusterQueueStopped,
					Message: "The ClusterQueue is stopped",
				}).
				SchedulingStatsEviction(
					kueue.WorkloadSchedulingStatsEviction{
						Reason: kueue.WorkloadEvictedByClusterQueueStopped,
						Count:  1,
					},
				).
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
			cq: utiltestingapi.MakeClusterQueue("cq").Obj(),
			lq: utiltestingapi.MakeLocalQueue("lq", "ns").ClusterQueue("cq").StopPolicy(kueue.HoldAndDrain).Obj(),
			workload: utiltestingapi.MakeWorkload("wl", "ns").
				Active(true).
				ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").Obj(), now).
				AdmittedAt(true, now).
				Queue("lq").
				Obj(),
			wantWorkload: utiltestingapi.MakeWorkload("wl", "ns").
				Active(true).
				ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").Obj(), now).
				AdmittedAt(true, now).
				Queue("lq").
				Condition(metav1.Condition{
					Type:    kueue.WorkloadEvicted,
					Status:  metav1.ConditionTrue,
					Reason:  kueue.WorkloadEvictedByLocalQueueStopped,
					Message: "The LocalQueue is stopped",
				}).
				SchedulingStatsEviction(
					kueue.WorkloadSchedulingStatsEviction{
						Reason: kueue.WorkloadEvictedByLocalQueueStopped,
						Count:  1,
					},
				).
				Obj(),
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Name: "wl", Namespace: "ns"},
					EventType: corev1.EventTypeNormal,
					Reason:    "EvictedDueToLocalQueueStopped",
					Message:   "The LocalQueue is stopped",
				},
			},
		},
	}
	runReconcileTestCases(t, cases, fakeClock)
}
