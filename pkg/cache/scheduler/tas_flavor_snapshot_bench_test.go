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

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	"sigs.k8s.io/kueue/pkg/resources"
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

	for _, topo := range topologies {
		flavors := max(1, topo.flavors)
		name := fmt.Sprintf("nodes=%d/flavors=%d", topo.nodes, flavors)
		if topo.withNonTASPods {
			name += "/withNonTASPods"
		}
		b.Run(name, func(b *testing.B) {
			b.ReportAllocs()
			log := logr.Discard()

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
					tasCache.Update(pod, log)
				}
			}

			flavorCaches := make([]*TASFlavorCache, flavors)
			for i := range flavorCaches {
				flavorCaches[i] = tasCache.NewTASFlavorCache(
					topologyInformation{Levels: levels},
					flavorInformation{TopologyName: "default"},
				)
			}

			flavorNodes := make([][]*corev1.Node, len(flavorCaches))
			for i, flavorCache := range flavorCaches {
				flavorNodes[i] = tasCache.nodesCache.find(flavorCache.flavor.NodeLabels, flavorCache.topology.Levels)
			}

			for b.Loop() {
				for i, flavorCache := range flavorCaches {
					_, _ = flavorCache.snapshot(b.Context(), log, flavorNodes[i], nil)
				}
			}
		})
	}
}
