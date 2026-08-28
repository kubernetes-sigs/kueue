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
	"fmt"
	"reflect"

	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// GetUnstructured fetches an object without decoding it through the vendored
// typed API, preserving fields that the typed API does not model.
func GetUnstructured(ctx context.Context, c client.Reader, key types.NamespacedName, gvk schema.GroupVersionKind) (*unstructured.Unstructured, error) {
	obj := &unstructured.Unstructured{}
	obj.SetGroupVersionKind(gvk)
	if err := c.Get(ctx, key, obj); err != nil {
		return nil, err
	}
	return obj, nil
}

// CreateWithPreservedSpec creates an unstructured destination from
// the typed destination while applying its typed spec changes to the raw source
// spec. This preserves source fields unknown to the vendored API. Returning an
// unstructured object is important: converting back to a typed object would
// discard those fields during client serialization.
func CreateWithPreservedSpec(ctx context.Context, c client.Writer, source *unstructured.Unstructured, typedSource, typedDestination runtime.Object) error {
	obj, err := NewUnstructuredWithPreservedSpec(source, typedSource, typedDestination)
	if err != nil {
		return err
	}
	return c.Create(ctx, obj)
}

func NewUnstructuredWithPreservedSpec(source *unstructured.Unstructured, typedSource, typedDestination runtime.Object) (*unstructured.Unstructured, error) {
	sourceMap, err := runtime.DefaultUnstructuredConverter.ToUnstructured(typedSource)
	if err != nil {
		return nil, fmt.Errorf("converting typed source object: %w", err)
	}
	destinationMap, err := runtime.DefaultUnstructuredConverter.ToUnstructured(typedDestination)
	if err != nil {
		return nil, fmt.Errorf("converting typed destination object: %w", err)
	}

	sourceSpec, _, err := unstructured.NestedFieldNoCopy(sourceMap, "spec")
	if err != nil {
		return nil, fmt.Errorf("reading typed source spec: %w", err)
	}
	destinationSpec, _, err := unstructured.NestedFieldNoCopy(destinationMap, "spec")
	if err != nil {
		return nil, fmt.Errorf("reading typed destination spec: %w", err)
	}
	rawSourceSpec, found, err := unstructured.NestedFieldCopy(source.Object, "spec")
	if err != nil {
		return nil, fmt.Errorf("reading raw source spec: %w", err)
	}
	if found {
		mergedSpec := merge3Way(sourceSpec, destinationSpec, rawSourceSpec)
		if err := unstructured.SetNestedField(destinationMap, mergedSpec, "spec"); err != nil {
			return nil, fmt.Errorf("setting merged destination spec: %w", err)
		}
	}

	result := &unstructured.Unstructured{Object: destinationMap}
	result.SetGroupVersionKind(source.GroupVersionKind())
	return result, nil
}

var defaultMergeKeys = []string{"name", "manager", "key", "containerPort", "port", "type", "topologyKey", "ip"}

func merge3Way(base, target, raw any) any {
	baseMap, isBaseMap := base.(map[string]any)
	targetMap, isTargetMap := target.(map[string]any)
	rawMap, isRawMap := raw.(map[string]any)

	if isBaseMap && isTargetMap && isRawMap {
		resultMap := make(map[string]any, len(rawMap))
		for k, v := range rawMap {
			resultMap[k] = v
		}

		allKeys := make(map[string]struct{})
		for k := range baseMap {
			allKeys[k] = struct{}{}
		}
		for k := range targetMap {
			allKeys[k] = struct{}{}
		}
		for k := range rawMap {
			allKeys[k] = struct{}{}
		}

		for k := range allKeys {
			baseVal, baseHas := baseMap[k]
			targetVal, targetHas := targetMap[k]
			rawVal, rawHas := rawMap[k]

			switch {
			case baseHas && !targetHas:
				delete(resultMap, k)
			case !baseHas && targetHas:
				resultMap[k] = targetVal
			case baseHas && targetHas:
				if !rawHas {
					resultMap[k] = targetVal
				} else {
					resultMap[k] = merge3Way(baseVal, targetVal, rawVal)
				}
			case !baseHas && !targetHas && rawHas:
				resultMap[k] = rawVal
			}
		}
		return resultMap
	}

	baseSlice, isBaseSlice := base.([]any)
	targetSlice, isTargetSlice := target.([]any)
	rawSlice, isRawSlice := raw.([]any)

	if isBaseSlice && isTargetSlice && isRawSlice {
		for _, key := range defaultMergeKeys {
			baseIndexed, baseOK := isUniqueKeyedList(baseSlice, key)
			targetIndexed, targetOK := isUniqueKeyedList(targetSlice, key)
			rawIndexed, rawOK := isUniqueKeyedList(rawSlice, key)

			if baseOK && targetOK && rawOK && (len(baseSlice) > 0 || len(targetSlice) > 0 || len(rawSlice) > 0) {
				resultSlice := make([]any, 0, len(targetSlice))
				for _, targetElem := range targetSlice {
					targetMap := targetElem.(map[string]any)
					k := fmt.Sprintf("%v", targetMap[key])
					baseElem, baseExists := baseIndexed[k]
					rawElem, rawExists := rawIndexed[k]
					if rawExists {
						if baseExists {
							resultSlice = append(resultSlice, merge3Way(baseElem, targetElem, rawElem))
						} else {
							resultSlice = append(resultSlice, targetElem)
						}
						continue
					}
					resultSlice = append(resultSlice, targetElem)
				}

				// Preserve unmodeled elements in raw that were never known to typed base or target
				for _, rawElem := range rawSlice {
					rawMap := rawElem.(map[string]any)
					k := fmt.Sprintf("%v", rawMap[key])
					_, baseExists := baseIndexed[k]
					_, targetExists := targetIndexed[k]
					if !baseExists && !targetExists {
						resultSlice = append(resultSlice, rawElem)
					}
				}
				return resultSlice
			}
		}

		if len(baseSlice) == len(targetSlice) && len(baseSlice) == len(rawSlice) {
			resultSlice := make([]any, len(targetSlice))
			for i := range targetSlice {
				resultSlice[i] = merge3Way(baseSlice[i], targetSlice[i], rawSlice[i])
			}
			return resultSlice
		}

		if reflect.DeepEqual(baseSlice, targetSlice) {
			return rawSlice
		}
		return targetSlice
	}

	if reflect.DeepEqual(base, target) {
		return raw
	}
	return target
}

func isUniqueKeyedList(slice []any, key string) (map[string]map[string]any, bool) {
	if len(slice) == 0 {
		return map[string]map[string]any{}, true
	}
	indexed := make(map[string]map[string]any, len(slice))
	for _, item := range slice {
		itemMap, ok := item.(map[string]any)
		if !ok {
			return nil, false
		}
		val, found := itemMap[key]
		if !found || val == nil || val == "" {
			return nil, false
		}
		keyStr := fmt.Sprintf("%v", val)
		if _, duplicate := indexed[keyStr]; duplicate {
			return nil, false
		}
		indexed[keyStr] = itemMap
	}
	return indexed, true
}
