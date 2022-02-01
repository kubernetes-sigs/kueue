/*
Copyright 2022 Google LLC.

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
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	kueue "gke-internal.googlesource.com/gke-batch/kueue/api/v1alpha1"
	"gke-internal.googlesource.com/gke-batch/kueue/pkg/workload"
)

func TestSnapshot(t *testing.T) {
	cache := NewCache()
	capacities := []kueue.QueueCapacity{
		{
			ObjectMeta: metav1.ObjectMeta{
				Name: "foofoo",
			},
			Spec: kueue.QueueCapacitySpec{
				Cohort: "foo",
				RequestableResources: []kueue.Resource{
					{
						Name: corev1.ResourceCPU,
						Types: []kueue.ResourceType{
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
			Spec: kueue.QueueCapacitySpec{
				Cohort: "foo",
				RequestableResources: []kueue.Resource{
					{
						Name: corev1.ResourceCPU,
						Types: []kueue.ResourceType{
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
						Types: []kueue.ResourceType{
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
			Spec: kueue.QueueCapacitySpec{
				RequestableResources: []kueue.Resource{
					{
						Name: corev1.ResourceCPU,
						Types: []kueue.ResourceType{
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
		// Purposedly do not make a copy of cap. Clones of necessary fields are
		// done in AddCapacity.
		cache.AddCapacity(&cap)
	}
	workloads := []kueue.QueuedWorkload{
		{
			ObjectMeta: metav1.ObjectMeta{Name: "alpha"},
			Spec: kueue.QueuedWorkloadSpec{
				Pods: []kueue.PodSet{
					{
						Count: 5,
						Spec: podSpecForRequest(map[corev1.ResourceName]string{
							corev1.ResourceCPU: "2",
						}),
						AssignedTypes: map[corev1.ResourceName]string{
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
						Spec: podSpecForRequest(map[corev1.ResourceName]string{
							corev1.ResourceCPU: "1",
							"example.com/gpu":  "2",
						}),
						AssignedTypes: map[corev1.ResourceName]string{
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
						Spec: podSpecForRequest(map[corev1.ResourceName]string{
							corev1.ResourceCPU: "1",
							"example.com/gpu":  "1",
						}),
						AssignedTypes: map[corev1.ResourceName]string{
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
		cache.AddWorkload(w.DeepCopy())
	}
	snapshot := cache.Snapshot()
	wantCohorts := []Cohort{
		{
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
				Name:                 "foofoo",
				Cohort:               &wantCohorts[0],
				RequestableResources: capacities[0].Spec.RequestableResources,
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
				Name:                 "foobar",
				Cohort:               &wantCohorts[0],
				RequestableResources: capacities[1].Spec.RequestableResources,
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
				Name:                 "bar",
				RequestableResources: capacities[2].Spec.RequestableResources,
				UsedResources: Resources{
					corev1.ResourceCPU: map[string]int64{"": 0},
				},
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
	if diff := cmp.Diff(wantSnapshot, snapshot, cmpopts.IgnoreUnexported(Cohort{}), cmpopts.IgnoreFields(Capacity{}, "Workloads")); diff != "" {
		t.Errorf("Unexpected Snapshot (-want,+got):\n%s", diff)
	}
}

func podSpecForRequest(request map[corev1.ResourceName]string) corev1.PodSpec {
	rl := make(corev1.ResourceList, len(request))
	for name, val := range request {
		rl[name] = resource.MustParse(val)
	}
	return corev1.PodSpec{
		Containers: []corev1.Container{
			{
				Resources: corev1.ResourceRequirements{
					Requests: rl,
				},
			},
		},
	}
}
