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
	"testing"

	corev1 "k8s.io/api/core/v1"
)

func TestEqual(t *testing.T) {
	withEqualOperator := corev1.Toleration{
		Key:      "t0",
		Operator: corev1.TolerationOpEqual,
		Value:    "t0v",
		Effect:   corev1.TaintEffectNoSchedule,
	}
	withoutOperator := corev1.Toleration{
		Key:    "t0",
		Value:  "t0v",
		Effect: corev1.TaintEffectNoSchedule,
	}
	differentKey := corev1.Toleration{
		Key:      "t1",
		Operator: corev1.TolerationOpEqual,
		Value:    "t0v",
		Effect:   corev1.TaintEffectNoSchedule,
	}

	cases := map[string]struct {
		a, b   corev1.Toleration
		wantEq bool
	}{
		"empty vs Equal operator": {
			a:      withEqualOperator,
			b:      withoutOperator,
			wantEq: true,
		},
		"Equal vs empty operator": {
			a:      withoutOperator,
			b:      withEqualOperator,
			wantEq: true,
		},
		"same toleration (empty)": {
			a:      withoutOperator,
			b:      withoutOperator,
			wantEq: true,
		},
		"same toleration (not empty)": {
			a:      withEqualOperator,
			b:      withEqualOperator,
			wantEq: true,
		},
		"different tolerations": {
			a:      withEqualOperator,
			b:      differentKey,
			wantEq: false,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := Equal(tc.a, tc.b); got != tc.wantEq {
				t.Errorf("Equal() = %v, want %v", got, tc.wantEq)
			}
		})
	}
}
