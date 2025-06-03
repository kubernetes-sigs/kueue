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
	"context"
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

func TestFindTopologyAssignment(t *testing.T) {
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
		// TODO: remove after dropping the TASMostFreeCapacity feature gate
		enableFeatureGates []featuregate.Feature
		wantReason         string
		topologyRequest    *kueue.PodSetTopologyRequest
		levels             []string
		nodeLabels         map[string]string
		nodeSelector       map[string]string
		nodes              []corev1.Node
		pods               []corev1.Pod
		requests           resources.Requests
		count              int32
		tolerations        []corev1.Toleration
		wantAssignment     *kueue.TopologyAssignment
	}{
		// TODO: remove suffixes MostFreeCapacity/BestFit after dropping the TASMostFreeCapacity feature gate
		"minimize the number of used racks before optimizing the number of nodes; MostFreeCapacity": {
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
			topologyRequest: &kueue.PodSetTopologyRequest{
				Required: ptr.To(tasBlockLabel),
			},
			levels: defaultThreeLevels,
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
			enableFeatureGates: []featuregate.Feature{features.TASProfileMostFreeCapacity},
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
			topologyRequest: &kueue.PodSetTopologyRequest{
				Required: ptr.To(tasBlockLabel),
			},
			levels: defaultThreeLevels,
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
			topologyRequest: &kueue.PodSetTopologyRequest{
				Required: ptr.To(tasBlockLabel),
			},
			levels: defaultThreeLevels,
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
		},
		"unconstrained; 2 pods fit into hosts scattered across the whole datacenter even they could fit into single rack; LeastFreeCapacity": {
			nodes: scatteredNodes,
			topologyRequest: &kueue.PodSetTopologyRequest{
				Unconstrained: ptr.To(true),
			},
			levels: defaultThreeLevels,
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
			enableFeatureGates: []featuregate.Feature{features.TASProfileLeastFreeCapacity},
		},
		"no annotation; implied default to unconstrained; 6 pods fit into hosts scattered across the whole datacenter even they could fit into single rack; BestFit": {
			nodes:  scatteredNodes,
			levels: defaultThreeLevels,
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
		},
		"unconstrained; 6 pods fit into hosts scattered across the whole datacenter even they could fit into single rack; BestFit": {
			nodes: scatteredNodes,
			topologyRequest: &kueue.PodSetTopologyRequest{
				Unconstrained: ptr.To(true),
			},
			levels: defaultThreeLevels,
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
		},
		"unconstrained; 6 pods fit into hosts scattered across the whole datacenter even they could fit into single rack; MostFreeCapacityFit": {
			nodes: scatteredNodes,
			topologyRequest: &kueue.PodSetTopologyRequest{
				Unconstrained: ptr.To(true),
			},
			levels: defaultThreeLevels,
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
			enableFeatureGates: []featuregate.Feature{features.TASProfileMostFreeCapacity},
		},
		"unconstrained; a single pod fits into each host; BestFit": {
			nodes: defaultNodes,
			topologyRequest: &kueue.PodSetTopologyRequest{
				Unconstrained: ptr.To(true),
			},
			levels: defaultThreeLevels,
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
		},
		"unconstrained; a single pod fits into each host; MostFreeCapacity": {
			nodes: defaultNodes,
			topologyRequest: &kueue.PodSetTopologyRequest{
				Unconstrained: ptr.To(true),
			},
			levels: defaultThreeLevels,
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
							"x6",
						},
					},
				},
			},
			enableFeatureGates: []featuregate.Feature{features.TASProfileMostFreeCapacity},
		},
		"unconstrained; a single pod fits into each host; LeastFreeCapacity; TASProfileLeastFreeCapacity": {
			nodes: defaultNodes,
			topologyRequest: &kueue.PodSetTopologyRequest{
				Unconstrained: ptr.To(true),
			},
			levels: defaultThreeLevels,
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
			enableFeatureGates: []featuregate.Feature{features.TASProfileLeastFreeCapacity},
		},
		"unconstrained; a single pod fits into each host; LeastFreeCapacity; TASProfileMixed": {
			nodes: defaultNodes,
			topologyRequest: &kueue.PodSetTopologyRequest{
				Unconstrained: ptr.To(true),
			},
			levels: defaultThreeLevels,
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
			enableFeatureGates: []featuregate.Feature{features.TASProfileMixed},
		},
		"block required; 4 pods fit into one host each; MostFreeCapacity": {
			nodes: binaryTreesNodes,
			topologyRequest: &kueue.PodSetTopologyRequest{
				Required: ptr.To(tasBlockLabel),
			},
			levels: defaultThreeLevels,
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
			enableFeatureGates: []featuregate.Feature{features.TASProfileMostFreeCapacity},
		},
		"block required; 4 pods fit into one host each; BestFit": {
			nodes: binaryTreesNodes,
			topologyRequest: &kueue.PodSetTopologyRequest{
				Required: ptr.To(tasBlockLabel),
			},
			levels: defaultThreeLevels,
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
		},
		"host required; single Pod fits in the host; MostFreeCapacity": {
			// TODO: remove after dropping the TASMostFreeCapacity feature gate
			nodes: defaultNodes,
			topologyRequest: &kueue.PodSetTopologyRequest{
				Required: ptr.To(corev1.LabelHostname),
			},
			levels: defaultThreeLevels,
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
							"x6",
						},
					},
				},
			},
			enableFeatureGates: []featuregate.Feature{features.TASProfileMostFreeCapacity},
		},
		"host required; single Pod fits in the host; LeastFreeCapacityFit": {
			nodes: defaultNodes,
			topologyRequest: &kueue.PodSetTopologyRequest{
				Required: ptr.To(corev1.LabelHostname),
			},
			levels: defaultThreeLevels,
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
			enableFeatureGates: []featuregate.Feature{features.TASProfileLeastFreeCapacity},
		},
		"host required; single Pod fits in the host; BestFit": {
			nodes: defaultNodes,
			topologyRequest: &kueue.PodSetTopologyRequest{
				Required: ptr.To(corev1.LabelHostname),
			},
			levels: defaultThreeLevels,
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
		},
		"host required; single Pod fits in the host; BestFit; TASProfileMixed": {
			nodes: defaultNodes,
			topologyRequest: &kueue.PodSetTopologyRequest{
				Required: ptr.To(corev1.LabelHostname),
			},
			levels: defaultThreeLevels,
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
			enableFeatureGates: []featuregate.Feature{features.TASProfileMixed},
		},
		"host preferred; single Pod fits in the host; BestFit; TASProfileMixed": {
			nodes: defaultNodes,
			topologyRequest: &kueue.PodSetTopologyRequest{
				Required: ptr.To(corev1.LabelHostname),
			},
			levels: defaultThreeLevels,
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
			enableFeatureGates: []featuregate.Feature{features.TASProfileMixed},
		},
		"rack required; single Pod fits in a rack; MostFreeCapacity": {
			// TODO: remove after dropping the TASMostFreeCapacity feature gate
			nodes: defaultNodes,
			topologyRequest: &kueue.PodSetTopologyRequest{
				Required: ptr.To(tasRackLabel),
			},
			levels: defaultTwoLevels,
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
							"r2",
						},
					},
				},
			},
			enableFeatureGates: []featuregate.Feature{features.TASProfileMostFreeCapacity},
		},
		"rack required; single Pod fits in a rack; LeastFreeCapacityFit": {
			nodes: defaultNodes,
			topologyRequest: &kueue.PodSetTopologyRequest{
				Required: ptr.To(tasRackLabel),
			},
			levels: defaultThreeLevels,
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
			enableFeatureGates: []featuregate.Feature{features.TASProfileLeastFreeCapacity},
		},
		"rack preferred; multiple Pods fits in multiple racks; LeastFreeCapacityFit": {
			nodes: defaultNodes,
			topologyRequest: &kueue.PodSetTopologyRequest{
				Preferred: ptr.To(tasRackLabel),
			},
			levels: defaultTwoLevels,
			requests: resources.Requests{
				corev1.ResourceCPU: 1000,
			},
			count: 2,
			wantAssignment: &kueue.TopologyAssignment{
				Levels: defaultTwoLevels,
				Domains: []kueue.TopologyDomainAssignment{
					{
						Count: 1,
						Values: []string{
							"b2",
							"r1",
						},
					},
					{
						Count: 1,
						Values: []string{
							"b2",
							"r2",
						},
					},
				},
			},
			enableFeatureGates: []featuregate.Feature{features.TASProfileLeastFreeCapacity},
		},
		"rack required; multiple Pods fits in a rack; MostFreeCapacity": {
			nodes: defaultNodes,
			topologyRequest: &kueue.PodSetTopologyRequest{
				Required: ptr.To(tasRackLabel),
			},
			levels: defaultTwoLevels,
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
			enableFeatureGates: []featuregate.Feature{features.TASProfileMostFreeCapacity},
		},
		"rack required; multiple Pods fit in a rack; BestFit": {
			nodes: defaultNodes,
			topologyRequest: &kueue.PodSetTopologyRequest{
				Required: ptr.To(tasRackLabel),
			},
			levels: defaultTwoLevels,
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
			topologyRequest: &kueue.PodSetTopologyRequest{
				Preferred: ptr.To(tasBlockLabel),
			},
			levels: []string{tasBlockLabel},
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
		},
		"rack required; multiple Pods fit in some racks; BestFit": {
			nodes: defaultNodes,
			topologyRequest: &kueue.PodSetTopologyRequest{
				Required: ptr.To(tasRackLabel),
			},
			levels: defaultTwoLevels,
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
		},
		"rack required; too many pods to fit in any rack; MostFreeCapacity": {
			nodes: defaultNodes,
			topologyRequest: &kueue.PodSetTopologyRequest{
				Required: ptr.To(tasRackLabel),
			},
			levels: defaultTwoLevels,
			requests: resources.Requests{
				corev1.ResourceCPU: 1000,
			},
			count:      4,
			wantReason: `topology "default" allows to fit only 3 out of 4 pod(s)`,

			enableFeatureGates: []featuregate.Feature{features.TASProfileMostFreeCapacity},
		},
		"block required; single Pod fits in a block; MostFreeCapacity": {
			// TODO: remove after dropping the TASMostFreeCapacity feature gate
			nodes: defaultNodes,
			topologyRequest: &kueue.PodSetTopologyRequest{
				Required: ptr.To(tasBlockLabel),
			},
			levels: defaultTwoLevels,
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
							"b1",
							"r2",
						},
					},
				},
			},
			enableFeatureGates: []featuregate.Feature{features.TASProfileMostFreeCapacity},
		},
		"block required; single Pod fits in a block; LeastFreeCapacityFit": {
			nodes: defaultNodes,
			topologyRequest: &kueue.PodSetTopologyRequest{
				Required: ptr.To(tasBlockLabel),
			},
			levels: defaultThreeLevels,
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
			enableFeatureGates: []featuregate.Feature{features.TASProfileLeastFreeCapacity},
		},
		"block required; two Pods fits in a block; LeastFreeCapacityFit": {
			nodes: defaultNodes,
			topologyRequest: &kueue.PodSetTopologyRequest{
				Required: ptr.To(tasBlockLabel),
			},
			levels: defaultThreeLevels,
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
			enableFeatureGates: []featuregate.Feature{features.TASProfileLeastFreeCapacity},
		},
		"block required; single Pod fits in a block and a single rack; BestFit": {
			nodes: defaultNodes,
			topologyRequest: &kueue.PodSetTopologyRequest{
				Required: ptr.To(tasBlockLabel),
			},
			levels: defaultTwoLevels,
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
		},
		"block required; single Pod fits in a block spread across two racks; BestFit": {
			nodes: defaultNodes,
			topologyRequest: &kueue.PodSetTopologyRequest{
				Required: ptr.To(tasBlockLabel),
			},
			levels: defaultTwoLevels,
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
		},
		"block required; Pods fit in a block spread across two racks; MostFreeCapacity": {
			nodes: defaultNodes,
			topologyRequest: &kueue.PodSetTopologyRequest{
				Required: ptr.To(tasBlockLabel),
			},
			levels: defaultTwoLevels,
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
			enableFeatureGates: []featuregate.Feature{features.TASProfileMostFreeCapacity},
		},
		"block required; single Pod which cannot be split; MostFreeCapacity": {
			nodes: defaultNodes,
			topologyRequest: &kueue.PodSetTopologyRequest{
				Required: ptr.To(tasBlockLabel),
			},
			levels: defaultTwoLevels,
			requests: resources.Requests{
				corev1.ResourceCPU: 4000,
			},
			count:              1,
			wantReason:         `topology "default" doesn't allow to fit any of 1 pod(s)`,
			enableFeatureGates: []featuregate.Feature{features.TASProfileMostFreeCapacity},
		},
		"block required; too many Pods to fit requested; MostFreeCapacity": {
			nodes: defaultNodes,
			topologyRequest: &kueue.PodSetTopologyRequest{
				Required: ptr.To(tasBlockLabel),
			},
			levels: defaultTwoLevels,
			requests: resources.Requests{
				corev1.ResourceCPU: 1000,
			},
			count:              5,
			wantReason:         `topology "default" allows to fit only 4 out of 5 pod(s)`,
			enableFeatureGates: []featuregate.Feature{features.TASProfileMostFreeCapacity},
		},
		"rack required; single Pod requiring memory; MostFreeCapacity": {
			// TODO: remove after dropping the TASMostFreeCapacity feature gate
			nodes: defaultNodes,
			topologyRequest: &kueue.PodSetTopologyRequest{
				Required: ptr.To(tasRackLabel),
			},
			levels: defaultTwoLevels,
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
							"b2",
							"r2",
						},
					},
				},
			},
			enableFeatureGates: []featuregate.Feature{features.TASProfileMostFreeCapacity},
		},
		"rack preferred; but only block can accommodate the workload; MostFreeCapacity": {
			nodes: defaultNodes,
			topologyRequest: &kueue.PodSetTopologyRequest{
				Preferred: ptr.To(tasRackLabel),
			},
			levels: defaultTwoLevels,
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
			enableFeatureGates: []featuregate.Feature{features.TASProfileMostFreeCapacity},
		},
		"rack preferred; but only multiple blocks can accommodate the workload; MostFreeCapacity": {
			nodes: defaultNodes,
			topologyRequest: &kueue.PodSetTopologyRequest{
				Preferred: ptr.To(tasRackLabel),
			},
			levels: defaultTwoLevels,
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
			enableFeatureGates: []featuregate.Feature{features.TASProfileMostFreeCapacity},
		},
		"block preferred; but only multiple blocks can accommodate the workload; MostFreeCapacity": {
			nodes: defaultNodes,
			topologyRequest: &kueue.PodSetTopologyRequest{
				Preferred: ptr.To(tasBlockLabel),
			},
			levels: defaultTwoLevels,
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
			enableFeatureGates: []featuregate.Feature{features.TASProfileMostFreeCapacity},
		},
		"block preferred; but the workload cannot be accommodate in entire topology; MostFreeCapacity": {
			nodes: defaultNodes,
			topologyRequest: &kueue.PodSetTopologyRequest{
				Preferred: ptr.To(tasBlockLabel),
			},
			levels: defaultTwoLevels,
			requests: resources.Requests{
				corev1.ResourceCPU: 1000,
			},
			count:              10,
			wantReason:         `topology "default" allows to fit only 7 out of 10 pod(s)`,
			enableFeatureGates: []featuregate.Feature{features.TASProfileMostFreeCapacity},
		},
		"only nodes with matching labels are considered; no matching node; MostFreeCapacity": {
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
			topologyRequest: &kueue.PodSetTopologyRequest{
				Required: ptr.To(corev1.LabelHostname),
			},
			nodeLabels: map[string]string{
				"zone": "zone-b",
			},
			levels: defaultOneLevel,
			requests: resources.Requests{
				corev1.ResourceCPU: 1000,
			},
			count:              1,
			wantReason:         "no topology domains at level: kubernetes.io/hostname",
			enableFeatureGates: []featuregate.Feature{features.TASProfileMostFreeCapacity},
		},
		"only nodes with matching labels are considered; matching node is found; MostFreeCapacity": {
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
			topologyRequest: &kueue.PodSetTopologyRequest{
				Required: ptr.To(corev1.LabelHostname),
			},
			nodeLabels: map[string]string{
				"zone": "zone-a",
			},
			levels: defaultOneLevel,
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
			enableFeatureGates: []featuregate.Feature{features.TASProfileMostFreeCapacity},
		},
		"only nodes with matching levels are considered; no host label on node; MostFreeCapacity": {
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
			topologyRequest: &kueue.PodSetTopologyRequest{
				Required: ptr.To(tasRackLabel),
			},
			levels: defaultThreeLevels,
			requests: resources.Requests{
				corev1.ResourceCPU: 1000,
			},
			count:              1,
			wantReason:         "no topology domains at level: cloud.com/topology-rack",
			enableFeatureGates: []featuregate.Feature{features.TASProfileMostFreeCapacity},
		},
		"don't consider unscheduled Pods when computing capacity; MostFreeCapacity": {
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
			topologyRequest: &kueue.PodSetTopologyRequest{
				Required: ptr.To(corev1.LabelHostname),
			},
			levels: defaultOneLevel,
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
			enableFeatureGates: []featuregate.Feature{features.TASProfileMostFreeCapacity},
		},
		"don't consider terminal pods when computing the capacity; MostFreeCapacity": {
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
			topologyRequest: &kueue.PodSetTopologyRequest{
				Required: ptr.To(corev1.LabelHostname),
			},
			levels: defaultOneLevel,
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
			enableFeatureGates: []featuregate.Feature{features.TASProfileMostFreeCapacity},
		},
		"include usage from pending scheduled non-TAS pods, blocked assignment; MostFreeCapacity": {
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
			topologyRequest: &kueue.PodSetTopologyRequest{
				Required: ptr.To(corev1.LabelHostname),
			},
			levels: defaultOneLevel,
			requests: resources.Requests{
				corev1.ResourceCPU: 600,
			},
			count:      1,
			wantReason: `topology "default" doesn't allow to fit any of 1 pod(s)`,
		},
		"include usage from running non-TAS pods, blocked assignment; MostFreeCapacity": {
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
			topologyRequest: &kueue.PodSetTopologyRequest{
				Required: ptr.To(corev1.LabelHostname),
			},
			levels: defaultOneLevel,
			requests: resources.Requests{
				corev1.ResourceCPU: 600,
			},
			count:              1,
			wantReason:         `topology "default" doesn't allow to fit any of 1 pod(s)`,
			enableFeatureGates: []featuregate.Feature{features.TASProfileMostFreeCapacity},
		},
		"include usage from running non-TAS pods, found free capacity on another node; MostFreeCapacity": {
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
			topologyRequest: &kueue.PodSetTopologyRequest{
				Required: ptr.To(corev1.LabelHostname),
			},
			levels: defaultOneLevel,
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
			enableFeatureGates: []featuregate.Feature{features.TASProfileMostFreeCapacity},
		},
		"no assignment as node is not ready; MostFreeCapacity": {
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
			topologyRequest: &kueue.PodSetTopologyRequest{
				Required: ptr.To(corev1.LabelHostname),
			},
			nodeLabels: map[string]string{
				"zone": "zone-a",
			},
			levels: defaultOneLevel,
			requests: resources.Requests{
				corev1.ResourceCPU: 1000,
			},
			count:              1,
			wantReason:         "no topology domains at level: kubernetes.io/hostname",
			enableFeatureGates: []featuregate.Feature{features.TASProfileMostFreeCapacity},
		},
		"no assignment as node is unschedulable; MostFreeCapacity": {
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
			topologyRequest: &kueue.PodSetTopologyRequest{
				Required: ptr.To(corev1.LabelHostname),
			},
			nodeLabels: map[string]string{
				"zone": "zone-a",
			},
			levels: defaultOneLevel,
			requests: resources.Requests{
				corev1.ResourceCPU: 1000,
			},
			count:              1,
			wantReason:         "no topology domains at level: kubernetes.io/hostname",
			enableFeatureGates: []featuregate.Feature{features.TASProfileMostFreeCapacity},
		},
		"skip node which has untolerated taint; MostFreeCapacity": {
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
			topologyRequest: &kueue.PodSetTopologyRequest{
				Required: ptr.To(corev1.LabelHostname),
			},
			nodeLabels: map[string]string{
				"zone": "zone-a",
			},
			levels: defaultOneLevel,
			requests: resources.Requests{
				corev1.ResourceCPU: 1000,
			},
			count:              1,
			wantReason:         `topology "default" doesn't allow to fit any of 1 pod(s)`,
			enableFeatureGates: []featuregate.Feature{features.TASProfileMostFreeCapacity},
		},
		"allow to schedule on node with tolerated taint; MostFreeCapacity": {
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
			tolerations: []corev1.Toleration{
				{
					Key:      "example.com/gpu",
					Value:    "present",
					Operator: corev1.TolerationOpEqual,
				},
			},
			topologyRequest: &kueue.PodSetTopologyRequest{
				Required: ptr.To(corev1.LabelHostname),
			},
			nodeLabels: map[string]string{
				"zone": "zone-a",
			},
			levels: defaultOneLevel,
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
			enableFeatureGates: []featuregate.Feature{features.TASProfileMostFreeCapacity},
		},
		"no assignment as node does not have enough allocatable pods (.status.allocatable['pods']); MostFreeCapacity": {
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
			topologyRequest: &kueue.PodSetTopologyRequest{
				Required: ptr.To(corev1.LabelHostname),
			},
			nodeLabels: map[string]string{
				"zone": "zone-a",
			},
			levels: defaultOneLevel,
			requests: resources.Requests{
				corev1.ResourceCPU: 300,
			},
			count:              1,
			wantReason:         `topology "default" doesn't allow to fit any of 1 pod(s)`,
			enableFeatureGates: []featuregate.Feature{features.TASProfileMostFreeCapacity},
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
			topologyRequest: &kueue.PodSetTopologyRequest{
				Required: ptr.To(corev1.LabelHostname),
			},
			nodeLabels: map[string]string{
				"zone": "zone-a",
			},
			levels: defaultOneLevel,
			requests: resources.Requests{
				corev1.ResourceCPU: 300,
			},
			nodeSelector: map[string]string{
				"custom-label-1": "custom-value-1",
			},
			count:      1,
			wantReason: `topology "default" doesn't allow to fit any of 1 pod(s)`,
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
			topologyRequest: &kueue.PodSetTopologyRequest{
				Required: ptr.To(corev1.LabelHostname),
			},
			nodeLabels: map[string]string{
				"zone": "zone-a",
			},
			nodeSelector: map[string]string{
				"custom-label-1": "value-2",
			},
			levels: defaultOneLevel,
			requests: resources.Requests{
				corev1.ResourceCPU: 300,
			},
			count:      1,
			wantReason: `topology "default" doesn't allow to fit any of 1 pod(s)`,
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
			topologyRequest: &kueue.PodSetTopologyRequest{
				Required: ptr.To(corev1.LabelHostname),
			},
			nodeLabels: map[string]string{
				"zone": "zone-a",
			},
			nodeSelector: map[string]string{
				"custom-label-1": "value-2",
			},
			levels: defaultOneLevel,
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
				Tolerations:  tc.tolerations,
			}
			tasFlavorCache := tasCache.NewTASFlavorCache(topologyInformation, flavorInformation)

			snapshot, err := tasFlavorCache.snapshot(ctx)
			if err != nil {
				t.Fatalf("failed to build the snapshot: %v", err)
			}
			tasInput := TASPodSetRequests{
				PodSet: &kueue.PodSet{
					Name:            kueue.DefaultPodSetName,
					TopologyRequest: tc.topologyRequest,
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Tolerations:  tc.tolerations,
							NodeSelector: tc.nodeSelector,
						},
					},
				},
				SinglePodRequests: tc.requests,
				Count:             tc.count,
			}
			if tc.topologyRequest == nil {
				tasInput.Implied = true
			}
			flavorTASRequests := []TASPodSetRequests{tasInput}
			wantResult := make(TASAssignmentsResult)
			wantMainPodSetResult := tasPodSetAssignmentResult{
				FailureReason: tc.wantReason,
			}
			if tc.wantAssignment != nil {
				sort.Slice(tc.wantAssignment.Domains, func(i, j int) bool {
					return utiltas.DomainID(tc.wantAssignment.Domains[i].Values) < utiltas.DomainID(tc.wantAssignment.Domains[j].Values)
				})
				wantMainPodSetResult.TopologyAssignment = tc.wantAssignment
			}
			wantResult[kueue.DefaultPodSetName] = wantMainPodSetResult
			gotResult := snapshot.FindTopologyAssignmentsForFlavor(flavorTASRequests, false, nil)
			if diff := cmp.Diff(wantResult, gotResult); diff != "" {
				t.Errorf("unexpected topology assignment (-want,+got): %s", diff)
			}
		})
	}
}

func TestFindTopologyAssignmentForTwoPodSets(t *testing.T) {
	const (
		tasBlockLabel = "cloud.com/topology-block"
	)

	t.Run("find topology assignment for two podsets with overlapping domain", func(t *testing.T) {
		ctx, _ := utiltesting.ContextWithLog(t)
		nodes := []corev1.Node{
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
		}
		levels := []string{tasBlockLabel}
		requests := resources.Requests{
			corev1.ResourceCPU: 1000,
		}
		topologyRequest := &kueue.PodSetTopologyRequest{
			Preferred: ptr.To(tasBlockLabel),
		}
		wantAssignment1 := &kueue.TopologyAssignment{
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
		}
		wantAssignment2 := &kueue.TopologyAssignment{
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
		}

		snapshot := buildSnapshot(ctx, t, nodes, levels)

		tasInput1 := buildTASInput("podset1", topologyRequest, requests, 3)
		tasInput2 := buildTASInput("podset2", topologyRequest, requests, 3)

		flavorTASRequests := []TASPodSetRequests{tasInput1, tasInput2}

		wantResult := make(TASAssignmentsResult)
		wantResult["podset1"] = buildWantedResult(wantAssignment1)
		wantResult["podset2"] = buildWantedResult(wantAssignment2)

		gotResult := snapshot.FindTopologyAssignmentsForFlavor(flavorTASRequests, false, nil)
		if diff := cmp.Diff(wantResult, gotResult); diff != "" {
			t.Errorf("unexpected topology assignment (-want,+got): %s", diff)
		}
	})
}

func buildSnapshot(ctx context.Context, t *testing.T, nodes []corev1.Node, levels []string) *TASFlavorSnapshot {
	initialObjects := make([]client.Object, 0)
	for i := range nodes {
		initialObjects = append(initialObjects, &nodes[i])
	}
	clientBuilder := utiltesting.NewClientBuilder()
	clientBuilder.WithObjects(initialObjects...)
	_ = tasindexer.SetupIndexes(ctx, utiltesting.AsIndexer(clientBuilder))
	client := clientBuilder.Build()

	tasCache := NewTASCache(client)
	topologyInformation := topologyInformation{
		Levels: levels,
	}
	flavorInformation := flavorInformation{
		TopologyName: "default",
	}
	tasFlavorCache := tasCache.NewTASFlavorCache(topologyInformation, flavorInformation)

	snapshot, err := tasFlavorCache.snapshot(ctx)
	if err != nil {
		t.Fatalf("failed to build the snapshot: %v", err)
	}
	return snapshot
}

func buildTASInput(podsetName kueue.PodSetReference, topologyRequest *kueue.PodSetTopologyRequest, requests resources.Requests, podCount int32) TASPodSetRequests {
	return TASPodSetRequests{
		PodSet: &kueue.PodSet{
			Name:            podsetName,
			TopologyRequest: topologyRequest,
			Template: corev1.PodTemplateSpec{
				Spec: corev1.PodSpec{
					Tolerations:  []corev1.Toleration{},
					NodeSelector: map[string]string{},
				},
			},
		},
		SinglePodRequests: requests,
		Count:             podCount,
	}
}
func buildWantedResult(wantAssignment *kueue.TopologyAssignment) tasPodSetAssignmentResult {
	wantPodSetResult := tasPodSetAssignmentResult{
		FailureReason: "",
	}
	wantPodSetResult.TopologyAssignment = wantAssignment
	return wantPodSetResult
}
