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

package resources

import (
	"iter"

	corev1 "k8s.io/api/core/v1"
)

// Requests represents an abstract collection of resource requests.
type Requests interface {
	Clone() Requests
	ScaledUp(f int64) Requests
	ScaledDown(f int64) Requests
	// Add adds other into the receiver, element-wise. Values saturate at
	// math.MaxInt64 and math.MinInt64 instead of wrapping, so a total too large
	// to represent is never returned with the opposite sign. Implementations
	// have to agree on this, since the one in use is chosen by feature gate.
	Add(other Requests)
	// Sub subtracts other from the receiver, element-wise, saturating like Add.
	Sub(other Requests)
	Divide(f int64)
	Mul(f int64)
	CountIn(capacity Requests) int32
	CountInWithLimitingResource(capacity Requests) (int32, corev1.ResourceName)
	ResourceValue(name corev1.ResourceName) int64
	Set(name corev1.ResourceName, val int64)
	ForEach(fn func(name corev1.ResourceName, val int64))
	Iter() iter.Seq2[corev1.ResourceName, int64]
	Len() int
	IsEmpty() bool
	// FloorToZero replaces negative resource values with zero.
	FloorToZero()
	ToResourceList(formatter *ResourceFormatter) corev1.ResourceList
	// GreaterKeys returns the keys where the receiver is greater than other,
	// sorted alphabetically for deterministic output.
	GreaterKeys(other Requests) []corev1.ResourceName
	// GreaterKeysRL returns the keys where the receiver is greater than the
	// ResourceList value, sorted alphabetically for deterministic output.
	GreaterKeysRL(rl corev1.ResourceList) []corev1.ResourceName
}
