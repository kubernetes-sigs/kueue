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
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/metrics"
	"sigs.k8s.io/kueue/pkg/util/pointer"
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
	}
	setup := func(cache *Cache) {
		cache.AddOrUpdateResourceFlavor(
			utiltesting.MakeResourceFlavor("default").
				Label("cpuType", "default").
				Obj())
		for _, c := range initialClusterQueues {
			if err := cache.AddClusterQueue(context.Background(), &c); err != nil {
				t.Fatalf("Failed adding ClusterQueue: %v", err)
			}
		}
	}
	cases := []struct {
		name              string
		operation         func(*Cache)
		wantClusterQueues map[string]*ClusterQueue
		wantCohorts       map[string]sets.Set[string]
	}{
		{
			name: "add",
			operation: func(cache *Cache) {
				setup(cache)
			},
			wantClusterQueues: map[string]*ClusterQueue{
				"a": {
					Name: "a",
					ResourceGroups: []ResourceGroup{{
						CoveredResources: sets.New(corev1.ResourceCPU),
						Flavors: []FlavorQuotas{{
							Name: "default",
							Resources: map[corev1.ResourceName]*ResourceQuota{
								corev1.ResourceCPU: {
									Nominal:        10_000,
									BorrowingLimit: pointer.Int64(10_000),
								},
							},
						}},
						LabelKeys: sets.New("cpuType"),
					}},
					NamespaceSelector: labels.Nothing(),
					Usage: FlavorResourceQuantities{
						"default": {corev1.ResourceCPU: 0},
					},
					Status:     active,
					Preemption: defaultPreemption,
				},
				"b": {
					Name: "b",
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
					Usage: FlavorResourceQuantities{
						"default": {corev1.ResourceCPU: 0},
					},
					Status:     active,
					Preemption: defaultPreemption,
				},
				"c": {
					Name:              "c",
					ResourceGroups:    []ResourceGroup{},
					NamespaceSelector: labels.Nothing(),
					Usage:             FlavorResourceQuantities{},
					Status:            active,
					Preemption:        defaultPreemption,
				},
				"d": {
					Name:              "d",
					ResourceGroups:    []ResourceGroup{},
					NamespaceSelector: labels.Nothing(),
					Usage:             FlavorResourceQuantities{},
					Status:            active,
					Preemption:        defaultPreemption,
				},
				"e": {
					Name: "e",
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
					Usage: FlavorResourceQuantities{
						"nonexistent-flavor": {corev1.ResourceCPU: 0},
					},
					Status:     pending,
					Preemption: defaultPreemption,
				},
			},
			wantCohorts: map[string]sets.Set[string]{
				"one": sets.New("a", "b"),
				"two": sets.New("c", "e"),
			},
		},
		{
			name: "add ClusterQueue with preemption policies",
			operation: func(cache *Cache) {
				cq := utiltesting.MakeClusterQueue("foo").Preemption(kueue.ClusterQueuePreemption{
					ReclaimWithinCohort: kueue.PreemptionPolicyLowerPriority,
					WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
				}).Obj()
				if err := cache.AddClusterQueue(context.Background(), cq); err != nil {
					t.Fatalf("Failed to add ClusterQueue: %v", err)
				}
			},
			wantClusterQueues: map[string]*ClusterQueue{
				"foo": {
					Name:              "foo",
					NamespaceSelector: labels.Everything(),
					Status:            active,
					Preemption: kueue.ClusterQueuePreemption{
						ReclaimWithinCohort: kueue.PreemptionPolicyLowerPriority,
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
					},
				},
			},
		},
		{
			name: "add flavors after queue capacities",
			operation: func(cache *Cache) {
				for _, c := range initialClusterQueues {
					if err := cache.AddClusterQueue(context.Background(), &c); err != nil {
						t.Fatalf("Failed adding ClusterQueue: %v", err)
					}
				}
				cache.AddOrUpdateResourceFlavor(
					utiltesting.MakeResourceFlavor("default").
						Label("cpuType", "default").
						Obj())
			},
			wantClusterQueues: map[string]*ClusterQueue{
				"a": {
					Name: "a",
					ResourceGroups: []ResourceGroup{{
						CoveredResources: sets.New(corev1.ResourceCPU),
						Flavors: []FlavorQuotas{{
							Name: "default",
							Resources: map[corev1.ResourceName]*ResourceQuota{
								corev1.ResourceCPU: {
									Nominal:        10_000,
									BorrowingLimit: pointer.Int64(10_000),
								},
							},
						}},
						LabelKeys: sets.New("cpuType"),
					}},
					NamespaceSelector: labels.Nothing(),
					Usage: FlavorResourceQuantities{
						"default": {corev1.ResourceCPU: 0},
					},
					Status:     active,
					Preemption: defaultPreemption,
				},
				"b": {
					Name: "b",
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
					Usage: FlavorResourceQuantities{
						"default": {corev1.ResourceCPU: 0},
					},
					Status:     active,
					Preemption: defaultPreemption,
				},
				"c": {
					Name:              "c",
					ResourceGroups:    []ResourceGroup{},
					NamespaceSelector: labels.Nothing(),
					Usage:             FlavorResourceQuantities{},
					Status:            active,
					Preemption:        defaultPreemption,
				},
				"d": {
					Name:              "d",
					ResourceGroups:    []ResourceGroup{},
					NamespaceSelector: labels.Nothing(),
					Usage:             FlavorResourceQuantities{},
					Status:            active,
					Preemption:        defaultPreemption,
				},
				"e": {
					Name: "e",
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
					Usage: FlavorResourceQuantities{
						"nonexistent-flavor": {corev1.ResourceCPU: 0},
					},
					Status:     pending,
					Preemption: defaultPreemption,
				},
			},
			wantCohorts: map[string]sets.Set[string]{
				"one": sets.New("a", "b"),
				"two": sets.New("c", "e"),
			},
		},
		{
			name: "update",
			operation: func(cache *Cache) {
				setup(cache)
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
								Resource(corev1.ResourceCPU, "5", "5").
								Obj()).
						Cohort("two").
						NamespaceSelector(nil).
						Obj(),
				}
				for _, c := range clusterQueues {
					if err := cache.UpdateClusterQueue(&c); err != nil {
						t.Fatalf("Failed updating ClusterQueue: %v", err)
					}
				}
				cache.AddOrUpdateResourceFlavor(
					utiltesting.MakeResourceFlavor("default").
						Label("cpuType", "default").
						Label("region", "central").
						Obj())
			},
			wantClusterQueues: map[string]*ClusterQueue{
				"a": {
					Name: "a",
					ResourceGroups: []ResourceGroup{{
						CoveredResources: sets.New(corev1.ResourceCPU),
						Flavors: []FlavorQuotas{{
							Name: "default",
							Resources: map[corev1.ResourceName]*ResourceQuota{
								corev1.ResourceCPU: {
									Nominal:        5_000,
									BorrowingLimit: pointer.Int64(5_000),
								},
							},
						}},
						LabelKeys: sets.New("cpuType", "region"),
					}},
					NamespaceSelector: labels.Nothing(),
					Usage: FlavorResourceQuantities{
						"default": {corev1.ResourceCPU: 0},
					},
					Status:     active,
					Preemption: defaultPreemption,
				},
				"b": {
					Name:              "b",
					ResourceGroups:    []ResourceGroup{},
					NamespaceSelector: labels.Everything(),
					Usage:             FlavorResourceQuantities{},
					Status:            active,
					Preemption:        defaultPreemption,
				},
				"c": {
					Name:              "c",
					ResourceGroups:    []ResourceGroup{},
					NamespaceSelector: labels.Nothing(),
					Usage:             FlavorResourceQuantities{},
					Status:            active,
					Preemption:        defaultPreemption,
				},
				"d": {
					Name:              "d",
					ResourceGroups:    []ResourceGroup{},
					NamespaceSelector: labels.Nothing(),
					Usage:             FlavorResourceQuantities{},
					Status:            active,
					Preemption:        defaultPreemption,
				},
				"e": {
					Name: "e",
					ResourceGroups: []ResourceGroup{{
						CoveredResources: sets.New(corev1.ResourceCPU),
						Flavors: []FlavorQuotas{{
							Name: "default",
							Resources: map[corev1.ResourceName]*ResourceQuota{
								corev1.ResourceCPU: {
									Nominal:        5_000,
									BorrowingLimit: pointer.Int64(5_000),
								},
							}},
						},
						LabelKeys: sets.New("cpuType", "region"),
					}},
					NamespaceSelector: labels.Nothing(),
					Usage: FlavorResourceQuantities{
						"default": {corev1.ResourceCPU: 0},
					},
					Status:     active,
					Preemption: defaultPreemption,
				},
			},
			wantCohorts: map[string]sets.Set[string]{
				"one": sets.New("b"),
				"two": sets.New("a", "c", "e"),
			},
		},
		{
			name: "delete",
			operation: func(cache *Cache) {
				setup(cache)
				clusterQueues := []kueue.ClusterQueue{
					{ObjectMeta: metav1.ObjectMeta{Name: "a"}},
					{ObjectMeta: metav1.ObjectMeta{Name: "d"}},
				}
				for _, c := range clusterQueues {
					cache.DeleteClusterQueue(&c)
				}
			},
			wantClusterQueues: map[string]*ClusterQueue{
				"b": {
					Name: "b",
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
					Usage: FlavorResourceQuantities{
						"default": {corev1.ResourceCPU: 0},
					},
					Status:     active,
					Preemption: defaultPreemption,
				},
				"c": {
					Name:              "c",
					ResourceGroups:    []ResourceGroup{},
					NamespaceSelector: labels.Nothing(),
					Usage:             FlavorResourceQuantities{},
					Status:            active,
					Preemption:        defaultPreemption,
				},
				"e": {
					Name: "e",
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
					Usage: FlavorResourceQuantities{
						"nonexistent-flavor": {corev1.ResourceCPU: 0},
					},
					Status:     pending,
					Preemption: defaultPreemption,
				},
			},
			wantCohorts: map[string]sets.Set[string]{
				"one": sets.New("b"),
				"two": sets.New("c", "e"),
			},
		},
		{
			name: "add resource flavors",
			operation: func(cache *Cache) {
				setup(cache)
				cache.AddOrUpdateResourceFlavor(&kueue.ResourceFlavor{
					ObjectMeta: metav1.ObjectMeta{Name: "nonexistent-flavor"},
				})
			},
			wantClusterQueues: map[string]*ClusterQueue{
				"a": {
					Name: "a",
					ResourceGroups: []ResourceGroup{{
						CoveredResources: sets.New(corev1.ResourceCPU),
						Flavors: []FlavorQuotas{{
							Name: "default",
							Resources: map[corev1.ResourceName]*ResourceQuota{
								corev1.ResourceCPU: {
									Nominal:        10_000,
									BorrowingLimit: pointer.Int64(10_000),
								},
							},
						}},
						LabelKeys: sets.New("cpuType"),
					}},
					NamespaceSelector: labels.Nothing(),
					Usage: FlavorResourceQuantities{
						"default": {corev1.ResourceCPU: 0},
					},
					Status:     active,
					Preemption: defaultPreemption,
				},
				"b": {
					Name: "b",
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
					Usage: FlavorResourceQuantities{
						"default": {corev1.ResourceCPU: 0},
					},
					Status:     active,
					Preemption: defaultPreemption,
				},
				"c": {
					Name:              "c",
					ResourceGroups:    []ResourceGroup{},
					NamespaceSelector: labels.Nothing(),
					Usage:             FlavorResourceQuantities{},
					Status:            active,
					Preemption:        defaultPreemption,
				},
				"d": {
					Name:              "d",
					ResourceGroups:    []ResourceGroup{},
					NamespaceSelector: labels.Nothing(),
					Usage:             FlavorResourceQuantities{},
					Status:            active,
					Preemption:        defaultPreemption,
				},
				"e": {
					Name: "e",
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
					Usage:             FlavorResourceQuantities{"nonexistent-flavor": {corev1.ResourceCPU: 0}},
					Status:            active,
					Preemption:        defaultPreemption,
				},
			},
			wantCohorts: map[string]sets.Set[string]{
				"one": sets.New("a", "b"),
				"two": sets.New("c", "e"),
			},
		},
		{
			name: "Add ClusterQueue with multiple resource groups",
			operation: func(cache *Cache) {
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
					t.Fatalf("Adding ClusterQueue: %v", err)
				}
			},
			wantClusterQueues: map[string]*ClusterQueue{
				"foo": {
					Name:              "foo",
					NamespaceSelector: labels.Everything(),
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
					Status:     pending,
					Preemption: defaultPreemption,
				},
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cache := New(utiltesting.NewFakeClient())
			tc.operation(cache)
			if diff := cmp.Diff(tc.wantClusterQueues, cache.clusterQueues,
				cmpopts.IgnoreFields(ClusterQueue{}, "Cohort", "Workloads", "RGByResource"),
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
		utiltesting.MakeWorkload("a", "").PodSets(podSets...).Admit(&kueue.Admission{
			ClusterQueue:      "one",
			PodSetAssignments: podSetFlavors,
		}).Obj(),
		utiltesting.MakeWorkload("b", "").Admit(&kueue.Admission{
			ClusterQueue: "one",
		}).Obj(),
		utiltesting.MakeWorkload("c", "").PodSets(podSets...).Admit(&kueue.Admission{
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
					utiltesting.MakeWorkload("a", "").PodSets(podSets...).Admit(&kueue.Admission{
						ClusterQueue:      "one",
						PodSetAssignments: podSetFlavors,
					}).Obj(),
					utiltesting.MakeWorkload("d", "").Admit(&kueue.Admission{
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
				w := utiltesting.MakeWorkload("d", "").Admit(&kueue.Admission{
					ClusterQueue: "three",
				}).Obj()
				if !cache.AddOrUpdateWorkload(w) {
					return fmt.Errorf("failed to add workload")
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
				w := utiltesting.MakeWorkload("b", "").Admit(&kueue.Admission{
					ClusterQueue: "one",
				}).Obj()
				if !cache.AddOrUpdateWorkload(w) {
					return fmt.Errorf("failed to add workload")
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
			name: "update",
			operation: func(cache *Cache) error {
				old := utiltesting.MakeWorkload("a", "").Admit(&kueue.Admission{
					ClusterQueue: "one",
				}).Obj()
				latest := utiltesting.MakeWorkload("a", "").PodSets(podSets...).Admit(&kueue.Admission{
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
				old := utiltesting.MakeWorkload("d", "").Admit(&kueue.Admission{
					ClusterQueue: "three",
				}).Obj()
				latest := utiltesting.MakeWorkload("d", "").Admit(&kueue.Admission{
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
				old := utiltesting.MakeWorkload("d", "").Admit(&kueue.Admission{
					ClusterQueue: "one",
				}).Obj()
				latest := utiltesting.MakeWorkload("d", "").Admit(&kueue.Admission{
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
				old := utiltesting.MakeWorkload("d", "").Admit(&kueue.Admission{
					ClusterQueue: "one",
				}).Obj()
				latest := utiltesting.MakeWorkload("d", "").Admit(&kueue.Admission{
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
				w := utiltesting.MakeWorkload("a", "").Admit(&kueue.Admission{
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
				w := utiltesting.MakeWorkload("a", "").Admit(&kueue.Admission{
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
				w := utiltesting.MakeWorkload("d", "").Admit(&kueue.Admission{
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
					utiltesting.MakeWorkload("d", "").PodSets(podSets...).Admit(&kueue.Admission{
						ClusterQueue:      "one",
						PodSetAssignments: podSetFlavors,
					}).Obj(),
					utiltesting.MakeWorkload("e", "").PodSets(podSets...).Admit(&kueue.Admission{
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
				w := utiltesting.MakeWorkload("d", "").PodSets(podSets...).Admit(&kueue.Admission{
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
					utiltesting.MakeWorkload("d", "").PodSets(podSets...).Admit(&kueue.Admission{
						ClusterQueue:      "one",
						PodSetAssignments: podSetFlavors,
					}).Obj(),
					utiltesting.MakeWorkload("e", "").PodSets(podSets...).Admit(&kueue.Admission{
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
				w := utiltesting.MakeWorkload("b", "").Admit(&kueue.Admission{
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
					utiltesting.MakeWorkload("d", "").PodSets(podSets...).Admit(&kueue.Admission{
						ClusterQueue:      "one",
						PodSetAssignments: podSetFlavors,
					}).Obj(),
					utiltesting.MakeWorkload("e", "").PodSets(podSets...).Admit(&kueue.Admission{
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
					return fmt.Errorf("failed to add workload")
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
			gotWorkloads := make(map[string]result)
			for name, cq := range cache.clusterQueues {
				gotWorkloads[name] = result{Workloads: sets.KeySet(cq.Workloads), UsedResources: cq.Usage}
			}
			if diff := cmp.Diff(step.wantResults, gotWorkloads); diff != "" {
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
	cq := *utiltesting.MakeClusterQueue("foo").
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
		Obj()
	workloads := []kueue.Workload{
		*utiltesting.MakeWorkload("one", "").
			Request(corev1.ResourceCPU, "8").
			Request("example.com/gpu", "5").
			Admit(utiltesting.MakeAdmission("foo").Assignment(corev1.ResourceCPU, "default", "8000m").Assignment("example.com/gpu", "model_a", "5").Obj()).
			Obj(),
		*utiltesting.MakeWorkload("two", "").
			Request(corev1.ResourceCPU, "5").
			Request("example.com/gpu", "6").
			Admit(utiltesting.MakeAdmission("foo").Assignment(corev1.ResourceCPU, "default", "5000m").Assignment("example.com/gpu", "model_b", "6").Obj()).
			Obj(),
	}
	cases := map[string]struct {
		workloads         []kueue.Workload
		wantUsedResources []kueue.FlavorUsage
		wantWorkloads     int
	}{
		"single no borrowing": {
			workloads: workloads[:1],
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
			},
			wantWorkloads: 1,
		},
		"multiple borrowing": {
			workloads: workloads,
			wantUsedResources: []kueue.FlavorUsage{
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
			},
			wantWorkloads: 2,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			cache := New(utiltesting.NewFakeClient())
			ctx := context.Background()
			err := cache.AddClusterQueue(ctx, &cq)
			if err != nil {
				t.Fatalf("Adding ClusterQueue: %v", err)
			}
			for _, w := range tc.workloads {
				if added := cache.AddOrUpdateWorkload(&w); !added {
					t.Fatalf("Workload %s was not added", workload.Key(&w))
				}
			}
			resources, workloads, err := cache.Usage(&cq)
			if err != nil {
				t.Fatalf("Couldn't get usage: %v", err)
			}
			if diff := cmp.Diff(tc.wantUsedResources, resources); diff != "" {
				t.Errorf("Unexpected used resources (-want,+got):\n%s", diff)
			}
			if workloads != tc.wantWorkloads {
				t.Errorf("Got %d workloads, want %d", workloads, tc.wantWorkloads)
			}
		})
	}
}

func TestCacheQueueOperations(t *testing.T) {
	cqs := []*kueue.ClusterQueue{
		utiltesting.MakeClusterQueue("foo").Obj(),
		utiltesting.MakeClusterQueue("bar").Obj(),
	}
	queues := []*kueue.LocalQueue{
		utiltesting.MakeLocalQueue("alpha", "ns1").ClusterQueue("foo").Obj(),
		utiltesting.MakeLocalQueue("beta", "ns2").ClusterQueue("foo").Obj(),
		utiltesting.MakeLocalQueue("gamma", "ns1").ClusterQueue("bar").Obj(),
	}
	workloads := []*kueue.Workload{
		utiltesting.MakeWorkload("job1", "ns1").Queue("alpha").Admit(utiltesting.MakeAdmission("foo").Obj()).Obj(),
		utiltesting.MakeWorkload("job2", "ns2").Queue("beta").Admit(utiltesting.MakeAdmission("foo").Obj()).Obj(),
		utiltesting.MakeWorkload("job3", "ns1").Queue("gamma").Admit(utiltesting.MakeAdmission("bar").Obj()).Obj(),
		utiltesting.MakeWorkload("job4", "ns2").Queue("beta").Admit(utiltesting.MakeAdmission("foo").Obj()).Obj(),
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
	cases := map[string]struct {
		ops             []func(context.Context, client.Client, *Cache) error
		wantQueueCounts map[string]int32
	}{
		"insert cqs, queues, workloads": {
			ops: []func(ctx context.Context, cl client.Client, cache *Cache) error{
				insertAllClusterQueues,
				insertAllQueues,
				insertAllWorkloads,
			},
			wantQueueCounts: map[string]int32{
				"ns1/alpha": 1,
				"ns2/beta":  2,
				"ns1/gamma": 1,
			},
		},
		"insert cqs, workloads but no queues": {
			ops: []func(context.Context, client.Client, *Cache) error{
				insertAllClusterQueues,
				insertAllWorkloads,
			},
			wantQueueCounts: map[string]int32{},
		},
		"insert queues, workloads but no cqs": {
			ops: []func(context.Context, client.Client, *Cache) error{
				insertAllQueues,
				insertAllWorkloads,
			},
			wantQueueCounts: map[string]int32{},
		},
		"insert queues last": {
			ops: []func(context.Context, client.Client, *Cache) error{
				insertAllClusterQueues,
				insertAllWorkloads,
				insertAllQueues,
			},
			wantQueueCounts: map[string]int32{
				"ns1/alpha": 1,
				"ns2/beta":  2,
				"ns1/gamma": 1,
			},
		},
		"insert cqs last": {
			ops: []func(context.Context, client.Client, *Cache) error{
				insertAllQueues,
				insertAllWorkloads,
				insertAllClusterQueues,
			},
			wantQueueCounts: map[string]int32{
				"ns1/alpha": 1,
				"ns2/beta":  2,
				"ns1/gamma": 1,
			},
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
			wantQueueCounts: map[string]int32{
				"ns1/alpha": 1,
				"ns2/beta":  0,
				"ns1/gamma": 0,
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
			wantQueueCounts: map[string]int32{
				"ns1/alpha": 0,
				"ns2/beta":  0,
				"ns1/gamma": 0,
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
			wantQueueCounts: map[string]int32{
				"ns1/alpha": 0,
				"ns2/beta":  2,
				"ns1/gamma": 1,
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
			wantQueueCounts: map[string]int32{
				"ns1/gamma": 1,
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
			wantQueueCounts: map[string]int32{
				"ns2/beta":  2,
				"ns1/gamma": 1,
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
			for _, lq := range queues {
				queueAdmitted := cache.AdmittedWorkloadsInLocalQueue(lq)
				key := fmt.Sprintf("%s/%s", lq.Namespace, lq.Name)
				if diff := cmp.Diff(tc.wantQueueCounts[key], queueAdmitted); diff != "" {
					t.Errorf("Want %d but got %d for %s", tc.wantQueueCounts[key], queueAdmitted, key)
				}
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

func TestClusterQueueUpdateWithFlavors(t *testing.T) {
	rf := utiltesting.MakeResourceFlavor("x86").Obj()
	cq := utiltesting.MakeClusterQueue("cq").
		ResourceGroup(*utiltesting.MakeFlavorQuotas("x86").Resource("cpu", "5").Obj()).
		Obj()

	testcases := []struct {
		name       string
		curStatus  metrics.ClusterQueueStatus
		flavors    map[kueue.ResourceFlavorReference]*kueue.ResourceFlavor
		wantStatus metrics.ClusterQueueStatus
	}{
		{
			name:      "Pending clusterQueue updated existent flavors",
			curStatus: pending,
			flavors: map[kueue.ResourceFlavorReference]*kueue.ResourceFlavor{
				kueue.ResourceFlavorReference(rf.Name): rf,
			},
			wantStatus: active,
		},
		{
			name:       "Active clusterQueue updated with not found flavors",
			curStatus:  active,
			flavors:    map[kueue.ResourceFlavorReference]*kueue.ResourceFlavor{},
			wantStatus: pending,
		},
		{
			name:      "Terminating clusterQueue updated with existent flavors",
			curStatus: terminating,
			flavors: map[kueue.ResourceFlavorReference]*kueue.ResourceFlavor{
				kueue.ResourceFlavorReference(rf.Name): rf,
			},
			wantStatus: terminating,
		},
		{
			name:       "Terminating clusterQueue updated with not found flavors",
			curStatus:  terminating,
			wantStatus: terminating,
		},
	}

	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			cache := New(utiltesting.NewFakeClient())
			cq, err := cache.newClusterQueue(cq)
			if err != nil {
				t.Fatalf("failed to new clusterQueue %v", err)
			}

			cq.Status = tc.curStatus
			cq.UpdateWithFlavors(tc.flavors)

			if cq.Status != tc.wantStatus {
				t.Fatalf("got different status, want: %v, got: %v", tc.wantStatus, cq.Status)
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

	go cache.CleanUpOnContext(ctx)

	cq := kueue.ClusterQueue{
		ObjectMeta: metav1.ObjectMeta{Name: "one"},
	}
	if err := cache.AddClusterQueue(ctx, &cq); err != nil {
		t.Fatalf("Failed adding clusterQueue: %v", err)
	}

	wl := utiltesting.MakeWorkload("a", "").Admit(&kueue.Admission{
		ClusterQueue: "one",
	}).Obj()
	if err := cache.AssumeWorkload(wl); err != nil {
		t.Fatalf("Failed assuming the workload to block the further admission: %v", err)
	}

	if cache.PodsReadyForAllAdmittedWorkloads(ctx) {
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
				wl := utiltesting.MakeWorkload("a", "").Admit(&kueue.Admission{
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
				wl := utiltesting.MakeWorkload("a", "").Admit(&kueue.Admission{
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
				wl := utiltesting.MakeWorkload("a", "").Admit(&kueue.Admission{
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
				wl := utiltesting.MakeWorkload("a", "").Admit(&kueue.Admission{
					ClusterQueue: "one",
				}).Obj()
				return cache.AssumeWorkload(wl)
			},
			wantReady: false,
		},
		{
			name: "assume Workload with PodsReady=False",
			operation: func(cache *Cache) error {
				wl := utiltesting.MakeWorkload("a", "").Admit(&kueue.Admission{
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
				wl := utiltesting.MakeWorkload("a", "").Admit(&kueue.Admission{
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
				wl := utiltesting.MakeWorkload("a", "").Admit(&kueue.Admission{
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
				wl := utiltesting.MakeWorkload("a", "").Admit(&kueue.Admission{
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
				wl1 := utiltesting.MakeWorkload("a", "").Admit(&kueue.Admission{
					ClusterQueue: "one",
				}).Condition(metav1.Condition{
					Type:   kueue.WorkloadPodsReady,
					Status: metav1.ConditionTrue,
				}).Obj()
				cache.AddOrUpdateWorkload(wl1)
				return nil
			},
			operation: func(cache *Cache) error {
				wl2 := utiltesting.MakeWorkload("b", "").Admit(&kueue.Admission{
					ClusterQueue: "two",
				}).Obj()
				return cache.AssumeWorkload(wl2)
			},
			wantReady: false,
		},
		{
			name: "update second workload to have PodsReady=True",
			setup: func(cache *Cache) error {
				wl1 := utiltesting.MakeWorkload("a", "").Admit(&kueue.Admission{
					ClusterQueue: "one",
				}).Condition(metav1.Condition{
					Type:   kueue.WorkloadPodsReady,
					Status: metav1.ConditionTrue,
				}).Obj()
				cache.AddOrUpdateWorkload(wl1)
				wl2 := utiltesting.MakeWorkload("b", "").Admit(&kueue.Admission{
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
				wl := utiltesting.MakeWorkload("a", "").Admit(&kueue.Admission{
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
				wl := utiltesting.MakeWorkload("a", "").Admit(&kueue.Admission{
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
			gotReady := cache.PodsReadyForAllAdmittedWorkloads(ctx)
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

func messageOrEmpty(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}
