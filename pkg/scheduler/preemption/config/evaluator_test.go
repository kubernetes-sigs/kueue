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
	"slices"
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
		wantError      string
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
			wantError:   "\"invalid\" is not a valid label selector operator",
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

			evaluator := NewPreemptionEvaluator(ctx, log, clock.RealClock{}, tc.config)

			wlInfo := workload.NewInfo(log, tc.preemptorWl)
			wlInfo.ClusterQueue = tc.preemptorCq

			trigger := tc.trigger
			if trigger == "" {
				trigger = kueuealpha.Always
			}
			frsNeedPreemption := sets.New(resources.FlavorResource{Flavor: "default", Resource: corev1.ResourceCPU})
			candidates, err := evaluator.Candidates(snapshot, wlInfo, frsNeedPreemption, trigger)
			if err != nil || tc.wantError != "" {
				gotError := ""
				if err != nil {
					gotError = err.Error()
				}
				if diff := cmp.Diff(tc.wantError, gotError, cmpopts.EquateEmpty()); diff != "" {
					t.Errorf("Candidates() error (-want +got):\n%s", diff)
				}
				return
			}

			// Candidates are not ordered, so compare them as sorted lists.
			gotCandidates := slices.Sorted(slices.Values(utilslices.Map(candidates, func(wlInfo **workload.Info) string {
				return (*wlInfo).Obj.Name
			})))
			wantCandidates := slices.Sorted(slices.Values(tc.wantCandidates))
			if diff := cmp.Diff(wantCandidates, gotCandidates, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("Selected candidates (-want,+got):\n%s", diff)
			}
		})
	}
}
