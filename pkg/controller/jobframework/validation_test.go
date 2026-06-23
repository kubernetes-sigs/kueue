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

package jobframework_test

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"go.uber.org/mock/gomock"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/component-base/featuregate"
	"k8s.io/utils/ptr"

	mocks "sigs.k8s.io/kueue/internal/mocks/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/features"
	utiltestingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
	"sigs.k8s.io/kueue/pkg/workloadslicing"
)

var (
	testPath = field.NewPath("spec")
)

func TestValidateImmutablePodSpec(t *testing.T) {
	testCases := map[string]struct {
		newPodSpec *corev1.PodSpec
		oldPodSpec *corev1.PodSpec
		wantErr    field.ErrorList
	}{
		"add container": {
			oldPodSpec: &corev1.PodSpec{},
			newPodSpec: &corev1.PodSpec{Containers: []corev1.Container{{Image: "busybox"}}},
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeInvalid,
					Field: testPath.Child("containers").String(),
				},
			},
		},
		"remove container": {
			oldPodSpec: &corev1.PodSpec{Containers: []corev1.Container{{Image: "busybox"}}},
			newPodSpec: &corev1.PodSpec{},
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeInvalid,
					Field: testPath.Child("containers").String(),
				},
			},
		},
		"change image on container": {
			oldPodSpec: &corev1.PodSpec{Containers: []corev1.Container{{Image: "other"}}},
			newPodSpec: &corev1.PodSpec{Containers: []corev1.Container{{Image: "busybox"}}},
		},
		"change request on container": {
			oldPodSpec: &corev1.PodSpec{
				Containers: []corev1.Container{
					{
						Resources: corev1.ResourceRequirements{
							Requests: map[corev1.ResourceName]resource.Quantity{
								corev1.ResourceCPU: resource.MustParse("100m"),
							},
						},
					},
				},
			},
			newPodSpec: &corev1.PodSpec{
				Containers: []corev1.Container{
					{
						Resources: corev1.ResourceRequirements{
							Requests: map[corev1.ResourceName]resource.Quantity{
								corev1.ResourceCPU: resource.MustParse("200m"),
							},
						},
					},
				},
			},
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeInvalid,
					Field: testPath.Child("containers").Index(0).Child("resources", "requests").String(),
				},
			},
		},
		"add init container": {
			oldPodSpec: &corev1.PodSpec{},
			newPodSpec: &corev1.PodSpec{InitContainers: []corev1.Container{{Image: "busybox"}}},
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeInvalid,
					Field: testPath.Child("initContainers").String(),
				},
			},
		},
		"remove init container": {
			oldPodSpec: &corev1.PodSpec{InitContainers: []corev1.Container{{Image: "busybox"}}},
			newPodSpec: &corev1.PodSpec{},
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeInvalid,
					Field: testPath.Child("initContainers").String(),
				},
			},
		},
		"change request on init container": {
			oldPodSpec: &corev1.PodSpec{
				InitContainers: []corev1.Container{
					{
						Resources: corev1.ResourceRequirements{
							Requests: map[corev1.ResourceName]resource.Quantity{
								corev1.ResourceCPU: resource.MustParse("100m"),
							},
						},
					},
				},
			},
			newPodSpec: &corev1.PodSpec{
				InitContainers: []corev1.Container{
					{
						Resources: corev1.ResourceRequirements{
							Requests: map[corev1.ResourceName]resource.Quantity{},
						},
					},
				},
			},
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeInvalid,
					Field: testPath.Child("initContainers").Index(0).Child("resources", "requests").String(),
				},
			},
		},
		"change nodeTemplate": {
			oldPodSpec: &corev1.PodSpec{},
			newPodSpec: &corev1.PodSpec{
				NodeSelector: map[string]string{"key": "value"},
			},
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeInvalid,
					Field: testPath.Child("nodeSelector").String(),
				},
			},
		},
		"add toleration": {
			oldPodSpec: &corev1.PodSpec{},
			newPodSpec: &corev1.PodSpec{
				Tolerations: []corev1.Toleration{{
					Key:      "example.com/gpu",
					Value:    "present",
					Operator: corev1.TolerationOpEqual,
				}},
			},
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeInvalid,
					Field: testPath.Child("tolerations").String(),
				},
			},
		},
		"change toleration": {
			oldPodSpec: &corev1.PodSpec{
				Tolerations: []corev1.Toleration{{
					Key:      "example.com/gpu",
					Value:    "present",
					Operator: corev1.TolerationOpEqual,
				}},
			},
			newPodSpec: &corev1.PodSpec{
				Tolerations: []corev1.Toleration{{
					Key:      "example.com/gpu",
					Value:    "new",
					Operator: corev1.TolerationOpEqual,
				}},
			},
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeInvalid,
					Field: testPath.Child("tolerations").String(),
				},
			},
		},
		"delete toleration": {
			oldPodSpec: &corev1.PodSpec{
				Tolerations: []corev1.Toleration{{
					Key:      "example.com/gpu",
					Value:    "present",
					Operator: corev1.TolerationOpEqual,
				}},
			},
			newPodSpec: &corev1.PodSpec{},
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeInvalid,
					Field: testPath.Child("tolerations").String(),
				},
			},
		},
		"change runtimeClassName": {
			oldPodSpec: &corev1.PodSpec{
				RuntimeClassName: new("new"),
			},
			newPodSpec: &corev1.PodSpec{},
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeInvalid,
					Field: testPath.Child("runtimeClassName").String(),
				},
			},
		},
		"change priority": {
			oldPodSpec: &corev1.PodSpec{},
			newPodSpec: &corev1.PodSpec{
				Priority: ptr.To[int32](1),
			},
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeInvalid,
					Field: testPath.Child("priority").String(),
				},
			},
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			gotErr := jobframework.ValidateImmutablePodGroupPodSpec(tc.newPodSpec, tc.oldPodSpec, testPath)
			if diff := cmp.Diff(tc.wantErr, gotErr, cmpopts.IgnoreFields(field.Error{}, "BadValue", "Detail")); diff != "" {
				t.Errorf("Unexpected error (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestValidateJobOnUpdate(t *testing.T) {
	t.Cleanup(jobframework.EnableIntegrationsForTest(t, "batch/job"))
	fieldString := field.NewPath("metadata").Child("labels").Key(constants.QueueLabel).String()
	testCases := map[string]struct {
		oldJob            *batchv1.Job
		newJob            *batchv1.Job
		nsHasDefaultQueue bool
		featureGates      map[featuregate.Feature]bool
		wantErr           field.ErrorList
	}{
		"local queue cannot be changed if job is not suspended": {
			featureGates: map[featuregate.Feature]bool{features.WorkloadIdentifierAnnotations: false},
			oldJob:       utiltestingjob.MakeJob("test-job", "ns1").Queue("lq1").Suspend(false).Obj(),
			newJob:       utiltestingjob.MakeJob("test-job", "ns1").Queue("lq2").Suspend(false).Obj(),
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeInvalid,
					Field: fieldString,
				},
			},
		},
		"local queue can be changed": {
			featureGates:      map[featuregate.Feature]bool{features.WorkloadIdentifierAnnotations: false},
			oldJob:            utiltestingjob.MakeJob("test-job", "ns1").Queue("lq1").Suspend(true).Obj(),
			newJob:            utiltestingjob.MakeJob("test-job", "ns1").Queue("lq2").Suspend(true).Obj(),
			nsHasDefaultQueue: true,
		},
		"local queue can be changed from default": {
			featureGates:      map[featuregate.Feature]bool{features.WorkloadIdentifierAnnotations: false},
			oldJob:            utiltestingjob.MakeJob("test-job", "ns1").Queue("default").Suspend(true).Obj(),
			newJob:            utiltestingjob.MakeJob("test-job", "ns1").Queue("lq2").Suspend(true).Obj(),
			nsHasDefaultQueue: true,
		},
		"local queue cannot be removed if default queue exists and feature is enabled": {
			featureGates:      map[featuregate.Feature]bool{features.WorkloadIdentifierAnnotations: false},
			oldJob:            utiltestingjob.MakeJob("test-job", "ns1").Suspend(true).Queue("lq1").Obj(),
			newJob:            utiltestingjob.MakeJob("test-job", "ns1").Suspend(true).Queue("").Obj(),
			nsHasDefaultQueue: true,
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeInvalid,
					Field: fieldString,
				},
			},
		},
		"local queue can be removed if default queue does not exists and feature is enabled": {
			featureGates:      map[featuregate.Feature]bool{features.WorkloadIdentifierAnnotations: false},
			oldJob:            utiltestingjob.MakeJob("test-job", "ns1").Suspend(true).Queue("lq1").Obj(),
			newJob:            utiltestingjob.MakeJob("test-job", "ns1").Suspend(true).Queue("").Obj(),
			nsHasDefaultQueue: false,
		},
		"elastic job enabled annotation cannot be removed on update": {
			oldJob: utiltestingjob.MakeJob("test-job", "ns1").SetAnnotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).Obj(),
			newJob: utiltestingjob.MakeJob("test-job", "ns1").Obj(),
			featureGates: map[featuregate.Feature]bool{
				features.ElasticJobsViaWorkloadSlices: true,
			},
			wantErr: field.ErrorList{
				field.Invalid(field.NewPath("metadata.annotations["+workloadslicing.EnabledAnnotationKey+"]"), "false", "field is immutable"),
			},
		},
		"elastic job enabled annotation cannot be added on update": {
			oldJob: utiltestingjob.MakeJob("test-job", "ns1").Obj(),
			newJob: utiltestingjob.MakeJob("test-job", "ns1").SetAnnotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).Obj(),
			featureGates: map[featuregate.Feature]bool{
				features.ElasticJobsViaWorkloadSlices: true,
			},
			wantErr: field.ErrorList{
				field.Invalid(field.NewPath("metadata.annotations["+workloadslicing.EnabledAnnotationKey+"]"), "false", "field is immutable"),
			},
		},
		"prebuilt workload label valid on update": {
			featureGates: map[featuregate.Feature]bool{features.WorkloadIdentifierAnnotations: false},
			oldJob:       utiltestingjob.MakeJob("test-job", "ns1").PrebuiltWorkloadLabel("workload-name").Suspend(true).Obj(),
			newJob:       utiltestingjob.MakeJob("test-job", "ns1").PrebuiltWorkloadLabel("workload-name").Suspend(true).Obj(),
		},
		"prebuilt workload annotation valid on update, WorkloadIdentifierAnnotations enabled": {
			oldJob:       utiltestingjob.MakeJob("test-job", "ns1").PrebuiltWorkloadAnnotation("workload-name").Suspend(true).Obj(),
			newJob:       utiltestingjob.MakeJob("test-job", "ns1").PrebuiltWorkloadAnnotation("workload-name").Suspend(true).Obj(),
			featureGates: map[featuregate.Feature]bool{features.WorkloadIdentifierAnnotations: true},
		},
		"prebuilt workload label fallback valid on update, WorkloadIdentifierAnnotations enabled": {
			oldJob:       utiltestingjob.MakeJob("test-job", "ns1").PrebuiltWorkloadLabel("workload-name").Suspend(true).Obj(),
			newJob:       utiltestingjob.MakeJob("test-job", "ns1").PrebuiltWorkloadLabel("workload-name").Suspend(true).Obj(),
			featureGates: map[featuregate.Feature]bool{features.WorkloadIdentifierAnnotations: true},
		},
		"prebuilt workload same label and annotation on update, WorkloadIdentifierAnnotations enabled": {
			oldJob: utiltestingjob.MakeJob("test-job", "ns1").
				PrebuiltWorkloadLabel("workload-name").
				PrebuiltWorkloadAnnotation("workload-name").Suspend(true).Obj(),
			newJob: utiltestingjob.MakeJob("test-job", "ns1").
				PrebuiltWorkloadLabel("workload-name").
				PrebuiltWorkloadAnnotation("workload-name").Suspend(true).Obj(),
			featureGates: map[featuregate.Feature]bool{features.WorkloadIdentifierAnnotations: true},
		},
		"prebuilt workload invalid label format with fallback on update, WorkloadIdentifierAnnotations enabled": {
			oldJob:       utiltestingjob.MakeJob("test-job", "ns1").PrebuiltWorkloadLabel("workload name").Suspend(true).Obj(),
			newJob:       utiltestingjob.MakeJob("test-job", "ns1").PrebuiltWorkloadLabel("workload name").Suspend(true).Obj(),
			featureGates: map[featuregate.Feature]bool{features.WorkloadIdentifierAnnotations: true},
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeInvalid,
					Field: "metadata.labels[kueue.x-k8s.io/prebuilt-workload-name]",
				},
			},
		},
		"prebuilt workload invalid annotation format, WorkloadIdentifierAnnotations enabled": {
			oldJob:       utiltestingjob.MakeJob("test-job", "ns1").PrebuiltWorkloadAnnotation("workload name").Suspend(true).Obj(),
			newJob:       utiltestingjob.MakeJob("test-job", "ns1").PrebuiltWorkloadAnnotation("workload name").Suspend(true).Obj(),
			featureGates: map[featuregate.Feature]bool{features.WorkloadIdentifierAnnotations: true},
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeInvalid,
					Field: "metadata.annotations[kueue.x-k8s.io/prebuilt-workload-name]",
				},
			},
		},
		"prebuilt workload annotation update, WorkloadIdentifierAnnotations enabled": {
			oldJob:       utiltestingjob.MakeJob("test-job", "ns1").PrebuiltWorkloadAnnotation("workload-name").Suspend(true).Obj(),
			newJob:       utiltestingjob.MakeJob("test-job", "ns1").PrebuiltWorkloadAnnotation("workload-name-new").Suspend(true).Obj(),
			featureGates: map[featuregate.Feature]bool{features.WorkloadIdentifierAnnotations: true},
		},
		"prebuilt workload annotation update not suspended, WorkloadIdentifierAnnotations enabled": {
			oldJob:       utiltestingjob.MakeJob("test-job", "ns1").PrebuiltWorkloadAnnotation("workload-name").Suspend(false).Obj(),
			newJob:       utiltestingjob.MakeJob("test-job", "ns1").PrebuiltWorkloadAnnotation("workload-name-new").Suspend(false).Obj(),
			featureGates: map[featuregate.Feature]bool{features.WorkloadIdentifierAnnotations: true},
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeInvalid,
					Field: "metadata.annotations[kueue.x-k8s.io/prebuilt-workload-name]",
				},
			},
		},
		"prebuilt workload label to annotation update, WorkloadIdentifierAnnotations enabled": {
			oldJob:       utiltestingjob.MakeJob("test-job", "ns1").PrebuiltWorkloadLabel("workload-name").Suspend(true).Obj(),
			newJob:       utiltestingjob.MakeJob("test-job", "ns1").PrebuiltWorkloadAnnotation("workload-name-new").Suspend(true).Obj(),
			featureGates: map[featuregate.Feature]bool{features.WorkloadIdentifierAnnotations: true},
		},
		"prebuilt workload annotation to label update, WorkloadIdentifierAnnotations enabled": {
			oldJob:       utiltestingjob.MakeJob("test-job", "ns1").PrebuiltWorkloadAnnotation("workload-name").Suspend(true).Obj(),
			newJob:       utiltestingjob.MakeJob("test-job", "ns1").PrebuiltWorkloadLabel("workload-name-new").Suspend(true).Obj(),
			featureGates: map[featuregate.Feature]bool{features.WorkloadIdentifierAnnotations: true},
		},
	}

	for tcName, tc := range testCases {
		t.Run(tcName, func(t *testing.T) {
			features.SetFeatureGatesDuringTest(t, tc.featureGates)

			mockctrl := gomock.NewController(t)

			newMockJob := func(job *batchv1.Job) *mocks.MockGenericJob {
				mj := mocks.NewMockGenericJob(mockctrl)
				mj.EXPECT().Object().Return(job).AnyTimes()
				mj.EXPECT().IsSuspended().Return(ptr.Deref(job.Spec.Suspend, false)).AnyTimes()
				mj.EXPECT().GVK().Return(batchv1.SchemeGroupVersion.WithKind("Job")).AnyTimes()
				return mj
			}

			oldMJ := newMockJob(tc.oldJob)
			newMJ := newMockJob(tc.newJob)

			gotErr := jobframework.ValidateJobOnUpdate(oldMJ, newMJ, func(string) bool { return tc.nsHasDefaultQueue })
			if diff := cmp.Diff(tc.wantErr, gotErr, cmpopts.IgnoreFields(field.Error{}, "BadValue", "Detail")); diff != "" {
				t.Errorf("Unexpected error (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestValidateJobOnCreate(t *testing.T) {
	t.Cleanup(jobframework.EnableIntegrationsForTest(t, "batch/job"))
	elasticAnnotationPath := field.NewPath("metadata", "annotations").Key(workloadslicing.EnabledAnnotationKey)
	testCases := map[string]struct {
		job          *batchv1.Job
		gvk          schema.GroupVersionKind
		featureGates map[featuregate.Feature]bool
		wantErr      field.ErrorList
	}{
		"elastic annotation on batch/v1.Job is allowed": {
			job: utiltestingjob.MakeJob("test-job", "ns1").
				SetAnnotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
				Obj(),
			gvk: batchv1.SchemeGroupVersion.WithKind("Job"),
			featureGates: map[featuregate.Feature]bool{
				features.ElasticJobsViaWorkloadSlices: true,
			},
		},
		"elastic annotation on unsupported framework is rejected": {
			job: utiltestingjob.MakeJob("test-job", "ns1").
				SetAnnotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
				Obj(),
			gvk: schema.GroupVersionKind{Group: "jobset.x-k8s.io", Version: "v1alpha2", Kind: "JobSet"},
			featureGates: map[featuregate.Feature]bool{
				features.ElasticJobsViaWorkloadSlices: true,
			},
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeForbidden,
					Field: elasticAnnotationPath.String(),
				},
			},
		},
		"elastic annotation without feature gate is ignored": {
			job: utiltestingjob.MakeJob("test-job", "ns1").
				SetAnnotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
				Obj(),
			gvk:          schema.GroupVersionKind{Group: "jobset.x-k8s.io", Version: "v1alpha2", Kind: "JobSet"},
			featureGates: map[featuregate.Feature]bool{features.ElasticJobsViaWorkloadSlices: false},
		},
	}

	for tcName, tc := range testCases {
		t.Run(tcName, func(t *testing.T) {
			features.SetFeatureGatesDuringTest(t, tc.featureGates)

			mockctrl := gomock.NewController(t)
			mj := mocks.NewMockGenericJob(mockctrl)
			mj.EXPECT().Object().Return(tc.job).AnyTimes()
			mj.EXPECT().GVK().Return(tc.gvk).AnyTimes()

			gotErr := jobframework.ValidateJobOnCreate(mj)
			if diff := cmp.Diff(tc.wantErr, gotErr, cmpopts.IgnoreFields(field.Error{}, "BadValue", "Detail")); diff != "" {
				t.Errorf("Unexpected error (-want,+got):\n%s", diff)
			}
		})
	}
}
