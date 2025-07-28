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
	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/util/validation/field"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/cache"
	"sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/podset"
	"sigs.k8s.io/kueue/pkg/queue"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiljob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
)

type testGenericJob struct {
	*batchv1.Job

	validateOnCreate func() (field.ErrorList, error)
	validateOnUpdate func(jobframework.GenericJob) (field.ErrorList, error)
}

var _ jobframework.GenericJob = (*testGenericJob)(nil)
var _ jobframework.JobWithCustomValidation = (*testGenericJob)(nil)
var _ jobframework.JobWithManagedBy = (*testGenericJob)(nil)

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

func (j *testGenericJob) CanDefaultManagedBy() bool {
	jobSpecManagedBy := j.Spec.ManagedBy
	return features.Enabled(features.MultiKueue) &&
		(jobSpecManagedBy == nil || *jobSpecManagedBy == batchv1.JobControllerName)
}

func (j *testGenericJob) ManagedBy() *string {
	return j.Spec.ManagedBy
}

func (j *testGenericJob) SetManagedBy(managedBy *string) {
	j.Spec.ManagedBy = managedBy
}

func (j *testGenericJob) GVK() schema.GroupVersionKind {
	panic("not implemented")
}

func (j *testGenericJob) ValidateOnCreate() (field.ErrorList, error) {
	if j.validateOnCreate != nil {
		return j.validateOnCreate()
	}
	return nil, nil
}

func (j *testGenericJob) ValidateOnUpdate(oldJob jobframework.GenericJob) (field.ErrorList, error) {
	if j.validateOnUpdate != nil {
		return j.validateOnUpdate(oldJob)
	}
	return nil, nil
}

func (j *testGenericJob) withValidateOnCreate(validateOnCreate func() (field.ErrorList, error)) *testGenericJob {
	j.validateOnCreate = validateOnCreate
	return j
}

func (j *testGenericJob) withValidateOnUpdate(validateOnUpdate func(jobframework.GenericJob) (field.ErrorList, error)) *testGenericJob {
	j.validateOnUpdate = validateOnUpdate
	return j
}

func (j *testGenericJob) fromObject(o runtime.Object) jobframework.GenericJob {
	if o == nil {
		return nil
	}
	j.Job = o.(*batchv1.Job)
	return j
}

func makeTestGenericJob() *testGenericJob {
	return &testGenericJob{}
}

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
			job: &batchv1.Job{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "job",
					Namespace: "default",
					Labels:    map[string]string{constants.QueueLabel: "queue"},
				},
			},
			want: &batchv1.Job{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "job",
					Namespace: "default",
					Labels:    map[string]string{constants.QueueLabel: "queue"},
				},
				Spec: batchv1.JobSpec{Suspend: ptr.To(true)},
			},
		},
		"update the suspend field 'manageJobsWithoutQueueName=true'": {
			manageJobsWithoutQueueName: true,
			job: &batchv1.Job{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "job",
					Namespace: "default",
					Labels:    map[string]string{constants.QueueLabel: "queue"},
				},
			},
			want: &batchv1.Job{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "job",
					Namespace: "default",
					Labels:    map[string]string{constants.QueueLabel: "queue"},
				},
				Spec: batchv1.JobSpec{Suspend: ptr.To(true)},
			},
		},
		"LocalQueueDefaulting enabled, default lq is created, job doesn't have queue label": {
			localQueueDefaulting: true,
			defaultLqExist:       true,
			job: utiljob.MakeJob("job", "default").
				Obj(),
			want: utiljob.MakeJob("job", "default").
				Label(constants.QueueLabel, "default").
				Obj(),
		},
		"LocalQueueDefaulting enabled, default lq is created, job has queue label": {
			localQueueDefaulting: true,
			defaultLqExist:       true,
			job: utiljob.MakeJob("job", "default").
				Queue("queue").
				Obj(),
			want: utiljob.MakeJob("job", "default").
				Queue("queue").
				Obj(),
		},
		"LocalQueueDefaulting enabled, default lq isn't created, job doesn't have queue label": {
			localQueueDefaulting: true,
			defaultLqExist:       false,
			job: utiljob.MakeJob("job", "default").
				Obj(),
			want: utiljob.MakeJob("job", "default").
				Obj(),
		},
		"ManagedByDefaulting, targeting multikueue local queue": {
			job: utiljob.MakeJob("job", "default").
				Queue("multikueue").
				Obj(),
			want: utiljob.MakeJob("job", "default").
				Queue("multikueue").
				ManagedBy(kueue.MultiKueueControllerName).
				Obj(),
			enableMultiKueue: true,
		},
		"ManagedByDefaulting, targeting multikueue local queue but already managaed by someone else": {
			job: utiljob.MakeJob("job", "default").
				Queue("multikueue").
				ManagedBy("someone-else").
				Obj(),
			want: utiljob.MakeJob("job", "default").
				Queue("multikueue").
				ManagedBy("someone-else").
				Obj(),
			enableMultiKueue: true,
		},
		"ManagedByDefaulting, targeting non-multikueue local queue": {
			job: utiljob.MakeJob("job", "default").
				Queue("queue").
				Obj(),
			want: utiljob.MakeJob("job", "default").
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
					utiltesting.MakeNamespace("default"),
				)
			cl := clientBuilder.Build()
			cqCache := cache.New(cl)
			queueManager := queue.NewManager(cl, cqCache)
			if tc.defaultLqExist {
				if err := queueManager.AddLocalQueue(ctx, utiltesting.MakeLocalQueue("default", "default").
					ClusterQueue("cluster-queue").Obj()); err != nil {
					t.Fatalf("failed to create default local queue: %s", err)
				}
			}
			if tc.enableMultiKueue {
				if err := queueManager.AddLocalQueue(ctx, utiltesting.MakeLocalQueue("multikueue", "default").
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

			w := &jobframework.BaseWebhook{
				ManageJobsWithoutQueueName: tc.manageJobsWithoutQueueName,
				FromObject:                 makeTestGenericJob().fromObject,
				Queues:                     queueManager,
				Cache:                      cqCache,
			}
			if err := w.Default(t.Context(), tc.job); err != nil {
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
		validateOnCreate func() (field.ErrorList, error)
		wantErr          error
		wantWarn         admission.Warnings
	}{
		{
			name: "valid request",
			job: &batchv1.Job{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "job",
					Namespace: "default",
					Labels:    map[string]string{constants.QueueLabel: "queue"},
				},
			},
		},
		{
			name: "invalid request with validate on create",
			job: &batchv1.Job{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "job",
					Namespace: "default",
					Labels:    map[string]string{constants.QueueLabel: "queue"},
				},
			},
			validateOnCreate: func() (field.ErrorList, error) {
				return field.ErrorList{
					field.Invalid(
						field.NewPath("metadata.annotations"),
						field.OmitValueType{},
						`invalid annotation`,
					),
				}, nil
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
			w := &jobframework.BaseWebhook{
				FromObject: makeTestGenericJob().withValidateOnCreate(tc.validateOnCreate).fromObject,
			}
			gotWarn, gotErr := w.ValidateCreate(t.Context(), tc.job)
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
		validateOnUpdate func(jobframework.GenericJob) (field.ErrorList, error)
		wantErr          error
		wantWarn         admission.Warnings
	}{
		{
			name: "valid request",
			oldJob: &batchv1.Job{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "job",
					Namespace: "default",
					Labels:    map[string]string{constants.QueueLabel: "queue"},
				},
			},
			job: &batchv1.Job{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "job",
					Namespace: "default",
					Labels:    map[string]string{constants.QueueLabel: "queue"},
				},
			},
		},
		{
			name: "invalid request with validate on update",
			oldJob: &batchv1.Job{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "job",
					Namespace: "default",
					Labels:    map[string]string{constants.QueueLabel: "queue"},
				},
			},
			job: &batchv1.Job{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "job",
					Namespace: "default",
					Labels:    map[string]string{constants.QueueLabel: "queue"},
				},
			},
			validateOnUpdate: func(jobframework.GenericJob) (field.ErrorList, error) {
				return field.ErrorList{
					field.Invalid(
						field.NewPath("metadata.annotations"),
						field.OmitValueType{},
						`invalid annotation`,
					),
				}, nil
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
			w := &jobframework.BaseWebhook{
				FromObject: makeTestGenericJob().withValidateOnUpdate(tc.validateOnUpdate).fromObject,
			}
			gotWarn, gotErr := w.ValidateUpdate(t.Context(), tc.oldJob, tc.job)
			if diff := cmp.Diff(tc.wantErr, gotErr); diff != "" {
				t.Errorf("validate create err mismatch (-want +got):\n%s", diff)
			}
			if diff := cmp.Diff(tc.wantWarn, gotWarn); diff != "" {
				t.Errorf("validate create warn mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
