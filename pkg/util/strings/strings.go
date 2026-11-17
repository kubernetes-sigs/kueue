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

import "strings"

// TruncateNamePrefix cuts s to at most n bytes for use as the prefix of a
// generated object name, then drops any trailing '.', since a '.' followed by
// the '-' separator is not a valid DNS subdomain. A trailing '-' is kept: it
// produces a valid name, and trimming it would rename existing objects.
func TruncateNamePrefix(s string, n int) string {
	if n <= 0 {
		return ""
	}
	return strings.TrimRight(s[:min(len(s), n)], ".")
}

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
