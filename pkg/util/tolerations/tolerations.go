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

package tolerations

import (
	"slices"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
)

// Equal reports whether two tolerations have the same Kubernetes identity.
// Identity consists of key, operator, value, and effect; TolerationSeconds is
// intentionally ignored because it controls NoExecute duration, not matching.
// The Kubernetes API defaults an empty Operator to "Equal", so Operator: ""
// and Operator: "Equal" describe the same toleration and compare as equal.
func Equal(a, b corev1.Toleration) bool {
	if a.Operator == "" {
		a.Operator = corev1.TolerationOpEqual
	}
	if b.Operator == "" {
		b.Operator = corev1.TolerationOpEqual
	}
	return a.MatchToleration(&b)
}

// Merge returns a copy of base followed by tolerations from extra that are not
// already present. The first matching toleration wins, so base values and
// ordering are preserved, including when TolerationSeconds differs. Neither
// input slice is modified.
func Merge(base, extras []corev1.Toleration) []corev1.Toleration {
	merged := slices.Clone(base)
	for _, t := range extras {
		if !slices.ContainsFunc(merged, func(existing corev1.Toleration) bool {
			return Equal(existing, t)
		}) {
			merged = append(merged, t)
		}
	}
	return merged
}

// ContainsAll reports whether tolerations contains every toleration in subset,
// in any order. Unlike Equal, it compares all fields, including
// TolerationSeconds.
func ContainsAll(tolerations, subset []corev1.Toleration) bool {
	for _, s := range subset {
		if !slices.ContainsFunc(tolerations, func(t corev1.Toleration) bool {
			return equality.Semantic.DeepEqual(t, s)
		}) {
			return false
		}
	}
	return true
}

// CopyAddedByName copies the tolerations of each template in src to the
// template with the same name in dst when they only add tolerations, and
// reports whether any template in dst changed.
func CopyAddedByName[K comparable](dst, src map[K]*corev1.PodTemplateSpec) bool {
	var updated bool
	for name, dstTemplate := range dst {
		if srcTemplate, found := src[name]; found && copyIfOnlyAdded(&dstTemplate.Spec.Tolerations, srcTemplate.Spec.Tolerations) {
			updated = true
		}
	}
	return updated
}

// copyIfOnlyAdded sets dst to a copy of src when src contains every toleration
// in dst and differs from it, and reports whether dst changed.
func copyIfOnlyAdded(dst *[]corev1.Toleration, src []corev1.Toleration) bool {
	if equality.Semantic.DeepEqual(*dst, src) || !ContainsAll(src, *dst) {
		return false
	}
	*dst = slices.Clone(src)
	return true
}
