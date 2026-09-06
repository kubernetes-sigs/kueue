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
	"fmt"
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/resources"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	testingnode "sigs.k8s.io/kueue/pkg/util/testingjobs/node"
	testingpod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
)

const (
	benchBlockLabel = "cloud.provider.com/topology-block"
	benchRackLabel  = "cloud.provider.com/topology-rack"
	benchHostLabel  = corev1.LabelHostname
)

type benchTopology struct {
	nodes          int
	nodesPerRack   int
	racksPerBlock  int
	flavors        int
	withNonTASPods bool
}

type benchSnapshotMode struct {
	name            string
	heartbeatUpdate bool
	// invalidationPeriod bumps the nodesCache generation on every cycle that is a
	// multiple of it; 0 never invalidates.
	invalidationPeriod int
}

func buildBenchNodes(t benchTopology) []corev1.Node {
	nodes := make([]corev1.Node, 0, t.nodes)
	for i := range t.nodes {
		rack := i / t.nodesPerRack
		block := rack / t.racksPerBlock
		host := fmt.Sprintf("node-%d", i)
		node := testingnode.MakeNode(host).
			Label(benchBlockLabel, fmt.Sprintf("block-%d", block)).
			Label(benchRackLabel, fmt.Sprintf("rack-%d", rack)).
			Label(benchHostLabel, host).
			StatusAllocatable(corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("96"),
				corev1.ResourceMemory: resource.MustParse("384Gi"),
				corev1.ResourcePods:   resource.MustParse("110"),
			}).
			Ready().
			Obj()
		nodes = append(nodes, *node)
	}
	return nodes
}

func BenchmarkTASFlavorSnapshot(b *testing.B) {
	topologies := []benchTopology{
		{nodes: 100, nodesPerRack: 16, racksPerBlock: 16, withNonTASPods: true},
		{nodes: 500, nodesPerRack: 16, racksPerBlock: 16, withNonTASPods: true},
		{nodes: 2500, nodesPerRack: 16, racksPerBlock: 16, withNonTASPods: true},
		{nodes: 2500, nodesPerRack: 16, racksPerBlock: 16, flavors: 15, withNonTASPods: true},
		{nodes: 2500, nodesPerRack: 16, racksPerBlock: 16},
	}
	modes := []benchSnapshotMode{
		{name: "reuse"},
		{name: "heartbeat", heartbeatUpdate: true},
		{name: "invalidate-every-cycle", invalidationPeriod: 1},
		{name: "invalidate-every-2-cycles", invalidationPeriod: 2},
	}

	for _, topo := range topologies {
		flavors := max(1, topo.flavors)
		name := fmt.Sprintf("nodes=%d/flavors=%d", topo.nodes, flavors)
		if topo.withNonTASPods {
			name += "/withNonTASPods"
		}
		for _, mode := range modes {
			runBenchmarkTASFlavorSnapshot(b, topo, flavors, fmt.Sprintf("%s/mode=%s", name, mode.name), mode)
		}
	}
}

func runBenchmarkTASFlavorSnapshot(b *testing.B, topo benchTopology, flavors int, name string, mode benchSnapshotMode) {
	b.Run(name, func(b *testing.B) {
		b.ReportAllocs()
		_, log := utiltesting.ContextWithLog(b)

		nodes := buildBenchNodes(topo)
		levels := []string{benchBlockLabel, benchRackLabel, benchHostLabel}

		tasCache := NewTASCache(nil, newDefaultSimulator(), resources.NewResourceFormatter())
		for i := range nodes {
			tasCache.SyncNode(&nodes[i])
		}

		if topo.withNonTASPods {
			for i := range nodes {
				pod := testingpod.MakePod(fmt.Sprintf("bg-%d", i), "default").
					NodeName(nodes[i].Name).
					Request(corev1.ResourceCPU, "1").
					Request(corev1.ResourceMemory, "1Gi").
					StatusPhase(corev1.PodRunning).
					Obj()
				tasCache.UpdateNonTASUsage(pod, log)
			}
		}

		flavorCaches := make([]*TASFlavorCache, flavors)
		for i := range flavorCaches {
			flavorCaches[i] = tasCache.NewTASFlavorCache(
				topologyInformation{Levels: levels},
				flavorInformation{TopologyName: "default"},
			)
		}

		// Build the initial tree outside the timed loop. This makes reuse a
		// cache-hit-only benchmark and gives the update modes a tree to
		// invalidate.
		for _, flavorCache := range flavorCaches {
			if _, err := flavorCache.snapshot(b.Context(), log, newDefaultSimulatorSnapshot(), nil); err != nil {
				b.Fatalf("initial TASFlavorSnapshot creation failed: %v", err)
			}
		}

		// Only fields dropped by copyAndStripNode differ, so syncing this must not
		// bump the nodesCache generation.
		heartbeatNode := (&testingnode.NodeWrapper{Node: *nodes[0].DeepCopy()}).
			ResourceVersion("heartbeat").
			ConditionHeartbeat(corev1.NodeReady, metav1.NewTime(time.Unix(1, 0))).
			Obj()
		invalidatingNodes := []*corev1.Node{
			(&testingnode.NodeWrapper{Node: *nodes[0].DeepCopy()}).
				StatusAllocatable(corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("97")}).Obj(),
			(&testingnode.NodeWrapper{Node: *nodes[0].DeepCopy()}).
				StatusAllocatable(corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("96")}).Obj(),
		}

		cycle := 0
		for b.Loop() {
			switch {
			case mode.heartbeatUpdate:
				tasCache.SyncNode(heartbeatNode)
			case mode.invalidationPeriod > 0 && cycle%mode.invalidationPeriod == 0:
				update := cycle / mode.invalidationPeriod
				tasCache.SyncNode(invalidatingNodes[update%len(invalidatingNodes)])
			}
			for _, flavorCache := range flavorCaches {
				if _, err := flavorCache.snapshot(b.Context(), log, newDefaultSimulatorSnapshot(), nil); err != nil {
					b.Fatalf("TASFlavorSnapshot creation failed: %v", err)
				}
			}
			cycle++
		}
	})
}

func BenchmarkTASFlavorAssignment(b *testing.B) {
	features.SetFeatureGateDuringTest(b, features.TASBalancedPlacement, true)

	benchmarks := []struct {
		name       string
		topology   benchTopology
		withLeader bool
	}{
		{name: "nodes=100/workersOnly", topology: benchTopology{nodes: 100, nodesPerRack: 16, racksPerBlock: 16}},
		{name: "nodes=500/workersOnly", topology: benchTopology{nodes: 500, nodesPerRack: 16, racksPerBlock: 16}},
		{name: "nodes=2500/workersOnly", topology: benchTopology{nodes: 2500, nodesPerRack: 16, racksPerBlock: 16}},
		{name: "nodes=2500/withLeader", topology: benchTopology{nodes: 2500, nodesPerRack: 16, racksPerBlock: 16}, withLeader: true},
	}
	for _, benchmark := range benchmarks {
		runBenchmarkTASFlavorAssignment(b, benchmark.topology, benchmark.name, benchmark.withLeader)
	}
}

func runBenchmarkTASFlavorAssignment(b *testing.B, topo benchTopology, name string, withLeader bool) {
	b.Run(name, func(b *testing.B) {
		b.ReportAllocs()
		_, log := utiltesting.ContextWithLog(b)
		nodes := buildBenchNodes(topo)
		levels := []string{benchBlockLabel, benchRackLabel, benchHostLabel}

		tasCache := NewTASCache(nil, newDefaultSimulator(), resources.NewResourceFormatter())
		for i := range nodes {
			tasCache.SyncNode(&nodes[i])
		}
		flavorCache := tasCache.NewTASFlavorCache(
			topologyInformation{Levels: levels},
			flavorInformation{TopologyName: "default"},
		)
		snapshot, err := flavorCache.snapshot(b.Context(), log, newDefaultSimulatorSnapshot(), nil)
		if err != nil {
			b.Fatalf("TASFlavorSnapshot creation failed: %v", err)
		}

		requests := balancedPlacementBenchRequests(topo, withLeader)
		result := snapshot.FindTopologyAssignmentsForFlavor(b.Context(), requests)
		if failure := result.Failure(); failure != nil {
			b.Fatalf("balanced placement preflight failed: %s", failure.Reason)
		}
		if len(snapshot.domainStates) == snapshot.domainCount {
			b.Fatal("balanced placement preflight did not clone domain state")
		}

		for b.Loop() {
			result = snapshot.FindTopologyAssignmentsForFlavor(b.Context(), requests)
		}
		if failure := result.Failure(); failure != nil {
			b.Fatalf("repeated balanced placement failed: %s", failure.Reason)
		}
	})
}

func balancedPlacementBenchRequests(topo benchTopology, withLeader bool) FlavorTASRequests {
	preferredLevel := benchRackLabel
	groupName := "benchmark-group"
	nodesInBlock := min(topo.nodes, topo.nodesPerRack*topo.racksPerBlock)
	requests := FlavorTASRequests{{
		PodSet: &kueue.PodSet{
			Name: "workers",
			TopologyRequest: &kueue.PodSetTopologyRequest{
				Preferred: &preferredLevel,
			},
		},
		SinglePodRequests: resources.NewRequestsFromMap(map[corev1.ResourceName]int64{
			corev1.ResourceCPU: 8000,
		}),
		Count:           int32(nodesInBlock * 9),
		PodSetGroupName: &groupName,
	}}
	if withLeader {
		requests = append(requests, TASPodSetRequests{
			PodSet: &kueue.PodSet{Name: "leader"},
			SinglePodRequests: resources.NewRequestsFromMap(map[corev1.ResourceName]int64{
				corev1.ResourceCPU: 72000,
			}),
			Count:           1,
			PodSetGroupName: &groupName,
		})
	}
	return requests
}
