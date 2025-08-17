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

package cache

import (
	"testing"

	"github.com/google/go-cmp/cmp"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/resources"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	"sigs.k8s.io/kueue/pkg/workload"
)

func TestAvailable(t *testing.T) {
	cases := map[string]struct {
		cohorts                  []kueue.Cohort
		clusterQueues            []kueue.ClusterQueue
		usage                    map[kueue.ClusterQueueReference]resources.FlavorResourceQuantities
		wantAvailable            map[kueue.ClusterQueueReference]resources.FlavorResourceQuantities
		wantPotentiallyAvailable map[kueue.ClusterQueueReference]resources.FlavorResourceQuantities
	}{
		"base cqs": {
			clusterQueues: []kueue.ClusterQueue{
				*utiltesting.MakeClusterQueue("cq1").
					ResourceGroup(
						*utiltesting.MakeFlavorQuotas("red").Resource("cpu", "0").Obj(),
					).Obj(),
				*utiltesting.MakeClusterQueue("cq2").
					ResourceGroup(
						*utiltesting.MakeFlavorQuotas("red").Resource("cpu", "5").Obj(),
						*utiltesting.MakeFlavorQuotas("blue").Resource("cpu", "10").Obj(),
					).Obj(),
			},
			usage: map[kueue.ClusterQueueReference]resources.FlavorResourceQuantities{
				"cq1": {{Flavor: "red", Resource: "cpu"}: 1000},
				"cq2": {
					{Flavor: "red", Resource: "cpu"}:  2_500,
					{Flavor: "blue", Resource: "cpu"}: 1_000,
				},
			},
			wantAvailable: map[kueue.ClusterQueueReference]resources.FlavorResourceQuantities{
				"cq1": {{Flavor: "red", Resource: "cpu"}: 0},
				"cq2": {{Flavor: "red", Resource: "cpu"}: 2_500, {Flavor: "blue", Resource: "cpu"}: 9_000},
			},
			wantPotentiallyAvailable: map[kueue.ClusterQueueReference]resources.FlavorResourceQuantities{
				"cq1": {{Flavor: "red", Resource: "cpu"}: 0},
				"cq2": {{Flavor: "red", Resource: "cpu"}: 5_000, {Flavor: "blue", Resource: "cpu"}: 10_000},
			},
		},
		"cqs with cohort": {
			clusterQueues: []kueue.ClusterQueue{
				*utiltesting.MakeClusterQueue("cq1").
					Cohort("cohort").
					ResourceGroup(
						*utiltesting.MakeFlavorQuotas("red").Resource("cpu", "10").Obj(),
					).Obj(),
				*utiltesting.MakeClusterQueue("cq2").
					Cohort("cohort").
					ResourceGroup(
						*utiltesting.MakeFlavorQuotas("red").Resource("cpu", "10", "", "9").Obj(),
					).Obj(),
			},
			cohorts: []kueue.Cohort{
				utiltesting.MakeCohort("cohort").
					ResourceGroup(
						*utiltesting.MakeFlavorQuotas("red").Resource("cpu", "10").Obj(),
					).Cohort,
			},
			usage: map[kueue.ClusterQueueReference]resources.FlavorResourceQuantities{
				"cq1": {{Flavor: "red", Resource: "cpu"}: 1000},
				"cq2": {{Flavor: "red", Resource: "cpu"}: 500},
			},
			wantAvailable: map[kueue.ClusterQueueReference]resources.FlavorResourceQuantities{
				"cq1": {{Flavor: "red", Resource: "cpu"}: 28_000},
				"cq2": {{Flavor: "red", Resource: "cpu"}: 28_500},
			},
			wantPotentiallyAvailable: map[kueue.ClusterQueueReference]resources.FlavorResourceQuantities{
				"cq1": {{Flavor: "red", Resource: "cpu"}: 29_000},
				"cq2": {{Flavor: "red", Resource: "cpu"}: 30_000},
			},
		},
		"cq borrows from cohort": {
			clusterQueues: []kueue.ClusterQueue{
				*utiltesting.MakeClusterQueue("cq1").
					Cohort("cohort").
					ResourceGroup(
						*utiltesting.MakeFlavorQuotas("red").Resource("cpu", "10").Obj(),
					).Obj(),
			},
			cohorts: []kueue.Cohort{
				utiltesting.MakeCohort("cohort").
					ResourceGroup(
						*utiltesting.MakeFlavorQuotas("red").Resource("cpu", "10").Obj(),
					).Cohort,
			},
			usage:                    map[kueue.ClusterQueueReference]resources.FlavorResourceQuantities{"cq1": {{Flavor: "red", Resource: "cpu"}: 11_000}},
			wantAvailable:            map[kueue.ClusterQueueReference]resources.FlavorResourceQuantities{"cq1": {{Flavor: "red", Resource: "cpu"}: 9_000}},
			wantPotentiallyAvailable: map[kueue.ClusterQueueReference]resources.FlavorResourceQuantities{"cq1": {{Flavor: "red", Resource: "cpu"}: 20_000}},
		},
		"cq oversubscription spills into cohort": {
			clusterQueues: []kueue.ClusterQueue{
				*utiltesting.MakeClusterQueue("cq1").
					Cohort("cohort").
					ResourceGroup(
						*utiltesting.MakeFlavorQuotas("red").Resource("cpu", "10").Obj(),
					).Obj(),
				*utiltesting.MakeClusterQueue("cq2").
					Cohort("cohort").
					ResourceGroup(
						*utiltesting.MakeFlavorQuotas("red").Resource("cpu", "10", "0", "0").Obj(),
					).Obj(),
			},
			cohorts: []kueue.Cohort{
				utiltesting.MakeCohort("cohort").
					ResourceGroup(
						*utiltesting.MakeFlavorQuotas("red").Resource("cpu", "10").Obj(),
					).Cohort,
			},
			usage: map[kueue.ClusterQueueReference]resources.FlavorResourceQuantities{
				"cq1": {{Flavor: "red", Resource: "cpu"}: 31_000},
			},
			wantAvailable: map[kueue.ClusterQueueReference]resources.FlavorResourceQuantities{
				"cq1": {{Flavor: "red", Resource: "cpu"}: 0},
				// even though cq2 doesn't borrow/lend
				// resources from Cohort, the
				// misbehaving cq1's usage spills into
				// its available, to prevent
				// overadmission for (red,cpu) within
				// the CohortTree.
				"cq2": {{Flavor: "red", Resource: "cpu"}: 0},
			},
			wantPotentiallyAvailable: map[kueue.ClusterQueueReference]resources.FlavorResourceQuantities{
				"cq1": {{Flavor: "red", Resource: "cpu"}: 20_000},
				"cq2": {{Flavor: "red", Resource: "cpu"}: 10_000},
			},
		},
		"lending and borrowing limits respected": {
			// cohort has 40 (red, cpu) total
			// cq1 can access 35 = 40 - 5 reserved
			// cq2 can access 12, due to borrowing limit
			// cq3 can access 40, as there are no restrictions.
			clusterQueues: []kueue.ClusterQueue{
				*utiltesting.MakeClusterQueue("cq1").
					Cohort("cohort").
					ResourceGroup(
						*utiltesting.MakeFlavorQuotas("red").Resource("cpu", "10").Obj(),
					).Obj(),
				*utiltesting.MakeClusterQueue("cq2").
					Cohort("cohort").
					ResourceGroup(
						*utiltesting.MakeFlavorQuotas("red").Resource("cpu", "10", "2").Obj(),
					).Obj(),
				*utiltesting.MakeClusterQueue("cq3").
					Cohort("cohort").
					ResourceGroup(
						*utiltesting.MakeFlavorQuotas("red").Resource("cpu", "10", "", "5").Obj(),
					).Obj(),
			},
			cohorts: []kueue.Cohort{
				utiltesting.MakeCohort("cohort").
					ResourceGroup(
						*utiltesting.MakeFlavorQuotas("red").Resource("cpu", "10").Obj(),
					).Cohort,
			},
			usage: map[kueue.ClusterQueueReference]resources.FlavorResourceQuantities{
				"cq1": {{Flavor: "red", Resource: "cpu"}: 20_000},
				"cq2": {{Flavor: "red", Resource: "cpu"}: 10_000},
				"cq3": {{Flavor: "red", Resource: "cpu"}: 6_000},
			},
			wantAvailable: map[kueue.ClusterQueueReference]resources.FlavorResourceQuantities{
				// cq1 uses 20k, cq2 uses 10k, and
				// cq3 uses 1k (not counting first 5k
				// in guaranteedQuota), resulting in
				// 4k left of potential 35k.
				"cq1": {{Flavor: "red", Resource: "cpu"}: 4_000},
				// cq2 has access to only 2k of the
				// remaining 4k due to its borrowing
				// limit.
				"cq2": {{Flavor: "red", Resource: "cpu"}: 2_000},
				// 40k - (20k + 10k + 6k) = 4k
				"cq3": {{Flavor: "red", Resource: "cpu"}: 4_000},
			},
			wantPotentiallyAvailable: map[kueue.ClusterQueueReference]resources.FlavorResourceQuantities{
				"cq1": {{Flavor: "red", Resource: "cpu"}: 35_000},
				"cq2": {{Flavor: "red", Resource: "cpu"}: 12_000},
				"cq3": {{Flavor: "red", Resource: "cpu"}: 40_000},
			},
		},
		"hierarchical cohort": {
			//               root
			//              /    \
			//            left    right
			//           /           \
			//        cq1            cq2
			clusterQueues: []kueue.ClusterQueue{
				*utiltesting.MakeClusterQueue("cq1").
					Cohort("left").
					ResourceGroup(
						*utiltesting.MakeFlavorQuotas("red").Resource("cpu", "10").Obj(),
					).Obj(),
				*utiltesting.MakeClusterQueue("cq2").
					Cohort("right").
					ResourceGroup(
						*utiltesting.MakeFlavorQuotas("red").Resource("cpu", "10", "", "0").Obj(),
					).Obj(),
			},
			cohorts: []kueue.Cohort{
				utiltesting.MakeCohort("root").Cohort,
				utiltesting.MakeCohort("left").
					Parent("root").
					ResourceGroup(
						*utiltesting.MakeFlavorQuotas("red").Resource("cpu", "10").Obj(),
					).Cohort,
				utiltesting.MakeCohort("right").
					Parent("root").
					ResourceGroup(
						*utiltesting.MakeFlavorQuotas("red").Resource("cpu", "10").Obj(),
					).Cohort,
			},
			usage: map[kueue.ClusterQueueReference]resources.FlavorResourceQuantities{
				"cq1": {{Flavor: "red", Resource: "cpu"}: 10_000},
				"cq2": {{Flavor: "red", Resource: "cpu"}: 5_000},
			},
			wantAvailable: map[kueue.ClusterQueueReference]resources.FlavorResourceQuantities{
				// 30k available - 10k usage.
				// no capacity/usage from right subtree
				"cq1": {{Flavor: "red", Resource: "cpu"}: 20_000},
				// 40k available - 15k usage.
				"cq2": {{Flavor: "red", Resource: "cpu"}: 25_000},
			},
			wantPotentiallyAvailable: map[kueue.ClusterQueueReference]resources.FlavorResourceQuantities{
				"cq1": {{Flavor: "red", Resource: "cpu"}: 30_000},
				"cq2": {{Flavor: "red", Resource: "cpu"}: 40_000},
			},
		},
		"hierarchical cohort respects borrowing limit": {
			//               root
			//              5    \
			//            left    right -- right-cq
			//           /    5
			//   left-cq1      left-cq2
			clusterQueues: []kueue.ClusterQueue{
				*utiltesting.MakeClusterQueue("left-cq1").
					Cohort("left").
					ResourceGroup(
						*utiltesting.MakeFlavorQuotas("red").Resource("cpu", "10").Obj(),
					).Obj(),
				*utiltesting.MakeClusterQueue("left-cq2").
					Cohort("left").
					ResourceGroup(
						*utiltesting.MakeFlavorQuotas("red").Resource("cpu", "10", "5").Obj(),
					).Obj(),
				*utiltesting.MakeClusterQueue("right-cq").
					Cohort("right").
					ResourceGroup(
						*utiltesting.MakeFlavorQuotas("red").Resource("cpu", "10").Obj(),
					).Obj(),
			},
			cohorts: []kueue.Cohort{
				utiltesting.MakeCohort("root").Cohort,
				utiltesting.MakeCohort("left").
					Parent("root").
					ResourceGroup(
						*utiltesting.MakeFlavorQuotas("red").Resource("cpu", "10", "5").Obj(),
					).Cohort,
				utiltesting.MakeCohort("right").
					Parent("root").
					ResourceGroup(
						*utiltesting.MakeFlavorQuotas("red").Resource("cpu", "10").Obj(),
					).Cohort,
			},
			wantAvailable: map[kueue.ClusterQueueReference]resources.FlavorResourceQuantities{
				// 30k in "left" subtree + 5k limit above tree
				"left-cq1": {{Flavor: "red", Resource: "cpu"}: 35_000},
				// 10k + 5k lending limit
				"left-cq2": {{Flavor: "red", Resource: "cpu"}: 15_000},
				// all 50k in "root" subtree
				"right-cq": {{Flavor: "red", Resource: "cpu"}: 50_000},
			},
			wantPotentiallyAvailable: map[kueue.ClusterQueueReference]resources.FlavorResourceQuantities{
				"left-cq1": {{Flavor: "red", Resource: "cpu"}: 35_000},
				"left-cq2": {{Flavor: "red", Resource: "cpu"}: 15_000},
				"right-cq": {{Flavor: "red", Resource: "cpu"}: 50_000},
			},
		},
		"hierarchical cohort respects lending limit": {
			//               root -- root-cq
			//              5    \
			//            left    right -- right-cq
			//           5    \
			//   left-cq1      left-cq2
			clusterQueues: []kueue.ClusterQueue{
				*utiltesting.MakeClusterQueue("left-cq1").
					Cohort("left").
					ResourceGroup(
						*utiltesting.MakeFlavorQuotas("red").Resource("cpu", "10", "", "5").Obj(),
					).Obj(),
				*utiltesting.MakeClusterQueue("left-cq2").
					Cohort("left").
					ResourceGroup(
						*utiltesting.MakeFlavorQuotas("red").Resource("cpu", "10").Obj(),
					).Obj(),
				*utiltesting.MakeClusterQueue("right-cq").
					Cohort("right").
					ResourceGroup(
						*utiltesting.MakeFlavorQuotas("red").Resource("cpu", "0").Obj(),
					).Obj(),
				*utiltesting.MakeClusterQueue("root-cq").
					Cohort("root").
					ResourceGroup(
						*utiltesting.MakeFlavorQuotas("red").Resource("cpu", "0").Obj(),
					).Obj(),
			},
			cohorts: []kueue.Cohort{
				utiltesting.MakeCohort("root").Cohort,
				utiltesting.MakeCohort("left").
					Parent("root").
					ResourceGroup(
						*utiltesting.MakeFlavorQuotas("red").Resource("cpu", "0", "", "5").Obj(),
					).Cohort,
				utiltesting.MakeCohort("right").
					Parent("root").
					ResourceGroup(
						*utiltesting.MakeFlavorQuotas("red").Resource("cpu", "0").Obj(),
					).Cohort,
			},
			wantAvailable: map[kueue.ClusterQueueReference]resources.FlavorResourceQuantities{
				"left-cq1": {{Flavor: "red", Resource: "cpu"}: 20_000},
				// only 5k of left-cq1's resources are available,
				// plus 10k of its own.
				"left-cq2": {{Flavor: "red", Resource: "cpu"}: 15_000},
				// 5k lending limit from left to root.
				"right-cq": {{Flavor: "red", Resource: "cpu"}: 5_000},
				"root-cq":  {{Flavor: "red", Resource: "cpu"}: 5_000},
			},
			wantPotentiallyAvailable: map[kueue.ClusterQueueReference]resources.FlavorResourceQuantities{
				"left-cq1": {{Flavor: "red", Resource: "cpu"}: 20_000},
				"left-cq2": {{Flavor: "red", Resource: "cpu"}: 15_000},
				"right-cq": {{Flavor: "red", Resource: "cpu"}: 5_000},
				"root-cq":  {{Flavor: "red", Resource: "cpu"}: 5_000},
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx, log := utiltesting.ContextWithLog(t)
			cache := New(utiltesting.NewFakeClient())
			cache.AddOrUpdateResourceFlavor(log, utiltesting.MakeResourceFlavor("red").Obj())
			cache.AddOrUpdateResourceFlavor(log, utiltesting.MakeResourceFlavor("blue").Obj())
			for _, cq := range tc.clusterQueues {
				_ = cache.AddClusterQueue(ctx, &cq)
			}
			for _, cohort := range tc.cohorts {
				_ = cache.AddOrUpdateCohort(&cohort)
			}

			snapshot, err := cache.Snapshot(ctx)
			if err != nil {
				t.Fatalf("unexpected error while building snapshot: %v", err)
			}
			// before adding usage
			{
				clusterQueues := snapshot.ClusterQueues()
				gotAvailable := make(map[kueue.ClusterQueueReference]resources.FlavorResourceQuantities, len(clusterQueues))
				gotPotentiallyAvailable := make(map[kueue.ClusterQueueReference]resources.FlavorResourceQuantities, len(clusterQueues))
				for _, cq := range clusterQueues {
					numFrs := len(cq.ResourceNode.Quotas)
					gotAvailable[cq.Name] = make(resources.FlavorResourceQuantities, numFrs)
					gotPotentiallyAvailable[cq.Name] = make(resources.FlavorResourceQuantities, numFrs)
					for fr := range cq.ResourceNode.Quotas {
						gotAvailable[cq.Name][fr] = cq.Available(fr)
						gotPotentiallyAvailable[cq.Name][fr] = potentialAvailable(cq, fr)
					}
				}
				// before adding usage, available == potentiallyAvailable
				if diff := cmp.Diff(tc.wantPotentiallyAvailable, gotAvailable); diff != "" {
					t.Errorf("unexpected available (-want/+got):\n%s", diff)
				}
				if diff := cmp.Diff(tc.wantPotentiallyAvailable, gotPotentiallyAvailable); diff != "" {
					t.Errorf("unexpected potentially available (-want/+got):\n%s", diff)
				}
			}

			// add usage
			{
				for cqName, usage := range tc.usage {
					snapshot.ClusterQueue(cqName).AddUsage(workload.Usage{Quota: usage})
				}
				clusterQueues := snapshot.ClusterQueues()
				gotAvailable := make(map[kueue.ClusterQueueReference]resources.FlavorResourceQuantities, len(clusterQueues))
				gotPotentiallyAvailable := make(map[kueue.ClusterQueueReference]resources.FlavorResourceQuantities, len(clusterQueues))
				for _, cq := range clusterQueues {
					numFrs := len(cq.ResourceNode.Quotas)
					gotAvailable[cq.Name] = make(resources.FlavorResourceQuantities, numFrs)
					gotPotentiallyAvailable[cq.Name] = make(resources.FlavorResourceQuantities, numFrs)
					for fr := range cq.ResourceNode.Quotas {
						gotAvailable[cq.Name][fr] = cq.Available(fr)
						gotPotentiallyAvailable[cq.Name][fr] = potentialAvailable(cq, fr)
					}
				}
				if diff := cmp.Diff(tc.wantAvailable, gotAvailable); diff != "" {
					t.Errorf("unexpected available (-want/+got):\n%s", diff)
				}
				if diff := cmp.Diff(tc.wantPotentiallyAvailable, gotPotentiallyAvailable); diff != "" {
					t.Errorf("unexpected potentially available (-want/+got):\n%s", diff)
				}
			}

			// remove usage
			{
				for cqName, usage := range tc.usage {
					snapshot.ClusterQueue(cqName).RemoveUsage(workload.Usage{Quota: usage})
				}
				clusterQueues := snapshot.ClusterQueues()
				gotAvailable := make(map[kueue.ClusterQueueReference]resources.FlavorResourceQuantities, len(clusterQueues))
				gotPotentiallyAvailable := make(map[kueue.ClusterQueueReference]resources.FlavorResourceQuantities, len(clusterQueues))
				for _, cq := range clusterQueues {
					numFrs := len(cq.ResourceNode.Quotas)
					gotAvailable[cq.Name] = make(resources.FlavorResourceQuantities, numFrs)
					gotPotentiallyAvailable[cq.Name] = make(resources.FlavorResourceQuantities, numFrs)
					for fr := range cq.ResourceNode.Quotas {
						gotAvailable[cq.Name][fr] = cq.Available(fr)
						gotPotentiallyAvailable[cq.Name][fr] = potentialAvailable(cq, fr)
					}
				}
				// once again, available == potentiallyAvailable
				if diff := cmp.Diff(tc.wantPotentiallyAvailable, gotAvailable); diff != "" {
					t.Errorf("unexpected available (-want/+got):\n%s", diff)
				}
				if diff := cmp.Diff(tc.wantPotentiallyAvailable, gotPotentiallyAvailable); diff != "" {
					t.Errorf("unexpected potentiallyAvailable (-want/+got):\n%s", diff)
				}
			}
		})
	}
}
