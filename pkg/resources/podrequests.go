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
)

// PodRequests totals a PodSpec the way a Pod's own requests are totalled, with
// every list read as chargeable first. Zeroing the sum instead would let a
// negative request in one container spend what another asked for under the same
// name, and the aggregation hides that the sum ever went there.
//
// Every reader of a PodSpec goes through here, so quota, Topology Aware
// Scheduling and the pod-level LimitRange see one total. The spec is borrowed,
// and nothing here writes to it.
func PodRequests(spec *corev1.PodSpec) corev1.ResourceList {
	if spec == nil {
		return nil
	}
	if !hasNegativeRequest(spec) {
		return resourcehelpers.PodRequests(&corev1.Pod{Spec: *detachPodLevelRequests(spec)}, resourcehelpers.PodResourcesOptions{})
	}
	requests := resourcehelpers.PodRequests(&corev1.Pod{Spec: *chargeableSpec(spec)}, resourcehelpers.PodResourcesOptions{})
	// A dropped override leaves the name unset where no container asked for it,
	// and a multiplyBy that reads it then scales by one. Only the names
	// component-helpers reads at the pod level: no other was in the untreated
	// total, and a name the ClusterQueue covers picks a flavor at any quantity.
	if spec.Resources != nil {
		for name, q := range spec.Resources.Requests {
			if q.Sign() >= 0 || !resourcehelpers.IsSupportedPodLevelResource(name) {
				continue
			}
			if _, found := requests[name]; !found {
				requests[name] = resource.Quantity{}
			}
		}
	}
	return requests
}

// detachPodLevelRequests gives the helper its own pod-level list, which it adds
// the overhead into in place. A decimal quantity is held behind a pointer, so
// the borrowed spec would otherwise grow by the overhead on every read.
func detachPodLevelRequests(spec *corev1.PodSpec) *corev1.PodSpec {
	if spec.Resources == nil {
		return spec
	}
	out := *spec
	out.Resources = spec.Resources.DeepCopy()
	return &out
}

// hasNegativeRequest reports whether any list the charge is built from holds one.
func hasNegativeRequest(spec *corev1.PodSpec) bool {
	negative := func(rl corev1.ResourceList) bool {
		for _, q := range rl {
			if q.Sign() < 0 {
				return true
			}
		}
		return false
	}
	for _, containers := range [][]corev1.Container{spec.InitContainers, spec.Containers} {
		for _, c := range containers {
			if negative(c.Resources.Requests) {
				return true
			}
		}
	}
	if spec.Resources != nil && negative(spec.Resources.Requests) {
		return true
	}
	return negative(spec.Overhead)
}

// chargeableSpec copies the spec rather than editing the one the caller is only
// borrowing.
func chargeableSpec(spec *corev1.PodSpec) *corev1.PodSpec {
	out := spec.DeepCopy()
	for i := range out.InitContainers {
		out.InitContainers[i].Resources.Requests = chargeableRequests(out.InitContainers[i].Resources.Requests)
	}
	for i := range out.Containers {
		out.Containers[i].Resources.Requests = chargeableRequests(out.Containers[i].Resources.Requests)
	}
	if out.Resources != nil {
		// A pod-level request replaces the container total instead of adding to it,
		// so a zero left here would spend what the containers asked for.
		out.Resources.Requests = dropNegativeRequests(out.Resources.Requests)
	}
	// PodRequests adds the overhead only where the field is set, so a nil one is
	// left alone rather than handed back as an empty map.
	if out.Overhead != nil {
		out.Overhead = chargeableRequests(out.Overhead)
	}
	return out
}

// chargeableRequests keeps a name it cannot charge, at zero.
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

// dropNegativeRequests removes the names it cannot charge, rather than keeping
// them at zero as chargeableRequests does.
func dropNegativeRequests(requests corev1.ResourceList) corev1.ResourceList {
	out := make(corev1.ResourceList, len(requests))
	for name, quantity := range requests {
		if quantity.Sign() >= 0 {
			out[name] = quantity
		}
	}
	return out
}
