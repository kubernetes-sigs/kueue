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
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	"github.com/go-logr/logr"
	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/klog/v2"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/cache/scheduler/simulator"
	utiltas "sigs.k8s.io/kueue/pkg/util/tas"
	testingnode "sigs.k8s.io/kueue/pkg/util/testingjobs/node"
	testingpod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
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

	node1 := testingnode.MakeNode("node1").
		Label(corev1.LabelHostname, "node1").
		StatusAllocatable(corev1.ResourceList{
			corev1.ResourceCPU:  resource.MustParse("4"),
			corev1.ResourcePods: resource.MustParse("10"),
		}).
		Ready().
		Obj()
	node2 := testingnode.MakeNode("node2").
		Label(corev1.LabelHostname, "node2").
		StatusAllocatable(corev1.ResourceList{
			corev1.ResourceCPU:  resource.MustParse("4"),
			corev1.ResourcePods: resource.MustParse("10"),
		}).
		Ready().
		Obj()
	nodes := []*corev1.Node{node1, node2}

	existingPod := testingpod.MakePod("existing-pod", "default").
		UID("uid-1").
		Annotation(kueue.WorkloadAnnotation, "test-workload").
		NodeName("node1").
		StatusPhase(corev1.PodRunning).
		Port(8080, 8080, corev1.ProtocolTCP).
		Obj()

	tests := map[string]struct {
		addExistingPod bool
		candidateSpec  corev1.PodSpec
		wantFeasible   map[string]bool
	}{
		"hostPort conflict excludes node with occupied port": {
			addExistingPod: true,
			candidateSpec: corev1.PodSpec{
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

			wantFeasible: map[string]bool{"node2": true},
		},
		"different hostPort has no conflict": {
			addExistingPod: true,
			candidateSpec: corev1.PodSpec{
				Containers: []corev1.Container{
					{
						Name:  "c",
						Image: "busybox",
						Ports: []corev1.ContainerPort{{
							ContainerPort: 9090,
							HostPort:      9090,
							Protocol:      corev1.ProtocolTCP,
						}},
					},
				},
			},
			wantFeasible: map[string]bool{"node1": true, "node2": true},
		},
		"pod without hostPort passes through unaffected": {
			addExistingPod: true,
			candidateSpec: corev1.PodSpec{
				Containers: []corev1.Container{{
					Name:  "c",
					Image: "busybox",
					Resources: corev1.ResourceRequirements{
						Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("100m")},
					},
				}},
			},
			wantFeasible: map[string]bool{"node1": true, "node2": true},
		},
		"no existing pods means all nodes feasible": {
			candidateSpec: corev1.PodSpec{
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
			wantFeasible: map[string]bool{"node1": true, "node2": true},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			sim, err := NewWASSimulator(klog.NewContext(ctx, logr.Discard()), nil)
			if err != nil {
				t.Fatalf("NewWASSimulator failed: %v", err)
			}

			candidates := func(yield func(simulator.Candidate) bool) {
				for _, n := range nodes {
					if !yield(&testCandidate{node: n, id: utiltas.TopologyDomainID(n.Name)}) {
						return
					}
				}
			}

			if tc.addExistingPod {
				sim.TrackPod(ctx, existingPod)
			}
			snapshot, err := sim.Snapshot(ctx, nodes)
			if err != nil {
				t.Fatalf("CreateSnapshot failed: %v", err)
			}

			stats := &simulator.NodeExclusionStats{}
			results, err := snapshot.FindFeasibleNodes(ctx, candidates, &simulator.PodRequirements{
				PodTemplate: &corev1.PodTemplateSpec{Spec: tc.candidateSpec},
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

func TestNodeUnschedulableFeasibility(t *testing.T) {
	ctx := t.Context()

	node1 := testingnode.MakeNode("node1").
		Label(corev1.LabelHostname, "node1").
		Obj()
	node2 := testingnode.MakeNode("node2").
		Label(corev1.LabelHostname, "node2").
		Obj()
	unschedulable := testingnode.MakeNode("node-unschedulable").
		Label(corev1.LabelHostname, "node-unschedulable").
		Unschedulable().
		Obj()
	nodes := []*corev1.Node{node1, unschedulable, node2}

	t.Run("return all schedulable notes, skip unschedulable ones", func(t *testing.T) {
		sim, err := NewWASSimulator(klog.NewContext(ctx, logr.Discard()), nil)
		if err != nil {
			t.Fatalf("NewWASSimulator failed: %v", err)
		}

		snapshot, err := sim.Snapshot(ctx, nodes)
		if err != nil {
			t.Fatalf("Snapshot failed: %v", err)
		}

		candidates := func(yield func(simulator.Candidate) bool) {
			for _, n := range nodes {
				if !yield(&testCandidate{node: n, id: utiltas.TopologyDomainID(n.Name)}) {
					return
				}
			}
		}

		want := []simulator.MatchedCandidate{
			&testCandidate{node: node1, id: utiltas.TopologyDomainID("node1")},
			&testCandidate{node: node2, id: utiltas.TopologyDomainID("node2")},
		}

		got, err := snapshot.FindFeasibleNodes(
			ctx,
			candidates,
			&simulator.PodRequirements{
				PodTemplate: &corev1.PodTemplateSpec{},
			},
			&simulator.NodeExclusionStats{},
		)
		if err != nil {
			t.Fatalf("FindFeasibleNodes failed: %v", err)
		}

		slices.SortFunc(got, func(a, b simulator.MatchedCandidate) int {
			return strings.Compare(a.GetNode().Name, b.GetNode().Name)
		})
		if diff := cmp.Diff(want, got, cmp.AllowUnexported(testCandidate{})); diff != "" {
			t.Errorf("Unexpected feasible nodes (-want,+got):\n%s", diff)
		}
	})
}

func TestPreemptWorkload(t *testing.T) {
	ctx := t.Context()

	node1 := testingnode.MakeNode("node1").
		Label(corev1.LabelHostname, "node1").
		StatusAllocatable(corev1.ResourceList{
			corev1.ResourceCPU:  resource.MustParse("4"),
			corev1.ResourcePods: resource.MustParse("10"),
		}).
		Ready().
		Obj()
	nodes := []*corev1.Node{node1}

	existingPodWlKey := types.NamespacedName{Namespace: "default", Name: "wl1"}
	existingPod := testingpod.MakePod("existing-pod", existingPodWlKey.Namespace).
		UID("uid-1").
		Annotation(kueue.WorkloadAnnotation, existingPodWlKey.Name).
		NodeName("node1").
		StatusPhase(corev1.PodRunning).
		Port(8080, 8080, corev1.ProtocolTCP).
		Obj()

	candidatePod := corev1.PodTemplateSpec{
		Spec: *existingPod.Spec.DeepCopy(),
	}

	candidates := func(yield func(simulator.Candidate) bool) {
		for _, n := range nodes {
			if !yield(&testCandidate{node: n, id: utiltas.TopologyDomainID(n.Name)}) {
				return
			}
		}
	}

	checkFeasible := func(snapshot simulator.SimulatorSnapshot) bool {
		results, err := snapshot.FindFeasibleNodes(ctx, candidates, &simulator.PodRequirements{
			PodTemplate: &candidatePod,
		}, &simulator.NodeExclusionStats{})
		if err != nil {
			t.Fatalf("FindFeasibleNodes failed: %v", err)
		}
		return len(results) > 0
	}

	cases := map[string]struct {
		setup        func(context.Context, *wasSimulator)
		preemptKey   types.NamespacedName
		wantFeasible bool
	}{
		"preempt existing workload": {
			setup: func(ctx context.Context, sim *wasSimulator) {
				sim.TrackPod(ctx, existingPod)
			},
			preemptKey:   existingPodWlKey,
			wantFeasible: true,
		},
		"preempt non-existent workload": {
			setup: func(ctx context.Context, sim *wasSimulator) {
				sim.TrackPod(ctx, existingPod)
			},
			preemptKey:   types.NamespacedName{Namespace: "default", Name: "non-existent"},
			wantFeasible: false,
		},
		"preempt when unassigned pod exists": {
			setup: func(ctx context.Context, sim *wasSimulator) {
				sim.TrackPod(ctx, existingPod)
				unassignedPod := testingpod.MakePod("unassigned", "default").Annotation("", "").Obj()
				sim.TrackPod(ctx, unassignedPod)
			},
			preemptKey:   existingPodWlKey,
			wantFeasible: true,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			sim, err := NewWASSimulator(klog.NewContext(ctx, logr.Discard()), nil)
			if err != nil {
				t.Fatalf("NewWASSimulator failed: %v", err)
			}
			tc.setup(ctx, sim)

			snapshot, err := sim.Snapshot(ctx, nodes)
			if err != nil {
				t.Fatalf("Snapshot failed: %v", err)
			}

			if checkFeasible(snapshot) {
				t.Errorf("expected non-feasible before preemption")
			}

			revert, err := snapshot.PreemptWorkload(ctx, tc.preemptKey)
			if err != nil {
				t.Fatalf("PreemptWorkload failed: %v", err)
			}

			if got := checkFeasible(snapshot); got != tc.wantFeasible {
				t.Errorf("checkFeasible after preemption = %v, want %v", got, tc.wantFeasible)
			}

			if err := revert(); err != nil {
				t.Fatalf("revert failed: %v", err)
			}

			if checkFeasible(snapshot) {
				t.Errorf("expected non-feasible after preemption reverted")
			}
		})
	}
}

func TestSimulate(t *testing.T) {
	ctx := t.Context()

	node1 := testingnode.MakeNode("node1").
		Label(corev1.LabelHostname, "node1").
		StatusAllocatable(corev1.ResourceList{
			corev1.ResourceCPU:  resource.MustParse("4"),
			corev1.ResourcePods: resource.MustParse("10"),
		}).
		Ready().
		Obj()
	nodes := []*corev1.Node{node1}

	existingPod := testingpod.MakePod("existing-pod", "default").
		UID("uid-1").
		Annotation(kueue.WorkloadAnnotation, "wl1").
		NodeName("node1").
		StatusPhase(corev1.PodRunning).
		Port(8080, 8080, corev1.ProtocolTCP).
		Obj()

	candidatePod := corev1.PodTemplateSpec{
		Spec: *existingPod.Spec.DeepCopy(),
	}

	candidateIter := func(yield func(simulator.Candidate) bool) {
		yield(&testCandidate{node: node1, id: utiltas.TopologyDomainID(node1.Name)})
	}

	checkFeasible := func(snapshot simulator.SimulatorSnapshot) bool {
		results, err := snapshot.FindFeasibleNodes(ctx, candidateIter, &simulator.PodRequirements{
			PodTemplate: &candidatePod,
		}, &simulator.NodeExclusionStats{})
		if err != nil {
			t.Fatalf("FindFeasibleNodes failed: %v", err)
		}
		return len(results) > 0
	}

	sim, err := NewWASSimulator(klog.NewContext(ctx, logr.Discard()), nil)
	if err != nil {
		t.Fatalf("NewWASSimulator failed: %v", err)
	}
	sim.TrackPod(ctx, existingPod)

	snapshot, err := sim.Snapshot(ctx, nodes)
	if err != nil {
		t.Fatalf("Snapshot failed: %v", err)
	}

	if checkFeasible(snapshot) {
		t.Errorf("Expected node1 to be unfeasible before simulation")
	}

	if err = snapshot.Simulate(ctx, func() error {
		_, err := snapshot.PreemptWorkload(ctx, types.NamespacedName{Namespace: "default", Name: "wl1"})
		if err != nil {
			return err
		}
		if !checkFeasible(snapshot) {
			return errors.New("Expected node1 to be feasible inside simulation after preemption")
		}
		return nil
	}); err != nil {
		t.Fatalf("Simulation failed: %v", err)
	}

	if checkFeasible(snapshot) {
		t.Errorf("Expected node1 to be unfeasible after simulation completed (auto-reverted)")
	}
}
