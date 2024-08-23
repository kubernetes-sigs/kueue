/*
Copyright 2024 The Kubernetes Authors.

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

package preemption

import (
	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/util/sets"

	"sigs.k8s.io/kueue/pkg/cache"
	"sigs.k8s.io/kueue/pkg/resources"
	"sigs.k8s.io/kueue/pkg/workload"
)

func NewOracle(preemptor *Preemptor, snapshot *cache.Snapshot) *PreemptionOracle {
	return &PreemptionOracle{preemptor, snapshot}
}

type PreemptionOracle struct {
	preemptor *Preemptor
	snapshot  *cache.Snapshot
}

// IsReclaimPossible determines if a ClusterQueue can fit this
// FlavorResource by reclaiming its nominal quota which it lent to its
// Cohort.
func (p *PreemptionOracle) IsReclaimPossible(log logr.Logger, cq *cache.ClusterQueueSnapshot, wl workload.Info, fr resources.FlavorResource, quantity int64) bool {
	if cq.BorrowingWith(fr, quantity) {
		return false
	}

	for _, candidate := range p.preemptor.getTargets(log, wl, resources.FlavorResourceQuantities{fr: quantity}, sets.New(fr), p.snapshot) {
		if candidate.WorkloadInfo.ClusterQueue == cq.Name {
			return false
		}
	}
	return true
}
