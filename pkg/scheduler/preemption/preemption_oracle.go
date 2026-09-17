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

	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/resources"
	"sigs.k8s.io/kueue/pkg/scheduler/preemption/classical"
	preemptioncommon "sigs.k8s.io/kueue/pkg/scheduler/preemption/common"
	"sigs.k8s.io/kueue/pkg/workload"
)

type PreemptionOracle interface {
	SimulatePreemption(
		ctx context.Context,
		cq *schdcache.ClusterQueueSnapshot,
		wl workload.Info,
		fr resources.FlavorResource,
		quantity resources.Amount,
	) (preemptioncommon.PreemptionPossibility, int)
}

func NewOracle(preemptor *Preemptor, snapshot *schdcache.Snapshot) *ClassicalPreemptionOracle {
	return &ClassicalPreemptionOracle{preemptor, snapshot}
}

type ClassicalPreemptionOracle struct {
	preemptor *Preemptor
	snapshot  *schdcache.Snapshot
}

// SimulatePreemption runs the preemption algorithm for a given flavor resource to check if
// preemption and reclaim are possible in this flavor resource.
func (p *ClassicalPreemptionOracle) SimulatePreemption(
	ctx context.Context,
	cq *schdcache.ClusterQueueSnapshot,
	wl workload.Info,
	fr resources.FlavorResource,
	quantity resources.Amount,
) (preemptioncommon.PreemptionPossibility, int) {
	pCtx := &preemptionCtx{
		clock:             p.preemptor.clock,
		preemptor:         wl,
		preemptorCQ:       p.snapshot.ClusterQueue(wl.ClusterQueue),
		snapshot:          p.snapshot,
		frsNeedPreemption: sets.New(fr),
		workloadUsage: workload.Usage{
			Quota: workload.ResourceUsage{
				Assigned: resources.FlavorResourceQuantities{fr: quantity},
			},
		},
	}
	candidates := p.preemptor.getTargets(ctx, p.preemptor.getPreemptionPlan(ctx, pCtx))

	if len(candidates) == 0 {
		borrow, _ := classical.FindHeightOfLowestSubtreeThatFits(cq, fr, quantity)
		return preemptioncommon.NoCandidates, borrow
	}

	workloadsToPreempt := make([]*workload.Info, len(candidates))
	for i, c := range candidates {
		workloadsToPreempt[i] = c.WorkloadInfo
	}
	revertRemoval := p.snapshot.SimulateWorkloadUsageRemoval(workloadsToPreempt)
	borrowAfterPreemptions, _ := classical.FindHeightOfLowestSubtreeThatFits(cq, fr, quantity)
	revertRemoval()

	for _, candidate := range candidates {
		if candidate.WorkloadInfo.ClusterQueue == cq.Name {
			return preemptioncommon.Preempt, borrowAfterPreemptions
		}
	}
	return preemptioncommon.Reclaim, borrowAfterPreemptions
}

func NewSchedulerLibraryOracle(snapshot *schdcache.Snapshot) *SchedulerLibraryPreemptionOracle {
	return &SchedulerLibraryPreemptionOracle{
		snapshot: snapshot,
	}
}

type SchedulerLibraryPreemptionOracle struct {
	snapshot *schdcache.Snapshot
}

// SimulatePreemption runs the preemption algorithm for a given flavor resource to check if
// preemption and reclaim are possible in this flavor resource.
func (s *SchedulerLibraryPreemptionOracle) SimulatePreemption(
	ctx context.Context,
	cq *schdcache.ClusterQueueSnapshot,
	wl workload.Info,
	fr resources.FlavorResource,
	quantity resources.Amount,
) (preemptioncommon.PreemptionPossibility, int) {
	panic("not implemented")
}
