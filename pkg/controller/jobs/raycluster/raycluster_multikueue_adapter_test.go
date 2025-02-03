/*
Copyright 2025 The Kubernetes Authors.

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

package raycluster

import (
	"context"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	rayv1 "github.com/ray-project/kuberay/ray-operator/apis/ray/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/util/slices"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingraycluster "sigs.k8s.io/kueue/pkg/util/testingjobs/raycluster"
)

const (
	TestNamespace = "ns"
)

func TestMultiKueueAdapter(t *testing.T) {
	objCheckOpts := []cmp.Option{
		cmpopts.IgnoreFields(metav1.ObjectMeta{}, "ResourceVersion"),
		cmpopts.EquateEmpty(),
	}

	rayClusterBuilder := utiltestingraycluster.MakeCluster("raycluster1", TestNamespace).Suspend(false)

	cases := map[string]struct {
		managersRayClusters []rayv1.RayCluster
		workerRayClusters   []rayv1.RayCluster

		operation func(ctx context.Context, adapter *multikueueAdapter, managerClient, workerClient client.Client) error

		wantError               error
		wantManagersRayClusters []rayv1.RayCluster
		wantWorkerRayClusters   []rayv1.RayCluster
	}{
		"sync creates missing remote raycluster": {
			managersRayClusters: []rayv1.RayCluster{
				*rayClusterBuilder.DeepCopy(),
			},
			operation: func(ctx context.Context, adapter *multikueueAdapter, managerClient, workerClient client.Client) error {
				return adapter.SyncJob(ctx, managerClient, workerClient, types.NamespacedName{Name: "raycluster1", Namespace: TestNamespace}, "wl1", "origin1")
			},

			wantManagersRayClusters: []rayv1.RayCluster{
				*rayClusterBuilder.DeepCopy(),
			},
			wantWorkerRayClusters: []rayv1.RayCluster{
				*rayClusterBuilder.Clone().
					Label(constants.PrebuiltWorkloadLabel, "wl1").
					Label(kueue.MultiKueueOriginLabel, "origin1").
					Obj(),
			},
		},
		"sync status from remote raycluster": {
			managersRayClusters: []rayv1.RayCluster{
				*rayClusterBuilder.DeepCopy(),
			},
			workerRayClusters: []rayv1.RayCluster{
				*rayClusterBuilder.Clone().
					Label(constants.PrebuiltWorkloadLabel, "wl1").
					Label(kueue.MultiKueueOriginLabel, "origin1").
					StatusConditions(metav1.Condition{Type: string(rayv1.HeadPodReady), Status: metav1.ConditionStatus(corev1.ConditionTrue)}).
					Obj(),
			},
			operation: func(ctx context.Context, adapter *multikueueAdapter, managerClient, workerClient client.Client) error {
				return adapter.SyncJob(ctx, managerClient, workerClient, types.NamespacedName{Name: "raycluster1", Namespace: TestNamespace}, "wl1", "origin1")
			},

			wantManagersRayClusters: []rayv1.RayCluster{
				*rayClusterBuilder.Clone().
					StatusConditions(metav1.Condition{Type: string(rayv1.HeadPodReady), Status: metav1.ConditionStatus(corev1.ConditionTrue)}).
					Obj(),
			},
			wantWorkerRayClusters: []rayv1.RayCluster{
				*rayClusterBuilder.Clone().
					Label(constants.PrebuiltWorkloadLabel, "wl1").
					Label(kueue.MultiKueueOriginLabel, "origin1").
					StatusConditions(metav1.Condition{Type: string(rayv1.HeadPodReady), Status: metav1.ConditionStatus(corev1.ConditionTrue)}).
					Obj(),
			},
		},
		"remote raycluster is deleted": {
			workerRayClusters: []rayv1.RayCluster{
				*rayClusterBuilder.Clone().
					Label(constants.PrebuiltWorkloadLabel, "wl1").
					Label(kueue.MultiKueueOriginLabel, "origin1").
					Obj(),
			},
			operation: func(ctx context.Context, adapter *multikueueAdapter, managerClient, workerClient client.Client) error {
				return adapter.DeleteRemoteObject(ctx, workerClient, types.NamespacedName{Name: "raycluster1", Namespace: TestNamespace})
			},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			managerBuilder := utiltesting.NewClientBuilder(rayv1.AddToScheme).WithInterceptorFuncs(interceptor.Funcs{SubResourcePatch: utiltesting.TreatSSAAsStrategicMerge})
			managerBuilder = managerBuilder.WithLists(&rayv1.RayClusterList{Items: tc.managersRayClusters})
			managerBuilder = managerBuilder.WithStatusSubresource(slices.Map(tc.managersRayClusters, func(w *rayv1.RayCluster) client.Object { return w })...)
			managerClient := managerBuilder.Build()

			workerBuilder := utiltesting.NewClientBuilder(rayv1.AddToScheme).WithInterceptorFuncs(interceptor.Funcs{SubResourcePatch: utiltesting.TreatSSAAsStrategicMerge})
			workerBuilder = workerBuilder.WithLists(&rayv1.RayClusterList{Items: tc.workerRayClusters})
			workerClient := workerBuilder.Build()

			ctx, _ := utiltesting.ContextWithLog(t)

			adapter := &multikueueAdapter{}

			gotErr := tc.operation(ctx, adapter, managerClient, workerClient)

			if diff := cmp.Diff(tc.wantError, gotErr, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("unexpected error (-want/+got):\n%s", diff)
			}

			gotManagersRayClusters := &rayv1.RayClusterList{}
			if err := managerClient.List(ctx, gotManagersRayClusters); err != nil {
				t.Errorf("unexpected list manager's rayclusters error %s", err)
			} else {
				if diff := cmp.Diff(tc.wantManagersRayClusters, gotManagersRayClusters.Items, objCheckOpts...); diff != "" {
					t.Errorf("unexpected manager's rayclusters (-want/+got):\n%s", diff)
				}
			}

			gotWorkerRayClusters := &rayv1.RayClusterList{}
			if err := workerClient.List(ctx, gotWorkerRayClusters); err != nil {
				t.Errorf("unexpected list worker's rayclusters error %s", err)
			} else {
				if diff := cmp.Diff(tc.wantWorkerRayClusters, gotWorkerRayClusters.Items, objCheckOpts...); diff != "" {
					t.Errorf("unexpected worker's rayclusters (-want/+got):\n%s", diff)
				}
			}
		})
	}
}
