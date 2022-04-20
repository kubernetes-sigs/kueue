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
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/validation/field"

	. "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	testingutil "sigs.k8s.io/kueue/pkg/util/testing"
)

func TestValidateWorkload(t *testing.T) {
	const (
		objName = "name"
		objNs   = "ns"
	)
	specField := field.NewPath("spec")
	podSetsField := specField.Child("podSets")
	testCases := map[string]struct {
		workload *Workload
		wantErr  field.ErrorList
	}{
		"should have at least one podSet": {
			workload: testingutil.MakeWorkload(objName, objNs).PodSets(nil).Obj(),
			wantErr: field.ErrorList{
				field.Required(podSetsField, ""),
			},
		},
		"count should be greater than 0": {
			workload: testingutil.MakeWorkload(objName, objNs).PodSets([]PodSet{
				{
					Name:  "main",
					Count: -1,
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name: "c",
								Resources: corev1.ResourceRequirements{
									Requests: make(corev1.ResourceList),
								},
							},
						},
					},
				},
			}).Obj(),
			wantErr: field.ErrorList{
				field.Invalid(podSetsField.Index(0).Child("count"), int32(-1), ""),
			},
		},
		"should have valid priorityClassName": {
			workload: testingutil.MakeWorkload(objName, objNs).PriorityClass("invalid_class").Obj(),
			wantErr: field.ErrorList{
				field.Invalid(specField.Child("priorityClassName"), "invalid_class", ""),
			},
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			errList := ValidateWorkload(tc.workload)
			if len(errList) != 1 {
				t.Errorf("Unexpected error: %v, want %v", errList, tc.wantErr)
			}
			if diff := cmp.Diff(tc.wantErr[0], errList[0], cmpopts.IgnoreFields(field.Error{}, "Detail")); diff != "" {
				t.Errorf("ValidateWorkload() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
