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

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/metrics"
	"sigs.k8s.io/kueue/pkg/resources"
	utilpod "sigs.k8s.io/kueue/pkg/util/pod"
	utiltas "sigs.k8s.io/kueue/pkg/util/tas"
)

type tasCache struct {
	sync.RWMutex
	client      client.Client
	flavors     map[kueue.ResourceFlavorReference]flavorInformation
	topologies  map[kueue.TopologyReference]topologyInformation
	flavorCache map[kueue.ResourceFlavorReference]*TASFlavorCache

	nonTasUsageCache *nonTasUsageCache
	nodesCache       *nodesCache
}

func NewTASCache(client client.Client) tasCache {
	return tasCache{
		client:      client,
		flavors:     make(map[kueue.ResourceFlavorReference]flavorInformation),
		topologies:  make(map[kueue.TopologyReference]topologyInformation),
		flavorCache: make(map[kueue.ResourceFlavorReference]*TASFlavorCache),
		nonTasUsageCache: &nonTasUsageCache{
			podUsage:  make(map[types.NamespacedName]podUsageValue),
			nodeUsage: make(map[string]resources.Requests),
			lock:      sync.RWMutex{},
		},
		nodesCache: newNodesCache(),
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
	t.deleteFlavorFromCache(name)
}

func (t *tasCache) DeleteTopology(name kueue.TopologyReference) {
	t.Lock()
	defer t.Unlock()
	delete(t.topologies, name)
	for flavor, c := range t.flavorCache {
		if c.flavor.TopologyName == name {
			t.deleteFlavorFromCache(flavor)
		}
	}
}

func (t *tasCache) deleteFlavorFromCache(name kueue.ResourceFlavorReference) {
	delete(t.flavorCache, name)
	if features.Enabled(features.TASNodeMetrics) {
		metrics.ClearTASFlavorUsage(name)
	}
}

// emitNonTASUsageByCoords updates kueue_tas_domain_usage using pre-computed metric
// coordinates stored at pod-add time. Using stored coords avoids a nodesCache lookup,
// so the subtract is correct even when the node has since left nodesCache.
func (t *tasCache) emitNonTASUsageByCoords(coords []podMetricCoord, usage resources.Requests, op usageOp) {
	if len(coords) == 0 {
		return
	}
	t.RLock()
	defer t.RUnlock()
	for _, coord := range coords {
		if _, exists := t.flavorCache[coord.flavorName]; !exists {
			// Flavor was deleted; ClearTASFlavorUsage already zeroed its metrics.
			continue
		}
		for resource, qty := range usage {
			if op == add {
				metrics.AddTASDomainUsage(coord.flavorName, coord.levels, coord.values, resource, qty)
			} else {
				metrics.SubTASDomainUsage(coord.flavorName, coord.levels, coord.values, resource, qty)
			}
		}
	}
}

// Update may add a pod to the cache, or delete a terminated pod.
func (t *tasCache) Update(pod *corev1.Pod, log logr.Logger) {
	var coords []podMetricCoord
	if features.Enabled(features.TASNodeMetrics) && !utilpod.IsTerminated(pod) {
		t.RLock()
		if node := t.nodesCache.findByHostname(pod.Spec.NodeName); node != nil {
			for flavorName, fc := range t.flavorCache {
				if !utiltas.NodeMatchesFlavor(node.Labels, fc.flavor.NodeLabels, fc.topology.Levels) {
					continue
				}
				coords = append(coords, podMetricCoord{
					flavorName: flavorName,
					levels:     fc.topology.Levels,
					values:     utiltas.LevelValues(fc.topology.Levels, node.Labels),
				})
			}
		}
		t.RUnlock()
	}

	removed, added := t.nonTasUsageCache.update(pod, coords, log)
	if features.Enabled(features.TASNodeMetrics) {
		if removed != nil {
			t.emitNonTASUsageByCoords(removed.coords, removed.usage, subtract)
		}
		if added != nil {
			t.emitNonTASUsageByCoords(added.coords, added.usage, add)
		}
	}
}

func (t *tasCache) DeletePodByKey(key client.ObjectKey) {
	removed := t.nonTasUsageCache.delete(key)
	if removed == nil {
		return
	}
	if features.Enabled(features.TASNodeMetrics) {
		t.emitNonTASUsageByCoords(removed.coords, removed.usage, subtract)
	}
}

func (t *tasCache) SyncNode(node *corev1.Node) {
	t.nodesCache.sync(node)
}

func (t *tasCache) DeleteNodeByName(nodeName string) {
	t.nodesCache.delete(nodeName)
}
