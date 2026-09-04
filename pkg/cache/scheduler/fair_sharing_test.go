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

package scheduler

import (
	"fmt"
	"math"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/util/sets"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/resources"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/pkg/workload"
)

func TestDominantResourceShare(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	type nodeType bool
	var (
		nodeTypeCq     nodeType = false
		nodeTypeCohort nodeType = true
	)

	type fairSharingResult struct {
		Name      string
		NodeType  nodeType
		DrValue   int64
		DrName    corev1.ResourceName
		Borrowing bool
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
				{Flavor: "default", Resource: corev1.ResourceCPU}: resources.NewAmount(1_000),
				{Flavor: "default", Resource: "example.com/gpu"}:  resources.NewAmount(2),
			},
			clusterQueue: utiltestingapi.MakeClusterQueue("cq").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("default").
						ResourceQuotaWrapper("cpu").NominalQuota("2000").Append().
						ResourceQuotaWrapper("example.com/gpu").NominalQuota("5").Append().
						Obj(),
				).Obj(),
			want: []fairSharingResult{
				{
					Name:      "cq",
					NodeType:  nodeTypeCq,
					DrName:    "",
					DrValue:   0,
					Borrowing: false,
				},
			},
		},
		"usage below nominal": {
			usage: resources.FlavorResourceQuantities{
				{Flavor: "default", Resource: corev1.ResourceCPU}: resources.NewAmount(1_000),
				{Flavor: "default", Resource: "example.com/gpu"}:  resources.NewAmount(2),
			},
			clusterQueue: utiltestingapi.MakeClusterQueue("cq").
				Cohort("test-cohort").
				FairWeight(resource.MustParse("1")).
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("default").
						ResourceQuotaWrapper("cpu").NominalQuota("2").Append().
						ResourceQuotaWrapper("example.com/gpu").NominalQuota("5").Append().
						Obj(),
				).Obj(),
			lendingClusterQueue: utiltestingapi.MakeClusterQueue("lending-cq").
				Cohort("test-cohort").
				FairWeight(resource.MustParse("1")).
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("default").
						ResourceQuotaWrapper("cpu").NominalQuota("8").Append().
						ResourceQuotaWrapper("example.com/gpu").NominalQuota("5").Append().
						Obj(),
				).Obj(),
			want: []fairSharingResult{
				{
					Name:      "cq",
					NodeType:  nodeTypeCq,
					DrName:    "",
					DrValue:   0,
					Borrowing: false,
				},
				{
					Name:      "lending-cq",
					NodeType:  nodeTypeCq,
					DrName:    "",
					DrValue:   0,
					Borrowing: false,
				},
				{
					Name:      "test-cohort",
					NodeType:  nodeTypeCohort,
					DrName:    "",
					DrValue:   0,
					Borrowing: false,
				},
			},
		},
		"usage above nominal": {
			usage: resources.FlavorResourceQuantities{
				{Flavor: "default", Resource: corev1.ResourceCPU}: resources.NewAmount(3_000),
				{Flavor: "default", Resource: "example.com/gpu"}:  resources.NewAmount(7),
			},
			clusterQueue: utiltestingapi.MakeClusterQueue("cq").
				Cohort("test-cohort").
				FairWeight(resource.MustParse("1")).
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("default").
						ResourceQuotaWrapper("cpu").NominalQuota("2").Append().
						ResourceQuotaWrapper("example.com/gpu").NominalQuota("5").Append().
						Obj(),
				).Obj(),
			lendingClusterQueue: utiltestingapi.MakeClusterQueue("lending-cq").
				Cohort("test-cohort").
				FairWeight(resource.MustParse("1")).
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("default").
						ResourceQuotaWrapper("cpu").NominalQuota("8").Append().
						ResourceQuotaWrapper("example.com/gpu").NominalQuota("5").Append().
						Obj(),
				).Obj(),
			want: []fairSharingResult{
				{
					Name:      "cq",
					NodeType:  nodeTypeCq,
					DrName:    "example.com/gpu",
					DrValue:   200, // (7-5)*1000/10
					Borrowing: true,
				},
				{
					Name:      "lending-cq",
					NodeType:  nodeTypeCq,
					DrName:    "",
					DrValue:   0,
					Borrowing: false,
				},
				{
					Name:      "test-cohort",
					NodeType:  nodeTypeCohort,
					DrName:    "",
					DrValue:   0,
					Borrowing: false,
				},
			},
		},
		"usage slightly above nominal in a cohort with large quotas": {
			usage: resources.FlavorResourceQuantities{
				{Flavor: "default", Resource: "example.com/gpu"}: resources.NewAmount(501),
			},
			clusterQueue: utiltestingapi.MakeClusterQueue("cq").
				Cohort("test-cohort").
				FairWeight(resource.MustParse("1")).
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("default").
						ResourceQuotaWrapper("example.com/gpu").NominalQuota("500").Append().
						Obj(),
				).Obj(),
			lendingClusterQueue: utiltestingapi.MakeClusterQueue("lending-cq").
				Cohort("test-cohort").
				FairWeight(resource.MustParse("300")).
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("default").
						ResourceQuotaWrapper("example.com/gpu").NominalQuota("1000").Append().
						Obj(),
				).Obj(),
			want: []fairSharingResult{
				{
					Name:      "cq",
					NodeType:  nodeTypeCq,
					DrName:    "example.com/gpu",
					DrValue:   1,
					Borrowing: true,
				},
				{
					Name:      "lending-cq",
					NodeType:  nodeTypeCq,
					DrName:    "",
					DrValue:   0,
					Borrowing: false,
				},
				{
					Name:      "test-cohort",
					NodeType:  nodeTypeCohort,
					DrName:    "",
					DrValue:   0,
					Borrowing: false,
				},
			},
		},
		"usage way above nominal in a cohort with large quotas and weights": {
			usage: resources.FlavorResourceQuantities{
				{Flavor: "default", Resource: "example.com/gpu"}: resources.NewAmount(800),
			},
			clusterQueue: utiltestingapi.MakeClusterQueue("cq").
				Cohort("test-cohort").
				FairWeight(resource.MustParse("300")).
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("default").
						ResourceQuotaWrapper("example.com/gpu").NominalQuota("500").Append().
						Obj(),
				).Obj(),
			lendingClusterQueue: utiltestingapi.MakeClusterQueue("lending-cq").
				Cohort("test-cohort").
				FairWeight(resource.MustParse("300")).
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("default").
						ResourceQuotaWrapper("example.com/gpu").NominalQuota("1000").Append().
						Obj(),
				).Obj(),
			want: []fairSharingResult{
				{
					Name:      "cq",
					NodeType:  nodeTypeCq,
					DrName:    "example.com/gpu",
					DrValue:   1,
					Borrowing: true,
				},
				{
					Name:      "lending-cq",
					NodeType:  nodeTypeCq,
					DrName:    "",
					DrValue:   0,
					Borrowing: false,
				},
				{
					Name:      "test-cohort",
					NodeType:  nodeTypeCohort,
					DrName:    "",
					DrValue:   0,
					Borrowing: false,
				},
			},
		},
		"one resource above nominal": {
			usage: resources.FlavorResourceQuantities{
				{Flavor: "default", Resource: corev1.ResourceCPU}: resources.NewAmount(3_000),
				{Flavor: "default", Resource: "example.com/gpu"}:  resources.NewAmount(3),
			},
			clusterQueue: utiltestingapi.MakeClusterQueue("cq").
				Cohort("test-cohort").
				FairWeight(resource.MustParse("1")).
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("default").
						ResourceQuotaWrapper("cpu").NominalQuota("2").Append().
						ResourceQuotaWrapper("example.com/gpu").NominalQuota("5").Append().
						Obj(),
				).Obj(),
			lendingClusterQueue: utiltestingapi.MakeClusterQueue("lending-cq").
				Cohort("test-cohort").
				FairWeight(resource.MustParse("1")).
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("default").
						ResourceQuotaWrapper("cpu").NominalQuota("8").Append().
						ResourceQuotaWrapper("example.com/gpu").NominalQuota("5").Append().
						Obj(),
				).Obj(),
			want: []fairSharingResult{
				{
					Name:      "cq",
					NodeType:  nodeTypeCq,
					DrName:    corev1.ResourceCPU,
					DrValue:   100, // (3-2)*1000/10
					Borrowing: true,
				},
				{
					Name:      "lending-cq",
					NodeType:  nodeTypeCq,
					DrName:    "",
					DrValue:   0,
					Borrowing: false,
				},
				{
					Name:      "test-cohort",
					NodeType:  nodeTypeCohort,
					DrName:    "",
					DrValue:   0,
					Borrowing: false,
				},
			},
		},
		"usage with workload above nominal": {
			usage: resources.FlavorResourceQuantities{
				{Flavor: "default", Resource: corev1.ResourceCPU}: resources.NewAmount(1_000),
				{Flavor: "default", Resource: "example.com/gpu"}:  resources.NewAmount(2),
			},
			clusterQueue: utiltestingapi.MakeClusterQueue("cq").
				Cohort("test-cohort").
				FairWeight(resource.MustParse("1")).
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("default").
						ResourceQuotaWrapper("cpu").NominalQuota("2").Append().
						ResourceQuotaWrapper("example.com/gpu").NominalQuota("5").Append().
						Obj(),
				).Obj(),
			lendingClusterQueue: utiltestingapi.MakeClusterQueue("lending-cq").
				Cohort("test-cohort").
				FairWeight(resource.MustParse("1")).
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("default").
						ResourceQuotaWrapper("cpu").NominalQuota("8").Append().
						ResourceQuotaWrapper("example.com/gpu").NominalQuota("5").Append().
						Obj(),
				).Obj(),
			flvResQ: resources.FlavorResourceQuantities{
				{Flavor: "default", Resource: corev1.ResourceCPU}: resources.NewAmount(4_000),
				{Flavor: "default", Resource: "example.com/gpu"}:  resources.NewAmount(4),
			},
			want: []fairSharingResult{
				{
					Name:      "cq",
					NodeType:  nodeTypeCq,
					DrName:    corev1.ResourceCPU,
					DrValue:   300, // (1+4-2)*1000/10
					Borrowing: true,
				},
				{
					Name:      "lending-cq",
					NodeType:  nodeTypeCq,
					DrName:    "",
					DrValue:   0,
					Borrowing: false,
				},
				{
					Name:      "test-cohort",
					NodeType:  nodeTypeCohort,
					DrName:    "",
					DrValue:   0,
					Borrowing: false,
				},
			},
		},
		"A resource with zero lendable": {
			usage: resources.FlavorResourceQuantities{
				{Flavor: "default", Resource: corev1.ResourceCPU}: resources.NewAmount(1_000),
				{Flavor: "default", Resource: "example.com/gpu"}:  resources.NewAmount(1),
			},
			clusterQueue: utiltestingapi.MakeClusterQueue("cq").
				Cohort("test-cohort").
				FairWeight(resource.MustParse("1")).
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("default").
						ResourceQuotaWrapper("cpu").NominalQuota("2").Append().
						ResourceQuotaWrapper("example.com/gpu").NominalQuota("2").LendingLimit("0").Append().
						Obj(),
				).Obj(),
			lendingClusterQueue: utiltestingapi.MakeClusterQueue("lending-cq").
				Cohort("test-cohort").
				FairWeight(resource.MustParse("1")).
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("default").
						ResourceQuotaWrapper("cpu").NominalQuota("8").Append().
						ResourceQuotaWrapper("example.com/gpu").NominalQuota("64").LendingLimit("0").Append().
						Obj(),
				).Obj(),
			flvResQ: resources.FlavorResourceQuantities{
				{Flavor: "default", Resource: corev1.ResourceCPU}: resources.NewAmount(4_000),
				{Flavor: "default", Resource: "example.com/gpu"}:  resources.NewAmount(4),
			},
			want: []fairSharingResult{
				{
					Name:      "cq",
					NodeType:  nodeTypeCq,
					DrName:    corev1.ResourceCPU,
					DrValue:   300, // (1+4-2)*1000/10
					Borrowing: true,
				},
				{
					Name:      "lending-cq",
					NodeType:  nodeTypeCq,
					DrName:    "",
					DrValue:   0,
					Borrowing: false,
				},
				{
					Name:      "test-cohort",
					NodeType:  nodeTypeCohort,
					DrName:    "",
					DrValue:   0,
					Borrowing: false,
				},
			},
		},
		"multiple flavors": {
			usage: resources.FlavorResourceQuantities{
				{Flavor: "on-demand", Resource: corev1.ResourceCPU}: resources.NewAmount(15_000),
				{Flavor: "spot", Resource: corev1.ResourceCPU}:      resources.NewAmount(5_000),
			},
			clusterQueue: utiltestingapi.MakeClusterQueue("cq").
				Cohort("test-cohort").
				FairWeight(resource.MustParse("1")).
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("on-demand").
						ResourceQuotaWrapper("cpu").NominalQuota("20").Append().
						Obj(),
					*utiltestingapi.MakeFlavorQuotas("spot").
						ResourceQuotaWrapper("cpu").NominalQuota("80").Append().
						Obj(),
				).Obj(),
			lendingClusterQueue: utiltestingapi.MakeClusterQueue("lending-cq").
				Cohort("test-cohort").
				FairWeight(resource.MustParse("1")).
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("default").
						ResourceQuotaWrapper("cpu").NominalQuota("100").Append().
						Obj(),
				).Obj(),
			flvResQ: resources.FlavorResourceQuantities{
				{Flavor: "on-demand", Resource: corev1.ResourceCPU}: resources.NewAmount(10_000),
			},
			want: []fairSharingResult{
				{
					Name:      "cq",
					NodeType:  nodeTypeCq,
					DrName:    corev1.ResourceCPU,
					DrValue:   25, // ((15+10-20)+0)*1000/200 (spot under nominal)
					Borrowing: true,
				},
				{
					Name:      "lending-cq",
					NodeType:  nodeTypeCq,
					DrName:    "",
					DrValue:   0,
					Borrowing: false,
				},
				{
					Name:      "test-cohort",
					NodeType:  nodeTypeCohort,
					DrName:    "",
					DrValue:   0,
					Borrowing: false,
				},
			},
		},
		"above nominal with integer weight": {
			usage: resources.FlavorResourceQuantities{
				{Flavor: "default", Resource: "example.com/gpu"}: resources.NewAmount(7),
			},
			clusterQueue: utiltestingapi.MakeClusterQueue("cq").
				Cohort("test-cohort").
				FairWeight(resource.MustParse("2")).
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("default").
						ResourceQuotaWrapper("example.com/gpu").NominalQuota("5").Append().
						Obj(),
				).Obj(),
			lendingClusterQueue: utiltestingapi.MakeClusterQueue("lending-cq").
				Cohort("test-cohort").
				FairWeight(resource.MustParse("1")).
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("default").
						ResourceQuotaWrapper("example.com/gpu").NominalQuota("5").Append().
						Obj(),
				).Obj(),
			want: []fairSharingResult{
				{
					Name:      "cq",
					NodeType:  nodeTypeCq,
					DrName:    "example.com/gpu",
					DrValue:   100, // ((7-5)*1000/10)/2
					Borrowing: true,
				},
				{
					Name:      "lending-cq",
					NodeType:  nodeTypeCq,
					DrName:    "",
					DrValue:   0,
					Borrowing: false,
				},
				{
					Name:      "test-cohort",
					NodeType:  nodeTypeCohort,
					DrName:    "",
					DrValue:   0,
					Borrowing: false,
				},
			},
		},
		"above nominal with decimal weight": {
			usage: resources.FlavorResourceQuantities{
				{Flavor: "default", Resource: "example.com/gpu"}: resources.NewAmount(7),
			},
			clusterQueue: utiltestingapi.MakeClusterQueue("cq").
				Cohort("test-cohort").
				FairWeight(resource.MustParse("0.5")).
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("default").
						ResourceQuotaWrapper("example.com/gpu").NominalQuota("5").Append().
						Obj(),
				).Obj(),
			lendingClusterQueue: utiltestingapi.MakeClusterQueue("lending-cq").
				Cohort("test-cohort").
				FairWeight(resource.MustParse("1")).
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("default").
						ResourceQuotaWrapper("example.com/gpu").NominalQuota("5").Append().
						Obj(),
				).Obj(),
			want: []fairSharingResult{
				{
					Name:      "cq",
					NodeType:  nodeTypeCq,
					DrName:    "example.com/gpu",
					DrValue:   400, // ((7-5)*1000/10)/(1/2)
					Borrowing: true,
				},
				{
					Name:      "lending-cq",
					NodeType:  nodeTypeCq,
					DrName:    "",
					DrValue:   0,
					Borrowing: false,
				},
				{
					Name:      "test-cohort",
					NodeType:  nodeTypeCohort,
					DrName:    "",
					DrValue:   0,
					Borrowing: false,
				},
			},
		},
		"above nominal with zero weight": {
			usage: resources.FlavorResourceQuantities{
				{Flavor: "default", Resource: "example.com/gpu"}: resources.NewAmount(7),
			},
			clusterQueue: utiltestingapi.MakeClusterQueue("cq").
				Cohort("test-cohort").
				FairWeight(resource.MustParse("0")).
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("default").
						ResourceQuotaWrapper("example.com/gpu").NominalQuota("5").Append().
						Obj(),
				).Obj(),
			lendingClusterQueue: utiltestingapi.MakeClusterQueue("lending-cq").
				Cohort("test-cohort").
				FairWeight(resource.MustParse("1")).
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("default").
						ResourceQuotaWrapper("example.com/gpu").NominalQuota("10").Append().
						Obj(),
				).Obj(),
			want: []fairSharingResult{
				{
					Name:      "cq",
					NodeType:  nodeTypeCq,
					DrName:    "example.com/gpu",
					DrValue:   math.MaxInt,
					Borrowing: true,
				},
				{
					Name:      "lending-cq",
					NodeType:  nodeTypeCq,
					DrName:    "",
					DrValue:   0,
					Borrowing: false,
				},
				{
					Name:      "test-cohort",
					NodeType:  nodeTypeCohort,
					DrName:    "",
					DrValue:   0,
					Borrowing: false,
				},
			},
		},
		"cohort has resource share": {
			usage: resources.FlavorResourceQuantities{
				{Flavor: "default", Resource: "example.com/gpu"}: resources.NewAmount(10),
			},
			clusterQueue: utiltestingapi.MakeClusterQueue("cq").
				Cohort("child-cohort").
				FairWeight(resource.MustParse("1")).
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("default").
						ResourceQuotaWrapper("example.com/gpu").NominalQuota("5").Append().
						Obj(),
				).Obj(),
			cohorts: []*kueue.Cohort{
				utiltestingapi.MakeCohort("child-cohort").FairWeight(resource.MustParse("2")).Parent("root").Obj(),
				utiltestingapi.MakeCohort("root").ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("default").
						ResourceQuotaWrapper("example.com/gpu").NominalQuota("45").Append().
						Obj(),
				).Obj(),
			},
			want: []fairSharingResult{
				{
					Name:      "cq",
					NodeType:  nodeTypeCq,
					DrName:    "example.com/gpu",
					DrValue:   100, // (5 / 50) * 1000
					Borrowing: true,
				},
				{
					Name:      "child-cohort",
					NodeType:  nodeTypeCohort,
					DrName:    "example.com/gpu",
					DrValue:   50, // (5 / 50) * 1000 / 2
					Borrowing: true,
				},
				{
					Name:      "root",
					NodeType:  nodeTypeCohort,
					DrName:    "",
					DrValue:   0,
					Borrowing: false,
				},
			},
		},
		"resource share defined for resources only available at the root cohort": {
			usage: resources.FlavorResourceQuantities{
				{Flavor: "default", Resource: "example.com/gpu"}: resources.NewAmount(10),
			},
			clusterQueue: utiltestingapi.MakeClusterQueue("cq").
				Cohort("child-cohort").
				FairWeight(resource.MustParse("1")).
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("default").
						ResourceQuotaWrapper("example.com/gpu").NominalQuota("0").Append().
						Obj(),
				).Obj(),
			cohorts: []*kueue.Cohort{
				utiltestingapi.MakeCohort("child-cohort").FairWeight(resource.MustParse("2")).Parent("root").Obj(),
				utiltestingapi.MakeCohort("root").ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("default").
						ResourceQuotaWrapper("example.com/gpu").NominalQuota("50").Append().
						Obj(),
				).Obj(),
			},
			want: []fairSharingResult{
				{
					Name:      "cq",
					NodeType:  nodeTypeCq,
					DrName:    "example.com/gpu",
					DrValue:   200, // (10 / 50) * 1000
					Borrowing: true,
				},
				{
					Name:      "child-cohort",
					NodeType:  nodeTypeCohort,
					DrName:    "example.com/gpu",
					DrValue:   100, // (10 / 50) * 1000 / 2
					Borrowing: true,
				},
				{
					Name:      "root",
					NodeType:  nodeTypeCohort,
					DrName:    "",
					DrValue:   0,
					Borrowing: false,
				},
			},
		},
		"resource share affected by borrowing limit": {
			// Cohort resources from view of CQ are 10, while
			// from view of child-cohort are 50. So, they get
			// different FairSharing values.
			usage: resources.FlavorResourceQuantities{
				{Flavor: "default", Resource: "example.com/gpu"}: resources.NewAmount(10),
			},
			clusterQueue: utiltestingapi.MakeClusterQueue("cq").
				Cohort("child-cohort").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("default").
						ResourceQuotaWrapper("example.com/gpu").NominalQuota("0").Append().
						Obj(),
				).Obj(),
			cohorts: []*kueue.Cohort{
				utiltestingapi.MakeCohort("child-cohort").ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("default").
						ResourceQuotaWrapper("example.com/gpu").NominalQuota("0").BorrowingLimit("10").Append().
						Obj(),
				).Parent("root").Obj(),
				utiltestingapi.MakeCohort("root").ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("default").
						ResourceQuotaWrapper("example.com/gpu").NominalQuota("50").Append().
						Obj(),
				).Obj(),
			},
			want: []fairSharingResult{
				{
					Name:      "cq",
					NodeType:  nodeTypeCq,
					DrName:    "example.com/gpu",
					DrValue:   1000, // (10 / 10) * 1000
					Borrowing: true,
				},
				{
					Name:      "child-cohort",
					NodeType:  nodeTypeCohort,
					DrName:    "example.com/gpu",
					DrValue:   200, // (10 / 50) * 1000
					Borrowing: true,
				},
				{
					Name:      "root",
					NodeType:  nodeTypeCohort,
					DrName:    "",
					DrValue:   0,
					Borrowing: false,
				},
			},
		},
		// When the lending CQ holds an "exabyte-scale" quota (1E CPU), AmountFromQuantity
		// is exact past int64. calculateLendable then aggregates potentialAvailable
		// and lendable["cpu"] carries the whole of it.
		// b.PerThousandOf(lr) divides the exact operands and evaluates to a tiny
		// positive finite number; math.Ceil rounds it up to 1. This test pins that
		// behaviour and guards against NaN/Inf regressions.
		"borrowing against unlimited lendable capacity (exabyte-scale quota)": {
			usage: resources.FlavorResourceQuantities{
				{Flavor: "default", Resource: corev1.ResourceCPU}: resources.NewAmount(1_000), // 1 CPU
			},
			clusterQueue: utiltestingapi.MakeClusterQueue("cq").
				Cohort("test-cohort").
				FairWeight(resource.MustParse("1")).
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("default").
						ResourceQuotaWrapper("cpu").NominalQuota("0").Append().
						Obj(),
				).Obj(),
			lendingClusterQueue: utiltestingapi.MakeClusterQueue("lending-cq").
				Cohort("test-cohort").
				FairWeight(resource.MustParse("1")).
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("default").
						// "1E" CPU is past int64 in milliCPU and is charged as the number it is.
						ResourceQuotaWrapper("cpu").NominalQuota("1E").Append().
						Obj(),
				).Obj(),
			want: []fairSharingResult{
				{
					Name:     "cq",
					NodeType: nodeTypeCq,
					// ratio = 1000*1000/10^21 = 1e-15, the whole 1E quota being lendable;
					// math.Ceil → 1.
					DrValue:   1,
					DrName:    corev1.ResourceCPU,
					Borrowing: true,
				},
				{
					Name:      "lending-cq",
					NodeType:  nodeTypeCq,
					DrValue:   0,
					DrName:    "",
					Borrowing: false,
				},
				{
					Name:      "test-cohort",
					NodeType:  nodeTypeCohort,
					DrValue:   0,
					DrName:    "",
					Borrowing: false,
				},
			},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx, log := utiltesting.ContextWithLog(t)
			cache := New(utiltesting.NewFakeClient())
			cache.AddOrUpdateResourceFlavor(log, utiltestingapi.MakeResourceFlavor("default").Obj())
			cache.AddOrUpdateResourceFlavor(log, utiltestingapi.MakeResourceFlavor("on-demand").Obj())
			cache.AddOrUpdateResourceFlavor(log, utiltestingapi.MakeResourceFlavor("spot").Obj())

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
				admission := utiltestingapi.MakeAdmission("cq")
				quantity := quantityForTest(fr.Resource, v)
				admission.PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
					Assignment(fr.Resource, fr.Flavor, quantity.String()).
					Obj())

				wl := utiltestingapi.MakeWorkload(fmt.Sprintf("workload-%d", i), "default-namespace").ReserveQuotaAt(admission.Obj(), now).Obj()

				cache.AddOrUpdateWorkload(log, wl)
				snapshot.AddWorkload(workload.NewInfo(wl))
				i++
			}

			cacheClusterQueuesMap := cache.hm.ClusterQueues()
			cacheCohortsMap := cache.hm.Cohorts()
			gotCache := make([]fairSharingResult, 0, len(cacheClusterQueuesMap)+len(cacheCohortsMap))
			for _, cq := range cacheClusterQueuesMap {
				drs := dominantResourceShare(cq, tc.flvResQ)
				drVal, drName := drs.roundedWeightedShare()
				gotCache = append(gotCache, fairSharingResult{
					Name:      string(cq.Name),
					NodeType:  nodeTypeCq,
					DrValue:   drVal,
					DrName:    drName,
					Borrowing: drs.IsBorrowing(),
				})
			}
			for _, cohort := range cacheCohortsMap {
				drs := dominantResourceShare(cohort, tc.flvResQ)
				drVal, drName := drs.roundedWeightedShare()
				gotCache = append(gotCache, fairSharingResult{
					Name:      string(cohort.Name),
					NodeType:  nodeTypeCohort,
					DrValue:   drVal,
					DrName:    drName,
					Borrowing: drs.IsBorrowing(),
				})
			}
			if diff := cmp.Diff(sets.New(tc.want...), sets.New(gotCache...)); diff != "" {
				t.Errorf("dominantResourceShare cache mismatch: %s", diff)
			}

			snapshotClusterQueuesMap := snapshot.ClusterQueues()
			snapshotCohortsMap := snapshot.Cohorts()
			gotSnapshot := make([]fairSharingResult, 0, len(snapshotClusterQueuesMap)+len(snapshotCohortsMap))
			for _, cq := range snapshotClusterQueuesMap {
				drs := dominantResourceShare(cq, tc.flvResQ)
				drVal, drName := drs.roundedWeightedShare()
				gotSnapshot = append(gotSnapshot, fairSharingResult{
					Name:      string(cq.Name),
					NodeType:  nodeTypeCq,
					DrValue:   drVal,
					DrName:    drName,
					Borrowing: drs.IsBorrowing(),
				})
			}
			for _, cohort := range snapshotCohortsMap {
				drs := dominantResourceShare(cohort, tc.flvResQ)
				drVal, drName := drs.roundedWeightedShare()
				gotSnapshot = append(gotSnapshot, fairSharingResult{
					Name:      string(cohort.Name),
					NodeType:  nodeTypeCohort,
					DrValue:   drVal,
					DrName:    drName,
					Borrowing: drs.IsBorrowing(),
				})
			}
			if diff := cmp.Diff(sets.New(tc.want...), sets.New(gotSnapshot...)); diff != "" {
				t.Errorf("dominantResourceShare snapshot mismatch: %s", diff)
			}
		})
	}
}

func TestIsBorrowingOn(t *testing.T) {
	cpuDefault := resources.FlavorResource{Flavor: "default", Resource: corev1.ResourceCPU}
	gpuDefault := resources.FlavorResource{Flavor: "default", Resource: "example.com/gpu"}

	// CQ "cq" quota: cpu=2, gpu=5. Lending CQ adds: cpu=8, gpu=5.
	// Cohort subtreeQuota: cpu=10, gpu=10.
	cases := map[string]struct {
		usage                    resources.FlavorResourceQuantities
		requestedFRs             resources.FlavorResourceQuantities
		wantBorrowingOnRequested bool
		wantBorrowing            bool
	}{
		"borrows on requested flavor": {
			usage:                    resources.FlavorResourceQuantities{cpuDefault: resources.NewAmount(3_000)},
			requestedFRs:             resources.FlavorResourceQuantities{cpuDefault: resources.NewAmount(1_000)},
			wantBorrowingOnRequested: true,
			wantBorrowing:            true,
		},
		"borrows on unrequested flavor only": {
			usage:                    resources.FlavorResourceQuantities{cpuDefault: resources.NewAmount(1_000), gpuDefault: resources.NewAmount(7)},
			requestedFRs:             resources.FlavorResourceQuantities{cpuDefault: resources.NewAmount(1_000)},
			wantBorrowingOnRequested: false,
			wantBorrowing:            true,
		},
		"borrows on both, requests one": {
			usage:                    resources.FlavorResourceQuantities{cpuDefault: resources.NewAmount(3_000), gpuDefault: resources.NewAmount(7)},
			requestedFRs:             resources.FlavorResourceQuantities{gpuDefault: resources.NewAmount(1)},
			wantBorrowingOnRequested: true,
			wantBorrowing:            true,
		},
		"no borrowing": {
			usage:                    resources.FlavorResourceQuantities{cpuDefault: resources.NewAmount(1_000), gpuDefault: resources.NewAmount(2)},
			requestedFRs:             resources.FlavorResourceQuantities{cpuDefault: resources.NewAmount(1_000)},
			wantBorrowingOnRequested: false,
			wantBorrowing:            false,
		},
		"nil requestedFRs": {
			usage:                    resources.FlavorResourceQuantities{cpuDefault: resources.NewAmount(3_000)},
			requestedFRs:             nil,
			wantBorrowingOnRequested: false,
			wantBorrowing:            true,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			now := time.Now().Truncate(time.Second)
			cq := utiltestingapi.MakeClusterQueue("cq").
				Cohort("cohort").
				FairWeight(resource.MustParse("1")).
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("default").
						ResourceQuotaWrapper("cpu").NominalQuota("2").Append().
						ResourceQuotaWrapper("example.com/gpu").NominalQuota("5").Append().
						Obj(),
				).Obj()
			lendingCQ := utiltestingapi.MakeClusterQueue("lending-cq").
				Cohort("cohort").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("default").
						ResourceQuotaWrapper("cpu").NominalQuota("8").Append().
						ResourceQuotaWrapper("example.com/gpu").NominalQuota("5").Append().
						Obj(),
				).Obj()

			ctx, log := utiltesting.ContextWithLog(t)
			cache := New(utiltesting.NewFakeClient())
			cache.AddOrUpdateResourceFlavor(log, utiltestingapi.MakeResourceFlavor("default").Obj())
			_ = cache.AddClusterQueue(ctx, cq)
			_ = cache.AddClusterQueue(ctx, lendingCQ)
			snapshot, err := cache.Snapshot(ctx)
			if err != nil {
				t.Fatalf("snapshot: %v", err)
			}
			i := 0
			for fr, v := range tc.usage {
				admission := utiltestingapi.MakeAdmission("cq")
				quantity := quantityForTest(fr.Resource, v)
				admission.PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
					Assignment(fr.Resource, fr.Flavor, quantity.String()).
					Obj())
				wl := utiltestingapi.MakeWorkload(fmt.Sprintf("wl-%d", i), "default-namespace").
					ReserveQuotaAt(admission.Obj(), now).Obj()
				cache.AddOrUpdateWorkload(log, wl)
				snapshot.AddWorkload(workload.NewInfo(wl))
				i++
			}

			snapshotCQ := snapshot.ClusterQueues()["cq"]
			drs := dominantResourceShare(snapshotCQ, nil)
			if got := drs.IsBorrowing(); got != tc.wantBorrowing {
				t.Errorf("IsBorrowing() = %v, want %v", got, tc.wantBorrowing)
			}
			if got := drs.IsBorrowingOn(tc.requestedFRs); got != tc.wantBorrowingOnRequested {
				t.Errorf("IsBorrowingOn() = %v, want %v", got, tc.wantBorrowingOnRequested)
			}
		})
	}
}

func TestZeroWeightBorrows(t *testing.T) {
	cases := map[string]struct {
		drs  DRS
		want bool
	}{
		"zero weight and borrowing returns true": {
			drs:  DRS{fairWeight: 0, unweightedRatio: 100, borrowing: true},
			want: true,
		},
		"zero weight and not borrowing returns false": {
			drs:  DRS{fairWeight: 0, unweightedRatio: 0},
			want: false,
		},
		"non-zero weight and borrowing returns false": {
			drs:  DRS{fairWeight: 1, unweightedRatio: 100, borrowing: true},
			want: false,
		},
		// A borrower whose share rounds to zero is still a borrower, which
		// reading the ratio alone could not tell from not borrowing at all.
		"zero weight borrowing a share too small to show returns true": {
			drs:  DRS{fairWeight: 0, unweightedRatio: 0, borrowing: true},
			want: true,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := tc.drs.ZeroWeightBorrows(); got != tc.want {
				t.Errorf("ZeroWeightBorrows() = %v, want %v", got, tc.want)
			}
		})
	}
}

func TestPreciseWeightedShareSerialized(t *testing.T) {
	cases := map[string]struct {
		drs  DRS
		want string
	}{
		"zero weight returning Inf": {
			drs:  DRS{fairWeight: 0, unweightedRatio: 100},
			want: "+Inf",
		},
		"zero unweighted ratio returns 0": {
			drs:  DRS{fairWeight: 1, unweightedRatio: 0},
			want: "0",
		},
		"regular integer division": {
			drs:  DRS{fairWeight: 2, unweightedRatio: 400},
			want: "200",
		},
		"decimal division": {
			drs:  DRS{fairWeight: 4, unweightedRatio: 10},
			want: "2.5",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := tc.drs.PreciseWeightedShareSerialized()
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("Unexpected string output (-want,+got):\n%s", diff)
			}
		})
	}
}

// quantityForTest is the API boundary conversion the cache uses.
func quantityForTest(name corev1.ResourceName, a resources.Amount) resource.Quantity {
	q, _ := resources.NewResourceFormatter().AmountQuantity(name, a)
	return q
}

// The ratio survives the exact division, then the weight is applied to it. A
// borrower whose ratio was taken and comes out of the second step at zero
// reports what a node below its nominal quota reports.
func TestWeightedShareDoesNotUnderflowBorrowerToZero(t *testing.T) {
	cases := map[string]DRS{
		"a share too small to survive the weight": {
			borrowing: true, ratioDefined: true,
			unweightedRatio: math.SmallestNonzeroFloat64, fairWeight: 2,
		},
		"a weight past float64": {
			borrowing: true, ratioDefined: true,
			unweightedRatio: 1, fairWeight: math.Inf(1),
		},
	}

	for name, drs := range cases {
		t.Run(name, func(t *testing.T) {
			if raw := drs.unweightedRatio / drs.fairWeight; raw != 0 {
				t.Fatalf("the fixture no longer divides to zero: %v", raw)
			}
			if got := drs.PreciseWeightedShare(); got <= 0 {
				t.Errorf("PreciseWeightedShare() = %v for a borrower, want more than zero", got)
			}
		})
	}
}

// A borrower none of whose resources had a positive lendable amount never
// reached a division, so its zero is an absent ratio rather than one that
// underflowed. What such a node's share should be is a fair-sharing policy
// question, and this change deliberately leaves it where it is rather than
// deciding it through the underflow correction.
func TestWeightedShareLeavesAbsentRatioAlone(t *testing.T) {
	drs := DRS{borrowing: true, unweightedRatio: 0, fairWeight: defaultWeight}
	if drs.ratioDefined {
		t.Fatal("the fixture defines a ratio, so it does not test the absent one")
	}
	if got := drs.PreciseWeightedShare(); got != 0 {
		t.Errorf("PreciseWeightedShare() = %v, want 0", got)
	}
	if got, _ := drs.roundedWeightedShare(); got != 0 {
		t.Errorf("roundedWeightedShare() = %d, want 0", got)
	}
}

// A resource with nothing lendable is skipped rather than counted, so the
// production path leaves the ratio undefined when it is the only one borrowed.
func TestDominantResourceShareLeavesRatioUndefinedWithoutLendable(t *testing.T) {
	ctx, log := utiltesting.ContextWithLog(t)
	cache := New(utiltesting.NewFakeClient())
	cache.AddOrUpdateResourceFlavor(log, utiltestingapi.MakeResourceFlavor("default").Obj())
	if err := cache.AddOrUpdateCohort(utiltestingapi.MakeCohort("cohort").Obj()); err != nil {
		t.Fatalf("AddOrUpdateCohort() = %v", err)
	}
	cq := utiltestingapi.MakeClusterQueue("cq").
		Cohort("cohort").
		NamespaceSelector(nil).
		FairWeight(resource.MustParse("1")).
		ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").
			ResourceQuotaWrapper("cpu").NominalQuota("0").Append().Obj()).
		Obj()
	if err := cache.AddClusterQueue(ctx, cq); err != nil {
		t.Fatalf("AddClusterQueue() = %v", err)
	}
	snapshot, err := cache.Snapshot(ctx)
	if err != nil {
		t.Fatalf("Snapshot() = %v", err)
	}

	req := resources.FlavorResourceQuantities{
		{Flavor: "default", Resource: corev1.ResourceCPU}: resources.NewAmount(1_000),
	}
	drs := dominantResourceShare(snapshot.ClusterQueue("cq"), req)
	if !drs.borrowing {
		t.Fatal("the fixture does not borrow, so it does not test the case")
	}
	if drs.ratioDefined {
		t.Error("a resource with nothing lendable defined a ratio")
	}
	if got := drs.PreciseWeightedShare(); got != 0 {
		t.Errorf("PreciseWeightedShare() = %v, want 0", got)
	}
}

// Zero from roundedWeightedShare is documented as usage below the nominal
// quota, so a borrower whose ratio underflowed has to reach at least one.
func TestRoundedWeightedShareKeepsBorrowerPositive(t *testing.T) {
	borrower := DRS{
		borrowing: true, ratioDefined: true,
		unweightedRatio: math.SmallestNonzeroFloat64, fairWeight: 2,
	}
	if got, _ := borrower.roundedWeightedShare(); got < 1 {
		t.Errorf("roundedWeightedShare() = %d for a borrower, want at least 1", got)
	}
	idle := DRS{fairWeight: defaultWeight}
	if got, _ := idle.roundedWeightedShare(); got != 0 {
		t.Errorf("roundedWeightedShare() = %d for a node that is not borrowing, want 0", got)
	}
}

// A borrower whose ratio underflowed outranks a node that is not borrowing,
// rather than tying with it and falling through to the next tie-break.
func TestCompareDRSRanksTinyBorrowerAboveNonBorrower(t *testing.T) {
	borrower := DRS{
		borrowing: true, ratioDefined: true,
		unweightedRatio: math.SmallestNonzeroFloat64, fairWeight: 2,
	}
	idle := DRS{fairWeight: defaultWeight}
	if got := CompareDRS(borrower, idle); got <= 0 {
		t.Errorf("CompareDRS(borrower, idle) = %d, want a positive value", got)
	}
	if got := CompareDRS(idle, borrower); got >= 0 {
		t.Errorf("CompareDRS(idle, borrower) = %d, want a negative value", got)
	}
}

// Two infinities divide to NaN, which compares false against every bound and
// would otherwise reach a conversion that promises a value in range.
func TestWeightedShareNaNIsConservative(t *testing.T) {
	drs := DRS{borrowing: true, unweightedRatio: math.Inf(1), fairWeight: math.Inf(1)}
	if raw := drs.unweightedRatio / drs.fairWeight; !math.IsNaN(raw) {
		t.Fatalf("the fixture no longer produces NaN: %v", raw)
	}
	if got := drs.PreciseWeightedShare(); !math.IsInf(got, 1) {
		t.Errorf("PreciseWeightedShare() = %v, want +Inf", got)
	}
	if got, _ := drs.roundedWeightedShare(); got != math.MaxInt64 {
		t.Errorf("roundedWeightedShare() = %d, want %d", got, int64(math.MaxInt64))
	}
}
