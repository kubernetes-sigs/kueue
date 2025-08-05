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
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/utils/ptr"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/hierarchy"
	"sigs.k8s.io/kueue/pkg/resources"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	"sigs.k8s.io/kueue/pkg/workload"
)

var snapCmpOpts = cmp.Options{
	cmpopts.EquateEmpty(),
	cmpopts.IgnoreUnexported(hierarchy.Cohort[*ClusterQueueSnapshot, *CohortSnapshot]{}),
	cmpopts.IgnoreUnexported(hierarchy.ClusterQueue[*CohortSnapshot]{}),
	cmpopts.IgnoreUnexported(hierarchy.Manager[*ClusterQueueSnapshot, *CohortSnapshot]{}),
	cmpopts.IgnoreFields(metav1.Condition{}, "LastTransitionTime"),
}

func TestSnapshot(t *testing.T) {
	testCases := map[string]struct {
		cqs                 []*kueue.ClusterQueue
		cohorts             []*kueue.Cohort
		rfs                 []*kueue.ResourceFlavor
		wls                 []*kueue.Workload
		wantSnapshot        Snapshot
		disableLendingLimit bool
	}{
		"empty": {},
		"independent clusterQueues": {
			cqs: []*kueue.ClusterQueue{
				utiltesting.MakeClusterQueue("a").Obj(),
				utiltesting.MakeClusterQueue("b").Obj(),
			},
			wls: []*kueue.Workload{
				utiltesting.MakeWorkload("alpha", "").
					ReserveQuota(&kueue.Admission{ClusterQueue: "a"}).Obj(),
				utiltesting.MakeWorkload("beta", "").
					ReserveQuota(&kueue.Admission{ClusterQueue: "b"}).Obj(),
			},
			wantSnapshot: Snapshot{
				Manager: hierarchy.NewManagerForTest(
					map[kueue.CohortReference]*CohortSnapshot{},
					map[kueue.ClusterQueueReference]*ClusterQueueSnapshot{
						"a": {
							Name:                          "a",
							NamespaceSelector:             labels.Everything(),
							Status:                        active,
							FlavorFungibility:             defaultFlavorFungibility,
							AllocatableResourceGeneration: 1,
							Workloads: map[workload.Reference]*workload.Info{
								"/alpha": workload.NewInfo(
									utiltesting.MakeWorkload("alpha", "").
										ReserveQuota(&kueue.Admission{ClusterQueue: "a"}).Obj()),
							},
							Preemption: defaultPreemption,
							FairWeight: oneQuantity,
						},
						"b": {
							Name:                          "b",
							NamespaceSelector:             labels.Everything(),
							Status:                        active,
							FlavorFungibility:             defaultFlavorFungibility,
							AllocatableResourceGeneration: 1,
							Workloads: map[workload.Reference]*workload.Info{
								"/beta": workload.NewInfo(
									utiltesting.MakeWorkload("beta", "").
										ReserveQuota(&kueue.Admission{ClusterQueue: "b"}).Obj()),
							},
							Preemption: defaultPreemption,
							FairWeight: oneQuantity,
						},
					},
				),
			},
		},
		"inactive clusterQueues": {
			cqs: []*kueue.ClusterQueue{
				utiltesting.MakeClusterQueue("flavor-nonexistent-cq").
					ResourceGroup(*utiltesting.MakeFlavorQuotas("nonexistent-flavor").
						Resource(corev1.ResourceCPU, "100").Obj()).
					Obj(),
			},
			wantSnapshot: Snapshot{
				InactiveClusterQueueSets: sets.New[kueue.ClusterQueueReference]("flavor-nonexistent-cq"),
			},
		},
		"resourceFlavors": {
			rfs: []*kueue.ResourceFlavor{
				utiltesting.MakeResourceFlavor("demand").
					NodeLabel("a", "b").
					NodeLabel("instance", "demand").
					Obj(),
				utiltesting.MakeResourceFlavor("spot").
					NodeLabel("c", "d").
					NodeLabel("instance", "spot").
					Obj(),
				utiltesting.MakeResourceFlavor("default").Obj(),
			},
			wantSnapshot: Snapshot{
				Manager: hierarchy.NewManagerForTest(
					map[kueue.CohortReference]*CohortSnapshot{},
					map[kueue.ClusterQueueReference]*ClusterQueueSnapshot{},
				),
				ResourceFlavors: map[kueue.ResourceFlavorReference]*kueue.ResourceFlavor{
					"demand": utiltesting.MakeResourceFlavor("demand").
						NodeLabel("a", "b").
						NodeLabel("instance", "demand").
						Obj(),
					"spot": utiltesting.MakeResourceFlavor("spot").
						NodeLabel("c", "d").
						NodeLabel("instance", "spot").
						Obj(),
					"default": utiltesting.MakeResourceFlavor("default").Obj(),
				},
			},
		},
		"cohort": {
			cqs: []*kueue.ClusterQueue{
				utiltesting.MakeClusterQueue("a").
					Cohort("borrowing").
					ResourceGroup(
						*utiltesting.MakeFlavorQuotas("demand").Resource(corev1.ResourceCPU, "100").Obj(),
						*utiltesting.MakeFlavorQuotas("spot").Resource(corev1.ResourceCPU, "200").Obj(),
					).
					Obj(),
				utiltesting.MakeClusterQueue("b").
					Cohort("borrowing").
					ResourceGroup(
						*utiltesting.MakeFlavorQuotas("spot").Resource(corev1.ResourceCPU, "100").Obj(),
					).
					ResourceGroup(
						*utiltesting.MakeFlavorQuotas("default").Resource("example.com/gpu", "50").Obj(),
					).
					Obj(),
				utiltesting.MakeClusterQueue("c").
					ResourceGroup(
						*utiltesting.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "100").Obj(),
					).
					Obj(),
			},
			rfs: []*kueue.ResourceFlavor{
				utiltesting.MakeResourceFlavor("demand").NodeLabel("instance", "demand").Obj(),
				utiltesting.MakeResourceFlavor("spot").NodeLabel("instance", "spot").Obj(),
				utiltesting.MakeResourceFlavor("default").Obj(),
			},
			wls: []*kueue.Workload{
				utiltesting.MakeWorkload("alpha", "").
					PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 5).
						Request(corev1.ResourceCPU, "2").Obj()).
					ReserveQuota(utiltesting.MakeAdmission("a", kueue.DefaultPodSetName).Assignment(corev1.ResourceCPU, "demand", "10000m").AssignmentPodCount(5).Obj()).
					Obj(),
				utiltesting.MakeWorkload("beta", "").
					PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 5).
						Request(corev1.ResourceCPU, "1").
						Request("example.com/gpu", "2").
						Obj(),
					).
					ReserveQuota(utiltesting.MakeAdmission("b", kueue.DefaultPodSetName).
						Assignment(corev1.ResourceCPU, "spot", "5000m").
						Assignment("example.com/gpu", "default", "10").
						AssignmentPodCount(5).
						Obj()).
					Obj(),
				utiltesting.MakeWorkload("gamma", "").
					PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 5).
						Request(corev1.ResourceCPU, "1").
						Request("example.com/gpu", "1").
						Obj(),
					).
					ReserveQuota(utiltesting.MakeAdmission("b", kueue.DefaultPodSetName).
						Assignment(corev1.ResourceCPU, "spot", "5000m").
						Assignment("example.com/gpu", "default", "5").
						AssignmentPodCount(5).
						Obj()).
					Obj(),
				utiltesting.MakeWorkload("sigma", "").
					PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 5).
						Request(corev1.ResourceCPU, "1").
						Obj(),
					).
					Obj(),
			},
			wantSnapshot: func() Snapshot {
				cohort := &CohortSnapshot{
					Name: "borrowing",
					ResourceNode: resourceNode{
						Usage: resources.FlavorResourceQuantities{
							{Flavor: "demand", Resource: corev1.ResourceCPU}: 10_000,
							{Flavor: "spot", Resource: corev1.ResourceCPU}:   10_000,
							{Flavor: "default", Resource: "example.com/gpu"}: 15,
						},
						SubtreeQuota: resources.FlavorResourceQuantities{
							{Flavor: "demand", Resource: corev1.ResourceCPU}: 100_000,
							{Flavor: "spot", Resource: corev1.ResourceCPU}:   300_000,
							{Flavor: "default", Resource: "example.com/gpu"}: 50,
						},
					},
				}
				return Snapshot{
					Manager: hierarchy.NewManagerForTest(
						map[kueue.CohortReference]*CohortSnapshot{
							"borrowing": cohort,
						},
						map[kueue.ClusterQueueReference]*ClusterQueueSnapshot{
							"a": {
								Name:                          "a",
								AllocatableResourceGeneration: 2,
								ResourceGroups: []ResourceGroup{
									{
										CoveredResources: sets.New(corev1.ResourceCPU),
										Flavors:          []kueue.ResourceFlavorReference{"demand", "spot"},
										LabelKeys:        sets.New("instance"),
									},
								},
								ResourceNode: resourceNode{
									Quotas: map[resources.FlavorResource]ResourceQuota{
										{Flavor: "demand", Resource: corev1.ResourceCPU}: {Nominal: 100_000},
										{Flavor: "spot", Resource: corev1.ResourceCPU}:   {Nominal: 200_000},
									},
									SubtreeQuota: resources.FlavorResourceQuantities{
										{Flavor: "demand", Resource: "cpu"}: 100_000,
										{Flavor: "spot", Resource: "cpu"}:   200_000,
									},
									Usage: resources.FlavorResourceQuantities{
										{Flavor: "demand", Resource: corev1.ResourceCPU}: 10_000,
									},
								},
								FlavorFungibility: defaultFlavorFungibility,
								Workloads: map[workload.Reference]*workload.Info{
									"/alpha": workload.NewInfo(utiltesting.MakeWorkload("alpha", "").
										PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 5).
											Request(corev1.ResourceCPU, "2").Obj()).
										ReserveQuota(utiltesting.MakeAdmission("a", kueue.DefaultPodSetName).
											Assignment(corev1.ResourceCPU, "demand", "10000m").
											AssignmentPodCount(5).
											Obj()).
										Obj()),
								},
								Preemption:        defaultPreemption,
								FairWeight:        oneQuantity,
								NamespaceSelector: labels.Everything(),
								Status:            active,
							},
							"b": {
								Name:                          "b",
								AllocatableResourceGeneration: 1,
								ResourceGroups: []ResourceGroup{
									{
										CoveredResources: sets.New(corev1.ResourceCPU),
										Flavors:          []kueue.ResourceFlavorReference{"spot"},
										LabelKeys:        sets.New("instance"),
									},
									{
										CoveredResources: sets.New[corev1.ResourceName]("example.com/gpu"),
										Flavors:          []kueue.ResourceFlavorReference{"default"},
									},
								},
								ResourceNode: resourceNode{
									Quotas: map[resources.FlavorResource]ResourceQuota{
										{Flavor: "spot", Resource: corev1.ResourceCPU}:   {Nominal: 100_000},
										{Flavor: "default", Resource: "example.com/gpu"}: {Nominal: 50},
									},
									SubtreeQuota: resources.FlavorResourceQuantities{
										{Flavor: "spot", Resource: "cpu"}:                100_000,
										{Flavor: "default", Resource: "example.com/gpu"}: 50,
									},
									Usage: resources.FlavorResourceQuantities{
										{Flavor: "spot", Resource: corev1.ResourceCPU}:   10_000,
										{Flavor: "default", Resource: "example.com/gpu"}: 15,
									},
								},
								FlavorFungibility: defaultFlavorFungibility,
								Workloads: map[workload.Reference]*workload.Info{
									"/beta": workload.NewInfo(utiltesting.MakeWorkload("beta", "").
										PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 5).
											Request(corev1.ResourceCPU, "1").
											Request("example.com/gpu", "2").
											Obj()).
										ReserveQuota(utiltesting.MakeAdmission("b", kueue.DefaultPodSetName).
											Assignment(corev1.ResourceCPU, "spot", "5000m").
											Assignment("example.com/gpu", "default", "10").
											AssignmentPodCount(5).
											Obj()).
										Obj()),
									"/gamma": workload.NewInfo(utiltesting.MakeWorkload("gamma", "").
										PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 5).
											Request(corev1.ResourceCPU, "1").
											Request("example.com/gpu", "1").
											Obj(),
										).
										ReserveQuota(utiltesting.MakeAdmission("b", kueue.DefaultPodSetName).
											Assignment(corev1.ResourceCPU, "spot", "5000m").
											Assignment("example.com/gpu", "default", "5").
											AssignmentPodCount(5).
											Obj()).
										Obj()),
								},
								Preemption:        defaultPreemption,
								FairWeight:        oneQuantity,
								NamespaceSelector: labels.Everything(),
								Status:            active,
							},
							"c": {
								Name:                          "c",
								AllocatableResourceGeneration: 1,
								ResourceGroups: []ResourceGroup{
									{
										CoveredResources: sets.New(corev1.ResourceCPU),
										Flavors:          []kueue.ResourceFlavorReference{"default"},
									},
								},
								ResourceNode: resourceNode{
									Quotas: map[resources.FlavorResource]ResourceQuota{
										{Flavor: "default", Resource: corev1.ResourceCPU}: {Nominal: 100_000},
									},
									SubtreeQuota: resources.FlavorResourceQuantities{
										{Flavor: "default", Resource: "cpu"}: 100_000,
									},
									Usage: resources.FlavorResourceQuantities{},
								},
								FlavorFungibility: defaultFlavorFungibility,
								Preemption:        defaultPreemption,
								FairWeight:        oneQuantity,
								NamespaceSelector: labels.Everything(),
								Status:            active,
							},
						},
					),
					ResourceFlavors: map[kueue.ResourceFlavorReference]*kueue.ResourceFlavor{
						"demand":  utiltesting.MakeResourceFlavor("demand").NodeLabel("instance", "demand").Obj(),
						"spot":    utiltesting.MakeResourceFlavor("spot").NodeLabel("instance", "spot").Obj(),
						"default": utiltesting.MakeResourceFlavor("default").Obj(),
					},
				}
			}(),
		},
		"clusterQueues with preemption": {
			cqs: []*kueue.ClusterQueue{
				utiltesting.MakeClusterQueue("with-preemption").
					Preemption(kueue.ClusterQueuePreemption{
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
					}).Obj(),
			},
			wantSnapshot: Snapshot{
				Manager: hierarchy.NewManagerForTest(
					map[kueue.CohortReference]*CohortSnapshot{},
					map[kueue.ClusterQueueReference]*ClusterQueueSnapshot{
						"with-preemption": {
							Name:                          "with-preemption",
							NamespaceSelector:             labels.Everything(),
							AllocatableResourceGeneration: 1,
							Status:                        active,
							Workloads:                     map[workload.Reference]*workload.Info{},
							FlavorFungibility:             defaultFlavorFungibility,
							Preemption: kueue.ClusterQueuePreemption{
								ReclaimWithinCohort: kueue.PreemptionPolicyAny,
								WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
							},
							FairWeight: oneQuantity,
						},
					},
				),
			},
		},
		"clusterQueue with fair sharing weight": {
			cqs: []*kueue.ClusterQueue{
				utiltesting.MakeClusterQueue("with-preemption").FairWeight(resource.MustParse("3")).Obj(),
			},
			wantSnapshot: Snapshot{
				Manager: hierarchy.NewManagerForTest(
					map[kueue.CohortReference]*CohortSnapshot{},
					map[kueue.ClusterQueueReference]*ClusterQueueSnapshot{
						"with-preemption": {
							Name:                          "with-preemption",
							NamespaceSelector:             labels.Everything(),
							AllocatableResourceGeneration: 1,
							Status:                        active,
							Workloads:                     map[workload.Reference]*workload.Info{},
							FlavorFungibility:             defaultFlavorFungibility,
							Preemption:                    defaultPreemption,
							FairWeight:                    resource.MustParse("3"),
						},
					},
				),
			},
		},
		"lendingLimit with 2 clusterQueues and 2 flavors(whenCanBorrow: Borrow)": {
			cqs: []*kueue.ClusterQueue{
				utiltesting.MakeClusterQueue("a").
					Cohort("lending").
					ResourceGroup(
						*utiltesting.MakeFlavorQuotas("arm").Resource(corev1.ResourceCPU, "10", "", "5").Obj(),
						*utiltesting.MakeFlavorQuotas("x86").Resource(corev1.ResourceCPU, "20", "", "10").Obj(),
					).
					FlavorFungibility(defaultFlavorFungibility).
					Obj(),
				utiltesting.MakeClusterQueue("b").
					Cohort("lending").
					ResourceGroup(
						*utiltesting.MakeFlavorQuotas("arm").Resource(corev1.ResourceCPU, "10", "", "5").Obj(),
						*utiltesting.MakeFlavorQuotas("x86").Resource(corev1.ResourceCPU, "20", "", "10").Obj(),
					).
					Obj(),
			},
			rfs: []*kueue.ResourceFlavor{
				utiltesting.MakeResourceFlavor("arm").NodeLabel("arch", "arm").Obj(),
				utiltesting.MakeResourceFlavor("x86").NodeLabel("arch", "x86").Obj(),
			},
			wls: []*kueue.Workload{
				utiltesting.MakeWorkload("alpha", "").
					PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 5).
						Request(corev1.ResourceCPU, "2").Obj()).
					ReserveQuota(utiltesting.MakeAdmission("a", kueue.DefaultPodSetName).
						Assignment(corev1.ResourceCPU, "arm", "10000m").
						AssignmentPodCount(5).Obj()).
					Obj(),
				utiltesting.MakeWorkload("beta", "").
					PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 5).
						Request(corev1.ResourceCPU, "1").Obj()).
					ReserveQuota(utiltesting.MakeAdmission("a", kueue.DefaultPodSetName).
						Assignment(corev1.ResourceCPU, "arm", "5000m").
						AssignmentPodCount(5).Obj()).
					Obj(),
				utiltesting.MakeWorkload("gamma", "").
					PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 5).
						Request(corev1.ResourceCPU, "2").Obj()).
					ReserveQuota(utiltesting.MakeAdmission("a", kueue.DefaultPodSetName).
						Assignment(corev1.ResourceCPU, "x86", "10000m").
						AssignmentPodCount(5).Obj()).
					Obj(),
			},
			wantSnapshot: func() Snapshot {
				cohort := &CohortSnapshot{
					Name: "lending",
					ResourceNode: resourceNode{
						Usage: resources.FlavorResourceQuantities{
							{Flavor: "arm", Resource: corev1.ResourceCPU}: 10_000,
						},
						SubtreeQuota: resources.FlavorResourceQuantities{
							{Flavor: "arm", Resource: corev1.ResourceCPU}: 10_000,
							{Flavor: "x86", Resource: corev1.ResourceCPU}: 20_000,
						},
					},
				}
				return Snapshot{
					Manager: hierarchy.NewManagerForTest(
						map[kueue.CohortReference]*CohortSnapshot{
							"lending": cohort,
						},
						map[kueue.ClusterQueueReference]*ClusterQueueSnapshot{
							"a": {
								Name:                          "a",
								AllocatableResourceGeneration: 2,
								ResourceGroups: []ResourceGroup{
									{
										CoveredResources: sets.New(corev1.ResourceCPU),
										Flavors:          []kueue.ResourceFlavorReference{"arm", "x86"},
										LabelKeys:        sets.New("arch"),
									},
								},
								ResourceNode: resourceNode{
									Quotas: map[resources.FlavorResource]ResourceQuota{
										{Flavor: "arm", Resource: corev1.ResourceCPU}: {Nominal: 10_000, BorrowingLimit: nil, LendingLimit: ptr.To[int64](5_000)},
										{Flavor: "x86", Resource: corev1.ResourceCPU}: {Nominal: 20_000, BorrowingLimit: nil, LendingLimit: ptr.To[int64](10_000)},
									},
									SubtreeQuota: resources.FlavorResourceQuantities{
										{Flavor: "arm", Resource: corev1.ResourceCPU}: 10_000,
										{Flavor: "x86", Resource: corev1.ResourceCPU}: 20_000,
									},
									Usage: resources.FlavorResourceQuantities{
										{Flavor: "arm", Resource: corev1.ResourceCPU}: 15_000,
										{Flavor: "x86", Resource: corev1.ResourceCPU}: 10_000,
									},
								},
								FlavorFungibility: defaultFlavorFungibility,
								FairWeight:        oneQuantity,
								Workloads: map[workload.Reference]*workload.Info{
									"/alpha": workload.NewInfo(utiltesting.MakeWorkload("alpha", "").
										PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 5).
											Request(corev1.ResourceCPU, "2").Obj()).
										ReserveQuota(utiltesting.MakeAdmission("a", kueue.DefaultPodSetName).
											Assignment(corev1.ResourceCPU, "arm", "10000m").
											AssignmentPodCount(5).Obj()).
										Obj()),
									"/beta": workload.NewInfo(utiltesting.MakeWorkload("beta", "").
										PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 5).
											Request(corev1.ResourceCPU, "1").Obj()).
										ReserveQuota(utiltesting.MakeAdmission("a", kueue.DefaultPodSetName).
											Assignment(corev1.ResourceCPU, "arm", "5000m").
											AssignmentPodCount(5).Obj()).
										Obj()),
									"/gamma": workload.NewInfo(utiltesting.MakeWorkload("gamma", "").
										PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 5).
											Request(corev1.ResourceCPU, "2").Obj()).
										ReserveQuota(utiltesting.MakeAdmission("a", kueue.DefaultPodSetName).
											Assignment(corev1.ResourceCPU, "x86", "10000m").
											AssignmentPodCount(5).Obj()).
										Obj()),
								},
								Preemption:        defaultPreemption,
								NamespaceSelector: labels.Everything(),
								Status:            active,
							},
							"b": {
								Name:                          "b",
								AllocatableResourceGeneration: 1,
								ResourceGroups: []ResourceGroup{
									{
										CoveredResources: sets.New(corev1.ResourceCPU),
										Flavors:          []kueue.ResourceFlavorReference{"arm", "x86"},
										LabelKeys:        sets.New("arch"),
									},
								},
								ResourceNode: resourceNode{
									Quotas: map[resources.FlavorResource]ResourceQuota{
										{Flavor: "arm", Resource: corev1.ResourceCPU}: {Nominal: 10_000, BorrowingLimit: nil, LendingLimit: ptr.To[int64](5_000)},
										{Flavor: "x86", Resource: corev1.ResourceCPU}: {Nominal: 20_000, BorrowingLimit: nil, LendingLimit: ptr.To[int64](10_000)},
									},
									SubtreeQuota: resources.FlavorResourceQuantities{
										{Flavor: "arm", Resource: corev1.ResourceCPU}: 10_000,
										{Flavor: "x86", Resource: corev1.ResourceCPU}: 20_000,
									},
									Usage: resources.FlavorResourceQuantities{},
								},
								FlavorFungibility: defaultFlavorFungibility,
								FairWeight:        oneQuantity,
								Preemption:        defaultPreemption,
								NamespaceSelector: labels.Everything(),
								Status:            active,
							},
						},
					),
					ResourceFlavors: map[kueue.ResourceFlavorReference]*kueue.ResourceFlavor{
						"arm": utiltesting.MakeResourceFlavor("arm").NodeLabel("arch", "arm").Obj(),
						"x86": utiltesting.MakeResourceFlavor("x86").NodeLabel("arch", "x86").Obj(),
					},
				}
			}(),
		},
		"should not populate the snapshot with lendingLimit when feature disabled": {
			disableLendingLimit: true,
			cqs: []*kueue.ClusterQueue{
				utiltesting.MakeClusterQueue("a").
					Cohort("lending").
					ResourceGroup(
						*utiltesting.MakeFlavorQuotas("arm").Resource(corev1.ResourceCPU, "10", "", "5").Obj(),
						*utiltesting.MakeFlavorQuotas("x86").Resource(corev1.ResourceCPU, "20", "", "10").Obj(),
					).
					FlavorFungibility(defaultFlavorFungibility).
					Obj(),
				utiltesting.MakeClusterQueue("b").
					Cohort("lending").
					ResourceGroup(
						*utiltesting.MakeFlavorQuotas("arm").Resource(corev1.ResourceCPU, "10", "", "5").Obj(),
						*utiltesting.MakeFlavorQuotas("x86").Resource(corev1.ResourceCPU, "20", "", "10").Obj(),
					).
					Obj(),
			},
			rfs: []*kueue.ResourceFlavor{
				utiltesting.MakeResourceFlavor("arm").NodeLabel("arch", "arm").Obj(),
				utiltesting.MakeResourceFlavor("x86").NodeLabel("arch", "x86").Obj(),
			},
			wls: []*kueue.Workload{
				utiltesting.MakeWorkload("alpha", "").
					PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 5).
						Request(corev1.ResourceCPU, "2").Obj()).
					ReserveQuota(utiltesting.MakeAdmission("a", kueue.DefaultPodSetName).
						Assignment(corev1.ResourceCPU, "arm", "10000m").
						AssignmentPodCount(5).Obj()).
					Obj(),
				utiltesting.MakeWorkload("beta", "").
					PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 5).
						Request(corev1.ResourceCPU, "1").Obj()).
					ReserveQuota(utiltesting.MakeAdmission("a", kueue.DefaultPodSetName).
						Assignment(corev1.ResourceCPU, "arm", "5000m").
						AssignmentPodCount(5).Obj()).
					Obj(),
				utiltesting.MakeWorkload("gamma", "").
					PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 5).
						Request(corev1.ResourceCPU, "2").Obj()).
					ReserveQuota(utiltesting.MakeAdmission("a", kueue.DefaultPodSetName).
						Assignment(corev1.ResourceCPU, "x86", "10000m").
						AssignmentPodCount(5).Obj()).
					Obj(),
			},
			wantSnapshot: func() Snapshot {
				cohort := &CohortSnapshot{
					Name: "lending",
					ResourceNode: resourceNode{
						SubtreeQuota: resources.FlavorResourceQuantities{
							{Flavor: "arm", Resource: corev1.ResourceCPU}: 20_000,
							{Flavor: "x86", Resource: corev1.ResourceCPU}: 40_000,
						},
						Usage: resources.FlavorResourceQuantities{
							{Flavor: "arm", Resource: corev1.ResourceCPU}: 15_000,
							{Flavor: "x86", Resource: corev1.ResourceCPU}: 10_000,
						},
					},
				}
				return Snapshot{
					Manager: hierarchy.NewManagerForTest(
						map[kueue.CohortReference]*CohortSnapshot{
							"lending": cohort,
						},
						map[kueue.ClusterQueueReference]*ClusterQueueSnapshot{
							"a": {
								Name:                          "a",
								AllocatableResourceGeneration: 2,
								ResourceGroups: []ResourceGroup{
									{
										CoveredResources: sets.New(corev1.ResourceCPU),
										Flavors:          []kueue.ResourceFlavorReference{"arm", "x86"},
										LabelKeys:        sets.New("arch"),
									},
								},
								ResourceNode: resourceNode{
									Quotas: map[resources.FlavorResource]ResourceQuota{
										{Flavor: "arm", Resource: corev1.ResourceCPU}: {Nominal: 10_000, BorrowingLimit: nil, LendingLimit: nil},
										{Flavor: "x86", Resource: corev1.ResourceCPU}: {Nominal: 20_000, BorrowingLimit: nil, LendingLimit: nil},
									},
									SubtreeQuota: resources.FlavorResourceQuantities{
										{Flavor: "arm", Resource: corev1.ResourceCPU}: 10_000,
										{Flavor: "x86", Resource: corev1.ResourceCPU}: 20_000,
									},
									Usage: resources.FlavorResourceQuantities{
										{Flavor: "arm", Resource: corev1.ResourceCPU}: 15_000,
										{Flavor: "x86", Resource: corev1.ResourceCPU}: 10_000,
									},
								},
								FlavorFungibility: defaultFlavorFungibility,
								FairWeight:        oneQuantity,
								Workloads: map[workload.Reference]*workload.Info{
									"/alpha": workload.NewInfo(utiltesting.MakeWorkload("alpha", "").
										PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 5).
											Request(corev1.ResourceCPU, "2").Obj()).
										ReserveQuota(utiltesting.MakeAdmission("a", kueue.DefaultPodSetName).
											Assignment(corev1.ResourceCPU, "arm", "10000m").
											AssignmentPodCount(5).Obj()).
										Obj()),
									"/beta": workload.NewInfo(utiltesting.MakeWorkload("beta", "").
										PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 5).
											Request(corev1.ResourceCPU, "1").Obj()).
										ReserveQuota(utiltesting.MakeAdmission("a", kueue.DefaultPodSetName).
											Assignment(corev1.ResourceCPU, "arm", "5000m").
											AssignmentPodCount(5).Obj()).
										Obj()),
									"/gamma": workload.NewInfo(utiltesting.MakeWorkload("gamma", "").
										PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 5).
											Request(corev1.ResourceCPU, "2").Obj()).
										ReserveQuota(utiltesting.MakeAdmission("a", kueue.DefaultPodSetName).
											Assignment(corev1.ResourceCPU, "x86", "10000m").
											AssignmentPodCount(5).Obj()).
										Obj()),
								},
								Preemption:        defaultPreemption,
								NamespaceSelector: labels.Everything(),
								Status:            active,
							},
							"b": {
								Name:                          "b",
								AllocatableResourceGeneration: 1,
								ResourceGroups: []ResourceGroup{
									{
										CoveredResources: sets.New(corev1.ResourceCPU),
										Flavors:          []kueue.ResourceFlavorReference{"arm", "x86"},
										LabelKeys:        sets.New("arch"),
									},
								},
								ResourceNode: resourceNode{
									Quotas: map[resources.FlavorResource]ResourceQuota{
										{Flavor: "arm", Resource: corev1.ResourceCPU}: {Nominal: 10_000, BorrowingLimit: nil, LendingLimit: nil},
										{Flavor: "x86", Resource: corev1.ResourceCPU}: {Nominal: 20_000, BorrowingLimit: nil, LendingLimit: nil},
									},
									SubtreeQuota: resources.FlavorResourceQuantities{
										{Flavor: "arm", Resource: corev1.ResourceCPU}: 10_000,
										{Flavor: "x86", Resource: corev1.ResourceCPU}: 20_000,
									},
								},
								FlavorFungibility: defaultFlavorFungibility,
								FairWeight:        oneQuantity,
								Preemption:        defaultPreemption,
								NamespaceSelector: labels.Everything(),
								Status:            active,
							},
						},
					),
					ResourceFlavors: map[kueue.ResourceFlavorReference]*kueue.ResourceFlavor{
						"arm": utiltesting.MakeResourceFlavor("arm").NodeLabel("arch", "arm").Obj(),
						"x86": utiltesting.MakeResourceFlavor("x86").NodeLabel("arch", "x86").Obj(),
					},
				}
			}(),
		},
		"cohort provides resources": {
			rfs: []*kueue.ResourceFlavor{
				utiltesting.MakeResourceFlavor("arm").Obj(),
				utiltesting.MakeResourceFlavor("x86").Obj(),
				utiltesting.MakeResourceFlavor("mips").Obj(),
			},
			cqs: []*kueue.ClusterQueue{
				utiltesting.MakeClusterQueue("cq").
					Cohort("cohort").
					ResourceGroup(
						*utiltesting.MakeFlavorQuotas("arm").Resource(corev1.ResourceCPU, "7", "", "3").Obj(),
						*utiltesting.MakeFlavorQuotas("x86").Resource(corev1.ResourceCPU, "5").Obj(),
					).
					Obj(),
			},
			cohorts: []*kueue.Cohort{
				utiltesting.MakeCohort("cohort").
					ResourceGroup(
						*utiltesting.MakeFlavorQuotas("arm").Resource(corev1.ResourceCPU, "10").Obj(),
						*utiltesting.MakeFlavorQuotas("x86").Resource(corev1.ResourceCPU, "20").Obj(),
					).
					ResourceGroup(
						*utiltesting.MakeFlavorQuotas("mips").Resource(corev1.ResourceCPU, "42").Obj(),
					).Obj(),
			},
			wantSnapshot: Snapshot{
				Manager: hierarchy.NewManagerForTest(
					map[kueue.CohortReference]*CohortSnapshot{
						"cohort": {
							Name: "cohort",
							ResourceNode: resourceNode{
								Quotas: map[resources.FlavorResource]ResourceQuota{
									{Flavor: "arm", Resource: corev1.ResourceCPU}:  {Nominal: 10_000, BorrowingLimit: nil, LendingLimit: nil},
									{Flavor: "x86", Resource: corev1.ResourceCPU}:  {Nominal: 20_000, BorrowingLimit: nil, LendingLimit: nil},
									{Flavor: "mips", Resource: corev1.ResourceCPU}: {Nominal: 42_000, BorrowingLimit: nil, LendingLimit: nil},
								},
								SubtreeQuota: resources.FlavorResourceQuantities{
									{Flavor: "arm", Resource: corev1.ResourceCPU}:  13_000,
									{Flavor: "x86", Resource: corev1.ResourceCPU}:  25_000,
									{Flavor: "mips", Resource: corev1.ResourceCPU}: 42_000,
								},
							},
							FairWeight: oneQuantity,
						},
					},
					map[kueue.ClusterQueueReference]*ClusterQueueSnapshot{
						"cq": {
							Name:                          "cq",
							AllocatableResourceGeneration: 2,
							ResourceGroups: []ResourceGroup{
								{
									CoveredResources: sets.New(corev1.ResourceCPU),
									Flavors:          []kueue.ResourceFlavorReference{"arm", "x86"},
								},
							},
							ResourceNode: resourceNode{
								Quotas: map[resources.FlavorResource]ResourceQuota{
									{Flavor: "arm", Resource: corev1.ResourceCPU}: {Nominal: 7_000, BorrowingLimit: nil, LendingLimit: ptr.To[int64](3_000)},
									{Flavor: "x86", Resource: corev1.ResourceCPU}: {Nominal: 5_000, BorrowingLimit: nil, LendingLimit: nil},
								},
								SubtreeQuota: resources.FlavorResourceQuantities{
									{Flavor: "arm", Resource: corev1.ResourceCPU}: 7_000,
									{Flavor: "x86", Resource: corev1.ResourceCPU}: 5_000,
								},
							},
							FlavorFungibility: defaultFlavorFungibility,
							FairWeight:        oneQuantity,
							Preemption:        defaultPreemption,
							NamespaceSelector: labels.Everything(),
							Status:            active,
						},
					},
				),
				ResourceFlavors: map[kueue.ResourceFlavorReference]*kueue.ResourceFlavor{
					"arm":  utiltesting.MakeResourceFlavor("arm").Obj(),
					"x86":  utiltesting.MakeResourceFlavor("x86").Obj(),
					"mips": utiltesting.MakeResourceFlavor("mips").Obj(),
				},
			},
		},
		"cohorts with cycles and their cqs excluded from snapshot": {
			rfs: []*kueue.ResourceFlavor{
				utiltesting.MakeResourceFlavor("arm").Obj(),
			},
			cohorts: []*kueue.Cohort{
				utiltesting.MakeCohort("autocycle").Parent("autocycle").Obj(),
				utiltesting.MakeCohort("cycle-a").Parent("cycle-b").Obj(),
				utiltesting.MakeCohort("cycle-b").Parent("cycle-a").Obj(),
				utiltesting.MakeCohort("nocycle").Obj(),
			},
			cqs: []*kueue.ClusterQueue{
				utiltesting.MakeClusterQueue("cq-autocycle").
					Cohort("autocycle").
					ResourceGroup(
						*utiltesting.MakeFlavorQuotas("arm").Resource(corev1.ResourceCPU, "0").Obj(),
					).Obj(),
				utiltesting.MakeClusterQueue("cq-a").
					Cohort("cycle-a").
					ResourceGroup(
						*utiltesting.MakeFlavorQuotas("arm").Resource(corev1.ResourceCPU, "0").Obj(),
					).Obj(),
				utiltesting.MakeClusterQueue("cq-b").
					Cohort("cycle-b").
					ResourceGroup(
						*utiltesting.MakeFlavorQuotas("arm").Resource(corev1.ResourceCPU, "0").Obj(),
					).Obj(),
				utiltesting.MakeClusterQueue("cq-nocycle").
					Cohort("nocycle").
					ResourceGroup(
						*utiltesting.MakeFlavorQuotas("arm").Resource(corev1.ResourceCPU, "0").Obj(),
					).Obj(),
			},
			wantSnapshot: Snapshot{
				Manager: hierarchy.NewManagerForTest(
					map[kueue.CohortReference]*CohortSnapshot{
						"nocycle": {
							Name: "nocycle",
							ResourceNode: resourceNode{
								SubtreeQuota: resources.FlavorResourceQuantities{
									{Flavor: "arm", Resource: corev1.ResourceCPU}: 0,
								},
							},
							FairWeight: oneQuantity,
						},
					},
					map[kueue.ClusterQueueReference]*ClusterQueueSnapshot{
						"cq-nocycle": {
							Name:                          "cq-nocycle",
							AllocatableResourceGeneration: 2,
							ResourceGroups: []ResourceGroup{
								{
									CoveredResources: sets.New(corev1.ResourceCPU),
									Flavors:          []kueue.ResourceFlavorReference{"arm"},
								},
							},
							ResourceNode: resourceNode{
								Quotas: map[resources.FlavorResource]ResourceQuota{
									{Flavor: "arm", Resource: corev1.ResourceCPU}: {Nominal: 0},
								},
								SubtreeQuota: resources.FlavorResourceQuantities{
									{Flavor: "arm", Resource: corev1.ResourceCPU}: 0,
								},
							},
							FlavorFungibility: defaultFlavorFungibility,
							FairWeight:        oneQuantity,
							Preemption:        defaultPreemption,
							NamespaceSelector: labels.Everything(),
							Status:            active,
						},
					},
				),
				InactiveClusterQueueSets: sets.New[kueue.ClusterQueueReference]("cq-autocycle", "cq-a", "cq-b"),
				ResourceFlavors: map[kueue.ResourceFlavorReference]*kueue.ResourceFlavor{
					"arm": utiltesting.MakeResourceFlavor("arm").Obj(),
				},
			},
		},
		"cohort snapshot has fair sharing weight": {
			cohorts: []*kueue.Cohort{
				utiltesting.MakeCohort("cohort").FairWeight(resource.MustParse("0.5")).Obj(),
			},
			wantSnapshot: Snapshot{
				Manager: hierarchy.NewManagerForTest(
					map[kueue.CohortReference]*CohortSnapshot{
						"cohort": {
							Name:       "cohort",
							FairWeight: resource.MustParse("0.5"),
						},
					},
					map[kueue.ClusterQueueReference]*ClusterQueueSnapshot{},
				),
			},
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			ctx, log := utiltesting.ContextWithLog(t)
			if tc.disableLendingLimit {
				features.SetFeatureGateDuringTest(t, features.LendingLimit, false)
			}
			cache := New(utiltesting.NewFakeClient())
			for _, cq := range tc.cqs {
				// Purposely do not make a copy of clusterQueues. Clones of necessary fields are
				// done in AddClusterQueue.
				if err := cache.AddClusterQueue(ctx, cq); err != nil {
					t.Fatalf("Failed adding ClusterQueue: %v", err)
				}
			}
			for _, cohort := range tc.cohorts {
				_ = cache.AddOrUpdateCohort(cohort)
			}
			for _, rf := range tc.rfs {
				cache.AddOrUpdateResourceFlavor(log, rf)
			}
			for _, wl := range tc.wls {
				cache.AddOrUpdateWorkload(log, wl)
			}
			snapshot, err := cache.Snapshot(ctx)
			if err != nil {
				t.Fatalf("unexpected error while building snapshot: %v", err)
			}
			if diff := cmp.Diff(tc.wantSnapshot, *snapshot, snapCmpOpts...); len(diff) != 0 {
				t.Errorf("Unexpected Snapshot (-want,+got):\n%s", diff)
			}
			for _, cq := range snapshot.ClusterQueues() {
				for i := range cq.ResourceGroups {
					rg := &cq.ResourceGroups[i]
					for rName := range rg.CoveredResources {
						if cq.RGByResource(rName) != rg {
							t.Errorf("RGByResource[%s] does return its resource group", rName)
						}
					}
				}
			}
		})
	}
}

func TestSnapshotAddRemoveWorkload(t *testing.T) {
	flavors := []*kueue.ResourceFlavor{
		utiltesting.MakeResourceFlavor("default").Obj(),
		utiltesting.MakeResourceFlavor("alpha").Obj(),
		utiltesting.MakeResourceFlavor("beta").Obj(),
	}
	clusterQueues := []*kueue.ClusterQueue{
		utiltesting.MakeClusterQueue("c1").
			Cohort("cohort").
			ResourceGroup(
				*utiltesting.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "6").Obj(),
			).
			ResourceGroup(
				*utiltesting.MakeFlavorQuotas("alpha").Resource(corev1.ResourceMemory, "6Gi").Obj(),
				*utiltesting.MakeFlavorQuotas("beta").Resource(corev1.ResourceMemory, "6Gi").Obj(),
			).
			Obj(),
		utiltesting.MakeClusterQueue("c2").
			Cohort("cohort").
			ResourceGroup(
				*utiltesting.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "6").Obj(),
			).
			Obj(),
	}
	workloads := []kueue.Workload{
		*utiltesting.MakeWorkload("c1-cpu", "").
			Request(corev1.ResourceCPU, "1").
			ReserveQuota(utiltesting.MakeAdmission("c1").Assignment(corev1.ResourceCPU, "default", "1000m").Obj()).
			Obj(),
		*utiltesting.MakeWorkload("c1-memory-alpha", "").
			Request(corev1.ResourceMemory, "1Gi").
			ReserveQuota(utiltesting.MakeAdmission("c1").Assignment(corev1.ResourceMemory, "alpha", "1Gi").Obj()).
			Obj(),
		*utiltesting.MakeWorkload("c1-memory-beta", "").
			Request(corev1.ResourceMemory, "1Gi").
			ReserveQuota(utiltesting.MakeAdmission("c1").Assignment(corev1.ResourceMemory, "beta", "1Gi").Obj()).
			Obj(),
		*utiltesting.MakeWorkload("c2-cpu-1", "").
			Request(corev1.ResourceCPU, "1").
			ReserveQuota(utiltesting.MakeAdmission("c2").Assignment(corev1.ResourceCPU, "default", "1000m").Obj()).
			Obj(),
		*utiltesting.MakeWorkload("c2-cpu-2", "").
			Request(corev1.ResourceCPU, "1").
			ReserveQuota(utiltesting.MakeAdmission("c2").Assignment(corev1.ResourceCPU, "default", "1000m").Obj()).
			Obj(),
	}

	ctx, log := utiltesting.ContextWithLog(t)
	cl := utiltesting.NewClientBuilder().WithLists(&kueue.WorkloadList{Items: workloads}).Build()

	cqCache := New(cl)
	for _, flv := range flavors {
		cqCache.AddOrUpdateResourceFlavor(log, flv)
	}
	for _, cq := range clusterQueues {
		if err := cqCache.AddClusterQueue(ctx, cq); err != nil {
			t.Fatalf("Couldn't add ClusterQueue to cache: %v", err)
		}
	}
	wlInfos := make(map[workload.Reference]*workload.Info, len(workloads))
	for _, cq := range cqCache.hm.ClusterQueues() {
		for _, wl := range cq.Workloads {
			wlInfos[workload.Key(wl.Obj)] = wl
		}
	}
	initialSnapshot, err := cqCache.Snapshot(ctx)
	if err != nil {
		t.Fatalf("unexpected error while building snapshot: %v", err)
	}
	initialCohortResources := initialSnapshot.ClusterQueue("c1").Parent().ResourceNode.SubtreeQuota
	cases := map[string]struct {
		remove []workload.Reference
		add    []workload.Reference
		want   Snapshot
	}{
		"no-op remove add": {
			remove: []workload.Reference{"/c1-cpu", "/c2-cpu-1"},
			add:    []workload.Reference{"/c1-cpu", "/c2-cpu-1"},
			want:   *initialSnapshot,
		},
		"remove all": {
			remove: []workload.Reference{"/c1-cpu", "/c1-memory-alpha", "/c1-memory-beta", "/c2-cpu-1", "/c2-cpu-2"},
			want: func() Snapshot {
				cohort := &CohortSnapshot{
					Name: "cohort",
					ResourceNode: resourceNode{
						Usage: resources.FlavorResourceQuantities{
							{Flavor: "default", Resource: corev1.ResourceCPU}:  0,
							{Flavor: "alpha", Resource: corev1.ResourceMemory}: 0,
							{Flavor: "beta", Resource: corev1.ResourceMemory}:  0,
						},
						SubtreeQuota: initialCohortResources,
					},
				}
				return Snapshot{
					Manager: hierarchy.NewManagerForTest(
						map[kueue.CohortReference]*CohortSnapshot{
							"cohort": cohort,
						},
						map[kueue.ClusterQueueReference]*ClusterQueueSnapshot{
							"c1": {
								Name:              "c1",
								Workloads:         make(map[workload.Reference]*workload.Info),
								ResourceGroups:    cqCache.hm.ClusterQueue("c1").ResourceGroups,
								FlavorFungibility: defaultFlavorFungibility,
								FairWeight:        oneQuantity,
								ResourceNode: resourceNode{
									Usage: resources.FlavorResourceQuantities{
										{Flavor: "default", Resource: corev1.ResourceCPU}:  0,
										{Flavor: "alpha", Resource: corev1.ResourceMemory}: 0,
										{Flavor: "beta", Resource: corev1.ResourceMemory}:  0,
									},
									SubtreeQuota: resources.FlavorResourceQuantities{
										{Flavor: "default", Resource: corev1.ResourceCPU}:  6_000,
										{Flavor: "alpha", Resource: corev1.ResourceMemory}: utiltesting.Gi * 6,
										{Flavor: "beta", Resource: corev1.ResourceMemory}:  utiltesting.Gi * 6,
									},
								},
							},
							"c2": {
								Name:                          "c2",
								Workloads:                     make(map[workload.Reference]*workload.Info),
								ResourceGroups:                cqCache.hm.ClusterQueue("c2").ResourceGroups,
								FlavorFungibility:             defaultFlavorFungibility,
								FairWeight:                    oneQuantity,
								AllocatableResourceGeneration: 1,
								ResourceNode: resourceNode{
									Usage: resources.FlavorResourceQuantities{
										{Flavor: "default", Resource: corev1.ResourceCPU}: 0,
									},
									SubtreeQuota: resources.FlavorResourceQuantities{
										{Flavor: "default", Resource: corev1.ResourceCPU}: 6_000,
									},
								},
							},
						},
					),
				}
			}(),
		},
		"remove c1-cpu": {
			remove: []workload.Reference{"/c1-cpu"},
			want: func() Snapshot {
				cohort := &CohortSnapshot{
					Name: "cohort",
					ResourceNode: resourceNode{
						Usage: resources.FlavorResourceQuantities{
							{Flavor: "default", Resource: corev1.ResourceCPU}:  2_000,
							{Flavor: "alpha", Resource: corev1.ResourceMemory}: utiltesting.Gi,
							{Flavor: "beta", Resource: corev1.ResourceMemory}:  utiltesting.Gi,
						},
						SubtreeQuota: initialCohortResources,
					},
				}
				return Snapshot{
					Manager: hierarchy.NewManagerForTest(
						map[kueue.CohortReference]*CohortSnapshot{
							"cohort": cohort,
						},
						map[kueue.ClusterQueueReference]*ClusterQueueSnapshot{
							"c1": {
								Name: "c1",
								Workloads: map[workload.Reference]*workload.Info{
									"/c1-memory-alpha": nil,
									"/c1-memory-beta":  nil,
								},
								ResourceGroups:    cqCache.hm.ClusterQueue("c1").ResourceGroups,
								FlavorFungibility: defaultFlavorFungibility,
								FairWeight:        oneQuantity,
								ResourceNode: resourceNode{
									Usage: resources.FlavorResourceQuantities{
										{Flavor: "default", Resource: corev1.ResourceCPU}:  0,
										{Flavor: "alpha", Resource: corev1.ResourceMemory}: utiltesting.Gi,
										{Flavor: "beta", Resource: corev1.ResourceMemory}:  utiltesting.Gi,
									},
									SubtreeQuota: resources.FlavorResourceQuantities{
										{Flavor: "default", Resource: corev1.ResourceCPU}:  6_000,
										{Flavor: "alpha", Resource: corev1.ResourceMemory}: utiltesting.Gi * 6,
										{Flavor: "beta", Resource: corev1.ResourceMemory}:  utiltesting.Gi * 6,
									},
								},
							},
							"c2": {
								Name: "c2",
								Workloads: map[workload.Reference]*workload.Info{
									"/c2-cpu-1": nil,
									"/c2-cpu-2": nil,
								},
								ResourceGroups:                cqCache.hm.ClusterQueue("c2").ResourceGroups,
								FlavorFungibility:             defaultFlavorFungibility,
								FairWeight:                    oneQuantity,
								AllocatableResourceGeneration: 1,
								ResourceNode: resourceNode{
									Usage: resources.FlavorResourceQuantities{
										{Flavor: "default", Resource: corev1.ResourceCPU}: 2_000,
									},
									SubtreeQuota: resources.FlavorResourceQuantities{
										{Flavor: "default", Resource: corev1.ResourceCPU}: 6_000,
									},
								},
							},
						},
					),
				}
			}(),
		},
		"remove c1-memory-alpha": {
			remove: []workload.Reference{"/c1-memory-alpha"},
			want: func() Snapshot {
				cohort := &CohortSnapshot{
					Name: "cohort",
					ResourceNode: resourceNode{
						Usage: resources.FlavorResourceQuantities{
							{Flavor: "default", Resource: corev1.ResourceCPU}:  3_000,
							{Flavor: "alpha", Resource: corev1.ResourceMemory}: 0,
							{Flavor: "beta", Resource: corev1.ResourceMemory}:  utiltesting.Gi,
						},
						SubtreeQuota: initialCohortResources,
					},
				}
				return Snapshot{
					Manager: hierarchy.NewManagerForTest(
						map[kueue.CohortReference]*CohortSnapshot{
							"cohort": cohort,
						},
						map[kueue.ClusterQueueReference]*ClusterQueueSnapshot{
							"c1": {
								Name: "c1",
								Workloads: map[workload.Reference]*workload.Info{
									"/c1-memory-alpha": nil,
									"/c1-memory-beta":  nil,
								},
								ResourceGroups:    cqCache.hm.ClusterQueue("c1").ResourceGroups,
								FlavorFungibility: defaultFlavorFungibility,
								FairWeight:        oneQuantity,
								ResourceNode: resourceNode{
									Usage: resources.FlavorResourceQuantities{
										{Flavor: "default", Resource: corev1.ResourceCPU}:  1_000,
										{Flavor: "alpha", Resource: corev1.ResourceMemory}: 0,
										{Flavor: "beta", Resource: corev1.ResourceMemory}:  utiltesting.Gi,
									},
									SubtreeQuota: resources.FlavorResourceQuantities{
										{Flavor: "default", Resource: corev1.ResourceCPU}:  6_000,
										{Flavor: "alpha", Resource: corev1.ResourceMemory}: utiltesting.Gi * 6,
										{Flavor: "beta", Resource: corev1.ResourceMemory}:  utiltesting.Gi * 6,
									},
								},
							},
							"c2": {
								Name: "c2",
								Workloads: map[workload.Reference]*workload.Info{
									"/c2-cpu-1": nil,
									"/c2-cpu-2": nil,
								},
								ResourceGroups:    cqCache.hm.ClusterQueue("c2").ResourceGroups,
								FlavorFungibility: defaultFlavorFungibility,
								FairWeight:        oneQuantity,
								ResourceNode: resourceNode{
									Usage: resources.FlavorResourceQuantities{
										{Flavor: "default", Resource: corev1.ResourceCPU}: 2_000,
									},
									SubtreeQuota: resources.FlavorResourceQuantities{
										{Flavor: "default", Resource: corev1.ResourceCPU}: 6_000,
									},
								},
							},
						},
					),
				}
			}(),
		},
	}
	cmpOpts := append(snapCmpOpts,
		cmpopts.IgnoreFields(ClusterQueueSnapshot{}, "NamespaceSelector", "Preemption", "Status", "AllocatableResourceGeneration"),
		cmpopts.IgnoreFields(resourceNode{}, "Quotas"),
		cmpopts.IgnoreFields(Snapshot{}, "ResourceFlavors"),
		cmpopts.IgnoreTypes(&workload.Info{}))
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			snap, err := cqCache.Snapshot(ctx)
			if err != nil {
				t.Fatalf("unexpected error while building snapshot: %v", err)
			}
			for _, name := range tc.remove {
				snap.RemoveWorkload(wlInfos[name])
			}
			for _, name := range tc.add {
				snap.AddWorkload(wlInfos[name])
			}
			if diff := cmp.Diff(tc.want, *snap, cmpOpts...); diff != "" {
				t.Errorf("Unexpected snapshot state after operations (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestSnapshotAddRemoveWorkloadWithLendingLimit(t *testing.T) {
	flavors := []*kueue.ResourceFlavor{
		utiltesting.MakeResourceFlavor("default").Obj(),
	}
	clusterQueues := []*kueue.ClusterQueue{
		utiltesting.MakeClusterQueue("lend-a").
			Cohort("lend").
			ResourceGroup(
				*utiltesting.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "10", "", "4").Obj(),
			).
			Preemption(kueue.ClusterQueuePreemption{
				WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
				ReclaimWithinCohort: kueue.PreemptionPolicyLowerPriority,
			}).
			Obj(),
		utiltesting.MakeClusterQueue("lend-b").
			Cohort("lend").
			ResourceGroup(
				*utiltesting.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "10", "", "6").Obj(),
			).
			Preemption(kueue.ClusterQueuePreemption{
				WithinClusterQueue:  kueue.PreemptionPolicyNever,
				ReclaimWithinCohort: kueue.PreemptionPolicyAny,
			}).
			Obj(),
	}
	workloads := []kueue.Workload{
		*utiltesting.MakeWorkload("lend-a-1", "").
			Request(corev1.ResourceCPU, "1").
			ReserveQuota(utiltesting.MakeAdmission("lend-a").Assignment(corev1.ResourceCPU, "default", "1").Obj()).
			Obj(),
		*utiltesting.MakeWorkload("lend-a-2", "").
			Request(corev1.ResourceCPU, "9").
			ReserveQuota(utiltesting.MakeAdmission("lend-a").Assignment(corev1.ResourceCPU, "default", "9").Obj()).
			Obj(),
		*utiltesting.MakeWorkload("lend-a-3", "").
			Request(corev1.ResourceCPU, "6").
			ReserveQuota(utiltesting.MakeAdmission("lend-a").Assignment(corev1.ResourceCPU, "default", "6").Obj()).
			Obj(),
		*utiltesting.MakeWorkload("lend-b-1", "").
			Request(corev1.ResourceCPU, "4").
			ReserveQuota(utiltesting.MakeAdmission("lend-b").Assignment(corev1.ResourceCPU, "default", "4").Obj()).
			Obj(),
	}

	ctx, log := utiltesting.ContextWithLog(t)
	cl := utiltesting.NewClientBuilder().WithLists(&kueue.WorkloadList{Items: workloads}).Build()

	cqCache := New(cl)
	for _, flv := range flavors {
		cqCache.AddOrUpdateResourceFlavor(log, flv)
	}
	for _, cq := range clusterQueues {
		if err := cqCache.AddClusterQueue(ctx, cq); err != nil {
			t.Fatalf("Couldn't add ClusterQueue to cache: %v", err)
		}
	}
	wlInfos := make(map[workload.Reference]*workload.Info, len(workloads))
	for _, cq := range cqCache.hm.ClusterQueues() {
		for _, wl := range cq.Workloads {
			wlInfos[workload.Key(wl.Obj)] = wl
		}
	}
	initialSnapshot, err := cqCache.Snapshot(ctx)
	if err != nil {
		t.Fatalf("unexpected error while building snapshot: %v", err)
	}
	initialCohortResources := initialSnapshot.ClusterQueue("lend-a").Parent().ResourceNode.SubtreeQuota
	cases := map[string]struct {
		remove []workload.Reference
		add    []workload.Reference
		want   Snapshot
	}{
		"remove all then add all": {
			remove: []workload.Reference{"/lend-a-1", "/lend-a-2", "/lend-a-3", "/lend-b-1"},
			add:    []workload.Reference{"/lend-a-1", "/lend-a-2", "/lend-a-3", "/lend-b-1"},
			want:   *initialSnapshot,
		},
		"remove all": {
			remove: []workload.Reference{"/lend-a-1", "/lend-a-2", "/lend-a-3", "/lend-b-1"},
			want: func() Snapshot {
				cohort := &CohortSnapshot{
					Name: "lend",
					ResourceNode: resourceNode{
						Usage: resources.FlavorResourceQuantities{
							{Flavor: "default", Resource: corev1.ResourceCPU}: 0,
						},
						SubtreeQuota: initialCohortResources,
					},
				}
				return Snapshot{
					Manager: hierarchy.NewManagerForTest(
						map[kueue.CohortReference]*CohortSnapshot{
							"lend": cohort,
						},
						map[kueue.ClusterQueueReference]*ClusterQueueSnapshot{
							"lend-a": {
								Name:              "lend-a",
								Workloads:         make(map[workload.Reference]*workload.Info),
								ResourceGroups:    cqCache.hm.ClusterQueue("lend-a").ResourceGroups,
								FlavorFungibility: defaultFlavorFungibility,
								FairWeight:        oneQuantity,
								ResourceNode: resourceNode{
									Usage: resources.FlavorResourceQuantities{
										{Flavor: "default", Resource: corev1.ResourceCPU}: 0,
									},
									SubtreeQuota: resources.FlavorResourceQuantities{
										{Flavor: "default", Resource: corev1.ResourceCPU}: 10_000,
									},
								},
							},
							"lend-b": {
								Name:              "lend-b",
								Workloads:         make(map[workload.Reference]*workload.Info),
								ResourceGroups:    cqCache.hm.ClusterQueue("lend-b").ResourceGroups,
								FlavorFungibility: defaultFlavorFungibility,
								FairWeight:        oneQuantity,
								ResourceNode: resourceNode{
									Usage: resources.FlavorResourceQuantities{
										{Flavor: "default", Resource: corev1.ResourceCPU}: 0,
									},
									SubtreeQuota: resources.FlavorResourceQuantities{
										{Flavor: "default", Resource: corev1.ResourceCPU}: 10_000,
									},
								},
							},
						},
					),
				}
			}(),
		},
		"remove workload, but still using quota over GuaranteedQuota": {
			remove: []workload.Reference{"/lend-a-2"},
			want: func() Snapshot {
				cohort := &CohortSnapshot{
					Name: "lend",
					ResourceNode: resourceNode{
						Usage: resources.FlavorResourceQuantities{
							{Flavor: "default", Resource: corev1.ResourceCPU}: 1_000,
						},
						SubtreeQuota: initialCohortResources,
					},
				}
				return Snapshot{
					Manager: hierarchy.NewManagerForTest(
						map[kueue.CohortReference]*CohortSnapshot{
							"lend": cohort,
						},
						map[kueue.ClusterQueueReference]*ClusterQueueSnapshot{
							"lend-a": {
								Name:              "lend-a",
								Workloads:         make(map[workload.Reference]*workload.Info),
								ResourceGroups:    cqCache.hm.ClusterQueue("lend-a").ResourceGroups,
								FlavorFungibility: defaultFlavorFungibility,
								FairWeight:        oneQuantity,
								ResourceNode: resourceNode{
									Usage: resources.FlavorResourceQuantities{
										{Flavor: "default", Resource: corev1.ResourceCPU}: 7_000,
									},
									SubtreeQuota: resources.FlavorResourceQuantities{
										{Flavor: "default", Resource: corev1.ResourceCPU}: 10_000,
									},
								},
							},
							"lend-b": {
								Name:                          "lend-b",
								Workloads:                     make(map[workload.Reference]*workload.Info),
								ResourceGroups:                cqCache.hm.ClusterQueue("lend-b").ResourceGroups,
								FlavorFungibility:             defaultFlavorFungibility,
								FairWeight:                    oneQuantity,
								AllocatableResourceGeneration: 1,
								ResourceNode: resourceNode{
									Usage: resources.FlavorResourceQuantities{
										{Flavor: "default", Resource: corev1.ResourceCPU}: 4_000,
									},
									SubtreeQuota: resources.FlavorResourceQuantities{
										{Flavor: "default", Resource: corev1.ResourceCPU}: 10_000,
									},
								},
							},
						},
					),
				}
			}(),
		},
		"remove wokload, using same quota as GuaranteedQuota": {
			remove: []workload.Reference{"/lend-a-1", "/lend-a-2"},
			want: func() Snapshot {
				cohort := &CohortSnapshot{
					Name: "lend",
					ResourceNode: resourceNode{
						Usage: resources.FlavorResourceQuantities{
							{Flavor: "default", Resource: corev1.ResourceCPU}: 0,
						},
						SubtreeQuota: initialCohortResources,
					},
				}
				return Snapshot{
					Manager: hierarchy.NewManagerForTest(
						map[kueue.CohortReference]*CohortSnapshot{
							"lend": cohort,
						},
						map[kueue.ClusterQueueReference]*ClusterQueueSnapshot{
							"lend-a": {
								Name:              "lend-a",
								Workloads:         make(map[workload.Reference]*workload.Info),
								ResourceGroups:    cqCache.hm.ClusterQueue("lend-a").ResourceGroups,
								FlavorFungibility: defaultFlavorFungibility,
								FairWeight:        oneQuantity,
								ResourceNode: resourceNode{
									Usage: resources.FlavorResourceQuantities{
										{Flavor: "default", Resource: corev1.ResourceCPU}: 6_000,
									},
									SubtreeQuota: resources.FlavorResourceQuantities{
										{Flavor: "default", Resource: corev1.ResourceCPU}: 10_000,
									},
								},
							},
							"lend-b": {
								Name:                          "lend-b",
								Workloads:                     make(map[workload.Reference]*workload.Info),
								ResourceGroups:                cqCache.hm.ClusterQueue("lend-b").ResourceGroups,
								FlavorFungibility:             defaultFlavorFungibility,
								FairWeight:                    oneQuantity,
								AllocatableResourceGeneration: 1,
								ResourceNode: resourceNode{
									Usage: resources.FlavorResourceQuantities{
										{Flavor: "default", Resource: corev1.ResourceCPU}: 4_000,
									},
									SubtreeQuota: resources.FlavorResourceQuantities{
										{Flavor: "default", Resource: corev1.ResourceCPU}: 10_000,
									},
								},
							},
						},
					),
				}
			}(),
		},
		"remove workload, using less quota than GuaranteedQuota": {
			remove: []workload.Reference{"/lend-a-2", "/lend-a-3"},
			want: func() Snapshot {
				cohort := &CohortSnapshot{
					Name: "lend",
					ResourceNode: resourceNode{
						Usage: resources.FlavorResourceQuantities{
							{Flavor: "default", Resource: corev1.ResourceCPU}: 0,
						},
						SubtreeQuota: initialCohortResources,
					},
				}
				return Snapshot{
					Manager: hierarchy.NewManagerForTest(
						map[kueue.CohortReference]*CohortSnapshot{
							"lend": cohort,
						},
						map[kueue.ClusterQueueReference]*ClusterQueueSnapshot{
							"lend-a": {
								Name:              "lend-a",
								Workloads:         make(map[workload.Reference]*workload.Info),
								ResourceGroups:    cqCache.hm.ClusterQueue("lend-a").ResourceGroups,
								FlavorFungibility: defaultFlavorFungibility,
								FairWeight:        oneQuantity,
								ResourceNode: resourceNode{
									Usage: resources.FlavorResourceQuantities{
										{Flavor: "default", Resource: corev1.ResourceCPU}: 1_000,
									},
									SubtreeQuota: resources.FlavorResourceQuantities{
										{Flavor: "default", Resource: corev1.ResourceCPU}: 10_000,
									},
								},
							},
							"lend-b": {
								Name:                          "lend-b",
								Workloads:                     make(map[workload.Reference]*workload.Info),
								ResourceGroups:                cqCache.hm.ClusterQueue("lend-b").ResourceGroups,
								FlavorFungibility:             defaultFlavorFungibility,
								FairWeight:                    oneQuantity,
								AllocatableResourceGeneration: 1,
								ResourceNode: resourceNode{
									Usage: resources.FlavorResourceQuantities{
										{Flavor: "default", Resource: corev1.ResourceCPU}: 4_000,
									},
									SubtreeQuota: resources.FlavorResourceQuantities{
										{Flavor: "default", Resource: corev1.ResourceCPU}: 10_000,
									},
								},
							},
						},
					),
				}
			}(),
		},
		"remove all then add workload, using less quota than GuaranteedQuota": {
			remove: []workload.Reference{"/lend-a-1", "/lend-a-2", "/lend-a-3", "/lend-b-1"},
			add:    []workload.Reference{"/lend-a-1"},
			want: func() Snapshot {
				cohort := &CohortSnapshot{
					Name: "lend",
					ResourceNode: resourceNode{
						Usage: resources.FlavorResourceQuantities{
							{Flavor: "default", Resource: corev1.ResourceCPU}: 0,
						},
						SubtreeQuota: initialCohortResources,
					},
				}
				return Snapshot{
					Manager: hierarchy.NewManagerForTest(
						map[kueue.CohortReference]*CohortSnapshot{
							"lend": cohort,
						},
						map[kueue.ClusterQueueReference]*ClusterQueueSnapshot{
							"lend-a": {
								Name:              "lend-a",
								Workloads:         make(map[workload.Reference]*workload.Info),
								ResourceGroups:    cqCache.hm.ClusterQueue("lend-a").ResourceGroups,
								FlavorFungibility: defaultFlavorFungibility,
								FairWeight:        oneQuantity,
								ResourceNode: resourceNode{
									Usage: resources.FlavorResourceQuantities{
										{Flavor: "default", Resource: corev1.ResourceCPU}: 1_000,
									},
									SubtreeQuota: resources.FlavorResourceQuantities{
										{Flavor: "default", Resource: corev1.ResourceCPU}: 10_000,
									},
								},
							},
							"lend-b": {
								Name:                          "lend-b",
								Workloads:                     make(map[workload.Reference]*workload.Info),
								ResourceGroups:                cqCache.hm.ClusterQueue("lend-b").ResourceGroups,
								FlavorFungibility:             defaultFlavorFungibility,
								FairWeight:                    oneQuantity,
								AllocatableResourceGeneration: 1,
								ResourceNode: resourceNode{
									Usage: resources.FlavorResourceQuantities{
										{Flavor: "default", Resource: corev1.ResourceCPU}: 0,
									},
									SubtreeQuota: resources.FlavorResourceQuantities{
										{Flavor: "default", Resource: corev1.ResourceCPU}: 10_000,
									},
								},
							},
						},
					),
				}
			}(),
		},
		"remove all then add workload, using same quota as GuaranteedQuota": {
			remove: []workload.Reference{"/lend-a-1", "/lend-a-2", "/lend-a-3", "/lend-b-1"},
			add:    []workload.Reference{"/lend-a-3"},
			want: func() Snapshot {
				cohort := &CohortSnapshot{
					Name: "lend",
					ResourceNode: resourceNode{
						Usage: resources.FlavorResourceQuantities{
							{Flavor: "default", Resource: corev1.ResourceCPU}: 0,
						},
						SubtreeQuota: initialCohortResources,
					},
				}
				return Snapshot{
					Manager: hierarchy.NewManagerForTest(
						map[kueue.CohortReference]*CohortSnapshot{
							"lend": cohort,
						},
						map[kueue.ClusterQueueReference]*ClusterQueueSnapshot{
							"lend-a": {
								Name:              "lend-a",
								Workloads:         make(map[workload.Reference]*workload.Info),
								ResourceGroups:    cqCache.hm.ClusterQueue("lend-a").ResourceGroups,
								FlavorFungibility: defaultFlavorFungibility,
								FairWeight:        oneQuantity,
								ResourceNode: resourceNode{
									Usage: resources.FlavorResourceQuantities{
										{Flavor: "default", Resource: corev1.ResourceCPU}: 6_000,
									},
									SubtreeQuota: resources.FlavorResourceQuantities{
										{Flavor: "default", Resource: corev1.ResourceCPU}: 10_000,
									},
								},
							},
							"lend-b": {
								Name:              "lend-b",
								Workloads:         make(map[workload.Reference]*workload.Info),
								ResourceGroups:    cqCache.hm.ClusterQueue("lend-b").ResourceGroups,
								FlavorFungibility: defaultFlavorFungibility,
								FairWeight:        oneQuantity,
								ResourceNode: resourceNode{
									Usage: resources.FlavorResourceQuantities{
										{Flavor: "default", Resource: corev1.ResourceCPU}: 0,
									},
									SubtreeQuota: resources.FlavorResourceQuantities{
										{Flavor: "default", Resource: corev1.ResourceCPU}: 10_000,
									},
								},
							},
						},
					),
				}
			}(),
		},
		"remove all then add workload, using quota over GuaranteedQuota": {
			remove: []workload.Reference{"/lend-a-1", "/lend-a-2", "/lend-a-3", "/lend-b-1"},
			add:    []workload.Reference{"/lend-a-2"},
			want: func() Snapshot {
				cohort := &CohortSnapshot{
					Name: "lend",
					ResourceNode: resourceNode{
						Usage: resources.FlavorResourceQuantities{
							{Flavor: "default", Resource: corev1.ResourceCPU}: 3_000,
						},
						SubtreeQuota: initialCohortResources,
					},
				}
				return Snapshot{
					Manager: hierarchy.NewManagerForTest(
						map[kueue.CohortReference]*CohortSnapshot{
							"lend": cohort,
						},
						map[kueue.ClusterQueueReference]*ClusterQueueSnapshot{
							"lend-a": {
								Name:              "lend-a",
								Workloads:         make(map[workload.Reference]*workload.Info),
								ResourceGroups:    cqCache.hm.ClusterQueue("lend-a").ResourceGroups,
								FlavorFungibility: defaultFlavorFungibility,
								FairWeight:        oneQuantity,
								ResourceNode: resourceNode{
									Usage: resources.FlavorResourceQuantities{
										{Flavor: "default", Resource: corev1.ResourceCPU}: 9_000,
									},
									SubtreeQuota: resources.FlavorResourceQuantities{
										{Flavor: "default", Resource: corev1.ResourceCPU}: 10_000,
									},
								},
							},
							"lend-b": {
								Name:                          "lend-b",
								Workloads:                     make(map[workload.Reference]*workload.Info),
								ResourceGroups:                cqCache.hm.ClusterQueue("lend-b").ResourceGroups,
								FlavorFungibility:             defaultFlavorFungibility,
								FairWeight:                    oneQuantity,
								AllocatableResourceGeneration: 1,
								ResourceNode: resourceNode{
									Usage: resources.FlavorResourceQuantities{
										{Flavor: "default", Resource: corev1.ResourceCPU}: 0,
									},
									SubtreeQuota: resources.FlavorResourceQuantities{
										{Flavor: "default", Resource: corev1.ResourceCPU}: 10_000,
									},
								},
							},
						},
					),
				}
			}(),
		},
	}
	cmpOpts := append(snapCmpOpts,
		cmpopts.IgnoreFields(ClusterQueueSnapshot{}, "NamespaceSelector", "Preemption", "Status", "AllocatableResourceGeneration"),
		cmpopts.IgnoreFields(resourceNode{}, "Quotas"),
		cmpopts.IgnoreFields(Snapshot{}, "ResourceFlavors"),
		cmpopts.IgnoreTypes(&workload.Info{}))
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			snap, err := cqCache.Snapshot(ctx)
			if err != nil {
				t.Fatalf("unexpected error while building snapshot: %v", err)
			}
			for _, name := range tc.remove {
				snap.RemoveWorkload(wlInfos[name])
			}
			for _, name := range tc.add {
				snap.AddWorkload(wlInfos[name])
			}
			if diff := cmp.Diff(tc.want, *snap, cmpOpts...); diff != "" {
				t.Errorf("Unexpected snapshot state after operations (-want,+got):\n%s", diff)
			}
		})
	}
}
