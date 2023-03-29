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

package mpijob

import (
	"context"
	"testing"

	"github.com/google/go-cmp/cmp"
	kubeflow "github.com/kubeflow/mpi-operator/pkg/apis/kubeflow/v2beta1"
	"k8s.io/apimachinery/pkg/util/validation/field"

	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	testingutil "sigs.k8s.io/kueue/pkg/util/testingjobs/mpijob"
)

var (
	originalNodeSelectorsKeyPath = field.NewPath("metadata", "annotations").Key(jobframework.OriginalNodeSelectorsAnnotation)
)

func TestUpdate(t *testing.T) {
	testcases := map[string]struct {
		oldJob  *kubeflow.MPIJob
		newJob  *kubeflow.MPIJob
		wantErr error
	}{
		"original node selectors can be set while unsuspending": {
			oldJob:  testingutil.MakeMPIJob("job", "default").Suspend(true).Obj(),
			newJob:  testingutil.MakeMPIJob("job", "default").Suspend(false).OriginalNodeSelectorsAnnotation("[{\"l1\":\"v1\"}]").Obj(),
			wantErr: nil,
		},
		"original node selectors can be set while suspending": {
			oldJob:  testingutil.MakeMPIJob("job", "default").Suspend(true).Obj(),
			newJob:  testingutil.MakeMPIJob("job", "default").Suspend(false).OriginalNodeSelectorsAnnotation("[{\"l1\":\"v1\"}]").Obj(),
			wantErr: nil,
		},
		"immutable original node selectors while not suspended": {
			oldJob: testingutil.MakeMPIJob("job", "default").Suspend(false).OriginalNodeSelectorsAnnotation("[{\"l1\":\"v1\"}]").Obj(),
			newJob: testingutil.MakeMPIJob("job", "default").Suspend(false).OriginalNodeSelectorsAnnotation("").Obj(),
			wantErr: field.ErrorList{
				field.Forbidden(originalNodeSelectorsKeyPath, "this annotation is immutable while the job is not changing its suspended state"),
			}.ToAggregate(),
		},
		"immutable original node selectors while suspended": {
			oldJob: testingutil.MakeMPIJob("job", "default").Suspend(true).OriginalNodeSelectorsAnnotation("[{\"l1\":\"v1\"}]").Obj(),
			newJob: testingutil.MakeMPIJob("job", "default").Suspend(true).OriginalNodeSelectorsAnnotation("").Obj(),
			wantErr: field.ErrorList{
				field.Forbidden(originalNodeSelectorsKeyPath, "this annotation is immutable while the job is not changing its suspended state"),
			}.ToAggregate(),
		},
	}

	for name, tc := range testcases {
		t.Run(name, func(t *testing.T) {
			wh := &MPIJobWebhook{}
			result := wh.ValidateUpdate(context.Background(), tc.oldJob, tc.newJob)

			if diff := cmp.Diff(tc.wantErr, result); diff != "" {
				t.Errorf("ValidateUpdate() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
