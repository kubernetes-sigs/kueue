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

package config

import (
	"context"
	"slices"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/utils/clock"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/resources"
	"sigs.k8s.io/kueue/pkg/scheduler/preemption/common"
	"sigs.k8s.io/kueue/pkg/workload"
)

// This file provides methods for evaluating configurable preemption rules across triggers:
// resolving the PreemptionConfig, checking which triggers are configured, and simulating
// candidate preemption across triggers.
//
// Triggers are applied as a fallback in the order the API defines them: the Always
// trigger first, as a baseline, then InsufficientQuota while the quota is not sufficient,
// and finally QuotaFeasibleAndInsufficientTopology once the quota is sufficient but no
// topology assignment can be found.

// NewEvaluatorForClusterQueue returns the PreemptionEvaluator for the PreemptionConfig
// referenced by the ClusterQueue, or nil if the ConfigurablePreemptions feature is
// disabled, the ClusterQueue references no PreemptionConfig, or it cannot be read.
func NewEvaluatorForClusterQueue(
	ctx context.Context,
	log logr.Logger,
	clock clock.Clock,
	cl client.Client,
	cq *schdcache.ClusterQueueSnapshot,
) *PreemptionEvaluator {
	if !features.Enabled(features.ConfigurablePreemptions) || cq == nil || cq.PreemptionConfigName == nil {
		return nil
	}
	preemptionConfig := &kueuealpha.PreemptionConfig{}
	preemptionConfigName := *cq.PreemptionConfigName
	if err := cl.Get(ctx, client.ObjectKey{Name: preemptionConfigName}, preemptionConfig); err != nil {
		log.Error(err, "Failed to get PreemptionConfig", "preemptionConfigName", preemptionConfigName)
		return nil
	}
	return NewPreemptionEvaluator(ctx, log, clock, *preemptionConfig)
}

// HasRules returns whether the PreemptionConfig holds any rule at all. It inspects
// the configuration only, which lets callers keep going without evaluating the
// candidates of a trigger before the phase actually reached it.
func (p *PreemptionEvaluator) HasRules() bool {
	return p != nil &&
		p.HasRulesFor(kueuealpha.Always, kueuealpha.InsufficientQuota, kueuealpha.QuotaFeasibleAndInsufficientTopology)
}

// HasConditionalRules returns whether the PreemptionConfig holds any rule of a
// trigger which is only reached once the preceding ones are not enough. It inspects
// the configuration only, and therefore lets callers skip the fit checks guarding
// the evaluation of those triggers.
func (p *PreemptionEvaluator) HasConditionalRules() bool {
	return p != nil &&
		p.HasRulesFor(kueuealpha.InsufficientQuota, kueuealpha.QuotaFeasibleAndInsufficientTopology)
}

// OrderedCandidates returns the candidates selected by the rules of the
// PreemptionConfig activated by the given trigger, ordered from the most to the least
// preferred one, or no candidate if the evaluator is nil.
// Only the candidates still admitted in the snapshot are returned, so a trigger
// evaluated after some workloads have been preempted never returns those again.
func (p *PreemptionEvaluator) OrderedCandidates(
	snapshot *schdcache.Snapshot,
	preemptor *workload.Info,
	frsNeedPreemption sets.Set[resources.FlavorResource],
	candidatesOrdering func(a, b *workload.Info) int,
	trigger kueuealpha.PreemptionConfigActivationTrigger,
) []*workload.Info {
	if p == nil {
		return nil
	}
	candidates, err := p.Candidates(snapshot, preemptor, frsNeedPreemption, trigger)
	if err != nil {
		p.log.Error(err, "Failed to get candidates for preemption", "trigger", trigger)
		return nil
	}
	slices.SortFunc(candidates, candidatesOrdering)
	return candidates
}

// FindCandidates evaluates candidates across applicable PreemptionConfig
// triggers and returns (fits, configurableTargets).
//
// Because candidates are removed from the snapshot as they are evaluated, subsequent
// fit checks observe the updated snapshot state, and the evaluator only returns
// candidates still admitted in the snapshot.
func (p *PreemptionEvaluator) FindCandidates(
	snapshot *schdcache.Snapshot,
	preemptor *workload.Info,
	frsNeedPreemption sets.Set[resources.FlavorResource],
	candidatesOrdering func(a, b *workload.Info) int,
	workloadQuotaFits func() bool,
	yield func(*common.Target) bool,
) (interrupted bool) {
	yield = common.YieldFromSnapshot(snapshot, yield)

	if !p.HasRules() {
		return
	}

	if iterateOverCandidates(
		snapshot,
		p.OrderedCandidates(snapshot, preemptor, frsNeedPreemption, candidatesOrdering, kueuealpha.Always),
		yield,
	) {
		return true
	}

	if !p.HasConditionalRules() {
		// We iterated over all initial candidates.
		// Without conditional rules no more candidates can be yielded.
		return
	}

	if !workloadQuotaFits() && iterateOverCandidates(
		snapshot,
		p.OrderedCandidates(snapshot, preemptor, frsNeedPreemption, candidatesOrdering, kueuealpha.InsufficientQuota),
		yield,
	) {
		return true
	}

	// The topology trigger requires a feasible quota, so it is only applied once
	// the quota fits while the workload still doesn't fit (meaning topology is
	// what keeps the workload out).
	if workloadQuotaFits() && iterateOverCandidates(
		snapshot,
		p.OrderedCandidates(snapshot, preemptor, frsNeedPreemption, candidatesOrdering, kueuealpha.QuotaFeasibleAndInsufficientTopology),
		yield,
	) {
		return true
	}

	return
}

func iterateOverCandidates(
	snapshot *schdcache.Snapshot,
	candidates []*workload.Info,
	yield func(*common.Target) bool,
) (interrupted bool) {
	for _, candidate := range candidates {
		if !yield(&common.Target{
			WorkloadInfo: candidate,
			Reason:       kueue.ConfigurablePreemptionReason,
			WorkloadCq:   snapshot.ClusterQueue(candidate.ClusterQueue),
		}) {
			return true
		}
	}
	return
}
