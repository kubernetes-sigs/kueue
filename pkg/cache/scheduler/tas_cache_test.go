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

package scheduler

import (
	"fmt"
	"testing"

	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/component-base/featuregate"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	tasindexer "sigs.k8s.io/kueue/pkg/controller/tas/indexer"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/resources"
	"sigs.k8s.io/kueue/pkg/util/tas"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingnode "sigs.k8s.io/kueue/pkg/util/testingjobs/node"
	testingpod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
)

// PodSetTestCase defines a test case for a single podset in the consolidated test.
type PodSetTestCase struct {
	podSetName         string
	topologyRequest    *kueue.PodSetTopologyRequest
	requests           resources.Requests
	count              int32
	tolerations        []corev1.Toleration
	nodeSelector       map[string]string
	podSetGroupName    *string
	previousAssignment *kueue.TopologyAssignment
	wantAssignment     *tas.TopologyAssignment
	wantReason         string
}

func TestFindTopologyAssignments(t *testing.T) {
	const (
		tasDataCenterLabel = "cloud.com/datacenter"
		tasAIZoneLabel     = "cloud.com/aizone"
		tasBlockLabel      = "cloud.com/topology-block"
		tasRackLabel       = "cloud.com/topology-rack"
		tasSubBlockLabel   = "cloud.com/topology-subblock"
	)

	//      b1                   b2
	//   /      \             /      \
	//  r1       r2          r1       r2
	//  |      /  |  \       |         |
	//  x3    x5  x1  x6     x2       x4
	defaultNodes := []corev1.Node{
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
		*testingnode.MakeNode("b1-r2-x5").
			Label(tasBlockLabel, "b1").
			Label(tasRackLabel, "r2").
			Label(corev1.LabelHostname, "x5").
			StatusAllocatable(corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("1"),
				corev1.ResourceMemory: resource.MustParse("1Gi"),
				corev1.ResourcePods:   resource.MustParse("10"),
			}).
			Ready().
			Obj(),
		*testingnode.MakeNode("b1-r2-x1").
			Label(tasBlockLabel, "b1").
			Label(tasRackLabel, "r2").
			Label(corev1.LabelHostname, "x1").
			StatusAllocatable(corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("1"),
				corev1.ResourceMemory: resource.MustParse("1Gi"),
				corev1.ResourcePods:   resource.MustParse("10"),
			}).
			Ready().
			Obj(),
		*testingnode.MakeNode("b1-r2-x6").
			Label(tasBlockLabel, "b1").
			Label(tasRackLabel, "r2").
			Label(corev1.LabelHostname, "x6").
			StatusAllocatable(corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("1"),
				corev1.ResourceMemory: resource.MustParse("1Gi"),
				corev1.ResourcePods:   resource.MustParse("10"),
			}).
			Ready().
			Obj(),
		*testingnode.MakeNode("b2-r2-x2").
			Label(tasBlockLabel, "b2").
			Label(tasRackLabel, "r1").
			Label(corev1.LabelHostname, "x2").
			StatusAllocatable(corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("1"),
				corev1.ResourceMemory: resource.MustParse("1Gi"),
				corev1.ResourcePods:   resource.MustParse("10"),
			}).
			Ready().
			Obj(),
		*testingnode.MakeNode("b2-r2-x4").
			Label(tasBlockLabel, "b2").
			Label(tasRackLabel, "r2").
			Label(corev1.LabelHostname, "x4").
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
	//   x3  x5  x1     x6  x2
	scatteredNodes := []corev1.Node{
		*testingnode.MakeNode("b1-r1-x3").
			Label(tasBlockLabel, "b1").
			Label(tasRackLabel, "r1").
			Label(corev1.LabelHostname, "x3").
			StatusAllocatable(corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("4"),
				corev1.ResourceMemory: resource.MustParse("1Gi"),
				corev1.ResourcePods:   resource.MustParse("10"),
			}).
			Ready().
			Obj(),
		*testingnode.MakeNode("b1-r1-x5").
			Label(tasBlockLabel, "b1").
			Label(tasRackLabel, "r1").
			Label(corev1.LabelHostname, "x5").
			StatusAllocatable(corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("1"),
				corev1.ResourceMemory: resource.MustParse("1Gi"),
				corev1.ResourcePods:   resource.MustParse("10"),
			}).
			Ready().
			Obj(),
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
		*testingnode.MakeNode("b2-r1-x6").
			Label(tasBlockLabel, "b2").
			Label(tasRackLabel, "r1").
			Label(corev1.LabelHostname, "x6").
			StatusAllocatable(corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("2"),
				corev1.ResourceMemory: resource.MustParse("1Gi"),
				corev1.ResourcePods:   resource.MustParse("10"),
			}).
			Ready().
			Obj(),
		*testingnode.MakeNode("b2-r1-x2").
			Label(tasBlockLabel, "b2").
			Label(tasRackLabel, "r1").
			Label(corev1.LabelHostname, "x2").
			StatusAllocatable(corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("1"),
				corev1.ResourceMemory: resource.MustParse("1Gi"),
				corev1.ResourcePods:   resource.MustParse("10"),
			}).
			Ready().
			Obj(),
	}

	//		b1                b2
	//	   /     \           /     \
	//	 r1      r2        r1      r2
	//	 |    /  |  \      |        |
	//	x3  x5  x1  x6    x2       x4
	multipodNodeset := []corev1.Node{*testingnode.MakeNode("b1-r1-x3").
		Label(tasBlockLabel, "b1").
		Label(tasRackLabel, "r1").
		Label(corev1.LabelHostname, "x3").
		StatusAllocatable(corev1.ResourceList{
			corev1.ResourceCPU:    resource.MustParse("10"),
			corev1.ResourceMemory: resource.MustParse("1Gi"),
			corev1.ResourcePods:   resource.MustParse("10"),
		}).
		Ready().
		Obj(),
		*testingnode.MakeNode("b1-r2-x5").
			Label(tasBlockLabel, "b1").
			Label(tasRackLabel, "r2").
			Label(corev1.LabelHostname, "x5").
			StatusAllocatable(corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("10"),
				corev1.ResourceMemory: resource.MustParse("1Gi"),
				corev1.ResourcePods:   resource.MustParse("10"),
			}).
			Ready().
			Obj(),
		*testingnode.MakeNode("b1-r2-x1").
			Label(tasBlockLabel, "b1").
			Label(tasRackLabel, "r2").
			Label(corev1.LabelHostname, "x1").
			StatusAllocatable(corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("10"),
				corev1.ResourceMemory: resource.MustParse("1Gi"),
				corev1.ResourcePods:   resource.MustParse("10"),
			}).
			Ready().
			Obj(),
		*testingnode.MakeNode("b1-r2-x6").
			Label(tasBlockLabel, "b1").
			Label(tasRackLabel, "r2").
			Label(corev1.LabelHostname, "x6").
			StatusAllocatable(corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("10"),
				corev1.ResourceMemory: resource.MustParse("1Gi"),
				corev1.ResourcePods:   resource.MustParse("10"),
			}).
			Ready().
			Obj(),
		*testingnode.MakeNode("b2-r1-x2").
			Label(tasBlockLabel, "b2").
			Label(tasRackLabel, "r1").
			Label(corev1.LabelHostname, "x2").
			StatusAllocatable(corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("10"),
				corev1.ResourceMemory: resource.MustParse("1Gi"),
				corev1.ResourcePods:   resource.MustParse("10"),
			}).
			Ready().
			Obj(),
		*testingnode.MakeNode("b2-r2-x4").
			Label(tasBlockLabel, "b2").
			Label(tasRackLabel, "r2").
			Label(corev1.LabelHostname, "x4").
			StatusAllocatable(corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("20"),
				corev1.ResourceMemory: resource.MustParse("4Gi"),
				corev1.ResourcePods:   resource.MustParse("40"),
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
	//    x3   x5   x1    x6     x2   x4  x7    x4
	binaryTreesNodes := []corev1.Node{
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
		*testingnode.MakeNode("b1-r1-x5").
			Label(tasBlockLabel, "b1").
			Label(tasRackLabel, "r1").
			Label(corev1.LabelHostname, "x5").
			StatusAllocatable(corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("1"),
				corev1.ResourceMemory: resource.MustParse("1Gi"),
				corev1.ResourcePods:   resource.MustParse("10"),
			}).
			Ready().
			Obj(),
		*testingnode.MakeNode("b1-r2-x1").
			Label(tasBlockLabel, "b1").
			Label(tasRackLabel, "r2").
			Label(corev1.LabelHostname, "x1").
			StatusAllocatable(corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("1"),
				corev1.ResourceMemory: resource.MustParse("1Gi"),
				corev1.ResourcePods:   resource.MustParse("10"),
			}).
			Ready().
			Obj(),
		*testingnode.MakeNode("b1-r2-x6").
			Label(tasBlockLabel, "b1").
			Label(tasRackLabel, "r2").
			Label(corev1.LabelHostname, "x6").
			StatusAllocatable(corev1.ResourceList{
				corev1.ResourceCPU:    resource.MustParse("1"),
				corev1.ResourceMemory: resource.MustParse("1Gi"),
				corev1.ResourcePods:   resource.MustParse("10"),
			}).
			Ready().
			Obj(),
		*testingnode.MakeNode("b2-r1-x2").
			Label(tasBlockLabel, "b2").
			Label(tasRackLabel, "r1").
			Label(corev1.LabelHostname, "x2").
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
		featureGates map[featuregate.Feature]bool
		nodes        []corev1.Node
		pods         []corev1.Pod
		levels       []string
		nodeLabels   map[string]string
		podSets      []PodSetTestCase
	}{
		"minimize the number of used racks before optimizing the number of nodes; BestFit": {
			// Solution by optimizing the number of racks then nodes: [r3]: [x1,x6,x2,x4]
			// Solution by optimizing the number of nodes: [r1,r2]: [x3,x5]
			//
			//       b1
			//   /   |    \
			//  r1   r2   r3
			//  |     |    |   \   \     \
			// x3:2,x5:2,x1:1,x6:1,x2:1,x4:1
			//
			nodes: []corev1.Node{
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
				*testingnode.MakeNode("b1-r2-x5").
					Label(tasBlockLabel, "b1").
					Label(tasRackLabel, "r2").
					Label(corev1.LabelHostname, "x5").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("2"),
						corev1.ResourcePods: resource.MustParse("20"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r3-x1").
					Label(tasBlockLabel, "b1").
					Label(tasRackLabel, "r3").
					Label(corev1.LabelHostname, "x1").
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
				*testingnode.MakeNode("b1-r3-x2").
					Label(tasBlockLabel, "b1").
					Label(tasRackLabel, "r3").
					Label(corev1.LabelHostname, "x2").
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
				wantAssignment: &tas.TopologyAssignment{
					Levels: defaultOneLevel,
					Domains: []tas.TopologyDomainAssignment{
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
								"x4",
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
		},
		"choose the node that can accommodate all Pods": {
			//        b1
			//         |
			//        r1
			//    /    |    \
			// x3:2, x5:1, x1:1
			nodes: []corev1.Node{
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
				*testingnode.MakeNode("b1-r1-x5").
					Label(tasBlockLabel, "b1").
					Label(tasRackLabel, "r1").
					Label(corev1.LabelHostname, "x5").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("1"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
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
				wantAssignment: &tas.TopologyAssignment{
					Levels: defaultOneLevel,
					Domains: []tas.TopologyDomainAssignment{
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
		"no annotation; implied default to unconstrained; 6 pods fit into hosts scattered across the whole datacenter even they could fit into single rack; BestFit": {
			nodes:  scatteredNodes,
			levels: defaultThreeLevels,
			podSets: []PodSetTestCase{{
				requests: resources.Requests{
					corev1.ResourceCPU: 1000,
				},
				count: 6,
				wantAssignment: &tas.TopologyAssignment{
					Levels: defaultOneLevel,
					Domains: []tas.TopologyDomainAssignment{
						{Count: 1, Values: []string{"x1"}},
						{Count: 1, Values: []string{"x3"}},
						{Count: 1, Values: []string{"x5"}},
						{Count: 1, Values: []string{"x2"}},
						{Count: 2, Values: []string{"x6"}},
					},
				},
			}},
		},
		"unconstrained; 6 pods fit into hosts scattered across the whole datacenter even they could fit into single rack; BestFit": {
			nodes:  scatteredNodes,
			levels: defaultThreeLevels,
			podSets: []PodSetTestCase{{
				topologyRequest: &kueue.PodSetTopologyRequest{
					Unconstrained: new(true),
				},
				requests: resources.Requests{
					corev1.ResourceCPU: 1000,
				},
				count: 6,
				wantAssignment: &tas.TopologyAssignment{
					Levels: defaultOneLevel,
					Domains: []tas.TopologyDomainAssignment{
						{Count: 1, Values: []string{"x1"}},
						{Count: 1, Values: []string{"x3"}},
						{Count: 1, Values: []string{"x5"}},
						{Count: 1, Values: []string{"x2"}},
						{Count: 2, Values: []string{"x6"}},
					},
				},
			}},
		},
		"unconstrained; a single pod fits into each host; BestFit": {
			nodes:  defaultNodes,
			levels: defaultThreeLevels,
			podSets: []PodSetTestCase{{
				topologyRequest: &kueue.PodSetTopologyRequest{
					Unconstrained: new(true),
				},
				requests: resources.Requests{
					corev1.ResourceCPU: 1000,
				},
				count: 1,
				wantAssignment: &tas.TopologyAssignment{
					Levels: defaultOneLevel,
					Domains: []tas.TopologyDomainAssignment{
						{
							Count: 1,
							Values: []string{
								"x3",
							},
						},
					},
				},
			}},
		},
		"unconstrained; a single pod fits into each host; LeastFreeCapacity; TASProfileMixed": {
			nodes:  defaultNodes,
			levels: defaultThreeLevels,
			podSets: []PodSetTestCase{{
				topologyRequest: &kueue.PodSetTopologyRequest{
					Unconstrained: new(true),
				},
				requests: resources.Requests{
					corev1.ResourceCPU: 1000,
				},
				count: 1,
				wantAssignment: &tas.TopologyAssignment{
					Levels: defaultOneLevel,
					Domains: []tas.TopologyDomainAssignment{
						{
							Count: 1,
							Values: []string{
								"x3",
							},
						},
					},
				},
			}},
			featureGates: map[featuregate.Feature]bool{features.TASProfileMixed: true},
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
				wantAssignment: &tas.TopologyAssignment{
					Levels: defaultOneLevel,
					Domains: []tas.TopologyDomainAssignment{
						{
							Count: 1,
							Values: []string{
								"x3",
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
								"x1",
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
				wantAssignment: &tas.TopologyAssignment{
					Levels: defaultOneLevel,
					Domains: []tas.TopologyDomainAssignment{
						{
							Count: 1,
							Values: []string{
								"x3",
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
				wantAssignment: &tas.TopologyAssignment{
					Levels: defaultOneLevel,
					Domains: []tas.TopologyDomainAssignment{
						{
							Count: 1,
							Values: []string{
								"x3",
							},
						},
					},
				},
			}},
			featureGates: map[featuregate.Feature]bool{features.TASProfileMixed: true},
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
				wantAssignment: &tas.TopologyAssignment{
					Levels: defaultOneLevel,
					Domains: []tas.TopologyDomainAssignment{
						{
							Count: 1,
							Values: []string{
								"x3",
							},
						},
					},
				},
			}},
			featureGates: map[featuregate.Feature]bool{features.TASProfileMixed: true},
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
				wantAssignment: &tas.TopologyAssignment{
					Levels: defaultTwoLevels,
					Domains: []tas.TopologyDomainAssignment{
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
				wantAssignment: &tas.TopologyAssignment{
					Levels: defaultTwoLevels,
					Domains: []tas.TopologyDomainAssignment{
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
				wantAssignment: &tas.TopologyAssignment{
					Levels: []string{tasBlockLabel},
					Domains: []tas.TopologyDomainAssignment{
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
				wantAssignment: &tas.TopologyAssignment{
					Levels: defaultTwoLevels,
					Domains: []tas.TopologyDomainAssignment{
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
				wantAssignment: &tas.TopologyAssignment{
					Levels: []string{
						tasBlockLabel,
						tasRackLabel,
					},
					Domains: []tas.TopologyDomainAssignment{
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
				wantAssignment: &tas.TopologyAssignment{
					Levels: []string{
						tasBlockLabel,
						tasRackLabel,
					},
					Domains: []tas.TopologyDomainAssignment{
						{
							Count: 1,
							Values: []string{
								"b1",
								"r1",
							},
						},
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
				wantAssignment: &tas.TopologyAssignment{
					Levels: defaultTwoLevels,
					Domains: []tas.TopologyDomainAssignment{
						{
							Count: 1,
							Values: []string{
								"b1",
								"r1",
							},
						},
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
				wantReason: `topology "default" doesn't allow to fit any of 1 pod(s). Total nodes: 4; excluded: resource "cpu": 4`,
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
				wantAssignment: &tas.TopologyAssignment{
					Levels: defaultTwoLevels,
					Domains: []tas.TopologyDomainAssignment{
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
				wantAssignment: &tas.TopologyAssignment{
					Levels: defaultTwoLevels,
					Domains: []tas.TopologyDomainAssignment{
						{
							Count: 1,
							Values: []string{
								"b1",
								"r1",
							},
						},
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
				wantAssignment: &tas.TopologyAssignment{
					Levels: defaultTwoLevels,
					Domains: []tas.TopologyDomainAssignment{
						{
							Count: 1,
							Values: []string{
								"b1",
								"r1",
							},
						},
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
				wantAssignment: &tas.TopologyAssignment{
					Levels: defaultTwoLevels,
					Domains: []tas.TopologyDomainAssignment{
						{
							Count: 1,
							Values: []string{
								"b1",
								"r1",
							},
						},
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
				*testingnode.MakeNode("b1-r1-x3").
					Label("zone", "zone-a").
					Label(corev1.LabelHostname, "x3").
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
				*testingnode.MakeNode("b1-r1-x3").
					Label("zone", "zone-a").
					Label(corev1.LabelHostname, "x3").
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
				wantAssignment: &tas.TopologyAssignment{
					Levels: defaultOneLevel,
					Domains: []tas.TopologyDomainAssignment{
						{
							Count: 1,
							Values: []string{
								"x3",
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
				*testingnode.MakeNode("b1-r1-x3").
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
				*testingnode.MakeNode("x3").
					Label(corev1.LabelHostname, "x3").
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
				wantAssignment: &tas.TopologyAssignment{
					Levels: defaultOneLevel,
					Domains: []tas.TopologyDomainAssignment{
						{
							Count: 1,
							Values: []string{
								"x3",
							},
						},
					},
				},
			}},
		},
		"don't consider terminal pods when computing the capacity; BestFit": {
			nodes: []corev1.Node{
				*testingnode.MakeNode("x3").
					Label(corev1.LabelHostname, "x3").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
						corev1.ResourcePods:   resource.MustParse("10"),
					}).
					Ready().
					Obj(),
			},
			pods: []corev1.Pod{
				*testingpod.MakePod("test-failed", "test-ns").NodeName("x3").
					Request(corev1.ResourceCPU, "600m").
					StatusPhase(corev1.PodFailed).
					Obj(),
				*testingpod.MakePod("test-succeeded", "test-ns").NodeName("x3").
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
				wantAssignment: &tas.TopologyAssignment{
					Levels: defaultOneLevel,
					Domains: []tas.TopologyDomainAssignment{
						{
							Count: 1,
							Values: []string{
								"x3",
							},
						},
					},
				},
			}},
		},
		"include usage from pending scheduled non-TAS pods, blocked assignment; BestFit": {
			// there is not enough free capacity on the only node x3
			nodes: []corev1.Node{
				*testingnode.MakeNode("x3").
					Label(corev1.LabelHostname, "x3").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
						corev1.ResourcePods:   resource.MustParse("10"),
					}).
					Ready().
					Obj(),
			},
			pods: []corev1.Pod{
				*testingpod.MakePod("test-pending", "test-ns").NodeName("x3").
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
				wantReason: `topology "default" doesn't allow to fit any of 1 pod(s). Total nodes: 1; excluded: resource "cpu": 1`,
			}},
		},
		"include usage from running non-TAS pods, blocked assignment; BestFit": {
			// there is not enough free capacity on the only node x3
			nodes: []corev1.Node{
				*testingnode.MakeNode("x3").
					Label(corev1.LabelHostname, "x3").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
						corev1.ResourcePods:   resource.MustParse("10"),
					}).
					Ready().
					Obj(),
			},
			pods: []corev1.Pod{
				*testingpod.MakePod("test-running", "test-ns").NodeName("x3").
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
				wantReason: `topology "default" doesn't allow to fit any of 1 pod(s). Total nodes: 1; excluded: resource "cpu": 1`,
			}},
		},
		"include usage from non-TAS pods; pod usage": {
			// this test case ensures we are counting pods properly
			// when aggregating non-tas pod usage by node.
			nodes: []corev1.Node{
				*testingnode.MakeNode("x3").
					Label(corev1.LabelHostname, "x3").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
			},
			pods: []corev1.Pod{
				*testingpod.MakePod("running1", "test-ns").NodeName("x3").
					StatusPhase(corev1.PodRunning).
					Obj(),
				*testingpod.MakePod("running2", "test-ns").NodeName("x3").
					StatusPhase(corev1.PodRunning).
					Obj(),
			},
			levels: defaultOneLevel,
			podSets: []PodSetTestCase{{
				topologyRequest: &kueue.PodSetTopologyRequest{
					Required: ptr.To(corev1.LabelHostname),
				},
				requests: resources.Requests{
					corev1.ResourceCPU: 0,
				},
				count:      9,
				wantReason: `topology "default" allows to fit only 8 out of 9 pod(s)`,
			}},
		},
		"include usage from running non-TAS pods, found free capacity on another node; BestFit": {
			// there is not enough free capacity on the node x3 as the
			// assignments lends on the free x5
			nodes: []corev1.Node{
				*testingnode.MakeNode("x3").
					Label(corev1.LabelHostname, "x3").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
						corev1.ResourcePods:   resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("x5").
					Label(corev1.LabelHostname, "x5").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
						corev1.ResourcePods:   resource.MustParse("10"),
					}).
					Ready().
					Obj(),
			},
			pods: []corev1.Pod{
				*testingpod.MakePod("test-pod", "test-ns").NodeName("x3").
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
				wantAssignment: &tas.TopologyAssignment{
					Levels: defaultOneLevel,
					Domains: []tas.TopologyDomainAssignment{
						{
							Count: 1,
							Values: []string{
								"x5",
							},
						},
					},
				},
			}},
		},
		"no assignment as node is not ready; BestFit": {
			nodes: []corev1.Node{
				*testingnode.MakeNode("b1-r1-x3").
					Label("zone", "zone-a").
					Label(corev1.LabelHostname, "x3").
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
				*testingnode.MakeNode("b1-r1-x3").
					Label("zone", "zone-a").
					Label(corev1.LabelHostname, "x3").
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
				*testingnode.MakeNode("x3").
					Label("zone", "zone-a").
					Label(corev1.LabelHostname, "x3").
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
				wantReason: `topology "default" doesn't allow to fit any of 1 pod(s). Total nodes: 1; excluded: taint "example.com/gpu=present:NoSchedule": 1`,
			}},
		},
		"detailed failure message with exclusion stats": {
			nodes: []corev1.Node{
				*testingnode.MakeNode("x1").
					Label(corev1.LabelHostname, "x1").
					Taints(corev1.Taint{
						Key:    "key",
						Value:  "value",
						Effect: corev1.TaintEffectNoSchedule,
					}).
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("1"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("x2").
					Label(corev1.LabelHostname, "x2").
					Label("zone", "zone-b"). // Wrong zone
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("1"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("x3").
					Label(corev1.LabelHostname, "x3").
					Label("zone", "zone-b"). // Wrong zone for nodeSelector
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("2"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("x4").
					Label(corev1.LabelHostname, "x4").
					Label("zone", "zone-a"). // Correct zone but insufficient CPU
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("100m"),
						corev1.ResourcePods: resource.MustParse("10"),
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
				nodeSelector: map[string]string{
					"zone": "zone-a",
				},
				count:      1,
				wantReason: `topology "default" doesn't allow to fit any of 1 pod(s). Total nodes: 4; excluded: nodeSelector: 2, resource "cpu": 1, taint "key=value:NoSchedule": 1`,
			}},
		},
		"resource exclusion picks most restrictive resource": {
			nodes: []corev1.Node{
				*testingnode.MakeNode("dual-shortage").
					Label(corev1.LabelHostname, "dual-shortage").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:                     resource.MustParse("500m"),
						corev1.ResourcePods:                    resource.MustParse("10"),
						corev1.ResourceName("example.com/gpu"): resource.MustParse("0"),
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
					corev1.ResourceCPU:                     1000,
					corev1.ResourceName("example.com/gpu"): 1,
				},
				count: 1,
				// When both resources give count=0, alphabetical tie-breaking picks "cpu"
				wantReason: `topology "default" doesn't allow to fit any of 1 pod(s). Total nodes: 1; excluded: resource "cpu": 1`,
			}},
		},
		"allow to schedule on node with tolerated taint; BestFit": {
			nodes: []corev1.Node{
				*testingnode.MakeNode("b1-r1-x3").
					Label("zone", "zone-a").
					Label(corev1.LabelHostname, "x3").
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
				wantAssignment: &tas.TopologyAssignment{
					Levels: defaultOneLevel,
					Domains: []tas.TopologyDomainAssignment{
						{
							Count: 1,
							Values: []string{
								"x3",
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
				*testingnode.MakeNode("b1-r1-x3").
					Label("zone", "zone-a").
					Label(corev1.LabelHostname, "x3").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("1000m"),
						corev1.ResourcePods: resource.MustParse("1"),
					}).
					Ready().
					Obj(),
			},
			pods: []corev1.Pod{
				*testingpod.MakePod("test-running", "test-ns").
					NodeName("b1-r1-x3").
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
				wantReason: `topology "default" doesn't allow to fit any of 1 pod(s). Total nodes: 1; excluded: resource "pods": 1`,
			}},
		},
		"skip node which doesn't match node selector, missing label; BestFit": {
			nodes: []corev1.Node{
				*testingnode.MakeNode("x3").
					Label("zone", "zone-a").
					Label(corev1.LabelHostname, "x3").
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
				wantReason: `topology "default" doesn't allow to fit any of 1 pod(s). Total nodes: 1; excluded: nodeSelector: 1`,
				nodeSelector: map[string]string{
					"custom-label-1": "custom-value-1",
				},
			}},
		},
		"skip node which doesn't match node selector, label exists, value doesn't match; BestFit": {
			nodes: []corev1.Node{
				*testingnode.MakeNode("x3").
					Label("zone", "zone-a").
					Label(corev1.LabelHostname, "x3").
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
				wantReason: `topology "default" doesn't allow to fit any of 1 pod(s). Total nodes: 1; excluded: nodeSelector: 1`,
				nodeSelector: map[string]string{
					"custom-label-1": "value-2",
				},
			}},
		},
		"allow to schedule on node which matches node; BestFit": {
			nodes: []corev1.Node{
				*testingnode.MakeNode("b1-r1-x3").
					Label("zone", "zone-a").
					Label(corev1.LabelHostname, "x3").
					Label("custom-label-1", "value-1").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
						corev1.ResourcePods:   resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r1-x5").
					Label("zone", "zone-a").
					Label(corev1.LabelHostname, "x5").
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
				wantAssignment: &tas.TopologyAssignment{
					Levels: defaultOneLevel,
					Domains: []tas.TopologyDomainAssignment{
						{
							Count: 1,
							Values: []string{
								"x5",
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
			// x3:3, x5:3, x1:3
			//
			nodes: []corev1.Node{
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
				*testingnode.MakeNode("b1-r1-x5").
					Label(tasBlockLabel, "b1").
					Label(tasRackLabel, "r1").
					Label(corev1.LabelHostname, "x5").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("3"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
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
			},
			levels: defaultThreeLevels,
			podSets: []PodSetTestCase{{
				topologyRequest: &kueue.PodSetTopologyRequest{
					Required:                    ptr.To(tasBlockLabel),
					PodSetSliceRequiredTopology: ptr.To(corev1.LabelHostname),
					PodSetSliceSize:             new(int32(2)),
				},
				requests: resources.Requests{
					corev1.ResourceCPU: 1000,
				},
				count: 6,
				wantAssignment: &tas.TopologyAssignment{
					Levels: defaultOneLevel,
					Domains: []tas.TopologyDomainAssignment{
						{
							Count: 2,
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
						{
							Count: 2,
							Values: []string{
								"x5",
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
			// x3:6  x5:5   x1:4  x6:2
			//
			nodes: []corev1.Node{
				*testingnode.MakeNode("b1-r1-x3").
					Label(tasBlockLabel, "b1").
					Label(tasRackLabel, "r1").
					Label(corev1.LabelHostname, "x3").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("6"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r1-x5").
					Label(tasBlockLabel, "b1").
					Label(tasRackLabel, "r1").
					Label(corev1.LabelHostname, "x5").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("5"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
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
				*testingnode.MakeNode("b1-r1-x6").
					Label(tasBlockLabel, "b1").
					Label(tasRackLabel, "r1").
					Label(corev1.LabelHostname, "x6").
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
					PodSetSliceSize:             new(int32(2)),
				},
				requests: resources.Requests{
					corev1.ResourceCPU: 1000,
				},
				count: 12,
				wantAssignment: &tas.TopologyAssignment{
					Levels: defaultOneLevel,
					Domains: []tas.TopologyDomainAssignment{
						{
							Count: 4,
							Values: []string{
								"x1",
							},
						},
						{
							Count: 6,
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
		"block required for podset; host required for slices; select domains with tight fit; BestFit": {
			//        b1
			//         |
			//        r1
			//    /    |    \
			// x3:3, x5:2, x1:2
			//
			nodes: []corev1.Node{
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
				*testingnode.MakeNode("b1-r1-x5").
					Label(tasBlockLabel, "b1").
					Label(tasRackLabel, "r1").
					Label(corev1.LabelHostname, "x5").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("2"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
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
			},
			levels: defaultThreeLevels,
			podSets: []PodSetTestCase{{
				topologyRequest: &kueue.PodSetTopologyRequest{
					Required:                    ptr.To(tasBlockLabel),
					PodSetSliceRequiredTopology: ptr.To(corev1.LabelHostname),
					PodSetSliceSize:             new(int32(2)),
				},
				requests: resources.Requests{
					corev1.ResourceCPU: 1000,
				},
				count: 4,
				wantAssignment: &tas.TopologyAssignment{
					Levels: defaultOneLevel,
					Domains: []tas.TopologyDomainAssignment{
						{
							Count: 2,
							Values: []string{
								"x1",
							},
						},
						{
							Count: 2,
							Values: []string{
								"x5",
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
			// x3 x5  x1 x6  x2   x4   x7
			nodes: []corev1.Node{
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
				*testingnode.MakeNode("b1-r1-x5").
					Label(tasBlockLabel, "b1").
					Label(tasRackLabel, "r1").
					Label(corev1.LabelHostname, "x5").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("1"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r2-x1").
					Label(tasBlockLabel, "b1").
					Label(tasRackLabel, "r2").
					Label(corev1.LabelHostname, "x1").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("1"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r2-x6").
					Label(tasBlockLabel, "b1").
					Label(tasRackLabel, "r2").
					Label(corev1.LabelHostname, "x6").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("1"),
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
				*testingnode.MakeNode("b2-r1-x4").
					Label(tasBlockLabel, "b2").
					Label(tasRackLabel, "r1").
					Label(corev1.LabelHostname, "x4").
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
					PodSetSliceSize:             new(int32(2)),
				},
				requests: resources.Requests{
					corev1.ResourceCPU: 1000,
				},
				count: 4,
				wantAssignment: &tas.TopologyAssignment{
					Levels: defaultOneLevel,
					Domains: []tas.TopologyDomainAssignment{
						{
							Count: 1,
							Values: []string{
								"x3",
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
								"x1",
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
		},
		"block preferred for podset; rack required for slices; BestFit": {
			nodes:  defaultNodes,
			levels: defaultThreeLevels,
			podSets: []PodSetTestCase{{
				topologyRequest: &kueue.PodSetTopologyRequest{
					Preferred:                   ptr.To(tasBlockLabel),
					PodSetSliceRequiredTopology: ptr.To(tasRackLabel),
					PodSetSliceSize:             new(int32(2)),
				},
				requests: resources.Requests{
					corev1.ResourceCPU: 1000,
				},
				count: 4,
				wantAssignment: &tas.TopologyAssignment{
					Levels: defaultOneLevel,
					Domains: []tas.TopologyDomainAssignment{
						{
							Count: 1,
							Values: []string{
								"x1",
							},
						},
						{
							Count: 1,
							Values: []string{
								"x5",
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
		"block required for podset; host required for slices; optimize last domain; BestFit": {
			//        b1
			//         |
			//        r1
			//    /    |    \
			// x3:4, x5:3, x1:2
			//
			nodes: []corev1.Node{
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
				*testingnode.MakeNode("b1-r1-x5").
					Label(tasBlockLabel, "b1").
					Label(tasRackLabel, "r1").
					Label(corev1.LabelHostname, "x5").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("3"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
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
			},
			levels: defaultThreeLevels,
			podSets: []PodSetTestCase{{
				topologyRequest: &kueue.PodSetTopologyRequest{
					Required:                    ptr.To(tasBlockLabel),
					PodSetSliceRequiredTopology: ptr.To(corev1.LabelHostname),
					PodSetSliceSize:             new(int32(2)),
				},
				requests: resources.Requests{
					corev1.ResourceCPU: 1000,
				},
				count: 6,
				wantAssignment: &tas.TopologyAssignment{
					Levels: defaultOneLevel,
					Domains: []tas.TopologyDomainAssignment{
						{
							Count: 2,
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
					},
				},
			}},
		},
		"block preferred for podset; host required for slices; 2 blocks with unbalanced subdomains; BestFit": {
			//nolint:dupword // suppress duplicate r1 word
			//        b1                b2
			//         |                 |
			//        r1                r1
			//    /    |    \            |
			// x3:3, x5:3, x1:3   		x6:6
			//
			nodes: []corev1.Node{
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
				*testingnode.MakeNode("b1-r1-x5").
					Label(tasBlockLabel, "b1").
					Label(tasRackLabel, "r1").
					Label(corev1.LabelHostname, "x5").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("3"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
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
				*testingnode.MakeNode("b2-r1-x6").
					Label(tasBlockLabel, "b2").
					Label(tasRackLabel, "r1").
					Label(corev1.LabelHostname, "x6").
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
					PodSetSliceSize:             new(int32(3)),
				},
				requests: resources.Requests{
					corev1.ResourceCPU: 1000,
				},
				count: 12,
				wantAssignment: &tas.TopologyAssignment{
					Levels: defaultOneLevel,
					Domains: []tas.TopologyDomainAssignment{
						{
							Count: 3,
							Values: []string{
								"x1",
							},
						},
						{
							Count: 3,
							Values: []string{
								"x3",
							},
						},
						{
							Count: 6,
							Values: []string{
								"x6",
							},
						},
					},
				},
			}},
		},
		//         b1
		//       /  |
		//     r1   r2
		//    /     |
		// x1:15   x2:15
		// request: 25
		// expected outcome: x1:13, x2:12
		"balanced placement; basic": {
			featureGates: map[featuregate.Feature]bool{features.TASBalancedPlacement: true},
			nodes: []corev1.Node{
				*testingnode.MakeNode("b1-r1-x1").
					Label(tasBlockLabel, "b1").
					Label(tasRackLabel, "r1").
					Label(corev1.LabelHostname, "x1").
					StatusAllocatable(corev1.ResourceList{
						"example.com/gpu":   resource.MustParse("15"),
						corev1.ResourcePods: resource.MustParse("25"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r2-x2").
					Label(tasBlockLabel, "b1").
					Label(tasRackLabel, "r2").
					Label(corev1.LabelHostname, "x2").
					StatusAllocatable(corev1.ResourceList{
						"example.com/gpu":   resource.MustParse("15"),
						corev1.ResourcePods: resource.MustParse("25"),
					}).
					Ready().
					Obj(),
			},
			levels: defaultThreeLevels,
			podSets: []PodSetTestCase{{
				topologyRequest: &kueue.PodSetTopologyRequest{
					Preferred: ptr.To(tasRackLabel),
				},
				requests: resources.Requests{
					"example.com/gpu": 1,
				},
				count: 25,
				wantAssignment: &tas.TopologyAssignment{
					Levels: defaultOneLevel,
					Domains: []tas.TopologyDomainAssignment{
						{
							Count:  13,
							Values: []string{"x1"},
						},
						{
							Count:  12,
							Values: []string{"x2"},
						},
					},
				},
			}},
		},
		//        b1
		//         |
		//        r1
		//    /    |    \
		// x1:15, x2:13, x3:10
		// request: 23
		// expected outcome: x1:12, x2:11
		"balanced placement; select optmial set of domains": {
			featureGates: map[featuregate.Feature]bool{features.TASBalancedPlacement: true},
			nodes: []corev1.Node{
				*testingnode.MakeNode("b1-r1-x1").
					Label(tasBlockLabel, "b1").
					Label(tasRackLabel, "r1").
					Label(corev1.LabelHostname, "x1").
					StatusAllocatable(corev1.ResourceList{
						"example.com/gpu":   resource.MustParse("15"),
						corev1.ResourcePods: resource.MustParse("23"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r1-x2").
					Label(tasBlockLabel, "b1").
					Label(tasRackLabel, "r1").
					Label(corev1.LabelHostname, "x2").
					StatusAllocatable(corev1.ResourceList{
						"example.com/gpu":   resource.MustParse("13"),
						corev1.ResourcePods: resource.MustParse("23"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r1-x3").
					Label(tasBlockLabel, "b1").
					Label(tasRackLabel, "r1").
					Label(corev1.LabelHostname, "x3").
					StatusAllocatable(corev1.ResourceList{
						"example.com/gpu":   resource.MustParse("10"),
						corev1.ResourcePods: resource.MustParse("23"),
					}).
					Ready().
					Obj(),
			},
			levels: defaultThreeLevels,
			podSets: []PodSetTestCase{{
				topologyRequest: &kueue.PodSetTopologyRequest{
					Preferred: ptr.To(tasRackLabel),
				},
				requests: resources.Requests{
					"example.com/gpu": 1,
				},
				count: 23,
				wantAssignment: &tas.TopologyAssignment{
					Levels: defaultOneLevel,
					Domains: []tas.TopologyDomainAssignment{
						{
							Count:  12,
							Values: []string{"x1"},
						},
						{
							Count:  11,
							Values: []string{"x2"},
						},
					},
				},
			}},
		},
		//        b1
		//         |
		//        r1
		//    /    |    \
		// x1:20, x2:15, x3:10
		// request: 25
		// sliceSize: 5
		// expected outcome: x2:15, x3:10
		"balanced placement; select optmial set of domains; with slices": {
			featureGates: map[featuregate.Feature]bool{features.TASBalancedPlacement: true},
			nodes: []corev1.Node{
				*testingnode.MakeNode("b1-r1-x1").
					Label(tasBlockLabel, "b1").
					Label(tasRackLabel, "r1").
					Label(corev1.LabelHostname, "x1").
					StatusAllocatable(corev1.ResourceList{
						"example.com/gpu":   resource.MustParse("20"),
						corev1.ResourcePods: resource.MustParse("25"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r1-x2").
					Label(tasBlockLabel, "b1").
					Label(tasRackLabel, "r1").
					Label(corev1.LabelHostname, "x2").
					StatusAllocatable(corev1.ResourceList{
						"example.com/gpu":   resource.MustParse("15"),
						corev1.ResourcePods: resource.MustParse("25"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r1-x3").
					Label(tasBlockLabel, "b1").
					Label(tasRackLabel, "r1").
					Label(corev1.LabelHostname, "x3").
					StatusAllocatable(corev1.ResourceList{
						"example.com/gpu":   resource.MustParse("10"),
						corev1.ResourcePods: resource.MustParse("25"),
					}).
					Ready().
					Obj(),
			},
			levels: defaultThreeLevels,
			podSets: []PodSetTestCase{{
				topologyRequest: &kueue.PodSetTopologyRequest{
					Preferred:                   ptr.To(tasRackLabel),
					PodSetSliceSize:             new(int32(5)),
					PodSetSliceRequiredTopology: ptr.To(corev1.LabelHostname),
				},
				requests: resources.Requests{
					"example.com/gpu": 1,
				},
				count: 25,
				wantAssignment: &tas.TopologyAssignment{
					Levels: defaultOneLevel,
					Domains: []tas.TopologyDomainAssignment{
						{
							Count:  15,
							Values: []string{"x2"},
						},
						{
							Count:  10,
							Values: []string{"x3"},
						},
					},
				},
			}},
		},
		//         b1      b2
		//         |       |
		//        r1        r2
		//    /     |      |    \
		// x1:20   x2:10  x3:15   x4:15
		// request: 22
		// expected outcome: x3:11, x4:11
		"balanced placement; select correct block": {
			featureGates: map[featuregate.Feature]bool{features.TASBalancedPlacement: true},
			nodes: []corev1.Node{
				*testingnode.MakeNode("b1-r1-x1").
					Label(tasBlockLabel, "b1").
					Label(tasRackLabel, "r1").
					Label(corev1.LabelHostname, "x1").
					StatusAllocatable(corev1.ResourceList{
						"example.com/gpu":   resource.MustParse("20"),
						corev1.ResourcePods: resource.MustParse("22"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r1-x2").
					Label(tasBlockLabel, "b1").
					Label(tasRackLabel, "r1").
					Label(corev1.LabelHostname, "x2").
					StatusAllocatable(corev1.ResourceList{
						"example.com/gpu":   resource.MustParse("10"),
						corev1.ResourcePods: resource.MustParse("22"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b2-r2-x3").
					Label(tasBlockLabel, "b2").
					Label(tasRackLabel, "r2").
					Label(corev1.LabelHostname, "x3").
					StatusAllocatable(corev1.ResourceList{
						"example.com/gpu":   resource.MustParse("15"),
						corev1.ResourcePods: resource.MustParse("22"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b2-r2-x4").
					Label(tasBlockLabel, "b2").
					Label(tasRackLabel, "r2").
					Label(corev1.LabelHostname, "x4").
					StatusAllocatable(corev1.ResourceList{
						"example.com/gpu":   resource.MustParse("15"),
						corev1.ResourcePods: resource.MustParse("22"),
					}).
					Ready().
					Obj(),
			},
			levels: defaultThreeLevels,
			podSets: []PodSetTestCase{{
				topologyRequest: &kueue.PodSetTopologyRequest{
					Preferred:       ptr.To(tasRackLabel),
					PodSetSliceSize: new(int32(1)),
				},
				requests: resources.Requests{
					"example.com/gpu": 1,
				},
				count: 22,
				wantAssignment: &tas.TopologyAssignment{
					Levels: defaultOneLevel,
					Domains: []tas.TopologyDomainAssignment{
						{
							Count:  11,
							Values: []string{"x3"},
						},
						{
							Count:  11,
							Values: []string{"x4"},
						},
					},
				},
			}},
		},
		//         b1      b2
		//         |       |
		//        r1        r2
		//    /     |      |    \
		// x1:14   x2:11  x3:15   x4:10
		// request: 25
		// expected outcome: x1:14, x2:11
		"balanced placement; cannot chose domains from different blocks": {
			featureGates: map[featuregate.Feature]bool{features.TASBalancedPlacement: true},
			nodes: []corev1.Node{
				*testingnode.MakeNode("b1-r1-x1").
					Label(tasBlockLabel, "b1").
					Label(tasRackLabel, "r1").
					Label(corev1.LabelHostname, "x1").
					StatusAllocatable(corev1.ResourceList{
						"example.com/gpu":   resource.MustParse("14"),
						corev1.ResourcePods: resource.MustParse("25"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r1-x2").
					Label(tasBlockLabel, "b1").
					Label(tasRackLabel, "r1").
					Label(corev1.LabelHostname, "x2").
					StatusAllocatable(corev1.ResourceList{
						"example.com/gpu":   resource.MustParse("11"),
						corev1.ResourcePods: resource.MustParse("25"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b2-r2-x3").
					Label(tasBlockLabel, "b2").
					Label(tasRackLabel, "r2").
					Label(corev1.LabelHostname, "x3").
					StatusAllocatable(corev1.ResourceList{
						"example.com/gpu":   resource.MustParse("15"),
						corev1.ResourcePods: resource.MustParse("25"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b2-r2-x4").
					Label(tasBlockLabel, "b2").
					Label(tasRackLabel, "r2").
					Label(corev1.LabelHostname, "x4").
					StatusAllocatable(corev1.ResourceList{
						"example.com/gpu":   resource.MustParse("10"),
						corev1.ResourcePods: resource.MustParse("25"),
					}).
					Ready().
					Obj(),
			},
			levels: defaultThreeLevels,
			podSets: []PodSetTestCase{{
				topologyRequest: &kueue.PodSetTopologyRequest{
					Preferred:       ptr.To(tasRackLabel),
					PodSetSliceSize: new(int32(1)),
				},
				requests: resources.Requests{
					"example.com/gpu": 1,
				},
				count: 25,
				wantAssignment: &tas.TopologyAssignment{
					Levels: defaultOneLevel,
					Domains: []tas.TopologyDomainAssignment{
						{
							Count:  14,
							Values: []string{"x1"},
						},
						{
							Count:  11,
							Values: []string{"x2"},
						},
					},
				},
			}},
		},
		//          b1     b2
		//        / |      |
		//     r1   r2     r3
		//    /     |      |    \
		// x1:15   x2:15  x3:15   x4:15
		// request: 25
		// sizeSize: 5
		// expected outcome: x3:15, x4:10
		"balanced placement; select correct block; prefer blocks with single rack; with slices": {
			featureGates: map[featuregate.Feature]bool{features.TASBalancedPlacement: true},
			nodes: []corev1.Node{
				*testingnode.MakeNode("b1-r1-x1").
					Label(tasBlockLabel, "b1").
					Label(tasRackLabel, "r1").
					Label(corev1.LabelHostname, "x1").
					StatusAllocatable(corev1.ResourceList{
						"example.com/gpu":   resource.MustParse("15"),
						corev1.ResourcePods: resource.MustParse("25"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r2-x2").
					Label(tasBlockLabel, "b1").
					Label(tasRackLabel, "r2").
					Label(corev1.LabelHostname, "x2").
					StatusAllocatable(corev1.ResourceList{
						"example.com/gpu":   resource.MustParse("15"),
						corev1.ResourcePods: resource.MustParse("25"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r3-x3").
					Label(tasBlockLabel, "b1").
					Label(tasRackLabel, "r3").
					Label(corev1.LabelHostname, "x3").
					StatusAllocatable(corev1.ResourceList{
						"example.com/gpu":   resource.MustParse("15"),
						corev1.ResourcePods: resource.MustParse("25"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r3-x4").
					Label(tasBlockLabel, "b1").
					Label(tasRackLabel, "r3").
					Label(corev1.LabelHostname, "x4").
					StatusAllocatable(corev1.ResourceList{
						"example.com/gpu":   resource.MustParse("15"),
						corev1.ResourcePods: resource.MustParse("25"),
					}).
					Ready().
					Obj(),
			},
			levels: defaultThreeLevels,
			podSets: []PodSetTestCase{{
				topologyRequest: &kueue.PodSetTopologyRequest{
					Preferred:                   ptr.To(tasRackLabel),
					PodSetSliceSize:             new(int32(5)),
					PodSetSliceRequiredTopology: ptr.To(corev1.LabelHostname),
				},
				requests: resources.Requests{
					"example.com/gpu": 1,
				},
				count: 25,
				wantAssignment: &tas.TopologyAssignment{
					Levels: defaultOneLevel,
					Domains: []tas.TopologyDomainAssignment{
						{
							Count:  15,
							Values: []string{"x3"},
						},
						{
							Count:  10,
							Values: []string{"x4"},
						},
					},
				},
			}},
		},
		//           b1
		//         /     \
		//        sb1     sb2
		//      /   |      |   \
		//     r1   r2     r3   r4
		//    /     |      |     |     \
		// x1:18   x2:8    x3:15 x4:9  x5:7
		// request: 20
		// sliceSize: 2
		// expected outcome: x3:10, x4:8, x5:2
		"balanced placement; four level topology; slice two levels below balanced level": {
			featureGates: map[featuregate.Feature]bool{features.TASBalancedPlacement: true},
			nodes: []corev1.Node{
				*testingnode.MakeNode("b1-sb1-r1-x1").
					Label(tasBlockLabel, "b1").
					Label(tasSubBlockLabel, "sb1").
					Label(tasRackLabel, "r1").
					Label(corev1.LabelHostname, "x1").
					StatusAllocatable(corev1.ResourceList{
						"example.com/gpu":   resource.MustParse("18"),
						corev1.ResourcePods: resource.MustParse("20"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-sb1-r2-x2").
					Label(tasBlockLabel, "b1").
					Label(tasSubBlockLabel, "sb1").
					Label(tasRackLabel, "r2").
					Label(corev1.LabelHostname, "x2").
					StatusAllocatable(corev1.ResourceList{
						"example.com/gpu":   resource.MustParse("8"),
						corev1.ResourcePods: resource.MustParse("20"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-sb1-r3-x3").
					Label(tasBlockLabel, "b1").
					Label(tasSubBlockLabel, "sb2").
					Label(tasRackLabel, "r3").
					Label(corev1.LabelHostname, "x3").
					StatusAllocatable(corev1.ResourceList{
						"example.com/gpu":   resource.MustParse("15"),
						corev1.ResourcePods: resource.MustParse("20"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-sb1-r3-x4").
					Label(tasBlockLabel, "b1").
					Label(tasSubBlockLabel, "sb2").
					Label(tasRackLabel, "r4").
					Label(corev1.LabelHostname, "x4").
					StatusAllocatable(corev1.ResourceList{
						"example.com/gpu":   resource.MustParse("9"),
						corev1.ResourcePods: resource.MustParse("20"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-sb1-r3-x5").
					Label(tasBlockLabel, "b1").
					Label(tasSubBlockLabel, "sb2").
					Label(tasRackLabel, "r4").
					Label(corev1.LabelHostname, "x5").
					StatusAllocatable(corev1.ResourceList{
						"example.com/gpu":   resource.MustParse("7"),
						corev1.ResourcePods: resource.MustParse("20"),
					}).
					Ready().
					Obj(),
			},
			levels: []string{tasBlockLabel, tasSubBlockLabel, tasRackLabel, corev1.LabelHostname},
			podSets: []PodSetTestCase{{
				topologyRequest: &kueue.PodSetTopologyRequest{
					Preferred:                   ptr.To(string(tasSubBlockLabel)),
					PodSetSliceSize:             new(int32(2)),
					PodSetSliceRequiredTopology: ptr.To(corev1.LabelHostname),
				},
				requests: resources.Requests{
					"example.com/gpu": 1,
				},
				count: 20,
				wantAssignment: &tas.TopologyAssignment{
					Levels: []string{corev1.LabelHostname},
					Domains: []tas.TopologyDomainAssignment{
						{
							Count:  10,
							Values: []string{"x3"},
						},
						{
							Count:  8,
							Values: []string{"x4"},
						},
						{
							Count:  2,
							Values: []string{"x5"},
						},
					},
				},
			}},
		},
		//           b1
		//         /     \
		//        sb1     sb2
		//      /   |      |   \
		//     r1   r2     r3   r4
		//    /     |      |     |     \
		// x1:18   x2:8    x3:15 x4:9  x5:7
		// request: 20
		// expected outcome: x3:10, x4:3, x5:7
		"balanced placement; four level topology; slice one level below balanced level": {
			featureGates: map[featuregate.Feature]bool{features.TASBalancedPlacement: true},
			nodes: []corev1.Node{
				*testingnode.MakeNode("b1-sb1-r1-x1").
					Label(tasBlockLabel, "b1").
					Label(tasSubBlockLabel, "sb1").
					Label(tasRackLabel, "r1").
					Label(corev1.LabelHostname, "x1").
					StatusAllocatable(corev1.ResourceList{
						"example.com/gpu":   resource.MustParse("18"),
						corev1.ResourcePods: resource.MustParse("20"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-sb1-r2-x2").
					Label(tasBlockLabel, "b1").
					Label(tasSubBlockLabel, "sb1").
					Label(tasRackLabel, "r2").
					Label(corev1.LabelHostname, "x2").
					StatusAllocatable(corev1.ResourceList{
						"example.com/gpu":   resource.MustParse("8"),
						corev1.ResourcePods: resource.MustParse("20"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-sb1-r3-x3").
					Label(tasBlockLabel, "b1").
					Label(tasSubBlockLabel, "sb2").
					Label(tasRackLabel, "r3").
					Label(corev1.LabelHostname, "x3").
					StatusAllocatable(corev1.ResourceList{
						"example.com/gpu":   resource.MustParse("15"),
						corev1.ResourcePods: resource.MustParse("20"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-sb1-r3-x4").
					Label(tasBlockLabel, "b1").
					Label(tasSubBlockLabel, "sb2").
					Label(tasRackLabel, "r4").
					Label(corev1.LabelHostname, "x4").
					StatusAllocatable(corev1.ResourceList{
						"example.com/gpu":   resource.MustParse("9"),
						corev1.ResourcePods: resource.MustParse("20"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-sb1-r3-x5").
					Label(tasBlockLabel, "b1").
					Label(tasSubBlockLabel, "sb2").
					Label(tasRackLabel, "r4").
					Label(corev1.LabelHostname, "x5").
					StatusAllocatable(corev1.ResourceList{
						"example.com/gpu":   resource.MustParse("7"),
						corev1.ResourcePods: resource.MustParse("20"),
					}).
					Ready().
					Obj(),
			},
			levels: []string{tasBlockLabel, tasSubBlockLabel, tasRackLabel, corev1.LabelHostname},
			podSets: []PodSetTestCase{{
				topologyRequest: &kueue.PodSetTopologyRequest{
					Preferred:                   ptr.To(string(tasSubBlockLabel)),
					PodSetSliceSize:             new(int32(2)),
					PodSetSliceRequiredTopology: ptr.To(tasRackLabel),
				},
				requests: resources.Requests{
					"example.com/gpu": 1,
				},
				count: 20,
				wantAssignment: &tas.TopologyAssignment{
					Levels: []string{corev1.LabelHostname},
					Domains: []tas.TopologyDomainAssignment{
						{
							Count:  10,
							Values: []string{"x3"},
						},
						{
							Count:  3,
							Values: []string{"x4"},
						},
						{
							Count:  7,
							Values: []string{"x5"},
						},
					},
				},
			}},
		},
		//           b1
		//         /     \
		//        sb1     sb2
		//      /   |      | \
		//     r1   r2     r3 r4
		//    /     |      |     \
		// x1:18   x2:8    x3:15 x4:15
		// request: 20
		// sliceSize: 2
		// expected outcome: x3:10, x4:10
		"balanced placement; four level topology; balance on rack level; slice not on the lowest level": {
			featureGates: map[featuregate.Feature]bool{features.TASBalancedPlacement: true},
			nodes: []corev1.Node{
				*testingnode.MakeNode("b1-sb1-r1-x1").
					Label(tasBlockLabel, "b1").
					Label(tasSubBlockLabel, "sb1").
					Label(tasRackLabel, "r1").
					Label(corev1.LabelHostname, "x1").
					StatusAllocatable(corev1.ResourceList{
						"example.com/gpu":   resource.MustParse("18"),
						corev1.ResourcePods: resource.MustParse("20"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-sb1-r2-x2").
					Label(tasBlockLabel, "b1").
					Label(tasSubBlockLabel, "sb1").
					Label(tasRackLabel, "r2").
					Label(corev1.LabelHostname, "x2").
					StatusAllocatable(corev1.ResourceList{
						"example.com/gpu":   resource.MustParse("8"),
						corev1.ResourcePods: resource.MustParse("20"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-sb1-r3-x3").
					Label(tasBlockLabel, "b1").
					Label(tasSubBlockLabel, "sb2").
					Label(tasRackLabel, "r3").
					Label(corev1.LabelHostname, "x3").
					StatusAllocatable(corev1.ResourceList{
						"example.com/gpu":   resource.MustParse("15"),
						corev1.ResourcePods: resource.MustParse("20"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-sb1-r3-x4").
					Label(tasBlockLabel, "b1").
					Label(tasSubBlockLabel, "sb2").
					Label(tasRackLabel, "r4").
					Label(corev1.LabelHostname, "x4").
					StatusAllocatable(corev1.ResourceList{
						"example.com/gpu":   resource.MustParse("15"),
						corev1.ResourcePods: resource.MustParse("20"),
					}).
					Ready().
					Obj(),
			},
			levels: []string{tasBlockLabel, tasSubBlockLabel, tasRackLabel},
			podSets: []PodSetTestCase{{
				topologyRequest: &kueue.PodSetTopologyRequest{
					Preferred:                   ptr.To(string(tasSubBlockLabel)),
					PodSetSliceSize:             new(int32(2)),
					PodSetSliceRequiredTopology: ptr.To(tasRackLabel),
				},
				requests: resources.Requests{
					"example.com/gpu": 1,
				},
				count: 20,
				wantAssignment: &tas.TopologyAssignment{
					Levels: []string{tasBlockLabel, tasSubBlockLabel, tasRackLabel},
					Domains: []tas.TopologyDomainAssignment{
						{
							Count:  10,
							Values: []string{"b1", "sb2", "r3"},
						},
						{
							Count:  10,
							Values: []string{"b1", "sb2", "r4"},
						},
					},
				},
			}},
		},
		//        b1
		//         |        \
		//        r1             r2
		//    /    |    \         |     \
		// x1:15, x2:13, x3:10   x4:20   x5:8
		// request: 22
		// sliceSize: 2
		// expected outcome: x2:12, x3:10
		"balanced placement; three level topology; balance on the same level as slice": {
			featureGates: map[featuregate.Feature]bool{features.TASBalancedPlacement: true},
			nodes: []corev1.Node{
				*testingnode.MakeNode("b1-r1-x1").
					Label(tasBlockLabel, "b1").
					Label(tasRackLabel, "r1").
					Label(corev1.LabelHostname, "x1").
					StatusAllocatable(corev1.ResourceList{
						"example.com/gpu":   resource.MustParse("15"),
						corev1.ResourcePods: resource.MustParse("22"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r1-x2").
					Label(tasBlockLabel, "b1").
					Label(tasRackLabel, "r1").
					Label(corev1.LabelHostname, "x2").
					StatusAllocatable(corev1.ResourceList{
						"example.com/gpu":   resource.MustParse("13"),
						corev1.ResourcePods: resource.MustParse("22"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r1-x3").
					Label(tasBlockLabel, "b1").
					Label(tasRackLabel, "r1").
					Label(corev1.LabelHostname, "x3").
					StatusAllocatable(corev1.ResourceList{
						"example.com/gpu":   resource.MustParse("10"),
						corev1.ResourcePods: resource.MustParse("22"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r2-x4").
					Label(tasBlockLabel, "b1").
					Label(tasRackLabel, "r2").
					Label(corev1.LabelHostname, "x4").
					StatusAllocatable(corev1.ResourceList{
						"example.com/gpu":   resource.MustParse("20"),
						corev1.ResourcePods: resource.MustParse("22"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r2-x5").
					Label(tasBlockLabel, "b1").
					Label(tasRackLabel, "r2").
					Label(corev1.LabelHostname, "x5").
					StatusAllocatable(corev1.ResourceList{
						"example.com/gpu":   resource.MustParse("8"),
						corev1.ResourcePods: resource.MustParse("22"),
					}).
					Ready().
					Obj(),
			},
			levels: defaultThreeLevels,
			podSets: []PodSetTestCase{{
				topologyRequest: &kueue.PodSetTopologyRequest{
					Preferred:                   ptr.To(string(corev1.LabelHostname)),
					PodSetSliceSize:             new(int32(2)),
					PodSetSliceRequiredTopology: ptr.To(corev1.LabelHostname),
				},
				requests: resources.Requests{
					"example.com/gpu": 1,
				},
				count: 22,
				wantAssignment: &tas.TopologyAssignment{
					Levels: defaultOneLevel,
					Domains: []tas.TopologyDomainAssignment{
						{
							Count:  12,
							Values: []string{"x2"},
						},
						{
							Count:  10,
							Values: []string{"x3"},
						},
					},
				},
			}},
		},
		//        b1
		//         |        \
		//        r1             r2
		//    /    |        /     |     \
		// x1:20  x2:8  x3:15   x4:13   x5:10
		// request: 22
		// expected outcome: x3:11, x4:11
		"balanced placement; three level topology; balance on the lowest level": {
			featureGates: map[featuregate.Feature]bool{features.TASBalancedPlacement: true},
			nodes: []corev1.Node{
				*testingnode.MakeNode("b1-r1-x1").
					Label(tasBlockLabel, "b1").
					Label(tasRackLabel, "r1").
					Label(corev1.LabelHostname, "x1").
					StatusAllocatable(corev1.ResourceList{
						"example.com/gpu":   resource.MustParse("20"),
						corev1.ResourcePods: resource.MustParse("22"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r1-x2").
					Label(tasBlockLabel, "b1").
					Label(tasRackLabel, "r1").
					Label(corev1.LabelHostname, "x2").
					StatusAllocatable(corev1.ResourceList{
						"example.com/gpu":   resource.MustParse("8"),
						corev1.ResourcePods: resource.MustParse("22"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r2-x3").
					Label(tasBlockLabel, "b1").
					Label(tasRackLabel, "r2").
					Label(corev1.LabelHostname, "x3").
					StatusAllocatable(corev1.ResourceList{
						"example.com/gpu":   resource.MustParse("15"),
						corev1.ResourcePods: resource.MustParse("22"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r2-x4").
					Label(tasBlockLabel, "b1").
					Label(tasRackLabel, "r2").
					Label(corev1.LabelHostname, "x4").
					StatusAllocatable(corev1.ResourceList{
						"example.com/gpu":   resource.MustParse("13"),
						corev1.ResourcePods: resource.MustParse("22"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r2-x5").
					Label(tasBlockLabel, "b1").
					Label(tasRackLabel, "r2").
					Label(corev1.LabelHostname, "x5").
					StatusAllocatable(corev1.ResourceList{
						"example.com/gpu":   resource.MustParse("10"),
						corev1.ResourcePods: resource.MustParse("22"),
					}).
					Ready().
					Obj(),
			},
			levels: defaultThreeLevels,
			podSets: []PodSetTestCase{{
				topologyRequest: &kueue.PodSetTopologyRequest{
					Preferred: ptr.To(string(corev1.LabelHostname)),
				},
				requests: resources.Requests{
					"example.com/gpu": 1,
				},
				count: 22,
				wantAssignment: &tas.TopologyAssignment{
					Levels: defaultOneLevel,
					Domains: []tas.TopologyDomainAssignment{
						{
							Count:  11,
							Values: []string{"x3"},
						},
						{
							Count:  11,
							Values: []string{"x4"},
						},
					},
				},
			}},
		},
		//        b1
		//         |
		//        r1
		//    /    |    \
		// x1:20, x2:15, x3:12
		// request: 25
		// sliceSize: 5
		// leaders: 1
		// expected outcome: x2:15, x3:10 + leader
		"balanced placement; leader worker set": {
			featureGates: map[featuregate.Feature]bool{features.TASBalancedPlacement: true},
			nodes: []corev1.Node{
				*testingnode.MakeNode("b1-r1-x1").
					Label(tasBlockLabel, "b1").
					Label(tasRackLabel, "r1").
					Label(corev1.LabelHostname, "x1").
					StatusAllocatable(corev1.ResourceList{
						"example.com/gpu":   resource.MustParse("20"),
						corev1.ResourcePods: resource.MustParse("24"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r1-x2").
					Label(tasBlockLabel, "b1").
					Label(tasRackLabel, "r1").
					Label(corev1.LabelHostname, "x2").
					StatusAllocatable(corev1.ResourceList{
						"example.com/gpu":   resource.MustParse("15"),
						corev1.ResourcePods: resource.MustParse("24"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r1-x3").
					Label(tasBlockLabel, "b1").
					Label(tasRackLabel, "r1").
					Label(corev1.LabelHostname, "x3").
					StatusAllocatable(corev1.ResourceList{
						"example.com/gpu":   resource.MustParse("12"),
						corev1.ResourcePods: resource.MustParse("24"),
					}).
					Ready().
					Obj(),
			},
			levels: defaultThreeLevels,
			podSets: []PodSetTestCase{
				{
					podSetName: "leader",
					topologyRequest: &kueue.PodSetTopologyRequest{
						Preferred:                   ptr.To(string(tasRackLabel)),
						PodSetSliceRequiredTopology: ptr.To(corev1.LabelHostname),
					},
					requests: resources.Requests{
						"example.com/gpu": 1,
					},
					podSetGroupName: new("sameGroup"),
					count:           1,
					wantAssignment: &tas.TopologyAssignment{
						Levels: defaultOneLevel,
						Domains: []tas.TopologyDomainAssignment{
							{
								Count:  1,
								Values: []string{"x3"},
							},
						},
					},
				},
				{
					podSetName: "workers",
					topologyRequest: &kueue.PodSetTopologyRequest{
						Preferred:                   ptr.To(string(tasRackLabel)),
						PodSetSliceSize:             new(int32(5)),
						PodSetSliceRequiredTopology: ptr.To(corev1.LabelHostname),
					},
					requests: resources.Requests{
						"example.com/gpu": 1,
					},
					podSetGroupName: new("sameGroup"),
					count:           25,
					wantAssignment: &tas.TopologyAssignment{
						Levels: defaultOneLevel,
						Domains: []tas.TopologyDomainAssignment{
							{
								Count:  15,
								Values: []string{"x2"},
							},
							{
								Count:  10,
								Values: []string{"x3"},
							},
						},
					},
				},
			},
		},
		//                  b1
		//              /        \
		//            r1         r2
		//        /   |        /  |   \
		//     x1:10 x2:5  x3:5  x4:5  x5:5
		// request: 15
		// expected outcome: x3:5, x4:5, x5:5
		"balanced placement; prioritize (using entropy) balanced domains; second rack wins": {
			featureGates: map[featuregate.Feature]bool{features.TASBalancedPlacement: true},
			nodes: []corev1.Node{
				*testingnode.MakeNode("b1-r1-x1").
					Label(tasBlockLabel, "b1").
					Label(tasRackLabel, "r1").
					Label(corev1.LabelHostname, "x1").
					StatusAllocatable(corev1.ResourceList{
						"example.com/gpu":   resource.MustParse("10"),
						corev1.ResourcePods: resource.MustParse("20"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r1-x2").
					Label(tasBlockLabel, "b1").
					Label(tasRackLabel, "r1").
					Label(corev1.LabelHostname, "x2").
					StatusAllocatable(corev1.ResourceList{
						"example.com/gpu":   resource.MustParse("5"),
						corev1.ResourcePods: resource.MustParse("20"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r2-x3").
					Label(tasBlockLabel, "b1").
					Label(tasRackLabel, "r2").
					Label(corev1.LabelHostname, "x3").
					StatusAllocatable(corev1.ResourceList{
						"example.com/gpu":   resource.MustParse("5"),
						corev1.ResourcePods: resource.MustParse("20"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r2-x4").
					Label(tasBlockLabel, "b1").
					Label(tasRackLabel, "r2").
					Label(corev1.LabelHostname, "x4").
					StatusAllocatable(corev1.ResourceList{
						"example.com/gpu":   resource.MustParse("5"),
						corev1.ResourcePods: resource.MustParse("20"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r2-x5").
					Label(tasBlockLabel, "b1").
					Label(tasRackLabel, "r2").
					Label(corev1.LabelHostname, "x5").
					StatusAllocatable(corev1.ResourceList{
						"example.com/gpu":   resource.MustParse("5"),
						corev1.ResourcePods: resource.MustParse("20"),
					}).
					Ready().
					Obj(),
			},
			levels: defaultThreeLevels,
			podSets: []PodSetTestCase{{
				topologyRequest: &kueue.PodSetTopologyRequest{
					Preferred: ptr.To(string(tasRackLabel)),
				},
				requests: resources.Requests{
					"example.com/gpu": 1,
				},
				count: 15,
				wantAssignment: &tas.TopologyAssignment{
					Levels: defaultOneLevel,
					Domains: []tas.TopologyDomainAssignment{
						{
							Count:  5,
							Values: []string{"x3"},
						},
						{
							Count:  5,
							Values: []string{"x4"},
						},
						{
							Count:  5,
							Values: []string{"x5"},
						},
					},
				},
			}},
		},
		//                  b1
		//              /        \
		//            r1          r2
		//        /   |    \     |   \
		//     x1:8 x2:8  x3:8  x4:16  x5:8
		// request: 24
		// expected outcome: x1:8, x2:8, x3:8
		"balanced placement; prioritize (using entropy) balanced domains; first rack wins": {
			featureGates: map[featuregate.Feature]bool{features.TASBalancedPlacement: true},
			nodes: []corev1.Node{
				*testingnode.MakeNode("b1-r1-x1").
					Label(tasBlockLabel, "b1").
					Label(tasRackLabel, "r1").
					Label(corev1.LabelHostname, "x1").
					StatusAllocatable(corev1.ResourceList{
						"example.com/gpu":   resource.MustParse("8"),
						corev1.ResourcePods: resource.MustParse("24"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r1-x2").
					Label(tasBlockLabel, "b1").
					Label(tasRackLabel, "r1").
					Label(corev1.LabelHostname, "x2").
					StatusAllocatable(corev1.ResourceList{
						"example.com/gpu":   resource.MustParse("8"),
						corev1.ResourcePods: resource.MustParse("24"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r1-x3").
					Label(tasBlockLabel, "b1").
					Label(tasRackLabel, "r1").
					Label(corev1.LabelHostname, "x3").
					StatusAllocatable(corev1.ResourceList{
						"example.com/gpu":   resource.MustParse("8"),
						corev1.ResourcePods: resource.MustParse("24"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r2-x4").
					Label(tasBlockLabel, "b1").
					Label(tasRackLabel, "r2").
					Label(corev1.LabelHostname, "x4").
					StatusAllocatable(corev1.ResourceList{
						"example.com/gpu":   resource.MustParse("16"),
						corev1.ResourcePods: resource.MustParse("24"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r2-x5").
					Label(tasBlockLabel, "b1").
					Label(tasRackLabel, "r2").
					Label(corev1.LabelHostname, "x5").
					StatusAllocatable(corev1.ResourceList{
						"example.com/gpu":   resource.MustParse("8"),
						corev1.ResourcePods: resource.MustParse("24"),
					}).
					Ready().
					Obj(),
			},
			levels: defaultThreeLevels,
			podSets: []PodSetTestCase{{
				topologyRequest: &kueue.PodSetTopologyRequest{
					Preferred: ptr.To(string(tasRackLabel)),
				},
				requests: resources.Requests{
					"example.com/gpu": 1,
				},
				count: 24,
				wantAssignment: &tas.TopologyAssignment{
					Levels: defaultOneLevel,
					Domains: []tas.TopologyDomainAssignment{
						{
							Count:  8,
							Values: []string{"x1"},
						},
						{
							Count:  8,
							Values: []string{"x2"},
						},
						{
							Count:  8,
							Values: []string{"x3"},
						},
					},
				},
			}},
		},
		//        r1             r2
		//    /    |        /     |     \
		// x1:21  x2:9  x3:11   x4:10   x5:10
		// request: 23
		// expected outcome: x1:12, x3:11
		"balanced placement; two level topology; balance on the highest level": {
			featureGates: map[featuregate.Feature]bool{features.TASBalancedPlacement: true},
			nodes: []corev1.Node{
				*testingnode.MakeNode("r1-x1").
					Label(tasRackLabel, "r1").
					Label(corev1.LabelHostname, "x1").
					StatusAllocatable(corev1.ResourceList{
						"example.com/gpu":   resource.MustParse("21"),
						corev1.ResourcePods: resource.MustParse("22"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("r1-x2").
					Label(tasRackLabel, "r1").
					Label(corev1.LabelHostname, "x2").
					StatusAllocatable(corev1.ResourceList{
						"example.com/gpu":   resource.MustParse("9"),
						corev1.ResourcePods: resource.MustParse("22"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("r2-x3").
					Label(tasRackLabel, "r2").
					Label(corev1.LabelHostname, "x3").
					StatusAllocatable(corev1.ResourceList{
						"example.com/gpu":   resource.MustParse("11"),
						corev1.ResourcePods: resource.MustParse("22"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("r2-x4").
					Label(tasRackLabel, "r2").
					Label(corev1.LabelHostname, "x4").
					StatusAllocatable(corev1.ResourceList{
						"example.com/gpu":   resource.MustParse("10"),
						corev1.ResourcePods: resource.MustParse("22"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("r2-x5").
					Label(tasRackLabel, "r2").
					Label(corev1.LabelHostname, "x5").
					StatusAllocatable(corev1.ResourceList{
						"example.com/gpu":   resource.MustParse("10"),
						corev1.ResourcePods: resource.MustParse("22"),
					}).
					Ready().
					Obj(),
			},
			levels: []string{tasRackLabel, corev1.LabelHostname},
			podSets: []PodSetTestCase{{
				topologyRequest: &kueue.PodSetTopologyRequest{
					Preferred: ptr.To(string(tasRackLabel)),
				},
				requests: resources.Requests{
					"example.com/gpu": 1,
				},
				count: 23,
				wantAssignment: &tas.TopologyAssignment{
					Levels: defaultOneLevel,
					Domains: []tas.TopologyDomainAssignment{
						{
							Count:  12,
							Values: []string{"x1"},
						},
						{
							Count:  11,
							Values: []string{"x3"},
						},
					},
				},
			}},
		},
		//          b1                                b2
		//        /      \                      /        |    \
		//     r1         r2                r3           r4    r5
		//    /  \       /   \        /   |    |    \     |    |
		// x1:5  x2:5  x3:5   x4:5  x5:10 x6:4 x7:4 x8:4  x9:5 x10:5
		// request: 20
		// expected outcome: x1:5, x2:5, x3:5, x4:5
		"balanced placement; select correct block taking into account pruning": {
			featureGates: map[featuregate.Feature]bool{features.TASBalancedPlacement: true},
			nodes: []corev1.Node{
				*testingnode.MakeNode("b1-r1-x1").
					Label(tasBlockLabel, "b1").
					Label(tasRackLabel, "r1").
					Label(corev1.LabelHostname, "x1").
					StatusAllocatable(corev1.ResourceList{
						"example.com/gpu":   resource.MustParse("5"),
						corev1.ResourcePods: resource.MustParse("20"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r1-x2").
					Label(tasBlockLabel, "b1").
					Label(tasRackLabel, "r1").
					Label(corev1.LabelHostname, "x2").
					StatusAllocatable(corev1.ResourceList{
						"example.com/gpu":   resource.MustParse("5"),
						corev1.ResourcePods: resource.MustParse("20"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r2-x3").
					Label(tasBlockLabel, "b1").
					Label(tasRackLabel, "r2").
					Label(corev1.LabelHostname, "x3").
					StatusAllocatable(corev1.ResourceList{
						"example.com/gpu":   resource.MustParse("5"),
						corev1.ResourcePods: resource.MustParse("20"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r2-x4").
					Label(tasBlockLabel, "b1").
					Label(tasRackLabel, "r2").
					Label(corev1.LabelHostname, "x4").
					StatusAllocatable(corev1.ResourceList{
						"example.com/gpu":   resource.MustParse("5"),
						corev1.ResourcePods: resource.MustParse("20"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b2-r3-x5").
					Label(tasBlockLabel, "b2").
					Label(tasRackLabel, "r3").
					Label(corev1.LabelHostname, "x5").
					StatusAllocatable(corev1.ResourceList{
						"example.com/gpu":   resource.MustParse("10"),
						corev1.ResourcePods: resource.MustParse("20"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b2-r3-x6").
					Label(tasBlockLabel, "b2").
					Label(tasRackLabel, "r3").
					Label(corev1.LabelHostname, "x6").
					StatusAllocatable(corev1.ResourceList{
						"example.com/gpu":   resource.MustParse("4"),
						corev1.ResourcePods: resource.MustParse("20"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b2-r3-x7").
					Label(tasBlockLabel, "b2").
					Label(tasRackLabel, "r3").
					Label(corev1.LabelHostname, "x7").
					StatusAllocatable(corev1.ResourceList{
						"example.com/gpu":   resource.MustParse("4"),
						corev1.ResourcePods: resource.MustParse("20"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b2-r3-x8").
					Label(tasBlockLabel, "b2").
					Label(tasRackLabel, "r3").
					Label(corev1.LabelHostname, "x8").
					StatusAllocatable(corev1.ResourceList{
						"example.com/gpu":   resource.MustParse("4"),
						corev1.ResourcePods: resource.MustParse("20"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b2-r4-x9").
					Label(tasBlockLabel, "b2").
					Label(tasRackLabel, "r4").
					Label(corev1.LabelHostname, "x9").
					StatusAllocatable(corev1.ResourceList{
						"example.com/gpu":   resource.MustParse("5"),
						corev1.ResourcePods: resource.MustParse("20"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b2-r5-x10").
					Label(tasBlockLabel, "b2").
					Label(tasRackLabel, "r5").
					Label(corev1.LabelHostname, "x10").
					StatusAllocatable(corev1.ResourceList{
						"example.com/gpu":   resource.MustParse("5"),
						corev1.ResourcePods: resource.MustParse("20"),
					}).
					Ready().
					Obj(),
			},
			levels: defaultThreeLevels,
			podSets: []PodSetTestCase{{
				topologyRequest: &kueue.PodSetTopologyRequest{
					Preferred: ptr.To(tasRackLabel),
				},
				requests: resources.Requests{
					"example.com/gpu": 1,
				},
				count: 20,
				wantAssignment: &tas.TopologyAssignment{
					Levels: defaultOneLevel,
					Domains: []tas.TopologyDomainAssignment{
						{
							Count:  5,
							Values: []string{"x1"},
						},
						{
							Count:  5,
							Values: []string{"x2"},
						},
						{
							Count:  5,
							Values: []string{"x3"},
						},
						{
							Count:  5,
							Values: []string{"x4"},
						},
					},
				},
			}},
		},
		//        b1
		//         |
		//        r1
		//    /    |
		// x1:6  x2:5
		// request: 10
		// leaders: 1
		// expected outcome: x1:5 + leader, x2:5
		"balanced placement; should not prune domain": {
			featureGates: map[featuregate.Feature]bool{features.TASBalancedPlacement: true},
			nodes: []corev1.Node{
				*testingnode.MakeNode("b1-r1-x1").
					Label(tasBlockLabel, "b1").
					Label(tasRackLabel, "r1").
					Label(corev1.LabelHostname, "x1").
					StatusAllocatable(corev1.ResourceList{
						"example.com/gpu":   resource.MustParse("6"),
						corev1.ResourcePods: resource.MustParse("24"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r1-x2").
					Label(tasBlockLabel, "b1").
					Label(tasRackLabel, "r1").
					Label(corev1.LabelHostname, "x2").
					StatusAllocatable(corev1.ResourceList{
						"example.com/gpu":   resource.MustParse("5"),
						corev1.ResourcePods: resource.MustParse("24"),
					}).
					Ready().
					Obj(),
			},
			levels: defaultThreeLevels,
			podSets: []PodSetTestCase{
				{
					podSetName: "leader",
					topologyRequest: &kueue.PodSetTopologyRequest{
						Preferred:                   ptr.To(string(tasRackLabel)),
						PodSetSliceRequiredTopology: ptr.To(corev1.LabelHostname),
					},
					requests: resources.Requests{
						"example.com/gpu": 1,
					},
					podSetGroupName: new("sameGroup"),
					count:           1,
					wantAssignment: &tas.TopologyAssignment{
						Levels: defaultOneLevel,
						Domains: []tas.TopologyDomainAssignment{
							{
								Count:  1,
								Values: []string{"x1"},
							},
						},
					},
				},
				{
					podSetName: "workers",
					topologyRequest: &kueue.PodSetTopologyRequest{
						Preferred: ptr.To(string(tasRackLabel)),
					},
					requests: resources.Requests{
						"example.com/gpu": 1,
					},
					podSetGroupName: new("sameGroup"),
					count:           10,
					wantAssignment: &tas.TopologyAssignment{
						Levels: defaultOneLevel,
						Domains: []tas.TopologyDomainAssignment{
							{
								Count:  5,
								Values: []string{"x1"},
							},
							{
								Count:  5,
								Values: []string{"x2"},
							},
						},
					},
				},
			},
		},
		//        b1
		//         |
		//        r1
		//    /    |
		// x1:7  x2:5
		// request: 10
		// leaders: 1
		// expected outcome: x1:5 + leader, x2:5
		"balanced placement; leader worker set does not reserve leader space on every node": {
			featureGates: map[featuregate.Feature]bool{features.TASBalancedPlacement: true},
			nodes: []corev1.Node{
				*testingnode.MakeNode("b1-r1-x1").
					Label(tasBlockLabel, "b1").
					Label(tasRackLabel, "r1").
					Label(corev1.LabelHostname, "x1").
					StatusAllocatable(corev1.ResourceList{
						"example.com/gpu":   resource.MustParse("7"),
						corev1.ResourcePods: resource.MustParse("12"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r1-x2").
					Label(tasBlockLabel, "b1").
					Label(tasRackLabel, "r1").
					Label(corev1.LabelHostname, "x2").
					StatusAllocatable(corev1.ResourceList{
						"example.com/gpu":   resource.MustParse("5"),
						corev1.ResourcePods: resource.MustParse("12"),
					}).
					Ready().
					Obj(),
			},
			levels: defaultThreeLevels,
			podSets: []PodSetTestCase{
				{
					podSetName: "leader",
					topologyRequest: &kueue.PodSetTopologyRequest{
						Preferred:                   ptr.To(string(tasRackLabel)),
						PodSetSliceRequiredTopology: ptr.To(corev1.LabelHostname),
					},
					requests: resources.Requests{
						"example.com/gpu": 1,
					},
					podSetGroupName: new("sameGroup"),
					count:           1,
					wantAssignment: &tas.TopologyAssignment{
						Levels: defaultOneLevel,
						Domains: []tas.TopologyDomainAssignment{
							{
								Count:  1,
								Values: []string{"x1"},
							},
						},
					},
				},
				{
					podSetName: "workers",
					topologyRequest: &kueue.PodSetTopologyRequest{
						Preferred: ptr.To(string(tasRackLabel)),
					},
					requests: resources.Requests{
						"example.com/gpu": 1,
					},
					podSetGroupName: new("sameGroup"),
					count:           10,
					wantAssignment: &tas.TopologyAssignment{
						Levels: defaultOneLevel,
						Domains: []tas.TopologyDomainAssignment{
							{
								Count:  5,
								Values: []string{"x1"},
							},
							{
								Count:  5,
								Values: []string{"x2"},
							},
						},
					},
				},
			},
		},
		"block required for podset; rack required for slices; podset fits in a block, but slices do not fit in racks": {

			//         b1
			//     /    |    \
			//   r1    r2    r3
			//   |      |     |
			//   x3:2  x5:2  x1:2
			nodes: []corev1.Node{
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
				*testingnode.MakeNode("b1-r2-x5").
					Label(tasBlockLabel, "b1").
					Label(tasRackLabel, "r2").
					Label(corev1.LabelHostname, "x5").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("2"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r3-x1").
					Label(tasBlockLabel, "b1").
					Label(tasRackLabel, "r3").
					Label(corev1.LabelHostname, "x1").
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
					PodSetSliceSize:             new(int32(3)),
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
			//   x3:3  x5:1  x1:1  x6:1
			nodes: []corev1.Node{
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
				*testingnode.MakeNode("b1-r3-x1").
					Label(tasBlockLabel, "b1").
					Label(tasRackLabel, "r3").
					Label(corev1.LabelHostname, "x1").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("1"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r4-x6").
					Label(tasBlockLabel, "b1").
					Label(tasRackLabel, "r4").
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
					Required:                    ptr.To(tasBlockLabel),
					PodSetSliceRequiredTopology: ptr.To(tasRackLabel),
					PodSetSliceSize:             new(int32(3)),
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
			//   x3:2  x5:2  x1:2  x6:3  x2:3
			nodes: []corev1.Node{
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
				*testingnode.MakeNode("b1-r2-x5").
					Label(tasBlockLabel, "b1").
					Label(tasRackLabel, "r2").
					Label(corev1.LabelHostname, "x5").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("2"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r3-x1").
					Label(tasBlockLabel, "b1").
					Label(tasRackLabel, "r3").
					Label(corev1.LabelHostname, "x1").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("2"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b2-r4-x6").
					Label(tasBlockLabel, "b2").
					Label(tasRackLabel, "r4").
					Label(corev1.LabelHostname, "x6").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("3"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b2-r5-x2").
					Label(tasBlockLabel, "b2").
					Label(tasRackLabel, "r5").
					Label(corev1.LabelHostname, "x2").
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
					PodSetSliceSize:             new(int32(3)),
				},
				requests: resources.Requests{
					corev1.ResourceCPU: 1000,
				},
				count: 6,
				wantAssignment: &tas.TopologyAssignment{
					Levels: defaultOneLevel,
					Domains: []tas.TopologyDomainAssignment{
						{
							Count: 3,
							Values: []string{
								"x6",
							},
						},
						{
							Count: 3,
							Values: []string{
								"x2",
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
					PodSetSliceSize:             new(int32(1)),
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
					Required:                    ptr.To(tasBlockLabel),
					PodSetSliceRequiredTopology: new("not-existing-topology-level"),
					PodSetSliceSize:             new(int32(1)),
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
			// x3:3, x5:3, x1:3
			//
			nodes: []corev1.Node{
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
				*testingnode.MakeNode("b1-r1-x5").
					Label(tasBlockLabel, "b1").
					Label(tasRackLabel, "r1").
					Label(corev1.LabelHostname, "x5").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("3"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
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
			},
			levels: defaultThreeLevels,
			podSets: []PodSetTestCase{{
				topologyRequest: &kueue.PodSetTopologyRequest{
					PodSetSliceRequiredTopology: ptr.To(corev1.LabelHostname),
					PodSetSliceSize:             new(int32(2)),
				},
				requests: resources.Requests{
					corev1.ResourceCPU: 1000,
				},
				count: 6,
				wantAssignment: &tas.TopologyAssignment{
					Levels: defaultOneLevel,
					Domains: []tas.TopologyDomainAssignment{
						{
							Count: 2,
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
						{
							Count: 2,
							Values: []string{
								"x5",
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
					PodSetSliceSize:             new(int32(2)),
				},
				requests: resources.Requests{
					corev1.ResourceCPU: 1000,
				},
				count: 6,
				wantAssignment: &tas.TopologyAssignment{
					Levels: defaultOneLevel,
					Domains: []tas.TopologyDomainAssignment{
						{
							Count: 4,
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
		"no topology for podset; rack required for slices; multiple blocks; BestFit": {
			nodes:  defaultNodes,
			levels: defaultThreeLevels,
			podSets: []PodSetTestCase{{
				topologyRequest: &kueue.PodSetTopologyRequest{
					PodSetSliceRequiredTopology: ptr.To(tasRackLabel),
					PodSetSliceSize:             new(int32(2)),
				},
				requests: resources.Requests{
					corev1.ResourceCPU: 1000,
				},
				count: 4,
				wantAssignment: &tas.TopologyAssignment{
					Levels: defaultOneLevel,
					Domains: []tas.TopologyDomainAssignment{
						{
							Count: 1,
							Values: []string{
								"x1",
							},
						},
						{
							Count: 1,
							Values: []string{
								"x5",
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
					wantAssignment: &tas.TopologyAssignment{
						Levels: []string{tasBlockLabel},
						Domains: []tas.TopologyDomainAssignment{
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
					wantAssignment: &tas.TopologyAssignment{
						Levels: []string{tasBlockLabel},
						Domains: []tas.TopologyDomainAssignment{
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
					podSetGroupName: new("sameGroup"),
					count:           1,
					wantAssignment: &tas.TopologyAssignment{
						Levels: []string{tasBlockLabel},
						Domains: []tas.TopologyDomainAssignment{
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
					podSetGroupName: new("sameGroup"),
					count:           4,
					wantAssignment: &tas.TopologyAssignment{
						Levels: []string{tasBlockLabel},
						Domains: []tas.TopologyDomainAssignment{
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
					podSetGroupName: new("sameGroup"),
					count:           1,
					wantAssignment: &tas.TopologyAssignment{
						Levels: []string{tasBlockLabel},
						Domains: []tas.TopologyDomainAssignment{
							{
								Count:  1,
								Values: []string{"b2"},
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
					podSetGroupName: new("sameGroup"),
					count:           4,
					wantAssignment: &tas.TopologyAssignment{
						Levels: []string{tasBlockLabel},
						Domains: []tas.TopologyDomainAssignment{
							{
								Count:  4,
								Values: []string{"b2"},
							},
						},
					},
				},
			},
		},
		"find topology assignment for grouped podsets skips domain where only workers fit without leader": {
			nodes: []corev1.Node{
				*testingnode.MakeNode("small-used").
					Label(tasBlockLabel, "b1").
					Label(tasRackLabel, "small").
					Label(corev1.LabelHostname, "small-used").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("2800m"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("small-free").
					Label(tasBlockLabel, "b1").
					Label(tasRackLabel, "small").
					Label(corev1.LabelHostname, "small-free").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("2800m"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("large-free").
					Label(tasBlockLabel, "b1").
					Label(tasRackLabel, "large").
					Label(corev1.LabelHostname, "large-free").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("6"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
			},
			pods: []corev1.Pod{
				*testingpod.MakePod("filler", "test-ns").
					NodeName("small-used").
					StatusPhase(corev1.PodRunning).
					Request(corev1.ResourceCPU, "2500m").
					Obj(),
			},
			levels: defaultThreeLevels,
			podSets: []PodSetTestCase{
				{
					podSetName: "leader",
					topologyRequest: &kueue.PodSetTopologyRequest{
						Required: ptr.To(tasRackLabel),
					},
					requests: resources.Requests{
						corev1.ResourceCPU: 2500,
					},
					podSetGroupName: new("sameGroup"),
					count:           1,
					wantAssignment: &tas.TopologyAssignment{
						Levels: defaultOneLevel,
						Domains: []tas.TopologyDomainAssignment{
							{Count: 1, Values: []string{"large-free"}},
						},
					},
				},
				{
					podSetName: "workers",
					topologyRequest: &kueue.PodSetTopologyRequest{
						Required: ptr.To(tasRackLabel),
					},
					requests: resources.Requests{
						corev1.ResourceCPU: 2500,
					},
					podSetGroupName: new("sameGroup"),
					count:           1,
					wantAssignment: &tas.TopologyAssignment{
						Levels: defaultOneLevel,
						Domains: []tas.TopologyDomainAssignment{
							{Count: 1, Values: []string{"large-free"}},
						},
					},
				},
			},
		},
		"find topology assignment for grouped podsets skips domain where mixed-size workers only fit without leader": {
			nodes: []corev1.Node{
				*testingnode.MakeNode("small-used").
					Label(tasBlockLabel, "b1").
					Label(tasRackLabel, "small").
					Label(corev1.LabelHostname, "small-used").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("2800m"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("small-free").
					Label(tasBlockLabel, "b1").
					Label(tasRackLabel, "small").
					Label(corev1.LabelHostname, "small-free").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("2800m"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("large-free").
					Label(tasBlockLabel, "b1").
					Label(tasRackLabel, "large").
					Label(corev1.LabelHostname, "large-free").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("6"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
			},
			pods: []corev1.Pod{
				*testingpod.MakePod("filler", "test-ns").
					NodeName("small-used").
					StatusPhase(corev1.PodRunning).
					Request(corev1.ResourceCPU, "2500m").
					Obj(),
			},
			levels: defaultThreeLevels,
			podSets: []PodSetTestCase{
				{
					podSetName: "leader",
					topologyRequest: &kueue.PodSetTopologyRequest{
						Required: ptr.To(tasRackLabel),
					},
					requests: resources.Requests{
						corev1.ResourceCPU: 2500,
					},
					podSetGroupName: new("sameGroup"),
					count:           1,
					wantAssignment: &tas.TopologyAssignment{
						Levels: defaultOneLevel,
						Domains: []tas.TopologyDomainAssignment{
							{Count: 1, Values: []string{"large-free"}},
						},
					},
				},
				{
					podSetName: "workers",
					topologyRequest: &kueue.PodSetTopologyRequest{
						Required: ptr.To(tasRackLabel),
					},
					requests: resources.Requests{
						corev1.ResourceCPU: 500,
					},
					podSetGroupName: new("sameGroup"),
					count:           2,
					wantAssignment: &tas.TopologyAssignment{
						Levels: defaultOneLevel,
						Domains: []tas.TopologyDomainAssignment{
							{Count: 2, Values: []string{"large-free"}},
						},
					},
				},
			},
		},
		"find topology assignment for grouped podsets keeps tight domain when leader and workers fit together": {
			nodes: []corev1.Node{
				*testingnode.MakeNode("small-used").
					Label(tasBlockLabel, "b1").
					Label(tasRackLabel, "small").
					Label(corev1.LabelHostname, "small-used").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("2800m"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("small-free").
					Label(tasBlockLabel, "b1").
					Label(tasRackLabel, "small").
					Label(corev1.LabelHostname, "small-free").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("2800m"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("large-free").
					Label(tasBlockLabel, "b1").
					Label(tasRackLabel, "large").
					Label(corev1.LabelHostname, "large-free").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("6"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
			},
			pods: []corev1.Pod{
				*testingpod.MakePod("filler", "test-ns").
					NodeName("small-used").
					StatusPhase(corev1.PodRunning).
					Request(corev1.ResourceCPU, "2500m").
					Obj(),
			},
			levels: defaultThreeLevels,
			podSets: []PodSetTestCase{
				{
					podSetName: "leader",
					topologyRequest: &kueue.PodSetTopologyRequest{
						Required: ptr.To(tasRackLabel),
					},
					requests: resources.Requests{
						corev1.ResourceCPU: 1000,
					},
					podSetGroupName: new("sameGroup"),
					count:           1,
					wantAssignment: &tas.TopologyAssignment{
						Levels: defaultOneLevel,
						Domains: []tas.TopologyDomainAssignment{
							{Count: 1, Values: []string{"small-free"}},
						},
					},
				},
				{
					podSetName: "workers",
					topologyRequest: &kueue.PodSetTopologyRequest{
						Required: ptr.To(tasRackLabel),
					},
					requests: resources.Requests{
						corev1.ResourceCPU: 1000,
					},
					podSetGroupName: new("sameGroup"),
					count:           1,
					wantAssignment: &tas.TopologyAssignment{
						Levels: defaultOneLevel,
						Domains: []tas.TopologyDomainAssignment{
							{Count: 1, Values: []string{"small-free"}},
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
					podSetGroupName: new("sameGroup"),
					count:           1,
					wantAssignment:  nil,
					wantReason:      `topology "default" allows to fit only 4 out of 4 pod(s). Total nodes: 2; excluded: resource "example.com/gpu": 1`,
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
					podSetGroupName: new("sameGroup"),
					count:           4,
					wantAssignment:  nil,
					wantReason:      `topology "default" allows to fit only 4 out of 4 pod(s). Total nodes: 2; excluded: resource "example.com/gpu": 1`,
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
					podSetGroupName: new("sameGroup"),
					count:           1,
					wantAssignment: &tas.TopologyAssignment{
						Levels: []string{tasBlockLabel},
						Domains: []tas.TopologyDomainAssignment{
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
					podSetGroupName: new("sameGroup"),
					count:           4,
					wantAssignment: &tas.TopologyAssignment{
						Levels: []string{tasBlockLabel},
						Domains: []tas.TopologyDomainAssignment{
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
		// Podset group where workers must spread across multiple domains.
		// Setup: 4 hosts with different GPU capacities.
		//        b1
		//         |
		//        r1
		//    /   |   |    \
		// x1:4  x2:2 x3:2  x4:2  (GPU capacity)
		//
		// Leader: 1 pod, CPU only
		// Workers: 6 pods, 1 GPU each, slice size=2 (need 3 slices)
		//
		// BestFit goes from largest to smallest: workers placed in x1 (2 slices), x2 (1 slice).
		"BestFit: podset group workers spread across hosts": {
			nodes: []corev1.Node{
				*testingnode.MakeNode("b1-r1-x1").
					Label(tasBlockLabel, "b1").
					Label(tasRackLabel, "r1").
					Label(corev1.LabelHostname, "x1").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("20"),
						"example.com/gpu":   resource.MustParse("4"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r1-x2").
					Label(tasBlockLabel, "b1").
					Label(tasRackLabel, "r1").
					Label(corev1.LabelHostname, "x2").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("20"),
						"example.com/gpu":   resource.MustParse("2"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r1-x3").
					Label(tasBlockLabel, "b1").
					Label(tasRackLabel, "r1").
					Label(corev1.LabelHostname, "x3").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("20"),
						"example.com/gpu":   resource.MustParse("2"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r1-x4").
					Label(tasBlockLabel, "b1").
					Label(tasRackLabel, "r1").
					Label(corev1.LabelHostname, "x4").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("20"),
						"example.com/gpu":   resource.MustParse("2"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
			},
			levels: defaultThreeLevels,
			podSets: []PodSetTestCase{
				{
					podSetName: "leader",
					topologyRequest: &kueue.PodSetTopologyRequest{
						Preferred: ptr.To(tasBlockLabel),
					},
					requests: resources.Requests{
						corev1.ResourceCPU: 1000,
					},
					podSetGroupName: new("sameGroup"),
					count:           1,
					wantAssignment: &tas.TopologyAssignment{
						Levels: defaultOneLevel,
						Domains: []tas.TopologyDomainAssignment{
							{
								Count:  1,
								Values: []string{"x1"},
							},
						},
					},
				},
				{
					podSetName: "workers",
					topologyRequest: &kueue.PodSetTopologyRequest{
						Preferred:                   ptr.To(tasBlockLabel),
						PodSetSliceSize:             new(int32(2)),
						PodSetSliceRequiredTopology: ptr.To(corev1.LabelHostname),
					},
					requests: resources.Requests{
						corev1.ResourceCPU: 1000,
						"example.com/gpu":  1,
					},
					podSetGroupName: new("sameGroup"),
					count:           6,
					wantAssignment: &tas.TopologyAssignment{
						Levels: defaultOneLevel,
						Domains: []tas.TopologyDomainAssignment{
							{
								Count:  4,
								Values: []string{"x1"},
							},
							{
								Count:  2,
								Values: []string{"x2"},
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
					podSetGroupName: new("sameGroup"),
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
					podSetGroupName: new("sameGroup"),
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
			//  x3   x5   x1    x6   x2   x4
			//
			nodes: []corev1.Node{
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
				*testingnode.MakeNode("b1-r1-x5").
					Label(tasBlockLabel, "b1").
					Label(tasRackLabel, "r1").
					Label(corev1.LabelHostname, "x5").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("2"),
						corev1.ResourcePods: resource.MustParse("10"),
						"example.com/gpu":   resource.MustParse("1"),
					}).
					Ready().
					Obj(),
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
				*testingnode.MakeNode("b2-r4-x6").
					Label(tasBlockLabel, "b2").
					Label(tasRackLabel, "r4").
					Label(corev1.LabelHostname, "x6").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("1"),
						corev1.ResourcePods: resource.MustParse("10"),
						"example.com/gpu":   resource.MustParse("1"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b2-r5-x2").
					Label(tasBlockLabel, "b2").
					Label(tasRackLabel, "r5").
					Label(corev1.LabelHostname, "x2").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("2"),
						corev1.ResourcePods: resource.MustParse("10"),
						"example.com/gpu":   resource.MustParse("1"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b2-r6-x4").
					Label(tasBlockLabel, "b2").
					Label(tasRackLabel, "r6").
					Label(corev1.LabelHostname, "x4").
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
					podSetGroupName: new("sameGroup"),
					count:           1,
					wantAssignment: &tas.TopologyAssignment{
						Levels: []string{corev1.LabelHostname},
						Domains: []tas.TopologyDomainAssignment{
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
					podSetGroupName: new("sameGroup"),
					count:           2,
					wantAssignment: &tas.TopologyAssignment{
						Levels: []string{corev1.LabelHostname},
						Domains: []tas.TopologyDomainAssignment{
							{
								Count: 1,
								Values: []string{
									"x3",
								},
							},
							{
								Count: 1,
								Values: []string{
									"x5",
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
			//  x3  x5    x1  x6  x2   x4   x7   x8
			//
			nodes: []corev1.Node{
				*testingnode.MakeNode("b1-r1-x3").
					Label(tasBlockLabel, "b1").
					Label(tasRackLabel, "r1").
					Label(corev1.LabelHostname, "x3").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("1"),
						corev1.ResourcePods: resource.MustParse("10"),
						"example.com/gpu":   resource.MustParse("1"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r1-x5").
					Label(tasBlockLabel, "b1").
					Label(tasRackLabel, "r1").
					Label(corev1.LabelHostname, "x5").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("1"),
						corev1.ResourcePods: resource.MustParse("10"),
						"example.com/gpu":   resource.MustParse("1"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r2-x1").
					Label(tasBlockLabel, "b1").
					Label(tasRackLabel, "r2").
					Label(corev1.LabelHostname, "x1").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("1"),
						corev1.ResourcePods: resource.MustParse("10"),
						"example.com/gpu":   resource.MustParse("1"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r2-x6").
					Label(tasBlockLabel, "b1").
					Label(tasRackLabel, "r2").
					Label(corev1.LabelHostname, "x6").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("1"),
						corev1.ResourcePods: resource.MustParse("10"),
						"example.com/gpu":   resource.MustParse("1"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b2-r3-x2").
					Label(tasBlockLabel, "b2").
					Label(tasRackLabel, "r3").
					Label(corev1.LabelHostname, "x2").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("1"),
						corev1.ResourcePods: resource.MustParse("10"),
						"example.com/gpu":   resource.MustParse("1"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b2-r3-x4").
					Label(tasBlockLabel, "b2").
					Label(tasRackLabel, "r3").
					Label(corev1.LabelHostname, "x4").
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
					podSetGroupName: new("sameGroup"),
					count:           1,
					wantAssignment: &tas.TopologyAssignment{
						Levels: []string{corev1.LabelHostname},
						Domains: []tas.TopologyDomainAssignment{
							{
								Count: 1,
								Values: []string{
									"x3",
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
					podSetGroupName: new("sameGroup"),
					count:           2,
					wantAssignment: &tas.TopologyAssignment{
						Levels: []string{corev1.LabelHostname},
						Domains: []tas.TopologyDomainAssignment{
							{
								Count: 1,
								Values: []string{
									"x5",
								},
							},
							{
								Count: 1,
								Values: []string{
									"x1",
								},
							},
						},
					},
				},
			},
		},
		"multiple podsets: rack required for both, different resource requests; BestFit": {
			nodes:  multipodNodeset,
			levels: defaultTwoLevels,
			podSets: []PodSetTestCase{
				{
					podSetName: "podset1",
					topologyRequest: &kueue.PodSetTopologyRequest{
						Required: ptr.To(tasRackLabel),
					},
					requests: resources.Requests{
						corev1.ResourceCPU: 1000,
					},
					count: 2,
					wantAssignment: &tas.TopologyAssignment{
						Levels: defaultTwoLevels,
						Domains: []tas.TopologyDomainAssignment{
							{
								Count:  2,
								Values: []string{"b1", "r1"},
							},
						},
					},
				},
				{
					podSetName: "podset2",
					topologyRequest: &kueue.PodSetTopologyRequest{
						Required: ptr.To(tasRackLabel),
					},
					requests: resources.Requests{
						corev1.ResourceMemory: 1024,
					},
					count: 1,
					wantAssignment: &tas.TopologyAssignment{
						Levels: defaultTwoLevels,
						Domains: []tas.TopologyDomainAssignment{
							{
								Count:  1,
								Values: []string{"b1", "r1"},
							},
						},
					},
				},
			},
		},
		"multiple podsets: block required for one, unconstrained for another; TASProfileMixed": {
			nodes:        multipodNodeset,
			levels:       defaultThreeLevels,
			featureGates: map[featuregate.Feature]bool{features.TASProfileMixed: true},
			podSets: []PodSetTestCase{
				{
					podSetName: "podset1",
					topologyRequest: &kueue.PodSetTopologyRequest{
						Required: ptr.To(tasBlockLabel),
					},
					requests: resources.Requests{
						corev1.ResourceCPU: 1000,
					},
					count: 8,
					wantAssignment: &tas.TopologyAssignment{
						Levels: []string{corev1.LabelHostname},
						Domains: []tas.TopologyDomainAssignment{
							{Count: 8, Values: []string{"x2"}},
						},
					},
				},
				{
					podSetName: "podset2",
					topologyRequest: &kueue.PodSetTopologyRequest{
						Unconstrained: new(true),
					},
					requests: resources.Requests{
						corev1.ResourceCPU: 1000,
					},
					count: 2,
					wantAssignment: &tas.TopologyAssignment{
						Levels: []string{corev1.LabelHostname},
						Domains: []tas.TopologyDomainAssignment{
							{Count: 2, Values: []string{"x2"}},
						},
					},
				},
			},
		},
		// Proves cleanup necessity: the second PodSet excludes all nodes via selector.
		// Without resetting temporary per-domain state (e.g. state/stateWithLeader), stale
		// values from the first PodSet would leak and produce a bogus assignment instead of failure.
		"temporary state cleanup prevents leakage across PodSets": {
			nodes: []corev1.Node{
				*testingnode.MakeNode("n1").
					Label(corev1.LabelHostname, "x1").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("4"),
						corev1.ResourceMemory: resource.MustParse("4Gi"),
						corev1.ResourcePods:   resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("n2").
					Label(corev1.LabelHostname, "x2").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("4"),
						corev1.ResourceMemory: resource.MustParse("4Gi"),
						corev1.ResourcePods:   resource.MustParse("10"),
					}).
					Ready().
					Obj(),
			},
			levels: []string{corev1.LabelHostname},
			podSets: []PodSetTestCase{
				{
					podSetName: "ps1",
					requests: resources.Requests{
						corev1.ResourceCPU:    1000,
						corev1.ResourceMemory: 1000,
					},
					count: 1,
					wantAssignment: &tas.TopologyAssignment{
						Levels: []string{corev1.LabelHostname},
						Domains: []tas.TopologyDomainAssignment{
							{Count: 1, Values: []string{"x1"}},
						},
					},
				},
				{
					podSetName:   "ps2",
					nodeSelector: map[string]string{"never": "match"},
					requests: resources.Requests{
						corev1.ResourceCPU:    1000,
						corev1.ResourceMemory: 1000,
					},
					count:      1,
					wantReason: "topology \"default\" doesn't allow to fit any of 1 pod(s). Total nodes: 2; excluded: nodeSelector: 2",
				},
			},
		},
		"elastic workload scale up: delta-only placement preserves previous assignment": {
			nodes: []corev1.Node{
				*testingnode.MakeNode("x1").
					Label(corev1.LabelHostname, "x1").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("2"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("x2").
					Label(corev1.LabelHostname, "x2").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("2"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("x3").
					Label(corev1.LabelHostname, "x3").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("2"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
			},
			levels: defaultOneLevel,
			podSets: []PodSetTestCase{{
				topologyRequest: &kueue.PodSetTopologyRequest{
					Unconstrained: new(true),
				},
				requests: resources.Requests{
					corev1.ResourceCPU: 1000,
				},
				count: 4,
				previousAssignment: tas.V1Beta2From(&tas.TopologyAssignment{
					Levels: []string{corev1.LabelHostname},
					Domains: []tas.TopologyDomainAssignment{
						{Count: 2, Values: []string{"x1"}},
					},
				}),
				wantAssignment: &tas.TopologyAssignment{
					Levels: []string{corev1.LabelHostname},
					Domains: []tas.TopologyDomainAssignment{
						{Count: 2, Values: []string{"x1"}},
						{Count: 2, Values: []string{"x2"}},
					},
				},
			}},
			featureGates: map[featuregate.Feature]bool{
				features.ElasticJobsViaWorkloadSlices:        true,
				features.ElasticJobsViaWorkloadSlicesWithTAS: true,
			},
		},
		"elastic workload scale up: spread across multiple nodes preserved": {
			nodes: []corev1.Node{
				*testingnode.MakeNode("x1").
					Label(corev1.LabelHostname, "x1").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("2"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("x2").
					Label(corev1.LabelHostname, "x2").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("2"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("x3").
					Label(corev1.LabelHostname, "x3").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("2"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
			},
			levels: defaultOneLevel,
			podSets: []PodSetTestCase{{
				topologyRequest: &kueue.PodSetTopologyRequest{
					Unconstrained: new(true),
				},
				requests: resources.Requests{
					corev1.ResourceCPU: 1000,
				},
				count: 4,
				previousAssignment: tas.V1Beta2From(&tas.TopologyAssignment{
					Levels: []string{corev1.LabelHostname},
					Domains: []tas.TopologyDomainAssignment{
						{Count: 1, Values: []string{"x1"}},
						{Count: 1, Values: []string{"x2"}},
					},
				}),
				wantAssignment: &tas.TopologyAssignment{
					Levels: []string{corev1.LabelHostname},
					Domains: []tas.TopologyDomainAssignment{
						{Count: 1, Values: []string{"x1"}},
						{Count: 1, Values: []string{"x2"}},
						{Count: 2, Values: []string{"x3"}},
					},
				},
			}},
			featureGates: map[featuregate.Feature]bool{
				features.ElasticJobsViaWorkloadSlices:        true,
				features.ElasticJobsViaWorkloadSlicesWithTAS: true,
			},
		},
		"elastic workload scale down: truncates assignment": {
			nodes: []corev1.Node{
				*testingnode.MakeNode("x1").
					Label(corev1.LabelHostname, "x1").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("4"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("x2").
					Label(corev1.LabelHostname, "x2").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("4"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
			},
			levels: defaultOneLevel,
			podSets: []PodSetTestCase{{
				topologyRequest: &kueue.PodSetTopologyRequest{
					Unconstrained: new(true),
				},
				requests: resources.Requests{
					corev1.ResourceCPU: 1000,
				},
				count: 3,
				previousAssignment: tas.V1Beta2From(&tas.TopologyAssignment{
					Levels: []string{corev1.LabelHostname},
					Domains: []tas.TopologyDomainAssignment{
						{Count: 3, Values: []string{"x1"}},
						{Count: 2, Values: []string{"x2"}},
					},
				}),
				wantAssignment: &tas.TopologyAssignment{
					Levels: []string{corev1.LabelHostname},
					Domains: []tas.TopologyDomainAssignment{
						{Count: 3, Values: []string{"x1"}},
					},
				},
			}},
			featureGates: map[featuregate.Feature]bool{
				features.ElasticJobsViaWorkloadSlices:        true,
				features.ElasticJobsViaWorkloadSlicesWithTAS: true,
			},
		},
		"elastic workload same count: reuses previous assignment exactly": {
			nodes: []corev1.Node{
				*testingnode.MakeNode("x1").
					Label(corev1.LabelHostname, "x1").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("4"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("x2").
					Label(corev1.LabelHostname, "x2").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("4"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
			},
			levels: defaultOneLevel,
			podSets: []PodSetTestCase{{
				topologyRequest: &kueue.PodSetTopologyRequest{
					Unconstrained: new(true),
				},
				requests: resources.Requests{
					corev1.ResourceCPU: 1000,
				},
				count: 3,
				previousAssignment: tas.V1Beta2From(&tas.TopologyAssignment{
					Levels: []string{corev1.LabelHostname},
					Domains: []tas.TopologyDomainAssignment{
						{Count: 2, Values: []string{"x1"}},
						{Count: 1, Values: []string{"x2"}},
					},
				}),
				wantAssignment: &tas.TopologyAssignment{
					Levels: []string{corev1.LabelHostname},
					Domains: []tas.TopologyDomainAssignment{
						{Count: 2, Values: []string{"x1"}},
						{Count: 1, Values: []string{"x2"}},
					},
				},
			}},
			featureGates: map[featuregate.Feature]bool{
				features.ElasticJobsViaWorkloadSlices:        true,
				features.ElasticJobsViaWorkloadSlicesWithTAS: true,
			},
		},
		"multi-layer topology: block required; rack slices of 4; host slices of 2; TASMultiLayerTopology": {
			// 4-level topology: block → rack → hostname
			//              b1
			//       /             \
			//      r1             r2
			//   /      \        /    \
			//  x1(1)  x2(4)  x3(3)  x4(4)
			//
			// 8 pods total: 4 per rack, 2 per host
			featureGates: map[featuregate.Feature]bool{features.TASMultiLayerTopology: true},
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
						corev1.ResourceCPU:  resource.MustParse("4"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r2-x3").
					Label(tasBlockLabel, "b1").
					Label(tasRackLabel, "r2").
					Label(corev1.LabelHostname, "x3").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("3"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r2-x4").
					Label(tasBlockLabel, "b1").
					Label(tasRackLabel, "r2").
					Label(corev1.LabelHostname, "x4").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("4"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
			},
			levels: defaultThreeLevels,
			podSets: []PodSetTestCase{{
				topologyRequest: &kueue.PodSetTopologyRequest{
					Required: ptr.To(tasBlockLabel),
					PodsetSliceRequiredTopologyConstraints: []kueue.PodsetSliceRequiredTopologyConstraint{
						{Topology: tasRackLabel, Size: 4},
						{Topology: corev1.LabelHostname, Size: 2},
					},
				},
				requests: resources.Requests{
					corev1.ResourceCPU: 1000,
				},
				count: 8,
				wantAssignment: &tas.TopologyAssignment{
					Levels: defaultOneLevel,
					Domains: []tas.TopologyDomainAssignment{
						{Count: 4, Values: []string{"x2"}},
						{Count: 2, Values: []string{"x3"}},
						{Count: 2, Values: []string{"x4"}},
					},
				},
			}},
		},
		"multi-layer topology: no feature gate; additional layers ignored": {
			// Without the feature gate, additional slice layers should be ignored
			// and the algorithm should behave as single-layer slicing.
			// 4-level topology: block → rack → hostname
			//              b1
			//       /             \
			//      r1             r2
			//   /      \        /    \
			//  x1(1)  x2(4)  x3(3)  x4(4)
			//
			// 8 pods total: 4 per rack
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
						corev1.ResourceCPU:  resource.MustParse("4"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r2-x3").
					Label(tasBlockLabel, "b1").
					Label(tasRackLabel, "r2").
					Label(corev1.LabelHostname, "x3").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("3"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r2-x4").
					Label(tasBlockLabel, "b1").
					Label(tasRackLabel, "r2").
					Label(corev1.LabelHostname, "x4").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("4"),
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
					PodSetSliceSize:             new(int32(4)),
					// Without the feature gate, only two-level fields are used;
					// PodsetSliceRequiredTopologyConstraints would not be populated by the parser.
				},
				requests: resources.Requests{
					corev1.ResourceCPU: 1000,
				},
				count: 8,
				wantAssignment: &tas.TopologyAssignment{
					Levels: defaultOneLevel,
					Domains: []tas.TopologyDomainAssignment{
						{Count: 1, Values: []string{"x1"}},
						{Count: 3, Values: []string{"x2"}},
						{Count: 3, Values: []string{"x3"}},
						{Count: 1, Values: []string{"x4"}},
					},
				},
			}},
		},
		"multi-layer topology: mimic a real-world GB200 cluster, with NVL36 arch (2GPUs/node); dc required; aizone slices of 48; rack slices of 16; TASMultiLayerTopology": {
			// Mimic a real-world GB200 cluster setup
			// organized as 1 dc × 2 aizones × 2 blocks × 2 racks = 8 racks,
			// 18 nodes per rack (18 × 2 GPU = 36 GPUs → 18 pods at 2 GPUs each).
			// 96 pods with constraints: 48 per aizone, 16 per rack.
			// Expected: 6 racks used, 16 pods each (3 per aizone).
			featureGates: map[featuregate.Feature]bool{features.TASMultiLayerTopology: true},
			nodes: func() []corev1.Node {
				type rackDef struct {
					dc, az, block, rack string
				}
				racks := []rackDef{
					{"dc0", "aizone0", "block0", "r0"},
					{"dc0", "aizone0", "block0", "r1"},
					{"dc0", "aizone0", "block1", "r2"},
					{"dc0", "aizone0", "block1", "r3"},
					{"dc0", "aizone1", "block2", "r4"},
					{"dc0", "aizone1", "block2", "r5"},
					{"dc0", "aizone1", "block3", "r6"},
					{"dc0", "aizone1", "block3", "r7"},
				}
				var nodes []corev1.Node
				for _, r := range racks {
					for i := range 18 {
						name := fmt.Sprintf("%s-%s-%s-n%d", r.block, r.rack, r.az, i)
						nodes = append(nodes, *testingnode.MakeNode(name).
							Label(tasDataCenterLabel, r.dc).
							Label(tasAIZoneLabel, r.az).
							Label(tasBlockLabel, r.block).
							Label(tasRackLabel, r.rack).
							StatusAllocatable(corev1.ResourceList{
								"nvidia.com/gpu":    resource.MustParse("2"),
								corev1.ResourcePods: resource.MustParse("110"),
							}).
							Ready().
							Obj())
					}
				}
				return nodes
			}(),
			levels: []string{
				tasDataCenterLabel,
				tasAIZoneLabel,
				tasBlockLabel,
				tasRackLabel,
			},
			podSets: []PodSetTestCase{{
				topologyRequest: &kueue.PodSetTopologyRequest{
					Required: ptr.To(tasDataCenterLabel),
					PodsetSliceRequiredTopologyConstraints: []kueue.PodsetSliceRequiredTopologyConstraint{
						{Topology: tasAIZoneLabel, Size: 48},
						{Topology: tasRackLabel, Size: 16},
					},
				},
				requests: resources.Requests{
					"nvidia.com/gpu": 2,
				},
				count: 96,
				wantAssignment: &tas.TopologyAssignment{
					Levels: []string{
						tasDataCenterLabel,
						tasAIZoneLabel,
						tasBlockLabel,
						tasRackLabel,
					},
					Domains: []tas.TopologyDomainAssignment{
						{Count: 16, Values: []string{"dc0", "aizone0", "block0", "r0"}},
						{Count: 16, Values: []string{"dc0", "aizone0", "block0", "r1"}},
						{Count: 16, Values: []string{"dc0", "aizone0", "block1", "r2"}},
						{Count: 16, Values: []string{"dc0", "aizone1", "block2", "r4"}},
						{Count: 16, Values: []string{"dc0", "aizone1", "block2", "r5"}},
						{Count: 16, Values: []string{"dc0", "aizone1", "block3", "r6"}},
					},
				},
			}},
		},
		"multi-layer topology: host slice rounding makes rack slice impossible": {
			// block → rack → hostname
			//          b1
			//          |
			//         r1
			//      /   |   \
			//   x1(3) x2(3) x3(0)
			featureGates: map[featuregate.Feature]bool{features.TASMultiLayerTopology: true},
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
						corev1.ResourceCPU:  resource.MustParse("0"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
			},
			levels: defaultThreeLevels,
			podSets: []PodSetTestCase{
				{
					topologyRequest: &kueue.PodSetTopologyRequest{
						Required: ptr.To(tasBlockLabel),
						PodsetSliceRequiredTopologyConstraints: []kueue.PodsetSliceRequiredTopologyConstraint{
							{Topology: tasRackLabel, Size: 6},
							{Topology: corev1.LabelHostname, Size: 2},
						},
					},
					requests: resources.Requests{
						corev1.ResourceCPU: 1000,
					},
					count:      6,
					wantReason: `topology "default" doesn't allow to fit; 0/1 slice(s) fit on level cloud.com/topology-rack; 2/3 slice(s) fit on level kubernetes.io/hostname. Total nodes: 3; excluded: resource "cpu": 1`,
				},
			},
		},
		"multi-layer topology: small host kills rack slices despite enough total capacity": {
			// block → rack → hostname
			//              b1
			//       /             \
			//      r1             r2
			//   /      \        /    \
			//  x1(7)  x2(4)  x3(7)  x4(3)
			featureGates: map[featuregate.Feature]bool{features.TASMultiLayerTopology: true},
			nodes: []corev1.Node{
				*testingnode.MakeNode("b1-r1-x1").
					Label(tasBlockLabel, "b1").
					Label(tasRackLabel, "r1").
					Label(corev1.LabelHostname, "x1").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("7"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r1-x2").
					Label(tasBlockLabel, "b1").
					Label(tasRackLabel, "r1").
					Label(corev1.LabelHostname, "x2").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("4"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r2-x3").
					Label(tasBlockLabel, "b1").
					Label(tasRackLabel, "r2").
					Label(corev1.LabelHostname, "x3").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("7"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r2-x4").
					Label(tasBlockLabel, "b1").
					Label(tasRackLabel, "r2").
					Label(corev1.LabelHostname, "x4").
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
					Required: ptr.To(tasBlockLabel),
					PodsetSliceRequiredTopologyConstraints: []kueue.PodsetSliceRequiredTopologyConstraint{
						{Topology: tasRackLabel, Size: 8},
						{Topology: corev1.LabelHostname, Size: 4},
					},
				},
				requests: resources.Requests{
					corev1.ResourceCPU: 1000,
				},
				count:      16,
				wantReason: `topology "default" doesn't allow to fit; 1/2 slice(s) fit on level cloud.com/topology-rack; 3/4 slice(s) fit on level kubernetes.io/hostname`,
			}},
		},
		"multi-layer topology: enough hostname slices but not enough rack slices": {
			// block → rack → hostname
			//            b1
			//       /    |     \
			//      r1    r2    r3
			//      |     |      |
			//    x1(4) x2(4)  x3(4)
			featureGates: map[featuregate.Feature]bool{features.TASMultiLayerTopology: true},
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
				*testingnode.MakeNode("b1-r2-x2").
					Label(tasBlockLabel, "b1").
					Label(tasRackLabel, "r2").
					Label(corev1.LabelHostname, "x2").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("4"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r3-x3").
					Label(tasBlockLabel, "b1").
					Label(tasRackLabel, "r3").
					Label(corev1.LabelHostname, "x3").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("4"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
			},
			levels: defaultThreeLevels,
			podSets: []PodSetTestCase{{
				topologyRequest: &kueue.PodSetTopologyRequest{
					Required: ptr.To(tasBlockLabel),
					PodsetSliceRequiredTopologyConstraints: []kueue.PodsetSliceRequiredTopologyConstraint{
						{Topology: tasRackLabel, Size: 6},
						{Topology: corev1.LabelHostname, Size: 2},
					},
				},
				requests: resources.Requests{
					corev1.ResourceCPU: 1000,
				},
				count:      12,
				wantReason: `topology "default" doesn't allow to fit; 0/2 slice(s) fit on level cloud.com/topology-rack; 6/6 slice(s) fit on level kubernetes.io/hostname`,
			}},
		},
		"multi-layer topology: 3-layer negative case with small hosts cascading up": {
			// datacenter → block → rack → hostname
			//                         dc1
			//             /                        \
			//            b1                        b2
			//        /         \               /         \
			//     r1(8)        r2(8)         r3(8)        r4(2)
			//    /    \       /    \         /  \         /   \
			// x1(4)  x2(4)  x3(4)  x4(4)   x5(4) x6(4)  x7(1) x8(1)
			//
			// Each rack has 2 nodes; x7,x8 are weaker (1 CPU each).
			// The incoming workload requests 24 pods: hostname slice=3, rack slice=6, block slice=12.
			// x7,x8 too small for hostname slices → r4 loses slices → b2 can't fit.
			featureGates: map[featuregate.Feature]bool{features.TASMultiLayerTopology: true},
			nodes: []corev1.Node{
				// b1 / r1
				*testingnode.MakeNode("dc1-b1-r1-x1").
					Label(tasDataCenterLabel, "dc1").
					Label(tasBlockLabel, "b1").
					Label(tasRackLabel, "r1").
					Label(corev1.LabelHostname, "x1").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("4"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("dc1-b1-r1-x2").
					Label(tasDataCenterLabel, "dc1").
					Label(tasBlockLabel, "b1").
					Label(tasRackLabel, "r1").
					Label(corev1.LabelHostname, "x2").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("4"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				// b1 / r2
				*testingnode.MakeNode("dc1-b1-r2-x3").
					Label(tasDataCenterLabel, "dc1").
					Label(tasBlockLabel, "b1").
					Label(tasRackLabel, "r2").
					Label(corev1.LabelHostname, "x3").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("4"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("dc1-b1-r2-x4").
					Label(tasDataCenterLabel, "dc1").
					Label(tasBlockLabel, "b1").
					Label(tasRackLabel, "r2").
					Label(corev1.LabelHostname, "x4").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("4"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				// b2 / r3
				*testingnode.MakeNode("dc1-b2-r3-x5").
					Label(tasDataCenterLabel, "dc1").
					Label(tasBlockLabel, "b2").
					Label(tasRackLabel, "r3").
					Label(corev1.LabelHostname, "x5").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("4"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("dc1-b2-r3-x6").
					Label(tasDataCenterLabel, "dc1").
					Label(tasBlockLabel, "b2").
					Label(tasRackLabel, "r3").
					Label(corev1.LabelHostname, "x6").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("4"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				// b2 / r4 — small rack
				*testingnode.MakeNode("dc1-b2-r4-x7").
					Label(tasDataCenterLabel, "dc1").
					Label(tasBlockLabel, "b2").
					Label(tasRackLabel, "r4").
					Label(corev1.LabelHostname, "x7").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("1"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("dc1-b2-r4-x8").
					Label(tasDataCenterLabel, "dc1").
					Label(tasBlockLabel, "b2").
					Label(tasRackLabel, "r4").
					Label(corev1.LabelHostname, "x8").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("1"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
			},
			levels: append([]string{tasDataCenterLabel}, defaultThreeLevels...),
			podSets: []PodSetTestCase{
				{
					topologyRequest: &kueue.PodSetTopologyRequest{
						Required: ptr.To(tasDataCenterLabel),
						PodsetSliceRequiredTopologyConstraints: []kueue.PodsetSliceRequiredTopologyConstraint{
							{Topology: tasBlockLabel, Size: 12},
							{Topology: tasRackLabel, Size: 6},
							{Topology: corev1.LabelHostname, Size: 3},
						},
					},
					requests: resources.Requests{
						corev1.ResourceCPU: 1000,
					},
					count:      24,
					wantReason: `topology "default" doesn't allow to fit; 1/2 slice(s) fit on level cloud.com/topology-block; 3/4 slice(s) fit on level cloud.com/topology-rack; 6/8 slice(s) fit on level kubernetes.io/hostname`,
				},
			},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx, log := utiltesting.ContextWithLog(t)
			features.SetFeatureGatesDuringTest(t, tc.featureGates)

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
			for i := range tc.nodes {
				tasCache.SyncNode(&tc.nodes[i])
			}

			topologyInformation := topologyInformation{
				Levels: tc.levels,
			}
			flavorInformation := flavorInformation{
				TopologyName: "default",
				NodeLabels:   tc.nodeLabels,
			}
			for _, pod := range tc.pods {
				tasCache.Update(&pod, log)
			}
			tasFlavorCache := tasCache.NewTASFlavorCache(topologyInformation, flavorInformation)
			snapshot := tasFlavorCache.snapshot(log, tasCache.nodesCache.find(tasFlavorCache.flavor.NodeLabels, tasFlavorCache.topology.Levels))
			flavorTASRequests := make([]TASPodSetRequests, 0, len(tc.podSets))
			wantResult := make(TASAssignmentsResult)
			for _, ps := range tc.podSets {
				tasInput := TASPodSetRequests{
					PodSet: &kueue.PodSet{
						Name:            kueue.NewPodSetReference(ps.podSetName),
						TopologyRequest: ps.topologyRequest,
						Template: corev1.PodTemplateSpec{
							Spec: corev1.PodSpec{
								Tolerations:  ps.tolerations,
								NodeSelector: ps.nodeSelector,
							},
						},
					},
					SinglePodRequests:  ps.requests,
					Count:              ps.count,
					PreviousAssignment: ps.previousAssignment,
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
					wantPodSetResult.TopologyAssignment = ps.wantAssignment
				}
				wantResult[kueue.NewPodSetReference(ps.podSetName)] = wantPodSetResult
			}
			gotResult := snapshot.FindTopologyAssignmentsForFlavor(flavorTASRequests)
			if diff := cmp.Diff(wantResult, gotResult); diff != "" {
				t.Errorf("unexpected topology assignment (-want,+got): %s", diff)
			}
		})
	}
}

func TestFindTopologyAssignmentsMultiLayerReplacement(t *testing.T) {
	const (
		tasBlockLabel  = "cloud.com/topology-block"
		tasRackLabel   = "cloud.com/topology-rack"
		tasSwitchLabel = "cloud.com/topology-switch"
	)
	defaultLevels := []string{tasBlockLabel, tasRackLabel, corev1.LabelHostname}
	podSetName := kueue.PodSetReference("main")

	cases := map[string]struct {
		levels          []string
		nodes           []corev1.Node
		pods            []corev1.Pod
		existingTA      *kueue.TopologyAssignment
		admissionCount  int32
		unhealthyNode   string
		topologyRequest *kueue.PodSetTopologyRequest
		count           int32
		wantAssignment  *tas.TopologyAssignment
		wantReason      string
	}{
		"replace unhealthy node in incomplete rack slice": {
			//       b1
			//   /        \
			//  r1        r2
			//  /  \    /  |  \
			// x1  x2  x3  x4 x5
			//          ^(NotReady)
			nodes: []corev1.Node{
				*testingnode.MakeNode("b1-r1-x1").
					Label(tasBlockLabel, "b1").Label(tasRackLabel, "r1").Label(corev1.LabelHostname, "x1").
					StatusAllocatable(corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1"), corev1.ResourcePods: resource.MustParse("10")}).
					Ready().Obj(),
				*testingnode.MakeNode("b1-r1-x2").
					Label(tasBlockLabel, "b1").Label(tasRackLabel, "r1").Label(corev1.LabelHostname, "x2").
					StatusAllocatable(corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1"), corev1.ResourcePods: resource.MustParse("10")}).
					Ready().Obj(),
				*testingnode.MakeNode("b1-r2-x3").
					Label(tasBlockLabel, "b1").Label(tasRackLabel, "r2").Label(corev1.LabelHostname, "x3").
					StatusAllocatable(corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1"), corev1.ResourcePods: resource.MustParse("10")}).
					NotReady().Obj(),
				*testingnode.MakeNode("b1-r2-x4").
					Label(tasBlockLabel, "b1").Label(tasRackLabel, "r2").Label(corev1.LabelHostname, "x4").
					StatusAllocatable(corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1"), corev1.ResourcePods: resource.MustParse("10")}).
					Ready().Obj(),
				*testingnode.MakeNode("b1-r2-x5").
					Label(tasBlockLabel, "b1").Label(tasRackLabel, "r2").Label(corev1.LabelHostname, "x5").
					StatusAllocatable(corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("2"), corev1.ResourcePods: resource.MustParse("10")}).
					Ready().Obj(),
			},
			// Existing assignment: 4 pods in block b1 (2 per rack).
			existingTA: utiltestingapi.MakeTopologyAssignment([]string{corev1.LabelHostname}).
				Domain(tas.TopologyDomainAssignment{Count: 1, Values: []string{"x1"}}).
				Domain(tas.TopologyDomainAssignment{Count: 1, Values: []string{"x2"}}).
				Domain(tas.TopologyDomainAssignment{Count: 1, Values: []string{"x3"}}).
				Domain(tas.TopologyDomainAssignment{Count: 1, Values: []string{"x4"}}).
				Obj(),
			admissionCount: 4,
			unhealthyNode:  "x3",
			topologyRequest: &kueue.PodSetTopologyRequest{
				Required: ptr.To(tasBlockLabel),
				PodsetSliceRequiredTopologyConstraints: []kueue.PodsetSliceRequiredTopologyConstraint{
					{Topology: tasRackLabel, Size: 2},
				},
			},
			count: 4,
			// x3 is NotReady → replacement confined to rack r2. BestFit picks x4 (smaller allocatable = tighter fit).
			wantAssignment: &tas.TopologyAssignment{
				Levels: []string{corev1.LabelHostname},
				Domains: []tas.TopologyDomainAssignment{
					{Count: 1, Values: []string{"x1"}},
					{Count: 1, Values: []string{"x2"}},
					{Count: 2, Values: []string{"x4"}},
				},
			},
		},
		"replacement fails when no capacity in incomplete slice domain": {
			//       b1
			//   /        \
			//  r1        r2
			//  /  \    /    \
			// x1  x2  x3    x4
			//          ^(NotReady)
			nodes: []corev1.Node{
				*testingnode.MakeNode("b1-r1-x1").
					Label(tasBlockLabel, "b1").Label(tasRackLabel, "r1").Label(corev1.LabelHostname, "x1").
					StatusAllocatable(corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1"), corev1.ResourcePods: resource.MustParse("10")}).
					Ready().Obj(),
				*testingnode.MakeNode("b1-r1-x2").
					Label(tasBlockLabel, "b1").Label(tasRackLabel, "r1").Label(corev1.LabelHostname, "x2").
					StatusAllocatable(corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1"), corev1.ResourcePods: resource.MustParse("10")}).
					Ready().Obj(),
				*testingnode.MakeNode("b1-r2-x3").
					Label(tasBlockLabel, "b1").Label(tasRackLabel, "r2").Label(corev1.LabelHostname, "x3").
					StatusAllocatable(corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1"), corev1.ResourcePods: resource.MustParse("10")}).
					NotReady().Obj(),
				*testingnode.MakeNode("b1-r2-x4").
					Label(tasBlockLabel, "b1").Label(tasRackLabel, "r2").Label(corev1.LabelHostname, "x4").
					StatusAllocatable(corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("500m"), corev1.ResourcePods: resource.MustParse("10")}).
					Ready().Obj(),
			},
			existingTA: utiltestingapi.MakeTopologyAssignment([]string{corev1.LabelHostname}).
				Domain(tas.TopologyDomainAssignment{Count: 1, Values: []string{"x1"}}).
				Domain(tas.TopologyDomainAssignment{Count: 1, Values: []string{"x2"}}).
				Domain(tas.TopologyDomainAssignment{Count: 1, Values: []string{"x3"}}).
				Domain(tas.TopologyDomainAssignment{Count: 1, Values: []string{"x4"}}).
				Obj(),
			admissionCount: 4,
			unhealthyNode:  "x3",
			topologyRequest: &kueue.PodSetTopologyRequest{
				Required: ptr.To(tasBlockLabel),
				PodsetSliceRequiredTopologyConstraints: []kueue.PodsetSliceRequiredTopologyConstraint{
					{Topology: tasRackLabel, Size: 2},
				},
			},
			count: 4,
			// x3 is NotReady, x4 has insufficient CPU (500m < 1000m request) → no node available in r2 for replacement.
			wantReason: `topology "default" doesn't allow to fit any of 1 pod(s). Total nodes: 3; excluded: resource "cpu": 1, topologyDomain: 2`,
		},
		"3-layer: innermost broken domain confines replacement to correct switch": {
			// Topology: block → rack → switch → hostname
			//                    b1
			//              /            \
			//            r1              r2
			//          /    \          /    \
			//        s1      s2      s3      s4
			//       / \     / \     / \     / \
			//      x1 x2  x3 x4  x5  x6  x7  x8
			//              ^(NotReady)
			//
			// Constraints: [{rack: 8}, {switch: 4}, {hostname: 2}], Required: block
			// Initial: 16 pods (2 per host). x3 dies → tr.Count = 2.
			//   2 % 8 ≠ 0 → rack constraint broken (outermost)
			//   2 % 4 ≠ 0 → switch constraint broken (innermost broken)
			//   2 % 2 = 0 → hostname constraint satisfied
			//
			// BUG without fix: requiredReplacementDomain uses outermost constraint
			// (rack, constraints[0]) → domain = rack r1 ("b1,r1").
			// belongsToRequiredDomain admits all hosts in r1 (x1, x2, x4).
			// x1 has tighter fit (allocatable=4) than x4
			// (allocatable=8) → BestFit picks x1 (in switch s1!) over x4 (in s2).
			// Result: x1(4) — replacement lands in WRONG switch. ✗
			//
			// With fix: uses innermost broken constraint (switch) → domain = switch s2
			// ("b1,r1,s2"). Only x4 is eligible. Result: x4(4). ✓
			levels: []string{tasBlockLabel, tasRackLabel, tasSwitchLabel, corev1.LabelHostname},
			nodes: []corev1.Node{
				*testingnode.MakeNode("b1-r1-s1-x1").
					Label(tasBlockLabel, "b1").Label(tasRackLabel, "r1").Label(tasSwitchLabel, "s1").Label(corev1.LabelHostname, "x1").
					StatusAllocatable(corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("4"), corev1.ResourcePods: resource.MustParse("10")}).
					Ready().Obj(),
				*testingnode.MakeNode("b1-r1-s1-x2").
					Label(tasBlockLabel, "b1").Label(tasRackLabel, "r1").Label(tasSwitchLabel, "s1").Label(corev1.LabelHostname, "x2").
					StatusAllocatable(corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("4"), corev1.ResourcePods: resource.MustParse("10")}).
					Ready().Obj(),
				*testingnode.MakeNode("b1-r1-s2-x3").
					Label(tasBlockLabel, "b1").Label(tasRackLabel, "r1").Label(tasSwitchLabel, "s2").Label(corev1.LabelHostname, "x3").
					StatusAllocatable(corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("4"), corev1.ResourcePods: resource.MustParse("10")}).
					NotReady().Obj(),
				*testingnode.MakeNode("b1-r1-s2-x4").
					Label(tasBlockLabel, "b1").Label(tasRackLabel, "r1").Label(tasSwitchLabel, "s2").Label(corev1.LabelHostname, "x4").
					StatusAllocatable(corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("8"), corev1.ResourcePods: resource.MustParse("10")}).
					Ready().Obj(),
				*testingnode.MakeNode("b1-r2-s3-x5").
					Label(tasBlockLabel, "b1").Label(tasRackLabel, "r2").Label(tasSwitchLabel, "s3").Label(corev1.LabelHostname, "x5").
					StatusAllocatable(corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("4"), corev1.ResourcePods: resource.MustParse("10")}).
					Ready().Obj(),
				*testingnode.MakeNode("b1-r2-s3-x6").
					Label(tasBlockLabel, "b1").Label(tasRackLabel, "r2").Label(tasSwitchLabel, "s3").Label(corev1.LabelHostname, "x6").
					StatusAllocatable(corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("4"), corev1.ResourcePods: resource.MustParse("10")}).
					Ready().Obj(),
				*testingnode.MakeNode("b1-r2-s4-x7").
					Label(tasBlockLabel, "b1").Label(tasRackLabel, "r2").Label(tasSwitchLabel, "s4").Label(corev1.LabelHostname, "x7").
					StatusAllocatable(corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("4"), corev1.ResourcePods: resource.MustParse("10")}).
					Ready().Obj(),
				*testingnode.MakeNode("b1-r2-s4-x8").
					Label(tasBlockLabel, "b1").Label(tasRackLabel, "r2").Label(tasSwitchLabel, "s4").Label(corev1.LabelHostname, "x8").
					StatusAllocatable(corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("4"), corev1.ResourcePods: resource.MustParse("10")}).
					Ready().Obj(),
			},
			// 16 pods: 2 per host. r1: s1(x1+x2)=4 + s2(x3+x4)=4 = 8.
			// r2: s3(x5+x6)=4 + s4(x7+x8)=4 = 8. All constraints satisfied.
			existingTA: utiltestingapi.MakeTopologyAssignment([]string{corev1.LabelHostname}).
				Domain(tas.TopologyDomainAssignment{Count: 2, Values: []string{"x1"}}).
				Domain(tas.TopologyDomainAssignment{Count: 2, Values: []string{"x2"}}).
				Domain(tas.TopologyDomainAssignment{Count: 2, Values: []string{"x3"}}).
				Domain(tas.TopologyDomainAssignment{Count: 2, Values: []string{"x4"}}).
				Domain(tas.TopologyDomainAssignment{Count: 2, Values: []string{"x5"}}).
				Domain(tas.TopologyDomainAssignment{Count: 2, Values: []string{"x6"}}).
				Domain(tas.TopologyDomainAssignment{Count: 2, Values: []string{"x7"}}).
				Domain(tas.TopologyDomainAssignment{Count: 2, Values: []string{"x8"}}).
				Obj(),
			admissionCount: 16,
			unhealthyNode:  "x3",
			topologyRequest: &kueue.PodSetTopologyRequest{
				Required: ptr.To(tasBlockLabel),
				PodsetSliceRequiredTopologyConstraints: []kueue.PodsetSliceRequiredTopologyConstraint{
					{Topology: tasRackLabel, Size: 8},
					{Topology: tasSwitchLabel, Size: 4},
					{Topology: corev1.LabelHostname, Size: 2},
				},
			},
			count: 16,
			// With fix: domain = switch s2. Only x4 eligible. x4(4). ✓
			wantAssignment: &tas.TopologyAssignment{
				Levels: []string{corev1.LabelHostname},
				Domains: []tas.TopologyDomainAssignment{
					{Count: 2, Values: []string{"x1"}},
					{Count: 2, Values: []string{"x2"}},
					{Count: 4, Values: []string{"x4"}},
					{Count: 2, Values: []string{"x5"}},
					{Count: 2, Values: []string{"x6"}},
					{Count: 2, Values: []string{"x7"}},
					{Count: 2, Values: []string{"x8"}},
				},
			},
		},
		"2-layer: sliceSize=2 prevents scattered single-pod placement across hosts": {
			// Topology: block → rack → hostname
			//       b1
			//   /        \
			//  r1         r2
			//  /  \    /  |  \
			// x1  x2  x3 x4  x5
			//          ^(NotReady)
			//
			// Constraints: [{rack: 4}, {hostname: 2}], Required: block
			// Initial: 8 pods, 2 per host on x1-x4. After x3 dies → tr.Count = 2.
			//   2 % 4 ≠ 0 → rack constraint broken → domain = r2
			//   2 % 2 = 0 → hostname constraint satisfied
			//
			// x4 and x5 each have insufficient CPU to fit a pair of pods (2 CPU).
			//
			// BUG without fix: effectiveSliceSize hardcoded to 1.
			// sliceSize=1 places 2 individual pods. No single host fits 2,
			// so BestFit goes to rack level and scatters: x4(1), x5(1).
			// Hostname grouping constraint (size 2) is violated.
			//
			// With fix: effectiveSliceSize = 2. sliceSize=2 needs one host
			// that can fit 2 pods. Neither x4 nor x5 can. Replacement correctly
			// fails rather than silently violating the topology constraint.
			nodes: []corev1.Node{
				*testingnode.MakeNode("b1-r1-x1").
					Label(tasBlockLabel, "b1").Label(tasRackLabel, "r1").Label(corev1.LabelHostname, "x1").
					StatusAllocatable(corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("2"), corev1.ResourcePods: resource.MustParse("10")}).
					Ready().Obj(),
				*testingnode.MakeNode("b1-r1-x2").
					Label(tasBlockLabel, "b1").Label(tasRackLabel, "r1").Label(corev1.LabelHostname, "x2").
					StatusAllocatable(corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("2"), corev1.ResourcePods: resource.MustParse("10")}).
					Ready().Obj(),
				*testingnode.MakeNode("b1-r2-x3").
					Label(tasBlockLabel, "b1").Label(tasRackLabel, "r2").Label(corev1.LabelHostname, "x3").
					StatusAllocatable(corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("2"), corev1.ResourcePods: resource.MustParse("10")}).
					NotReady().Obj(),
				*testingnode.MakeNode("b1-r2-x4").
					Label(tasBlockLabel, "b1").Label(tasRackLabel, "r2").Label(corev1.LabelHostname, "x4").
					StatusAllocatable(corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1"), corev1.ResourcePods: resource.MustParse("10")}).
					Ready().Obj(),
				*testingnode.MakeNode("b1-r2-x5").
					Label(tasBlockLabel, "b1").Label(tasRackLabel, "r2").Label(corev1.LabelHostname, "x5").
					StatusAllocatable(corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1"), corev1.ResourcePods: resource.MustParse("10")}).
					Ready().Obj(),
			},
			existingTA: utiltestingapi.MakeTopologyAssignment([]string{corev1.LabelHostname}).
				Domain(tas.TopologyDomainAssignment{Count: 2, Values: []string{"x1"}}).
				Domain(tas.TopologyDomainAssignment{Count: 2, Values: []string{"x2"}}).
				Domain(tas.TopologyDomainAssignment{Count: 2, Values: []string{"x3"}}).
				Domain(tas.TopologyDomainAssignment{Count: 2, Values: []string{"x4"}}).
				Obj(),
			admissionCount: 8,
			unhealthyNode:  "x3",
			topologyRequest: &kueue.PodSetTopologyRequest{
				Required: ptr.To(tasBlockLabel),
				PodsetSliceRequiredTopologyConstraints: []kueue.PodsetSliceRequiredTopologyConstraint{
					{Topology: tasRackLabel, Size: 4},
					{Topology: corev1.LabelHostname, Size: 2},
				},
			},
			count: 8,
			// With fix: sliceSize=2, no host in r2 can fit a pair → correctly fails.
			// Without fix: sliceSize=1, scatters x4(1)+x5(1) → wrongly succeeds.
			wantReason: `topology "default" doesn't allow to fit any of 1 slice(s). Total nodes: 4; excluded: topologyDomain: 2`,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			features.SetFeatureGateDuringTest(t, features.TASMultiLayerTopology, true)
			ctx, log := utiltesting.ContextWithLog(t)

			wl := utiltestingapi.MakeWorkload("test-wl", "test-ns").
				Admission(utiltestingapi.MakeAdmission("test-cq", podSetName).
					PodSets(utiltestingapi.MakePodSetAssignment(podSetName).
						Count(tc.admissionCount).
						TopologyAssignment(tc.existingTA).
						Obj()).
					Obj()).
				UnhealthyNodes(tc.unhealthyNode).
				Obj()

			flavorTASRequests := []TASPodSetRequests{{
				PodSet: &kueue.PodSet{
					Name:            podSetName,
					TopologyRequest: tc.topologyRequest,
					Template:        corev1.PodTemplateSpec{Spec: corev1.PodSpec{}},
				},
				SinglePodRequests: resources.Requests{corev1.ResourceCPU: 1000},
				Count:             tc.count,
			}}

			initialObjects := make([]client.Object, 0, len(tc.nodes)+len(tc.pods))
			for i := range tc.nodes {
				initialObjects = append(initialObjects, &tc.nodes[i])
			}
			for i := range tc.pods {
				initialObjects = append(initialObjects, &tc.pods[i])
			}
			clientBuilder := utiltesting.NewClientBuilder()
			clientBuilder.WithObjects(initialObjects...)
			_ = tasindexer.SetupIndexes(ctx, utiltesting.AsIndexer(clientBuilder))
			c := clientBuilder.Build()

			tasCache := NewTASCache(c)
			for i := range tc.nodes {
				tasCache.SyncNode(&tc.nodes[i])
			}
			for i := range tc.pods {
				tasCache.Update(&tc.pods[i], log)
			}
			tcLevels := tc.levels
			if tcLevels == nil {
				tcLevels = defaultLevels
			}
			tasFlavorCache := tasCache.NewTASFlavorCache(
				topologyInformation{Levels: tcLevels},
				flavorInformation{TopologyName: "default"},
			)

			snapshot := tasFlavorCache.snapshot(log, tasCache.nodesCache.find(tasFlavorCache.flavor.NodeLabels, tasFlavorCache.topology.Levels))
			result := snapshot.FindTopologyAssignmentsForFlavor(flavorTASRequests, WithWorkload(wl))

			psResult, ok := result[podSetName]
			if !ok {
				t.Fatal("expected result for pod set 'main'")
			}
			if tc.wantReason != "" {
				if psResult.FailureReason != tc.wantReason {
					t.Errorf("unexpected failure reason: got %q, want %q", psResult.FailureReason, tc.wantReason)
				}
				return
			}
			if psResult.FailureReason != "" {
				t.Fatalf("unexpected failure: %s", psResult.FailureReason)
			}
			if diff := cmp.Diff(tc.wantAssignment, psResult.TopologyAssignment); diff != "" {
				t.Errorf("unexpected topology assignment (-want,+got):\n%s", diff)
			}
		})
	}
}
