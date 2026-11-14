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

func TestNodeMatchesFlavor(t *testing.T) {
	cases := map[string]struct {
		nodeLabels     map[string]string
		requiredLabels map[string]string
		requiredLevels []string
		want           bool
	}{
		"no required labels and levels": {
			nodeLabels: map[string]string{"a": "1"},
			want:       true,
		},
		"no required labels and levels, node without labels": {
			want: true,
		},
		"label present and equal": {
			nodeLabels:     map[string]string{"a": "1"},
			requiredLabels: map[string]string{"a": "1"},
			want:           true,
		},
		"label present and different": {
			nodeLabels:     map[string]string{"a": "2"},
			requiredLabels: map[string]string{"a": "1"},
		},
		"label missing": {
			nodeLabels:     map[string]string{"b": "1"},
			requiredLabels: map[string]string{"a": "1"},
		},
		"empty required value, node label empty": {
			nodeLabels:     map[string]string{"a": ""},
			requiredLabels: map[string]string{"a": ""},
			want:           true,
		},
		"empty required value, node label missing": {
			nodeLabels:     map[string]string{"b": "1"},
			requiredLabels: map[string]string{"a": ""},
		},
		"empty required value, node without labels": {
			requiredLabels: map[string]string{"a": ""},
		},
		"empty required value, node label non-empty": {
			nodeLabels:     map[string]string{"a": "1"},
			requiredLabels: map[string]string{"a": ""},
		},
		"non-empty required value, node label empty": {
			nodeLabels:     map[string]string{"a": ""},
			requiredLabels: map[string]string{"a": "1"},
		},
		"all required labels match": {
			nodeLabels:     map[string]string{"a": "1", "b": "", "c": "3"},
			requiredLabels: map[string]string{"a": "1", "b": ""},
			want:           true,
		},
		"one of the required labels is missing": {
			nodeLabels:     map[string]string{"a": "1"},
			requiredLabels: map[string]string{"a": "1", "b": ""},
		},
		"required levels present": {
			nodeLabels:     map[string]string{"block": "b1", "rack": "r1"},
			requiredLevels: []string{"block", "rack"},
			want:           true,
		},
		"required level with empty value present": {
			nodeLabels:     map[string]string{"block": ""},
			requiredLevels: []string{"block"},
			want:           true,
		},
		"required level missing": {
			nodeLabels:     map[string]string{"block": "b1"},
			requiredLevels: []string{"block", "rack"},
		},
		"labels match and levels present": {
			nodeLabels:     map[string]string{"a": "1", "block": "b1"},
			requiredLabels: map[string]string{"a": "1"},
			requiredLevels: []string{"block"},
			want:           true,
		},
		"labels match and level missing": {
			nodeLabels:     map[string]string{"a": "1"},
			requiredLabels: map[string]string{"a": "1"},
			requiredLevels: []string{"block"},
		},
		"empty required label missing and level present": {
			nodeLabels:     map[string]string{"block": "b1"},
			requiredLabels: map[string]string{"a": ""},
			requiredLevels: []string{"block"},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := NodeMatchesFlavor(tc.nodeLabels, tc.requiredLabels, tc.requiredLevels); got != tc.want {
				t.Errorf("NodeMatchesFlavor() = %v, want %v", got, tc.want)
			}
		})
	}
}
