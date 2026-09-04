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

package resource

import (
	"fmt"
	"strconv"
	"strings"

	"gopkg.in/inf.v0"
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
			ret[k] = f(va, vb).DeepCopy()
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

func QuantityToFloat(q *resource.Quantity) float64 {
	if q == nil || q.IsZero() {
		return 0
	}
	return q.AsApproximateFloat64()
}

// Decimal multiplication grows the scale on every call, and callers re-multiply
// the same value indefinitely, so results are rounded back to a fixed scale.
const mulByFloatScale = 9

// MulByFloat multiplies every element in q by f, which must be finite.
// Uses arbitrary-precision decimals so that results below one milli-unit are not
// truncated to zero and large quantities cannot overflow int64. Results are rounded
// down so that repeatedly scaling a value by a factor below 1 decays it to zero
// rather than settling on a non-zero fixed point.
func MulByFloat(q corev1.ResourceList, f float64) corev1.ResourceList {
	if q == nil {
		return nil
	}
	factor, ok := new(inf.Dec).SetString(strconv.FormatFloat(f, 'f', -1, 64))
	if !ok {
		panic(fmt.Sprintf("MulByFloat called with a non-finite factor: %v", f))
	}
	ret := make(corev1.ResourceList, len(q))
	for k, v := range q {
		scaled := new(inf.Dec).Mul(v.AsDec(), factor)
		scaled.Round(scaled, mulByFloatScale, inf.RoundDown)
		ret[k] = *resource.NewDecimalQuantity(*scaled, resource.DecimalSI)
	}
	return ret
}

func IsZero(rl corev1.ResourceList) bool {
	if len(rl) != 0 {
		return false
	}

	for _, qty := range rl {
		if !qty.IsZero() {
			return false
		}
	}

	return true
}

// IsExtendedResourceName returns true if the resource name is an extended resource.
// An extended resource is a fully-qualified resource name with a domain prefix
// that is not in the kubernetes.io namespace and is not a standard resource.
// This matches the upstream logic in k8s.io/kubernetes/pkg/apis/core/helper.
func IsExtendedResourceName(name corev1.ResourceName) bool {
	if isNativeResource(name) || isHugePageResourceName(name) {
		return false
	}
	return strings.Contains(string(name), "/")
}

func isNativeResource(name corev1.ResourceName) bool {
	return !strings.Contains(string(name), "/") ||
		strings.HasPrefix(string(name), corev1.ResourceDefaultNamespacePrefix)
}

func isHugePageResourceName(name corev1.ResourceName) bool {
	return strings.HasPrefix(string(name), corev1.ResourceHugePagesPrefix)
}

// MultiplyQuantity returns the product of two resource.Quantity values with arbitrary precision.
func MultiplyQuantity(value, mul resource.Quantity) resource.Quantity {
	value = value.DeepCopy()
	mul = mul.DeepCopy()
	product := inf.Dec{}
	product.Mul(value.AsDec(), mul.AsDec())
	return *resource.NewDecimalQuantity(product, value.Format)
}
