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

package expectations

import (
	"sync"
	"time"

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
	expectedUIDs := sets.New(uids...)
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

// CreationStore tracks creation expectations for objects that may not yet be
// observable in the informer cache.  After a controller successfully creates an
// object it calls ExpectCreation; once the object appears in a watch event the
// controller calls CreationObserved.  Satisfied returns true when no
// outstanding creation expectation exists for the given key.
//
// Unlike Store (which tracks UIDs), CreationStore is keyed only by
// NamespacedName because the UID is not known until after creation.
//
// Entries carry a timestamp so that a missed watch event cannot block creation
// indefinitely; call ResetExpired before checking Satisfied to evict stale
// expectations.
type CreationStore struct {
	sync.Mutex
	name  string
	ttl   time.Duration
	store map[types.NamespacedName]time.Time
}

// NewCreationStore creates a CreationStore with the given name and TTL.
// A zero TTL means entries never expire (caller must ensure observations always
// fire or manage expiry externally).
func NewCreationStore(name string, ttl time.Duration) *CreationStore {
	return &CreationStore{
		name:  name,
		ttl:   ttl,
		store: make(map[types.NamespacedName]time.Time),
	}
}

// ExpectCreation records that a create was issued for key.
func (e *CreationStore) ExpectCreation(log logr.Logger, key types.NamespacedName) {
	log.V(3).Info("Expecting creation", "store", e.name, "key", key)
	e.Lock()
	defer e.Unlock()
	e.store[key] = time.Now()
}

// CreationObserved clears the expectation for key after the object has been
// observed via a watch event.
func (e *CreationStore) CreationObserved(log logr.Logger, key types.NamespacedName) {
	log.V(3).Info("Observed creation", "store", e.name, "key", key)
	e.Lock()
	defer e.Unlock()
	delete(e.store, key)
}

// ResetExpired removes entries whose timestamp is older than the TTL, returning
// the number of entries removed.  Call this at the start of each reconcile or
// on a periodic timer so that a missed watch event cannot block creation
// indefinitely.
func (e *CreationStore) ResetExpired(log logr.Logger) int {
	if e.ttl <= 0 {
		return 0
	}
	e.Lock()
	defer e.Unlock()
	now := time.Now()
	expired := 0
	for key, ts := range e.store {
		if now.Sub(ts) > e.ttl {
			delete(e.store, key)
			expired++
		}
	}
	if expired > 0 {
		log.V(3).Info("Reset expired creation expectations", "store", e.name, "count", expired)
	}
	return expired
}

// Satisfied returns true if no outstanding creation expectation exists for key
// (or if it existed but has expired and been cleared by ResetExpired).
func (e *CreationStore) Satisfied(log logr.Logger, key types.NamespacedName) bool {
	e.Lock()
	_, found := e.store[key]
	e.Unlock()
	if logV := log.V(4); logV.Enabled() {
		log.V(4).Info("Retrieved satisfied creation expectations", "store", e.name, "key", key, "satisfied", !found)
	}
	return !found
}
