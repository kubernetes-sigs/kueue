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
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"
	"go.uber.org/mock/gomock"
	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	mocks "sigs.k8s.io/kueue/internal/mocks/controller/jobframework"
	qcache "sigs.k8s.io/kueue/pkg/cache/queue"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/features"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	utiljob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
)

func TestBaseWebhookDefault(t *testing.T) {
	testcases := map[string]struct {
		manageJobsWithoutQueueName bool
		defaultLqExist             bool
		enableMultiKueue           bool
		job                        *batchv1.Job
		want                       *batchv1.Job
	}{
		"update the suspend field with 'manageJobsWithoutQueueName=false'": {
			job:  utiljob.MakeJob("job", metav1.NamespaceDefault).Queue("queue").Obj(),
			want: utiljob.MakeJob("job", metav1.NamespaceDefault).Queue("queue").Suspend(true).Obj(),
		},
		"update the suspend field 'manageJobsWithoutQueueName=true'": {
			manageJobsWithoutQueueName: true,
			job:                        utiljob.MakeJob("job", metav1.NamespaceDefault).Queue("queue").Obj(),
			want:                       utiljob.MakeJob("job", metav1.NamespaceDefault).Queue("queue").Suspend(true).Obj(),
		},
		"default lq is created, job doesn't have queue label": {
			defaultLqExist: true,
			job:            utiljob.MakeJob("job", metav1.NamespaceDefault).Obj(),
			want: utiljob.MakeJob("job", metav1.NamespaceDefault).
				Label(constants.QueueLabel, "default").
				Obj(),
		},
		"default lq is created, job has queue label": {
			defaultLqExist: true,
			job:            utiljob.MakeJob("job", metav1.NamespaceDefault).Queue("queue").Obj(),
			want:           utiljob.MakeJob("job", metav1.NamespaceDefault).Queue("queue").Obj(),
		},
		"default lq isn't created, job doesn't have queue label": {
			defaultLqExist: false,
			job:            utiljob.MakeJob("job", metav1.NamespaceDefault).Obj(),
			want:           utiljob.MakeJob("job", metav1.NamespaceDefault).Obj(),
		},
		"ManagedByDefaulting, targeting multikueue local queue": {
			job: utiljob.MakeJob("job", metav1.NamespaceDefault).Queue("multikueue").Obj(),
			want: utiljob.MakeJob("job", metav1.NamespaceDefault).
				Queue("multikueue").
				ManagedBy(kueue.MultiKueueControllerName).
				Obj(),
			enableMultiKueue: true,
		},
		"ManagedByDefaulting, targeting multikueue local queue but already managaed by someone else": {
			job: utiljob.MakeJob("job", metav1.NamespaceDefault).
				Queue("multikueue").
				ManagedBy("someone-else").
				Obj(),
			want: utiljob.MakeJob("job", metav1.NamespaceDefault).
				Queue("multikueue").
				ManagedBy("someone-else").
				Obj(),
			enableMultiKueue: true,
		},
		"ManagedByDefaulting, targeting non-multikueue local queue": {
			job: utiljob.MakeJob("job", metav1.NamespaceDefault).
				Queue("queue").
				Obj(),
			want: utiljob.MakeJob("job", metav1.NamespaceDefault).
				Queue("queue").
				Obj(),
			enableMultiKueue: true,
		},
	}
	for name, tc := range testcases {
		t.Run(name, func(t *testing.T) {
			ctx, log := utiltesting.ContextWithLog(t)
			features.SetFeatureGateDuringTest(t, features.MultiKueue, tc.enableMultiKueue)
			clientBuilder := utiltesting.NewClientBuilder().
				WithObjects(
					utiltesting.MakeNamespace(metav1.NamespaceDefault),
				)
			cl := clientBuilder.Build()
			cqCache := schdcache.New(cl)
			queueManager := qcache.NewManagerForUnitTests(cl, cqCache)
			if tc.defaultLqExist {
				if err := queueManager.AddLocalQueue(ctx, utiltestingapi.MakeLocalQueue("default", metav1.NamespaceDefault).
					ClusterQueue("cluster-queue").Obj()); err != nil {
					t.Fatalf("failed to create default local queue: %s", err)
				}
			}
			if tc.enableMultiKueue {
				if err := queueManager.AddLocalQueue(ctx, utiltestingapi.MakeLocalQueue("multikueue", metav1.NamespaceDefault).
					ClusterQueue("cluster-queue").Obj()); err != nil {
					t.Fatalf("failed to create default local queue: %s", err)
				}
				cq := *utiltestingapi.MakeClusterQueue("cluster-queue").
					AdmissionChecks("admission-check").
					Obj()
				if err := cqCache.AddClusterQueue(ctx, &cq); err != nil {
					t.Fatalf("Inserting clusterQueue %s in cache: %v", cq.Name, err)
				}
				ac := utiltestingapi.MakeAdmissionCheck("admission-check").
					ControllerName(kueue.MultiKueueControllerName).
					Active(metav1.ConditionTrue).
					Obj()
				cqCache.AddOrUpdateAdmissionCheck(log, ac)
				if err := queueManager.AddClusterQueue(ctx, &cq); err != nil {
					t.Fatalf("Inserting clusterQueue %s in manager: %v", cq.Name, err)
				}
			}

			mockctrl := gomock.NewController(t)

			type mockJob struct {
				*batchv1.Job
				*mocks.MockGenericJob
				*mocks.MockJobWithManagedBy
			}

			mj := &mockJob{
				Job:                  tc.job,
				MockGenericJob:       mocks.NewMockGenericJob(mockctrl),
				MockJobWithManagedBy: mocks.NewMockJobWithManagedBy(mockctrl),
			}

			mj.MockGenericJob.EXPECT().Object().Return(tc.job).AnyTimes()
			mj.MockGenericJob.EXPECT().IsSuspended().Return(ptr.Deref(tc.job.Spec.Suspend, false)).AnyTimes()
			mj.MockGenericJob.EXPECT().Suspend().Do(func() {
				tc.job.Spec.Suspend = ptr.To(true)
			}).AnyTimes()

			mj.MockJobWithManagedBy.EXPECT().ManagedBy().Return(tc.job.Spec.ManagedBy).AnyTimes()
			mj.MockJobWithManagedBy.EXPECT().SetManagedBy(gomock.Any()).Do(func(manageBy *string) {
				tc.job.Spec.ManagedBy = manageBy
			}).AnyTimes()
			mj.MockJobWithManagedBy.EXPECT().CanDefaultManagedBy().
				Return(features.Enabled(features.MultiKueue) && (tc.job.Spec.ManagedBy == nil || *tc.job.Spec.ManagedBy == batchv1.JobControllerName)).
				AnyTimes()

			w := &jobframework.BaseWebhook[*mockJob]{
				ManageJobsWithoutQueueName: tc.manageJobsWithoutQueueName,
				FromObject: func(object *mockJob) jobframework.GenericJob {
					return object
				},
				Queues: queueManager,
				Cache:  cqCache,
			}
			if err := w.Default(ctx, mj); err != nil {
				t.Errorf("set defaults by base webhook")
			}
			if diff := cmp.Diff(tc.want, tc.job); len(diff) != 0 {
				t.Errorf("Default() mismatch (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestValidateOnCreate(t *testing.T) {
	testcases := []struct {
		name string
		job  *batchv1.Job
		// JobWithCustomValidation return values.
		customValidationFailure field.ErrorList
		customValidationError   error

		wantError   error
		wantWarning admission.Warnings // Note: ValidateCreate always returns nil for admission.Warning.
	}{
		{
			name: "valid request",
			job:  utiljob.MakeJob("job", metav1.NamespaceDefault).Queue("queue").Obj(),
		},
		{
			name: "invalid request",
			job:  utiljob.MakeJob("job", metav1.NamespaceDefault).Label(constants.MaxExecTimeSecondsLabel, "0").Obj(),
			customValidationFailure: field.ErrorList{
				field.Invalid(field.NewPath("metadata.annotations"), field.OmitValueType{}, "custom validation test error"),
			},
			wantError: field.ErrorList{
				field.Invalid(field.NewPath("metadata.labels["+constants.MaxExecTimeSecondsLabel+"]"), 0, "should be greater than 0"),
				field.Invalid(field.NewPath("metadata.annotations"), field.OmitValueType{}, "custom validation test error"),
			}.ToAggregate(),
		},
		{
			name:                  "invalid request custom validation error",
			job:                   utiljob.MakeJob("job", metav1.NamespaceDefault).Label(constants.MaxExecTimeSecondsLabel, "0").Obj(),
			customValidationError: field.InternalError(nil, errors.New("test-custom-validation-error")),
			// Important: When a Job implements JobWithCustomValidation and the custom validation returns an
			// error (as opposed to a validation failure, which is a different error type),
			// all previous validation errors are ignored, and only the custom validation error is returned.
			//
			// Note: In this test, we intentionally "piggyback" on the field.Error type to avoid mixing
			// different error types. This simplifies the assertion logic.
			wantError: field.InternalError(nil, errors.New("test-custom-validation-error")),
		},
	}

	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			mockctrl := gomock.NewController(t)

			type mockJob struct {
				// A dummy place-holder to make it "runtime.Object".
				*batchv1.Job
				*mocks.MockGenericJob
				*mocks.MockJobWithCustomValidation
			}

			job := &mockJob{
				MockGenericJob:              mocks.NewMockGenericJob(mockctrl),
				MockJobWithCustomValidation: mocks.NewMockJobWithCustomValidation(mockctrl),
			}
			job.MockGenericJob.EXPECT().Object().Return(tc.job).AnyTimes()
			job.MockJobWithCustomValidation.EXPECT().ValidateOnCreate(gomock.Any()).Return(tc.customValidationFailure, tc.customValidationError).AnyTimes()

			w := &jobframework.BaseWebhook[*mockJob]{
				FromObject: func(object *mockJob) jobframework.GenericJob {
					return object
				},
			}

			ctx, _ := utiltesting.ContextWithLog(t)

			gotWarn, gotErr := w.ValidateCreate(ctx, job)
			if diff := cmp.Diff(tc.wantError, gotErr); diff != "" {
				t.Errorf("validate create err mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(tc.wantWarning, gotWarn); diff != "" {
				t.Errorf("validate create warn mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestValidateOnUpdate(t *testing.T) {
	type args struct {
		oldObj *batchv1.Job
		newObj *batchv1.Job

		// JobWithCustomValidation return values applicable to newObj only.
		customValidationFailure field.ErrorList
		customValidationError   error
	}
	testcases := []struct {
		name        string
		args        args
		wantWarning admission.Warnings // Note: ValidateUpdate always returns nil for admission.Warning.
		wantError   error
	}{
		{
			name: "valid request",
			args: args{
				oldObj: utiljob.MakeJob("job", metav1.NamespaceDefault).Queue("queue").Obj(),
				newObj: utiljob.MakeJob("job", metav1.NamespaceDefault).Queue("queue").Obj(),
			},
		},
		{
			name: "invalid request",
			args: args{
				oldObj: utiljob.MakeJob("job", metav1.NamespaceDefault).Queue("queue").Obj(),
				newObj: utiljob.MakeJob("job", metav1.NamespaceDefault).Suspend(false).Queue("changed").Obj(),
				customValidationFailure: field.ErrorList{
					field.Invalid(field.NewPath("metadata.annotations"), field.OmitValueType{}, "custom validation test error"),
				},
			},
			wantError: field.ErrorList{
				field.Invalid(field.NewPath("metadata.labels[kueue.x-k8s.io/queue-name]"), kueue.LocalQueueName("changed"), "field is immutable"),
				field.Invalid(field.NewPath("metadata.annotations"), field.OmitValueType{}, "custom validation test error"),
			}.ToAggregate(),
		},
		{
			name: "invalid request custom validation error",
			args: args{
				oldObj:                utiljob.MakeJob("job", metav1.NamespaceDefault).Queue("queue").Obj(),
				newObj:                utiljob.MakeJob("job", metav1.NamespaceDefault).Suspend(false).Queue("changed").Obj(),
				customValidationError: field.InternalError(nil, errors.New("test-custom-validation-error")),
			},
			wantError: field.InternalError(nil, errors.New("test-custom-validation-error")),
		},
	}

	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			mockctrl := gomock.NewController(t)

			type mockJob struct {
				// A dummy place-holder to make it "runtime.Object".
				*batchv1.Job
				*mocks.MockGenericJob
				*mocks.MockJobWithCustomValidation
			}

			newMockJob := func(job *batchv1.Job, customValidationFailure field.ErrorList, customValidationError error) *mockJob {
				mj := &mockJob{
					MockGenericJob:              mocks.NewMockGenericJob(mockctrl),
					MockJobWithCustomValidation: mocks.NewMockJobWithCustomValidation(mockctrl),
				}
				mj.MockGenericJob.EXPECT().Object().Return(job).AnyTimes()
				mj.MockGenericJob.EXPECT().IsSuspended().Return(ptr.Deref(job.Spec.Suspend, false)).AnyTimes()
				mj.MockJobWithCustomValidation.EXPECT().ValidateOnUpdate(gomock.Any(), gomock.Any()).Return(customValidationFailure, customValidationError).AnyTimes()
				return mj
			}

			w := &jobframework.BaseWebhook[*mockJob]{
				FromObject: func(object *mockJob) jobframework.GenericJob {
					return object
				},
			}

			ctx, _ := utiltesting.ContextWithLog(t)

			gotWarn, gotErr := w.ValidateUpdate(ctx,
				newMockJob(tc.args.oldObj, nil, nil),
				newMockJob(tc.args.newObj, tc.args.customValidationFailure, tc.args.customValidationError))
			if diff := cmp.Diff(tc.wantError, gotErr); diff != "" {
				t.Errorf("validate create err mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(tc.wantWarning, gotWarn); diff != "" {
				t.Errorf("validate create warn mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
