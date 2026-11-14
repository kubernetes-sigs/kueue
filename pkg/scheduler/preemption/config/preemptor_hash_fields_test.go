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

package config

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	kueuetestalpha1 "sigs.k8s.io/kueue/pkg/util/testing/v1alpha1"
	"sigs.k8s.io/kueue/pkg/workload"
)

func TestPreemptorHashFields(t *testing.T) {
	withinCQ := func() *kueuetestalpha1.CandidateSelectorWrapper {
		return kueuetestalpha1.MakeCandidateSelector(kueuealpha.WithinClusterQueue)
	}
	cases := map[string]struct {
		config *kueuealpha.PreemptionConfig
		want   workload.PreemptorHashFields
	}{
		"no rules": {
			config: kueuetestalpha1.MakePreemptionConfig("config").Obj(),
		},
		"candidate-only selectors read nothing of the preemptor": {
			config: kueuetestalpha1.MakePreemptionConfig("config").
				Rule("rule", kueuealpha.Always,
					withinCQ().
						LabelSelector(&metav1.LabelSelector{MatchLabels: map[string]string{"class": "dev"}}).
						PriorityClassIn("low").
						Priority(kueuealpha.Boosted, kueuealpha.LessThan).
						NumericLabels(kueuealpha.PreemptionConfigNumericLabelConstraint{Key: "tpus", MaxValue: new(int32(8))}).
						Obj()).
				Obj(),
		},
		"preemptor selectors, numeric comparisons, base priority and WithinLocalQueue": {
			config: kueuetestalpha1.MakePreemptionConfig("config").
				Rules(
					kueuetestalpha1.MakePreemptionRule("by-labels", kueuealpha.Always,
						withinCQ().NumericLabels(kueuealpha.PreemptionConfigNumericLabelConstraint{
							Key: "tpus", Comparison: new(kueuealpha.LessThan),
						}).Obj(),
					).PreemptorSelector(&metav1.LabelSelector{
						MatchLabels: map[string]string{"class": "prod"},
						MatchExpressions: []metav1.LabelSelectorRequirement{
							{Key: "team", Operator: metav1.LabelSelectorOpNotIn, Values: []string{"b"}},
							{Key: "class", Operator: metav1.LabelSelectorOpExists},
						},
					}).Obj(),
					kueuetestalpha1.MakePreemptionRule("by-priority", kueuealpha.InsufficientQuota,
						kueuetestalpha1.MakeCandidateSelector(kueuealpha.WithinLocalQueue).
							Priority(kueuealpha.Base, kueuealpha.LessThan).Obj(),
					).PreemptorPriorityClassIn("high").Obj(),
				).
				Obj(),
			want: workload.PreemptorHashFields{
				LabelKeys:     []string{"class", "team", "tpus"},
				PriorityClass: true,
				BasePriority:  true,
				LocalQueue:    true,
			},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := PreemptorHashFields(tc.config)
			if diff := cmp.Diff(tc.want, *got); diff != "" {
				t.Errorf("Unexpected PreemptorHashFields (-want,+got):\n%s", diff)
			}
		})
	}
}
