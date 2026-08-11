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

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
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

func TestNodePortsFeasibility(t *testing.T) {
	ctx := t.Context()

	node1 := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "node1", Labels: map[string]string{corev1.LabelHostname: "node1"}},
		Status: corev1.NodeStatus{
			Allocatable: corev1.ResourceList{
				corev1.ResourceCPU:  resource.MustParse("4"),
				corev1.ResourcePods: resource.MustParse("10"),
			},
			Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}},
		},
	}
	node2 := &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{Name: "node2", Labels: map[string]string{corev1.LabelHostname: "node2"}},
		Status: corev1.NodeStatus{
			Allocatable: corev1.ResourceList{
				corev1.ResourceCPU:  resource.MustParse("4"),
				corev1.ResourcePods: resource.MustParse("10"),
			},
			Conditions: []corev1.NodeCondition{{Type: corev1.NodeReady, Status: corev1.ConditionTrue}},
		},
	}
	nodes := []*corev1.Node{node1, node2}

	existingPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{Name: "existing-pod", Namespace: "default", UID: "uid-1"},
		Spec: corev1.PodSpec{
			NodeName: "node1",
			Containers: []corev1.Container{{
				Name:  "c",
				Image: "busybox",
				Ports: []corev1.ContainerPort{{
					ContainerPort: 8080,
					HostPort:      8080,
					Protocol:      corev1.ProtocolTCP,
				}},
			}},
		},
		Status: corev1.PodStatus{Phase: corev1.PodRunning},
	}

	tests := map[string]struct {
		pods         []*corev1.Pod
		candidatePod corev1.PodTemplateSpec
		wantFeasible map[string]bool
	}{
		"hostPort conflict excludes node with occupied port": {
			pods: []*corev1.Pod{existingPod},
			candidatePod: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						Name:  "c",
						Image: "busybox",
						Ports: []corev1.ContainerPort{{
							ContainerPort: 8080,
							HostPort:      8080,
							Protocol:      corev1.ProtocolTCP,
						}},
					}},
				},
			},
			wantFeasible: map[string]bool{"node2": true},
		},
		"different hostPort has no conflict": {
			pods: []*corev1.Pod{existingPod},
			candidatePod: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						Name:  "c",
						Image: "busybox",
						Ports: []corev1.ContainerPort{{
							ContainerPort: 9090,
							HostPort:      9090,
							Protocol:      corev1.ProtocolTCP,
						}},
					}},
				},
			},
			wantFeasible: map[string]bool{"node1": true, "node2": true},
		},
		"pod without hostPort passes through unaffected": {
			pods: []*corev1.Pod{existingPod},
			candidatePod: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						Name:  "c",
						Image: "busybox",
						Resources: corev1.ResourceRequirements{
							Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("100m")},
						},
					}},
				},
			},
			wantFeasible: map[string]bool{"node1": true, "node2": true},
		},
		"no existing pods means all nodes feasible": {
			pods: nil,
			candidatePod: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						Name:  "c",
						Image: "busybox",
						Ports: []corev1.ContainerPort{{
							ContainerPort: 8080,
							HostPort:      8080,
							Protocol:      corev1.ProtocolTCP,
						}},
					}},
				},
			},
			wantFeasible: map[string]bool{"node1": true, "node2": true},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			sim, err := NewWASSimulatorForTest(ctx)
			if err != nil {
				t.Fatalf("NewWASSimulatorForTest failed: %v", err)
			}

			candidates := func(yield func(simulator.Candidate) bool) {
				for _, n := range nodes {
					if !yield(&testCandidate{node: n, id: utiltas.TopologyDomainID(n.Name)}) {
						return
					}
				}
			}

			for _, pod := range tc.pods {
				sim.TrackPod(pod)
			}
			checker, err := sim.NewFeasibilityChecker(ctx, nodes)
			if err != nil {
				t.Fatalf("NewFeasibilityCheckerWithPods failed: %v", err)
			}

			stats := &simulator.NodeExclusionStats{}
			results, err := checker.FindFeasibleNodes(ctx, candidates, &simulator.PodRequirements{
				PodTemplate: &tc.candidatePod,
			}, stats)
			if err != nil {
				t.Fatalf("FindFeasibleNodes failed: %v", err)
			}

			gotNames := make(map[string]bool)
			for _, r := range results {
				gotNames[r.GetNode().Name] = true
			}

			if len(gotNames) != len(tc.wantFeasible) {
				t.Errorf("got feasible nodes %v, want %v", gotNames, tc.wantFeasible)
				return
			}
			for n := range tc.wantFeasible {
				if !gotNames[n] {
					t.Errorf("expected node %s to be feasible, got %v", n, gotNames)
				}
			}
		})
	}
}
