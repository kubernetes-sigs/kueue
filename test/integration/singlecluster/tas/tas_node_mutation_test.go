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
	apitypes "k8s.io/apimachinery/pkg/types"
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

	ginkgo.When("Single TAS Resource Flavor", func() {
		var (
			tasFlavor    *kueue.ResourceFlavor
			topology     *kueue.Topology
			localQueue   *kueue.LocalQueue
			clusterQueue *kueue.ClusterQueue
		)

		ginkgo.Context("TAS features, preemption and provisioning", func() {
			ginkgo.When("Node structure is mutated during test cases", func() {
				var (
					nodes []corev1.Node
				)

				ginkgo.BeforeEach(func() {
					topology = utiltestingapi.MakeDefaultTwoLevelTopology("default")
					behavioral.MustCreate(ctx, k8sClient, topology)

					tasFlavor = utiltestingapi.MakeResourceFlavor("tas-flavor").
						NodeLabel("node-group", "tas").
						TopologyName("default").
						Obj()
					behavioral.MustCreate(ctx, k8sClient, tasFlavor)

					clusterQueue = utiltestingapi.MakeClusterQueue("cluster-queue").
						ResourceGroup(*utiltestingapi.MakeFlavorQuotas(tasFlavor.Name).Resource(corev1.ResourceCPU, "5").Obj()).
						Obj()
					behavioral.MustCreate(ctx, k8sClient, clusterQueue)

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

				ginkgo.It("should admit workload when nodes become available", func() {
					var (
						wl1 *kueue.Workload
					)

					ginkgo.By("creating a workload which requires rack, but does not fit in any", func() {
						wl1 = utiltestingapi.MakeWorkload("wl1", ns.Name).
							Queue(kueue.LocalQueueName(localQueue.Name)).Request(corev1.ResourceCPU, "1").Obj()
						wl1.Spec.PodSets[0].TopologyRequest = &kueue.PodSetTopologyRequest{
							Required: new(utiltesting.DefaultRackTopologyLevel),
						}
						behavioral.MustCreate(ctx, k8sClient, wl1)
					})

					ginkgo.By("verify the workload is inadmissible", func() {
						behavioral.ExpectPendingWorkloadsMetric(clusterQueue, 0, 1)
					})

					ginkgo.By("Create nodes to allow scheduling", func() {
						nodes = []corev1.Node{
							*testingnode.MakeNode("b1-r1").
								Label("node-group", "tas").
								Label(utiltesting.DefaultBlockTopologyLevel, "b1").
								Label(utiltesting.DefaultRackTopologyLevel, "r1").
								StatusAllocatable(corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("1"),
									corev1.ResourceMemory: resource.MustParse("1Gi"),
									corev1.ResourcePods:   resource.MustParse("10"),
								}).
								Ready().
								Obj(),
						}
						integration.CreateNodesWithStatus(ctx, k8sClient, nodes)
					})

					ginkgo.By("verify the workload is admitted", func() {
						behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1)
						behavioral.ExpectAdmittedWorkloadsTotalMetric(clusterQueue, "", 1)
						behavioral.ExpectPendingWorkloadsMetric(clusterQueue, 0, 0)
					})
				})
			})

			ginkgo.When("Workloads use preferred node affinity and TASRespectNodeAffinityPreferred is enabled", func() {
				var (
					nodes []corev1.Node
				)

				ginkgo.BeforeEach(func() {
					features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.TASRespectNodeAffinityPreferred, true)
					gomega.Expect(k8sClient.DeleteAllOf(ctx, &corev1.Node{}, client.MatchingLabels{"node-group": "tas"})).Should(gomega.Succeed())
					nodes = []corev1.Node{
						*testingnode.MakeNode("node-best").
							Label("node-group", "tas").
							Label(utiltesting.DefaultBlockTopologyLevel, "b1").
							Label(utiltesting.DefaultRackTopologyLevel, "r1").
							Label(corev1.LabelHostname, "node-best").
							Label("region", "us-west").
							Label("zone", "us").
							StatusAllocatable(corev1.ResourceList{
								corev1.ResourceCPU:    resource.MustParse("1"),
								corev1.ResourceMemory: resource.MustParse("1Gi"),
								corev1.ResourcePods:   resource.MustParse("10"),
							}).
							Ready().
							Obj(),
						*testingnode.MakeNode("node-good").
							Label("node-group", "tas").
							Label(utiltesting.DefaultBlockTopologyLevel, "b1").
							Label(utiltesting.DefaultRackTopologyLevel, "r1").
							Label(corev1.LabelHostname, "node-good").
							Label("region", "us-east").
							Label("zone", "us").
							StatusAllocatable(corev1.ResourceList{
								corev1.ResourceCPU:    resource.MustParse("1"),
								corev1.ResourceMemory: resource.MustParse("1Gi"),
								corev1.ResourcePods:   resource.MustParse("10"),
							}).
							Ready().
							Obj(),
						*testingnode.MakeNode("node-bad").
							Label("node-group", "tas").
							Label(utiltesting.DefaultBlockTopologyLevel, "b1").
							Label(utiltesting.DefaultRackTopologyLevel, "r1").
							Label(corev1.LabelHostname, "node-bad").
							Label("region", "us-east").
							Label("zone", "eu").
							StatusAllocatable(corev1.ResourceList{
								corev1.ResourceCPU:    resource.MustParse("1"),
								corev1.ResourceMemory: resource.MustParse("1Gi"),
								corev1.ResourcePods:   resource.MustParse("10"),
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
						ResourceGroup(*utiltestingapi.MakeFlavorQuotas(tasFlavor.Name).Resource(corev1.ResourceCPU, "5").Obj()).
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

				ginkgo.It("should admit workload to the node matching preferred affinity", func() {
					var (
						wl *kueue.Workload
					)
					ginkgo.By("creating a workload with preferred affinity", func() {
						wl = utiltestingapi.MakeWorkload("wl-preferred", ns.Name).
							Queue(kueue.LocalQueueName(localQueue.Name)).
							PodSets(*utiltestingapi.MakePodSet("main", 1).
								PreferredNodeSelectorRequirement(10, "region", corev1.NodeSelectorOpIn, "us-west").
								Request(corev1.ResourceCPU, "1").
								Obj()).
							Obj()
						behavioral.MustCreate(ctx, k8sClient, wl)
					})

					ginkgo.By("verify the workload is admitted to the preferred node", func() {
						behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl)
						behavioral.ExpectAdmittedWorkloadsTotalMetric(clusterQueue, "", 1)
						gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), wl)).To(gomega.Succeed())
						gomega.Expect(wl.Status.Admission.PodSetAssignments[0].TopologyAssignment).Should(gomega.BeComparableTo(
							utiltas.V1Beta2From(&utiltas.TopologyAssignment{
								Levels: []string{corev1.LabelHostname},
								Domains: []utiltas.TopologyDomainAssignment{
									{Count: 1, Values: []string{"node-best"}},
								},
							}),
						))
					})
				})

				ginkgo.It("should admit workload to the node matching both required and preferred affinity", func() {
					var (
						wl *kueue.Workload
					)
					ginkgo.By("creating a workload with required and preferred affinity", func() {
						wl = utiltestingapi.MakeWorkload("wl-combined", ns.Name).
							Queue(kueue.LocalQueueName(localQueue.Name)).
							PodSets(*utiltestingapi.MakePodSet("main", 1).
								RequiredNodeSelectorRequirement("zone", corev1.NodeSelectorOpIn, "us").
								PreferredNodeSelectorRequirement(10, "region", corev1.NodeSelectorOpIn, "us-west").
								Request(corev1.ResourceCPU, "1").
								Obj()).
							Obj()
						behavioral.MustCreate(ctx, k8sClient, wl)
					})

					ginkgo.By("verify the workload is admitted to the best node", func() {
						behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl)
						behavioral.ExpectAdmittedWorkloadsTotalMetric(clusterQueue, "", 1)
						gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), wl)).To(gomega.Succeed())
						gomega.Expect(wl.Status.Admission.PodSetAssignments[0].TopologyAssignment).Should(gomega.BeComparableTo(
							utiltas.V1Beta2From(&utiltas.TopologyAssignment{
								Levels: []string{corev1.LabelHostname},
								Domains: []utiltas.TopologyDomainAssignment{
									{Count: 1, Values: []string{"node-best"}},
								},
							}),
						))
					})
				})
			})

			ginkgo.When("Node is mutated during test cases", func() {
				var (
					nodes []corev1.Node
				)

				ginkgo.BeforeEach(func() {
					topology = utiltestingapi.MakeDefaultThreeLevelTopology("default")
					behavioral.MustCreate(ctx, k8sClient, topology)

					tasFlavor = utiltestingapi.MakeResourceFlavor("tas-flavor").
						NodeLabel("node-group", "tas").
						TopologyName("default").
						Obj()
					behavioral.MustCreate(ctx, k8sClient, tasFlavor)

					clusterQueue = utiltestingapi.MakeClusterQueue("cluster-queue").
						ResourceGroup(*utiltestingapi.MakeFlavorQuotas(tasFlavor.Name).Resource(corev1.ResourceCPU, "5").Obj()).
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

				ginkgo.It("should admit workload when node gets untainted", func() {
					var (
						wl1 *kueue.Workload
					)

					ginkgo.By("creating a tainted node which will prevent admitting the workload", func() {
						nodes = []corev1.Node{
							*testingnode.MakeNode("b1-r1-x1").
								Label("node-group", "tas").
								Label(utiltesting.DefaultBlockTopologyLevel, "b1").
								Label(utiltesting.DefaultRackTopologyLevel, "r1").
								Label(corev1.LabelHostname, "b1-r1-x1").
								StatusAllocatable(corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("1"),
									corev1.ResourceMemory: resource.MustParse("1Gi"),
									corev1.ResourcePods:   resource.MustParse("10"),
								}).
								Taints(corev1.Taint{
									Key:    "maintenance",
									Value:  "true",
									Effect: corev1.TaintEffectNoSchedule,
								}).
								Ready().
								Obj(),
						}
						integration.CreateNodesWithStatus(ctx, k8sClient, nodes)
					})

					ginkgo.By("creating a workload which does not tolerate the taint", func() {
						wl1 = utiltestingapi.MakeWorkload("wl1", ns.Name).
							Queue(kueue.LocalQueueName(localQueue.Name)).Request(corev1.ResourceCPU, "1").Obj()
						wl1.Spec.PodSets[0].TopologyRequest = &kueue.PodSetTopologyRequest{
							Required: new(utiltesting.DefaultRackTopologyLevel),
						}
						behavioral.MustCreate(ctx, k8sClient, wl1)
					})

					ginkgo.By("verify the workload is inadmissible", func() {
						behavioral.ExpectPendingWorkloadsMetric(clusterQueue, 0, 1)
					})

					ginkgo.By("Remove the node taint to allow scheduling", func() {
						nodes[0].Spec.Taints = nil
						for _, node := range nodes {
							gomega.Expect(k8sClient.Update(ctx, &node)).Should(gomega.Succeed())
						}
					})

					ginkgo.By("verify the workload gets admitted", func() {
						behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1)
						behavioral.ExpectAdmittedWorkloadsTotalMetric(clusterQueue, "", 1)
						behavioral.ExpectPendingWorkloadsMetric(clusterQueue, 0, 0)
					})
				})

				ginkgo.It("should admit workload when node is edited to match the required affinity node selector terms", func() {
					var (
						wl1 *kueue.Workload
					)
					customLabelKey := "custom-label-key-1"
					customLabelCorrectValue := "value-1"
					customLabelWrongValue := "value-2"

					ginkgo.By("creating a node missing the required label", func() {
						nodes = []corev1.Node{
							*testingnode.MakeNode("node-missing-label").
								Label("node-group", "tas").
								Label(utiltesting.DefaultBlockTopologyLevel, "b1").
								Label(utiltesting.DefaultRackTopologyLevel, "r1").
								Label(corev1.LabelHostname, "node-missing-label").
								StatusAllocatable(corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("1"),
									corev1.ResourceMemory: resource.MustParse("1Gi"),
									corev1.ResourcePods:   resource.MustParse("10"),
								}).
								Ready().
								Obj(),
						}
						integration.CreateNodesWithStatus(ctx, k8sClient, nodes)
					})

					ginkgo.By("creating a workload requiring the missing label via Affinity", func() {
						wl1 = utiltestingapi.MakeWorkload("wl-needs-label", ns.Name).
							Queue(kueue.LocalQueueName(localQueue.Name)).
							PodSets(*utiltestingapi.MakePodSet("main", 1).
								RequiredDuringSchedulingIgnoredDuringExecution([]corev1.NodeSelectorTerm{
									{
										MatchExpressions: []corev1.NodeSelectorRequirement{
											{
												Key:      customLabelKey,
												Operator: corev1.NodeSelectorOpIn,
												Values:   []string{customLabelCorrectValue},
											},
										},
									},
								}).
								Request(corev1.ResourceCPU, "1").
								Obj()).
							Obj()
						behavioral.MustCreate(ctx, k8sClient, wl1)
					})

					ginkgo.By("verify the workload with missing label is inadmissible", func() {
						behavioral.ExpectWorkloadsToBePending(ctx, k8sClient, wl1)
						behavioral.ExpectPendingWorkloadsMetric(clusterQueue, 0, 1)
					})

					ginkgo.By("add a label to the node with correct key but wrong value", func() {
						nodeToUpdate := &corev1.Node{}
						gomega.Expect(k8sClient.Get(ctx, client.ObjectKey{Name: nodes[0].Name}, nodeToUpdate)).Should(gomega.Succeed())
						nodeToUpdate.Labels[customLabelKey] = customLabelWrongValue
						gomega.Expect(k8sClient.Update(ctx, nodeToUpdate)).Should(gomega.Succeed())
					})

					ginkgo.By("verify the workload with the correct label key but wrong value is inadmissible", func() {
						behavioral.ExpectWorkloadsToBePending(ctx, k8sClient, wl1)
						behavioral.ExpectPendingWorkloadsMetric(clusterQueue, 0, 1)
					})

					ginkgo.By("add the correct label to the node", func() {
						nodeToUpdate := &corev1.Node{}
						gomega.Expect(k8sClient.Get(ctx, client.ObjectKey{Name: nodes[0].Name}, nodeToUpdate)).Should(gomega.Succeed())
						nodeToUpdate.Labels[customLabelKey] = customLabelCorrectValue
						gomega.Expect(k8sClient.Update(ctx, nodeToUpdate)).Should(gomega.Succeed())
					})

					ginkgo.By("verify the workload gets admitted after label with correct key and value is added", func() {
						behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1)
						behavioral.ExpectAdmittedWorkloadsTotalMetric(clusterQueue, "", 1)
						behavioral.ExpectPendingWorkloadsMetric(clusterQueue, 0, 0)
					})
				})

				ginkgo.It("should admit workload when node gets required label added", func() {
					var (
						wl1 *kueue.Workload
					)
					customLabelKey := "custom-label-key-1"
					customLabelCorrectValue := "value-1"
					customLabelWrongValue := "value-2"

					ginkgo.By("creating a node missing the required label", func() {
						nodes = []corev1.Node{
							*testingnode.MakeNode("node-missing-label").
								Label("node-group", "tas").
								Label(utiltesting.DefaultBlockTopologyLevel, "b1").
								Label(utiltesting.DefaultRackTopologyLevel, "r1").
								Label(corev1.LabelHostname, "node-missing-label").
								StatusAllocatable(corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("1"),
									corev1.ResourceMemory: resource.MustParse("1Gi"),
									corev1.ResourcePods:   resource.MustParse("10"),
								}).
								Ready().
								Obj(),
						}
						integration.CreateNodesWithStatus(ctx, k8sClient, nodes)
					})

					ginkgo.By("creating a workload requiring the missing label via NodeSelector", func() {
						wl1 = utiltestingapi.MakeWorkload("wl-needs-label", ns.Name).
							Queue(kueue.LocalQueueName(localQueue.Name)).
							PodSets(*utiltestingapi.MakePodSet("main", 1).
								NodeSelector(map[string]string{customLabelKey: customLabelCorrectValue}).
								Request(corev1.ResourceCPU, "1").
								Obj()).
							Obj()
						behavioral.MustCreate(ctx, k8sClient, wl1)
					})

					ginkgo.By("verify the workload is inadmissible due to missing label", func() {
						behavioral.ExpectWorkloadsToBePending(ctx, k8sClient, wl1)
						behavioral.ExpectPendingWorkloadsMetric(clusterQueue, 0, 1)
					})

					ginkgo.By("Add a label to the node with correct key but wrong value", func() {
						nodeToUpdate := &corev1.Node{}
						gomega.Expect(k8sClient.Get(ctx, client.ObjectKey{Name: nodes[0].Name}, nodeToUpdate)).Should(gomega.Succeed())
						nodeToUpdate.Labels[customLabelKey] = customLabelWrongValue
						gomega.Expect(k8sClient.Update(ctx, nodeToUpdate)).Should(gomega.Succeed())
					})

					ginkgo.By("verify the workload is inadmissible due to wrong value in the label", func() {
						behavioral.ExpectWorkloadsToBePending(ctx, k8sClient, wl1)
						behavioral.ExpectPendingWorkloadsMetric(clusterQueue, 0, 1)
					})

					ginkgo.By("Add the correct label to the node", func() {
						nodeToUpdate := &corev1.Node{}
						gomega.Expect(k8sClient.Get(ctx, client.ObjectKey{Name: nodes[0].Name}, nodeToUpdate)).Should(gomega.Succeed())
						nodeToUpdate.Labels[customLabelKey] = customLabelCorrectValue
						gomega.Expect(k8sClient.Update(ctx, nodeToUpdate)).Should(gomega.Succeed())
					})

					ginkgo.By("verify the workload gets admitted after label is added", func() {
						behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1)
						behavioral.ExpectAdmittedWorkloadsTotalMetric(clusterQueue, "", 1)
						behavioral.ExpectPendingWorkloadsMetric(clusterQueue, 0, 0)
					})
				})
			})

			// https://github.com/kubernetes-sigs/kueue/pull/10949

			ginkgo.When("Node allocatable for an extended resource drops below current usage", func() {
				var (
					nodes []corev1.Node
				)

				ginkgo.BeforeEach(func() {
					topology = utiltestingapi.MakeDefaultThreeLevelTopology("default")
					behavioral.MustCreate(ctx, k8sClient, topology)

					tasFlavor = utiltestingapi.MakeResourceFlavor("tas-flavor").
						NodeLabel("node-group", "tas").
						TopologyName("default").
						Obj()
					behavioral.MustCreate(ctx, k8sClient, tasFlavor)

					clusterQueue = utiltestingapi.MakeClusterQueue("cluster-queue").
						ResourceGroup(*utiltestingapi.MakeFlavorQuotas(tasFlavor.Name).
							Resource("nvidia.com/gpu", "12").Obj()).
						Obj()
					behavioral.MustCreate(ctx, k8sClient, clusterQueue)
					behavioral.ExpectClusterQueuesToBeActive(ctx, k8sClient, clusterQueue)

					localQueue = utiltestingapi.MakeLocalQueue("local-queue", ns.Name).ClusterQueue(clusterQueue.Name).Obj()
					behavioral.MustCreate(ctx, k8sClient, localQueue)

					nodes = []corev1.Node{
						*testingnode.MakeNode("x1").
							Label("node-group", "tas").
							Label(utiltesting.DefaultBlockTopologyLevel, "b1").
							Label(utiltesting.DefaultRackTopologyLevel, "r1").
							Label(corev1.LabelHostname, "x1").
							StatusAllocatable(corev1.ResourceList{
								"nvidia.com/gpu":      resource.MustParse("4"),
								corev1.ResourceCPU:    resource.MustParse("1"),
								corev1.ResourceMemory: resource.MustParse("1Gi"),
								corev1.ResourcePods:   resource.MustParse("10"),
							}).
							Ready().
							Obj(),
						// Sibling node in the SAME rack so the rack-level
						// capacity sum mixes a healthy leaf with the
						// over-subscribed leaf. Without the negative-count
						// clamp, x1's state goes to -4 and cancels x2's +4 at
						// the rack/block level, making a rack-required workload
						// look unschedulable even though x2 has free capacity.
						*testingnode.MakeNode("x2").
							Label("node-group", "tas").
							Label(utiltesting.DefaultBlockTopologyLevel, "b1").
							Label(utiltesting.DefaultRackTopologyLevel, "r1").
							Label(corev1.LabelHostname, "x2").
							StatusAllocatable(corev1.ResourceList{
								"nvidia.com/gpu":      resource.MustParse("8"),
								corev1.ResourceCPU:    resource.MustParse("1"),
								corev1.ResourceMemory: resource.MustParse("1Gi"),
								corev1.ResourcePods:   resource.MustParse("10"),
							}).
							Ready().
							Obj(),
					}
					integration.CreateNodesWithStatus(ctx, k8sClient, nodes)
				})

				ginkgo.AfterEach(func() {
					gomega.Expect(behavioral.DeleteAllJobsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
					gomega.Expect(behavioral.DeleteAllPodsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
					gomega.Expect(behavioral.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
					gomega.Expect(behavioral.DeleteObject(ctx, k8sClient, localQueue)).Should(gomega.Succeed())
					behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
					behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, tasFlavor, true)
					behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, topology, true)
					for _, node := range nodes {
						behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, &node, true)
					}
				})

				ginkgo.It("should not produce negative TopologyDomain.Count when allocatable drops to 0 mid-workload", func() {
					var (
						wl1 *kueue.Workload
						wl2 *kueue.Workload
					)

					ginkgo.By("admitting wl1 that consumes all GPUs on x1", func() {
						wl1 = utiltestingapi.MakeWorkload("wl1", ns.Name).
							PodSets(*utiltestingapi.MakePodSet("worker", 4).
								RequiredTopologyRequest(corev1.LabelHostname).
								Request("nvidia.com/gpu", "1").
								Obj()).
							Queue(kueue.LocalQueueName(localQueue.Name)).Obj()
						behavioral.MustCreate(ctx, k8sClient, wl1)
						behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1)
						gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl1), wl1)).To(gomega.Succeed())
						gomega.Expect(wl1.Status.Admission.PodSetAssignments[0].TopologyAssignment).Should(gomega.BeComparableTo(
							utiltas.V1Beta2From(&utiltas.TopologyAssignment{
								Levels: []string{corev1.LabelHostname},
								Domains: []utiltas.TopologyDomainAssignment{
									{Count: 4, Values: []string{"x1"}},
								},
							}),
						))
					})

					ginkgo.By("creating running pods so cache observes the GPU usage on x1", func() {
						createPodsForWorkload(wl1, ns.Name, false, true)
					})

					ginkgo.By("simulating a device-plugin failure: drop x1 nvidia.com/gpu allocatable to 0", func() {
						gomega.Eventually(func(g gomega.Gomega) {
							nodeToUpdate := &corev1.Node{}
							g.Expect(k8sClient.Get(ctx, apitypes.NamespacedName{Name: "x1"}, nodeToUpdate)).Should(gomega.Succeed())
							nodeToUpdate.Status.Allocatable["nvidia.com/gpu"] = resource.MustParse("0")
							g.Expect(k8sClient.Status().Update(ctx, nodeToUpdate)).Should(gomega.Succeed())
						}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
					})

					// Submit wl2 with rack-required topology so the placement
					// algorithm must sum child leaf states at the rack level.
					// Without the clamp, x1 contributes -4 and cancels out
					// x2's +4 — making the rack appear unschedulable even
					// though x2 has 4 free GPUs. With the clamp, rack capacity
					// is 0+4=4 and wl2 lands on x2.
					ginkgo.By("submitting wl2 (4 pods, slice-required topology), exposing the negative-leaf walk-down escape", func() {
						wl2 = utiltestingapi.MakeWorkload("wl2", ns.Name).
							PodSets(*utiltestingapi.MakePodSet("worker", 4).
								SliceRequiredTopologyRequest(utiltesting.DefaultRackTopologyLevel).
								SliceSizeTopologyRequest(1).
								Request("nvidia.com/gpu", "1").
								Obj()).
							Queue(kueue.LocalQueueName(localQueue.Name)).Obj()
						behavioral.MustCreate(ctx, k8sClient, wl2)
					})

					ginkgo.By("verifying wl2 lands on the healthy node x2 (rack capacity must not be hidden by negative leaf state)", func() {
						behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl2)
						gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl2), wl2)).To(gomega.Succeed())
						gomega.Expect(wl2.Status.Admission.PodSetAssignments[0].TopologyAssignment).Should(gomega.BeComparableTo(
							utiltas.V1Beta2From(&utiltas.TopologyAssignment{
								Levels: []string{corev1.LabelHostname},
								Domains: []utiltas.TopologyDomainAssignment{
									{Count: 4, Values: []string{"x2"}},
								},
							}),
						))
					})
				})
			})
		})
	})
})
