// Copyright The Kubernetes Authors.
//
// Licensed under the Apache License, Version 2.0 (the "License");
// you may not use this file except in compliance with the License.
// You may obtain a copy of the License at
//
//     http://www.apache.org/licenses/LICENSE-2.0
//
// Unless required by applicable law or agreed to in writing, software
// distributed under the License is distributed on an "AS IS" BASIS,
// WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
// See the License for the specific language governing permissions and
// limitations under the License.

package afs

import (
	"testing"
	"time"

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	utilqueue "sigs.k8s.io/kueue/pkg/util/queue"
)

type penaltyOp struct {
	wl   WorkloadReference
	push corev1.ResourceList // nil means SubPenalty
}

func TestPenaltyBookkeepingIsIdempotentAndExact(t *testing.T) {
	lqKey := utilqueue.NewLocalQueueReference("ns", "lq")
	now := time.Now()

	cases := map[string]struct {
		ops     []penaltyOp
		wantHas bool
		// wantMilli is asserted per resource name on the aggregate.
		wantMilli map[corev1.ResourceName]int64
	}{
		"pushes for distinct workloads accumulate": {
			ops: []penaltyOp{
				{wl: "ns/wl1", push: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("2")}},
				{wl: "ns/wl2", push: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("3")}},
			},
			wantHas:   true,
			wantMilli: map[corev1.ResourceName]int64{corev1.ResourceCPU: 5_000},
		},
		"re-pushing the same workload replaces instead of stacking": {
			ops: []penaltyOp{
				{wl: "ns/wl1", push: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("2")}},
				{wl: "ns/wl1", push: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("2")}},
			},
			wantHas:   true,
			wantMilli: map[corev1.ResourceName]int64{corev1.ResourceCPU: 2_000},
		},
		"re-pushing with a different amount keeps the latest": {
			ops: []penaltyOp{
				{wl: "ns/wl1", push: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("2")}},
				{wl: "ns/wl1", push: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("3")}},
			},
			wantHas:   true,
			wantMilli: map[corev1.ResourceName]int64{corev1.ResourceCPU: 3_000},
		},
		"rollback after replacement subtracts the replacement amount": {
			ops: []penaltyOp{
				{wl: "ns/wl1", push: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("2")}},
				{wl: "ns/wl1", push: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("3")}},
				{wl: "ns/wl1"},
			},
			wantHas: false,
		},
		"subtracting a workload removes exactly its record": {
			ops: []penaltyOp{
				{wl: "ns/wl1", push: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("2")}},
				{wl: "ns/wl2", push: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("3")}},
				{wl: "ns/wl1"},
			},
			wantHas:   true,
			wantMilli: map[corev1.ResourceName]int64{corev1.ResourceCPU: 3_000},
		},
		"subtracting the only workload leaves nothing pending": {
			ops: []penaltyOp{
				{wl: "ns/wl1", push: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("2")}},
				{wl: "ns/wl1"},
			},
			wantHas: false,
		},
		"subtracting twice is a no-op the second time": {
			ops: []penaltyOp{
				{wl: "ns/wl1", push: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("2")}},
				{wl: "ns/wl2", push: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("3")}},
				{wl: "ns/wl1"},
				{wl: "ns/wl1"},
			},
			wantHas:   true,
			wantMilli: map[corev1.ResourceName]int64{corev1.ResourceCPU: 3_000},
		},
		"subtracting a workload that never pushed leaves no negative residue": {
			ops: []penaltyOp{
				{wl: "ns/wl1"},
			},
			wantHas: false,
		},
		"a workload's subtraction cannot drain another workload's resources": {
			ops: []penaltyOp{
				{wl: "ns/wl1", push: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("2")}},
				{wl: "ns/wl2", push: corev1.ResourceList{corev1.ResourceMemory: resource.MustParse("4Gi")}},
				{wl: "ns/wl1"},
			},
			wantHas:   true,
			wantMilli: map[corev1.ResourceName]int64{corev1.ResourceCPU: 0, corev1.ResourceMemory: 4 * 1024 * 1024 * 1024 * 1000},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ledger := NewAfsUsageLedger()
			for _, op := range tc.ops {
				if op.push != nil {
					ledger.PushPenalty(lqKey, op.wl, op.push, now)
				} else {
					ledger.SubPenalty(lqKey, op.wl)
				}
			}

			if got := ledger.HasPendingPenalty(lqKey); got != tc.wantHas {
				t.Fatalf("HasPendingPenalty() = %t, want %t (peek: %v)", got, tc.wantHas, ledger.PeekPenalty(lqKey))
			}
			for resName, wantMilli := range tc.wantMilli {
				got := ledger.PeekPenalty(lqKey)[resName]
				if got.MilliValue() != wantMilli {
					t.Errorf("unexpected %s penalty: want %dm, got %dm", resName, wantMilli, got.MilliValue())
				}
			}
		})
	}
}

// A push may be the first writer for a LocalQueue. It must stamp LastUpdate, or the
// LocalQueue reconciler would merge the persisted status onto a zero timestamp and the
// first decay would elapse from the zero time and wash the history away.
func TestPushPenaltySeedsLastUpdateOnlyWhenCreating(t *testing.T) {
	lqKey := utilqueue.NewLocalQueueReference("ns", "lq")
	created := time.Now()

	ledger := NewAfsUsageLedger()
	ledger.PushPenalty(lqKey, "ns/wl1", corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1")}, created)

	entry, found := ledger.Get(lqKey)
	if !found {
		t.Fatal("expected the push to create an entry")
	}
	if !entry.LastUpdate.Equal(created) {
		t.Errorf("LastUpdate = %v, want %v", entry.LastUpdate, created)
	}
	if entry.StatusAccounted {
		t.Error("expected StatusAccounted to stay false so the persisted status is still merged")
	}

	ledger.PushPenalty(lqKey, "ns/wl2", corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1")}, created.Add(time.Hour))
	entry, _ = ledger.Get(lqKey)
	if !entry.LastUpdate.Equal(created) {
		t.Errorf("a later push moved LastUpdate to %v, want it left at %v", entry.LastUpdate, created)
	}
	if got := entry.PendingPenalty()[corev1.ResourceCPU]; got.MilliValue() != 2_000 {
		t.Errorf("pending CPU = %dm, want 2000m", got.MilliValue())
	}
}

// A subtraction racing the LocalQueue's deletion must not materialize an entry:
// the LocalQueue's cleanup already ran, so it would leak until restart.
func TestSubPenaltyDoesNotCreateEntry(t *testing.T) {
	lqKey := utilqueue.NewLocalQueueReference("ns", "lq")

	ledger := NewAfsUsageLedger()
	if removed := ledger.SubPenalty(lqKey, "ns/wl1"); removed != nil {
		t.Errorf("SubPenalty() on an absent LocalQueue removed %v, want nil", removed)
	}
	if _, found := ledger.Get(lqKey); found {
		t.Error("SubPenalty materialized a ledger entry for a LocalQueue that had none")
	}

	// Same for a present entry: subtracting an unknown workload must not add or
	// remove anything.
	now := time.Now()
	ledger.PushPenalty(lqKey, "ns/other", corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("2")}, now)
	ledger.SubPenalty(lqKey, "ns/wl1")
	if got := ledger.PeekPenalty(lqKey)[corev1.ResourceCPU]; got.MilliValue() != 2_000 {
		t.Errorf("pending CPU = %dm, want 2000m", got.MilliValue())
	}
}
