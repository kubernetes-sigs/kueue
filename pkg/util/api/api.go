/*
Copyright 2022 The Kubernetes Authors.

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

package api

import (
	v1 "k8s.io/api/core/v1"
)

const (
	maxEventMsgSize     = 1024
	maxConditionMsgSize = 32 * 1024
)

// TruncateEventMessage truncates a message if it hits the maxEventMessage.
func TruncateEventMessage(message string) string {
	return truncateMessage(message, maxEventMsgSize)
}

func TruncateConditionMessage(message string) string {
	return truncateMessage(message, maxConditionMsgSize)
}

// truncateMessage truncates a message if it hits the NoteLengthLimit.
func truncateMessage(message string, limit int) string {
	if len(message) <= limit {
		return message
	}
	suffix := " ..."
	return message[:limit-len(suffix)] + suffix
}

// SetContainersDefaults will fill up the containers with default values.
// Note that this is aimed to bridge the gap between job and workload,
// E.g. job may have resource limits configured but no requests,
// however Kueue depends on the necessary workload requests for computing,
// so we need a function like this to make up for this.
//
// This is inspired by Kubernetes pod defaulting:
// https://github.com/kubernetes/kubernetes/blob/ab002db78835a94bd19ce4aaa46fb39b4d9c276f/pkg/apis/core/v1/defaults.go#L144
func SetContainersDefaults(containers []v1.Container) []v1.Container {
	// This only happens in tests for containers should always exist.
	// We return containers itself here rather than nil or empty slice directly for
	// not failing the unit tests or integration tests.
	if len(containers) == 0 {
		return containers
	}
	res := make([]v1.Container, len(containers))
	for i, c := range containers {
		if c.Resources.Limits != nil {
			if c.Resources.Requests == nil {
				c.Resources.Requests = make(v1.ResourceList)
			}
			for k, v := range c.Resources.Limits {
				if _, exists := c.Resources.Requests[k]; !exists {
					c.Resources.Requests[k] = v.DeepCopy()
				}
			}
		}
		res[i] = c
	}
	return res
}
