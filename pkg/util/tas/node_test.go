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

package tas

import (
	"testing"

	corev1 "k8s.io/api/core/v1"

	testingnode "sigs.k8s.io/kueue/pkg/util/testingjobs/node"
)

func TestNodeHostname(t *testing.T) {
	cases := map[string]struct {
		node *corev1.Node
		want string
	}{
		"hostname label differs from the Node name": {
			node: testingnode.MakeNode("node-x1").Label(corev1.LabelHostname, "x1").Obj(),
			want: "x1",
		},
		"hostname label equals the Node name": {
			node: testingnode.MakeNode("x1").Label(corev1.LabelHostname, "x1").Obj(),
			want: "x1",
		},
		"hostname label missing falls back to the Node name": {
			node: testingnode.MakeNode("x1").Obj(),
			want: "x1",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := NodeHostname(tc.node); got != tc.want {
				t.Errorf("NodeHostname() = %q, want %q", got, tc.want)
			}
		})
	}
}
