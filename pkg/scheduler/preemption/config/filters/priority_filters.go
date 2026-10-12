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
	"slices"

	"github.com/go-logr/logr"
	"k8s.io/klog/v2"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	"sigs.k8s.io/kueue/pkg/util/priority"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/pkg/workload/patching"
)

type priorityGetter func(log logr.Logger, wl *workload.Info) int64

type priorityComparisonFilter struct {
	log               logr.Logger
	comparison        kueuealpha.NumericComparison
	priorityFn        priorityGetter
	preemptorPriority int64
}

// NewPriorityComparisonFilter creates a WorkloadFilter to evaluate candidate workloads
// based on the priority mode and comparison against the preemptor workload.
func NewPriorityComparisonFilter(log logr.Logger, mode kueuealpha.PreemptionConfigPriorityMode, comparison kueuealpha.NumericComparison, preemptor *workload.Info) (WorkloadFilter, *FilterBuildError) {
	if !isSupportedComparison(comparison) {
		return nil, &FilterBuildError{
			Filter: FilterPriority,
			Reason: ReasonUnsupportedComparison,
			Err:    fmt.Errorf("unsupported comparison %q", comparison),
		}
	}

	filterLog := log.WithValues("filter", "Priority", "mode", mode, "comparison", comparison)
	preemptorLog := filterLog.WithValues("preemptor", klog.KObj(preemptor.Obj))

	var priorityFn priorityGetter
	switch mode {
	case kueuealpha.Base:
		priorityFn = func(_ logr.Logger, wl *workload.Info) int64 {
			return int64(priority.Priority(wl.Obj))
		}
	case kueuealpha.Boosted:
		priorityFn = func(log logr.Logger, wl *workload.Info) int64 {
			return priority.EffectivePriority(log, wl.Obj)
		}
	default:
		return nil, &FilterBuildError{
			Filter: FilterPriority,
			Reason: ReasonUnsupportedMode,
			Err:    fmt.Errorf("unsupported mode %q", mode),
		}
	}

	preemptorPriority := priorityFn(preemptorLog, preemptor)

	return &priorityComparisonFilter{
		log:               filterLog,
		comparison:        comparison,
		priorityFn:        priorityFn,
		preemptorPriority: preemptorPriority,
	}, nil
}

// Matches evaluates a candidate workload's priority against the preemptor's priority.
func (f *priorityComparisonFilter) Matches(wl *workload.Info) bool {
	candLog := f.log.WithValues("candidate", klog.KObj(wl.Obj))
	candPriority := f.priorityFn(candLog, wl)
	return matchesComparison(candLog, &f.comparison, candPriority, f.preemptorPriority)
}

type priorityClassFilter struct {
	selector kueuealpha.PreemptionConfigPriorityClassSelector
}

// NewPriorityClassFilter creates a WorkloadFilter to evaluate candidate workloads
// based on their priority class name.
func NewPriorityClassFilter(selector kueuealpha.PreemptionConfigPriorityClassSelector) WorkloadFilter {
	return &priorityClassFilter{
		selector: selector,
	}
}

// Matches evaluates a candidate workload against the priority class selector.
func (f *priorityClassFilter) Matches(wl *workload.Info) bool {
	return MatchesPriorityClassSelector(&f.selector, wl)
}

// MatchesPriorityClassSelector evaluates whether a workload's spec.priorityClassRef.name
// matches the given PreemptionConfigPriorityClassSelector.
func MatchesPriorityClassSelector(selector *kueuealpha.PreemptionConfigPriorityClassSelector, wl *workload.Info) bool {
	if selector == nil {
		return true
	}
	name := patching.PriorityClassName(wl.Obj)
	if len(selector.MatchNames) > 0 && !slices.Contains(selector.MatchNames, name) {
		return false
	}
	return !slices.Contains(selector.NotMatchNames, name)
}
