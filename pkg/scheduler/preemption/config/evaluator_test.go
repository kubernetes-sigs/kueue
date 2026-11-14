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

	"github.com/go-logr/logr"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/component-base/featuregate"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/resources"
	"sigs.k8s.io/kueue/pkg/scheduler/preemption/config/filters"
	"sigs.k8s.io/kueue/pkg/scheduler/preemption/policy"
	utilslices "sigs.k8s.io/kueue/pkg/util/slices"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingalpha "sigs.k8s.io/kueue/pkg/util/testing/v1alpha1"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingnode "sigs.k8s.io/kueue/pkg/util/testingjobs/node"
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
				Rules(
					utiltestingalpha.MakePreemptionRule("test", kueuealpha.Always).
						PreemptorSelector(&metav1.LabelSelector{
							MatchExpressions: []metav1.LabelSelectorRequirement{
								{
									Key:      "test",
									Operator: "invalid",
								},
							},
						}).Obj(),
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
		"returns error for preemptor priority class selector with unsupported operator": {
			clusterQueues: baseCqs,
			config: *utiltestingalpha.MakePreemptionConfig("test").
				Rules(
					kueuealpha.PreemptionConfigPreemptionRule{
						Name:             "test",
						ActivationPolicy: kueuealpha.PreemptionConfigActivationPolicy{Trigger: kueuealpha.Always},
						PreemptorPriorityClassSelector: &kueuealpha.PreemptionConfigPriorityClassSelector{
							NameExpressions: []kueuealpha.PriorityClassSelectorRequirement{
								{
									Operator: kueuealpha.PriorityClassSelectorOperator("invalid"),
									Values:   []string{"high-priority"},
								},
							},
						},
					},
				).Obj(),
			admitted: []kueue.Workload{
				*unitWl.Clone().Name("a1").SimpleReserveQuota("a", "default", now).Obj(),
			},
			preemptorWl: unitWl.Clone().Name("a-incoming").Obj(),
			preemptorCq: "a",
			wantErrs: []*filters.FilterBuildError{
				{
					Filter: filters.FilterPreemptorPriorityClassSelector,
					Reason: filters.ReasonUnsupportedOperator,
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
				Rules(
					utiltestingalpha.MakePreemptionRule(
						"test",
						kueuealpha.Always,
						utiltestingalpha.MakeCandidateSelector(kueuealpha.WithinCohortTree).Obj(),
					).PreemptorSelector(&metav1.LabelSelector{
						MatchLabels: map[string]string{"active": "true"},
					}).Obj(),
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
				Rules(
					utiltestingalpha.MakePreemptionRule(
						"test",
						kueuealpha.Always,
						utiltestingalpha.MakeCandidateSelector(kueuealpha.WithinCohortTree).Obj(),
					).PreemptorSelector(&metav1.LabelSelector{
						MatchLabels: map[string]string{"active": "true"},
					}).Obj(),
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
		"rule with matching preemptor priority class selector and candidate priorityClassSelector": {
			clusterQueues: baseCqs,
			config: *utiltestingalpha.MakePreemptionConfig("test").
				Rules(
					utiltestingalpha.MakePreemptionRule(
						"priority-class-rule",
						kueuealpha.Always,
						utiltestingalpha.MakeCandidateSelector(kueuealpha.WithinClusterQueue).
							PriorityClassSourceRef(kueuealpha.WorkloadPriorityClassGroup, kueuealpha.WorkloadPriorityClassKind).
							PriorityClassIn("low-priority").
							Obj(),
					).
						PreemptorPriorityClassSourceRef(kueuealpha.WorkloadPriorityClassGroup, kueuealpha.WorkloadPriorityClassKind).
						PreemptorPriorityClassIn("high-priority").
						Obj(),
				).Obj(),
			admitted: []kueue.Workload{
				*unitWl.Clone().Name("a1").WorkloadPriorityClassRef("low-priority").SimpleReserveQuota("a", "default", now).Obj(),
				*unitWl.Clone().Name("a2").WorkloadPriorityClassRef("protected-priority").SimpleReserveQuota("a", "default", now).Obj(),
				*unitWl.Clone().Name("a3").PodPriorityClassRef("low-priority").SimpleReserveQuota("a", "default", now).Obj(),
			},
			preemptorWl:    unitWl.Clone().Name("a-incoming").Request(corev1.ResourceCPU, "3").WorkloadPriorityClassRef("high-priority").Obj(),
			preemptorCq:    "a",
			wantCandidates: []string{"a1"},
		},
		"rule does not apply because of excluded preemptor priority class selector": {
			clusterQueues: baseCqs,
			config: *utiltestingalpha.MakePreemptionConfig("test").
				Rules(
					utiltestingalpha.MakePreemptionRule(
						"priority-class-rule",
						kueuealpha.Always,
						utiltestingalpha.MakeCandidateSelector(kueuealpha.WithinClusterQueue).Obj(),
					).PreemptorPriorityClassNotIn("low-priority").Obj(),
				).Obj(),
			admitted: []kueue.Workload{
				*unitWl.Clone().Name("a1").SimpleReserveQuota("a", "default", now).Obj(),
			},
			preemptorWl:    unitWl.Clone().Name("a-incoming").WorkloadPriorityClassRef("low-priority").Obj(),
			preemptorCq:    "a",
			wantCandidates: []string{},
		},
		"rule does not apply because preemptor priority class sourceRef does not match": {
			clusterQueues: baseCqs,
			config: *utiltestingalpha.MakePreemptionConfig("test").
				Rules(
					utiltestingalpha.MakePreemptionRule(
						"priority-class-rule",
						kueuealpha.Always,
						utiltestingalpha.MakeCandidateSelector(kueuealpha.WithinClusterQueue).Obj(),
					).PreemptorPriorityClassSourceRef(kueuealpha.WorkloadPriorityClassGroup, kueuealpha.WorkloadPriorityClassKind).Obj(),
				).Obj(),
			admitted: []kueue.Workload{
				*unitWl.Clone().Name("a1").SimpleReserveQuota("a", "default", now).Obj(),
			},
			preemptorWl:    unitWl.Clone().Name("a-incoming").PodPriorityClassRef("high-priority").Obj(),
			preemptorCq:    "a",
			wantCandidates: []string{},
		},
		"NumericLabels filters candidates with numeric label constraint": {
			clusterQueues: baseCqs,
			config: *utiltestingalpha.MakePreemptionConfig("test").
				Rule("numeric-label-rule", kueuealpha.Always,
					utiltestingalpha.MakeCandidateSelector(kueuealpha.WithinClusterQueue).
						NumericLabels(kueuealpha.PreemptionConfigNumericLabelConstraint{
							Key:        "tpus",
							Comparison: new(kueuealpha.LessThanOrEqual),
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

			wlInfo := workload.NewInfo(log, tc.preemptorWl)
			wlInfo.ClusterQueue = tc.preemptorCq
			evaluator := NewPreemptionEvaluator(ctx, log, tc.config, wlInfo, candidatesByName)

			trigger := tc.trigger
			if trigger == "" {
				trigger = kueuealpha.Always
			}
			frsNeedPreemption := sets.New(resources.FlavorResource{Flavor: "default", Resource: corev1.ResourceCPU})
			candidates, err := evaluator.candidatesFor(snapshot, frsNeedPreemption, trigger)
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

func TestPreemptionEvaluatorOverlappingTASCandidates(t *testing.T) {
	now := time.Now()

	// tas-default and tas-overlap select the same nodes x1 and x2, while
	// tas-disjoint selects the node y1.
	topology := utiltestingapi.MakeDefaultOneLevelTopology("hostname")
	flavors := []*kueue.ResourceFlavor{
		utiltestingapi.MakeResourceFlavor("tas-default").TopologyName("hostname").NodeLabel("tas-node", "true").Obj(),
		utiltestingapi.MakeResourceFlavor("tas-overlap").TopologyName("hostname").NodeLabel("tas-node", "true").Obj(),
		utiltestingapi.MakeResourceFlavor("tas-disjoint").TopologyName("hostname").NodeLabel("tas-other", "true").Obj(),
	}
	makeNode := func(name, label string) *corev1.Node {
		return testingnode.MakeNode(name).
			Label(corev1.LabelHostname, name).
			Label(label, "true").
			StatusAllocatable(corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("4"),
				corev1.ResourceMemory: resource.MustParse("4Gi"),
				corev1.ResourcePods:   resource.MustParse("10"),
			}).
			Ready().
			Obj()
	}
	nodes := []*corev1.Node{
		makeNode("x1", "tas-node"),
		makeNode("x2", "tas-node"),
		makeNode("y1", "tas-other"),
	}
	makeClusterQueue := func(name, flavor string) *kueue.ClusterQueue {
		return utiltestingapi.MakeClusterQueue(name).
			Cohort("all").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas(flavor).
				Resource(corev1.ResourceCPU, "10").
				Resource(corev1.ResourceMemory, "10Gi").
				Obj()).
			Obj()
	}
	clusterQueues := []*kueue.ClusterQueue{
		makeClusterQueue("a", "tas-default"),
		makeClusterQueue("b", "tas-overlap"),
		makeClusterQueue("c", "tas-disjoint"),
	}
	admittedWl := func(name string, cq kueue.ClusterQueueReference, flavor kueue.ResourceFlavorReference, res corev1.ResourceName, qty, node string) *kueue.Workload {
		return utiltestingapi.MakeWorkload(name, "").
			UID(types.UID(name)).
			PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
				RequiredTopologyRequest(corev1.LabelHostname).
				Request(res, qty).
				Obj()).
			ReserveQuotaAt(utiltestingapi.MakeAdmission(cq).
				PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
					Assignment(res, flavor, qty).
					TopologyAssignment(utiltestingapi.MakeTopologyAssignment([]string{corev1.LabelHostname}).
						Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{node}, 1).Obj()).
						Obj()).
					Obj()).
				Obj(), now).
			AdmittedAt(true, now).
			Obj()
	}
	// The workload using memory only doesn't use the flavor resource needing
	// preemption, but still holds capacity on the nodes of its flavor.
	admitted := []*kueue.Workload{
		admittedWl("a1", "a", "tas-default", corev1.ResourceCPU, "1", "x2"),
		admittedWl("a-mem", "a", "tas-default", corev1.ResourceMemory, "1Gi", "x1"),
		admittedWl("b1", "b", "tas-overlap", corev1.ResourceCPU, "1", "x1"),
		admittedWl("c1", "c", "tas-disjoint", corev1.ResourceCPU, "1", "y1"),
	}

	tests := map[string]struct {
		wantCandidates []string
	}{
		"workloads on the nodes of the flavor are selected, whatever flavor and resource they use": {
			wantCandidates: []string{"a-mem", "a1", "b1"},
		},
	}

	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGatesDuringTest(t, map[featuregate.Feature]bool{
				features.ConfigurablePreemptions: true,
				features.TopologyAwareScheduling: true,
			})
			ctx, log := utiltesting.ContextWithLog(t)
			cqCache := schdcache.New(utiltesting.NewFakeClient())
			for _, cq := range clusterQueues {
				if err := cqCache.AddClusterQueue(ctx, cq.DeepCopy()); err != nil {
					t.Fatalf("Couldn't add ClusterQueue to cache: %v", err)
				}
			}
			for _, rf := range flavors {
				cqCache.AddOrUpdateResourceFlavor(log, rf.DeepCopy())
			}
			cqCache.AddOrUpdateTopology(log, topology.DeepCopy())
			for _, wl := range admitted {
				cqCache.AddOrUpdateWorkload(ctx, log, wl.DeepCopy())
			}
			for _, n := range nodes {
				cqCache.TASCache().SyncNode(n.DeepCopy())
			}
			snapshot, err := cqCache.Snapshot(ctx)
			if err != nil {
				t.Fatalf("unexpected error while building snapshot: %v", err)
			}

			preemptor := workload.NewInfo(log, utiltestingapi.MakeWorkload("a-incoming", "").
				PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
					RequiredTopologyRequest(corev1.LabelHostname).
					Request(corev1.ResourceCPU, "1").
					Obj()).
				Obj())
			preemptor.ClusterQueue = "a"

			frsNeedPreemption := sets.New(resources.FlavorResource{Flavor: "tas-default", Resource: corev1.ResourceCPU})

			// The trigger only defines when the rule applies, so any trigger
			// selects the same candidates.
			triggers := []kueuealpha.PreemptionConfigActivationTrigger{
				kueuealpha.Always,
				kueuealpha.InsufficientQuota,
				kueuealpha.QuotaFeasibleAndInsufficientTopology,
			}
			for _, trigger := range triggers {
				config := *utiltestingalpha.MakePreemptionConfig("test").
					Rule("test", trigger,
						utiltestingalpha.MakeCandidateSelector(kueuealpha.AnyClusterQueue).Obj(),
					).Obj()
				evaluator := NewPreemptionEvaluator(ctx, log, config, preemptor, candidatesByName)

				candidates, err := evaluator.candidatesFor(snapshot, frsNeedPreemption, trigger)
				if err != nil {
					t.Fatalf("candidatesFor() unexpected error: %v", err)
				}

				// Candidates are not ordered, so compare them as sorted lists.
				gotCandidates := slices.Sorted(slices.Values(utilslices.Map(candidates, func(candidate **configurableCandidate) string {
					return (*candidate).WlInfo.Obj.Name
				})))
				if diff := cmp.Diff(tc.wantCandidates, gotCandidates, cmpopts.EquateEmpty()); diff != "" {
					t.Errorf("Selected candidates (-want,+got) for trigger %s:\n%s", trigger, diff)
				}
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
	candidate := func(name string, indexes map[policy.PreemptionConfigRuleReference][]int) *configurableCandidate {
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
				candidate("a1", map[policy.PreemptionConfigRuleReference][]int{"test": {0}}),
				candidate("a2", map[policy.PreemptionConfigRuleReference][]int{"test": {0}}),
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
				candidate("a1", map[policy.PreemptionConfigRuleReference][]int{"test1": {0}, "test2": {0}}),
				candidate("a2", map[policy.PreemptionConfigRuleReference][]int{"test1": {0}, "test2": {0}}),
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
				candidate("a1", map[policy.PreemptionConfigRuleReference][]int{"test2": {0}, "test3": {0}}),
				candidate("a2", map[policy.PreemptionConfigRuleReference][]int{"test2": {0}, "test3": {0}}),
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
				candidate("a1", map[policy.PreemptionConfigRuleReference][]int{"test": {0, 1}}),
				candidate("a2", map[policy.PreemptionConfigRuleReference][]int{"test": {0, 1}}),
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
				candidate("a1", map[policy.PreemptionConfigRuleReference][]int{"test": {1}}),
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

			wlInfo := workload.NewInfo(log, tc.preemptorWl)
			wlInfo.ClusterQueue = tc.preemptorCq
			evaluator := NewPreemptionEvaluator(ctx, log, tc.config, wlInfo, candidatesByName)

			trigger := tc.trigger
			if trigger == "" {
				trigger = kueuealpha.Always
			}

			frsNeedPreemption := sets.New(resources.FlavorResource{Flavor: "default", Resource: corev1.ResourceCPU})
			candidates, err := evaluator.candidatesFor(snapshot, frsNeedPreemption, trigger)
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
	const fullCPUQuota = "3"

	now := time.Now()
	unitWl := *utiltestingapi.MakeWorkload("unit", "").Request(corev1.ResourceCPU, "1")
	clusterQueue := utiltestingapi.MakeClusterQueue("a").
		ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
			Resource(corev1.ResourceCPU, fullCPUQuota).Obj()).
		Obj()
	fr := resources.FlavorResource{Flavor: "default", Resource: corev1.ResourceCPU}

	lowerTierSelector := utiltestingalpha.MakeCandidateSelector(kueuealpha.WithinClusterQueue).
		NumericLabels(kueuealpha.PreemptionConfigNumericLabelConstraint{
			Key:        "preemption-tier",
			Comparison: new(kueuealpha.LessThan),
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
		"invalid Always selector does not fall back to InsufficientQuota": {
			config: utiltestingalpha.MakePreemptionConfig("test").
				Rule("invalid-always", kueuealpha.Always,
					utiltestingalpha.MakeCandidateSelector(kueuealpha.WithinClusterQueue).
						LabelSelector(&metav1.LabelSelector{
							MatchExpressions: []metav1.LabelSelectorRequirement{
								{Key: "preemptible", Operator: "invalid", Values: []string{"true"}},
							},
						}).Obj(),
				).
				Rule("fallback", kueuealpha.InsufficientQuota,
					utiltestingalpha.MakeCandidateSelector(kueuealpha.WithinClusterQueue).Obj(),
				).Obj(),
			admitted: []kueue.Workload{
				*unitWl.Clone().Name("a1").SimpleReserveQuota("a", "default", now).Obj(),
			},
			preemptor:   unitWl.Clone().Name("a_incoming").Request(corev1.ResourceCPU, fullCPUQuota).Obj(),
			wantTargets: []string{},
		},
		"stops as soon as the yield returns false": {
			config: &multiTriggerConfig,
			admitted: []kueue.Workload{
				*unitWl.Clone().Name("a1").SimpleReserveQuota("a", "default", now).Obj(),
				*unitWl.Clone().Name("a2").SimpleReserveQuota("a", "default", now).Obj(),
			},
			// Needs 2 CPUs out of the 1 available: a1 and a2 have no tier, so they are
			// only selected by the InsufficientQuota trigger. Preempting a1 frees enough
			// quota for the preemptor to fit, so the yield returns false and a2 is not
			// yielded even though it is also a candidate.
			preemptor:       unitWl.Clone().Name("a_incoming").Label("preemption-tier", "5").Request(corev1.ResourceCPU, "2").Obj(),
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

			preemptor := workload.NewInfo(log, tc.preemptor)
			preemptor.ClusterQueue = "a"
			var evaluator *PreemptionEvaluator
			if tc.config != nil {
				evaluator = NewPreemptionEvaluator(ctx, log, *tc.config, preemptor, candidatesByName)
			}
			requestedCPU := preemptor.TotalRequests[0].Requests.ResourceValue(corev1.ResourceCPU)
			quotaFits := func() bool {
				return snapshot.ClusterQueue("a").Available(fr).Cmp(requestedCPU) >= 0
			}

			// When
			// The yield interrupts FindCandidates once the preemptor fits.
			var gotTargets []string
			gotInterrupted := evaluator.FindCandidates(snapshot, sets.New(fr), quotaFits, func(target *policy.Target) bool {
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

// fakeTimeFilter blocks the listed workloads until the given time, or forever when nil.
type fakeTimeFilter struct {
	blockedUntil map[string]*time.Time
}

func (f fakeTimeFilter) Matches(wl *workload.Info) (*time.Time, bool) {
	retryAt, blocked := f.blockedUntil[wl.Obj.Name]
	return retryAt, !blocked
}

func TestPreemptionEvaluatorRetryAt(t *testing.T) {
	now := time.Date(2026, time.January, 1, 0, 0, 0, 0, time.UTC)
	tVeryEarly := now.Add(5 * time.Second)
	tEarly := now.Add(15 * time.Second)
	tLate := now.Add(40 * time.Second)

	frCPU := resources.FlavorResource{Flavor: "default", Resource: corev1.ResourceCPU}
	frTASCPU := resources.FlavorResource{Flavor: "tas-default", Resource: corev1.ResourceCPU}
	anyCQ := utiltestingalpha.MakeCandidateSelector(kueuealpha.AnyClusterQueue).Obj()
	preemptibleOnly := utiltestingalpha.MakeCandidateSelector(kueuealpha.AnyClusterQueue).
		LabelSelector(&metav1.LabelSelector{MatchLabels: map[string]string{"preemptible": "true"}}).
		Obj()
	unitWl := *utiltestingapi.MakeWorkload("unit", "").Request(corev1.ResourceCPU, "1")
	tasMemWl := utiltestingapi.MakeWorkload("tas-mem", "").
		PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
			RequiredTopologyRequest(corev1.LabelHostname).
			Request(corev1.ResourceMemory, "1Gi").
			Obj()).
		ReserveQuotaAt(utiltestingapi.MakeAdmission("a").
			PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
				Assignment(corev1.ResourceMemory, "tas-default", "1Gi").
				TopologyAssignment(utiltestingapi.MakeTopologyAssignment([]string{corev1.LabelHostname}).
					Domain(utiltestingapi.MakeTopologyDomainAssignment([]string{"x1"}, 1).Obj()).
					Obj()).
				Obj()).
			Obj(), now).
		AdmittedAt(true, now).
		Obj()
	blockedUntil := func(untilByName map[string]*time.Time) []filters.WorkloadTimeFilter {
		return []filters.WorkloadTimeFilter{fakeTimeFilter{blockedUntil: untilByName}}
	}

	cases := map[string]struct {
		config *kueuealpha.PreemptionConfig // Nil leaves the evaluator nil.
		// timeFilters are added to the selectors of the config, in rule and selector order.
		timeFilters           [][]filters.WorkloadTimeFilter
		admitted              []kueue.Workload
		flavorsNeedPreemption sets.Set[resources.FlavorResource] // Defaults to frCPU.
		wantCandidates        []string
		wantRetryAt           *time.Time
	}{
		"nil evaluator does not record retryAt": {},
		"evaluator without rules does not record retryAt": {
			config: utiltestingalpha.MakePreemptionConfig("test").Obj(),
			admitted: []kueue.Workload{
				*unitWl.Clone().Name("a1").SimpleReserveQuota("a", "default", now).Obj(),
			},
		},
		"workload in ClusterQueue out of scope does not record retryAt": {
			config: utiltestingalpha.MakePreemptionConfig("test").
				Rule("rule", kueuealpha.Always, utiltestingalpha.MakeCandidateSelector(kueuealpha.WithinClusterQueue).Obj()).
				Obj(),
			timeFilters: [][]filters.WorkloadTimeFilter{blockedUntil(map[string]*time.Time{"b1": &tEarly})},
			admitted: []kueue.Workload{
				*unitWl.Clone().Name("b1").SimpleReserveQuota("b", "default", now).Obj(),
			},
		},
		"workload not matching labels does not record retryAt": {
			config:      utiltestingalpha.MakePreemptionConfig("test").Rule("rule", kueuealpha.Always, preemptibleOnly).Obj(),
			timeFilters: [][]filters.WorkloadTimeFilter{blockedUntil(map[string]*time.Time{"a1": &tEarly})},
			admitted: []kueue.Workload{
				*unitWl.Clone().Name("a1").SimpleReserveQuota("a", "default", now).Obj(),
			},
		},
		"workload not using needed flavor quota or TAS nodes does not record retryAt": {
			config:      utiltestingalpha.MakePreemptionConfig("test").Rule("rule", kueuealpha.Always, anyCQ).Obj(),
			timeFilters: [][]filters.WorkloadTimeFilter{blockedUntil(map[string]*time.Time{"other-flavor": &tEarly, "mem-only": &tEarly})},
			admitted: []kueue.Workload{
				*unitWl.Clone().Name("other-flavor").SimpleReserveQuota("a", "other", now).Obj(),
				*utiltestingapi.MakeWorkload("mem-only", "").Request(corev1.ResourceMemory, "1Gi").SimpleReserveQuota("a", "default", now).Obj(),
			},
		},
		"workload holding TAS node of needed flavor records retryAt without using its quota": {
			config:                utiltestingalpha.MakePreemptionConfig("test").Rule("rule", kueuealpha.Always, anyCQ).Obj(),
			timeFilters:           [][]filters.WorkloadTimeFilter{blockedUntil(map[string]*time.Time{"tas-mem": &tEarly})},
			admitted:              []kueue.Workload{*tasMemWl},
			flavorsNeedPreemption: sets.New(frTASCPU),
			wantRetryAt:           &tEarly,
		},
		"workload never matching one time filter does not record retryAt": {
			config: utiltestingalpha.MakePreemptionConfig("test").Rule("rule", kueuealpha.Always, anyCQ).Obj(),
			timeFilters: [][]filters.WorkloadTimeFilter{{
				fakeTimeFilter{blockedUntil: map[string]*time.Time{"a1": &tEarly}},
				fakeTimeFilter{blockedUntil: map[string]*time.Time{"a1": nil}},
			}},
			admitted: []kueue.Workload{
				*unitWl.Clone().Name("a1").SimpleReserveQuota("a", "default", now).Obj(),
			},
		},
		"workload failing several time filters records the latest retryAt": {
			config: utiltestingalpha.MakePreemptionConfig("test").Rule("rule", kueuealpha.Always, anyCQ).Obj(),
			timeFilters: [][]filters.WorkloadTimeFilter{{
				fakeTimeFilter{},
				fakeTimeFilter{blockedUntil: map[string]*time.Time{"a1": &tEarly}},
				fakeTimeFilter{blockedUntil: map[string]*time.Time{"a1": &tLate}},
				fakeTimeFilter{blockedUntil: map[string]*time.Time{"a1": &tVeryEarly}},
			}},
			admitted: []kueue.Workload{
				*unitWl.Clone().Name("a1").SimpleReserveQuota("a", "default", now).Obj(),
			},
			wantRetryAt: &tLate,
		},
		"workloads not selected by any selector record the earliest retryAt": {
			config: utiltestingalpha.MakePreemptionConfig("test").Rule("rule", kueuealpha.Always, anyCQ, anyCQ, anyCQ).Obj(),
			timeFilters: [][]filters.WorkloadTimeFilter{
				blockedUntil(map[string]*time.Time{"a1": &tLate, "a2": nil}),
				blockedUntil(map[string]*time.Time{"a1": &tEarly, "a2": &tLate}),
				blockedUntil(map[string]*time.Time{"a1": nil, "a2": nil}),
			},
			admitted: []kueue.Workload{
				*unitWl.Clone().Name("a1").SimpleReserveQuota("a", "default", now).Obj(),
				*unitWl.Clone().Name("a2").SimpleReserveQuota("a", "default", now).Obj(),
			},
			wantRetryAt: &tEarly,
		},
		"workload selected by one selector of a rule does not record retryAt of another selector of the rule, even when a later rule skips it": {
			config: utiltestingalpha.MakePreemptionConfig("test").
				Rule("rule", kueuealpha.Always,
					anyCQ,
					utiltestingalpha.MakeCandidateSelector(kueuealpha.WithinLocalQueue).Obj(),
				).
				Rule("skipping-rule", kueuealpha.Always, preemptibleOnly).
				Obj(),
			timeFilters: [][]filters.WorkloadTimeFilter{
				blockedUntil(map[string]*time.Time{"a1": &tEarly, "a2": &tLate}),
			},
			admitted: []kueue.Workload{
				*unitWl.Clone().Name("a1").Queue("lq").SimpleReserveQuota("a", "default", now).Obj(),
				*unitWl.Clone().Name("a2").Queue("other-lq").SimpleReserveQuota("a", "default", now).Obj(),
			},
			wantCandidates: []string{"a1"},
			wantRetryAt:    &tLate,
		},
		"workload selected by one rule records retryAt of another rule": {
			config: utiltestingalpha.MakePreemptionConfig("test").
				Rule("selecting-rule", kueuealpha.Always, anyCQ).
				Rule("blocking-rule", kueuealpha.Always, anyCQ).
				Obj(),
			timeFilters: [][]filters.WorkloadTimeFilter{
				nil,
				blockedUntil(map[string]*time.Time{"a1": &tEarly}),
			},
			admitted: []kueue.Workload{
				*unitWl.Clone().Name("a1").SimpleReserveQuota("a", "default", now).Obj(),
			},
			wantCandidates: []string{"a1"},
			wantRetryAt:    &tEarly,
		},
		"blocked workloads do not prevent selecting the other workloads of their ClusterQueue": {
			config: utiltestingalpha.MakePreemptionConfig("test").Rule("rule", kueuealpha.Always, anyCQ).Obj(),
			timeFilters: [][]filters.WorkloadTimeFilter{blockedUntil(map[string]*time.Time{
				"a-blocked-late":  &tLate,
				"b-blocked-early": &tEarly,
			})},
			admitted: []kueue.Workload{
				*unitWl.Clone().Name("a-blocked-late").SimpleReserveQuota("a", "default", now).Obj(),
				*unitWl.Clone().Name("a-ready").SimpleReserveQuota("a", "default", now).Obj(),
				*unitWl.Clone().Name("b-blocked-early").SimpleReserveQuota("b", "default", now).Obj(),
				*unitWl.Clone().Name("b-ready").SimpleReserveQuota("b", "default", now).Obj(),
			},
			wantCandidates: []string{"a-ready", "b-ready"},
			wantRetryAt:    &tEarly,
		},
		"workload blocked by rules of several triggers records the earliest retryAt": {
			config: utiltestingalpha.MakePreemptionConfig("test").
				Rule("always", kueuealpha.Always, anyCQ).
				Rule("insufficient-quota", kueuealpha.InsufficientQuota, anyCQ).
				Obj(),
			timeFilters: [][]filters.WorkloadTimeFilter{
				blockedUntil(map[string]*time.Time{"a1": &tEarly}),
				blockedUntil(map[string]*time.Time{"a1": &tLate}),
			},
			admitted: []kueue.Workload{
				*unitWl.Clone().Name("a1").SimpleReserveQuota("a", "default", now).Obj(),
			},
			wantRetryAt: &tEarly,
		},
		"invalid rule discards retryAt of the other rules of its trigger": {
			config: utiltestingalpha.MakePreemptionConfig("test").
				Rule("rule", kueuealpha.Always, anyCQ).
				Rule("invalid", kueuealpha.Always, utiltestingalpha.MakeCandidateSelector(kueuealpha.AnyClusterQueue).
					LabelSelector(&metav1.LabelSelector{MatchExpressions: []metav1.LabelSelectorRequirement{{Key: "test", Operator: "invalid"}}}).
					Obj()).
				Obj(),
			timeFilters: [][]filters.WorkloadTimeFilter{blockedUntil(map[string]*time.Time{"a1": &tEarly})},
			admitted: []kueue.Workload{
				*unitWl.Clone().Name("a1").SimpleReserveQuota("a", "default", now).Obj(),
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGatesDuringTest(t, map[featuregate.Feature]bool{
				features.ConfigurablePreemptions:     true,
				features.TopologyAwareScheduling:     true,
				features.TASHandleOverlappingFlavors: true,
			})
			ctx, log := utiltesting.ContextWithLog(t)

			cqCache := schdcache.New(utiltesting.NewFakeClient())
			for _, cqName := range []string{"a", "b"} {
				cq := utiltestingapi.MakeClusterQueue(cqName).
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("default").
							Resource(corev1.ResourceCPU, "3").
							Resource(corev1.ResourceMemory, "3Gi").Obj(),
						*utiltestingapi.MakeFlavorQuotas("other").
							Resource(corev1.ResourceCPU, "3").
							Resource(corev1.ResourceMemory, "3Gi").Obj(),
						*utiltestingapi.MakeFlavorQuotas("tas-default").
							Resource(corev1.ResourceCPU, "3").
							Resource(corev1.ResourceMemory, "3Gi").Obj(),
					).Obj()
				if err := cqCache.AddClusterQueue(ctx, cq); err != nil {
					t.Fatalf("Couldn't add ClusterQueue %s to cache: %v", cqName, err)
				}
			}
			cqCache.AddOrUpdateResourceFlavor(log, utiltestingapi.MakeResourceFlavor("default").Obj())
			cqCache.AddOrUpdateResourceFlavor(log, utiltestingapi.MakeResourceFlavor("other").Obj())
			cqCache.AddOrUpdateResourceFlavor(log, utiltestingapi.MakeResourceFlavor("tas-default").TopologyName("hostname").NodeLabel("tas-node", "true").Obj())
			cqCache.AddOrUpdateTopology(log, utiltestingapi.MakeDefaultOneLevelTopology("hostname"))
			cqCache.TASCache().SyncNode(testingnode.MakeNode("x1").
				Label(corev1.LabelHostname, "x1").
				Label("tas-node", "true").
				StatusAllocatable(corev1.ResourceList{
					corev1.ResourceCPU:    resource.MustParse("4"),
					corev1.ResourceMemory: resource.MustParse("4Gi"),
					corev1.ResourcePods:   resource.MustParse("10"),
				}).
				Ready().
				Obj())
			for i := range tc.admitted {
				tc.admitted[i].UID = types.UID(tc.admitted[i].Name)
				cqCache.AddOrUpdateWorkload(ctx, log, &tc.admitted[i])
			}

			snapshot, err := cqCache.Snapshot(ctx)
			if err != nil {
				t.Fatalf("unexpected error while building snapshot: %v", err)
			}

			var evaluator *PreemptionEvaluator
			if tc.config != nil {
				preemptor := workload.NewInfo(log, unitWl.Clone().Name("a-incoming").Queue("lq").Obj())
				preemptor.ClusterQueue = "a"
				evaluator = NewPreemptionEvaluator(ctx, log, *tc.config, preemptor, candidatesByName)
				// TODO(#17600): Set the time filters through the config once NewCandidateFilters builds them.
				built := 0
				evaluator.newCandidateFilters = func(log logr.Logger, selector *kueuealpha.PreemptionConfigPreemptionCandidateSelector, preemptor *workload.Info, snapshot *schdcache.Snapshot) (filters.CandidateFilters, []*filters.FilterBuildError) {
					filter, errs := filters.NewCandidateFilters(log, selector, preemptor, snapshot)
					if built < len(tc.timeFilters) {
						filter.WLTimeFilters = tc.timeFilters[built]
					}
					built++
					return filter, errs
				}
			}

			flavorsNeedPreemption := tc.flavorsNeedPreemption
			if flavorsNeedPreemption == nil {
				flavorsNeedPreemption = sets.New(frCPU)
			}
			var gotCandidates []string
			evaluator.FindCandidates(snapshot, flavorsNeedPreemption, func() bool { return false }, func(target *policy.Target) bool {
				gotCandidates = append(gotCandidates, target.WorkloadInfo.Obj.Name)
				return true
			})
			if diff := cmp.Diff(tc.wantCandidates, gotCandidates, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("FindCandidates() candidates (-want,+got):\n%s", diff)
			}
			if diff := cmp.Diff(tc.wantRetryAt, evaluator.RetryAt()); diff != "" {
				t.Errorf("RetryAt() (-want,+got):\n%s", diff)
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
