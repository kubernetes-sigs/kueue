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

package taints

import (
	"testing"

	corev1 "k8s.io/api/core/v1"
)

func TestTaintKeyExists(t *testing.T) {
	testingTaints := []corev1.Taint{
		{
			Key:    "foo_1",
			Value:  "bar_1",
			Effect: corev1.TaintEffectNoExecute,
		},
		{
			Key:    "foo_2",
			Value:  "bar_2",
			Effect: corev1.TaintEffectNoSchedule,
		},
	}

	cases := []struct {
		name            string
		taintKeyToMatch string
		expectedResult  bool
	}{
		{
			name:            "taint key exists",
			taintKeyToMatch: "foo_1",
			expectedResult:  true,
		},
		{
			name:            "taint key does not exist",
			taintKeyToMatch: "foo_3",
			expectedResult:  false,
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			result := TaintKeyExists(testingTaints, c.taintKeyToMatch)

			if result != c.expectedResult {
				t.Errorf("[%s] unexpected results: %v", c.name, result)
			}
		})
	}
}
