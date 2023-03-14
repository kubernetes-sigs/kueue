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

package jobframework

import (
	"errors"
	"strings"
	"testing"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

func TestGetWorkloadNameForOwner(t *testing.T) {
	testCases := map[string]struct {
		owner            *metav1.OwnerReference
		wantWorkloadName string
		wantErr          error
	}{
		"simple name": {
			owner:            &metav1.OwnerReference{Kind: "Job", APIVersion: "batch/v1", Name: "myjob"},
			wantWorkloadName: "job-myjob-2f79a",
		},
		"lengthy name": {
			owner:            &metav1.OwnerReference{Kind: "Job", APIVersion: "batch/v1", Name: strings.Repeat("a", 252)},
			wantWorkloadName: "job-" + strings.Repeat("a", 253-4-6) + "-f1c14",
		},
		"simple MPIJob name for v2beta1": {
			owner:            &metav1.OwnerReference{Kind: "MPIJob", APIVersion: "kubeflow.org/v2beta1", Name: "myjob"},
			wantWorkloadName: "mpijob-myjob-98672",
		},
		"simple MPIJob name for v2; should be as for v2beta1": {
			owner:            &metav1.OwnerReference{Kind: "MPIJob", APIVersion: "kubeflow.org/v2", Name: "myjob"},
			wantWorkloadName: "mpijob-myjob-98672",
		},
		"invalid APIVersion": {
			owner:   &metav1.OwnerReference{Kind: "Job", APIVersion: "batch/v1/beta1", Name: "myjob"},
			wantErr: errors.New("unexpected GroupVersion string: batch/v1/beta1"),
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			gotWorkloadName, gotErr := GetWorkloadNameForOwnerRef(tc.owner)
			if tc.wantWorkloadName != gotWorkloadName {
				t.Errorf("Unexpected workload name. want=%s, got=%s", tc.wantWorkloadName, gotWorkloadName)
			}
			if gotErr != nil && tc.wantErr == nil {
				t.Errorf("Unexpected error present: %s", gotErr.Error())
			} else if gotErr == nil && tc.wantErr != nil {
				t.Errorf("Missing expected error: %s", tc.wantErr.Error())
			} else if gotErr != nil && tc.wantErr != nil && gotErr.Error() != tc.wantErr.Error() {
				t.Errorf("Unexpected error. want: %s, got: %s.", tc.wantErr.Error(), gotErr.Error())
			}
		})
	}
}
