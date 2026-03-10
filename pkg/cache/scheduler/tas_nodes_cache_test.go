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
				nc.sync(nodeWrapper.Clone().Obj())
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
				nc.nodes[tc.nodes[i].Name] = newNodeInfo(&tc.nodes[i])
			}

			tc.op(nc)

			wantNodesMap := make(map[string]*nodeInfo, len(tc.wantNodes))
			for i := range tc.wantNodes {
				wantNodesMap[tc.wantNodes[i].Name] = newNodeInfo(&tc.wantNodes[i])
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
		nc.nodes[nodes[i].Name] = newNodeInfo(&nodes[i])
	}

	testCases := map[string]struct {
		nodeLabels map[string]string
		levels     []string
		wantNodes  []*nodeInfo
	}{
		"no nodeLabels and levels": {
			wantNodes: []*nodeInfo{
				newNodeInfo(node1),
				newNodeInfo(node2),
				newNodeInfo(node3),
				newNodeInfo(node4),
			},
		},
		"match labels": {
			nodeLabels: map[string]string{"cloud.provider.com/zone": "us-east-1a"},
			wantNodes:  []*nodeInfo{newNodeInfo(node2), newNodeInfo(node3)},
		},
		"match levels": {
			levels:    []string{"cloud.provider.com/topology-block"},
			wantNodes: []*nodeInfo{newNodeInfo(node3)},
		},
		"match labels and levels": {
			nodeLabels: map[string]string{"cloud.provider.com/zone": "us-east-1a"},
			levels:     []string{"cloud.provider.com/topology-block"},
			wantNodes:  []*nodeInfo{newNodeInfo(node3)},
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			gotNodes := nc.find(tc.nodeLabels, tc.levels)
			if diff := cmp.Diff(tc.wantNodes, gotNodes, cmpopts.SortSlices(func(a, b *nodeInfo) bool {
				return a.Name < b.Name
			})); diff != "" {
				t.Errorf("Unexpected nodes (-want,+got):\n%s", diff)
			}
		})
	}
}
