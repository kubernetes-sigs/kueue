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
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"

	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/util/testingjobs/node"
)

func TestNodesCache(t *testing.T) {
	nodeWrapper := node.MakeNode("test")

	testCases := map[string]struct {
		nodes     []corev1.Node
		op        func(nc *nodesCache)
		wantNodes []corev1.Node
	}{
		"sync not ready": {
			op: func(nc *nodesCache) {
				nc.sync(nodeWrapper.DeepCopy())
			},
		},
		"sync unschedulable and not ready": {
			op: func(nc *nodesCache) {
				nc.sync(nodeWrapper.Clone().Unschedulable().Obj())
			},
		},
		"sync ready and unschedulable": {
			op: func(nc *nodesCache) {
				nc.sync(nodeWrapper.Clone().Ready().Unschedulable().Obj())
			},
		},
		"sync ready": {
			op: func(nc *nodesCache) {
				nc.sync(nodeWrapper.Clone().Ready().Obj())
			},
			wantNodes: []corev1.Node{*nodeWrapper.Clone().Ready().Obj()},
		},
		"sync ready to not ready": {
			nodes: []corev1.Node{*nodeWrapper.Clone().Ready().Obj()},
			op: func(nc *nodesCache) {
				nc.sync(nodeWrapper.DeepCopy())
			},
		},
		"sync ready to unschedulable": {
			nodes: []corev1.Node{*nodeWrapper.Clone().Ready().Obj()},
			op: func(nc *nodesCache) {
				nc.sync(nodeWrapper.Clone().Unschedulable().Obj())
			},
		},
		"delete": {
			nodes: []corev1.Node{*nodeWrapper.Clone().Ready().Obj()},
			op: func(nc *nodesCache) {
				nc.delete(nodeWrapper.Node.Name)
			},
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			nc := newNodesCache()

			for i := range tc.nodes {
				nc.nodes[tc.nodes[i].Name] = copyAndStripNode(&tc.nodes[i])
			}

			tc.op(nc)

			wantNodesMap := make(map[string]*corev1.Node, len(tc.wantNodes))
			for i := range tc.wantNodes {
				wantNodesMap[tc.wantNodes[i].Name] = copyAndStripNode(&tc.wantNodes[i])
			}

			if diff := cmp.Diff(wantNodesMap, nc.nodes); diff != "" {
				t.Errorf("Unexpected nodes (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestNodesCacheFind(t *testing.T) {
	nc := newNodesCache()

	node1 := node.MakeNode("test1").Obj()
	node2 := node.MakeNode("test2").Label("cloud.provider.com/zone", "us-east-1a").Obj()
	node3 := node.MakeNode("test3").
		Label("cloud.provider.com/zone", "us-east-1a").
		Label("cloud.provider.com/topology-block", "b1").
		Obj()
	node4 := node.MakeNode("test4").Label("cloud.provider.com/zone", "us-east-1").Obj()

	nodes := []corev1.Node{*node1, *node2, *node3, *node4}

	for i := range nodes {
		nc.nodes[nodes[i].Name] = copyAndStripNode(&nodes[i])
	}

	testCases := map[string]struct {
		nodeLabels map[string]string
		levels     []string
		wantNodes  []*corev1.Node
	}{
		"no nodeLabels and levels": {
			wantNodes: []*corev1.Node{
				copyAndStripNode(node1),
				copyAndStripNode(node2),
				copyAndStripNode(node3),
				copyAndStripNode(node4),
			},
		},
		"match labels": {
			nodeLabels: map[string]string{"cloud.provider.com/zone": "us-east-1a"},
			wantNodes:  []*corev1.Node{copyAndStripNode(node2), copyAndStripNode(node3)},
		},
		"match levels": {
			levels:    []string{"cloud.provider.com/topology-block"},
			wantNodes: []*corev1.Node{copyAndStripNode(node3)},
		},
		"match labels and levels": {
			nodeLabels: map[string]string{"cloud.provider.com/zone": "us-east-1a"},
			levels:     []string{"cloud.provider.com/topology-block"},
			wantNodes:  []*corev1.Node{copyAndStripNode(node3)},
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			gotNodes, _ := nc.find(tc.nodeLabels, tc.levels)
			if diff := cmp.Diff(tc.wantNodes, gotNodes, cmpopts.SortSlices(func(a, b *corev1.Node) bool {
				return a.Name < b.Name
			})); diff != "" {
				t.Errorf("Unexpected nodes (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestNodesCacheGeneration(t *testing.T) {
	baseNode := func() *node.NodeWrapper {
		return node.MakeNode("gen-test").
			Label("cloud.provider.com/zone", "us-east-1a").
			StatusAllocatable(corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("4"),
				corev1.ResourceMemory: resource.MustParse("16Gi"),
			}).
			Ready()
	}

	testCases := map[string]struct {
		prime     []*corev1.Node
		op        func(nc *nodesCache)
		wantDelta int64
	}{
		"sync of a new ready node bumps": {
			op: func(nc *nodesCache) {
				nc.sync(baseNode().Obj())
			},
			wantDelta: 1,
		},
		"re-sync of an identical node does not bump": {
			prime: []*corev1.Node{baseNode().Obj()},
			op: func(nc *nodesCache) {
				nc.sync(baseNode().Obj())
			},
			wantDelta: 0,
		},
		"heartbeat-only update does not bump": {
			prime: []*corev1.Node{baseNode().Obj()},
			op: func(nc *nodesCache) {
				nc.sync(baseNode().
					ResourceVersion("2").
					ConditionHeartbeat(corev1.NodeReady, metav1.Now()).
					Obj())
			},
			wantDelta: 0,
		},
		"equivalent allocatable expressed in different units does not bump": {
			prime: []*corev1.Node{baseNode().Obj()},
			op: func(nc *nodesCache) {
				nc.sync(baseNode().StatusAllocatable(corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("4000m"),
					corev1.ResourceMemory: resource.MustParse("16Gi"),
				}).Obj())
			},
			wantDelta: 0,
		},
		"label change bumps": {
			prime: []*corev1.Node{baseNode().Obj()},
			op: func(nc *nodesCache) {
				nc.sync(baseNode().Label("cloud.provider.com/zone", "us-east-1b").Obj())
			},
			wantDelta: 1,
		},
		"allocatable change bumps": {
			prime: []*corev1.Node{baseNode().Obj()},
			op: func(nc *nodesCache) {
				nc.sync(baseNode().StatusAllocatable(corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("8"),
					corev1.ResourceMemory: resource.MustParse("16Gi"),
				}).Obj())
			},
			wantDelta: 1,
		},
		"taint change bumps": {
			prime: []*corev1.Node{baseNode().Obj()},
			op: func(nc *nodesCache) {
				nc.sync(baseNode().Taints(corev1.Taint{
					Key:    "example.com/gpu",
					Effect: corev1.TaintEffectNoSchedule,
				}).Obj())
			},
			wantDelta: 1,
		},
		"transition to unschedulable removes the node and bumps": {
			prime: []*corev1.Node{baseNode().Obj()},
			op: func(nc *nodesCache) {
				nc.sync(baseNode().Unschedulable().Obj())
			},
			wantDelta: 1,
		},
		"sync of an absent not-ready node does not bump": {
			op: func(nc *nodesCache) {
				nc.sync(node.MakeNode("gen-test").Obj())
			},
			wantDelta: 0,
		},
		"delete of an existing node bumps": {
			prime: []*corev1.Node{baseNode().Obj()},
			op: func(nc *nodesCache) {
				nc.delete("gen-test")
			},
			wantDelta: 1,
		},
		"delete of an absent node does not bump": {
			op: func(nc *nodesCache) {
				nc.delete("other")
			},
			wantDelta: 0,
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			nc := newNodesCache()
			for _, n := range tc.prime {
				nc.sync(n)
			}
			before := nc.currentGeneration()
			tc.op(nc)
			if delta := nc.currentGeneration() - before; delta != tc.wantDelta {
				t.Errorf("unexpected generation delta: got %d, want %d", delta, tc.wantDelta)
			}
		})
	}
}

func TestNodesCacheSync(t *testing.T) {
	nodeWrapper := node.MakeNode("test")

	testCases := map[string]struct {
		enableSchedulerLibraryIntegration bool
		prime                             []corev1.Node
		node                              *corev1.Node
		wantNodes                         []corev1.Node
		wantSchedulableAndReady           []string
	}{
		"FG disabled: sync not ready removes node": {
			node: nodeWrapper.DeepCopy(),
		},
		"FG disabled: sync ready adds node": {
			node:                    nodeWrapper.Clone().Ready().Obj(),
			wantNodes:               []corev1.Node{*nodeWrapper.Clone().Ready().Obj()},
			wantSchedulableAndReady: []string{"test"},
		},
		"FG enabled: sync not ready keeps node in cache but not in schedulableAndReady": {
			enableSchedulerLibraryIntegration: true,
			node:                              nodeWrapper.DeepCopy(),
			wantNodes:                         []corev1.Node{*nodeWrapper.DeepCopy()},
		},
		"FG enabled: sync unschedulable keeps node in cache but not in schedulableAndReady": {
			enableSchedulerLibraryIntegration: true,
			node:                              nodeWrapper.Clone().Ready().Unschedulable().Obj(),
			wantNodes:                         []corev1.Node{*nodeWrapper.Clone().Ready().Unschedulable().Obj()},
		},
		"FG enabled: sync ready adds node to cache and schedulableAndReady": {
			enableSchedulerLibraryIntegration: true,
			node:                              nodeWrapper.Clone().Ready().Obj(),
			wantNodes:                         []corev1.Node{*nodeWrapper.Clone().Ready().Obj()},
			wantSchedulableAndReady:           []string{"test"},
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.SchedulerLibraryIntegration, tc.enableSchedulerLibraryIntegration)
			nc := newNodesCache()
			for i := range tc.prime {
				nc.sync(&tc.prime[i])
			}
			nc.sync(tc.node)

			wantNodeNameNodes := make(map[string]*corev1.Node, len(tc.wantNodes))
			for i := range tc.wantNodes {
				wantNodeNameNodes[tc.wantNodes[i].Name] = copyAndStripNode(&tc.wantNodes[i])
			}
			if diff := cmp.Diff(wantNodeNameNodes, nc.nodes, cmpopts.SortMaps(func(a, b string) bool {
				return a < b
			})); diff != "" {
				t.Errorf("Unexpected nodes (-want,+got):\n%s", diff)
			}
			if diff := cmp.Diff(sets.New(tc.wantSchedulableAndReady...), nc.schedulableAndReadyNodes); diff != "" {
				t.Errorf("Unexpected schedulableAndReadyNodes (-want,+got):\n%s", diff)
			}
		})
	}
}
