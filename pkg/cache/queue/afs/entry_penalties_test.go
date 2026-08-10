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

func TestSubPenaltyClampsAtZero(t *testing.T) {
	lqKey := utilqueue.LocalQueueReference("ns/lq")
	now := time.Now()

	cases := map[string]struct {
		pushes  []corev1.ResourceList
		sub     corev1.ResourceList
		wantHas bool
		// wantMilli is asserted per resource name when wantHas is true.
		wantMilli map[corev1.ResourceName]int64
	}{
		"exact subtraction leaves nothing pending": {
			pushes:  []corev1.ResourceList{{corev1.ResourceCPU: resource.MustParse("2")}},
			sub:     corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("2")},
			wantHas: false,
		},
		"partial subtraction keeps the remainder": {
			pushes:    []corev1.ResourceList{{corev1.ResourceCPU: resource.MustParse("3")}},
			sub:       corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("2")},
			wantHas:   true,
			wantMilli: map[corev1.ResourceName]int64{corev1.ResourceCPU: 1_000},
		},
		"over-subtraction clamps to zero": {
			pushes:  []corev1.ResourceList{{corev1.ResourceCPU: resource.MustParse("2")}},
			sub:     corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("5")},
			wantHas: false,
		},
		"over-subtraction on one resource does not drain another": {
			pushes: []corev1.ResourceList{{
				corev1.ResourceCPU:    resource.MustParse("2"),
				corev1.ResourceMemory: resource.MustParse("4Gi"),
			}},
			sub:       corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("5")},
			wantHas:   true,
			wantMilli: map[corev1.ResourceName]int64{corev1.ResourceCPU: 0, corev1.ResourceMemory: 4 * 1024 * 1024 * 1024 * 1000},
		},
		"subtraction without a prior push leaves no negative residue": {
			sub:     corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("2")},
			wantHas: false,
		},
		"clamping applies per resource": {
			pushes: []corev1.ResourceList{{corev1.ResourceCPU: resource.MustParse("4")}},
			sub: corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("1"),
				corev1.ResourceMemory: resource.MustParse("1Gi"),
			},
			wantHas:   true,
			wantMilli: map[corev1.ResourceName]int64{corev1.ResourceCPU: 3_000, corev1.ResourceMemory: 0},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			penalties := NewAfsUsageLedger()
			for _, p := range tc.pushes {
				penalties.PushPenalty(lqKey, p, now)
			}

			penalties.SubPenalty(lqKey, tc.sub)

			if got := penalties.HasPendingPenalty(lqKey); got != tc.wantHas {
				t.Fatalf("HasPendingPenalty() = %t, want %t (peek: %v)", got, tc.wantHas, penalties.PeekPenalty(lqKey))
			}
			for resName, wantMilli := range tc.wantMilli {
				got := penalties.PeekPenalty(lqKey)[resName]
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
	lqKey := utilqueue.LocalQueueReference("ns/lq")
	created := time.Now()
	penalty := corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1")}

	ledger := NewAfsUsageLedger()
	ledger.PushPenalty(lqKey, penalty, created)

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

	ledger.PushPenalty(lqKey, penalty, created.Add(time.Hour))
	entry, _ = ledger.Get(lqKey)
	if !entry.LastUpdate.Equal(created) {
		t.Errorf("a later push moved LastUpdate to %v, want it left at %v", entry.LastUpdate, created)
	}
	if got := entry.PendingPenalty[corev1.ResourceCPU]; got.MilliValue() != 2_000 {
		t.Errorf("pending CPU = %dm, want 2000m", got.MilliValue())
	}
}

// A subtraction can target a LocalQueue whose ledger entry no longer exists — the
// LocalQueue was deleted while the rollback was in flight. It must not materialize
// an entry: the LocalQueue's cleanup already ran, so a stored entry would leak
// until restart.
func TestSubPenaltyDoesNotCreateEntry(t *testing.T) {
	lqKey := utilqueue.LocalQueueReference("ns/lq")

	ledger := NewAfsUsageLedger()
	ledger.SubPenalty(lqKey, corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1")})

	if _, found := ledger.Get(lqKey); found {
		t.Error("SubPenalty materialized a ledger entry for a LocalQueue that had none")
	}
}
