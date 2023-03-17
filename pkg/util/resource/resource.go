/*
Copyright 2023 The Kubernetes Authors.

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

package resource

import (
	corev1 "k8s.io/api/core/v1"
	apiresource "k8s.io/apimachinery/pkg/api/resource"
)

type resolveConflict func(a, b apiresource.Quantity) apiresource.Quantity

func mergeResourceList(a, b corev1.ResourceList, f resolveConflict) corev1.ResourceList {
	if a == nil {
		return b.DeepCopy()
	}
	ret := a.DeepCopy()

	for k, vb := range b {
		if va, exists := ret[k]; !exists {
			ret[k] = vb.DeepCopy()
		} else {
			if f != nil {
				ret[k] = f(va, vb)
			}
		}
	}
	return ret
}

// MergeResourceListKeepFirst creates a new ResourceList holding all resource values from dst
// and any new values from src
func MergeResourceListKeepFirst(dst, src corev1.ResourceList) corev1.ResourceList {
	return mergeResourceList(dst, src, nil)
}

// MergeResourceListKeepMax creates a new ResourceList holding all the values from a and b
// and resolve potential conflicts by keeping the highest value.
func MergeResourceListKeepMax(a, b corev1.ResourceList) corev1.ResourceList {
	return mergeResourceList(a, b, func(a, b apiresource.Quantity) apiresource.Quantity {
		if a.Cmp(b) < 0 {
			return b
		}
		return a
	})
}

// MergeResourceListKeepMin creates a new ResourceList holding all the values from a and b
// and resolve potential conflicts by keeping the lowest value.
func MergeResourceListKeepMin(a, b corev1.ResourceList) corev1.ResourceList {
	return mergeResourceList(a, b, func(a, b apiresource.Quantity) apiresource.Quantity {
		if a.Cmp(b) > 0 {
			return b
		}
		return a
	})
}

// MergeResourceListKeepSum creates a new ResourceList holding all the values from a and b
// and resolve potential conflicts by adding up the tow values.
func MergeResourceListKeepSum(a, b corev1.ResourceList) corev1.ResourceList {
	return mergeResourceList(a, b, func(a, b apiresource.Quantity) apiresource.Quantity {
		a.Add(b)
		return a
	})
}
