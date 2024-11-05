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
	"sync"

	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
)

type TASCache struct {
	sync.RWMutex
	client  client.Client
	flavors map[kueue.ResourceFlavorReference]*TASFlavorCache
}

func NewTASCache(client client.Client) TASCache {
	return TASCache{
		client:  client,
		flavors: make(map[kueue.ResourceFlavorReference]*TASFlavorCache),
	}
}

func (t *TASCache) Get(name kueue.ResourceFlavorReference) *TASFlavorCache {
	t.RLock()
	defer t.RUnlock()
	return t.flavors[name]
}

// Clone returns a shallow copy of the map
func (t *TASCache) Clone() map[kueue.ResourceFlavorReference]*TASFlavorCache {
	t.RLock()
	defer t.RUnlock()
	return maps.Clone(t.flavors)
}

func (t *TASCache) Set(name kueue.ResourceFlavorReference, info *TASFlavorCache) {
	t.Lock()
	defer t.Unlock()
	t.flavors[name] = info
}

func (t *TASCache) Delete(name kueue.ResourceFlavorReference) {
	t.Lock()
	defer t.Unlock()
	delete(t.flavors, name)
}
