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

package trainjob

import (
	"context"
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	kftrainerapi "github.com/kubeflow/trainer/v2/pkg/apis/trainer/v1alpha1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/util/slices"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	testingtrainjob "sigs.k8s.io/kueue/pkg/util/testingjobs/trainjob"
)

const (
	TestNamespace = "ns"
)

func TestMultiKueueAdapter(t *testing.T) {
	objCheckOpts := cmp.Options{
		cmpopts.IgnoreFields(metav1.ObjectMeta{}, "ResourceVersion"),
		cmpopts.EquateEmpty(),
	}

	baseTrainJobBuilder := testingtrainjob.MakeTrainJob("trainjob1", TestNamespace).Suspend(false)
	baseTrainJobManagedByKueueBuilder := baseTrainJobBuilder.Clone().ManagedBy(kueue.MultiKueueControllerName)

	cases := map[string]struct {
		managersTrainJobs []kftrainerapi.TrainJob
		workerTrainJobs   []kftrainerapi.TrainJob

		operation func(ctx context.Context, adapter *multiKueueAdapter, managerClient, workerClient client.Client) error

		wantError             error
		wantManagersTrainJobs []kftrainerapi.TrainJob
		wantWorkerTrainJobs   []kftrainerapi.TrainJob
	}{

		"sync creates missing remote TrainJob": {
			managersTrainJobs: []kftrainerapi.TrainJob{
				*baseTrainJobManagedByKueueBuilder.Clone().Obj(),
			},
			operation: func(ctx context.Context, adapter *multiKueueAdapter, managerClient, workerClient client.Client) error {
				return adapter.SyncJob(ctx, managerClient, workerClient, types.NamespacedName{Name: "trainjob1", Namespace: TestNamespace}, "wl1", "origin1")
			},

			wantManagersTrainJobs: []kftrainerapi.TrainJob{
				*baseTrainJobManagedByKueueBuilder.Clone().Obj(),
			},
			wantWorkerTrainJobs: []kftrainerapi.TrainJob{
				*baseTrainJobBuilder.Clone().
					Label(constants.PrebuiltWorkloadLabel, "wl1").
					Label(kueue.MultiKueueOriginLabel, "origin1").
					Obj(),
			},
		},
		"sync status from remote trainjob": {
			managersTrainJobs: []kftrainerapi.TrainJob{
				*baseTrainJobManagedByKueueBuilder.Clone().Obj(),
			},
			workerTrainJobs: []kftrainerapi.TrainJob{
				*baseTrainJobBuilder.Clone().
					Label(constants.PrebuiltWorkloadLabel, "wl1").
					Label(kueue.MultiKueueOriginLabel, "origin1").
					JobsStatus(
						testingtrainjob.MakeJobStatusWrapper("replicated-job-1").
							Ready(1).
							Succeeded(1).
							Obj(),
						testingtrainjob.MakeJobStatusWrapper("replicated-job-2").
							Ready(3).
							Succeeded(0).
							Obj(),
					).
					Obj(),
			},
			operation: func(ctx context.Context, adapter *multiKueueAdapter, managerClient, workerClient client.Client) error {
				return adapter.SyncJob(ctx, managerClient, workerClient, types.NamespacedName{Name: "trainjob1", Namespace: TestNamespace}, "wl1", "origin1")
			},

			wantManagersTrainJobs: []kftrainerapi.TrainJob{
				*baseTrainJobManagedByKueueBuilder.Clone().
					JobsStatus(
						testingtrainjob.MakeJobStatusWrapper("replicated-job-1").
							Ready(1).
							Succeeded(1).
							Obj(),
						testingtrainjob.MakeJobStatusWrapper("replicated-job-2").
							Ready(3).
							Succeeded(0).
							Obj(),
					).
					Obj(),
			},
			wantWorkerTrainJobs: []kftrainerapi.TrainJob{
				*baseTrainJobBuilder.Clone().
					Label(constants.PrebuiltWorkloadLabel, "wl1").
					Label(kueue.MultiKueueOriginLabel, "origin1").
					JobsStatus(
						testingtrainjob.MakeJobStatusWrapper("replicated-job-1").
							Ready(1).
							Succeeded(1).
							Obj(),
						testingtrainjob.MakeJobStatusWrapper("replicated-job-2").
							Ready(3).
							Succeeded(0).
							Obj(),
					).
					Obj(),
			},
		},
		"skip to sync status from remote suspended trainjob": {
			managersTrainJobs: []kftrainerapi.TrainJob{
				*baseTrainJobManagedByKueueBuilder.Clone().
					Suspend(true).
					Obj(),
			},
			workerTrainJobs: []kftrainerapi.TrainJob{
				*baseTrainJobBuilder.Clone().
					Label(constants.PrebuiltWorkloadLabel, "wl1").
					Label(kueue.MultiKueueOriginLabel, "origin1").
					Suspend(true).
					JobsStatus(
						testingtrainjob.MakeJobStatusWrapper("replicated-job-1").
							Ready(1).
							Succeeded(1).
							Obj(),
						testingtrainjob.MakeJobStatusWrapper("replicated-job-2").
							Ready(3).
							Succeeded(0).
							Obj(),
					).
					Obj(),
			},
			operation: func(ctx context.Context, adapter *multiKueueAdapter, managerClient, workerClient client.Client) error {
				return adapter.SyncJob(ctx, managerClient, workerClient, types.NamespacedName{Name: "trainjob1", Namespace: TestNamespace}, "wl1", "origin1")
			},

			wantManagersTrainJobs: []kftrainerapi.TrainJob{
				*baseTrainJobManagedByKueueBuilder.Clone().
					Suspend(true).
					Obj(),
			},
			wantWorkerTrainJobs: []kftrainerapi.TrainJob{
				*baseTrainJobBuilder.Clone().
					Label(constants.PrebuiltWorkloadLabel, "wl1").
					Label(kueue.MultiKueueOriginLabel, "origin1").
					Suspend(true).
					JobsStatus(
						testingtrainjob.MakeJobStatusWrapper("replicated-job-1").
							Ready(1).
							Succeeded(1).
							Obj(),
						testingtrainjob.MakeJobStatusWrapper("replicated-job-2").
							Ready(3).
							Succeeded(0).
							Obj(),
					).
					Obj(),
			},
		},
		"remote trainjob is deleted": {
			workerTrainJobs: []kftrainerapi.TrainJob{
				*baseTrainJobBuilder.Clone().
					Label(constants.PrebuiltWorkloadLabel, "wl1").
					Label(kueue.MultiKueueOriginLabel, "origin1").
					JobsStatus(
						testingtrainjob.MakeJobStatusWrapper("replicated-job-1").
							Ready(1).
							Succeeded(1).
							Obj(),
						testingtrainjob.MakeJobStatusWrapper("replicated-job-2").
							Ready(3).
							Succeeded(0).
							Obj(),
					).
					Obj(),
			},
			operation: func(ctx context.Context, adapter *multiKueueAdapter, managerClient, workerClient client.Client) error {
				return adapter.DeleteRemoteObject(ctx, workerClient, types.NamespacedName{Name: "trainjob1", Namespace: TestNamespace})
			},
		},
		"missing trainjob is not considered managed": {
			operation: func(ctx context.Context, adapter *multiKueueAdapter, managerClient, workerClient client.Client) error {
				if isManged, _, _ := adapter.IsJobManagedByKueue(ctx, managerClient, types.NamespacedName{Name: "trainjob1", Namespace: TestNamespace}); isManged {
					return errors.New("expecting false")
				}
				return nil
			},
		},
		"job with wrong managedBy is not considered managed": {
			managersTrainJobs: []kftrainerapi.TrainJob{
				*baseTrainJobBuilder.Clone().Obj(),
			},
			operation: func(ctx context.Context, adapter *multiKueueAdapter, managerClient, workerClient client.Client) error {
				if isManged, _, _ := adapter.IsJobManagedByKueue(ctx, managerClient, types.NamespacedName{Name: "trainbjo1", Namespace: TestNamespace}); isManged {
					return errors.New("expecting false")
				}
				return nil
			},
			wantManagersTrainJobs: []kftrainerapi.TrainJob{
				*baseTrainJobBuilder.Clone().Obj(),
			},
		},

		"job managedBy multikueue": {
			managersTrainJobs: []kftrainerapi.TrainJob{
				*baseTrainJobManagedByKueueBuilder.Clone().Obj(),
			},
			operation: func(ctx context.Context, adapter *multiKueueAdapter, managerClient, workerClient client.Client) error {
				if isManged, _, _ := adapter.IsJobManagedByKueue(ctx, managerClient, types.NamespacedName{Name: "trainjob1", Namespace: TestNamespace}); !isManged {
					return errors.New("expecting true")
				}
				return nil
			},
			wantManagersTrainJobs: []kftrainerapi.TrainJob{
				*baseTrainJobManagedByKueueBuilder.Clone().Obj(),
			},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			managerBuilder := utiltesting.NewClientBuilder(kftrainerapi.AddToScheme).WithInterceptorFuncs(interceptor.Funcs{SubResourcePatch: utiltesting.TreatSSAAsStrategicMerge})
			managerBuilder = managerBuilder.WithLists(&kftrainerapi.TrainJobList{Items: tc.managersTrainJobs})
			managerBuilder = managerBuilder.WithStatusSubresource(slices.Map(tc.managersTrainJobs, func(w *kftrainerapi.TrainJob) client.Object { return w })...)
			managerClient := managerBuilder.Build()

			workerBuilder := utiltesting.NewClientBuilder(kftrainerapi.AddToScheme).WithInterceptorFuncs(interceptor.Funcs{SubResourcePatch: utiltesting.TreatSSAAsStrategicMerge})
			workerBuilder = workerBuilder.WithLists(&kftrainerapi.TrainJobList{Items: tc.workerTrainJobs})
			workerClient := workerBuilder.Build()

			ctx, _ := utiltesting.ContextWithLog(t)

			adapter := &multiKueueAdapter{}

			gotErr := tc.operation(ctx, adapter, managerClient, workerClient)

			if diff := cmp.Diff(tc.wantError, gotErr, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("unexpected error (-want/+got):\n%s", diff)
			}

			gotManagersTrainJobs := &kftrainerapi.TrainJobList{}
			if err := managerClient.List(ctx, gotManagersTrainJobs); err != nil {
				t.Errorf("unexpected list manager's trainjobs error %s", err)
			} else {
				if diff := cmp.Diff(tc.wantManagersTrainJobs, gotManagersTrainJobs.Items, objCheckOpts...); diff != "" {
					t.Errorf("unexpected manager's trainjobs (-want/+got):\n%s", diff)
				}
			}

			gotWorkerTrainJobs := &kftrainerapi.TrainJobList{}
			if err := workerClient.List(ctx, gotWorkerTrainJobs); err != nil {
				t.Errorf("unexpected list worker's trainjobs error %s", err)
			} else {
				if diff := cmp.Diff(tc.wantWorkerTrainJobs, gotWorkerTrainJobs.Items, objCheckOpts...); diff != "" {
					t.Errorf("unexpected worker's trainjobs (-want/+got):\n%s", diff)
				}
			}
		})
	}
}
