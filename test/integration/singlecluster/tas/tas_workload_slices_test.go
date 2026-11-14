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

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/features"
	utiltas "sigs.k8s.io/kueue/pkg/util/tas"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingnode "sigs.k8s.io/kueue/pkg/util/testingjobs/node"
	testingpod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/pkg/workloadslicing"
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
		ginkgo.When("TAS with ElasticJobsViaWorkloadSlices", func() {
			var (
				tasFlavor    *kueue.ResourceFlavor
				topology     *kueue.Topology
				localQueue   *kueue.LocalQueue
				clusterQueue *kueue.ClusterQueue
				nodes        []corev1.Node
			)

			ginkgo.BeforeEach(func() {
				features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.ElasticJobsViaWorkloadSlices, true)
				features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.ElasticJobsViaWorkloadSlicesWithTAS, true)

				// Create two blocks with two racks each to test topology placement
				nodes = []corev1.Node{
					*testingnode.MakeNode("b1-r1").
						Label("node-group", "tas").
						Label(utiltesting.DefaultBlockTopologyLevel, "b1").
						Label(utiltesting.DefaultRackTopologyLevel, "r1").
						StatusAllocatable(corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("8"),
							corev1.ResourceMemory: resource.MustParse("8Gi"),
							corev1.ResourcePods:   resource.MustParse("20"),
						}).
						Ready().
						Obj(),
					*testingnode.MakeNode("b1-r2").
						Label("node-group", "tas").
						Label(utiltesting.DefaultBlockTopologyLevel, "b1").
						Label(utiltesting.DefaultRackTopologyLevel, "r2").
						StatusAllocatable(corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("8"),
							corev1.ResourceMemory: resource.MustParse("8Gi"),
							corev1.ResourcePods:   resource.MustParse("20"),
						}).
						Ready().
						Obj(),
					*testingnode.MakeNode("b2-r1").
						Label("node-group", "tas").
						Label(utiltesting.DefaultBlockTopologyLevel, "b2").
						Label(utiltesting.DefaultRackTopologyLevel, "r1").
						StatusAllocatable(corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("8"),
							corev1.ResourceMemory: resource.MustParse("8Gi"),
							corev1.ResourcePods:   resource.MustParse("20"),
						}).
						Ready().
						Obj(),
				}
				integration.CreateNodesWithStatus(ctx, k8sClient, nodes)

				topology = utiltestingapi.MakeDefaultTwoLevelTopology("default")
				behavioral.MustCreate(ctx, k8sClient, topology)

				tasFlavor = utiltestingapi.MakeResourceFlavor("tas-flavor").
					NodeLabel("node-group", "tas").
					TopologyName("default").Obj()
				behavioral.MustCreate(ctx, k8sClient, tasFlavor)

				clusterQueue = utiltestingapi.MakeClusterQueue("cluster-queue").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas(tasFlavor.Name).Resource(corev1.ResourceCPU, "24").Obj()).
					Obj()
				behavioral.MustCreate(ctx, k8sClient, clusterQueue)
				behavioral.ExpectClusterQueuesToBeActive(ctx, k8sClient, clusterQueue)

				localQueue = utiltestingapi.MakeLocalQueue("local-queue", ns.Name).ClusterQueue(clusterQueue.Name).Obj()
				behavioral.MustCreate(ctx, k8sClient, localQueue)
			})

			ginkgo.AfterEach(func() {
				gomega.Expect(behavioral.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
				behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
				behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, tasFlavor, true)
				behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, topology, true)
				for _, node := range nodes {
					behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, &node, true)
				}
			})

			ginkgo.It("should ungate late and recreated pods referencing a replaced slice", func() {
				ginkgo.By("admitting the original slice")
				original := utiltestingapi.MakeWorkload("original", ns.Name).
					Queue(kueue.LocalQueueName(localQueue.Name)).
					Annotation(constants.ElasticJobAnnotation, "true").
					PodSets(*utiltestingapi.MakePodSet("workers", 1).
						Request(corev1.ResourceCPU, "1").UnconstrainedTopologyRequest().Obj()).Obj()
				behavioral.MustCreate(ctx, k8sClient, original)
				behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, original)

				newPod := func(name string) *corev1.Pod {
					return testingpod.MakePod(name, ns.Name).
						Annotation(kueue.WorkloadAnnotation, original.Name).
						Annotation(kueue.WorkloadSliceNameAnnotation, original.Name).
						Annotation(kueue.PodSetUnconstrainedTopologyAnnotation, "true").
						Label(constants.PodSetLabel, "workers").
						Request(corev1.ResourceCPU, "1").TopologySchedulingGate().Obj()
				}
				expectUngated := func(pod *corev1.Pod) {
					ginkgo.GinkgoHelper()
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(pod), pod)).To(gomega.Succeed())
						g.Expect(pod.Spec.SchedulingGates).To(gomega.BeEmpty())
						g.Expect(pod.Spec.NodeSelector).NotTo(gomega.BeEmpty())
					}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
				}

				ginkgo.By("ungating the first worker using the original admission")
				first := newPod("first")
				behavioral.MustCreate(ctx, k8sClient, first)
				expectUngated(first)

				ginkgo.By("admitting the replacement slice and finishing the original")
				replacement := utiltestingapi.MakeWorkload("replacement", ns.Name).
					Queue(kueue.LocalQueueName(localQueue.Name)).
					Annotation(constants.ElasticJobAnnotation, "true").
					Annotation(workloadslicing.WorkloadSliceReplacementFor, string(workload.Key(original))).
					Annotation(kueue.WorkloadSliceNameAnnotation, original.Name).
					PodSets(*utiltestingapi.MakePodSet("workers", 2).
						Request(corev1.ResourceCPU, "1").UnconstrainedTopologyRequest().Obj()).Obj()
				behavioral.MustCreate(ctx, k8sClient, replacement)
				behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, replacement)
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(original), original)).To(gomega.Succeed())
					g.Expect(apimeta.IsStatusConditionTrue(original.Status.Conditions, kueue.WorkloadFinished)).To(gomega.BeTrue())
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

				ginkgo.By("creating a worker after the replacement has been admitted")
				second := newPod("second")
				behavioral.MustCreate(ctx, k8sClient, second)
				expectUngated(second)

				ginkgo.By("recreating the worker without another Workload admission event")
				gomega.Expect(k8sClient.Delete(ctx, second, client.GracePeriodSeconds(0))).To(gomega.Succeed())
				behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, second, false)
				recreated := newPod("recreated")
				behavioral.MustCreate(ctx, k8sClient, recreated)
				expectUngated(recreated)
			})

			ginkgo.It("does not double-count a bound Pod covered by a replacement slice", func() {
				selector := map[string]string{
					utiltesting.DefaultBlockTopologyLevel: "b1",
					utiltesting.DefaultRackTopologyLevel:  "r1",
				}
				makePodSet := func(count int) kueue.PodSet {
					return *utiltestingapi.MakePodSet("workers", count).
						Request(corev1.ResourceCPU, "1").NodeSelector(selector).UnconstrainedTopologyRequest().Obj()
				}
				original := utiltestingapi.MakeWorkload("original", ns.Name).
					Queue(kueue.LocalQueueName(localQueue.Name)).
					Annotation(constants.ElasticJobAnnotation, "true").
					PodSets(makePodSet(1)).Obj()
				behavioral.MustCreate(ctx, k8sClient, original)
				behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, original)
				boundPod := testingpod.MakePod("old-bound-worker", ns.Name).
					NodeName("b1-r1").StatusPhase(corev1.PodRunning).
					TerminationGracePeriod(0).
					Annotation(kueue.WorkloadAnnotation, original.Name).
					Annotation(kueue.WorkloadSliceNameAnnotation, original.Name).
					Annotation(kueue.PodSetUnconstrainedTopologyAnnotation, "true").
					Request(corev1.ResourceCPU, "1").Obj()
				behavioral.MustCreate(ctx, k8sClient, boundPod)
				integration.SetPodsPhase(ctx, k8sClient, corev1.PodRunning, boundPod)
				ginkgo.DeferCleanup(func() {
					_ = client.IgnoreNotFound(k8sClient.Delete(ctx, boundPod, client.GracePeriodSeconds(0)))
				})

				replacement := utiltestingapi.MakeWorkload("replacement", ns.Name).
					Queue(kueue.LocalQueueName(localQueue.Name)).
					Annotation(constants.ElasticJobAnnotation, "true").
					Annotation(workloadslicing.WorkloadSliceReplacementFor, string(workload.Key(original))).
					Annotation(kueue.WorkloadSliceNameAnnotation, original.Name).
					PodSets(makePodSet(2)).Obj()
				behavioral.MustCreate(ctx, k8sClient, replacement)
				behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, replacement)
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(original), original)).To(gomega.Succeed())
					g.Expect(apimeta.IsStatusConditionTrue(original.Status.Conditions, kueue.WorkloadFinished)).To(gomega.BeTrue())
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(boundPod), boundPod)).To(gomega.Succeed())
				gomega.Expect(boundPod.Spec.NodeName).To(gomega.Equal("b1-r1"))

				other := utiltestingapi.MakeWorkload("other", ns.Name).
					Queue(kueue.LocalQueueName(localQueue.Name)).
					PodSets(*utiltestingapi.MakePodSet("workers", 1).
						Request(corev1.ResourceCPU, "6").NodeSelector(selector).UnconstrainedTopologyRequest().Obj()).Obj()
				behavioral.MustCreate(ctx, k8sClient, other)
				behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, other)
			})

			ginkgo.It("defers elastic growth until residual Pods release capacity without double-counting the predecessor", func() {
				selector := map[string]string{
					utiltesting.DefaultBlockTopologyLevel: "b1",
					utiltesting.DefaultRackTopologyLevel:  "r1",
				}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(clusterQueue), clusterQueue)).To(gomega.Succeed())
					clusterQueue.Spec.Preemption = &kueue.ClusterQueuePreemption{WithinClusterQueue: kueue.PreemptionPolicyLowerPriority}
					g.Expect(k8sClient.Update(ctx, clusterQueue)).To(gomega.Succeed())
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
				residual := testingpod.MakePod("released-bound-pod", ns.Name).NodeName("b1-r1").
					TerminationGracePeriod(0).Request(corev1.ResourceCPU, "2").
					Annotation(kueue.WorkloadAnnotation, "released").
					Annotation(kueue.PodSetUnconstrainedTopologyAnnotation, "true").Obj()
				residual.Finalizers = []string{"test.kueue.x-k8s.io/hold"}
				behavioral.MustCreate(ctx, k8sClient, residual)
				integration.SetPodsPhase(ctx, k8sClient, corev1.PodRunning, residual)
				ginkgo.DeferCleanup(func() {
					if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(residual), residual); err == nil {
						residual.Finalizers = nil
						gomega.Expect(k8sClient.Update(ctx, residual)).To(gomega.Succeed())
						_ = client.IgnoreNotFound(k8sClient.Delete(ctx, residual, client.GracePeriodSeconds(0)))
					}
				})
				makePodSet := func(count int) kueue.PodSet {
					return *utiltestingapi.MakePodSet("workers", count).
						Request(corev1.ResourceCPU, "2").NodeSelector(selector).UnconstrainedTopologyRequest().Obj()
				}
				original := utiltestingapi.MakeWorkload("original", ns.Name).Priority(2).
					Queue(kueue.LocalQueueName(localQueue.Name)).Annotation(constants.ElasticJobAnnotation, "true").
					PodSets(makePodSet(2)).Obj()
				other := utiltestingapi.MakeWorkload("other", ns.Name).Priority(1).
					Queue(kueue.LocalQueueName(localQueue.Name)).PodSets(makePodSet(1)).Obj()
				behavioral.MustCreate(ctx, k8sClient, original)
				behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, original)
				behavioral.MustCreate(ctx, k8sClient, other)
				behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, other)
				gomega.Expect(k8sClient.Delete(ctx, residual, client.GracePeriodSeconds(0))).To(gomega.Succeed())
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(residual), residual)).To(gomega.Succeed())
					g.Expect(residual.DeletionTimestamp).NotTo(gomega.BeNil())
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
				replacement := utiltestingapi.MakeWorkload("replacement", ns.Name).Priority(3).
					Queue(kueue.LocalQueueName(localQueue.Name)).Annotation(constants.ElasticJobAnnotation, "true").
					Annotation(workloadslicing.WorkloadSliceReplacementFor, string(workload.Key(original))).
					Annotation(kueue.WorkloadSliceNameAnnotation, original.Name).PodSets(makePodSet(3)).Obj()
				behavioral.MustCreate(ctx, k8sClient, replacement)
				assertWaiting := func(g gomega.Gomega) {
					for _, wl := range []*kueue.Workload{original, other} {
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), wl)).To(gomega.Succeed())
						g.Expect(workload.IsAdmitted(wl)).To(gomega.BeTrue())
						g.Expect(apimeta.IsStatusConditionTrue(wl.Status.Conditions, kueue.WorkloadEvicted)).To(gomega.BeFalse())
					}
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(replacement), replacement)).To(gomega.Succeed())
					g.Expect(workload.IsAdmitted(replacement)).To(gomega.BeFalse())
					condition := apimeta.FindStatusCondition(replacement.Status.Conditions, kueue.WorkloadQuotaReserved)
					g.Expect(condition).NotTo(gomega.BeNil())
					g.Expect(condition.Message).To(gomega.ContainSubstring("bound Pods from released TAS reservations"))
				}
				gomega.Eventually(assertWaiting, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
				gomega.Consistently(assertWaiting, behavioral.LongConsistentDuration, behavioral.ShortInterval).Should(gomega.Succeed())
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(residual), residual)).To(gomega.Succeed())
					residual.Finalizers = nil
					g.Expect(k8sClient.Update(ctx, residual)).To(gomega.Succeed())
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
				behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, residual, false)
				behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, other, replacement)
			})

			ginkgo.It("should grow an elastic workload to exactly fill topology capacity", func() {
				var wl1 *kueue.Workload

				ginkgo.By("create initial workload with 2 pods using unconstrained topology", func() {
					wl1 = utiltestingapi.MakeWorkload("wl1", ns.Name).
						Queue(kueue.LocalQueueName(localQueue.Name)).
						Annotation(constants.ElasticJobAnnotation, "true").
						Obj()
					wl1.Spec.PodSets[0] = *utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 2).
						Request(corev1.ResourceCPU, "1").
						UnconstrainedTopologyRequest().
						Image("image").
						Obj()
					behavioral.MustCreate(ctx, k8sClient, wl1)
				})

				ginkgo.By("verify the workload is admitted", func() {
					behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1)
				})

				var originalAssignment *kueue.TopologyAssignment
				ginkgo.By("verify admission for the workload1 and record the assignment", func() {
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl1), wl1)).To(gomega.Succeed())
						g.Expect(wl1.Status.Admission).ShouldNot(gomega.BeNil())
						g.Expect(wl1.Status.Admission.PodSetAssignments).Should(gomega.HaveLen(1))
						g.Expect(wl1.Status.Admission.PodSetAssignments[0].TopologyAssignment).ShouldNot(gomega.BeNil())
					}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
					originalAssignment = wl1.Status.Admission.PodSetAssignments[0].TopologyAssignment
				})

				ginkgo.By("verify the original assignment is in block b1 or b2", func() {
					// Extract the block from the original assignment
					gomega.Expect(originalAssignment).ShouldNot(gomega.BeNil())
					gomega.Expect(originalAssignment.Slices).ShouldNot(gomega.BeEmpty())
				})

				ginkgo.By("create pods simulating running pods", func() {
					// Create pods with the workload slice name annotation - this is set by the reconciler
					for i := range 2 {
						pod := testingpod.MakePod(fmt.Sprintf("pod-%d", i), ns.Name).
							Annotation(kueue.WorkloadAnnotation, wl1.Name).
							Annotation(kueue.WorkloadSliceNameAnnotation, wl1.Name).
							Request(corev1.ResourceCPU, "1").
							Obj()
						behavioral.MustCreate(ctx, k8sClient, pod)
					}
				})

				var wl2 *kueue.Workload
				// The three nodes have 24 CPUs total. Counting the old two-Pod slice
				// twice would incorrectly reject a replacement that exactly fits.
				ginkgo.By("create a replacement workload slice requesting all 24 CPUs", func() {
					wl2 = utiltestingapi.MakeWorkload("wl2", ns.Name).
						Queue(kueue.LocalQueueName(localQueue.Name)).
						Annotation(constants.ElasticJobAnnotation, "true").
						Annotation(workloadslicing.WorkloadSliceReplacementFor, string(workload.Key(wl1))).
						Annotation(kueue.WorkloadSliceNameAnnotation, wl1.Name).
						Obj()
					wl2.Spec.PodSets[0] = *utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 24).
						Request(corev1.ResourceCPU, "1").
						UnconstrainedTopologyRequest().
						Image("image").
						Obj()
					behavioral.MustCreate(ctx, k8sClient, wl2)
				})

				ginkgo.By("verify the replacement workload is admitted", func() {
					behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl2)
				})

				ginkgo.By("verify the replacement workload has topology assignment", func() {
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl2), wl2)).To(gomega.Succeed())
						g.Expect(wl2.Status.Admission).ShouldNot(gomega.BeNil())
						g.Expect(wl2.Status.Admission.PodSetAssignments).Should(gomega.HaveLen(1))
						g.Expect(wl2.Status.Admission.PodSetAssignments[0].TopologyAssignment).ShouldNot(gomega.BeNil())

						newAssignment := wl2.Status.Admission.PodSetAssignments[0].TopologyAssignment
						g.Expect(newAssignment.Slices).ShouldNot(gomega.BeEmpty())
						g.Expect(originalAssignment.Slices).ShouldNot(gomega.BeEmpty())
						g.Expect(assignedPodCount(newAssignment)).To(gomega.Equal(int32(24)))
					}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
				})
			})

			ginkgo.It("should preserve the leader assignment when scaling up workers in a pod set group", func() {
				var wl1 *kueue.Workload

				// The group requests exactly one node's capacity (2 + 2*3 = 8 CPU), so the
				// initial placement packs everything onto a single, fully used node. During
				// scale-up the delta workers cannot fit there, so a (buggy) recomputation of
				// the leader placement would necessarily move the leader to a different node.
				ginkgo.By("create initial elastic workload with a leader and 2 workers", func() {
					wl1 = utiltestingapi.MakeWorkload("wl1", ns.Name).
						Queue(kueue.LocalQueueName(localQueue.Name)).
						Annotation(constants.ElasticJobAnnotation, "true").
						Obj()
					wl1.Spec.PodSets = []kueue.PodSet{
						*utiltestingapi.MakePodSet("leader", 1).
							Request(corev1.ResourceCPU, "2").
							UnconstrainedTopologyRequest().
							PodSetGroup("elastic-group").
							Image("image").
							Obj(),
						*utiltestingapi.MakePodSet("workers", 2).
							Request(corev1.ResourceCPU, "3").
							UnconstrainedTopologyRequest().
							PodSetGroup("elastic-group").
							Image("image").
							Obj(),
					}
					behavioral.MustCreate(ctx, k8sClient, wl1)
				})

				ginkgo.By("verify the workload is admitted", func() {
					behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1)
				})

				var originalLeaderAssignment, originalWorkersAssignment *utiltas.TopologyAssignment
				ginkgo.By("record the leader and workers assignments", func() {
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl1), wl1)).To(gomega.Succeed())
						g.Expect(wl1.Status.Admission).ShouldNot(gomega.BeNil())
						g.Expect(wl1.Status.Admission.PodSetAssignments).Should(gomega.HaveLen(2))
						originalLeaderAssignment = topologyAssignmentByName(g, wl1, "leader")
						originalWorkersAssignment = topologyAssignmentByName(g, wl1, "workers")
					}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
				})

				ginkgo.By("verify the group is packed onto a single node", func() {
					// Precondition for the scenario: the leader's node is fully used, so a
					// recomputed leader placement could never land on it again.
					gomega.Expect(originalLeaderAssignment.Domains).Should(gomega.HaveLen(1))
					gomega.Expect(originalWorkersAssignment.Domains).Should(gomega.HaveLen(1))
					gomega.Expect(originalLeaderAssignment.Domains[0].Values).Should(gomega.Equal(originalWorkersAssignment.Domains[0].Values))
				})

				ginkgo.By("create pods simulating running pods", func() {
					requests := []string{"2", "3", "3"}
					for i := range 3 {
						pod := testingpod.MakePod(fmt.Sprintf("pod-%d", i), ns.Name).
							Annotation(kueue.WorkloadAnnotation, wl1.Name).
							Annotation(kueue.WorkloadSliceNameAnnotation, wl1.Name).
							Request(corev1.ResourceCPU, requests[i]).
							Obj()
						behavioral.MustCreate(ctx, k8sClient, pod)
					}
				})

				var wl2 *kueue.Workload
				ginkgo.By("create a replacement workload slice with more workers", func() {
					wl2 = utiltestingapi.MakeWorkload("wl2", ns.Name).
						Queue(kueue.LocalQueueName(localQueue.Name)).
						Annotation(constants.ElasticJobAnnotation, "true").
						Annotation(workloadslicing.WorkloadSliceReplacementFor, string(workload.Key(wl1))).
						Annotation(kueue.WorkloadSliceNameAnnotation, wl1.Name).
						Obj()
					wl2.Spec.PodSets = []kueue.PodSet{
						*utiltestingapi.MakePodSet("leader", 1).
							Request(corev1.ResourceCPU, "2").
							UnconstrainedTopologyRequest().
							PodSetGroup("elastic-group").
							Image("image").
							Obj(),
						*utiltestingapi.MakePodSet("workers", 4).
							Request(corev1.ResourceCPU, "3").
							UnconstrainedTopologyRequest().
							PodSetGroup("elastic-group").
							Image("image").
							Obj(),
					}
					behavioral.MustCreate(ctx, k8sClient, wl2)
				})

				ginkgo.By("verify the replacement workload is admitted", func() {
					behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl2)
				})

				ginkgo.By("verify the leader keeps its assignment and workers keep their previous placement", func() {
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl2), wl2)).To(gomega.Succeed())
						g.Expect(wl2.Status.Admission).ShouldNot(gomega.BeNil())
						g.Expect(wl2.Status.Admission.PodSetAssignments).Should(gomega.HaveLen(2))
						g.Expect(topologyAssignmentByName(g, wl2, "leader")).Should(gomega.BeComparableTo(originalLeaderAssignment))
						newWorkersAssignment := topologyAssignmentByName(g, wl2, "workers")
						g.Expect(utiltas.CountPodsInAssignment(newWorkersAssignment)).Should(gomega.Equal(int32(4)))
						newCounts := domainPodCounts(newWorkersAssignment)
						for _, domain := range originalWorkersAssignment.Domains {
							g.Expect(newCounts[utiltas.DomainID(domain.Values)]).Should(
								gomega.BeNumerically(">=", domain.Count),
								"previous workers domain %v must be preserved", domain.Values)
						}
					}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
				})
			})

			ginkgo.It("should keep the pods left after a scale-down gated while the pods at the lowest ranks hold the domains", func() {
				// A replacement slice admitted with a count below the previous
				// assignment's size truncates that assignment to the lowest ranks, while
				// the Pods above the new count still exist until the Job controller
				// deletes them.
				var wl1, wl2 *kueue.Workload
				listPods := func(g gomega.Gomega) []corev1.Pod {
					var pods corev1.PodList
					g.Expect(k8sClient.List(ctx, &pods, client.InNamespace(ns.Name),
						client.MatchingLabels{constants.PodSetLabel: string(kueue.DefaultPodSetName)})).To(gomega.Succeed())
					return pods.Items
				}

				ginkgo.By("creating a workload with 4 pods using unconstrained topology", func() {
					wl1 = utiltestingapi.MakeWorkload("wl1", ns.Name).
						Queue(kueue.LocalQueueName(localQueue.Name)).
						Annotation(constants.ElasticJobAnnotation, "true").
						Obj()
					wl1.Spec.PodSets[0] = *utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 4).
						Request(corev1.ResourceCPU, "1").
						UnconstrainedTopologyRequest().
						PodIndexLabel(new(batchv1.JobCompletionIndexAnnotation)).
						Image("image").
						Obj()
					behavioral.MustCreate(ctx, k8sClient, wl1)
					behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1)
				})

				ginkgo.By("creating a replacement slice larger than the available capacity", func() {
					wl2 = utiltestingapi.MakeWorkload("wl2", ns.Name).
						Queue(kueue.LocalQueueName(localQueue.Name)).
						Annotation(constants.ElasticJobAnnotation, "true").
						Annotation(workloadslicing.WorkloadSliceReplacementFor, string(workload.Key(wl1))).
						Annotation(kueue.WorkloadSliceNameAnnotation, wl1.Name).
						Obj()
					wl2.Spec.PodSets[0] = *utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 25).
						Request(corev1.ResourceCPU, "1").
						UnconstrainedTopologyRequest().
						PodIndexLabel(new(batchv1.JobCompletionIndexAnnotation)).
						Image("image").
						Obj()
					behavioral.MustCreate(ctx, k8sClient, wl2)
					behavioral.ExpectWorkloadsToBePending(ctx, k8sClient, wl2)
				})

				ginkgo.By("scaling the pending replacement slice down to 2 pods", func() {
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl2), wl2)).To(gomega.Succeed())
						wl2.Spec.PodSets[0].Count = 2
						g.Expect(k8sClient.Update(ctx, wl2)).To(gomega.Succeed())
					}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
				})

				ginkgo.By("verifying the replacement slice is admitted with an assignment truncated to 2 pods", func() {
					behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl2)
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl2), wl2)).To(gomega.Succeed())
						g.Expect(utiltas.CountPodsInAssignment(topologyAssignmentByName(g, wl2, kueue.DefaultPodSetName))).To(gomega.Equal(int32(2)))
					}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
				})

				gatedPod := testingpod.MakePod("", ns.Name).
					Annotation(kueue.WorkloadAnnotation, wl2.Name).
					Annotation(kueue.WorkloadSliceNameAnnotation, wl1.Name).
					Annotation(kueue.PodSetUnconstrainedTopologyAnnotation, "true").
					Label(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
					Request(corev1.ResourceCPU, "1").
					TopologySchedulingGate()
				ginkgo.By("creating the 2 gated pods at completion index 0 and 1", func() {
					behavioral.MustCreate(ctx, k8sClient, gatedPod.Clone().Name("pod-c").Label(batchv1.JobCompletionIndexAnnotation, "0").Obj())
					behavioral.MustCreate(ctx, k8sClient, gatedPod.Clone().Name("pod-d").Label(batchv1.JobCompletionIndexAnnotation, "1").Obj())
				})

				nodeSelectors := make(map[string]map[string]string)
				ginkgo.By("verifying the pods at completion index 0 and 1 are ungated", func() {
					gomega.Eventually(func(g gomega.Gomega) {
						pods := listPods(g)
						g.Expect(pods).Should(gomega.HaveLen(2))
						expectUngatedExactlyAtIndexes(g, pods, "0", "1")
						for _, pod := range pods {
							nodeSelectors[pod.Name] = pod.Spec.NodeSelector
						}
					}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
				})

				ginkgo.By("creating the 2 gated pods awaiting deletion", func() {
					behavioral.MustCreate(ctx, k8sClient, gatedPod.Clone().Name("pod-a").Label(batchv1.JobCompletionIndexAnnotation, "2").Obj())
					behavioral.MustCreate(ctx, k8sClient, gatedPod.Clone().Name("pod-b").Label(batchv1.JobCompletionIndexAnnotation, "3").Obj())
				})

				ginkgo.By("verifying the pods awaiting deletion stay gated and the pods at completion index 0 and 1 keep their domains", func() {
					gomega.Consistently(func(g gomega.Gomega) {
						pods := listPods(g)
						g.Expect(pods).Should(gomega.HaveLen(4))
						expectUngatedExactlyAtIndexes(g, pods, "0", "1")
						for _, pod := range pods {
							if selector, found := nodeSelectors[pod.Name]; found {
								g.Expect(pod.Spec.NodeSelector).To(gomega.Equal(selector))
							}
						}
					}, behavioral.LongConsistentDuration, behavioral.ShortInterval).Should(gomega.Succeed())
				})
			})

			ginkgo.It("should ungate the pod at completion index 2 into the domain freed by the succeeded pod at index 0", func() {
				// An Indexed Job with completions above parallelism creates the pod at
				// completion index 2 once the pod at index 0 succeeds.
				// Each pod fills a node, so the two pods are placed in different domains.
				var wl *kueue.Workload
				expectUngated := func(pod *corev1.Pod) {
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(pod), pod)).To(gomega.Succeed())
						g.Expect(pod.Spec.SchedulingGates).To(gomega.BeEmpty())
						g.Expect(pod.Spec.NodeSelector).NotTo(gomega.BeEmpty())
					}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
				}

				ginkgo.By("creating and admitting an elastic workload ranked by the completion index", func() {
					wl = utiltestingapi.MakeWorkload("wl-rotation", ns.Name).
						Queue(kueue.LocalQueueName(localQueue.Name)).
						Annotation(constants.ElasticJobAnnotation, "true").
						PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 2).
							Request(corev1.ResourceCPU, "5").
							UnconstrainedTopologyRequest().
							PodIndexLabel(new(batchv1.JobCompletionIndexAnnotation)).
							Obj()).
						Obj()
					behavioral.MustCreate(ctx, k8sClient, wl)
					behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl)
				})

				gatedPod := testingpod.MakePod("", ns.Name).
					Annotation(kueue.WorkloadAnnotation, wl.Name).
					Annotation(kueue.PodSetUnconstrainedTopologyAnnotation, "true").
					Label(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
					Request(corev1.ResourceCPU, "5").
					TopologySchedulingGate()
				var pod0 *corev1.Pod
				ginkgo.By("ungating the pods at completion index 0 and 1", func() {
					pod0 = gatedPod.Clone().Name("worker-0").Label(batchv1.JobCompletionIndexAnnotation, "0").Obj()
					behavioral.MustCreate(ctx, k8sClient, pod0)
					pod1 := gatedPod.Clone().Name("worker-1").Label(batchv1.JobCompletionIndexAnnotation, "1").Obj()
					behavioral.MustCreate(ctx, k8sClient, pod1)
					expectUngated(pod0)
					expectUngated(pod1)
					gomega.Expect(pod1.Spec.NodeSelector).NotTo(gomega.Equal(pod0.Spec.NodeSelector))
				})

				ginkgo.By("marking the pod at completion index 0 as succeeded", func() {
					integration.SetPodsPhase(ctx, k8sClient, corev1.PodSucceeded, pod0)
				})

				ginkgo.By("verifying the pod at completion index 2 is ungated into the freed domain", func() {
					pod2 := gatedPod.Clone().Name("worker-2").Label(batchv1.JobCompletionIndexAnnotation, "2").Obj()
					behavioral.MustCreate(ctx, k8sClient, pod2)
					expectUngated(pod2)
					gomega.Expect(pod2.Spec.NodeSelector).To(gomega.Equal(pod0.Spec.NodeSelector))
				})
			})

			ginkgo.It("should isolate workloads with same name in different namespaces", func() {
				ns2 := &corev1.Namespace{GenerateName: "tas-elastic-ns2-"}
				gomega.Expect(k8sClient.Create(ctx, ns2)).To(gomega.Succeed())
				defer func() {
					gomega.Expect(forceDeleteNamespace(ctx, k8sClient, ns2)).To(gomega.Succeed())
				}()

				localQueue2 := utiltestingapi.MakeLocalQueue("local-queue", ns2.Name).ClusterQueue(clusterQueue.Name).Obj()
				behavioral.MustCreate(ctx, k8sClient, localQueue2)

				var wl1, wl2 *kueue.Workload
				ginkgo.By("create workloads in both namespaces with same name", func() {
					wl1 = utiltestingapi.MakeWorkload("wl1", ns.Name).
						Queue(kueue.LocalQueueName(localQueue.Name)).
						Obj()
					wl1.Spec.PodSets[0] = *utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 2).
						Request(corev1.ResourceCPU, "1").
						UnconstrainedTopologyRequest().
						Image("image").
						Obj()
					behavioral.MustCreate(ctx, k8sClient, wl1)

					wl2 = utiltestingapi.MakeWorkload("wl1", ns2.Name).
						Queue(kueue.LocalQueueName(localQueue2.Name)).
						Obj()
					wl2.Spec.PodSets[0] = *utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 2).
						Request(corev1.ResourceCPU, "1").
						UnconstrainedTopologyRequest().
						Image("image").
						Obj()
					behavioral.MustCreate(ctx, k8sClient, wl2)
				})

				ginkgo.By("verify both workloads are admitted", func() {
					behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1)
					behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl2)
				})

				var originalAssignment1, originalAssignment2 *kueue.TopologyAssignment
				ginkgo.By("record the topology assignments", func() {
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl1), wl1)).To(gomega.Succeed())
						g.Expect(wl1.Status.Admission).ShouldNot(gomega.BeNil())
						g.Expect(wl1.Status.Admission.PodSetAssignments).Should(gomega.HaveLen(1))
						originalAssignment1 = wl1.Status.Admission.PodSetAssignments[0].TopologyAssignment
						g.Expect(originalAssignment1).ShouldNot(gomega.BeNil())
					}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl2), wl2)).To(gomega.Succeed())
						g.Expect(wl2.Status.Admission).ShouldNot(gomega.BeNil())
						g.Expect(wl2.Status.Admission.PodSetAssignments).Should(gomega.HaveLen(1))
						originalAssignment2 = wl2.Status.Admission.PodSetAssignments[0].TopologyAssignment
						g.Expect(originalAssignment2).ShouldNot(gomega.BeNil())
					}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
				})

				ginkgo.By("create pods for both workloads", func() {
					// Pods for workload in namespace 1
					for i := range 2 {
						pod := testingpod.MakePod(fmt.Sprintf("pod-ns1-%d", i), ns.Name).
							Annotation(kueue.WorkloadAnnotation, wl1.Name).
							Annotation(kueue.WorkloadSliceNameAnnotation, wl1.Name).
							Request(corev1.ResourceCPU, "1").
							Obj()
						behavioral.MustCreate(ctx, k8sClient, pod)
					}
					// Pods for workload in namespace 2
					for i := range 2 {
						pod := testingpod.MakePod(fmt.Sprintf("pod-ns2-%d", i), ns2.Name).
							Annotation(kueue.WorkloadAnnotation, wl2.Name).
							Annotation(kueue.WorkloadSliceNameAnnotation, wl2.Name).
							Request(corev1.ResourceCPU, "1").
							Obj()
						behavioral.MustCreate(ctx, k8sClient, pod)
					}
				})

				var wl1Replacement, wl2Replacement *kueue.Workload
				ginkgo.By("create replacement workloads", func() {
					wl1Replacement = utiltestingapi.MakeWorkload("wl1-replacement", ns.Name).
						Queue(kueue.LocalQueueName(localQueue.Name)).
						Annotation(workloadslicing.WorkloadSliceReplacementFor, string(workload.Key(wl1))).
						Obj()
					wl1Replacement.Spec.PodSets[0] = *utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 3).
						Request(corev1.ResourceCPU, "1").
						UnconstrainedTopologyRequest().
						Image("image").
						Obj()
					behavioral.MustCreate(ctx, k8sClient, wl1Replacement)

					wl2Replacement = utiltestingapi.MakeWorkload("wl1-replacement", ns2.Name).
						Queue(kueue.LocalQueueName(localQueue2.Name)).
						Annotation(workloadslicing.WorkloadSliceReplacementFor, string(workload.Key(wl2))).
						Obj()
					wl2Replacement.Spec.PodSets[0] = *utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 3).
						Request(corev1.ResourceCPU, "1").
						UnconstrainedTopologyRequest().
						Image("image").
						Obj()
					behavioral.MustCreate(ctx, k8sClient, wl2Replacement)
				})

				ginkgo.By("verify both replacement workloads are admitted", func() {
					behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1Replacement)
					behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl2Replacement)
				})

				ginkgo.By("verify each replacement workload has topology assignment", func() {
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl1Replacement), wl1Replacement)).To(gomega.Succeed())
						g.Expect(wl1Replacement.Status.Admission).ShouldNot(gomega.BeNil())
						newAssignment1 := wl1Replacement.Status.Admission.PodSetAssignments[0].TopologyAssignment
						g.Expect(newAssignment1).ShouldNot(gomega.BeNil())
						g.Expect(newAssignment1.Slices).ShouldNot(gomega.BeEmpty())
					}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl2Replacement), wl2Replacement)).To(gomega.Succeed())
						g.Expect(wl2Replacement.Status.Admission).ShouldNot(gomega.BeNil())
						newAssignment2 := wl2Replacement.Status.Admission.PodSetAssignments[0].TopologyAssignment
						g.Expect(newAssignment2).ShouldNot(gomega.BeNil())
						g.Expect(newAssignment2.Slices).ShouldNot(gomega.BeEmpty())
					}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
				})
			})
		})
	})
})
