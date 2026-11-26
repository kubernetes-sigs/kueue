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
	newTransition := metav1.Time{}
	check := func(refName string, generation int64, conditions ...metav1.Condition) *kueue.AdmissionCheck {
		ac := utiltestingapi.MakeAdmissionCheck("buffer-check").
			ControllerName(kueuealpha.CapacityBufferControllerName).
			Parameters(kueuealpha.SchemeGroupVersion.Group, "CapacityBufferConfig", refName).
			Generation(generation).
			Obj()
		ac.Status.Conditions = conditions
		return ac
	}
	active := func(generation int64, transition metav1.Time) metav1.Condition {
		return metav1.Condition{
			Type:               kueue.AdmissionCheckActive,
			Status:             metav1.ConditionTrue,
			Reason:             "Active",
			Message:            "The admission check is active",
			ObservedGeneration: generation,
			LastTransitionTime: transition,
		}
	}
	inactive := func(generation int64, message string, transition metav1.Time) metav1.Condition {
		return metav1.Condition{
			Type:               kueue.AdmissionCheckActive,
			Status:             metav1.ConditionFalse,
			Reason:             "BadParametersRef",
			Message:            message,
			ObservedGeneration: generation,
			LastTransitionTime: transition,
		}
	}

	const missingA = "capacitybufferconfigs.kueue.x-k8s.io \"missing-a\" not found"
	const missingB = "capacitybufferconfigs.kueue.x-k8s.io \"missing-b\" not found"
	otherCondition := metav1.Condition{
		Type:               "OtherCondition",
		Status:             metav1.ConditionTrue,
		Reason:             "Unchanged",
		Message:            "Unrelated condition",
		ObservedGeneration: 1,
		LastTransitionTime: previousTransition,
	}

	cases := map[string]struct {
		check            *kueue.AdmissionCheck
		configs          []string
		wantCheck        *kueue.AdmissionCheck
		wantStatusWrites int
	}{
		"first valid reference publishes Active": {
			check:            check("config-a", 1),
			configs:          []string{"config-a"},
			wantCheck:        check("config-a", 1, active(1, newTransition)),
			wantStatusWrites: 1,
		},
		"first invalid reference publishes error": {
			check:            check("missing-a", 1),
			wantCheck:        check("missing-a", 1, inactive(1, missingA, newTransition)),
			wantStatusWrites: 1,
		},
		"valid reference changes but Active stays True": {
			check:            check("config-b", 2, active(1, previousTransition), otherCondition),
			configs:          []string{"config-b"},
			wantCheck:        check("config-b", 2, active(2, previousTransition), otherCondition),
			wantStatusWrites: 1,
		},
		"invalid reference changes but Active stays False": {
			check:            check("missing-b", 2, inactive(1, missingA, previousTransition)),
			wantCheck:        check("missing-b", 2, inactive(2, missingB, previousTransition)),
			wantStatusWrites: 1,
		},
		"corrects stale reason and message without status transition": {
			check: check("config-a", 1, metav1.Condition{
				Type:               kueue.AdmissionCheckActive,
				Status:             metav1.ConditionTrue,
				Reason:             "StaleReason",
				Message:            "stale message",
				ObservedGeneration: 1,
				LastTransitionTime: previousTransition,
			}),
			configs:          []string{"config-a"},
			wantCheck:        check("config-a", 1, active(1, previousTransition)),
			wantStatusWrites: 1,
		},
		"False to True changes transition time": {
			check:            check("config-a", 2, inactive(1, missingA, previousTransition)),
			configs:          []string{"config-a"},
			wantCheck:        check("config-a", 2, active(2, newTransition)),
			wantStatusWrites: 1,
		},
		"True to False changes transition time": {
			check:            check("missing-b", 2, active(1, previousTransition)),
			wantCheck:        check("missing-b", 2, inactive(2, missingB, newTransition)),
			wantStatusWrites: 1,
		},
		"unchanged Active condition skips status write": {
			check:     check("config-a", 1, active(1, previousTransition)),
			configs:   []string{"config-a"},
			wantCheck: check("config-a", 1, active(1, previousTransition)),
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

			// A zero expected transition time denotes a newly generated timestamp.
			// Validate it before normalizing just that field for the full-object diff.
			want := tc.wantCheck.DeepCopy()
			for i := range want.Status.Conditions {
				condition := &want.Status.Conditions[i]
				if !condition.LastTransitionTime.IsZero() {
					continue
				}
				actual := apimeta.FindStatusCondition(got.Status.Conditions, condition.Type)
				if actual == nil || actual.LastTransitionTime.IsZero() {
					t.Fatalf("Missing transition time for condition %q", condition.Type)
				}
				previous := apimeta.FindStatusCondition(tc.check.Status.Conditions, condition.Type)
				if previous != nil && !actual.LastTransitionTime.Time.After(previous.LastTransitionTime.Time) {
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
