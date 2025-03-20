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

package rayjob

import (
	"context"
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	rayv1 "github.com/ray-project/kuberay/ray-operator/apis/ray/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/util/slices"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingrayjob "sigs.k8s.io/kueue/pkg/util/testingjobs/rayjob"
)

const (
	TestNamespace = "ns"
)

func TestMultiKueueAdapter(t *testing.T) {
	objCheckOpts := cmp.Options{
		cmpopts.IgnoreFields(metav1.ObjectMeta{}, "ResourceVersion"),
		cmpopts.EquateEmpty(),
	}

	rayJobBuilder := utiltestingrayjob.MakeJob("rayjob1", TestNamespace).Suspend(false)

	cases := map[string]struct {
		managersRayJobs []rayv1.RayJob
		workerRayJobs   []rayv1.RayJob

		operation func(ctx context.Context, adapter *multiKueueAdapter, managerClient, workerClient client.Client) error

		wantError           error
		wantManagersRayJobs []rayv1.RayJob
		wantWorkerRayJobs   []rayv1.RayJob
	}{
		"sync creates missing remote rayjob": {
			managersRayJobs: []rayv1.RayJob{
				*rayJobBuilder.DeepCopy(),
			},
			operation: func(ctx context.Context, adapter *multiKueueAdapter, managerClient, workerClient client.Client) error {
				return adapter.SyncJob(ctx, managerClient, workerClient, types.NamespacedName{Name: "rayjob1", Namespace: TestNamespace}, "wl1", "origin1")
			},

			wantManagersRayJobs: []rayv1.RayJob{
				*rayJobBuilder.DeepCopy(),
			},
			wantWorkerRayJobs: []rayv1.RayJob{
				*rayJobBuilder.Clone().
					Label(constants.PrebuiltWorkloadLabel, "wl1").
					Label(kueue.MultiKueueOriginLabel, "origin1").
					Obj(),
			},
		},
		"sync status from remote rayjob": {
			managersRayJobs: []rayv1.RayJob{
				*rayJobBuilder.DeepCopy(),
			},
			workerRayJobs: []rayv1.RayJob{
				*rayJobBuilder.Clone().
					Label(constants.PrebuiltWorkloadLabel, "wl1").
					Label(kueue.MultiKueueOriginLabel, "origin1").
					JobDeploymentStatus(rayv1.JobDeploymentStatusComplete).
					Obj(),
			},
			operation: func(ctx context.Context, adapter *multiKueueAdapter, managerClient, workerClient client.Client) error {
				return adapter.SyncJob(ctx, managerClient, workerClient, types.NamespacedName{Name: "rayjob1", Namespace: TestNamespace}, "wl1", "origin1")
			},

			wantManagersRayJobs: []rayv1.RayJob{
				*rayJobBuilder.Clone().
					JobDeploymentStatus(rayv1.JobDeploymentStatusComplete).
					Obj(),
			},
			wantWorkerRayJobs: []rayv1.RayJob{
				*rayJobBuilder.Clone().
					Label(constants.PrebuiltWorkloadLabel, "wl1").
					Label(kueue.MultiKueueOriginLabel, "origin1").
					JobDeploymentStatus(rayv1.JobDeploymentStatusComplete).
					Obj(),
			},
		},
		"skip to sync status from remote suspended rayjob": {
			managersRayJobs: []rayv1.RayJob{
				*rayJobBuilder.Clone().
					Suspend(true).
					Obj(),
			},
			workerRayJobs: []rayv1.RayJob{
				*rayJobBuilder.Clone().
					Label(constants.PrebuiltWorkloadLabel, "wl1").
					Label(kueue.MultiKueueOriginLabel, "origin1").
					Suspend(true).
					JobDeploymentStatus(rayv1.JobDeploymentStatusComplete).
					Obj(),
			},
			operation: func(ctx context.Context, adapter *multiKueueAdapter, managerClient, workerClient client.Client) error {
				return adapter.SyncJob(ctx, managerClient, workerClient, types.NamespacedName{Name: "rayjob1", Namespace: TestNamespace}, "wl1", "origin1")
			},
			wantManagersRayJobs: []rayv1.RayJob{
				*rayJobBuilder.Clone().
					Suspend(true).
					Obj(),
			},
			wantWorkerRayJobs: []rayv1.RayJob{
				*rayJobBuilder.Clone().
					Label(constants.PrebuiltWorkloadLabel, "wl1").
					Label(kueue.MultiKueueOriginLabel, "origin1").
					Suspend(true).
					JobDeploymentStatus(rayv1.JobDeploymentStatusComplete).
					Obj(),
			},
		},
		"remote rayjob is deleted": {
			workerRayJobs: []rayv1.RayJob{
				*rayJobBuilder.Clone().
					Label(constants.PrebuiltWorkloadLabel, "wl1").
					Label(kueue.MultiKueueOriginLabel, "origin1").
					Obj(),
			},
			operation: func(ctx context.Context, adapter *multiKueueAdapter, managerClient, workerClient client.Client) error {
				return adapter.DeleteRemoteObject(ctx, workerClient, types.NamespacedName{Name: "rayjob1", Namespace: TestNamespace})
			},
		},
		"job with wrong managedBy is not considered managed": {
			managersRayJobs: []rayv1.RayJob{
				*rayJobBuilder.Clone().
					ManagedBy("some-other-controller").
					Obj(),
			},
			operation: func(ctx context.Context, adapter *multiKueueAdapter, managerClient, workerClient client.Client) error {
				if isManged, _, _ := adapter.IsJobManagedByKueue(ctx, managerClient, types.NamespacedName{Name: "rayjob1", Namespace: TestNamespace}); isManged {
					return errors.New("expecting false")
				}
				return nil
			},
			wantManagersRayJobs: []rayv1.RayJob{
				*rayJobBuilder.Clone().
					ManagedBy("some-other-controller").
					Obj(),
			},
		},

		"job managedBy multikueue": {
			managersRayJobs: []rayv1.RayJob{
				*rayJobBuilder.Clone().
					ManagedBy(kueue.MultiKueueControllerName).
					Obj(),
			},
			operation: func(ctx context.Context, adapter *multiKueueAdapter, managerClient, workerClient client.Client) error {
				if isManged, _, _ := adapter.IsJobManagedByKueue(ctx, managerClient, types.NamespacedName{Name: "rayjob1", Namespace: TestNamespace}); !isManged {
					return errors.New("expecting true")
				}
				return nil
			},
			wantManagersRayJobs: []rayv1.RayJob{
				*rayJobBuilder.Clone().
					ManagedBy(kueue.MultiKueueControllerName).
					Obj(),
			},
		},
		"missing job is not considered managed": {
			operation: func(ctx context.Context, adapter *multiKueueAdapter, managerClient, workerClient client.Client) error {
				if isManged, _, _ := adapter.IsJobManagedByKueue(ctx, managerClient, types.NamespacedName{Name: "rayjob1", Namespace: TestNamespace}); isManged {
					return errors.New("expecting false")
				}
				return nil
			},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			managerBuilder := utiltesting.NewClientBuilder(rayv1.AddToScheme).WithInterceptorFuncs(interceptor.Funcs{SubResourcePatch: utiltesting.TreatSSAAsStrategicMerge})
			managerBuilder = managerBuilder.WithLists(&rayv1.RayJobList{Items: tc.managersRayJobs})
			managerBuilder = managerBuilder.WithStatusSubresource(slices.Map(tc.managersRayJobs, func(w *rayv1.RayJob) client.Object { return w })...)
			managerClient := managerBuilder.Build()

			workerBuilder := utiltesting.NewClientBuilder(rayv1.AddToScheme).WithInterceptorFuncs(interceptor.Funcs{SubResourcePatch: utiltesting.TreatSSAAsStrategicMerge})
			workerBuilder = workerBuilder.WithLists(&rayv1.RayJobList{Items: tc.workerRayJobs})
			workerClient := workerBuilder.Build()

			ctx, _ := utiltesting.ContextWithLog(t)

			adapter := &multiKueueAdapter{}

			gotErr := tc.operation(ctx, adapter, managerClient, workerClient)

			if diff := cmp.Diff(tc.wantError, gotErr, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("unexpected error (-want/+got):\n%s", diff)
			}

			gotManagersRayJobs := &rayv1.RayJobList{}
			if err := managerClient.List(ctx, gotManagersRayJobs); err != nil {
				t.Errorf("unexpected list manager's rayjobs error %s", err)
			} else {
				if diff := cmp.Diff(tc.wantManagersRayJobs, gotManagersRayJobs.Items, objCheckOpts...); diff != "" {
					t.Errorf("unexpected manager's rayjobs (-want/+got):\n%s", diff)
				}
			}

			gotWorkerRayJobs := &rayv1.RayJobList{}
			if err := workerClient.List(ctx, gotWorkerRayJobs); err != nil {
				t.Errorf("unexpected list worker's rayjobs error %s", err)
			} else {
				if diff := cmp.Diff(tc.wantWorkerRayJobs, gotWorkerRayJobs.Items, objCheckOpts...); diff != "" {
					t.Errorf("unexpected worker's rayjobs (-want/+got):\n%s", diff)
				}
			}
		})
	}
}
