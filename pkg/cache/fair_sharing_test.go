/*
Copyright 2025 The Kubernetes Authors.

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

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/resources"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	"sigs.k8s.io/kueue/pkg/workload"
)

func TestDominantResourceShare(t *testing.T) {
	cases := map[string]struct {
		usage               resources.FlavorResourceQuantities
		clusterQueue        *kueue.ClusterQueue
		lendingClusterQueue *kueue.ClusterQueue
		flvResQ             resources.FlavorResourceQuantities
		wantDRValue         int
		wantDRName          corev1.ResourceName
	}{
		"no cohort": {
			usage: resources.FlavorResourceQuantities{
				{Flavor: "default", Resource: corev1.ResourceCPU}: 1_000,
				{Flavor: "default", Resource: "example.com/gpu"}:  2,
			},
			clusterQueue: utiltesting.MakeClusterQueue("cq").
				ResourceGroup(
					utiltesting.MakeFlavorQuotas("default").
						ResourceQuotaWrapper("cpu").NominalQuota("2000").Append().
						ResourceQuotaWrapper("example.com/gpu").NominalQuota("5").Append().
						FlavorQuotas,
				).Obj(),
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
					utiltesting.MakeFlavorQuotas("default").
						ResourceQuotaWrapper("cpu").NominalQuota("2").Append().
						ResourceQuotaWrapper("example.com/gpu").NominalQuota("5").Append().
						FlavorQuotas,
				).Obj(),
			lendingClusterQueue: utiltesting.MakeClusterQueue("lending-cq").
				Cohort("test-cohort").
				FairWeight(oneQuantity).
				ResourceGroup(
					utiltesting.MakeFlavorQuotas("default").
						ResourceQuotaWrapper("cpu").NominalQuota("8").Append().
						ResourceQuotaWrapper("example.com/gpu").NominalQuota("5").Append().
						FlavorQuotas,
				).Obj(),
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
					utiltesting.MakeFlavorQuotas("default").
						ResourceQuotaWrapper("cpu").NominalQuota("2").Append().
						ResourceQuotaWrapper("example.com/gpu").NominalQuota("5").Append().
						FlavorQuotas,
				).Obj(),
			lendingClusterQueue: utiltesting.MakeClusterQueue("lending-cq").
				Cohort("test-cohort").
				FairWeight(oneQuantity).
				ResourceGroup(
					utiltesting.MakeFlavorQuotas("default").
						ResourceQuotaWrapper("cpu").NominalQuota("8").Append().
						ResourceQuotaWrapper("example.com/gpu").NominalQuota("5").Append().
						FlavorQuotas,
				).Obj(),
			wantDRName:  "example.com/gpu",
			wantDRValue: 200, // (7-5)*1000/10
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
					utiltesting.MakeFlavorQuotas("default").
						ResourceQuotaWrapper("cpu").NominalQuota("2").Append().
						ResourceQuotaWrapper("example.com/gpu").NominalQuota("5").Append().
						FlavorQuotas,
				).Obj(),
			lendingClusterQueue: utiltesting.MakeClusterQueue("lending-cq").
				Cohort("test-cohort").
				FairWeight(oneQuantity).
				ResourceGroup(
					utiltesting.MakeFlavorQuotas("default").
						ResourceQuotaWrapper("cpu").NominalQuota("8").Append().
						ResourceQuotaWrapper("example.com/gpu").NominalQuota("5").Append().
						FlavorQuotas,
				).Obj(),
			wantDRName:  corev1.ResourceCPU,
			wantDRValue: 100, // (3-2)*1000/10
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
					utiltesting.MakeFlavorQuotas("default").
						ResourceQuotaWrapper("cpu").NominalQuota("2").Append().
						ResourceQuotaWrapper("example.com/gpu").NominalQuota("5").Append().
						FlavorQuotas,
				).Obj(),
			lendingClusterQueue: utiltesting.MakeClusterQueue("lending-cq").
				Cohort("test-cohort").
				FairWeight(oneQuantity).
				ResourceGroup(
					utiltesting.MakeFlavorQuotas("default").
						ResourceQuotaWrapper("cpu").NominalQuota("8").Append().
						ResourceQuotaWrapper("example.com/gpu").NominalQuota("5").Append().
						FlavorQuotas,
				).Obj(),
			flvResQ: resources.FlavorResourceQuantities{
				{Flavor: "default", Resource: corev1.ResourceCPU}: 4_000,
				{Flavor: "default", Resource: "example.com/gpu"}:  4,
			},
			wantDRName:  corev1.ResourceCPU,
			wantDRValue: 300, // (1+4-2)*1000/10
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
					utiltesting.MakeFlavorQuotas("default").
						ResourceQuotaWrapper("cpu").NominalQuota("2").Append().
						ResourceQuotaWrapper("example.com/gpu").NominalQuota("2").LendingLimit("0").Append().
						FlavorQuotas,
				).Obj(),
			lendingClusterQueue: utiltesting.MakeClusterQueue("lending-cq").
				Cohort("test-cohort").
				FairWeight(oneQuantity).
				ResourceGroup(
					utiltesting.MakeFlavorQuotas("default").
						ResourceQuotaWrapper("cpu").NominalQuota("8").Append().
						ResourceQuotaWrapper("example.com/gpu").NominalQuota("64").LendingLimit("0").Append().
						FlavorQuotas,
				).Obj(),
			flvResQ: resources.FlavorResourceQuantities{
				{Flavor: "default", Resource: corev1.ResourceCPU}: 4_000,
				{Flavor: "default", Resource: "example.com/gpu"}:  4,
			},
			wantDRName:  corev1.ResourceCPU,
			wantDRValue: 300, // (1+4-2)*1000/10
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
					utiltesting.MakeFlavorQuotas("on-demand").
						ResourceQuotaWrapper("cpu").NominalQuota("20").Append().
						FlavorQuotas,
					utiltesting.MakeFlavorQuotas("spot").
						ResourceQuotaWrapper("cpu").NominalQuota("80").Append().
						FlavorQuotas,
				).Obj(),
			lendingClusterQueue: utiltesting.MakeClusterQueue("lending-cq").
				Cohort("test-cohort").
				FairWeight(oneQuantity).
				ResourceGroup(
					utiltesting.MakeFlavorQuotas("default").
						ResourceQuotaWrapper("cpu").NominalQuota("100").Append().
						FlavorQuotas,
				).Obj(),
			flvResQ: resources.FlavorResourceQuantities{
				{Flavor: "on-demand", Resource: corev1.ResourceCPU}: 10_000,
			},
			wantDRName:  corev1.ResourceCPU,
			wantDRValue: 25, // ((15+10-20)+0)*1000/200 (spot under nominal)
		},
		"above nominal with integer weight": {
			usage: resources.FlavorResourceQuantities{
				{Flavor: "default", Resource: "example.com/gpu"}: 7,
			},
			clusterQueue: utiltesting.MakeClusterQueue("cq").
				Cohort("test-cohort").
				FairWeight(resource.MustParse("2")).
				ResourceGroup(
					utiltesting.MakeFlavorQuotas("default").
						ResourceQuotaWrapper("example.com/gpu").NominalQuota("5").Append().
						FlavorQuotas,
				).Obj(),
			lendingClusterQueue: utiltesting.MakeClusterQueue("lending-cq").
				Cohort("test-cohort").
				FairWeight(oneQuantity).
				ResourceGroup(
					utiltesting.MakeFlavorQuotas("default").
						ResourceQuotaWrapper("example.com/gpu").NominalQuota("5").Append().
						FlavorQuotas,
				).Obj(),
			wantDRName:  "example.com/gpu",
			wantDRValue: 100, // ((7-5)*1000/10)/2
		},
		"above nominal with decimal weight": {
			usage: resources.FlavorResourceQuantities{
				{Flavor: "default", Resource: "example.com/gpu"}: 7,
			},
			clusterQueue: utiltesting.MakeClusterQueue("cq").
				Cohort("test-cohort").
				FairWeight(resource.MustParse("0.5")).
				ResourceGroup(
					utiltesting.MakeFlavorQuotas("default").
						ResourceQuotaWrapper("example.com/gpu").NominalQuota("5").Append().
						FlavorQuotas,
				).Obj(),
			lendingClusterQueue: utiltesting.MakeClusterQueue("lending-cq").
				Cohort("test-cohort").
				FairWeight(oneQuantity).
				ResourceGroup(
					utiltesting.MakeFlavorQuotas("default").
						ResourceQuotaWrapper("example.com/gpu").NominalQuota("5").Append().
						FlavorQuotas,
				).Obj(),
			wantDRName:  "example.com/gpu",
			wantDRValue: 400, // ((7-5)*1000/10)/(1/2)
		},
		"above nominal with zero weight": {
			usage: resources.FlavorResourceQuantities{
				{Flavor: "default", Resource: "example.com/gpu"}: 7,
			},
			clusterQueue: utiltesting.MakeClusterQueue("cq").
				Cohort("test-cohort").
				FairWeight(oneQuantity).
				FairWeight(resource.MustParse("0")).
				ResourceGroup(
					utiltesting.MakeFlavorQuotas("default").
						ResourceQuotaWrapper("example.com/gpu").NominalQuota("5").Append().
						FlavorQuotas,
				).Obj(),
			lendingClusterQueue: utiltesting.MakeClusterQueue("lending-cq").
				Cohort("test-cohort").
				FairWeight(oneQuantity).
				ResourceGroup(
					utiltesting.MakeFlavorQuotas("default").
						ResourceQuotaWrapper("example.com/gpu").NominalQuota("10").Append().
						FlavorQuotas,
				).Obj(),
			wantDRValue: math.MaxInt,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)
			cache := New(utiltesting.NewFakeClient())
			cache.AddOrUpdateResourceFlavor(utiltesting.MakeResourceFlavor("default").Obj())
			cache.AddOrUpdateResourceFlavor(utiltesting.MakeResourceFlavor("on-demand").Obj())
			cache.AddOrUpdateResourceFlavor(utiltesting.MakeResourceFlavor("spot").Obj())

			_ = cache.AddClusterQueue(ctx, tc.clusterQueue)

			if tc.lendingClusterQueue != nil {
				// we create a second cluster queue to add lendable capacity to the cohort.
				_ = cache.AddClusterQueue(ctx, tc.lendingClusterQueue)
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

				cache.AddOrUpdateWorkload(wl)
				snapshot.AddWorkload(workload.NewInfo(wl))
				i++
			}

			drVal, drNameCache := dominantResourceShare(cache.hm.ClusterQueues["cq"], tc.flvResQ)
			if drVal != tc.wantDRValue {
				t.Errorf("cache.DominantResourceShare(_) returned value %d, want %d", drVal, tc.wantDRValue)
			}
			if drNameCache != tc.wantDRName {
				t.Errorf("cache.DominantResourceShare(_) returned resource %s, want %s", drNameCache, tc.wantDRName)
			}

			drValSnap, drNameSnap := snapshot.ClusterQueues["cq"].DominantResourceShareWith(tc.flvResQ)
			if drValSnap != tc.wantDRValue {
				t.Errorf("snapshot.DominantResourceShare(_) returned value %d, want %d", drValSnap, tc.wantDRValue)
			}
			if drNameSnap != tc.wantDRName {
				t.Errorf("snapshot.DominantResourceShare(_) returned resource %s, want %s", drNameSnap, tc.wantDRName)
			}
		})
	}
}
