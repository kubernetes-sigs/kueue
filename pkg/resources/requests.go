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
	"strings"
	"sync"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	resourcehelpers "k8s.io/component-helpers/resource"
	"k8s.io/utils/ptr"

	utilmath "sigs.k8s.io/kueue/pkg/util/math"
)

var binaryFormattedResources sync.Map

// RegisterBinaryFormattedResource marks a resource name as byte-valued for display.
// Counter-based DRA logical resources (for example gpu.memory) should be registered
// at startup so quantities serialize with BinarySI units.
func RegisterBinaryFormattedResource(name corev1.ResourceName) {
	binaryFormattedResources.Store(name, struct{}{})
}

func usesBinaryFormat(name corev1.ResourceName) bool {
	_, ok := binaryFormattedResources.Load(name)
	return ok
}

// The following resources calculations are inspired on
// https://github.com/kubernetes/kubernetes/blob/master/pkg/scheduler/framework/types.go

// MapRequests maps ResourceName to flavor to value; for CPU it is tracked in MilliCPU.
type MapRequests map[corev1.ResourceName]int64

var OnePodRequest = MapRequests{corev1.ResourcePods: 1}

func (r MapRequests) ForEach(fn func(name corev1.ResourceName, val int64)) {
	for k, v := range r {
		fn(k, v)
	}
}

func NewMapRequests(rl corev1.ResourceList) MapRequests {
	r := MapRequests{}
	for name, quant := range rl {
		r[name] = ResourceValue(name, quant)
	}
	return r
}

func NewMapRequestsFromPodSpec(podSpec *corev1.PodSpec) MapRequests {
	return NewMapRequests(resourcehelpers.PodRequests(&corev1.Pod{Spec: *podSpec}, resourcehelpers.PodResourcesOptions{}))
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

func (r MapRequests) ResourceValue(name corev1.ResourceName) int64 {
	return r[name]
}

func (r MapRequests) Set(name corev1.ResourceName, val int64) {
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
		r[k] = max(v, 0)
	}
}

func (r MapRequests) Add(other Requests) {
	other.ForEach(func(k corev1.ResourceName, v int64) {
		r[k] = utilmath.SaturatingAdd(r[k], v)
	})
}

func (r MapRequests) Sub(other Requests) {
	other.ForEach(func(k corev1.ResourceName, v int64) {
		r[k] = utilmath.SaturatingSub(r[k], v)
	})
}

func (r MapRequests) ToResourceList() corev1.ResourceList {
	ret := make(corev1.ResourceList, len(r))
	for k, v := range r {
		ret[k] = ResourceQuantity(k, v)
	}
	return ret
}

// ResourceValue returns the integer value for the resource name.
// It's milli-units for CPU and absolute units for everything else.
// Both clamp: Quantity.Value and Quantity.MilliValue read a big.Int that need
// not fit in an int64.
func ResourceValue(name corev1.ResourceName, q resource.Quantity) int64 {
	if name == corev1.ResourceCPU {
		return utilmath.SafeMilliValue(q)
	}
	return utilmath.SafeValue(q)
}

func ResourceQuantity(name corev1.ResourceName, v int64) resource.Quantity {
	switch name {
	case corev1.ResourceCPU:
		return *resource.NewMilliQuantity(v, resource.DecimalSI)
	case corev1.ResourceMemory, corev1.ResourceEphemeralStorage:
		return newCanonicalQuantity(v, resource.BinarySI)
	default:
		if strings.HasPrefix(string(name), corev1.ResourceHugePagesPrefix) || usesBinaryFormat(name) {
			return newCanonicalQuantity(v, resource.BinarySI)
		}
		return *resource.NewQuantity(v, resource.DecimalSI)
	}
}

// newCanonicalQuantity returns a Quantity that will successfully round-trip.
//
// This means the returned quantity can be serialized then deserialized back to
// an identical quantity.
//
// If the value can round-trip using the preferred format, that one will be used.
// Otherwise, the format will be automatically determined.
//
// For example, if preferred format is BinarySI, 128000 will use BinarySI format
// (because it can be represented as 125Ki), but 100000 will use DecimalSI format.
func newCanonicalQuantity(v int64, preferredFormat resource.Format) resource.Quantity {
	preferred := *resource.NewQuantity(v, preferredFormat)
	final, err := resource.ParseQuantity(preferred.String())
	if err != nil {
		// Should never happen
		return preferred
	}
	return final
}

func ResourceQuantityString(name corev1.ResourceName, v int64) string {
	rq := ResourceQuantity(name, v)
	return rq.String()
}

// AmountQuantity returns a in the format the API reports name in. A Quantity
// carries at most MaxInt64 in the unit it reports, cores for CPU, so the scale
// is applied before that bound; a magnitude past it is capped with its sign.
func AmountQuantity(name corev1.ResourceName, a Amount) resource.Quantity {
	if name == corev1.ResourceCPU {
		// Everything held in an int64 of milli keeps the path it is on today.
		if v, ok := a.asInt64(); ok {
			return ResourceQuantity(name, v)
		}
		if dec, ok := a.milliDec(); ok {
			return *resource.NewDecimalQuantity(*dec, resource.DecimalSI)
		}
		// ResourceQuantity reads its argument as milli, so the cap is built
		// here in the cores a CPU Quantity reports.
		return *resource.NewQuantity(quantityCap(a.Sign()), resource.DecimalSI)
	}
	// Reported in the whole units it is held in, so the two limits coincide.
	// MinInt64 fits an int64 and is one past the magnitude a Quantity carries.
	if v, ok := a.asInt64(); ok && v != math.MinInt64 {
		return ResourceQuantity(name, v)
	}
	return ResourceQuantity(name, quantityCap(a.Sign()))
}

// quantityCap returns the largest magnitude a Quantity carries, with sign, in
// the unit the Quantity reports.
func quantityCap(sign int) int64 {
	if sign < 0 {
		return -math.MaxInt64
	}
	return math.MaxInt64
}

// AmountQuantityString renders a as the API would report it, capped past what
// a Quantity carries; Amount.String is the exact form.
func AmountQuantityString(name corev1.ResourceName, a Amount) string {
	q := AmountQuantity(name, a)
	return q.String()
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
		if otherValue, found := otherMap[name]; found && value > otherValue {
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
	requests.ForEach(func(rName corev1.ResourceName, rValue int64) {
		cap := capacity.ResourceValue(rName)
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
			// Clamp the upper bound before converting to int32 to avoid
			// overflowing large capacity-to-request ratios.
			count = int32(max(0, min(cap/rValue, math.MaxInt32)))
		}
		// Tie-break between CPU and memory counts to ensure deterministic results.
		if result == nil || count < *result || (count == *result && rName < limitingResource) {
			result = new(count)
			limitingResource = rName
		}
	})
	return ptr.Deref(result, 0), limitingResource
}

func (r MapRequests) Iter() iter.Seq2[corev1.ResourceName, int64] {
	return func(yield func(corev1.ResourceName, int64) bool) {
		for k, v := range r {
			if !yield(k, v) {
				return
			}
		}
	}
}
