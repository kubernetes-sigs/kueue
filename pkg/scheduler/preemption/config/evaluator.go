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

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/resources"
	"sigs.k8s.io/kueue/pkg/scheduler/preemption/classical"
	"sigs.k8s.io/kueue/pkg/scheduler/preemption/config/filters"
	"sigs.k8s.io/kueue/pkg/workload"
)

// PreemptionEvaluator selects the preemption candidates according to the configuration.
type PreemptionEvaluator struct {
	ctx    context.Context
	log    logr.Logger
	clock  clock.Clock
	config kueuealpha.PreemptionConfig
}

func NewPreemptionEvaluator(
	ctx context.Context,
	log logr.Logger,
	clock clock.Clock,
	config kueuealpha.PreemptionConfig,
) *PreemptionEvaluator {
	return &PreemptionEvaluator{
		ctx:    ctx,
		log:    log,
		clock:  clock,
		config: config,
	}
}

// HasRulesFor returns whether any rule of the PreemptionConfig is activated by one of
// the given triggers. It only inspects the configuration, never the snapshot, and is
// therefore cheap enough to guard the candidate evaluation.
func (p *PreemptionEvaluator) HasRulesFor(triggers ...kueuealpha.PreemptionConfigActivationTrigger) bool {
	for _, rule := range p.config.Spec.Rules {
		if slices.Contains(triggers, rule.ActivationPolicy.Trigger) {
			return true
		}
	}
	return false
}

// configurableCandidate represents a workload selected for configurable preemption
// together with the configuration rules and selectors that selected it.
type configurableCandidate struct {
	WlInfo                    *workload.Info
	ConfigName                string
	RuleNameToSelectorIndexes map[string][]int
}

// Candidates returns the workloads selected as preemption candidates by the rules of the
// PreemptionConfig activated by the given trigger, deduplicated across the rules and
// selectors of the trigger.
func (p *PreemptionEvaluator) Candidates(
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
		matches, err := matchesPreemptor(&rule, preemptor)
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

			p.addMatchingCandidates(&filter, snapshot, flavorsNeedPreemption, rule.Name, seen, &candidates, selectorIndex)
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
	ruleName string,
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

				candidate.RuleNameToSelectorIndexes[ruleName] = append(candidate.RuleNameToSelectorIndexes[ruleName], selectorIndex)
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
		ConfigName:                p.config.Name,
		RuleNameToSelectorIndexes: map[string][]int{},
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

func matchesPreemptor(rule *kueuealpha.PreemptionConfigPreemptionRule, preemptor *workload.Info) (bool, *filters.FilterBuildError) {
	matches, err := workloadMatchesSelector(rule.PreemptorSelector, preemptor)
	if err != nil || !matches {
		return false, err
	}
	return filters.MatchesPriorityClassSelector(rule.PreemptorPriorityClassSelector, preemptor), nil
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
