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
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	kueuetestalpha1 "sigs.k8s.io/kueue/pkg/util/testing/v1alpha1"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/pkg/workload"
)

func TestNewEquivalenceHasher(t *testing.T) {
	withinCQ := func() *kueuetestalpha1.CandidateSelectorWrapper {
		return kueuetestalpha1.MakeCandidateSelector(kueuealpha.WithinClusterQueue)
	}
	cases := map[string]struct {
		config *kueuealpha.PreemptionConfig
		want   EquivalenceHasher
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
			want: EquivalenceHasher{
				labelKeys:     []string{"class", "team", "tpus"},
				priorityClass: true,
				basePriority:  true,
				localQueue:    true,
			},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := NewEquivalenceHasher(tc.config)
			if diff := cmp.Diff(tc.want, *got, cmp.AllowUnexported(EquivalenceHasher{})); diff != "" {
				t.Errorf("Unexpected EquivalenceHasher (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestEquivalenceHasherHash(t *testing.T) {
	info := func(wl *kueue.Workload) *workload.Info {
		return &workload.Info{Obj: wl, SchedulingHash: "scheduling-hash"}
	}
	base := func() *utiltestingapi.WorkloadWrapper {
		return utiltestingapi.MakeWorkload("wl", "default").Queue("lq").Priority(10).
			Label("team", "a").Label("kueue.x-k8s.io/job-uid", "uid-1")
	}
	labels := &EquivalenceHasher{labelKeys: []string{"team"}}
	cases := map[string]struct {
		fields      *EquivalenceHasher
		otherFields *EquivalenceHasher
		a, b        *kueue.Workload
		wantSame    bool
	}{
		"rules reading nothing keep the scheduling hash": {
			fields:   &EquivalenceHasher{},
			a:        base().Obj(),
			b:        base().Label("team", "b").Obj(),
			wantSame: true,
		},
		"a label read by a rule tells workloads apart": {
			fields: labels,
			a:      base().Obj(),
			b:      base().Label("team", "b").Obj(),
		},
		"labels no rule reads do not tell workloads apart": {
			fields:   labels,
			a:        base().Obj(),
			b:        base().Label("kueue.x-k8s.io/job-uid", "uid-2").Obj(),
			wantSame: true,
		},
		"workloads lacking the label read by a rule are equivalent": {
			fields:   labels,
			a:        utiltestingapi.MakeWorkload("a", "default").Obj(),
			b:        utiltestingapi.MakeWorkload("b", "default").Label("other", "x").Obj(),
			wantSame: true,
		},
		"a missing label differs from an empty one": {
			fields: labels,
			a:      utiltestingapi.MakeWorkload("a", "default").Obj(),
			b:      utiltestingapi.MakeWorkload("b", "default").Label("team", "").Obj(),
		},
		"the priority class tells workloads apart when a rule reads it": {
			fields: &EquivalenceHasher{priorityClass: true},
			a:      base().WorkloadPriorityClassRef("high").Obj(),
			b:      base().Obj(),
		},
		"the base priority tells workloads apart when a rule reads it": {
			fields: &EquivalenceHasher{basePriority: true},
			a:      base().Obj(),
			b:      base().Priority(20).Obj(),
		},
		"the LocalQueue tells workloads apart when a rule reads it": {
			fields: &EquivalenceHasher{localQueue: true},
			a:      base().Obj(),
			b:      utiltestingapi.MakeWorkload("wl", "other").Queue("lq").Priority(10).Label("team", "a").Obj(),
		},
		"fields no rule reads do not tell workloads apart": {
			fields:   labels,
			a:        base().WorkloadPriorityClassRef("high").Obj(),
			b:        utiltestingapi.MakeWorkload("wl", "other").Queue("lq-2").Priority(20).Label("team", "a").Obj(),
			wantSame: true,
		},
		"a workload extended by other rules differs": {
			fields:      labels,
			otherFields: &EquivalenceHasher{labelKeys: []string{"team"}, priorityClass: true},
			a:           base().Obj(),
			b:           base().Obj(),
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			otherFields := tc.fields
			if tc.otherFields != nil {
				otherFields = tc.otherFields
			}
			a, b := tc.fields.Hash(info(tc.a)), otherFields.Hash(info(tc.b))
			if a == workload.SchedulingHashUnknown || b == workload.SchedulingHashUnknown {
				t.Fatalf("Hash() = (%q, %q), want known hashes", a, b)
			}
			if gotSame := a == b; gotSame != tc.wantSame {
				t.Errorf("Hash() = (%q, %q), want equal: %v", a, b, tc.wantSame)
			}
		})
	}
}
