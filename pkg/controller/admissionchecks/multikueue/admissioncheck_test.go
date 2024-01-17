/*
Copyright 2024 The Kubernetes Authors.

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
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
)

func testRemoteController() *remoteController {
	return &remoteController{
		watchCancel: func() {},
	}
}

func TestReconcile(t *testing.T) {
	cases := map[string]struct {
		checks       []kueue.AdmissionCheck
		controllers  map[string]*remoteController
		reconcileFor string
		configs      []kueuealpha.MultiKueueConfig
		secrets      []corev1.Secret

		wantChecks      []kueue.AdmissionCheck
		wantControllers map[string]*remoteController
		wantError       error
	}{
		"missing admissioncheck": {
			reconcileFor: "missing-ac",
		},
		"removed admissioncheck": {
			reconcileFor: "removed-ac",
			controllers: map[string]*remoteController{
				"removed-ac": testRemoteController(),
			},
		},
		"missing config": {
			reconcileFor: "ac1",
			checks: []kueue.AdmissionCheck{
				*utiltesting.MakeAdmissionCheck("ac1").
					ControllerName(ControllerName).
					Parameters(kueue.GroupVersion.Group, "MultiKueueConfig", "config1").
					Obj(),
			},
			wantChecks: []kueue.AdmissionCheck{
				*utiltesting.MakeAdmissionCheck("ac1").
					ControllerName(ControllerName).
					Parameters(kueue.GroupVersion.Group, "MultiKueueConfig", "config1").
					Condition(metav1.Condition{
						Type:    kueue.AdmissionCheckActive,
						Status:  metav1.ConditionFalse,
						Reason:  "Inactive",
						Message: `Cannot load the AdmissionChecks parameters: multikueueconfigs.kueue.x-k8s.io "config1" not found`,
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
		"missing kubeconfig secret": {
			reconcileFor: "ac1",
			checks: []kueue.AdmissionCheck{
				*utiltesting.MakeAdmissionCheck("ac1").
					ControllerName(ControllerName).
					Parameters(kueue.GroupVersion.Group, "MultiKueueConfig", "config1").
					Obj(),
			},
			configs: []kueuealpha.MultiKueueConfig{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "config1"},
					Spec: kueuealpha.MultiKueueConfigSpec{
						Clusters: []kueuealpha.MultiKueueCluster{
							{
								Name: "worker1",
								KubeconfigRef: kueuealpha.KubeconfigRef{
									Location:     "secret1",
									LocationType: kueuealpha.SecretLocationType,
								},
							},
						},
					},
				},
			},
			wantChecks: []kueue.AdmissionCheck{
				*utiltesting.MakeAdmissionCheck("ac1").
					ControllerName(ControllerName).
					Parameters(kueue.GroupVersion.Group, "MultiKueueConfig", "config1").
					Condition(metav1.Condition{
						Type:    kueue.AdmissionCheckActive,
						Status:  metav1.ConditionFalse,
						Reason:  "Inactive",
						Message: `Cannot load kubeconfigs: getting kubeconfig secret for "worker1": secrets "secret1" not found`,
					}).
					Obj(),
			},
		},
		"missing kubeconfig key in secret": {
			reconcileFor: "ac1",
			checks: []kueue.AdmissionCheck{
				*utiltesting.MakeAdmissionCheck("ac1").
					ControllerName(ControllerName).
					Parameters(kueue.GroupVersion.Group, "MultiKueueConfig", "config1").
					Obj(),
			},
			configs: []kueuealpha.MultiKueueConfig{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "config1"},
					Spec: kueuealpha.MultiKueueConfigSpec{
						Clusters: []kueuealpha.MultiKueueCluster{
							{
								Name: "worker1",
								KubeconfigRef: kueuealpha.KubeconfigRef{
									Location:     "secret1",
									LocationType: kueuealpha.SecretLocationType,
								},
							},
						},
					},
				},
			},
			secrets: []corev1.Secret{
				{
					ObjectMeta: metav1.ObjectMeta{Namespace: TestNamespace, Name: "secret1"},
				},
			},
			wantChecks: []kueue.AdmissionCheck{
				*utiltesting.MakeAdmissionCheck("ac1").
					ControllerName(ControllerName).
					Parameters(kueue.GroupVersion.Group, "MultiKueueConfig", "config1").
					Condition(metav1.Condition{
						Type:    kueue.AdmissionCheckActive,
						Status:  metav1.ConditionFalse,
						Reason:  "Inactive",
						Message: `Cannot load kubeconfigs: getting kubeconfig secret for "worker1": key "kubeconfig" not found in secret "secret1"`,
					}).
					Obj(),
			},
		},
		"invalid kubeconfig in secret": {
			reconcileFor: "ac1",
			checks: []kueue.AdmissionCheck{
				*utiltesting.MakeAdmissionCheck("ac1").
					ControllerName(ControllerName).
					Parameters(kueue.GroupVersion.Group, "MultiKueueConfig", "config1").
					Obj(),
			},
			configs: []kueuealpha.MultiKueueConfig{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "config1"},
					Spec: kueuealpha.MultiKueueConfigSpec{
						Clusters: []kueuealpha.MultiKueueCluster{
							{
								Name: "worker1",
								KubeconfigRef: kueuealpha.KubeconfigRef{
									Location:     "secret1",
									LocationType: kueuealpha.SecretLocationType,
								},
							},
						},
					},
				},
			},
			secrets: []corev1.Secret{
				{
					ObjectMeta: metav1.ObjectMeta{Namespace: TestNamespace, Name: "secret1"},
					Data: map[string][]byte{
						"kubeconfig": []byte("invalid"),
					},
				},
			},
			wantChecks: []kueue.AdmissionCheck{
				*utiltesting.MakeAdmissionCheck("ac1").
					ControllerName(ControllerName).
					Parameters(kueue.GroupVersion.Group, "MultiKueueConfig", "config1").
					Condition(metav1.Condition{
						Type:    kueue.AdmissionCheckActive,
						Status:  metav1.ConditionFalse,
						Reason:  "Inactive",
						Message: `Cannot start MultiKueueClusters controller: cluster "worker1": invalid kubeconfig`,
					}).
					Obj(),
			},
		},
		"valid": {
			reconcileFor: "ac1",
			checks: []kueue.AdmissionCheck{
				*utiltesting.MakeAdmissionCheck("ac1").
					ControllerName(ControllerName).
					Parameters(kueue.GroupVersion.Group, "MultiKueueConfig", "config1").
					Obj(),
			},
			configs: []kueuealpha.MultiKueueConfig{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "config1"},
					Spec: kueuealpha.MultiKueueConfigSpec{
						Clusters: []kueuealpha.MultiKueueCluster{
							{
								Name: "worker1",
								KubeconfigRef: kueuealpha.KubeconfigRef{
									Location:     "secret1",
									LocationType: kueuealpha.SecretLocationType,
								},
							},
						},
					},
				},
			},
			secrets: []corev1.Secret{
				{
					ObjectMeta: metav1.ObjectMeta{Namespace: TestNamespace, Name: "secret1"},
					Data: map[string][]byte{
						"kubeconfig": []byte("good kubeconfig"),
					},
				},
			},
			wantChecks: []kueue.AdmissionCheck{
				*utiltesting.MakeAdmissionCheck("ac1").
					ControllerName(ControllerName).
					Parameters(kueue.GroupVersion.Group, "MultiKueueConfig", "config1").
					Condition(metav1.Condition{
						Type:    kueue.AdmissionCheckActive,
						Status:  metav1.ConditionTrue,
						Reason:  "Active",
						Message: `The admission check is active`,
					}).
					Obj(),
			},
			wantControllers: map[string]*remoteController{
				"ac1": {
					remoteClients: map[string]*remoteClient{
						"worker1": {
							kubeconfig: []byte("good kubeconfig"),
						},
					},
				},
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			builder, ctx := getClientBuilder()

			builder = builder.WithLists(
				&kueue.AdmissionCheckList{Items: tc.checks},
				&kueuealpha.MultiKueueConfigList{Items: tc.configs},
				&corev1.SecretList{Items: tc.secrets},
			)

			for _, ac := range tc.checks {
				builder = builder.WithStatusSubresource(ac.DeepCopy())
			}

			c := builder.Build()

			helper, _ := newMultiKueueStoreHelper(c)
			reconciler := newACController(c, helper, TestNamespace)
			if len(tc.controllers) > 0 {
				reconciler.controllers = tc.controllers
			}
			reconciler.builderOverride = fakeClientBuilder
			_ = reconciler.Start(ctx)

			_, gotErr := reconciler.Reconcile(ctx, reconcile.Request{NamespacedName: types.NamespacedName{Name: tc.reconcileFor}})
			if diff := cmp.Diff(tc.wantError, gotErr); diff != "" {
				t.Errorf("unexpected error (-want/+got):\n%s", diff)
			}

			if diff := cmp.Diff(tc.wantControllers, reconciler.controllers, cmpopts.EquateEmpty(),
				cmp.AllowUnexported(remoteController{}),
				cmpopts.IgnoreFields(remoteController{}, "localClient", "watchCancel", "watchCtx", "wlUpdateCh", "builderOverride"),
				cmp.Comparer(func(a, b remoteClient) bool { return string(a.kubeconfig) == string(b.kubeconfig) })); diff != "" {
				t.Errorf("unexpected controllers (-want/+got):\n%s", diff)
			}

			checks := &kueue.AdmissionCheckList{}
			listErr := c.List(ctx, checks)

			if listErr != nil {
				t.Errorf("unexpected list checks error: %s", listErr)
			}

			if diff := cmp.Diff(tc.wantChecks, checks.Items, cmpopts.EquateEmpty(), cmpopts.IgnoreTypes(metav1.ObjectMeta{}), cmpopts.IgnoreFields(metav1.Condition{}, "LastTransitionTime")); diff != "" {
				t.Errorf("unexpected controllers (-want/+got):\n%s", diff)
			}

		})
	}
}
