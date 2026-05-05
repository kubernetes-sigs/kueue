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

package fairsharing

import (
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	clocktesting "k8s.io/utils/clock/testing"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	schdcache "sigs.k8s.io/kueue/pkg/cache/scheduler"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/pkg/workload"
)

func TestMakeClusterQueueOrdering(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	clk := clocktesting.NewFakeClock(now)

	cases := map[string]struct {
		clusterQueues []*kueue.ClusterQueue
		cohorts       []*kueue.Cohort
		// admitted workloads create resource usage in target CQs (making them borrow).
		// The same workloads are used as preemption candidates.
		admitted    []kueue.Workload
		preemptorCQ kueue.ClusterQueueReference
		// candidateCQs restricts which admitted workloads become candidates (by CQ name).
		candidateCQs []kueue.ClusterQueueReference
		// actions controls per-iteration behavior: "drop" calls DropQueue, anything else calls PopWorkload.
		actions   []string
		wantOrder []kueue.ClusterQueueReference
	}{
		"no cohort: preemptor CQ yielded for in-CQ preemption; repro for nil pointer panic issue": {
			clusterQueues: []*kueue.ClusterQueue{
				utiltestingapi.MakeClusterQueue("preemptor").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "4").Obj()).
					Obj(),
			},
			admitted: []kueue.Workload{
				*utiltestingapi.MakeWorkload("wl1", "ns").Request(corev1.ResourceCPU, "1").
					SimpleReserveQuota("preemptor", "default", now).Obj(),
			},
			preemptorCQ:  "preemptor",
			candidateCQs: []kueue.ClusterQueueReference{"preemptor"},
			wantOrder:    []kueue.ClusterQueueReference{"preemptor"},
		},
		"non-borrowing CQ is pruned even with candidates": {
			clusterQueues: []*kueue.ClusterQueue{
				utiltestingapi.MakeClusterQueue("preemptor").Cohort("all").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "4").Obj()).Obj(),
				// "target" is within its nominal quota (2 < 5), so DRS = 0 (not borrowing).
				utiltestingapi.MakeClusterQueue("target").Cohort("all").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "5").Obj()).Obj(),
			},
			admitted: []kueue.Workload{
				*utiltestingapi.MakeWorkload("t1", "ns").Request(corev1.ResourceCPU, "2").
					SimpleReserveQuota("target", "default", now).Obj(),
			},
			preemptorCQ:  "preemptor",
			candidateCQs: []kueue.ClusterQueueReference{"target"},
			wantOrder:    nil,
		},
		"higher DRS CQ returned before lower DRS CQ": {
			// Cohort "all": preemptor(nominal=4), high(nominal=2, usage=5), low(nominal=2, usage=3).
			// DRS(high) > DRS(low), so "high" is yielded first.
			clusterQueues: []*kueue.ClusterQueue{
				utiltestingapi.MakeClusterQueue("preemptor").Cohort("all").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "4").Obj()).Obj(),
				utiltestingapi.MakeClusterQueue("high").Cohort("all").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "2").Obj()).Obj(),
				utiltestingapi.MakeClusterQueue("low").Cohort("all").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "2").Obj()).Obj(),
			},
			admitted: []kueue.Workload{
				*utiltestingapi.MakeWorkload("h1", "ns").Request(corev1.ResourceCPU, "5").
					SimpleReserveQuota("high", "default", now).Obj(),
				*utiltestingapi.MakeWorkload("l1", "ns").Request(corev1.ResourceCPU, "3").
					SimpleReserveQuota("low", "default", now).Obj(),
			},
			preemptorCQ:  "preemptor",
			candidateCQs: []kueue.ClusterQueueReference{"high", "low"},
			wantOrder:    []kueue.ClusterQueueReference{"high", "low"},
		},
		"CQ with highest DRS returned again while it still has candidates": {
			// "high" has 2 candidates; it keeps its high DRS across iterations (DRS
			// is based on actual resource usage, not candidate count), so it is
			// returned twice before "low" is returned.
			clusterQueues: []*kueue.ClusterQueue{
				utiltestingapi.MakeClusterQueue("preemptor").Cohort("all").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "4").Obj()).Obj(),
				utiltestingapi.MakeClusterQueue("high").Cohort("all").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "2").Obj()).Obj(),
				utiltestingapi.MakeClusterQueue("low").Cohort("all").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "2").Obj()).Obj(),
			},
			admitted: []kueue.Workload{
				*utiltestingapi.MakeWorkload("h1", "ns").Request(corev1.ResourceCPU, "3").
					SimpleReserveQuota("high", "default", now).Obj(),
				*utiltestingapi.MakeWorkload("h2", "ns").Request(corev1.ResourceCPU, "2").
					SimpleReserveQuota("high", "default", now).Obj(),
				*utiltestingapi.MakeWorkload("l1", "ns").Request(corev1.ResourceCPU, "3").
					SimpleReserveQuota("low", "default", now).Obj(),
			},
			preemptorCQ:  "preemptor",
			candidateCQs: []kueue.ClusterQueueReference{"high", "low"},
			wantOrder:    []kueue.ClusterQueueReference{"high", "high", "low"},
		},
		"drop queue prevents CQ from being returned again": {
			// Same setup as above; calling DropQueue on the first "high" occurrence
			// skips its second candidate and moves immediately to "low".
			clusterQueues: []*kueue.ClusterQueue{
				utiltestingapi.MakeClusterQueue("preemptor").Cohort("all").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "4").Obj()).Obj(),
				utiltestingapi.MakeClusterQueue("high").Cohort("all").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "2").Obj()).Obj(),
				utiltestingapi.MakeClusterQueue("low").Cohort("all").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "2").Obj()).Obj(),
			},
			admitted: []kueue.Workload{
				*utiltestingapi.MakeWorkload("h1", "ns").Request(corev1.ResourceCPU, "3").
					SimpleReserveQuota("high", "default", now).Obj(),
				*utiltestingapi.MakeWorkload("h2", "ns").Request(corev1.ResourceCPU, "2").
					SimpleReserveQuota("high", "default", now).Obj(),
				*utiltestingapi.MakeWorkload("l1", "ns").Request(corev1.ResourceCPU, "3").
					SimpleReserveQuota("low", "default", now).Obj(),
			},
			preemptorCQ:  "preemptor",
			candidateCQs: []kueue.ClusterQueueReference{"high", "low"},
			// Drop "high" on first occurrence; PopWorkload for "low".
			actions:   []string{"drop", "pop"},
			wantOrder: []kueue.ClusterQueueReference{"high", "low"},
		},
		"hierarchical cohorts: higher-DRS subtree visited first": {
			// Tree:
			//           root
			//          /    \
			//    left-cohort  right-cohort
			//        |              |
			//     left-cq        right-cq       preemptor-cq
			//   (usage=5>2)     (usage=3>2)     (usage=0, nominal=4)
			//
			// left-cohort has higher aggregate DRS, so left-cq is returned first.
			clusterQueues: []*kueue.ClusterQueue{
				utiltestingapi.MakeClusterQueue("preemptor-cq").Cohort("root").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "4").Obj()).Obj(),
				utiltestingapi.MakeClusterQueue("left-cq").Cohort("left-cohort").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "2").Obj()).Obj(),
				utiltestingapi.MakeClusterQueue("right-cq").Cohort("right-cohort").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "2").Obj()).Obj(),
			},
			cohorts: []*kueue.Cohort{
				utiltestingapi.MakeCohort("root").Obj(),
				utiltestingapi.MakeCohort("left-cohort").Parent("root").Obj(),
				utiltestingapi.MakeCohort("right-cohort").Parent("root").Obj(),
			},
			admitted: []kueue.Workload{
				*utiltestingapi.MakeWorkload("lc1", "ns").Request(corev1.ResourceCPU, "5").
					SimpleReserveQuota("left-cq", "default", now).Obj(),
				*utiltestingapi.MakeWorkload("rc1", "ns").Request(corev1.ResourceCPU, "3").
					SimpleReserveQuota("right-cq", "default", now).Obj(),
			},
			preemptorCQ:  "preemptor-cq",
			candidateCQs: []kueue.ClusterQueueReference{"left-cq", "right-cq"},
			wantOrder:    []kueue.ClusterQueueReference{"left-cq", "right-cq"},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx, log := utiltesting.ContextWithLog(t)
			cl := utiltesting.NewClientBuilder().
				WithLists(&kueue.WorkloadList{Items: tc.admitted}).
				Build()
			cqCache := schdcache.New(cl)
			cqCache.AddOrUpdateResourceFlavor(log, utiltestingapi.MakeResourceFlavor("default").Obj())

			for _, cq := range tc.clusterQueues {
				if err := cqCache.AddClusterQueue(ctx, cq); err != nil {
					t.Fatalf("AddClusterQueue(%q): %v", cq.Name, err)
				}
			}
			for _, cohort := range tc.cohorts {
				if err := cqCache.AddOrUpdateCohort(cohort); err != nil {
					t.Fatalf("AddOrUpdateCohort(%q): %v", cohort.Name, err)
				}
			}

			snapshot, err := cqCache.Snapshot(ctx)
			if err != nil {
				t.Fatalf("Snapshot: %v", err)
			}

			preemptorCQ := snapshot.ClusterQueue(tc.preemptorCQ)

			candidateCQSet := sets.New(tc.candidateCQs...)
			var candidates []*workload.Info
			for i := range tc.admitted {
				info := workload.NewInfo(&tc.admitted[i])
				if candidateCQSet.Has(info.ClusterQueue) {
					candidates = append(candidates, info)
				}
			}

			ordering := MakeClusterQueueOrdering(preemptorCQ, candidates, log, clk)

			var gotOrder []kueue.ClusterQueueReference
			actionIdx := 0
			for target := range ordering.Iter() {
				gotOrder = append(gotOrder, target.GetTargetCq().GetName())
				if actionIdx < len(tc.actions) && tc.actions[actionIdx] == "drop" {
					ordering.DropQueue(target)
				} else {
					target.PopWorkload()
				}
				actionIdx++
				if len(gotOrder) > 50 {
					t.Error("exceeded 50 iterations, likely an infinite loop")
				}
			}

			if diff := cmp.Diff(tc.wantOrder, gotOrder, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("ordering mismatch (-want,+got):\n%s", diff)
			}
		})
	}
}
