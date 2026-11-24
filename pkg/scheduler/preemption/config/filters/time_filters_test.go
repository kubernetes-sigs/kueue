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
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingalpha "sigs.k8s.io/kueue/pkg/util/testing/v1alpha1"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/pkg/workload"
)

func TestNewExecutionFilter(t *testing.T) {
	tests := map[string]struct {
		constraint kueuealpha.PreemptionConfigDurationConstraint
		preemptor  *kueue.Workload
		wantErr    *FilterBuildError
	}{
		"valid constraint returns no error": {
			constraint: *utiltestingalpha.MakeDurationConstraint().
				Comparison(kueuealpha.LessThan).
				Obj(),
			preemptor: utiltestingapi.MakeWorkload("preemptor", "ns").Obj(),
		},
		"unsupported Execution comparison returns build error": {
			constraint: *utiltestingalpha.MakeDurationConstraint().
				Comparison(kueuealpha.NumericComparison("InvalidComparison")).
				Obj(),
			preemptor: utiltestingapi.MakeWorkload("preemptor", "ns").Obj(),
			wantErr: &FilterBuildError{
				Filter: FilterTimeConstraintsExecution,
				Reason: ReasonUnsupportedComparison,
			},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			_, log := utiltesting.ContextWithLog(t)
			workloadInfo := workload.NewInfo(log, tc.preemptor)

			_, gotErr := NewExecutionFilter(log, time.Now(), tc.constraint, workloadInfo)
			if diff := cmp.Diff(tc.wantErr, gotErr, cmpopts.EquateErrors()); diff != "" {
				t.Fatalf("NewExecutionFilter() error mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func TestExecutionFilterMatches(t *testing.T) {
	now := time.Now().Truncate(time.Second)

	workloadWithCumulativeExecution := func(duration time.Duration) *kueue.Workload {
		// Split one second for pastAdmittedTime and the rest to currentExecution
		currentExecution := max(0, duration-time.Second)
		pastAdmittedTime := duration - currentExecution

		return utiltestingapi.MakeWorkload("test-workload", "ns").
			Creation(now.Add(-duration)).
			PastAdmittedTime(int32(pastAdmittedTime.Seconds())).
			Condition(metav1.Condition{
				Type:               kueue.WorkloadAdmitted,
				Status:             metav1.ConditionTrue,
				LastTransitionTime: metav1.NewTime(now.Add(-currentExecution)),
			}).
			Obj()
	}

	workloadWithFirstExecution := func(duration time.Duration) *kueue.Workload {
		return utiltestingapi.MakeWorkload("test-workload", "ns").
			Creation(now.Add(-duration)).
			Condition(metav1.Condition{
				Type:               kueue.WorkloadAdmitted,
				Status:             metav1.ConditionTrue,
				LastTransitionTime: metav1.NewTime(now.Add(-duration)),
			}).
			Obj()
	}

	type TestCase struct {
		candidate   *kueue.Workload
		wantMatch   bool
		wantRetryAt *time.Time
	}

	tests := []struct {
		constraint kueuealpha.PreemptionConfigDurationConstraint
		preemptor  *kueue.Workload
		testCases  map[string]TestCase
	}{
		{
			constraint: *utiltestingalpha.MakeDurationConstraint().MinSeconds(10).Obj(),
			preemptor:  utiltestingapi.MakeWorkload("preemptor", "ns").Obj(),
			testCases: map[string]TestCase{
				"MinSeconds: Candidate with less cumulative execution duration": {
					candidate:   workloadWithCumulativeExecution(5 * time.Second),
					wantMatch:   false,
					wantRetryAt: new(now.Add(5 * time.Second)),
				},
				"MinSeconds: Candidate with equal cumulative execution duration": {
					candidate: workloadWithCumulativeExecution(10 * time.Second),
					wantMatch: true,
				},
				"MinSeconds: Candidate with more cumulative execution duration": {
					candidate: workloadWithCumulativeExecution(15 * time.Second),
					wantMatch: true,
				},
			},
		},
		{
			constraint: *utiltestingalpha.MakeDurationConstraint().MaxSeconds(10).Obj(),
			preemptor:  utiltestingapi.MakeWorkload("preemptor", "ns").Obj(),
			testCases: map[string]TestCase{
				"MaxSeconds: Candidate with less cumulative execution duration": {
					candidate: workloadWithCumulativeExecution(5 * time.Second),
					wantMatch: true,
				},
				"MaxSeconds: Candidate with equal cumulative execution duration": {
					candidate: workloadWithCumulativeExecution(10 * time.Second),
					wantMatch: true,
				},
				"MaxSeconds: Candidate with more cumulative execution duration": {
					candidate: workloadWithCumulativeExecution(15 * time.Second),
					wantMatch: false,
				},
			},
		},
		{
			constraint: *utiltestingalpha.MakeDurationConstraint().Comparison(kueuealpha.LessThan).Obj(),
			preemptor:  utiltestingapi.MakeWorkload("preemptor", "ns").PastAdmittedTime(10).Obj(),
			testCases: map[string]TestCase{
				"Comparison LessThan: Candidate with less cumulative execution duration": {
					candidate: workloadWithCumulativeExecution(5 * time.Second),
					wantMatch: true,
				},
				"Comparison LessThan: Candidate with equal cumulative execution duration": {
					candidate: workloadWithCumulativeExecution(10 * time.Second),
					wantMatch: false,
				},
				"Comparison LessThan: Candidate with more cumulative execution duration": {
					candidate: workloadWithCumulativeExecution(15 * time.Second),
					wantMatch: false,
				},
			},
		},
		{
			constraint: *utiltestingalpha.MakeDurationConstraint().Comparison(kueuealpha.LessThanOrEqual).Obj(),
			preemptor:  utiltestingapi.MakeWorkload("preemptor", "ns").PastAdmittedTime(10).Obj(),
			testCases: map[string]TestCase{
				"Comparison LessThanOrEqual: Candidate with less cumulative execution duration": {
					candidate: workloadWithCumulativeExecution(5 * time.Second),
					wantMatch: true,
				},
				"Comparison LessThanOrEqual: Candidate with equal cumulative execution duration": {
					candidate: workloadWithCumulativeExecution(10 * time.Second),
					wantMatch: true,
				},
				"Comparison LessThanOrEqual: Candidate with more cumulative execution duration": {
					candidate: workloadWithCumulativeExecution(15 * time.Second),
					wantMatch: false,
				},
			},
		},
		{
			constraint: *utiltestingalpha.MakeDurationConstraint().Comparison(kueuealpha.GreaterThan).Obj(),
			preemptor:  utiltestingapi.MakeWorkload("preemptor", "ns").PastAdmittedTime(10).Obj(),
			testCases: map[string]TestCase{
				"Comparison GreaterThan: Candidate with less cumulative execution duration": {
					candidate:   workloadWithCumulativeExecution(5 * time.Second),
					wantMatch:   false,
					wantRetryAt: new(now.Add(5*time.Second + time.Millisecond)),
				},
				// In the case when executionDuration for the candidate isn't increasing
				// (meaning Quota reserved but not Admitted yet), we still retry as candidate will be
				// Admitted any time soon in most cases.
				"Comparison GreaterThan: Not Admitted candidate retried on comparison": {
					candidate:   utiltestingapi.MakeWorkload("test-workload", "ns").PastAdmittedTime(5).Obj(),
					wantMatch:   false,
					wantRetryAt: new(now.Add(5*time.Second + time.Millisecond)),
				},
				"Comparison GreaterThan: Candidate with equal cumulative execution duration": {
					candidate:   workloadWithCumulativeExecution(10 * time.Second),
					wantMatch:   false,
					wantRetryAt: new(now.Add(time.Millisecond)),
				},
				"Comparison GreaterThan: Candidate with more cumulative execution duration": {
					candidate: workloadWithCumulativeExecution(15 * time.Second),
					wantMatch: true,
				},
			},
		},
		{
			constraint: *utiltestingalpha.MakeDurationConstraint().Comparison(kueuealpha.GreaterThanOrEqual).Obj(),
			preemptor:  utiltestingapi.MakeWorkload("preemptor", "ns").PastAdmittedTime(10).Obj(),
			testCases: map[string]TestCase{
				"Comparison GreaterThanOrEqual: Candidate with less cumulative execution duration": {
					candidate:   workloadWithCumulativeExecution(5 * time.Second),
					wantMatch:   false,
					wantRetryAt: new(now.Add(5 * time.Second)),
				},
				// In the case when executionDuration for the candidate isn't increasing
				// (meaning Quota reserved but not Admitted yet), we still retry as candidate will be
				// Admitted any time soon in most cases.
				"Comparison GreaterThanOrEqual: Not Admitted candidate retried on comparison": {
					candidate:   utiltestingapi.MakeWorkload("test-workload", "ns").PastAdmittedTime(5).Obj(),
					wantMatch:   false,
					wantRetryAt: new(now.Add(5 * time.Second)),
				},
				"Comparison GreaterThanOrEqual: Candidate with equal cumulative execution duration": {
					candidate: workloadWithCumulativeExecution(10 * time.Second),
					wantMatch: true,
				},
				"Comparison GreaterThanOrEqual: Candidate with more cumulative execution duration": {
					candidate: workloadWithCumulativeExecution(15 * time.Second),
					wantMatch: true,
				},
			},
		},
		{
			constraint: *utiltestingalpha.MakeDurationConstraint().
				MinSeconds(5).
				MaxSeconds(10).
				Comparison(kueuealpha.LessThan).
				Obj(),
			preemptor: utiltestingapi.MakeWorkload("preemptor", "ns").PastAdmittedTime(10).Obj(),
			testCases: map[string]TestCase{
				"Multiple constraints, all satisfied": {
					candidate: workloadWithCumulativeExecution(5 * time.Second),
					wantMatch: true,
				},
				"Multiple constraints, MinSeconds isn't satisfied": {
					candidate:   workloadWithCumulativeExecution(3 * time.Second),
					wantMatch:   false,
					wantRetryAt: new(now.Add(2 * time.Second)),
				},
				"Multiple constraints, MaxSeconds isn't satisfied": {
					candidate: workloadWithCumulativeExecution(15 * time.Second),
					wantMatch: false,
				},
				"Multiple constraints for candidate with first execution, all satisfied": {
					candidate: workloadWithFirstExecution(5 * time.Second),
					wantMatch: true,
				},
				"Multiple constraints for candidate with first execution, MinSeconds isn't satisfied": {
					candidate:   workloadWithFirstExecution(3 * time.Second),
					wantMatch:   false,
					wantRetryAt: new(now.Add(2 * time.Second)),
				},
				"Multiple constraints for candidate with first execution, MaxSeconds isn't satisfied": {
					candidate: workloadWithFirstExecution(15 * time.Second),
					wantMatch: false,
				},
			},
		},
		{
			constraint: *utiltestingalpha.MakeDurationConstraint().
				MinSeconds(10).
				Comparison(kueuealpha.LessThan).
				Obj(),
			preemptor: utiltestingapi.MakeWorkload("preemptor", "ns").PastAdmittedTime(5).Obj(),
			testCases: map[string]TestCase{
				"Comparison which cannot be satisfied invalidates potential retry from MinSeconds": {
					candidate: workloadWithCumulativeExecution(7 * time.Second),
					wantMatch: false,
				},
			},
		},
	}

	for _, group := range tests {
		for name, tc := range group.testCases {
			t.Run(name, func(t *testing.T) {
				_, log := utiltesting.ContextWithLog(t)

				preemptorWlInfo := workload.NewInfo(log, group.preemptor)
				filter, err := NewExecutionFilter(log, now, group.constraint, preemptorWlInfo)
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}

				wlInfo := workload.NewInfo(log, tc.candidate)
				gotRetryAt, gotMatch := filter.Matches(wlInfo)
				if gotMatch != tc.wantMatch {
					t.Errorf("Unexpected match = %v, want %v", gotMatch, tc.wantMatch)
				}
				if diff := cmp.Diff(tc.wantRetryAt, gotRetryAt); diff != "" {
					t.Errorf("Unexpected retryAt (-want +got):\n%s", diff)
				}
			})
		}
	}
}
