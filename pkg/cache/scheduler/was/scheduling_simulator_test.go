//go:build !exclude_scheduler_library

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

package was

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"sigs.k8s.io/kueue/pkg/cache/scheduler/simulator"
	utiltas "sigs.k8s.io/kueue/pkg/util/tas"
)

type testCandidate struct {
	node          *corev1.Node
	id            utiltas.TopologyDomainID
	affinityScore int64
}

func (c *testCandidate) GetNode() *corev1.Node           { return c.node }
func (c *testCandidate) GetID() utiltas.TopologyDomainID { return c.id }
func (c *testCandidate) GetAffinityScore() int64         { return c.affinityScore }
func (c *testCandidate) SetAffinityScore(score int64)    { c.affinityScore = score }


func TestNodeUnschedulableFeasibility(t *testing.T) {
	ctx := t.Context()

	node1 := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "node1", Labels: map[string]string{corev1.LabelHostname: "node1"}},
		Spec:       corev1.NodeSpec{Unschedulable: true},
	}
	node2 := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "node2", Labels: map[string]string{corev1.LabelHostname: "node2"}},
		Spec:       corev1.NodeSpec{Unschedulable: false},
	}

	tests := map[string]struct {
		nodes        []*corev1.Node
		candidatePod corev1.PodTemplateSpec
		wantFeasible map[string]bool
	}{
		"unschedulable node is excluded": {
			nodes:        []*corev1.Node{node1, node2},
			candidatePod: corev1.PodTemplateSpec{},
			wantFeasible: map[string]bool{"node2": true},
		},
		"all schedulable nodes are feasible": {
			nodes:        []*corev1.Node{node2},
			candidatePod: corev1.PodTemplateSpec{},
			wantFeasible: map[string]bool{"node2": true},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			sim, err := NewWASSimulatorForTest(ctx)
			if err != nil {
				t.Fatalf("NewWASSimulatorForTest failed: %v", err)
			}

			candidates := func(yield func(simulator.Candidate) bool) {
				for _, n := range tc.nodes {
					if !yield(&testCandidate{node: n, id: utiltas.TopologyDomainID(n.Name)}) {
						return
					}
				}
			}

			checker, err := sim.NewFeasibilityChecker(ctx, tc.nodes)
			if err != nil {
				t.Fatalf("NewFeasibilityChecker failed: %v", err)
			}

			results, err := checker.FindFeasibleNodes(ctx, candidates, &simulator.PodRequirements{
				PodTemplate: &tc.candidatePod,
			}, &simulator.NodeExclusionStats{})
			if err != nil {
				t.Fatalf("FindFeasibleNodes failed: %v", err)
			}

			gotNames := make(map[string]bool)
			for _, r := range results {
				gotNames[r.GetNode().Name] = true
			}

			if diff := cmp.Diff(tc.wantFeasible, gotNames); diff != "" {
				t.Errorf("Unexpected feasible nodes (-want,+got):\n%s", diff)
			}
		})
	}
}
