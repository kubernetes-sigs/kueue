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

func makeSimplePod(name, ns, annotation, wl string) *corev1.Pod {
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

	testCases := map[string]struct {
		operation func(*wasSimulator)
		want      *podsByWorkload
	}{
		"add pod with workload annotation": {
			operation: func(sim *wasSimulator) {
				sim.TrackPod(makeSimplePod("pod", "ns", kueue.WorkloadAnnotation, "wl"))
			},
			want: &podsByWorkload{
				key("ns", "wl"): podsByKey{
					key("ns", "pod"): makeSimplePod("pod", "ns", kueue.WorkloadAnnotation, "wl"),
				},
			},
		},
		"add pod with slice name annotation": {
			operation: func(sim *wasSimulator) {
				sim.TrackPod(makeSimplePod("pod", "ns", kueue.WorkloadSliceNameAnnotation, "wl"))
			},
			want: &podsByWorkload{
				key("ns", "wl"): podsByKey{
					key("ns", "pod"): makeSimplePod("pod", "ns", kueue.WorkloadSliceNameAnnotation, "wl"),
				},
			},
		},
		"add pod with prebuilt workload annotation": {
			operation: func(sim *wasSimulator) {
				sim.TrackPod(makeSimplePod(
					"pod",
					"ns",
					controllerconstants.PrebuiltWorkloadAnnotation,
					"prebuild-wl",
				))
			},
			want: &podsByWorkload{
				key("ns", "prebuild-wl"): podsByKey{
					key("ns", "pod"): makeSimplePod(
						"pod",
						"ns",
						controllerconstants.PrebuiltWorkloadAnnotation,
						"prebuild-wl",
					),
				},
			},
		},
		"add pod with group name annotation": {
			operation: func(sim *wasSimulator) {
				sim.TrackPod(makeSimplePod("pod", "ns", podconstants.GroupNameAnnotation, "group-wl"))
			},
			want: &podsByWorkload{
				key("ns", "group-wl"): podsByKey{
					key("ns", "pod"): makeSimplePod("pod", "ns", podconstants.GroupNameAnnotation, "group-wl"),
				},
			},
		},
		"remove pod": {
			operation: func(sim *wasSimulator) {
				sim.TrackPod(makeSimplePod("pod1", "ns", kueue.WorkloadAnnotation, "wl1"))
				sim.TrackPod(makeSimplePod("pod2", "ns", kueue.WorkloadAnnotation, "wl1"))
				sim.TrackPod(makeSimplePod("pod3", "ns", kueue.WorkloadAnnotation, "wl2"))
				sim.TrackPod(makeSimplePod("pod4", "ns", kueue.WorkloadAnnotation, "wl2"))
				sim.UntrackPod(key("ns", "pod1"))
			},
			want: &podsByWorkload{
				key("ns", "wl1"): podsByKey{
					key("ns", "pod2"): makeSimplePod("pod2", "ns", kueue.WorkloadAnnotation, "wl1"),
				},
				key("ns", "wl2"): podsByKey{
					key("ns", "pod3"): makeSimplePod("pod3", "ns", kueue.WorkloadAnnotation, "wl2"),
					key("ns", "pod4"): makeSimplePod("pod4", "ns", kueue.WorkloadAnnotation, "wl2"),
				},
			},
		},
		"remove all pods": {
			operation: func(sim *wasSimulator) {
				sim.TrackPod(makeSimplePod("pod1", "ns", kueue.WorkloadAnnotation, "wl1"))
				sim.TrackPod(makeSimplePod("pod2", "ns", kueue.WorkloadAnnotation, "wl1"))
				sim.TrackPod(makeSimplePod("pod3", "ns", kueue.WorkloadAnnotation, "wl2"))
				sim.UntrackPod(key("ns", "pod1"))
				sim.UntrackPod(key("ns", "pod2"))
				sim.UntrackPod(key("ns", "pod3"))
			},
			want: &podsByWorkload{},
		},
		"update pod workload annotation": {
			operation: func(sim *wasSimulator) {
				sim.TrackPod(makeSimplePod("pod1", "ns", kueue.WorkloadAnnotation, "wl1"))
				sim.TrackPod(makeSimplePod("pod1", "ns", kueue.WorkloadAnnotation, "wl2"))
			},
			want: &podsByWorkload{
				key("ns", "wl2"): podsByKey{
					key("ns", "pod1"): makeSimplePod("pod1", "ns", kueue.WorkloadAnnotation, "wl2"),
				},
			},
		},
		"update unassigned pod to have workload annotation": {
			operation: func(sim *wasSimulator) {
				sim.TrackPod(makeSimplePod("pod1", "ns", "", ""))
				sim.TrackPod(makeSimplePod("pod1", "ns", kueue.WorkloadAnnotation, "wl1"))
			},
			want: &podsByWorkload{
				key("ns", "wl1"): podsByKey{
					key("ns", "pod1"): makeSimplePod("pod1", "ns", kueue.WorkloadAnnotation, "wl1"),
				},
			},
		},
		"update_pod_from_workload_annotation_to_unassigned": {
			operation: func(sim *wasSimulator) {
				sim.TrackPod(makeSimplePod("pod1", "ns", kueue.WorkloadAnnotation, "wl1"))
				sim.TrackPod(makeSimplePod("pod1", "ns", "", ""))
			},
			want: nil,
		},
		"workload slice name annotation preferred over workload annotation": {
			operation: func(sim *wasSimulator) {
				pod := makeSimplePod("pod1", "ns", kueue.WorkloadSliceNameAnnotation, "slice-wl")
				pod.Annotations[kueue.WorkloadAnnotation] = "regular-wl"
				sim.TrackPod(pod)
			},
			want: &podsByWorkload{
				key("ns", "slice-wl"): podsByKey{
					key("ns", "pod1"): func() *corev1.Pod {
						pod := makeSimplePod("pod1", "ns", kueue.WorkloadSliceNameAnnotation, "slice-wl")
						pod.Annotations[kueue.WorkloadAnnotation] = "regular-wl"
						return pod
					}(),
				},
			},
		},
		"empty workload annotation with higher priority overrides non-empty workload annotation": {
			operation: func(sim *wasSimulator) {
				pod := makeSimplePod("pod1", "ns", podWorkloadAnnotations[0], "")
				pod.Annotations[podWorkloadAnnotations[1]] = "regular-wl"
				sim.TrackPod(pod)
			},
			want: nil,
		},
		"add pod with empty workload annotation": {
			operation: func(sim *wasSimulator) {
				sim.TrackPod(makeSimplePod("pod1", "ns", kueue.WorkloadAnnotation, ""))
			},
			want: nil,
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			simRaw, err := NewWASSimulatorForTest(ctx)
			if err != nil {
				t.Fatalf("NewWASSimulatorForTest failed: %v", err)
			}
			sim := simRaw.(*wasSimulator)

			tc.operation(sim)

			snapshotRaw, err := sim.Snapshot(ctx, nodes)
			if err != nil {
				t.Fatalf("Snapshot failed: %v", err)
			}
			snapshot := snapshotRaw.(*wasSimulatorSnapshot)

			if diff := cmp.Diff(tc.want, snapshot.podsByWorkload); diff != "" {
				t.Errorf("Unexpected pod assignments (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestPreemptWorkload(t *testing.T) {
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

	cases := map[string]struct {
		setup        func(sim *wasSimulator)
		preemptKey   types.NamespacedName
		wantFeasible bool
	}{
		"preempt existing workload and revert": {
			preemptKey:   key("default", "wl1"),
			wantFeasible: true,
		},
		"preempt non-existent workload": {
			preemptKey:   key("default", "non-existent"),
			wantFeasible: false,
		},
		"preempt when podsByWorkload is nil": {
			setup: func(sim *wasSimulator) {
				unassignedPod := makeSimplePod("unassigned", "default", "", "")
				sim.TrackPod(unassignedPod)
			},
			preemptKey:   key("default", "wl1"),
			wantFeasible: false,
		},
		"preempt workload after pod workload annotation update": {
			setup: func(sim *wasSimulator) {
				podWL1 := makeSimplePod("existing-pod", "default", kueue.WorkloadAnnotation, "wl1")
				podWL1.UID = "uid-1"
				podWL1.Spec = existingPod.Spec
				sim.TrackPod(podWL1)

				podWL2 := makeSimplePod("existing-pod", "default", kueue.WorkloadAnnotation, "wl2")
				podWL2.UID = "uid-1"
				podWL2.Spec = existingPod.Spec
				sim.TrackPod(podWL2)
			},
			preemptKey:   key("default", "wl2"),
			wantFeasible: true,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			simRaw, err := NewWASSimulatorForTest(ctx)
			if err != nil {
				t.Fatalf("NewWASSimulatorForTest failed: %v", err)
			}
			sim := simRaw.(*wasSimulator)
			sim.TrackPod(existingPod)
			if tc.setup != nil {
				tc.setup(sim)
			}

			snapshot, err := sim.Snapshot(ctx, nodes)
			if err != nil {
				t.Fatalf("Snapshot failed: %v", err)
			}

			if checkFeasible(snapshot) {
				t.Errorf("expected non-feasible before preemption")
			}

			revert, err := snapshot.PreemptWorkload(tc.preemptKey)
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

	simRaw, err := NewWASSimulatorForTest(ctx)
	if err != nil {
		t.Fatalf("NewWASSimulatorForTest failed: %v", err)
	}
	sim := simRaw.(*wasSimulator)
	sim.TrackPod(existingPod)

	snapshot, err := sim.Snapshot(ctx, nodes)
	if err != nil {
		t.Fatalf("Snapshot failed: %v", err)
	}

	if checkFeasible(snapshot) {
		t.Errorf("Expected node1 to be unfeasible before simulation")
	}

	simErr := snapshot.Simulate(func() {
		_, err := snapshot.PreemptWorkload(key("default", "wl1"))
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
	simRaw, err := NewWASSimulatorForTest(ctx)
	if err != nil {
		t.Fatalf("NewWASSimulatorForTest failed: %v", err)
	}
	sim := simRaw.(*wasSimulator)

	pod := makeSimplePod("pod1", "ns", kueue.WorkloadAnnotation, "wl1")
	sim.TrackPod(pod)

	// Mutate the pod object that was passed into TrackPod
	pod.Annotations[kueue.WorkloadAnnotation] = "mutated-wl"

	snapshotRaw, err := sim.Snapshot(ctx, nil)
	if err != nil {
		t.Fatalf("Snapshot failed: %v", err)
	}
	snapshot := snapshotRaw.(*wasSimulatorSnapshot)

	want := &podsByWorkload{
		key("ns", "wl1"): podsByKey{
			key("ns", "pod1"): makeSimplePod("pod1", "ns", kueue.WorkloadAnnotation, "wl1"),
		},
	}

	if diff := cmp.Diff(want, snapshot.podsByWorkload); diff != "" {
		t.Errorf("TrackPod did not deep copy pod (-want,+got):\n%s", diff)
	}
}
