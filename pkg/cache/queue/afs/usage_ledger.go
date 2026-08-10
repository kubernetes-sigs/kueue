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

package afs

import (
	"time"

	corev1 "k8s.io/api/core/v1"

	utilmaps "sigs.k8s.io/kueue/pkg/util/maps"
	utilqueue "sigs.k8s.io/kueue/pkg/util/queue"
)

// UsageLedgerEntry holds everything a LocalQueue's fair-sharing usage is computed
// from, as one consistent unit.
//
// Update closures should mutate the copy they receive rather than build a fresh
// literal, so fields they do not touch are carried forward by construction.
type UsageLedgerEntry struct {
	// Resources is the decayed consumed history.
	Resources corev1.ResourceList
	// PendingPenalty is the aggregate of the entry penalties not yet settled
	// into Resources.
	PendingPenalty corev1.ResourceList
	// LastUpdate anchors the decay clock: when Resources was last decayed,
	// or when the entry was created.
	LastUpdate time.Time
	// StatusAccounted records whether the LocalQueue's persisted fair-sharing
	// status has been folded into this entry; once history is merged, the
	// entry's existence alone cannot tell.
	StatusAccounted bool
}

// AfsUsageLedger is the per-LocalQueue fair-sharing accounting cache.
//
// Concurrency contract: entries are values guarded by the map's RWMutex. Get
// returns a snapshot of the whole entry, and every mutation is a whole-entry
// rewrite under the write lock via Update, so readers always observe the
// consumed history and the pending penalties as one consistent pair.
type AfsUsageLedger struct {
	entries *utilmaps.SyncMap[utilqueue.LocalQueueReference, UsageLedgerEntry]
}

// NewAfsUsageLedger creates an empty ledger.
func NewAfsUsageLedger() *AfsUsageLedger {
	return &AfsUsageLedger{
		entries: utilmaps.NewSyncMap[utilqueue.LocalQueueReference, UsageLedgerEntry](0),
	}
}

// SetForTest unconditionally replaces the entry for a LocalQueue, resetting
// StatusAccounted and discarding any pending penalty. It exists to seed consumed
// history in tests; production writers must use Update.
func (a *AfsUsageLedger) SetForTest(lqKey utilqueue.LocalQueueReference, resources corev1.ResourceList, lastUpdate time.Time) {
	a.entries.Add(lqKey, UsageLedgerEntry{
		Resources:  resources,
		LastUpdate: lastUpdate,
	})
}

// Update atomically rewrites the entry for a LocalQueue with the result of fn.
// fn receives the current entry (zero-valued if absent) and whether it was
// present. It runs under the map's write lock, so it must be pure computation:
// it must not call back into the scheduler cache (lock ordering).
// All controller writers should go through Update so concurrent read-modify-writes
// cannot overwrite each other and StatusAccounted is preserved by construction.
func (a *AfsUsageLedger) Update(lqKey utilqueue.LocalQueueReference, fn func(entry UsageLedgerEntry, found bool) UsageLedgerEntry) UsageLedgerEntry {
	return a.entries.Update(lqKey, fn)
}

// Get retrieves a LocalQueue's whole ledger entry, both the consumed history and
// any penalty still pending, as one consistent pair.
func (a *AfsUsageLedger) Get(lqKey utilqueue.LocalQueueReference) (UsageLedgerEntry, bool) {
	return a.entries.Get(lqKey)
}

// Delete drops a LocalQueue's entry, discarding its consumed history and any
// pending penalty together.
func (a *AfsUsageLedger) Delete(lqKey utilqueue.LocalQueueReference) {
	a.entries.Delete(lqKey)
}
