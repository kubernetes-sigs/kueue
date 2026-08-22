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

package scheduler

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/cache/scheduler/simulator"
	"sigs.k8s.io/kueue/pkg/features"
	utiltas "sigs.k8s.io/kueue/pkg/util/tas"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingnode "sigs.k8s.io/kueue/pkg/util/testingjobs/node"
)

type snapshotConsistencySimulator struct {
	simulator.SchedulingSimulator
	performOnSnapshot func()
	nodes             []*corev1.Node
}

func (s *snapshotConsistencySimulator) Snapshot(
	ctx context.Context,
	nodes []*corev1.Node,
) (simulator.SimulatorSnapshot, error) {
	s.nodes = nodes
	if s.performOnSnapshot != nil {
		s.performOnSnapshot()
	}
	return newDefaultSimulatorSnapshot(), nil
}

func TestSnapshotUsesConsistentTASNodesForSimulator(t *testing.T) {
	features.SetFeatureGateDuringTest(t, features.TopologyAwareScheduling, true)
	ctx, log := utiltesting.ContextWithLog(t)

	sim := &snapshotConsistencySimulator{
		SchedulingSimulator: newDefaultSimulator(),
	}
	cache := New(utiltesting.NewFakeClient(), WithSchedulingSimulator(sim))

	topology := utiltestingapi.MakeTopology("default").Levels(corev1.LabelHostname).Obj()
	cache.AddOrUpdateTopology(log, topology)

	rf := utiltestingapi.MakeResourceFlavor("tas-flavor").
		TopologyName("default").
		Obj()
	cache.AddOrUpdateResourceFlavor(log, rf)

	cq := utiltestingapi.MakeClusterQueue("cq").
		ResourceGroup(*utiltestingapi.MakeFlavorQuotas("tas-flavor").Resource(corev1.ResourceCPU, "10").Obj()).
		Obj()
	if err := cache.AddClusterQueue(ctx, cq); err != nil {
		t.Fatalf("Failed to add ClusterQueue: %v", err)
	}

	initialNode := testingnode.MakeNode("node-1").
		Label(corev1.LabelHostname, "node-1").
		StatusAllocatable(corev1.ResourceList{
			corev1.ResourceCPU: resource.MustParse("2"),
		}).
		Ready().
		Obj()
	cache.tasCache.SyncNode(initialNode)

	sim.performOnSnapshot = func() {
		updatedNode := testingnode.MakeNode("node-1").
			Label(corev1.LabelHostname, "node-1").
			StatusAllocatable(corev1.ResourceList{
				corev1.ResourceCPU: resource.MustParse("4"),
			}).
			Ready().
			Obj()
		cache.tasCache.SyncNode(updatedNode)
	}

	snapshot, err := cache.Snapshot(ctx)
	if err != nil {
		t.Fatalf("Snapshot failed: %v", err)
	}

	if len(sim.nodes) != 1 {
		t.Fatalf("Simulator received %d nodes, want 1", len(sim.nodes))
	}
	if sim.nodes[0].Name != "node-1" {
		t.Errorf("Simulator received node %q, want %q", sim.nodes[0].Name, "node-1")
	}
	if gotCPU := sim.nodes[0].Status.Allocatable.Cpu().Value(); gotCPU != 2 {
		t.Errorf("Simulator node allocatable CPU = %d, want 2", gotCPU)
	}

	cqSnapshot := snapshot.ClusterQueue(kueue.ClusterQueueReference("cq"))
	if cqSnapshot == nil {
		t.Fatal("ClusterQueue snapshot not found")
	}
	tasSnapshot, ok := cqSnapshot.TASFlavors[kueue.ResourceFlavorReference("tas-flavor")]
	if !ok || tasSnapshot == nil {
		t.Fatal("TAS flavor snapshot not found")
	}

	leaf := tasSnapshot.leaves[utiltas.TopologyDomainID("node-1")]
	if leaf == nil {
		t.Fatal("Leaf node-1 not found in TAS flavor snapshot")
	}
	if leaf.node != sim.nodes[0] {
		t.Errorf("Leaf node pointer %p differs from simulator node pointer %p", leaf.node, sim.nodes[0])
	}
	leafCap := tasSnapshot.leafCapacityOf(leaf)
	if gotCap := leafCap.freeCapacity.ResourceValue(corev1.ResourceCPU); gotCap != 2000 {
		t.Errorf("Leaf free CPU capacity = %d, want 2000 (from initial node generation)", gotCap)
	}

	if tasSnapshot.simulatorSnapshot != snapshot.SimulatorSnapshot || tasSnapshot.simulatorSnapshot == nil {
		t.Errorf("Simulator snapshot was not propagated to TAS flavor snapshot")
	}
}
