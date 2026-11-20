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

package webhooks

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/validation/field"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
)

func TestValidateResourceFlavor(t *testing.T) {
	testcases := []struct {
		name    string
		rf      *kueue.ResourceFlavor
		labels  map[string]string
		wantErr field.ErrorList
	}{
		{
			name: "empty",
			rf:   utiltestingapi.MakeResourceFlavor("resource-flavor").Obj(),
		},
		{
			name: "valid",
			rf: utiltestingapi.MakeResourceFlavor("resource-flavor").
				NodeLabel("foo", "bar").
				Taint(corev1.Taint{
					Key:    "spot",
					Value:  "true",
					Effect: corev1.TaintEffectNoSchedule,
				}).Obj(),
		},
		{
			name: "invalid label name",
			rf:   utiltestingapi.MakeResourceFlavor("resource-flavor").NodeLabel("@abc", "foo").Obj(),
			wantErr: field.ErrorList{
				field.Invalid(field.NewPath("spec", "nodeLabels"), "@abc", "").
					WithOrigin("format=k8s-label-key"),
			},
		},
		{
			name: "invalid label value",
			rf:   utiltestingapi.MakeResourceFlavor("resource-flavor").NodeLabel("foo", "@abc").Obj(),
			wantErr: field.ErrorList{
				field.Invalid(field.NewPath("spec", "nodeLabels"), "@abc", "").
					WithOrigin("format=k8s-label-value"),
			},
		},
		{
			name: "valid toleration value",
			rf: utiltestingapi.MakeResourceFlavor("resource-flavor").
				Toleration(corev1.Toleration{
					Key:      "example.com/gpu",
					Operator: corev1.TolerationOpEqual,
					Value:    "a100",
					Effect:   corev1.TaintEffectNoSchedule,
				}).Obj(),
		},
		{
			name: "Exists toleration with an empty value",
			rf: utiltestingapi.MakeResourceFlavor("resource-flavor").
				Toleration(corev1.Toleration{
					Key:      "example.com/gpu",
					Operator: corev1.TolerationOpExists,
					Effect:   corev1.TaintEffectNoSchedule,
				}).Obj(),
		},
		{
			name: "Equal toleration with an empty value",
			rf: utiltestingapi.MakeResourceFlavor("resource-flavor").
				Toleration(corev1.Toleration{
					Key:      "example.com/gpu",
					Operator: corev1.TolerationOpEqual,
					Value:    "",
					Effect:   corev1.TaintEffectNoSchedule,
				}).Obj(),
		},
		{
			name: "invalid toleration value",
			rf: utiltestingapi.MakeResourceFlavor("resource-flavor").
				Toleration(corev1.Toleration{
					Key:      "example.com/gpu",
					Operator: corev1.TolerationOpEqual,
					Value:    "not a label value!",
					Effect:   corev1.TaintEffectNoSchedule,
				}).Obj(),
			wantErr: field.ErrorList{
				field.Invalid(field.NewPath("spec", "tolerations").Index(0).Child("value"), "not a label value!", ""),
			},
		},
		{
			name: "invalid toleration value with the default operator",
			rf: utiltestingapi.MakeResourceFlavor("resource-flavor").
				Toleration(corev1.Toleration{
					Key:    "example.com/gpu",
					Value:  "not a label value!",
					Effect: corev1.TaintEffectNoSchedule,
				}).Obj(),
			wantErr: field.ErrorList{
				field.Invalid(field.NewPath("spec", "tolerations").Index(0).Child("value"), "not a label value!", ""),
			},
		},
	}

	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			gotErr := ValidateResourceFlavor(tc.rf)
			if diff := cmp.Diff(tc.wantErr, gotErr, cmpopts.IgnoreFields(field.Error{}, "Detail")); diff != "" {
				t.Errorf("validateResourceFlavorLabels() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestResourceFlavorWebhookValidateUpdate(t *testing.T) {
	invalidToleration := corev1.Toleration{
		Key:      "example.com/gpu",
		Operator: corev1.TolerationOpEqual,
		Value:    "not a label value!",
		Effect:   corev1.TaintEffectNoSchedule,
	}
	testcases := []struct {
		name    string
		oldRF   *kueue.ResourceFlavor
		newRF   *kueue.ResourceFlavor
		wantErr field.ErrorList
	}{
		{
			name:  "adding a toleration with an invalid value",
			oldRF: utiltestingapi.MakeResourceFlavor("resource-flavor").Obj(),
			newRF: utiltestingapi.MakeResourceFlavor("resource-flavor").Toleration(invalidToleration).Obj(),
			wantErr: field.ErrorList{
				field.Invalid(field.NewPath("spec", "tolerations").Index(0).Child("value"), "not a label value!", ""),
			},
		},
		{
			name: "unchanged toleration with an invalid value",
			oldRF: utiltestingapi.MakeResourceFlavor("resource-flavor").
				Toleration(invalidToleration).
				Finalizers(kueue.ResourceInUseFinalizerName).
				Obj(),
			newRF: utiltestingapi.MakeResourceFlavor("resource-flavor").Toleration(invalidToleration).Obj(),
		},
	}

	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			_, gotErr := (&ResourceFlavorWebhook{}).ValidateUpdate(t.Context(), tc.oldRF, tc.newRF)
			if diff := cmp.Diff(tc.wantErr.ToAggregate(), gotErr, cmpopts.IgnoreFields(field.Error{}, "Detail")); diff != "" {
				t.Errorf("ValidateUpdate() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
