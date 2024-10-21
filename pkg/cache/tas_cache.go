/*
Copyright 2024 The Kubernetes Authors.

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

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/resources"
	utiltas "sigs.k8s.io/kueue/pkg/util/tas"
)

type TASCache struct {
	sync.RWMutex
	client  client.Client
	flvsMap map[kueue.ResourceFlavorReference]*TASFlavorCache
}

func NewTASCache(client client.Client) TASCache {
	return TASCache{
		client:  client,
		flvsMap: make(map[kueue.ResourceFlavorReference]*TASFlavorCache),
	}
}

func (t *TASCache) NewFlavorCache(labels []string, nodeLabels map[string]string) *TASFlavorCache {
	return &TASFlavorCache{
		client:     t.client,
		Levels:     slices.Clone(labels),
		NodeLabels: maps.Clone(nodeLabels),
		usageMap:   make(map[utiltas.TopologyDomainID]resources.Requests),
	}
}

func (t *TASCache) Get(name kueue.ResourceFlavorReference) *TASFlavorCache {
	t.RLock()
	defer t.RUnlock()
	return t.flvsMap[name]
}

// Clone returns a shallow copy of the map
func (t *TASCache) Clone() map[kueue.ResourceFlavorReference]*TASFlavorCache {
	t.RLock()
	defer t.RUnlock()
	return maps.Clone(t.flvsMap)
}

func (t *TASCache) Set(name kueue.ResourceFlavorReference, info *TASFlavorCache) {
	t.Lock()
	defer t.Unlock()
	t.flvsMap[name] = info
}

func (t *TASCache) Delete(name kueue.ResourceFlavorReference) {
	t.Lock()
	defer t.Unlock()
	delete(t.flvsMap, name)
}
