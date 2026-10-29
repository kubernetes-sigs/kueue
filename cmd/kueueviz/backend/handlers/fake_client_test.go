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

package handlers

import (
	"context"
	"testing"

	authorizationv1 "k8s.io/api/authorization/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	clientgoscheme "k8s.io/client-go/kubernetes/scheme"
	"kueueviz/middleware"
	ctrlcache "sigs.k8s.io/controller-runtime/pkg/cache"
	ctrlclient "sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	kueueapi "sigs.k8s.io/kueue/apis/kueue/v1beta2"
)

// fakeReaderClient adapts a controller-runtime fake client to the Client
// interface used by the handlers. Informers are not needed by the fetch
// functions, so GetInformerForKind returns nil.
type fakeReaderClient struct {
	ctrlclient.Reader
}

func (fakeReaderClient) GetInformerForKind(_ context.Context, _ schema.GroupVersionKind, _ ...ctrlcache.InformerGetOption) (ctrlcache.Informer, error) {
	return nil, nil
}

// newFakeClient returns a Client backed by the controller-runtime fake client,
// seeded with objs. funcs may be used to inject errors.
func newFakeClient(t *testing.T, funcs *interceptor.Funcs, objs ...ctrlclient.Object) Client {
	t.Helper()

	scheme := runtime.NewScheme()
	if err := clientgoscheme.AddToScheme(scheme); err != nil {
		t.Fatalf("adding client-go scheme: %v", err)
	}
	if err := kueueapi.AddToScheme(scheme); err != nil {
		t.Fatalf("adding kueue scheme: %v", err)
	}

	builder := fake.NewClientBuilder().WithScheme(scheme).WithObjects(objs...)
	if funcs != nil {
		builder = builder.WithInterceptorFuncs(*funcs)
	}
	return fakeReaderClient{Reader: builder.Build()}
}

// authorizerFunc lets a test decide per request whether access is allowed.
type authorizerFunc func(attrs authorizationv1.ResourceAttributes) (bool, error)

func (f authorizerFunc) Authorize(_ context.Context, _ middleware.Identity, attrs authorizationv1.ResourceAttributes) (bool, error) {
	return f(attrs)
}
