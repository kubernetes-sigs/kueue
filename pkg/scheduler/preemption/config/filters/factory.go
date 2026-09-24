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
	"github.com/go-logr/logr"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/workload"
)

// NewCandidateFilters compiles PreemptionConfigPreemptionCandidateSelector rules into CandidateFilters & RejectAll boolean (if preemptor doesn't pass).
// It returns (CandidateFilters{}, true) if the selector fails to compile and all the candidates should be rejected.
func NewCandidateFilters(
	log logr.Logger,
	selector *kueuealpha.PreemptionConfigPreemptionCandidateSelector,
	preemptor *workload.Info,
	snapshot *schdcache.Snapshot,
) (CandidateFilters, bool) {
	if selector == nil {
		return CandidateFilters{}, false
	}

	cqScopeFilters, wlScopeFilters, ok := buildScopeFilters(log, selector.Scope, preemptor, snapshot)
	if !ok {
		return CandidateFilters{}, true
	}
	cqLabelFilter, ok := buildClusterQueueLabelFilter(log, selector.ClusterQueueSelector)
	if !ok {
		return CandidateFilters{}, true
	}
	wlLabelFilter, ok := buildWorkloadLabelFilter(log, selector.LabelSelector)
	if !ok {
		return CandidateFilters{}, true
	}
	wlNumericFilters := buildNumericLabelFilters(log, selector.NumericLabels, preemptor)
	wlPriorityFilter, ok := buildPriorityFilter(log, selector.Priority, preemptor)
	if !ok {
		return CandidateFilters{}, true
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
	}, false
}

func buildScopeFilters(
	log logr.Logger,
	scope kueuealpha.PreemptionConfigPreemptionQueueScope,
	preemptor *workload.Info,
	snapshot *schdcache.Snapshot,
) ([]ClusterQueueFilter, []WorkloadFilter, bool) {
	switch scope {
	case kueuealpha.WithinLocalQueue:
		// CQ Level: Prune all other ClusterQueues
		// WL Level: Narrow down workloads to those matching exactly same LocalQueue
		return []ClusterQueueFilter{NewWithinClusterQueueFilter(preemptor.ClusterQueue)},
			[]WorkloadFilter{NewWithinLocalQueueFilter(preemptor.Obj.Namespace, preemptor.Obj.Spec.QueueName)}, true

	case kueuealpha.WithinClusterQueue:
		return []ClusterQueueFilter{NewWithinClusterQueueFilter(preemptor.ClusterQueue)}, nil, true

	case kueuealpha.WithinParentCohort:
		return []ClusterQueueFilter{NewWithinParentCohortFilter(preemptor.ClusterQueue, snapshot)}, nil, true

	case kueuealpha.WithinCohortTree:
		return []ClusterQueueFilter{NewWithinCohortTreeFilter(preemptor.ClusterQueue, snapshot)}, nil, true

	case kueuealpha.AnyClusterQueue:
		return nil, nil, true

	default:
		log.V(3).Info("Unsupported or unhandled candidate scope evaluated; 0 candidates permitted", "scope", scope)
		return nil, nil, false
	}
}

func buildNumericLabelFilters(
	log logr.Logger,
	labels []kueuealpha.PreemptionConfigNumericLabelConstraint,
	preemptor *workload.Info,
) []WorkloadFilter {
	if len(labels) == 0 {
		return nil
	}
	filters := make([]WorkloadFilter, 0, len(labels))
	for _, numConstraint := range labels {
		filters = append(filters, NewNumericLabelFilter(log, numConstraint, preemptor))
	}
	return filters
}

func buildWorkloadLabelFilter(
	log logr.Logger,
	selector *metav1.LabelSelector,
) (WorkloadFilter, bool) {
	if selector == nil {
		return nil, true
	}
	ls, err := metav1.LabelSelectorAsSelector(selector)
	if err != nil {
		log.V(3).Info("Invalid LabelSelector", "error", err, "selector", selector)
		return nil, false
	}
	if ls.Empty() {
		return nil, true
	}
	return NewWorkloadLabelFilter(ls), true
}

func buildPriorityFilter(
	log logr.Logger,
	priority *kueuealpha.PreemptionConfigPriorityConstraint,
	preemptor *workload.Info,
) (WorkloadFilter, bool) {
	if priority == nil {
		return nil, true
	}
	return NewPriorityFilter(log, *priority, preemptor)
}

func buildClusterQueueLabelFilter(
	log logr.Logger,
	selector *metav1.LabelSelector,
) (ClusterQueueFilter, bool) {
	if selector == nil {
		return nil, true
	}
	ls, err := metav1.LabelSelectorAsSelector(selector)
	if err != nil {
		log.V(3).Info("Invalid ClusterQueueSelector", "error", err, "selector", selector)
		return nil, false
	}
	if ls.Empty() {
		return nil, true
	}
	return newClusterQueueLabelFilter(ls), true
}
