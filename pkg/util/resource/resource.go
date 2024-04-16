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
	"k8s.io/apimachinery/pkg/api/resource"
)

type resolveConflict func(a, b resource.Quantity) resource.Quantity

func mergeResourceList(a, b corev1.ResourceList, f resolveConflict) corev1.ResourceList {
	if a == nil {
		return b.DeepCopy()
	}
	ret := a.DeepCopy()

	for k, vb := range b {
		if va, exists := ret[k]; !exists {
			ret[k] = vb.DeepCopy()
		} else if f != nil {
			ret[k] = f(va, vb)
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
	return mergeResourceList(a, b, func(a, b resource.Quantity) resource.Quantity {
		if a.Cmp(b) < 0 {
			return b
		}
		return a
	})
}

// MergeResourceListKeepMin creates a new ResourceList holding all the values from a and b
// and resolve potential conflicts by keeping the lowest value.
func MergeResourceListKeepMin(a, b corev1.ResourceList) corev1.ResourceList {
	return mergeResourceList(a, b, func(a, b resource.Quantity) resource.Quantity {
		if a.Cmp(b) > 0 {
			return b
		}
		return a
	})
}

// MergeResourceListKeepSum creates a new ResourceList holding all the values from a and b
// and resolve potential conflicts by adding up the two values.
func MergeResourceListKeepSum(a, b corev1.ResourceList) corev1.ResourceList {
	return mergeResourceList(a, b, func(a, b resource.Quantity) resource.Quantity {
		a.Add(b)
		return a
	})
}

// GetGreaterKeys returns the list of ResourceNames for which the value in a are greater than the value in b.
func GetGreaterKeys(a, b corev1.ResourceList) []string {
	if len(a) == 0 || len(b) == 0 {
		return nil
	}

	ret := make([]string, 0, len(a))
	for k, va := range a {
		if vb, found := b[k]; found && va.Cmp(vb) > 0 {
			ret = append(ret, k.String())
		}
	}
	if len(ret) == 0 {
		return nil
	}
	return ret
}

func QuantityToFloat(q *resource.Quantity) float64 {
	if q == nil || q.IsZero() {
		return 0
	}
	if i64, isInt := q.AsInt64(); isInt {
		return float64(i64)
	}
	return float64(q.MilliValue()) / 1000
}
