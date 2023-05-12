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
	validPodSelectors := `
[
  {
    "name": "podSetName",
    "nodeSelector": {
      "l1": "v1"
    }
  }
]
`
	testcases := map[string]struct {
		oldJob  *kubeflow.MPIJob
		newJob  *kubeflow.MPIJob
		wantErr error
	}{
		"original node selectors can be set while unsuspending": {
			oldJob:  testingutil.MakeMPIJob("job", "default").Suspend(true).Obj(),
			newJob:  testingutil.MakeMPIJob("job", "default").Suspend(false).OriginalNodeSelectorsAnnotation(validPodSelectors).Obj(),
			wantErr: nil,
		},
		"original node selectors can be set while suspending": {
			oldJob:  testingutil.MakeMPIJob("job", "default").Suspend(false).Obj(),
			newJob:  testingutil.MakeMPIJob("job", "default").Suspend(true).OriginalNodeSelectorsAnnotation(validPodSelectors).Obj(),
			wantErr: nil,
		},
		"immutable original node selectors while not suspended": {
			oldJob: testingutil.MakeMPIJob("job", "default").Suspend(false).OriginalNodeSelectorsAnnotation(validPodSelectors).Obj(),
			newJob: testingutil.MakeMPIJob("job", "default").Suspend(false).OriginalNodeSelectorsAnnotation("").Obj(),
			wantErr: field.ErrorList{
				field.Forbidden(originalNodeSelectorsKeyPath, "this annotation is immutable while the job is not changing its suspended state"),
			}.ToAggregate(),
		},
		"immutable original node selectors while suspended": {
			oldJob: testingutil.MakeMPIJob("job", "default").Suspend(true).OriginalNodeSelectorsAnnotation(validPodSelectors).Obj(),
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

func TestDefault(t *testing.T) {
	testcases := map[string]struct {
		job                        *kubeflow.MPIJob
		manageJobsWithoutQueueName bool
		want                       *kubeflow.MPIJob
	}{
		"update the suspend field with 'manageJobsWithoutQueueName=false'": {
			job:  testingutil.MakeMPIJob("job", "default").Queue("queue").Suspend(false).Obj(),
			want: testingutil.MakeMPIJob("job", "default").Queue("queue").Obj(),
		},
		"update the suspend field 'manageJobsWithoutQueueName=true'": {
			job:                        testingutil.MakeMPIJob("job", "default").Suspend(false).Obj(),
			manageJobsWithoutQueueName: true,
			want:                       testingutil.MakeMPIJob("job", "default").Obj(),
		},
	}
	for name, tc := range testcases {
		t.Run(name, func(t *testing.T) {
			w := &MPIJobWebhook{manageJobsWithoutQueueName: tc.manageJobsWithoutQueueName}
			if err := w.Default(context.Background(), tc.job); err != nil {
				t.Errorf("set defaults to a kubeflow/mpijob by a Defaulter")
			}
			if diff := cmp.Diff(tc.want, tc.job); len(diff) != 0 {
				t.Errorf("Default() mismatch (-want,+got):\n%s", diff)
			}
		})
	}
}
