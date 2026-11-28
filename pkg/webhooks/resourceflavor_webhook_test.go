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
	testCases := map[string]struct {
		rf      *kueue.ResourceFlavor
		labels  map[string]string
		wantErr field.ErrorList
	}{
		"empty": {
			rf: utiltestingapi.MakeResourceFlavor("resource-flavor").Obj(),
		},
		"valid": {
			rf: utiltestingapi.MakeResourceFlavor("resource-flavor").
				NodeLabel("foo", "bar").
				Taint(corev1.Taint{
					Key:    "spot",
					Value:  "true",
					Effect: corev1.TaintEffectNoSchedule,
				}).Obj(),
		},
		"invalid label name": {
			rf: utiltestingapi.MakeResourceFlavor("resource-flavor").NodeLabel("@abc", "foo").Obj(),
			wantErr: field.ErrorList{
				field.Invalid(field.NewPath("spec", "nodeLabels"), "@abc", "").
					WithOrigin("format=k8s-label-key"),
			},
		},
		"invalid label value": {
			rf: utiltestingapi.MakeResourceFlavor("resource-flavor").NodeLabel("foo", "@abc").Obj(),
			wantErr: field.ErrorList{
				field.Invalid(field.NewPath("spec", "nodeLabels"), "@abc", "").
					WithOrigin("format=k8s-label-value"),
			},
		},
		"valid toleration value": {
			rf: utiltestingapi.MakeResourceFlavor("resource-flavor").
				Toleration(corev1.Toleration{
					Key:      "example.com/gpu",
					Operator: corev1.TolerationOpEqual,
					Value:    "a100",
					Effect:   corev1.TaintEffectNoSchedule,
				}).Obj(),
		},
		"Exists toleration with an empty value": {
			rf: utiltestingapi.MakeResourceFlavor("resource-flavor").
				Toleration(corev1.Toleration{
					Key:      "example.com/gpu",
					Operator: corev1.TolerationOpExists,
					Effect:   corev1.TaintEffectNoSchedule,
				}).Obj(),
		},
		"Equal toleration with an empty value": {
			rf: utiltestingapi.MakeResourceFlavor("resource-flavor").
				Toleration(corev1.Toleration{
					Key:      "example.com/gpu",
					Operator: corev1.TolerationOpEqual,
					Value:    "",
					Effect:   corev1.TaintEffectNoSchedule,
				}).Obj(),
		},
		"invalid toleration value": {
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
		"invalid toleration value with the default operator": {
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

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
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
	testCases := map[string]struct {
		oldRF   *kueue.ResourceFlavor
		newRF   *kueue.ResourceFlavor
		wantErr field.ErrorList
	}{
		"adding a toleration with an invalid value": {
			oldRF: utiltestingapi.MakeResourceFlavor("resource-flavor").Obj(),
			newRF: utiltestingapi.MakeResourceFlavor("resource-flavor").Toleration(invalidToleration).Obj(),
			wantErr: field.ErrorList{
				field.Invalid(field.NewPath("spec", "tolerations").Index(0).Child("value"), "not a label value!", ""),
			},
		},
		"unchanged toleration with an invalid value": {
			oldRF: utiltestingapi.MakeResourceFlavor("resource-flavor").
				Toleration(invalidToleration).
				Finalizers(kueue.ResourceInUseFinalizerName).
				Obj(),
			newRF: utiltestingapi.MakeResourceFlavor("resource-flavor").Toleration(invalidToleration).Obj(),
		},
		"changed tolerations keeping an invalid value": {
			oldRF: utiltestingapi.MakeResourceFlavor("resource-flavor").Toleration(invalidToleration).Obj(),
			newRF: utiltestingapi.MakeResourceFlavor("resource-flavor").
				Toleration(invalidToleration).
				Toleration(corev1.Toleration{
					Key:      "example.com/fpga",
					Operator: corev1.TolerationOpEqual,
					Value:    "true",
					Effect:   corev1.TaintEffectNoSchedule,
				}).
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(field.NewPath("spec", "tolerations").Index(0).Child("value"), "not a label value!", ""),
			},
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			_, gotErr := (&ResourceFlavorWebhook{}).ValidateUpdate(t.Context(), tc.oldRF, tc.newRF)
			if diff := cmp.Diff(tc.wantErr.ToAggregate(), gotErr, cmpopts.IgnoreFields(field.Error{}, "Detail")); diff != "" {
				t.Errorf("ValidateUpdate() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
