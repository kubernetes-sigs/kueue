/*
Copyright 2022 The Kubernetes Authors.

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

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
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
			rf:   utiltesting.MakeResourceFlavor("resource-flavor").Obj(),
		},
		{
			name: "valid",
			rf: utiltesting.MakeResourceFlavor("resource-flavor").
				Label("foo", "bar").
				Taint(corev1.Taint{
					Key:    "spot",
					Value:  "true",
					Effect: corev1.TaintEffectNoSchedule,
				}).Obj(),
		},
		{
			// Taint validation is not exhaustively tested, because the code was copied from upstream k8s.
			name: "invalid taint",
			rf: utiltesting.MakeResourceFlavor("resource-flavor").Taint(corev1.Taint{
				Key: "skdajf",
			}).Obj(),
			wantErr: field.ErrorList{
				field.Required(field.NewPath("spec", "nodeTaints").Index(0).Child("effect"), ""),
			},
		},
		{
			name: "invalid label name",
			rf:   utiltesting.MakeResourceFlavor("resource-flavor").Label("@abc", "foo").Obj(),
			wantErr: field.ErrorList{
				field.Invalid(field.NewPath("spec", "nodeLabels"), "@abc", ""),
			},
		},
		{
			name: "invalid label value",
			rf:   utiltesting.MakeResourceFlavor("resource-flavor").Label("foo", "@abc").Obj(),
			wantErr: field.ErrorList{
				field.Invalid(field.NewPath("spec", "nodeLabels"), "@abc", ""),
			},
		},
		{
			name: "bad tolerations",
			rf: utiltesting.MakeResourceFlavor("resource-flavor").
				Toleration(corev1.Toleration{
					Key:      "@abc",
					Operator: corev1.TolerationOpEqual,
					Value:    "v",
					Effect:   corev1.TaintEffectNoSchedule,
				}).
				Toleration(corev1.Toleration{
					Key:      "abc",
					Operator: corev1.TolerationOpExists,
					Value:    "v",
					Effect:   corev1.TaintEffectNoSchedule,
				}).
				Toleration(corev1.Toleration{
					Key:      "abc",
					Operator: corev1.TolerationOpEqual,
					Value:    "v",
					Effect:   corev1.TaintEffect("not-valid"),
				}).
				Toleration(corev1.Toleration{
					Key:      "abc",
					Operator: corev1.TolerationOpEqual,
					Value:    "v",
					Effect:   corev1.TaintEffectNoSchedule,
				}).
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(field.NewPath("spec", "tolerations").Index(0).Child("key"), "@abc", ""),
				field.Invalid(field.NewPath("spec", "tolerations").Index(1).Child("operator"), corev1.Toleration{
					Key:      "abc",
					Operator: corev1.TolerationOpExists,
					Value:    "v",
					Effect:   corev1.TaintEffectNoSchedule,
				}, ""),
				field.NotSupported(field.NewPath("spec", "tolerations").Index(2).Child("effect"), corev1.TaintEffect("not-valid"), []corev1.TaintEffect{}),
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
