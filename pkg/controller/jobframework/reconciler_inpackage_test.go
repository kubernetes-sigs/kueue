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
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/controller/jobframework/mock"
	"sigs.k8s.io/kueue/pkg/features"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingjobsjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/pkg/workloadslicing"
)

var testGVK = batchv1.SchemeGroupVersion.WithKind("Job")

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
		WithIndex(&kueue.Workload{}, jobframework.OwnerReferenceIndexKey(testGVK), func(object client.Object) []string {
			wl, ok := object.(*kueue.Workload)
			if !ok || len(wl.OwnerReferences) == 0 {
				return nil
			}
			owners := make([]string, 0, len(wl.OwnerReferences))
			for i := range wl.OwnerReferences {
				owner := &wl.OwnerReferences[i]
				if owner.Kind == testGVK.Kind && owner.APIVersion == testGVK.GroupVersion().String() {
					owners = append(owners, owner.Name)
				}
			}
			return owners
		})
}

// testWorkload helper creates a workload instance for the provided generic job.
func testWorkload(t *testing.T, jobObject *batchv1.Job) *kueue.Workload {
	t.Helper()
	wl := jobframework.NewWorkload(jobframework.GetWorkloadNameForOwnerWithGVKAndGeneration(jobObject.GetName(), jobObject.GetUID(), testGVK, jobObject.GetGeneration()), jobObject, nil, nil)
	if err := ctrl.SetControllerReference(jobObject, wl, testScheme()); err != nil {
		t.Errorf("unexpected error assigning workload controller reference: %v", err)
		return nil
	}
	return wl
}

func Test_prepareWorkloadSlice(t *testing.T) {
	type args struct {
		clnt client.Client
		job  *batchv1.Job
		wl   *kueue.Workload
	}
	type want struct {
		err      bool
		workload *kueue.Workload
	}
	tests := map[string]struct {
		args args
		want want
	}{
		"FailureRetrievingWorkloads": {
			args: args{
				// Intentionally using a scheme that doesn’t register Kueue types to trigger a failure.
				clnt: fake.NewClientBuilder().Build(),
				job: utiltestingjobsjob.MakeJob("job", metav1.NamespaceDefault).
					SetAnnotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
					Obj(),
			},
			want: want{err: true},
		},
		"NoExistingWorkloads": {
			args: args{
				clnt: testClient().Build(),
				job: utiltestingjobsjob.MakeJob("job", metav1.NamespaceDefault).
					SetAnnotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
					Generation(1).
					Obj(),
				wl: testWorkload(t, utiltestingjobsjob.MakeJob("job", metav1.NamespaceDefault).
					SetAnnotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
					Generation(1).
					Obj()),
			},
			want: want{
				workload: testWorkload(t, utiltestingjobsjob.MakeJob("job", metav1.NamespaceDefault).
					SetAnnotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
					Generation(1).
					Obj()),
			},
		},
		"OneExistingWorkload": {
			args: args{
				clnt: testClient().WithLists(&kueue.WorkloadList{
					Items: []kueue.Workload{
						*testWorkload(t, utiltestingjobsjob.MakeJob("job", metav1.NamespaceDefault).
							SetAnnotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
							Generation(1).
							Obj()),
					},
				}).Build(),
				job: utiltestingjobsjob.MakeJob("job", metav1.NamespaceDefault).
					SetAnnotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
					Generation(2).
					Obj(),
				wl: testWorkload(t, utiltestingjobsjob.MakeJob("job", metav1.NamespaceDefault).
					SetAnnotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
					Generation(2).
					Obj()),
			},
			want: want{
				workload: func() *kueue.Workload {
					wl := testWorkload(t, utiltestingjobsjob.MakeJob("job", metav1.NamespaceDefault).
						SetAnnotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
						Generation(2).
						Obj())
					metav1.SetMetaDataAnnotation(&wl.ObjectMeta, workloadslicing.WorkloadSliceReplacementFor, string(workload.Key(testWorkload(t, utiltestingjobsjob.MakeJob("job", metav1.NamespaceDefault).
						SetAnnotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
						Generation(1).
						Obj()))))
					return wl
				}(),
			},
		},
		"MoreThanOneExistingWorkload": {
			args: args{
				clnt: testClient().WithLists(&kueue.WorkloadList{
					Items: []kueue.Workload{
						*testWorkload(t, utiltestingjobsjob.MakeJob("job", metav1.NamespaceDefault).
							SetAnnotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
							Generation(1).
							Obj()),
						*testWorkload(t, utiltestingjobsjob.MakeJob("job", metav1.NamespaceDefault).
							SetAnnotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
							Generation(2).
							Obj()),
					},
				}).Build(),
				job: utiltestingjobsjob.MakeJob("job", metav1.NamespaceDefault).
					SetAnnotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
					Generation(3).
					Obj(),
				wl: testWorkload(t, utiltestingjobsjob.MakeJob("job", metav1.NamespaceDefault).
					SetAnnotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
					Generation(3).
					Obj()),
			},
			want: want{
				err: true,
				workload: testWorkload(t, utiltestingjobsjob.MakeJob("job", metav1.NamespaceDefault).
					SetAnnotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
					Generation(3).
					Obj()),
			},
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)
			ctrl := gomock.NewController(t)

			gj := mock.NewMockGenericJob(ctrl)
			gj.EXPECT().Object().Return(tt.args.job).AnyTimes()
			gj.EXPECT().GVK().Return(testGVK).AnyTimes()

			err := jobframework.PrepareWorkloadSlice(ctx, tt.args.clnt, gj, tt.args.wl)
			if (err != nil) != tt.want.err {
				t.Errorf("PrepareWorkloadSlice() error = %v, wantErr %v", err, tt.want.err)
				return
			}
			if diff := cmp.Diff(tt.args.wl, tt.want.workload); diff != "" {
				t.Errorf("PrepareWorkloadSlice() workload(-),want(+): %s", diff)
			}
		})
	}
}

func TestWorkloadSliceEnabled(t *testing.T) {
	tests := map[string]struct {
		featureEnabled bool
		job            *batchv1.Job
		want           bool
	}{
		"FeatureIsNotEnabled": {
			job: &batchv1.Job{},
		},
		"JobIsNil": {
			featureEnabled: true,
		},
		"NotOptIn": {
			featureEnabled: true,
			job:            &batchv1.Job{},
		},
		"OptIn": {
			featureEnabled: true,
			job: &batchv1.Job{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{workloadslicing.EnabledAnnotationKey: workloadslicing.EnabledAnnotationValue},
				},
			},
			want: true,
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			ctrl := gomock.NewController(t)

			var gj jobframework.GenericJob

			if tt.job != nil {
				mgj := mock.NewMockGenericJob(ctrl)
				mgj.EXPECT().Object().Return(tt.job).AnyTimes()
				gj = mgj
			}

			if tt.featureEnabled {
				if err := features.SetEnable(features.ElasticJobsViaWorkloadSlices, true); err != nil {
					t.Errorf("WorkloadSliceEnabled() unexpected error enbabling features: %v", err)
				}
			}
			if got := jobframework.WorkloadSliceEnabled(gj); got != tt.want {
				t.Errorf("WorkloadSliceEnabled() = %v, want %v", got, tt.want)
			}
		})
	}
}
