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

package leaderworkerset

import (
	"context"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	leaderworkersetv1 "sigs.k8s.io/lws/api/leaderworkerset/v1"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/util/slices"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingleaderworkerset "sigs.k8s.io/kueue/pkg/util/testingjobs/leaderworkerset"
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
		managersLeaderWorkerSets     []leaderworkersetv1.LeaderWorkerSet
		workerLeaderWorkerSets       []leaderworkersetv1.LeaderWorkerSet
		operation                    func(ctx context.Context, adapter *multiKueueAdapter, managerClient, workerClient client.Client) error
		wantError                    error
		wantManagersLeaderWorkerSets []leaderworkersetv1.LeaderWorkerSet
		wantWorkerLeaderWorkerSets   []leaderworkersetv1.LeaderWorkerSet
	}{
		"sync creates missing remote leaderworkerset with origin UID annotation": {
			managersLeaderWorkerSets: []leaderworkersetv1.LeaderWorkerSet{
				*utiltestingleaderworkerset.MakeLeaderWorkerSet("lws1", TestNamespace).UID("manager-uid-123").Obj(),
			},
			operation: func(ctx context.Context, adapter *multiKueueAdapter, managerClient, workerClient client.Client) error {
				return adapter.SyncJob(ctx, managerClient, workerClient, types.NamespacedName{Name: "lws1", Namespace: TestNamespace}, "wl1", "origin1")
			},
			wantManagersLeaderWorkerSets: []leaderworkersetv1.LeaderWorkerSet{
				*utiltestingleaderworkerset.MakeLeaderWorkerSet("lws1", TestNamespace).UID("manager-uid-123").Obj(),
			},
			wantWorkerLeaderWorkerSets: []leaderworkersetv1.LeaderWorkerSet{
				*utiltestingleaderworkerset.MakeLeaderWorkerSet("lws1", TestNamespace).
					Label(kueue.MultiKueueOriginLabel, "origin1").
					Annotation(kueue.MultiKueueOriginUIDAnnotation, "manager-uid-123").
					Obj(),
			},
		},
		"sync does not overwrite existing remote leaderworkerset": {
			managersLeaderWorkerSets: []leaderworkersetv1.LeaderWorkerSet{
				*utiltestingleaderworkerset.MakeLeaderWorkerSet("lws1", TestNamespace).UID("manager-uid-123").Obj(),
			},
			workerLeaderWorkerSets: []leaderworkersetv1.LeaderWorkerSet{
				*utiltestingleaderworkerset.MakeLeaderWorkerSet("lws1", TestNamespace).
					Label(kueue.MultiKueueOriginLabel, "origin1").
					Annotation(kueue.MultiKueueOriginUIDAnnotation, "manager-uid-123").
					ReadyReplicas(3).
					Obj(),
			},
			operation: func(ctx context.Context, adapter *multiKueueAdapter, managerClient, workerClient client.Client) error {
				return adapter.SyncJob(ctx, managerClient, workerClient, types.NamespacedName{Name: "lws1", Namespace: TestNamespace}, "wl1", "origin1")
			},
			wantManagersLeaderWorkerSets: []leaderworkersetv1.LeaderWorkerSet{
				*utiltestingleaderworkerset.MakeLeaderWorkerSet("lws1", TestNamespace).
					UID("manager-uid-123").
					Obj(),
			},
			wantWorkerLeaderWorkerSets: []leaderworkersetv1.LeaderWorkerSet{
				*utiltestingleaderworkerset.MakeLeaderWorkerSet("lws1", TestNamespace).
					Label(kueue.MultiKueueOriginLabel, "origin1").
					Annotation(kueue.MultiKueueOriginUIDAnnotation, "manager-uid-123").
					ReadyReplicas(3).
					Obj(),
			},
		},
		"remote leaderworkerset is deleted": {
			workerLeaderWorkerSets: []leaderworkersetv1.LeaderWorkerSet{
				*utiltestingleaderworkerset.MakeLeaderWorkerSet("lws1", TestNamespace).
					Label(kueue.MultiKueueOriginLabel, "origin1").
					Annotation(kueue.MultiKueueOriginUIDAnnotation, "manager-uid-123").
					ReadyReplicas(3).
					Obj(),
			},
			operation: func(ctx context.Context, adapter *multiKueueAdapter, managerClient, workerClient client.Client) error {
				return adapter.DeleteRemoteObject(ctx, workerClient, types.NamespacedName{Name: "lws1", Namespace: TestNamespace})
			},
		},
		"IsJobManagedByKueue returns true": {
			managersLeaderWorkerSets: []leaderworkersetv1.LeaderWorkerSet{
				*utiltestingleaderworkerset.MakeLeaderWorkerSet("lws1", TestNamespace).Obj(),
			},
			operation: func(ctx context.Context, adapter *multiKueueAdapter, managerClient, workerClient client.Client) error {
				isManaged, _, err := adapter.IsJobManagedByKueue(ctx, managerClient, types.NamespacedName{Name: "lws1", Namespace: TestNamespace})
				if err != nil {
					return err
				}
				if !isManaged {
					t.Error("expected IsJobManagedByKueue to return true")
				}
				return nil
			},
			wantManagersLeaderWorkerSets: []leaderworkersetv1.LeaderWorkerSet{
				*utiltestingleaderworkerset.MakeLeaderWorkerSet("lws1", TestNamespace).Obj(),
			},
		},
		"GetExpectedWorkloadCount returns replicas count": {
			managersLeaderWorkerSets: []leaderworkersetv1.LeaderWorkerSet{
				*utiltestingleaderworkerset.MakeLeaderWorkerSet("lws1", TestNamespace).Replicas(4).Obj(),
			},
			operation: func(ctx context.Context, adapter *multiKueueAdapter, managerClient, workerClient client.Client) error {
				count, err := adapter.GetExpectedWorkloadCount(ctx, managerClient, types.NamespacedName{Name: "lws1", Namespace: TestNamespace})
				if err != nil {
					return err
				}
				if count != 4 {
					t.Errorf("expected GetExpectedWorkloadCount to return 4, got %d", count)
				}
				return nil
			},
			wantManagersLeaderWorkerSets: []leaderworkersetv1.LeaderWorkerSet{
				*utiltestingleaderworkerset.MakeLeaderWorkerSet("lws1", TestNamespace).Replicas(4).Obj(),
			},
		},
		"GetExpectedWorkloadCount returns 1 for nil replicas": {
			managersLeaderWorkerSets: []leaderworkersetv1.LeaderWorkerSet{
				*utiltestingleaderworkerset.MakeLeaderWorkerSet("lws1", TestNamespace).Obj(),
			},
			operation: func(ctx context.Context, adapter *multiKueueAdapter, managerClient, workerClient client.Client) error {
				count, err := adapter.GetExpectedWorkloadCount(ctx, managerClient, types.NamespacedName{Name: "lws1", Namespace: TestNamespace})
				if err != nil {
					return err
				}
				if count != 1 {
					t.Errorf("expected GetExpectedWorkloadCount to return 1, got %d", count)
				}
				return nil
			},
			wantManagersLeaderWorkerSets: []leaderworkersetv1.LeaderWorkerSet{
				*utiltestingleaderworkerset.MakeLeaderWorkerSet("lws1", TestNamespace).Obj(),
			},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			managerBuilder := utiltesting.NewClientBuilder(leaderworkersetv1.AddToScheme).WithInterceptorFuncs(interceptor.Funcs{SubResourcePatch: utiltesting.TreatSSAAsStrategicMerge})
			managerBuilder = managerBuilder.WithLists(&leaderworkersetv1.LeaderWorkerSetList{Items: tc.managersLeaderWorkerSets})
			managerBuilder = managerBuilder.WithStatusSubresource(slices.Map(tc.managersLeaderWorkerSets, func(w *leaderworkersetv1.LeaderWorkerSet) client.Object { return w })...)
			managerClient := managerBuilder.Build()

			workerBuilder := utiltesting.NewClientBuilder(leaderworkersetv1.AddToScheme).WithInterceptorFuncs(interceptor.Funcs{SubResourcePatch: utiltesting.TreatSSAAsStrategicMerge})
			workerBuilder = workerBuilder.WithLists(&leaderworkersetv1.LeaderWorkerSetList{Items: tc.workerLeaderWorkerSets})
			workerClient := workerBuilder.Build()

			ctx, _ := utiltesting.ContextWithLog(t)

			adapter := &multiKueueAdapter{}

			gotErr := tc.operation(ctx, adapter, managerClient, workerClient)

			if diff := cmp.Diff(tc.wantError, gotErr, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("unexpected error (-want/+got):\n%s", diff)
			}

			gotManagersLeaderWorkerSets := &leaderworkersetv1.LeaderWorkerSetList{}
			if err := managerClient.List(ctx, gotManagersLeaderWorkerSets); err != nil {
				t.Fatalf("unexpected list manager's leaderworkersets error %s", err)
			}
			if diff := cmp.Diff(tc.wantManagersLeaderWorkerSets, gotManagersLeaderWorkerSets.Items, objCheckOpts...); diff != "" {
				t.Errorf("unexpected manager's leaderworkersets (-want/+got):\n%s", diff)
			}

			gotWorkerLeaderWorkerSets := &leaderworkersetv1.LeaderWorkerSetList{}
			if err := workerClient.List(ctx, gotWorkerLeaderWorkerSets); err != nil {
				t.Fatalf("unexpected list worker's leaderworkersets error %s", err)
			}
			if diff := cmp.Diff(tc.wantWorkerLeaderWorkerSets, gotWorkerLeaderWorkerSets.Items, objCheckOpts...); diff != "" {
				t.Errorf("unexpected worker's leaderworkersets (-want/+got):\n%s", diff)
			}
		})
	}
}

func TestGetWorkloadIndex(t *testing.T) {
	adapter := &multiKueueAdapter{}

	// Verify the adapter implements MultiKueueMultiWorkloadAdapter
	var _ jobframework.MultiKueueMultiWorkloadAdapter = adapter

	cases := map[string]struct {
		workload  *kueue.Workload
		wantIndex int
	}{
		"workload with index 0": {
			workload: &kueue.Workload{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						"kueue.x-k8s.io/component-workload-index": "0",
					},
				},
			},
			wantIndex: 0,
		},
		"workload with index 5": {
			workload: &kueue.Workload{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						"kueue.x-k8s.io/component-workload-index": "5",
					},
				},
			},
			wantIndex: 5,
		},
		"workload with index 10": {
			workload: &kueue.Workload{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						"kueue.x-k8s.io/component-workload-index": "10",
					},
				},
			},
			wantIndex: 10,
		},
		"workload with index 100": {
			workload: &kueue.Workload{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						"kueue.x-k8s.io/component-workload-index": "100",
					},
				},
			},
			wantIndex: 100,
		},
		"workload without annotation": {
			workload: &kueue.Workload{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{},
				},
			},
			wantIndex: -1,
		},
		"workload with nil annotations": {
			workload: &kueue.Workload{
				ObjectMeta: metav1.ObjectMeta{},
			},
			wantIndex: -1,
		},
		"workload with non-numeric index": {
			workload: &kueue.Workload{
				ObjectMeta: metav1.ObjectMeta{
					Annotations: map[string]string{
						"kueue.x-k8s.io/component-workload-index": "abc",
					},
				},
			},
			wantIndex: -1,
		},
		"nil workload": {
			workload:  nil,
			wantIndex: -1,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			gotIndex := adapter.GetWorkloadIndex(tc.workload)
			if gotIndex != tc.wantIndex {
				t.Errorf("GetWorkloadIndex() = %d, want %d", gotIndex, tc.wantIndex)
			}
		})
	}
}
