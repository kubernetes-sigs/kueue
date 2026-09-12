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

package jobframework_test

import (
	"context"
	"errors"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"go.uber.org/mock/gomock"
	batchv1 "k8s.io/api/batch/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/component-base/featuregate"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	mocks "sigs.k8s.io/kueue/internal/mocks/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/features"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
)

type cleanupTrackingAdapter struct {
	jobframework.MultiKueueAdapter
	cleanupContext *jobframework.MultiKueueRemoteObjectCleanupContext
}

func (a *cleanupTrackingAdapter) DeleteRemoteObjectWithCleanupContext(
	_ context.Context,
	_, _ client.Client,
	_ types.NamespacedName,
	cleanupContext jobframework.MultiKueueRemoteObjectCleanupContext,
) error {
	a.cleanupContext = &cleanupContext
	cleanupContext.WorkloadAnnotations["mutated-by-adapter"] = "true"
	return nil
}

func TestValidateRemoteObjectOwnership(t *testing.T) {
	key := types.NamespacedName{Name: "test", Namespace: "default"}
	gvk := batchv1.SchemeGroupVersion.WithKind("Job")

	makeJob := func(labels map[string]string) *batchv1.Job {
		return &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace, Labels: labels}}
	}

	tests := map[string]struct {
		obj       client.Object
		origin    string
		wantFound bool
		wantErr   error
	}{
		"origin not set fails validation": {
			obj:       makeJob(nil),
			origin:    "",
			wantFound: false,
			wantErr:   jobframework.ErrMultiKueueOriginEmpty,
		},
		"origin not set and object missing fails validation": {
			origin:    "",
			wantFound: false,
			wantErr:   jobframework.ErrMultiKueueOriginEmpty,
		},
		"origin set and label missing": {
			obj:       makeJob(nil),
			origin:    "origin-1",
			wantFound: false,
			wantErr:   jobframework.ErrRemoteObjectNotOwnedByMultiKueue,
		},
		"origin set and label present": {
			obj:       makeJob(map[string]string{kueue.MultiKueueOriginLabel: "origin-1"}),
			origin:    "origin-1",
			wantFound: true,
			wantErr:   nil,
		},
		"origin set and label mismatched": {
			obj:       makeJob(map[string]string{kueue.MultiKueueOriginLabel: "origin-2"}),
			origin:    "origin-1",
			wantFound: false,
			wantErr:   jobframework.ErrRemoteObjectNotOwnedByMultiKueue,
		},
		"object missing": {
			origin:    "origin-1",
			wantFound: false,
			wantErr:   nil,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			if err := batchv1.AddToScheme(scheme); err != nil {
				t.Fatalf("adding batch scheme: %v", err)
			}

			remoteClient := fake.NewClientBuilder().WithScheme(scheme).Build()
			if tc.obj != nil {
				remoteClient = fake.NewClientBuilder().WithScheme(scheme).WithObjects(tc.obj).Build()
			}

			ctx, _ := utiltesting.ContextWithLog(t)
			found, err := jobframework.ValidateRemoteObjectOwnership(ctx, remoteClient, key, gvk, tc.origin)
			if diff := cmp.Diff(err, tc.wantErr, cmpopts.EquateErrors()); diff != "" {
				t.Fatalf("ValidateRemoteObjectOwnership() error = %v, wantErr %v", err, tc.wantErr)
			}
			if found != tc.wantFound {
				t.Fatalf("ValidateRemoteObjectOwnership() found = %v, wantFound %v", found, tc.wantFound)
			}
		})
	}
}

func TestValidateMultiKueueObjectAssociation(t *testing.T) {
	const (
		origin       = "origin-1"
		workloadName = "workload-1"
	)
	longWorkloadName := "workload-name-that-is-longer-than-the-sixty-three-character-label-limit"

	tests := map[string]struct {
		featureGate bool
		labels      map[string]string
		annotations map[string]string
		wantErr     error
		workload    string
	}{
		"label survives gate enable": {
			featureGate: true,
			labels: map[string]string{
				kueue.MultiKueueOriginLabel:     origin,
				constants.PrebuiltWorkloadLabel: workloadName,
			},
		},
		"annotation survives gate disable": {
			featureGate: false,
			labels:      map[string]string{kueue.MultiKueueOriginLabel: origin},
			annotations: map[string]string{constants.PrebuiltWorkloadAnnotation: workloadName},
		},
		"matching label and annotation are accepted": {
			featureGate: true,
			labels: map[string]string{
				kueue.MultiKueueOriginLabel:     origin,
				constants.PrebuiltWorkloadLabel: workloadName,
			},
			annotations: map[string]string{constants.PrebuiltWorkloadAnnotation: workloadName},
		},
		"conflicting label and annotation are rejected": {
			featureGate: false,
			labels: map[string]string{
				kueue.MultiKueueOriginLabel:     origin,
				constants.PrebuiltWorkloadLabel: workloadName,
			},
			annotations: map[string]string{constants.PrebuiltWorkloadAnnotation: "another-workload"},
			wantErr:     jobframework.ErrRemoteObjectNotOwnedByMultiKueue,
		},
		"long annotation survives gate disable": {
			featureGate: false,
			labels:      map[string]string{kueue.MultiKueueOriginLabel: origin},
			annotations: map[string]string{constants.PrebuiltWorkloadAnnotation: longWorkloadName},
			workload:    longWorkloadName,
		},
		"wrong origin is rejected": {
			featureGate: true,
			labels: map[string]string{
				kueue.MultiKueueOriginLabel:     "another-origin",
				constants.PrebuiltWorkloadLabel: workloadName,
			},
			wantErr: jobframework.ErrRemoteObjectNotOwnedByMultiKueue,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGatesDuringTest(t, map[featuregate.Feature]bool{features.WorkloadIdentifierAnnotations: tc.featureGate})
			job := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{
				Name:        "job",
				Namespace:   "default",
				Labels:      tc.labels,
				Annotations: tc.annotations,
			}}
			expectedWorkload := tc.workload
			if expectedWorkload == "" {
				expectedWorkload = workloadName
			}
			err := jobframework.ValidateMultiKueueObjectAssociation(job, jobframework.MultiKueueObjectAssociation{
				Origin:       origin,
				WorkloadName: expectedWorkload,
			})
			if diff := cmp.Diff(tc.wantErr, err, cmpopts.EquateErrors()); diff != "" {
				t.Fatalf("ValidateMultiKueueObjectAssociation() error (-want,+got):\n%s", diff)
			}
		})
	}

	t.Run("empty origin is rejected", func(t *testing.T) {
		job := &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: "job", Namespace: "default"}}
		err := jobframework.ValidateMultiKueueObjectAssociation(job, jobframework.MultiKueueObjectAssociation{WorkloadName: workloadName})
		if !errors.Is(err, jobframework.ErrMultiKueueOriginEmpty) {
			t.Fatalf("ValidateMultiKueueObjectAssociation() error = %v, want %v", err, jobframework.ErrMultiKueueOriginEmpty)
		}
	})
}

func TestDeleteRemoteObjectIfOwned(t *testing.T) {
	makeJob := func(key types.NamespacedName, labels map[string]string) *batchv1.Job {
		return &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: key.Name, Namespace: key.Namespace, Labels: labels}}
	}

	key := types.NamespacedName{Name: "test-job", Namespace: "default"}
	const defaultOrigin = "origin-1"
	boomErr := errors.New("boom")

	tests := map[string]struct {
		remoteObjects []client.Object
		remoteClient  func(*runtime.Scheme, ...client.Object) client.Client
		origin        string
		wantErr       error
		wantDeleted   bool
	}{
		"empty origin returns error": {
			origin:  "",
			wantErr: jobframework.ErrMultiKueueOriginEmpty,
		},
		"not found skips delete": {
			origin: defaultOrigin,
		},
		"origin mismatch skips delete": {
			origin:        defaultOrigin,
			remoteObjects: []client.Object{makeJob(key, map[string]string{kueue.MultiKueueOriginLabel: "other-origin"})},
		},
		"remote get error returns error": {
			origin: defaultOrigin,
			remoteClient: func(scheme *runtime.Scheme, objs ...client.Object) client.Client {
				base := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...).Build()
				return interceptor.NewClient(base, interceptor.Funcs{
					Get: func(context.Context, client.WithWatch, client.ObjectKey, client.Object, ...client.GetOption) error {
						return boomErr
					},
				})
			},
			wantErr: boomErr,
		},
		"owned object triggers adapter delete": {
			remoteObjects: []client.Object{makeJob(key, map[string]string{kueue.MultiKueueOriginLabel: defaultOrigin})},
			origin:        defaultOrigin,
			wantDeleted:   true,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			if err := batchv1.AddToScheme(scheme); err != nil {
				t.Fatalf("adding batch scheme: %v", err)
			}
			localClient := fake.NewClientBuilder().WithScheme(scheme).Build()
			mockCtrl := gomock.NewController(t)
			adapter := mocks.NewMockMultiKueueAdapter(mockCtrl)
			adapter.EXPECT().GVK().Return(batchv1.SchemeGroupVersion.WithKind("Job")).AnyTimes()

			remoteClient := client.Client(fake.NewClientBuilder().WithScheme(scheme).WithObjects(tc.remoteObjects...).Build())
			if tc.remoteClient != nil {
				remoteClient = tc.remoteClient(scheme, tc.remoteObjects...)
			}

			if tc.wantDeleted {
				adapter.EXPECT().DeleteRemoteObject(gomock.Any(), gomock.Any(), gomock.Any(), key).Return(nil).Times(1)
			}

			ctx, _ := utiltesting.ContextWithLog(t)
			err := jobframework.DeleteRemoteObjectIfOwned(ctx, localClient, remoteClient, adapter, key, tc.origin)
			if diff := cmp.Diff(err, tc.wantErr, cmpopts.EquateErrors()); diff != "" {
				t.Fatalf("DeleteRemoteObjectIfOwned() error = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}

func TestDeleteRemoteObjectForWorkloadIfOwned(t *testing.T) {
	key := types.NamespacedName{Name: "test-job", Namespace: "default"}
	const (
		origin       = "origin-1"
		workloadName = "workload-1"
	)
	makeRemoteJob := func(remoteWorkloadName string) *batchv1.Job {
		return &batchv1.Job{ObjectMeta: metav1.ObjectMeta{
			Name:      key.Name,
			Namespace: key.Namespace,
			UID:       "remote-uid",
			Labels: map[string]string{
				kueue.MultiKueueOriginLabel:     origin,
				constants.PrebuiltWorkloadLabel: remoteWorkloadName,
			},
		}}
	}
	makeClients := func(t *testing.T, remoteObjects ...client.Object) (client.Client, client.Client) {
		t.Helper()
		scheme := runtime.NewScheme()
		if err := batchv1.AddToScheme(scheme); err != nil {
			t.Fatalf("adding batch scheme: %v", err)
		}
		return fake.NewClientBuilder().WithScheme(scheme).Build(), fake.NewClientBuilder().WithScheme(scheme).WithObjects(remoteObjects...).Build()
	}

	t.Run("cleanup-aware adapter receives exact identity and copied Workload context", func(t *testing.T) {
		localClient, remoteClient := makeClients(t, makeRemoteJob(workloadName))
		mockCtrl := gomock.NewController(t)
		baseAdapter := mocks.NewMockMultiKueueAdapter(mockCtrl)
		baseAdapter.EXPECT().GVK().Return(batchv1.SchemeGroupVersion.WithKind("Job")).AnyTimes()
		adapter := &cleanupTrackingAdapter{MultiKueueAdapter: baseAdapter}
		localWorkload := &kueue.Workload{ObjectMeta: metav1.ObjectMeta{
			Name:        workloadName,
			Namespace:   key.Namespace,
			Annotations: map[string]string{"context": "manager"},
		}}

		ctx, _ := utiltesting.ContextWithLog(t)
		if err := jobframework.DeleteRemoteObjectForWorkloadIfOwned(ctx, localClient, remoteClient, adapter, key, localWorkload, origin); err != nil {
			t.Fatalf("DeleteRemoteObjectForWorkloadIfOwned() error = %v", err)
		}
		if adapter.cleanupContext == nil {
			t.Fatal("cleanup-aware adapter was not called")
		}
		if adapter.cleanupContext.RemoteObjectUID != "remote-uid" {
			t.Fatalf("RemoteObjectUID = %q, want remote-uid", adapter.cleanupContext.RemoteObjectUID)
		}
		if adapter.cleanupContext.WorkloadKey != (types.NamespacedName{Name: workloadName, Namespace: key.Namespace}) {
			t.Fatalf("WorkloadKey = %q", adapter.cleanupContext.WorkloadKey)
		}
		if _, mutated := localWorkload.Annotations["mutated-by-adapter"]; mutated {
			t.Fatal("adapter mutation changed the manager Workload annotations")
		}
	})

	t.Run("cleanup-aware adapter rejects another Workload association", func(t *testing.T) {
		localClient, remoteClient := makeClients(t, makeRemoteJob("another-workload"))
		mockCtrl := gomock.NewController(t)
		baseAdapter := mocks.NewMockMultiKueueAdapter(mockCtrl)
		baseAdapter.EXPECT().GVK().Return(batchv1.SchemeGroupVersion.WithKind("Job")).AnyTimes()
		adapter := &cleanupTrackingAdapter{MultiKueueAdapter: baseAdapter}
		localWorkload := &kueue.Workload{ObjectMeta: metav1.ObjectMeta{Name: workloadName, Namespace: key.Namespace}}

		ctx, _ := utiltesting.ContextWithLog(t)
		if err := jobframework.DeleteRemoteObjectForWorkloadIfOwned(ctx, localClient, remoteClient, adapter, key, localWorkload, origin); err != nil {
			t.Fatalf("DeleteRemoteObjectForWorkloadIfOwned() error = %v", err)
		}
		if adapter.cleanupContext != nil {
			t.Fatal("cleanup-aware adapter was called for another Workload")
		}
	})

	t.Run("cleanup-aware adapter rejects an unexpected remote UID", func(t *testing.T) {
		localClient, remoteClient := makeClients(t, makeRemoteJob(workloadName))
		mockCtrl := gomock.NewController(t)
		baseAdapter := mocks.NewMockMultiKueueAdapter(mockCtrl)
		baseAdapter.EXPECT().GVK().Return(batchv1.SchemeGroupVersion.WithKind("Job")).AnyTimes()
		adapter := &cleanupTrackingAdapter{MultiKueueAdapter: baseAdapter}

		ctx, _ := utiltesting.ContextWithLog(t)
		err := jobframework.DeleteRemoteObjectWithCleanupContextIfOwned(ctx, localClient, remoteClient, adapter, key, jobframework.MultiKueueRemoteObjectCleanupContext{
			RemoteObjectUID: "another-uid",
			Association: jobframework.MultiKueueObjectAssociation{
				Origin:       origin,
				WorkloadName: workloadName,
			},
			WorkloadKey: types.NamespacedName{Name: workloadName, Namespace: key.Namespace},
		})
		if err != nil {
			t.Fatalf("DeleteRemoteObjectWithCleanupContextIfOwned() error = %v", err)
		}
		if adapter.cleanupContext != nil {
			t.Fatal("cleanup-aware adapter was called for an unexpected remote UID")
		}
	})

	t.Run("legacy adapter retains origin-only fallback", func(t *testing.T) {
		localClient, remoteClient := makeClients(t, makeRemoteJob("another-workload"))
		mockCtrl := gomock.NewController(t)
		adapter := mocks.NewMockMultiKueueAdapter(mockCtrl)
		adapter.EXPECT().GVK().Return(batchv1.SchemeGroupVersion.WithKind("Job")).AnyTimes()
		adapter.EXPECT().DeleteRemoteObject(gomock.Any(), gomock.Any(), gomock.Any(), key).Return(nil)
		localWorkload := &kueue.Workload{ObjectMeta: metav1.ObjectMeta{Name: workloadName, Namespace: key.Namespace}}

		ctx, _ := utiltesting.ContextWithLog(t)
		if err := jobframework.DeleteRemoteObjectForWorkloadIfOwned(ctx, localClient, remoteClient, adapter, key, localWorkload, origin); err != nil {
			t.Fatalf("DeleteRemoteObjectForWorkloadIfOwned() error = %v", err)
		}
	})

	t.Run("nil Workload is rejected", func(t *testing.T) {
		localClient, remoteClient := makeClients(t)
		mockCtrl := gomock.NewController(t)
		adapter := mocks.NewMockMultiKueueAdapter(mockCtrl)
		ctx, _ := utiltesting.ContextWithLog(t)
		err := jobframework.DeleteRemoteObjectForWorkloadIfOwned(ctx, localClient, remoteClient, adapter, key, nil, origin)
		if !errors.Is(err, jobframework.ErrMultiKueueWorkloadNameEmpty) {
			t.Fatalf("DeleteRemoteObjectForWorkloadIfOwned() error = %v, want %v", err, jobframework.ErrMultiKueueWorkloadNameEmpty)
		}
	})
}
