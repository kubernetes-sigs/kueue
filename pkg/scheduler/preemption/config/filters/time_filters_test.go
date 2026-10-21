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
	"k8s.io/utils/clock"
	testingclock "k8s.io/utils/clock/testing"
	"k8s.io/utils/ptr"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/pkg/workload"
)

func TestNewTimeFilterError(t *testing.T) {
	tests := map[string]struct {
		selector  *kueuealpha.PreemptionConfigPreemptionCandidateSelector
		preemptor *kueue.Workload
		wantErr   *FilterBuildError
	}{
		"unsupported ExecutionTimeRelation returns build error": {
			selector: &kueuealpha.PreemptionConfigPreemptionCandidateSelector{
				ExecutionTimeRelation: ptr.To(kueuealpha.NumericComparison("InvalidComparison")),
			},
			preemptor: utiltestingapi.MakeWorkload("preemptor-pending", "ns").Obj(),
			wantErr: &FilterBuildError{
				Filter: FilterTimeSelector,
				Reason: ReasonUnsupportedComparison,
			},
		},
		"unsupported TimeFromCreationRelation returns build error": {
			selector: &kueuealpha.PreemptionConfigPreemptionCandidateSelector{
				TimeFromCreationRelation: ptr.To(kueuealpha.NumericComparison("InvalidComparison")),
			},
			preemptor: utiltestingapi.MakeWorkload("preemptor-pending", "ns").Obj(),
			wantErr: &FilterBuildError{
				Filter: FilterTimeSelector,
				Reason: ReasonUnsupportedComparison,
			},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			_, log := utiltesting.ContextWithLog(t)
			workloadInfo := workload.NewInfo(log, tc.preemptor)

			_, gotErr := NewTimeFilter(log, clock.RealClock{}, tc.selector, workloadInfo)
			if diff := cmp.Diff(tc.wantErr, gotErr, cmpopts.EquateErrors()); diff != "" {
				t.Fatalf("NewTimeFilter() error mismatch (-want +got):\n%s", diff)
			}
		})
	}
}

func Test_cumulativeExecutionTime(t *testing.T) {
	now := time.Now().Truncate(time.Second)

	tests := map[string]struct {
		wl   *kueue.Workload
		want time.Duration
	}{
		"no past execution time and not admitted returns zero": {
			wl:   utiltestingapi.MakeWorkload("wl", "ns").Obj(),
			want: 0,
		},
		"past execution time only and no admitted condition returns past execution duration": {
			wl: utiltestingapi.MakeWorkload("wl", "ns").
				PastAdmittedTime(120).
				Obj(),
			want: 2 * time.Minute,
		},
		"past execution time with Admitted=False condition ignores transition time": {
			wl: utiltestingapi.MakeWorkload("wl", "ns").
				PastAdmittedTime(120).
				Condition(metav1.Condition{
					Type:               kueue.WorkloadAdmitted,
					Status:             metav1.ConditionFalse,
					LastTransitionTime: metav1.NewTime(now.Add(-45 * time.Second)),
				}).
				Obj(),
			want: 2 * time.Minute,
		},
		"no past execution time with Admitted=True condition returns current cycle duration": {
			wl: utiltestingapi.MakeWorkload("wl", "ns").
				Condition(metav1.Condition{
					Type:               kueue.WorkloadAdmitted,
					Status:             metav1.ConditionTrue,
					LastTransitionTime: metav1.NewTime(now.Add(-45 * time.Second)),
				}).
				Obj(),
			want: 45 * time.Second,
		},
		"past execution time with Admitted=True condition returns sum of past and current cycle": {
			wl: utiltestingapi.MakeWorkload("wl", "ns").
				PastAdmittedTime(120).
				Condition(metav1.Condition{
					Type:               kueue.WorkloadAdmitted,
					Status:             metav1.ConditionTrue,
					LastTransitionTime: metav1.NewTime(now.Add(-45 * time.Second)),
				}).
				Obj(),
			want: 2*time.Minute + 45*time.Second,
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			fakeClock := testingclock.NewFakeClock(now)

			if got := cumulativeExecutionTime(tc.wl, fakeClock); got != tc.want {
				t.Errorf("cumulativeExecutionTime() = %v, want %v", got, tc.want)
			}
		})
	}
}

func Test_timeFilter_Matches(t *testing.T) {
	now := time.Now().Truncate(time.Second)

	creationTime := now.Add(-200 * time.Second)

	preemptor := utiltestingapi.MakeWorkload("preemptor", "ns").
		Creation(creationTime).
		Obj()

	candidate := utiltestingapi.MakeWorkload("candidate", "ns").
		Creation(creationTime).
		PastAdmittedTime(40).
		Condition(metav1.Condition{
			Type:               kueue.WorkloadAdmitted,
			Status:             metav1.ConditionTrue,
			LastTransitionTime: metav1.NewTime(now.Add(-60 * time.Second)),
		}).
		Obj()

	tests := map[string]struct {
		selector  *kueuealpha.PreemptionConfigPreemptionCandidateSelector
		preemptor *kueue.Workload
		wl        *kueue.Workload
		want      bool
	}{
		"empty selector matches any workload": {
			selector:  &kueuealpha.PreemptionConfigPreemptionCandidateSelector{},
			preemptor: preemptor,
			wl:        candidate,
			want:      true,
		},
		"MinExecutionSeconds: execution time below minimum is rejected": {
			selector: &kueuealpha.PreemptionConfigPreemptionCandidateSelector{
				MinExecutionSeconds: ptr.To[int32](101),
			},
			preemptor: preemptor,
			wl:        candidate,
			want:      false,
		},
		"MinExecutionSeconds: execution time equal to minimum matches": {
			selector: &kueuealpha.PreemptionConfigPreemptionCandidateSelector{
				MinExecutionSeconds: ptr.To[int32](100),
			},
			preemptor: preemptor,
			wl:        candidate,
			want:      true,
		},
		"MinExecutionSeconds: execution time above minimum matches": {
			selector: &kueuealpha.PreemptionConfigPreemptionCandidateSelector{
				MinExecutionSeconds: ptr.To[int32](50),
			},
			preemptor: preemptor,
			wl:        candidate,
			want:      true,
		},
		"MaxExecutionSeconds: execution time below maximum matches": {
			selector: &kueuealpha.PreemptionConfigPreemptionCandidateSelector{
				MaxExecutionSeconds: ptr.To[int32](150),
			},
			preemptor: preemptor,
			wl:        candidate,
			want:      true,
		},
		"MaxExecutionSeconds: execution time equal to maximum matches": {
			selector: &kueuealpha.PreemptionConfigPreemptionCandidateSelector{
				MaxExecutionSeconds: ptr.To[int32](100),
			},
			preemptor: preemptor,
			wl:        candidate,
			want:      true,
		},
		"MaxExecutionSeconds: execution time above maximum is rejected": {
			selector: &kueuealpha.PreemptionConfigPreemptionCandidateSelector{
				MaxExecutionSeconds: ptr.To[int32](99),
			},
			preemptor: preemptor,
			wl:        candidate,
			want:      false,
		},
		"ExecutionTimeRelation LessThan: candidate strictly less than preemptor matches": {
			selector: &kueuealpha.PreemptionConfigPreemptionCandidateSelector{
				ExecutionTimeRelation: ptr.To(kueuealpha.LessThan),
			},
			preemptor: admittedWorkloadSince("preemptor", now.Add(-150*time.Second), creationTime),
			wl:        candidate,
			want:      true,
		},
		"ExecutionTimeRelation LessThan: candidate equal to preemptor is rejected": {
			selector: &kueuealpha.PreemptionConfigPreemptionCandidateSelector{
				ExecutionTimeRelation: ptr.To(kueuealpha.LessThan),
			},
			preemptor: admittedWorkloadSince("preemptor", now.Add(-100*time.Second), creationTime),
			wl:        candidate,
			want:      false,
		},
		"ExecutionTimeRelation LessThanOrEqual: candidate equal to preemptor matches": {
			selector: &kueuealpha.PreemptionConfigPreemptionCandidateSelector{
				ExecutionTimeRelation: ptr.To(kueuealpha.LessThanOrEqual),
			},
			preemptor: admittedWorkloadSince("preemptor", now.Add(-100*time.Second), creationTime),
			wl:        candidate,
			want:      true,
		},
		"ExecutionTimeRelation GreaterThan: candidate strictly greater than preemptor matches": {
			selector: &kueuealpha.PreemptionConfigPreemptionCandidateSelector{
				ExecutionTimeRelation: ptr.To(kueuealpha.GreaterThan),
			},
			preemptor: admittedWorkloadSince("preemptor", now.Add(-50*time.Second), creationTime),
			wl:        candidate,
			want:      true,
		},
		"ExecutionTimeRelation GreaterThan: candidate equal to preemptor is rejected": {
			selector: &kueuealpha.PreemptionConfigPreemptionCandidateSelector{
				ExecutionTimeRelation: ptr.To(kueuealpha.GreaterThan),
			},
			preemptor: admittedWorkloadSince("preemptor", now.Add(-100*time.Second), creationTime),
			wl:        candidate,
			want:      false,
		},
		"ExecutionTimeRelation GreaterThanOrEqual: candidate equal to preemptor matches": {
			selector: &kueuealpha.PreemptionConfigPreemptionCandidateSelector{
				ExecutionTimeRelation: ptr.To(kueuealpha.GreaterThanOrEqual),
			},
			preemptor: admittedWorkloadSince("preemptor", now.Add(-100*time.Second), creationTime),
			wl:        candidate,
			want:      true,
		},
		"MinTimeFromCreationSeconds: age below minimum is rejected": {
			selector: &kueuealpha.PreemptionConfigPreemptionCandidateSelector{
				MinTimeFromCreationSeconds: ptr.To[int32](201),
			},
			preemptor: preemptor,
			wl:        candidate,
			want:      false,
		},
		"MinTimeFromCreationSeconds: age equal to minimum matches": {
			selector: &kueuealpha.PreemptionConfigPreemptionCandidateSelector{
				MinTimeFromCreationSeconds: ptr.To[int32](200),
			},
			preemptor: preemptor,
			wl:        candidate,
			want:      true,
		},
		"MinTimeFromCreationSeconds: age above minimum matches": {
			selector: &kueuealpha.PreemptionConfigPreemptionCandidateSelector{
				MinTimeFromCreationSeconds: ptr.To[int32](100),
			},
			preemptor: preemptor,
			wl:        candidate,
			want:      true,
		},
		"MaxTimeFromCreationSeconds: age below maximum matches": {
			selector: &kueuealpha.PreemptionConfigPreemptionCandidateSelector{
				MaxTimeFromCreationSeconds: ptr.To[int32](300),
			},
			preemptor: preemptor,
			wl:        candidate,
			want:      true,
		},
		"MaxTimeFromCreationSeconds: age equal to maximum matches": {
			selector: &kueuealpha.PreemptionConfigPreemptionCandidateSelector{
				MaxTimeFromCreationSeconds: ptr.To[int32](200),
			},
			preemptor: preemptor,
			wl:        candidate,
			want:      true,
		},
		"MaxTimeFromCreationSeconds: age above maximum is rejected": {
			selector: &kueuealpha.PreemptionConfigPreemptionCandidateSelector{
				MaxTimeFromCreationSeconds: ptr.To[int32](199),
			},
			preemptor: preemptor,
			wl:        candidate,
			want:      false,
		},
		"TimeFromCreationRelation LessThan: candidate younger than preemptor matches": {
			selector: &kueuealpha.PreemptionConfigPreemptionCandidateSelector{
				TimeFromCreationRelation: ptr.To(kueuealpha.LessThan),
			},
			preemptor: workloadCreatedSince("preemptor", now.Add(-250*time.Second)),
			wl:        candidate,
			want:      true,
		},
		"TimeFromCreationRelation LessThan: candidate same age as preemptor is rejected": {
			selector: &kueuealpha.PreemptionConfigPreemptionCandidateSelector{
				TimeFromCreationRelation: ptr.To(kueuealpha.LessThan),
			},
			preemptor: workloadCreatedSince("preemptor", now.Add(-200*time.Second)),
			wl:        candidate,
			want:      false,
		},
		"TimeFromCreationRelation LessThanOrEqual: candidate same age as preemptor matches": {
			selector: &kueuealpha.PreemptionConfigPreemptionCandidateSelector{
				TimeFromCreationRelation: ptr.To(kueuealpha.LessThanOrEqual),
			},
			preemptor: workloadCreatedSince("preemptor", now.Add(-200*time.Second)),
			wl:        candidate,
			want:      true,
		},
		"TimeFromCreationRelation GreaterThan: candidate older than preemptor matches": {
			selector: &kueuealpha.PreemptionConfigPreemptionCandidateSelector{
				TimeFromCreationRelation: ptr.To(kueuealpha.GreaterThan),
			},
			preemptor: workloadCreatedSince("preemptor", now.Add(-150*time.Second)),
			wl:        candidate,
			want:      true,
		},
		"TimeFromCreationRelation GreaterThan: candidate same age as preemptor is rejected": {
			selector: &kueuealpha.PreemptionConfigPreemptionCandidateSelector{
				TimeFromCreationRelation: ptr.To(kueuealpha.GreaterThan),
			},
			preemptor: workloadCreatedSince("preemptor", now.Add(-200*time.Second)),
			wl:        candidate,
			want:      false,
		},
		"TimeFromCreationRelation GreaterThanOrEqual: candidate same age as preemptor matches": {
			selector: &kueuealpha.PreemptionConfigPreemptionCandidateSelector{
				TimeFromCreationRelation: ptr.To(kueuealpha.GreaterThanOrEqual),
			},
			preemptor: workloadCreatedSince("preemptor", now.Add(-200*time.Second)),
			wl:        candidate,
			want:      true,
		},
		"combined constraints: matches when all execution and creation conditions are met": {
			selector: &kueuealpha.PreemptionConfigPreemptionCandidateSelector{
				MinExecutionSeconds:        ptr.To[int32](50),
				MaxExecutionSeconds:        ptr.To[int32](150),
				ExecutionTimeRelation:      ptr.To(kueuealpha.LessThan),
				MinTimeFromCreationSeconds: ptr.To[int32](150),
				MaxTimeFromCreationSeconds: ptr.To[int32](250),
				TimeFromCreationRelation:   ptr.To(kueuealpha.GreaterThan),
			},
			preemptor: admittedWorkloadSince("preemptor", now.Add(-120*time.Second), now.Add(-180*time.Second)),
			wl:        candidate,
			want:      true,
		},
		"combined constraints: rejected when execution time matches but creation relation fails": {
			selector: &kueuealpha.PreemptionConfigPreemptionCandidateSelector{
				MinExecutionSeconds:      ptr.To[int32](50),
				MaxExecutionSeconds:      ptr.To[int32](150),
				ExecutionTimeRelation:    ptr.To(kueuealpha.LessThan),
				TimeFromCreationRelation: ptr.To(kueuealpha.LessThan),
			},
			preemptor: admittedWorkloadSince("preemptor", now.Add(-120*time.Second), now.Add(-180*time.Second)),
			wl:        candidate,
			want:      false,
		},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			_, log := utiltesting.ContextWithLog(t)
			fakeClock := testingclock.NewFakeClock(now)

			if tc.preemptor == nil {
				t.Fatalf("preemptor's workload should be provided")
			}
			preemptorWlInfo := workload.NewInfo(log, tc.preemptor)
			tf, err := NewTimeFilter(log, fakeClock, tc.selector, preemptorWlInfo)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}

			wlInfo := workload.NewInfo(log, tc.wl)
			if got := tf.Matches(wlInfo); got != tc.want {
				t.Errorf("timeFilter.Matches() = %v, want %v", got, tc.want)
			}
		})
	}
}

func workloadCreatedSince(name string, creationTime time.Time) *kueue.Workload {
	return utiltestingapi.MakeWorkload(name, "ns").
		Creation(creationTime).
		Obj()
}

func admittedWorkloadSince(name string, admissionTime time.Time, creationTime time.Time) *kueue.Workload {
	return utiltestingapi.MakeWorkload(name, "ns").
		Creation(creationTime).
		Condition(metav1.Condition{
			Type:               kueue.WorkloadAdmitted,
			Status:             metav1.ConditionTrue,
			LastTransitionTime: metav1.NewTime(admissionTime),
		}).
		Obj()
}
