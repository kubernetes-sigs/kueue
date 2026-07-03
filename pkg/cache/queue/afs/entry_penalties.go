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
	corev1 "k8s.io/api/core/v1"

	utilmaps "sigs.k8s.io/kueue/pkg/util/maps"
	utilqueue "sigs.k8s.io/kueue/pkg/util/queue"
	"sigs.k8s.io/kueue/pkg/util/resource"
)

type AfsEntryPenalties struct {
	penalties *utilmaps.SyncMap[utilqueue.LocalQueueReference, corev1.ResourceList]
}

func NewPenaltyMap() *AfsEntryPenalties {
	return &AfsEntryPenalties{
		penalties: utilmaps.NewSyncMap[utilqueue.LocalQueueReference, corev1.ResourceList](0),
	}
}

func (m *AfsEntryPenalties) Push(lqKey utilqueue.LocalQueueReference, penalty corev1.ResourceList) {
	m.penalties.Add(lqKey, resource.MergeResourceListKeepSum(m.Peek(lqKey), penalty))
}

func (m *AfsEntryPenalties) Sub(lqKey utilqueue.LocalQueueReference, penalty corev1.ResourceList) {
	for k, v := range penalty {
		v.Neg()
		penalty[k] = v
	}
	newPenalty := resource.MergeResourceListKeepSum(m.Peek(lqKey), penalty)
	// Sub recomputes the amount at settlement time rather than recording it at
	// push time, so it can exceed what was pushed (e.g. after a restart loses the
	// in-memory penalties). Clamp at zero: a mismatch must not become a permanent
	// usage discount for the LocalQueue.
	for k, v := range newPenalty {
		if v.Sign() < 0 {
			v.Set(0)
			newPenalty[k] = v
		}
	}
	m.penalties.Add(lqKey, newPenalty)
	if canClearPenalty(m.Peek(lqKey)) {
		m.penalties.Delete(lqKey)
	}
}

func canClearPenalty(penalty corev1.ResourceList) bool {
	for _, v := range penalty {
		if !v.IsZero() {
			return false
		}
	}
	return true
}

func (m *AfsEntryPenalties) Peek(lqKey utilqueue.LocalQueueReference) corev1.ResourceList {
	penalty, found := m.penalties.Get(lqKey)
	if !found {
		return corev1.ResourceList{}
	}

	return penalty
}

func (m *AfsEntryPenalties) HasPendingFor(lqKey utilqueue.LocalQueueReference) bool {
	_, found := m.penalties.Get(lqKey)
	return found
}

func (m *AfsEntryPenalties) Delete(lqKey utilqueue.LocalQueueReference) {
	m.penalties.Delete(lqKey)
}
