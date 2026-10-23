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
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/go-logr/logr"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	schedulingv1alpha3 "k8s.io/api/scheduling/v1alpha3"
	schedulingv1beta1 "k8s.io/api/scheduling/v1beta1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	utilfeature "k8s.io/apiserver/pkg/util/feature"
	"k8s.io/client-go/informers"
	"k8s.io/client-go/kubernetes/fake"
	featuregatetesting "k8s.io/component-base/featuregate/testing"
	"k8s.io/klog/v2"
	fwk "k8s.io/kube-scheduler/framework"
	kubefeatures "k8s.io/kubernetes/pkg/features"
	schedulerconfig "k8s.io/kubernetes/pkg/scheduler/apis/config"
	"k8s.io/kubernetes/pkg/scheduler/backend/cache"
	frameworkruntime "k8s.io/kubernetes/pkg/scheduler/framework/runtime"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/scheduler-library/pkg/framework"
	"sigs.k8s.io/scheduler-library/pkg/upstreamsync"
	schedLibSnapshot "sigs.k8s.io/scheduler-library/pkg/upstreamsync/snapshot"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/cache/scheduler/simulator"
	"sigs.k8s.io/kueue/pkg/features"
	utiltas "sigs.k8s.io/kueue/pkg/util/tas"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
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

	// No Workload annotation, so nothing can preempt it.
	unmanagedPod := testingpod.MakePod("unmanaged-pod", "default").
		UID("uid-2").
		NodeName("node1").
		StatusPhase(corev1.PodRunning).
		Port(8080, 8080, corev1.ProtocolTCP).
		Obj()

	tests := map[string]struct {
		addExistingPod  bool
		addUnmanagedPod bool
		simulateEmpty   bool
		candidateSpec   corev1.PodSpec
		wantFeasible    map[string]bool
	}{
		// Preemption cannot remove a Pod that no Workload owns, so it still holds
		// its host port even when the caller assumes every Workload is gone.
		"hostPort held by a Pod outside any Workload still excludes the node": {
			addUnmanagedPod: true,
			simulateEmpty:   true,
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
		// TAS asks this while deciding whether preemption could help. The Pod
		// holding the port is one of the Workloads that would be preempted, so
		// it must not count against the candidate.
		"hostPort conflict is ignored when the cluster is assumed empty": {
			addExistingPod: true,
			simulateEmpty:  true,
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
		// The simulator builds one profile, so a candidate naming a profile it does not
		// build is judged by that profile rather than failing the whole check.
		"another scheduler name does not change hostPort feasibility": {
			addExistingPod: true,
			candidateSpec: corev1.PodSpec{
				SchedulerName: "secondary-scheduler",
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
			simulatorFactory, err := NewWASSimulatorFactory(klog.NewContext(ctx, logr.Discard()), nil)
			if err != nil {
				t.Fatalf("NewWASSimulatorFactory failed: %v", err)
			}

			candidates := func(yield func(simulator.Candidate) bool) {
				for _, n := range nodes {
					if !yield(&testCandidate{node: n, id: utiltas.TopologyDomainID(n.Name)}) {
						return
					}
				}
			}

			if tc.addExistingPod {
				simulatorFactory.TrackPod(ctx, existingPod)
			}
			if tc.addUnmanagedPod {
				simulatorFactory.TrackPod(ctx, unmanagedPod)
			}
			schedulerSimulator, err := simulatorFactory.NewSimulator(ctx, nodes)

			if err != nil {
				t.Fatalf("CreateSnapshot failed: %v", err)
			}

			stats := &simulator.NodeExclusionStats{}
			podTemplate := &corev1.PodTemplateSpec{Spec: tc.candidateSpec}
			origSpec := *tc.candidateSpec.DeepCopy()
			results, err := schedulerSimulator.FindFeasibleNodes(ctx, candidates, &simulator.PodRequirements{
				PodTemplate:   podTemplate,
				SimulateEmpty: tc.simulateEmpty,
			}, stats)
			if err != nil {
				t.Fatalf("FindFeasibleNodes failed: %v", err)
			}

			if diff := cmp.Diff(origSpec, podTemplate.Spec); diff != "" {
				t.Errorf("PodTemplate.Spec was rewritten (-want,+got):\n%s", diff)
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
		simulatorFactory, err := NewWASSimulatorFactory(klog.NewContext(ctx, logr.Discard()), nil)
		if err != nil {
			t.Fatalf("NewWASSimulatorFactory failed: %v", err)
		}

		schedulerSimulator, err := simulatorFactory.NewSimulator(ctx, nodes)
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

		got, err := schedulerSimulator.FindFeasibleNodes(
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

// TestFindFeasibleNodesWithDeepIntegration checks that FindFeasibleNodes refuses
// to run when SchedulerLibraryDeepIntegration is enabled, as ScheduleWorkload is
// used instead.
func TestFindFeasibleNodesWithDeepIntegration(t *testing.T) {
	features.SetFeatureGateDuringTest(t, features.SchedulerLibraryDeepIntegration, true)
	ctx := klog.NewContext(t.Context(), logr.Discard())

	node := testingnode.MakeNode("node1").
		Label(corev1.LabelHostname, "node1").
		StatusAllocatable(corev1.ResourceList{
			corev1.ResourceCPU:  resource.MustParse("4"),
			corev1.ResourcePods: resource.MustParse("10"),
		}).
		Ready().
		Obj()
	nodes := []*corev1.Node{node}

	simulatorFactory, err := NewWASSimulatorFactory(ctx, nil)
	if err != nil {
		t.Fatalf("NewWASSimulatorFactory failed: %v", err)
	}
	schedulerSimulator, err := simulatorFactory.NewSimulator(ctx, nodes)
	if err != nil {
		t.Fatalf("NewSimulator failed: %v", err)
	}

	candidates := func(yield func(simulator.Candidate) bool) {
		yield(&testCandidate{node: node, id: utiltas.TopologyDomainID(node.Name)})
	}
	got, err := schedulerSimulator.FindFeasibleNodes(ctx, candidates, &simulator.PodRequirements{
		PodTemplate: &corev1.PodTemplateSpec{},
	}, &simulator.NodeExclusionStats{})
	if diff := cmp.Diff(errFindFeasibleNodesNotAllowed, err, cmpopts.EquateErrors()); diff != "" {
		t.Errorf("FindFeasibleNodes() unexpected error (-want,+got):\n%s", diff)
	}
	if got != nil {
		t.Errorf("FindFeasibleNodes() returned candidates %v, want none", got)
	}
}

// TestRepeatedSnapshots guards the informer factory against being shared across
// snapshots: the framework registers a DRA index on the factory it is given.
func TestRepeatedSnapshots(t *testing.T) {
	ctx := klog.NewContext(t.Context(), logr.Discard())

	simulatorFactory, err := NewWASSimulatorFactory(ctx, nil)
	if err != nil {
		t.Fatalf("NewWASSimulatorFactory failed: %v", err)
	}

	for i := range 3 {
		if _, err := simulatorFactory.NewSimulator(ctx, nil); err != nil {
			t.Fatalf("Snapshot %d failed: %v", i, err)
		}
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

	checkFeasible := func(schedulerSimulator simulator.SchedulerSimulator) bool {
		results, err := schedulerSimulator.FindFeasibleNodes(ctx, candidates, &simulator.PodRequirements{
			PodTemplate: &candidatePod,
		}, &simulator.NodeExclusionStats{})
		if err != nil {
			t.Fatalf("FindFeasibleNodes failed: %v", err)
		}
		return len(results) > 0
	}

	cases := map[string]struct {
		setup        func(context.Context, *wasSimulatorFactory)
		preemptKey   types.NamespacedName
		wantFeasible bool
	}{
		"preempt existing workload": {
			setup: func(ctx context.Context, simulatorFactory *wasSimulatorFactory) {
				simulatorFactory.TrackPod(ctx, existingPod)
			},
			preemptKey:   existingPodWlKey,
			wantFeasible: true,
		},
		"preempt non-existent workload": {
			setup: func(ctx context.Context, simulatorFactory *wasSimulatorFactory) {
				simulatorFactory.TrackPod(ctx, existingPod)
			},
			preemptKey:   types.NamespacedName{Namespace: "default", Name: "non-existent"},
			wantFeasible: false,
		},
		"preempt when unassigned pod exists": {
			setup: func(ctx context.Context, simulatorFactory *wasSimulatorFactory) {
				simulatorFactory.TrackPod(ctx, existingPod)
				unassignedPod := testingpod.MakePod("unassigned", "default").Annotation("", "").Obj()
				simulatorFactory.TrackPod(ctx, unassignedPod)
			},
			preemptKey:   existingPodWlKey,
			wantFeasible: true,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			simulatorFactory, err := NewWASSimulatorFactory(klog.NewContext(ctx, logr.Discard()), nil)
			if err != nil {
				t.Fatalf("NewWASSimulatorFactory failed: %v", err)
			}
			tc.setup(ctx, simulatorFactory)

			schedulerSimulator, err := simulatorFactory.NewSimulator(ctx, nodes)
			if err != nil {
				t.Fatalf("Snapshot failed: %v", err)
			}

			if checkFeasible(schedulerSimulator) {
				t.Errorf("expected non-feasible before preemption")
			}

			revert, err := schedulerSimulator.PreemptWorkload(ctx, tc.preemptKey)
			if err != nil {
				t.Fatalf("PreemptWorkload failed: %v", err)
			}

			if got := checkFeasible(schedulerSimulator); got != tc.wantFeasible {
				t.Errorf("checkFeasible after preemption = %v, want %v", got, tc.wantFeasible)
			}

			if err := revert(); err != nil {
				t.Fatalf("revert failed: %v", err)
			}

			if checkFeasible(schedulerSimulator) {
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

	checkFeasible := func(schedulerSimulator simulator.SchedulerSimulator) bool {
		results, err := schedulerSimulator.FindFeasibleNodes(ctx, candidateIter, &simulator.PodRequirements{
			PodTemplate: &candidatePod,
		}, &simulator.NodeExclusionStats{})
		if err != nil {
			t.Fatalf("FindFeasibleNodes failed: %v", err)
		}
		return len(results) > 0
	}

	simulatorFactory, err := NewWASSimulatorFactory(klog.NewContext(ctx, logr.Discard()), nil)
	if err != nil {
		t.Fatalf("NewWASSimulatorFactory failed: %v", err)
	}
	simulatorFactory.TrackPod(ctx, existingPod)

	schedulerSimulator, err := simulatorFactory.NewSimulator(ctx, nodes)
	if err != nil {
		t.Fatalf("Snapshot failed: %v", err)
	}

	if checkFeasible(schedulerSimulator) {
		t.Errorf("Expected node1 to be unfeasible before simulation")
	}

	simErr := schedulerSimulator.Simulate(ctx, func() {
		_, err := schedulerSimulator.PreemptWorkload(ctx, types.NamespacedName{Namespace: "default", Name: "wl1"})
		if err != nil {
			t.Fatalf("PreemptWorkload inside Simulate failed: %v", err)
		}

		if !checkFeasible(schedulerSimulator) {
			t.Errorf("Expected node1 to be feasible inside simulation after preemption")
		}
	})
	if simErr != nil {
		t.Fatalf("Simulation failed: %v", simErr)
	}

	if checkFeasible(schedulerSimulator) {
		t.Errorf("Expected node1 to be unfeasible after simulation completed (auto-reverted)")
	}
}

func TestSnapshotWithVirtualPods(t *testing.T) {
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

	wl := utiltestingapi.MakeWorkload("wl1", "default").
		UID("wl1-uid").
		PodSets(kueue.PodSet{
			Name:  "main",
			Count: 1,
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						Name:  "c",
						Ports: []corev1.ContainerPort{{HostPort: 8080, Protocol: corev1.ProtocolTCP}},
					}},
				},
			},
		}).
		Admission(
			utiltestingapi.MakeAdmission("cq").
				PodSets(kueue.PodSetAssignment{
					Name:  "main",
					Count: ptr.To[int32](1),
					TopologyAssignment: utiltestingapi.MakeTopologyAssignment([]string{corev1.LabelHostname}).
						Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"node1"}, 1).Obj()).
						Obj(),
				}).
				Obj(),
		).
		Obj()

	simulatorFactory, err := NewWASSimulatorFactory(klog.NewContext(ctx, logr.Discard()), nil)
	if err != nil {
		t.Fatalf("NewWASSimulatorFactory failed: %v", err)
	}

	schedulerSimulator, err := simulatorFactory.NewSimulator(ctx, nodes, simulator.WithAssumedWorkloads([]*kueue.Workload{wl}))
	if err != nil {
		t.Fatalf("Snapshot failed: %v", err)
	}

	candidateSpec := corev1.PodSpec{
		Containers: []corev1.Container{{
			Name:  "c2",
			Ports: []corev1.ContainerPort{{HostPort: 8080, Protocol: corev1.ProtocolTCP}},
		}},
	}
	candidates := func(yield func(simulator.Candidate) bool) {
		for _, n := range nodes {
			if !yield(&testCandidate{node: n, id: utiltas.TopologyDomainID(n.Name)}) {
				return
			}
		}
	}

	stats := &simulator.NodeExclusionStats{}
	results, err := schedulerSimulator.FindFeasibleNodes(ctx, candidates, &simulator.PodRequirements{
		PodTemplate: &corev1.PodTemplateSpec{Spec: candidateSpec},
	}, stats)
	if err != nil {
		t.Fatalf("FindFeasibleNodes failed: %v", err)
	}

	if len(results) != 1 || results[0].GetNode().Name != "node2" {
		t.Errorf("Expected only node2 to be feasible due to virtual pod on node1, got %v", results)
	}
}

func TestSnapshotVirtualPodsDeduplication(t *testing.T) {
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

	wl := utiltestingapi.MakeWorkload("wl1", "default").
		UID("wl1-uid").
		PodSets(kueue.PodSet{
			Name:  "main",
			Count: 2,
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{Name: "c"}},
				},
			},
		}).
		Admission(
			utiltestingapi.MakeAdmission("cq").
				PodSets(kueue.PodSetAssignment{
					Name:  "main",
					Count: ptr.To[int32](2),
					TopologyAssignment: utiltestingapi.MakeTopologyAssignment([]string{corev1.LabelHostname}).
						Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"node1"}, 1).Obj()).
						Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"node2"}, 1).Obj()).
						Obj(),
				}).
				Obj(),
		).
		Obj()

	simulatorFactory, err := NewWASSimulatorFactory(klog.NewContext(ctx, logr.Discard()), nil)
	if err != nil {
		t.Fatalf("NewWASSimulatorFactory failed: %v", err)
	}

	// Track 1 real pod on node1 for wl1
	realPod := testingpod.MakePod("real-pod-1", "default").
		UID("real-pod-1-uid").
		Annotation(kueue.WorkloadAnnotation, "wl1").
		NodeName("node1").
		StatusPhase(corev1.PodRunning).
		Obj()
	simulatorFactory.TrackPod(ctx, realPod)

	schedulerSimulator, err := simulatorFactory.NewSimulator(ctx, nodes, simulator.WithAssumedWorkloads([]*kueue.Workload{wl}))
	if err != nil {
		t.Fatalf("Snapshot failed: %v", err)
	}
	wasSim := schedulerSimulator.(*wasSimulator)

	pods := wasSim.podsByWorkload.getPodsForWorkload(types.NamespacedName{Namespace: "default", Name: "wl1"})
	if len(pods) != 2 {
		t.Fatalf("Expected 2 pods in podsByWorkload, got %d", len(pods))
	}

	for _, p := range pods {
		if !strings.HasPrefix(p.Name, "virtual-wl1-main-") {
			t.Errorf("Expected virtual pod, got real pod %q", p.Name)
		}
	}
}

func TestPreemptVirtualPods(t *testing.T) {
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

	wl := utiltestingapi.MakeWorkload("wl1", "default").
		UID("wl1-uid").
		PodSets(kueue.PodSet{
			Name:  "main",
			Count: 1,
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Containers: []corev1.Container{{
						Name:  "c",
						Ports: []corev1.ContainerPort{{HostPort: 8080, Protocol: corev1.ProtocolTCP}},
					}},
				},
			},
		}).
		Admission(
			utiltestingapi.MakeAdmission("cq").
				PodSets(kueue.PodSetAssignment{
					Name:  "main",
					Count: ptr.To[int32](1),
					TopologyAssignment: utiltestingapi.MakeTopologyAssignment([]string{corev1.LabelHostname}).
						Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"node1"}, 1).Obj()).
						Obj(),
				}).
				Obj(),
		).
		Obj()

	simulatorFactory, err := NewWASSimulatorFactory(klog.NewContext(ctx, logr.Discard()), nil)
	if err != nil {
		t.Fatalf("NewWASSimulatorFactory failed: %v", err)
	}

	schedulerSimulator, err := simulatorFactory.NewSimulator(ctx, nodes, simulator.WithAssumedWorkloads([]*kueue.Workload{wl}))
	if err != nil {
		t.Fatalf("Snapshot failed: %v", err)
	}

	candidateSpec := corev1.PodSpec{
		Containers: []corev1.Container{{
			Name:  "c2",
			Ports: []corev1.ContainerPort{{HostPort: 8080, Protocol: corev1.ProtocolTCP}},
		}},
	}
	candidates := func(yield func(simulator.Candidate) bool) {
		yield(&testCandidate{node: node1, id: utiltas.TopologyDomainID(node1.Name)})
	}

	checkFeasible := func() bool {
		results, err := schedulerSimulator.FindFeasibleNodes(ctx, candidates, &simulator.PodRequirements{
			PodTemplate: &corev1.PodTemplateSpec{Spec: candidateSpec},
		}, &simulator.NodeExclusionStats{})
		if err != nil {
			t.Fatalf("FindFeasibleNodes failed: %v", err)
		}
		return len(results) > 0
	}

	if checkFeasible() {
		t.Errorf("Expected node1 to be unfeasible due to virtual pod port conflict")
	}

	revert, err := schedulerSimulator.PreemptWorkload(ctx, types.NamespacedName{Namespace: "default", Name: "wl1"})
	if err != nil {
		t.Fatalf("PreemptWorkload failed: %v", err)
	}

	if !checkFeasible() {
		t.Errorf("Expected node1 to become feasible after preempting virtual workload")
	}

	if err := revert(); err != nil {
		t.Fatalf("revert failed: %v", err)
	}

	if checkFeasible() {
		t.Errorf("Expected node1 to be unfeasible after preemption was reverted")
	}
}

// TestPreemptWorkloadReleasesPodsOnEveryNode checks that PreemptWorkload releases
// every Pod of the victim, not just the first, and that the revert puts all of them
// back. TestPreemptWorkload covers one Pod on one node; a real victim spans many.
func TestPreemptWorkloadReleasesPodsOnEveryNode(t *testing.T) {
	ctx := t.Context()
	for _, nNodes := range []int{2, 3, 5} {
		t.Run(fmt.Sprintf("%d-nodes", nNodes), func(t *testing.T) {
			var nodes []*corev1.Node
			var cands []simulator.Candidate
			for i := range nNodes {
				name := fmt.Sprintf("n%d", i)
				n := testingnode.MakeNode(name).
					Label(corev1.LabelHostname, name).
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("4"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).Ready().Obj()
				nodes = append(nodes, n)
				cands = append(cands, &testCandidate{node: n, id: utiltas.TopologyDomainID(name)})
			}
			simulatorFactory, err := NewWASSimulatorFactory(ctx, nil)
			if err != nil {
				t.Fatal(err)
			}
			victim := client.ObjectKey{Namespace: "default", Name: "victim"}
			// One Pod per node, each holding the same host port, so every node is
			// blocked until the whole victim is released.
			for i := range nodes {
				simulatorFactory.TrackPod(ctx, testingpod.MakePod(fmt.Sprintf("victim-%d", i), victim.Namespace).
					UID(fmt.Sprintf("uid-%d", i)).
					Annotation(kueue.WorkloadAnnotation, victim.Name).
					NodeName(nodes[i].Name).
					StatusPhase(corev1.PodRunning).
					Port(8080, 8080, corev1.ProtocolTCP).
					Obj())
			}
			schedulerSimulator, err := simulatorFactory.NewSimulator(ctx, nodes)
			if err != nil {
				t.Fatal(err)
			}
			probe := testingpod.MakePod("probe", "default").Obj()
			probe.Spec.Containers[0].Ports = []corev1.ContainerPort{{ContainerPort: 8080, HostPort: 8080, Protocol: corev1.ProtocolTCP}}
			feasible := func() []string {
				var stats simulator.NodeExclusionStats
				got, err := schedulerSimulator.FindFeasibleNodes(ctx, slices.Values(cands),
					&simulator.PodRequirements{PodTemplate: &corev1.PodTemplateSpec{ObjectMeta: probe.ObjectMeta, Spec: probe.Spec}}, &stats)
				if err != nil {
					t.Fatal(err)
				}
				names := make([]string, 0, len(got))
				for _, c := range got {
					names = append(names, c.GetNode().Name)
				}
				slices.Sort(names)
				return names
			}
			if got := feasible(); len(got) != 0 {
				t.Fatalf("before preemption: want no feasible node, got %v", got)
			}
			revert, err := schedulerSimulator.PreemptWorkload(ctx, victim)
			if err != nil {
				t.Fatal(err)
			}
			if got := feasible(); len(got) != nNodes {
				t.Errorf("after preempting the victim: want all %d nodes free, got %v", nNodes, got)
			}
			if err := revert(); err != nil {
				t.Fatal(err)
			}
			if got := feasible(); len(got) != 0 {
				t.Errorf("after revert: want no feasible node, got %v", got)
			}
		})
	}
}

// preFilterErrorPluginName names preFilterErrorPlugin in the profile.
const preFilterErrorPluginName = "TestPreFilterError"

// preFilterErrorPodLabel marks the Pods preFilterErrorPlugin fails.
const preFilterErrorPodLabel = "test.kueue.x-k8s.io/prefilter-error"

// preFilterErrorPlugin fails PreFilter with an error status for Pods carrying
// preFilterErrorPodLabel.
// This is used to simulate a Pod-specific error being returned in scheduler-library result.
type preFilterErrorPlugin struct{}

var _ fwk.PreFilterPlugin = preFilterErrorPlugin{}

func (preFilterErrorPlugin) Name() string { return preFilterErrorPluginName }

func (preFilterErrorPlugin) PreFilter(_ context.Context, _ fwk.CycleState, pod *corev1.Pod, _ []fwk.NodeInfo) (*fwk.PreFilterResult, *fwk.Status) {
	if _, ok := pod.Labels[preFilterErrorPodLabel]; ok {
		return nil, fwk.AsStatus(fmt.Errorf("injected PreFilter error for pod %s", pod.Name))
	}
	return nil, nil
}

func (preFilterErrorPlugin) PreFilterExtensions() fwk.PreFilterExtensions { return nil }

// testSimulatorOption adjusts the profile and the out-of-tree plugin registry
// newTestSimulatorWithPodGroups builds the simulator with.
type testSimulatorOption func(profile *schedulerconfig.KubeSchedulerProfile, registry frameworkruntime.Registry)

// withPreFilterErrorPlugin adds preFilterErrorPlugin to the profile.
func withPreFilterErrorPlugin(profile *schedulerconfig.KubeSchedulerProfile, registry frameworkruntime.Registry) {
	registry[preFilterErrorPluginName] = func(context.Context, runtime.Object, fwk.Handle) (fwk.Plugin, error) {
		return preFilterErrorPlugin{}, nil
	}
	profile.Plugins.PreFilter.Enabled = append(profile.Plugins.PreFilter.Enabled, schedulerconfig.Plugin{Name: preFilterErrorPluginName})
}

// newTestSimulatorWithPodGroups builds a wasSimulator the same way the factory
// does, but seeds the snapshot with PodGroups, which ScheduleWorkload resolves the
// workload's Pods against and the factory has no way to provide yet.
func newTestSimulatorWithPodGroups(
	ctx context.Context,
	t *testing.T,
	nodes []*corev1.Node,
	existingPods []*corev1.Pod,
	podGroups []*schedulingv1beta1.PodGroup,
	opts ...testSimulatorOption,
) *wasSimulator {
	t.Helper()
	kubeClient := fake.NewSimpleClientset()
	informerFactory := informers.NewSharedInformerFactory(kubeClient, 0)
	_ = informerFactory.Core().V1().Nodes().Informer()
	_ = informerFactory.Core().V1().Pods().Informer()
	cfg := newWASSchedulerConfig()
	registry := frameworkruntime.Registry{}
	for _, opt := range opts {
		opt(&cfg.Profiles[0], registry)
	}
	comps, err := upstreamsync.NewFrameworkComponents(ctx, kubeClient, informerFactory,
		upstreamsync.WithProfiles(cfg.Profiles...),
		upstreamsync.WithFrameworkOutOfTreeRegistry(registry))
	if err != nil {
		t.Fatalf("NewFrameworkComponents failed: %v", err)
	}
	informerFactory.StartWithContext(ctx)
	if err := informerFactory.WaitForCacheSyncWithContext(ctx).AsError(); err != nil {
		t.Fatalf("WaitForCacheSync failed: %v", err)
	}
	if err := comps.WaitForHandlersSync(ctx); err != nil {
		t.Fatalf("WaitForHandlersSync failed: %v", err)
	}

	snap := cache.NewTestSnapshotWithPodGroups(existingPods, nodes, podGroups)
	profiles, err := upstreamsync.NewFrameworkMap(ctx, comps, framework.DiscardRecorderFactory, snap)
	if err != nil {
		t.Fatalf("NewFrameworkMap failed: %v", err)
	}
	framework.ApplySimulationNeutralizers(profiles)
	tracker := podTracker{
		pods:         make(podsByKey),
		workloadPods: make(podsByWorkload),
	}
	for _, pod := range existingPods {
		tracker.track(pod)
	}
	sim := &wasSimulator{
		wasSnapshot:    schedLibSnapshot.New(snap, profiles),
		podsByWorkload: tracker.workloadPods,
	}
	sim.emptyCluster.build = func(ctx context.Context) (*schedLibSnapshot.ClusterSnapshot, error) {
		emptySnap := cache.NewTestSnapshotWithPodGroups(podsNotManagedByKueue(existingPods, tracker.workloadPods), nodes, podGroups)
		emptyProfiles, err := upstreamsync.NewFrameworkMap(ctx, comps, framework.DiscardRecorderFactory, emptySnap)
		if err != nil {
			return nil, err
		}
		framework.ApplySimulationNeutralizers(emptyProfiles)
		return schedLibSnapshot.New(emptySnap, emptyProfiles), nil
	}
	return sim
}

// cmpErrorMessage compares errors by message, as the scheduler-library returns
// plain fmt.Errorf errors that cannot be matched with errors.Is.
var cmpErrorMessage = cmp.Comparer(func(a, b error) bool {
	if a == nil || b == nil {
		return a == b
	}
	return a.Error() == b.Error()
})

type fakePreemptionFilter struct {
	fitFunc   func(preempted sets.Set[client.ObjectKey]) bool
	preempted sets.Set[client.ObjectKey]
}

func newFakePreemptionFilter(fitFunc func(preempted sets.Set[client.ObjectKey]) bool) *fakePreemptionFilter {
	return &fakePreemptionFilter{
		fitFunc:   fitFunc,
		preempted: sets.New[client.ObjectKey](),
	}
}

func (f *fakePreemptionFilter) PreemptWorkload(wlKey client.ObjectKey) {
	f.preempted.Insert(wlKey)
}

func (f *fakePreemptionFilter) UnpreemptWorkload(wlKey client.ObjectKey) {
	f.preempted.Delete(wlKey)
}

func (f *fakePreemptionFilter) MayFit() bool {
	return f.fitFunc(f.preempted)
}

func TestScheduleWorkload(t *testing.T) {
	const ns = "default"

	makeNode := func(name, cpu string) *corev1.Node {
		return testingnode.MakeNode(name).
			Label(corev1.LabelHostname, name).
			StatusAllocatable(corev1.ResourceList{
				corev1.ResourceCPU:  resource.MustParse(cpu),
				corev1.ResourcePods: resource.MustParse("10"),
			}).
			Ready().
			Obj()
	}
	nodes := []*corev1.Node{makeNode("node1", "4"), makeNode("node2", "4")}

	// blocker holds host port 8080 on node1, so a Pod asking for that port can
	// only go to node2.
	blocker := testingpod.MakePod("blocker", ns).
		UID("blocker-uid").
		NodeName("node1").
		StatusPhase(corev1.PodRunning).
		Port(8080, 8080, corev1.ProtocolTCP).
		Obj()

	gangPodGroup := func(name string, minCount int32) *schedulingv1beta1.PodGroup {
		return &schedulingv1beta1.PodGroup{
			Name: name, Namespace: ns,
			Spec: schedulingv1beta1.PodGroupSpec{
				SchedulingPolicy: schedulingv1beta1.PodGroupSchedulingPolicy{
					Gang: &schedulingv1beta1.GangSchedulingPolicy{MinCount: minCount},
				},
			},
		}
	}
	// workloadPod returns a Pod of the scheduled workload, asking for host port
	// 8080 and belonging to the given PodGroup (none when podGroup is empty).
	workloadPod := func(name, podGroup string) *testingpod.PodWrapper {
		w := testingpod.MakePod(name, ns).
			UID(name+"-uid").
			Port(8080, 8080, corev1.ProtocolTCP)
		if podGroup != "" {
			w.Spec.SchedulingGroup = &corev1.PodSchedulingGroup{PodGroupName: new(podGroup)}
		}
		return w
	}
	key := func(name string) client.ObjectKey {
		return client.ObjectKey{Namespace: ns, Name: name}
	}

	// otherSchedulerPod is a workload Pod naming a scheduler the simulator does not build.
	otherSchedulerPod := workloadPod("p", "pg").Obj()
	otherSchedulerPod.Spec.SchedulerName = "secondary-scheduler"

	cases := map[string]struct {
		// nodes overrides the default two 4-CPU nodes when set.
		nodes          []*corev1.Node
		existingPods   []*corev1.Pod
		podGroups      []*schedulingv1beta1.PodGroup
		pods           []*corev1.Pod
		simulatorOpts  []testSimulatorOption
		scheduleOpts   []simulator.ScheduleOption
		wantPlacements simulator.PodPlacements
		wantPreempted  []client.ObjectKey
		wantError      error
	}{
		"no pods: no placements": {
			wantPlacements: simulator.PodPlacements{},
		},
		"pod outside any PodGroup: the result reports the error": {
			podGroups: []*schedulingv1beta1.PodGroup{gangPodGroup("pg", 1)},
			pods: []*corev1.Pod{
				workloadPod("in-group", "pg").Obj(),
				workloadPod("no-group", "").Obj(),
			},
			wantError: errors.New(`failed to build pod group hierarchy: validation failed: pod "no-group" does not belong to any PodGroup`),
		},
		"PodGroup missing from the snapshot: the result reports the error": {
			pods:      []*corev1.Pod{workloadPod("p", "missing").Obj()},
			wantError: errors.New("failed to build pod group hierarchy: failed to get pod group default/missing from snapshot: pod group state not found for pod group podgroup/default/missing"),
		},
		"pod is placed on the only feasible node": {
			existingPods: []*corev1.Pod{blocker},
			podGroups:    []*schedulingv1beta1.PodGroup{gangPodGroup("pg", 1)},
			pods:         []*corev1.Pod{workloadPod("p", "pg").Obj()},
			wantPlacements: simulator.PodPlacements{
				key("p"): {Node: "node2"},
			},
		},
		// The simulator builds one profile, so a workload naming a profile it does
		// not build is judged by that profile rather than failing to schedule.
		"pod naming another scheduler is placed on the only feasible node": {
			existingPods: []*corev1.Pod{blocker},
			podGroups:    []*schedulingv1beta1.PodGroup{gangPodGroup("pg", 1)},
			pods:         []*corev1.Pod{otherSchedulerPod},
			wantPlacements: simulator.PodPlacements{
				key("p"): {Node: "node2"},
			},
		},
		"pod with no feasible node fails": {
			existingPods: []*corev1.Pod{blocker},
			podGroups:    []*schedulingv1beta1.PodGroup{gangPodGroup("pg", 1)},
			pods:         []*corev1.Pod{workloadPod("p", "pg").NodeSelector(corev1.LabelHostname, "node1").Obj()},
			wantPlacements: simulator.PodPlacements{
				key("p"): {Reasons: []string{FailedReasonSkipped}},
			},
		},
		"pods of the workload are each placed on their feasible node": {
			podGroups: []*schedulingv1beta1.PodGroup{gangPodGroup("pg", 2)},
			pods: []*corev1.Pod{
				workloadPod("p1", "pg").NodeSelector(corev1.LabelHostname, "node1").Obj(),
				workloadPod("p2", "pg").NodeSelector(corev1.LabelHostname, "node2").Obj(),
			},
			wantPlacements: simulator.PodPlacements{
				key("p1"): {Node: "node1"},
				key("p2"): {Node: "node2"},
			},
		},
		// Both Pods want host port 8080 on node1, so only one of them could go
		// there. The library fails the whole workload when any of its Pods does
		// not fit, so none of them is placed, not even the one that would fit.
		"one pod of the workload cannot be placed: none of its pods is placed": {
			podGroups: []*schedulingv1beta1.PodGroup{gangPodGroup("pg", 2)},
			pods: []*corev1.Pod{
				workloadPod("p1", "pg").NodeSelector(corev1.LabelHostname, "node1").Obj(),
				workloadPod("p2", "pg").NodeSelector(corev1.LabelHostname, "node1").Obj(),
			},
			wantPlacements: simulator.PodPlacements{
				key("p1"): {Reasons: []string{FailedReasonSkipped}},
				key("p2"): {Reasons: []string{FailedReasonSkipped}},
			},
		},
		// The library tries the Pods in order and stops at the first one that
		// does not fit; when the PodGroup fails to schedule, PodResults is empty
		// so all pods are reported as skipped.
		"pods placed, errored and skipped by the library": {
			podGroups:     []*schedulingv1beta1.PodGroup{gangPodGroup("pg", 4)},
			simulatorOpts: []testSimulatorOption{withPreFilterErrorPlugin},
			pods: []*corev1.Pod{
				workloadPod("fits", "pg").Obj(),
				workloadPod("errors", "pg").Label(preFilterErrorPodLabel, "true").Obj(),
				workloadPod("skipped1", "pg").Obj(),
				workloadPod("skipped2", "pg").Obj(),
			},
			wantPlacements: simulator.PodPlacements{
				key("fits"):     {Reasons: []string{FailedReasonSkipped}},
				key("errors"):   {Reasons: []string{FailedReasonSkipped}},
				key("skipped1"): {Reasons: []string{FailedReasonSkipped}},
				key("skipped2"): {Reasons: []string{FailedReasonSkipped}},
			},
		},
		"node resources: only one of two pods fits: the workload fails": {
			nodes:     []*corev1.Node{makeNode("node1", "4"), makeNode("node2", "8")},
			podGroups: []*schedulingv1beta1.PodGroup{gangPodGroup("pg", 2)},
			pods: []*corev1.Pod{
				testingpod.MakePod("p1", ns).
					UID("p1-uid").
					Request(corev1.ResourceCPU, "6").
					SchedulingGroup("pg").
					Obj(),
				testingpod.MakePod("p2", ns).
					UID("p2-uid").
					Request(corev1.ResourceCPU, "6").
					SchedulingGroup("pg").
					Obj(),
			},
			wantPlacements: simulator.PodPlacements{
				key("p1"): {Reasons: []string{FailedReasonSkipped}},
				key("p2"): {Reasons: []string{FailedReasonSkipped}},
			},
		},
		// Ensure 6cpu pod ends up on the 8cpu node, and 3cpu pod on the 4cpu node.
		"node resources: pods are assigned to nodes that can fit them": {
			nodes:     []*corev1.Node{makeNode("node1", "4"), makeNode("node2", "8")},
			podGroups: []*schedulingv1beta1.PodGroup{gangPodGroup("pg", 2)},
			pods: []*corev1.Pod{
				testingpod.MakePod("p1", ns).
					UID("p1-uid").
					Request(corev1.ResourceCPU, "6").
					SchedulingGroup("pg").
					Obj(),
				testingpod.MakePod("p2", ns).
					UID("p2-uid").
					Request(corev1.ResourceCPU, "3").
					SchedulingGroup("pg").
					Obj(),
			},
			wantPlacements: simulator.PodPlacements{
				key("p1"): {Node: "node2"},
				key("p2"): {Node: "node1"},
			},
		},
		"node resources: pods filling a node each are placed on different nodes": {
			podGroups: []*schedulingv1beta1.PodGroup{gangPodGroup("pg", 2)},
			pods: []*corev1.Pod{
				testingpod.MakePod("p1", ns).
					UID("p1-uid").
					Request(corev1.ResourceCPU, "4").
					SchedulingGroup("pg").
					NodeSelector(corev1.LabelHostname, "node1").
					Obj(),
				testingpod.MakePod("p2", ns).
					UID("p2-uid").
					Request(corev1.ResourceCPU, "4").
					SchedulingGroup("pg").
					Obj(),
			},
			wantPlacements: simulator.PodPlacements{
				key("p1"): {Node: "node1"},
				key("p2"): {Node: "node2"},
			},
		},
		"preemption: pod fits on node after preempting candidate workload": {
			existingPods: []*corev1.Pod{
				testingpod.MakePod("victim-pod", ns).
					UID("victim-uid").
					Annotation(kueue.WorkloadAnnotation, "victim-wl").
					NodeName("node1").
					StatusPhase(corev1.PodRunning).
					Port(8080, 8080, corev1.ProtocolTCP).
					Obj(),
			},
			podGroups: []*schedulingv1beta1.PodGroup{gangPodGroup("pg", 1)},
			pods:      []*corev1.Pod{workloadPod("p", "pg").NodeSelector(corev1.LabelHostname, "node1").Obj()},
			scheduleOpts: []simulator.ScheduleOption{
				simulator.WithPreemptionCandidates(slices.Values([]client.ObjectKey{key("victim-wl")})),
			},
			wantPlacements: simulator.PodPlacements{
				key("p"): {Node: "node1"},
			},
			wantPreempted: []client.ObjectKey{key("victim-wl")},
		},
		"preemption: workload fits after preempting ordered candidate workloads": {
			nodes: []*corev1.Node{makeNode("node1", "6")},
			existingPods: []*corev1.Pod{
				testingpod.MakePod("victim-1-pod", ns).
					UID("victim-1-uid").
					Annotation(kueue.WorkloadAnnotation, "victim-1").
					NodeName("node1").
					StatusPhase(corev1.PodRunning).
					Request(corev1.ResourceCPU, "2").
					Obj(),
				testingpod.MakePod("victim-2-pod", ns).
					UID("victim-2-uid").
					Annotation(kueue.WorkloadAnnotation, "victim-2").
					NodeName("node1").
					StatusPhase(corev1.PodRunning).
					Request(corev1.ResourceCPU, "4").
					Obj(),
			},
			podGroups: []*schedulingv1beta1.PodGroup{gangPodGroup("pg", 2)},
			pods: []*corev1.Pod{
				testingpod.MakePod("p1", ns).
					UID("p1-uid").
					Request(corev1.ResourceCPU, "2").
					SchedulingGroup("pg").
					Obj(),
				testingpod.MakePod("p2", ns).
					UID("p2-uid").
					Request(corev1.ResourceCPU, "2").
					SchedulingGroup("pg").
					Obj(),
			},
			scheduleOpts: []simulator.ScheduleOption{
				simulator.WithPreemptionCandidates(slices.Values([]client.ObjectKey{key("victim-1"), key("victim-2")})),
			},
			wantPlacements: simulator.PodPlacements{
				key("p1"): {Node: "node1"},
				key("p2"): {Node: "node1"},
			},
			wantPreempted: []client.ObjectKey{key("victim-2")},
		},
		"preemption: fails when candidate workloads do not free enough resources": {
			nodes: []*corev1.Node{makeNode("node1", "6")},
			existingPods: []*corev1.Pod{
				testingpod.MakePod("victim-1-pod", ns).
					UID("victim-1-uid").
					Annotation(kueue.WorkloadAnnotation, "victim-1").
					NodeName("node1").
					StatusPhase(corev1.PodRunning).
					Request(corev1.ResourceCPU, "1").
					Obj(),
				testingpod.MakePod("other-pod", ns).
					UID("other-uid").
					Annotation(kueue.WorkloadAnnotation, "other-wl").
					NodeName("node1").
					StatusPhase(corev1.PodRunning).
					Request(corev1.ResourceCPU, "4").
					Obj(),
			},
			podGroups: []*schedulingv1beta1.PodGroup{gangPodGroup("pg", 2)},
			pods: []*corev1.Pod{
				testingpod.MakePod("p1", ns).
					UID("p1-uid").
					Request(corev1.ResourceCPU, "2").
					SchedulingGroup("pg").
					Obj(),
				testingpod.MakePod("p2", ns).
					UID("p2-uid").
					Request(corev1.ResourceCPU, "2").
					SchedulingGroup("pg").
					Obj(),
			},
			scheduleOpts: []simulator.ScheduleOption{
				simulator.WithPreemptionCandidates(slices.Values([]client.ObjectKey{key("victim-1")})),
			},
			wantPlacements: simulator.PodPlacements{
				key("p1"): {Reasons: []string{FailedReasonSkipped}},
				key("p2"): {Reasons: []string{FailedReasonSkipped}},
			},
		},
		"preemption with filter: pod fits on free node, filter selects minimal victims and reprieves unneeded ones": {
			existingPods: []*corev1.Pod{
				blocker,
				testingpod.MakePod("victim-1-pod", ns).
					UID("victim-1-uid").
					Annotation(kueue.WorkloadAnnotation, "victim-1").
					NodeName("node1").
					StatusPhase(corev1.PodRunning).
					Request(corev1.ResourceCPU, "1").
					Obj(),
				testingpod.MakePod("victim-2-pod", ns).
					UID("victim-2-uid").
					Annotation(kueue.WorkloadAnnotation, "victim-2").
					NodeName("node1").
					StatusPhase(corev1.PodRunning).
					Request(corev1.ResourceCPU, "1").
					Obj(),
			},
			podGroups: []*schedulingv1beta1.PodGroup{gangPodGroup("pg", 1)},
			pods:      []*corev1.Pod{workloadPod("p", "pg").Obj()},
			scheduleOpts: []simulator.ScheduleOption{
				simulator.WithPreemptionCandidates(slices.Values([]client.ObjectKey{key("victim-1"), key("victim-2")})),
				simulator.WithPreemptionFilter(newFakePreemptionFilter(func(preempted sets.Set[client.ObjectKey]) bool {
					return preempted.Has(key("victim-2"))
				})),
			},
			wantPlacements: simulator.PodPlacements{
				key("p"): {Node: "node2"},
			},
			wantPreempted: []client.ObjectKey{key("victim-2")},
		},
		"preemption with filter: node-level fit needs one victim, filter requires both victims": {
			nodes: []*corev1.Node{makeNode("node1", "6")},
			existingPods: []*corev1.Pod{
				testingpod.MakePod("victim-1-pod", ns).
					UID("victim-1-uid").
					Annotation(kueue.WorkloadAnnotation, "victim-1").
					NodeName("node1").
					StatusPhase(corev1.PodRunning).
					Request(corev1.ResourceCPU, "2").
					Obj(),
				testingpod.MakePod("victim-2-pod", ns).
					UID("victim-2-uid").
					Annotation(kueue.WorkloadAnnotation, "victim-2").
					NodeName("node1").
					StatusPhase(corev1.PodRunning).
					Request(corev1.ResourceCPU, "4").
					Obj(),
			},
			podGroups: []*schedulingv1beta1.PodGroup{gangPodGroup("pg", 2)},
			pods: []*corev1.Pod{
				testingpod.MakePod("p1", ns).
					UID("p1-uid").
					Request(corev1.ResourceCPU, "2").
					SchedulingGroup("pg").
					Obj(),
				testingpod.MakePod("p2", ns).
					UID("p2-uid").
					Request(corev1.ResourceCPU, "2").
					SchedulingGroup("pg").
					Obj(),
			},
			scheduleOpts: []simulator.ScheduleOption{
				simulator.WithPreemptionCandidates(slices.Values([]client.ObjectKey{key("victim-1"), key("victim-2")})),
				simulator.WithPreemptionFilter(newFakePreemptionFilter(func(preempted sets.Set[client.ObjectKey]) bool {
					return preempted.HasAll(key("victim-1"), key("victim-2"))
				})),
			},
			wantPlacements: simulator.PodPlacements{
				key("p1"): {Node: "node1"},
				key("p2"): {Node: "node1"},
			},
			wantPreempted: []client.ObjectKey{key("victim-1"), key("victim-2")},
		},
		"simulate empty: ignores pods managed by Kueue": {
			existingPods: []*corev1.Pod{
				testingpod.MakePod("managed-pod", ns).
					UID("managed-uid").
					Annotation(kueue.WorkloadAnnotation, "other-wl").
					NodeName("node1").
					StatusPhase(corev1.PodRunning).
					Port(8080, 8080, corev1.ProtocolTCP).
					Obj(),
			},
			podGroups: []*schedulingv1beta1.PodGroup{gangPodGroup("pg", 1)},
			pods:      []*corev1.Pod{workloadPod("p", "pg").NodeSelector(corev1.LabelHostname, "node1").Obj()},
			scheduleOpts: []simulator.ScheduleOption{
				simulator.WithSimulateEmpty(true),
			},
			wantPlacements: simulator.PodPlacements{
				key("p"): {Node: "node1"},
			},
		},
		"simulate empty: keeps pods not managed by Kueue": {
			existingPods: []*corev1.Pod{blocker},
			podGroups:    []*schedulingv1beta1.PodGroup{gangPodGroup("pg", 1)},
			pods:         []*corev1.Pod{workloadPod("p", "pg").NodeSelector(corev1.LabelHostname, "node1").Obj()},
			scheduleOpts: []simulator.ScheduleOption{
				simulator.WithSimulateEmpty(true),
			},
			wantPlacements: simulator.PodPlacements{
				key("p"): {Reasons: []string{FailedReasonSkipped}},
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			// The library only resolves the workload's PodGroups with this gate on.
			featuregatetesting.SetFeatureGateDuringTest(t, utilfeature.DefaultFeatureGate, kubefeatures.GenericWorkload, true)
			// ScheduleWorkload is only allowed with this gate on, which also
			// makes the simulator check node resources.
			features.SetFeatureGateDuringTest(t, features.SchedulerLibraryDeepIntegration, true)
			ctx := klog.NewContext(t.Context(), logr.Discard())
			caseNodes := nodes
			if tc.nodes != nil {
				caseNodes = tc.nodes
			}
			schedulerSimulator := newTestSimulatorWithPodGroups(ctx, t, caseNodes, tc.existingPods, tc.podGroups, tc.simulatorOpts...)

			// ScheduleWorkload runs dry, so asking twice must give the same answer:
			// the first call must not leave its Pods on the nodes.
			for attempt := range 2 {
				if opts := simulator.BuildScheduleOptions(tc.scheduleOpts...); opts.PreemptionFilter != nil {
					opts.PreemptionFilter.(*fakePreemptionFilter).preempted.Clear()
				}
				got := schedulerSimulator.ScheduleWorkload(ctx, tc.pods, tc.scheduleOpts...)
				if diff := cmp.Diff(&tc.wantError, &got.Error, cmpErrorMessage); diff != "" {
					t.Errorf("attempt %d: unexpected error (-want,+got):\n%s", attempt, diff)
				}
				if diff := cmp.Diff(tc.wantPlacements, got.PodPlacements, cmpErrorMessage); diff != "" {
					t.Errorf("attempt %d: unexpected placements (-want,+got):\n%s", attempt, diff)
				}
				if diff := cmp.Diff(tc.wantPreempted, got.PreemptedWorkloads); diff != "" {
					t.Errorf("attempt %d: unexpected preempted workloads (-want,+got):\n%s", attempt, diff)
				}
			}
		})
	}
}

// TestScheduleWorkloadWithoutDeepIntegration checks that ScheduleWorkload refuses
// to run when SchedulerLibraryDeepIntegration is disabled, as FindFeasibleNodes is
// used instead.
func TestScheduleWorkloadWithoutDeepIntegration(t *testing.T) {
	// The library only resolves the workload's PodGroups with this gate on.
	featuregatetesting.SetFeatureGateDuringTest(t, utilfeature.DefaultFeatureGate, kubefeatures.GenericWorkload, true)
	features.SetFeatureGateDuringTest(t, features.SchedulerLibraryDeepIntegration, false)
	ctx := klog.NewContext(t.Context(), logr.Discard())

	nodes := []*corev1.Node{
		testingnode.MakeNode("node1").
			Label(corev1.LabelHostname, "node1").
			StatusAllocatable(corev1.ResourceList{
				corev1.ResourceCPU:  resource.MustParse("4"),
				corev1.ResourcePods: resource.MustParse("10"),
			}).
			Ready().
			Obj(),
	}
	// A workload the library would place, so any error comes from the gate check.
	podGroups := []*schedulingv1beta1.PodGroup{{
		Name: "pg", Namespace: "default",
		Spec: schedulingv1beta1.PodGroupSpec{
			SchedulingPolicy: schedulingv1beta1.PodGroupSchedulingPolicy{
				Gang: &schedulingv1beta1.GangSchedulingPolicy{MinCount: 1},
			},
		},
	}}
	pod := testingpod.MakePod("p", "default").UID("p-uid").Obj()
	pod.Spec.SchedulingGroup = &corev1.PodSchedulingGroup{PodGroupName: new("pg")}

	schedulerSimulator := newTestSimulatorWithPodGroups(ctx, t, nodes, nil, podGroups)

	got := schedulerSimulator.ScheduleWorkload(ctx, []*corev1.Pod{pod})
	if diff := cmp.Diff(errScheduleWorkloadNotAllowed, got.Error, cmpopts.EquateErrors()); diff != "" {
		t.Errorf("ScheduleWorkload() unexpected error (-want,+got):\n%s", diff)
	}
	if got.PodPlacements != nil {
		t.Errorf("ScheduleWorkload() returned placements %v, want none", got.PodPlacements)
	}
}

func TestAddPodGroup(t *testing.T) {
	featuregatetesting.SetFeatureGateDuringTest(t, utilfeature.DefaultFeatureGate, kubefeatures.GenericWorkload, true)
	features.SetFeatureGateDuringTest(t, features.SchedulerLibraryDeepIntegration, true)
	ctx := t.Context()
	nodes := []*corev1.Node{
		testingnode.MakeNode("node1").
			Label(corev1.LabelHostname, "node1").
			StatusAllocatable(corev1.ResourceList{
				corev1.ResourceCPU:  resource.MustParse("4"),
				corev1.ResourcePods: resource.MustParse("10"),
			}).
			Ready().
			Obj(),
	}

	factory, err := NewWASSimulatorFactory(klog.NewContext(ctx, logr.Discard()), nil)
	if err != nil {
		t.Fatalf("NewWASSimulatorFactory failed: %v", err)
	}
	sim, err := factory.NewSimulator(ctx, nodes)
	if err != nil {
		t.Fatalf("NewSimulator failed: %v", err)
	}

	pg := &schedulingv1beta1.PodGroup{
		Name:      "pg",
		Namespace: "default",
		Spec: schedulingv1beta1.PodGroupSpec{
			SchedulingPolicy: schedulingv1beta1.PodGroupSchedulingPolicy{
				Gang: &schedulingv1beta1.GangSchedulingPolicy{MinCount: 1},
			},
		},
	}
	if err := sim.AddPodGroup(ctx, pg); err != nil {
		t.Fatalf("AddPodGroup failed: %v", err)
	}

	pod := testingpod.MakePod("p1", "default").UID("p1-uid").Obj()
	pod.Spec.SchedulingGroup = &corev1.PodSchedulingGroup{PodGroupName: new("pg")}

	got := sim.ScheduleWorkload(ctx, []*corev1.Pod{pod})
	want := simulator.PodPlacements{client.ObjectKeyFromObject(pod): {Node: "node1"}}
	if diff := cmp.Diff(want, got.PodPlacements); diff != "" {
		t.Errorf("unexpected placements (-want +got):\n%s", diff)
	}
}

func TestRemovePodGroup(t *testing.T) {
	featuregatetesting.SetFeatureGateDuringTest(t, utilfeature.DefaultFeatureGate, kubefeatures.GenericWorkload, true)
	features.SetFeatureGateDuringTest(t, features.SchedulerLibraryDeepIntegration, true)
	ctx := t.Context()
	nodes := []*corev1.Node{
		testingnode.MakeNode("node1").
			Label(corev1.LabelHostname, "node1").
			StatusAllocatable(corev1.ResourceList{
				corev1.ResourceCPU:  resource.MustParse("4"),
				corev1.ResourcePods: resource.MustParse("10"),
			}).
			Ready().
			Obj(),
	}

	factory, err := NewWASSimulatorFactory(klog.NewContext(ctx, logr.Discard()), nil)
	if err != nil {
		t.Fatalf("NewWASSimulatorFactory failed: %v", err)
	}
	sim, err := factory.NewSimulator(ctx, nodes)
	if err != nil {
		t.Fatalf("NewSimulator failed: %v", err)
	}

	pg := &schedulingv1beta1.PodGroup{
		Name:      "pg",
		Namespace: "default",
		Spec: schedulingv1beta1.PodGroupSpec{
			SchedulingPolicy: schedulingv1beta1.PodGroupSchedulingPolicy{
				Gang: &schedulingv1beta1.GangSchedulingPolicy{MinCount: 1},
			},
		},
	}
	if err := sim.AddPodGroup(ctx, pg); err != nil {
		t.Fatalf("AddPodGroup failed: %v", err)
	}

	pod := testingpod.MakePod("p1", "default").UID("p1-uid").Obj()
	pod.Spec.SchedulingGroup = &corev1.PodSchedulingGroup{PodGroupName: new("pg")}

	got := sim.ScheduleWorkload(ctx, []*corev1.Pod{pod})
	want := simulator.PodPlacements{client.ObjectKeyFromObject(pod): {Node: "node1"}}
	if diff := cmp.Diff(want, got.PodPlacements); diff != "" {
		t.Fatalf("unexpected placements (-want +got):\n%s", diff)
	}

	if err := sim.RemovePodGroup(ctx, pg); err != nil {
		t.Fatalf("RemovePodGroup failed: %v", err)
	}

	got = sim.ScheduleWorkload(ctx, []*corev1.Pod{pod})
	wantErr := errors.New("failed to build pod group hierarchy: failed to get pod group default/pg from snapshot: pod group state not found for pod group podgroup/default/pg")
	if diff := cmp.Diff(&wantErr, &got.Error, cmpErrorMessage); diff != "" {
		t.Errorf("ScheduleWorkload() after RemovePodGroup unexpected error (-want,+got):\n%s", diff)
	}

	err = sim.RemovePodGroup(ctx, pg)
	wantErr = errors.New("pod group podgroup/default/pg not found in snapshot")
	if diff := cmp.Diff(&wantErr, &err, cmpErrorMessage); diff != "" {
		t.Errorf("second RemovePodGroup() unexpected error (-want,+got):\n%s", diff)
	}
}

func TestAddCompositePodGroup(t *testing.T) {
	featuregatetesting.SetFeatureGatesDuringTest(t, utilfeature.DefaultFeatureGate, featuregatetesting.FeatureOverrides{
		kubefeatures.GenericWorkload:                 true,
		kubefeatures.TopologyAwareWorkloadScheduling: true,
		kubefeatures.CompositePodGroup:               true,
	})
	features.SetFeatureGateDuringTest(t, features.SchedulerLibraryDeepIntegration, true)
	ctx := t.Context()
	nodes := []*corev1.Node{
		testingnode.MakeNode("node1").
			Label(corev1.LabelHostname, "node1").
			StatusAllocatable(corev1.ResourceList{
				corev1.ResourceCPU:  resource.MustParse("4"),
				corev1.ResourcePods: resource.MustParse("10"),
			}).
			Ready().
			Obj(),
	}

	factory, err := NewWASSimulatorFactory(klog.NewContext(ctx, logr.Discard()), nil)
	if err != nil {
		t.Fatalf("NewWASSimulatorFactory failed: %v", err)
	}
	sim, err := factory.NewSimulator(ctx, nodes)
	if err != nil {
		t.Fatalf("NewSimulator failed: %v", err)
	}

	cpg := &schedulingv1alpha3.CompositePodGroup{
		Name:      "cpg",
		Namespace: "default",
		Spec: schedulingv1alpha3.CompositePodGroupSpec{
			SchedulingPolicy: schedulingv1alpha3.CompositePodGroupSchedulingPolicy{
				Gang: &schedulingv1alpha3.CompositeGangSchedulingPolicy{MinGroupCount: 1},
			},
		},
	}
	if err := sim.AddCompositePodGroup(ctx, cpg); err != nil {
		t.Fatalf("AddCompositePodGroup failed: %v", err)
	}

	pg := &schedulingv1beta1.PodGroup{
		Name:      "pg",
		Namespace: "default",
		Spec: schedulingv1beta1.PodGroupSpec{
			SchedulingPolicy: schedulingv1beta1.PodGroupSchedulingPolicy{
				Gang: &schedulingv1beta1.GangSchedulingPolicy{MinCount: 1},
			},
			ParentCompositePodGroupName: new("cpg"),
		},
	}
	if err := sim.AddPodGroup(ctx, pg); err != nil {
		t.Fatalf("AddPodGroup failed: %v", err)
	}

	pod := testingpod.MakePod("p1", "default").UID("p1-uid").Obj()
	pod.Spec.SchedulingGroup = &corev1.PodSchedulingGroup{PodGroupName: new("pg")}

	got := sim.ScheduleWorkload(ctx, []*corev1.Pod{pod})
	want := simulator.PodPlacements{client.ObjectKeyFromObject(pod): {Node: "node1"}}
	if diff := cmp.Diff(want, got.PodPlacements); diff != "" {
		t.Errorf("unexpected placements (-want +got):\n%s", diff)
	}
}

func TestRemoveCompositePodGroup(t *testing.T) {
	featuregatetesting.SetFeatureGatesDuringTest(t, utilfeature.DefaultFeatureGate, featuregatetesting.FeatureOverrides{
		kubefeatures.GenericWorkload:                 true,
		kubefeatures.TopologyAwareWorkloadScheduling: true,
		kubefeatures.CompositePodGroup:               true,
	})
	features.SetFeatureGateDuringTest(t, features.SchedulerLibraryDeepIntegration, true)
	ctx := t.Context()
	nodes := []*corev1.Node{
		testingnode.MakeNode("node1").
			Label(corev1.LabelHostname, "node1").
			StatusAllocatable(corev1.ResourceList{
				corev1.ResourceCPU:  resource.MustParse("4"),
				corev1.ResourcePods: resource.MustParse("10"),
			}).
			Ready().
			Obj(),
	}

	factory, err := NewWASSimulatorFactory(klog.NewContext(ctx, logr.Discard()), nil)
	if err != nil {
		t.Fatalf("NewWASSimulatorFactory failed: %v", err)
	}
	sim, err := factory.NewSimulator(ctx, nodes)
	if err != nil {
		t.Fatalf("NewSimulator failed: %v", err)
	}

	cpg := &schedulingv1alpha3.CompositePodGroup{
		Name:      "cpg",
		Namespace: "default",
		Spec: schedulingv1alpha3.CompositePodGroupSpec{
			SchedulingPolicy: schedulingv1alpha3.CompositePodGroupSchedulingPolicy{
				Gang: &schedulingv1alpha3.CompositeGangSchedulingPolicy{MinGroupCount: 1},
			},
		},
	}
	if err := sim.AddCompositePodGroup(ctx, cpg); err != nil {
		t.Fatalf("AddCompositePodGroup failed: %v", err)
	}

	pg := &schedulingv1beta1.PodGroup{
		Name:      "pg",
		Namespace: "default",
		Spec: schedulingv1beta1.PodGroupSpec{
			SchedulingPolicy: schedulingv1beta1.PodGroupSchedulingPolicy{
				Gang: &schedulingv1beta1.GangSchedulingPolicy{MinCount: 1},
			},
			ParentCompositePodGroupName: new("cpg"),
		},
	}
	if err := sim.AddPodGroup(ctx, pg); err != nil {
		t.Fatalf("AddPodGroup failed: %v", err)
	}

	pod := testingpod.MakePod("p1", "default").UID("p1-uid").Obj()
	pod.Spec.SchedulingGroup = &corev1.PodSchedulingGroup{PodGroupName: new("pg")}

	got := sim.ScheduleWorkload(ctx, []*corev1.Pod{pod})
	want := simulator.PodPlacements{client.ObjectKeyFromObject(pod): {Node: "node1"}}
	if diff := cmp.Diff(want, got.PodPlacements); diff != "" {
		t.Fatalf("unexpected placements (-want +got):\n%s", diff)
	}

	if err := sim.RemoveCompositePodGroup(ctx, cpg); err != nil {
		t.Fatalf("RemoveCompositePodGroup failed: %v", err)
	}

	got = sim.ScheduleWorkload(ctx, []*corev1.Pod{pod})
	wantErr := errors.New("failed to build pod group hierarchy: failed to get composite pod group default/cpg from snapshot: " +
		`composite pod group object not found for pod group "compositepodgroup/default/cpg"`)
	if diff := cmp.Diff(&wantErr, &got.Error, cmpErrorMessage); diff != "" {
		t.Errorf("ScheduleWorkload() after RemoveCompositePodGroup unexpected error (-want,+got):\n%s", diff)
	}

	err = sim.RemoveCompositePodGroup(ctx, cpg)
	wantErr = errors.New("composite pod group compositepodgroup/default/cpg not found in snapshot")
	if diff := cmp.Diff(&wantErr, &err, cmpErrorMessage); diff != "" {
		t.Errorf("second RemoveCompositePodGroup() unexpected error (-want,+got):\n%s", diff)
	}
}
