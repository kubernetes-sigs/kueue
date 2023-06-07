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

package slices

// ToMap creates a map[K]V out of the provided slice s using key() to create the map keys and
// val() to create the values.
//
// The caller can compare the length of the map to the one of the slice in order to detect
// potential key conflicts.
func ToMap[K comparable, V any, S ~[]E, E any](s S, key func(int) K, val func(int) V) map[K]V {
	if s == nil {
		return nil
	}

	if len(s) == 0 {
		return map[K]V{}
	}

	ret := make(map[K]V, len(s))
	for i := range s {
		ret[key(i)] = val(i)
	}
	return ret
}

// ToRefMap creates a map[K]*S out of the provided slice s []S using key() to create the map keys.
//
// The caller can compare the length of the map to the one of the slice in order to detect
// potential key conflicts.
func ToRefMap[K comparable, S ~[]E, E any](s S, key func(*E) K) map[K]*E {
	return ToMap(s,
		func(i int) K {
			return key(&s[i])
		},
		func(i int) *E {
			return &s[i]
		})
}
