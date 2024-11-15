/*
Copyright 2022 The Kubernetes Authors.

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

package flavorassigner

import (
	"testing"

	"github.com/go-logr/logr"
	"github.com/go-logr/logr/testr"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/cache"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/resources"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	"sigs.k8s.io/kueue/pkg/workload"
)

type testOracle struct{}

func (f *testOracle) IsReclaimPossible(log logr.Logger, cq *cache.ClusterQueueSnapshot, wl workload.Info, fr resources.FlavorResource, quantity int64) bool {
	return !cq.BorrowingWith(fr, quantity)
}

func TestAssignFlavors(t *testing.T) {
	resourceFlavors := map[kueue.ResourceFlavorReference]*kueue.ResourceFlavor{
		"default": {
			ObjectMeta: metav1.ObjectMeta{Name: "default"},
		},
		"one":   utiltesting.MakeResourceFlavor("one").NodeLabel("type", "one").Obj(),
		"two":   utiltesting.MakeResourceFlavor("two").NodeLabel("type", "two").Obj(),
		"b_one": utiltesting.MakeResourceFlavor("b_one").NodeLabel("b_type", "one").Obj(),
		"b_two": utiltesting.MakeResourceFlavor("b_two").NodeLabel("b_type", "two").Obj(),
		"tainted": utiltesting.MakeResourceFlavor("tainted").
			Taint(corev1.Taint{
				Key:    "instance",
				Value:  "spot",
				Effect: corev1.TaintEffectNoSchedule,
			}).Obj(),
	}

	cases := map[string]struct {
		wlPods                     []kueue.PodSet
		wlReclaimablePods          []kueue.ReclaimablePod
		clusterQueue               kueue.ClusterQueue
		clusterQueueUsage          resources.FlavorResourceQuantities
		secondaryClusterQueue      *kueue.ClusterQueue
		secondaryClusterQueueUsage resources.FlavorResourceQuantities
		wantRepMode                FlavorAssignmentMode
		wantAssignment             Assignment
		disableLendingLimit        bool
		enableFairSharing          bool
	}{
		"single flavor, fits": {
			wlPods: []kueue.PodSet{
				*utiltesting.MakePodSet("main", 1).
					Request(corev1.ResourceCPU, "1").
					Request(corev1.ResourceMemory, "1Mi").
					Obj(),
			},
			clusterQueue: utiltesting.MakeClusterQueue("test-clusterqueue").
				ResourceGroup(
					utiltesting.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "1").
						Resource(corev1.ResourceMemory, "2Mi").
						FlavorQuotas,
				).ClusterQueue,
			wantRepMode: Fit,
			wantAssignment: Assignment{
				PodSets: []PodSetAssignment{
					{
						Name: "main",
						Flavors: ResourceAssignment{
							corev1.ResourceCPU:    {Name: "default", Mode: Fit, TriedFlavorIdx: -1},
							corev1.ResourceMemory: {Name: "default", Mode: Fit, TriedFlavorIdx: -1},
						},
						Requests: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("1"),
							corev1.ResourceMemory: resource.MustParse("1Mi"),
						},
						Count: 1,
					},
				},
				Usage: resources.FlavorResourceQuantities{
					{Flavor: "default", Resource: corev1.ResourceCPU}:    1_000,
					{Flavor: "default", Resource: corev1.ResourceMemory}: utiltesting.Mi,
				},
			},
		},
		"single flavor, fits tainted flavor": {
			wlPods: []kueue.PodSet{
				*utiltesting.MakePodSet("main", 1).
					Request(corev1.ResourceCPU, "1").
					Toleration(corev1.Toleration{
						Key:      "instance",
						Operator: corev1.TolerationOpEqual,
						Value:    "spot",
						Effect:   corev1.TaintEffectNoSchedule,
					}).
					Obj(),
			},
			clusterQueue: utiltesting.MakeClusterQueue("test-clusterqueue").
				ResourceGroup(
					utiltesting.MakeFlavorQuotas("tainted").
						Resource(corev1.ResourceCPU, "4").
						FlavorQuotas,
				).ClusterQueue,

			wantRepMode: Fit,
			wantAssignment: Assignment{
				PodSets: []PodSetAssignment{{
					Name: "main",
					Flavors: ResourceAssignment{
						corev1.ResourceCPU: {Name: "tainted", Mode: Fit, TriedFlavorIdx: -1},
					},
					Requests: corev1.ResourceList{
						corev1.ResourceCPU: resource.MustParse("1"),
					},
					Count: 1,
				}},
				Usage: resources.FlavorResourceQuantities{
					{Flavor: "tainted", Resource: corev1.ResourceCPU}: 1_000,
				},
			},
		},
		"single flavor, used resources, doesn't fit": {
			wlPods: []kueue.PodSet{
				*utiltesting.MakePodSet("main", 1).
					Request(corev1.ResourceCPU, "2").
					Obj(),
			},
			clusterQueue: utiltesting.MakeClusterQueue("test-clusterqueue").
				ResourceGroup(
					utiltesting.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "4").
						FlavorQuotas,
				).ClusterQueue,
			clusterQueueUsage: resources.FlavorResourceQuantities{
				{Flavor: "default", Resource: corev1.ResourceCPU}: 3_000,
			},
			wantRepMode: Preempt,
			wantAssignment: Assignment{
				PodSets: []PodSetAssignment{{
					Name: "main",
					Flavors: ResourceAssignment{
						corev1.ResourceCPU: {Name: "default", Mode: Preempt, TriedFlavorIdx: -1},
					},
					Requests: corev1.ResourceList{
						corev1.ResourceCPU: resource.MustParse("2"),
					},
					Status: &Status{
						reasons: []string{"insufficient unused quota for cpu in flavor default, 1 more needed"},
					},
					Count: 1,
				}},
				Usage: resources.FlavorResourceQuantities{
					{Flavor: "default", Resource: corev1.ResourceCPU}: 2_000,
				},
			},
		},
		"multiple resource groups, fits": {
			wlPods: []kueue.PodSet{
				*utiltesting.MakePodSet("main", 1).
					Request(corev1.ResourceCPU, "3").
					Request(corev1.ResourceMemory, "10Mi").
					Obj(),
			},
			clusterQueue: utiltesting.MakeClusterQueue("test-clusterqueue").
				ResourceGroup(
					utiltesting.MakeFlavorQuotas("one").
						Resource(corev1.ResourceCPU, "2").
						FlavorQuotas,
					utiltesting.MakeFlavorQuotas("two").
						Resource(corev1.ResourceCPU, "4").
						FlavorQuotas,
				).
				ResourceGroup(
					utiltesting.MakeFlavorQuotas("b_one").
						Resource(corev1.ResourceMemory, "1Gi").
						FlavorQuotas,
					utiltesting.MakeFlavorQuotas("b_two").
						Resource(corev1.ResourceMemory, "5Gi").
						FlavorQuotas,
				).
				ClusterQueue,
			wantRepMode: Fit,
			wantAssignment: Assignment{
				PodSets: []PodSetAssignment{{
					Name: "main",
					Flavors: ResourceAssignment{
						corev1.ResourceCPU:    {Name: "two", Mode: Fit, TriedFlavorIdx: -1},
						corev1.ResourceMemory: {Name: "b_one", Mode: Fit, TriedFlavorIdx: 0},
					},
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("3"),
						corev1.ResourceMemory: resource.MustParse("10Mi"),
					},
					Count: 1,
				}},
				Usage: resources.FlavorResourceQuantities{
					{Flavor: "two", Resource: corev1.ResourceCPU}:      3_000,
					{Flavor: "b_one", Resource: corev1.ResourceMemory}: 10 * utiltesting.Mi,
				},
			},
		},
		"multiple resource groups, one could fit with preemption, other doesn't fit": {
			wlPods: []kueue.PodSet{
				*utiltesting.MakePodSet("main", 1).
					Request(corev1.ResourceCPU, "3").
					Request(corev1.ResourceMemory, "10Mi").
					Obj(),
			},
			clusterQueue: utiltesting.MakeClusterQueue("test-clusterqueue").
				ResourceGroup(
					utiltesting.MakeFlavorQuotas("one").
						Resource(corev1.ResourceCPU, "3").
						FlavorQuotas,
				).ResourceGroup(
				utiltesting.MakeFlavorQuotas("b_one").
					Resource(corev1.ResourceMemory, "1Mi").
					FlavorQuotas,
			).ClusterQueue,

			clusterQueueUsage: resources.FlavorResourceQuantities{
				{Flavor: "one", Resource: corev1.ResourceCPU}: 1_000,
			},

			wantAssignment: Assignment{
				PodSets: []PodSetAssignment{{
					Name: "main",
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("3"),
						corev1.ResourceMemory: resource.MustParse("10Mi"),
					},
					Status: &Status{
						reasons: []string{
							"insufficient quota for memory in flavor b_one, request > maximum capacity (10Mi > 1Mi)",
						},
					},
					Count: 1,
				}},
				Usage: resources.FlavorResourceQuantities{},
			},
		},
		"multiple resource groups with multiple resources, fits": {
			wlPods: []kueue.PodSet{
				*utiltesting.MakePodSet("main", 1).
					Request(corev1.ResourceCPU, "3").
					Request(corev1.ResourceMemory, "10Mi").
					Request("example.com/gpu", "3").
					Obj(),
			},
			clusterQueue: utiltesting.MakeClusterQueue("test-clusterqueue").
				ResourceGroup(
					utiltesting.MakeFlavorQuotas("one").
						Resource(corev1.ResourceCPU, "2").
						Resource(corev1.ResourceMemory, "1Gi").
						FlavorQuotas,
					utiltesting.MakeFlavorQuotas("two").
						Resource(corev1.ResourceCPU, "4").
						Resource(corev1.ResourceMemory, "15Mi").
						FlavorQuotas,
				).ResourceGroup(
				utiltesting.MakeFlavorQuotas("b_one").
					Resource("example.com/gpu", "4").
					FlavorQuotas,
				utiltesting.MakeFlavorQuotas("b_two").
					Resource("example.com/gpu", "2").
					FlavorQuotas,
			).ClusterQueue,

			wantRepMode: Fit,
			wantAssignment: Assignment{
				PodSets: []PodSetAssignment{{
					Name: "main",
					Flavors: ResourceAssignment{
						corev1.ResourceCPU:    {Name: "two", Mode: Fit, TriedFlavorIdx: -1},
						corev1.ResourceMemory: {Name: "two", Mode: Fit, TriedFlavorIdx: -1},
						"example.com/gpu":     {Name: "b_one", Mode: Fit, TriedFlavorIdx: 0},
					},
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("3"),
						corev1.ResourceMemory: resource.MustParse("10Mi"),
						"example.com/gpu":     resource.MustParse("3"),
					},
					Count: 1,
				}},
				Usage: resources.FlavorResourceQuantities{
					{Flavor: "two", Resource: corev1.ResourceCPU}:    3_000,
					{Flavor: "two", Resource: corev1.ResourceMemory}: 10 * utiltesting.Mi,
					{Flavor: "b_one", Resource: "example.com/gpu"}:   3,
				},
			},
		},
		"multiple resource groups with multiple resources, fits with different modes": {
			wlPods: []kueue.PodSet{
				*utiltesting.MakePodSet("main", 1).
					Request(corev1.ResourceCPU, "3").
					Request(corev1.ResourceMemory, "10Mi").
					Request("example.com/gpu", "3").
					Obj(),
			},
			clusterQueue: utiltesting.MakeClusterQueue("test-clusterqueue").
				ResourceGroup(
					utiltesting.MakeFlavorQuotas("one").
						Resource(corev1.ResourceCPU, "2").
						Resource(corev1.ResourceMemory, "1Gi").
						FlavorQuotas,
					utiltesting.MakeFlavorQuotas("two").
						Resource(corev1.ResourceCPU, "4").
						Resource(corev1.ResourceMemory, "15Mi").
						FlavorQuotas,
				).ResourceGroup(
				utiltesting.MakeFlavorQuotas("b_one").
					Resource("example.com/gpu", "4").
					FlavorQuotas,
			).Cohort("test-cohort").
				ClusterQueue,
			clusterQueueUsage: resources.FlavorResourceQuantities{
				{Flavor: "two", Resource: corev1.ResourceMemory}: 10 * utiltesting.Mi,
			},
			secondaryClusterQueue: utiltesting.MakeClusterQueue("test-secondary-clusterqueue").
				ResourceGroup(
					utiltesting.MakeFlavorQuotas("b_one").
						Resource("example.com/gpu", "0").
						FlavorQuotas,
				).
				Cohort("test-cohort").
				Obj(),
			secondaryClusterQueueUsage: resources.FlavorResourceQuantities{
				{Flavor: "b_one", Resource: "example.com/gpu"}: 2,
			},
			wantRepMode: Preempt,
			wantAssignment: Assignment{
				PodSets: []PodSetAssignment{{
					Name: "main",
					Flavors: ResourceAssignment{
						corev1.ResourceCPU:    {Name: "two", Mode: Fit, TriedFlavorIdx: -1},
						corev1.ResourceMemory: {Name: "two", Mode: Preempt, TriedFlavorIdx: -1},
						"example.com/gpu":     {Name: "b_one", Mode: Preempt, TriedFlavorIdx: -1},
					},
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("3"),
						corev1.ResourceMemory: resource.MustParse("10Mi"),
						"example.com/gpu":     resource.MustParse("3"),
					},
					Status: &Status{
						reasons: []string{
							"insufficient quota for cpu in flavor one, request > maximum capacity (3 > 2)",
							"insufficient unused quota for memory in flavor two, 5Mi more needed",
							"insufficient unused quota for example.com/gpu in flavor b_one, 1 more needed",
						},
					},
					Count: 1,
				}},
				Borrowing: true,
				Usage: resources.FlavorResourceQuantities{
					{Flavor: "two", Resource: corev1.ResourceCPU}:    3_000,
					{Flavor: "two", Resource: corev1.ResourceMemory}: 10 * utiltesting.Mi,
					{Flavor: "b_one", Resource: "example.com/gpu"}:   3,
				},
			},
		},
		"multiple resources in a group, doesn't fit": {
			wlPods: []kueue.PodSet{
				*utiltesting.MakePodSet("main", 1).
					Request(corev1.ResourceCPU, "3").
					Request(corev1.ResourceMemory, "10Mi").
					Obj(),
			},
			clusterQueue: utiltesting.MakeClusterQueue("test-clusterqueue").
				ResourceGroup(
					utiltesting.MakeFlavorQuotas("one").
						Resource(corev1.ResourceCPU, "2").
						Resource(corev1.ResourceMemory, "1Gi").
						FlavorQuotas,
					utiltesting.MakeFlavorQuotas("two").
						Resource(corev1.ResourceCPU, "4").
						Resource(corev1.ResourceMemory, "5Mi").
						FlavorQuotas,
				).ClusterQueue,
			wantAssignment: Assignment{
				PodSets: []PodSetAssignment{{
					Name: "main",
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("3"),
						corev1.ResourceMemory: resource.MustParse("10Mi"),
					},
					Status: &Status{
						reasons: []string{
							"insufficient quota for cpu in flavor one, request > maximum capacity (3 > 2)",
							"insufficient quota for memory in flavor two, request > maximum capacity (10Mi > 5Mi)",
						},
					},
					Count: 1,
				}},
				Usage: resources.FlavorResourceQuantities{},
			},
		},
		"multiple flavors, fits while skipping tainted flavor": {
			wlPods: []kueue.PodSet{
				*utiltesting.MakePodSet("main", 1).
					Request(corev1.ResourceCPU, "3").
					Obj(),
			},
			clusterQueue: utiltesting.MakeClusterQueue("test-clusterqueue").
				ResourceGroup(
					utiltesting.MakeFlavorQuotas("tainted").
						Resource(corev1.ResourceCPU, "4").
						FlavorQuotas,
					utiltesting.MakeFlavorQuotas("two").
						Resource(corev1.ResourceCPU, "4").
						FlavorQuotas,
				).ClusterQueue,
			wantRepMode: Fit,
			wantAssignment: Assignment{
				PodSets: []PodSetAssignment{{
					Name: "main",
					Flavors: ResourceAssignment{
						corev1.ResourceCPU: {Name: "two", Mode: Fit, TriedFlavorIdx: -1},
					},
					Requests: corev1.ResourceList{
						corev1.ResourceCPU: resource.MustParse("3"),
					},
					Count: 1,
				}},
				Usage: resources.FlavorResourceQuantities{
					{Flavor: "two", Resource: corev1.ResourceCPU}: 3_000,
				},
			},
		},
		"multiple flavors, fits a node selector": {
			wlPods: []kueue.PodSet{
				{
					Count: 1,
					Name:  "main",
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers: utiltesting.SingleContainerForRequest(map[corev1.ResourceName]string{
								corev1.ResourceCPU: "1",
							}),
							// ignored:foo should get ignored
							NodeSelector: map[string]string{"type": "two", "ignored1": "foo"},
							Affinity: &corev1.Affinity{NodeAffinity: &corev1.NodeAffinity{
								RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{
									NodeSelectorTerms: []corev1.NodeSelectorTerm{
										{
											MatchExpressions: []corev1.NodeSelectorRequirement{
												{
													// this expression should get ignored
													Key:      "ignored2",
													Operator: corev1.NodeSelectorOpIn,
													Values:   []string{"bar"},
												},
											},
										},
									},
								}},
							},
						},
					},
				},
			},
			clusterQueue: utiltesting.MakeClusterQueue("test-clusterqueue").
				ResourceGroup(
					utiltesting.MakeFlavorQuotas("one").
						Resource(corev1.ResourceCPU, "4").
						FlavorQuotas,
					utiltesting.MakeFlavorQuotas("two").
						Resource(corev1.ResourceCPU, "4").
						FlavorQuotas,
				).ClusterQueue,
			wantRepMode: Fit,
			wantAssignment: Assignment{
				PodSets: []PodSetAssignment{{
					Name: "main",
					Flavors: ResourceAssignment{
						corev1.ResourceCPU: {Name: "two", Mode: Fit, TriedFlavorIdx: -1},
					},
					Requests: corev1.ResourceList{
						corev1.ResourceCPU: resource.MustParse("1"),
					},
					Count: 1,
				}},
				Usage: resources.FlavorResourceQuantities{
					{Flavor: "two", Resource: corev1.ResourceCPU}: 1_000,
				},
			},
		},
		"multiple flavors, fits with node affinity": {
			wlPods: []kueue.PodSet{
				{
					Count: 1,
					Name:  "main",
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers: utiltesting.SingleContainerForRequest(map[corev1.ResourceName]string{
								corev1.ResourceCPU:    "1",
								corev1.ResourceMemory: "1Mi",
							}),
							NodeSelector: map[string]string{"ignored1": "foo"},
							Affinity: &corev1.Affinity{NodeAffinity: &corev1.NodeAffinity{
								RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{
									NodeSelectorTerms: []corev1.NodeSelectorTerm{
										{
											MatchExpressions: []corev1.NodeSelectorRequirement{
												{
													Key:      "type",
													Operator: corev1.NodeSelectorOpIn,
													Values:   []string{"two"},
												},
											},
										},
									},
								}},
							},
						},
					},
				},
			},
			clusterQueue: utiltesting.MakeClusterQueue("test-clusterqueue").
				ResourceGroup(
					utiltesting.MakeFlavorQuotas("one").
						Resource(corev1.ResourceCPU, "4").
						Resource(corev1.ResourceMemory, "1Gi").
						FlavorQuotas,
					utiltesting.MakeFlavorQuotas("two").
						Resource(corev1.ResourceCPU, "4").
						Resource(corev1.ResourceMemory, "1Gi").
						FlavorQuotas,
				).ClusterQueue,

			wantRepMode: Fit,
			wantAssignment: Assignment{
				PodSets: []PodSetAssignment{{
					Name: "main",
					Flavors: ResourceAssignment{
						corev1.ResourceCPU:    {Name: "two", Mode: Fit, TriedFlavorIdx: -1},
						corev1.ResourceMemory: {Name: "two", Mode: Fit, TriedFlavorIdx: -1},
					},
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("1Mi"),
					},
					Count: 1,
				}},
				Usage: resources.FlavorResourceQuantities{
					{Flavor: "two", Resource: corev1.ResourceCPU}:    1_000,
					{Flavor: "two", Resource: corev1.ResourceMemory}: utiltesting.Mi,
				},
			},
		},
		"multiple flavors, node affinity fits any flavor": {
			wlPods: []kueue.PodSet{
				{
					Count: 1,
					Name:  "main",
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers: utiltesting.SingleContainerForRequest(map[corev1.ResourceName]string{
								corev1.ResourceCPU: "1",
							}),
							Affinity: &corev1.Affinity{NodeAffinity: &corev1.NodeAffinity{
								RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{
									NodeSelectorTerms: []corev1.NodeSelectorTerm{
										{
											MatchExpressions: []corev1.NodeSelectorRequirement{
												{
													Key:      "ignored2",
													Operator: corev1.NodeSelectorOpIn,
													Values:   []string{"bar"},
												},
											},
										},
										{
											MatchExpressions: []corev1.NodeSelectorRequirement{
												{
													// although this terms selects two
													// the first term practically matches
													// any flavor; and since the terms
													// are ORed, any flavor can be selected.
													Key:      "cpuType",
													Operator: corev1.NodeSelectorOpIn,
													Values:   []string{"two"},
												},
											},
										},
									},
								}},
							},
						},
					},
				},
			},
			clusterQueue: utiltesting.MakeClusterQueue("test-clusterqueue").
				ResourceGroup(
					utiltesting.MakeFlavorQuotas("one").
						Resource(corev1.ResourceCPU, "4").
						FlavorQuotas,
					utiltesting.MakeFlavorQuotas("two").
						Resource(corev1.ResourceCPU, "4").
						FlavorQuotas,
				).ClusterQueue,

			wantRepMode: Fit,
			wantAssignment: Assignment{
				PodSets: []PodSetAssignment{{
					Name: "main",
					Flavors: ResourceAssignment{
						corev1.ResourceCPU: {Name: "one", Mode: Fit, TriedFlavorIdx: 0},
					},
					Requests: corev1.ResourceList{
						corev1.ResourceCPU: resource.MustParse("1"),
					},
					Count: 1,
				}},
				Usage: resources.FlavorResourceQuantities{
					{Flavor: "one", Resource: corev1.ResourceCPU}: 1_000,
				},
			},
		},
		"multiple flavors, doesn't fit node affinity": {
			wlPods: []kueue.PodSet{
				{
					Count: 1,
					Name:  "main",
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers: utiltesting.SingleContainerForRequest(map[corev1.ResourceName]string{
								corev1.ResourceCPU: "1",
							}),
							Affinity: &corev1.Affinity{NodeAffinity: &corev1.NodeAffinity{
								RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{
									NodeSelectorTerms: []corev1.NodeSelectorTerm{
										{
											MatchExpressions: []corev1.NodeSelectorRequirement{
												{
													Key:      "type",
													Operator: corev1.NodeSelectorOpIn,
													Values:   []string{"three"},
												},
											},
										},
									},
								}},
							},
						},
					},
				},
			},
			clusterQueue: utiltesting.MakeClusterQueue("test-clusterqueue").
				ResourceGroup(
					utiltesting.MakeFlavorQuotas("one").
						Resource(corev1.ResourceCPU, "4").
						FlavorQuotas,
					utiltesting.MakeFlavorQuotas("two").
						Resource(corev1.ResourceCPU, "4").
						FlavorQuotas,
				).ClusterQueue,

			wantAssignment: Assignment{
				PodSets: []PodSetAssignment{{
					Name: "main",
					Requests: corev1.ResourceList{
						corev1.ResourceCPU: resource.MustParse("1"),
					},
					Status: &Status{
						reasons: []string{
							"flavor one doesn't match node affinity",
							"flavor two doesn't match node affinity",
						},
					},
					Count: 1,
				}},
				Usage: resources.FlavorResourceQuantities{},
			},
		},
		"multiple specs, fit different flavors": {
			wlPods: []kueue.PodSet{
				*utiltesting.MakePodSet("driver", 1).
					Request(corev1.ResourceCPU, "5").
					Obj(),
				*utiltesting.MakePodSet("worker", 1).
					Request(corev1.ResourceCPU, "3").
					Obj(),
			},
			clusterQueue: utiltesting.MakeClusterQueue("test-clusterqueue").
				ResourceGroup(
					utiltesting.MakeFlavorQuotas("one").
						Resource(corev1.ResourceCPU, "4").
						FlavorQuotas,
					utiltesting.MakeFlavorQuotas("two").
						Resource(corev1.ResourceCPU, "10").
						FlavorQuotas,
				).ClusterQueue,

			wantRepMode: Fit,
			wantAssignment: Assignment{
				PodSets: []PodSetAssignment{
					{
						Name: "driver",
						Flavors: ResourceAssignment{
							corev1.ResourceCPU: {Name: "two", Mode: Fit, TriedFlavorIdx: -1},
						},
						Requests: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("5"),
						},
						Count: 1,
					},
					{
						Name: "worker",
						Flavors: ResourceAssignment{
							corev1.ResourceCPU: {Name: "one", Mode: Fit, TriedFlavorIdx: 0},
						},
						Requests: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("3"),
						},
						Count: 1,
					},
				},
				Usage: resources.FlavorResourceQuantities{
					{Flavor: "one", Resource: corev1.ResourceCPU}: 3_000,
					{Flavor: "two", Resource: corev1.ResourceCPU}: 5_000,
				},
			},
		},
		"multiple specs, fits borrowing": {
			wlPods: []kueue.PodSet{
				*utiltesting.MakePodSet("driver", 1).
					Request(corev1.ResourceCPU, "4").
					Request(corev1.ResourceMemory, "1Gi").
					Obj(),
				*utiltesting.MakePodSet("worker", 1).
					Request(corev1.ResourceCPU, "6").
					Request(corev1.ResourceMemory, "4Gi").
					Obj(),
			},
			clusterQueue: utiltesting.MakeClusterQueue("test-clusterqueue").
				ResourceGroup(
					utiltesting.MakeFlavorQuotas("default").
						ResourceQuotaWrapper(corev1.ResourceCPU).NominalQuota("2").BorrowingLimit("98").Append().
						Resource(corev1.ResourceMemory, "2Gi").
						FlavorQuotas,
				).Cohort("test-cohort").
				ClusterQueue,
			secondaryClusterQueue: utiltesting.MakeClusterQueue("test-secondary-clusterqueue").
				ResourceGroup(
					*utiltesting.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "198").
						Resource(corev1.ResourceMemory, "198Gi").
						Obj(),
				).
				Cohort("test-cohort").
				Obj(),
			wantRepMode: Fit,
			wantAssignment: Assignment{
				PodSets: []PodSetAssignment{
					{
						Name: "driver",
						Flavors: ResourceAssignment{
							corev1.ResourceCPU:    {Name: "default", Mode: Fit, TriedFlavorIdx: -1},
							corev1.ResourceMemory: {Name: "default", Mode: Fit, TriedFlavorIdx: -1},
						},
						Requests: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("4"),
							corev1.ResourceMemory: resource.MustParse("1Gi"),
						},
						Count: 1,
					},
					{
						Name: "worker",
						Flavors: ResourceAssignment{
							corev1.ResourceCPU:    {Name: "default", Mode: Fit, TriedFlavorIdx: -1},
							corev1.ResourceMemory: {Name: "default", Mode: Fit, TriedFlavorIdx: -1},
						},
						Requests: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("6"),
							corev1.ResourceMemory: resource.MustParse("4Gi"),
						},
						Count: 1,
					},
				},
				Borrowing: true,
				Usage: resources.FlavorResourceQuantities{
					{Flavor: "default", Resource: corev1.ResourceCPU}:    10_000,
					{Flavor: "default", Resource: corev1.ResourceMemory}: 5 * utiltesting.Gi,
				},
			},
		},
		"not enough space to borrow": {
			wlPods: []kueue.PodSet{
				*utiltesting.MakePodSet("main", 1).
					Request(corev1.ResourceCPU, "2").
					Obj(),
			},
			clusterQueue: utiltesting.MakeClusterQueue("test-clusterqueue").
				ResourceGroup(
					utiltesting.MakeFlavorQuotas("one").
						Resource(corev1.ResourceCPU, "1").
						FlavorQuotas,
				).Cohort("test-cohort").ClusterQueue,
			secondaryClusterQueue: utiltesting.MakeClusterQueue("test-secondary-clusterqueue").
				ResourceGroup(
					utiltesting.MakeFlavorQuotas("one").
						ResourceQuotaWrapper(corev1.ResourceCPU).NominalQuota("10").LendingLimit("0").Append().
						FlavorQuotas,
				).
				Cohort("test-cohort").
				Obj(),
			secondaryClusterQueueUsage: resources.FlavorResourceQuantities{
				{Flavor: "one", Resource: corev1.ResourceCPU}: 9_000,
			},
			wantAssignment: Assignment{
				PodSets: []PodSetAssignment{{
					Name: "main",
					Requests: corev1.ResourceList{
						corev1.ResourceCPU: resource.MustParse("2"),
					},
					Status: &Status{
						reasons: []string{"insufficient quota for cpu in flavor one, request > maximum capacity (2 > 1)"},
					},
					Count: 1,
				}},
				Usage: resources.FlavorResourceQuantities{},
			},
		},
		"past max, but can preempt in ClusterQueue": {
			wlPods: []kueue.PodSet{
				*utiltesting.MakePodSet("main", 1).
					Request(corev1.ResourceCPU, "2").
					Obj(),
			},
			clusterQueue: utiltesting.MakeClusterQueue("test-clusterqueue").
				ResourceGroup(
					utiltesting.MakeFlavorQuotas("one").
						ResourceQuotaWrapper(corev1.ResourceCPU).NominalQuota("2").BorrowingLimit("8").Append().
						FlavorQuotas,
				).Cohort("test-cohort").
				ClusterQueue,
			clusterQueueUsage: resources.FlavorResourceQuantities{
				{Flavor: "one", Resource: corev1.ResourceCPU}: 9_000,
			},
			secondaryClusterQueue: utiltesting.MakeClusterQueue("test-secondary-clusterqueue").
				ResourceGroup(
					utiltesting.MakeFlavorQuotas("one").
						Resource(corev1.ResourceCPU, "98").
						FlavorQuotas,
				).
				Cohort("test-cohort").
				Obj(),
			secondaryClusterQueueUsage: resources.FlavorResourceQuantities{
				{Flavor: "one", Resource: corev1.ResourceCPU}: 9_000,
			},
			wantRepMode: Preempt,
			wantAssignment: Assignment{
				PodSets: []PodSetAssignment{{
					Name: "main",
					Flavors: ResourceAssignment{
						corev1.ResourceCPU: {Name: "one", Mode: Preempt, TriedFlavorIdx: -1},
					},
					Requests: corev1.ResourceList{
						corev1.ResourceCPU: resource.MustParse("2"),
					},

					Status: &Status{
						reasons: []string{"insufficient unused quota for cpu in flavor one, 1 more needed"},
					},
					Count: 1,
				}},
				Borrowing: true,
				Usage: resources.FlavorResourceQuantities{
					{Flavor: "one", Resource: corev1.ResourceCPU}: 2_000,
				},
			},
		},
		"past min, but can preempt in ClusterQueue": {
			wlPods: []kueue.PodSet{
				*utiltesting.MakePodSet("main", 1).
					Request(corev1.ResourceCPU, "2").
					Obj(),
			},
			clusterQueue: utiltesting.MakeClusterQueue("test-clusterqueue").
				ResourceGroup(
					utiltesting.MakeFlavorQuotas("one").
						Resource(corev1.ResourceCPU, "2").
						FlavorQuotas,
				).ClusterQueue,
			clusterQueueUsage: resources.FlavorResourceQuantities{
				{Flavor: "one", Resource: corev1.ResourceCPU}: 1_000,
			},
			wantRepMode: Preempt,
			wantAssignment: Assignment{
				PodSets: []PodSetAssignment{{
					Name: "main",
					Flavors: ResourceAssignment{
						corev1.ResourceCPU: {Name: "one", Mode: Preempt, TriedFlavorIdx: -1},
					},
					Requests: corev1.ResourceList{
						corev1.ResourceCPU: resource.MustParse("2"),
					},
					Status: &Status{
						reasons: []string{"insufficient unused quota for cpu in flavor one, 1 more needed"},
					},
					Count: 1,
				}},
				Usage: resources.FlavorResourceQuantities{
					{Flavor: "one", Resource: corev1.ResourceCPU}: 2_000,
				},
			},
		},
		"past min, but can preempt in cohort and ClusterQueue": {
			wlPods: []kueue.PodSet{
				*utiltesting.MakePodSet("main", 1).
					Request(corev1.ResourceCPU, "2").
					Obj(),
			},
			clusterQueue: utiltesting.MakeClusterQueue("test-clusterqueue").
				ResourceGroup(
					utiltesting.MakeFlavorQuotas("one").
						Resource(corev1.ResourceCPU, "3").
						FlavorQuotas,
				).Cohort("test-cohort").ClusterQueue,
			clusterQueueUsage: resources.FlavorResourceQuantities{
				{Flavor: "one", Resource: corev1.ResourceCPU}: 2_000,
			},
			secondaryClusterQueue: utiltesting.MakeClusterQueue("test-secondary-clusterqueue").
				ResourceGroup(
					utiltesting.MakeFlavorQuotas("one").
						Resource(corev1.ResourceCPU, "7").
						FlavorQuotas,
				).
				Cohort("test-cohort").
				Obj(),
			secondaryClusterQueueUsage: resources.FlavorResourceQuantities{
				{Flavor: "one", Resource: corev1.ResourceCPU}: 8_000,
			},
			wantRepMode: Preempt,
			wantAssignment: Assignment{
				PodSets: []PodSetAssignment{{
					Name: "main",
					Flavors: ResourceAssignment{
						corev1.ResourceCPU: {Name: "one", Mode: Preempt, TriedFlavorIdx: -1},
					},
					Requests: corev1.ResourceList{
						corev1.ResourceCPU: resource.MustParse("2"),
					},
					Status: &Status{
						reasons: []string{"insufficient unused quota for cpu in flavor one, 2 more needed"},
					},
					Count: 1,
				}},
				Borrowing: true,
				Usage: resources.FlavorResourceQuantities{
					{Flavor: "one", Resource: corev1.ResourceCPU}: 2_000,
				},
			},
		},
		"can only preempt flavors that match affinity": {
			wlPods: []kueue.PodSet{
				{
					Count: 1,
					Name:  "main",
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Containers: utiltesting.SingleContainerForRequest(map[corev1.ResourceName]string{
								corev1.ResourceCPU: "2",
							}),
							NodeSelector: map[string]string{"type": "two"},
						},
					},
				},
			},
			clusterQueue: utiltesting.MakeClusterQueue("test-clusterqueue").
				ResourceGroup(
					utiltesting.MakeFlavorQuotas("one").
						Resource(corev1.ResourceCPU, "4").
						FlavorQuotas,
					utiltesting.MakeFlavorQuotas("two").
						Resource(corev1.ResourceCPU, "4").
						FlavorQuotas,
				).ClusterQueue,
			clusterQueueUsage: resources.FlavorResourceQuantities{
				{Flavor: "one", Resource: corev1.ResourceCPU}: 3_000,
				{Flavor: "two", Resource: corev1.ResourceCPU}: 3_000,
			},
			wantRepMode: Preempt,
			wantAssignment: Assignment{
				PodSets: []PodSetAssignment{{
					Name: "main",
					Flavors: ResourceAssignment{
						corev1.ResourceCPU: {Name: "two", Mode: Preempt, TriedFlavorIdx: -1},
					},
					Requests: corev1.ResourceList{
						corev1.ResourceCPU: resource.MustParse("2"),
					},
					Status: &Status{
						reasons: []string{
							"flavor one doesn't match node affinity",
							"insufficient unused quota for cpu in flavor two, 1 more needed",
						},
					},
					Count: 1,
				}},
				Usage: resources.FlavorResourceQuantities{
					{Flavor: "two", Resource: corev1.ResourceCPU}: 2_000,
				},
			},
		},
		"each podset requires preemption on a different flavor": {
			wlPods: []kueue.PodSet{
				*utiltesting.MakePodSet("launcher", 1).
					Request(corev1.ResourceCPU, "2").
					Obj(),
				*utiltesting.MakePodSet("workers", 10).
					Request(corev1.ResourceCPU, "1").
					Toleration(corev1.Toleration{
						Key:      "instance",
						Operator: corev1.TolerationOpEqual,
						Value:    "spot",
						Effect:   corev1.TaintEffectNoSchedule,
					}).
					Obj(),
			},
			clusterQueue: utiltesting.MakeClusterQueue("test-clusterqueue").
				ResourceGroup(
					utiltesting.MakeFlavorQuotas("one").
						Resource(corev1.ResourceCPU, "4").
						FlavorQuotas,
					utiltesting.MakeFlavorQuotas("tainted").
						Resource(corev1.ResourceCPU, "10").
						FlavorQuotas,
				).ClusterQueue,
			clusterQueueUsage: resources.FlavorResourceQuantities{
				{Flavor: "one", Resource: corev1.ResourceCPU}:     3_000,
				{Flavor: "tainted", Resource: corev1.ResourceCPU}: 3_000,
			},
			wantRepMode: Preempt,
			wantAssignment: Assignment{
				PodSets: []PodSetAssignment{
					{
						Name: "launcher",
						Flavors: ResourceAssignment{
							corev1.ResourceCPU: {Name: "one", Mode: Preempt, TriedFlavorIdx: -1},
						},
						Requests: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("2"),
						},
						Status: &Status{
							reasons: []string{
								"insufficient unused quota for cpu in flavor one, 1 more needed",
								"untolerated taint {instance spot NoSchedule <nil>} in flavor tainted",
							},
						},
						Count: 1,
					},
					{
						Name: "workers",
						Flavors: ResourceAssignment{
							corev1.ResourceCPU: {Name: "tainted", Mode: Preempt, TriedFlavorIdx: -1},
						},
						Requests: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("10"),
						},
						Status: &Status{
							reasons: []string{
								"insufficient quota for cpu in flavor one, request > maximum capacity (12 > 4)",
								"insufficient unused quota for cpu in flavor tainted, 3 more needed",
							},
						},
						Count: 10,
					},
				},
				Usage: resources.FlavorResourceQuantities{
					{Flavor: "one", Resource: corev1.ResourceCPU}:     2_000,
					{Flavor: "tainted", Resource: corev1.ResourceCPU}: 10_000,
				},
			},
		},
		"resource not listed in clusterQueue": {
			wlPods: []kueue.PodSet{
				*utiltesting.MakePodSet("main", 1).
					Request("example.com/gpu", "2").
					Obj(),
			},
			clusterQueue: utiltesting.MakeClusterQueue("test-clusterqueue").
				ResourceGroup(
					utiltesting.MakeFlavorQuotas("one").
						Resource(corev1.ResourceCPU, "4").
						FlavorQuotas,
				).ClusterQueue,
			wantAssignment: Assignment{
				PodSets: []PodSetAssignment{{
					Name: "main",
					Requests: corev1.ResourceList{
						"example.com/gpu": resource.MustParse("2"),
					},
					Status: &Status{
						reasons: []string{"resource example.com/gpu unavailable in ClusterQueue"},
					},
					Count: 1,
				}},
				Usage: resources.FlavorResourceQuantities{},
			},
		},
		"num pods fit": {
			wlPods: []kueue.PodSet{
				*utiltesting.MakePodSet("main", 3).
					Request(corev1.ResourceCPU, "1").
					Obj(),
			},
			clusterQueue: utiltesting.MakeClusterQueue("test-clusterqueue").
				ResourceGroup(
					utiltesting.MakeFlavorQuotas("default").
						Resource(corev1.ResourcePods, "3").
						Resource(corev1.ResourceCPU, "10").
						FlavorQuotas,
				).ClusterQueue,

			wantAssignment: Assignment{
				PodSets: []PodSetAssignment{{
					Name: "main",
					Flavors: ResourceAssignment{

						corev1.ResourceCPU:  &FlavorAssignment{Name: "default", Mode: Fit, TriedFlavorIdx: -1},
						corev1.ResourcePods: &FlavorAssignment{Name: "default", Mode: Fit, TriedFlavorIdx: -1},
					},
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("3"),
						corev1.ResourcePods: resource.MustParse("3"),
					},
					Count: 3,
				}},
				Usage: resources.FlavorResourceQuantities{
					{Flavor: "default", Resource: corev1.ResourcePods}: 3,
					{Flavor: "default", Resource: corev1.ResourceCPU}:  3_000,
				},
			},
			wantRepMode: Fit,
		},
		"num pods don't fit": {
			wlPods: []kueue.PodSet{
				*utiltesting.MakePodSet("main", 3).
					Request(corev1.ResourceCPU, "1").
					Obj(),
			},
			clusterQueue: utiltesting.MakeClusterQueue("test-clusterqueue").
				ResourceGroup(
					utiltesting.MakeFlavorQuotas("default").
						Resource(corev1.ResourcePods, "2").
						Resource(corev1.ResourceCPU, "10").
						FlavorQuotas,
				).ClusterQueue,

			wantAssignment: Assignment{
				PodSets: []PodSetAssignment{{
					Name: "main",
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("3"),
						corev1.ResourcePods: resource.MustParse("3"),
					},
					Status: &Status{
						reasons: []string{"insufficient quota for pods in flavor default, request > maximum capacity (3 > 2)"},
					},
					Count: 3,
				}},
				Usage: resources.FlavorResourceQuantities{},
			},
		},
		"with reclaimable pods": {
			wlPods: []kueue.PodSet{
				*utiltesting.MakePodSet("main", 5).
					Request(corev1.ResourceCPU, "1").
					Obj(),
			},
			wlReclaimablePods: []kueue.ReclaimablePod{
				{
					Name:  "main",
					Count: 2,
				},
			},
			clusterQueue: utiltesting.MakeClusterQueue("test-clusterqueue").
				ResourceGroup(
					utiltesting.MakeFlavorQuotas("default").
						Resource(corev1.ResourcePods, "3").
						Resource(corev1.ResourceCPU, "10").
						FlavorQuotas,
				).ClusterQueue,
			wantAssignment: Assignment{
				PodSets: []PodSetAssignment{{
					Name: "main",
					Flavors: ResourceAssignment{

						corev1.ResourceCPU:  &FlavorAssignment{Name: "default", Mode: Fit, TriedFlavorIdx: -1},
						corev1.ResourcePods: &FlavorAssignment{Name: "default", Mode: Fit, TriedFlavorIdx: -1},
					},
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("3"),
						corev1.ResourcePods: resource.MustParse("3"),
					},
					Count: 3,
				}},
				Usage: resources.FlavorResourceQuantities{
					{Flavor: "default", Resource: corev1.ResourcePods}: 3,
					{Flavor: "default", Resource: corev1.ResourceCPU}:  3_000,
				},
			},
			wantRepMode: Fit,
		},
		"preempt before try next flavor": {
			wlPods: []kueue.PodSet{
				*utiltesting.MakePodSet("main", 1).
					Request(corev1.ResourceCPU, "9").
					Obj(),
			},
			clusterQueue: utiltesting.MakeClusterQueue("test-clusterqueue").
				FlavorFungibility(kueue.FlavorFungibility{WhenCanBorrow: kueue.Borrow, WhenCanPreempt: kueue.Preempt}).
				ResourceGroup(
					utiltesting.MakeFlavorQuotas("one").
						Resource(corev1.ResourcePods, "10").
						Resource(corev1.ResourceCPU, "10").
						FlavorQuotas,
					utiltesting.MakeFlavorQuotas("two").
						Resource(corev1.ResourcePods, "10").
						Resource(corev1.ResourceCPU, "10").
						FlavorQuotas,
				).ClusterQueue,
			clusterQueueUsage: resources.FlavorResourceQuantities{
				{Flavor: "one", Resource: corev1.ResourceCPU}: 2_000,
			},
			wantRepMode: Preempt,
			wantAssignment: Assignment{
				PodSets: []PodSetAssignment{{
					Name: "main",
					Flavors: ResourceAssignment{
						corev1.ResourceCPU:  {Name: "one", Mode: Preempt, TriedFlavorIdx: 0},
						corev1.ResourcePods: {Name: "one", Mode: Fit, TriedFlavorIdx: 0},
					},
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("9"),
						corev1.ResourcePods: resource.MustParse("1"),
					},
					Status: &Status{
						reasons: []string{"insufficient unused quota for cpu in flavor one, 1 more needed"},
					},
					Count: 1,
				}},
				Usage: resources.FlavorResourceQuantities{
					{Flavor: "one", Resource: "cpu"}:  9_000,
					{Flavor: "one", Resource: "pods"}: 1,
				},
			},
		},
		"preempt try next flavor": {
			wlPods: []kueue.PodSet{
				*utiltesting.MakePodSet("main", 1).
					Request(corev1.ResourceCPU, "9").
					Obj(),
			},
			clusterQueue: utiltesting.MakeClusterQueue("test-clusterqueue").
				ResourceGroup(
					utiltesting.MakeFlavorQuotas("one").
						Resource(corev1.ResourcePods, "10").
						Resource(corev1.ResourceCPU, "10").
						FlavorQuotas,
					utiltesting.MakeFlavorQuotas("two").
						Resource(corev1.ResourcePods, "10").
						Resource(corev1.ResourceCPU, "10").
						FlavorQuotas,
				).ClusterQueue,
			clusterQueueUsage: resources.FlavorResourceQuantities{
				{Flavor: "one", Resource: corev1.ResourceCPU}: 2_000,
			},
			wantRepMode: Fit,
			wantAssignment: Assignment{
				PodSets: []PodSetAssignment{{
					Name: "main",
					Flavors: ResourceAssignment{
						corev1.ResourceCPU:  {Name: "two", Mode: Fit, TriedFlavorIdx: -1},
						corev1.ResourcePods: {Name: "two", Mode: Fit, TriedFlavorIdx: -1},
					},
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("9"),
						corev1.ResourcePods: resource.MustParse("1"),
					},
					Count: 1,
				}},
				Usage: resources.FlavorResourceQuantities{
					{Flavor: "two", Resource: "cpu"}:  9_000,
					{Flavor: "two", Resource: "pods"}: 1,
				},
			},
		},
		"borrow try next flavor, found the first flavor": {
			wlPods: []kueue.PodSet{
				*utiltesting.MakePodSet("main", 1).
					Request(corev1.ResourceCPU, "9").
					Obj(),
			},
			clusterQueue: utiltesting.MakeClusterQueue("test-clusterqueue").
				FlavorFungibility(kueue.FlavorFungibility{WhenCanBorrow: kueue.TryNextFlavor, WhenCanPreempt: kueue.TryNextFlavor}).
				ResourceGroup(
					utiltesting.MakeFlavorQuotas("one").
						Resource(corev1.ResourcePods, "10").
						ResourceQuotaWrapper(corev1.ResourceCPU).NominalQuota("10").BorrowingLimit("1").Append().
						FlavorQuotas,
					utiltesting.MakeFlavorQuotas("two").
						Resource(corev1.ResourcePods, "10").
						Resource(corev1.ResourceCPU, "1").
						FlavorQuotas,
				).Cohort("test-cohort").
				ClusterQueue,
			clusterQueueUsage: resources.FlavorResourceQuantities{
				{Flavor: "one", Resource: corev1.ResourceCPU}: 2_000,
			},
			secondaryClusterQueue: utiltesting.MakeClusterQueue("test-secondary-clusterqueue").
				ResourceGroup(
					utiltesting.MakeFlavorQuotas("one").
						Resource(corev1.ResourceCPU, "1").
						FlavorQuotas,
				).
				Cohort("test-cohort").
				Obj(),
			wantRepMode: Fit,
			wantAssignment: Assignment{
				Borrowing: true,
				PodSets: []PodSetAssignment{{
					Name: "main",
					Flavors: ResourceAssignment{
						corev1.ResourceCPU:  {Name: "one", Mode: Fit, TriedFlavorIdx: -1},
						corev1.ResourcePods: {Name: "one", Mode: Fit, TriedFlavorIdx: -1},
					},
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("9"),
						corev1.ResourcePods: resource.MustParse("1"),
					},
					Count: 1,
				}},
				Usage: resources.FlavorResourceQuantities{
					{Flavor: "one", Resource: corev1.ResourceCPU}:  9_000,
					{Flavor: "one", Resource: corev1.ResourcePods}: 1,
				},
			},
		},
		"borrow try next flavor, found the second flavor": {
			wlPods: []kueue.PodSet{
				*utiltesting.MakePodSet("main", 1).
					Request(corev1.ResourceCPU, "9").
					Obj(),
			},
			clusterQueue: utiltesting.MakeClusterQueue("test-clusterqueue").
				FlavorFungibility(kueue.FlavorFungibility{WhenCanBorrow: kueue.TryNextFlavor, WhenCanPreempt: kueue.TryNextFlavor}).
				ResourceGroup(
					utiltesting.MakeFlavorQuotas("one").
						Resource(corev1.ResourcePods, "10").
						ResourceQuotaWrapper(corev1.ResourceCPU).NominalQuota("10").BorrowingLimit("1").Append().
						FlavorQuotas,
					utiltesting.MakeFlavorQuotas("two").
						Resource(corev1.ResourcePods, "10").
						Resource(corev1.ResourceCPU, "10").
						FlavorQuotas,
				).Cohort("test-cohort").
				ClusterQueue,
			clusterQueueUsage: resources.FlavorResourceQuantities{
				{Flavor: "one", Resource: corev1.ResourceCPU}: 2_000,
			},

			secondaryClusterQueue: utiltesting.MakeClusterQueue("test-secondary-clusterqueue").
				ResourceGroup(
					utiltesting.MakeFlavorQuotas("one").
						Resource(corev1.ResourceCPU, "1").
						FlavorQuotas,
				).
				Cohort("test-cohort").
				Obj(),
			wantRepMode: Fit,
			wantAssignment: Assignment{
				PodSets: []PodSetAssignment{{
					Name: "main",
					Flavors: ResourceAssignment{
						corev1.ResourceCPU:  {Name: "two", Mode: Fit, TriedFlavorIdx: -1},
						corev1.ResourcePods: {Name: "two", Mode: Fit, TriedFlavorIdx: -1},
					},
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("9"),
						corev1.ResourcePods: resource.MustParse("1"),
					},
					Count: 1,
				}},
				Usage: resources.FlavorResourceQuantities{
					{Flavor: "two", Resource: corev1.ResourceCPU}:  9_000,
					{Flavor: "two", Resource: corev1.ResourcePods}: 1,
				},
			},
		},
		"borrow before try next flavor": {
			wlPods: []kueue.PodSet{
				*utiltesting.MakePodSet("main", 1).
					Request(corev1.ResourceCPU, "9").
					Obj(),
			},
			clusterQueue: utiltesting.MakeClusterQueue("test-clusterqueue").
				ResourceGroup(
					utiltesting.MakeFlavorQuotas("one").
						Resource(corev1.ResourcePods, "10").
						ResourceQuotaWrapper(corev1.ResourceCPU).NominalQuota("10").BorrowingLimit("1").Append().
						FlavorQuotas,
					utiltesting.MakeFlavorQuotas("two").
						Resource(corev1.ResourcePods, "10").
						Resource(corev1.ResourceCPU, "10").
						FlavorQuotas,
				).Cohort("test-cohort").
				ClusterQueue,
			clusterQueueUsage: resources.FlavorResourceQuantities{
				{Flavor: "one", Resource: corev1.ResourceCPU}: 2_000,
			},
			secondaryClusterQueue: utiltesting.MakeClusterQueue("test-secondary-clusterqueue").
				ResourceGroup(
					utiltesting.MakeFlavorQuotas("one").
						Resource(corev1.ResourceCPU, "1").
						FlavorQuotas,
				).
				Cohort("test-cohort").
				Obj(),
			wantRepMode: Fit,
			wantAssignment: Assignment{
				Borrowing: true,
				PodSets: []PodSetAssignment{{
					Name: "main",
					Flavors: ResourceAssignment{
						corev1.ResourceCPU:  {Name: "one", Mode: Fit, TriedFlavorIdx: 0},
						corev1.ResourcePods: {Name: "one", Mode: Fit, TriedFlavorIdx: 0},
					},
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("9"),
						corev1.ResourcePods: resource.MustParse("1"),
					},
					Count: 1,
				}},
				Usage: resources.FlavorResourceQuantities{
					{Flavor: "one", Resource: "cpu"}:  9_000,
					{Flavor: "one", Resource: "pods"}: 1,
				},
			},
		},
		"when borrowing while preemption is needed for flavor one; WhenCanBorrow=Borrow": {
			wlPods: []kueue.PodSet{
				*utiltesting.MakePodSet("main", 1).
					Request(corev1.ResourceCPU, "12").
					Obj(),
			},
			clusterQueue: utiltesting.MakeClusterQueue("test-clusterqueue").
				Preemption(kueue.ClusterQueuePreemption{
					ReclaimWithinCohort: kueue.PreemptionPolicyLowerPriority,
					BorrowWithinCohort: &kueue.BorrowWithinCohort{
						Policy: kueue.BorrowWithinCohortPolicyLowerPriority,
					},
				}).
				FlavorFungibility(kueue.FlavorFungibility{
					WhenCanBorrow:  kueue.Borrow,
					WhenCanPreempt: kueue.Preempt,
				}).
				ResourceGroup(
					utiltesting.MakeFlavorQuotas("one").
						ResourceQuotaWrapper(corev1.ResourceCPU).NominalQuota("0").BorrowingLimit("12").Append().
						FlavorQuotas,
					utiltesting.MakeFlavorQuotas("two").
						Resource(corev1.ResourceCPU, "12").
						FlavorQuotas,
				).Cohort("test-cohort").ClusterQueue,
			secondaryClusterQueue: utiltesting.MakeClusterQueue("test-secondary-clusterqueue").
				ResourceGroup(
					utiltesting.MakeFlavorQuotas("one").
						Resource(corev1.ResourceCPU, "12").
						FlavorQuotas,
				).
				Cohort("test-cohort").
				Obj(),
			secondaryClusterQueueUsage: resources.FlavorResourceQuantities{
				{Flavor: "one", Resource: corev1.ResourceCPU}: 10_000,
			},
			wantRepMode: Preempt,
			wantAssignment: Assignment{
				Borrowing: true,
				PodSets: []PodSetAssignment{{
					Name: "main",
					Flavors: ResourceAssignment{
						corev1.ResourceCPU: {Name: "one", Mode: Preempt, TriedFlavorIdx: 0},
					},
					Status: &Status{
						reasons: []string{"insufficient unused quota for cpu in flavor one, 10 more needed"},
					},
					Requests: corev1.ResourceList{
						corev1.ResourceCPU: resource.MustParse("12"),
					},
					Count: 1,
				}},
				Usage: resources.FlavorResourceQuantities{
					{Flavor: "one", Resource: corev1.ResourceCPU}: 12_000,
				},
			},
		},
		"when borrowing while preemption is needed for flavor one, no borrowingLimit; WhenCanBorrow=Borrow": {
			wlPods: []kueue.PodSet{
				*utiltesting.MakePodSet("main", 1).
					Request(corev1.ResourceCPU, "12").
					Obj(),
			},
			clusterQueue: utiltesting.MakeClusterQueue("test-clusterqueue").
				Preemption(kueue.ClusterQueuePreemption{
					ReclaimWithinCohort: kueue.PreemptionPolicyLowerPriority,
					BorrowWithinCohort: &kueue.BorrowWithinCohort{
						Policy: kueue.BorrowWithinCohortPolicyLowerPriority,
					},
				}).
				FlavorFungibility(kueue.FlavorFungibility{
					WhenCanBorrow:  kueue.Borrow,
					WhenCanPreempt: kueue.Preempt,
				}).
				ResourceGroup(
					utiltesting.MakeFlavorQuotas("one").
						Resource(corev1.ResourceCPU, "0").
						FlavorQuotas,
					utiltesting.MakeFlavorQuotas("two").
						Resource(corev1.ResourceCPU, "12").
						FlavorQuotas,
				).Cohort("test-cohort").ClusterQueue,
			secondaryClusterQueue: utiltesting.MakeClusterQueue("test-secondary-clusterqueue").
				ResourceGroup(
					utiltesting.MakeFlavorQuotas("one").
						Resource(corev1.ResourceCPU, "12").
						FlavorQuotas,
				).
				Cohort("test-cohort").
				Obj(),
			secondaryClusterQueueUsage: resources.FlavorResourceQuantities{
				{Flavor: "one", Resource: corev1.ResourceCPU}: 10_000,
			},
			wantRepMode: Preempt,
			wantAssignment: Assignment{
				Borrowing: true,
				PodSets: []PodSetAssignment{{
					Name: "main",
					Flavors: ResourceAssignment{
						corev1.ResourceCPU: {Name: "one", Mode: Preempt, TriedFlavorIdx: 0},
					},
					Status: &Status{
						reasons: []string{"insufficient unused quota for cpu in flavor one, 10 more needed"},
					},
					Requests: corev1.ResourceList{
						corev1.ResourceCPU: resource.MustParse("12"),
					},
					Count: 1,
				}},
				Usage: resources.FlavorResourceQuantities{
					{Flavor: "one", Resource: corev1.ResourceCPU}: 12_000,
				},
			},
		},
		"when borrowing while preemption is needed for flavor one; WhenCanBorrow=TryNextFlavor": {
			wlPods: []kueue.PodSet{
				*utiltesting.MakePodSet("main", 1).
					Request(corev1.ResourceCPU, "12").
					Obj(),
			},
			clusterQueue: utiltesting.MakeClusterQueue("test-clusterqueue").
				Preemption(kueue.ClusterQueuePreemption{
					ReclaimWithinCohort: kueue.PreemptionPolicyLowerPriority,
					BorrowWithinCohort: &kueue.BorrowWithinCohort{
						Policy: kueue.BorrowWithinCohortPolicyLowerPriority,
					},
				}).
				FlavorFungibility(kueue.FlavorFungibility{
					WhenCanBorrow:  kueue.TryNextFlavor,
					WhenCanPreempt: kueue.Preempt,
				}).
				ResourceGroup(
					utiltesting.MakeFlavorQuotas("one").
						ResourceQuotaWrapper(corev1.ResourceCPU).NominalQuota("0").BorrowingLimit("12").Append().
						FlavorQuotas,
					utiltesting.MakeFlavorQuotas("two").
						Resource(corev1.ResourceCPU, "12").
						FlavorQuotas,
				).Cohort("test-cohort").
				ClusterQueue,
			secondaryClusterQueue: utiltesting.MakeClusterQueue("test-secondary-clusterqueue").
				ResourceGroup(
					utiltesting.MakeFlavorQuotas("one").
						Resource(corev1.ResourceCPU, "12").
						FlavorQuotas,
				).
				Cohort("test-cohort").
				Obj(),
			wantRepMode: Fit,
			wantAssignment: Assignment{
				PodSets: []PodSetAssignment{{
					Name: "main",
					Flavors: ResourceAssignment{
						corev1.ResourceCPU: {Name: "two", Mode: Fit, TriedFlavorIdx: -1},
					},
					Requests: corev1.ResourceList{
						corev1.ResourceCPU: resource.MustParse("12"),
					},
					Count: 1,
				}},
				Usage: resources.FlavorResourceQuantities{
					{Flavor: "two", Resource: corev1.ResourceCPU}: 12_000,
				},
			},
		},
		"when borrowing while preemption is needed, but borrowingLimit exceeds the quota available in the cohort": {
			wlPods: []kueue.PodSet{
				*utiltesting.MakePodSet("main", 1).
					Request(corev1.ResourceCPU, "12").
					Obj(),
			},
			clusterQueue: utiltesting.MakeClusterQueue("test-clusterqueue").
				Preemption(kueue.ClusterQueuePreemption{
					ReclaimWithinCohort: kueue.PreemptionPolicyLowerPriority,
					BorrowWithinCohort: &kueue.BorrowWithinCohort{
						Policy: kueue.BorrowWithinCohortPolicyLowerPriority,
					},
				}).
				ResourceGroup(
					utiltesting.MakeFlavorQuotas("one").
						ResourceQuotaWrapper(corev1.ResourceCPU).NominalQuota("0").BorrowingLimit("12").Append().
						FlavorQuotas,
				).Cohort("test-cohort").ClusterQueue,
			secondaryClusterQueue: utiltesting.MakeClusterQueue("test-secondary-clusterqueue").
				ResourceGroup(
					utiltesting.MakeFlavorQuotas("one").
						Resource(corev1.ResourceCPU, "11").
						FlavorQuotas,
				).
				Cohort("test-cohort").
				Obj(),
			secondaryClusterQueueUsage: resources.FlavorResourceQuantities{
				{Flavor: "one", Resource: corev1.ResourceCPU}: 10_000,
			},
			wantRepMode: NoFit,
			wantAssignment: Assignment{
				Usage: resources.FlavorResourceQuantities{},
				PodSets: []PodSetAssignment{
					{
						Name: "main",
						Status: &Status{
							reasons: []string{"insufficient quota for cpu in flavor one, request > maximum capacity (12 > 11)"},
						},
						Requests: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("12"),
						},
						Count: 1,
					},
				},
			},
		},
		"lend try next flavor, found the second flavor": {
			wlPods: []kueue.PodSet{
				*utiltesting.MakePodSet("main", 1).
					Request(corev1.ResourceCPU, "9").
					Obj(),
			},
			clusterQueue: utiltesting.MakeClusterQueue("test-clusterqueue").
				FlavorFungibility(kueue.FlavorFungibility{
					WhenCanBorrow:  kueue.TryNextFlavor,
					WhenCanPreempt: kueue.TryNextFlavor},
				).
				ResourceGroup(
					utiltesting.MakeFlavorQuotas("one").
						Resource(corev1.ResourcePods, "10").
						ResourceQuotaWrapper(corev1.ResourceCPU).NominalQuota("10").LendingLimit("1").Append().
						FlavorQuotas,
					utiltesting.MakeFlavorQuotas("two").
						Resource(corev1.ResourcePods, "10").
						ResourceQuotaWrapper(corev1.ResourceCPU).NominalQuota("10").LendingLimit("0").Append().
						FlavorQuotas,
				).Cohort("test-cohort").
				ClusterQueue,
			clusterQueueUsage: resources.FlavorResourceQuantities{
				{Flavor: "one", Resource: corev1.ResourceCPU}: 2_000,
			},
			secondaryClusterQueue: utiltesting.MakeClusterQueue("test-secondary-clusterqueue").
				ResourceGroup(
					utiltesting.MakeFlavorQuotas("one").
						Resource(corev1.ResourceCPU, "1").
						FlavorQuotas,
				).
				Cohort("test-cohort").
				Obj(),
			wantRepMode: Fit,
			wantAssignment: Assignment{
				PodSets: []PodSetAssignment{{
					Name: "main",
					Flavors: ResourceAssignment{
						corev1.ResourceCPU:  {Name: "two", Mode: Fit, TriedFlavorIdx: -1},
						corev1.ResourcePods: {Name: "two", Mode: Fit, TriedFlavorIdx: -1},
					},
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("9"),
						corev1.ResourcePods: resource.MustParse("1"),
					},
					Count: 1,
				}},
				Usage: resources.FlavorResourceQuantities{
					{Flavor: "two", Resource: corev1.ResourceCPU}:  9_000,
					{Flavor: "two", Resource: corev1.ResourcePods}: 1,
				},
			},
		},
		"lend try next flavor, found the first flavor": {
			wlPods: []kueue.PodSet{
				*utiltesting.MakePodSet("main", 1).
					Request(corev1.ResourceCPU, "9").
					Obj(),
			},
			clusterQueue: utiltesting.MakeClusterQueue("test-clusterqueue").
				FlavorFungibility(kueue.FlavorFungibility{WhenCanBorrow: kueue.TryNextFlavor, WhenCanPreempt: kueue.TryNextFlavor}).
				ResourceGroup(
					utiltesting.MakeFlavorQuotas("one").
						Resource(corev1.ResourcePods, "10").
						ResourceQuotaWrapper(corev1.ResourceCPU).NominalQuota("10").LendingLimit("1").Append().
						FlavorQuotas,
					utiltesting.MakeFlavorQuotas("two").
						Resource(corev1.ResourcePods, "10").
						ResourceQuotaWrapper(corev1.ResourceCPU).NominalQuota("1").LendingLimit("0").Append().
						FlavorQuotas,
				).Cohort("test-cohort").
				ClusterQueue,
			clusterQueueUsage: resources.FlavorResourceQuantities{
				{Flavor: "one", Resource: corev1.ResourceCPU}: 2_000,
			},
			secondaryClusterQueue: utiltesting.MakeClusterQueue("test-secondary-clusterqueue").
				ResourceGroup(
					utiltesting.MakeFlavorQuotas("one").
						Resource(corev1.ResourceCPU, "1").
						FlavorQuotas,
				).
				Cohort("test-cohort").
				Obj(),
			wantRepMode: Fit,
			wantAssignment: Assignment{
				PodSets: []PodSetAssignment{{
					Name: "main",
					Flavors: ResourceAssignment{
						corev1.ResourceCPU:  {Name: "one", Mode: Fit, TriedFlavorIdx: -1},
						corev1.ResourcePods: {Name: "one", Mode: Fit, TriedFlavorIdx: -1},
					},
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("9"),
						corev1.ResourcePods: resource.MustParse("1"),
					},
					Count: 1,
				}},
				Borrowing: true,
				Usage: resources.FlavorResourceQuantities{
					{Flavor: "one", Resource: corev1.ResourceCPU}:  9_000,
					{Flavor: "one", Resource: corev1.ResourcePods}: 1,
				},
			},
		},
		"quota exhausted, but can preempt in cohort and ClusterQueue": {
			wlPods: []kueue.PodSet{
				*utiltesting.MakePodSet("main", 1).
					Request(corev1.ResourceCPU, "9").
					Obj(),
			},
			clusterQueue: utiltesting.MakeClusterQueue("test-clusterqueue").
				ResourceGroup(
					utiltesting.MakeFlavorQuotas("one").
						Resource(corev1.ResourcePods, "10").
						ResourceQuotaWrapper(corev1.ResourceCPU).NominalQuota("10").LendingLimit("0").Append().
						FlavorQuotas,
				).Cohort("test-cohort").ClusterQueue,
			clusterQueueUsage: resources.FlavorResourceQuantities{
				{Flavor: "one", Resource: corev1.ResourceCPU}: 2_000,
			},
			secondaryClusterQueue: utiltesting.MakeClusterQueue("test-secondary-clusterqueue").
				ResourceGroup(
					utiltesting.MakeFlavorQuotas("one").
						Resource(corev1.ResourcePods, "0").
						Resource(corev1.ResourceCPU, "0").
						FlavorQuotas,
				).
				Cohort("test-cohort").
				Obj(),

			wantRepMode: Preempt,
			wantAssignment: Assignment{
				PodSets: []PodSetAssignment{{
					Name: "main",
					Flavors: ResourceAssignment{
						corev1.ResourceCPU:  {Name: "one", Mode: Preempt, TriedFlavorIdx: -1},
						corev1.ResourcePods: {Name: "one", Mode: Fit, TriedFlavorIdx: -1},
					},
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("9"),
						corev1.ResourcePods: resource.MustParse("1"),
					},
					Status: &Status{
						reasons: []string{"insufficient unused quota for cpu in flavor one, 1 more needed"},
					},
					Count: 1,
				}},
				Borrowing: true,
				Usage: resources.FlavorResourceQuantities{
					{Flavor: "one", Resource: corev1.ResourceCPU}:  9_000,
					{Flavor: "one", Resource: corev1.ResourcePods}: 1,
				},
			},
		},
		"when borrowing while preemption is needed for flavor one, fair sharing enabled, reclaimWithinCohort=Any": {
			enableFairSharing: true,
			wlPods: []kueue.PodSet{
				*utiltesting.MakePodSet("main", 1).
					Request(corev1.ResourceCPU, "12").
					Obj(),
			},
			clusterQueue: utiltesting.MakeClusterQueue("test-clusterqueue").
				Preemption(kueue.ClusterQueuePreemption{ReclaimWithinCohort: kueue.PreemptionPolicyAny}).
				FlavorFungibility(kueue.FlavorFungibility{WhenCanBorrow: kueue.Borrow, WhenCanPreempt: kueue.Preempt}).
				ResourceGroup(
					utiltesting.MakeFlavorQuotas("one").
						Resource(corev1.ResourceCPU, "0").
						FlavorQuotas,
					utiltesting.MakeFlavorQuotas("two").
						Resource(corev1.ResourceCPU, "12").
						FlavorQuotas,
				).Cohort("test-cohort").ClusterQueue,
			secondaryClusterQueue: utiltesting.MakeClusterQueue("test-secondary-clusterqueue").
				ResourceGroup(
					utiltesting.MakeFlavorQuotas("one").
						Resource(corev1.ResourceCPU, "12").
						FlavorQuotas,
				).
				Cohort("test-cohort").
				Obj(),
			secondaryClusterQueueUsage: resources.FlavorResourceQuantities{
				{Flavor: "one", Resource: corev1.ResourceCPU}: 10_000,
			},
			wantRepMode: Preempt,
			wantAssignment: Assignment{
				Borrowing: true,
				PodSets: []PodSetAssignment{{
					Name: "main",
					Flavors: ResourceAssignment{
						corev1.ResourceCPU: {Name: "one", Mode: Preempt, TriedFlavorIdx: 0},
					},
					Status: &Status{
						reasons: []string{"insufficient unused quota for cpu in flavor one, 10 more needed"},
					},
					Requests: corev1.ResourceList{
						corev1.ResourceCPU: resource.MustParse("12"),
					},
					Count: 1,
				}},
				Usage: resources.FlavorResourceQuantities{
					{Flavor: "one", Resource: corev1.ResourceCPU}: 12_000,
				},
			},
		},
		"when borrowing while preemption is needed for flavor one, fair sharing enabled, reclaimWithinCohor=Never": {
			enableFairSharing: true,
			wlPods: []kueue.PodSet{
				*utiltesting.MakePodSet("main", 1).
					Request(corev1.ResourceCPU, "12").
					Obj(),
			},
			clusterQueue: utiltesting.MakeClusterQueue("test-clusterqueue").
				Preemption(kueue.ClusterQueuePreemption{ReclaimWithinCohort: kueue.PreemptionPolicyNever}).
				FlavorFungibility(kueue.FlavorFungibility{WhenCanBorrow: kueue.Borrow, WhenCanPreempt: kueue.Preempt}).
				ResourceGroup(
					utiltesting.MakeFlavorQuotas("one").
						Resource(corev1.ResourceCPU, "0").
						FlavorQuotas,
					utiltesting.MakeFlavorQuotas("two").
						Resource(corev1.ResourceCPU, "12").
						FlavorQuotas,
				).Cohort("test-cohort").ClusterQueue,
			secondaryClusterQueue: utiltesting.MakeClusterQueue("test-secondary-clusterqueue").
				ResourceGroup(
					utiltesting.MakeFlavorQuotas("one").
						Resource(corev1.ResourceCPU, "12").
						FlavorQuotas,
				).
				Cohort("test-cohort").
				Obj(),
			secondaryClusterQueueUsage: resources.FlavorResourceQuantities{
				{Flavor: "one", Resource: corev1.ResourceCPU}: 10_000,
			},
			wantRepMode: Fit,
			wantAssignment: Assignment{
				PodSets: []PodSetAssignment{{
					Name: "main",
					Flavors: ResourceAssignment{
						corev1.ResourceCPU: {Name: "two", Mode: Fit, TriedFlavorIdx: -1},
					},
					Requests: corev1.ResourceList{
						corev1.ResourceCPU: resource.MustParse("12"),
					},
					Count: 1,
				}},
				Usage: resources.FlavorResourceQuantities{
					{Flavor: "two", Resource: corev1.ResourceCPU}: 12_000,
				},
			},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)
			if tc.disableLendingLimit {
				features.SetFeatureGateDuringTest(t, features.LendingLimit, false)
			}
			log := testr.NewWithOptions(t, testr.Options{
				Verbosity: 2,
			})
			wlInfo := workload.NewInfo(&kueue.Workload{
				Spec: kueue.WorkloadSpec{
					PodSets: tc.wlPods,
				},
				Status: kueue.WorkloadStatus{
					ReclaimablePods: tc.wlReclaimablePods,
				},
			})

			cache := cache.New(utiltesting.NewFakeClient())
			if err := cache.AddClusterQueue(ctx, &tc.clusterQueue); err != nil {
				t.Fatalf("Failed to add CQ to cache")
			}
			if tc.secondaryClusterQueue != nil {
				if err := cache.AddClusterQueue(ctx, tc.secondaryClusterQueue); err != nil {
					t.Fatalf("Failed to add secondary CQ to cache")
				}
			}
			for _, rf := range resourceFlavors {
				cache.AddOrUpdateResourceFlavor(rf)
			}

			if err := cache.AddOrUpdateCohort(utiltesting.MakeCohort(tc.clusterQueue.Spec.Cohort).Obj()); err != nil {
				t.Fatalf("Failed to create a cohort")
			}

			snapshot, err := cache.Snapshot(ctx)
			if err != nil {
				t.Fatalf("unexpected error while building snapshot: %v", err)
			}
			clusterQueue := snapshot.ClusterQueues[tc.clusterQueue.Name]

			if clusterQueue == nil {
				t.Fatalf("Failed to create CQ snapshot")
			}
			if tc.clusterQueueUsage != nil {
				clusterQueue.AddUsage(tc.clusterQueueUsage)
			}

			if tc.secondaryClusterQueue != nil {
				secondaryClusterQueue := snapshot.ClusterQueues[tc.secondaryClusterQueue.Name]
				if secondaryClusterQueue == nil {
					t.Fatalf("Failed to create secondary CQ snapshot")
				}
				secondaryClusterQueue.AddUsage(tc.secondaryClusterQueueUsage)
			}

			flvAssigner := New(wlInfo, clusterQueue, resourceFlavors, tc.enableFairSharing, &testOracle{})
			assignment := flvAssigner.Assign(log, nil)
			if repMode := assignment.RepresentativeMode(); repMode != tc.wantRepMode {
				t.Errorf("e.assignFlavors(_).RepresentativeMode()=%s, want %s", repMode, tc.wantRepMode)
			}

			if diff := cmp.Diff(tc.wantAssignment, assignment, cmpopts.IgnoreUnexported(Assignment{}, FlavorAssignment{}), cmpopts.IgnoreFields(Assignment{}, "LastState")); diff != "" {
				t.Errorf("Unexpected assignment (-want,+got):\n%s", diff)
			}
		})
	}
}

// We have 3 flavors: uno, due, tre. Each has 10 compute and 10 gpu.
// These FlavorResources are provided by test-clusterqueue, and made
// available to its Cohort.
func TestReclaimBeforePriorityPreemption(t *testing.T) {
	type rfMap = map[corev1.ResourceName]kueue.ResourceFlavorReference
	cases := map[string]struct {
		workloadRequests       *utiltesting.PodSetWrapper
		testClusterQueueUsage  resources.FlavorResourceQuantities
		otherClusterQueueUsage resources.FlavorResourceQuantities
		flavorFungibility      *kueue.FlavorFungibility
		wantMode               FlavorAssignmentMode
		wantAssigment          rfMap
	}{
		"Select first flavor which fits": {
			workloadRequests: utiltesting.MakePodSet("main", 1).Request("gpu", "10"),
			testClusterQueueUsage: resources.FlavorResourceQuantities{
				{Flavor: "uno", Resource: "gpu"}: 1,
			},
			otherClusterQueueUsage: resources.FlavorResourceQuantities{
				{Flavor: "due", Resource: "gpu"}: 1,
			},
			wantMode:      Fit,
			wantAssigment: rfMap{"gpu": "tre"},
		},
		"Select first flavor where gpu reclamation is possible": {
			workloadRequests: utiltesting.MakePodSet("main", 1).Request("gpu", "10"),
			testClusterQueueUsage: resources.FlavorResourceQuantities{
				{Flavor: "uno", Resource: "gpu"}: 1,
			},
			otherClusterQueueUsage: resources.FlavorResourceQuantities{
				{Flavor: "due", Resource: "gpu"}: 1,
				{Flavor: "tre", Resource: "gpu"}: 1,
			},
			wantMode:      Preempt,
			wantAssigment: rfMap{"gpu": "due"},
		},
		"Select first flavor when flavor fungibility is disabled": {
			workloadRequests: utiltesting.MakePodSet("main", 1).Request("gpu", "10"),
			testClusterQueueUsage: resources.FlavorResourceQuantities{
				{Flavor: "uno", Resource: "gpu"}: 1,
			},
			otherClusterQueueUsage: resources.FlavorResourceQuantities{
				{Flavor: "due", Resource: "gpu"}: 1,
				{Flavor: "tre", Resource: "gpu"}: 1,
			},
			flavorFungibility: &kueue.FlavorFungibility{
				WhenCanPreempt: kueue.Preempt,
			},
			wantMode:      Preempt,
			wantAssigment: rfMap{"gpu": "uno"},
		},
		"Select first flavor where priority based preemption is possible": {
			workloadRequests: utiltesting.MakePodSet("main", 1).Request("gpu", "10"),
			testClusterQueueUsage: resources.FlavorResourceQuantities{
				{Flavor: "uno", Resource: "gpu"}: 1,
				{Flavor: "due", Resource: "gpu"}: 1,
				{Flavor: "tre", Resource: "gpu"}: 1,
			},
			wantMode:      Preempt,
			wantAssigment: rfMap{"gpu": "uno"},
		},
		"Select second flavor where gpu reclamation is possible, as compute Fits": {
			workloadRequests: utiltesting.MakePodSet("main", 1).Request("gpu", "10").Request("compute", "10"),
			testClusterQueueUsage: resources.FlavorResourceQuantities{
				{Flavor: "uno", Resource: "gpu"}:     1,
				{Flavor: "uno", Resource: "compute"}: 1,
				{Flavor: "due", Resource: "compute"}: 1,
			},
			otherClusterQueueUsage: resources.FlavorResourceQuantities{
				{Flavor: "due", Resource: "gpu"}: 1,
				{Flavor: "tre", Resource: "gpu"}: 1,
			},
			wantMode:      Preempt,
			wantAssigment: rfMap{"gpu": "tre", "compute": "tre"},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)
			resourceFlavors := map[kueue.ResourceFlavorReference]*kueue.ResourceFlavor{
				"uno": utiltesting.MakeResourceFlavor("uno").Obj(),
				"due": utiltesting.MakeResourceFlavor("due").Obj(),
				"tre": utiltesting.MakeResourceFlavor("tre").Obj(),
			}
			testCq := utiltesting.MakeClusterQueue("test-clusterqueue").
				Cohort("cohort").
				Preemption(kueue.ClusterQueuePreemption{
					WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
					ReclaimWithinCohort: kueue.PreemptionPolicyLowerPriority,
				}).
				FlavorFungibility(kueue.FlavorFungibility{
					WhenCanPreempt: kueue.TryNextFlavor,
				}).
				ResourceGroup(
					utiltesting.MakeFlavorQuotas("uno").Resource("compute", "10").Resource("gpu", "10").FlavorQuotas,
					utiltesting.MakeFlavorQuotas("due").Resource("compute", "10").Resource("gpu", "10").FlavorQuotas,
					utiltesting.MakeFlavorQuotas("tre").Resource("compute", "10").Resource("gpu", "10").FlavorQuotas,
				).ClusterQueue
			otherCq := utiltesting.MakeClusterQueue("other-clusterqueue").
				Cohort("cohort").
				ResourceGroup(
					utiltesting.MakeFlavorQuotas("uno").Resource("compute", "0").Resource("gpu", "0").FlavorQuotas,
					utiltesting.MakeFlavorQuotas("due").Resource("compute", "0").Resource("gpu", "0").FlavorQuotas,
					utiltesting.MakeFlavorQuotas("tre").Resource("compute", "0").Resource("gpu", "0").FlavorQuotas,
				).ClusterQueue

			wlInfo := workload.NewInfo(&kueue.Workload{
				Spec: kueue.WorkloadSpec{
					PodSets: []kueue.PodSet{
						tc.workloadRequests.PodSet,
					},
				},
			})

			if tc.flavorFungibility != nil {
				testCq.Spec.FlavorFungibility = tc.flavorFungibility
			}

			cache := cache.New(utiltesting.NewFakeClient())
			if err := cache.AddClusterQueue(ctx, &testCq); err != nil {
				t.Fatalf("Failed to add CQ to cache")
			}
			if err := cache.AddClusterQueue(ctx, &otherCq); err != nil {
				t.Fatalf("Failed to add CQ to cache")
			}
			for _, rf := range resourceFlavors {
				cache.AddOrUpdateResourceFlavor(rf)
			}

			snapshot, err := cache.Snapshot(ctx)
			if err != nil {
				t.Fatalf("unexpected error while building snapshot: %v", err)
			}
			otherClusterQueue := snapshot.ClusterQueues["other-clusterqueue"]
			otherClusterQueue.AddUsage(tc.otherClusterQueueUsage)

			testClusterQueue := snapshot.ClusterQueues["test-clusterqueue"]
			testClusterQueue.AddUsage(tc.testClusterQueueUsage)

			flvAssigner := New(wlInfo, testClusterQueue, resourceFlavors, false, &testOracle{})
			log := testr.NewWithOptions(t, testr.Options{Verbosity: 2})
			assignment := flvAssigner.Assign(log, nil)
			if gotRepMode := assignment.RepresentativeMode(); gotRepMode != tc.wantMode {
				t.Errorf("Unexpected RepresentativeMode. got %s, want %s", gotRepMode, tc.wantMode)
			}
			if len(assignment.PodSets[0].Flavors) != len(tc.wantAssigment) {
				t.Errorf("Wrong number of flavors. got %d, want %d", len(assignment.PodSets[0].Flavors), len(tc.wantAssigment))
			}
			for resourceName, wantFlavor := range tc.wantAssigment {
				if gotFlavor := assignment.PodSets[0].Flavors[resourceName].Name; gotFlavor != wantFlavor {
					t.Errorf("Unexpected flavor. got %s, want %s", gotFlavor, wantFlavor)
				}
			}
		})
	}
}

// Tests the case where the Cache's flavors and CQs flavors
// fall out of sync, so that the CQ has flavors which no-longer exist.
func TestDeletedFlavors(t *testing.T) {
	cases := map[string]struct {
		wlPods            []kueue.PodSet
		wlReclaimablePods []kueue.ReclaimablePod
		clusterQueue      kueue.ClusterQueue
		wantRepMode       FlavorAssignmentMode
		wantAssignment    Assignment
	}{
		"multiple flavors, skip missing ResourceFlavor": {
			wlPods: []kueue.PodSet{
				*utiltesting.MakePodSet("main", 1).
					Request(corev1.ResourceCPU, "3").
					Obj(),
			},
			clusterQueue: utiltesting.MakeClusterQueue("test-clusterqueue").
				ResourceGroup(
					utiltesting.MakeFlavorQuotas("deleted-flavor").
						ResourceQuotaWrapper(corev1.ResourceCPU).NominalQuota("4").Append().
						FlavorQuotas,
					utiltesting.MakeFlavorQuotas("flavor").
						ResourceQuotaWrapper(corev1.ResourceCPU).NominalQuota("4").Append().
						FlavorQuotas,
				).ClusterQueue,
			wantRepMode: Fit,
			wantAssignment: Assignment{
				PodSets: []PodSetAssignment{{
					Name: "main",
					Flavors: ResourceAssignment{
						corev1.ResourceCPU: {Name: "flavor", Mode: Fit, TriedFlavorIdx: -1},
					},
					Requests: corev1.ResourceList{
						corev1.ResourceCPU: resource.MustParse("3"),
					},
					Count: 1,
				}},
				Usage: resources.FlavorResourceQuantities{
					{Flavor: "flavor", Resource: corev1.ResourceCPU}: 3_000,
				},
			},
		},
		"flavor not found": {
			wlPods: []kueue.PodSet{
				*utiltesting.MakePodSet("main", 1).
					Request(corev1.ResourceCPU, "1").
					Obj(),
			},
			clusterQueue: utiltesting.MakeClusterQueue("test-clusterqueue").
				ResourceGroup(
					utiltesting.MakeFlavorQuotas("deleted-flavor").
						ResourceQuotaWrapper(corev1.ResourceCPU).NominalQuota("4").Append().
						FlavorQuotas,
				).ClusterQueue,
			wantAssignment: Assignment{
				PodSets: []PodSetAssignment{{
					Name: "main",
					Requests: corev1.ResourceList{
						corev1.ResourceCPU: resource.MustParse("1"),
					},
					Status: &Status{
						reasons: []string{"flavor deleted-flavor not found"},
					},
					Count: 1,
				}},
				Usage: resources.FlavorResourceQuantities{},
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)
			log := testr.NewWithOptions(t, testr.Options{
				Verbosity: 2,
			})
			wlInfo := workload.NewInfo(&kueue.Workload{
				Spec: kueue.WorkloadSpec{
					PodSets: tc.wlPods,
				},
				Status: kueue.WorkloadStatus{
					ReclaimablePods: tc.wlReclaimablePods,
				},
			})

			cache := cache.New(utiltesting.NewFakeClient())
			if err := cache.AddClusterQueue(ctx, &tc.clusterQueue); err != nil {
				t.Fatalf("Failed to add CQ to cache")
			}

			flavorMap := map[kueue.ResourceFlavorReference]*kueue.ResourceFlavor{
				"flavor":         utiltesting.MakeResourceFlavor("flavor").Obj(),
				"deleted-flavor": utiltesting.MakeResourceFlavor("deleted-flavor").Obj(),
			}

			// we have to add the deleted flavor to the cache before snapshot,
			// or else snapshot will fail
			for _, flavor := range flavorMap {
				cache.AddOrUpdateResourceFlavor(flavor)
			}
			snapshot, err := cache.Snapshot(ctx)
			if err != nil {
				t.Fatalf("unexpected error while building snapshot: %v", err)
			}
			clusterQueue := snapshot.ClusterQueues[tc.clusterQueue.Name]
			if clusterQueue == nil {
				t.Fatalf("Failed to create CQ snapshot")
			}

			// and we delete it
			cache.DeleteResourceFlavor(flavorMap["deleted-flavor"])
			delete(flavorMap, "deleted-flavor")

			flvAssigner := New(wlInfo, clusterQueue, flavorMap, false, &testOracle{})

			assignment := flvAssigner.Assign(log, nil)
			if repMode := assignment.RepresentativeMode(); repMode != tc.wantRepMode {
				t.Errorf("e.assignFlavors(_).RepresentativeMode()=%s, want %s", repMode, tc.wantRepMode)
			}

			if diff := cmp.Diff(tc.wantAssignment, assignment, cmpopts.IgnoreUnexported(Assignment{}, FlavorAssignment{}), cmpopts.IgnoreFields(Assignment{}, "LastState")); diff != "" {
				t.Errorf("Unexpected assignment (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestLastAssignmentOutdated(t *testing.T) {
	type args struct {
		wl *workload.Info
		cq *cache.ClusterQueueSnapshot
	}
	tests := []struct {
		name string
		args args
		want bool
	}{
		{
			name: "Cluster queue allocatableResourceIncreasedGen increased",
			args: args{
				wl: &workload.Info{
					LastAssignment: &workload.AssignmentClusterQueueState{
						ClusterQueueGeneration: 0,
					},
				},
				cq: &cache.ClusterQueueSnapshot{
					AllocatableResourceGeneration: 1,
				},
			},
			want: true,
		},
		{
			name: "AllocatableResourceGeneration not increased",
			args: args{
				wl: &workload.Info{
					LastAssignment: &workload.AssignmentClusterQueueState{
						ClusterQueueGeneration: 0,
					},
				},
				cq: &cache.ClusterQueueSnapshot{
					AllocatableResourceGeneration: 0,
				},
			},
			want: false,
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := lastAssignmentOutdated(tt.args.wl, tt.args.cq); got != tt.want {
				t.Errorf("LastAssignmentOutdated() = %v, want %v", got, tt.want)
			}
		})
	}
}
