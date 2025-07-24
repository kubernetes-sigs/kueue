/*
Copyright The Kubernetes Authors.

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
	"sort"
	"testing"

	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/component-base/featuregate"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	tasindexer "sigs.k8s.io/kueue/pkg/controller/tas/indexer"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/resources"
	utiltas "sigs.k8s.io/kueue/pkg/util/tas"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	testingnode "sigs.k8s.io/kueue/pkg/util/testingjobs/node"
	testingpod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
)

// PodSetTestCase defines a test case for a single podset in the consolidated test.
type PodSetTestCase struct {
	podSetName      string
	topologyRequest *kueue.PodSetTopologyRequest
	requests        resources.Requests
	count           int32
	tolerations     []corev1.Toleration
	nodeSelector    map[string]string
	podSetGroupName *string
	wantAssignment  *kueue.TopologyAssignment
	wantReason      string
}

func TestFindTopologyAssignments(t *testing.T) {
	const (
		tasBlockLabel = "cloud.com/topology-block"
		tasRackLabel  = "cloud.com/topology-rack"
	)

	//      b1                   b2
	//   /      \             /      \
	//  r1       r2          r1       r2
	//  |      /  |  \       |         |
	//  x1    x2  x3  x4     x5       x6
	defaultNodes := []corev1.Node{
		*testingnode.MakeNode("b1-r1-x1").
			Label(tasBlockLabel, "b1").
			Label(tasRackLabel, "r1").
			Label(corev1.LabelHostname, "x1").
			StatusAllocatable(corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("1"),
				corev1.ResourceMemory: resource.MustParse("1Gi"),
				corev1.ResourcePods:   resource.MustParse("10"),
			}).
			Ready().
			Obj(),
		*testingnode.MakeNode("b1-r2-x2").
			Label(tasBlockLabel, "b1").
			Label(tasRackLabel, "r2").
			Label(corev1.LabelHostname, "x2").
			StatusAllocatable(corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("1"),
				corev1.ResourceMemory: resource.MustParse("1Gi"),
				corev1.ResourcePods:   resource.MustParse("10"),
			}).
			Ready().
			Obj(),
		*testingnode.MakeNode("b1-r2-x3").
			Label(tasBlockLabel, "b1").
			Label(tasRackLabel, "r2").
			Label(corev1.LabelHostname, "x3").
			StatusAllocatable(corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("1"),
				corev1.ResourceMemory: resource.MustParse("1Gi"),
				corev1.ResourcePods:   resource.MustParse("10"),
			}).
			Ready().
			Obj(),
		*testingnode.MakeNode("b1-r2-x4").
			Label(tasBlockLabel, "b1").
			Label(tasRackLabel, "r2").
			Label(corev1.LabelHostname, "x4").
			StatusAllocatable(corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("1"),
				corev1.ResourceMemory: resource.MustParse("1Gi"),
				corev1.ResourcePods:   resource.MustParse("10"),
			}).
			Ready().
			Obj(),
		*testingnode.MakeNode("b2-r2-x5").
			Label(tasBlockLabel, "b2").
			Label(tasRackLabel, "r1").
			Label(corev1.LabelHostname, "x5").
			StatusAllocatable(corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("1"),
				corev1.ResourceMemory: resource.MustParse("1Gi"),
				corev1.ResourcePods:   resource.MustParse("10"),
			}).
			Ready().
			Obj(),
		*testingnode.MakeNode("b2-r2-x6").
			Label(tasBlockLabel, "b2").
			Label(tasRackLabel, "r2").
			Label(corev1.LabelHostname, "x6").
			StatusAllocatable(corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("2"),
				corev1.ResourceMemory: resource.MustParse("4Gi"),
				corev1.ResourcePods:   resource.MustParse("40"),
			}).
			Ready().
			Obj(),
	}
	//nolint:dupword // suppress duplicate r1 word
	//       b1           b2
	//       |             |
	//       r1           r1
	//     /  |  \       /  \
	//   x1  x2  x3     x4  x5
	scatteredNodes := []corev1.Node{
		*testingnode.MakeNode("b1-r1-x1").
			Label(tasBlockLabel, "b1").
			Label(tasRackLabel, "r1").
			Label(corev1.LabelHostname, "x1").
			StatusAllocatable(corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("4"),
				corev1.ResourceMemory: resource.MustParse("1Gi"),
				corev1.ResourcePods:   resource.MustParse("10"),
			}).
			Ready().
			Obj(),
		*testingnode.MakeNode("b1-r1-x2").
			Label(tasBlockLabel, "b1").
			Label(tasRackLabel, "r1").
			Label(corev1.LabelHostname, "x2").
			StatusAllocatable(corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("1"),
				corev1.ResourceMemory: resource.MustParse("1Gi"),
				corev1.ResourcePods:   resource.MustParse("10"),
			}).
			Ready().
			Obj(),
		*testingnode.MakeNode("b1-r1-x3").
			Label(tasBlockLabel, "b1").
			Label(tasRackLabel, "r1").
			Label(corev1.LabelHostname, "x3").
			StatusAllocatable(corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("1"),
				corev1.ResourceMemory: resource.MustParse("1Gi"),
				corev1.ResourcePods:   resource.MustParse("10"),
			}).
			Ready().
			Obj(),
		*testingnode.MakeNode("b2-r1-x4").
			Label(tasBlockLabel, "b2").
			Label(tasRackLabel, "r1").
			Label(corev1.LabelHostname, "x4").
			StatusAllocatable(corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("2"),
				corev1.ResourceMemory: resource.MustParse("1Gi"),
				corev1.ResourcePods:   resource.MustParse("10"),
			}).
			Ready().
			Obj(),
		*testingnode.MakeNode("b2-r1-x5").
			Label(tasBlockLabel, "b2").
			Label(tasRackLabel, "r1").
			Label(corev1.LabelHostname, "x5").
			StatusAllocatable(corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("1"),
				corev1.ResourceMemory: resource.MustParse("1Gi"),
				corev1.ResourcePods:   resource.MustParse("10"),
			}).
			Ready().
			Obj(),
	}
	defaultOneLevel := []string{
		corev1.LabelHostname,
	}
	defaultTwoLevels := []string{
		tasBlockLabel,
		tasRackLabel,
	}
	defaultThreeLevels := []string{
		tasBlockLabel,
		tasRackLabel,
		corev1.LabelHostname,
	}

	//           b1                    b2
	//       /        \             /      \
	//      r1         r2          r1       r2
	//     /  \      /   \        /   \    /   \
	//    x1   x2   x3    x4     x5   x6  x7    x6
	binaryTreesNodes := []corev1.Node{
		*testingnode.MakeNode("b1-r1-x1").
			Label(tasBlockLabel, "b1").
			Label(tasRackLabel, "r1").
			Label(corev1.LabelHostname, "x1").
			StatusAllocatable(corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("1"),
				corev1.ResourceMemory: resource.MustParse("1Gi"),
				corev1.ResourcePods:   resource.MustParse("10"),
			}).
			Ready().
			Obj(),
		*testingnode.MakeNode("b1-r1-x2").
			Label(tasBlockLabel, "b1").
			Label(tasRackLabel, "r1").
			Label(corev1.LabelHostname, "x2").
			StatusAllocatable(corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("1"),
				corev1.ResourceMemory: resource.MustParse("1Gi"),
				corev1.ResourcePods:   resource.MustParse("10"),
			}).
			Ready().
			Obj(),
		*testingnode.MakeNode("b1-r2-x3").
			Label(tasBlockLabel, "b1").
			Label(tasRackLabel, "r2").
			Label(corev1.LabelHostname, "x3").
			StatusAllocatable(corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("1"),
				corev1.ResourceMemory: resource.MustParse("1Gi"),
				corev1.ResourcePods:   resource.MustParse("10"),
			}).
			Ready().
			Obj(),
		*testingnode.MakeNode("b1-r2-x4").
			Label(tasBlockLabel, "b1").
			Label(tasRackLabel, "r2").
			Label(corev1.LabelHostname, "x4").
			StatusAllocatable(corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("1"),
				corev1.ResourceMemory: resource.MustParse("1Gi"),
				corev1.ResourcePods:   resource.MustParse("10"),
			}).
			Ready().
			Obj(),
		*testingnode.MakeNode("b2-r1-x5").
			Label(tasBlockLabel, "b2").
			Label(tasRackLabel, "r1").
			Label(corev1.LabelHostname, "x5").
			StatusAllocatable(corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("1"),
				corev1.ResourceMemory: resource.MustParse("1Gi"),
				corev1.ResourcePods:   resource.MustParse("10"),
			}).
			Ready().
			Obj(),
		*testingnode.MakeNode("b2-r1-x6").
			Label(tasBlockLabel, "b2").
			Label(tasRackLabel, "r1").
			Label(corev1.LabelHostname, "x6").
			StatusAllocatable(corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("1"),
				corev1.ResourceMemory: resource.MustParse("1Gi"),
				corev1.ResourcePods:   resource.MustParse("10"),
			}).
			Ready().
			Obj(),
		*testingnode.MakeNode("b2-r2-x7").
			Label(tasBlockLabel, "b2").
			Label(tasRackLabel, "r2").
			Label(corev1.LabelHostname, "x7").
			StatusAllocatable(corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("1"),
				corev1.ResourceMemory: resource.MustParse("1Gi"),
				corev1.ResourcePods:   resource.MustParse("10"),
			}).
			Ready().
			Obj(),
		*testingnode.MakeNode("b2-r2-x8").
			Label(tasBlockLabel, "b2").
			Label(tasRackLabel, "r2").
			Label(corev1.LabelHostname, "x8").
			StatusAllocatable(corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("1"),
				corev1.ResourceMemory: resource.MustParse("1Gi"),
				corev1.ResourcePods:   resource.MustParse("10"),
			}).
			Ready().
			Obj(),
	}

	cases := map[string]struct {
		enableFeatureGates []featuregate.Feature
		nodes              []corev1.Node
		pods               []corev1.Pod
		levels             []string
		nodeLabels         map[string]string
		podSets            []PodSetTestCase
	}{
		"minimize the number of used racks before optimizing the number of nodes; BestFit": {
			// Solution by optimizing the number of racks then nodes: [r3]: [x3,x4,x5,x6]
			// Solution by optimizing the number of nodes: [r1,r2]: [x1,x2]
			//
			//       b1
			//   /   |    \
			//  r1   r2   r3
			//  |     |    |   \   \     \
			// x1:2,x2:2,x3:1,x4:1,x5:1,x6:1
			//
			nodes: []corev1.Node{
				*testingnode.MakeNode("b1-r1-x1").
					Label(tasBlockLabel, "b1").
					Label(tasRackLabel, "r1").
					Label(corev1.LabelHostname, "x1").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("2"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r2-x2").
					Label(tasBlockLabel, "b1").
					Label(tasRackLabel, "r2").
					Label(corev1.LabelHostname, "x2").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("2"),
						corev1.ResourcePods: resource.MustParse("20"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r3-x3").
					Label(tasBlockLabel, "b1").
					Label(tasRackLabel, "r3").
					Label(corev1.LabelHostname, "x3").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("1"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r3-x4").
					Label(tasBlockLabel, "b1").
					Label(tasRackLabel, "r3").
					Label(corev1.LabelHostname, "x4").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("1"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r3-x5").
					Label(tasBlockLabel, "b1").
					Label(tasRackLabel, "r3").
					Label(corev1.LabelHostname, "x5").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("1"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r3-x6").
					Label(tasBlockLabel, "b1").
					Label(tasRackLabel, "r3").
					Label(corev1.LabelHostname, "x6").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("1"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
			},
			levels: defaultThreeLevels,
			podSets: []PodSetTestCase{{
				topologyRequest: &kueue.PodSetTopologyRequest{
					Required: ptr.To(tasBlockLabel),
				},
				requests: resources.Requests{
					corev1.ResourceCPU: 1000,
				},
				count: 4,
				wantAssignment: &kueue.TopologyAssignment{
					Levels: defaultOneLevel,
					Domains: []kueue.TopologyDomainAssignment{
						{
							Count: 1,
							Values: []string{
								"x3",
							},
						},
						{
							Count: 1,
							Values: []string{
								"x4",
							},
						},
						{
							Count: 1,
							Values: []string{
								"x5",
							},
						},
						{
							Count: 1,
							Values: []string{
								"x6",
							},
						},
					},
				},
			}},
			enableFeatureGates: []featuregate.Feature{},
		},
		"minimize resource fragmentation; LeastFreeCapacityFit": {
			//        b1
			//         |
			//        r1
			//    /    |    \
			// x1:2, x2:1, x3:1
			//
			nodes: []corev1.Node{
				*testingnode.MakeNode("b1-r1-x1").
					Label(tasBlockLabel, "b1").
					Label(tasRackLabel, "r1").
					Label(corev1.LabelHostname, "x1").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("2"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r1-x2").
					Label(tasBlockLabel, "b1").
					Label(tasRackLabel, "r1").
					Label(corev1.LabelHostname, "x2").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("1"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r1-x3").
					Label(tasBlockLabel, "b1").
					Label(tasRackLabel, "r1").
					Label(corev1.LabelHostname, "x3").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("1"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
			},
			levels: defaultThreeLevels,
			podSets: []PodSetTestCase{{
				topologyRequest: &kueue.PodSetTopologyRequest{
					Required: ptr.To(tasBlockLabel),
				},
				requests: resources.Requests{
					corev1.ResourceCPU: 1000,
				},
				count: 2,
				wantAssignment: &kueue.TopologyAssignment{
					Levels: defaultOneLevel,
					Domains: []kueue.TopologyDomainAssignment{
						{
							Count: 1,
							Values: []string{
								"x3",
							},
						},
						{
							Count: 1,
							Values: []string{
								"x2",
							},
						},
					},
				},
			}},
			enableFeatureGates: []featuregate.Feature{features.TASProfileLeastFreeCapacity},
		},
		"choose the node that can accommodate all Pods": {
			//        b1
			//         |
			//        r1
			//    /    |    \
			// x1:2, x2:1, x3:1
			nodes: []corev1.Node{
				*testingnode.MakeNode("b1-r1-x1").
					Label(tasBlockLabel, "b1").
					Label(tasRackLabel, "r1").
					Label(corev1.LabelHostname, "x1").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("2"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r1-x2").
					Label(tasBlockLabel, "b1").
					Label(tasRackLabel, "r1").
					Label(corev1.LabelHostname, "x2").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("1"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r1-x3").
					Label(tasBlockLabel, "b1").
					Label(tasRackLabel, "r1").
					Label(corev1.LabelHostname, "x3").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("1"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
			},
			levels: defaultThreeLevels,
			podSets: []PodSetTestCase{{
				topologyRequest: &kueue.PodSetTopologyRequest{
					Required: ptr.To(tasBlockLabel),
				},
				requests: resources.Requests{
					corev1.ResourceCPU: 1000,
				},
				count: 2,
				wantAssignment: &kueue.TopologyAssignment{
					Levels: defaultOneLevel,
					Domains: []kueue.TopologyDomainAssignment{
						{
							Count: 2,
							Values: []string{
								"x1",
							},
						},
					},
				},
			}},
		},
		"unconstrained; 2 pods fit into hosts scattered across the whole datacenter even they could fit into single rack; LeastFreeCapacity": {
			nodes:  scatteredNodes,
			levels: defaultThreeLevels,
			podSets: []PodSetTestCase{{
				topologyRequest: &kueue.PodSetTopologyRequest{
					Unconstrained: ptr.To(true),
				},
				requests: resources.Requests{
					corev1.ResourceCPU: 1000,
				},
				count: 2,
				wantAssignment: &kueue.TopologyAssignment{
					Levels: defaultOneLevel,
					Domains: []kueue.TopologyDomainAssignment{
						{
							Count: 2,
							Values: []string{
								"x4",
							},
						},
					},
				},
			}},
			enableFeatureGates: []featuregate.Feature{features.TASProfileLeastFreeCapacity},
		},
		"no annotation; implied default to unconstrained; 6 pods fit into hosts scattered across the whole datacenter even they could fit into single rack; BestFit": {
			nodes:  scatteredNodes,
			levels: defaultThreeLevels,
			podSets: []PodSetTestCase{{
				requests: resources.Requests{
					corev1.ResourceCPU: 1000,
				},
				count: 6,
				wantAssignment: &kueue.TopologyAssignment{
					Levels: defaultOneLevel,
					Domains: []kueue.TopologyDomainAssignment{
						{
							Count: 4,
							Values: []string{
								"x1",
							},
						},
						{
							Count: 2,
							Values: []string{
								"x4",
							},
						},
					},
				},
			}},
		},
		"unconstrained; 6 pods fit into hosts scattered across the whole datacenter even they could fit into single rack; BestFit": {
			nodes:  scatteredNodes,
			levels: defaultThreeLevels,
			podSets: []PodSetTestCase{{
				topologyRequest: &kueue.PodSetTopologyRequest{
					Unconstrained: ptr.To(true),
				},
				requests: resources.Requests{
					corev1.ResourceCPU: 1000,
				},
				count: 6,
				wantAssignment: &kueue.TopologyAssignment{
					Levels: defaultOneLevel,
					Domains: []kueue.TopologyDomainAssignment{
						{
							Count: 4,
							Values: []string{
								"x1",
							},
						},
						{
							Count: 2,
							Values: []string{
								"x4",
							},
						},
					},
				},
			}},
		},
		"unconstrained; a single pod fits into each host; BestFit": {
			nodes:  defaultNodes,
			levels: defaultThreeLevels,
			podSets: []PodSetTestCase{{
				topologyRequest: &kueue.PodSetTopologyRequest{
					Unconstrained: ptr.To(true),
				},
				requests: resources.Requests{
					corev1.ResourceCPU: 1000,
				},
				count: 1,
				wantAssignment: &kueue.TopologyAssignment{
					Levels: defaultOneLevel,
					Domains: []kueue.TopologyDomainAssignment{
						{
							Count: 1,
							Values: []string{
								"x1",
							},
						},
					},
				},
			}},
		},
		"unconstrained; a single pod fits into each host; LeastFreeCapacity; TASProfileLeastFreeCapacity": {
			nodes:  defaultNodes,
			levels: defaultThreeLevels,
			podSets: []PodSetTestCase{{
				topologyRequest: &kueue.PodSetTopologyRequest{
					Unconstrained: ptr.To(true),
				},
				requests: resources.Requests{
					corev1.ResourceCPU: 1000,
				},
				count: 1,
				wantAssignment: &kueue.TopologyAssignment{
					Levels: defaultOneLevel,
					Domains: []kueue.TopologyDomainAssignment{
						{
							Count: 1,
							Values: []string{
								"x1",
							},
						},
					},
				},
			}},
			enableFeatureGates: []featuregate.Feature{features.TASProfileLeastFreeCapacity},
		},
		"unconstrained; a single pod fits into each host; LeastFreeCapacity; TASProfileMixed": {
			nodes:  defaultNodes,
			levels: defaultThreeLevels,
			podSets: []PodSetTestCase{{
				topologyRequest: &kueue.PodSetTopologyRequest{
					Unconstrained: ptr.To(true),
				},
				requests: resources.Requests{
					corev1.ResourceCPU: 1000,
				},
				count: 1,
				wantAssignment: &kueue.TopologyAssignment{
					Levels: defaultOneLevel,
					Domains: []kueue.TopologyDomainAssignment{
						{
							Count: 1,
							Values: []string{
								"x1",
							},
						},
					},
				},
			}},
			enableFeatureGates: []featuregate.Feature{features.TASProfileMixed},
		},
		"block required; 4 pods fit into one host each; BestFit": {
			nodes:  binaryTreesNodes,
			levels: defaultThreeLevels,
			podSets: []PodSetTestCase{{
				topologyRequest: &kueue.PodSetTopologyRequest{
					Required: ptr.To(tasBlockLabel),
				},
				requests: resources.Requests{
					corev1.ResourceCPU: 1000,
				},
				count: 4,
				wantAssignment: &kueue.TopologyAssignment{
					Levels: defaultOneLevel,
					Domains: []kueue.TopologyDomainAssignment{
						{
							Count: 1,
							Values: []string{
								"x1",
							},
						},
						{
							Count: 1,
							Values: []string{
								"x2",
							},
						},
						{
							Count: 1,
							Values: []string{
								"x3",
							},
						},
						{
							Count: 1,
							Values: []string{
								"x4",
							},
						},
					},
				},
			}},
		},
		"host required; single Pod fits in the host; LeastFreeCapacityFit": {
			nodes:  defaultNodes,
			levels: defaultThreeLevels,
			podSets: []PodSetTestCase{{
				topologyRequest: &kueue.PodSetTopologyRequest{
					Required: ptr.To(corev1.LabelHostname),
				},
				requests: resources.Requests{
					corev1.ResourceCPU: 1000,
				},
				count: 1,
				wantAssignment: &kueue.TopologyAssignment{
					Levels: defaultOneLevel,
					Domains: []kueue.TopologyDomainAssignment{
						{
							Count: 1,
							Values: []string{
								"x1",
							},
						},
					},
				},
			}},
			enableFeatureGates: []featuregate.Feature{features.TASProfileLeastFreeCapacity},
		},
		"host required; single Pod fits in the host; BestFit": {
			nodes:  defaultNodes,
			levels: defaultThreeLevels,
			podSets: []PodSetTestCase{{
				topologyRequest: &kueue.PodSetTopologyRequest{
					Required: ptr.To(corev1.LabelHostname),
				},
				requests: resources.Requests{
					corev1.ResourceCPU: 1000,
				},
				count: 1,
				wantAssignment: &kueue.TopologyAssignment{
					Levels: defaultOneLevel,
					Domains: []kueue.TopologyDomainAssignment{
						{
							Count: 1,
							Values: []string{
								"x1",
							},
						},
					},
				},
			}},
		},
		"host required; single Pod fits in the host; BestFit; TASProfileMixed": {
			nodes:  defaultNodes,
			levels: defaultThreeLevels,
			podSets: []PodSetTestCase{{
				topologyRequest: &kueue.PodSetTopologyRequest{
					Required: ptr.To(corev1.LabelHostname),
				},
				requests: resources.Requests{
					corev1.ResourceCPU: 1000,
				},
				count: 1,
				wantAssignment: &kueue.TopologyAssignment{
					Levels: defaultOneLevel,
					Domains: []kueue.TopologyDomainAssignment{
						{
							Count: 1,
							Values: []string{
								"x1",
							},
						},
					},
				},
			}},
			enableFeatureGates: []featuregate.Feature{features.TASProfileMixed},
		},
		"host required; single Pod fits in the largest host; LeastFreeCapacityFit": {
			nodes:  defaultNodes,
			levels: defaultThreeLevels,
			podSets: []PodSetTestCase{{
				topologyRequest: &kueue.PodSetTopologyRequest{
					Required: ptr.To(corev1.LabelHostname),
				},
				requests: resources.Requests{
					corev1.ResourceCPU: 1000,
				},
				count: 2,
				wantAssignment: &kueue.TopologyAssignment{
					Levels: defaultOneLevel,
					Domains: []kueue.TopologyDomainAssignment{
						{
							Count: 2,
							Values: []string{
								"x6",
							},
						},
					},
				},
			}},
			enableFeatureGates: []featuregate.Feature{features.TASProfileLeastFreeCapacity},
		},
		"host preferred; single Pod fits in the largest host; LeastFreeCapacityFit": {
			nodes:  defaultNodes,
			levels: defaultThreeLevels,
			podSets: []PodSetTestCase{{
				topologyRequest: &kueue.PodSetTopologyRequest{
					Preferred: ptr.To(corev1.LabelHostname),
				},
				requests: resources.Requests{
					corev1.ResourceCPU: 1000,
				},
				count: 2,
				wantAssignment: &kueue.TopologyAssignment{
					Levels: defaultOneLevel,
					Domains: []kueue.TopologyDomainAssignment{
						{
							Count: 2,
							Values: []string{
								"x6",
							},
						},
					},
				},
			}},
			enableFeatureGates: []featuregate.Feature{features.TASProfileLeastFreeCapacity},
		},
		"host required; no single host fits all pods, expect notFitMessage; LeastFreeCapacityFit": {
			nodes:  defaultNodes,
			levels: defaultThreeLevels,
			podSets: []PodSetTestCase{{
				topologyRequest: &kueue.PodSetTopologyRequest{
					Required: ptr.To(corev1.LabelHostname),
				},
				requests: resources.Requests{
					corev1.ResourceCPU: 1000,
				},
				count:      3,
				wantReason: `topology "default" allows to fit only 2 out of 3 pod(s)`,
			}},
			enableFeatureGates: []featuregate.Feature{features.TASProfileLeastFreeCapacity},
		},
		"host preferred; single Pod fits in the host; BestFit; TASProfileMixed": {
			nodes:  defaultNodes,
			levels: defaultThreeLevels,
			podSets: []PodSetTestCase{{
				topologyRequest: &kueue.PodSetTopologyRequest{
					Required: ptr.To(corev1.LabelHostname),
				},
				requests: resources.Requests{
					corev1.ResourceCPU: 1000,
				},
				count: 1,
				wantAssignment: &kueue.TopologyAssignment{
					Levels: defaultOneLevel,
					Domains: []kueue.TopologyDomainAssignment{
						{
							Count: 1,
							Values: []string{
								"x1",
							},
						},
					},
				},
			}},
			enableFeatureGates: []featuregate.Feature{features.TASProfileMixed},
		},
		"rack required; single Pod fits in a rack; BestFit": {
			nodes:  defaultNodes,
			levels: defaultTwoLevels,
			podSets: []PodSetTestCase{{
				topologyRequest: &kueue.PodSetTopologyRequest{
					Required: ptr.To(tasRackLabel),
				},
				requests: resources.Requests{
					corev1.ResourceCPU: 1000,
				},
				count: 1,
				wantAssignment: &kueue.TopologyAssignment{
					Levels: defaultTwoLevels,
					Domains: []kueue.TopologyDomainAssignment{
						{
							Count: 1,
							Values: []string{
								"b1",
								"r1",
							},
						},
					},
				},
			}},
		},
		"rack required; single Pod fits in a rack; LeastFreeCapacityFit": {
			nodes:  defaultNodes,
			levels: defaultThreeLevels,
			podSets: []PodSetTestCase{{
				topologyRequest: &kueue.PodSetTopologyRequest{
					Required: ptr.To(tasRackLabel),
				},
				requests: resources.Requests{
					corev1.ResourceCPU: 1000,
				},
				count: 1,
				wantAssignment: &kueue.TopologyAssignment{
					Levels: defaultOneLevel,
					Domains: []kueue.TopologyDomainAssignment{
						{
							Count: 1,
							Values: []string{
								"x1",
							},
						},
					},
				},
			}},
			enableFeatureGates: []featuregate.Feature{features.TASProfileLeastFreeCapacity},
		},
		"rack preferred; multiple Pods fits in multiple racks; LeastFreeCapacityFit": {
			nodes:  defaultNodes,
			levels: defaultTwoLevels,
			podSets: []PodSetTestCase{{
				topologyRequest: &kueue.PodSetTopologyRequest{
					Preferred: ptr.To(tasRackLabel),
				},
				requests: resources.Requests{
					corev1.ResourceCPU: 1000,
				},
				count: 2,
				wantAssignment: &kueue.TopologyAssignment{
					Levels: defaultTwoLevels,
					Domains: []kueue.TopologyDomainAssignment{
						{
							Count: 2,
							Values: []string{
								"b2",
								"r2",
							},
						},
					},
				},
			}},
			enableFeatureGates: []featuregate.Feature{features.TASProfileLeastFreeCapacity},
		},
		"rack required; multiple Pods fit in a rack; BestFit": {
			nodes:  defaultNodes,
			levels: defaultTwoLevels,
			podSets: []PodSetTestCase{{
				topologyRequest: &kueue.PodSetTopologyRequest{
					Required: ptr.To(tasRackLabel),
				},
				requests: resources.Requests{
					corev1.ResourceCPU: 1000,
				},
				count: 3,
				wantAssignment: &kueue.TopologyAssignment{
					Levels: defaultTwoLevels,
					Domains: []kueue.TopologyDomainAssignment{
						{
							Count: 3,
							Values: []string{
								"b1",
								"r2",
							},
						},
					},
				},
			}},
		},
		"block preferred; Pods fit in 2 blocks; BestFit": {
			nodes: []corev1.Node{
				*testingnode.MakeNode("b1").
					Label(tasBlockLabel, "b1").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("2"),
						corev1.ResourcePods: resource.MustParse("20"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b2").
					Label(tasBlockLabel, "b2").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("1"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b3").
					Label(tasBlockLabel, "b3").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("4"),
						corev1.ResourcePods: resource.MustParse("40"),
					}).
					Ready().
					Obj(),
			},
			levels: []string{tasBlockLabel},
			podSets: []PodSetTestCase{{
				topologyRequest: &kueue.PodSetTopologyRequest{
					Preferred: ptr.To(tasBlockLabel),
				},
				requests: resources.Requests{
					corev1.ResourceCPU: 1000,
				},
				count: 5,
				wantAssignment: &kueue.TopologyAssignment{
					Levels: []string{tasBlockLabel},
					Domains: []kueue.TopologyDomainAssignment{
						{
							Count: 1,
							Values: []string{
								"b2",
							},
						},
						{
							Count: 4,
							Values: []string{
								"b3",
							},
						},
					},
				},
			}},
		},
		"rack required; multiple Pods fit in some racks; BestFit": {
			nodes:  defaultNodes,
			levels: defaultTwoLevels,
			podSets: []PodSetTestCase{{
				topologyRequest: &kueue.PodSetTopologyRequest{
					Required: ptr.To(tasRackLabel),
				},
				requests: resources.Requests{
					corev1.ResourceCPU: 1000,
				},
				count: 2,
				wantAssignment: &kueue.TopologyAssignment{
					Levels: defaultTwoLevels,
					Domains: []kueue.TopologyDomainAssignment{
						{
							Count: 2,
							Values: []string{
								"b2",
								"r2",
							},
						},
					},
				},
			}},
		},
		"rack required; too many pods to fit in any rack; BestFit": {
			nodes:  defaultNodes,
			levels: defaultTwoLevels,
			podSets: []PodSetTestCase{{
				topologyRequest: &kueue.PodSetTopologyRequest{
					Required: ptr.To(tasRackLabel),
				},
				requests: resources.Requests{
					corev1.ResourceCPU: 1000,
				},
				count:      4,
				wantReason: `topology "default" allows to fit only 3 out of 4 pod(s)`,
			}},
		},
		"block required; single Pod fits in a block; LeastFreeCapacityFit": {
			nodes:  defaultNodes,
			levels: defaultThreeLevels,
			podSets: []PodSetTestCase{{
				topologyRequest: &kueue.PodSetTopologyRequest{
					Required: ptr.To(tasBlockLabel),
				},
				requests: resources.Requests{
					corev1.ResourceCPU: 1000,
				},
				count: 1,
				wantAssignment: &kueue.TopologyAssignment{
					Levels: defaultOneLevel,
					Domains: []kueue.TopologyDomainAssignment{
						{
							Count: 1,
							Values: []string{
								"x5",
							},
						},
					},
				},
			}},
			enableFeatureGates: []featuregate.Feature{features.TASProfileLeastFreeCapacity},
		},
		"block required; two Pods fits in a block; LeastFreeCapacityFit": {
			nodes:  defaultNodes,
			levels: defaultThreeLevels,
			podSets: []PodSetTestCase{{
				topologyRequest: &kueue.PodSetTopologyRequest{
					Required: ptr.To(tasBlockLabel),
				},
				requests: resources.Requests{
					corev1.ResourceCPU: 1000,
				},
				count: 2,
				wantAssignment: &kueue.TopologyAssignment{
					Levels: defaultOneLevel,
					Domains: []kueue.TopologyDomainAssignment{
						{
							Count: 1,
							Values: []string{
								"x5",
							},
						},
						{
							Count: 1,
							Values: []string{
								"x6",
							},
						},
					},
				},
			}},
			enableFeatureGates: []featuregate.Feature{features.TASProfileLeastFreeCapacity},
		},
		"block required; single Pod fits in a block and a single rack; BestFit": {
			nodes:  defaultNodes,
			levels: defaultTwoLevels,
			podSets: []PodSetTestCase{{
				topologyRequest: &kueue.PodSetTopologyRequest{
					Required: ptr.To(tasBlockLabel),
				},
				requests: resources.Requests{
					corev1.ResourceCPU: 1000,
				},
				count: 1,
				wantAssignment: &kueue.TopologyAssignment{
					Levels: []string{
						tasBlockLabel,
						tasRackLabel,
					},
					Domains: []kueue.TopologyDomainAssignment{
						{
							Count: 1,
							Values: []string{
								"b2",
								"r1",
							},
						},
					},
				},
			}},
		},
		"block required; single Pod fits in a block spread across two racks; BestFit": {
			nodes:  defaultNodes,
			levels: defaultTwoLevels,
			podSets: []PodSetTestCase{{
				topologyRequest: &kueue.PodSetTopologyRequest{
					Required: ptr.To(tasBlockLabel),
				},
				requests: resources.Requests{
					corev1.ResourceCPU: 1000,
				},
				count: 4,
				wantAssignment: &kueue.TopologyAssignment{
					Levels: []string{
						tasBlockLabel,
						tasRackLabel,
					},
					Domains: []kueue.TopologyDomainAssignment{
						{
							Count: 3,
							Values: []string{
								"b1",
								"r2",
							},
						},
						{
							Count: 1,
							Values: []string{
								"b1",
								"r1",
							},
						},
					},
				},
			}},
		},
		"block required; Pods fit in a block spread across two racks; BestFit": {
			nodes:  defaultNodes,
			levels: defaultTwoLevels,
			podSets: []PodSetTestCase{{
				topologyRequest: &kueue.PodSetTopologyRequest{
					Required: ptr.To(tasBlockLabel),
				},
				requests: resources.Requests{
					corev1.ResourceCPU: 1000,
				},
				count: 4,
				wantAssignment: &kueue.TopologyAssignment{
					Levels: defaultTwoLevels,
					Domains: []kueue.TopologyDomainAssignment{
						{
							Count: 3,
							Values: []string{
								"b1",
								"r2",
							},
						},
						{
							Count: 1,
							Values: []string{
								"b1",
								"r1",
							},
						},
					},
				},
			}},
		},
		"block required; single Pod which cannot be split; BestFit": {
			nodes:  defaultNodes,
			levels: defaultTwoLevels,
			podSets: []PodSetTestCase{{
				topologyRequest: &kueue.PodSetTopologyRequest{
					Required: ptr.To(tasBlockLabel),
				},
				requests: resources.Requests{
					corev1.ResourceCPU: 4000,
				},
				count:      1,
				wantReason: `topology "default" doesn't allow to fit any of 1 pod(s)`,
			}},
		},
		"block required; too many Pods to fit requested; BestFit": {
			nodes:  defaultNodes,
			levels: defaultTwoLevels,
			podSets: []PodSetTestCase{{
				topologyRequest: &kueue.PodSetTopologyRequest{
					Required: ptr.To(tasBlockLabel),
				},
				requests: resources.Requests{
					corev1.ResourceCPU: 1000,
				},
				count:      5,
				wantReason: `topology "default" allows to fit only 4 out of 5 pod(s)`,
			}},
		},
		"rack required; single Pod requiring memory; BestFit": {
			nodes:  defaultNodes,
			levels: defaultTwoLevels,
			podSets: []PodSetTestCase{{
				topologyRequest: &kueue.PodSetTopologyRequest{
					Required: ptr.To(tasRackLabel),
				},
				requests: resources.Requests{
					corev1.ResourceMemory: 1024,
				},
				count: 4,
				wantAssignment: &kueue.TopologyAssignment{
					Levels: defaultTwoLevels,
					Domains: []kueue.TopologyDomainAssignment{
						{
							Count: 4,
							Values: []string{
								"b1",
								"r1",
							},
						},
					},
				},
			}},
		},
		"rack preferred; but only block can accommodate the workload; BestFit": {
			nodes:  defaultNodes,
			levels: defaultTwoLevels,
			podSets: []PodSetTestCase{{
				topologyRequest: &kueue.PodSetTopologyRequest{
					Preferred: ptr.To(tasRackLabel),
				},
				requests: resources.Requests{
					corev1.ResourceCPU: 1000,
				},
				count: 4,
				wantAssignment: &kueue.TopologyAssignment{
					Levels: defaultTwoLevels,
					Domains: []kueue.TopologyDomainAssignment{
						{
							Count: 3,
							Values: []string{
								"b1",
								"r2",
							},
						},
						{
							Count: 1,
							Values: []string{
								"b1",
								"r1",
							},
						},
					},
				},
			}},
		},
		"rack preferred; but only multiple blocks can accommodate the workload; BestFit": {
			nodes:  defaultNodes,
			levels: defaultTwoLevels,
			podSets: []PodSetTestCase{{
				topologyRequest: &kueue.PodSetTopologyRequest{
					Preferred: ptr.To(tasRackLabel),
				},
				requests: resources.Requests{
					corev1.ResourceCPU: 1000,
				},
				count: 6,
				wantAssignment: &kueue.TopologyAssignment{
					Levels: defaultTwoLevels,
					Domains: []kueue.TopologyDomainAssignment{
						{
							Count: 3,
							Values: []string{
								"b1",
								"r2",
							},
						},
						{
							Count: 2,
							Values: []string{
								"b2",
								"r2",
							},
						},
						{
							Count: 1,
							Values: []string{
								"b1",
								"r1",
							},
						},
					},
				},
			}},
		},
		"block preferred; but only multiple blocks can accommodate the workload; BestFit": {
			nodes:  defaultNodes,
			levels: defaultTwoLevels,
			podSets: []PodSetTestCase{{
				topologyRequest: &kueue.PodSetTopologyRequest{
					Preferred: ptr.To(tasBlockLabel),
				},
				requests: resources.Requests{
					corev1.ResourceCPU: 1000,
				},
				count: 6,
				wantAssignment: &kueue.TopologyAssignment{
					Levels: defaultTwoLevels,
					Domains: []kueue.TopologyDomainAssignment{
						{
							Count: 3,
							Values: []string{
								"b1",
								"r2",
							},
						},
						{
							Count: 2,
							Values: []string{
								"b2",
								"r2",
							},
						},
						{
							Count: 1,
							Values: []string{
								"b1",
								"r1",
							},
						},
					},
				},
			}},
		},
		"block preferred; but the workload cannot be accommodate in entire topology; BestFit": {
			nodes:  defaultNodes,
			levels: defaultTwoLevels,
			podSets: []PodSetTestCase{{
				topologyRequest: &kueue.PodSetTopologyRequest{
					Preferred: ptr.To(tasBlockLabel),
				},
				requests: resources.Requests{
					corev1.ResourceCPU: 1000,
				},
				count:      10,
				wantReason: `topology "default" allows to fit only 7 out of 10 pod(s)`,
			}},
		},
		"only nodes with matching labels are considered; no matching node; BestFit": {
			nodes: []corev1.Node{
				*testingnode.MakeNode("b1-r1-x1").
					Label("zone", "zone-a").
					Label(corev1.LabelHostname, "x1").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
						corev1.ResourcePods:   resource.MustParse("10"),
					}).
					Obj(),
			},
			levels: defaultOneLevel,
			podSets: []PodSetTestCase{{
				topologyRequest: &kueue.PodSetTopologyRequest{
					Required: ptr.To(corev1.LabelHostname),
				},
				requests: resources.Requests{
					corev1.ResourceCPU: 1000,
				},
				count:      1,
				wantReason: "no topology domains at level: kubernetes.io/hostname",
			}},
			nodeLabels: map[string]string{
				"zone": "zone-b",
			},
		},
		"only nodes with matching labels are considered; matching node is found; BestFit": {
			nodes: []corev1.Node{
				*testingnode.MakeNode("b1-r1-x1").
					Label("zone", "zone-a").
					Label(corev1.LabelHostname, "x1").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
						corev1.ResourcePods:   resource.MustParse("10"),
					}).
					Ready().
					Obj(),
			},
			levels: defaultOneLevel,
			podSets: []PodSetTestCase{{
				topologyRequest: &kueue.PodSetTopologyRequest{
					Required: ptr.To(corev1.LabelHostname),
				},
				requests: resources.Requests{
					corev1.ResourceCPU: 1000,
				},
				count: 1,
				wantAssignment: &kueue.TopologyAssignment{
					Levels: defaultOneLevel,
					Domains: []kueue.TopologyDomainAssignment{
						{
							Count: 1,
							Values: []string{
								"x1",
							},
						},
					},
				},
			}},
			nodeLabels: map[string]string{
				"zone": "zone-a",
			},
		},
		"only nodes with matching levels are considered; no host label on node; BestFit": {
			nodes: []corev1.Node{
				*testingnode.MakeNode("b1-r1-x1").
					Label(tasBlockLabel, "b1").
					Label(tasRackLabel, "r1").
					// the node doesn't have the 'kubernetes.io/hostname' required by topology
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
						corev1.ResourcePods:   resource.MustParse("10"),
					}).
					Ready().
					Obj(),
			},
			levels: defaultThreeLevels,
			podSets: []PodSetTestCase{{
				topologyRequest: &kueue.PodSetTopologyRequest{
					Required: ptr.To(tasRackLabel),
				},
				requests: resources.Requests{
					corev1.ResourceCPU: 1000,
				},
				count:      1,
				wantReason: "no topology domains at level: cloud.com/topology-rack",
			}},
		},
		"don't consider unscheduled Pods when computing capacity; BestFit": {
			// the Pod is not scheduled (no NodeName set, so is not blocking capacity)
			nodes: []corev1.Node{
				*testingnode.MakeNode("x1").
					Label(corev1.LabelHostname, "x1").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
						corev1.ResourcePods:   resource.MustParse("10"),
					}).
					Ready().
					Obj(),
			},
			pods: []corev1.Pod{
				*testingpod.MakePod("test-unscheduled", "test-ns").
					Request(corev1.ResourceCPU, "600m").
					Obj(),
			},
			levels: defaultOneLevel,
			podSets: []PodSetTestCase{{
				topologyRequest: &kueue.PodSetTopologyRequest{
					Required: ptr.To(corev1.LabelHostname),
				},
				requests: resources.Requests{
					corev1.ResourceCPU: 600,
				},
				count: 1,
				wantAssignment: &kueue.TopologyAssignment{
					Levels: defaultOneLevel,
					Domains: []kueue.TopologyDomainAssignment{
						{
							Count: 1,
							Values: []string{
								"x1",
							},
						},
					},
				},
			}},
		},
		"don't consider terminal pods when computing the capacity; BestFit": {
			nodes: []corev1.Node{
				*testingnode.MakeNode("x1").
					Label(corev1.LabelHostname, "x1").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
						corev1.ResourcePods:   resource.MustParse("10"),
					}).
					Ready().
					Obj(),
			},
			pods: []corev1.Pod{
				*testingpod.MakePod("test-failed", "test-ns").NodeName("x1").
					Request(corev1.ResourceCPU, "600m").
					StatusPhase(corev1.PodFailed).
					Obj(),
				*testingpod.MakePod("test-succeeded", "test-ns").NodeName("x1").
					Request(corev1.ResourceCPU, "600m").
					StatusPhase(corev1.PodSucceeded).
					Obj(),
			},
			levels: defaultOneLevel,
			podSets: []PodSetTestCase{{
				topologyRequest: &kueue.PodSetTopologyRequest{
					Required: ptr.To(corev1.LabelHostname),
				},
				requests: resources.Requests{
					corev1.ResourceCPU: 600,
				},
				count: 1,
				wantAssignment: &kueue.TopologyAssignment{
					Levels: defaultOneLevel,
					Domains: []kueue.TopologyDomainAssignment{
						{
							Count: 1,
							Values: []string{
								"x1",
							},
						},
					},
				},
			}},
		},
		"include usage from pending scheduled non-TAS pods, blocked assignment; BestFit": {
			// there is not enough free capacity on the only node x1
			nodes: []corev1.Node{
				*testingnode.MakeNode("x1").
					Label(corev1.LabelHostname, "x1").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
						corev1.ResourcePods:   resource.MustParse("10"),
					}).
					Ready().
					Obj(),
			},
			pods: []corev1.Pod{
				*testingpod.MakePod("test-pending", "test-ns").NodeName("x1").
					StatusPhase(corev1.PodPending).
					Request(corev1.ResourceCPU, "600m").
					Obj(),
			},
			levels: defaultOneLevel,
			podSets: []PodSetTestCase{{
				topologyRequest: &kueue.PodSetTopologyRequest{
					Required: ptr.To(corev1.LabelHostname),
				},
				requests: resources.Requests{
					corev1.ResourceCPU: 600,
				},
				count:      1,
				wantReason: `topology "default" doesn't allow to fit any of 1 pod(s)`,
			}},
		},
		"include usage from running non-TAS pods, blocked assignment; BestFit": {
			// there is not enough free capacity on the only node x1
			nodes: []corev1.Node{
				*testingnode.MakeNode("x1").
					Label(corev1.LabelHostname, "x1").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
						corev1.ResourcePods:   resource.MustParse("10"),
					}).
					Ready().
					Obj(),
			},
			pods: []corev1.Pod{
				*testingpod.MakePod("test-running", "test-ns").NodeName("x1").
					StatusPhase(corev1.PodRunning).
					Request(corev1.ResourceCPU, "600m").
					Obj(),
			},
			levels: defaultOneLevel,
			podSets: []PodSetTestCase{{
				topologyRequest: &kueue.PodSetTopologyRequest{
					Required: ptr.To(corev1.LabelHostname),
				},
				requests: resources.Requests{
					corev1.ResourceCPU: 600,
				},
				count:      1,
				wantReason: `topology "default" doesn't allow to fit any of 1 pod(s)`,
			}},
		},
		"include usage from running non-TAS pods, found free capacity on another node; BestFit": {
			// there is not enough free capacity on the node x1 as the
			// assignments lends on the free x2
			nodes: []corev1.Node{
				*testingnode.MakeNode("x1").
					Label(corev1.LabelHostname, "x1").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
						corev1.ResourcePods:   resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("x2").
					Label(corev1.LabelHostname, "x2").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
						corev1.ResourcePods:   resource.MustParse("10"),
					}).
					Ready().
					Obj(),
			},
			pods: []corev1.Pod{
				*testingpod.MakePod("test-pod", "test-ns").NodeName("x1").
					Request(corev1.ResourceCPU, "600m").
					Obj(),
			},
			levels: defaultOneLevel,
			podSets: []PodSetTestCase{{
				topologyRequest: &kueue.PodSetTopologyRequest{
					Required: ptr.To(corev1.LabelHostname),
				},
				requests: resources.Requests{
					corev1.ResourceCPU: 600,
				},
				count: 1,
				wantAssignment: &kueue.TopologyAssignment{
					Levels: defaultOneLevel,
					Domains: []kueue.TopologyDomainAssignment{
						{
							Count: 1,
							Values: []string{
								"x2",
							},
						},
					},
				},
			}},
		},
		"no assignment as node is not ready; BestFit": {
			nodes: []corev1.Node{
				*testingnode.MakeNode("b1-r1-x1").
					Label("zone", "zone-a").
					Label(corev1.LabelHostname, "x1").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
						corev1.ResourcePods:   resource.MustParse("10"),
					}).
					NotReady().
					StatusConditions(corev1.NodeCondition{
						Type:   corev1.NodeNetworkUnavailable,
						Status: corev1.ConditionTrue,
					}).
					Obj(),
			},
			levels: defaultOneLevel,
			podSets: []PodSetTestCase{{
				topologyRequest: &kueue.PodSetTopologyRequest{
					Required: ptr.To(corev1.LabelHostname),
				},
				requests: resources.Requests{
					corev1.ResourceCPU: 1000,
				},
				count:      1,
				wantReason: "no topology domains at level: kubernetes.io/hostname",
			}},
			nodeLabels: map[string]string{
				"zone": "zone-a",
			},
		},
		"no assignment as node is unschedulable; BestFit": {
			nodes: []corev1.Node{
				*testingnode.MakeNode("b1-r1-x1").
					Label("zone", "zone-a").
					Label(corev1.LabelHostname, "x1").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
						corev1.ResourcePods:   resource.MustParse("10"),
					}).
					Ready().
					Unschedulable().
					Obj(),
			},
			levels: defaultOneLevel,
			podSets: []PodSetTestCase{{
				topologyRequest: &kueue.PodSetTopologyRequest{
					Required: ptr.To(corev1.LabelHostname),
				},
				requests: resources.Requests{
					corev1.ResourceCPU: 1000,
				},
				count:      1,
				wantReason: "no topology domains at level: kubernetes.io/hostname",
			}},
			nodeLabels: map[string]string{
				"zone": "zone-a",
			},
		},
		"skip node which has untolerated taint; BestFit": {
			nodes: []corev1.Node{
				*testingnode.MakeNode("x1").
					Label("zone", "zone-a").
					Label(corev1.LabelHostname, "x1").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
						corev1.ResourcePods:   resource.MustParse("10"),
					}).
					Ready().
					Taints(corev1.Taint{
						Key:    "example.com/gpu",
						Value:  "present",
						Effect: corev1.TaintEffectNoSchedule,
					}).
					Obj(),
			},
			nodeLabels: map[string]string{
				"zone": "zone-a",
			},
			levels: defaultOneLevel,
			podSets: []PodSetTestCase{{
				topologyRequest: &kueue.PodSetTopologyRequest{
					Required: ptr.To(corev1.LabelHostname),
				},
				requests: resources.Requests{
					corev1.ResourceCPU: 1000,
				},
				count:      1,
				wantReason: `topology "default" doesn't allow to fit any of 1 pod(s)`,
			}},
		},
		"allow to schedule on node with tolerated taint; BestFit": {
			nodes: []corev1.Node{
				*testingnode.MakeNode("b1-r1-x1").
					Label("zone", "zone-a").
					Label(corev1.LabelHostname, "x1").
					Taints(corev1.Taint{
						Key:    "example.com/gpu",
						Value:  "present",
						Effect: corev1.TaintEffectNoSchedule,
					}).
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
						corev1.ResourcePods:   resource.MustParse("10"),
					}).
					Ready().
					Obj(),
			},
			levels: defaultOneLevel,
			podSets: []PodSetTestCase{{
				topologyRequest: &kueue.PodSetTopologyRequest{
					Required: ptr.To(corev1.LabelHostname),
				},
				requests: resources.Requests{
					corev1.ResourceCPU: 1000,
				},
				count: 1,
				wantAssignment: &kueue.TopologyAssignment{
					Levels: defaultOneLevel,
					Domains: []kueue.TopologyDomainAssignment{
						{
							Count: 1,
							Values: []string{
								"x1",
							},
						},
					},
				},
				tolerations: []corev1.Toleration{
					{
						Key:      "example.com/gpu",
						Value:    "present",
						Operator: corev1.TolerationOpEqual,
					},
				},
			}},
			nodeLabels: map[string]string{
				"zone": "zone-a",
			},
		},
		"no assignment as node does not have enough allocatable pods (.status.allocatable['pods']); BestFit": {
			nodes: []corev1.Node{
				*testingnode.MakeNode("b1-r1-x1").
					Label("zone", "zone-a").
					Label(corev1.LabelHostname, "x1").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("1000m"),
						corev1.ResourcePods: resource.MustParse("1"),
					}).
					Ready().
					Obj(),
			},
			pods: []corev1.Pod{
				*testingpod.MakePod("test-running", "test-ns").
					NodeName("b1-r1-x1").
					StatusPhase(corev1.PodRunning).
					Request(corev1.ResourceCPU, "300m").
					Obj(),
			},
			nodeLabels: map[string]string{
				"zone": "zone-a",
			},
			levels: defaultOneLevel,
			podSets: []PodSetTestCase{{
				topologyRequest: &kueue.PodSetTopologyRequest{
					Required: ptr.To(corev1.LabelHostname),
				},
				requests: resources.Requests{
					corev1.ResourceCPU: 300,
				},
				count:      1,
				wantReason: `topology "default" doesn't allow to fit any of 1 pod(s)`,
			}},
		},
		"skip node which doesn't match node selector, missing label; BestFit": {
			nodes: []corev1.Node{
				*testingnode.MakeNode("x1").
					Label("zone", "zone-a").
					Label(corev1.LabelHostname, "x1").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
						corev1.ResourcePods:   resource.MustParse("10"),
					}).
					Ready().
					Obj(),
			},
			nodeLabels: map[string]string{
				"zone": "zone-a",
			},
			levels: defaultOneLevel,

			podSets: []PodSetTestCase{{
				topologyRequest: &kueue.PodSetTopologyRequest{
					Required: ptr.To(corev1.LabelHostname),
				},
				requests: resources.Requests{
					corev1.ResourceCPU: 300,
				},
				count:      1,
				wantReason: `topology "default" doesn't allow to fit any of 1 pod(s)`,
				nodeSelector: map[string]string{
					"custom-label-1": "custom-value-1",
				},
			}},
		},
		"skip node which doesn't match node selector, label exists, value doesn't match; BestFit": {
			nodes: []corev1.Node{
				*testingnode.MakeNode("x1").
					Label("zone", "zone-a").
					Label(corev1.LabelHostname, "x1").
					Label("custom-label-1", "value-1").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
						corev1.ResourcePods:   resource.MustParse("10"),
					}).
					Ready().
					Obj(),
			},
			levels: defaultOneLevel,
			nodeLabels: map[string]string{
				"zone": "zone-a",
			},
			podSets: []PodSetTestCase{{
				topologyRequest: &kueue.PodSetTopologyRequest{
					Required: ptr.To(corev1.LabelHostname),
				},
				requests: resources.Requests{
					corev1.ResourceCPU: 300,
				},
				count:      1,
				wantReason: `topology "default" doesn't allow to fit any of 1 pod(s)`,
				nodeSelector: map[string]string{
					"custom-label-1": "value-2",
				},
			}},
		},
		"allow to schedule on node which matches node; BestFit": {
			nodes: []corev1.Node{
				*testingnode.MakeNode("b1-r1-x1").
					Label("zone", "zone-a").
					Label(corev1.LabelHostname, "x1").
					Label("custom-label-1", "value-1").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
						corev1.ResourcePods:   resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r1-x2").
					Label("zone", "zone-a").
					Label(corev1.LabelHostname, "x2").
					Label("custom-label-1", "value-2").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
						corev1.ResourcePods:   resource.MustParse("10"),
					}).
					Ready().
					Obj(),
			},
			nodeLabels: map[string]string{
				"zone": "zone-a",
			},

			levels: defaultOneLevel,
			podSets: []PodSetTestCase{{
				topologyRequest: &kueue.PodSetTopologyRequest{
					Required: ptr.To(corev1.LabelHostname),
				},
				requests: resources.Requests{
					corev1.ResourceCPU: 1000,
				},
				count: 1,
				wantAssignment: &kueue.TopologyAssignment{
					Levels: defaultOneLevel,
					Domains: []kueue.TopologyDomainAssignment{
						{
							Count: 1,
							Values: []string{
								"x2",
							},
						},
					},
				},
				nodeSelector: map[string]string{
					"custom-label-1": "value-2",
				},
			}},
		},
		"block required for podset; host required for slices; BestFit": {
			//        b1
			//         |
			//        r1
			//    /    |    \
			// x1:3, x2:3, x3:3
			//
			nodes: []corev1.Node{
				*testingnode.MakeNode("b1-r1-x1").
					Label(tasBlockLabel, "b1").
					Label(tasRackLabel, "r1").
					Label(corev1.LabelHostname, "x1").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("3"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r1-x2").
					Label(tasBlockLabel, "b1").
					Label(tasRackLabel, "r1").
					Label(corev1.LabelHostname, "x2").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("3"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r1-x3").
					Label(tasBlockLabel, "b1").
					Label(tasRackLabel, "r1").
					Label(corev1.LabelHostname, "x3").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("3"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
			},
			levels: defaultThreeLevels,
			podSets: []PodSetTestCase{{
				topologyRequest: &kueue.PodSetTopologyRequest{
					Required:                    ptr.To(tasBlockLabel),
					PodSetSliceRequiredTopology: ptr.To(corev1.LabelHostname),
					PodSetSliceSize:             ptr.To(int32(2)),
				},
				requests: resources.Requests{
					corev1.ResourceCPU: 1000,
				},
				count: 6,
				wantAssignment: &kueue.TopologyAssignment{
					Levels: defaultOneLevel,
					Domains: []kueue.TopologyDomainAssignment{
						{
							Count: 2,
							Values: []string{
								"x3",
							},
						},
						{
							Count: 2,
							Values: []string{
								"x2",
							},
						},
						{
							Count: 2,
							Values: []string{
								"x1",
							},
						},
					},
				},
			}},
		},
		"block required for podset; host required for slices; prioritize more free slice capacity first and then tight fit; BestFit": {
			//           b1
			//            |
			//           r1
			//    /    /    \    \
			// x1:6  x2:5   x3:4  x4:2
			//
			nodes: []corev1.Node{
				*testingnode.MakeNode("b1-r1-x1").
					Label(tasBlockLabel, "b1").
					Label(tasRackLabel, "r1").
					Label(corev1.LabelHostname, "x1").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("6"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r1-x2").
					Label(tasBlockLabel, "b1").
					Label(tasRackLabel, "r1").
					Label(corev1.LabelHostname, "x2").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("5"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r1-x3").
					Label(tasBlockLabel, "b1").
					Label(tasRackLabel, "r1").
					Label(corev1.LabelHostname, "x3").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("4"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r1-x4").
					Label(tasBlockLabel, "b1").
					Label(tasRackLabel, "r1").
					Label(corev1.LabelHostname, "x4").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("2"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
			},
			levels: defaultThreeLevels,
			podSets: []PodSetTestCase{{
				topologyRequest: &kueue.PodSetTopologyRequest{
					Required:                    ptr.To(tasBlockLabel),
					PodSetSliceRequiredTopology: ptr.To(corev1.LabelHostname),
					PodSetSliceSize:             ptr.To(int32(2)),
				},
				requests: resources.Requests{
					corev1.ResourceCPU: 1000,
				},
				count: 12,
				wantAssignment: &kueue.TopologyAssignment{
					Levels: defaultOneLevel,
					Domains: []kueue.TopologyDomainAssignment{
						{
							Count: 6,
							Values: []string{
								"x1",
							},
						},
						{
							Count: 4,
							Values: []string{
								"x3",
							},
						},
						{
							Count: 2,
							Values: []string{
								"x4",
							},
						},
					},
				},
			}},
		},
		"block required for podset; host required for slices; select domains with tight fit; BestFit": {
			//        b1
			//         |
			//        r1
			//    /    |    \
			// x1:3, x2:2, x3:2
			//
			nodes: []corev1.Node{
				*testingnode.MakeNode("b1-r1-x1").
					Label(tasBlockLabel, "b1").
					Label(tasRackLabel, "r1").
					Label(corev1.LabelHostname, "x1").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("3"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r1-x2").
					Label(tasBlockLabel, "b1").
					Label(tasRackLabel, "r1").
					Label(corev1.LabelHostname, "x2").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("2"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r1-x3").
					Label(tasBlockLabel, "b1").
					Label(tasRackLabel, "r1").
					Label(corev1.LabelHostname, "x3").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("2"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
			},
			levels: defaultThreeLevels,
			podSets: []PodSetTestCase{{
				topologyRequest: &kueue.PodSetTopologyRequest{
					Required:                    ptr.To(tasBlockLabel),
					PodSetSliceRequiredTopology: ptr.To(corev1.LabelHostname),
					PodSetSliceSize:             ptr.To(int32(2)),
				},
				requests: resources.Requests{
					corev1.ResourceCPU: 1000,
				},
				count: 4,
				wantAssignment: &kueue.TopologyAssignment{
					Levels: defaultOneLevel,
					Domains: []kueue.TopologyDomainAssignment{
						{
							Count: 2,
							Values: []string{
								"x2",
							},
						},
						{
							Count: 2,
							Values: []string{
								"x3",
							},
						},
					},
				},
			}},
		},
		"block required for podset; rack required for slices; BestFit": {

			//        b1            b2
			//    /       \          |
			//   r1        r2       r1
			//  / \     /  / \     /  \
			// x1 x2  x3 x4  x5   x6   x7
			nodes: []corev1.Node{
				*testingnode.MakeNode("b1-r1-x1").
					Label(tasBlockLabel, "b1").
					Label(tasRackLabel, "r1").
					Label(corev1.LabelHostname, "x1").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("1"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r1-x2").
					Label(tasBlockLabel, "b1").
					Label(tasRackLabel, "r1").
					Label(corev1.LabelHostname, "x2").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("1"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r2-x3").
					Label(tasBlockLabel, "b1").
					Label(tasRackLabel, "r2").
					Label(corev1.LabelHostname, "x3").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("1"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r2-x4").
					Label(tasBlockLabel, "b1").
					Label(tasRackLabel, "r2").
					Label(corev1.LabelHostname, "x4").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("1"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r2-x5").
					Label(tasBlockLabel, "b1").
					Label(tasRackLabel, "r2").
					Label(corev1.LabelHostname, "x5").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("1"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b2-r1-x6").
					Label(tasBlockLabel, "b2").
					Label(tasRackLabel, "r1").
					Label(corev1.LabelHostname, "x6").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("1"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b2-r1-x7").
					Label(tasBlockLabel, "b2").
					Label(tasRackLabel, "r1").
					Label(corev1.LabelHostname, "x7").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("1"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
			},
			levels: defaultThreeLevels,
			podSets: []PodSetTestCase{{
				topologyRequest: &kueue.PodSetTopologyRequest{
					Required:                    ptr.To(tasBlockLabel),
					PodSetSliceRequiredTopology: ptr.To(tasRackLabel),
					PodSetSliceSize:             ptr.To(int32(2)),
				},
				requests: resources.Requests{
					corev1.ResourceCPU: 1000,
				},
				count: 4,
				wantAssignment: &kueue.TopologyAssignment{
					Levels: defaultOneLevel,
					Domains: []kueue.TopologyDomainAssignment{
						{
							Count: 1,
							Values: []string{
								"x1",
							},
						},
						{
							Count: 1,
							Values: []string{
								"x2",
							},
						},
						{
							Count: 1,
							Values: []string{
								"x3",
							},
						},
						{
							Count: 1,
							Values: []string{
								"x4",
							},
						},
					},
				},
			}},
		},
		"block preferred for podset; rack required for slices; BestFit": {
			nodes:  defaultNodes,
			levels: defaultThreeLevels,
			podSets: []PodSetTestCase{{
				topologyRequest: &kueue.PodSetTopologyRequest{
					Preferred:                   ptr.To(tasBlockLabel),
					PodSetSliceRequiredTopology: ptr.To(tasRackLabel),
					PodSetSliceSize:             ptr.To(int32(2)),
				},
				requests: resources.Requests{
					corev1.ResourceCPU: 1000,
				},
				count: 4,
				wantAssignment: &kueue.TopologyAssignment{
					Levels: defaultOneLevel,
					Domains: []kueue.TopologyDomainAssignment{
						{
							Count: 1,
							Values: []string{
								"x2",
							},
						},
						{
							Count: 1,
							Values: []string{
								"x3",
							},
						},
						{
							Count: 2,
							Values: []string{
								"x6",
							},
						},
					},
				},
			}},
		},
		"block required for podset; host required for slices; optimize last domain; BestFit": {
			//        b1
			//         |
			//        r1
			//    /    |    \
			// x1:4, x2:3, x3:2
			//
			nodes: []corev1.Node{
				*testingnode.MakeNode("b1-r1-x1").
					Label(tasBlockLabel, "b1").
					Label(tasRackLabel, "r1").
					Label(corev1.LabelHostname, "x1").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("4"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r1-x2").
					Label(tasBlockLabel, "b1").
					Label(tasRackLabel, "r1").
					Label(corev1.LabelHostname, "x2").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("3"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r1-x3").
					Label(tasBlockLabel, "b1").
					Label(tasRackLabel, "r1").
					Label(corev1.LabelHostname, "x3").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("2"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
			},
			levels: defaultThreeLevels,
			podSets: []PodSetTestCase{{
				topologyRequest: &kueue.PodSetTopologyRequest{
					Required:                    ptr.To(tasBlockLabel),
					PodSetSliceRequiredTopology: ptr.To(corev1.LabelHostname),
					PodSetSliceSize:             ptr.To(int32(2)),
				},
				requests: resources.Requests{
					corev1.ResourceCPU: 1000,
				},
				count: 6,
				wantAssignment: &kueue.TopologyAssignment{
					Levels: defaultOneLevel,
					Domains: []kueue.TopologyDomainAssignment{
						{
							Count: 4,
							Values: []string{
								"x1",
							},
						},
						{
							Count: 2,
							Values: []string{
								"x3",
							},
						},
					},
				},
			}},
		},
		"block required for podset; host required for slices; LeastFreeCapacity": {
			//        b1
			//         |
			//        r1
			//    /    |    \
			// x1:4, x2:3, x3:2
			//
			nodes: []corev1.Node{
				*testingnode.MakeNode("b1-r1-x1").
					Label(tasBlockLabel, "b1").
					Label(tasRackLabel, "r1").
					Label(corev1.LabelHostname, "x1").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("4"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r1-x2").
					Label(tasBlockLabel, "b1").
					Label(tasRackLabel, "r1").
					Label(corev1.LabelHostname, "x2").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("3"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r1-x3").
					Label(tasBlockLabel, "b1").
					Label(tasRackLabel, "r1").
					Label(corev1.LabelHostname, "x3").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("2"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
			},
			levels: defaultThreeLevels,
			podSets: []PodSetTestCase{{
				topologyRequest: &kueue.PodSetTopologyRequest{
					Required:                    ptr.To(tasBlockLabel),
					PodSetSliceRequiredTopology: ptr.To(corev1.LabelHostname),
					PodSetSliceSize:             ptr.To(int32(2)),
				},
				requests: resources.Requests{
					corev1.ResourceCPU: 1000,
				},
				count: 4,
				wantAssignment: &kueue.TopologyAssignment{
					Levels: defaultOneLevel,
					Domains: []kueue.TopologyDomainAssignment{
						{
							Count: 2,
							Values: []string{
								"x2",
							},
						},
						{
							Count: 2,
							Values: []string{
								"x3",
							},
						},
					},
				},
			}},
			enableFeatureGates: []featuregate.Feature{features.TASProfileLeastFreeCapacity},
		},
		"block preferred for podset; host required for slices; LeastFreeCapacity": {
			//nolint:dupword // suppress duplicate r1 word
			//        b1                b2
			//         |                 |
			//        r1                r1
			//    /    |    \        /   |
			// x1:4, x2:3, x3:2   x4:4  x5:2
			//
			nodes: []corev1.Node{
				*testingnode.MakeNode("b1-r1-x1").
					Label(tasBlockLabel, "b1").
					Label(tasRackLabel, "r1").
					Label(corev1.LabelHostname, "x1").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("4"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r1-x2").
					Label(tasBlockLabel, "b1").
					Label(tasRackLabel, "r1").
					Label(corev1.LabelHostname, "x2").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("3"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r1-x3").
					Label(tasBlockLabel, "b1").
					Label(tasRackLabel, "r1").
					Label(corev1.LabelHostname, "x3").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("2"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b2-r1-x4").
					Label(tasBlockLabel, "b2").
					Label(tasRackLabel, "r1").
					Label(corev1.LabelHostname, "x4").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("4"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b2-r1-x5").
					Label(tasBlockLabel, "b2").
					Label(tasRackLabel, "r1").
					Label(corev1.LabelHostname, "x5").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("2"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
			},
			levels: defaultThreeLevels,
			podSets: []PodSetTestCase{{
				topologyRequest: &kueue.PodSetTopologyRequest{
					Preferred:                   ptr.To(tasBlockLabel),
					PodSetSliceRequiredTopology: ptr.To(corev1.LabelHostname),
					PodSetSliceSize:             ptr.To(int32(2)),
				},
				requests: resources.Requests{
					corev1.ResourceCPU: 1000,
				},
				count: 8,
				wantAssignment: &kueue.TopologyAssignment{
					Levels: defaultOneLevel,
					Domains: []kueue.TopologyDomainAssignment{
						{
							Count: 2,
							Values: []string{
								"x2",
							},
						},
						{
							Count: 2,
							Values: []string{
								"x3",
							},
						},
						{
							Count: 4,
							Values: []string{
								"x1",
							},
						},
					},
				},
			}},
			enableFeatureGates: []featuregate.Feature{features.TASProfileLeastFreeCapacity},
		},
		"block preferred for podset; host required for slices; 2 blocks with unbalanced subdomains; BestFit": {
			//nolint:dupword // suppress duplicate r1 word
			//        b1                b2
			//         |                 |
			//        r1                r1
			//    /    |    \            |
			// x1:3, x2:3, x3:3   		x4:6
			//
			nodes: []corev1.Node{
				*testingnode.MakeNode("b1-r1-x1").
					Label(tasBlockLabel, "b1").
					Label(tasRackLabel, "r1").
					Label(corev1.LabelHostname, "x1").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("3"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r1-x2").
					Label(tasBlockLabel, "b1").
					Label(tasRackLabel, "r1").
					Label(corev1.LabelHostname, "x2").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("3"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r1-x3").
					Label(tasBlockLabel, "b1").
					Label(tasRackLabel, "r1").
					Label(corev1.LabelHostname, "x3").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("3"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b2-r1-x4").
					Label(tasBlockLabel, "b2").
					Label(tasRackLabel, "r1").
					Label(corev1.LabelHostname, "x4").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("6"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
			},
			levels: defaultThreeLevels,
			podSets: []PodSetTestCase{{
				topologyRequest: &kueue.PodSetTopologyRequest{
					Preferred:                   ptr.To(tasBlockLabel),
					PodSetSliceRequiredTopology: ptr.To(corev1.LabelHostname),
					PodSetSliceSize:             ptr.To(int32(3)),
				},
				requests: resources.Requests{
					corev1.ResourceCPU: 1000,
				},
				count: 12,
				wantAssignment: &kueue.TopologyAssignment{
					Levels: defaultOneLevel,
					Domains: []kueue.TopologyDomainAssignment{
						{
							Count: 3,
							Values: []string{
								"x1",
							},
						},
						{
							Count: 3,
							Values: []string{
								"x2",
							},
						},
						{
							Count: 6,
							Values: []string{
								"x4",
							},
						},
					},
				},
			}},
		},
		"block required for podset; rack required for slices; podset fits in a block, but slices do not fit in racks": {

			//         b1
			//     /    |    \
			//   r1    r2    r3
			//   |      |     |
			//   x1:2  x2:2  x3:2
			nodes: []corev1.Node{
				*testingnode.MakeNode("b1-r1-x1").
					Label(tasBlockLabel, "b1").
					Label(tasRackLabel, "r1").
					Label(corev1.LabelHostname, "x1").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("2"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r2-x2").
					Label(tasBlockLabel, "b1").
					Label(tasRackLabel, "r2").
					Label(corev1.LabelHostname, "x2").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("2"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r3-x3").
					Label(tasBlockLabel, "b1").
					Label(tasRackLabel, "r3").
					Label(corev1.LabelHostname, "x3").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("2"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
			},
			levels: defaultThreeLevels,
			podSets: []PodSetTestCase{{
				topologyRequest: &kueue.PodSetTopologyRequest{
					Required:                    ptr.To(tasBlockLabel),
					PodSetSliceRequiredTopology: ptr.To(tasRackLabel),
					PodSetSliceSize:             ptr.To(int32(3)),
				},
				requests: resources.Requests{
					corev1.ResourceCPU: 1000,
				},
				count:      6,
				wantReason: `topology "default" doesn't allow to fit any of 2 slice(s)`,
			}},
		},
		"block required for podset; rack required for slices; only 1 out of 2 slices fit the topology": {

			//           b1:6
			//     /    /    \    \
			//   r1    r2    r3    r4
			//   |      |     |     |
			//   x1:3  x2:1  x3:1  x4:1
			nodes: []corev1.Node{
				*testingnode.MakeNode("b1-r1-x1").
					Label(tasBlockLabel, "b1").
					Label(tasRackLabel, "r1").
					Label(corev1.LabelHostname, "x1").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("3"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r2-x2").
					Label(tasBlockLabel, "b1").
					Label(tasRackLabel, "r2").
					Label(corev1.LabelHostname, "x2").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("1"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r3-x3").
					Label(tasBlockLabel, "b1").
					Label(tasRackLabel, "r3").
					Label(corev1.LabelHostname, "x3").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("1"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r4-x4").
					Label(tasBlockLabel, "b1").
					Label(tasRackLabel, "r4").
					Label(corev1.LabelHostname, "x4").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("1"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
			},
			levels: defaultThreeLevels,
			podSets: []PodSetTestCase{{
				topologyRequest: &kueue.PodSetTopologyRequest{
					Required:                    ptr.To(tasBlockLabel),
					PodSetSliceRequiredTopology: ptr.To(tasRackLabel),
					PodSetSliceSize:             ptr.To(int32(3)),
				},
				requests: resources.Requests{
					corev1.ResourceCPU: 1000,
				},
				count:      6,
				wantReason: `topology "default" allows to fit only 1 out of 2 slice(s)`,
			}},
		},
		"block required for podset; rack required for slices; podset fits in both blocks, but slices fit in only one block": {

			//       b1:6          b2:6
			//    /    |    \      |    \
			//   r1    r2    r3    r4    r5
			//    |    |     |     |     |
			//   x1:2  x2:2  x3:2  x4:3  x5:3
			nodes: []corev1.Node{
				*testingnode.MakeNode("b1-r1-x1").
					Label(tasBlockLabel, "b1").
					Label(tasRackLabel, "r1").
					Label(corev1.LabelHostname, "x1").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("2"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r2-x2").
					Label(tasBlockLabel, "b1").
					Label(tasRackLabel, "r2").
					Label(corev1.LabelHostname, "x2").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("2"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r3-x3").
					Label(tasBlockLabel, "b1").
					Label(tasRackLabel, "r3").
					Label(corev1.LabelHostname, "x3").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("2"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b2-r4-x4").
					Label(tasBlockLabel, "b2").
					Label(tasRackLabel, "r4").
					Label(corev1.LabelHostname, "x4").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("3"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b2-r5-x5").
					Label(tasBlockLabel, "b2").
					Label(tasRackLabel, "r5").
					Label(corev1.LabelHostname, "x5").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("3"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
			},
			levels: defaultThreeLevels,
			podSets: []PodSetTestCase{{
				topologyRequest: &kueue.PodSetTopologyRequest{
					Required:                    ptr.To(tasBlockLabel),
					PodSetSliceRequiredTopology: ptr.To(tasRackLabel),
					PodSetSliceSize:             ptr.To(int32(3)),
				},
				requests: resources.Requests{
					corev1.ResourceCPU: 1000,
				},
				count: 6,
				wantAssignment: &kueue.TopologyAssignment{
					Levels: defaultOneLevel,
					Domains: []kueue.TopologyDomainAssignment{
						{
							Count: 3,
							Values: []string{
								"x4",
							},
						},
						{
							Count: 3,
							Values: []string{
								"x5",
							},
						},
					},
				},
			}},
		},
		"slice required topology level cannot be above the main required topology level": {
			nodes:  defaultNodes,
			levels: defaultThreeLevels,
			podSets: []PodSetTestCase{{
				topologyRequest: &kueue.PodSetTopologyRequest{
					Required:                    ptr.To(corev1.LabelHostname),
					PodSetSliceRequiredTopology: ptr.To(tasBlockLabel),
					PodSetSliceSize:             ptr.To(int32(1)),
				},
				requests: resources.Requests{
					corev1.ResourceCPU: 1000,
				},
				count:      1,
				wantReason: "podset slice topology cloud.com/topology-block is above the podset topology kubernetes.io/hostname",
			}},
		},
		"slice size is required when slice topology is requested": {
			nodes:  defaultNodes,
			levels: defaultThreeLevels,
			podSets: []PodSetTestCase{{
				topologyRequest: &kueue.PodSetTopologyRequest{
					Required:                    ptr.To(tasBlockLabel),
					PodSetSliceRequiredTopology: ptr.To(corev1.LabelHostname),
				},
				requests: resources.Requests{
					corev1.ResourceCPU: 1000,
				},
				count:      1,
				wantReason: "slice topology requested, but slice size not provided",
			}},
		},
		"cannot request not existing slice topology": {
			nodes:  defaultNodes,
			levels: defaultThreeLevels,
			podSets: []PodSetTestCase{{
				topologyRequest: &kueue.PodSetTopologyRequest{
					Required:                    ptr.To(string(tasBlockLabel)),
					PodSetSliceRequiredTopology: ptr.To("not-existing-topology-level"),
					PodSetSliceSize:             ptr.To(int32(1)),
				},
				requests: resources.Requests{
					corev1.ResourceCPU: 1000,
				},
				count:      1,
				wantReason: "no requested topology level for slices: not-existing-topology-level",
			}},
		},
		"no topology for podset; host required for slices; BestFit": {
			//        b1
			//         |
			//        r1
			//    /    |    \
			// x1:3, x2:3, x3:3
			//
			nodes: []corev1.Node{
				*testingnode.MakeNode("b1-r1-x1").
					Label(tasBlockLabel, "b1").
					Label(tasRackLabel, "r1").
					Label(corev1.LabelHostname, "x1").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("3"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r1-x2").
					Label(tasBlockLabel, "b1").
					Label(tasRackLabel, "r1").
					Label(corev1.LabelHostname, "x2").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("3"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r1-x3").
					Label(tasBlockLabel, "b1").
					Label(tasRackLabel, "r1").
					Label(corev1.LabelHostname, "x3").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("3"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
			},
			levels: defaultThreeLevels,
			podSets: []PodSetTestCase{{
				topologyRequest: &kueue.PodSetTopologyRequest{
					PodSetSliceRequiredTopology: ptr.To(corev1.LabelHostname),
					PodSetSliceSize:             ptr.To(int32(2)),
				},
				requests: resources.Requests{
					corev1.ResourceCPU: 1000,
				},
				count: 6,
				wantAssignment: &kueue.TopologyAssignment{
					Levels: defaultOneLevel,
					Domains: []kueue.TopologyDomainAssignment{
						{
							Count: 2,
							Values: []string{
								"x3",
							},
						},
						{
							Count: 2,
							Values: []string{
								"x2",
							},
						},
						{
							Count: 2,
							Values: []string{
								"x1",
							},
						},
					},
				},
			}},
		},
		"no topology for podset; host required for slices; multiple blocks; BestFit": {
			nodes:  scatteredNodes,
			levels: defaultThreeLevels,
			podSets: []PodSetTestCase{{
				topologyRequest: &kueue.PodSetTopologyRequest{
					PodSetSliceRequiredTopology: ptr.To(corev1.LabelHostname),
					PodSetSliceSize:             ptr.To(int32(2)),
				},
				requests: resources.Requests{
					corev1.ResourceCPU: 1000,
				},
				count: 6,
				wantAssignment: &kueue.TopologyAssignment{
					Levels: defaultOneLevel,
					Domains: []kueue.TopologyDomainAssignment{
						{
							Count: 4,
							Values: []string{
								"x1",
							},
						},
						{
							Count: 2,
							Values: []string{
								"x4",
							},
						},
					},
				},
			}},
		},
		"no topology for podset; rack required for slices; multiple blocks; BestFit": {
			nodes:  defaultNodes,
			levels: defaultThreeLevels,
			podSets: []PodSetTestCase{{
				topologyRequest: &kueue.PodSetTopologyRequest{
					PodSetSliceRequiredTopology: ptr.To(tasRackLabel),
					PodSetSliceSize:             ptr.To(int32(2)),
				},
				requests: resources.Requests{
					corev1.ResourceCPU: 1000,
				},
				count: 4,
				wantAssignment: &kueue.TopologyAssignment{
					Levels: defaultOneLevel,
					Domains: []kueue.TopologyDomainAssignment{
						{
							Count: 1,
							Values: []string{
								"x2",
							},
						},
						{
							Count: 1,
							Values: []string{
								"x3",
							},
						},
						{
							Count: 2,
							Values: []string{
								"x6",
							},
						},
					},
				},
			}},
		},
		"find topology assignment for two podsets with overlapping domain": {
			nodes: []corev1.Node{
				*testingnode.MakeNode("b1").
					Label(tasBlockLabel, "b1").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("2"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b2").
					Label(tasBlockLabel, "b2").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("2"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b3").
					Label(tasBlockLabel, "b3").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("2"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
			},
			levels: []string{tasBlockLabel},
			podSets: []PodSetTestCase{
				{
					podSetName: "podset1",
					topologyRequest: &kueue.PodSetTopologyRequest{
						Preferred: ptr.To(tasBlockLabel),
					},
					requests: resources.Requests{
						corev1.ResourceCPU: 1000,
					},
					count: 3,
					wantAssignment: &kueue.TopologyAssignment{
						Levels: []string{tasBlockLabel},
						Domains: []kueue.TopologyDomainAssignment{
							{
								Count: 2,
								Values: []string{
									"b1",
								},
							},
							{
								Count: 1,
								Values: []string{
									"b2",
								},
							},
						},
					},
				},
				{
					podSetName: "podset2",
					topologyRequest: &kueue.PodSetTopologyRequest{
						Preferred: ptr.To(tasBlockLabel),
					},
					requests: resources.Requests{
						corev1.ResourceCPU: 1000,
					},
					count: 3,
					wantAssignment: &kueue.TopologyAssignment{
						Levels: []string{tasBlockLabel},
						Domains: []kueue.TopologyDomainAssignment{
							{
								Count: 1,
								Values: []string{
									"b2",
								},
							},
							{
								Count: 2,
								Values: []string{
									"b3",
								},
							},
						},
					},
				},
			},
		},
		"find topology assignment for two podsets with the same group": {
			nodes: []corev1.Node{
				*testingnode.MakeNode("b1").
					Label(tasBlockLabel, "b1").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("2"),
						"example.com/gpu":     resource.MustParse("2"),
						corev1.ResourceMemory: resource.MustParse("2Gi"),
						corev1.ResourcePods:   resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b2").
					Label(tasBlockLabel, "b2").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("5"),
						"example.com/gpu":   resource.MustParse("4"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b3").
					Label(tasBlockLabel, "b3").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("2"),
						"example.com/gpu":   resource.MustParse("2"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
			},
			levels: []string{tasBlockLabel},
			podSets: []PodSetTestCase{
				{
					podSetName: "leader",
					topologyRequest: &kueue.PodSetTopologyRequest{
						Required: ptr.To(tasBlockLabel),
					},
					requests: resources.Requests{
						corev1.ResourceCPU: 1000,
					},
					podSetGroupName: ptr.To("sameGroup"),
					count:           1,
					wantAssignment: &kueue.TopologyAssignment{
						Levels: []string{tasBlockLabel},
						Domains: []kueue.TopologyDomainAssignment{
							{
								Count: 1,
								Values: []string{
									"b2",
								},
							},
						},
					},
				},
				{
					podSetName: "workers",
					topologyRequest: &kueue.PodSetTopologyRequest{
						Required: ptr.To(tasBlockLabel),
					},
					requests: resources.Requests{
						corev1.ResourceCPU: 1000,
						"example.com/gpu":  1,
					},
					podSetGroupName: ptr.To("sameGroup"),
					count:           4,
					wantAssignment: &kueue.TopologyAssignment{
						Levels: []string{tasBlockLabel},
						Domains: []kueue.TopologyDomainAssignment{
							{
								Count: 4,
								Values: []string{
									"b2",
								},
							},
						},
					},
				},
			},
		},
		"find topology assignment for two podsets with the same group with domains that can tightly fit leader and workers": {
			nodes: []corev1.Node{
				*testingnode.MakeNode("b1").
					Label(tasBlockLabel, "b1").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("2"),
						"example.com/gpu":   resource.MustParse("2"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b2").
					Label(tasBlockLabel, "b2").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("8"),
						"example.com/gpu":   resource.MustParse("8"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b3").
					Label(tasBlockLabel, "b3").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("2"),
						"example.com/gpu":   resource.MustParse("2"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
			},
			levels: []string{tasBlockLabel},
			podSets: []PodSetTestCase{
				{
					podSetName: "leader",
					topologyRequest: &kueue.PodSetTopologyRequest{
						Required: ptr.To(tasBlockLabel),
					},
					requests: resources.Requests{
						corev1.ResourceCPU: 1000,
					},
					count: 1,
					wantAssignment: &kueue.TopologyAssignment{
						Levels: []string{tasBlockLabel},
						Domains: []kueue.TopologyDomainAssignment{
							{
								Count:  1,
								Values: []string{"b1"},
							},
						},
					},
				},
				{
					podSetName: "workers",
					topologyRequest: &kueue.PodSetTopologyRequest{
						Required: ptr.To(tasBlockLabel),
					},
					requests: resources.Requests{
						corev1.ResourceCPU: 1000,
						"example.com/gpu":  2,
					},
					count: 4,
					wantAssignment: &kueue.TopologyAssignment{
						Levels: []string{tasBlockLabel},
						Domains: []kueue.TopologyDomainAssignment{
							{
								Count:  4,
								Values: []string{"b2"},
							},
						},
					},
				},
			},
		},
		"find topology assignment for two podsets with the same group - no fit": {
			nodes: []corev1.Node{
				*testingnode.MakeNode("b1").
					Label(tasBlockLabel, "b1").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("1"),
						"example.com/gpu":   resource.MustParse("0"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b2").
					Label(tasBlockLabel, "b2").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("4"),
						"example.com/gpu":   resource.MustParse("4"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
			},
			levels: []string{tasBlockLabel},
			podSets: []PodSetTestCase{
				{
					podSetName: "leader",
					topologyRequest: &kueue.PodSetTopologyRequest{
						Required: ptr.To(tasBlockLabel),
					},
					requests: resources.Requests{
						corev1.ResourceCPU: 1000,
					},
					podSetGroupName: ptr.To("sameGroup"),
					count:           1,
					wantAssignment:  nil,
					wantReason:      `topology "default" allows to fit only 4 out of 4 pod(s)`,
				},
				{
					podSetName: "workers",
					topologyRequest: &kueue.PodSetTopologyRequest{
						Required: ptr.To(tasBlockLabel),
					},
					requests: resources.Requests{
						corev1.ResourceCPU: 1000,
						"example.com/gpu":  1,
					},
					podSetGroupName: ptr.To("sameGroup"),
					count:           4,
					wantAssignment:  nil,
					wantReason:      `topology "default" allows to fit only 4 out of 4 pod(s)`,
				},
			},
		},
		"find topology assignment for two podsets with the same group - optimizes domain for both leader and workers": {
			nodes: []corev1.Node{
				*testingnode.MakeNode("b1").
					Label(tasBlockLabel, "b1").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("11"),
						"example.com/gpu":   resource.MustParse("8"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b2").
					Label(tasBlockLabel, "b2").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("4"),
						"example.com/gpu":   resource.MustParse("4"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
			},
			levels: []string{tasBlockLabel},
			podSets: []PodSetTestCase{
				{
					podSetName: "leader",
					topologyRequest: &kueue.PodSetTopologyRequest{
						Required: ptr.To(tasBlockLabel),
					},
					requests: resources.Requests{
						corev1.ResourceCPU: 1000,
					},
					podSetGroupName: ptr.To("sameGroup"),
					count:           1,
					wantAssignment: &kueue.TopologyAssignment{
						Levels: []string{tasBlockLabel},
						Domains: []kueue.TopologyDomainAssignment{
							{
								Count: 1,
								Values: []string{
									"b1",
								},
							},
						},
					},
				},
				{
					podSetName: "workers",
					topologyRequest: &kueue.PodSetTopologyRequest{
						Required: ptr.To(tasBlockLabel),
					},
					requests: resources.Requests{
						corev1.ResourceCPU: 1000,
						"example.com/gpu":  1,
					},
					podSetGroupName: ptr.To("sameGroup"),
					count:           4,
					wantAssignment: &kueue.TopologyAssignment{
						Levels: []string{tasBlockLabel},
						Domains: []kueue.TopologyDomainAssignment{
							{
								Count: 4,
								Values: []string{
									"b1",
								},
							},
						},
					},
				},
			},
		},
		"find topology assignment for two podsets with the same group - leader does not fit anywhere": {
			nodes: []corev1.Node{
				*testingnode.MakeNode("b1").
					Label(tasBlockLabel, "b1").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("4"),
						"example.com/gpu":   resource.MustParse("4"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b2").
					Label(tasBlockLabel, "b2").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("4"),
						"example.com/gpu":   resource.MustParse("4"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
			},
			levels: []string{tasBlockLabel},
			podSets: []PodSetTestCase{
				{
					podSetName: "leader",
					topologyRequest: &kueue.PodSetTopologyRequest{
						Required: ptr.To(tasBlockLabel),
					},
					requests: resources.Requests{
						corev1.ResourceCPU: 10000,
					},
					podSetGroupName: ptr.To("sameGroup"),
					count:           1,
					wantReason:      `topology "default" allows to fit only 4 out of 4 pod(s)`,
				},
				{
					podSetName: "workers",
					topologyRequest: &kueue.PodSetTopologyRequest{
						Required: ptr.To(tasBlockLabel),
					},
					requests: resources.Requests{
						corev1.ResourceCPU: 1000,
						"example.com/gpu":  1,
					},
					podSetGroupName: ptr.To("sameGroup"),
					count:           4,
					wantReason:      `topology "default" allows to fit only 4 out of 4 pod(s)`,
				},
			},
		},
		"find topology assignment for two podsets with the same group - multiple hosts": {
			//       b1              b2
			//        |               |
			//       r1              r2
			//    /   |   \       /   |   \
			//  x1   x2   x3    x4   x5   x6
			//
			nodes: []corev1.Node{
				*testingnode.MakeNode("b1-r1-x1").
					Label(tasBlockLabel, "b1").
					Label(tasRackLabel, "r1").
					Label(corev1.LabelHostname, "x1").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("2"),
						corev1.ResourcePods: resource.MustParse("10"),
						"example.com/gpu":   resource.MustParse("1"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r1-x2").
					Label(tasBlockLabel, "b1").
					Label(tasRackLabel, "r1").
					Label(corev1.LabelHostname, "x2").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("2"),
						corev1.ResourcePods: resource.MustParse("10"),
						"example.com/gpu":   resource.MustParse("1"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r1-x3").
					Label(tasBlockLabel, "b1").
					Label(tasRackLabel, "r1").
					Label(corev1.LabelHostname, "x3").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("2"),
						corev1.ResourcePods: resource.MustParse("10"),
						"example.com/gpu":   resource.MustParse("1"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b2-r4-x4").
					Label(tasBlockLabel, "b2").
					Label(tasRackLabel, "r4").
					Label(corev1.LabelHostname, "x4").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("1"),
						corev1.ResourcePods: resource.MustParse("10"),
						"example.com/gpu":   resource.MustParse("1"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b2-r5-x5").
					Label(tasBlockLabel, "b2").
					Label(tasRackLabel, "r5").
					Label(corev1.LabelHostname, "x5").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("2"),
						corev1.ResourcePods: resource.MustParse("10"),
						"example.com/gpu":   resource.MustParse("1"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b2-r6-x6").
					Label(tasBlockLabel, "b2").
					Label(tasRackLabel, "r6").
					Label(corev1.LabelHostname, "x6").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("2"),
						corev1.ResourcePods: resource.MustParse("10"),
						"example.com/gpu":   resource.MustParse("1"),
					}).
					Ready().
					Obj(),
			},
			levels: []string{tasBlockLabel, tasRackLabel, corev1.LabelHostname},
			podSets: []PodSetTestCase{
				{
					podSetName: "leader",
					topologyRequest: &kueue.PodSetTopologyRequest{
						Required: ptr.To(tasBlockLabel),
					},
					requests: resources.Requests{
						corev1.ResourceCPU: 2000,
					},
					podSetGroupName: ptr.To("sameGroup"),
					count:           1,
					wantAssignment: &kueue.TopologyAssignment{
						Levels: []string{corev1.LabelHostname},
						Domains: []kueue.TopologyDomainAssignment{
							{
								Count: 1,
								Values: []string{
									"x1",
								},
							},
						},
					},
				},
				{
					podSetName: "workers",
					topologyRequest: &kueue.PodSetTopologyRequest{
						Required: ptr.To(tasBlockLabel),
					},
					requests: resources.Requests{
						corev1.ResourceCPU: 1000,
						"example.com/gpu":  1,
					},
					podSetGroupName: ptr.To("sameGroup"),
					count:           2,
					wantAssignment: &kueue.TopologyAssignment{
						Levels: []string{corev1.LabelHostname},
						Domains: []kueue.TopologyDomainAssignment{
							{
								Count: 1,
								Values: []string{
									"x2",
								},
							},
							{
								Count: 1,
								Values: []string{
									"x3",
								},
							},
						},
					},
				},
			},
		},
		"find topology assignment for two podsets with the same group requesting same resources and nodes in the same rack": {
			//         b1                 b2
			//        /   \             /    \
			//      r1     r2         r3     r4
			//    /  |     |  \     /  |     |  \
			//  x1  x2    x3  x4  x5   x6   x7   x8
			//
			nodes: []corev1.Node{
				*testingnode.MakeNode("b1-r1-x1").
					Label(tasBlockLabel, "b1").
					Label(tasRackLabel, "r1").
					Label(corev1.LabelHostname, "x1").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("1"),
						corev1.ResourcePods: resource.MustParse("10"),
						"example.com/gpu":   resource.MustParse("1"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r1-x2").
					Label(tasBlockLabel, "b1").
					Label(tasRackLabel, "r1").
					Label(corev1.LabelHostname, "x2").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("1"),
						corev1.ResourcePods: resource.MustParse("10"),
						"example.com/gpu":   resource.MustParse("1"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r2-x3").
					Label(tasBlockLabel, "b1").
					Label(tasRackLabel, "r2").
					Label(corev1.LabelHostname, "x3").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("1"),
						corev1.ResourcePods: resource.MustParse("10"),
						"example.com/gpu":   resource.MustParse("1"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r2-x4").
					Label(tasBlockLabel, "b1").
					Label(tasRackLabel, "r2").
					Label(corev1.LabelHostname, "x4").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("1"),
						corev1.ResourcePods: resource.MustParse("10"),
						"example.com/gpu":   resource.MustParse("1"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b2-r3-x5").
					Label(tasBlockLabel, "b2").
					Label(tasRackLabel, "r3").
					Label(corev1.LabelHostname, "x5").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("1"),
						corev1.ResourcePods: resource.MustParse("10"),
						"example.com/gpu":   resource.MustParse("1"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b2-r3-x6").
					Label(tasBlockLabel, "b2").
					Label(tasRackLabel, "r3").
					Label(corev1.LabelHostname, "x6").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("1"),
						corev1.ResourcePods: resource.MustParse("10"),
						"example.com/gpu":   resource.MustParse("1"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b2-r4-x7").
					Label(tasBlockLabel, "b2").
					Label(tasRackLabel, "r4").
					Label(corev1.LabelHostname, "x7").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("1"),
						corev1.ResourcePods: resource.MustParse("10"),
						"example.com/gpu":   resource.MustParse("1"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b2-r4-x8").
					Label(tasBlockLabel, "b2").
					Label(tasRackLabel, "r4").
					Label(corev1.LabelHostname, "x8").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("1"),
						corev1.ResourcePods: resource.MustParse("10"),
						"example.com/gpu":   resource.MustParse("1"),
					}).
					Ready().
					Obj(),
			},
			levels: []string{tasBlockLabel, tasRackLabel, corev1.LabelHostname},
			podSets: []PodSetTestCase{
				{
					podSetName: "leader",
					topologyRequest: &kueue.PodSetTopologyRequest{
						Required: ptr.To(tasBlockLabel),
					},
					requests: resources.Requests{
						corev1.ResourceCPU: 1000,
						"example.com/gpu":  1,
					},
					podSetGroupName: ptr.To("sameGroup"),
					count:           1,
					wantAssignment: &kueue.TopologyAssignment{
						Levels: []string{corev1.LabelHostname},
						Domains: []kueue.TopologyDomainAssignment{
							{
								Count: 1,
								Values: []string{
									"x1",
								},
							},
						},
					},
				},
				{
					podSetName: "workers",
					topologyRequest: &kueue.PodSetTopologyRequest{
						Required: ptr.To(tasBlockLabel),
					},
					requests: resources.Requests{
						corev1.ResourceCPU: 1000,
						"example.com/gpu":  1,
					},
					podSetGroupName: ptr.To("sameGroup"),
					count:           2,
					wantAssignment: &kueue.TopologyAssignment{
						Levels: []string{corev1.LabelHostname},
						Domains: []kueue.TopologyDomainAssignment{
							{
								Count: 1,
								Values: []string{
									"x2",
								},
							},
							{
								Count: 1,
								Values: []string{
									"x3",
								},
							},
						},
					},
				},
			},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)
			// TODO: remove after dropping the TAS profiles feature gates
			for _, gate := range tc.enableFeatureGates {
				features.SetFeatureGateDuringTest(t, gate, true)
			}

			initialObjects := make([]client.Object, 0)
			for i := range tc.nodes {
				initialObjects = append(initialObjects, &tc.nodes[i])
			}
			for i := range tc.pods {
				initialObjects = append(initialObjects, &tc.pods[i])
			}
			clientBuilder := utiltesting.NewClientBuilder()
			clientBuilder.WithObjects(initialObjects...)
			_ = tasindexer.SetupIndexes(ctx, utiltesting.AsIndexer(clientBuilder))
			client := clientBuilder.Build()

			tasCache := NewTASCache(client)
			topologyInformation := topologyInformation{
				Levels: tc.levels,
			}
			flavorInformation := flavorInformation{
				TopologyName: "default",
				NodeLabels:   tc.nodeLabels,
			}
			tasFlavorCache := tasCache.NewTASFlavorCache(topologyInformation, flavorInformation)

			snapshot, err := tasFlavorCache.snapshot(ctx)
			if err != nil {
				t.Fatalf("failed to build the snapshot: %v", err)
			}

			flavorTASRequests := make([]TASPodSetRequests, 0, len(tc.podSets))
			wantResult := make(TASAssignmentsResult)
			for _, ps := range tc.podSets {
				tasInput := TASPodSetRequests{
					PodSet: &kueue.PodSet{
						Name:            kueue.PodSetReference(ps.podSetName),
						TopologyRequest: ps.topologyRequest,
						Template: corev1.PodTemplateSpec{
							Spec: corev1.PodSpec{
								Tolerations:  ps.tolerations,
								NodeSelector: ps.nodeSelector,
							},
						},
					},
					SinglePodRequests: ps.requests,
					Count:             ps.count,
				}
				if ps.podSetGroupName != nil {
					tasInput.PodSetGroupName = ps.podSetGroupName
				}
				if ps.topologyRequest == nil {
					tasInput.Implied = true
				}
				flavorTASRequests = append(flavorTASRequests, tasInput)

				wantPodSetResult := tasPodSetAssignmentResult{
					FailureReason: ps.wantReason,
				}
				if ps.wantAssignment != nil {
					sort.Slice(ps.wantAssignment.Domains, func(i, j int) bool {
						return utiltas.DomainID(ps.wantAssignment.Domains[i].Values) < utiltas.DomainID(ps.wantAssignment.Domains[j].Values)
					})
					wantPodSetResult.TopologyAssignment = ps.wantAssignment
				}
				wantResult[kueue.PodSetReference(ps.podSetName)] = wantPodSetResult
			}
			gotResult := snapshot.FindTopologyAssignmentsForFlavor(flavorTASRequests)
			if diff := cmp.Diff(wantResult, gotResult); diff != "" {
				t.Errorf("unexpected topology assignment (-want,+got): %s", diff)
			}
		})
	}
}
