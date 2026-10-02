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

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"k8s.io/component-base/featuregate"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	controllerconstants "sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/features"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/pkg/workload"
)

func TestNewPriorityFilter_Validation(t *testing.T) {
	cases := map[string]struct {
		constraint   kueuealpha.PreemptionConfigPriorityConstraint
		wantBuildErr *FilterBuildError
	}{
		"Mode set without Comparison returns build error": {
			constraint: kueuealpha.PreemptionConfigPriorityConstraint{
				Mode: new(kueuealpha.Base),
			},
			wantBuildErr: &FilterBuildError{
				Filter: FilterPriority,
				Reason: ReasonMissingModeOrComparison,
			},
		},
		"Comparison set without Mode returns build error": {
			constraint: kueuealpha.PreemptionConfigPriorityConstraint{
				Comparison: new(kueuealpha.LessThan),
			},
			wantBuildErr: &FilterBuildError{
				Filter: FilterPriority,
				Reason: ReasonMissingModeOrComparison,
			},
		},
		"Neither Mode nor Comparison set with MatchNames succeeds": {
			constraint: kueuealpha.PreemptionConfigPriorityConstraint{
				MatchNames: []string{"low-priority"},
			},
			wantBuildErr: nil,
		},
		"Neither Mode nor Comparison set with NotMatchNames succeeds": {
			constraint: kueuealpha.PreemptionConfigPriorityConstraint{
				NotMatchNames: []string{"high-priority"},
			},
			wantBuildErr: nil,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, log := utiltesting.ContextWithLog(t)
			preemptor := workload.NewInfo(log, utiltestingapi.MakeWorkload("preemptor", "ns").Priority(100).Obj())
			_, err := NewPriorityFilter(log, tc.constraint, preemptor)
			if diff := cmp.Diff(tc.wantBuildErr, err, cmpopts.EquateErrors()); diff != "" {
				t.Fatalf("NewPriorityFilter() build error (-want +got):\n%s", diff)
			}
		})
	}
}

func TestPriorityFilter_Matches(t *testing.T) {
	cases := map[string]struct {
		constraint                kueuealpha.PreemptionConfigPriorityConstraint
		preemptorPriority         *int32
		candidatePriority         *int32
		candidatePriorityClassRef *kueue.PriorityClassRef
		wantMatch                 bool
		wantBuildErr              *FilterBuildError
	}{
		"LessThan: candidate strictly lower matches": {
			constraint: kueuealpha.PreemptionConfigPriorityConstraint{
				Mode:       new(kueuealpha.Base),
				Comparison: new(kueuealpha.LessThan),
			},
			preemptorPriority: new(int32(100)),
			candidatePriority: new(int32(50)),
			wantMatch:         true,
		},
		"LessThan: candidate equal rejected": {
			constraint: kueuealpha.PreemptionConfigPriorityConstraint{
				Mode:       new(kueuealpha.Base),
				Comparison: new(kueuealpha.LessThan),
			},
			preemptorPriority: new(int32(100)),
			candidatePriority: new(int32(100)),
			wantMatch:         false,
		},
		"LessThan: candidate strictly greater rejected": {
			constraint: kueuealpha.PreemptionConfigPriorityConstraint{
				Mode:       new(kueuealpha.Base),
				Comparison: new(kueuealpha.LessThan),
			},
			preemptorPriority: new(int32(100)),
			candidatePriority: new(int32(150)),
			wantMatch:         false,
		},
		"LessThanOrEqual: candidate strictly lower matches": {
			constraint: kueuealpha.PreemptionConfigPriorityConstraint{
				Mode:       new(kueuealpha.Base),
				Comparison: new(kueuealpha.LessThanOrEqual),
			},
			preemptorPriority: new(int32(100)),
			candidatePriority: new(int32(50)),
			wantMatch:         true,
		},
		"LessThanOrEqual: candidate equal matches": {
			constraint: kueuealpha.PreemptionConfigPriorityConstraint{
				Mode:       new(kueuealpha.Base),
				Comparison: new(kueuealpha.LessThanOrEqual),
			},
			preemptorPriority: new(int32(100)),
			candidatePriority: new(int32(100)),
			wantMatch:         true,
		},
		"LessThanOrEqual: candidate strictly greater rejected": {
			constraint: kueuealpha.PreemptionConfigPriorityConstraint{
				Mode:       new(kueuealpha.Base),
				Comparison: new(kueuealpha.LessThanOrEqual),
			},
			preemptorPriority: new(int32(100)),
			candidatePriority: new(int32(150)),
			wantMatch:         false,
		},
		"GreaterThan: candidate strictly greater matches": {
			constraint: kueuealpha.PreemptionConfigPriorityConstraint{
				Mode:       new(kueuealpha.Base),
				Comparison: new(kueuealpha.GreaterThan),
			},
			preemptorPriority: new(int32(100)),
			candidatePriority: new(int32(150)),
			wantMatch:         true,
		},
		"GreaterThan: candidate equal rejected": {
			constraint: kueuealpha.PreemptionConfigPriorityConstraint{
				Mode:       new(kueuealpha.Base),
				Comparison: new(kueuealpha.GreaterThan),
			},
			preemptorPriority: new(int32(100)),
			candidatePriority: new(int32(100)),
			wantMatch:         false,
		},
		"GreaterThan: candidate strictly lower rejected": {
			constraint: kueuealpha.PreemptionConfigPriorityConstraint{
				Mode:       new(kueuealpha.Base),
				Comparison: new(kueuealpha.GreaterThan),
			},
			preemptorPriority: new(int32(100)),
			candidatePriority: new(int32(50)),
			wantMatch:         false,
		},
		"GreaterThanOrEqual: candidate strictly greater matches": {
			constraint: kueuealpha.PreemptionConfigPriorityConstraint{
				Mode:       new(kueuealpha.Base),
				Comparison: new(kueuealpha.GreaterThanOrEqual),
			},
			preemptorPriority: new(int32(100)),
			candidatePriority: new(int32(150)),
			wantMatch:         true,
		},
		"GreaterThanOrEqual: candidate equal matches": {
			constraint: kueuealpha.PreemptionConfigPriorityConstraint{
				Mode:       new(kueuealpha.Base),
				Comparison: new(kueuealpha.GreaterThanOrEqual),
			},
			preemptorPriority: new(int32(100)),
			candidatePriority: new(int32(100)),
			wantMatch:         true,
		},
		"GreaterThanOrEqual: candidate strictly lower rejected": {
			constraint: kueuealpha.PreemptionConfigPriorityConstraint{
				Mode:       new(kueuealpha.Base),
				Comparison: new(kueuealpha.GreaterThanOrEqual),
			},
			preemptorPriority: new(int32(100)),
			candidatePriority: new(int32(50)),
			wantMatch:         false,
		},
		"Default priority handling: nil preemptor priority defaults to 0 and matches strictly lower candidate": {
			constraint: kueuealpha.PreemptionConfigPriorityConstraint{
				Mode:       new(kueuealpha.Base),
				Comparison: new(kueuealpha.LessThan),
			},
			preemptorPriority: nil,
			candidatePriority: new(int32(-10)),
			wantMatch:         true,
		},
		"Default priority handling: nil candidate priority defaults to 0 and matches when equal": {
			constraint: kueuealpha.PreemptionConfigPriorityConstraint{
				Mode:       new(kueuealpha.Base),
				Comparison: new(kueuealpha.LessThanOrEqual),
			},
			preemptorPriority: new(int32(0)),
			candidatePriority: nil,
			wantMatch:         true,
		},
		"Default priority handling: both nil priorities compare as equal (0 vs 0)": {
			constraint: kueuealpha.PreemptionConfigPriorityConstraint{
				Mode:       new(kueuealpha.Base),
				Comparison: new(kueuealpha.LessThanOrEqual),
			},
			preemptorPriority: nil,
			candidatePriority: nil,
			wantMatch:         true,
		},
		"Negative priorities: candidate -100 is LessThan preemptor -50": {
			constraint: kueuealpha.PreemptionConfigPriorityConstraint{
				Mode:       new(kueuealpha.Base),
				Comparison: new(kueuealpha.LessThan),
			},
			preemptorPriority: new(int32(-50)),
			candidatePriority: new(int32(-100)),
			wantMatch:         true,
		},
		"Negative priorities: candidate -150 is not GreaterThan preemptor -100": {
			constraint: kueuealpha.PreemptionConfigPriorityConstraint{
				Mode:       new(kueuealpha.Base),
				Comparison: new(kueuealpha.GreaterThan),
			},
			preemptorPriority: new(int32(-100)),
			candidatePriority: new(int32(-150)),
			wantMatch:         false,
		},
		"MatchNames only: matching WorkloadPriorityClass matches": {
			constraint: kueuealpha.PreemptionConfigPriorityConstraint{
				MatchNames: []string{"low-priority", "very-low-priority"},
			},
			candidatePriorityClassRef: kueue.NewWorkloadPriorityClassRef("low-priority"),
			wantMatch:                 true,
		},
		"MatchNames only: matching Pod PriorityClass matches": {
			constraint: kueuealpha.PreemptionConfigPriorityConstraint{
				MatchNames: []string{"low-priority", "very-low-priority"},
			},
			candidatePriorityClassRef: kueue.NewPodPriorityClassRef("very-low-priority"),
			wantMatch:                 true,
		},
		"MatchNames only: non-matching priority class rejected": {
			constraint: kueuealpha.PreemptionConfigPriorityConstraint{
				MatchNames: []string{"low-priority", "very-low-priority"},
			},
			candidatePriorityClassRef: kueue.NewWorkloadPriorityClassRef("high-priority"),
			wantMatch:                 false,
		},
		"MatchNames only: nil PriorityClassRef rejected": {
			constraint: kueuealpha.PreemptionConfigPriorityConstraint{
				MatchNames: []string{"low-priority"},
			},
			candidatePriorityClassRef: nil,
			wantMatch:                 false,
		},
		"NotMatchNames only: excluded WorkloadPriorityClass rejected": {
			constraint: kueuealpha.PreemptionConfigPriorityConstraint{
				NotMatchNames: []string{"high-priority", "critical-priority"},
			},
			candidatePriorityClassRef: kueue.NewWorkloadPriorityClassRef("high-priority"),
			wantMatch:                 false,
		},
		"NotMatchNames only: excluded Pod PriorityClass rejected": {
			constraint: kueuealpha.PreemptionConfigPriorityConstraint{
				NotMatchNames: []string{"high-priority", "critical-priority"},
			},
			candidatePriorityClassRef: kueue.NewPodPriorityClassRef("critical-priority"),
			wantMatch:                 false,
		},
		"NotMatchNames only: non-excluded priority class matches": {
			constraint: kueuealpha.PreemptionConfigPriorityConstraint{
				NotMatchNames: []string{"high-priority"},
			},
			candidatePriorityClassRef: kueue.NewWorkloadPriorityClassRef("low-priority"),
			wantMatch:                 true,
		},
		"NotMatchNames only: nil PriorityClassRef matches": {
			constraint: kueuealpha.PreemptionConfigPriorityConstraint{
				NotMatchNames: []string{"high-priority"},
			},
			candidatePriorityClassRef: nil,
			wantMatch:                 true,
		},
		"MatchNames and NotMatchNames: in MatchNames and not in NotMatchNames matches": {
			constraint: kueuealpha.PreemptionConfigPriorityConstraint{
				MatchNames:    []string{"low-priority", "very-low-priority"},
				NotMatchNames: []string{"high-priority"},
			},
			candidatePriorityClassRef: kueue.NewWorkloadPriorityClassRef("low-priority"),
			wantMatch:                 true,
		},
		"MatchNames and NotMatchNames: overlapping name in both lists rejected": {
			constraint: kueuealpha.PreemptionConfigPriorityConstraint{
				MatchNames:    []string{"low-priority", "very-low-priority"},
				NotMatchNames: []string{"low-priority"},
			},
			candidatePriorityClassRef: kueue.NewWorkloadPriorityClassRef("low-priority"),
			wantMatch:                 false,
		},
		"MatchNames and NotMatchNames: nil PriorityClassRef rejected because MatchNames is non-empty": {
			constraint: kueuealpha.PreemptionConfigPriorityConstraint{
				MatchNames:    []string{"low-priority"},
				NotMatchNames: []string{"high-priority"},
			},
			candidatePriorityClassRef: nil,
			wantMatch:                 false,
		},
		"Combined Mode+Comparison+MatchNames: both relative priority and class name match": {
			constraint: kueuealpha.PreemptionConfigPriorityConstraint{
				Mode:       new(kueuealpha.Base),
				Comparison: new(kueuealpha.LessThan),
				MatchNames: []string{"low-priority"},
			},
			preemptorPriority:         new(int32(100)),
			candidatePriority:         new(int32(50)),
			candidatePriorityClassRef: kueue.NewWorkloadPriorityClassRef("low-priority"),
			wantMatch:                 true,
		},
		"Combined Mode+Comparison+MatchNames: relative priority matches but class name does not": {
			constraint: kueuealpha.PreemptionConfigPriorityConstraint{
				Mode:       new(kueuealpha.Base),
				Comparison: new(kueuealpha.LessThan),
				MatchNames: []string{"low-priority"},
			},
			preemptorPriority:         new(int32(100)),
			candidatePriority:         new(int32(50)),
			candidatePriorityClassRef: kueue.NewWorkloadPriorityClassRef("mid-priority"),
			wantMatch:                 false,
		},
		"Combined Mode+Comparison+MatchNames: class name matches but relative priority does not": {
			constraint: kueuealpha.PreemptionConfigPriorityConstraint{
				Mode:       new(kueuealpha.Base),
				Comparison: new(kueuealpha.LessThan),
				MatchNames: []string{"low-priority"},
			},
			preemptorPriority:         new(int32(100)),
			candidatePriority:         new(int32(150)),
			candidatePriorityClassRef: kueue.NewWorkloadPriorityClassRef("low-priority"),
			wantMatch:                 false,
		},
		"Combined Mode+Comparison+NotMatchNames: lower priority and not in NotMatchNames matches": {
			constraint: kueuealpha.PreemptionConfigPriorityConstraint{
				Mode:          new(kueuealpha.Base),
				Comparison:    new(kueuealpha.LessThan),
				NotMatchNames: []string{"mid-priority"},
			},
			preemptorPriority:         new(int32(100)),
			candidatePriority:         new(int32(50)),
			candidatePriorityClassRef: kueue.NewWorkloadPriorityClassRef("low-priority"),
			wantMatch:                 true,
		},
		"Combined Mode+Comparison+NotMatchNames: lower priority but in NotMatchNames rejected": {
			constraint: kueuealpha.PreemptionConfigPriorityConstraint{
				Mode:          new(kueuealpha.Base),
				Comparison:    new(kueuealpha.LessThan),
				NotMatchNames: []string{"mid-priority"},
			},
			preemptorPriority:         new(int32(100)),
			candidatePriority:         new(int32(50)),
			candidatePriorityClassRef: kueue.NewWorkloadPriorityClassRef("mid-priority"),
			wantMatch:                 false,
		},
		"Unknown/unsupported mode returns build error": {
			constraint: kueuealpha.PreemptionConfigPriorityConstraint{
				Mode:       new(kueuealpha.PreemptionConfigPriorityMode("InvalidMode")),
				Comparison: new(kueuealpha.LessThan),
			},
			preemptorPriority: new(int32(100)),
			candidatePriority: new(int32(50)),
			wantBuildErr: &FilterBuildError{
				Filter: FilterPriority,
				Reason: ReasonUnsupportedMode,
			},
		},
		"Unknown/unsupported comparison returns build error": {
			constraint: kueuealpha.PreemptionConfigPriorityConstraint{
				Mode:       new(kueuealpha.Base),
				Comparison: new(kueuealpha.NumericComparison("InvalidComparison")),
			},
			preemptorPriority: new(int32(100)),
			candidatePriority: new(int32(50)),
			wantBuildErr: &FilterBuildError{
				Filter: FilterPriority,
				Reason: ReasonUnsupportedComparison,
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, log := utiltesting.ContextWithLog(t)
			preemptorBuilder := utiltestingapi.MakeWorkload("preemptor", "ns")
			if tc.preemptorPriority != nil {
				preemptorBuilder = preemptorBuilder.Priority(*tc.preemptorPriority)
			}
			preemptor := workload.NewInfo(log, preemptorBuilder.Obj())

			candBuilder := utiltestingapi.MakeWorkload("candidate", "ns")
			if tc.candidatePriority != nil {
				candBuilder = candBuilder.Priority(*tc.candidatePriority)
			}
			if tc.candidatePriorityClassRef != nil {
				candBuilder = candBuilder.PriorityClassRef(tc.candidatePriorityClassRef)
			}
			candidate := workload.NewInfo(log, candBuilder.Obj())

			filter, err := NewPriorityFilter(log, tc.constraint, preemptor)
			if diff := cmp.Diff(tc.wantBuildErr, err, cmpopts.EquateErrors()); diff != "" {
				t.Fatalf("NewPriorityFilter() build error (-want +got):\n%s", diff)
			}
			if tc.wantBuildErr != nil {
				return
			}
			if got := filter.Matches(candidate); got != tc.wantMatch {
				t.Errorf("Matches(candidate) = %v, want %v", got, tc.wantMatch)
			}
		})
	}
}

func TestPriorityFilter_PriorityBoost(t *testing.T) {
	cases := map[string]struct {
		featureGates      map[featuregate.Feature]bool
		constraint        kueuealpha.PreemptionConfigPriorityConstraint
		preemptorPriority int32
		preemptorBoost    string
		candidatePriority int32
		candidateBoost    string
		wantMatch         bool
	}{
		"Boosted mode, PriorityBoost enabled: candidate boost raises effective priority above preemptor": {
			featureGates: map[featuregate.Feature]bool{features.PriorityBoost: true},
			constraint: kueuealpha.PreemptionConfigPriorityConstraint{
				Mode:       new(kueuealpha.Boosted),
				Comparison: new(kueuealpha.GreaterThan),
			},
			preemptorPriority: 50,
			candidatePriority: 10,
			candidateBoost:    "100", // effective priority: 10 + 100 = 110 > 50
			wantMatch:         true,
		},
		"Boosted mode, PriorityBoost enabled: preemptor boost raises effective priority above candidate": {
			featureGates: map[featuregate.Feature]bool{features.PriorityBoost: true},
			constraint: kueuealpha.PreemptionConfigPriorityConstraint{
				Mode:       new(kueuealpha.Boosted),
				Comparison: new(kueuealpha.LessThan),
			},
			preemptorPriority: 50,
			preemptorBoost:    "100", // effective priority: 50 + 100 = 150 > 120
			candidatePriority: 120,
			wantMatch:         true,
		},
		"Boosted mode, PriorityBoost enabled: both workloads boosted with boundary equality": {
			featureGates: map[featuregate.Feature]bool{features.PriorityBoost: true},
			constraint: kueuealpha.PreemptionConfigPriorityConstraint{
				Mode:       new(kueuealpha.Boosted),
				Comparison: new(kueuealpha.LessThanOrEqual),
			},
			preemptorPriority: 60,
			preemptorBoost:    "10", // effective priority: 60 + 10 = 70
			candidatePriority: 50,
			candidateBoost:    "20", // effective priority: 50 + 20 = 70 <= 70
			wantMatch:         true,
		},
		"Boosted mode, PriorityBoost disabled: boost annotation is ignored and base priority is used": {
			featureGates: map[featuregate.Feature]bool{features.PriorityBoost: false},
			constraint: kueuealpha.PreemptionConfigPriorityConstraint{
				Mode:       new(kueuealpha.Boosted),
				Comparison: new(kueuealpha.GreaterThan),
			},
			preemptorPriority: 50,
			candidatePriority: 10,
			candidateBoost:    "100", // ignored -> base priority is 10 (not > 50)
			wantMatch:         false,
		},
		"Base mode, PriorityBoost enabled: candidate boost is ignored, raw priority used": {
			featureGates: map[featuregate.Feature]bool{features.PriorityBoost: true},
			constraint: kueuealpha.PreemptionConfigPriorityConstraint{
				Mode:       new(kueuealpha.Base),
				Comparison: new(kueuealpha.LessThan),
			},
			preemptorPriority: 50,
			candidatePriority: 10,
			candidateBoost:    "100", // effective is 110, but base is 10 < 50
			wantMatch:         true,
		},
		"Base mode, PriorityBoost enabled: preemptor boost is ignored, raw priority used": {
			featureGates: map[featuregate.Feature]bool{features.PriorityBoost: true},
			constraint: kueuealpha.PreemptionConfigPriorityConstraint{
				Mode:       new(kueuealpha.Base),
				Comparison: new(kueuealpha.LessThan),
			},
			preemptorPriority: 50,
			preemptorBoost:    "100", // effective is 150, but base is 50; cand is 80 (80 not < 50)
			candidatePriority: 80,
			wantMatch:         false,
		},
		"Base mode, PriorityBoost enabled: both boosted, raw priority evaluated with GreaterThan": {
			featureGates: map[featuregate.Feature]bool{features.PriorityBoost: true},
			constraint: kueuealpha.PreemptionConfigPriorityConstraint{
				Mode:       new(kueuealpha.Base),
				Comparison: new(kueuealpha.GreaterThan),
			},
			preemptorPriority: 50,
			preemptorBoost:    "100", // effective 150, base 50
			candidatePriority: 80,
			candidateBoost:    "-100", // effective -20, base 80 -> 80 > 50
			wantMatch:         true,
		},
		"Base mode, without boost: evaluates raw priorities correctly": {
			featureGates: map[featuregate.Feature]bool{features.PriorityBoost: true},
			constraint: kueuealpha.PreemptionConfigPriorityConstraint{
				Mode:       new(kueuealpha.Base),
				Comparison: new(kueuealpha.GreaterThanOrEqual),
			},
			preemptorPriority: 50,
			candidatePriority: 50,
			wantMatch:         true,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, log := utiltesting.ContextWithLog(t)
			features.SetFeatureGatesDuringTest(t, tc.featureGates)

			preemptorBuilder := utiltestingapi.MakeWorkload("preemptor", "ns").Priority(tc.preemptorPriority)
			if tc.preemptorBoost != "" {
				preemptorBuilder = preemptorBuilder.Annotation(controllerconstants.PriorityBoostAnnotationKey, tc.preemptorBoost)
			}
			preemptor := workload.NewInfo(log, preemptorBuilder.Obj())

			candBuilder := utiltestingapi.MakeWorkload("candidate", "ns").Priority(tc.candidatePriority)
			if tc.candidateBoost != "" {
				candBuilder = candBuilder.Annotation(controllerconstants.PriorityBoostAnnotationKey, tc.candidateBoost)
			}
			candidate := workload.NewInfo(log, candBuilder.Obj())

			filter, err := NewPriorityFilter(log, tc.constraint, preemptor)
			if err != nil {
				t.Fatalf("NewPriorityFilter() failed unexpectedly: %v", err)
			}
			if got := filter.Matches(candidate); got != tc.wantMatch {
				t.Errorf("Matches(candidate) = %v, want %v", got, tc.wantMatch)
			}
		})
	}
}
