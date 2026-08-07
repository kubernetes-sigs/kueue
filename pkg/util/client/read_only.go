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

package client

import (
	"context"
	"fmt"

	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func ErrWriteForbidden(op string) error {
	return fmt.Errorf("write operation %q not allowed in follower mode", op)
}

type readOnlyClient struct {
	client.Client
}

func NewReadOnlyClient(c client.Client) client.Client {
	return &readOnlyClient{Client: c}
}

// Mutating Methods on client.Client

func (r *readOnlyClient) Create(ctx context.Context, obj client.Object, opts ...client.CreateOption) error {
	return ErrWriteForbidden("Create")
}

func (r *readOnlyClient) Update(ctx context.Context, obj client.Object, opts ...client.UpdateOption) error {
	return ErrWriteForbidden("Update")
}

func (r *readOnlyClient) Delete(ctx context.Context, obj client.Object, opts ...client.DeleteOption) error {
	return ErrWriteForbidden("Delete")
}

func (r *readOnlyClient) DeleteAllOf(ctx context.Context, obj client.Object, opts ...client.DeleteAllOfOption) error {
	return ErrWriteForbidden("DeleteAllOf")
}

func (r *readOnlyClient) Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.PatchOption) error {
	return ErrWriteForbidden("Patch")
}

// Status SubResource Writer (.Status())

func (r *readOnlyClient) Status() client.SubResourceWriter {
	return &readOnlySubResourceWriter{}
}

type readOnlySubResourceWriter struct{}

func (w *readOnlySubResourceWriter) Apply(ctx context.Context, obj runtime.ApplyConfiguration, opts ...client.SubResourceApplyOption) error {
	return ErrWriteForbidden("Status.Apply")
}

func (w *readOnlySubResourceWriter) Create(ctx context.Context, obj client.Object, subResource client.Object, opts ...client.SubResourceCreateOption) error {
	return ErrWriteForbidden("Status.Create")
}

func (w *readOnlySubResourceWriter) Update(ctx context.Context, obj client.Object, opts ...client.SubResourceUpdateOption) error {
	return ErrWriteForbidden("Status.Update")
}

func (w *readOnlySubResourceWriter) Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
	return ErrWriteForbidden("Status.Patch")
}

// Generic SubResource Client (.SubResource(name))

func (r *readOnlyClient) SubResource(subResource string) client.SubResourceClient {
	return &readOnlySubResourceClient{client: r.Client.SubResource(subResource)}
}

type readOnlySubResourceClient struct {
	client client.SubResourceClient
}

func (w *readOnlySubResourceClient) Get(ctx context.Context, obj client.Object, subResource client.Object, opts ...client.SubResourceGetOption) error {
	return w.client.Get(ctx, obj, subResource, opts...)
}

func (w *readOnlySubResourceClient) Create(ctx context.Context, obj client.Object, subResource client.Object, opts ...client.SubResourceCreateOption) error {
	return ErrWriteForbidden("SubResource.Create")
}

func (w *readOnlySubResourceClient) Update(ctx context.Context, obj client.Object, opts ...client.SubResourceUpdateOption) error {
	return ErrWriteForbidden("SubResource.Update")
}

func (w *readOnlySubResourceClient) Patch(ctx context.Context, obj client.Object, patch client.Patch, opts ...client.SubResourcePatchOption) error {
	return ErrWriteForbidden("SubResource.Patch")
}

func (w *readOnlySubResourceClient) Apply(ctx context.Context, obj runtime.ApplyConfiguration, opts ...client.SubResourceApplyOption) error {
	return ErrWriteForbidden("SubResource.Apply")
}
