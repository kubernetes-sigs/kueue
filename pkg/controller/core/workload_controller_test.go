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
	"context"
	stderrors "errors"
	"fmt"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	resourcev1 "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	testingclock "k8s.io/utils/clock/testing"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	qcache "sigs.k8s.io/kueue/pkg/cache/queue"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/dra"
	"sigs.k8s.io/kueue/pkg/features"
	utilqueue "sigs.k8s.io/kueue/pkg/util/queue"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/test/util"
)

func TestAdmittedNotReadyWorkload(t *testing.T) {
	now := time.Now()
	minuteAgo := now.Add(-time.Minute)
	fakeClock := testingclock.NewFakeClock(now)

	testCases := map[string]struct {
		workload            kueue.Workload
		waitForPodsReady    *waitForPodsReadyConfig
		wantUnderlyingCause kueue.EvictionUnderlyingCause
		wantRecheckAfter    time.Duration
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
			waitForPodsReady:    &waitForPodsReadyConfig{timeout: 5 * time.Minute},
			wantUnderlyingCause: kueue.WorkloadWaitForStart,
			wantRecheckAfter:    4 * time.Minute,
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
			waitForPodsReady:    &waitForPodsReadyConfig{timeout: 5 * time.Minute},
			wantUnderlyingCause: kueue.WorkloadWaitForStart,
			wantRecheckAfter:    0,
		},
		"with reason WorkloadWaitForPodsReadyStart; workload with Admitted=True, PodsReady=False; counting since admitted.LastTransitionTime": {
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
							Reason:             kueue.WorkloadWaitForStart,
							LastTransitionTime: metav1.NewTime(now),
						},
					},
				},
			},
			waitForPodsReady:    &waitForPodsReadyConfig{timeout: 5 * time.Minute},
			wantUnderlyingCause: kueue.WorkloadWaitForStart,
			wantRecheckAfter:    4 * time.Minute,
		},
		"with reason PodsReady; workload with Admitted=True, PodsReady=False; counting since admitted.LastTransitionTime": {
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
							Reason:             "PodsReady",
							LastTransitionTime: metav1.NewTime(now),
						},
					},
				},
			},
			waitForPodsReady:    &waitForPodsReadyConfig{timeout: 5 * time.Minute},
			wantUnderlyingCause: kueue.WorkloadWaitForStart,
			wantRecheckAfter:    4 * time.Minute,
		},
		"workload with Admitted=True, PodsReady=False, Reason=WorkloadWaitForPodsReadyRecovery": {
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
							Reason:             kueue.WorkloadWaitForRecovery,
							LastTransitionTime: metav1.NewTime(now),
						},
					},
				},
			},
			waitForPodsReady:    &waitForPodsReadyConfig{recoveryTimeout: ptr.To(3 * time.Minute)},
			wantUnderlyingCause: kueue.WorkloadWaitForRecovery,
			wantRecheckAfter:    3 * time.Minute,
		},
		"workload with Admitted=True, PodsReady=False, Reason=WorkloadWaitForPodsReadyRecovery, recoveryTimeout not configured": {
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
							Reason:             kueue.WorkloadWaitForRecovery,
							LastTransitionTime: metav1.NewTime(now),
						},
					},
				},
			},
			waitForPodsReady: &waitForPodsReadyConfig{recoveryTimeout: nil},
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
			underlyingCause, recheckAfter := wRec.admittedNotReadyWorkload(&tc.workload)

			if tc.wantRecheckAfter != recheckAfter {
				t.Errorf("Unexpected recheckAfter, want=%v, got=%v", tc.wantRecheckAfter, recheckAfter)
			}
			if tc.wantUnderlyingCause != underlyingCause {
				t.Errorf("Unexpected underlyingCause, want=%v, got=%v", tc.wantUnderlyingCause, underlyingCause)
			}
		})
	}
}

func TestSyncCheckStates(t *testing.T) {
	now := time.Now()
	fakeClock := testingclock.NewFakeClock(now)
	cases := map[string]struct {
		states               []kueue.AdmissionCheckState
		list                 []kueue.AdmissionCheckReference
		wantStates           []kueue.AdmissionCheckState
		wantChange           bool
		ignoreTransitionTime bool
	}{
		"nil conditions, nil list": {},
		"add to nil conditions": {
			list:       []kueue.AdmissionCheckReference{"ac1", "ac2"},
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
			list:       []kueue.AdmissionCheckReference{"ac1", "ac2"},
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
					Name:    "ac0",
					State:   kueue.CheckStateReady,
					Message: "Message one",
				},
				{
					Name:  "ac1",
					State: kueue.CheckStatePending,
				},
			},
			list:       []kueue.AdmissionCheckReference{"ac0", "ac1"},
			wantChange: false,
			wantStates: []kueue.AdmissionCheckState{
				{
					Name:    "ac0",
					State:   kueue.CheckStateReady,
					Message: "Message one",
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
			gotStates, gotShouldChange := syncAdmissionCheckConditions(tc.states, sets.New(tc.list...), fakeClock)

			if tc.wantChange != gotShouldChange {
				t.Errorf("Unexpected should change, want=%v", tc.wantChange)
			}

			var opts cmp.Options
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
	workloadCmpOpts = cmp.Options{
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
	now := time.Now().Truncate(time.Second)
	fakeClock := testingclock.NewFakeClock(now)

	cases := map[string]struct {
		enableDRAFeature bool

		workload                  *kueue.Workload
		cq                        *kueue.ClusterQueue
		lq                        *kueue.LocalQueue
		resourceClaims            []*resourcev1.ResourceClaim
		resourceClaimTemplates    []*resourcev1.ResourceClaimTemplate
		wantDRAResourceTotal      *int64
		wantWorkloadsInQueue      *int
		wantWorkload              *kueue.Workload
		wantWorkloadUseMergePatch *kueue.Workload // workload version to compensate for the difference between use of Apply and Merge patch in FakeClient
		wantError                 error
		wantErrorMsg              string
		wantEvents                []utiltesting.EventRecord
		wantResult                reconcile.Result
		reconcilerOpts            []Option
	}{
		"reconcile DRA ResourceClaim should be rejected as inadmissible": {
			enableDRAFeature: true,
			workload: utiltestingapi.MakeWorkload("wlWithDRAResourceClaim", "ns").
				Queue("lq").
				PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
					ResourceClaim("gpu", "rc1").
					Obj()).
				Obj(),
			resourceClaims: []*resourcev1.ResourceClaim{
				utiltesting.MakeResourceClaim("rc1", "ns").
					DeviceRequest("", "gpu.example.com", 1).
					Obj(),
			},
			cq: utiltestingapi.MakeClusterQueue("cq").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("flavor1").
						Resource("gpus", "2").Obj(),
				).Obj(),
			lq: utiltestingapi.MakeLocalQueue("lq", "ns").ClusterQueue("cq").Obj(),
			wantWorkload: utiltestingapi.MakeWorkload("wlWithDRAResourceClaim", "ns").
				Queue("lq").
				PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
					ResourceClaim("gpu", "rc1").
					Obj()).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadQuotaReserved,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadInadmissible,
					Message: "DynamicResourceAllocation feature does not support use of resource claims",
				}).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadRequeued,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadInadmissible,
					Message: "DRA resource claims not supported",
				}).
				Obj(),
			wantEvents: nil,
		},
		"reconcile DRA ResourceClaimTemplate should be pre-processed and queued": {
			enableDRAFeature:     true,
			wantDRAResourceTotal: ptr.To(int64(1)),
			wantWorkloadsInQueue: ptr.To(1),
			workload: utiltestingapi.MakeWorkload("wlWithDRAResourceClaimTemplate", "ns").
				Queue("lq").
				PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
					ResourceClaimTemplate("gpu", "gpu-template").
					Obj()).
				Obj(),
			resourceClaimTemplates: []*resourcev1.ResourceClaimTemplate{
				utiltesting.MakeResourceClaimTemplate("gpu-template", "ns").
					DeviceRequest("gpu-request", "gpu.example.com", 1).
					Obj(),
			},
			cq: utiltestingapi.MakeClusterQueue("cq").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("flavor1").
						Resource("gpu", "2").Obj(),
				).Obj(),
			lq: utiltestingapi.MakeLocalQueue("lq", "ns").ClusterQueue("cq").Obj(),
			wantWorkload: utiltestingapi.MakeWorkload("wlWithDRAResourceClaimTemplate", "ns").
				Queue("lq").
				PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
					ResourceClaimTemplate("gpu", "gpu-template").
					Obj()).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadQuotaReserved,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadInadmissible,
					Message: "ClusterQueue cq is inactive",
				}).
				Obj(),
			wantEvents: nil,
		},
		"reconcile DRA ResourceClaimTemplate multi-pod should be pre-processed and queued": {
			enableDRAFeature:     true,
			wantDRAResourceTotal: ptr.To(int64(6)),
			wantWorkloadsInQueue: ptr.To(1),
			workload: utiltestingapi.MakeWorkload("wlMultiPodDRA", "ns").
				Queue("lq").
				PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 3).
					ResourceClaimTemplate("gpu", "gpu-template").
					Obj()).
				Obj(),
			resourceClaimTemplates: []*resourcev1.ResourceClaimTemplate{
				utiltesting.MakeResourceClaimTemplate("gpu-template", "ns").
					DeviceRequest("gpu-request", "gpu.example.com", 2).
					Obj(),
			},
			cq: utiltestingapi.MakeClusterQueue("cq").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("flavor1").
						Resource("gpu", "10").Obj(),
				).Obj(),
			lq: utiltestingapi.MakeLocalQueue("lq", "ns").ClusterQueue("cq").Obj(),
			wantWorkload: utiltestingapi.MakeWorkload("wlMultiPodDRA", "ns").
				Queue("lq").
				PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 3).
					ResourceClaimTemplate("gpu", "gpu-template").
					Obj()).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadQuotaReserved,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadInadmissible,
					Message: "ClusterQueue cq is inactive",
				}).
				Obj(),
			wantEvents: nil,
		},
		"reconcile DRA ResourceClaimTemplate with unmapped device class": {
			enableDRAFeature: true,
			workload: utiltestingapi.MakeWorkload("wlUnmappedDRA", "ns").
				Queue("lq").
				PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
					ResourceClaimTemplate("gpu", "gpu-template").
					Obj()).
				Obj(),
			resourceClaimTemplates: []*resourcev1.ResourceClaimTemplate{
				utiltesting.MakeResourceClaimTemplate("gpu-template", "ns").
					DeviceRequest("gpu-request", "unmapped.example.com", 1).
					Obj(),
			},
			cq: utiltestingapi.MakeClusterQueue("cq").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("flavor1").
						Resource("gpu", "2").Obj(),
				).Obj(),
			lq: utiltestingapi.MakeLocalQueue("lq", "ns").ClusterQueue("cq").Obj(),
			wantWorkload: func() *kueue.Workload {
				wl := utiltestingapi.MakeWorkload("wlUnmappedDRA", "ns").
					Queue("lq").
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
						ResourceClaimTemplate("gpu", "gpu-template").
						Obj()).
					Condition(metav1.Condition{
						Type:    kueue.WorkloadQuotaReserved,
						Status:  metav1.ConditionFalse,
						Reason:  kueue.WorkloadInadmissible,
						Message: "spec.podSets[0].template.spec.resourceClaims[0].resourceClaimTemplateName: Not found: \"DeviceClass unmapped.example.com is not mapped in DRA configuration for podset main\"",
					}).
					Condition(metav1.Condition{
						Type:    kueue.WorkloadRequeued,
						Status:  metav1.ConditionFalse,
						Reason:  kueue.WorkloadInadmissible,
						Message: "spec.podSets[0].template.spec.resourceClaims[0].resourceClaimTemplateName: Not found: \"DeviceClass unmapped.example.com is not mapped in DRA configuration for podset main\"",
					}).
					Obj()
				wl.Spec.PodSets[0].Template.Spec.ResourceClaims = []corev1.PodResourceClaim{{
					Name: "gpu", ResourceClaimTemplateName: ptr.To("gpu-template"),
				}}
				if len(wl.Spec.PodSets[0].Template.Spec.Containers) > 0 {
					wl.Spec.PodSets[0].Template.Spec.Containers[0].Resources.Claims = []corev1.ResourceClaim{{Name: "gpu"}}
				}
				return wl
			}(),
			wantErrorMsg: "not mapped in DRA configuration",
			wantEvents:   nil,
		},
		"reconcile DRA ResourceClaimTemplate not found should return error": {
			enableDRAFeature: true,
			workload: utiltestingapi.MakeWorkload("wlMissingTemplate", "ns").
				Queue("lq").
				PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
					ResourceClaimTemplate("gpu", "missing-template").
					Obj()).
				Obj(),
			cq: utiltestingapi.MakeClusterQueue("cq").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("flavor1").
						Resource("gpu", "2").Obj(),
				).Obj(),
			lq: utiltestingapi.MakeLocalQueue("lq", "ns").ClusterQueue("cq").Obj(),
			wantWorkload: utiltestingapi.MakeWorkload("wlMissingTemplate", "ns").
				Queue("lq").
				PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
					ResourceClaimTemplate("gpu", "missing-template").
					Obj()).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadQuotaReserved,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadInadmissible,
					Message: `spec.podSets[0].template.spec.resourceClaims[0]: Internal error: failed to get claim spec for ResourceClaimTemplate missing-template in podset main: resourceclaimtemplates.resource.k8s.io "missing-template" not found`,
				}).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadRequeued,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadInadmissible,
					Message: `spec.podSets[0].template.spec.resourceClaims[0]: Internal error: failed to get claim spec for ResourceClaimTemplate missing-template in podset main: resourceclaimtemplates.resource.k8s.io "missing-template" not found`,
				}).
				Obj(),
			wantErrorMsg: "failed to get claim spec",
			wantEvents:   nil,
		},
		"assign Admission Checks from ClusterQueue.spec.AdmissionCheckStrategy": {
			workload: utiltestingapi.MakeWorkload("wl", "ns").
				ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment("cpu", "flavor1", "1").
						Obj()).
					Obj(), now).
				Queue("queue").
				Obj(),
			cq: utiltestingapi.MakeClusterQueue("cq").
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas("flavor1").Obj(), *utiltestingapi.MakeFlavorQuotas("flavor2").Obj()).
				AdmissionCheckStrategy(
					*utiltestingapi.MakeAdmissionCheckStrategyRule("ac1", "flavor1").Obj(),
					*utiltestingapi.MakeAdmissionCheckStrategyRule("ac2").Obj()).
				Obj(),
			lq: utiltestingapi.MakeLocalQueue("queue", "ns").ClusterQueue("cq").Obj(),
			wantWorkload: utiltestingapi.MakeWorkload("wl", "ns").
				ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment("cpu", "flavor1", "1").
						Obj()).
					Obj(), now).
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
			workload: utiltestingapi.MakeWorkload("wl", "ns").
				ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment("cpu", "flavor1", "1").
						Obj()).
					Obj(), now).
				Queue("queue").
				Obj(),
			cq: utiltestingapi.MakeClusterQueue("cq").
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas("flavor1").Obj(), *utiltestingapi.MakeFlavorQuotas("flavor2").Obj()).
				AdmissionChecks("ac1", "ac2").
				Obj(),
			lq: utiltestingapi.MakeLocalQueue("queue", "ns").ClusterQueue("cq").Obj(),
			wantWorkload: utiltestingapi.MakeWorkload("wl", "ns").
				ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment("cpu", "flavor1", "1").
						Obj()).
					Obj(), now).
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
			workload: utiltestingapi.MakeWorkload("wl", "ns").
				ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").Obj(), now).
				AdmissionCheck(kueue.AdmissionCheckState{
					Name:  "check",
					State: kueue.CheckStateReady,
				}).
				Obj(),
			wantWorkload: utiltestingapi.MakeWorkload("wl", "ns").
				ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").Obj(), now).
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
						fakeClock.Since(metav1.NewTime(now).Time.Truncate(time.Second)).Seconds()),
				},
			},
		},
		"already admitted": {
			workload: utiltestingapi.MakeWorkload("wl", "ns").
				ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").Obj(), now).
				AdmittedAt(true, now).
				AdmissionCheck(kueue.AdmissionCheckState{
					Name:  "check",
					State: kueue.CheckStateReady,
				}).
				Obj(),
			wantWorkload: utiltestingapi.MakeWorkload("wl", "ns").
				ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").Obj(), now).
				AdmittedAt(true, now).
				AdmissionCheck(kueue.AdmissionCheckState{
					Name:  "check",
					State: kueue.CheckStateReady,
				}).
				Obj(),
		},
		"remove finalizer for finished workload": {
			workload: utiltestingapi.MakeWorkload("unit-test", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
				Condition(metav1.Condition{
					Type:   "Finished",
					Status: "True",
				}).
				DeletionTimestamp(now).
				Obj(),
			wantWorkload: nil,
		},
		"don't remove finalizer for owned finished workload": {
			workload: utiltestingapi.MakeWorkload("unit-test", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
				Condition(metav1.Condition{
					Type:   "Finished",
					Status: "True",
				}).
				ControllerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "job", "test-uid").
				DeletionTimestamp(now).
				Obj(),
			wantWorkload: utiltestingapi.MakeWorkload("unit-test", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
				Condition(metav1.Condition{
					Type:   "Finished",
					Status: "True",
				}).
				ControllerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "job", "test-uid").
				DeletionTimestamp(now).
				Obj(),
		},
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
						Message: "Admission check(s): check, were rejected",
					},
				).
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
						Message: "Admission check(s): check, were rejected",
					},
				).
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
					Message: "Admission check(s): check-1, were rejected",
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
						RetryCount: ptr.To(int32(1)),
					},
				).
				Conditions(
					metav1.Condition{
						Type:    kueue.WorkloadEvicted,
						Status:  metav1.ConditionTrue,
						Reason:  "DeactivatedDueToAdmissionCheck",
						Message: "The workload is deactivated due to Admission check(s): check-1, were rejected",
					},
					// In a real cluster this condition would be removed but it cant be in the fake cluster
					metav1.Condition{
						Type:    kueue.WorkloadDeactivationTarget,
						Status:  metav1.ConditionTrue,
						Reason:  kueue.WorkloadEvictedByAdmissionCheck,
						Message: "Admission check(s): check-1, were rejected",
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
						RetryCount: ptr.To(int32(1)),
					},
				).
				Conditions(
					metav1.Condition{
						Type:    kueue.WorkloadEvicted,
						Status:  metav1.ConditionTrue,
						Reason:  "DeactivatedDueToAdmissionCheck",
						Message: "The workload is deactivated due to Admission check(s): check-1, were rejected",
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
					Message:   "The workload is deactivated due to Admission check(s): check-1, were rejected",
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
					RetryCount: ptr.To(int32(1)),
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
				RequeueState(ptr.To[int32](3), nil).
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
				RequeueState(ptr.To[int32](4), ptr.To(metav1.NewTime(now.Add(80*time.Second).Truncate(time.Second)))).
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
				RequeueState(ptr.To[int32](1), ptr.To(metav1.NewTime(now.Add(-1*time.Second).Truncate(time.Second)))).
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
				RequeueState(ptr.To[int32](1), ptr.To(metav1.NewTime(now.Add(1*time.Second).Truncate(time.Second)))).
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
				RequeueState(ptr.To[int32](10), ptr.To(metav1.NewTime(now.Add(-1*time.Second).Truncate(time.Second)))).
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
				RequeueState(ptr.To[int32](11), ptr.To(metav1.NewTime(now.Add(7200*time.Second).Truncate(time.Second)))).
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
					recoveryTimeout:             ptr.To(3 * time.Second),
					requeuingBackoffLimitCount:  ptr.To[int32](100),
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
				RequeueState(ptr.To[int32](1), ptr.To(metav1.NewTime(now.Add(7200*time.Second).Truncate(time.Second)))).
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
		"should keep the WorkloadRequeued condition until the WaitForPodsReady backoff expires": {
			workload: utiltestingapi.MakeWorkload("wl", "ns").
				Active(true).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadRequeued,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadEvictedByPodsReadyTimeout,
					Message: "Exceeded the PodsReady timeout ns",
				}).
				RequeueState(ptr.To[int32](1), ptr.To(metav1.NewTime(now.Add(60*time.Second).Truncate(time.Second)))).
				Obj(),
			wantWorkload: utiltestingapi.MakeWorkload("wl", "ns").
				Active(true).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadRequeued,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadEvictedByPodsReadyTimeout,
					Message: "Exceeded the PodsReady timeout ns",
				}).
				RequeueState(ptr.To[int32](1), ptr.To(metav1.NewTime(now.Add(60*time.Second).Truncate(time.Second)))).
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
				RequeueState(ptr.To[int32](1), ptr.To(metav1.NewTime(now.Add(60*time.Second).Truncate(time.Second)))).
				Obj(),
			wantWorkload: utiltestingapi.MakeWorkload("wl", "ns").
				Active(true).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadRequeued,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadEvictedByAdmissionCheck,
					Message: "Exceeded the AdmissionCheck timeout ns",
				}).
				RequeueState(ptr.To[int32](1), ptr.To(metav1.NewTime(now.Add(60*time.Second).Truncate(time.Second)))).
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
				RequeueState(ptr.To[int32](1), ptr.To(metav1.NewTime(now.Truncate(time.Second)))).
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
				RequeueState(ptr.To[int32](1), ptr.To(metav1.NewTime(now.Truncate(time.Second)))).
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
		"should set the Inadmissible reason on QuotaReservation condition when the LocalQueue was deleted": {
			cq: utiltestingapi.MakeClusterQueue("cq").AdmissionChecks("check").Obj(),
			workload: utiltestingapi.MakeWorkload("wl", "ns").
				Active(true).
				ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").Obj(), now).
				AdmissionCheck(kueue.AdmissionCheckState{
					Name:  "check",
					State: kueue.CheckStatePending,
				}).
				Queue("lq").
				Obj(),
			wantWorkload: utiltestingapi.MakeWorkload("wl", "ns").
				Active(true).
				Admission(utiltestingapi.MakeAdmission("cq", kueue.DefaultPodSetName).Obj()).
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
			wantWorkloadUseMergePatch: utiltestingapi.MakeWorkload("wl", "ns").
				Active(true).
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
			cq: utiltestingapi.MakeClusterQueue("cq").AdmissionChecks("check").Obj(),
			lq: utiltestingapi.MakeLocalQueue("lq", "ns").ClusterQueue("cq").StopPolicy(kueue.Hold).Obj(),
			workload: utiltestingapi.MakeWorkload("wl", "ns").
				Active(true).
				ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").Obj(), now).
				AdmissionCheck(kueue.AdmissionCheckState{
					Name:  "check",
					State: kueue.CheckStatePending,
				}).
				Queue("lq").
				Obj(),
			wantWorkload: utiltestingapi.MakeWorkload("wl", "ns").
				Active(true).
				Admission(utiltestingapi.MakeAdmission("cq", kueue.DefaultPodSetName).Obj()).
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
			wantWorkloadUseMergePatch: utiltestingapi.MakeWorkload("wl", "ns").
				Active(true).
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
			lq: utiltestingapi.MakeLocalQueue("lq", "ns").ClusterQueue("cq").Obj(),
			workload: utiltestingapi.MakeWorkload("wl", "ns").
				Active(true).
				ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").Obj(), now).
				AdmissionCheck(kueue.AdmissionCheckState{
					Name:  "check",
					State: kueue.CheckStatePending,
				}).
				Queue("lq").
				Obj(),
			wantWorkload: utiltestingapi.MakeWorkload("wl", "ns").
				Active(true).
				Admission(utiltestingapi.MakeAdmission("cq", kueue.DefaultPodSetName).Obj()).
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
			wantWorkloadUseMergePatch: utiltestingapi.MakeWorkload("wl", "ns").
				Active(true).
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
			cq: utiltestingapi.MakeClusterQueue("cq").AdmissionChecks("check").StopPolicy(kueue.Hold).Obj(),
			lq: utiltestingapi.MakeLocalQueue("lq", "ns").ClusterQueue("cq").Obj(),
			workload: utiltestingapi.MakeWorkload("wl", "ns").
				Active(true).
				ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").Obj(), now).
				AdmissionCheck(kueue.AdmissionCheckState{
					Name:  "check",
					State: kueue.CheckStatePending,
				}).
				Queue("lq").
				Obj(),
			wantWorkload: utiltestingapi.MakeWorkload("wl", "ns").
				Active(true).
				Admission(utiltestingapi.MakeAdmission("cq", kueue.DefaultPodSetName).Obj()).
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
			wantWorkloadUseMergePatch: utiltestingapi.MakeWorkload("wl", "ns").
				Active(true).
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
			workload: utiltestingapi.MakeWorkload("wl", "ns").
				Active(true).
				Queue("lq").
				Obj(),
			wantWorkload: utiltestingapi.MakeWorkload("wl", "ns").
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
			lq: utiltestingapi.MakeLocalQueue("lq", "ns").StopPolicy(kueue.Hold).Obj(),
			workload: utiltestingapi.MakeWorkload("wl", "ns").
				Active(true).
				Queue("lq").
				Obj(),
			wantWorkload: utiltestingapi.MakeWorkload("wl", "ns").
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
			lq: utiltestingapi.MakeLocalQueue("lq", "ns").StopPolicy(kueue.HoldAndDrain).Obj(),
			workload: utiltestingapi.MakeWorkload("wl", "ns").
				Active(true).
				Queue("lq").
				Obj(),
			wantWorkload: utiltestingapi.MakeWorkload("wl", "ns").
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
			lq: utiltestingapi.MakeLocalQueue("lq", "ns").ClusterQueue("cq").StopPolicy(kueue.None).Obj(),
			workload: utiltestingapi.MakeWorkload("wl", "ns").
				Active(true).
				Queue("lq").
				Obj(),
			wantWorkload: utiltestingapi.MakeWorkload("wl", "ns").
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
			lq: utiltestingapi.MakeLocalQueue("lq", "ns").ClusterQueue("cq").StopPolicy(kueue.None).Obj(),
			cq: utiltestingapi.MakeClusterQueue("cq").StopPolicy(kueue.Hold).Obj(),
			workload: utiltestingapi.MakeWorkload("wl", "ns").
				Active(true).
				Queue("lq").
				Obj(),
			wantWorkload: utiltestingapi.MakeWorkload("wl", "ns").
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
			workload: utiltestingapi.MakeWorkload("wl", "ns").
				ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").Obj(), now).
				MaximumExecutionTimeSeconds(120).
				AdmittedAt(true, now.Add(-time.Minute)).
				ControllerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "ownername", "owneruid").
				Obj(),
			wantWorkload: utiltestingapi.MakeWorkload("wl", "ns").
				ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").Obj(), now).
				MaximumExecutionTimeSeconds(120).
				AdmittedAt(true, now.Add(-time.Minute)).
				ControllerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "ownername", "owneruid").
				Obj(),
			wantResult: reconcile.Result{RequeueAfter: time.Minute},
		},

		"admitted workload with max execution time - expired": {
			workload: utiltestingapi.MakeWorkload("wl", "ns").
				ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").Obj(), now).
				MaximumExecutionTimeSeconds(60).
				AdmittedAt(true, now.Add(-2*time.Minute)).
				ControllerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "ownername", "owneruid").
				Obj(),
			wantWorkload: utiltestingapi.MakeWorkload("wl", "ns").
				ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").Obj(), now).
				MaximumExecutionTimeSeconds(60).
				AdmittedAt(true, now.Add(-2*time.Minute)).
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
		"shouldn't delete the workload because, object retention not configured": {
			workload: utiltestingapi.MakeWorkload("wl", "ns").
				Condition(metav1.Condition{
					Type:   kueue.WorkloadFinished,
					Status: metav1.ConditionTrue,
				}).
				Obj(),
			reconcilerOpts: []Option{
				WithWorkloadRetention(nil),
			},
			wantWorkload: utiltestingapi.MakeWorkload("wl", "ns").
				Condition(metav1.Condition{
					Type:   kueue.WorkloadFinished,
					Status: metav1.ConditionTrue,
				}).
				Obj(),
			wantError: nil,
		},
		"shouldn't try to delete the workload (no event emitted) because it is already being deleted by kubernetes, object retention configured": {
			workload: utiltestingapi.MakeWorkload("wl", "ns").
				Condition(metav1.Condition{
					Type:   kueue.WorkloadFinished,
					Status: metav1.ConditionTrue,
					LastTransitionTime: metav1.Time{
						Time: now,
					},
				}).
				DeletionTimestamp(now).
				Finalizers(kueue.ResourceInUseFinalizerName).
				Obj(),
			reconcilerOpts: []Option{
				WithWorkloadRetention(
					&workloadRetentionConfig{
						afterFinished: ptr.To(util.LongTimeout),
					},
				),
			},
			wantWorkload: nil,
			wantError:    nil,
		},
		"shouldn't try to delete the workload because the retention period hasn't elapsed yet, object retention configured": {
			workload: utiltestingapi.MakeWorkload("wl", "ns").
				Condition(metav1.Condition{
					Type:               kueue.WorkloadFinished,
					Status:             metav1.ConditionTrue,
					LastTransitionTime: metav1.NewTime(now.Add(-util.Timeout)),
				}).
				Obj(),
			reconcilerOpts: []Option{
				WithWorkloadRetention(
					&workloadRetentionConfig{
						afterFinished: ptr.To(util.LongTimeout),
					},
				),
			},
			wantResult: reconcile.Result{
				RequeueAfter: util.LongTimeout - util.Timeout,
			},
			wantWorkload: utiltestingapi.MakeWorkload("wl", "ns").
				Condition(metav1.Condition{
					Type:               kueue.WorkloadFinished,
					Status:             metav1.ConditionTrue,
					LastTransitionTime: metav1.NewTime(now.Add(-util.Timeout)),
				}).
				Obj(),
			wantError: nil,
		},
		"should delete the workload because the retention period has elapsed, object retention configured": {
			workload: utiltestingapi.MakeWorkload("wl", "ns").
				Condition(metav1.Condition{
					Type:               kueue.WorkloadFinished,
					Status:             metav1.ConditionTrue,
					LastTransitionTime: metav1.NewTime(now.Add(-2 * util.LongTimeout)),
				}).
				Obj(),
			reconcilerOpts: []Option{
				WithWorkloadRetention(
					&workloadRetentionConfig{
						afterFinished: ptr.To(util.LongTimeout),
					},
				),
			},
			wantWorkload: nil,
			wantEvents: []utiltesting.EventRecord{
				{
					Key: types.NamespacedName{
						Namespace: "ns",
						Name:      "wl",
					},
					EventType: corev1.EventTypeNormal,
					Reason:    "Deleted",
					Message:   "Deleted finished workload due to elapsed retention",
				},
			},
			wantError: nil,
		},
		"should update requeueState.requeueAt when admission check sets a delay": {
			workload: utiltestingapi.MakeWorkload("wl", "ns").
				RequeueState(nil, ptr.To(metav1.NewTime(now.Add(5*time.Second)))).
				AdmissionCheck(kueue.AdmissionCheckState{
					Name:                "check",
					State:               kueue.CheckStateRetry,
					RequeueAfterSeconds: ptr.To(int32(10)),
					LastTransitionTime:  metav1.NewTime(now),
				}).
				Obj(),
			wantWorkload: utiltestingapi.MakeWorkload("wl", "ns").
				RequeueState(nil, ptr.To(metav1.NewTime(now.Add(10*time.Second)))).
				AdmissionCheck(kueue.AdmissionCheckState{
					Name:                "check",
					State:               kueue.CheckStateRetry,
					RequeueAfterSeconds: ptr.To(int32(10)),
					LastTransitionTime:  metav1.NewTime(now),
				}).
				Obj(),
			wantWorkloadUseMergePatch: utiltestingapi.MakeWorkload("wl", "ns").
				RequeueState(nil, ptr.To(metav1.NewTime(now.Add(10*time.Second)))).
				AdmissionCheck(kueue.AdmissionCheckState{
					Name:                "check",
					State:               kueue.CheckStateRetry,
					RequeueAfterSeconds: ptr.To(int32(10)),
					LastTransitionTime:  metav1.NewTime(now),
				}).
				Obj(),
			wantResult: reconcile.Result{},
		},
		"should not update requeueState.requeueAt when admission check delay is smaller": {
			workload: utiltestingapi.MakeWorkload("wl", "ns").
				RequeueState(nil, ptr.To(metav1.NewTime(now.Add(10*time.Second)))).
				AdmissionCheck(kueue.AdmissionCheckState{
					Name:                "check",
					State:               kueue.CheckStateRetry,
					RequeueAfterSeconds: ptr.To(int32(5)),
					LastTransitionTime:  metav1.NewTime(now),
				}).
				Obj(),
			wantWorkload: utiltestingapi.MakeWorkload("wl", "ns").
				RequeueState(nil, ptr.To(metav1.NewTime(now.Add(10*time.Second)))).
				AdmissionCheck(kueue.AdmissionCheckState{
					Name:                "check",
					State:               kueue.CheckStateRetry,
					RequeueAfterSeconds: ptr.To(int32(5)),
					LastTransitionTime:  metav1.NewTime(now),
				}).
				Obj(),
			wantWorkloadUseMergePatch: utiltestingapi.MakeWorkload("wl", "ns").
				RequeueState(nil, ptr.To(metav1.NewTime(now.Add(10*time.Second)))).
				AdmissionCheck(kueue.AdmissionCheckState{
					Name:                "check",
					State:               kueue.CheckStateRetry,
					RequeueAfterSeconds: ptr.To(int32(5)),
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
					RequeueAfterSeconds: ptr.To(int32(5)),
					LastTransitionTime:  metav1.NewTime(now),
				}).
				AdmissionCheck(kueue.AdmissionCheckState{
					Name:                "check2",
					State:               kueue.CheckStateRetry,
					RequeueAfterSeconds: ptr.To(int32(7)),
					LastTransitionTime:  metav1.NewTime(now),
				}).
				Obj(),
			wantWorkload: utiltestingapi.MakeWorkload("wl", "ns").
				RequeueState(nil, ptr.To(metav1.NewTime(now.Add(10*time.Second)))).
				AdmissionCheck(kueue.AdmissionCheckState{
					Name:                "check1",
					State:               kueue.CheckStateRetry,
					RequeueAfterSeconds: ptr.To(int32(5)),
					LastTransitionTime:  metav1.NewTime(now),
				}).
				AdmissionCheck(kueue.AdmissionCheckState{
					Name:                "check2",
					State:               kueue.CheckStateRetry,
					RequeueAfterSeconds: ptr.To(int32(7)),
					LastTransitionTime:  metav1.NewTime(now),
				}).
				Obj(),
			wantWorkloadUseMergePatch: utiltestingapi.MakeWorkload("wl", "ns").
				RequeueState(nil, ptr.To(metav1.NewTime(now.Add(10*time.Second)))).
				AdmissionCheck(kueue.AdmissionCheckState{
					Name:                "check1",
					State:               kueue.CheckStateRetry,
					RequeueAfterSeconds: ptr.To(int32(5)),
					LastTransitionTime:  metav1.NewTime(now),
				}).
				AdmissionCheck(kueue.AdmissionCheckState{
					Name:                "check2",
					State:               kueue.CheckStateRetry,
					RequeueAfterSeconds: ptr.To(int32(7)),
					LastTransitionTime:  metav1.NewTime(now),
				}).
				Obj(),
			wantResult: reconcile.Result{},
		},
		"should use the biggest total time not the biggest RequeueAfterSeconds": {
			workload: utiltestingapi.MakeWorkload("wl", "ns").
				AdmissionCheck(kueue.AdmissionCheckState{
					Name:                "check1",
					State:               kueue.CheckStateRetry,
					RequeueAfterSeconds: ptr.To(int32(5)),
					LastTransitionTime:  metav1.NewTime(now.Add(10 * time.Second)),
				}).
				AdmissionCheck(kueue.AdmissionCheckState{
					Name:                "check2",
					State:               kueue.CheckStateRetry,
					RequeueAfterSeconds: ptr.To(int32(10)),
					LastTransitionTime:  metav1.NewTime(now),
				}).
				Obj(),
			wantWorkload: utiltestingapi.MakeWorkload("wl", "ns").
				RequeueState(nil, ptr.To(metav1.NewTime(now.Add(15*time.Second)))).
				AdmissionCheck(kueue.AdmissionCheckState{
					Name:                "check1",
					State:               kueue.CheckStateRetry,
					RequeueAfterSeconds: ptr.To(int32(5)),
					LastTransitionTime:  metav1.NewTime(now.Add(10 * time.Second)),
				}).
				AdmissionCheck(kueue.AdmissionCheckState{
					Name:                "check2",
					State:               kueue.CheckStateRetry,
					RequeueAfterSeconds: ptr.To(int32(10)),
					LastTransitionTime:  metav1.NewTime(now),
				}).
				Obj(),
			wantWorkloadUseMergePatch: utiltestingapi.MakeWorkload("wl", "ns").
				RequeueState(nil, ptr.To(metav1.NewTime(now.Add(15*time.Second)))).
				AdmissionCheck(kueue.AdmissionCheckState{
					Name:                "check1",
					State:               kueue.CheckStateRetry,
					RequeueAfterSeconds: ptr.To(int32(5)),
					LastTransitionTime:  metav1.NewTime(now.Add(10 * time.Second)),
				}).
				AdmissionCheck(kueue.AdmissionCheckState{
					Name:                "check2",
					State:               kueue.CheckStateRetry,
					RequeueAfterSeconds: ptr.To(int32(10)),
					LastTransitionTime:  metav1.NewTime(now),
				}).
				Obj(),
			wantResult: reconcile.Result{},
		},
	}
	for name, tc := range cases {
		for _, enabled := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s WorkloadRequestUseMergePatch enabled: %t", name, enabled), func(t *testing.T) {
				features.SetFeatureGateDuringTest(t, features.DynamicResourceAllocation, tc.enableDRAFeature)
				features.SetFeatureGateDuringTest(t, features.WorkloadRequestUseMergePatch, enabled)

				testWl := tc.workload.DeepCopy()
				objs := []client.Object{testWl}
				for _, rc := range tc.resourceClaims {
					objs = append(objs, rc)
				}

				for _, rct := range tc.resourceClaimTemplates {
					objs = append(objs, rct)
				}

				clientBuilder := utiltesting.NewClientBuilder().WithObjects(objs...).WithStatusSubresource(objs...).WithInterceptorFuncs(interceptor.Funcs{SubResourcePatch: utiltesting.TreatSSAAsStrategicMerge})
				cl := clientBuilder.Build()
				recorder := &utiltesting.EventRecorder{}

				cqCache := schdcache.New(cl)
				qManager := qcache.NewManagerForUnitTests(cl, cqCache)
				reconciler := NewWorkloadReconciler(cl, qManager, cqCache, recorder, tc.reconcilerOpts...)
				// use a fake clock with jitter = 0 to be able to assert on the requeueAt.
				reconciler.clock = fakeClock

				ctxWithLogger, _ := utiltesting.ContextWithLog(t)
				ctx, ctxCancel := context.WithCancel(ctxWithLogger)
				defer ctxCancel()

				if tc.cq != nil {
					testCq := tc.cq.DeepCopy()
					if err := cl.Create(ctx, testCq); err != nil {
						t.Errorf("couldn't create the cluster queue: %v", err)
					}
					if err := qManager.AddClusterQueue(ctx, testCq); err != nil {
						t.Errorf("couldn't add the cluster queue to the cache: %v", err)
					}
				}

				if tc.lq != nil {
					testLq := tc.lq.DeepCopy()
					if err := cl.Create(ctx, testLq); err != nil {
						t.Errorf("couldn't create the local queue: %v", err)
					}
					if err := qManager.AddLocalQueue(ctx, testLq); err != nil {
						t.Errorf("couldn't add the local queue to the cache: %v", err)
					}
				}

				if testWl != nil && testWl.Namespace == "ns" &&
					len(testWl.Spec.PodSets) > 0 &&
					len(testWl.Spec.PodSets[0].Template.Spec.ResourceClaims) > 0 {
					draConfig := []configapi.DeviceClassMapping{
						{
							Name:             corev1.ResourceName("foo"),
							DeviceClassNames: []corev1.ResourceName{"foo.example.com"},
						},
						{
							Name:             corev1.ResourceName("gpu"),
							DeviceClassNames: []corev1.ResourceName{"gpu.example.com"},
						},
					}
					err := dra.CreateMapperFromConfiguration(draConfig)
					if err != nil {
						t.Fatalf("Failed to initialize DRA mapper: %v", err)
					}
				}

				gotResult, gotError := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(testWl)})

				switch {
				case tc.wantError != nil:
					if gotError == nil {
						t.Errorf("expected error %v, got nil", tc.wantError)
					} else if !stderrors.Is(gotError, tc.wantError) {
						t.Errorf("unexpected error type: want %v, got %v", tc.wantError, gotError)
					}
				case tc.wantErrorMsg != "":
					if gotError == nil {
						t.Errorf("expected error containing %q, got nil", tc.wantErrorMsg)
					} else if !strings.Contains(gotError.Error(), tc.wantErrorMsg) {
						t.Errorf("expected error containing %q, got %v", tc.wantErrorMsg, gotError)
					}
				case gotError != nil:
					t.Errorf("unexpected error: %v", gotError)
				}

				if diff := cmp.Diff(tc.wantResult, gotResult); diff != "" {
					t.Errorf("unexpected reconcile result (-want/+got):\n%s", diff)
				}

				if tc.wantWorkload != nil {
					gotWorkload := &kueue.Workload{}
					if err := cl.Get(ctx, client.ObjectKeyFromObject(testWl), gotWorkload); err != nil {
						if !errors.IsNotFound(err) {
							t.Fatalf("Could not get Workloads after reconcile: %v", err)
						}
						t.Fatalf("expected workload to persist")
					}
					if features.Enabled(features.WorkloadRequestUseMergePatch) && tc.wantWorkloadUseMergePatch != nil {
						if diff := cmp.Diff(tc.wantWorkloadUseMergePatch, gotWorkload, workloadCmpOpts...); diff != "" {
							t.Errorf("Workloads after reconcile (-want,+got):\n%s", diff)
						}
					} else {
						if diff := cmp.Diff(tc.wantWorkload, gotWorkload, workloadCmpOpts...); diff != "" {
							t.Errorf("Workloads after reconcile (-want,+got):\n%s", diff)
						}
					}
				}
				if diff := cmp.Diff(tc.wantEvents, recorder.RecordedEvents); diff != "" {
					t.Errorf("unexpected events (-want/+got):\n%s", diff)
				}

				// For DRA tests, verify that workloads are properly queued/cached
				if tc.enableDRAFeature && testWl != nil &&
					len(testWl.Spec.PodSets) > 0 &&
					len(testWl.Spec.PodSets[0].Template.Spec.ResourceClaims) > 0 {
					workloadKey := client.ObjectKeyFromObject(testWl)

					if cqName, found := qManager.ClusterQueueFromLocalQueue(utilqueue.KeyFromWorkload(testWl)); found {
						pendingWorkloads := qManager.PendingWorkloadsInfo(cqName)

						if tc.wantWorkloadsInQueue != nil {
							if len(pendingWorkloads) != *tc.wantWorkloadsInQueue {
								t.Errorf("Expected exactly %d workload(s) in queue, got %d workloads", *tc.wantWorkloadsInQueue, len(pendingWorkloads))
								for i, wl := range pendingWorkloads {
									t.Logf("Workload %d: %s/%s", i, wl.Obj.Namespace, wl.Obj.Name)
								}
							}
						}

						var foundInQueue bool
						for _, wlInfo := range pendingWorkloads {
							if wlInfo.Obj.Name == workloadKey.Name && wlInfo.Obj.Namespace == workloadKey.Namespace {
								foundInQueue = true
								if len(tc.resourceClaimTemplates) > 0 && wlInfo.TotalRequests != nil {
									t.Logf("DRA workload found in queue with TotalRequests: %+v", wlInfo.TotalRequests)

									if tc.wantDRAResourceTotal != nil {
										if len(wlInfo.TotalRequests) > 0 && wlInfo.TotalRequests[0].Requests != nil {
											if gpuVal, hasGPU := wlInfo.TotalRequests[0].Requests["gpu"]; hasGPU {
												if gpuVal != *tc.wantDRAResourceTotal {
													t.Errorf("Expected gpu resource total to be %d, got %d", *tc.wantDRAResourceTotal, gpuVal)
												}
											} else {
												t.Errorf("Expected gpu resource in DRA workload TotalRequests, but not found")
											}
										} else {
											t.Errorf("Expected TotalRequests with DRA resources, but TotalRequests is empty")
										}
									}
								}
								break
							}
						}
						if tc.wantWorkloadsInQueue != nil && !foundInQueue {
							t.Errorf("DRA workload not found in queue - expected to be queued for processing")
						}
					} else {
						t.Errorf("LocalQueue not found in queue manager - DRA workload should have been queued")
					}
				}
			})
		}
	}
}

type notification struct {
	Cq kueue.ClusterQueueReference
	Lq kueue.LocalQueueName
}

type workloadUpdateWatcherMock struct {
	qManager              *qcache.Manager
	notificationsRecorded []notification
	testErrors            []error
}

func mockWorkloadUpdateWatcher(qManager *qcache.Manager) *workloadUpdateWatcherMock {
	return &workloadUpdateWatcherMock{
		qManager:              qManager,
		notificationsRecorded: []notification{},
		testErrors:            []error{},
	}
}

func (w *workloadUpdateWatcherMock) NotifyWorkloadUpdate(oldWl, newWl *kueue.Workload) {
	if oldWl == nil && newWl == nil {
		err := stderrors.New("Both workloads were nil")
		w.testErrors = append(w.testErrors, err)
		return
	}

	if newWl != nil {
		err := fmt.Errorf("Illegal new workload in delete request: want nil, got %v", newWl)
		w.testErrors = append(w.testErrors, err)
		return
	}

	n := notification{
		Lq: oldWl.Spec.QueueName,
	}
	if oldWl.Status.Admission != nil {
		n.Cq = oldWl.Status.Admission.ClusterQueue
	} else if cq, ok := w.qManager.ClusterQueueForWorkload(oldWl); ok {
		n.Cq = cq
	}

	w.notificationsRecorded = append(w.notificationsRecorded, n)
}

func TestWorkloadDeletion(t *testing.T) {
	now := time.Now().Truncate(time.Second)

	wlName := "wl"
	wlNs := "ns"

	pendingWl := utiltestingapi.MakeWorkload(wlName, wlNs).Queue("lq1").Obj()
	admittedWl := utiltestingapi.MakeWorkload(wlName, wlNs).Queue("lq1").ReserveQuotaAt(&kueue.Admission{
		ClusterQueue: "cq1",
	}, now).Condition(metav1.Condition{
		Type:   kueue.WorkloadPodsReady,
		Status: metav1.ConditionFalse,
	}).Obj()

	admittedWlWithDifferentQueues := utiltestingapi.MakeWorkload(wlName, wlNs).Queue("lq2").ReserveQuotaAt(&kueue.Admission{
		ClusterQueue: "cq2",
	}, now).Condition(metav1.Condition{
		Type:   kueue.WorkloadPodsReady,
		Status: metav1.ConditionFalse,
	}).Obj()

	noiseWlPending := utiltestingapi.MakeWorkload("other-wl", "other-ns").Queue("lq3").Obj()
	noiseWlAdmitted := utiltestingapi.MakeWorkload("other-wl", "other-ns").Queue("lq3").ReserveQuotaAt(&kueue.Admission{
		ClusterQueue: "cq3",
	}, now).Condition(metav1.Condition{
		Type:   kueue.WorkloadPodsReady,
		Status: metav1.ConditionFalse,
	}).Obj()

	clusterQueues := []*kueue.ClusterQueue{
		utiltestingapi.MakeClusterQueue("cq1").Obj(),
		utiltestingapi.MakeClusterQueue("cq2").Obj(),
		utiltestingapi.MakeClusterQueue("cq3").Obj(),
	}

	localQueues := []*kueue.LocalQueue{
		utiltestingapi.MakeLocalQueue("lq1", wlNs).ClusterQueue("cq1").Obj(),
		utiltestingapi.MakeLocalQueue("lq2", wlNs).ClusterQueue("cq2").Obj(),
		utiltestingapi.MakeLocalQueue("lq3", "other-ns").ClusterQueue("cq3").Obj(),
	}

	cases := map[string]struct {
		wlInQueueCache    *kueue.Workload
		wlInSchedCache    *kueue.Workload
		wantNotifications []notification
	}{
		"no workloads in either cache": {
			wlInQueueCache:    nil,
			wlInSchedCache:    nil,
			wantNotifications: []notification{},
		},
		"workload only in queue cache (doesn't have admissions)": {
			wlInQueueCache: pendingWl,
			wlInSchedCache: nil,
			wantNotifications: []notification{{
				Cq: "cq1",
				Lq: "lq1",
			}},
		},
		"workload only in scheduler cache": {
			wlInQueueCache: nil,
			wlInSchedCache: admittedWl,
			wantNotifications: []notification{{
				Cq: "cq1",
				Lq: "lq1",
			}},
		},
		"workload present in both caches with same local queue": {
			wlInQueueCache: pendingWl,
			wlInSchedCache: admittedWl,
			wantNotifications: []notification{
				{
					Cq: "cq1",
					Lq: "lq1",
				},
			},
		},
		"workload present with different queues in each cache": {
			wlInQueueCache: pendingWl,
			wlInSchedCache: admittedWlWithDifferentQueues,
			wantNotifications: []notification{
				{
					Cq: "cq2",
					Lq: "lq2",
				},
				{
					Cq: "cq1",
					Lq: "lq1",
				},
			},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			clientBuilder := utiltesting.NewClientBuilder().WithInterceptorFuncs(interceptor.Funcs{SubResourcePatch: utiltesting.TreatSSAAsStrategicMerge})

			cl := clientBuilder.Build()
			recorder := &utiltesting.EventRecorder{}
			cqCache := schdcache.New(cl)
			qManager := qcache.NewManagerForUnitTests(cl, cqCache)

			mockWatcher := mockWorkloadUpdateWatcher(qManager)

			reconciler := NewWorkloadReconciler(cl, qManager, cqCache, recorder)
			reconciler.watchers = []WorkloadUpdateWatcher{mockWatcher}

			ctxWithLogger, log := utiltesting.ContextWithLog(t)
			ctx, ctxCancel := context.WithCancel(ctxWithLogger)
			defer ctxCancel()

			for _, cq := range clusterQueues {
				cqCopy := cq.DeepCopy()
				if err := stderrors.Join(cqCache.AddClusterQueue(ctx, cqCopy), qManager.AddClusterQueue(ctx, cqCopy)); err != nil {
					t.Errorf("couldn't add the cluster queue: %v", err)
				}
			}
			for _, lq := range localQueues {
				lqCopy := lq.DeepCopy()
				if err := stderrors.Join(cqCache.AddLocalQueue(lqCopy), qManager.AddLocalQueue(ctx, lqCopy)); err != nil {
					t.Errorf("couldn't add the local queue: %v", err)
				}
			}

			addToQueueCache := []*kueue.Workload{noiseWlPending}
			if tc.wlInQueueCache != nil {
				addToQueueCache = append(addToQueueCache, tc.wlInQueueCache)
			}
			for _, wl := range addToQueueCache {
				if err := qManager.AddOrUpdateWorkload(log, wl.DeepCopy()); err != nil {
					t.Errorf("couldn't add workload to queue cache: %v", err)
				}
			}

			addToSchedCache := []*kueue.Workload{noiseWlAdmitted}
			if tc.wlInSchedCache != nil {
				addToSchedCache = append(addToSchedCache, tc.wlInSchedCache)
			}
			for _, wl := range addToSchedCache {
				if !cqCache.AddOrUpdateWorkload(log, wl.DeepCopy()) {
					t.Errorf("couldn't add workload to scheduler cache: %v", wl)
				}
			}

			reconciler.deleteWorkloadFromCaches(ctx, wlNs, wlName)

			if testError := stderrors.Join(mockWatcher.testErrors...); testError != nil {
				t.Error(testError)
			}

			if diff := cmp.Diff(tc.wantNotifications, mockWatcher.notificationsRecorded); diff != "" {
				t.Errorf("Incorrect notifications recorded (-want,+got):\n%s", diff)
			}

			wlRef := workload.NewReference(wlNs, wlName)
			if qManager.GetWorkloadFromCache(wlRef) != nil {
				t.Error("Workload still present in the queue cache")
			}
			if cqCache.GetWorkloadFromCache(wlRef) != nil {
				t.Error("Workload still present in the scheduler cache")
			}
		})
	}
}
