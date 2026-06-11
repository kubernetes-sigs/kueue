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

package core

import (
	"context"
	"reflect"
	"sync"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
)

// Manager trigger specific updates in order.
type QuotaUpdateStep = func(ctx context.Context, cq *kueue.ClusterQueue, cache *QuotaCache) (cont bool, err error)

// Controller can trigger an update at a specific stage of the manager's workflow.
type TriggerQuotaUpdate = func(ctx context.Context, cq *kueue.ClusterQueue) error

type QuotaManager struct {
	sync.RWMutex
	cache       *QuotaCache
	updateChain []QuotaUpdateStep
}

type QuotaCache struct {
	spec              map[kueue.ClusterQueueReference][]kueue.ResourceGroup
	mkAggregatedQuota map[kueue.ClusterQueueReference]kueue.ResourceGroup
	effectiveQuota    map[kueue.ClusterQueueReference][]kueue.ResourceGroup
}

func NewQuotaManager() *QuotaManager {
	return &QuotaManager{
		cache: &QuotaCache{
			spec:              nil,
			mkAggregatedQuota: nil,
		},
		updateChain: make([]QuotaUpdateStep, 0),
	}
}

func (qm *QuotaManager) triggerChain(id int, ctx context.Context, cq *kueue.ClusterQueue) error {
	qm.Lock()
	defer qm.Unlock()
	for _, update := range qm.updateChain[id:] {
		if cont, err := update(ctx, cq, qm.cache); err != nil {
			return err
		} else if !cont {
			return nil
		}
	}
	return nil
}

func (qm *QuotaManager) WithUpdateStep(update QuotaUpdateStep) TriggerQuotaUpdate {
	qm.Lock()
	defer qm.Unlock()
	qm.updateChain = append(qm.updateChain, update)
	idx := len(qm.updateChain) - 1
	return func(ctx context.Context, cq *kueue.ClusterQueue) error {
		return qm.triggerChain(idx, ctx, cq)
	}
}

func (qm *QuotaManager) IsSpecEqual(template []kueue.ResourceGroup) bool {
	qm.RLock()
	defer qm.RUnlock()
	return reflect.DeepEqual(qm.cache.spec, template)
}
