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

	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	configtesting "sigs.k8s.io/kueue/pkg/scheduler/preemption/config/testing"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/pkg/workload"
)

func TestClusterQueueScopeFilters(t *testing.T) {
	// Hierarchy topology:
	//                 rootA (Root Cohort)                     rootB (Root Cohort)
	//              /           |             \                          |
	// cqDirectRootA          subA1          subA2                     subB
	//                      /   |     \        |                         |
	//              cq1SubA1 cq2SubA1 subSubA cq3SubA2                cq4SubB
	//                                  |
	//                            cqDeepSubSubA
	//
	// Standalone CQs (no cohort):
	// - cqStandalone1
	// - cqStandalone2
	snapshot := configtesting.NewSnapshotBuilder().
		Cohort("rootA", "").
		Cohort("subA1", "rootA").
		Cohort("subSubA", "subA1").
		Cohort("subA2", "rootA").
		Cohort("rootB", "").
		Cohort("subB", "rootB").
		ClusterQueue("cqDirectRootA", "rootA").
		ClusterQueue("cq1SubA1", "subA1").
		ClusterQueue("cq2SubA1", "subA1").
		ClusterQueue("cqDeepSubSubA", "subSubA").
		ClusterQueue("cq3SubA2", "subA2").
		ClusterQueue("cq4SubB", "subB").
		ClusterQueue("cqStandalone1", "").
		ClusterQueue("cqStandalone2", "").
		Build()

	cq1SubA1 := snapshot.ClusterQueue("cq1SubA1")
	cq2SubA1 := snapshot.ClusterQueue("cq2SubA1")
	cqDeepSubSubA := snapshot.ClusterQueue("cqDeepSubSubA")
	cqDirectRootA := snapshot.ClusterQueue("cqDirectRootA")
	cq3SubA2 := snapshot.ClusterQueue("cq3SubA2")
	cq4SubB := snapshot.ClusterQueue("cq4SubB")
	cqStandalone1 := snapshot.ClusterQueue("cqStandalone1")
	cqStandalone2 := snapshot.ClusterQueue("cqStandalone2")

	cases := map[string]struct {
		filter      ClusterQueueFilter
		candidateCQ *schdcache.ClusterQueueSnapshot
		wantMatch   bool
	}{
		// 1. WithinClusterQueue Filter Tests
		"WithinClusterQueue: matching target CQ": {
			filter:      NewWithinClusterQueueFilter("cq1SubA1"),
			candidateCQ: cq1SubA1,
			wantMatch:   true,
		},
		"WithinClusterQueue: different sibling CQ in same cohort rejected": {
			filter:      NewWithinClusterQueueFilter("cq1SubA1"),
			candidateCQ: cq2SubA1,
			wantMatch:   false,
		},
		"WithinClusterQueue: different CQ in disjoint tree rejected": {
			filter:      NewWithinClusterQueueFilter("cq1SubA1"),
			candidateCQ: cq4SubB,
			wantMatch:   false,
		},
		"WithinClusterQueue: standalone CQ matches itself": {
			filter:      NewWithinClusterQueueFilter("cqStandalone1"),
			candidateCQ: cqStandalone1,
			wantMatch:   true,
		},
		"WithinClusterQueue: standalone CQ rejects another standalone CQ": {
			filter:      NewWithinClusterQueueFilter("cqStandalone1"),
			candidateCQ: cqStandalone2,
			wantMatch:   false,
		},

		// 2. WithinParentCohort Filter Tests
		"WithinParentCohort: sibling CQ in same immediate parent cohort matches": {
			filter:      NewWithinParentCohortFilter("cq1SubA1", snapshot),
			candidateCQ: cq2SubA1,
			wantMatch:   true,
		},
		"WithinParentCohort: candidate in exact same CQ as preemptor matches": {
			filter:      NewWithinParentCohortFilter("cq1SubA1", snapshot),
			candidateCQ: cq1SubA1,
			wantMatch:   true,
		},
		"WithinParentCohort: sub-cohort child under same immediate parent rejected": {
			filter:      NewWithinParentCohortFilter("cq1SubA1", snapshot),
			candidateCQ: cqDeepSubSubA,
			wantMatch:   false,
		},
		"WithinParentCohort: sibling sub-cohort under same root rejected": {
			filter:      NewWithinParentCohortFilter("cq1SubA1", snapshot),
			candidateCQ: cq3SubA2,
			wantMatch:   false,
		},
		"WithinParentCohort: direct child of root cohort rejected when preemptor is in sub-cohort": {
			filter:      NewWithinParentCohortFilter("cq1SubA1", snapshot),
			candidateCQ: cqDirectRootA,
			wantMatch:   false,
		},
		"WithinParentCohort: candidate in disjoint cohort tree rejected": {
			filter:      NewWithinParentCohortFilter("cq1SubA1", snapshot),
			candidateCQ: cq4SubB,
			wantMatch:   false,
		},
		"WithinParentCohort: standalone candidate rejected for preemptor with cohort": {
			filter:      NewWithinParentCohortFilter("cq1SubA1", snapshot),
			candidateCQ: cqStandalone1,
			wantMatch:   false,
		},
		"WithinParentCohort: preemptor directly under root matches itself": {
			filter:      NewWithinParentCohortFilter("cqDirectRootA", snapshot),
			candidateCQ: cqDirectRootA,
			wantMatch:   true,
		},
		"WithinParentCohort: preemptor directly under root rejects sub-cohort child": {
			filter:      NewWithinParentCohortFilter("cqDirectRootA", snapshot),
			candidateCQ: cq1SubA1,
			wantMatch:   false,
		},
		"WithinParentCohort: standalone preemptor matches candidate in its own CQ": {
			filter:      NewWithinParentCohortFilter("cqStandalone1", snapshot),
			candidateCQ: cqStandalone1,
			wantMatch:   true,
		},
		"WithinParentCohort: standalone preemptor rejects candidate in another standalone CQ": {
			filter:      NewWithinParentCohortFilter("cqStandalone1", snapshot),
			candidateCQ: cqStandalone2,
			wantMatch:   false,
		},
		"WithinParentCohort: standalone preemptor rejects candidate in a cohort CQ": {
			filter:      NewWithinParentCohortFilter("cqStandalone1", snapshot),
			candidateCQ: cq1SubA1,
			wantMatch:   false,
		},

		// 3. WithinCohortTree Filter Tests
		"WithinCohortTree: sibling CQ in same immediate cohort matches": {
			filter:      NewWithinCohortTreeFilter("cq1SubA1", snapshot),
			candidateCQ: cq2SubA1,
			wantMatch:   true,
		},
		"WithinCohortTree: candidate in sibling sub-cohort sharing root matches": {
			filter:      NewWithinCohortTreeFilter("cq1SubA1", snapshot),
			candidateCQ: cq3SubA2,
			wantMatch:   true,
		},
		"WithinCohortTree: candidate in 3-level deep sub-cohort sharing root matches": {
			filter:      NewWithinCohortTreeFilter("cq1SubA1", snapshot),
			candidateCQ: cqDeepSubSubA,
			wantMatch:   true,
		},
		"WithinCohortTree: candidate directly under root cohort matches": {
			filter:      NewWithinCohortTreeFilter("cq1SubA1", snapshot),
			candidateCQ: cqDirectRootA,
			wantMatch:   true,
		},
		"WithinCohortTree: candidate in exact same CQ as preemptor matches": {
			filter:      NewWithinCohortTreeFilter("cq1SubA1", snapshot),
			candidateCQ: cq1SubA1,
			wantMatch:   true,
		},
		"WithinCohortTree: candidate in disjoint cohort tree rejected": {
			filter:      NewWithinCohortTreeFilter("cq1SubA1", snapshot),
			candidateCQ: cq4SubB,
			wantMatch:   false,
		},
		"WithinCohortTree: preemptor in rootB matches itself": {
			filter:      NewWithinCohortTreeFilter("cq4SubB", snapshot),
			candidateCQ: cq4SubB,
			wantMatch:   true,
		},
		"WithinCohortTree: preemptor in rootB rejects candidate in rootA": {
			filter:      NewWithinCohortTreeFilter("cq4SubB", snapshot),
			candidateCQ: cq1SubA1,
			wantMatch:   false,
		},
		"WithinCohortTree: standalone candidate rejected for preemptor with cohort tree": {
			filter:      NewWithinCohortTreeFilter("cq1SubA1", snapshot),
			candidateCQ: cqStandalone1,
			wantMatch:   false,
		},
		"WithinCohortTree: standalone preemptor matches candidate in its own CQ": {
			filter:      NewWithinCohortTreeFilter("cqStandalone1", snapshot),
			candidateCQ: cqStandalone1,
			wantMatch:   true,
		},
		"WithinCohortTree: standalone preemptor rejects candidate in another standalone CQ": {
			filter:      NewWithinCohortTreeFilter("cqStandalone1", snapshot),
			candidateCQ: cqStandalone2,
			wantMatch:   false,
		},
		"WithinCohortTree: standalone preemptor rejects candidate in cohort tree": {
			filter:      NewWithinCohortTreeFilter("cqStandalone1", snapshot),
			candidateCQ: cq1SubA1,
			wantMatch:   false,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			gotMatch := tc.filter.Matches(tc.candidateCQ)
			if gotMatch != tc.wantMatch {
				t.Errorf("Matches() = %v, want %v", gotMatch, tc.wantMatch)
			}
		})
	}
}

func TestWithinLocalQueueFilter(t *testing.T) {
	_, log := utiltesting.ContextWithLog(t)
	filter := NewWithinLocalQueueFilter("ns1", "lq1")

	cases := map[string]struct {
		candidate *workload.Info
		wantMatch bool
	}{
		"matching namespace and queue name": {
			candidate: workload.NewInfo(log, utiltestingapi.MakeWorkload("c-exact", "ns1").Queue("lq1").Obj()),
			wantMatch: true,
		},
		"different local queue name rejected": {
			candidate: workload.NewInfo(log, utiltestingapi.MakeWorkload("c-diff-lq", "ns1").Queue("lq2").Obj()),
			wantMatch: false,
		},
		"different namespace rejected": {
			candidate: workload.NewInfo(log, utiltestingapi.MakeWorkload("c-diff-ns", "ns2").Queue("lq1").Obj()),
			wantMatch: false,
		},
		"different namespace and queue name rejected": {
			candidate: workload.NewInfo(log, utiltestingapi.MakeWorkload("c-diff-both", "ns2").Queue("lq2").Obj()),
			wantMatch: false,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			gotMatch := filter.Matches(tc.candidate)
			if gotMatch != tc.wantMatch {
				t.Errorf("Matches() = %v, want %v", gotMatch, tc.wantMatch)
			}
		})
	}
}
