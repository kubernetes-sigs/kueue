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
)

// Requests represents an abstract collection of resource requests.
type Requests interface {
	CloneRequests() Requests
	CreateEmpty() Requests
	Add(other Requests)
	Sub(other Requests)
	CountIn(capacity Requests) int32
	CountInWithLimitingResource(capacity Requests) (int32, corev1.ResourceName)
	ToMapRequests() MapRequests
}

// NewRequests creates a Requests interface from a Kubernetes ResourceList.
func NewRequests(rl corev1.ResourceList) Requests {
	return NewMapRequests(rl)
}

// NewRequestsFromMap creates a Requests interface wrapping the provided MapRequests map.
func NewRequestsFromMap(m MapRequests) Requests {
	if m == nil {
		return nil
	}
	return m
}

// NewRequestsFromPodSpec creates a Requests interface from a PodSpec.
func NewRequestsFromPodSpec(podSpec *corev1.PodSpec) Requests {
	return NewMapRequestsFromPodSpec(podSpec)
}
