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
	"fmt"
	"math"
	"testing"

	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/sets"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/resources"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	"sigs.k8s.io/kueue/pkg/workload"
)

func TestDominantResourceShare(t *testing.T) {
	type nodeType bool
	var (
		nodeTypeCq     nodeType = false
		nodeTypeCohort nodeType = true
	)

	type fairSharingResult struct {
		Name     string
		NodeType nodeType
		DrValue  int
		DrName   corev1.ResourceName
	}

	cases := map[string]struct {
		usage               resources.FlavorResourceQuantities
		clusterQueue        *kueue.ClusterQueue
		lendingClusterQueue *kueue.ClusterQueue
		cohorts             []*kueue.Cohort
		flvResQ             resources.FlavorResourceQuantities
		want                []fairSharingResult
	}{
		"no cohort": {
			usage: resources.FlavorResourceQuantities{
				{Flavor: "default", Resource: corev1.ResourceCPU}: 1_000,
				{Flavor: "default", Resource: "example.com/gpu"}:  2,
			},
			clusterQueue: utiltesting.MakeClusterQueue("cq").
				ResourceGroup(
					*utiltesting.MakeFlavorQuotas("default").
						ResourceQuotaWrapper("cpu").NominalQuota("2000").Append().
						ResourceQuotaWrapper("example.com/gpu").NominalQuota("5").Append().
						Obj(),
				).Obj(),
			want: []fairSharingResult{
				{
					Name:     "cq",
					NodeType: nodeTypeCq,
					DrName:   "",
					DrValue:  0,
				},
			},
		},
		"usage below nominal": {
			usage: resources.FlavorResourceQuantities{
				{Flavor: "default", Resource: corev1.ResourceCPU}: 1_000,
				{Flavor: "default", Resource: "example.com/gpu"}:  2,
			},
			clusterQueue: utiltesting.MakeClusterQueue("cq").
				Cohort("test-cohort").
				FairWeight(oneQuantity).
				ResourceGroup(
					*utiltesting.MakeFlavorQuotas("default").
						ResourceQuotaWrapper("cpu").NominalQuota("2").Append().
						ResourceQuotaWrapper("example.com/gpu").NominalQuota("5").Append().
						Obj(),
				).Obj(),
			lendingClusterQueue: utiltesting.MakeClusterQueue("lending-cq").
				Cohort("test-cohort").
				FairWeight(oneQuantity).
				ResourceGroup(
					*utiltesting.MakeFlavorQuotas("default").
						ResourceQuotaWrapper("cpu").NominalQuota("8").Append().
						ResourceQuotaWrapper("example.com/gpu").NominalQuota("5").Append().
						Obj(),
				).Obj(),
			want: []fairSharingResult{
				{
					Name:     "cq",
					NodeType: nodeTypeCq,
					DrName:   "",
					DrValue:  0,
				},
				{
					Name:     "lending-cq",
					NodeType: nodeTypeCq,
					DrName:   "",
					DrValue:  0,
				},
				{
					Name:     "test-cohort",
					NodeType: nodeTypeCohort,
					DrName:   "",
					DrValue:  0,
				},
			},
		},
		"usage above nominal": {
			usage: resources.FlavorResourceQuantities{
				{Flavor: "default", Resource: corev1.ResourceCPU}: 3_000,
				{Flavor: "default", Resource: "example.com/gpu"}:  7,
			},
			clusterQueue: utiltesting.MakeClusterQueue("cq").
				Cohort("test-cohort").
				FairWeight(oneQuantity).
				ResourceGroup(
					*utiltesting.MakeFlavorQuotas("default").
						ResourceQuotaWrapper("cpu").NominalQuota("2").Append().
						ResourceQuotaWrapper("example.com/gpu").NominalQuota("5").Append().
						Obj(),
				).Obj(),
			lendingClusterQueue: utiltesting.MakeClusterQueue("lending-cq").
				Cohort("test-cohort").
				FairWeight(oneQuantity).
				ResourceGroup(
					*utiltesting.MakeFlavorQuotas("default").
						ResourceQuotaWrapper("cpu").NominalQuota("8").Append().
						ResourceQuotaWrapper("example.com/gpu").NominalQuota("5").Append().
						Obj(),
				).Obj(),
			want: []fairSharingResult{
				{
					Name:     "cq",
					NodeType: nodeTypeCq,
					DrName:   "example.com/gpu",
					DrValue:  200, // (7-5)*1000/10
				},
				{
					Name:     "lending-cq",
					NodeType: nodeTypeCq,
					DrName:   "",
					DrValue:  0,
				},
				{
					Name:     "test-cohort",
					NodeType: nodeTypeCohort,
					DrName:   "",
					DrValue:  0,
				},
			},
		},
		"one resource above nominal": {
			usage: resources.FlavorResourceQuantities{
				{Flavor: "default", Resource: corev1.ResourceCPU}: 3_000,
				{Flavor: "default", Resource: "example.com/gpu"}:  3,
			},
			clusterQueue: utiltesting.MakeClusterQueue("cq").
				Cohort("test-cohort").
				FairWeight(oneQuantity).
				ResourceGroup(
					*utiltesting.MakeFlavorQuotas("default").
						ResourceQuotaWrapper("cpu").NominalQuota("2").Append().
						ResourceQuotaWrapper("example.com/gpu").NominalQuota("5").Append().
						Obj(),
				).Obj(),
			lendingClusterQueue: utiltesting.MakeClusterQueue("lending-cq").
				Cohort("test-cohort").
				FairWeight(oneQuantity).
				ResourceGroup(
					*utiltesting.MakeFlavorQuotas("default").
						ResourceQuotaWrapper("cpu").NominalQuota("8").Append().
						ResourceQuotaWrapper("example.com/gpu").NominalQuota("5").Append().
						Obj(),
				).Obj(),
			want: []fairSharingResult{
				{
					Name:     "cq",
					NodeType: nodeTypeCq,
					DrName:   corev1.ResourceCPU,
					DrValue:  100, // (3-2)*1000/10
				},
				{
					Name:     "lending-cq",
					NodeType: nodeTypeCq,
					DrName:   "",
					DrValue:  0,
				},
				{
					Name:     "test-cohort",
					NodeType: nodeTypeCohort,
					DrName:   "",
					DrValue:  0,
				},
			},
		},
		"usage with workload above nominal": {
			usage: resources.FlavorResourceQuantities{
				{Flavor: "default", Resource: corev1.ResourceCPU}: 1_000,
				{Flavor: "default", Resource: "example.com/gpu"}:  2,
			},
			clusterQueue: utiltesting.MakeClusterQueue("cq").
				Cohort("test-cohort").
				FairWeight(oneQuantity).
				ResourceGroup(
					*utiltesting.MakeFlavorQuotas("default").
						ResourceQuotaWrapper("cpu").NominalQuota("2").Append().
						ResourceQuotaWrapper("example.com/gpu").NominalQuota("5").Append().
						Obj(),
				).Obj(),
			lendingClusterQueue: utiltesting.MakeClusterQueue("lending-cq").
				Cohort("test-cohort").
				FairWeight(oneQuantity).
				ResourceGroup(
					*utiltesting.MakeFlavorQuotas("default").
						ResourceQuotaWrapper("cpu").NominalQuota("8").Append().
						ResourceQuotaWrapper("example.com/gpu").NominalQuota("5").Append().
						Obj(),
				).Obj(),
			flvResQ: resources.FlavorResourceQuantities{
				{Flavor: "default", Resource: corev1.ResourceCPU}: 4_000,
				{Flavor: "default", Resource: "example.com/gpu"}:  4,
			},
			want: []fairSharingResult{
				{
					Name:     "cq",
					NodeType: nodeTypeCq,
					DrName:   corev1.ResourceCPU,
					DrValue:  300, // (1+4-2)*1000/10
				},
				{
					Name:     "lending-cq",
					NodeType: nodeTypeCq,
					DrName:   "",
					DrValue:  0,
				},
				{
					Name:     "test-cohort",
					NodeType: nodeTypeCohort,
					DrName:   "",
					DrValue:  0,
				},
			},
		},
		"A resource with zero lendable": {
			usage: resources.FlavorResourceQuantities{
				{Flavor: "default", Resource: corev1.ResourceCPU}: 1_000,
				{Flavor: "default", Resource: "example.com/gpu"}:  1,
			},
			clusterQueue: utiltesting.MakeClusterQueue("cq").
				Cohort("test-cohort").
				FairWeight(oneQuantity).
				ResourceGroup(
					*utiltesting.MakeFlavorQuotas("default").
						ResourceQuotaWrapper("cpu").NominalQuota("2").Append().
						ResourceQuotaWrapper("example.com/gpu").NominalQuota("2").LendingLimit("0").Append().
						Obj(),
				).Obj(),
			lendingClusterQueue: utiltesting.MakeClusterQueue("lending-cq").
				Cohort("test-cohort").
				FairWeight(oneQuantity).
				ResourceGroup(
					*utiltesting.MakeFlavorQuotas("default").
						ResourceQuotaWrapper("cpu").NominalQuota("8").Append().
						ResourceQuotaWrapper("example.com/gpu").NominalQuota("64").LendingLimit("0").Append().
						Obj(),
				).Obj(),
			flvResQ: resources.FlavorResourceQuantities{
				{Flavor: "default", Resource: corev1.ResourceCPU}: 4_000,
				{Flavor: "default", Resource: "example.com/gpu"}:  4,
			},
			want: []fairSharingResult{
				{
					Name:     "cq",
					NodeType: nodeTypeCq,
					DrName:   corev1.ResourceCPU,
					DrValue:  300, // (1+4-2)*1000/10
				},
				{
					Name:     "lending-cq",
					NodeType: nodeTypeCq,
					DrName:   "",
					DrValue:  0,
				},
				{
					Name:     "test-cohort",
					NodeType: nodeTypeCohort,
					DrName:   "",
					DrValue:  0,
				},
			},
		},
		"multiple flavors": {
			usage: resources.FlavorResourceQuantities{
				{Flavor: "on-demand", Resource: corev1.ResourceCPU}: 15_000,
				{Flavor: "spot", Resource: corev1.ResourceCPU}:      5_000,
			},
			clusterQueue: utiltesting.MakeClusterQueue("cq").
				Cohort("test-cohort").
				FairWeight(oneQuantity).
				ResourceGroup(
					*utiltesting.MakeFlavorQuotas("on-demand").
						ResourceQuotaWrapper("cpu").NominalQuota("20").Append().
						Obj(),
					*utiltesting.MakeFlavorQuotas("spot").
						ResourceQuotaWrapper("cpu").NominalQuota("80").Append().
						Obj(),
				).Obj(),
			lendingClusterQueue: utiltesting.MakeClusterQueue("lending-cq").
				Cohort("test-cohort").
				FairWeight(oneQuantity).
				ResourceGroup(
					*utiltesting.MakeFlavorQuotas("default").
						ResourceQuotaWrapper("cpu").NominalQuota("100").Append().
						Obj(),
				).Obj(),
			flvResQ: resources.FlavorResourceQuantities{
				{Flavor: "on-demand", Resource: corev1.ResourceCPU}: 10_000,
			},
			want: []fairSharingResult{
				{
					Name:     "cq",
					NodeType: nodeTypeCq,
					DrName:   corev1.ResourceCPU,
					DrValue:  25, // ((15+10-20)+0)*1000/200 (spot under nominal)
				},
				{
					Name:     "lending-cq",
					NodeType: nodeTypeCq,
					DrName:   "",
					DrValue:  0,
				},
				{
					Name:     "test-cohort",
					NodeType: nodeTypeCohort,
					DrName:   "",
					DrValue:  0,
				},
			},
		},
		"above nominal with integer weight": {
			usage: resources.FlavorResourceQuantities{
				{Flavor: "default", Resource: "example.com/gpu"}: 7,
			},
			clusterQueue: utiltesting.MakeClusterQueue("cq").
				Cohort("test-cohort").
				FairWeight(resource.MustParse("2")).
				ResourceGroup(
					*utiltesting.MakeFlavorQuotas("default").
						ResourceQuotaWrapper("example.com/gpu").NominalQuota("5").Append().
						Obj(),
				).Obj(),
			lendingClusterQueue: utiltesting.MakeClusterQueue("lending-cq").
				Cohort("test-cohort").
				FairWeight(oneQuantity).
				ResourceGroup(
					*utiltesting.MakeFlavorQuotas("default").
						ResourceQuotaWrapper("example.com/gpu").NominalQuota("5").Append().
						Obj(),
				).Obj(),
			want: []fairSharingResult{
				{
					Name:     "cq",
					NodeType: nodeTypeCq,
					DrName:   "example.com/gpu",
					DrValue:  100, // ((7-5)*1000/10)/2
				},
				{
					Name:     "lending-cq",
					NodeType: nodeTypeCq,
					DrName:   "",
					DrValue:  0,
				},
				{
					Name:     "test-cohort",
					NodeType: nodeTypeCohort,
					DrName:   "",
					DrValue:  0,
				},
			},
		},
		"above nominal with decimal weight": {
			usage: resources.FlavorResourceQuantities{
				{Flavor: "default", Resource: "example.com/gpu"}: 7,
			},
			clusterQueue: utiltesting.MakeClusterQueue("cq").
				Cohort("test-cohort").
				FairWeight(resource.MustParse("0.5")).
				ResourceGroup(
					*utiltesting.MakeFlavorQuotas("default").
						ResourceQuotaWrapper("example.com/gpu").NominalQuota("5").Append().
						Obj(),
				).Obj(),
			lendingClusterQueue: utiltesting.MakeClusterQueue("lending-cq").
				Cohort("test-cohort").
				FairWeight(oneQuantity).
				ResourceGroup(
					*utiltesting.MakeFlavorQuotas("default").
						ResourceQuotaWrapper("example.com/gpu").NominalQuota("5").Append().
						Obj(),
				).Obj(),
			want: []fairSharingResult{
				{
					Name:     "cq",
					NodeType: nodeTypeCq,
					DrName:   "example.com/gpu",
					DrValue:  400, // ((7-5)*1000/10)/(1/2)
				},
				{
					Name:     "lending-cq",
					NodeType: nodeTypeCq,
					DrName:   "",
					DrValue:  0,
				},
				{
					Name:     "test-cohort",
					NodeType: nodeTypeCohort,
					DrName:   "",
					DrValue:  0,
				},
			},
		},
		"above nominal with zero weight": {
			usage: resources.FlavorResourceQuantities{
				{Flavor: "default", Resource: "example.com/gpu"}: 7,
			},
			clusterQueue: utiltesting.MakeClusterQueue("cq").
				Cohort("test-cohort").
				FairWeight(resource.MustParse("0")).
				ResourceGroup(
					*utiltesting.MakeFlavorQuotas("default").
						ResourceQuotaWrapper("example.com/gpu").NominalQuota("5").Append().
						Obj(),
				).Obj(),
			lendingClusterQueue: utiltesting.MakeClusterQueue("lending-cq").
				Cohort("test-cohort").
				FairWeight(oneQuantity).
				ResourceGroup(
					*utiltesting.MakeFlavorQuotas("default").
						ResourceQuotaWrapper("example.com/gpu").NominalQuota("10").Append().
						Obj(),
				).Obj(),
			want: []fairSharingResult{
				{
					Name:     "cq",
					NodeType: nodeTypeCq,
					DrName:   "example.com/gpu",
					DrValue:  math.MaxInt,
				},
				{
					Name:     "lending-cq",
					NodeType: nodeTypeCq,
					DrName:   "",
					DrValue:  0,
				},
				{
					Name:     "test-cohort",
					NodeType: nodeTypeCohort,
					DrName:   "",
					DrValue:  0,
				},
			},
		},
		"cohort has resource share": {
			usage: resources.FlavorResourceQuantities{
				{Flavor: "default", Resource: "example.com/gpu"}: 10,
			},
			clusterQueue: utiltesting.MakeClusterQueue("cq").
				Cohort("child-cohort").
				FairWeight(oneQuantity).
				ResourceGroup(
					*utiltesting.MakeFlavorQuotas("default").
						ResourceQuotaWrapper("example.com/gpu").NominalQuota("5").Append().
						Obj(),
				).Obj(),
			cohorts: []*kueue.Cohort{
				utiltesting.MakeCohort("child-cohort").FairWeight(resource.MustParse("2")).Parent("root").Obj(),
				utiltesting.MakeCohort("root").ResourceGroup(
					*utiltesting.MakeFlavorQuotas("default").
						ResourceQuotaWrapper("example.com/gpu").NominalQuota("45").Append().
						Obj(),
				).Obj(),
			},
			want: []fairSharingResult{
				{
					Name:     "cq",
					NodeType: nodeTypeCq,
					DrName:   "example.com/gpu",
					DrValue:  100, // (5 / 50) * 1000
				},
				{
					Name:     "child-cohort",
					NodeType: nodeTypeCohort,
					DrName:   "example.com/gpu",
					DrValue:  50, // (5 / 50) * 1000 / 2
				},
				{
					Name:     "root",
					NodeType: nodeTypeCohort,
					DrName:   "",
					DrValue:  0,
				},
			},
		},
		"resource share defined for resources only available at the root cohort": {
			usage: resources.FlavorResourceQuantities{
				{Flavor: "default", Resource: "example.com/gpu"}: 10,
			},
			clusterQueue: utiltesting.MakeClusterQueue("cq").
				Cohort("child-cohort").
				FairWeight(oneQuantity).
				ResourceGroup(
					*utiltesting.MakeFlavorQuotas("default").
						ResourceQuotaWrapper("example.com/gpu").NominalQuota("0").Append().
						Obj(),
				).Obj(),
			cohorts: []*kueue.Cohort{
				utiltesting.MakeCohort("child-cohort").FairWeight(resource.MustParse("2")).Parent("root").Obj(),
				utiltesting.MakeCohort("root").ResourceGroup(
					*utiltesting.MakeFlavorQuotas("default").
						ResourceQuotaWrapper("example.com/gpu").NominalQuota("50").Append().
						Obj(),
				).Obj(),
			},
			want: []fairSharingResult{
				{
					Name:     "cq",
					NodeType: nodeTypeCq,
					DrName:   "example.com/gpu",
					DrValue:  200, // (10 / 50) * 1000
				},
				{
					Name:     "child-cohort",
					NodeType: nodeTypeCohort,
					DrName:   "example.com/gpu",
					DrValue:  100, // (10 / 50) * 1000 / 2
				},
				{
					Name:     "root",
					NodeType: nodeTypeCohort,
					DrName:   "",
					DrValue:  0,
				},
			},
		},
		"resource share affected by borrowing limit": {
			// Cohort resources from view of CQ are 10, while
			// from view of child-cohort are 50. So, they get
			// different FairSharing values.
			usage: resources.FlavorResourceQuantities{
				{Flavor: "default", Resource: "example.com/gpu"}: 10,
			},
			clusterQueue: utiltesting.MakeClusterQueue("cq").
				Cohort("child-cohort").
				ResourceGroup(
					*utiltesting.MakeFlavorQuotas("default").
						ResourceQuotaWrapper("example.com/gpu").NominalQuota("0").Append().
						Obj(),
				).Obj(),
			cohorts: []*kueue.Cohort{
				utiltesting.MakeCohort("child-cohort").ResourceGroup(
					*utiltesting.MakeFlavorQuotas("default").
						ResourceQuotaWrapper("example.com/gpu").NominalQuota("0").BorrowingLimit("10").Append().
						Obj(),
				).Parent("root").Obj(),
				utiltesting.MakeCohort("root").ResourceGroup(
					*utiltesting.MakeFlavorQuotas("default").
						ResourceQuotaWrapper("example.com/gpu").NominalQuota("50").Append().
						Obj(),
				).Obj(),
			},
			want: []fairSharingResult{
				{
					Name:     "cq",
					NodeType: nodeTypeCq,
					DrName:   "example.com/gpu",
					DrValue:  1000, // (10 / 10) * 1000
				},
				{
					Name:     "child-cohort",
					NodeType: nodeTypeCohort,
					DrName:   "example.com/gpu",
					DrValue:  200, // (10 / 50) * 1000
				},
				{
					Name:     "root",
					NodeType: nodeTypeCohort,
					DrName:   "",
					DrValue:  0,
				},
			},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx, log := utiltesting.ContextWithLog(t)
			cache := New(utiltesting.NewFakeClient())
			cache.AddOrUpdateResourceFlavor(log, utiltesting.MakeResourceFlavor("default").Obj())
			cache.AddOrUpdateResourceFlavor(log, utiltesting.MakeResourceFlavor("on-demand").Obj())
			cache.AddOrUpdateResourceFlavor(log, utiltesting.MakeResourceFlavor("spot").Obj())

			_ = cache.AddClusterQueue(ctx, tc.clusterQueue)

			if tc.lendingClusterQueue != nil {
				// we create a second cluster queue to add lendable capacity to the cohort.
				_ = cache.AddClusterQueue(ctx, tc.lendingClusterQueue)
			}

			for _, cohort := range tc.cohorts {
				_ = cache.AddOrUpdateCohort(cohort)
			}

			snapshot, err := cache.Snapshot(ctx)
			if err != nil {
				t.Fatalf("unexpected error while building snapshot: %v", err)
			}
			i := 0
			for fr, v := range tc.usage {
				admission := utiltesting.MakeAdmission("cq")
				quantity := resources.ResourceQuantity(fr.Resource, v)
				admission.Assignment(fr.Resource, fr.Flavor, quantity.String())

				wl := utiltesting.MakeWorkload(fmt.Sprintf("workload-%d", i), "default-namespace").ReserveQuota(admission.Obj()).Obj()

				cache.AddOrUpdateWorkload(log, wl)
				snapshot.AddWorkload(workload.NewInfo(wl))
				i++
			}

			cacheClusterQueuesMap := cache.hm.ClusterQueues()
			cacheCohortsMap := cache.hm.Cohorts()
			gotCache := make([]fairSharingResult, 0, len(cacheClusterQueuesMap)+len(cacheCohortsMap))
			for _, cq := range cacheClusterQueuesMap {
				drVal, drName := dominantResourceShare(cq, tc.flvResQ)
				gotCache = append(gotCache, fairSharingResult{
					Name:     string(cq.Name),
					NodeType: nodeTypeCq,
					DrValue:  drVal,
					DrName:   drName,
				})
			}
			for _, cohort := range cacheCohortsMap {
				drVal, drName := dominantResourceShare(cohort, tc.flvResQ)
				gotCache = append(gotCache, fairSharingResult{
					Name:     string(cohort.Name),
					NodeType: nodeTypeCohort,
					DrValue:  drVal,
					DrName:   drName,
				})
			}
			if diff := cmp.Diff(sets.New(tc.want...), sets.New(gotCache...)); diff != "" {
				t.Errorf("dominantResourceShare cache mismatch: %s", diff)
			}

			snapshotClusterQueuesMap := snapshot.ClusterQueues()
			snapshotCohortsMap := snapshot.Cohorts()
			gotSnapshot := make([]fairSharingResult, 0, len(snapshotClusterQueuesMap)+len(snapshotCohortsMap))
			for _, cq := range snapshotClusterQueuesMap {
				drVal, drName := dominantResourceShare(cq, tc.flvResQ)
				gotSnapshot = append(gotSnapshot, fairSharingResult{
					Name:     string(cq.Name),
					NodeType: nodeTypeCq,
					DrValue:  drVal,
					DrName:   drName,
				})
			}
			for _, cohort := range snapshotCohortsMap {
				drVal, drName := dominantResourceShare(cohort, tc.flvResQ)
				gotSnapshot = append(gotSnapshot, fairSharingResult{
					Name:     string(cohort.Name),
					NodeType: nodeTypeCohort,
					DrValue:  drVal,
					DrName:   drName,
				})
			}
			if diff := cmp.Diff(sets.New(tc.want...), sets.New(gotSnapshot...)); diff != "" {
				t.Errorf("dominantResourceShare snapshot mismatch: %s", diff)
			}
		})
	}
}
