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

	kueue "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	"sigs.k8s.io/kueue/pkg/workload"
)

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
			ObjectMeta: metav1.ObjectMeta{Name: "demand"},
			Labels:     map[string]string{"foo": "bar", "instance": "demand"},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "spot"},
			Labels:     map[string]string{"baz": "bar", "instance": "spot"},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "default"},
			Labels:     nil,
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
	wantSnapshot := Snapshot{
		ClusterQueues: map[string]*ClusterQueue{
			"foofoo": {
				Name:   "foofoo",
				Cohort: &wantCohort,
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
					corev1.ResourceCPU: map[string]int64{
						"demand": 10_000,
						"spot":   0,
					},
				},
				Workloads: map[string]*workload.Info{
					"/alpha": workload.NewInfo(&workloads[0]),
				},
				LabelKeys:         map[corev1.ResourceName]sets.String{corev1.ResourceCPU: {"baz": {}, "foo": {}, "instance": {}}},
				NamespaceSelector: labels.Nothing(),
				Status:            Active,
			},
			"foobar": {
				Name:   "foobar",
				Cohort: &wantCohort,
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
				NamespaceSelector: labels.Nothing(),
				LabelKeys:         map[corev1.ResourceName]sets.String{corev1.ResourceCPU: {"baz": {}, "instance": {}}},
				Status:            Active,
			},
			"bar": {
				Name: "bar",
				RequestableResources: map[corev1.ResourceName][]FlavorLimits{
					corev1.ResourceCPU: {
						{
							Name: "default",
							Min:  100_000,
						},
					},
				},
				UsedResources: Resources{
					corev1.ResourceCPU: map[string]int64{"default": 0},
				},
				Workloads:         map[string]*workload.Info{},
				NamespaceSelector: labels.Nothing(),
				Status:            Active,
			},
		},
		ResourceFlavors: map[string]*kueue.ResourceFlavor{
			"default": {
				ObjectMeta: metav1.ObjectMeta{Name: "default"},
				Labels:     nil,
			},
			"demand": {
				ObjectMeta: metav1.ObjectMeta{Name: "demand"},
				Labels:     map[string]string{"foo": "bar", "instance": "demand"},
			},
			"spot": {
				ObjectMeta: metav1.ObjectMeta{Name: "spot"},
				Labels:     map[string]string{"baz": "bar", "instance": "spot"},
			},
		},
		InactiveClusterQueueSets: sets.String{"flavor-nonexistent-cq": {}},
	}
	if diff := cmp.Diff(wantSnapshot, snapshot, cmpopts.IgnoreUnexported(Cohort{})); diff != "" {
		t.Errorf("Unexpected Snapshot (-want,+got):\n%s", diff)
	}
}
