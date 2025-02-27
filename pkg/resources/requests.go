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
	"strings"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/utils/ptr"
)

// The following resources calculations are inspired on
// https://github.com/kubernetes/kubernetes/blob/master/pkg/scheduler/framework/types.go

// PodGroupRequests maps ResourceName to flavor to value; for CPU it is tracked in MilliCPU.
// This is assumed to have multiple Pods (>= 1) resource requests.
// Note that PodGroup is a soft group of Pods and does not require a set of Pods (=~ PodSet) belonging to
// a single higher object like Job.
type PodGroupRequests map[corev1.ResourceName]int64

func NewPodGroupRequests(rl corev1.ResourceList) PodGroupRequests {
	r := PodGroupRequests{}
	for name, quant := range rl {
		r[name] = ResourceValue(name, quant)
	}
	return r
}

func (r PodGroupRequests) Clone() PodGroupRequests {
	return maps.Clone(r)
}

func (r PodGroupRequests) ScaledUp(f int64) PodGroupRequests {
	ret := r.Clone()
	ret.Mul(f)
	return ret
}

func (r PodGroupRequests) ScaledDown(f int64) PodGroupRequests {
	ret := r.Clone()
	ret.Divide(f)
	return ret
}

func (r PodGroupRequests) Divide(f int64) {
	for k := range r {
		r[k] /= f
	}
}

func (r PodGroupRequests) Mul(f int64) {
	for k := range r {
		r[k] *= f
	}
}

func (r PodGroupRequests) Add(addRequests PodGroupRequests) {
	for k, v := range addRequests {
		r[k] += v
	}
}

func (r PodGroupRequests) Sub(subRequests PodGroupRequests) {
	for k, v := range subRequests {
		r[k] -= v
	}
}

func (r PodGroupRequests) ToResourceList() corev1.ResourceList {
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

func (r PodGroupRequests) CountIn(capacity PodGroupRequests) int32 {
	var result *int32
	for rName, rValue := range r {
		capacity, found := capacity[rName]
		if !found {
			return 0
		}
		// find the minimum count matching all the resource quota.
		count := int32(capacity / rValue)
		if result == nil || count < *result {
			result = ptr.To(count)
		}
	}
	return ptr.Deref(result, 0)
}
