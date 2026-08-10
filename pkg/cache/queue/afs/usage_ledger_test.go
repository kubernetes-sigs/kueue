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
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	utilqueue "sigs.k8s.io/kueue/pkg/util/queue"
)

func TestUpdate(t *testing.T) {
	lqKey := utilqueue.NewLocalQueueReference("default", "lq")
	settleTime := time.Now()
	seedTime := settleTime.Add(time.Millisecond)

	seeded := corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("8")}
	settled := corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("2")}

	seedFn := func(entry UsageLedgerEntry, found bool) UsageLedgerEntry {
		if !found {
			return UsageLedgerEntry{Resources: seeded, LastUpdate: seedTime, StatusAccounted: true}
		}
		return entry
	}
	writerFn := func(entry UsageLedgerEntry, found bool) UsageLedgerEntry {
		return UsageLedgerEntry{Resources: settled, LastUpdate: settleTime, StatusAccounted: entry.StatusAccounted}
	}

	cases := map[string]struct {
		existing  *UsageLedgerEntry
		fn        func(UsageLedgerEntry, bool) UsageLedgerEntry
		wantEntry UsageLedgerEntry
	}{
		"passes found=false and a zero entry when absent, stores the result": {
			fn:        seedFn,
			wantEntry: UsageLedgerEntry{Resources: seeded, LastUpdate: seedTime, StatusAccounted: true},
		},
		"passes the current entry when present": {
			existing:  &UsageLedgerEntry{Resources: settled, LastUpdate: settleTime},
			fn:        seedFn,
			wantEntry: UsageLedgerEntry{Resources: settled, LastUpdate: settleTime},
		},
		"a writer-style update preserves StatusAccounted": {
			existing:  &UsageLedgerEntry{Resources: seeded, LastUpdate: seedTime, StatusAccounted: true},
			fn:        writerFn,
			wantEntry: UsageLedgerEntry{Resources: settled, LastUpdate: settleTime, StatusAccounted: true},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ledger := NewAfsUsageLedger()
			if tc.existing != nil {
				ledger.Update(lqKey, func(UsageLedgerEntry, bool) UsageLedgerEntry { return *tc.existing })
			}

			got := ledger.Update(lqKey, tc.fn)

			if diff := cmp.Diff(tc.wantEntry, got, cmp.AllowUnexported(UsageLedgerEntry{})); diff != "" {
				t.Errorf("Update() returned entry (-want,+got):\n%s", diff)
			}
			stored, found := ledger.Get(lqKey)
			if !found {
				t.Fatal("Get() found no entry after Update()")
			}
			if diff := cmp.Diff(tc.wantEntry, stored, cmp.AllowUnexported(UsageLedgerEntry{})); diff != "" {
				t.Errorf("stored entry (-want,+got):\n%s", diff)
			}
		})
	}
}

// The aggregate must equal the sum of the records after any helper sequence, or
// readers would count penalties no record backs.
func TestEntryPenaltyAggregateMatchesRecords(t *testing.T) {
	entry := UsageLedgerEntry{}
	entry = entry.withPenalty("ns/wl1", corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("2")})
	entry = entry.withPenalty("ns/wl2", corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("3")})
	entry = entry.withPenalty("ns/wl1", corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("4")})
	entry, removed := entry.WithoutPenalty("ns/wl2")

	if got := removed[corev1.ResourceCPU]; got.MilliValue() != 3_000 {
		t.Errorf("WithoutPenalty() removed %dm CPU, want 3000m", got.MilliValue())
	}
	sum := corev1.ResourceList{}
	for _, p := range entry.penaltyRecords {
		for k, v := range p {
			q := sum[k]
			q.Add(v)
			sum[k] = q
		}
	}
	gotAggregate := entry.PendingPenalty()[corev1.ResourceCPU]
	gotSum := sum[corev1.ResourceCPU]
	if gotAggregate.MilliValue() != gotSum.MilliValue() {
		t.Errorf("aggregate %dm != sum of records %dm", gotAggregate.MilliValue(), gotSum.MilliValue())
	}
	if gotAggregate.MilliValue() != 4_000 {
		t.Errorf("aggregate CPU = %dm, want 4000m (wl1 replaced to 4, wl2 removed)", gotAggregate.MilliValue())
	}
}

// Entries are stored by value but share their maps with readers that fetched them via
// Get, so the entry helpers must copy rather than mutate in place.
func TestEntryPenaltyHelpersDoNotMutateSharedMaps(t *testing.T) {
	lqKey := utilqueue.NewLocalQueueReference("default", "lq")
	ledger := NewAfsUsageLedger()
	ledger.PushPenalty(lqKey, "ns/wl1", corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("2")}, time.Now())

	snapshot, found := ledger.Get(lqKey)
	if !found {
		t.Fatal("expected the push to create an entry")
	}

	ledger.PushPenalty(lqKey, "ns/wl2", corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("3")}, time.Now())
	ledger.SubPenalty(lqKey, "ns/wl1")

	if got := snapshot.PendingPenalty()[corev1.ResourceCPU]; got.MilliValue() != 2_000 {
		t.Errorf("a later write mutated a fetched aggregate: got %dm, want 2000m", got.MilliValue())
	}
	if !snapshot.HasPenaltyRecord("ns/wl1") {
		t.Error("a later write deleted a record from a fetched entry's map")
	}
	if snapshot.HasPenaltyRecord("ns/wl2") {
		t.Error("a later write inserted a record into a fetched entry's map")
	}
}
