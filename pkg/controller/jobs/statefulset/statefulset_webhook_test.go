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

package statefulset

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	awv1beta2 "github.com/project-codeflare/appwrapper/api/v1beta2"
	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
	leaderworkersetv1 "sigs.k8s.io/lws/api/leaderworkerset/v1"

	"sigs.k8s.io/kueue/pkg/cache"
	"sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/controller/jobs/appwrapper"
	"sigs.k8s.io/kueue/pkg/controller/jobs/leaderworkerset"
	podconstants "sigs.k8s.io/kueue/pkg/controller/jobs/pod/constants"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/queue"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	testingappwrapper "sigs.k8s.io/kueue/pkg/util/testingjobs/appwrapper"
	testingleaderworkerset "sigs.k8s.io/kueue/pkg/util/testingjobs/leaderworkerset"
	testingstatefulset "sigs.k8s.io/kueue/pkg/util/testingjobs/statefulset"
)

func TestDefault(t *testing.T) {
	testCases := map[string]struct {
		statefulset                *appsv1.StatefulSet
		manageJobsWithoutQueueName bool
		localQueueDefaulting       bool
		defaultLqExist             bool
		enableIntegrations         []string
		want                       *appsv1.StatefulSet
	}{
		"statefulset with queue": {
			enableIntegrations: []string{"pod"},
			statefulset: testingstatefulset.MakeStatefulSet("test-pod", "").
				Replicas(10).
				Queue("test-queue").
				Obj(),
			want: testingstatefulset.MakeStatefulSet("test-pod", "").
				Replicas(10).
				Queue("test-queue").
				PodTemplateSpecQueue("test-queue").
				PodTemplateManagedByKueue().
				PodTemplateAnnotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
				PodTemplateSpecPodGroupNameLabel("test-pod", "", gvk).
				PodTemplateSpecPodGroupTotalCountAnnotation(10).
				PodTemplateSpecPodGroupFastAdmissionAnnotation().
				PodTemplateSpecPodGroupServingAnnotation().
				PodTemplateSpecPodGroupPodIndexLabelAnnotation(appsv1.PodIndexLabel).
				Obj(),
		},
		"statefulset with queue and priority class": {
			enableIntegrations: []string{"pod"},
			statefulset: testingstatefulset.MakeStatefulSet("test-pod", "").
				Replicas(10).
				Queue("test-queue").
				Label(constants.WorkloadPriorityClassLabel, "test").
				Obj(),
			want: testingstatefulset.MakeStatefulSet("test-pod", "").
				Replicas(10).
				Queue("test-queue").
				Label(constants.WorkloadPriorityClassLabel, "test").
				PodTemplateSpecQueue("test-queue").
				PodTemplateManagedByKueue().
				PodTemplateAnnotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
				PodTemplateSpecLabel(constants.WorkloadPriorityClassLabel, "test").
				PodTemplateSpecPodGroupNameLabel("test-pod", "", gvk).
				PodTemplateSpecPodGroupTotalCountAnnotation(10).
				PodTemplateSpecPodGroupFastAdmissionAnnotation().
				PodTemplateSpecPodGroupServingAnnotation().
				PodTemplateSpecPodGroupPodIndexLabelAnnotation(appsv1.PodIndexLabel).
				Obj(),
		},
		"statefulset without replicas": {
			enableIntegrations: []string{"pod"},
			statefulset: testingstatefulset.MakeStatefulSet("test-pod", "").
				Queue("test-queue").
				Obj(),
			want: testingstatefulset.MakeStatefulSet("test-pod", "").
				Queue("test-queue").
				PodTemplateManagedByKueue().
				PodTemplateSpecPodGroupNameLabel("test-pod", "", gvk).
				PodTemplateSpecPodGroupTotalCountAnnotation(1).
				PodTemplateSpecQueue("test-queue").
				PodTemplateAnnotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
				PodTemplateSpecPodGroupFastAdmissionAnnotation().
				PodTemplateSpecPodGroupServingAnnotation().
				PodTemplateSpecPodGroupPodIndexLabelAnnotation(appsv1.PodIndexLabel).
				Obj(),
		},
		"LocalQueueDefaulting enabled, default lq is created, job doesn't have queue label": {
			localQueueDefaulting: true,
			defaultLqExist:       true,
			statefulset:          testingstatefulset.MakeStatefulSet("test-pod", "default").Obj(),
			want: testingstatefulset.MakeStatefulSet("test-pod", "default").
				Queue("default").
				PodTemplateSpecQueue("default").
				PodTemplateManagedByKueue().
				PodTemplateAnnotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
				PodTemplateSpecPodGroupNameLabel("test-pod", "", gvk).
				PodTemplateSpecPodGroupTotalCountAnnotation(1).
				PodTemplateSpecPodGroupFastAdmissionAnnotation().
				PodTemplateSpecPodGroupServingAnnotation().
				PodTemplateSpecPodGroupPodIndexLabelAnnotation(appsv1.PodIndexLabel).
				Obj(),
		},
		"LocalQueueDefaulting enabled, default lq is created, job has queue label": {
			localQueueDefaulting: true,
			defaultLqExist:       true,
			statefulset:          testingstatefulset.MakeStatefulSet("test-pod", "").Queue("test-queue").Obj(),
			want: testingstatefulset.MakeStatefulSet("test-pod", "").
				Queue("test-queue").
				PodTemplateSpecQueue("test-queue").
				PodTemplateManagedByKueue().
				PodTemplateAnnotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
				PodTemplateSpecPodGroupNameLabel("test-pod", "", gvk).
				PodTemplateSpecPodGroupTotalCountAnnotation(1).
				PodTemplateSpecPodGroupFastAdmissionAnnotation().
				PodTemplateSpecPodGroupServingAnnotation().
				PodTemplateSpecPodGroupPodIndexLabelAnnotation(appsv1.PodIndexLabel).
				Obj(),
		},
		"LocalQueueDefaulting enabled, default lq isn't created, job doesn't have queue label": {
			localQueueDefaulting: true,
			defaultLqExist:       false,
			statefulset:          testingstatefulset.MakeStatefulSet("test-pod", "").Obj(),
			want:                 testingstatefulset.MakeStatefulSet("test-pod", "").Obj(),
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

			if err := w.Default(ctx, tc.statefulset); err != nil {
				t.Errorf("failed to set defaults for v1/statefulset: %s", err)
			}
			if diff := cmp.Diff(tc.want, tc.statefulset); len(diff) != 0 {
				t.Errorf("Default() mismatch (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestValidateCreate(t *testing.T) {
	testCases := map[string]struct {
		sts       *appsv1.StatefulSet
		wantErr   error
		wantWarns admission.Warnings
	}{
		"without queue": {
			sts: testingstatefulset.MakeStatefulSet("test-pod", "").Obj(),
		},
		"valid queue name": {
			sts: testingstatefulset.MakeStatefulSet("test-pod", "").
				Queue("test-queue").
				Obj(),
		},
		"invalid queue name": {
			sts: testingstatefulset.MakeStatefulSet("test-pod", "").
				Queue("test/queue").
				Obj(),
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeInvalid,
					Field: "metadata.labels[kueue.x-k8s.io/queue-name]",
				},
			}.ToAggregate(),
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			t.Cleanup(jobframework.EnableIntegrationsForTest(t, "pod"))
			builder := utiltesting.NewClientBuilder()
			client := builder.Build()
			w := &Webhook{client: client}
			ctx, _ := utiltesting.ContextWithLog(t)
			warns, err := w.ValidateCreate(ctx, tc.sts)
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
		objs         []runtime.Object
		oldObj       *appsv1.StatefulSet
		newObj       *appsv1.StatefulSet
		wantErr      error
	}{
		"no changes": {
			oldObj: &appsv1.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						constants.QueueLabel:        "queue1",
						podconstants.GroupNameLabel: "group1",
					},
				},
				Spec: appsv1.StatefulSetSpec{
					Replicas: ptr.To(int32(3)),
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Labels: map[string]string{
								constants.QueueLabel: "queue1",
							},
						},
					},
				},
			},
			newObj: &appsv1.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						constants.QueueLabel:        "queue1",
						podconstants.GroupNameLabel: "group1",
					},
				},
				Spec: appsv1.StatefulSetSpec{
					Replicas: ptr.To(int32(3)),
					Template: corev1.PodTemplateSpec{
						ObjectMeta: metav1.ObjectMeta{
							Labels: map[string]string{
								constants.QueueLabel: "queue1",
							},
						},
					},
				},
			},
			wantErr: nil,
		},
		"change in queue label": {
			oldObj: testingstatefulset.MakeStatefulSet("test-sts", "test-ns").
				Queue("test-queue").
				Obj(),
			newObj: testingstatefulset.MakeStatefulSet("test-sts", "test-ns").
				Queue("test-queue-new").
				Obj(),
		},
		"change in queue label (ReadyReplicas > 0)": {
			oldObj: testingstatefulset.MakeStatefulSet("test-sts", "test-ns").
				Queue("test-queue").
				ReadyReplicas(1).
				Obj(),
			newObj: testingstatefulset.MakeStatefulSet("test-sts", "test-ns").
				Queue("test-queue-new").
				ReadyReplicas(1).
				Obj(),
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeInvalid,
					Field: queueNameLabelPath.String(),
				},
			}.ToAggregate(),
		},
		"set queue label": {
			oldObj: testingstatefulset.MakeStatefulSet("test-sts", "test-ns").
				Obj(),
			newObj: testingstatefulset.MakeStatefulSet("test-sts", "test-ns").
				Queue("test-queue").
				Obj(),
		},
		"set queue label (ReadyReplicas > 0)": {
			oldObj: testingstatefulset.MakeStatefulSet("test-sts", "test-ns").
				ReadyReplicas(1).
				Obj(),
			newObj: testingstatefulset.MakeStatefulSet("test-sts", "test-ns").
				Queue("test-queue").
				ReadyReplicas(1).
				Obj(),
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeInvalid,
					Field: queueNameLabelPath.String(),
				},
			}.ToAggregate(),
		},
		"delete queue name": {
			oldObj: testingstatefulset.MakeStatefulSet("test-sts", "test-ns").
				Queue("test-queue").
				Obj(),
			newObj: testingstatefulset.MakeStatefulSet("test-sts", "test-ns").
				Obj(),
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeInvalid,
					Field: queueNameLabelPath.String(),
				},
			}.ToAggregate(),
		},
		"change in priority class label": {
			oldObj: &appsv1.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						constants.QueueLabel:                 "queue1",
						constants.WorkloadPriorityClassLabel: "priority1",
					},
				},
			},
			newObj: &appsv1.StatefulSet{
				ObjectMeta: metav1.ObjectMeta{
					Labels: map[string]string{
						constants.QueueLabel:                 "queue1",
						constants.WorkloadPriorityClassLabel: "priority2",
					},
				},
			},
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeInvalid,
					Field: priorityClassNameLabelPath.String(),
				},
			}.ToAggregate(),
		},
		"change in replicas (scale down to zero)": {
			oldObj: &appsv1.StatefulSet{
				Spec: appsv1.StatefulSetSpec{
					Replicas: ptr.To(int32(3)),
				},
			},
			newObj: &appsv1.StatefulSet{
				Spec: appsv1.StatefulSetSpec{
					Replicas: ptr.To(int32(0)),
				},
			},
		},
		"change in replicas (scale up from zero)": {
			oldObj: &appsv1.StatefulSet{
				Spec: appsv1.StatefulSetSpec{
					Replicas: ptr.To(int32(0)),
				},
			},
			newObj: &appsv1.StatefulSet{
				Spec: appsv1.StatefulSetSpec{
					Replicas: ptr.To(int32(3)),
				},
			},
		},
		"change in replicas (scale up while the previous scaling operation is still in progress)": {
			oldObj: testingstatefulset.MakeStatefulSet("test-sts", "test-ns").
				Queue("test-queue").
				Replicas(0).
				StatusReplicas(3).
				Obj(),
			newObj: testingstatefulset.MakeStatefulSet("test-sts", "test-ns").
				Queue("test-queue").
				Replicas(3).
				StatusReplicas(1).
				Obj(),
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeForbidden,
					Field: replicasPath.String(),
				},
			}.ToAggregate(),
		},
		"change in replicas (scale up)": {
			oldObj: testingstatefulset.MakeStatefulSet("test-sts", "test-ns").
				Queue("test-queue").
				Replicas(3).
				Obj(),
			newObj: testingstatefulset.MakeStatefulSet("test-sts", "test-ns").
				Queue("test-queue").
				Replicas(4).
				Obj(),
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeInvalid,
					Field: replicasPath.String(),
				},
			}.ToAggregate(),
		},

		"change in replicas (scale up without queue-name while the previous scaling operation is still in progress)": {
			oldObj: testingstatefulset.MakeStatefulSet("test-sts", "test-ns").
				Replicas(0).
				StatusReplicas(3).
				Obj(),
			newObj: testingstatefulset.MakeStatefulSet("test-sts", "test-ns").
				Replicas(3).
				StatusReplicas(1).
				Obj(),
		},
		"change in replicas (scale up without queue-name)": {
			oldObj: testingstatefulset.MakeStatefulSet("test-sts", "test-ns").
				Replicas(3).
				Obj(),
			newObj: testingstatefulset.MakeStatefulSet("test-sts", "test-ns").
				Replicas(4).
				Obj(),
		},
		"change in replicas (scale up with AppWrapper ownerReference while the previous scaling operation is still in progress)": {
			integrations: []string{appwrapper.FrameworkName},
			objs: []runtime.Object{
				testingappwrapper.MakeAppWrapper("test-app-wrapper", "test-ns").
					UID("test-app-wrapper").
					Queue("test-queue").
					Obj(),
			},
			oldObj: testingstatefulset.MakeStatefulSet("test-sts", "test-ns").
				Queue("test-queue").
				Replicas(0).
				StatusReplicas(3).
				Obj(),
			newObj: testingstatefulset.MakeStatefulSet("test-sts", "test-ns").
				OwnerReference("test-app-wrapper", awv1beta2.GroupVersion.WithKind(awv1beta2.AppWrapperKind)).
				Queue("test-queue").
				Replicas(3).
				StatusReplicas(1).
				Obj(),
		},
		"change in replicas (scale up with AppWrapper ownerReference)": {
			integrations: []string{appwrapper.FrameworkName},
			objs: []runtime.Object{
				testingappwrapper.MakeAppWrapper("test-app-wrapper", "test-ns").
					UID("test-app-wrapper").
					Queue("test-queue").
					Obj(),
			},
			oldObj: testingstatefulset.MakeStatefulSet("test-sts", "test-ns").
				Queue("test-queue").
				Replicas(3).
				Obj(),
			newObj: testingstatefulset.MakeStatefulSet("test-sts", "test-ns").
				OwnerReference("test-app-wrapper", awv1beta2.GroupVersion.WithKind(awv1beta2.AppWrapperKind)).
				Queue("test-queue").
				Replicas(4).
				Obj(),
		},
		"change in replicas (scale up with LeaderWorkerSet ownerReference while the previous scaling operation is still in progress)": {
			integrations: []string{leaderworkerset.FrameworkName},
			objs: []runtime.Object{
				testingleaderworkerset.MakeLeaderWorkerSet("test-lws", "test-ns").
					UID("test-lws").
					Queue("test-queue").
					Obj(),
			},
			oldObj: testingstatefulset.MakeStatefulSet("test-sts", "test-ns").
				Queue("test-queue").
				Replicas(0).
				StatusReplicas(3).
				Obj(),
			newObj: testingstatefulset.MakeStatefulSet("test-sts", "test-ns").
				OwnerReference("test-lws", leaderworkersetv1.GroupVersion.WithKind("LeaderWorkerSet")).
				Queue("test-queue").
				Replicas(3).
				StatusReplicas(1).
				Obj(),
		},
		"change in replicas (scale up with LeaderWorkerSet ownerReference)": {
			integrations: []string{leaderworkerset.FrameworkName},
			objs: []runtime.Object{
				testingleaderworkerset.MakeLeaderWorkerSet("test-lws", "test-ns").
					UID("test-lws").
					Queue("test-queue").
					Obj(),
			},
			oldObj: testingstatefulset.MakeStatefulSet("test-sts", "test-ns").
				Queue("test-queue").
				Replicas(3).
				Obj(),
			newObj: testingstatefulset.MakeStatefulSet("test-sts", "test-ns").
				OwnerReference("test-lws", leaderworkersetv1.GroupVersion.WithKind("LeaderWorkerSet")).
				Queue("test-queue").
				Replicas(4).
				Obj(),
		},
		"attempt to change resources in container": {
			oldObj: testingstatefulset.MakeStatefulSet("test-sts", "test-ns").
				Queue("test-queue").
				Template(corev1.PodTemplateSpec{
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
				Obj(),
			newObj: testingstatefulset.MakeStatefulSet("test-sts", "test-ns").
				Queue("test-queue").
				Template(corev1.PodTemplateSpec{
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
				Obj(),
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeInvalid,
					Field: podSpecPath.Child("containers").Index(0).Child("resources", "requests").String(),
				},
			}.ToAggregate(),
		},
		"attempt to change resources in init container": {
			oldObj: testingstatefulset.MakeStatefulSet("test-sts", "test-ns").
				Queue("test-queue").
				Template(corev1.PodTemplateSpec{
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
				Obj(),
			newObj: testingstatefulset.MakeStatefulSet("test-sts", "test-ns").
				Queue("test-queue").
				Template(corev1.PodTemplateSpec{
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
				Obj(),
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeInvalid,
					Field: podSpecPath.Child("containers").Index(0).Child("resources", "requests").String(),
				},
			}.ToAggregate(),
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			t.Cleanup(jobframework.EnableIntegrationsForTest(t, tc.integrations...))
			ctx := t.Context()

			client := utiltesting.NewClientBuilder(awv1beta2.AddToScheme, leaderworkersetv1.AddToScheme).
				WithRuntimeObjects(tc.objs...).
				Build()

			wh := &Webhook{
				client: client,
			}

			_, err := wh.ValidateUpdate(ctx, tc.oldObj, tc.newObj)
			if diff := cmp.Diff(tc.wantErr, err, cmpopts.IgnoreFields(field.Error{}, "BadValue", "Detail")); diff != "" {
				t.Errorf("Unexpected error (-want,+got):\n%s", diff)
			}
		})
	}
}
