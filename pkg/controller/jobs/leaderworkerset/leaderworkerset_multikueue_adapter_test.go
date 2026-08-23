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
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/component-base/featuregate"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"
	leaderworkersetv1 "sigs.k8s.io/lws/api/leaderworkerset/v1"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/util/slices"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingleaderworkerset "sigs.k8s.io/kueue/pkg/util/testingjobs/leaderworkerset"
)

const (
	TestNamespace = "ns"
)

func makeLWSBoundWorkload(name string, key types.NamespacedName, managerUID types.UID) *kueue.Workload {
	return &kueue.Workload{ObjectMeta: metav1.ObjectMeta{
		Name:      name,
		Namespace: key.Namespace,
		OwnerReferences: []metav1.OwnerReference{{
			APIVersion: gvk.GroupVersion().String(),
			Kind:       gvk.Kind,
			Name:       key.Name,
			UID:        managerUID,
		}},
	}}
}

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
		featureGates                 map[featuregate.Feature]bool
	}{
		"sync creates missing remote leaderworkerset with origin UID annotation": {
			featureGates: map[featuregate.Feature]bool{features.WorkloadIdentifierAnnotations: false},
			managersLeaderWorkerSets: []leaderworkersetv1.LeaderWorkerSet{
				*utiltestingleaderworkerset.MakeLeaderWorkerSet("lws1", TestNamespace).UID("manager-uid-123").Obj(),
			},
			operation: func(ctx context.Context, adapter *multiKueueAdapter, managerClient, workerClient client.Client) error {
				_, err := adapter.SyncJob(ctx, managerClient, workerClient, types.NamespacedName{Name: "lws1", Namespace: TestNamespace}, "wl1", "origin1")
				return err
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
			featureGates: map[featuregate.Feature]bool{features.WorkloadIdentifierAnnotations: false},
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
				_, err := adapter.SyncJob(ctx, managerClient, workerClient, types.NamespacedName{Name: "lws1", Namespace: TestNamespace}, "wl1", "origin1")
				return err
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
			featureGates: map[featuregate.Feature]bool{features.WorkloadIdentifierAnnotations: false},
			workerLeaderWorkerSets: []leaderworkersetv1.LeaderWorkerSet{
				*utiltestingleaderworkerset.MakeLeaderWorkerSet("lws1", TestNamespace).
					Label(kueue.MultiKueueOriginLabel, "origin1").
					Annotation(kueue.MultiKueueOriginUIDAnnotation, "manager-uid-123").
					ReadyReplicas(3).
					Obj(),
			},
			operation: func(ctx context.Context, adapter *multiKueueAdapter, managerClient, workerClient client.Client) error {
				return adapter.DeleteRemoteObject(ctx, managerClient, workerClient, types.NamespacedName{Name: "lws1", Namespace: TestNamespace})
			},
		},
		"IsJobManagedByKueue returns true": {
			featureGates: map[featuregate.Feature]bool{features.WorkloadIdentifierAnnotations: false},
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
			featureGates: map[featuregate.Feature]bool{features.WorkloadIdentifierAnnotations: false},
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
			featureGates: map[featuregate.Feature]bool{features.WorkloadIdentifierAnnotations: false},
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
		"GetExpectedWorkloadCount rejects negative replicas": {
			managersLeaderWorkerSets: []leaderworkersetv1.LeaderWorkerSet{
				*utiltestingleaderworkerset.MakeLeaderWorkerSet("lws1", TestNamespace).Replicas(-1).Obj(),
			},
			operation: func(ctx context.Context, adapter *multiKueueAdapter, managerClient, workerClient client.Client) error {
				_, err := adapter.GetExpectedWorkloadCount(ctx, managerClient, types.NamespacedName{Name: "lws1", Namespace: TestNamespace})
				return err
			},
			wantError: errInvalidLeaderWorkerSetReplicas,
			wantManagersLeaderWorkerSets: []leaderworkersetv1.LeaderWorkerSet{
				*utiltestingleaderworkerset.MakeLeaderWorkerSet("lws1", TestNamespace).Replicas(-1).Obj(),
			},
		},
		"GetExpectedWorkloadCount rejects replicas above maximum": {
			managersLeaderWorkerSets: []leaderworkersetv1.LeaderWorkerSet{
				*utiltestingleaderworkerset.MakeLeaderWorkerSet("lws1", TestNamespace).Replicas(maxLeaderWorkerSetReplicas + 1).Obj(),
			},
			operation: func(ctx context.Context, adapter *multiKueueAdapter, managerClient, workerClient client.Client) error {
				_, err := adapter.GetExpectedWorkloadCount(ctx, managerClient, types.NamespacedName{Name: "lws1", Namespace: TestNamespace})
				return err
			},
			wantError: errInvalidLeaderWorkerSetReplicas,
			wantManagersLeaderWorkerSets: []leaderworkersetv1.LeaderWorkerSet{
				*utiltestingleaderworkerset.MakeLeaderWorkerSet("lws1", TestNamespace).Replicas(maxLeaderWorkerSetReplicas + 1).Obj(),
			},
		},
		"WorkloadKeysFor rejects negative replicas": {
			operation: func(ctx context.Context, adapter *multiKueueAdapter, managerClient, workerClient client.Client) error {
				_, err := adapter.WorkloadKeysFor(utiltestingleaderworkerset.MakeLeaderWorkerSet("lws1", TestNamespace).
					Annotation(kueue.MultiKueueOriginUIDAnnotation, "manager-uid-123").
					Replicas(-1).
					Obj())
				return err
			},
			wantError: errInvalidLeaderWorkerSetReplicas,
		},
		"WorkloadKeysFor rejects replicas above maximum": {
			operation: func(ctx context.Context, adapter *multiKueueAdapter, managerClient, workerClient client.Client) error {
				_, err := adapter.WorkloadKeysFor(utiltestingleaderworkerset.MakeLeaderWorkerSet("lws1", TestNamespace).
					Annotation(kueue.MultiKueueOriginUIDAnnotation, "manager-uid-123").
					Replicas(maxLeaderWorkerSetReplicas + 1).
					Obj())
				return err
			},
			wantError: errInvalidLeaderWorkerSetReplicas,
		},
		"sync creates missing remote leaderworkerset, WorkloadIdentifierAnnotations enabled": {
			featureGates: map[featuregate.Feature]bool{features.WorkloadIdentifierAnnotations: true},
			managersLeaderWorkerSets: []leaderworkersetv1.LeaderWorkerSet{
				*utiltestingleaderworkerset.MakeLeaderWorkerSet("lws1", TestNamespace).UID("manager-uid-123").Obj(),
			},
			operation: func(ctx context.Context, adapter *multiKueueAdapter, managerClient, workerClient client.Client) error {
				_, err := adapter.SyncJob(ctx, managerClient, workerClient, types.NamespacedName{Name: "lws1", Namespace: TestNamespace}, "wl1", "origin1")
				return err
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
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGatesDuringTest(t, tc.featureGates)
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

func TestMultiKueueOwnershipWrapperUsesManagerObjectUID(t *testing.T) {
	const (
		origin       = "origin1"
		workloadName = "wl1"
		managerUID   = "manager-uid-123"
	)
	key := types.NamespacedName{Name: "lws1", Namespace: TestNamespace}

	makeManagerClient := func() client.Client {
		return utiltesting.NewClientBuilder(leaderworkersetv1.AddToScheme).
			WithObjects(utiltestingleaderworkerset.MakeLeaderWorkerSet(key.Name, key.Namespace).UID(managerUID).Obj()).
			WithStatusSubresource(&leaderworkersetv1.LeaderWorkerSet{}).
			Build()
	}
	makeWorkerClient := func(originUID string) client.Client {
		return utiltesting.NewClientBuilder(leaderworkersetv1.AddToScheme).
			WithObjects(utiltestingleaderworkerset.MakeLeaderWorkerSet(key.Name, key.Namespace).
				UID("worker-uid").
				Label(kueue.MultiKueueOriginLabel, origin).
				Annotation(kueue.MultiKueueOriginUIDAnnotation, originUID).
				ReadyReplicas(3).
				Obj()).
			Build()
	}

	t.Run("matching UID permits synchronization", func(t *testing.T) {
		managerClient := makeManagerClient()
		workerClient := makeWorkerClient(managerUID)
		ctx, _ := utiltesting.ContextWithLog(t)

		if _, err := jobframework.SyncJobWithRemoteObjectOwnership(
			ctx,
			managerClient,
			managerClient,
			workerClient,
			&multiKueueAdapter{},
			key,
			makeLWSBoundWorkload(workloadName, key, managerUID),
			origin,
		); err != nil {
			t.Fatalf("SyncJobWithRemoteObjectOwnership() error = %v", err)
		}
		local := &leaderworkersetv1.LeaderWorkerSet{}
		if err := managerClient.Get(ctx, key, local); err != nil {
			t.Fatalf("getting manager LeaderWorkerSet: %v", err)
		}
		if local.Status.ReadyReplicas != 0 {
			t.Fatalf("manager ready replicas = %d, want 0 because LWS intentionally skips status copying", local.Status.ReadyReplicas)
		}
	})

	t.Run("different UID blocks status synchronization", func(t *testing.T) {
		managerClient := makeManagerClient()
		workerClient := makeWorkerClient("other-manager-uid")
		ctx, _ := utiltesting.ContextWithLog(t)

		_, err := jobframework.SyncJobWithRemoteObjectOwnership(ctx, managerClient, managerClient, workerClient, &multiKueueAdapter{}, key, makeLWSBoundWorkload(workloadName, key, managerUID), origin)
		if !errors.Is(err, jobframework.ErrRemoteObjectNotOwnedByMultiKueue) {
			t.Fatalf("SyncJobWithRemoteObjectOwnership() error = %v, want %v", err, jobframework.ErrRemoteObjectNotOwnedByMultiKueue)
		}
		local := &leaderworkersetv1.LeaderWorkerSet{}
		if err := managerClient.Get(ctx, key, local); err != nil {
			t.Fatalf("getting manager LeaderWorkerSet: %v", err)
		}
		if local.Status.ReadyReplicas != 0 {
			t.Fatalf("manager ready replicas = %d, want 0", local.Status.ReadyReplicas)
		}
	})

	t.Run("matching UID permits deletion", func(t *testing.T) {
		managerClient := utiltesting.NewClientBuilder(leaderworkersetv1.AddToScheme).Build()
		workerClient := makeWorkerClient(managerUID)
		ctx, _ := utiltesting.ContextWithLog(t)
		managerWorkload := &kueue.Workload{ObjectMeta: metav1.ObjectMeta{
			Name:      workloadName,
			Namespace: key.Namespace,
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: leaderworkersetv1.GroupVersion.String(),
				Kind:       "LeaderWorkerSet",
				Name:       key.Name,
				UID:        managerUID,
				Controller: new(bool),
			}},
		}}
		*managerWorkload.OwnerReferences[0].Controller = true

		if err := jobframework.DeleteRemoteObjectForWorkloadIfOwned(ctx, managerClient, workerClient, &multiKueueAdapter{}, key, managerWorkload, origin); err != nil {
			t.Fatalf("DeleteRemoteObjectForWorkloadIfOwned() error = %v", err)
		}
		if err := workerClient.Get(ctx, key, &leaderworkersetv1.LeaderWorkerSet{}); !apierrors.IsNotFound(err) {
			t.Fatalf("getting worker LeaderWorkerSet error = %v, want NotFound", err)
		}
	})

	t.Run("different UID preserves object during deletion", func(t *testing.T) {
		managerClient := utiltesting.NewClientBuilder(leaderworkersetv1.AddToScheme).Build()
		workerClient := makeWorkerClient("other-manager-uid")
		ctx, _ := utiltesting.ContextWithLog(t)
		managerWorkload := &kueue.Workload{ObjectMeta: metav1.ObjectMeta{
			Name:      workloadName,
			Namespace: key.Namespace,
			OwnerReferences: []metav1.OwnerReference{{
				APIVersion: leaderworkersetv1.GroupVersion.String(),
				Kind:       "LeaderWorkerSet",
				Name:       key.Name,
				UID:        managerUID,
				Controller: new(bool),
			}},
		}}
		*managerWorkload.OwnerReferences[0].Controller = true

		err := jobframework.DeleteRemoteObjectForWorkloadIfOwned(ctx, managerClient, workerClient, &multiKueueAdapter{}, key, managerWorkload, origin)
		if !errors.Is(err, jobframework.ErrRemoteObjectNotOwnedByMultiKueue) {
			t.Fatalf("DeleteRemoteObjectForWorkloadIfOwned() error = %v, want %v", err, jobframework.ErrRemoteObjectNotOwnedByMultiKueue)
		}
		if err := workerClient.Get(ctx, key, &leaderworkersetv1.LeaderWorkerSet{}); err != nil {
			t.Fatalf("getting worker LeaderWorkerSet: %v", err)
		}
	})
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
