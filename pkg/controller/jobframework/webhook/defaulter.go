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
	"reflect"
	"strconv"
	"strings"

	"gomodules.xyz/jsonpatch/v2"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/webhook/admission"
)

// WithLosslessDefaulter creates a new Handler for a CustomDefaulter interface that **drops** remove operations,
// which are typically the result of new API fields not present in Kueue libraries.
func WithLosslessDefaulter(scheme *runtime.Scheme, obj runtime.Object, defaulter admission.CustomDefaulter) admission.Handler {
	return &losslessDefaulter{
		Handler: admission.WithCustomDefaulter(scheme, obj, defaulter).Handler,
		object:  obj,
	}
}

type losslessDefaulter struct {
	admission.Handler
	object runtime.Object
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
			if p.Operation != "remove" || fieldExistsByJSONPath(h.object, p.Path) {
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

func fieldExistsByJSONPath(object interface{}, path string) bool {
	pathParts := strings.Split(path, "/")
	// Invalid path. For more information, see https://jsonpatch.com/.
	if len(pathParts) < 2 || len(pathParts) > 0 && pathParts[0] != "" {
		return false
	}
	return fieldExistsByJSONPathHelper(object, pathParts[1:])
}

func fieldExistsByJSONPathHelper(object interface{}, pathParts []string) bool {
	if object == nil {
		return false
	}

	t := reflect.TypeOf(object)
	v := reflect.ValueOf(object)

	if v.Kind() == reflect.Pointer {
		t = t.Elem()
		v = v.Elem()
	}

	if t.Kind() != reflect.Struct {
		return false
	}

	for i := 0; i < t.NumField(); i++ {
		ft := t.Field(i)
		fv := v.Field(i)

		if getFieldName(ft) != pathParts[0] {
			continue
		}

		if len(pathParts) == 1 {
			return true
		}

		switch fv.Kind() {
		case reflect.Pointer:
			return fieldExistsByJSONPathHelper(reflect.New(ft.Type.Elem()).Interface(), pathParts[1:])
		case reflect.Struct:
			return fieldExistsByJSONPathHelper(fv.Interface(), pathParts[1:])
		case reflect.Array, reflect.Slice:
			if len(pathParts) > 2 && isInt(pathParts[1]) {
				return fieldExistsByJSONPathHelper(reflect.New(ft.Type.Elem()).Interface(), pathParts[2:])
			}
		}

		return false
	}

	return false
}

func getFieldName(fieldType reflect.StructField) string {
	fieldName := fieldType.Tag.Get("json")

	if fieldName == "" {
		fieldName = fieldType.Name
	} else if tagParts := strings.Split(fieldName, ","); len(tagParts) > 1 {
		fieldName = strings.Trim(tagParts[0], " ")
	}

	return fieldName
}

func isInt(v string) bool {
	_, err := strconv.ParseInt(v, 10, 64)
	return err == nil
}
