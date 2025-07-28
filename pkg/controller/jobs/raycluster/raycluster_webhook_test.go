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

package raycluster

import (
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
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/cache"
	"sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/queue"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	testingrayutil "sigs.k8s.io/kueue/pkg/util/testingjobs/raycluster"
)

func TestValidateDefault(t *testing.T) {
	testcases := map[string]struct {
		oldJob               *rayv1.RayCluster
		newJob               *rayv1.RayCluster
		manageAll            bool
		localQueueDefaulting bool
		defaultLqExist       bool
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
		"LocalQueueDefaulting enabled, default lq is created, job doesn't have queue label": {
			localQueueDefaulting: true,
			defaultLqExist:       true,
			oldJob:               testingrayutil.MakeCluster("test-job", "default").Obj(),
			newJob: testingrayutil.MakeCluster("test-job", "default").
				Queue("default").
				Obj(),
		},
		"LocalQueueDefaulting enabled, default lq is created, job has queue label": {
			localQueueDefaulting: true,
			defaultLqExist:       true,
			oldJob:               testingrayutil.MakeCluster("test-job", "").Queue("test-queue").Obj(),
			newJob: testingrayutil.MakeCluster("test-job", "").
				Queue("test-queue").
				Obj(),
		},
		"LocalQueueDefaulting enabled, default lq isn't created, job doesn't have queue label": {
			localQueueDefaulting: true,
			defaultLqExist:       false,
			oldJob:               testingrayutil.MakeCluster("test-job", "").Obj(),
			newJob: testingrayutil.MakeCluster("test-job", "").
				Obj(),
		},
	}

	for name, tc := range testcases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.LocalQueueDefaulting, tc.localQueueDefaulting)
			ctx, _ := utiltesting.ContextWithLog(t)
			builder := utiltesting.NewClientBuilder()
			cli := builder.Build()
			cqCache := cache.New(cli)
			queueManager := queue.NewManager(cli, cqCache)
			if tc.defaultLqExist {
				if err := queueManager.AddLocalQueue(ctx, utiltesting.MakeLocalQueue("default", "default").
					ClusterQueue("cluster-queue").Obj()); err != nil {
					t.Fatalf("failed to create default local queue: %s", err)
				}
			}

			wh := &RayClusterWebhook{
				manageJobsWithoutQueueName: tc.manageAll,
				queues:                     queueManager,
				cache:                      cqCache,
			}
			result := tc.oldJob.DeepCopy()
			if err := wh.Default(t.Context(), result); err != nil {
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
		job                     *rayv1.RayCluster
		manageAll               bool
		wantErr                 error
		topologyAwareScheduling bool
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
			topologyAwareScheduling: true,
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
					`must not contain more than one topology annotation: ["kueue.x-k8s.io/podset-required-topology", `+
						`"kueue.x-k8s.io/podset-preferred-topology", "kueue.x-k8s.io/podset-unconstrained-topology"]`),
				field.Invalid(
					field.NewPath("spec.workerGroupSpecs[0].template.metadata.annotations"),
					field.OmitValueType{},
					`must not contain more than one topology annotation: ["kueue.x-k8s.io/podset-required-topology", `+
						`"kueue.x-k8s.io/podset-preferred-topology", "kueue.x-k8s.io/podset-unconstrained-topology"]`),
			}.ToAggregate(),
			topologyAwareScheduling: true,
		},
		"invalid slice topology request - slice size larger than number of podsets": {
			job: testingrayutil.MakeCluster("raycluster", "ns").Queue("queue").
				WithHeadGroupSpec(rayv1.HeadGroupSpec{
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Annotations: map[string]string{
								kueuealpha.PodSetRequiredTopologyAnnotation:      "cloud.com/block",
								kueuealpha.PodSetSliceRequiredTopologyAnnotation: "cloud.com/block",
								kueuealpha.PodSetSliceSizeAnnotation:             "2",
							},
						},
					},
				}).
				WithWorkerGroups(
					rayv1.WorkerGroupSpec{
						GroupName: "wg1",
						Replicas:  ptr.To(int32(5)),
						Template: corev1.PodTemplateSpec{
							ObjectMeta: metav1.ObjectMeta{
								Annotations: map[string]string{
									kueuealpha.PodSetRequiredTopologyAnnotation:      "cloud.com/block",
									kueuealpha.PodSetSliceRequiredTopologyAnnotation: "cloud.com/block",
									kueuealpha.PodSetSliceSizeAnnotation:             "10",
								},
							},
						},
					},
					rayv1.WorkerGroupSpec{
						GroupName: "wg2",
						Replicas:  ptr.To(int32(10)),
						Template: corev1.PodTemplateSpec{
							ObjectMeta: metav1.ObjectMeta{
								Annotations: map[string]string{
									kueuealpha.PodSetRequiredTopologyAnnotation:      "cloud.com/block",
									kueuealpha.PodSetSliceRequiredTopologyAnnotation: "cloud.com/block",
									kueuealpha.PodSetSliceSizeAnnotation:             "20",
								},
							},
						},
					},
				).
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(field.NewPath("spec.headGroupSpec.template, metadata.annotations").
					Key("kueue.x-k8s.io/podset-slice-size"), "2", "must not be greater than pod set count 1"),
				field.Invalid(field.NewPath("spec.workerGroupSpecs[0].template.metadata.annotations").
					Key("kueue.x-k8s.io/podset-slice-size"), "10", "must not be greater than pod set count 5"),
				field.Invalid(field.NewPath("spec.workerGroupSpecs[1].template.metadata.annotations").
					Key("kueue.x-k8s.io/podset-slice-size"), "20", "must not be greater than pod set count 10"),
			}.ToAggregate(),
			topologyAwareScheduling: true,
		},
	}

	for name, tc := range testcases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.TopologyAwareScheduling, tc.topologyAwareScheduling)
			wh := &RayClusterWebhook{
				manageJobsWithoutQueueName: tc.manageAll,
			}
			_, result := wh.ValidateCreate(t.Context(), tc.job)
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
				field.Invalid(field.NewPath("metadata", "labels").Key(constants.QueueLabel), kueue.LocalQueueName("queue2"), apivalidation.FieldImmutableErrorMsg),
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
		"priorityClassName is mutable": {
			oldJob: testingrayutil.MakeCluster("job", "ns").
				Queue("queue").
				WorkloadPriorityClass("test-1").
				Obj(),
			newJob: testingrayutil.MakeCluster("job", "ns").
				Queue("queue").
				WorkloadPriorityClass("test-2").
				Obj(),
		},
	}

	for name, tc := range testcases {
		t.Run(name, func(t *testing.T) {
			wh := &RayClusterWebhook{
				manageJobsWithoutQueueName: tc.manageAll,
			}
			_, result := wh.ValidateUpdate(t.Context(), tc.oldJob, tc.newJob)
			if diff := cmp.Diff(tc.wantErr, result); diff != "" {
				t.Errorf("ValidateUpdate() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
