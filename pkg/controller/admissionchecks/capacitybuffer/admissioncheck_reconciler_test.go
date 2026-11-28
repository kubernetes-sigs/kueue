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
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
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

func TestACReconcile(t *testing.T) {
	previousTransition := metav1.NewTime(time.Date(2024, time.January, 1, 0, 0, 0, 0, time.UTC))
	// Condition uses SetStatusCondition, which fills a zero transition time.
	// Use a distinct, non-zero marker for expected newly generated timestamps.
	newTransition := metav1.NewTime(time.Date(2024, time.January, 2, 0, 0, 0, 0, time.UTC))

	cases := map[string]struct {
		check            *kueue.AdmissionCheck
		configs          []string
		wantCheck        *kueue.AdmissionCheck
		wantStatusWrites int
	}{
		"first valid reference publishes Active": {
			check: utiltestingapi.MakeAdmissionCheck("buffer-check").
				ControllerName(kueuealpha.CapacityBufferControllerName).
				Parameters(kueuealpha.SchemeGroupVersion.Group, "CapacityBufferConfig", "config-a").
				Generation(1).
				Obj(),
			configs: []string{"config-a"},
			wantCheck: utiltestingapi.MakeAdmissionCheck("buffer-check").
				ControllerName(kueuealpha.CapacityBufferControllerName).
				Parameters(kueuealpha.SchemeGroupVersion.Group, "CapacityBufferConfig", "config-a").
				Generation(1).
				Condition(metav1.Condition{
					Type:               kueue.AdmissionCheckActive,
					Status:             metav1.ConditionTrue,
					Reason:             "Active",
					Message:            "The admission check is active",
					ObservedGeneration: 1,
					LastTransitionTime: newTransition,
				}).
				Obj(),
			wantStatusWrites: 1,
		},
		"first invalid reference publishes error": {
			check: utiltestingapi.MakeAdmissionCheck("buffer-check").
				ControllerName(kueuealpha.CapacityBufferControllerName).
				Parameters(kueuealpha.SchemeGroupVersion.Group, "CapacityBufferConfig", "missing-a").
				Generation(1).
				Obj(),
			wantCheck: utiltestingapi.MakeAdmissionCheck("buffer-check").
				ControllerName(kueuealpha.CapacityBufferControllerName).
				Parameters(kueuealpha.SchemeGroupVersion.Group, "CapacityBufferConfig", "missing-a").
				Generation(1).
				Condition(metav1.Condition{
					Type:               kueue.AdmissionCheckActive,
					Status:             metav1.ConditionFalse,
					Reason:             "BadParametersRef",
					Message:            "capacitybufferconfigs.kueue.x-k8s.io \"missing-a\" not found",
					ObservedGeneration: 1,
					LastTransitionTime: newTransition,
				}).
				Obj(),
			wantStatusWrites: 1,
		},
		"valid reference changes but Active stays True": {
			check: utiltestingapi.MakeAdmissionCheck("buffer-check").
				ControllerName(kueuealpha.CapacityBufferControllerName).
				Parameters(kueuealpha.SchemeGroupVersion.Group, "CapacityBufferConfig", "config-b").
				Generation(2).
				Condition(metav1.Condition{
					Type:               kueue.AdmissionCheckActive,
					Status:             metav1.ConditionTrue,
					Reason:             "Active",
					Message:            "The admission check is active",
					ObservedGeneration: 1,
					LastTransitionTime: previousTransition,
				}).
				Condition(metav1.Condition{
					Type:               "OtherCondition",
					Status:             metav1.ConditionTrue,
					Reason:             "Unchanged",
					Message:            "Unrelated condition",
					ObservedGeneration: 1,
					LastTransitionTime: previousTransition,
				}).
				Obj(),
			configs: []string{"config-b"},
			wantCheck: utiltestingapi.MakeAdmissionCheck("buffer-check").
				ControllerName(kueuealpha.CapacityBufferControllerName).
				Parameters(kueuealpha.SchemeGroupVersion.Group, "CapacityBufferConfig", "config-b").
				Generation(2).
				Condition(metav1.Condition{
					Type:               kueue.AdmissionCheckActive,
					Status:             metav1.ConditionTrue,
					Reason:             "Active",
					Message:            "The admission check is active",
					ObservedGeneration: 2,
					LastTransitionTime: previousTransition,
				}).
				Condition(metav1.Condition{
					Type:               "OtherCondition",
					Status:             metav1.ConditionTrue,
					Reason:             "Unchanged",
					Message:            "Unrelated condition",
					ObservedGeneration: 1,
					LastTransitionTime: previousTransition,
				}).
				Obj(),
			wantStatusWrites: 1,
		},
		"invalid reference changes but Active stays False": {
			check: utiltestingapi.MakeAdmissionCheck("buffer-check").
				ControllerName(kueuealpha.CapacityBufferControllerName).
				Parameters(kueuealpha.SchemeGroupVersion.Group, "CapacityBufferConfig", "missing-b").
				Generation(2).
				Condition(metav1.Condition{
					Type:               kueue.AdmissionCheckActive,
					Status:             metav1.ConditionFalse,
					Reason:             "BadParametersRef",
					Message:            "capacitybufferconfigs.kueue.x-k8s.io \"missing-a\" not found",
					ObservedGeneration: 1,
					LastTransitionTime: previousTransition,
				}).
				Obj(),
			wantCheck: utiltestingapi.MakeAdmissionCheck("buffer-check").
				ControllerName(kueuealpha.CapacityBufferControllerName).
				Parameters(kueuealpha.SchemeGroupVersion.Group, "CapacityBufferConfig", "missing-b").
				Generation(2).
				Condition(metav1.Condition{
					Type:               kueue.AdmissionCheckActive,
					Status:             metav1.ConditionFalse,
					Reason:             "BadParametersRef",
					Message:            "capacitybufferconfigs.kueue.x-k8s.io \"missing-b\" not found",
					ObservedGeneration: 2,
					LastTransitionTime: previousTransition,
				}).
				Obj(),
			wantStatusWrites: 1,
		},
		"corrects stale reason and message without status transition": {
			check: utiltestingapi.MakeAdmissionCheck("buffer-check").
				ControllerName(kueuealpha.CapacityBufferControllerName).
				Parameters(kueuealpha.SchemeGroupVersion.Group, "CapacityBufferConfig", "config-a").
				Generation(1).
				Condition(metav1.Condition{
					Type:               kueue.AdmissionCheckActive,
					Status:             metav1.ConditionTrue,
					Reason:             "StaleReason",
					Message:            "stale message",
					ObservedGeneration: 1,
					LastTransitionTime: previousTransition,
				}).
				Obj(),
			configs: []string{"config-a"},
			wantCheck: utiltestingapi.MakeAdmissionCheck("buffer-check").
				ControllerName(kueuealpha.CapacityBufferControllerName).
				Parameters(kueuealpha.SchemeGroupVersion.Group, "CapacityBufferConfig", "config-a").
				Generation(1).
				Condition(metav1.Condition{
					Type:               kueue.AdmissionCheckActive,
					Status:             metav1.ConditionTrue,
					Reason:             "Active",
					Message:            "The admission check is active",
					ObservedGeneration: 1,
					LastTransitionTime: previousTransition,
				}).
				Obj(),
			wantStatusWrites: 1,
		},
		"False to True changes transition time": {
			check: utiltestingapi.MakeAdmissionCheck("buffer-check").
				ControllerName(kueuealpha.CapacityBufferControllerName).
				Parameters(kueuealpha.SchemeGroupVersion.Group, "CapacityBufferConfig", "config-a").
				Generation(2).
				Condition(metav1.Condition{
					Type:               kueue.AdmissionCheckActive,
					Status:             metav1.ConditionFalse,
					Reason:             "BadParametersRef",
					Message:            "capacitybufferconfigs.kueue.x-k8s.io \"missing-a\" not found",
					ObservedGeneration: 1,
					LastTransitionTime: previousTransition,
				}).
				Obj(),
			configs: []string{"config-a"},
			wantCheck: utiltestingapi.MakeAdmissionCheck("buffer-check").
				ControllerName(kueuealpha.CapacityBufferControllerName).
				Parameters(kueuealpha.SchemeGroupVersion.Group, "CapacityBufferConfig", "config-a").
				Generation(2).
				Condition(metav1.Condition{
					Type:               kueue.AdmissionCheckActive,
					Status:             metav1.ConditionTrue,
					Reason:             "Active",
					Message:            "The admission check is active",
					ObservedGeneration: 2,
					LastTransitionTime: newTransition,
				}).
				Obj(),
			wantStatusWrites: 1,
		},
		"True to False changes transition time": {
			check: utiltestingapi.MakeAdmissionCheck("buffer-check").
				ControllerName(kueuealpha.CapacityBufferControllerName).
				Parameters(kueuealpha.SchemeGroupVersion.Group, "CapacityBufferConfig", "missing-b").
				Generation(2).
				Condition(metav1.Condition{
					Type:               kueue.AdmissionCheckActive,
					Status:             metav1.ConditionTrue,
					Reason:             "Active",
					Message:            "The admission check is active",
					ObservedGeneration: 1,
					LastTransitionTime: previousTransition,
				}).
				Obj(),
			wantCheck: utiltestingapi.MakeAdmissionCheck("buffer-check").
				ControllerName(kueuealpha.CapacityBufferControllerName).
				Parameters(kueuealpha.SchemeGroupVersion.Group, "CapacityBufferConfig", "missing-b").
				Generation(2).
				Condition(metav1.Condition{
					Type:               kueue.AdmissionCheckActive,
					Status:             metav1.ConditionFalse,
					Reason:             "BadParametersRef",
					Message:            "capacitybufferconfigs.kueue.x-k8s.io \"missing-b\" not found",
					ObservedGeneration: 2,
					LastTransitionTime: newTransition,
				}).
				Obj(),
			wantStatusWrites: 1,
		},
		"unchanged Active condition skips status write": {
			check: utiltestingapi.MakeAdmissionCheck("buffer-check").
				ControllerName(kueuealpha.CapacityBufferControllerName).
				Parameters(kueuealpha.SchemeGroupVersion.Group, "CapacityBufferConfig", "config-a").
				Generation(1).
				Condition(metav1.Condition{
					Type:               kueue.AdmissionCheckActive,
					Status:             metav1.ConditionTrue,
					Reason:             "Active",
					Message:            "The admission check is active",
					ObservedGeneration: 1,
					LastTransitionTime: previousTransition,
				}).
				Obj(),
			configs: []string{"config-a"},
			wantCheck: utiltestingapi.MakeAdmissionCheck("buffer-check").
				ControllerName(kueuealpha.CapacityBufferControllerName).
				Parameters(kueuealpha.SchemeGroupVersion.Group, "CapacityBufferConfig", "config-a").
				Generation(1).
				Condition(metav1.Condition{
					Type:               kueue.AdmissionCheckActive,
					Status:             metav1.ConditionTrue,
					Reason:             "Active",
					Message:            "The admission check is active",
					ObservedGeneration: 1,
					LastTransitionTime: previousTransition,
				}).
				Obj(),
		},
	}

	cmpOpts := cmp.Options{cmpopts.IgnoreFields(metav1.ObjectMeta{}, "ResourceVersion")}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)
			objects := []client.Object{tc.check.DeepCopy()}
			for _, name := range tc.configs {
				objects = append(objects, &kueuealpha.CapacityBufferConfig{Name: name})
			}

			statusWrites := 0
			k8sClient := fake.NewClientBuilder().
				WithScheme(newScheme()).
				WithObjects(objects...).
				WithStatusSubresource(tc.check).
				WithInterceptorFuncs(interceptor.Funcs{SubResourceUpdate: utiltesting.CountSubResourceUpdates(&statusWrites)}).
				Build()
			controller, err := NewController(k8sClient, &utiltesting.EventRecorder{}, nil)
			if err != nil {
				t.Fatalf("Creating capacity buffer controller: %v", err)
			}
			r := &acReconciler{client: k8sClient, helper: controller.helper}
			request := reconcile.Request{NamespacedName: client.ObjectKeyFromObject(tc.check)}
			if _, err := r.Reconcile(ctx, request); err != nil {
				t.Fatalf("Reconciling admission check: %v", err)
			}
			if statusWrites != tc.wantStatusWrites {
				t.Errorf("Status writes after reconciliation: got %d, want %d", statusWrites, tc.wantStatusWrites)
			}

			got := &kueue.AdmissionCheck{}
			if err := k8sClient.Get(ctx, request.NamespacedName, got); err != nil {
				t.Fatalf("Getting admission check: %v", err)
			}

			// The marker denotes a newly generated transition time.
			// Validate it before normalizing just that field for the full-object diff.
			want := tc.wantCheck.DeepCopy()
			for i := range want.Status.Conditions {
				condition := &want.Status.Conditions[i]
				if !condition.LastTransitionTime.Equal(&newTransition) {
					continue
				}
				actual := apimeta.FindStatusCondition(got.Status.Conditions, condition.Type)
				if actual == nil || actual.LastTransitionTime.IsZero() {
					t.Fatalf("Missing transition time for condition %q", condition.Type)
				}
				previous := apimeta.FindStatusCondition(tc.check.Status.Conditions, condition.Type)
				if previous != nil && !actual.LastTransitionTime.After(previous.LastTransitionTime.Time) {
					t.Errorf("Expected a newer transition time for condition %q", condition.Type)
				}
				condition.LastTransitionTime = actual.LastTransitionTime
			}
			if diff := cmp.Diff(want, got, cmpOpts...); diff != "" {
				t.Errorf("AdmissionCheck after reconcile (-want/+got):\n%s", diff)
			}

			// A second reconciliation must leave the entire object unchanged.
			first := got.DeepCopy()
			if _, err := r.Reconcile(ctx, request); err != nil {
				t.Fatalf("Repeating admission check reconciliation: %v", err)
			}
			if statusWrites != tc.wantStatusWrites {
				t.Errorf("Repeated reconciliation wrote status: got %d writes, want %d", statusWrites, tc.wantStatusWrites)
			}
			got = &kueue.AdmissionCheck{}
			if err := k8sClient.Get(ctx, request.NamespacedName, got); err != nil {
				t.Fatalf("Getting admission check after repeat reconciliation: %v", err)
			}
			if diff := cmp.Diff(first, got); diff != "" {
				t.Errorf("AdmissionCheck changed on a no-op reconcile (-want/+got):\n%s", diff)
			}
		})
	}
}
