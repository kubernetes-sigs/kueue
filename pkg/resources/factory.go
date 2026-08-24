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

	"sigs.k8s.io/kueue/pkg/features"
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
