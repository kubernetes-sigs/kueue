/*
Copyright 2023 The Kubernetes Authors.

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

package provisioning

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
)

func TestReconcileAdmissionCheck(t *testing.T) {
	cases := map[string]struct {
		configs       []kueue.ProvisioningRequestConfig
		check         *kueue.AdmissionCheck
		wantCondition *metav1.Condition
	}{
		"unrelated check": {
			check: utiltesting.MakeAdmissionCheck("check1").
				ControllerName("other-controller").
				Obj(),
		},
		"no parameters specified": {
			check: utiltesting.MakeAdmissionCheck("check1").
				ControllerName(kueue.ProvisioningRequestControllerName).
				Generation(1).
				Obj(),
			wantCondition: &metav1.Condition{
				Type:               kueue.AdmissionCheckActive,
				Status:             metav1.ConditionFalse,
				Reason:             "BadParametersRef",
				Message:            "missing parameters reference",
				ObservedGeneration: 1,
			},
		},
		"bad ref group": {
			check: utiltesting.MakeAdmissionCheck("check1").
				Parameters("bad.group", ConfigKind, "config1").
				ControllerName(kueue.ProvisioningRequestControllerName).
				Generation(1).
				Obj(),
			wantCondition: &metav1.Condition{
				Type:               kueue.AdmissionCheckActive,
				Status:             metav1.ConditionFalse,
				Reason:             "BadParametersRef",
				Message:            "wrong group \"bad.group\", expecting \"kueue.x-k8s.io\": bad parameters reference",
				ObservedGeneration: 1,
			},
		},
		"bad ref kind": {
			check: utiltesting.MakeAdmissionCheck("check1").
				Parameters(kueue.GroupVersion.Group, "BadKind", "config1").
				ControllerName(kueue.ProvisioningRequestControllerName).
				Generation(1).
				Obj(),
			wantCondition: &metav1.Condition{
				Type:               kueue.AdmissionCheckActive,
				Status:             metav1.ConditionFalse,
				Reason:             "BadParametersRef",
				Message:            "wrong kind \"BadKind\", expecting \"ProvisioningRequestConfig\": bad parameters reference",
				ObservedGeneration: 1,
			},
		},
		"config missing": {
			check: utiltesting.MakeAdmissionCheck("check1").
				Parameters(kueue.GroupVersion.Group, ConfigKind, "config1").
				ControllerName(kueue.ProvisioningRequestControllerName).
				Generation(1).
				Obj(),
			wantCondition: &metav1.Condition{
				Type:               kueue.AdmissionCheckActive,
				Status:             metav1.ConditionFalse,
				Reason:             "BadParametersRef",
				Message:            "provisioningrequestconfigs.kueue.x-k8s.io \"config1\" not found",
				ObservedGeneration: 1,
			},
		},
		"config found": {
			check: utiltesting.MakeAdmissionCheck("check1").
				Parameters(kueue.GroupVersion.Group, ConfigKind, "config1").
				ControllerName(kueue.ProvisioningRequestControllerName).
				Generation(1).
				Obj(),
			configs: []kueue.ProvisioningRequestConfig{*utiltesting.MakeProvisioningRequestConfig("config1").Obj()},
			wantCondition: &metav1.Condition{
				Type:               kueue.AdmissionCheckActive,
				Status:             metav1.ConditionTrue,
				Reason:             "Active",
				Message:            "The admission check is active",
				ObservedGeneration: 1,
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			builder, ctx := getClientBuilder()

			builder = builder.WithObjects(tc.check)
			builder = builder.WithStatusSubresource(tc.check)

			builder = builder.WithLists(&kueue.ProvisioningRequestConfigList{Items: tc.configs})

			k8sclient := builder.Build()

			helper, err := newProvisioningConfigHelper(k8sclient)
			if err != nil {
				t.Errorf("unable to create the config helper: %s", err)
				return
			}
			reconciler := acReconciler{
				client: k8sclient,
				helper: helper,
			}

			req := reconcile.Request{
				NamespacedName: types.NamespacedName{
					Name: tc.check.Name,
				},
			}
			_, gotReconcileError := reconciler.Reconcile(ctx, req)
			if gotReconcileError != nil {
				t.Errorf("unexpected reconcile error: %s", gotReconcileError)
			}

			gotAc := &kueue.AdmissionCheck{}
			if err := k8sclient.Get(ctx, types.NamespacedName{Name: tc.check.Name}, gotAc); err != nil {
				t.Errorf("unexpected error getting check %q", tc.check.Name)
			}

			gotCondition := apimeta.FindStatusCondition(gotAc.Status.Conditions, kueue.AdmissionCheckActive)
			if diff := cmp.Diff(tc.wantCondition, gotCondition, acCmpOptions...); diff != "" {
				t.Errorf("unexpected check %q (-want/+got):\n%s", tc.check.Name, diff)
			}
		})
	}
}
