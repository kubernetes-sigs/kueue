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
	"encoding/json"
	"net/http"

	jsonpatch "gomodules.xyz/jsonpatch/v2"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/utils/set"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

// WithLosslessDefaulter creates a new Handler for a CustomDefaulter interface that **drops** remove operations,
// which are typically the result of new API fields not present in Kueue libraries.
func WithLosslessDefaulter(scheme *runtime.Scheme, obj runtime.Object, defaulter admission.CustomDefaulter) admission.Handler {
	return &losslessDefaulter{
		Handler: admission.WithCustomDefaulter(scheme, obj, defaulter).Handler,
		decoder: admission.NewDecoder(scheme),
		object:  obj,
	}

}

type losslessDefaulter struct {
	admission.Handler
	decoder admission.Decoder
	object  runtime.Object
}

// Handle handles admission requests, **dropping** remove operations from patches produced by controller-runtime.
// The controller-runtime handler works by creating a jsondiff from the raw object and the marshalled
// version of the object modified by the defaulter. This generates "remove" operations for fields
// that are not present in the go types, which can occur when Kueue libraries are behind the latest
// released CRDs.
// Dropping the "remove" operations is safe because Kueue's job mutators never remove fields.
func (h *losslessDefaulter) Handle(ctx context.Context, req admission.Request) admission.Response {
	response := h.Handler.Handle(ctx, req)
	if needsCleanup(response) {
		// get the schema caused patch
		obj := h.object.DeepCopyObject()
		if err := h.decoder.Decode(req, obj); err != nil {
			return admission.Errored(http.StatusBadRequest, err)
		}

		marshalled, err := json.Marshal(obj)
		if err != nil {
			return admission.Errored(http.StatusInternalServerError, err)
		}
		schemePatch := admission.PatchResponseFromRaw(req.Object.Raw, marshalled)
		if len(schemePatch.Patches) > 0 {
			removedByScheme := set.New[string]()
			for _, p := range schemePatch.Patches {
				if p.Operation == "remove" {
					removedByScheme.Insert(p.Path)
				}
			}

			var patches []jsonpatch.Operation
			for _, p := range response.Patches {
				switch p.Operation {
				case "remove":
					if !removedByScheme.Has(p.Path) {
						patches = append(patches, p)
					}
				default:
					patches = append(patches, p)
				}
			}
			if len(patches) == 0 {
				response.PatchType = nil
			}
			response.Patches = patches
		}
	}
	return response
}

func needsCleanup(r admission.Response) bool {
	if !r.Allowed {
		return false
	}
	for _, p := range r.Patches {
		if p.Operation != "remove" {
			return true
		}
	}
	return false
}
