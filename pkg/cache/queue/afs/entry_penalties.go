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

// PushPenalty records an entry penalty for a LocalQueue. The penalty counts toward
// the LocalQueue's fair-sharing usage until a settlement consolidates it into the
// consumed history.
//
// now is used only when the push creates the entry, so that the first sampling tick
// measures elapsed time from the push rather than from the zero time. An entry
// created here carries StatusAccounted=false, which makes the LocalQueue reconciler
// merge the persisted status into it exactly once.
func (a *AfsUsageLedger) PushPenalty(lqKey utilqueue.LocalQueueReference, penalty corev1.ResourceList, now time.Time) {
	a.resources.Update(lqKey, func(entry UsageLedgerEntry, found bool) UsageLedgerEntry {
		if !found {
			entry.LastUpdate = now
		}
		entry.PendingPenalty = resource.MergeResourceListKeepSum(entry.PendingPenalty, penalty)
		return entry
	})
}

// SubPenalty removes a penalty that was pushed but will never be settled, i.e. the
// scheduler rolled an assumed Workload back after a failed admission patch.
//
// It stamps LastUpdate on creation for the same reason PushPenalty does: the rolled
// back Workload's LocalQueue may have been deleted in the meantime, and resurrecting
// its entry with a zero timestamp would make the next decay elapse from the zero time.
func (a *AfsUsageLedger) SubPenalty(lqKey utilqueue.LocalQueueReference, penalty corev1.ResourceList, now time.Time) {
	negated := make(corev1.ResourceList, len(penalty))
	for k, v := range penalty {
		q := v.DeepCopy()
		q.Neg()
		negated[k] = q
	}
	a.resources.Update(lqKey, func(entry UsageLedgerEntry, found bool) UsageLedgerEntry {
		if !found {
			entry.LastUpdate = now
		}
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
	entry, found := a.resources.Get(lqKey)
	if !found {
		return corev1.ResourceList{}
	}
	return entry.PendingPenalty
}

// HasPendingPenalty reports whether any penalty is awaiting settlement. It tests the
// amounts rather than the presence of an entry, which now outlives every penalty, and
// rather than the presence of a key, which a fully subtracted penalty leaves behind.
func (a *AfsUsageLedger) HasPendingPenalty(lqKey utilqueue.LocalQueueReference) bool {
	entry, found := a.resources.Get(lqKey)
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
