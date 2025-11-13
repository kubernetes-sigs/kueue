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
	"fmt"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	testingclock "k8s.io/utils/clock/testing"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta2"
	"sigs.k8s.io/kueue/pkg/constants"
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

func TestReconciler(t *testing.T) {
	now := time.Now()
	nowSecondPrecision := metav1.NewTime(now).Rfc3339Copy()
	beforeGracePeriod := now.Add(-time.Minute * 3)
	inbetweenConfiguredGracePeriods := now.Add(-time.Second * 45)
	fakeClock := testingclock.NewFakeClock(now)

	unreachableNode := testingnode.MakeNode("unreachable-node").
		Taints(corev1.Taint{Key: corev1.TaintNodeUnreachable}).Obj()
	healthyNode := testingnode.MakeNode("healthy-node").Obj()
	podToForcefullyTerminate := testingpod.MakePod("pod", "").
		StatusPhase(corev1.PodRunning).
		ManagedByKueueLabel().
		Label("terminate-quickly", "true").
		NodeName(unreachableNode.Name).
		DeletionTimestamp(beforeGracePeriod).
		KueueFinalizer()
	baseCfg := []configapi.TerminatePodConfig{
		// Config matching every pod
		{
			PodLabelSelector:               &metav1.LabelSelector{},
			ForcefulTerminationGracePeriod: metav1.Duration{Duration: time.Minute},
		},
		// Config matching specific pods. Tested pod does not have this label.
		{
			PodLabelSelector: &metav1.LabelSelector{
				MatchLabels: map[string]string{
					"missing-label": "value",
				},
			},
			ForcefulTerminationGracePeriod: metav1.Duration{Duration: time.Second * 10},
		},
	}
	// Additional config matching specific pods. Tested pod has this label.
	multiCfg := append(baseCfg, configapi.TerminatePodConfig{
		PodLabelSelector: &metav1.LabelSelector{
			MatchLabels: map[string]string{
				"terminate-quickly": "true",
			},
		},
		ForcefulTerminationGracePeriod: metav1.Duration{Duration: time.Second * 30},
	})

	cases := map[string]struct {
		testPod    *corev1.Pod
		testConfig []configapi.TerminatePodConfig
		wantResult ctrl.Result
		wantErr    error
		wantPod    *corev1.Pod
	}{
		"pod is not found": {
			testPod:    testingpod.MakePod("pod2", "").Obj(),
			testConfig: baseCfg,
			wantResult: ctrl.Result{},
			wantErr:    nil,
		},
		"pod is not marked for termination": {
			testPod: podToForcefullyTerminate.
				Clone().
				DeletionTimestamp(time.Time{}).
				Obj(),
			testConfig: baseCfg,
			wantResult: ctrl.Result{},
			wantErr:    nil,
		},
		"pod is in failed phase": {
			testPod: podToForcefullyTerminate.
				Clone().
				StatusPhase(corev1.PodFailed).
				Obj(),
			testConfig: baseCfg,
			wantResult: ctrl.Result{},
			wantErr:    nil,
		},
		"pod is in succeeded phase": {
			testPod: podToForcefullyTerminate.
				Clone().
				StatusPhase(corev1.PodSucceeded).
				Obj(),
			testConfig: baseCfg,
			wantResult: ctrl.Result{},
			wantErr:    nil,
		},
		"pod is not managed by Kueue": {
			testPod: podToForcefullyTerminate.
				Clone().
				Label(constants.ManagedByKueueLabelKey, "false").
				Obj(),
			testConfig: baseCfg,
			wantResult: ctrl.Result{},
			wantErr:    nil,
		},
		"pod is not scheduled on an unreachable node": {
			testPod: podToForcefullyTerminate.
				Clone().
				NodeName(healthyNode.Name).
				Obj(),
			testConfig: baseCfg,
			wantResult: ctrl.Result{},
			wantErr:    nil,
		},
		"forceful termination grace period did not elapse for pod": {
			testPod: podToForcefullyTerminate.
				Clone().
				DeletionTimestamp(now).
				Obj(),
			testConfig: baseCfg,
			wantResult: ctrl.Result{RequeueAfter: nowSecondPrecision.Add(time.Minute).Sub(now)},
			wantErr:    nil,
		},
		"forceful termination grace period elapsed for pod": {
			testPod:    podToForcefullyTerminate.Clone().Obj(),
			testConfig: baseCfg,
			wantResult: ctrl.Result{},
			wantErr:    nil,
			wantPod:    podToForcefullyTerminate.Clone().StatusPhase(corev1.PodFailed).Obj(),
		},
		"stricter forceful termination grace period did not elapse for pod with multiple matching configs": {
			testPod: podToForcefullyTerminate.
				Clone().
				DeletionTimestamp(now).
				Obj(),
			testConfig: multiCfg,
			wantResult: ctrl.Result{RequeueAfter: nowSecondPrecision.Add(time.Second * 30).Sub(now)},
			wantErr:    nil,
		},
		"stricter forceful termination grace period elapsed for pod with multiple matching configs": {
			testPod: podToForcefullyTerminate.
				Clone().
				DeletionTimestamp(inbetweenConfiguredGracePeriods).
				Obj(),
			testConfig: multiCfg,
			wantResult: ctrl.Result{},
			wantErr:    nil,
			wantPod:    podToForcefullyTerminate.Clone().StatusPhase(corev1.PodFailed).Obj(),
		},
		"pod is scheduled on a node that does not exist": {
			testPod:    podToForcefullyTerminate.Clone().NodeName("missing-node").Obj(),
			testConfig: baseCfg,
			wantResult: ctrl.Result{},
			wantErr:    apierrors.NewNotFound(schema.GroupResource{Group: corev1.GroupName, Resource: "nodes"}, "missing-node"),
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			objs := []client.Object{tc.testPod, healthyNode, unreachableNode}
			clientBuilder := utiltesting.NewClientBuilder().WithObjects(objs...)
			cl := clientBuilder.Build()
			reconciler, err := NewTerminatingPodReconciler(cl, tc.testConfig, WithClock(fakeClock))
			if err != nil {
				t.Fatalf("could not create reconciler: %v", err)
			}

			ctxWithLogger, _ := utiltesting.ContextWithLog(t)
			ctx, ctxCancel := context.WithCancel(ctxWithLogger)
			defer ctxCancel()

			gotResult, gotError := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: client.ObjectKeyFromObject(tc.testPod)})

			if diff := cmp.Diff(tc.wantResult, gotResult); diff != "" {
				fmt.Println(tc.testPod.DeletionTimestamp.Add(time.Minute).Sub(now))
				t.Errorf("unexpected reconcile result (-want/+got):\n%s", diff)
			}

			if diff := cmp.Diff(tc.wantErr, gotError); diff != "" {
				t.Errorf("unexpected reconcile error (-want/+got):\n%s", diff)
			}

			gotPod := podToForcefullyTerminate.Clone().Obj()
			if err := cl.Get(ctx, client.ObjectKeyFromObject(tc.testPod), gotPod); err != nil {
				t.Fatalf("could not get pod after reconcile")
			}
			wantPod := tc.wantPod
			if wantPod == nil {
				wantPod = tc.testPod
			}
			if diff := cmp.Diff(wantPod, gotPod, podCmpOpts...); diff != "" {
				t.Errorf("Workloads after reconcile (-want,+got):\n%s", diff)
			}
		})
	}
}
