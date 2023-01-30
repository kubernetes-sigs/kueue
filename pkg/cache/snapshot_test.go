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

	testCases := map[string]struct {
		cqs          []*kueue.ClusterQueue
		rfs          []*kueue.ResourceFlavor
		wls          []*kueue.Workload
		wantSnapshot Snapshot
	}{
		"empty": {
			wantSnapshot: Snapshot{
				ClusterQueues:   map[string]*ClusterQueue{},
				ResourceFlavors: map[string]*kueue.ResourceFlavor{},
			},
		},
		"independent clusterQueues": {
			cqs: []*kueue.ClusterQueue{
				utiltesting.MakeClusterQueue("a").Obj(),
				utiltesting.MakeClusterQueue("b").Obj(),
			},
			wls: []*kueue.Workload{
				utiltesting.MakeWorkload("alpha", "").
					Admit(&kueue.Admission{ClusterQueue: "a"}).Obj(),
				utiltesting.MakeWorkload("beta", "").
					Admit(&kueue.Admission{ClusterQueue: "b"}).Obj(),
			},
			wantSnapshot: Snapshot{
				ClusterQueues: map[string]*ClusterQueue{
					"a": {
						Name:                 "a",
						NamespaceSelector:    labels.Everything(),
						Status:               active,
						RequestableResources: map[corev1.ResourceName]*Resource{},
						UsedResources:        ResourceQuantities{},
						Workloads: map[string]*workload.Info{
							"/alpha": workload.NewInfo(
								utiltesting.MakeWorkload("alpha", "").
									Admit(&kueue.Admission{ClusterQueue: "a"}).Obj()),
						},
						Preemption: defaultPreemption,
					},
					"b": {
						Name:                 "b",
						NamespaceSelector:    labels.Everything(),
						Status:               active,
						RequestableResources: map[corev1.ResourceName]*Resource{},
						UsedResources:        ResourceQuantities{},
						Workloads: map[string]*workload.Info{
							"/beta": workload.NewInfo(
								utiltesting.MakeWorkload("beta", "").
									Admit(&kueue.Admission{ClusterQueue: "b"}).Obj()),
						},
						Preemption: defaultPreemption,
					},
				},
				ResourceFlavors: map[string]*kueue.ResourceFlavor{},
			},
		},
		"inactive clusterQueues": {
			cqs: []*kueue.ClusterQueue{
				utiltesting.MakeClusterQueue("flavor-nonexistent-cq").
					Resource(&kueue.Resource{
						Name: corev1.ResourceCPU,
						Flavors: []kueue.Flavor{
							*utiltesting.MakeFlavor("nonexistent-flavor", "100").Obj(),
						},
					}).Obj(),
			},
			wantSnapshot: Snapshot{
				ClusterQueues:            map[string]*ClusterQueue{},
				ResourceFlavors:          map[string]*kueue.ResourceFlavor{},
				InactiveClusterQueueSets: sets.New("flavor-nonexistent-cq"),
			},
		},
		"resourceFlavors": {
			rfs: []*kueue.ResourceFlavor{
				utiltesting.MakeResourceFlavor("demand").
					MultiLabels(map[string]string{"a": "b", "instance": "demand"}).Obj(),
				utiltesting.MakeResourceFlavor("spot").
					MultiLabels(map[string]string{"c": "d", "instance": "spot"}).Obj(),
				utiltesting.MakeResourceFlavor("default").Obj(),
			},
			wantSnapshot: Snapshot{
				ClusterQueues: map[string]*ClusterQueue{},
				ResourceFlavors: map[string]*kueue.ResourceFlavor{
					"demand": utiltesting.MakeResourceFlavor("demand").
						MultiLabels(map[string]string{"a": "b", "instance": "demand"}).Obj(),
					"spot": utiltesting.MakeResourceFlavor("spot").
						MultiLabels(map[string]string{"c": "d", "instance": "spot"}).Obj(),
					"default": utiltesting.MakeResourceFlavor("default").Obj(),
				},
			},
		},
		"cohort": {
			cqs: []*kueue.ClusterQueue{
				utiltesting.MakeClusterQueue("a").
					Cohort("borrowing").
					Resource(&kueue.Resource{
						Name: corev1.ResourceCPU,
						Flavors: []kueue.Flavor{
							*utiltesting.MakeFlavor("demand", "100").Obj(),
							*utiltesting.MakeFlavor("spot", "200").Obj(),
						},
					}).Obj(),
				utiltesting.MakeClusterQueue("b").
					Cohort("borrowing").
					Resource(&kueue.Resource{
						Name: corev1.ResourceCPU,
						Flavors: []kueue.Flavor{
							*utiltesting.MakeFlavor("spot", "100").Obj(),
						},
					}).Resource(&kueue.Resource{
					Name: "example.com/gpu",
					Flavors: []kueue.Flavor{
						*utiltesting.MakeFlavor("default", "50").Obj(),
					},
				}).Obj(),
				utiltesting.MakeClusterQueue("c").
					Resource(&kueue.Resource{
						Name: corev1.ResourceCPU,
						Flavors: []kueue.Flavor{
							*utiltesting.MakeFlavor("default", "100").Obj(),
						},
					}).Obj(),
			},
			rfs: []*kueue.ResourceFlavor{
				utiltesting.MakeResourceFlavor("demand").
					MultiLabels(map[string]string{"one": "two", "instance": "demand"}).Obj(),
				utiltesting.MakeResourceFlavor("spot").
					MultiLabels(map[string]string{"two": "three", "instance": "spot"}).Obj(),
				utiltesting.MakeResourceFlavor("default").Obj(),
			},
			wls: []*kueue.Workload{
				utiltesting.MakeWorkload("alpha", "").
					PodSets([]kueue.PodSet{{
						Name:  "main",
						Count: 5,
						Spec: utiltesting.PodSpecForRequest(map[corev1.ResourceName]string{
							corev1.ResourceCPU: "2",
						}),
					}}).Admit(&kueue.Admission{
					ClusterQueue: "a",
					PodSetFlavors: []kueue.PodSetFlavors{{
						Name: "main",
						Flavors: map[corev1.ResourceName]string{
							corev1.ResourceCPU: "demand",
						},
					}},
				}).Obj(),
				utiltesting.MakeWorkload("beta", "").
					PodSets([]kueue.PodSet{{
						Name:  "main",
						Count: 5,
						Spec: utiltesting.PodSpecForRequest(map[corev1.ResourceName]string{
							corev1.ResourceCPU: "1",
							"example.com/gpu":  "2",
						}),
					}}).Admit(&kueue.Admission{
					ClusterQueue: "b",
					PodSetFlavors: []kueue.PodSetFlavors{{
						Name: "main",
						Flavors: map[corev1.ResourceName]string{
							corev1.ResourceCPU: "spot",
							"example.com/gpu":  "default",
						},
					}},
				}).Obj(),
				utiltesting.MakeWorkload("gamma", "").
					PodSets([]kueue.PodSet{{
						Name:  "main",
						Count: 5,
						Spec: utiltesting.PodSpecForRequest(map[corev1.ResourceName]string{
							corev1.ResourceCPU: "1",
							"example.com/gpu":  "1",
						}),
					}}).Admit(&kueue.Admission{
					ClusterQueue: "b",
					PodSetFlavors: []kueue.PodSetFlavors{{
						Name: "main",
						Flavors: map[corev1.ResourceName]string{
							corev1.ResourceCPU: "spot",
							"example.com/gpu":  "default",
						},
					}},
				}).Obj(),
				utiltesting.MakeWorkload("sigma", "").
					PodSets([]kueue.PodSet{{
						Name:  "main",
						Count: 5,
						Spec: utiltesting.PodSpecForRequest(map[corev1.ResourceName]string{
							corev1.ResourceCPU: "1",
						}),
					}}).Obj(),
			},
			wantSnapshot: func() Snapshot {
				cohort := &Cohort{
					Name: "borrowing",
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
				return Snapshot{
					ClusterQueues: map[string]*ClusterQueue{
						"a": {
							Name:   "a",
							Cohort: cohort,
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
								"/alpha": workload.NewInfo(
									utiltesting.MakeWorkload("alpha", "").
										PodSets([]kueue.PodSet{{
											Name:  "main",
											Count: 5,
											Spec: utiltesting.PodSpecForRequest(map[corev1.ResourceName]string{
												corev1.ResourceCPU: "2",
											}),
										}}).Admit(&kueue.Admission{
										ClusterQueue: "a",
										PodSetFlavors: []kueue.PodSetFlavors{{
											Name: "main",
											Flavors: map[corev1.ResourceName]string{
												corev1.ResourceCPU: "demand",
											},
										}},
									}).Obj()),
							},
							Preemption: defaultPreemption,
							LabelKeys: map[corev1.ResourceName]sets.Set[string]{
								corev1.ResourceCPU: sets.New("one", "two", "instance"),
							},
							NamespaceSelector: labels.Everything(),
							Status:            active,
						},
						"b": {
							Name:   "b",
							Cohort: cohort,
							RequestableResources: map[corev1.ResourceName]*Resource{
								corev1.ResourceCPU: {
									Flavors: []FlavorLimits{{
										Name: "spot",
										Min:  100_000,
									}},
								},
								"example.com/gpu": {
									Flavors: []FlavorLimits{{
										Name: "default",
										Min:  50,
									}},
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
								"/beta": workload.NewInfo(
									utiltesting.MakeWorkload("beta", "").
										PodSets([]kueue.PodSet{{
											Name:  "main",
											Count: 5,
											Spec: utiltesting.PodSpecForRequest(map[corev1.ResourceName]string{
												corev1.ResourceCPU: "1",
												"example.com/gpu":  "2",
											}),
										}}).Admit(&kueue.Admission{
										ClusterQueue: "b",
										PodSetFlavors: []kueue.PodSetFlavors{{
											Name: "main",
											Flavors: map[corev1.ResourceName]string{
												corev1.ResourceCPU: "spot",
												"example.com/gpu":  "default",
											},
										}},
									}).Obj()),
								"/gamma": workload.NewInfo(
									utiltesting.MakeWorkload("gamma", "").
										PodSets([]kueue.PodSet{{
											Name:  "main",
											Count: 5,
											Spec: utiltesting.PodSpecForRequest(map[corev1.ResourceName]string{
												corev1.ResourceCPU: "1",
												"example.com/gpu":  "1",
											}),
										}}).Admit(&kueue.Admission{
										ClusterQueue: "b",
										PodSetFlavors: []kueue.PodSetFlavors{{
											Name: "main",
											Flavors: map[corev1.ResourceName]string{
												corev1.ResourceCPU: "spot",
												"example.com/gpu":  "default",
											},
										}},
									}).Obj()),
							},
							Preemption: defaultPreemption,
							LabelKeys: map[corev1.ResourceName]sets.Set[string]{
								corev1.ResourceCPU: sets.New("two", "instance"),
							},
							NamespaceSelector: labels.Everything(),
							Status:            active,
						},
						"c": {
							Name: "c",
							RequestableResources: map[corev1.ResourceName]*Resource{
								corev1.ResourceCPU: {
									Flavors: []FlavorLimits{{
										Name: "default",
										Min:  100_000,
									}},
								},
							},
							UsedResources: ResourceQuantities{
								corev1.ResourceCPU: map[string]int64{
									"default": 0,
								},
							},
							Workloads:         map[string]*workload.Info{},
							Preemption:        defaultPreemption,
							NamespaceSelector: labels.Everything(),
							Status:            active,
						},
					},
					ResourceFlavors: map[string]*kueue.ResourceFlavor{
						"demand": utiltesting.MakeResourceFlavor("demand").
							MultiLabels(map[string]string{"one": "two", "instance": "demand"}).Obj(),
						"spot": utiltesting.MakeResourceFlavor("spot").
							MultiLabels(map[string]string{"two": "three", "instance": "spot"}).Obj(),
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
				ClusterQueues: map[string]*ClusterQueue{
					"with-preemption": {
						Name:                 "with-preemption",
						NamespaceSelector:    labels.Everything(),
						Status:               active,
						RequestableResources: map[corev1.ResourceName]*Resource{},
						UsedResources:        ResourceQuantities{},
						Workloads:            map[string]*workload.Info{},
						Preemption: kueue.ClusterQueuePreemption{
							ReclaimWithinCohort: kueue.PreemptionPolicyAny,
							WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
						},
					},
				},
				ResourceFlavors: map[string]*kueue.ResourceFlavor{},
			},
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			cache := New(fake.NewClientBuilder().WithScheme(scheme).Build())
			for _, cq := range tc.cqs {
				// Purposely do not make a copy of clusterQueues. Clones of necessary fields are
				// done in AddClusterQueue.
				if err := cache.AddClusterQueue(context.Background(), cq); err != nil {
					t.Fatalf("Failed adding ClusterQueue: %v", err)
				}
			}
			for _, rf := range tc.rfs {
				cache.AddOrUpdateResourceFlavor(rf)
			}
			for _, wl := range tc.wls {
				cache.AddOrUpdateWorkload(wl)
			}
			snapshot := cache.Snapshot()
			if diff := cmp.Diff(tc.wantSnapshot, snapshot, snapCmpOpts...); len(diff) != 0 {
				t.Errorf("Unexpected Snapshot (-want,+got):\n%s", diff)
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
