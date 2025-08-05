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
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"

	"github.com/go-logr/logr"
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
	"sigs.k8s.io/controller-runtime/pkg/client/interceptor"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/hierarchy"
	"sigs.k8s.io/kueue/pkg/resources"
	"sigs.k8s.io/kueue/pkg/util/queue"
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
	setup := func(log logr.Logger, cache *Cache) error {
		cache.AddOrUpdateResourceFlavor(log,
			utiltesting.MakeResourceFlavor("default").
				NodeLabel("cpuType", "default").
				Obj())
		for _, c := range initialClusterQueues {
			if err := cache.AddClusterQueue(t.Context(), &c); err != nil {
				return fmt.Errorf("failed adding ClusterQueue: %w", err)
			}
		}
		return nil
	}
	cases := []struct {
		name                string
		operation           func(log logr.Logger, cache *Cache) error
		clientObjects       []client.Object
		wantClusterQueues   map[kueue.ClusterQueueReference]*clusterQueue
		wantCohorts         map[kueue.CohortReference]sets.Set[kueue.ClusterQueueReference]
		disableLendingLimit bool
	}{
		{
			name: "add",
			operation: func(log logr.Logger, cache *Cache) error {
				return setup(log, cache)
			},
			wantClusterQueues: map[kueue.ClusterQueueReference]*clusterQueue{
				"a": {
					Name:                          "a",
					AllocatableResourceGeneration: 2,
					NamespaceSelector:             labels.Nothing(),
					FlavorFungibility:             defaultFlavorFungibility,
					Status:                        active,
					Preemption:                    defaultPreemption,
					FairWeight:                    oneQuantity,
				},
				"b": {
					Name:                          "b",
					AllocatableResourceGeneration: 1,
					NamespaceSelector:             labels.Nothing(),
					FlavorFungibility:             defaultFlavorFungibility,
					Status:                        active,
					Preemption:                    defaultPreemption,
					FairWeight:                    oneQuantity,
				},
				"c": {
					Name:                          "c",
					AllocatableResourceGeneration: 3,
					NamespaceSelector:             labels.Nothing(),
					FlavorFungibility:             defaultFlavorFungibility,
					Status:                        active,
					Preemption:                    defaultPreemption,
					FairWeight:                    oneQuantity,
				},
				"d": {
					Name:                          "d",
					AllocatableResourceGeneration: 1,
					NamespaceSelector:             labels.Nothing(),
					FlavorFungibility:             defaultFlavorFungibility,
					Status:                        active,
					Preemption:                    defaultPreemption,
					FairWeight:                    oneQuantity,
				},
				"e": {
					Name:                          "e",
					AllocatableResourceGeneration: 2,
					NamespaceSelector:             labels.Nothing(),
					FlavorFungibility:             defaultFlavorFungibility,
					Status:                        pending,
					Preemption:                    defaultPreemption,
					FairWeight:                    oneQuantity,
				},
				"f": {
					Name:                          "f",
					AllocatableResourceGeneration: 1,
					NamespaceSelector:             labels.Nothing(),
					Status:                        active,
					Preemption:                    defaultPreemption,
					FlavorFungibility: kueue.FlavorFungibility{
						WhenCanBorrow:  kueue.TryNextFlavor,
						WhenCanPreempt: kueue.TryNextFlavor,
					},
					FairWeight: oneQuantity,
				},
			},
			wantCohorts: map[kueue.CohortReference]sets.Set[kueue.ClusterQueueReference]{
				"one": sets.New[kueue.ClusterQueueReference]("a", "b"),
				"two": sets.New[kueue.ClusterQueueReference]("c", "e", "f"),
			},
		},
		{
			name: "add ClusterQueue with preemption policies",
			operation: func(log logr.Logger, cache *Cache) error {
				cq := utiltesting.MakeClusterQueue("foo").Preemption(kueue.ClusterQueuePreemption{
					ReclaimWithinCohort: kueue.PreemptionPolicyLowerPriority,
					WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
				}).Obj()
				if err := cache.AddClusterQueue(t.Context(), cq); err != nil {
					return fmt.Errorf("failed to add ClusterQueue: %w", err)
				}
				return nil
			},
			wantClusterQueues: map[kueue.ClusterQueueReference]*clusterQueue{
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
			operation: func(log logr.Logger, cache *Cache) error {
				cq := utiltesting.MakeClusterQueue("foo").FairWeight(resource.MustParse("2")).Obj()
				if err := cache.AddClusterQueue(t.Context(), cq); err != nil {
					return fmt.Errorf("failed to add ClusterQueue: %w", err)
				}
				return nil
			},
			wantClusterQueues: map[kueue.ClusterQueueReference]*clusterQueue{
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
			operation: func(log logr.Logger, cache *Cache) error {
				for _, c := range initialClusterQueues {
					if err := cache.AddClusterQueue(t.Context(), &c); err != nil {
						return fmt.Errorf("failed adding ClusterQueue: %w", err)
					}
				}
				cache.AddOrUpdateResourceFlavor(log,
					utiltesting.MakeResourceFlavor("default").
						NodeLabel("cpuType", "default").
						Obj())
				return nil
			},
			wantClusterQueues: map[kueue.ClusterQueueReference]*clusterQueue{
				"a": {
					Name:                          "a",
					AllocatableResourceGeneration: 2,
					FlavorFungibility:             defaultFlavorFungibility,
					NamespaceSelector:             labels.Nothing(),
					Status:                        active,
					Preemption:                    defaultPreemption,
					FairWeight:                    oneQuantity,
				},
				"b": {
					Name:                          "b",
					AllocatableResourceGeneration: 1,
					FlavorFungibility:             defaultFlavorFungibility,
					NamespaceSelector:             labels.Nothing(),
					Status:                        active,
					Preemption:                    defaultPreemption,
					FairWeight:                    oneQuantity,
				},
				"c": {
					Name:                          "c",
					AllocatableResourceGeneration: 3,
					NamespaceSelector:             labels.Nothing(),
					FlavorFungibility:             defaultFlavorFungibility,
					Status:                        active,
					Preemption:                    defaultPreemption,
					FairWeight:                    oneQuantity,
				},
				"d": {
					Name:                          "d",
					AllocatableResourceGeneration: 1,
					NamespaceSelector:             labels.Nothing(),
					FlavorFungibility:             defaultFlavorFungibility,
					Status:                        active,
					Preemption:                    defaultPreemption,
					FairWeight:                    oneQuantity,
				},
				"e": {
					Name:                          "e",
					AllocatableResourceGeneration: 2,
					NamespaceSelector:             labels.Nothing(),
					FlavorFungibility:             defaultFlavorFungibility,
					Status:                        pending,
					Preemption:                    defaultPreemption,
					FairWeight:                    oneQuantity,
				},
				"f": {
					Name:                          "f",
					AllocatableResourceGeneration: 1,
					NamespaceSelector:             labels.Nothing(),
					Status:                        active,
					Preemption:                    defaultPreemption,
					FlavorFungibility: kueue.FlavorFungibility{
						WhenCanBorrow:  kueue.TryNextFlavor,
						WhenCanPreempt: kueue.TryNextFlavor,
					},
					FairWeight: oneQuantity,
				},
			},
			wantCohorts: map[kueue.CohortReference]sets.Set[kueue.ClusterQueueReference]{
				"one": sets.New[kueue.ClusterQueueReference]("a", "b"),
				"two": sets.New[kueue.ClusterQueueReference]("c", "e", "f"),
			},
		},
		{
			name: "update",
			operation: func(log logr.Logger, cache *Cache) error {
				err := setup(log, cache)
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
					if err := cache.UpdateClusterQueue(log, &c); err != nil {
						return fmt.Errorf("failed updating ClusterQueue: %w", err)
					}
				}
				cache.AddOrUpdateResourceFlavor(log,
					utiltesting.MakeResourceFlavor("default").
						NodeLabel("cpuType", "default").
						NodeLabel("region", "central").
						Obj())
				return nil
			},
			wantClusterQueues: map[kueue.ClusterQueueReference]*clusterQueue{
				"a": {
					Name:                          "a",
					AllocatableResourceGeneration: 4,
					NamespaceSelector:             labels.Nothing(),
					FlavorFungibility:             defaultFlavorFungibility,
					Status:                        active,
					Preemption:                    defaultPreemption,
					FairWeight:                    oneQuantity,
				},
				"b": {
					Name:                          "b",
					AllocatableResourceGeneration: 3,
					NamespaceSelector:             labels.Everything(),
					FlavorFungibility:             defaultFlavorFungibility,
					Status:                        active,
					Preemption:                    defaultPreemption,
					FairWeight:                    oneQuantity,
				},
				"c": {
					Name:                          "c",
					AllocatableResourceGeneration: 5,
					NamespaceSelector:             labels.Nothing(),
					FlavorFungibility:             defaultFlavorFungibility,
					Status:                        active,
					Preemption:                    defaultPreemption,
					FairWeight:                    oneQuantity,
				},
				"d": {
					Name:                          "d",
					AllocatableResourceGeneration: 1,
					NamespaceSelector:             labels.Nothing(),
					FlavorFungibility:             defaultFlavorFungibility,
					Status:                        active,
					Preemption:                    defaultPreemption,
					FairWeight:                    oneQuantity,
				},
				"e": {
					Name:                          "e",
					AllocatableResourceGeneration: 4,
					NamespaceSelector:             labels.Nothing(),
					FlavorFungibility:             defaultFlavorFungibility,
					Status:                        active,
					Preemption:                    defaultPreemption,
					FairWeight:                    oneQuantity,
				},
				"f": {
					Name:                          "f",
					AllocatableResourceGeneration: 3,
					NamespaceSelector:             labels.Nothing(),
					Status:                        active,
					Preemption:                    defaultPreemption,
					FlavorFungibility: kueue.FlavorFungibility{
						WhenCanBorrow:  kueue.TryNextFlavor,
						WhenCanPreempt: kueue.TryNextFlavor,
					},
					FairWeight: oneQuantity,
				},
			},
			wantCohorts: map[kueue.CohortReference]sets.Set[kueue.ClusterQueueReference]{
				"one": sets.New[kueue.ClusterQueueReference]("b"),
				"two": sets.New[kueue.ClusterQueueReference]("a", "c", "e", "f"),
			},
		},
		{
			name: "shouldn't delete usage resources on update ClusterQueue",
			operation: func(log logr.Logger, cache *Cache) error {
				cache.AddOrUpdateResourceFlavor(log,
					utiltesting.MakeResourceFlavor("default").
						NodeLabel("cpuType", "default").
						Obj())

				cq := utiltesting.MakeClusterQueue("a").
					ResourceGroup(
						*utiltesting.MakeFlavorQuotas("default").
							Resource(corev1.ResourceCPU, "10", "10").Obj()).
					Cohort("one").
					NamespaceSelector(nil).
					Obj()

				if err := cache.AddClusterQueue(t.Context(), cq); err != nil {
					return fmt.Errorf("failed adding ClusterQueue: %w", err)
				}

				wl := utiltesting.MakeWorkload("one", "").
					Request(corev1.ResourceCPU, "5").
					ReserveQuota(utiltesting.MakeAdmission("a").
						Assignment(corev1.ResourceCPU, "default", "5000m").
						Obj()).
					Condition(metav1.Condition{Type: kueue.WorkloadAdmitted, Status: metav1.ConditionTrue}).
					Obj()

				cache.AddOrUpdateWorkload(log, wl)

				cq = utiltesting.MakeClusterQueue("a").
					ResourceGroup(*utiltesting.MakeFlavorQuotas("default").Obj()).
					Cohort("one").
					NamespaceSelector(nil).
					Obj()

				if err := cache.UpdateClusterQueue(log, cq); err != nil {
					return fmt.Errorf("failed updating ClusterQueue: %w", err)
				}

				return nil
			},
			wantClusterQueues: map[kueue.ClusterQueueReference]*clusterQueue{
				"a": {
					Name:                          "a",
					AllocatableResourceGeneration: 2,
					NamespaceSelector:             labels.Nothing(),
					FlavorFungibility:             defaultFlavorFungibility,
					resourceNode: resourceNode{
						Usage: resources.FlavorResourceQuantities{
							{Flavor: "default", Resource: corev1.ResourceCPU}: 5000,
						},
					},
					AdmittedUsage: resources.FlavorResourceQuantities{
						{Flavor: "default", Resource: corev1.ResourceCPU}: 5000,
					},
					Status:     active,
					Preemption: defaultPreemption,
					FairWeight: oneQuantity,
					Workloads: map[workload.Reference]*workload.Info{
						"/one": {
							Obj: utiltesting.MakeWorkload("one", "").
								Request(corev1.ResourceCPU, "5").
								ReserveQuota(utiltesting.MakeAdmission("a").
									Assignment(corev1.ResourceCPU, "default", "5000m").
									Obj()).
								Condition(metav1.Condition{Type: kueue.WorkloadAdmitted, Status: metav1.ConditionTrue}).
								Obj(),
							TotalRequests: []workload.PodSetResources{
								{
									Name:     kueue.DefaultPodSetName,
									Requests: resources.Requests{corev1.ResourceCPU: 5000},
									Count:    1,
									Flavors:  map[corev1.ResourceName]kueue.ResourceFlavorReference{corev1.ResourceCPU: "default"},
								},
							},
							ClusterQueue: "a",
						},
					},
				},
			},
			wantCohorts: map[kueue.CohortReference]sets.Set[kueue.ClusterQueueReference]{
				"one": sets.New[kueue.ClusterQueueReference]("a"),
			},
		},
		{
			// Cohort one is deleted, as its members move
			// to another Cohort (or no Cohort). Cohort two
			// is deleted as all of its members are deleted.
			name: "implicit Cohorts created and deleted",
			operation: func(log logr.Logger, cache *Cache) error {
				_ = setup(log, cache)
				updateCqs := []kueue.ClusterQueue{
					*utiltesting.MakeClusterQueue("a").NamespaceSelector(nil).Cohort("three").Obj(),
					*utiltesting.MakeClusterQueue("b").NamespaceSelector(nil).Obj(),
					*utiltesting.MakeClusterQueue("c").NamespaceSelector(nil).Cohort("three").Obj(),
				}
				for _, c := range updateCqs {
					_ = cache.UpdateClusterQueue(log, &c)
				}
				deleteCqs := []kueue.ClusterQueue{
					*utiltesting.MakeClusterQueue("d").Cohort("two").Obj(),
					*utiltesting.MakeClusterQueue("e").Cohort("two").Obj(),
					*utiltesting.MakeClusterQueue("f").Cohort("two").Obj(),
				}
				for _, c := range deleteCqs {
					cache.DeleteClusterQueue(&c)
				}
				return nil
			},
			wantClusterQueues: map[kueue.ClusterQueueReference]*clusterQueue{
				"a": {
					Name:                          "a",
					AllocatableResourceGeneration: 4,
					FlavorFungibility:             defaultFlavorFungibility,
					Status:                        active,
					Preemption:                    defaultPreemption,
					FairWeight:                    oneQuantity,
					NamespaceSelector:             labels.Nothing(),
				},
				"b": {
					Name:                          "b",
					AllocatableResourceGeneration: 3,
					FlavorFungibility:             defaultFlavorFungibility,
					Status:                        active,
					Preemption:                    defaultPreemption,
					FairWeight:                    oneQuantity,
					NamespaceSelector:             labels.Nothing(),
				},
				"c": {
					Name:                          "c",
					AllocatableResourceGeneration: 4,
					FlavorFungibility:             defaultFlavorFungibility,
					Status:                        active,
					Preemption:                    defaultPreemption,
					FairWeight:                    oneQuantity,
					NamespaceSelector:             labels.Nothing(),
				},
			},
			wantCohorts: map[kueue.CohortReference]sets.Set[kueue.ClusterQueueReference]{
				"three": sets.New[kueue.ClusterQueueReference]("a", "c"),
			},
		},
		{
			name: "delete",
			operation: func(log logr.Logger, cache *Cache) error {
				err := setup(log, cache)
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
			wantClusterQueues: map[kueue.ClusterQueueReference]*clusterQueue{
				"b": {
					Name: "b",
					// AllocatableResourceGeneration is 2 because it was incremented:
					// 1. Once during initial setup when added to cohort "one"
					// 2. Once during deletion when cohort tree resources were recalculated after "a" was deleted
					AllocatableResourceGeneration: 2,
					NamespaceSelector:             labels.Nothing(),
					FlavorFungibility:             defaultFlavorFungibility,
					Status:                        active,
					Preemption:                    defaultPreemption,
					FairWeight:                    oneQuantity,
				},
				"c": {
					Name:                          "c",
					AllocatableResourceGeneration: 3,
					NamespaceSelector:             labels.Nothing(),
					FlavorFungibility:             defaultFlavorFungibility,
					Status:                        active,
					Preemption:                    defaultPreemption,
					FairWeight:                    oneQuantity,
				},
				"e": {
					Name:                          "e",
					AllocatableResourceGeneration: 2,
					NamespaceSelector:             labels.Nothing(),
					FlavorFungibility:             defaultFlavorFungibility,
					Status:                        pending,
					Preemption:                    defaultPreemption,
					FairWeight:                    oneQuantity,
				},
				"f": {
					Name:                          "f",
					AllocatableResourceGeneration: 1,
					NamespaceSelector:             labels.Nothing(),
					Status:                        active,
					Preemption:                    defaultPreemption,
					FlavorFungibility: kueue.FlavorFungibility{
						WhenCanBorrow:  kueue.TryNextFlavor,
						WhenCanPreempt: kueue.TryNextFlavor,
					},
					FairWeight: oneQuantity,
				},
			},
			wantCohorts: map[kueue.CohortReference]sets.Set[kueue.ClusterQueueReference]{
				"one": sets.New[kueue.ClusterQueueReference]("b"),
				"two": sets.New[kueue.ClusterQueueReference]("c", "e", "f"),
			},
		},
		{
			name: "add resource flavors",
			operation: func(log logr.Logger, cache *Cache) error {
				err := setup(log, cache)
				if err != nil {
					return err
				}
				cache.AddOrUpdateResourceFlavor(log, utiltesting.MakeResourceFlavor("nonexistent-flavor").Obj())
				return nil
			},
			wantClusterQueues: map[kueue.ClusterQueueReference]*clusterQueue{
				"a": {
					Name:                          "a",
					AllocatableResourceGeneration: 2,
					NamespaceSelector:             labels.Nothing(),
					FlavorFungibility:             defaultFlavorFungibility,
					Status:                        active,
					Preemption:                    defaultPreemption,
					FairWeight:                    oneQuantity,
				},
				"b": {
					Name:                          "b",
					AllocatableResourceGeneration: 1,
					NamespaceSelector:             labels.Nothing(),
					FlavorFungibility:             defaultFlavorFungibility,
					Status:                        active,
					Preemption:                    defaultPreemption,
					FairWeight:                    oneQuantity,
				},
				"c": {
					Name:                          "c",
					AllocatableResourceGeneration: 3,
					NamespaceSelector:             labels.Nothing(),
					FlavorFungibility:             defaultFlavorFungibility,
					Status:                        active,
					Preemption:                    defaultPreemption,
					FairWeight:                    oneQuantity,
				},
				"d": {
					Name:                          "d",
					AllocatableResourceGeneration: 1,
					NamespaceSelector:             labels.Nothing(),
					FlavorFungibility:             defaultFlavorFungibility,
					Status:                        active,
					Preemption:                    defaultPreemption,
					FairWeight:                    oneQuantity,
				},
				"e": {
					Name:                          "e",
					AllocatableResourceGeneration: 2,
					NamespaceSelector:             labels.Nothing(),
					FlavorFungibility:             defaultFlavorFungibility,
					Status:                        active,
					Preemption:                    defaultPreemption,
					FairWeight:                    oneQuantity,
				},
				"f": {
					Name:                          "f",
					AllocatableResourceGeneration: 1,
					NamespaceSelector:             labels.Nothing(),
					Status:                        active,
					Preemption:                    defaultPreemption,
					FlavorFungibility: kueue.FlavorFungibility{
						WhenCanBorrow:  kueue.TryNextFlavor,
						WhenCanPreempt: kueue.TryNextFlavor,
					},
					FairWeight: oneQuantity,
				},
			},
			wantCohorts: map[kueue.CohortReference]sets.Set[kueue.ClusterQueueReference]{
				"one": sets.New[kueue.ClusterQueueReference]("a", "b"),
				"two": sets.New[kueue.ClusterQueueReference]("c", "e", "f"),
			},
		},
		{
			name: "Add ClusterQueue with multiple resource groups",
			operation: func(log logr.Logger, cache *Cache) error {
				err := cache.AddClusterQueue(t.Context(),
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
					return fmt.Errorf("adding ClusterQueue: %w", err)
				}
				return nil
			},
			wantClusterQueues: map[kueue.ClusterQueueReference]*clusterQueue{
				"foo": {
					Name:                          "foo",
					NamespaceSelector:             labels.Everything(),
					AllocatableResourceGeneration: 1,
					FlavorFungibility:             defaultFlavorFungibility,
					Status:                        pending,
					Preemption:                    defaultPreemption,
					FairWeight:                    oneQuantity,
				},
			},
		},
		{
			name: "add cluster queue with missing check",
			operation: func(log logr.Logger, cache *Cache) error {
				err := cache.AddClusterQueue(t.Context(),
					utiltesting.MakeClusterQueue("foo").
						AdmissionChecks("check1", "check2").
						Obj())
				if err != nil {
					return fmt.Errorf("adding ClusterQueue: %w", err)
				}
				return nil
			},
			wantClusterQueues: map[kueue.ClusterQueueReference]*clusterQueue{
				"foo": {
					Name:                          "foo",
					NamespaceSelector:             labels.Everything(),
					Status:                        pending,
					Preemption:                    defaultPreemption,
					AllocatableResourceGeneration: 1,
					FlavorFungibility:             defaultFlavorFungibility,
					AdmissionChecks: map[kueue.AdmissionCheckReference]sets.Set[kueue.ResourceFlavorReference]{
						"check1": sets.New[kueue.ResourceFlavorReference](),
						"check2": sets.New[kueue.ResourceFlavorReference](),
					},
					FairWeight: oneQuantity,
				},
			},
			wantCohorts: map[kueue.CohortReference]sets.Set[kueue.ClusterQueueReference]{},
		},
		{
			name: "add check after queue creation",
			operation: func(log logr.Logger, cache *Cache) error {
				err := cache.AddClusterQueue(t.Context(),
					utiltesting.MakeClusterQueue("foo").
						AdmissionChecks("check1", "check2").
						Obj())
				if err != nil {
					return fmt.Errorf("adding ClusterQueue: %w", err)
				}

				cache.AddOrUpdateAdmissionCheck(log, utiltesting.MakeAdmissionCheck("check1").Active(metav1.ConditionTrue).Obj())
				cache.AddOrUpdateAdmissionCheck(log, utiltesting.MakeAdmissionCheck("check2").Active(metav1.ConditionTrue).Obj())
				return nil
			},
			wantClusterQueues: map[kueue.ClusterQueueReference]*clusterQueue{
				"foo": {
					Name:                          "foo",
					NamespaceSelector:             labels.Everything(),
					Status:                        active,
					Preemption:                    defaultPreemption,
					AllocatableResourceGeneration: 1,
					FlavorFungibility:             defaultFlavorFungibility,
					AdmissionChecks: map[kueue.AdmissionCheckReference]sets.Set[kueue.ResourceFlavorReference]{
						"check1": sets.New[kueue.ResourceFlavorReference](),
						"check2": sets.New[kueue.ResourceFlavorReference](),
					},
					FairWeight: oneQuantity,
				},
			},
			wantCohorts: map[kueue.CohortReference]sets.Set[kueue.ClusterQueueReference]{},
		},
		{
			name: "remove check after queue creation",
			operation: func(log logr.Logger, cache *Cache) error {
				cache.AddOrUpdateAdmissionCheck(log, utiltesting.MakeAdmissionCheck("check1").Active(metav1.ConditionTrue).Obj())
				cache.AddOrUpdateAdmissionCheck(log, utiltesting.MakeAdmissionCheck("check2").Active(metav1.ConditionTrue).Obj())
				err := cache.AddClusterQueue(t.Context(),
					utiltesting.MakeClusterQueue("foo").
						AdmissionChecks("check1", "check2").
						Obj())
				if err != nil {
					return fmt.Errorf("adding ClusterQueue: %w", err)
				}

				cache.DeleteAdmissionCheck(log, utiltesting.MakeAdmissionCheck("check2").Obj())
				return nil
			},
			wantClusterQueues: map[kueue.ClusterQueueReference]*clusterQueue{
				"foo": {
					Name:                          "foo",
					NamespaceSelector:             labels.Everything(),
					Status:                        pending,
					Preemption:                    defaultPreemption,
					AllocatableResourceGeneration: 1,
					FlavorFungibility:             defaultFlavorFungibility,
					AdmissionChecks: map[kueue.AdmissionCheckReference]sets.Set[kueue.ResourceFlavorReference]{
						"check1": sets.New[kueue.ResourceFlavorReference](),
						"check2": sets.New[kueue.ResourceFlavorReference](),
					},
					FairWeight: oneQuantity,
				},
			},
			wantCohorts: map[kueue.CohortReference]sets.Set[kueue.ClusterQueueReference]{},
		},
		{
			name: "inactivate check after queue creation",
			operation: func(log logr.Logger, cache *Cache) error {
				cache.AddOrUpdateAdmissionCheck(log, utiltesting.MakeAdmissionCheck("check1").Active(metav1.ConditionTrue).Obj())
				cache.AddOrUpdateAdmissionCheck(log, utiltesting.MakeAdmissionCheck("check2").Active(metav1.ConditionTrue).Obj())
				err := cache.AddClusterQueue(t.Context(),
					utiltesting.MakeClusterQueue("foo").
						AdmissionChecks("check1", "check2").
						Obj())
				if err != nil {
					return fmt.Errorf("adding ClusterQueue: %w", err)
				}

				cache.AddOrUpdateAdmissionCheck(log, utiltesting.MakeAdmissionCheck("check2").Active(metav1.ConditionFalse).Obj())
				return nil
			},
			wantClusterQueues: map[kueue.ClusterQueueReference]*clusterQueue{
				"foo": {
					Name:                          "foo",
					NamespaceSelector:             labels.Everything(),
					Status:                        pending,
					Preemption:                    defaultPreemption,
					AllocatableResourceGeneration: 1,
					FlavorFungibility:             defaultFlavorFungibility,
					AdmissionChecks: map[kueue.AdmissionCheckReference]sets.Set[kueue.ResourceFlavorReference]{
						"check1": sets.New[kueue.ResourceFlavorReference](),
						"check2": sets.New[kueue.ResourceFlavorReference](),
					},
					FairWeight: oneQuantity,
				},
			},
			wantCohorts: map[kueue.CohortReference]sets.Set[kueue.ClusterQueueReference]{},
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
			operation: func(log logr.Logger, cache *Cache) error {
				cache.AddOrUpdateResourceFlavor(log, utiltesting.MakeResourceFlavor("f1").Obj())
				err := cache.AddClusterQueue(t.Context(),
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
					return fmt.Errorf("adding ClusterQueue: %w", err)
				}
				return nil
			},
			wantClusterQueues: map[kueue.ClusterQueueReference]*clusterQueue{
				"cq1": {
					Name:                          "cq1",
					NamespaceSelector:             labels.Everything(),
					Status:                        active,
					Preemption:                    defaultPreemption,
					AllocatableResourceGeneration: 1,
					FlavorFungibility:             defaultFlavorFungibility,
					AdmittedUsage: resources.FlavorResourceQuantities{
						{Flavor: "f1", Resource: corev1.ResourceCPU}: 1000,
					},
					FairWeight: oneQuantity,
					resourceNode: resourceNode{
						Usage: resources.FlavorResourceQuantities{
							{Flavor: "f1", Resource: corev1.ResourceCPU}: 2000,
						},
					},
					Workloads: map[workload.Reference]*workload.Info{
						"ns/reserving": {
							ClusterQueue: "cq1",
							TotalRequests: []workload.PodSetResources{
								{
									Name:     kueue.DefaultPodSetName,
									Requests: resources.Requests{corev1.ResourceCPU: 1000},
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
									Name:     kueue.DefaultPodSetName,
									Requests: resources.Requests{corev1.ResourceCPU: 1000},
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
			wantCohorts: map[kueue.CohortReference]sets.Set[kueue.ClusterQueueReference]{},
		},
		{
			name: "add CQ with multiple resource groups and flavors",
			operation: func(log logr.Logger, cache *Cache) error {
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
				return cache.AddClusterQueue(t.Context(), cq)
			},
			wantClusterQueues: map[kueue.ClusterQueueReference]*clusterQueue{
				"foo": {
					Name:                          "foo",
					NamespaceSelector:             labels.Everything(),
					Status:                        pending,
					Preemption:                    defaultPreemption,
					AllocatableResourceGeneration: 1,
					FlavorFungibility:             defaultFlavorFungibility,
					FairWeight:                    oneQuantity,
				},
			},
		},
		{
			name:                "should not populate the fields with lendingLimit when feature disabled",
			disableLendingLimit: true,
			operation: func(log logr.Logger, cache *Cache) error {
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
				return cache.AddClusterQueue(t.Context(), cq)
			},
			wantClusterQueues: map[kueue.ClusterQueueReference]*clusterQueue{
				"foo": {
					Name:                          "foo",
					NamespaceSelector:             labels.Everything(),
					Status:                        pending,
					Preemption:                    defaultPreemption,
					AllocatableResourceGeneration: 1,
					FlavorFungibility:             defaultFlavorFungibility,
					FairWeight:                    oneQuantity,
				},
			},
		},
		{
			name: "create cohort",
			operation: func(log logr.Logger, cache *Cache) error {
				cohort := utiltesting.MakeCohort("cohort").Obj()
				return cache.AddOrUpdateCohort(cohort)
			},
			wantCohorts: map[kueue.CohortReference]sets.Set[kueue.ClusterQueueReference]{
				"cohort": nil,
			},
		},
		{
			name: "create and delete cohort",
			operation: func(log logr.Logger, cache *Cache) error {
				cohort := utiltesting.MakeCohort("cohort").Obj()
				_ = cache.AddOrUpdateCohort(cohort)
				cache.DeleteCohort(kueue.CohortReference(cohort.Name))
				return nil
			},
			wantCohorts: nil,
		},
		{
			name: "cohort remains after deletion when child exists",
			operation: func(log logr.Logger, cache *Cache) error {
				cohort := utiltesting.MakeCohort("cohort").Obj()
				_ = cache.AddOrUpdateCohort(cohort)

				_ = cache.AddClusterQueue(t.Context(),
					utiltesting.MakeClusterQueue("cq").Cohort("cohort").Obj())
				cache.DeleteCohort(kueue.CohortReference(cohort.Name))
				return nil
			},
			wantClusterQueues: map[kueue.ClusterQueueReference]*clusterQueue{
				"cq": {
					Name:                          "cq",
					NamespaceSelector:             labels.Everything(),
					Status:                        active,
					Preemption:                    defaultPreemption,
					AllocatableResourceGeneration: 2,
					FlavorFungibility:             defaultFlavorFungibility,
					FairWeight:                    oneQuantity,
				},
			},
			wantCohorts: map[kueue.CohortReference]sets.Set[kueue.ClusterQueueReference]{
				"cohort": sets.New[kueue.ClusterQueueReference]("cq"),
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, log := utiltesting.ContextWithLog(t)
			if tc.disableLendingLimit {
				features.SetFeatureGateDuringTest(t, features.LendingLimit, false)
			}
			cache := New(utiltesting.NewFakeClient(tc.clientObjects...))
			if err := tc.operation(log, cache); err != nil {
				t.Errorf("Unexpected error during test operation: %s", err)
			}
			if diff := cmp.Diff(tc.wantClusterQueues, cache.hm.ClusterQueues(),
				cmpopts.IgnoreFields(clusterQueue{}, "ResourceGroups"),
				cmpopts.IgnoreFields(workload.Info{}, "Obj", "LastAssignment"),
				cmpopts.IgnoreUnexported(clusterQueue{}, hierarchy.ClusterQueue[*cohort]{}),
				cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("Unexpected clusterQueues (-want,+got):\n%s", diff)
			}

			cohorts := cache.hm.Cohorts()
			gotCohorts := make(map[kueue.CohortReference]sets.Set[kueue.ClusterQueueReference], len(cohorts))
			for name, cohort := range cohorts {
				gotCohort := sets.New[kueue.ClusterQueueReference]()
				for _, cq := range cohort.ChildCQs() {
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
	psAssignments := []kueue.PodSetAssignment{
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
			PodSetAssignments: psAssignments,
		}).Obj(),
		utiltesting.MakeWorkload("b", "").ReserveQuota(&kueue.Admission{
			ClusterQueue: "one",
		}).Obj(),
		utiltesting.MakeWorkload("c", "").PodSets(podSets...).ReserveQuota(&kueue.Admission{
			ClusterQueue: "two",
		}).Obj())

	type result struct {
		Workloads     sets.Set[workload.Reference]
		UsedResources resources.FlavorResourceQuantities
	}

	steps := []struct {
		name                 string
		operation            func(log logr.Logger, cache *Cache) error
		wantResults          map[kueue.ClusterQueueReference]result
		wantAssumedWorkloads map[workload.Reference]kueue.ClusterQueueReference
		wantError            string
		wantLocalQueue       queue.LocalQueueReference
	}{
		{
			name: "add",
			operation: func(log logr.Logger, cache *Cache) error {
				workloads := []*kueue.Workload{
					utiltesting.MakeWorkload("a", "").PodSets(podSets...).ReserveQuota(&kueue.Admission{
						ClusterQueue:      "one",
						PodSetAssignments: psAssignments,
					}).Obj(),
					utiltesting.MakeWorkload("d", "").ReserveQuota(&kueue.Admission{
						ClusterQueue: "two",
					}).Obj(),
					utiltesting.MakeWorkload("pending", "").Obj(),
				}
				for i := range workloads {
					cache.AddOrUpdateWorkload(log, workloads[i])
				}
				return nil
			},
			wantResults: map[kueue.ClusterQueueReference]result{
				"one": {
					Workloads: sets.New[workload.Reference]("/a", "/b"),
					UsedResources: resources.FlavorResourceQuantities{
						{Flavor: "on-demand", Resource: corev1.ResourceCPU}: 10,
						{Flavor: "spot", Resource: corev1.ResourceCPU}:      15,
					},
				},
				"two": {
					Workloads: sets.New[workload.Reference]("/c", "/d"),
				},
			},
		},
		{
			name: "add error clusterQueue doesn't exist",
			operation: func(log logr.Logger, cache *Cache) error {
				w := utiltesting.MakeWorkload("d", "").ReserveQuota(&kueue.Admission{
					ClusterQueue: "three",
				}).Obj()
				if !cache.AddOrUpdateWorkload(log, w) {
					return errors.New("failed to add workload")
				}
				return nil
			},
			wantError: "failed to add workload",
			wantResults: map[kueue.ClusterQueueReference]result{
				"one": {
					Workloads: sets.New[workload.Reference]("/a", "/b"),
					UsedResources: resources.FlavorResourceQuantities{
						{Flavor: "on-demand", Resource: corev1.ResourceCPU}: 10,
						{Flavor: "spot", Resource: corev1.ResourceCPU}:      15,
					},
				},
				"two": {
					Workloads: sets.New[workload.Reference]("/c"),
				},
			},
		},
		{
			name: "add already exists",
			operation: func(log logr.Logger, cache *Cache) error {
				w := utiltesting.MakeWorkload("b", "").ReserveQuota(&kueue.Admission{
					ClusterQueue: "one",
				}).Obj()
				if !cache.AddOrUpdateWorkload(log, w) {
					return errors.New("failed to add workload")
				}
				return nil
			},
			wantResults: map[kueue.ClusterQueueReference]result{
				"one": {
					Workloads: sets.New[workload.Reference]("/a", "/b"),
					UsedResources: resources.FlavorResourceQuantities{
						{Flavor: "on-demand", Resource: corev1.ResourceCPU}: 10,
						{Flavor: "spot", Resource: corev1.ResourceCPU}:      15,
					},
				},
				"two": {
					Workloads: sets.New[workload.Reference]("/c"),
				},
			},
		},
		{
			name: "update cluster queue for a workload",
			operation: func(log logr.Logger, cache *Cache) error {
				old := utiltesting.MakeWorkload("a", "").ReserveQuota(&kueue.Admission{
					ClusterQueue: "one",
				}).Obj()
				latest := utiltesting.MakeWorkload("a", "").PodSets(podSets...).ReserveQuota(&kueue.Admission{
					ClusterQueue:      "two",
					PodSetAssignments: psAssignments,
				}).Obj()
				return cache.UpdateWorkload(log, old, latest)
			},
			wantResults: map[kueue.ClusterQueueReference]result{
				"one": {
					Workloads: sets.New[workload.Reference]("/b"),
					UsedResources: resources.FlavorResourceQuantities{
						{Flavor: "on-demand", Resource: corev1.ResourceCPU}: 0,
						{Flavor: "spot", Resource: corev1.ResourceCPU}:      0,
					},
				},
				"two": {
					Workloads: sets.New[workload.Reference]("/a", "/c"),
					UsedResources: resources.FlavorResourceQuantities{
						{Flavor: "on-demand", Resource: corev1.ResourceCPU}: 10,
						{Flavor: "spot", Resource: corev1.ResourceCPU}:      15,
					},
				},
			},
		},
		{
			name: "update error old clusterQueue doesn't exist",
			operation: func(log logr.Logger, cache *Cache) error {
				old := utiltesting.MakeWorkload("d", "").ReserveQuota(&kueue.Admission{
					ClusterQueue: "three",
				}).Obj()
				latest := utiltesting.MakeWorkload("d", "").ReserveQuota(&kueue.Admission{
					ClusterQueue: "one",
				}).Obj()
				return cache.UpdateWorkload(log, old, latest)
			},
			wantError: "old ClusterQueue doesn't exist",
			wantResults: map[kueue.ClusterQueueReference]result{
				"one": {
					Workloads: sets.New[workload.Reference]("/a", "/b"),
					UsedResources: resources.FlavorResourceQuantities{
						{Flavor: "on-demand", Resource: corev1.ResourceCPU}: 10,
						{Flavor: "spot", Resource: corev1.ResourceCPU}:      15,
					},
				},
				"two": {
					Workloads: sets.New[workload.Reference]("/c"),
				},
			},
		},
		{
			name: "update error new clusterQueue doesn't exist",
			operation: func(log logr.Logger, cache *Cache) error {
				old := utiltesting.MakeWorkload("d", "").ReserveQuota(&kueue.Admission{
					ClusterQueue: "one",
				}).Obj()
				latest := utiltesting.MakeWorkload("d", "").ReserveQuota(&kueue.Admission{
					ClusterQueue: "three",
				}).Obj()
				return cache.UpdateWorkload(log, old, latest)
			},
			wantError: "new ClusterQueue doesn't exist",
			wantResults: map[kueue.ClusterQueueReference]result{
				"one": {
					Workloads: sets.New[workload.Reference]("/a", "/b"),
					UsedResources: resources.FlavorResourceQuantities{
						{Flavor: "on-demand", Resource: corev1.ResourceCPU}: 10,
						{Flavor: "spot", Resource: corev1.ResourceCPU}:      15,
					},
				},
				"two": {
					Workloads: sets.New[workload.Reference]("/c"),
				},
			},
		},
		{
			name: "update workload which doesn't exist.",
			operation: func(log logr.Logger, cache *Cache) error {
				old := utiltesting.MakeWorkload("d", "").ReserveQuota(&kueue.Admission{
					ClusterQueue: "one",
				}).Obj()
				latest := utiltesting.MakeWorkload("d", "").ReserveQuota(&kueue.Admission{
					ClusterQueue: "two",
				}).Obj()
				return cache.UpdateWorkload(log, old, latest)
			},
			wantResults: map[kueue.ClusterQueueReference]result{
				"one": {
					Workloads: sets.New[workload.Reference]("/a", "/b"),
					UsedResources: resources.FlavorResourceQuantities{
						{Flavor: "on-demand", Resource: corev1.ResourceCPU}: 10,
						{Flavor: "spot", Resource: corev1.ResourceCPU}:      15,
					},
				},
				"two": {
					Workloads: sets.New[workload.Reference]("/c", "/d"),
				},
			},
		},
		{
			name: "delete",
			operation: func(log logr.Logger, cache *Cache) error {
				w := utiltesting.MakeWorkload("a", "").ReserveQuota(&kueue.Admission{
					ClusterQueue: "one",
				}).Obj()
				return cache.DeleteWorkload(log, w)
			},
			wantResults: map[kueue.ClusterQueueReference]result{
				"one": {
					Workloads: sets.New[workload.Reference]("/b"),
					UsedResources: resources.FlavorResourceQuantities{
						{Flavor: "on-demand", Resource: corev1.ResourceCPU}: 0,
						{Flavor: "spot", Resource: corev1.ResourceCPU}:      0,
					},
				},
				"two": {
					Workloads: sets.New[workload.Reference]("/c"),
				},
			},
		},
		{
			name: "delete workload with cancelled admission",
			operation: func(log logr.Logger, cache *Cache) error {
				w := utiltesting.MakeWorkload("a", "").Obj()
				return cache.DeleteWorkload(log, w)
			},
			wantResults: map[kueue.ClusterQueueReference]result{
				"one": {
					Workloads: sets.New[workload.Reference]("/b"),
					UsedResources: resources.FlavorResourceQuantities{
						{Flavor: "on-demand", Resource: corev1.ResourceCPU}: 0,
						{Flavor: "spot", Resource: corev1.ResourceCPU}:      0,
					},
				},
				"two": {
					Workloads: sets.New[workload.Reference]("/c"),
				},
			},
		},
		{
			name: "attempt deleting non-existing workload with cancelled admission",
			operation: func(log logr.Logger, cache *Cache) error {
				w := utiltesting.MakeWorkload("d", "").Obj()
				return cache.DeleteWorkload(log, w)
			},
			wantError: "cluster queue not found",
			wantResults: map[kueue.ClusterQueueReference]result{
				"one": {
					Workloads: sets.New[workload.Reference]("/a", "/b"),
					UsedResources: resources.FlavorResourceQuantities{
						{Flavor: "on-demand", Resource: corev1.ResourceCPU}: 10,
						{Flavor: "spot", Resource: corev1.ResourceCPU}:      15,
					},
				},
				"two": {
					Workloads: sets.New[workload.Reference]("/c"),
				},
			},
		},
		{
			name: "delete error clusterQueue doesn't exist",
			operation: func(log logr.Logger, cache *Cache) error {
				w := utiltesting.MakeWorkload("a", "").ReserveQuota(&kueue.Admission{
					ClusterQueue: "three",
				}).Obj()
				return cache.DeleteWorkload(log, w)
			},
			wantError: "cluster queue not found",
			wantResults: map[kueue.ClusterQueueReference]result{
				"one": {
					Workloads: sets.New[workload.Reference]("/a", "/b"),
					UsedResources: resources.FlavorResourceQuantities{
						{Flavor: "on-demand", Resource: corev1.ResourceCPU}: 10,
						{Flavor: "spot", Resource: corev1.ResourceCPU}:      15,
					},
				},
				"two": {
					Workloads: sets.New[workload.Reference]("/c"),
				},
			},
		},
		{
			name: "delete workload which doesn't exist",
			operation: func(log logr.Logger, cache *Cache) error {
				w := utiltesting.MakeWorkload("d", "").ReserveQuota(&kueue.Admission{
					ClusterQueue: "one",
				}).Obj()
				return cache.DeleteWorkload(log, w)
			},
			wantResults: map[kueue.ClusterQueueReference]result{
				"one": {
					Workloads: sets.New[workload.Reference]("/a", "/b"),
					UsedResources: resources.FlavorResourceQuantities{
						{Flavor: "on-demand", Resource: corev1.ResourceCPU}: 10,
						{Flavor: "spot", Resource: corev1.ResourceCPU}:      15,
					},
				},
				"two": {
					Workloads: sets.New[workload.Reference]("/c"),
				},
			},
		},
		{
			name: "assume",
			operation: func(log logr.Logger, cache *Cache) error {
				workloads := []*kueue.Workload{
					utiltesting.MakeWorkload("d", "").PodSets(podSets...).ReserveQuota(&kueue.Admission{
						ClusterQueue:      "one",
						PodSetAssignments: psAssignments,
					}).Obj(),
					utiltesting.MakeWorkload("e", "").PodSets(podSets...).ReserveQuota(&kueue.Admission{
						ClusterQueue:      "two",
						PodSetAssignments: psAssignments,
					}).Obj(),
				}
				for i := range workloads {
					if err := cache.AssumeWorkload(log, workloads[i]); err != nil {
						return err
					}
				}
				return nil
			},
			wantResults: map[kueue.ClusterQueueReference]result{
				"one": {
					Workloads: sets.New[workload.Reference]("/a", "/b", "/d"),
					UsedResources: resources.FlavorResourceQuantities{
						{Flavor: "on-demand", Resource: corev1.ResourceCPU}: 20,
						{Flavor: "spot", Resource: corev1.ResourceCPU}:      30,
					},
				},
				"two": {
					Workloads: sets.New[workload.Reference]("/c", "/e"),
					UsedResources: resources.FlavorResourceQuantities{
						{Flavor: "on-demand", Resource: corev1.ResourceCPU}: 10,
						{Flavor: "spot", Resource: corev1.ResourceCPU}:      15,
					},
				},
			},
			wantAssumedWorkloads: map[workload.Reference]kueue.ClusterQueueReference{
				"/d": "one",
				"/e": "two",
			},
		},
		{
			name: "assume error clusterQueue doesn't exist",
			operation: func(log logr.Logger, cache *Cache) error {
				w := utiltesting.MakeWorkload("d", "").PodSets(podSets...).ReserveQuota(&kueue.Admission{
					ClusterQueue: "three",
				}).Obj()
				if err := cache.AssumeWorkload(log, w); err != nil {
					return err
				}
				return nil
			},
			wantError: "cluster queue not found",
			wantResults: map[kueue.ClusterQueueReference]result{
				"one": {
					Workloads: sets.New[workload.Reference]("/a", "/b"),
					UsedResources: resources.FlavorResourceQuantities{
						{Flavor: "on-demand", Resource: corev1.ResourceCPU}: 10,
						{Flavor: "spot", Resource: corev1.ResourceCPU}:      15,
					},
				},
				"two": {
					Workloads: sets.New[workload.Reference]("/c"),
				},
			},
			wantAssumedWorkloads: map[workload.Reference]kueue.ClusterQueueReference{},
		},
		{
			name: "forget",
			operation: func(log logr.Logger, cache *Cache) error {
				workloads := []*kueue.Workload{
					utiltesting.MakeWorkload("d", "").PodSets(podSets...).ReserveQuota(&kueue.Admission{
						ClusterQueue:      "one",
						PodSetAssignments: psAssignments,
					}).Obj(),
					utiltesting.MakeWorkload("e", "").PodSets(podSets...).ReserveQuota(&kueue.Admission{
						ClusterQueue:      "two",
						PodSetAssignments: psAssignments,
					}).Obj(),
				}
				for i := range workloads {
					if err := cache.AssumeWorkload(log, workloads[i]); err != nil {
						return err
					}
				}

				w := workloads[0]
				return cache.ForgetWorkload(log, w)
			},
			wantResults: map[kueue.ClusterQueueReference]result{
				"one": {
					Workloads: sets.New[workload.Reference]("/a", "/b"),
					UsedResources: resources.FlavorResourceQuantities{
						{Flavor: "on-demand", Resource: corev1.ResourceCPU}: 10,
						{Flavor: "spot", Resource: corev1.ResourceCPU}:      15,
					},
				},
				"two": {
					Workloads: sets.New[workload.Reference]("/c", "/e"),
					UsedResources: resources.FlavorResourceQuantities{
						{Flavor: "on-demand", Resource: corev1.ResourceCPU}: 10,
						{Flavor: "spot", Resource: corev1.ResourceCPU}:      15,
					},
				},
			},
			wantAssumedWorkloads: map[workload.Reference]kueue.ClusterQueueReference{
				"/e": "two",
			},
		},
		{
			name: "forget error workload is not assumed",
			operation: func(log logr.Logger, cache *Cache) error {
				w := utiltesting.MakeWorkload("b", "").ReserveQuota(&kueue.Admission{
					ClusterQueue: "one",
				}).Obj()
				if err := cache.ForgetWorkload(log, w); err != nil {
					return err
				}
				return nil
			},
			wantError: "the workload is not assumed",
			wantResults: map[kueue.ClusterQueueReference]result{
				"one": {
					Workloads: sets.New[workload.Reference]("/a", "/b"),
					UsedResources: resources.FlavorResourceQuantities{
						{Flavor: "on-demand", Resource: corev1.ResourceCPU}: 10,
						{Flavor: "spot", Resource: corev1.ResourceCPU}:      15,
					},
				},
				"two": {
					Workloads: sets.New[workload.Reference]("/c"),
				},
			},
		},
		{
			name: "add assumed workload",
			operation: func(log logr.Logger, cache *Cache) error {
				workloads := []*kueue.Workload{
					utiltesting.MakeWorkload("d", "").PodSets(podSets...).ReserveQuota(&kueue.Admission{
						ClusterQueue:      "one",
						PodSetAssignments: psAssignments,
					}).Obj(),
					utiltesting.MakeWorkload("e", "").PodSets(podSets...).ReserveQuota(&kueue.Admission{
						ClusterQueue:      "two",
						PodSetAssignments: psAssignments,
					}).Obj(),
				}
				for i := range workloads {
					if err := cache.AssumeWorkload(log, workloads[i]); err != nil {
						return err
					}
				}

				w := workloads[0]
				if !cache.AddOrUpdateWorkload(log, w) {
					return errors.New("failed to add workload")
				}
				return nil
			},
			wantResults: map[kueue.ClusterQueueReference]result{
				"one": {
					Workloads: sets.New[workload.Reference]("/a", "/b", "/d"),
					UsedResources: resources.FlavorResourceQuantities{
						{Flavor: "on-demand", Resource: corev1.ResourceCPU}: 20,
						{Flavor: "spot", Resource: corev1.ResourceCPU}:      30,
					},
				},
				"two": {
					Workloads: sets.New[workload.Reference]("/c", "/e"),
					UsedResources: resources.FlavorResourceQuantities{
						{Flavor: "on-demand", Resource: corev1.ResourceCPU}: 10,
						{Flavor: "spot", Resource: corev1.ResourceCPU}:      15,
					},
				},
			},
			wantAssumedWorkloads: map[workload.Reference]kueue.ClusterQueueReference{
				"/e": "two",
			},
		},
	}
	for _, step := range steps {
		t.Run(step.name, func(t *testing.T) {
			cache := New(cl)
			_, log := utiltesting.ContextWithLog(t)

			for _, c := range clusterQueues {
				if err := cache.AddClusterQueue(t.Context(), &c); err != nil {
					t.Fatalf("Failed adding clusterQueue: %v", err)
				}
			}

			gotError := step.operation(log, cache)
			if diff := cmp.Diff(step.wantError, messageOrEmpty(gotError)); diff != "" {
				t.Errorf("Unexpected error (-want,+got):\n%s", diff)
			}
			gotResult := make(map[kueue.ClusterQueueReference]result)
			for name, cq := range cache.hm.ClusterQueues() {
				gotResult[name] = result{
					Workloads:     sets.KeySet(cq.Workloads),
					UsedResources: cq.resourceNode.Usage,
				}
			}
			if diff := cmp.Diff(step.wantResults, gotResult, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("Unexpected clusterQueues (-want,+got):\n%s", diff)
			}
			if step.wantAssumedWorkloads == nil {
				step.wantAssumedWorkloads = map[workload.Reference]kueue.ClusterQueueReference{}
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
		"clusterQueue with cohort; partial admission": {
			clusterQueue: cq,
			workloads: []kueue.Workload{
				*utiltesting.MakeWorkload("partial-one", "").
					PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 5).Request(corev1.ResourceCPU, "2").Obj()).
					ReserveQuota(utiltesting.MakeAdmission("foo").Assignment(corev1.ResourceCPU, "default", "4000m").AssignmentPodCount(2).Obj()).
					Admitted(true).
					Obj(),
				*utiltesting.MakeWorkload("partial-two", "").
					PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 5).Request(corev1.ResourceCPU, "2").Obj()).
					ReserveQuota(utiltesting.MakeAdmission("foo").Assignment(corev1.ResourceCPU, "default", "4000m").AssignmentPodCount(2).Obj()).
					Obj(),
			},
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
						Name: "example.com/gpu",
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
			wantReservingWorkloads: 2,
			wantUsedResources: []kueue.FlavorUsage{
				{
					Name: "default",
					Resources: []kueue.ResourceUsage{{
						Name:  corev1.ResourceCPU,
						Total: resource.MustParse("4"),
					}},
				},
				{
					Name: "model_a",
					Resources: []kueue.ResourceUsage{{
						Name: "example.com/gpu",
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
			ctx, log := utiltesting.ContextWithLog(t)
			err := cache.AddClusterQueue(ctx, tc.clusterQueue)
			if err != nil {
				t.Fatalf("Adding ClusterQueue: %v", err)
			}
			for i := range tc.workloads {
				w := &tc.workloads[i]
				if added := cache.AddOrUpdateWorkload(log, w); !added {
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
			ctx, log := utiltesting.ContextWithLog(t)
			if tc.cq != nil {
				if err := cache.AddClusterQueue(ctx, tc.cq); err != nil {
					t.Fatalf("Adding ClusterQueue: %v", err)
				}
			}
			if err := cache.AddLocalQueue(&localQueue); err != nil {
				t.Fatalf("Adding LocalQueue: %v", err)
			}
			for _, w := range tc.wls {
				if added := cache.AddOrUpdateWorkload(log, &w); !added && !tc.inAdmissibleWl.Has(w.Name) {
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

func TestGetCacheLQ(t *testing.T) {
	cq := utiltesting.MakeClusterQueue("cq").
		ResourceGroup(
			*utiltesting.MakeFlavorQuotas("spot").
				Resource("cpu", "10", "10").
				Resource("memory", "64Gi", "64Gi").Obj(),
		).ResourceGroup(
		*utiltesting.MakeFlavorQuotas("model-a").
			Resource("example.com/gpu", "10", "10").Obj(),
	).Obj()
	lq := utiltesting.MakeLocalQueue("lq-a", "ns").ClusterQueue("cq").Obj()
	cases := map[string]struct {
		getLq          *kueue.LocalQueue
		getCQReference kueue.ClusterQueueReference
		wantErr        error
		wantLq         *LocalQueue
	}{
		"valid LQ": {
			getLq:          lq,
			getCQReference: "cq",
			wantLq: &LocalQueue{
				key: "ns/lq-a",
			}},
		"LQ doesnt exist": {
			getLq:          utiltesting.MakeLocalQueue("non-existing-lq", "ns").ClusterQueue("cq").Obj(),
			getCQReference: "cq",
			wantErr:        errQNotFound,
		},
		"CQ doesnt exist": {
			getLq:          lq,
			getCQReference: "non-existing-cq",
			wantErr:        ErrCqNotFound,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			cl := utiltesting.NewFakeClient()
			cache := New(cl)
			ctx := t.Context()

			if err := cache.AddClusterQueue(ctx, cq); err != nil {
				t.Fatalf("Adding ClusterQueue: %v", err)
			}
			if err := cache.AddLocalQueue(lq); err != nil {
				t.Fatalf("Adding LocalQueue: %v", err)
			}

			gotLq, gotErr := cache.GetCacheLocalQueue(tc.getCQReference, tc.getLq)
			if diff := cmp.Diff(tc.wantLq, gotLq, cmp.AllowUnexported(LocalQueue{}), cmpopts.EquateEmpty(), cmpopts.IgnoreTypes(sync.RWMutex{})); diff != "" {
				t.Errorf("Unexpected localQueues (-want,+got):\n%s", diff)
			}
			if diff := cmp.Diff(tc.wantErr, gotErr, cmpopts.EquateErrors()); diff != "" {
				t.Fatalf("Unexpected error (-want/+got)\n%s", diff)
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
		log := ctrl.LoggerFrom(ctx)
		for _, wl := range workloads {
			wl := wl.DeepCopy()
			if err := cl.Create(ctx, wl); err != nil {
				return err
			}
			cache.AddOrUpdateWorkload(log, wl)
		}
		return nil
	}
	cases := map[string]struct {
		ops             []func(context.Context, client.Client, *Cache) error
		wantLocalQueues map[queue.LocalQueueReference]*LocalQueue
	}{
		"insert cqs, queues, workloads": {
			ops: []func(ctx context.Context, cl client.Client, cache *Cache) error{
				insertAllClusterQueues,
				insertAllQueues,
				insertAllWorkloads,
			},
			wantLocalQueues: map[queue.LocalQueueReference]*LocalQueue{
				"ns1/alpha": {
					key:                "ns1/alpha",
					reservingWorkloads: 1,
					admittedWorkloads:  1,
					totalReserved: resources.FlavorResourceQuantities{
						{Flavor: "spot", Resource: corev1.ResourceCPU}:    resources.ResourceValue(corev1.ResourceCPU, resource.MustParse("2")),
						{Flavor: "spot", Resource: corev1.ResourceMemory}: resources.ResourceValue(corev1.ResourceMemory, resource.MustParse("8Gi")),
					},
					admittedUsage: resources.FlavorResourceQuantities{
						{Flavor: "spot", Resource: corev1.ResourceCPU}:    resources.ResourceValue(corev1.ResourceCPU, resource.MustParse("2")),
						{Flavor: "spot", Resource: corev1.ResourceMemory}: resources.ResourceValue(corev1.ResourceMemory, resource.MustParse("8Gi")),
					},
				},
				"ns2/beta": {
					key:                "ns2/beta",
					reservingWorkloads: 2,
					admittedWorkloads:  1,
					totalReserved: resources.FlavorResourceQuantities{
						{Flavor: "model-a", Resource: "example.com/gpu"}: resources.ResourceValue("example.com/gpu", resource.MustParse("7")),
					},
					admittedUsage: resources.FlavorResourceQuantities{
						{Flavor: "model-a", Resource: "example.com/gpu"}: resources.ResourceValue("example.com/gpu", resource.MustParse("2")),
					},
				},
				"ns1/gamma": {
					key:                "ns1/gamma",
					reservingWorkloads: 1,
					admittedWorkloads:  0,
					totalReserved: resources.FlavorResourceQuantities{
						{Flavor: "ondemand", Resource: corev1.ResourceCPU}:    resources.ResourceValue(corev1.ResourceCPU, resource.MustParse("5")),
						{Flavor: "ondemand", Resource: corev1.ResourceMemory}: resources.ResourceValue(corev1.ResourceMemory, resource.MustParse("16Gi")),
					},
				},
			},
		},
		"insert cqs, workloads but no queues": {
			ops: []func(context.Context, client.Client, *Cache) error{
				insertAllClusterQueues,
				insertAllWorkloads,
			},
			wantLocalQueues: map[queue.LocalQueueReference]*LocalQueue{},
		},
		"insert queues, workloads but no cqs": {
			ops: []func(context.Context, client.Client, *Cache) error{
				insertAllQueues,
				insertAllWorkloads,
			},
			wantLocalQueues: map[queue.LocalQueueReference]*LocalQueue{},
		},
		"insert queues last": {
			ops: []func(context.Context, client.Client, *Cache) error{
				insertAllClusterQueues,
				insertAllWorkloads,
				insertAllQueues,
			},
			wantLocalQueues: map[queue.LocalQueueReference]*LocalQueue{
				"ns1/alpha": {
					key:                "ns1/alpha",
					reservingWorkloads: 1,
					admittedWorkloads:  1,
					totalReserved: resources.FlavorResourceQuantities{
						{Flavor: "spot", Resource: corev1.ResourceCPU}:    resources.ResourceValue(corev1.ResourceCPU, resource.MustParse("2")),
						{Flavor: "spot", Resource: corev1.ResourceMemory}: resources.ResourceValue(corev1.ResourceMemory, resource.MustParse("8Gi")),
						{Flavor: "model-a", Resource: "example.com/gpu"}:  resources.ResourceValue("example.com/gpu", resource.MustParse("0")),
					},
					admittedUsage: resources.FlavorResourceQuantities{
						{Flavor: "spot", Resource: corev1.ResourceCPU}:    resources.ResourceValue(corev1.ResourceCPU, resource.MustParse("2")),
						{Flavor: "spot", Resource: corev1.ResourceMemory}: resources.ResourceValue(corev1.ResourceMemory, resource.MustParse("8Gi")),
						{Flavor: "model-a", Resource: "example.com/gpu"}:  resources.ResourceValue("example.com/gpu", resource.MustParse("0")),
					},
				},
				"ns2/beta": {
					key:                "ns2/beta",
					reservingWorkloads: 2,
					admittedWorkloads:  1,
					totalReserved: resources.FlavorResourceQuantities{
						{Flavor: "spot", Resource: corev1.ResourceCPU}:    resources.ResourceValue(corev1.ResourceCPU, resource.MustParse("0")),
						{Flavor: "spot", Resource: corev1.ResourceMemory}: resources.ResourceValue(corev1.ResourceMemory, resource.MustParse("0")),
						{Flavor: "model-a", Resource: "example.com/gpu"}:  resources.ResourceValue("example.com/gpu", resource.MustParse("7")),
					},
					admittedUsage: resources.FlavorResourceQuantities{
						{Flavor: "spot", Resource: corev1.ResourceCPU}:    resources.ResourceValue(corev1.ResourceCPU, resource.MustParse("0")),
						{Flavor: "spot", Resource: corev1.ResourceMemory}: resources.ResourceValue(corev1.ResourceMemory, resource.MustParse("0")),
						{Flavor: "model-a", Resource: "example.com/gpu"}:  resources.ResourceValue("example.com/gpu", resource.MustParse("2")),
					},
				},
				"ns1/gamma": {
					key:                "ns1/gamma",
					reservingWorkloads: 1,
					admittedWorkloads:  0,
					totalReserved: resources.FlavorResourceQuantities{
						{Flavor: "ondemand", Resource: corev1.ResourceCPU}:    resources.ResourceValue(corev1.ResourceCPU, resource.MustParse("5")),
						{Flavor: "ondemand", Resource: corev1.ResourceMemory}: resources.ResourceValue(corev1.ResourceMemory, resource.MustParse("16Gi")),
					},
				},
			},
		},
		"insert cqs last": {
			ops: []func(context.Context, client.Client, *Cache) error{
				insertAllQueues,
				insertAllWorkloads,
				insertAllClusterQueues,
			},
			wantLocalQueues: map[queue.LocalQueueReference]*LocalQueue{
				"ns1/alpha": {
					key:                "ns1/alpha",
					reservingWorkloads: 1,
					admittedWorkloads:  1,
					totalReserved: resources.FlavorResourceQuantities{
						{Flavor: "spot", Resource: corev1.ResourceCPU}:    resources.ResourceValue(corev1.ResourceCPU, resource.MustParse("2")),
						{Flavor: "spot", Resource: corev1.ResourceMemory}: resources.ResourceValue(corev1.ResourceMemory, resource.MustParse("8Gi")),
					},
					admittedUsage: resources.FlavorResourceQuantities{
						{Flavor: "spot", Resource: corev1.ResourceCPU}:    resources.ResourceValue(corev1.ResourceCPU, resource.MustParse("2")),
						{Flavor: "spot", Resource: corev1.ResourceMemory}: resources.ResourceValue(corev1.ResourceMemory, resource.MustParse("8Gi")),
					},
				},
				"ns2/beta": {
					key:                "ns2/beta",
					reservingWorkloads: 2,
					admittedWorkloads:  1,
					totalReserved: resources.FlavorResourceQuantities{
						{Flavor: "model-a", Resource: "example.com/gpu"}: resources.ResourceValue("example.com/gpu", resource.MustParse("7")),
					},
					admittedUsage: resources.FlavorResourceQuantities{
						{Flavor: "model-a", Resource: "example.com/gpu"}: resources.ResourceValue("example.com/gpu", resource.MustParse("2")),
					},
				},
				"ns1/gamma": {
					key:                "ns1/gamma",
					reservingWorkloads: 1,
					admittedWorkloads:  0,
					totalReserved: resources.FlavorResourceQuantities{
						{Flavor: "ondemand", Resource: corev1.ResourceCPU}:    resources.ResourceValue(corev1.ResourceCPU, resource.MustParse("5")),
						{Flavor: "ondemand", Resource: corev1.ResourceMemory}: resources.ResourceValue(corev1.ResourceMemory, resource.MustParse("16Gi")),
					},
				},
			},
		},
		"assume": {
			ops: []func(context.Context, client.Client, *Cache) error{
				insertAllClusterQueues,
				insertAllQueues,
				func(ctx context.Context, cl client.Client, cache *Cache) error {
					log := ctrl.LoggerFrom(ctx)
					wl := workloads[0].DeepCopy()
					if err := cl.Create(ctx, wl); err != nil {
						return err
					}
					return cache.AssumeWorkload(log, wl)
				},
			},
			wantLocalQueues: map[queue.LocalQueueReference]*LocalQueue{
				"ns1/alpha": {
					key:                "ns1/alpha",
					reservingWorkloads: 1,
					admittedWorkloads:  1,
					totalReserved: resources.FlavorResourceQuantities{
						{Flavor: "spot", Resource: corev1.ResourceCPU}:    resources.ResourceValue(corev1.ResourceCPU, resource.MustParse("2")),
						{Flavor: "spot", Resource: corev1.ResourceMemory}: resources.ResourceValue(corev1.ResourceMemory, resource.MustParse("8Gi")),
					},
					admittedUsage: resources.FlavorResourceQuantities{
						{Flavor: "spot", Resource: corev1.ResourceCPU}:    resources.ResourceValue(corev1.ResourceCPU, resource.MustParse("2")),
						{Flavor: "spot", Resource: corev1.ResourceMemory}: resources.ResourceValue(corev1.ResourceMemory, resource.MustParse("8Gi")),
					},
				},
				"ns2/beta": {
					key:                "ns2/beta",
					reservingWorkloads: 0,
					admittedWorkloads:  0,
				},
				"ns1/gamma": {
					key:                "ns1/gamma",
					reservingWorkloads: 0,
					admittedWorkloads:  0,
				},
			},
		},
		"assume and forget": {
			ops: []func(context.Context, client.Client, *Cache) error{
				insertAllClusterQueues,
				insertAllQueues,
				func(ctx context.Context, cl client.Client, cache *Cache) error {
					log := ctrl.LoggerFrom(ctx)
					wl := workloads[0].DeepCopy()
					if err := cl.Create(ctx, wl); err != nil {
						return err
					}
					if err := cache.AssumeWorkload(log, wl); err != nil {
						return err
					}
					return cache.ForgetWorkload(log, wl)
				},
			},
			wantLocalQueues: map[queue.LocalQueueReference]*LocalQueue{
				"ns1/alpha": {
					key:                "ns1/alpha",
					reservingWorkloads: 0,
					admittedWorkloads:  0,
					totalReserved: resources.FlavorResourceQuantities{
						{Flavor: "spot", Resource: corev1.ResourceCPU}:    resources.ResourceValue(corev1.ResourceCPU, resource.MustParse("0")),
						{Flavor: "spot", Resource: corev1.ResourceMemory}: resources.ResourceValue(corev1.ResourceMemory, resource.MustParse("0")),
					},
					admittedUsage: resources.FlavorResourceQuantities{
						{Flavor: "spot", Resource: corev1.ResourceCPU}:    resources.ResourceValue(corev1.ResourceCPU, resource.MustParse("0")),
						{Flavor: "spot", Resource: corev1.ResourceMemory}: resources.ResourceValue(corev1.ResourceMemory, resource.MustParse("0")),
					},
				},
				"ns2/beta": {
					key:                "ns2/beta",
					reservingWorkloads: 0,
					admittedWorkloads:  0,
				},
				"ns1/gamma": {
					key:                "ns1/gamma",
					reservingWorkloads: 0,
					admittedWorkloads:  0,
				},
			},
		},
		"delete workload": {
			ops: []func(ctx context.Context, cl client.Client, cache *Cache) error{
				insertAllClusterQueues,
				insertAllQueues,
				insertAllWorkloads,
				func(ctx context.Context, cl client.Client, cache *Cache) error {
					log := ctrl.LoggerFrom(ctx)
					return cache.DeleteWorkload(log, workloads[0])
				},
			},
			wantLocalQueues: map[queue.LocalQueueReference]*LocalQueue{
				"ns1/alpha": {
					key:                "ns1/alpha",
					reservingWorkloads: 0,
					admittedWorkloads:  0,
					totalReserved: resources.FlavorResourceQuantities{
						{Flavor: "spot", Resource: corev1.ResourceCPU}:    resources.ResourceValue(corev1.ResourceCPU, resource.MustParse("0")),
						{Flavor: "spot", Resource: corev1.ResourceMemory}: resources.ResourceValue(corev1.ResourceMemory, resource.MustParse("0")),
					},
					admittedUsage: resources.FlavorResourceQuantities{
						{Flavor: "spot", Resource: corev1.ResourceCPU}:    resources.ResourceValue(corev1.ResourceCPU, resource.MustParse("0")),
						{Flavor: "spot", Resource: corev1.ResourceMemory}: resources.ResourceValue(corev1.ResourceMemory, resource.MustParse("0")),
					},
				},
				"ns2/beta": {
					key:                "ns2/beta",
					reservingWorkloads: 2,
					admittedWorkloads:  1,
					totalReserved: resources.FlavorResourceQuantities{
						{Flavor: "model-a", Resource: "example.com/gpu"}: resources.ResourceValue("example.com/gpu", resource.MustParse("7")),
					},
					admittedUsage: resources.FlavorResourceQuantities{
						{Flavor: "model-a", Resource: "example.com/gpu"}: resources.ResourceValue("example.com/gpu", resource.MustParse("2")),
					},
				},
				"ns1/gamma": {
					key:                "ns1/gamma",
					reservingWorkloads: 1,
					admittedWorkloads:  0,
					totalReserved: resources.FlavorResourceQuantities{
						{Flavor: "ondemand", Resource: corev1.ResourceCPU}:    resources.ResourceValue(corev1.ResourceCPU, resource.MustParse("5")),
						{Flavor: "ondemand", Resource: corev1.ResourceMemory}: resources.ResourceValue(corev1.ResourceMemory, resource.MustParse("16Gi")),
					},
				},
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
			wantLocalQueues: map[queue.LocalQueueReference]*LocalQueue{
				"ns1/gamma": {
					key:                "ns1/gamma",
					reservingWorkloads: 1,
					admittedWorkloads:  0,
					totalReserved: resources.FlavorResourceQuantities{
						{Flavor: "ondemand", Resource: corev1.ResourceCPU}:    resources.ResourceValue(corev1.ResourceCPU, resource.MustParse("5")),
						{Flavor: "ondemand", Resource: corev1.ResourceMemory}: resources.ResourceValue(corev1.ResourceMemory, resource.MustParse("16Gi")),
					},
				},
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
			wantLocalQueues: map[queue.LocalQueueReference]*LocalQueue{
				"ns2/beta": {
					key:                "ns2/beta",
					reservingWorkloads: 2,
					admittedWorkloads:  1,
					totalReserved: resources.FlavorResourceQuantities{
						{Flavor: "model-a", Resource: "example.com/gpu"}: resources.ResourceValue("example.com/gpu", resource.MustParse("7")),
					},
					admittedUsage: resources.FlavorResourceQuantities{
						{Flavor: "model-a", Resource: "example.com/gpu"}: resources.ResourceValue("example.com/gpu", resource.MustParse("2")),
					},
				},
				"ns1/gamma": {
					key:                "ns1/gamma",
					reservingWorkloads: 1,
					admittedWorkloads:  0,
					totalReserved: resources.FlavorResourceQuantities{
						{Flavor: "ondemand", Resource: corev1.ResourceCPU}:    resources.ResourceValue(corev1.ResourceCPU, resource.MustParse("5")),
						{Flavor: "ondemand", Resource: corev1.ResourceMemory}: resources.ResourceValue(corev1.ResourceMemory, resource.MustParse("16Gi")),
					},
				},
			},
		},
		// Not tested: changing a workload's queue and changing a queue's cluster queue.
		// These operations should not be allowed by the webhook.
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			cl := utiltesting.NewFakeClient()
			cache := New(cl)
			ctx := t.Context()
			for i, op := range tc.ops {
				if err := op(ctx, cl, cache); err != nil {
					t.Fatalf("Running op %d: %v", i, err)
				}
			}
			cacheQueues := make(map[queue.LocalQueueReference]*LocalQueue)
			for _, cacheCQ := range cache.hm.ClusterQueues() {
				for qKey, cacheQ := range cacheCQ.localQueues {
					if _, ok := cacheQueues[qKey]; ok {
						t.Fatalf("The cache have a duplicated localQueue %q across multiple clusterQueues", qKey)
					}
					cacheQueues[qKey] = cacheQ
				}
			}
			if diff := cmp.Diff(tc.wantLocalQueues, cacheQueues, cmp.AllowUnexported(LocalQueue{}), cmpopts.EquateEmpty(), cmpopts.IgnoreTypes(sync.RWMutex{})); diff != "" {
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
		wantInUseClusterQueueNames []kueue.ClusterQueueReference
	}{
		{
			name: "single clusterQueue with flavor in use",
			clusterQueues: []*kueue.ClusterQueue{
				fooCq,
			},
			wantInUseClusterQueueNames: []kueue.ClusterQueueReference{kueue.ClusterQueueReference(fooCq.Name)},
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
			wantInUseClusterQueueNames: []kueue.ClusterQueueReference{
				kueue.ClusterQueueReference(fooCq.Name),
				kueue.ClusterQueueReference(fizzCq.Name),
			},
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			_, log := utiltesting.ContextWithLog(t)
			cache := New(utiltesting.NewFakeClient())
			cache.AddOrUpdateResourceFlavor(log, x86Rf)
			cache.AddOrUpdateResourceFlavor(log, aarch64Rf)

			for _, cq := range tc.clusterQueues {
				if err := cache.AddClusterQueue(t.Context(), cq); err != nil {
					t.Errorf("failed to add clusterQueue %s", cq.Name)
				}
			}

			cqs := cache.ClusterQueuesUsingFlavor("x86")
			if diff := cmp.Diff(tc.wantInUseClusterQueueNames, cqs, cmpopts.SortSlices(func(a, b kueue.ClusterQueueReference) bool {
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
	wantCQs := sets.New[kueue.ClusterQueueReference]("matching1", "matching2")

	cache := New(utiltesting.NewFakeClient())
	for _, cq := range clusterQueues {
		if err := cache.AddClusterQueue(t.Context(), cq); err != nil {
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
	ctx, cancel := context.WithCancel(t.Context())
	defer cancel()
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
	if err := cache.AssumeWorkload(log, wl); err != nil {
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
		setup     func(log logr.Logger, cache *Cache) error
		operation func(log logr.Logger, cache *Cache) error
		wantReady bool
	}{
		{
			name:      "empty cache",
			operation: func(log logr.Logger, cache *Cache) error { return nil },
			wantReady: true,
		},
		{
			name: "add Workload without PodsReady condition",
			operation: func(log logr.Logger, cache *Cache) error {
				wl := utiltesting.MakeWorkload("a", "").ReserveQuota(&kueue.Admission{
					ClusterQueue: "one",
				}).Obj()
				cache.AddOrUpdateWorkload(log, wl)
				return nil
			},
			wantReady: false,
		},
		{
			name: "add Workload with PodsReady=False",
			operation: func(log logr.Logger, cache *Cache) error {
				wl := utiltesting.MakeWorkload("a", "").ReserveQuota(&kueue.Admission{
					ClusterQueue: "one",
				}).Condition(metav1.Condition{
					Type:   kueue.WorkloadPodsReady,
					Status: metav1.ConditionFalse,
				}).Obj()
				cache.AddOrUpdateWorkload(log, wl)
				return nil
			},
			wantReady: false,
		},
		{
			name: "add Workload with PodsReady=True",
			operation: func(log logr.Logger, cache *Cache) error {
				wl := utiltesting.MakeWorkload("a", "").ReserveQuota(&kueue.Admission{
					ClusterQueue: "one",
				}).Condition(metav1.Condition{
					Type:   kueue.WorkloadPodsReady,
					Status: metav1.ConditionTrue,
				}).Obj()
				cache.AddOrUpdateWorkload(log, wl)
				return nil
			},
			wantReady: true,
		},
		{
			name: "assume Workload without PodsReady condition",
			operation: func(log logr.Logger, cache *Cache) error {
				wl := utiltesting.MakeWorkload("a", "").ReserveQuota(&kueue.Admission{
					ClusterQueue: "one",
				}).Obj()
				return cache.AssumeWorkload(log, wl)
			},
			wantReady: false,
		},
		{
			name: "assume Workload with PodsReady=False",
			operation: func(log logr.Logger, cache *Cache) error {
				wl := utiltesting.MakeWorkload("a", "").ReserveQuota(&kueue.Admission{
					ClusterQueue: "one",
				}).Condition(metav1.Condition{
					Type:   kueue.WorkloadPodsReady,
					Status: metav1.ConditionFalse,
				}).Obj()
				return cache.AssumeWorkload(log, wl)
			},
			wantReady: false,
		},
		{
			name: "assume Workload with PodsReady=True",
			operation: func(log logr.Logger, cache *Cache) error {
				wl := utiltesting.MakeWorkload("a", "").ReserveQuota(&kueue.Admission{
					ClusterQueue: "one",
				}).Condition(metav1.Condition{
					Type:   kueue.WorkloadPodsReady,
					Status: metav1.ConditionTrue,
				}).Obj()
				return cache.AssumeWorkload(log, wl)
			},
			wantReady: true,
		},
		{
			name: "update workload to have PodsReady=True",
			setup: func(log logr.Logger, cache *Cache) error {
				wl := utiltesting.MakeWorkload("a", "").ReserveQuota(&kueue.Admission{
					ClusterQueue: "one",
				}).Obj()
				cache.AddOrUpdateWorkload(log, wl)
				return nil
			},
			operation: func(log logr.Logger, cache *Cache) error {
				wl := cache.hm.ClusterQueue("one").Workloads["/a"].Obj
				newWl := wl.DeepCopy()
				apimeta.SetStatusCondition(&newWl.Status.Conditions, metav1.Condition{
					Type:   kueue.WorkloadPodsReady,
					Status: metav1.ConditionTrue,
				})
				return cache.UpdateWorkload(log, wl, newWl)
			},
			wantReady: true,
		},
		{
			name: "update workload to have PodsReady=False",
			setup: func(log logr.Logger, cache *Cache) error {
				wl := utiltesting.MakeWorkload("a", "").ReserveQuota(&kueue.Admission{
					ClusterQueue: "one",
				}).Condition(metav1.Condition{
					Type:   kueue.WorkloadPodsReady,
					Status: metav1.ConditionTrue,
				}).Obj()
				cache.AddOrUpdateWorkload(log, wl)
				return nil
			},
			operation: func(log logr.Logger, cache *Cache) error {
				wl := cache.hm.ClusterQueue("one").Workloads["/a"].Obj
				newWl := wl.DeepCopy()
				apimeta.SetStatusCondition(&newWl.Status.Conditions, metav1.Condition{
					Type:   kueue.WorkloadPodsReady,
					Status: metav1.ConditionFalse,
				})
				return cache.UpdateWorkload(log, wl, newWl)
			},
			wantReady: false,
		},
		{
			name: "assume second workload without PodsReady",
			setup: func(log logr.Logger, cache *Cache) error {
				wl1 := utiltesting.MakeWorkload("a", "").ReserveQuota(&kueue.Admission{
					ClusterQueue: "one",
				}).Condition(metav1.Condition{
					Type:   kueue.WorkloadPodsReady,
					Status: metav1.ConditionTrue,
				}).Obj()
				cache.AddOrUpdateWorkload(log, wl1)
				return nil
			},
			operation: func(log logr.Logger, cache *Cache) error {
				wl2 := utiltesting.MakeWorkload("b", "").ReserveQuota(&kueue.Admission{
					ClusterQueue: "two",
				}).Obj()
				return cache.AssumeWorkload(log, wl2)
			},
			wantReady: false,
		},
		{
			name: "update second workload to have PodsReady=True",
			setup: func(log logr.Logger, cache *Cache) error {
				wl1 := utiltesting.MakeWorkload("a", "").ReserveQuota(&kueue.Admission{
					ClusterQueue: "one",
				}).Condition(metav1.Condition{
					Type:   kueue.WorkloadPodsReady,
					Status: metav1.ConditionTrue,
				}).Obj()
				cache.AddOrUpdateWorkload(log, wl1)
				wl2 := utiltesting.MakeWorkload("b", "").ReserveQuota(&kueue.Admission{
					ClusterQueue: "two",
				}).Obj()
				cache.AddOrUpdateWorkload(log, wl2)
				return nil
			},
			operation: func(log logr.Logger, cache *Cache) error {
				wl2 := cache.hm.ClusterQueue("two").Workloads["/b"].Obj
				newWl2 := wl2.DeepCopy()
				apimeta.SetStatusCondition(&newWl2.Status.Conditions, metav1.Condition{
					Type:   kueue.WorkloadPodsReady,
					Status: metav1.ConditionTrue,
				})
				return cache.UpdateWorkload(log, wl2, newWl2)
			},
			wantReady: true,
		},
		{
			name: "delete workload with PodsReady=False",
			setup: func(log logr.Logger, cache *Cache) error {
				wl := utiltesting.MakeWorkload("a", "").ReserveQuota(&kueue.Admission{
					ClusterQueue: "one",
				}).Condition(metav1.Condition{
					Type:   kueue.WorkloadPodsReady,
					Status: metav1.ConditionFalse,
				}).Obj()
				cache.AddOrUpdateWorkload(log, wl)
				return nil
			},
			operation: func(log logr.Logger, cache *Cache) error {
				wl := cache.hm.ClusterQueue("one").Workloads["/a"].Obj
				return cache.DeleteWorkload(log, wl)
			},
			wantReady: true,
		},
		{
			name: "forget workload with PodsReady=False",
			setup: func(log logr.Logger, cache *Cache) error {
				wl := utiltesting.MakeWorkload("a", "").ReserveQuota(&kueue.Admission{
					ClusterQueue: "one",
				}).Condition(metav1.Condition{
					Type:   kueue.WorkloadPodsReady,
					Status: metav1.ConditionFalse,
				}).Obj()
				return cache.AssumeWorkload(log, wl)
			},
			operation: func(log logr.Logger, cache *Cache) error {
				wl := cache.hm.ClusterQueue("one").Workloads["/a"].Obj
				return cache.ForgetWorkload(log, wl)
			},
			wantReady: true,
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			cache := New(cl, WithPodsReadyTracking(true))
			ctx, log := utiltesting.ContextWithLog(t)

			for _, c := range clusterQueues {
				if err := cache.AddClusterQueue(ctx, &c); err != nil {
					t.Fatalf("Failed adding clusterQueue: %v", err)
				}
			}
			if tc.setup != nil {
				if err := tc.setup(log, cache); err != nil {
					t.Errorf("Unexpected error during setup: %q", err)
				}
			}
			if err := tc.operation(log, cache); err != nil {
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
		name             string
		clusterQueues    map[string]*clusterQueue
		assumedWorkloads map[workload.Reference]kueue.ClusterQueueReference
		workload         workload.Info
		expected         bool
	}{
		{
			name:             "Workload Is Assumed and not Admitted",
			assumedWorkloads: map[workload.Reference]kueue.ClusterQueueReference{"workload_namespace/workload_name": "test", "test2": "test2"},
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
		},
		{
			name: "Workload Is not Assumed but is Admitted",
			clusterQueues: map[string]*clusterQueue{
				"ClusterQueue1": {
					Name: "ClusterQueue1",
					Workloads: map[workload.Reference]*workload.Info{"workload_namespace/workload_name": {
						Obj: &kueue.Workload{
							ObjectMeta: metav1.ObjectMeta{
								Name:      "workload_name",
								Namespace: "workload_namespace",
							},
						},
					}},
				}},
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
		},
		{
			name: "Workload Is Assumed and Admitted",

			clusterQueues: map[string]*clusterQueue{
				"ClusterQueue1": {
					Name: "ClusterQueue1",
					Workloads: map[workload.Reference]*workload.Info{"workload_namespace/workload_name": {
						Obj: &kueue.Workload{
							ObjectMeta: metav1.ObjectMeta{
								Name:      "workload_name",
								Namespace: "workload_namespace",
							},
						},
					}},
				}},
			assumedWorkloads: map[workload.Reference]kueue.ClusterQueueReference{"workload_namespace/workload_name": "test", "test2": "test2"},
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
		},
		{
			name: "Workload Is not Assumed and is not Admitted",
			clusterQueues: map[string]*clusterQueue{
				"ClusterQueue1": {
					Name: "ClusterQueue1",
					Workloads: map[workload.Reference]*workload.Info{"workload_namespace2/workload_name2": {
						Obj: &kueue.Workload{
							ObjectMeta: metav1.ObjectMeta{
								Name:      "workload_name2",
								Namespace: "workload_namespace2",
							},
						},
					}},
				}},

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
			cache := New(utiltesting.NewFakeClient())
			for _, cq := range tc.clusterQueues {
				cache.hm.AddClusterQueue(cq)
			}
			cache.assumedWorkloads = tc.assumedWorkloads
			if cache.IsAssumedOrAdmittedWorkload(tc.workload) != tc.expected {
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
		wantInUseClusterQueueNames []kueue.ClusterQueueReference
		check                      kueue.AdmissionCheckReference
	}{
		"single clusterQueue with check in use": {
			clusterQueues:              []*kueue.ClusterQueue{fooCq},
			wantInUseClusterQueueNames: []kueue.ClusterQueueReference{kueue.ClusterQueueReference(fooCq.Name)},
			check:                      "ac1",
		},
		"single clusterQueue with AdmissionCheckStrategy in use": {
			clusterQueues:              []*kueue.ClusterQueue{strategyCq},
			wantInUseClusterQueueNames: []kueue.ClusterQueueReference{kueue.ClusterQueueReference(strategyCq.Name)},
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
			wantInUseClusterQueueNames: []kueue.ClusterQueueReference{
				kueue.ClusterQueueReference(fooCq.Name),
				kueue.ClusterQueueReference(fizzCq.Name),
				kueue.ClusterQueueReference(strategyCq.Name),
			},
			check: "ac1",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, log := utiltesting.ContextWithLog(t)
			cache := New(utiltesting.NewFakeClient())
			for _, check := range checks {
				cache.AddOrUpdateAdmissionCheck(log, check)
			}

			for _, cq := range tc.clusterQueues {
				if err := cache.AddClusterQueue(t.Context(), cq); err != nil {
					t.Errorf("failed to add clusterQueue %s", cq.Name)
				}
			}

			cqs := cache.ClusterQueuesUsingAdmissionCheck(tc.check)
			if diff := cmp.Diff(tc.wantInUseClusterQueueNames, cqs, cmpopts.SortSlices(func(a, b kueue.ClusterQueueReference) bool {
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
		AdmissionChecks(kueue.AdmissionCheckReference(baseCheck.Name)).
		Obj()

	cases := map[string]struct {
		clusterQueues    []*kueue.ClusterQueue
		resourceFlavors  []*kueue.ResourceFlavor
		admissionChecks  []*kueue.AdmissionCheck
		clusterQueueName kueue.ClusterQueueReference
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
			wantMessage:      "Can't admit new workloads: references missing ResourceFlavor(s): flavor1.",
		},
		"check not found": {
			clusterQueues:    []*kueue.ClusterQueue{baseQueue},
			resourceFlavors:  []*kueue.ResourceFlavor{baseFlavor},
			clusterQueueName: "queue1",
			wantStatus:       metav1.ConditionFalse,
			wantReason:       "AdmissionCheckNotFound",
			wantMessage:      "Can't admit new workloads: references missing AdmissionCheck(s): check1.",
		},
		"check inactive": {
			clusterQueues:    []*kueue.ClusterQueue{baseQueue},
			resourceFlavors:  []*kueue.ResourceFlavor{baseFlavor},
			admissionChecks:  []*kueue.AdmissionCheck{utiltesting.MakeAdmissionCheck("check1").Obj()},
			clusterQueueName: "queue1",
			wantStatus:       metav1.ConditionFalse,
			wantReason:       "AdmissionCheckInactive",
			wantMessage:      "Can't admit new workloads: references inactive AdmissionCheck(s): check1.",
		},
		"flavor and check not found": {
			clusterQueues:    []*kueue.ClusterQueue{baseQueue},
			clusterQueueName: "queue1",
			wantStatus:       metav1.ConditionFalse,
			wantReason:       "FlavorNotFound",
			wantMessage:      "Can't admit new workloads: references missing ResourceFlavor(s): flavor1, references missing AdmissionCheck(s): check1.",
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
			wantMessage:      "Can't admit new workloads: is stopped.",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			_, log := utiltesting.ContextWithLog(t)
			cache := New(utiltesting.NewFakeClient())
			for _, rf := range tc.resourceFlavors {
				cache.AddOrUpdateResourceFlavor(log, rf)
			}
			for _, ac := range tc.admissionChecks {
				cache.AddOrUpdateAdmissionCheck(log, ac)
			}
			for _, cq := range tc.clusterQueues {
				if err := cache.AddClusterQueue(t.Context(), cq); err != nil {
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

func TestCohortCycles(t *testing.T) {
	t.Run("self cycle", func(t *testing.T) {
		cache := New(utiltesting.NewFakeClient())
		cohort := utiltesting.MakeCohort("cohort").Parent("cohort").Obj()
		if err := cache.AddOrUpdateCohort(cohort); err == nil {
			t.Fatal("Expected failure when cycle")
		}
	})
	t.Run("simple cycle", func(t *testing.T) {
		cache := New(utiltesting.NewFakeClient())
		cohortA := utiltesting.MakeCohort("cohort-a").Parent("cohort-b").Obj()
		cohortB := utiltesting.MakeCohort("cohort-b").Parent("cohort-c").Obj()
		cohortC := utiltesting.MakeCohort("cohort-c").Parent("cohort-a").Obj()
		if err := cache.AddOrUpdateCohort(cohortA); err != nil {
			t.Fatal("Expected success as no cycle yet")
		}
		if err := cache.AddOrUpdateCohort(cohortB); err != nil {
			t.Fatal("Expected success as no cycle yet")
		}
		if err := cache.AddOrUpdateCohort(cohortC); err == nil {
			t.Fatal("Expected failure when cycle")
		}
	})
	t.Run("clusterqueue add and update return error when cohort has cycle", func(t *testing.T) {
		cache := New(utiltesting.NewFakeClient())
		ctx, log := utiltesting.ContextWithLog(t)
		cohortA := utiltesting.MakeCohort("cohort-a").Parent("cohort-b").Obj()
		if err := cache.AddOrUpdateCohort(cohortA); err != nil {
			t.Fatal("Expected success as no cycle yet")
		}
		cohortB := utiltesting.MakeCohort("cohort-b").Parent("cohort-c").Obj()
		if err := cache.AddOrUpdateCohort(cohortB); err != nil {
			t.Fatal("Expected success as no cycle yet")
		}
		cohortC := utiltesting.MakeCohort("cohort-c").Parent("cohort-a").Obj()
		if err := cache.AddOrUpdateCohort(cohortC); err == nil {
			t.Fatal("Expected failure when cycle")
		}

		// Error when creating CQ with parent Cohort-A
		cq := utiltesting.MakeClusterQueue("cq").Cohort("cohort-a").Obj()
		if err := cache.AddClusterQueue(ctx, cq); err == nil {
			t.Fatal("Expected failure when adding cq to cohort with cycle")
		}

		// Error when updating CQ with parent Cohort-B
		cq = utiltesting.MakeClusterQueue("cq").Cohort("cohort-b").Obj()
		if err := cache.UpdateClusterQueue(log, cq); err == nil {
			t.Fatal("Expected failure when updating cq to cohort with cycle")
		}

		// Delete Cohort C, breaking cycle
		cache.DeleteCohort("cohort-c")

		// Update succeeds
		cq = utiltesting.MakeClusterQueue("cq").Cohort("cohort-b").Obj()
		if err := cache.UpdateClusterQueue(log, cq); err != nil {
			t.Fatal("Expected success")
		}
	})

	t.Run("clusterqueue leaving cohort with cycle successfully updates new cohort", func(t *testing.T) {
		cache := New(utiltesting.NewFakeClient())
		ctx, log := utiltesting.ContextWithLog(t)
		cycleCohort := utiltesting.MakeCohort("cycle").Parent("cycle").Obj()
		if err := cache.AddOrUpdateCohort(cycleCohort); err == nil {
			t.Fatal("Expected failure")
		}

		cohort := utiltesting.MakeCohort("cohort").
			ResourceGroup(
				*utiltesting.MakeFlavorQuotas("arm").Resource(corev1.ResourceCPU, "10").Obj(),
			).Obj()
		if err := cache.AddOrUpdateCohort(cohort); err != nil {
			t.Fatal("Expected success")
		}

		// Error when creating cq with parent that has cycle
		cq := utiltesting.MakeClusterQueue("cq").
			ResourceGroup(
				*utiltesting.MakeFlavorQuotas("arm").Resource(corev1.ResourceCPU, "5").Obj(),
			).Cohort("cycle").Obj()
		if err := cache.AddClusterQueue(ctx, cq); err == nil {
			t.Fatal("Expected failure")
		}

		// Successfully updated to cohort without cycle
		cq.Spec.Cohort = "cohort"
		if err := cache.UpdateClusterQueue(log, cq); err != nil {
			t.Fatal("Expected success")
		}

		// cohort's SubtreeQuota contains resources from cq.
		gotResource := cache.hm.Cohort("cohort").getResourceNode()
		wantResource := resourceNode{
			Quotas: map[resources.FlavorResource]ResourceQuota{
				{Flavor: "arm", Resource: corev1.ResourceCPU}: {Nominal: 10_000},
			},
			SubtreeQuota: resources.FlavorResourceQuantities{
				{Flavor: "arm", Resource: corev1.ResourceCPU}: 15_000,
			},
			Usage: resources.FlavorResourceQuantities{},
		}
		if diff := cmp.Diff(wantResource, gotResource); diff != "" {
			t.Errorf("Unexpected resource (-want,+got):\n%s", diff)
		}
	})

	t.Run("clusterqueue joining cohort with cycle successfully updates old cohort", func(t *testing.T) {
		cache := New(utiltesting.NewFakeClient())
		ctx, log := utiltesting.ContextWithLog(t)
		cycleCohort := utiltesting.MakeCohort("cycle").Parent("cycle").Obj()
		if err := cache.AddOrUpdateCohort(cycleCohort); err == nil {
			t.Fatal("Expected failure")
		}
		cohort := utiltesting.MakeCohort("cohort").
			ResourceGroup(*utiltesting.MakeFlavorQuotas("arm").Resource(corev1.ResourceCPU, "10").Obj()).Obj()
		if err := cache.AddOrUpdateCohort(cohort); err != nil {
			t.Fatal("Expected success")
		}

		// Add CQ to cohort
		cq := utiltesting.MakeClusterQueue("cq").
			ResourceGroup(*utiltesting.MakeFlavorQuotas("arm").Resource(corev1.ResourceCPU, "5").Obj()).Cohort("cohort").Obj()
		if err := cache.AddClusterQueue(ctx, cq); err != nil {
			t.Fatal("Expected success")
		}

		// cohort's SubtreeQuota contains resources from cq
		gotResource := cache.hm.Cohort("cohort").getResourceNode()
		wantResource := resourceNode{
			Quotas: map[resources.FlavorResource]ResourceQuota{
				{Flavor: "arm", Resource: corev1.ResourceCPU}: {Nominal: 10_000},
			},
			SubtreeQuota: resources.FlavorResourceQuantities{
				{Flavor: "arm", Resource: corev1.ResourceCPU}: 15_000,
			},
			Usage: resources.FlavorResourceQuantities{},
		}
		if diff := cmp.Diff(wantResource, gotResource); diff != "" {
			t.Errorf("Unexpected resource (-want,+got):\n%s", diff)
		}

		// Updated to cycle
		cq.Spec.Cohort = "cycle"
		if err := cache.UpdateClusterQueue(log, cq); err == nil {
			t.Fatal("Expected failure")
		}

		// Cohort's SubtreeQuota no longer contains resources from CQ.
		gotResource = cache.hm.Cohort("cohort").getResourceNode()
		wantResource = resourceNode{
			Quotas: map[resources.FlavorResource]ResourceQuota{
				{Flavor: "arm", Resource: corev1.ResourceCPU}: {Nominal: 10_000},
			},
			SubtreeQuota: resources.FlavorResourceQuantities{
				{Flavor: "arm", Resource: corev1.ResourceCPU}: 10_000,
			},
			Usage: resources.FlavorResourceQuantities{},
		}
		if diff := cmp.Diff(wantResource, gotResource); diff != "" {
			t.Errorf("Unexpected resource (-want,+got):\n%s", diff)
		}
	})

	t.Run("cohort switching cohorts updates both cohort trees", func(t *testing.T) {
		cache := New(utiltesting.NewFakeClient())
		root1 := utiltesting.MakeCohort("root1").Obj()
		root2 := utiltesting.MakeCohort("root2").Obj()
		if err := cache.AddOrUpdateCohort(root1); err != nil {
			t.Fatal("Expected success")
		}
		if err := cache.AddOrUpdateCohort(root2); err != nil {
			t.Fatal("Expected success")
		}

		cohort := utiltesting.MakeCohort("cohort").Parent("root1").
			ResourceGroup(*utiltesting.MakeFlavorQuotas("arm").Resource(corev1.ResourceCPU, "10").Obj()).Obj()
		if err := cache.AddOrUpdateCohort(cohort); err != nil {
			t.Fatal("Expected success")
		}

		// before move
		{
			wantRoot1 := resourceNode{
				Quotas: map[resources.FlavorResource]ResourceQuota{},
				SubtreeQuota: resources.FlavorResourceQuantities{
					{Flavor: "arm", Resource: corev1.ResourceCPU}: 10_000,
				},
				Usage: resources.FlavorResourceQuantities{},
			}
			wantRoot2 := resourceNode{
				Quotas:       map[resources.FlavorResource]ResourceQuota{},
				SubtreeQuota: resources.FlavorResourceQuantities{},
				Usage:        resources.FlavorResourceQuantities{},
			}
			if diff := cmp.Diff(wantRoot1, cache.hm.Cohort("root1").getResourceNode()); diff != "" {
				t.Errorf("Unexpected resource (-want,+got):\n%s", diff)
			}
			if diff := cmp.Diff(wantRoot2, cache.hm.Cohort("root2").getResourceNode()); diff != "" {
				t.Errorf("Unexpected resource (-want,+got):\n%s", diff)
			}
		}
		cohort.Spec.ParentName = "root2"
		if err := cache.AddOrUpdateCohort(cohort); err != nil {
			t.Fatal("Expected success")
		}
		// after move
		{
			wantRoot1 := resourceNode{
				Quotas:       map[resources.FlavorResource]ResourceQuota{},
				SubtreeQuota: resources.FlavorResourceQuantities{},
				Usage:        resources.FlavorResourceQuantities{},
			}
			wantRoot2 := resourceNode{
				Quotas: map[resources.FlavorResource]ResourceQuota{},
				SubtreeQuota: resources.FlavorResourceQuantities{
					{Flavor: "arm", Resource: corev1.ResourceCPU}: 10_000,
				},
				Usage: resources.FlavorResourceQuantities{},
			}
			if diff := cmp.Diff(wantRoot1, cache.hm.Cohort("root1").getResourceNode()); diff != "" {
				t.Errorf("Unexpected resource (-want,+got):\n%s", diff)
			}
			if diff := cmp.Diff(wantRoot2, cache.hm.Cohort("root2").getResourceNode()); diff != "" {
				t.Errorf("Unexpected resource (-want,+got):\n%s", diff)
			}
		}
	})

	t.Run("cohort leaving cohort with cycle successfully updates new cohort", func(t *testing.T) {
		cache := New(utiltesting.NewFakeClient())
		cycleRoot := utiltesting.MakeCohort("cycle-root").Parent("cycle-root").Obj()
		if err := cache.AddOrUpdateCohort(cycleRoot); err == nil {
			t.Fatal("Expected failure")
		}
		root := utiltesting.MakeCohort("root").Obj()
		if err := cache.AddOrUpdateCohort(root); err != nil {
			t.Fatal("Expected success")
		}

		cohort := utiltesting.MakeCohort("cohort").Parent("cycle-root").
			ResourceGroup(
				*utiltesting.MakeFlavorQuotas("arm").Resource(corev1.ResourceCPU, "10").Obj(),
			).Obj()
		if err := cache.AddOrUpdateCohort(cohort); err == nil {
			t.Fatal("Expected failure")
		}

		cohort.Spec.ParentName = "root"
		if err := cache.AddOrUpdateCohort(cohort); err != nil {
			t.Fatal("Expected success")
		}
		wantRoot := resourceNode{
			Quotas: map[resources.FlavorResource]ResourceQuota{},
			SubtreeQuota: resources.FlavorResourceQuantities{
				{Flavor: "arm", Resource: corev1.ResourceCPU}: 10_000,
			},
			Usage: resources.FlavorResourceQuantities{},
		}
		if diff := cmp.Diff(wantRoot, cache.hm.Cohort("root").getResourceNode()); diff != "" {
			t.Errorf("Unexpected resource (-want,+got):\n%s", diff)
		}
	})

	t.Run("cohort joining cohort with cycle successfully updates old cohort", func(t *testing.T) {
		cache := New(utiltesting.NewFakeClient())
		cycleRoot := utiltesting.MakeCohort("cycle-root").Parent("cycle-root").Obj()
		if err := cache.AddOrUpdateCohort(cycleRoot); err == nil {
			t.Fatal("Expected failure")
		}
		root := utiltesting.MakeCohort("root").Obj()
		if err := cache.AddOrUpdateCohort(root); err != nil {
			t.Fatal("Expected success")
		}

		cohort := utiltesting.MakeCohort("cohort").Parent("root").
			ResourceGroup(
				*utiltesting.MakeFlavorQuotas("arm").Resource(corev1.ResourceCPU, "10").Obj(),
			).Obj()
		if err := cache.AddOrUpdateCohort(cohort); err != nil {
			t.Fatal("Expected success")
		}

		// before move
		{
			wantRoot := resourceNode{
				Quotas: map[resources.FlavorResource]ResourceQuota{},
				SubtreeQuota: resources.FlavorResourceQuantities{
					{Flavor: "arm", Resource: corev1.ResourceCPU}: 10_000,
				},
				Usage: resources.FlavorResourceQuantities{},
			}
			if diff := cmp.Diff(wantRoot, cache.hm.Cohort("root").getResourceNode()); diff != "" {
				t.Errorf("Unexpected resource (-want,+got):\n%s", diff)
			}
		}

		cohort.Spec.ParentName = "cycle-root"
		if err := cache.AddOrUpdateCohort(cohort); err == nil {
			t.Fatal("Expected failure")
		}

		// after move
		{
			wantRoot := resourceNode{
				Quotas:       map[resources.FlavorResource]ResourceQuota{},
				SubtreeQuota: resources.FlavorResourceQuantities{},
				Usage:        resources.FlavorResourceQuantities{},
			}
			if diff := cmp.Diff(wantRoot, cache.hm.Cohort("root").getResourceNode()); diff != "" {
				t.Errorf("Unexpected resource (-want,+got):\n%s", diff)
			}
		}
	})
}

// TestSnapshotError tests the negative scenario when an error is returned while
// using TopologyAwareScheduling
func TestSnapshotError(t *testing.T) {
	var (
		connectionRefusedErr = errors.New("connection refused")
	)
	features.SetFeatureGateDuringTest(t, features.TopologyAwareScheduling, true)
	ctx, log := utiltesting.ContextWithLog(t)

	topology := *utiltesting.MakeDefaultOneLevelTopology("default")
	flavor := *utiltesting.MakeResourceFlavor("tas-default").
		TopologyName("default").
		Obj()
	localQueue := *utiltesting.MakeLocalQueue("lq", "default").ClusterQueue("cq").Obj()
	clusterQueue := utiltesting.MakeClusterQueue("cq").
		ResourceGroup(
			*utiltesting.MakeFlavorQuotas("tas-default").
				ResourceQuotaWrapper("cpu").NominalQuota("8").Append().
				Obj(),
		).ClusterQueue

	clientBuilder := utiltesting.NewClientBuilder()
	clientBuilder.WithObjects(&topology)
	clientBuilder.WithObjects(&localQueue)
	clientBuilder.WithObjects(&clusterQueue)
	kcBuilder := clientBuilder.
		WithInterceptorFuncs(interceptor.Funcs{
			List: func(ctx context.Context, client client.WithWatch, list client.ObjectList, opts ...client.ListOption) error {
				_, isNodeList := list.(*corev1.NodeList)
				if isNodeList {
					if connectionRefusedErr != nil {
						return connectionRefusedErr
					}
				}
				return client.List(ctx, list, opts...)
			},
		})
	kcBuilder.WithObjects(&flavor)
	client := kcBuilder.Build()
	cache := New(client)
	cache.AddOrUpdateResourceFlavor(log, &flavor)
	if flavor.Spec.TopologyName != nil {
		cache.AddOrUpdateTopology(log, &kueuealpha.Topology{
			ObjectMeta: metav1.ObjectMeta{
				Name: string(*flavor.Spec.TopologyName),
			},
			Spec: kueuealpha.TopologySpec{
				Levels: []kueuealpha.TopologyLevel{
					{
						NodeLabel: corev1.LabelHostname,
					},
				},
			},
		})
	}
	if err := cache.AddClusterQueue(ctx, &clusterQueue); err != nil {
		t.Fatalf("failed to add CQ: %v", err)
	}
	_, gotErr := cache.Snapshot(ctx)
	if diff := cmp.Diff(connectionRefusedErr, gotErr, cmpopts.EquateErrors()); diff != "" {
		t.Fatalf("Unexpected error (-want/+got)\n%s", diff)
	}
}

func TestClusterQueueAncestors(t *testing.T) {
	testCases := map[string]struct {
		cohorts       []*kueue.Cohort
		cq            *kueue.ClusterQueue
		wantAncestors []kueue.CohortReference
		wantErr       error
	}{
		"without cohort": {
			cq: utiltesting.MakeClusterQueue("cq").Obj(),
		},
		"cohort not found": {
			cq: utiltesting.MakeClusterQueue("cq").Cohort("root").Obj(),
		},
		"one level": {
			cohorts: []*kueue.Cohort{
				utiltesting.MakeCohort("root").Obj(),
			},
			cq:            utiltesting.MakeClusterQueue("cq").Cohort("root").Obj(),
			wantAncestors: []kueue.CohortReference{},
		},
		"two level": {
			cohorts: []*kueue.Cohort{
				utiltesting.MakeCohort("root").Obj(),
				utiltesting.MakeCohort("left").Parent("root").Obj(),
				utiltesting.MakeCohort("right").Parent("root").Obj(),
			},
			cq:            utiltesting.MakeClusterQueue("cq").Cohort("left").Obj(),
			wantAncestors: []kueue.CohortReference{"left"},
		},
		"three levels": {
			cohorts: []*kueue.Cohort{
				utiltesting.MakeCohort("root").Obj(),
				utiltesting.MakeCohort("first-left").Parent("root").Obj(),
				utiltesting.MakeCohort("first-right").Parent("root").Obj(),
				utiltesting.MakeCohort("second-left").Parent("first-left").Obj(),
				utiltesting.MakeCohort("second-right").Parent("first-left").Obj(),
			},
			cq:            utiltesting.MakeClusterQueue("cq").Cohort("second-left").Obj(),
			wantAncestors: []kueue.CohortReference{"second-left", "first-left"},
		},
		"with cycle": {
			cohorts: []*kueue.Cohort{
				utiltesting.MakeCohort("root").Parent("second-right").Obj(),
				utiltesting.MakeCohort("first-left").Parent("root").Obj(),
				utiltesting.MakeCohort("first-right").Parent("root").Obj(),
				utiltesting.MakeCohort("second-left").Parent("first-left").Obj(),
				utiltesting.MakeCohort("second-right").Parent("first-left").Obj(),
			},
			cq:      utiltesting.MakeClusterQueue("cq").Cohort("second-left").Obj(),
			wantErr: ErrCohortHasCycle,
		},
	}
	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			client := utiltesting.NewClientBuilder().Build()
			cache := New(client)
			for _, cohort := range tc.cohorts {
				_ = cache.AddOrUpdateCohort(cohort)
			}

			gotAncestors, gotErr := cache.ClusterQueueAncestors(tc.cq)

			if diff := cmp.Diff(tc.wantAncestors, gotAncestors); diff != "" {
				t.Fatalf("Unexpected error (-want/+got)\n%s", diff)
			}

			if diff := cmp.Diff(tc.wantErr, gotErr, cmpopts.EquateErrors()); diff != "" {
				t.Fatalf("Unexpected error (-want/+got)\n%s", diff)
			}
		})
	}
}
