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

package mapping

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
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
    resources:
    - example.com/gpu
    - example.com/tpu
  toLocalQueue: alpha-gpu
- match:
    labels:
      project_id: alpha
      resource_type: cpu
  skip: true
`
)

var testMappingRules = Rules{
	{
		Match: Match{
			PriorityClassName: "preemptible",
			Labels: map[string]string{
				"resource_type": "cpu-only",
			},
		},
		ToLocalQueue: "preemptible-cpu",
	},
	{
		Match: Match{
			Labels: map[string]string{
				"project_id":    "alpha",
				"resource_type": "gpu",
			},
			Resources: []corev1.ResourceName{"example.com/gpu", "example.com/tpu"},
		},
		ToLocalQueue: "alpha-gpu",
	},
	{
		Match: Match{
			Labels: map[string]string{
				"project_id":    "alpha",
				"resource_type": "cpu",
			},
		},
		Skip: true,
	},
}

func TestRulesFromFile(t *testing.T) {
	tdir := t.TempDir()
	fPath := filepath.Join(tdir, "mapping.yaml")
	err := os.WriteFile(fPath, []byte(testContent), os.FileMode(0600))
	if err != nil {
		t.Fatalf("unable to create the test file: %s", err)
	}

	rules, err := RulesFromFile(fPath)
	if err != nil {
		t.Fatalf("unexpected load error: %s", err)
	}

	if diff := cmp.Diff(testMappingRules, rules); diff != "" {
		t.Errorf("unexpected mapping(want-/ got+):\n%s", diff)
	}
}

func TestRulesQueueFor(t *testing.T) {
	cases := map[string]struct {
		className string
		labels    map[string]string
		requests  corev1.ResourceList
		rules     Rules

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
			requests: corev1.ResourceList{
				"example.com/gpu": resource.MustParse("1"),
				"example.com/tpu": resource.MustParse("4"),
			},
			rules: testMappingRules,

			wantMatch: true,
			wantQueue: "alpha-gpu",
		},
		"all the listed resources need to be requested": {
			labels:   map[string]string{"project_id": "alpha", "resource_type": "gpu"},
			requests: corev1.ResourceList{"example.com/tpu": resource.MustParse("4")},
			rules:    testMappingRules,
		},
		"labels match but none of the resources is requested": {
			labels:   map[string]string{"project_id": "alpha", "resource_type": "gpu"},
			requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1")},
			rules:    testMappingRules,
		},
		"a zero request does not match": {
			labels: map[string]string{"project_id": "alpha", "resource_type": "gpu"},
			requests: corev1.ResourceList{
				"example.com/gpu": resource.MustParse("0"),
				"example.com/tpu": resource.MustParse("4"),
			},
			rules: testMappingRules,
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
			pod := &corev1.Pod{
				ObjectMeta: metav1.ObjectMeta{Labels: tc.labels},
				Spec: corev1.PodSpec{
					PriorityClassName: tc.className,
					Containers: []corev1.Container{
						{Resources: corev1.ResourceRequirements{Requests: tc.requests}},
					},
				},
			}
			gotQueue, gotSkip, gotMatch := tc.rules.QueueFor(pod)

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
