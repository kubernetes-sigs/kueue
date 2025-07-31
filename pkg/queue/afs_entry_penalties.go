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

package queue

import (
	"sync"

	corev1 "k8s.io/api/core/v1"

	utilmaps "sigs.k8s.io/kueue/pkg/util/maps"
	utilqueue "sigs.k8s.io/kueue/pkg/util/queue"
	"sigs.k8s.io/kueue/pkg/util/resource"
)

type AfsEntryPenalties struct {
	sync.RWMutex
	penalties *utilmaps.SyncMap[utilqueue.LocalQueueReference, corev1.ResourceList]
}

func newPenaltyMap() *AfsEntryPenalties {
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
	m.penalties.Add(lqKey, resource.MergeResourceListKeepSum(m.Peek(lqKey), penalty))
}

func (m *AfsEntryPenalties) Peek(lqKey utilqueue.LocalQueueReference) corev1.ResourceList {
	penalty, found := m.penalties.Get(lqKey)
	if !found {
		return corev1.ResourceList{}
	}

	return penalty
}

func (m *AfsEntryPenalties) WithPenaltyLocked(lqKey utilqueue.LocalQueueReference, fn func(penalty corev1.ResourceList) error) error {
	m.Lock()
	defer m.Unlock()

	penalty, found := m.penalties.Get(lqKey)
	if !found {
		penalty = corev1.ResourceList{}
	}

	err := fn(penalty)
	if err == nil && found {
		m.penalties.Delete(lqKey)
	}

	return err
}

func (m *AfsEntryPenalties) HasPendingFor(lqKey utilqueue.LocalQueueReference) bool {
	m.Lock()
	defer m.Unlock()

	_, found := m.penalties.Get(lqKey)

	return found
}

func (m *AfsEntryPenalties) HasAny() bool {
	m.RLock()
	defer m.RUnlock()

	return m.penalties.Len() > 0
}

func (m *AfsEntryPenalties) GetPenalties() *utilmaps.SyncMap[utilqueue.LocalQueueReference, corev1.ResourceList] {
	return m.penalties
}

func (m *AfsEntryPenalties) GetLocalQueueKeysWithPenalties() []utilqueue.LocalQueueReference {
	m.RLock()
	defer m.RUnlock()

	return m.penalties.Keys()
}
