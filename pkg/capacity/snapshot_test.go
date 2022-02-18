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

package capacity

import (
	"context"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"

	kueue "sigs.k8s.io/kueue/api/v1alpha1"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	"sigs.k8s.io/kueue/pkg/workload"
)

func TestSnapshot(t *testing.T) {
	scheme := runtime.NewScheme()
	kueue.AddToScheme(scheme)
	cache := NewCache(fake.NewClientBuilder().WithScheme(scheme).Build())
	capacities := []kueue.Capacity{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: "foofoo",
			},
			Spec: kueue.CapacitySpec{
				Cohort: "foo",
				RequestableResources: []kueue.Resource{
					{
						Name: corev1.ResourceCPU,
						Flavors: []kueue.ResourceFlavor{
							{
								Name: "demand",
								Quota: kueue.Quota{
									Guaranteed: resource.MustParse("100"),
								},
							},
							{
								Name: "spot",
								Quota: kueue.Quota{
									Guaranteed: resource.MustParse("200"),
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
			Spec: kueue.CapacitySpec{
				Cohort: "foo",
				RequestableResources: []kueue.Resource{
					{
						Name: corev1.ResourceCPU,
						Flavors: []kueue.ResourceFlavor{
							{
								Name: "spot",
								Quota: kueue.Quota{
									Guaranteed: resource.MustParse("100"),
								},
							},
						},
					},
					{
						Name: "example.com/gpu",
						Flavors: []kueue.ResourceFlavor{
							{
								Quota: kueue.Quota{
									Guaranteed: resource.MustParse("50"),
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
			Spec: kueue.CapacitySpec{
				RequestableResources: []kueue.Resource{
					{
						Name: corev1.ResourceCPU,
						Flavors: []kueue.ResourceFlavor{
							{
								Quota: kueue.Quota{
									Guaranteed: resource.MustParse("100"),
								},
							},
						},
					},
				},
			},
		},
	}
	for _, cap := range capacities {
		// Purposely  do not make a copy of cap. Clones of necessary fields are
		// done in AddCapacity.
		cache.AddCapacity(context.Background(), &cap)
	}
	workloads := []kueue.QueuedWorkload{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "alpha"},
			Spec: kueue.QueuedWorkloadSpec{
				Pods: []kueue.PodSet{
					{
						Count: 5,
						Spec: utiltesting.PodSpecForRequest(map[corev1.ResourceName]string{
							corev1.ResourceCPU: "2",
						}),
						AssignedFlavors: map[corev1.ResourceName]string{
							corev1.ResourceCPU: "demand",
						},
					},
				},
				AssignedCapacity: "foofoo",
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "beta"},
			Spec: kueue.QueuedWorkloadSpec{
				Pods: []kueue.PodSet{
					{
						Count: 5,
						Spec: utiltesting.PodSpecForRequest(map[corev1.ResourceName]string{
							corev1.ResourceCPU: "1",
							"example.com/gpu":  "2",
						}),
						AssignedFlavors: map[corev1.ResourceName]string{
							corev1.ResourceCPU: "spot",
							"example.com/gpu":  "",
						},
					},
				},
				AssignedCapacity: "foobar",
			},
		},
		{
			ObjectMeta: metav1.ObjectMeta{Name: "gamma"},
			Spec: kueue.QueuedWorkloadSpec{
				Pods: []kueue.PodSet{
					{
						Count: 5,
						Spec: utiltesting.PodSpecForRequest(map[corev1.ResourceName]string{
							corev1.ResourceCPU: "1",
							"example.com/gpu":  "1",
						}),
						AssignedFlavors: map[corev1.ResourceName]string{
							corev1.ResourceCPU: "spot",
							"example.com/gpu":  "",
						},
					},
				},
				AssignedCapacity: "foobar",
			},
		},
	}
	for _, w := range workloads {
		cache.AddOrUpdateWorkload(w.DeepCopy())
	}
	snapshot := cache.Snapshot()
	wantCohorts := []Cohort{
		{
			Name: "foo",
			RequestableResources: Resources{
				corev1.ResourceCPU: map[string]int64{
					"demand": 100_000,
					"spot":   300_000,
				},
				"example.com/gpu": map[string]int64{
					"": 50,
				},
			},
			UsedResources: Resources{
				corev1.ResourceCPU: map[string]int64{
					"demand": 10_000,
					"spot":   10_000,
				},
				"example.com/gpu": map[string]int64{
					"": 15,
				},
			},
		},
	}
	wantSnapshot := Snapshot{
		Capacities: map[string]*Capacity{
			"foofoo": {
				Name:   "foofoo",
				Cohort: &wantCohorts[0],
				RequestableResources: map[corev1.ResourceName][]kueue.ResourceFlavor{
					corev1.ResourceCPU: {
						{
							Name: "demand",
							Quota: kueue.Quota{
								Guaranteed: resource.MustParse("100"),
							},
						},
						{
							Name: "spot",
							Quota: kueue.Quota{
								Guaranteed: resource.MustParse("200"),
							},
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
			},
			"foobar": {
				Name:   "foobar",
				Cohort: &wantCohorts[0],
				RequestableResources: map[corev1.ResourceName][]kueue.ResourceFlavor{
					corev1.ResourceCPU: {
						{
							Name: "spot",
							Quota: kueue.Quota{
								Guaranteed: resource.MustParse("100"),
							},
						},
					},
					"example.com/gpu": {
						{
							Quota: kueue.Quota{
								Guaranteed: resource.MustParse("50"),
							},
						},
					},
				},
				UsedResources: Resources{
					corev1.ResourceCPU: map[string]int64{
						"spot": 10_000,
					},
					"example.com/gpu": map[string]int64{
						"": 15,
					},
				},
				Workloads: map[string]*workload.Info{
					"/beta":  workload.NewInfo(&workloads[1]),
					"/gamma": workload.NewInfo(&workloads[2]),
				},
			},
			"bar": {
				Name: "bar",
				RequestableResources: map[corev1.ResourceName][]kueue.ResourceFlavor{
					corev1.ResourceCPU: {
						{
							Quota: kueue.Quota{
								Guaranteed: resource.MustParse("100"),
							},
						},
					},
				},
				UsedResources: Resources{
					corev1.ResourceCPU: map[string]int64{"": 0},
				},
				Workloads: map[string]*workload.Info{},
			},
		},
	}
	for i, c := range wantCohorts {
		for m := range c.members {
			if m == nil {

			}
			m.Cohort = &wantCohorts[i]
		}
	}
	if diff := cmp.Diff(wantSnapshot, snapshot, cmpopts.IgnoreUnexported(Cohort{})); diff != "" {
		t.Errorf("Unexpected Snapshot (-want,+got):\n%s", diff)
	}
}
