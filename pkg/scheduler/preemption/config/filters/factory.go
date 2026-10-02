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

package filters

import (
	"fmt"

	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/workload"
)

// NewCandidateFilters compiles PreemptionConfigPreemptionCandidateSelector rules into CandidateFilters.
// It returns a slice of errors identifying all filters that failed to build if compilation fails.
func NewCandidateFilters(
	log logr.Logger,
	selector *kueuealpha.PreemptionConfigPreemptionCandidateSelector,
	preemptor *workload.Info,
	snapshot *schdcache.Snapshot,
) (CandidateFilters, []*FilterBuildError) {
	if selector == nil {
		return CandidateFilters{}, nil
	}

	var errs []*FilterBuildError

	cqScopeFilters, wlScopeFilters, err := buildScopeFilters(selector.Scope, preemptor, snapshot)
	if err != nil {
		errs = append(errs, err)
	}
	cqLabelFilter, err := buildClusterQueueLabelFilter(selector.ClusterQueueSelector)
	if err != nil {
		errs = append(errs, err)
	}
	wlLabelFilter, err := buildWorkloadLabelFilter(selector.LabelSelector)
	if err != nil {
		errs = append(errs, err)
	}
	wlNumericFilters, nErrs := buildNumericLabelFilters(log, selector.NumericLabels, preemptor)
	if len(nErrs) > 0 {
		errs = append(errs, nErrs...)
	}
	wlPriorityFilter, err := buildPriorityFilter(log, selector.Priority, preemptor)
	if err != nil {
		errs = append(errs, err)
	}

	if len(errs) > 0 {
		return CandidateFilters{}, errs
	}

	var cqFilters []ClusterQueueFilter
	cqFilters = append(cqFilters, cqScopeFilters...)
	if cqLabelFilter != nil {
		cqFilters = append(cqFilters, cqLabelFilter)
	}

	var wlFilters []WorkloadFilter
	wlFilters = append(wlFilters, wlScopeFilters...)
	if wlLabelFilter != nil {
		wlFilters = append(wlFilters, wlLabelFilter)
	}
	wlFilters = append(wlFilters, wlNumericFilters...)
	if wlPriorityFilter != nil {
		wlFilters = append(wlFilters, wlPriorityFilter)
	}

	return CandidateFilters{
		CQFilters: cqFilters,
		WLFilters: wlFilters,
	}, nil
}

func buildScopeFilters(
	scope kueuealpha.PreemptionConfigPreemptionQueueScope,
	preemptor *workload.Info,
	snapshot *schdcache.Snapshot,
) ([]ClusterQueueFilter, []WorkloadFilter, *FilterBuildError) {
	switch scope {
	case kueuealpha.WithinLocalQueue:
		return []ClusterQueueFilter{NewWithinClusterQueueFilter(preemptor.ClusterQueue)},
			[]WorkloadFilter{NewWithinLocalQueueFilter(preemptor.Obj.Namespace, preemptor.Obj.Spec.QueueName)}, nil

	case kueuealpha.WithinClusterQueue:
		return []ClusterQueueFilter{NewWithinClusterQueueFilter(preemptor.ClusterQueue)}, nil, nil

	case kueuealpha.WithinParentCohort:
		return []ClusterQueueFilter{NewWithinParentCohortFilter(preemptor.ClusterQueue, snapshot)}, nil, nil

	case kueuealpha.WithinCohortTree:
		return []ClusterQueueFilter{NewWithinCohortTreeFilter(preemptor.ClusterQueue, snapshot)}, nil, nil

	case kueuealpha.AnyClusterQueue:
		return nil, nil, nil

	default:
		return nil, nil, &FilterBuildError{
			Filter: FilterScope,
			Reason: ReasonUnsupportedScope,
			Err:    fmt.Errorf("unsupported scope %q", scope),
		}
	}
}

func buildNumericLabelFilters(
	log logr.Logger,
	labels []kueuealpha.PreemptionConfigNumericLabelConstraint,
	preemptor *workload.Info,
) ([]WorkloadFilter, []*FilterBuildError) {
	if len(labels) == 0 {
		return nil, nil
	}
	var errs []*FilterBuildError
	filters := make([]WorkloadFilter, 0, len(labels))
	for _, numConstraint := range labels {
		if numConstraint.Comparison != nil && !isSupportedComparison(*numConstraint.Comparison) {
			errs = append(errs, &FilterBuildError{
				Filter: FilterNumericLabels,
				Reason: ReasonUnsupportedComparison,
				Err:    fmt.Errorf("unsupported comparison %q for key %q", *numConstraint.Comparison, numConstraint.Key),
			})
			continue
		}
		filters = append(filters, NewNumericLabelFilter(log, numConstraint, preemptor))
	}
	if len(errs) > 0 {
		return nil, errs
	}
	return filters, nil
}

func buildWorkloadLabelFilter(
	selector *metav1.LabelSelector,
) (WorkloadFilter, *FilterBuildError) {
	if selector == nil {
		return nil, nil
	}
	ls, err := metav1.LabelSelectorAsSelector(selector)
	if err != nil {
		return nil, &FilterBuildError{
			Filter: FilterLabelSelector,
			Reason: ReasonInvalidSelector,
			Err:    err,
		}
	}
	if ls.Empty() {
		return nil, nil
	}
	return NewWorkloadLabelFilter(ls), nil
}

func buildPriorityFilter(
	log logr.Logger,
	priority *kueuealpha.PreemptionConfigPriorityConstraint,
	preemptor *workload.Info,
) (WorkloadFilter, *FilterBuildError) {
	if priority == nil || (priority.Mode == nil && priority.Comparison == nil && len(priority.MatchNames) == 0 && len(priority.NotMatchNames) == 0) {
		return nil, nil
	}
	return NewPriorityFilter(log, *priority, preemptor)
}

func buildClusterQueueLabelFilter(
	selector *metav1.LabelSelector,
) (ClusterQueueFilter, *FilterBuildError) {
	if selector == nil {
		return nil, nil
	}
	ls, err := metav1.LabelSelectorAsSelector(selector)
	if err != nil {
		return nil, &FilterBuildError{
			Filter: FilterClusterQueueSelector,
			Reason: ReasonInvalidSelector,
			Err:    err,
		}
	}
	if ls.Empty() {
		return nil, nil
	}
	return newClusterQueueLabelFilter(ls), nil
}
