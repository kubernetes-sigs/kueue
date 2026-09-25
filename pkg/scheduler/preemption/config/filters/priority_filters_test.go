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

	"k8s.io/component-base/featuregate"
	"k8s.io/utils/ptr"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	controllerconstants "sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/features"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/pkg/workload"
)

func TestPriorityFilter_Matches(t *testing.T) {
	cases := map[string]struct {
		constraint        kueuealpha.PreemptionConfigPriorityConstraint
		preemptorPriority *int32
		candidatePriority *int32
		wantMatch         bool
		wantBuildErr      bool
	}{
		"LessThan: candidate strictly lower matches": {
			constraint: kueuealpha.PreemptionConfigPriorityConstraint{
				Mode:       kueuealpha.Base,
				Comparison: kueuealpha.LessThan,
			},
			preemptorPriority: ptr.To[int32](100),
			candidatePriority: ptr.To[int32](50),
			wantMatch:         true,
		},
		"LessThan: candidate equal rejected": {
			constraint: kueuealpha.PreemptionConfigPriorityConstraint{
				Mode:       kueuealpha.Base,
				Comparison: kueuealpha.LessThan,
			},
			preemptorPriority: ptr.To[int32](100),
			candidatePriority: ptr.To[int32](100),
			wantMatch:         false,
		},
		"LessThan: candidate strictly greater rejected": {
			constraint: kueuealpha.PreemptionConfigPriorityConstraint{
				Mode:       kueuealpha.Base,
				Comparison: kueuealpha.LessThan,
			},
			preemptorPriority: ptr.To[int32](100),
			candidatePriority: ptr.To[int32](150),
			wantMatch:         false,
		},
		"LessThanOrEqual: candidate strictly lower matches": {
			constraint: kueuealpha.PreemptionConfigPriorityConstraint{
				Mode:       kueuealpha.Base,
				Comparison: kueuealpha.LessThanOrEqual,
			},
			preemptorPriority: ptr.To[int32](100),
			candidatePriority: ptr.To[int32](50),
			wantMatch:         true,
		},
		"LessThanOrEqual: candidate equal matches": {
			constraint: kueuealpha.PreemptionConfigPriorityConstraint{
				Mode:       kueuealpha.Base,
				Comparison: kueuealpha.LessThanOrEqual,
			},
			preemptorPriority: ptr.To[int32](100),
			candidatePriority: ptr.To[int32](100),
			wantMatch:         true,
		},
		"LessThanOrEqual: candidate strictly greater rejected": {
			constraint: kueuealpha.PreemptionConfigPriorityConstraint{
				Mode:       kueuealpha.Base,
				Comparison: kueuealpha.LessThanOrEqual,
			},
			preemptorPriority: ptr.To[int32](100),
			candidatePriority: ptr.To[int32](150),
			wantMatch:         false,
		},
		"GreaterThan: candidate strictly greater matches": {
			constraint: kueuealpha.PreemptionConfigPriorityConstraint{
				Mode:       kueuealpha.Base,
				Comparison: kueuealpha.GreaterThan,
			},
			preemptorPriority: ptr.To[int32](100),
			candidatePriority: ptr.To[int32](150),
			wantMatch:         true,
		},
		"GreaterThan: candidate equal rejected": {
			constraint: kueuealpha.PreemptionConfigPriorityConstraint{
				Mode:       kueuealpha.Base,
				Comparison: kueuealpha.GreaterThan,
			},
			preemptorPriority: ptr.To[int32](100),
			candidatePriority: ptr.To[int32](100),
			wantMatch:         false,
		},
		"GreaterThan: candidate strictly lower rejected": {
			constraint: kueuealpha.PreemptionConfigPriorityConstraint{
				Mode:       kueuealpha.Base,
				Comparison: kueuealpha.GreaterThan,
			},
			preemptorPriority: ptr.To[int32](100),
			candidatePriority: ptr.To[int32](50),
			wantMatch:         false,
		},
		"GreaterThanOrEqual: candidate strictly greater matches": {
			constraint: kueuealpha.PreemptionConfigPriorityConstraint{
				Mode:       kueuealpha.Base,
				Comparison: kueuealpha.GreaterThanOrEqual,
			},
			preemptorPriority: ptr.To[int32](100),
			candidatePriority: ptr.To[int32](150),
			wantMatch:         true,
		},
		"GreaterThanOrEqual: candidate equal matches": {
			constraint: kueuealpha.PreemptionConfigPriorityConstraint{
				Mode:       kueuealpha.Base,
				Comparison: kueuealpha.GreaterThanOrEqual,
			},
			preemptorPriority: ptr.To[int32](100),
			candidatePriority: ptr.To[int32](100),
			wantMatch:         true,
		},
		"GreaterThanOrEqual: candidate strictly lower rejected": {
			constraint: kueuealpha.PreemptionConfigPriorityConstraint{
				Mode:       kueuealpha.Base,
				Comparison: kueuealpha.GreaterThanOrEqual,
			},
			preemptorPriority: ptr.To[int32](100),
			candidatePriority: ptr.To[int32](50),
			wantMatch:         false,
		},
		"Default priority handling: nil preemptor priority defaults to 0 and matches strictly lower candidate": {
			constraint: kueuealpha.PreemptionConfigPriorityConstraint{
				Mode:       kueuealpha.Base,
				Comparison: kueuealpha.LessThan,
			},
			preemptorPriority: nil,
			candidatePriority: ptr.To[int32](-10),
			wantMatch:         true,
		},
		"Default priority handling: nil candidate priority defaults to 0 and matches when equal": {
			constraint: kueuealpha.PreemptionConfigPriorityConstraint{
				Mode:       kueuealpha.Base,
				Comparison: kueuealpha.LessThanOrEqual,
			},
			preemptorPriority: ptr.To[int32](0),
			candidatePriority: nil,
			wantMatch:         true,
		},
		"Default priority handling: both nil priorities compare as equal (0 vs 0)": {
			constraint: kueuealpha.PreemptionConfigPriorityConstraint{
				Mode:       kueuealpha.Base,
				Comparison: kueuealpha.LessThanOrEqual,
			},
			preemptorPriority: nil,
			candidatePriority: nil,
			wantMatch:         true,
		},
		"Negative priorities: candidate -100 is LessThan preemptor -50": {
			constraint: kueuealpha.PreemptionConfigPriorityConstraint{
				Mode:       kueuealpha.Base,
				Comparison: kueuealpha.LessThan,
			},
			preemptorPriority: ptr.To[int32](-50),
			candidatePriority: ptr.To[int32](-100),
			wantMatch:         true,
		},
		"Negative priorities: candidate -150 is not GreaterThan preemptor -100": {
			constraint: kueuealpha.PreemptionConfigPriorityConstraint{
				Mode:       kueuealpha.Base,
				Comparison: kueuealpha.GreaterThan,
			},
			preemptorPriority: ptr.To[int32](-100),
			candidatePriority: ptr.To[int32](-150),
			wantMatch:         false,
		},
		"Unknown/unsupported comparison rejects all candidates": {
			constraint: kueuealpha.PreemptionConfigPriorityConstraint{
				Mode:       kueuealpha.Base,
				Comparison: kueuealpha.NumericComparison("InvalidComparison"),
			},
			preemptorPriority: ptr.To[int32](100),
			candidatePriority: ptr.To[int32](50),
			wantMatch:         false,
		},
		"Unknown/unsupported mode rejects all candidates": {
			constraint: kueuealpha.PreemptionConfigPriorityConstraint{
				Mode:       kueuealpha.PreemptionConfigPriorityMode("InvalidMode"),
				Comparison: kueuealpha.LessThan,
			},
			preemptorPriority: ptr.To[int32](100),
			candidatePriority: ptr.To[int32](50),
			wantMatch:         false,
			wantBuildErr:      true,
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
			candidate := workload.NewInfo(log, candBuilder.Obj())

			filter, ok := NewPriorityFilter(log, tc.constraint, preemptor)
			if !ok {
				if !tc.wantBuildErr {
					t.Fatalf("NewPriorityFilter() failed unexpectedly")
				}
				return
			}
			if tc.wantBuildErr {
				t.Fatalf("NewPriorityFilter() succeeded unexpectedly, want build error")
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
				Mode:       kueuealpha.Boosted,
				Comparison: kueuealpha.GreaterThan,
			},
			preemptorPriority: 50,
			candidatePriority: 10,
			candidateBoost:    "100", // effective priority: 10 + 100 = 110 > 50
			wantMatch:         true,
		},
		"Boosted mode, PriorityBoost enabled: preemptor boost raises effective priority above candidate": {
			featureGates: map[featuregate.Feature]bool{features.PriorityBoost: true},
			constraint: kueuealpha.PreemptionConfigPriorityConstraint{
				Mode:       kueuealpha.Boosted,
				Comparison: kueuealpha.LessThan,
			},
			preemptorPriority: 50,
			preemptorBoost:    "100", // effective priority: 50 + 100 = 150 > 120
			candidatePriority: 120,
			wantMatch:         true,
		},
		"Boosted mode, PriorityBoost enabled: both workloads boosted with boundary equality": {
			featureGates: map[featuregate.Feature]bool{features.PriorityBoost: true},
			constraint: kueuealpha.PreemptionConfigPriorityConstraint{
				Mode:       kueuealpha.Boosted,
				Comparison: kueuealpha.LessThanOrEqual,
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
				Mode:       kueuealpha.Boosted,
				Comparison: kueuealpha.GreaterThan,
			},
			preemptorPriority: 50,
			candidatePriority: 10,
			candidateBoost:    "100", // ignored -> base priority is 10 (not > 50)
			wantMatch:         false,
		},
		"Base mode, PriorityBoost enabled: candidate boost is ignored, raw priority used": {
			featureGates: map[featuregate.Feature]bool{features.PriorityBoost: true},
			constraint: kueuealpha.PreemptionConfigPriorityConstraint{
				Mode:       kueuealpha.Base,
				Comparison: kueuealpha.LessThan,
			},
			preemptorPriority: 50,
			candidatePriority: 10,
			candidateBoost:    "100", // effective is 110, but base is 10 < 50
			wantMatch:         true,
		},
		"Base mode, PriorityBoost enabled: preemptor boost is ignored, raw priority used": {
			featureGates: map[featuregate.Feature]bool{features.PriorityBoost: true},
			constraint: kueuealpha.PreemptionConfigPriorityConstraint{
				Mode:       kueuealpha.Base,
				Comparison: kueuealpha.LessThan,
			},
			preemptorPriority: 50,
			preemptorBoost:    "100", // effective is 150, but base is 50; cand is 80 (80 not < 50)
			candidatePriority: 80,
			wantMatch:         false,
		},
		"Base mode, PriorityBoost enabled: both boosted, raw priority evaluated with GreaterThan": {
			featureGates: map[featuregate.Feature]bool{features.PriorityBoost: true},
			constraint: kueuealpha.PreemptionConfigPriorityConstraint{
				Mode:       kueuealpha.Base,
				Comparison: kueuealpha.GreaterThan,
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
				Mode:       kueuealpha.Base,
				Comparison: kueuealpha.GreaterThanOrEqual,
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

			filter, ok := NewPriorityFilter(log, tc.constraint, preemptor)
			if !ok {
				t.Fatalf("NewPriorityFilter() failed unexpectedly")
			}
			if got := filter.Matches(candidate); got != tc.wantMatch {
				t.Errorf("Matches(candidate) = %v, want %v", got, tc.wantMatch)
			}
		})
	}
}
