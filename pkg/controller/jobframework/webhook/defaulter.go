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

package webhook

import (
	"context"

	jsonpatch "gomodules.xyz/jsonpatch/v2"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

// WithLosslessDefaulter creates a new Handler for a CustomDefaulter interface that **drops** remove operations,
// which are typically the result of new API fields not present in Kueue libraries.
func WithLosslessDefaulter(scheme *runtime.Scheme, obj runtime.Object, defaulter admission.CustomDefaulter) admission.Handler {
	return &losslessDefaulter{
		Handler: admission.WithCustomDefaulter(scheme, obj, defaulter).Handler,
	}
}

type losslessDefaulter struct {
	admission.Handler
}

// Handle handles admission requests, **dropping** remove operations from patches produced by controller-runtime.
// The controller-runtime handler works by creating a jsondiff from the raw object and the marshalled
// version of the object modified by the defaulter. This generates "remove" operations for fields
// that are not present in the go types, which can occur when Kueue libraries are behind the latest
// released CRDs.
// Dropping the "remove" operations is safe because Kueue's job mutators never remove fields.
func (h *losslessDefaulter) Handle(ctx context.Context, req admission.Request) admission.Response {
	response := h.Handler.Handle(ctx, req)
	if response.Allowed {
		var patches []jsonpatch.Operation
		for _, p := range response.Patches {
			if p.Operation != "remove" {
				patches = append(patches, p)
			}
		}
		if len(patches) == 0 {
			response.PatchType = nil
		}
		response.Patches = patches
	}
	return response
}
