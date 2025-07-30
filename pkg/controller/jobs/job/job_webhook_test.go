/*
Copyright The Kubernetes Authors.

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
	kfmpi "github.com/kubeflow/mpi-operator/pkg/apis/kubeflow/v2beta1"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apivalidation "k8s.io/apimachinery/pkg/api/validation"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/utils/ptr"
	jobsetapi "sigs.k8s.io/jobset/api/jobset/v1alpha2"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/cache"
	"sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/queue"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	testingutil "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
	testingmpijob "sigs.k8s.io/kueue/pkg/util/testingjobs/mpijob"
	"sigs.k8s.io/kueue/pkg/workloadslicing"

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
		name                    string
		job                     *batchv1.Job
		wantValidationErrs      field.ErrorList
		wantErr                 error
		topologyAwareScheduling bool
	}{
		{
			name:               "simple",
			job:                testingutil.MakeJob("job", "default").Queue("queue").Obj(),
			wantValidationErrs: nil,
		},
		{
			name:               "invalid queue-name label",
			job:                testingutil.MakeJob("job", "default").Queue("queue name").Obj(),
			wantValidationErrs: field.ErrorList{field.Invalid(queueNameLabelPath, "queue name", invalidRFC1123Message)},
		},
		{
			name:               "invalid queue-name annotation (deprecated)",
			job:                testingutil.MakeJob("job", "default").QueueNameAnnotation("queue name").Obj(),
			wantValidationErrs: field.ErrorList{field.Invalid(queueNameAnnotationsPath, "queue name", invalidRFC1123Message)},
		},
		{
			name: "invalid partial admission annotation (format)",
			job: testingutil.MakeJob("job", "default").
				Parallelism(4).
				Completions(6).
				SetAnnotation(JobMinParallelismAnnotation, "NaN").
				Obj(),
			wantValidationErrs: field.ErrorList{
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
			wantValidationErrs: field.ErrorList{
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
			wantValidationErrs: nil,
		},
		{
			name: "invalid sync completions annotation (format)",
			job: testingutil.MakeJob("job", "default").
				Parallelism(4).
				Completions(6).
				SetAnnotation(JobCompletionsEqualParallelismAnnotation, "-").
				Indexed(true).
				Obj(),
			wantValidationErrs: field.ErrorList{
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
			wantValidationErrs: field.ErrorList{
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
			wantValidationErrs: field.ErrorList{
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
			wantValidationErrs: field.ErrorList{
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
			wantValidationErrs: nil,
		},
		{
			name: "invalid prebuilt workload",
			job: testingutil.MakeJob("job", "default").
				Parallelism(4).
				Completions(4).
				Label(constants.PrebuiltWorkloadLabel, "workload name").
				Indexed(true).
				Obj(),
			wantValidationErrs: field.ErrorList{
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
			wantValidationErrs: nil,
		},
		{
			name: "invalid maximum execution time",
			job: testingutil.MakeJob("job", "default").
				Parallelism(4).
				Completions(4).
				Label(constants.MaxExecTimeSecondsLabel, "NaN").
				Indexed(true).
				Obj(),
			wantValidationErrs: field.ErrorList{
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
			wantValidationErrs: field.ErrorList{
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
			wantValidationErrs: field.ErrorList{
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
			wantValidationErrs:      nil,
			topologyAwareScheduling: true,
		},
		{
			name: "invalid topology request - both annotations",
			job: testingutil.MakeJob("job", "default").
				PodAnnotation(kueuealpha.PodSetRequiredTopologyAnnotation, "cloud.com/block").
				PodAnnotation(kueuealpha.PodSetPreferredTopologyAnnotation, "cloud.com/block").
				Obj(),
			wantValidationErrs: field.ErrorList{
				field.Invalid(replicaMetaPath.Child("annotations"), field.OmitValueType{},
					`must not contain more than one topology annotation: ["kueue.x-k8s.io/podset-required-topology", `+
						`"kueue.x-k8s.io/podset-preferred-topology", "kueue.x-k8s.io/podset-unconstrained-topology"]`),
			},
			topologyAwareScheduling: true,
		},
		{
			name: "invalid topology request - invalid required",
			job: testingutil.MakeJob("job", "default").
				PodAnnotation(kueuealpha.PodSetRequiredTopologyAnnotation, "some required value").
				Obj(),
			wantValidationErrs: field.ErrorList{
				field.Invalid(replicaMetaPath.Child("annotations").Key("kueue.x-k8s.io/podset-required-topology"), "some required value",
					invalidLabelKeyMessage).WithOrigin("labelKey"),
			},
			topologyAwareScheduling: true,
		},
		{
			name: "invalid topology request - invalid preferred",
			job: testingutil.MakeJob("job", "default").
				PodAnnotation(kueuealpha.PodSetPreferredTopologyAnnotation, "some preferred value").
				Obj(),
			wantValidationErrs: field.ErrorList{
				field.Invalid(replicaMetaPath.Child("annotations").Key("kueue.x-k8s.io/podset-preferred-topology"), "some preferred value",
					invalidLabelKeyMessage).WithOrigin("labelKey"),
			},
			topologyAwareScheduling: true,
		},
		{
			name: "valid slice topology request",
			job: testingutil.MakeJob("job", "default").
				PodAnnotation(kueuealpha.PodSetRequiredTopologyAnnotation, "cloud.com/block").
				PodAnnotation(kueuealpha.PodSetSliceRequiredTopologyAnnotation, "cloud.com/block").
				PodAnnotation(kueuealpha.PodSetSliceSizeAnnotation, "1").
				Obj(),
			wantValidationErrs:      nil,
			topologyAwareScheduling: true,
		},
		{
			name: "valid topology request - slice-only topology - unconstrained with slices defined",
			job: testingutil.MakeJob("job", "default").
				PodAnnotation(kueuealpha.PodSetUnconstrainedTopologyAnnotation, "true").
				PodAnnotation(kueuealpha.PodSetSliceRequiredTopologyAnnotation, "cloud.com/block").
				PodAnnotation(kueuealpha.PodSetSliceSizeAnnotation, "1").
				Obj(),
			wantValidationErrs:      nil,
			topologyAwareScheduling: true,
		},
		{
			name: "invalid topology request - slice requested without slice size",
			job: testingutil.MakeJob("job", "default").
				PodAnnotation(kueuealpha.PodSetRequiredTopologyAnnotation, "cloud.com/block").
				PodAnnotation(kueuealpha.PodSetSliceRequiredTopologyAnnotation, "cloud.com/block").
				Obj(),
			wantValidationErrs: field.ErrorList{
				field.Required(replicaMetaPath.Child("annotations").Key("kueue.x-k8s.io/podset-slice-size"), "slice size is required if slice topology is requested"),
			},
			topologyAwareScheduling: true,
		},
		{
			name: "invalid topology request - slice size is not a number",
			job: testingutil.MakeJob("job", "default").
				PodAnnotation(kueuealpha.PodSetRequiredTopologyAnnotation, "cloud.com/block").
				PodAnnotation(kueuealpha.PodSetSliceRequiredTopologyAnnotation, "cloud.com/block").
				PodAnnotation(kueuealpha.PodSetSliceSizeAnnotation, "not a number").
				Obj(),
			wantValidationErrs: field.ErrorList{
				field.Invalid(replicaMetaPath.Child("annotations").Key("kueue.x-k8s.io/podset-slice-size"), "not a number", "must be a numeric value"),
			},
			topologyAwareScheduling: true,
		},
		{
			name: "invalid topology request - slice size is negative",
			job: testingutil.MakeJob("job", "default").
				PodAnnotation(kueuealpha.PodSetRequiredTopologyAnnotation, "cloud.com/block").
				PodAnnotation(kueuealpha.PodSetSliceRequiredTopologyAnnotation, "cloud.com/block").
				PodAnnotation(kueuealpha.PodSetSliceSizeAnnotation, "-1").
				Obj(),
			wantValidationErrs: field.ErrorList{
				field.Invalid(replicaMetaPath.Child("annotations").Key("kueue.x-k8s.io/podset-slice-size"), "-1", "must be greater than or equal to 1"),
			},
			topologyAwareScheduling: true,
		},
		{
			name: "invalid topology request - slice size is zero",
			job: testingutil.MakeJob("job", "default").
				PodAnnotation(kueuealpha.PodSetRequiredTopologyAnnotation, "cloud.com/block").
				PodAnnotation(kueuealpha.PodSetSliceRequiredTopologyAnnotation, "cloud.com/block").
				PodAnnotation(kueuealpha.PodSetSliceSizeAnnotation, "0").
				Obj(),
			wantValidationErrs: field.ErrorList{
				field.Invalid(replicaMetaPath.Child("annotations").Key("kueue.x-k8s.io/podset-slice-size"), "0", "must be greater than or equal to 1"),
			},
			topologyAwareScheduling: true,
		},
		{
			name: "invalid topology request - slice size provided without slice topology",
			job: testingutil.MakeJob("job", "default").
				PodAnnotation(kueuealpha.PodSetRequiredTopologyAnnotation, "cloud.com/block").
				PodAnnotation(kueuealpha.PodSetSliceSizeAnnotation, "1").
				Obj(),
			wantValidationErrs: field.ErrorList{
				field.Forbidden(replicaMetaPath.Child("annotations").Key("kueue.x-k8s.io/podset-slice-size"), "cannot be set when 'kueue.x-k8s.io/podset-slice-required-topology' is not present"),
			},
			topologyAwareScheduling: true,
		},
		{
			name: "valid topology request - slice-only topology",
			job: testingutil.MakeJob("job", "default").
				PodAnnotation(kueuealpha.PodSetSliceRequiredTopologyAnnotation, "cloud.com/block").
				PodAnnotation(kueuealpha.PodSetSliceSizeAnnotation, "1").
				Obj(),
			wantValidationErrs:      nil,
			topologyAwareScheduling: true,
		},
		{
			name: "invalid slice topology request - slice size larger than number of podsets",
			job: testingutil.MakeJob("job", "default").
				Parallelism(4).
				PodAnnotation(kueuealpha.PodSetRequiredTopologyAnnotation, "cloud.com/block").
				PodAnnotation(kueuealpha.PodSetSliceRequiredTopologyAnnotation, "cloud.com/block").
				PodAnnotation(kueuealpha.PodSetSliceSizeAnnotation, "20").
				Obj(),
			wantValidationErrs: field.ErrorList{
				field.Invalid(replicaMetaPath.Child("annotations").
					Key("kueue.x-k8s.io/podset-slice-size"), "20", "must not be greater than pod set count 4"),
			},
			topologyAwareScheduling: true,
		},
	}

	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.TopologyAwareScheduling, tc.topologyAwareScheduling)

			jw := &JobWebhook{}

			gotValidationErrs, gotErr := jw.validateCreate((*Job)(tc.job))

			if diff := cmp.Diff(tc.wantErr, gotErr); diff != "" {
				t.Errorf("validateCreate() error mismatch (-want +got):\n%s", diff)
			}

			if diff := cmp.Diff(tc.wantValidationErrs, gotValidationErrs); diff != "" {
				t.Errorf("validateCreate() validation errors mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestValidateUpdate(t *testing.T) {
	testcases := []struct {
		name                    string
		oldJob                  *batchv1.Job
		newJob                  *batchv1.Job
		wantValidationErrs      field.ErrorList
		wantErr                 error
		topologyAwareScheduling bool
	}{
		{
			name:               "normal update",
			oldJob:             testingutil.MakeJob("job", "default").Queue("queue").Obj(),
			newJob:             testingutil.MakeJob("job", "default").Queue("queue").Suspend(false).Obj(),
			wantValidationErrs: nil,
		},
		{
			name:   "add queue name with suspend is false",
			oldJob: testingutil.MakeJob("job", "default").Obj(),
			newJob: testingutil.MakeJob("job", "default").Queue("queue").Suspend(false).Obj(),
			wantValidationErrs: field.ErrorList{
				field.Invalid(queueNameLabelPath, kueue.LocalQueueName("queue"), apivalidation.FieldImmutableErrorMsg),
			},
		},
		{
			name:               "add queue name with suspend is true",
			oldJob:             testingutil.MakeJob("job", "default").Obj(),
			newJob:             testingutil.MakeJob("job", "default").Queue("queue").Suspend(true).Obj(),
			wantValidationErrs: nil,
		},
		{
			name:   "change queue name with suspend is false",
			oldJob: testingutil.MakeJob("job", "default").Queue("queue").Obj(),
			newJob: testingutil.MakeJob("job", "default").Queue("queue2").Suspend(false).Obj(),
			wantValidationErrs: field.ErrorList{
				field.Invalid(queueNameLabelPath, kueue.LocalQueueName("queue2"), apivalidation.FieldImmutableErrorMsg),
			},
		},
		{
			name:               "change queue name with suspend is true",
			oldJob:             testingutil.MakeJob("job", "default").Obj(),
			newJob:             testingutil.MakeJob("job", "default").Queue("queue").Suspend(true).Obj(),
			wantValidationErrs: nil,
		},
		{
			name:               "change queue name with suspend is true, but invalid value",
			oldJob:             testingutil.MakeJob("job", "default").Obj(),
			newJob:             testingutil.MakeJob("job", "default").Queue("queue name").Suspend(true).Obj(),
			wantValidationErrs: field.ErrorList{field.Invalid(queueNameLabelPath, "queue name", invalidRFC1123Message)},
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
			wantValidationErrs: field.ErrorList{
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
			wantValidationErrs: nil,
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
			wantValidationErrs: field.ErrorList{
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
			wantValidationErrs: nil,
		},
		{
			name:   "workloadPriorityClassName is mutable when job is suspended",
			oldJob: testingutil.MakeJob("job", "default").WorkloadPriorityClass("test-1").Obj(),
			newJob: testingutil.MakeJob("job", "default").WorkloadPriorityClass("test-2").Obj(),
		},
		{
			name:   "workloadPriorityClassName is immutable when job is running",
			oldJob: testingutil.MakeJob("job", "default").WorkloadPriorityClass("test-1").Suspend(false).Obj(),
			newJob: testingutil.MakeJob("job", "default").WorkloadPriorityClass("test-2").Suspend(false).Obj(),
			wantValidationErrs: field.ErrorList{
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
			wantValidationErrs: apivalidation.ValidateImmutableField("new-workload", "old-workload", prebuiltWlNameLabelPath),
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
			wantValidationErrs: apivalidation.ValidateImmutableField(kueue.LocalQueueName("new-queue"), "old-queue", queueNameLabelPath),
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
			wantValidationErrs: nil,
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
			wantValidationErrs: nil,
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
			wantValidationErrs: field.ErrorList{
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
			wantValidationErrs: apivalidation.ValidateImmutableField("20", "10", maxExecTimeLabelPath),
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
			wantValidationErrs: apivalidation.ValidateImmutableField("20", "10", maxExecTimeLabelPath),
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
			topologyAwareScheduling: true,
		},
		{
			name: "attempt to set invalid TAS request",
			oldJob: testingutil.MakeJob("job", "default").
				Obj(),
			newJob: testingutil.MakeJob("job", "default").
				PodAnnotation(kueuealpha.PodSetRequiredTopologyAnnotation, "cloud.com/block").
				PodAnnotation(kueuealpha.PodSetPreferredTopologyAnnotation, "cloud.com/block").
				Obj(),
			wantValidationErrs: field.ErrorList{
				field.Invalid(replicaMetaPath.Child("annotations"), field.OmitValueType{},
					`must not contain more than one topology annotation: ["kueue.x-k8s.io/podset-required-topology", `+
						`"kueue.x-k8s.io/podset-preferred-topology", "kueue.x-k8s.io/podset-unconstrained-topology"]`)},
			topologyAwareScheduling: true,
		},
		{
			name: "valid slice topology request",
			oldJob: testingutil.MakeJob("job", "default").
				Obj(),
			newJob: testingutil.MakeJob("job", "default").
				PodAnnotation(kueuealpha.PodSetRequiredTopologyAnnotation, "cloud.com/block").
				PodAnnotation(kueuealpha.PodSetSliceRequiredTopologyAnnotation, "cloud.com/block").
				PodAnnotation(kueuealpha.PodSetSliceSizeAnnotation, "1").
				Obj(),
			topologyAwareScheduling: true,
		},
		{
			name: "attempt to set invalid slice topology request",
			oldJob: testingutil.MakeJob("job", "default").
				Obj(),
			newJob: testingutil.MakeJob("job", "default").
				PodAnnotation(kueuealpha.PodSetRequiredTopologyAnnotation, "cloud.com/block").
				PodAnnotation(kueuealpha.PodSetSliceRequiredTopologyAnnotation, "cloud.com/block").
				Obj(),
			wantValidationErrs: field.ErrorList{
				field.Required(replicaMetaPath.Child("annotations").Key("kueue.x-k8s.io/podset-slice-size"), "slice size is required if slice topology is requested"),
			},
			topologyAwareScheduling: true,
		},
	}

	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.TopologyAwareScheduling, tc.topologyAwareScheduling)

			gotValidationErrs, gotErr := new(JobWebhook).validateUpdate((*Job)(tc.oldJob), (*Job)(tc.newJob))
			if diff := cmp.Diff(tc.wantErr, gotErr, cmpopts.IgnoreFields(field.Error{})); diff != "" {
				t.Errorf("validateUpdate() error mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(tc.wantValidationErrs, gotValidationErrs, cmpopts.IgnoreFields(field.Error{})); diff != "" {
				t.Errorf("validateUpdate() validation errors mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestDefault(t *testing.T) {
	testcases := map[string]struct {
		job                                    *batchv1.Job
		objs                                   []runtime.Object
		queues                                 []kueue.LocalQueue
		clusterQueues                          []kueue.ClusterQueue
		admissionCheck                         *kueue.AdmissionCheck
		manageJobsWithoutQueueName             bool
		multiKueueEnabled                      bool
		multiKueueBatchJobWithManagedByEnabled bool
		localQueueDefaulting                   bool
		defaultLqExist                         bool
		enableIntegrations                     []string
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
		"LocalQueueDefaulting enabled, default lq is created, job doesn't have queue label": {
			localQueueDefaulting: true,
			defaultLqExist:       true,
			job:                  testingutil.MakeJob("test-job", "default").Obj(),
			want: testingutil.MakeJob("test-job", "default").
				Queue("default").
				Obj(),
		},
		"LocalQueueDefaulting enabled, default lq is created, job has queue label": {
			localQueueDefaulting: true,
			defaultLqExist:       true,
			job:                  testingutil.MakeJob("test-job", "").Queue("test-queue").Obj(),
			want: testingutil.MakeJob("test-job", "").
				Queue("test-queue").
				Obj(),
		},
		"LocalQueueDefaulting enabled, default lq isn't created, job doesn't have queue label": {
			localQueueDefaulting: true,
			defaultLqExist:       false,
			job:                  testingutil.MakeJob("test-job", "").Obj(),
			want: testingutil.MakeJob("test-job", "").
				Obj(),
		},
		"LocalQueueDefaulting enabled, job is managed by Kueue managed owner, job doesn't have queue label": {
			localQueueDefaulting: true,
			defaultLqExist:       true,
			// MPIJob callBackFunction is registered as integrations since we initialize MPIJob integration package.
			enableIntegrations: []string{"kubeflow.org/mpijob"},
			job: testingutil.MakeJob("test-job", metav1.NamespaceDefault).
				OwnerReference("owner", kfmpi.SchemeGroupVersionKind).
				Obj(),
			objs: []runtime.Object{
				testingmpijob.MakeMPIJob("owner", "default").UID("owner").Obj(),
			},
			want: testingutil.MakeJob("test-job", metav1.NamespaceDefault).
				OwnerReference("owner", kfmpi.SchemeGroupVersionKind).
				Obj(),
		},
		"LocalQueueDefaulting enabled, job is managed by non Kueue managed owner, job has queue label": {
			localQueueDefaulting: true,
			defaultLqExist:       true,
			job: testingutil.MakeJob("test-job", metav1.NamespaceDefault).
				OwnerReference("owner", jobsetapi.SchemeGroupVersion.WithKind("JobSet")).
				Obj(),
			want: testingutil.MakeJob("test-job", metav1.NamespaceDefault).
				OwnerReference("owner", jobsetapi.SchemeGroupVersion.WithKind("JobSet")).
				Queue("default").
				Obj(),
		},
	}
	for name, tc := range testcases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.MultiKueue, tc.multiKueueEnabled)
			features.SetFeatureGateDuringTest(t, features.MultiKueueBatchJobWithManagedBy, tc.multiKueueBatchJobWithManagedByEnabled)
			features.SetFeatureGateDuringTest(t, features.LocalQueueDefaulting, tc.localQueueDefaulting)

			ctx, log := utiltesting.ContextWithLog(t)

			clientBuilder := utiltesting.NewClientBuilder(kfmpi.AddToScheme).
				WithObjects(utiltesting.MakeNamespace("default")).
				WithRuntimeObjects(tc.objs...)
			cl := clientBuilder.Build()
			cqCache := cache.New(cl)
			queueManager := queue.NewManager(cl, cqCache)
			if tc.defaultLqExist {
				if err := queueManager.AddLocalQueue(ctx, utiltesting.MakeLocalQueue("default", "default").
					ClusterQueue("cluster-queue").Obj()); err != nil {
					t.Fatalf("failed to create default local queue: %s", err)
				}
			}

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
					cqCache.AddOrUpdateAdmissionCheck(log, tc.admissionCheck)
					if err := queueManager.AddClusterQueue(ctx, &cq); err != nil {
						t.Fatalf("Inserting clusterQueue %s in manager: %v", cq.Name, err)
					}
				}
			}
			t.Cleanup(jobframework.EnableIntegrationsForTest(t, tc.enableIntegrations...))
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

func Test_applyWorkloadSliceSchedulingGate(t *testing.T) {
	type args struct {
		job *Job
	}
	// Test case matrix covers combinations of:
	//   - WorkloadSlice feature enabled: true or false
	//   - Job opt-in via annotation: present or absent
	tests := map[string]struct {
		featureEnabled bool
		args           args
		want           []corev1.PodSchedulingGate
	}{
		"FeatureDisabledAndNotOptIn": {
			args: args{job: &Job{}},
		},
		"FeatureDisabledAndOptIn": {
			args: args{
				job: &Job{
					ObjectMeta: metav1.ObjectMeta{
						Annotations: map[string]string{
							workloadslicing.EnabledAnnotationKey: workloadslicing.EnabledAnnotationValue,
						},
					},
				},
			},
		},
		"FeatureEnabledButNotOptIn": {
			featureEnabled: true,
			args:           args{job: &Job{}},
		},
		"FeatureEnabledAndOptIn": {
			featureEnabled: true,
			args: args{
				job: &Job{
					ObjectMeta: metav1.ObjectMeta{
						Annotations: map[string]string{
							workloadslicing.EnabledAnnotationKey: workloadslicing.EnabledAnnotationValue,
						},
					},
					Spec: batchv1.JobSpec{
						Template: corev1.PodTemplateSpec{
							Spec: corev1.PodSpec{
								SchedulingGates: []corev1.PodSchedulingGate{
									// To assert that other gates are not removed as a side effect.
									{Name: "SomeOtherGate"},
								},
							},
						},
					},
				},
			},
			want: []corev1.PodSchedulingGate{
				{Name: "SomeOtherGate"},
				{Name: kueue.ElasticJobSchedulingGate},
			},
		},
		"FeatureEnabledAndOptIn_SpecAlreadyContainsWorkloadSliceGate": {
			featureEnabled: true,
			args: args{
				job: &Job{
					ObjectMeta: metav1.ObjectMeta{
						Annotations: map[string]string{
							workloadslicing.EnabledAnnotationKey: workloadslicing.EnabledAnnotationValue,
						},
					},
					Spec: batchv1.JobSpec{
						Template: corev1.PodTemplateSpec{
							Spec: corev1.PodSpec{
								SchedulingGates: []corev1.PodSchedulingGate{
									{Name: kueue.ElasticJobSchedulingGate},
								},
							},
						},
					},
				},
			},
			want: []corev1.PodSchedulingGate{
				{Name: kueue.ElasticJobSchedulingGate},
			},
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			if err := features.SetEnable(features.ElasticJobsViaWorkloadSlices, tt.featureEnabled); err != nil {
				t.Errorf("applyWorkloadSliceSchedulingGate() unexpcted error enabling feature: %v", err)
			}
			applyWorkloadSliceSchedulingGate(tt.args.job)
			if diff := cmp.Diff(tt.args.job.Spec.Template.Spec.SchedulingGates, tt.want); diff != "" {
				t.Errorf("applyWorkloadSliceSchedulingGate() got(-),want(+): %s", diff)
			}
		})
	}
}
