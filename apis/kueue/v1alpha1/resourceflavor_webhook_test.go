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

package v1alpha1_test

// Rename the package to avoid circular dependencies which is caused by "sigs.k8s.io/kueue/pkg/util/testing".
// See also: https://github.com/golang/go/wiki/CodeReviewComments#import-dot

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"k8s.io/apimachinery/pkg/util/validation/field"

	. "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
)

func TestValidateResourceFlavorLabels(t *testing.T) {
	testcases := []struct {
		name    string
		labels  map[string]string
		wantErr field.ErrorList
	}{
		{
			name:    "empty labels",
			wantErr: field.ErrorList{},
		},
		{
			name: "invalid label name",
			wantErr: field.ErrorList{
				field.Invalid(field.NewPath("labels"), nil, ""),
			},
			labels: map[string]string{
				"foo@bar": "",
			},
		},
		{
			name: "invalid label value",
			wantErr: field.ErrorList{
				field.Invalid(field.NewPath("labels"), nil, ""),
			},
			labels: map[string]string{
				"foo": "@abcdefg",
			},
		},
	}

	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			rf := utiltesting.MakeResourceFlavor("resource-flavor").MultiLabels(tc.labels).Obj()
			gotErr := ValidateResourceFlavorLabels(rf)
			if diff := cmp.Diff(tc.wantErr, gotErr, cmpopts.IgnoreFields(field.Error{}, "Detail", "BadValue")); diff != "" {
				t.Errorf("validateResourceFlavorLabels() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
