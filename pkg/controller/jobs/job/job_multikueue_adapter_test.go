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

package job

import (
	"context"
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/kubernetes/scheme"
	"k8s.io/component-base/featuregate"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/util/slices"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
	"sigs.k8s.io/kueue/pkg/workloadslicing"
)

const (
	TestNamespace = "ns"
)

func TestMultiKueueAdapter(t *testing.T) {
	objCheckOpts := cmp.Options{
		cmpopts.IgnoreFields(metav1.ObjectMeta{}, "ResourceVersion"),
		cmpopts.EquateEmpty(),
	}

	baseJobBuilder := utiltestingjob.MakeJob("job1", TestNamespace).Suspend(false)
	baseJobManagedByKueueBuilder := baseJobBuilder.Clone().ManagedBy(kueue.MultiKueueControllerName)

	cases := map[string]struct {
		managersJobs []batchv1.Job
		workerJobs   []batchv1.Job

		operation func(ctx context.Context, adapter *multiKueueAdapter, managerClient, workerClient client.Client) error

		wantError        error
		wantManagersJobs []batchv1.Job
		wantWorkerJobs   []batchv1.Job
	}{
		"sync creates missing remote job": {
			managersJobs: []batchv1.Job{
				*baseJobManagedByKueueBuilder.Clone().Obj(),
			},
			operation: func(ctx context.Context, adapter *multiKueueAdapter, managerClient, workerClient client.Client) error {
				return adapter.SyncJob(ctx, managerClient, workerClient, types.NamespacedName{Name: "job1", Namespace: TestNamespace}, "wl1", "origin1")
			},

			wantManagersJobs: []batchv1.Job{
				*baseJobManagedByKueueBuilder.Clone().Obj(),
			},
			wantWorkerJobs: []batchv1.Job{
				*baseJobBuilder.Clone().
					Label(constants.PrebuiltWorkloadLabel, "wl1").
					Label(kueue.MultiKueueOriginLabel, "origin1").
					Obj(),
			},
		},
		"sync intermediate status from remote job": {
			managersJobs: []batchv1.Job{
				*baseJobManagedByKueueBuilder.Clone().Obj(),
			},
			workerJobs: []batchv1.Job{
				*baseJobBuilder.Clone().
					Label(constants.PrebuiltWorkloadLabel, "wl1").
					Label(kueue.MultiKueueOriginLabel, "origin1").
					Active(2).
					Obj(),
			},
			operation: func(ctx context.Context, adapter *multiKueueAdapter, managerClient, workerClient client.Client) error {
				return adapter.SyncJob(ctx, managerClient, workerClient, types.NamespacedName{Name: "job1", Namespace: TestNamespace}, "wl1", "origin1")
			},

			wantManagersJobs: []batchv1.Job{
				*baseJobManagedByKueueBuilder.Clone().Active(2).Obj(),
			},
			wantWorkerJobs: []batchv1.Job{
				*baseJobBuilder.Clone().
					Label(constants.PrebuiltWorkloadLabel, "wl1").
					Label(kueue.MultiKueueOriginLabel, "origin1").
					Active(2).
					Obj(),
			},
		},
		"skip to sync intermediate status from remote suspended job": {
			managersJobs: []batchv1.Job{
				*baseJobManagedByKueueBuilder.Clone().
					Suspend(true).
					Obj(),
			},
			workerJobs: []batchv1.Job{
				*baseJobBuilder.Clone().
					Label(constants.PrebuiltWorkloadLabel, "wl1").
					Label(kueue.MultiKueueOriginLabel, "origin1").
					Suspend(true).
					Active(2).
					Obj(),
			},
			operation: func(ctx context.Context, adapter *multiKueueAdapter, managerClient, workerClient client.Client) error {
				return adapter.SyncJob(ctx, managerClient, workerClient, types.NamespacedName{Name: "job1", Namespace: TestNamespace}, "wl1", "origin1")
			},
			wantManagersJobs: []batchv1.Job{
				*baseJobManagedByKueueBuilder.Clone().
					Suspend(true).
					Obj(),
			},
			wantWorkerJobs: []batchv1.Job{
				*baseJobBuilder.Clone().
					Label(constants.PrebuiltWorkloadLabel, "wl1").
					Label(kueue.MultiKueueOriginLabel, "origin1").
					Suspend(true).
					Active(2).
					Obj(),
			},
		},
		"sync final status from remote job": {
			managersJobs: []batchv1.Job{
				*baseJobManagedByKueueBuilder.Clone().Obj(),
			},
			workerJobs: []batchv1.Job{
				*baseJobBuilder.Clone().
					Label(constants.PrebuiltWorkloadLabel, "wl1").
					Label(kueue.MultiKueueOriginLabel, "origin1").
					Condition(batchv1.JobCondition{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}).
					Obj(),
			},
			operation: func(ctx context.Context, adapter *multiKueueAdapter, managerClient, workerClient client.Client) error {
				return adapter.SyncJob(ctx, managerClient, workerClient, types.NamespacedName{Name: "job1", Namespace: TestNamespace}, "wl1", "origin1")
			},

			wantManagersJobs: []batchv1.Job{
				*baseJobManagedByKueueBuilder.Clone().
					Condition(batchv1.JobCondition{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}).
					Obj(),
			},
			wantWorkerJobs: []batchv1.Job{
				*baseJobBuilder.Clone().
					Label(constants.PrebuiltWorkloadLabel, "wl1").
					Label(kueue.MultiKueueOriginLabel, "origin1").
					Condition(batchv1.JobCondition{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}).
					Obj(),
			},
		},
		"remote job is deleted": {
			workerJobs: []batchv1.Job{
				*baseJobBuilder.Clone().
					Label(constants.PrebuiltWorkloadLabel, "wl1").
					Label(kueue.MultiKueueOriginLabel, "origin1").
					Active(2).
					Obj(),
			},
			operation: func(ctx context.Context, adapter *multiKueueAdapter, managerClient, workerClient client.Client) error {
				return adapter.DeleteRemoteObject(ctx, workerClient, types.NamespacedName{Name: "job1", Namespace: TestNamespace})
			},
		},
		"missing job is not considered managed": {
			operation: func(ctx context.Context, adapter *multiKueueAdapter, managerClient, workerClient client.Client) error {
				if isManged, _, _ := adapter.IsJobManagedByKueue(ctx, managerClient, types.NamespacedName{Name: "job1", Namespace: TestNamespace}); isManged {
					return errors.New("expecting false")
				}
				return nil
			},
		},
		"job with wrong managedBy is not considered managed": {
			managersJobs: []batchv1.Job{
				*baseJobBuilder.Clone().Obj(),
			},
			operation: func(ctx context.Context, adapter *multiKueueAdapter, managerClient, workerClient client.Client) error {
				if isManged, _, _ := adapter.IsJobManagedByKueue(ctx, managerClient, types.NamespacedName{Name: "job1", Namespace: TestNamespace}); isManged {
					return errors.New("expecting false")
				}
				return nil
			},
			wantManagersJobs: []batchv1.Job{
				*baseJobBuilder.Clone().Obj(),
			},
		},
		"job managedBy multikueue": {
			managersJobs: []batchv1.Job{
				*baseJobManagedByKueueBuilder.Clone().Obj(),
			},
			operation: func(ctx context.Context, adapter *multiKueueAdapter, managerClient, workerClient client.Client) error {
				if isManged, _, _ := adapter.IsJobManagedByKueue(ctx, managerClient, types.NamespacedName{Name: "job1", Namespace: TestNamespace}); !isManged {
					return errors.New("expecting true")
				}
				return nil
			},
			wantManagersJobs: []batchv1.Job{
				*baseJobManagedByKueueBuilder.Clone().Obj(),
			},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			managerBuilder := utiltesting.NewClientBuilder().WithInterceptorFuncs(interceptor.Funcs{SubResourcePatch: utiltesting.TreatSSAAsStrategicMerge})
			managerBuilder = managerBuilder.WithLists(&batchv1.JobList{Items: tc.managersJobs})
			managerBuilder = managerBuilder.WithStatusSubresource(slices.Map(tc.managersJobs, func(w *batchv1.Job) client.Object { return w })...)
			managerClient := managerBuilder.Build()

			workerBuilder := utiltesting.NewClientBuilder().WithInterceptorFuncs(interceptor.Funcs{SubResourcePatch: utiltesting.TreatSSAAsStrategicMerge})
			workerBuilder = workerBuilder.WithLists(&batchv1.JobList{Items: tc.workerJobs})
			workerClient := workerBuilder.Build()

			ctx, _ := utiltesting.ContextWithLog(t)

			adapter := &multiKueueAdapter{}

			gotErr := tc.operation(ctx, adapter, managerClient, workerClient)

			if diff := cmp.Diff(tc.wantError, gotErr, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("unexpected error (-want/+got):\n%s", diff)
			}

			gotManagersJobs := &batchv1.JobList{}
			if err := managerClient.List(ctx, gotManagersJobs); err != nil {
				t.Errorf("unexpected list manager's jobs error %s", err)
			} else {
				if diff := cmp.Diff(tc.wantManagersJobs, gotManagersJobs.Items, objCheckOpts...); diff != "" {
					t.Errorf("unexpected manager's jobs (-want/+got):\n%s", diff)
				}
			}

			gotWorkerJobs := &batchv1.JobList{}
			if err := workerClient.List(ctx, gotWorkerJobs); err != nil {
				t.Errorf("unexpected list worker's jobs error %s", err)
			} else {
				if diff := cmp.Diff(tc.wantWorkerJobs, gotWorkerJobs.Items, objCheckOpts...); diff != "" {
					t.Errorf("unexpected worker's jobs (-want/+got):\n%s", diff)
				}
			}
		})
	}
}

func Test_multiKueueAdapter_SyncJob(t *testing.T) {
	type fields struct {
		features map[featuregate.Feature]bool
	}
	type args struct {
		localClient  client.Client
		remoteClient client.Client
		key          types.NamespacedName
		workloadName string
		origin       string
	}
	type want struct {
		err       bool
		localJob  *batchv1.Job
		remoteJob *batchv1.Job
	}

	schema := runtime.NewScheme()
	_ = scheme.AddToScheme(schema)
	_ = kueue.AddToScheme(schema)

	newJob := func() *utiltestingjob.JobWrapper {
		return utiltestingjob.MakeJob("test", TestNamespace).ResourceVersion("1")
	}
	runningJobCondition := batchv1.JobCondition{
		Type:   batchv1.JobSuccessCriteriaMet,
		Status: corev1.ConditionFalse,
	}

	tests := map[string]struct {
		fields fields
		args   args
		want   want
	}{
		"FailureToRetrieveLocalJob": {
			args: args{
				localClient: fake.NewClientBuilder().Build(),
			},
			want: want{
				err: true,
			},
		},
		"FailureToRetrieveRemoteJob": {
			args: args{
				localClient: fake.NewClientBuilder().WithScheme(schema).WithObjects(newJob().
					ManagedBy("parent").Obj()).Build(),
				remoteClient: fake.NewClientBuilder().WithInterceptorFuncs(interceptor.Funcs{
					Get: func(_ context.Context, _ client.WithWatch, _ client.ObjectKey, _ client.Object, _ ...client.GetOption) error {
						return errors.New("test-error")
					},
				}).Build(),
				key: client.ObjectKeyFromObject(newJob().Obj()),
			},
			want: want{
				err:      true,
				localJob: newJob().ManagedBy("parent").Obj(),
			},
		},
		"RemoteJobNotFound": {
			args: args{
				localClient: fake.NewClientBuilder().WithScheme(schema).WithObjects(newJob().
					ManagedBy("parent").Obj()).Build(),
				remoteClient: fake.NewClientBuilder().WithScheme(schema).Build(),
				key:          client.ObjectKeyFromObject(newJob().Obj()),
			},
			want: want{
				localJob: newJob().ManagedBy("parent").Obj(),
				remoteJob: newJob().Label(kueue.MultiKueueOriginLabel, "").
					Label(constants.PrebuiltWorkloadLabel, "").
					Obj(),
			},
		},
		"RemoteJobInProgress_LocalIsManagedButStillSuspended": {
			args: args{
				localClient: fake.NewClientBuilder().WithScheme(schema).WithObjects(newJob().Obj()).Build(),
				remoteClient: fake.NewClientBuilder().WithScheme(schema).WithObjects(newJob().
					Condition(runningJobCondition).
					Obj()).Build(),
				key: client.ObjectKeyFromObject(newJob().Obj()),
			},
			want: want{
				localJob: newJob().Obj(),
				remoteJob: newJob().
					Condition(runningJobCondition).
					Obj(),
			},
		},
		"RemoteJobInProgress_LocalIsManagedAndUnsuspended": {
			args: args{
				localClient: fake.NewClientBuilder().WithScheme(schema).WithObjects(newJob().Suspend(false).Obj()).Build(),
				remoteClient: fake.NewClientBuilder().WithScheme(schema).WithObjects(newJob().
					Condition(runningJobCondition).
					Obj()).Build(),
				key: client.ObjectKeyFromObject(newJob().Obj()),
			},
			want: want{
				localJob: newJob().
					ResourceVersion("2").
					Suspend(false).
					Condition(runningJobCondition).
					Obj(),
				remoteJob: newJob().
					Condition(runningJobCondition).
					Obj(),
			},
		},
		"RemoteJobInProgress_LocalIsNotManaged": {
			args: args{
				localClient: fake.NewClientBuilder().WithScheme(schema).WithObjects(newJob().Obj()).Build(),
				remoteClient: fake.NewClientBuilder().WithScheme(schema).WithObjects(newJob().
					Condition(runningJobCondition).
					Obj()).Build(),
				key: client.ObjectKeyFromObject(newJob().Obj()),
			},
			want: want{
				localJob: newJob().Obj(),
				remoteJob: newJob().
					Condition(runningJobCondition).
					Obj(),
			},
		},
		"RemoteJobFinished_Completed": {
			args: args{
				localClient: fake.NewClientBuilder().WithScheme(schema).WithObjects(newJob().Suspend(false).Obj()).Build(),
				remoteClient: fake.NewClientBuilder().WithScheme(schema).WithObjects(newJob().
					Condition(batchv1.JobCondition{
						Type:   batchv1.JobComplete,
						Status: corev1.ConditionTrue,
					}).
					Obj()).Build(),
				key: client.ObjectKeyFromObject(newJob().Obj()),
			},
			want: want{
				localJob: newJob().
					Condition(batchv1.JobCondition{
						Type:   batchv1.JobComplete,
						Status: corev1.ConditionTrue,
					}).
					Suspend(false).
					ResourceVersion("2").
					Obj(),
			},
		},
		"RemoteJobFinished_Failed": {
			args: args{
				localClient: fake.NewClientBuilder().WithScheme(schema).WithObjects(newJob().Suspend(false).Obj()).Build(),
				remoteClient: fake.NewClientBuilder().WithScheme(schema).WithObjects(newJob().
					Suspend(false).
					Condition(batchv1.JobCondition{
						Type:   batchv1.JobFailed,
						Status: corev1.ConditionTrue,
					}).
					Obj()).Build(),
				key: client.ObjectKeyFromObject(newJob().Obj()),
			},
			want: want{
				localJob: newJob().
					Suspend(false).
					Condition(batchv1.JobCondition{
						Type:   batchv1.JobFailed,
						Status: corev1.ConditionTrue,
					}).
					ResourceVersion("2").
					Obj(),
			},
		},
		"ElasticJob_RemoteInSync": {
			fields: fields{
				features: map[featuregate.Feature]bool{features.ElasticJobsViaWorkloadSlices: true},
			},
			args: args{
				localClient: fake.NewClientBuilder().WithScheme(schema).WithObjects(newJob().
					SetAnnotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
					Condition(runningJobCondition).
					Obj()).Build(),
				remoteClient: fake.NewClientBuilder().WithScheme(schema).WithObjects(newJob().
					SetAnnotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
					Label(constants.PrebuiltWorkloadLabel, "test-workload").
					Condition(runningJobCondition).
					Obj()).Build(),
				key:          client.ObjectKeyFromObject(newJob().Obj()),
				workloadName: "test-workload",
			},
			want: want{
				localJob: newJob().
					SetAnnotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
					Condition(runningJobCondition).
					Obj(),
				remoteJob: newJob().
					SetAnnotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
					Label(constants.PrebuiltWorkloadLabel, "test-workload").
					Condition(runningJobCondition).
					Obj(),
			},
		},
		"ElasticJob_LocalIsStale": {
			fields: fields{
				features: map[featuregate.Feature]bool{features.ElasticJobsViaWorkloadSlices: true},
			},
			args: args{
				localClient: fake.NewClientBuilder().WithScheme(schema).WithObjects(newJob().
					SetAnnotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
					Parallelism(2).
					Condition(runningJobCondition).
					Obj()).Build(),
				remoteClient: fake.NewClientBuilder().WithScheme(schema).WithObjects(newJob().
					SetAnnotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
					Label(constants.PrebuiltWorkloadLabel, "test-workload").
					Condition(runningJobCondition).
					Obj()).Build(),
				key:          client.ObjectKeyFromObject(newJob().Obj()),
				workloadName: "test-workload",
			},
			want: want{
				localJob: newJob().
					SetAnnotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
					Parallelism(2).
					Condition(runningJobCondition).
					Obj(),
				remoteJob: newJob().
					SetAnnotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
					Label(constants.PrebuiltWorkloadLabel, "test-workload").
					Condition(runningJobCondition).
					Obj(),
			},
		},
		"ElasticJob_WorkloadNameOnlyChange_EdgeCase": {
			fields: fields{
				features: map[featuregate.Feature]bool{features.ElasticJobsViaWorkloadSlices: true},
			},
			args: args{
				localClient: fake.NewClientBuilder().WithScheme(schema).WithObjects(newJob().
					SetAnnotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
					Condition(runningJobCondition).
					Obj()).Build(),
				remoteClient: fake.NewClientBuilder().WithScheme(schema).WithObjects(newJob().
					SetAnnotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
					Condition(runningJobCondition).
					Obj()).Build(),
				key:          client.ObjectKeyFromObject(newJob().Obj()),
				workloadName: "test-workload-new",
			},
			want: want{
				localJob: newJob().
					SetAnnotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
					Condition(runningJobCondition).
					Obj(),
				remoteJob: newJob().
					ResourceVersion("1").
					SetAnnotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
					Condition(runningJobCondition).
					Obj(),
			},
		},
		"ElasticJob_RemoteOutOfSync": {
			fields: fields{
				features: map[featuregate.Feature]bool{features.ElasticJobsViaWorkloadSlices: true},
			},
			args: args{
				localClient: fake.NewClientBuilder().WithScheme(schema).WithObjects(newJob().
					SetAnnotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
					Parallelism(22).
					Condition(runningJobCondition).
					Obj()).Build(),
				remoteClient: fake.NewClientBuilder().WithScheme(schema).WithObjects(newJob().
					SetAnnotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
					Label(constants.PrebuiltWorkloadLabel, "test-workload").
					Condition(runningJobCondition).
					Obj()).Build(),
				key:          client.ObjectKeyFromObject(newJob().Obj()),
				workloadName: jobframework.GetWorkloadNameForOwnerWithGVKAndGeneration("test", "", gvk, 0),
			},
			want: want{
				localJob: newJob().
					SetAnnotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
					Parallelism(22).
					Condition(runningJobCondition).
					Obj(),
				remoteJob: newJob().
					ResourceVersion("1").
					SetAnnotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
					Label(constants.PrebuiltWorkloadLabel, "test-workload").
					Parallelism(1).
					Condition(runningJobCondition).
					Obj(),
			},
		},
		"ElasticJob_RemoteOutOfSync_PatchFailure": {
			fields: fields{
				features: map[featuregate.Feature]bool{features.ElasticJobsViaWorkloadSlices: true},
			},
			args: args{
				localClient: fake.NewClientBuilder().WithScheme(schema).WithObjects(newJob().
					SetAnnotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
					Parallelism(22).
					Condition(runningJobCondition).
					Obj()).Build(),
				remoteClient: fake.NewClientBuilder().WithScheme(schema).WithObjects(newJob().
					SetAnnotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
					Label(constants.PrebuiltWorkloadLabel, "test-workload").
					Condition(runningJobCondition).
					Obj()).
					WithInterceptorFuncs(interceptor.Funcs{
						Patch: func(ctx context.Context, client client.WithWatch, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
							return errors.New("test-patch-error")
						},
					}).Build(),
				key:          client.ObjectKeyFromObject(newJob().Obj()),
				workloadName: jobframework.GetWorkloadNameForOwnerWithGVKAndGeneration("test", "", gvk, 0),
			},
			want: want{
				localJob: newJob().
					SetAnnotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
					Parallelism(22).
					Condition(runningJobCondition).
					Obj(),
				remoteJob: newJob().
					SetAnnotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
					Label(constants.PrebuiltWorkloadLabel, "test-workload").
					Condition(runningJobCondition).
					Obj(),
			},
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			// Setup feature gates if any provided.
			for featureName, enabled := range tt.fields.features {
				features.SetFeatureGateDuringTest(t, featureName, enabled)
			}

			adapter := &multiKueueAdapter{}
			ctx, _ := utiltesting.ContextWithLog(t)

			// Function call under test with result (error) assertion.
			if err := adapter.SyncJob(ctx, tt.args.localClient, tt.args.remoteClient, tt.args.key, tt.args.workloadName, tt.args.origin); (err != nil) != tt.want.err {
				t.Errorf("SyncJob() error = %v, wantErr %v", err, tt.want.err)
			}

			// Side effect assertion: changes to the local job. Must have (not nil) both the client and the job.
			if tt.args.localClient != nil && tt.want.localJob != nil {
				got := &batchv1.Job{}
				if err := tt.args.localClient.Get(ctx, client.ObjectKeyFromObject(tt.want.localJob), got); err != nil {
					t.Errorf("SyncJob() unexpected assertion error retrieving local job: %v", err)
				}
				if diff := cmp.Diff(tt.want.localJob, got, cmpopts.EquateEmpty()); diff != "" {
					t.Errorf("SyncJob() localJob (-want,+got):\n%s", diff)
				}
			}
			// Side effect assertion: changes on the remote job. Must have (not nil) both the client and the job.
			if tt.args.remoteClient != nil && tt.want.remoteJob != nil {
				got := &batchv1.Job{}
				if err := tt.args.remoteClient.Get(ctx, client.ObjectKeyFromObject(tt.want.remoteJob), got); err != nil {
					t.Errorf("SyncJob() unexpected assertion error retrieving remote job: %v", err)
				}
				if diff := cmp.Diff(tt.want.remoteJob, got, cmpopts.EquateEmpty()); diff != "" {
					t.Errorf("SyncJob() remoteJob (-want,+got):\n%s", diff)
				}
			}
		})
	}
}
