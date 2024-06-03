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

package cache

import (
	"context"
	"errors"
	"fmt"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/features"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	"sigs.k8s.io/kueue/pkg/workload"
)

func TestCacheClusterQueueOperations(t *testing.T) {
	initialClusterQueues := []kueue.ClusterQueue{
		*utiltesting.MakeClusterQueue("a").
			ResourceGroup(
				*utiltesting.MakeFlavorQuotas("default").
					Resource(corev1.ResourceCPU, "10", "10").Obj()).
			Cohort("one").
			NamespaceSelector(nil).
			Obj(),
		*utiltesting.MakeClusterQueue("b").
			ResourceGroup(
				*utiltesting.MakeFlavorQuotas("default").
					Resource(corev1.ResourceCPU, "15").Obj()).
			Cohort("one").
			NamespaceSelector(nil).
			Obj(),
		*utiltesting.MakeClusterQueue("c").
			Cohort("two").
			NamespaceSelector(nil).
			Obj(),
		*utiltesting.MakeClusterQueue("d").
			NamespaceSelector(nil).
			Obj(),
		*utiltesting.MakeClusterQueue("e").
			ResourceGroup(
				*utiltesting.MakeFlavorQuotas("nonexistent-flavor").
					Resource(corev1.ResourceCPU, "15").Obj()).
			Cohort("two").
			NamespaceSelector(nil).
			Obj(),
		*utiltesting.MakeClusterQueue("f").
			Cohort("two").
			NamespaceSelector(nil).
			FlavorFungibility(kueue.FlavorFungibility{
				WhenCanBorrow: kueue.TryNextFlavor,
			}).
			Obj(),
	}
	setup := func(cache *Cache) error {
		cache.AddOrUpdateResourceFlavor(
			utiltesting.MakeResourceFlavor("default").
				Label("cpuType", "default").
				Obj())
		for _, c := range initialClusterQueues {
			if err := cache.AddClusterQueue(context.Background(), &c); err != nil {
				return fmt.Errorf("Failed adding ClusterQueue: %w", err)
			}
		}
		return nil
	}
	cases := []struct {
		name               string
		operation          func(*Cache) error
		clientObjects      []client.Object
		wantClusterQueues  map[string]*ClusterQueue
		wantCohorts        map[string]sets.Set[string]
		enableLendingLimit bool
	}{
		{
			name: "add",
			operation: func(cache *Cache) error {
				return setup(cache)
			},
			wantClusterQueues: map[string]*ClusterQueue{
				"a": {
					Name:                          "a",
					AllocatableResourceGeneration: 1,
					ResourceGroups: []ResourceGroup{{
						CoveredResources: sets.New(corev1.ResourceCPU),
						Flavors: []FlavorQuotas{{
							Name: "default",
							Resources: map[corev1.ResourceName]*ResourceQuota{
								corev1.ResourceCPU: {
									Nominal:        10_000,
									BorrowingLimit: ptr.To[int64](10_000),
								},
							},
						}},
						LabelKeys: sets.New("cpuType"),
					}},
					NamespaceSelector: labels.Nothing(),
					FlavorFungibility: defaultFlavorFungibility,
					Usage: FlavorResourceQuantities{
						"default": {corev1.ResourceCPU: 0},
					},
					AdmittedUsage: FlavorResourceQuantities{
						"default": {corev1.ResourceCPU: 0},
					},
					Lendable: map[corev1.ResourceName]int64{
						corev1.ResourceCPU: 10_000,
					},
					Status:     active,
					Preemption: defaultPreemption,
					FairWeight: oneQuantity,
				},
				"b": {
					Name:                          "b",
					AllocatableResourceGeneration: 1,
					ResourceGroups: []ResourceGroup{{
						CoveredResources: sets.New(corev1.ResourceCPU),
						Flavors: []FlavorQuotas{{
							Name: "default",
							Resources: map[corev1.ResourceName]*ResourceQuota{
								corev1.ResourceCPU: {
									Nominal: 15_000,
								},
							},
						}},
						LabelKeys: sets.New("cpuType"),
					}},
					NamespaceSelector: labels.Nothing(),
					FlavorFungibility: defaultFlavorFungibility,
					Usage: FlavorResourceQuantities{
						"default": {corev1.ResourceCPU: 0},
					},
					AdmittedUsage: FlavorResourceQuantities{
						"default": {corev1.ResourceCPU: 0},
					},
					Lendable: map[corev1.ResourceName]int64{
						corev1.ResourceCPU: 15_000,
					},
					Status:     active,
					Preemption: defaultPreemption,
					FairWeight: oneQuantity,
				},
				"c": {
					Name:                          "c",
					AllocatableResourceGeneration: 1,
					ResourceGroups:                []ResourceGroup{},
					NamespaceSelector:             labels.Nothing(),
					FlavorFungibility:             defaultFlavorFungibility,
					Usage:                         FlavorResourceQuantities{},
					Status:                        active,
					Preemption:                    defaultPreemption,
					FairWeight:                    oneQuantity,
				},
				"d": {
					Name:                          "d",
					AllocatableResourceGeneration: 1,
					ResourceGroups:                []ResourceGroup{},
					NamespaceSelector:             labels.Nothing(),
					FlavorFungibility:             defaultFlavorFungibility,
					Usage:                         FlavorResourceQuantities{},
					Status:                        active,
					Preemption:                    defaultPreemption,
					FairWeight:                    oneQuantity,
				},
				"e": {
					Name:                          "e",
					AllocatableResourceGeneration: 1,
					ResourceGroups: []ResourceGroup{{
						CoveredResources: sets.New(corev1.ResourceCPU),
						Flavors: []FlavorQuotas{{
							Name: "nonexistent-flavor",
							Resources: map[corev1.ResourceName]*ResourceQuota{
								corev1.ResourceCPU: {
									Nominal: 15_000,
								},
							},
						}},
					}},
					NamespaceSelector: labels.Nothing(),
					FlavorFungibility: defaultFlavorFungibility,
					Usage: FlavorResourceQuantities{
						"nonexistent-flavor": {corev1.ResourceCPU: 0},
					},
					AdmittedUsage: FlavorResourceQuantities{
						"nonexistent-flavor": {corev1.ResourceCPU: 0},
					},
					Lendable: map[corev1.ResourceName]int64{
						corev1.ResourceCPU: 15_000,
					},
					Status:     pending,
					Preemption: defaultPreemption,
					FairWeight: oneQuantity,
				},
				"f": {
					Name:                          "f",
					AllocatableResourceGeneration: 1,
					ResourceGroups:                []ResourceGroup{},
					NamespaceSelector:             labels.Nothing(),
					Usage:                         FlavorResourceQuantities{},
					Status:                        active,
					Preemption:                    defaultPreemption,
					FlavorFungibility: kueue.FlavorFungibility{
						WhenCanBorrow:  kueue.TryNextFlavor,
						WhenCanPreempt: kueue.TryNextFlavor,
					},
					FairWeight: oneQuantity,
				},
			},
			wantCohorts: map[string]sets.Set[string]{
				"one": sets.New("a", "b"),
				"two": sets.New("c", "e", "f"),
			},
		},
		{
			name: "add ClusterQueue with preemption policies",
			operation: func(cache *Cache) error {
				cq := utiltesting.MakeClusterQueue("foo").Preemption(kueue.ClusterQueuePreemption{
					ReclaimWithinCohort: kueue.PreemptionPolicyLowerPriority,
					WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
				}).Obj()
				if err := cache.AddClusterQueue(context.Background(), cq); err != nil {
					return fmt.Errorf("Failed to add ClusterQueue: %w", err)
				}
				return nil
			},
			wantClusterQueues: map[string]*ClusterQueue{
				"foo": {
					Name:                          "foo",
					AllocatableResourceGeneration: 1,
					NamespaceSelector:             labels.Everything(),
					Status:                        active,
					FlavorFungibility:             defaultFlavorFungibility,
					Preemption: kueue.ClusterQueuePreemption{
						ReclaimWithinCohort: kueue.PreemptionPolicyLowerPriority,
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
					},
					FairWeight: oneQuantity,
				},
			},
		},
		{
			name: "add ClusterQueue with fair sharing weight",
			operation: func(cache *Cache) error {
				cq := utiltesting.MakeClusterQueue("foo").FairWeight(resource.MustParse("2")).Obj()
				if err := cache.AddClusterQueue(context.Background(), cq); err != nil {
					return fmt.Errorf("Failed to add ClusterQueue: %w", err)
				}
				return nil
			},
			wantClusterQueues: map[string]*ClusterQueue{
				"foo": {
					Name:                          "foo",
					AllocatableResourceGeneration: 1,
					NamespaceSelector:             labels.Everything(),
					Status:                        active,
					FlavorFungibility:             defaultFlavorFungibility,
					Preemption:                    defaultPreemption,
					FairWeight:                    resource.MustParse("2"),
				},
			},
		},
		{
			name: "add flavors after queue capacities",
			operation: func(cache *Cache) error {
				for _, c := range initialClusterQueues {
					if err := cache.AddClusterQueue(context.Background(), &c); err != nil {
						return fmt.Errorf("Failed adding ClusterQueue: %w", err)
					}
				}
				cache.AddOrUpdateResourceFlavor(
					utiltesting.MakeResourceFlavor("default").
						Label("cpuType", "default").
						Obj())
				return nil
			},
			wantClusterQueues: map[string]*ClusterQueue{
				"a": {
					Name:                          "a",
					AllocatableResourceGeneration: 1,
					ResourceGroups: []ResourceGroup{{
						CoveredResources: sets.New(corev1.ResourceCPU),
						Flavors: []FlavorQuotas{{
							Name: "default",
							Resources: map[corev1.ResourceName]*ResourceQuota{
								corev1.ResourceCPU: {
									Nominal:        10_000,
									BorrowingLimit: ptr.To[int64](10_000),
								},
							},
						}},
						LabelKeys: sets.New("cpuType"),
					}},
					FlavorFungibility: defaultFlavorFungibility,
					NamespaceSelector: labels.Nothing(),
					Usage: FlavorResourceQuantities{
						"default": {corev1.ResourceCPU: 0},
					},
					AdmittedUsage: FlavorResourceQuantities{
						"default": {corev1.ResourceCPU: 0},
					},
					Lendable: map[corev1.ResourceName]int64{
						corev1.ResourceCPU: 10_000,
					},
					Status:     active,
					Preemption: defaultPreemption,
					FairWeight: oneQuantity,
				},
				"b": {
					Name:                          "b",
					AllocatableResourceGeneration: 1,
					ResourceGroups: []ResourceGroup{{
						CoveredResources: sets.New(corev1.ResourceCPU),
						Flavors: []FlavorQuotas{{
							Name: "default",
							Resources: map[corev1.ResourceName]*ResourceQuota{
								corev1.ResourceCPU: {
									Nominal: 15_000,
								},
							},
						}},
						LabelKeys: sets.New("cpuType"),
					}},
					FlavorFungibility: defaultFlavorFungibility,
					NamespaceSelector: labels.Nothing(),
					Usage: FlavorResourceQuantities{
						"default": {corev1.ResourceCPU: 0},
					},
					AdmittedUsage: FlavorResourceQuantities{
						"default": {corev1.ResourceCPU: 0},
					},
					Lendable: map[corev1.ResourceName]int64{
						corev1.ResourceCPU: 15_000,
					},
					Status:     active,
					Preemption: defaultPreemption,
					FairWeight: oneQuantity,
				},
				"c": {
					Name:                          "c",
					AllocatableResourceGeneration: 1,
					ResourceGroups:                []ResourceGroup{},
					NamespaceSelector:             labels.Nothing(),
					FlavorFungibility:             defaultFlavorFungibility,
					Usage:                         FlavorResourceQuantities{},
					Status:                        active,
					Preemption:                    defaultPreemption,
					FairWeight:                    oneQuantity,
				},
				"d": {
					Name:                          "d",
					AllocatableResourceGeneration: 1,
					ResourceGroups:                []ResourceGroup{},
					NamespaceSelector:             labels.Nothing(),
					FlavorFungibility:             defaultFlavorFungibility,
					Usage:                         FlavorResourceQuantities{},
					Status:                        active,
					Preemption:                    defaultPreemption,
					FairWeight:                    oneQuantity,
				},
				"e": {
					Name:                          "e",
					AllocatableResourceGeneration: 1,
					ResourceGroups: []ResourceGroup{{
						CoveredResources: sets.New(corev1.ResourceCPU),
						Flavors: []FlavorQuotas{
							{
								Name: "nonexistent-flavor",
								Resources: map[corev1.ResourceName]*ResourceQuota{
									corev1.ResourceCPU: {
										Nominal: 15_000,
									},
								},
							},
						},
					}},
					NamespaceSelector: labels.Nothing(),
					FlavorFungibility: defaultFlavorFungibility,
					Usage: FlavorResourceQuantities{
						"nonexistent-flavor": {corev1.ResourceCPU: 0},
					},
					AdmittedUsage: FlavorResourceQuantities{
						"nonexistent-flavor": {corev1.ResourceCPU: 0},
					},
					Lendable: map[corev1.ResourceName]int64{
						corev1.ResourceCPU: 15_000,
					},
					Status:     pending,
					Preemption: defaultPreemption,
					FairWeight: oneQuantity,
				},
				"f": {
					Name:                          "f",
					AllocatableResourceGeneration: 1,
					ResourceGroups:                []ResourceGroup{},
					NamespaceSelector:             labels.Nothing(),
					Usage:                         FlavorResourceQuantities{},
					Status:                        active,
					Preemption:                    defaultPreemption,
					FlavorFungibility: kueue.FlavorFungibility{
						WhenCanBorrow:  kueue.TryNextFlavor,
						WhenCanPreempt: kueue.TryNextFlavor,
					},
					FairWeight: oneQuantity,
				},
			},
			wantCohorts: map[string]sets.Set[string]{
				"one": sets.New("a", "b"),
				"two": sets.New("c", "e", "f"),
			},
		},
		{
			name: "update",
			operation: func(cache *Cache) error {
				err := setup(cache)
				if err != nil {
					return err
				}
				clusterQueues := []kueue.ClusterQueue{
					*utiltesting.MakeClusterQueue("a").
						ResourceGroup(
							*utiltesting.MakeFlavorQuotas("default").
								Resource(corev1.ResourceCPU, "5", "5").Obj()).
						Cohort("two").
						NamespaceSelector(nil).
						Obj(),
					*utiltesting.MakeClusterQueue("b").Cohort("one").Obj(), // remove the only resource group and set a namespace selector.
					*utiltesting.MakeClusterQueue("e").
						ResourceGroup(
							*utiltesting.MakeFlavorQuotas("default").
								Resource(corev1.ResourceCPU, "5", "5", "4").
								Obj()).
						Cohort("two").
						NamespaceSelector(nil).
						Obj(),
				}
				for _, c := range clusterQueues {
					if err := cache.UpdateClusterQueue(&c); err != nil {
						return fmt.Errorf("Failed updating ClusterQueue: %w", err)
					}
				}
				cache.AddOrUpdateResourceFlavor(
					utiltesting.MakeResourceFlavor("default").
						Label("cpuType", "default").
						Label("region", "central").
						Obj())
				return nil
			},
			wantClusterQueues: map[string]*ClusterQueue{
				"a": {
					Name:                          "a",
					AllocatableResourceGeneration: 2,
					ResourceGroups: []ResourceGroup{{
						CoveredResources: sets.New(corev1.ResourceCPU),
						Flavors: []FlavorQuotas{{
							Name: "default",
							Resources: map[corev1.ResourceName]*ResourceQuota{
								corev1.ResourceCPU: {
									Nominal:        5_000,
									BorrowingLimit: ptr.To[int64](5_000),
								},
							},
						}},
						LabelKeys: sets.New("cpuType", "region"),
					}},
					NamespaceSelector: labels.Nothing(),
					FlavorFungibility: defaultFlavorFungibility,
					Usage: FlavorResourceQuantities{
						"default": {corev1.ResourceCPU: 0},
					},
					AdmittedUsage: FlavorResourceQuantities{
						"default": {corev1.ResourceCPU: 0},
					},
					Lendable: map[corev1.ResourceName]int64{
						corev1.ResourceCPU: 5_000,
					},
					Status:     active,
					Preemption: defaultPreemption,
					FairWeight: oneQuantity,
				},
				"b": {
					Name:                          "b",
					AllocatableResourceGeneration: 2,
					ResourceGroups:                []ResourceGroup{},
					NamespaceSelector:             labels.Everything(),
					FlavorFungibility:             defaultFlavorFungibility,
					Usage:                         FlavorResourceQuantities{},
					Status:                        active,
					Preemption:                    defaultPreemption,
					FairWeight:                    oneQuantity,
				},
				"c": {
					Name:                          "c",
					AllocatableResourceGeneration: 1,
					ResourceGroups:                []ResourceGroup{},
					NamespaceSelector:             labels.Nothing(),
					FlavorFungibility:             defaultFlavorFungibility,
					Usage:                         FlavorResourceQuantities{},
					Status:                        active,
					Preemption:                    defaultPreemption,
					FairWeight:                    oneQuantity,
				},
				"d": {
					Name:                          "d",
					AllocatableResourceGeneration: 1,
					ResourceGroups:                []ResourceGroup{},
					NamespaceSelector:             labels.Nothing(),
					FlavorFungibility:             defaultFlavorFungibility,
					Usage:                         FlavorResourceQuantities{},
					Status:                        active,
					Preemption:                    defaultPreemption,
					FairWeight:                    oneQuantity,
				},
				"e": {
					Name:                          "e",
					AllocatableResourceGeneration: 2,
					ResourceGroups: []ResourceGroup{{
						CoveredResources: sets.New(corev1.ResourceCPU),
						Flavors: []FlavorQuotas{{
							Name: "default",
							Resources: map[corev1.ResourceName]*ResourceQuota{
								corev1.ResourceCPU: {
									Nominal:        5_000,
									BorrowingLimit: ptr.To[int64](5_000),
									LendingLimit:   ptr.To[int64](4_000),
								},
							}},
						},
						LabelKeys: sets.New("cpuType", "region"),
					}},
					GuaranteedQuota: FlavorResourceQuantities{
						"default": {
							"cpu": 1_000,
						},
					},
					NamespaceSelector: labels.Nothing(),
					FlavorFungibility: defaultFlavorFungibility,
					Usage: FlavorResourceQuantities{
						"default": {corev1.ResourceCPU: 0},
					},
					AdmittedUsage: FlavorResourceQuantities{
						"default": {corev1.ResourceCPU: 0},
					},
					Lendable: map[corev1.ResourceName]int64{
						corev1.ResourceCPU: 4_000,
					},
					Status:     active,
					Preemption: defaultPreemption,
					FairWeight: oneQuantity,
				},
				"f": {
					Name:                          "f",
					AllocatableResourceGeneration: 1,
					ResourceGroups:                []ResourceGroup{},
					NamespaceSelector:             labels.Nothing(),
					Usage:                         FlavorResourceQuantities{},
					Status:                        active,
					Preemption:                    defaultPreemption,
					FlavorFungibility: kueue.FlavorFungibility{
						WhenCanBorrow:  kueue.TryNextFlavor,
						WhenCanPreempt: kueue.TryNextFlavor,
					},
					FairWeight: oneQuantity,
				},
			},
			wantCohorts: map[string]sets.Set[string]{
				"one": sets.New("b"),
				"two": sets.New("a", "c", "e", "f"),
			},
			enableLendingLimit: true,
		},
		{
			name: "delete",
			operation: func(cache *Cache) error {
				err := setup(cache)
				if err != nil {
					return err
				}
				clusterQueues := []kueue.ClusterQueue{
					{ObjectMeta: metav1.ObjectMeta{Name: "a"}},
					{ObjectMeta: metav1.ObjectMeta{Name: "d"}},
				}
				for _, c := range clusterQueues {
					cache.DeleteClusterQueue(&c)
				}
				return nil
			},
			wantClusterQueues: map[string]*ClusterQueue{
				"b": {
					Name:                          "b",
					AllocatableResourceGeneration: 1,
					ResourceGroups: []ResourceGroup{{
						CoveredResources: sets.New(corev1.ResourceCPU),
						Flavors: []FlavorQuotas{{
							Name: "default",
							Resources: map[corev1.ResourceName]*ResourceQuota{
								corev1.ResourceCPU: {Nominal: 15_000},
							},
						}},
						LabelKeys: sets.New("cpuType"),
					}},
					NamespaceSelector: labels.Nothing(),
					FlavorFungibility: defaultFlavorFungibility,
					Usage: FlavorResourceQuantities{
						"default": {corev1.ResourceCPU: 0},
					},
					AdmittedUsage: FlavorResourceQuantities{
						"default": {corev1.ResourceCPU: 0},
					},
					Lendable: map[corev1.ResourceName]int64{
						corev1.ResourceCPU: 15_000,
					},
					Status:     active,
					Preemption: defaultPreemption,
					FairWeight: oneQuantity,
				},
				"c": {
					Name:                          "c",
					AllocatableResourceGeneration: 1,
					ResourceGroups:                []ResourceGroup{},
					NamespaceSelector:             labels.Nothing(),
					FlavorFungibility:             defaultFlavorFungibility,
					Usage:                         FlavorResourceQuantities{},
					Status:                        active,
					Preemption:                    defaultPreemption,
					FairWeight:                    oneQuantity,
				},
				"e": {
					Name:                          "e",
					AllocatableResourceGeneration: 1,
					ResourceGroups: []ResourceGroup{{
						CoveredResources: sets.New(corev1.ResourceCPU),
						Flavors: []FlavorQuotas{
							{
								Name: "nonexistent-flavor",
								Resources: map[corev1.ResourceName]*ResourceQuota{
									corev1.ResourceCPU: {
										Nominal: 15_000,
									},
								},
							},
						},
					}},
					NamespaceSelector: labels.Nothing(),
					FlavorFungibility: defaultFlavorFungibility,
					Usage: FlavorResourceQuantities{
						"nonexistent-flavor": {corev1.ResourceCPU: 0},
					},
					AdmittedUsage: FlavorResourceQuantities{
						"nonexistent-flavor": {corev1.ResourceCPU: 0},
					},
					Lendable: map[corev1.ResourceName]int64{
						corev1.ResourceCPU: 15_000,
					},
					Status:     pending,
					Preemption: defaultPreemption,
					FairWeight: oneQuantity,
				},
				"f": {
					Name:                          "f",
					AllocatableResourceGeneration: 1,
					ResourceGroups:                []ResourceGroup{},
					NamespaceSelector:             labels.Nothing(),
					Usage:                         FlavorResourceQuantities{},
					Status:                        active,
					Preemption:                    defaultPreemption,
					FlavorFungibility: kueue.FlavorFungibility{
						WhenCanBorrow:  kueue.TryNextFlavor,
						WhenCanPreempt: kueue.TryNextFlavor,
					},
					FairWeight: oneQuantity,
				},
			},
			wantCohorts: map[string]sets.Set[string]{
				"one": sets.New("b"),
				"two": sets.New("c", "e", "f"),
			},
		},
		{
			name: "add resource flavors",
			operation: func(cache *Cache) error {
				err := setup(cache)
				if err != nil {
					return err
				}
				cache.AddOrUpdateResourceFlavor(&kueue.ResourceFlavor{
					ObjectMeta: metav1.ObjectMeta{Name: "nonexistent-flavor"},
				})
				return nil
			},
			wantClusterQueues: map[string]*ClusterQueue{
				"a": {
					Name:                          "a",
					AllocatableResourceGeneration: 1,
					ResourceGroups: []ResourceGroup{{
						CoveredResources: sets.New(corev1.ResourceCPU),
						Flavors: []FlavorQuotas{{
							Name: "default",
							Resources: map[corev1.ResourceName]*ResourceQuota{
								corev1.ResourceCPU: {
									Nominal:        10_000,
									BorrowingLimit: ptr.To[int64](10_000),
								},
							},
						}},
						LabelKeys: sets.New("cpuType"),
					}},
					NamespaceSelector: labels.Nothing(),
					FlavorFungibility: defaultFlavorFungibility,
					Usage: FlavorResourceQuantities{
						"default": {corev1.ResourceCPU: 0},
					},
					AdmittedUsage: FlavorResourceQuantities{
						"default": {corev1.ResourceCPU: 0},
					},
					Lendable: map[corev1.ResourceName]int64{
						corev1.ResourceCPU: 10_000,
					},
					Status:     active,
					Preemption: defaultPreemption,
					FairWeight: oneQuantity,
				},
				"b": {
					Name:                          "b",
					AllocatableResourceGeneration: 1,
					ResourceGroups: []ResourceGroup{{
						CoveredResources: sets.New(corev1.ResourceCPU),
						Flavors: []FlavorQuotas{{
							Name: "default",
							Resources: map[corev1.ResourceName]*ResourceQuota{
								corev1.ResourceCPU: {
									Nominal: 15_000,
								},
							},
						}},
						LabelKeys: sets.New("cpuType"),
					}},
					NamespaceSelector: labels.Nothing(),
					FlavorFungibility: defaultFlavorFungibility,
					Usage: FlavorResourceQuantities{
						"default": {corev1.ResourceCPU: 0},
					},
					AdmittedUsage: FlavorResourceQuantities{
						"default": {corev1.ResourceCPU: 0},
					},
					Lendable: map[corev1.ResourceName]int64{
						corev1.ResourceCPU: 15_000,
					},
					Status:     active,
					Preemption: defaultPreemption,
					FairWeight: oneQuantity,
				},
				"c": {
					Name:                          "c",
					AllocatableResourceGeneration: 1,
					ResourceGroups:                []ResourceGroup{},
					NamespaceSelector:             labels.Nothing(),
					FlavorFungibility:             defaultFlavorFungibility,
					Usage:                         FlavorResourceQuantities{},
					Status:                        active,
					Preemption:                    defaultPreemption,
					FairWeight:                    oneQuantity,
				},
				"d": {
					Name:                          "d",
					AllocatableResourceGeneration: 1,
					ResourceGroups:                []ResourceGroup{},
					NamespaceSelector:             labels.Nothing(),
					FlavorFungibility:             defaultFlavorFungibility,
					Usage:                         FlavorResourceQuantities{},
					Status:                        active,
					Preemption:                    defaultPreemption,
					FairWeight:                    oneQuantity,
				},
				"e": {
					Name:                          "e",
					AllocatableResourceGeneration: 1,
					ResourceGroups: []ResourceGroup{{
						CoveredResources: sets.New(corev1.ResourceCPU),
						Flavors: []FlavorQuotas{{
							Name: "nonexistent-flavor",
							Resources: map[corev1.ResourceName]*ResourceQuota{
								corev1.ResourceCPU: {
									Nominal: 15_000,
								},
							},
						}},
					}},
					NamespaceSelector: labels.Nothing(),
					FlavorFungibility: defaultFlavorFungibility,
					Usage: FlavorResourceQuantities{
						"nonexistent-flavor": {corev1.ResourceCPU: 0},
					},
					AdmittedUsage: FlavorResourceQuantities{
						"nonexistent-flavor": {corev1.ResourceCPU: 0},
					},
					Lendable: map[corev1.ResourceName]int64{
						corev1.ResourceCPU: 15_000,
					},
					Status:     active,
					Preemption: defaultPreemption,
					FairWeight: oneQuantity,
				},
				"f": {
					Name:                          "f",
					AllocatableResourceGeneration: 1,
					ResourceGroups:                []ResourceGroup{},
					NamespaceSelector:             labels.Nothing(),
					Usage:                         FlavorResourceQuantities{},
					Status:                        active,
					Preemption:                    defaultPreemption,
					FlavorFungibility: kueue.FlavorFungibility{
						WhenCanBorrow:  kueue.TryNextFlavor,
						WhenCanPreempt: kueue.TryNextFlavor,
					},
					FairWeight: oneQuantity,
				},
			},
			wantCohorts: map[string]sets.Set[string]{
				"one": sets.New("a", "b"),
				"two": sets.New("c", "e", "f"),
			},
		},
		{
			name: "Add ClusterQueue with multiple resource groups",
			operation: func(cache *Cache) error {
				err := cache.AddClusterQueue(context.Background(),
					utiltesting.MakeClusterQueue("foo").
						ResourceGroup(
							*utiltesting.MakeFlavorQuotas("foo").
								Resource("cpu").
								Resource("memory").
								Obj(),
							*utiltesting.MakeFlavorQuotas("bar").
								Resource("cpu").
								Resource("memory").
								Obj(),
						).
						ResourceGroup(
							*utiltesting.MakeFlavorQuotas("theta").Resource("example.com/gpu").Obj(),
							*utiltesting.MakeFlavorQuotas("gamma").Resource("example.com/gpu").Obj(),
						).
						Obj())
				if err != nil {
					return fmt.Errorf("Adding ClusterQueue: %w", err)
				}
				return nil
			},
			wantClusterQueues: map[string]*ClusterQueue{
				"foo": {
					Name:                          "foo",
					NamespaceSelector:             labels.Everything(),
					AllocatableResourceGeneration: 1,
					ResourceGroups: []ResourceGroup{
						{
							CoveredResources: sets.New[corev1.ResourceName]("cpu", "memory"),
							Flavors: []FlavorQuotas{
								{
									Name: "foo",
									Resources: map[corev1.ResourceName]*ResourceQuota{
										"cpu":    {},
										"memory": {},
									},
								},
								{
									Name: "bar",
									Resources: map[corev1.ResourceName]*ResourceQuota{
										"cpu":    {},
										"memory": {},
									},
								},
							},
						},
						{
							CoveredResources: sets.New[corev1.ResourceName]("example.com/gpu"),
							Flavors: []FlavorQuotas{
								{
									Name: "theta",
									Resources: map[corev1.ResourceName]*ResourceQuota{
										"example.com/gpu": {},
									},
								},
								{
									Name: "gamma",
									Resources: map[corev1.ResourceName]*ResourceQuota{
										"example.com/gpu": {},
									},
								},
							},
						},
					},
					FlavorFungibility: defaultFlavorFungibility,
					Usage: FlavorResourceQuantities{
						"foo": {
							"cpu":    0,
							"memory": 0,
						},
						"bar": {
							"cpu":    0,
							"memory": 0,
						},
						"theta": {
							"example.com/gpu": 0,
						},
						"gamma": {
							"example.com/gpu": 0,
						},
					},
					AdmittedUsage: FlavorResourceQuantities{
						"foo": {
							"cpu":    0,
							"memory": 0,
						},
						"bar": {
							"cpu":    0,
							"memory": 0,
						},
						"theta": {
							"example.com/gpu": 0,
						},
						"gamma": {
							"example.com/gpu": 0,
						},
					},
					Lendable: map[corev1.ResourceName]int64{
						corev1.ResourceCPU:    0,
						corev1.ResourceMemory: 0,
						"example.com/gpu":     0,
					},
					Status:     pending,
					Preemption: defaultPreemption,
					FairWeight: oneQuantity,
				},
			},
		},
		{
			name: "add cluster queue with missing check",
			operation: func(cache *Cache) error {
				err := cache.AddClusterQueue(context.Background(),
					utiltesting.MakeClusterQueue("foo").
						AdmissionChecks("check1", "check2").
						Obj())
				if err != nil {
					return fmt.Errorf("Adding ClusterQueue: %w", err)
				}
				return nil
			},
			wantClusterQueues: map[string]*ClusterQueue{
				"foo": {
					Name:                          "foo",
					NamespaceSelector:             labels.Everything(),
					Status:                        pending,
					Preemption:                    defaultPreemption,
					AllocatableResourceGeneration: 1,
					FlavorFungibility:             defaultFlavorFungibility,
					AdmissionChecks: map[string]sets.Set[kueue.ResourceFlavorReference]{
						"check1": sets.New[kueue.ResourceFlavorReference](),
						"check2": sets.New[kueue.ResourceFlavorReference](),
					},
					FairWeight: oneQuantity,
				},
			},
			wantCohorts: map[string]sets.Set[string]{},
		},
		{
			name: "add check after queue creation",
			operation: func(cache *Cache) error {
				err := cache.AddClusterQueue(context.Background(),
					utiltesting.MakeClusterQueue("foo").
						AdmissionChecks("check1", "check2").
						Obj())
				if err != nil {
					return fmt.Errorf("Adding ClusterQueue: %w", err)
				}

				cache.AddOrUpdateAdmissionCheck(utiltesting.MakeAdmissionCheck("check1").Active(metav1.ConditionTrue).Obj())
				cache.AddOrUpdateAdmissionCheck(utiltesting.MakeAdmissionCheck("check2").Active(metav1.ConditionTrue).Obj())
				return nil
			},
			wantClusterQueues: map[string]*ClusterQueue{
				"foo": {
					Name:                          "foo",
					NamespaceSelector:             labels.Everything(),
					Status:                        active,
					Preemption:                    defaultPreemption,
					AllocatableResourceGeneration: 1,
					FlavorFungibility:             defaultFlavorFungibility,
					AdmissionChecks: map[string]sets.Set[kueue.ResourceFlavorReference]{
						"check1": sets.New[kueue.ResourceFlavorReference](),
						"check2": sets.New[kueue.ResourceFlavorReference](),
					},
					FairWeight: oneQuantity,
				},
			},
			wantCohorts: map[string]sets.Set[string]{},
		},
		{
			name: "remove check after queue creation",
			operation: func(cache *Cache) error {
				cache.AddOrUpdateAdmissionCheck(utiltesting.MakeAdmissionCheck("check1").Active(metav1.ConditionTrue).Obj())
				cache.AddOrUpdateAdmissionCheck(utiltesting.MakeAdmissionCheck("check2").Active(metav1.ConditionTrue).Obj())
				err := cache.AddClusterQueue(context.Background(),
					utiltesting.MakeClusterQueue("foo").
						AdmissionChecks("check1", "check2").
						Obj())
				if err != nil {
					return fmt.Errorf("Adding ClusterQueue: %w", err)
				}

				cache.DeleteAdmissionCheck(utiltesting.MakeAdmissionCheck("check2").Obj())
				return nil
			},
			wantClusterQueues: map[string]*ClusterQueue{
				"foo": {
					Name:                          "foo",
					NamespaceSelector:             labels.Everything(),
					Status:                        pending,
					Preemption:                    defaultPreemption,
					AllocatableResourceGeneration: 1,
					FlavorFungibility:             defaultFlavorFungibility,
					AdmissionChecks: map[string]sets.Set[kueue.ResourceFlavorReference]{
						"check1": sets.New[kueue.ResourceFlavorReference](),
						"check2": sets.New[kueue.ResourceFlavorReference](),
					},
					FairWeight: oneQuantity,
				},
			},
			wantCohorts: map[string]sets.Set[string]{},
		},
		{
			name: "inactivate check after queue creation",
			operation: func(cache *Cache) error {
				cache.AddOrUpdateAdmissionCheck(utiltesting.MakeAdmissionCheck("check1").Active(metav1.ConditionTrue).Obj())
				cache.AddOrUpdateAdmissionCheck(utiltesting.MakeAdmissionCheck("check2").Active(metav1.ConditionTrue).Obj())
				err := cache.AddClusterQueue(context.Background(),
					utiltesting.MakeClusterQueue("foo").
						AdmissionChecks("check1", "check2").
						Obj())
				if err != nil {
					return fmt.Errorf("Adding ClusterQueue: %w", err)
				}

				cache.AddOrUpdateAdmissionCheck(utiltesting.MakeAdmissionCheck("check2").Active(metav1.ConditionFalse).Obj())
				return nil
			},
			wantClusterQueues: map[string]*ClusterQueue{
				"foo": {
					Name:                          "foo",
					NamespaceSelector:             labels.Everything(),
					Status:                        pending,
					Preemption:                    defaultPreemption,
					AllocatableResourceGeneration: 1,
					FlavorFungibility:             defaultFlavorFungibility,
					AdmissionChecks: map[string]sets.Set[kueue.ResourceFlavorReference]{
						"check1": sets.New[kueue.ResourceFlavorReference](),
						"check2": sets.New[kueue.ResourceFlavorReference](),
					},
					FairWeight: oneQuantity,
				},
			},
			wantCohorts: map[string]sets.Set[string]{},
		},
		{
			name: "add cluster queue after finished workloads",
			clientObjects: []client.Object{
				utiltesting.MakeLocalQueue("lq1", "ns").ClusterQueue("cq1").Obj(),
				utiltesting.MakeWorkload("pending", "ns").Obj(),
				utiltesting.MakeWorkload("reserving", "ns").ReserveQuota(
					utiltesting.MakeAdmission("cq1").Assignment(corev1.ResourceCPU, "f1", "1").Obj(),
				).Obj(),
				utiltesting.MakeWorkload("admitted", "ns").ReserveQuota(
					utiltesting.MakeAdmission("cq1").Assignment(corev1.ResourceCPU, "f1", "1").Obj(),
				).Admitted(true).Obj(),
				utiltesting.MakeWorkload("finished", "ns").ReserveQuota(
					utiltesting.MakeAdmission("cq1").Assignment(corev1.ResourceCPU, "f1", "1").Obj(),
				).Admitted(true).Finished().Obj(),
			},
			operation: func(cache *Cache) error {
				cache.AddOrUpdateResourceFlavor(utiltesting.MakeResourceFlavor("f1").Obj())
				err := cache.AddClusterQueue(context.Background(),
					utiltesting.MakeClusterQueue("cq1").
						ResourceGroup(kueue.FlavorQuotas{
							Name: "f1",
							Resources: []kueue.ResourceQuota{
								{
									Name:         corev1.ResourceCPU,
									NominalQuota: resource.MustParse("10"),
								},
							},
						}).
						Obj())
				if err != nil {
					return fmt.Errorf("Adding ClusterQueue: %w", err)
				}
				return nil
			},
			wantClusterQueues: map[string]*ClusterQueue{
				"cq1": {
					Name:                          "cq1",
					NamespaceSelector:             labels.Everything(),
					Status:                        active,
					Preemption:                    defaultPreemption,
					AllocatableResourceGeneration: 1,
					FlavorFungibility:             defaultFlavorFungibility,
					Usage:                         FlavorResourceQuantities{"f1": {corev1.ResourceCPU: 2000}},
					AdmittedUsage:                 FlavorResourceQuantities{"f1": {corev1.ResourceCPU: 1000}},
					Lendable: map[corev1.ResourceName]int64{
						corev1.ResourceCPU: 10_000,
					},
					FairWeight: oneQuantity,
					Workloads: map[string]*workload.Info{
						"ns/reserving": {
							ClusterQueue: "cq1",
							TotalRequests: []workload.PodSetResources{
								{
									Name:     "main",
									Requests: workload.Requests{corev1.ResourceCPU: 1000},
									Count:    1,
									Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
										corev1.ResourceCPU: "f1",
									},
								},
							},
						},
						"ns/admitted": {
							ClusterQueue: "cq1",
							TotalRequests: []workload.PodSetResources{
								{
									Name:     "main",
									Requests: workload.Requests{corev1.ResourceCPU: 1000},
									Count:    1,
									Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
										corev1.ResourceCPU: "f1",
									},
								},
							},
						},
					},
				},
			},
			wantCohorts: map[string]sets.Set[string]{},
		},
		{
			name:               "add CQ with multiple resource groups and flavors",
			enableLendingLimit: true,
			operation: func(cache *Cache) error {
				cq := utiltesting.MakeClusterQueue("foo").
					ResourceGroup(
						kueue.FlavorQuotas{
							Name: "on-demand",
							Resources: []kueue.ResourceQuota{
								{
									Name:         corev1.ResourceCPU,
									NominalQuota: resource.MustParse("10"),
									LendingLimit: ptr.To(resource.MustParse("8")),
								},
								{
									Name:         corev1.ResourceMemory,
									NominalQuota: resource.MustParse("10Gi"),
									LendingLimit: ptr.To(resource.MustParse("8Gi")),
								},
							},
						},
						kueue.FlavorQuotas{
							Name: "spot",
							Resources: []kueue.ResourceQuota{
								{
									Name:         corev1.ResourceCPU,
									NominalQuota: resource.MustParse("20"),
									LendingLimit: ptr.To(resource.MustParse("20")),
								},
								{
									Name:         corev1.ResourceMemory,
									NominalQuota: resource.MustParse("20Gi"),
									LendingLimit: ptr.To(resource.MustParse("20Gi")),
								},
							},
						},
					).
					ResourceGroup(
						kueue.FlavorQuotas{
							Name: "license",
							Resources: []kueue.ResourceQuota{
								{
									Name:         "license",
									NominalQuota: resource.MustParse("8"),
									LendingLimit: ptr.To(resource.MustParse("4")),
								},
							},
						},
					).
					Obj()
				return cache.AddClusterQueue(context.Background(), cq)
			},
			wantClusterQueues: map[string]*ClusterQueue{
				"foo": {
					Name:                          "foo",
					NamespaceSelector:             labels.Everything(),
					Status:                        pending,
					Preemption:                    defaultPreemption,
					AllocatableResourceGeneration: 1,
					FlavorFungibility:             defaultFlavorFungibility,
					FairWeight:                    oneQuantity,
					GuaranteedQuota: FlavorResourceQuantities{
						"on-demand": {
							corev1.ResourceCPU:    2_000,
							corev1.ResourceMemory: 2 * utiltesting.Gi,
						},
						"spot": {
							corev1.ResourceCPU:    0,
							corev1.ResourceMemory: 0,
						},
						"license": {
							"license": 4,
						},
					},
					Usage: FlavorResourceQuantities{
						"on-demand": {
							corev1.ResourceCPU:    0,
							corev1.ResourceMemory: 0,
						},
						"spot": {
							corev1.ResourceCPU:    0,
							corev1.ResourceMemory: 0,
						},
						"license": {
							"license": 0,
						},
					},
					AdmittedUsage: FlavorResourceQuantities{
						"on-demand": {
							corev1.ResourceCPU:    0,
							corev1.ResourceMemory: 0,
						},
						"spot": {
							corev1.ResourceCPU:    0,
							corev1.ResourceMemory: 0,
						},
						"license": {
							"license": 0,
						},
					},
					Lendable: map[corev1.ResourceName]int64{
						corev1.ResourceCPU:    28_000,
						corev1.ResourceMemory: 28 * utiltesting.Gi,
						"license":             4,
					},
				},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			defer features.SetFeatureGateDuringTest(t, features.LendingLimit, tc.enableLendingLimit)()
			cache := New(utiltesting.NewFakeClient(tc.clientObjects...))
			if err := tc.operation(cache); err != nil {
				t.Errorf("Unexpected error during test operation: %s", err)
			}
			if diff := cmp.Diff(tc.wantClusterQueues, cache.clusterQueues,
				cmpopts.IgnoreFields(ClusterQueue{}, "Cohort", "RGByResource", "ResourceGroups"),
				cmpopts.IgnoreFields(workload.Info{}, "Obj", "LastAssignment"),
				cmpopts.IgnoreUnexported(ClusterQueue{}),
				cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("Unexpected clusterQueues (-want,+got):\n%s", diff)
			}
			for _, cq := range cache.clusterQueues {
				for i := range cq.ResourceGroups {
					rg := &cq.ResourceGroups[i]
					for rName := range rg.CoveredResources {
						if cq.RGByResource[rName] != rg {
							t.Errorf("RGByResource[%s] does not point to its resource group", rName)
						}
					}
				}
			}

			gotCohorts := make(map[string]sets.Set[string], len(cache.cohorts))
			for name, cohort := range cache.cohorts {
				gotCohort := sets.New[string]()
				for cq := range cohort.Members {
					gotCohort.Insert(cq.Name)
				}
				gotCohorts[name] = gotCohort
			}
			if diff := cmp.Diff(tc.wantCohorts, gotCohorts, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("Unexpected cohorts (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestCacheWorkloadOperations(t *testing.T) {
	clusterQueues := []kueue.ClusterQueue{
		*utiltesting.MakeClusterQueue("one").
			ResourceGroup(
				*utiltesting.MakeFlavorQuotas("on-demand").Resource("cpu").Obj(),
				*utiltesting.MakeFlavorQuotas("spot").Resource("cpu").Obj(),
			).
			NamespaceSelector(nil).
			Obj(),
		*utiltesting.MakeClusterQueue("two").
			ResourceGroup(
				*utiltesting.MakeFlavorQuotas("on-demand").Resource("cpu").Obj(),
				*utiltesting.MakeFlavorQuotas("spot").Resource("cpu").Obj(),
			).
			NamespaceSelector(nil).
			Obj(),
	}
	podSets := []kueue.PodSet{
		*utiltesting.MakePodSet("driver", 1).
			Request(corev1.ResourceCPU, "10m").
			Request(corev1.ResourceMemory, "512Ki").
			Obj(),
		*utiltesting.MakePodSet("workers", 3).
			Request(corev1.ResourceCPU, "5m").
			Obj(),
	}
	podSetFlavors := []kueue.PodSetAssignment{
		{
			Name: "driver",
			Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
				corev1.ResourceCPU: "on-demand",
			},
			ResourceUsage: corev1.ResourceList{
				corev1.ResourceCPU: resource.MustParse("10m"),
			},
		},
		{
			Name: "workers",
			Flavors: map[corev1.ResourceName]kueue.ResourceFlavorReference{
				corev1.ResourceCPU: "spot",
			},
			ResourceUsage: corev1.ResourceList{
				corev1.ResourceCPU: resource.MustParse("15m"),
			},
		},
	}
	cl := utiltesting.NewFakeClient(
		utiltesting.MakeWorkload("a", "").PodSets(podSets...).ReserveQuota(&kueue.Admission{
			ClusterQueue:      "one",
			PodSetAssignments: podSetFlavors,
		}).Obj(),
		utiltesting.MakeWorkload("b", "").ReserveQuota(&kueue.Admission{
			ClusterQueue: "one",
		}).Obj(),
		utiltesting.MakeWorkload("c", "").PodSets(podSets...).ReserveQuota(&kueue.Admission{
			ClusterQueue: "two",
		}).Obj())

	type result struct {
		Workloads     sets.Set[string]
		UsedResources FlavorResourceQuantities
	}

	steps := []struct {
		name                 string
		operation            func(cache *Cache) error
		wantResults          map[string]result
		wantAssumedWorkloads map[string]string
		wantError            string
	}{
		{
			name: "add",
			operation: func(cache *Cache) error {
				workloads := []*kueue.Workload{
					utiltesting.MakeWorkload("a", "").PodSets(podSets...).ReserveQuota(&kueue.Admission{
						ClusterQueue:      "one",
						PodSetAssignments: podSetFlavors,
					}).Obj(),
					utiltesting.MakeWorkload("d", "").ReserveQuota(&kueue.Admission{
						ClusterQueue: "two",
					}).Obj(),
					utiltesting.MakeWorkload("pending", "").Obj(),
				}
				for i := range workloads {
					cache.AddOrUpdateWorkload(workloads[i])
				}
				return nil
			},
			wantResults: map[string]result{
				"one": {
					Workloads: sets.New("/a", "/b"),
					UsedResources: FlavorResourceQuantities{
						"on-demand": {corev1.ResourceCPU: 10},
						"spot":      {corev1.ResourceCPU: 15},
					},
				},
				"two": {
					Workloads: sets.New("/c", "/d"),
					UsedResources: FlavorResourceQuantities{
						"on-demand": {corev1.ResourceCPU: 0},
						"spot":      {corev1.ResourceCPU: 0},
					},
				},
			},
		},
		{
			name: "add error clusterQueue doesn't exist",
			operation: func(cache *Cache) error {
				w := utiltesting.MakeWorkload("d", "").ReserveQuota(&kueue.Admission{
					ClusterQueue: "three",
				}).Obj()
				if !cache.AddOrUpdateWorkload(w) {
					return errors.New("failed to add workload")
				}
				return nil
			},
			wantError: "failed to add workload",
			wantResults: map[string]result{
				"one": {
					Workloads: sets.New("/a", "/b"),
					UsedResources: FlavorResourceQuantities{
						"on-demand": {corev1.ResourceCPU: 10},
						"spot":      {corev1.ResourceCPU: 15},
					},
				},
				"two": {
					Workloads: sets.New("/c"),
					UsedResources: FlavorResourceQuantities{
						"on-demand": {corev1.ResourceCPU: 0},
						"spot":      {corev1.ResourceCPU: 0},
					},
				},
			},
		},
		{
			name: "add already exists",
			operation: func(cache *Cache) error {
				w := utiltesting.MakeWorkload("b", "").ReserveQuota(&kueue.Admission{
					ClusterQueue: "one",
				}).Obj()
				if !cache.AddOrUpdateWorkload(w) {
					return errors.New("failed to add workload")
				}
				return nil
			},
			wantResults: map[string]result{
				"one": {
					Workloads: sets.New("/a", "/b"),
					UsedResources: FlavorResourceQuantities{
						"on-demand": {corev1.ResourceCPU: 10},
						"spot":      {corev1.ResourceCPU: 15},
					},
				},
				"two": {
					Workloads: sets.New("/c"),
					UsedResources: FlavorResourceQuantities{
						"on-demand": {corev1.ResourceCPU: 0},
						"spot":      {corev1.ResourceCPU: 0},
					},
				},
			},
		},
		{
			name: "update cluster queue for a workload",
			operation: func(cache *Cache) error {
				old := utiltesting.MakeWorkload("a", "").ReserveQuota(&kueue.Admission{
					ClusterQueue: "one",
				}).Obj()
				latest := utiltesting.MakeWorkload("a", "").PodSets(podSets...).ReserveQuota(&kueue.Admission{
					ClusterQueue:      "two",
					PodSetAssignments: podSetFlavors,
				}).Obj()
				return cache.UpdateWorkload(old, latest)
			},
			wantResults: map[string]result{
				"one": {
					Workloads: sets.New("/b"),
					UsedResources: FlavorResourceQuantities{
						"on-demand": {corev1.ResourceCPU: 0},
						"spot":      {corev1.ResourceCPU: 0},
					},
				},
				"two": {
					Workloads: sets.New("/a", "/c"),
					UsedResources: FlavorResourceQuantities{
						"on-demand": {corev1.ResourceCPU: 10},
						"spot":      {corev1.ResourceCPU: 15},
					},
				},
			},
		},
		{
			name: "update error old clusterQueue doesn't exist",
			operation: func(cache *Cache) error {
				old := utiltesting.MakeWorkload("d", "").ReserveQuota(&kueue.Admission{
					ClusterQueue: "three",
				}).Obj()
				latest := utiltesting.MakeWorkload("d", "").ReserveQuota(&kueue.Admission{
					ClusterQueue: "one",
				}).Obj()
				return cache.UpdateWorkload(old, latest)
			},
			wantError: "old ClusterQueue doesn't exist",
			wantResults: map[string]result{
				"one": {
					Workloads: sets.New("/a", "/b"),
					UsedResources: FlavorResourceQuantities{
						"on-demand": {corev1.ResourceCPU: 10},
						"spot":      {corev1.ResourceCPU: 15},
					},
				},
				"two": {
					Workloads: sets.New("/c"),
					UsedResources: FlavorResourceQuantities{
						"on-demand": {corev1.ResourceCPU: 0},
						"spot":      {corev1.ResourceCPU: 0},
					},
				},
			},
		},
		{
			name: "update error new clusterQueue doesn't exist",
			operation: func(cache *Cache) error {
				old := utiltesting.MakeWorkload("d", "").ReserveQuota(&kueue.Admission{
					ClusterQueue: "one",
				}).Obj()
				latest := utiltesting.MakeWorkload("d", "").ReserveQuota(&kueue.Admission{
					ClusterQueue: "three",
				}).Obj()
				return cache.UpdateWorkload(old, latest)
			},
			wantError: "new ClusterQueue doesn't exist",
			wantResults: map[string]result{
				"one": {
					Workloads: sets.New("/a", "/b"),
					UsedResources: FlavorResourceQuantities{
						"on-demand": {corev1.ResourceCPU: 10},
						"spot":      {corev1.ResourceCPU: 15},
					},
				},
				"two": {
					Workloads: sets.New("/c"),
					UsedResources: FlavorResourceQuantities{
						"on-demand": {corev1.ResourceCPU: 0},
						"spot":      {corev1.ResourceCPU: 0},
					},
				},
			},
		},
		{
			name: "update workload which doesn't exist.",
			operation: func(cache *Cache) error {
				old := utiltesting.MakeWorkload("d", "").ReserveQuota(&kueue.Admission{
					ClusterQueue: "one",
				}).Obj()
				latest := utiltesting.MakeWorkload("d", "").ReserveQuota(&kueue.Admission{
					ClusterQueue: "two",
				}).Obj()
				return cache.UpdateWorkload(old, latest)
			},
			wantResults: map[string]result{
				"one": {
					Workloads: sets.New("/a", "/b"),
					UsedResources: FlavorResourceQuantities{
						"on-demand": {corev1.ResourceCPU: 10},
						"spot":      {corev1.ResourceCPU: 15},
					},
				},
				"two": {
					Workloads: sets.New("/c", "/d"),
					UsedResources: FlavorResourceQuantities{
						"on-demand": {corev1.ResourceCPU: 0},
						"spot":      {corev1.ResourceCPU: 0},
					},
				},
			},
		},
		{
			name: "delete",
			operation: func(cache *Cache) error {
				w := utiltesting.MakeWorkload("a", "").ReserveQuota(&kueue.Admission{
					ClusterQueue: "one",
				}).Obj()
				return cache.DeleteWorkload(w)
			},
			wantResults: map[string]result{
				"one": {
					Workloads: sets.New("/b"),
					UsedResources: FlavorResourceQuantities{
						"on-demand": {corev1.ResourceCPU: 0},
						"spot":      {corev1.ResourceCPU: 0},
					},
				},
				"two": {
					Workloads: sets.New("/c"),
					UsedResources: FlavorResourceQuantities{
						"on-demand": {corev1.ResourceCPU: 0},
						"spot":      {corev1.ResourceCPU: 0},
					},
				},
			},
		},
		{
			name: "delete workload with cancelled admission",
			operation: func(cache *Cache) error {
				w := utiltesting.MakeWorkload("a", "").Obj()
				return cache.DeleteWorkload(w)
			},
			wantResults: map[string]result{
				"one": {
					Workloads: sets.New("/b"),
					UsedResources: FlavorResourceQuantities{
						"on-demand": {corev1.ResourceCPU: 0},
						"spot":      {corev1.ResourceCPU: 0},
					},
				},
				"two": {
					Workloads: sets.New("/c"),
					UsedResources: FlavorResourceQuantities{
						"on-demand": {corev1.ResourceCPU: 0},
						"spot":      {corev1.ResourceCPU: 0},
					},
				},
			},
		},
		{
			name: "attempt deleting non-existing workload with cancelled admission",
			operation: func(cache *Cache) error {
				w := utiltesting.MakeWorkload("d", "").Obj()
				return cache.DeleteWorkload(w)
			},
			wantError: "cluster queue not found",
			wantResults: map[string]result{
				"one": {
					Workloads: sets.New("/a", "/b"),
					UsedResources: FlavorResourceQuantities{
						"on-demand": {corev1.ResourceCPU: 10},
						"spot":      {corev1.ResourceCPU: 15},
					},
				},
				"two": {
					Workloads: sets.New("/c"),
					UsedResources: FlavorResourceQuantities{
						"on-demand": {corev1.ResourceCPU: 0},
						"spot":      {corev1.ResourceCPU: 0},
					},
				},
			},
		},
		{
			name: "delete error clusterQueue doesn't exist",
			operation: func(cache *Cache) error {
				w := utiltesting.MakeWorkload("a", "").ReserveQuota(&kueue.Admission{
					ClusterQueue: "three",
				}).Obj()
				return cache.DeleteWorkload(w)
			},
			wantError: "cluster queue not found",
			wantResults: map[string]result{
				"one": {
					Workloads: sets.New("/a", "/b"),
					UsedResources: FlavorResourceQuantities{
						"on-demand": {corev1.ResourceCPU: 10},
						"spot":      {corev1.ResourceCPU: 15},
					},
				},
				"two": {
					Workloads: sets.New("/c"),
					UsedResources: FlavorResourceQuantities{
						"on-demand": {corev1.ResourceCPU: 0},
						"spot":      {corev1.ResourceCPU: 0},
					},
				},
			},
		},
		{
			name: "delete workload which doesn't exist",
			operation: func(cache *Cache) error {
				w := utiltesting.MakeWorkload("d", "").ReserveQuota(&kueue.Admission{
					ClusterQueue: "one",
				}).Obj()
				return cache.DeleteWorkload(w)
			},
			wantResults: map[string]result{
				"one": {
					Workloads: sets.New("/a", "/b"),
					UsedResources: FlavorResourceQuantities{
						"on-demand": {corev1.ResourceCPU: 10},
						"spot":      {corev1.ResourceCPU: 15},
					},
				},
				"two": {
					Workloads: sets.New("/c"),
					UsedResources: FlavorResourceQuantities{
						"on-demand": {corev1.ResourceCPU: 0},
						"spot":      {corev1.ResourceCPU: 0},
					},
				},
			},
		},
		{
			name: "assume",
			operation: func(cache *Cache) error {
				workloads := []*kueue.Workload{
					utiltesting.MakeWorkload("d", "").PodSets(podSets...).ReserveQuota(&kueue.Admission{
						ClusterQueue:      "one",
						PodSetAssignments: podSetFlavors,
					}).Obj(),
					utiltesting.MakeWorkload("e", "").PodSets(podSets...).ReserveQuota(&kueue.Admission{
						ClusterQueue:      "two",
						PodSetAssignments: podSetFlavors,
					}).Obj(),
				}
				for i := range workloads {
					if err := cache.AssumeWorkload(workloads[i]); err != nil {
						return err
					}
				}
				return nil
			},
			wantResults: map[string]result{
				"one": {
					Workloads: sets.New("/a", "/b", "/d"),
					UsedResources: FlavorResourceQuantities{
						"on-demand": {corev1.ResourceCPU: 20},
						"spot":      {corev1.ResourceCPU: 30},
					},
				},
				"two": {
					Workloads: sets.New("/c", "/e"),
					UsedResources: FlavorResourceQuantities{
						"on-demand": {corev1.ResourceCPU: 10},
						"spot":      {corev1.ResourceCPU: 15},
					},
				},
			},
			wantAssumedWorkloads: map[string]string{
				"/d": "one",
				"/e": "two",
			},
		},
		{
			name: "assume error clusterQueue doesn't exist",
			operation: func(cache *Cache) error {
				w := utiltesting.MakeWorkload("d", "").PodSets(podSets...).ReserveQuota(&kueue.Admission{
					ClusterQueue: "three",
				}).Obj()
				if err := cache.AssumeWorkload(w); err != nil {
					return err
				}
				return nil
			},
			wantError: "cluster queue not found",
			wantResults: map[string]result{
				"one": {
					Workloads: sets.New("/a", "/b"),
					UsedResources: FlavorResourceQuantities{
						"on-demand": {corev1.ResourceCPU: 10},
						"spot":      {corev1.ResourceCPU: 15},
					},
				},
				"two": {
					Workloads: sets.New("/c"),
					UsedResources: FlavorResourceQuantities{
						"on-demand": {corev1.ResourceCPU: 0},
						"spot":      {corev1.ResourceCPU: 0},
					},
				},
			},
			wantAssumedWorkloads: map[string]string{},
		},
		{
			name: "forget",
			operation: func(cache *Cache) error {
				workloads := []*kueue.Workload{
					utiltesting.MakeWorkload("d", "").PodSets(podSets...).ReserveQuota(&kueue.Admission{
						ClusterQueue:      "one",
						PodSetAssignments: podSetFlavors,
					}).Obj(),
					utiltesting.MakeWorkload("e", "").PodSets(podSets...).ReserveQuota(&kueue.Admission{
						ClusterQueue:      "two",
						PodSetAssignments: podSetFlavors,
					}).Obj(),
				}
				for i := range workloads {
					if err := cache.AssumeWorkload(workloads[i]); err != nil {
						return err
					}
				}

				w := workloads[0]
				return cache.ForgetWorkload(w)
			},
			wantResults: map[string]result{
				"one": {
					Workloads: sets.New("/a", "/b"),
					UsedResources: FlavorResourceQuantities{
						"on-demand": {corev1.ResourceCPU: 10},
						"spot":      {corev1.ResourceCPU: 15},
					},
				},
				"two": {
					Workloads: sets.New("/c", "/e"),
					UsedResources: FlavorResourceQuantities{
						"on-demand": {corev1.ResourceCPU: 10},
						"spot":      {corev1.ResourceCPU: 15},
					},
				},
			},
			wantAssumedWorkloads: map[string]string{
				"/e": "two",
			},
		},
		{
			name: "forget error workload is not assumed",
			operation: func(cache *Cache) error {
				w := utiltesting.MakeWorkload("b", "").ReserveQuota(&kueue.Admission{
					ClusterQueue: "one",
				}).Obj()
				if err := cache.ForgetWorkload(w); err != nil {
					return err
				}
				return nil
			},
			wantError: "the workload is not assumed",
			wantResults: map[string]result{
				"one": {
					Workloads: sets.New("/a", "/b"),
					UsedResources: FlavorResourceQuantities{
						"on-demand": {corev1.ResourceCPU: 10},
						"spot":      {corev1.ResourceCPU: 15},
					},
				},
				"two": {
					Workloads: sets.New("/c"),
					UsedResources: FlavorResourceQuantities{
						"on-demand": {corev1.ResourceCPU: 0},
						"spot":      {corev1.ResourceCPU: 0},
					},
				},
			},
		},
		{
			name: "add assumed workload",
			operation: func(cache *Cache) error {
				workloads := []*kueue.Workload{
					utiltesting.MakeWorkload("d", "").PodSets(podSets...).ReserveQuota(&kueue.Admission{
						ClusterQueue:      "one",
						PodSetAssignments: podSetFlavors,
					}).Obj(),
					utiltesting.MakeWorkload("e", "").PodSets(podSets...).ReserveQuota(&kueue.Admission{
						ClusterQueue:      "two",
						PodSetAssignments: podSetFlavors,
					}).Obj(),
				}
				for i := range workloads {
					if err := cache.AssumeWorkload(workloads[i]); err != nil {
						return err
					}
				}

				w := workloads[0]
				if !cache.AddOrUpdateWorkload(w) {
					return errors.New("failed to add workload")
				}
				return nil
			},
			wantResults: map[string]result{
				"one": {
					Workloads: sets.New("/a", "/b", "/d"),
					UsedResources: FlavorResourceQuantities{
						"on-demand": {corev1.ResourceCPU: 20},
						"spot":      {corev1.ResourceCPU: 30},
					},
				},
				"two": {
					Workloads: sets.New("/c", "/e"),
					UsedResources: FlavorResourceQuantities{
						"on-demand": {corev1.ResourceCPU: 10},
						"spot":      {corev1.ResourceCPU: 15},
					},
				},
			},
			wantAssumedWorkloads: map[string]string{
				"/e": "two",
			},
		},
	}
	for _, step := range steps {
		t.Run(step.name, func(t *testing.T) {
			cache := New(cl)

			for _, c := range clusterQueues {
				if err := cache.AddClusterQueue(context.Background(), &c); err != nil {
					t.Fatalf("Failed adding clusterQueue: %v", err)
				}
			}

			gotError := step.operation(cache)
			if diff := cmp.Diff(step.wantError, messageOrEmpty(gotError)); diff != "" {
				t.Errorf("Unexpected error (-want,+got):\n%s", diff)
			}
			gotResult := make(map[string]result)
			for name, cq := range cache.clusterQueues {
				gotResult[name] = result{
					Workloads:     sets.KeySet(cq.Workloads),
					UsedResources: cq.Usage,
				}
			}
			if diff := cmp.Diff(step.wantResults, gotResult); diff != "" {
				t.Errorf("Unexpected clusterQueues (-want,+got):\n%s", diff)
			}
			if step.wantAssumedWorkloads == nil {
				step.wantAssumedWorkloads = map[string]string{}
			}
			if diff := cmp.Diff(step.wantAssumedWorkloads, cache.assumedWorkloads); diff != "" {
				t.Errorf("Unexpected assumed workloads (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestClusterQueueUsage(t *testing.T) {
	cq := utiltesting.MakeClusterQueue("foo").
		ResourceGroup(
			*utiltesting.MakeFlavorQuotas("default").
				Resource(corev1.ResourceCPU, "10", "10").
				Obj(),
		).
		ResourceGroup(
			*utiltesting.MakeFlavorQuotas("model_a").
				Resource("example.com/gpu", "5", "5").
				Obj(),
			*utiltesting.MakeFlavorQuotas("model_b").
				Resource("example.com/gpu", "5").
				Obj(),
		).
		ResourceGroup(
			*utiltesting.MakeFlavorQuotas("interconnect_a").
				Resource("example.com/vf-0", "5", "5").
				Resource("example.com/vf-1", "5", "5").
				Resource("example.com/vf-2", "5", "5").
				Obj(),
		).
		Cohort("one").Obj()
	cqWithOutCohort := cq.DeepCopy()
	cqWithOutCohort.Spec.Cohort = ""
	workloads := []kueue.Workload{
		*utiltesting.MakeWorkload("one", "").
			Request(corev1.ResourceCPU, "8").
			Request("example.com/gpu", "5").
			ReserveQuota(utiltesting.MakeAdmission("foo").Assignment(corev1.ResourceCPU, "default", "8000m").Assignment("example.com/gpu", "model_a", "5").Obj()).
			Condition(metav1.Condition{Type: kueue.WorkloadAdmitted, Status: metav1.ConditionTrue}).
			Obj(),
		*utiltesting.MakeWorkload("two", "").
			Request(corev1.ResourceCPU, "5").
			Request("example.com/gpu", "6").
			ReserveQuota(utiltesting.MakeAdmission("foo").Assignment(corev1.ResourceCPU, "default", "5000m").Assignment("example.com/gpu", "model_b", "6").Obj()).
			Obj(),
	}
	cases := map[string]struct {
		clusterQueue           *kueue.ClusterQueue
		workloads              []kueue.Workload
		wantReservedResources  []kueue.FlavorUsage
		wantReservingWorkloads int
		wantUsedResources      []kueue.FlavorUsage
		wantAdmittedWorkloads  int
	}{
		"clusterQueue without cohort; single, admitted no borrowing": {
			clusterQueue: cqWithOutCohort,
			workloads:    workloads[:1],
			wantReservedResources: []kueue.FlavorUsage{
				{
					Name: "default",
					Resources: []kueue.ResourceUsage{{
						Name:  corev1.ResourceCPU,
						Total: resource.MustParse("8"),
					}},
				},
				{
					Name: "model_a",
					Resources: []kueue.ResourceUsage{{
						Name:  "example.com/gpu",
						Total: resource.MustParse("5"),
					}},
				},
				{
					Name: "model_b",
					Resources: []kueue.ResourceUsage{{
						Name: "example.com/gpu",
					}},
				},
				{
					Name: "interconnect_a",
					Resources: []kueue.ResourceUsage{
						{Name: "example.com/vf-0"},
						{Name: "example.com/vf-1"},
						{Name: "example.com/vf-2"},
					},
				},
			},
			wantReservingWorkloads: 1,
			wantUsedResources: []kueue.FlavorUsage{
				{
					Name: "default",
					Resources: []kueue.ResourceUsage{{
						Name:  corev1.ResourceCPU,
						Total: resource.MustParse("8"),
					}},
				},
				{
					Name: "model_a",
					Resources: []kueue.ResourceUsage{{
						Name:  "example.com/gpu",
						Total: resource.MustParse("5"),
					}},
				},
				{
					Name: "model_b",
					Resources: []kueue.ResourceUsage{{
						Name: "example.com/gpu",
					}},
				},
				{
					Name: "interconnect_a",
					Resources: []kueue.ResourceUsage{
						{Name: "example.com/vf-0"},
						{Name: "example.com/vf-1"},
						{Name: "example.com/vf-2"},
					},
				},
			},
			wantAdmittedWorkloads: 1,
		},
		"clusterQueue with cohort; multiple borrowing": {
			clusterQueue: cq,
			workloads:    workloads,
			wantReservedResources: []kueue.FlavorUsage{
				{
					Name: "default",
					Resources: []kueue.ResourceUsage{{
						Name:     corev1.ResourceCPU,
						Total:    resource.MustParse("13"),
						Borrowed: resource.MustParse("3"),
					}},
				},
				{
					Name: "model_a",
					Resources: []kueue.ResourceUsage{{
						Name:  "example.com/gpu",
						Total: resource.MustParse("5"),
					}},
				},
				{
					Name: "model_b",
					Resources: []kueue.ResourceUsage{{
						Name:     "example.com/gpu",
						Total:    resource.MustParse("6"),
						Borrowed: resource.MustParse("1"),
					}},
				},
				{
					Name: "interconnect_a",
					Resources: []kueue.ResourceUsage{
						{Name: "example.com/vf-0"},
						{Name: "example.com/vf-1"},
						{Name: "example.com/vf-2"},
					},
				},
			},
			wantReservingWorkloads: 2,
			wantUsedResources: []kueue.FlavorUsage{
				{
					Name: "default",
					Resources: []kueue.ResourceUsage{{
						Name:  corev1.ResourceCPU,
						Total: resource.MustParse("8"),
					}},
				},
				{
					Name: "model_a",
					Resources: []kueue.ResourceUsage{{
						Name:  "example.com/gpu",
						Total: resource.MustParse("5"),
					}},
				},
				{
					Name: "model_b",
					Resources: []kueue.ResourceUsage{{
						Name: "example.com/gpu",
					}},
				},
				{
					Name: "interconnect_a",
					Resources: []kueue.ResourceUsage{
						{Name: "example.com/vf-0"},
						{Name: "example.com/vf-1"},
						{Name: "example.com/vf-2"},
					},
				},
			},
			wantAdmittedWorkloads: 1,
		},
		"clusterQueue without cohort; multiple borrowing": {
			clusterQueue: cqWithOutCohort,
			workloads:    workloads,
			wantReservedResources: []kueue.FlavorUsage{
				{
					Name: "default",
					Resources: []kueue.ResourceUsage{{
						Name:     corev1.ResourceCPU,
						Total:    resource.MustParse("13"),
						Borrowed: resource.MustParse("0"),
					}},
				},
				{
					Name: "model_a",
					Resources: []kueue.ResourceUsage{{
						Name:  "example.com/gpu",
						Total: resource.MustParse("5"),
					}},
				},
				{
					Name: "model_b",
					Resources: []kueue.ResourceUsage{{
						Name:     "example.com/gpu",
						Total:    resource.MustParse("6"),
						Borrowed: resource.MustParse("0"),
					}},
				},
				{
					Name: "interconnect_a",
					Resources: []kueue.ResourceUsage{
						{Name: "example.com/vf-0"},
						{Name: "example.com/vf-1"},
						{Name: "example.com/vf-2"},
					},
				},
			},
			wantReservingWorkloads: 2,
			wantUsedResources: []kueue.FlavorUsage{
				{
					Name: "default",
					Resources: []kueue.ResourceUsage{{
						Name:  corev1.ResourceCPU,
						Total: resource.MustParse("8"),
					}},
				},
				{
					Name: "model_a",
					Resources: []kueue.ResourceUsage{{
						Name:  "example.com/gpu",
						Total: resource.MustParse("5"),
					}},
				},
				{
					Name: "model_b",
					Resources: []kueue.ResourceUsage{{
						Name: "example.com/gpu",
					}},
				},
				{
					Name: "interconnect_a",
					Resources: []kueue.ResourceUsage{
						{Name: "example.com/vf-0"},
						{Name: "example.com/vf-1"},
						{Name: "example.com/vf-2"},
					},
				},
			},
			wantAdmittedWorkloads: 1,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			cache := New(utiltesting.NewFakeClient())
			ctx := context.Background()
			err := cache.AddClusterQueue(ctx, tc.clusterQueue)
			if err != nil {
				t.Fatalf("Adding ClusterQueue: %v", err)
			}
			for i := range tc.workloads {
				w := &tc.workloads[i]
				if added := cache.AddOrUpdateWorkload(w); !added {
					t.Fatalf("Workload %s was not added", workload.Key(w))
				}
			}
			stats, err := cache.Usage(tc.clusterQueue)
			if err != nil {
				t.Fatalf("Couldn't get usage: %v", err)
			}

			if diff := cmp.Diff(tc.wantReservedResources, stats.ReservedResources); diff != "" {
				t.Errorf("Unexpected used reserved resources (-want,+got):\n%s", diff)
			}
			if stats.ReservingWorkloads != tc.wantReservingWorkloads {
				t.Errorf("Got %d reserving workloads, want %d", stats.ReservingWorkloads, tc.wantReservingWorkloads)
			}

			if diff := cmp.Diff(tc.wantUsedResources, stats.AdmittedResources); diff != "" {
				t.Errorf("Unexpected used resources (-want,+got):\n%s", diff)
			}
			if stats.AdmittedWorkloads != tc.wantAdmittedWorkloads {
				t.Errorf("Got %d admitted workloads, want %d", stats.AdmittedWorkloads, tc.wantAdmittedWorkloads)
			}
		})
	}
}

func TestLocalQueueUsage(t *testing.T) {
	cq := *utiltesting.MakeClusterQueue("foo").
		ResourceGroup(
			*utiltesting.MakeFlavorQuotas("default").
				Resource(corev1.ResourceCPU, "10", "10").Obj(),
		).
		ResourceGroup(
			*utiltesting.MakeFlavorQuotas("model-a").
				Resource("example.com/gpu", "5").Obj(),
			*utiltesting.MakeFlavorQuotas("model-b").
				Resource("example.com/gpu", "5").Obj(),
		).
		ResourceGroup(
			*utiltesting.MakeFlavorQuotas("interconnect-a").
				Resource("example.com/vf-0", "5", "5").
				Resource("example.com/vf-1", "5", "5").
				Resource("example.com/vf-2", "5", "5").
				Obj(),
		).
		Obj()
	localQueue := *utiltesting.MakeLocalQueue("test", "ns1").
		ClusterQueue("foo").Obj()
	cases := map[string]struct {
		cq             *kueue.ClusterQueue
		wls            []kueue.Workload
		wantUsage      []kueue.LocalQueueFlavorUsage
		inAdmissibleWl sets.Set[string]
	}{
		"clusterQueue is missing": {
			wls: []kueue.Workload{
				*utiltesting.MakeWorkload("one", "ns1").
					Queue("test").
					Request(corev1.ResourceCPU, "5").Obj(),
			},
			inAdmissibleWl: sets.New("one"),
		},
		"workloads is nothing": {
			cq: &cq,
			wantUsage: []kueue.LocalQueueFlavorUsage{
				{
					Name: "default",
					Resources: []kueue.LocalQueueResourceUsage{
						{
							Name:  corev1.ResourceCPU,
							Total: resource.MustParse("0"),
						},
					},
				},
				{
					Name: "model-a",
					Resources: []kueue.LocalQueueResourceUsage{
						{
							Name:  "example.com/gpu",
							Total: resource.MustParse("0"),
						},
					},
				},
				{
					Name: "model-b",
					Resources: []kueue.LocalQueueResourceUsage{
						{
							Name:  "example.com/gpu",
							Total: resource.MustParse("0"),
						},
					},
				},
				{
					Name: "interconnect-a",
					Resources: []kueue.LocalQueueResourceUsage{
						{Name: "example.com/vf-0"},
						{Name: "example.com/vf-1"},
						{Name: "example.com/vf-2"},
					},
				},
			},
		},
		"all workloads are admitted": {
			cq: &cq,
			wls: []kueue.Workload{
				*utiltesting.MakeWorkload("one", "ns1").
					Queue("test").
					Request(corev1.ResourceCPU, "5").
					Request("example.com/gpu", "5").
					ReserveQuota(
						utiltesting.MakeAdmission("foo").
							Assignment(corev1.ResourceCPU, "default", "5000m").
							Assignment("example.com/gpu", "model-a", "5").Obj(),
					).
					Obj(),
				*utiltesting.MakeWorkload("two", "ns1").
					Queue("test").
					Request(corev1.ResourceCPU, "3").
					Request("example.com/gpu", "3").
					ReserveQuota(
						utiltesting.MakeAdmission("foo").
							Assignment(corev1.ResourceCPU, "default", "3000m").
							Assignment("example.com/gpu", "model-b", "3").Obj(),
					).
					Obj(),
			},
			wantUsage: []kueue.LocalQueueFlavorUsage{
				{
					Name: "default",
					Resources: []kueue.LocalQueueResourceUsage{
						{
							Name:  corev1.ResourceCPU,
							Total: resource.MustParse("8"),
						},
					},
				},
				{
					Name: "model-a",
					Resources: []kueue.LocalQueueResourceUsage{
						{
							Name:  "example.com/gpu",
							Total: resource.MustParse("5"),
						},
					},
				},
				{
					Name: "model-b",
					Resources: []kueue.LocalQueueResourceUsage{
						{
							Name:  "example.com/gpu",
							Total: resource.MustParse("3"),
						},
					},
				},
				{
					Name: "interconnect-a",
					Resources: []kueue.LocalQueueResourceUsage{
						{Name: "example.com/vf-0"},
						{Name: "example.com/vf-1"},
						{Name: "example.com/vf-2"},
					},
				},
			},
		},
		"some workloads are inadmissible": {
			cq: &cq,
			wls: []kueue.Workload{
				*utiltesting.MakeWorkload("one", "ns1").
					Queue("test").
					Request(corev1.ResourceCPU, "5").
					Request("example.com/gpu", "5").
					ReserveQuota(
						utiltesting.MakeAdmission("foo").
							Assignment(corev1.ResourceCPU, "default", "5000m").
							Assignment("example.com/gpu", "model-a", "5").Obj(),
					).Obj(),
				*utiltesting.MakeWorkload("two", "ns1").
					Queue("test").
					Request(corev1.ResourceCPU, "100000").
					Request("example.com/gpu", "3").Obj(),
			},
			inAdmissibleWl: sets.New("two"),
			wantUsage: []kueue.LocalQueueFlavorUsage{
				{
					Name: "default",
					Resources: []kueue.LocalQueueResourceUsage{
						{
							Name:  corev1.ResourceCPU,
							Total: resource.MustParse("5"),
						},
					},
				},
				{
					Name: "model-a",
					Resources: []kueue.LocalQueueResourceUsage{
						{
							Name:  "example.com/gpu",
							Total: resource.MustParse("5"),
						},
					},
				},
				{
					Name: "model-b",
					Resources: []kueue.LocalQueueResourceUsage{
						{
							Name:  "example.com/gpu",
							Total: resource.MustParse("0"),
						},
					},
				},
				{
					Name: "interconnect-a",
					Resources: []kueue.LocalQueueResourceUsage{
						{Name: "example.com/vf-0"},
						{Name: "example.com/vf-1"},
						{Name: "example.com/vf-2"},
					},
				},
			},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			cache := New(utiltesting.NewFakeClient())
			ctx := context.Background()
			if tc.cq != nil {
				if err := cache.AddClusterQueue(ctx, tc.cq); err != nil {
					t.Fatalf("Adding ClusterQueue: %v", err)
				}
			}
			if err := cache.AddLocalQueue(&localQueue); err != nil {
				t.Fatalf("Adding LocalQueue: %v", err)
			}
			for _, w := range tc.wls {
				if added := cache.AddOrUpdateWorkload(&w); !added && !tc.inAdmissibleWl.Has(w.Name) {
					t.Fatalf("Workload %s was not added", workload.Key(&w))
				}
			}
			gotUsage, err := cache.LocalQueueUsage(&localQueue)
			if err != nil {
				t.Fatalf("Couldn't get usage for the queue: %v", err)
			}
			if diff := cmp.Diff(tc.wantUsage, gotUsage.ReservedResources); diff != "" {
				t.Errorf("Unexpected used resources for the queue (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestCacheQueueOperations(t *testing.T) {
	cqs := []*kueue.ClusterQueue{
		utiltesting.MakeClusterQueue("foo").
			ResourceGroup(
				*utiltesting.MakeFlavorQuotas("spot").
					Resource("cpu", "10", "10").
					Resource("memory", "64Gi", "64Gi").Obj(),
			).ResourceGroup(
			*utiltesting.MakeFlavorQuotas("model-a").
				Resource("example.com/gpu", "10", "10").Obj(),
		).Obj(),
		utiltesting.MakeClusterQueue("bar").
			ResourceGroup(
				*utiltesting.MakeFlavorQuotas("ondemand").
					Resource("cpu", "5", "5").
					Resource("memory", "32Gi", "32Gi").Obj(),
			).ResourceGroup(
			*utiltesting.MakeFlavorQuotas("model-b").
				Resource("example.com/gpu", "5", "5").Obj(),
		).Obj(),
	}
	queues := []*kueue.LocalQueue{
		utiltesting.MakeLocalQueue("alpha", "ns1").ClusterQueue("foo").Obj(),
		utiltesting.MakeLocalQueue("beta", "ns2").ClusterQueue("foo").Obj(),
		utiltesting.MakeLocalQueue("gamma", "ns1").ClusterQueue("bar").Obj(),
	}
	workloads := []*kueue.Workload{
		utiltesting.MakeWorkload("job1", "ns1").
			Queue("alpha").
			Request("cpu", "2").
			Request("memory", "8Gi").
			ReserveQuota(
				utiltesting.MakeAdmission("foo").
					Assignment("cpu", "spot", "2").
					Assignment("memory", "spot", "8Gi").Obj(),
			).
			Condition(metav1.Condition{Type: kueue.WorkloadAdmitted, Status: metav1.ConditionTrue}).
			Obj(),
		utiltesting.MakeWorkload("job2", "ns2").
			Queue("beta").
			Request("example.com/gpu", "2").
			ReserveQuota(
				utiltesting.MakeAdmission("foo").
					Assignment("example.com/gpu", "model-a", "2").Obj(),
			).
			Condition(metav1.Condition{Type: kueue.WorkloadAdmitted, Status: metav1.ConditionTrue}).
			Obj(),
		utiltesting.MakeWorkload("job3", "ns1").
			Queue("gamma").
			Request("cpu", "5").
			Request("memory", "16Gi").
			ReserveQuota(
				utiltesting.MakeAdmission("bar").
					Assignment("cpu", "ondemand", "5").
					Assignment("memory", "ondemand", "16Gi").Obj(),
			).Obj(),
		utiltesting.MakeWorkload("job4", "ns2").
			Queue("beta").
			Request("example.com/gpu", "5").
			ReserveQuota(
				utiltesting.MakeAdmission("foo").
					Assignment("example.com/gpu", "model-a", "5").Obj(),
			).Obj(),
	}
	insertAllClusterQueues := func(ctx context.Context, cl client.Client, cache *Cache) error {
		for _, cq := range cqs {
			cq := cq.DeepCopy()
			if err := cl.Create(ctx, cq); err != nil {
				return err
			}
			if err := cache.AddClusterQueue(ctx, cq); err != nil {
				return err
			}
		}
		return nil
	}
	insertAllQueues := func(ctx context.Context, cl client.Client, cache *Cache) error {
		for _, q := range queues {
			q := q.DeepCopy()
			if err := cl.Create(ctx, q.DeepCopy()); err != nil {
				return err
			}
			if err := cache.AddLocalQueue(q); err != nil {
				return err
			}
		}
		return nil
	}
	insertAllWorkloads := func(ctx context.Context, cl client.Client, cache *Cache) error {
		for _, wl := range workloads {
			wl := wl.DeepCopy()
			if err := cl.Create(ctx, wl); err != nil {
				return err
			}
			cache.AddOrUpdateWorkload(wl)
		}
		return nil
	}
	cacheLocalQueuesAfterInsertingAll := map[string]*queue{
		"ns1/alpha": {
			key:                "ns1/alpha",
			reservingWorkloads: 1,
			admittedWorkloads:  1,
			usage: FlavorResourceQuantities{
				"spot": {
					corev1.ResourceCPU:    workload.ResourceValue(corev1.ResourceCPU, resource.MustParse("2")),
					corev1.ResourceMemory: workload.ResourceValue(corev1.ResourceMemory, resource.MustParse("8Gi")),
				},
				"model-a": {
					"example.com/gpu": workload.ResourceValue("example.com/gpu", resource.MustParse("0")),
				},
			},
			admittedUsage: FlavorResourceQuantities{
				"spot": {
					corev1.ResourceCPU:    workload.ResourceValue(corev1.ResourceCPU, resource.MustParse("2")),
					corev1.ResourceMemory: workload.ResourceValue(corev1.ResourceMemory, resource.MustParse("8Gi")),
				},
				"model-a": {
					"example.com/gpu": workload.ResourceValue("example.com/gpu", resource.MustParse("0")),
				},
			},
		},
		"ns2/beta": {
			key:                "ns2/beta",
			reservingWorkloads: 2,
			admittedWorkloads:  1,
			usage: FlavorResourceQuantities{
				"spot": {
					corev1.ResourceCPU:    workload.ResourceValue(corev1.ResourceCPU, resource.MustParse("0")),
					corev1.ResourceMemory: workload.ResourceValue(corev1.ResourceMemory, resource.MustParse("0")),
				},
				"model-a": {
					"example.com/gpu": workload.ResourceValue("example.com/gpu", resource.MustParse("7")),
				},
			},
			admittedUsage: FlavorResourceQuantities{
				"spot": {
					corev1.ResourceCPU:    workload.ResourceValue(corev1.ResourceCPU, resource.MustParse("0")),
					corev1.ResourceMemory: workload.ResourceValue(corev1.ResourceMemory, resource.MustParse("0")),
				},
				"model-a": {
					"example.com/gpu": workload.ResourceValue("example.com/gpu", resource.MustParse("2")),
				},
			},
		},
		"ns1/gamma": {
			key:                "ns1/gamma",
			reservingWorkloads: 1,
			admittedWorkloads:  0,
			usage: FlavorResourceQuantities{
				"ondemand": {
					corev1.ResourceCPU:    workload.ResourceValue(corev1.ResourceCPU, resource.MustParse("5")),
					corev1.ResourceMemory: workload.ResourceValue(corev1.ResourceMemory, resource.MustParse("16Gi")),
				},
				"model-b": {
					"example.com/gpu": workload.ResourceValue("example.com/gpu", resource.MustParse("0")),
				},
			},
			admittedUsage: FlavorResourceQuantities{
				"ondemand": {
					corev1.ResourceCPU:    workload.ResourceValue(corev1.ResourceCPU, resource.MustParse("0")),
					corev1.ResourceMemory: workload.ResourceValue(corev1.ResourceMemory, resource.MustParse("0")),
				},
				"model-b": {
					"example.com/gpu": workload.ResourceValue("example.com/gpu", resource.MustParse("0")),
				},
			},
		},
	}
	cacheLocalQueuesAfterInsertingCqAndQ := map[string]*queue{
		"ns1/alpha": {
			key:                "ns1/alpha",
			reservingWorkloads: 0,
			admittedWorkloads:  0,
			usage: FlavorResourceQuantities{
				"spot": {
					corev1.ResourceCPU:    workload.ResourceValue(corev1.ResourceCPU, resource.MustParse("0")),
					corev1.ResourceMemory: workload.ResourceValue(corev1.ResourceMemory, resource.MustParse("0")),
				},
				"model-a": {
					"example.com/gpu": workload.ResourceValue("example.com/gpu", resource.MustParse("0")),
				},
			},
			admittedUsage: FlavorResourceQuantities{
				"spot": {
					corev1.ResourceCPU:    workload.ResourceValue(corev1.ResourceCPU, resource.MustParse("0")),
					corev1.ResourceMemory: workload.ResourceValue(corev1.ResourceMemory, resource.MustParse("0")),
				},
				"model-a": {
					"example.com/gpu": workload.ResourceValue("example.com/gpu", resource.MustParse("0")),
				},
			},
		},
		"ns2/beta": {
			key:                "ns2/beta",
			reservingWorkloads: 0,
			admittedWorkloads:  0,
			usage: FlavorResourceQuantities{
				"spot": {
					corev1.ResourceCPU:    workload.ResourceValue(corev1.ResourceCPU, resource.MustParse("0")),
					corev1.ResourceMemory: workload.ResourceValue(corev1.ResourceMemory, resource.MustParse("0")),
				},
				"model-a": {
					"example.com/gpu": workload.ResourceValue("example.com/gpu", resource.MustParse("0")),
				},
			},
			admittedUsage: FlavorResourceQuantities{
				"spot": {
					corev1.ResourceCPU:    workload.ResourceValue(corev1.ResourceCPU, resource.MustParse("0")),
					corev1.ResourceMemory: workload.ResourceValue(corev1.ResourceMemory, resource.MustParse("0")),
				},
				"model-a": {
					"example.com/gpu": workload.ResourceValue("example.com/gpu", resource.MustParse("0")),
				},
			},
		},
		"ns1/gamma": {
			key:                "ns1/gamma",
			reservingWorkloads: 0,
			admittedWorkloads:  0,
			usage: FlavorResourceQuantities{
				"ondemand": {
					corev1.ResourceCPU:    workload.ResourceValue(corev1.ResourceCPU, resource.MustParse("0")),
					corev1.ResourceMemory: workload.ResourceValue(corev1.ResourceMemory, resource.MustParse("0")),
				},
				"model-b": {
					"example.com/gpu": workload.ResourceValue("example.com/gpu", resource.MustParse("0")),
				},
			},
			admittedUsage: FlavorResourceQuantities{
				"ondemand": {
					corev1.ResourceCPU:    workload.ResourceValue(corev1.ResourceCPU, resource.MustParse("0")),
					corev1.ResourceMemory: workload.ResourceValue(corev1.ResourceMemory, resource.MustParse("0")),
				},
				"model-b": {
					"example.com/gpu": workload.ResourceValue("example.com/gpu", resource.MustParse("0")),
				},
			},
		},
	}
	cases := map[string]struct {
		ops             []func(context.Context, client.Client, *Cache) error
		wantLocalQueues map[string]*queue
	}{
		"insert cqs, queues, workloads": {
			ops: []func(ctx context.Context, cl client.Client, cache *Cache) error{
				insertAllClusterQueues,
				insertAllQueues,
				insertAllWorkloads,
			},
			wantLocalQueues: cacheLocalQueuesAfterInsertingAll,
		},
		"insert cqs, workloads but no queues": {
			ops: []func(context.Context, client.Client, *Cache) error{
				insertAllClusterQueues,
				insertAllWorkloads,
			},
			wantLocalQueues: map[string]*queue{},
		},
		"insert queues, workloads but no cqs": {
			ops: []func(context.Context, client.Client, *Cache) error{
				insertAllQueues,
				insertAllWorkloads,
			},
			wantLocalQueues: map[string]*queue{},
		},
		"insert queues last": {
			ops: []func(context.Context, client.Client, *Cache) error{
				insertAllClusterQueues,
				insertAllWorkloads,
				insertAllQueues,
			},
			wantLocalQueues: cacheLocalQueuesAfterInsertingAll,
		},
		"insert cqs last": {
			ops: []func(context.Context, client.Client, *Cache) error{
				insertAllQueues,
				insertAllWorkloads,
				insertAllClusterQueues,
			},
			wantLocalQueues: cacheLocalQueuesAfterInsertingAll,
		},
		"assume": {
			ops: []func(context.Context, client.Client, *Cache) error{
				insertAllClusterQueues,
				insertAllQueues,
				func(ctx context.Context, cl client.Client, cache *Cache) error {
					wl := workloads[0].DeepCopy()
					if err := cl.Create(ctx, wl); err != nil {
						return err
					}
					return cache.AssumeWorkload(wl)
				},
			},
			wantLocalQueues: map[string]*queue{
				"ns1/alpha": cacheLocalQueuesAfterInsertingAll["ns1/alpha"],
				"ns2/beta":  cacheLocalQueuesAfterInsertingCqAndQ["ns2/beta"],
				"ns1/gamma": cacheLocalQueuesAfterInsertingCqAndQ["ns1/gamma"],
			},
		},
		"assume and forget": {
			ops: []func(context.Context, client.Client, *Cache) error{
				insertAllClusterQueues,
				insertAllQueues,
				func(ctx context.Context, cl client.Client, cache *Cache) error {
					wl := workloads[0].DeepCopy()
					if err := cl.Create(ctx, wl); err != nil {
						return err
					}
					if err := cache.AssumeWorkload(wl); err != nil {
						return err
					}
					return cache.ForgetWorkload(wl)
				},
			},
			wantLocalQueues: map[string]*queue{
				"ns1/alpha": cacheLocalQueuesAfterInsertingCqAndQ["ns1/alpha"],
				"ns2/beta":  cacheLocalQueuesAfterInsertingCqAndQ["ns2/beta"],
				"ns1/gamma": cacheLocalQueuesAfterInsertingCqAndQ["ns1/gamma"],
			},
		},
		"delete workload": {
			ops: []func(ctx context.Context, cl client.Client, cache *Cache) error{
				insertAllClusterQueues,
				insertAllQueues,
				insertAllWorkloads,
				func(ctx context.Context, cl client.Client, cache *Cache) error {
					return cache.DeleteWorkload(workloads[0])
				},
			},
			wantLocalQueues: map[string]*queue{
				"ns1/alpha": cacheLocalQueuesAfterInsertingCqAndQ["ns1/alpha"],
				"ns2/beta":  cacheLocalQueuesAfterInsertingAll["ns2/beta"],
				"ns1/gamma": cacheLocalQueuesAfterInsertingAll["ns1/gamma"],
			},
		},
		"delete cq": {
			ops: []func(ctx context.Context, cl client.Client, cache *Cache) error{
				insertAllClusterQueues,
				insertAllQueues,
				insertAllWorkloads,
				func(ctx context.Context, cl client.Client, cache *Cache) error {
					cache.DeleteClusterQueue(cqs[0])
					return nil
				},
			},
			wantLocalQueues: map[string]*queue{
				"ns1/gamma": cacheLocalQueuesAfterInsertingAll["ns1/gamma"],
			},
		},
		"delete queue": {
			ops: []func(ctx context.Context, cl client.Client, cache *Cache) error{
				insertAllClusterQueues,
				insertAllQueues,
				insertAllWorkloads,
				func(ctx context.Context, cl client.Client, cache *Cache) error {
					cache.DeleteLocalQueue(queues[0])
					return nil
				},
			},
			wantLocalQueues: map[string]*queue{
				"ns2/beta":  cacheLocalQueuesAfterInsertingAll["ns2/beta"],
				"ns1/gamma": cacheLocalQueuesAfterInsertingAll["ns1/gamma"],
			},
		},
		// Not tested: changing a workload's queue and changing a queue's cluster queue.
		// These operations should not be allowed by the webhook.
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			cl := utiltesting.NewFakeClient()
			cache := New(cl)
			ctx := context.Background()
			for i, op := range tc.ops {
				if err := op(ctx, cl, cache); err != nil {
					t.Fatalf("Running op %d: %v", i, err)
				}
			}
			cacheQueues := make(map[string]*queue)
			for _, cacheCQ := range cache.clusterQueues {
				for qKey, cacheQ := range cacheCQ.localQueues {
					if _, ok := cacheQueues[qKey]; ok {
						t.Fatalf("The cache have a duplicated localQueue %q across multiple clusterQueues", qKey)
					}
					cacheQueues[qKey] = cacheQ
				}
			}
			if diff := cmp.Diff(tc.wantLocalQueues, cacheQueues, cmp.AllowUnexported(queue{})); diff != "" {
				t.Errorf("Unexpected localQueues (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestClusterQueuesUsingFlavor(t *testing.T) {
	x86Rf := utiltesting.MakeResourceFlavor("x86").Obj()
	aarch64Rf := utiltesting.MakeResourceFlavor("aarch64").Obj()
	fooCq := utiltesting.MakeClusterQueue("fooCq").
		ResourceGroup(
			*utiltesting.MakeFlavorQuotas("x86").Resource("cpu", "5").Obj()).
		Obj()
	barCq := utiltesting.MakeClusterQueue("barCq").Obj()
	fizzCq := utiltesting.MakeClusterQueue("fizzCq").
		ResourceGroup(
			*utiltesting.MakeFlavorQuotas("x86").Resource("cpu", "5").Obj(),
			*utiltesting.MakeFlavorQuotas("aarch64").Resource("cpu", "3").Obj(),
		).
		Obj()

	tests := []struct {
		name                       string
		clusterQueues              []*kueue.ClusterQueue
		wantInUseClusterQueueNames []string
	}{
		{
			name: "single clusterQueue with flavor in use",
			clusterQueues: []*kueue.ClusterQueue{
				fooCq,
			},
			wantInUseClusterQueueNames: []string{fooCq.Name},
		},
		{
			name: "single clusterQueue with no flavor",
			clusterQueues: []*kueue.ClusterQueue{
				barCq,
			},
		},
		{
			name: "multiple clusterQueues with flavor in use",
			clusterQueues: []*kueue.ClusterQueue{
				fooCq,
				barCq,
				fizzCq,
			},
			wantInUseClusterQueueNames: []string{fooCq.Name, fizzCq.Name},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cache := New(utiltesting.NewFakeClient())
			cache.AddOrUpdateResourceFlavor(x86Rf)
			cache.AddOrUpdateResourceFlavor(aarch64Rf)

			for _, cq := range tc.clusterQueues {
				if err := cache.AddClusterQueue(context.Background(), cq); err != nil {
					t.Errorf("failed to add clusterQueue %s", cq.Name)
				}
			}

			cqs := cache.ClusterQueuesUsingFlavor("x86")
			if diff := cmp.Diff(tc.wantInUseClusterQueueNames, cqs, cmpopts.SortSlices(func(a, b string) bool {
				return a < b
			})); len(diff) != 0 {
				t.Errorf("Unexpected flavor is in use by clusterQueues (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestMatchingClusterQueues(t *testing.T) {
	clusterQueues := []*kueue.ClusterQueue{
		utiltesting.MakeClusterQueue("matching1").
			NamespaceSelector(&metav1.LabelSelector{}).Obj(),
		utiltesting.MakeClusterQueue("not-matching").
			NamespaceSelector(nil).Obj(),
		utiltesting.MakeClusterQueue("matching2").
			NamespaceSelector(&metav1.LabelSelector{
				MatchExpressions: []metav1.LabelSelectorRequirement{
					{
						Key:      "dep",
						Operator: metav1.LabelSelectorOpIn,
						Values:   []string{"eng"},
					},
				},
			}).Obj(),
	}
	wantCQs := sets.New("matching1", "matching2")

	cache := New(utiltesting.NewFakeClient())
	for _, cq := range clusterQueues {
		if err := cache.AddClusterQueue(context.Background(), cq); err != nil {
			t.Errorf("failed to add clusterQueue %s", cq.Name)
		}
	}

	gotCQs := cache.MatchingClusterQueues(map[string]string{"dep": "eng"})
	if diff := cmp.Diff(wantCQs, gotCQs); diff != "" {
		t.Errorf("Wrong ClusterQueues (-want,+got):\n%s", diff)
	}
}

// TestWaitForPodsReadyCancelled ensures that the WaitForPodsReady call does not block when the context is closed.
func TestWaitForPodsReadyCancelled(t *testing.T) {
	cache := New(utiltesting.NewFakeClient(), WithPodsReadyTracking(true))
	ctx, cancel := context.WithCancel(context.Background())
	log := ctrl.LoggerFrom(ctx)

	go cache.CleanUpOnContext(ctx)

	cq := kueue.ClusterQueue{
		ObjectMeta: metav1.ObjectMeta{Name: "one"},
	}
	if err := cache.AddClusterQueue(ctx, &cq); err != nil {
		t.Fatalf("Failed adding clusterQueue: %v", err)
	}

	wl := utiltesting.MakeWorkload("a", "").ReserveQuota(&kueue.Admission{
		ClusterQueue: "one",
	}).Obj()
	if err := cache.AssumeWorkload(wl); err != nil {
		t.Fatalf("Failed assuming the workload to block the further admission: %v", err)
	}

	if cache.PodsReadyForAllAdmittedWorkloads(log) {
		t.Fatalf("Unexpected that all admitted workloads are in PodsReady condition")
	}

	// cancel the context so that the WaitForPodsReady is returns
	go cancel()

	cache.WaitForPodsReady(ctx)
}

// TestCachePodsReadyForAllAdmittedWorkloads verifies the condition used to determine whether to wait
func TestCachePodsReadyForAllAdmittedWorkloads(t *testing.T) {
	clusterQueues := []kueue.ClusterQueue{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "one"},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "two"},
		},
	}

	cl := utiltesting.NewFakeClient()

	tests := []struct {
		name      string
		setup     func(cache *Cache) error
		operation func(cache *Cache) error
		wantReady bool
	}{
		{
			name:      "empty cache",
			operation: func(cache *Cache) error { return nil },
			wantReady: true,
		},
		{
			name: "add Workload without PodsReady condition",
			operation: func(cache *Cache) error {
				wl := utiltesting.MakeWorkload("a", "").ReserveQuota(&kueue.Admission{
					ClusterQueue: "one",
				}).Obj()
				cache.AddOrUpdateWorkload(wl)
				return nil
			},
			wantReady: false,
		},
		{
			name: "add Workload with PodsReady=False",
			operation: func(cache *Cache) error {
				wl := utiltesting.MakeWorkload("a", "").ReserveQuota(&kueue.Admission{
					ClusterQueue: "one",
				}).Condition(metav1.Condition{
					Type:   kueue.WorkloadPodsReady,
					Status: metav1.ConditionFalse,
				}).Obj()
				cache.AddOrUpdateWorkload(wl)
				return nil
			},
			wantReady: false,
		},
		{
			name: "add Workload with PodsReady=True",
			operation: func(cache *Cache) error {
				wl := utiltesting.MakeWorkload("a", "").ReserveQuota(&kueue.Admission{
					ClusterQueue: "one",
				}).Condition(metav1.Condition{
					Type:   kueue.WorkloadPodsReady,
					Status: metav1.ConditionTrue,
				}).Obj()
				cache.AddOrUpdateWorkload(wl)
				return nil
			},
			wantReady: true,
		},
		{
			name: "assume Workload without PodsReady condition",
			operation: func(cache *Cache) error {
				wl := utiltesting.MakeWorkload("a", "").ReserveQuota(&kueue.Admission{
					ClusterQueue: "one",
				}).Obj()
				return cache.AssumeWorkload(wl)
			},
			wantReady: false,
		},
		{
			name: "assume Workload with PodsReady=False",
			operation: func(cache *Cache) error {
				wl := utiltesting.MakeWorkload("a", "").ReserveQuota(&kueue.Admission{
					ClusterQueue: "one",
				}).Condition(metav1.Condition{
					Type:   kueue.WorkloadPodsReady,
					Status: metav1.ConditionFalse,
				}).Obj()
				return cache.AssumeWorkload(wl)
			},
			wantReady: false,
		},
		{
			name: "assume Workload with PodsReady=True",
			operation: func(cache *Cache) error {
				wl := utiltesting.MakeWorkload("a", "").ReserveQuota(&kueue.Admission{
					ClusterQueue: "one",
				}).Condition(metav1.Condition{
					Type:   kueue.WorkloadPodsReady,
					Status: metav1.ConditionTrue,
				}).Obj()
				return cache.AssumeWorkload(wl)
			},
			wantReady: true,
		},
		{
			name: "update workload to have PodsReady=True",
			setup: func(cache *Cache) error {
				wl := utiltesting.MakeWorkload("a", "").ReserveQuota(&kueue.Admission{
					ClusterQueue: "one",
				}).Obj()
				cache.AddOrUpdateWorkload(wl)
				return nil
			},
			operation: func(cache *Cache) error {
				wl := cache.clusterQueues["one"].Workloads["/a"].Obj
				newWl := wl.DeepCopy()
				apimeta.SetStatusCondition(&newWl.Status.Conditions, metav1.Condition{
					Type:   kueue.WorkloadPodsReady,
					Status: metav1.ConditionTrue,
				})
				return cache.UpdateWorkload(wl, newWl)
			},
			wantReady: true,
		},
		{
			name: "update workload to have PodsReady=False",
			setup: func(cache *Cache) error {
				wl := utiltesting.MakeWorkload("a", "").ReserveQuota(&kueue.Admission{
					ClusterQueue: "one",
				}).Condition(metav1.Condition{
					Type:   kueue.WorkloadPodsReady,
					Status: metav1.ConditionTrue,
				}).Obj()
				cache.AddOrUpdateWorkload(wl)
				return nil
			},
			operation: func(cache *Cache) error {
				wl := cache.clusterQueues["one"].Workloads["/a"].Obj
				newWl := wl.DeepCopy()
				apimeta.SetStatusCondition(&newWl.Status.Conditions, metav1.Condition{
					Type:   kueue.WorkloadPodsReady,
					Status: metav1.ConditionFalse,
				})
				return cache.UpdateWorkload(wl, newWl)
			},
			wantReady: false,
		},
		{
			name: "assume second workload without PodsReady",
			setup: func(cache *Cache) error {
				wl1 := utiltesting.MakeWorkload("a", "").ReserveQuota(&kueue.Admission{
					ClusterQueue: "one",
				}).Condition(metav1.Condition{
					Type:   kueue.WorkloadPodsReady,
					Status: metav1.ConditionTrue,
				}).Obj()
				cache.AddOrUpdateWorkload(wl1)
				return nil
			},
			operation: func(cache *Cache) error {
				wl2 := utiltesting.MakeWorkload("b", "").ReserveQuota(&kueue.Admission{
					ClusterQueue: "two",
				}).Obj()
				return cache.AssumeWorkload(wl2)
			},
			wantReady: false,
		},
		{
			name: "update second workload to have PodsReady=True",
			setup: func(cache *Cache) error {
				wl1 := utiltesting.MakeWorkload("a", "").ReserveQuota(&kueue.Admission{
					ClusterQueue: "one",
				}).Condition(metav1.Condition{
					Type:   kueue.WorkloadPodsReady,
					Status: metav1.ConditionTrue,
				}).Obj()
				cache.AddOrUpdateWorkload(wl1)
				wl2 := utiltesting.MakeWorkload("b", "").ReserveQuota(&kueue.Admission{
					ClusterQueue: "two",
				}).Obj()
				cache.AddOrUpdateWorkload(wl2)
				return nil
			},
			operation: func(cache *Cache) error {
				wl2 := cache.clusterQueues["two"].Workloads["/b"].Obj
				newWl2 := wl2.DeepCopy()
				apimeta.SetStatusCondition(&newWl2.Status.Conditions, metav1.Condition{
					Type:   kueue.WorkloadPodsReady,
					Status: metav1.ConditionTrue,
				})
				return cache.UpdateWorkload(wl2, newWl2)
			},
			wantReady: true,
		},
		{
			name: "delete workload with PodsReady=False",
			setup: func(cache *Cache) error {
				wl := utiltesting.MakeWorkload("a", "").ReserveQuota(&kueue.Admission{
					ClusterQueue: "one",
				}).Condition(metav1.Condition{
					Type:   kueue.WorkloadPodsReady,
					Status: metav1.ConditionFalse,
				}).Obj()
				cache.AddOrUpdateWorkload(wl)
				return nil
			},
			operation: func(cache *Cache) error {
				wl := cache.clusterQueues["one"].Workloads["/a"].Obj
				return cache.DeleteWorkload(wl)
			},
			wantReady: true,
		},
		{
			name: "forget workload with PodsReady=False",
			setup: func(cache *Cache) error {
				wl := utiltesting.MakeWorkload("a", "").ReserveQuota(&kueue.Admission{
					ClusterQueue: "one",
				}).Condition(metav1.Condition{
					Type:   kueue.WorkloadPodsReady,
					Status: metav1.ConditionFalse,
				}).Obj()
				return cache.AssumeWorkload(wl)
			},
			operation: func(cache *Cache) error {
				wl := cache.clusterQueues["one"].Workloads["/a"].Obj
				return cache.ForgetWorkload(wl)
			},
			wantReady: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cache := New(cl, WithPodsReadyTracking(true))
			ctx := context.Background()
			log := ctrl.LoggerFrom(ctx)

			for _, c := range clusterQueues {
				if err := cache.AddClusterQueue(ctx, &c); err != nil {
					t.Fatalf("Failed adding clusterQueue: %v", err)
				}
			}
			if tc.setup != nil {
				if err := tc.setup(cache); err != nil {
					t.Errorf("Unexpected error during setup: %q", err)
				}
			}
			if err := tc.operation(cache); err != nil {
				t.Errorf("Unexpected error during operation: %q", err)
			}
			gotReady := cache.PodsReadyForAllAdmittedWorkloads(log)
			if diff := cmp.Diff(tc.wantReady, gotReady); diff != "" {
				t.Errorf("Unexpected response about workloads without pods ready (-want,+got):\n%s", diff)
			}
			// verify that the WaitForPodsReady is non-blocking when podsReadyForAllAdmittedWorkloads returns true
			if gotReady {
				cache.WaitForPodsReady(ctx)
			}
		})
	}
}

// TestIsAssumedOrAdmittedCheckWorkload verifies if workload is in Assumed map from cache or if it is Admitted in one ClusterQueue
func TestIsAssumedOrAdmittedCheckWorkload(t *testing.T) {
	tests := []struct {
		name     string
		cache    *Cache
		workload workload.Info
		expected bool
	}{
		{
			name: "Workload Is Assumed and not Admitted",
			cache: &Cache{
				assumedWorkloads: map[string]string{"workload_namespace/workload_name": "test", "test2": "test2"},
			},
			workload: workload.Info{
				ClusterQueue: "ClusterQueue1",
				Obj: &kueue.Workload{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "workload_name",
						Namespace: "workload_namespace",
					},
				},
			},
			expected: true,
		}, {
			name: "Workload Is not Assumed but is Admitted",
			cache: &Cache{
				clusterQueues: map[string]*ClusterQueue{
					"ClusterQueue1": {
						Name: "ClusterQueue1",
						Workloads: map[string]*workload.Info{"workload_namespace/workload_name": {
							Obj: &kueue.Workload{
								ObjectMeta: metav1.ObjectMeta{
									Name:      "workload_name",
									Namespace: "workload_namespace",
								},
							},
						}},
					}},
			},

			workload: workload.Info{
				ClusterQueue: "ClusterQueue1",
				Obj: &kueue.Workload{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "workload_name",
						Namespace: "workload_namespace",
					},
				},
			},
			expected: true,
		}, {
			name: "Workload Is Assumed and Admitted",
			cache: &Cache{
				clusterQueues: map[string]*ClusterQueue{
					"ClusterQueue1": {
						Name: "ClusterQueue1",
						Workloads: map[string]*workload.Info{"workload_namespace/workload_name": {
							Obj: &kueue.Workload{
								ObjectMeta: metav1.ObjectMeta{
									Name:      "workload_name",
									Namespace: "workload_namespace",
								},
							},
						}},
					}},
				assumedWorkloads: map[string]string{"workload_namespace/workload_name": "test", "test2": "test2"},
			},
			workload: workload.Info{
				ClusterQueue: "ClusterQueue1",
				Obj: &kueue.Workload{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "workload_name",
						Namespace: "workload_namespace",
					},
				},
			},
			expected: true,
		}, {
			name: "Workload Is not Assumed and is not Admitted",
			cache: &Cache{
				clusterQueues: map[string]*ClusterQueue{
					"ClusterQueue1": {
						Name: "ClusterQueue1",
						Workloads: map[string]*workload.Info{"workload_namespace2/workload_name2": {
							Obj: &kueue.Workload{
								ObjectMeta: metav1.ObjectMeta{
									Name:      "workload_name2",
									Namespace: "workload_namespace2",
								},
							},
						}},
					}},
			},
			workload: workload.Info{
				ClusterQueue: "ClusterQueue1",
				Obj: &kueue.Workload{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "workload_name",
						Namespace: "workload_namespace",
					},
				},
			},
			expected: false,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			if tc.cache.IsAssumedOrAdmittedWorkload(tc.workload) != tc.expected {
				t.Error("Unexpected response")
			}
		})
	}
}

func messageOrEmpty(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

func TestClusterQueuesUsingAdmissionChecks(t *testing.T) {
	checks := []*kueue.AdmissionCheck{
		utiltesting.MakeAdmissionCheck("ac1").Obj(),
		utiltesting.MakeAdmissionCheck("ac2").Obj(),
		utiltesting.MakeAdmissionCheck("ac3").Obj(),
	}

	fooCq := utiltesting.MakeClusterQueue("fooCq").
		AdmissionChecks("ac1").
		Obj()
	barCq := utiltesting.MakeClusterQueue("barCq").Obj()
	fizzCq := utiltesting.MakeClusterQueue("fizzCq").
		AdmissionChecks("ac1", "ac2").
		Obj()
	strategyCq := utiltesting.MakeClusterQueue("strategyCq").
		AdmissionCheckStrategy(
			*utiltesting.MakeAdmissionCheckStrategyRule("ac1").Obj(),
			*utiltesting.MakeAdmissionCheckStrategyRule("ac3").Obj()).
		Obj()

	cases := map[string]struct {
		clusterQueues              []*kueue.ClusterQueue
		wantInUseClusterQueueNames []string
		check                      string
	}{
		"single clusterQueue with check in use": {
			clusterQueues:              []*kueue.ClusterQueue{fooCq},
			wantInUseClusterQueueNames: []string{fooCq.Name},
			check:                      "ac1",
		},
		"single clusterQueue with AdmissionCheckStrategy in use": {
			clusterQueues:              []*kueue.ClusterQueue{strategyCq},
			wantInUseClusterQueueNames: []string{strategyCq.Name},
			check:                      "ac3",
		},
		"single clusterQueue with no checks": {
			clusterQueues: []*kueue.ClusterQueue{barCq},
			check:         "ac1",
		},
		"multiple clusterQueues with checks in use": {
			clusterQueues: []*kueue.ClusterQueue{
				fooCq,
				barCq,
				fizzCq,
				strategyCq,
			},
			wantInUseClusterQueueNames: []string{fooCq.Name, fizzCq.Name, strategyCq.Name},
			check:                      "ac1",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			cache := New(utiltesting.NewFakeClient())
			for _, check := range checks {
				cache.AddOrUpdateAdmissionCheck(check)
			}

			for _, cq := range tc.clusterQueues {
				if err := cache.AddClusterQueue(context.Background(), cq); err != nil {
					t.Errorf("failed to add clusterQueue %s", cq.Name)
				}
			}

			cqs := cache.ClusterQueuesUsingAdmissionCheck(tc.check)
			if diff := cmp.Diff(tc.wantInUseClusterQueueNames, cqs, cmpopts.SortSlices(func(a, b string) bool {
				return a < b
			})); len(diff) != 0 {
				t.Errorf("Unexpected AdmissionCheck is in use by clusterQueues (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestClusterQueueReadiness(t *testing.T) {
	baseFlavor := utiltesting.MakeResourceFlavor("flavor1").Obj()
	baseCheck := utiltesting.MakeAdmissionCheck("check1").Active(metav1.ConditionTrue).Obj()
	baseQueue := utiltesting.MakeClusterQueue("queue1").
		ResourceGroup(
			*utiltesting.MakeFlavorQuotas(baseFlavor.Name).
				Resource(corev1.ResourceCPU, "10", "10").Obj()).
		AdmissionChecks(baseCheck.Name).
		Obj()

	cases := map[string]struct {
		clusterQueues    []*kueue.ClusterQueue
		resourceFlavors  []*kueue.ResourceFlavor
		admissionChecks  []*kueue.AdmissionCheck
		clusterQueueName string
		terminate        bool
		wantStatus       metav1.ConditionStatus
		wantReason       string
		wantMessage      string
		wantActive       bool
		wantTerminating  bool
	}{
		"queue not found": {
			clusterQueueName: "queue1",
			wantStatus:       metav1.ConditionFalse,
			wantReason:       "NotFound",
			wantMessage:      "ClusterQueue not found",
		},
		"flavor not found": {
			clusterQueues:    []*kueue.ClusterQueue{baseQueue},
			admissionChecks:  []*kueue.AdmissionCheck{baseCheck},
			clusterQueueName: "queue1",
			wantStatus:       metav1.ConditionFalse,
			wantReason:       "FlavorNotFound",
			wantMessage:      "Can't admit new workloads: FlavorNotFound",
		},
		"check not found": {
			clusterQueues:    []*kueue.ClusterQueue{baseQueue},
			resourceFlavors:  []*kueue.ResourceFlavor{baseFlavor},
			clusterQueueName: "queue1",
			wantStatus:       metav1.ConditionFalse,
			wantReason:       "CheckNotFoundOrInactive",
			wantMessage:      "Can't admit new workloads: CheckNotFoundOrInactive",
		},
		"check inactive": {
			clusterQueues:    []*kueue.ClusterQueue{baseQueue},
			resourceFlavors:  []*kueue.ResourceFlavor{baseFlavor},
			admissionChecks:  []*kueue.AdmissionCheck{utiltesting.MakeAdmissionCheck("check1").Obj()},
			clusterQueueName: "queue1",
			wantStatus:       metav1.ConditionFalse,
			wantReason:       "CheckNotFoundOrInactive",
			wantMessage:      "Can't admit new workloads: CheckNotFoundOrInactive",
		},
		"flavor and check not found": {
			clusterQueues:    []*kueue.ClusterQueue{baseQueue},
			clusterQueueName: "queue1",
			wantStatus:       metav1.ConditionFalse,
			wantReason:       "FlavorNotFound",
			wantMessage:      "Can't admit new workloads: FlavorNotFound, CheckNotFoundOrInactive",
		},
		"terminating": {
			clusterQueues:    []*kueue.ClusterQueue{baseQueue},
			admissionChecks:  []*kueue.AdmissionCheck{baseCheck},
			resourceFlavors:  []*kueue.ResourceFlavor{baseFlavor},
			clusterQueueName: "queue1",
			terminate:        true,
			wantStatus:       metav1.ConditionFalse,
			wantReason:       "Terminating",
			wantMessage:      "Can't admit new workloads; clusterQueue is terminating",
			wantTerminating:  true,
		},
		"ready": {
			clusterQueues:    []*kueue.ClusterQueue{baseQueue},
			admissionChecks:  []*kueue.AdmissionCheck{baseCheck},
			resourceFlavors:  []*kueue.ResourceFlavor{baseFlavor},
			clusterQueueName: "queue1",
			wantStatus:       metav1.ConditionTrue,
			wantReason:       "Ready",
			wantMessage:      "Can admit new workloads",
			wantActive:       true,
		},
		"stopped": {
			clusterQueues:    []*kueue.ClusterQueue{utiltesting.MakeClusterQueue("queue1").StopPolicy(kueue.HoldAndDrain).Obj()},
			clusterQueueName: "queue1",
			wantStatus:       metav1.ConditionFalse,
			wantReason:       "Stopped",
			wantMessage:      "Can't admit new workloads: Stopped",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			cache := New(utiltesting.NewFakeClient())
			for _, rf := range tc.resourceFlavors {
				cache.AddOrUpdateResourceFlavor(rf)
			}
			for _, ac := range tc.admissionChecks {
				cache.AddOrUpdateAdmissionCheck(ac)
			}
			for _, cq := range tc.clusterQueues {
				if err := cache.AddClusterQueue(context.Background(), cq); err != nil {
					t.Errorf("failed to add clusterQueue %q: %v", cq.Name, err)
				}
			}

			if tc.terminate {
				cache.TerminateClusterQueue(tc.clusterQueueName)
			}

			gotStatus, gotReason, gotMessage := cache.ClusterQueueReadiness(tc.clusterQueueName)

			if diff := cmp.Diff(tc.wantStatus, gotStatus); len(diff) != 0 {
				t.Errorf("Unexpected status (-want,+got):\n%s", diff)
			}
			if diff := cmp.Diff(tc.wantReason, gotReason); len(diff) != 0 {
				t.Errorf("Unexpected reason (-want,+got):\n%s", diff)
			}
			if diff := cmp.Diff(tc.wantMessage, gotMessage); len(diff) != 0 {
				t.Errorf("Unexpected message (-want,+got):\n%s", diff)
			}

			if gotActive := cache.ClusterQueueActive(tc.clusterQueueName); gotActive != tc.wantActive {
				t.Errorf("Unexpected active state %v", gotActive)
			}

			if gotTerminating := cache.ClusterQueueTerminating(tc.clusterQueueName); gotTerminating != tc.wantTerminating {
				t.Errorf("Unexpected terminating state %v", gotTerminating)
			}
		})
	}
}
