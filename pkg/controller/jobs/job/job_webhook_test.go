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

package job

import (
	"context"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	kubeflow "github.com/kubeflow/mpi-operator/pkg/apis/kubeflow/v2beta1"
	batchv1 "k8s.io/api/batch/v1"
	"k8s.io/apimachinery/pkg/util/validation/field"

	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	testingutil "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
)

const (
	invalidRFC1123Message = `a lowercase RFC 1123 subdomain must consist of lower case alphanumeric characters, '-' or '.', and must start and end with an alphanumeric character (e.g. 'example.com', regex used for validation is '[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*')`
)

var (
	annotationsPath          = field.NewPath("metadata", "annotations")
	labelsPath               = field.NewPath("metadata", "labels")
	parentWorkloadKeyPath    = annotationsPath.Key(jobframework.ParentWorkloadAnnotation)
	queueNameLabelPath       = labelsPath.Key(jobframework.QueueLabel)
	queueNameAnnotationsPath = annotationsPath.Key(jobframework.QueueAnnotation)

	originalNodeSelectorsKeyPath = annotationsPath.Key(jobframework.OriginalNodeSelectorsAnnotation)
)

func TestValidateCreate(t *testing.T) {
	testcases := []struct {
		name    string
		job     *batchv1.Job
		wantErr field.ErrorList
	}{
		{
			name:    "simple",
			job:     testingutil.MakeJob("job", "default").Queue("queue").Obj(),
			wantErr: nil,
		},
		{
			name:    "valid parent-workload annotation",
			job:     testingutil.MakeJob("job", "default").ParentWorkload("parent-workload-name").Queue("queue").Obj(),
			wantErr: nil,
		},
		{
			name:    "invalid parent-workload annotation",
			job:     testingutil.MakeJob("job", "default").ParentWorkload("parent workload name").Queue("queue").Obj(),
			wantErr: field.ErrorList{field.Invalid(parentWorkloadKeyPath, "parent workload name", invalidRFC1123Message)},
		},
		{
			name:    "invalid queue-name label",
			job:     testingutil.MakeJob("job", "default").Queue("queue name").Obj(),
			wantErr: field.ErrorList{field.Invalid(queueNameLabelPath, "queue name", invalidRFC1123Message)},
		},
		{
			name:    "invalid queue-name annotation (deprecated)",
			job:     testingutil.MakeJob("job", "default").QueueNameAnnotation("queue name").Obj(),
			wantErr: field.ErrorList{field.Invalid(queueNameAnnotationsPath, "queue name", invalidRFC1123Message)},
		},
		{
			name: "invalid queue-name and parent-workload annotation",
			job:  testingutil.MakeJob("job", "default").Queue("queue name").ParentWorkload("parent workload name").Obj(),
			wantErr: field.ErrorList{
				field.Invalid(parentWorkloadKeyPath, "parent workload name", invalidRFC1123Message),
				field.Invalid(queueNameLabelPath, "queue name", invalidRFC1123Message),
			},
		},
		{
			name: "invalid partial admission annotation (format)",
			job: testingutil.MakeJob("job", "default").
				Parallelism(4).
				Completions(6).
				SetAnnotation(JobMinParallelismAnnotation, "NaN").
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(minPodsCountAnnotationsPath, "NaN", "strconv.Atoi: parsing \"NaN\": invalid syntax"),
			},
		},
		{
			name: "invalid partial admission annotation (badValue)",
			job: testingutil.MakeJob("job", "default").
				Parallelism(4).
				Completions(6).
				SetAnnotation(JobMinParallelismAnnotation, "5").
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(minPodsCountAnnotationsPath, 5, "should be between 0 and 3"),
			},
		},
		{
			name: "valid partial admission annotation",
			job: testingutil.MakeJob("job", "default").
				Parallelism(4).
				Completions(6).
				SetAnnotation(JobMinParallelismAnnotation, "3").
				Obj(),
			wantErr: nil,
		},
	}

	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			gotErr := validateCreate(&Job{tc.job})

			if diff := cmp.Diff(tc.wantErr, gotErr); diff != "" {
				t.Errorf("validateCreate() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestValidateUpdate(t *testing.T) {

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
	testcases := []struct {
		name    string
		oldJob  *batchv1.Job
		newJob  *batchv1.Job
		wantErr field.ErrorList
	}{
		{
			name:    "normal update",
			oldJob:  testingutil.MakeJob("job", "default").Queue("queue").Obj(),
			newJob:  testingutil.MakeJob("job", "default").Queue("queue").Suspend(false).Obj(),
			wantErr: nil,
		},
		{
			name:    "add queue name with suspend is false",
			oldJob:  testingutil.MakeJob("job", "default").Obj(),
			newJob:  testingutil.MakeJob("job", "default").Queue("queue").Suspend(false).Obj(),
			wantErr: field.ErrorList{field.Forbidden(queueNameLabelPath, "must not update queue name when job is unsuspend")},
		},
		{
			name:    "add queue name with suspend is true",
			oldJob:  testingutil.MakeJob("job", "default").Obj(),
			newJob:  testingutil.MakeJob("job", "default").Queue("queue").Suspend(true).Obj(),
			wantErr: nil,
		},
		{
			name:    "change queue name with suspend is false",
			oldJob:  testingutil.MakeJob("job", "default").Queue("queue").Obj(),
			newJob:  testingutil.MakeJob("job", "default").Queue("queue2").Suspend(false).Obj(),
			wantErr: field.ErrorList{field.Forbidden(queueNameLabelPath, "must not update queue name when job is unsuspend")},
		},
		{
			name:    "change queue name with suspend is true",
			oldJob:  testingutil.MakeJob("job", "default").Obj(),
			newJob:  testingutil.MakeJob("job", "default").Queue("queue").Suspend(true).Obj(),
			wantErr: nil,
		},
		{
			name:    "change queue name with suspend is true, but invalid value",
			oldJob:  testingutil.MakeJob("job", "default").Obj(),
			newJob:  testingutil.MakeJob("job", "default").Queue("queue name").Suspend(true).Obj(),
			wantErr: field.ErrorList{field.Invalid(queueNameLabelPath, "queue name", invalidRFC1123Message)},
		},
		{
			name:    "update the nil parent workload to non-empty",
			oldJob:  testingutil.MakeJob("job", "default").Obj(),
			newJob:  testingutil.MakeJob("job", "default").ParentWorkload("parent").Obj(),
			wantErr: field.ErrorList{field.Forbidden(parentWorkloadKeyPath, "this annotation is immutable")},
		},
		{
			name:    "update the non-empty parent workload to nil",
			oldJob:  testingutil.MakeJob("job", "default").ParentWorkload("parent").Obj(),
			newJob:  testingutil.MakeJob("job", "default").Obj(),
			wantErr: field.ErrorList{field.Forbidden(parentWorkloadKeyPath, "this annotation is immutable")},
		},
		{
			name:   "invalid queue name and immutable parent",
			oldJob: testingutil.MakeJob("job", "default").Obj(),
			newJob: testingutil.MakeJob("job", "default").Queue("queue name").ParentWorkload("parent").Obj(),
			wantErr: field.ErrorList{
				field.Invalid(queueNameLabelPath, "queue name", invalidRFC1123Message),
				field.Forbidden(parentWorkloadKeyPath, "this annotation is immutable"),
			},
		},
		{
			name:    "original node selectors can be set while unsuspending",
			oldJob:  testingutil.MakeJob("job", "default").Suspend(true).Obj(),
			newJob:  testingutil.MakeJob("job", "default").Suspend(false).OriginalNodeSelectorsAnnotation(validPodSelectors).Obj(),
			wantErr: nil,
		},
		{
			name:    "original node selectors can be set while suspending",
			oldJob:  testingutil.MakeJob("job", "default").Suspend(false).Obj(),
			newJob:  testingutil.MakeJob("job", "default").Suspend(true).OriginalNodeSelectorsAnnotation(validPodSelectors).Obj(),
			wantErr: nil,
		},
		{
			name:   "immutable original node selectors while not suspended",
			oldJob: testingutil.MakeJob("job", "default").Suspend(false).OriginalNodeSelectorsAnnotation(validPodSelectors).Obj(),
			newJob: testingutil.MakeJob("job", "default").Suspend(false).OriginalNodeSelectorsAnnotation("").Obj(),
			wantErr: field.ErrorList{
				field.Forbidden(originalNodeSelectorsKeyPath, "this annotation is immutable while the job is not changing its suspended state"),
			},
		},
		{
			name:   "immutable original node selectors while suspended",
			oldJob: testingutil.MakeJob("job", "default").Suspend(true).OriginalNodeSelectorsAnnotation(validPodSelectors).Obj(),
			newJob: testingutil.MakeJob("job", "default").Suspend(true).OriginalNodeSelectorsAnnotation("").Obj(),
			wantErr: field.ErrorList{
				field.Forbidden(originalNodeSelectorsKeyPath, "this annotation is immutable while the job is not changing its suspended state"),
			},
		},
		{
			name: "immutable parallelism while unsuspended with partial admission enabled",
			oldJob: testingutil.MakeJob("job", "default").
				Suspend(false).
				Parallelism(4).
				Completions(6).
				SetAnnotation(JobMinParallelismAnnotation, "3").
				Obj(),
			newJob: testingutil.MakeJob("job", "default").
				Suspend(false).
				Parallelism(5).
				Completions(6).
				SetAnnotation(JobMinParallelismAnnotation, "3").
				Obj(),
			wantErr: field.ErrorList{
				field.Forbidden(field.NewPath("spec", "parallelism"), "cannot change when partial admission is enabled and the job is not suspended"),
			},
		},
		{
			name: "mutable parallelism while suspended with partial admission enabled",
			oldJob: testingutil.MakeJob("job", "default").
				Parallelism(4).
				Completions(6).
				SetAnnotation(JobMinParallelismAnnotation, "3").
				Obj(),
			newJob: testingutil.MakeJob("job", "default").
				Parallelism(5).
				Completions(6).
				SetAnnotation(JobMinParallelismAnnotation, "3").
				Obj(),
			wantErr: nil,
		},
	}

	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			gotErr := validateUpdate(&Job{tc.oldJob}, &Job{tc.newJob})

			if diff := cmp.Diff(tc.wantErr, gotErr, cmpopts.IgnoreFields(field.Error{})); diff != "" {
				t.Errorf("validateUpdate() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestDefault(t *testing.T) {
	testcases := map[string]struct {
		job                        *batchv1.Job
		manageJobsWithoutQueueName bool
		want                       *batchv1.Job
	}{
		"add a parent job name to annotations": {
			job: testingutil.MakeJob("child-job", "default").
				OwnerReference("parent-job", kubeflow.SchemeGroupVersionKind).
				Obj(),
			want: testingutil.MakeJob("child-job", "default").
				OwnerReference("parent-job", kubeflow.SchemeGroupVersionKind).
				ParentWorkload(jobframework.GetWorkloadNameForOwnerWithGVK("parent-job", kubeflow.SchemeGroupVersionKind)).
				Obj(),
		},
		"update the suspend field with 'manageJobsWithoutQueueName=false'": {
			job:  testingutil.MakeJob("job", "default").Queue("queue").Suspend(false).Obj(),
			want: testingutil.MakeJob("job", "default").Queue("queue").Obj(),
		},
		"update the suspend field 'manageJobsWithoutQueueName=true'": {
			job:                        testingutil.MakeJob("job", "default").Suspend(false).Obj(),
			manageJobsWithoutQueueName: true,
			want:                       testingutil.MakeJob("job", "default").Obj(),
		},
	}
	for name, tc := range testcases {
		t.Run(name, func(t *testing.T) {
			w := &JobWebhook{manageJobsWithoutQueueName: tc.manageJobsWithoutQueueName}
			if err := w.Default(context.Background(), tc.job); err != nil {
				t.Errorf("set defaults to a batch/job by a Defaulter")
			}
			if diff := cmp.Diff(tc.want, tc.job); len(diff) != 0 {
				t.Errorf("Default() mismatch (-want,+got):\n%s", diff)
			}
		})
	}
}
