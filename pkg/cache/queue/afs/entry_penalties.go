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
	"time"

	corev1 "k8s.io/api/core/v1"

	utilqueue "sigs.k8s.io/kueue/pkg/util/queue"
	"sigs.k8s.io/kueue/pkg/util/resource"
)

// PushPenalty records an entry penalty for a LocalQueue; it counts toward the
// LocalQueue's fair-sharing usage until a settlement consolidates it. now stamps
// a newly created entry's LastUpdate so the first decay elapses from the push
// rather than from the zero time; such an entry carries StatusAccounted=false so
// the persisted status is still merged.
func (a *AfsUsageLedger) PushPenalty(lqKey utilqueue.LocalQueueReference, penalty corev1.ResourceList, now time.Time) {
	a.entries.Update(lqKey, func(entry UsageLedgerEntry, found bool) UsageLedgerEntry {
		if !found {
			entry.LastUpdate = now
		}
		entry.PendingPenalty = resource.MergeResourceListKeepSum(entry.PendingPenalty, penalty)
		return entry
	})
}

// SubPenalty removes a penalty that was pushed but will never settle, i.e. the
// scheduler rolled an assumed Workload back after a failed admission patch.
// A LocalQueue with no ledger entry is a no-op: materializing one would outlive
// the LocalQueue's own cleanup when the rollback races its deletion.
func (a *AfsUsageLedger) SubPenalty(lqKey utilqueue.LocalQueueReference, penalty corev1.ResourceList) {
	negated := make(corev1.ResourceList, len(penalty))
	for k, v := range penalty {
		q := v.DeepCopy()
		q.Neg()
		negated[k] = q
	}
	a.entries.UpdateIfPresent(lqKey, func(entry UsageLedgerEntry) UsageLedgerEntry {
		entry.PendingPenalty = clampNegativeToZero(resource.MergeResourceListKeepSum(entry.PendingPenalty, negated))
		return entry
	})
}

// clampNegativeToZero floors each resource at zero. A rollback can outrun the
// settlement that already consolidated the penalty; a negative bucket would be a
// lasting usage discount for the LocalQueue — the exploitable direction, whereas
// over-counting only penalizes the queue itself.
func clampNegativeToZero(penalty corev1.ResourceList) corev1.ResourceList {
	for k, v := range penalty {
		if v.Sign() < 0 {
			q := v.DeepCopy()
			q.Set(0)
			penalty[k] = q
		}
	}
	return penalty
}

// PeekPenalty returns the penalties awaiting settlement for a LocalQueue.
func (a *AfsUsageLedger) PeekPenalty(lqKey utilqueue.LocalQueueReference) corev1.ResourceList {
	entry, found := a.entries.Get(lqKey)
	if !found {
		return corev1.ResourceList{}
	}
	return entry.PendingPenalty
}

// HasPendingPenalty reports whether any penalty is awaiting settlement. It tests
// the amounts: an entry outlives its penalties (it also holds the consumed
// history), so entry presence is not a reliable signal of pending work.
func (a *AfsUsageLedger) HasPendingPenalty(lqKey utilqueue.LocalQueueReference) bool {
	entry, found := a.entries.Get(lqKey)
	if !found {
		return false
	}
	for _, q := range entry.PendingPenalty {
		if !q.IsZero() {
			return true
		}
	}
	return false
}
