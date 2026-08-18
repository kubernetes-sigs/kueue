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
	"maps"
	"slices"
	"sync"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/equality"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"

	"sigs.k8s.io/kueue/pkg/features"
	utiltas "sigs.k8s.io/kueue/pkg/util/tas"
)

type nodesCache struct {
	lock sync.RWMutex

	// nodes stores stripped Node views that nodesCache treats as immutable. sync
	// replaces an entry rather than mutating it. The views share Labels, Taints,
	// and Allocatable with the input Node, so callers must not mutate those fields
	// after sync.
	nodes map[string]*corev1.Node

	// generation counts changes to scheduling-relevant node data. It increments
	// when a node is added or removed, or when its labels, taints, or allocatable
	// resources change - not on every Node update event.
	generation int64

	// schedulableAndReadyNodes tracks node names that are both schedulable and ready
	schedulableAndReadyNodes sets.Set[string]
}

func newNodesCache() *nodesCache {
	return &nodesCache{
		nodes:                    make(map[string]*corev1.Node),
		schedulableAndReadyNodes: sets.New[string](),
	}
}

func (t *nodesCache) sync(node *corev1.Node) {
	schedulableAndReady := !node.Spec.Unschedulable &&
		utiltas.IsNodeStatusConditionTrue(node.Status.Conditions, corev1.NodeReady)
	t.lock.Lock()
	defer t.lock.Unlock()

	if !features.Enabled(features.SchedulerLibraryIntegration) && !schedulableAndReady {
		t.deleteWithoutLock(node.Name)
		return
	}

	availabilityChanged := t.schedulableAndReadyNodes.Has(node.Name) != schedulableAndReady
	if schedulableAndReady {
		t.schedulableAndReadyNodes.Insert(node.Name)
	} else {
		t.schedulableAndReadyNodes.Delete(node.Name)
	}

	stripped := copyAndStripNode(node)
	existing, found := t.nodes[node.Name]
	nodeChanged := !found || !strippedNodesEqual(existing, stripped)

	if !nodeChanged && !availabilityChanged {
		return
	}

	if nodeChanged {
		t.nodes[node.Name] = stripped
	}
	t.generation++
}

func (t *nodesCache) delete(nodeName string) {
	t.lock.Lock()
	defer t.lock.Unlock()
	t.deleteWithoutLock(nodeName)
}

func (t *nodesCache) deleteWithoutLock(nodeName string) {
	if _, found := t.nodes[nodeName]; found {
		delete(t.nodes, nodeName)
		t.schedulableAndReadyNodes.Delete(nodeName)
		t.generation++
	}
}

// find returns the nodes matching the flavor along with the generation at
// which they were read, so that structures derived from the result can later
// be revalidated against currentGeneration.
func (t *nodesCache) find(nodeLabels map[string]string, levels []string) ([]*corev1.Node, int64) {
	t.lock.RLock()
	defer t.lock.RUnlock()
	filteredNodes := make([]*corev1.Node, 0, len(t.nodes))
	shouldExcludeUnschedulableAndNotReadyNodes :=
		features.Enabled(features.SchedulerLibraryIntegration) &&
			(len(levels) == 0 || !utiltas.IsLowestLevelHostname(levels))

	for _, node := range t.nodes {
		if shouldExcludeUnschedulableAndNotReadyNodes && !t.schedulableAndReadyNodes.Has(node.Name) {
			continue
		}
		if utiltas.NodeMatchesFlavor(node.Labels, nodeLabels, levels) {
			filteredNodes = append(filteredNodes, node)
		}
	}
	return filteredNodes, t.generation
}

func (t *nodesCache) currentGeneration() int64 {
	t.lock.RLock()
	defer t.lock.RUnlock()
	return t.generation
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
			Unschedulable: node.Spec.Unschedulable,
			Taints:        node.Spec.Taints,
		},
		Status: corev1.NodeStatus{
			Allocatable: node.Status.Allocatable,
		},
	}
}

func (t *nodesCache) getAllNodes() []*corev1.Node {
	t.lock.RLock()
	defer t.lock.RUnlock()
	return slices.Collect(maps.Values(t.nodes))
}

// strippedNodesEqual reports whether two stripped nodes carry semantically
// identical scheduling-relevant information. It is used to avoid bumping the
// nodesCache generation for Node updates that do not affect TAS scheduling,
// such as kubelet heartbeats.
func strippedNodesEqual(a, b *corev1.Node) bool {
	return maps.Equal(a.Labels, b.Labels) &&
		a.Spec.Unschedulable == b.Spec.Unschedulable &&
		equality.Semantic.DeepEqual(a.Spec.Taints, b.Spec.Taints) &&
		equality.Semantic.DeepEqual(a.Status.Allocatable, b.Status.Allocatable)
}
