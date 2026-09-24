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

	"github.com/go-logr/logr"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/utils/ptr"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	configtesting "sigs.k8s.io/kueue/pkg/scheduler/preemption/config/testing"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/pkg/workload"
)

func makeWorkloadInfo(log logr.Logger, w *kueue.Workload, cq kueue.ClusterQueueReference) *workload.Info {
	info := workload.NewInfo(log, w)
	info.ClusterQueue = cq
	return info
}

func TestNewCandidateFilters(t *testing.T) {
	_, log := utiltesting.ContextWithLog(t)

	// Minimal snapshot required by constructor for resolving preemptor's cohort ancestors:
	// rootA -> subA1 -> cq1
	snapshot := configtesting.NewSnapshotBuilder().
		Cohort("rootA", "").
		Cohort("subA1", "rootA").
		ClusterQueue("cq1", "subA1").
		Build()

	preemptor := makeWorkloadInfo(log, utiltestingapi.MakeWorkload("preemptor", "ns1").
		Queue("lq1").
		Label("tpu-size", "8").
		Priority(100).
		Obj(), "cq1")

	candSelectorProd, err := metav1.LabelSelectorAsSelector(&metav1.LabelSelector{
		MatchLabels: map[string]string{"env": "prod"},
	})
	if err != nil {
		t.Fatalf("Failed to parse label selector: %v", err)
	}

	cases := map[string]struct {
		selector      *kueuealpha.PreemptionConfigPreemptionCandidateSelector
		preemptor     *workload.Info
		wantFilters   CandidateFilters
		wantRejectAll bool
	}{
		"nil selector returns empty CandidateFilters": {
			selector:    nil,
			preemptor:   preemptor,
			wantFilters: CandidateFilters{},
		},
		"WithinLocalQueue instantiates withinClusterQueueFilter and withinLocalQueueFilter": {
			selector: &kueuealpha.PreemptionConfigPreemptionCandidateSelector{
				Scope: kueuealpha.WithinLocalQueue,
			},
			preemptor: preemptor,
			wantFilters: CandidateFilters{
				CQFilters: []ClusterQueueFilter{
					&withinClusterQueueFilter{preemptorCQ: "cq1"},
				},
				WLFilters: []WorkloadFilter{
					&withinLocalQueueFilter{namespace: "ns1", queueName: "lq1"},
				},
			},
		},
		"WithinClusterQueue instantiates withinClusterQueueFilter": {
			selector: &kueuealpha.PreemptionConfigPreemptionCandidateSelector{
				Scope: kueuealpha.WithinClusterQueue,
			},
			preemptor: preemptor,
			wantFilters: CandidateFilters{
				CQFilters: []ClusterQueueFilter{
					&withinClusterQueueFilter{preemptorCQ: "cq1"},
				},
			},
		},
		"WithinParentCohort resolves immediate parent cohort from snapshot": {
			selector: &kueuealpha.PreemptionConfigPreemptionCandidateSelector{
				Scope: kueuealpha.WithinParentCohort,
			},
			preemptor: preemptor,
			wantFilters: CandidateFilters{
				CQFilters: []ClusterQueueFilter{
					&withinParentCohortFilter{
						preemptorCQ:     "cq1",
						preemptorCohort: "subA1",
						hasCohort:       true,
					},
				},
			},
		},
		"WithinCohortTree resolves root ancestor cohort from snapshot": {
			selector: &kueuealpha.PreemptionConfigPreemptionCandidateSelector{
				Scope: kueuealpha.WithinCohortTree,
			},
			preemptor: preemptor,
			wantFilters: CandidateFilters{
				CQFilters: []ClusterQueueFilter{
					&withinCohortTreeFilter{
						preemptorCQ:         "cq1",
						preemptorRootCohort: "rootA",
						hasCohort:           true,
					},
				},
			},
		},
		"AnyClusterQueue results in empty filters": {
			selector: &kueuealpha.PreemptionConfigPreemptionCandidateSelector{
				Scope: kueuealpha.AnyClusterQueue,
			},
			preemptor:   preemptor,
			wantFilters: CandidateFilters{},
		},
		"unrecognized candidate scope returns rejectAll true": {
			selector: &kueuealpha.PreemptionConfigPreemptionCandidateSelector{
				Scope: kueuealpha.PreemptionConfigPreemptionQueueScope("UnknownScope"),
			},
			preemptor:     preemptor,
			wantFilters:   CandidateFilters{},
			wantRejectAll: true,
		},
		"WithinClusterQueue with empty NumericLabels produces no WorkloadFilters": {
			selector: &kueuealpha.PreemptionConfigPreemptionCandidateSelector{
				Scope:         kueuealpha.WithinClusterQueue,
				NumericLabels: []kueuealpha.PreemptionConfigNumericLabelConstraint{},
			},
			preemptor: preemptor,
			wantFilters: CandidateFilters{
				CQFilters: []ClusterQueueFilter{
					&withinClusterQueueFilter{preemptorCQ: "cq1"},
				},
				WLFilters: nil,
			},
		},
		"Combined WithinLocalQueue and NumericLabelConstraints appends both scope and numeric WorkloadFilters": {
			selector: &kueuealpha.PreemptionConfigPreemptionCandidateSelector{
				Scope: kueuealpha.WithinLocalQueue,
				NumericLabels: []kueuealpha.PreemptionConfigNumericLabelConstraint{
					{
						Key:           "tpu-size",
						FallbackValue: ptr.To[int32](1),
						Comparison:    ptr.To(kueuealpha.LessThanOrEqual),
					},
					{
						Key:      "priority-boost",
						MinValue: ptr.To[int32](10),
					},
				},
			},
			preemptor: preemptor,
			wantFilters: CandidateFilters{
				CQFilters: []ClusterQueueFilter{
					&withinClusterQueueFilter{preemptorCQ: "cq1"},
				},
				WLFilters: []WorkloadFilter{
					&withinLocalQueueFilter{
						namespace: "ns1",
						queueName: "lq1",
					},
					&numericLabelFilter{
						constraint: kueuealpha.PreemptionConfigNumericLabelConstraint{
							Key:           "tpu-size",
							FallbackValue: ptr.To[int32](1),
							Comparison:    ptr.To(kueuealpha.LessThanOrEqual),
						},
						preemptorVal: ptr.To[int32](8),
					},
					&numericLabelFilter{
						constraint: kueuealpha.PreemptionConfigNumericLabelConstraint{
							Key:      "priority-boost",
							MinValue: ptr.To[int32](10),
						},
						preemptorVal: nil,
					},
				},
			},
		},
		"Full combination of all selector criteria compiles into complete CandidateFilters": {
			selector: &kueuealpha.PreemptionConfigPreemptionCandidateSelector{
				Scope: kueuealpha.WithinParentCohort,
				ClusterQueueSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{"env": "prod"},
				},
				LabelSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{"env": "prod"},
				},
				Priority: &kueuealpha.PreemptionConfigPriorityConstraint{
					Mode:       kueuealpha.Base,
					Comparison: kueuealpha.LessThan,
				},
				NumericLabels: []kueuealpha.PreemptionConfigNumericLabelConstraint{
					{
						Key:        "tpu-size",
						Comparison: ptr.To(kueuealpha.LessThan),
					},
				},
			},
			preemptor: preemptor,
			wantFilters: CandidateFilters{
				CQFilters: []ClusterQueueFilter{
					&withinParentCohortFilter{
						preemptorCQ:     "cq1",
						preemptorCohort: "subA1",
						hasCohort:       true,
					},
					&clusterQueueLabelFilter{
						selector: candSelectorProd,
					},
				},
				WLFilters: []WorkloadFilter{
					&workloadLabelFilter{
						selector: candSelectorProd,
					},
					&numericLabelFilter{
						constraint: kueuealpha.PreemptionConfigNumericLabelConstraint{
							Key:        "tpu-size",
							Comparison: ptr.To(kueuealpha.LessThan),
						},
						preemptorVal: ptr.To[int32](8),
					},
					&priorityFilter{
						mode:              kueuealpha.Base,
						comparison:        kueuealpha.LessThan,
						preemptorPriority: 100,
					},
				},
			},
		},
		"WithinClusterQueue with Priority compiles both CQ and WL priority filters": {
			selector: &kueuealpha.PreemptionConfigPreemptionCandidateSelector{
				Scope: kueuealpha.WithinClusterQueue,
				Priority: &kueuealpha.PreemptionConfigPriorityConstraint{
					Mode:       kueuealpha.Base,
					Comparison: kueuealpha.LessThan,
				},
			},
			preemptor: preemptor,
			wantFilters: CandidateFilters{
				CQFilters: []ClusterQueueFilter{
					&withinClusterQueueFilter{preemptorCQ: "cq1"},
				},
				WLFilters: []WorkloadFilter{
					&priorityFilter{
						mode:              kueuealpha.Base,
						comparison:        kueuealpha.LessThan,
						preemptorPriority: 100,
					},
				},
			},
		},
		"Combined WithinLocalQueue, NumericLabels, and Priority compiles all filters": {
			selector: &kueuealpha.PreemptionConfigPreemptionCandidateSelector{
				Scope: kueuealpha.WithinLocalQueue,
				NumericLabels: []kueuealpha.PreemptionConfigNumericLabelConstraint{
					{
						Key:           "tpu-size",
						FallbackValue: ptr.To[int32](1),
						Comparison:    ptr.To(kueuealpha.LessThanOrEqual),
					},
				},
				Priority: &kueuealpha.PreemptionConfigPriorityConstraint{
					Mode:       kueuealpha.Boosted,
					Comparison: kueuealpha.LessThanOrEqual,
				},
			},
			preemptor: preemptor,
			wantFilters: CandidateFilters{
				CQFilters: []ClusterQueueFilter{
					&withinClusterQueueFilter{preemptorCQ: "cq1"},
				},
				WLFilters: []WorkloadFilter{
					&withinLocalQueueFilter{
						namespace: "ns1",
						queueName: "lq1",
					},
					&numericLabelFilter{
						constraint: kueuealpha.PreemptionConfigNumericLabelConstraint{
							Key:           "tpu-size",
							FallbackValue: ptr.To[int32](1),
							Comparison:    ptr.To(kueuealpha.LessThanOrEqual),
						},
						preemptorVal: ptr.To[int32](8),
					},
					&priorityFilter{
						mode:              kueuealpha.Boosted,
						comparison:        kueuealpha.LessThanOrEqual,
						preemptorPriority: 100,
					},
				},
			},
		},
		"SameClusterQueue with empty LabelSelector produces no extra WorkloadFilters": {
			selector: &kueuealpha.PreemptionConfigPreemptionCandidateSelector{
				Scope:         kueuealpha.WithinClusterQueue,
				LabelSelector: &metav1.LabelSelector{},
			},
			preemptor: preemptor,
			wantFilters: CandidateFilters{
				CQFilters: []ClusterQueueFilter{
					&withinClusterQueueFilter{preemptorCQ: "cq1"},
				},
			},
		},
		"SameClusterQueue with valid LabelSelector compiles into WLFilters": {
			selector: &kueuealpha.PreemptionConfigPreemptionCandidateSelector{
				Scope: kueuealpha.WithinClusterQueue,
				LabelSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{"env": "prod"},
				},
			},
			preemptor: preemptor,
			wantFilters: CandidateFilters{
				CQFilters: []ClusterQueueFilter{
					&withinClusterQueueFilter{preemptorCQ: "cq1"},
				},
				WLFilters: []WorkloadFilter{
					&workloadLabelFilter{
						selector: candSelectorProd,
					},
				},
			},
		},
		"LabelSelector with invalid selector returns rejectAll true": {
			selector: &kueuealpha.PreemptionConfigPreemptionCandidateSelector{
				Scope: kueuealpha.WithinClusterQueue,
				LabelSelector: &metav1.LabelSelector{
					MatchExpressions: []metav1.LabelSelectorRequirement{
						{Key: "env", Operator: metav1.LabelSelectorOperator("InvalidOp")},
					},
				},
			},
			preemptor:     preemptor,
			wantFilters:   CandidateFilters{},
			wantRejectAll: true,
		},
		"ClusterQueueSelector instantiates clusterQueueLabelFilter": {
			selector: &kueuealpha.PreemptionConfigPreemptionCandidateSelector{
				Scope: kueuealpha.WithinClusterQueue,
				ClusterQueueSelector: &metav1.LabelSelector{
					MatchLabels: map[string]string{"env": "prod"},
				},
			},
			preemptor: preemptor,
			wantFilters: CandidateFilters{
				CQFilters: []ClusterQueueFilter{
					&withinClusterQueueFilter{preemptorCQ: "cq1"},
					&clusterQueueLabelFilter{
						selector: candSelectorProd,
					},
				},
			},
		},
		"ClusterQueueSelector with empty selector adds no CQ filter": {
			selector: &kueuealpha.PreemptionConfigPreemptionCandidateSelector{
				Scope:                kueuealpha.WithinClusterQueue,
				ClusterQueueSelector: &metav1.LabelSelector{},
			},
			preemptor: preemptor,
			wantFilters: CandidateFilters{
				CQFilters: []ClusterQueueFilter{
					&withinClusterQueueFilter{preemptorCQ: "cq1"},
				},
			},
		},
		"ClusterQueueSelector with invalid selector returns rejectAll true": {
			selector: &kueuealpha.PreemptionConfigPreemptionCandidateSelector{
				Scope: kueuealpha.WithinClusterQueue,
				ClusterQueueSelector: &metav1.LabelSelector{
					MatchExpressions: []metav1.LabelSelectorRequirement{
						{Key: "env", Operator: metav1.LabelSelectorOperator("InvalidOp")},
					},
				},
			},
			preemptor:     preemptor,
			wantFilters:   CandidateFilters{},
			wantRejectAll: true,
		},
		"Priority with invalid mode returns rejectAll true": {
			selector: &kueuealpha.PreemptionConfigPreemptionCandidateSelector{
				Scope: kueuealpha.WithinClusterQueue,
				Priority: &kueuealpha.PreemptionConfigPriorityConstraint{
					Mode:       kueuealpha.PreemptionConfigPriorityMode("InvalidMode"),
					Comparison: kueuealpha.LessThan,
				},
			},
			preemptor:     preemptor,
			wantFilters:   CandidateFilters{},
			wantRejectAll: true,
		},
	}

	cmpOptions := []cmp.Option{
		cmp.AllowUnexported(
			withinClusterQueueFilter{},
			withinParentCohortFilter{},
			withinCohortTreeFilter{},
			withinLocalQueueFilter{},
			workloadLabelFilter{},
			clusterQueueLabelFilter{},
			numericLabelFilter{},
			priorityFilter{},
		),
		cmpopts.IgnoreFields(numericLabelFilter{}, "log"),
		cmpopts.IgnoreFields(priorityFilter{}, "log", "priorityFn"),
		cmp.Comparer(func(a, b labels.Selector) bool {
			if a == nil && b == nil {
				return true
			}
			if a == nil || b == nil {
				return false
			}
			return a.String() == b.String()
		}),
		cmpopts.EquateEmpty(),
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			gotFilters, gotRejectAll := NewCandidateFilters(log, tc.selector, tc.preemptor, snapshot)
			if gotRejectAll != tc.wantRejectAll {
				t.Errorf("NewCandidateFilters() rejectAll = %v, want %v", gotRejectAll, tc.wantRejectAll)
			}
			if diff := cmp.Diff(tc.wantFilters, gotFilters, cmpOptions...); diff != "" {
				t.Errorf("NewCandidateFilters() mismatch (-want +got):\n%s", diff)
			}
		})
	}
}
