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

package jobframework

import (
	"context"
	"errors"

	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/apiutil"
)

// observedRemoteObjectClient binds an adapter's deletion to the object whose
// ownership was checked. Other objects (such as sibling pods) keep their adapter's
// deletion semantics and must not receive the representative object's identity.
type observedRemoteObjectClient struct {
	client.Client
	observed *metav1.PartialObjectMetadata
}

func (c *observedRemoteObjectClient) isObservedObject(obj client.Object) (bool, error) {
	if client.ObjectKeyFromObject(obj) != client.ObjectKeyFromObject(c.observed) {
		return false, nil
	}
	gvk, err := apiutil.GVKForObject(obj, c.Scheme())
	return gvk.GroupKind() == c.observed.GroupVersionKind().GroupKind(), err
}

func (c *observedRemoteObjectClient) Get(ctx context.Context, key types.NamespacedName, obj client.Object, opts ...client.GetOption) error {
	if err := c.Client.Get(ctx, key, obj, opts...); err != nil {
		return err
	}
	matches, err := c.isObservedObject(obj)
	if err != nil {
		return err
	}
	if matches && (obj.GetUID() != c.observed.UID || obj.GetResourceVersion() != c.observed.ResourceVersion) {
		return apierrors.NewConflict(c.observed.GroupVersionKind().GroupVersion().WithResource(c.observed.Kind).GroupResource(), key.Name,
			errors.New("remote object changed after its ownership was checked"))
	}
	return nil
}

func (c *observedRemoteObjectClient) Delete(ctx context.Context, obj client.Object, opts ...client.DeleteOption) error {
	matches, err := c.isObservedObject(obj)
	if err != nil {
		return err
	}
	if matches {
		options := (&client.DeleteOptions{}).ApplyOptions(opts)
		if p := options.Preconditions; p != nil &&
			((p.UID != nil && *p.UID != c.observed.UID) || (p.ResourceVersion != nil && *p.ResourceVersion != c.observed.ResourceVersion)) {
			return apierrors.NewConflict(c.observed.GroupVersionKind().GroupVersion().WithResource(c.observed.Kind).GroupResource(), obj.GetName(),
				errors.New("adapter deletion preconditions differ from the observed remote object"))
		}
		opts = append(opts, client.Preconditions{UID: &c.observed.UID, ResourceVersion: &c.observed.ResourceVersion})
	}
	return c.Client.Delete(ctx, obj, opts...)
}
