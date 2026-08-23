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

package multikueue

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/go-logr/logr"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	config "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/admissionchecks/multikueue/externalframeworks"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/features"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
)

func TestValidateWorkerNamespaceBinding(t *testing.T) {
	const namespace = "team-a"
	managerNamespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace, UID: "manager-uid"}}

	tests := map[string]struct {
		managerObjects []client.Object
		workerObjects  []client.Object
		wantErr        bool
	}{
		"matching UID": {
			managerObjects: []client.Object{managerNamespace},
			workerObjects: []client.Object{&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
				Name: namespace,
				Annotations: map[string]string{
					kueue.MultiKueueAllowedManagerNamespaceUIDsAnnotation: `["manager-uid"]`,
				},
			}}},
		},
		"one of multiple matching UIDs": {
			managerObjects: []client.Object{managerNamespace},
			workerObjects: []client.Object{&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
				Name: namespace,
				Annotations: map[string]string{
					kueue.MultiKueueAllowedManagerNamespaceUIDsAnnotation: `["other-manager","manager-uid"]`,
				},
			}}},
		},
		"missing manager Namespace": {
			workerObjects: []client.Object{&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}}},
			wantErr:       true,
		},
		"manager Namespace without UID": {
			managerObjects: []client.Object{&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}}},
			workerObjects:  []client.Object{&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}}},
			wantErr:        true,
		},
		"missing worker Namespace": {
			managerObjects: []client.Object{managerNamespace},
			wantErr:        true,
		},
		"missing annotation": {
			managerObjects: []client.Object{managerNamespace},
			workerObjects:  []client.Object{&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}}},
			wantErr:        true,
		},
		"malformed annotation": {
			managerObjects: []client.Object{managerNamespace},
			workerObjects: []client.Object{&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
				Name: namespace,
				Annotations: map[string]string{
					kueue.MultiKueueAllowedManagerNamespaceUIDsAnnotation: `manager-uid`,
				},
			}}},
			wantErr: true,
		},
		"different UID": {
			managerObjects: []client.Object{managerNamespace},
			workerObjects: []client.Object{&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
				Name: namespace,
				Annotations: map[string]string{
					kueue.MultiKueueAllowedManagerNamespaceUIDsAnnotation: `["old-manager-uid"]`,
				},
			}}},
			wantErr: true,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.MultiKueueAllowUnboundWorkerNamespaces, false)
			managerClient := utiltesting.NewClientBuilder().WithObjects(tc.managerObjects...).Build()
			workerClient := utiltesting.NewClientBuilder().WithObjects(tc.workerObjects...).Build()

			err := validateWorkerNamespaceBinding(context.Background(), managerClient, workerClient, namespace)
			if tc.wantErr {
				if !errors.Is(err, errWorkerNamespaceNotBound) {
					t.Fatalf("validateWorkerNamespaceBinding() error = %v, want %v", err, errWorkerNamespaceNotBound)
				}
			} else if err != nil {
				t.Fatalf("validateWorkerNamespaceBinding() unexpected error = %v", err)
			}
		})
	}
}

func TestValidateWorkerNamespaceBindingCompatibilityGate(t *testing.T) {
	features.SetFeatureGateDuringTest(t, features.MultiKueueAllowUnboundWorkerNamespaces, true)
	if err := validateWorkerNamespaceBinding(context.Background(), utiltesting.NewClientBuilder().Build(), utiltesting.NewClientBuilder().Build(), "team-a"); err != nil {
		t.Fatalf("validateWorkerNamespaceBinding() unexpected error with compatibility gate = %v", err)
	}
}

func TestNamespaceAuthorizingClientCompatibilityGatePreservesLegacyClient(t *testing.T) {
	const namespace = "team-a"
	features.SetFeatureGateDuringTest(t, features.MultiKueueAllowUnboundWorkerNamespaces, true)
	remoteWorkload := &kueue.Workload{ObjectMeta: metav1.ObjectMeta{Name: "workload", Namespace: namespace}}
	rawWorkerClient := NewNeverCachingClient(utiltesting.NewClientBuilder().WithObjects(remoteWorkload).Build())
	workerClient := (&wlReconciler{}).namespaceAuthorizingClient(rawWorkerClient)

	if err := workerClient.DeleteAllOf(context.Background(), &kueue.Workload{}, client.InNamespace(namespace)); err != nil {
		t.Fatalf("DeleteAllOf() in compatibility mode: %v", err)
	}
	if err := rawWorkerClient.Get(context.Background(), client.ObjectKeyFromObject(remoteWorkload), &kueue.Workload{}); !apierrors.IsNotFound(err) {
		t.Fatalf("worker Workload get error = %v, want NotFound", err)
	}
}

func TestSyncToSingleClusterRequiresWorkerNamespaceBinding(t *testing.T) {
	const (
		namespace = "team-a"
		cluster   = "worker1"
	)
	managerNamespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace, UID: types.UID("manager-uid")}}
	localWorkload := &kueue.Workload{ObjectMeta: metav1.ObjectMeta{Name: "workload", Namespace: namespace}}
	managerClient := utiltesting.NewClientBuilder().WithObjects(managerNamespace, localWorkload).Build()

	for name, tc := range map[string]struct {
		workerNamespace   *corev1.Namespace
		allowUnbound      bool
		wantNotBoundError bool
	}{
		"unbound": {
			workerNamespace:   &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}},
			wantNotBoundError: true,
		},
		"bound": {
			workerNamespace: &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
				Name: namespace,
				Annotations: map[string]string{
					kueue.MultiKueueAllowedManagerNamespaceUIDsAnnotation: `["manager-uid"]`,
				},
			}},
		},
		"unbound compatibility mode": {
			workerNamespace: &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}},
			allowUnbound:    true,
		},
	} {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.MultiKueueAllowUnboundWorkerNamespaces, tc.allowUnbound)
			workerClient := NewNeverCachingClient(utiltesting.NewClientBuilder().WithObjects(tc.workerNamespace).Build())
			group := &wlGroup{
				local:         localWorkload,
				remotes:       map[string]*kueue.Workload{cluster: nil},
				remoteClients: map[string]*remoteClient{cluster: {client: workerClient, origin: "origin"}},
			}
			reconciler := &wlReconciler{client: managerClient, managerNamespaceReader: managerClient}

			_, err := reconciler.syncToSingleCluster(context.Background(), logr.Discard(), group, cluster)
			remoteWorkload := &kueue.Workload{}
			getErr := workerClient.Get(context.Background(), client.ObjectKeyFromObject(localWorkload), remoteWorkload)
			if tc.wantNotBoundError {
				if !errors.Is(err, errWorkerNamespaceNotBound) {
					t.Fatalf("syncToSingleCluster() error = %v, want %v", err, errWorkerNamespaceNotBound)
				}
				if !apierrors.IsNotFound(getErr) {
					t.Fatalf("worker Workload get error = %v, want NotFound", getErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("syncToSingleCluster() unexpected error = %v", err)
			}
			if getErr != nil {
				t.Fatalf("worker Workload was not created: %v", getErr)
			}
		})
	}
}

func TestNamespaceBindingGuardsExternalFrameworkCreate(t *testing.T) {
	const namespace = "team-a"
	features.SetFeatureGateDuringTest(t, features.MultiKueueAllowUnboundWorkerNamespaces, false)

	gvk := schema.GroupVersionKind{Group: "example.test", Version: "v1", Kind: "TestJob"}
	key := types.NamespacedName{Name: "job", Namespace: namespace}
	localJob := &unstructured.Unstructured{Object: map[string]any{
		"apiVersion": gvk.GroupVersion().String(),
		"kind":       gvk.Kind,
		"metadata": map[string]any{
			"name":      key.Name,
			"namespace": key.Namespace,
		},
		"spec": map[string]any{"value": "preserved"},
	}}
	localJob.SetGroupVersionKind(gvk)
	managerNamespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace, UID: "manager-uid"}}
	managerClient := utiltesting.NewClientBuilder().WithObjects(managerNamespace, localJob).Build()
	workerClient := NewNeverCachingClient(utiltesting.NewClientBuilder().WithObjects(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}},
	).Build())
	reconciler := &wlReconciler{managerNamespaceReader: managerClient}

	adapter := externalframeworks.NewAdapter(gvk)
	_, err := adapter.SyncJob(context.Background(), managerClient, reconciler.namespaceAuthorizingClient(workerClient), key, "workload", "origin")
	if !errors.Is(err, errWorkerNamespaceNotBound) {
		t.Fatalf("SyncJob() error = %v, want %v", err, errWorkerNamespaceNotBound)
	}
	remoteJob := &unstructured.Unstructured{}
	remoteJob.SetGroupVersionKind(gvk)
	if err := workerClient.Get(context.Background(), key, remoteJob); !apierrors.IsNotFound(err) {
		t.Fatalf("worker external Job get error = %v, want NotFound", err)
	}
}

func TestNamespaceAuthorizingClientRejectsWritesAfterRevocation(t *testing.T) {
	const namespace = "team-a"
	features.SetFeatureGateDuringTest(t, features.MultiKueueAllowUnboundWorkerNamespaces, false)

	for name, write := range map[string]func(context.Context, client.Client, *kueue.Workload) error{
		"update": func(ctx context.Context, workerClient client.Client, remoteWorkload *kueue.Workload) error {
			remoteWorkload.Annotations = map[string]string{"changed": "true"}
			return workerClient.Update(ctx, remoteWorkload)
		},
		"status update": func(ctx context.Context, workerClient client.Client, remoteWorkload *kueue.Workload) error {
			remoteWorkload.Status.Conditions = []metav1.Condition{{Type: "Changed", Status: metav1.ConditionTrue}}
			return workerClient.Status().Update(ctx, remoteWorkload)
		},
		"status patch": func(ctx context.Context, workerClient client.Client, remoteWorkload *kueue.Workload) error {
			original := remoteWorkload.DeepCopy()
			remoteWorkload.Status.Conditions = []metav1.Condition{{Type: "Changed", Status: metav1.ConditionTrue}}
			return workerClient.Status().Patch(ctx, remoteWorkload, client.MergeFrom(original))
		},
	} {
		t.Run(name, func(t *testing.T) {
			managerNamespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace, UID: "manager-uid"}}
			managerClient := utiltesting.NewClientBuilder().WithObjects(managerNamespace).Build()
			remoteWorkload := &kueue.Workload{ObjectMeta: metav1.ObjectMeta{Name: "workload", Namespace: namespace}}
			rawWorkerClient := NewNeverCachingClient(utiltesting.NewClientBuilder().WithObjects(
				&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}},
				remoteWorkload,
			).WithStatusSubresource(remoteWorkload).Build())
			reconciler := &wlReconciler{managerNamespaceReader: managerClient}
			workerClient := reconciler.namespaceAuthorizingClient(rawWorkerClient)
			stored := &kueue.Workload{}
			if err := rawWorkerClient.Get(context.Background(), client.ObjectKeyFromObject(remoteWorkload), stored); err != nil {
				t.Fatalf("getting worker Workload: %v", err)
			}

			err := write(context.Background(), workerClient, stored)
			if !errors.Is(err, errWorkerNamespaceNotBound) {
				t.Fatalf("write error = %v, want %v", err, errWorkerNamespaceNotBound)
			}
			unchanged := &kueue.Workload{}
			if err := rawWorkerClient.Get(context.Background(), client.ObjectKeyFromObject(remoteWorkload), unchanged); err != nil {
				t.Fatalf("getting worker Workload after rejected write: %v", err)
			}
			if len(unchanged.Annotations) != 0 || len(unchanged.Status.Conditions) != 0 {
				t.Fatalf("worker Workload changed after rejected write: annotations=%v conditions=%v", unchanged.Annotations, unchanged.Status.Conditions)
			}
		})
	}
}

func TestNamespaceAuthorizingClientRejectsListsAfterRevocation(t *testing.T) {
	const namespace = "team-a"
	features.SetFeatureGateDuringTest(t, features.MultiKueueAllowUnboundWorkerNamespaces, false)
	managerNamespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace, UID: "manager-uid"}}
	managerClient := utiltesting.NewClientBuilder().WithObjects(managerNamespace).Build()
	rawWorkerClient := NewNeverCachingClient(utiltesting.NewClientBuilder().WithObjects(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}},
	).Build())
	workerClient := (&wlReconciler{managerNamespaceReader: managerClient}).namespaceAuthorizingClient(rawWorkerClient)

	if err := workerClient.List(context.Background(), &kueue.WorkloadList{}); err == nil {
		t.Fatal("all-Namespace List() unexpectedly succeeded")
	}
	if err := workerClient.List(context.Background(), &kueue.WorkloadList{}, client.InNamespace(namespace)); !errors.Is(err, errWorkerNamespaceNotBound) {
		t.Fatalf("namespaced List() error = %v, want %v", err, errWorkerNamespaceNotBound)
	}
}

func TestNamespaceAuthorizingClientAllowsNamespacedDeleteAllOf(t *testing.T) {
	const namespace = "team-a"
	features.SetFeatureGateDuringTest(t, features.MultiKueueAllowUnboundWorkerNamespaces, false)
	managerNamespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace, UID: "manager-uid"}}
	managerClient := utiltesting.NewClientBuilder().WithObjects(managerNamespace).Build()
	remoteWorkload := &kueue.Workload{ObjectMeta: metav1.ObjectMeta{Name: "workload", Namespace: namespace}}
	rawWorkerClient := NewNeverCachingClient(utiltesting.NewClientBuilder().WithObjects(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
			Name: namespace,
			Annotations: map[string]string{
				kueue.MultiKueueAllowedManagerNamespaceUIDsAnnotation: `["manager-uid"]`,
			},
		}},
		remoteWorkload,
	).Build())
	workerClient := (&wlReconciler{managerNamespaceReader: managerClient}).namespaceAuthorizingClient(rawWorkerClient)

	if err := workerClient.DeleteAllOf(context.Background(), &kueue.Workload{}, client.InNamespace(namespace)); err != nil {
		t.Fatalf("DeleteAllOf() in authorized Namespace: %v", err)
	}
	if err := rawWorkerClient.Get(context.Background(), client.ObjectKeyFromObject(remoteWorkload), &kueue.Workload{}); !apierrors.IsNotFound(err) {
		t.Fatalf("worker Workload get error = %v, want NotFound", err)
	}
}

func TestSyncToSingleClusterUsesLiveManagerNamespaceUID(t *testing.T) {
	const (
		namespace = "team-a"
		cluster   = "worker1"
	)
	features.SetFeatureGateDuringTest(t, features.MultiKueueAllowUnboundWorkerNamespaces, false)

	localWorkload := &kueue.Workload{ObjectMeta: metav1.ObjectMeta{Name: "workload", Namespace: namespace}}
	staleManagerClient := utiltesting.NewClientBuilder().WithObjects(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace, UID: "old-manager-uid"}},
		localWorkload,
	).Build()
	liveManagerReader := utiltesting.NewClientBuilder().WithObjects(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace, UID: "new-manager-uid"}},
	).Build()
	workerClient := NewNeverCachingClient(utiltesting.NewClientBuilder().WithObjects(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
			Name: namespace,
			Annotations: map[string]string{
				kueue.MultiKueueAllowedManagerNamespaceUIDsAnnotation: `["old-manager-uid"]`,
			},
		}},
	).Build())
	group := &wlGroup{
		local:         localWorkload,
		remotes:       map[string]*kueue.Workload{cluster: nil},
		remoteClients: map[string]*remoteClient{cluster: {client: workerClient, origin: "origin"}},
	}
	reconciler := &wlReconciler{client: staleManagerClient, managerNamespaceReader: liveManagerReader}

	_, err := reconciler.syncToSingleCluster(context.Background(), logr.Discard(), group, cluster)
	if !errors.Is(err, errWorkerNamespaceNotBound) {
		t.Fatalf("syncToSingleCluster() error = %v, want %v", err, errWorkerNamespaceNotBound)
	}
	if err := workerClient.Get(context.Background(), client.ObjectKeyFromObject(localWorkload), &kueue.Workload{}); !apierrors.IsNotFound(err) {
		t.Fatalf("worker Workload get error = %v, want NotFound", err)
	}
}

type namespaceBindingSyncStub struct {
	syncCalls int
	createKey *types.NamespacedName
}

var _ jobframework.MultiKueueAdapter = (*namespaceBindingSyncStub)(nil)

func (s *namespaceBindingSyncStub) SyncJob(ctx context.Context, _, remoteClient client.Client, _ types.NamespacedName, _, origin string) (bool, error) {
	s.syncCalls++
	if s.createKey != nil {
		return false, remoteClient.Create(ctx, &batchv1.Job{ObjectMeta: metav1.ObjectMeta{
			Name:      s.createKey.Name,
			Namespace: s.createKey.Namespace,
			Labels:    map[string]string{kueue.MultiKueueOriginLabel: origin},
		}})
	}
	return false, nil
}

func (s *namespaceBindingSyncStub) DeleteRemoteObject(_ context.Context, _, _ client.Client, _ types.NamespacedName) error {
	return nil
}

func (s *namespaceBindingSyncStub) IsJobManagedByKueue(_ context.Context, _ client.Client, _ types.NamespacedName) (bool, string, error) {
	return true, "", nil
}

func (s *namespaceBindingSyncStub) GVK() schema.GroupVersionKind {
	return batchv1.SchemeGroupVersion.WithKind("Job")
}

type namespaceBindingCleanupStub struct {
	deleteCalls int
}

var _ jobframework.MultiKueueAdapter = (*namespaceBindingCleanupStub)(nil)

func (*namespaceBindingCleanupStub) SyncJob(context.Context, client.Client, client.Client, types.NamespacedName, string, string) (bool, error) {
	return false, nil
}

func (s *namespaceBindingCleanupStub) DeleteRemoteObject(ctx context.Context, _ client.Client, remoteClient client.Client, key types.NamespacedName) error {
	remoteJob := &batchv1.Job{}
	if err := remoteClient.Get(ctx, key, remoteJob); err != nil {
		return client.IgnoreNotFound(err)
	}
	s.deleteCalls++
	return remoteClient.Delete(ctx, remoteJob)
}

func (*namespaceBindingCleanupStub) IsJobManagedByKueue(context.Context, client.Client, types.NamespacedName) (bool, string, error) {
	return true, "", nil
}

func (*namespaceBindingCleanupStub) GVK() schema.GroupVersionKind {
	return batchv1.SchemeGroupVersion.WithKind("Job")
}

func TestReconcileGroupRequiresBindingBeforeRemoteObjectCreate(t *testing.T) {
	const (
		namespace = "team-a"
		cluster   = "worker1"
		acName    = kueue.AdmissionCheckReference("ac1")
	)
	features.SetFeatureGateDuringTest(t, features.MultiKueueAllowUnboundWorkerNamespaces, false)

	for name, tc := range map[string]struct {
		managerUID               types.UID
		allowedUIDs              string
		existingRemoteController bool
		wantErr                  bool
	}{
		"unbound": {
			managerUID: "manager-uid",
			wantErr:    true,
		},
		"manager Namespace recreated with existing remote controller": {
			managerUID:               "new-manager-uid",
			allowedUIDs:              `["old-manager-uid"]`,
			existingRemoteController: true,
			wantErr:                  true,
		},
		"bound": {
			managerUID:  "manager-uid",
			allowedUIDs: `["manager-uid"]`,
		},
	} {
		t.Run(name, func(t *testing.T) {
			now := time.Now()
			localWorkload := utiltestingapi.MakeWorkload("workload", namespace).
				ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").Obj(), now).
				AdmittedAt(true, now).
				AdmissionCheck(kueue.AdmissionCheckState{
					Name:               acName,
					State:              kueue.CheckStateReady,
					LastTransitionTime: metav1.NewTime(now),
				}).
				ClusterName(cluster).
				Obj()
			remoteWorkload := utiltestingapi.MakeWorkload("workload", namespace).
				Condition(metav1.Condition{
					Type:               kueue.WorkloadAdmitted,
					Status:             metav1.ConditionTrue,
					Reason:             "Admitted",
					LastTransitionTime: metav1.NewTime(now),
				}).
				Obj()

			managerNamespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace, UID: tc.managerUID}}
			workerNamespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}}
			if tc.allowedUIDs != "" {
				workerNamespace.Annotations = map[string]string{
					kueue.MultiKueueAllowedManagerNamespaceUIDsAnnotation: tc.allowedUIDs,
				}
			}

			managerClient := utiltesting.NewClientBuilder().WithObjects(managerNamespace, localWorkload).WithStatusSubresource(localWorkload).Build()
			workerObjects := []client.Object{workerNamespace, remoteWorkload}
			if tc.existingRemoteController {
				workerObjects = append(workerObjects, &batchv1.Job{ObjectMeta: metav1.ObjectMeta{
					Name:      "job",
					Namespace: namespace,
					Labels:    map[string]string{kueue.MultiKueueOriginLabel: "origin"},
				}})
			}
			workerClient := NewNeverCachingClient(utiltesting.NewClientBuilder().WithObjects(workerObjects...).WithStatusSubresource(remoteWorkload).Build())
			adapter := &namespaceBindingSyncStub{}
			group := &wlGroup{
				local:       localWorkload,
				localClient: managerClient,
				remotes:     map[string]*kueue.Workload{cluster: remoteWorkload},
				remoteClients: map[string]*remoteClient{
					cluster: {client: workerClient, origin: "origin"},
				},
				acName:        acName,
				jobAdapter:    adapter,
				controllerKey: types.NamespacedName{Name: "job", Namespace: namespace},
			}
			reconciler := &wlReconciler{
				client:                 managerClient,
				managerNamespaceReader: managerClient,
				clock:                  realClock,
				origin:                 "origin",
				workerLostTimeout:      defaultWorkerLostTimeout,
				recorder:               &utiltesting.EventRecorder{},
				dispatcherName:         config.MultiKueueDispatcherModeAllAtOnce,
			}

			_, err := reconciler.reconcileGroup(context.Background(), group)
			if tc.wantErr {
				if !errors.Is(err, errWorkerNamespaceNotBound) {
					t.Fatalf("reconcileGroup() error = %v, want %v", err, errWorkerNamespaceNotBound)
				}
				if adapter.syncCalls != 0 {
					t.Fatalf("SyncJob() calls = %d, want 0", adapter.syncCalls)
				}
				return
			}
			if err != nil {
				t.Fatalf("reconcileGroup() unexpected error = %v", err)
			}
			if adapter.syncCalls != 1 {
				t.Fatalf("SyncJob() calls = %d, want 1", adapter.syncCalls)
			}
		})
	}
}

func TestReconcileGroupRechecksBindingAtAdapterCreate(t *testing.T) {
	const (
		namespace = "team-a"
		cluster   = "worker1"
		origin    = "origin"
		acName    = kueue.AdmissionCheckReference("ac1")
	)
	features.SetFeatureGateDuringTest(t, features.MultiKueueAllowUnboundWorkerNamespaces, false)
	now := time.Now()
	localWorkload := utiltestingapi.MakeWorkload("workload", namespace).
		ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").Obj(), now).
		AdmittedAt(true, now).
		AdmissionCheck(kueue.AdmissionCheckState{
			Name:               acName,
			State:              kueue.CheckStateReady,
			LastTransitionTime: metav1.NewTime(now),
		}).
		ClusterName(cluster).
		Obj()
	remoteWorkload := utiltestingapi.MakeWorkload("workload", namespace).
		Condition(metav1.Condition{
			Type:               kueue.WorkloadAdmitted,
			Status:             metav1.ConditionTrue,
			Reason:             "Admitted",
			LastTransitionTime: metav1.NewTime(now),
		}).
		Obj()
	managerNamespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace, UID: "manager-uid"}}
	managerClient := utiltesting.NewClientBuilder().WithObjects(managerNamespace, localWorkload).WithStatusSubresource(localWorkload).Build()

	workerNamespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
		Name: namespace,
		Annotations: map[string]string{
			kueue.MultiKueueAllowedManagerNamespaceUIDsAnnotation: `["manager-uid"]`,
		},
	}}
	remoteController := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{
		Name:      "controller",
		Namespace: namespace,
		Labels:    map[string]string{kueue.MultiKueueOriginLabel: origin},
	}}
	namespaceGets := 0
	workerClient := NewNeverCachingClient(utiltesting.NewClientBuilder().WithObjects(workerNamespace, remoteWorkload, remoteController).
		WithStatusSubresource(remoteWorkload).
		WithInterceptorFuncs(interceptor.Funcs{
			Get: func(ctx context.Context, c client.WithWatch, key client.ObjectKey, obj client.Object, opts ...client.GetOption) error {
				if err := c.Get(ctx, key, obj, opts...); err != nil {
					return err
				}
				if key.Namespace == "" && key.Name == namespace {
					namespaceGets++
					if namespaceGets >= 4 {
						delete(obj.GetAnnotations(), kueue.MultiKueueAllowedManagerNamespaceUIDsAnnotation)
					}
				}
				return nil
			},
		}).Build())
	memberKey := types.NamespacedName{Name: "member", Namespace: namespace}
	adapter := &namespaceBindingSyncStub{createKey: &memberKey}
	group := &wlGroup{
		local:       localWorkload,
		localClient: managerClient,
		remotes:     map[string]*kueue.Workload{cluster: remoteWorkload},
		remoteClients: map[string]*remoteClient{
			cluster: {client: workerClient, origin: origin},
		},
		acName:        acName,
		jobAdapter:    adapter,
		controllerKey: client.ObjectKeyFromObject(remoteController),
	}
	reconciler := &wlReconciler{
		client:                 managerClient,
		managerNamespaceReader: managerClient,
		clock:                  realClock,
		origin:                 origin,
		workerLostTimeout:      defaultWorkerLostTimeout,
		recorder:               &utiltesting.EventRecorder{},
		dispatcherName:         config.MultiKueueDispatcherModeAllAtOnce,
	}

	_, err := reconciler.reconcileGroup(context.Background(), group)
	if !errors.Is(err, errWorkerNamespaceNotBound) {
		t.Fatalf("reconcileGroup() error = %v, want %v", err, errWorkerNamespaceNotBound)
	}
	if adapter.syncCalls != 1 {
		t.Fatalf("SyncJob() calls = %d, want 1", adapter.syncCalls)
	}
	if err := workerClient.Get(context.Background(), memberKey, &batchv1.Job{}); !apierrors.IsNotFound(err) {
		t.Fatalf("worker member Job get error = %v, want NotFound", err)
	}
}

func TestReconcileGroupRejectsRevokedRemoteStatusBeforeManagerStateChange(t *testing.T) {
	const (
		namespace = "team-a"
		cluster   = "worker1"
		acName    = kueue.AdmissionCheckReference("ac1")
	)
	features.SetFeatureGateDuringTest(t, features.MultiKueueAllowUnboundWorkerNamespaces, false)

	for name, remotePresent := range map[string]bool{
		"remote admitted condition removed": true,
		"remote Workload removed":           false,
	} {
		t.Run(name, func(t *testing.T) {
			now := time.Now()
			localWorkload := utiltestingapi.MakeWorkload("workload", namespace).
				ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").Obj(), now).
				AdmittedAt(true, now).
				AdmissionCheck(kueue.AdmissionCheckState{
					Name:               acName,
					State:              kueue.CheckStateReady,
					LastTransitionTime: metav1.NewTime(now),
				}).
				ClusterName(cluster).
				Obj()
			managerNamespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace, UID: "manager-uid"}}
			managerClient := utiltesting.NewClientBuilder().WithObjects(managerNamespace, localWorkload).WithStatusSubresource(localWorkload).Build()
			workerObjects := []client.Object{&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}}}
			var remoteWorkload *kueue.Workload
			if remotePresent {
				remoteWorkload = localWorkload.DeepCopy()
				remoteWorkload.Status = kueue.WorkloadStatus{}
				workerObjects = append(workerObjects, remoteWorkload)
			}
			workerClient := NewNeverCachingClient(utiltesting.NewClientBuilder().WithObjects(workerObjects...).Build())
			group := &wlGroup{
				local:         localWorkload,
				localClient:   managerClient,
				remotes:       map[string]*kueue.Workload{cluster: remoteWorkload},
				remoteClients: map[string]*remoteClient{cluster: {client: workerClient, origin: "origin"}},
				acName:        acName,
			}
			reconciler := &wlReconciler{
				client:                 managerClient,
				managerNamespaceReader: managerClient,
				clock:                  realClock,
				workerLostTimeout:      defaultWorkerLostTimeout,
			}

			_, err := reconciler.reconcileGroup(context.Background(), group)
			if !errors.Is(err, errWorkerNamespaceNotBound) {
				t.Fatalf("reconcileGroup() error = %v, want %v", err, errWorkerNamespaceNotBound)
			}
			stored := &kueue.Workload{}
			if err := managerClient.Get(context.Background(), client.ObjectKeyFromObject(localWorkload), stored); err != nil {
				t.Fatalf("getting manager Workload: %v", err)
			}
			if got := stored.Status.AdmissionChecks[0].State; got != kueue.CheckStateReady {
				t.Fatalf("manager AdmissionCheck state = %q, want %q", got, kueue.CheckStateReady)
			}
		})
	}
}

func TestReconcileGroupRetainsMissingRemoteStatusBehaviorWhenAuthorized(t *testing.T) {
	const (
		namespace = "team-a"
		cluster   = "worker1"
		acName    = kueue.AdmissionCheckReference("ac1")
	)

	for name, tc := range map[string]struct {
		compatibilityMode bool
		workerAnnotations map[string]string
	}{
		"bound Namespace": {
			workerAnnotations: map[string]string{
				kueue.MultiKueueAllowedManagerNamespaceUIDsAnnotation: `["manager-uid"]`,
			},
		},
		"compatibility mode": {
			compatibilityMode: true,
		},
	} {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.MultiKueueAllowUnboundWorkerNamespaces, tc.compatibilityMode)
			now := time.Now()
			localWorkload := utiltestingapi.MakeWorkload("workload", namespace).
				ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").Obj(), now).
				AdmittedAt(true, now).
				AdmissionCheck(kueue.AdmissionCheckState{
					Name:               acName,
					State:              kueue.CheckStateReady,
					LastTransitionTime: metav1.NewTime(now),
				}).
				ClusterName(cluster).
				Obj()
			managerNamespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace, UID: "manager-uid"}}
			managerClient := utiltesting.NewClientBuilder().WithObjects(managerNamespace, localWorkload).WithStatusSubresource(localWorkload).Build()
			workerClient := NewNeverCachingClient(utiltesting.NewClientBuilder().WithObjects(
				&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace, Annotations: tc.workerAnnotations}},
			).Build())
			group := &wlGroup{
				local:         localWorkload,
				localClient:   managerClient,
				remotes:       map[string]*kueue.Workload{cluster: nil},
				remoteClients: map[string]*remoteClient{cluster: {client: workerClient, origin: "origin"}},
				acName:        acName,
			}
			reconciler := &wlReconciler{
				client:                 managerClient,
				managerNamespaceReader: managerClient,
				clock:                  realClock,
				workerLostTimeout:      defaultWorkerLostTimeout,
			}

			if _, err := reconciler.reconcileGroup(context.Background(), group); err != nil {
				t.Fatalf("reconcileGroup() unexpected error = %v", err)
			}
			stored := &kueue.Workload{}
			if err := managerClient.Get(context.Background(), client.ObjectKeyFromObject(localWorkload), stored); err != nil {
				t.Fatalf("getting manager Workload: %v", err)
			}
			if got := stored.Status.AdmissionChecks[0].State; got != kueue.CheckStateRetry {
				t.Fatalf("manager AdmissionCheck state = %q, want %q", got, kueue.CheckStateRetry)
			}
		})
	}
}

func TestReconcileGroupRejectsRevokedSelectedWorkerBeforeDeletingAuthorizedWorker(t *testing.T) {
	const (
		namespace         = "team-a"
		authorizedCluster = "worker1"
		revokedCluster    = "worker2"
		origin            = "origin"
		acName            = kueue.AdmissionCheckReference("ac1")
	)
	features.SetFeatureGateDuringTest(t, features.MultiKueueAllowUnboundWorkerNamespaces, false)
	now := time.Now()
	localWorkload := utiltestingapi.MakeWorkload("workload", namespace).
		ReserveQuotaAt(utiltestingapi.MakeAdmission("cq").Obj(), now).
		AdmissionCheck(kueue.AdmissionCheckState{
			Name:               acName,
			State:              kueue.CheckStatePending,
			LastTransitionTime: metav1.NewTime(now),
		}).
		Obj()
	managerNamespace := &corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace, UID: "manager-uid"}}
	managerClient := utiltesting.NewClientBuilder().WithObjects(managerNamespace, localWorkload).WithStatusSubresource(localWorkload).Build()

	authorizedRemote := localWorkload.DeepCopy()
	authorizedRemote.Status = kueue.WorkloadStatus{}
	authorizedJob := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{
		Name:      "job",
		Namespace: namespace,
		Labels:    map[string]string{kueue.MultiKueueOriginLabel: origin},
	}}
	authorizedWorkerClient := NewNeverCachingClient(utiltesting.NewClientBuilder().WithObjects(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{
			Name: namespace,
			Annotations: map[string]string{
				kueue.MultiKueueAllowedManagerNamespaceUIDsAnnotation: `["manager-uid"]`,
			},
		}},
		authorizedRemote,
		authorizedJob,
	).Build())
	revokedRemote := localWorkload.DeepCopy()
	revokedRemote.Status = kueue.WorkloadStatus{Conditions: []metav1.Condition{{
		Type:               kueue.WorkloadAdmitted,
		Status:             metav1.ConditionTrue,
		Reason:             "Admitted",
		LastTransitionTime: metav1.NewTime(now),
	}}}
	revokedWorkerClient := NewNeverCachingClient(utiltesting.NewClientBuilder().WithObjects(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}},
		revokedRemote,
	).Build())
	adapter := &namespaceBindingCleanupStub{}
	group := &wlGroup{
		local:       localWorkload,
		localClient: managerClient,
		remotes: map[string]*kueue.Workload{
			authorizedCluster: authorizedRemote,
			revokedCluster:    revokedRemote,
		},
		remoteClients: map[string]*remoteClient{
			authorizedCluster: {client: authorizedWorkerClient, origin: origin},
			revokedCluster:    {client: revokedWorkerClient, origin: origin},
		},
		acName:        acName,
		jobAdapter:    adapter,
		controllerKey: types.NamespacedName{Name: authorizedJob.Name, Namespace: namespace},
	}
	reconciler := &wlReconciler{
		client:                 managerClient,
		managerNamespaceReader: managerClient,
		clock:                  realClock,
		origin:                 origin,
		workerLostTimeout:      defaultWorkerLostTimeout,
		dispatcherName:         config.MultiKueueDispatcherModeAllAtOnce,
	}

	_, err := reconciler.reconcileGroup(context.Background(), group)
	if !errors.Is(err, errWorkerNamespaceNotBound) {
		t.Fatalf("reconcileGroup() error = %v, want %v", err, errWorkerNamespaceNotBound)
	}
	if err := authorizedWorkerClient.Get(context.Background(), client.ObjectKeyFromObject(authorizedRemote), &kueue.Workload{}); err != nil {
		t.Fatalf("authorized worker Workload was deleted: %v", err)
	}
	if err := authorizedWorkerClient.Get(context.Background(), client.ObjectKeyFromObject(authorizedJob), &batchv1.Job{}); err != nil {
		t.Fatalf("authorized worker Job was deleted: %v", err)
	}
	if adapter.deleteCalls != 0 {
		t.Fatalf("DeleteRemoteObject() calls = %d, want 0", adapter.deleteCalls)
	}
	stored := &kueue.Workload{}
	if err := managerClient.Get(context.Background(), client.ObjectKeyFromObject(localWorkload), stored); err != nil {
		t.Fatalf("getting manager Workload: %v", err)
	}
	if got := stored.Status.AdmissionChecks[0].State; got != kueue.CheckStatePending {
		t.Fatalf("manager AdmissionCheck state = %q, want %q", got, kueue.CheckStatePending)
	}
}

func TestReconcileGroupAllowsCleanupAfterNamespaceRevocation(t *testing.T) {
	const (
		namespace = "team-a"
		cluster   = "worker1"
		origin    = "origin"
	)
	features.SetFeatureGateDuringTest(t, features.MultiKueueAllowUnboundWorkerNamespaces, false)
	localWorkload := &kueue.Workload{ObjectMeta: metav1.ObjectMeta{Name: "workload", Namespace: namespace}}
	managerClient := utiltesting.NewClientBuilder().WithObjects(localWorkload).Build()
	remoteWorkload := localWorkload.DeepCopy()
	remoteJob := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{
		Name:      "job",
		Namespace: namespace,
		Labels:    map[string]string{kueue.MultiKueueOriginLabel: origin},
	}}
	workerClient := NewNeverCachingClient(utiltesting.NewClientBuilder().WithObjects(
		&corev1.Namespace{ObjectMeta: metav1.ObjectMeta{Name: namespace}},
		remoteWorkload,
		remoteJob,
	).Build())
	adapter := &namespaceBindingCleanupStub{}
	group := &wlGroup{
		local:         localWorkload,
		localClient:   managerClient,
		remotes:       map[string]*kueue.Workload{cluster: remoteWorkload},
		remoteClients: map[string]*remoteClient{cluster: {client: workerClient, origin: origin}},
		jobAdapter:    adapter,
		controllerKey: types.NamespacedName{Name: remoteJob.Name, Namespace: namespace},
	}
	reconciler := &wlReconciler{
		managerNamespaceReader: managerClient,
		workerLostTimeout:      defaultWorkerLostTimeout,
	}

	if _, err := reconciler.reconcileGroup(context.Background(), group); err != nil {
		t.Fatalf("reconcileGroup() cleanup after revocation: %v", err)
	}
	if adapter.deleteCalls != 1 {
		t.Fatalf("DeleteRemoteObject() calls = %d, want 1", adapter.deleteCalls)
	}
	if err := workerClient.Get(context.Background(), client.ObjectKeyFromObject(remoteJob), &batchv1.Job{}); !apierrors.IsNotFound(err) {
		t.Fatalf("worker Job get error = %v, want NotFound", err)
	}
	if err := workerClient.Get(context.Background(), client.ObjectKeyFromObject(remoteWorkload), &kueue.Workload{}); !apierrors.IsNotFound(err) {
		t.Fatalf("worker Workload get error = %v, want NotFound", err)
	}
}
