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

package math

import stdmath "math"

// SaturatingAdd adds two int64s, returning math.MaxInt64 or math.MinInt64 if the
// result would overflow or underflow.
func SaturatingAdd(a, b int64) int64 {
	if b > 0 && a > stdmath.MaxInt64-b {
		return stdmath.MaxInt64
	}
	if b < 0 && a < stdmath.MinInt64-b {
		return stdmath.MinInt64
	}
	return a + b
}
