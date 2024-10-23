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

package maps

import "maps"

// Merge merges a and b while resolving the conflicts by calling commonKeyValue
func Merge[K comparable, V any, S ~map[K]V](a, b S, commonKeyValue func(a, b V) V) S {
	if a == nil {
		return maps.Clone(b)
	}

	ret := maps.Clone(a)

	for k, v := range b {
		if _, found := a[k]; found {
			ret[k] = commonKeyValue(a[k], v)
		} else {
			ret[k] = v
		}
	}
	return ret
}

// MergeKeepFirst merges a and b keeping the values in a in case of conflict
func MergeKeepFirst[K comparable, V any, S ~map[K]V](a, b S) S {
	return Merge(a, b, func(v, _ V) V { return v })
}
