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
	"go.uber.org/mock/gomock"
	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	mocks "sigs.k8s.io/kueue/internal/mocks/controller/jobframework"
	qcache "sigs.k8s.io/kueue/pkg/cache/queue"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/features"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiljob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
)

func TestBaseWebhookDefault(t *testing.T) {
	testcases := map[string]struct {
		manageJobsWithoutQueueName bool
		localQueueDefaulting       bool
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
		"LocalQueueDefaulting enabled, default lq is created, job doesn't have queue label": {
			localQueueDefaulting: true,
			defaultLqExist:       true,
			job:                  utiljob.MakeJob("job", metav1.NamespaceDefault).Obj(),
			want: utiljob.MakeJob("job", metav1.NamespaceDefault).
				Label(constants.QueueLabel, "default").
				Obj(),
		},
		"LocalQueueDefaulting enabled, default lq is created, job has queue label": {
			localQueueDefaulting: true,
			defaultLqExist:       true,
			job:                  utiljob.MakeJob("job", metav1.NamespaceDefault).Queue("queue").Obj(),
			want:                 utiljob.MakeJob("job", metav1.NamespaceDefault).Queue("queue").Obj(),
		},
		"LocalQueueDefaulting enabled, default lq isn't created, job doesn't have queue label": {
			localQueueDefaulting: true,
			defaultLqExist:       false,
			job:                  utiljob.MakeJob("job", metav1.NamespaceDefault).Obj(),
			want:                 utiljob.MakeJob("job", metav1.NamespaceDefault).Obj(),
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
			features.SetFeatureGateDuringTest(t, features.LocalQueueDefaulting, tc.localQueueDefaulting)
			features.SetFeatureGateDuringTest(t, features.MultiKueue, tc.enableMultiKueue)
			clientBuilder := utiltesting.NewClientBuilder().
				WithObjects(
					utiltesting.MakeNamespace(metav1.NamespaceDefault),
				)
			cl := clientBuilder.Build()
			cqCache := schdcache.New(cl)
			queueManager := qcache.NewManager(cl, cqCache)
			if tc.defaultLqExist {
				if err := queueManager.AddLocalQueue(ctx, utiltesting.MakeLocalQueue("default", metav1.NamespaceDefault).
					ClusterQueue("cluster-queue").Obj()); err != nil {
					t.Fatalf("failed to create default local queue: %s", err)
				}
			}
			if tc.enableMultiKueue {
				if err := queueManager.AddLocalQueue(ctx, utiltesting.MakeLocalQueue("multikueue", metav1.NamespaceDefault).
					ClusterQueue("cluster-queue").Obj()); err != nil {
					t.Fatalf("failed to create default local queue: %s", err)
				}
				cq := *utiltesting.MakeClusterQueue("cluster-queue").
					AdmissionChecks("admission-check").
					Obj()
				if err := cqCache.AddClusterQueue(ctx, &cq); err != nil {
					t.Fatalf("Inserting clusterQueue %s in cache: %v", cq.Name, err)
				}
				ac := utiltesting.MakeAdmissionCheck("admission-check").
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

			w := &jobframework.BaseWebhook{
				ManageJobsWithoutQueueName: tc.manageJobsWithoutQueueName,
				FromObject: func(object runtime.Object) jobframework.GenericJob {
					return object.(*mockJob)
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
		name             string
		job              *batchv1.Job
		validateOnCreate field.ErrorList
		wantErr          error
		wantWarn         admission.Warnings
	}{
		{
			name: "valid request",
			job:  utiljob.MakeJob("job", metav1.NamespaceDefault).Queue("queue").Obj(),
		},
		{
			name: "invalid request with validate on create",
			job:  utiljob.MakeJob("job", metav1.NamespaceDefault).Queue("queue").Obj(),
			validateOnCreate: field.ErrorList{
				field.Invalid(
					field.NewPath("metadata.annotations"),
					field.OmitValueType{},
					`invalid annotation`,
				),
			},
			wantErr: field.ErrorList{
				field.Invalid(
					field.NewPath("metadata.annotations"),
					field.OmitValueType{},
					`invalid annotation`,
				),
			}.ToAggregate(),
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
				Job:                         tc.job,
				MockGenericJob:              mocks.NewMockGenericJob(mockctrl),
				MockJobWithCustomValidation: mocks.NewMockJobWithCustomValidation(mockctrl),
			}

			job.MockGenericJob.EXPECT().Object().Return(tc.job).AnyTimes()
			job.MockJobWithCustomValidation.EXPECT().ValidateOnCreate().Return(tc.validateOnCreate, nil).AnyTimes()

			w := &jobframework.BaseWebhook{
				FromObject: func(object runtime.Object) jobframework.GenericJob {
					return object.(*mockJob)
				},
			}
			ctx, _ := utiltesting.ContextWithLog(t)
			gotWarn, gotErr := w.ValidateCreate(ctx, job)
			if diff := cmp.Diff(tc.wantErr, gotErr); diff != "" {
				t.Errorf("validate create err mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(tc.wantWarn, gotWarn); diff != "" {
				t.Errorf("validate create warn mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestValidateOnUpdate(t *testing.T) {
	testcases := []struct {
		name             string
		oldJob           *batchv1.Job
		job              *batchv1.Job
		validateOnUpdate field.ErrorList
		wantErr          error
		wantWarn         admission.Warnings
	}{
		{
			name:   "valid request",
			oldJob: utiljob.MakeJob("job", metav1.NamespaceDefault).Queue("queue").Obj(),
			job:    utiljob.MakeJob("job", metav1.NamespaceDefault).Queue("queue").Obj(),
		},
		{
			name:   "invalid request with validate on update",
			oldJob: utiljob.MakeJob("job", metav1.NamespaceDefault).Queue("queue").Obj(),
			job:    utiljob.MakeJob("job", metav1.NamespaceDefault).Queue("queue").Obj(),
			validateOnUpdate: field.ErrorList{
				field.Invalid(
					field.NewPath("metadata.annotations"),
					field.OmitValueType{},
					`invalid annotation`,
				),
			},
			wantErr: field.ErrorList{
				field.Invalid(
					field.NewPath("metadata.annotations"),
					field.OmitValueType{},
					`invalid annotation`,
				),
			}.ToAggregate(),
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

			newMockJob := func(job *batchv1.Job, validationErrList field.ErrorList) *mockJob {
				mj := &mockJob{
					Job:                         job,
					MockGenericJob:              mocks.NewMockGenericJob(mockctrl),
					MockJobWithCustomValidation: mocks.NewMockJobWithCustomValidation(mockctrl),
				}
				mj.MockGenericJob.EXPECT().Object().Return(job).AnyTimes()
				mj.MockGenericJob.EXPECT().IsSuspended().Return(ptr.Deref(job.Spec.Suspend, false)).AnyTimes()
				mj.MockJobWithCustomValidation.EXPECT().ValidateOnUpdate(gomock.Any()).Return(validationErrList, nil).AnyTimes()
				return mj
			}

			w := &jobframework.BaseWebhook{
				FromObject: func(object runtime.Object) jobframework.GenericJob {
					return object.(*mockJob)
				},
			}
			ctx, _ := utiltesting.ContextWithLog(t)
			gotWarn, gotErr := w.ValidateUpdate(
				ctx,
				newMockJob(tc.oldJob, nil),
				newMockJob(tc.job, tc.validateOnUpdate),
			)
			if diff := cmp.Diff(tc.wantErr, gotErr); diff != "" {
				t.Errorf("validate create err mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(tc.wantWarn, gotWarn); diff != "" {
				t.Errorf("validate create warn mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
