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

package capacitybuffer

import (
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
)

func TestACReconcileCondition(t *testing.T) {
	previousTransition := metav1.NewTime(time.Date(2024, time.January, 1, 0, 0, 0, 0, time.UTC))
	oldCondition := func(status metav1.ConditionStatus, reason, message string) *metav1.Condition {
		return &metav1.Condition{
			Type:               kueue.AdmissionCheckActive,
			Status:             status,
			Reason:             reason,
			Message:            message,
			ObservedGeneration: 1,
			LastTransitionTime: previousTransition,
		}
	}

	tests := map[string]struct {
		generation       int64
		refName          string
		existingConfigs  []string
		previous         *metav1.Condition
		wantStatus       metav1.ConditionStatus
		wantReason       string
		wantMessage      string
		wantStatusWrites int
		wantTransition   bool
	}{
		"first valid reference publishes Active": {
			generation: 1, refName: "config-a", existingConfigs: []string{"config-a"},
			wantStatus: metav1.ConditionTrue, wantReason: "Active",
			wantMessage: "The admission check is active", wantStatusWrites: 1,
		},
		"first invalid reference publishes error": {
			generation: 1, refName: "missing-a",
			wantStatus: metav1.ConditionFalse, wantReason: "BadParametersRef",
			wantMessage: "missing-a", wantStatusWrites: 1,
		},
		"valid reference changes but Active stays True": {
			generation: 2, refName: "config-b", existingConfigs: []string{"config-b"},
			previous:   oldCondition(metav1.ConditionTrue, "Active", "The admission check is active"),
			wantStatus: metav1.ConditionTrue, wantReason: "Active",
			wantMessage: "The admission check is active", wantStatusWrites: 1,
		},
		"invalid reference changes but Active stays False": {
			generation: 2, refName: "missing-b",
			previous:   oldCondition(metav1.ConditionFalse, "BadParametersRef", "capacitybufferconfigs.kueue.x-k8s.io \"missing-a\" not found"),
			wantStatus: metav1.ConditionFalse, wantReason: "BadParametersRef",
			wantMessage: "missing-b", wantStatusWrites: 1,
		},
		"corrects stale reason and message without status transition": {
			generation: 1, refName: "config-a", existingConfigs: []string{"config-a"},
			previous:   oldCondition(metav1.ConditionTrue, "StaleReason", "stale message"),
			wantStatus: metav1.ConditionTrue, wantReason: "Active",
			wantMessage: "The admission check is active", wantStatusWrites: 1,
		},
		"False to True changes transition time": {
			generation: 2, refName: "config-a", existingConfigs: []string{"config-a"},
			previous:   oldCondition(metav1.ConditionFalse, "BadParametersRef", "missing config"),
			wantStatus: metav1.ConditionTrue, wantReason: "Active",
			wantMessage: "The admission check is active", wantStatusWrites: 1, wantTransition: true,
		},
		"True to False changes transition time": {
			generation: 2, refName: "missing-b",
			previous:   oldCondition(metav1.ConditionTrue, "Active", "The admission check is active"),
			wantStatus: metav1.ConditionFalse, wantReason: "BadParametersRef",
			wantMessage: "missing-b", wantStatusWrites: 1, wantTransition: true,
		},
		"unchanged Active condition skips status write": {
			generation: 1, refName: "config-a", existingConfigs: []string{"config-a"},
			previous:   oldCondition(metav1.ConditionTrue, "Active", "The admission check is active"),
			wantStatus: metav1.ConditionTrue, wantReason: "Active",
			wantMessage: "The admission check is active", wantStatusWrites: 0,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)
			builder := utiltestingapi.MakeAdmissionCheck("buffer-check").
				ControllerName(kueuealpha.CapacityBufferControllerName).
				Parameters(kueuealpha.SchemeGroupVersion.Group, "CapacityBufferConfig", tc.refName).
				Generation(tc.generation)
			if tc.previous != nil {
				builder.Condition(*tc.previous)
			}
			ac := builder.Obj()
			objects := []client.Object{ac}
			for _, name := range tc.existingConfigs {
				objects = append(objects, &kueuealpha.CapacityBufferConfig{Name: name})
			}

			statusWrites := 0
			k8sClient := fake.NewClientBuilder().
				WithScheme(newScheme()).
				WithObjects(objects...).
				WithStatusSubresource(ac).
				WithInterceptorFuncs(interceptor.Funcs{SubResourceUpdate: utiltesting.CountSubResourceUpdates(&statusWrites)}).
				Build()
			controller, err := NewController(k8sClient, &utiltesting.EventRecorder{}, nil)
			if err != nil {
				t.Fatalf("Creating capacity buffer controller: %v", err)
			}
			r := &acReconciler{client: k8sClient, helper: controller.helper}
			request := reconcile.Request{NamespacedName: client.ObjectKeyFromObject(ac)}
			if _, err := r.Reconcile(ctx, request); err != nil {
				t.Fatalf("Reconciling admission check: %v", err)
			}
			if statusWrites != tc.wantStatusWrites {
				t.Fatalf("Status writes after reconciliation: got %d, want %d", statusWrites, tc.wantStatusWrites)
			}

			got := &kueue.AdmissionCheck{}
			if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(ac), got); err != nil {
				t.Fatalf("Getting admission check: %v", err)
			}
			condition := apimeta.FindStatusCondition(got.Status.Conditions, kueue.AdmissionCheckActive)
			if condition == nil {
				t.Fatal("Active condition not published")
			}
			if condition.Status != tc.wantStatus || condition.Reason != tc.wantReason || condition.ObservedGeneration != tc.generation {
				t.Errorf("Unexpected Active condition: got %#v; want Status=%s Reason=%s ObservedGeneration=%d",
					condition, tc.wantStatus, tc.wantReason, tc.generation)
			}
			if tc.wantStatus == metav1.ConditionTrue {
				if condition.Message != tc.wantMessage {
					t.Errorf("Active message: got %q, want %q", condition.Message, tc.wantMessage)
				}
			} else if !strings.Contains(condition.Message, tc.wantMessage) {
				t.Errorf("Active error message %q does not mention %q", condition.Message, tc.wantMessage)
			}
			if tc.previous == nil {
				if condition.LastTransitionTime.IsZero() {
					t.Error("New condition has zero lastTransitionTime")
				}
			} else if same := condition.LastTransitionTime.Equal(&tc.previous.LastTransitionTime); same == tc.wantTransition {
				t.Errorf("lastTransitionTime changed=%t; want %t", !same, tc.wantTransition)
			}

			// Reconciliation of an unchanged object must not issue another status write.
			firstCondition := *condition
			if _, err := r.Reconcile(ctx, request); err != nil {
				t.Fatalf("Repeating admission check reconciliation: %v", err)
			}
			if statusWrites != tc.wantStatusWrites {
				t.Errorf("Repeated reconciliation wrote status: got %d writes, want %d", statusWrites, tc.wantStatusWrites)
			}
			if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(ac), got); err != nil {
				t.Fatalf("Getting admission check after repeat reconciliation: %v", err)
			}
			if diff := cmp.Diff(&firstCondition, apimeta.FindStatusCondition(got.Status.Conditions, kueue.AdmissionCheckActive)); diff != "" {
				t.Errorf("Condition changed on a no-op reconcile (-want/+got):\n%s", diff)
			}
		})
	}
}
