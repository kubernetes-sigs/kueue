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

	utiltas "sigs.k8s.io/kueue/pkg/util/tas"
)

type nodesCache struct {
	lock  sync.RWMutex
	nodes map[string]*nodeInfo
}

func newNodesCache() *nodesCache {
	return &nodesCache{
		nodes: make(map[string]*nodeInfo),
	}
}

func (t *nodesCache) sync(node *corev1.Node) {
	schedulableAndReady := !node.Spec.Unschedulable &&
		utiltas.IsNodeStatusConditionTrue(node.Status.Conditions, corev1.NodeReady)

	t.lock.Lock()
	defer t.lock.Unlock()

	if schedulableAndReady {
		t.nodes[node.Name] = newNodeInfo(node)
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

func (t *nodesCache) find(nodeLabels map[string]string, levels []string) []*nodeInfo {
	t.lock.RLock()
	defer t.lock.RUnlock()
	filteredNodes := make([]*nodeInfo, 0, len(t.nodes))
	for _, node := range t.nodes {
		if utiltas.NodeMatchesFlavor(node.Labels, nodeLabels, levels) {
			filteredNodes = append(filteredNodes, node)
		}
	}
	return filteredNodes
}
