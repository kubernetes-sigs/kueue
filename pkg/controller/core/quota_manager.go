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

type QuotaManagerOpts struct {
	CoreCQRec *ClusterQueueReconciler
}

// NewQuotaManager creates a QuotaManager with update setps configured based on active features using reconcilers provided in opts.
func NewQuotaManager(opts *QuotaManagerOpts) *QuotaManager {
	qm := &QuotaManager{
		cache: &QuotaCache{
			spec:              nil,
			mkAggregatedQuota: nil,
		},
		updateChain: make([]QuotaUpdateStep, 0),
	}

	qm.addUpdateStep(opts.CoreCQRec.updateSpec, &opts.CoreCQRec.triggerSpecUpdate)
	qm.addUpdateStep(opts.CoreCQRec.updateEffectiveResourceGroups, nil)

	return qm
}

func (qm *QuotaManager) triggerChain(startIdx int, ctx context.Context, cq *kueue.ClusterQueue) error {
	qm.Lock()
	defer qm.Unlock()
	for _, update := range qm.updateChain[startIdx:] {
		if cont, err := update(ctx, cq, qm.cache); err != nil {
			return err
		} else if !cont {
			return nil
		}
	}
	return nil
}

// addUpdateStep adds the provided QuotaUpdateStep function to the end of the update chain.
// If callbackDest is not nil, a callback to start the update chain from the added step is injected there.
func (qm *QuotaManager) addUpdateStep(update QuotaUpdateStep, callbackDest *TriggerQuotaUpdate) {
	qm.Lock()
	defer qm.Unlock()
	// Add the update step to the end of the chain.
	qm.updateChain = append(qm.updateChain, update)

	// Save the callback function to the destination provided via pointer.
	if callbackDest != nil {
		idx := len(qm.updateChain) - 1
		*callbackDest = func(ctx context.Context, cq *kueue.ClusterQueue) error {
			return qm.triggerChain(idx, ctx, cq)
		}
	}
}

func (qm *QuotaManager) IsSpecEqual(template []kueue.ResourceGroup) bool {
	qm.RLock()
	defer qm.RUnlock()
	return reflect.DeepEqual(qm.cache.spec, template)
}
