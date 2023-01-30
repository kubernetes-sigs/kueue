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
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/sets"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1alpha2"
	"sigs.k8s.io/kueue/pkg/metrics"
	"sigs.k8s.io/kueue/pkg/util/pointer"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	"sigs.k8s.io/kueue/pkg/workload"
)

func TestCacheClusterQueueOperations(t *testing.T) {
	initialClusterQueues := []kueue.ClusterQueue{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "a"},
			Spec: kueue.ClusterQueueSpec{
				Resources: []kueue.Resource{
					{
						Name: corev1.ResourceCPU,
						Flavors: []kueue.Flavor{{
							Name: "default",
							Quota: kueue.Quota{
								Min: resource.MustParse("10"),
								Max: pointer.Quantity(resource.MustParse("20")),
							},
						}},
					},
				},
				Cohort: "one",
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "b"},
			Spec: kueue.ClusterQueueSpec{
				Resources: []kueue.Resource{
					{
						Name: corev1.ResourceCPU,
						Flavors: []kueue.Flavor{{
							Name: "default",
							Quota: kueue.Quota{
								Min: resource.MustParse("15"),
							},
						}},
					},
				},
				Cohort: "one",
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "c"},
			Spec:       kueue.ClusterQueueSpec{Cohort: "two"},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "d"},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "e"},
			Spec: kueue.ClusterQueueSpec{
				Resources: []kueue.Resource{
					{
						Name: corev1.ResourceCPU,
						Flavors: []kueue.Flavor{{
							Name: "nonexistent-flavor",
							Quota: kueue.Quota{
								Min: resource.MustParse("15"),
							},
						}},
					},
				},
				Cohort: "two",
			},
		},
	}
	setup := func(cache *Cache) {
		cache.AddOrUpdateResourceFlavor(&kueue.ResourceFlavor{
			ObjectMeta: metav1.ObjectMeta{
				Name: "default",
			},
			NodeSelector: map[string]string{"cpuType": "default"},
		})
		for _, c := range initialClusterQueues {
			if err := cache.AddClusterQueue(context.Background(), &c); err != nil {
				t.Fatalf("Failed adding ClusterQueue: %v", err)
			}
		}
	}
	scheme := runtime.NewScheme()
	if err := kueue.AddToScheme(scheme); err != nil {
		t.Fatalf("Failed adding kueue scheme: %v", err)
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
					RequestableResources: map[corev1.ResourceName]*Resource{
						corev1.ResourceCPU: {
							Flavors: []FlavorLimits{{Name: "default", Min: 10000, Max: pointer.Int64(20000)}},
						},
					},
					NamespaceSelector: labels.Nothing(),
					LabelKeys:         map[corev1.ResourceName]sets.Set[string]{corev1.ResourceCPU: sets.New("cpuType")},
					UsedResources:     ResourceQuantities{corev1.ResourceCPU: {"default": 0}},
					Status:            active,
					Preemption:        defaultPreemption,
				},
				"b": {
					Name: "b",
					RequestableResources: map[corev1.ResourceName]*Resource{
						corev1.ResourceCPU: {Flavors: []FlavorLimits{{Name: "default", Min: 15000}}},
					},
					NamespaceSelector: labels.Nothing(),
					UsedResources:     ResourceQuantities{corev1.ResourceCPU: {"default": 0}},
					LabelKeys:         map[corev1.ResourceName]sets.Set[string]{corev1.ResourceCPU: sets.New("cpuType")},
					Status:            active,
					Preemption:        defaultPreemption,
				},
				"c": {
					Name:                 "c",
					RequestableResources: map[corev1.ResourceName]*Resource{},
					NamespaceSelector:    labels.Nothing(),
					UsedResources:        ResourceQuantities{},
					Status:               active,
					Preemption:           defaultPreemption,
				},
				"d": {
					Name:                 "d",
					RequestableResources: map[corev1.ResourceName]*Resource{},
					NamespaceSelector:    labels.Nothing(),
					UsedResources:        ResourceQuantities{},
					Status:               active,
					Preemption:           defaultPreemption,
				},
				"e": {
					Name: "e",
					RequestableResources: map[corev1.ResourceName]*Resource{
						corev1.ResourceCPU: {Flavors: []FlavorLimits{{Name: "nonexistent-flavor", Min: 15000}}},
					},
					NamespaceSelector: labels.Nothing(),
					UsedResources:     ResourceQuantities{corev1.ResourceCPU: {"nonexistent-flavor": 0}},
					LabelKeys:         nil,
					Status:            pending,
					Preemption:        defaultPreemption,
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
				cache.AddOrUpdateResourceFlavor(&kueue.ResourceFlavor{
					ObjectMeta: metav1.ObjectMeta{
						Name: "default",
					},
					NodeSelector: map[string]string{"cpuType": "default"},
				})
			},
			wantClusterQueues: map[string]*ClusterQueue{
				"a": {
					Name: "a",
					RequestableResources: map[corev1.ResourceName]*Resource{
						corev1.ResourceCPU: {Flavors: []FlavorLimits{{Name: "default", Min: 10000, Max: pointer.Int64(20000)}}},
					},
					NamespaceSelector: labels.Nothing(),
					LabelKeys:         map[corev1.ResourceName]sets.Set[string]{corev1.ResourceCPU: sets.New("cpuType")},
					UsedResources:     ResourceQuantities{corev1.ResourceCPU: {"default": 0}},
					Status:            active,
					Preemption:        defaultPreemption,
				},
				"b": {
					Name: "b",
					RequestableResources: map[corev1.ResourceName]*Resource{
						corev1.ResourceCPU: {Flavors: []FlavorLimits{{Name: "default", Min: 15000}}},
					},
					NamespaceSelector: labels.Nothing(),
					UsedResources:     ResourceQuantities{corev1.ResourceCPU: {"default": 0}},
					LabelKeys:         map[corev1.ResourceName]sets.Set[string]{corev1.ResourceCPU: sets.New("cpuType")},
					Status:            active,
					Preemption:        defaultPreemption,
				},
				"c": {
					Name:                 "c",
					RequestableResources: map[corev1.ResourceName]*Resource{},
					NamespaceSelector:    labels.Nothing(),
					UsedResources:        ResourceQuantities{},
					Status:               active,
					Preemption:           defaultPreemption,
				},
				"d": {
					Name:                 "d",
					RequestableResources: map[corev1.ResourceName]*Resource{},
					NamespaceSelector:    labels.Nothing(),
					UsedResources:        ResourceQuantities{},
					Status:               active,
					Preemption:           defaultPreemption,
				},
				"e": {
					Name: "e",
					RequestableResources: map[corev1.ResourceName]*Resource{
						corev1.ResourceCPU: {Flavors: []FlavorLimits{{Name: "nonexistent-flavor", Min: 15000}}},
					},
					NamespaceSelector: labels.Nothing(),
					UsedResources:     ResourceQuantities{corev1.ResourceCPU: {"nonexistent-flavor": 0}},
					LabelKeys:         nil,
					Status:            pending,
					Preemption:        defaultPreemption,
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
					{
						ObjectMeta: metav1.ObjectMeta{Name: "a"},
						Spec: kueue.ClusterQueueSpec{
							Resources: []kueue.Resource{
								{
									Name: corev1.ResourceCPU,
									Flavors: []kueue.Flavor{
										{
											Name: "default",
											Quota: kueue.Quota{
												Min: resource.MustParse("5"),
												Max: pointer.Quantity(resource.MustParse("10")),
											},
										},
									},
								}},
							Cohort: "two",
						},
					},
					{
						ObjectMeta: metav1.ObjectMeta{Name: "b"},
						Spec: kueue.ClusterQueueSpec{
							// remove the only flavor
							Cohort:            "one",                   // No change.
							NamespaceSelector: &metav1.LabelSelector{}, // everything
						},
					},
					{
						ObjectMeta: metav1.ObjectMeta{Name: "e"},
						Spec: kueue.ClusterQueueSpec{
							Resources: []kueue.Resource{
								{
									Name: corev1.ResourceCPU,
									Flavors: []kueue.Flavor{
										{
											Name: "default",
											Quota: kueue.Quota{
												Min: resource.MustParse("5"),
												Max: pointer.Quantity(resource.MustParse("10")),
											},
										},
									},
								}},
							Cohort: "two",
						},
					},
				}
				for _, c := range clusterQueues {
					if err := cache.UpdateClusterQueue(&c); err != nil {
						t.Fatalf("Failed updating ClusterQueue: %v", err)
					}
				}
				cache.AddOrUpdateResourceFlavor(&kueue.ResourceFlavor{
					ObjectMeta: metav1.ObjectMeta{
						Name: "default",
					},
					NodeSelector: map[string]string{"cpuType": "default", "region": "central"},
				})
			},
			wantClusterQueues: map[string]*ClusterQueue{
				"a": {
					Name: "a",
					RequestableResources: map[corev1.ResourceName]*Resource{
						corev1.ResourceCPU: {Flavors: []FlavorLimits{{Name: "default", Min: 5000, Max: pointer.Int64(10000)}}},
					},
					NamespaceSelector: labels.Nothing(),
					LabelKeys:         map[corev1.ResourceName]sets.Set[string]{corev1.ResourceCPU: sets.New("cpuType", "region")},
					UsedResources:     ResourceQuantities{corev1.ResourceCPU: {"default": 0}},
					Status:            active,
					Preemption:        defaultPreemption,
				},
				"b": {
					Name:                 "b",
					RequestableResources: map[corev1.ResourceName]*Resource{},
					NamespaceSelector:    labels.Everything(),
					UsedResources:        ResourceQuantities{},
					Status:               active,
					Preemption:           defaultPreemption,
				},
				"c": {
					Name:                 "c",
					RequestableResources: map[corev1.ResourceName]*Resource{},
					NamespaceSelector:    labels.Nothing(),
					UsedResources:        ResourceQuantities{},
					Status:               active,
					Preemption:           defaultPreemption,
				},
				"d": {
					Name:                 "d",
					RequestableResources: map[corev1.ResourceName]*Resource{},
					NamespaceSelector:    labels.Nothing(),
					UsedResources:        ResourceQuantities{},
					Status:               active,
					Preemption:           defaultPreemption,
				},
				"e": {
					Name: "e",
					RequestableResources: map[corev1.ResourceName]*Resource{
						corev1.ResourceCPU: {Flavors: []FlavorLimits{{Name: "default", Min: 5000, Max: pointer.Int64(10000)}}},
					},
					NamespaceSelector: labels.Nothing(),
					UsedResources:     ResourceQuantities{corev1.ResourceCPU: {"default": 0}},
					LabelKeys:         map[corev1.ResourceName]sets.Set[string]{corev1.ResourceCPU: sets.New("cpuType", "region")},
					Status:            active,
					Preemption:        defaultPreemption,
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
					RequestableResources: map[corev1.ResourceName]*Resource{
						corev1.ResourceCPU: {Flavors: []FlavorLimits{{Name: "default", Min: 15000}}},
					},
					NamespaceSelector: labels.Nothing(),
					UsedResources:     ResourceQuantities{corev1.ResourceCPU: {"default": 0}},
					LabelKeys:         map[corev1.ResourceName]sets.Set[string]{corev1.ResourceCPU: sets.New("cpuType")},
					Status:            active,
					Preemption:        defaultPreemption,
				},
				"c": {
					Name:                 "c",
					RequestableResources: map[corev1.ResourceName]*Resource{},
					NamespaceSelector:    labels.Nothing(),
					UsedResources:        ResourceQuantities{},
					Status:               active,
					Preemption:           defaultPreemption,
				},
				"e": {
					Name: "e",
					RequestableResources: map[corev1.ResourceName]*Resource{
						corev1.ResourceCPU: {Flavors: []FlavorLimits{{Name: "nonexistent-flavor", Min: 15000}}},
					},
					NamespaceSelector: labels.Nothing(),
					UsedResources:     ResourceQuantities{corev1.ResourceCPU: {"nonexistent-flavor": 0}},
					LabelKeys:         nil,
					Status:            pending,
					Preemption:        defaultPreemption,
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
					RequestableResources: map[corev1.ResourceName]*Resource{
						corev1.ResourceCPU: {Flavors: []FlavorLimits{{Name: "default", Min: 10000, Max: pointer.Int64(20000)}}},
					},
					NamespaceSelector: labels.Nothing(),
					LabelKeys:         map[corev1.ResourceName]sets.Set[string]{corev1.ResourceCPU: sets.New("cpuType")},
					UsedResources:     ResourceQuantities{corev1.ResourceCPU: {"default": 0}},
					Status:            active,
					Preemption:        defaultPreemption,
				},
				"b": {
					Name: "b",
					RequestableResources: map[corev1.ResourceName]*Resource{
						corev1.ResourceCPU: {Flavors: []FlavorLimits{{Name: "default", Min: 15000}}},
					},
					NamespaceSelector: labels.Nothing(),
					UsedResources:     ResourceQuantities{corev1.ResourceCPU: {"default": 0}},
					LabelKeys:         map[corev1.ResourceName]sets.Set[string]{corev1.ResourceCPU: sets.New("cpuType")},
					Status:            active,
					Preemption:        defaultPreemption,
				},
				"c": {
					Name:                 "c",
					RequestableResources: map[corev1.ResourceName]*Resource{},
					NamespaceSelector:    labels.Nothing(),
					UsedResources:        ResourceQuantities{},
					Status:               active,
					Preemption:           defaultPreemption,
				},
				"d": {
					Name:                 "d",
					RequestableResources: map[corev1.ResourceName]*Resource{},
					NamespaceSelector:    labels.Nothing(),
					UsedResources:        ResourceQuantities{},
					Status:               active,
					Preemption:           defaultPreemption,
				},
				"e": {
					Name: "e",
					RequestableResources: map[corev1.ResourceName]*Resource{
						corev1.ResourceCPU: {Flavors: []FlavorLimits{{Name: "nonexistent-flavor", Min: 15000}}},
					},
					NamespaceSelector: labels.Nothing(),
					UsedResources:     ResourceQuantities{corev1.ResourceCPU: {"nonexistent-flavor": 0}},
					LabelKeys:         nil,
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
			name: "Add ClusterQueue with codependent resources",
			operation: func(cache *Cache) {
				err := cache.AddClusterQueue(context.Background(),
					&kueue.ClusterQueue{
						ObjectMeta: metav1.ObjectMeta{
							Name: "foo",
						},
						Spec: kueue.ClusterQueueSpec{
							Resources: []kueue.Resource{
								{
									Name: "cpu",
									Flavors: []kueue.Flavor{
										{Name: "foo"},
										{Name: "bar"},
									},
								},
								{
									Name: "memory",
									Flavors: []kueue.Flavor{
										{Name: "foo"},
										{Name: "bar"},
									},
								},
								{
									Name: "example.com/gpu",
									Flavors: []kueue.Flavor{
										{Name: "theta"},
										{Name: "gamma"},
									},
								},
							},
						},
					})
				if err != nil {
					t.Fatalf("Adding ClusterQueue: %v", err)
				}
			},
			wantClusterQueues: map[string]*ClusterQueue{
				"foo": {
					Name:              "foo",
					NamespaceSelector: labels.Nothing(),
					RequestableResources: map[corev1.ResourceName]*Resource{
						"cpu": {
							Flavors: []FlavorLimits{
								{Name: "foo"},
								{Name: "bar"},
							},
							CodependentResources: sets.New[corev1.ResourceName]("cpu", "memory"),
						},
						"memory": {
							Flavors: []FlavorLimits{
								{Name: "foo"},
								{Name: "bar"},
							},
							CodependentResources: sets.New[corev1.ResourceName]("cpu", "memory"),
						},
						"example.com/gpu": {
							Flavors: []FlavorLimits{
								{Name: "theta"},
								{Name: "gamma"},
							},
						},
					},
					UsedResources: ResourceQuantities{
						"cpu": map[string]int64{
							"bar": 0,
							"foo": 0,
						},
						"memory": map[string]int64{
							"bar": 0,
							"foo": 0,
						},
						"example.com/gpu": map[string]int64{
							"theta": 0,
							"gamma": 0,
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
			cache := New(fake.NewClientBuilder().WithScheme(scheme).Build())
			tc.operation(cache)
			if diff := cmp.Diff(tc.wantClusterQueues, cache.clusterQueues,
				cmpopts.IgnoreFields(ClusterQueue{}, "Cohort", "Workloads"),
				cmpopts.IgnoreUnexported(ClusterQueue{}),
				cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("Unexpected clusterQueues (-want,+got):\n%s", diff)
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
		{
			ObjectMeta: metav1.ObjectMeta{Name: "one"},
			Spec: kueue.ClusterQueueSpec{
				Resources: []kueue.Resource{
					{
						Name: "cpu",
						Flavors: []kueue.Flavor{
							{Name: "on-demand"},
							{Name: "spot"},
						},
					},
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "two"},
			Spec: kueue.ClusterQueueSpec{
				Resources: []kueue.Resource{
					{
						Name: "cpu",
						Flavors: []kueue.Flavor{
							{Name: "on-demand"},
							{Name: "spot"},
						},
					},
				},
			},
		},
	}
	podSets := []kueue.PodSet{
		{
			Name: "driver",
			Spec: utiltesting.PodSpecForRequest(map[corev1.ResourceName]string{
				corev1.ResourceCPU:    "10m",
				corev1.ResourceMemory: "512Ki",
			}),
			Count: 1,
		},
		{
			Name: "workers",
			Spec: utiltesting.PodSpecForRequest(
				map[corev1.ResourceName]string{
					corev1.ResourceCPU: "5m",
				}),
			Count: 3,
		},
	}
	podSetFlavors := []kueue.PodSetFlavors{
		{
			Name: "driver",
			Flavors: map[corev1.ResourceName]string{
				corev1.ResourceCPU: "on-demand",
			},
		},
		{
			Name: "workers",
			Flavors: map[corev1.ResourceName]string{
				corev1.ResourceCPU: "spot",
			},
		},
	}
	scheme := runtime.NewScheme()
	if err := kueue.AddToScheme(scheme); err != nil {
		t.Fatalf("Failed adding kueue scheme: %v", err)
	}
	cl := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		utiltesting.MakeWorkload("a", "").PodSets(podSets).Admit(&kueue.Admission{
			ClusterQueue:  "one",
			PodSetFlavors: podSetFlavors,
		}).Obj(),
		utiltesting.MakeWorkload("b", "").Admit(&kueue.Admission{
			ClusterQueue: "one",
		}).Obj(),
		utiltesting.MakeWorkload("c", "").PodSets(podSets).Admit(&kueue.Admission{
			ClusterQueue: "two",
		}).Obj(),
	).Build()

	type result struct {
		Workloads     sets.Set[string]
		UsedResources ResourceQuantities
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
					utiltesting.MakeWorkload("a", "").PodSets(podSets).Admit(&kueue.Admission{
						ClusterQueue:  "one",
						PodSetFlavors: podSetFlavors,
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
					Workloads:     sets.New("/a", "/b"),
					UsedResources: ResourceQuantities{"cpu": {"on-demand": 10, "spot": 15}},
				},
				"two": {
					Workloads:     sets.New("/c", "/d"),
					UsedResources: ResourceQuantities{"cpu": {"on-demand": 0, "spot": 0}},
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
					Workloads:     sets.New("/a", "/b"),
					UsedResources: ResourceQuantities{"cpu": {"on-demand": 10, "spot": 15}},
				},
				"two": {
					Workloads:     sets.New("/c"),
					UsedResources: ResourceQuantities{"cpu": {"on-demand": 0, "spot": 0}},
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
					Workloads:     sets.New("/a", "/b"),
					UsedResources: ResourceQuantities{"cpu": {"on-demand": 10, "spot": 15}},
				},
				"two": {
					Workloads:     sets.New("/c"),
					UsedResources: ResourceQuantities{"cpu": {"on-demand": 0, "spot": 0}},
				},
			},
		},
		{
			name: "update",
			operation: func(cache *Cache) error {
				old := utiltesting.MakeWorkload("a", "").Admit(&kueue.Admission{
					ClusterQueue: "one",
				}).Obj()
				latest := utiltesting.MakeWorkload("a", "").PodSets(podSets).Admit(&kueue.Admission{
					ClusterQueue:  "two",
					PodSetFlavors: podSetFlavors,
				}).Obj()
				return cache.UpdateWorkload(old, latest)
			},
			wantResults: map[string]result{
				"one": {
					Workloads:     sets.New("/b"),
					UsedResources: ResourceQuantities{"cpu": {"on-demand": 0, "spot": 0}},
				},
				"two": {
					Workloads:     sets.New("/a", "/c"),
					UsedResources: ResourceQuantities{"cpu": {"on-demand": 10, "spot": 15}},
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
					Workloads:     sets.New("/a", "/b"),
					UsedResources: ResourceQuantities{"cpu": {"on-demand": 10, "spot": 15}},
				},
				"two": {
					Workloads:     sets.New("/c"),
					UsedResources: ResourceQuantities{"cpu": {"on-demand": 0, "spot": 0}},
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
					Workloads:     sets.New("/a", "/b"),
					UsedResources: ResourceQuantities{"cpu": {"on-demand": 10, "spot": 15}},
				},
				"two": {
					Workloads:     sets.New("/c"),
					UsedResources: ResourceQuantities{"cpu": {"on-demand": 0, "spot": 0}},
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
					Workloads:     sets.New("/a", "/b"),
					UsedResources: ResourceQuantities{"cpu": {"on-demand": 10, "spot": 15}},
				},
				"two": {
					Workloads:     sets.New("/c", "/d"),
					UsedResources: ResourceQuantities{"cpu": {"on-demand": 0, "spot": 0}},
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
					Workloads:     sets.New("/b"),
					UsedResources: ResourceQuantities{"cpu": {"on-demand": 0, "spot": 0}},
				},
				"two": {
					Workloads:     sets.New("/c"),
					UsedResources: ResourceQuantities{"cpu": {"on-demand": 0, "spot": 0}},
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
					Workloads:     sets.New("/b"),
					UsedResources: ResourceQuantities{"cpu": {"on-demand": 0, "spot": 0}},
				},
				"two": {
					Workloads:     sets.New("/c"),
					UsedResources: ResourceQuantities{"cpu": {"on-demand": 0, "spot": 0}},
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
					Workloads:     sets.New("/a", "/b"),
					UsedResources: ResourceQuantities{"cpu": {"on-demand": 10, "spot": 15}},
				},
				"two": {
					Workloads:     sets.New("/c"),
					UsedResources: ResourceQuantities{"cpu": {"on-demand": 0, "spot": 0}},
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
					Workloads:     sets.New("/a", "/b"),
					UsedResources: ResourceQuantities{"cpu": {"on-demand": 10, "spot": 15}},
				},
				"two": {
					Workloads:     sets.New("/c"),
					UsedResources: ResourceQuantities{"cpu": {"on-demand": 0, "spot": 0}},
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
					Workloads:     sets.New("/a", "/b"),
					UsedResources: ResourceQuantities{"cpu": {"on-demand": 10, "spot": 15}},
				},
				"two": {
					Workloads:     sets.New("/c"),
					UsedResources: ResourceQuantities{"cpu": {"on-demand": 0, "spot": 0}},
				},
			},
		},
		{
			name: "assume",
			operation: func(cache *Cache) error {
				workloads := []*kueue.Workload{
					utiltesting.MakeWorkload("d", "").PodSets(podSets).Admit(&kueue.Admission{
						ClusterQueue:  "one",
						PodSetFlavors: podSetFlavors,
					}).Obj(),
					utiltesting.MakeWorkload("e", "").PodSets(podSets).Admit(&kueue.Admission{
						ClusterQueue:  "two",
						PodSetFlavors: podSetFlavors,
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
					Workloads:     sets.New("/a", "/b", "/d"),
					UsedResources: ResourceQuantities{"cpu": {"on-demand": 20, "spot": 30}},
				},
				"two": {
					Workloads:     sets.New("/c", "/e"),
					UsedResources: ResourceQuantities{"cpu": {"on-demand": 10, "spot": 15}},
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
				w := utiltesting.MakeWorkload("d", "").PodSets(podSets).Admit(&kueue.Admission{
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
					Workloads:     sets.New("/a", "/b"),
					UsedResources: ResourceQuantities{"cpu": {"on-demand": 10, "spot": 15}},
				},
				"two": {
					Workloads:     sets.New("/c"),
					UsedResources: ResourceQuantities{"cpu": {"on-demand": 0, "spot": 0}},
				},
			},
			wantAssumedWorkloads: map[string]string{},
		},
		{
			name: "forget",
			operation: func(cache *Cache) error {
				workloads := []*kueue.Workload{
					utiltesting.MakeWorkload("d", "").PodSets(podSets).Admit(&kueue.Admission{
						ClusterQueue:  "one",
						PodSetFlavors: podSetFlavors,
					}).Obj(),
					utiltesting.MakeWorkload("e", "").PodSets(podSets).Admit(&kueue.Admission{
						ClusterQueue:  "two",
						PodSetFlavors: podSetFlavors,
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
					Workloads:     sets.New("/a", "/b"),
					UsedResources: ResourceQuantities{"cpu": {"on-demand": 10, "spot": 15}},
				},
				"two": {
					Workloads:     sets.New("/c", "/e"),
					UsedResources: ResourceQuantities{"cpu": {"on-demand": 10, "spot": 15}},
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
					Workloads:     sets.New("/a", "/b"),
					UsedResources: ResourceQuantities{"cpu": {"on-demand": 10, "spot": 15}},
				},
				"two": {
					Workloads:     sets.New("/c"),
					UsedResources: ResourceQuantities{"cpu": {"on-demand": 0, "spot": 0}},
				},
			},
		},
		{
			name: "add assumed workload",
			operation: func(cache *Cache) error {
				workloads := []*kueue.Workload{
					utiltesting.MakeWorkload("d", "").PodSets(podSets).Admit(&kueue.Admission{
						ClusterQueue:  "one",
						PodSetFlavors: podSetFlavors,
					}).Obj(),
					utiltesting.MakeWorkload("e", "").PodSets(podSets).Admit(&kueue.Admission{
						ClusterQueue:  "two",
						PodSetFlavors: podSetFlavors,
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
					Workloads:     sets.New("/a", "/b", "/d"),
					UsedResources: ResourceQuantities{"cpu": {"on-demand": 20, "spot": 30}},
				},
				"two": {
					Workloads:     sets.New("/c", "/e"),
					UsedResources: ResourceQuantities{"cpu": {"on-demand": 10, "spot": 15}},
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
				gotWorkloads[name] = result{Workloads: sets.KeySet(cq.Workloads), UsedResources: cq.UsedResources}
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
	cq := kueue.ClusterQueue{
		ObjectMeta: metav1.ObjectMeta{Name: "foo"},
		Spec: kueue.ClusterQueueSpec{
			Resources: []kueue.Resource{
				{
					Name: corev1.ResourceCPU,
					Flavors: []kueue.Flavor{
						{
							Name: "default",
							Quota: kueue.Quota{
								Min: resource.MustParse("10"),
								Max: pointer.Quantity(resource.MustParse("20")),
							},
						},
					},
				},
				{
					Name: "example.com/gpu",
					Flavors: []kueue.Flavor{
						{
							Name: "model_a",
							Quota: kueue.Quota{
								Min: resource.MustParse("5"),
								Max: pointer.Quantity(resource.MustParse("10")),
							},
						},
						{
							Name: "model_b",
							Quota: kueue.Quota{
								Min: resource.MustParse("5"),
								// No max.
							},
						},
					},
				},
			},
		},
	}
	workloads := []kueue.Workload{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "one"},
			Spec: kueue.WorkloadSpec{
				PodSets: []kueue.PodSet{
					{
						Name:  "main",
						Count: 1,
						Spec: utiltesting.PodSpecForRequest(map[corev1.ResourceName]string{
							corev1.ResourceCPU: "8",
							"example.com/gpu":  "5",
						}),
					},
				},
				Admission: &kueue.Admission{
					ClusterQueue: "foo",
					PodSetFlavors: []kueue.PodSetFlavors{
						{
							Name: "main",
							Flavors: map[corev1.ResourceName]string{
								corev1.ResourceCPU: "default",
								"example.com/gpu":  "model_a",
							},
						},
					},
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "two"},
			Spec: kueue.WorkloadSpec{
				PodSets: []kueue.PodSet{
					{
						Name:  "main",
						Count: 1,
						Spec: utiltesting.PodSpecForRequest(map[corev1.ResourceName]string{
							corev1.ResourceCPU: "5",
							"example.com/gpu":  "6",
						}),
					},
				},
				Admission: &kueue.Admission{
					ClusterQueue: "foo",
					PodSetFlavors: []kueue.PodSetFlavors{
						{
							Name: "main",
							Flavors: map[corev1.ResourceName]string{
								corev1.ResourceCPU: "default",
								"example.com/gpu":  "model_b",
							},
						},
					},
				},
			},
		},
	}
	cases := map[string]struct {
		workloads         []kueue.Workload
		wantUsedResources kueue.UsedResources
		wantWorkloads     int
	}{
		"single no borrowing": {
			workloads: workloads[:1],
			wantUsedResources: kueue.UsedResources{
				corev1.ResourceCPU: {
					"default": kueue.Usage{
						Total: pointer.Quantity(resource.MustParse("8")),
					},
				},
				"example.com/gpu": {
					"model_a": kueue.Usage{
						Total: pointer.Quantity(resource.MustParse("5")),
					},
					"model_b": kueue.Usage{
						Total: pointer.Quantity(resource.MustParse("0")),
					},
				},
			},
			wantWorkloads: 1,
		},
		"multiple borrowing": {
			workloads: workloads,
			wantUsedResources: kueue.UsedResources{
				corev1.ResourceCPU: {
					"default": kueue.Usage{
						Total:    pointer.Quantity(resource.MustParse("13")),
						Borrowed: pointer.Quantity(resource.MustParse("3")),
					},
				},
				"example.com/gpu": {
					"model_a": kueue.Usage{
						Total: pointer.Quantity(resource.MustParse("5")),
					},
					"model_b": kueue.Usage{
						Total:    pointer.Quantity(resource.MustParse("6")),
						Borrowed: pointer.Quantity(resource.MustParse("1")),
					},
				},
			},
			wantWorkloads: 2,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			if err := kueue.AddToScheme(scheme); err != nil {
				t.Fatalf("Failed adding kueue scheme: %v", err)
			}
			cache := New(fake.NewClientBuilder().WithScheme(scheme).Build())
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
			scheme := runtime.NewScheme()
			if err := kueue.AddToScheme(scheme); err != nil {
				t.Fatalf("Failed adding kueue scheme: %v", err)
			}
			cl := fake.NewClientBuilder().WithScheme(scheme).Build()
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
	x86Flavor := utiltesting.MakeFlavor(x86Rf.Name, "5").Obj()
	aarch64Flavor := utiltesting.MakeFlavor(aarch64Rf.Name, "3").Obj()
	fooCq := utiltesting.MakeClusterQueue("fooCq").
		Resource(utiltesting.MakeResource("cpu").Flavor(x86Flavor).Obj()).
		Obj()
	barCq := utiltesting.MakeClusterQueue("barCq").Obj()
	fizzCq := utiltesting.MakeClusterQueue("fizzCq").
		Resource(utiltesting.MakeResource("cpu").
			Flavor(x86Flavor).
			Flavor(aarch64Flavor).Obj()).
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
			ctx := context.Background()
			scheme := runtime.NewScheme()
			if err := kueue.AddToScheme(scheme); err != nil {
				t.Fatalf("Failed adding kueue scheme: %v", err)
			}
			cache := New(fake.NewClientBuilder().WithScheme(scheme).Build())
			cache.AddOrUpdateResourceFlavor(x86Rf)
			cache.AddOrUpdateResourceFlavor(aarch64Rf)
			for _, cq := range tc.clusterQueues {
				if err := cache.AddClusterQueue(ctx, cq); err != nil {
					t.Errorf("failed to add clusterQueue %s", cq.Name)
				}
			}

			cqs := cache.ClusterQueuesUsingFlavor(string(x86Flavor.Name))
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
	flavor := utiltesting.MakeFlavor(rf.Name, "5").Obj()

	testcases := []struct {
		name         string
		curStatus    metrics.ClusterQueueStatus
		clusterQueue *kueue.ClusterQueue
		flavors      map[string]*kueue.ResourceFlavor
		wantStatus   metrics.ClusterQueueStatus
	}{
		{
			name:      "Pending clusterQueue updated existent flavors",
			curStatus: pending,
			clusterQueue: utiltesting.MakeClusterQueue("cq").
				Resource(utiltesting.MakeResource("cpu").Flavor(flavor).Obj()).
				Obj(),
			flavors: map[string]*kueue.ResourceFlavor{
				rf.Name: rf,
			},
			wantStatus: active,
		},
		{
			name:      "Active clusterQueue updated with not found flavors",
			curStatus: active,
			clusterQueue: utiltesting.MakeClusterQueue("cq").
				Resource(utiltesting.MakeResource("cpu").Flavor(flavor).Obj()).
				Obj(),
			flavors:    map[string]*kueue.ResourceFlavor{},
			wantStatus: pending,
		},
		{
			name:      "Terminating clusterQueue updated with existent flavors",
			curStatus: terminating,
			clusterQueue: utiltesting.MakeClusterQueue("cq").
				Resource(utiltesting.MakeResource("cpu").Flavor(flavor).Obj()).
				Obj(),
			flavors: map[string]*kueue.ResourceFlavor{
				rf.Name: rf,
			},
			wantStatus: terminating,
		},
		{
			name:      "Terminating clusterQueue updated with not found flavors",
			curStatus: terminating,
			clusterQueue: utiltesting.MakeClusterQueue("cq").
				Resource(utiltesting.MakeResource("cpu").Flavor(flavor).Obj()).
				Obj(),
			flavors:    map[string]*kueue.ResourceFlavor{},
			wantStatus: terminating,
		},
	}

	for _, tc := range testcases {
		t.Run(tc.name, func(t *testing.T) {
			scheme := runtime.NewScheme()
			if err := kueue.AddToScheme(scheme); err != nil {
				t.Fatalf("Failed adding kueue scheme: %v", err)
			}
			cache := New(fake.NewClientBuilder().WithScheme(scheme).Build())
			cq, err := cache.newClusterQueue(tc.clusterQueue)
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

func TestClusterQueueUpdateCodependentResources(t *testing.T) {
	cases := map[string]struct {
		cq     ClusterQueue
		wantCQ ClusterQueue
	}{
		"no codependent resources": {
			cq: ClusterQueue{
				RequestableResources: map[corev1.ResourceName]*Resource{
					"cpu": {
						Flavors: []FlavorLimits{
							{Name: "alpha"},
							{Name: "beta"},
						},
					},
					"memory": {
						Flavors: []FlavorLimits{
							{Name: "default"},
						},
					},
					"example.com/gpu": {
						Flavors: []FlavorLimits{
							{Name: "gamma"},
							{Name: "theta"},
						},
					},
				},
			},
			wantCQ: ClusterQueue{
				RequestableResources: map[corev1.ResourceName]*Resource{
					"cpu": {
						Flavors: []FlavorLimits{
							{Name: "alpha"},
							{Name: "beta"},
						},
					},
					"memory": {
						Flavors: []FlavorLimits{
							{Name: "default"},
						},
					},
					"example.com/gpu": {
						Flavors: []FlavorLimits{
							{Name: "gamma"},
							{Name: "theta"},
						},
					},
				},
			},
		},
		"some codependent resources": {
			cq: ClusterQueue{
				RequestableResources: map[corev1.ResourceName]*Resource{
					"cpu": {
						Flavors: []FlavorLimits{
							{Name: "alpha"},
							{Name: "beta"},
						},
					},
					"memory": {
						Flavors: []FlavorLimits{
							{Name: "alpha"},
							{Name: "beta"},
						},
					},
					"example.com/gpu": {
						Flavors: []FlavorLimits{
							{Name: "gamma"},
							{Name: "theta"},
						},
					},
					"example.com/foo": {
						Flavors: []FlavorLimits{
							{Name: "default"},
						},
					},
					"example.com/bar": {
						Flavors: []FlavorLimits{
							{Name: "default"},
						},
					},
				},
			},
			wantCQ: ClusterQueue{
				RequestableResources: map[corev1.ResourceName]*Resource{
					"cpu": {
						Flavors: []FlavorLimits{
							{Name: "alpha"},
							{Name: "beta"},
						},
						CodependentResources: sets.New[corev1.ResourceName]("cpu", "memory"),
					},
					"memory": {
						Flavors: []FlavorLimits{
							{Name: "alpha"},
							{Name: "beta"},
						},
						CodependentResources: sets.New[corev1.ResourceName]("cpu", "memory"),
					},
					"example.com/gpu": {
						Flavors: []FlavorLimits{
							{Name: "gamma"},
							{Name: "theta"},
						},
					},
					"example.com/foo": {
						Flavors: []FlavorLimits{
							{Name: "default"},
						},
						CodependentResources: sets.New[corev1.ResourceName]("example.com/foo", "example.com/bar"),
					},
					"example.com/bar": {
						Flavors: []FlavorLimits{
							{Name: "default"},
						},
						CodependentResources: sets.New[corev1.ResourceName]("example.com/foo", "example.com/bar"),
					},
				},
			},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			tc.cq.UpdateCodependentResources()
			if diff := cmp.Diff(tc.wantCQ, tc.cq, cmpopts.IgnoreUnexported(ClusterQueue{})); diff != "" {
				t.Errorf("Unexpected ClusterQueue after updating codependent resources: (-want,+got):\n%s", diff)
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

	ctx := context.Background()
	scheme := runtime.NewScheme()
	if err := kueue.AddToScheme(scheme); err != nil {
		t.Fatalf("Failed adding kueue scheme: %v", err)
	}
	cache := New(fake.NewClientBuilder().WithScheme(scheme).Build())
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
	scheme := runtime.NewScheme()
	if err := kueue.AddToScheme(scheme); err != nil {
		t.Fatalf("Failed adding kueue scheme: %v", err)
	}
	cl := fake.NewClientBuilder().WithScheme(scheme).Build()

	cache := New(cl, WithPodsReadyTracking(true))
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

	scheme := runtime.NewScheme()
	if err := kueue.AddToScheme(scheme); err != nil {
		t.Fatalf("Failed adding kueue scheme: %v", err)
	}
	cl := fake.NewClientBuilder().WithScheme(scheme).Build()

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
