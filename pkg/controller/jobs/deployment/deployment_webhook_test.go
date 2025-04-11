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

package deployment

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	appsv1 "k8s.io/api/apps/v1"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	"sigs.k8s.io/kueue/pkg/cache"
	"sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	podconstants "sigs.k8s.io/kueue/pkg/controller/jobs/pod/constants"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/queue"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	testingdeployment "sigs.k8s.io/kueue/pkg/util/testingjobs/deployment"
)

func TestDefault(t *testing.T) {
	testCases := map[string]struct {
		deployment           *appsv1.Deployment
		localQueueDefaulting bool
		defaultLqExist       bool
		want                 *appsv1.Deployment
	}{
		"deployment without queue": {
			deployment: testingdeployment.MakeDeployment("test-pod", "").Obj(),
			want:       testingdeployment.MakeDeployment("test-pod", "").Obj(),
		},
		"deployment with queue": {
			deployment: testingdeployment.MakeDeployment("test-pod", "").
				Queue("test-queue").
				Obj(),
			want: testingdeployment.MakeDeployment("test-pod", "").
				PodTemplateSpecManagedByKueue().
				Queue("test-queue").
				PodTemplateSpecQueue("test-queue").
				PodTemplateAnnotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
				Obj(),
		},
		"deployment with queue and pod template spec queue": {
			deployment: testingdeployment.MakeDeployment("test-pod", "").
				Queue("new-test-queue").
				PodTemplateSpecQueue("test-queue").
				Obj(),
			want: testingdeployment.MakeDeployment("test-pod", "").
				PodTemplateSpecManagedByKueue().
				Queue("new-test-queue").
				PodTemplateSpecQueue("new-test-queue").
				PodTemplateAnnotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
				Obj(),
		},
		"deployment without queue with pod template spec queue": {
			deployment: testingdeployment.MakeDeployment("test-pod", "").PodTemplateSpecQueue("test-queue").Obj(),
			want:       testingdeployment.MakeDeployment("test-pod", "").PodTemplateSpecQueue("test-queue").Obj(),
		},
		"LocalQueueDefaulting enabled, default lq is created, job doesn't have queue label": {
			localQueueDefaulting: true,
			defaultLqExist:       true,
			deployment:           testingdeployment.MakeDeployment("test-pod", "default").Obj(),
			want: testingdeployment.MakeDeployment("test-pod", "default").
				PodTemplateSpecManagedByKueue().
				Queue("default").
				PodTemplateSpecQueue("default").
				PodTemplateAnnotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
				Obj(),
		},
		"LocalQueueDefaulting enabled, default lq is created, job has queue label": {
			localQueueDefaulting: true,
			defaultLqExist:       true,
			deployment:           testingdeployment.MakeDeployment("test-pod", "").Queue("test-queue").Obj(),
			want: testingdeployment.MakeDeployment("test-pod", "").
				PodTemplateSpecManagedByKueue().
				Queue("test-queue").
				PodTemplateSpecQueue("test-queue").
				PodTemplateAnnotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
				Obj(),
		},
		"LocalQueueDefaulting enabled, default lq isn't created, job doesn't have queue label": {
			localQueueDefaulting: true,
			defaultLqExist:       false,
			deployment:           testingdeployment.MakeDeployment("test-pod", "").Obj(),
			want: testingdeployment.MakeDeployment("test-pod", "").
				Obj(),
		},
		"deployment with queue and priority class": {
			deployment: testingdeployment.MakeDeployment("test-pod", "").
				Queue("test-queue").
				Label(constants.WorkloadPriorityClassLabel, "test").
				Obj(),
			want: testingdeployment.MakeDeployment("test-pod", "").
				PodTemplateSpecManagedByKueue().
				Queue("test-queue").
				Label(constants.WorkloadPriorityClassLabel, "test").
				PodTemplateSpecQueue("test-queue").
				PodTemplateAnnotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
				PodTemplateSpecLabel(constants.WorkloadPriorityClassLabel, "test").
				Obj(),
		},
		"deployment with queue, priority class and pod template spec queue, priority class": {
			deployment: testingdeployment.MakeDeployment("test-pod", "").
				Queue("new-test-queue").
				Label(constants.WorkloadPriorityClassLabel, "new-test").
				PodTemplateSpecQueue("test-queue").
				PodTemplateSpecLabel(constants.WorkloadPriorityClassLabel, "test").
				Obj(),
			want: testingdeployment.MakeDeployment("test-pod", "").
				PodTemplateSpecManagedByKueue().
				Queue("new-test-queue").
				Label(constants.WorkloadPriorityClassLabel, "new-test").
				PodTemplateSpecQueue("new-test-queue").
				PodTemplateAnnotation(podconstants.SuspendedByParentAnnotation, FrameworkName).
				PodTemplateSpecLabel(constants.WorkloadPriorityClassLabel, "new-test").
				Obj(),
		},
		"deployment without queue with pod template spec queue and priority class": {
			deployment: testingdeployment.MakeDeployment("test-pod", "").
				PodTemplateSpecQueue("test-queue").
				PodTemplateSpecLabel(constants.WorkloadPriorityClassLabel, "test").
				Obj(),
			want: testingdeployment.MakeDeployment("test-pod", "").
				PodTemplateSpecQueue("test-queue").
				PodTemplateSpecLabel(constants.WorkloadPriorityClassLabel, "test").
				Obj(),
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)
			features.SetFeatureGateDuringTest(t, features.LocalQueueDefaulting, tc.localQueueDefaulting)
			t.Cleanup(jobframework.EnableIntegrationsForTest(t, "pod"))
			builder := utiltesting.NewClientBuilder()
			client := builder.Build()
			cqCache := cache.New(client)
			queueManager := queue.NewManager(client, cqCache)
			if tc.defaultLqExist {
				if err := queueManager.AddLocalQueue(ctx, utiltesting.MakeLocalQueue("default", "default").
					ClusterQueue("cluster-queue").
					Obj()); err != nil {
					t.Fatalf("failed to create default local queue: %s", err)
				}
			}
			w := &Webhook{
				client: client,
				queues: queueManager,
			}

			if err := w.Default(ctx, tc.deployment); err != nil {
				t.Errorf("failed to set defaults for v1/deployment: %s", err)
			}
			if diff := cmp.Diff(tc.want, tc.deployment); len(diff) != 0 {
				t.Errorf("Default() mismatch (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestValidateCreate(t *testing.T) {
	testCases := map[string]struct {
		deployment *appsv1.Deployment
		wantErr    error
		wantWarns  admission.Warnings
	}{
		"without queue": {
			deployment: testingdeployment.MakeDeployment("test-pod", "").Obj(),
		},
		"valid queue name": {
			deployment: testingdeployment.MakeDeployment("test-pod", "").
				Queue("test-queue").
				Obj(),
		},
		"invalid queue name": {
			deployment: testingdeployment.MakeDeployment("test-pod", "").
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

			warns, err := w.ValidateCreate(ctx, tc.deployment)
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
		oldDeployment *appsv1.Deployment
		newDeployment *appsv1.Deployment
		wantErr       error
		wantWarns     admission.Warnings
	}{
		"without queue (no changes)": {
			oldDeployment: testingdeployment.MakeDeployment("test-pod", "").Obj(),
			newDeployment: testingdeployment.MakeDeployment("test-pod", "").Obj(),
		},
		"without queue": {
			oldDeployment: testingdeployment.MakeDeployment("test-pod", "").
				Queue("test-queue").
				Obj(),
			newDeployment: testingdeployment.MakeDeployment("test-pod", "").Obj(),
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeInvalid,
					Field: "metadata.labels[kueue.x-k8s.io/queue-name]",
				},
			}.ToAggregate(),
		},
		"with queue (no changes)": {
			oldDeployment: testingdeployment.MakeDeployment("test-pod", "").
				Queue("test-queue").
				Obj(),
			newDeployment: testingdeployment.MakeDeployment("test-pod", "").
				Queue("test-queue").
				Obj(),
		},
		"with queue": {
			oldDeployment: testingdeployment.MakeDeployment("test-pod", "").Obj(),
			newDeployment: testingdeployment.MakeDeployment("test-pod", "").
				Queue("test-queue").
				Obj(),
		},
		"with queue (invalid)": {
			oldDeployment: testingdeployment.MakeDeployment("test-pod", "").
				Queue("test/queue").
				Obj(),
			newDeployment: testingdeployment.MakeDeployment("test-pod", "").
				Queue("test/queue").
				Obj(),
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeInvalid,
					Field: "metadata.labels[kueue.x-k8s.io/queue-name]",
				},
			}.ToAggregate(),
		},
		"with queue (ready replicas)": {
			oldDeployment: testingdeployment.MakeDeployment("test-pod", "").
				Queue("test-queue").
				ReadyReplicas(1).
				Obj(),
			newDeployment: testingdeployment.MakeDeployment("test-pod", "").
				Queue("test-queue-new").
				ReadyReplicas(1).
				Obj(),
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeInvalid,
					Field: "metadata.labels[kueue.x-k8s.io/queue-name]",
				},
			}.ToAggregate(),
		},
		"update priority-class": {
			oldDeployment: testingdeployment.MakeDeployment("test-pod", "").
				Queue("test-queue").
				Label(constants.WorkloadPriorityClassLabel, "test").
				Obj(),
			newDeployment: testingdeployment.MakeDeployment("test-pod", "").
				Queue("test-queue").
				Label(constants.WorkloadPriorityClassLabel, "new-test").
				Obj(),
			wantErr: field.ErrorList{
				&field.Error{
					Type:  field.ErrorTypeInvalid,
					Field: "metadata.labels[kueue.x-k8s.io/priority-class]",
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

			warns, err := w.ValidateUpdate(ctx, tc.oldDeployment, tc.newDeployment)
			if diff := cmp.Diff(tc.wantErr, err, cmpopts.IgnoreFields(field.Error{}, "BadValue", "Detail")); diff != "" {
				t.Errorf("Unexpected error (-want,+got):\n%s", diff)
			}
			if diff := cmp.Diff(warns, tc.wantWarns); diff != "" {
				t.Errorf("Expected different list of warnings (-want,+got):\n%s", diff)
			}
		})
	}
}
