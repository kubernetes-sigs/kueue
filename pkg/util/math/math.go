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

import (
	stdmath "math"

	"k8s.io/apimachinery/pkg/api/resource"
)

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

var (
	maxMilliValue = *resource.NewMilliQuantity(stdmath.MaxInt64, resource.DecimalSI)
	minMilliValue = *resource.NewMilliQuantity(stdmath.MinInt64, resource.DecimalSI)
)

// SafeMilliValue returns the integer milli-value for the resource quantity, clamped to math.MinInt64 and math.MaxInt64.
func SafeMilliValue(q resource.Quantity) int64 {
	if q.Cmp(maxMilliValue) > 0 {
		return stdmath.MaxInt64
	}
	if q.Cmp(minMilliValue) < 0 {
		return stdmath.MinInt64
	}
	return q.MilliValue()
}

// SaturatingMul multiplies two int64s, returning math.MaxInt64 or math.MinInt64 if the
// result would overflow or underflow.
func SaturatingMul(a, b int64) int64 {
	if a == 0 || b == 0 {
		return 0
	}
	// Explicitly catch the MinInt64 * -1 edge case.
	// In two's complement, the absolute value of MinInt64 is 1 greater than MaxInt64.
	// If we multiply it by -1, it overflows to MinInt64. Go safely handles MinInt64 / -1
	// by evaluating it back to MinInt64. Because of this, our division check
	// (res/b != a) evaluates to false and fails to detect the overflow.
	if (a == -1 && b == stdmath.MinInt64) || (b == -1 && a == stdmath.MinInt64) {
		return stdmath.MaxInt64
	}
	res := a * b
	if res/b != a {
		if (a < 0) == (b < 0) {
			return stdmath.MaxInt64
		}
		return stdmath.MinInt64
	}
	return res
}
