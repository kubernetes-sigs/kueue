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

package controller

import (
	"context"
	"testing"
	"time"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/tools/events"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/cmd/experimental/kueue-priority-booster/pkg/constants"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
)

const (
	testWorkloadNS       = "ns"
	testWorkloadName     = "wl-1"
	defaultNegativeBoost = int32(100000)
	defaultTimeWindow    = 30 * time.Minute
)

func newTestScheme(t *testing.T) *runtime.Scheme {
	t.Helper()
	scheme := runtime.NewScheme()
	if err := kueue.AddToScheme(scheme); err != nil {
		t.Fatalf("AddToScheme: %v", err)
	}
	return scheme
}

func admittedWorkload(at time.Time) *kueue.Workload {
	return utiltestingapi.MakeWorkload(testWorkloadName, testWorkloadNS).
		Condition(metav1.Condition{
			Type:               string(kueue.WorkloadAdmitted),
			Status:             metav1.ConditionTrue,
			LastTransitionTime: metav1.NewTime(at),
			Reason:             "Admitted",
		}).
		Obj()
}

func defaultReconcilerOpts() []PriorityBoostReconcilerOption {
	return []PriorityBoostReconcilerOption{
		WithTimeSharingInterval(defaultTimeWindow),
		WithNegativeBoostValue(defaultNegativeBoost),
	}
}

func TestComputeBoost(t *testing.T) {
	window := defaultTimeWindow
	neg := defaultNegativeBoost
	baseR := &PriorityBoostReconciler{
		timeSharingInterval: window,
		negativeBoostValue:  neg,
	}

	cases := map[string]struct {
		r                   *PriorityBoostReconciler
		wl                  *kueue.Workload
		wantBoost           int32
		wantRequeueAfter    time.Duration
		wantRequeueAfterTol time.Duration
	}{
		"not admitted": {
			r:                baseR,
			wl:               &kueue.Workload{},
			wantBoost:        0,
			wantRequeueAfter: 0,
		},
		"admitted condition false": {
			r: baseR,
			wl: utiltestingapi.MakeWorkload(testWorkloadName, testWorkloadNS).
				Condition(metav1.Condition{
					Type:               string(kueue.WorkloadAdmitted),
					Status:             metav1.ConditionFalse,
					LastTransitionTime: metav1.NewTime(time.Now().Add(-5 * time.Minute)),
					Reason:             "Evicted",
				}).
				Obj(),
			wantBoost:        0,
			wantRequeueAfter: 0,
		},
		"within window": {
			r:                   baseR,
			wl:                  admittedWorkload(time.Now().Add(-5 * time.Minute)),
			wantBoost:           0,
			wantRequeueAfter:    window - 5*time.Minute,
			wantRequeueAfterTol: 2 * time.Second,
		},
		"window expired negative boost": {
			r:                baseR,
			wl:               admittedWorkload(time.Now().Add(-31 * time.Minute)),
			wantBoost:        -neg,
			wantRequeueAfter: 0,
		},
		"disabled zero window": {
			r: &PriorityBoostReconciler{
				timeSharingInterval: 0,
				negativeBoostValue:  neg,
			},
			wl:               admittedWorkload(time.Now().Add(-1 * time.Minute)),
			wantBoost:        0,
			wantRequeueAfter: 0,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			boost, requeueAfter := tc.r.computeBoost(tc.wl)
			if boost != tc.wantBoost {
				t.Errorf("boost=%d want %d", boost, tc.wantBoost)
			}
			if tc.wantRequeueAfterTol > 0 {
				lo, hi := tc.wantRequeueAfter-tc.wantRequeueAfterTol, tc.wantRequeueAfter+tc.wantRequeueAfterTol
				if requeueAfter < lo || requeueAfter > hi {
					t.Errorf("requeueAfter=%v want ≈%v (±%v)", requeueAfter, tc.wantRequeueAfter, tc.wantRequeueAfterTol)
				}
			} else if requeueAfter != tc.wantRequeueAfter {
				t.Errorf("requeueAfter=%v want %v", requeueAfter, tc.wantRequeueAfter)
			}
		})
	}
}

func TestReconcile(t *testing.T) {
	batchSelector, err := metav1.LabelSelectorAsSelector(&metav1.LabelSelector{
		MatchLabels: map[string]string{"tier": "batch"},
	})
	if err != nil {
		t.Fatal(err)
	}
	maxP := int32(100)

	type checkFn func(t *testing.T, ctx context.Context, cl client.Client, result ctrl.Result, req types.NamespacedName)

	cases := map[string]struct {
		wl        *kueue.Workload
		extraOpts []PriorityBoostReconcilerOption
		check     checkFn
	}{
		"within window no annotation": {
			wl: admittedWorkload(time.Now().Add(-2 * time.Minute)),
			check: func(t *testing.T, ctx context.Context, cl client.Client, result ctrl.Result, req types.NamespacedName) {
				if result.RequeueAfter <= 0 {
					t.Errorf("RequeueAfter=%v want >0", result.RequeueAfter)
				}
				var updated kueue.Workload
				if err := cl.Get(ctx, req, &updated); err != nil {
					t.Fatalf("Get: %v", err)
				}
				if _, ok := updated.Annotations[constants.PriorityBoostAnnotationKey]; ok {
					t.Errorf("unexpected annotation during window")
				}
			},
		},
		"after window sets negative boost": {
			wl: utiltestingapi.MakeWorkload(testWorkloadName, testWorkloadNS).
				Condition(metav1.Condition{
					Type:               string(kueue.WorkloadAdmitted),
					Status:             metav1.ConditionTrue,
					LastTransitionTime: metav1.NewTime(time.Now().Add(-60 * time.Minute)),
					Reason:             "Admitted",
				}).
				Annotation(constants.PriorityBoostAnnotationKey, "100000").
				Obj(),
			check: func(t *testing.T, ctx context.Context, cl client.Client, result ctrl.Result, req types.NamespacedName) {
				if result.RequeueAfter != 0 {
					t.Errorf("RequeueAfter=%v want 0", result.RequeueAfter)
				}
				var updated kueue.Workload
				if err := cl.Get(ctx, req, &updated); err != nil {
					t.Fatalf("Get: %v", err)
				}
				if got, want := updated.Annotations[constants.PriorityBoostAnnotationKey], "-100000"; got != want {
					t.Errorf("annotation=%q want %q", got, want)
				}
			},
		},
		"not admitted clears stale annotation": {
			wl: utiltestingapi.MakeWorkload("wl-pending", testWorkloadNS).
				Annotation(constants.PriorityBoostAnnotationKey, "-100000").
				Obj(),
			check: func(t *testing.T, ctx context.Context, cl client.Client, result ctrl.Result, req types.NamespacedName) {
				var updated kueue.Workload
				if err := cl.Get(ctx, req, &updated); err != nil {
					t.Fatalf("Get: %v", err)
				}
				if _, ok := updated.Annotations[constants.PriorityBoostAnnotationKey]; ok {
					t.Error("annotation should be cleared for pending workload")
				}
			},
		},
		"out of selector clears controller-managed annotation": {
			wl: utiltestingapi.MakeWorkload(testWorkloadName, testWorkloadNS).
				Labels(map[string]string{"tier": "interactive"}).
				Condition(metav1.Condition{
					Type:               string(kueue.WorkloadAdmitted),
					Status:             metav1.ConditionTrue,
					LastTransitionTime: metav1.NewTime(time.Now().Add(-60 * time.Minute)),
					Reason:             "Admitted",
				}).
				Annotation(constants.PriorityBoostAnnotationKey, "-100000").
				Obj(),
			extraOpts: []PriorityBoostReconcilerOption{WithWorkloadSelector(batchSelector)},
			check: func(t *testing.T, ctx context.Context, cl client.Client, result ctrl.Result, req types.NamespacedName) {
				var updated kueue.Workload
				if err := cl.Get(ctx, req, &updated); err != nil {
					t.Fatalf("Get: %v", err)
				}
				if _, ok := updated.Annotations[constants.PriorityBoostAnnotationKey]; ok {
					t.Error("controller-managed annotation should be cleared when out of selector")
				}
			},
		},
		"out of selector preserves manually-set annotation": {
			wl: utiltestingapi.MakeWorkload(testWorkloadName, testWorkloadNS).
				Labels(map[string]string{"tier": "interactive"}).
				Condition(metav1.Condition{
					Type:               string(kueue.WorkloadAdmitted),
					Status:             metav1.ConditionTrue,
					LastTransitionTime: metav1.NewTime(time.Now().Add(-60 * time.Minute)),
					Reason:             "Admitted",
				}).
				Annotation(constants.PriorityBoostAnnotationKey, "500").
				Obj(),
			extraOpts: []PriorityBoostReconcilerOption{WithWorkloadSelector(batchSelector)},
			check: func(t *testing.T, ctx context.Context, cl client.Client, result ctrl.Result, req types.NamespacedName) {
				var updated kueue.Workload
				if err := cl.Get(ctx, req, &updated); err != nil {
					t.Fatalf("Get: %v", err)
				}
				if got := updated.Annotations[constants.PriorityBoostAnnotationKey]; got != "500" {
					t.Errorf("manually-set annotation should be preserved, got %q", got)
				}
			},
		},
		"above max workload priority clears controller-managed annotation": {
			wl: utiltestingapi.MakeWorkload(testWorkloadName, testWorkloadNS).
				Condition(metav1.Condition{
					Type:               string(kueue.WorkloadAdmitted),
					Status:             metav1.ConditionTrue,
					LastTransitionTime: metav1.NewTime(time.Now().Add(-60 * time.Minute)),
					Reason:             "Admitted",
				}).
				Priority(500).
				Annotation(constants.PriorityBoostAnnotationKey, "-100000").
				Obj(),
			extraOpts: []PriorityBoostReconcilerOption{WithMaxWorkloadPriority(&maxP)},
			check: func(t *testing.T, ctx context.Context, cl client.Client, result ctrl.Result, req types.NamespacedName) {
				var updated kueue.Workload
				if err := cl.Get(ctx, req, &updated); err != nil {
					t.Fatalf("Get: %v", err)
				}
				if _, ok := updated.Annotations[constants.PriorityBoostAnnotationKey]; ok {
					t.Error("controller-managed annotation should be cleared when above maxWorkloadPriority")
				}
			},
		},
		"above max workload priority preserves manually-set annotation": {
			wl: utiltestingapi.MakeWorkload(testWorkloadName, testWorkloadNS).
				Condition(metav1.Condition{
					Type:               string(kueue.WorkloadAdmitted),
					Status:             metav1.ConditionTrue,
					LastTransitionTime: metav1.NewTime(time.Now().Add(-60 * time.Minute)),
					Reason:             "Admitted",
				}).
				Priority(500).
				Annotation(constants.PriorityBoostAnnotationKey, "1000").
				Obj(),
			extraOpts: []PriorityBoostReconcilerOption{WithMaxWorkloadPriority(&maxP)},
			check: func(t *testing.T, ctx context.Context, cl client.Client, result ctrl.Result, req types.NamespacedName) {
				var updated kueue.Workload
				if err := cl.Get(ctx, req, &updated); err != nil {
					t.Fatalf("Get: %v", err)
				}
				if got := updated.Annotations[constants.PriorityBoostAnnotationKey]; got != "1000" {
					t.Errorf("manually-set annotation should be preserved, got %q", got)
				}
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx := t.Context()
			scheme := newTestScheme(t)
			cl := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(tc.wl).WithObjects(tc.wl).Build()
			opts := append(append([]PriorityBoostReconcilerOption{}, defaultReconcilerOpts()...), tc.extraOpts...)
			r := NewPriorityBoostReconciler(cl, events.NewFakeRecorder(32), opts...)
			req := types.NamespacedName{Name: tc.wl.Name, Namespace: tc.wl.Namespace}
			result, err := r.Reconcile(ctx, ctrlRequest(req))
			if err != nil {
				t.Fatalf("Reconcile: %v", err)
			}
			tc.check(t, ctx, cl, result, req)
		})
	}
}

func TestReconcile_NoPatchWhenAlreadyDesired(t *testing.T) {
	batchSelector, err := metav1.LabelSelectorAsSelector(&metav1.LabelSelector{
		MatchLabels: map[string]string{"tier": "batch"},
	})
	if err != nil {
		t.Fatal(err)
	}

	cases := map[string]struct {
		wl        *kueue.Workload
		extraOpts []PriorityBoostReconcilerOption
	}{
		"within window idempotent": {
			wl: admittedWorkload(time.Now().Add(-2 * time.Minute)),
		},
		"post window annotation already correct": {
			wl: utiltestingapi.MakeWorkload(testWorkloadName, testWorkloadNS).
				Condition(metav1.Condition{
					Type:               string(kueue.WorkloadAdmitted),
					Status:             metav1.ConditionTrue,
					LastTransitionTime: metav1.NewTime(time.Now().Add(-60 * time.Minute)),
					Reason:             "Admitted",
				}).
				Annotation(constants.PriorityBoostAnnotationKey, "-100000").
				Obj(),
		},
		"out of selector no annotation no patch": {
			wl: utiltestingapi.MakeWorkload(testWorkloadName, testWorkloadNS).
				Labels(map[string]string{"tier": "interactive"}).
				Condition(metav1.Condition{
					Type:               string(kueue.WorkloadAdmitted),
					Status:             metav1.ConditionTrue,
					LastTransitionTime: metav1.NewTime(time.Now().Add(-60 * time.Minute)),
					Reason:             "Admitted",
				}).
				Obj(),
			extraOpts: []PriorityBoostReconcilerOption{WithWorkloadSelector(batchSelector)},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx := t.Context()
			scheme := newTestScheme(t)
			base := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(tc.wl).WithObjects(tc.wl).Build()
			patchCalled := false
			cl := interceptor.NewClient(base, interceptor.Funcs{
				Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
					patchCalled = true
					return c.Patch(ctx, obj, patch, opts...)
				},
			})
			opts := append(append([]PriorityBoostReconcilerOption{}, defaultReconcilerOpts()...), tc.extraOpts...)
			r := NewPriorityBoostReconciler(cl, events.NewFakeRecorder(32), opts...)
			req := types.NamespacedName{Name: tc.wl.Name, Namespace: tc.wl.Namespace}
			if _, err := r.Reconcile(ctx, ctrlRequest(req)); err != nil {
				t.Fatalf("Reconcile: %v", err)
			}
			if patchCalled {
				t.Error("unexpected Patch call")
			}
		})
	}
}

func TestReconcile_ConflictThenSuccess(t *testing.T) {
	ctx := t.Context()
	scheme := newTestScheme(t)
	wl := admittedWorkload(time.Now().Add(-60 * time.Minute))
	baseClient := fake.NewClientBuilder().WithScheme(scheme).WithStatusSubresource(wl).WithObjects(wl).Build()
	conflictInjected := false
	cl := interceptor.NewClient(baseClient, interceptor.Funcs{
		Patch: func(ctx context.Context, c client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
			if !conflictInjected {
				conflictInjected = true
				return apierrors.NewConflict(schema.GroupResource{Group: "kueue.x-k8s.io", Resource: "workloads"}, obj.GetName(), nil)
			}
			return c.Patch(ctx, obj, patch, opts...)
		},
	})
	r := NewPriorityBoostReconciler(cl, events.NewFakeRecorder(32), defaultReconcilerOpts()...)
	req := types.NamespacedName{Name: wl.Name, Namespace: wl.Namespace}
	if _, err := r.Reconcile(ctx, ctrlRequest(req)); err == nil {
		t.Fatal("expected first reconcile to fail with conflict")
	}
	if _, err := r.Reconcile(ctx, ctrlRequest(req)); err != nil {
		t.Fatalf("second reconcile: %v", err)
	}
	var updated kueue.Workload
	if err := baseClient.Get(ctx, req, &updated); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if got, want := updated.Annotations[constants.PriorityBoostAnnotationKey], "-100000"; got != want {
		t.Errorf("annotation=%q want %q", got, want)
	}
}

func TestShouldEnqueueWorkload(t *testing.T) {
	sel, err := metav1.LabelSelectorAsSelector(&metav1.LabelSelector{
		MatchLabels: map[string]string{"tier": "batch"},
	})
	if err != nil {
		t.Fatal(err)
	}
	r := &PriorityBoostReconciler{workloadSelector: sel}

	cases := map[string]struct {
		wl   *kueue.Workload
		want bool
	}{
		"out of selector no annotation": {
			wl: utiltestingapi.MakeWorkload("b", testWorkloadNS).
				Label("tier", "interactive").
				Obj(),
			want: false,
		},
		"out of selector with annotation": {
			wl: utiltestingapi.MakeWorkload("b", testWorkloadNS).
				Label("tier", "interactive").
				Annotation(constants.PriorityBoostAnnotationKey, "-100000").
				Obj(),
			want: true,
		},
		"in selector": {
			wl: utiltestingapi.MakeWorkload("c", testWorkloadNS).
				Label("tier", "batch").
				Obj(),
			want: true,
		},
		"deleting": {
			wl: utiltestingapi.MakeWorkload("c", testWorkloadNS).
				Label("tier", "batch").
				DeletionTimestamp(time.Now()).
				Obj(),
			want: false,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := r.shouldEnqueueWorkload(tc.wl); got != tc.want {
				t.Errorf("shouldEnqueueWorkload()=%v want %v", got, tc.want)
			}
		})
	}
}

func ctrlRequest(name types.NamespacedName) ctrl.Request {
	return ctrl.Request{NamespacedName: name}
}
