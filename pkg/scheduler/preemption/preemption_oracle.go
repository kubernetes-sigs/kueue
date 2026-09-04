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
	"context"

	"k8s.io/apimachinery/pkg/util/sets"
	"sigs.k8s.io/controller-runtime/pkg/log"

	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/resources"
	"sigs.k8s.io/kueue/pkg/scheduler/preemption/classical"
	preemptioncommon "sigs.k8s.io/kueue/pkg/scheduler/preemption/common"
	"sigs.k8s.io/kueue/pkg/scheduler/simulation"
	"sigs.k8s.io/kueue/pkg/workload"
)

func NewOracle(preemptor *Preemptor, simCtx *simulation.SimulationContext) *PreemptionOracle {
	return &PreemptionOracle{preemptor, simCtx}
}

type PreemptionOracle struct {
	preemptor         *Preemptor
	simulationContext *simulation.SimulationContext
}

// SimulatePreemption runs the preemption algorithm for a given flavor resource to check if
// preemption and reclaim are possible in this flavor resource.
func (p *PreemptionOracle) SimulatePreemption(
	ctx context.Context,
	cq *schdcache.ClusterQueueSnapshot,
	wl workload.Info,
	fr resources.FlavorResource,
	quantity resources.Amount,
) (possibility preemptioncommon.PreemptionPossibility, borrow int, simErr error) {
	log := log.FromContext(ctx)
	simErr = simulation.SimulateNested(p.simulationContext, func(simCtx *simulation.SimulationContext) error {
		candidates, err := p.preemptor.getTargets(simCtx, &preemptionCtx{
			ctx:               ctx,
			clock:             p.preemptor.clock,
			log:               log,
			preemptor:         wl,
			preemptorCQ:       simCtx.ClusterQueue(wl.ClusterQueue),
			frsNeedPreemption: sets.New(fr),
			workloadUsage: workload.Usage{
				Quota: workload.ResourceUsage{
					Assigned: resources.FlavorResourceQuantities{fr: quantity},
				},
			},
		})

		if err != nil {
			return err
		}

		if len(candidates) == 0 {
			possibility = preemptioncommon.NoCandidates
			borrow, _ = classical.FindHeightOfLowestSubtreeThatFits(cq, fr, quantity)
			return nil
		}

		borrowAfterPreemptions, _ := classical.FindHeightOfLowestSubtreeThatFits(cq, fr, quantity)
		for _, candidate := range candidates {
			if candidate.WorkloadInfo.ClusterQueue == cq.Name {
				possibility, borrow = preemptioncommon.Preempt, borrowAfterPreemptions
				return nil
			}
		}
		possibility, borrow = preemptioncommon.Reclaim, borrowAfterPreemptions
		return nil
	})
	if simErr != nil {
		return preemptioncommon.NoCandidates, 0, simErr
	}
	return
}
