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
	"time"

	"github.com/go-logr/logr"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/klog/v2"
	"k8s.io/utils/ptr"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/workload"
)

type executionFilter struct {
	log        logr.Logger
	now        time.Time
	constraint kueuealpha.PreemptionConfigDurationConstraint

	// precomputed fields
	preemptorExecutionDuration time.Duration
}

func NewExecutionFilter(log logr.Logger, now time.Time, constraint kueuealpha.PreemptionConfigDurationConstraint, preemptor *workload.Info) (WorkloadTimeFilter, *FilterBuildError) {
	if constraint.Comparison != nil && !isSupportedComparison(*constraint.Comparison) {
		return nil, &FilterBuildError{
			Filter: FilterTimeConstraintsExecution,
			Reason: ReasonUnsupportedComparison,
			Err:    fmt.Errorf("unsupported execution time comparison %q", *constraint.Comparison),
		}
	}

	filterLog := log.WithValues("filter", "Execution",
		"now", now,
		"preemptor", klog.KObj(preemptor.Obj))

	filter := &executionFilter{
		log:                        filterLog,
		now:                        now,
		constraint:                 constraint,
		preemptorExecutionDuration: cumulativeExecutionDuration(preemptor.Obj, now),
	}

	return filter, nil
}

func (f *executionFilter) Matches(wl *workload.Info) (*time.Time, bool) {
	executionDuration := cumulativeExecutionDuration(wl.Obj, f.now)
	if f.constraint.MaxSeconds != nil && executionDuration > secondsToDuration(*f.constraint.MaxSeconds) {
		// Returns immediately as there is no time in the future when this condition is satisfied.
		return nil, false
	}

	match := true
	var retryDurations []time.Duration

	if f.constraint.MinSeconds != nil && executionDuration < secondsToDuration(*f.constraint.MinSeconds) {
		match = false
		retryDurations = append(retryDurations, secondsToDuration(*f.constraint.MinSeconds)-executionDuration)
	}

	if !matchesComparison(f.log, f.constraint.Comparison, executionDuration, f.preemptorExecutionDuration) {
		match = false
		switch *f.constraint.Comparison {
		// Retry when executionDuration of the candidate increases above preemptor executionDuration.
		// In the case when executionDuration for the candidate isn't increasing
		// (meaning Quota reserved but not Admitted yet), we still retry as candidate will be Admitted
		// any time soon in most cases.
		case kueuealpha.GreaterThan:
			retryDurations = append(retryDurations, f.preemptorExecutionDuration-executionDuration+time.Millisecond)
		case kueuealpha.GreaterThanOrEqual:
			retryDurations = append(retryDurations, f.preemptorExecutionDuration-executionDuration)
		case kueuealpha.LessThan, kueuealpha.LessThanOrEqual:
			// Returns immediately as there is no time in the future when this condition is satisfied.
			return nil, false
		}
	}

	if len(retryDurations) == 0 {
		return nil, match
	}

	retryTime := f.now.Add(slices.Max(retryDurations))
	return &retryTime, false
}

// cumulativeExecutionDuration returns accumulated duration for current and previous
// executions for workloads.
// This function is implemented to handle non-finished workloads as it is part of preemption logic.
func cumulativeExecutionDuration(wl *kueue.Workload, now time.Time) time.Duration {
	pastExecutionSeconds := ptr.Deref(wl.Status.AccumulatedPastExecutionTimeSeconds, 0)
	pastExecutionDuration := secondsToDuration(pastExecutionSeconds)

	admittedCond := meta.FindStatusCondition(wl.Status.Conditions, kueue.WorkloadAdmitted)
	if admittedCond == nil ||
		admittedCond.Status != metav1.ConditionTrue ||
		// Should not happen but added for safety
		admittedCond.LastTransitionTime.IsZero() {
		return pastExecutionDuration
	}

	durationSinceLastAdmitted := max(0, now.Sub(admittedCond.LastTransitionTime.Time))
	return pastExecutionDuration + durationSinceLastAdmitted
}

func secondsToDuration(seconds int32) time.Duration {
	return time.Duration(seconds) * time.Second
}

type sinceCreationFilter struct {
	log        logr.Logger
	now        time.Time
	constraint kueuealpha.PreemptionConfigDurationConstraint

	// precomputed fields
	preemptorDurationSinceCreation time.Duration
}

func NewSinceCreationFilter(log logr.Logger, now time.Time, constraint kueuealpha.PreemptionConfigDurationConstraint, preemptor *workload.Info) (WorkloadTimeFilter, *FilterBuildError) {
	if constraint.Comparison != nil && !isSupportedComparison(*constraint.Comparison) {
		return nil, &FilterBuildError{
			Filter: FilterTimeConstraintsSinceCreation,
			Reason: ReasonUnsupportedComparison,
			Err:    fmt.Errorf("unsupported \"SinceCreation\" comparison %q", *constraint.Comparison),
		}
	}

	filterLog := log.WithValues("filter", "SinceCreation",
		"now", now,
		"preemptor", klog.KObj(preemptor.Obj))

	filter := &sinceCreationFilter{
		log:                            filterLog,
		now:                            now,
		constraint:                     constraint,
		preemptorDurationSinceCreation: durationSinceCreation(preemptor.Obj, now),
	}

	return filter, nil
}

func (f *sinceCreationFilter) Matches(wl *workload.Info) (*time.Time, bool) {
	durationSinceCreation := durationSinceCreation(wl.Obj, f.now)
	if f.constraint.MaxSeconds != nil && durationSinceCreation > secondsToDuration(*f.constraint.MaxSeconds) {
		// Returns immediately as there is no time in the future when this condition is satisfied.
		return nil, false
	}
	if !matchesComparison(f.log, f.constraint.Comparison, durationSinceCreation, f.preemptorDurationSinceCreation) {
		// Returns immediately as there is no time in the future when this condition is satisfied
		// because both metrics are growing simultaneously.
		return nil, false
	}

	if f.constraint.MinSeconds != nil && durationSinceCreation < secondsToDuration(*f.constraint.MinSeconds) {
		retryTime := f.now.Add(secondsToDuration(*f.constraint.MinSeconds) - durationSinceCreation)
		return &retryTime, false
	}

	return nil, true
}

func durationSinceCreation(wl *kueue.Workload, now time.Time) time.Duration {
	return now.Sub(wl.CreationTimestamp.Time)
}
