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

package failurerecovery

import (
	"context"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	testingclock "k8s.io/utils/clock/testing"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/event"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"sigs.k8s.io/kueue/pkg/controller/constants"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	testingnode "sigs.k8s.io/kueue/pkg/util/testingjobs/node"
	testingpod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
)

var (
	podCmpOpts = cmp.Options{
		cmpopts.EquateEmpty(),
		cmpopts.IgnoreFields(
			corev1.Pod{}, "ObjectMeta.ResourceVersion", "ObjectMeta.DeletionTimestamp",
		),
	}
)

func TestCreateEventFilter(t *testing.T) {
	now := time.Now()
	fakeClock := testingclock.NewFakeClock(now)
	cl := utiltesting.NewFakeClient()
	recorder := &utiltesting.EventRecorder{}
	reconciler, err := NewTerminatingPodReconciler(cl, recorder, WithClock(fakeClock))
	if err != nil {
		t.Fatalf("could not create reconciler: %v", err)
	}

	podToReconcile := testingpod.MakePod("pod", "ns").
		StatusPhase(corev1.PodRunning).
		Annotation(constants.SafeToForcefullyTerminateAnnotationKey, constants.SafeToForcefullyTerminateAnnotationValue).
		DeletionTimestamp(now).
		KueueFinalizer()

	cases := map[string]struct {
		pod        *corev1.Pod
		wantResult bool
	}{
		"pod is annotated and marked for deletion": {
			pod:        podToReconcile.Obj(),
			wantResult: true,
		},
		"pod is not annotated": {
			pod: podToReconcile.
				Clone().
				Annotation(constants.SafeToForcefullyTerminateAnnotationKey, "false").
				Obj(),
			wantResult: false,
		},
		"pod is not marked for deletion": {
			pod: podToReconcile.
				Clone().
				DeletionTimestamp(time.Time{}).
				Obj(),
			wantResult: false,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			gotResult := reconciler.Create(event.CreateEvent{Object: tc.pod})

			if diff := cmp.Diff(tc.wantResult, gotResult); diff != "" {
				t.Errorf("unexpected reconcile result (-want/+got):\n%s", diff)
			}
		})
	}
}

func TestUpdateEventFilter(t *testing.T) {
	now := time.Now()
	fakeClock := testingclock.NewFakeClock(now)
	cl := utiltesting.NewFakeClient()
	recorder := &utiltesting.EventRecorder{}
	reconciler, err := NewTerminatingPodReconciler(cl, recorder, WithClock(fakeClock))
	if err != nil {
		t.Fatalf("could not create reconciler: %v", err)
	}

	oldPod := testingpod.MakePod("pod", "ns").
		StatusPhase(corev1.PodRunning).
		Annotation(constants.SafeToForcefullyTerminateAnnotationKey, constants.SafeToForcefullyTerminateAnnotationValue)
	newPodToReconcile := oldPod.Clone().DeletionTimestamp(now)

	cases := map[string]struct {
		oldPod     *corev1.Pod
		newPod     *corev1.Pod
		wantResult bool
	}{
		"pod is annotated and was marked for deletion": {
			oldPod:     oldPod.Obj(),
			newPod:     newPodToReconcile.Obj(),
			wantResult: true,
		},
		"pod was already deleted": {
			oldPod:     oldPod.Clone().DeletionTimestamp(now).Obj(),
			newPod:     newPodToReconcile.Obj(),
			wantResult: false,
		},
		"updated object is not annotated": {
			oldPod: oldPod.Obj(),
			newPod: newPodToReconcile.
				Clone().
				Annotation(constants.SafeToForcefullyTerminateAnnotationKey, "false").
				Obj(),
			wantResult: false,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			gotResult := reconciler.Update(event.UpdateEvent{ObjectOld: tc.oldPod, ObjectNew: tc.newPod})

			if diff := cmp.Diff(tc.wantResult, gotResult); diff != "" {
				t.Errorf("unexpected reconcile result (-want/+got):\n%s", diff)
			}
		})
	}
}

func TestReconciler(t *testing.T) {
	now := time.Now()
	nowSecondPrecision := metav1.NewTime(now).Rfc3339Copy()
	beforeGracePeriod := now.Add(-time.Minute * 3)
	fakeClock := testingclock.NewFakeClock(now)

	unreachableNode := testingnode.MakeNode("unreachable-node").
		Taints(corev1.Taint{Key: corev1.TaintNodeUnreachable}).Obj()
	healthyNode := testingnode.MakeNode("healthy-node").Obj()
	podToForcefullyTerminate := testingpod.MakePod("pod", "ns").
		StatusPhase(corev1.PodRunning).
		Annotation(constants.SafeToForcefullyTerminateAnnotationKey, constants.SafeToForcefullyTerminateAnnotationValue).
		NodeName(unreachableNode.Name).
		DeletionTimestamp(beforeGracePeriod).
		KueueFinalizer()

	cases := map[string]struct {
		testPod    *corev1.Pod
		wantResult ctrl.Result
		wantErr    error
		wantPod    *corev1.Pod
		wantEvents []utiltesting.EventRecord
	}{
		"pod is in failed phase": {
			testPod: podToForcefullyTerminate.
				Clone().
				StatusPhase(corev1.PodFailed).
				Obj(),
			wantResult: ctrl.Result{},
			wantErr:    nil,
			wantPod: podToForcefullyTerminate.
				Clone().
				StatusPhase(corev1.PodFailed).
				Obj(),
			wantEvents: nil,
		},
		"pod is in succeeded phase": {
			testPod: podToForcefullyTerminate.
				Clone().
				StatusPhase(corev1.PodSucceeded).
				Obj(),
			wantResult: ctrl.Result{},
			wantErr:    nil,
			wantPod: podToForcefullyTerminate.
				Clone().
				StatusPhase(corev1.PodSucceeded).
				Obj(),
			wantEvents: nil,
		},
		"pod is not scheduled on an unreachable node": {
			testPod: podToForcefullyTerminate.
				Clone().
				NodeName(healthyNode.Name).
				Obj(),
			wantResult: ctrl.Result{},
			wantErr:    nil,
			wantPod: podToForcefullyTerminate.
				Clone().
				NodeName(healthyNode.Name).
				Obj(),
			wantEvents: nil,
		},
		"forceful termination grace period did not elapse for pod": {
			testPod: podToForcefullyTerminate.
				Clone().
				DeletionTimestamp(now).
				Obj(),
			wantResult: ctrl.Result{RequeueAfter: nowSecondPrecision.Add(time.Minute).Sub(now)},
			wantErr:    nil,
			wantPod: podToForcefullyTerminate.
				Clone().
				DeletionTimestamp(now).
				Obj(),
			wantEvents: nil,
		},
		"forceful termination grace period elapsed for pod": {
			testPod:    podToForcefullyTerminate.Clone().Obj(),
			wantResult: ctrl.Result{},
			wantErr:    nil,
			wantPod: podToForcefullyTerminate.Clone().
				StatusPhase(corev1.PodFailed).
				StatusConditions(corev1.PodCondition{
					Type:    KueueFailureRecoveryConditionType,
					Status:  "True",
					Reason:  KueueForcefulTerminationReason,
					Message: "Pod forcefully terminated after 1m0s grace period due to unreachable node `unreachable-node` (triggered by `kueue.x-k8s.io/safe-to-forcefully-terminate` annotation)",
				}).
				Obj(),
			wantEvents: []utiltesting.EventRecord{
				{
					Key:       types.NamespacedName{Namespace: "ns", Name: "pod"},
					EventType: "Warning",
					Reason:    KueueForcefulTerminationReason,
					Message:   "Pod forcefully terminated after 1m0s grace period due to unreachable node `unreachable-node` (triggered by `kueue.x-k8s.io/safe-to-forcefully-terminate` annotation)",
				},
			},
		},
		"pod is scheduled on a node that does not exist": {
			testPod:    podToForcefullyTerminate.Clone().NodeName("missing-node").Obj(),
			wantResult: ctrl.Result{},
			wantErr:    nil,
			wantPod:    podToForcefullyTerminate.Clone().NodeName("missing-node").Obj(),
			wantEvents: nil,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			objs := []client.Object{tc.testPod, healthyNode, unreachableNode}
			clientBuilder := utiltesting.NewClientBuilder().WithObjects(objs...)
			cl := clientBuilder.Build()
			recorder := &utiltesting.EventRecorder{}
			reconciler, err := NewTerminatingPodReconciler(cl, recorder, WithClock(fakeClock))
			if err != nil {
				t.Fatalf("could not create reconciler: %v", err)
			}

			ctxWithLogger, _ := utiltesting.ContextWithLog(t)
			ctx, ctxCancel := context.WithCancel(ctxWithLogger)
			defer ctxCancel()

			gotResult, gotError := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(tc.testPod)})

			if diff := cmp.Diff(tc.wantResult, gotResult); diff != "" {
				t.Errorf("unexpected reconcile result (-want/+got):\n%s", diff)
			}

			if diff := cmp.Diff(tc.wantErr, gotError); diff != "" {
				t.Errorf("unexpected reconcile error (-want/+got):\n%s", diff)
			}

			gotPod := podToForcefullyTerminate.Clone().Obj()
			if err := cl.Get(ctx, client.ObjectKeyFromObject(tc.testPod), gotPod); err != nil {
				t.Fatalf("could not get pod after reconcile")
			}

			if diff := cmp.Diff(tc.wantPod, gotPod, podCmpOpts...); diff != "" {
				t.Errorf("Workloads after reconcile (-want,+got):\n%s", diff)
			}

			if diff := cmp.Diff(tc.wantEvents, recorder.RecordedEvents); diff != "" {
				t.Errorf("unexpected events (-want/+got):\n%s", diff)
			}
		})
	}
}
