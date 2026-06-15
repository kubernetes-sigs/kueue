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
	"errors"
	"sync"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
)

const (
	UpdateStepSpec                    string = "spec"
	UpdateStepEffectiveResourceGroups string = "effective"
)

// QuotaUpdateStep can perform updates on ClusterQueues and the internal QuotaCache.
// It returns information on whether the update chain should continue, and an error if something goes wrong.
// An error always ends the chain and is returned immediately to the calling QuotaUpdateTrigger.
type QuotaUpdateStep = func(ctx context.Context, cq *kueue.ClusterQueue, cache *QuotaCache) (cont bool, err error)

// QuotaUpdateTrigger allows to start an update chain from a specific QuotaUpdateStep.
// The execution will proceed until:
//   - we execute all remaining steps, OR
//   - a step returns cont=false or err != nil.
type QuotaUpdateTrigger = func(ctx context.Context, cq *kueue.ClusterQueue) error

// QuotaManager is a central place for managing updates of quota related information.
// It executes the chain of registered QuotaUpdateSteps and allows to start the chain from any step via a dedicated QuotaUpdateTrigger.
type QuotaManager struct {
	sync.RWMutex
	cache          *QuotaCache
	updateRegistry map[string]int
	updateChain    []QuotaUpdateStep
}

// QuotaCache is the internal cache of the QuotaManager.
// It is used by QuotaUpdateSteps to pass partial quota calculation results to following steps.
type QuotaCache struct {
	spec              map[kueue.ClusterQueueReference][]kueue.ResourceGroup
	mkAggregatedQuota map[kueue.ClusterQueueReference]kueue.ResourceGroup
	effectiveQuota    map[kueue.ClusterQueueReference][]kueue.ResourceGroup
}

// QuotaManagerOpts houses references to reconcilers that provide the QuotaUpdateStep functions and invoke their QuotaUpdateTriggers.
type QuotaManagerOpts struct {
	Manager   *QuotaManager
	CoreCQRec *ClusterQueueReconciler
}

// NewQuotaManager creates a QuotaManager with update setps configured based on active features using reconcilers provided in opts.
func NewQuotaManager() *QuotaManager {
	qm := &QuotaManager{
		cache: &QuotaCache{
			spec:              nil,
			mkAggregatedQuota: nil,
		},
		updateChain: make([]QuotaUpdateStep, 0),
	}
	return qm
}

func (opts *QuotaManagerOpts) Apply() {
	opts.Manager.addUpdateStep(UpdateStepSpec, opts.CoreCQRec.updateSpec)
	opts.Manager.addUpdateStep(UpdateStepEffectiveResourceGroups, opts.CoreCQRec.updateEffectiveResourceGroups)
}

func (qm *QuotaManager) UpdateQuota(startStepAlias string, ctx context.Context, cq *kueue.ClusterQueue) error {
	stepIdx, ok := qm.updateRegistry[startStepAlias]
	if !ok {
		return errors.New("no update registered with alias: " + startStepAlias)
	}
	return qm.triggerChain(stepIdx, ctx, cq)
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
func (qm *QuotaManager) addUpdateStep(alias string, update QuotaUpdateStep) {
	qm.Lock()
	defer qm.Unlock()
	qm.updateChain = append(qm.updateChain, update)
	qm.updateRegistry[alias] = len(qm.updateChain) - 1
}
