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
	"fmt"
	"testing"

	"github.com/go-logr/logr/testr"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/utils/ptr"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/cache"
	"sigs.k8s.io/kueue/pkg/features"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	"sigs.k8s.io/kueue/pkg/workload"
)

func TestAssignFlavors(t *testing.T) {
	defaultFlavorFungibility := kueue.FlavorFungibility{
		WhenCanBorrow:  kueue.Borrow,
		WhenCanPreempt: kueue.TryNextFlavor,
	}
	resourceFlavors := map[kueue.ResourceFlavorReference]*kueue.ResourceFlavor{
		"default": {
			ObjectMeta: metav1.ObjectMeta{Name: "default"},
		},
		"one":   utiltesting.MakeResourceFlavor("one").Label("type", "one").Obj(),
		"two":   utiltesting.MakeResourceFlavor("two").Label("type", "two").Obj(),
		"b_one": utiltesting.MakeResourceFlavor("b_one").Label("b_type", "one").Obj(),
		"b_two": utiltesting.MakeResourceFlavor("b_two").Label("b_type", "two").Obj(),
		"tainted": utiltesting.MakeResourceFlavor("tainted").
			Taint(corev1.Taint{
				Key:    "instance",
				Value:  "spot",
				Effect: corev1.TaintEffectNoSchedule,
			}).Obj(),
	}

	cases := map[string]struct {
		wlPods             []kueue.PodSet
		wlReclaimablePods  []kueue.ReclaimablePod
		clusterQueue       cache.ClusterQueue
		wantRepMode        FlavorAssignmentMode
		wantAssignment     Assignment
		enableLendingLimit bool
	}{
		"single flavor, fits": {
			wlPods: []kueue.PodSet{
				*utiltesting.MakePodSet("main", 1).
					Request(corev1.ResourceCPU, "1").
					Request(corev1.ResourceMemory, "1Mi").
					Obj(),
			},
			clusterQueue: cache.ClusterQueue{
				ResourceGroups: []cache.ResourceGroup{{
					CoveredResources: sets.New(corev1.ResourceCPU, corev1.ResourceMemory),
					Flavors: []cache.FlavorQuotas{{
						Name: "default",
						Resources: map[corev1.ResourceName]*cache.ResourceQuota{
							corev1.ResourceCPU:    {Nominal: 1000},
							corev1.ResourceMemory: {Nominal: 2 * utiltesting.Mi},
						},
					}},
				}},
			},
			wantRepMode: Fit,
			wantAssignment: Assignment{
				PodSets: []PodSetAssignment{
					{
						Name: "main",
						Flavors: ResourceAssignment{
							corev1.ResourceCPU:    {Name: "default", Mode: Fit},
							corev1.ResourceMemory: {Name: "default", Mode: Fit},
						},
						Requests: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("1000m"),
							corev1.ResourceMemory: resource.MustParse("1Mi"),
						},
						Count: 1,
					},
				},
				Usage: cache.FlavorResourceQuantities{
					"default": map[corev1.ResourceName]int64{
						corev1.ResourceCPU:    1000,
						corev1.ResourceMemory: 1 * 1024 * 1024,
					},
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
			clusterQueue: cache.ClusterQueue{
				ResourceGroups: []cache.ResourceGroup{{
					CoveredResources: sets.New(corev1.ResourceCPU),
					Flavors: []cache.FlavorQuotas{{
						Name: "tainted",
						Resources: map[corev1.ResourceName]*cache.ResourceQuota{
							corev1.ResourceCPU: {Nominal: 4000},
						},
					}},
				}},
			},
			wantRepMode: Fit,
			wantAssignment: Assignment{
				PodSets: []PodSetAssignment{{
					Name: "main",
					Flavors: ResourceAssignment{
						corev1.ResourceCPU: {Name: "tainted", Mode: Fit},
					},
					Requests: corev1.ResourceList{
						corev1.ResourceCPU: resource.MustParse("1000m"),
					},
					Count: 1,
				}},
				Usage: cache.FlavorResourceQuantities{
					"tainted": {
						corev1.ResourceCPU: 1000,
					},
				},
			},
		},
		"single flavor, used resources, doesn't fit": {
			wlPods: []kueue.PodSet{
				*utiltesting.MakePodSet("main", 1).
					Request(corev1.ResourceCPU, "2").
					Obj(),
			},
			clusterQueue: cache.ClusterQueue{
				ResourceGroups: []cache.ResourceGroup{{
					CoveredResources: sets.New(corev1.ResourceCPU),
					Flavors: []cache.FlavorQuotas{{
						Name: "default",
						Resources: map[corev1.ResourceName]*cache.ResourceQuota{
							corev1.ResourceCPU: {Nominal: 4000},
						},
					}},
				}},
				Usage: cache.FlavorResourceQuantities{
					"default": {corev1.ResourceCPU: 3_000},
				},
			},
			wantRepMode: Preempt,
			wantAssignment: Assignment{
				PodSets: []PodSetAssignment{{
					Name: "main",
					Flavors: ResourceAssignment{
						corev1.ResourceCPU: {Name: "default", Mode: Preempt},
					},
					Requests: corev1.ResourceList{
						corev1.ResourceCPU: resource.MustParse("2000m"),
					},
					Status: &Status{
						reasons: []string{"insufficient unused quota for cpu in flavor default, 1 more needed"},
					},
					Count: 1,
				}},
				Usage: cache.FlavorResourceQuantities{
					"default": {
						corev1.ResourceCPU: 2000,
					},
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
			clusterQueue: cache.ClusterQueue{
				ResourceGroups: []cache.ResourceGroup{
					{
						CoveredResources: sets.New(corev1.ResourceCPU),
						Flavors: []cache.FlavorQuotas{
							{
								Name: "one",
								Resources: map[corev1.ResourceName]*cache.ResourceQuota{
									corev1.ResourceCPU: {Nominal: 2000},
								},
							},
							{
								Name: "two",
								Resources: map[corev1.ResourceName]*cache.ResourceQuota{
									corev1.ResourceCPU: {Nominal: 4000},
								},
							},
						},
					},
					{
						CoveredResources: sets.New(corev1.ResourceMemory),
						Flavors: []cache.FlavorQuotas{
							{
								Name: "b_one",
								Resources: map[corev1.ResourceName]*cache.ResourceQuota{
									corev1.ResourceMemory: {Nominal: utiltesting.Gi},
								},
							},
							{
								Name: "b_two",
								Resources: map[corev1.ResourceName]*cache.ResourceQuota{
									corev1.ResourceMemory: {Nominal: 5 * utiltesting.Gi},
								},
							},
						},
					},
				},
			},
			wantRepMode: Fit,
			wantAssignment: Assignment{
				PodSets: []PodSetAssignment{{
					Name: "main",
					Flavors: ResourceAssignment{
						corev1.ResourceCPU:    {Name: "two", Mode: Fit},
						corev1.ResourceMemory: {Name: "b_one", Mode: Fit},
					},
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("3000m"),
						corev1.ResourceMemory: resource.MustParse("10Mi"),
					},
					Count: 1,
				}},
				Usage: cache.FlavorResourceQuantities{
					"two": map[corev1.ResourceName]int64{
						corev1.ResourceCPU: 3000,
					},
					"b_one": map[corev1.ResourceName]int64{
						corev1.ResourceMemory: 10 * 1024 * 1024,
					},
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
			clusterQueue: cache.ClusterQueue{
				ResourceGroups: []cache.ResourceGroup{
					{
						CoveredResources: sets.New(corev1.ResourceCPU),
						Flavors: []cache.FlavorQuotas{{
							Name: "one",
							Resources: map[corev1.ResourceName]*cache.ResourceQuota{
								corev1.ResourceCPU: {Nominal: 3000},
							},
						}},
					},
					{
						CoveredResources: sets.New(corev1.ResourceMemory),
						Flavors: []cache.FlavorQuotas{{
							Name: "b_one",
							Resources: map[corev1.ResourceName]*cache.ResourceQuota{
								corev1.ResourceMemory: {Nominal: utiltesting.Mi},
							},
						}},
					},
				},
				Usage: cache.FlavorResourceQuantities{
					"one": {corev1.ResourceCPU: 1000},
				},
			},
			wantAssignment: Assignment{
				PodSets: []PodSetAssignment{{
					Name: "main",
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("3000m"),
						corev1.ResourceMemory: resource.MustParse("10Mi"),
					},
					Status: &Status{
						reasons: []string{
							"insufficient quota for memory in flavor b_one in ClusterQueue",
						},
					},
					Count: 1,
				}},
				Usage: cache.FlavorResourceQuantities{},
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
			clusterQueue: cache.ClusterQueue{
				ResourceGroups: []cache.ResourceGroup{
					{
						CoveredResources: sets.New(corev1.ResourceCPU, corev1.ResourceMemory),
						Flavors: []cache.FlavorQuotas{
							{
								Name: "one",
								Resources: map[corev1.ResourceName]*cache.ResourceQuota{
									corev1.ResourceCPU:    {Nominal: 2000},
									corev1.ResourceMemory: {Nominal: utiltesting.Gi},
								},
							},
							{
								Name: "two",
								Resources: map[corev1.ResourceName]*cache.ResourceQuota{
									corev1.ResourceCPU:    {Nominal: 4000},
									corev1.ResourceMemory: {Nominal: 15 * utiltesting.Mi},
								},
							},
						},
					},
					{
						CoveredResources: sets.New[corev1.ResourceName]("example.com/gpu"),
						Flavors: []cache.FlavorQuotas{
							{
								Name: "b_one",
								Resources: map[corev1.ResourceName]*cache.ResourceQuota{
									"example.com/gpu": {Nominal: 4},
								},
							},
							{
								Name: "b_two",
								Resources: map[corev1.ResourceName]*cache.ResourceQuota{
									"example.com/gpu": {Nominal: 2},
								},
							},
						},
					},
				},
			},
			wantRepMode: Fit,
			wantAssignment: Assignment{
				PodSets: []PodSetAssignment{{
					Name: "main",
					Flavors: ResourceAssignment{
						corev1.ResourceCPU:    {Name: "two", Mode: Fit},
						corev1.ResourceMemory: {Name: "two", Mode: Fit},
						"example.com/gpu":     {Name: "b_one", Mode: Fit},
					},
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("3000m"),
						corev1.ResourceMemory: resource.MustParse("10Mi"),
						"example.com/gpu":     resource.MustParse("3"),
					},
					Count: 1,
				}},
				Usage: cache.FlavorResourceQuantities{
					"two": map[corev1.ResourceName]int64{
						corev1.ResourceCPU:    3000,
						corev1.ResourceMemory: 10 * 1024 * 1024,
					},
					"b_one": map[corev1.ResourceName]int64{
						"example.com/gpu": 3,
					},
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
			clusterQueue: cache.ClusterQueue{
				ResourceGroups: []cache.ResourceGroup{
					{
						CoveredResources: sets.New(corev1.ResourceCPU, corev1.ResourceMemory),
						Flavors: []cache.FlavorQuotas{
							{
								Name: "one",
								Resources: map[corev1.ResourceName]*cache.ResourceQuota{
									corev1.ResourceCPU:    {Nominal: 2000},
									corev1.ResourceMemory: {Nominal: utiltesting.Gi},
								},
							},
							{
								Name: "two",
								Resources: map[corev1.ResourceName]*cache.ResourceQuota{
									corev1.ResourceCPU:    {Nominal: 4000},
									corev1.ResourceMemory: {Nominal: 15 * utiltesting.Mi},
								},
							},
						},
					},
					{
						CoveredResources: sets.New[corev1.ResourceName]("example.com/gpu"),
						Flavors: []cache.FlavorQuotas{{
							Name: "b_one",
							Resources: map[corev1.ResourceName]*cache.ResourceQuota{
								"example.com/gpu": {Nominal: 4},
							},
						}},
					},
				},
				Usage: cache.FlavorResourceQuantities{
					"two": {corev1.ResourceMemory: 10 * utiltesting.Mi},
				},
				Cohort: &cache.Cohort{
					RequestableResources: cache.FlavorResourceQuantities{
						"one": {
							corev1.ResourceCPU:    2000,
							corev1.ResourceMemory: utiltesting.Gi,
						},
						"two": {
							corev1.ResourceCPU:    4000,
							corev1.ResourceMemory: 15 * utiltesting.Mi,
						},
						"b_one": {
							"example.com/gpu": 4,
						},
					},
					Usage: cache.FlavorResourceQuantities{
						"two": {
							corev1.ResourceMemory: 10 * utiltesting.Mi,
						},
						"b_one": {
							"example.com/gpu": 2,
						},
					},
				},
			},
			wantRepMode: Preempt,
			wantAssignment: Assignment{
				PodSets: []PodSetAssignment{{
					Name: "main",
					Flavors: ResourceAssignment{
						corev1.ResourceCPU:    {Name: "two", Mode: Fit},
						corev1.ResourceMemory: {Name: "two", Mode: Preempt},
						"example.com/gpu":     {Name: "b_one", Mode: Preempt},
					},
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("3000m"),
						corev1.ResourceMemory: resource.MustParse("10Mi"),
						"example.com/gpu":     resource.MustParse("3"),
					},
					Status: &Status{
						reasons: []string{
							"insufficient unused quota in cohort for cpu in flavor one, 1 more needed",
							"insufficient unused quota in cohort for memory in flavor two, 5Mi more needed",
							"insufficient unused quota in cohort for example.com/gpu in flavor b_one, 1 more needed",
						},
					},
					Count: 1,
				}},
				Usage: cache.FlavorResourceQuantities{
					"two": map[corev1.ResourceName]int64{
						corev1.ResourceCPU:    3000,
						corev1.ResourceMemory: 10 * 1024 * 1024,
					},
					"b_one": map[corev1.ResourceName]int64{
						"example.com/gpu": 3,
					},
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
			clusterQueue: cache.ClusterQueue{
				ResourceGroups: []cache.ResourceGroup{
					{
						CoveredResources: sets.New(corev1.ResourceCPU, corev1.ResourceMemory),
						Flavors: []cache.FlavorQuotas{
							{
								Name: "one",
								Resources: map[corev1.ResourceName]*cache.ResourceQuota{
									corev1.ResourceCPU:    {Nominal: 2000},
									corev1.ResourceMemory: {Nominal: utiltesting.Gi},
								},
							},
							{
								Name: "two",
								Resources: map[corev1.ResourceName]*cache.ResourceQuota{
									corev1.ResourceCPU:    {Nominal: 4000},
									corev1.ResourceMemory: {Nominal: 5 * utiltesting.Mi},
								},
							},
						},
					},
				},
			},
			wantAssignment: Assignment{
				PodSets: []PodSetAssignment{{
					Name: "main",
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("3000m"),
						corev1.ResourceMemory: resource.MustParse("10Mi"),
					},
					Status: &Status{
						reasons: []string{
							"insufficient quota for cpu in flavor one in ClusterQueue",
							"insufficient quota for memory in flavor two in ClusterQueue",
						},
					},
					Count: 1,
				}},
				Usage: cache.FlavorResourceQuantities{},
			},
		},
		"multiple flavors, fits while skipping tainted flavor": {
			wlPods: []kueue.PodSet{
				*utiltesting.MakePodSet("main", 1).
					Request(corev1.ResourceCPU, "3").
					Obj(),
			},
			clusterQueue: cache.ClusterQueue{
				ResourceGroups: []cache.ResourceGroup{
					{
						CoveredResources: sets.New(corev1.ResourceCPU),
						Flavors: []cache.FlavorQuotas{
							{
								Name: "tainted",
								Resources: map[corev1.ResourceName]*cache.ResourceQuota{
									corev1.ResourceCPU: {Nominal: 4000},
								},
							},
							{
								Name: "two",
								Resources: map[corev1.ResourceName]*cache.ResourceQuota{
									corev1.ResourceCPU: {Nominal: 4000},
								},
							},
						},
					},
				},
			},
			wantRepMode: Fit,
			wantAssignment: Assignment{
				PodSets: []PodSetAssignment{{
					Name: "main",
					Flavors: ResourceAssignment{
						corev1.ResourceCPU: {Name: "two", Mode: Fit},
					},
					Requests: corev1.ResourceList{
						corev1.ResourceCPU: resource.MustParse("3000m"),
					},
					Count: 1,
				}},
				Usage: cache.FlavorResourceQuantities{
					"two": map[corev1.ResourceName]int64{
						corev1.ResourceCPU: 3000,
					},
				},
			},
		},
		"multiple flavors, skip missing ResourceFlavor": {
			wlPods: []kueue.PodSet{
				*utiltesting.MakePodSet("main", 1).
					Request(corev1.ResourceCPU, "3").
					Obj(),
			},
			clusterQueue: cache.ClusterQueue{
				ResourceGroups: []cache.ResourceGroup{
					{
						CoveredResources: sets.New(corev1.ResourceCPU),
						Flavors: []cache.FlavorQuotas{
							{
								Name: "nonexistent-flavor",
								Resources: map[corev1.ResourceName]*cache.ResourceQuota{
									corev1.ResourceCPU: {Nominal: 4000},
								},
							},
							{
								Name: "two",
								Resources: map[corev1.ResourceName]*cache.ResourceQuota{
									corev1.ResourceCPU: {Nominal: 4000},
								},
							},
						},
					},
				},
			},
			wantRepMode: Fit,
			wantAssignment: Assignment{
				PodSets: []PodSetAssignment{{
					Name: "main",
					Flavors: ResourceAssignment{
						corev1.ResourceCPU: {Name: "two", Mode: Fit},
					},
					Requests: corev1.ResourceList{
						corev1.ResourceCPU: resource.MustParse("3000m"),
					},
					Count: 1,
				}},
				Usage: cache.FlavorResourceQuantities{
					"two": map[corev1.ResourceName]int64{
						corev1.ResourceCPU: 3000,
					},
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
			clusterQueue: cache.ClusterQueue{
				ResourceGroups: []cache.ResourceGroup{
					{
						CoveredResources: sets.New(corev1.ResourceCPU),
						Flavors: []cache.FlavorQuotas{
							{
								Name: "non-existent",
								Resources: map[corev1.ResourceName]*cache.ResourceQuota{
									corev1.ResourceCPU: {Nominal: 4000},
								},
							},
							{
								Name: "one",
								Resources: map[corev1.ResourceName]*cache.ResourceQuota{
									corev1.ResourceCPU: {Nominal: 4000},
								},
							},
							{
								Name: "two",
								Resources: map[corev1.ResourceName]*cache.ResourceQuota{
									corev1.ResourceCPU: {Nominal: 4000},
								},
							},
						},
					},
				},
			},
			wantRepMode: Fit,
			wantAssignment: Assignment{
				PodSets: []PodSetAssignment{{
					Name: "main",
					Flavors: ResourceAssignment{
						corev1.ResourceCPU: {Name: "two", Mode: Fit},
					},
					Requests: corev1.ResourceList{
						corev1.ResourceCPU: resource.MustParse("1000m"),
					},
					Count: 1,
				}},
				Usage: cache.FlavorResourceQuantities{
					"two": map[corev1.ResourceName]int64{
						corev1.ResourceCPU: 1000,
					},
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
			clusterQueue: cache.ClusterQueue{
				ResourceGroups: []cache.ResourceGroup{
					{
						CoveredResources: sets.New(corev1.ResourceCPU, corev1.ResourceMemory),
						Flavors: []cache.FlavorQuotas{
							{
								Name: "one",
								Resources: map[corev1.ResourceName]*cache.ResourceQuota{
									corev1.ResourceCPU:    {Nominal: 4000},
									corev1.ResourceMemory: {Nominal: utiltesting.Gi},
								},
							},
							{
								Name: "two",
								Resources: map[corev1.ResourceName]*cache.ResourceQuota{
									corev1.ResourceCPU:    {Nominal: 4000},
									corev1.ResourceMemory: {Nominal: utiltesting.Gi},
								},
							},
						},
					},
				},
			},
			wantRepMode: Fit,
			wantAssignment: Assignment{
				PodSets: []PodSetAssignment{{
					Name: "main",
					Flavors: ResourceAssignment{
						corev1.ResourceCPU:    {Name: "two", Mode: Fit},
						corev1.ResourceMemory: {Name: "two", Mode: Fit},
					},
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1000m"),
						corev1.ResourceMemory: resource.MustParse("1Mi"),
					},
					Count: 1,
				}},
				Usage: cache.FlavorResourceQuantities{
					"two": map[corev1.ResourceName]int64{
						corev1.ResourceCPU:    1000,
						corev1.ResourceMemory: 1 * 1024 * 1024,
					},
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
			clusterQueue: cache.ClusterQueue{
				ResourceGroups: []cache.ResourceGroup{
					{
						CoveredResources: sets.New(corev1.ResourceCPU),
						Flavors: []cache.FlavorQuotas{
							{
								Name: "one",
								Resources: map[corev1.ResourceName]*cache.ResourceQuota{
									corev1.ResourceCPU: {Nominal: 4000},
								},
							},
							{
								Name: "two",
								Resources: map[corev1.ResourceName]*cache.ResourceQuota{
									corev1.ResourceCPU: {Nominal: 4000},
								},
							},
						},
					},
				},
			},
			wantRepMode: Fit,
			wantAssignment: Assignment{
				PodSets: []PodSetAssignment{{
					Name: "main",
					Flavors: ResourceAssignment{
						corev1.ResourceCPU: {Name: "one", Mode: Fit},
					},
					Requests: corev1.ResourceList{
						corev1.ResourceCPU: resource.MustParse("1000m"),
					},
					Count: 1,
				}},
				Usage: cache.FlavorResourceQuantities{
					"one": map[corev1.ResourceName]int64{
						corev1.ResourceCPU: 1000,
					},
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
			clusterQueue: cache.ClusterQueue{
				ResourceGroups: []cache.ResourceGroup{
					{
						CoveredResources: sets.New(corev1.ResourceCPU),
						Flavors: []cache.FlavorQuotas{
							{
								Name: "one",
								Resources: map[corev1.ResourceName]*cache.ResourceQuota{
									corev1.ResourceCPU: {Nominal: 4000},
								},
							},
							{
								Name: "two",
								Resources: map[corev1.ResourceName]*cache.ResourceQuota{
									corev1.ResourceCPU: {Nominal: 4000},
								},
							},
						},
					},
				},
			},
			wantAssignment: Assignment{
				PodSets: []PodSetAssignment{{
					Name: "main",
					Requests: corev1.ResourceList{
						corev1.ResourceCPU: resource.MustParse("1000m"),
					},
					Status: &Status{
						reasons: []string{
							"flavor one doesn't match node affinity",
							"flavor two doesn't match node affinity",
						},
					},
					Count: 1,
				}},
				Usage: cache.FlavorResourceQuantities{},
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
			clusterQueue: cache.ClusterQueue{
				ResourceGroups: []cache.ResourceGroup{
					{
						CoveredResources: sets.New(corev1.ResourceCPU),
						Flavors: []cache.FlavorQuotas{
							{
								Name: "one",
								Resources: map[corev1.ResourceName]*cache.ResourceQuota{
									corev1.ResourceCPU: {Nominal: 4000},
								},
							},
							{
								Name: "two",
								Resources: map[corev1.ResourceName]*cache.ResourceQuota{
									corev1.ResourceCPU: {Nominal: 10_000},
								},
							},
						},
					},
				},
			},
			wantRepMode: Fit,
			wantAssignment: Assignment{
				PodSets: []PodSetAssignment{
					{
						Name: "driver",
						Flavors: ResourceAssignment{
							corev1.ResourceCPU: {Name: "two", Mode: Fit},
						},
						Requests: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("5000m"),
						},
						Count: 1,
					},
					{
						Name: "worker",
						Flavors: ResourceAssignment{
							corev1.ResourceCPU: {Name: "one", Mode: Fit},
						},
						Requests: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("3000m"),
						},
						Count: 1,
					},
				},
				Usage: cache.FlavorResourceQuantities{
					"one": map[corev1.ResourceName]int64{
						corev1.ResourceCPU: 3000,
					},
					"two": map[corev1.ResourceName]int64{
						corev1.ResourceCPU: 5000,
					},
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
			clusterQueue: cache.ClusterQueue{
				ResourceGroups: []cache.ResourceGroup{{
					CoveredResources: sets.New(corev1.ResourceCPU, corev1.ResourceMemory),
					Flavors: []cache.FlavorQuotas{{
						Name: "default",
						Resources: map[corev1.ResourceName]*cache.ResourceQuota{
							corev1.ResourceCPU:    {Nominal: 2000, BorrowingLimit: ptr.To[int64](98_000)},
							corev1.ResourceMemory: {Nominal: 2 * utiltesting.Gi},
						},
					}},
				}},
				Cohort: &cache.Cohort{
					RequestableResources: cache.FlavorResourceQuantities{
						"default": {
							corev1.ResourceCPU:    200_000,
							corev1.ResourceMemory: 200 * utiltesting.Gi,
						},
					},
				},
			},
			wantRepMode: Fit,
			wantAssignment: Assignment{
				PodSets: []PodSetAssignment{
					{
						Name: "driver",
						Flavors: ResourceAssignment{
							corev1.ResourceCPU:    {Name: "default", Mode: Fit},
							corev1.ResourceMemory: {Name: "default", Mode: Fit},
						},
						Requests: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("4000m"),
							corev1.ResourceMemory: resource.MustParse("1Gi"),
						},
						Count: 1,
					},
					{
						Name: "worker",
						Flavors: ResourceAssignment{
							corev1.ResourceCPU:    {Name: "default", Mode: Fit},
							corev1.ResourceMemory: {Name: "default", Mode: Fit},
						},
						Requests: corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("6000m"),
							corev1.ResourceMemory: resource.MustParse("4Gi"),
						},
						Count: 1,
					},
				},
				Borrowing: true,
				Usage: cache.FlavorResourceQuantities{
					"default": map[corev1.ResourceName]int64{
						corev1.ResourceCPU:    10000,
						corev1.ResourceMemory: 5 * 1024 * 1024 * 1024,
					},
				},
			},
		},
		"not enough space to borrow": {
			wlPods: []kueue.PodSet{
				*utiltesting.MakePodSet("main", 1).
					Request(corev1.ResourceCPU, "2").
					Obj(),
			},
			clusterQueue: cache.ClusterQueue{
				ResourceGroups: []cache.ResourceGroup{{
					CoveredResources: sets.New(corev1.ResourceCPU),
					Flavors: []cache.FlavorQuotas{{
						Name: "one",
						Resources: map[corev1.ResourceName]*cache.ResourceQuota{
							corev1.ResourceCPU: {Nominal: 1000},
						},
					}},
				}},
				Cohort: &cache.Cohort{
					RequestableResources: cache.FlavorResourceQuantities{
						"one": {corev1.ResourceCPU: 10_000},
					},
					Usage: cache.FlavorResourceQuantities{
						"one": {corev1.ResourceCPU: 9_000},
					},
				},
			},
			wantAssignment: Assignment{
				PodSets: []PodSetAssignment{{
					Name: "main",
					Requests: corev1.ResourceList{
						corev1.ResourceCPU: resource.MustParse("2000m"),
					},
					Status: &Status{
						reasons: []string{"insufficient unused quota in cohort for cpu in flavor one, 1 more needed"},
					},
					Count: 1,
				}},
				Usage: cache.FlavorResourceQuantities{},
			},
		},
		"past max, but can preempt in ClusterQueue": {
			wlPods: []kueue.PodSet{
				*utiltesting.MakePodSet("main", 1).
					Request(corev1.ResourceCPU, "2").
					Obj(),
			},
			clusterQueue: cache.ClusterQueue{
				ResourceGroups: []cache.ResourceGroup{{
					CoveredResources: sets.New(corev1.ResourceCPU),
					Flavors: []cache.FlavorQuotas{{
						Name: "one",
						Resources: map[corev1.ResourceName]*cache.ResourceQuota{
							corev1.ResourceCPU: {Nominal: 2000, BorrowingLimit: ptr.To[int64](8_000)},
						},
					}},
				}},
				Usage: cache.FlavorResourceQuantities{
					"one": {corev1.ResourceCPU: 9_000},
				},
				Cohort: &cache.Cohort{
					RequestableResources: cache.FlavorResourceQuantities{
						"one": {corev1.ResourceCPU: 100_000},
					},
					Usage: cache.FlavorResourceQuantities{
						"one": {corev1.ResourceCPU: 9_000},
					},
				},
			},
			wantRepMode: Preempt,
			wantAssignment: Assignment{
				PodSets: []PodSetAssignment{{
					Name: "main",
					Flavors: ResourceAssignment{
						corev1.ResourceCPU: {Name: "one", Mode: Preempt},
					},
					Requests: corev1.ResourceList{
						corev1.ResourceCPU: resource.MustParse("2000m"),
					},

					Status: &Status{
						reasons: []string{"borrowing limit for cpu in flavor one exceeded"},
					},
					Count: 1,
				}},
				Usage: cache.FlavorResourceQuantities{
					"one": map[corev1.ResourceName]int64{
						corev1.ResourceCPU: 2000,
					},
				},
			},
		},
		"past min, but can preempt in ClusterQueue": {
			wlPods: []kueue.PodSet{
				*utiltesting.MakePodSet("main", 1).
					Request(corev1.ResourceCPU, "2").
					Obj(),
			},
			clusterQueue: cache.ClusterQueue{
				ResourceGroups: []cache.ResourceGroup{{
					CoveredResources: sets.New(corev1.ResourceCPU),
					Flavors: []cache.FlavorQuotas{{
						Name: "one",
						Resources: map[corev1.ResourceName]*cache.ResourceQuota{
							corev1.ResourceCPU: {Nominal: 2000},
						},
					}},
				}},
				Usage: cache.FlavorResourceQuantities{
					"one": {corev1.ResourceCPU: 1_000},
				},
			},
			wantRepMode: Preempt,
			wantAssignment: Assignment{
				PodSets: []PodSetAssignment{{
					Name: "main",
					Flavors: ResourceAssignment{
						corev1.ResourceCPU: {Name: "one", Mode: Preempt},
					},
					Requests: corev1.ResourceList{
						corev1.ResourceCPU: resource.MustParse("2000m"),
					},
					Status: &Status{
						reasons: []string{"insufficient unused quota for cpu in flavor one, 1 more needed"},
					},
					Count: 1,
				}},
				Usage: cache.FlavorResourceQuantities{
					"one": map[corev1.ResourceName]int64{
						corev1.ResourceCPU: 2000,
					},
				},
			},
		},
		"past min, but can preempt in cohort and ClusterQueue": {
			wlPods: []kueue.PodSet{
				*utiltesting.MakePodSet("main", 1).
					Request(corev1.ResourceCPU, "2").
					Obj(),
			},
			clusterQueue: cache.ClusterQueue{
				ResourceGroups: []cache.ResourceGroup{{
					CoveredResources: sets.New(corev1.ResourceCPU),
					Flavors: []cache.FlavorQuotas{{
						Name: "one",
						Resources: map[corev1.ResourceName]*cache.ResourceQuota{
							corev1.ResourceCPU: {Nominal: 3000},
						},
					}},
				}},
				Usage: cache.FlavorResourceQuantities{
					"one": {corev1.ResourceCPU: 2_000},
				},
				Cohort: &cache.Cohort{
					RequestableResources: cache.FlavorResourceQuantities{
						"one": {corev1.ResourceCPU: 10_000},
					},
					Usage: cache.FlavorResourceQuantities{
						"one": {corev1.ResourceCPU: 10_000},
					},
				},
			},
			wantRepMode: Preempt,
			wantAssignment: Assignment{
				PodSets: []PodSetAssignment{{
					Name: "main",
					Flavors: ResourceAssignment{
						corev1.ResourceCPU: {Name: "one", Mode: Preempt},
					},
					Requests: corev1.ResourceList{
						corev1.ResourceCPU: resource.MustParse("2000m"),
					},
					Status: &Status{
						reasons: []string{"insufficient unused quota in cohort for cpu in flavor one, 2 more needed"},
					},
					Count: 1,
				}},
				Usage: cache.FlavorResourceQuantities{
					"one": map[corev1.ResourceName]int64{
						corev1.ResourceCPU: 2000,
					},
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
			clusterQueue: cache.ClusterQueue{
				ResourceGroups: []cache.ResourceGroup{{
					CoveredResources: sets.New(corev1.ResourceCPU),
					Flavors: []cache.FlavorQuotas{
						{
							Name: "one",
							Resources: map[corev1.ResourceName]*cache.ResourceQuota{
								corev1.ResourceCPU: {Nominal: 4000},
							},
						},
						{
							Name: "two",
							Resources: map[corev1.ResourceName]*cache.ResourceQuota{
								corev1.ResourceCPU: {Nominal: 4000},
							},
						},
					},
				}},
				Usage: cache.FlavorResourceQuantities{
					"one": {corev1.ResourceCPU: 3000},
					"two": {corev1.ResourceCPU: 3000},
				},
			},
			wantRepMode: Preempt,
			wantAssignment: Assignment{
				PodSets: []PodSetAssignment{{
					Name: "main",
					Flavors: ResourceAssignment{
						corev1.ResourceCPU: {Name: "two", Mode: Preempt},
					},
					Requests: corev1.ResourceList{
						corev1.ResourceCPU: resource.MustParse("2000m"),
					},
					Status: &Status{
						reasons: []string{
							"flavor one doesn't match node affinity",
							"insufficient unused quota for cpu in flavor two, 1 more needed",
						},
					},
					Count: 1,
				}},
				Usage: cache.FlavorResourceQuantities{
					"two": map[corev1.ResourceName]int64{
						corev1.ResourceCPU: 2000,
					},
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
			clusterQueue: cache.ClusterQueue{
				ResourceGroups: []cache.ResourceGroup{{
					CoveredResources: sets.New(corev1.ResourceCPU),
					Flavors: []cache.FlavorQuotas{
						{
							Name: "one",
							Resources: map[corev1.ResourceName]*cache.ResourceQuota{
								corev1.ResourceCPU: {Nominal: 4000},
							},
						},
						{
							Name: "tainted",
							Resources: map[corev1.ResourceName]*cache.ResourceQuota{
								corev1.ResourceCPU: {Nominal: 10_000},
							},
						},
					},
				}},
				Usage: cache.FlavorResourceQuantities{
					"one":     {corev1.ResourceCPU: 3000},
					"tainted": {corev1.ResourceCPU: 3000},
				},
			},
			wantRepMode: Preempt,
			wantAssignment: Assignment{
				PodSets: []PodSetAssignment{
					{
						Name: "launcher",
						Flavors: ResourceAssignment{
							corev1.ResourceCPU: {Name: "one", Mode: Preempt},
						},
						Requests: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("2000m"),
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
							corev1.ResourceCPU: {Name: "tainted", Mode: Preempt},
						},
						Requests: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("10000m"),
						},
						Status: &Status{
							reasons: []string{
								"insufficient quota for cpu in flavor one in ClusterQueue",
								"insufficient unused quota for cpu in flavor tainted, 3 more needed",
							},
						},
						Count: 10,
					},
				},
				Usage: cache.FlavorResourceQuantities{
					"one": map[corev1.ResourceName]int64{
						corev1.ResourceCPU: 2000,
					},
					"tainted": map[corev1.ResourceName]int64{
						corev1.ResourceCPU: 10000,
					},
				},
			},
		},
		"resource not listed in clusterQueue": {
			wlPods: []kueue.PodSet{
				*utiltesting.MakePodSet("main", 1).
					Request("example.com/gpu", "2").
					Obj(),
			},
			clusterQueue: cache.ClusterQueue{
				ResourceGroups: []cache.ResourceGroup{{
					CoveredResources: sets.New(corev1.ResourceCPU),
					Flavors: []cache.FlavorQuotas{{
						Name: "one",
						Resources: map[corev1.ResourceName]*cache.ResourceQuota{
							corev1.ResourceCPU: {Nominal: 4000},
						},
					}},
				}},
			},
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
				Usage: cache.FlavorResourceQuantities{},
			},
		},
		"flavor not found": {
			wlPods: []kueue.PodSet{
				*utiltesting.MakePodSet("main", 1).
					Request(corev1.ResourceCPU, "1").
					Obj(),
			},
			clusterQueue: cache.ClusterQueue{
				ResourceGroups: []cache.ResourceGroup{{
					CoveredResources: sets.New(corev1.ResourceCPU),
					Flavors: []cache.FlavorQuotas{{
						Name: "nonexistent-flavor",
						Resources: map[corev1.ResourceName]*cache.ResourceQuota{
							corev1.ResourceCPU: {Nominal: 1000},
						},
					}},
				}},
			},
			wantAssignment: Assignment{
				PodSets: []PodSetAssignment{{
					Name: "main",
					Requests: corev1.ResourceList{
						corev1.ResourceCPU: resource.MustParse("1000m"),
					},
					Status: &Status{
						reasons: []string{"flavor nonexistent-flavor not found"},
					},
					Count: 1,
				}},
				Usage: cache.FlavorResourceQuantities{},
			},
		},
		"num pods fit": {
			wlPods: []kueue.PodSet{
				*utiltesting.MakePodSet("main", 3).
					Request(corev1.ResourceCPU, "1").
					Obj(),
			},
			clusterQueue: cache.ClusterQueue{
				ResourceGroups: []cache.ResourceGroup{{
					CoveredResources: sets.New(corev1.ResourceCPU, corev1.ResourcePods),
					Flavors: []cache.FlavorQuotas{{
						Name: "default",
						Resources: map[corev1.ResourceName]*cache.ResourceQuota{
							corev1.ResourcePods: {Nominal: 3},
							corev1.ResourceCPU:  {Nominal: 10000},
						},
					}},
				}},
			},
			wantAssignment: Assignment{
				PodSets: []PodSetAssignment{{
					Name: "main",
					Flavors: ResourceAssignment{

						corev1.ResourceCPU:  &FlavorAssignment{Name: "default", Mode: Fit},
						corev1.ResourcePods: &FlavorAssignment{Name: "default", Mode: Fit},
					},
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("3000m"),
						corev1.ResourcePods: resource.MustParse("3"),
					},
					Count: 3,
				}},
				Usage: cache.FlavorResourceQuantities{
					"default": map[corev1.ResourceName]int64{
						corev1.ResourcePods: 3,
						corev1.ResourceCPU:  3000,
					},
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
			clusterQueue: cache.ClusterQueue{
				ResourceGroups: []cache.ResourceGroup{{
					CoveredResources: sets.New(corev1.ResourceCPU, corev1.ResourcePods),
					Flavors: []cache.FlavorQuotas{{
						Name: "default",
						Resources: map[corev1.ResourceName]*cache.ResourceQuota{
							corev1.ResourcePods: {Nominal: 2},
							corev1.ResourceCPU:  {Nominal: 10000},
						},
					}},
				}},
			},
			wantAssignment: Assignment{
				PodSets: []PodSetAssignment{{
					Name: "main",
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("3000m"),
						corev1.ResourcePods: resource.MustParse("3"),
					},
					Status: &Status{
						reasons: []string{fmt.Sprintf("insufficient quota for %s in flavor default in ClusterQueue", corev1.ResourcePods)},
					},
					Count: 3,
				}},
				Usage: cache.FlavorResourceQuantities{},
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
			clusterQueue: cache.ClusterQueue{
				ResourceGroups: []cache.ResourceGroup{{
					CoveredResources: sets.New(corev1.ResourceCPU, corev1.ResourcePods),
					Flavors: []cache.FlavorQuotas{{
						Name: "default",
						Resources: map[corev1.ResourceName]*cache.ResourceQuota{
							corev1.ResourcePods: {Nominal: 3},
							corev1.ResourceCPU:  {Nominal: 10000},
						},
					}},
				}},
			},
			wantAssignment: Assignment{
				PodSets: []PodSetAssignment{{
					Name: "main",
					Flavors: ResourceAssignment{

						corev1.ResourceCPU:  &FlavorAssignment{Name: "default", Mode: Fit},
						corev1.ResourcePods: &FlavorAssignment{Name: "default", Mode: Fit},
					},
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("3000m"),
						corev1.ResourcePods: resource.MustParse("3"),
					},
					Count: 3,
				}},
				Usage: cache.FlavorResourceQuantities{
					"default": map[corev1.ResourceName]int64{
						corev1.ResourcePods: 3,
						corev1.ResourceCPU:  3000,
					},
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
			clusterQueue: cache.ClusterQueue{
				FlavorFungibility: kueue.FlavorFungibility{
					WhenCanBorrow:  kueue.Borrow,
					WhenCanPreempt: kueue.Preempt,
				},
				ResourceGroups: []cache.ResourceGroup{{
					CoveredResources: sets.New(corev1.ResourceCPU, corev1.ResourcePods),
					Flavors: []cache.FlavorQuotas{{
						Name: "one",
						Resources: map[corev1.ResourceName]*cache.ResourceQuota{
							corev1.ResourcePods: {Nominal: 10},
							corev1.ResourceCPU:  {Nominal: 10000},
						},
					}, {
						Name: "two",
						Resources: map[corev1.ResourceName]*cache.ResourceQuota{
							corev1.ResourcePods: {Nominal: 10},
							corev1.ResourceCPU:  {Nominal: 10000},
						},
					}},
				}},
				Usage: cache.FlavorResourceQuantities{
					"one": {corev1.ResourceCPU: 2000},
				},
			},
			wantRepMode: Preempt,
			wantAssignment: Assignment{
				PodSets: []PodSetAssignment{{
					Name: "main",
					Flavors: ResourceAssignment{
						corev1.ResourceCPU:  {Name: "one", Mode: Preempt},
						corev1.ResourcePods: {Name: "one", Mode: Fit},
					},
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("9000m"),
						corev1.ResourcePods: resource.MustParse("1"),
					},
					Status: &Status{
						reasons: []string{"insufficient unused quota for cpu in flavor one, 1 more needed"},
					},
					Count: 1,
				}},
				Usage: cache.FlavorResourceQuantities{"one": {"cpu": 9000, "pods": 1}},
			},
		},
		"preempt try next flavor": {
			wlPods: []kueue.PodSet{
				*utiltesting.MakePodSet("main", 1).
					Request(corev1.ResourceCPU, "9").
					Obj(),
			},
			clusterQueue: cache.ClusterQueue{
				FlavorFungibility: defaultFlavorFungibility,
				ResourceGroups: []cache.ResourceGroup{{
					CoveredResources: sets.New(corev1.ResourceCPU, corev1.ResourcePods),
					Flavors: []cache.FlavorQuotas{{
						Name: "one",
						Resources: map[corev1.ResourceName]*cache.ResourceQuota{
							corev1.ResourcePods: {Nominal: 10},
							corev1.ResourceCPU:  {Nominal: 10000},
						},
					}, {
						Name: "two",
						Resources: map[corev1.ResourceName]*cache.ResourceQuota{
							corev1.ResourcePods: {Nominal: 10},
							corev1.ResourceCPU:  {Nominal: 10000},
						},
					}},
				}},
				Usage: cache.FlavorResourceQuantities{
					"one": {corev1.ResourceCPU: 2000},
				},
			},
			wantRepMode: Fit,
			wantAssignment: Assignment{
				PodSets: []PodSetAssignment{{
					Name: "main",
					Flavors: ResourceAssignment{
						corev1.ResourceCPU:  {Name: "two", Mode: Fit},
						corev1.ResourcePods: {Name: "two", Mode: Fit},
					},
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("9000m"),
						corev1.ResourcePods: resource.MustParse("1"),
					},
					Count: 1,
				}},
				Usage: cache.FlavorResourceQuantities{"two": {"cpu": 9000, "pods": 1}},
			},
		},
		"borrow try next flavor, found the first flavor": {
			wlPods: []kueue.PodSet{
				*utiltesting.MakePodSet("main", 1).
					Request(corev1.ResourceCPU, "9").
					Obj(),
			},
			clusterQueue: cache.ClusterQueue{
				Cohort: &cache.Cohort{
					Usage: cache.FlavorResourceQuantities{
						"one": {corev1.ResourceCPU: 2000},
					},
					RequestableResources: cache.FlavorResourceQuantities{
						"one": {corev1.ResourceCPU: 11000, corev1.ResourcePods: 10},
						"two": {corev1.ResourceCPU: 1000, corev1.ResourcePods: 10},
					},
				},
				FlavorFungibility: kueue.FlavorFungibility{
					WhenCanBorrow:  kueue.TryNextFlavor,
					WhenCanPreempt: kueue.TryNextFlavor,
				},
				ResourceGroups: []cache.ResourceGroup{{
					CoveredResources: sets.New(corev1.ResourceCPU, corev1.ResourcePods),
					Flavors: []cache.FlavorQuotas{{
						Name: "one",
						Resources: map[corev1.ResourceName]*cache.ResourceQuota{
							corev1.ResourcePods: {Nominal: 10},
							corev1.ResourceCPU:  {Nominal: 10000, BorrowingLimit: ptr.To[int64](1000)},
						},
					}, {
						Name: "two",
						Resources: map[corev1.ResourceName]*cache.ResourceQuota{
							corev1.ResourcePods: {Nominal: 10},
							corev1.ResourceCPU:  {Nominal: 1000},
						},
					}},
				}},
				Usage: cache.FlavorResourceQuantities{
					"one": {corev1.ResourceCPU: 2000},
				},
			},
			wantRepMode: Fit,
			wantAssignment: Assignment{
				Borrowing: true,
				PodSets: []PodSetAssignment{{
					Name: "main",
					Flavors: ResourceAssignment{
						corev1.ResourceCPU:  {Name: "one", Mode: Fit},
						corev1.ResourcePods: {Name: "one", Mode: Fit},
					},
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("9000m"),
						corev1.ResourcePods: resource.MustParse("1"),
					},
					Count: 1,
				}},
				Usage: cache.FlavorResourceQuantities{
					"one": {corev1.ResourceCPU: 9000, corev1.ResourcePods: 1},
				},
			},
		},
		"borrow try next flavor, found the second flavor": {
			wlPods: []kueue.PodSet{
				*utiltesting.MakePodSet("main", 1).
					Request(corev1.ResourceCPU, "9").
					Obj(),
			},
			clusterQueue: cache.ClusterQueue{
				Cohort: &cache.Cohort{
					Usage: cache.FlavorResourceQuantities{
						"one": {corev1.ResourceCPU: 2000},
					},
					RequestableResources: cache.FlavorResourceQuantities{
						"one": {corev1.ResourceCPU: 11000, corev1.ResourcePods: 10},
						"two": {corev1.ResourceCPU: 10000, corev1.ResourcePods: 10},
					},
				},
				FlavorFungibility: kueue.FlavorFungibility{
					WhenCanBorrow:  kueue.TryNextFlavor,
					WhenCanPreempt: kueue.TryNextFlavor,
				},
				ResourceGroups: []cache.ResourceGroup{{
					CoveredResources: sets.New(corev1.ResourceCPU, corev1.ResourcePods),
					Flavors: []cache.FlavorQuotas{{
						Name: "one",
						Resources: map[corev1.ResourceName]*cache.ResourceQuota{
							corev1.ResourcePods: {Nominal: 10},
							corev1.ResourceCPU:  {Nominal: 10000, BorrowingLimit: ptr.To[int64](1000)},
						},
					}, {
						Name: "two",
						Resources: map[corev1.ResourceName]*cache.ResourceQuota{
							corev1.ResourcePods: {Nominal: 10},
							corev1.ResourceCPU:  {Nominal: 10000},
						},
					}},
				}},
				Usage: cache.FlavorResourceQuantities{
					"one": {corev1.ResourceCPU: 2000},
				},
			},
			wantRepMode: Fit,
			wantAssignment: Assignment{
				PodSets: []PodSetAssignment{{
					Name: "main",
					Flavors: ResourceAssignment{
						corev1.ResourceCPU:  {Name: "two", Mode: Fit},
						corev1.ResourcePods: {Name: "two", Mode: Fit},
					},
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("9000m"),
						corev1.ResourcePods: resource.MustParse("1"),
					},
					Count: 1,
				}},
				Usage: cache.FlavorResourceQuantities{
					"two": {corev1.ResourceCPU: 9000, corev1.ResourcePods: 1},
				},
			},
		},
		"borrow before try next flavor": {
			wlPods: []kueue.PodSet{
				*utiltesting.MakePodSet("main", 1).
					Request(corev1.ResourceCPU, "9").
					Obj(),
			},
			clusterQueue: cache.ClusterQueue{
				Cohort: &cache.Cohort{
					Usage: cache.FlavorResourceQuantities{
						"one": {corev1.ResourceCPU: 2000},
					},
					RequestableResources: cache.FlavorResourceQuantities{
						"one": {corev1.ResourceCPU: 11000, corev1.ResourcePods: 10},
						"two": {corev1.ResourceCPU: 10000, corev1.ResourcePods: 10},
					},
				},
				FlavorFungibility: defaultFlavorFungibility,
				ResourceGroups: []cache.ResourceGroup{{
					CoveredResources: sets.New(corev1.ResourceCPU, corev1.ResourcePods),
					Flavors: []cache.FlavorQuotas{{
						Name: "one",
						Resources: map[corev1.ResourceName]*cache.ResourceQuota{
							corev1.ResourcePods: {Nominal: 10},
							corev1.ResourceCPU:  {Nominal: 10000, BorrowingLimit: ptr.To[int64](1000)},
						},
					}, {
						Name: "two",
						Resources: map[corev1.ResourceName]*cache.ResourceQuota{
							corev1.ResourcePods: {Nominal: 10},
							corev1.ResourceCPU:  {Nominal: 10000},
						},
					}},
				}},
				Usage: cache.FlavorResourceQuantities{
					"one": {corev1.ResourceCPU: 2000},
				},
			},
			wantRepMode: Fit,
			wantAssignment: Assignment{
				Borrowing: true,
				PodSets: []PodSetAssignment{{
					Name: "main",
					Flavors: ResourceAssignment{
						corev1.ResourceCPU:  {Name: "one", Mode: Fit},
						corev1.ResourcePods: {Name: "one", Mode: Fit},
					},
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("9000m"),
						corev1.ResourcePods: resource.MustParse("1"),
					},
					Count: 1,
				}},
				Usage: cache.FlavorResourceQuantities{"one": {"cpu": 9000, "pods": 1}},
			},
		},
		"when borrowing while preemption is needed for flavor one; WhenCanBorrow=Borrow": {
			wlPods: []kueue.PodSet{
				*utiltesting.MakePodSet("main", 1).
					Request(corev1.ResourceCPU, "12").
					Obj(),
			},
			clusterQueue: cache.ClusterQueue{
				Preemption: kueue.ClusterQueuePreemption{
					ReclaimWithinCohort: kueue.PreemptionPolicyLowerPriority,
					BorrowWithinCohort: &kueue.BorrowWithinCohort{
						Policy: kueue.BorrowWithinCohortPolicyLowerPriority,
					},
				},
				Cohort: &cache.Cohort{
					Usage: cache.FlavorResourceQuantities{
						"one": {corev1.ResourceCPU: 10000},
					},
					RequestableResources: cache.FlavorResourceQuantities{
						"one": {corev1.ResourceCPU: 12000},
						"two": {corev1.ResourceCPU: 12000},
					},
				},
				FlavorFungibility: kueue.FlavorFungibility{
					WhenCanBorrow:  kueue.Borrow,
					WhenCanPreempt: kueue.Preempt,
				},
				ResourceGroups: []cache.ResourceGroup{{
					CoveredResources: sets.New(corev1.ResourceCPU),
					Flavors: []cache.FlavorQuotas{{
						Name: "one",
						Resources: map[corev1.ResourceName]*cache.ResourceQuota{
							corev1.ResourceCPU: {Nominal: 0, BorrowingLimit: ptr.To[int64](12000)},
						},
					}, {
						Name: "two",
						Resources: map[corev1.ResourceName]*cache.ResourceQuota{
							corev1.ResourceCPU: {Nominal: 12000},
						},
					}},
				}},
			},
			wantRepMode: Preempt,
			wantAssignment: Assignment{
				Borrowing: true,
				PodSets: []PodSetAssignment{{
					Name: "main",
					Flavors: ResourceAssignment{
						corev1.ResourceCPU: {Name: "one", Mode: Preempt},
					},
					Status: &Status{
						reasons: []string{"insufficient unused quota in cohort for cpu in flavor one, 10 more needed"},
					},
					Requests: corev1.ResourceList{
						corev1.ResourceCPU: resource.MustParse("12000m"),
					},
					Count: 1,
				}},
				Usage: cache.FlavorResourceQuantities{
					"one": {corev1.ResourceCPU: 12000},
				},
			},
		},
		"when borrowing while preemption is needed for flavor one, no borrowingLimit; WhenCanBorrow=Borrow": {
			wlPods: []kueue.PodSet{
				*utiltesting.MakePodSet("main", 1).
					Request(corev1.ResourceCPU, "12").
					Obj(),
			},
			clusterQueue: cache.ClusterQueue{
				Preemption: kueue.ClusterQueuePreemption{
					ReclaimWithinCohort: kueue.PreemptionPolicyLowerPriority,
					BorrowWithinCohort: &kueue.BorrowWithinCohort{
						Policy: kueue.BorrowWithinCohortPolicyLowerPriority,
					},
				},
				Cohort: &cache.Cohort{
					Usage: cache.FlavorResourceQuantities{
						"one": {corev1.ResourceCPU: 10000},
					},
					RequestableResources: cache.FlavorResourceQuantities{
						"one": {corev1.ResourceCPU: 12000},
						"two": {corev1.ResourceCPU: 12000},
					},
				},
				FlavorFungibility: kueue.FlavorFungibility{
					WhenCanBorrow:  kueue.Borrow,
					WhenCanPreempt: kueue.Preempt,
				},
				ResourceGroups: []cache.ResourceGroup{{
					CoveredResources: sets.New(corev1.ResourceCPU),
					Flavors: []cache.FlavorQuotas{{
						Name: "one",
						Resources: map[corev1.ResourceName]*cache.ResourceQuota{
							corev1.ResourceCPU: {Nominal: 0},
						},
					}, {
						Name: "two",
						Resources: map[corev1.ResourceName]*cache.ResourceQuota{
							corev1.ResourceCPU: {Nominal: 12000},
						},
					}},
				}},
			},
			wantRepMode: Preempt,
			wantAssignment: Assignment{
				Borrowing: true,
				PodSets: []PodSetAssignment{{
					Name: "main",
					Flavors: ResourceAssignment{
						corev1.ResourceCPU: {Name: "one", Mode: Preempt},
					},
					Status: &Status{
						reasons: []string{"insufficient unused quota in cohort for cpu in flavor one, 10 more needed"},
					},
					Requests: corev1.ResourceList{
						corev1.ResourceCPU: resource.MustParse("12000m"),
					},
					Count: 1,
				}},
				Usage: cache.FlavorResourceQuantities{
					"one": {corev1.ResourceCPU: 12000},
				},
			},
		},
		"when borrowing while preemption is needed for flavor one; WhenCanBorrow=TryNextFlavor": {
			wlPods: []kueue.PodSet{
				*utiltesting.MakePodSet("main", 1).
					Request(corev1.ResourceCPU, "12").
					Obj(),
			},
			clusterQueue: cache.ClusterQueue{
				Preemption: kueue.ClusterQueuePreemption{
					ReclaimWithinCohort: kueue.PreemptionPolicyLowerPriority,
					BorrowWithinCohort: &kueue.BorrowWithinCohort{
						Policy: kueue.BorrowWithinCohortPolicyLowerPriority,
					},
				},
				Cohort: &cache.Cohort{
					Usage: cache.FlavorResourceQuantities{
						"one": {corev1.ResourceCPU: 10000},
					},
					RequestableResources: cache.FlavorResourceQuantities{
						"one": {corev1.ResourceCPU: 12000},
						"two": {corev1.ResourceCPU: 12000},
					},
				},
				FlavorFungibility: kueue.FlavorFungibility{
					WhenCanBorrow:  kueue.TryNextFlavor,
					WhenCanPreempt: kueue.Preempt,
				},
				ResourceGroups: []cache.ResourceGroup{{
					CoveredResources: sets.New(corev1.ResourceCPU),
					Flavors: []cache.FlavorQuotas{{
						Name: "one",
						Resources: map[corev1.ResourceName]*cache.ResourceQuota{
							corev1.ResourceCPU: {Nominal: 0, BorrowingLimit: ptr.To[int64](12000)},
						},
					}, {
						Name: "two",
						Resources: map[corev1.ResourceName]*cache.ResourceQuota{
							corev1.ResourceCPU: {Nominal: 12000},
						},
					}},
				}},
			},
			wantRepMode: Fit,
			wantAssignment: Assignment{
				PodSets: []PodSetAssignment{{
					Name: "main",
					Flavors: ResourceAssignment{
						corev1.ResourceCPU: {Name: "two", Mode: Fit},
					},
					Requests: corev1.ResourceList{
						corev1.ResourceCPU: resource.MustParse("12000m"),
					},
					Count: 1,
				}},
				Usage: cache.FlavorResourceQuantities{
					"two": {corev1.ResourceCPU: 12000},
				},
			},
		},
		"when borrowing while preemption is needed, but borrowingLimit exceeds the quota available in the cohort": {
			wlPods: []kueue.PodSet{
				*utiltesting.MakePodSet("main", 1).
					Request(corev1.ResourceCPU, "12").
					Obj(),
			},
			clusterQueue: cache.ClusterQueue{
				Preemption: kueue.ClusterQueuePreemption{
					ReclaimWithinCohort: kueue.PreemptionPolicyLowerPriority,
					BorrowWithinCohort: &kueue.BorrowWithinCohort{
						Policy: kueue.BorrowWithinCohortPolicyLowerPriority,
					},
				},
				Cohort: &cache.Cohort{
					Usage: cache.FlavorResourceQuantities{
						"one": {corev1.ResourceCPU: 10000},
					},
					RequestableResources: cache.FlavorResourceQuantities{
						// below the borrowingLimit required to admit
						"one": {corev1.ResourceCPU: 11000},
					},
				},
				ResourceGroups: []cache.ResourceGroup{{
					CoveredResources: sets.New(corev1.ResourceCPU),
					Flavors: []cache.FlavorQuotas{{
						Name: "one",
						Resources: map[corev1.ResourceName]*cache.ResourceQuota{
							corev1.ResourceCPU: {Nominal: 0, BorrowingLimit: ptr.To[int64](12000)},
						},
					}},
				}},
			},
			wantRepMode: NoFit,
			wantAssignment: Assignment{
				Usage: cache.FlavorResourceQuantities{},
				PodSets: []PodSetAssignment{
					{
						Name: "main",
						Status: &Status{
							reasons: []string{"insufficient unused quota in cohort for cpu in flavor one, 11 more needed"},
						},
						Requests: corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("12000m"),
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
			clusterQueue: cache.ClusterQueue{
				Cohort: &cache.Cohort{
					Usage: cache.FlavorResourceQuantities{
						"one": {corev1.ResourceCPU: 2000},
					},
					RequestableResources: cache.FlavorResourceQuantities{
						"one": {corev1.ResourceCPU: 11000, corev1.ResourcePods: 10},
						"two": {corev1.ResourceCPU: 10000, corev1.ResourcePods: 10},
					},
				},
				FlavorFungibility: kueue.FlavorFungibility{
					WhenCanBorrow:  kueue.TryNextFlavor,
					WhenCanPreempt: kueue.TryNextFlavor,
				},
				ResourceGroups: []cache.ResourceGroup{{
					CoveredResources: sets.New(corev1.ResourceCPU, corev1.ResourcePods),
					Flavors: []cache.FlavorQuotas{{
						Name: "one",
						Resources: map[corev1.ResourceName]*cache.ResourceQuota{
							corev1.ResourcePods: {Nominal: 10},
							corev1.ResourceCPU:  {Nominal: 10_000, LendingLimit: ptr.To[int64](1000)},
						},
					}, {
						Name: "two",
						Resources: map[corev1.ResourceName]*cache.ResourceQuota{
							corev1.ResourcePods: {Nominal: 10},
							corev1.ResourceCPU:  {Nominal: 10_000, LendingLimit: ptr.To[int64](0)},
						},
					}},
				}},
				Usage: cache.FlavorResourceQuantities{
					"one": {corev1.ResourceCPU: 2000},
				},
				GuaranteedQuota: cache.FlavorResourceQuantities{
					"one": {
						"cpu": 9,
					},
					"two": {
						"cpu": 10,
					},
				},
			},
			wantRepMode: Fit,
			wantAssignment: Assignment{
				PodSets: []PodSetAssignment{{
					Name: "main",
					Flavors: ResourceAssignment{
						corev1.ResourceCPU:  {Name: "two", Mode: Fit},
						corev1.ResourcePods: {Name: "two", Mode: Fit},
					},
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("9000m"),
						corev1.ResourcePods: resource.MustParse("1"),
					},
					Count: 1,
				}},
				Usage: cache.FlavorResourceQuantities{
					"two": {corev1.ResourceCPU: 9000, corev1.ResourcePods: 1},
				},
			},
			enableLendingLimit: true,
		},
		"lend try next flavor, found the first flavor": {
			wlPods: []kueue.PodSet{
				*utiltesting.MakePodSet("main", 1).
					Request(corev1.ResourceCPU, "9").
					Obj(),
			},
			clusterQueue: cache.ClusterQueue{
				Cohort: &cache.Cohort{
					Usage: cache.FlavorResourceQuantities{
						"one": {corev1.ResourceCPU: 2000},
					},
					RequestableResources: cache.FlavorResourceQuantities{
						"one": {corev1.ResourceCPU: 11000, corev1.ResourcePods: 10},
						"two": {corev1.ResourceCPU: 1000, corev1.ResourcePods: 10},
					},
				},
				FlavorFungibility: kueue.FlavorFungibility{
					WhenCanBorrow:  kueue.TryNextFlavor,
					WhenCanPreempt: kueue.TryNextFlavor,
				},
				ResourceGroups: []cache.ResourceGroup{{
					CoveredResources: sets.New(corev1.ResourceCPU, corev1.ResourcePods),
					Flavors: []cache.FlavorQuotas{{
						Name: "one",
						Resources: map[corev1.ResourceName]*cache.ResourceQuota{
							corev1.ResourcePods: {Nominal: 10},
							corev1.ResourceCPU:  {Nominal: 10_000, LendingLimit: ptr.To[int64](1000)},
						},
					}, {
						Name: "two",
						Resources: map[corev1.ResourceName]*cache.ResourceQuota{
							corev1.ResourcePods: {Nominal: 10},
							corev1.ResourceCPU:  {Nominal: 1_000, LendingLimit: ptr.To[int64](0)},
						},
					}},
				}},
				Usage: cache.FlavorResourceQuantities{
					"one": {corev1.ResourceCPU: 2000},
				},
				GuaranteedQuota: cache.FlavorResourceQuantities{
					"one": {
						"cpu": 9,
					},
					"two": {
						"cpu": 1,
					},
				},
			},
			wantRepMode: Fit,
			wantAssignment: Assignment{
				PodSets: []PodSetAssignment{{
					Name: "main",
					Flavors: ResourceAssignment{
						corev1.ResourceCPU:  {Name: "one", Mode: Fit},
						corev1.ResourcePods: {Name: "one", Mode: Fit},
					},
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("9000m"),
						corev1.ResourcePods: resource.MustParse("1"),
					},
					Count: 1,
				}},
				Borrowing: true,
				Usage: cache.FlavorResourceQuantities{
					"one": {corev1.ResourceCPU: 9000, corev1.ResourcePods: 1},
				},
			},
			enableLendingLimit: true,
		},
		"lendingLimit exceeded, but can preempt in cohort and ClusterQueue": {
			wlPods: []kueue.PodSet{
				*utiltesting.MakePodSet("main", 1).
					Request(corev1.ResourceCPU, "9").
					Obj(),
			},
			clusterQueue: cache.ClusterQueue{
				Cohort: &cache.Cohort{
					Usage: cache.FlavorResourceQuantities{
						"one": {corev1.ResourceCPU: 2000},
					},
					RequestableResources: cache.FlavorResourceQuantities{
						"one": {corev1.ResourceCPU: 10000, corev1.ResourcePods: 10},
					},
				},
				FlavorFungibility: defaultFlavorFungibility,
				ResourceGroups: []cache.ResourceGroup{{
					CoveredResources: sets.New(corev1.ResourceCPU, corev1.ResourcePods),
					Flavors: []cache.FlavorQuotas{{
						Name: "one",
						Resources: map[corev1.ResourceName]*cache.ResourceQuota{
							corev1.ResourcePods: {Nominal: 10},
							corev1.ResourceCPU:  {Nominal: 10_000, LendingLimit: ptr.To[int64](0)},
						},
					}},
				}},
				Usage: cache.FlavorResourceQuantities{
					"one": {corev1.ResourceCPU: 2000},
				},
			},
			wantRepMode: Preempt,
			wantAssignment: Assignment{
				PodSets: []PodSetAssignment{{
					Name: "main",
					Flavors: ResourceAssignment{
						corev1.ResourceCPU:  {Name: "one", Mode: Preempt},
						corev1.ResourcePods: {Name: "one", Mode: Fit},
					},
					Requests: corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("9000m"),
						corev1.ResourcePods: resource.MustParse("1"),
					},
					Status: &Status{
						reasons: []string{"insufficient unused quota in cohort for cpu in flavor one, 1 more needed"},
					},
					Count: 1,
				}},
				Usage: cache.FlavorResourceQuantities{
					"one": {corev1.ResourceCPU: 9000, corev1.ResourcePods: 1},
				},
			},
			enableLendingLimit: true,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			defer features.SetFeatureGateDuringTest(t, features.LendingLimit, tc.enableLendingLimit)()
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
			if tc.clusterQueue.FlavorFungibility.WhenCanBorrow == "" {
				tc.clusterQueue.FlavorFungibility.WhenCanBorrow = kueue.Borrow
			}
			if tc.clusterQueue.FlavorFungibility.WhenCanPreempt == "" {
				tc.clusterQueue.FlavorFungibility.WhenCanPreempt = kueue.TryNextFlavor
			}
			tc.clusterQueue.UpdateWithFlavors(resourceFlavors)
			tc.clusterQueue.UpdateRGByResource()
			flvAssigner := New(wlInfo, &tc.clusterQueue, resourceFlavors)
			assignment := flvAssigner.Assign(log, nil)
			if repMode := assignment.RepresentativeMode(); repMode != tc.wantRepMode {
				t.Errorf("e.assignFlavors(_).RepresentativeMode()=%s, want %s", repMode, tc.wantRepMode)
			}

			if diff := cmp.Diff(tc.wantAssignment, assignment, cmpopts.IgnoreUnexported(Assignment{}, FlavorAssignment{}), cmpopts.IgnoreFields(Assignment{}, "LastState"), cmpopts.IgnoreFields(FlavorAssignment{}, "TriedFlavorIdx")); diff != "" {
				t.Errorf("Unexpected assignment (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestLastAssignmentOutdated(t *testing.T) {
	type args struct {
		wl *workload.Info
		cq *cache.ClusterQueue
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
				cq: &cache.ClusterQueue{
					Cohort:                        nil,
					AllocatableResourceGeneration: 1,
				},
			},
			want: true,
		},
		{
			name: "Cohort allocatableResourceIncreasedGen increased",
			args: args{
				wl: &workload.Info{
					LastAssignment: &workload.AssignmentClusterQueueState{
						ClusterQueueGeneration: 0,
						CohortGeneration:       0,
					},
				},
				cq: &cache.ClusterQueue{
					Cohort: &cache.Cohort{
						AllocatableResourceGeneration: 1,
					},
					AllocatableResourceGeneration: 0,
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
						CohortGeneration:       0,
					},
				},
				cq: &cache.ClusterQueue{
					Cohort: &cache.Cohort{
						AllocatableResourceGeneration: 0,
					},
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
