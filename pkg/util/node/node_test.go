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

package node

import (
	"testing"

	corev1 "k8s.io/api/core/v1"

	testingnode "sigs.k8s.io/kueue/pkg/util/testingjobs/node"
)

func TestHasTaint(t *testing.T) {
	baseNode := testingnode.MakeNode("")

	testCases := map[string]struct {
		taintName string
		node      *corev1.Node
		want      bool
	}{
		"taint present": {
			taintName: "example.com/taint",
			node: baseNode.Clone().
				Taints(corev1.Taint{Key: "example.com/taint"}).
				Obj(),
			want: true,
		},
		"another taint present": {
			taintName: "example.com/taint",
			node: baseNode.Clone().
				Taints(corev1.Taint{Key: "example.com/taint2"}).
				Obj(),
			want: false,
		},
		"no taints": {
			taintName: "example.com/taint",
			node:      baseNode.Clone().Obj(),
			want:      false,
		},
	}

	for desc, tc := range testCases {
		t.Run(desc, func(t *testing.T) {
			got := HasTaint(tc.node, tc.taintName)
			if got != tc.want {
				t.Errorf("Unexpected result: want=%v, got=%v", tc.want, got)
			}
		})
	}
}
