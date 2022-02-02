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

func TestEntryAssignTypes(t *testing.T) {
	cases := map[string]struct {
		wlPods      []kueue.PodSet
		capacity    capacity.Capacity
		wantFits    bool
		wantTypes   map[string]map[corev1.ResourceName]string
		wantBorrows capacity.Resources
	}{
		"single type, fits": {
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
				RequestableResources: map[corev1.ResourceName][]kueue.ResourceType{
					corev1.ResourceCPU:    defaultTypeNoBorrowing("1"),
					corev1.ResourceMemory: defaultTypeNoBorrowing("2Mi"),
				},
			},
			wantFits: true,
			wantTypes: map[string]map[corev1.ResourceName]string{
				"main": {
					corev1.ResourceCPU:    "default",
					corev1.ResourceMemory: "default",
				},
			},
		},
		"single type, used resources, doesn't fit": {
			wlPods: []kueue.PodSet{
				{
					Count: 1,
					Name:  "main",
					Spec: utiltesting.PodSpecForRequest(map[corev1.ResourceName]string{
						corev1.ResourceCPU:    "2",
					}),
				},
			},
			capacity: capacity.Capacity{
				RequestableResources: map[corev1.ResourceName][]kueue.ResourceType{
					corev1.ResourceCPU:    defaultTypeNoBorrowing("4"),
				},
				UsedResources: capacity.Resources{
					corev1.ResourceCPU: {
						"default": 3_000,
					},
				},
			},
		},
		"multiple types, fits": {
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
				RequestableResources: map[corev1.ResourceName][]kueue.ResourceType{
					corev1.ResourceCPU: noBorrowing([]quickType{
						{name: "one", guaranteed: "2"},
						{name: "two", guaranteed: "4"},
					}),
					corev1.ResourceMemory: noBorrowing([]quickType{
						{name: "one", guaranteed: "1Gi"},
						{name: "two", guaranteed: "5Mi"},
					}),
				},
			},
			wantFits: true,
			wantTypes: map[string]map[corev1.ResourceName]string{
				"main": {
					corev1.ResourceCPU:    "two",
					corev1.ResourceMemory: "one",
				},
			},
		},
		"multiple types, doesn't fit": {
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
				RequestableResources: map[corev1.ResourceName][]kueue.ResourceType{
					corev1.ResourceCPU: noBorrowing([]quickType{
						{name: "one", guaranteed: "2"},
						{name: "two", guaranteed: "4"},
					}),
					corev1.ResourceMemory: noBorrowing([]quickType{
						{name: "one", guaranteed: "1Gi"},
						{name: "two", guaranteed: "5Mi"},
					}),
				},
			},
		},
		"multiple specs, fit different types": {
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
				RequestableResources: map[corev1.ResourceName][]kueue.ResourceType{
					corev1.ResourceCPU: noBorrowing([]quickType{
						{name: "one", guaranteed: "4"},
						{name: "two", guaranteed: "10"},
					}),
				},
			},
			wantFits: true,
			wantTypes: map[string]map[corev1.ResourceName]string{
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
				RequestableResources: map[corev1.ResourceName][]kueue.ResourceType{
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
			wantTypes: map[string]map[corev1.ResourceName]string{
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
						corev1.ResourceCPU:    "2",
					}),
				},
			},
			capacity: capacity.Capacity{
				RequestableResources: map[corev1.ResourceName][]kueue.ResourceType{
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
						corev1.ResourceCPU:    "2",
					}),
				},
			},
			capacity: capacity.Capacity{
				RequestableResources: map[corev1.ResourceName][]kueue.ResourceType{
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
			fits := e.assignTypes(&tc.capacity)
			if fits != tc.wantFits {
				t.Errorf("e.assignTypes(_)=%t, want %t", fits, tc.wantFits)
			}
			var types map[string]map[corev1.ResourceName]string
			if fits {
				types = make(map[string]map[corev1.ResourceName]string)
				for name, podSet := range e.TotalRequests {
					types[name] = podSet.Types
				}
			}
			if diff := cmp.Diff(tc.wantTypes, types); diff != "" {
				t.Errorf("Assigned unexpected types (-want,+got):\n%s", diff)
			}
			if diff := cmp.Diff(tc.wantBorrows, e.borrows); diff != "" {
				t.Errorf("Calculated unexpected borrowing (-want,+got):\n%s", diff)
			}
		})
	}
}

func defaultTypeNoBorrowing(guaranteed string) []kueue.ResourceType {
	return noBorrowing([]quickType{{name: "default", guaranteed: guaranteed}})
}

func noBorrowing(ts []quickType) []kueue.ResourceType {
	types := make([]kueue.ResourceType, len(ts))
	for i, t := range ts {
		g := resource.MustParse(t.guaranteed)
		types[i] = kueue.ResourceType{
			Name: t.name,
			Quota: kueue.Quota{
				Guaranteed: g,
				Ceiling:    g,
			},
		}
	}
	return types
}

type quickType struct {
	name       string
	guaranteed string
}
