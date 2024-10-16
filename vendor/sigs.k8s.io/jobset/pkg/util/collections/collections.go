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
package collections

func Concat[T any](slices ...[]T) []T {
	var result = make([]T, 0)
	for _, slice := range slices {
		result = append(result, slice...)
	}
	return result
}

func CloneMap[K, V comparable](m map[K]V) map[K]V {
	copy := make(map[K]V)
	for k, v := range m {
		copy[k] = v
	}
	return copy
}

func Contains[T comparable](slice []T, element T) bool {
	for _, item := range slice {
		if item == element {
			return true
		}
	}
	return false
}

func IndexOf[T comparable](slice []T, item T) int {
	for i, v := range slice {
		if v == item {
			return i
		}
	}
	return -1
}

// MergeMaps will merge the `old` and `new` maps and return the
// merged map. If a key appears in both maps, the key-value pair
// in the `new` map will overwrite the value in the `old` map.
func MergeMaps[K comparable, V any](old, new map[K]V) map[K]V {
	merged := make(map[K]V)
	for k, v := range old {
		merged[k] = v
	}
	for k, v := range new {
		merged[k] = v // Overwrite if duplicate
	}
	return merged
}

func MergeSlices[T comparable](s1, s2 []T) []T {
	mergedSet := make(map[T]bool)

	// Add elements from s1 to the set
	for _, item := range s1 {
		mergedSet[item] = true
	}

	// Add elements from s2, only if they are not already in the set
	for _, item := range s2 {
		if _, exists := mergedSet[item]; !exists {
			mergedSet[item] = true
		}
	}

	// Convert the set back into a slice
	mergedSlice := make([]T, 0, len(mergedSet))
	for item := range mergedSet {
		mergedSlice = append(mergedSlice, item)
	}

	return mergedSlice
}
