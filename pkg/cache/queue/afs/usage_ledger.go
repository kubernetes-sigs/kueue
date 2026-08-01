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

// UsageLedgerEntry holds everything a LocalQueue's fair-sharing usage is computed from:
// the decayed history in Resources, and the entry penalties in PendingPenalty that no
// settlement has consolidated into it yet. The two live in one entry so a single Update
// moves a penalty from one to the other and a single Get observes a consistent pair,
// which is what keeps a reader from seeing the same penalty counted in both.
//
// StatusAccounted records whether the LocalQueue's persisted fair-sharing status has
// been folded into this entry: once history is merged, the entry's existence alone no
// longer tells whether that happened, so it must be tracked explicitly. Every writer
// must carry it, and PendingPenalty, forward (use Update), or history is merged twice
// or a pending penalty is silently dropped.
type UsageLedgerEntry struct {
	Resources       corev1.ResourceList
	PendingPenalty  corev1.ResourceList
	LastUpdate      time.Time
	StatusAccounted bool
}

// AfsUsageLedger is the per-LocalQueue fair-sharing accounting cache.
type AfsUsageLedger struct {
	resources *utilmaps.SyncMap[utilqueue.LocalQueueReference, UsageLedgerEntry]
}

// NewAfsUsageLedger creates an empty ledger.
func NewAfsUsageLedger() *AfsUsageLedger {
	return &AfsUsageLedger{
		resources: utilmaps.NewSyncMap[utilqueue.LocalQueueReference, UsageLedgerEntry](0),
	}
}

// Set unconditionally replaces the entry for a LocalQueue, resetting
// StatusAccounted. Controllers should use Update instead.
func (a *AfsUsageLedger) Set(lqKey utilqueue.LocalQueueReference, resources corev1.ResourceList, lastUpdate time.Time) {
	a.resources.Add(lqKey, UsageLedgerEntry{
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
	return a.resources.Update(lqKey, fn)
}

// Get retrieves a LocalQueue's whole ledger entry, both the consumed history and
// any penalty still pending, as one consistent pair.
func (a *AfsUsageLedger) Get(lqKey utilqueue.LocalQueueReference) (UsageLedgerEntry, bool) {
	return a.resources.Get(lqKey)
}

// Delete drops a LocalQueue's entry, discarding its consumed history and any
// pending penalty together.
func (a *AfsUsageLedger) Delete(lqKey utilqueue.LocalQueueReference) {
	a.resources.Delete(lqKey)
}
