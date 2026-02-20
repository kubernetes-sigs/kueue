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
	"context"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

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

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/cache/hierarchy"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/resources"
	"sigs.k8s.io/kueue/pkg/util/queue"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/pkg/workload"
)

func TestCacheClusterQueueOperations(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	ctx, _ := utiltesting.ContextWithLog(t)
	initialClusterQueues := []kueue.ClusterQueue{
		*utiltestingapi.MakeClusterQueue("a").
			ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas("default").
					Resource(corev1.ResourceCPU, "10", "10").Obj()).
			Cohort("one").
			NamespaceSelector(nil).
			Obj(),
		*utiltestingapi.MakeClusterQueue("b").
			ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas("default").
					Resource(corev1.ResourceCPU, "15").Obj()).
			Cohort("one").
			NamespaceSelector(nil).
			Obj(),
		*utiltestingapi.MakeClusterQueue("c").
			Cohort("two").
			NamespaceSelector(nil).
			Obj(),
		*utiltestingapi.MakeClusterQueue("d").
			NamespaceSelector(nil).
			Obj(),
		*utiltestingapi.MakeClusterQueue("e").
			ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas("nonexistent-flavor").
					Resource(corev1.ResourceCPU, "15").Obj()).
			Cohort("two").
			NamespaceSelector(nil).
			Obj(),
		*utiltestingapi.MakeClusterQueue("f").
			Cohort("two").
			NamespaceSelector(nil).
			FlavorFungibility(kueue.FlavorFungibility{
				WhenCanBorrow: kueue.TryNextFlavor,
			}).
			Obj(),
	}
	setup := func(log logr.Logger, cache *Cache) error {
		cache.AddOrUpdateResourceFlavor(log,
			utiltestingapi.MakeResourceFlavor("default").
				NodeLabel("cpuType", "default").
				Obj())
		for _, c := range initialClusterQueues {
			if err := cache.AddClusterQueue(ctx, &c); err != nil {
				return fmt.Errorf("failed adding ClusterQueue: %w", err)
			}
		}
		return nil
	}
	cases := []struct {
		name              string
		operation         func(log logr.Logger, cache *Cache) error
		clientObjects     []client.Object
		wantClusterQueues map[kueue.ClusterQueueReference]*clusterQueue
		wantCohorts       map[kueue.CohortReference]sets.Set[kueue.ClusterQueueReference]
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
					FairWeight:                    defaultWeight,
				},
				"b": {
					Name:                          "b",
					AllocatableResourceGeneration: 1,
					NamespaceSelector:             labels.Nothing(),
					FlavorFungibility:             defaultFlavorFungibility,
					Status:                        active,
					Preemption:                    defaultPreemption,
					FairWeight:                    defaultWeight,
				},
				"c": {
					Name:                          "c",
					AllocatableResourceGeneration: 3,
					NamespaceSelector:             labels.Nothing(),
					FlavorFungibility:             defaultFlavorFungibility,
					Status:                        active,
					Preemption:                    defaultPreemption,
					FairWeight:                    defaultWeight,
				},
				"d": {
					Name:                          "d",
					AllocatableResourceGeneration: 1,
					NamespaceSelector:             labels.Nothing(),
					FlavorFungibility:             defaultFlavorFungibility,
					Status:                        active,
					Preemption:                    defaultPreemption,
					FairWeight:                    defaultWeight,
				},
				"e": {
					Name:                          "e",
					AllocatableResourceGeneration: 2,
					NamespaceSelector:             labels.Nothing(),
					FlavorFungibility:             defaultFlavorFungibility,
					Status:                        pending,
					Preemption:                    defaultPreemption,
					FairWeight:                    defaultWeight,
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
					FairWeight: defaultWeight,
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
				cq := utiltestingapi.MakeClusterQueue("foo").Preemption(kueue.ClusterQueuePreemption{
					ReclaimWithinCohort: kueue.PreemptionPolicyLowerPriority,
					WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
				}).Obj()
				if err := cache.AddClusterQueue(ctx, cq); err != nil {
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
					FairWeight: defaultWeight,
				},
			},
		},
		{
			name: "add ClusterQueue with fair sharing weight",
			operation: func(log logr.Logger, cache *Cache) error {
				cq := utiltestingapi.MakeClusterQueue("foo").FairWeight(resource.MustParse("2")).Obj()
				if err := cache.AddClusterQueue(ctx, cq); err != nil {
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
					FairWeight:                    2.0,
				},
			},
		},
		{
			name: "add flavors after queue capacities",
			operation: func(log logr.Logger, cache *Cache) error {
				for _, c := range initialClusterQueues {
					if err := cache.AddClusterQueue(ctx, &c); err != nil {
						return fmt.Errorf("failed adding ClusterQueue: %w", err)
					}
				}
				cache.AddOrUpdateResourceFlavor(log,
					utiltestingapi.MakeResourceFlavor("default").
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
					FairWeight:                    defaultWeight,
				},
				"b": {
					Name:                          "b",
					AllocatableResourceGeneration: 1,
					FlavorFungibility:             defaultFlavorFungibility,
					NamespaceSelector:             labels.Nothing(),
					Status:                        active,
					Preemption:                    defaultPreemption,
					FairWeight:                    defaultWeight,
				},
				"c": {
					Name:                          "c",
					AllocatableResourceGeneration: 3,
					NamespaceSelector:             labels.Nothing(),
					FlavorFungibility:             defaultFlavorFungibility,
					Status:                        active,
					Preemption:                    defaultPreemption,
					FairWeight:                    defaultWeight,
				},
				"d": {
					Name:                          "d",
					AllocatableResourceGeneration: 1,
					NamespaceSelector:             labels.Nothing(),
					FlavorFungibility:             defaultFlavorFungibility,
					Status:                        active,
					Preemption:                    defaultPreemption,
					FairWeight:                    defaultWeight,
				},
				"e": {
					Name:                          "e",
					AllocatableResourceGeneration: 2,
					NamespaceSelector:             labels.Nothing(),
					FlavorFungibility:             defaultFlavorFungibility,
					Status:                        pending,
					Preemption:                    defaultPreemption,
					FairWeight:                    defaultWeight,
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
					FairWeight: defaultWeight,
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
					*utiltestingapi.MakeClusterQueue("a").
						ResourceGroup(
							*utiltestingapi.MakeFlavorQuotas("default").
								Resource(corev1.ResourceCPU, "5", "5").Obj()).
						Cohort("two").
						NamespaceSelector(nil).
						Obj(),
					*utiltestingapi.MakeClusterQueue("b").Cohort("one").Obj(), // remove the only resource group and set a namespace selector.
					*utiltestingapi.MakeClusterQueue("e").
						ResourceGroup(
							*utiltestingapi.MakeFlavorQuotas("default").
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
					utiltestingapi.MakeResourceFlavor("default").
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
					FairWeight:                    defaultWeight,
				},
				"b": {
					Name:                          "b",
					AllocatableResourceGeneration: 3,
					NamespaceSelector:             labels.Everything(),
					FlavorFungibility:             defaultFlavorFungibility,
					Status:                        active,
					Preemption:                    defaultPreemption,
					FairWeight:                    defaultWeight,
				},
				"c": {
					Name:                          "c",
					AllocatableResourceGeneration: 5,
					NamespaceSelector:             labels.Nothing(),
					FlavorFungibility:             defaultFlavorFungibility,
					Status:                        active,
					Preemption:                    defaultPreemption,
					FairWeight:                    defaultWeight,
				},
				"d": {
					Name:                          "d",
					AllocatableResourceGeneration: 1,
					NamespaceSelector:             labels.Nothing(),
					FlavorFungibility:             defaultFlavorFungibility,
					Status:                        active,
					Preemption:                    defaultPreemption,
					FairWeight:                    defaultWeight,
				},
				"e": {
					Name:                          "e",
					AllocatableResourceGeneration: 4,
					NamespaceSelector:             labels.Nothing(),
					FlavorFungibility:             defaultFlavorFungibility,
					Status:                        active,
					Preemption:                    defaultPreemption,
					FairWeight:                    defaultWeight,
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
					FairWeight: defaultWeight,
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
					utiltestingapi.MakeResourceFlavor("default").
						NodeLabel("cpuType", "default").
						Obj())

				cq := utiltestingapi.MakeClusterQueue("a").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("default").
							Resource(corev1.ResourceCPU, "10", "10").Obj()).
					Cohort("one").
					NamespaceSelector(nil).
					Obj()

				if err := cache.AddClusterQueue(ctx, cq); err != nil {
					return fmt.Errorf("failed adding ClusterQueue: %w", err)
				}

				wl := utiltestingapi.MakeWorkload("one", "").
					Request(corev1.ResourceCPU, "5").
					ReserveQuotaAt(utiltestingapi.MakeAdmission("a").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "default", "5000m").
							Obj()).Obj(), now).
					Condition(metav1.Condition{Type: kueue.WorkloadAdmitted, Status: metav1.ConditionTrue}).
					Obj()

				cache.AddOrUpdateWorkload(log, wl)

				cq = utiltestingapi.MakeClusterQueue("a").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Obj()).
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
					FairWeight: defaultWeight,
					Workloads: map[workload.Reference]*workload.Info{
						"/one": {
							Obj: utiltestingapi.MakeWorkload("one", "").
								Request(corev1.ResourceCPU, "5").
								ReserveQuotaAt(utiltestingapi.MakeAdmission("a").
									PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
										Assignment(corev1.ResourceCPU, "default", "5000m").
										Obj()).Obj(), now).
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
					*utiltestingapi.MakeClusterQueue("a").NamespaceSelector(nil).Cohort("three").Obj(),
					*utiltestingapi.MakeClusterQueue("b").NamespaceSelector(nil).Obj(),
					*utiltestingapi.MakeClusterQueue("c").NamespaceSelector(nil).Cohort("three").Obj(),
				}
				for _, c := range updateCqs {
					_ = cache.UpdateClusterQueue(log, &c)
				}
				deleteCqs := []kueue.ClusterQueue{
					*utiltestingapi.MakeClusterQueue("d").Cohort("two").Obj(),
					*utiltestingapi.MakeClusterQueue("e").Cohort("two").Obj(),
					*utiltestingapi.MakeClusterQueue("f").Cohort("two").Obj(),
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
					FairWeight:                    defaultWeight,
					NamespaceSelector:             labels.Nothing(),
				},
				"b": {
					Name:                          "b",
					AllocatableResourceGeneration: 3,
					FlavorFungibility:             defaultFlavorFungibility,
					Status:                        active,
					Preemption:                    defaultPreemption,
					FairWeight:                    defaultWeight,
					NamespaceSelector:             labels.Nothing(),
				},
				"c": {
					Name:                          "c",
					AllocatableResourceGeneration: 4,
					FlavorFungibility:             defaultFlavorFungibility,
					Status:                        active,
					Preemption:                    defaultPreemption,
					FairWeight:                    defaultWeight,
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
					FairWeight:                    defaultWeight,
				},
				"c": {
					Name:                          "c",
					AllocatableResourceGeneration: 3,
					NamespaceSelector:             labels.Nothing(),
					FlavorFungibility:             defaultFlavorFungibility,
					Status:                        active,
					Preemption:                    defaultPreemption,
					FairWeight:                    defaultWeight,
				},
				"e": {
					Name:                          "e",
					AllocatableResourceGeneration: 2,
					NamespaceSelector:             labels.Nothing(),
					FlavorFungibility:             defaultFlavorFungibility,
					Status:                        pending,
					Preemption:                    defaultPreemption,
					FairWeight:                    defaultWeight,
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
					FairWeight: defaultWeight,
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
				cache.AddOrUpdateResourceFlavor(log, utiltestingapi.MakeResourceFlavor("nonexistent-flavor").Obj())
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
					FairWeight:                    defaultWeight,
				},
				"b": {
					Name:                          "b",
					AllocatableResourceGeneration: 1,
					NamespaceSelector:             labels.Nothing(),
					FlavorFungibility:             defaultFlavorFungibility,
					Status:                        active,
					Preemption:                    defaultPreemption,
					FairWeight:                    defaultWeight,
				},
				"c": {
					Name:                          "c",
					AllocatableResourceGeneration: 3,
					NamespaceSelector:             labels.Nothing(),
					FlavorFungibility:             defaultFlavorFungibility,
					Status:                        active,
					Preemption:                    defaultPreemption,
					FairWeight:                    defaultWeight,
				},
				"d": {
					Name:                          "d",
					AllocatableResourceGeneration: 1,
					NamespaceSelector:             labels.Nothing(),
					FlavorFungibility:             defaultFlavorFungibility,
					Status:                        active,
					Preemption:                    defaultPreemption,
					FairWeight:                    defaultWeight,
				},
				"e": {
					Name:                          "e",
					AllocatableResourceGeneration: 2,
					NamespaceSelector:             labels.Nothing(),
					FlavorFungibility:             defaultFlavorFungibility,
					Status:                        active,
					Preemption:                    defaultPreemption,
					FairWeight:                    defaultWeight,
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
					FairWeight: defaultWeight,
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
				err := cache.AddClusterQueue(ctx,
					utiltestingapi.MakeClusterQueue("foo").
						ResourceGroup(
							*utiltestingapi.MakeFlavorQuotas("foo").
								Resource("cpu").
								Resource("memory").
								Obj(),
							*utiltestingapi.MakeFlavorQuotas("bar").
								Resource("cpu").
								Resource("memory").
								Obj(),
						).
						ResourceGroup(
							*utiltestingapi.MakeFlavorQuotas("theta").Resource("example.com/gpu").Obj(),
							*utiltestingapi.MakeFlavorQuotas("gamma").Resource("example.com/gpu").Obj(),
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
					FairWeight:                    defaultWeight,
				},
			},
		},
		{
			name: "add cluster queue with missing check",
			operation: func(log logr.Logger, cache *Cache) error {
				err := cache.AddClusterQueue(ctx,
					utiltestingapi.MakeClusterQueue("foo").
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
					FairWeight: defaultWeight,
				},
			},
			wantCohorts: map[kueue.CohortReference]sets.Set[kueue.ClusterQueueReference]{},
		},
		{
			name: "add check after queue creation",
			operation: func(log logr.Logger, cache *Cache) error {
				err := cache.AddClusterQueue(ctx,
					utiltestingapi.MakeClusterQueue("foo").
						AdmissionChecks("check1", "check2").
						Obj())
				if err != nil {
					return fmt.Errorf("adding ClusterQueue: %w", err)
				}

				cache.AddOrUpdateAdmissionCheck(log, utiltestingapi.MakeAdmissionCheck("check1").Active(metav1.ConditionTrue).Obj())
				cache.AddOrUpdateAdmissionCheck(log, utiltestingapi.MakeAdmissionCheck("check2").Active(metav1.ConditionTrue).Obj())
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
					FairWeight: defaultWeight,
				},
			},
			wantCohorts: map[kueue.CohortReference]sets.Set[kueue.ClusterQueueReference]{},
		},
		{
			name: "remove check after queue creation",
			operation: func(log logr.Logger, cache *Cache) error {
				cache.AddOrUpdateAdmissionCheck(log, utiltestingapi.MakeAdmissionCheck("check1").Active(metav1.ConditionTrue).Obj())
				cache.AddOrUpdateAdmissionCheck(log, utiltestingapi.MakeAdmissionCheck("check2").Active(metav1.ConditionTrue).Obj())
				err := cache.AddClusterQueue(ctx,
					utiltestingapi.MakeClusterQueue("foo").
						AdmissionChecks("check1", "check2").
						Obj())
				if err != nil {
					return fmt.Errorf("adding ClusterQueue: %w", err)
				}

				cache.DeleteAdmissionCheck(log, utiltestingapi.MakeAdmissionCheck("check2").Obj())
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
					FairWeight: defaultWeight,
				},
			},
			wantCohorts: map[kueue.CohortReference]sets.Set[kueue.ClusterQueueReference]{},
		},
		{
			name: "inactivate check after queue creation",
			operation: func(log logr.Logger, cache *Cache) error {
				cache.AddOrUpdateAdmissionCheck(log, utiltestingapi.MakeAdmissionCheck("check1").Active(metav1.ConditionTrue).Obj())
				cache.AddOrUpdateAdmissionCheck(log, utiltestingapi.MakeAdmissionCheck("check2").Active(metav1.ConditionTrue).Obj())
				err := cache.AddClusterQueue(ctx,
					utiltestingapi.MakeClusterQueue("foo").
						AdmissionChecks("check1", "check2").
						Obj())
				if err != nil {
					return fmt.Errorf("adding ClusterQueue: %w", err)
				}

				cache.AddOrUpdateAdmissionCheck(log, utiltestingapi.MakeAdmissionCheck("check2").Active(metav1.ConditionFalse).Obj())
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
					FairWeight: defaultWeight,
				},
			},
			wantCohorts: map[kueue.CohortReference]sets.Set[kueue.ClusterQueueReference]{},
		},
		{
			name: "add cluster queue after finished workloads",
			clientObjects: []client.Object{
				utiltestingapi.MakeLocalQueue("lq1", "ns").ClusterQueue("cq1").Obj(),
				utiltestingapi.MakeWorkload("pending", "ns").Obj(),
				utiltestingapi.MakeWorkload("reserving", "ns").ReserveQuotaAt(
					utiltestingapi.MakeAdmission("cq1").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "f1", "1").
							Obj()).
						Obj(), now,
				).Obj(),
				utiltestingapi.MakeWorkload("admitted", "ns").ReserveQuotaAt(
					utiltestingapi.MakeAdmission("cq1").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "f1", "1").
							Obj()).
						Obj(), now,
				).AdmittedAt(true, now).Obj(),
				utiltestingapi.MakeWorkload("finished", "ns").ReserveQuotaAt(
					utiltestingapi.MakeAdmission("cq1").
						PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
							Assignment(corev1.ResourceCPU, "f1", "1").
							Obj()).
						Obj(), now,
				).AdmittedAt(true, now).Finished().Obj(),
			},
			operation: func(log logr.Logger, cache *Cache) error {
				cache.AddOrUpdateResourceFlavor(log, utiltestingapi.MakeResourceFlavor("f1").Obj())
				err := cache.AddClusterQueue(ctx,
					utiltestingapi.MakeClusterQueue("cq1").
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
					FairWeight: defaultWeight,
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
				cq := utiltestingapi.MakeClusterQueue("foo").
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
				return cache.AddClusterQueue(ctx, cq)
			},
			wantClusterQueues: map[kueue.ClusterQueueReference]*clusterQueue{
				"foo": {
					Name:                          "foo",
					NamespaceSelector:             labels.Everything(),
					Status:                        pending,
					Preemption:                    defaultPreemption,
					AllocatableResourceGeneration: 1,
					FlavorFungibility:             defaultFlavorFungibility,
					FairWeight:                    defaultWeight,
				},
			},
		},
		{
			name: "create cohort",
			operation: func(log logr.Logger, cache *Cache) error {
				cohort := utiltestingapi.MakeCohort("cohort").Obj()
				return cache.AddOrUpdateCohort(cohort)
			},
			wantCohorts: map[kueue.CohortReference]sets.Set[kueue.ClusterQueueReference]{
				"cohort": nil,
			},
		},
		{
			name: "create and delete cohort",
			operation: func(log logr.Logger, cache *Cache) error {
				cohort := utiltestingapi.MakeCohort("cohort").Obj()
				_ = cache.AddOrUpdateCohort(cohort)
				cache.DeleteCohort(kueue.CohortReference(cohort.Name))
				return nil
			},
			wantCohorts: nil,
		},
		{
			name: "cohort remains after deletion when child exists",
			operation: func(log logr.Logger, cache *Cache) error {
				cohort := utiltestingapi.MakeCohort("cohort").Obj()
				_ = cache.AddOrUpdateCohort(cohort)

				_ = cache.AddClusterQueue(ctx,
					utiltestingapi.MakeClusterQueue("cq").Cohort("cohort").Obj())
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
					FairWeight:                    defaultWeight,
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
	now := time.Now().Truncate(time.Second)
	clusterQueues := []kueue.ClusterQueue{
		*utiltestingapi.MakeClusterQueue("one").
			ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas("on-demand").Resource("cpu").Obj(),
				*utiltestingapi.MakeFlavorQuotas("spot").Resource("cpu").Obj(),
			).
			NamespaceSelector(nil).
			Obj(),
		*utiltestingapi.MakeClusterQueue("two").
			ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas("on-demand").Resource("cpu").Obj(),
				*utiltestingapi.MakeFlavorQuotas("spot").Resource("cpu").Obj(),
			).
			NamespaceSelector(nil).
			Obj(),
	}
	podSets := []kueue.PodSet{
		*utiltestingapi.MakePodSet("driver", 1).
			Request(corev1.ResourceCPU, "10m").
			Request(corev1.ResourceMemory, "512Ki").
			Obj(),
		*utiltestingapi.MakePodSet("workers", 3).
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
		utiltestingapi.MakeWorkload("a", "").PodSets(podSets...).ReserveQuotaAt(&kueue.Admission{
			ClusterQueue:      "one",
			PodSetAssignments: psAssignments,
		}, now).Obj(),
		utiltestingapi.MakeWorkload("b", "").ReserveQuotaAt(&kueue.Admission{
			ClusterQueue: "one",
		}, now).Obj(),
		utiltestingapi.MakeWorkload("c", "").PodSets(podSets...).ReserveQuotaAt(&kueue.Admission{
			ClusterQueue: "two",
		}, now).Obj())

	type result struct {
		Workloads     sets.Set[workload.Reference]
		UsedResources resources.FlavorResourceQuantities
	}

	steps := []struct {
		name               string
		operation          func(log logr.Logger, cache *Cache) error
		wantResults        map[kueue.ClusterQueueReference]result
		wantError          string
		wantLocalQueue     queue.LocalQueueReference
		wantQueuesAssigned map[workload.Reference]kueue.ClusterQueueReference
	}{
		{
			name: "add",
			operation: func(log logr.Logger, cache *Cache) error {
				workloads := []*kueue.Workload{
					utiltestingapi.MakeWorkload("a", "").PodSets(podSets...).ReserveQuotaAt(&kueue.Admission{
						ClusterQueue:      "one",
						PodSetAssignments: psAssignments,
					}, now).Obj(),
					utiltestingapi.MakeWorkload("d", "").ReserveQuotaAt(&kueue.Admission{
						ClusterQueue: "two",
					}, now).Obj(),
					utiltestingapi.MakeWorkload("pending", "").Obj(),
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
				w := utiltestingapi.MakeWorkload("d", "").ReserveQuotaAt(&kueue.Admission{
					ClusterQueue: "three",
				}, now).Obj()
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
				w := utiltestingapi.MakeWorkload("b", "").ReserveQuotaAt(&kueue.Admission{
					ClusterQueue: "one",
				}, now).Obj()
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
			name: "AddOrUpdateWorkload; finished workload removed",
			operation: func(log logr.Logger, cache *Cache) error {
				w := utiltestingapi.MakeWorkload("b", "").ReserveQuotaAt(&kueue.Admission{
					ClusterQueue: "one",
				}, now).Finished().Obj()
				if cache.AddOrUpdateWorkload(log, w) {
					return errors.New("declared workload update performed when only a deletion should have been performed")
				}
				return nil
			},
			wantResults: map[kueue.ClusterQueueReference]result{
				"one": {
					Workloads: sets.New[workload.Reference]("/a"),
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
			name: "AddOrUpdateWorkload; quota assigned -> quota unassigned",
			operation: func(log logr.Logger, cache *Cache) error {
				w := utiltestingapi.MakeWorkload("b", "").Obj()
				if cache.AddOrUpdateWorkload(log, w) {
					return errors.New("declared workload update performed when only a deletion should have been performed")
				}
				return nil
			},
			wantResults: map[kueue.ClusterQueueReference]result{
				"one": {
					Workloads: sets.New[workload.Reference]("/a"),
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
			name: "AddOrUpdateWorkload; quota not assigned -> quota still not assigned",
			operation: func(log logr.Logger, cache *Cache) error {
				w := utiltestingapi.MakeWorkload("d", "").Obj()
				if cache.AddOrUpdateWorkload(log, w) {
					return errors.New("declared workload update performed when no action should have been taken")
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
				latest := utiltestingapi.MakeWorkload("a", "").PodSets(podSets...).ReserveQuotaAt(&kueue.Admission{
					ClusterQueue:      "two",
					PodSetAssignments: psAssignments,
				}, now).Obj()
				if !cache.AddOrUpdateWorkload(log, latest) {
					return errors.New("failed to update workload")
				}
				return nil
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
			name: "update error new ClusterQueue not found",
			operation: func(log logr.Logger, cache *Cache) error {
				latest := utiltestingapi.MakeWorkload("d", "").ReserveQuotaAt(&kueue.Admission{
					ClusterQueue: "three",
				}, now).Obj()
				if !cache.AddOrUpdateWorkload(log, latest) {
					return errors.New("failed to update workload")
				}
				return nil
			},
			wantError: "failed to update workload",
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
				latest := utiltestingapi.MakeWorkload("d", "").ReserveQuotaAt(&kueue.Admission{
					ClusterQueue: "two",
				}, now).Obj()
				if !cache.AddOrUpdateWorkload(log, latest) {
					return errors.New("failed to update workload")
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
			name: "delete",
			operation: func(log logr.Logger, cache *Cache) error {
				w := utiltestingapi.MakeWorkload("a", "").ReserveQuotaAt(&kueue.Admission{
					ClusterQueue: "one",
				}, now).Obj()
				return cache.DeleteWorkload(log, workload.Key(w))
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
				w := utiltestingapi.MakeWorkload("a", "").Obj()
				return cache.DeleteWorkload(log, workload.Key(w))
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
			name: "delete non-existing workload with cancelled admission",
			operation: func(log logr.Logger, cache *Cache) error {
				w := utiltestingapi.MakeWorkload("d", "").Obj()
				return cache.DeleteWorkload(log, workload.Key(w))
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
			name: "delete error clusterQueue doesn't exist",
			operation: func(log logr.Logger, cache *Cache) error {
				cq := utiltestingapi.MakeClusterQueue("three").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas("on-demand").Resource("cpu").Obj(),
						*utiltestingapi.MakeFlavorQuotas("spot").Resource("cpu").Obj(),
					).
					NamespaceSelector(nil).
					Obj()
				w := utiltestingapi.MakeWorkload("d", "").ReserveQuotaAt(&kueue.Admission{
					ClusterQueue: "three",
				}, now).Obj()

				if err := cache.AddClusterQueue(logr.NewContext(t.Context(), log), cq); err != nil {
					return err
				}
				if updated, _ := cache.addOrUpdateWorkloadWithoutLock(log, w); !updated {
					return errors.New("failed to add test workload")
				}
				cache.DeleteClusterQueue(cq)

				return cache.DeleteWorkload(log, workload.Key(w))
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
			wantQueuesAssigned: map[workload.Reference]kueue.ClusterQueueReference{
				"/a": "one",
				"/b": "one",
				"/c": "two",
				"/d": "three",
			},
		},
		{
			name: "delete workload which doesn't exist",
			operation: func(log logr.Logger, cache *Cache) error {
				w := utiltestingapi.MakeWorkload("d", "").ReserveQuotaAt(&kueue.Admission{
					ClusterQueue: "one",
				}, now).Obj()
				return cache.DeleteWorkload(log, workload.Key(w))
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
	}
	for _, step := range steps {
		t.Run(step.name, func(t *testing.T) {
			cache := New(cl)
			ctx, log := utiltesting.ContextWithLog(t)

			for _, c := range clusterQueues {
				if err := cache.AddClusterQueue(ctx, &c); err != nil {
					t.Fatalf("Failed adding clusterQueue: %v", err)
				}
			}

			wantQueuesAssigned := step.wantQueuesAssigned
			if wantQueuesAssigned == nil {
				wantQueuesAssigned = map[workload.Reference]kueue.ClusterQueueReference{}
				for cq, res := range step.wantResults {
					for wlKey := range res.Workloads {
						wantQueuesAssigned[wlKey] = cq
					}
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
			if diff := cmp.Diff(wantQueuesAssigned, cache.workloadAssignedQueues, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("Unexpected clusterQueues assignments for workloads (-want,+got):\n%s", diff)
			}
		})
	}
}

func TestClusterQueueUsage(t *testing.T) {
	now := time.Now().Truncate(time.Second)
	cq := utiltestingapi.MakeClusterQueue("foo").
		ResourceGroup(
			*utiltestingapi.MakeFlavorQuotas("default").
				Resource(corev1.ResourceCPU, "10", "10").
				Obj(),
		).
		ResourceGroup(
			*utiltestingapi.MakeFlavorQuotas("model_a").
				Resource("example.com/gpu", "5", "5").
				Obj(),
			*utiltestingapi.MakeFlavorQuotas("model_b").
				Resource("example.com/gpu", "5").
				Obj(),
		).
		ResourceGroup(
			*utiltestingapi.MakeFlavorQuotas("interconnect_a").
				Resource("example.com/vf-0", "5", "5").
				Resource("example.com/vf-1", "5", "5").
				Resource("example.com/vf-2", "5", "5").
				Obj(),
		).
		Cohort("one").Obj()
	cqWithOutCohort := cq.DeepCopy()
	cqWithOutCohort.Spec.CohortName = ""
	workloads := []kueue.Workload{
		*utiltestingapi.MakeWorkload("one", "").
			Request(corev1.ResourceCPU, "8").
			Request("example.com/gpu", "5").
			ReserveQuotaAt(utiltestingapi.MakeAdmission("foo").
				PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
					Assignment(corev1.ResourceCPU, "default", "8000m").
					Assignment("example.com/gpu", "model_a", "5").
					Obj()).
				Obj(), now).
			Condition(metav1.Condition{Type: kueue.WorkloadAdmitted, Status: metav1.ConditionTrue}).
			Obj(),
		*utiltestingapi.MakeWorkload("two", "").
			Request(corev1.ResourceCPU, "5").
			Request("example.com/gpu", "6").
			ReserveQuotaAt(utiltestingapi.MakeAdmission("foo").
				PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
					Assignment(corev1.ResourceCPU, "default", "5000m").
					Assignment("example.com/gpu", "model_b", "6").
					Obj()).
				Obj(), now).
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
				*utiltestingapi.MakeWorkload("partial-one", "").
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 5).Request(corev1.ResourceCPU, "2").Obj()).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("foo").PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).Assignment(corev1.ResourceCPU, "default", "4000m").Count(2).Obj()).Obj(), now).
					AdmittedAt(true, now).
					Obj(),
				*utiltestingapi.MakeWorkload("partial-two", "").
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 5).Request(corev1.ResourceCPU, "2").Obj()).
					ReserveQuotaAt(utiltestingapi.MakeAdmission("foo").PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).Assignment(corev1.ResourceCPU, "default", "4000m").Count(2).Obj()).Obj(), now).
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
	now := time.Now().Truncate(time.Second)
	cq := *utiltestingapi.MakeClusterQueue("foo").
		ResourceGroup(
			*utiltestingapi.MakeFlavorQuotas("default").
				Resource(corev1.ResourceCPU, "10", "10").Obj(),
		).
		ResourceGroup(
			*utiltestingapi.MakeFlavorQuotas("model-a").
				Resource("example.com/gpu", "5").Obj(),
			*utiltestingapi.MakeFlavorQuotas("model-b").
				Resource("example.com/gpu", "5").Obj(),
		).
		ResourceGroup(
			*utiltestingapi.MakeFlavorQuotas("interconnect-a").
				Resource("example.com/vf-0", "5", "5").
				Resource("example.com/vf-1", "5", "5").
				Resource("example.com/vf-2", "5", "5").
				Obj(),
		).
		Obj()
	localQueue := *utiltestingapi.MakeLocalQueue("test", "ns1").
		ClusterQueue("foo").Obj()
	cases := map[string]struct {
		cq             *kueue.ClusterQueue
		wls            []kueue.Workload
		wantUsage      []kueue.LocalQueueFlavorUsage
		inAdmissibleWl sets.Set[string]
	}{
		"clusterQueue is missing": {
			wls: []kueue.Workload{
				*utiltestingapi.MakeWorkload("one", "ns1").
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
				*utiltestingapi.MakeWorkload("one", "ns1").
					Queue("test").
					Request(corev1.ResourceCPU, "5").
					Request("example.com/gpu", "5").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("foo").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "5000m").
								Assignment("example.com/gpu", "model-a", "5").Obj()).Obj(), now,
					).
					Obj(),
				*utiltestingapi.MakeWorkload("two", "ns1").
					Queue("test").
					Request(corev1.ResourceCPU, "3").
					Request("example.com/gpu", "3").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("foo").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "3000m").
								Assignment("example.com/gpu", "model-b", "3").Obj()).Obj(), now,
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
				*utiltestingapi.MakeWorkload("one", "ns1").
					Queue("test").
					Request(corev1.ResourceCPU, "5").
					Request("example.com/gpu", "5").
					ReserveQuotaAt(
						utiltestingapi.MakeAdmission("foo").
							PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
								Assignment(corev1.ResourceCPU, "default", "5000m").
								Assignment("example.com/gpu", "model-a", "5").Obj()).Obj(), now,
					).Obj(),
				*utiltestingapi.MakeWorkload("two", "ns1").
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
	cq := utiltestingapi.MakeClusterQueue("cq").
		ResourceGroup(
			*utiltestingapi.MakeFlavorQuotas("spot").
				Resource("cpu", "10", "10").
				Resource("memory", "64Gi", "64Gi").Obj(),
		).ResourceGroup(
		*utiltestingapi.MakeFlavorQuotas("model-a").
			Resource("example.com/gpu", "10", "10").Obj(),
	).Obj()
	lq := utiltestingapi.MakeLocalQueue("lq-a", "ns").ClusterQueue("cq").Obj()
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
			getLq:          utiltestingapi.MakeLocalQueue("non-existing-lq", "ns").ClusterQueue("cq").Obj(),
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
			ctx, _ := utiltesting.ContextWithLog(t)
			if err := cache.AddClusterQueue(ctx, cq); err != nil {
				t.Fatalf("Adding ClusterQueue: %v", err)
			}
			if err := cache.AddLocalQueue(lq); err != nil {
				t.Fatalf("Adding LocalQueue: %v", err)
			}

			lqKey := queue.Key(tc.getLq)
			gotLq, gotErr := cache.GetCacheLocalQueue(tc.getCQReference, lqKey)
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
	now := time.Now().Truncate(time.Second)
	cqs := []*kueue.ClusterQueue{
		utiltestingapi.MakeClusterQueue("foo").
			ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas("spot").
					Resource("cpu", "10", "10").
					Resource("memory", "64Gi", "64Gi").Obj(),
			).ResourceGroup(
			*utiltestingapi.MakeFlavorQuotas("model-a").
				Resource("example.com/gpu", "10", "10").Obj(),
		).Obj(),
		utiltestingapi.MakeClusterQueue("bar").
			ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas("ondemand").
					Resource("cpu", "5", "5").
					Resource("memory", "32Gi", "32Gi").Obj(),
			).ResourceGroup(
			*utiltestingapi.MakeFlavorQuotas("model-b").
				Resource("example.com/gpu", "5", "5").Obj(),
		).Obj(),
	}
	queues := []*kueue.LocalQueue{
		utiltestingapi.MakeLocalQueue("alpha", "ns1").ClusterQueue("foo").Obj(),
		utiltestingapi.MakeLocalQueue("beta", "ns2").ClusterQueue("foo").Obj(),
		utiltestingapi.MakeLocalQueue("gamma", "ns1").ClusterQueue("bar").Obj(),
	}
	workloads := []*kueue.Workload{
		utiltestingapi.MakeWorkload("job1", "ns1").
			Queue("alpha").
			Request("cpu", "2").
			Request("memory", "8Gi").
			ReserveQuotaAt(
				utiltestingapi.MakeAdmission("foo").
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment("cpu", "spot", "2").
						Assignment("memory", "spot", "8Gi").Obj()).Obj(), now,
			).
			Condition(metav1.Condition{Type: kueue.WorkloadAdmitted, Status: metav1.ConditionTrue}).
			Obj(),
		utiltestingapi.MakeWorkload("job2", "ns2").
			Queue("beta").
			Request("example.com/gpu", "2").
			ReserveQuotaAt(
				utiltestingapi.MakeAdmission("foo").
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment("example.com/gpu", "model-a", "2").Obj()).Obj(), now,
			).
			Condition(metav1.Condition{Type: kueue.WorkloadAdmitted, Status: metav1.ConditionTrue}).
			Obj(),
		utiltestingapi.MakeWorkload("job3", "ns1").
			Queue("gamma").
			Request("cpu", "5").
			Request("memory", "16Gi").
			ReserveQuotaAt(
				utiltestingapi.MakeAdmission("bar").
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment("cpu", "ondemand", "5").
						Assignment("memory", "ondemand", "16Gi").Obj()).Obj(), now,
			).Obj(),
		utiltestingapi.MakeWorkload("job4", "ns2").
			Queue("beta").
			Request("example.com/gpu", "5").
			ReserveQuotaAt(
				utiltestingapi.MakeAdmission("foo").
					PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).
						Assignment("example.com/gpu", "model-a", "5").Obj()).Obj(), now,
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
		"add workload": {
			ops: []func(context.Context, client.Client, *Cache) error{
				insertAllClusterQueues,
				insertAllQueues,
				func(ctx context.Context, cl client.Client, cache *Cache) error {
					log := ctrl.LoggerFrom(ctx)
					wl := workloads[0].DeepCopy()
					if err := cl.Create(ctx, wl); err != nil {
						return err
					}
					if added := cache.AddOrUpdateWorkload(log, wl); !added {
						return fmt.Errorf("workload %s/%s could not be added to the cache", wl.Namespace, wl.Name)
					}
					return nil
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
		"delete workload": {
			ops: []func(ctx context.Context, cl client.Client, cache *Cache) error{
				insertAllClusterQueues,
				insertAllQueues,
				insertAllWorkloads,
				func(ctx context.Context, cl client.Client, cache *Cache) error {
					log := ctrl.LoggerFrom(ctx)
					return cache.DeleteWorkload(log, workload.Key(workloads[0]))
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
			ctx, _ := utiltesting.ContextWithLog(t)
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
	x86Rf := utiltestingapi.MakeResourceFlavor("x86").Obj()
	aarch64Rf := utiltestingapi.MakeResourceFlavor("aarch64").Obj()
	fooCq := utiltestingapi.MakeClusterQueue("fooCq").
		ResourceGroup(
			*utiltestingapi.MakeFlavorQuotas("x86").Resource("cpu", "5").Obj()).
		Obj()
	barCq := utiltestingapi.MakeClusterQueue("barCq").Obj()
	fizzCq := utiltestingapi.MakeClusterQueue("fizzCq").
		ResourceGroup(
			*utiltestingapi.MakeFlavorQuotas("x86").Resource("cpu", "5").Obj(),
			*utiltestingapi.MakeFlavorQuotas("aarch64").Resource("cpu", "3").Obj(),
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
			ctx, log := utiltesting.ContextWithLog(t)
			cache := New(utiltesting.NewFakeClient())
			cache.AddOrUpdateResourceFlavor(log, x86Rf)
			cache.AddOrUpdateResourceFlavor(log, aarch64Rf)

			for _, cq := range tc.clusterQueues {
				if err := cache.AddClusterQueue(ctx, cq); err != nil {
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
		utiltestingapi.MakeClusterQueue("matching1").
			NamespaceSelector(&metav1.LabelSelector{}).Obj(),
		utiltestingapi.MakeClusterQueue("not-matching").
			NamespaceSelector(nil).Obj(),
		utiltestingapi.MakeClusterQueue("matching2").
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
	ctx, _ := utiltesting.ContextWithLog(t)
	for _, cq := range clusterQueues {
		if err := cache.AddClusterQueue(ctx, cq); err != nil {
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
	now := time.Now().Truncate(time.Second)
	ctx, _ := utiltesting.ContextWithLog(t)
	cache := New(utiltesting.NewFakeClient(), WithPodsReadyTracking(true))
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	log := ctrl.LoggerFrom(ctx)

	go cache.CleanUpOnContext(ctx)

	cq := kueue.ClusterQueue{
		ObjectMeta: metav1.ObjectMeta{Name: "one"},
	}
	if err := cache.AddClusterQueue(ctx, &cq); err != nil {
		t.Fatalf("Failed adding clusterQueue: %v", err)
	}

	wl := utiltestingapi.MakeWorkload("a", "").ReserveQuotaAt(&kueue.Admission{
		ClusterQueue: "one",
	}, now).Obj()
	if added := cache.AddOrUpdateWorkload(log, wl); !added {
		t.Fatalf("workload %s/%s could not be added to the cache", wl.Namespace, wl.Name)
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
	now := time.Now().Truncate(time.Second)
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
				wl := utiltestingapi.MakeWorkload("a", "").ReserveQuotaAt(&kueue.Admission{
					ClusterQueue: "one",
				}, now).Obj()
				cache.AddOrUpdateWorkload(log, wl)
				return nil
			},
			wantReady: false,
		},
		{
			name: "add Workload with PodsReady=False",
			operation: func(log logr.Logger, cache *Cache) error {
				wl := utiltestingapi.MakeWorkload("a", "").ReserveQuotaAt(&kueue.Admission{
					ClusterQueue: "one",
				}, now).Condition(metav1.Condition{
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
				wl := utiltestingapi.MakeWorkload("a", "").ReserveQuotaAt(&kueue.Admission{
					ClusterQueue: "one",
				}, now).Condition(metav1.Condition{
					Type:   kueue.WorkloadPodsReady,
					Status: metav1.ConditionTrue,
				}).Obj()
				cache.AddOrUpdateWorkload(log, wl)
				return nil
			},
			wantReady: true,
		},
		{
			name: "update workload to have PodsReady=True",
			setup: func(log logr.Logger, cache *Cache) error {
				wl := utiltestingapi.MakeWorkload("a", "").ReserveQuotaAt(&kueue.Admission{
					ClusterQueue: "one",
				}, now).Obj()
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
				if !cache.AddOrUpdateWorkload(log, newWl) {
					return errors.New("failed to update workload")
				}
				return nil
			},
			wantReady: true,
		},
		{
			name: "update workload to have PodsReady=False",
			setup: func(log logr.Logger, cache *Cache) error {
				wl := utiltestingapi.MakeWorkload("a", "").ReserveQuotaAt(&kueue.Admission{
					ClusterQueue: "one",
				}, now).Condition(metav1.Condition{
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
				if !cache.AddOrUpdateWorkload(log, newWl) {
					return errors.New("failed to update workload")
				}
				return nil
			},
			wantReady: false,
		},
		{
			name: "update second workload to have PodsReady=True",
			setup: func(log logr.Logger, cache *Cache) error {
				wl1 := utiltestingapi.MakeWorkload("a", "").ReserveQuotaAt(&kueue.Admission{
					ClusterQueue: "one",
				}, now).Condition(metav1.Condition{
					Type:   kueue.WorkloadPodsReady,
					Status: metav1.ConditionTrue,
				}).Obj()
				cache.AddOrUpdateWorkload(log, wl1)
				wl2 := utiltestingapi.MakeWorkload("b", "").ReserveQuotaAt(&kueue.Admission{
					ClusterQueue: "two",
				}, now).Obj()
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
				if !cache.AddOrUpdateWorkload(log, newWl2) {
					return errors.New("failed to update workload")
				}
				return nil
			},
			wantReady: true,
		},
		{
			name: "delete workload with PodsReady=False",
			setup: func(log logr.Logger, cache *Cache) error {
				wl := utiltestingapi.MakeWorkload("a", "").ReserveQuotaAt(&kueue.Admission{
					ClusterQueue: "one",
				}, now).Condition(metav1.Condition{
					Type:   kueue.WorkloadPodsReady,
					Status: metav1.ConditionFalse,
				}).Obj()
				cache.AddOrUpdateWorkload(log, wl)
				return nil
			},
			operation: func(log logr.Logger, cache *Cache) error {
				wl := cache.hm.ClusterQueue("one").Workloads["/a"].Obj
				return cache.DeleteWorkload(log, workload.Key(wl))
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

// TestIsAddedCheckWorkload verifies if workload is correctly returned from cache
func TestIsAddedCheckWorkload(t *testing.T) {
	tests := []struct {
		name          string
		clusterQueues map[string]*clusterQueue
		workload      workload.Info
		expected      bool
	}{
		{
			name: "Workload is Admitted",
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
			name: "Workload is not Admitted",
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
			if cache.IsAdded(tc.workload) != tc.expected {
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
		utiltestingapi.MakeAdmissionCheck("ac1").Obj(),
		utiltestingapi.MakeAdmissionCheck("ac2").Obj(),
		utiltestingapi.MakeAdmissionCheck("ac3").Obj(),
	}

	fooCq := utiltestingapi.MakeClusterQueue("fooCq").
		AdmissionChecks("ac1").
		Obj()
	barCq := utiltestingapi.MakeClusterQueue("barCq").Obj()
	fizzCq := utiltestingapi.MakeClusterQueue("fizzCq").
		AdmissionChecks("ac1", "ac2").
		Obj()
	strategyCq := utiltestingapi.MakeClusterQueue("strategyCq").
		AdmissionCheckStrategy(
			*utiltestingapi.MakeAdmissionCheckStrategyRule("ac1").Obj(),
			*utiltestingapi.MakeAdmissionCheckStrategyRule("ac3").Obj()).
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
			ctx, log := utiltesting.ContextWithLog(t)
			cache := New(utiltesting.NewFakeClient())
			for _, check := range checks {
				cache.AddOrUpdateAdmissionCheck(log, check)
			}
			for _, cq := range tc.clusterQueues {
				if err := cache.AddClusterQueue(ctx, cq); err != nil {
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
	baseFlavor := utiltestingapi.MakeResourceFlavor("flavor1").Obj()
	baseCheck := utiltestingapi.MakeAdmissionCheck("check1").Active(metav1.ConditionTrue).Obj()
	baseQueue := utiltestingapi.MakeClusterQueue("queue1").
		ResourceGroup(
			*utiltestingapi.MakeFlavorQuotas(baseFlavor.Name).
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
			admissionChecks:  []*kueue.AdmissionCheck{utiltestingapi.MakeAdmissionCheck("check1").Obj()},
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
			clusterQueues:    []*kueue.ClusterQueue{utiltestingapi.MakeClusterQueue("queue1").StopPolicy(kueue.HoldAndDrain).Obj()},
			clusterQueueName: "queue1",
			wantStatus:       metav1.ConditionFalse,
			wantReason:       "Stopped",
			wantMessage:      "Can't admit new workloads: is stopped.",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)
			_, log := utiltesting.ContextWithLog(t)
			cache := New(utiltesting.NewFakeClient())
			for _, rf := range tc.resourceFlavors {
				cache.AddOrUpdateResourceFlavor(log, rf)
			}
			for _, ac := range tc.admissionChecks {
				cache.AddOrUpdateAdmissionCheck(log, ac)
			}
			for _, cq := range tc.clusterQueues {
				if err := cache.AddClusterQueue(ctx, cq); err != nil {
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
		cohort := utiltestingapi.MakeCohort("cohort").Parent("cohort").Obj()
		if err := cache.AddOrUpdateCohort(cohort); err == nil {
			t.Fatal("Expected failure when cycle")
		}
	})
	t.Run("simple cycle", func(t *testing.T) {
		cache := New(utiltesting.NewFakeClient())
		cohortA := utiltestingapi.MakeCohort("cohort-a").Parent("cohort-b").Obj()
		cohortB := utiltestingapi.MakeCohort("cohort-b").Parent("cohort-c").Obj()
		cohortC := utiltestingapi.MakeCohort("cohort-c").Parent("cohort-a").Obj()
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
		cohortA := utiltestingapi.MakeCohort("cohort-a").Parent("cohort-b").Obj()
		if err := cache.AddOrUpdateCohort(cohortA); err != nil {
			t.Fatal("Expected success as no cycle yet")
		}
		cohortB := utiltestingapi.MakeCohort("cohort-b").Parent("cohort-c").Obj()
		if err := cache.AddOrUpdateCohort(cohortB); err != nil {
			t.Fatal("Expected success as no cycle yet")
		}
		cohortC := utiltestingapi.MakeCohort("cohort-c").Parent("cohort-a").Obj()
		if err := cache.AddOrUpdateCohort(cohortC); err == nil {
			t.Fatal("Expected failure when cycle")
		}

		// Error when creating CQ with parent Cohort-A
		cq := utiltestingapi.MakeClusterQueue("cq").Cohort("cohort-a").Obj()
		if err := cache.AddClusterQueue(ctx, cq); err == nil {
			t.Fatal("Expected failure when adding cq to cohort with cycle")
		}

		// Error when updating CQ with parent Cohort-B
		cq = utiltestingapi.MakeClusterQueue("cq").Cohort("cohort-b").Obj()
		if err := cache.UpdateClusterQueue(log, cq); err == nil {
			t.Fatal("Expected failure when updating cq to cohort with cycle")
		}

		// Delete Cohort C, breaking cycle
		cache.DeleteCohort("cohort-c")

		// Update succeeds
		cq = utiltestingapi.MakeClusterQueue("cq").Cohort("cohort-b").Obj()
		if err := cache.UpdateClusterQueue(log, cq); err != nil {
			t.Fatal("Expected success")
		}
	})

	t.Run("clusterqueue leaving cohort with cycle successfully updates new cohort", func(t *testing.T) {
		cache := New(utiltesting.NewFakeClient())
		ctx, log := utiltesting.ContextWithLog(t)
		cycleCohort := utiltestingapi.MakeCohort("cycle").Parent("cycle").Obj()
		if err := cache.AddOrUpdateCohort(cycleCohort); err == nil {
			t.Fatal("Expected failure")
		}

		cohort := utiltestingapi.MakeCohort("cohort").
			ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas("arm").Resource(corev1.ResourceCPU, "10").Obj(),
			).Obj()
		if err := cache.AddOrUpdateCohort(cohort); err != nil {
			t.Fatal("Expected success")
		}

		// Error when creating cq with parent that has cycle
		cq := utiltestingapi.MakeClusterQueue("cq").
			ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas("arm").Resource(corev1.ResourceCPU, "5").Obj(),
			).Cohort("cycle").Obj()
		if err := cache.AddClusterQueue(ctx, cq); err == nil {
			t.Fatal("Expected failure")
		}

		// Successfully updated to cohort without cycle
		cq.Spec.CohortName = "cohort"
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
		cycleCohort := utiltestingapi.MakeCohort("cycle").Parent("cycle").Obj()
		if err := cache.AddOrUpdateCohort(cycleCohort); err == nil {
			t.Fatal("Expected failure")
		}
		cohort := utiltestingapi.MakeCohort("cohort").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas("arm").Resource(corev1.ResourceCPU, "10").Obj()).Obj()
		if err := cache.AddOrUpdateCohort(cohort); err != nil {
			t.Fatal("Expected success")
		}

		// Add CQ to cohort
		cq := utiltestingapi.MakeClusterQueue("cq").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas("arm").Resource(corev1.ResourceCPU, "5").Obj()).Cohort("cohort").Obj()
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
		cq.Spec.CohortName = "cycle"
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
		root1 := utiltestingapi.MakeCohort("root1").Obj()
		root2 := utiltestingapi.MakeCohort("root2").Obj()
		if err := cache.AddOrUpdateCohort(root1); err != nil {
			t.Fatal("Expected success")
		}
		if err := cache.AddOrUpdateCohort(root2); err != nil {
			t.Fatal("Expected success")
		}

		cohort := utiltestingapi.MakeCohort("cohort").Parent("root1").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas("arm").Resource(corev1.ResourceCPU, "10").Obj()).Obj()
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
		cycleRoot := utiltestingapi.MakeCohort("cycle-root").Parent("cycle-root").Obj()
		if err := cache.AddOrUpdateCohort(cycleRoot); err == nil {
			t.Fatal("Expected failure")
		}
		root := utiltestingapi.MakeCohort("root").Obj()
		if err := cache.AddOrUpdateCohort(root); err != nil {
			t.Fatal("Expected success")
		}

		cohort := utiltestingapi.MakeCohort("cohort").Parent("cycle-root").
			ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas("arm").Resource(corev1.ResourceCPU, "10").Obj(),
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
		cycleRoot := utiltestingapi.MakeCohort("cycle-root").Parent("cycle-root").Obj()
		if err := cache.AddOrUpdateCohort(cycleRoot); err == nil {
			t.Fatal("Expected failure")
		}
		root := utiltestingapi.MakeCohort("root").Obj()
		if err := cache.AddOrUpdateCohort(root); err != nil {
			t.Fatal("Expected success")
		}

		cohort := utiltestingapi.MakeCohort("cohort").Parent("root").
			ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas("arm").Resource(corev1.ResourceCPU, "10").Obj(),
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

	topology := *utiltestingapi.MakeDefaultOneLevelTopology("default")
	flavor := *utiltestingapi.MakeResourceFlavor("tas-default").
		TopologyName("default").
		Obj()
	localQueue := *utiltestingapi.MakeLocalQueue("lq", "default").ClusterQueue("cq").Obj()
	clusterQueue := utiltestingapi.MakeClusterQueue("cq").
		ResourceGroup(
			*utiltestingapi.MakeFlavorQuotas("tas-default").
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
		cache.AddOrUpdateTopology(log, &kueue.Topology{
			ObjectMeta: metav1.ObjectMeta{
				Name: string(*flavor.Spec.TopologyName),
			},
			Spec: kueue.TopologySpec{
				Levels: []kueue.TopologyLevel{
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
			cq: utiltestingapi.MakeClusterQueue("cq").Obj(),
		},
		"cohort not found": {
			cq: utiltestingapi.MakeClusterQueue("cq").Cohort("root").Obj(),
		},
		"one level": {
			cohorts: []*kueue.Cohort{
				utiltestingapi.MakeCohort("root").Obj(),
			},
			cq:            utiltestingapi.MakeClusterQueue("cq").Cohort("root").Obj(),
			wantAncestors: []kueue.CohortReference{},
		},
		"two level": {
			cohorts: []*kueue.Cohort{
				utiltestingapi.MakeCohort("root").Obj(),
				utiltestingapi.MakeCohort("left").Parent("root").Obj(),
				utiltestingapi.MakeCohort("right").Parent("root").Obj(),
			},
			cq:            utiltestingapi.MakeClusterQueue("cq").Cohort("left").Obj(),
			wantAncestors: []kueue.CohortReference{"left"},
		},
		"three levels": {
			cohorts: []*kueue.Cohort{
				utiltestingapi.MakeCohort("root").Obj(),
				utiltestingapi.MakeCohort("first-left").Parent("root").Obj(),
				utiltestingapi.MakeCohort("first-right").Parent("root").Obj(),
				utiltestingapi.MakeCohort("second-left").Parent("first-left").Obj(),
				utiltestingapi.MakeCohort("second-right").Parent("first-left").Obj(),
			},
			cq:            utiltestingapi.MakeClusterQueue("cq").Cohort("second-left").Obj(),
			wantAncestors: []kueue.CohortReference{"second-left", "first-left"},
		},
		"with cycle": {
			cohorts: []*kueue.Cohort{
				utiltestingapi.MakeCohort("root").Parent("second-right").Obj(),
				utiltestingapi.MakeCohort("first-left").Parent("root").Obj(),
				utiltestingapi.MakeCohort("first-right").Parent("root").Obj(),
				utiltestingapi.MakeCohort("second-left").Parent("first-left").Obj(),
				utiltestingapi.MakeCohort("second-right").Parent("first-left").Obj(),
			},
			cq:      utiltestingapi.MakeClusterQueue("cq").Cohort("second-left").Obj(),
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

func TestGetWorkloadFromCache(t *testing.T) {
	ctx, log := utiltesting.ContextWithLog(t)
	now := time.Now().Truncate(time.Second)

	cqToDelete := utiltestingapi.MakeClusterQueue("deleted-cq").Obj()
	clusterQueues := []*kueue.ClusterQueue{
		utiltestingapi.MakeClusterQueue("cq").Obj(),
		cqToDelete,
	}

	lqToDelete := utiltestingapi.MakeLocalQueue("deleted-lq", "").ClusterQueue("deleted-cq").Obj()
	queues := []*kueue.LocalQueue{
		utiltestingapi.MakeLocalQueue("lq", "").ClusterQueue("cq").Obj(),
		lqToDelete,
	}

	cases := map[string]struct {
		wl           *kueue.Workload
		deleteFromCq kueue.ClusterQueueReference
		wantWl       *kueue.Workload
	}{
		"workload fetched correctly": {
			wl: utiltestingapi.MakeWorkload("a", "").ReserveQuotaAt(&kueue.Admission{
				ClusterQueue: "cq",
			}, now).Condition(metav1.Condition{
				Type:   kueue.WorkloadPodsReady,
				Status: metav1.ConditionFalse,
			}).Obj(),
			wantWl: utiltestingapi.MakeWorkload("a", "").ReserveQuotaAt(&kueue.Admission{
				ClusterQueue: "cq",
			}, now).Condition(metav1.Condition{
				Type:   kueue.WorkloadPodsReady,
				Status: metav1.ConditionFalse,
			}).Obj(),
		},
		"cluster queue deleted": {
			wl: utiltestingapi.MakeWorkload("a", "").ReserveQuotaAt(&kueue.Admission{
				ClusterQueue: "deleted-cq",
			}, now).Condition(metav1.Condition{
				Type:   kueue.WorkloadPodsReady,
				Status: metav1.ConditionFalse,
			}).Obj(),
			wantWl: nil,
		},
		"workload missing": {
			wl:     nil,
			wantWl: nil,
		},
		"workload missing from cluter queue": {
			wl: utiltestingapi.MakeWorkload("a", "").ReserveQuotaAt(&kueue.Admission{
				ClusterQueue: "cq",
			}, now).Condition(metav1.Condition{
				Type:   kueue.WorkloadPodsReady,
				Status: metav1.ConditionFalse,
			}).Obj(),
			deleteFromCq: "cq",
			wantWl:       nil,
		},
		"missing local queue": {
			wl: utiltestingapi.MakeWorkload("a", "").Queue("deleted-lq").ReserveQuotaAt(&kueue.Admission{
				ClusterQueue: "cq",
			}, now).Condition(metav1.Condition{
				Type:   kueue.WorkloadPodsReady,
				Status: metav1.ConditionFalse,
			}).Obj(),
			wantWl: utiltestingapi.MakeWorkload("a", "").Queue("deleted-lq").ReserveQuotaAt(&kueue.Admission{
				ClusterQueue: "cq",
			}, now).Condition(metav1.Condition{
				Type:   kueue.WorkloadPodsReady,
				Status: metav1.ConditionFalse,
			}).Obj(),
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			client := utiltesting.NewClientBuilder().Build()
			cache := New(client)
			for _, cq := range clusterQueues {
				if err := cache.AddClusterQueue(ctx, cq); err != nil {
					t.Fatalf("Failed adding clusterQueue %s: %v", cq.Name, err)
				}
			}
			for _, q := range queues {
				if err := cache.AddLocalQueue(q); err != nil {
					t.Fatalf("Failed adding queue %s: %v", q.Name, err)
				}
			}

			wlRef := workload.NewReference("", "non-existent-wl")
			if tc.wl != nil {
				wlRef = workload.Key(tc.wl)
				if !cache.AddOrUpdateWorkload(log, tc.wl) {
					t.Errorf("Failed to add or update workload: %v", tc.wl)
				}
				if tc.deleteFromCq != "" {
					delete(cache.hm.ClusterQueue(tc.deleteFromCq).Workloads, wlRef)
				}
			}

			cache.DeleteClusterQueue(cqToDelete)
			cache.DeleteLocalQueue(lqToDelete)

			gotWl := cache.GetWorkloadFromCache(wlRef)
			if diff := cmp.Diff(tc.wantWl, gotWl, cmpopts.IgnoreFields(metav1.Condition{}, "LastTransitionTime")); diff != "" {
				t.Errorf("GetWorkloadFromCache returned wrong workload (-want,+got):\n%s", diff)
			}
		})
	}
}
