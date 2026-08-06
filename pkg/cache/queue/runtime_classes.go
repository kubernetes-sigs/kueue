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

package queue

import (
	"maps"
	"sync"

	nodev1 "k8s.io/api/node/v1"
)

type RuntimeClasses struct {
	sync.RWMutex
	store map[string]*nodev1.RuntimeClass
}

func newRuntimeClasses() *RuntimeClasses {
	return &RuntimeClasses{
		store: make(map[string]*nodev1.RuntimeClass),
	}
}

func (r *RuntimeClasses) Add(rc *nodev1.RuntimeClass) {
	r.Lock()
	defer r.Unlock()
	r.store[rc.Name] = rc
}

func (r *RuntimeClasses) Update(oldRc, newRc *nodev1.RuntimeClass) {
	r.Add(newRc)
}

func (r *RuntimeClasses) Delete(rc *nodev1.RuntimeClass) {
	r.Lock()
	defer r.Unlock()
	delete(r.store, rc.Name)
}

func (r *RuntimeClasses) Get(name string) *nodev1.RuntimeClass {
	r.RLock()
	defer r.RUnlock()
	return r.store[name]
}

func (r *RuntimeClasses) GetAll() map[string]*nodev1.RuntimeClass {
	r.RLock()
	defer r.RUnlock()
	res := make(map[string]*nodev1.RuntimeClass, len(r.store))
	maps.Copy(res, r.store)
	return res
}
