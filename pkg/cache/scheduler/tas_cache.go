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
	"maps"
	"slices"
	"sync"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	utiltas "sigs.k8s.io/kueue/pkg/util/tas"
)

type tasCache struct {
	sync.RWMutex
	client      client.Client
	flavors     map[kueue.ResourceFlavorReference]flavorInformation
	topologies  map[kueue.TopologyReference]topologyInformation
	flavorCache map[kueue.ResourceFlavorReference]*TASFlavorCache

	nonTasUsageCache *nonTasUsageCache
}

func NewTASCache(client client.Client) tasCache {
	return tasCache{
		client:      client,
		flavors:     make(map[kueue.ResourceFlavorReference]flavorInformation),
		topologies:  make(map[kueue.TopologyReference]topologyInformation),
		flavorCache: make(map[kueue.ResourceFlavorReference]*TASFlavorCache),
		nonTasUsageCache: &nonTasUsageCache{
			podUsage: make(map[types.NamespacedName]podUsageValue),
			lock:     sync.RWMutex{},
		},
	}
}

func (t *tasCache) Get(name kueue.ResourceFlavorReference) *TASFlavorCache {
	t.RLock()
	defer t.RUnlock()
	return t.flavorCache[name]
}

// Clone returns a shallow copy of the map
func (t *tasCache) Clone() map[kueue.ResourceFlavorReference]*TASFlavorCache {
	t.RLock()
	defer t.RUnlock()
	return maps.Clone(t.flavorCache)
}

func (t *tasCache) AddFlavor(flavor *kueue.ResourceFlavor) {
	t.Lock()
	defer t.Unlock()
	name := kueue.ResourceFlavorReference(flavor.Name)
	if _, ok := t.flavors[name]; !ok {
		flavorInfo := flavorInformation{
			TopologyName: *flavor.Spec.TopologyName,
			NodeLabels:   maps.Clone(flavor.Spec.NodeLabels),
			Tolerations:  slices.Clone(flavor.Spec.Tolerations),
		}
		t.flavors[name] = flavorInfo
		if tInfo, ok := t.topologies[flavorInfo.TopologyName]; ok {
			t.flavorCache[name] = t.NewTASFlavorCache(tInfo, flavorInfo)
		}
	}
}

func (t *tasCache) AddTopology(topology *kueue.Topology) {
	t.Lock()
	defer t.Unlock()
	name := kueue.TopologyReference(topology.Name)
	if _, ok := t.topologies[name]; !ok {
		tInfo := topologyInformation{
			Levels: utiltas.Levels(topology),
		}
		t.topologies[name] = tInfo
		for fName, flavorInfo := range t.flavors {
			if flavorInfo.TopologyName == name {
				t.flavorCache[fName] = t.NewTASFlavorCache(tInfo, flavorInfo)
			}
		}
	}
}

func (t *tasCache) DeleteFlavor(name kueue.ResourceFlavorReference) {
	t.Lock()
	defer t.Unlock()
	delete(t.flavors, name)
	delete(t.flavorCache, name)
}

func (t *tasCache) DeleteTopology(name kueue.TopologyReference) {
	t.Lock()
	defer t.Unlock()
	delete(t.topologies, name)
	for flavor, c := range t.flavorCache {
		if c.flavor.TopologyName == name {
			delete(t.flavorCache, flavor)
		}
	}
}

// Update adds a pod to the cache or removes it if terminated.
// Returns true and the node name only on first-time removal of a terminated pod.
func (t *tasCache) Update(pod *corev1.Pod, log logr.Logger) (removed bool, nodeName string) {
	return t.nonTasUsageCache.update(pod, log)
}

// DeletePodByKey removes a pod from the cache and returns the node it was on.
func (t *tasCache) DeletePodByKey(key client.ObjectKey) string {
	return t.nonTasUsageCache.delete(key)
}

// FlavorsForNodes returns TAS flavors matching any of the given nodes.
// Falls back to all active flavors if nodes can't be fetched.
func (t *tasCache) FlavorsForNodes(ctx context.Context, nodeNames []string) []kueue.ResourceFlavorReference {
	if len(nodeNames) == 0 {
		return nil
	}

	nodeLabelsMap := make(map[string]map[string]string, len(nodeNames))
	for _, nodeName := range nodeNames {
		var node corev1.Node
		if err := t.client.Get(ctx, types.NamespacedName{Name: nodeName}, &node); err != nil {
			continue
		}
		nodeLabelsMap[nodeName] = node.Labels
	}

	t.RLock()
	defer t.RUnlock()

	if len(t.flavorCache) == 0 {
		return nil
	}

	if len(nodeLabelsMap) == 0 {
		result := make([]kueue.ResourceFlavorReference, 0, len(t.flavorCache))
		for name := range t.flavorCache {
			result = append(result, name)
		}
		return result
	}

	var result []kueue.ResourceFlavorReference
	for name, flavorCache := range t.flavorCache {
		for _, nodeLabels := range nodeLabelsMap {
			if flavorMatchesNode(flavorCache.flavor.NodeLabels, nodeLabels) {
				result = append(result, name)
				break
			}
		}
	}
	return result
}

// flavorMatchesNode returns true if the node has all labels from the flavor.
func flavorMatchesNode(flavorNodeLabels, nodeLabels map[string]string) bool {
	for k, v := range flavorNodeLabels {
		if nodeLabels[k] != v {
			return false
		}
	}
	return true
}
