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

package jobframework

import (
	"context"
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"
	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/tools/record"
	"k8s.io/utils/clock"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/podset"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/pkg/workload/workloadslicing"
)

var testJobGVK = batchv1.SchemeGroupVersion.WithKind("Job")

type mockJob struct {
	object       *batchv1.Job
	suspended    bool
	podSets      []kueue.PodSet
	podSetsError error
	gvk          schema.GroupVersionKind
}

func (j *mockJob) Object() client.Object {
	return j.object
}

func (j *mockJob) IsSuspended() bool {
	return j.suspended
}

func (j *mockJob) Suspend() {}

func (j *mockJob) RunWithPodSetsInfo(_ []podset.PodSetInfo) error {
	panic("implement me")
}

func (j *mockJob) RestorePodSetsInfo(_ []podset.PodSetInfo) bool {
	panic("implement me")
}

func (j *mockJob) Finished() (message string, success, finished bool) {
	panic("implement me")
}

func (j *mockJob) PodSets() ([]kueue.PodSet, error) {
	return j.podSets, j.podSetsError
}

func (j *mockJob) IsActive() bool {
	panic("implement me")
}

func (j *mockJob) PodsReady() bool {
	panic("implement me")
}

func (j *mockJob) GVK() schema.GroupVersionKind { return j.gvk }

// testJobObject helper returns a workload-slice-enabled v1.Job instance.
func testJobObject(optIn bool) *batchv1.Job {
	job := &batchv1.Job{}
	if optIn {
		metav1.SetMetaDataAnnotation(&job.ObjectMeta, workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue)
	}
	return job
}

// testScheme helper returns runtime.Scheme with registered types needed for unit-tests.
func testScheme() *runtime.Scheme {
	sch := runtime.NewScheme()
	_ = batchv1.AddToScheme(sch)
	_ = kueue.AddToScheme(sch)
	return sch
}

// testClient helper returns a fake.ClientBuilder initialized with expected Scheme and Index.
func testClient() *fake.ClientBuilder {
	return fake.NewClientBuilder().
		WithScheme(testScheme()).
		WithIndex(&kueue.Workload{}, OwnerReferenceIndexKey(testJobGVK), func(object client.Object) []string {
			wl, ok := object.(*kueue.Workload)
			if !ok || len(wl.OwnerReferences) == 0 {
				return nil
			}
			owners := make([]string, 0, len(wl.OwnerReferences))
			for i := range wl.OwnerReferences {
				owner := &wl.OwnerReferences[i]
				if owner.Kind == testJobGVK.Kind && owner.APIVersion == testJobGVK.GroupVersion().String() {
					owners = append(owners, owner.Name)
				}
			}
			return owners
		})
}

// testWorkload helper creates a workload instance for the provided generic job.
func testWorkload(t *testing.T, job GenericJob) *kueue.Workload {
	t.Helper()
	jobObject := job.Object()
	wl := NewWorkload(GetWorkloadNameForOwnerWithGVKAndGeneration(jobObject.GetName(), jobObject.GetUID(), job.GVK(), jobObject.GetGeneration()), jobObject, nil, nil)
	if err := ctrl.SetControllerReference(jobObject, wl, testScheme()); err != nil {
		t.Errorf("unexpected error assigning workload controller reference: %v", err)
		return nil
	}
	return wl
}

func TestJobReconciler_ensureWorkload(t *testing.T) {
	type fields struct {
		client client.Client
		record record.EventRecorder

		manageJobsWithoutQueueName   bool
		managedJobsNamespaceSelector labels.Selector
		waitForPodsReady             bool
		labelKeysToCopy              []string
		clock                        clock.Clock
		workloadRetentionPolicy      WorkloadRetentionPolicy
	}
	type args struct {
		ctx context.Context
		job GenericJob
	}
	type want struct {
		err      bool
		workload *kueue.Workload
	}
	tests := map[string]struct {
		workloadSliceEnabled bool
		fields               fields
		args                 args
		want                 want
	}{
		"WorkloadSliceNotEnabled_Failure": {
			args: args{
				ctx: t.Context(),
				job: &mockJob{object: testJobObject(false)},
			},
			fields: fields{
				client: fake.NewClientBuilder().Build(),
			},
			want: want{err: true},
		},
		"WorkloadSliceNotEnabled_Success": {
			args: args{
				ctx: t.Context(),
				job: &mockJob{
					object:    testJobObject(false),
					gvk:       testJobGVK,
					suspended: true,
				},
			},
			fields: fields{
				client: testClient().Build(),
			},
		},
		"WorkloadSliceEnabled_NoPreviousWorkload": {
			workloadSliceEnabled: true,
			args: args{
				ctx: t.Context(),
				job: &mockJob{
					object: testJobObject(true),
					gvk:    testJobGVK,
				},
			},
			fields: fields{
				client: testClient().Build(),
			},
		},
		"WorkloadSliceEnabled_FailureRetrievingJobPodSets": {
			workloadSliceEnabled: true,
			args: args{
				ctx: t.Context(),
				job: &mockJob{
					object:       testJobObject(true),
					gvk:          testJobGVK,
					podSetsError: errors.New("test-get-podsets-error"),
				},
			},
			want: want{
				err: true,
			},
		},
		"WorkloadSliceEnabled_FailureRetrievingWorkloads": {
			workloadSliceEnabled: true,
			args: args{
				ctx: t.Context(),
				job: &mockJob{
					object: testJobObject(true),
					gvk:    testJobGVK,
				},
			},
			fields: fields{
				client: testClient().
					WithInterceptorFuncs(interceptor.Funcs{
						List: func(ctx context.Context, client client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
							return errors.New("test-list-workload-error")
						},
					}).
					Build(),
			},
			want: want{
				err: true,
			},
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			r := &JobReconciler{
				client:                       tt.fields.client,
				record:                       tt.fields.record,
				manageJobsWithoutQueueName:   tt.fields.manageJobsWithoutQueueName,
				managedJobsNamespaceSelector: tt.fields.managedJobsNamespaceSelector,
				waitForPodsReady:             tt.fields.waitForPodsReady,
				labelKeysToCopy:              tt.fields.labelKeysToCopy,
				clock:                        tt.fields.clock,
				workloadRetentionPolicy:      tt.fields.workloadRetentionPolicy,
			}
			if err := features.SetEnable(features.DynamicallySizedJob, tt.workloadSliceEnabled); err != nil {
				t.Errorf("ensureWorkload() unexpected error enabling feature: %v", err)
			}

			got, err := r.ensureWorkload(tt.args.ctx, tt.args.job, tt.args.job.Object())
			if (err != nil) != tt.want.err {
				t.Errorf("ensureWorkload() error = %v, wantErr %v", err, tt.want.err)
				return
			}
			if diff := cmp.Diff(got, tt.want.workload); diff != "" {
				t.Errorf("ensureWorkload() got(-),want(+): %s", diff)
			}
		})
	}
}

func Test_prepareWorkloadSlice(t *testing.T) {
	type args struct {
		ctx  context.Context
		clnt client.Client
		job  GenericJob
		wl   *kueue.Workload
	}
	type want struct {
		err      bool
		workload *kueue.Workload
	}
	testJob := func(generation int64) GenericJob {
		job := testJobObject(true)
		job.Generation = generation
		return &mockJob{
			object: job,
			gvk:    testJobGVK,
		}
	}
	tests := map[string]struct {
		args args
		want want
	}{
		"FailureRetrievingWorkloads": {
			args: args{
				ctx: t.Context(),
				// Intentionally using a scheme that doesn’t register Kueue types to trigger a failure.
				clnt: fake.NewClientBuilder().Build(),
				job: &mockJob{
					object: testJobObject(true),
					gvk:    testJobGVK,
				},
			},
			want: want{err: true},
		},
		"NoExistingWorkloads": {
			args: args{
				ctx:  t.Context(),
				clnt: testClient().Build(),
				job:  testJob(1),
				wl:   testWorkload(t, testJob(1)),
			},
			want: want{
				workload: testWorkload(t, testJob(1)),
			},
		},
		"EdgeCase_OneExistingWorkload_NameCollision": {
			// Simulates a scenario where a new workload collides with an existing one by name.
			args: args{
				ctx: t.Context(),
				clnt: testClient().WithLists(&kueue.WorkloadList{
					Items: []kueue.Workload{
						*testWorkload(t, testJob(1)),
					},
				}).Build(),
				job: testJob(1),
				wl:  testWorkload(t, testJob(1)),
			},
			want: want{
				err:      true,
				workload: testWorkload(t, testJob(1)),
			},
		},
		"OneExistingWorkload": {
			args: args{
				ctx: t.Context(),
				clnt: testClient().WithLists(&kueue.WorkloadList{
					Items: []kueue.Workload{
						*testWorkload(t, testJob(1)),
					},
				}).Build(),
				job: testJob(2),
				wl:  testWorkload(t, testJob(2)),
			},
			want: want{
				workload: func() *kueue.Workload {
					wl := testWorkload(t, testJob(2))
					metav1.SetMetaDataAnnotation(&wl.ObjectMeta, workloadslicing.WorkloadPreemptibleSliceNameKey, string(workload.Key(testWorkload(t, testJob(1)))))
					return wl
				}(),
			},
		},
		"MoreThanOneExistingWorkload": {
			args: args{
				ctx: t.Context(),
				clnt: testClient().WithLists(&kueue.WorkloadList{
					Items: []kueue.Workload{
						*testWorkload(t, testJob(1)),
						*testWorkload(t, testJob(2)),
					},
				}).Build(),
				job: testJob(3),
				wl:  testWorkload(t, testJob(3)),
			},
			want: want{
				err:      true,
				workload: testWorkload(t, testJob(3)),
			},
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			err := prepareWorkloadSlice(tt.args.ctx, tt.args.clnt, tt.args.job, tt.args.wl)
			if (err != nil) != tt.want.err {
				t.Errorf("prepareWorkloadSlice() error = %v, wantErr %v", err, tt.want.err)
				return
			}
			if diff := cmp.Diff(tt.args.wl, tt.want.workload); diff != "" {
				t.Errorf("prepareWorkloadSlice() workload(-),want(+): %s", diff)
			}
		})
	}
}
