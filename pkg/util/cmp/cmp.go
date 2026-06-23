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

package cmp

// CompareBool compares two boolean values and returns:
// - 0 if they are equal
// - -1 if a is true and b is false
// - 1 if a is false and b is true
func CompareBool(a, b bool) int {
	if a == b {
		return 0
	}

	if a {
		return -1
	}

	return 1
}

// LazyOr evaluates the provided functions in order and returns the first non-zero value.
// It returns the zero value if all functions return zero values.
// This is useful for implementing short-circuit evaluation in comparison functions.
func LazyOr[T comparable](funcs ...func() T) T {
	var zero T
	for _, fn := range funcs {
		n := fn()
		if n != zero {
			return n
		}
	}
	return zero
}
