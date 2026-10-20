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
	"errors"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/component-base/featuregate"
	testingclock "k8s.io/utils/clock/testing"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	qcache "sigs.k8s.io/kueue/pkg/cache/queue"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/features"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
)

func TestReconcileRequeueStateReset(t *testing.T) {
	errStatus := errors.New("status write failed")
	// the clock is primarily used with second rounded times
	// use the current time trimmed.
	now := time.Now().Truncate(time.Second)
	fakeClock := testingclock.NewFakeClock(now)

	timeoutConfig := &waitForPodsReadyConfig{
		timeout:                     5 * time.Minute,
		requeuingBackoffLimitCount:  new(int32(100)),
		requeuingBackoffBaseSeconds: 10,
		requeuingBackoffJitter:      0,
		requeuingBackoffMaxDuration: time.Hour,
	}

	// readyWl returns an admitted workload which is PodsReady for the
	// given duration and carries a requeueState left by earlier evictions.
	readyWl := func(readyFor time.Duration) *utiltestingapi.WorkloadWrapper {
		return utiltestingapi.MakeWorkload("wl", "ns").
			ControllerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "ownername", "owneruid").
			ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").Obj(), now).
			AdmittedAt(true, now).
			Condition(metav1.Condition{
				Type:               kueue.WorkloadPodsReady,
				Status:             metav1.ConditionTrue,
				Reason:             "PodsReady",
				Message:            "All pods are ready or succeeded",
				LastTransitionTime: metav1.NewTime(now.Add(-readyFor)),
			}).
			RequeueState(new(int32(3)), nil)
	}

	cases := map[string]reconcileTestCase{
		"should keep the requeueState after sustained readiness when the gate is disabled": {
			featureGates: map[featuregate.Feature]bool{
				features.ResetRequeueStateOnPodsReady: false,
			},
			reconcilerOpts: []Option{WithWaitForPodsReady(timeoutConfig)},
			workload:       readyWl(10 * time.Minute).Obj(),
			wantWorkload:   readyWl(10 * time.Minute).Obj(),
			wantResult:     reconcile.Result{},
		},
		"should keep the requeueState and requeue when the readiness is sustained shorter than the timeout": {
			featureGates: map[featuregate.Feature]bool{
				features.ResetRequeueStateOnPodsReady: true,
			},
			reconcilerOpts: []Option{WithWaitForPodsReady(timeoutConfig)},
			workload:       readyWl(2 * time.Minute).Obj(),
			wantWorkload:   readyWl(2 * time.Minute).Obj(),
			wantResult:     reconcile.Result{RequeueAfter: 3 * time.Minute},
		},
		"should clear the requeueState after readiness sustained for at least the timeout": {
			featureGates: map[featuregate.Feature]bool{
				features.ResetRequeueStateOnPodsReady: true,
			},
			reconcilerOpts: []Option{WithWaitForPodsReady(timeoutConfig)},
			workload:       readyWl(5 * time.Minute).Obj(),
			wantWorkload:   readyWl(5*time.Minute).RequeueState(nil, nil).Obj(),
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Name: "wl", Namespace: "ns"},
					EventType: corev1.EventTypeNormal,
					Reason:    "RequeueStateReset",
					Message:   "The requeueState was cleared since the workload was in PodsReady for at least the WaitForPodsReady timeout",
				},
			},
			wantResult: reconcile.Result{},
		},
		"should not reset the requeueState when the readiness flipped to False before the threshold": {
			featureGates: map[featuregate.Feature]bool{
				features.ResetRequeueStateOnPodsReady: true,
			},
			reconcilerOpts: []Option{WithWaitForPodsReady(timeoutConfig)},
			workload: utiltestingapi.MakeWorkload("wl", "ns").
				ControllerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "ownername", "owneruid").
				ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").Obj(), now).
				AdmittedAt(true, now).
				Condition(metav1.Condition{
					Type:               kueue.WorkloadPodsReady,
					Status:             metav1.ConditionFalse,
					Reason:             kueue.WorkloadWaitForStart,
					Message:            "Not all pods are ready or succeeded",
					LastTransitionTime: metav1.NewTime(now.Add(-time.Minute)),
				}).
				RequeueState(new(int32(3)), nil).
				Obj(),
			wantWorkload: utiltestingapi.MakeWorkload("wl", "ns").
				ControllerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "ownername", "owneruid").
				ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").Obj(), now).
				AdmittedAt(true, now).
				Condition(metav1.Condition{
					Type:               kueue.WorkloadPodsReady,
					Status:             metav1.ConditionFalse,
					Reason:             kueue.WorkloadWaitForStart,
					Message:            "Not all pods are ready or succeeded",
					LastTransitionTime: metav1.NewTime(now.Add(-time.Minute)),
				}).
				RequeueState(new(int32(3)), nil).
				Obj(),
			wantResult: reconcile.Result{RequeueAfter: 5 * time.Minute},
		},
		"should not reset the requeueState when no waitForPodsReady timeout is configured": {
			featureGates: map[featuregate.Feature]bool{
				features.ResetRequeueStateOnPodsReady: true,
			},
			workload:     readyWl(10 * time.Minute).Obj(),
			wantWorkload: readyWl(10 * time.Minute).Obj(),
			wantResult:   reconcile.Result{},
		},
		"should do nothing when the requeueState is already cleared": {
			featureGates: map[featuregate.Feature]bool{
				features.ResetRequeueStateOnPodsReady: true,
			},
			reconcilerOpts: []Option{WithWaitForPodsReady(timeoutConfig)},
			workload:       readyWl(10*time.Minute).RequeueState(nil, nil).Obj(),
			wantWorkload:   readyWl(10*time.Minute).RequeueState(nil, nil).Obj(),
			wantResult:     reconcile.Result{},
		},
		"should return the error when clearing the requeueState fails": {
			featureGates: map[featuregate.Feature]bool{
				features.ResetRequeueStateOnPodsReady: true,
			},
			reconcilerOpts: []Option{WithWaitForPodsReady(timeoutConfig)},
			patchErr:       errStatus,
			workload:       readyWl(5 * time.Minute).Obj(),
			wantWorkload:   readyWl(5 * time.Minute).Obj(),
			wantError:      errStatus,
		},
		"should count from one on the next PodsReadyTimeout eviction when requeueState is unset (post-reset shape)": {
			featureGates: map[featuregate.Feature]bool{
				features.ResetRequeueStateOnPodsReady: true,
			},
			reconcilerOpts: []Option{WithWaitForPodsReady(timeoutConfig)},
			workload: utiltestingapi.MakeWorkload("wl", "ns").
				ControllerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "ownername", "owneruid").
				ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").Obj(), now).
				Condition(metav1.Condition{ // Override LastTransitionTime
					Type:               kueue.WorkloadAdmitted,
					Status:             metav1.ConditionTrue,
					LastTransitionTime: metav1.NewTime(now.Add(-6 * time.Minute)),
					Reason:             "ByTest",
					Message:            "Admitted by ClusterQueue q1",
				}).
				AdmittedAt(true, now).
				Generation(1).
				Obj(),
			wantWorkload: utiltestingapi.MakeWorkload("wl", "ns").
				ControllerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "ownername", "owneruid").
				ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").Obj(), now).
				Condition(metav1.Condition{
					Type:               kueue.WorkloadAdmitted,
					Status:             metav1.ConditionTrue,
					LastTransitionTime: metav1.NewTime(now.Add(-6 * time.Minute)),
					Reason:             "ByTest",
					Message:            "Admitted by ClusterQueue q1",
				}).
				AdmittedAt(true, now).
				Generation(1).
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
	}
	runReconcileTestCases(t, cases, fakeClock)
}

// TestReconcileRequeueStateResetIdempotent verifies the second reconciliation
// after the reset neither emits an event nor fails.
func TestReconcileRequeueStateResetIdempotent(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	fakeClock := testingclock.NewFakeClock(now)
	features.SetFeatureGateDuringTest(t, features.ResetRequeueStateOnPodsReady, true)

	wl := utiltestingapi.MakeWorkload("wl", "ns").
		ControllerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "ownername", "owneruid").
		ReserveQuotaAt(utiltestingapi.MakeAdmission("q1").Obj(), now).
		AdmittedAt(true, now).
		Condition(metav1.Condition{
			Type:               kueue.WorkloadPodsReady,
			Status:             metav1.ConditionTrue,
			Reason:             "PodsReady",
			Message:            "All pods are ready or succeeded",
			LastTransitionTime: metav1.NewTime(now.Add(-10 * time.Minute)),
		}).
		RequeueState(new(int32(3)), nil).
		Obj()
	wantEvents := []utiltesting.EventRecord{
		{
			Key:       types.NamespacedName{Name: "wl", Namespace: "ns"},
			EventType: corev1.EventTypeNormal,
			Reason:    "RequeueStateReset",
			Message:   "The requeueState was cleared since the workload was in PodsReady for at least the WaitForPodsReady timeout",
		},
	}

	objs := []client.Object{wl, &batchv1.Job{
		Name:      "ownername",
		Namespace: "ns",
		UID:       "owneruid",
	}, &corev1.Namespace{Name: "ns"}}
	cl := utiltesting.NewClientBuilder().
		WithObjects(objs...).
		WithStatusSubresource(objs...).
		WithInterceptorFuncs(interceptor.Funcs{
			SubResourceApply: utiltesting.TreatSSAAsStrategicMergeForApplyConfiguration,
		}).
		Build()
	recorder := &utiltesting.EventRecorder{}
	cqCache := schdcache.New(cl)
	qManager := qcache.NewManagerForUnitTests(cl, cqCache)
	reconciler := NewWorkloadReconciler(cl, qManager, cqCache, recorder,
		WithWaitForPodsReady(&waitForPodsReadyConfig{
			timeout:                     5 * time.Minute,
			requeuingBackoffLimitCount:  new(int32(100)),
			requeuingBackoffBaseSeconds: 10,
			requeuingBackoffJitter:      0,
			requeuingBackoffMaxDuration: time.Hour,
		}))
	reconciler.clock = fakeClock

	ctx, _ := utiltesting.ContextWithLog(t)
	req := reconcile.Request{NamespacedName: client.ObjectKeyFromObject(wl)}

	result, err := reconciler.Reconcile(ctx, req)
	if err != nil {
		t.Fatalf("unexpected error on the first reconcile: %v", err)
	}
	if diff := cmp.Diff(reconcile.Result{}, result); diff != "" {
		t.Errorf("unexpected reconcile result (-want/+got):\n%s", diff)
	}
	gotWl := &kueue.Workload{}
	if err := cl.Get(ctx, req.NamespacedName, gotWl); err != nil {
		t.Fatalf("could not get the workload after the first reconcile: %v", err)
	}
	if gotWl.Status.RequeueState != nil {
		t.Errorf("expected the requeueState to be cleared, got %+v", gotWl.Status.RequeueState)
	}
	if diff := cmp.Diff(wantEvents, recorder.RecordedEvents); diff != "" {
		t.Errorf("unexpected events after the first reconcile (-want/+got):\n%s", diff)
	}

	result, err = reconciler.Reconcile(ctx, req)
	if err != nil {
		t.Fatalf("unexpected error on the second reconcile: %v", err)
	}
	if diff := cmp.Diff(reconcile.Result{}, result); diff != "" {
		t.Errorf("unexpected reconcile result on the second reconcile (-want/+got):\n%s", diff)
	}
	if err := cl.Get(ctx, req.NamespacedName, gotWl); err != nil {
		t.Fatalf("could not get the workload after the second reconcile: %v", err)
	}
	if gotWl.Status.RequeueState != nil {
		t.Errorf("expected the requeueState to stay cleared, got %+v", gotWl.Status.RequeueState)
	}
	if diff := cmp.Diff(wantEvents, recorder.RecordedEvents); diff != "" {
		t.Errorf("unexpected events after the second reconcile (-want/+got):\n%s", diff)
	}
}
