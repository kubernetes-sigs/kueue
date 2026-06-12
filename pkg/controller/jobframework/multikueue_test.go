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
	makeJob := func(labels map[string]string) *batchv1.Job {
		return &batchv1.Job{ObjectMeta: metav1.ObjectMeta{Name: "test", Namespace: "default", Labels: labels}}
	}

	tests := map[string]struct {
		obj     *batchv1.Job
		origin  string
		wantErr bool
	}{
		"origin not set fails validation": {
			obj:     makeJob(nil),
			origin:  "",
			wantErr: true,
		},
		"origin set and label missing": {
			obj:     makeJob(nil),
			origin:  "origin-1",
			wantErr: true,
		},
		"origin set and label present": {
			obj:     makeJob(map[string]string{kueue.MultiKueueOriginLabel: "origin-1"}),
			origin:  "origin-1",
			wantErr: false,
		},
		"origin set and label mismatched": {
			obj:     makeJob(map[string]string{kueue.MultiKueueOriginLabel: "origin-2"}),
			origin:  "origin-1",
			wantErr: true,
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)
			err := jobframework.ValidateRemoteObjectOwnership(ctx, tc.obj, tc.origin)
			if (err != nil) != tc.wantErr {
				t.Fatalf("ValidateRemoteObjectOwnership() error = %v, wantErr %v", err, tc.wantErr)
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

	tests := map[string]struct {
		remoteObjects []client.Object
		remoteClient  func(*runtime.Scheme, ...client.Object) client.Client
		origin        string
		wantErr       bool
		wantDeleted   bool
	}{
		"empty origin returns error": {
			origin:  "",
			wantErr: true,
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
						return errors.New("boom")
					},
				})
			},
			wantErr: true,
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
			if (err != nil) != tc.wantErr {
				t.Fatalf("DeleteRemoteObjectIfOwned() error = %v, wantErr %v", err, tc.wantErr)
			}
		})
	}
}
