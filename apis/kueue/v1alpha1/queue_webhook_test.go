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

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"k8s.io/apimachinery/pkg/util/validation/field"

	. "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	testingutil "sigs.k8s.io/kueue/pkg/util/testing"
)

// Rename the package to avoid circular dependencies which is caused by "sigs.k8s.io/kueue/pkg/util/testing".
// See also: https://github.com/golang/go/wiki/CodeReviewComments#import-dot

func TestValidateQueueUpdate(t *testing.T) {
	const (
		objName = "name"
		objNs   = "ns"
	)
	testCases := map[string]struct {
		before, after *Queue
		wantErr       field.ErrorList
	}{
		"clusterQueue cannot be updated": {
			before: testingutil.MakeQueue(objName, objNs).ClusterQueue("foo").Obj(),
			after:  testingutil.MakeQueue(objName, objNs).ClusterQueue("bar").Obj(),
			wantErr: field.ErrorList{
				field.Invalid(field.NewPath("spec").Child("clusterQueue"), nil, ""),
			},
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			errList := ValidateQueueUpdate(tc.before, tc.after)
			if len(errList) == 0 {
				t.Fatalf("Unexpected error: %v, want %v", errList, tc.wantErr)
			}
			if diff := cmp.Diff(tc.wantErr[0], errList[0], cmpopts.IgnoreFields(field.Error{}, "Detail", "BadValue")); diff != "" {
				t.Errorf("ValidateQueueUpdate() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
