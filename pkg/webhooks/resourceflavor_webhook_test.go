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
				NodeLabel("foo", "bar").
				Taint(corev1.Taint{
					Key:    "spot",
					Value:  "true",
					Effect: corev1.TaintEffectNoSchedule,
				}).Obj(),
		},
		{
			name: "invalid label name",
			rf:   utiltesting.MakeResourceFlavor("resource-flavor").NodeLabel("@abc", "foo").Obj(),
			wantErr: field.ErrorList{
				field.Invalid(field.NewPath("spec", "nodeLabels"), "@abc", ""),
			},
		},
		{
			name: "invalid label value",
			rf:   utiltesting.MakeResourceFlavor("resource-flavor").NodeLabel("foo", "@abc").Obj(),
			wantErr: field.ErrorList{
				field.Invalid(field.NewPath("spec", "nodeLabels"), "@abc", ""),
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
