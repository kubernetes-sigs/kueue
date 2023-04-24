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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/sets"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	"sigs.k8s.io/kueue/pkg/workload"
)

var snapCmpOpts = []cmp.Option{
	cmpopts.EquateEmpty(),
	cmpopts.IgnoreUnexported(ClusterQueue{}),
	cmpopts.IgnoreFields(ClusterQueue{}, "RGByResource"),
	cmpopts.IgnoreFields(Cohort{}, "Members"), // avoid recursion.
	cmpopts.IgnoreFields(metav1.Condition{}, "LastTransitionTime"),
}

func TestSnapshot(t *testing.T) {
	testCases := map[string]struct {
		cqs          []*kueue.ClusterQueue
		rfs          []*kueue.ResourceFlavor
		wls          []*kueue.Workload
		wantSnapshot Snapshot
	}{
		"empty": {},
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
						Name:              "a",
						NamespaceSelector: labels.Everything(),
						Status:            active,
						Workloads: map[string]*workload.Info{
							"/alpha": workload.NewInfo(
								utiltesting.MakeWorkload("alpha", "").
									Admit(&kueue.Admission{ClusterQueue: "a"}).Obj()),
						},
						Preemption: defaultPreemption,
					},
					"b": {
						Name:              "b",
						NamespaceSelector: labels.Everything(),
						Status:            active,
						Workloads: map[string]*workload.Info{
							"/beta": workload.NewInfo(
								utiltesting.MakeWorkload("beta", "").
									Admit(&kueue.Admission{ClusterQueue: "b"}).Obj()),
						},
						Preemption: defaultPreemption,
					},
				},
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
				InactiveClusterQueueSets: sets.New("flavor-nonexistent-cq"),
			},
		},
		"resourceFlavors": {
			rfs: []*kueue.ResourceFlavor{
				utiltesting.MakeResourceFlavor("demand").
					Label("a", "b").
					Label("instance", "demand").
					Obj(),
				utiltesting.MakeResourceFlavor("spot").
					Label("c", "d").
					Label("instance", "spot").
					Obj(),
				utiltesting.MakeResourceFlavor("default").Obj(),
			},
			wantSnapshot: Snapshot{
				ClusterQueues: map[string]*ClusterQueue{},
				ResourceFlavors: map[kueue.ResourceFlavorReference]*kueue.ResourceFlavor{
					"demand": utiltesting.MakeResourceFlavor("demand").
						Label("a", "b").
						Label("instance", "demand").
						Obj(),
					"spot": utiltesting.MakeResourceFlavor("spot").
						Label("c", "d").
						Label("instance", "spot").
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
				utiltesting.MakeResourceFlavor("demand").Label("instance", "demand").Obj(),
				utiltesting.MakeResourceFlavor("spot").Label("instance", "spot").Obj(),
				utiltesting.MakeResourceFlavor("default").Obj(),
			},
			wls: []*kueue.Workload{
				utiltesting.MakeWorkload("alpha", "").
					PodSets(*utiltesting.MakePodSet("main", 5).
						Request(corev1.ResourceCPU, "2").Obj()).
					Admit(utiltesting.MakeAdmission("a", "main").Assignment(corev1.ResourceCPU, "demand", "10000m").Obj()).
					Obj(),
				utiltesting.MakeWorkload("beta", "").
					PodSets(*utiltesting.MakePodSet("main", 5).
						Request(corev1.ResourceCPU, "1").
						Request("example.com/gpu", "2").
						Obj(),
					).
					Admit(utiltesting.MakeAdmission("b", "main").Assignment(corev1.ResourceCPU, "spot", "5000m").Assignment("example.com/gpu", "default", "10").Obj()).
					Obj(),
				utiltesting.MakeWorkload("gamma", "").
					PodSets(*utiltesting.MakePodSet("main", 5).
						Request(corev1.ResourceCPU, "1").
						Request("example.com/gpu", "1").
						Obj(),
					).
					Admit(utiltesting.MakeAdmission("b", "main").Assignment(corev1.ResourceCPU, "spot", "5000m").Assignment("example.com/gpu", "default", "5").Obj()).
					Obj(),
				utiltesting.MakeWorkload("sigma", "").
					PodSets(*utiltesting.MakePodSet("main", 5).
						Request(corev1.ResourceCPU, "1").
						Obj(),
					).
					Obj(),
			},
			wantSnapshot: func() Snapshot {
				cohort := &Cohort{
					Name: "borrowing",
					RequestableResources: FlavorResourceQuantities{
						"demand": {
							corev1.ResourceCPU: 100_000,
						},
						"spot": {
							corev1.ResourceCPU: 300_000,
						},
						"default": {
							"example.com/gpu": 50,
						},
					},
					Usage: FlavorResourceQuantities{
						"demand": {
							corev1.ResourceCPU: 10_000,
						},
						"spot": {
							corev1.ResourceCPU: 10_000,
						},
						"default": {
							"example.com/gpu": 15,
						},
					},
				}
				return Snapshot{
					ClusterQueues: map[string]*ClusterQueue{
						"a": {
							Name:   "a",
							Cohort: cohort,
							ResourceGroups: []ResourceGroup{
								{
									CoveredResources: sets.New(corev1.ResourceCPU),
									Flavors: []FlavorQuotas{
										{
											Name: "demand",
											Resources: map[corev1.ResourceName]*ResourceQuota{
												corev1.ResourceCPU: {Nominal: 100_000},
											},
										},
										{
											Name: "spot",
											Resources: map[corev1.ResourceName]*ResourceQuota{
												corev1.ResourceCPU: {Nominal: 200_000},
											},
										},
									},
									LabelKeys: sets.New("instance"),
								},
							},
							Usage: FlavorResourceQuantities{
								"demand": {corev1.ResourceCPU: 10_000},
								"spot":   {corev1.ResourceCPU: 0},
							},
							Workloads: map[string]*workload.Info{
								"/alpha": workload.NewInfo(utiltesting.MakeWorkload("alpha", "").
									PodSets(*utiltesting.MakePodSet("main", 5).
										Request(corev1.ResourceCPU, "2").Obj()).
									Admit(utiltesting.MakeAdmission("a", "main").Assignment(corev1.ResourceCPU, "demand", "10000m").Obj()).
									Obj()),
							},
							Preemption:        defaultPreemption,
							NamespaceSelector: labels.Everything(),
							Status:            active,
						},
						"b": {
							Name:   "b",
							Cohort: cohort,
							ResourceGroups: []ResourceGroup{
								{
									CoveredResources: sets.New(corev1.ResourceCPU),
									Flavors: []FlavorQuotas{{
										Name: "spot",
										Resources: map[corev1.ResourceName]*ResourceQuota{
											corev1.ResourceCPU: {Nominal: 100_000},
										},
									}},
									LabelKeys: sets.New("instance"),
								},
								{
									CoveredResources: sets.New[corev1.ResourceName]("example.com/gpu"),
									Flavors: []FlavorQuotas{{
										Name: "default",
										Resources: map[corev1.ResourceName]*ResourceQuota{
											"example.com/gpu": {Nominal: 50},
										},
									}},
								},
							},
							Usage: FlavorResourceQuantities{
								"spot": {
									corev1.ResourceCPU: 10_000,
								},
								"default": {
									"example.com/gpu": 15,
								},
							},
							Workloads: map[string]*workload.Info{
								"/beta": workload.NewInfo(utiltesting.MakeWorkload("beta", "").
									PodSets(*utiltesting.MakePodSet("main", 5).
										Request(corev1.ResourceCPU, "1").
										Request("example.com/gpu", "2").
										Obj()).
									Admit(utiltesting.MakeAdmission("b", "main").Assignment(corev1.ResourceCPU, "spot", "5000m").Assignment("example.com/gpu", "default", "10").Obj()).
									Obj()),
								"/gamma": workload.NewInfo(utiltesting.MakeWorkload("gamma", "").
									PodSets(*utiltesting.MakePodSet("main", 5).
										Request(corev1.ResourceCPU, "1").
										Request("example.com/gpu", "1").
										Obj(),
									).
									Admit(utiltesting.MakeAdmission("b", "main").Assignment(corev1.ResourceCPU, "spot", "5000m").Assignment("example.com/gpu", "default", "5").Obj()).
									Obj()),
							},
							Preemption:        defaultPreemption,
							NamespaceSelector: labels.Everything(),
							Status:            active,
						},
						"c": {
							Name: "c",
							ResourceGroups: []ResourceGroup{
								{
									CoveredResources: sets.New(corev1.ResourceCPU),
									Flavors: []FlavorQuotas{{
										Name: "default",
										Resources: map[corev1.ResourceName]*ResourceQuota{
											corev1.ResourceCPU: {Nominal: 100_000},
										},
									}},
								},
							},
							Usage: FlavorResourceQuantities{
								"default": {
									corev1.ResourceCPU: 0,
								},
							},
							Preemption:        defaultPreemption,
							NamespaceSelector: labels.Everything(),
							Status:            active,
						},
					},
					ResourceFlavors: map[kueue.ResourceFlavorReference]*kueue.ResourceFlavor{
						"demand":  utiltesting.MakeResourceFlavor("demand").Label("instance", "demand").Obj(),
						"spot":    utiltesting.MakeResourceFlavor("spot").Label("instance", "spot").Obj(),
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
						Name:              "with-preemption",
						NamespaceSelector: labels.Everything(),
						Status:            active,
						Workloads:         map[string]*workload.Info{},
						Preemption: kueue.ClusterQueuePreemption{
							ReclaimWithinCohort: kueue.PreemptionPolicyAny,
							WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
						},
					},
				},
			},
		},
	}

	for name, tc := range testCases {
		t.Run(name, func(t *testing.T) {
			cache := New(utiltesting.NewFakeClient())
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
			for _, cq := range snapshot.ClusterQueues {
				for i := range cq.ResourceGroups {
					rg := &cq.ResourceGroups[i]
					for rName := range rg.CoveredResources {
						if cq.RGByResource[rName] != rg {
							t.Errorf("RGByResource[%s] does not point to its resource group", rName)
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
			Admit(utiltesting.MakeAdmission("c1").Assignment(corev1.ResourceCPU, "default", "1000m").Obj()).
			Obj(),
		*utiltesting.MakeWorkload("c1-memory-alpha", "").
			Request(corev1.ResourceMemory, "1Gi").
			Admit(utiltesting.MakeAdmission("c1").Assignment(corev1.ResourceMemory, "alpha", "1Gi").Obj()).
			Obj(),
		*utiltesting.MakeWorkload("c1-memory-beta", "").
			Request(corev1.ResourceMemory, "1Gi").
			Admit(utiltesting.MakeAdmission("c1").Assignment(corev1.ResourceMemory, "beta", "1Gi").Obj()).
			Obj(),
		*utiltesting.MakeWorkload("c2-cpu-1", "").
			Request(corev1.ResourceCPU, "1").
			Admit(utiltesting.MakeAdmission("c2").Assignment(corev1.ResourceCPU, "default", "1000m").Obj()).
			Obj(),
		*utiltesting.MakeWorkload("c2-cpu-2", "").
			Request(corev1.ResourceCPU, "1").
			Admit(utiltesting.MakeAdmission("c2").Assignment(corev1.ResourceCPU, "default", "1000m").Obj()).
			Obj(),
	}

	ctx := context.Background()
	cl := utiltesting.NewClientBuilder().WithLists(&kueue.WorkloadList{Items: workloads}).Build()

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
					Usage: FlavorResourceQuantities{
						"default": {corev1.ResourceCPU: 0},
						"alpha":   {corev1.ResourceMemory: 0},
						"beta":    {corev1.ResourceMemory: 0},
					},
				}
				return Snapshot{
					ClusterQueues: map[string]*ClusterQueue{
						"c1": {
							Name:           "c1",
							Cohort:         cohort,
							Workloads:      make(map[string]*workload.Info),
							ResourceGroups: cqCache.clusterQueues["c1"].ResourceGroups,
							Usage: FlavorResourceQuantities{
								"default": {corev1.ResourceCPU: 0},
								"alpha":   {corev1.ResourceMemory: 0},
								"beta":    {corev1.ResourceMemory: 0},
							},
						},
						"c2": {
							Name:           "c2",
							Cohort:         cohort,
							Workloads:      make(map[string]*workload.Info),
							ResourceGroups: cqCache.clusterQueues["c2"].ResourceGroups,
							Usage: FlavorResourceQuantities{
								"default": {corev1.ResourceCPU: 0},
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
					Usage: FlavorResourceQuantities{
						"default": {corev1.ResourceCPU: 2_000},
						"alpha":   {corev1.ResourceMemory: utiltesting.Gi},
						"beta":    {corev1.ResourceMemory: utiltesting.Gi},
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
							ResourceGroups: cqCache.clusterQueues["c1"].ResourceGroups,
							Usage: FlavorResourceQuantities{
								"default": {corev1.ResourceCPU: 0},
								"alpha":   {corev1.ResourceMemory: utiltesting.Gi},
								"beta":    {corev1.ResourceMemory: utiltesting.Gi},
							},
						},
						"c2": {
							Name:   "c2",
							Cohort: cohort,
							Workloads: map[string]*workload.Info{
								"/c2-cpu-1": nil,
								"/c2-cpu-2": nil,
							},
							ResourceGroups: cqCache.clusterQueues["c2"].ResourceGroups,
							Usage: FlavorResourceQuantities{
								"default": {corev1.ResourceCPU: 2_000},
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
					Usage: FlavorResourceQuantities{
						"default": {corev1.ResourceCPU: 3_000},
						"alpha":   {corev1.ResourceMemory: 0},
						"beta":    {corev1.ResourceMemory: utiltesting.Gi},
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
							ResourceGroups: cqCache.clusterQueues["c1"].ResourceGroups,
							Usage: FlavorResourceQuantities{
								"default": {corev1.ResourceCPU: 1_000},
								"alpha":   {corev1.ResourceMemory: 0},
								"beta":    {corev1.ResourceMemory: utiltesting.Gi},
							},
						},
						"c2": {
							Name:   "c2",
							Cohort: cohort,
							Workloads: map[string]*workload.Info{
								"/c2-cpu-1": nil,
								"/c2-cpu-2": nil,
							},
							ResourceGroups: cqCache.clusterQueues["c2"].ResourceGroups,
							Usage: FlavorResourceQuantities{
								"default": {corev1.ResourceCPU: 2_000},
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
