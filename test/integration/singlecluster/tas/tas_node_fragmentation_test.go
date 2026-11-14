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
	"fmt"
	"slices"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	utiltas "sigs.k8s.io/kueue/pkg/util/tas"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingnode "sigs.k8s.io/kueue/pkg/util/testingjobs/node"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/test/integration/framework"
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
		// The purpose of this test is to demonstrate a TAS use case
		// for just the host scope of fragmentation
		ginkgo.When("Testing node resource fragmentation", func() {
			var (
				nodes []corev1.Node
			)

			ginkgo.BeforeEach(func() {
				// 1. Create 8 fake nodes with 8 GPUs each. (8 nodes * 8 GPU = 64 GPUs)
				// 2. Per node, schedule a workload that uses 7 of the 8 GPUs. (7 x 8 = 56 GPU allocated)
				// This means:
				// There are 8 total GPUs unallocated, however each GPU is on a different node.
				nodes = make([]corev1.Node, 8)
				for i := range 8 {
					nodes[i] = *testingnode.MakeNode(fmt.Sprintf("fake-gpu-node-%d", i+1)).
						Label("nodepool", "gpu-nodes").
						Label(corev1.LabelHostname, fmt.Sprintf("fake-gpu-node-%d", i+1)).
						StatusAllocatable(corev1.ResourceList{
							"nvidia.com/gpu":    resource.MustParse("8"), // 8 GPUs per node
							corev1.ResourcePods: resource.MustParse("10"),
						}).
						StatusConditions(corev1.NodeCondition{
							Type:               corev1.NodeReady,
							Status:             corev1.ConditionTrue,
							LastTransitionTime: metav1.NewTime(time.Now()),
							Reason:             "KubeletReady",
							Message:            "kubelet is posting ready status",
						}).
						Obj()
				}

				ginkgo.By("Creating nodes first", func() {
					integration.CreateNodesWithStatus(ctx, k8sClient, nodes)

					// Verify all nodes are properly recognized by TAS system
					gomega.Eventually(func(g gomega.Gomega) {
						var nodeList corev1.NodeList
						g.Expect(k8sClient.List(ctx, &nodeList)).To(gomega.Succeed())
						g.Expect(nodeList.Items).To(gomega.HaveLen(8))
						for _, node := range nodeList.Items {
							g.Expect(utiltas.IsNodeStatusConditionTrue(node.Status.Conditions, corev1.NodeReady)).To(gomega.BeTrue())
						}
					}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
				})
			})

			ginkgo.AfterEach(func() {
				for i := range nodes {
					behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, &nodes[i], true)
				}
			})

			ginkgo.Context("Without Topology Aware Scheduling", func() {
				var (
					nonTasFlavor       *kueue.ResourceFlavor
					nonTasClusterQueue *kueue.ClusterQueue
					nonTasLocalQueue   *kueue.LocalQueue
				)

				ginkgo.BeforeEach(func() {
					// Create a ResourceFlavor without topology
					nonTasFlavor = utiltestingapi.MakeResourceFlavor("non-tas-gpu-flavor").
						NodeLabel("nodepool", "gpu-nodes").
						// No TopologyName - this simulates non-TAS behavior
						Obj()
					behavioral.MustCreate(ctx, k8sClient, nonTasFlavor)

					nonTasClusterQueue = utiltestingapi.MakeClusterQueue("non-tas-gpu-cluster-queue").
						ResourceGroup(
							*utiltestingapi.MakeFlavorQuotas("non-tas-gpu-flavor").
								Resource("nvidia.com/gpu", "64"). // Total 64 GPUs available (8 nodes × 8 GPUs)
								Obj(),
						).
						Obj()
					behavioral.MustCreate(ctx, k8sClient, nonTasClusterQueue)
					behavioral.ExpectClusterQueuesToBeActive(ctx, k8sClient, nonTasClusterQueue)

					nonTasLocalQueue = utiltestingapi.MakeLocalQueue("non-tas-gpu-queue", ns.Name).
						ClusterQueue("non-tas-gpu-cluster-queue").
						Obj()
					behavioral.MustCreate(ctx, k8sClient, nonTasLocalQueue)

					// Create existing workloads that consume 7 GPUs on each node
					// This simulates the scenario where only 1 GPU is available per node
					for i := range 8 {
						existingWl := utiltestingapi.MakeWorkload(fmt.Sprintf("existing-workload-%d", i+1), ns.Name).
							Queue(kueue.LocalQueueName(nonTasLocalQueue.Name)).
							PodSets(*utiltestingapi.MakePodSet("worker", 1).
								Request("nvidia.com/gpu", "7"). // Single pod needs 7 GPUs
								NodeSelector(map[string]string{
									"nodepool":           "gpu-nodes",
									corev1.LabelHostname: fmt.Sprintf("fake-gpu-node-%d", i+1),
								}).
								Obj()).
							Obj()
						behavioral.MustCreate(ctx, k8sClient, existingWl)
						behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, existingWl)
					}
				})

				ginkgo.AfterEach(func() {
					gomega.Expect(behavioral.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
					behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, nonTasClusterQueue, true)
					behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, nonTasFlavor, true)
				})

				ginkgo.It("Should demonstrate fragmentation issue", framework.SlowSpec, func() {
					// Create a 1 pod workload that needs 8 GPUs on a single node
					// This should cause fragmentation because:
					// - Kueue sees 8 GPUs available total (1 GPU per node across 8 nodes)
					// - But a single pod requiring 8 GPUs cannot fit on any single node
					wl := utiltestingapi.MakeWorkload("fragmented-workload", ns.Name).
						Queue(kueue.LocalQueueName(nonTasLocalQueue.Name)).
						PodSets(*utiltestingapi.MakePodSet("worker", 1).
							Request("nvidia.com/gpu", "8"). // Single pod needs 8 GPUs
							NodeSelector(map[string]string{"nodepool": "gpu-nodes"}).
							Obj()).
						Obj()

					ginkgo.By("Creating the workload that requires all 8 GPUs", func() {
						behavioral.MustCreate(ctx, k8sClient, wl)
					})

					ginkgo.By("Verifying the workload gets admitted (Kueue thinks it can schedule)", func() {
						// Without TAS, Kueue should admit this because it sees 8 GPUs are available
						// irrespective of quota topology
						behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl)
						// We expect 9 total workloads: 8 existing (7 GPUs each) + 1 new (8 GPUs)
						// Total: 8×7 + 1×8 = 64 GPUs, which fits in the 64 GPU quota
						behavioral.ExpectReservingActiveWorkloadsMetric(nonTasClusterQueue, 9)
					})

					ginkgo.By("Verifying admission shows resource assignment", func() {
						gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), wl)).To(gomega.Succeed())
						gomega.Expect(wl.Status.Admission).ShouldNot(gomega.BeNil())
						gomega.Expect(wl.Status.Admission.PodSetAssignments).Should(gomega.HaveLen(1))
						gomega.Expect(wl.Status.Admission.PodSetAssignments[0].Flavors).Should(gomega.Equal(
							map[corev1.ResourceName]kueue.ResourceFlavorReference{
								"nvidia.com/gpu": "non-tas-gpu-flavor",
							},
						))
						// Without TAS, there should be no TopologyAssignment
						gomega.Expect(wl.Status.Admission.PodSetAssignments[0].TopologyAssignment).Should(gomega.BeNil())
					})

					ginkgo.By("Demonstrating the problem: workload is admitted but cannot actually be scheduled", func() {
						// This test demonstrates the core issue:
						// 1. Kueue sees 8 GPUs available total (1 per node across 8 nodes) and admits the 1 pod workload
						// 2. But the single pod needs 8 GPUs in 1 node, and no single node has 8 available GPUs
						// 3. Each node only has 1 GPU available (the other 7 are busy with existing workloads)
						// 4. This leads to the pod being unschedulable, and will eventually reach scheduling timeout in
						//    a real world scenario

						// The workload is admitted, showing Kueue thinks it can be scheduled
						gomega.Expect(workload.HasQuotaReservation(wl)).Should(gomega.BeTrue())
					})
				})
			})

			ginkgo.Context("With Topology-Aware Scheduling", func() {
				var (
					tasFlavor    *kueue.ResourceFlavor
					topology     *kueue.Topology
					localQueue   *kueue.LocalQueue
					clusterQueue *kueue.ClusterQueue
				)
				ginkgo.Context("Single pod fitting into single node scenario", func() {
					ginkgo.BeforeEach(func() {
						ginkgo.By("Creating TAS topology", func() {
							topology = utiltestingapi.MakeDefaultOneLevelTopology("node")
							behavioral.MustCreate(ctx, k8sClient, topology)
						})

						ginkgo.By("Creating TAS ResourceFlavor", func() {
							tasFlavor = utiltestingapi.MakeResourceFlavor("gpu-flavor").
								NodeLabel("nodepool", "gpu-nodes").
								TopologyName("node").
								Obj()
							behavioral.MustCreate(ctx, k8sClient, tasFlavor)
						})

						ginkgo.By("Creating ClusterQueue and LocalQueue", func() {
							clusterQueue = utiltestingapi.MakeClusterQueue("gpu-cluster-queue").
								ResourceGroup(
									*utiltestingapi.MakeFlavorQuotas("gpu-flavor").
										Resource("nvidia.com/gpu", "64"). // Total 64 GPUs available (8 nodes × 8 GPUs)
										Obj(),
								).
								Obj()
							behavioral.MustCreate(ctx, k8sClient, clusterQueue)
							behavioral.ExpectClusterQueuesToBeActive(ctx, k8sClient, clusterQueue)

							localQueue = utiltestingapi.MakeLocalQueue("gpu-queue", ns.Name).
								ClusterQueue("gpu-cluster-queue").
								Obj()
							behavioral.MustCreate(ctx, k8sClient, localQueue)
						})

						// Verify all nodes are properly recognized by TAS system
						gomega.Eventually(func(g gomega.Gomega) {
							var nodeList corev1.NodeList
							g.Expect(k8sClient.List(ctx, &nodeList)).To(gomega.Succeed())
							g.Expect(nodeList.Items).To(gomega.HaveLen(8))
							for _, node := range nodeList.Items {
								g.Expect(utiltas.IsNodeStatusConditionTrue(node.Status.Conditions, corev1.NodeReady)).To(gomega.BeTrue())
							}
						}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

						// Create existing workloads that consume 7 GPUs on each node
						// This simulates the scenario where only 1 GPU is available per node
						for i := range 8 {
							existingWl := utiltestingapi.MakeWorkload(fmt.Sprintf("existing-tas-workload-%d", i+1), ns.Name).
								Queue(kueue.LocalQueueName(localQueue.Name)).
								PodSets(*utiltestingapi.MakePodSet("worker", 1).
									Request("nvidia.com/gpu", "7").                // Single pod needs 7 GPUs
									RequiredTopologyRequest(corev1.LabelHostname). // Require hostname topology
									Obj()).
								Obj()
							behavioral.MustCreate(ctx, k8sClient, existingWl)
							behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, existingWl)
						}
					})

					ginkgo.AfterEach(func() {
						gomega.Expect(behavioral.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
						behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
						behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, tasFlavor, true)
						behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, topology, true)
					})

					ginkgo.It("Should prevent unschedulable admission by rejecting 8-GPU workload that requires 8-GPUs on single node", framework.SlowSpec, func() {
						// Create a workload that needs 8 GPUs on a single pod
						// With TAS, this should be rejected because no single node has 8 available GPUs
						wl := utiltestingapi.MakeWorkload("tas-workload", ns.Name).
							Queue(kueue.LocalQueueName(localQueue.Name)).
							PodSets(*utiltestingapi.MakePodSet("worker", 1).
								Request("nvidia.com/gpu", "8"). // Single pod needs 8 GPUs
								NodeSelector(map[string]string{"nodepool": "gpu-nodes"}).
								RequiredTopologyRequest(corev1.LabelHostname). // Require pod on single hostname
								Obj()).
							Obj()

						ginkgo.By("Creating the workload with TAS hostname requirement", func() {
							behavioral.MustCreate(ctx, k8sClient, wl)
						})

						ginkgo.By("Verifying the workload is not admitted due to topology constraints", func() {
							// With TAS, Kueue should not admit this because no single node has 8 GPUs
							behavioral.ExpectWorkloadsToBePending(ctx, k8sClient, wl)
							behavioral.ExpectPendingWorkloadsMetric(clusterQueue, 0, 1)
						})

						ginkgo.By("Verifying workload remains pending (preventing unscheduable admission thus preventing resource waste)", func() {
							// The workload should remain pending
							gomega.Consistently(func(g gomega.Gomega) {
								g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), wl)).To(gomega.Succeed())
								g.Expect(workload.HasQuotaReservation(wl)).Should(gomega.BeFalse())
							}, behavioral.ConsistentDuration, behavioral.ShortInterval).Should(gomega.Succeed())
						})
					})

					ginkgo.It("Should allow workload that fits within topology constraints", framework.SlowSpec, func() {
						// Create a workload that needs only 1 GPU (fits on any single node)
						wl := utiltestingapi.MakeWorkload("tas-fitting-workload", ns.Name).
							Queue(kueue.LocalQueueName(localQueue.Name)).
							PodSets(*utiltestingapi.MakePodSet("worker", 1).
								Request("nvidia.com/gpu", "1").
								NodeSelector(map[string]string{"nodepool": "gpu-nodes"}).
								RequiredTopologyRequest(corev1.LabelHostname). // Require pod on single hostname
								Obj()).
							Obj()

						ginkgo.By("Creating the workload with TAS hostname requirement that fits", func() {
							behavioral.MustCreate(ctx, k8sClient, wl)
						})

						ginkgo.By("Verifying the workload IS admitted and runs successfully", func() {
							// This should work because 1 GPU fits on any single node
							behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl)
							behavioral.ExpectReservingActiveWorkloadsMetric(clusterQueue, 9) // 8 original workloads, 9th 1 GPU workload here
						})

						ginkgo.By("Verifying admission contains topology assignment", func() {
							gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), wl)).To(gomega.Succeed())
							gomega.Expect(wl.Status.Admission).ShouldNot(gomega.BeNil())
							gomega.Expect(wl.Status.Admission.PodSetAssignments).Should(gomega.HaveLen(1))
							gomega.Expect(wl.Status.Admission.PodSetAssignments[0].TopologyAssignment).ShouldNot(gomega.BeNil())
							gomega.Expect(wl.Status.Admission.PodSetAssignments[0].TopologyAssignment.Levels).Should(gomega.Equal([]string{corev1.LabelHostname}))
							gomega.Expect(slices.Collect(utiltas.PodCounts(wl.Status.Admission.PodSetAssignments[0].TopologyAssignment))).To(gomega.Equal([]int32{1}))
						})
					})
				})

				ginkgo.Context("Preemption with fragmentation scenario", func() {
					var (
						nodes             []corev1.Node
						topology          *kueue.Topology
						tasFlavor         *kueue.ResourceFlavor
						localQueue        *kueue.LocalQueue
						clusterQueue      *kueue.ClusterQueue
						highPriorityClass *kueue.WorkloadPriorityClass
						lowPriorityClass  *kueue.WorkloadPriorityClass
						allWorkloads      []*kueue.Workload
					)

					ginkgo.BeforeEach(func() {
						ginkgo.By("Creating 4 GPU nodes with 8 GPUs each")
						nodes = make([]corev1.Node, 4)
						for i := range 4 {
							nodes[i] = *testingnode.MakeNode(fmt.Sprintf("preempt-gpu-node-%d", i+1)).
								Label("nodepool", "preemption-gpu-nodes").
								Label(corev1.LabelHostname, fmt.Sprintf("preempt-gpu-node-%d", i+1)).
								StatusAllocatable(corev1.ResourceList{
									"nvidia.com/gpu":    resource.MustParse("8"),
									corev1.ResourcePods: resource.MustParse("20"),
								}).
								StatusConditions(corev1.NodeCondition{
									Type:               corev1.NodeReady,
									Status:             corev1.ConditionTrue,
									LastTransitionTime: metav1.NewTime(time.Now()),
									Reason:             "KubeletReady",
									Message:            "kubelet is posting ready status",
								}).
								Obj()
						}
						integration.CreateNodesWithStatus(ctx, k8sClient, nodes)

						gomega.Eventually(func(g gomega.Gomega) {
							var nodeList corev1.NodeList
							g.Expect(k8sClient.List(ctx, &nodeList, client.MatchingLabels{
								"nodepool": "preemption-gpu-nodes",
							})).To(gomega.Succeed())
							g.Expect(nodeList.Items).To(gomega.HaveLen(4))
						}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

						ginkgo.By("Creating TAS topology")
						topology = utiltestingapi.MakeDefaultOneLevelTopology("preemption-topology")
						behavioral.MustCreate(ctx, k8sClient, topology)

						ginkgo.By("Creating TAS ResourceFlavor")
						tasFlavor = utiltestingapi.MakeResourceFlavor("preemption-gpu-flavor").
							NodeLabel("nodepool", "preemption-gpu-nodes").
							TopologyName("preemption-topology").
							Obj()
						behavioral.MustCreate(ctx, k8sClient, tasFlavor)

						ginkgo.By("Creating WorkloadPriorityClasses")
						highPriorityClass = utiltestingapi.MakeWorkloadPriorityClass("high-priority").
							PriorityValue(100).
							Obj()
						behavioral.MustCreate(ctx, k8sClient, highPriorityClass)

						lowPriorityClass = utiltestingapi.MakeWorkloadPriorityClass("low-priority").
							PriorityValue(10).
							Obj()
						behavioral.MustCreate(ctx, k8sClient, lowPriorityClass)

						ginkgo.By("Creating ClusterQueue with preemption enabled")
						clusterQueue = utiltestingapi.MakeClusterQueue("preemption-gpu-cq").
							ResourceGroup(
								*utiltestingapi.MakeFlavorQuotas("preemption-gpu-flavor").
									Resource("nvidia.com/gpu", "32").
									Obj(),
							).
							Preemption(kueue.ClusterQueuePreemption{
								WithinClusterQueue: kueue.PreemptionPolicyLowerPriority,
							}).
							Obj()
						behavioral.MustCreate(ctx, k8sClient, clusterQueue)
						behavioral.ExpectClusterQueuesToBeActive(ctx, k8sClient, clusterQueue)

						ginkgo.By("Creating LocalQueue")
						localQueue = utiltestingapi.MakeLocalQueue("preemption-gpu-queue", ns.Name).
							ClusterQueue("preemption-gpu-cq").
							Obj()
						behavioral.MustCreate(ctx, k8sClient, localQueue)

						ginkgo.By("Creating 24 high-priority workloads (6 per node)")
						highPriorityWorkloads := make([]*kueue.Workload, 24)
						for i := range 24 {
							nodeIndex := i / 6 // Distribute: 6 workloads per node
							nodeName := fmt.Sprintf("preempt-gpu-node-%d", nodeIndex+1)
							wl := utiltestingapi.MakeWorkload(fmt.Sprintf("high-priority-wl-%d", i+1), ns.Name).
								Queue(kueue.LocalQueueName(localQueue.Name)).
								WorkloadPriorityClassRef("high-priority").
								Priority(100).
								PodSets(*utiltestingapi.MakePodSet("worker", 1).
									Request("nvidia.com/gpu", "1").
									NodeSelector(map[string]string{
										"nodepool":           "preemption-gpu-nodes",
										corev1.LabelHostname: nodeName,
									}).
									RequiredTopologyRequest(corev1.LabelHostname).
									Obj()).
								Obj()
							behavioral.MustCreate(ctx, k8sClient, wl)
							highPriorityWorkloads[i] = wl
						}

						ginkgo.By("Creating 8 low-priority workloads (2 per node)")
						lowPriorityWorkloads := make([]*kueue.Workload, 8)
						for i := range 8 {
							nodeIndex := i / 2 // Distribute: 2 workloads per node
							nodeName := fmt.Sprintf("preempt-gpu-node-%d", nodeIndex+1)
							wl := utiltestingapi.MakeWorkload(fmt.Sprintf("low-priority-wl-%d", i+1), ns.Name).
								Queue(kueue.LocalQueueName(localQueue.Name)).
								WorkloadPriorityClassRef("low-priority").
								Priority(10).
								PodSets(*utiltestingapi.MakePodSet("worker", 1).
									Request("nvidia.com/gpu", "1").
									NodeSelector(map[string]string{
										"nodepool":           "preemption-gpu-nodes",
										corev1.LabelHostname: nodeName,
									}).
									RequiredTopologyRequest(corev1.LabelHostname).
									Obj()).
								Obj()
							behavioral.MustCreate(ctx, k8sClient, wl)
							lowPriorityWorkloads[i] = wl
						}

						ginkgo.By("Waiting for all workloads to be admitted")
						allWorkloads = make([]*kueue.Workload, 0, 32)
						allWorkloads = append(allWorkloads, highPriorityWorkloads...)
						allWorkloads = append(allWorkloads, lowPriorityWorkloads...)
						behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, allWorkloads...)
						behavioral.ExpectReservingActiveWorkloadsMetric(clusterQueue, 32)
					})

					ginkgo.AfterEach(func() {
						gomega.Expect(behavioral.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
						behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, localQueue, true)
						behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
						behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, highPriorityClass, true)
						behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, lowPriorityClass, true)
						behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, tasFlavor, true)
						behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, topology, true)
						for i := range nodes {
							behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, &nodes[i], true)
						}
					})

					ginkgo.It("Should reject workload with required topology after preemption causes fragmentation", framework.SlowSpec, func() {
						// Test scenario:
						// - 4 nodes with 8 GPUs each
						// - 24 high-priority 1-GPU workloads (6 per node)
						// - 8 low-priority 1-GPU workloads (2 per node)
						// - New high-priority workload with 8 pods × 1 GPU each (required topology)
						//
						// Expected behavior:
						// Even after preempting 8 low-priority workloads, the freed GPUs would be
						// fragmented (2 per node across 4 nodes). Since the workload requires all
						// 8 pods on a single node, and no single node can accommodate 8 pods,
						// TAS should reject it without preemption.

						wl := utiltestingapi.MakeWorkload("high-priority-required-wl", ns.Name).
							Queue(kueue.LocalQueueName(localQueue.Name)).
							WorkloadPriorityClassRef("high-priority").
							Priority(100).
							PodSets(*utiltestingapi.MakePodSet("worker", 8).
								Request("nvidia.com/gpu", "1").
								NodeSelector(map[string]string{"nodepool": "preemption-gpu-nodes"}).
								RequiredTopologyRequest(corev1.LabelHostname).
								Obj()).
							Obj()

						ginkgo.By("Creating the high-priority workload with required topology")
						behavioral.MustCreate(ctx, k8sClient, wl)

						ginkgo.By("Verifying the workload is not admitted due to topology constraints")
						// The workload should remain pending because TAS recognizes that
						// even after preemption, resources would be fragmented
						behavioral.ExpectWorkloadsToBePending(ctx, k8sClient, wl)
						behavioral.ExpectPendingWorkloadsMetric(clusterQueue, 0, 1)

						ginkgo.By("Verifying workload stays pending (no quota reservation)")
						updatedWl := &kueue.Workload{}
						gomega.Consistently(func(g gomega.Gomega) {
							g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), updatedWl)).To(gomega.Succeed())
							g.Expect(workload.HasQuotaReservation(updatedWl)).To(gomega.BeFalse())
						}, behavioral.ConsistentDuration, behavioral.ShortInterval).Should(gomega.Succeed())

						ginkgo.By("Verifying no low-priority workloads were evicted")
						// Since the high-priority workload cannot be admitted even with preemption,
						// no workloads should be evicted
						gomega.Consistently(func(g gomega.Gomega) {
							evictedWorkloads := behavioral.FilterEvictedWorkloads(ctx, k8sClient, allWorkloads...)
							g.Expect(evictedWorkloads).To(gomega.BeEmpty())
						}, behavioral.ConsistentDuration, behavioral.ShortInterval).Should(gomega.Succeed())
					})

					ginkgo.It("Should admit workload with preferred topology after preemption", func() {
						// Test scenario:
						// - Same setup: 4 nodes with 8 GPUs each
						// - 24 high-priority 1-GPU workloads (6 per node)
						// - 8 low-priority 1-GPU workloads (2 per node)
						// - New high-priority workload with 8 pods × 1 GPU each (preferred topology)
						//
						// Expected behavior:
						// With preferred topology, TAS allows the 8 pods to be distributed across
						// multiple nodes. It should preempt the 8 low-priority workloads (2 per node)
						// and successfully admit the workload with 2 pods per node across 4 nodes.

						wl := utiltestingapi.MakeWorkload("high-priority-preferred-wl", ns.Name).
							Queue(kueue.LocalQueueName(localQueue.Name)).
							WorkloadPriorityClassRef("high-priority").
							Priority(100).
							PodSets(*utiltestingapi.MakePodSet("worker", 8).
								Request("nvidia.com/gpu", "1").
								NodeSelector(map[string]string{"nodepool": "preemption-gpu-nodes"}).
								PreferredTopologyRequest(corev1.LabelHostname).
								Obj()).
							Obj()

						ginkgo.By("Creating the high-priority workload with preferred topology")
						behavioral.MustCreate(ctx, k8sClient, wl)

						ginkgo.By("Verifying low-priority workloads are evicted")
						// With preferred topology, TAS should preempt low-priority workloads
						// to distribute the 8 pods across nodes
						var evictedWorkloads []*kueue.Workload
						gomega.Eventually(func(g gomega.Gomega) {
							evictedWorkloads = behavioral.FilterEvictedWorkloads(ctx, k8sClient, allWorkloads...)
							g.Expect(evictedWorkloads).To(gomega.HaveLen(8))
						}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

						ginkgo.By("Finishing eviction for preempted workloads")
						behavioral.FinishEvictionForWorkloads(ctx, k8sClient, evictedWorkloads...)

						ginkgo.By("Verifying the workload is admitted")
						// The workload should be admitted and distributed across multiple nodes
						behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl)
						behavioral.ExpectReservingActiveWorkloadsMetric(clusterQueue, 25) // 24 high-priority + 1 new workload
					})
				})

				ginkgo.Context("Multiple pods in 1 workload TAS scenario", func() {
					var (
						tasFlavor    *kueue.ResourceFlavor
						topology     *kueue.Topology
						localQueue   *kueue.LocalQueue
						clusterQueue *kueue.ClusterQueue
					)

					ginkgo.BeforeEach(func() {
						// Create TAS topology
						topology = utiltestingapi.MakeDefaultOneLevelTopology("gpu-topology")
						behavioral.MustCreate(ctx, k8sClient, topology)

						// Create TAS ResourceFlavor
						tasFlavor = utiltestingapi.MakeResourceFlavor("gpu-tas-flavor").
							NodeLabel("nodepool", "gpu-nodes").
							TopologyName("gpu-topology").
							Obj()
						behavioral.MustCreate(ctx, k8sClient, tasFlavor)

						// Create ClusterQueue and LocalQueue
						clusterQueue = utiltestingapi.MakeClusterQueue("gpu-tas-cluster-queue").
							ResourceGroup(
								*utiltestingapi.MakeFlavorQuotas("gpu-tas-flavor").
									Resource("nvidia.com/gpu", "64"). // Total 64 GPUs available (8 nodes × 8 GPUs)
									Obj(),
							).
							Obj()
						behavioral.MustCreate(ctx, k8sClient, clusterQueue)
						behavioral.ExpectClusterQueuesToBeActive(ctx, k8sClient, clusterQueue)

						localQueue = utiltestingapi.MakeLocalQueue("gpu-tas-queue", ns.Name).
							ClusterQueue("gpu-tas-cluster-queue").
							Obj()
						behavioral.MustCreate(ctx, k8sClient, localQueue)

						// Verify all nodes are properly recognized by TAS system
						gomega.Eventually(func(g gomega.Gomega) {
							var nodeList corev1.NodeList
							g.Expect(k8sClient.List(ctx, &nodeList)).To(gomega.Succeed())
							g.Expect(nodeList.Items).To(gomega.HaveLen(8))
							for _, node := range nodeList.Items {
								g.Expect(utiltas.IsNodeStatusConditionTrue(node.Status.Conditions, corev1.NodeReady)).To(gomega.BeTrue())
							}
						}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

						// Create 7 workloads that consume 7 GPUs each on different nodes
						// Here, 7 nodes have 1 GPU available each, and 1 node has 8 GPUs available
						for i := range 7 {
							existingWl := utiltestingapi.MakeWorkload(fmt.Sprintf("existing-7gpu-workload-%d", i+1), ns.Name).
								Queue(kueue.LocalQueueName(localQueue.Name)).
								PodSets(*utiltestingapi.MakePodSet("worker", 1).
									Request("nvidia.com/gpu", "7").                // Single pod needs 7 GPUs
									RequiredTopologyRequest(corev1.LabelHostname). // Require hostname topology
									Obj()).
								Obj()
							behavioral.MustCreate(ctx, k8sClient, existingWl)
							behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, existingWl)
						}
					})

					ginkgo.AfterEach(func() {
						gomega.Expect(behavioral.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
						behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
						behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, tasFlavor, true)
						behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, topology, true)
					})

					ginkgo.It("Should handle workload with 8-GPU pod and 7-GPU pod requirements", func() {
						// Create a workload with two pods: one needing 8 GPUs and another needing 7 GPUs
						// With TAS, this should be rejected because:
						// - The 8-GPU pod can fit on the one remaining node with 8 GPUs available
						// - But the 7-GPU pod cannot fit on any single node (7 nodes have only 1 GPU available each)
						wl := utiltestingapi.MakeWorkload("mixed-gpu-workload", ns.Name).
							Queue(kueue.LocalQueueName(localQueue.Name)).
							Obj()

						// First pod set: 1 pod needing 8 GPUs
						ps1 := *utiltestingapi.MakePodSet("gpu8-worker", 1).
							Request("nvidia.com/gpu", "8"). // Single pod needs 8 GPUs
							NodeSelector(map[string]string{"nodepool": "gpu-nodes"}).
							RequiredTopologyRequest(corev1.LabelHostname). // Require pod on single hostname
							Obj()

						// Second pod set: 1 pod needing 7 GPUs
						ps2 := *utiltestingapi.MakePodSet("gpu7-worker", 1).
							Request("nvidia.com/gpu", "7"). // Single pod needs 7 GPUs
							NodeSelector(map[string]string{"nodepool": "gpu-nodes"}).
							RequiredTopologyRequest(corev1.LabelHostname). // Require pod on single hostname
							Obj()

						wl.Spec.PodSets = []kueue.PodSet{ps1, ps2}

						ginkgo.By("Creating the workload with mixed GPU requirements", func() {
							behavioral.MustCreate(ctx, k8sClient, wl)
						})

						ginkgo.By("Verifying the workload is not admitted due to topology constraints", func() {
							// With TAS, Kueue should not admit this because:
							// - The 8-GPU pod can fit on the one remaining node with 8 GPUs available
							// - But the 7-GPU pod cannot fit on any single node (7 nodes have only 1 GPU available each)
							// - TAS prevents admission when not all pods can be scheduled
							behavioral.ExpectWorkloadsToBePending(ctx, k8sClient, wl)
							behavioral.ExpectPendingWorkloadsMetric(clusterQueue, 0, 1)
						})

						ginkgo.By("Verifying workload remains pending (preventing unschedulable admission)", func() {
							// The workload should remain pending, demonstrating TAS prevents
							// admission of workloads that cannot actually be scheduled
							gomega.Eventually(func(g gomega.Gomega) {
								g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), wl)).To(gomega.Succeed())
								g.Expect(workload.HasQuotaReservation(wl)).Should(gomega.BeFalse())
							}, behavioral.ConsistentDuration, behavioral.ShortInterval).Should(gomega.Succeed())
						})
					})
					ginkgo.It("Should admit workload with 8-GPU pod and 1-GPU pod requirements", framework.SlowSpec, func() {
						// Create a workload with two pods: one needing 8 GPUs and another needing 1 GPU
						// With TAS, this should be admitted because:
						// - The 8-GPU pod can fit on the one remaining node with 8 GPUs available
						// - The 1-GPU pod can fit on any of the 7 nodes with 1 GPU available each
						// - TAS allows admission when all pods can be scheduled
						wl := utiltestingapi.MakeWorkload("admissible-mixed-gpu-workload", ns.Name).
							Queue(kueue.LocalQueueName(localQueue.Name)).
							Obj()

						// First pod set: 1 pod needing 8 GPUs
						ps1 := *utiltestingapi.MakePodSet("gpu8-worker", 1).
							Request("nvidia.com/gpu", "8"). // Single pod needs 8 GPUs
							NodeSelector(map[string]string{"nodepool": "gpu-nodes"}).
							RequiredTopologyRequest(corev1.LabelHostname). // Require pod on single hostname
							Obj()

						// Second pod set: 1 pod needing 1 GPU
						ps2 := *utiltestingapi.MakePodSet("gpu1-worker", 1).
							Request("nvidia.com/gpu", "1"). // Single pod needs 1 GPU
							NodeSelector(map[string]string{"nodepool": "gpu-nodes"}).
							RequiredTopologyRequest(corev1.LabelHostname). // Require pod on single hostname
							Obj()

						wl.Spec.PodSets = []kueue.PodSet{ps1, ps2}

						ginkgo.By("Creating the workload with 8+1 GPU requirements", func() {
							behavioral.MustCreate(ctx, k8sClient, wl)
						})

						ginkgo.By("Verifying the workload is admitted due to schedulable topology constraints", func() {
							// With TAS, Kueue should admit this because:
							// - The 8-GPU pod can fit on the one remaining node with 8 GPUs available
							// - The 1-GPU pod can fit on any of the 7 nodes with 1 GPU available each
							// - TAS allows admission when all pods can be scheduled
							behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl)
							behavioral.ExpectReservingActiveWorkloadsMetric(clusterQueue, 8) // 7 existing + 1 new workload
						})

						ginkgo.By("Verifying admission contains correct topology assignments", func() {
							gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), wl)).To(gomega.Succeed())
							gomega.Expect(wl.Status.Admission).ShouldNot(gomega.BeNil())
							gomega.Expect(wl.Status.Admission.PodSetAssignments).Should(gomega.HaveLen(2))

							// First pod set (8 GPUs) should be assigned to a single node
							gomega.Expect(wl.Status.Admission.PodSetAssignments[0].TopologyAssignment).ShouldNot(gomega.BeNil())
							gomega.Expect(wl.Status.Admission.PodSetAssignments[0].TopologyAssignment.Levels).Should(gomega.Equal([]string{corev1.LabelHostname}))
							gomega.Expect(slices.Collect(utiltas.PodCounts(wl.Status.Admission.PodSetAssignments[0].TopologyAssignment))).To(gomega.Equal([]int32{1}))

							// Second pod set (1 GPU) should be assigned to a single node
							gomega.Expect(wl.Status.Admission.PodSetAssignments[1].TopologyAssignment).ShouldNot(gomega.BeNil())
							gomega.Expect(wl.Status.Admission.PodSetAssignments[1].TopologyAssignment.Levels).Should(gomega.Equal([]string{corev1.LabelHostname}))
							gomega.Expect(slices.Collect(utiltas.PodCounts(wl.Status.Admission.PodSetAssignments[1].TopologyAssignment))).To(gomega.Equal([]int32{1}))
						})

						ginkgo.By("Demonstrating TAS prevents resource waste by allowing schedulable workloads", func() {
							// This test demonstrates the benefit of TAS:
							// 1. TAS correctly identifies that both pods can be scheduled
							// 2. The 8-GPU pod goes to the node with 8 available GPUs
							// 3. The 1-GPU pod goes to one of the nodes with 1 available GPU
							// 4. This prevents resource waste by admitting workloads that can actually be scheduled
							gomega.Expect(workload.HasQuotaReservation(wl)).Should(gomega.BeTrue())
						})
					})
				})
			})
		})
	})
})
