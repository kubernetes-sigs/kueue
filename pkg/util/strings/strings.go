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

package strings

import (
	"cmp"
	"maps"
	"slices"
	"strconv"
	"strings"

	sliceutils "sigs.k8s.io/kueue/pkg/util/slices"
)

func StringContainsSubstrings(s string, substrings ...string) bool {
	for _, substring := range substrings {
		if !strings.Contains(s, substring) {
			return false
		}
	}

	return true
}

// Join function for string-like types.
func Join[T ~string](a []T, sep string) string {
	strs := make([]string, len(a))
	for i, v := range a {
		strs[i] = string(v)
	}
	return strings.Join(strs, sep)
}

// JoinMap builds a string from a map of string to int.
// Keys are sorted alphabetically. Values are converted to strings and joined with valueSep.
// Entries are separated by entrySep.
// Example:
// m = map[string][]int{"a": {1, 2}, "b": {3}}
// keyValueSep = ":"
// valueSep = ","
// entrySep = "; "
// result = "a:1,2; b:3"
func JoinMap(m map[string][]int, keyValueSep string, valueSep string, entrySep string) string {
	itoa := func(value *int) string { return strconv.Itoa(*value) }

	keys := slices.Collect(maps.Keys(m))
	slices.SortFunc(keys, cmp.Compare)

	var builder strings.Builder
	for i, key := range keys {
		if i > 0 {
			builder.WriteString(entrySep)
		}
		builder.WriteString(key)
		if len(m[key]) > 0 {
			builder.WriteString(keyValueSep)

			indexes := strings.Join(sliceutils.Map(m[key], itoa), valueSep)
			builder.WriteString(indexes)
		}
	}
	return builder.String()
}
