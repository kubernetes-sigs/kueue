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

package cache

import (
	"maps"
	"slices"
	"sync"

	"sigs.k8s.io/controller-runtime/pkg/client"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	utiltas "sigs.k8s.io/kueue/pkg/util/tas"
)

type tasCache struct {
	sync.RWMutex
	client      client.Client
	flavors     map[kueue.ResourceFlavorReference]flavorInformation
	topologies  map[kueue.TopologyReference]topologyInformation
	flavorCache map[kueue.ResourceFlavorReference]*TASFlavorCache
}

func NewTASCache(client client.Client) tasCache {
	return tasCache{
		client:      client,
		flavors:     make(map[kueue.ResourceFlavorReference]flavorInformation),
		topologies:  make(map[kueue.TopologyReference]topologyInformation),
		flavorCache: make(map[kueue.ResourceFlavorReference]*TASFlavorCache),
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

func (t *tasCache) AddTopology(topology *kueuealpha.Topology) {
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
