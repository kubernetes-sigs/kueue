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
		featureGates     map[featuregate.Feature]bool
	}{
		"sync creates missing remote job": {
			featureGates: map[featuregate.Feature]bool{features.WorkloadIdentifierAnnotations: false},
			managersJobs: []batchv1.Job{
				*baseJobManagedByKueueBuilder.DeepCopy(),
			},
			operation: func(ctx context.Context, adapter *multiKueueAdapter, managerClient, workerClient client.Client) error {
				_, err := adapter.SyncJob(ctx, managerClient, workerClient, types.NamespacedName{Name: "job1", Namespace: TestNamespace}, "wl1", "origin1")
				return err
			},

			wantManagersJobs: []batchv1.Job{
				*baseJobManagedByKueueBuilder.DeepCopy(),
			},
			wantWorkerJobs: []batchv1.Job{
				*baseJobBuilder.Clone().
					PrebuiltWorkloadLabel("wl1").
					Label(kueue.MultiKueueOriginLabel, "origin1").
					Obj(),
			},
		},
		"sync intermediate status from remote job": {
			featureGates: map[featuregate.Feature]bool{features.WorkloadIdentifierAnnotations: false},
			managersJobs: []batchv1.Job{
				*baseJobManagedByKueueBuilder.DeepCopy(),
			},
			workerJobs: []batchv1.Job{
				*baseJobBuilder.Clone().
					PrebuiltWorkloadLabel("wl1").
					Label(kueue.MultiKueueOriginLabel, "origin1").
					Active(2).
					Obj(),
			},
			operation: func(ctx context.Context, adapter *multiKueueAdapter, managerClient, workerClient client.Client) error {
				_, err := adapter.SyncJob(ctx, managerClient, workerClient, types.NamespacedName{Name: "job1", Namespace: TestNamespace}, "wl1", "origin1")
				return err
			},

			wantManagersJobs: []batchv1.Job{
				*baseJobManagedByKueueBuilder.Clone().Active(2).Obj(),
			},
			wantWorkerJobs: []batchv1.Job{
				*baseJobBuilder.Clone().
					PrebuiltWorkloadLabel("wl1").
					Label(kueue.MultiKueueOriginLabel, "origin1").
					Active(2).
					Obj(),
			},
		},
		"sync intermediate status from remote suspended job": {
			featureGates: map[featuregate.Feature]bool{features.WorkloadIdentifierAnnotations: false},
			managersJobs: []batchv1.Job{
				*baseJobManagedByKueueBuilder.Clone().
					Suspend(true).
					Obj(),
			},
			workerJobs: []batchv1.Job{
				*baseJobBuilder.Clone().
					PrebuiltWorkloadLabel("wl1").
					Label(kueue.MultiKueueOriginLabel, "origin1").
					Suspend(true).
					Active(2).
					Obj(),
			},
			operation: func(ctx context.Context, adapter *multiKueueAdapter, managerClient, workerClient client.Client) error {
				_, err := adapter.SyncJob(ctx, managerClient, workerClient, types.NamespacedName{Name: "job1", Namespace: TestNamespace}, "wl1", "origin1")
				return err
			},
			wantManagersJobs: []batchv1.Job{
				*baseJobManagedByKueueBuilder.Clone().
					Suspend(true).
					Active(2).
					Obj(),
			},
			wantWorkerJobs: []batchv1.Job{
				*baseJobBuilder.Clone().
					PrebuiltWorkloadLabel("wl1").
					Label(kueue.MultiKueueOriginLabel, "origin1").
					Suspend(true).
					Active(2).
					Obj(),
			},
		},
		"sync final status from remote job": {
			featureGates: map[featuregate.Feature]bool{features.WorkloadIdentifierAnnotations: false},
			managersJobs: []batchv1.Job{
				*baseJobManagedByKueueBuilder.DeepCopy(),
			},
			workerJobs: []batchv1.Job{
				*baseJobBuilder.Clone().
					PrebuiltWorkloadLabel("wl1").
					Label(kueue.MultiKueueOriginLabel, "origin1").
					Condition(batchv1.JobCondition{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}).
					Obj(),
			},
			operation: func(ctx context.Context, adapter *multiKueueAdapter, managerClient, workerClient client.Client) error {
				_, err := adapter.SyncJob(ctx, managerClient, workerClient, types.NamespacedName{Name: "job1", Namespace: TestNamespace}, "wl1", "origin1")
				return err
			},

			wantManagersJobs: []batchv1.Job{
				*baseJobManagedByKueueBuilder.Clone().
					Condition(batchv1.JobCondition{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}).
					Obj(),
			},
			wantWorkerJobs: []batchv1.Job{
				*baseJobBuilder.Clone().
					PrebuiltWorkloadLabel("wl1").
					Label(kueue.MultiKueueOriginLabel, "origin1").
					Condition(batchv1.JobCondition{Type: batchv1.JobComplete, Status: corev1.ConditionTrue}).
					Obj(),
			},
		},
		"remote job is deleted": {
			featureGates: map[featuregate.Feature]bool{features.WorkloadIdentifierAnnotations: false},
			workerJobs: []batchv1.Job{
				*baseJobBuilder.Clone().
					PrebuiltWorkloadLabel("wl1").
					Label(kueue.MultiKueueOriginLabel, "origin1").
					Active(2).
					Obj(),
			},
			operation: func(ctx context.Context, adapter *multiKueueAdapter, managerClient, workerClient client.Client) error {
				return adapter.DeleteRemoteObject(ctx, managerClient, workerClient, types.NamespacedName{Name: "job1", Namespace: TestNamespace})
			},
		},
		"missing job is not considered managed": {
			featureGates: map[featuregate.Feature]bool{features.WorkloadIdentifierAnnotations: false},
			operation: func(ctx context.Context, adapter *multiKueueAdapter, managerClient, workerClient client.Client) error {
				if isManged, _, _ := adapter.IsJobManagedByKueue(ctx, managerClient, types.NamespacedName{Name: "job1", Namespace: TestNamespace}); isManged {
					return errors.New("expecting false")
				}
				return nil
			},
		},
		"job with wrong managedBy is not considered managed": {
			featureGates: map[featuregate.Feature]bool{features.WorkloadIdentifierAnnotations: false},
			managersJobs: []batchv1.Job{
				*baseJobBuilder.DeepCopy(),
			},
			operation: func(ctx context.Context, adapter *multiKueueAdapter, managerClient, workerClient client.Client) error {
				if isManged, _, _ := adapter.IsJobManagedByKueue(ctx, managerClient, types.NamespacedName{Name: "job1", Namespace: TestNamespace}); isManged {
					return errors.New("expecting false")
				}
				return nil
			},
			wantManagersJobs: []batchv1.Job{
				*baseJobBuilder.DeepCopy(),
			},
		},
		"job managedBy multikueue": {
			featureGates: map[featuregate.Feature]bool{features.WorkloadIdentifierAnnotations: false},
			managersJobs: []batchv1.Job{
				*baseJobManagedByKueueBuilder.DeepCopy(),
			},
			operation: func(ctx context.Context, adapter *multiKueueAdapter, managerClient, workerClient client.Client) error {
				if isManged, _, _ := adapter.IsJobManagedByKueue(ctx, managerClient, types.NamespacedName{Name: "job1", Namespace: TestNamespace}); !isManged {
					return errors.New("expecting true")
				}
				return nil
			},
			wantManagersJobs: []batchv1.Job{
				*baseJobManagedByKueueBuilder.DeepCopy(),
			},
		},
		"sync creates missing remote job, WorkloadIdentifierAnnotations enabled": {
			featureGates: map[featuregate.Feature]bool{features.WorkloadIdentifierAnnotations: true},
			managersJobs: []batchv1.Job{
				*baseJobManagedByKueueBuilder.DeepCopy(),
			},
			operation: func(ctx context.Context, adapter *multiKueueAdapter, managerClient, workerClient client.Client) error {
				_, err := adapter.SyncJob(ctx, managerClient, workerClient, types.NamespacedName{Name: "job1", Namespace: TestNamespace}, "wl1", "origin1")
				return err
			},
			wantManagersJobs: []batchv1.Job{
				*baseJobManagedByKueueBuilder.DeepCopy(),
			},
			wantWorkerJobs: []batchv1.Job{
				*baseJobBuilder.Clone().
					PrebuiltWorkloadAnnotation("wl1").
					Label(kueue.MultiKueueOriginLabel, "origin1").
					Obj(),
			},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGatesDuringTest(t, tc.featureGates)
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
		featureGates map[featuregate.Feature]bool
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
		deferred  bool
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
			fields: fields{
				featureGates: map[featuregate.Feature]bool{features.WorkloadIdentifierAnnotations: false},
			},
			args: args{
				localClient: fake.NewClientBuilder().WithScheme(schema).WithObjects(newJob().
					ManagedBy("parent").Obj()).Build(),
				remoteClient: fake.NewClientBuilder().WithScheme(schema).Build(),
				key:          client.ObjectKeyFromObject(newJob().Obj()),
			},
			want: want{
				localJob: newJob().ManagedBy("parent").Obj(),
				remoteJob: newJob().Label(kueue.MultiKueueOriginLabel, "").
					PrebuiltWorkloadLabel("").
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
				localJob: newJob().
					ResourceVersion("2").
					Condition(runningJobCondition).
					Obj(),
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
				featureGates: map[featuregate.Feature]bool{features.ElasticJobsViaWorkloadSlices: true},
			},
			args: args{
				localClient: fake.NewClientBuilder().WithScheme(schema).WithObjects(newJob().
					SetAnnotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
					Condition(runningJobCondition).
					Obj()).Build(),
				remoteClient: fake.NewClientBuilder().WithScheme(schema).WithObjects(newJob().
					SetAnnotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
					PrebuiltWorkloadLabel("test-workload").
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
					PrebuiltWorkloadLabel("test-workload").
					Condition(runningJobCondition).
					Obj(),
			},
		},
		"ElasticJob_LocalIsStale": {
			fields: fields{
				featureGates: map[featuregate.Feature]bool{features.ElasticJobsViaWorkloadSlices: true},
			},
			args: args{
				localClient: fake.NewClientBuilder().WithScheme(schema).WithObjects(newJob().
					SetAnnotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
					Parallelism(2).
					Condition(runningJobCondition).
					Obj()).Build(),
				remoteClient: fake.NewClientBuilder().WithScheme(schema).WithObjects(newJob().
					SetAnnotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
					PrebuiltWorkloadLabel("test-workload").
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
					PrebuiltWorkloadLabel("test-workload").
					Condition(runningJobCondition).
					Obj(),
			},
		},
		"ElasticJob_WorkloadNameOnlyChange_EdgeCase": {
			fields: fields{
				featureGates: map[featuregate.Feature]bool{features.ElasticJobsViaWorkloadSlices: true},
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
					ResourceVersion("2").
					SetAnnotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
					SetAnnotation(constants.PrebuiltWorkloadAnnotation, "test-workload-new").
					Condition(runningJobCondition).
					Obj(),
			},
		},
		"ElasticJob_RemoteOutOfSync": {
			fields: fields{
				featureGates: map[featuregate.Feature]bool{features.ElasticJobsViaWorkloadSlices: true},
			},
			args: args{
				localClient: fake.NewClientBuilder().WithScheme(schema).WithObjects(newJob().
					SetAnnotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
					Parallelism(22).
					Condition(runningJobCondition).
					Obj()).Build(),
				remoteClient: fake.NewClientBuilder().WithScheme(schema).WithObjects(newJob().
					SetAnnotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
					PrebuiltWorkloadLabel("test-workload").
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
					ResourceVersion("2").
					SetAnnotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
					PrebuiltWorkloadLabel("test-workload").
					SetAnnotation(constants.PrebuiltWorkloadAnnotation, "job-test-f53b2").
					Parallelism(22).
					Condition(runningJobCondition).
					Obj(),
			},
		},
		"ElasticJob_RemoteOutOfSync_PatchFailure": {
			fields: fields{
				featureGates: map[featuregate.Feature]bool{features.ElasticJobsViaWorkloadSlices: true},
			},
			args: args{
				localClient: fake.NewClientBuilder().WithScheme(schema).WithObjects(newJob().
					SetAnnotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
					Parallelism(22).
					Condition(runningJobCondition).
					Obj()).Build(),
				remoteClient: fake.NewClientBuilder().WithScheme(schema).WithObjects(newJob().
					SetAnnotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
					PrebuiltWorkloadLabel("test-workload").
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
				err: true,
				localJob: newJob().
					SetAnnotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
					Parallelism(22).
					Condition(runningJobCondition).
					Obj(),
				remoteJob: newJob().
					SetAnnotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
					PrebuiltWorkloadLabel("test-workload").
					Condition(runningJobCondition).
					Obj(),
			},
		},
		// Regression test for #11115: when an elastic sync is needed AND the local
		// Job is still suspended (remote unsuspended and not finished), SyncJob must
		// still report deferred=true — the deferred flag must not be dropped on the
		// elastic-sync return path.
		"ElasticJob_DeferredWhenLocalSuspended": {
			fields: fields{
				featureGates: map[featuregate.Feature]bool{features.ElasticJobsViaWorkloadSlices: true},
			},
			args: args{
				// local: suspended (default), elastic, parallelism 22.
				localClient: fake.NewClientBuilder().WithScheme(schema).WithObjects(newJob().
					SetAnnotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
					Parallelism(22).
					Condition(runningJobCondition).
					Obj()).Build(),
				// remote: unsuspended and not finished, elastic, parallelism unset
				// (mismatch with local -> needElasticJobSync is true).
				remoteClient: fake.NewClientBuilder().WithScheme(schema).WithObjects(newJob().
					SetAnnotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
					Suspend(false).
					PrebuiltWorkloadLabel("test-workload").
					Condition(runningJobCondition).
					Obj()).Build(),
				key:          client.ObjectKeyFromObject(newJob().Obj()),
				workloadName: jobframework.GetWorkloadNameForOwnerWithGVKAndGeneration("test", "", gvk, 0),
			},
			// localJob/remoteJob left nil: the deferred path patches the local
			// JobSuspended condition with a wall-clock timestamp not worth asserting
			// here. This case guards the deferred flag, not the patched objects.
			want: want{
				deferred: true,
			},
		},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			// Setup feature gates if any provided.
			features.SetFeatureGatesDuringTest(t, tt.fields.featureGates)

			adapter := &multiKueueAdapter{}
			ctx, _ := utiltesting.ContextWithLog(t)

			origin := tt.args.origin
			if origin == "" {
				origin = "origin1"
			}

			if tt.args.remoteClient != nil {
				remoteJobs := &batchv1.JobList{}
				if err := tt.args.remoteClient.List(ctx, remoteJobs); err == nil {
					for i := range remoteJobs.Items {
						j := &remoteJobs.Items[i]
						if j.Labels == nil {
							j.Labels = make(map[string]string)
						}
						if j.Labels[kueue.MultiKueueOriginLabel] == "" {
							j.Labels[kueue.MultiKueueOriginLabel] = origin
							if err := tt.args.remoteClient.Update(ctx, j); err != nil {
								t.Fatalf("failed to prepare remote job origin label: %v", err)
							}
						}
					}
				}
			}

			if tt.want.remoteJob != nil {
				if tt.want.remoteJob.Labels == nil {
					tt.want.remoteJob.Labels = make(map[string]string)
				}
				if tt.want.remoteJob.Labels[kueue.MultiKueueOriginLabel] == "" {
					tt.want.remoteJob.Labels[kueue.MultiKueueOriginLabel] = origin
				}
			}

			// Function call under test with result (deferred, error) assertion.
			gotDeferred, gotErr := adapter.SyncJob(ctx, tt.args.localClient, tt.args.remoteClient, tt.args.key, tt.args.workloadName, origin)
			if (gotErr != nil) != tt.want.err {
				t.Errorf("SyncJob() error = %v, wantErr %v", gotErr, tt.want.err)
			}
			if gotDeferred != tt.want.deferred {
				t.Errorf("SyncJob() deferred = %v, want %v for case %q", gotDeferred, tt.want.deferred, name)
			}

			// Side effect assertion: changes to the local job. Must have (not nil) both the client and the job.
			if tt.args.localClient != nil && tt.want.localJob != nil {
				got := &batchv1.Job{}
				if err := tt.args.localClient.Get(ctx, client.ObjectKeyFromObject(tt.want.localJob), got); err != nil {
					t.Errorf("SyncJob() unexpected assertion error retrieving local job: %v", err)
				}
				if diff := cmp.Diff(tt.want.localJob, got, cmpopts.EquateEmpty(), cmpopts.IgnoreFields(metav1.ObjectMeta{}, "ResourceVersion")); diff != "" {
					t.Errorf("SyncJob() localJob (-want,+got):\n%s", diff)
				}
			}
			// Side effect assertion: changes on the remote job. Must have (not nil) both the client and the job.
			if tt.args.remoteClient != nil && tt.want.remoteJob != nil {
				got := &batchv1.Job{}
				if err := tt.args.remoteClient.Get(ctx, client.ObjectKeyFromObject(tt.want.remoteJob), got); err != nil {
					t.Errorf("SyncJob() unexpected assertion error retrieving remote job: %v", err)
				}
				if diff := cmp.Diff(tt.want.remoteJob, got, cmpopts.EquateEmpty(), cmpopts.IgnoreFields(metav1.ObjectMeta{}, "ResourceVersion")); diff != "" {
					t.Errorf("SyncJob() remoteJob (-want,+got):\n%s", diff)
				}
			}
		})
	}
}

// Test_multiKueueAdapter_SyncJob_DeferredWhenLocalSuspended covers the race
// observed in https://github.com/kubernetes-sigs/kueue/issues/11115. When the
// remote Job is already unsuspended (and not finished) but the local Job is
// still suspended, SyncJob cannot propagate status.Active to the local Job
// without violating K8s 1.36 suspend-validation rules. In that case SyncJob
// must report deferred=true so the multikueue reconciler can requeue on a
// short timer; without that signal the workload reconciler waits its default
// workerLostTimeout requeue and the manager Job's status.Active never catches up.
func Test_multiKueueAdapter_SyncJob_DeferredWhenLocalSuspended(t *testing.T) {
	schema := runtime.NewScheme()
	_ = scheme.AddToScheme(schema)
	_ = kueue.AddToScheme(schema)

	newJob := func() *utiltestingjob.JobWrapper {
		return utiltestingjob.MakeJob("test", TestNamespace).ResourceVersion("1")
	}

	localJob := newJob().Obj() // Suspend defaults to true on JobWrapper
	const origin = "origin1"
	remoteJob := newJob().
		Label(kueue.MultiKueueOriginLabel, origin).
		Suspend(false).
		Active(1).
		Obj()

	localClient := fake.NewClientBuilder().WithScheme(schema).WithObjects(localJob).Build()
	remoteClient := fake.NewClientBuilder().WithScheme(schema).WithObjects(remoteJob).Build()

	adapter := &multiKueueAdapter{}
	ctx, _ := utiltesting.ContextWithLog(t)

	deferred, err := adapter.SyncJob(ctx, localClient, remoteClient,
		client.ObjectKeyFromObject(localJob), "" /*workloadName*/, origin)
	if err != nil {
		t.Fatalf("SyncJob() unexpected error = %v", err)
	}
	if !deferred {
		t.Fatalf("SyncJob() deferred = false; want true (local suspended, remote unsuspended and not finished)")
	}
}
