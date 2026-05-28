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

import corev1 "k8s.io/api/core/v1"

// Equal reports whether two tolerations describe the same toleration.
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
