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
	"k8s.io/klog/v2"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	"sigs.k8s.io/kueue/pkg/util/priority"
	"sigs.k8s.io/kueue/pkg/workload"
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
