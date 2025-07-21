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
	"testing"

	"github.com/google/go-cmp/cmp"
	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/podset"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/pkg/workloadslicing"
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
					metav1.SetMetaDataAnnotation(&wl.ObjectMeta, workloadslicing.WorkloadSliceReplacementFor, string(workload.Key(testWorkload(t, testJob(1)))))
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

func TestWorkloadSliceEnabled(t *testing.T) {
	type args struct {
		job GenericJob
	}
	tests := map[string]struct {
		featureEnabled bool
		args           args
		want           bool
	}{
		"FeatureIsNotEnabled": {
			args: args{
				job: &testGenericJob{
					Job: &batchv1.Job{},
				},
			},
		},
		"JobIsNil": {
			featureEnabled: true,
			args:           args{},
		},
		"NotOptIn": {
			featureEnabled: true,
			args: args{
				job: &testGenericJob{
					Job: &batchv1.Job{},
				},
			},
		},
		"OptIn": {
			featureEnabled: true,
			args: args{
				job: &testGenericJob{
					Job: &batchv1.Job{
						ObjectMeta: metav1.ObjectMeta{
							Annotations: map[string]string{workloadslicing.EnabledAnnotationKey: workloadslicing.EnabledAnnotationValue},
						},
					},
				},
			},
			want: true,
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			if tt.featureEnabled {
				if err := features.SetEnable(features.ElasticJobsViaWorkloadSlices, true); err != nil {
					t.Errorf("workloadSliceEnabled() unexpected error enbabling features: %v", err)
				}
			}
			if got := workloadSliceEnabled(tt.args.job); got != tt.want {
				t.Errorf("workloadSliceEnabled() = %v, want %v", got, tt.want)
			}
		})
	}
}
