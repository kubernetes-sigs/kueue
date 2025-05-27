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

package multikueue

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
)

func TestReconcile(t *testing.T) {
	cases := map[string]struct {
		checks       []kueue.AdmissionCheck
		clusters     []kueue.MultiKueueCluster
		reconcileFor string
		configs      []kueue.MultiKueueConfig

		wantChecks []kueue.AdmissionCheck
		wantError  error
	}{
		"missing admissioncheck": {
			reconcileFor: "missing-ac",
		},
		"missing config": {
			reconcileFor: "ac1",
			checks: []kueue.AdmissionCheck{
				*utiltesting.MakeAdmissionCheck("ac1").
					ControllerName(kueue.MultiKueueControllerName).
					Parameters(kueue.GroupVersion.Group, "MultiKueueConfig", "config1").
					Generation(1).
					Obj(),
			},
			wantChecks: []kueue.AdmissionCheck{
				*utiltesting.MakeAdmissionCheck("ac1").
					ControllerName(kueue.MultiKueueControllerName).
					Parameters(kueue.GroupVersion.Group, "MultiKueueConfig", "config1").
					Condition(metav1.Condition{
						Type:               kueue.AdmissionCheckActive,
						Status:             metav1.ConditionFalse,
						Reason:             "BadConfig",
						Message:            `Cannot load the AdmissionChecks parameters: multikueueconfigs.kueue.x-k8s.io "config1" not found`,
						ObservedGeneration: 1,
					}).
					Obj(),
			},
		},
		"unmanaged": {
			reconcileFor: "ac1",
			checks: []kueue.AdmissionCheck{
				*utiltesting.MakeAdmissionCheck("ac1").
					ControllerName("not-multikueue").
					Obj(),
			},
			wantChecks: []kueue.AdmissionCheck{
				*utiltesting.MakeAdmissionCheck("ac1").
					ControllerName("not-multikueue").
					Obj(),
			},
		},
		"missing cluster": {
			reconcileFor: "ac1",
			checks: []kueue.AdmissionCheck{
				*utiltesting.MakeAdmissionCheck("ac1").
					ControllerName(kueue.MultiKueueControllerName).
					Parameters(kueue.GroupVersion.Group, "MultiKueueConfig", "config1").
					Generation(1).
					Obj(),
			},
			configs: []kueue.MultiKueueConfig{
				*utiltesting.MakeMultiKueueConfig("config1").Clusters("worker1").Obj(),
			},
			wantChecks: []kueue.AdmissionCheck{
				*utiltesting.MakeAdmissionCheck("ac1").
					ControllerName(kueue.MultiKueueControllerName).
					Parameters(kueue.GroupVersion.Group, "MultiKueueConfig", "config1").
					Condition(metav1.Condition{
						Type:               kueue.AdmissionCheckActive,
						Status:             metav1.ConditionFalse,
						Reason:             "NoUsableClusters",
						Message:            "Missing clusters: [worker1]",
						ObservedGeneration: 1,
					}).
					Obj(),
			},
		},
		"inactive cluster": {
			reconcileFor: "ac1",
			checks: []kueue.AdmissionCheck{
				*utiltesting.MakeAdmissionCheck("ac1").
					ControllerName(kueue.MultiKueueControllerName).
					Parameters(kueue.GroupVersion.Group, "MultiKueueConfig", "config1").
					Generation(1).
					Obj(),
			},
			configs: []kueue.MultiKueueConfig{
				*utiltesting.MakeMultiKueueConfig("config1").Clusters("worker1").Obj(),
			},
			clusters: []kueue.MultiKueueCluster{
				*utiltesting.MakeMultiKueueCluster("worker1").
					Active(metav1.ConditionFalse, "ByTest", "by test", 1).
					Obj(),
			},
			wantChecks: []kueue.AdmissionCheck{
				*utiltesting.MakeAdmissionCheck("ac1").
					ControllerName(kueue.MultiKueueControllerName).
					Parameters(kueue.GroupVersion.Group, "MultiKueueConfig", "config1").
					Condition(metav1.Condition{
						Type:               kueue.AdmissionCheckActive,
						Status:             metav1.ConditionFalse,
						Reason:             "NoUsableClusters",
						Message:            "Inactive clusters: [worker1]",
						ObservedGeneration: 1,
					}).
					Obj(),
			},
		},
		"all clusters missing or inactive": {
			reconcileFor: "ac1",
			checks: []kueue.AdmissionCheck{
				*utiltesting.MakeAdmissionCheck("ac1").
					ControllerName(kueue.MultiKueueControllerName).
					Parameters(kueue.GroupVersion.Group, "MultiKueueConfig", "config1").
					Generation(1).
					Obj(),
			},
			configs: []kueue.MultiKueueConfig{
				*utiltesting.MakeMultiKueueConfig("config1").Clusters("worker1", "worker2", "worker3").Obj(),
			},
			clusters: []kueue.MultiKueueCluster{
				*utiltesting.MakeMultiKueueCluster("worker1").
					Active(metav1.ConditionFalse, "ByTest", "by test", 1).
					Obj(),
				*utiltesting.MakeMultiKueueCluster("worker2").
					Active(metav1.ConditionFalse, "ByTest", "by test", 1).
					Obj(),
			},
			wantChecks: []kueue.AdmissionCheck{
				*utiltesting.MakeAdmissionCheck("ac1").
					ControllerName(kueue.MultiKueueControllerName).
					Parameters(kueue.GroupVersion.Group, "MultiKueueConfig", "config1").
					Condition(metav1.Condition{
						Type:               kueue.AdmissionCheckActive,
						Status:             metav1.ConditionFalse,
						Reason:             "NoUsableClusters",
						Message:            "Missing clusters: [worker3], Inactive clusters: [worker1 worker2]",
						ObservedGeneration: 1,
					}).
					Obj(),
			},
		},
		"partially active": {
			reconcileFor: "ac1",
			checks: []kueue.AdmissionCheck{
				*utiltesting.MakeAdmissionCheck("ac1").
					ControllerName(kueue.MultiKueueControllerName).
					Parameters(kueue.GroupVersion.Group, "MultiKueueConfig", "config1").
					Generation(1).
					Obj(),
			},
			configs: []kueue.MultiKueueConfig{
				*utiltesting.MakeMultiKueueConfig("config1").Clusters("worker1", "worker2", "worker3").Obj(),
			},
			clusters: []kueue.MultiKueueCluster{
				*utiltesting.MakeMultiKueueCluster("worker1").
					Active(metav1.ConditionFalse, "ByTest", "by test", 1).
					Obj(),
				*utiltesting.MakeMultiKueueCluster("worker2").
					Active(metav1.ConditionTrue, "ByTest", "by test", 1).
					Obj(),
			},
			wantChecks: []kueue.AdmissionCheck{
				*utiltesting.MakeAdmissionCheck("ac1").
					ControllerName(kueue.MultiKueueControllerName).
					Parameters(kueue.GroupVersion.Group, "MultiKueueConfig", "config1").
					Condition(metav1.Condition{
						Type:               kueue.AdmissionCheckActive,
						Status:             metav1.ConditionTrue,
						Reason:             "SomeActiveClusters",
						Message:            "Missing clusters: [worker3], Inactive clusters: [worker1]",
						ObservedGeneration: 1,
					}).
					Obj(),
			},
		},
		"active": {
			reconcileFor: "ac1",
			checks: []kueue.AdmissionCheck{
				*utiltesting.MakeAdmissionCheck("ac1").
					ControllerName(kueue.MultiKueueControllerName).
					Parameters(kueue.GroupVersion.Group, "MultiKueueConfig", "config1").
					Generation(1).
					Obj(),
			},
			configs: []kueue.MultiKueueConfig{
				*utiltesting.MakeMultiKueueConfig("config1").Clusters("worker1").Obj(),
			},
			clusters: []kueue.MultiKueueCluster{
				*utiltesting.MakeMultiKueueCluster("worker1").
					Active(metav1.ConditionTrue, "ByTest", "by test", 1).
					Obj(),
			},
			wantChecks: []kueue.AdmissionCheck{
				*utiltesting.MakeAdmissionCheck("ac1").
					ControllerName(kueue.MultiKueueControllerName).
					Parameters(kueue.GroupVersion.Group, "MultiKueueConfig", "config1").
					Condition(metav1.Condition{
						Type:               kueue.AdmissionCheckActive,
						Status:             metav1.ConditionTrue,
						Reason:             "Active",
						Message:            `The admission check is active`,
						ObservedGeneration: 1,
					}).
					Obj(),
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			builder := getClientBuilder(t.Context())

			builder = builder.WithLists(
				&kueue.AdmissionCheckList{Items: tc.checks},
				&kueue.MultiKueueConfigList{Items: tc.configs},
				&kueue.MultiKueueClusterList{Items: tc.clusters},
			)

			for _, ac := range tc.checks {
				builder = builder.WithStatusSubresource(ac.DeepCopy())
			}

			c := builder.Build()

			helper, _ := newMultiKueueStoreHelper(c)
			reconciler := newACReconciler(c, helper)

			_, gotErr := reconciler.Reconcile(t.Context(), reconcile.Request{NamespacedName: types.NamespacedName{Name: tc.reconcileFor}})
			if diff := cmp.Diff(tc.wantError, gotErr); diff != "" {
				t.Errorf("unexpected error (-want/+got):\n%s", diff)
			}

			checks := &kueue.AdmissionCheckList{}
			listErr := c.List(t.Context(), checks)

			if listErr != nil {
				t.Errorf("unexpected list checks error: %s", listErr)
			}

			if diff := cmp.Diff(tc.wantChecks, checks.Items, cmpopts.EquateEmpty(),
				cmpopts.IgnoreTypes(metav1.ObjectMeta{}),
				cmpopts.IgnoreFields(metav1.Condition{}, "LastTransitionTime"),
				cmpopts.SortSlices(func(a, b metav1.Condition) bool { return a.Type < b.Type })); diff != "" {
				t.Errorf("unexpected controllers (-want/+got):\n%s", diff)
			}
		})
	}
}
