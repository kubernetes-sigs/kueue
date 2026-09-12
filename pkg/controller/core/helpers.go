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

package core

import (
	utilmath "sigs.k8s.io/kueue/pkg/util/math"
)

// WeightedShare converts a share into the int64 the API field carries. The
// field is documented as ranging from 0 to MaxInt64, and a share is a float64
// that can be NaN, infinite, or finite and past that range, so the conversion
// saturates rather than handing an out-of-range float to int64().
func WeightedShare(f float64) int64 {
	return utilmath.SaturatingCeilToNonNegativeInt64(f)
}
