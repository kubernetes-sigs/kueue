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
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	mocks "sigs.k8s.io/kueue/internal/mocks/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
)

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
