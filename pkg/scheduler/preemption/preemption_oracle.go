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

package preemption

import (
	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/util/sets"

	"sigs.k8s.io/kueue/pkg/cache"
	"sigs.k8s.io/kueue/pkg/resources"
	"sigs.k8s.io/kueue/pkg/scheduler/preemption/classical"
	preemptioncommon "sigs.k8s.io/kueue/pkg/scheduler/preemption/common"
	"sigs.k8s.io/kueue/pkg/workload"
)

func NewOracle(preemptor *Preemptor, snapshot *cache.Snapshot) *PreemptionOracle {
	return &PreemptionOracle{preemptor, snapshot}
}

type PreemptionOracle struct {
	preemptor *Preemptor
	snapshot  *cache.Snapshot
}

// SimulatePreemption runs the preemption algorithm for a given flavor resource to check if
// preemption and reclaim are possible in this flavor resource.
func (p *PreemptionOracle) SimulatePreemption(log logr.Logger, cq *cache.ClusterQueueSnapshot, wl workload.Info, fr resources.FlavorResource, quantity int64) (preemptioncommon.PreemptionPossibility, int) {
	candidates := p.preemptor.getTargets(&preemptionCtx{
		log:               log,
		preemptor:         wl,
		preemptorCQ:       p.snapshot.ClusterQueue(wl.ClusterQueue),
		snapshot:          p.snapshot,
		frsNeedPreemption: sets.New(fr),
		workloadUsage:     workload.Usage{Quota: resources.FlavorResourceQuantities{fr: quantity}},
	})

	if len(candidates) == 0 {
		borrow, _ := classical.FindHeightOfLowestSubtreeThatFits(cq, fr, quantity)
		return preemptioncommon.NoCandidates, borrow
	}

	workloadsToPreempt := make([]*workload.Info, len(candidates))
	for i, c := range candidates {
		workloadsToPreempt[i] = c.WorkloadInfo
	}
	revertRemoval := cq.SimulateWorkloadRemoval(workloadsToPreempt)
	borrowAfterPreemptions, _ := classical.FindHeightOfLowestSubtreeThatFits(cq, fr, quantity)
	revertRemoval()

	for _, candidate := range candidates {
		if candidate.WorkloadInfo.ClusterQueue == cq.Name {
			return preemptioncommon.Preempt, borrowAfterPreemptions
		}
	}
	return preemptioncommon.Reclaim, borrowAfterPreemptions
}
