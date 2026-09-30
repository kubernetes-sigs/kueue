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
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	resourcehelpers "k8s.io/component-helpers/resource"

	"sigs.k8s.io/kueue/pkg/features"
	utilresource "sigs.k8s.io/kueue/pkg/util/resource"
)

// Equal reports whether two Requests objects are Equal.
func Equal(a, b Requests) bool {
	if a == nil && b == nil {
		return true
	}
	if a == nil || b == nil || a.Len() != b.Len() {
		return false
	}
	equal := true
	a.ForEach(func(name corev1.ResourceName, val int64) {
		if equal && b.ResourceValue(name) != val {
			equal = false
		}
	})
	return equal
}

// NewRequests creates an empty Requests instance based on feature gates.
func NewRequests() Requests {
	if features.Enabled(features.VectorizedResourceRequests) {
		return &SliceRequests{}
	}
	return MapRequests{}
}

// NewRequestsFromMap creates a Requests instance from a map based on feature gates.
func NewRequestsFromMap(m map[corev1.ResourceName]int64) Requests {
	if len(m) == 0 {
		return NewRequests()
	}
	if features.Enabled(features.VectorizedResourceRequests) {
		return new(toSliceRequests(MapRequests(m)))
	}
	return MapRequests(m)
}

// NewRequestsFromResourceList creates a Requests instance from a corev1.ResourceList based on feature gates.
func NewRequestsFromResourceList(rl corev1.ResourceList) Requests {
	if features.Enabled(features.VectorizedResourceRequests) {
		return new(ResourceListToSliceRequests(rl))
	}
	return NewMapRequests(rl)
}

// NewRequestsFromPodSpec creates a Requests instance from a PodSpec based on feature gates.
func NewRequestsFromPodSpec(podSpec *corev1.PodSpec) Requests {
	if podSpec == nil {
		return NewRequests()
	}
	return NewRequestsFromResourceList(PodRequests(podSpec))
}

// PodRequests returns the effective requests for a PodSpec, overhead included.
func PodRequests(podSpec *corev1.PodSpec) corev1.ResourceList {
	return podRequests(podSpec, true)
}

// ContainerRequests is PodRequests without the overhead.
func ContainerRequests(podSpec *corev1.PodSpec) corev1.ResourceList {
	return podRequests(podSpec, false)
}

// podRequests keeps the aggregate container request when a smaller pod-level
// request would otherwise replace it, and reads a negative request as zero so
// that one cannot spend what another container, the overhead or a
// transformation charges under the same name. Valid PodSpecs are unchanged:
// Kubernetes requires pod-level requests to cover the aggregate container
// requests and refuses negative quantities, and the Workload webhook refuses
// them too while WorkloadValidateResourcesAreNonNegative is enabled.
func podRequests(podSpec *corev1.PodSpec, withOverhead bool) corev1.ResourceList {
	if podSpec == nil {
		return nil
	}
	spec := podSpec
	if hasNegativeRequest(spec) {
		spec = chargeableSpec(spec)
	}
	pod := &corev1.Pod{Spec: *spec}
	requests := resourcehelpers.PodRequests(pod, resourcehelpers.PodResourcesOptions{ExcludeOverhead: true})
	containerRequests := resourcehelpers.AggregateContainerRequests(pod, resourcehelpers.PodResourcesOptions{})
	requests = utilresource.MergeResourceListKeepMax(requests, containerRequests)
	if withOverhead {
		requests = utilresource.MergeResourceListKeepSum(requests, spec.Overhead)
	}
	return requests
}

func hasNegativeRequest(spec *corev1.PodSpec) bool {
	for _, containers := range [][]corev1.Container{spec.InitContainers, spec.Containers} {
		for _, c := range containers {
			if hasNegativeQuantity(c.Resources.Requests) {
				return true
			}
		}
	}
	if spec.Resources != nil && hasNegativeQuantity(spec.Resources.Requests) {
		return true
	}
	return hasNegativeQuantity(spec.Overhead)
}

func hasNegativeQuantity(rl corev1.ResourceList) bool {
	for _, q := range rl {
		if q.Sign() < 0 {
			return true
		}
	}
	return false
}

// chargeableSpec returns a copy of spec with every negative request read as
// zero. A zero pod-level entry is raised back to the container aggregate by
// the merge in podRequests, and a name nothing else asks for stays at zero, so
// a multiplyBy reading it scales by zero rather than by one.
func chargeableSpec(spec *corev1.PodSpec) *corev1.PodSpec {
	out := spec.DeepCopy()
	for i := range out.InitContainers {
		out.InitContainers[i].Resources.Requests = chargeableRequests(out.InitContainers[i].Resources.Requests)
	}
	for i := range out.Containers {
		out.Containers[i].Resources.Requests = chargeableRequests(out.Containers[i].Resources.Requests)
	}
	if out.Resources != nil {
		out.Resources.Requests = chargeableRequests(out.Resources.Requests)
	}
	if out.Overhead != nil {
		out.Overhead = chargeableRequests(out.Overhead)
	}
	return out
}

// chargeableRequests copies input, reading a negative quantity as zero.
func chargeableRequests(input corev1.ResourceList) corev1.ResourceList {
	res := make(corev1.ResourceList, len(input))
	for name, quantity := range input {
		if quantity.Sign() < 0 {
			quantity = resource.Quantity{}
		}
		res[name] = quantity
	}
	return res
}

// ToMap converts any Requests instance into a MapRequests map.
func ToMap(r Requests) map[corev1.ResourceName]int64 {
	if isEmpty(r) {
		return nil
	}
	res := make(MapRequests, r.Len())
	r.ForEach(func(name corev1.ResourceName, val int64) {
		res[name] = val
	})
	return res
}
