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
	"maps"
	"time"

	corev1 "k8s.io/api/core/v1"

	utilmaps "sigs.k8s.io/kueue/pkg/util/maps"
	utilqueue "sigs.k8s.io/kueue/pkg/util/queue"
	"sigs.k8s.io/kueue/pkg/util/resource"
)

// WorkloadReference identifies the Workload a pending penalty was pushed for.
// It mirrors workload.Reference, which this package cannot import because
// pkg/workload already imports it.
type WorkloadReference string

// UsageLedgerEntry holds everything a LocalQueue's fair-sharing usage is computed
// from, as one consistent unit.
//
// Update closures should mutate the copy they receive rather than build a fresh
// literal, so fields they do not touch are carried forward by construction.
type UsageLedgerEntry struct {
	// Resources is the decayed consumed history.
	Resources corev1.ResourceList
	// pendingPenalty is the aggregate of the entry penalties not yet settled
	// into Resources, kept equal to the sum of penaltyRecords by withPenalty
	// and WithoutPenalty — the only operations that touch the pair.
	pendingPenalty corev1.ResourceList
	// penaltyRecords is the pending penalty per Workload. Recording per
	// Workload makes the operations idempotent: re-pushing replaces the
	// Workload's record, and rollback or deletion subtracts exactly what was
	// recorded.
	penaltyRecords map[WorkloadReference]corev1.ResourceList
	// LastUpdate anchors the decay clock: when Resources was last decayed,
	// or when the entry was created.
	LastUpdate time.Time
	// StatusAccounted records whether the LocalQueue's persisted fair-sharing
	// status has been folded into this entry; once history is merged, the
	// entry's existence alone cannot tell.
	StatusAccounted bool
}

// PendingPenalty returns the aggregate penalty awaiting settlement. The returned
// map is shared with the entry and must be treated as read-only.
func (e UsageLedgerEntry) PendingPenalty() corev1.ResourceList {
	return e.pendingPenalty
}

// HasPenaltyRecord reports whether a penalty is recorded for the Workload.
func (e UsageLedgerEntry) HasPenaltyRecord(wlKey WorkloadReference) bool {
	_, found := e.penaltyRecords[wlKey]
	return found
}

// withPenalty sets the Workload's pending penalty, replacing any previous record,
// and keeps the aggregate in sync. It copies rather than mutates the maps, which
// are shared with readers that fetched the entry via Get.
func (e UsageLedgerEntry) withPenalty(wlKey WorkloadReference, penalty corev1.ResourceList) UsageLedgerEntry {
	e, _ = e.WithoutPenalty(wlKey)
	records := maps.Clone(e.penaltyRecords)
	if records == nil {
		records = make(map[WorkloadReference]corev1.ResourceList, 1)
	}
	records[wlKey] = penalty.DeepCopy()
	e.penaltyRecords = records
	e.pendingPenalty = resource.MergeResourceListKeepSum(e.pendingPenalty, penalty)
	return e
}

// WithoutPenalty removes the Workload's record and subtracts it from the
// aggregate, returning the removed amount (nil if none was recorded). The
// aggregate only ever accumulates recorded amounts, so the subtraction is exact
// and needs no clamping. Like withPenalty, it copies the shared maps.
func (e UsageLedgerEntry) WithoutPenalty(wlKey WorkloadReference) (UsageLedgerEntry, corev1.ResourceList) {
	recorded, found := e.penaltyRecords[wlKey]
	if !found {
		return e, nil
	}
	records := maps.Clone(e.penaltyRecords)
	delete(records, wlKey)
	e.penaltyRecords = records

	negated := make(corev1.ResourceList, len(recorded))
	for k, v := range recorded {
		q := v.DeepCopy()
		q.Neg()
		negated[k] = q
	}
	aggregate := resource.MergeResourceListKeepSum(e.pendingPenalty, negated)
	// Drop the explicit zeros the subtraction leaves behind.
	for k, v := range aggregate {
		if v.IsZero() {
			delete(aggregate, k)
		}
	}
	e.pendingPenalty = aggregate
	return e, recorded
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
// StatusAccounted and discarding any pending penalty records. It exists to seed
// consumed history in tests; production writers must use Update.
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
