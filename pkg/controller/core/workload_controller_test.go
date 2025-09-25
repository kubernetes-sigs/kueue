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
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	resourcev1beta2 "k8s.io/api/resource/v1beta2"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	testingclock "k8s.io/utils/clock/testing"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	qcache "sigs.k8s.io/kueue/pkg/cache/queue"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/dra"
	"sigs.k8s.io/kueue/pkg/features"
	utilqueue "sigs.k8s.io/kueue/pkg/util/queue"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
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
					Name:               "ac0",
					State:              kueue.CheckStateReady,
					Message:            "Message one",
					LastTransitionTime: metav1.NewTime(now),
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
					Name:               "ac0",
					State:              kueue.CheckStateReady,
					Message:            "Message one",
					LastTransitionTime: metav1.NewTime(now),
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
	testStartTime := time.Now().Truncate(time.Second)
	fakeClock := testingclock.NewFakeClock(testStartTime)

	cases := map[string]struct {
		enableObjectRetentionPolicies bool
		enableDRAFeature              bool

		workload                  *kueue.Workload
		cq                        *kueue.ClusterQueue
		lq                        *kueue.LocalQueue
		resourceClaims            []*resourcev1beta2.ResourceClaim
		resourceClaimTemplates    []*resourcev1beta2.ResourceClaimTemplate
		wantDRAResourceTotal      *int64
		wantWorkloadsInQueue      *int
		wantWorkload              *kueue.Workload
		wantWorkloadUseMergePatch *kueue.Workload // workload version to compensate for the difference between use of Apply and Merge patch in FakeClient
		wantError                 error
		wantEvents                []utiltesting.EventRecord
		wantResult                reconcile.Result
		reconcilerOpts            []Option
	}{
		"reconcile DRA ResourceClaim should be rejected as inadmissible": {
			enableDRAFeature: true,
			workload: utiltesting.MakeWorkload("wlWithDRAResourceClaim", "ns").
				Queue("lq").
				PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 1).
					ResourceClaim("gpu", "rc1").
					Obj()).
				Obj(),
			resourceClaims: []*resourcev1beta2.ResourceClaim{{
				ObjectMeta: metav1.ObjectMeta{Name: "rc1", Namespace: "ns"},
				Spec: resourcev1beta2.ResourceClaimSpec{
					Devices: resourcev1beta2.DeviceClaim{
						Requests: []resourcev1beta2.DeviceRequest{{
							Exactly: &resourcev1beta2.ExactDeviceRequest{
								DeviceClassName: "gpu.example.com",
								AllocationMode:  resourcev1beta2.DeviceAllocationModeExactCount,
								Count:           1,
							},
						}},
					},
				},
			}},
			cq: utiltesting.MakeClusterQueue("cq").
				ResourceGroup(
					*utiltesting.MakeFlavorQuotas("flavor1").
						Resource("gpus", "2").Obj(),
				).Obj(),
			lq: utiltesting.MakeLocalQueue("lq", "ns").ClusterQueue("cq").Obj(),
			wantWorkload: utiltesting.MakeWorkload("wlWithDRAResourceClaim", "ns").
				Queue("lq").
				PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 1).
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
			workload: utiltesting.MakeWorkload("wlWithDRAResourceClaimTemplate", "ns").
				Queue("lq").
				PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 1).
					ResourceClaimTemplate("gpu", "gpu-template").
					Obj()).
				Obj(),
			resourceClaimTemplates: []*resourcev1beta2.ResourceClaimTemplate{{
				ObjectMeta: metav1.ObjectMeta{Name: "gpu-template", Namespace: "ns"},
				Spec: resourcev1beta2.ResourceClaimTemplateSpec{
					Spec: resourcev1beta2.ResourceClaimSpec{
						Devices: resourcev1beta2.DeviceClaim{
							Requests: []resourcev1beta2.DeviceRequest{{
								Name: "gpu-request",
								Exactly: &resourcev1beta2.ExactDeviceRequest{
									DeviceClassName: "gpu.example.com",
									AllocationMode:  resourcev1beta2.DeviceAllocationModeExactCount,
									Count:           1,
								},
							}},
						},
					},
				},
			}},
			cq: utiltesting.MakeClusterQueue("cq").
				ResourceGroup(
					*utiltesting.MakeFlavorQuotas("flavor1").
						Resource("gpu", "2").Obj(),
				).Obj(),
			lq: utiltesting.MakeLocalQueue("lq", "ns").ClusterQueue("cq").Obj(),
			wantWorkload: utiltesting.MakeWorkload("wlWithDRAResourceClaimTemplate", "ns").
				Queue("lq").
				PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 1).
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
			workload: utiltesting.MakeWorkload("wlMultiPodDRA", "ns").
				Queue("lq").
				PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 3).
					ResourceClaimTemplate("gpu", "gpu-template").
					Obj()).
				Obj(),
			resourceClaimTemplates: []*resourcev1beta2.ResourceClaimTemplate{{
				ObjectMeta: metav1.ObjectMeta{Name: "gpu-template", Namespace: "ns"},
				Spec: resourcev1beta2.ResourceClaimTemplateSpec{
					Spec: resourcev1beta2.ResourceClaimSpec{
						Devices: resourcev1beta2.DeviceClaim{
							Requests: []resourcev1beta2.DeviceRequest{{
								Name: "gpu-request",
								Exactly: &resourcev1beta2.ExactDeviceRequest{
									DeviceClassName: "gpu.example.com",
									AllocationMode:  resourcev1beta2.DeviceAllocationModeExactCount,
									Count:           2,
								},
							}},
						},
					},
				},
			}},
			cq: utiltesting.MakeClusterQueue("cq").
				ResourceGroup(
					*utiltesting.MakeFlavorQuotas("flavor1").
						Resource("gpu", "10").Obj(),
				).Obj(),
			lq: utiltesting.MakeLocalQueue("lq", "ns").ClusterQueue("cq").Obj(),
			wantWorkload: utiltesting.MakeWorkload("wlMultiPodDRA", "ns").
				Queue("lq").
				PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 3).
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
			workload: utiltesting.MakeWorkload("wlUnmappedDRA", "ns").
				Queue("lq").
				PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 1).
					ResourceClaimTemplate("gpu", "gpu-template").
					Obj()).
				Obj(),
			resourceClaimTemplates: []*resourcev1beta2.ResourceClaimTemplate{{
				ObjectMeta: metav1.ObjectMeta{Name: "gpu-template", Namespace: "ns"},
				Spec: resourcev1beta2.ResourceClaimTemplateSpec{
					Spec: resourcev1beta2.ResourceClaimSpec{
						Devices: resourcev1beta2.DeviceClaim{
							Requests: []resourcev1beta2.DeviceRequest{{
								Name: "gpu-request",
								Exactly: &resourcev1beta2.ExactDeviceRequest{
									DeviceClassName: "unmapped.example.com",
									AllocationMode:  resourcev1beta2.DeviceAllocationModeExactCount,
									Count:           1,
								},
							}},
						},
					},
				},
			}},
			cq: utiltesting.MakeClusterQueue("cq").
				ResourceGroup(
					*utiltesting.MakeFlavorQuotas("flavor1").
						Resource("gpu", "2").Obj(),
				).Obj(),
			lq: utiltesting.MakeLocalQueue("lq", "ns").ClusterQueue("cq").Obj(),
			wantWorkload: func() *kueue.Workload {
				wl := utiltesting.MakeWorkload("wlUnmappedDRA", "ns").
					Queue("lq").
					PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 1).
						ResourceClaimTemplate("gpu", "gpu-template").
						Obj()).
					Condition(metav1.Condition{
						Type:    kueue.WorkloadQuotaReserved,
						Status:  metav1.ConditionFalse,
						Reason:  kueue.WorkloadInadmissible,
						Message: "DeviceClass unmapped.example.com is not mapped in DRA configuration for workload wlUnmappedDRA podset main: DeviceClass is not mapped in DRA configuration",
					}).
					Condition(metav1.Condition{
						Type:    kueue.WorkloadRequeued,
						Status:  metav1.ConditionFalse,
						Reason:  kueue.WorkloadInadmissible,
						Message: "DeviceClass unmapped.example.com is not mapped in DRA configuration for workload wlUnmappedDRA podset main: DeviceClass is not mapped in DRA configuration",
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
			wantError:  dra.ErrDeviceClassNotMapped,
			wantEvents: nil,
		},
		"reconcile DRA ResourceClaimTemplate not found should return error": {
			enableDRAFeature: true,
			workload: utiltesting.MakeWorkload("wlMissingTemplate", "ns").
				Queue("lq").
				PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 1).
					ResourceClaimTemplate("gpu", "missing-template").
					Obj()).
				Obj(),
			cq: utiltesting.MakeClusterQueue("cq").
				ResourceGroup(
					*utiltesting.MakeFlavorQuotas("flavor1").
						Resource("gpu", "2").Obj(),
				).Obj(),
			lq: utiltesting.MakeLocalQueue("lq", "ns").ClusterQueue("cq").Obj(),
			wantWorkload: utiltesting.MakeWorkload("wlMissingTemplate", "ns").
				Queue("lq").
				PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 1).
					ResourceClaimTemplate("gpu", "missing-template").
					Obj()).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadQuotaReserved,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadInadmissible,
					Message: `failed to get claim spec for ResourceClaimTemplate missing-template in workload wlMissingTemplate podset main: failed to get claim spec: resourceclaimtemplates.resource.k8s.io "missing-template" not found`,
				}).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadRequeued,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadInadmissible,
					Message: `failed to get claim spec for ResourceClaimTemplate missing-template in workload wlMissingTemplate podset main: failed to get claim spec: resourceclaimtemplates.resource.k8s.io "missing-template" not found`,
				}).
				Obj(),
			wantError:  dra.ErrClaimSpecNotFound,
			wantEvents: nil,
		},
		"assign Admission Checks from ClusterQueue.spec.AdmissionCheckStrategy": {
			workload: utiltesting.MakeWorkload("wl", "ns").
				ReserveQuota(utiltesting.MakeAdmission("cq").
					PodSets(utiltesting.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment("cpu", "flavor1", "1").
						Obj()).
					Obj()).
				Queue("queue").
				Obj(),
			cq: utiltesting.MakeClusterQueue("cq").
				AdmissionCheckStrategy(
					*utiltesting.MakeAdmissionCheckStrategyRule("ac1", "flavor1").Obj(),
					*utiltesting.MakeAdmissionCheckStrategyRule("ac2").Obj()).
				Obj(),
			lq: utiltesting.MakeLocalQueue("queue", "ns").ClusterQueue("cq").Obj(),
			wantWorkload: utiltesting.MakeWorkload("wl", "ns").
				ReserveQuota(utiltesting.MakeAdmission("cq").
					PodSets(utiltesting.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment("cpu", "flavor1", "1").
						Obj()).
					Obj()).
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
				ReserveQuota(utiltesting.MakeAdmission("cq").
					PodSets(utiltesting.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment("cpu", "flavor1", "1").
						Obj()).
					Obj()).
				Queue("queue").
				Obj(),
			cq: utiltesting.MakeClusterQueue("cq").
				AdmissionChecks("ac1", "ac2").
				Obj(),
			lq: utiltesting.MakeLocalQueue("queue", "ns").ClusterQueue("cq").Obj(),
			wantWorkload: utiltesting.MakeWorkload("wl", "ns").
				ReserveQuota(utiltesting.MakeAdmission("cq").
					PodSets(utiltesting.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment("cpu", "flavor1", "1").
						Obj()).
					Obj()).
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
			workload: utiltesting.MakeWorkload("wl", "ns").
				ReserveQuota(utiltesting.MakeAdmission("q1").Obj()).
				Admitted(true).
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
			wantWorkload: utiltesting.MakeWorkload("wl", "ns").
				ReserveQuota(utiltesting.MakeAdmission("q1").Obj()).
				Admitted(true).
				Active(false).
				ControllerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "ownername", "owneruid").
				AdmissionChecks(
					kueue.AdmissionCheckState{
						Name:    "check-1",
						State:   kueue.CheckStatePending,
						Message: "Reset to Pending after eviction. Previously: Rejected",
					},
					kueue.AdmissionCheckState{
						Name:    "check-2",
						State:   kueue.CheckStatePending,
						Message: "Reset to Pending after eviction. Previously: Retry",
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
			wantWorkloadUseMergePatch: utiltesting.MakeWorkload("wl", "ns").
				ReserveQuota(utiltesting.MakeAdmission("q1").Obj()).
				Admitted(true).
				Active(false).
				ControllerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "ownername", "owneruid").
				AdmissionChecks(
					kueue.AdmissionCheckState{
						Name:    "check-1",
						State:   kueue.CheckStatePending,
						Message: "Reset to Pending after eviction. Previously: Rejected",
					},
					kueue.AdmissionCheckState{
						Name:    "check-2",
						State:   kueue.CheckStatePending,
						Message: "Reset to Pending after eviction. Previously: Retry",
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
				Condition(metav1.Condition{
					Type:               kueue.WorkloadPodsReady,
					Status:             metav1.ConditionFalse,
					LastTransitionTime: metav1.NewTime(testStartTime.Add(-3 * time.Minute)),
					Reason:             kueue.WorkloadWaitForRecovery,
					Message:            "At least one pod has failed, waiting for recovery",
				}).
				Admitted(true).
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
				Condition(metav1.Condition{
					Type:               kueue.WorkloadPodsReady,
					Status:             metav1.ConditionFalse,
					LastTransitionTime: metav1.NewTime(testStartTime),
					Reason:             kueue.WorkloadWaitForRecovery,
					Message:            "At least one pod has failed, waiting for recovery",
				}).
				//  10s * 2^(11-1) = 10240s > requeuingBackoffMaxSeconds; then wait time should be limited to requeuingBackoffMaxSeconds
				RequeueState(ptr.To[int32](1), ptr.To(metav1.NewTime(testStartTime.Add(7200*time.Second).Truncate(time.Second)))).
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
			workload: utiltesting.MakeWorkload("wl", "ns").
				Active(true).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadRequeued,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadDeactivated,
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
		"should set the Evicted condition with Deactivated reason when the .spec.active=False": {
			workload: utiltesting.MakeWorkload("wl", "ns").Active(false).Obj(),
			wantWorkload: utiltesting.MakeWorkload("wl", "ns").
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
				SchedulingStatsEviction(
					kueue.WorkloadSchedulingStatsEviction{
						Reason:          kueue.WorkloadEvictedByPodsReadyTimeout,
						UnderlyingCause: kueue.WorkloadWaitForStart,
						Count:           1,
					},
				).
				Obj(),
			wantWorkload: utiltesting.MakeWorkload("wl", "ns").
				Active(false).
				ReserveQuota(utiltesting.MakeAdmission("q1").Obj()).
				Admitted(true).
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
			wantWorkloadUseMergePatch: utiltesting.MakeWorkload("wl", "ns").
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
			workload: utiltesting.MakeWorkload("wl", "ns").
				Active(false).
				ReserveQuota(utiltesting.MakeAdmission("q1").Obj()).
				Admitted(true).
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
			wantWorkload: utiltesting.MakeWorkload("wl", "ns").
				Active(false).
				ReserveQuota(utiltesting.MakeAdmission("q1").Obj()).
				Admitted(true).
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
			wantWorkloadUseMergePatch: utiltesting.MakeWorkload("wl", "ns").
				Active(false).
				ReserveQuota(utiltesting.MakeAdmission("q1").Obj()).
				Admitted(true).
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
			wantWorkloadUseMergePatch: utiltesting.MakeWorkload("wl", "ns").
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
			workload: utiltesting.MakeWorkload("wl", "ns").
				Active(false).
				ReserveQuota(utiltesting.MakeAdmission("q1").Obj()).
				Admitted(true).
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
			wantWorkload: utiltesting.MakeWorkload("wl", "ns").
				Active(false).
				ReserveQuota(utiltesting.MakeAdmission("q1").Obj()).
				Admitted(true).
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
			wantWorkloadUseMergePatch: utiltesting.MakeWorkload("wl", "ns").
				Active(false).
				ReserveQuota(utiltesting.MakeAdmission("q1").Obj()).
				Admitted(true).
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
				Admission(utiltesting.MakeAdmission("cq", kueue.DefaultPodSetName).Obj()).
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
			wantWorkloadUseMergePatch: utiltesting.MakeWorkload("wl", "ns").
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
				Admission(utiltesting.MakeAdmission("cq", kueue.DefaultPodSetName).Obj()).
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
			wantWorkloadUseMergePatch: utiltesting.MakeWorkload("wl", "ns").
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
				Admission(utiltesting.MakeAdmission("cq", kueue.DefaultPodSetName).Obj()).
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
			wantWorkloadUseMergePatch: utiltesting.MakeWorkload("wl", "ns").
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
				Admission(utiltesting.MakeAdmission("cq", kueue.DefaultPodSetName).Obj()).
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
			wantWorkloadUseMergePatch: utiltesting.MakeWorkload("wl", "ns").
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
		"shouldn't delete the workload because, object retention not configured": {
			enableObjectRetentionPolicies: true,
			workload: utiltesting.MakeWorkload("wl", "ns").
				Condition(metav1.Condition{
					Type:   kueue.WorkloadFinished,
					Status: metav1.ConditionTrue,
				}).
				Obj(),
			reconcilerOpts: []Option{
				WithWorkloadRetention(nil),
			},
			wantWorkload: utiltesting.MakeWorkload("wl", "ns").
				Condition(metav1.Condition{
					Type:   kueue.WorkloadFinished,
					Status: metav1.ConditionTrue,
				}).
				Obj(),
			wantError: nil,
		},
		"shouldn't try to delete the workload (no event emitted) because it is already being deleted by kubernetes, object retention configured": {
			enableObjectRetentionPolicies: true,
			workload: utiltesting.MakeWorkload("wl", "ns").
				Condition(metav1.Condition{
					Type:   kueue.WorkloadFinished,
					Status: metav1.ConditionTrue,
					LastTransitionTime: metav1.Time{
						Time: testStartTime,
					},
				}).
				DeletionTimestamp(testStartTime).
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
			enableObjectRetentionPolicies: true,
			workload: utiltesting.MakeWorkload("wl", "ns").
				Condition(metav1.Condition{
					Type:               kueue.WorkloadFinished,
					Status:             metav1.ConditionTrue,
					LastTransitionTime: metav1.NewTime(testStartTime.Add(-util.Timeout)),
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
			wantWorkload: utiltesting.MakeWorkload("wl", "ns").
				Condition(metav1.Condition{
					Type:               kueue.WorkloadFinished,
					Status:             metav1.ConditionTrue,
					LastTransitionTime: metav1.NewTime(testStartTime.Add(-util.Timeout)),
				}).
				Obj(),
			wantError: nil,
		},
		"should delete the workload because the retention period has elapsed, object retention configured": {
			enableObjectRetentionPolicies: true,
			workload: utiltesting.MakeWorkload("wl", "ns").
				Condition(metav1.Condition{
					Type:               kueue.WorkloadFinished,
					Status:             metav1.ConditionTrue,
					LastTransitionTime: metav1.NewTime(testStartTime.Add(-2 * util.LongTimeout)),
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
	}
	for name, tc := range cases {
		for _, enabled := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s WorkloadRequestUseMergePatch enabled: %t", name, enabled), func(t *testing.T) {
				features.SetFeatureGateDuringTest(t, features.ObjectRetentionPolicies, tc.enableObjectRetentionPolicies)
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
				qManager := qcache.NewManager(cl, cqCache)
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

				if tc.wantError != nil {
					if gotError == nil {
						t.Errorf("expected error %v, got nil", tc.wantError)
					} else if !stderrors.Is(gotError, tc.wantError) {
						t.Errorf("unexpected error type: want %v, got %v", tc.wantError, gotError)
					}
				} else if gotError != nil {
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
