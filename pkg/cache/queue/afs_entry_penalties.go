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

func (m *AfsEntryPenalties) push(lqKey utilqueue.LocalQueueReference, penalty corev1.ResourceList) {
	m.Lock()
	defer m.Unlock()
	m.penalties.Add(lqKey, resource.MergeResourceListKeepSum(m.peek(lqKey), penalty))
}

func (m *AfsEntryPenalties) sub(lqKey utilqueue.LocalQueueReference, penalty corev1.ResourceList) {
	m.Lock()
	defer m.Unlock()
	for k, v := range penalty {
		v.Neg()
		penalty[k] = v
	}
	m.penalties.Add(lqKey, resource.MergeResourceListKeepSum(m.peek(lqKey), penalty))
}

func (m *AfsEntryPenalties) peek(lqKey utilqueue.LocalQueueReference) corev1.ResourceList {
	penalty, found := m.penalties.Get(lqKey)
	if !found {
		return corev1.ResourceList{}
	}

	return penalty
}

func (m *AfsEntryPenalties) updateWithPenalty(lqKey utilqueue.LocalQueueReference, fn func(penalty corev1.ResourceList) error) error {
	m.Lock()
	defer m.Unlock()
	penalty, found := m.penalties.Get(lqKey)
	if !found {
		penalty = corev1.ResourceList{}
	}
	if err := fn(penalty); err != nil {
		return err
	}
	m.penalties.Delete(lqKey)
	return nil
}

func (m *AfsEntryPenalties) hasPendingFor(lqKey utilqueue.LocalQueueReference) bool {
	_, found := m.penalties.Get(lqKey)
	return found
}

func (m *AfsEntryPenalties) getPenalties() *utilmaps.SyncMap[utilqueue.LocalQueueReference, corev1.ResourceList] {
	return m.penalties
}

func (m *AfsEntryPenalties) getLocalQueueKeysWithPenalties() []utilqueue.LocalQueueReference {
	return m.penalties.Keys()
}
