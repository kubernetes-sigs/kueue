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
	"sync"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	utiltas "sigs.k8s.io/kueue/pkg/util/tas"
)

type nodesCache struct {
	lock  sync.RWMutex
	nodes map[string]*corev1.Node
}

func newNodesCache() *nodesCache {
	return &nodesCache{
		nodes: make(map[string]*corev1.Node),
	}
}

func (t *nodesCache) sync(node *corev1.Node) {
	schedulableAndReady := !node.Spec.Unschedulable &&
		utiltas.IsNodeStatusConditionTrue(node.Status.Conditions, corev1.NodeReady)

	t.lock.Lock()
	defer t.lock.Unlock()

	if schedulableAndReady {
		t.nodes[node.Name] = copyAndStripNode(node)
	} else {
		t.deleteWithoutLock(node.Name)
	}
}

func (t *nodesCache) delete(nodeName string) {
	t.lock.Lock()
	defer t.lock.Unlock()
	t.deleteWithoutLock(nodeName)
}

func (t *nodesCache) deleteWithoutLock(nodeName string) {
	delete(t.nodes, nodeName)
}

func (t *nodesCache) find(nodeLabels map[string]string, levels []string) []*corev1.Node {
	t.lock.RLock()
	defer t.lock.RUnlock()
	filteredNodes := make([]*corev1.Node, 0, len(t.nodes))
	for _, node := range t.nodes {
		if utiltas.NodeMatchesFlavor(node.Labels, nodeLabels, levels) {
			filteredNodes = append(filteredNodes, node)
		}
	}
	return filteredNodes
}

// copyAndStripNode creates a minimal copy of the Node object containing only the
// fields required for TAS scheduling (Name, Labels, Taints, and Allocatable).
// This reduces the memory footprint and, more importantly, minimizes the number
// of pointer fields the garbage collector needs to traverse in a large cluster
// with frequent scheduling activity.
func copyAndStripNode(node *corev1.Node) *corev1.Node {
	return &corev1.Node{
		ObjectMeta: metav1.ObjectMeta{
			Name:   node.Name,
			Labels: node.Labels,
		},
		Spec: corev1.NodeSpec{
			Taints: node.Spec.Taints,
		},
		Status: corev1.NodeStatus{
			Allocatable: node.Status.Allocatable,
		},
	}
}
