/*
Copyright 2024 The Kubernetes Authors.

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

package util

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"
)

const (
	testContent = `
- match:
    priorityClassName: preemptible
    labels:
      resource_type: cpu-only
  toLocalQueue: preemptible-cpu
- match:
    labels:
      project_id: alpha
      resource_type: gpu
  toLocalQueue: alpha-gpu
- match:
    labels:
      project_id: alpha
      resource_type: cpu
  skip: true
`
)

var testMappingRules = MappingRules{
	{
		Match: MappingMatch{
			PriorityClassName: "preemptible",
			Labels: map[string]string{
				"resource_type": "cpu-only",
			},
		},
		ToLocalQueue: "preemptible-cpu",
	},
	{
		Match: MappingMatch{
			Labels: map[string]string{
				"project_id":    "alpha",
				"resource_type": "gpu",
			},
		},
		ToLocalQueue: "alpha-gpu",
	},
	{
		Match: MappingMatch{
			Labels: map[string]string{
				"project_id":    "alpha",
				"resource_type": "cpu",
			},
		},
		Skip: true,
	},
}

func TestMappingFromFile(t *testing.T) {
	tdir := t.TempDir()
	fPath := filepath.Join(tdir, "mapping.yaml")
	err := os.WriteFile(fPath, []byte(testContent), os.FileMode(0600))
	if err != nil {
		t.Fatalf("unable to create the test file: %s", err)
	}

	mapping, err := MappingRulesFromFile(fPath)
	if err != nil {
		t.Fatalf("unexpected load error: %s", err)
	}

	if diff := cmp.Diff(testMappingRules, mapping); diff != "" {
		t.Errorf("unexpected mapping(want-/ got+):\n%s", diff)
	}
}

func TestMappingMatch(t *testing.T) {
	cases := map[string]struct {
		className string
		labels    map[string]string
		rules     MappingRules

		wantMatch bool
		wantSkip  bool
		wantQueue string
	}{
		"missing one label": {
			labels: map[string]string{"project_id": "alpha"},
			rules:  testMappingRules,
		},
		"priority class not checked if not part of the rule": {
			className: "preemptible",
			labels:    map[string]string{"project_id": "alpha", "resource_type": "gpu"},
			rules:     testMappingRules,

			wantMatch: true,
			wantQueue: "alpha-gpu",
		},
		"skip": {
			className: "preemptible",
			labels:    map[string]string{"project_id": "alpha", "resource_type": "cpu"},
			rules:     testMappingRules,

			wantMatch: true,
			wantSkip:  true,
		},
		"priority class not matching": {
			className: "preemptible-1",
			labels:    map[string]string{"resource_type": "cpu-only"},
			rules:     testMappingRules,
		},
		"priority class matching": {
			className: "preemptible",
			labels:    map[string]string{"resource_type": "cpu-only"},
			rules:     testMappingRules,

			wantMatch: true,
			wantQueue: "preemptible-cpu",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			gotQueue, gotSkip, gotMatch := tc.rules.QueueFor(tc.className, tc.labels)

			if tc.wantMatch != gotMatch {
				t.Errorf("unexpected match %v", gotMatch)
			}

			if tc.wantSkip != gotSkip {
				t.Errorf("unexpected skip %v", gotSkip)
			}

			if tc.wantQueue != gotQueue {
				t.Errorf("unexpected queue want %q got %q", tc.wantQueue, gotQueue)
			}
		})
	}
}
