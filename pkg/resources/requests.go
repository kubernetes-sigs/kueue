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
		return *resource.NewQuantity(v, resource.BinarySI)
	default:
		if strings.HasPrefix(string(name), corev1.ResourceHugePagesPrefix) {
			return *resource.NewQuantity(v, resource.BinarySI)
		}
		return *resource.NewQuantity(v, resource.DecimalSI)
	}
}

func ResourceQuantityString(name corev1.ResourceName, v int64) string {
	rq := ResourceQuantity(name, v)
	return rq.String()
}

func (r Requests) CountIn(capacity Requests) int32 {
	var result *int32
	for rName, rValue := range r {
		capacity, found := capacity[rName]
		if !found && rValue != 0 {
			return 0
		}
		// find the minimum count matching all the resource quota.
		var count int32
		if rValue == 0 {
			count = int32(math.MaxInt32)
		} else {
			count = int32(capacity / rValue)
		}
		if result == nil || count < *result {
			result = ptr.To(count)
		}
	}
	return ptr.Deref(result, 0)
}
