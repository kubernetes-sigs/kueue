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
	"k8s.io/component-base/featuregate"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
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
		featureGates             map[featuregate.Feature]bool
	}{
		"sync creates missing remote statefulset": {
			featureGates: map[featuregate.Feature]bool{features.WorkloadIdentifierAnnotations: false},
			managersStatefulSets: []appsv1.StatefulSet{
				*utiltestingstatefulset.MakeStatefulSet("statefulset1", TestNamespace).
					UID("manager-uid").
					Obj(),
			},
			operation: func(ctx context.Context, adapter *multiKueueAdapter, managerClient, workerClient client.Client) error {
				_, err := adapter.SyncJob(ctx, managerClient, workerClient, types.NamespacedName{Name: "statefulset1", Namespace: TestNamespace}, "wl1", "origin1")
				return err
			},
			wantManagersStatefulSets: []appsv1.StatefulSet{
				*utiltestingstatefulset.MakeStatefulSet("statefulset1", TestNamespace).
					UID("manager-uid").
					Obj(),
			},
			wantWorkerStatefulSets: []appsv1.StatefulSet{
				*utiltestingstatefulset.MakeStatefulSet("statefulset1", TestNamespace).
					PrebuiltWorkloadLabel("wl1").
					Label(kueue.MultiKueueOriginLabel, "origin1").
					Annotation(kueue.MultiKueueOriginUIDAnnotation, "manager-uid").
					Obj(),
			},
		},
		"sync status from remote statefulset": {
			featureGates: map[featuregate.Feature]bool{features.WorkloadIdentifierAnnotations: false},
			managersStatefulSets: []appsv1.StatefulSet{
				*utiltestingstatefulset.MakeStatefulSet("statefulset1", TestNamespace).Obj(),
			},
			workerStatefulSets: []appsv1.StatefulSet{
				*utiltestingstatefulset.MakeStatefulSet("statefulset1", TestNamespace).
					PrebuiltWorkloadLabel("wl1").
					Label(kueue.MultiKueueOriginLabel, "origin1").
					StatusReplicas(3).
					ReadyReplicas(2).
					Obj(),
			},
			operation: func(ctx context.Context, adapter *multiKueueAdapter, managerClient, workerClient client.Client) error {
				_, err := adapter.SyncJob(ctx, managerClient, workerClient, types.NamespacedName{Name: "statefulset1", Namespace: TestNamespace}, "wl1", "origin1")
				return err
			},
			wantManagersStatefulSets: []appsv1.StatefulSet{
				*utiltestingstatefulset.MakeStatefulSet("statefulset1", TestNamespace).
					Obj(),
			},
			wantWorkerStatefulSets: []appsv1.StatefulSet{
				*utiltestingstatefulset.MakeStatefulSet("statefulset1", TestNamespace).
					PrebuiltWorkloadLabel("wl1").
					Label(kueue.MultiKueueOriginLabel, "origin1").
					StatusReplicas(3).
					ReadyReplicas(2).
					Obj(),
			},
		},
		"remote statefulset is deleted": {
			featureGates: map[featuregate.Feature]bool{features.WorkloadIdentifierAnnotations: false},
			workerStatefulSets: []appsv1.StatefulSet{
				*utiltestingstatefulset.MakeStatefulSet("statefulset1", TestNamespace).
					PrebuiltWorkloadLabel("wl1").
					Label(kueue.MultiKueueOriginLabel, "origin1").
					StatusReplicas(3).
					ReadyReplicas(2).
					Obj(),
			},
			operation: func(ctx context.Context, adapter *multiKueueAdapter, managerClient, workerClient client.Client) error {
				return adapter.DeleteRemoteObject(ctx, managerClient, workerClient, types.NamespacedName{Name: "statefulset1", Namespace: TestNamespace})
			},
		},
		"IsJobManagedByKueue returns true": {
			featureGates: map[featuregate.Feature]bool{features.WorkloadIdentifierAnnotations: false},
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
		"sync creates missing remote statefulset, WorkloadIdentifierAnnotations enabled": {
			featureGates: map[featuregate.Feature]bool{features.WorkloadIdentifierAnnotations: true},
			managersStatefulSets: []appsv1.StatefulSet{
				*utiltestingstatefulset.MakeStatefulSet("statefulset1", TestNamespace).
					UID("manager-uid").
					Obj(),
			},
			operation: func(ctx context.Context, adapter *multiKueueAdapter, managerClient, workerClient client.Client) error {
				_, err := adapter.SyncJob(ctx, managerClient, workerClient, types.NamespacedName{Name: "statefulset1", Namespace: TestNamespace}, "wl1", "origin1")
				return err
			},
			wantManagersStatefulSets: []appsv1.StatefulSet{
				*utiltestingstatefulset.MakeStatefulSet("statefulset1", TestNamespace).
					UID("manager-uid").
					Obj(),
			},
			wantWorkerStatefulSets: []appsv1.StatefulSet{
				*utiltestingstatefulset.MakeStatefulSet("statefulset1", TestNamespace).
					PrebuiltWorkloadAnnotation("wl1").
					Label(kueue.MultiKueueOriginLabel, "origin1").
					Annotation(kueue.MultiKueueOriginUIDAnnotation, "manager-uid").
					Obj(),
			},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGatesDuringTest(t, tc.featureGates)
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

// TestWorkloadKeysFor verifies that WorkloadKeysFor returns the correct workload
// keys for the three meaningful cases: prebuilt label, plain local StatefulSet,
// and remote StatefulSet (MultiKueue worker) where the Workload is named from
// the origin UID stored in the annotation rather than from the object's own UID.
func TestWorkloadKeysFor(t *testing.T) {
	adapter := &multiKueueAdapter{}

	cases := map[string]struct {
		statefulSet *appsv1.StatefulSet
		wantKeys    []types.NamespacedName
	}{
		"prebuilt workload label returns that name directly": {
			statefulSet: utiltestingstatefulset.MakeStatefulSet("sts", TestNamespace).
				UID("local-uid").
				PrebuiltWorkloadLabel("my-prebuilt-wl").
				Obj(),
			wantKeys: []types.NamespacedName{
				{Name: "my-prebuilt-wl", Namespace: TestNamespace},
			},
		},
		"plain local StatefulSet uses its own UID": {
			statefulSet: utiltestingstatefulset.MakeStatefulSet("sts", TestNamespace).
				UID("local-uid").
				Obj(),
			wantKeys: []types.NamespacedName{
				{Name: GetWorkloadName("local-uid", "sts"), Namespace: TestNamespace},
				// legacy fallback (empty UID)
				{Name: GetWorkloadName("", "sts"), Namespace: TestNamespace},
			},
		},
		"remote StatefulSet uses origin UID annotation, not object UID": {
			// On a worker cluster MultiKueue stamps MultiKueueOriginLabel and
			// writes the manager UID into MultiKueueOriginUIDAnnotation.
			// The Workload is named from the origin UID, so the first key must
			// also be built from the annotation rather than from .UID.
			// Reverting to statefulSet.GetUID() must turn this case red.
			statefulSet: utiltestingstatefulset.MakeStatefulSet("sts", TestNamespace).
				UID("worker-local-uid").
				Label(kueue.MultiKueueOriginLabel, "manager").
				Annotation(kueue.MultiKueueOriginUIDAnnotation, "manager-origin-uid").
				Obj(),
			wantKeys: []types.NamespacedName{
				{Name: GetWorkloadName("manager-origin-uid", "sts"), Namespace: TestNamespace},
				// legacy fallback (empty UID)
				{Name: GetWorkloadName("", "sts"), Namespace: TestNamespace},
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := adapter.WorkloadKeysFor(tc.statefulSet)
			if err != nil {
				t.Fatalf("WorkloadKeysFor returned unexpected error: %v", err)
			}
			if diff := cmp.Diff(tc.wantKeys, got); diff != "" {
				t.Errorf("WorkloadKeysFor keys mismatch (-want/+got):\n%s", diff)
			}
		})
	}
}
