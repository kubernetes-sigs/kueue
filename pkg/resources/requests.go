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
	"maps"
	"math"
	"slices"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/utils/ptr"
)

// The following resources calculations are inspired on
// https://github.com/kubernetes/kubernetes/blob/master/pkg/scheduler/framework/types.go

// MapRequests maps ResourceName to flavor to value; for CPU it is tracked in MilliCPU.
type MapRequests map[corev1.ResourceName]Amount

var OnePodRequest = MapRequests{corev1.ResourcePods: NewAmount(1)}

func (r MapRequests) ForEach(fn func(name corev1.ResourceName, val Amount)) {
	for k, v := range r {
		fn(k, v)
	}
}

func NewMapRequests(rl corev1.ResourceList) MapRequests {
	r := MapRequests{}
	for name, quant := range rl {
		r[name] = AmountFromQuantity(name, quant)
	}
	return r
}

func NewMapRequestsFromPodSpec(podSpec *corev1.PodSpec) MapRequests {
	return NewMapRequests(PodRequests(podSpec))
}

func (r MapRequests) Clone() Requests {
	return maps.Clone(r)
}

func (r MapRequests) ScaledUp(f int64) Requests {
	ret := maps.Clone(r)
	ret.Mul(f)
	return ret
}

func (r MapRequests) ScaledDown(f int64) Requests {
	ret := maps.Clone(r)
	ret.Divide(f)
	return ret
}

func (r MapRequests) Divide(f int64) {
	for k, v := range r {
		if v.Sign() == 0 && f == 0 {
			// Skip dividing by 0 when resources are 0.
			// This may happen when the function is used to scale down the
			// resources computed initially for all (0) Pods, and thus r[k] = 0.
			continue
		}
		r[k] = v.QuoInt64(f)
	}
}

func (r MapRequests) Mul(f int64) {
	for k, v := range r {
		r[k] = v.MulInt64(f)
	}
}

func (r MapRequests) ResourceValue(name corev1.ResourceName) Amount {
	return r[name]
}

func (r MapRequests) Set(name corev1.ResourceName, val Amount) {
	r[name] = val
}

func (r MapRequests) Len() int {
	return len(r)
}

func (r MapRequests) IsEmpty() bool {
	return len(r) == 0
}

// FloorToZero replaces negative resource values with zero.
// Defense-in-depth for pre-existing negative Workloads while
// WorkloadValidateResourcesAreNonNegative rolls out.
// TODO: remove ~2 releases after WorkloadValidateResourcesAreNonNegative locks to GA.
func (r MapRequests) FloorToZero() {
	for k, v := range r {
		if v.Sign() < 0 {
			r[k] = Amount{}
		}
	}
}

func (r MapRequests) Add(other Requests) {
	other.ForEach(func(k corev1.ResourceName, v Amount) {
		r[k] = r[k].Add(v)
	})
}

func (r MapRequests) Sub(other Requests) {
	other.ForEach(func(k corev1.ResourceName, v Amount) {
		r[k] = r[k].Sub(v)
	})
}

func (r MapRequests) ToResourceList(formatter *ResourceFormatter) corev1.ResourceList {
	if len(r) == 0 {
		return nil
	}
	ret := make(corev1.ResourceList, len(r))
	for k, v := range r {
		ret[k] = formatter.AmountQuantity(k, v)
	}
	return ret
}

// GreaterKeys returns keys where the receiver is greater than other,
// sorted alphabetically for deterministic output.
func (r MapRequests) GreaterKeys(other Requests) []corev1.ResourceName {
	if len(r) == 0 || isEmpty(other) {
		return nil
	}
	otherMap := ToMap(other)
	var result []corev1.ResourceName
	for name, value := range r {
		if otherValue, found := otherMap[name]; found && value.Cmp(otherValue) > 0 {
			result = append(result, name)
		}
	}
	if len(result) == 0 {
		return nil
	}
	slices.Sort(result)
	return result
}

// GreaterKeysRL compares against a ResourceList and returns larger keys.
func (r MapRequests) GreaterKeysRL(rl corev1.ResourceList) []corev1.ResourceName {
	return r.GreaterKeys(NewRequestsFromResourceList(rl))
}

func (r MapRequests) CountIn(capacity Requests) int32 {
	count, _ := r.CountInWithLimitingResource(capacity)
	return count
}

// CountInWithLimitingResource returns how many times the request fits into capacity
// and the resource that is most constraining (i.e., gave the minimum count).
// When multiple resources have the same count, ties are broken alphabetically
// by resource name for determinism.
func (r MapRequests) CountInWithLimitingResource(capacity Requests) (int32, corev1.ResourceName) {
	return CountInWithLimitingResource(r, capacity)
}

// CountInWithLimitingResource returns how many times requests fit into capacity
// and the resource that is most constraining (i.e., gave the minimum count).
// When multiple resources have the same count, ties are broken alphabetically
// by resource name for determinism.
func CountInWithLimitingResource(requests Requests, capacity Requests) (int32, corev1.ResourceName) {
	var (
		result           *int32
		limitingResource corev1.ResourceName
	)
	requests.ForEach(func(rName corev1.ResourceName, rValue Amount) {
		cap := capacity.ResourceValue(rName)
		// find the minimum count matching all the resource quota.
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
		// Clamp the upper bound before converting to int32 to avoid
		// overflowing large capacity-to-request ratios.
		count := fitsCount(cap, rValue)
		// Tie-break between CPU and memory counts to ensure deterministic results.
		if result == nil || count < *result || (count == *result && rName < limitingResource) {
			result = new(count)
			limitingResource = rName
		}
	})
	return ptr.Deref(result, 0), limitingResource
}

// fitsCount is how many times req fits into cap, clamped to [0, MaxInt32].
// A zero request is treated as unbounded.
func fitsCount(capVal, req Amount) int32 {
	if req.Sign() == 0 {
		return math.MaxInt32
	}
	q := capVal.Quo(req)
	if q.Sign() <= 0 {
		return 0
	}
	if q.CmpInt64(math.MaxInt32) >= 0 {
		return math.MaxInt32
	}
	n, _ := q.asInt64()
	return int32(n)
}

func (r MapRequests) Iter() iter.Seq2[corev1.ResourceName, Amount] {
	return func(yield func(corev1.ResourceName, Amount) bool) {
		for k, v := range r {
			if !yield(k, v) {
				return
			}
		}
	}
}
