/*
Copyright 2024 The Kubernetes Authors.
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

package raycluster

import (
	"context"
	"fmt"
	"testing"

	"github.com/google/go-cmp/cmp"
	rayv1 "github.com/ray-project/kuberay/ray-operator/apis/ray/v1"
	corev1 "k8s.io/api/core/v1"
	apivalidation "k8s.io/apimachinery/pkg/api/validation"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/utils/ptr"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	"sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/features"
	testingrayutil "sigs.k8s.io/kueue/pkg/util/testingjobs/raycluster"
)

var (
	labelsPath                    = field.NewPath("metadata", "labels")
	workloadPriorityClassNamePath = labelsPath.Key(constants.WorkloadPriorityClassLabel)
)

func TestValidateDefault(t *testing.T) {
	testcases := map[string]struct {
		oldJob    *rayv1.RayCluster
		newJob    *rayv1.RayCluster
		manageAll bool
	}{
		"unmanaged": {
			oldJob: testingrayutil.MakeCluster("job", "ns").
				Suspend(false).
				Obj(),
			newJob: testingrayutil.MakeCluster("job", "ns").
				Suspend(false).
				Obj(),
		},
		"managed - by config": {
			oldJob: testingrayutil.MakeCluster("job", "ns").
				Suspend(false).
				Obj(),
			newJob: testingrayutil.MakeCluster("job", "ns").
				Suspend(true).
				Obj(),
			manageAll: true,
		},
		"managed - by queue": {
			oldJob: testingrayutil.MakeCluster("job", "ns").
				Queue("queue").
				Suspend(false).
				Obj(),
			newJob: testingrayutil.MakeCluster("job", "ns").
				Queue("queue").
				Suspend(true).
				Obj(),
		},
	}

	for name, tc := range testcases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.ManagedJobsNamespaceSelector, false)
			wh := &RayClusterWebhook{
				manageJobsWithoutQueueName: tc.manageAll,
			}
			result := tc.oldJob.DeepCopy()
			if err := wh.Default(context.Background(), result); err != nil {
				t.Errorf("unexpected Default() error: %s", err)
			}
			if diff := cmp.Diff(tc.newJob, result); diff != "" {
				t.Errorf("Default() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestValidateCreate(t *testing.T) {
	worker := rayv1.WorkerGroupSpec{}
	bigWorkerGroup := []rayv1.WorkerGroupSpec{worker, worker, worker, worker, worker, worker, worker, worker}

	testcases := map[string]struct {
		job       *rayv1.RayCluster
		manageAll bool
		wantErr   error
	}{
		"invalid unmanaged": {
			job: testingrayutil.MakeCluster("job", "ns").
				Obj(),
			wantErr: nil,
		},
		"invalid managed - has auto scaler": {
			job: testingrayutil.MakeCluster("job", "ns").Queue("queue").
				WithEnableAutoscaling(ptr.To(true)).
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(field.NewPath("spec", "enableInTreeAutoscaling"), ptr.To(true), "a kueue managed job should not use autoscaling"),
			}.ToAggregate(),
		},
		"invalid managed - too many worker groups": {
			job: testingrayutil.MakeCluster("job", "ns").Queue("queue").
				WithWorkerGroups(bigWorkerGroup...).
				Obj(),
			wantErr: field.ErrorList{
				field.TooMany(field.NewPath("spec", "workerGroupSpecs"), 8, 7),
			}.ToAggregate(),
		},
		"worker group uses head name": {
			job: testingrayutil.MakeCluster("job", "ns").Queue("queue").
				WithWorkerGroups(rayv1.WorkerGroupSpec{
					GroupName: headGroupPodSetName,
				}).
				Obj(),
			wantErr: field.ErrorList{
				field.Forbidden(field.NewPath("spec", "workerGroupSpecs").Index(0).Child("groupName"), fmt.Sprintf("%q is reserved for the head group", headGroupPodSetName)),
			}.ToAggregate(),
		},
		"valid topology request": {
			job: testingrayutil.MakeCluster("raycluster", "ns").Queue("queue").
				WithHeadGroupSpec(rayv1.HeadGroupSpec{
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Annotations: map[string]string{
								kueuealpha.PodSetRequiredTopologyAnnotation: "cloud.com/block",
							},
						},
					},
				}).
				WithWorkerGroups(
					rayv1.WorkerGroupSpec{
						GroupName: "wg1",
						Template: corev1.PodTemplateSpec{
							ObjectMeta: metav1.ObjectMeta{
								Annotations: map[string]string{
									kueuealpha.PodSetRequiredTopologyAnnotation: "cloud.com/block",
								},
							},
						},
					},
					rayv1.WorkerGroupSpec{
						GroupName: "wg2",
						Template: corev1.PodTemplateSpec{
							ObjectMeta: metav1.ObjectMeta{
								Annotations: map[string]string{
									kueuealpha.PodSetPreferredTopologyAnnotation: "cloud.com/block",
								},
							},
						},
					},
					rayv1.WorkerGroupSpec{GroupName: "wg3"},
				).
				Obj(),
		},
		"invalid topology request": {
			job: testingrayutil.MakeCluster("raycluster", "ns").Queue("queue").
				WithHeadGroupSpec(rayv1.HeadGroupSpec{
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Annotations: map[string]string{
								kueuealpha.PodSetPreferredTopologyAnnotation: "cloud.com/block",
								kueuealpha.PodSetRequiredTopologyAnnotation:  "cloud.com/block",
							},
						},
					},
				}).
				WithWorkerGroups(
					rayv1.WorkerGroupSpec{
						GroupName: "wg1",
						Template: corev1.PodTemplateSpec{
							ObjectMeta: metav1.ObjectMeta{
								Annotations: map[string]string{
									kueuealpha.PodSetPreferredTopologyAnnotation: "cloud.com/block",
									kueuealpha.PodSetRequiredTopologyAnnotation:  "cloud.com/block",
								},
							},
						},
					},
				).
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(
					field.NewPath("spec.headGroupSpec.template, metadata.annotations"),
					field.OmitValueType{},
					`must not contain both "kueue.x-k8s.io/podset-required-topology" and "kueue.x-k8s.io/podset-preferred-topology"`,
				),
				field.Invalid(
					field.NewPath("spec.workerGroupSpecs[0].template.metadata.annotations"),
					field.OmitValueType{},
					`must not contain both "kueue.x-k8s.io/podset-required-topology" and "kueue.x-k8s.io/podset-preferred-topology"`,
				),
			}.ToAggregate(),
		},
	}

	for name, tc := range testcases {
		t.Run(name, func(t *testing.T) {
			wh := &RayClusterWebhook{
				manageJobsWithoutQueueName: tc.manageAll,
			}
			_, result := wh.ValidateCreate(context.Background(), tc.job)
			if diff := cmp.Diff(tc.wantErr, result); diff != "" {
				t.Errorf("ValidateCreate() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestValidateUpdate(t *testing.T) {
	testcases := map[string]struct {
		oldJob    *rayv1.RayCluster
		newJob    *rayv1.RayCluster
		manageAll bool
		wantErr   error
	}{
		"invalid unmanaged": {
			oldJob: testingrayutil.MakeCluster("job", "ns").
				Obj(),
			newJob: testingrayutil.MakeCluster("job", "ns").
				Obj(),
			wantErr: nil,
		},
		"invalid managed - queue name should not change while unsuspended": {
			oldJob: testingrayutil.MakeCluster("job", "ns").
				Queue("queue").
				Suspend(false).
				Obj(),
			newJob: testingrayutil.MakeCluster("job", "ns").
				Queue("queue2").
				Suspend(false).
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(field.NewPath("metadata", "labels").Key(constants.QueueLabel), "queue2", apivalidation.FieldImmutableErrorMsg),
			}.ToAggregate(),
		},
		"managed - queue name can change while suspended": {
			oldJob: testingrayutil.MakeCluster("job", "ns").
				Queue("queue").
				Suspend(true).
				Obj(),
			newJob: testingrayutil.MakeCluster("job", "ns").
				Queue("queue2").
				Suspend(true).
				Obj(),
			wantErr: nil,
		},
		"priorityClassName is immutable": {
			oldJob: testingrayutil.MakeCluster("job", "ns").
				Queue("queue").
				WorkloadPriorityClass("test-1").
				Obj(),
			newJob: testingrayutil.MakeCluster("job", "ns").
				Queue("queue").
				WorkloadPriorityClass("test-2").
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(workloadPriorityClassNamePath, "test-2", apivalidation.FieldImmutableErrorMsg),
			}.ToAggregate(),
		},
	}

	for name, tc := range testcases {
		t.Run(name, func(t *testing.T) {
			wh := &RayClusterWebhook{
				manageJobsWithoutQueueName: tc.manageAll,
			}
			_, result := wh.ValidateUpdate(context.Background(), tc.oldJob, tc.newJob)
			if diff := cmp.Diff(tc.wantErr, result); diff != "" {
				t.Errorf("ValidateUpdate() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
