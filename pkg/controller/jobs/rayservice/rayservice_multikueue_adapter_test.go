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

package rayservice

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

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/controller/jobs/ray"
	"sigs.k8s.io/kueue/pkg/util/slices"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingrayservice "sigs.k8s.io/kueue/pkg/util/testingjobs/rayservice"
)

const (
	TestNamespace = "ns"
)

func TestMultiKueueAdapter(t *testing.T) {
	objCheckOpts := cmp.Options{
		cmpopts.IgnoreFields(metav1.ObjectMeta{}, "ResourceVersion"),
		cmpopts.EquateEmpty(),
	}

	rayServiceBuilder := utiltestingrayservice.MakeService("rayservice1", TestNamespace).Suspend(false)

	cases := map[string]struct {
		managersRayServices []rayv1.RayService
		workerRayServices   []rayv1.RayService

		operation func(ctx context.Context, adapter jobframework.MultiKueueAdapter, managerClient, workerClient client.Client) error

		wantError               error
		wantManagersRayServices []rayv1.RayService
		wantWorkerRayServices   []rayv1.RayService
	}{
		"sync creates missing remote rayservice": {
			managersRayServices: []rayv1.RayService{
				*rayServiceBuilder.DeepCopy(),
			},
			operation: func(ctx context.Context, adapter jobframework.MultiKueueAdapter, managerClient, workerClient client.Client) error {
				return adapter.SyncJob(ctx, managerClient, workerClient, types.NamespacedName{Name: "rayservice1", Namespace: TestNamespace}, "wl1", "origin1")
			},

			wantManagersRayServices: []rayv1.RayService{
				*rayServiceBuilder.DeepCopy(),
			},
			wantWorkerRayServices: []rayv1.RayService{
				*rayServiceBuilder.Clone().
					PrebuiltWorkloadLabel("wl1").
					Label(kueue.MultiKueueOriginLabel, "origin1").
					Obj(),
			},
		},
		"sync status from remote rayservice": {
			managersRayServices: []rayv1.RayService{
				*rayServiceBuilder.DeepCopy(),
			},
			workerRayServices: []rayv1.RayService{
				*rayServiceBuilder.Clone().
					PrebuiltWorkloadLabel("wl1").
					Label(kueue.MultiKueueOriginLabel, "origin1").
					StatusConditions(metav1.Condition{
						Type:   string(rayv1.RayServiceReady),
						Status: metav1.ConditionTrue,
						Reason: string(rayv1.NonZeroServeEndpoints),
					}).
					Obj(),
			},
			operation: func(ctx context.Context, adapter jobframework.MultiKueueAdapter, managerClient, workerClient client.Client) error {
				return adapter.SyncJob(ctx, managerClient, workerClient, types.NamespacedName{Name: "rayservice1", Namespace: TestNamespace}, "wl1", "origin1")
			},

			wantManagersRayServices: []rayv1.RayService{
				*rayServiceBuilder.Clone().
					StatusConditions(metav1.Condition{
						Type:   string(rayv1.RayServiceReady),
						Status: metav1.ConditionTrue,
						Reason: string(rayv1.NonZeroServeEndpoints),
					}).
					Obj(),
			},
			wantWorkerRayServices: []rayv1.RayService{
				*rayServiceBuilder.Clone().
					PrebuiltWorkloadLabel("wl1").
					Label(kueue.MultiKueueOriginLabel, "origin1").
					StatusConditions(metav1.Condition{
						Type:   string(rayv1.RayServiceReady),
						Status: metav1.ConditionTrue,
						Reason: string(rayv1.NonZeroServeEndpoints),
					}).
					Obj(),
			},
		},
		"sync status from remote while local rayservice is suspended": {
			managersRayServices: []rayv1.RayService{
				*rayServiceBuilder.Clone().
					Suspend(true).
					Obj(),
			},
			workerRayServices: []rayv1.RayService{
				*rayServiceBuilder.Clone().
					PrebuiltWorkloadLabel("wl1").
					Label(kueue.MultiKueueOriginLabel, "origin1").
					Suspend(true).
					StatusConditions(metav1.Condition{
						Type:   string(rayv1.RayServiceReady),
						Status: metav1.ConditionTrue,
						Reason: string(rayv1.NonZeroServeEndpoints),
					}).
					Obj(),
			},
			operation: func(ctx context.Context, adapter jobframework.MultiKueueAdapter, managerClient, workerClient client.Client) error {
				return adapter.SyncJob(ctx, managerClient, workerClient, types.NamespacedName{Name: "rayservice1", Namespace: TestNamespace}, "wl1", "origin1")
			},
			wantManagersRayServices: []rayv1.RayService{
				*rayServiceBuilder.Clone().
					Suspend(true).
					StatusConditions(metav1.Condition{
						Type:   string(rayv1.RayServiceReady),
						Status: metav1.ConditionTrue,
						Reason: string(rayv1.NonZeroServeEndpoints),
					}).
					Obj(),
			},
			wantWorkerRayServices: []rayv1.RayService{
				*rayServiceBuilder.Clone().
					PrebuiltWorkloadLabel("wl1").
					Label(kueue.MultiKueueOriginLabel, "origin1").
					Suspend(true).
					StatusConditions(metav1.Condition{
						Type:   string(rayv1.RayServiceReady),
						Status: metav1.ConditionTrue,
						Reason: string(rayv1.NonZeroServeEndpoints),
					}).
					Obj(),
			},
		},
		"remote rayservice is deleted": {
			workerRayServices: []rayv1.RayService{
				*rayServiceBuilder.Clone().
					PrebuiltWorkloadLabel("wl1").
					Label(kueue.MultiKueueOriginLabel, "origin1").
					Obj(),
			},
			operation: func(ctx context.Context, adapter jobframework.MultiKueueAdapter, managerClient, workerClient client.Client) error {
				return adapter.DeleteRemoteObject(ctx, workerClient, types.NamespacedName{Name: "rayservice1", Namespace: TestNamespace})
			},
		},
		"job with wrong managedBy is not considered managed": {
			managersRayServices: []rayv1.RayService{
				*rayServiceBuilder.Clone().
					ManagedBy("some-other-controller").
					Obj(),
			},
			operation: func(ctx context.Context, adapter jobframework.MultiKueueAdapter, managerClient, workerClient client.Client) error {
				if isManged, _, _ := adapter.IsJobManagedByKueue(ctx, managerClient, types.NamespacedName{Name: "rayservice1", Namespace: TestNamespace}); isManged {
					return errors.New("expecting false")
				}
				return nil
			},
			wantManagersRayServices: []rayv1.RayService{
				*rayServiceBuilder.Clone().
					ManagedBy("some-other-controller").
					Obj(),
			},
		},

		"job managedBy multikueue": {
			managersRayServices: []rayv1.RayService{
				*rayServiceBuilder.Clone().
					ManagedBy(kueue.MultiKueueControllerName).
					Obj(),
			},
			operation: func(ctx context.Context, adapter jobframework.MultiKueueAdapter, managerClient, workerClient client.Client) error {
				if isManged, _, _ := adapter.IsJobManagedByKueue(ctx, managerClient, types.NamespacedName{Name: "rayservice1", Namespace: TestNamespace}); !isManged {
					return errors.New("expecting true")
				}
				return nil
			},
			wantManagersRayServices: []rayv1.RayService{
				*rayServiceBuilder.Clone().
					ManagedBy(kueue.MultiKueueControllerName).
					Obj(),
			},
		},
		"missing job is not considered managed": {
			operation: func(ctx context.Context, adapter jobframework.MultiKueueAdapter, managerClient, workerClient client.Client) error {
				if isManged, _, _ := adapter.IsJobManagedByKueue(ctx, managerClient, types.NamespacedName{Name: "rayservice1", Namespace: TestNamespace}); isManged {
					return errors.New("expecting false")
				}
				return nil
			},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			managerBuilder := utiltesting.NewClientBuilder(rayv1.AddToScheme).WithInterceptorFuncs(interceptor.Funcs{SubResourcePatch: utiltesting.TreatSSAAsStrategicMerge})
			managerBuilder = managerBuilder.WithLists(&rayv1.RayServiceList{Items: tc.managersRayServices})
			managerBuilder = managerBuilder.WithStatusSubresource(slices.Map(tc.managersRayServices, func(w *rayv1.RayService) client.Object { return w })...)
			managerClient := managerBuilder.Build()

			workerBuilder := utiltesting.NewClientBuilder(rayv1.AddToScheme).WithInterceptorFuncs(interceptor.Funcs{SubResourcePatch: utiltesting.TreatSSAAsStrategicMerge})
			workerBuilder = workerBuilder.WithLists(&rayv1.RayServiceList{Items: tc.workerRayServices})
			workerClient := workerBuilder.Build()

			ctx, _ := utiltesting.ContextWithLog(t)

			adapter := ray.NewMKAdapter(copyJobSpec, copyJobStatus, getEmptyList, gvk, getManagedBy, setManagedBy)

			gotErr := tc.operation(ctx, adapter, managerClient, workerClient)

			if diff := cmp.Diff(tc.wantError, gotErr, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("unexpected error (-want/+got):\n%s", diff)
			}

			gotManagersRayServices := &rayv1.RayServiceList{}
			if err := managerClient.List(ctx, gotManagersRayServices); err != nil {
				t.Errorf("unexpected list manager's rayservices error %s", err)
			} else {
				if diff := cmp.Diff(tc.wantManagersRayServices, gotManagersRayServices.Items, objCheckOpts...); diff != "" {
					t.Errorf("unexpected manager's rayservices (-want/+got):\n%s", diff)
				}
			}

			gotWorkerRayServices := &rayv1.RayServiceList{}
			if err := workerClient.List(ctx, gotWorkerRayServices); err != nil {
				t.Errorf("unexpected list worker's rayservices error %s", err)
			} else {
				if diff := cmp.Diff(tc.wantWorkerRayServices, gotWorkerRayServices.Items, objCheckOpts...); diff != "" {
					t.Errorf("unexpected worker's rayservices (-want/+got):\n%s", diff)
				}
			}
		})
	}
}
