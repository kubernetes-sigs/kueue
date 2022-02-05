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

package scheduler

import (
	"testing"

	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"

	kueue "gke-internal.googlesource.com/gke-batch/kueue/api/v1alpha1"
	"gke-internal.googlesource.com/gke-batch/kueue/pkg/capacity"
	utiltesting "gke-internal.googlesource.com/gke-batch/kueue/pkg/util/testing"
	"gke-internal.googlesource.com/gke-batch/kueue/pkg/workload"
)

func TestEntryAssignFlavors(t *testing.T) {
	cases := map[string]struct {
		wlPods      []kueue.PodSet
		capacity    capacity.Capacity
		wantFits    bool
		wantFlavors map[string]map[corev1.ResourceName]string
		wantBorrows capacity.Resources
	}{
		"single flavor, fits": {
			wlPods: []kueue.PodSet{
				{
					Count: 1,
					Name:  "main",
					Spec: utiltesting.PodSpecForRequest(map[corev1.ResourceName]string{
						corev1.ResourceCPU:    "1",
						corev1.ResourceMemory: "1Mi",
					}),
				},
			},
			capacity: capacity.Capacity{
				RequestableResources: map[corev1.ResourceName][]kueue.ResourceFlavor{
					corev1.ResourceCPU:    defaultFlavorNoBorrowing("1"),
					corev1.ResourceMemory: defaultFlavorNoBorrowing("2Mi"),
				},
			},
			wantFits: true,
			wantFlavors: map[string]map[corev1.ResourceName]string{
				"main": {
					corev1.ResourceCPU:    "default",
					corev1.ResourceMemory: "default",
				},
			},
		},
		"single flavor, used resources, doesn't fit": {
			wlPods: []kueue.PodSet{
				{
					Count: 1,
					Name:  "main",
					Spec: utiltesting.PodSpecForRequest(map[corev1.ResourceName]string{
						corev1.ResourceCPU: "2",
					}),
				},
			},
			capacity: capacity.Capacity{
				RequestableResources: map[corev1.ResourceName][]kueue.ResourceFlavor{
					corev1.ResourceCPU: defaultFlavorNoBorrowing("4"),
				},
				UsedResources: capacity.Resources{
					corev1.ResourceCPU: {
						"default": 3_000,
					},
				},
			},
		},
		"multiple flavors, fits": {
			wlPods: []kueue.PodSet{
				{
					Count: 1,
					Name:  "main",
					Spec: utiltesting.PodSpecForRequest(map[corev1.ResourceName]string{
						corev1.ResourceCPU:    "3",
						corev1.ResourceMemory: "10Mi",
					}),
				},
			},
			capacity: capacity.Capacity{
				RequestableResources: map[corev1.ResourceName][]kueue.ResourceFlavor{
					corev1.ResourceCPU: noBorrowing([]quickFlavor{
						{name: "one", guaranteed: "2"},
						{name: "two", guaranteed: "4"},
					}),
					corev1.ResourceMemory: noBorrowing([]quickFlavor{
						{name: "one", guaranteed: "1Gi"},
						{name: "two", guaranteed: "5Mi"},
					}),
				},
			},
			wantFits: true,
			wantFlavors: map[string]map[corev1.ResourceName]string{
				"main": {
					corev1.ResourceCPU:    "two",
					corev1.ResourceMemory: "one",
				},
			},
		},
		"multiple flavors, doesn't fit": {
			wlPods: []kueue.PodSet{
				{
					Count: 1,
					Name:  "main",
					Spec: utiltesting.PodSpecForRequest(map[corev1.ResourceName]string{
						corev1.ResourceCPU:    "4.1",
						corev1.ResourceMemory: "0.5Gi",
					}),
				},
			},
			capacity: capacity.Capacity{
				RequestableResources: map[corev1.ResourceName][]kueue.ResourceFlavor{
					corev1.ResourceCPU: noBorrowing([]quickFlavor{
						{name: "one", guaranteed: "2"},
						{name: "two", guaranteed: "4"},
					}),
					corev1.ResourceMemory: noBorrowing([]quickFlavor{
						{name: "one", guaranteed: "1Gi"},
						{name: "two", guaranteed: "5Mi"},
					}),
				},
			},
		},
		"multiple specs, fit different flavors": {
			wlPods: []kueue.PodSet{
				{
					Count: 1,
					Name:  "driver",
					Spec: utiltesting.PodSpecForRequest(map[corev1.ResourceName]string{
						corev1.ResourceCPU: "5",
					}),
				},
				{
					Count: 1,
					Name:  "worker",
					Spec: utiltesting.PodSpecForRequest(map[corev1.ResourceName]string{
						corev1.ResourceCPU: "3",
					}),
				},
			},
			capacity: capacity.Capacity{
				RequestableResources: map[corev1.ResourceName][]kueue.ResourceFlavor{
					corev1.ResourceCPU: noBorrowing([]quickFlavor{
						{name: "one", guaranteed: "4"},
						{name: "two", guaranteed: "10"},
					}),
				},
			},
			wantFits: true,
			wantFlavors: map[string]map[corev1.ResourceName]string{
				"driver": {
					corev1.ResourceCPU: "two",
				},
				"worker": {
					corev1.ResourceCPU: "one",
				},
			},
		},
		"multiple specs, fits borrowing": {
			wlPods: []kueue.PodSet{
				{
					Count: 1,
					Name:  "driver",
					Spec: utiltesting.PodSpecForRequest(map[corev1.ResourceName]string{
						corev1.ResourceCPU:    "4",
						corev1.ResourceMemory: "1Gi",
					}),
				},
				{
					Count: 1,
					Name:  "worker",
					Spec: utiltesting.PodSpecForRequest(map[corev1.ResourceName]string{
						corev1.ResourceCPU:    "6",
						corev1.ResourceMemory: "4Gi",
					}),
				},
			},
			capacity: capacity.Capacity{
				RequestableResources: map[corev1.ResourceName][]kueue.ResourceFlavor{
					corev1.ResourceCPU: {
						{
							Name: "default",
							Quota: kueue.Quota{
								Guaranteed: resource.MustParse("2"),
								Ceiling:    resource.MustParse("100"),
							},
						},
					},
					corev1.ResourceMemory: {
						{
							Name: "default",
							Quota: kueue.Quota{
								Guaranteed: resource.MustParse("2Gi"),
								Ceiling:    resource.MustParse("100Gi"),
							},
						},
					},
				},
				Cohort: &capacity.Cohort{
					RequestableResources: capacity.Resources{
						corev1.ResourceCPU: {
							"default": 200_000,
						},
						corev1.ResourceMemory: {
							"default": 200 * utiltesting.Gi,
						},
					},
				},
			},
			wantFits: true,
			wantFlavors: map[string]map[corev1.ResourceName]string{
				"driver": {
					corev1.ResourceCPU:    "default",
					corev1.ResourceMemory: "default",
				},
				"worker": {
					corev1.ResourceCPU:    "default",
					corev1.ResourceMemory: "default",
				},
			},
			wantBorrows: capacity.Resources{
				corev1.ResourceCPU: {
					"default": 8_000,
				},
				corev1.ResourceMemory: {
					"default": 3 * utiltesting.Gi,
				},
			},
		},
		"not enough space to borrow": {
			wlPods: []kueue.PodSet{
				{
					Count: 1,
					Name:  "main",
					Spec: utiltesting.PodSpecForRequest(map[corev1.ResourceName]string{
						corev1.ResourceCPU: "2",
					}),
				},
			},
			capacity: capacity.Capacity{
				RequestableResources: map[corev1.ResourceName][]kueue.ResourceFlavor{
					corev1.ResourceCPU: {
						{
							Name: "one",
							Quota: kueue.Quota{
								Guaranteed: resource.MustParse("1"),
								Ceiling:    resource.MustParse("10"),
							},
						},
					},
				},
				Cohort: &capacity.Cohort{
					RequestableResources: capacity.Resources{
						corev1.ResourceCPU: {"one": 10_000},
					},
					UsedResources: capacity.Resources{
						corev1.ResourceCPU: {"one": 9_000},
					},
				},
			},
		},
		"past ceiling": {
			wlPods: []kueue.PodSet{
				{
					Count: 1,
					Name:  "main",
					Spec: utiltesting.PodSpecForRequest(map[corev1.ResourceName]string{
						corev1.ResourceCPU: "2",
					}),
				},
			},
			capacity: capacity.Capacity{
				RequestableResources: map[corev1.ResourceName][]kueue.ResourceFlavor{
					corev1.ResourceCPU: {
						{
							Name: "one",
							Quota: kueue.Quota{
								Guaranteed: resource.MustParse("1"),
								Ceiling:    resource.MustParse("10"),
							},
						},
					},
				},
				UsedResources: capacity.Resources{
					corev1.ResourceCPU: {"one": 9_000},
				},
				Cohort: &capacity.Cohort{
					RequestableResources: capacity.Resources{
						corev1.ResourceCPU: {"one": 100_000},
					},
					UsedResources: capacity.Resources{
						corev1.ResourceCPU: {"one": 9_000},
					},
				},
			},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			e := entry{
				Info: *workload.NewInfo(&kueue.QueuedWorkload{
					Spec: kueue.QueuedWorkloadSpec{
						Pods: tc.wlPods,
					},
				}),
			}
			fits := e.assignFlavors(&tc.capacity)
			if fits != tc.wantFits {
				t.Errorf("e.assignFlavors(_)=%t, want %t", fits, tc.wantFits)
			}
			var flavors map[string]map[corev1.ResourceName]string
			if fits {
				flavors = make(map[string]map[corev1.ResourceName]string)
				for name, podSet := range e.TotalRequests {
					flavors[name] = podSet.Flavors
				}
			}
			if diff := cmp.Diff(tc.wantFlavors, flavors); diff != "" {
				t.Errorf("Assigned unexpected flavors (-want,+got):\n%s", diff)
			}
			if diff := cmp.Diff(tc.wantBorrows, e.borrows); diff != "" {
				t.Errorf("Calculated unexpected borrowing (-want,+got):\n%s", diff)
			}
		})
	}
}

func defaultFlavorNoBorrowing(guaranteed string) []kueue.ResourceFlavor {
	return noBorrowing([]quickFlavor{{name: "default", guaranteed: guaranteed}})
}

func noBorrowing(ts []quickFlavor) []kueue.ResourceFlavor {
	flavors := make([]kueue.ResourceFlavor, len(ts))
	for i, t := range ts {
		g := resource.MustParse(t.guaranteed)
		flavors[i] = kueue.ResourceFlavor{
			Name: t.name,
			Quota: kueue.Quota{
				Guaranteed: g,
				Ceiling:    g,
			},
		}
	}
	return flavors
}

type quickFlavor struct {
	name       string
	guaranteed string
}
