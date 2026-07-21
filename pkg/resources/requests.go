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
	"maps"
	"math"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	resourcehelpers "k8s.io/component-helpers/resource"
	"k8s.io/utils/ptr"

	utilmath "sigs.k8s.io/kueue/pkg/util/math"
)

// The following resources calculations are inspired on
// https://github.com/kubernetes/kubernetes/blob/master/pkg/scheduler/framework/types.go

// MapRequests maps ResourceName to flavor to value; for CPU it is tracked in MilliCPU.
type MapRequests map[corev1.ResourceName]int64

func NewMapRequests(rl corev1.ResourceList) MapRequests {
	r := MapRequests{}
	for name, quant := range rl {
		r[name] = ResourceValue(name, quant)
	}
	return r
}

func NewRequestsFromPodSpec(podSpec *corev1.PodSpec) MapRequests {
	return NewMapRequests(resourcehelpers.PodRequests(&corev1.Pod{Spec: *podSpec}, resourcehelpers.PodResourcesOptions{}))
}

func (r MapRequests) Clone() MapRequests {
	return maps.Clone(r)
}

func (r MapRequests) ScaledUp(f int64) MapRequests {
	ret := r.Clone()
	ret.Mul(f)
	return ret
}

func (r MapRequests) ScaledDown(f int64) MapRequests {
	ret := r.Clone()
	ret.Divide(f)
	return ret
}

func (r MapRequests) Divide(f int64) {
	for k := range r {
		if r[k] == 0 && f == 0 {
			// Skip dividing by 0 when resources are 0.
			// This may happen when the function is used to scale down the
			// resources computed initially for all (0) Pods, and thus r[k] = 0.
			continue
		}
		r[k] /= f
	}
}

func (r MapRequests) Mul(f int64) {
	for k := range r {
		r[k] = utilmath.SaturatingMul(r[k], f)
	}
}

func (r MapRequests) Add(addRequests MapRequests) {
	for k, v := range addRequests {
		r[k] += v
	}
}

func (r MapRequests) Sub(subRequests MapRequests) {
	for k, v := range subRequests {
		r[k] -= v
	}
}

func (r MapRequests) ToResourceList(formatter *ResourceFormatter) corev1.ResourceList {
	ret := make(corev1.ResourceList, len(r))
	for k, v := range r {
		ret[k] = formatter.ResourceQuantity(k, v)
	}
	return ret
}

// ResourceValue returns the integer value for the resource name.
// It's milli-units for CPU and absolute units for everything else.
func ResourceValue(name corev1.ResourceName, q resource.Quantity) int64 {
	if name == corev1.ResourceCPU {
		return utilmath.SafeMilliValue(q)
	}
	return q.Value()
}

// GreaterKeys returns keys where the receiver is greater than other.
func (r MapRequests) GreaterKeys(other MapRequests) []corev1.ResourceName {
	if len(r) == 0 || len(other) == 0 {
		return nil
	}
	var result []corev1.ResourceName
	for name, value := range r {
		if otherValue, found := other[name]; found && value > otherValue {
			result = append(result, name)
		}
	}
	if len(result) == 0 {
		return nil
	}
	return result
}

// GreaterKeysRL compares against a ResourceList and returns larger keys.
func (r MapRequests) GreaterKeysRL(rl corev1.ResourceList) []corev1.ResourceName {
	return r.GreaterKeys(NewMapRequests(rl))
}

func (r MapRequests) CountIn(capacity MapRequests) int32 {
	count, _ := r.CountInWithLimitingResource(capacity)
	return count
}

// CountInWithLimitingResource returns how many times the request fits into capacity
// and the resource that is most constraining (i.e., gave the minimum count).
// When multiple resources have the same count, ties are broken alphabetically
// by resource name for determinism.
func (r MapRequests) CountInWithLimitingResource(capacity MapRequests) (int32, corev1.ResourceName) {
	var (
		result           *int32
		limitingResource corev1.ResourceName
	)
	for rName, rValue := range r {
		cap, found := capacity[rName]
		if !found && rValue != 0 {
			return 0, rName
		}
		// find the minimum count matching all the resource quota.
		var count int32
		if rValue == 0 {
			count = int32(math.MaxInt32)
		} else {
			// Clamp to 0: when an extended-resource allocatable on a node
			// drops below current usage mid-workload (e.g. GPU lost to a
			// driver issue, SKU removed, or NFD label flap), the TAS
			// snapshot's per-domain cap (allocatable - inUse) can go
			// negative. Integer division would then yield a negative count
			// and propagate into TopologyDomain.Count, which the apiserver
			// rejects with "podCounts.individual[X] in body should be greater
			// than or equal to 1", permanently wedging the workload. A
			// negative "fits N times" is meaningless; treat it as 0 so the
			// scheduler skips the over-subscribed domain instead.
			count = max(int32(cap/rValue), 0)
		}
		// Tie-break between CPU and memory counts to ensure deterministic results.
		if result == nil || count < *result || (count == *result && rName < limitingResource) {
			result = new(count)
			limitingResource = rName
		}
	}
	return ptr.Deref(result, 0), limitingResource
}

// LazyRequests wraps a base Requests map and performs copy-on-write
// (lazy cloning) when mutations occur.
type LazyRequests struct {
	base   MapRequests
	cached MapRequests
}

func NewLazyRequests(base MapRequests) LazyRequests {
	return LazyRequests{base: base}
}

// IsValid returns true if either the base or cached map is initialized.
func (l *LazyRequests) IsValid() bool {
	return l.base != nil || l.cached != nil
}

// Get returns the underlying Requests (either the cached clone if mutated, or base).
func (l *LazyRequests) Get() MapRequests {
	if l.cached != nil {
		return l.cached
	}
	return l.base
}

// Sub subtracts subRequests from the underlying Requests map,
// cloning base on first write.
func (l *LazyRequests) Sub(subRequests MapRequests) {
	if len(subRequests) == 0 {
		return
	}
	if l.cached == nil {
		if l.base == nil {
			l.cached = MapRequests{}
		} else {
			l.cached = l.base.Clone()
		}
	}
	l.cached.Sub(subRequests)
}

// Add adds addRequests to the underlying Requests map,
// cloning base on first write.
func (l *LazyRequests) Add(addRequests MapRequests) {
	if len(addRequests) == 0 {
		return
	}
	if l.cached == nil {
		if l.base == nil {
			l.cached = MapRequests{}
		} else {
			l.cached = l.base.Clone()
		}
	}
	l.cached.Add(addRequests)
}
