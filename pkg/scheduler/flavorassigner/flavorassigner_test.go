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
		wantRepMode    FlavorAssignmentMode
		wantAssignment Assignment
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
			wantRepMode: Fit,
			wantAssignment: Assignment{
				PodSets: []PodSetAssignment{{
					Name: "main",
					Flavors: ResourceAssignment{
						corev1.ResourceCPU:    {Name: "default", Mode: Fit},
						corev1.ResourceMemory: {Name: "default", Mode: Fit},
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
			wantRepMode: Fit,
			wantAssignment: Assignment{
				PodSets: []PodSetAssignment{{
					Name: "main",
					Flavors: ResourceAssignment{
						corev1.ResourceCPU: {Name: "tainted", Mode: Fit},
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
			wantRepMode: ClusterQueuePreempt,
			wantAssignment: Assignment{
				PodSets: []PodSetAssignment{{
					Name: "main",
					Flavors: ResourceAssignment{
						corev1.ResourceCPU: {Name: "default", Mode: ClusterQueuePreempt},
					},
					Status: &Status{
						reasons: []string{"insufficient unused quota for cpu flavor default, 1 more needed"},
					},
				}},
			},
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
			wantRepMode: Fit,
			wantAssignment: Assignment{
				PodSets: []PodSetAssignment{{
					Name: "main",
					Flavors: ResourceAssignment{
						corev1.ResourceCPU:    {Name: "two", Mode: Fit},
						corev1.ResourceMemory: {Name: "b_one", Mode: Fit},
					},
				}},
			},
		},
		"multiple independent flavors, one could fit with preemption, other doesn't fit": {
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
							{Name: "one", Min: 3000},
						},
					},
					corev1.ResourceMemory: {
						Flavors: []cache.FlavorLimits{
							{Name: "b_one", Min: utiltesting.Mi},
						},
					},
				},
				UsedResources: cache.ResourceQuantities{
					corev1.ResourceCPU: {
						"one": 1000,
					},
				},
			},
			wantAssignment: Assignment{
				PodSets: []PodSetAssignment{{
					Name: "main",
					Status: &Status{
						reasons: []string{
							"insufficient quota for memory flavor b_one in ClusterQueue",
						},
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
			wantRepMode: Fit,
			wantAssignment: Assignment{
				PodSets: []PodSetAssignment{{
					Name: "main",
					Flavors: ResourceAssignment{
						corev1.ResourceCPU:    {Name: "two", Mode: Fit},
						corev1.ResourceMemory: {Name: "two", Mode: Fit},
						"example.com/gpu":     {Name: "b_one", Mode: Fit},
					},
				}},
			},
		},
		"some codepedent flavors, fits with different modes": {
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
						},
					},
				},
				UsedResources: cache.ResourceQuantities{
					corev1.ResourceMemory: {
						"two": 10 * utiltesting.Mi,
					},
				},
				Cohort: &cache.Cohort{
					RequestableResources: cache.ResourceQuantities{
						corev1.ResourceCPU: {
							"one": 2000,
							"two": 4000,
						},
						corev1.ResourceMemory: {
							"one": utiltesting.Gi,
							"two": 15 * utiltesting.Mi,
						},
						"example.com/gpu": {
							"b_one": 4,
						},
					},
					UsedResources: cache.ResourceQuantities{
						corev1.ResourceMemory: {
							"two": 10 * utiltesting.Mi,
						},
						"example.com/gpu": {
							"b_one": 2,
						},
					},
				},
			},
			wantRepMode: ClusterQueuePreempt,
			wantAssignment: Assignment{
				PodSets: []PodSetAssignment{{
					Name: "main",
					Flavors: ResourceAssignment{
						corev1.ResourceCPU:    {Name: "two", Mode: Fit},
						corev1.ResourceMemory: {Name: "two", Mode: ClusterQueuePreempt},
						"example.com/gpu":     {Name: "b_one", Mode: CohortReclaim},
					},
					Status: &Status{
						reasons: []string{
							"insufficient unused quota in cohort for cpu flavor one, 1 more needed",
							"insufficient unused quota in cohort for memory flavor two, 5Mi more needed",
							"insufficient unused quota in cohort for example.com/gpu flavor b_one, 1 more needed",
						},
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
			wantAssignment: Assignment{
				PodSets: []PodSetAssignment{{
					Name: "main",
					Status: &Status{
						reasons: []string{
							"insufficient quota for cpu flavor one in ClusterQueue",
							"insufficient quota for memory flavor two in ClusterQueue",
						},
					},
				}},
			},
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
			wantRepMode: Fit,
			wantAssignment: Assignment{
				PodSets: []PodSetAssignment{{
					Name: "main",
					Flavors: ResourceAssignment{
						corev1.ResourceCPU: {Name: "two", Mode: Fit},
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
			wantRepMode: Fit,
			wantAssignment: Assignment{
				PodSets: []PodSetAssignment{{
					Name: "main",
					Flavors: ResourceAssignment{
						corev1.ResourceCPU: {Name: "two", Mode: Fit},
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
				LabelKeys: map[corev1.ResourceName]sets.Set[string]{corev1.ResourceCPU: sets.New("cpuType")},
			},
			wantRepMode: Fit,
			wantAssignment: Assignment{
				PodSets: []PodSetAssignment{{
					Name: "main",
					Flavors: ResourceAssignment{
						corev1.ResourceCPU: {Name: "two", Mode: Fit},
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
			wantRepMode: Fit,
			wantAssignment: Assignment{
				PodSets: []PodSetAssignment{{
					Name: "main",
					Flavors: ResourceAssignment{
						corev1.ResourceCPU:    {Name: "two", Mode: Fit},
						corev1.ResourceMemory: {Name: "two", Mode: Fit},
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
			wantRepMode: Fit,
			wantAssignment: Assignment{
				PodSets: []PodSetAssignment{{
					Name: "main",
					Flavors: ResourceAssignment{
						corev1.ResourceCPU: {Name: "one", Mode: Fit},
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
				LabelKeys: map[corev1.ResourceName]sets.Set[string]{corev1.ResourceCPU: sets.New("cpuType")},
			},
			wantAssignment: Assignment{
				PodSets: []PodSetAssignment{{
					Name: "main",
					Status: &Status{
						reasons: []string{
							"flavor one doesn't match with node affinity",
							"flavor two doesn't match with node affinity",
						},
					},
				}},
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
			wantRepMode: Fit,
			wantAssignment: Assignment{
				PodSets: []PodSetAssignment{
					{
						Name: "driver",
						Flavors: ResourceAssignment{
							corev1.ResourceCPU: {Name: "two", Mode: Fit},
						},
					},
					{
						Name: "worker",
						Flavors: ResourceAssignment{
							corev1.ResourceCPU: {Name: "one", Mode: Fit},
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
			wantRepMode: Fit,
			wantAssignment: Assignment{
				PodSets: []PodSetAssignment{
					{
						Name: "driver",
						Flavors: ResourceAssignment{
							corev1.ResourceCPU:    {Name: "default", Mode: Fit},
							corev1.ResourceMemory: {Name: "default", Mode: Fit},
						},
					},
					{
						Name: "worker",
						Flavors: ResourceAssignment{
							corev1.ResourceCPU:    {Name: "default", Mode: Fit},
							corev1.ResourceMemory: {Name: "default", Mode: Fit},
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
			wantAssignment: Assignment{
				PodSets: []PodSetAssignment{{
					Name: "main",
					Status: &Status{
						reasons: []string{"insufficient unused quota in cohort for cpu flavor one, 1 more needed"},
					},
				}},
			},
		},
		"past max, but can preempt in ClusterQueue": {
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
								Min:  2000,
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
			wantRepMode: ClusterQueuePreempt,
			wantAssignment: Assignment{
				PodSets: []PodSetAssignment{{
					Name: "main",
					Flavors: ResourceAssignment{
						corev1.ResourceCPU: {Name: "one", Mode: ClusterQueuePreempt},
					},
					Status: &Status{
						reasons: []string{"borrowing limit for cpu flavor one exceeded"},
					},
				}},
			},
		},
		"past min, but can preempt in ClusterQueue": {
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
								Min:  2000,
							},
						},
					},
				},
				UsedResources: cache.ResourceQuantities{
					corev1.ResourceCPU: {"one": 1_000},
				},
			},
			wantRepMode: ClusterQueuePreempt,
			wantAssignment: Assignment{
				PodSets: []PodSetAssignment{{
					Name: "main",
					Flavors: ResourceAssignment{
						corev1.ResourceCPU: {Name: "one", Mode: ClusterQueuePreempt},
					},
					Status: &Status{
						reasons: []string{"insufficient unused quota for cpu flavor one, 1 more needed"},
					},
				}},
			},
		},
		"past min, but can preempt in cohort": {
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
								Min:  2000,
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
			wantRepMode: CohortReclaim,
			wantAssignment: Assignment{
				PodSets: []PodSetAssignment{{
					Name: "main",
					Flavors: ResourceAssignment{
						corev1.ResourceCPU: {Name: "one", Mode: CohortReclaim},
					},
					Status: &Status{
						reasons: []string{"insufficient unused quota in cohort for cpu flavor one, 1 more needed"},
					},
				}},
			},
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
			wantAssignment: Assignment{
				PodSets: []PodSetAssignment{{
					Name: "main",
					Status: &Status{
						reasons: []string{"resource example.com/gpu unavailable in ClusterQueue"},
					},
				}},
			},
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
			wantAssignment: Assignment{
				PodSets: []PodSetAssignment{{
					Name: "main",
					Status: &Status{
						reasons: []string{"flavor nonexistent-flavor not found"},
					},
				}},
			},
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
			assignment := AssignFlavors(log, wlInfo, resourceFlavors, &tc.clusterQueue)
			if repMode := assignment.RepresentativeMode(); repMode != tc.wantRepMode {
				t.Errorf("e.assignFlavors(_).RepresentativeMode()=%s, want %s", repMode, tc.wantRepMode)
			}
			if diff := cmp.Diff(tc.wantAssignment, assignment, cmpopts.IgnoreUnexported(Assignment{}, FlavorAssignment{})); diff != "" {
				t.Errorf("Unexpected assignment (-want,+got):\n%s", diff)
			}
		})
	}
}
