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
	"fmt"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apivalidation "k8s.io/apimachinery/pkg/api/validation"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/utils/ptr"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/cache"
	"sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/queue"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	testingutil "sigs.k8s.io/kueue/pkg/util/testingjobs/job"

	// without this only the job framework is registered
	_ "sigs.k8s.io/kueue/pkg/controller/jobs/mpijob"
)

const (
	invalidRFC1123Message  = `a lowercase RFC 1123 subdomain must consist of lower case alphanumeric characters, '-' or '.', and must start and end with an alphanumeric character (e.g. 'example.com', regex used for validation is '[a-z0-9]([-a-z0-9]*[a-z0-9])?(\.[a-z0-9]([-a-z0-9]*[a-z0-9])?)*')`
	invalidLabelKeyMessage = `name part must consist of alphanumeric characters, '-', '_' or '.', and must start and end with an alphanumeric character (e.g. 'MyName',  or 'my.name',  or '123-abc', regex used for validation is '([A-Za-z0-9][-A-Za-z0-9_.]*)?[A-Za-z0-9]')`
)

var (
	annotationsPath               = field.NewPath("metadata", "annotations")
	labelsPath                    = field.NewPath("metadata", "labels")
	queueNameLabelPath            = labelsPath.Key(constants.QueueLabel)
	prebuiltWlNameLabelPath       = labelsPath.Key(constants.PrebuiltWorkloadLabel)
	maxExecTimeLabelPath          = labelsPath.Key(constants.MaxExecTimeSecondsLabel)
	queueNameAnnotationsPath      = annotationsPath.Key(constants.QueueAnnotation)
	workloadPriorityClassNamePath = labelsPath.Key(constants.WorkloadPriorityClassLabel)
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
		},
		{
			name: "valid sync completions annotation",
			job: testingutil.MakeJob("job", "default").
				Parallelism(4).
				Completions(4).
				SetAnnotation(JobCompletionsEqualParallelismAnnotation, "true").
				Indexed(true).
				Obj(),
			wantErr: nil,
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
		},
		{
			name: "valid prebuilt workload",
			job: testingutil.MakeJob("job", "default").
				Parallelism(4).
				Completions(4).
				Label(constants.PrebuiltWorkloadLabel, "workload-name").
				Indexed(true).
				Obj(),
			wantErr: nil,
		},
		{
			name: "invalid maximum execution time",
			job: testingutil.MakeJob("job", "default").
				Parallelism(4).
				Completions(4).
				Label(constants.MaxExecTimeSecondsLabel, "NaN").
				Indexed(true).
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(maxExecTimeLabelPath, "NaN", `strconv.Atoi: parsing "NaN": invalid syntax`),
			},
		},
		{
			name: "zero maximum execution time",
			job: testingutil.MakeJob("job", "default").
				Parallelism(4).
				Completions(4).
				Label(constants.MaxExecTimeSecondsLabel, "0").
				Indexed(true).
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(maxExecTimeLabelPath, 0, "should be greater than 0"),
			},
		},
		{
			name: "negative maximum execution time",
			job: testingutil.MakeJob("job", "default").
				Parallelism(4).
				Completions(4).
				Label(constants.MaxExecTimeSecondsLabel, "-10").
				Indexed(true).
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(maxExecTimeLabelPath, -10, "should be greater than 0"),
			},
		},
		{
			name: "valid maximum execution time",
			job: testingutil.MakeJob("job", "default").
				Parallelism(4).
				Completions(4).
				Label(constants.MaxExecTimeSecondsLabel, "10").
				Indexed(true).
				Obj(),
		},
		{
			name: "valid topology request",
			job: testingutil.MakeJob("job", "default").
				PodAnnotation(kueuealpha.PodSetRequiredTopologyAnnotation, "cloud.com/block").
				Obj(),
			wantErr: nil,
		},
		{
			name: "invalid topology request - both annotations",
			job: testingutil.MakeJob("job", "default").
				PodAnnotation(kueuealpha.PodSetRequiredTopologyAnnotation, "cloud.com/block").
				PodAnnotation(kueuealpha.PodSetPreferredTopologyAnnotation, "cloud.com/block").
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(replicaMetaPath.Child("annotations"), field.OmitValueType{},
					`must not contain both "kueue.x-k8s.io/podset-required-topology" and "kueue.x-k8s.io/podset-preferred-topology"`),
			},
		},
		{
			name: "invalid topology request - invalid required",
			job: testingutil.MakeJob("job", "default").
				PodAnnotation(kueuealpha.PodSetRequiredTopologyAnnotation, "some required value").
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(replicaMetaPath.Child("annotations").Key("kueue.x-k8s.io/podset-required-topology"), "some required value",
					invalidLabelKeyMessage),
			},
		},
		{
			name: "invalid topology request - invalid preferred",
			job: testingutil.MakeJob("job", "default").
				PodAnnotation(kueuealpha.PodSetPreferredTopologyAnnotation, "some preferred value").
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(replicaMetaPath.Child("annotations").Key("kueue.x-k8s.io/podset-preferred-topology"), "some preferred value",
					invalidLabelKeyMessage),
			},
		},
	}

	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			jw := &JobWebhook{}

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
				field.Invalid(queueNameLabelPath, "queue", apivalidation.FieldImmutableErrorMsg),
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
				field.Invalid(queueNameLabelPath, "queue2", apivalidation.FieldImmutableErrorMsg),
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
				SetAnnotation(StoppingAnnotation, "true").
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
				field.Invalid(workloadPriorityClassNamePath, "test-2", apivalidation.FieldImmutableErrorMsg),
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
			wantErr: apivalidation.ValidateImmutableField("new-workload", "old-workload", prebuiltWlNameLabelPath),
		},
		{
			name: "immutable queue name not suspend",
			oldJob: testingutil.MakeJob("job", "default").
				Suspend(false).
				Label(constants.QueueLabel, "old-queue").
				Obj(),
			newJob: testingutil.MakeJob("job", "default").
				Suspend(false).
				Label(constants.QueueLabel, "new-queue").
				Obj(),
			wantErr: apivalidation.ValidateImmutableField("new-queue", "old-queue", queueNameLabelPath),
		},
		{
			name: "queue name can changes when it is  suspend",
			oldJob: testingutil.MakeJob("job", "default").
				Suspend(true).
				Label(constants.QueueLabel, "old-queue").
				Obj(),
			newJob: testingutil.MakeJob("job", "default").
				Suspend(true).
				Label(constants.QueueLabel, "new-queue").
				Obj(),
			wantErr: nil,
		},
		{
			name: "can update the kueue.x-k8s.io/job-min-parallelism  annotation",
			oldJob: testingutil.MakeJob("job", "default").
				Parallelism(4).
				Completions(6).
				SetAnnotation(JobMinParallelismAnnotation, "3").
				Obj(),
			newJob: testingutil.MakeJob("job", "default").
				Parallelism(4).
				Completions(6).
				SetAnnotation(JobMinParallelismAnnotation, "2").
				Obj(),
			wantErr: nil,
		},
		{
			name: "validates kueue.x-k8s.io/job-min-parallelism annotation value (bad format)",
			oldJob: testingutil.MakeJob("job", "default").
				Parallelism(4).
				Completions(6).
				SetAnnotation(JobMinParallelismAnnotation, "3").
				Obj(),
			newJob: testingutil.MakeJob("job", "default").
				Parallelism(4).
				Completions(6).
				SetAnnotation(JobMinParallelismAnnotation, "NaN").
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(minPodsCountAnnotationsPath, "NaN", "strconv.Atoi: parsing \"NaN\": invalid syntax"),
			},
		},
		{
			name: "immutable max exec time while unsuspended",
			oldJob: testingutil.MakeJob("job", "default").
				Suspend(false).
				Label(constants.MaxExecTimeSecondsLabel, "10").
				Obj(),
			newJob: testingutil.MakeJob("job", "default").
				Suspend(false).
				Label(constants.MaxExecTimeSecondsLabel, "20").
				Obj(),
			wantErr: apivalidation.ValidateImmutableField("20", "10", maxExecTimeLabelPath),
		},
		{
			name: "immutable max exec time while transitioning to unsuspended",
			oldJob: testingutil.MakeJob("job", "default").
				Suspend(true).
				Label(constants.MaxExecTimeSecondsLabel, "10").
				Obj(),
			newJob: testingutil.MakeJob("job", "default").
				Suspend(false).
				Label(constants.MaxExecTimeSecondsLabel, "20").
				Obj(),
			wantErr: apivalidation.ValidateImmutableField("20", "10", maxExecTimeLabelPath),
		},
		{
			name: "mutable max exec time while suspended",
			oldJob: testingutil.MakeJob("job", "default").
				Suspend(true).
				Label(constants.MaxExecTimeSecondsLabel, "10").
				Obj(),
			newJob: testingutil.MakeJob("job", "default").
				Suspend(true).
				Label(constants.MaxExecTimeSecondsLabel, "20").
				Obj(),
		},
		{
			name: "set valid TAS request",
			oldJob: testingutil.MakeJob("job", "default").
				Obj(),
			newJob: testingutil.MakeJob("job", "default").
				PodAnnotation(kueuealpha.PodSetRequiredTopologyAnnotation, "cloud.com/block").
				Obj(),
		},
		{
			name: "attempt to set invalid TAS request",
			oldJob: testingutil.MakeJob("job", "default").
				Obj(),
			newJob: testingutil.MakeJob("job", "default").
				PodAnnotation(kueuealpha.PodSetRequiredTopologyAnnotation, "cloud.com/block").
				PodAnnotation(kueuealpha.PodSetPreferredTopologyAnnotation, "cloud.com/block").
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(replicaMetaPath.Child("annotations"), field.OmitValueType{},
					`must not contain both "kueue.x-k8s.io/podset-required-topology" and "kueue.x-k8s.io/podset-preferred-topology"`),
			},
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
		job                                    *batchv1.Job
		queues                                 []kueue.LocalQueue
		clusterQueues                          []kueue.ClusterQueue
		admissionCheck                         *kueue.AdmissionCheck
		manageJobsWithoutQueueName             bool
		multiKueueEnabled                      bool
		multiKueueBatchJobWithManagedByEnabled bool
		want                                   *batchv1.Job
		wantErr                                error
	}{
		"update the suspend field with 'manageJobsWithoutQueueName=false'": {
			job:  testingutil.MakeJob("job", "default").Queue("queue").Suspend(false).Obj(),
			want: testingutil.MakeJob("job", "default").Queue("queue").Obj(),
		},
		"update the suspend field 'manageJobsWithoutQueueName=true'": {
			job:                        testingutil.MakeJob("job", "default").Suspend(false).Obj(),
			manageJobsWithoutQueueName: true,
			want:                       testingutil.MakeJob("job", "default").Obj(),
		},
		"no change in managed by: features.MultiKueueBatchJobWithManagedBy disabled": {
			job:                                    testingutil.MakeJob("job", "default").Queue("queue").Suspend(false).Obj(),
			multiKueueBatchJobWithManagedByEnabled: false,
			want:                                   testingutil.MakeJob("job", "default").Queue("queue").Obj(),
		},
		"no change in managed by: features.MultiKueue disabled": {
			job:               testingutil.MakeJob("job", "default").Queue("queue").Suspend(false).Obj(),
			multiKueueEnabled: false,
			want:              testingutil.MakeJob("job", "default").Queue("queue").Obj(),
		},
		"managed by is defaulted: queue label was set": {
			job: testingutil.MakeJob("job", "default").
				Queue("local-queue").
				Suspend(false).
				Obj(),
			queues: []kueue.LocalQueue{
				*utiltesting.MakeLocalQueue("local-queue", "default").
					ClusterQueue("cluster-queue").
					Obj(),
			},
			clusterQueues: []kueue.ClusterQueue{
				*utiltesting.MakeClusterQueue("cluster-queue").
					AdmissionChecks("admission-check").
					Obj(),
			},
			admissionCheck: utiltesting.MakeAdmissionCheck("admission-check").
				ControllerName(kueue.MultiKueueControllerName).
				Active(metav1.ConditionTrue).
				Obj(),
			want: testingutil.MakeJob("job", "default").
				Queue("local-queue").
				ManagedBy(kueue.MultiKueueControllerName).
				Obj(),
			multiKueueEnabled:                      true,
			multiKueueBatchJobWithManagedByEnabled: true,
		},
		"no change in managed by: user specified managed by": {
			job: testingutil.MakeJob("job", "default").
				Queue("local-queue").
				ManagedBy("example.com/foo").
				Suspend(false).
				Obj(),
			queues: []kueue.LocalQueue{
				*utiltesting.MakeLocalQueue("local-queue", "default").
					ClusterQueue("cluster-queue").
					Obj(),
			},
			clusterQueues: []kueue.ClusterQueue{
				*utiltesting.MakeClusterQueue("cluster-queue").
					AdmissionChecks("admission-check").
					Obj(),
			},
			admissionCheck: utiltesting.MakeAdmissionCheck("admission-check").
				ControllerName(kueue.MultiKueueControllerName).
				Active(metav1.ConditionTrue).
				Obj(),
			want: testingutil.MakeJob("job", "default").
				Queue("local-queue").
				ManagedBy("example.com/foo").
				Obj(),
			multiKueueEnabled:                      true,
			multiKueueBatchJobWithManagedByEnabled: true,
		},
		"invalid queue name": {
			job: testingutil.MakeJob("job", "default").
				Queue("invalid-local-queue").
				Suspend(false).
				Obj(),
			want: testingutil.MakeJob("job", "default").
				Queue("invalid-local-queue").
				Obj(),
			multiKueueEnabled:                      true,
			multiKueueBatchJobWithManagedByEnabled: true,
		},
	}
	for name, tc := range testcases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.MultiKueue, tc.multiKueueEnabled)
			features.SetFeatureGateDuringTest(t, features.MultiKueueBatchJobWithManagedBy, tc.multiKueueBatchJobWithManagedByEnabled)

			ctx, _ := utiltesting.ContextWithLog(t)

			clientBuilder := utiltesting.NewClientBuilder().
				WithObjects(
					&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: "default"}},
				)
			cl := clientBuilder.Build()
			cqCache := cache.New(cl)
			queueManager := queue.NewManager(cl, cqCache)

			for _, q := range tc.queues {
				if err := queueManager.AddLocalQueue(ctx, &q); err != nil {
					t.Fatalf("Inserting queue %s/%s in manager: %v", q.Namespace, q.Name, err)
				}
			}
			for _, cq := range tc.clusterQueues {
				if err := cqCache.AddClusterQueue(ctx, &cq); err != nil {
					t.Fatalf("Inserting clusterQueue %s in cache: %v", cq.Name, err)
				}
				if tc.admissionCheck != nil {
					cqCache.AddOrUpdateAdmissionCheck(tc.admissionCheck)
					if err := queueManager.AddClusterQueue(ctx, &cq); err != nil {
						t.Fatalf("Inserting clusterQueue %s in manager: %v", cq.Name, err)
					}
				}
			}
			w := &JobWebhook{
				client:                       cl,
				manageJobsWithoutQueueName:   tc.manageJobsWithoutQueueName,
				managedJobsNamespaceSelector: labels.Everything(),
				queues:                       queueManager,
				cache:                        cqCache,
			}
			gotErr := w.Default(ctx, tc.job)
			if diff := cmp.Diff(tc.wantErr, gotErr, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("Default() error mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(tc.want, tc.job); len(diff) != 0 {
				t.Errorf("Default() mismatch (-want,+got):\n%s", diff)
			}
		})
	}
}
