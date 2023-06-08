/*
Copyright 2023 The Kubernetes Authors.

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

// replacing this with `https://pkg.go.dev/golang.org/x/exp/maps` should be considered
// when `x/exp/maps` graduates to stable.
package maps

// Clone clones the input preserving the difference between nil and empty
func Clone[K comparable, V any, S ~map[K]V](s S) S {
	if s == nil {
		return nil
	}

	if len(s) == 0 {
		return S{}
	}

	ret := make(S, len(s))
	for k, v := range s {
		ret[k] = v
	}
	return ret
}

// Merge merges a and b while resolving the conflicts by calling resolveConflict
func Merge[K comparable, V any, S ~map[K]V](a, b S, resolveConflict func(a, b V) V) S {
	if a == nil {
		return Clone(b)
	}

	ret := Clone(a)

	for k, v := range b {
		if _, found := ret[k]; found {
			ret[k] = resolveConflict(ret[k], v)
		} else {
			ret[k] = v
		}
	}
	return ret
}

// Merge merges a and b keeping the values in a in case of conflict
func MergeKeepFirst[K comparable, V any, S ~map[K]V](a, b S) S {
	return Merge(a, b, func(v, _ V) V { return v })
}
