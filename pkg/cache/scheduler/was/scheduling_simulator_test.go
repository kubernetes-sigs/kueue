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
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/cache/scheduler/simulator"
	controllerconstants "sigs.k8s.io/kueue/pkg/controller/constants"
	podconstants "sigs.k8s.io/kueue/pkg/controller/jobs/pod/constants"
	utiltas "sigs.k8s.io/kueue/pkg/util/tas"
	testingnode "sigs.k8s.io/kueue/pkg/util/testingjobs/node"
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

	existingPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "existing-pod",
			Namespace: "default",
			UID:       "uid-1",
			Annotations: map[string]string{
				kueue.WorkloadAnnotation: "test-workload",
			},
		},
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
			snapshot, err := sim.Snapshot(ctx, nodes)
			if err != nil {
				t.Fatalf("CreateSnapshot failed: %v", err)
			}

			stats := &simulator.NodeExclusionStats{}
			results, err := snapshot.FindFeasibleNodes(ctx, candidates, &simulator.PodRequirements{
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

func TestNodeUnschedulableFeasibility(t *testing.T) {
	ctx := t.Context()

	node1 := testingnode.MakeNode("node1").
		Label(corev1.LabelHostname, "node1").
		Unschedulable().
		Obj()
	node2 := testingnode.MakeNode("node2").
		Label(corev1.LabelHostname, "node2").
		Obj()

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

			snapshot, err := sim.Snapshot(ctx, tc.nodes)
			if err != nil {
				t.Fatalf("Snapshot failed: %v", err)
			}

			results, err := snapshot.FindFeasibleNodes(ctx, candidates, &simulator.PodRequirements{
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

func makeSimplePod(ns, name, annotation, wl string) *corev1.Pod {
	return &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      name,
			Namespace: ns,
			Annotations: map[string]string{
				annotation: wl,
			},
		},
	}
}

func key(ns, name string) types.NamespacedName {
	return types.NamespacedName{Namespace: ns, Name: name}
}

func TestWorkloadMapping(t *testing.T) {
	ctx := t.Context()

	basicPod := makeSimplePod("ns", "pod", kueue.WorkloadAnnotation, "wl")
	slicePod := makeSimplePod("ns", "pod", kueue.WorkloadSliceNameAnnotation, "slice-wl")
	slicePodWithBasicAnnotation := func() *corev1.Pod {
		pod := slicePod.DeepCopy()
		pod.Annotations[kueue.WorkloadAnnotation] = "stale-wl"
		return pod
	}()
	prebuiltWlPod := makeSimplePod(
		"ns",
		"pod",
		controllerconstants.PrebuiltWorkloadAnnotation,
		"prebuilt-wl",
	)
	groupPod := makeSimplePod("ns", "pod", podconstants.GroupNameAnnotation, "group-wl")

	testCases := map[string]struct {
		operation func(*wasSimulator)
		want      podsByWorkload
	}{
		"add pod with workload annotation": {
			operation: func(sim *wasSimulator) {
				sim.TrackPod(basicPod)
			},
			want: podsByWorkload{
				key("ns", "wl"): podsByKey{
					key("ns", "pod"): basicPod,
				},
			},
		},
		"add pod with slice name annotation": {
			operation: func(sim *wasSimulator) {
				sim.TrackPod(slicePod)
			},
			want: podsByWorkload{
				key("ns", "slice-wl"): podsByKey{
					key("ns", "pod"): slicePod,
				},
			},
		},
		"add pod with prebuilt workload annotation": {
			operation: func(sim *wasSimulator) {
				sim.TrackPod(prebuiltWlPod)
			},
			want: podsByWorkload{
				key("ns", "prebuilt-wl"): podsByKey{
					key("ns", "pod"): prebuiltWlPod,
				},
			},
		},
		"add pod with group name annotation": {
			operation: func(sim *wasSimulator) {
				sim.TrackPod(groupPod)
			},
			want: podsByWorkload{
				key("ns", "group-wl"): podsByKey{
					key("ns", "pod"): groupPod,
				},
			},
		},
		"remove pod": {
			operation: func(sim *wasSimulator) {
				sim.TrackPod(makeSimplePod("ns", "pod1", kueue.WorkloadAnnotation, "wl1"))
				sim.TrackPod(makeSimplePod("ns", "pod2", kueue.WorkloadAnnotation, "wl1"))
				sim.TrackPod(makeSimplePod("ns", "pod3", kueue.WorkloadAnnotation, "wl2"))
				sim.TrackPod(makeSimplePod("ns", "pod4", kueue.WorkloadAnnotation, "wl2"))
				sim.UntrackPod(key("ns", "pod1"))
			},
			want: podsByWorkload{
				key("ns", "wl1"): podsByKey{
					key("ns", "pod2"): makeSimplePod("ns", "pod2", kueue.WorkloadAnnotation, "wl1"),
				},
				key("ns", "wl2"): podsByKey{
					key("ns", "pod3"): makeSimplePod("ns", "pod3", kueue.WorkloadAnnotation, "wl2"),
					key("ns", "pod4"): makeSimplePod("ns", "pod4", kueue.WorkloadAnnotation, "wl2"),
				},
			},
		},
		"remove all pods": {
			operation: func(sim *wasSimulator) {
				sim.TrackPod(makeSimplePod("ns", "pod1", kueue.WorkloadAnnotation, "wl1"))
				sim.TrackPod(makeSimplePod("ns", "pod2", kueue.WorkloadAnnotation, "wl1"))
				sim.TrackPod(makeSimplePod("ns", "pod3", kueue.WorkloadAnnotation, "wl2"))
				sim.UntrackPod(key("ns", "pod1"))
				sim.UntrackPod(key("ns", "pod2"))
				sim.UntrackPod(key("ns", "pod3"))
			},
			want: podsByWorkload{},
		},
		"update pod workload annotation": {
			operation: func(sim *wasSimulator) {
				sim.TrackPod(makeSimplePod("ns", "pod1", kueue.WorkloadAnnotation, "wl1"))
				sim.TrackPod(makeSimplePod("ns", "pod1", kueue.WorkloadAnnotation, "wl2"))
			},
			want: podsByWorkload{
				key("ns", "wl2"): podsByKey{
					key("ns", "pod1"): makeSimplePod("ns", "pod1", kueue.WorkloadAnnotation, "wl2"),
				},
			},
		},
		"update unassigned pod to have workload annotation": {
			operation: func(sim *wasSimulator) {
				sim.TrackPod(makeSimplePod("ns", "pod1", "", ""))
				sim.TrackPod(makeSimplePod("ns", "pod1", kueue.WorkloadAnnotation, "wl1"))
			},
			want: podsByWorkload{
				key("ns", "wl1"): podsByKey{
					key("ns", "pod1"): makeSimplePod("ns", "pod1", kueue.WorkloadAnnotation, "wl1"),
				},
			},
		},
		"update_pod_from_workload_annotation_to_unassigned": {
			operation: func(sim *wasSimulator) {
				sim.TrackPod(makeSimplePod("ns", "pod1", kueue.WorkloadAnnotation, "wl1"))
				sim.TrackPod(makeSimplePod("ns", "pod1", "", ""))
			},
			want: podsByWorkload{},
		},
		"workload slice name annotation preferred over workload annotation": {
			operation: func(sim *wasSimulator) {
				sim.TrackPod(slicePodWithBasicAnnotation)
			},
			want: podsByWorkload{
				key("ns", "slice-wl"): podsByKey{
					key("ns", "pod"): slicePodWithBasicAnnotation,
				},
			},
		},
		"empty workload annotation with higher priority overrides non-empty workload annotation": {
			operation: func(sim *wasSimulator) {
				pod := makeSimplePod("ns", "pod1", podWorkloadAnnotations[0], "")
				pod.Annotations[podWorkloadAnnotations[1]] = "non-empty-wl-name"
				sim.TrackPod(pod)
			},
			want: podsByWorkload{},
		},
		"add pod with empty workload annotation": {
			operation: func(sim *wasSimulator) {
				sim.TrackPod(makeSimplePod("ns", "pod1", kueue.WorkloadAnnotation, ""))
			},
			want: podsByWorkload{},
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			sim, err := NewWASSimulatorForTest(ctx)
			if err != nil {
				t.Fatalf("NewWASSimulatorForTest failed: %v", err)
			}

			tc.operation(sim)

			snapshotRaw, err := sim.Snapshot(ctx, []*corev1.Node{})
			if err != nil {
				t.Fatalf("Snapshot failed: %v", err)
			}
			snapshot, ok := snapshotRaw.(*wasSimulatorSnapshot)
			if !ok {
				t.Fatalf("Snapshot is not a wasSimulatorSnapshot: %T", snapshotRaw)
			}

			if diff := cmp.Diff(tc.want, snapshot.podsByWorkload); diff != "" {
				t.Errorf("Unexpected pod assignments (-want,+got):\n%s", diff)
			}
		})
	}
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

	existingPodWlKey := key("default", "wl1")
	existingPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "existing-pod",
			Namespace: existingPodWlKey.Namespace,
			UID:       "uid-1",
			Annotations: map[string]string{
				kueue.WorkloadAnnotation: existingPodWlKey.Name,
			},
		},
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

	candidatePod := corev1.PodTemplateSpec{
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
		setup        func(sim *wasSimulator)
		preemptKey   types.NamespacedName
		wantFeasible bool
	}{
		"preempt existing workload": {
			setup: func(sim *wasSimulator) {
				sim.TrackPod(existingPod)
			},
			preemptKey:   existingPodWlKey,
			wantFeasible: true,
		},
		"preempt non-existent workload": {
			setup: func(sim *wasSimulator) {
				sim.TrackPod(existingPod)
			},
			preemptKey:   key("default", "non-existent"),
			wantFeasible: false,
		},
		"preempt when unassigned pod exists": {
			setup: func(sim *wasSimulator) {
				sim.TrackPod(existingPod)
				unassignedPod := makeSimplePod("default", "unassigned", "", "")
				sim.TrackPod(unassignedPod)
			},
			preemptKey:   existingPodWlKey,
			wantFeasible: true,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			sim, err := NewWASSimulatorForTest(ctx)
			if err != nil {
				t.Fatalf("NewWASSimulatorForTest failed: %v", err)
			}
			tc.setup(sim)

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

	existingPod := &corev1.Pod{
		ObjectMeta: metav1.ObjectMeta{
			Name:      "existing-pod",
			Namespace: "default",
			UID:       "uid-1",
			Annotations: map[string]string{
				kueue.WorkloadAnnotation: "wl1",
			},
		},
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

	candidatePod := corev1.PodTemplateSpec{
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

	sim, err := NewWASSimulatorForTest(ctx)
	if err != nil {
		t.Fatalf("NewWASSimulatorForTest failed: %v", err)
	}
	sim.TrackPod(existingPod)

	snapshot, err := sim.Snapshot(ctx, nodes)
	if err != nil {
		t.Fatalf("Snapshot failed: %v", err)
	}

	if checkFeasible(snapshot) {
		t.Errorf("Expected node1 to be unfeasible before simulation")
	}

	simErr := snapshot.Simulate(ctx, func() {
		_, err := snapshot.PreemptWorkload(ctx, key("default", "wl1"))
		if err != nil {
			t.Fatalf("PreemptWorkload inside Simulate failed: %v", err)
		}

		if !checkFeasible(snapshot) {
			t.Errorf("Expected node1 to be feasible inside simulation after preemption")
		}
	})
	if simErr != nil {
		t.Fatalf("Simulation failed: %v", simErr)
	}

	if checkFeasible(snapshot) {
		t.Errorf("Expected node1 to be unfeasible after simulation completed (auto-reverted)")
	}
}

func TestTrackPodDeepCopy(t *testing.T) {
	ctx := t.Context()
	sim, err := NewWASSimulatorForTest(ctx)
	if err != nil {
		t.Fatalf("NewWASSimulatorForTest failed: %v", err)
	}

	pod := makeSimplePod("ns", "pod1", kueue.WorkloadAnnotation, "wl1")
	sim.TrackPod(pod)

	// Mutate the pod object that was passed into TrackPod
	pod.Annotations[kueue.WorkloadAnnotation] = "mutated-wl"

	snapshotRaw, err := sim.Snapshot(ctx, nil)
	if err != nil {
		t.Fatalf("Snapshot failed: %v", err)
	}
	snapshot := snapshotRaw.(*wasSimulatorSnapshot)

	want := podsByWorkload{
		key("ns", "wl1"): podsByKey{
			key("ns", "pod1"): makeSimplePod("ns", "pod1", kueue.WorkloadAnnotation, "wl1"),
		},
	}

	if diff := cmp.Diff(want, snapshot.podsByWorkload); diff != "" {
		t.Errorf("TrackPod did not deep copy pod (-want,+got):\n%s", diff)
	}
}
