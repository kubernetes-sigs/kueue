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

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/component-base/featuregate"
	testingclock "k8s.io/utils/clock/testing"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/resources"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/pkg/workload"
)

func TestReconcileRequeue(t *testing.T) {
	// the clock is primarily used with second rounded times
	// use the current time trimmed.
	now := time.Now().Truncate(time.Second)
	fakeClock := testingclock.NewFakeClock(now)

	cases := map[string]reconcileTestCase{
		"increment re-queue count": {
			reconcilerOpts: []Option{
				WithWaitForPodsReady(&waitForPodsReadyConfig{
					timeout:                     3 * time.Second,
					requeuingBackoffLimitCount:  new(int32(100)),
					requeuingBackoffBaseSeconds: 10,
					requeuingBackoffJitter:      0,
					requeuingBackoffMaxDuration: time.Duration(3600) * time.Second,
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
				RequeueState(new(int32(3)), nil).
				SchedulingStatsEviction(
					kueue.WorkloadSchedulingStatsEviction{
						Reason:          kueue.WorkloadEvictedByPodsReadyTimeout,
						UnderlyingCause: kueue.WorkloadWaitForRecovery,
						Count:           1,
					},
				).
				SchedulingStatsEviction(
					kueue.WorkloadSchedulingStatsEviction{
						Reason:          kueue.WorkloadEvictedByPodsReadyTimeout,
						UnderlyingCause: kueue.WorkloadWaitForStart,
						Count:           1,
					},
				).
				Generation(1).
				Obj(),
			wantWorkload: utiltestingapi.MakeWorkload("wl", "ns").
				ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").Obj(), now).
				AdmittedAt(true, now).
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
				RequeueState(new(int32(4)), new(metav1.NewTime(now.Add(80*time.Second).Truncate(time.Second)))).
				// check EvictionState mergeStrategy
				SchedulingStatsEviction(
					kueue.WorkloadSchedulingStatsEviction{
						Reason:          kueue.WorkloadEvictedByPodsReadyTimeout,
						UnderlyingCause: kueue.WorkloadWaitForRecovery,
						Count:           1,
					},
				).
				SchedulingStatsEviction(
					kueue.WorkloadSchedulingStatsEviction{
						Reason:          kueue.WorkloadEvictedByPodsReadyTimeout,
						UnderlyingCause: kueue.WorkloadWaitForStart,
						Count:           2,
					},
				).
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
		"evict with the WaitForScheduling cause when the unscheduledTimeout is exceeded": {
			featureGates: map[featuregate.Feature]bool{
				features.WaitForPodsReadyUnscheduledTimeout: true,
			},
			reconcilerOpts: []Option{
				WithWaitForPodsReady(&waitForPodsReadyConfig{
					timeout:                     30 * time.Minute,
					unscheduledTimeout:          new(3 * time.Second),
					requeuingBackoffLimitCount:  new(int32(100)),
					requeuingBackoffBaseSeconds: 10,
					requeuingBackoffJitter:      0,
					requeuingBackoffMaxDuration: time.Hour,
				}),
			},
			workload: utiltestingapi.MakeWorkload("wl", "ns").
				ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").Obj(), now).
				Condition(metav1.Condition{
					Type:               kueue.WorkloadAdmitted,
					Status:             metav1.ConditionTrue,
					LastTransitionTime: metav1.NewTime(now.Add(-5 * time.Minute)),
					Reason:             "ByTest",
					Message:            "Admitted by ClusterQueue q1",
				}).
				AdmittedAt(true, now).
				Condition(metav1.Condition{
					Type:               kueue.WorkloadPodsScheduled,
					Status:             metav1.ConditionFalse,
					Reason:             kueue.WorkloadWaitForScheduling,
					Message:            "At least one required pod is not scheduled",
					LastTransitionTime: metav1.NewTime(now.Add(-4 * time.Minute)),
					ObservedGeneration: 1,
				}).
				Generation(1).
				Condition(metav1.Condition{
					Type:               kueue.WorkloadPodsReady,
					Status:             metav1.ConditionFalse,
					Reason:             kueue.WorkloadWaitForScheduling,
					LastTransitionTime: metav1.NewTime(now.Add(-4 * time.Minute)),
				}).
				Obj(),
			wantWorkload: utiltestingapi.MakeWorkload("wl", "ns").
				ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").Obj(), now).
				Condition(metav1.Condition{
					Type:               kueue.WorkloadPodsScheduled,
					Status:             metav1.ConditionFalse,
					Reason:             kueue.WorkloadWaitForScheduling,
					Message:            "At least one required pod is not scheduled",
					LastTransitionTime: metav1.NewTime(now.Add(-4 * time.Minute)),
					ObservedGeneration: 1,
				}).
				Condition(metav1.Condition{
					Type:               kueue.WorkloadAdmitted,
					Status:             metav1.ConditionTrue,
					LastTransitionTime: metav1.NewTime(now.Add(-5 * time.Minute)),
					Reason:             "ByTest",
					Message:            "Admitted by ClusterQueue q1",
				}).
				AdmittedAt(true, now).
				Generation(1).
				Condition(metav1.Condition{
					Type:               kueue.WorkloadPodsReady,
					Status:             metav1.ConditionFalse,
					Reason:             kueue.WorkloadWaitForScheduling,
					LastTransitionTime: metav1.NewTime(now.Add(-4 * time.Minute)),
				}).
				Condition(metav1.Condition{
					Type:               kueue.WorkloadEvicted,
					Status:             metav1.ConditionTrue,
					Reason:             kueue.WorkloadEvictedByPodsReadyTimeout,
					Message:            "Exceeded the PodsReady timeout ns/wl",
					ObservedGeneration: 1,
				}).
				RequeueState(new(int32(1)), new(metav1.NewTime(now.Add(10*time.Second).Truncate(time.Second)))).
				SchedulingStatsEviction(
					kueue.WorkloadSchedulingStatsEviction{
						Reason:          kueue.WorkloadEvictedByPodsReadyTimeout,
						UnderlyingCause: kueue.WorkloadWaitForScheduling,
						Count:           1,
					},
				).
				Obj(),
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Name: "wl", Namespace: "ns"},
					EventType: corev1.EventTypeNormal,
					Reason:    "EvictedDueToPodsReadyTimeoutDueToWaitForScheduling",
					Message:   "Exceeded the PodsReady timeout ns/wl",
				},
			},
		},
		"gate off keeps the same unscheduled workload until the normal timeout": {
			reconcilerOpts: []Option{
				WithWaitForPodsReady(&waitForPodsReadyConfig{
					timeout:                     30 * time.Minute,
					unscheduledTimeout:          new(3 * time.Second),
					requeuingBackoffLimitCount:  new(int32(100)),
					requeuingBackoffBaseSeconds: 10,
					requeuingBackoffJitter:      0,
					requeuingBackoffMaxDuration: time.Hour,
				}),
			},
			workload: utiltestingapi.MakeWorkload("wl", "ns").
				ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").Obj(), now).
				Condition(metav1.Condition{
					Type:               kueue.WorkloadAdmitted,
					Status:             metav1.ConditionTrue,
					LastTransitionTime: metav1.NewTime(now.Add(-5 * time.Minute)),
					Reason:             "ByTest",
					Message:            "Admitted by ClusterQueue q1",
				}).
				AdmittedAt(true, now).
				Condition(metav1.Condition{
					Type:               kueue.WorkloadPodsScheduled,
					Status:             metav1.ConditionFalse,
					Reason:             kueue.WorkloadWaitForScheduling,
					Message:            "At least one required pod is not scheduled",
					LastTransitionTime: metav1.NewTime(now.Add(-4 * time.Minute)),
					ObservedGeneration: 1,
				}).
				Generation(1).
				Condition(metav1.Condition{
					Type:               kueue.WorkloadPodsReady,
					Status:             metav1.ConditionFalse,
					Reason:             kueue.WorkloadWaitForScheduling,
					LastTransitionTime: metav1.NewTime(now.Add(-4 * time.Minute)),
				}).
				Obj(),
			wantWorkload: utiltestingapi.MakeWorkload("wl", "ns").
				ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").Obj(), now).
				Condition(metav1.Condition{
					Type:               kueue.WorkloadAdmitted,
					Status:             metav1.ConditionTrue,
					LastTransitionTime: metav1.NewTime(now.Add(-5 * time.Minute)),
					Reason:             "ByTest",
					Message:            "Admitted by ClusterQueue q1",
				}).
				AdmittedAt(true, now).
				Condition(metav1.Condition{
					Type:               kueue.WorkloadPodsScheduled,
					Status:             metav1.ConditionFalse,
					Reason:             kueue.WorkloadWaitForScheduling,
					Message:            "At least one required pod is not scheduled",
					LastTransitionTime: metav1.NewTime(now.Add(-4 * time.Minute)),
					ObservedGeneration: 1,
				}).
				Generation(1).
				Condition(metav1.Condition{
					Type:               kueue.WorkloadPodsReady,
					Status:             metav1.ConditionFalse,
					Reason:             kueue.WorkloadWaitForScheduling,
					LastTransitionTime: metav1.NewTime(now.Add(-4 * time.Minute)),
				}).
				Obj(),
			wantResult: reconcile.Result{RequeueAfter: 25 * time.Minute},
		},
		"gate off ignores leftover scheduling state and evicts at the normal timeout": {
			featureGates: map[featuregate.Feature]bool{
				features.MultiKueue: true,
			},
			reconcilerOpts: []Option{
				WithWaitForPodsReady(&waitForPodsReadyConfig{
					timeout:                     3 * time.Minute,
					unscheduledTimeout:          new(3 * time.Second),
					requeuingBackoffLimitCount:  new(int32(100)),
					requeuingBackoffBaseSeconds: 10,
					requeuingBackoffJitter:      0,
					requeuingBackoffMaxDuration: time.Hour,
				}),
			},
			workload: utiltestingapi.MakeWorkload("wl", "ns").
				ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").Obj(), now).
				Condition(metav1.Condition{
					Type:               kueue.WorkloadAdmitted,
					Status:             metav1.ConditionTrue,
					LastTransitionTime: metav1.NewTime(now.Add(-5 * time.Minute)),
					Reason:             "ByTest",
					Message:            "Admitted by ClusterQueue q1",
				}).
				AdmittedAt(true, now).
				AdmissionCheck(kueue.AdmissionCheckState{Name: "multikueue", State: kueue.CheckStateReady}).
				Condition(metav1.Condition{
					Type:               kueue.WorkloadPodsScheduled,
					Status:             metav1.ConditionFalse,
					Reason:             kueue.WorkloadWaitForScheduling,
					Message:            "At least one required pod is not scheduled",
					LastTransitionTime: metav1.NewTime(now.Add(-4 * time.Minute)),
					ObservedGeneration: 1,
				}).
				Generation(1).
				Condition(metav1.Condition{
					Type:               kueue.WorkloadPodsReady,
					Status:             metav1.ConditionFalse,
					Reason:             kueue.WorkloadWaitForScheduling,
					LastTransitionTime: metav1.NewTime(now.Add(-4 * time.Minute)),
				}).
				Obj(),
			wantWorkload: utiltestingapi.MakeWorkload("wl", "ns").
				ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").Obj(), now).
				Condition(metav1.Condition{
					Type:               kueue.WorkloadAdmitted,
					Status:             metav1.ConditionTrue,
					LastTransitionTime: metav1.NewTime(now.Add(-5 * time.Minute)),
					Reason:             "ByTest",
					Message:            "Admitted by ClusterQueue q1",
				}).
				AdmittedAt(true, now).
				AdmissionCheck(kueue.AdmissionCheckState{Name: "multikueue", State: kueue.CheckStatePending, Message: "Reset to Pending after eviction. Previously: Ready"}).
				Condition(metav1.Condition{
					Type:               kueue.WorkloadPodsScheduled,
					Status:             metav1.ConditionFalse,
					Reason:             kueue.WorkloadWaitForScheduling,
					Message:            "At least one required pod is not scheduled",
					LastTransitionTime: metav1.NewTime(now.Add(-4 * time.Minute)),
					ObservedGeneration: 1,
				}).
				Generation(1).
				Condition(metav1.Condition{
					Type:               kueue.WorkloadPodsReady,
					Status:             metav1.ConditionFalse,
					Reason:             kueue.WorkloadWaitForScheduling,
					LastTransitionTime: metav1.NewTime(now.Add(-4 * time.Minute)),
				}).
				Condition(metav1.Condition{
					Type:               kueue.WorkloadEvicted,
					Status:             metav1.ConditionTrue,
					Reason:             kueue.WorkloadEvictedByPodsReadyTimeout,
					Message:            "Exceeded the PodsReady timeout ns/wl",
					ObservedGeneration: 1,
				}).
				RequeueState(new(int32(1)), new(metav1.NewTime(now.Add(10*time.Second).Truncate(time.Second)))).
				SchedulingStatsEviction(
					kueue.WorkloadSchedulingStatsEviction{
						Reason:          kueue.WorkloadEvictedByPodsReadyTimeout,
						UnderlyingCause: kueue.WorkloadWaitForStart,
						Count:           1,
					},
				).
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
		"keep a workload delegated to a MultiKueue worker until the timeout is exceeded": {
			featureGates: map[featuregate.Feature]bool{
				features.WaitForPodsReadyUnscheduledTimeout: true,
				features.MultiKueue:                         true,
			},
			reconcilerOpts: []Option{
				WithWaitForPodsReady(&waitForPodsReadyConfig{
					timeout:                     30 * time.Minute,
					unscheduledTimeout:          new(3 * time.Second),
					requeuingBackoffLimitCount:  new(int32(100)),
					requeuingBackoffBaseSeconds: 10,
					requeuingBackoffJitter:      0,
					requeuingBackoffMaxDuration: time.Hour,
				}),
			},
			workload: utiltestingapi.MakeWorkload("wl", "ns").
				ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").Obj(), now).
				Condition(metav1.Condition{
					Type:               kueue.WorkloadAdmitted,
					Status:             metav1.ConditionTrue,
					LastTransitionTime: metav1.NewTime(now.Add(-5 * time.Minute)),
					Reason:             "ByTest",
					Message:            "Admitted by ClusterQueue q1",
				}).
				AdmittedAt(true, now).
				Generation(1).
				Condition(metav1.Condition{
					Type:               kueue.WorkloadPodsReady,
					Status:             metav1.ConditionFalse,
					Reason:             kueue.WorkloadWaitForStart,
					LastTransitionTime: metav1.NewTime(now.Add(-4 * time.Minute)),
				}).
				AdmissionCheck(kueue.AdmissionCheckState{Name: "multikueue", State: kueue.CheckStateReady}).
				Obj(),
			wantWorkload: utiltestingapi.MakeWorkload("wl", "ns").
				ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").Obj(), now).
				Condition(metav1.Condition{
					Type:               kueue.WorkloadAdmitted,
					Status:             metav1.ConditionTrue,
					LastTransitionTime: metav1.NewTime(now.Add(-5 * time.Minute)),
					Reason:             "ByTest",
					Message:            "Admitted by ClusterQueue q1",
				}).
				AdmittedAt(true, now).
				Generation(1).
				Condition(metav1.Condition{
					Type:               kueue.WorkloadPodsReady,
					Status:             metav1.ConditionFalse,
					Reason:             kueue.WorkloadWaitForStart,
					LastTransitionTime: metav1.NewTime(now.Add(-4 * time.Minute)),
				}).
				AdmissionCheck(kueue.AdmissionCheckState{Name: "multikueue", State: kueue.CheckStateReady}).
				Obj(),
			wantResult: reconcile.Result{RequeueAfter: 25 * time.Minute},
		},
		"evict a workload delegated to a MultiKueue worker with the WaitForStart cause when the timeout is exceeded": {
			featureGates: map[featuregate.Feature]bool{
				features.WaitForPodsReadyUnscheduledTimeout: true,
				features.MultiKueue:                         true,
			},
			reconcilerOpts: []Option{
				WithWaitForPodsReady(&waitForPodsReadyConfig{
					timeout:                     3 * time.Second,
					unscheduledTimeout:          new(3 * time.Second),
					requeuingBackoffLimitCount:  new(int32(100)),
					requeuingBackoffBaseSeconds: 10,
					requeuingBackoffJitter:      0,
					requeuingBackoffMaxDuration: time.Hour,
				}),
			},
			workload: utiltestingapi.MakeWorkload("wl", "ns").
				ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").Obj(), now).
				Condition(metav1.Condition{
					Type:               kueue.WorkloadAdmitted,
					Status:             metav1.ConditionTrue,
					LastTransitionTime: metav1.NewTime(now.Add(-5 * time.Minute)),
					Reason:             "ByTest",
					Message:            "Admitted by ClusterQueue q1",
				}).
				AdmittedAt(true, now).
				Generation(1).
				Condition(metav1.Condition{
					Type:               kueue.WorkloadPodsReady,
					Status:             metav1.ConditionFalse,
					Reason:             kueue.WorkloadWaitForStart,
					LastTransitionTime: metav1.NewTime(now.Add(-4 * time.Minute)),
				}).
				AdmissionCheck(kueue.AdmissionCheckState{Name: "multikueue", State: kueue.CheckStateReady}).
				Obj(),
			wantWorkload: utiltestingapi.MakeWorkload("wl", "ns").
				ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").Obj(), now).
				Condition(metav1.Condition{
					Type:               kueue.WorkloadAdmitted,
					Status:             metav1.ConditionTrue,
					LastTransitionTime: metav1.NewTime(now.Add(-5 * time.Minute)),
					Reason:             "ByTest",
					Message:            "Admitted by ClusterQueue q1",
				}).
				AdmittedAt(true, now).
				Generation(1).
				Condition(metav1.Condition{
					Type:               kueue.WorkloadPodsReady,
					Status:             metav1.ConditionFalse,
					Reason:             kueue.WorkloadWaitForStart,
					LastTransitionTime: metav1.NewTime(now.Add(-4 * time.Minute)),
				}).
				AdmissionCheck(kueue.AdmissionCheckState{
					Name:    "multikueue",
					State:   kueue.CheckStatePending,
					Message: "Reset to Pending after eviction. Previously: Ready",
				}).
				Condition(metav1.Condition{
					Type:               kueue.WorkloadEvicted,
					Status:             metav1.ConditionTrue,
					Reason:             kueue.WorkloadEvictedByPodsReadyTimeout,
					Message:            "Exceeded the PodsReady timeout ns/wl",
					ObservedGeneration: 1,
				}).
				RequeueState(new(int32(1)), new(metav1.NewTime(now.Add(10*time.Second).Truncate(time.Second)))).
				SchedulingStatsEviction(
					kueue.WorkloadSchedulingStatsEviction{
						Reason:          kueue.WorkloadEvictedByPodsReadyTimeout,
						UnderlyingCause: kueue.WorkloadWaitForStart,
						Count:           1,
					},
				).
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
		"wait time should be limited to backoffMaxSeconds": {
			reconcilerOpts: []Option{
				WithWaitForPodsReady(&waitForPodsReadyConfig{
					timeout:                     3 * time.Second,
					requeuingBackoffLimitCount:  new(int32(100)),
					requeuingBackoffBaseSeconds: 10,
					requeuingBackoffJitter:      0,
					requeuingBackoffMaxDuration: time.Duration(7200) * time.Second,
				}),
			},
			workload: utiltestingapi.MakeWorkload("wl", "ns").
				ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").Obj(), now).
				AdmissionCheck(kueue.AdmissionCheckState{
					Name:  "check",
					State: kueue.CheckStateReady,
				}).
				Generation(1).
				Condition(metav1.Condition{ // Override LastTransitionTime
					Type:               kueue.WorkloadAdmitted,
					Status:             metav1.ConditionTrue,
					LastTransitionTime: metav1.NewTime(now.Add(-5 * time.Minute)),
					Reason:             "ByTest",
					Message:            "Admitted by ClusterQueue q1",
				}).
				AdmittedAt(true, now).
				RequeueState(new(int32(10)), new(metav1.NewTime(now.Add(-1*time.Second).Truncate(time.Second)))).
				Obj(),
			wantWorkload: utiltestingapi.MakeWorkload("wl", "ns").
				ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").Obj(), now).
				AdmittedAt(true, now).
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
				RequeueState(new(int32(11)), new(metav1.NewTime(now.Add(7200*time.Second).Truncate(time.Second)))).
				SchedulingStatsEviction(
					kueue.WorkloadSchedulingStatsEviction{
						Reason:          kueue.WorkloadEvictedByPodsReadyTimeout,
						UnderlyingCause: kueue.WorkloadWaitForStart,
						Count:           1,
					},
				).
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
		"recovery time should be limited to recoveryTimeout": {
			reconcilerOpts: []Option{
				WithWaitForPodsReady(&waitForPodsReadyConfig{
					timeout:                     5 * time.Minute,
					recoveryTimeout:             new(3 * time.Second),
					requeuingBackoffLimitCount:  new(int32(100)),
					requeuingBackoffBaseSeconds: 10,
					requeuingBackoffJitter:      0,
					requeuingBackoffMaxDuration: time.Duration(7200) * time.Second,
				}),
			},
			workload: utiltestingapi.MakeWorkload("wl", "ns").
				ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").Obj(), now).
				AdmissionCheck(kueue.AdmissionCheckState{
					Name:  "check",
					State: kueue.CheckStateReady,
				}).
				Generation(1).
				Condition(metav1.Condition{ // Override LastTransitionTime
					Type:               kueue.WorkloadAdmitted,
					Status:             metav1.ConditionTrue,
					LastTransitionTime: metav1.NewTime(now.Add(-5 * time.Minute)),
					Reason:             "ByTest",
					Message:            "Admitted by ClusterQueue q1",
				}).
				Condition(metav1.Condition{
					Type:               kueue.WorkloadPodsReady,
					Status:             metav1.ConditionFalse,
					LastTransitionTime: metav1.NewTime(now.Add(-3 * time.Minute)),
					Reason:             kueue.WorkloadWaitForRecovery,
					Message:            "At least one pod has failed, waiting for recovery",
				}).
				AdmittedAt(true, now).
				Obj(),
			wantWorkload: utiltestingapi.MakeWorkload("wl", "ns").
				ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").Obj(), now).
				AdmittedAt(true, now).
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
				Condition(metav1.Condition{
					Type:               kueue.WorkloadPodsReady,
					Status:             metav1.ConditionFalse,
					LastTransitionTime: metav1.NewTime(now),
					Reason:             kueue.WorkloadWaitForRecovery,
					Message:            "At least one pod has failed, waiting for recovery",
				}).
				//  10s * 2^(11-1) = 10240s > requeuingBackoffMaxSeconds; then wait time should be limited to requeuingBackoffMaxSeconds
				RequeueState(new(int32(1)), new(metav1.NewTime(now.Add(7200*time.Second).Truncate(time.Second)))).
				SchedulingStatsEviction(
					kueue.WorkloadSchedulingStatsEviction{
						Reason:          kueue.WorkloadEvictedByPodsReadyTimeout,
						UnderlyingCause: kueue.WorkloadWaitForRecovery,
						Count:           1,
					},
				).
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
		"recovery time with unhealthy nodes should still be limited to recoveryTimeout": {
			reconcilerOpts: []Option{
				WithWaitForPodsReady(&waitForPodsReadyConfig{
					timeout:                     5 * time.Minute,
					recoveryTimeout:             new(3 * time.Second),
					requeuingBackoffLimitCount:  new(int32(100)),
					requeuingBackoffBaseSeconds: 10,
					requeuingBackoffJitter:      0,
					requeuingBackoffMaxDuration: time.Duration(7200) * time.Second,
				}),
			},
			workload: utiltestingapi.MakeWorkload("wl", "ns").
				ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").Obj(), now).
				AdmissionCheck(kueue.AdmissionCheckState{
					Name:  "check",
					State: kueue.CheckStateReady,
				}).
				Generation(1).
				Condition(metav1.Condition{
					Type:               kueue.WorkloadAdmitted,
					Status:             metav1.ConditionTrue,
					LastTransitionTime: metav1.NewTime(now.Add(-5 * time.Minute)),
					Reason:             "ByTest",
					Message:            "Admitted by ClusterQueue q1",
				}).
				Condition(metav1.Condition{
					Type:               kueue.WorkloadPodsReady,
					Status:             metav1.ConditionFalse,
					LastTransitionTime: metav1.NewTime(now.Add(-3 * time.Minute)),
					Reason:             kueue.WorkloadWaitForRecovery,
					Message:            "At least one pod has failed, waiting for recovery",
				}).
				AdmittedAt(true, now).
				UnhealthyNodes("xyz").
				Obj(),
			wantWorkload: utiltestingapi.MakeWorkload("wl", "ns").
				ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").Obj(), now).
				AdmittedAt(true, now).
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
				Condition(metav1.Condition{
					Type:               kueue.WorkloadPodsReady,
					Status:             metav1.ConditionFalse,
					LastTransitionTime: metav1.NewTime(now),
					Reason:             kueue.WorkloadWaitForRecovery,
					Message:            "At least one pod has failed, waiting for recovery",
				}).
				AdmittedAt(true, now).
				UnhealthyNodes("xyz").
				RequeueState(new(int32(1)), new(metav1.NewTime(now.Add(7200*time.Second).Truncate(time.Second)))).
				SchedulingStatsEviction(
					kueue.WorkloadSchedulingStatsEviction{
						Reason:          kueue.WorkloadEvictedByPodsReadyTimeout,
						UnderlyingCause: kueue.WorkloadWaitForRecovery,
						Count:           1,
					},
				).
				Obj(),
			wantWorkloadUseMergePatch: utiltestingapi.MakeWorkload("wl", "ns").
				ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").Obj(), now).
				AdmittedAt(true, now).
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
				Condition(metav1.Condition{
					Type:               kueue.WorkloadPodsReady,
					Status:             metav1.ConditionFalse,
					LastTransitionTime: metav1.NewTime(now),
					Reason:             kueue.WorkloadWaitForRecovery,
					Message:            "At least one pod has failed, waiting for recovery",
				}).
				AdmittedAt(true, now).
				RequeueState(new(int32(1)), new(metav1.NewTime(now.Add(7200*time.Second).Truncate(time.Second)))).
				SchedulingStatsEviction(
					kueue.WorkloadSchedulingStatsEviction{
						Reason:          kueue.WorkloadEvictedByPodsReadyTimeout,
						UnderlyingCause: kueue.WorkloadWaitForRecovery,
						Count:           1,
					},
				).
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
			workload: utiltestingapi.MakeWorkload("wl", "ns").
				Active(true).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadRequeued,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadDeactivated,
					Message: "The workload is deactivated",
				}).
				Obj(),
			wantWorkload: utiltestingapi.MakeWorkload("wl", "ns").
				Active(true).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadRequeued,
					Status:  metav1.ConditionTrue,
					Reason:  kueue.WorkloadReactivated,
					Message: "The workload was reactivated",
				}).
				Obj(),
		},
		"should set the WorkloadRequeued condition to true on re-activated with a derived deactivation reason": {
			workload: utiltestingapi.MakeWorkload("wl", "ns").
				Active(true).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadRequeued,
					Status:  metav1.ConditionFalse,
					Reason:  "DeactivatedDueToRequeuingLimitExceeded",
					Message: "The workload is deactivated due to exceeding the maximum number of re-queuing retries",
				}).
				Obj(),
			wantWorkload: utiltestingapi.MakeWorkload("wl", "ns").
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
			workload: utiltestingapi.MakeWorkload("wl", "ns").
				Active(true).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadRequeued,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadEvictedByPodsReadyTimeout,
					Message: "Exceeded the PodsReady timeout ns",
				}).
				RequeueState(new(int32(1)), new(metav1.NewTime(now.Add(60*time.Second).Truncate(time.Second)))).
				Obj(),
			wantWorkload: utiltestingapi.MakeWorkload("wl", "ns").
				Active(true).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadRequeued,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadEvictedByPodsReadyTimeout,
					Message: "Exceeded the PodsReady timeout ns",
				}).
				RequeueState(new(int32(1)), new(metav1.NewTime(now.Add(60*time.Second).Truncate(time.Second)))).
				Obj(),
			wantResult: reconcile.Result{RequeueAfter: time.Minute},
		},
		"should set the WorkloadRequeued condition when the WaitForPodsReady backoff expires": {
			workload: utiltestingapi.MakeWorkload("wl", "ns").
				Active(true).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadRequeued,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadEvictedByPodsReadyTimeout,
					Message: "Exceeded the PodsReady timeout ns",
				}).
				Obj(),
			wantWorkload: utiltestingapi.MakeWorkload("wl", "ns").
				Active(true).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadRequeued,
					Status:  metav1.ConditionTrue,
					Reason:  kueue.WorkloadBackoffFinished,
					Message: "The workload backoff was finished",
				}).
				Obj(),
		},
		"should keep the WorkloadRequeued condition until the AdmissionCheck backoff expires": {
			workload: utiltestingapi.MakeWorkload("wl", "ns").
				Active(true).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadRequeued,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadEvictedByAdmissionCheck,
					Message: "Exceeded the AdmissionCheck timeout ns",
				}).
				RequeueState(new(int32(1)), new(metav1.NewTime(now.Add(60*time.Second).Truncate(time.Second)))).
				Obj(),
			wantWorkload: utiltestingapi.MakeWorkload("wl", "ns").
				Active(true).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadRequeued,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadEvictedByAdmissionCheck,
					Message: "Exceeded the AdmissionCheck timeout ns",
				}).
				RequeueState(new(int32(1)), new(metav1.NewTime(now.Add(60*time.Second).Truncate(time.Second)))).
				Obj(),
			wantResult: reconcile.Result{RequeueAfter: time.Minute},
		},
		"should set the WorkloadRequeued condition when the AdmissionCheck backoff expires": {
			workload: utiltestingapi.MakeWorkload("wl", "ns").
				Active(true).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadRequeued,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadEvictedByAdmissionCheck,
					Message: "Exceeded the AdmissionCheck timeout ns",
				}).
				Obj(),
			wantWorkload: utiltestingapi.MakeWorkload("wl", "ns").
				Active(true).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadRequeued,
					Status:  metav1.ConditionTrue,
					Reason:  kueue.WorkloadBackoffFinished,
					Message: "The workload backoff was finished",
				}).
				Obj(),
		},
		"scale-to-zero released workload should not be requeued while transitioning back to pending": {
			workload: utiltestingapi.MakeWorkload("wl", "ns").
				Queue("lq").
				Active(true).
				ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").Obj(), now).
				AdmittedAt(true, now).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadQuotaReserved,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadOnHold,
					Message: "StatefulSet scaled to zero; workload on hold",
				}).
				Obj(),
			cq:                   utiltestingapi.MakeClusterQueue("cq").Obj(),
			lq:                   utiltestingapi.MakeLocalQueue("lq", "ns").ClusterQueue("cq").Obj(),
			wantWorkloadsInQueue: new(int),
		},
		"shouldn't set the WorkloadRequeued condition when backoff expires and workload finished": {
			workload: utiltestingapi.MakeWorkload("wl", "ns").
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
				RequeueState(new(int32(1)), new(metav1.NewTime(now.Truncate(time.Second)))).
				Obj(),
			wantWorkload: utiltestingapi.MakeWorkload("wl", "ns").
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
				RequeueState(new(int32(1)), new(metav1.NewTime(now.Truncate(time.Second)))).
				Obj(),
		},
		"should set the WorkloadRequeued condition to true on ClusterQueue started": {
			cq: utiltestingapi.MakeClusterQueue("cq").Obj(),
			lq: utiltestingapi.MakeLocalQueue("lq", "ns").ClusterQueue("cq").Obj(),
			workload: utiltestingapi.MakeWorkload("wl", "ns").
				Active(true).
				Queue("lq").
				Condition(metav1.Condition{
					Type:    kueue.WorkloadRequeued,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadEvictedByClusterQueueStopped,
					Message: "The ClusterQueue is stopped",
				}).
				Obj(),
			wantWorkload: utiltestingapi.MakeWorkload("wl", "ns").
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
			cq: utiltestingapi.MakeClusterQueue("cq").Obj(),
			lq: utiltestingapi.MakeLocalQueue("lq", "ns").ClusterQueue("cq").Obj(),
			workload: utiltestingapi.MakeWorkload("wl", "ns").
				Active(true).
				Queue("lq").
				Condition(metav1.Condition{
					Type:    kueue.WorkloadRequeued,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadEvictedByLocalQueueStopped,
					Message: "The LocalQueue is stopped",
				}).
				Obj(),
			wantWorkload: utiltestingapi.MakeWorkload("wl", "ns").
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
		"should update requeueState.requeueAt when admission check sets a delay": {
			workload: utiltestingapi.MakeWorkload("wl", "ns").
				RequeueState(nil, new(metav1.NewTime(now.Add(5*time.Second)))).
				AdmissionCheck(kueue.AdmissionCheckState{
					Name:                "check",
					State:               kueue.CheckStateRetry,
					RequeueAfterSeconds: new(int32(10)),
					LastTransitionTime:  metav1.NewTime(now),
				}).
				Obj(),
			wantWorkload: utiltestingapi.MakeWorkload("wl", "ns").
				RequeueState(nil, new(metav1.NewTime(now.Add(10*time.Second)))).
				AdmissionCheck(kueue.AdmissionCheckState{
					Name:                "check",
					State:               kueue.CheckStateRetry,
					RequeueAfterSeconds: new(int32(10)),
					LastTransitionTime:  metav1.NewTime(now),
				}).
				Obj(),
			wantWorkloadUseMergePatch: utiltestingapi.MakeWorkload("wl", "ns").
				RequeueState(nil, new(metav1.NewTime(now.Add(10*time.Second)))).
				AdmissionCheck(kueue.AdmissionCheckState{
					Name:                "check",
					State:               kueue.CheckStateRetry,
					RequeueAfterSeconds: new(int32(10)),
					LastTransitionTime:  metav1.NewTime(now),
				}).
				Obj(),
			wantResult: reconcile.Result{},
		},
		"should not update requeueState.requeueAt when admission check delay is smaller": {
			workload: utiltestingapi.MakeWorkload("wl", "ns").
				RequeueState(nil, new(metav1.NewTime(now.Add(10*time.Second)))).
				AdmissionCheck(kueue.AdmissionCheckState{
					Name:                "check",
					State:               kueue.CheckStateRetry,
					RequeueAfterSeconds: new(int32(5)),
					LastTransitionTime:  metav1.NewTime(now),
				}).
				Obj(),
			wantWorkload: utiltestingapi.MakeWorkload("wl", "ns").
				RequeueState(nil, new(metav1.NewTime(now.Add(10*time.Second)))).
				AdmissionCheck(kueue.AdmissionCheckState{
					Name:                "check",
					State:               kueue.CheckStateRetry,
					RequeueAfterSeconds: new(int32(5)),
					LastTransitionTime:  metav1.NewTime(now),
				}).
				Obj(),
			wantWorkloadUseMergePatch: utiltestingapi.MakeWorkload("wl", "ns").
				RequeueState(nil, new(metav1.NewTime(now.Add(10*time.Second)))).
				AdmissionCheck(kueue.AdmissionCheckState{
					Name:                "check",
					State:               kueue.CheckStateRetry,
					RequeueAfterSeconds: new(int32(5)),
					LastTransitionTime:  metav1.NewTime(now),
				}).
				Obj(),
			wantResult: reconcile.Result{RequeueAfter: 10 * time.Second},
		},
		"should always use the biggest admission check delay": {
			workload: utiltestingapi.MakeWorkload("wl", "ns").
				AdmissionCheck(kueue.AdmissionCheckState{
					Name:                "check1",
					State:               kueue.CheckStateRetry,
					RequeueAfterSeconds: new(int32(5)),
					LastTransitionTime:  metav1.NewTime(now),
				}).
				AdmissionCheck(kueue.AdmissionCheckState{
					Name:                "check2",
					State:               kueue.CheckStateRetry,
					RequeueAfterSeconds: new(int32(7)),
					LastTransitionTime:  metav1.NewTime(now),
				}).
				Obj(),
			wantWorkload: utiltestingapi.MakeWorkload("wl", "ns").
				RequeueState(nil, new(metav1.NewTime(now.Add(10*time.Second)))).
				AdmissionCheck(kueue.AdmissionCheckState{
					Name:                "check1",
					State:               kueue.CheckStateRetry,
					RequeueAfterSeconds: new(int32(5)),
					LastTransitionTime:  metav1.NewTime(now),
				}).
				AdmissionCheck(kueue.AdmissionCheckState{
					Name:                "check2",
					State:               kueue.CheckStateRetry,
					RequeueAfterSeconds: new(int32(7)),
					LastTransitionTime:  metav1.NewTime(now),
				}).
				Obj(),
			wantWorkloadUseMergePatch: utiltestingapi.MakeWorkload("wl", "ns").
				RequeueState(nil, new(metav1.NewTime(now.Add(10*time.Second)))).
				AdmissionCheck(kueue.AdmissionCheckState{
					Name:                "check1",
					State:               kueue.CheckStateRetry,
					RequeueAfterSeconds: new(int32(5)),
					LastTransitionTime:  metav1.NewTime(now),
				}).
				AdmissionCheck(kueue.AdmissionCheckState{
					Name:                "check2",
					State:               kueue.CheckStateRetry,
					RequeueAfterSeconds: new(int32(7)),
					LastTransitionTime:  metav1.NewTime(now),
				}).
				Obj(),
			wantResult: reconcile.Result{},
		},
		"requeue after backoff accounts for the effective cpu request": {
			cq: utiltestingapi.MakeClusterQueue("cq").Obj(),
			lq: utiltestingapi.MakeLocalQueue("lq", "ns").ClusterQueue("cq").Obj(),
			workload: utiltestingapi.MakeWorkload("wl", "ns").
				Queue("lq").
				Active(true).
				Limit(corev1.ResourceCPU, "3").
				RequeueState(new(int32(1)), new(metav1.NewTime(now.Add(-time.Minute)))).
				Obj(),
			wantPendingWorkloads: map[kueue.ClusterQueueReference]map[workload.Reference]*workload.Info{
				"cq": {
					"ns/wl": {
						TotalRequests: []workload.PodSetResources{
							{
								Name:  kueue.DefaultPodSetName,
								Count: 1,
								Requests: resources.NewRequestsFromMap(map[corev1.ResourceName]int64{
									corev1.ResourceCPU: 3000,
								}),
							},
						},
					},
				},
			},
		},
		"should use the biggest total time not the biggest RequeueAfterSeconds": {
			workload: utiltestingapi.MakeWorkload("wl", "ns").
				AdmissionCheck(kueue.AdmissionCheckState{
					Name:                "check1",
					State:               kueue.CheckStateRetry,
					RequeueAfterSeconds: new(int32(5)),
					LastTransitionTime:  metav1.NewTime(now.Add(10 * time.Second)),
				}).
				AdmissionCheck(kueue.AdmissionCheckState{
					Name:                "check2",
					State:               kueue.CheckStateRetry,
					RequeueAfterSeconds: new(int32(10)),
					LastTransitionTime:  metav1.NewTime(now),
				}).
				Obj(),
			wantWorkload: utiltestingapi.MakeWorkload("wl", "ns").
				RequeueState(nil, new(metav1.NewTime(now.Add(15*time.Second)))).
				AdmissionCheck(kueue.AdmissionCheckState{
					Name:                "check1",
					State:               kueue.CheckStateRetry,
					RequeueAfterSeconds: new(int32(5)),
					LastTransitionTime:  metav1.NewTime(now.Add(10 * time.Second)),
				}).
				AdmissionCheck(kueue.AdmissionCheckState{
					Name:                "check2",
					State:               kueue.CheckStateRetry,
					RequeueAfterSeconds: new(int32(10)),
					LastTransitionTime:  metav1.NewTime(now),
				}).
				Obj(),
			wantWorkloadUseMergePatch: utiltestingapi.MakeWorkload("wl", "ns").
				RequeueState(nil, new(metav1.NewTime(now.Add(15*time.Second)))).
				AdmissionCheck(kueue.AdmissionCheckState{
					Name:                "check1",
					State:               kueue.CheckStateRetry,
					RequeueAfterSeconds: new(int32(5)),
					LastTransitionTime:  metav1.NewTime(now.Add(10 * time.Second)),
				}).
				AdmissionCheck(kueue.AdmissionCheckState{
					Name:                "check2",
					State:               kueue.CheckStateRetry,
					RequeueAfterSeconds: new(int32(10)),
					LastTransitionTime:  metav1.NewTime(now),
				}).
				Obj(),
			wantResult: reconcile.Result{},
		},
		"record firstEvictedAt on the first PodsReadyTimeout eviction when backoffLimitTimeout is set": {
			reconcilerOpts: []Option{
				WithWaitForPodsReady(&waitForPodsReadyConfig{
					timeout:                      3 * time.Second,
					requeuingBackoffLimitTimeout: new(time.Hour),
					requeuingBackoffBaseSeconds:  10,
					requeuingBackoffJitter:       0,
					requeuingBackoffMaxDuration:  time.Duration(3600) * time.Second,
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
				Generation(1).
				Obj(),
			wantWorkload: utiltestingapi.MakeWorkload("wl", "ns").
				ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").Obj(), now).
				AdmittedAt(true, now).
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
				RequeueState(new(int32(1)), new(metav1.NewTime(now.Add(10*time.Second).Truncate(time.Second)))).
				RequeueStateFirstEvictedAt(new(metav1.NewTime(now))).
				SchedulingStatsEviction(
					kueue.WorkloadSchedulingStatsEviction{
						Reason:          kueue.WorkloadEvictedByPodsReadyTimeout,
						UnderlyingCause: kueue.WorkloadWaitForStart,
						Count:           1,
					},
				).
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
		"keep firstEvictedAt on a PodsReadyTimeout eviction within backoffLimitTimeout": {
			reconcilerOpts: []Option{
				WithWaitForPodsReady(&waitForPodsReadyConfig{
					timeout:                      3 * time.Second,
					requeuingBackoffLimitCount:   new(int32(100)),
					requeuingBackoffLimitTimeout: new(time.Hour),
					requeuingBackoffBaseSeconds:  10,
					requeuingBackoffJitter:       0,
					requeuingBackoffMaxDuration:  time.Duration(3600) * time.Second,
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
				RequeueState(new(int32(3)), nil).
				RequeueStateFirstEvictedAt(new(metav1.NewTime(now.Add(-30 * time.Minute)))).
				Generation(1).
				Obj(),
			wantWorkload: utiltestingapi.MakeWorkload("wl", "ns").
				ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").Obj(), now).
				AdmittedAt(true, now).
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
				RequeueState(new(int32(4)), new(metav1.NewTime(now.Add(80*time.Second).Truncate(time.Second)))).
				RequeueStateFirstEvictedAt(new(metav1.NewTime(now.Add(-30 * time.Minute)))).
				SchedulingStatsEviction(
					kueue.WorkloadSchedulingStatsEviction{
						Reason:          kueue.WorkloadEvictedByPodsReadyTimeout,
						UnderlyingCause: kueue.WorkloadWaitForStart,
						Count:           1,
					},
				).
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
		"trigger deactivation of workload when exceeding backoffLimitTimeout before backoffLimitCount": {
			reconcilerOpts: []Option{
				WithWaitForPodsReady(&waitForPodsReadyConfig{
					timeout:                      3 * time.Second,
					requeuingBackoffLimitCount:   new(int32(100)),
					requeuingBackoffLimitTimeout: new(time.Hour),
					requeuingBackoffBaseSeconds:  10,
					requeuingBackoffJitter:       0,
					requeuingBackoffMaxDuration:  time.Duration(3600) * time.Second,
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
				RequeueState(new(int32(3)), nil).
				RequeueStateFirstEvictedAt(new(metav1.NewTime(now.Add(-time.Hour)))).
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
					Message: "exceeding the maximum time 1h0m0s for re-queuing retries",
				}).
				RequeueState(new(int32(3)), nil).
				RequeueStateFirstEvictedAt(new(metav1.NewTime(now.Add(-time.Hour)))).
				Obj(),
		},
		"clear firstEvictedAt when the workload reaches PodsReady=True": {
			reconcilerOpts: []Option{
				WithWaitForPodsReady(&waitForPodsReadyConfig{
					timeout:                      3 * time.Second,
					requeuingBackoffLimitTimeout: new(time.Hour),
					requeuingBackoffBaseSeconds:  10,
					requeuingBackoffJitter:       0,
					requeuingBackoffMaxDuration:  time.Duration(3600) * time.Second,
				}),
			},
			workload: utiltestingapi.MakeWorkload("wl", "ns").
				ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").Obj(), now).
				AdmissionCheck(kueue.AdmissionCheckState{
					Name:  "check",
					State: kueue.CheckStateReady,
				}).
				AdmittedAt(true, now).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadPodsReady,
					Status:  metav1.ConditionTrue,
					Reason:  kueue.WorkloadStarted,
					Message: "All pods reached readiness and the workload is running",
				}).
				RequeueState(new(int32(3)), nil).
				RequeueStateFirstEvictedAt(new(metav1.NewTime(now.Add(-30 * time.Minute)))).
				Obj(),
			// The fake client treats SSA as a strategic merge patch, which cannot clear the field.
			wantWorkload: utiltestingapi.MakeWorkload("wl", "ns").
				ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").Obj(), now).
				AdmittedAt(true, now).
				AdmissionCheck(kueue.AdmissionCheckState{
					Name:  "check",
					State: kueue.CheckStateReady,
				}).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadPodsReady,
					Status:  metav1.ConditionTrue,
					Reason:  kueue.WorkloadStarted,
					Message: "All pods reached readiness and the workload is running",
				}).
				RequeueState(new(int32(3)), nil).
				RequeueStateFirstEvictedAt(new(metav1.NewTime(now.Add(-30 * time.Minute)))).
				Obj(),
			wantWorkloadUseMergePatch: utiltestingapi.MakeWorkload("wl", "ns").
				ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").Obj(), now).
				AdmittedAt(true, now).
				AdmissionCheck(kueue.AdmissionCheckState{
					Name:  "check",
					State: kueue.CheckStateReady,
				}).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadPodsReady,
					Status:  metav1.ConditionTrue,
					Reason:  kueue.WorkloadStarted,
					Message: "All pods reached readiness and the workload is running",
				}).
				RequeueState(new(int32(3)), nil).
				Obj(),
		},
	}
	runReconcileTestCases(t, cases, fakeClock)
}
