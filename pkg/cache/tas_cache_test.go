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
		// TODO: remove after dropping the TASLeastAllocated feature gate
		enableTASLeastAllocated bool
		wantReason              string
		topologyRequest         kueue.PodSetTopologyRequest
		levels                  []string
		nodeLabels              map[string]string
		nodes                   []corev1.Node
		pods                    []corev1.Pod
		requests                resources.Requests
		count                   int32
		tolerations             []corev1.Toleration
		wantAssignment          *kueue.TopologyAssignment
	}{
		// TODO: remove suffixes LeastAllocated/MostAllocated after dropping the TASLeastAllocated feature gate
		"minimize the number of used racks before optimizing the number of nodes; LeastAllocated": {
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
			topologyRequest: kueue.PodSetTopologyRequest{
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
			enableTASLeastAllocated: true,
		},
		"block required; 4 pods fit into one host each; LeastAllocated": {
			nodes: binaryTreesNodes,
			topologyRequest: kueue.PodSetTopologyRequest{
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
			enableTASLeastAllocated: true,
		},
		"block required; 4 pods fit into one host each; MostAllocated": {
			nodes: binaryTreesNodes,
			topologyRequest: kueue.PodSetTopologyRequest{
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
			enableTASLeastAllocated: false,
		},
		"host required; single Pod fits in the host; LeastAllocated": {
			// TODO: remove after dropping the TASLeastAllocated feature gate
			nodes: defaultNodes,
			topologyRequest: kueue.PodSetTopologyRequest{
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
			enableTASLeastAllocated: true,
		},
		"host required; single Pod fits in the host; MostAllocated": {
			nodes: defaultNodes,
			topologyRequest: kueue.PodSetTopologyRequest{
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
			enableTASLeastAllocated: false,
		},
		"rack required; single Pod fits in a rack; LeastAllocated": {
			// TODO: remove after dropping the TASLeastAllocated feature gate
			nodes: defaultNodes,
			topologyRequest: kueue.PodSetTopologyRequest{
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
			enableTASLeastAllocated: true,
		},
		"rack required; multiple Pods fits in a rack; LeastAllocated": {
			nodes: defaultNodes,
			topologyRequest: kueue.PodSetTopologyRequest{
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
			enableTASLeastAllocated: true,
		},
		"rack required; multiple Pods fit in a rack; MostAllocated": {
			nodes: defaultNodes,
			topologyRequest: kueue.PodSetTopologyRequest{
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
			enableTASLeastAllocated: false,
		},
		"block preferred; Pods fit in 2 blocks; MostAllocated": {
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
			topologyRequest: kueue.PodSetTopologyRequest{
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
			enableTASLeastAllocated: false,
		},
		"rack required; multiple Pods fit in some racks; MostAllocated": {
			nodes: defaultNodes,
			topologyRequest: kueue.PodSetTopologyRequest{
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
			enableTASLeastAllocated: false,
		},
		"rack required; too many pods to fit in any rack; LeastAllocated": {
			nodes: defaultNodes,
			topologyRequest: kueue.PodSetTopologyRequest{
				Required: ptr.To(tasRackLabel),
			},
			levels: defaultTwoLevels,
			requests: resources.Requests{
				corev1.ResourceCPU: 1000,
			},
			count:                   4,
			wantReason:              `topology "default" allows to fit only 3 out of 4 pod(s)`,
			enableTASLeastAllocated: true,
		},
		"block required; single Pod fits in a block; LeastAllocated": {
			// TODO: remove after dropping the TASLeastAllocated feature gate
			nodes: defaultNodes,
			topologyRequest: kueue.PodSetTopologyRequest{
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
			enableTASLeastAllocated: true,
		},
		"block required; single Pod fits in a block and a single rack; MostAllocated": {
			nodes: defaultNodes,
			topologyRequest: kueue.PodSetTopologyRequest{
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
			enableTASLeastAllocated: false,
		},
		"block required; single Pod fits in a block spread across two racks; MostAllocated": {
			nodes: defaultNodes,
			topologyRequest: kueue.PodSetTopologyRequest{
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
			enableTASLeastAllocated: false,
		},
		"block required; Pods fit in a block spread across two racks; LeastAllocated": {
			nodes: defaultNodes,
			topologyRequest: kueue.PodSetTopologyRequest{
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
			enableTASLeastAllocated: true,
		},
		"block required; single Pod which cannot be split; LeastAllocated": {
			nodes: defaultNodes,
			topologyRequest: kueue.PodSetTopologyRequest{
				Required: ptr.To(tasBlockLabel),
			},
			levels: defaultTwoLevels,
			requests: resources.Requests{
				corev1.ResourceCPU: 4000,
			},
			count:                   1,
			wantReason:              `topology "default" doesn't allow to fit any of 1 pod(s)`,
			enableTASLeastAllocated: true,
		},
		"block required; too many Pods to fit requested; LeastAllocated": {
			nodes: defaultNodes,
			topologyRequest: kueue.PodSetTopologyRequest{
				Required: ptr.To(tasBlockLabel),
			},
			levels: defaultTwoLevels,
			requests: resources.Requests{
				corev1.ResourceCPU: 1000,
			},
			count:                   5,
			wantReason:              `topology "default" allows to fit only 4 out of 5 pod(s)`,
			enableTASLeastAllocated: true,
		},
		"rack required; single Pod requiring memory; LeastAllocated": {
			// TODO: remove after dropping the TASLeastAllocated feature gate
			nodes: defaultNodes,
			topologyRequest: kueue.PodSetTopologyRequest{
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
			enableTASLeastAllocated: true,
		},
		"rack preferred; but only block can accommodate the workload; LeastAllocated": {
			nodes: defaultNodes,
			topologyRequest: kueue.PodSetTopologyRequest{
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
			enableTASLeastAllocated: true,
		},
		"rack preferred; but only multiple blocks can accommodate the workload; LeastAllocated": {
			nodes: defaultNodes,
			topologyRequest: kueue.PodSetTopologyRequest{
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
			enableTASLeastAllocated: true,
		},
		"block preferred; but only multiple blocks can accommodate the workload; LeastAllocated": {
			nodes: defaultNodes,
			topologyRequest: kueue.PodSetTopologyRequest{
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
			enableTASLeastAllocated: true,
		},
		"block preferred; but the workload cannot be accommodate in entire topology; LeastAllocated": {
			nodes: defaultNodes,
			topologyRequest: kueue.PodSetTopologyRequest{
				Preferred: ptr.To(tasBlockLabel),
			},
			levels: defaultTwoLevels,
			requests: resources.Requests{
				corev1.ResourceCPU: 1000,
			},
			count:                   10,
			wantReason:              `topology "default" allows to fit only 7 out of 10 pod(s)`,
			enableTASLeastAllocated: true,
		},
		"only nodes with matching labels are considered; no matching node; LeastAllocated": {
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
			topologyRequest: kueue.PodSetTopologyRequest{
				Required: ptr.To(corev1.LabelHostname),
			},
			nodeLabels: map[string]string{
				"zone": "zone-b",
			},
			levels: defaultOneLevel,
			requests: resources.Requests{
				corev1.ResourceCPU: 1000,
			},
			count:                   1,
			wantReason:              "no topology domains at level: kubernetes.io/hostname",
			enableTASLeastAllocated: true,
		},
		"only nodes with matching labels are considered; matching node is found; LeastAllocated": {
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
			topologyRequest: kueue.PodSetTopologyRequest{
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
			enableTASLeastAllocated: true,
		},
		"only nodes with matching levels are considered; no host label on node; LeastAllocated": {
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
			topologyRequest: kueue.PodSetTopologyRequest{
				Required: ptr.To(tasRackLabel),
			},
			levels: defaultThreeLevels,
			requests: resources.Requests{
				corev1.ResourceCPU: 1000,
			},
			count:                   1,
			wantReason:              "no topology domains at level: cloud.com/topology-rack",
			enableTASLeastAllocated: true,
		},
		"don't consider unscheduled Pods when computing capacity; LeastAllocated": {
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
			topologyRequest: kueue.PodSetTopologyRequest{
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
			enableTASLeastAllocated: true,
		},
		"don't consider terminal pods when computing the capacity; LeastAllocated": {
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
			topologyRequest: kueue.PodSetTopologyRequest{
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
			enableTASLeastAllocated: true,
		},
		"include usage from pending scheduled non-TAS pods, blocked assignment; LeastAllocated": {
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
			topologyRequest: kueue.PodSetTopologyRequest{
				Required: ptr.To(corev1.LabelHostname),
			},
			levels: defaultOneLevel,
			requests: resources.Requests{
				corev1.ResourceCPU: 600,
			},
			count:                   1,
			wantReason:              `topology "default" doesn't allow to fit any of 1 pod(s)`,
			enableTASLeastAllocated: true,
		},
		"include usage from running non-TAS pods, blocked assignment; LeastAllocated": {
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
			topologyRequest: kueue.PodSetTopologyRequest{
				Required: ptr.To(corev1.LabelHostname),
			},
			levels: defaultOneLevel,
			requests: resources.Requests{
				corev1.ResourceCPU: 600,
			},
			count:                   1,
			wantReason:              `topology "default" doesn't allow to fit any of 1 pod(s)`,
			enableTASLeastAllocated: true,
		},
		"include usage from running non-TAS pods, found free capacity on another node; LeastAllocated": {
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
			topologyRequest: kueue.PodSetTopologyRequest{
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
			enableTASLeastAllocated: true,
		},
		"no assignment as node is not ready; LeastAllocated": {
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
			topologyRequest: kueue.PodSetTopologyRequest{
				Required: ptr.To(corev1.LabelHostname),
			},
			nodeLabels: map[string]string{
				"zone": "zone-a",
			},
			levels: defaultOneLevel,
			requests: resources.Requests{
				corev1.ResourceCPU: 1000,
			},
			count:                   1,
			wantReason:              "no topology domains at level: kubernetes.io/hostname",
			enableTASLeastAllocated: true,
		},
		"no assignment as node is unschedulable; LeastAllocated": {
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
			topologyRequest: kueue.PodSetTopologyRequest{
				Required: ptr.To(corev1.LabelHostname),
			},
			nodeLabels: map[string]string{
				"zone": "zone-a",
			},
			levels: defaultOneLevel,
			requests: resources.Requests{
				corev1.ResourceCPU: 1000,
			},
			count:                   1,
			wantReason:              "no topology domains at level: kubernetes.io/hostname",
			enableTASLeastAllocated: true,
		},
		"skip node which has untolerated taint; LeastAllocated": {
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
			topologyRequest: kueue.PodSetTopologyRequest{
				Required: ptr.To(corev1.LabelHostname),
			},
			nodeLabels: map[string]string{
				"zone": "zone-a",
			},
			levels: defaultOneLevel,
			requests: resources.Requests{
				corev1.ResourceCPU: 1000,
			},
			count:                   1,
			wantReason:              `topology "default" doesn't allow to fit any of 1 pod(s)`,
			enableTASLeastAllocated: true,
		},
		"allow to schedule on node with tolerated taint; LeastAllocated": {
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
			topologyRequest: kueue.PodSetTopologyRequest{
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
			enableTASLeastAllocated: true,
		},
		"no assignment as node does not have enough allocatable pods (.status.allocatable['pods']); LeastAllocated": {
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
			topologyRequest: kueue.PodSetTopologyRequest{
				Required: ptr.To(corev1.LabelHostname),
			},
			nodeLabels: map[string]string{
				"zone": "zone-a",
			},
			levels: defaultOneLevel,
			requests: resources.Requests{
				corev1.ResourceCPU: 300,
			},
			count:                   1,
			wantReason:              `topology "default" doesn't allow to fit any of 1 pod(s)`,
			enableTASLeastAllocated: true,
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)
			// TODO: remove after dropping the TASLeastAllocated feature gate
			features.SetFeatureGateDuringTest(t, features.TASLeastAllocated, tc.enableTASLeastAllocated)

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
			tasFlavorCache := tasCache.NewTASFlavorCache("default", tc.levels, tc.nodeLabels, tc.tolerations)

			snapshot, err := tasFlavorCache.snapshot(ctx)
			if err != nil {
				t.Fatalf("failed to build the snapshot: %v", err)
			}
			tasInput := TASPodSetRequests{
				PodSet: &kueue.PodSet{
					Name:            "main",
					TopologyRequest: &tc.topologyRequest,
					Template: corev1.PodTemplateSpec{
						Spec: corev1.PodSpec{
							Tolerations: tc.tolerations,
						},
					},
				},
				SinglePodRequests: tc.requests,
				Count:             tc.count,
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
			wantResult["main"] = wantMainPodSetResult
			gotResult := snapshot.FindTopologyAssignmentsForFlavor(flavorTASRequests, false)
			if diff := cmp.Diff(wantResult, gotResult); diff != "" {
				t.Errorf("unexpected topology assignment (-want,+got): %s", diff)
			}
		})
	}
}
