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
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	batchv1 "k8s.io/api/batch/v1"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	testingclock "k8s.io/utils/clock/testing"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
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
		podsReadyTimeout           *time.Duration
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
			podsReadyTimeout:           ptr.To(5 * time.Minute),
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
			podsReadyTimeout:           ptr.To(5 * time.Minute),
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
			podsReadyTimeout:           ptr.To(5 * time.Minute),
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
			podsReadyTimeout: ptr.To(5 * time.Minute),
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
			podsReadyTimeout: ptr.To(5 * time.Minute),
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
			podsReadyTimeout: ptr.To(5 * time.Minute),
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			wRec := WorkloadReconciler{podsReadyTimeout: tc.podsReadyTimeout}
			countingTowardsTimeout, recheckAfter := wRec.admittedNotReadyWorkload(&tc.workload, fakeClock)

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
			gotStates, gotShouldChange := syncAdmissionCheckConditions(tc.states, tc.list)

			if tc.wantChange != gotShouldChange {
				t.Errorf("Unexpected should change, want=%v", tc.wantChange)
			}

			opts := []cmp.Option{}
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
			kueue.Workload{}, "TypeMeta", "ObjectMeta.ResourceVersion", "Status.RequeueState.RequeueAt",
		),
		cmpopts.IgnoreFields(metav1.Condition{}, "LastTransitionTime"),
		cmpopts.SortSlices(func(a, b metav1.Condition) bool { return a.Type < b.Type }),
	}
)

func TestReconcile(t *testing.T) {
	testStartTime := time.Now()
	cases := map[string]struct {
		workload       *kueue.Workload
		wantWorkload   *kueue.Workload
		wantError      error
		wantEvents     []utiltesting.EventRecord
		reconcilerOpts []Option
	}{
		"admit": {
			workload: utiltesting.MakeWorkload("wl", "ns").
				ReserveQuota(utiltesting.MakeAdmission("q1").Obj()).
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
		"unadmitted workload with rejected checks": {
			workload: utiltesting.MakeWorkload("wl", "ns").
				ControllerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "ownername", "owneruid").
				AdmissionCheck(kueue.AdmissionCheckState{
					Name:  "check",
					State: kueue.CheckStateRejected,
				}).
				Obj(),
			wantWorkload: utiltesting.MakeWorkload("wl", "ns").
				ControllerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "ownername", "owneruid").
				AdmissionCheck(kueue.AdmissionCheckState{
					Name:  "check",
					State: kueue.CheckStateRejected,
				}).
				Condition(metav1.Condition{
					Type:    "Finished",
					Status:  "True",
					Reason:  "AdmissionChecksRejected",
					Message: "Admission checks [check] are rejected",
				}).
				Obj(),
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Namespace: "ns", Name: "ownername"},
					EventType: "Normal",
					Reason:    "WorkloadFinished",
				},
			},
		},
		"admitted workload with rejected checks": {
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
				Condition(metav1.Condition{
					Type:    "Evicted",
					Status:  "True",
					Reason:  "AdmissionCheck",
					Message: "At least one admission check is false",
				}).
				Obj(),
		},
		"increment re-queue count": {
			reconcilerOpts: []Option{
				WithPodsReadyTimeout(ptr.To(3 * time.Second)),
				WithRequeuingBackoffLimitCount(ptr.To[int32](100)),
				WithRequeuingBaseDelaySeconds(10),
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
				Obj(),
			wantWorkload: utiltesting.MakeWorkload("wl", "ns").
				ReserveQuota(utiltesting.MakeAdmission("q1").Obj()).
				Admitted(true).
				AdmissionCheck(kueue.AdmissionCheckState{
					Name:  "check",
					State: kueue.CheckStateReady,
				}).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadEvicted,
					Status:  metav1.ConditionTrue,
					Reason:  kueue.WorkloadEvictedByPodsReadyTimeout,
					Message: "Exceeded the PodsReady timeout ns/wl",
				}).
				// 10s * 2^(4-1) = 80s
				RequeueState(ptr.To[int32](4), ptr.To(metav1.NewTime(testStartTime.Add(80*time.Second).Truncate(time.Second)))).
				Obj(),
		},
		"deactivated workload": {
			reconcilerOpts: []Option{
				WithPodsReadyTimeout(ptr.To(3 * time.Second)),
				WithRequeuingBackoffLimitCount(ptr.To[int32](1)),
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
				Active(false).
				ReserveQuota(utiltesting.MakeAdmission("q1").Obj()).
				Admitted(true).
				AdmissionCheck(kueue.AdmissionCheckState{
					Name:  "check",
					State: kueue.CheckStateReady,
				}).
				RequeueState(ptr.To[int32](1), ptr.To(metav1.NewTime(testStartTime.Add(1*time.Second).Truncate(time.Second)))).
				Obj(),
			wantEvents: []utiltesting.EventRecord{{
				Key:       types.NamespacedName{Name: "wl", Namespace: "ns"},
				EventType: v1.EventTypeNormal,
				Reason:    kueue.WorkloadEvictedByDeactivation,
				Message:   "Deactivated Workload \"ns/wl\" by reached re-queue backoffLimitCount",
			}},
		},
		"should set the Evicted condition with InactiveWorkload reason when the .spec.active=False": {
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
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			objs := []client.Object{tc.workload}
			clientBuilder := utiltesting.NewClientBuilder().WithObjects(objs...).WithStatusSubresource(objs...)
			cl := clientBuilder.Build()
			recorder := &utiltesting.EventRecorder{}

			cqCache := cache.New(cl)
			qManager := queue.NewManager(cl, cqCache)
			reconciler := NewWorkloadReconciler(cl, qManager, cqCache, recorder, tc.reconcilerOpts...)

			ctxWithLogger, _ := utiltesting.ContextWithLog(t)
			ctx, ctxCancel := context.WithCancel(ctxWithLogger)
			defer ctxCancel()

			_, gotError := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(tc.workload)})

			if diff := cmp.Diff(tc.wantError, gotError); diff != "" {
				t.Errorf("unexpected reconcile error (-want/+got):\n%s", diff)
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

			if tc.wantWorkload != nil {
				if requeueState := tc.wantWorkload.Status.RequeueState; requeueState != nil && requeueState.RequeueAt != nil {
					gotRequeueState := gotWorkload.Status.RequeueState
					if gotRequeueState != nil && gotRequeueState.RequeueAt != nil {
						// We verify the got requeueAt if the got requeueAt is after the desired requeueAt
						// since the requeueAt is included in positive seconds of random jitter.
						// Additionally, we need to verify the requeueAt by "Equal" function
						// as the "After" function evaluates the nanoseconds despite the metav1.Time is seconds level precision.
						if !gotRequeueState.RequeueAt.After(requeueState.RequeueAt.Time) && !gotRequeueState.RequeueAt.Equal(requeueState.RequeueAt) {
							t.Errorf("Unexpected requeueState.requeueAt; gotRequeueAt %v needs to be after requeueAt %v", requeueState.RequeueAt, gotRequeueState.RequeueAt)
						}
					} else {
						t.Errorf("Unexpected nil requeueState.requeuAt; requeueState.requeueAt shouldn't be nil")
					}
				}
			}

			if diff := cmp.Diff(tc.wantEvents, recorder.RecordedEvents, cmpopts.IgnoreFields(utiltesting.EventRecord{}, "Message")); diff != "" {
				t.Errorf("unexpected events (-want/+got):\n%s", diff)
			}
		})
	}
}
