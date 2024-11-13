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

package statefulset

import (
	"context"
	"fmt"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	"sigs.k8s.io/kueue/pkg/cache"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/controller/jobs/pod"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/queue"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	testingstatefulset "sigs.k8s.io/kueue/pkg/util/testingjobs/statefulset"
)

const (
	testDefaultNamespaceName = "default"
	testDefaultQueueName     = "default"

	testStatefulSetName = "test-sts"
	testNamespaceName   = "test-ns"
	testQueueName       = "test-queue"
)

func TestDefault(t *testing.T) {
	baseStatefulSet := testingstatefulset.MakeStatefulSet(testStatefulSetName, testNamespaceName).Replicas(3)
	baseStatefulSetWithQueue := baseStatefulSet.Clone().Queue(testQueueName)

	groupName, err := GetWorkloadName(baseStatefulSet.Obj())
	if err != nil {
		t.Fatalf("unexpected error on getting workload name: %v", err)
	}

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
			statefulset:        baseStatefulSetWithQueue.Clone().Obj(),
			want: baseStatefulSetWithQueue.Clone().
				PodTemplateSpecQueue(testQueueName).
				PodTemplateAnnotation(pod.SuspendedByParentAnnotation, FrameworkName).
				PodTemplateSpecPodGroupNameLabel(groupName).
				PodTemplateSpecPodGroupTotalCountAnnotation(3).
				PodTemplateSpecPodGroupFastAdmissionAnnotation(true).
				PodTemplateSpecPodGroupServingAnnotation(true).
				PodTemplateSpecPodGroupPodIndexLabelAnnotation(appsv1.PodIndexLabel).
				Obj(),
		},
		"statefulset without replicas": {
			enableIntegrations: []string{"pod"},
			statefulset:        baseStatefulSetWithQueue.DeepCopy(),
			want: baseStatefulSetWithQueue.Clone().
				PodTemplateSpecPodGroupNameLabel(groupName).
				PodTemplateSpecPodGroupTotalCountAnnotation(3).
				PodTemplateSpecQueue(testQueueName).
				PodTemplateAnnotation(pod.SuspendedByParentAnnotation, FrameworkName).
				PodTemplateSpecPodGroupFastAdmissionAnnotation(true).
				PodTemplateSpecPodGroupServingAnnotation(true).
				PodTemplateSpecPodGroupPodIndexLabelAnnotation(appsv1.PodIndexLabel).
				Obj(),
		},
		"LocalQueueDefaulting enabled, default lq is created, job doesn't have queue label": {
			localQueueDefaulting: true,
			defaultLqExist:       true,
			statefulset:          baseStatefulSet.Clone().Namespace(testDefaultNamespaceName).Obj(),
			want: baseStatefulSet.Clone().Namespace(testDefaultNamespaceName).Queue(testDefaultQueueName).
				PodTemplateSpecQueue(testDefaultQueueName).
				PodTemplateAnnotation(pod.SuspendedByParentAnnotation, FrameworkName).
				PodTemplateSpecPodGroupNameLabel(groupName).
				PodTemplateSpecPodGroupTotalCountAnnotation(3).
				PodTemplateSpecPodGroupFastAdmissionAnnotation(true).
				PodTemplateSpecPodGroupServingAnnotation(true).
				PodTemplateSpecPodGroupPodIndexLabelAnnotation(appsv1.PodIndexLabel).
				Obj(),
		},
		"LocalQueueDefaulting enabled, default lq is created, job has queue label": {
			localQueueDefaulting: true,
			defaultLqExist:       true,
			statefulset:          baseStatefulSetWithQueue.DeepCopy(),
			want: baseStatefulSetWithQueue.Clone().
				PodTemplateSpecQueue(testQueueName).
				PodTemplateAnnotation(pod.SuspendedByParentAnnotation, FrameworkName).
				PodTemplateSpecPodGroupNameLabel(groupName).
				PodTemplateSpecPodGroupTotalCountAnnotation(3).
				PodTemplateSpecPodGroupFastAdmissionAnnotation(true).
				PodTemplateSpecPodGroupServingAnnotation(true).
				PodTemplateSpecPodGroupPodIndexLabelAnnotation(appsv1.PodIndexLabel).
				Obj(),
		},
		"LocalQueueDefaulting enabled, default lq isn't created, job doesn't have queue label": {
			localQueueDefaulting: true,
			defaultLqExist:       false,
			statefulset:          baseStatefulSet.DeepCopy(),
			want:                 baseStatefulSet.DeepCopy(),
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
				if err := queueManager.AddLocalQueue(ctx, utiltesting.MakeLocalQueue(testDefaultQueueName, testDefaultNamespaceName).
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
		"without queue name": {
			sts: testingstatefulset.MakeStatefulSet(testStatefulSetName, testNamespaceName).Obj(),
		},
		"valid queue name": {
			sts: testingstatefulset.MakeStatefulSet(testStatefulSetName, testNamespaceName).
				Queue("test-queue").
				Obj(),
		},
		"invalid queue name": {
			sts: testingstatefulset.MakeStatefulSet(testStatefulSetName, testNamespaceName).
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
		oldObj  *appsv1.StatefulSet
		newObj  *appsv1.StatefulSet
		wantErr error
	}{
		"no changes": {
			oldObj: testingstatefulset.MakeStatefulSet(testStatefulSetName, testNamespaceName).
				Queue(testQueueName).
				Obj(),
			newObj: testingstatefulset.MakeStatefulSet(testStatefulSetName, testNamespaceName).
				Queue(testQueueName).
				Obj(),
		},
		"change in queue label": {
			oldObj: testingstatefulset.MakeStatefulSet(testStatefulSetName, testNamespaceName).
				Queue(testQueueName).
				Obj(),
			newObj: testingstatefulset.MakeStatefulSet(testStatefulSetName, testNamespaceName).
				Queue(fmt.Sprintf("%s-new", testQueueName)).
				Obj(),
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeInvalid,
					Field: queueNameLabelPath.String(),
				},
			}.ToAggregate(),
		},
		"change in pod template queue label": {
			oldObj: testingstatefulset.MakeStatefulSet(testStatefulSetName, testNamespaceName).
				PodTemplateSpecQueue(testQueueName).
				Obj(),
			newObj: testingstatefulset.MakeStatefulSet(testStatefulSetName, testNamespaceName).
				PodTemplateSpecQueue(fmt.Sprintf("%s-new", testQueueName)).
				Obj(),
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeInvalid,
					Field: podSpecQueueNameLabelPath.String(),
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
			oldObj: &appsv1.StatefulSet{
				Spec: appsv1.StatefulSetSpec{
					Replicas: ptr.To(int32(0)),
				},
				Status: appsv1.StatefulSetStatus{
					Replicas: 3,
				},
			},
			newObj: &appsv1.StatefulSet{
				Spec: appsv1.StatefulSetSpec{
					Replicas: ptr.To(int32(3)),
				},
				Status: appsv1.StatefulSetStatus{
					Replicas: 1,
				},
			},
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeForbidden,
					Field: replicasPath.String(),
				},
			}.ToAggregate(),
		},
		"change in replicas (scale up)": {
			oldObj: &appsv1.StatefulSet{
				Spec: appsv1.StatefulSetSpec{
					Replicas: ptr.To(int32(3)),
				},
			},
			newObj: &appsv1.StatefulSet{
				Spec: appsv1.StatefulSetSpec{
					Replicas: ptr.To(int32(4)),
				},
			},
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeInvalid,
					Field: replicasPath.String(),
				},
			}.ToAggregate(),
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			ctx := context.Background()

			wh := &Webhook{}

			_, err := wh.ValidateUpdate(ctx, tc.oldObj, tc.newObj)
			if diff := cmp.Diff(tc.wantErr, err, cmpopts.IgnoreFields(field.Error{}, "BadValue", "Detail")); diff != "" {
				t.Errorf("Unexpected error (-want,+got):\n%s", diff)
			}
		})
	}
}
