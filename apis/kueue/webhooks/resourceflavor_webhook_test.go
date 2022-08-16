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
	"fmt"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/validation/field"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
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
			name: "invalid label name",
			rf: utiltesting.MakeResourceFlavor("resource-flavor").MultiLabels(map[string]string{
				"foo@bar": "",
			}).Obj(),
			wantErr: field.ErrorList{
				field.Invalid(field.NewPath("labels"), nil, ""),
			},
		},
		{
			name: "invalid label value",
			rf: utiltesting.MakeResourceFlavor("resource-flavor").MultiLabels(map[string]string{
				"foo": "@abcdefg",
			}).Obj(),
			wantErr: field.ErrorList{
				field.Invalid(field.NewPath("labels"), nil, ""),
			},
		},
		{
			// Taint validation is not exhaustively tested, because the code was copied from upstream k8s.
			name: "invalid taint",
			rf: utiltesting.MakeResourceFlavor("resource-flavor").Taint(corev1.Taint{
				Key: "skdajf",
			}).Obj(),
			wantErr: field.ErrorList{
				field.Required(field.NewPath("taints").Index(0).Child("effect"), ""),
			},
		},
		{
			name: "too many labels",
			rf: utiltesting.MakeResourceFlavor("resource-flavor").MultiLabels(func() map[string]string {
				m := make(map[string]string)
				for i := 0; i < 9; i++ {
					m[fmt.Sprintf("l%d", i)] = ""
				}
				return m
			}()).Obj(),
			wantErr: field.ErrorList{
				field.TooMany(field.NewPath("labels"), 9, 8),
			},
		},
		{
			name: "too many taints",
			rf: func() *kueue.ResourceFlavor {
				rf := utiltesting.MakeResourceFlavor("resource-flavor")
				for i := 0; i < 9; i++ {
					rf.Taint(corev1.Taint{
						Key:    fmt.Sprintf("t%d", i),
						Effect: corev1.TaintEffectNoExecute,
					})
				}
				return rf.Obj()
			}(),
			wantErr: field.ErrorList{
				field.TooMany(field.NewPath("taints"), 9, 8),
			},
		},
	}

	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			gotErr := ValidateResourceFlavor(tc.rf)
			if diff := cmp.Diff(tc.wantErr, gotErr, cmpopts.IgnoreFields(field.Error{}, "Detail", "BadValue")); diff != "" {
				t.Errorf("validateResourceFlavorLabels() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
