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

package expectations

import (
	"sync"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
)

type uids = sets.Set[types.UID]

// Store contains UIDs for which we are waiting to observe some change through event handlers.
type Store struct {
	sync.Mutex
	name string

	store map[types.NamespacedName]uids
}

func NewStore(name string) *Store {
	return &Store{
		name:  name,
		store: make(map[types.NamespacedName]uids),
	}
}

func (e *Store) ExpectUIDs(log logr.Logger, key types.NamespacedName, uids []types.UID) {
	log.V(3).Info("Expecting UIDs", "store", e.name, "key", key, "uids", uids)
	expectedUIDs := sets.New[types.UID](uids...)
	e.Lock()
	defer e.Unlock()

	stored, found := e.store[key]
	if !found {
		e.store[key] = expectedUIDs
	} else {
		e.store[key] = stored.Union(expectedUIDs)
	}
}

func (e *Store) ObservedUID(log logr.Logger, key types.NamespacedName, uid types.UID) {
	log.V(3).Info("Observed UID", "store", e.name, "key", key, "uid", uid)
	e.Lock()
	defer e.Unlock()

	stored, found := e.store[key]
	if !found {
		return
	}
	stored.Delete(uid)

	// clean up key if empty.
	if stored.Len() == 0 {
		delete(e.store, key)
	}
}

func (e *Store) Satisfied(log logr.Logger, key types.NamespacedName) bool {
	e.Lock()
	_, found := e.store[key]
	e.Unlock()

	if logV := log.V(4); logV.Enabled() {
		log.V(4).Info("Retrieved satisfied expectations", "store", e.name, "key", key, "satisfied", !found)
	}
	return !found
}
