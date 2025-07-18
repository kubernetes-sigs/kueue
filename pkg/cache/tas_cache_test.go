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

// TestFindTopologyAssignments is a unified and extensible test for topology assignment logic.
// It supports any number of pod sets and merges all cases from previous tests.
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
	type podSetCase struct {
		name            kueue.PodSetReference
		topologyRequest *kueue.PodSetTopologyRequest
		requests        resources.Requests
		count           int32
		tolerations     []corev1.Toleration
		nodeSelector    map[string]string
		wantAssignment  *kueue.TopologyAssignment
		wantReason      string
	}

	type testCase struct {
		desc               string
		enableFeatureGates []featuregate.Feature
		nodes              []corev1.Node
		pods               []corev1.Pod
		levels             []string
		nodeLabels         map[string]string
		podSets            []podSetCase
	}

	// Helper to build a snapshot for the test
	buildSnapshot := func(ctx context.Context, t *testing.T, nodes []corev1.Node, pods []corev1.Pod, levels []string, nodeLabels map[string]string, tolerations []corev1.Toleration) *TASFlavorSnapshot {
		initialObjects := make([]client.Object, 0)
		for i := range nodes {
			initialObjects = append(initialObjects, &nodes[i])
		}
		for i := range pods {
			initialObjects = append(initialObjects, &pods[i])
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
			NodeLabels:   nodeLabels,
			Tolerations:  tolerations,
		}
		tasFlavorCache := tasCache.NewTASFlavorCache(topologyInformation, flavorInformation)

		snapshot, err := tasFlavorCache.snapshot(ctx)
		if err != nil {
			t.Fatalf("failed to build the snapshot: %v", err)
		}
		return snapshot
	}

	// Helper to build TASPodSetRequests for a pod set
	buildTASInput := func(ps podSetCase) TASPodSetRequests {
		return TASPodSetRequests{
			PodSet: &kueue.PodSet{
				Name:            ps.name,
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
	}

	// Helper to build expected result for a pod set
	buildWantedResult := func(wantAssignment *kueue.TopologyAssignment, wantReason string) tasPodSetAssignmentResult {
		wantPodSetResult := tasPodSetAssignmentResult{
			FailureReason: wantReason,
		}
		if wantAssignment != nil {
			// Sort domains for deterministic comparison
			sort.Slice(wantAssignment.Domains, func(i, j int) bool {
				return utiltas.DomainID(wantAssignment.Domains[i].Values) < utiltas.DomainID(wantAssignment.Domains[j].Values)
			})
			wantPodSetResult.TopologyAssignment = wantAssignment
		}
		return wantPodSetResult
	}

	// All test cases (migrated and extensible)
	testCases := []testCase{
		// 1. minimize the number of used racks before optimizing the number of nodes; BestFit
		{
			desc: "minimize the number of used racks before optimizing the number of nodes; BestFit",
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
			podSets: []podSetCase{
				{
					name: "default",
					topologyRequest: &kueue.PodSetTopologyRequest{
						Required: ptr.To(tasBlockLabel),
					},
					requests: resources.Requests{
						corev1.ResourceCPU: 1000,
					},
					count: 4,
					wantAssignment: &kueue.TopologyAssignment{
						Levels: []string{corev1.LabelHostname},
						Domains: []kueue.TopologyDomainAssignment{
							{Count: 1, Values: []string{"x3"}},
							{Count: 1, Values: []string{"x4"}},
							{Count: 1, Values: []string{"x5"}},
							{Count: 1, Values: []string{"x6"}},
						},
					},
				},
			},
		},
		// 2. minimize resource fragmentation; LeastFreeCapacityFit
		{
			desc:               "minimize resource fragmentation; LeastFreeCapacityFit",
			enableFeatureGates: []featuregate.Feature{features.TASProfileLeastFreeCapacity},
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
			podSets: []podSetCase{
				{
					name: "default",
					topologyRequest: &kueue.PodSetTopologyRequest{
						Required: ptr.To(tasBlockLabel),
					},
					requests: resources.Requests{
						corev1.ResourceCPU: 1000,
					},
					count: 2,
					wantAssignment: &kueue.TopologyAssignment{
						Levels: []string{corev1.LabelHostname},
						Domains: []kueue.TopologyDomainAssignment{
							{Count: 1, Values: []string{"x3"}},
							{Count: 1, Values: []string{"x2"}},
						},
					},
				},
			},
		},
		// 3. choose the node that can accommodate all Pods
		{
			desc: "choose the node that can accommodate all Pods",
			nodes: []corev1.Node{
				*testingnode.MakeNode("b1-r1-x1").
					Label("cloud.com/topology-block", "b1").
					Label("cloud.com/topology-rack", "r1").
					Label(corev1.LabelHostname, "x1").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("2"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r1-x2").
					Label("cloud.com/topology-block", "b1").
					Label("cloud.com/topology-rack", "r1").
					Label(corev1.LabelHostname, "x2").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("1"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r1-x3").
					Label("cloud.com/topology-block", "b1").
					Label("cloud.com/topology-rack", "r1").
					Label(corev1.LabelHostname, "x3").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("1"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
			},
			levels: []string{"cloud.com/topology-block", "cloud.com/topology-rack", corev1.LabelHostname},
			podSets: []podSetCase{
				{
					name: "default",
					topologyRequest: &kueue.PodSetTopologyRequest{
						Required: ptr.To("cloud.com/topology-block"),
					},
					requests: resources.Requests{
						corev1.ResourceCPU: 1000,
					},
					count: 2,
					wantAssignment: &kueue.TopologyAssignment{
						Levels: []string{corev1.LabelHostname},
						Domains: []kueue.TopologyDomainAssignment{
							{Count: 2, Values: []string{"x1"}},
						},
					},
				},
			},
		},
		// 4. unconstrained; 2 pods fit into hosts scattered across the whole datacenter even they could fit into single rack; LeastFreeCapacity
		{
			desc:               "unconstrained; 2 pods fit into hosts scattered across the whole datacenter even they could fit into single rack; LeastFreeCapacity",
			enableFeatureGates: []featuregate.Feature{features.TASProfileLeastFreeCapacity},
			nodes:              scatteredNodes,
			levels:             defaultThreeLevels,
			podSets: []podSetCase{
				{
					name: "default",
					topologyRequest: &kueue.PodSetTopologyRequest{
						Unconstrained: ptr.To(true),
					},
					requests: resources.Requests{
						corev1.ResourceCPU: 1000,
					},
					count: 2,
					wantAssignment: &kueue.TopologyAssignment{
						Levels: []string{corev1.LabelHostname},
						Domains: []kueue.TopologyDomainAssignment{
							{Count: 2, Values: []string{"x4"}},
						},
					},
				},
			},
		},
		// 5. constrained; 2 pods fit into hosts scattered across the whole datacenter even they could fit into single rack; LeastFreeCapacity
		{
			desc:               "constrained; 2 pods fit into hosts scattered across the whole datacenter even they could fit into single rack; LeastFreeCapacity",
			enableFeatureGates: []featuregate.Feature{features.TASProfileLeastFreeCapacity},
			nodes: []corev1.Node{
				*testingnode.MakeNode("b1-r1-x1").
					Label("cloud.com/topology-block", "b1").
					Label("cloud.com/topology-rack", "r1").
					Label(corev1.LabelHostname, "x1").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("4"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
						corev1.ResourcePods:   resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r1-x2").
					Label("cloud.com/topology-block", "b1").
					Label("cloud.com/topology-rack", "r1").
					Label(corev1.LabelHostname, "x2").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
						corev1.ResourcePods:   resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r1-x3").
					Label("cloud.com/topology-block", "b1").
					Label("cloud.com/topology-rack", "r1").
					Label(corev1.LabelHostname, "x3").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
						corev1.ResourcePods:   resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b2-r1-x4").
					Label("cloud.com/topology-block", "b2").
					Label("cloud.com/topology-rack", "r1").
					Label(corev1.LabelHostname, "x4").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("2"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
						corev1.ResourcePods:   resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b2-r1-x5").
					Label("cloud.com/topology-block", "b2").
					Label("cloud.com/topology-rack", "r1").
					Label(corev1.LabelHostname, "x5").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
						corev1.ResourcePods:   resource.MustParse("10"),
					}).
					Ready().
					Obj(),
			},
			levels: []string{"cloud.com/topology-block", "cloud.com/topology-rack", corev1.LabelHostname},
			podSets: []podSetCase{
				{
					name: "default",
					topologyRequest: &kueue.PodSetTopologyRequest{
						Unconstrained: ptr.To(true),
					},
					requests: resources.Requests{
						corev1.ResourceCPU: 1000,
					},
					count: 2,
					wantAssignment: &kueue.TopologyAssignment{
						Levels: []string{corev1.LabelHostname},
						Domains: []kueue.TopologyDomainAssignment{
							{Count: 2, Values: []string{"x4"}},
						},
					},
				},
			},
		},
		// 6. constrained; 2 pods fit into hosts scattered across the whole datacenter even they could fit into single rack; LeastFreeCapacity
		{
			desc:               "constrained; 2 pods fit into hosts scattered across the whole datacenter even they could fit into single rack; LeastFreeCapacity",
			enableFeatureGates: []featuregate.Feature{features.TASProfileLeastFreeCapacity},
			nodes: []corev1.Node{
				*testingnode.MakeNode("b1-r1-x1").
					Label("cloud.com/topology-block", "b1").
					Label("cloud.com/topology-rack", "r1").
					Label(corev1.LabelHostname, "x1").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("4"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
						corev1.ResourcePods:   resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r1-x2").
					Label("cloud.com/topology-block", "b1").
					Label("cloud.com/topology-rack", "r1").
					Label(corev1.LabelHostname, "x2").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
						corev1.ResourcePods:   resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r1-x3").
					Label("cloud.com/topology-block", "b1").
					Label("cloud.com/topology-rack", "r1").
					Label(corev1.LabelHostname, "x3").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
						corev1.ResourcePods:   resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b2-r1-x4").
					Label("cloud.com/topology-block", "b2").
					Label("cloud.com/topology-rack", "r1").
					Label(corev1.LabelHostname, "x4").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("2"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
						corev1.ResourcePods:   resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b2-r1-x5").
					Label("cloud.com/topology-block", "b2").
					Label("cloud.com/topology-rack", "r1").
					Label(corev1.LabelHostname, "x5").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
						corev1.ResourcePods:   resource.MustParse("10"),
					}).
					Ready().
					Obj(),
			},
			levels: []string{"cloud.com/topology-block", "cloud.com/topology-rack", corev1.LabelHostname},
			podSets: []podSetCase{
				{
					name: "default",
					topologyRequest: &kueue.PodSetTopologyRequest{
						Unconstrained: ptr.To(true),
					},
					requests: resources.Requests{
						corev1.ResourceCPU: 1000,
					},
					count: 2,
					wantAssignment: &kueue.TopologyAssignment{
						Levels: []string{corev1.LabelHostname},
						Domains: []kueue.TopologyDomainAssignment{
							{Count: 2, Values: []string{"x4"}},
						},
					},
				},
			},
		},
		// 7. constrained; 2 pods fit into hosts scattered across the whole datacenter even they could fit into single rack; LeastFreeCapacity
		{
			desc:               "constrained; 2 pods fit into hosts scattered across the whole datacenter even they could fit into single rack; LeastFreeCapacity",
			enableFeatureGates: []featuregate.Feature{features.TASProfileLeastFreeCapacity},
			nodes: []corev1.Node{
				*testingnode.MakeNode("b1-r1-x1").
					Label("cloud.com/topology-block", "b1").
					Label("cloud.com/topology-rack", "r1").
					Label(corev1.LabelHostname, "x1").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("4"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
						corev1.ResourcePods:   resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r1-x2").
					Label("cloud.com/topology-block", "b1").
					Label("cloud.com/topology-rack", "r1").
					Label(corev1.LabelHostname, "x2").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
						corev1.ResourcePods:   resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r1-x3").
					Label("cloud.com/topology-block", "b1").
					Label("cloud.com/topology-rack", "r1").
					Label(corev1.LabelHostname, "x3").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
						corev1.ResourcePods:   resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b2-r1-x4").
					Label("cloud.com/topology-block", "b2").
					Label("cloud.com/topology-rack", "r1").
					Label(corev1.LabelHostname, "x4").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("2"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
						corev1.ResourcePods:   resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b2-r1-x5").
					Label("cloud.com/topology-block", "b2").
					Label("cloud.com/topology-rack", "r1").
					Label(corev1.LabelHostname, "x5").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
						corev1.ResourcePods:   resource.MustParse("10"),
					}).
					Ready().
					Obj(),
			},
			levels: []string{"cloud.com/topology-block", "cloud.com/topology-rack", corev1.LabelHostname},
			podSets: []podSetCase{
				{
					name: "default",
					topologyRequest: &kueue.PodSetTopologyRequest{
						Unconstrained: ptr.To(true),
					},
					requests: resources.Requests{
						corev1.ResourceCPU: 1000,
					},
					count: 2,
					wantAssignment: &kueue.TopologyAssignment{
						Levels: []string{corev1.LabelHostname},
						Domains: []kueue.TopologyDomainAssignment{
							{Count: 2, Values: []string{"x4"}},
						},
					},
				},
			},
		},
		// 8. constrained; 2 pods fit into hosts scattered across the whole datacenter even they could fit into single rack; LeastFreeCapacity
		{
			desc:               "constrained; 2 pods fit into hosts scattered across the whole datacenter even they could fit into single rack; LeastFreeCapacity",
			enableFeatureGates: []featuregate.Feature{features.TASProfileLeastFreeCapacity},
			nodes: []corev1.Node{
				*testingnode.MakeNode("b1-r1-x1").
					Label("cloud.com/topology-block", "b1").
					Label("cloud.com/topology-rack", "r1").
					Label(corev1.LabelHostname, "x1").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("4"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
						corev1.ResourcePods:   resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r1-x2").
					Label("cloud.com/topology-block", "b1").
					Label("cloud.com/topology-rack", "r1").
					Label(corev1.LabelHostname, "x2").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
						corev1.ResourcePods:   resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r1-x3").
					Label("cloud.com/topology-block", "b1").
					Label("cloud.com/topology-rack", "r1").
					Label(corev1.LabelHostname, "x3").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
						corev1.ResourcePods:   resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b2-r1-x4").
					Label("cloud.com/topology-block", "b2").
					Label("cloud.com/topology-rack", "r1").
					Label(corev1.LabelHostname, "x4").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("2"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
						corev1.ResourcePods:   resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b2-r1-x5").
					Label("cloud.com/topology-block", "b2").
					Label("cloud.com/topology-rack", "r1").
					Label(corev1.LabelHostname, "x5").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
						corev1.ResourcePods:   resource.MustParse("10"),
					}).
					Ready().
					Obj(),
			},
			levels: []string{"cloud.com/topology-block", "cloud.com/topology-rack", corev1.LabelHostname},
			podSets: []podSetCase{
				{
					name: "default",
					topologyRequest: &kueue.PodSetTopologyRequest{
						Unconstrained: ptr.To(true),
					},
					requests: resources.Requests{
						corev1.ResourceCPU: 1000,
					},
					count: 2,
					wantAssignment: &kueue.TopologyAssignment{
						Levels: []string{corev1.LabelHostname},
						Domains: []kueue.TopologyDomainAssignment{
							{Count: 2, Values: []string{"x4"}},
						},
					},
				},
			},
		},
		// 5. no annotation; implied default to unconstrained; 6 pods fit into hosts scattered across the whole datacenter even they could fit into single rack; BestFit
		{
			desc:   "no annotation; implied default to unconstrained; 6 pods fit into hosts scattered across the whole datacenter even they could fit into single rack; BestFit",
			nodes:  scatteredNodes,
			levels: defaultThreeLevels,
			podSets: []podSetCase{
				{
					name: "default",
					requests: resources.Requests{
						corev1.ResourceCPU: 1000,
					},
					count: 6,
					wantAssignment: &kueue.TopologyAssignment{
						Levels: defaultOneLevel,
						Domains: []kueue.TopologyDomainAssignment{
							{Count: 4, Values: []string{"x1"}},
							{Count: 2, Values: []string{"x4"}},
						},
					},
				},
			},
		},
		// 6. unconstrained; 6 pods fit into hosts scattered across the whole datacenter even they could fit into single rack; BestFit
		{
			desc:   "unconstrained; 6 pods fit into hosts scattered across the whole datacenter even they could fit into single rack; BestFit",
			nodes:  scatteredNodes,
			levels: defaultThreeLevels,
			podSets: []podSetCase{
				{
					name: "default",
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
							{Count: 4, Values: []string{"x1"}},
							{Count: 2, Values: []string{"x4"}},
						},
					},
				},
			},
		},
		// 7. unconstrained; a single pod fits into each host; BestFit
		{
			desc: "unconstrained; a single pod fits into each host; BestFit",
			nodes: []corev1.Node{
				*testingnode.MakeNode("b1-r1-x1").
					Label("cloud.com/topology-block", "b1").
					Label("cloud.com/topology-rack", "r1").
					Label(corev1.LabelHostname, "x1").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
						corev1.ResourcePods:   resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r2-x2").
					Label("cloud.com/topology-block", "b1").
					Label("cloud.com/topology-rack", "r2").
					Label(corev1.LabelHostname, "x2").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
						corev1.ResourcePods:   resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r2-x3").
					Label("cloud.com/topology-block", "b1").
					Label("cloud.com/topology-rack", "r2").
					Label(corev1.LabelHostname, "x3").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
						corev1.ResourcePods:   resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r2-x4").
					Label("cloud.com/topology-block", "b1").
					Label("cloud.com/topology-rack", "r2").
					Label(corev1.LabelHostname, "x4").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
						corev1.ResourcePods:   resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b2-r2-x5").
					Label("cloud.com/topology-block", "b2").
					Label("cloud.com/topology-rack", "r1").
					Label(corev1.LabelHostname, "x5").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
						corev1.ResourcePods:   resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b2-r2-x6").
					Label("cloud.com/topology-block", "b2").
					Label("cloud.com/topology-rack", "r2").
					Label(corev1.LabelHostname, "x6").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("2"),
						corev1.ResourceMemory: resource.MustParse("4Gi"),
						corev1.ResourcePods:   resource.MustParse("40"),
					}).
					Ready().
					Obj(),
			},
			levels: []string{"cloud.com/topology-block", "cloud.com/topology-rack", corev1.LabelHostname},
			podSets: []podSetCase{
				{
					name: "default",
					topologyRequest: &kueue.PodSetTopologyRequest{
						Unconstrained: ptr.To(true),
					},
					requests: resources.Requests{
						corev1.ResourceCPU: 1000,
					},
					count: 1,
					wantAssignment: &kueue.TopologyAssignment{
						Levels: []string{corev1.LabelHostname},
						Domains: []kueue.TopologyDomainAssignment{
							{Count: 1, Values: []string{"x1"}},
						},
					},
				},
			},
		},
		// 8. unconstrained; a single pod fits into each host; LeastFreeCapacity; TASProfileLeastFreeCapacity
		{
			desc:               "unconstrained; a single pod fits into each host; LeastFreeCapacity; TASProfileLeastFreeCapacity",
			enableFeatureGates: []featuregate.Feature{features.TASProfileLeastFreeCapacity},
			nodes: []corev1.Node{
				*testingnode.MakeNode("b1-r1-x1").
					Label("cloud.com/topology-block", "b1").
					Label("cloud.com/topology-rack", "r1").
					Label(corev1.LabelHostname, "x1").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
						corev1.ResourcePods:   resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r2-x2").
					Label("cloud.com/topology-block", "b1").
					Label("cloud.com/topology-rack", "r2").
					Label(corev1.LabelHostname, "x2").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
						corev1.ResourcePods:   resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r2-x3").
					Label("cloud.com/topology-block", "b1").
					Label("cloud.com/topology-rack", "r2").
					Label(corev1.LabelHostname, "x3").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
						corev1.ResourcePods:   resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r2-x4").
					Label("cloud.com/topology-block", "b1").
					Label("cloud.com/topology-rack", "r2").
					Label(corev1.LabelHostname, "x4").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
						corev1.ResourcePods:   resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b2-r2-x5").
					Label("cloud.com/topology-block", "b2").
					Label("cloud.com/topology-rack", "r1").
					Label(corev1.LabelHostname, "x5").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
						corev1.ResourcePods:   resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b2-r2-x6").
					Label("cloud.com/topology-block", "b2").
					Label("cloud.com/topology-rack", "r2").
					Label(corev1.LabelHostname, "x6").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("2"),
						corev1.ResourceMemory: resource.MustParse("4Gi"),
						corev1.ResourcePods:   resource.MustParse("40"),
					}).
					Ready().
					Obj(),
			},
			levels: []string{"cloud.com/topology-block", "cloud.com/topology-rack", corev1.LabelHostname},
			podSets: []podSetCase{
				{
					name: "default",
					topologyRequest: &kueue.PodSetTopologyRequest{
						Unconstrained: ptr.To(true),
					},
					requests: resources.Requests{
						corev1.ResourceCPU: 1000,
					},
					count: 1,
					wantAssignment: &kueue.TopologyAssignment{
						Levels: []string{corev1.LabelHostname},
						Domains: []kueue.TopologyDomainAssignment{
							{Count: 1, Values: []string{"x1"}},
						},
					},
				},
			},
		},
		// 9. unconstrained; a single pod fits into each host; LeastFreeCapacity; TASProfileMixed
		{
			desc:               "unconstrained; a single pod fits into each host; LeastFreeCapacity; TASProfileMixed",
			enableFeatureGates: []featuregate.Feature{features.TASProfileMixed},
			nodes: []corev1.Node{
				*testingnode.MakeNode("b1-r1-x1").
					Label("cloud.com/topology-block", "b1").
					Label("cloud.com/topology-rack", "r1").
					Label(corev1.LabelHostname, "x1").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
						corev1.ResourcePods:   resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r2-x2").
					Label("cloud.com/topology-block", "b1").
					Label("cloud.com/topology-rack", "r2").
					Label(corev1.LabelHostname, "x2").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
						corev1.ResourcePods:   resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r2-x3").
					Label("cloud.com/topology-block", "b1").
					Label("cloud.com/topology-rack", "r2").
					Label(corev1.LabelHostname, "x3").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
						corev1.ResourcePods:   resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r2-x4").
					Label("cloud.com/topology-block", "b1").
					Label("cloud.com/topology-rack", "r2").
					Label(corev1.LabelHostname, "x4").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
						corev1.ResourcePods:   resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b2-r2-x5").
					Label("cloud.com/topology-block", "b2").
					Label("cloud.com/topology-rack", "r1").
					Label(corev1.LabelHostname, "x5").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
						corev1.ResourcePods:   resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b2-r2-x6").
					Label("cloud.com/topology-block", "b2").
					Label("cloud.com/topology-rack", "r2").
					Label(corev1.LabelHostname, "x6").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("2"),
						corev1.ResourceMemory: resource.MustParse("4Gi"),
						corev1.ResourcePods:   resource.MustParse("40"),
					}).
					Ready().
					Obj(),
			},
			levels: []string{"cloud.com/topology-block", "cloud.com/topology-rack", corev1.LabelHostname},
			podSets: []podSetCase{
				{
					name: "default",
					topologyRequest: &kueue.PodSetTopologyRequest{
						Unconstrained: ptr.To(true),
					},
					requests: resources.Requests{
						corev1.ResourceCPU: 1000,
					},
					count: 1,
					wantAssignment: &kueue.TopologyAssignment{
						Levels: []string{corev1.LabelHostname},
						Domains: []kueue.TopologyDomainAssignment{
							{Count: 1, Values: []string{"x1"}},
						},
					},
				},
			},
		},
		// 10. block required; 4 pods fit into one host each; BestFit
		{
			desc:   "block required; 4 pods fit into one host each; BestFit",
			nodes:  binaryTreesNodes,
			levels: defaultThreeLevels,
			podSets: []podSetCase{
				{
					name: "default",
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
							{Count: 1, Values: []string{"x1"}},
							{Count: 1, Values: []string{"x2"}},
							{Count: 1, Values: []string{"x3"}},
							{Count: 1, Values: []string{"x4"}},
						},
					},
				},
			},
		},
		// 11. host required; single Pod fits in the host; LeastFreeCapacityFit
		{
			desc:               "host required; single Pod fits in the host; LeastFreeCapacityFit",
			enableFeatureGates: []featuregate.Feature{features.TASProfileLeastFreeCapacity},
			nodes: []corev1.Node{
				*testingnode.MakeNode("b1-r1-x1").
					Label("cloud.com/topology-block", "b1").
					Label("cloud.com/topology-rack", "r1").
					Label(corev1.LabelHostname, "x1").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
						corev1.ResourcePods:   resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r2-x2").
					Label("cloud.com/topology-block", "b1").
					Label("cloud.com/topology-rack", "r2").
					Label(corev1.LabelHostname, "x2").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
						corev1.ResourcePods:   resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r2-x3").
					Label("cloud.com/topology-block", "b1").
					Label("cloud.com/topology-rack", "r2").
					Label(corev1.LabelHostname, "x3").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
						corev1.ResourcePods:   resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r2-x4").
					Label("cloud.com/topology-block", "b1").
					Label("cloud.com/topology-rack", "r2").
					Label(corev1.LabelHostname, "x4").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
						corev1.ResourcePods:   resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b2-r2-x5").
					Label("cloud.com/topology-block", "b2").
					Label("cloud.com/topology-rack", "r1").
					Label(corev1.LabelHostname, "x5").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
						corev1.ResourcePods:   resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b2-r2-x6").
					Label("cloud.com/topology-block", "b2").
					Label("cloud.com/topology-rack", "r2").
					Label(corev1.LabelHostname, "x6").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("2"),
						corev1.ResourceMemory: resource.MustParse("4Gi"),
						corev1.ResourcePods:   resource.MustParse("40"),
					}).
					Ready().
					Obj(),
			},
			levels: []string{"cloud.com/topology-block", "cloud.com/topology-rack", corev1.LabelHostname},
			podSets: []podSetCase{
				{
					name: "default",
					topologyRequest: &kueue.PodSetTopologyRequest{
						Required: ptr.To(corev1.LabelHostname),
					},
					requests: resources.Requests{
						corev1.ResourceCPU: 1000,
					},
					count: 1,
					wantAssignment: &kueue.TopologyAssignment{
						Levels: []string{corev1.LabelHostname},
						Domains: []kueue.TopologyDomainAssignment{
							{Count: 1, Values: []string{"x1"}},
						},
					},
				},
			},
		},
		// 12. host required; single Pod fits in the host; BestFit
		{
			desc: "host required; single Pod fits in the host; BestFit",
			nodes: []corev1.Node{
				*testingnode.MakeNode("b1-r1-x1").
					Label("cloud.com/topology-block", "b1").
					Label("cloud.com/topology-rack", "r1").
					Label(corev1.LabelHostname, "x1").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
						corev1.ResourcePods:   resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r2-x2").
					Label("cloud.com/topology-block", "b1").
					Label("cloud.com/topology-rack", "r2").
					Label(corev1.LabelHostname, "x2").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
						corev1.ResourcePods:   resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r2-x3").
					Label("cloud.com/topology-block", "b1").
					Label("cloud.com/topology-rack", "r2").
					Label(corev1.LabelHostname, "x3").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
						corev1.ResourcePods:   resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r2-x4").
					Label("cloud.com/topology-block", "b1").
					Label("cloud.com/topology-rack", "r2").
					Label(corev1.LabelHostname, "x4").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
						corev1.ResourcePods:   resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b2-r2-x5").
					Label("cloud.com/topology-block", "b2").
					Label("cloud.com/topology-rack", "r1").
					Label(corev1.LabelHostname, "x5").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
						corev1.ResourcePods:   resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b2-r2-x6").
					Label("cloud.com/topology-block", "b2").
					Label("cloud.com/topology-rack", "r2").
					Label(corev1.LabelHostname, "x6").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("2"),
						corev1.ResourceMemory: resource.MustParse("4Gi"),
						corev1.ResourcePods:   resource.MustParse("40"),
					}).
					Ready().
					Obj(),
			},
			levels: []string{"cloud.com/topology-block", "cloud.com/topology-rack", corev1.LabelHostname},
			podSets: []podSetCase{
				{
					name: "default",
					topologyRequest: &kueue.PodSetTopologyRequest{
						Required: ptr.To(corev1.LabelHostname),
					},
					requests: resources.Requests{
						corev1.ResourceCPU: 1000,
					},
					count: 1,
					wantAssignment: &kueue.TopologyAssignment{
						Levels: []string{corev1.LabelHostname},
						Domains: []kueue.TopologyDomainAssignment{
							{Count: 1, Values: []string{"x1"}},
						},
					},
				},
			},
		},
		// 13. host required; single Pod fits in the host; BestFit; TASProfileMixed
		{
			desc:               "host required; single Pod fits in the host; BestFit; TASProfileMixed",
			enableFeatureGates: []featuregate.Feature{features.TASProfileMixed},
			nodes: []corev1.Node{
				*testingnode.MakeNode("b1-r1-x1").
					Label("cloud.com/topology-block", "b1").
					Label("cloud.com/topology-rack", "r1").
					Label(corev1.LabelHostname, "x1").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
						corev1.ResourcePods:   resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r2-x2").
					Label("cloud.com/topology-block", "b1").
					Label("cloud.com/topology-rack", "r2").
					Label(corev1.LabelHostname, "x2").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
						corev1.ResourcePods:   resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r2-x3").
					Label("cloud.com/topology-block", "b1").
					Label("cloud.com/topology-rack", "r2").
					Label(corev1.LabelHostname, "x3").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
						corev1.ResourcePods:   resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r2-x4").
					Label("cloud.com/topology-block", "b1").
					Label("cloud.com/topology-rack", "r2").
					Label(corev1.LabelHostname, "x4").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
						corev1.ResourcePods:   resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b2-r2-x5").
					Label("cloud.com/topology-block", "b2").
					Label("cloud.com/topology-rack", "r1").
					Label(corev1.LabelHostname, "x5").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
						corev1.ResourcePods:   resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b2-r2-x6").
					Label("cloud.com/topology-block", "b2").
					Label("cloud.com/topology-rack", "r2").
					Label(corev1.LabelHostname, "x6").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("2"),
						corev1.ResourceMemory: resource.MustParse("4Gi"),
						corev1.ResourcePods:   resource.MustParse("40"),
					}).
					Ready().
					Obj(),
			},
			levels: []string{"cloud.com/topology-block", "cloud.com/topology-rack", corev1.LabelHostname},
			podSets: []podSetCase{
				{
					name: "default",
					topologyRequest: &kueue.PodSetTopologyRequest{
						Required: ptr.To(corev1.LabelHostname),
					},
					requests: resources.Requests{
						corev1.ResourceCPU: 1000,
					},
					count: 1,
					wantAssignment: &kueue.TopologyAssignment{
						Levels: []string{corev1.LabelHostname},
						Domains: []kueue.TopologyDomainAssignment{
							{Count: 1, Values: []string{"x1"}},
						},
					},
				},
			},
		},
		// 14. host required; single Pod fits in the largest host; LeastFreeCapacityFit
		{
			desc:               "host required; single Pod fits in the largest host; LeastFreeCapacityFit",
			enableFeatureGates: []featuregate.Feature{features.TASProfileLeastFreeCapacity},
			nodes: []corev1.Node{
				*testingnode.MakeNode("b1-r1-x1").
					Label("cloud.com/topology-block", "b1").
					Label("cloud.com/topology-rack", "r1").
					Label(corev1.LabelHostname, "x1").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
						corev1.ResourcePods:   resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r2-x2").
					Label("cloud.com/topology-block", "b1").
					Label("cloud.com/topology-rack", "r2").
					Label(corev1.LabelHostname, "x2").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
						corev1.ResourcePods:   resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r2-x3").
					Label("cloud.com/topology-block", "b1").
					Label("cloud.com/topology-rack", "r2").
					Label(corev1.LabelHostname, "x3").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
						corev1.ResourcePods:   resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r2-x4").
					Label("cloud.com/topology-block", "b1").
					Label("cloud.com/topology-rack", "r2").
					Label(corev1.LabelHostname, "x4").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
						corev1.ResourcePods:   resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b2-r2-x5").
					Label("cloud.com/topology-block", "b2").
					Label("cloud.com/topology-rack", "r1").
					Label(corev1.LabelHostname, "x5").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
						corev1.ResourcePods:   resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b2-r2-x6").
					Label("cloud.com/topology-block", "b2").
					Label("cloud.com/topology-rack", "r2").
					Label(corev1.LabelHostname, "x6").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("2"),
						corev1.ResourceMemory: resource.MustParse("4Gi"),
						corev1.ResourcePods:   resource.MustParse("40"),
					}).
					Ready().
					Obj(),
			},
			levels: []string{"cloud.com/topology-block", "cloud.com/topology-rack", corev1.LabelHostname},
			podSets: []podSetCase{
				{
					name: "default",
					topologyRequest: &kueue.PodSetTopologyRequest{
						Required: ptr.To(corev1.LabelHostname),
					},
					requests: resources.Requests{
						corev1.ResourceCPU: 1000,
					},
					count: 2,
					wantAssignment: &kueue.TopologyAssignment{
						Levels: []string{corev1.LabelHostname},
						Domains: []kueue.TopologyDomainAssignment{
							{Count: 2, Values: []string{"x6"}},
						},
					},
				},
			},
		},
		// 15. host preferred; single Pod fits in the largest host; LeastFreeCapacityFit
		{
			desc:               "host preferred; single Pod fits in the largest host; LeastFreeCapacityFit",
			enableFeatureGates: []featuregate.Feature{features.TASProfileLeastFreeCapacity},
			nodes: []corev1.Node{
				*testingnode.MakeNode("b1-r1-x1").
					Label("cloud.com/topology-block", "b1").
					Label("cloud.com/topology-rack", "r1").
					Label(corev1.LabelHostname, "x1").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
						corev1.ResourcePods:   resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r2-x2").
					Label("cloud.com/topology-block", "b1").
					Label("cloud.com/topology-rack", "r2").
					Label(corev1.LabelHostname, "x2").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
						corev1.ResourcePods:   resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r2-x3").
					Label("cloud.com/topology-block", "b1").
					Label("cloud.com/topology-rack", "r2").
					Label(corev1.LabelHostname, "x3").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
						corev1.ResourcePods:   resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r2-x4").
					Label("cloud.com/topology-block", "b1").
					Label("cloud.com/topology-rack", "r2").
					Label(corev1.LabelHostname, "x4").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
						corev1.ResourcePods:   resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b2-r2-x5").
					Label("cloud.com/topology-block", "b2").
					Label("cloud.com/topology-rack", "r1").
					Label(corev1.LabelHostname, "x5").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
						corev1.ResourcePods:   resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b2-r2-x6").
					Label("cloud.com/topology-block", "b2").
					Label("cloud.com/topology-rack", "r2").
					Label(corev1.LabelHostname, "x6").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("2"),
						corev1.ResourceMemory: resource.MustParse("4Gi"),
						corev1.ResourcePods:   resource.MustParse("40"),
					}).
					Ready().
					Obj(),
			},
			levels: []string{"cloud.com/topology-block", "cloud.com/topology-rack", corev1.LabelHostname},
			podSets: []podSetCase{
				{
					name: "default",
					topologyRequest: &kueue.PodSetTopologyRequest{
						Preferred: ptr.To(corev1.LabelHostname),
					},
					requests: resources.Requests{
						corev1.ResourceCPU: 1000,
					},
					count: 2,
					wantAssignment: &kueue.TopologyAssignment{
						Levels: []string{corev1.LabelHostname},
						Domains: []kueue.TopologyDomainAssignment{
							{Count: 2, Values: []string{"x6"}},
						},
					},
				},
			},
		},
		// 16. host required; no single host fits all pods, expect notFitMessage; LeastFreeCapacityFit
		{
			desc:               "host required; no single host fits all pods, expect notFitMessage; LeastFreeCapacityFit",
			enableFeatureGates: []featuregate.Feature{features.TASProfileLeastFreeCapacity},
			nodes: []corev1.Node{
				*testingnode.MakeNode("b1-r1-x1").
					Label("cloud.com/topology-block", "b1").
					Label("cloud.com/topology-rack", "r1").
					Label(corev1.LabelHostname, "x1").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
						corev1.ResourcePods:   resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r2-x2").
					Label("cloud.com/topology-block", "b1").
					Label("cloud.com/topology-rack", "r2").
					Label(corev1.LabelHostname, "x2").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
						corev1.ResourcePods:   resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r2-x3").
					Label("cloud.com/topology-block", "b1").
					Label("cloud.com/topology-rack", "r2").
					Label(corev1.LabelHostname, "x3").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
						corev1.ResourcePods:   resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r2-x4").
					Label("cloud.com/topology-block", "b1").
					Label("cloud.com/topology-rack", "r2").
					Label(corev1.LabelHostname, "x4").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
						corev1.ResourcePods:   resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b2-r2-x5").
					Label("cloud.com/topology-block", "b2").
					Label("cloud.com/topology-rack", "r1").
					Label(corev1.LabelHostname, "x5").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
						corev1.ResourcePods:   resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b2-r2-x6").
					Label("cloud.com/topology-block", "b2").
					Label("cloud.com/topology-rack", "r2").
					Label(corev1.LabelHostname, "x6").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("2"),
						corev1.ResourceMemory: resource.MustParse("4Gi"),
						corev1.ResourcePods:   resource.MustParse("40"),
					}).
					Ready().
					Obj(),
			},
			levels: []string{"cloud.com/topology-block", "cloud.com/topology-rack", corev1.LabelHostname},
			podSets: []podSetCase{
				{
					name: "default",
					topologyRequest: &kueue.PodSetTopologyRequest{
						Required: ptr.To(corev1.LabelHostname),
					},
					requests: resources.Requests{
						corev1.ResourceCPU: 1000,
					},
					count:      3,
					wantReason: `topology "default" allows to fit only 2 out of 3 pod(s)`,
				},
			},
		},
		// 17. host preferred; single Pod fits in the host; BestFit; TASProfileMixed
		{
			desc:               "host preferred; single Pod fits in the host; BestFit; TASProfileMixed",
			enableFeatureGates: []featuregate.Feature{features.TASProfileMixed},
			nodes: []corev1.Node{
				*testingnode.MakeNode("b1-r1-x1").
					Label("cloud.com/topology-block", "b1").
					Label("cloud.com/topology-rack", "r1").
					Label(corev1.LabelHostname, "x1").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
						corev1.ResourcePods:   resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r2-x2").
					Label("cloud.com/topology-block", "b1").
					Label("cloud.com/topology-rack", "r2").
					Label(corev1.LabelHostname, "x2").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
						corev1.ResourcePods:   resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r2-x3").
					Label("cloud.com/topology-block", "b1").
					Label("cloud.com/topology-rack", "r2").
					Label(corev1.LabelHostname, "x3").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
						corev1.ResourcePods:   resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r2-x4").
					Label("cloud.com/topology-block", "b1").
					Label("cloud.com/topology-rack", "r2").
					Label(corev1.LabelHostname, "x4").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
						corev1.ResourcePods:   resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b2-r2-x5").
					Label("cloud.com/topology-block", "b2").
					Label("cloud.com/topology-rack", "r1").
					Label(corev1.LabelHostname, "x5").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
						corev1.ResourcePods:   resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b2-r2-x6").
					Label("cloud.com/topology-block", "b2").
					Label("cloud.com/topology-rack", "r2").
					Label(corev1.LabelHostname, "x6").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("2"),
						corev1.ResourceMemory: resource.MustParse("4Gi"),
						corev1.ResourcePods:   resource.MustParse("40"),
					}).
					Ready().
					Obj(),
			},
			levels: []string{"cloud.com/topology-block", "cloud.com/topology-rack", corev1.LabelHostname},
			podSets: []podSetCase{
				{
					name: "default",
					topologyRequest: &kueue.PodSetTopologyRequest{
						Required: ptr.To(corev1.LabelHostname),
					},
					requests: resources.Requests{
						corev1.ResourceCPU: 1000,
					},
					count: 1,
					wantAssignment: &kueue.TopologyAssignment{
						Levels: []string{corev1.LabelHostname},
						Domains: []kueue.TopologyDomainAssignment{
							{Count: 1, Values: []string{"x1"}},
						},
					},
				},
			},
		},
		// 18. rack required; single Pod fits in a rack; BestFit
		{
			desc: "rack required; single Pod fits in a rack; BestFit",
			nodes: []corev1.Node{
				*testingnode.MakeNode("b1-r1-x1").
					Label("cloud.com/topology-block", "b1").
					Label("cloud.com/topology-rack", "r1").
					Label(corev1.LabelHostname, "x1").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
						corev1.ResourcePods:   resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r2-x2").
					Label("cloud.com/topology-block", "b1").
					Label("cloud.com/topology-rack", "r2").
					Label(corev1.LabelHostname, "x2").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
						corev1.ResourcePods:   resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r2-x3").
					Label("cloud.com/topology-block", "b1").
					Label("cloud.com/topology-rack", "r2").
					Label(corev1.LabelHostname, "x3").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
						corev1.ResourcePods:   resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r2-x4").
					Label("cloud.com/topology-block", "b1").
					Label("cloud.com/topology-rack", "r2").
					Label(corev1.LabelHostname, "x4").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
						corev1.ResourcePods:   resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b2-r2-x5").
					Label("cloud.com/topology-block", "b2").
					Label("cloud.com/topology-rack", "r1").
					Label(corev1.LabelHostname, "x5").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
						corev1.ResourcePods:   resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b2-r2-x6").
					Label("cloud.com/topology-block", "b2").
					Label("cloud.com/topology-rack", "r2").
					Label(corev1.LabelHostname, "x6").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("2"),
						corev1.ResourceMemory: resource.MustParse("4Gi"),
						corev1.ResourcePods:   resource.MustParse("40"),
					}).
					Ready().
					Obj(),
			},
			levels: []string{"cloud.com/topology-block", "cloud.com/topology-rack"},
			podSets: []podSetCase{
				{
					name: "default",
					topologyRequest: &kueue.PodSetTopologyRequest{
						Required: ptr.To("cloud.com/topology-rack"),
					},
					requests: resources.Requests{
						corev1.ResourceCPU: 1000,
					},
					count: 1,
					wantAssignment: &kueue.TopologyAssignment{
						Levels: []string{"cloud.com/topology-block", "cloud.com/topology-rack"},
						Domains: []kueue.TopologyDomainAssignment{
							{Count: 1, Values: []string{"b1", "r1"}},
						},
					},
				},
			},
		},
		// 19. rack required; single Pod fits in a rack; LeastFreeCapacityFit
		{
			desc:               "rack required; single Pod fits in a rack; LeastFreeCapacityFit",
			enableFeatureGates: []featuregate.Feature{features.TASProfileLeastFreeCapacity},
			nodes: []corev1.Node{
				*testingnode.MakeNode("b1-r1-x1").
					Label("cloud.com/topology-block", "b1").
					Label("cloud.com/topology-rack", "r1").
					Label(corev1.LabelHostname, "x1").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
						corev1.ResourcePods:   resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r2-x2").
					Label("cloud.com/topology-block", "b1").
					Label("cloud.com/topology-rack", "r2").
					Label(corev1.LabelHostname, "x2").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
						corev1.ResourcePods:   resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r2-x3").
					Label("cloud.com/topology-block", "b1").
					Label("cloud.com/topology-rack", "r2").
					Label(corev1.LabelHostname, "x3").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
						corev1.ResourcePods:   resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r2-x4").
					Label("cloud.com/topology-block", "b1").
					Label("cloud.com/topology-rack", "r2").
					Label(corev1.LabelHostname, "x4").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
						corev1.ResourcePods:   resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b2-r2-x5").
					Label("cloud.com/topology-block", "b2").
					Label("cloud.com/topology-rack", "r1").
					Label(corev1.LabelHostname, "x5").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
						corev1.ResourcePods:   resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b2-r2-x6").
					Label("cloud.com/topology-block", "b2").
					Label("cloud.com/topology-rack", "r2").
					Label(corev1.LabelHostname, "x6").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("2"),
						corev1.ResourceMemory: resource.MustParse("4Gi"),
						corev1.ResourcePods:   resource.MustParse("40"),
					}).
					Ready().
					Obj(),
			},
			levels: []string{"cloud.com/topology-block", "cloud.com/topology-rack", corev1.LabelHostname},
			podSets: []podSetCase{
				{
					name: "default",
					topologyRequest: &kueue.PodSetTopologyRequest{
						Required: ptr.To("cloud.com/topology-rack"),
					},
					requests: resources.Requests{
						corev1.ResourceCPU: 1000,
					},
					count: 1,
					wantAssignment: &kueue.TopologyAssignment{
						Levels: []string{corev1.LabelHostname},
						Domains: []kueue.TopologyDomainAssignment{
							{Count: 1, Values: []string{"x1"}},
						},
					},
				},
			},
		},
		// 20. rack preferred; multiple Pods fits in multiple racks; LeastFreeCapacityFit
		{
			desc:               "rack preferred; multiple Pods fits in multiple racks; LeastFreeCapacityFit",
			enableFeatureGates: []featuregate.Feature{features.TASProfileLeastFreeCapacity},
			nodes: []corev1.Node{
				*testingnode.MakeNode("b1-r1-x1").
					Label("cloud.com/topology-block", "b1").
					Label("cloud.com/topology-rack", "r1").
					Label(corev1.LabelHostname, "x1").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
						corev1.ResourcePods:   resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r2-x2").
					Label("cloud.com/topology-block", "b1").
					Label("cloud.com/topology-rack", "r2").
					Label(corev1.LabelHostname, "x2").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
						corev1.ResourcePods:   resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r2-x3").
					Label("cloud.com/topology-block", "b1").
					Label("cloud.com/topology-rack", "r2").
					Label(corev1.LabelHostname, "x3").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
						corev1.ResourcePods:   resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r2-x4").
					Label("cloud.com/topology-block", "b1").
					Label("cloud.com/topology-rack", "r2").
					Label(corev1.LabelHostname, "x4").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
						corev1.ResourcePods:   resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b2-r2-x5").
					Label("cloud.com/topology-block", "b2").
					Label("cloud.com/topology-rack", "r1").
					Label(corev1.LabelHostname, "x5").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
						corev1.ResourcePods:   resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b2-r2-x6").
					Label("cloud.com/topology-block", "b2").
					Label("cloud.com/topology-rack", "r2").
					Label(corev1.LabelHostname, "x6").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("2"),
						corev1.ResourceMemory: resource.MustParse("4Gi"),
						corev1.ResourcePods:   resource.MustParse("40"),
					}).
					Ready().
					Obj(),
			},
			levels: []string{"cloud.com/topology-block", "cloud.com/topology-rack"},
			podSets: []podSetCase{
				{
					name: "default",
					topologyRequest: &kueue.PodSetTopologyRequest{
						Preferred: ptr.To("cloud.com/topology-rack"),
					},
					requests: resources.Requests{
						corev1.ResourceCPU: 1000,
					},
					count: 2,
					wantAssignment: &kueue.TopologyAssignment{
						Levels: []string{"cloud.com/topology-block", "cloud.com/topology-rack"},
						Domains: []kueue.TopologyDomainAssignment{
							{Count: 2, Values: []string{"b2", "r2"}},
						},
					},
				},
			},
		},
		// 21. rack required; multiple Pods fit in a rack; BestFit
		{
			desc: "rack required; multiple Pods fit in a rack; BestFit",
			nodes: []corev1.Node{
				*testingnode.MakeNode("b1-r1-x1").
					Label("cloud.com/topology-block", "b1").
					Label("cloud.com/topology-rack", "r1").
					Label(corev1.LabelHostname, "x1").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
						corev1.ResourcePods:   resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r2-x2").
					Label("cloud.com/topology-block", "b1").
					Label("cloud.com/topology-rack", "r2").
					Label(corev1.LabelHostname, "x2").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
						corev1.ResourcePods:   resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r2-x3").
					Label("cloud.com/topology-block", "b1").
					Label("cloud.com/topology-rack", "r2").
					Label(corev1.LabelHostname, "x3").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
						corev1.ResourcePods:   resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r2-x4").
					Label("cloud.com/topology-block", "b1").
					Label("cloud.com/topology-rack", "r2").
					Label(corev1.LabelHostname, "x4").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
						corev1.ResourcePods:   resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b2-r2-x5").
					Label("cloud.com/topology-block", "b2").
					Label("cloud.com/topology-rack", "r1").
					Label(corev1.LabelHostname, "x5").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
						corev1.ResourcePods:   resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b2-r2-x6").
					Label("cloud.com/topology-block", "b2").
					Label("cloud.com/topology-rack", "r2").
					Label(corev1.LabelHostname, "x6").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("2"),
						corev1.ResourceMemory: resource.MustParse("4Gi"),
						corev1.ResourcePods:   resource.MustParse("40"),
					}).
					Ready().
					Obj(),
			},
			levels: []string{"cloud.com/topology-block", "cloud.com/topology-rack"},
			podSets: []podSetCase{
				{
					name: "default",
					topologyRequest: &kueue.PodSetTopologyRequest{
						Required: ptr.To("cloud.com/topology-rack"),
					},
					requests: resources.Requests{
						corev1.ResourceCPU: 1000,
					},
					count: 3,
					wantAssignment: &kueue.TopologyAssignment{
						Levels: []string{"cloud.com/topology-block", "cloud.com/topology-rack"},
						Domains: []kueue.TopologyDomainAssignment{
							{Count: 3, Values: []string{"b1", "r2"}},
						},
					},
				},
			},
		},
		// 22. rack required; too many pods to fit in any rack; BestFit
		{
			desc: "rack required; too many pods to fit in any rack; BestFit",
			nodes: []corev1.Node{
				*testingnode.MakeNode("b1-r1-x1").
					Label("cloud.com/topology-block", "b1").
					Label("cloud.com/topology-rack", "r1").
					Label(corev1.LabelHostname, "x1").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
						corev1.ResourcePods:   resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r2-x2").
					Label("cloud.com/topology-block", "b1").
					Label("cloud.com/topology-rack", "r2").
					Label(corev1.LabelHostname, "x2").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
						corev1.ResourcePods:   resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r2-x3").
					Label("cloud.com/topology-block", "b1").
					Label("cloud.com/topology-rack", "r2").
					Label(corev1.LabelHostname, "x3").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
						corev1.ResourcePods:   resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r2-x4").
					Label("cloud.com/topology-block", "b1").
					Label("cloud.com/topology-rack", "r2").
					Label(corev1.LabelHostname, "x4").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
						corev1.ResourcePods:   resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b2-r2-x5").
					Label("cloud.com/topology-block", "b2").
					Label("cloud.com/topology-rack", "r1").
					Label(corev1.LabelHostname, "x5").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
						corev1.ResourcePods:   resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b2-r2-x6").
					Label("cloud.com/topology-block", "b2").
					Label("cloud.com/topology-rack", "r2").
					Label(corev1.LabelHostname, "x6").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("2"),
						corev1.ResourceMemory: resource.MustParse("4Gi"),
						corev1.ResourcePods:   resource.MustParse("40"),
					}).
					Ready().
					Obj(),
			},
			levels: []string{"cloud.com/topology-block", "cloud.com/topology-rack"},
			podSets: []podSetCase{
				{
					name: "default",
					topologyRequest: &kueue.PodSetTopologyRequest{
						Required: ptr.To("cloud.com/topology-rack"),
					},
					requests: resources.Requests{
						corev1.ResourceCPU: 1000,
					},
					count:      4,
					wantReason: `topology "default" allows to fit only 3 out of 4 pod(s)`,
				},
			},
		},
		// 23. block required; single Pod fits in a block; LeastFreeCapacityFit
		{
			desc:               "block required; single Pod fits in a block; LeastFreeCapacityFit",
			enableFeatureGates: []featuregate.Feature{features.TASProfileLeastFreeCapacity},
			nodes: []corev1.Node{
				*testingnode.MakeNode("b1-r1-x1").
					Label("cloud.com/topology-block", "b1").
					Label("cloud.com/topology-rack", "r1").
					Label(corev1.LabelHostname, "x1").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
						corev1.ResourcePods:   resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r2-x2").
					Label("cloud.com/topology-block", "b1").
					Label("cloud.com/topology-rack", "r2").
					Label(corev1.LabelHostname, "x2").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
						corev1.ResourcePods:   resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r2-x3").
					Label("cloud.com/topology-block", "b1").
					Label("cloud.com/topology-rack", "r2").
					Label(corev1.LabelHostname, "x3").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
						corev1.ResourcePods:   resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r2-x4").
					Label("cloud.com/topology-block", "b1").
					Label("cloud.com/topology-rack", "r2").
					Label(corev1.LabelHostname, "x4").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
						corev1.ResourcePods:   resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b2-r2-x5").
					Label("cloud.com/topology-block", "b2").
					Label("cloud.com/topology-rack", "r1").
					Label(corev1.LabelHostname, "x5").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
						corev1.ResourcePods:   resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b2-r2-x6").
					Label("cloud.com/topology-block", "b2").
					Label("cloud.com/topology-rack", "r2").
					Label(corev1.LabelHostname, "x6").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("2"),
						corev1.ResourceMemory: resource.MustParse("4Gi"),
						corev1.ResourcePods:   resource.MustParse("40"),
					}).
					Ready().
					Obj(),
			},
			levels: []string{"cloud.com/topology-block", "cloud.com/topology-rack", corev1.LabelHostname},
			podSets: []podSetCase{
				{
					name: "default",
					topologyRequest: &kueue.PodSetTopologyRequest{
						Required: ptr.To("cloud.com/topology-block"),
					},
					requests: resources.Requests{
						corev1.ResourceCPU: 1000,
					},
					count: 1,
					wantAssignment: &kueue.TopologyAssignment{
						Levels: []string{corev1.LabelHostname},
						Domains: []kueue.TopologyDomainAssignment{
							{Count: 1, Values: []string{"x5"}},
						},
					},
				},
			},
		},
		// 24. block required; two Pods fits in a block; LeastFreeCapacityFit
		{
			desc:               "block required; two Pods fits in a block; LeastFreeCapacityFit",
			enableFeatureGates: []featuregate.Feature{features.TASProfileLeastFreeCapacity},
			nodes: []corev1.Node{
				*testingnode.MakeNode("b1-r1-x1").
					Label("cloud.com/topology-block", "b1").
					Label("cloud.com/topology-rack", "r1").
					Label(corev1.LabelHostname, "x1").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
						corev1.ResourcePods:   resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r2-x2").
					Label("cloud.com/topology-block", "b1").
					Label("cloud.com/topology-rack", "r2").
					Label(corev1.LabelHostname, "x2").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
						corev1.ResourcePods:   resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r2-x3").
					Label("cloud.com/topology-block", "b1").
					Label("cloud.com/topology-rack", "r2").
					Label(corev1.LabelHostname, "x3").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
						corev1.ResourcePods:   resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r2-x4").
					Label("cloud.com/topology-block", "b1").
					Label("cloud.com/topology-rack", "r2").
					Label(corev1.LabelHostname, "x4").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
						corev1.ResourcePods:   resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b2-r2-x5").
					Label("cloud.com/topology-block", "b2").
					Label("cloud.com/topology-rack", "r1").
					Label(corev1.LabelHostname, "x5").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
						corev1.ResourcePods:   resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b2-r2-x6").
					Label("cloud.com/topology-block", "b2").
					Label("cloud.com/topology-rack", "r2").
					Label(corev1.LabelHostname, "x6").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("2"),
						corev1.ResourceMemory: resource.MustParse("4Gi"),
						corev1.ResourcePods:   resource.MustParse("40"),
					}).
					Ready().
					Obj(),
			},
			levels: []string{"cloud.com/topology-block", "cloud.com/topology-rack", corev1.LabelHostname},
			podSets: []podSetCase{
				{
					name: "default",
					topologyRequest: &kueue.PodSetTopologyRequest{
						Required: ptr.To("cloud.com/topology-block"),
					},
					requests: resources.Requests{
						corev1.ResourceCPU: 1000,
					},
					count: 2,
					wantAssignment: &kueue.TopologyAssignment{
						Levels: []string{corev1.LabelHostname},
						Domains: []kueue.TopologyDomainAssignment{
							{Count: 1, Values: []string{"x5"}},
							{Count: 1, Values: []string{"x6"}},
						},
					},
				},
			},
		},
		// 25. block required; single Pod fits in a block and a single rack; BestFit
		{
			desc: "block required; single Pod fits in a block and a single rack; BestFit",
			nodes: []corev1.Node{
				*testingnode.MakeNode("b1-r1-x1").
					Label("cloud.com/topology-block", "b1").
					Label("cloud.com/topology-rack", "r1").
					Label(corev1.LabelHostname, "x1").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
						corev1.ResourcePods:   resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r2-x2").
					Label("cloud.com/topology-block", "b1").
					Label("cloud.com/topology-rack", "r2").
					Label(corev1.LabelHostname, "x2").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
						corev1.ResourcePods:   resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r2-x3").
					Label("cloud.com/topology-block", "b1").
					Label("cloud.com/topology-rack", "r2").
					Label(corev1.LabelHostname, "x3").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
						corev1.ResourcePods:   resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r2-x4").
					Label("cloud.com/topology-block", "b1").
					Label("cloud.com/topology-rack", "r2").
					Label(corev1.LabelHostname, "x4").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
						corev1.ResourcePods:   resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b2-r2-x5").
					Label("cloud.com/topology-block", "b2").
					Label("cloud.com/topology-rack", "r1").
					Label(corev1.LabelHostname, "x5").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
						corev1.ResourcePods:   resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b2-r2-x6").
					Label("cloud.com/topology-block", "b2").
					Label("cloud.com/topology-rack", "r2").
					Label(corev1.LabelHostname, "x6").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("2"),
						corev1.ResourceMemory: resource.MustParse("4Gi"),
						corev1.ResourcePods:   resource.MustParse("40"),
					}).
					Ready().
					Obj(),
			},
			levels: []string{"cloud.com/topology-block", "cloud.com/topology-rack"},
			podSets: []podSetCase{
				{
					name: "default",
					topologyRequest: &kueue.PodSetTopologyRequest{
						Required: ptr.To("cloud.com/topology-block"),
					},
					requests: resources.Requests{
						corev1.ResourceCPU: 1000,
					},
					count: 1,
					wantAssignment: &kueue.TopologyAssignment{
						Levels: []string{"cloud.com/topology-block", "cloud.com/topology-rack"},
						Domains: []kueue.TopologyDomainAssignment{
							{Count: 1, Values: []string{"b2", "r1"}},
						},
					},
				},
			},
		},
		// 26. block required; single Pod fits in a block spread across two racks; BestFit
		{
			desc: "block required; single Pod fits in a block spread across two racks; BestFit",
			nodes: []corev1.Node{
				*testingnode.MakeNode("b1-r1-x1").
					Label("cloud.com/topology-block", "b1").
					Label("cloud.com/topology-rack", "r1").
					Label(corev1.LabelHostname, "x1").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
						corev1.ResourcePods:   resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r2-x2").
					Label("cloud.com/topology-block", "b1").
					Label("cloud.com/topology-rack", "r2").
					Label(corev1.LabelHostname, "x2").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
						corev1.ResourcePods:   resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r2-x3").
					Label("cloud.com/topology-block", "b1").
					Label("cloud.com/topology-rack", "r2").
					Label(corev1.LabelHostname, "x3").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
						corev1.ResourcePods:   resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r2-x4").
					Label("cloud.com/topology-block", "b1").
					Label("cloud.com/topology-rack", "r2").
					Label(corev1.LabelHostname, "x4").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
						corev1.ResourcePods:   resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b2-r2-x5").
					Label("cloud.com/topology-block", "b2").
					Label("cloud.com/topology-rack", "r1").
					Label(corev1.LabelHostname, "x5").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
						corev1.ResourcePods:   resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b2-r2-x6").
					Label("cloud.com/topology-block", "b2").
					Label("cloud.com/topology-rack", "r2").
					Label(corev1.LabelHostname, "x6").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("2"),
						corev1.ResourceMemory: resource.MustParse("4Gi"),
						corev1.ResourcePods:   resource.MustParse("40"),
					}).
					Ready().
					Obj(),
			},
			levels: []string{"cloud.com/topology-block", "cloud.com/topology-rack"},
			podSets: []podSetCase{
				{
					name: "default",
					topologyRequest: &kueue.PodSetTopologyRequest{
						Required: ptr.To("cloud.com/topology-block"),
					},
					requests: resources.Requests{
						corev1.ResourceCPU: 1000,
					},
					count: 4,
					wantAssignment: &kueue.TopologyAssignment{
						Levels: []string{"cloud.com/topology-block", "cloud.com/topology-rack"},
						Domains: []kueue.TopologyDomainAssignment{
							{Count: 3, Values: []string{"b1", "r2"}},
							{Count: 1, Values: []string{"b1", "r1"}},
						},
					},
				},
			},
		},
		// 27. block required; Pods fit in a block spread across two racks; BestFit
		{
			desc: "block required; Pods fit in a block spread across two racks; BestFit",
			nodes: []corev1.Node{
				*testingnode.MakeNode("b1-r1-x1").
					Label("cloud.com/topology-block", "b1").
					Label("cloud.com/topology-rack", "r1").
					Label(corev1.LabelHostname, "x1").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
						corev1.ResourcePods:   resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r2-x2").
					Label("cloud.com/topology-block", "b1").
					Label("cloud.com/topology-rack", "r2").
					Label(corev1.LabelHostname, "x2").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
						corev1.ResourcePods:   resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r2-x3").
					Label("cloud.com/topology-block", "b1").
					Label("cloud.com/topology-rack", "r2").
					Label(corev1.LabelHostname, "x3").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
						corev1.ResourcePods:   resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b1-r2-x4").
					Label("cloud.com/topology-block", "b1").
					Label("cloud.com/topology-rack", "r2").
					Label(corev1.LabelHostname, "x4").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
						corev1.ResourcePods:   resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b2-r2-x5").
					Label("cloud.com/topology-block", "b2").
					Label("cloud.com/topology-rack", "r1").
					Label(corev1.LabelHostname, "x5").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("1"),
						corev1.ResourceMemory: resource.MustParse("1Gi"),
						corev1.ResourcePods:   resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b2-r2-x6").
					Label("cloud.com/topology-block", "b2").
					Label("cloud.com/topology-rack", "r2").
					Label(corev1.LabelHostname, "x6").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("2"),
						corev1.ResourceMemory: resource.MustParse("4Gi"),
						corev1.ResourcePods:   resource.MustParse("40"),
					}).
					Ready().
					Obj(),
			},
			levels: []string{"cloud.com/topology-block", "cloud.com/topology-rack"},
			podSets: []podSetCase{
				{
					name: "default",
					topologyRequest: &kueue.PodSetTopologyRequest{
						Required: ptr.To("cloud.com/topology-block"),
					},
					requests: resources.Requests{
						corev1.ResourceCPU: 1000,
					},
					count: 4,
					wantAssignment: &kueue.TopologyAssignment{
						Levels: []string{"cloud.com/topology-block", "cloud.com/topology-rack"},
						Domains: []kueue.TopologyDomainAssignment{
							{Count: 3, Values: []string{"b1", "r2"}},
							{Count: 1, Values: []string{"b1", "r1"}},
						},
					},
				},
			},
		},
		// 28. block preferred; but only multiple blocks can accommodate the workload; BestFit
		{
			desc:   "block preferred; but only multiple blocks can accommodate the workload; BestFit",
			nodes:  defaultNodes,
			levels: defaultTwoLevels,
			podSets: []podSetCase{
				{
					name: "default",
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
							{Count: 3, Values: []string{"b1", "r2"}},
							{Count: 2, Values: []string{"b2", "r2"}},
							{Count: 1, Values: []string{"b1", "r1"}},
						},
					},
				},
			},
		},
		// 29. block preferred; but the workload cannot be accommodate in entire topology; BestFit
		{
			desc:   "block preferred; but the workload cannot be accommodate in entire topology; BestFit",
			nodes:  defaultNodes,
			levels: defaultTwoLevels,
			podSets: []podSetCase{
				{
					name: "default",
					topologyRequest: &kueue.PodSetTopologyRequest{
						Preferred: ptr.To(tasBlockLabel),
					},
					requests: resources.Requests{
						corev1.ResourceCPU: 1000,
					},
					count:      10,
					wantReason: `topology "default" allows to fit only 7 out of 10 pod(s)`,
				},
			},
		},
		// 30. only nodes with matching labels are considered; no matching node; BestFit
		{
			desc: "only nodes with matching labels are considered; no matching node; BestFit",
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
			nodeLabels: map[string]string{
				"zone": "zone-b",
			},
			podSets: []podSetCase{
				{
					name: "default",
					topologyRequest: &kueue.PodSetTopologyRequest{
						Required: ptr.To(corev1.LabelHostname),
					},
					requests: resources.Requests{
						corev1.ResourceCPU: 1000,
					},
					count:      1,
					wantReason: "no topology domains at level: kubernetes.io/hostname",
				},
			},
		},
		// 31. only nodes with matching labels are considered; matching node is found; BestFit
		{
			desc: "only nodes with matching labels are considered; matching node is found; BestFit",
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
			nodeLabels: map[string]string{
				"zone": "zone-a",
			},
			podSets: []podSetCase{
				{
					name: "default",
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
							{Count: 1, Values: []string{"x1"}},
						},
					},
				},
			},
		},
		// 32. only nodes with matching levels are considered; no host label on node; BestFit
		{
			desc: "only nodes with matching levels are considered; no host label on node; BestFit",
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
			podSets: []podSetCase{
				{
					name: "default",
					topologyRequest: &kueue.PodSetTopologyRequest{
						Required: ptr.To(tasRackLabel),
					},
					requests: resources.Requests{
						corev1.ResourceCPU: 1000,
					},
					count:      1,
					wantReason: "no topology domains at level: cloud.com/topology-rack",
				},
			},
		},
		// 33. don't consider terminal pods when computing the capacity; BestFit
		{
			desc: "don't consider terminal pods when computing the capacity; BestFit",
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
			podSets: []podSetCase{
				{
					name: "default",
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
							{Count: 1, Values: []string{"x1"}},
						},
					},
				},
			},
		},
		// 34. include usage from pending scheduled non-TAS pods, blocked assignment; BestFit
		{
			desc: "include usage from pending scheduled non-TAS pods, blocked assignment; BestFit",
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
			podSets: []podSetCase{
				{
					name: "default",
					topologyRequest: &kueue.PodSetTopologyRequest{
						Required: ptr.To(corev1.LabelHostname),
					},
					requests: resources.Requests{
						corev1.ResourceCPU: 600,
					},
					count:      1,
					wantReason: `topology "default" doesn't allow to fit any of 1 pod(s)`,
				},
			},
		},
		// 35. include usage from running non-TAS pods, blocked assignment; BestFit
		{
			desc: "include usage from running non-TAS pods, blocked assignment; BestFit",
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
			podSets: []podSetCase{
				{
					name: "default",
					topologyRequest: &kueue.PodSetTopologyRequest{
						Required: ptr.To(corev1.LabelHostname),
					},
					requests: resources.Requests{
						corev1.ResourceCPU: 600,
					},
					count:      1,
					wantReason: `topology "default" doesn't allow to fit any of 1 pod(s)`,
				},
			},
		},
		// 36. include usage from running non-TAS pods, found free capacity on another node; BestFit
		{
			desc: "include usage from running non-TAS pods, found free capacity on another node; BestFit",
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
			podSets: []podSetCase{
				{
					name: "default",
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
							{Count: 1, Values: []string{"x2"}},
						},
					},
				},
			},
		},
		// 37. no assignment as node is not ready; BestFit
		{
			desc: "no assignment as node is not ready; BestFit",
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
			nodeLabels: map[string]string{
				"zone": "zone-a",
			},
			podSets: []podSetCase{
				{
					name: "default",
					topologyRequest: &kueue.PodSetTopologyRequest{
						Required: ptr.To(corev1.LabelHostname),
					},
					requests: resources.Requests{
						corev1.ResourceCPU: 1000,
					},
					count:      1,
					wantReason: "no topology domains at level: kubernetes.io/hostname",
				},
			},
		},
		// 38. block required for podset; host required for slices; BestFit
		{
			desc: "block required for podset; host required for slices; BestFit",
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
			podSets: []podSetCase{
				{
					name: "default",
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
							{Count: 2, Values: []string{"x3"}},
							{Count: 2, Values: []string{"x2"}},
							{Count: 2, Values: []string{"x1"}},
						},
					},
				},
			},
		},
		// 39. block required for podset; host required for slices; prioritize more free slice capacity first and then tight fit; BestFit
		{
			desc: "block required for podset; host required for slices; prioritize more free slice capacity first and then tight fit; BestFit",
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
			podSets: []podSetCase{
				{
					name: "default",
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
							{Count: 6, Values: []string{"x1"}},
							{Count: 4, Values: []string{"x3"}},
							{Count: 2, Values: []string{"x4"}},
						},
					},
				},
			},
		},
		// 40. block required for podset; host required for slices; select domains with tight fit; BestFit
		{
			desc: "block required for podset; host required for slices; select domains with tight fit; BestFit",
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
			podSets: []podSetCase{
				{
					name: "default",
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
							{Count: 2, Values: []string{"x2"}},
							{Count: 2, Values: []string{"x3"}},
						},
					},
				},
			},
		},
		// 41. block required for podset; rack required for slices; BestFit
		{
			desc: "block required for podset; rack required for slices; BestFit",
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
			podSets: []podSetCase{
				{
					name: "default",
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
							{Count: 1, Values: []string{"x1"}},
							{Count: 1, Values: []string{"x2"}},
							{Count: 1, Values: []string{"x3"}},
							{Count: 1, Values: []string{"x4"}},
						},
					},
				},
			},
		},
		// 42. block preferred for podset; rack required for slices; BestFit
		{
			desc:   "block preferred for podset; rack required for slices; BestFit",
			nodes:  defaultNodes,
			levels: defaultThreeLevels,
			podSets: []podSetCase{
				{
					name: "default",
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
							{Count: 1, Values: []string{"x2"}},
							{Count: 1, Values: []string{"x3"}},
							{Count: 2, Values: []string{"x6"}},
						},
					},
				},
			},
		},
		// 43. block required for podset; rack required for slices; podset fits in both blocks, but slices fit in only one block
		{
			desc: "block required for podset; rack required for slices; podset fits in both blocks, but slices fit in only one block",
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
			podSets: []podSetCase{
				{
					name: "default",
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
							{Count: 3, Values: []string{"x4"}},
							{Count: 3, Values: []string{"x5"}},
						},
					},
				},
			},
		},
		// 44. slice required topology level cannot be above the main required topology level
		{
			desc:   "slice required topology level cannot be above the main required topology level",
			nodes:  defaultNodes,
			levels: defaultThreeLevels,
			podSets: []podSetCase{
				{
					name: "default",
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
				},
			},
		},
		// 45. slice size is required when slice topology is requested
		{
			desc:   "slice size is required when slice topology is requested",
			nodes:  defaultNodes,
			levels: defaultThreeLevels,
			podSets: []podSetCase{
				{
					name: "default",
					topologyRequest: &kueue.PodSetTopologyRequest{
						Required:                    ptr.To(tasBlockLabel),
						PodSetSliceRequiredTopology: ptr.To(corev1.LabelHostname),
					},
					requests: resources.Requests{
						corev1.ResourceCPU: 1000,
					},
					count:      1,
					wantReason: "slice topology requested, but slice size not provided",
				},
			},
		},
		// 46. cannot request not existing slice topology
		{
			desc:   "cannot request not existing slice topology",
			nodes:  defaultNodes,
			levels: defaultThreeLevels,
			podSets: []podSetCase{
				{
					name: "default",
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
				},
			},
		},
		// 47. no topology for podset; host required for slices; BestFit
		{
			desc: "no topology for podset; host required for slices; BestFit",
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
			podSets: []podSetCase{
				{
					name: "default",
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
							{Count: 2, Values: []string{"x3"}},
							{Count: 2, Values: []string{"x2"}},
							{Count: 2, Values: []string{"x1"}},
						},
					},
				},
			},
		},
		// 48. find topology assignment for two podsets with overlapping domain
		{
			desc: "find topology assignment for two podsets with overlapping domain",
			nodes: []corev1.Node{
				*testingnode.MakeNode("b1").
					Label("cloud.com/topology-block", "b1").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("2"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b2").
					Label("cloud.com/topology-block", "b2").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("2"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("b3").
					Label("cloud.com/topology-block", "b3").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("2"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj(),
			},
			levels: []string{"cloud.com/topology-block"},
			podSets: []podSetCase{
				{
					name: "podset1",
					topologyRequest: &kueue.PodSetTopologyRequest{
						Preferred: ptr.To("cloud.com/topology-block"),
					},
					requests: resources.Requests{
						corev1.ResourceCPU: 1000,
					},
					count: 3,
					wantAssignment: &kueue.TopologyAssignment{
						Levels: []string{"cloud.com/topology-block"},
						Domains: []kueue.TopologyDomainAssignment{
							{Count: 2, Values: []string{"b1"}},
							{Count: 1, Values: []string{"b2"}},
						},
					},
				},
				{
					name: "podset2",
					topologyRequest: &kueue.PodSetTopologyRequest{
						Preferred: ptr.To("cloud.com/topology-block"),
					},
					requests: resources.Requests{
						corev1.ResourceCPU: 1000,
					},
					count: 3,
					wantAssignment: &kueue.TopologyAssignment{
						Levels: []string{"cloud.com/topology-block"},
						Domains: []kueue.TopologyDomainAssignment{
							{Count: 1, Values: []string{"b2"}},
							{Count: 2, Values: []string{"b3"}},
						},
					},
				},
			},
		},
	}

	for _, tc := range testCases {
		t.Run(tc.desc, func(t *testing.T) {
			ctx, _ := utiltesting.ContextWithLog(t)
			for _, gate := range tc.enableFeatureGates {
				features.SetFeatureGateDuringTest(t, gate, true)
			}
			// Use tolerations from the first podset for flavor cache (for legacy compatibility)
			var tolerations []corev1.Toleration
			if len(tc.podSets) > 0 {
				tolerations = tc.podSets[0].tolerations
			}
			snapshot := buildSnapshot(ctx, t, tc.nodes, tc.pods, tc.levels, tc.nodeLabels, tolerations)
			flavorTASRequests := make([]TASPodSetRequests, len(tc.podSets))
			for i, ps := range tc.podSets {
				flavorTASRequests[i] = buildTASInput(ps)
				if ps.topologyRequest == nil {
					flavorTASRequests[i].Implied = true
				}
			}
			wantResult := make(TASAssignmentsResult)
			for _, ps := range tc.podSets {
				wantResult[ps.name] = buildWantedResult(ps.wantAssignment, ps.wantReason)
			}
			gotResult := snapshot.FindTopologyAssignmentsForFlavor(flavorTASRequests)
			if diff := cmp.Diff(wantResult, gotResult); diff != "" {
				t.Errorf("unexpected topology assignment (-want,+got): %s", diff)
			}
		})
	}
}
