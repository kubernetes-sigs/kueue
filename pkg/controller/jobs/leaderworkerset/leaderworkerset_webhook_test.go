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

package leaderworkerset

import (
	"context"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
	leaderworkersetv1 "sigs.k8s.io/lws/api/leaderworkerset/v1"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	"sigs.k8s.io/kueue/pkg/cache"
	"sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	podcontroller "sigs.k8s.io/kueue/pkg/controller/jobs/pod"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/queue"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	testingleaderworkerset "sigs.k8s.io/kueue/pkg/util/testingjobs/leaderworkerset"
)

func TestDefault(t *testing.T) {
	testCases := map[string]struct {
		lws                        *leaderworkersetv1.LeaderWorkerSet
		manageJobsWithoutQueueName bool
		localQueueDefaulting       bool
		defaultLqExist             bool
		enableIntegrations         []string
		want                       *leaderworkersetv1.LeaderWorkerSet
	}{
		"LocalQueueDefaulting enabled, default lq is created, job doesn't have queue label": {
			localQueueDefaulting: true,
			defaultLqExist:       true,
			lws: testingleaderworkerset.MakeLeaderWorkerSet("test-lws", "default").
				LeaderTemplate(corev1.PodTemplateSpec{}).
				Obj(),
			want: testingleaderworkerset.MakeLeaderWorkerSet("test-lws", "default").
				LeaderTemplate(corev1.PodTemplateSpec{}).
				Queue("default").
				LeaderTemplateSpecAnnotation(podcontroller.SuspendedByParentAnnotation, FrameworkName).
				LeaderTemplateSpecAnnotation(podcontroller.GroupServingAnnotation, "true").
				WorkerTemplateSpecAnnotation(podcontroller.SuspendedByParentAnnotation, FrameworkName).
				WorkerTemplateSpecAnnotation(podcontroller.GroupServingAnnotation, "true").
				Obj(),
		},
		"LocalQueueDefaulting enabled, default lq is created, job has queue label": {
			localQueueDefaulting: true,
			defaultLqExist:       true,
			lws: testingleaderworkerset.MakeLeaderWorkerSet("test-lws", "").
				LeaderTemplate(corev1.PodTemplateSpec{}).
				Queue("test-queue").
				Obj(),
			want: testingleaderworkerset.MakeLeaderWorkerSet("test-lws", "").
				LeaderTemplate(corev1.PodTemplateSpec{}).
				Queue("test-queue").
				LeaderTemplateSpecAnnotation(podcontroller.SuspendedByParentAnnotation, FrameworkName).
				LeaderTemplateSpecAnnotation(podcontroller.GroupServingAnnotation, "true").
				WorkerTemplateSpecAnnotation(podcontroller.SuspendedByParentAnnotation, FrameworkName).
				WorkerTemplateSpecAnnotation(podcontroller.GroupServingAnnotation, "true").
				Obj(),
		},
		"LocalQueueDefaulting enabled, default lq isn't created, job doesn't have queue label": {
			localQueueDefaulting: true,
			defaultLqExist:       false,
			lws: testingleaderworkerset.MakeLeaderWorkerSet("test-lws", "").
				LeaderTemplate(corev1.PodTemplateSpec{}).
				Obj(),
			want: testingleaderworkerset.MakeLeaderWorkerSet("test-lws", "").
				LeaderTemplate(corev1.PodTemplateSpec{}).
				Obj(),
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.LocalQueueDefaulting, tc.localQueueDefaulting)
			t.Cleanup(jobframework.EnableIntegrationsForTest(t, tc.enableIntegrations...))
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

			w := &Webhook{
				client:                     cli,
				manageJobsWithoutQueueName: tc.manageJobsWithoutQueueName,
				queues:                     queueManager,
			}

			if err := w.Default(ctx, tc.lws); err != nil {
				t.Errorf("failed to set defaults for v1/leaderworkerset: %s", err)
			}
			if diff := cmp.Diff(tc.want, tc.lws); len(diff) != 0 {
				t.Errorf("Default() mismatch (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestValidateCreate(t *testing.T) {
	testCases := map[string]struct {
		integrations []string
		lws          *leaderworkersetv1.LeaderWorkerSet
		wantErr      error
		wantWarns    admission.Warnings
	}{
		"without queue": {
			lws: testingleaderworkerset.MakeLeaderWorkerSet("test-lws", "").
				LeaderTemplate(corev1.PodTemplateSpec{}).
				Obj(),
		},
		"valid queue name": {
			lws: testingleaderworkerset.MakeLeaderWorkerSet("test-lws", "").
				LeaderTemplate(corev1.PodTemplateSpec{}).
				Queue("test-queue").
				Obj(),
		},
		"invalid queue name": {
			lws: testingleaderworkerset.MakeLeaderWorkerSet("test-lws", "").
				LeaderTemplate(corev1.PodTemplateSpec{}).
				Queue("test/queue").
				Obj(),
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeInvalid,
					Field: "metadata.labels[kueue.x-k8s.io/queue-name]",
				},
			}.ToAggregate(),
		},
		"valid topology request": {
			lws: testingleaderworkerset.MakeLeaderWorkerSet("test-lws", "").
				LeaderTemplate(corev1.PodTemplateSpec{}).
				Queue("test-queue").
				LeaderTemplate(corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Annotations: map[string]string{
							kueuealpha.PodSetRequiredTopologyAnnotation: "cloud.com/block",
						},
					},
				}).
				WorkerTemplate(corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Annotations: map[string]string{
							kueuealpha.PodSetRequiredTopologyAnnotation: "cloud.com/block",
						},
					},
				}).
				Obj(),
		},
		"invalid topology request": {
			lws: testingleaderworkerset.MakeLeaderWorkerSet("test-lws", "").
				LeaderTemplate(corev1.PodTemplateSpec{}).
				Queue("test-queue").
				LeaderTemplate(corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Annotations: map[string]string{
							kueuealpha.PodSetRequiredTopologyAnnotation:  "cloud.com/block",
							kueuealpha.PodSetPreferredTopologyAnnotation: "cloud.com/block",
						},
					},
				}).
				WorkerTemplate(corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Annotations: map[string]string{
							kueuealpha.PodSetRequiredTopologyAnnotation:  "cloud.com/block",
							kueuealpha.PodSetPreferredTopologyAnnotation: "cloud.com/block",
						},
					},
				}).
				Obj(),
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeInvalid,
					Field: "spec.leaderWorkerTemplate.leaderTemplate.metadata.annotations",
				},
				&field.Error{
					Type:  field.ErrorTypeInvalid,
					Field: "spec.leaderWorkerTemplate.workerTemplate.metadata.annotations",
				},
			}.ToAggregate(),
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			t.Cleanup(jobframework.EnableIntegrationsForTest(t, "pod"))
			for _, integration := range tc.integrations {
				jobframework.EnableIntegrationsForTest(t, integration)
			}
			builder := utiltesting.NewClientBuilder()
			client := builder.Build()
			w := &Webhook{client: client}
			ctx, _ := utiltesting.ContextWithLog(t)
			warns, err := w.ValidateCreate(ctx, tc.lws)
			if diff := cmp.Diff(tc.wantErr, err, cmpopts.IgnoreFields(field.Error{}, "BadValue", "Detail")); diff != "" {
				t.Errorf("Unexpected error (-want,+got):\n%s", diff)
			}
			if diff := cmp.Diff(warns, tc.wantWarns); diff != "" {
				t.Errorf("Expected different list of warnings (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestValidateUpdate(t *testing.T) {
	testCases := map[string]struct {
		integrations []string
		oldObj       *leaderworkersetv1.LeaderWorkerSet
		newObj       *leaderworkersetv1.LeaderWorkerSet
		wantErr      error
	}{
		"no changes": {
			oldObj: testingleaderworkerset.MakeLeaderWorkerSet("test-lws", "").
				LeaderTemplate(corev1.PodTemplateSpec{}).
				Queue("test-queue").
				LeaderTemplateSpecAnnotation(podcontroller.SuspendedByParentAnnotation, FrameworkName).
				WorkerTemplateSpecAnnotation(podcontroller.SuspendedByParentAnnotation, FrameworkName).
				Obj(),
			newObj: testingleaderworkerset.MakeLeaderWorkerSet("test-lws", "").
				LeaderTemplate(corev1.PodTemplateSpec{}).
				Queue("test-queue").
				LeaderTemplateSpecAnnotation(podcontroller.SuspendedByParentAnnotation, FrameworkName).
				WorkerTemplateSpecAnnotation(podcontroller.SuspendedByParentAnnotation, FrameworkName).
				Obj(),
		},
		"change queue name": {
			oldObj: testingleaderworkerset.MakeLeaderWorkerSet("test-lws", "").
				LeaderTemplate(corev1.PodTemplateSpec{}).
				Queue("test-queue").
				Obj(),
			newObj: testingleaderworkerset.MakeLeaderWorkerSet("test-lws", "").
				LeaderTemplate(corev1.PodTemplateSpec{}).
				Queue("new-test-queue").
				Obj(),
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeInvalid,
					Field: queueNameLabelPath.String(),
				},
			}.ToAggregate(),
		},
		"change priority class": {
			oldObj: testingleaderworkerset.MakeLeaderWorkerSet("test-lws", "").
				LeaderTemplate(corev1.PodTemplateSpec{}).
				Queue("test-queue").
				Label(constants.WorkloadPriorityClassLabel, "test").
				Obj(),
			newObj: testingleaderworkerset.MakeLeaderWorkerSet("test-lws", "").
				LeaderTemplate(corev1.PodTemplateSpec{}).
				Queue("test-queue").
				Label(constants.WorkloadPriorityClassLabel, "new-test").
				Obj(),
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeInvalid,
					Field: priorityClassNamePath.String(),
				},
			}.ToAggregate(),
		},
		"change image": {
			oldObj: testingleaderworkerset.MakeLeaderWorkerSet("test-lws", "").
				LeaderTemplate(corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name:      "c",
								Image:     "pause",
								Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{}},
							},
						},
						InitContainers: []corev1.Container{
							{
								Name:      "ic",
								Image:     "pause",
								Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{}},
							},
						},
					},
				}).
				WorkerTemplate(corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name:      "c",
								Image:     "pause:0.1.0",
								Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{}},
							},
						},
						InitContainers: []corev1.Container{
							{
								Name:      "ic",
								Image:     "pause:0.1.0",
								Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{}},
							},
						},
					},
				}).
				Queue("test-queue").
				LeaderTemplateSpecAnnotation(podcontroller.SuspendedByParentAnnotation, FrameworkName).
				WorkerTemplateSpecAnnotation(podcontroller.SuspendedByParentAnnotation, FrameworkName).
				Obj(),
			newObj: testingleaderworkerset.MakeLeaderWorkerSet("test-lws", "").
				LeaderTemplate(corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name:      "c",
								Image:     "pause:0.1.1",
								Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{}},
							},
						},
						InitContainers: []corev1.Container{
							{
								Name:      "ic",
								Image:     "pause:0.1.1",
								Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{}},
							},
						},
					},
				}).
				WorkerTemplate(corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name:      "c",
								Image:     "pause",
								Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{}},
							},
						},
						InitContainers: []corev1.Container{
							{
								Name:      "ic",
								Image:     "pause",
								Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{}},
							},
						},
					},
				}).
				Queue("test-queue").
				LeaderTemplateSpecAnnotation(podcontroller.SuspendedByParentAnnotation, FrameworkName).
				WorkerTemplateSpecAnnotation(podcontroller.SuspendedByParentAnnotation, FrameworkName).
				Obj(),
		},
		"change resources in container": {
			oldObj: testingleaderworkerset.MakeLeaderWorkerSet("test-lws", "").
				LeaderTemplate(corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name:      "c",
								Image:     "pause",
								Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{}},
							},
						},
						InitContainers: []corev1.Container{
							{
								Name:      "ic",
								Image:     "pause",
								Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{}},
							},
						},
					},
				}).
				WorkerTemplate(corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name:      "c",
								Image:     "pause:0.1.0",
								Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{}},
							},
						},
						InitContainers: []corev1.Container{
							{
								Name:      "ic",
								Image:     "pause:0.1.0",
								Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{}},
							},
						},
					},
				}).
				Queue("test-queue").
				LeaderTemplateSpecAnnotation(podcontroller.SuspendedByParentAnnotation, FrameworkName).
				WorkerTemplateSpecAnnotation(podcontroller.SuspendedByParentAnnotation, FrameworkName).
				Obj(),
			newObj: testingleaderworkerset.MakeLeaderWorkerSet("test-lws", "").
				LeaderTemplate(corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name:  "c",
								Image: "pause:0.1.1",
								Resources: corev1.ResourceRequirements{
									Requests: corev1.ResourceList{
										corev1.ResourceCPU: resource.MustParse("1"),
									},
								},
							},
						},
						InitContainers: []corev1.Container{
							{
								Name:      "ic",
								Image:     "pause:0.1.1",
								Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{}},
							},
						},
					},
				}).
				WorkerTemplate(corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name:  "c",
								Image: "pause",
								Resources: corev1.ResourceRequirements{
									Requests: corev1.ResourceList{
										corev1.ResourceCPU: resource.MustParse("1"),
									},
								},
							},
						},
						InitContainers: []corev1.Container{
							{
								Name:      "ic",
								Image:     "pause",
								Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{}},
							},
						},
					},
				}).
				Queue("test-queue").
				LeaderTemplateSpecAnnotation(podcontroller.SuspendedByParentAnnotation, FrameworkName).
				WorkerTemplateSpecAnnotation(podcontroller.SuspendedByParentAnnotation, FrameworkName).
				Obj(),
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeInvalid,
					Field: leaderTemplatePath.Child("spec", "containers").Index(0).Child("resources", "requests").String(),
				},
				&field.Error{
					Type:  field.ErrorTypeInvalid,
					Field: workerTemplatePath.Child("spec", "containers").Index(0).Child("resources", "requests").String(),
				},
			}.ToAggregate(),
		},
		"change resources in init containers": {
			oldObj: testingleaderworkerset.MakeLeaderWorkerSet("test-lws", "").
				LeaderTemplate(corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name:      "c",
								Image:     "pause",
								Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{}},
							},
						},
						InitContainers: []corev1.Container{
							{
								Name:      "ic",
								Image:     "pause",
								Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{}},
							},
						},
					},
				}).
				WorkerTemplate(corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name:      "c",
								Image:     "pause:0.1.0",
								Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{}},
							},
						},
						InitContainers: []corev1.Container{
							{
								Name:      "ic",
								Image:     "pause:0.1.0",
								Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{}},
							},
						},
					},
				}).
				Queue("test-queue").
				LeaderTemplateSpecAnnotation(podcontroller.SuspendedByParentAnnotation, FrameworkName).
				WorkerTemplateSpecAnnotation(podcontroller.SuspendedByParentAnnotation, FrameworkName).
				Obj(),
			newObj: testingleaderworkerset.MakeLeaderWorkerSet("test-lws", "").
				LeaderTemplate(corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name:      "c",
								Image:     "pause:0.1.1",
								Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{}},
							},
						},
						InitContainers: []corev1.Container{
							{
								Name:  "ic",
								Image: "pause:0.1.1",
								Resources: corev1.ResourceRequirements{
									Requests: corev1.ResourceList{
										corev1.ResourceCPU: resource.MustParse("1"),
									},
								},
							},
						},
					},
				}).
				WorkerTemplate(corev1.PodTemplateSpec{
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Name:      "c",
								Image:     "pause",
								Resources: corev1.ResourceRequirements{Requests: corev1.ResourceList{}},
							},
						},
						InitContainers: []corev1.Container{
							{
								Name:  "ic",
								Image: "pause",
								Resources: corev1.ResourceRequirements{
									Requests: corev1.ResourceList{
										corev1.ResourceCPU: resource.MustParse("1"),
									},
								},
							},
						},
					},
				}).
				Queue("test-queue").
				LeaderTemplateSpecAnnotation(podcontroller.SuspendedByParentAnnotation, FrameworkName).
				WorkerTemplateSpecAnnotation(podcontroller.SuspendedByParentAnnotation, FrameworkName).
				Obj(),
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeInvalid,
					Field: leaderTemplatePath.Child("spec", "initContainers").Index(0).Child("resources", "requests").String(),
				},
				&field.Error{
					Type:  field.ErrorTypeInvalid,
					Field: workerTemplatePath.Child("spec", "initContainers").Index(0).Child("resources", "requests").String(),
				},
			}.ToAggregate(),
		},
		"set valid topology request": {
			oldObj: testingleaderworkerset.MakeLeaderWorkerSet("test-lws", "").
				LeaderTemplate(corev1.PodTemplateSpec{}).
				WorkerTemplate(corev1.PodTemplateSpec{}).
				Queue("test-queue").
				Obj(),
			newObj: testingleaderworkerset.MakeLeaderWorkerSet("test-lws", "").
				LeaderTemplate(corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Annotations: map[string]string{
							kueuealpha.PodSetRequiredTopologyAnnotation: "cloud.com/block",
						},
					},
				}).
				WorkerTemplate(corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Annotations: map[string]string{
							kueuealpha.PodSetRequiredTopologyAnnotation: "cloud.com/block",
						},
					},
				}).
				Queue("test-queue").
				Obj(),
		},
		"set invalid topology request": {
			oldObj: testingleaderworkerset.MakeLeaderWorkerSet("test-lws", "").
				LeaderTemplate(corev1.PodTemplateSpec{}).
				WorkerTemplate(corev1.PodTemplateSpec{}).
				Queue("test-queue").
				Obj(),
			newObj: testingleaderworkerset.MakeLeaderWorkerSet("test-lws", "").
				LeaderTemplate(corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Annotations: map[string]string{
							kueuealpha.PodSetRequiredTopologyAnnotation:  "cloud.com/block",
							kueuealpha.PodSetPreferredTopologyAnnotation: "cloud.com/block",
						},
					},
				}).
				WorkerTemplate(corev1.PodTemplateSpec{
					ObjectMeta: metav1.ObjectMeta{
						Annotations: map[string]string{
							kueuealpha.PodSetRequiredTopologyAnnotation:  "cloud.com/block",
							kueuealpha.PodSetPreferredTopologyAnnotation: "cloud.com/block",
						},
					},
				}).
				Queue("test-queue").
				Obj(),
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeInvalid,
					Field: "spec.leaderWorkerTemplate.leaderTemplate.metadata.annotations",
				},
				&field.Error{
					Type:  field.ErrorTypeInvalid,
					Field: "spec.leaderWorkerTemplate.workerTemplate.metadata.annotations",
				},
			}.ToAggregate(),
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			for _, integration := range tc.integrations {
				jobframework.EnableIntegrationsForTest(t, integration)
			}

			ctx := context.Background()

			wh := &Webhook{}

			_, err := wh.ValidateUpdate(ctx, tc.oldObj, tc.newObj)
			if diff := cmp.Diff(tc.wantErr, err, cmpopts.IgnoreFields(field.Error{}, "BadValue", "Detail")); diff != "" {
				t.Errorf("Unexpected error (-want,+got):\n%s", diff)
			}
		})
	}
}
