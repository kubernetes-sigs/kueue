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
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/sets"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1alpha2"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	"sigs.k8s.io/kueue/pkg/workload"
)

var snapCmpOpts = []cmp.Option{
	cmpopts.IgnoreUnexported(ClusterQueue{}),
	cmpopts.IgnoreFields(Cohort{}, "Members"), // avoid recursion.
}

func TestSnapshot(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := kueue.AddToScheme(scheme); err != nil {
		t.Fatalf("Failed adding kueue scheme: %s", err)
	}
	cache := New(fake.NewClientBuilder().WithScheme(scheme).Build())
	clusterQueues := []kueue.ClusterQueue{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: "foofoo",
			},
			Spec: kueue.ClusterQueueSpec{
				Cohort: "foo",
				Resources: []kueue.Resource{
					{
						Name: corev1.ResourceCPU,
						Flavors: []kueue.Flavor{
							{
								Name: "demand",
								Quota: kueue.Quota{
									Min: resource.MustParse("100"),
								},
							},
							{
								Name: "spot",
								Quota: kueue.Quota{
									Min: resource.MustParse("200"),
								},
							},
						},
					},
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: "foobar",
			},
			Spec: kueue.ClusterQueueSpec{
				Cohort: "foo",
				Resources: []kueue.Resource{
					{
						Name: corev1.ResourceCPU,
						Flavors: []kueue.Flavor{
							{
								Name: "spot",
								Quota: kueue.Quota{
									Min: resource.MustParse("100"),
								},
							},
						},
					},
					{
						Name: "example.com/gpu",
						Flavors: []kueue.Flavor{
							{
								Name: "default",
								Quota: kueue.Quota{
									Min: resource.MustParse("50"),
								},
							},
						},
					},
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: "flavor-nonexistent-cq",
			},
			Spec: kueue.ClusterQueueSpec{
				Cohort: "foo",
				Resources: []kueue.Resource{
					{
						Name: corev1.ResourceCPU,
						Flavors: []kueue.Flavor{
							{
								Name: "nonexistent-flavor",
								Quota: kueue.Quota{
									Min: resource.MustParse("100"),
								},
							},
						},
					},
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: "bar",
			},
			Spec: kueue.ClusterQueueSpec{
				Resources: []kueue.Resource{
					{
						Name: corev1.ResourceCPU,
						Flavors: []kueue.Flavor{
							{
								Name: "default",
								Quota: kueue.Quota{
									Min: resource.MustParse("100"),
								},
							},
						},
					},
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: "with-preemption",
			},
			Spec: kueue.ClusterQueueSpec{
				Preemption: &kueue.ClusterQueuePreemption{
					ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
				},
			},
		},
	}
	for _, c := range clusterQueues {
		// Purposely do not make a copy of clusterQueues. Clones of necessary fields are
		// done in AddClusterQueue.
		if err := cache.AddClusterQueue(context.Background(), &c); err != nil {
			t.Fatalf("Failed adding ClusterQueue: %v", err)
		}
	}
	flavors := []kueue.ResourceFlavor{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: "demand",
			},
			NodeSelector: map[string]string{"foo": "bar", "instance": "demand"},
		},
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: "spot",
			},
			NodeSelector: map[string]string{"baz": "bar", "instance": "spot"},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "default"},
		},
	}

	for i := range flavors {
		cache.AddOrUpdateResourceFlavor(&flavors[i])
	}
	workloads := []kueue.Workload{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "alpha"},
			Spec: kueue.WorkloadSpec{
				PodSets: []kueue.PodSet{
					{
						Name:  "main",
						Count: 5,
						Spec: utiltesting.PodSpecForRequest(map[corev1.ResourceName]string{
							corev1.ResourceCPU: "2",
						}),
					},
				},
				Admission: &kueue.Admission{
					ClusterQueue: "foofoo",
					PodSetFlavors: []kueue.PodSetFlavors{
						{
							Name: "main",
							Flavors: map[corev1.ResourceName]string{
								corev1.ResourceCPU: "demand",
							},
						},
					},
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "beta"},
			Spec: kueue.WorkloadSpec{
				PodSets: []kueue.PodSet{
					{
						Name:  "main",
						Count: 5,
						Spec: utiltesting.PodSpecForRequest(map[corev1.ResourceName]string{
							corev1.ResourceCPU: "1",
							"example.com/gpu":  "2",
						}),
					},
				},
				Admission: &kueue.Admission{
					ClusterQueue: "foobar",
					PodSetFlavors: []kueue.PodSetFlavors{
						{
							Name: "main",
							Flavors: map[corev1.ResourceName]string{
								corev1.ResourceCPU: "spot",
								"example.com/gpu":  "default",
							},
						},
					},
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "gamma"},
			Spec: kueue.WorkloadSpec{
				PodSets: []kueue.PodSet{
					{
						Name:  "main",
						Count: 5,
						Spec: utiltesting.PodSpecForRequest(map[corev1.ResourceName]string{
							corev1.ResourceCPU: "1",
							"example.com/gpu":  "1",
						}),
					},
				},
				Admission: &kueue.Admission{
					ClusterQueue: "foobar",
					PodSetFlavors: []kueue.PodSetFlavors{
						{
							Name: "main",
							Flavors: map[corev1.ResourceName]string{
								corev1.ResourceCPU: "spot",
								"example.com/gpu":  "default",
							},
						},
					},
				},
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "sigma"},
			Spec: kueue.WorkloadSpec{
				PodSets: []kueue.PodSet{
					{
						Name:  "main",
						Count: 5,
						Spec: utiltesting.PodSpecForRequest(map[corev1.ResourceName]string{
							corev1.ResourceCPU: "1",
						}),
					},
				},
				Admission: nil,
			},
		},
	}
	for _, w := range workloads {
		cache.AddOrUpdateWorkload(w.DeepCopy())
	}
	snapshot := cache.Snapshot()
	wantCohort := Cohort{
		Name: "foo",
		RequestableResources: ResourceQuantities{
			corev1.ResourceCPU: map[string]int64{
				"demand": 100_000,
				"spot":   300_000,
			},
			"example.com/gpu": map[string]int64{
				"default": 50,
			},
		},
		UsedResources: ResourceQuantities{
			corev1.ResourceCPU: map[string]int64{
				"demand": 10_000,
				"spot":   10_000,
			},
			"example.com/gpu": map[string]int64{
				"default": 15,
			},
		},
	}
	wantSnapshot := Snapshot{
		ClusterQueues: map[string]*ClusterQueue{
			"foofoo": {
				Name:   "foofoo",
				Cohort: &wantCohort,
				RequestableResources: map[corev1.ResourceName]*Resource{
					corev1.ResourceCPU: {
						Flavors: []FlavorLimits{
							{
								Name: "demand",
								Min:  100_000,
							},
							{
								Name: "spot",
								Min:  200_000,
							},
						},
					},
				},
				UsedResources: ResourceQuantities{
					corev1.ResourceCPU: map[string]int64{
						"demand": 10_000,
						"spot":   0,
					},
				},
				Workloads: map[string]*workload.Info{
					"/alpha": workload.NewInfo(&workloads[0]),
				},
				Preemption:        defaultPreemption,
				LabelKeys:         map[corev1.ResourceName]sets.Set[string]{corev1.ResourceCPU: sets.New("baz", "foo", "instance")},
				NamespaceSelector: labels.Nothing(),
				Status:            active,
			},
			"foobar": {
				Name:   "foobar",
				Cohort: &wantCohort,
				RequestableResources: map[corev1.ResourceName]*Resource{
					corev1.ResourceCPU: {
						Flavors: []FlavorLimits{
							{
								Name: "spot",
								Min:  100_000,
							},
						},
					},
					"example.com/gpu": {
						Flavors: []FlavorLimits{
							{
								Name: "default",
								Min:  50,
							},
						},
					},
				},
				UsedResources: ResourceQuantities{
					corev1.ResourceCPU: map[string]int64{
						"spot": 10_000,
					},
					"example.com/gpu": map[string]int64{
						"default": 15,
					},
				},
				Workloads: map[string]*workload.Info{
					"/beta":  workload.NewInfo(&workloads[1]),
					"/gamma": workload.NewInfo(&workloads[2]),
				},
				Preemption:        defaultPreemption,
				NamespaceSelector: labels.Nothing(),
				LabelKeys:         map[corev1.ResourceName]sets.Set[string]{corev1.ResourceCPU: sets.New("baz", "instance")},
				Status:            active,
			},
			"bar": {
				Name: "bar",
				RequestableResources: map[corev1.ResourceName]*Resource{
					corev1.ResourceCPU: {
						Flavors: []FlavorLimits{
							{
								Name: "default",
								Min:  100_000,
							},
						},
					},
				},
				UsedResources: ResourceQuantities{
					corev1.ResourceCPU: map[string]int64{"default": 0},
				},
				Workloads:         map[string]*workload.Info{},
				Preemption:        defaultPreemption,
				NamespaceSelector: labels.Nothing(),
				Status:            active,
			},
			"with-preemption": {
				Name:                 "with-preemption",
				RequestableResources: map[corev1.ResourceName]*Resource{},
				UsedResources:        ResourceQuantities{},
				Workloads:            map[string]*workload.Info{},
				Preemption: kueue.ClusterQueuePreemption{
					ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
				},
				NamespaceSelector: labels.Nothing(),
				Status:            active,
			},
		},
		ResourceFlavors: map[string]*kueue.ResourceFlavor{
			"default": {
				ObjectMeta: metav1.ObjectMeta{Name: "default"},
			},
			"demand": {
				ObjectMeta: metav1.ObjectMeta{
					Name: "demand",
				},
				NodeSelector: map[string]string{"foo": "bar", "instance": "demand"},
			},
			"spot": {
				ObjectMeta: metav1.ObjectMeta{
					Name: "spot",
				},
				NodeSelector: map[string]string{"baz": "bar", "instance": "spot"},
			},
		},
		InactiveClusterQueueSets: sets.New("flavor-nonexistent-cq"),
	}
	if diff := cmp.Diff(wantSnapshot, snapshot, snapCmpOpts...); diff != "" {
		t.Errorf("Unexpected Snapshot (-want,+got):\n%s", diff)
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
			Resource(utiltesting.MakeResource(corev1.ResourceCPU).
				Flavor(utiltesting.MakeFlavor("default", "6").Obj()).
				Obj()).
			Resource(utiltesting.MakeResource(corev1.ResourceMemory).
				Flavor(utiltesting.MakeFlavor("alpha", "6Gi").Obj()).
				Flavor(utiltesting.MakeFlavor("beta", "6Gi").Obj()).
				Obj()).
			Obj(),
		utiltesting.MakeClusterQueue("c2").
			Cohort("cohort").
			Resource(utiltesting.MakeResource(corev1.ResourceCPU).
				Flavor(utiltesting.MakeFlavor("default", "6").Obj()).
				Obj()).
			Obj(),
	}
	workloads := []kueue.Workload{
		*utiltesting.MakeWorkload("c1-cpu", "").
			Request(corev1.ResourceCPU, "1").
			Admit(utiltesting.MakeAdmission("c1").Flavor(corev1.ResourceCPU, "default").Obj()).
			Obj(),
		*utiltesting.MakeWorkload("c1-memory-alpha", "").
			Request(corev1.ResourceMemory, "1Gi").
			Admit(utiltesting.MakeAdmission("c1").Flavor(corev1.ResourceMemory, "alpha").Obj()).
			Obj(),
		*utiltesting.MakeWorkload("c1-memory-beta", "").
			Request(corev1.ResourceMemory, "1Gi").
			Admit(utiltesting.MakeAdmission("c1").Flavor(corev1.ResourceMemory, "beta").Obj()).
			Obj(),
		*utiltesting.MakeWorkload("c2-cpu-1", "").
			Request(corev1.ResourceCPU, "1").
			Admit(utiltesting.MakeAdmission("c2").Flavor(corev1.ResourceCPU, "default").Obj()).
			Obj(),
		*utiltesting.MakeWorkload("c2-cpu-2", "").
			Request(corev1.ResourceCPU, "1").
			Admit(utiltesting.MakeAdmission("c2").Flavor(corev1.ResourceCPU, "default").Obj()).
			Obj(),
	}
	ctx := context.Background()
	scheme := utiltesting.MustGetScheme(t)
	cl := fake.NewClientBuilder().
		WithScheme(scheme).
		WithLists(&kueue.WorkloadList{Items: workloads}).
		Build()

	cqCache := New(cl)
	for _, flv := range flavors {
		cqCache.AddOrUpdateResourceFlavor(flv)
	}
	for _, cq := range clusterQueues {
		if err := cqCache.AddClusterQueue(ctx, cq); err != nil {
			t.Fatalf("Couldn't add ClusterQueue to cache: %v", err)
		}
	}
	wlInfos := make(map[string]*workload.Info, len(workloads))
	for _, cq := range cqCache.clusterQueues {
		for _, wl := range cq.Workloads {
			wlInfos[workload.Key(wl.Obj)] = wl
		}
	}
	initialSnapshot := cqCache.Snapshot()
	initialCohortResources := initialSnapshot.ClusterQueues["c1"].Cohort.RequestableResources
	cases := map[string]struct {
		remove []string
		add    []string
		want   Snapshot
	}{
		"no-op remove add": {
			remove: []string{"/c1-cpu", "/c2-cpu-1"},
			add:    []string{"/c1-cpu", "/c2-cpu-1"},
			want:   initialSnapshot,
		},
		"remove all": {
			remove: []string{"/c1-cpu", "/c1-memory-alpha", "/c1-memory-beta", "/c2-cpu-1", "/c2-cpu-2"},
			want: func() Snapshot {
				cohort := &Cohort{
					Name:                 "cohort",
					RequestableResources: initialCohortResources,
					UsedResources: ResourceQuantities{
						corev1.ResourceCPU:    {"default": 0},
						corev1.ResourceMemory: {"alpha": 0, "beta": 0},
					},
				}
				return Snapshot{
					ClusterQueues: map[string]*ClusterQueue{
						"c1": {
							Name:                 "c1",
							Cohort:               cohort,
							Workloads:            make(map[string]*workload.Info),
							RequestableResources: cqCache.clusterQueues["c1"].RequestableResources,
							UsedResources: ResourceQuantities{
								corev1.ResourceCPU:    {"default": 0},
								corev1.ResourceMemory: {"alpha": 0, "beta": 0},
							},
						},
						"c2": {
							Name:                 "c2",
							Cohort:               cohort,
							Workloads:            make(map[string]*workload.Info),
							RequestableResources: cqCache.clusterQueues["c2"].RequestableResources,
							UsedResources: ResourceQuantities{
								corev1.ResourceCPU: {"default": 0},
							},
						},
					},
				}
			}(),
		},
		"remove c1-cpu": {
			remove: []string{"/c1-cpu"},
			want: func() Snapshot {
				cohort := &Cohort{
					Name:                 "cohort",
					RequestableResources: initialCohortResources,
					UsedResources: ResourceQuantities{
						corev1.ResourceCPU: {"default": 2_000},
						corev1.ResourceMemory: {
							"alpha": 1 * utiltesting.Gi,
							"beta":  1 * utiltesting.Gi,
						},
					},
				}
				return Snapshot{
					ClusterQueues: map[string]*ClusterQueue{
						"c1": {
							Name:   "c1",
							Cohort: cohort,
							Workloads: map[string]*workload.Info{
								"/c1-memory-alpha": nil,
								"/c1-memory-beta":  nil,
							},
							RequestableResources: cqCache.clusterQueues["c1"].RequestableResources,
							UsedResources: ResourceQuantities{
								corev1.ResourceCPU: {"default": 0},
								corev1.ResourceMemory: {
									"alpha": 1 * utiltesting.Gi,
									"beta":  1 * utiltesting.Gi,
								},
							},
						},
						"c2": {
							Name:   "c2",
							Cohort: cohort,
							Workloads: map[string]*workload.Info{
								"/c2-cpu-1": nil,
								"/c2-cpu-2": nil,
							},
							RequestableResources: cqCache.clusterQueues["c2"].RequestableResources,
							UsedResources: ResourceQuantities{
								corev1.ResourceCPU: {"default": 2_000},
							},
						},
					},
				}
			}(),
		},
		"remove c1-memory-alpha": {
			remove: []string{"/c1-memory-alpha"},
			want: func() Snapshot {
				cohort := &Cohort{
					Name:                 "cohort",
					RequestableResources: initialCohortResources,
					UsedResources: ResourceQuantities{
						corev1.ResourceCPU: {"default": 3_000},
						corev1.ResourceMemory: {
							"alpha": 0,
							"beta":  1 * utiltesting.Gi,
						},
					},
				}
				return Snapshot{
					ClusterQueues: map[string]*ClusterQueue{
						"c1": {
							Name:   "c1",
							Cohort: cohort,
							Workloads: map[string]*workload.Info{
								"/c1-memory-alpha": nil,
								"/c1-memory-beta":  nil,
							},
							RequestableResources: cqCache.clusterQueues["c1"].RequestableResources,
							UsedResources: ResourceQuantities{
								corev1.ResourceCPU: {"default": 1_000},
								corev1.ResourceMemory: {
									"alpha": 0,
									"beta":  1 * utiltesting.Gi,
								},
							},
						},
						"c2": {
							Name:   "c2",
							Cohort: cohort,
							Workloads: map[string]*workload.Info{
								"/c2-cpu-1": nil,
								"/c2-cpu-2": nil,
							},
							RequestableResources: cqCache.clusterQueues["c2"].RequestableResources,
							UsedResources: ResourceQuantities{
								corev1.ResourceCPU: {"default": 2_000},
							},
						},
					},
				}
			}(),
		},
	}
	cmpOpts := append(snapCmpOpts,
		cmpopts.IgnoreFields(ClusterQueue{}, "NamespaceSelector", "Preemption", "Status"),
		cmpopts.IgnoreFields(Snapshot{}, "ResourceFlavors"),
		cmpopts.IgnoreTypes(&workload.Info{}))
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			snap := cqCache.Snapshot()
			for _, name := range tc.remove {
				snap.RemoveWorkload(wlInfos[name])
			}
			for _, name := range tc.add {
				snap.AddWorkload(wlInfos[name])
			}
			if diff := cmp.Diff(tc.want, snap, cmpOpts...); diff != "" {
				t.Errorf("Unexpected snapshot state after operations (-want,+got):\n%s", diff)
			}
		})
	}
}
