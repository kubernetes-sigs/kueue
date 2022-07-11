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
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/util/sets"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	"sigs.k8s.io/kueue/pkg/workload"
)

func TestSnapshot(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := kueue.AddToScheme(scheme); err != nil {
		t.Fatalf("Failed adding kueue scheme: %s", err)
	}

	cases := []struct {
		name          string
		clusterQueues []*kueue.ClusterQueue
		flavors       []kueue.ResourceFlavor
		workloads     []*kueue.Workload
		wantSnapshot  func(workloads []*kueue.Workload) Snapshot
	}{
		{
			name: "empty",
			wantSnapshot: func(workloads []*kueue.Workload) Snapshot {
				return Snapshot{
					ClusterQueues:   map[string]*ClusterQueue{},
					ResourceFlavors: map[string]*kueue.ResourceFlavor{},
				}
			},
		},
		{
			name: "cluster queues",
			clusterQueues: []*kueue.ClusterQueue{
				utiltesting.MakeClusterQueue("foo").Obj(),
				utiltesting.MakeClusterQueue("bar").Obj(),
			},
			wantSnapshot: func(workloads []*kueue.Workload) Snapshot {
				return Snapshot{
					ClusterQueues: map[string]*ClusterQueue{
						"bar": {
							Name:                 "bar",
							NamespaceSelector:    labels.Everything(),
							Status:               Active,
							RequestableResources: map[corev1.ResourceName][]FlavorLimits{},
							UsedResources:        Resources{},
							Workloads:            map[string]*workload.Info{},
						},
						"foo": {
							Name:                 "foo",
							NamespaceSelector:    labels.Everything(),
							Status:               Active,
							RequestableResources: map[corev1.ResourceName][]FlavorLimits{},
							UsedResources:        Resources{},
							Workloads:            map[string]*workload.Info{},
						},
					},
					ResourceFlavors: map[string]*kueue.ResourceFlavor{},
				}
			},
		},
		{
			name: "inactive cluster queues",
			clusterQueues: []*kueue.ClusterQueue{
				utiltesting.MakeClusterQueue("flavor-nonexistent").Resource(&kueue.Resource{
					Name: corev1.ResourceCPU,
					Flavors: []kueue.Flavor{
						*utiltesting.MakeFlavor("nonexistent-flavor", "100").Obj(),
					},
				}).Obj(),
			},
			wantSnapshot: func(workloads []*kueue.Workload) Snapshot {
				return Snapshot{
					ClusterQueues:            map[string]*ClusterQueue{},
					ResourceFlavors:          map[string]*kueue.ResourceFlavor{},
					InactiveClusterQueueSets: sets.String.Insert(sets.NewString("flavor-nonexistent")),
				}
			},
		},
		{
			name: "resource flavors",
			flavors: []kueue.ResourceFlavor{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "demand"},
					Labels:     map[string]string{"foo": "bar", "instance": "demand"},
				},
				{
					ObjectMeta: metav1.ObjectMeta{Name: "spot"},
					Labels:     map[string]string{"baz": "bar", "instance": "spot"},
				},
				{
					ObjectMeta: metav1.ObjectMeta{Name: "default"},
				},
			},
			wantSnapshot: func(workloads []*kueue.Workload) Snapshot {
				return Snapshot{
					ClusterQueues: map[string]*ClusterQueue{},
					ResourceFlavors: map[string]*kueue.ResourceFlavor{
						"demand": {
							ObjectMeta: metav1.ObjectMeta{Name: "demand"},
							Labels:     map[string]string{"foo": "bar", "instance": "demand"},
						},
						"spot": {
							ObjectMeta: metav1.ObjectMeta{Name: "spot"},
							Labels:     map[string]string{"baz": "bar", "instance": "spot"},
						},
						"default": {
							ObjectMeta: metav1.ObjectMeta{Name: "default"},
						},
					},
				}
			},
		},
		{
			name: "workloads",
			clusterQueues: []*kueue.ClusterQueue{
				utiltesting.MakeClusterQueue("foo").Obj(),
				utiltesting.MakeClusterQueue("bar").Obj(),
			},
			workloads: []*kueue.Workload{
				utiltesting.MakeWorkload("alpha", "").Admit(&kueue.Admission{
					ClusterQueue: "foo",
				}).Obj(),
				utiltesting.MakeWorkload("beta", "").Admit(&kueue.Admission{
					ClusterQueue: "bar",
				}).Obj(),
			},
			wantSnapshot: func(workloads []*kueue.Workload) Snapshot {
				return Snapshot{
					ClusterQueues: map[string]*ClusterQueue{
						"foo": {
							Name:                 "foo",
							NamespaceSelector:    labels.Everything(),
							Status:               Active,
							RequestableResources: map[corev1.ResourceName][]FlavorLimits{},
							UsedResources:        Resources{},
							Workloads: map[string]*workload.Info{
								"/alpha": workload.NewInfo(workloads[0]),
							},
						},
						"bar": {
							Name:                 "bar",
							NamespaceSelector:    labels.Everything(),
							Status:               Active,
							RequestableResources: map[corev1.ResourceName][]FlavorLimits{},
							UsedResources:        Resources{},
							Workloads: map[string]*workload.Info{
								"/beta": workload.NewInfo(workloads[1]),
							},
						},
					},
					ResourceFlavors: map[string]*kueue.ResourceFlavor{},
				}
			},
		},
		{
			name: "cohort",
			clusterQueues: []*kueue.ClusterQueue{
				utiltesting.MakeClusterQueue("foo").
					Cohort("borrowing").
					Resource(&kueue.Resource{
						Name: corev1.ResourceCPU,
						Flavors: []kueue.Flavor{
							*utiltesting.MakeFlavor("demand", "100").Obj(),
							*utiltesting.MakeFlavor("spot", "200").Obj(),
						},
					}).Obj(),
				utiltesting.MakeClusterQueue("bar").
					Cohort("borrowing").
					Resource(&kueue.Resource{
						Name: corev1.ResourceCPU,
						Flavors: []kueue.Flavor{
							*utiltesting.MakeFlavor("spot", "100").Obj(),
						},
					}).
					Resource(&kueue.Resource{
						Name: "example.com/gpu",
						Flavors: []kueue.Flavor{
							*utiltesting.MakeFlavor("default", "50").Obj(),
						},
					}).
					Obj(),
				utiltesting.MakeClusterQueue("baz").
					Resource(&kueue.Resource{
						Name: corev1.ResourceCPU,
						Flavors: []kueue.Flavor{
							*utiltesting.MakeFlavor("default", "100").Obj(),
						},
					}).
					Obj(),
			},
			flavors: []kueue.ResourceFlavor{
				{
					ObjectMeta: metav1.ObjectMeta{Name: "demand"},
					Labels:     map[string]string{"foo": "bar", "instance": "demand"},
				},
				{
					ObjectMeta: metav1.ObjectMeta{Name: "spot"},
					Labels:     map[string]string{"baz": "bar", "instance": "spot"},
				},
				{
					ObjectMeta: metav1.ObjectMeta{Name: "default"},
				},
			},
			workloads: []*kueue.Workload{
				utiltesting.MakeWorkload("alpha", "").
					PodSets([]kueue.PodSet{
						{
							Name:  "main",
							Count: 5,
							Spec: utiltesting.PodSpecForRequest(map[corev1.ResourceName]string{
								corev1.ResourceCPU: "2",
							}),
						},
					}).Admit(&kueue.Admission{
					ClusterQueue: "foo",
					PodSetFlavors: []kueue.PodSetFlavors{
						{
							Name: "main",
							Flavors: map[corev1.ResourceName]string{
								corev1.ResourceCPU: "demand",
							},
						},
					},
				}).Obj(),
				utiltesting.MakeWorkload("beta", "").
					PodSets([]kueue.PodSet{
						{
							Name:  "main",
							Count: 5,
							Spec: utiltesting.PodSpecForRequest(map[corev1.ResourceName]string{
								corev1.ResourceCPU: "1",
								"example.com/gpu":  "2",
							}),
						},
					}).Admit(&kueue.Admission{
					ClusterQueue: "bar",
					PodSetFlavors: []kueue.PodSetFlavors{
						{
							Name: "main",
							Flavors: map[corev1.ResourceName]string{
								corev1.ResourceCPU: "spot",
								"example.com/gpu":  "default",
							},
						},
					},
				}).Obj(),
				utiltesting.MakeWorkload("gamma", "").
					PodSets([]kueue.PodSet{
						{
							Name:  "main",
							Count: 5,
							Spec: utiltesting.PodSpecForRequest(map[corev1.ResourceName]string{
								corev1.ResourceCPU: "1",
								"example.com/gpu":  "1",
							}),
						},
					}).Admit(&kueue.Admission{
					ClusterQueue: "bar",
					PodSetFlavors: []kueue.PodSetFlavors{
						{
							Name: "main",
							Flavors: map[corev1.ResourceName]string{
								corev1.ResourceCPU: "spot",
								"example.com/gpu":  "default",
							},
						},
					},
				}).Obj(),
				utiltesting.MakeWorkload("sigma", "").
					PodSets([]kueue.PodSet{
						{
							Name:  "main",
							Count: 5,
							Spec: utiltesting.PodSpecForRequest(map[corev1.ResourceName]string{
								corev1.ResourceCPU: "1",
							}),
						},
					}).Obj(),
			},
			wantSnapshot: func(workloads []*kueue.Workload) Snapshot {
				wantCohort := Cohort{
					Name: "borrowing",
					RequestableResources: Resources{
						corev1.ResourceCPU: map[string]int64{
							"demand": 100_000,
							"spot":   300_000,
						},
						"example.com/gpu": map[string]int64{
							"default": 50,
						},
					},
					UsedResources: Resources{
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
						"foo": {
							Name:              "foo",
							NamespaceSelector: labels.Everything(),
							LabelKeys:         map[corev1.ResourceName]sets.String{corev1.ResourceCPU: {"baz": {}, "foo": {}, "instance": {}}},
							Status:            Active,
							Cohort:            &wantCohort,
							RequestableResources: map[corev1.ResourceName][]FlavorLimits{
								corev1.ResourceCPU: {
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
							UsedResources: Resources{
								corev1.ResourceCPU: {"demand": 10_000, "spot": 0},
							},
							Workloads: map[string]*workload.Info{
								"/alpha": workload.NewInfo(workloads[0]),
							},
						},
						"bar": {
							Name:              "bar",
							NamespaceSelector: labels.Everything(),
							LabelKeys:         map[corev1.ResourceName]sets.String{corev1.ResourceCPU: {"baz": {}, "instance": {}}},
							Status:            Active,
							Cohort:            &wantCohort,
							RequestableResources: map[corev1.ResourceName][]FlavorLimits{
								corev1.ResourceCPU: {
									{
										Name: "spot",
										Min:  100_000,
									},
								},
								"example.com/gpu": {
									{
										Name: "default",
										Min:  50,
									},
								},
							},
							UsedResources: Resources{
								corev1.ResourceCPU: {"spot": 10_000},
								"example.com/gpu":  {"default": 15},
							},
							Workloads: map[string]*workload.Info{
								"/beta":  workload.NewInfo(workloads[1]),
								"/gamma": workload.NewInfo(workloads[2]),
							},
						},
						"baz": {
							Name:              "baz",
							NamespaceSelector: labels.Everything(),
							Status:            Active,
							RequestableResources: map[corev1.ResourceName][]FlavorLimits{
								corev1.ResourceCPU: {
									{
										Name: "default",
										Min:  100_000,
									},
								},
							},
							UsedResources: Resources{
								corev1.ResourceCPU: {"default": 0},
							},
							Workloads: map[string]*workload.Info{},
						},
					},
					ResourceFlavors: map[string]*kueue.ResourceFlavor{
						"demand": {
							ObjectMeta: metav1.ObjectMeta{Name: "demand"},
							Labels:     map[string]string{"foo": "bar", "instance": "demand"},
						},
						"spot": {
							ObjectMeta: metav1.ObjectMeta{Name: "spot"},
							Labels:     map[string]string{"baz": "bar", "instance": "spot"},
						},
						"default": {
							ObjectMeta: metav1.ObjectMeta{Name: "default"},
						},
					},
				}
			},
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cache := New(fake.NewClientBuilder().WithScheme(scheme).Build())

			for _, c := range tc.clusterQueues {
				// Purposely do not make a copy of clusterQueues. Clones of necessary fields are
				// done in AddClusterQueue.
				if err := cache.AddClusterQueue(context.Background(), c); err != nil {
					t.Fatalf("Failed adding ClusterQueue: %v", err)
				}
			}

			for i := range tc.flavors {
				cache.AddOrUpdateResourceFlavor(&tc.flavors[i])
			}

			for _, w := range tc.workloads {
				cache.AddOrUpdateWorkload(w.DeepCopy())
			}

			snapshot := cache.Snapshot()
			if diff := cmp.Diff(tc.wantSnapshot(tc.workloads), snapshot, cmpopts.IgnoreUnexported(Cohort{})); diff != "" {
				t.Errorf("Unexpected Snapshot (-want,+got):\n%s", diff)
			}
		})
	}
}
