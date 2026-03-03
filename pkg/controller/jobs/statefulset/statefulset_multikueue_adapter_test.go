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

package statefulset

import (
	"context"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	appsv1 "k8s.io/api/apps/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/util/slices"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingstatefulset "sigs.k8s.io/kueue/pkg/util/testingjobs/statefulset"
)

const (
	TestNamespace = "ns"
)

func TestMultiKueueAdapter(t *testing.T) {
	objCheckOpts := cmp.Options{
		cmpopts.IgnoreFields(metav1.ObjectMeta{}, "ResourceVersion"),
		cmpopts.EquateEmpty(),
	}

	cases := map[string]struct {
		managersStatefulSets     []appsv1.StatefulSet
		workerStatefulSets       []appsv1.StatefulSet
		operation                func(ctx context.Context, adapter *multiKueueAdapter, managerClient, workerClient client.Client) error
		wantError                error
		wantManagersStatefulSets []appsv1.StatefulSet
		wantWorkerStatefulSets   []appsv1.StatefulSet
	}{
		"sync creates missing remote statefulset": {
			managersStatefulSets: []appsv1.StatefulSet{
				*utiltestingstatefulset.MakeStatefulSet("statefulset1", TestNamespace).Obj(),
			},
			operation: func(ctx context.Context, adapter *multiKueueAdapter, managerClient, workerClient client.Client) error {
				return adapter.SyncJob(ctx, managerClient, workerClient, types.NamespacedName{Name: "statefulset1", Namespace: TestNamespace}, "wl1", "origin1")
			},
			wantManagersStatefulSets: []appsv1.StatefulSet{
				*utiltestingstatefulset.MakeStatefulSet("statefulset1", TestNamespace).Obj(),
			},
			wantWorkerStatefulSets: []appsv1.StatefulSet{
				*utiltestingstatefulset.MakeStatefulSet("statefulset1", TestNamespace).
					Label(constants.PrebuiltWorkloadLabel, "wl1").
					Label(kueue.MultiKueueOriginLabel, "origin1").
					Obj(),
			},
		},
		"sync status from remote statefulset": {
			managersStatefulSets: []appsv1.StatefulSet{
				*utiltestingstatefulset.MakeStatefulSet("statefulset1", TestNamespace).Obj(),
			},
			workerStatefulSets: []appsv1.StatefulSet{
				*utiltestingstatefulset.MakeStatefulSet("statefulset1", TestNamespace).
					Label(constants.PrebuiltWorkloadLabel, "wl1").
					Label(kueue.MultiKueueOriginLabel, "origin1").
					StatusReplicas(3).
					ReadyReplicas(2).
					Obj(),
			},
			operation: func(ctx context.Context, adapter *multiKueueAdapter, managerClient, workerClient client.Client) error {
				return adapter.SyncJob(ctx, managerClient, workerClient, types.NamespacedName{Name: "statefulset1", Namespace: TestNamespace}, "wl1", "origin1")
			},
			wantManagersStatefulSets: []appsv1.StatefulSet{
				*utiltestingstatefulset.MakeStatefulSet("statefulset1", TestNamespace).
					Obj(),
			},
			wantWorkerStatefulSets: []appsv1.StatefulSet{
				*utiltestingstatefulset.MakeStatefulSet("statefulset1", TestNamespace).
					Label(constants.PrebuiltWorkloadLabel, "wl1").
					Label(kueue.MultiKueueOriginLabel, "origin1").
					StatusReplicas(3).
					ReadyReplicas(2).
					Obj(),
			},
		},
		"remote statefulset is deleted": {
			workerStatefulSets: []appsv1.StatefulSet{
				*utiltestingstatefulset.MakeStatefulSet("statefulset1", TestNamespace).
					Label(constants.PrebuiltWorkloadLabel, "wl1").
					Label(kueue.MultiKueueOriginLabel, "origin1").
					StatusReplicas(3).
					ReadyReplicas(2).
					Obj(),
			},
			operation: func(ctx context.Context, adapter *multiKueueAdapter, managerClient, workerClient client.Client) error {
				return adapter.DeleteRemoteObject(ctx, workerClient, types.NamespacedName{Name: "statefulset1", Namespace: TestNamespace})
			},
		},
		"IsJobManagedByKueue returns true": {
			managersStatefulSets: []appsv1.StatefulSet{
				*utiltestingstatefulset.MakeStatefulSet("statefulset1", TestNamespace).Obj(),
			},
			operation: func(ctx context.Context, adapter *multiKueueAdapter, managerClient, workerClient client.Client) error {
				isManaged, _, err := adapter.IsJobManagedByKueue(ctx, managerClient, types.NamespacedName{Name: "statefulset1", Namespace: TestNamespace})
				if err != nil {
					return err
				}
				if !isManaged {
					t.Error("expected IsJobManagedByKueue to return true")
				}
				return nil
			},
			wantManagersStatefulSets: []appsv1.StatefulSet{
				*utiltestingstatefulset.MakeStatefulSet("statefulset1", TestNamespace).Obj(),
			},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			managerBuilder := utiltesting.NewClientBuilder(appsv1.AddToScheme).WithInterceptorFuncs(interceptor.Funcs{SubResourcePatch: utiltesting.TreatSSAAsStrategicMerge})
			managerBuilder = managerBuilder.WithLists(&appsv1.StatefulSetList{Items: tc.managersStatefulSets})
			managerBuilder = managerBuilder.WithStatusSubresource(slices.Map(tc.managersStatefulSets, func(w *appsv1.StatefulSet) client.Object { return w })...)
			managerClient := managerBuilder.Build()

			workerBuilder := utiltesting.NewClientBuilder(appsv1.AddToScheme).WithInterceptorFuncs(interceptor.Funcs{SubResourcePatch: utiltesting.TreatSSAAsStrategicMerge})
			workerBuilder = workerBuilder.WithLists(&appsv1.StatefulSetList{Items: tc.workerStatefulSets})
			workerClient := workerBuilder.Build()

			ctx, _ := utiltesting.ContextWithLog(t)

			adapter := &multiKueueAdapter{}

			gotErr := tc.operation(ctx, adapter, managerClient, workerClient)

			if diff := cmp.Diff(tc.wantError, gotErr, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("unexpected error (-want/+got):\n%s", diff)
			}

			gotManagersStatefulSets := &appsv1.StatefulSetList{}
			if err := managerClient.List(ctx, gotManagersStatefulSets); err != nil {
				t.Fatalf("unexpected list manager's statefulsets error %s", err)
			}
			if diff := cmp.Diff(tc.wantManagersStatefulSets, gotManagersStatefulSets.Items, objCheckOpts...); diff != "" {
				t.Errorf("unexpected manager's statefulsets (-want/+got):\n%s", diff)
			}

			gotWorkerStatefulSets := &appsv1.StatefulSetList{}
			if err := workerClient.List(ctx, gotWorkerStatefulSets); err != nil {
				t.Fatalf("unexpected list worker's statefulsets error %s", err)
			}
			if diff := cmp.Diff(tc.wantWorkerStatefulSets, gotWorkerStatefulSets.Items, objCheckOpts...); diff != "" {
				t.Errorf("unexpected worker's statefulsets (-want/+got):\n%s", diff)
			}
		})
	}
}
