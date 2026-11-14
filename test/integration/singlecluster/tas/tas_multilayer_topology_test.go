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

package tas

import (
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
	utiltas "sigs.k8s.io/kueue/pkg/util/tas"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingnode "sigs.k8s.io/kueue/pkg/util/testingjobs/node"
	"sigs.k8s.io/kueue/test/util/behavioral"
	"sigs.k8s.io/kueue/test/util/behavioral/integration"
)

var _ = ginkgo.Describe("Topology Aware Scheduling", func() {
	var (
		ns *corev1.Namespace
	)

	ginkgo.BeforeEach(func() {
		fwk.StartManagerForSpec(ctx, cfg, managerSetup())
		ns = behavioral.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "tas-")
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(forceDeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
	})

	ginkgo.Context("Multiple flavors and workload lifecycle", func() {
		ginkgo.When("Multi-layer topology constraints", func() {
			var (
				nodes        []corev1.Node
				topology     *kueue.Topology
				tasFlavor    *kueue.ResourceFlavor
				clusterQueue *kueue.ClusterQueue
				localQueue   *kueue.LocalQueue
			)

			ginkgo.BeforeEach(func() {
				features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.TASMultiLayerTopology, true)

				// Topology:
				//             b1
				//         /         \
				//       r1           r2
				//    /     \       /    \
				//  x1(6)  x2(8)  x3(2)  x4(8)
				//
				// Node allocatable:
				// x1: 6 GPUs, x2: 8 GPUs, x3: 2 GPUs, x4: 8 GPUs
				nodes = []corev1.Node{
					*testingnode.MakeNode("x1").
						Label("node-group", "tas").
						Label(utiltesting.DefaultBlockTopologyLevel, "b1").
						Label(utiltesting.DefaultRackTopologyLevel, "r1").
						Label(corev1.LabelHostname, "x1").
						StatusAllocatable(corev1.ResourceList{
							"nvidia.com/gpu":    resource.MustParse("6"),
							corev1.ResourceCPU:  resource.MustParse("1"),
							corev1.ResourcePods: resource.MustParse("10"),
						}).
						Ready().
						Obj(),
					*testingnode.MakeNode("x2").
						Label("node-group", "tas").
						Label(utiltesting.DefaultBlockTopologyLevel, "b1").
						Label(utiltesting.DefaultRackTopologyLevel, "r1").
						Label(corev1.LabelHostname, "x2").
						StatusAllocatable(corev1.ResourceList{
							"nvidia.com/gpu":    resource.MustParse("8"),
							corev1.ResourceCPU:  resource.MustParse("1"),
							corev1.ResourcePods: resource.MustParse("10"),
						}).
						Ready().
						Obj(),
					*testingnode.MakeNode("x3").
						Label("node-group", "tas").
						Label(utiltesting.DefaultBlockTopologyLevel, "b1").
						Label(utiltesting.DefaultRackTopologyLevel, "r2").
						Label(corev1.LabelHostname, "x3").
						StatusAllocatable(corev1.ResourceList{
							"nvidia.com/gpu":    resource.MustParse("2"),
							corev1.ResourceCPU:  resource.MustParse("1"),
							corev1.ResourcePods: resource.MustParse("10"),
						}).
						Ready().
						Obj(),
					*testingnode.MakeNode("x4").
						Label("node-group", "tas").
						Label(utiltesting.DefaultBlockTopologyLevel, "b1").
						Label(utiltesting.DefaultRackTopologyLevel, "r2").
						Label(corev1.LabelHostname, "x4").
						StatusAllocatable(corev1.ResourceList{
							"nvidia.com/gpu":    resource.MustParse("8"),
							corev1.ResourceCPU:  resource.MustParse("1"),
							corev1.ResourcePods: resource.MustParse("10"),
						}).
						Ready().
						Obj(),
				}
				integration.CreateNodesWithStatus(ctx, k8sClient, nodes)

				topology = utiltestingapi.MakeDefaultThreeLevelTopology("default")
				behavioral.MustCreate(ctx, k8sClient, topology)

				tasFlavor = utiltestingapi.MakeResourceFlavor("tas-flavor").
					NodeLabel("node-group", "tas").
					TopologyName("default").Obj()
				behavioral.MustCreate(ctx, k8sClient, tasFlavor)

				clusterQueue = utiltestingapi.MakeClusterQueue("cluster-queue").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas(tasFlavor.Name).
						Resource("nvidia.com/gpu", "40").Obj()).
					Obj()
				behavioral.MustCreate(ctx, k8sClient, clusterQueue)
				behavioral.ExpectClusterQueuesToBeActive(ctx, k8sClient, clusterQueue)

				localQueue = utiltestingapi.MakeLocalQueue("local-queue", ns.Name).ClusterQueue(clusterQueue.Name).Obj()
				behavioral.MustCreate(ctx, k8sClient, localQueue)
			})

			ginkgo.AfterEach(func() {
				gomega.Expect(behavioral.DeleteAllJobsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
				gomega.Expect(behavioral.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
				gomega.Expect(behavioral.DeleteObject(ctx, k8sClient, localQueue)).Should(gomega.Succeed())
				behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
				behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, tasFlavor, true)
				behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, topology, true)
				for _, node := range nodes {
					behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, &node, true)
				}
			})

			ginkgo.It("should admit workload with single constraint layer", func() {
				// Constraints: [{rack, 8}] with required=block.
				var wl1 *kueue.Workload

				ginkgo.By("creating a workload with one constraint layer", func() {
					wl1 = utiltestingapi.MakeWorkload("wl1", ns.Name).
						PodSets(*utiltestingapi.MakePodSet("worker", 16).
							RequiredTopologyRequest(utiltesting.DefaultBlockTopologyLevel).
							SliceRequiredTopologyConstraints(
								kueue.PodsetSliceRequiredTopologyConstraint{
									Topology: utiltesting.DefaultRackTopologyLevel,
									Size:     8,
								},
							).
							Obj()).
						Queue(kueue.LocalQueueName(localQueue.Name)).
						Request("nvidia.com/gpu", "1").Obj()
					behavioral.MustCreate(ctx, k8sClient, wl1)
				})

				ginkgo.By("verify the workload is admitted", func() {
					behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1)
					gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl1), wl1)).To(gomega.Succeed())
					gomega.Expect(wl1.Status.Admission.PodSetAssignments[0].TopologyAssignment).Should(gomega.BeComparableTo(
						utiltas.V1Beta2From(&utiltas.TopologyAssignment{
							Levels: []string{corev1.LabelHostname},
							Domains: []utiltas.TopologyDomainAssignment{
								{Count: 6, Values: []string{"x1"}},
								{Count: 2, Values: []string{"x2"}},
								{Count: 2, Values: []string{"x3"}},
								{Count: 6, Values: []string{"x4"}},
							},
						}),
					))
				})
			})

			ginkgo.It("should admit workload with two constraint layers", func() {
				// Constraints: [{rack, 8}, {hostname, 4}] with required=block.
				var wl1 *kueue.Workload

				ginkgo.By("creating a workload with two constraint layers", func() {
					wl1 = utiltestingapi.MakeWorkload("wl1", ns.Name).
						PodSets(*utiltestingapi.MakePodSet("worker", 16).
							RequiredTopologyRequest(utiltesting.DefaultBlockTopologyLevel).
							SliceRequiredTopologyConstraints(
								kueue.PodsetSliceRequiredTopologyConstraint{
									Topology: utiltesting.DefaultRackTopologyLevel,
									Size:     8,
								},
								kueue.PodsetSliceRequiredTopologyConstraint{
									Topology: corev1.LabelHostname,
									Size:     4,
								},
							).
							Obj()).
						Queue(kueue.LocalQueueName(localQueue.Name)).
						Request("nvidia.com/gpu", "1").Obj()
					behavioral.MustCreate(ctx, k8sClient, wl1)
				})

				ginkgo.By("verify the workload is admitted", func() {
					behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1)
					gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl1), wl1)).To(gomega.Succeed())
					gomega.Expect(wl1.Status.Admission.PodSetAssignments[0].TopologyAssignment).Should(gomega.BeComparableTo(
						utiltas.V1Beta2From(&utiltas.TopologyAssignment{
							Levels: []string{corev1.LabelHostname},
							Domains: []utiltas.TopologyDomainAssignment{
								{Count: 4, Values: []string{"x1"}},
								{Count: 4, Values: []string{"x2"}},
								{Count: 8, Values: []string{"x4"}},
							},
						}),
					))
				})
			})

			ginkgo.It("should not admit workload when resources are insufficient for constraints", func() {
				// Constraints: [{rack, 10}, {hostname, 5}] with required=block.
				// It cannot fit on rack2.
				var wl1 *kueue.Workload

				ginkgo.By("creating a workload that cannot fit", func() {
					wl1 = utiltestingapi.MakeWorkload("wl1", ns.Name).
						PodSets(*utiltestingapi.MakePodSet("worker", 20).
							RequiredTopologyRequest(utiltesting.DefaultBlockTopologyLevel).
							SliceRequiredTopologyConstraints(
								kueue.PodsetSliceRequiredTopologyConstraint{
									Topology: utiltesting.DefaultRackTopologyLevel,
									Size:     10,
								},
								kueue.PodsetSliceRequiredTopologyConstraint{
									Topology: corev1.LabelHostname,
									Size:     5,
								},
							).
							Obj()).
						Queue(kueue.LocalQueueName(localQueue.Name)).
						Request("nvidia.com/gpu", "1").Obj()
					behavioral.MustCreate(ctx, k8sClient, wl1)
				})

				ginkgo.By("verify the workload is not admitted", func() {
					behavioral.ExpectWorkloadsToBePending(ctx, k8sClient, wl1)
				})
			})
		})
	})
})
