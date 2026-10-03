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
	"errors"
	"fmt"
	"slices"

	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/utils/clock"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/resources"
	"sigs.k8s.io/kueue/pkg/scheduler/preemption/classical"
	"sigs.k8s.io/kueue/pkg/scheduler/preemption/config/filters"
	"sigs.k8s.io/kueue/pkg/scheduler/preemption/preemptionpolicy"
	"sigs.k8s.io/kueue/pkg/workload"
)

// PreemptionEvaluator selects the preemption candidates according to the configuration.
//
// FindCandidates is its entry point, yielding the candidates selected by the rules
// of the PreemptionConfig, while HasRules lets callers check upfront whether it may
// yield any candidate at all. A nil PreemptionEvaluator holds no rule, and therefore
// never yields any candidate.
type PreemptionEvaluator struct {
	ctx    context.Context
	log    logr.Logger
	clock  clock.Clock
	config kueuealpha.PreemptionConfig
	// candidatesOrdering orders the candidates of a trigger from the most to the
	// least preferred one.
	candidatesOrdering func(a, b *workload.Info) int
}

// NewPreemptionEvaluator returns the PreemptionEvaluator for the given PreemptionConfig,
// which yields the candidates of each trigger in the order defined by candidatesOrdering.
func NewPreemptionEvaluator(
	ctx context.Context,
	log logr.Logger,
	clock clock.Clock,
	config kueuealpha.PreemptionConfig,
	candidatesOrdering func(a, b *workload.Info) int,
) *PreemptionEvaluator {
	return &PreemptionEvaluator{
		ctx:                ctx,
		log:                log,
		clock:              clock,
		config:             config,
		candidatesOrdering: candidatesOrdering,
	}
}

// NewEvaluatorForPreemptionConfig returns the PreemptionEvaluator for the PreemptionConfig
// with the given name, or nil if it cannot be read.
func NewEvaluatorForPreemptionConfig(
	ctx context.Context,
	log logr.Logger,
	clock clock.Clock,
	cl client.Client,
	preemptionConfigName string,
	candidatesOrdering func(a, b *workload.Info) int,
) *PreemptionEvaluator {
	preemptionConfig := &kueuealpha.PreemptionConfig{}
	if err := cl.Get(ctx, client.ObjectKey{Name: preemptionConfigName}, preemptionConfig); err != nil {
		log.Error(err, "Failed to get PreemptionConfig", "preemptionConfigName", preemptionConfigName)
		return nil
	}
	return NewPreemptionEvaluator(ctx, log, clock, *preemptionConfig, candidatesOrdering)
}

// HasRules returns whether the PreemptionConfig holds any rule at all. It only
// inspects the configuration, without evaluating any candidate, so callers can
// cheaply tell whether FindCandidates may yield any candidate before running it.
func (p *PreemptionEvaluator) HasRules() bool {
	return p != nil &&
		p.hasRulesFor(kueuealpha.Always, kueuealpha.InsufficientQuota, kueuealpha.QuotaFeasibleAndInsufficientTopology)
}

// FindCandidates evaluates candidates across applicable PreemptionConfig
// triggers and yields them.
// Returns (interrupted = true) if at any point the yield method returns false.
// An evaluation error is logged and stops evaluation of subsequent triggers.
//
// Triggers are applied as a fallback in the order the API defines them: the Always
// trigger first, as a baseline, then InsufficientQuota while the quota is not sufficient,
// and finally QuotaFeasibleAndInsufficientTopology once the quota is sufficient but no
// topology assignment can be found.
//
// Because candidates are removed from the snapshot as they are evaluated, subsequent
// fit checks observe the updated snapshot state, and the evaluator only returns
// candidates still admitted in the snapshot.
func (p *PreemptionEvaluator) FindCandidates(
	snapshot *schdcache.Snapshot,
	preemptor *workload.Info,
	frsNeedPreemption sets.Set[resources.FlavorResource],
	workloadQuotaFits func() bool,
	yield func(*preemptionpolicy.Target) bool,
) (interrupted bool) {
	yield = preemptionpolicy.YieldFromSnapshot(snapshot, yield)

	if !p.HasRules() {
		return
	}

	candidates, err := p.orderedCandidates(snapshot, preemptor, frsNeedPreemption, kueuealpha.Always)
	if err != nil {
		return
	}
	if iterateOverCandidates(snapshot, candidates, yield) {
		return true
	}

	if !p.hasConditionalRules() {
		// We iterated over all initial candidates.
		// Without conditional rules no more candidates can be yielded.
		return
	}

	if !workloadQuotaFits() {
		candidates, err = p.orderedCandidates(snapshot, preemptor, frsNeedPreemption, kueuealpha.InsufficientQuota)
		if err != nil {
			return
		}
		if iterateOverCandidates(snapshot, candidates, yield) {
			return true
		}
	}

	// The topology trigger requires a feasible quota, so it is only applied once
	// the quota fits while the workload still doesn't fit (meaning topology is
	// what keeps the workload out).
	if workloadQuotaFits() {
		candidates, err = p.orderedCandidates(snapshot, preemptor, frsNeedPreemption, kueuealpha.QuotaFeasibleAndInsufficientTopology)
		if err != nil {
			return
		}
		if iterateOverCandidates(snapshot, candidates, yield) {
			return true
		}
	}

	return
}

func iterateOverCandidates(
	snapshot *schdcache.Snapshot,
	candidates []*configurableCandidate,
	yield func(*preemptionpolicy.Target) bool,
) (interrupted bool) {
	for _, candidate := range candidates {
		if !yield(&preemptionpolicy.Target{
			WorkloadInfo: candidate.WlInfo,
			Reason:       kueue.ConfigurablePreemptionReason,
			WorkloadCq:   snapshot.ClusterQueue(candidate.WlInfo.ClusterQueue),
			ConfigurablePreemptionReasonData: &preemptionpolicy.ConfigurablePreemptionReasonData{
				ConfigName:                candidate.ConfigName,
				RuleNameToSelectorIndexes: candidate.RuleNameToSelectorIndexes,
			},
		}) {
			return true
		}
	}
	return
}

// hasRulesFor returns whether any rule of the PreemptionConfig is activated by one of
// the given triggers. It only inspects the configuration, never the snapshot, and is
// therefore cheap enough to guard the candidate evaluation.
func (p *PreemptionEvaluator) hasRulesFor(triggers ...kueuealpha.PreemptionConfigActivationTrigger) bool {
	for _, rule := range p.config.Spec.Rules {
		if slices.Contains(triggers, rule.ActivationPolicy.Trigger) {
			return true
		}
	}
	return false
}

// hasConditionalRules returns whether the PreemptionConfig holds any rule of a
// trigger which is only reached once the preceding ones are not enough. It inspects
// the configuration only, and therefore lets FindCandidates skip the fit checks
// guarding the evaluation of those triggers.
func (p *PreemptionEvaluator) hasConditionalRules() bool {
	return p.hasRulesFor(kueuealpha.InsufficientQuota, kueuealpha.QuotaFeasibleAndInsufficientTopology)
}

// configurableCandidate represents a workload selected for configurable preemption
// together with the configuration rules and selectors that selected it.
type configurableCandidate struct {
	WlInfo                    *workload.Info
	ConfigName                preemptionpolicy.PreemptionConfigReference
	RuleNameToSelectorIndexes map[preemptionpolicy.PreemptionConfigRuleReference][]int
}

// orderedCandidates returns the candidates selected by the rules of the
// PreemptionConfig activated by the given trigger, ordered from the most to the least
// preferred one.
// Only the candidates still admitted in the snapshot are returned, so a trigger
// evaluated after some workloads have been preempted never returns those again.
// Returns an error if a matching rule cannot be evaluated.
func (p *PreemptionEvaluator) orderedCandidates(
	snapshot *schdcache.Snapshot,
	preemptor *workload.Info,
	frsNeedPreemption sets.Set[resources.FlavorResource],
	trigger kueuealpha.PreemptionConfigActivationTrigger,
) ([]*configurableCandidate, error) {
	candidates, err := p.candidatesFor(snapshot, preemptor, frsNeedPreemption, trigger)
	if err != nil {
		p.log.Error(err, "Failed to get candidates for preemption", "trigger", trigger)
		return nil, err
	}
	slices.SortFunc(candidates, func(a, b *configurableCandidate) int {
		return p.candidatesOrdering(a.WlInfo, b.WlInfo)
	})
	return candidates, nil
}

// candidatesFor returns the workloads selected as preemption candidates by the rules of the
// PreemptionConfig activated by the given trigger, deduplicated across the rules and
// selectors of the trigger.
func (p *PreemptionEvaluator) candidatesFor(
	snapshot *schdcache.Snapshot,
	preemptor *workload.Info,
	flavorsNeedPreemption sets.Set[resources.FlavorResource],
	trigger kueuealpha.PreemptionConfigActivationTrigger,
) ([]*configurableCandidate, error) {
	var (
		candidates []*configurableCandidate
		errs       []error
	)
	// Several rules, or several selectors of a rule, can select the same workload.
	// Therefore, we need to keep track of the UIDs of the selected workloads
	// to avoid duplicates. Additionally map's value is used as index of already recorded candidate
	// for effective look up to add another selector's index in case of another match.
	seen := map[types.UID]int{}
	for _, rule := range p.config.Spec.Rules {
		if rule.ActivationPolicy.Trigger != trigger {
			continue
		}
		matches, err := workloadMatchesSelector(rule.PreemptorSelector, preemptor)
		if err != nil {
			errs = append(errs, fmt.Errorf("preemptionConfig %q rule %q: %w", p.config.Name, rule.Name, err))
			continue
		}
		if !matches {
			continue
		}

		for selectorIndex, selector := range rule.CandidateSelectors {
			filter, buildErrs := filters.NewCandidateFilters(p.log, &selector, preemptor, snapshot)
			if len(buildErrs) > 0 {
				for _, bErr := range buildErrs {
					errs = append(errs, fmt.Errorf("preemptionConfig %q rule %q candidateSelectors[%d]: %w", p.config.Name, rule.Name, selectorIndex, bErr))
				}
				continue
			}

			ruleReference := preemptionpolicy.PreemptionConfigRuleReference(rule.Name)
			p.addMatchingCandidates(&filter, snapshot, flavorsNeedPreemption, ruleReference, seen, &candidates, selectorIndex)
		}
	}

	if len(errs) > 0 {
		return nil, errors.Join(errs...)
	}

	return candidates, nil
}

func (p *PreemptionEvaluator) addMatchingCandidates(
	filter *filters.CandidateFilters,
	snapshot *schdcache.Snapshot,
	flavorsNeedPreemption sets.Set[resources.FlavorResource],
	ruleReference preemptionpolicy.PreemptionConfigRuleReference,
	seen map[types.UID]int,
	candidates *[]*configurableCandidate,
	selectorIndex int,
) {
	for _, targetCq := range snapshot.ClusterQueues() {
		if !matchesClusterQueue(filter, targetCq) {
			continue
		}

		for _, wlInfo := range targetCq.Workloads {
			if matchesWorkload(filter, wlInfo) && classical.WorkloadUsesResources(wlInfo, flavorsNeedPreemption) {
				candidate := p.ensureCandidate(seen, candidates, wlInfo)

				indexes := candidate.RuleNameToSelectorIndexes[ruleReference]
				candidate.RuleNameToSelectorIndexes[ruleReference] = append(indexes, selectorIndex)
			}
		}
	}
}

func (p *PreemptionEvaluator) ensureCandidate(
	seen map[types.UID]int,
	candidates *[]*configurableCandidate,
	wlInfo *workload.Info,
) *configurableCandidate {
	if existingIndex, found := seen[wlInfo.Obj.UID]; found {
		return (*candidates)[existingIndex]
	}

	seen[wlInfo.Obj.UID] = len(*candidates)
	candidate := &configurableCandidate{
		WlInfo:                    wlInfo,
		ConfigName:                preemptionpolicy.PreemptionConfigReference(p.config.Name),
		RuleNameToSelectorIndexes: map[preemptionpolicy.PreemptionConfigRuleReference][]int{},
	}

	*candidates = append(*candidates, candidate)
	return candidate
}

func matchesClusterQueue(filter *filters.CandidateFilters, cq *schdcache.ClusterQueueSnapshot) bool {
	for _, cqFilter := range filter.CQFilters {
		if !cqFilter.Matches(cq) {
			return false
		}
	}
	return true
}

func matchesWorkload(filter *filters.CandidateFilters, wl *workload.Info) bool {
	for _, wlFilter := range filter.WLFilters {
		if !wlFilter.Matches(wl) {
			return false
		}
	}
	return true
}

// workloadMatchesSelector returns whether the labels of the workload match the
// selector. A nil selector accepts every workload, which differs from
// LabelSelectorAsSelector(nil), matching none.
func workloadMatchesSelector(selector *metav1.LabelSelector, wlInfo *workload.Info) (bool, *filters.FilterBuildError) {
	if selector == nil {
		return true, nil
	}
	labelSelector, err := metav1.LabelSelectorAsSelector(selector)
	if err != nil {
		return false, &filters.FilterBuildError{
			Filter: filters.FilterPreemptorSelector,
			Reason: filters.ReasonInvalidSelector,
			Err:    err,
		}
	}

	return labelSelector.Matches(labels.Set(wlInfo.Obj.Labels)), nil
}
