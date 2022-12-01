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

package flavorassigner

import (
	"strings"
	"testing"

	"github.com/go-logr/logr/testr"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/sets"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1alpha2"
	"sigs.k8s.io/kueue/pkg/cache"
	"sigs.k8s.io/kueue/pkg/util/pointer"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	"sigs.k8s.io/kueue/pkg/workload"
)

func TestAssignFlavors(t *testing.T) {
	resourceFlavors := map[string]*kueue.ResourceFlavor{
		"default": {
			ObjectMeta: metav1.ObjectMeta{Name: "default"},
		},
		"one": {
			ObjectMeta: metav1.ObjectMeta{
				Name: "one",
			},
			NodeSelector: map[string]string{"type": "one"},
		},
		"two": {
			ObjectMeta: metav1.ObjectMeta{
				Name: "two",
			},
			NodeSelector: map[string]string{"type": "two"},
		},
		"b_one": {
			ObjectMeta: metav1.ObjectMeta{
				Name: "b_one",
			},
			NodeSelector: map[string]string{"b_type": "one"},
		},
		"b_two": {
			ObjectMeta: metav1.ObjectMeta{
				Name: "b_two",
			},
			NodeSelector: map[string]string{"b_type": "two"},
		},
		"tainted": {
			ObjectMeta: metav1.ObjectMeta{Name: "tainted"},
			Taints: []corev1.Taint{{
				Key:    "instance",
				Value:  "spot",
				Effect: corev1.TaintEffectNoSchedule,
			}},
		},
	}

	cases := map[string]struct {
		wlPods         []kueue.PodSet
		clusterQueue   cache.ClusterQueue
		wantFits       bool
		wantAssignment *Assignment
		wantMsg        string
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
			clusterQueue: cache.ClusterQueue{
				RequestableResources: map[corev1.ResourceName]*cache.Resource{
					corev1.ResourceCPU:    {Flavors: []cache.FlavorLimits{{Name: "default", Min: 1000}}},
					corev1.ResourceMemory: {Flavors: []cache.FlavorLimits{{Name: "default", Min: 2 * utiltesting.Mi}}},
				},
			},
			wantFits: true,
			wantAssignment: &Assignment{
				PodSets: []PodSetAssignment{{
					Name: "main",
					Flavors: ResourceAssignment{
						corev1.ResourceCPU:    {Name: "default", Mode: CohortFit},
						corev1.ResourceMemory: {Name: "default", Mode: CohortFit},
					},
				}},
			},
		},
		"single flavor, fits tainted flavor": {
			wlPods: []kueue.PodSet{
				{
					Count: 1,
					Name:  "main",
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Resources: corev1.ResourceRequirements{
									Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1")},
								},
							},
						},
						Tolerations: []corev1.Toleration{{
							Key:      "instance",
							Operator: corev1.TolerationOpEqual,
							Value:    "spot",
							Effect:   corev1.TaintEffectNoSchedule,
						}},
					},
				},
			},
			clusterQueue: cache.ClusterQueue{
				RequestableResources: map[corev1.ResourceName]*cache.Resource{
					corev1.ResourceCPU: {
						Flavors: []cache.FlavorLimits{{Name: "tainted", Min: 4000}},
					},
				},
			},
			wantFits: true,
			wantAssignment: &Assignment{
				PodSets: []PodSetAssignment{{
					Name: "main",
					Flavors: ResourceAssignment{
						corev1.ResourceCPU: {Name: "tainted", Mode: CohortFit},
					},
				}},
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
			clusterQueue: cache.ClusterQueue{
				RequestableResources: map[corev1.ResourceName]*cache.Resource{
					corev1.ResourceCPU: {Flavors: []cache.FlavorLimits{{Name: "default", Min: 4000}}},
				},
				UsedResources: cache.ResourceQuantities{
					corev1.ResourceCPU: {
						"default": 3_000,
					},
				},
			},
			wantMsg: "insufficient quota for cpu flavor default, 1 more needed",
		},
		"multiple independent flavors, fits": {
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
			clusterQueue: cache.ClusterQueue{
				RequestableResources: map[corev1.ResourceName]*cache.Resource{
					corev1.ResourceCPU: {
						Flavors: []cache.FlavorLimits{
							{Name: "one", Min: 2000},
							{Name: "two", Min: 4000},
						},
					},
					corev1.ResourceMemory: {
						Flavors: []cache.FlavorLimits{
							{Name: "b_one", Min: utiltesting.Gi},
							{Name: "b_two", Min: 5 * utiltesting.Mi},
						},
					},
				},
			},
			wantFits: true,
			wantAssignment: &Assignment{
				PodSets: []PodSetAssignment{{
					Name: "main",
					Flavors: ResourceAssignment{
						corev1.ResourceCPU:    {Name: "two", Mode: CohortFit},
						corev1.ResourceMemory: {Name: "b_one", Mode: CohortFit},
					},
				}},
			},
		},
		"some codependent flavors, fits": {
			wlPods: []kueue.PodSet{
				{
					Count: 1,
					Name:  "main",
					Spec: utiltesting.PodSpecForRequest(map[corev1.ResourceName]string{
						corev1.ResourceCPU:    "3",
						corev1.ResourceMemory: "10Mi",
						"example.com/gpu":     "3",
					}),
				},
			},
			clusterQueue: cache.ClusterQueue{
				RequestableResources: map[corev1.ResourceName]*cache.Resource{
					corev1.ResourceCPU: {
						Flavors: []cache.FlavorLimits{
							{Name: "one", Min: 2000},
							{Name: "two", Min: 4000},
						},
					},
					corev1.ResourceMemory: {
						Flavors: []cache.FlavorLimits{
							{Name: "one", Min: utiltesting.Gi},
							{Name: "two", Min: 15 * utiltesting.Mi},
						},
					},
					"example.com/gpu": {
						Flavors: []cache.FlavorLimits{
							{Name: "b_one", Min: 4},
							{Name: "b_two", Min: 2},
						},
					},
				},
			},
			wantFits: true,
			wantAssignment: &Assignment{
				PodSets: []PodSetAssignment{{
					Name: "main",
					Flavors: ResourceAssignment{
						corev1.ResourceCPU:    {Name: "two", Mode: CohortFit},
						corev1.ResourceMemory: {Name: "two", Mode: CohortFit},
						"example.com/gpu":     {Name: "b_one", Mode: CohortFit},
					},
				}},
			},
		},
		"codependent flavors, doesn't fit": {
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
			clusterQueue: cache.ClusterQueue{
				RequestableResources: map[corev1.ResourceName]*cache.Resource{
					corev1.ResourceCPU: {
						Flavors: []cache.FlavorLimits{
							{Name: "one", Min: 2000},
							{Name: "two", Min: 4000},
						},
					},
					corev1.ResourceMemory: {
						Flavors: []cache.FlavorLimits{
							{Name: "one", Min: utiltesting.Gi},
							{Name: "two", Min: 5 * utiltesting.Mi},
						},
					},
				},
			},
			wantMsg: "insufficient quota for cpu flavor one, 1 more needed; insufficient quota for memory flavor two, 5Mi more needed",
		},
		"multiple flavors, fits while skipping tainted flavor": {
			wlPods: []kueue.PodSet{
				{
					Count: 1,
					Name:  "main",
					Spec: utiltesting.PodSpecForRequest(map[corev1.ResourceName]string{
						corev1.ResourceCPU: "3",
					}),
				},
			},
			clusterQueue: cache.ClusterQueue{
				RequestableResources: map[corev1.ResourceName]*cache.Resource{
					corev1.ResourceCPU: {
						Flavors: []cache.FlavorLimits{
							{Name: "tainted", Min: 4000},
							{Name: "two", Min: 4000},
						},
					},
				},
			},
			wantFits: true,
			wantAssignment: &Assignment{
				PodSets: []PodSetAssignment{{
					Name: "main",
					Flavors: ResourceAssignment{
						corev1.ResourceCPU: {Name: "two", Mode: CohortFit},
					},
				}},
			},
		},
		"multiple flavors, skip missing ResourceFlavor": {
			wlPods: []kueue.PodSet{
				{
					Count: 1,
					Name:  "main",
					Spec: utiltesting.PodSpecForRequest(map[corev1.ResourceName]string{
						corev1.ResourceCPU: "3",
					}),
				},
			},
			clusterQueue: cache.ClusterQueue{
				RequestableResources: map[corev1.ResourceName]*cache.Resource{
					corev1.ResourceCPU: {
						Flavors: []cache.FlavorLimits{
							{Name: "non-existent", Min: 4000},
							{Name: "two", Min: 4000},
						},
					},
				},
			},
			wantFits: true,
			wantAssignment: &Assignment{
				PodSets: []PodSetAssignment{{
					Name: "main",
					Flavors: ResourceAssignment{
						corev1.ResourceCPU: {Name: "two", Mode: CohortFit},
					},
				}},
			},
		},
		"multiple flavors, fits a node selector": {
			wlPods: []kueue.PodSet{
				{
					Count: 1,
					Name:  "main",
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Resources: corev1.ResourceRequirements{
									Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1")},
								},
							},
						},
						// ignored:foo should get ignored
						NodeSelector: map[string]string{"type": "two", "ignored1": "foo"},
						Affinity: &corev1.Affinity{NodeAffinity: &corev1.NodeAffinity{
							RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{
								NodeSelectorTerms: []corev1.NodeSelectorTerm{
									{
										MatchExpressions: []corev1.NodeSelectorRequirement{
											{
												// this expression should get ignored
												Key:      "ignored2",
												Operator: corev1.NodeSelectorOpIn,
												Values:   []string{"bar"},
											},
										},
									},
								},
							}},
						},
					},
				},
			},
			clusterQueue: cache.ClusterQueue{
				RequestableResources: map[corev1.ResourceName]*cache.Resource{
					corev1.ResourceCPU: {
						Flavors: []cache.FlavorLimits{
							{Name: "non-existent", Min: 4000},
							{Name: "one", Min: 4000},
							{Name: "two", Min: 4000},
						},
					},
				},
				LabelKeys: map[corev1.ResourceName]sets.String{corev1.ResourceCPU: sets.NewString("cpuType")},
			},
			wantFits: true,
			wantAssignment: &Assignment{
				PodSets: []PodSetAssignment{{
					Name: "main",
					Flavors: ResourceAssignment{
						corev1.ResourceCPU: {Name: "two", Mode: CohortFit},
					},
				}},
			},
		},
		"multiple flavors, fits with node affinity": {
			wlPods: []kueue.PodSet{
				{
					Count: 1,
					Name:  "main",
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Resources: corev1.ResourceRequirements{
									Requests: corev1.ResourceList{
										corev1.ResourceCPU:    resource.MustParse("1"),
										corev1.ResourceMemory: resource.MustParse("1Mi"),
									},
								},
							},
						},
						NodeSelector: map[string]string{"ignored1": "foo"},
						Affinity: &corev1.Affinity{NodeAffinity: &corev1.NodeAffinity{
							RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{
								NodeSelectorTerms: []corev1.NodeSelectorTerm{
									{
										MatchExpressions: []corev1.NodeSelectorRequirement{
											{
												Key:      "type",
												Operator: corev1.NodeSelectorOpIn,
												Values:   []string{"two"},
											},
										},
									},
								},
							}},
						},
					},
				},
			},
			clusterQueue: cache.ClusterQueue{
				RequestableResources: map[corev1.ResourceName]*cache.Resource{
					corev1.ResourceCPU: {
						Flavors: []cache.FlavorLimits{
							{Name: "one", Min: 4000},
							{Name: "two", Min: 4000},
						},
					},
					corev1.ResourceMemory: {
						Flavors: []cache.FlavorLimits{
							{Name: "one", Min: utiltesting.Gi},
							{Name: "two", Min: utiltesting.Gi},
						},
					},
				},
			},
			wantFits: true,
			wantAssignment: &Assignment{
				PodSets: []PodSetAssignment{{
					Name: "main",
					Flavors: ResourceAssignment{
						corev1.ResourceCPU:    {Name: "two", Mode: CohortFit},
						corev1.ResourceMemory: {Name: "two", Mode: CohortFit},
					},
				}},
			},
		},
		"multiple flavors, node affinity fits any flavor": {
			wlPods: []kueue.PodSet{
				{
					Count: 1,
					Name:  "main",
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Resources: corev1.ResourceRequirements{
									Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1")},
								},
							},
						},
						Affinity: &corev1.Affinity{NodeAffinity: &corev1.NodeAffinity{
							RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{
								NodeSelectorTerms: []corev1.NodeSelectorTerm{
									{
										MatchExpressions: []corev1.NodeSelectorRequirement{
											{
												Key:      "ignored2",
												Operator: corev1.NodeSelectorOpIn,
												Values:   []string{"bar"},
											},
										},
									},
									{
										MatchExpressions: []corev1.NodeSelectorRequirement{
											{
												// although this terms selects two
												// the first term practically matches
												// any flavor; and since the terms
												// are ORed, any flavor can be selected.
												Key:      "cpuType",
												Operator: corev1.NodeSelectorOpIn,
												Values:   []string{"two"},
											},
										},
									},
								},
							}},
						},
					},
				},
			},
			clusterQueue: cache.ClusterQueue{
				RequestableResources: map[corev1.ResourceName]*cache.Resource{
					corev1.ResourceCPU: {
						Flavors: []cache.FlavorLimits{
							{Name: "one", Min: 4000},
							{Name: "two", Min: 4000},
						},
					},
				},
			},
			wantFits: true,
			wantAssignment: &Assignment{
				PodSets: []PodSetAssignment{{
					Name: "main",
					Flavors: ResourceAssignment{
						corev1.ResourceCPU: {Name: "one", Mode: CohortFit},
					},
				}},
			},
		},
		"multiple flavors, doesn't fit node affinity": {
			wlPods: []kueue.PodSet{
				{
					Count: 1,
					Name:  "main",
					Spec: corev1.PodSpec{
						Containers: []corev1.Container{
							{
								Resources: corev1.ResourceRequirements{
									Requests: corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1")},
								},
							},
						},
						Affinity: &corev1.Affinity{NodeAffinity: &corev1.NodeAffinity{
							RequiredDuringSchedulingIgnoredDuringExecution: &corev1.NodeSelector{
								NodeSelectorTerms: []corev1.NodeSelectorTerm{
									{
										MatchExpressions: []corev1.NodeSelectorRequirement{
											{
												Key:      "type",
												Operator: corev1.NodeSelectorOpIn,
												Values:   []string{"three"},
											},
										},
									},
								},
							}},
						},
					},
				},
			},
			clusterQueue: cache.ClusterQueue{
				RequestableResources: map[corev1.ResourceName]*cache.Resource{
					corev1.ResourceCPU: {
						Flavors: []cache.FlavorLimits{
							{Name: "one", Min: 4000},
							{Name: "two", Min: 4000},
						},
					},
				},
				LabelKeys: map[corev1.ResourceName]sets.String{corev1.ResourceCPU: sets.NewString("cpuType")},
			},
			wantFits: false,
			wantMsg:  "flavor one doesn't match with node affinity",
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
			clusterQueue: cache.ClusterQueue{
				RequestableResources: map[corev1.ResourceName]*cache.Resource{
					corev1.ResourceCPU: {
						Flavors: []cache.FlavorLimits{
							{Name: "one", Min: 4000},
							{Name: "two", Min: 10_000},
						},
					},
				},
			},
			wantFits: true,
			wantAssignment: &Assignment{
				PodSets: []PodSetAssignment{
					{
						Name: "driver",
						Flavors: ResourceAssignment{
							corev1.ResourceCPU: {Name: "two", Mode: CohortFit},
						},
					},
					{
						Name: "worker",
						Flavors: ResourceAssignment{
							corev1.ResourceCPU: {Name: "one", Mode: CohortFit},
						},
					},
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
			clusterQueue: cache.ClusterQueue{
				RequestableResources: map[corev1.ResourceName]*cache.Resource{
					corev1.ResourceCPU: {
						Flavors: []cache.FlavorLimits{
							{
								Name: "default",
								Min:  2000,
								Max:  pointer.Int64(100_000),
							},
						},
					},
					corev1.ResourceMemory: {
						Flavors: []cache.FlavorLimits{
							{
								Name: "default",
								Min:  2 * utiltesting.Gi,
								// No max.
							},
						},
					},
				},
				Cohort: &cache.Cohort{
					RequestableResources: cache.ResourceQuantities{
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
			wantAssignment: &Assignment{
				PodSets: []PodSetAssignment{
					{
						Name: "driver",
						Flavors: ResourceAssignment{
							corev1.ResourceCPU:    {Name: "default", Mode: CohortFit},
							corev1.ResourceMemory: {Name: "default", Mode: CohortFit},
						},
					},
					{
						Name: "worker",
						Flavors: ResourceAssignment{
							corev1.ResourceCPU:    {Name: "default", Mode: CohortFit},
							corev1.ResourceMemory: {Name: "default", Mode: CohortFit},
						},
					},
				},
				TotalBorrow: cache.ResourceQuantities{
					corev1.ResourceCPU: {
						"default": 8_000,
					},
					corev1.ResourceMemory: {
						"default": 3 * utiltesting.Gi,
					},
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
			clusterQueue: cache.ClusterQueue{
				RequestableResources: map[corev1.ResourceName]*cache.Resource{
					corev1.ResourceCPU: {
						Flavors: []cache.FlavorLimits{
							{
								Name: "one",
								Min:  1000,
								// No max.
							},
						},
					},
				},
				Cohort: &cache.Cohort{
					RequestableResources: cache.ResourceQuantities{
						corev1.ResourceCPU: {"one": 10_000},
					},
					UsedResources: cache.ResourceQuantities{
						corev1.ResourceCPU: {"one": 9_000},
					},
				},
			},
			wantMsg: "insufficient quota for cpu flavor one, 1 more needed after borrowing",
		},
		"past max": {
			wlPods: []kueue.PodSet{
				{
					Count: 1,
					Name:  "main",
					Spec: utiltesting.PodSpecForRequest(map[corev1.ResourceName]string{
						corev1.ResourceCPU: "2",
					}),
				},
			},
			clusterQueue: cache.ClusterQueue{
				RequestableResources: map[corev1.ResourceName]*cache.Resource{
					corev1.ResourceCPU: {
						Flavors: []cache.FlavorLimits{
							{
								Name: "one",
								Min:  1000,
								Max:  pointer.Int64(10_000),
							},
						},
					},
				},
				UsedResources: cache.ResourceQuantities{
					corev1.ResourceCPU: {"one": 9_000},
				},
				Cohort: &cache.Cohort{
					RequestableResources: cache.ResourceQuantities{
						corev1.ResourceCPU: {"one": 100_000},
					},
					UsedResources: cache.ResourceQuantities{
						corev1.ResourceCPU: {"one": 9_000},
					},
				},
			},
			wantMsg: "borrowing limit for cpu flavor one exceeded",
		},
		"resource not listed in clusterQueue": {
			wlPods: []kueue.PodSet{
				{
					Count: 1,
					Name:  "main",
					Spec: utiltesting.PodSpecForRequest(map[corev1.ResourceName]string{
						"example.com/gpu": "1",
					}),
				},
			},
			clusterQueue: cache.ClusterQueue{
				RequestableResources: map[corev1.ResourceName]*cache.Resource{
					corev1.ResourceCPU: {
						Flavors: []cache.FlavorLimits{
							{Name: "one", Min: 4000},
						},
					},
				},
			},
			wantFits: false,
			wantMsg:  "resource example.com/gpu unavailable in ClusterQueue",
		},
		"resource not found": {
			wlPods: []kueue.PodSet{
				{
					Count: 1,
					Name:  "main",
					Spec: utiltesting.PodSpecForRequest(map[corev1.ResourceName]string{
						"unknown_resource": "1",
					}),
				},
			},
			clusterQueue: cache.ClusterQueue{
				RequestableResources: map[corev1.ResourceName]*cache.Resource{
					corev1.ResourceCPU: {Flavors: []cache.FlavorLimits{{Name: "one", Min: 1000}}},
				},
			},
			wantMsg: "resource unknown_resource unavailable in ClusterQueue",
		},
		"flavor not found": {
			wlPods: []kueue.PodSet{
				{
					Count: 1,
					Name:  "main",
					Spec: utiltesting.PodSpecForRequest(map[corev1.ResourceName]string{
						corev1.ResourceCPU: "1",
					}),
				},
			},
			clusterQueue: cache.ClusterQueue{
				RequestableResources: map[corev1.ResourceName]*cache.Resource{
					corev1.ResourceCPU: {Flavors: []cache.FlavorLimits{{Name: "nonexistent-flavor", Min: 1000}}},
				},
			},
			wantMsg: "flavor nonexistent-flavor not found",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			log := testr.NewWithOptions(t, testr.Options{
				Verbosity: 2,
			})
			tc.clusterQueue.UpdateCodependentResources()
			wlInfo := workload.NewInfo(&kueue.Workload{
				Spec: kueue.WorkloadSpec{
					PodSets: tc.wlPods,
				},
			})
			tc.clusterQueue.UpdateWithFlavors(resourceFlavors)
			assignment, status := AssignFlavors(log, wlInfo, resourceFlavors, &tc.clusterQueue)
			if status.IsSuccess() != tc.wantFits {
				t.Errorf("e.assignFlavors(_)=%t, want %t", status.IsSuccess(), tc.wantFits)
			}
			if !tc.wantFits {
				if len(tc.wantMsg) == 0 || !strings.Contains(status.Message(), tc.wantMsg) {
					t.Errorf("got msg:\n%s\nwant msg containing:\n%s", status.Message(), tc.wantMsg)
				}
			}
			if diff := cmp.Diff(tc.wantAssignment, assignment, cmpopts.IgnoreUnexported(Assignment{}, FlavorAssignment{})); diff != "" {
				t.Errorf("Unexpected assignment (-want,+got):\n%s", diff)
			}
		})
	}
}
