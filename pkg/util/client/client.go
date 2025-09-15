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

	"sigs.k8s.io/controller-runtime/pkg/client"
)

type ClientPatchOption func(*ClientPatchOptions)

type ClientPatchOptions struct {
	Strict bool
}

func DefaultClientPatchOptions() *ClientPatchOptions {
	return &ClientPatchOptions{
		Strict: true, // default is strict
	}
}

func WithStrict(strict bool) ClientPatchOption {
	return func(o *ClientPatchOptions) {
		o.Strict = strict
	}
}

func patchCommon(obj client.Object, update func() (client.Object, bool, error), patchFunc func(patch client.Patch) error, options ...ClientPatchOption) error {
	opts := DefaultClientPatchOptions()
	for _, opt := range options {
		opt(opts)
	}
	strict := opts.Strict
	return updateAndPatch(obj, strict, update, patchFunc)
}

// Patch applies the merge patch of client.Object.
// If strict is true, the resourceVersion will be part of the patch, make this call fail if
// client.Object was changed.
func Patch(ctx context.Context, c client.Client, obj client.Object, update func() (client.Object, bool, error), options ...ClientPatchOption) error {
	return patchCommon(obj, update, func(patch client.Patch) error {
		return c.Patch(ctx, obj, patch)
	}, options...)
}

// PatchStatus applies the merge patch of client.Object status.
// The resourceVersion will be part of the patch, make this call fail if
// client.Object was changed.
func PatchStatus(ctx context.Context, c client.Client, obj client.Object, update func() (client.Object, bool, error), options ...ClientPatchOption) error {
	return patchCommon(obj, update, func(patch client.Patch) error {
		return c.Status().Patch(ctx, obj, patch)
	}, options...)
}

func getOriginalObject(obj client.Object, strict bool) client.Object {
	objOriginal := obj.DeepCopyObject().(client.Object)
	if strict {
		// Clearing ResourceVersion from the original object to make sure it is included in the generated patch.
		objOriginal.SetResourceVersion("")
	}
	return objOriginal
}

func updateAndPatch(obj client.Object, strict bool, update func() (client.Object, bool, error), patchFn func(client.Patch) error) error {
	objOriginal := getOriginalObject(obj, strict)
	objPatched, updated, err := update()
	if err != nil || !updated {
		return err
	}
	patch, err := createPatch(objOriginal, objPatched)
	if err != nil {
		return err
	}
	return patchFn(patch)
}

func createPatch(before, after client.Object) (client.Patch, error) {
	patchBase := client.MergeFrom(before)
	patchBytes, err := patchBase.Data(after)
	if err != nil {
		return nil, err
	}
	return client.RawPatch(patchBase.Type(), patchBytes), nil
}
