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
	"iter"
	"slices"

	"github.com/go-logr/logr"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/log"

	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/resources"
	"sigs.k8s.io/kueue/pkg/scheduler/flavorassigner"
	"sigs.k8s.io/kueue/pkg/util/priority"
	"sigs.k8s.io/kueue/pkg/workload"
)

// CanColocate limits discretionary preemption to the initial alpha scope.
func (p *Preemptor) CanColocate(wl *workload.Info, cq *schdcache.ClusterQueueSnapshot) bool {
	return features.Enabled(features.AggressivePriorityColocate) &&
		!p.enableFairSharing && cq.PreemptionConfigName == nil &&
		len(wl.Obj.Spec.PodSets) == 1 && !workload.HasQuotaReservation(wl.Obj) &&
		!workload.IsElasticWorkload(wl.Obj) && !workload.CanBePartiallyAdmitted(wl.Obj) &&
		!workload.HasUnhealthyNodes(wl.Obj) &&
		!cq.HasMultiKueueAdmissionCheck()
}

// ColocationTargetsEligible rechecks the retained victims against current policy
// and the whole-workload budget without allowing a replacement victim set.
func (p *Preemptor) ColocationTargetsEligible(ctx context.Context, wl *workload.Info, snapshot *schdcache.Snapshot, asgn flavorassigner.Assignment, targets []*Target) bool {
	if len(targets) == 0 {
		return true
	}
	logger := log.FromContext(ctx)
	budget, cost := totalReserved(wl), resources.NewRequests()
	wanted := make(map[workload.Reference]struct{}, len(targets))
	for _, target := range targets {
		if !lowerPriority(logger, wl, target.WorkloadInfo) {
			return false
		}
		cost.Add(totalReserved(target.WorkloadInfo))
		wanted[workload.Key(target.WorkloadInfo.Obj)] = struct{}{}
	}
	if !withinBudget(cost, budget) {
		return false
	}
	pCtx := p.buildContext(ctx, *wl, asgn, snapshot)
	pCtx.frsNeedPreemption = requestedFlavorResources(pCtx)
	for strategy := range p.getPreemptionStrategyIterator(ctx, pCtx) {
		var removed []*Target
		for candidate := range strategy.candidates {
			if _, keep := wanted[workload.Key(candidate.WorkloadInfo.Obj)]; keep {
				removed = append(removed, candidate)
			} else {
				snapshot.AddWorkload(candidate.WorkloadInfo)
			}
		}
		valid := len(removed) == len(targets) && workloadQuotaFits(pCtx, strategy.allowBorrowing) && strategy.accepts(ctx, removed)
		restoreSnapshot(snapshot, removed)
		if valid {
			return true
		}
	}
	return false
}

// ColocationTargets probes one optimistic placement for each classical borrowing
// strategy. Every real attempt rechecks policy against the changing snapshot.
func (p *Preemptor) ColocationTargets(
	ctx context.Context,
	wl *workload.Info,
	snapshot *schdcache.Snapshot,
	asgn flavorassigner.Assignment,
	requests schdcache.WorkloadTASRequests,
	baseline schdcache.TASAssignmentsResult,
) (schdcache.TASAssignmentsResult, []*Target, *workload.ColocationPlan) {
	logger := log.FromContext(ctx)
	cq := snapshot.ClusterQueue(wl.ClusterQueue)
	if !p.CanColocate(wl, cq) || len(requests) != 1 {
		return baseline, nil, nil
	}
	psName := wl.Obj.Spec.PodSets[0].Name
	base := baseline[psName]
	flavor := cq.TASFlavors[base.Flavor]
	budget := totalReserved(wl)
	if flavor == nil || base.TopologyAssignment == nil {
		return baseline, nil, nil
	}
	baseScore := flavor.ColocationScores(wl, budget, base.TopologyAssignment)
	pCtx := p.buildContext(ctx, *wl, asgn, snapshot)
	pCtx.tasRequests = requests
	pCtx.frsNeedPreemption = requestedFlavorResources(pCtx)
	for strategy := range p.getPreemptionStrategyIterator(ctx, pCtx) {
		var optimistic []*workload.Info
		for candidate := range strategy.candidates {
			snapshot.AddWorkload(candidate.WorkloadInfo)
			if lowerPriority(logger, wl, candidate.WorkloadInfo) && withinBudget(totalReserved(candidate.WorkloadInfo), budget) {
				optimistic = append(optimistic, candidate.WorkloadInfo)
			}
		}
		if len(optimistic) == 0 {
			continue
		}
		restore := snapshot.SimulateRemovals(ctx, logger, optimistic)
		desired := asgn.FindTopologyAssignments(ctx, cq, requests, schdcache.WithWorkloadInfo(wl), schdcache.WithPriorityColocation())
		restore()
		target := desired[psName].TopologyAssignment
		if desired.Failure() != nil || target == nil ||
			slices.Compare(flavor.ColocationScores(wl, budget, target), baseScore) <= 0 {
			continue
		}

		var verified schdcache.TASAssignmentsResult
		state := &workload.ColocationPlan{
			Hash: wl.SchedulingHash, Queue: wl.ClusterQueue, Flavor: base.Flavor,
			Assignment: target,
		}
		pCtx.topologyFit = func() bool {
			verified = asgn.FindTopologyAssignments(ctx, cq, requests, schdcache.WithWorkloadInfo(wl), schdcache.WithPinnedColocation(target))
			return verified.Failure() == nil && state.Matches(verified[psName].TopologyAssignment)
		}
		original := strategy.candidates
		strategy.candidates = colocationCandidates(original, snapshot, wl, budget, desired, logger)
		// Partial preemption is intentionally not used for a discretionary plan.
		targets := strategy.searchTargets(ctx, nil)
		pCtx.topologyFit = nil
		if len(targets) == 0 {
			continue
		}
		// Search/fill-back restored the snapshot. Verify the final full victim
		// set once more and return only the constrained, real assignment.
		victims := make([]*workload.Info, 0, len(targets))
		ids := make(map[workload.Reference]workload.ColocationVictim, len(targets))
		for _, t := range targets {
			victims = append(victims, t.WorkloadInfo)
			ids[workload.Key(t.WorkloadInfo.Obj)] = workload.ColocationVictim{UID: t.WorkloadInfo.Obj.UID, Reason: t.Reason}
		}
		restore = snapshot.SimulateRemovals(ctx, logger, victims)
		verified = asgn.FindTopologyAssignments(ctx, cq, requests, schdcache.WithWorkloadInfo(wl), schdcache.WithPinnedColocation(target))
		valid := verified.Failure() == nil && state.Matches(verified[psName].TopologyAssignment)
		restore()
		if !valid {
			continue
		}
		logger.V(3).Info("Selected colocation preemption", "oldScores", baseScore,
			"newScores", flavor.ColocationScores(wl, budget, target), "victimCount", len(targets))
		state.Victims = ids
		return verified, targets, state
	}
	return baseline, nil, nil
}

func colocationCandidates(
	original iter.Seq[*Target],
	snapshot *schdcache.Snapshot,
	wl *workload.Info,
	budget resources.Requests,
	desired schdcache.TASAssignmentsResult,
	logger logr.Logger,
) iter.Seq[*Target] {
	return func(yield func(*Target) bool) {
		spent := resources.NewRequests()
		for target := range original {
			cost := totalReserved(target.WorkloadInfo)
			next := spent.Clone()
			next.Add(cost)
			if !lowerPriority(logger, wl, target.WorkloadInfo) ||
				!withinBudget(next, budget) || !usesColocationDomains(target.WorkloadInfo, desired) {
				snapshot.AddWorkload(target.WorkloadInfo)
				continue
			}
			spent = next
			if !yield(target) {
				return
			}
		}
	}
}

func lowerPriority(logger logr.Logger, incoming, victim *workload.Info) bool {
	return priority.EffectivePriority(logger, victim.Obj) < priority.EffectivePriority(logger, incoming.Obj)
}

func totalReserved(wl *workload.Info) resources.Requests {
	total := resources.NewRequests()
	for _, ps := range wl.TotalRequests {
		total.Add(ps.Requests)
	}
	return total
}

func withinBudget(cost, budget resources.Requests) bool {
	fits := true
	budget.ForEach(func(name corev1.ResourceName, limit resources.Amount) {
		if name != corev1.ResourcePods && limit.Sign() > 0 && cost.ResourceValue(name).Cmp(limit) > 0 {
			fits = false
		}
	})
	return fits
}

func usesColocationDomains(wl *workload.Info, desired schdcache.TASAssignmentsResult) bool {
	for _, assignment := range desired {
		for _, usage := range wl.TASUsage()[assignment.Flavor] {
			for _, target := range assignment.TopologyAssignment.Domains {
				if slices.Equal(usage.Values, target.Values) {
					return true
				}
			}
		}
	}
	return false
}
