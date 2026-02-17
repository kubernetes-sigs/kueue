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
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	resourcehelpers "k8s.io/component-helpers/resource"
	"k8s.io/utils/ptr"
)

// The following resources calculations are inspired on
// https://github.com/kubernetes/kubernetes/blob/master/pkg/scheduler/framework/types.go

// Requests maps ResourceName to flavor to value; for CPU it is tracked in MilliCPU.
type Requests map[corev1.ResourceName]int64

func NewRequests(rl corev1.ResourceList) Requests {
	r := Requests{}
	for name, quant := range rl {
		r[name] = ResourceValue(name, quant)
	}
	return r
}

func NewRequestsFromPodSpec(podSpec *corev1.PodSpec) Requests {
	return NewRequests(resourcehelpers.PodRequests(&corev1.Pod{Spec: *podSpec}, resourcehelpers.PodResourcesOptions{}))
}

func (r Requests) Clone() Requests {
	return maps.Clone(r)
}

func (r Requests) ScaledUp(f int64) Requests {
	ret := r.Clone()
	ret.Mul(f)
	return ret
}

func (r Requests) ScaledDown(f int64) Requests {
	ret := r.Clone()
	ret.Divide(f)
	return ret
}

func (r Requests) Divide(f int64) {
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

func (r Requests) Mul(f int64) {
	for k := range r {
		r[k] *= f
	}
}

func (r Requests) Add(addRequests Requests) {
	for k, v := range addRequests {
		r[k] += v
	}
}

func (r Requests) Sub(subRequests Requests) {
	for k, v := range subRequests {
		r[k] -= v
	}
}

func (r Requests) ToResourceList() corev1.ResourceList {
	ret := make(corev1.ResourceList, len(r))
	for k, v := range r {
		ret[k] = ResourceQuantity(k, v)
	}
	return ret
}

// ResourceValue returns the integer value for the resource name.
// It's milli-units for CPU and absolute units for everything else.
func ResourceValue(name corev1.ResourceName, q resource.Quantity) int64 {
	if name == corev1.ResourceCPU {
		return q.MilliValue()
	}
	return q.Value()
}

func ResourceQuantity(name corev1.ResourceName, v int64) resource.Quantity {
	switch name {
	case corev1.ResourceCPU:
		return *resource.NewMilliQuantity(v, resource.DecimalSI)
	case corev1.ResourceMemory, corev1.ResourceEphemeralStorage:
		return newCanonicalQuantity(v, resource.BinarySI)
	default:
		if strings.HasPrefix(string(name), corev1.ResourceHugePagesPrefix) {
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

// GreaterKeys returns keys where the receiver is greater than other.
func (r Requests) GreaterKeys(other Requests) []corev1.ResourceName {
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
func (r Requests) GreaterKeysRL(rl corev1.ResourceList) []corev1.ResourceName {
	return r.GreaterKeys(NewRequests(rl))
}

func (r Requests) CountIn(capacity Requests) int32 {
	count, _ := r.CountInWithLimitingResource(capacity)
	return count
}

// CountInWithLimitingResource returns how many times the request fits into capacity
// and the resource that is most constraining (i.e., gave the minimum count).
// When multiple resources have the same count, ties are broken alphabetically
// by resource name for determinism.
func (r Requests) CountInWithLimitingResource(capacity Requests) (int32, corev1.ResourceName) {
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
			count = int32(cap / rValue)
		}
		// Tie-break between CPU and memory counts to ensure deterministic results.
		if result == nil || count < *result || (count == *result && rName < limitingResource) {
			result = ptr.To(count)
			limitingResource = rName
		}
	}
	return ptr.Deref(result, 0), limitingResource
}
