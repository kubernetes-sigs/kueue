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
	"fmt"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	kubeflow "github.com/kubeflow/mpi-operator/pkg/apis/kubeflow/v2beta1"
	batchv1 "k8s.io/api/batch/v1"
	apivalidation "k8s.io/apimachinery/pkg/api/validation"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/apimachinery/pkg/version"
	fakediscovery "k8s.io/client-go/discovery/fake"
	fakeclient "k8s.io/client-go/kubernetes/fake"
	"k8s.io/utils/ptr"

	"sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/util/kubeversion"
	testingutil "sigs.k8s.io/kueue/pkg/util/testingjobs/job"

	// without this only the job framework is registered
	_ "sigs.k8s.io/kueue/pkg/controller/jobs/mpijob"
)

const (
	invalidRFC1123Message = `a lowercase RFC 1123 subdomain must consist of lower case alphanumeric characters, '-' or '.', and must start and end with an alphanumeric character (e.g. 'example.com', regex used for validation is '[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*')`
)

var (
	annotationsPath               = field.NewPath("metadata", "annotations")
	labelsPath                    = field.NewPath("metadata", "labels")
	parentWorkloadKeyPath         = annotationsPath.Key(constants.ParentWorkloadAnnotation)
	queueNameLabelPath            = labelsPath.Key(constants.QueueLabel)
	prebuiltWlNameLabelPath       = labelsPath.Key(constants.PrebuiltWorkloadLabel)
	queueNameAnnotationsPath      = annotationsPath.Key(constants.QueueAnnotation)
	workloadPriorityClassNamePath = labelsPath.Key(constants.WorkloadPriorityClassLabel)
)

func TestValidateCreate(t *testing.T) {
	testcases := []struct {
		name          string
		job           *batchv1.Job
		wantErr       field.ErrorList
		serverVersion string
	}{
		{
			name:    "simple",
			job:     testingutil.MakeJob("job", "default").Queue("queue").Obj(),
			wantErr: nil,
		},
		{
			name: "valid parent-workload annotation",
			job: testingutil.MakeJob("job", "default").
				ParentWorkload("parent-workload-name").
				Queue("queue").
				OwnerReference("parent-workload-name", kubeflow.SchemeGroupVersionKind).
				Obj(),
			wantErr: nil,
		},
		{
			name: "invalid parent-workload annotation",
			job: testingutil.MakeJob("job", "default").
				ParentWorkload("parent workload name").
				OwnerReference("parent workload name", kubeflow.SchemeGroupVersionKind).
				Queue("queue").
				Obj(),
			wantErr: field.ErrorList{field.Invalid(parentWorkloadKeyPath, "parent workload name", invalidRFC1123Message)},
		},
		{
			name: "invalid parent-workload annotation (owner is missing)",
			job: testingutil.MakeJob("job", "default").
				ParentWorkload("parent-workload").
				Queue("queue").
				Obj(),
			wantErr: field.ErrorList{
				field.Forbidden(parentWorkloadKeyPath, "must not add a parent workload annotation to job without OwnerReference"),
			},
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
			job: testingutil.MakeJob("job", "default").
				Queue("queue name").
				ParentWorkload("parent workload name").
				OwnerReference("parent workload name", kubeflow.SchemeGroupVersionKind).
				Obj(),
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
		{
			name: "invalid sync completions annotation (format)",
			job: testingutil.MakeJob("job", "default").
				Parallelism(4).
				Completions(6).
				SetAnnotation(JobCompletionsEqualParallelismAnnotation, "-").
				Indexed(true).
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(syncCompletionAnnotationsPath, "-", "strconv.ParseBool: parsing \"-\": invalid syntax"),
			},
		},
		{
			name: "valid sync completions annotation, wrong completions count",
			job: testingutil.MakeJob("job", "default").
				Parallelism(4).
				Completions(6).
				SetAnnotation(JobCompletionsEqualParallelismAnnotation, "true").
				Indexed(true).
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(field.NewPath("spec", "completions"), ptr.To[int32](6), fmt.Sprintf("should be equal to parallelism when %s is annotation is true", JobCompletionsEqualParallelismAnnotation)),
			},
			serverVersion: "1.27.0",
		},
		{
			name: "valid sync completions annotation, wrong job completions type (default)",
			job: testingutil.MakeJob("job", "default").
				Parallelism(4).
				Completions(4).
				SetAnnotation(JobCompletionsEqualParallelismAnnotation, "true").
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(syncCompletionAnnotationsPath, "true", "should not be enabled for NonIndexed jobs"),
			},
			serverVersion: "1.27.0",
		},
		{
			name: "valid sync completions annotation, wrong job completions type",
			job: testingutil.MakeJob("job", "default").
				Parallelism(4).
				Completions(4).
				SetAnnotation(JobCompletionsEqualParallelismAnnotation, "true").
				Indexed(false).
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(syncCompletionAnnotationsPath, "true", "should not be enabled for NonIndexed jobs"),
			},
			serverVersion: "1.27.0",
		},
		{
			name: "valid sync completions annotation, server version less then 1.27",
			job: testingutil.MakeJob("job", "default").
				Parallelism(4).
				Completions(4).
				SetAnnotation(JobCompletionsEqualParallelismAnnotation, "true").
				Indexed(true).
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(syncCompletionAnnotationsPath, "true", "only supported in Kubernetes 1.27 or newer"),
			},
			serverVersion: "1.26.3",
		},
		{
			name: "valid sync completions annotation, server version wasn't specified",
			job: testingutil.MakeJob("job", "default").
				Parallelism(4).
				Completions(4).
				SetAnnotation(JobCompletionsEqualParallelismAnnotation, "true").
				Indexed(true).
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(syncCompletionAnnotationsPath, "true", "only supported in Kubernetes 1.27 or newer"),
			},
		},
		{
			name: "valid sync completions annotation",
			job: testingutil.MakeJob("job", "default").
				Parallelism(4).
				Completions(4).
				SetAnnotation(JobCompletionsEqualParallelismAnnotation, "true").
				Indexed(true).
				Obj(),
			wantErr:       nil,
			serverVersion: "1.27.0",
		},
		{
			name: "invalid prebuilt workload",
			job: testingutil.MakeJob("job", "default").
				Parallelism(4).
				Completions(4).
				Label(constants.PrebuiltWorkloadLabel, "workload name").
				Indexed(true).
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(prebuiltWlNameLabelPath, "workload name", invalidRFC1123Message),
			},
			serverVersion: "1.27.0",
		},
		{
			name: "valid prebuilt workload",
			job: testingutil.MakeJob("job", "default").
				Parallelism(4).
				Completions(4).
				Label(constants.PrebuiltWorkloadLabel, "workload-name").
				Indexed(true).
				Obj(),
			wantErr:       nil,
			serverVersion: "1.27.0",
		},
		{
			name: "invalid prebuilt workload and queue-name conflict",
			job: testingutil.MakeJob("job", "default").
				Parallelism(4).
				Completions(4).
				Label(constants.PrebuiltWorkloadLabel, "workload-name").
				Label(constants.QueueLabel, "queue-name").
				Indexed(true).
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(labelsPath, []string{constants.QueueLabel, constants.PrebuiltWorkloadLabel}, "Only one label allowed"),
			},
			serverVersion: "1.27.0",
		},
	}

	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			jw := &JobWebhook{}
			fakeDiscoveryClient, _ := fakeclient.NewSimpleClientset().Discovery().(*fakediscovery.FakeDiscovery)
			fakeDiscoveryClient.FakedServerVersion = &version.Info{GitVersion: tc.serverVersion}
			jw.kubeServerVersion = kubeversion.NewServerVersionFetcher(fakeDiscoveryClient)
			if err := jw.kubeServerVersion.FetchServerVersion(); err != nil && tc.serverVersion != "" {
				t.Fatalf("Failed fetching server version: %v", err)
			}

			gotErr := jw.validateCreate((*Job)(tc.job))

			if diff := cmp.Diff(tc.wantErr, gotErr); diff != "" {
				t.Errorf("validateCreate() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestValidateUpdate(t *testing.T) {
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
			name:   "add queue name with suspend is false",
			oldJob: testingutil.MakeJob("job", "default").Obj(),
			newJob: testingutil.MakeJob("job", "default").Queue("queue").Suspend(false).Obj(),
			wantErr: field.ErrorList{
				field.Invalid(queueNameLabelPath, "", apivalidation.FieldImmutableErrorMsg),
			},
		},
		{
			name:    "add queue name with suspend is true",
			oldJob:  testingutil.MakeJob("job", "default").Obj(),
			newJob:  testingutil.MakeJob("job", "default").Queue("queue").Suspend(true).Obj(),
			wantErr: nil,
		},
		{
			name:   "change queue name with suspend is false",
			oldJob: testingutil.MakeJob("job", "default").Queue("queue").Obj(),
			newJob: testingutil.MakeJob("job", "default").Queue("queue2").Suspend(false).Obj(),
			wantErr: field.ErrorList{
				field.Invalid(queueNameLabelPath, "queue", apivalidation.FieldImmutableErrorMsg),
			},
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
			name:   "update the nil parent workload to non-empty",
			oldJob: testingutil.MakeJob("job", "default").Obj(),
			newJob: testingutil.MakeJob("job", "default").
				ParentWorkload("parent").
				OwnerReference("parent", kubeflow.SchemeGroupVersionKind).
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(parentWorkloadKeyPath, "parent", apivalidation.FieldImmutableErrorMsg),
			},
		},
		{
			name:   "update the non-empty parent workload to nil",
			oldJob: testingutil.MakeJob("job", "default").ParentWorkload("parent").Obj(),
			newJob: testingutil.MakeJob("job", "default").Obj(),
			wantErr: field.ErrorList{
				field.Invalid(parentWorkloadKeyPath, "", apivalidation.FieldImmutableErrorMsg),
			},
		},
		{
			name:   "invalid queue name and immutable parent",
			oldJob: testingutil.MakeJob("job", "default").Obj(),
			newJob: testingutil.MakeJob("job", "default").
				Queue("queue name").
				ParentWorkload("parent").
				OwnerReference("parent", kubeflow.SchemeGroupVersionKind).
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(queueNameLabelPath, "queue name", invalidRFC1123Message),
				field.Invalid(parentWorkloadKeyPath, "parent", apivalidation.FieldImmutableErrorMsg),
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
		{
			name: "immutable sync completion annotation while unsuspended",
			oldJob: testingutil.MakeJob("job", "default").
				Suspend(false).
				Parallelism(4).
				Completions(6).
				SetAnnotation(JobCompletionsEqualParallelismAnnotation, "true").
				Obj(),
			newJob: testingutil.MakeJob("job", "default").
				Suspend(false).
				Parallelism(5).
				Completions(6).
				SetAnnotation(JobCompletionsEqualParallelismAnnotation, "false").
				Obj(),
			wantErr: field.ErrorList{
				field.Forbidden(syncCompletionAnnotationsPath, fmt.Sprintf("%s while the job is not suspended", apivalidation.FieldImmutableErrorMsg)),
			},
		},
		{
			name: "mutable sync completion annotation while suspended",
			oldJob: testingutil.MakeJob("job", "default").
				Suspend(true).
				Parallelism(4).
				Completions(6).
				SetAnnotation(JobCompletionsEqualParallelismAnnotation, "true").
				Obj(),
			newJob: testingutil.MakeJob("job", "default").
				Suspend(false).
				Parallelism(5).
				Completions(6).
				SetAnnotation(JobCompletionsEqualParallelismAnnotation, "false").
				Obj(),
			wantErr: nil,
		},
		{
			name:   "workloadPriorityClassName is immutable",
			oldJob: testingutil.MakeJob("job", "default").WorkloadPriorityClass("test-1").Obj(),
			newJob: testingutil.MakeJob("job", "default").WorkloadPriorityClass("test-2").Obj(),
			wantErr: field.ErrorList{
				field.Invalid(workloadPriorityClassNamePath, "test-1", apivalidation.FieldImmutableErrorMsg),
			},
		},
		{
			name: "immutable prebuilt workload ",
			oldJob: testingutil.MakeJob("job", "default").
				Suspend(true).
				Label(constants.PrebuiltWorkloadLabel, "old-workload").
				Obj(),
			newJob: testingutil.MakeJob("job", "default").
				Suspend(false).
				Label(constants.PrebuiltWorkloadLabel, "new-workload").
				Obj(),
			wantErr: apivalidation.ValidateImmutableField("old-workload", "new-workload", prebuiltWlNameLabelPath),
		},
		{
			name: "invalid prebuilt workload and queue-name conflict ",
			oldJob: testingutil.MakeJob("job", "default").
				Suspend(true).
				Label(constants.PrebuiltWorkloadLabel, "workload-name").
				Obj(),
			newJob: testingutil.MakeJob("job", "default").
				Suspend(false).
				Label(constants.PrebuiltWorkloadLabel, "workload-name").
				Label(constants.QueueLabel, "queue-name").
				Obj(),
			wantErr: append(field.ErrorList{field.Invalid(labelsPath, []string{constants.QueueLabel, constants.PrebuiltWorkloadLabel}, "Only one label allowed")},
				apivalidation.ValidateImmutableField("", "queue-name", queueNameLabelPath)...),
		},
	}

	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			gotErr := new(JobWebhook).validateUpdate((*Job)(tc.oldJob), (*Job)(tc.newJob))
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
