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

package jobframework

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/podset"
	utiltestingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
)

var (
	testPath = field.NewPath("spec")
)

type testGenericJob struct {
	*batchv1.Job
}

var _ GenericJob = (*testGenericJob)(nil)

func (j *testGenericJob) Object() client.Object {
	return j.Job
}

func (j *testGenericJob) IsSuspended() bool {
	return ptr.Deref(j.Spec.Suspend, false)
}

func (j *testGenericJob) Suspend() {
	j.Spec.Suspend = ptr.To(true)
}

func (j *testGenericJob) RunWithPodSetsInfo([]podset.PodSetInfo) error {
	panic("not implemented")
}

func (j *testGenericJob) RestorePodSetsInfo([]podset.PodSetInfo) bool {
	panic("not implemented")
}

func (j *testGenericJob) Finished() (string, bool, bool) {
	panic("not implemented")
}

func (j *testGenericJob) PodSets() ([]kueue.PodSet, error) {
	panic("not implemented")
}

func (j *testGenericJob) IsActive() bool {
	panic("not implemented")
}

func (j *testGenericJob) PodsReady() bool {
	panic("not implemented")
}

func (j *testGenericJob) GVK() schema.GroupVersionKind {
	panic("not implemented")
}

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
				RuntimeClassName: ptr.To("new"),
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
			gotErr := ValidateImmutablePodGroupPodSpec(tc.newPodSpec, tc.oldPodSpec, testPath)
			if diff := cmp.Diff(tc.wantErr, gotErr, cmpopts.IgnoreFields(field.Error{}, "BadValue", "Detail")); diff != "" {
				t.Errorf("Unexpected error (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestValidateUpdateForQueueName(t *testing.T) {
	t.Cleanup(EnableIntegrationsForTest(t, "batch/job"))
	fieldString := field.NewPath("metadata").Child("labels").Key(constants.QueueLabel).String()
	testCases := map[string]struct {
		oldJob                   *batchv1.Job
		newJob                   *batchv1.Job
		defaultLocalQueueEnabled bool
		nsHasDefaultQueue        bool
		wantErr                  field.ErrorList
	}{
		"local queue cannot be changed if job is not suspended": {
			oldJob:                   utiltestingjob.MakeJob("test-job", "ns1").Queue("lq1").Suspend(false).Obj(),
			newJob:                   utiltestingjob.MakeJob("test-job", "ns1").Queue("lq2").Suspend(false).Obj(),
			defaultLocalQueueEnabled: true,
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeInvalid,
					Field: fieldString,
				},
			},
		},
		"local queue can be changed": {
			oldJob:                   utiltestingjob.MakeJob("test-job", "ns1").Queue("lq1").Suspend(true).Obj(),
			newJob:                   utiltestingjob.MakeJob("test-job", "ns1").Queue("lq2").Suspend(true).Obj(),
			nsHasDefaultQueue:        true,
			defaultLocalQueueEnabled: true,
		},
		"local queue can be changed from default": {
			oldJob:                   utiltestingjob.MakeJob("test-job", "ns1").Queue("default").Suspend(true).Obj(),
			newJob:                   utiltestingjob.MakeJob("test-job", "ns1").Queue("lq2").Suspend(true).Obj(),
			nsHasDefaultQueue:        true,
			defaultLocalQueueEnabled: true,
		},
		"local queue cannot be removed if default queue exists and feature is enabled": {
			oldJob:                   utiltestingjob.MakeJob("test-job", "ns1").Suspend(true).Queue("lq1").Obj(),
			newJob:                   utiltestingjob.MakeJob("test-job", "ns1").Suspend(true).Queue("").Obj(),
			nsHasDefaultQueue:        true,
			defaultLocalQueueEnabled: true,
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeInvalid,
					Field: fieldString,
				},
			},
		},
		"local queue can be removed if default queue does not exists and feature is enabled": {
			oldJob:                   utiltestingjob.MakeJob("test-job", "ns1").Suspend(true).Queue("lq1").Obj(),
			newJob:                   utiltestingjob.MakeJob("test-job", "ns1").Suspend(true).Queue("").Obj(),
			nsHasDefaultQueue:        false,
			defaultLocalQueueEnabled: true,
		},
		"local queue can be removed if feature is not enabled": {
			oldJob:                   utiltestingjob.MakeJob("test-job", "ns1").Suspend(true).Queue("lq1").Obj(),
			newJob:                   utiltestingjob.MakeJob("test-job", "ns1").Suspend(true).Queue("").Obj(),
			nsHasDefaultQueue:        true,
			defaultLocalQueueEnabled: false,
		},
	}

	for tcName, tc := range testCases {
		t.Run(tcName, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.LocalQueueDefaulting, tc.defaultLocalQueueEnabled)
			oldGJ := &testGenericJob{Job: tc.oldJob}
			newGJ := &testGenericJob{Job: tc.newJob}
			gotErr := validateUpdateForQueueName(oldGJ, newGJ, func(string) bool { return tc.nsHasDefaultQueue })
			if diff := cmp.Diff(tc.wantErr, gotErr, cmpopts.IgnoreFields(field.Error{}, "BadValue", "Detail")); diff != "" {
				t.Errorf("Unexpected error (-want,+got):\n%s", diff)
			}
		})
	}
}
