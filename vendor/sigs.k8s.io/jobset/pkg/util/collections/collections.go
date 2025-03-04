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
