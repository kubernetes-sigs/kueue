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
	"sigs.k8s.io/kueue/pkg/cache/scheduler/simulator"
	"sigs.k8s.io/kueue/pkg/resources"
	utiltas "sigs.k8s.io/kueue/pkg/util/tas"
)

type tasCache struct {
	sync.RWMutex
	client            client.Client
	flavors           map[kueue.ResourceFlavorReference]flavorInformation
	topologies        map[kueue.TopologyReference]topologyInformation
	flavorCache       map[kueue.ResourceFlavorReference]*TASFlavorCache
	resourceFormatter *resources.ResourceFormatter

	nonTasUsageCache    *nonTasUsageCache
	nodesCache          *nodesCache
	schedulingSimulator simulator.SchedulingSimulator
}

func NewTASCache(client client.Client, schedulingSimulator simulator.SchedulingSimulator, resourceFormatter *resources.ResourceFormatter) tasCache {
	return tasCache{
		client:            client,
		flavors:           make(map[kueue.ResourceFlavorReference]flavorInformation),
		topologies:        make(map[kueue.TopologyReference]topologyInformation),
		flavorCache:       make(map[kueue.ResourceFlavorReference]*TASFlavorCache),
		resourceFormatter: resourceFormatter,
		nonTasUsageCache: &nonTasUsageCache{
			podUsage:  make(map[types.NamespacedName]podUsageValue),
			nodeUsage: make(map[string]resources.Requests),
			lock:      sync.RWMutex{},
		},
		nodesCache:          newNodesCache(),
		schedulingSimulator: schedulingSimulator,
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

func (t *tasCache) AddOrUpdateFlavor(flavor *kueue.ResourceFlavor) {
	t.Lock()
	defer t.Unlock()
	name := kueue.ResourceFlavorReference(flavor.Name)
	tolerations := slices.Clone(flavor.Spec.Tolerations)
	nodeLabels := maps.Clone(flavor.Spec.NodeLabels)
	if flavorInfo, ok := t.flavors[name]; ok {
		flavorInfo.Tolerations = tolerations
		flavorInfo.NodeLabels = nodeLabels
		t.flavors[name] = flavorInfo
		if flavorCache, ok := t.flavorCache[name]; ok {
			flavorCache.updateTolerations(tolerations)
			flavorCache.updateNodeLabels(nodeLabels)
		}
		return
	}
	flavorInfo := flavorInformation{
		TopologyName: *flavor.Spec.TopologyName,
		NodeLabels:   nodeLabels,
		Tolerations:  tolerations,
	}
	t.flavors[name] = flavorInfo
	if tInfo, ok := t.topologies[flavorInfo.TopologyName]; ok {
		t.flavorCache[name] = t.NewTASFlavorCache(tInfo, flavorInfo)
	}
}

func (t *tasCache) AddTopology(topology *kueue.Topology) {
	t.Lock()
	defer t.Unlock()
	name := kueue.TopologyReference(topology.Name)
	tInfo := topologyInformation{
		Levels: utiltas.Levels(topology),
	}
	t.topologies[name] = tInfo
	for fName, flavorInfo := range t.flavors {
		if flavorInfo.TopologyName != name {
			continue
		}
		if c, ok := t.flavorCache[fName]; ok {
			// Update the levels in place: rebuilding the cache entry would drop
			// the usage accumulated from admitted workloads.
			c.updateTopology(tInfo)
		} else {
			t.flavorCache[fName] = t.NewTASFlavorCache(tInfo, flavorInfo)
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

// UpdateNonTASUsage updates the non-TAS resource usage cache for the pod.
// Returns the node name when capacity may have been freed on a node.
func (t *tasCache) UpdateNonTASUsage(pod *corev1.Pod, log logr.Logger) string {
	return t.nonTasUsageCache.update(pod, log)
}

// DeleteNonTASUsageByKey removes the pod from the non-TAS resource usage cache.
// Returns the node name when an entry is removed.
func (t *tasCache) DeleteNonTASUsageByKey(key client.ObjectKey, log logr.Logger) string {
	return t.nonTasUsageCache.delete(key, log)
}

// TrackPod notifies the scheduling simulator that a pod is running on a node.
func (t *tasCache) TrackPod(ctx context.Context, pod *corev1.Pod) {
	t.schedulingSimulator.TrackPod(ctx, pod)
}

// UntrackPod notifies the scheduling simulator that a pod has been removed.
func (t *tasCache) UntrackPod(ctx context.Context, key client.ObjectKey) {
	t.schedulingSimulator.UntrackPod(ctx, key)
}

func (t *tasCache) SyncNode(node *corev1.Node) {
	t.nodesCache.sync(node)
}

func (t *tasCache) DeleteNodeByName(nodeName string) {
	t.nodesCache.delete(nodeName)
}
