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

package unscheduledpods

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/component-base/featuregate"
	testingclock "k8s.io/utils/clock/testing"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/constants"
	controllerconstants "sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/controller/core/indexer"
	"sigs.k8s.io/kueue/pkg/features"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingpod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/pkg/workloadslicing"
)

func TestReconcile(t *testing.T) {
	const (
		testNamespace   = "ns"
		testWorkload    = "wl"
		testWorkloadUID = types.UID("wl-uid")
		testPodSetName  = kueue.PodSetReference("main")
	)

	errListPods := errors.New("list pods failed")
	errConflict := apierrors.NewConflict(kueue.Resource("workloads"), testWorkload, errors.New("object was modified"))
	fakeClock := testingclock.NewFakeClock(time.Now().Truncate(time.Second))
	earlier := fakeClock.Now().Add(-time.Minute)
	muchEarlier := fakeClock.Now().Add(-time.Hour)
	quotaReservedCondition := metav1.Condition{
		Type:               kueue.WorkloadQuotaReserved,
		Status:             metav1.ConditionTrue,
		Reason:             "AdmittedByTest",
		Message:            "Admitted by ClusterQueue cq",
		LastTransitionTime: metav1.NewTime(earlier),
	}
	admittedCondition := metav1.Condition{
		Type:               kueue.WorkloadAdmitted,
		Status:             metav1.ConditionTrue,
		Reason:             "ByTest",
		Message:            "Admitted by ClusterQueue cq",
		LastTransitionTime: metav1.NewTime(earlier),
	}
	nonAdmittedWorkload := utiltestingapi.MakeWorkload(testWorkload, testNamespace).
		Generation(2).
		Condition(metav1.Condition{
			Type:               kueue.WorkloadAdmitted,
			Status:             metav1.ConditionFalse,
			Reason:             kueue.WorkloadAdmittedReasonNoReservation,
			LastTransitionTime: metav1.NewTime(earlier),
		}).
		Condition(metav1.Condition{
			Type:               kueue.WorkloadPodsReady,
			Status:             metav1.ConditionTrue,
			Reason:             kueue.WorkloadStarted,
			LastTransitionTime: metav1.NewTime(earlier),
		})
	scheduledHistory := metav1.Condition{
		Type:               kueue.WorkloadPodsScheduled,
		Status:             metav1.ConditionTrue,
		Reason:             kueue.WorkloadAllRequiredPodsScheduled,
		ObservedGeneration: 1,
		LastTransitionTime: metav1.NewTime(earlier),
	}
	resetCondition := metav1.Condition{
		Type:               kueue.WorkloadPodsScheduled,
		Status:             metav1.ConditionFalse,
		Reason:             kueue.WorkloadWaitForStart,
		Message:            workload.PodsNotReadyMessage,
		ObservedGeneration: 2,
		LastTransitionTime: metav1.NewTime(fakeClock.Now()),
	}

	testCases := map[string]struct {
		features             map[featuregate.Feature]bool
		configuration        *configapi.Configuration // nil uses a positive unscheduled timeout.
		request              *types.NamespacedName
		workloads            []*kueue.Workload
		pods                 []*corev1.Pod
		wantResult           reconcile.Result
		wantErr              error
		wantWorkloadStatuses map[string]kueue.WorkloadStatus
		reconcileTwice       bool
	}{
		"non-admitted scheduled workload is reset without pods": {
			features: map[featuregate.Feature]bool{
				features.WaitForPodsReadyUnscheduledTimeout: true,
			},
			workloads: []*kueue.Workload{
				nonAdmittedWorkload.Clone().
					Condition(scheduledHistory).
					Obj(),
			},
			wantWorkloadStatuses: map[string]kueue.WorkloadStatus{
				testWorkload: {
					Conditions: []metav1.Condition{
						{
							Type:               kueue.WorkloadAdmitted,
							Status:             metav1.ConditionFalse,
							Reason:             kueue.WorkloadAdmittedReasonNoReservation,
							LastTransitionTime: metav1.NewTime(earlier),
						},
						{
							Type:               kueue.WorkloadPodsReady,
							Status:             metav1.ConditionTrue,
							Reason:             kueue.WorkloadStarted,
							LastTransitionTime: metav1.NewTime(earlier),
						},
						resetCondition,
					},
				},
			},
			reconcileTwice: true,
		},
		"absent scheduling history stays absent": {
			features: map[featuregate.Feature]bool{
				features.WaitForPodsReadyUnscheduledTimeout: true,
			},
			workloads: []*kueue.Workload{
				nonAdmittedWorkload.Clone().
					Obj(),
			},
			wantWorkloadStatuses: map[string]kueue.WorkloadStatus{
				testWorkload: {
					Conditions: []metav1.Condition{
						{
							Type:               kueue.WorkloadAdmitted,
							Status:             metav1.ConditionFalse,
							Reason:             kueue.WorkloadAdmittedReasonNoReservation,
							LastTransitionTime: metav1.NewTime(earlier),
						},
						{
							Type:               kueue.WorkloadPodsReady,
							Status:             metav1.ConditionTrue,
							Reason:             kueue.WorkloadStarted,
							LastTransitionTime: metav1.NewTime(earlier),
						},
					},
				},
			},
			reconcileTwice: true,
		},
		"unscheduled history is retained": {
			features: map[featuregate.Feature]bool{
				features.WaitForPodsReadyUnscheduledTimeout: true,
			},
			workloads: []*kueue.Workload{
				nonAdmittedWorkload.Clone().
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPodsScheduled,
						Status:             metav1.ConditionFalse,
						Reason:             kueue.WorkloadWaitForScheduling,
						LastTransitionTime: metav1.NewTime(earlier),
					}).
					Obj(),
			},
			wantWorkloadStatuses: map[string]kueue.WorkloadStatus{
				testWorkload: {
					Conditions: []metav1.Condition{
						{
							Type:               kueue.WorkloadAdmitted,
							Status:             metav1.ConditionFalse,
							Reason:             kueue.WorkloadAdmittedReasonNoReservation,
							LastTransitionTime: metav1.NewTime(earlier),
						},
						{
							Type:               kueue.WorkloadPodsReady,
							Status:             metav1.ConditionTrue,
							Reason:             kueue.WorkloadStarted,
							LastTransitionTime: metav1.NewTime(earlier),
						},
						{
							Type:               kueue.WorkloadPodsScheduled,
							Status:             metav1.ConditionFalse,
							Reason:             kueue.WorkloadWaitForScheduling,
							LastTransitionTime: metav1.NewTime(earlier),
						},
					},
				},
			},
			reconcileTwice: true,
		},
		"unknown scheduling status is retained": {
			features: map[featuregate.Feature]bool{
				features.WaitForPodsReadyUnscheduledTimeout: true,
			},
			workloads: []*kueue.Workload{
				nonAdmittedWorkload.Clone().
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPodsScheduled,
						Status:             metav1.ConditionUnknown,
						Reason:             "Unknown",
						LastTransitionTime: metav1.NewTime(earlier),
					}).
					Obj(),
			},
			wantWorkloadStatuses: map[string]kueue.WorkloadStatus{
				testWorkload: {
					Conditions: []metav1.Condition{
						{
							Type:               kueue.WorkloadAdmitted,
							Status:             metav1.ConditionFalse,
							Reason:             kueue.WorkloadAdmittedReasonNoReservation,
							LastTransitionTime: metav1.NewTime(earlier),
						},
						{
							Type:               kueue.WorkloadPodsReady,
							Status:             metav1.ConditionTrue,
							Reason:             kueue.WorkloadStarted,
							LastTransitionTime: metav1.NewTime(earlier),
						},
						{
							Type:               kueue.WorkloadPodsScheduled,
							Status:             metav1.ConditionUnknown,
							Reason:             "Unknown",
							LastTransitionTime: metav1.NewTime(earlier),
						},
					},
				},
			},
			reconcileTwice: true,
		},
		"gate disabled": {
			features: map[featuregate.Feature]bool{
				features.WaitForPodsReadyUnscheduledTimeout: false,
			},
			workloads: []*kueue.Workload{
				nonAdmittedWorkload.Clone().
					Condition(scheduledHistory).
					Obj(),
			},
			wantWorkloadStatuses: map[string]kueue.WorkloadStatus{
				testWorkload: {
					Conditions: []metav1.Condition{
						{
							Type:               kueue.WorkloadAdmitted,
							Status:             metav1.ConditionFalse,
							Reason:             kueue.WorkloadAdmittedReasonNoReservation,
							LastTransitionTime: metav1.NewTime(earlier),
						},
						{
							Type:               kueue.WorkloadPodsReady,
							Status:             metav1.ConditionTrue,
							Reason:             kueue.WorkloadStarted,
							LastTransitionTime: metav1.NewTime(earlier),
						},
						scheduledHistory,
					},
				},
			},
			reconcileTwice: true,
		},
		"readiness configuration absent": {
			features: map[featuregate.Feature]bool{
				features.WaitForPodsReadyUnscheduledTimeout: true,
			},
			configuration: &configapi.Configuration{},
			workloads: []*kueue.Workload{
				nonAdmittedWorkload.Clone().
					Condition(scheduledHistory).
					Obj(),
			},
			wantWorkloadStatuses: map[string]kueue.WorkloadStatus{
				testWorkload: {
					Conditions: []metav1.Condition{
						{
							Type:               kueue.WorkloadAdmitted,
							Status:             metav1.ConditionFalse,
							Reason:             kueue.WorkloadAdmittedReasonNoReservation,
							LastTransitionTime: metav1.NewTime(earlier),
						},
						{
							Type:               kueue.WorkloadPodsReady,
							Status:             metav1.ConditionTrue,
							Reason:             kueue.WorkloadStarted,
							LastTransitionTime: metav1.NewTime(earlier),
						},
						scheduledHistory,
					},
				},
			},
			reconcileTwice: true,
		},
		"timeout omitted": {
			features: map[featuregate.Feature]bool{
				features.WaitForPodsReadyUnscheduledTimeout: true,
			},
			configuration: &configapi.Configuration{
				WaitForPodsReady: &configapi.WaitForPodsReady{},
			},
			workloads: []*kueue.Workload{
				nonAdmittedWorkload.Clone().
					Condition(scheduledHistory).
					Obj(),
			},
			wantWorkloadStatuses: map[string]kueue.WorkloadStatus{
				testWorkload: {
					Conditions: []metav1.Condition{
						{
							Type:               kueue.WorkloadAdmitted,
							Status:             metav1.ConditionFalse,
							Reason:             kueue.WorkloadAdmittedReasonNoReservation,
							LastTransitionTime: metav1.NewTime(earlier),
						},
						{
							Type:               kueue.WorkloadPodsReady,
							Status:             metav1.ConditionTrue,
							Reason:             kueue.WorkloadStarted,
							LastTransitionTime: metav1.NewTime(earlier),
						},
						scheduledHistory,
					},
				},
			},
			reconcileTwice: true,
		},
		"timeout zero": {
			features: map[featuregate.Feature]bool{
				features.WaitForPodsReadyUnscheduledTimeout: true,
			},
			configuration: &configapi.Configuration{
				WaitForPodsReady: &configapi.WaitForPodsReady{
					UnscheduledTimeout: &metav1.Duration{},
				},
			},
			workloads: []*kueue.Workload{
				nonAdmittedWorkload.Clone().
					Condition(scheduledHistory).
					Obj(),
			},
			wantWorkloadStatuses: map[string]kueue.WorkloadStatus{
				testWorkload: {
					Conditions: []metav1.Condition{
						{
							Type:               kueue.WorkloadAdmitted,
							Status:             metav1.ConditionFalse,
							Reason:             kueue.WorkloadAdmittedReasonNoReservation,
							LastTransitionTime: metav1.NewTime(earlier),
						},
						{
							Type:               kueue.WorkloadPodsReady,
							Status:             metav1.ConditionTrue,
							Reason:             kueue.WorkloadStarted,
							LastTransitionTime: metav1.NewTime(earlier),
						},
						scheduledHistory,
					},
				},
			},
			reconcileTwice: true,
		},
		"finished workload keeps its history": {
			features: map[featuregate.Feature]bool{
				features.WaitForPodsReadyUnscheduledTimeout: true,
			},
			workloads: []*kueue.Workload{
				nonAdmittedWorkload.Clone().
					Condition(scheduledHistory).
					FinishedAt(fakeClock.Now()).
					Obj(),
			},
			wantWorkloadStatuses: map[string]kueue.WorkloadStatus{
				testWorkload: {
					Conditions: []metav1.Condition{
						{
							Type:               kueue.WorkloadAdmitted,
							Status:             metav1.ConditionFalse,
							Reason:             kueue.WorkloadAdmittedReasonNoReservation,
							LastTransitionTime: metav1.NewTime(earlier),
						},
						{
							Type:               kueue.WorkloadPodsReady,
							Status:             metav1.ConditionTrue,
							Reason:             kueue.WorkloadStarted,
							LastTransitionTime: metav1.NewTime(earlier),
						},
						{
							Type:               kueue.WorkloadFinished,
							Status:             metav1.ConditionTrue,
							Reason:             "ByTest",
							Message:            "Finished by test",
							LastTransitionTime: metav1.NewTime(fakeClock.Now()),
						},
						scheduledHistory,
					},
				},
			},
			reconcileTwice: true,
		},
		"non-admitted variant is not reset": {
			features: map[featuregate.Feature]bool{
				features.WaitForPodsReadyUnscheduledTimeout: true,
				features.ConcurrentAdmission:                true,
			},
			workloads: []*kueue.Workload{
				nonAdmittedWorkload.Clone().
					Condition(scheduledHistory).
					ControllerReference(kueue.SchemeGroupVersion.WithKind("Workload"), "parent", "parent-uid").
					Obj(),
			},
			wantWorkloadStatuses: map[string]kueue.WorkloadStatus{
				testWorkload: {
					Conditions: []metav1.Condition{
						{
							Type:               kueue.WorkloadAdmitted,
							Status:             metav1.ConditionFalse,
							Reason:             kueue.WorkloadAdmittedReasonNoReservation,
							LastTransitionTime: metav1.NewTime(earlier),
						},
						{
							Type:               kueue.WorkloadPodsReady,
							Status:             metav1.ConditionTrue,
							Reason:             kueue.WorkloadStarted,
							LastTransitionTime: metav1.NewTime(earlier),
						},
						scheduledHistory,
					},
				},
			},
			reconcileTwice: true,
		},
		"gate off does not observe Pods": {
			features: map[featuregate.Feature]bool{
				features.WaitForPodsReadyUnscheduledTimeout: false,
			},
			workloads: []*kueue.Workload{
				utiltestingapi.MakeWorkload(testWorkload, testNamespace).
					UID(testWorkloadUID).
					PodSets(*utiltestingapi.MakePodSet(testPodSetName, 1).
						Obj()).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").
						PodSets(utiltestingapi.MakePodSetAssignment(testPodSetName).
							Count(1).
							Obj()).
						Obj(), earlier).
					AdmittedAt(true, earlier).
					Obj(),
			},
			pods: []*corev1.Pod{
				testingpod.MakePod("p1", testNamespace).
					Annotation(kueue.WorkloadAnnotation, testWorkload).
					Label(constants.PodSetLabel, string(testPodSetName)).
					Obj(),
			},
			wantWorkloadStatuses: map[string]kueue.WorkloadStatus{
				testWorkload: {
					Admission: &kueue.Admission{
						ClusterQueue: "cq",
						PodSetAssignments: []kueue.PodSetAssignment{
							{
								Name:  testPodSetName,
								Count: new(int32(1)),
							},
						},
					},
					Conditions: []metav1.Condition{
						quotaReservedCondition,
						admittedCondition,
					},
				},
			},
		},
		"gate off does not reset a released workload": {
			features: map[featuregate.Feature]bool{
				features.WaitForPodsReadyUnscheduledTimeout: false,
			},
			workloads: []*kueue.Workload{
				utiltestingapi.MakeWorkload(testWorkload, testNamespace).
					UID(testWorkloadUID).
					PodSets(*utiltestingapi.MakePodSet(testPodSetName, 1).
						Obj()).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadQuotaReserved,
						Status:             metav1.ConditionFalse,
						Reason:             "Pending",
						LastTransitionTime: metav1.NewTime(fakeClock.Now()),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPodsScheduled,
						Status:             metav1.ConditionFalse,
						Reason:             kueue.WorkloadWaitForScheduling,
						LastTransitionTime: metav1.NewTime(earlier),
					}).
					Obj(),
			},
			pods: []*corev1.Pod{
				testingpod.MakePod("p1", testNamespace).
					Annotation(kueue.WorkloadAnnotation, testWorkload).
					Label(constants.PodSetLabel, string(testPodSetName)).
					Obj(),
			},
			wantWorkloadStatuses: map[string]kueue.WorkloadStatus{
				testWorkload: {
					Conditions: []metav1.Condition{
						{
							Type:               kueue.WorkloadQuotaReserved,
							Status:             metav1.ConditionFalse,
							Reason:             "Pending",
							LastTransitionTime: metav1.NewTime(fakeClock.Now()),
						},
						{
							Type:               kueue.WorkloadPodsScheduled,
							Status:             metav1.ConditionFalse,
							Reason:             kueue.WorkloadWaitForScheduling,
							LastTransitionTime: metav1.NewTime(earlier),
						},
					},
				},
			},
		},
		"legacy readiness disabled does not track scheduling": {
			features: map[featuregate.Feature]bool{
				features.WaitForPodsReadyUnscheduledTimeout: false,
				features.DisableWaitForPodsReady:            true,
			},
			workloads: []*kueue.Workload{
				utiltestingapi.MakeWorkload(testWorkload, testNamespace).
					UID(testWorkloadUID).
					PodSets(*utiltestingapi.MakePodSet(testPodSetName, 1).
						Obj()).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").
						PodSets(utiltestingapi.MakePodSetAssignment(testPodSetName).
							Count(1).
							Obj()).
						Obj(), earlier).
					AdmittedAt(true, earlier).
					Obj(),
			},
			pods: []*corev1.Pod{
				testingpod.MakePod("p1", testNamespace).
					Annotation(kueue.WorkloadAnnotation, testWorkload).
					Label(constants.PodSetLabel, string(testPodSetName)).
					Obj(),
			},
			wantWorkloadStatuses: map[string]kueue.WorkloadStatus{
				testWorkload: {
					Admission: &kueue.Admission{
						ClusterQueue: "cq",
						PodSetAssignments: []kueue.PodSetAssignment{
							{
								Name:  testPodSetName,
								Count: new(int32(1)),
							},
						},
					},
					Conditions: []metav1.Condition{
						quotaReservedCondition,
						admittedCondition,
					},
				},
			},
		},
		"evicted workload is not observed or reset": {
			features: map[featuregate.Feature]bool{
				features.WaitForPodsReadyUnscheduledTimeout: true,
			},
			workloads: []*kueue.Workload{
				utiltestingapi.MakeWorkload(testWorkload, testNamespace).
					UID(testWorkloadUID).
					PodSets(*utiltestingapi.MakePodSet(testPodSetName, 1).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").
						PodSets(utiltestingapi.MakePodSetAssignment(testPodSetName).
							Count(1).
							Obj()).
						Obj(), earlier).
					AdmittedAt(true, earlier).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						Reason:             kueue.WorkloadEvictedByPodsReadyTimeout,
						Message:            "Exceeded the PodsReady timeout",
						LastTransitionTime: metav1.NewTime(fakeClock.Now()),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPodsScheduled,
						Status:             metav1.ConditionTrue,
						Reason:             kueue.WorkloadAllRequiredPodsScheduled,
						Message:            allPodsScheduledMessage,
						LastTransitionTime: metav1.NewTime(fakeClock.Now().Add(-30 * time.Second)),
					}).
					Obj(),
			},
			pods: []*corev1.Pod{
				testingpod.MakePod("p1", testNamespace).
					Annotation(kueue.WorkloadAnnotation, testWorkload).
					Label(constants.PodSetLabel, string(testPodSetName)).
					NodeName("node-a").
					StatusConditions(corev1.PodCondition{
						Type:   corev1.PodScheduled,
						Status: corev1.ConditionTrue,
					}).
					Obj(),
			},
			wantWorkloadStatuses: map[string]kueue.WorkloadStatus{
				testWorkload: {
					Admission: &kueue.Admission{
						ClusterQueue: "cq",
						PodSetAssignments: []kueue.PodSetAssignment{
							{
								Name:  testPodSetName,
								Count: new(int32(1)),
							},
						},
					},
					Conditions: []metav1.Condition{
						quotaReservedCondition,
						admittedCondition,
						{
							Type:               kueue.WorkloadEvicted,
							Status:             metav1.ConditionTrue,
							Reason:             kueue.WorkloadEvictedByPodsReadyTimeout,
							Message:            "Exceeded the PodsReady timeout",
							LastTransitionTime: metav1.NewTime(fakeClock.Now()),
						},
						{
							Type:               kueue.WorkloadPodsScheduled,
							Status:             metav1.ConditionTrue,
							Reason:             kueue.WorkloadAllRequiredPodsScheduled,
							Message:            allPodsScheduledMessage,
							LastTransitionTime: metav1.NewTime(fakeClock.Now().Add(-30 * time.Second)),
						},
					},
				},
			},
		},
		"evicted finished workload keeps its condition": {
			features: map[featuregate.Feature]bool{
				features.WaitForPodsReadyUnscheduledTimeout: true,
			},
			workloads: []*kueue.Workload{
				utiltestingapi.MakeWorkload(testWorkload, testNamespace).
					UID(testWorkloadUID).
					PodSets(*utiltestingapi.MakePodSet(testPodSetName, 1).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").
						PodSets(utiltestingapi.MakePodSetAssignment(testPodSetName).
							Count(1).
							Obj()).
						Obj(), earlier).
					AdmittedAt(true, earlier).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						Reason:             kueue.WorkloadEvictedByPodsReadyTimeout,
						Message:            "Exceeded the PodsReady timeout",
						LastTransitionTime: metav1.NewTime(fakeClock.Now()),
					}).
					FinishedAt(fakeClock.Now()).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPodsScheduled,
						Status:             metav1.ConditionTrue,
						Reason:             kueue.WorkloadAllRequiredPodsScheduled,
						Message:            allPodsScheduledMessage,
						LastTransitionTime: metav1.NewTime(fakeClock.Now().Add(-30 * time.Second)),
					}).
					Obj(),
			},
			wantWorkloadStatuses: map[string]kueue.WorkloadStatus{
				testWorkload: {
					Admission: &kueue.Admission{
						ClusterQueue: "cq",
						PodSetAssignments: []kueue.PodSetAssignment{
							{
								Name:  testPodSetName,
								Count: new(int32(1)),
							},
						},
					},
					Conditions: []metav1.Condition{
						quotaReservedCondition,
						admittedCondition,
						{
							Type:               kueue.WorkloadEvicted,
							Status:             metav1.ConditionTrue,
							Reason:             kueue.WorkloadEvictedByPodsReadyTimeout,
							Message:            "Exceeded the PodsReady timeout",
							LastTransitionTime: metav1.NewTime(fakeClock.Now()),
						},
						{
							Type:               kueue.WorkloadFinished,
							Status:             metav1.ConditionTrue,
							Reason:             "ByTest",
							Message:            "Finished by test",
							LastTransitionTime: metav1.NewTime(fakeClock.Now()),
						},
						{
							Type:               kueue.WorkloadPodsScheduled,
							Status:             metav1.ConditionTrue,
							Reason:             kueue.WorkloadAllRequiredPodsScheduled,
							Message:            allPodsScheduledMessage,
							LastTransitionTime: metav1.NewTime(fakeClock.Now().Add(-30 * time.Second)),
						},
					},
				},
			},
		},
		"evicted workload without the condition is left untouched": {
			features: map[featuregate.Feature]bool{
				features.WaitForPodsReadyUnscheduledTimeout: true,
			},
			workloads: []*kueue.Workload{
				utiltestingapi.MakeWorkload(testWorkload, testNamespace).
					UID(testWorkloadUID).
					PodSets(*utiltestingapi.MakePodSet(testPodSetName, 1).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").
						PodSets(utiltestingapi.MakePodSetAssignment(testPodSetName).
							Count(1).
							Obj()).
						Obj(), earlier).
					AdmittedAt(true, earlier).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						Reason:             kueue.WorkloadEvictedByPodsReadyTimeout,
						Message:            "Exceeded the PodsReady timeout",
						LastTransitionTime: metav1.NewTime(fakeClock.Now()),
					}).
					Obj(),
			},
			pods: []*corev1.Pod{
				testingpod.MakePod("p1", testNamespace).
					Annotation(kueue.WorkloadAnnotation, testWorkload).
					Label(constants.PodSetLabel, string(testPodSetName)).
					NodeName("node-a").
					StatusConditions(corev1.PodCondition{
						Type:   corev1.PodScheduled,
						Status: corev1.ConditionTrue,
					}).
					Obj(),
			},
			wantWorkloadStatuses: map[string]kueue.WorkloadStatus{
				testWorkload: {
					Admission: &kueue.Admission{
						ClusterQueue: "cq",
						PodSetAssignments: []kueue.PodSetAssignment{
							{
								Name:  testPodSetName,
								Count: new(int32(1)),
							},
						},
					},
					Conditions: []metav1.Condition{
						quotaReservedCondition,
						admittedCondition,
						{
							Type:               kueue.WorkloadEvicted,
							Status:             metav1.ConditionTrue,
							Reason:             kueue.WorkloadEvictedByPodsReadyTimeout,
							Message:            "Exceeded the PodsReady timeout",
							LastTransitionTime: metav1.NewTime(fakeClock.Now()),
						},
					},
				},
			},
		},
		"concurrent admission: an evicted variant workload keeps its condition": {
			features: map[featuregate.Feature]bool{
				features.WaitForPodsReadyUnscheduledTimeout: true,
				features.ConcurrentAdmission:                true,
			},
			workloads: []*kueue.Workload{
				utiltestingapi.MakeWorkload(testWorkload, testNamespace).
					UID(testWorkloadUID).
					PodSets(*utiltestingapi.MakePodSet(testPodSetName, 1).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").
						PodSets(utiltestingapi.MakePodSetAssignment(testPodSetName).
							Count(1).
							Obj()).
						Obj(), earlier).
					AdmittedAt(true, earlier).
					OwnerReference(kueue.SchemeGroupVersion.WithKind("Workload"), "parent", "parent-uid").
					Condition(metav1.Condition{
						Type:               kueue.WorkloadEvicted,
						Status:             metav1.ConditionTrue,
						Reason:             kueue.WorkloadEvictedByPodsReadyTimeout,
						Message:            "Exceeded the PodsReady timeout",
						LastTransitionTime: metav1.NewTime(fakeClock.Now()),
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPodsScheduled,
						Status:             metav1.ConditionTrue,
						Reason:             kueue.WorkloadAllRequiredPodsScheduled,
						Message:            allPodsScheduledMessage,
						LastTransitionTime: metav1.NewTime(fakeClock.Now().Add(-30 * time.Second)),
					}).
					Obj(),
			},
			wantWorkloadStatuses: map[string]kueue.WorkloadStatus{
				testWorkload: {
					Admission: &kueue.Admission{
						ClusterQueue: "cq",
						PodSetAssignments: []kueue.PodSetAssignment{
							{
								Name:  testPodSetName,
								Count: new(int32(1)),
							},
						},
					},
					Conditions: []metav1.Condition{
						quotaReservedCondition,
						admittedCondition,
						{
							Type:               kueue.WorkloadEvicted,
							Status:             metav1.ConditionTrue,
							Reason:             kueue.WorkloadEvictedByPodsReadyTimeout,
							Message:            "Exceeded the PodsReady timeout",
							LastTransitionTime: metav1.NewTime(fakeClock.Now()),
						},
						{
							Type:               kueue.WorkloadPodsScheduled,
							Status:             metav1.ConditionTrue,
							Reason:             kueue.WorkloadAllRequiredPodsScheduled,
							Message:            allPodsScheduledMessage,
							LastTransitionTime: metav1.NewTime(fakeClock.Now().Add(-30 * time.Second)),
						},
					},
				},
			},
		},
		"re-admitted workload with an observation of the previous admission and no pods is left untouched": {
			features: map[featuregate.Feature]bool{
				features.WaitForPodsReadyUnscheduledTimeout: true,
			},
			workloads: []*kueue.Workload{
				utiltestingapi.MakeWorkload(testWorkload, testNamespace).
					UID(testWorkloadUID).
					PodSets(*utiltestingapi.MakePodSet(testPodSetName, 2).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").
						PodSets(utiltestingapi.MakePodSetAssignment(testPodSetName).
							Count(2).
							Obj()).
						Obj(), earlier).
					AdmittedAt(true, earlier).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPodsScheduled,
						Status:             metav1.ConditionTrue,
						Reason:             kueue.WorkloadAllRequiredPodsScheduled,
						Message:            allPodsScheduledMessage,
						LastTransitionTime: metav1.NewTime(muchEarlier),
					}).
					Obj(),
			},
			wantWorkloadStatuses: map[string]kueue.WorkloadStatus{
				testWorkload: {
					Admission: &kueue.Admission{
						ClusterQueue: "cq",
						PodSetAssignments: []kueue.PodSetAssignment{
							{
								Name:  testPodSetName,
								Count: new(int32(2)),
							},
						},
					},
					Conditions: []metav1.Condition{
						quotaReservedCondition,
						admittedCondition,
						{
							Type:               kueue.WorkloadPodsScheduled,
							Status:             metav1.ConditionTrue,
							Reason:             kueue.WorkloadAllRequiredPodsScheduled,
							Message:            allPodsScheduledMessage,
							LastTransitionTime: metav1.NewTime(muchEarlier),
						},
					},
				},
			},
		},
		"re-admitted workload with an observation of the previous admission restamps it from the pods": {
			features: map[featuregate.Feature]bool{
				features.WaitForPodsReadyUnscheduledTimeout: true,
			},
			workloads: []*kueue.Workload{
				utiltestingapi.MakeWorkload(testWorkload, testNamespace).
					UID(testWorkloadUID).
					PodSets(*utiltestingapi.MakePodSet(testPodSetName, 2).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").
						PodSets(utiltestingapi.MakePodSetAssignment(testPodSetName).
							Count(2).
							Obj()).
						Obj(), earlier).
					AdmittedAt(true, earlier).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPodsScheduled,
						Status:             metav1.ConditionTrue,
						Reason:             kueue.WorkloadAllRequiredPodsScheduled,
						Message:            allPodsScheduledMessage,
						LastTransitionTime: metav1.NewTime(muchEarlier),
					}).
					Obj(),
			},
			pods: []*corev1.Pod{
				testingpod.MakePod("p1", testNamespace).
					Annotation(kueue.WorkloadAnnotation, testWorkload).
					Label(constants.PodSetLabel, string(testPodSetName)).
					NodeName("node-a").
					StatusConditions(corev1.PodCondition{
						Type:   corev1.PodScheduled,
						Status: corev1.ConditionTrue,
					}).
					Obj(),
				testingpod.MakePod("p2", testNamespace).
					Annotation(kueue.WorkloadAnnotation, testWorkload).
					Label(constants.PodSetLabel, string(testPodSetName)).
					NodeName("node-a").
					StatusConditions(corev1.PodCondition{
						Type:   corev1.PodScheduled,
						Status: corev1.ConditionTrue,
					}).
					Obj(),
			},
			wantWorkloadStatuses: map[string]kueue.WorkloadStatus{
				testWorkload: {
					Admission: &kueue.Admission{
						ClusterQueue: "cq",
						PodSetAssignments: []kueue.PodSetAssignment{
							{
								Name:  testPodSetName,
								Count: new(int32(2)),
							},
						},
					},
					Conditions: []metav1.Condition{
						quotaReservedCondition,
						admittedCondition,
						{
							Type:               kueue.WorkloadPodsScheduled,
							Status:             metav1.ConditionTrue,
							Reason:             kueue.WorkloadAllRequiredPodsScheduled,
							Message:            allPodsScheduledMessage,
							LastTransitionTime: metav1.NewTime(fakeClock.Now()),
						},
					},
				},
			},
		},
		"first observation after a tracker reset is stamped now": {
			features: map[featuregate.Feature]bool{
				features.WaitForPodsReadyUnscheduledTimeout: true,
			},
			workloads: []*kueue.Workload{
				utiltestingapi.MakeWorkload(testWorkload, testNamespace).
					UID(testWorkloadUID).
					PodSets(*utiltestingapi.MakePodSet(testPodSetName, 1).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").
						PodSets(utiltestingapi.MakePodSetAssignment(testPodSetName).
							Count(1).
							Obj()).
						Obj(), earlier).
					AdmittedAt(true, earlier).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPodsScheduled,
						Status:             metav1.ConditionFalse,
						Reason:             kueue.WorkloadWaitForStart,
						Message:            workload.PodsNotReadyMessage,
						LastTransitionTime: metav1.NewTime(fakeClock.Now().Add(-30 * time.Second)),
					}).
					Obj(),
			},
			pods: []*corev1.Pod{
				testingpod.MakePod("p1", testNamespace).
					Annotation(kueue.WorkloadAnnotation, testWorkload).
					Label(constants.PodSetLabel, string(testPodSetName)).
					Obj(),
			},
			wantWorkloadStatuses: map[string]kueue.WorkloadStatus{
				testWorkload: {
					Admission: &kueue.Admission{
						ClusterQueue: "cq",
						PodSetAssignments: []kueue.PodSetAssignment{
							{
								Name:  testPodSetName,
								Count: new(int32(1)),
							},
						},
					},
					Conditions: []metav1.Condition{
						quotaReservedCondition,
						admittedCondition,
						{
							Type:               kueue.WorkloadPodsScheduled,
							Status:             metav1.ConditionFalse,
							Reason:             kueue.WorkloadWaitForScheduling,
							Message:            unscheduledPodsMessage,
							LastTransitionTime: metav1.NewTime(fakeClock.Now()),
						},
					},
				},
			},
		},
		"condition stamped in the admission second is restamped with the same status": {
			features: map[featuregate.Feature]bool{
				features.WaitForPodsReadyUnscheduledTimeout: true,
			},
			workloads: []*kueue.Workload{
				utiltestingapi.MakeWorkload(testWorkload, testNamespace).
					UID(testWorkloadUID).
					PodSets(*utiltestingapi.MakePodSet(testPodSetName, 1).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").
						PodSets(utiltestingapi.MakePodSetAssignment(testPodSetName).
							Count(1).
							Obj()).
						Obj(), earlier).
					AdmittedAt(true, earlier).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPodsScheduled,
						Status:             metav1.ConditionFalse,
						Reason:             kueue.WorkloadWaitForScheduling,
						Message:            unscheduledPodsMessage,
						LastTransitionTime: metav1.NewTime(earlier),
					}).
					Obj(),
			},
			pods: []*corev1.Pod{
				testingpod.MakePod("p1", testNamespace).
					Annotation(kueue.WorkloadAnnotation, testWorkload).
					Label(constants.PodSetLabel, string(testPodSetName)).
					Obj(),
			},
			wantWorkloadStatuses: map[string]kueue.WorkloadStatus{
				testWorkload: {
					Admission: &kueue.Admission{
						ClusterQueue: "cq",
						PodSetAssignments: []kueue.PodSetAssignment{
							{
								Name:  testPodSetName,
								Count: new(int32(1)),
							},
						},
					},
					Conditions: []metav1.Condition{
						quotaReservedCondition,
						admittedCondition,
						{
							Type:               kueue.WorkloadPodsScheduled,
							Status:             metav1.ConditionFalse,
							Reason:             kueue.WorkloadWaitForScheduling,
							Message:            unscheduledPodsMessage,
							LastTransitionTime: metav1.NewTime(fakeClock.Now()),
						},
					},
				},
			},
		},
		"condition stamped in the admission second is replaced by the opposite observation (True to False)": {
			features: map[featuregate.Feature]bool{
				features.WaitForPodsReadyUnscheduledTimeout: true,
			},
			workloads: []*kueue.Workload{
				utiltestingapi.MakeWorkload(testWorkload, testNamespace).
					UID(testWorkloadUID).
					PodSets(*utiltestingapi.MakePodSet(testPodSetName, 1).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").
						PodSets(utiltestingapi.MakePodSetAssignment(testPodSetName).
							Count(1).
							Obj()).
						Obj(), earlier).
					AdmittedAt(true, earlier).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPodsScheduled,
						Status:             metav1.ConditionTrue,
						Reason:             kueue.WorkloadAllRequiredPodsScheduled,
						Message:            allPodsScheduledMessage,
						LastTransitionTime: metav1.NewTime(earlier),
					}).
					Obj(),
			},
			pods: []*corev1.Pod{
				testingpod.MakePod("p1", testNamespace).
					Annotation(kueue.WorkloadAnnotation, testWorkload).
					Label(constants.PodSetLabel, string(testPodSetName)).
					Obj(),
			},
			wantWorkloadStatuses: map[string]kueue.WorkloadStatus{
				testWorkload: {
					Admission: &kueue.Admission{
						ClusterQueue: "cq",
						PodSetAssignments: []kueue.PodSetAssignment{
							{
								Name:  testPodSetName,
								Count: new(int32(1)),
							},
						},
					},
					Conditions: []metav1.Condition{
						quotaReservedCondition,
						admittedCondition,
						{
							Type:               kueue.WorkloadPodsScheduled,
							Status:             metav1.ConditionFalse,
							Reason:             kueue.WorkloadWaitForScheduling,
							Message:            unscheduledPodsMessage,
							LastTransitionTime: metav1.NewTime(fakeClock.Now()),
						},
					},
				},
			},
		},
		"condition stamped in the admission second is replaced by the opposite observation (False to True)": {
			features: map[featuregate.Feature]bool{
				features.WaitForPodsReadyUnscheduledTimeout: true,
			},
			workloads: []*kueue.Workload{
				utiltestingapi.MakeWorkload(testWorkload, testNamespace).
					UID(testWorkloadUID).
					PodSets(*utiltestingapi.MakePodSet(testPodSetName, 1).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").
						PodSets(utiltestingapi.MakePodSetAssignment(testPodSetName).
							Count(1).
							Obj()).
						Obj(), earlier).
					AdmittedAt(true, earlier).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPodsScheduled,
						Status:             metav1.ConditionFalse,
						Reason:             kueue.WorkloadWaitForScheduling,
						Message:            unscheduledPodsMessage,
						LastTransitionTime: metav1.NewTime(earlier),
					}).
					Obj(),
			},
			pods: []*corev1.Pod{
				testingpod.MakePod("p1", testNamespace).
					Annotation(kueue.WorkloadAnnotation, testWorkload).
					Label(constants.PodSetLabel, string(testPodSetName)).
					NodeName("node-a").
					StatusConditions(corev1.PodCondition{
						Type:   corev1.PodScheduled,
						Status: corev1.ConditionTrue,
					}).
					Obj(),
			},
			wantWorkloadStatuses: map[string]kueue.WorkloadStatus{
				testWorkload: {
					Admission: &kueue.Admission{
						ClusterQueue: "cq",
						PodSetAssignments: []kueue.PodSetAssignment{
							{
								Name:  testPodSetName,
								Count: new(int32(1)),
							},
						},
					},
					Conditions: []metav1.Condition{
						quotaReservedCondition,
						admittedCondition,
						{
							Type:               kueue.WorkloadPodsScheduled,
							Status:             metav1.ConditionTrue,
							Reason:             kueue.WorkloadAllRequiredPodsScheduled,
							Message:            allPodsScheduledMessage,
							LastTransitionTime: metav1.NewTime(fakeClock.Now()),
						},
					},
				},
			},
		},
		"concurrent admission: a parent workload is observed": {
			features: map[featuregate.Feature]bool{
				features.WaitForPodsReadyUnscheduledTimeout: true,
				features.ConcurrentAdmission:                true,
			},
			workloads: []*kueue.Workload{
				utiltestingapi.MakeWorkload(testWorkload, testNamespace).
					UID(testWorkloadUID).
					PodSets(*utiltestingapi.MakePodSet(testPodSetName, 1).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").
						PodSets(utiltestingapi.MakePodSetAssignment(testPodSetName).
							Count(1).
							Obj()).
						Obj(), earlier).
					AdmittedAt(true, earlier).
					Label(controllerconstants.ConcurrentAdmissionParentLabelKey, "true").
					Obj(),
			},
			pods: []*corev1.Pod{
				testingpod.MakePod("p1", testNamespace).
					Annotation(kueue.WorkloadAnnotation, testWorkload).
					Label(constants.PodSetLabel, string(testPodSetName)).
					NodeName("node-a").
					StatusConditions(corev1.PodCondition{
						Type:   corev1.PodScheduled,
						Status: corev1.ConditionTrue,
					}).
					Obj(),
			},
			wantWorkloadStatuses: map[string]kueue.WorkloadStatus{
				testWorkload: {
					Admission: &kueue.Admission{
						ClusterQueue: "cq",
						PodSetAssignments: []kueue.PodSetAssignment{
							{
								Name:  testPodSetName,
								Count: new(int32(1)),
							},
						},
					},
					Conditions: []metav1.Condition{
						quotaReservedCondition,
						admittedCondition,
						{
							Type:               kueue.WorkloadPodsScheduled,
							Status:             metav1.ConditionTrue,
							Reason:             kueue.WorkloadAllRequiredPodsScheduled,
							Message:            allPodsScheduledMessage,
							LastTransitionTime: metav1.NewTime(fakeClock.Now()),
						},
					},
				},
			},
		},
		"concurrent admission: a variant workload is not observed": {
			features: map[featuregate.Feature]bool{
				features.WaitForPodsReadyUnscheduledTimeout: true,
				features.ConcurrentAdmission:                true,
			},
			workloads: []*kueue.Workload{
				utiltestingapi.MakeWorkload(testWorkload, testNamespace).
					UID(testWorkloadUID).
					PodSets(*utiltestingapi.MakePodSet(testPodSetName, 1).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").
						PodSets(utiltestingapi.MakePodSetAssignment(testPodSetName).
							Count(1).
							Obj()).
						Obj(), earlier).
					AdmittedAt(true, earlier).
					OwnerReference(kueue.SchemeGroupVersion.WithKind("Workload"), "parent", "parent-uid").
					Obj(),
			},
			pods: []*corev1.Pod{
				testingpod.MakePod("p1", testNamespace).
					Annotation(kueue.WorkloadAnnotation, testWorkload).
					Label(constants.PodSetLabel, string(testPodSetName)).
					NodeName("node-a").
					StatusConditions(corev1.PodCondition{
						Type:   corev1.PodScheduled,
						Status: corev1.ConditionTrue,
					}).
					Obj(),
			},
			wantWorkloadStatuses: map[string]kueue.WorkloadStatus{
				testWorkload: {
					Admission: &kueue.Admission{
						ClusterQueue: "cq",
						PodSetAssignments: []kueue.PodSetAssignment{
							{
								Name:  testPodSetName,
								Count: new(int32(1)),
							},
						},
					},
					Conditions: []metav1.Condition{
						quotaReservedCondition,
						admittedCondition,
					},
				},
			},
		},
		"workload without quota reservation is skipped": {
			features: map[featuregate.Feature]bool{
				features.WaitForPodsReadyUnscheduledTimeout: true,
			},
			workloads: []*kueue.Workload{
				utiltestingapi.MakeWorkload(testWorkload, testNamespace).
					PodSets(*utiltestingapi.MakePodSet(testPodSetName, 2).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					Obj(),
			},
			pods: []*corev1.Pod{
				testingpod.MakePod("p1", testNamespace).
					Annotation(kueue.WorkloadAnnotation, testWorkload).
					Label(constants.PodSetLabel, string(testPodSetName)).
					NodeName("node-a").
					StatusConditions(corev1.PodCondition{
						Type:   corev1.PodScheduled,
						Status: corev1.ConditionTrue,
					}).
					Obj(),
			},
			wantWorkloadStatuses: map[string]kueue.WorkloadStatus{
				testWorkload: {},
			},
		},
		"workload with quota reservation but not admitted is skipped": {
			features: map[featuregate.Feature]bool{
				features.WaitForPodsReadyUnscheduledTimeout: true,
			},
			workloads: []*kueue.Workload{
				utiltestingapi.MakeWorkload(testWorkload, testNamespace).
					PodSets(*utiltestingapi.MakePodSet(testPodSetName, 2).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").
						PodSets(utiltestingapi.MakePodSetAssignment(testPodSetName).
							Count(2).
							Obj()).
						Obj(), earlier).
					Obj(),
			},
			pods: []*corev1.Pod{
				testingpod.MakePod("p1", testNamespace).
					Annotation(kueue.WorkloadAnnotation, testWorkload).
					Label(constants.PodSetLabel, string(testPodSetName)).
					NodeName("node-a").
					StatusConditions(corev1.PodCondition{
						Type:   corev1.PodScheduled,
						Status: corev1.ConditionTrue,
					}).
					Obj(),
			},
			wantWorkloadStatuses: map[string]kueue.WorkloadStatus{
				testWorkload: {
					Admission: &kueue.Admission{
						ClusterQueue: "cq",
						PodSetAssignments: []kueue.PodSetAssignment{
							{
								Name:  testPodSetName,
								Count: new(int32(2)),
							},
						},
					},
					Conditions: []metav1.Condition{
						quotaReservedCondition,
					},
				},
			},
		},
		"finished workload keeps its condition": {
			features: map[featuregate.Feature]bool{
				features.WaitForPodsReadyUnscheduledTimeout: true,
			},
			workloads: []*kueue.Workload{
				utiltestingapi.MakeWorkload(testWorkload, testNamespace).
					UID(testWorkloadUID).
					PodSets(*utiltestingapi.MakePodSet(testPodSetName, 2).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").
						PodSets(utiltestingapi.MakePodSetAssignment(testPodSetName).
							Count(2).
							Obj()).
						Obj(), earlier).
					AdmittedAt(true, earlier).
					FinishedAt(fakeClock.Now()).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPodsScheduled,
						Status:             metav1.ConditionFalse,
						Reason:             kueue.WorkloadWaitForScheduling,
						Message:            unscheduledPodsMessage,
						LastTransitionTime: metav1.NewTime(earlier),
					}).
					Obj(),
			},
			wantWorkloadStatuses: map[string]kueue.WorkloadStatus{
				testWorkload: {
					Admission: &kueue.Admission{
						ClusterQueue: "cq",
						PodSetAssignments: []kueue.PodSetAssignment{
							{
								Name:  testPodSetName,
								Count: new(int32(2)),
							},
						},
					},
					Conditions: []metav1.Condition{
						quotaReservedCondition,
						admittedCondition,
						{
							Type:               kueue.WorkloadFinished,
							Status:             metav1.ConditionTrue,
							Reason:             "ByTest",
							Message:            "Finished by test",
							LastTransitionTime: metav1.NewTime(fakeClock.Now()),
						},
						{
							Type:               kueue.WorkloadPodsScheduled,
							Status:             metav1.ConditionFalse,
							Reason:             kueue.WorkloadWaitForScheduling,
							Message:            unscheduledPodsMessage,
							LastTransitionTime: metav1.NewTime(earlier),
						},
					},
				},
			},
		},
		"admitted in the current second requeues after the observation delay period without writing": {
			features: map[featuregate.Feature]bool{
				features.WaitForPodsReadyUnscheduledTimeout: true,
			},
			workloads: []*kueue.Workload{
				utiltestingapi.MakeWorkload(testWorkload, testNamespace).
					UID(testWorkloadUID).
					PodSets(*utiltestingapi.MakePodSet(testPodSetName, 1).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").
						PodSets(utiltestingapi.MakePodSetAssignment(testPodSetName).
							Count(1).
							Obj()).
						Obj(), fakeClock.Now()).
					AdmittedAt(true, fakeClock.Now()).
					Obj(),
			},
			pods: []*corev1.Pod{
				testingpod.MakePod("p1", testNamespace).
					Annotation(kueue.WorkloadAnnotation, testWorkload).
					Label(constants.PodSetLabel, string(testPodSetName)).
					NodeName("node-a").
					StatusConditions(corev1.PodCondition{
						Type:   corev1.PodScheduled,
						Status: corev1.ConditionTrue,
					}).
					Obj(),
			},
			wantResult: reconcile.Result{
				RequeueAfter: time.Second,
			},
			wantWorkloadStatuses: map[string]kueue.WorkloadStatus{
				testWorkload: {
					Admission: &kueue.Admission{
						ClusterQueue: "cq",
						PodSetAssignments: []kueue.PodSetAssignment{
							{
								Name:  testPodSetName,
								Count: new(int32(1)),
							},
						},
					},
					Conditions: []metav1.Condition{
						{
							Type:               kueue.WorkloadQuotaReserved,
							Status:             metav1.ConditionTrue,
							Reason:             "AdmittedByTest",
							Message:            "Admitted by ClusterQueue cq",
							LastTransitionTime: metav1.NewTime(fakeClock.Now()),
						},
						{
							Type:               kueue.WorkloadAdmitted,
							Status:             metav1.ConditionTrue,
							Reason:             "ByTest",
							Message:            "Admitted by ClusterQueue cq",
							LastTransitionTime: metav1.NewTime(fakeClock.Now()),
						},
					},
				},
			},
		},
		"no pods and no condition leaves the workload untouched": {
			features: map[featuregate.Feature]bool{
				features.WaitForPodsReadyUnscheduledTimeout: true,
			},
			workloads: []*kueue.Workload{
				utiltestingapi.MakeWorkload(testWorkload, testNamespace).
					UID(testWorkloadUID).
					PodSets(*utiltestingapi.MakePodSet(testPodSetName, 2).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").
						PodSets(utiltestingapi.MakePodSetAssignment(testPodSetName).
							Count(2).
							Obj()).
						Obj(), earlier).
					AdmittedAt(true, earlier).
					Obj(),
			},
			wantWorkloadStatuses: map[string]kueue.WorkloadStatus{
				testWorkload: {
					Admission: &kueue.Admission{
						ClusterQueue: "cq",
						PodSetAssignments: []kueue.PodSetAssignment{
							{
								Name:  testPodSetName,
								Count: new(int32(2)),
							},
						},
					},
					Conditions: []metav1.Condition{
						quotaReservedCondition,
						admittedCondition,
					},
				},
			},
		},
		"no pods and no condition with a zero grant leaves the workload untouched": {
			features: map[featuregate.Feature]bool{
				features.WaitForPodsReadyUnscheduledTimeout: true,
			},
			workloads: []*kueue.Workload{
				utiltestingapi.MakeWorkload(testWorkload, testNamespace).
					UID(testWorkloadUID).
					PodSets(*utiltestingapi.MakePodSet(testPodSetName, 0).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").
						PodSets(utiltestingapi.MakePodSetAssignment(testPodSetName).
							Count(0).
							Obj()).
						Obj(), earlier).
					AdmittedAt(true, earlier).
					Obj(),
			},
			wantWorkloadStatuses: map[string]kueue.WorkloadStatus{
				testWorkload: {
					Admission: &kueue.Admission{
						ClusterQueue: "cq",
						PodSetAssignments: []kueue.PodSetAssignment{
							{
								Name:  testPodSetName,
								Count: new(int32(0)),
							},
						},
					},
					Conditions: []metav1.Condition{
						quotaReservedCondition,
						admittedCondition,
					},
				},
			},
		},
		"no pods and a current observation of all pods scheduled stands": {
			features: map[featuregate.Feature]bool{
				features.WaitForPodsReadyUnscheduledTimeout: true,
			},
			workloads: []*kueue.Workload{
				utiltestingapi.MakeWorkload(testWorkload, testNamespace).
					UID(testWorkloadUID).
					PodSets(*utiltestingapi.MakePodSet(testPodSetName, 2).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").
						PodSets(utiltestingapi.MakePodSetAssignment(testPodSetName).
							Count(2).
							Obj()).
						Obj(), earlier).
					AdmittedAt(true, earlier).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPodsScheduled,
						Status:             metav1.ConditionTrue,
						Reason:             kueue.WorkloadAllRequiredPodsScheduled,
						Message:            allPodsScheduledMessage,
						LastTransitionTime: metav1.NewTime(fakeClock.Now().Add(-30 * time.Second)),
					}).
					Obj(),
			},
			wantWorkloadStatuses: map[string]kueue.WorkloadStatus{
				testWorkload: {
					Admission: &kueue.Admission{
						ClusterQueue: "cq",
						PodSetAssignments: []kueue.PodSetAssignment{
							{
								Name:  testPodSetName,
								Count: new(int32(2)),
							},
						},
					},
					Conditions: []metav1.Condition{
						quotaReservedCondition,
						admittedCondition,
						{
							Type:               kueue.WorkloadPodsScheduled,
							Status:             metav1.ConditionTrue,
							Reason:             kueue.WorkloadAllRequiredPodsScheduled,
							Message:            allPodsScheduledMessage,
							LastTransitionTime: metav1.NewTime(fakeClock.Now().Add(-30 * time.Second)),
						},
					},
				},
			},
		},
		"no pods and a current observation of unscheduled pods stands": {
			features: map[featuregate.Feature]bool{
				features.WaitForPodsReadyUnscheduledTimeout: true,
			},
			workloads: []*kueue.Workload{
				utiltestingapi.MakeWorkload(testWorkload, testNamespace).
					UID(testWorkloadUID).
					PodSets(*utiltestingapi.MakePodSet(testPodSetName, 2).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").
						PodSets(utiltestingapi.MakePodSetAssignment(testPodSetName).
							Count(2).
							Obj()).
						Obj(), earlier).
					AdmittedAt(true, earlier).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPodsScheduled,
						Status:             metav1.ConditionFalse,
						Reason:             kueue.WorkloadWaitForScheduling,
						Message:            unscheduledPodsMessage,
						LastTransitionTime: metav1.NewTime(fakeClock.Now().Add(-30 * time.Second)),
					}).
					Obj(),
			},
			wantWorkloadStatuses: map[string]kueue.WorkloadStatus{
				testWorkload: {
					Admission: &kueue.Admission{
						ClusterQueue: "cq",
						PodSetAssignments: []kueue.PodSetAssignment{
							{
								Name:  testPodSetName,
								Count: new(int32(2)),
							},
						},
					},
					Conditions: []metav1.Condition{
						quotaReservedCondition,
						admittedCondition,
						{
							Type:               kueue.WorkloadPodsScheduled,
							Status:             metav1.ConditionFalse,
							Reason:             kueue.WorkloadWaitForScheduling,
							Message:            unscheduledPodsMessage,
							LastTransitionTime: metav1.NewTime(fakeClock.Now().Add(-30 * time.Second)),
						},
					},
				},
			},
		},
		"no pods but a current condition and enough reclaimable pods report all pods scheduled": {
			features: map[featuregate.Feature]bool{
				features.WaitForPodsReadyUnscheduledTimeout: true,
				features.ReclaimablePods:                    true,
			},
			workloads: []*kueue.Workload{
				utiltestingapi.MakeWorkload(testWorkload, testNamespace).
					UID(testWorkloadUID).
					PodSets(*utiltestingapi.MakePodSet(testPodSetName, 2).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").
						PodSets(utiltestingapi.MakePodSetAssignment(testPodSetName).
							Count(2).
							Obj()).
						Obj(), earlier).
					AdmittedAt(true, earlier).
					ReclaimablePods(kueue.ReclaimablePod{
						Name:  testPodSetName,
						Count: 2,
					}).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPodsScheduled,
						Status:             metav1.ConditionFalse,
						Reason:             kueue.WorkloadWaitForScheduling,
						Message:            unscheduledPodsMessage,
						LastTransitionTime: metav1.NewTime(fakeClock.Now().Add(-30 * time.Second)),
					}).
					Obj(),
			},
			wantWorkloadStatuses: map[string]kueue.WorkloadStatus{
				testWorkload: {
					Admission: &kueue.Admission{
						ClusterQueue: "cq",
						PodSetAssignments: []kueue.PodSetAssignment{
							{
								Name:  testPodSetName,
								Count: new(int32(2)),
							},
						},
					},
					Conditions: []metav1.Condition{
						quotaReservedCondition,
						admittedCondition,
						{
							Type:               kueue.WorkloadPodsScheduled,
							Status:             metav1.ConditionTrue,
							Reason:             kueue.WorkloadAllRequiredPodsScheduled,
							Message:            allPodsScheduledMessage,
							LastTransitionTime: metav1.NewTime(fakeClock.Now()),
						},
					},
					ReclaimablePods: []kueue.ReclaimablePod{
						{
							Name:  testPodSetName,
							Count: 2,
						},
					},
				},
			},
		},
		"no pods and no condition with enough reclaimable pods leaves the workload untouched": {
			features: map[featuregate.Feature]bool{
				features.WaitForPodsReadyUnscheduledTimeout: true,
				features.ReclaimablePods:                    true,
			},
			workloads: []*kueue.Workload{
				utiltestingapi.MakeWorkload(testWorkload, testNamespace).
					UID(testWorkloadUID).
					PodSets(*utiltestingapi.MakePodSet(testPodSetName, 2).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").
						PodSets(utiltestingapi.MakePodSetAssignment(testPodSetName).
							Count(2).
							Obj()).
						Obj(), earlier).
					AdmittedAt(true, earlier).
					ReclaimablePods(kueue.ReclaimablePod{
						Name:  testPodSetName,
						Count: 2,
					}).
					Obj(),
			},
			wantWorkloadStatuses: map[string]kueue.WorkloadStatus{
				testWorkload: {
					Admission: &kueue.Admission{
						ClusterQueue: "cq",
						PodSetAssignments: []kueue.PodSetAssignment{
							{
								Name:  testPodSetName,
								Count: new(int32(2)),
							},
						},
					},
					Conditions: []metav1.Condition{
						quotaReservedCondition,
						admittedCondition,
					},
					ReclaimablePods: []kueue.ReclaimablePod{
						{
							Name:  testPodSetName,
							Count: 2,
						},
					},
				},
			},
		},
		"only terminating pods do not open the observation": {
			features: map[featuregate.Feature]bool{
				features.WaitForPodsReadyUnscheduledTimeout: true,
			},
			workloads: []*kueue.Workload{
				utiltestingapi.MakeWorkload(testWorkload, testNamespace).
					UID(testWorkloadUID).
					PodSets(*utiltestingapi.MakePodSet(testPodSetName, 1).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").
						PodSets(utiltestingapi.MakePodSetAssignment(testPodSetName).
							Count(1).
							Obj()).
						Obj(), earlier).
					AdmittedAt(true, earlier).
					Obj(),
			},
			pods: []*corev1.Pod{
				testingpod.MakePod("p1", testNamespace).
					Annotation(kueue.WorkloadAnnotation, testWorkload).
					Label(constants.PodSetLabel, string(testPodSetName)).
					NodeName("node-a").
					StatusConditions(corev1.PodCondition{
						Type:   corev1.PodScheduled,
						Status: corev1.ConditionTrue,
					}).
					DeletionTimestamp(fakeClock.Now()).
					Finalizer("example.com/finalizer").
					Obj(),
			},
			wantWorkloadStatuses: map[string]kueue.WorkloadStatus{
				testWorkload: {
					Admission: &kueue.Admission{
						ClusterQueue: "cq",
						PodSetAssignments: []kueue.PodSetAssignment{
							{
								Name:  testPodSetName,
								Count: new(int32(1)),
							},
						},
					},
					Conditions: []metav1.Condition{
						quotaReservedCondition,
						admittedCondition,
					},
				},
			},
		},
		"only terminating pods with a zero grant do not open the observation": {
			features: map[featuregate.Feature]bool{
				features.WaitForPodsReadyUnscheduledTimeout: true,
			},
			workloads: []*kueue.Workload{
				utiltestingapi.MakeWorkload(testWorkload, testNamespace).
					UID(testWorkloadUID).
					PodSets(*utiltestingapi.MakePodSet(testPodSetName, 0).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").
						PodSets(utiltestingapi.MakePodSetAssignment(testPodSetName).
							Count(0).
							Obj()).
						Obj(), earlier).
					AdmittedAt(true, earlier).
					Obj(),
			},
			pods: []*corev1.Pod{
				testingpod.MakePod("p1", testNamespace).
					Annotation(kueue.WorkloadAnnotation, testWorkload).
					Label(constants.PodSetLabel, string(testPodSetName)).
					NodeName("node-a").
					StatusConditions(corev1.PodCondition{
						Type:   corev1.PodScheduled,
						Status: corev1.ConditionTrue,
					}).
					DeletionTimestamp(fakeClock.Now()).
					Finalizer("example.com/finalizer").
					Obj(),
			},
			wantWorkloadStatuses: map[string]kueue.WorkloadStatus{
				testWorkload: {
					Admission: &kueue.Admission{
						ClusterQueue: "cq",
						PodSetAssignments: []kueue.PodSetAssignment{
							{
								Name:  testPodSetName,
								Count: new(int32(0)),
							},
						},
					},
					Conditions: []metav1.Condition{
						quotaReservedCondition,
						admittedCondition,
					},
				},
			},
		},
		"only failed pods of a previous admission do not open the observation": {
			features: map[featuregate.Feature]bool{
				features.WaitForPodsReadyUnscheduledTimeout: true,
			},
			workloads: []*kueue.Workload{
				utiltestingapi.MakeWorkload(testWorkload, testNamespace).
					UID(testWorkloadUID).
					PodSets(*utiltestingapi.MakePodSet(testPodSetName, 1).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").
						PodSets(utiltestingapi.MakePodSetAssignment(testPodSetName).
							Count(1).
							Obj()).
						Obj(), earlier).
					AdmittedAt(true, earlier).
					Obj(),
			},
			pods: []*corev1.Pod{
				testingpod.MakePod("p1", testNamespace).
					Annotation(kueue.WorkloadAnnotation, testWorkload).
					Label(constants.PodSetLabel, string(testPodSetName)).
					NodeName("node-a").
					StatusConditions(corev1.PodCondition{
						Type:   corev1.PodScheduled,
						Status: corev1.ConditionTrue,
					}).
					StatusPhase(corev1.PodFailed).
					Obj(),
			},
			wantWorkloadStatuses: map[string]kueue.WorkloadStatus{
				testWorkload: {
					Admission: &kueue.Admission{
						ClusterQueue: "cq",
						PodSetAssignments: []kueue.PodSetAssignment{
							{
								Name:  testPodSetName,
								Count: new(int32(1)),
							},
						},
					},
					Conditions: []metav1.Condition{
						quotaReservedCondition,
						admittedCondition,
					},
				},
			},
		},
		"only terminating failed pods do not open the observation": {
			features: map[featuregate.Feature]bool{
				features.WaitForPodsReadyUnscheduledTimeout: true,
			},
			workloads: []*kueue.Workload{
				utiltestingapi.MakeWorkload(testWorkload, testNamespace).
					UID(testWorkloadUID).
					PodSets(*utiltestingapi.MakePodSet(testPodSetName, 1).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").
						PodSets(utiltestingapi.MakePodSetAssignment(testPodSetName).
							Count(1).
							Obj()).
						Obj(), earlier).
					AdmittedAt(true, earlier).
					Obj(),
			},
			pods: []*corev1.Pod{
				testingpod.MakePod("p1", testNamespace).
					Annotation(kueue.WorkloadAnnotation, testWorkload).
					Label(constants.PodSetLabel, string(testPodSetName)).
					NodeName("node-a").
					StatusConditions(corev1.PodCondition{
						Type:   corev1.PodScheduled,
						Status: corev1.ConditionTrue,
					}).
					DeletionTimestamp(fakeClock.Now()).
					Finalizer("example.com/finalizer").
					StatusPhase(corev1.PodFailed).
					Obj(),
			},
			wantWorkloadStatuses: map[string]kueue.WorkloadStatus{
				testWorkload: {
					Admission: &kueue.Admission{
						ClusterQueue: "cq",
						PodSetAssignments: []kueue.PodSetAssignment{
							{
								Name:  testPodSetName,
								Count: new(int32(1)),
							},
						},
					},
					Conditions: []metav1.Condition{
						quotaReservedCondition,
						admittedCondition,
					},
				},
			},
		},
		"only failed pods with a zero grant do not open the observation": {
			features: map[featuregate.Feature]bool{
				features.WaitForPodsReadyUnscheduledTimeout: true,
			},
			workloads: []*kueue.Workload{
				utiltestingapi.MakeWorkload(testWorkload, testNamespace).
					UID(testWorkloadUID).
					PodSets(*utiltestingapi.MakePodSet(testPodSetName, 0).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").
						PodSets(utiltestingapi.MakePodSetAssignment(testPodSetName).
							Count(0).
							Obj()).
						Obj(), earlier).
					AdmittedAt(true, earlier).
					Obj(),
			},
			pods: []*corev1.Pod{
				testingpod.MakePod("p1", testNamespace).
					Annotation(kueue.WorkloadAnnotation, testWorkload).
					Label(constants.PodSetLabel, string(testPodSetName)).
					NodeName("node-a").
					StatusConditions(corev1.PodCondition{
						Type:   corev1.PodScheduled,
						Status: corev1.ConditionTrue,
					}).
					StatusPhase(corev1.PodFailed).
					Obj(),
			},
			wantWorkloadStatuses: map[string]kueue.WorkloadStatus{
				testWorkload: {
					Admission: &kueue.Admission{
						ClusterQueue: "cq",
						PodSetAssignments: []kueue.PodSetAssignment{
							{
								Name:  testPodSetName,
								Count: new(int32(0)),
							},
						},
					},
					Conditions: []metav1.Condition{
						quotaReservedCondition,
						admittedCondition,
					},
				},
			},
		},
		"only failed pods with enough reclaimable pods do not open the observation": {
			features: map[featuregate.Feature]bool{
				features.WaitForPodsReadyUnscheduledTimeout: true,
				features.ReclaimablePods:                    true,
			},
			workloads: []*kueue.Workload{
				utiltestingapi.MakeWorkload(testWorkload, testNamespace).
					UID(testWorkloadUID).
					PodSets(*utiltestingapi.MakePodSet(testPodSetName, 1).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").
						PodSets(utiltestingapi.MakePodSetAssignment(testPodSetName).
							Count(1).
							Obj()).
						Obj(), earlier).
					AdmittedAt(true, earlier).
					ReclaimablePods(kueue.ReclaimablePod{
						Name:  testPodSetName,
						Count: 1,
					}).
					Obj(),
			},
			pods: []*corev1.Pod{
				testingpod.MakePod("p1", testNamespace).
					Annotation(kueue.WorkloadAnnotation, testWorkload).
					Label(constants.PodSetLabel, string(testPodSetName)).
					NodeName("node-a").
					StatusConditions(corev1.PodCondition{
						Type:   corev1.PodScheduled,
						Status: corev1.ConditionTrue,
					}).
					StatusPhase(corev1.PodFailed).
					Obj(),
			},
			wantWorkloadStatuses: map[string]kueue.WorkloadStatus{
				testWorkload: {
					Admission: &kueue.Admission{
						ClusterQueue: "cq",
						PodSetAssignments: []kueue.PodSetAssignment{
							{
								Name:  testPodSetName,
								Count: new(int32(1)),
							},
						},
					},
					Conditions: []metav1.Condition{
						quotaReservedCondition,
						admittedCondition,
					},
					ReclaimablePods: []kueue.ReclaimablePod{
						{
							Name:  testPodSetName,
							Count: 1,
						},
					},
				},
			},
		},
		"succeeded pods below the grant do not open the observation": {
			features: map[featuregate.Feature]bool{
				features.WaitForPodsReadyUnscheduledTimeout: true,
			},
			workloads: []*kueue.Workload{
				utiltestingapi.MakeWorkload(testWorkload, testNamespace).
					UID(testWorkloadUID).
					PodSets(*utiltestingapi.MakePodSet(testPodSetName, 2).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").
						PodSets(utiltestingapi.MakePodSetAssignment(testPodSetName).
							Count(2).
							Obj()).
						Obj(), earlier).
					AdmittedAt(true, earlier).
					Obj(),
			},
			pods: []*corev1.Pod{
				testingpod.MakePod("p1", testNamespace).
					Annotation(kueue.WorkloadAnnotation, testWorkload).
					Label(constants.PodSetLabel, string(testPodSetName)).
					StatusPhase(corev1.PodSucceeded).
					Obj(),
			},
			wantWorkloadStatuses: map[string]kueue.WorkloadStatus{
				testWorkload: {
					Admission: &kueue.Admission{
						ClusterQueue: "cq",
						PodSetAssignments: []kueue.PodSetAssignment{
							{
								Name:  testPodSetName,
								Count: new(int32(2)),
							},
						},
					},
					Conditions: []metav1.Condition{
						quotaReservedCondition,
						admittedCondition,
					},
				},
			},
		},
		"succeeded pods filling the grant report all pods scheduled": {
			features: map[featuregate.Feature]bool{
				features.WaitForPodsReadyUnscheduledTimeout: true,
			},
			workloads: []*kueue.Workload{
				utiltestingapi.MakeWorkload(testWorkload, testNamespace).
					UID(testWorkloadUID).
					PodSets(*utiltestingapi.MakePodSet(testPodSetName, 2).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").
						PodSets(utiltestingapi.MakePodSetAssignment(testPodSetName).
							Count(2).
							Obj()).
						Obj(), earlier).
					AdmittedAt(true, earlier).
					Obj(),
			},
			pods: []*corev1.Pod{
				testingpod.MakePod("p1", testNamespace).
					Annotation(kueue.WorkloadAnnotation, testWorkload).
					Label(constants.PodSetLabel, string(testPodSetName)).
					StatusPhase(corev1.PodSucceeded).
					Obj(),
				testingpod.MakePod("p2", testNamespace).
					Annotation(kueue.WorkloadAnnotation, testWorkload).
					Label(constants.PodSetLabel, string(testPodSetName)).
					StatusPhase(corev1.PodSucceeded).
					Obj(),
			},
			wantWorkloadStatuses: map[string]kueue.WorkloadStatus{
				testWorkload: {
					Admission: &kueue.Admission{
						ClusterQueue: "cq",
						PodSetAssignments: []kueue.PodSetAssignment{
							{
								Name:  testPodSetName,
								Count: new(int32(2)),
							},
						},
					},
					Conditions: []metav1.Condition{
						quotaReservedCondition,
						admittedCondition,
						{
							Type:               kueue.WorkloadPodsScheduled,
							Status:             metav1.ConditionTrue,
							Reason:             kueue.WorkloadAllRequiredPodsScheduled,
							Message:            allPodsScheduledMessage,
							LastTransitionTime: metav1.NewTime(fakeClock.Now()),
						},
					},
				},
			},
		},
		"terminating succeeded pods filling the grant report all pods scheduled": {
			features: map[featuregate.Feature]bool{
				features.WaitForPodsReadyUnscheduledTimeout: true,
			},
			workloads: []*kueue.Workload{
				utiltestingapi.MakeWorkload(testWorkload, testNamespace).
					UID(testWorkloadUID).
					PodSets(*utiltestingapi.MakePodSet(testPodSetName, 1).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").
						PodSets(utiltestingapi.MakePodSetAssignment(testPodSetName).
							Count(1).
							Obj()).
						Obj(), earlier).
					AdmittedAt(true, earlier).
					Obj(),
			},
			pods: []*corev1.Pod{
				testingpod.MakePod("p1", testNamespace).
					Annotation(kueue.WorkloadAnnotation, testWorkload).
					Label(constants.PodSetLabel, string(testPodSetName)).
					NodeName("node-a").
					DeletionTimestamp(fakeClock.Now()).
					Finalizer("example.com/finalizer").
					StatusPhase(corev1.PodSucceeded).
					Obj(),
			},
			wantWorkloadStatuses: map[string]kueue.WorkloadStatus{
				testWorkload: {
					Admission: &kueue.Admission{
						ClusterQueue: "cq",
						PodSetAssignments: []kueue.PodSetAssignment{
							{
								Name:  testPodSetName,
								Count: new(int32(1)),
							},
						},
					},
					Conditions: []metav1.Condition{
						quotaReservedCondition,
						admittedCondition,
						{
							Type:               kueue.WorkloadPodsScheduled,
							Status:             metav1.ConditionTrue,
							Reason:             kueue.WorkloadAllRequiredPodsScheduled,
							Message:            allPodsScheduledMessage,
							LastTransitionTime: metav1.NewTime(fakeClock.Now()),
						},
					},
				},
			},
		},
		"surplus succeeded pods in one podset do not fill another podset's grant": {
			features: map[featuregate.Feature]bool{
				features.WaitForPodsReadyUnscheduledTimeout: true,
			},
			workloads: []*kueue.Workload{
				utiltestingapi.MakeWorkload(testWorkload, testNamespace).
					UID(testWorkloadUID).
					PodSets(
						*utiltestingapi.MakePodSet("leader", 1).
							Request(corev1.ResourceCPU, "1").
							Obj(),
						*utiltestingapi.MakePodSet("worker", 1).
							Request(corev1.ResourceCPU, "1").
							Obj(),
					).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").
						PodSets(
							utiltestingapi.MakePodSetAssignment("leader").
								Count(1).
								Obj(),
							utiltestingapi.MakePodSetAssignment("worker").
								Count(1).
								Obj(),
						).
						Obj(), earlier).
					AdmittedAt(true, earlier).
					Obj(),
			},
			pods: []*corev1.Pod{
				testingpod.MakePod("l1", testNamespace).
					Annotation(kueue.WorkloadAnnotation, testWorkload).
					Label(constants.PodSetLabel, "leader").
					StatusPhase(corev1.PodSucceeded).
					Obj(),
				testingpod.MakePod("l2", testNamespace).
					Annotation(kueue.WorkloadAnnotation, testWorkload).
					Label(constants.PodSetLabel, "leader").
					StatusPhase(corev1.PodSucceeded).
					Obj(),
			},
			wantWorkloadStatuses: map[string]kueue.WorkloadStatus{
				testWorkload: {
					Admission: &kueue.Admission{
						ClusterQueue: "cq",
						PodSetAssignments: []kueue.PodSetAssignment{
							{
								Name:  "leader",
								Count: new(int32(1)),
							},
							{
								Name:  "worker",
								Count: new(int32(1)),
							},
						},
					},
					Conditions: []metav1.Condition{
						quotaReservedCondition,
						admittedCondition,
					},
				},
			},
		},
		"failed pod with a pending replacement reports unscheduled pods": {
			features: map[featuregate.Feature]bool{
				features.WaitForPodsReadyUnscheduledTimeout: true,
			},
			workloads: []*kueue.Workload{
				utiltestingapi.MakeWorkload(testWorkload, testNamespace).
					UID(testWorkloadUID).
					PodSets(*utiltestingapi.MakePodSet(testPodSetName, 1).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").
						PodSets(utiltestingapi.MakePodSetAssignment(testPodSetName).
							Count(1).
							Obj()).
						Obj(), earlier).
					AdmittedAt(true, earlier).
					Obj(),
			},
			pods: []*corev1.Pod{
				testingpod.MakePod("p1", testNamespace).
					Annotation(kueue.WorkloadAnnotation, testWorkload).
					Label(constants.PodSetLabel, string(testPodSetName)).
					NodeName("node-a").
					StatusConditions(corev1.PodCondition{
						Type:   corev1.PodScheduled,
						Status: corev1.ConditionTrue,
					}).
					StatusPhase(corev1.PodFailed).
					Obj(),
				testingpod.MakePod("p1-replacement", testNamespace).
					Annotation(kueue.WorkloadAnnotation, testWorkload).
					Label(constants.PodSetLabel, string(testPodSetName)).
					Obj(),
			},
			wantWorkloadStatuses: map[string]kueue.WorkloadStatus{
				testWorkload: {
					Admission: &kueue.Admission{
						ClusterQueue: "cq",
						PodSetAssignments: []kueue.PodSetAssignment{
							{
								Name:  testPodSetName,
								Count: new(int32(1)),
							},
						},
					},
					Conditions: []metav1.Condition{
						quotaReservedCondition,
						admittedCondition,
						{
							Type:               kueue.WorkloadPodsScheduled,
							Status:             metav1.ConditionFalse,
							Reason:             kueue.WorkloadWaitForScheduling,
							Message:            unscheduledPodsMessage,
							LastTransitionTime: metav1.NewTime(fakeClock.Now()),
						},
					},
				},
			},
		},
		"pending pods are not scheduled": {
			features: map[featuregate.Feature]bool{
				features.WaitForPodsReadyUnscheduledTimeout: true,
			},
			workloads: []*kueue.Workload{
				utiltestingapi.MakeWorkload(testWorkload, testNamespace).
					UID(testWorkloadUID).
					PodSets(*utiltestingapi.MakePodSet(testPodSetName, 2).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").
						PodSets(utiltestingapi.MakePodSetAssignment(testPodSetName).
							Count(2).
							Obj()).
						Obj(), earlier).
					AdmittedAt(true, earlier).
					Obj(),
			},
			pods: []*corev1.Pod{
				testingpod.MakePod("p1", testNamespace).
					Annotation(kueue.WorkloadAnnotation, testWorkload).
					Label(constants.PodSetLabel, string(testPodSetName)).
					Obj(),
				testingpod.MakePod("p2", testNamespace).
					Annotation(kueue.WorkloadAnnotation, testWorkload).
					Label(constants.PodSetLabel, string(testPodSetName)).
					Obj(),
			},
			wantWorkloadStatuses: map[string]kueue.WorkloadStatus{
				testWorkload: {
					Admission: &kueue.Admission{
						ClusterQueue: "cq",
						PodSetAssignments: []kueue.PodSetAssignment{
							{
								Name:  testPodSetName,
								Count: new(int32(2)),
							},
						},
					},
					Conditions: []metav1.Condition{
						quotaReservedCondition,
						admittedCondition,
						{
							Type:               kueue.WorkloadPodsScheduled,
							Status:             metav1.ConditionFalse,
							Reason:             kueue.WorkloadWaitForScheduling,
							Message:            unscheduledPodsMessage,
							LastTransitionTime: metav1.NewTime(fakeClock.Now()),
						},
					},
				},
			},
		},
		"scheduling-gated pods report unscheduled pods": {
			features: map[featuregate.Feature]bool{
				features.WaitForPodsReadyUnscheduledTimeout: true,
			},
			workloads: []*kueue.Workload{
				utiltestingapi.MakeWorkload(testWorkload, testNamespace).
					UID(testWorkloadUID).
					PodSets(*utiltestingapi.MakePodSet(testPodSetName, 1).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").
						PodSets(utiltestingapi.MakePodSetAssignment(testPodSetName).
							Count(1).
							Obj()).
						Obj(), earlier).
					AdmittedAt(true, earlier).
					Obj(),
			},
			pods: []*corev1.Pod{
				testingpod.MakePod("p1", testNamespace).
					Annotation(kueue.WorkloadAnnotation, testWorkload).
					Label(constants.PodSetLabel, string(testPodSetName)).
					Gate("example.com/gate").
					StatusConditions(corev1.PodCondition{
						Type:   corev1.PodScheduled,
						Status: corev1.ConditionFalse,
						Reason: corev1.PodReasonSchedulingGated,
					}).
					Obj(),
			},
			wantWorkloadStatuses: map[string]kueue.WorkloadStatus{
				testWorkload: {
					Admission: &kueue.Admission{
						ClusterQueue: "cq",
						PodSetAssignments: []kueue.PodSetAssignment{
							{
								Name:  testPodSetName,
								Count: new(int32(1)),
							},
						},
					},
					Conditions: []metav1.Condition{
						quotaReservedCondition,
						admittedCondition,
						{
							Type:               kueue.WorkloadPodsScheduled,
							Status:             metav1.ConditionFalse,
							Reason:             kueue.WorkloadWaitForScheduling,
							Message:            unscheduledPodsMessage,
							LastTransitionTime: metav1.NewTime(fakeClock.Now()),
						},
					},
				},
			},
		},
		"some pods scheduled": {
			features: map[featuregate.Feature]bool{
				features.WaitForPodsReadyUnscheduledTimeout: true,
			},
			workloads: []*kueue.Workload{
				utiltestingapi.MakeWorkload(testWorkload, testNamespace).
					UID(testWorkloadUID).
					PodSets(*utiltestingapi.MakePodSet(testPodSetName, 2).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").
						PodSets(utiltestingapi.MakePodSetAssignment(testPodSetName).
							Count(2).
							Obj()).
						Obj(), earlier).
					AdmittedAt(true, earlier).
					Obj(),
			},
			pods: []*corev1.Pod{
				testingpod.MakePod("p1", testNamespace).
					Annotation(kueue.WorkloadAnnotation, testWorkload).
					Label(constants.PodSetLabel, string(testPodSetName)).
					NodeName("node-a").
					StatusConditions(corev1.PodCondition{
						Type:   corev1.PodScheduled,
						Status: corev1.ConditionTrue,
					}).
					Obj(),
				testingpod.MakePod("p2", testNamespace).
					Annotation(kueue.WorkloadAnnotation, testWorkload).
					Label(constants.PodSetLabel, string(testPodSetName)).
					Obj(),
			},
			wantWorkloadStatuses: map[string]kueue.WorkloadStatus{
				testWorkload: {
					Admission: &kueue.Admission{
						ClusterQueue: "cq",
						PodSetAssignments: []kueue.PodSetAssignment{
							{
								Name:  testPodSetName,
								Count: new(int32(2)),
							},
						},
					},
					Conditions: []metav1.Condition{
						quotaReservedCondition,
						admittedCondition,
						{
							Type:               kueue.WorkloadPodsScheduled,
							Status:             metav1.ConditionFalse,
							Reason:             kueue.WorkloadWaitForScheduling,
							Message:            unscheduledPodsMessage,
							LastTransitionTime: metav1.NewTime(fakeClock.Now()),
						},
					},
				},
			},
		},
		"all pods scheduled": {
			features: map[featuregate.Feature]bool{
				features.WaitForPodsReadyUnscheduledTimeout: true,
			},
			workloads: []*kueue.Workload{
				utiltestingapi.MakeWorkload(testWorkload, testNamespace).
					UID(testWorkloadUID).
					PodSets(*utiltestingapi.MakePodSet(testPodSetName, 2).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").
						PodSets(utiltestingapi.MakePodSetAssignment(testPodSetName).
							Count(2).
							Obj()).
						Obj(), earlier).
					AdmittedAt(true, earlier).
					Obj(),
			},
			pods: []*corev1.Pod{
				testingpod.MakePod("p1", testNamespace).
					Annotation(kueue.WorkloadAnnotation, testWorkload).
					Label(constants.PodSetLabel, string(testPodSetName)).
					NodeName("node-a").
					StatusConditions(corev1.PodCondition{
						Type:   corev1.PodScheduled,
						Status: corev1.ConditionTrue,
					}).
					Obj(),
				testingpod.MakePod("p2", testNamespace).
					Annotation(kueue.WorkloadAnnotation, testWorkload).
					Label(constants.PodSetLabel, string(testPodSetName)).
					NodeName("node-a").
					StatusConditions(corev1.PodCondition{
						Type:   corev1.PodScheduled,
						Status: corev1.ConditionTrue,
					}).
					Obj(),
			},
			wantWorkloadStatuses: map[string]kueue.WorkloadStatus{
				testWorkload: {
					Admission: &kueue.Admission{
						ClusterQueue: "cq",
						PodSetAssignments: []kueue.PodSetAssignment{
							{
								Name:  testPodSetName,
								Count: new(int32(2)),
							},
						},
					},
					Conditions: []metav1.Condition{
						quotaReservedCondition,
						admittedCondition,
						{
							Type:               kueue.WorkloadPodsScheduled,
							Status:             metav1.ConditionTrue,
							Reason:             kueue.WorkloadAllRequiredPodsScheduled,
							Message:            allPodsScheduledMessage,
							LastTransitionTime: metav1.NewTime(fakeClock.Now()),
						},
					},
				},
			},
		},
		"pod with a preset nodeName waits for PodScheduled=True": {
			features: map[featuregate.Feature]bool{
				features.WaitForPodsReadyUnscheduledTimeout: true,
			},
			workloads: []*kueue.Workload{
				utiltestingapi.MakeWorkload(testWorkload, testNamespace).
					UID(testWorkloadUID).
					PodSets(*utiltestingapi.MakePodSet(testPodSetName, 1).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").
						PodSets(utiltestingapi.MakePodSetAssignment(testPodSetName).
							Count(1).
							Obj()).
						Obj(), earlier).
					AdmittedAt(true, earlier).
					Obj(),
			},
			pods: []*corev1.Pod{
				testingpod.MakePod("p1", testNamespace).
					Annotation(kueue.WorkloadAnnotation, testWorkload).
					Label(constants.PodSetLabel, string(testPodSetName)).
					NodeName("node-a").
					Obj(),
			},
			wantWorkloadStatuses: map[string]kueue.WorkloadStatus{
				testWorkload: {
					Admission: &kueue.Admission{
						ClusterQueue: "cq",
						PodSetAssignments: []kueue.PodSetAssignment{
							{
								Name:  testPodSetName,
								Count: new(int32(1)),
							},
						},
					},
					Conditions: []metav1.Condition{
						quotaReservedCondition,
						admittedCondition,
						{
							Type:               kueue.WorkloadPodsScheduled,
							Status:             metav1.ConditionFalse,
							Reason:             kueue.WorkloadWaitForScheduling,
							Message:            unscheduledPodsMessage,
							LastTransitionTime: metav1.NewTime(fakeClock.Now()),
						},
					},
				},
			},
		},
		"pod with PodScheduled=True waits for nodeName": {
			features: map[featuregate.Feature]bool{
				features.WaitForPodsReadyUnscheduledTimeout: true,
			},
			workloads: []*kueue.Workload{
				utiltestingapi.MakeWorkload(testWorkload, testNamespace).
					UID(testWorkloadUID).
					PodSets(*utiltestingapi.MakePodSet(testPodSetName, 1).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").
						PodSets(utiltestingapi.MakePodSetAssignment(testPodSetName).
							Count(1).
							Obj()).
						Obj(), earlier).
					AdmittedAt(true, earlier).
					Obj(),
			},
			pods: []*corev1.Pod{
				testingpod.MakePod("p1", testNamespace).
					Annotation(kueue.WorkloadAnnotation, testWorkload).
					Label(constants.PodSetLabel, string(testPodSetName)).
					StatusConditions(corev1.PodCondition{
						Type:   corev1.PodScheduled,
						Status: corev1.ConditionTrue,
					}).
					Obj(),
			},
			wantWorkloadStatuses: map[string]kueue.WorkloadStatus{
				testWorkload: {
					Admission: &kueue.Admission{
						ClusterQueue: "cq",
						PodSetAssignments: []kueue.PodSetAssignment{
							{
								Name:  testPodSetName,
								Count: new(int32(1)),
							},
						},
					},
					Conditions: []metav1.Condition{
						quotaReservedCondition,
						admittedCondition,
						{
							Type:               kueue.WorkloadPodsScheduled,
							Status:             metav1.ConditionFalse,
							Reason:             kueue.WorkloadWaitForScheduling,
							Message:            unscheduledPodsMessage,
							LastTransitionTime: metav1.NewTime(fakeClock.Now()),
						},
					},
				},
			},
		},
		"failed pods do not satisfy the grant until replaced": {
			features: map[featuregate.Feature]bool{
				features.WaitForPodsReadyUnscheduledTimeout: true,
			},
			workloads: []*kueue.Workload{
				utiltestingapi.MakeWorkload(testWorkload, testNamespace).
					UID(testWorkloadUID).
					PodSets(*utiltestingapi.MakePodSet(testPodSetName, 2).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").
						PodSets(utiltestingapi.MakePodSetAssignment(testPodSetName).
							Count(2).
							Obj()).
						Obj(), earlier).
					AdmittedAt(true, earlier).
					Obj(),
			},
			pods: []*corev1.Pod{
				testingpod.MakePod("p1", testNamespace).
					Annotation(kueue.WorkloadAnnotation, testWorkload).
					Label(constants.PodSetLabel, string(testPodSetName)).
					NodeName("node-a").
					StatusConditions(corev1.PodCondition{
						Type:   corev1.PodScheduled,
						Status: corev1.ConditionTrue,
					}).
					Obj(),
				testingpod.MakePod("p2", testNamespace).
					Annotation(kueue.WorkloadAnnotation, testWorkload).
					Label(constants.PodSetLabel, string(testPodSetName)).
					NodeName("node-a").
					StatusConditions(corev1.PodCondition{
						Type:   corev1.PodScheduled,
						Status: corev1.ConditionTrue,
					}).
					StatusPhase(corev1.PodFailed).
					Obj(),
			},
			wantWorkloadStatuses: map[string]kueue.WorkloadStatus{
				testWorkload: {
					Admission: &kueue.Admission{
						ClusterQueue: "cq",
						PodSetAssignments: []kueue.PodSetAssignment{
							{
								Name:  testPodSetName,
								Count: new(int32(2)),
							},
						},
					},
					Conditions: []metav1.Condition{
						quotaReservedCondition,
						admittedCondition,
						{
							Type:               kueue.WorkloadPodsScheduled,
							Status:             metav1.ConditionFalse,
							Reason:             kueue.WorkloadWaitForScheduling,
							Message:            unscheduledPodsMessage,
							LastTransitionTime: metav1.NewTime(fakeClock.Now()),
						},
					},
				},
			},
		},
		"replacement pod scheduled after a failed one": {
			features: map[featuregate.Feature]bool{
				features.WaitForPodsReadyUnscheduledTimeout: true,
			},
			workloads: []*kueue.Workload{
				utiltestingapi.MakeWorkload(testWorkload, testNamespace).
					UID(testWorkloadUID).
					PodSets(*utiltestingapi.MakePodSet(testPodSetName, 2).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").
						PodSets(utiltestingapi.MakePodSetAssignment(testPodSetName).
							Count(2).
							Obj()).
						Obj(), earlier).
					AdmittedAt(true, earlier).
					Obj(),
			},
			pods: []*corev1.Pod{
				testingpod.MakePod("p1", testNamespace).
					Annotation(kueue.WorkloadAnnotation, testWorkload).
					Label(constants.PodSetLabel, string(testPodSetName)).
					NodeName("node-a").
					StatusConditions(corev1.PodCondition{
						Type:   corev1.PodScheduled,
						Status: corev1.ConditionTrue,
					}).
					Obj(),
				testingpod.MakePod("p2", testNamespace).
					Annotation(kueue.WorkloadAnnotation, testWorkload).
					Label(constants.PodSetLabel, string(testPodSetName)).
					NodeName("node-a").
					StatusConditions(corev1.PodCondition{
						Type:   corev1.PodScheduled,
						Status: corev1.ConditionTrue,
					}).
					StatusPhase(corev1.PodFailed).
					Obj(),
				testingpod.MakePod("p2-replacement", testNamespace).
					Annotation(kueue.WorkloadAnnotation, testWorkload).
					Label(constants.PodSetLabel, string(testPodSetName)).
					NodeName("node-a").
					StatusConditions(corev1.PodCondition{
						Type:   corev1.PodScheduled,
						Status: corev1.ConditionTrue,
					}).
					Obj(),
			},
			wantWorkloadStatuses: map[string]kueue.WorkloadStatus{
				testWorkload: {
					Admission: &kueue.Admission{
						ClusterQueue: "cq",
						PodSetAssignments: []kueue.PodSetAssignment{
							{
								Name:  testPodSetName,
								Count: new(int32(2)),
							},
						},
					},
					Conditions: []metav1.Condition{
						quotaReservedCondition,
						admittedCondition,
						{
							Type:               kueue.WorkloadPodsScheduled,
							Status:             metav1.ConditionTrue,
							Reason:             kueue.WorkloadAllRequiredPodsScheduled,
							Message:            allPodsScheduledMessage,
							LastTransitionTime: metav1.NewTime(fakeClock.Now()),
						},
					},
				},
			},
		},
		"succeeded pods satisfy the grant": {
			features: map[featuregate.Feature]bool{
				features.WaitForPodsReadyUnscheduledTimeout: true,
			},
			workloads: []*kueue.Workload{
				utiltestingapi.MakeWorkload(testWorkload, testNamespace).
					UID(testWorkloadUID).
					PodSets(*utiltestingapi.MakePodSet(testPodSetName, 2).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").
						PodSets(utiltestingapi.MakePodSetAssignment(testPodSetName).
							Count(2).
							Obj()).
						Obj(), earlier).
					AdmittedAt(true, earlier).
					Obj(),
			},
			pods: []*corev1.Pod{
				testingpod.MakePod("p1", testNamespace).
					Annotation(kueue.WorkloadAnnotation, testWorkload).
					Label(constants.PodSetLabel, string(testPodSetName)).
					NodeName("node-a").
					StatusConditions(corev1.PodCondition{
						Type:   corev1.PodScheduled,
						Status: corev1.ConditionTrue,
					}).
					Obj(),
				testingpod.MakePod("p2", testNamespace).
					Annotation(kueue.WorkloadAnnotation, testWorkload).
					Label(constants.PodSetLabel, string(testPodSetName)).
					StatusPhase(corev1.PodSucceeded).
					Obj(),
			},
			wantWorkloadStatuses: map[string]kueue.WorkloadStatus{
				testWorkload: {
					Admission: &kueue.Admission{
						ClusterQueue: "cq",
						PodSetAssignments: []kueue.PodSetAssignment{
							{
								Name:  testPodSetName,
								Count: new(int32(2)),
							},
						},
					},
					Conditions: []metav1.Condition{
						quotaReservedCondition,
						admittedCondition,
						{
							Type:               kueue.WorkloadPodsScheduled,
							Status:             metav1.ConditionTrue,
							Reason:             kueue.WorkloadAllRequiredPodsScheduled,
							Message:            allPodsScheduledMessage,
							LastTransitionTime: metav1.NewTime(fakeClock.Now()),
						},
					},
				},
			},
		},
		"terminating scheduled pod does not satisfy the grant": {
			features: map[featuregate.Feature]bool{
				features.WaitForPodsReadyUnscheduledTimeout: true,
			},
			workloads: []*kueue.Workload{
				utiltestingapi.MakeWorkload(testWorkload, testNamespace).
					UID(testWorkloadUID).
					PodSets(*utiltestingapi.MakePodSet(testPodSetName, 2).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").
						PodSets(utiltestingapi.MakePodSetAssignment(testPodSetName).
							Count(2).
							Obj()).
						Obj(), earlier).
					AdmittedAt(true, earlier).
					Obj(),
			},
			pods: []*corev1.Pod{
				testingpod.MakePod("p1", testNamespace).
					Annotation(kueue.WorkloadAnnotation, testWorkload).
					Label(constants.PodSetLabel, string(testPodSetName)).
					NodeName("node-a").
					StatusConditions(corev1.PodCondition{
						Type:   corev1.PodScheduled,
						Status: corev1.ConditionTrue,
					}).
					Obj(),
				testingpod.MakePod("p2", testNamespace).
					Annotation(kueue.WorkloadAnnotation, testWorkload).
					Label(constants.PodSetLabel, string(testPodSetName)).
					NodeName("node-a").
					StatusConditions(corev1.PodCondition{
						Type:   corev1.PodScheduled,
						Status: corev1.ConditionTrue,
					}).
					DeletionTimestamp(fakeClock.Now()).
					Finalizer("example.com/finalizer").
					Obj(),
			},
			wantWorkloadStatuses: map[string]kueue.WorkloadStatus{
				testWorkload: {
					Admission: &kueue.Admission{
						ClusterQueue: "cq",
						PodSetAssignments: []kueue.PodSetAssignment{
							{
								Name:  testPodSetName,
								Count: new(int32(2)),
							},
						},
					},
					Conditions: []metav1.Condition{
						quotaReservedCondition,
						admittedCondition,
						{
							Type:               kueue.WorkloadPodsScheduled,
							Status:             metav1.ConditionFalse,
							Reason:             kueue.WorkloadWaitForScheduling,
							Message:            unscheduledPodsMessage,
							LastTransitionTime: metav1.NewTime(fakeClock.Now()),
						},
					},
				},
			},
		},
		"reclaimable pods satisfy the grant of deleted succeeded pods": {
			features: map[featuregate.Feature]bool{
				features.WaitForPodsReadyUnscheduledTimeout: true,
				features.ReclaimablePods:                    true,
			},
			workloads: []*kueue.Workload{
				utiltestingapi.MakeWorkload(testWorkload, testNamespace).
					UID(testWorkloadUID).
					PodSets(*utiltestingapi.MakePodSet(testPodSetName, 2).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").
						PodSets(utiltestingapi.MakePodSetAssignment(testPodSetName).
							Count(2).
							Obj()).
						Obj(), earlier).
					AdmittedAt(true, earlier).
					ReclaimablePods(kueue.ReclaimablePod{
						Name:  testPodSetName,
						Count: 1,
					}).
					Obj(),
			},
			pods: []*corev1.Pod{
				testingpod.MakePod("p1", testNamespace).
					Annotation(kueue.WorkloadAnnotation, testWorkload).
					Label(constants.PodSetLabel, string(testPodSetName)).
					NodeName("node-a").
					StatusConditions(corev1.PodCondition{
						Type:   corev1.PodScheduled,
						Status: corev1.ConditionTrue,
					}).
					Obj(),
			},
			wantWorkloadStatuses: map[string]kueue.WorkloadStatus{
				testWorkload: {
					Admission: &kueue.Admission{
						ClusterQueue: "cq",
						PodSetAssignments: []kueue.PodSetAssignment{
							{
								Name:  testPodSetName,
								Count: new(int32(2)),
							},
						},
					},
					Conditions: []metav1.Condition{
						quotaReservedCondition,
						admittedCondition,
						{
							Type:               kueue.WorkloadPodsScheduled,
							Status:             metav1.ConditionTrue,
							Reason:             kueue.WorkloadAllRequiredPodsScheduled,
							Message:            allPodsScheduledMessage,
							LastTransitionTime: metav1.NewTime(fakeClock.Now()),
						},
					},
					ReclaimablePods: []kueue.ReclaimablePod{
						{
							Name:  testPodSetName,
							Count: 1,
						},
					},
				},
			},
		},
		"reclaimable pods are ignored when the ReclaimablePods feature is disabled": {
			features: map[featuregate.Feature]bool{
				features.WaitForPodsReadyUnscheduledTimeout: true,
				features.ReclaimablePods:                    false,
			},
			workloads: []*kueue.Workload{
				utiltestingapi.MakeWorkload(testWorkload, testNamespace).
					UID(testWorkloadUID).
					PodSets(*utiltestingapi.MakePodSet(testPodSetName, 2).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").
						PodSets(utiltestingapi.MakePodSetAssignment(testPodSetName).
							Count(2).
							Obj()).
						Obj(), earlier).
					AdmittedAt(true, earlier).
					ReclaimablePods(kueue.ReclaimablePod{
						Name:  testPodSetName,
						Count: 1,
					}).
					Obj(),
			},
			pods: []*corev1.Pod{
				testingpod.MakePod("p1", testNamespace).
					Annotation(kueue.WorkloadAnnotation, testWorkload).
					Label(constants.PodSetLabel, string(testPodSetName)).
					NodeName("node-a").
					StatusConditions(corev1.PodCondition{
						Type:   corev1.PodScheduled,
						Status: corev1.ConditionTrue,
					}).
					Obj(),
			},
			wantWorkloadStatuses: map[string]kueue.WorkloadStatus{
				testWorkload: {
					Admission: &kueue.Admission{
						ClusterQueue: "cq",
						PodSetAssignments: []kueue.PodSetAssignment{
							{
								Name:  testPodSetName,
								Count: new(int32(2)),
							},
						},
					},
					Conditions: []metav1.Condition{
						quotaReservedCondition,
						admittedCondition,
						{
							Type:               kueue.WorkloadPodsScheduled,
							Status:             metav1.ConditionFalse,
							Reason:             kueue.WorkloadWaitForScheduling,
							Message:            unscheduledPodsMessage,
							LastTransitionTime: metav1.NewTime(fakeClock.Now()),
						},
					},
					ReclaimablePods: []kueue.ReclaimablePod{
						{
							Name:  testPodSetName,
							Count: 1,
						},
					},
				},
			},
		},
		"reclaimable and succeeded pods are not summed": {
			features: map[featuregate.Feature]bool{
				features.WaitForPodsReadyUnscheduledTimeout: true,
				features.ReclaimablePods:                    true,
			},
			workloads: []*kueue.Workload{
				utiltestingapi.MakeWorkload(testWorkload, testNamespace).
					UID(testWorkloadUID).
					PodSets(*utiltestingapi.MakePodSet(testPodSetName, 3).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").
						PodSets(utiltestingapi.MakePodSetAssignment(testPodSetName).
							Count(3).
							Obj()).
						Obj(), earlier).
					AdmittedAt(true, earlier).
					ReclaimablePods(kueue.ReclaimablePod{
						Name:  testPodSetName,
						Count: 1,
					}).
					Obj(),
			},
			pods: []*corev1.Pod{
				testingpod.MakePod("p1", testNamespace).
					Annotation(kueue.WorkloadAnnotation, testWorkload).
					Label(constants.PodSetLabel, string(testPodSetName)).
					NodeName("node-a").
					StatusConditions(corev1.PodCondition{
						Type:   corev1.PodScheduled,
						Status: corev1.ConditionTrue,
					}).
					Obj(),
				testingpod.MakePod("p2", testNamespace).
					Annotation(kueue.WorkloadAnnotation, testWorkload).
					Label(constants.PodSetLabel, string(testPodSetName)).
					NodeName("node-a").
					StatusConditions(corev1.PodCondition{
						Type:   corev1.PodScheduled,
						Status: corev1.ConditionTrue,
					}).
					StatusPhase(corev1.PodSucceeded).
					Obj(),
			},
			wantWorkloadStatuses: map[string]kueue.WorkloadStatus{
				testWorkload: {
					Admission: &kueue.Admission{
						ClusterQueue: "cq",
						PodSetAssignments: []kueue.PodSetAssignment{
							{
								Name:  testPodSetName,
								Count: new(int32(3)),
							},
						},
					},
					Conditions: []metav1.Condition{
						quotaReservedCondition,
						admittedCondition,
						{
							Type:               kueue.WorkloadPodsScheduled,
							Status:             metav1.ConditionFalse,
							Reason:             kueue.WorkloadWaitForScheduling,
							Message:            unscheduledPodsMessage,
							LastTransitionTime: metav1.NewTime(fakeClock.Now()),
						},
					},
					ReclaimablePods: []kueue.ReclaimablePod{
						{
							Name:  testPodSetName,
							Count: 1,
						},
					},
				},
			},
		},
		"surplus scheduled pods are capped at the granted count": {
			features: map[featuregate.Feature]bool{
				features.WaitForPodsReadyUnscheduledTimeout: true,
			},
			workloads: []*kueue.Workload{
				utiltestingapi.MakeWorkload(testWorkload, testNamespace).
					UID(testWorkloadUID).
					PodSets(*utiltestingapi.MakePodSet(testPodSetName, 2).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").
						PodSets(utiltestingapi.MakePodSetAssignment(testPodSetName).
							Count(2).
							Obj()).
						Obj(), earlier).
					AdmittedAt(true, earlier).
					Obj(),
			},
			pods: []*corev1.Pod{
				testingpod.MakePod("p1", testNamespace).
					Annotation(kueue.WorkloadAnnotation, testWorkload).
					Label(constants.PodSetLabel, string(testPodSetName)).
					NodeName("node-a").
					StatusConditions(corev1.PodCondition{
						Type:   corev1.PodScheduled,
						Status: corev1.ConditionTrue,
					}).
					Obj(),
				testingpod.MakePod("p2", testNamespace).
					Annotation(kueue.WorkloadAnnotation, testWorkload).
					Label(constants.PodSetLabel, string(testPodSetName)).
					NodeName("node-a").
					StatusConditions(corev1.PodCondition{
						Type:   corev1.PodScheduled,
						Status: corev1.ConditionTrue,
					}).
					Obj(),
				testingpod.MakePod("p3", testNamespace).
					Annotation(kueue.WorkloadAnnotation, testWorkload).
					Label(constants.PodSetLabel, string(testPodSetName)).
					NodeName("node-a").
					StatusConditions(corev1.PodCondition{
						Type:   corev1.PodScheduled,
						Status: corev1.ConditionTrue,
					}).
					Obj(),
			},
			wantWorkloadStatuses: map[string]kueue.WorkloadStatus{
				testWorkload: {
					Admission: &kueue.Admission{
						ClusterQueue: "cq",
						PodSetAssignments: []kueue.PodSetAssignment{
							{
								Name:  testPodSetName,
								Count: new(int32(2)),
							},
						},
					},
					Conditions: []metav1.Condition{
						quotaReservedCondition,
						admittedCondition,
						{
							Type:               kueue.WorkloadPodsScheduled,
							Status:             metav1.ConditionTrue,
							Reason:             kueue.WorkloadAllRequiredPodsScheduled,
							Message:            allPodsScheduledMessage,
							LastTransitionTime: metav1.NewTime(fakeClock.Now()),
						},
					},
				},
			},
		},
		"pods of another workload are ignored": {
			features: map[featuregate.Feature]bool{
				features.WaitForPodsReadyUnscheduledTimeout: true,
			},
			workloads: []*kueue.Workload{
				utiltestingapi.MakeWorkload(testWorkload, testNamespace).
					UID(testWorkloadUID).
					PodSets(*utiltestingapi.MakePodSet(testPodSetName, 1).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").
						PodSets(utiltestingapi.MakePodSetAssignment(testPodSetName).
							Count(1).
							Obj()).
						Obj(), earlier).
					AdmittedAt(true, earlier).
					Obj(),
			},
			pods: []*corev1.Pod{
				testingpod.MakePod("other", testNamespace).
					Annotation(kueue.WorkloadAnnotation, "other-wl").
					Label(constants.PodSetLabel, string(testPodSetName)).
					NodeName("node-a").
					StatusConditions(corev1.PodCondition{
						Type:   corev1.PodScheduled,
						Status: corev1.ConditionTrue,
					}).
					Obj(),
				testingpod.MakePod("p1", testNamespace).
					Annotation(kueue.WorkloadAnnotation, testWorkload).
					Label(constants.PodSetLabel, string(testPodSetName)).
					Obj(),
			},
			wantWorkloadStatuses: map[string]kueue.WorkloadStatus{
				testWorkload: {
					Admission: &kueue.Admission{
						ClusterQueue: "cq",
						PodSetAssignments: []kueue.PodSetAssignment{
							{
								Name:  testPodSetName,
								Count: new(int32(1)),
							},
						},
					},
					Conditions: []metav1.Condition{
						quotaReservedCondition,
						admittedCondition,
						{
							Type:               kueue.WorkloadPodsScheduled,
							Status:             metav1.ConditionFalse,
							Reason:             kueue.WorkloadWaitForScheduling,
							Message:            unscheduledPodsMessage,
							LastTransitionTime: metav1.NewTime(fakeClock.Now()),
						},
					},
				},
			},
		},
		"pods of another podset do not satisfy the grant": {
			features: map[featuregate.Feature]bool{
				features.WaitForPodsReadyUnscheduledTimeout: true,
			},
			workloads: []*kueue.Workload{
				utiltestingapi.MakeWorkload(testWorkload, testNamespace).
					UID(testWorkloadUID).
					PodSets(
						*utiltestingapi.MakePodSet("leader", 1).
							Request(corev1.ResourceCPU, "1").
							Obj(),
						*utiltestingapi.MakePodSet("worker", 2).
							Request(corev1.ResourceCPU, "1").
							Obj(),
					).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").
						PodSets(
							utiltestingapi.MakePodSetAssignment("leader").
								Count(1).
								Obj(),
							utiltestingapi.MakePodSetAssignment("worker").
								Count(2).
								Obj(),
						).
						Obj(), earlier).
					AdmittedAt(true, earlier).
					Obj(),
			},
			pods: []*corev1.Pod{
				testingpod.MakePod("l1", testNamespace).
					Annotation(kueue.WorkloadAnnotation, testWorkload).
					Label(constants.PodSetLabel, string("leader")).
					NodeName("node-a").
					StatusConditions(corev1.PodCondition{
						Type:   corev1.PodScheduled,
						Status: corev1.ConditionTrue,
					}).
					Obj(),
				testingpod.MakePod("w1", testNamespace).
					Annotation(kueue.WorkloadAnnotation, testWorkload).
					Label(constants.PodSetLabel, string("worker")).
					NodeName("node-a").
					StatusConditions(corev1.PodCondition{
						Type:   corev1.PodScheduled,
						Status: corev1.ConditionTrue,
					}).
					Obj(),
				testingpod.MakePod("w-extra", testNamespace).
					Annotation(kueue.WorkloadAnnotation, testWorkload).
					Label(constants.PodSetLabel, string("leader")).
					NodeName("node-a").
					StatusConditions(corev1.PodCondition{
						Type:   corev1.PodScheduled,
						Status: corev1.ConditionTrue,
					}).
					Obj(),
			},
			wantWorkloadStatuses: map[string]kueue.WorkloadStatus{
				testWorkload: {
					Admission: &kueue.Admission{
						ClusterQueue: "cq",
						PodSetAssignments: []kueue.PodSetAssignment{
							{
								Name:  "leader",
								Count: new(int32(1)),
							},
							{
								Name:  "worker",
								Count: new(int32(2)),
							},
						},
					},
					Conditions: []metav1.Condition{
						quotaReservedCondition,
						admittedCondition,
						{
							Type:               kueue.WorkloadPodsScheduled,
							Status:             metav1.ConditionFalse,
							Reason:             kueue.WorkloadWaitForScheduling,
							Message:            unscheduledPodsMessage,
							LastTransitionTime: metav1.NewTime(fakeClock.Now()),
						},
					},
				},
			},
		},
		"pods without a podset label do not satisfy the grant": {
			features: map[featuregate.Feature]bool{
				features.WaitForPodsReadyUnscheduledTimeout: true,
			},
			workloads: []*kueue.Workload{
				utiltestingapi.MakeWorkload(testWorkload, testNamespace).
					PodSets(*utiltestingapi.MakePodSet(testPodSetName, 1).
						Obj()).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").
						PodSets(utiltestingapi.MakePodSetAssignment(testPodSetName).
							Count(1).
							Obj()).
						Obj(), earlier).
					AdmittedAt(true, earlier).
					Obj(),
			},
			pods: []*corev1.Pod{
				testingpod.MakePod("p1", testNamespace).
					Annotation(kueue.WorkloadAnnotation, testWorkload).
					NodeName("node-a").
					StatusConditions(corev1.PodCondition{
						Type:   corev1.PodScheduled,
						Status: corev1.ConditionTrue,
					}).
					Obj(),
			},
			wantWorkloadStatuses: map[string]kueue.WorkloadStatus{
				testWorkload: {
					Admission: &kueue.Admission{
						ClusterQueue: "cq",
						PodSetAssignments: []kueue.PodSetAssignment{
							{
								Name:  testPodSetName,
								Count: new(int32(1)),
							},
						},
					},
					Conditions: []metav1.Condition{
						quotaReservedCondition,
						admittedCondition,
						{
							Type:               kueue.WorkloadPodsScheduled,
							Status:             metav1.ConditionFalse,
							Reason:             kueue.WorkloadWaitForScheduling,
							Message:            unscheduledPodsMessage,
							LastTransitionTime: metav1.NewTime(fakeClock.Now()),
						},
					},
				},
			},
		},
		"scheduled and succeeded pods satisfy their respective podset grants": {
			features: map[featuregate.Feature]bool{
				features.WaitForPodsReadyUnscheduledTimeout: true,
			},
			workloads: []*kueue.Workload{
				utiltestingapi.MakeWorkload(testWorkload, testNamespace).
					PodSets(
						*utiltestingapi.MakePodSet("leader", 1).
							Obj(),
						*utiltestingapi.MakePodSet("worker", 1).
							Obj(),
					).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").
						PodSets(
							utiltestingapi.MakePodSetAssignment("leader").
								Count(1).
								Obj(),
							utiltestingapi.MakePodSetAssignment("worker").
								Count(1).
								Obj(),
						).
						Obj(), earlier).
					AdmittedAt(true, earlier).
					Obj(),
			},
			pods: []*corev1.Pod{
				testingpod.MakePod("leader", testNamespace).
					Annotation(kueue.WorkloadAnnotation, testWorkload).
					Label(constants.PodSetLabel, "leader").
					NodeName("node-a").
					StatusConditions(corev1.PodCondition{
						Type:   corev1.PodScheduled,
						Status: corev1.ConditionTrue,
					}).
					Obj(),
				testingpod.MakePod("worker", testNamespace).
					Annotation(kueue.WorkloadAnnotation, testWorkload).
					Label(constants.PodSetLabel, "worker").
					StatusPhase(corev1.PodSucceeded).
					Obj(),
			},
			wantWorkloadStatuses: map[string]kueue.WorkloadStatus{
				testWorkload: {
					Admission: &kueue.Admission{
						ClusterQueue: "cq",
						PodSetAssignments: []kueue.PodSetAssignment{
							{
								Name:  "leader",
								Count: new(int32(1)),
							},
							{
								Name:  "worker",
								Count: new(int32(1)),
							},
						},
					},
					Conditions: []metav1.Condition{
						quotaReservedCondition,
						admittedCondition,
						{
							Type:               kueue.WorkloadPodsScheduled,
							Status:             metav1.ConditionTrue,
							Reason:             kueue.WorkloadAllRequiredPodsScheduled,
							Message:            allPodsScheduledMessage,
							LastTransitionTime: metav1.NewTime(fakeClock.Now()),
						},
					},
				},
			},
		},
		"pods of an unknown podset do not satisfy the grant": {
			features: map[featuregate.Feature]bool{
				features.WaitForPodsReadyUnscheduledTimeout: true,
			},
			workloads: []*kueue.Workload{
				utiltestingapi.MakeWorkload(testWorkload, testNamespace).
					UID(testWorkloadUID).
					PodSets(*utiltestingapi.MakePodSet(testPodSetName, 1).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").
						PodSets(utiltestingapi.MakePodSetAssignment(testPodSetName).
							Count(1).
							Obj()).
						Obj(), earlier).
					AdmittedAt(true, earlier).
					Obj(),
			},
			pods: []*corev1.Pod{
				testingpod.MakePod("p1", testNamespace).
					Annotation(kueue.WorkloadAnnotation, testWorkload).
					Label(constants.PodSetLabel, "other").
					NodeName("node-a").
					StatusConditions(corev1.PodCondition{
						Type:   corev1.PodScheduled,
						Status: corev1.ConditionTrue,
					}).
					Obj(),
			},
			wantWorkloadStatuses: map[string]kueue.WorkloadStatus{
				testWorkload: {
					Admission: &kueue.Admission{
						ClusterQueue: "cq",
						PodSetAssignments: []kueue.PodSetAssignment{
							{
								Name:  testPodSetName,
								Count: new(int32(1)),
							},
						},
					},
					Conditions: []metav1.Condition{
						quotaReservedCondition,
						admittedCondition,
						{
							Type:               kueue.WorkloadPodsScheduled,
							Status:             metav1.ConditionFalse,
							Reason:             kueue.WorkloadWaitForScheduling,
							Message:            unscheduledPodsMessage,
							LastTransitionTime: metav1.NewTime(fakeClock.Now()),
						},
					},
				},
			},
		},
		"all podsets scheduled": {
			features: map[featuregate.Feature]bool{
				features.WaitForPodsReadyUnscheduledTimeout: true,
			},
			workloads: []*kueue.Workload{
				utiltestingapi.MakeWorkload(testWorkload, testNamespace).
					UID(testWorkloadUID).
					PodSets(
						*utiltestingapi.MakePodSet("leader", 1).
							Request(corev1.ResourceCPU, "1").
							Obj(),
						*utiltestingapi.MakePodSet("worker", 2).
							Request(corev1.ResourceCPU, "1").
							Obj(),
					).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").
						PodSets(
							utiltestingapi.MakePodSetAssignment("leader").
								Count(1).
								Obj(),
							utiltestingapi.MakePodSetAssignment("worker").
								Count(2).
								Obj(),
						).
						Obj(), earlier).
					AdmittedAt(true, earlier).
					Obj(),
			},
			pods: []*corev1.Pod{
				testingpod.MakePod("l1", testNamespace).
					Annotation(kueue.WorkloadAnnotation, testWorkload).
					Label(constants.PodSetLabel, string("leader")).
					NodeName("node-a").
					StatusConditions(corev1.PodCondition{
						Type:   corev1.PodScheduled,
						Status: corev1.ConditionTrue,
					}).
					Obj(),
				testingpod.MakePod("w1", testNamespace).
					Annotation(kueue.WorkloadAnnotation, testWorkload).
					Label(constants.PodSetLabel, string("worker")).
					NodeName("node-a").
					StatusConditions(corev1.PodCondition{
						Type:   corev1.PodScheduled,
						Status: corev1.ConditionTrue,
					}).
					Obj(),
				testingpod.MakePod("w2", testNamespace).
					Annotation(kueue.WorkloadAnnotation, testWorkload).
					Label(constants.PodSetLabel, string("worker")).
					NodeName("node-a").
					StatusConditions(corev1.PodCondition{
						Type:   corev1.PodScheduled,
						Status: corev1.ConditionTrue,
					}).
					Obj(),
			},
			wantWorkloadStatuses: map[string]kueue.WorkloadStatus{
				testWorkload: {
					Admission: &kueue.Admission{
						ClusterQueue: "cq",
						PodSetAssignments: []kueue.PodSetAssignment{
							{
								Name:  "leader",
								Count: new(int32(1)),
							},
							{
								Name:  "worker",
								Count: new(int32(2)),
							},
						},
					},
					Conditions: []metav1.Condition{
						quotaReservedCondition,
						admittedCondition,
						{
							Type:               kueue.WorkloadPodsScheduled,
							Status:             metav1.ConditionTrue,
							Reason:             kueue.WorkloadAllRequiredPodsScheduled,
							Message:            allPodsScheduledMessage,
							LastTransitionTime: metav1.NewTime(fakeClock.Now()),
						},
					},
				},
			},
		},
		"admitted condition without status admission is not observed": {
			features: map[featuregate.Feature]bool{
				features.WaitForPodsReadyUnscheduledTimeout: true,
			},
			workloads: []*kueue.Workload{
				utiltestingapi.MakeWorkload(testWorkload, testNamespace).
					PodSets(*utiltestingapi.MakePodSet(testPodSetName, 1).
						Obj()).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadAdmitted,
						Status:             metav1.ConditionTrue,
						Reason:             "Admitted",
						LastTransitionTime: metav1.NewTime(earlier),
					}).
					Obj(),
			},
			pods: []*corev1.Pod{
				testingpod.MakePod("p1", testNamespace).
					Annotation(kueue.WorkloadAnnotation, testWorkload).
					Label(constants.PodSetLabel, string(testPodSetName)).
					NodeName("node-a").
					StatusConditions(corev1.PodCondition{
						Type:   corev1.PodScheduled,
						Status: corev1.ConditionTrue,
					}).
					Obj(),
			},
			wantWorkloadStatuses: map[string]kueue.WorkloadStatus{
				testWorkload: {
					Conditions: []metav1.Condition{
						{
							Type:               kueue.WorkloadAdmitted,
							Status:             metav1.ConditionTrue,
							Reason:             "Admitted",
							LastTransitionTime: metav1.NewTime(earlier),
						},
					},
				},
			},
		},
		"scale-down before the first observation still requires the admission count": {
			features: map[featuregate.Feature]bool{
				features.WaitForPodsReadyUnscheduledTimeout: true,
			},
			workloads: []*kueue.Workload{
				utiltestingapi.MakeWorkload(testWorkload, testNamespace).
					PodSets(*utiltestingapi.MakePodSet(testPodSetName, 1).
						Obj()).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").
						PodSets(utiltestingapi.MakePodSetAssignment(testPodSetName).
							Count(2).
							Obj()).
						Obj(), earlier).
					AdmittedAt(true, earlier).
					Obj(),
			},
			pods: []*corev1.Pod{
				testingpod.MakePod("p1", testNamespace).
					Annotation(kueue.WorkloadAnnotation, testWorkload).
					Label(constants.PodSetLabel, string(testPodSetName)).
					NodeName("node-a").
					StatusConditions(corev1.PodCondition{
						Type:   corev1.PodScheduled,
						Status: corev1.ConditionTrue,
					}).
					Obj(),
			},
			wantWorkloadStatuses: map[string]kueue.WorkloadStatus{
				testWorkload: {
					Admission: &kueue.Admission{
						ClusterQueue: "cq",
						PodSetAssignments: []kueue.PodSetAssignment{
							{
								Name:  testPodSetName,
								Count: new(int32(2)),
							},
						},
					},
					Conditions: []metav1.Condition{
						quotaReservedCondition,
						admittedCondition,
						{
							Type:               kueue.WorkloadPodsScheduled,
							Status:             metav1.ConditionFalse,
							Reason:             kueue.WorkloadWaitForScheduling,
							Message:            unscheduledPodsMessage,
							LastTransitionTime: metav1.NewTime(fakeClock.Now()),
						},
					},
				},
			},
		},
		"scale-down while unscheduled preserves the admission count and observation timestamp": {
			features: map[featuregate.Feature]bool{
				features.WaitForPodsReadyUnscheduledTimeout: true,
				features.ElasticJobsViaWorkloadSlices:       true,
			},
			workloads: []*kueue.Workload{
				utiltestingapi.MakeWorkload(testWorkload, testNamespace).
					Generation(2).
					Annotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
					PodSets(*utiltestingapi.MakePodSet(testPodSetName, 1).
						Obj()).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").
						PodSets(utiltestingapi.MakePodSetAssignment(testPodSetName).
							Count(2).
							Obj()).
						Obj(), earlier).
					AdmittedAt(true, earlier).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPodsScheduled,
						Status:             metav1.ConditionFalse,
						Reason:             kueue.WorkloadWaitForScheduling,
						Message:            unscheduledPodsMessage,
						LastTransitionTime: metav1.NewTime(fakeClock.Now().Add(-30 * time.Second)),
						ObservedGeneration: 1,
					}).
					Obj(),
			},
			pods: []*corev1.Pod{
				testingpod.MakePod("p1", testNamespace).
					Annotation(kueue.WorkloadAnnotation, testWorkload).
					Label(constants.PodSetLabel, string(testPodSetName)).
					NodeName("node-a").
					StatusConditions(corev1.PodCondition{
						Type:   corev1.PodScheduled,
						Status: corev1.ConditionTrue,
					}).
					Obj(),
			},
			wantWorkloadStatuses: map[string]kueue.WorkloadStatus{
				testWorkload: {
					Admission: &kueue.Admission{
						ClusterQueue: "cq",
						PodSetAssignments: []kueue.PodSetAssignment{
							{
								Name:  testPodSetName,
								Count: new(int32(2)),
							},
						},
					},
					Conditions: []metav1.Condition{
						quotaReservedCondition,
						admittedCondition,
						{
							Type:               kueue.WorkloadPodsScheduled,
							Status:             metav1.ConditionFalse,
							Reason:             kueue.WorkloadWaitForScheduling,
							Message:            unscheduledPodsMessage,
							LastTransitionTime: metav1.NewTime(fakeClock.Now().Add(-30 * time.Second)),
							ObservedGeneration: 1,
						},
					},
				},
			},
		},
		"scale-down does not let retained succeeded pods below the admission count open observation": {
			features: map[featuregate.Feature]bool{
				features.WaitForPodsReadyUnscheduledTimeout: true,
			},
			workloads: []*kueue.Workload{
				utiltestingapi.MakeWorkload(testWorkload, testNamespace).
					PodSets(*utiltestingapi.MakePodSet(testPodSetName, 1).
						Obj()).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").
						PodSets(utiltestingapi.MakePodSetAssignment(testPodSetName).
							Count(2).
							Obj()).
						Obj(), earlier).
					AdmittedAt(true, earlier).
					Obj(),
			},
			pods: []*corev1.Pod{
				testingpod.MakePod("p1", testNamespace).
					Annotation(kueue.WorkloadAnnotation, testWorkload).
					Label(constants.PodSetLabel, string(testPodSetName)).
					StatusPhase(corev1.PodSucceeded).
					Obj(),
			},
			wantWorkloadStatuses: map[string]kueue.WorkloadStatus{
				testWorkload: {
					Admission: &kueue.Admission{
						ClusterQueue: "cq",
						PodSetAssignments: []kueue.PodSetAssignment{
							{
								Name:  testPodSetName,
								Count: new(int32(2)),
							},
						},
					},
					Conditions: []metav1.Condition{
						quotaReservedCondition,
						admittedCondition,
					},
				},
			},
		},
		"missing admission count falls back to the spec count": {
			features: map[featuregate.Feature]bool{
				features.WaitForPodsReadyUnscheduledTimeout: true,
			},
			workloads: []*kueue.Workload{
				utiltestingapi.MakeWorkload(testWorkload, testNamespace).
					PodSets(*utiltestingapi.MakePodSet(testPodSetName, 2).
						Obj()).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").
						PodSets(kueue.PodSetAssignment{
							Name: testPodSetName,
						}).
						Obj(), earlier).
					AdmittedAt(true, earlier).
					Obj(),
			},
			pods: []*corev1.Pod{
				testingpod.MakePod("p1", testNamespace).
					Annotation(kueue.WorkloadAnnotation, testWorkload).
					Label(constants.PodSetLabel, string(testPodSetName)).
					NodeName("node-a").
					StatusConditions(corev1.PodCondition{
						Type:   corev1.PodScheduled,
						Status: corev1.ConditionTrue,
					}).
					Obj(),
			},
			wantWorkloadStatuses: map[string]kueue.WorkloadStatus{
				testWorkload: {
					Admission: &kueue.Admission{
						ClusterQueue: "cq",
						PodSetAssignments: []kueue.PodSetAssignment{
							{
								Name: testPodSetName,
							},
						},
					},
					Conditions: []metav1.Condition{
						quotaReservedCondition,
						admittedCondition,
						{
							Type:               kueue.WorkloadPodsScheduled,
							Status:             metav1.ConditionFalse,
							Reason:             kueue.WorkloadWaitForScheduling,
							Message:            unscheduledPodsMessage,
							LastTransitionTime: metav1.NewTime(fakeClock.Now()),
						},
					},
				},
			},
		},
		"partial admission requires only the granted count": {
			features: map[featuregate.Feature]bool{
				features.WaitForPodsReadyUnscheduledTimeout: true,
			},
			workloads: []*kueue.Workload{
				utiltestingapi.MakeWorkload(testWorkload, testNamespace).
					PodSets(*utiltestingapi.MakePodSet(testPodSetName, 3).
						SetMinimumCount(1).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").
						PodSets(utiltestingapi.MakePodSetAssignment(testPodSetName).
							Count(2).
							Obj()).
						Obj(), earlier).
					AdmittedAt(true, earlier).
					Obj(),
			},
			pods: []*corev1.Pod{
				testingpod.MakePod("p1", testNamespace).
					Annotation(kueue.WorkloadAnnotation, testWorkload).
					Label(constants.PodSetLabel, string(testPodSetName)).
					NodeName("node-a").
					StatusConditions(corev1.PodCondition{
						Type:   corev1.PodScheduled,
						Status: corev1.ConditionTrue,
					}).
					Obj(),
				testingpod.MakePod("p2", testNamespace).
					Annotation(kueue.WorkloadAnnotation, testWorkload).
					Label(constants.PodSetLabel, string(testPodSetName)).
					NodeName("node-a").
					StatusConditions(corev1.PodCondition{
						Type:   corev1.PodScheduled,
						Status: corev1.ConditionTrue,
					}).
					Obj(),
			},
			wantWorkloadStatuses: map[string]kueue.WorkloadStatus{
				testWorkload: {
					Admission: &kueue.Admission{
						ClusterQueue: "cq",
						PodSetAssignments: []kueue.PodSetAssignment{
							{
								Name:  testPodSetName,
								Count: new(int32(2)),
							},
						},
					},
					Conditions: []metav1.Condition{
						quotaReservedCondition,
						admittedCondition,
						{
							Type:               kueue.WorkloadPodsScheduled,
							Status:             metav1.ConditionTrue,
							Reason:             kueue.WorkloadAllRequiredPodsScheduled,
							Message:            allPodsScheduledMessage,
							LastTransitionTime: metav1.NewTime(fakeClock.Now()),
						},
					},
				},
			},
		},
		"current observation of unscheduled pods stands while pods are still unscheduled": {
			features: map[featuregate.Feature]bool{
				features.WaitForPodsReadyUnscheduledTimeout: true,
			},
			workloads: []*kueue.Workload{
				utiltestingapi.MakeWorkload(testWorkload, testNamespace).
					UID(testWorkloadUID).
					PodSets(*utiltestingapi.MakePodSet(testPodSetName, 2).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").
						PodSets(utiltestingapi.MakePodSetAssignment(testPodSetName).
							Count(2).
							Obj()).
						Obj(), earlier).
					AdmittedAt(true, earlier).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPodsScheduled,
						Status:             metav1.ConditionFalse,
						Reason:             kueue.WorkloadWaitForScheduling,
						Message:            unscheduledPodsMessage,
						LastTransitionTime: metav1.NewTime(fakeClock.Now().Add(-30 * time.Second)),
					}).
					Obj(),
			},
			pods: []*corev1.Pod{
				testingpod.MakePod("p1", testNamespace).
					Annotation(kueue.WorkloadAnnotation, testWorkload).
					Label(constants.PodSetLabel, string(testPodSetName)).
					NodeName("node-a").
					StatusConditions(corev1.PodCondition{
						Type:   corev1.PodScheduled,
						Status: corev1.ConditionTrue,
					}).
					Obj(),
				testingpod.MakePod("p2", testNamespace).
					Annotation(kueue.WorkloadAnnotation, testWorkload).
					Label(constants.PodSetLabel, string(testPodSetName)).
					Obj(),
			},
			wantWorkloadStatuses: map[string]kueue.WorkloadStatus{
				testWorkload: {
					Admission: &kueue.Admission{
						ClusterQueue: "cq",
						PodSetAssignments: []kueue.PodSetAssignment{
							{
								Name:  testPodSetName,
								Count: new(int32(2)),
							},
						},
					},
					Conditions: []metav1.Condition{
						quotaReservedCondition,
						admittedCondition,
						{
							Type:               kueue.WorkloadPodsScheduled,
							Status:             metav1.ConditionFalse,
							Reason:             kueue.WorkloadWaitForScheduling,
							Message:            unscheduledPodsMessage,
							LastTransitionTime: metav1.NewTime(fakeClock.Now().Add(-30 * time.Second)),
						},
					},
				},
			},
		},
		"progress without completion keeps the observation": {
			features: map[featuregate.Feature]bool{
				features.WaitForPodsReadyUnscheduledTimeout: true,
			},
			workloads: []*kueue.Workload{
				utiltestingapi.MakeWorkload(testWorkload, testNamespace).
					UID(testWorkloadUID).
					PodSets(*utiltestingapi.MakePodSet(testPodSetName, 3).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").
						PodSets(utiltestingapi.MakePodSetAssignment(testPodSetName).
							Count(3).
							Obj()).
						Obj(), earlier).
					AdmittedAt(true, earlier).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPodsScheduled,
						Status:             metav1.ConditionFalse,
						Reason:             kueue.WorkloadWaitForScheduling,
						Message:            unscheduledPodsMessage,
						LastTransitionTime: metav1.NewTime(fakeClock.Now().Add(-30 * time.Second)),
					}).
					Obj(),
			},
			pods: []*corev1.Pod{
				testingpod.MakePod("p1", testNamespace).
					Annotation(kueue.WorkloadAnnotation, testWorkload).
					Label(constants.PodSetLabel, string(testPodSetName)).
					NodeName("node-a").
					StatusConditions(corev1.PodCondition{
						Type:   corev1.PodScheduled,
						Status: corev1.ConditionTrue,
					}).
					Obj(),
				testingpod.MakePod("p2", testNamespace).
					Annotation(kueue.WorkloadAnnotation, testWorkload).
					Label(constants.PodSetLabel, string(testPodSetName)).
					NodeName("node-a").
					StatusConditions(corev1.PodCondition{
						Type:   corev1.PodScheduled,
						Status: corev1.ConditionTrue,
					}).
					Obj(),
				testingpod.MakePod("p3", testNamespace).
					Annotation(kueue.WorkloadAnnotation, testWorkload).
					Label(constants.PodSetLabel, string(testPodSetName)).
					Obj(),
			},
			wantWorkloadStatuses: map[string]kueue.WorkloadStatus{
				testWorkload: {
					Admission: &kueue.Admission{
						ClusterQueue: "cq",
						PodSetAssignments: []kueue.PodSetAssignment{
							{
								Name:  testPodSetName,
								Count: new(int32(3)),
							},
						},
					},
					Conditions: []metav1.Condition{
						quotaReservedCondition,
						admittedCondition,
						{
							Type:               kueue.WorkloadPodsScheduled,
							Status:             metav1.ConditionFalse,
							Reason:             kueue.WorkloadWaitForScheduling,
							Message:            unscheduledPodsMessage,
							LastTransitionTime: metav1.NewTime(fakeClock.Now().Add(-30 * time.Second)),
						},
					},
				},
			},
		},
		"transition to all scheduled stamps the transition time": {
			features: map[featuregate.Feature]bool{
				features.WaitForPodsReadyUnscheduledTimeout: true,
			},
			workloads: []*kueue.Workload{
				utiltestingapi.MakeWorkload(testWorkload, testNamespace).
					UID(testWorkloadUID).
					PodSets(*utiltestingapi.MakePodSet(testPodSetName, 2).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").
						PodSets(utiltestingapi.MakePodSetAssignment(testPodSetName).
							Count(2).
							Obj()).
						Obj(), earlier).
					AdmittedAt(true, earlier).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPodsScheduled,
						Status:             metav1.ConditionFalse,
						Reason:             kueue.WorkloadWaitForScheduling,
						Message:            unscheduledPodsMessage,
						LastTransitionTime: metav1.NewTime(fakeClock.Now().Add(-30 * time.Second)),
					}).
					Obj(),
			},
			pods: []*corev1.Pod{
				testingpod.MakePod("p1", testNamespace).
					Annotation(kueue.WorkloadAnnotation, testWorkload).
					Label(constants.PodSetLabel, string(testPodSetName)).
					NodeName("node-a").
					StatusConditions(corev1.PodCondition{
						Type:   corev1.PodScheduled,
						Status: corev1.ConditionTrue,
					}).
					Obj(),
				testingpod.MakePod("p2", testNamespace).
					Annotation(kueue.WorkloadAnnotation, testWorkload).
					Label(constants.PodSetLabel, string(testPodSetName)).
					NodeName("node-a").
					StatusConditions(corev1.PodCondition{
						Type:   corev1.PodScheduled,
						Status: corev1.ConditionTrue,
					}).
					Obj(),
			},
			wantWorkloadStatuses: map[string]kueue.WorkloadStatus{
				testWorkload: {
					Admission: &kueue.Admission{
						ClusterQueue: "cq",
						PodSetAssignments: []kueue.PodSetAssignment{
							{
								Name:  testPodSetName,
								Count: new(int32(2)),
							},
						},
					},
					Conditions: []metav1.Condition{
						quotaReservedCondition,
						admittedCondition,
						{
							Type:               kueue.WorkloadPodsScheduled,
							Status:             metav1.ConditionTrue,
							Reason:             kueue.WorkloadAllRequiredPodsScheduled,
							Message:            allPodsScheduledMessage,
							LastTransitionTime: metav1.NewTime(fakeClock.Now()),
						},
					},
				},
			},
		},
		"observation of all pods scheduled stands when a scheduled pod is deleted": {
			features: map[featuregate.Feature]bool{
				features.WaitForPodsReadyUnscheduledTimeout: true,
			},
			workloads: []*kueue.Workload{
				utiltestingapi.MakeWorkload(testWorkload, testNamespace).
					UID(testWorkloadUID).
					PodSets(*utiltestingapi.MakePodSet(testPodSetName, 2).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").
						PodSets(utiltestingapi.MakePodSetAssignment(testPodSetName).
							Count(2).
							Obj()).
						Obj(), earlier).
					AdmittedAt(true, earlier).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPodsScheduled,
						Status:             metav1.ConditionTrue,
						Reason:             kueue.WorkloadAllRequiredPodsScheduled,
						Message:            allPodsScheduledMessage,
						LastTransitionTime: metav1.NewTime(fakeClock.Now().Add(-30 * time.Second)),
					}).
					Obj(),
			},
			pods: []*corev1.Pod{
				testingpod.MakePod("p1", testNamespace).
					Annotation(kueue.WorkloadAnnotation, testWorkload).
					Label(constants.PodSetLabel, string(testPodSetName)).
					NodeName("node-a").
					StatusConditions(corev1.PodCondition{
						Type:   corev1.PodScheduled,
						Status: corev1.ConditionTrue,
					}).
					Obj(),
			},
			wantWorkloadStatuses: map[string]kueue.WorkloadStatus{
				testWorkload: {
					Admission: &kueue.Admission{
						ClusterQueue: "cq",
						PodSetAssignments: []kueue.PodSetAssignment{
							{
								Name:  testPodSetName,
								Count: new(int32(2)),
							},
						},
					},
					Conditions: []metav1.Condition{
						quotaReservedCondition,
						admittedCondition,
						{
							Type:               kueue.WorkloadPodsScheduled,
							Status:             metav1.ConditionTrue,
							Reason:             kueue.WorkloadAllRequiredPodsScheduled,
							Message:            allPodsScheduledMessage,
							LastTransitionTime: metav1.NewTime(fakeClock.Now().Add(-30 * time.Second)),
						},
					},
				},
			},
		},
		"elastic scale-down preserves the scheduled observation even with a pending replacement": {
			features: map[featuregate.Feature]bool{
				features.WaitForPodsReadyUnscheduledTimeout: true,
				features.ElasticJobsViaWorkloadSlices:       true,
			},
			workloads: []*kueue.Workload{
				utiltestingapi.MakeWorkload(testWorkload, testNamespace).
					UID(testWorkloadUID).
					Generation(2).
					Annotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
					PodSets(*utiltestingapi.MakePodSet(testPodSetName, 1).
						Obj()).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").
						PodSets(utiltestingapi.MakePodSetAssignment(testPodSetName).
							Count(2).
							Obj()).
						Obj(), earlier).
					AdmittedAt(true, earlier).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPodsScheduled,
						Status:             metav1.ConditionTrue,
						Reason:             kueue.WorkloadAllRequiredPodsScheduled,
						Message:            allPodsScheduledMessage,
						LastTransitionTime: metav1.NewTime(fakeClock.Now().Add(-30 * time.Second)),
						ObservedGeneration: 1,
					}).
					Obj(),
			},
			pods: []*corev1.Pod{
				testingpod.MakePod("replacement", testNamespace).
					Annotation(kueue.WorkloadAnnotation, testWorkload).
					Label(constants.PodSetLabel, string(testPodSetName)).
					Obj(),
			},
			wantWorkloadStatuses: map[string]kueue.WorkloadStatus{
				testWorkload: {
					Admission: &kueue.Admission{
						ClusterQueue: "cq",
						PodSetAssignments: []kueue.PodSetAssignment{
							{
								Name:  testPodSetName,
								Count: new(int32(2)),
							},
						},
					},
					Conditions: []metav1.Condition{
						quotaReservedCondition,
						admittedCondition,
						{
							Type:               kueue.WorkloadPodsScheduled,
							Status:             metav1.ConditionTrue,
							Reason:             kueue.WorkloadAllRequiredPodsScheduled,
							Message:            allPodsScheduledMessage,
							LastTransitionTime: metav1.NewTime(fakeClock.Now().Add(-30 * time.Second)),
							ObservedGeneration: 1,
						},
					},
				},
			},
		},
		"pending replacement in the same admission does not reset the scheduled observation": {
			features: map[featuregate.Feature]bool{
				features.WaitForPodsReadyUnscheduledTimeout: true,
			},
			workloads: []*kueue.Workload{
				utiltestingapi.MakeWorkload(testWorkload, testNamespace).
					UID(testWorkloadUID).
					Generation(2).
					PodSets(*utiltestingapi.MakePodSet(testPodSetName, 2).
						Obj()).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").
						PodSets(utiltestingapi.MakePodSetAssignment(testPodSetName).
							Count(2).
							Obj()).
						Obj(), earlier).
					AdmittedAt(true, earlier).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPodsScheduled,
						Status:             metav1.ConditionTrue,
						Reason:             kueue.WorkloadAllRequiredPodsScheduled,
						Message:            allPodsScheduledMessage,
						LastTransitionTime: metav1.NewTime(fakeClock.Now().Add(-30 * time.Second)),
						ObservedGeneration: 1,
					}).
					Obj(),
			},
			pods: []*corev1.Pod{
				testingpod.MakePod("replacement", testNamespace).
					Annotation(kueue.WorkloadAnnotation, testWorkload).
					Label(constants.PodSetLabel, string(testPodSetName)).
					Obj(),
			},
			wantWorkloadStatuses: map[string]kueue.WorkloadStatus{
				testWorkload: {
					Admission: &kueue.Admission{
						ClusterQueue: "cq",
						PodSetAssignments: []kueue.PodSetAssignment{
							{
								Name:  testPodSetName,
								Count: new(int32(2)),
							},
						},
					},
					Conditions: []metav1.Condition{
						quotaReservedCondition,
						admittedCondition,
						{
							Type:               kueue.WorkloadPodsScheduled,
							Status:             metav1.ConditionTrue,
							Reason:             kueue.WorkloadAllRequiredPodsScheduled,
							Message:            allPodsScheduledMessage,
							LastTransitionTime: metav1.NewTime(fakeClock.Now().Add(-30 * time.Second)),
							ObservedGeneration: 1,
						},
					},
				},
			},
		},
		"generation change alone is not written": {
			features: map[featuregate.Feature]bool{
				features.WaitForPodsReadyUnscheduledTimeout: true,
			},
			workloads: []*kueue.Workload{
				utiltestingapi.MakeWorkload(testWorkload, testNamespace).
					UID(testWorkloadUID).
					PodSets(*utiltestingapi.MakePodSet(testPodSetName, 2).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").
						PodSets(utiltestingapi.MakePodSetAssignment(testPodSetName).
							Count(2).
							Obj()).
						Obj(), earlier).
					AdmittedAt(true, earlier).
					Generation(2).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPodsScheduled,
						Status:             metav1.ConditionTrue,
						Reason:             kueue.WorkloadAllRequiredPodsScheduled,
						Message:            allPodsScheduledMessage,
						LastTransitionTime: metav1.NewTime(fakeClock.Now().Add(-30 * time.Second)),
					}).
					Obj(),
			},
			pods: []*corev1.Pod{
				testingpod.MakePod("p1", testNamespace).
					Annotation(kueue.WorkloadAnnotation, testWorkload).
					Label(constants.PodSetLabel, string(testPodSetName)).
					NodeName("node-a").
					StatusConditions(corev1.PodCondition{
						Type:   corev1.PodScheduled,
						Status: corev1.ConditionTrue,
					}).
					Obj(),
				testingpod.MakePod("p2", testNamespace).
					Annotation(kueue.WorkloadAnnotation, testWorkload).
					Label(constants.PodSetLabel, string(testPodSetName)).
					NodeName("node-a").
					StatusConditions(corev1.PodCondition{
						Type:   corev1.PodScheduled,
						Status: corev1.ConditionTrue,
					}).
					Obj(),
			},
			wantWorkloadStatuses: map[string]kueue.WorkloadStatus{
				testWorkload: {
					Admission: &kueue.Admission{
						ClusterQueue: "cq",
						PodSetAssignments: []kueue.PodSetAssignment{
							{
								Name:  testPodSetName,
								Count: new(int32(2)),
							},
						},
					},
					Conditions: []metav1.Condition{
						quotaReservedCondition,
						admittedCondition,
						{
							Type:               kueue.WorkloadPodsScheduled,
							Status:             metav1.ConditionTrue,
							Reason:             kueue.WorkloadAllRequiredPodsScheduled,
							Message:            allPodsScheduledMessage,
							LastTransitionTime: metav1.NewTime(fakeClock.Now().Add(-30 * time.Second)),
						},
					},
				},
			},
		},
		"observation records the generation": {
			features: map[featuregate.Feature]bool{
				features.WaitForPodsReadyUnscheduledTimeout: true,
			},
			workloads: []*kueue.Workload{
				utiltestingapi.MakeWorkload(testWorkload, testNamespace).
					UID(testWorkloadUID).
					PodSets(*utiltestingapi.MakePodSet(testPodSetName, 1).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").
						PodSets(utiltestingapi.MakePodSetAssignment(testPodSetName).
							Count(1).
							Obj()).
						Obj(), earlier).
					AdmittedAt(true, earlier).
					Generation(2).
					Obj(),
			},
			pods: []*corev1.Pod{
				testingpod.MakePod("p1", testNamespace).
					Annotation(kueue.WorkloadAnnotation, testWorkload).
					Label(constants.PodSetLabel, string(testPodSetName)).
					NodeName("node-a").
					StatusConditions(corev1.PodCondition{
						Type:   corev1.PodScheduled,
						Status: corev1.ConditionTrue,
					}).
					Obj(),
			},
			wantWorkloadStatuses: map[string]kueue.WorkloadStatus{
				testWorkload: {
					Admission: &kueue.Admission{
						ClusterQueue: "cq",
						PodSetAssignments: []kueue.PodSetAssignment{
							{
								Name:  testPodSetName,
								Count: new(int32(1)),
							},
						},
					},
					Conditions: []metav1.Condition{
						quotaReservedCondition,
						admittedCondition,
						{
							Type:               kueue.WorkloadPodsScheduled,
							Status:             metav1.ConditionTrue,
							Reason:             kueue.WorkloadAllRequiredPodsScheduled,
							Message:            allPodsScheduledMessage,
							LastTransitionTime: metav1.NewTime(fakeClock.Now()),
							ObservedGeneration: 2,
						},
					},
				},
			},
		},
		"pod list failure keeps the previous condition and returns the error": {
			features: map[featuregate.Feature]bool{
				features.WaitForPodsReadyUnscheduledTimeout: true,
			},
			workloads: []*kueue.Workload{
				utiltestingapi.MakeWorkload(testWorkload, testNamespace).
					UID(testWorkloadUID).
					PodSets(*utiltestingapi.MakePodSet(testPodSetName, 2).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").
						PodSets(utiltestingapi.MakePodSetAssignment(testPodSetName).
							Count(2).
							Obj()).
						Obj(), earlier).
					AdmittedAt(true, earlier).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPodsScheduled,
						Status:             metav1.ConditionFalse,
						Reason:             kueue.WorkloadWaitForScheduling,
						Message:            unscheduledPodsMessage,
						LastTransitionTime: metav1.NewTime(fakeClock.Now().Add(-30 * time.Second)),
					}).
					Obj(),
			},
			wantErr: errListPods,
			wantWorkloadStatuses: map[string]kueue.WorkloadStatus{
				testWorkload: {
					Admission: &kueue.Admission{
						ClusterQueue: "cq",
						PodSetAssignments: []kueue.PodSetAssignment{
							{
								Name:  testPodSetName,
								Count: new(int32(2)),
							},
						},
					},
					Conditions: []metav1.Condition{
						quotaReservedCondition,
						admittedCondition,
						{
							Type:               kueue.WorkloadPodsScheduled,
							Status:             metav1.ConditionFalse,
							Reason:             kueue.WorkloadWaitForScheduling,
							Message:            unscheduledPodsMessage,
							LastTransitionTime: metav1.NewTime(fakeClock.Now().Add(-30 * time.Second)),
						},
					},
				},
			},
		},
		"pod list failure with a condition of a previous admission keeps it and returns the error": {
			features: map[featuregate.Feature]bool{
				features.WaitForPodsReadyUnscheduledTimeout: true,
			},
			workloads: []*kueue.Workload{
				utiltestingapi.MakeWorkload(testWorkload, testNamespace).
					UID(testWorkloadUID).
					PodSets(*utiltestingapi.MakePodSet(testPodSetName, 2).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").
						PodSets(utiltestingapi.MakePodSetAssignment(testPodSetName).
							Count(2).
							Obj()).
						Obj(), earlier).
					AdmittedAt(true, earlier).
					Condition(metav1.Condition{
						Type:               kueue.WorkloadPodsScheduled,
						Status:             metav1.ConditionTrue,
						Reason:             kueue.WorkloadAllRequiredPodsScheduled,
						Message:            allPodsScheduledMessage,
						LastTransitionTime: metav1.NewTime(muchEarlier),
					}).
					Obj(),
			},
			wantErr: errListPods,
			wantWorkloadStatuses: map[string]kueue.WorkloadStatus{
				testWorkload: {
					Admission: &kueue.Admission{
						ClusterQueue: "cq",
						PodSetAssignments: []kueue.PodSetAssignment{
							{
								Name:  testPodSetName,
								Count: new(int32(2)),
							},
						},
					},
					Conditions: []metav1.Condition{
						quotaReservedCondition,
						admittedCondition,
						{
							Type:               kueue.WorkloadPodsScheduled,
							Status:             metav1.ConditionTrue,
							Reason:             kueue.WorkloadAllRequiredPodsScheduled,
							Message:            allPodsScheduledMessage,
							LastTransitionTime: metav1.NewTime(muchEarlier),
						},
					},
				},
			},
		},
		"status patch conflict is returned for retry": {
			features: map[featuregate.Feature]bool{
				features.WaitForPodsReadyUnscheduledTimeout: true,
			},
			workloads: []*kueue.Workload{
				utiltestingapi.MakeWorkload(testWorkload, testNamespace).
					UID(testWorkloadUID).
					PodSets(*utiltestingapi.MakePodSet(testPodSetName, 1).
						Obj()).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").
						PodSets(utiltestingapi.MakePodSetAssignment(testPodSetName).
							Count(1).
							Obj()).
						Obj(), earlier).
					AdmittedAt(true, earlier).
					Obj(),
			},
			pods: []*corev1.Pod{
				testingpod.MakePod("p1", testNamespace).
					Annotation(kueue.WorkloadAnnotation, testWorkload).
					Label(constants.PodSetLabel, string(testPodSetName)).
					NodeName("node-a").
					StatusConditions(corev1.PodCondition{
						Type:   corev1.PodScheduled,
						Status: corev1.ConditionTrue,
					}).
					Obj(),
			},
			wantErr: errConflict,
			wantWorkloadStatuses: map[string]kueue.WorkloadStatus{
				testWorkload: {
					Admission: &kueue.Admission{
						ClusterQueue: "cq",
						PodSetAssignments: []kueue.PodSetAssignment{
							{
								Name:  testPodSetName,
								Count: new(int32(1)),
							},
						},
					},
					Conditions: []metav1.Condition{
						quotaReservedCondition,
						admittedCondition,
					},
				},
			},
		},
		"missing workload is ignored": {
			features: map[featuregate.Feature]bool{
				features.WaitForPodsReadyUnscheduledTimeout: true,
			},
			request: &types.NamespacedName{
				Namespace: testNamespace,
				Name:      "missing",
			},
			workloads: []*kueue.Workload{
				utiltestingapi.MakeWorkload(testWorkload, testNamespace).
					UID(testWorkloadUID).
					PodSets(*utiltestingapi.MakePodSet(testPodSetName, 1).
						Request(corev1.ResourceCPU, "1").
						Obj()).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").
						PodSets(utiltestingapi.MakePodSetAssignment(testPodSetName).
							Count(1).
							Obj()).
						Obj(), earlier).
					AdmittedAt(true, earlier).
					Obj(),
			},
			wantWorkloadStatuses: map[string]kueue.WorkloadStatus{
				testWorkload: {
					Admission: &kueue.Admission{
						ClusterQueue: "cq",
						PodSetAssignments: []kueue.PodSetAssignment{
							{
								Name:  testPodSetName,
								Count: new(int32(1)),
							},
						},
					},
					Conditions: []metav1.Condition{
						quotaReservedCondition,
						admittedCondition,
					},
				},
			},
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGatesDuringTest(t, tc.features)
			ctx, _ := utiltesting.ContextWithLog(t)
			clientBuilder := utiltesting.NewClientBuilder().
				WithIndex(&corev1.Pod{}, indexer.WorkloadSliceNameKey, indexer.IndexPodWorkloadSliceName).
				WithIndex(&kueue.Workload{}, indexer.WorkloadSliceNameKey, indexer.IndexWorkloadSliceName).
				WithInterceptorFuncs(interceptor.Funcs{
					List: func(ctx context.Context, c client.WithWatch, objs client.ObjectList, opts ...client.ListOption) error {
						if _, isPods := objs.(*corev1.PodList); isPods && errors.Is(tc.wantErr, errListPods) {
							return errListPods
						}
						return c.List(ctx, objs, opts...)
					},
					SubResourcePatch: func(ctx context.Context, c client.Client, subResourceName string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
						if _, isWl := obj.(*kueue.Workload); isWl && errors.Is(tc.wantErr, errConflict) {
							return errConflict
						}
						return utiltesting.TreatSSAAsStrategicMerge(ctx, c, subResourceName, obj, patch, opts...)
					},
				})
			for _, p := range tc.pods {
				clientBuilder = clientBuilder.WithObjects(p)
			}
			for _, wl := range tc.workloads {
				clientBuilder = clientBuilder.WithStatusSubresource(wl)
			}
			kClient := clientBuilder.Build()
			for _, wl := range tc.workloads {
				if err := kClient.Create(ctx, wl); err != nil {
					t.Fatalf("Could not create workload %s: %v", wl.Name, err)
				}
			}

			request := types.NamespacedName{
				Namespace: testNamespace,
				Name:      testWorkload,
			}
			if tc.request != nil {
				request = *tc.request
			}
			waitForPodsReady := &configapi.WaitForPodsReady{
				UnscheduledTimeout: &metav1.Duration{
					Duration: time.Minute,
				},
			}
			if tc.configuration != nil {
				waitForPodsReady = tc.configuration.WaitForPodsReady
			}
			tracker := NewTracker(kClient, nil, waitForPodsReady, withClock(fakeClock))
			gotResult, gotErr := tracker.Reconcile(ctx, reconcile.Request{NamespacedName: request})
			if !errors.Is(gotErr, tc.wantErr) {
				t.Errorf("Reconcile() error = %v, want %v", gotErr, tc.wantErr)
			}
			if diff := cmp.Diff(tc.wantResult, gotResult); diff != "" {
				t.Errorf("Reconcile returned unexpected result (-want,+got):\n%s", diff)
			}

			if tc.reconcileTwice {
				if result, err := tracker.Reconcile(ctx, reconcile.Request{NamespacedName: request}); err != nil || result != (reconcile.Result{}) {
					t.Errorf("second Reconcile() = (%v, %v), want no action", result, err)
				}
			}

			for wlName, wantStatus := range tc.wantWorkloadStatuses {
				gotWorkload := &kueue.Workload{}
				if err := kClient.Get(ctx, types.NamespacedName{Namespace: testNamespace, Name: wlName}, gotWorkload); err != nil {
					t.Fatalf("Could not get workload %s: %v", wlName, err)
				}
				if diff := cmp.Diff(wantStatus, gotWorkload.Status, cmpopts.SortSlices(func(a, b metav1.Condition) bool {
					return a.Type < b.Type
				})); diff != "" {
					t.Errorf("Unexpected status on workload %s (-want,+got):\n%s", wlName, diff)
				}
			}
		})
	}
}

func TestPodHandler_Create(t *testing.T) {
	const (
		testNamespace  = "ns"
		testWorkload   = "wl"
		testPodSetName = kueue.PodSetReference("main")
	)

	testCases := map[string]struct {
		pod          *corev1.Pod
		wantRequests []reconcile.Request
	}{
		"create of a linked pod enqueues its workload": {
			pod: testingpod.MakePod("p1", testNamespace).
				Annotation(kueue.WorkloadAnnotation, testWorkload).
				Label(constants.PodSetLabel, string(testPodSetName)).
				Obj(),
			wantRequests: []reconcile.Request{
				{
					NamespacedName: types.NamespacedName{
						Namespace: testNamespace,
						Name:      testWorkload,
					},
				},
			},
		},
		"create of a pod without workload annotations is ignored": {
			pod: testingpod.MakePod("p1", testNamespace).
				Label(constants.PodSetLabel, string(testPodSetName)).
				Obj(),
		},
		"the workload slice name annotation takes precedence over the workload annotation": {
			pod: testingpod.MakePod("p1", testNamespace).
				Annotation(kueue.WorkloadAnnotation, "wl-2").
				Annotation(kueue.WorkloadSliceNameAnnotation, "wl-1").
				Label(constants.PodSetLabel, string(testPodSetName)).
				Obj(),
			wantRequests: []reconcile.Request{
				{
					NamespacedName: types.NamespacedName{
						Namespace: testNamespace,
						Name:      "wl-1",
					},
				},
			},
		},
		"create of a pod with only the workload slice name annotation enqueues the slice": {
			pod: testingpod.MakePod("p1", testNamespace).
				Annotation(kueue.WorkloadSliceNameAnnotation, "wl-1").
				Obj(),
			wantRequests: []reconcile.Request{
				{
					NamespacedName: types.NamespacedName{
						Namespace: testNamespace,
						Name:      "wl-1",
					},
				},
			},
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)
			q := &utiltesting.MockTypedRateLimitingInterface{}
			(&podHandler{}).Create(ctx, event.TypedCreateEvent[*corev1.Pod]{Object: tc.pod}, q)
			if diff := cmp.Diff(tc.wantRequests, q.Items); diff != "" {
				t.Errorf("Unexpected requests (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestPodHandler_Update(t *testing.T) {
	const (
		testNamespace  = "ns"
		testWorkload   = "wl"
		testPodSetName = kueue.PodSetReference("main")
	)

	fakeClock := testingclock.NewFakeClock(time.Now().Truncate(time.Second))

	testCases := map[string]struct {
		oldPod       *corev1.Pod
		newPod       *corev1.Pod
		wantRequests []reconcile.Request
	}{
		"update binding the pod after PodScheduled=True enqueues its workload": {
			oldPod: testingpod.MakePod("p1", testNamespace).
				Annotation(kueue.WorkloadAnnotation, testWorkload).
				Label(constants.PodSetLabel, string(testPodSetName)).
				StatusConditions(corev1.PodCondition{
					Type:   corev1.PodScheduled,
					Status: corev1.ConditionTrue,
				}).
				Obj(),
			newPod: testingpod.MakePod("p1", testNamespace).
				Annotation(kueue.WorkloadAnnotation, testWorkload).
				Label(constants.PodSetLabel, string(testPodSetName)).
				NodeName("node-a").
				StatusConditions(corev1.PodCondition{
					Type:   corev1.PodScheduled,
					Status: corev1.ConditionTrue,
				}).
				Obj(),
			wantRequests: []reconcile.Request{
				{
					NamespacedName: types.NamespacedName{
						Namespace: testNamespace,
						Name:      testWorkload,
					},
				},
			},
		},
		"update binding the pod without PodScheduled=True is ignored": {
			oldPod: testingpod.MakePod("p1", testNamespace).
				Annotation(kueue.WorkloadAnnotation, testWorkload).
				Obj(),
			newPod: testingpod.MakePod("p1", testNamespace).
				Annotation(kueue.WorkloadAnnotation, testWorkload).
				NodeName("node-a").
				Obj(),
		},
		"update setting PodScheduled=True without node name is ignored": {
			oldPod: testingpod.MakePod("p1", testNamespace).
				Annotation(kueue.WorkloadAnnotation, testWorkload).
				Obj(),
			newPod: testingpod.MakePod("p1", testNamespace).
				Annotation(kueue.WorkloadAnnotation, testWorkload).
				StatusConditions(corev1.PodCondition{
					Type:   corev1.PodScheduled,
					Status: corev1.ConditionTrue,
				}).
				Obj(),
		},
		"update losing PodScheduled=True on a bound pod enqueues its workload": {
			oldPod: testingpod.MakePod("p1", testNamespace).
				Annotation(kueue.WorkloadAnnotation, testWorkload).
				NodeName("node-a").
				StatusConditions(corev1.PodCondition{
					Type:   corev1.PodScheduled,
					Status: corev1.ConditionTrue,
				}).
				Obj(),
			newPod: testingpod.MakePod("p1", testNamespace).
				Annotation(kueue.WorkloadAnnotation, testWorkload).
				NodeName("node-a").
				StatusConditions(corev1.PodCondition{
					Type:   corev1.PodScheduled,
					Status: corev1.ConditionFalse,
				}).
				Obj(),
			wantRequests: []reconcile.Request{
				{
					NamespacedName: types.NamespacedName{
						Namespace: testNamespace,
						Name:      testWorkload,
					},
				},
			},
		},
		"update changing the phase enqueues its workload": {
			oldPod: testingpod.MakePod("p1", testNamespace).
				Annotation(kueue.WorkloadAnnotation, testWorkload).
				Label(constants.PodSetLabel, string(testPodSetName)).
				NodeName("node-a").
				Obj(),
			newPod: testingpod.MakePod("p1", testNamespace).
				Annotation(kueue.WorkloadAnnotation, testWorkload).
				Label(constants.PodSetLabel, string(testPodSetName)).
				NodeName("node-a").
				StatusPhase(corev1.PodSucceeded).
				Obj(),
			wantRequests: []reconcile.Request{
				{
					NamespacedName: types.NamespacedName{
						Namespace: testNamespace,
						Name:      testWorkload,
					},
				},
			},
		},
		"update starting the deletion enqueues its workload": {
			oldPod: testingpod.MakePod("p1", testNamespace).
				Annotation(kueue.WorkloadAnnotation, testWorkload).
				Label(constants.PodSetLabel, string(testPodSetName)).
				NodeName("node-a").
				Obj(),
			newPod: testingpod.MakePod("p1", testNamespace).
				Annotation(kueue.WorkloadAnnotation, testWorkload).
				Label(constants.PodSetLabel, string(testPodSetName)).
				NodeName("node-a").
				DeletionTimestamp(fakeClock.Now()).
				Obj(),
			wantRequests: []reconcile.Request{
				{
					NamespacedName: types.NamespacedName{
						Namespace: testNamespace,
						Name:      testWorkload,
					},
				},
			},
		},
		"update without a scheduling change is ignored": {
			oldPod: testingpod.MakePod("p1", testNamespace).
				Annotation(kueue.WorkloadAnnotation, testWorkload).
				Label(constants.PodSetLabel, string(testPodSetName)).
				NodeName("node-a").
				Obj(),
			newPod: testingpod.MakePod("p1", testNamespace).
				Annotation(kueue.WorkloadAnnotation, testWorkload).
				Label(constants.PodSetLabel, string(testPodSetName)).
				Label("extra", "label").
				NodeName("node-a").
				Obj(),
		},
		"nil and zero deletion timestamps both mean the pod is not deleting": {
			oldPod: testingpod.MakePod("p1", testNamespace).
				Annotation(kueue.WorkloadAnnotation, testWorkload).
				Obj(),
			newPod: testingpod.MakePod("p1", testNamespace).
				Annotation(kueue.WorkloadAnnotation, testWorkload).
				DeletionTimestamp(time.Time{}).
				Obj(),
		},
		"update with nil annotations and labels is ignored": {
			oldPod: &corev1.Pod{},
			newPod: &corev1.Pod{},
		},
		"update relinking the pod enqueues both workloads": {
			oldPod: testingpod.MakePod("p1", testNamespace).
				Annotation(kueue.WorkloadAnnotation, "wl-a").
				Label(constants.PodSetLabel, string(testPodSetName)).
				Obj(),
			newPod: testingpod.MakePod("p1", testNamespace).
				Annotation(kueue.WorkloadAnnotation, "wl-b").
				Label(constants.PodSetLabel, string(testPodSetName)).
				Obj(),
			wantRequests: []reconcile.Request{
				{
					NamespacedName: types.NamespacedName{
						Namespace: testNamespace,
						Name:      "wl-a",
					},
				},
				{
					NamespacedName: types.NamespacedName{
						Namespace: testNamespace,
						Name:      "wl-b",
					},
				},
			},
		},
		"update unlinking the pod enqueues the previous workload": {
			oldPod: testingpod.MakePod("p1", testNamespace).
				Annotation(kueue.WorkloadAnnotation, "wl-a").
				Label(constants.PodSetLabel, string(testPodSetName)).
				Obj(),
			newPod: testingpod.MakePod("p1", testNamespace).
				Obj(),
			wantRequests: []reconcile.Request{
				{
					NamespacedName: types.NamespacedName{
						Namespace: testNamespace,
						Name:      "wl-a",
					},
				},
			},
		},
		"update linking the pod enqueues the new workload": {
			oldPod: testingpod.MakePod("p1", testNamespace).
				Obj(),
			newPod: testingpod.MakePod("p1", testNamespace).
				Annotation(kueue.WorkloadAnnotation, testWorkload).
				Label(constants.PodSetLabel, string(testPodSetName)).
				Obj(),
			wantRequests: []reconcile.Request{
				{
					NamespacedName: types.NamespacedName{
						Namespace: testNamespace,
						Name:      testWorkload,
					},
				},
			},
		},
		"update changing the podset label enqueues the workload once": {
			oldPod: testingpod.MakePod("p1", testNamespace).
				Annotation(kueue.WorkloadAnnotation, testWorkload).
				Label(constants.PodSetLabel, string(testPodSetName)).
				Obj(),
			newPod: testingpod.MakePod("p1", testNamespace).
				Annotation(kueue.WorkloadAnnotation, testWorkload).
				Label(constants.PodSetLabel, "worker").
				Obj(),
			wantRequests: []reconcile.Request{
				{
					NamespacedName: types.NamespacedName{
						Namespace: testNamespace,
						Name:      testWorkload,
					},
				},
			},
		},
		"update setting PodScheduled=True after binding enqueues the workload": {
			oldPod: testingpod.MakePod("p1", testNamespace).
				Annotation(kueue.WorkloadAnnotation, testWorkload).
				Label(constants.PodSetLabel, string(testPodSetName)).
				NodeName("node-a").
				Obj(),
			newPod: testingpod.MakePod("p1", testNamespace).
				Annotation(kueue.WorkloadAnnotation, testWorkload).
				Label(constants.PodSetLabel, string(testPodSetName)).
				NodeName("node-a").
				StatusConditions(corev1.PodCondition{
					Type:   corev1.PodScheduled,
					Status: corev1.ConditionTrue,
				}).
				Obj(),
			wantRequests: []reconcile.Request{
				{
					NamespacedName: types.NamespacedName{
						Namespace: testNamespace,
						Name:      testWorkload,
					},
				},
			},
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)
			q := &utiltesting.MockTypedRateLimitingInterface{}
			(&podHandler{}).Update(ctx, event.TypedUpdateEvent[*corev1.Pod]{ObjectOld: tc.oldPod, ObjectNew: tc.newPod}, q)
			if diff := cmp.Diff(tc.wantRequests, q.Items); diff != "" {
				t.Errorf("Unexpected requests (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestPodHandler_Delete(t *testing.T) {
	const (
		testNamespace  = "ns"
		testWorkload   = "wl"
		testPodSetName = kueue.PodSetReference("main")
	)

	testCases := map[string]struct {
		pod          *corev1.Pod
		wantRequests []reconcile.Request
	}{
		"delete of a linked pod enqueues its workload": {
			pod: testingpod.MakePod("p1", testNamespace).
				Annotation(kueue.WorkloadAnnotation, testWorkload).
				Label(constants.PodSetLabel, string(testPodSetName)).
				Obj(),
			wantRequests: []reconcile.Request{
				{
					NamespacedName: types.NamespacedName{
						Namespace: testNamespace,
						Name:      testWorkload,
					},
				},
			},
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)
			q := &utiltesting.MockTypedRateLimitingInterface{}
			(&podHandler{}).Delete(ctx, event.TypedDeleteEvent[*corev1.Pod]{Object: tc.pod}, q)
			if diff := cmp.Diff(tc.wantRequests, q.Items); diff != "" {
				t.Errorf("Unexpected requests (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestPodHandler_Generic(t *testing.T) {
	const (
		testNamespace = "ns"
		testWorkload  = "wl"
	)

	testCases := map[string]struct {
		pod          *corev1.Pod
		wantRequests []reconcile.Request
	}{
		"generic event is ignored": {
			pod: testingpod.MakePod("p1", testNamespace).
				Annotation(kueue.WorkloadAnnotation, testWorkload).
				Obj(),
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)
			q := &utiltesting.MockTypedRateLimitingInterface{}
			(&podHandler{}).Generic(ctx, event.TypedGenericEvent[*corev1.Pod]{Object: tc.pod}, q)
			if diff := cmp.Diff(tc.wantRequests, q.Items); diff != "" {
				t.Errorf("Unexpected requests (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestTracker_Create(t *testing.T) {
	const (
		testNamespace = "ns"
		testWorkload  = "wl"
	)

	fakeClock := testingclock.NewFakeClock(time.Now().Truncate(time.Second))

	testCases := map[string]struct {
		workload *kueue.Workload
		want     bool
	}{
		"create of an admitted workload": {
			workload: utiltestingapi.MakeWorkload(testWorkload, testNamespace).
				ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").
					Obj(), fakeClock.Now()).
				AdmittedAt(true, fakeClock.Now()).
				Obj(),
			want: true,
		},
		"create of a pending workload": {
			workload: utiltestingapi.MakeWorkload(testWorkload, testNamespace).
				Obj(),
		},
		"create of a pending workload with a condition": {
			workload: utiltestingapi.MakeWorkload(testWorkload, testNamespace).
				Condition(metav1.Condition{
					Type:   kueue.WorkloadPodsScheduled,
					Status: metav1.ConditionTrue,
					Reason: kueue.WorkloadAllRequiredPodsScheduled,
				}).
				Obj(),
			want: true,
		},
		"create of an evicted workload": {
			workload: utiltestingapi.MakeWorkload(testWorkload, testNamespace).
				ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").
					Obj(), fakeClock.Now()).
				AdmittedAt(true, fakeClock.Now()).
				EvictedAt(fakeClock.Now()).
				Obj(),
		},
		"create of an evicted workload with a condition": {
			workload: utiltestingapi.MakeWorkload(testWorkload, testNamespace).
				ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").
					Obj(), fakeClock.Now()).
				AdmittedAt(true, fakeClock.Now()).
				EvictedAt(fakeClock.Now()).
				Condition(metav1.Condition{
					Type:   kueue.WorkloadPodsScheduled,
					Status: metav1.ConditionTrue,
					Reason: kueue.WorkloadAllRequiredPodsScheduled,
				}).
				Obj(),
			want: true,
		},
		"create of a finished workload": {
			workload: utiltestingapi.MakeWorkload(testWorkload, testNamespace).
				ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").
					Obj(), fakeClock.Now()).
				AdmittedAt(true, fakeClock.Now()).
				FinishedAt(fakeClock.Now()).
				Obj(),
		},
		"create of a finished workload with a condition": {
			workload: utiltestingapi.MakeWorkload(testWorkload, testNamespace).
				ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").
					Obj(), fakeClock.Now()).
				AdmittedAt(true, fakeClock.Now()).
				FinishedAt(fakeClock.Now()).
				Condition(metav1.Condition{
					Type:   kueue.WorkloadPodsScheduled,
					Status: metav1.ConditionTrue,
					Reason: kueue.WorkloadAllRequiredPodsScheduled,
				}).
				Obj(),
			want: true,
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			got := (&Tracker{}).Create(event.TypedCreateEvent[*kueue.Workload]{Object: tc.workload})
			if got != tc.want {
				t.Errorf("Create() = %t, want %t", got, tc.want)
			}
		})
	}
}

func TestTracker_Update(t *testing.T) {
	const (
		testNamespace = "ns"
		testWorkload  = "wl"
	)

	fakeClock := testingclock.NewFakeClock(time.Now().Truncate(time.Second))

	testCases := map[string]struct {
		oldWorkload *kueue.Workload
		newWorkload *kueue.Workload
		want        bool
	}{
		"update to an admitted workload": {
			oldWorkload: utiltestingapi.MakeWorkload(testWorkload, testNamespace).
				Obj(),
			newWorkload: utiltestingapi.MakeWorkload(testWorkload, testNamespace).
				ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").
					Obj(), fakeClock.Now()).
				AdmittedAt(true, fakeClock.Now()).
				Obj(),
			want: true,
		},
		"admission becomes false without scheduling history": {
			oldWorkload: utiltestingapi.MakeWorkload(testWorkload, testNamespace).
				ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").
					Obj(), fakeClock.Now()).
				AdmittedAt(true, fakeClock.Now()).
				Obj(),
			newWorkload: utiltestingapi.MakeWorkload(testWorkload, testNamespace).
				Condition(metav1.Condition{
					Type:               kueue.WorkloadAdmitted,
					Status:             metav1.ConditionFalse,
					Reason:             kueue.WorkloadAdmittedReasonNoReservation,
					LastTransitionTime: metav1.NewTime(fakeClock.Now()),
				}).
				Obj(),
		},
		"admission becomes false while keeping scheduled history": {
			oldWorkload: utiltestingapi.MakeWorkload(testWorkload, testNamespace).
				ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").
					Obj(), fakeClock.Now()).
				AdmittedAt(true, fakeClock.Now()).
				Condition(metav1.Condition{
					Type:   kueue.WorkloadPodsScheduled,
					Status: metav1.ConditionTrue,
					Reason: kueue.WorkloadAllRequiredPodsScheduled,
				}).
				Obj(),
			newWorkload: utiltestingapi.MakeWorkload(testWorkload, testNamespace).
				Condition(metav1.Condition{
					Type:               kueue.WorkloadAdmitted,
					Status:             metav1.ConditionFalse,
					Reason:             kueue.WorkloadAdmittedReasonNoReservation,
					LastTransitionTime: metav1.NewTime(fakeClock.Now()),
				}).
				Condition(metav1.Condition{
					Type:   kueue.WorkloadPodsScheduled,
					Status: metav1.ConditionTrue,
					Reason: kueue.WorkloadAllRequiredPodsScheduled,
				}).
				Obj(),
			want: true,
		},
		"update to an evicted workload": {
			oldWorkload: utiltestingapi.MakeWorkload(testWorkload, testNamespace).
				ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").
					Obj(), fakeClock.Now()).
				AdmittedAt(true, fakeClock.Now()).
				Obj(),
			newWorkload: utiltestingapi.MakeWorkload(testWorkload, testNamespace).
				ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").
					Obj(), fakeClock.Now()).
				AdmittedAt(true, fakeClock.Now()).
				EvictedAt(fakeClock.Now()).
				Obj(),
		},
		"update to an evicted workload keeping its condition": {
			oldWorkload: utiltestingapi.MakeWorkload(testWorkload, testNamespace).
				ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").
					Obj(), fakeClock.Now()).
				AdmittedAt(true, fakeClock.Now()).
				Obj(),
			newWorkload: utiltestingapi.MakeWorkload(testWorkload, testNamespace).
				ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").
					Obj(), fakeClock.Now()).
				AdmittedAt(true, fakeClock.Now()).
				EvictedAt(fakeClock.Now()).
				Condition(metav1.Condition{
					Type:   kueue.WorkloadPodsScheduled,
					Status: metav1.ConditionTrue,
					Reason: kueue.WorkloadAllRequiredPodsScheduled,
				}).
				Obj(),
			want: true,
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			got := (&Tracker{}).Update(event.TypedUpdateEvent[*kueue.Workload]{ObjectOld: tc.oldWorkload, ObjectNew: tc.newWorkload})
			if got != tc.want {
				t.Errorf("Update() = %t, want %t", got, tc.want)
			}
		})
	}
}

func TestTracker_Delete(t *testing.T) {
	const (
		testNamespace = "ns"
		testWorkload  = "wl"
	)

	fakeClock := testingclock.NewFakeClock(time.Now().Truncate(time.Second))

	testCases := map[string]struct {
		workload *kueue.Workload
		want     bool
	}{
		"delete": {
			workload: utiltestingapi.MakeWorkload(testWorkload, testNamespace).
				ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").
					Obj(), fakeClock.Now()).
				AdmittedAt(true, fakeClock.Now()).
				Obj(),
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			got := (&Tracker{}).Delete(event.TypedDeleteEvent[*kueue.Workload]{Object: tc.workload})
			if got != tc.want {
				t.Errorf("Delete() = %t, want %t", got, tc.want)
			}
		})
	}
}

func TestTracker_Generic(t *testing.T) {
	const (
		testNamespace = "ns"
		testWorkload  = "wl"
	)

	fakeClock := testingclock.NewFakeClock(time.Now().Truncate(time.Second))

	testCases := map[string]struct {
		workload *kueue.Workload
		want     bool
	}{
		"generic": {
			workload: utiltestingapi.MakeWorkload(testWorkload, testNamespace).
				ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").
					Obj(), fakeClock.Now()).
				AdmittedAt(true, fakeClock.Now()).
				Obj(),
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			got := (&Tracker{}).Generic(event.TypedGenericEvent[*kueue.Workload]{Object: tc.workload})
			if got != tc.want {
				t.Errorf("Generic() = %t, want %t", got, tc.want)
			}
		})
	}
}
