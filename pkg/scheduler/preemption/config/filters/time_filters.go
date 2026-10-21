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
	"time"

	"github.com/go-logr/logr"
	"k8s.io/utils/clock"
	"k8s.io/utils/ptr"

	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/workload"
)

type timeFilter struct {
	log                       logr.Logger
	clock                     clock.Clock
	selector                  *kueuealpha.PreemptionConfigPreemptionCandidateSelector
	preemptorExecutionTime    time.Duration
	preemptorTimeFromCreation time.Duration
}

func NewTimeFilter(log logr.Logger, clock clock.Clock, selector *kueuealpha.PreemptionConfigPreemptionCandidateSelector, preemptor *workload.Info) (WorkloadFilter, *FilterBuildError) {
	if selector.ExecutionTimeRelation != nil &&
		!isSupportedComparison(*selector.ExecutionTimeRelation) {
		return nil, &FilterBuildError{
			Filter: FilterTimeSelector,
			Reason: ReasonUnsupportedComparison,
			Err:    fmt.Errorf("unsupported execution time comparison %q", *selector.ExecutionTimeRelation),
		}
	}

	if selector.TimeFromCreationRelation != nil &&
		!isSupportedComparison(*selector.TimeFromCreationRelation) {
		return nil, &FilterBuildError{
			Filter: FilterTimeSelector,
			Reason: ReasonUnsupportedComparison,
			Err:    fmt.Errorf("unsupported creation relation comparison %q", *selector.TimeFromCreationRelation),
		}
	}

	return &timeFilter{
		log:                       log,
		clock:                     clock,
		selector:                  selector,
		preemptorExecutionTime:    cumulativeExecutionTime(preemptor.Obj, clock),
		preemptorTimeFromCreation: clock.Since(preemptor.Obj.CreationTimestamp.Time),
	}, nil
}

// cumulativeExecutionTime returns accumulated time for current and  previous executions for workloads.
// This function implemeted to handle non-finished workloads as it is part of preemption logic.
func cumulativeExecutionTime(wl *kueue.Workload, clock clock.Clock) time.Duration {
	pastExecutionSeconds := ptr.Deref(wl.Status.AccumulatedPastExecutionTimeSeconds, 0)
	pastExecutionDuration := time.Duration(pastExecutionSeconds) * time.Second

	admittedCond := meta.FindStatusCondition(wl.Status.Conditions, kueue.WorkloadAdmitted)
	if admittedCond == nil || admittedCond.Status != metav1.ConditionTrue {
		return pastExecutionDuration
	}

	currentCycle := clock.Since(admittedCond.LastTransitionTime.Time)
	return pastExecutionDuration + currentCycle
}

func (t *timeFilter) Matches(wl *workload.Info) bool {
	executionTime := cumulativeExecutionTime(wl.Obj, t.clock)
	if t.selector.MinExecutionSeconds != nil && executionTime < secondsToDuration(*t.selector.MinExecutionSeconds) {
		return false
	}

	if t.selector.MaxExecutionSeconds != nil && executionTime > secondsToDuration(*t.selector.MaxExecutionSeconds) {
		return false
	}

	if !matchesComparison(t.log, t.selector.ExecutionTimeRelation, executionTime, t.preemptorExecutionTime) {
		return false
	}

	timeFromCreationTime := t.clock.Since(wl.Obj.CreationTimestamp.Time)
	if t.selector.MinTimeFromCreationSeconds != nil && timeFromCreationTime < secondsToDuration(*t.selector.MinTimeFromCreationSeconds) {
		return false
	}

	if t.selector.MaxTimeFromCreationSeconds != nil && timeFromCreationTime > secondsToDuration(*t.selector.MaxTimeFromCreationSeconds) {
		return false
	}

	if !matchesComparison(t.log, t.selector.TimeFromCreationRelation, timeFromCreationTime, t.preemptorTimeFromCreation) {
		return false
	}

	return true
}

func secondsToDuration(seconds int32) time.Duration {
	return time.Duration(seconds) * time.Second
}
