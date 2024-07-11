/*
Copyright 2024 The Kubernetes Authors.

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

func CreatePatch(before, after client.Object) (client.Patch, error) {
	patchBase := client.MergeFrom(before)
	patchBytes, err := patchBase.Data(after)
	if err != nil {
		return nil, err
	}
	return client.RawPatch(patchBase.Type(), patchBytes), nil
}

// Patch applies the merge patch of client.Object.
// If strict is true, the resourceVersion will be part of the patch, make this call fail if
// client.Object was changed.
func Patch(ctx context.Context, c client.Client, obj client.Object, strict bool, update func() (bool, error)) error {
	objOriginal := obj.DeepCopyObject().(client.Object)
	if strict {
		// Clearing ResourceVersion from the original object to make sure it is included in the generated patch.
		objOriginal.SetResourceVersion("")
	}
	updated, err := update()
	if err != nil || !updated {
		return err
	}
	patch, err := CreatePatch(objOriginal, obj)
	if err != nil {
		return err
	}
	if err = c.Patch(ctx, obj, patch); err != nil {
		return err
	}
	return nil
}

// PatchStatus applies the merge patch of client.Object status.
// The resourceVersion will be part of the patch, make this call fail if
// client.Object was changed.
func PatchStatus(ctx context.Context, c client.Client, obj client.Object, update func() (bool, error)) error {
	objOriginal := obj.DeepCopyObject().(client.Object)
	// Clearing ResourceVersion from the original object to make sure it is included in the generated patch.
	objOriginal.SetResourceVersion("")
	updated, err := update()
	if err != nil || !updated {
		return err
	}
	patch, err := CreatePatch(objOriginal, obj)
	if err != nil {
		return err
	}
	if err = c.Status().Patch(ctx, obj, patch); err != nil {
		return err
	}
	return nil
}
