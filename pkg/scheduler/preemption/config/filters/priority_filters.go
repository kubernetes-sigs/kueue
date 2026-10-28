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

type priorityFilter struct {
	log               logr.Logger
	mode              kueuealpha.PreemptionConfigPriorityMode
	comparison        kueuealpha.NumericComparison
	priorityFn        priorityGetter
	preemptorPriority int64
}

// NewPriorityFilter creates a WorkloadFilter to evaluate candidate workloads
// based on the priority constraint compared against the preemptor workload.
func NewPriorityFilter(log logr.Logger, constraint kueuealpha.PreemptionConfigPriorityConstraint, preemptor *workload.Info) (WorkloadFilter, *FilterBuildError) {
	if !isSupportedComparison(constraint.Comparison) {
		return nil, &FilterBuildError{
			Filter: FilterPriority,
			Reason: ReasonUnsupportedComparison,
			Err:    fmt.Errorf("unsupported comparison %q", constraint.Comparison),
		}
	}

	filterLog := log.WithValues("filter", "Priority", "mode", constraint.Mode, "comparison", constraint.Comparison)
	preemptorLog := filterLog.WithValues("preemptor", klog.KObj(preemptor.Obj))

	var priorityFn priorityGetter
	switch constraint.Mode {
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
			Err:    fmt.Errorf("unsupported mode %q", constraint.Mode),
		}
	}

	preemptorPriority := priorityFn(preemptorLog, preemptor)

	return &priorityFilter{
		log:               filterLog,
		mode:              constraint.Mode,
		comparison:        constraint.Comparison,
		priorityFn:        priorityFn,
		preemptorPriority: preemptorPriority,
	}, nil
}

// Matches evaluates a candidate workload's priority against the preemptor's priority.
func (f *priorityFilter) Matches(wl *workload.Info) bool {
	candLog := f.log.WithValues("candidate", klog.KObj(wl.Obj))
	candPriority := f.priorityFn(candLog, wl)
	return matchesComparison(candLog, &f.comparison, candPriority, f.preemptorPriority)
}

type priorityClassFilter struct {
	selector kueuealpha.PreemptionConfigPriorityClassSelector
}

func isSupportedPriorityClassOperator(operator kueuealpha.PriorityClassSelectorOperator) bool {
	switch operator {
	case kueuealpha.PriorityClassSelectorOpIn, kueuealpha.PriorityClassSelectorOpNotIn:
		return true
	default:
		return false
	}
}

// NewPriorityClassFilter creates a WorkloadFilter to evaluate candidate workloads
// based on their priority class.
func NewPriorityClassFilter(selector kueuealpha.PreemptionConfigPriorityClassSelector) (WorkloadFilter, *FilterBuildError) {
	for _, req := range selector.NameExpressions {
		if !isSupportedPriorityClassOperator(req.Operator) {
			return nil, &FilterBuildError{
				Filter: FilterPriorityClassSelector,
				Reason: ReasonUnsupportedOperator,
				Err:    fmt.Errorf("unsupported operator %q", req.Operator),
			}
		}
	}
	return &priorityClassFilter{
		selector: selector,
	}, nil
}

func isEmptyPriorityClassSelector(selector *kueuealpha.PreemptionConfigPriorityClassSelector) bool {
	return selector == nil || (selector.SourceRef == nil && len(selector.NameExpressions) == 0)
}

// Matches evaluates a candidate workload against the priority class selector.
func (f *priorityClassFilter) Matches(wl *workload.Info) bool {
	if f.selector.SourceRef != nil {
		ref := wl.Obj.Spec.PriorityClassRef
		if ref == nil || string(ref.Group) != string(f.selector.SourceRef.Group) || string(ref.Kind) != string(f.selector.SourceRef.Kind) {
			return false
		}
	}
	name := patching.PriorityClassName(wl.Obj)
	for _, req := range f.selector.NameExpressions {
		switch req.Operator {
		case kueuealpha.PriorityClassSelectorOpIn:
			if name == "" || !slices.Contains(req.Values, name) {
				return false
			}
		case kueuealpha.PriorityClassSelectorOpNotIn:
			if name != "" && slices.Contains(req.Values, name) {
				return false
			}
		default:
			return false
		}
	}
	return true
}
