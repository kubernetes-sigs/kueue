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

// UpdateFunc is a function that modifies an object in-place.
// It returns a bool indicating whether an update was made, and an error if any occurred.
type UpdateFunc func() (bool, error)

// PatchOption defines a functional option for customizing PatchOptions.
// It follows the functional options pattern, allowing callers to configure
// patch behavior at call sites without directly manipulating PatchOptions.
type PatchOption func(*PatchOptions)

// PatchOptions contains configuration parameters that control how patches
// are generated and applied.
//
// Fields:
//   - Strict: Controls whether ResourceVersion should always be cleared
//     from the "original" object to ensure its inclusion in the generated
//     patch. Defaults to true. Setting Strict=false preserves the current
//     ResourceVersion.
//
// Typically, PatchOptions are constructed via DefaultPatchOptions and
// modified using PatchOption functions (e.g., WithLoose).
type PatchOptions struct {
	Strict bool
}

// DefaultPatchOptions returns a new PatchOptions instance configured with
// default settings.
//
// By default, Strict is set to true, meaning ResourceVersion is cleared
// from the original object so it will always be included in the generated
// patch. This ensures stricter version handling during patch application.
func DefaultPatchOptions() *PatchOptions {
	return &PatchOptions{
		Strict: true, // default is strict
	}
}

// WithLoose returns a PatchOption that sets the Strict field on PatchOptions.
//
// By default, Strict is true. In strict mode, generated patches enforce stricter
// behavior by clearing the ResourceVersion field from the "original" object.
// This ensures that the ResourceVersion is always included in the generated patch
// and taken into account during patch application.
//
// Example:
//
//	patch := clientutil.Patch(ctx, c, obj, true, func() (bool, error) {
//	    return updateFn(obj), nil
//	}, WithLoose()) // disables strict mode
func WithLoose() PatchOption {
	return func(o *PatchOptions) {
		o.Strict = false
	}
}

// Patch applies an update to a Kubernetes object using a patch-based workflow.
//
// The function first computes the "original" and "modified" states of the object
// by invoking the provided update callback, then generates a patch describing
// the changes. Finally, it applies the patch through the given client.Client.
//
// Behavior is influenced by PatchOptions, which can be customized using
// PatchOption functional arguments (e.g., WithLoose).
//
// Returns:
//   - error: If patch generation or application fails, an error is returned.
//
// Example:
//
//	err := Patch(ctx, client, obj, func() (bool, error) {
//	    obj.SetLabels(map[string]string{"app": "demo"})
//	    return true, nil
//	}, WithLoose())
//	if err != nil {
//	    log.Fatal(err)
//	}
//
// Notes:
//   - By default, patches are generated in "strict" mode (Strict=true).
//     This clears the ResourceVersion field in the original object to ensure
//     it is always included in the generated patch.
func Patch(ctx context.Context, c client.Client, obj client.Object, update UpdateFunc, options ...PatchOption) error {
	return patchCommon(obj, update, func(patch client.Patch) error {
		return c.Patch(ctx, obj, patch)
	}, options...)
}

// PatchStatus applies an update to the *status* subresource of a Kubernetes object
// using a patch-based workflow.
//
// The function computes the "original" and "modified" states of the object by
// invoking the provided update callback, generates a patch describing the changes,
// and applies the patch specifically through the client's status interface
// (c.Status().Patch).
//
// Behavior is influenced by PatchOptions, which can be customized using
// PatchOption functional arguments (e.g., WithLoose).
//
// Returns:
//   - error: If patch generation or application fails, an error is returned.
//
// Example:
//
//	err := PatchStatus(ctx, client, obj, func() (bool, error) {
//	    obj.Status.Phase = "Ready"
//	    return true, nil
//	})
//	if err != nil {
//	    log.Fatal(err)
//	}
//
// Notes:
//   - By default, patches are generated in "strict" mode (Strict=true).
//     This clears the ResourceVersion field in the original object to ensure
//     it is always included in the generated patch.
func PatchStatus(ctx context.Context, c client.Client, obj client.Object, update UpdateFunc, options ...PatchOption) error {
	return patchCommon(obj, update, func(patch client.Patch) error {
		return c.Status().Patch(ctx, obj, patch)
	}, options...)
}

// patchFunc is a function that applies the given client.Patch to a resource.
// It returns an error if the patch could not be applied.
type patchFunc func(patch client.Patch) error

func patchCommon(obj client.Object, updateFn UpdateFunc, patchFn patchFunc, options ...PatchOption) error {
	opts := DefaultPatchOptions()
	for _, opt := range options {
		opt(opts)
	}
	return executePatch(obj, opts, updateFn, patchFn)
}

func executePatch(obj client.Object, options *PatchOptions, update UpdateFunc, patchFn patchFunc) error {
	objOriginal := getOriginalObject(obj, options)
	updated, err := update()
	if err != nil || !updated {
		return err
	}
	patch, err := createPatch(objOriginal, obj)
	if err != nil {
		return err
	}
	return patchFn(patch)
}

// getOriginalObject creates and returns a deep copy of the given client.Object.
// The copy represents the "original" state of the object before applying any
// modifications. This is typically used when generating a patch.
//
// If PatchOptions.Strict is set to true, the function clears the
// ResourceVersion field on the copied object. This ensures that the
// ResourceVersion is included in the generated patch, enforcing stricter
// version handling during the patch application.
func getOriginalObject(obj client.Object, options *PatchOptions) client.Object {
	objOriginal := obj.DeepCopyObject().(client.Object)
	if options.Strict {
		// Clearing ResourceVersion from the original object to make sure it is included in the generated patch.
		objOriginal.SetResourceVersion("")
	}
	return objOriginal
}

func createPatch(before, after client.Object) (client.Patch, error) {
	patchBase := client.MergeFrom(before)
	patchBytes, err := patchBase.Data(after)
	if err != nil {
		return nil, err
	}
	return client.RawPatch(patchBase.Type(), patchBytes), nil
}
