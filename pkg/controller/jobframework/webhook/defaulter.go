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
			if p.Operation != "remove" || fieldExistsByJSONPointer(h.object, p.Path) {
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

func fieldExistsByJSONPointer(object interface{}, jsonPointer string) bool {
	// A JSON Pointer is a Unicode string containing a sequence of zero or more
	// reference tokens, each prefixed by a '/' character.
	// For more information, see https://datatracker.ietf.org/doc/html/rfc6901#section-3.
	if !strings.HasPrefix(jsonPointer, "/") {
		return false
	}
	return fieldExistsByReferenceTokens(object, strings.Split(jsonPointer, "/")[1:])
}

func fieldExistsByReferenceTokens(object interface{}, referenceTokens []string) bool {
	if object == nil {
		return false
	}

	if referenceTokens[0] == "" {
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
		field := t.Field(i)
		value := v.Field(i)

		if getJSONFieldName(field) != unescapeReferenceToken(referenceTokens[0]) {
			continue
		}

		if len(referenceTokens) == 1 {
			return true
		}

		switch value.Kind() {
		case reflect.Pointer:
			return fieldExistsByReferenceTokens(reflect.New(field.Type.Elem()).Interface(), referenceTokens[1:])
		case reflect.Struct:
			return fieldExistsByReferenceTokens(value.Interface(), referenceTokens[1:])
		case reflect.Array, reflect.Slice:
			if isInt(referenceTokens[1]) {
				if len(referenceTokens) > 2 {
					return fieldExistsByReferenceTokens(reflect.New(field.Type.Elem()).Interface(), referenceTokens[2:])
				} else {
					return true
				}
			}
		case reflect.Map:
			keyType := value.Type().Key().Kind()
			keyTypeNum := isNumericType(keyType)
			if keyType != reflect.String && (!keyTypeNum || !isNumeric(referenceTokens[1])) {
				return false
			}
			if len(referenceTokens) == 2 {
				return true
			}

			return fieldExistsByReferenceTokens(reflect.New(field.Type.Elem()).Interface(), referenceTokens[2:])
		}

		return false
	}

	return false
}

// Because the characters '~' and '/' have special meanings in JSON Pointer,
// '~' needs to be encoded as '~0' and '/' needs to be encoded as '~1'
// when these characters appear in a reference token.
// For more information see https://datatracker.ietf.org/doc/html/rfc6901#section-3.
func unescapeReferenceToken(fieldName string) string {
	fieldName = strings.ReplaceAll(fieldName, "~0", "~")
	fieldName = strings.ReplaceAll(fieldName, "~1", "/")
	return fieldName
}

func getJSONFieldName(field reflect.StructField) string {
	jsonTag := field.Tag.Get("json")
	if jsonTag == "" {
		jsonTag = field.Name
	} else if parts := strings.Split(jsonTag, ","); len(parts) > 1 {
		jsonTag = strings.Trim(parts[0], " ")
	}
	return jsonTag
}

func isInt(v string) bool {
	_, err := strconv.ParseInt(v, 10, 64)
	return err == nil
}

func isNumeric(s string) bool {
	if _, err := strconv.Atoi(s); err == nil {
		return true
	}
	if _, err := strconv.ParseFloat(s, 64); err == nil {
		return true
	}
	return false
}

func isNumericType(kind reflect.Kind) bool {
	switch kind {
	case reflect.Int, reflect.Int8, reflect.Int16, reflect.Int32, reflect.Int64,
		reflect.Uint, reflect.Uint8, reflect.Uint16, reflect.Uint32, reflect.Uint64,
		reflect.Float32, reflect.Float64:
		return true
	default:
		return false
	}
}
