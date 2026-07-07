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
	"maps"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	resourcev1 "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/api/errors"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/component-base/featuregate"
	testingclock "k8s.io/utils/clock/testing"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	qcache "sigs.k8s.io/kueue/pkg/cache/queue"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/constants"
	utilindexer "sigs.k8s.io/kueue/pkg/controller/core/indexer"
	"sigs.k8s.io/kueue/pkg/dra"
	"sigs.k8s.io/kueue/pkg/features"
	preemptexpectations "sigs.k8s.io/kueue/pkg/scheduler/preemption/expectations"
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

var (
	errTest = stderrors.New("test error")
)

type reconcileTestCase struct {
	featureGates map[featuregate.Feature]bool

	// Set to true to simulate a terminating ClusterQueue or LocalQueue.
	// The setup helper will attach a finalizer and delete the object,
	// which automatically populates its DeletionTimestamp in the fake client.
	shouldDeleteCQ bool
	shouldDeleteLQ bool

	workload                  *kueue.Workload
	additionalObjects         []client.Object
	cq                        *kueue.ClusterQueue
	lq                        *kueue.LocalQueue
	resourceClaims            []*resourcev1.ResourceClaim
	resourceClaimTemplates    []*resourcev1.ResourceClaimTemplate
	patchErr                  error
	listErr                   error
	wantDRAResourceTotal      *int64
	wantWorkloadsInQueue      *int
	wantWorkload              *kueue.Workload
	wantWorkloadUseMergePatch *kueue.Workload // workload version to compensate for the difference between use of Apply and Merge patch in FakeClient
	wantError                 error
	wantErrorMsg              string
	wantEvents                []utiltesting.EventRecord
	wantResult                reconcile.Result
	reconcilerOpts            []Option
	beforeReconcile           func(context.Context, client.Client, *qcache.Manager)
}

func TestUpdateSkipsRequeueForOnHoldWorkload(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	fakeClock := testingclock.NewFakeClock(now)

	oldWl := utiltestingapi.MakeWorkload("wl", "ns").
		Queue("lq").
		Active(true).
		ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").Obj(), now).
		AdmittedAt(true, now).
		Obj()
	newWl := utiltestingapi.MakeWorkload("wl", "ns").
		Queue("lq").
		Active(true).
		Condition(metav1.Condition{
			Type:    kueue.WorkloadQuotaReserved,
			Status:  metav1.ConditionFalse,
			Reason:  kueue.WorkloadOnHold,
			Message: "StatefulSet scaled to zero; workload on hold",
		}).
		Obj()

	cl := utiltesting.NewClientBuilder().Build()
	recorder := &utiltesting.EventRecorder{}
	cqCache := schdcache.New(cl)
	qManager := qcache.NewManagerForUnitTests(cl, cqCache, qcache.WithClock(fakeClock))
	reconciler := NewWorkloadReconciler(cl, qManager, cqCache, recorder)

	ctx, _ := utiltesting.ContextWithLog(t)

	setupClusterQueue(ctx, t, cl, qManager, cqCache, utiltestingapi.MakeClusterQueue("cq").Obj(), false)
	setupLocalQueue(ctx, t, cl, qManager, utiltestingapi.MakeLocalQueue("lq", "ns").ClusterQueue("cq").Obj(), false)

	if got := reconciler.Update(event.TypedUpdateEvent[*kueue.Workload]{
		ObjectOld: oldWl,
		ObjectNew: newWl,
	}); !got {
		t.Fatalf("Update() = %v, want true", got)
	}

	if pending := qManager.PendingWorkloadsInfo("cq"); len(pending) != 0 {
		t.Fatalf("expected no workloads in pending queue, got %d", len(pending))
	}

	fakeClock.Step(time.Second)

	headsCtx, headsCancel := context.WithTimeout(t.Context(), 50*time.Millisecond)
	defer headsCancel()
	go qManager.CleanUpOnContext(headsCtx)

	if heads := qManager.Heads(headsCtx); len(heads) != 0 {
		t.Fatalf("expected no second-pass workloads, got %d", len(heads))
	}
}

func TestReconcile(t *testing.T) {
	// the clock is primarily used with second rounded times
	// use the current time trimmed.
	now := time.Now().Truncate(time.Second)
	fakeClock := testingclock.NewFakeClock(now)

	cases := map[string]reconcileTestCase{
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
		"remove finalizer for deleted orphaned workload with FinishOrphanedWorkloads enabled": {
			featureGates: map[featuregate.Feature]bool{features.FinishOrphanedWorkloads: true},
			workload: utiltestingapi.MakeWorkload("unit-test", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
				DeletionTimestamp(now).
				Obj(),
			wantWorkload: nil,
		},
		"remove finalizer for deleted orphaned workload with FinishOrphanedWorkloads disabled": {
			featureGates: map[featuregate.Feature]bool{features.FinishOrphanedWorkloads: false},
			workload: utiltestingapi.MakeWorkload("unit-test", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
				DeletionTimestamp(now).
				Obj(),
			wantWorkload: nil,
		},
		"remove finalizer for orphaned workload with JobUID label and FinishOrphanedWorkloads enabled": {
			featureGates: map[featuregate.Feature]bool{features.FinishOrphanedWorkloads: true},
			workload: utiltestingapi.MakeWorkload("unit-test", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
				JobUID("job_uid").
				Obj(),
			wantWorkload: nil,
		},
		"remove finalizer for orphaned workload with JobUID label and FinishOrphanedWorkloads disabled": {
			featureGates: map[featuregate.Feature]bool{features.FinishOrphanedWorkloads: false},
			workload: utiltestingapi.MakeWorkload("unit-test", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
				JobUID("job_uid").
				Condition(metav1.Condition{
					Type:   "Finished",
					Status: "True",
				}).
				Obj(),
			wantWorkload: utiltestingapi.MakeWorkload("unit-test", "ns").Finalizers(kueue.ResourceInUseFinalizerName).
				JobUID("job_uid").
				Condition(metav1.Condition{
					Type:   "Finished",
					Status: "True",
				}).
				Obj(),
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
		"should handle finished workload logic for orphaned workloads when FinishOrphanedWorkloads enabled": {
			featureGates: map[featuregate.Feature]bool{features.FinishOrphanedWorkloads: true},
			workload: utiltestingapi.MakeWorkload("wl", "ns").
				Finalizers(kueue.ResourceInUseFinalizerName).
				JobUID("job_uid").
				Obj(),
			reconcilerOpts: []Option{
				WithWorkloadRetention(
					&workloadRetentionConfig{
						afterFinished: new(util.MediumTimeout),
					},
				),
			},
			wantWorkload: utiltestingapi.MakeWorkload("wl", "ns").
				JobUID("job_uid").
				Condition(metav1.Condition{
					Type:    kueue.WorkloadFinished,
					Status:  metav1.ConditionTrue,
					Reason:  kueue.WorkloadFinishedReasonOwnerNotFound,
					Message: "The workload's owner no longer exists",
				}).
				Obj(),
			wantError: nil,
			wantResult: reconcile.Result{
				RequeueAfter: util.MediumTimeout,
			},
		},
		"shouldn't handle finished workload logic for orphaned workloads on error when FinishOrphanedWorkloads enabled": {
			featureGates: map[featuregate.Feature]bool{features.FinishOrphanedWorkloads: true},
			workload: utiltestingapi.MakeWorkload("wl", "ns").
				Finalizers(kueue.ResourceInUseFinalizerName).
				JobUID("job_uid").
				Obj(),
			reconcilerOpts: []Option{
				WithWorkloadRetention(
					&workloadRetentionConfig{
						afterFinished: new(util.MediumTimeout),
					},
				),
			},
			patchErr: errTest,
			wantWorkload: utiltestingapi.MakeWorkload("wl", "ns").
				Finalizers(kueue.ResourceInUseFinalizerName).
				JobUID("job_uid").
				Obj(),
			wantError: errTest,
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
						afterFinished: ptr.To(util.MediumTimeout),
					},
				),
			},
			wantWorkload: nil,
			wantError:    nil,
		},
		"should remove finalizer from owned finished workload already being deleted, object retention configured": {
			workload: utiltestingapi.MakeWorkload("wl", "ns").
				Condition(metav1.Condition{
					Type:   kueue.WorkloadFinished,
					Status: metav1.ConditionTrue,
					LastTransitionTime: metav1.Time{
						Time: now.Add(-2 * util.MediumTimeout),
					},
				}).
				ControllerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "ownername", "owneruid").
				DeletionTimestamp(now).
				Finalizers(kueue.ResourceInUseFinalizerName, "example.com/finalizer").
				Obj(),
			reconcilerOpts: []Option{
				WithWorkloadRetention(
					&workloadRetentionConfig{
						afterFinished: ptr.To(util.MediumTimeout),
					},
				),
			},
			wantWorkload: utiltestingapi.MakeWorkload("wl", "ns").
				Condition(metav1.Condition{
					Type:   kueue.WorkloadFinished,
					Status: metav1.ConditionTrue,
					LastTransitionTime: metav1.Time{
						Time: now.Add(-2 * util.MediumTimeout),
					},
				}).
				ControllerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "ownername", "owneruid").
				DeletionTimestamp(now).
				Finalizers("example.com/finalizer").
				Obj(),
			wantError: nil,
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
						afterFinished: ptr.To(util.MediumTimeout),
					},
				),
			},
			wantResult: reconcile.Result{
				RequeueAfter: util.MediumTimeout - util.Timeout,
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
					LastTransitionTime: metav1.NewTime(now.Add(-2 * util.MediumTimeout)),
				}).
				Obj(),
			reconcilerOpts: []Option{
				WithWorkloadRetention(
					&workloadRetentionConfig{
						afterFinished: ptr.To(util.MediumTimeout),
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
		"should synchronize the status of preemption gates": {
			featureGates: map[featuregate.Feature]bool{
				features.KueueDRAIntegration:              false,
				features.MultiKueueOrchestratedPreemption: true,
			},
			cq: utiltestingapi.MakeClusterQueue("cq").Obj(),
			lq: utiltestingapi.MakeLocalQueue("lq", "ns").ClusterQueue("cq").Obj(),
			workload: utiltestingapi.MakeWorkload("wl", "ns").
				Queue("lq").
				PreemptionGates(
					kueue.PreemptionGate{
						Name: "synchronized",
					},
					kueue.PreemptionGate{
						Name: "desynchronized-status",
					}).
				PreemptionGateStates(
					kueue.PreemptionGateState{
						Name:               "synchronized",
						Position:           kueue.PreemptionGatePositionClosed,
						LastTransitionTime: metav1.NewTime(now),
					},
					kueue.PreemptionGateState{
						Name:               "desynchronized-spec",
						Position:           kueue.PreemptionGatePositionClosed,
						LastTransitionTime: metav1.NewTime(now),
					},
				).
				Obj(),
			wantWorkload: utiltestingapi.MakeWorkload("wl", "ns").
				Queue("lq").
				PreemptionGates(
					kueue.PreemptionGate{
						Name: "synchronized",
					},
					kueue.PreemptionGate{
						Name: "desynchronized-status",
					}).
				PreemptionGateStates(
					kueue.PreemptionGateState{
						Name:               "synchronized",
						Position:           kueue.PreemptionGatePositionClosed,
						LastTransitionTime: metav1.NewTime(now),
					},
					kueue.PreemptionGateState{
						Name:               "desynchronized-status",
						Position:           kueue.PreemptionGatePositionClosed,
						LastTransitionTime: metav1.NewTime(now),
					},
				).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadQuotaReserved,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadQuotaReservedReasonSuspended,
					Message: "ClusterQueue cq is inactive",
				}).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadAdmitted,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadAdmittedReasonNoReservation,
					Message: "The workload has no reservation",
				}).
				Obj(),
			wantWorkloadUseMergePatch: utiltestingapi.MakeWorkload("wl", "ns").
				Queue("lq").
				PreemptionGates(
					kueue.PreemptionGate{
						Name: "synchronized",
					},
					kueue.PreemptionGate{
						Name: "desynchronized-status",
					}).
				PreemptionGateStates(
					kueue.PreemptionGateState{
						Name:               "synchronized",
						Position:           kueue.PreemptionGatePositionClosed,
						LastTransitionTime: metav1.NewTime(now),
					},
					kueue.PreemptionGateState{
						Name:               "desynchronized-status",
						Position:           kueue.PreemptionGatePositionClosed,
						LastTransitionTime: metav1.NewTime(now),
					},
				).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadQuotaReserved,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadQuotaReservedReasonSuspended,
					Message: "ClusterQueue cq is inactive",
				}).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadAdmitted,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadAdmittedReasonNoReservation,
					Message: "The workload has no reservation",
				}).
				Obj(),
			wantResult: reconcile.Result{},
		},
		"workload that is deactivated should set QuotaReserved condition to Deactivated (observability enabled)": {
			featureGates: map[featuregate.Feature]bool{
				features.UnadmittedWorkloadsObservability: true,
			},
			workload: utiltestingapi.MakeWorkload("wl", "ns").
				Queue("lq").
				Active(false).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadEvicted,
					Status:  metav1.ConditionTrue,
					Reason:  kueue.WorkloadDeactivated,
					Message: "The workload is deactivated",
				}).
				SchedulingStatsEviction(kueue.WorkloadSchedulingStatsEviction{Reason: "Deactivated", Count: 1}).
				Obj(),
			cq: utiltestingapi.MakeClusterQueue("cq").Active(metav1.ConditionTrue).Obj(),
			lq: utiltestingapi.MakeLocalQueue("lq", "ns").ClusterQueue("cq").Obj(),
			wantWorkload: utiltestingapi.MakeWorkload("wl", "ns").
				Queue("lq").
				Active(false).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadEvicted,
					Status:  metav1.ConditionTrue,
					Reason:  kueue.WorkloadDeactivated,
					Message: "The workload is deactivated",
				}).
				SchedulingStatsEviction(kueue.WorkloadSchedulingStatsEviction{Reason: "Deactivated", Count: 1}).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadQuotaReserved,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadDeactivated,
					Message: "The workload is deactivated",
				}).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadAdmitted,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadAdmittedReasonNoReservation,
					Message: "The workload has no reservation",
				}).
				Obj(),
		},
		"workload that is reactivated should clear Deactivated condition and transition to PendingEvaluation (observability enabled)": {
			featureGates: map[featuregate.Feature]bool{
				features.UnadmittedWorkloadsObservability: true,
			},
			workload: utiltestingapi.MakeWorkload("wl", "ns").
				Queue("lq").
				Active(true).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadQuotaReserved,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadDeactivated,
					Message: "The workload is deactivated",
				}).
				Obj(),
			cq: utiltestingapi.MakeClusterQueue("cq").Active(metav1.ConditionTrue).Obj(),
			lq: utiltestingapi.MakeLocalQueue("lq", "ns").ClusterQueue("cq").Obj(),
			wantWorkload: utiltestingapi.MakeWorkload("wl", "ns").
				Queue("lq").
				Active(true).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadQuotaReserved,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadQuotaReservedReasonPendingEvaluation,
					Message: "Workload is pending evaluation in the scheduling queue",
				}).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadAdmitted,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadAdmittedReasonNoReservation,
					Message: "The workload has no reservation",
				}).
				Obj(),
		},
		"workload with AdmissionGatedBy annotation should set QuotaReserved condition to AdmissionGated": {
			workload: utiltestingapi.MakeWorkload("wl", "ns").
				Queue("lq").
				Annotation(constants.AdmissionGatedByAnnotation, "example.com/controller1").
				Obj(),
			cq: utiltestingapi.MakeClusterQueue("cq").Obj(),
			lq: utiltestingapi.MakeLocalQueue("lq", "ns").ClusterQueue("cq").Obj(),
			wantWorkload: utiltestingapi.MakeWorkload("wl", "ns").
				Queue("lq").
				Annotation(constants.AdmissionGatedByAnnotation, "example.com/controller1").
				Condition(metav1.Condition{
					Type:    kueue.WorkloadQuotaReserved,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadAdmissionGated,
					Message: "Admission is gated by: example.com/controller1",
				}).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadAdmitted,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadAdmittedReasonNoReservation,
					Message: "The workload has no reservation",
				}).
				Obj(),
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Namespace: "ns", Name: "wl"},
					EventType: corev1.EventTypeNormal,
					Reason:    "AdmissionGated",
					Message:   "Workload admission is gated by: example.com/controller1",
				},
			},
			reconcilerOpts: []Option{},
		},
		"workload with legacy Pending reason should transition to PendingEvaluation (observability enabled)": {
			featureGates: map[featuregate.Feature]bool{
				features.UnadmittedWorkloadsObservability: true,
			},
			workload: utiltestingapi.MakeWorkload("wl", "ns").
				Queue("lq").
				Condition(metav1.Condition{
					Type:    kueue.WorkloadQuotaReserved,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadPending, //nolint:staticcheck // SA1019: legacy reason
					Message: "Workload is pending",
				}).
				Obj(),
			cq: utiltestingapi.MakeClusterQueue("cq").Active(metav1.ConditionTrue).Obj(),
			lq: utiltestingapi.MakeLocalQueue("lq", "ns").ClusterQueue("cq").Obj(),
			wantWorkload: utiltestingapi.MakeWorkload("wl", "ns").
				Queue("lq").
				Condition(metav1.Condition{
					Type:    kueue.WorkloadQuotaReserved,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadQuotaReservedReasonPendingEvaluation,
					Message: "Workload is pending evaluation in the scheduling queue",
				}).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadAdmitted,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadAdmittedReasonNoReservation,
					Message: "The workload has no reservation",
				}).
				Obj(),
		},
		"workload with granular PendingEvaluation reason should preserve it when queue is active (observability enabled)": {
			featureGates: map[featuregate.Feature]bool{
				features.UnadmittedWorkloadsObservability: true,
			},
			workload: utiltestingapi.MakeWorkload("wl", "ns").
				Queue("lq").
				Condition(metav1.Condition{
					Type:    kueue.WorkloadQuotaReserved,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadQuotaReservedReasonPendingEvaluation,
					Message: "Workload is pending evaluation in the scheduling queue",
				}).
				Obj(),
			cq: utiltestingapi.MakeClusterQueue("cq").Active(metav1.ConditionTrue).Obj(),
			lq: utiltestingapi.MakeLocalQueue("lq", "ns").ClusterQueue("cq").Obj(),
			wantWorkload: utiltestingapi.MakeWorkload("wl", "ns").
				Queue("lq").
				Condition(metav1.Condition{
					Type:    kueue.WorkloadQuotaReserved,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadQuotaReservedReasonPendingEvaluation,
					Message: "Workload is pending evaluation in the scheduling queue",
				}).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadAdmitted,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadAdmittedReasonNoReservation,
					Message: "The workload has no reservation",
				}).
				Obj(),
		},
		"workload with granular PendingEvaluation reason should transition to Inadmissible when queue is missing (observability disabled)": {
			featureGates: map[featuregate.Feature]bool{
				features.UnadmittedWorkloadsObservability: false,
			},
			workload: utiltestingapi.MakeWorkload("wl", "ns").
				Queue("lq").
				Condition(metav1.Condition{
					Type:    kueue.WorkloadQuotaReserved,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadQuotaReservedReasonPendingEvaluation,
					Message: "Workload is pending evaluation in the scheduling queue",
				}).
				Obj(),
			cq: utiltestingapi.MakeClusterQueue("cq").Active(metav1.ConditionTrue).Obj(),
			wantWorkload: utiltestingapi.MakeWorkload("wl", "ns").
				Queue("lq").
				Condition(metav1.Condition{
					Type:    kueue.WorkloadQuotaReserved,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadInadmissible,
					Message: "LocalQueue lq doesn't exist",
				}).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadAdmitted,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadAdmittedReasonNoReservation,
					Message: "The workload has no reservation",
				}).
				Obj(),
		},
		"workload with AdmissionGatedBy annotation removed should clear the gate and emit event": {
			workload: utiltestingapi.MakeWorkload("wl", "ns").
				Queue("lq").
				Condition(metav1.Condition{
					Type:    kueue.WorkloadQuotaReserved,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadAdmissionGated,
					Message: "Admission is gated by: example.com/controller1",
				}).
				Obj(),
			cq: utiltestingapi.MakeClusterQueue("cq").Obj(),
			lq: utiltestingapi.MakeLocalQueue("lq", "ns").ClusterQueue("cq").Obj(),
			wantWorkload: utiltestingapi.MakeWorkload("wl", "ns").
				Queue("lq").
				Condition(metav1.Condition{
					Type:    kueue.WorkloadQuotaReserved,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadQuotaReservedReasonPendingEvaluation,
					Message: "AdmissionGatedBy cleared, waiting for quota reservation",
				}).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadAdmitted,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadAdmittedReasonNoReservation,
					Message: "The workload has no reservation",
				}).
				Obj(),
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Namespace: "ns", Name: "wl"},
					EventType: corev1.EventTypeNormal,
					Reason:    "AdmissionGateCleared",
					Message:   "Admission gate cleared, workload is now admissible",
				},
			},
			reconcilerOpts: []Option{},
		},
		"workload with multiple AdmissionGatedBy gates should remain gated": {
			workload: utiltestingapi.MakeWorkload("wl", "ns").
				Queue("lq").
				Annotation(constants.AdmissionGatedByAnnotation, "example.com/controller1,example.com/controller2").
				Obj(),
			cq: utiltestingapi.MakeClusterQueue("cq").Obj(),
			lq: utiltestingapi.MakeLocalQueue("lq", "ns").ClusterQueue("cq").Obj(),
			wantWorkload: utiltestingapi.MakeWorkload("wl", "ns").
				Queue("lq").
				Annotation(constants.AdmissionGatedByAnnotation, "example.com/controller1,example.com/controller2").
				Condition(metav1.Condition{
					Type:    kueue.WorkloadQuotaReserved,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadAdmissionGated,
					Message: "Admission is gated by: example.com/controller1,example.com/controller2",
				}).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadAdmitted,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadAdmittedReasonNoReservation,
					Message: "The workload has no reservation",
				}).
				Obj(),
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Namespace: "ns", Name: "wl"},
					EventType: corev1.EventTypeNormal,
					Reason:    "AdmissionGated",
					Message:   "Workload admission is gated by: example.com/controller1,example.com/controller2",
				},
			},
			reconcilerOpts: []Option{},
		},
		"workload with AdmissionGatedBy should not be admitted even with quota reserved": {
			workload: utiltestingapi.MakeWorkload("wl", "ns").
				Queue("lq").
				Annotation(constants.AdmissionGatedByAnnotation, "example.com/controller1").
				ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").Obj(), now).
				Obj(),
			cq: utiltestingapi.MakeClusterQueue("cq").Obj(),
			lq: utiltestingapi.MakeLocalQueue("lq", "ns").ClusterQueue("cq").Obj(),
			wantWorkload: utiltestingapi.MakeWorkload("wl", "ns").
				Queue("lq").
				Annotation(constants.AdmissionGatedByAnnotation, "example.com/controller1").
				ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").Obj(), now).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadQuotaReserved,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadAdmissionGated,
					Message: "Admission is gated by: example.com/controller1",
				}).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadAdmitted,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadAdmittedReasonNoReservation,
					Message: "The workload has no reservation",
				}).
				Obj(),
			wantWorkloadUseMergePatch: utiltestingapi.MakeWorkload("wl", "ns").
				Queue("lq").
				Annotation(constants.AdmissionGatedByAnnotation, "example.com/controller1").
				Condition(metav1.Condition{
					Type:    kueue.WorkloadQuotaReserved,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadAdmissionGated,
					Message: "Admission is gated by: example.com/controller1",
				}).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadAdmitted,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadAdmittedReasonNoReservation,
					Message: "The workload has no reservation",
				}).
				Obj(),
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Namespace: "ns", Name: "wl"},
					EventType: corev1.EventTypeNormal,
					Reason:    "AdmissionGated",
					Message:   "Workload admission is gated by: example.com/controller1",
				},
			},
			reconcilerOpts: []Option{},
		},
		"workload without AdmissionGatedBy annotation and no gated condition should not be affected": {
			workload: utiltestingapi.MakeWorkload("wl", "ns").
				Queue("lq").
				Obj(),
			cq: utiltestingapi.MakeClusterQueue("cq").Obj(),
			lq: utiltestingapi.MakeLocalQueue("lq", "ns").ClusterQueue("cq").Obj(),
			wantWorkload: utiltestingapi.MakeWorkload("wl", "ns").
				Queue("lq").
				Condition(metav1.Condition{
					Type:    kueue.WorkloadQuotaReserved,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadQuotaReservedReasonSuspended,
					Message: "ClusterQueue cq is inactive",
				}).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadAdmitted,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadAdmittedReasonNoReservation,
					Message: "The workload has no reservation",
				}).
				Obj(),
			reconcilerOpts: []Option{},
		},
		"workload with existing controller owner should not be finished": {
			featureGates: map[featuregate.Feature]bool{
				features.FinishOrphanedWorkloads: true,
			},
			workload: utiltestingapi.MakeWorkload("wl", "ns").
				ControllerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "ownername", "owneruid").
				Obj(),
			additionalObjects: []client.Object{
				&batchv1.Job{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "ownername",
						Namespace: "ns",
						UID:       "owneruid",
					},
				},
			},
			wantWorkload: utiltestingapi.MakeWorkload("wl", "ns").
				ControllerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "ownername", "owneruid").
				Condition(metav1.Condition{
					Type:    kueue.WorkloadQuotaReserved,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadQuotaReservedReasonMisconfigured,
					Message: "LocalQueue  doesn't exist",
				}).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadAdmitted,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadAdmittedReasonNoReservation,
					Message: "The workload has no reservation",
				}).
				Obj(),
		},
		"inadmissible workload due to missing LocalQueue (observability enabled)": {
			featureGates: map[featuregate.Feature]bool{
				features.UnadmittedWorkloadsObservability: true,
			},
			workload: utiltestingapi.MakeWorkload("wl", "ns").
				Queue("non-existent-lq").
				Obj(),
			wantWorkload: utiltestingapi.MakeWorkload("wl", "ns").
				Queue("non-existent-lq").
				Condition(metav1.Condition{
					Type:    kueue.WorkloadQuotaReserved,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadQuotaReservedReasonMisconfigured,
					Message: "LocalQueue non-existent-lq doesn't exist",
				}).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadAdmitted,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadAdmittedReasonNoReservation,
					Message: "The workload has no reservation",
				}).
				Obj(),
		},
		"inadmissible workload due to stopped ClusterQueue (observability enabled)": {
			featureGates: map[featuregate.Feature]bool{
				features.UnadmittedWorkloadsObservability: true,
			},
			workload: utiltestingapi.MakeWorkload("wl", "ns").
				Queue("lq").
				Obj(),
			cq: utiltestingapi.MakeClusterQueue("cq").Active(metav1.ConditionTrue).StopPolicy(kueue.HoldAndDrain).Obj(),
			lq: utiltestingapi.MakeLocalQueue("lq", "ns").ClusterQueue("cq").Obj(),
			wantWorkload: utiltestingapi.MakeWorkload("wl", "ns").
				Queue("lq").
				Condition(metav1.Condition{
					Type:    kueue.WorkloadQuotaReserved,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadQuotaReservedReasonSuspended,
					Message: "ClusterQueue cq is inactive",
				}).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadAdmitted,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadAdmittedReasonNoReservation,
					Message: "The workload has no reservation",
				}).
				Obj(),
		},
		"inadmissible workload due to broken ClusterQueue namespace selector (observability enabled)": {
			featureGates: map[featuregate.Feature]bool{
				features.UnadmittedWorkloadsObservability: true,
			},
			workload: utiltestingapi.MakeWorkload("wl", "ns").
				Queue("lq").
				Obj(),
			cq: utiltestingapi.MakeClusterQueue("cq").Active(metav1.ConditionTrue).Obj(),
			lq: utiltestingapi.MakeLocalQueue("lq", "ns").ClusterQueue("cq").Obj(),
			beforeReconcile: func(ctx context.Context, cl client.Client, _ *qcache.Manager) {
				cq := &kueue.ClusterQueue{}
				if err := cl.Get(ctx, types.NamespacedName{Name: "cq"}, cq); err != nil {
					panic(err)
				}
				cq.Spec.NamespaceSelector = &metav1.LabelSelector{
					MatchExpressions: []metav1.LabelSelectorRequirement{
						{
							Key:      "invalid-operator",
							Operator: "InvalOp",
						},
					},
				}
				if err := cl.Update(ctx, cq); err != nil {
					panic(err)
				}
			},
			wantWorkload: utiltestingapi.MakeWorkload("wl", "ns").
				Queue("lq").
				Condition(metav1.Condition{
					Type:    kueue.WorkloadQuotaReserved,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadQuotaReservedReasonMisconfigured,
					Message: "invalid namespace selector: \"InvalOp\" is not a valid label selector operator",
				}).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadAdmitted,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadAdmittedReasonNoReservation,
					Message: "The workload has no reservation",
				}).
				Obj(),
		},
		"newly created workload (initial reconcile, ExplicitStatus enabled)": {
			featureGates: map[featuregate.Feature]bool{
				features.UnadmittedWorkloadsObservability:  true,
				features.UnadmittedWorkloadsExplicitStatus: true,
			},
			workload: utiltestingapi.MakeWorkload("wl", "ns").
				Queue("lq").
				Obj(),
			cq: utiltestingapi.MakeClusterQueue("cq").Active(metav1.ConditionTrue).Obj(),
			lq: utiltestingapi.MakeLocalQueue("lq", "ns").ClusterQueue("cq").Obj(),
			wantWorkload: utiltestingapi.MakeWorkload("wl", "ns").
				Queue("lq").
				Condition(metav1.Condition{
					Type:    kueue.WorkloadQuotaReserved,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadQuotaReservedReasonPendingEvaluation,
					Message: "Workload is pending evaluation in the scheduling queue",
				}).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadAdmitted,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadAdmittedReasonNoReservation,
					Message: "The workload has no reservation",
				}).
				Obj(),
		},
		"newly created workload (initial reconcile, ExplicitStatus disabled)": {
			featureGates: map[featuregate.Feature]bool{
				features.UnadmittedWorkloadsObservability:  true,
				features.UnadmittedWorkloadsExplicitStatus: false,
			},
			workload: utiltestingapi.MakeWorkload("wl", "ns").
				Queue("lq").
				Obj(),
			cq: utiltestingapi.MakeClusterQueue("cq").Active(metav1.ConditionTrue).Obj(),
			lq: utiltestingapi.MakeLocalQueue("lq", "ns").ClusterQueue("cq").Obj(),
			wantWorkload: utiltestingapi.MakeWorkload("wl", "ns").
				Queue("lq").
				Obj(),
		},
		"reconcile bypassed workload when SchedulingEquivalenceHashing is enabled (stale status -> bypassed status)": {
			featureGates: map[featuregate.Feature]bool{
				features.SchedulingEquivalenceHashing:      true,
				features.UnadmittedWorkloadsObservability:  true,
				features.UnadmittedWorkloadsExplicitStatus: true,
			},
			workload: utiltestingapi.MakeWorkload("wl", "ns").
				Queue("lq").
				Request(corev1.ResourceCPU, "1").
				Obj(),
			cq: utiltestingapi.MakeClusterQueue("cq").Active(metav1.ConditionTrue).Obj(),
			lq: utiltestingapi.MakeLocalQueue("lq", "ns").ClusterQueue("cq").Obj(),
			beforeReconcile: func(ctx context.Context, cl client.Client, qManager *qcache.Manager) {
				wl := &kueue.Workload{}
				if err := cl.Get(ctx, types.NamespacedName{Name: "wl", Namespace: "ns"}, wl); err != nil {
					panic(err)
				}
				qManager.Heads(ctx) // Pop from active heap
				wInfo := workload.NewInfo(wl)
				qManager.RequeueWorkload(ctx, wInfo, qcache.RequeueReasonNoFit, qcache.QuotaReservedReasonWaitingForQuota)
			},
			wantWorkload: utiltestingapi.MakeWorkload("wl", "ns").
				Queue("lq").
				Request(corev1.ResourceCPU, "1").
				Condition(metav1.Condition{
					Type:    kueue.WorkloadQuotaReserved,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadQuotaReservedReasonWaitingForQuota,
					Message: "Bypassed scheduling evaluation because an equivalent workload recently failed",
				}).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadAdmitted,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadAdmittedReasonNoReservation,
					Message: "The workload has no reservation",
				}).
				Obj(),
		},
		"reconcile bypassed workload when SchedulingEquivalenceHashing is enabled and UnadmittedWorkloadsExplicitStatus is disabled (no status stamped on creation)": {
			featureGates: map[featuregate.Feature]bool{
				features.SchedulingEquivalenceHashing:      true,
				features.UnadmittedWorkloadsObservability:  true,
				features.UnadmittedWorkloadsExplicitStatus: false,
			},
			workload: utiltestingapi.MakeWorkload("wl", "ns").
				Queue("lq").
				Request(corev1.ResourceCPU, "1").
				Obj(),
			cq: utiltestingapi.MakeClusterQueue("cq").Active(metav1.ConditionTrue).Obj(),
			lq: utiltestingapi.MakeLocalQueue("lq", "ns").ClusterQueue("cq").Obj(),
			beforeReconcile: func(ctx context.Context, cl client.Client, qManager *qcache.Manager) {
				wl := &kueue.Workload{}
				if err := cl.Get(ctx, types.NamespacedName{Name: "wl", Namespace: "ns"}, wl); err != nil {
					panic(err)
				}
				qManager.Heads(ctx) // Pop from active heap
				wInfo := workload.NewInfo(wl)
				qManager.RequeueWorkload(ctx, wInfo, qcache.RequeueReasonNoFit, qcache.QuotaReservedReasonWaitingForQuota)
			},
			wantWorkload: utiltestingapi.MakeWorkload("wl", "ns").
				Queue("lq").
				Request(corev1.ResourceCPU, "1").
				Obj(),
		},
		"reconcile bypassed workload when SchedulingEquivalenceHashing is enabled (keep detailed scheduler message)": {
			featureGates: map[featuregate.Feature]bool{
				features.SchedulingEquivalenceHashing:      true,
				features.UnadmittedWorkloadsObservability:  true,
				features.UnadmittedWorkloadsExplicitStatus: true,
			},
			workload: utiltestingapi.MakeWorkload("wl", "ns").
				Queue("lq").
				Request(corev1.ResourceCPU, "1").
				Condition(metav1.Condition{
					Type:    kueue.WorkloadQuotaReserved,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadQuotaReservedReasonWaitingForQuota,
					Message: "insufficient CPU in ClusterQueue cq",
				}).
				Obj(),
			cq: utiltestingapi.MakeClusterQueue("cq").Active(metav1.ConditionTrue).Obj(),
			lq: utiltestingapi.MakeLocalQueue("lq", "ns").ClusterQueue("cq").Obj(),
			beforeReconcile: func(ctx context.Context, cl client.Client, qManager *qcache.Manager) {
				wl := &kueue.Workload{}
				if err := cl.Get(ctx, types.NamespacedName{Name: "wl", Namespace: "ns"}, wl); err != nil {
					panic(err)
				}
				qManager.Heads(ctx) // Pop from active heap
				wInfo := workload.NewInfo(wl)
				qManager.RequeueWorkload(ctx, wInfo, qcache.RequeueReasonNoFit, qcache.QuotaReservedReasonWaitingForQuota)
			},
			wantWorkload: utiltestingapi.MakeWorkload("wl", "ns").
				Queue("lq").
				Request(corev1.ResourceCPU, "1").
				Condition(metav1.Condition{
					Type:    kueue.WorkloadQuotaReserved,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadQuotaReservedReasonWaitingForQuota,
					Message: "insufficient CPU in ClusterQueue cq",
				}).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadAdmitted,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadAdmittedReasonNoReservation,
					Message: "The workload has no reservation",
				}).
				Obj(),
		},
		"reconcile bypassed workload when SchedulingEquivalenceHashing is disabled (stale status remains pending)": {
			featureGates: map[featuregate.Feature]bool{
				features.SchedulingEquivalenceHashing:      false,
				features.UnadmittedWorkloadsObservability:  true,
				features.UnadmittedWorkloadsExplicitStatus: true,
			},
			workload: utiltestingapi.MakeWorkload("wl", "ns").
				Queue("lq").
				Request(corev1.ResourceCPU, "1").
				Obj(),
			cq: utiltestingapi.MakeClusterQueue("cq").Active(metav1.ConditionTrue).Obj(),
			lq: utiltestingapi.MakeLocalQueue("lq", "ns").ClusterQueue("cq").Obj(),
			beforeReconcile: func(ctx context.Context, cl client.Client, qManager *qcache.Manager) {
				wl := &kueue.Workload{}
				if err := cl.Get(ctx, types.NamespacedName{Name: "wl", Namespace: "ns"}, wl); err != nil {
					panic(err)
				}
				qManager.Heads(ctx) // Pop from active heap
				wInfo := workload.NewInfo(wl)
				qManager.RequeueWorkload(ctx, wInfo, qcache.RequeueReasonNoFit, qcache.QuotaReservedReasonWaitingForQuota)
			},
			wantWorkload: utiltestingapi.MakeWorkload("wl", "ns").
				Queue("lq").
				Request(corev1.ResourceCPU, "1").
				Condition(metav1.Condition{
					Type:    kueue.WorkloadQuotaReserved,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadQuotaReservedReasonPendingEvaluation,
					Message: "Workload is pending evaluation in the scheduling queue",
				}).
				Condition(metav1.Condition{
					Type:    kueue.WorkloadAdmitted,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadAdmittedReasonNoReservation,
					Message: "The workload has no reservation",
				}).
				Obj(),
		},
	}
	runReconcileTestCases(t, cases, fakeClock)
}

func runReconcileTestCases(t *testing.T, cases map[string]reconcileTestCase, fakeClock *testingclock.FakeClock) {
	scenarios := []map[featuregate.Feature]bool{
		{
			features.WorkloadRequestUseMergePatch:     false,
			features.UnadmittedWorkloadsObservability: false,
		},
		{
			features.WorkloadRequestUseMergePatch:     false,
			features.UnadmittedWorkloadsObservability: true,
		},
		{
			features.WorkloadRequestUseMergePatch:     true,
			features.UnadmittedWorkloadsObservability: false,
		},
		{
			features.WorkloadRequestUseMergePatch:     true,
			features.UnadmittedWorkloadsObservability: true,
		},
	}

	for name, tc := range cases {
		for _, scenario := range scenarios {
			// Skip scenarios where the test case overrides the scenario's feature gate value
			// to avoid running duplicate tests and misreporting the gate values in the subtest name.
			skip := false
			for fg, val := range tc.featureGates {
				if scenarioVal, exists := scenario[fg]; exists && scenarioVal != val {
					skip = true
					break
				}
			}
			if skip {
				continue
			}

			t.Run(fmt.Sprintf("%s WorkloadRequestUseMergePatch enabled: %t, UnadmittedWorkloadsObservability enabled: %t",
				name, scenario[features.WorkloadRequestUseMergePatch], scenario[features.UnadmittedWorkloadsObservability]), func(t *testing.T) {
				fgMap := make(map[featuregate.Feature]bool)
				maps.Copy(fgMap, scenario)
				maps.Copy(fgMap, tc.featureGates)
				features.SetFeatureGatesDuringTest(t, fgMap)
				features.SetFeatureGateDuringTest(t, features.AdmissionGatedBy, true)

				testWl := tc.workload.DeepCopy()
				objs := []client.Object{testWl}
				if testWl.Namespace != "" {
					objs = append(objs, &corev1.Namespace{
						ObjectMeta: metav1.ObjectMeta{
							Name: testWl.Namespace,
						},
					})
				}
				objs = append(objs, tc.additionalObjects...)
				for _, rc := range tc.resourceClaims {
					objs = append(objs, rc)
				}

				for _, rct := range tc.resourceClaimTemplates {
					objs = append(objs, rct)
				}

				// Create a stub owner object so that the FinishOrphanedWorkloads
				// check does not incorrectly mark them as orphaned. Skip when
				// the test explicitly enables the feature gate (those tests
				// provide their own additionalObjects to control ownership).
				if ref := metav1.GetControllerOf(testWl); ref != nil && !tc.featureGates[features.FinishOrphanedWorkloads] {
					objs = append(objs, &batchv1.Job{
						ObjectMeta: metav1.ObjectMeta{
							Name:      ref.Name,
							Namespace: testWl.Namespace,
							UID:       ref.UID,
						},
					})
				}

				clientBuilder := utiltesting.NewClientBuilder().
					WithObjects(objs...).
					WithStatusSubresource(objs...).
					WithInterceptorFuncs(interceptor.Funcs{
						SubResourcePatch: func(ctx context.Context, client client.Client, subResourceName string, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
							if tc.patchErr != nil {
								return tc.patchErr
							}
							return utiltesting.TreatSSAAsStrategicMerge(ctx, client, subResourceName, obj, patch, opts...)
						},
						List: func(ctx context.Context, c client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
							if tc.listErr != nil {
								if _, ok := list.(*resourcev1.ResourceSliceList); ok {
									return tc.listErr
								}
							}
							return c.List(ctx, list, opts...)
						},
					})
				if features.Enabled(features.KueueDRAIntegrationExtendedResource) {
					clientBuilder = clientBuilder.WithIndex(&resourcev1.DeviceClass{}, utilindexer.DeviceClassExtendedResourceNameIndex, utilindexer.IndexDeviceClassExtendedResourceName)
				}
				cl := clientBuilder.Build()
				recorder := &utiltesting.EventRecorder{}

				cqCache := schdcache.New(cl)
				var draCache *dra.ExtendedResourceCache
				if features.Enabled(features.KueueDRAIntegration) {
					draCache = setupDRACache(objs)
				}
				queueOptions := []qcache.Option{qcache.WithPreemptionExpectations(preemptexpectations.New())}
				if draCache != nil {
					queueOptions = append(queueOptions, qcache.WithDRABackedResources(draCache))
				}
				qManager := qcache.NewManagerForUnitTests(cl, cqCache, queueOptions...)
				reconcilerOpts := tc.reconcilerOpts
				if draCache != nil {
					reconcilerOpts = append(reconcilerOpts, WithDRABackedResources(draCache))
				}
				reconciler := NewWorkloadReconciler(cl, qManager, cqCache, recorder, reconcilerOpts...)
				if features.Enabled(features.KueueDRAIntegration) {
					qManager.SetDRAReconcileChannel(reconciler.GetDRAReconcileChannel())
				}
				// use a fake clock with jitter = 0 to be able to assert on the requeueAt.
				reconciler.clock = fakeClock

				ctxWithLogger, _ := utiltesting.ContextWithLog(t)
				ctx, ctxCancel := context.WithCancel(ctxWithLogger)
				defer ctxCancel()

				if tc.cq != nil {
					setupClusterQueue(ctx, t, cl, qManager, cqCache, tc.cq, tc.shouldDeleteCQ)
				}

				if tc.lq != nil {
					setupLocalQueue(ctx, t, cl, qManager, tc.lq, tc.shouldDeleteLQ)
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
					draMapper := dra.NewResourceMapper()
					err := draMapper.PopulateFromConfiguration(draConfig)
					if err != nil {
						t.Fatalf("Failed to initialize DRA mapper: %v", err)
					}
					reconciler.draMapper = draMapper
				}

				if tc.beforeReconcile != nil {
					tc.beforeReconcile(ctx, cl, qManager)
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

					var wantWl *kueue.Workload
					if features.Enabled(features.WorkloadRequestUseMergePatch) && tc.wantWorkloadUseMergePatch != nil {
						wantWl = tc.wantWorkloadUseMergePatch.DeepCopy()
					} else {
						wantWl = tc.wantWorkload.DeepCopy()
					}

					if !features.Enabled(features.UnadmittedWorkloadsObservability) {
						wantWl.Status.Conditions = utiltesting.AdjustConditionsForDisabledObservabilityInWorkloadController(
							wantWl.Status.Conditions,
							apimeta.IsStatusConditionTrue(tc.workload.Status.Conditions, kueue.WorkloadAdmitted),
						)
					}

					if diff := cmp.Diff(wantWl, gotWorkload, workloadCmpOpts...); diff != "" {
						t.Errorf("Workloads after reconcile (-want,+got):\n%s", diff)
					}
				}
				if diff := cmp.Diff(tc.wantEvents, recorder.RecordedEvents); diff != "" {
					t.Errorf("unexpected events (-want/+got):\n%s", diff)
				}

				// For DRA tests, verify that workloads are properly queued/cached
				if tc.featureGates[features.KueueDRAIntegration] && testWl != nil &&
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

func TestReconcileSyncAdmissionChecks(t *testing.T) {
	cases := map[string]struct {
		wl         kueue.Workload
		cq         kueue.ClusterQueue
		wantChecks []kueue.AdmissionCheckState
	}{
		"no checks in cq": {
			wl: *utiltestingapi.MakeWorkload("wl", "ns").Obj(),
			cq: *utiltestingapi.MakeClusterQueue("cq").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("flavor1").Obj(),
					*utiltestingapi.MakeFlavorQuotas("flavor2").Obj(),
				).Obj(),
		},
		"add checks from cq": {
			wl: *utiltestingapi.MakeWorkload("wl", "ns").Obj(),
			cq: *utiltestingapi.MakeClusterQueue("cq").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("flavor1").Obj(),
					*utiltestingapi.MakeFlavorQuotas("flavor2").Obj(),
				).AdmissionChecks("ac1", "ac2").Obj(),
			wantChecks: []kueue.AdmissionCheckState{
				{
					Name:  "ac1",
					State: kueue.CheckStatePending,
				},
				{
					Name:  "ac2",
					State: kueue.CheckStatePending,
				},
			},
		},
		"add only checks covering all flavors to unadmitted wl from cq with strategy": {
			wl: *utiltestingapi.MakeWorkload("wl", "ns").Obj(),
			cq: *utiltestingapi.MakeClusterQueue("cq").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("flavor1").Obj(),
					*utiltestingapi.MakeFlavorQuotas("flavor2").Obj(),
				).AdmissionCheckStrategy(
				*utiltestingapi.MakeAdmissionCheckStrategyRule("ac1", "flavor1").Obj(),
				*utiltestingapi.MakeAdmissionCheckStrategyRule("ac2").Obj(),
			).Obj(),
			wantChecks: []kueue.AdmissionCheckState{
				{
					Name:  "ac2",
					State: kueue.CheckStatePending,
				},
			},
		},
		"add checks to admitted wl from cq with strategy": {
			wl: *utiltestingapi.MakeWorkload("wl", "ns").
				Admission(&kueue.Admission{
					ClusterQueue: "cq",
					PodSetAssignments: []kueue.PodSetAssignment{
						{
							Name: "p1",
							Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
								corev1.ResourceCPU: "flavor1",
							},
						},
					},
				}).Obj(),
			cq: *utiltestingapi.MakeClusterQueue("cq").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("flavor1").Obj(),
					*utiltestingapi.MakeFlavorQuotas("flavor2").Obj(),
				).AdmissionCheckStrategy(
				*utiltestingapi.MakeAdmissionCheckStrategyRule("ac1", "flavor1").Obj(),
				*utiltestingapi.MakeAdmissionCheckStrategyRule("ac2", "flavor2").Obj(),
				*utiltestingapi.MakeAdmissionCheckStrategyRule("ac3").Obj(),
			).Obj(),
			wantChecks: []kueue.AdmissionCheckState{
				{
					Name:  "ac1",
					State: kueue.CheckStatePending,
				},
				{
					Name:  "ac3",
					State: kueue.CheckStatePending,
				},
			},
		},
		"keep only existing valid checks": {
			wl: *utiltestingapi.MakeWorkload("wl", "ns").
				AdmissionChecks(
					kueue.AdmissionCheckState{
						Name:  "ac1",
						State: kueue.CheckStateReady,
					},
					kueue.AdmissionCheckState{
						Name:  "ac3",
						State: kueue.CheckStateReady,
					},
				).Obj(),
			cq: *utiltestingapi.MakeClusterQueue("cq").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("flavor1").Obj(),
					*utiltestingapi.MakeFlavorQuotas("flavor2").Obj(),
				).AdmissionCheckStrategy(
				*utiltestingapi.MakeAdmissionCheckStrategyRule("ac1").Obj(),
				*utiltestingapi.MakeAdmissionCheckStrategyRule("ac2").Obj(),
			).Obj(),
			wantChecks: []kueue.AdmissionCheckState{
				{
					Name:  "ac1",
					State: kueue.CheckStateReady,
				},
				{
					Name:  "ac2",
					State: kueue.CheckStatePending,
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

			reconciler := NewWorkloadReconciler(cl, qManager, cqCache, recorder)

			ctxWithLogger, _ := utiltesting.ContextWithLog(t)
			ctx, ctxCancel := context.WithCancel(ctxWithLogger)
			defer ctxCancel()

			if _, err := reconciler.reconcileSyncAdmissionChecks(ctx, &tc.wl, &tc.cq); err != nil {
				t.Errorf("unexpected error: %v", err)
			}

			if diff := cmp.Diff(tc.wantChecks, tc.wl.Status.AdmissionChecks, cmpopts.IgnoreFields(kueue.AdmissionCheckState{}, "LastTransitionTime")); diff != "" {
				t.Errorf("Incorrect admission checks (-want,+got):\n%s", diff)
			}
		})
	}
}

func setupClusterQueue(ctx context.Context, t *testing.T, cl client.Client, qManager *qcache.Manager, cqCache *schdcache.Cache, cq *kueue.ClusterQueue, shouldDelete bool) {
	t.Helper()
	testCq := cq.DeepCopy()
	// DeletionTimestamp cannot be directly set during creation. We simulate it by deleting the object with a finalizer.
	if shouldDelete && len(testCq.Finalizers) == 0 {
		testCq.Finalizers = []string{"testing-finalizer"}
	}
	if err := cl.Create(ctx, testCq); err != nil {
		t.Fatalf("couldn't create the cluster queue: %v", err)
	}
	if err := qManager.AddClusterQueue(ctx, testCq); err != nil {
		t.Fatalf("couldn't add the cluster queue to the cache: %v", err)
	}
	isActive := apimeta.IsStatusConditionTrue(testCq.Status.Conditions, kueue.ClusterQueueActive)
	if isActive || shouldDelete {
		if err := cqCache.AddClusterQueue(ctx, testCq); err != nil {
			t.Fatalf("couldn't add the cluster queue to the scheduler cache: %v", err)
		}
	}
	if shouldDelete {
		if err := cl.Delete(ctx, testCq); err != nil {
			t.Fatalf("couldn't delete the cluster queue: %v", err)
		}
	}
}

func setupLocalQueue(ctx context.Context, t *testing.T, cl client.Client, qManager *qcache.Manager, lq *kueue.LocalQueue, shouldDelete bool) {
	t.Helper()
	testLq := lq.DeepCopy()
	// DeletionTimestamp cannot be directly set during creation. We simulate it by deleting the object with a finalizer.
	if shouldDelete && len(testLq.Finalizers) == 0 {
		testLq.Finalizers = []string{"testing-finalizer"}
	}
	if err := cl.Create(ctx, testLq); err != nil {
		t.Fatalf("couldn't create the local queue: %v", err)
	}
	if err := qManager.AddLocalQueue(ctx, testLq); err != nil {
		t.Fatalf("couldn't add the local queue to the cache: %v", err)
	}
	if shouldDelete {
		if err := cl.Delete(ctx, testLq); err != nil {
			t.Fatalf("couldn't delete the local queue: %v", err)
		}
	}
}

func setupDRACache(objs []client.Object) *dra.ExtendedResourceCache {
	draCache := dra.NewExtendedResourceCache()
	for _, obj := range objs {
		if dc, ok := obj.(*resourcev1.DeviceClass); ok {
			if dc.Spec.ExtendedResourceName != nil {
				draCache.Add(corev1.ResourceName(*dc.Spec.ExtendedResourceName), dc.Name)
			}
		}
	}
	return draCache
}
