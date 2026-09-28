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
	"errors"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/utils/clock"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/resources"
	"sigs.k8s.io/kueue/pkg/scheduler/preemption/common"
	"sigs.k8s.io/kueue/pkg/scheduler/preemption/config/filters"
	utilslices "sigs.k8s.io/kueue/pkg/util/slices"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingalpha "sigs.k8s.io/kueue/pkg/util/testing/v1alpha1"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/pkg/workload"
)

func TestPreemptionEvaluatorCandidates(t *testing.T) {
	now := time.Now()

	baseCqs := []*kueue.ClusterQueue{
		utiltestingapi.MakeClusterQueue("a").
			Cohort("all").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
				Resource(corev1.ResourceCPU, "1").Obj()).
			Obj(),
		utiltestingapi.MakeClusterQueue("b").
			Cohort("all").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
				Resource(corev1.ResourceCPU, "1").Obj()).
			Obj(),
	}

	unitWl := *utiltestingapi.MakeWorkload("unit", "").Request(corev1.ResourceCPU, "1")

	tests := map[string]struct {
		cohorts       []*kueue.Cohort
		clusterQueues []*kueue.ClusterQueue
		config        kueuealpha.PreemptionConfig
		admitted      []kueue.Workload
		preemptorWl   *kueue.Workload
		preemptorCq   kueue.ClusterQueueReference
		// Default testing value: Always
		trigger        kueuealpha.PreemptionConfigActivationTrigger
		client         client.Reader
		wantCandidates []string
		wantErrs       []*filters.FilterBuildError
	}{
		"no candidates for empty config": {
			clusterQueues: baseCqs,
			config:        *utiltestingalpha.MakePreemptionConfig("test").Obj(),
			admitted: []kueue.Workload{
				*unitWl.Clone().Name("a1").SimpleReserveQuota("a", "default", now).Obj(),
				*unitWl.Clone().Name("a2").SimpleReserveQuota("a", "default", now).Obj(),
			},
			preemptorWl:    unitWl.Clone().Name("a-incoming").Obj(),
			preemptorCq:    "a",
			wantCandidates: []string{},
		},
		"no candidates for rule without selectors": {
			clusterQueues: baseCqs,
			config: *utiltestingalpha.MakePreemptionConfig("test").
				Rule("test", kueuealpha.Always).Obj(),
			admitted: []kueue.Workload{
				*unitWl.Clone().Name("a1").SimpleReserveQuota("a", "default", now).Obj(),
				*unitWl.Clone().Name("a2").SimpleReserveQuota("a", "default", now).Obj(),
			},
			preemptorWl:    unitWl.Clone().Name("a-incoming").Obj(),
			preemptorCq:    "a",
			wantCandidates: []string{},
		},
		"returns error for selector with invalid label's operator while matching preemptor's workload": {
			clusterQueues: baseCqs,
			config: *utiltestingalpha.MakePreemptionConfig("test").
				RuleWithPreemptorSelector(
					"test",
					kueuealpha.Always,
					&metav1.LabelSelector{
						MatchExpressions: []metav1.LabelSelectorRequirement{
							{
								Key:      "test",
								Operator: "invalid",
							},
						},
					},
				).Obj(),
			admitted: []kueue.Workload{
				*unitWl.Clone().Name("a1").SimpleReserveQuota("a", "default", now).Obj(),
				*unitWl.Clone().Name("a2").SimpleReserveQuota("a", "default", now).Obj(),
			},
			preemptorWl: unitWl.Clone().Name("a-incoming").Obj(),
			preemptorCq: "a",
			wantErrs: []*filters.FilterBuildError{
				{
					Filter: filters.FilterPreemptorSelector,
					Reason: filters.ReasonInvalidSelector,
				},
			},
		},
		"returns error for candidate selector with invalid label selector operator": {
			clusterQueues: baseCqs,
			config: *utiltestingalpha.MakePreemptionConfig("test").
				Rule("test", kueuealpha.Always,
					utiltestingalpha.MakeCandidateSelector(kueuealpha.WithinClusterQueue).
						LabelSelector(&metav1.LabelSelector{
							MatchExpressions: []metav1.LabelSelectorRequirement{
								{Key: "test", Operator: "invalid"},
							},
						}).Obj(),
				).Obj(),
			admitted: []kueue.Workload{
				*unitWl.Clone().Name("a1").SimpleReserveQuota("a", "default", now).Obj(),
			},
			preemptorWl: unitWl.Clone().Name("a-incoming").Obj(),
			preemptorCq: "a",
			wantErrs: []*filters.FilterBuildError{
				{
					Filter: filters.FilterLabelSelector,
					Reason: filters.ReasonInvalidSelector,
				},
			},
		},
		"returns error for candidate selector with unsupported scope": {
			clusterQueues: baseCqs,
			config: *utiltestingalpha.MakePreemptionConfig("test").
				Rule("test", kueuealpha.Always,
					utiltestingalpha.MakeCandidateSelector(kueuealpha.PreemptionConfigPreemptionQueueScope("InvalidScope")).Obj(),
				).Obj(),
			admitted: []kueue.Workload{
				*unitWl.Clone().Name("a1").SimpleReserveQuota("a", "default", now).Obj(),
			},
			preemptorWl: unitWl.Clone().Name("a-incoming").Obj(),
			preemptorCq: "a",
			wantErrs: []*filters.FilterBuildError{
				{
					Filter: filters.FilterScope,
					Reason: filters.ReasonUnsupportedScope,
				},
			},
		},
		"returns error for candidate selector with unsupported priority mode": {
			clusterQueues: baseCqs,
			config: *utiltestingalpha.MakePreemptionConfig("test").
				Rule("test", kueuealpha.Always,
					utiltestingalpha.MakeCandidateSelector(kueuealpha.WithinClusterQueue).
						Priority(kueuealpha.PreemptionConfigPriorityMode("InvalidMode"), kueuealpha.LessThan).Obj(),
				).Obj(),
			admitted: []kueue.Workload{
				*unitWl.Clone().Name("a1").SimpleReserveQuota("a", "default", now).Obj(),
			},
			preemptorWl: unitWl.Clone().Name("a-incoming").Obj(),
			preemptorCq: "a",
			wantErrs: []*filters.FilterBuildError{
				{
					Filter: filters.FilterPriority,
					Reason: filters.ReasonUnsupportedMode,
				},
			},
		},
		"returns joined error for multiple invalid candidate selectors": {
			clusterQueues: baseCqs,
			config: *utiltestingalpha.MakePreemptionConfig("test").
				Rule("test", kueuealpha.Always,
					utiltestingalpha.MakeCandidateSelector(kueuealpha.PreemptionConfigPreemptionQueueScope("InvalidScope")).Obj(),
					utiltestingalpha.MakeCandidateSelector(kueuealpha.WithinClusterQueue).
						Priority(kueuealpha.PreemptionConfigPriorityMode("InvalidMode"), kueuealpha.LessThan).Obj(),
				).Obj(),
			admitted: []kueue.Workload{
				*unitWl.Clone().Name("a1").SimpleReserveQuota("a", "default", now).Obj(),
			},
			preemptorWl: unitWl.Clone().Name("a-incoming").Obj(),
			preemptorCq: "a",
			wantErrs: []*filters.FilterBuildError{
				{
					Filter: filters.FilterScope,
					Reason: filters.ReasonUnsupportedScope,
				},
				{
					Filter: filters.FilterPriority,
					Reason: filters.ReasonUnsupportedMode,
				},
			},
		},
		"selects candidates for CQ without cohort": {
			clusterQueues: []*kueue.ClusterQueue{
				utiltestingapi.MakeClusterQueue("a").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "2").Obj()).
					Obj(),
				utiltestingapi.MakeClusterQueue("b").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "2").Obj()).
					Obj(),
			},
			config: *utiltestingalpha.MakePreemptionConfig("test").
				Rule("test", kueuealpha.Always,
					utiltestingalpha.MakeCandidateSelector(kueuealpha.WithinCohortTree).Obj(),
				).Obj(),
			admitted: []kueue.Workload{
				*unitWl.Clone().Name("a1").SimpleReserveQuota("a", "default", now).Obj(),
				*unitWl.Clone().Name("a2").SimpleReserveQuota("a", "default", now).Obj(),
				*unitWl.Clone().Name("b1").SimpleReserveQuota("b", "default", now).Obj(),
			},
			preemptorWl:    unitWl.Clone().Name("a-incoming").Obj(),
			preemptorCq:    "a",
			wantCandidates: []string{"a1", "a2"},
		},
		"selects candidates for trigger which is present in config": {
			clusterQueues: baseCqs,
			config: *utiltestingalpha.MakePreemptionConfig("test").
				Rule("test", kueuealpha.InsufficientQuota,
					utiltestingalpha.MakeCandidateSelector(kueuealpha.WithinCohortTree).Obj(),
				).Obj(),
			admitted: []kueue.Workload{
				*unitWl.Clone().Name("a1").SimpleReserveQuota("a", "default", now).Obj(),
				*unitWl.Clone().Name("a2").SimpleReserveQuota("a", "default", now).Obj(),
			},
			preemptorWl:    unitWl.Clone().Name("a-incoming").Obj(),
			preemptorCq:    "a",
			trigger:        kueuealpha.InsufficientQuota,
			wantCandidates: []string{"a1", "a2"},
		},
		"returns empty candidates for trigger which is absent in config": {
			clusterQueues: baseCqs,
			config: *utiltestingalpha.MakePreemptionConfig("test").
				Rule("test", kueuealpha.InsufficientQuota,
					utiltestingalpha.MakeCandidateSelector(kueuealpha.WithinCohortTree).Obj(),
				).Obj(),
			admitted: []kueue.Workload{
				*unitWl.Clone().Name("a1").SimpleReserveQuota("a", "default", now).Obj(),
				*unitWl.Clone().Name("a2").SimpleReserveQuota("a", "default", now).Obj(),
			},
			preemptorWl:    unitWl.Clone().Name("a-incoming").Obj(),
			preemptorCq:    "a",
			trigger:        kueuealpha.QuotaFeasibleAndInsufficientTopology,
			wantCandidates: []string{},
		},
		"rule with nil preemptor selector matches any preemptor workload": {
			clusterQueues: baseCqs,
			config: *utiltestingalpha.MakePreemptionConfig("test").
				Rule("test", kueuealpha.Always,
					utiltestingalpha.MakeCandidateSelector(kueuealpha.WithinCohortTree).Obj(),
				).Obj(),
			admitted: []kueue.Workload{
				*unitWl.Clone().Name("a1").SimpleReserveQuota("a", "default", now).Obj(),
				*unitWl.Clone().Name("a2").SimpleReserveQuota("a", "default", now).Obj(),
			},
			preemptorWl:    unitWl.Clone().Name("a-incoming").Label("arbitrary", "label").Obj(),
			preemptorCq:    "a",
			wantCandidates: []string{"a1", "a2"},
		},
		"rule with matching preemptor labels selector is triggered for matching workload": {
			clusterQueues: baseCqs,
			config: *utiltestingalpha.MakePreemptionConfig("test").
				RuleWithPreemptorSelector(
					"test",
					kueuealpha.Always,
					&metav1.LabelSelector{
						MatchLabels: map[string]string{"active": "true"},
					},
					utiltestingalpha.MakeCandidateSelector(kueuealpha.WithinCohortTree).Obj(),
				).Obj(),
			admitted: []kueue.Workload{
				*unitWl.Clone().Name("a1").SimpleReserveQuota("a", "default", now).Obj(),
				*unitWl.Clone().Name("a2").SimpleReserveQuota("a", "default", now).Obj(),
			},
			preemptorWl:    unitWl.Clone().Name("a-incoming").Label("active", "true").Obj(),
			preemptorCq:    "a",
			wantCandidates: []string{"a1", "a2"},
		},
		"rule does not apply because of not matching preemptor labels selector": {
			clusterQueues: baseCqs,
			config: *utiltestingalpha.MakePreemptionConfig("test").
				RuleWithPreemptorSelector(
					"test",
					kueuealpha.Always,
					&metav1.LabelSelector{
						MatchLabels: map[string]string{"active": "true"},
					},
					utiltestingalpha.MakeCandidateSelector(kueuealpha.WithinCohortTree).Obj(),
				).Obj(),
			admitted: []kueue.Workload{
				*unitWl.Clone().Name("a1").SimpleReserveQuota("a", "default", now).Obj(),
				*unitWl.Clone().Name("a2").SimpleReserveQuota("a", "default", now).Obj(),
			},
			preemptorWl:    unitWl.Clone().Name("a-incoming").Obj(),
			preemptorCq:    "a",
			wantCandidates: []string{},
		},
		"WithinClusterQueue selects only candidates in the preemptor's ClusterQueue": {
			clusterQueues: baseCqs,
			config: *utiltestingalpha.MakePreemptionConfig("test").
				Rule("test", kueuealpha.Always,
					utiltestingalpha.MakeCandidateSelector(kueuealpha.WithinClusterQueue).Obj(),
				).Obj(),
			admitted: []kueue.Workload{
				*unitWl.Clone().Name("a1").SimpleReserveQuota("a", "default", now).Obj(),
				*unitWl.Clone().Name("b1").SimpleReserveQuota("b", "default", now).Obj(),
			},
			preemptorWl:    unitWl.Clone().Name("a-incoming").Obj(),
			preemptorCq:    "a",
			wantCandidates: []string{"a1"},
		},
		"WithinLocalQueue selects only candidates in the exact same LocalQueue and Namespace": {
			clusterQueues: baseCqs,
			config: *utiltestingalpha.MakePreemptionConfig("test").
				Rule("test", kueuealpha.Always,
					utiltestingalpha.MakeCandidateSelector(kueuealpha.WithinLocalQueue).Obj(),
				).Obj(),
			admitted: []kueue.Workload{
				*utiltestingapi.MakeWorkload("same-lq", "ns1").Request(corev1.ResourceCPU, "1").Queue("lq1").SimpleReserveQuota("a", "default", now).Obj(),
				*utiltestingapi.MakeWorkload("diff-lq", "ns1").Request(corev1.ResourceCPU, "1").Queue("lq2").SimpleReserveQuota("a", "default", now).Obj(),
				*utiltestingapi.MakeWorkload("diff-ns", "ns2").Request(corev1.ResourceCPU, "1").Queue("lq1").SimpleReserveQuota("a", "default", now).Obj(),
			},
			preemptorWl:    utiltestingapi.MakeWorkload("incoming", "ns1").Request(corev1.ResourceCPU, "1").Queue("lq1").Obj(),
			preemptorCq:    "a",
			wantCandidates: []string{"same-lq"},
		},
		"WithinParentCohort selects candidates from immediate parent cohort only": {
			cohorts: []*kueue.Cohort{
				utiltestingapi.MakeCohort("root").Obj(),
				utiltestingapi.MakeCohort("subA").Parent("root").Obj(),
				utiltestingapi.MakeCohort("subB").Parent("root").Obj(),
			},
			clusterQueues: []*kueue.ClusterQueue{
				utiltestingapi.MakeClusterQueue("a").Cohort("subA").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "1").Obj()).Obj(),
				utiltestingapi.MakeClusterQueue("a-sibling").Cohort("subA").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "1").Obj()).Obj(),
				utiltestingapi.MakeClusterQueue("b").Cohort("subB").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "1").Obj()).Obj(),
			},
			config: *utiltestingalpha.MakePreemptionConfig("test").
				Rule("test", kueuealpha.Always,
					utiltestingalpha.MakeCandidateSelector(kueuealpha.WithinParentCohort).Obj(),
				).Obj(),
			admitted: []kueue.Workload{
				*unitWl.Clone().Name("a1").SimpleReserveQuota("a", "default", now).Obj(),
				*unitWl.Clone().Name("a-sib1").SimpleReserveQuota("a-sibling", "default", now).Obj(),
				*unitWl.Clone().Name("b1").SimpleReserveQuota("b", "default", now).Obj(),
			},
			preemptorWl:    unitWl.Clone().Name("incoming").Obj(),
			preemptorCq:    "a",
			wantCandidates: []string{"a1", "a-sib1"},
		},
		"returns candidates from ClusterQueues not under the same root": {
			clusterQueues: []*kueue.ClusterQueue{
				utiltestingapi.MakeClusterQueue("a").
					Cohort("a-cohort").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "1").Obj()).
					Obj(),
				utiltestingapi.MakeClusterQueue("b").
					Cohort("b-cohort").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "1").Obj()).
					Obj(),
				utiltestingapi.MakeClusterQueue("c").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "1").Obj()).
					Obj(),
			},
			config: *utiltestingalpha.MakePreemptionConfig("test").
				Rule("test", kueuealpha.Always,
					utiltestingalpha.MakeCandidateSelector(kueuealpha.AnyClusterQueue).Obj(),
				).Obj(),
			admitted: []kueue.Workload{
				*unitWl.Clone().Name("a1").SimpleReserveQuota("a", "default", now).Obj(),
				*unitWl.Clone().Name("b1").SimpleReserveQuota("b", "default", now).Obj(),
				*unitWl.Clone().Name("c1").SimpleReserveQuota("c", "default", now).Obj(),
			},
			preemptorWl:    unitWl.Clone().Name("a-incoming").Obj(),
			preemptorCq:    "a",
			wantCandidates: []string{"a1", "b1", "c1"},
		},
		"returns non repeating candidates even when the same candidates are matched by several rules of a trigger": {
			clusterQueues: baseCqs,
			config: *utiltestingalpha.MakePreemptionConfig("test").
				Rule("first-rule", kueuealpha.Always,
					utiltestingalpha.MakeCandidateSelector(kueuealpha.WithinClusterQueue).Obj(),
				).
				Rule("second-rule", kueuealpha.Always,
					utiltestingalpha.MakeCandidateSelector(kueuealpha.WithinCohortTree).Obj(),
				).Obj(),
			admitted: []kueue.Workload{
				*unitWl.Clone().Name("a1").SimpleReserveQuota("a", "default", now).Obj(),
				*unitWl.Clone().Name("b1").SimpleReserveQuota("b", "default", now).Obj(),
			},
			preemptorWl:    unitWl.Clone().Name("a-incoming").Obj(),
			preemptorCq:    "a",
			wantCandidates: []string{"a1", "b1"},
		},
		"LabelSelector filters candidate workloads matching label selector": {
			clusterQueues: baseCqs,
			config: *utiltestingalpha.MakePreemptionConfig("test").
				Rule("label-selector-rule", kueuealpha.Always,
					utiltestingalpha.MakeCandidateSelector(kueuealpha.WithinClusterQueue).
						LabelSelector(&metav1.LabelSelector{
							MatchLabels: map[string]string{"env": "preemptible"},
						}).Obj(),
				).Obj(),
			admitted: []kueue.Workload{
				*unitWl.Clone().Name("a1").
					Label("env", "preemptible").
					SimpleReserveQuota("a", "default", now).Obj(),
				*unitWl.Clone().Name("a2").
					Label("env", "guaranteed").
					SimpleReserveQuota("a", "default", now).Obj(),
			},
			preemptorWl:    unitWl.Clone().Name("a-incoming").Obj(),
			preemptorCq:    "a",
			wantCandidates: []string{"a1"},
		},
		"ClusterQueueSelector filters candidates by matching ClusterQueue labels": {
			clusterQueues: []*kueue.ClusterQueue{
				utiltestingapi.MakeClusterQueue("a").
					Cohort("all").
					Label("tier", "preemptible").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "1").Obj()).
					Obj(),
				utiltestingapi.MakeClusterQueue("b").
					Cohort("all").
					Label("tier", "protected").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "1").Obj()).
					Obj(),
			},
			config: *utiltestingalpha.MakePreemptionConfig("test").
				Rule("cq-selector-rule", kueuealpha.Always,
					utiltestingalpha.MakeCandidateSelector(kueuealpha.WithinCohortTree).
						ClusterQueueSelector(&metav1.LabelSelector{
							MatchLabels: map[string]string{"tier": "preemptible"},
						}).Obj(),
				).Obj(),
			admitted: []kueue.Workload{
				*unitWl.Clone().Name("a1").SimpleReserveQuota("a", "default", now).Obj(),
				*unitWl.Clone().Name("b1").SimpleReserveQuota("b", "default", now).Obj(),
			},
			preemptorWl:    unitWl.Clone().Name("a-incoming").Obj(),
			preemptorCq:    "a",
			wantCandidates: []string{"a1"},
		},
		"Priority filters candidates with higher priority": {
			clusterQueues: baseCqs,
			config: *utiltestingalpha.MakePreemptionConfig("test").
				Rule("priority-rule", kueuealpha.Always,
					utiltestingalpha.MakeCandidateSelector(kueuealpha.WithinClusterQueue).
						Priority(kueuealpha.Base, kueuealpha.LessThan).Obj(),
				).Obj(),
			admitted: []kueue.Workload{
				*unitWl.Clone().Name("a1").Priority(50).SimpleReserveQuota("a", "default", now).Obj(),
				*unitWl.Clone().Name("a2").Priority(150).SimpleReserveQuota("a", "default", now).Obj(),
			},
			preemptorWl:    unitWl.Clone().Name("a-incoming").Priority(100).Obj(),
			preemptorCq:    "a",
			wantCandidates: []string{"a1"},
		},
		"NumericLabels filters candidates with numeric label constraint": {
			clusterQueues: baseCqs,
			config: *utiltestingalpha.MakePreemptionConfig("test").
				Rule("numeric-label-rule", kueuealpha.Always,
					utiltestingalpha.MakeCandidateSelector(kueuealpha.WithinClusterQueue).
						NumericLabels(kueuealpha.PreemptionConfigNumericLabelConstraint{
							Key:        "tpus",
							Comparison: ptr.To(kueuealpha.LessThanOrEqual),
						}).Obj(),
				).Obj(),
			admitted: []kueue.Workload{
				*unitWl.Clone().Name("a1").Label("tpus", "4").SimpleReserveQuota("a", "default", now).Obj(),
				*unitWl.Clone().Name("a2").Label("tpus", "16").SimpleReserveQuota("a", "default", now).Obj(),
			},
			preemptorWl:    unitWl.Clone().Name("a-incoming").Label("tpus", "8").Obj(),
			preemptorCq:    "a",
			wantCandidates: []string{"a1"},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.ConfigurablePreemptions, true)
			ctx, log := utiltesting.ContextWithLog(t)
			for i := range tc.admitted {
				tc.admitted[i].UID = types.UID(tc.admitted[i].Name)
			}

			cl := utiltesting.NewClientBuilder().
				WithLists(&kueue.WorkloadList{Items: tc.admitted}).
				Build()

			cqCache := schdcache.New(cl)
			cqCache.AddOrUpdateResourceFlavor(log, utiltestingapi.MakeResourceFlavor("default").Obj())
			cqCache.AddOrUpdateResourceFlavor(log, utiltestingapi.MakeResourceFlavor("other-flavour").Obj())

			for _, cq := range tc.clusterQueues {
				if err := cqCache.AddClusterQueue(ctx, cq); err != nil {
					t.Fatalf("Couldn't add ClusterQueue to cache: %v", err)
				}
			}
			for _, cohort := range tc.cohorts {
				if err := cqCache.AddOrUpdateCohort(cohort); err != nil {
					t.Fatalf("Couldn't add Cohort to cache: %v", err)
				}
			}

			snapshot, err := cqCache.Snapshot(ctx)
			if err != nil {
				t.Fatalf("unexpected error while building snapshot: %v", err)
			}

			evaluator := NewPreemptionEvaluator(ctx, log, clock.RealClock{}, tc.config, candidatesByName)

			wlInfo := workload.NewInfo(log, tc.preemptorWl)
			wlInfo.ClusterQueue = tc.preemptorCq

			trigger := tc.trigger
			if trigger == "" {
				trigger = kueuealpha.Always
			}
			frsNeedPreemption := sets.New(resources.FlavorResource{Flavor: "default", Resource: corev1.ResourceCPU})
			candidates, err := evaluator.candidatesFor(snapshot, wlInfo, frsNeedPreemption, trigger)
			if len(tc.wantErrs) > 0 {
				if err == nil {
					t.Fatalf("candidatesFor() expected error, got nil")
				}
				for _, wantErr := range tc.wantErrs {
					if !errors.Is(err, wantErr) {
						t.Errorf("candidatesFor() missing expected error %v in: %v", wantErr, err)
					}
				}
				return
			}
			if err != nil {
				t.Fatalf("candidatesFor() unexpected error: %v", err)
			}

			// Candidates are not ordered, so compare them as sorted lists.
			gotCandidates := slices.Sorted(slices.Values(utilslices.Map(candidates, func(candidate **configurableCandidate) string {
				return (*candidate).WlInfo.Obj.Name
			})))
			wantCandidates := slices.Sorted(slices.Values(tc.wantCandidates))
			if diff := cmp.Diff(wantCandidates, gotCandidates, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("Selected candidates (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestPreemptionEvaluatorSelectorIndexes(t *testing.T) {
	const configName = "test-config"

	now := time.Now()

	baseCqs := []*kueue.ClusterQueue{
		utiltestingapi.MakeClusterQueue("a").
			Cohort("all").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
				Resource(corev1.ResourceCPU, "1").Obj()).
			Obj(),
		utiltestingapi.MakeClusterQueue("b").
			Cohort("all").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
				Resource(corev1.ResourceCPU, "1").Obj()).
			Obj(),
	}

	unitWl := *utiltestingapi.MakeWorkload("unit", "").Request(corev1.ResourceCPU, "1")
	candidate := func(name string, indexes map[string][]int) *configurableCandidate {
		return &configurableCandidate{
			WlInfo:                    wlInfoWithName(name),
			ConfigName:                configName,
			RuleNameToSelectorIndexes: indexes,
		}
	}

	tests := map[string]struct {
		clusterQueues []*kueue.ClusterQueue
		config        kueuealpha.PreemptionConfig
		admitted      []kueue.Workload
		preemptorWl   *kueue.Workload
		preemptorCq   kueue.ClusterQueueReference
		// Default testing value: Always
		trigger        kueuealpha.PreemptionConfigActivationTrigger
		wantCandidates []*configurableCandidate
	}{
		"ConfigName and selector's index are added to the candidates": {
			clusterQueues: baseCqs,
			config: *utiltestingalpha.MakePreemptionConfig(configName).
				Rule("test", kueuealpha.Always,
					utiltestingalpha.MakeCandidateSelector(kueuealpha.WithinCohortTree).Obj(),
				).Obj(),
			admitted: []kueue.Workload{
				*unitWl.Clone().Name("a1").SimpleReserveQuota("a", "default", now).Obj(),
				*unitWl.Clone().Name("a2").SimpleReserveQuota("a", "default", now).Obj(),
			},
			preemptorWl: unitWl.Clone().Name("a-incoming").Obj(),
			preemptorCq: "a",
			wantCandidates: []*configurableCandidate{
				candidate("a1", map[string][]int{"test": {0}}),
				candidate("a2", map[string][]int{"test": {0}}),
			},
		},
		"Candidate match multiple rules": {
			clusterQueues: baseCqs,
			config: *utiltestingalpha.MakePreemptionConfig(configName).
				Rule("test1", kueuealpha.Always,
					utiltestingalpha.MakeCandidateSelector(kueuealpha.WithinCohortTree).Obj(),
				).
				Rule("test2", kueuealpha.Always,
					utiltestingalpha.MakeCandidateSelector(kueuealpha.AnyClusterQueue).Obj(),
				).Obj(),
			admitted: []kueue.Workload{
				*unitWl.Clone().Name("a1").SimpleReserveQuota("a", "default", now).Obj(),
				*unitWl.Clone().Name("a2").SimpleReserveQuota("a", "default", now).Obj(),
			},
			preemptorWl: unitWl.Clone().Name("a-incoming").Obj(),
			preemptorCq: "a",
			wantCandidates: []*configurableCandidate{
				candidate("a1", map[string][]int{"test1": {0}, "test2": {0}}),
				candidate("a2", map[string][]int{"test1": {0}, "test2": {0}}),
			},
		},
		"Candidate match multiple rules related to the trigger": {
			clusterQueues: baseCqs,
			config: *utiltestingalpha.MakePreemptionConfig(configName).
				Rule("test1", kueuealpha.Always,
					utiltestingalpha.MakeCandidateSelector(kueuealpha.WithinCohortTree).Obj(),
				).
				Rule("test2", kueuealpha.InsufficientQuota,
					utiltestingalpha.MakeCandidateSelector(kueuealpha.WithinCohortTree).Obj(),
				).
				Rule("test3", kueuealpha.InsufficientQuota,
					utiltestingalpha.MakeCandidateSelector(kueuealpha.AnyClusterQueue).Obj(),
				).Obj(),
			admitted: []kueue.Workload{
				*unitWl.Clone().Name("a1").SimpleReserveQuota("a", "default", now).Obj(),
				*unitWl.Clone().Name("a2").SimpleReserveQuota("a", "default", now).Obj(),
			},
			preemptorWl: unitWl.Clone().Name("a-incoming").Obj(),
			preemptorCq: "a",
			trigger:     kueuealpha.InsufficientQuota,
			wantCandidates: []*configurableCandidate{
				candidate("a1", map[string][]int{"test2": {0}, "test3": {0}}),
				candidate("a2", map[string][]int{"test2": {0}, "test3": {0}}),
			},
		},
		"Candidates match multiple selectors": {
			clusterQueues: baseCqs,
			config: *utiltestingalpha.MakePreemptionConfig(configName).
				Rule("test", kueuealpha.Always,
					utiltestingalpha.MakeCandidateSelector(kueuealpha.WithinCohortTree).Obj(),
					utiltestingalpha.MakeCandidateSelector(kueuealpha.AnyClusterQueue).Obj(),
				).Obj(),
			admitted: []kueue.Workload{
				*unitWl.Clone().Name("a1").SimpleReserveQuota("a", "default", now).Obj(),
				*unitWl.Clone().Name("a2").SimpleReserveQuota("a", "default", now).Obj(),
			},
			preemptorWl: unitWl.Clone().Name("a-incoming").Obj(),
			preemptorCq: "a",
			wantCandidates: []*configurableCandidate{
				candidate("a1", map[string][]int{"test": {0, 1}}),
				candidate("a2", map[string][]int{"test": {0, 1}}),
			},
		},
		"Candidate matches only the second selector": {
			clusterQueues: baseCqs,
			config: *utiltestingalpha.MakePreemptionConfig(configName).
				Rule("test", kueuealpha.Always,
					utiltestingalpha.MakeCandidateSelector(kueuealpha.WithinCohortTree).
						LabelSelector(&metav1.LabelSelector{MatchLabels: map[string]string{"group": "other"}}).
						Obj(),
					utiltestingalpha.MakeCandidateSelector(kueuealpha.WithinCohortTree).
						LabelSelector(&metav1.LabelSelector{MatchLabels: map[string]string{"group": "selected"}}).
						Obj(),
				).Obj(),
			admitted: []kueue.Workload{
				*unitWl.Clone().Name("a1").Label("group", "selected").SimpleReserveQuota("a", "default", now).Obj(),
			},
			preemptorWl: unitWl.Clone().Name("a-incoming").Obj(),
			preemptorCq: "a",
			wantCandidates: []*configurableCandidate{
				candidate("a1", map[string][]int{"test": {1}}),
			},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.ConfigurablePreemptions, true)
			ctx, log := utiltesting.ContextWithLog(t)
			for i := range tc.admitted {
				tc.admitted[i].UID = types.UID(tc.admitted[i].Name)
			}

			cl := utiltesting.NewClientBuilder().
				WithLists(&kueue.WorkloadList{Items: tc.admitted}).
				Build()

			cqCache := schdcache.New(cl)
			cqCache.AddOrUpdateResourceFlavor(log, utiltestingapi.MakeResourceFlavor("default").Obj())

			for _, cq := range tc.clusterQueues {
				if err := cqCache.AddClusterQueue(ctx, cq); err != nil {
					t.Fatalf("Couldn't add ClusterQueue to cache: %v", err)
				}
			}

			snapshot, err := cqCache.Snapshot(ctx)
			if err != nil {
				t.Fatalf("unexpected error while building snapshot: %v", err)
			}

			evaluator := NewPreemptionEvaluator(ctx, log, clock.RealClock{}, tc.config, candidatesByName)

			wlInfo := workload.NewInfo(log, tc.preemptorWl)
			wlInfo.ClusterQueue = tc.preemptorCq

			trigger := tc.trigger
			if trigger == "" {
				trigger = kueuealpha.Always
			}

			frsNeedPreemption := sets.New(resources.FlavorResource{Flavor: "default", Resource: corev1.ResourceCPU})
			candidates, err := evaluator.candidatesFor(snapshot, wlInfo, frsNeedPreemption, trigger)
			if err != nil {
				t.Errorf("candidatesFor() error: %v", err)
				return
			}

			candidateCmpOpts := []cmp.Option{
				// Compare only names for workload.Info
				cmpopts.AcyclicTransformer("Info", func(wlInfo *workload.Info) string {
					return wlInfo.Obj.Name
				}),
				// Sort candidates by name to have consistent output
				cmpopts.SortSlices(func(a, b *configurableCandidate) bool {
					return a.WlInfo.Obj.Name < b.WlInfo.Obj.Name
				}),
				cmpopts.EquateEmpty(),
			}
			if diff := cmp.Diff(tc.wantCandidates, candidates, candidateCmpOpts...); diff != "" {
				t.Errorf("Selected candidates (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestPreemptionEvaluatorFindCandidates(t *testing.T) {
	now := time.Now()
	unitWl := *utiltestingapi.MakeWorkload("unit", "").Request(corev1.ResourceCPU, "1")
	clusterQueue := utiltestingapi.MakeClusterQueue("a").
		ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
			Resource(corev1.ResourceCPU, "3").Obj()).
		Obj()
	fr := resources.FlavorResource{Flavor: "default", Resource: corev1.ResourceCPU}

	lowerTierSelector := utiltestingalpha.MakeCandidateSelector(kueuealpha.WithinClusterQueue).
		NumericLabels(kueuealpha.PreemptionConfigNumericLabelConstraint{
			Key:        "preemption-tier",
			Comparison: ptr.To(kueuealpha.LessThan),
		}).Obj()
	// multiTriggerConfig allows preempting the workloads of a lower tier unconditionally,
	// and the remaining workloads of the ClusterQueue only while the quota is insufficient.
	multiTriggerConfig := *utiltestingalpha.MakePreemptionConfig("test").
		Rule("tier-rule", kueuealpha.Always, lowerTierSelector).
		Rule("within-cluster-queue-rule", kueuealpha.InsufficientQuota,
			utiltestingalpha.MakeCandidateSelector(kueuealpha.WithinClusterQueue).Obj(),
		).Obj()
	// topologyTriggerConfig allows preempting the workloads of a lower tier unconditionally,
	// and the remaining workloads of the ClusterQueue only once the quota is sufficient.
	topologyTriggerConfig := *utiltestingalpha.MakePreemptionConfig("test").
		Rule("tier-rule", kueuealpha.Always, lowerTierSelector).
		Rule("within-cluster-queue-rule", kueuealpha.QuotaFeasibleAndInsufficientTopology,
			utiltestingalpha.MakeCandidateSelector(kueuealpha.WithinClusterQueue).Obj(),
		).Obj()

	cases := map[string]struct {
		// config is the PreemptionConfig of the evaluator; the evaluator is nil when unset.
		config    *kueuealpha.PreemptionConfig
		admitted  []kueue.Workload
		preemptor *kueue.Workload
		// neverFits makes the preemptor never fit, regardless of the quota, as when
		// no topology assignment can be found for it.
		neverFits       bool
		wantInterrupted bool
		wantTargets     []string
	}{
		"nil evaluator yields no candidates": {
			admitted: []kueue.Workload{
				*unitWl.Clone().Name("a1").SimpleReserveQuota("a", "default", now).Obj(),
			},
			preemptor:   unitWl.Clone().Name("a_incoming").Request(corev1.ResourceCPU, "3").Obj(),
			wantTargets: nil,
		},
		"evaluator without rules yields no candidates": {
			config: utiltestingalpha.MakePreemptionConfig("test").Obj(),
			admitted: []kueue.Workload{
				*unitWl.Clone().Name("a1").SimpleReserveQuota("a", "default", now).Obj(),
			},
			preemptor:   unitWl.Clone().Name("a_incoming").Request(corev1.ResourceCPU, "3").Obj(),
			wantTargets: nil,
		},
		"stops as soon as the yield returns false": {
			config: &multiTriggerConfig,
			admitted: []kueue.Workload{
				*unitWl.Clone().Name("a1").SimpleReserveQuota("a", "default", now).Obj(),
			},
			// a1 has no tier, so it is only selected by the InsufficientQuota trigger,
			// and preempting it frees enough quota for the preemptor to fit.
			preemptor:       unitWl.Clone().Name("a_incoming").Label("preemption-tier", "5").Request(corev1.ResourceCPU, "3").Obj(),
			wantInterrupted: true,
			wantTargets:     []string{"/a1"},
		},
		"yields candidates across triggers and does not return candidate matched by multiple triggers twice": {
			config: &multiTriggerConfig,
			admitted: []kueue.Workload{
				*unitWl.Clone().Name("a1").Label("preemption-tier", "1").SimpleReserveQuota("a", "default", now).Obj(),
				*unitWl.Clone().Name("a2").Label("preemption-tier", "1").SimpleReserveQuota("a", "default", now).Obj(),
				*unitWl.Clone().Name("a3").SimpleReserveQuota("a", "default", now).Obj(),
			},
			// Needs 3 CPUs: Always removes a1 and a2 (2 CPUs), then InsufficientQuota
			// selects from WithinClusterQueue where a1 and a2 are already gone from snapshot,
			// so only a3 is added.
			preemptor:       unitWl.Clone().Name("a_incoming").Label("preemption-tier", "5").Request(corev1.ResourceCPU, "3").Obj(),
			wantInterrupted: true,
			wantTargets:     []string{"/a1", "/a2", "/a3"},
		},
		"stops after Always trigger when it frees enough quota": {
			config: &multiTriggerConfig,
			admitted: []kueue.Workload{
				*unitWl.Clone().Name("a1").Label("preemption-tier", "1").SimpleReserveQuota("a", "default", now).Obj(),
				*unitWl.Clone().Name("a2").Label("preemption-tier", "1").SimpleReserveQuota("a", "default", now).Obj(),
				*unitWl.Clone().Name("a3").SimpleReserveQuota("a", "default", now).Obj(),
			},
			// Needs 2 CPUs out of the fully used quota of 3: the Always trigger yields
			// a1 and a2, which frees enough quota, but the preemptor never fits.
			// As the quota fits, the InsufficientQuota trigger is not reached, and
			// a3 is not yielded even though the preemptor still doesn't fit.
			preemptor:       unitWl.Clone().Name("a_incoming").Label("preemption-tier", "5").Request(corev1.ResourceCPU, "2").Obj(),
			neverFits:       true,
			wantInterrupted: false,
			wantTargets:     []string{"/a1", "/a2"},
		},
		"yields the candidates of the QuotaFeasibleAndInsufficientTopology trigger once the quota fits": {
			config: &topologyTriggerConfig,
			admitted: []kueue.Workload{
				*unitWl.Clone().Name("a1").Label("preemption-tier", "1").SimpleReserveQuota("a", "default", now).Obj(),
				*unitWl.Clone().Name("a2").SimpleReserveQuota("a", "default", now).Obj(),
				*unitWl.Clone().Name("a3").SimpleReserveQuota("a", "default", now).Obj(),
			},
			// Needs 1 CPU: the Always trigger yields a1, which frees enough quota,
			// but the preemptor never fits, so the QuotaFeasibleAndInsufficientTopology
			// trigger yields the remaining workloads.
			preemptor:       unitWl.Clone().Name("a_incoming").Label("preemption-tier", "5").Obj(),
			neverFits:       true,
			wantInterrupted: false,
			wantTargets:     []string{"/a1", "/a2", "/a3"},
		},
		"does not yield the candidates of the QuotaFeasibleAndInsufficientTopology trigger while the quota does not fit": {
			config: &topologyTriggerConfig,
			admitted: []kueue.Workload{
				*unitWl.Clone().Name("a1").Label("preemption-tier", "1").SimpleReserveQuota("a", "default", now).Obj(),
				*unitWl.Clone().Name("a2").SimpleReserveQuota("a", "default", now).Obj(),
				*unitWl.Clone().Name("a3").SimpleReserveQuota("a", "default", now).Obj(),
			},
			// Needs 2 CPUs: the Always trigger yields a1, which doesn't free enough
			// quota, so the QuotaFeasibleAndInsufficientTopology trigger is not reached.
			preemptor:       unitWl.Clone().Name("a_incoming").Label("preemption-tier", "5").Request(corev1.ResourceCPU, "2").Obj(),
			wantInterrupted: false,
			wantTargets:     []string{"/a1"},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			// Given
			features.SetFeatureGateDuringTest(t, features.ConfigurablePreemptions, true)
			ctx, log := utiltesting.ContextWithLog(t)
			for i := range tc.admitted {
				tc.admitted[i].UID = types.UID(tc.admitted[i].Name)
			}
			cl := utiltesting.NewClientBuilder().
				WithLists(&kueue.WorkloadList{Items: tc.admitted}).
				Build()

			cqCache := schdcache.New(cl)
			cqCache.AddOrUpdateResourceFlavor(log, utiltestingapi.MakeResourceFlavor("default").Obj())
			if err := cqCache.AddClusterQueue(ctx, clusterQueue); err != nil {
				t.Fatalf("Couldn't add ClusterQueue to cache: %v", err)
			}
			snapshot, err := cqCache.Snapshot(ctx)
			if err != nil {
				t.Fatalf("unexpected error while building snapshot: %v", err)
			}

			var evaluator *PreemptionEvaluator
			if tc.config != nil {
				evaluator = NewPreemptionEvaluator(ctx, log, clock.RealClock{}, *tc.config, candidatesByName)
			}
			preemptor := workload.NewInfo(log, tc.preemptor)
			preemptor.ClusterQueue = "a"
			requestedCPU := preemptor.TotalRequests[0].Requests.ResourceValue(corev1.ResourceCPU)
			quotaFits := func() bool {
				return snapshot.ClusterQueue("a").Available(fr).CmpInt64(requestedCPU) >= 0
			}

			// When
			// The yield interrupts FindCandidates once the preemptor fits.
			var gotTargets []string
			gotInterrupted := evaluator.FindCandidates(snapshot, preemptor, sets.New(fr), quotaFits, func(target *common.Target) bool {
				gotTargets = append(gotTargets, string(workload.Key(target.WorkloadInfo.Obj)))
				return tc.neverFits || !quotaFits()
			})

			// Then
			if gotInterrupted != tc.wantInterrupted {
				t.Errorf("FindCandidates() got interrupted = %v, want %v", gotInterrupted, tc.wantInterrupted)
			}
			if diff := cmp.Diff(tc.wantTargets, gotTargets, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("FindCandidates() targets (-want,+got):\n%s", diff)
			}
		})
	}
}

// candidatesByName orders the candidates by name, so that the order in which
// they are yielded is predictable.
func candidatesByName(a, b *workload.Info) int {
	return strings.Compare(a.Obj.Name, b.Obj.Name)
}

func wlInfoWithName(name string) *workload.Info {
	return &workload.Info{
		Obj: &kueue.Workload{
			Name: name,
		},
	}
}
