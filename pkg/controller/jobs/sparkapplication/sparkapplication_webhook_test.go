/*
Copyright 2025 The Kubernetes Authors.

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

package sparkapplication

import (
	"context"
	"testing"

	"github.com/google/go-cmp/cmp"

	apivalidation "k8s.io/apimachinery/pkg/api/validation"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/utils/ptr"

	"sigs.k8s.io/kueue/pkg/cache"
	"sigs.k8s.io/kueue/pkg/controller/constants"
	podcontroller "sigs.k8s.io/kueue/pkg/controller/jobs/pod"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/queue"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	sparkapptesting "sigs.k8s.io/kueue/pkg/util/testingjobs/sparkapplication"

	kfsparkapi "github.com/kubeflow/spark-operator/api/v1beta2"
)

func TestDefaults(t *testing.T) {
	baseApp := sparkapptesting.MakeSparkApplication("job", "default")
	expectedBaseApp := sparkapptesting.MakeSparkApplication("job", "default").
		DriverAnnotation(
			podcontroller.SkipWebhookAnnotationKey,
			podcontroller.SkipWebhookAnnotationValue,
		).Suspend(true)

	testCases := map[string]struct {
		sparkApp                   *kfsparkapi.SparkApplication
		manageJobsWithoutQueueName bool
		localQueueDefaulting       bool
		defaultLqExist             bool
		want                       *kfsparkapi.SparkApplication
	}{
		"unmanaged": {
			sparkApp: baseApp.Clone().Obj(),
			want:     baseApp.Clone().Obj(),
		},
		"unmanaged: LocalQueueDefaulting=true, DefaultLocalQueue not exist": {
			sparkApp:             baseApp.Clone().Obj(),
			want:                 baseApp.Clone().Obj(),
			localQueueDefaulting: true,
		},
		"managed: LocalQueueDefaulting=true, DefaultLocalQueue exists": {
			sparkApp:             baseApp.Clone().Obj(),
			want:                 expectedBaseApp.Clone().Queue("default").Obj(),
			localQueueDefaulting: true,
			defaultLqExist:       true,
		},
		"managed: manageJobsWithoutQueueName=true, LocalQueueDefaulting=false, default LocalQueue not exist": {
			sparkApp:                   baseApp.Clone().Obj(),
			want:                       expectedBaseApp.Clone().Obj(),
			manageJobsWithoutQueueName: true,
		},
		"managed: manageJobsWithoutQueueName=true, LocalQueueDefaulting=true, default LocalQueue not exist": {
			sparkApp:                   baseApp.Clone().Obj(),
			want:                       expectedBaseApp.Clone().Obj(),
			manageJobsWithoutQueueName: true,
			localQueueDefaulting:       true,
		},
		"managed: manageJobsWithoutQueueName=true, LocalQueueDefaulting=true, default LocalQueue exists": {
			sparkApp:                   baseApp.Clone().Obj(),
			want:                       expectedBaseApp.Clone().Queue("default").Obj(),
			manageJobsWithoutQueueName: true,
			localQueueDefaulting:       true,
			defaultLqExist:             true,
		},
		"managed: with queue, dynamicAllocation=nil(integrationMode=Unknown)": {
			sparkApp: baseApp.Clone().Queue("queue").Obj(),
			want:     expectedBaseApp.Clone().Queue("queue").Obj(),
		},
		"managed: with queue, dynamicAllocation.Enabled=true(integrationMode=ExecutorDetached)": {
			sparkApp: baseApp.Clone().Queue("queue").DynamicAllocation(true).Obj(),
			want: expectedBaseApp.Clone().Queue("queue").DynamicAllocation(true).
				ExecutorLabel(
					constants.QueueLabel, "queue",
				).
				ExecutorAnnotation(
					podcontroller.SuspendedByParentAnnotation, FrameworkName,
				).Obj(),
		},
		"managed: with queue, dynamicAllocation.Enabled=false(integrationMode=Aggregated)": {
			sparkApp: baseApp.Clone().Queue("queue").DynamicAllocation(false).Obj(),
			want: expectedBaseApp.Clone().Queue("queue").DynamicAllocation(false).
				ExecutorAnnotation(
					podcontroller.SkipWebhookAnnotationKey,
					podcontroller.SkipWebhookAnnotationValue,
				).Obj(),
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.ManagedJobsNamespaceSelector, false)
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

			wh := &SparkApplicationWebhook{
				client:                     cli,
				manageJobsWithoutQueueName: tc.manageJobsWithoutQueueName,
				queues:                     queueManager,
			}
			if err := wh.Default(ctx, tc.sparkApp); err != nil {
				t.Errorf("failed to set defaults for sparkoperator.k8s.io/v1beta2/sparkapplication: %s", err)
			}
			if diff := cmp.Diff(tc.want, tc.sparkApp); len(diff) != 0 {
				t.Errorf("Default() mismatch (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestValidateCreate(t *testing.T) {
	baseApp := sparkapptesting.MakeSparkApplication("job", "default")
	validAppAggregated := baseApp.Clone().
		Queue("queue").
		DynamicAllocation(false).
		Suspend(true).
		ExecutorInstances(1).
		DriverAnnotation(
			podcontroller.SkipWebhookAnnotationKey, podcontroller.SkipWebhookAnnotationValue,
		).
		ExecutorAnnotation(
			podcontroller.SkipWebhookAnnotationKey, podcontroller.SkipWebhookAnnotationValue,
		)
	validAppDetached := baseApp.Clone().
		Queue("queue").
		DynamicAllocation(true).
		DynamicAllocationMaxExecutor(1).
		Suspend(true).
		DriverAnnotation(
			podcontroller.SkipWebhookAnnotationKey, podcontroller.SkipWebhookAnnotationValue,
		).
		ExecutorLabel(
			constants.QueueLabel, "queue",
		).
		ExecutorAnnotation(
			podcontroller.SuspendedByParentAnnotation, FrameworkName,
		)

	testCases := map[string]struct {
		sparkApp                   *kfsparkapi.SparkApplication
		manageJobsWithoutQueueName bool
		wantErr                    error
	}{
		"valid: unmanaged": {
			sparkApp: baseApp.Clone().Obj(),
		},
		"invalid: managed, dynamicAllocation=nil": {
			sparkApp: baseApp.Clone().Queue("queue").Obj(),
			wantErr: field.ErrorList{
				field.Invalid(field.NewPath("spec", "driver", "annotations").Key(podcontroller.SkipWebhookAnnotationKey), "", "must be true"),
				field.Required(field.NewPath("spec", "dynamicAllocation"), "kueue managed sparkapplication must set spec.dynamicAllocation explicitly (must not be nil)"),
			}.ToAggregate(),
		},

		"valid: managed, dynamicAllocation=true(integrationMode=ExecutorDetached)": {
			sparkApp: validAppDetached.Clone().Obj(),
		},
		"invalid: managed, dynamicAllocation=true(integrationMode=ExecutorDetached)": {
			sparkApp: baseApp.Clone().Queue("queue").DynamicAllocation(true).
				DriverAnnotation(
					podcontroller.SuspendedByParentAnnotation, "",
				).
				ExecutorAnnotation(
					podcontroller.SkipWebhookAnnotationKey, "",
				).
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(
					field.NewPath("spec", "driver", "annotations").Key(podcontroller.SkipWebhookAnnotationKey), "", "must be true",
				),
				field.Invalid(
					field.NewPath("spec", "driver", "annotations").Key(podcontroller.SuspendedByParentAnnotation), "", "must not exist",
				),
				field.Invalid(
					field.NewPath("spec", "executor", "annotations").Key(podcontroller.SkipWebhookAnnotationKey), "", "must not exist",
				),
				field.Invalid(
					field.NewPath("spec", "executor", "annotations").Key(podcontroller.SuspendedByParentAnnotation),
					"", "must be sparkoperator.k8s.io/sparkapplication",
				),
				field.Required(
					field.NewPath("spec", "dynamicAllocation", "maxExecutors"), "must not be nil",
				),
			}.ToAggregate(),
		},

		"valid: managed, dynamicAllocation=false(integrationMode=Aggregated)": {
			sparkApp: validAppAggregated.Clone().Obj(),
		},
		"invalid: managed, dynamicAllocation=false(integrationMode=Aggregated)": {
			sparkApp: baseApp.Clone().Queue("queue").DynamicAllocation(false).
				DriverAnnotation(
					podcontroller.SuspendedByParentAnnotation, "",
				).
				ExecutorAnnotation(
					podcontroller.SuspendedByParentAnnotation, "",
				).
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(
					field.NewPath("spec", "driver", "annotations").Key(podcontroller.SkipWebhookAnnotationKey), "", "must be true",
				),
				field.Invalid(
					field.NewPath("spec", "driver", "annotations").Key(podcontroller.SuspendedByParentAnnotation), "", "must not exist",
				),
				field.Invalid(
					field.NewPath("spec", "executor", "annotations").Key(podcontroller.SkipWebhookAnnotationKey), "", "must be true",
				),
				field.Invalid(
					field.NewPath("spec", "executor", "annotations").Key(podcontroller.SuspendedByParentAnnotation), "", "must not exist",
				),
				field.Required(
					field.NewPath("spec", "executor", "instances"), "must not be nil",
				),
			}.ToAggregate(),
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			wh := &SparkApplicationWebhook{}
			_, result := wh.ValidateCreate(context.Background(), tc.sparkApp)
			if diff := cmp.Diff(tc.wantErr, result); diff != "" {
				t.Errorf("ValidateCreate() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestValidateUpdate(t *testing.T) {
	baseApp := sparkapptesting.MakeSparkApplication("job", "default").
		DynamicAllocation(false).
		ExecutorInstances(1).
		DriverAnnotation(
			podcontroller.SkipWebhookAnnotationKey, podcontroller.SkipWebhookAnnotationValue,
		).
		ExecutorAnnotation(
			podcontroller.SkipWebhookAnnotationKey, podcontroller.SkipWebhookAnnotationValue,
		)

	testCases := map[string]struct {
		oldSparkApp *kfsparkapi.SparkApplication
		newSparkApp *kfsparkapi.SparkApplication
		wantErr     error
	}{
		"valid: unmanaged": {
			oldSparkApp: baseApp.Clone().
				Obj(),
			newSparkApp: baseApp.Clone().
				Suspend(true).
				Obj(),
			wantErr: nil,
		},
		"valid: managed, suspend=true, nothing changed": {
			oldSparkApp: baseApp.Clone().
				Suspend(true).
				Queue("queue").
				Obj(),
			newSparkApp: baseApp.Clone().
				Suspend(true).
				Queue("queue").
				Obj(),
			wantErr: nil,
		},
		"valid: managed, suspend=true, update unrelated metadata": {
			oldSparkApp: baseApp.Clone().
				Suspend(true).
				Queue("queue").
				Obj(),
			newSparkApp: baseApp.Clone().
				Suspend(true).
				Queue("queue").
				Label("key", "value").
				Obj(),
			wantErr: nil,
		},
		"valid: managed, suspend=true, update Queue": {
			oldSparkApp: baseApp.Clone().
				Suspend(true).
				Queue("queue").
				Obj(),
			newSparkApp: baseApp.Clone().
				Suspend(true).
				Queue("queue2").
				Obj(),
			wantErr: nil,
		},
		"invalid: managed, suspend=true, update WorkloadPriorityClass": {
			oldSparkApp: baseApp.Clone().
				Suspend(true).
				Queue("queue").
				WorkloadPriorityClass("wp1").
				Obj(),
			newSparkApp: baseApp.Clone().
				Suspend(true).
				Queue("queue").
				WorkloadPriorityClass("wp2").
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(field.NewPath("metadata", "labels").Key(constants.WorkloadPriorityClassLabel), "wp2", apivalidation.FieldImmutableErrorMsg),
			}.ToAggregate(),
		},
		"invalid: managed, suspended=false, update Queue": {
			oldSparkApp: baseApp.Clone().
				Suspend(false).
				Queue("queue").
				Obj(),
			newSparkApp: baseApp.Clone().
				Suspend(false).
				Queue("queue2").
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(field.NewPath("metadata", "labels").Key(constants.QueueLabel), "queue2", apivalidation.FieldImmutableErrorMsg),
			}.ToAggregate(),
		},
		"invalid: managed, suspended=false, dynamicAllocation=false, update executor.instances": {
			oldSparkApp: baseApp.Clone().
				Suspend(false).
				Queue("queue").
				ExecutorInstances(1).
				Obj(),
			newSparkApp: baseApp.Clone().
				Suspend(false).
				Queue("queue").
				ExecutorInstances(2).
				Obj(),
			wantErr: field.ErrorList{
				field.Invalid(field.NewPath("spec", "executor", "instances"), ptr.To(int32(2)), apivalidation.FieldImmutableErrorMsg),
			}.ToAggregate(),
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			wh := &SparkApplicationWebhook{}
			_, result := wh.ValidateUpdate(context.Background(), tc.oldSparkApp, tc.newSparkApp)
			if diff := cmp.Diff(tc.wantErr, result); diff != "" {
				t.Errorf("ValidateUpdate() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
