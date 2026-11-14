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
	"time"

	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	apitypes "k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/tas"
	"sigs.k8s.io/kueue/pkg/features"
	utiltas "sigs.k8s.io/kueue/pkg/util/tas"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingnode "sigs.k8s.io/kueue/pkg/util/testingjobs/node"
	testingpod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
	"sigs.k8s.io/kueue/pkg/workload"
	workloadevict "sigs.k8s.io/kueue/pkg/workload/evict"
	workloadpatching "sigs.k8s.io/kueue/pkg/workload/patching"
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

	ginkgo.When("Single TAS Resource Flavor", func() {
		var (
			tasFlavor    *kueue.ResourceFlavor
			topology     *kueue.Topology
			localQueue   *kueue.LocalQueue
			clusterQueue *kueue.ClusterQueue
		)

		ginkgo.Context("TAS features, preemption and provisioning", func() {
			ginkgo.When("TASNodeFailedReplacementFailFast is enabled", func() {
				var (
					nodes []corev1.Node
				)
				ginkgo.BeforeEach(func() {
					//        b1
					//     /      \
					//    r1       r2
					//   /  \     /  \
					//  x3  x1   x4  x2
					nodes = []corev1.Node{
						*testingnode.MakeNode("x3").
							Label("node-group", "tas").
							Label(utiltesting.DefaultBlockTopologyLevel, "b1").
							Label(utiltesting.DefaultRackTopologyLevel, "r1").
							Label(corev1.LabelHostname, "x3").
							StatusAllocatable(corev1.ResourceList{
								corev1.ResourceCPU:    resource.MustParse("1"),
								corev1.ResourceMemory: resource.MustParse("1Gi"),
								corev1.ResourcePods:   resource.MustParse("10"),
							}).
							Ready().
							Obj(),
						*testingnode.MakeNode("x1").
							Label("node-group", "tas").
							Label(utiltesting.DefaultBlockTopologyLevel, "b1").
							Label(utiltesting.DefaultRackTopologyLevel, "r1").
							Label(corev1.LabelHostname, "x1").
							StatusAllocatable(corev1.ResourceList{
								corev1.ResourceCPU:    resource.MustParse("1"),
								corev1.ResourceMemory: resource.MustParse("1Gi"),
								corev1.ResourcePods:   resource.MustParse("10"),
							}).
							Ready().
							Obj(),
						*testingnode.MakeNode("x4").
							Label("node-group", "tas").
							Label(utiltesting.DefaultBlockTopologyLevel, "b1").
							Label(utiltesting.DefaultRackTopologyLevel, "r2").
							Label(corev1.LabelHostname, "x4").
							StatusAllocatable(corev1.ResourceList{
								corev1.ResourceCPU:    resource.MustParse("1"),
								corev1.ResourceMemory: resource.MustParse("1Gi"),
								corev1.ResourcePods:   resource.MustParse("10"),
							}).
							Ready().
							Obj(),
						*testingnode.MakeNode("x2").
							Label("node-group", "tas").
							Label(utiltesting.DefaultBlockTopologyLevel, "b1").
							Label(utiltesting.DefaultRackTopologyLevel, "r2").
							Label(corev1.LabelHostname, "x2").
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

				ginkgo.It("should evict the Workload if no replacement is possible and place it on another rack", framework.SlowSpec, func() {
					var wl1 *kueue.Workload
					nodeName := nodes[0].Name

					ginkgo.By("creating a workload", func() {
						wl1 = utiltestingapi.MakeWorkload("wl1", ns.Name).
							PodSets(*utiltestingapi.MakePodSet("worker", 2).
								RequiredTopologyRequest(utiltesting.DefaultRackTopologyLevel).
								Obj()).
							Queue(kueue.LocalQueueName(localQueue.Name)).Request(corev1.ResourceCPU, "1").Obj()
						behavioral.MustCreate(ctx, k8sClient, wl1)
					})

					ginkgo.By("verify the workload is admitted", func() {
						behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1)
						gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl1), wl1)).To(gomega.Succeed())
						gomega.Expect(wl1.Status.Admission.PodSetAssignments[0].TopologyAssignment).Should(gomega.BeComparableTo(
							utiltas.V1Beta2From(&utiltas.TopologyAssignment{
								Levels: []string{corev1.LabelHostname},
								Domains: []utiltas.TopologyDomainAssignment{
									{Count: 1, Values: []string{"x1"}},
									{Count: 1, Values: []string{"x3"}},
								},
							}),
						))
					})

					ginkgo.By("making the node NotReady 30s in the past", func() {
						nodeToUpdate := &corev1.Node{}
						gomega.Expect(k8sClient.Get(ctx, apitypes.NamespacedName{Name: nodeName}, nodeToUpdate)).Should(gomega.Succeed())

						behavioral.SetNodeCondition(ctx, k8sClient, nodeToUpdate, &corev1.NodeCondition{
							Type:               corev1.NodeReady,
							Status:             corev1.ConditionFalse,
							LastTransitionTime: metav1.NewTime(time.Now().Add(-tas.NodeFailureDelay)),
						})
					})

					ginkgo.By("verify the workload is evicted due to no replacement possible", func() {
						behavioral.FinishEvictionForWorkloads(ctx, k8sClient, wl1)
						behavioral.ExpectEvictedWorkloadsTotalMetric(clusterQueue.Name, kueue.WorkloadEvictedDueToNodeFailures, "", "", 1)
						gomega.Eventually(func(g gomega.Gomega) {
							updatedWl := &kueue.Workload{}
							g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl1), updatedWl)).To(gomega.Succeed())
							g.Expect(wl1.Status.UnhealthyNodes).To(gomega.BeEmpty())
						}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
					})

					ginkgo.By("verify the workload is placed on another rack", func() {
						behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1)
						gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl1), wl1)).To(gomega.Succeed())
						gomega.Expect(wl1.Status.Admission.PodSetAssignments[0].TopologyAssignment).Should(gomega.BeComparableTo(
							utiltas.V1Beta2From(&utiltas.TopologyAssignment{
								Levels: []string{corev1.LabelHostname},
								Domains: []utiltas.TopologyDomainAssignment{
									{Count: 1, Values: []string{"x2"}},
									{Count: 1, Values: []string{"x4"}},
								},
							}),
						))
					})
				})
				ginkgo.It("should defer the fail-fast eviction of a Workload while an in-flight eviction can free a replacement node", framework.SlowSpec, func() {
					features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.TASFailedNodeReplacementFailFast, true)
					features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.TASNodeReplacementDeferFailFastWhenUsefulInflightEvictions, true)
					makeRackWorkload := func(name string, count int) *kueue.Workload {
						return utiltestingapi.MakeWorkload(name, ns.Name).
							Queue(kueue.LocalQueueName(localQueue.Name)).
							PodSets(*utiltestingapi.MakePodSet("worker", count).
								RequiredTopologyRequest(utiltesting.DefaultRackTopologyLevel).
								Request(corev1.ResourceCPU, "1").
								Obj()).
							Obj()
					}
					admittedNode := func(g gomega.Gomega, wl *kueue.Workload) string {
						ginkgo.GinkgoHelper()
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), wl)).To(gomega.Succeed())
						g.Expect(wl.Status.Admission).NotTo(gomega.BeNil())
						g.Expect(wl.Status.Admission.PodSetAssignments).NotTo(gomega.BeEmpty())
						for domain := range utiltas.InternalSeqFrom(wl.Status.Admission.PodSetAssignments[0].TopologyAssignment) {
							return domain.Values[len(domain.Values)-1]
						}
						return ""
					}

					var foo, victim *kueue.Workload
					ginkgo.By("admitting a workload on a node of the first rack", func() {
						foo = makeRackWorkload("foo", 1)
						behavioral.MustCreate(ctx, k8sClient, foo)
						behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, foo)
					})

					ginkgo.By("admitting a workload that will be evicted on the remaining node of the same rack", func() {
						victim = makeRackWorkload("victim", 1)
						behavioral.MustCreate(ctx, k8sClient, victim)
						behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, victim)
					})

					ginkgo.By("occupying the second rack so the only replacement capacity is the victim node", func() {
						guard := makeRackWorkload("guard", 2)
						behavioral.MustCreate(ctx, k8sClient, guard)
						behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, guard)
					})

					fooNode := admittedNode(gomega.Default, foo)
					victimNode := admittedNode(gomega.Default, victim)
					gomega.Expect(fooNode).NotTo(gomega.Equal(victimNode))

					ginkgo.By("marking the victim as evicted by preemption while it keeps its reservation, as when a preemption is in flight", func() {
						gomega.Eventually(func(g gomega.Gomega) {
							g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(victim), victim)).To(gomega.Succeed())
							g.Expect(workloadpatching.PatchAdmissionStatus(ctx, k8sClient, victim, behavioral.RealClock, func(wl *kueue.Workload) (bool, error) {
								return workloadevict.SetEvictedCondition(wl, behavioral.RealClock.Now(), kueue.WorkloadEvictedByPreemption, "Preempted to accommodate a workload"), nil
							})).To(gomega.Succeed())
						}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
					})

					ginkgo.By("making the node running foo NotReady to trigger a failed replacement", func() {
						nodeToUpdate := &corev1.Node{}
						gomega.Expect(k8sClient.Get(ctx, apitypes.NamespacedName{Name: fooNode}, nodeToUpdate)).Should(gomega.Succeed())
						behavioral.SetNodeCondition(ctx, k8sClient, nodeToUpdate, &corev1.NodeCondition{
							Type:               corev1.NodeReady,
							Status:             corev1.ConditionFalse,
							LastTransitionTime: metav1.NewTime(time.Now().Add(-tas.NodeFailureDelay)),
						})
					})

					ginkgo.By("verifying foo keeps its reservation and is not evicted while the eviction is in flight", func() {
						// Wait until foo has been re-processed: it records the unhealthy
						// node, keeps its quota reservation and is not marked evicted.
						gomega.Eventually(func(g gomega.Gomega) {
							var updated kueue.Workload
							g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(foo), &updated)).To(gomega.Succeed())
							g.Expect(updated.Status.UnhealthyNodes).To(gomega.ContainElement(kueue.UnhealthyNode{Name: fooNode}))
							g.Expect(workload.HasQuotaReservation(&updated)).To(gomega.BeTrue())
							g.Expect(apimeta.FindStatusCondition(updated.Status.Conditions, kueue.WorkloadEvicted)).To(gomega.BeNil())
						}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

						// The deferral must be stable: the workload is re-evaluated each
						// scheduling cycle until the evicted workload is removed, and no
						// cycle may fail-fast evict it in the meantime. The evicted
						// workload must keep holding its reservation (and therefore its
						// usage) for the whole window, which is what keeps the replacement
						// from fitting yet.
						gomega.Consistently(func(g gomega.Gomega) {
							var updatedFoo, updatedVictim kueue.Workload
							g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(foo), &updatedFoo)).To(gomega.Succeed())
							g.Expect(workload.HasQuotaReservation(&updatedFoo)).To(gomega.BeTrue())
							g.Expect(apimeta.FindStatusCondition(updatedFoo.Status.Conditions, kueue.WorkloadEvicted)).To(gomega.BeNil())
							g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(victim), &updatedVictim)).To(gomega.Succeed())
							g.Expect(workload.HasQuotaReservation(&updatedVictim)).To(gomega.BeTrue())
						}, 3*time.Second, behavioral.Interval).Should(gomega.Succeed())
					})

					ginkgo.By("removing the evicted workload so it frees the node foo needs", func() {
						// An evicted workload keeps its usage until it is removed from the
						// cache. Finishing the eviction would requeue the victim, and with
						// no higher-priority workload waiting it would simply be re-admitted
						// onto the same node. Deleting the victim is what frees the capacity
						// without re-queueing it.
						gomega.Expect(k8sClient.Delete(ctx, victim)).To(gomega.Succeed())
					})

					ginkgo.By("verifying foo completes the replacement on the freed node", func() {
						// foo keeps its quota reservation (and therefore its old admission)
						// while deferred, so wait for the admission to move to the node
						// freed by the victim.
						// Second-pass requeues use exponential backoff (capped at 30s), so
						// allow enough time for the next retry after the removal.
						gomega.Eventually(func(g gomega.Gomega) {
							var updated kueue.Workload
							g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(foo), &updated)).To(gomega.Succeed())
							g.Expect(updated.Status.UnhealthyNodes).NotTo(gomega.ContainElement(kueue.UnhealthyNode{Name: fooNode}))
							g.Expect(admittedNode(g, foo)).To(gomega.Equal(victimNode))
						}, behavioral.MediumTimeout, behavioral.Interval).Should(gomega.Succeed())
					})
				})

				ginkgo.It("should update workload UnhealthyNodes immediately when node has NoExecute taint and TASReplaceNodeOnPodTermination is disabled", framework.SlowSpec, func() {
					features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.TASReplaceNodeOnPodTermination, false)

					var wl1 *kueue.Workload
					nodeName := nodes[0].Name

					ginkgo.By("creating a workload", func() {
						wl1 = utiltestingapi.MakeWorkload("wl1", ns.Name).
							PodSets(*utiltestingapi.MakePodSet("worker", 2).
								RequiredTopologyRequest(utiltesting.DefaultBlockTopologyLevel).
								Obj()).
							Queue(kueue.LocalQueueName(localQueue.Name)).Request(corev1.ResourceCPU, "1").Obj()
						behavioral.MustCreate(ctx, k8sClient, wl1)
					})

					ginkgo.By("verify the workload is admitted", func() {
						behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1)
					})

					ginkgo.By("applying NoExecute taint to the node", func() {
						nodeToUpdate := &corev1.Node{}
						gomega.Expect(k8sClient.Get(ctx, apitypes.NamespacedName{Name: nodeName}, nodeToUpdate)).Should(gomega.Succeed())
						nodeToUpdate.Spec.Taints = append(nodeToUpdate.Spec.Taints, corev1.Taint{
							Key:    "example.com/failure",
							Value:  "true",
							Effect: corev1.TaintEffectNoExecute,
						})
						gomega.Expect(k8sClient.Update(ctx, nodeToUpdate)).Should(gomega.Succeed())
					})

					ginkgo.By("verify the workload eventually gets an entry in unhealthyNodes list", func() {
						gomega.Eventually(func(g gomega.Gomega) {
							g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl1), wl1)).To(gomega.Succeed())
							g.Expect(wl1.Status.UnhealthyNodes).To(gomega.ContainElement(kueue.UnhealthyNode{Name: nodeName}))
						}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
					})
				})

				ginkgo.It("should not evict workload when node has NoSchedule taint, one pod is Running and another is Terminated", framework.SlowSpec, func() {
					features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.TASReplaceNodeOnNodeTaints, true)

					var wl1 *kueue.Workload
					nodeName := nodes[0].Name

					ginkgo.By("creating a workload", func() {
						wl1 = utiltestingapi.MakeWorkload("wl1", ns.Name).
							PodSets(*utiltestingapi.MakePodSet("worker", 2).
								RequiredTopologyRequest(utiltesting.DefaultBlockTopologyLevel).
								Obj()).
							Queue(kueue.LocalQueueName(localQueue.Name)).Request(corev1.ResourceCPU, "1").Obj()
						behavioral.MustCreate(ctx, k8sClient, wl1)
					})

					ginkgo.By("verify the workload is admitted", func() {
						behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1)
					})

					var pod1, pod2 *corev1.Pod
					ginkgo.By("creating two pods for the workload assigned to nodeName", func() {
						pod1 = testingpod.MakePod("pod1", ns.Name).
							Annotation(kueue.WorkloadAnnotation, "wl1").
							NodeSelector("kubernetes.io/hostname", nodeName).
							NodeName(nodeName).
							Obj()
						behavioral.MustCreate(ctx, k8sClient, pod1)

						pod2 = testingpod.MakePod("pod2", ns.Name).
							Annotation(kueue.WorkloadAnnotation, "wl1").
							NodeSelector("kubernetes.io/hostname", nodeName).
							Obj()
						behavioral.MustCreate(ctx, k8sClient, pod2)
					})

					ginkgo.By("setting pod1 to Running and pod2 to Failed", func() {
						pod1.Status.Phase = corev1.PodRunning
						gomega.Expect(k8sClient.Status().Update(ctx, pod1)).Should(gomega.Succeed())

						pod2.Status.Phase = corev1.PodFailed
						gomega.Expect(k8sClient.Status().Update(ctx, pod2)).Should(gomega.Succeed())
					})

					ginkgo.By("applying NoSchedule taint to the node", func() {
						nodeToUpdate := &corev1.Node{}
						gomega.Expect(k8sClient.Get(ctx, apitypes.NamespacedName{Name: nodeName}, nodeToUpdate)).Should(gomega.Succeed())
						nodeToUpdate.Spec.Taints = append(nodeToUpdate.Spec.Taints, corev1.Taint{
							Key:    "example.com/unschedulable",
							Value:  "true",
							Effect: corev1.TaintEffectNoSchedule,
						})
						gomega.Expect(k8sClient.Update(ctx, nodeToUpdate)).Should(gomega.Succeed())
					})

					ginkgo.By("verify workload stays healthy and UnhealthyNodes remains empty", func() {
						gomega.Consistently(func(g gomega.Gomega) {
							g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl1), wl1)).To(gomega.Succeed())
							g.Expect(wl1.Status.UnhealthyNodes).To(gomega.BeEmpty())
						}, 2*time.Second, behavioral.ShortInterval).Should(gomega.Succeed())
					})
				})
				ginkgo.It("should NOT update workload UnhealthyNodes immediately when node has NoExecute taint and TASReplaceNodeOnPodTermination is enabled", framework.SlowSpec, func() {
					features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.TASReplaceNodeOnPodTermination, true)

					var wl1 *kueue.Workload
					nodeName := nodes[0].Name

					ginkgo.By("creating a workload", func() {
						wl1 = utiltestingapi.MakeWorkload("wl1", ns.Name).
							PodSets(*utiltestingapi.MakePodSet("worker", 2).
								RequiredTopologyRequest(utiltesting.DefaultBlockTopologyLevel).
								Obj()).
							Queue(kueue.LocalQueueName(localQueue.Name)).Request(corev1.ResourceCPU, "1").Obj()
						behavioral.MustCreate(ctx, k8sClient, wl1)
					})

					ginkgo.By("verify the workload is admitted", func() {
						behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1)
					})

					var pod *corev1.Pod
					ginkgo.By("creating a pod for the workload assigned to nodeName", func() {
						pod = testingpod.MakePod("pod1", ns.Name).
							Annotation(kueue.WorkloadAnnotation, "wl1").
							Annotation(kueue.PodSetUnconstrainedTopologyAnnotation, "true").
							NodeName(nodeName).
							// envtest doesn't set deletion timestamp when there is no finalizer and we need it for node controller to be triggered
							KueueFinalizer().
							Obj()
						behavioral.MustCreate(ctx, k8sClient, pod)
					})

					ginkgo.By("applying NoExecute taint to the node", func() {
						nodeToUpdate := &corev1.Node{}
						gomega.Expect(k8sClient.Get(ctx, apitypes.NamespacedName{Name: nodeName}, nodeToUpdate)).Should(gomega.Succeed())
						nodeToUpdate.Spec.Taints = append(nodeToUpdate.Spec.Taints, corev1.Taint{
							Key:    "example.com/proactive",
							Value:  "true",
							Effect: corev1.TaintEffectNoExecute,
						})
						gomega.Expect(k8sClient.Update(ctx, nodeToUpdate)).Should(gomega.Succeed())
					})

					ginkgo.By("verify UnhealthyNodes is NOT updated while pod is running", func() {
						gomega.Consistently(func(g gomega.Gomega) {
							g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl1), wl1)).To(gomega.Succeed())
							g.Expect(wl1.Status.UnhealthyNodes).NotTo(gomega.ContainElement(kueue.UnhealthyNode{Name: nodeName}))
						}, behavioral.ConsistentDuration, behavioral.ShortInterval).Should(gomega.Succeed())
					})

					ginkgo.By("deleting the pod", func() {
						gomega.Expect(k8sClient.Delete(ctx, pod)).Should(gomega.Succeed())
					})

					ginkgo.By("verify the workload eventually gets an entry in unhealthyNodes list", func() {
						gomega.Eventually(func(g gomega.Gomega) {
							g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl1), wl1)).To(gomega.Succeed())
							g.Expect(wl1.Status.UnhealthyNodes).To(gomega.ContainElement(kueue.UnhealthyNode{Name: nodeName}))
						}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
					})

					ginkgo.By("remove pod finalizer to allow cleanup", func() {
						gomega.Expect(client.IgnoreNotFound(k8sClient.Get(ctx, client.ObjectKeyFromObject(pod), pod))).Should(gomega.Succeed())
						pod.Finalizers = nil
						gomega.Expect(client.IgnoreNotFound(k8sClient.Update(ctx, pod))).Should(gomega.Succeed())
					})
				})
				ginkgo.It("should selectively recover workload health based on tolerations of remaining taints", framework.SlowSpec, func() {
					var wl1, wl2 *kueue.Workload
					nodeName := nodes[0].Name // x3
					taintA := corev1.Taint{Key: "example.com/taint-a", Value: "true", Effect: corev1.TaintEffectNoExecute}
					taintB := corev1.Taint{Key: "example.com/taint-b", Value: "true", Effect: corev1.TaintEffectNoExecute}

					ginkgo.By("creating workloads", func() {
						wl1 = utiltestingapi.MakeWorkload("wl1", ns.Name).
							PodSets(*utiltestingapi.MakePodSet("worker", 1).
								RequiredTopologyRequest(utiltesting.DefaultBlockTopologyLevel).
								NodeSelector(map[string]string{corev1.LabelHostname: nodeName}).
								Toleration(corev1.Toleration{
									Key:      taintA.Key,
									Operator: corev1.TolerationOpEqual,
									Value:    taintA.Value,
									Effect:   taintA.Effect,
								}).
								Obj()).
							Queue(kueue.LocalQueueName(localQueue.Name)).Request(corev1.ResourceCPU, "100m").Obj()
						behavioral.MustCreate(ctx, k8sClient, wl1)

						wl2 = utiltestingapi.MakeWorkload("wl2", ns.Name).
							PodSets(*utiltestingapi.MakePodSet("worker", 1).
								RequiredTopologyRequest(utiltesting.DefaultBlockTopologyLevel).
								NodeSelector(map[string]string{corev1.LabelHostname: nodeName}).
								Obj()).
							Queue(kueue.LocalQueueName(localQueue.Name)).Request(corev1.ResourceCPU, "100m").Obj()
						behavioral.MustCreate(ctx, k8sClient, wl2)
					})

					ginkgo.By("verify the workloads are admitted", func() {
						behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1, wl2)
					})

					ginkgo.By("applying both taints to the node", func() {
						nodeToUpdate := &corev1.Node{}
						gomega.Expect(k8sClient.Get(ctx, apitypes.NamespacedName{Name: nodeName}, nodeToUpdate)).Should(gomega.Succeed())
						nodeToUpdate.Spec.Taints = append(nodeToUpdate.Spec.Taints, taintA, taintB)
						gomega.Expect(k8sClient.Update(ctx, nodeToUpdate)).Should(gomega.Succeed())
					})

					ginkgo.By("verify both workloads eventually get an entry in unhealthyNodes list", func() {
						gomega.Eventually(func(g gomega.Gomega) {
							g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl1), wl1)).To(gomega.Succeed())
							g.Expect(wl1.Status.UnhealthyNodes).To(gomega.ContainElement(kueue.UnhealthyNode{Name: nodeName}))

							g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl2), wl2)).To(gomega.Succeed())
							g.Expect(wl2.Status.UnhealthyNodes).To(gomega.ContainElement(kueue.UnhealthyNode{Name: nodeName}))
						}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
					})

					ginkgo.By("removing Taint B from the node", func() {
						nodeToUpdate := &corev1.Node{}
						gomega.Expect(k8sClient.Get(ctx, apitypes.NamespacedName{Name: nodeName}, nodeToUpdate)).Should(gomega.Succeed())
						nodeToUpdate.Spec.Taints = []corev1.Taint{taintA}
						gomega.Expect(k8sClient.Update(ctx, nodeToUpdate)).Should(gomega.Succeed())
					})

					ginkgo.By("verify WL1 becomes healthy while WL2 stays unhealthy", func() {
						gomega.Eventually(func(g gomega.Gomega) {
							g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl1), wl1)).To(gomega.Succeed())
							g.Expect(wl1.Status.UnhealthyNodes).NotTo(gomega.ContainElement(kueue.UnhealthyNode{Name: nodeName}))

							g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl2), wl2)).To(gomega.Succeed())
							g.Expect(wl2.Status.UnhealthyNodes).To(gomega.ContainElement(kueue.UnhealthyNode{Name: nodeName}))
						}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
					})
				})
				ginkgo.It("should evict workload when multiple assigned nodes get NoExecute taints", framework.SlowSpec, func() {
					var wl1 *kueue.Workload
					node1Name := "x3"
					node2Name := "x1"
					taint := corev1.Taint{Key: "example.com/failure", Value: "true", Effect: corev1.TaintEffectNoExecute}

					ginkgo.By("creating a workload", func() {
						wl1 = utiltestingapi.MakeWorkload("wl1", ns.Name).
							PodSets(*utiltestingapi.MakePodSet("worker", 2).
								PreferredTopologyRequest(utiltesting.DefaultBlockTopologyLevel).
								Obj()).
							Queue(kueue.LocalQueueName(localQueue.Name)).Request(corev1.ResourceCPU, "1").Obj()
						behavioral.MustCreate(ctx, k8sClient, wl1)
					})

					ginkgo.By("verify the workload is admitted", func() {
						behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1)
						gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl1), wl1)).To(gomega.Succeed())
						ta := utiltas.InternalFrom(wl1.Status.Admission.PodSetAssignments[0].TopologyAssignment)
						gomega.Expect(ta.Domains).To(gomega.HaveLen(2))
						node1Name = ta.Domains[0].Values[0]
						node2Name = ta.Domains[1].Values[0]
					})

					ginkgo.By("applying NoExecute taint to the first node", func() {
						nodeToUpdate := &corev1.Node{}
						gomega.Expect(k8sClient.Get(ctx, apitypes.NamespacedName{Name: node1Name}, nodeToUpdate)).Should(gomega.Succeed())
						nodeToUpdate.Spec.Taints = append(nodeToUpdate.Spec.Taints, taint)
						gomega.Expect(k8sClient.Update(ctx, nodeToUpdate)).Should(gomega.Succeed())
					})

					ginkgo.By("applying NoExecute taint to the second node", func() {
						nodeToUpdate := &corev1.Node{}
						gomega.Expect(k8sClient.Get(ctx, apitypes.NamespacedName{Name: node2Name}, nodeToUpdate)).Should(gomega.Succeed())
						nodeToUpdate.Spec.Taints = append(nodeToUpdate.Spec.Taints, taint)
						gomega.Expect(k8sClient.Update(ctx, nodeToUpdate)).Should(gomega.Succeed())
					})

					ginkgo.By("verify the workload is evicted due to multiple node failures", func() {
						behavioral.FinishEvictionForWorkloads(ctx, k8sClient, wl1)
						gomega.Eventually(func(g gomega.Gomega) {
							updatedWl := &kueue.Workload{}
							g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl1), updatedWl)).To(gomega.Succeed())
							g.Expect(updatedWl.Status.UnhealthyNodes).To(gomega.BeEmpty(), "UnhealthyNodes should be cleared after eviction due to multiple node failures")
						}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
					})
				})
				ginkgo.It("should replace a tainted node with a new one within the same block", framework.SlowSpec, func() {
					var wl1 *kueue.Workload
					nodeName := "x3"
					taint := corev1.Taint{Key: "example.com/failure", Value: "true", Effect: corev1.TaintEffectNoExecute}

					ginkgo.By("creating a workload", func() {
						wl1 = utiltestingapi.MakeWorkload("wl1", ns.Name).
							PodSets(*utiltestingapi.MakePodSet("worker", 2).
								PreferredTopologyRequest(utiltesting.DefaultBlockTopologyLevel).
								Obj()).
							Queue(kueue.LocalQueueName(localQueue.Name)).Request(corev1.ResourceCPU, "1").Obj()
						behavioral.MustCreate(ctx, k8sClient, wl1)
					})

					ginkgo.By("verify the workload is admitted", func() {
						behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1)
						gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl1), wl1)).To(gomega.Succeed())
						ta := utiltas.InternalFrom(wl1.Status.Admission.PodSetAssignments[0].TopologyAssignment)
						gomega.Expect(ta.Domains).To(gomega.HaveLen(2))
						nodeName = ta.Domains[0].Values[0]
					})

					ginkgo.By("applying NoExecute taint to the first node", func() {
						nodeToUpdate := &corev1.Node{}
						gomega.Expect(k8sClient.Get(ctx, apitypes.NamespacedName{Name: nodeName}, nodeToUpdate)).Should(gomega.Succeed())
						nodeToUpdate.Spec.Taints = append(nodeToUpdate.Spec.Taints, taint)
						gomega.Expect(k8sClient.Update(ctx, nodeToUpdate)).Should(gomega.Succeed())
					})

					ginkgo.By("verify that workload is rescheduled using a free node in the same block", func() {
						gomega.Eventually(func(g gomega.Gomega) {
							g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl1), wl1)).To(gomega.Succeed())
							ta := utiltas.InternalFrom(wl1.Status.Admission.PodSetAssignments[0].TopologyAssignment)
							g.Expect(ta.Domains).To(gomega.HaveLen(2))
							assignedNode1 := ta.Domains[0].Values[0]
							assignedNode2 := ta.Domains[1].Values[0]
							g.Expect([]string{assignedNode1, assignedNode2}).NotTo(gomega.ContainElement(nodeName))
							g.Expect(wl1.Status.UnhealthyNodes).NotTo(gomega.ContainElement(kueue.UnhealthyNode{Name: nodeName}))
						}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
					})
				})

				ginkgo.It("should catch and terminate a late pod assigned to a replaced node", framework.SlowSpec, func() {
					features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.TASReplaceNodeOnNodeTaints, true)

					var wl1 *kueue.Workload
					nodeName := "x3"
					taint := corev1.Taint{Key: "example.com/failure", Value: "true", Effect: corev1.TaintEffectNoExecute}

					ginkgo.By("creating a workload", func() {
						wl1 = utiltestingapi.MakeWorkload("wl1", ns.Name).
							PodSets(*utiltestingapi.MakePodSet("worker", 2).
								PreferredTopologyRequest(utiltesting.DefaultBlockTopologyLevel).
								Obj()).
							Queue(kueue.LocalQueueName(localQueue.Name)).Request(corev1.ResourceCPU, "1").Obj()
						behavioral.MustCreate(ctx, k8sClient, wl1)
					})

					ginkgo.By("verify the workload is admitted", func() {
						behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1)
						gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl1), wl1)).To(gomega.Succeed())
						ta := utiltas.InternalFrom(wl1.Status.Admission.PodSetAssignments[0].TopologyAssignment)
						gomega.Expect(ta.Domains).To(gomega.HaveLen(2))
						nodeName = ta.Domains[0].Values[0]
					})

					// We must clear the NodeName to ensure the pods are treated as truly Pending (unscheduled)
					// rather than running/scheduled pods in Pending phase.
					createPodsForWorkload(wl1, ns.Name, false, false)

					ginkgo.By("applying NoExecute taint to the first node", func() {
						nodeToUpdate := &corev1.Node{}
						gomega.Expect(k8sClient.Get(ctx, apitypes.NamespacedName{Name: nodeName}, nodeToUpdate)).Should(gomega.Succeed())
						nodeToUpdate.Spec.Taints = append(nodeToUpdate.Spec.Taints, taint)
						gomega.Expect(k8sClient.Update(ctx, nodeToUpdate)).Should(gomega.Succeed())
					})

					ginkgo.By("verify that originally scheduled pods are automatically marked as Failed by Kueue", func() {
						gomega.Eventually(func(g gomega.Gomega) {
							var pods corev1.PodList
							g.Expect(k8sClient.List(ctx, &pods, client.InNamespace(ns.Name), client.MatchingLabels{kueue.WorkloadAnnotation: wl1.Name})).Should(gomega.Succeed())
							for _, pod := range pods.Items {
								g.Expect(pod.Status.Phase).To(gomega.Equal(corev1.PodFailed))
								g.Expect(pod.Status.Conditions).To(gomega.ContainElement(gomega.BeComparableTo(corev1.PodCondition{
									Type:   "TerminatedByKueue",
									Status: corev1.ConditionTrue,
								}, cmpopts.IgnoreFields(corev1.PodCondition{}, "LastTransitionTime", "Message", "LastProbeTime", "Reason"))))
							}
						}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
					})

					ginkgo.By("verify that workload is rescheduled using a free node in the same block", func() {
						gomega.Eventually(func(g gomega.Gomega) {
							g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl1), wl1)).To(gomega.Succeed())
							ta := utiltas.InternalFrom(wl1.Status.Admission.PodSetAssignments[0].TopologyAssignment)
							g.Expect(ta.Domains).To(gomega.HaveLen(2))
							assignedNode1 := ta.Domains[0].Values[0]
							assignedNode2 := ta.Domains[1].Values[0]
							g.Expect([]string{assignedNode1, assignedNode2}).NotTo(gomega.ContainElement(nodeName))
							g.Expect(wl1.Status.UnhealthyNodes).NotTo(gomega.ContainElement(kueue.UnhealthyNode{Name: nodeName}))
						}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
					})

					var latePod *corev1.Pod
					ginkgo.By("simulating a late pod mapped to the old failed node by its nodeSelector", func() {
						latePod = testingpod.MakePod(fmt.Sprintf("%s-late-arriving", wl1.Name), ns.Name).
							Annotation(kueue.WorkloadAnnotation, wl1.Name).
							Annotation(kueue.PodSetRequiredTopologyAnnotation, utiltesting.DefaultBlockTopologyLevel).
							Annotation(kueue.WorkloadSliceNameAnnotation, wl1.Name).
							NodeSelector(corev1.LabelHostname, nodeName).
							Obj()
						behavioral.MustCreate(ctx, k8sClient, latePod)

						// The controller only listens to Pending pods
						latePod.Status.Phase = corev1.PodPending
						gomega.Expect(k8sClient.Status().Update(ctx, latePod)).Should(gomega.Succeed())
					})

					ginkgo.By("verify that the controller catches and fails the late pod", func() {
						gomega.Eventually(func(g gomega.Gomega) {
							updatedPod := &corev1.Pod{}
							g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(latePod), updatedPod)).To(gomega.Succeed())
							g.Expect(updatedPod.Status.Phase).To(gomega.Equal(corev1.PodFailed))
							g.Expect(updatedPod.Status.Conditions).To(gomega.ContainElement(gomega.BeComparableTo(corev1.PodCondition{
								Type:   "TerminatedByKueue",
								Status: corev1.ConditionTrue,
							}, cmpopts.IgnoreFields(corev1.PodCondition{}, "LastTransitionTime", "Message", "LastProbeTime", "Reason"))))
						}, behavioral.LongTimeout, behavioral.Interval).Should(gomega.Succeed())
					})
				})

				ginkgo.It("should evict the Workload if no replacement is possible after NoExecute taint", framework.SlowSpec, func() {
					var wl1 *kueue.Workload
					nodeName := nodes[0].Name // x3
					taint := corev1.Taint{Key: "example.com/failure", Value: "true", Effect: corev1.TaintEffectNoExecute}

					ginkgo.By("creating a workload requiring a full rack", func() {
						// Rack r2 has exactly 2 nodes: x4 and x2. If one fails, it cannot be replaced within the same rack.
						wl1 = utiltestingapi.MakeWorkload("wl1", ns.Name).
							PodSets(*utiltestingapi.MakePodSet("worker", 2).
								RequiredTopologyRequest(utiltesting.DefaultRackTopologyLevel).
								Obj()).
							Queue(kueue.LocalQueueName(localQueue.Name)).Request(corev1.ResourceCPU, "1").Obj()
						behavioral.MustCreate(ctx, k8sClient, wl1)
					})

					ginkgo.By("verify the workload is admitted on a rack", func() {
						behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1)
						gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl1), wl1)).To(gomega.Succeed())
						ta := utiltas.InternalFrom(wl1.Status.Admission.PodSetAssignments[0].TopologyAssignment)
						gomega.Expect(ta.Domains).To(gomega.HaveLen(2))
						nodeName = ta.Domains[0].Values[0]
					})

					ginkgo.By("applying NoExecute taint to one of the assigned nodes", func() {
						nodeToUpdate := &corev1.Node{}
						gomega.Expect(k8sClient.Get(ctx, apitypes.NamespacedName{Name: nodeName}, nodeToUpdate)).Should(gomega.Succeed())
						nodeToUpdate.Spec.Taints = append(nodeToUpdate.Spec.Taints, taint)
						gomega.Expect(k8sClient.Update(ctx, nodeToUpdate)).Should(gomega.Succeed())
					})

					ginkgo.By("verify the workload is evicted due to no replacement possible", func() {
						behavioral.FinishEvictionForWorkloads(ctx, k8sClient, wl1)
						gomega.Eventually(func(g gomega.Gomega) {
							updatedWl := &kueue.Workload{}
							g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl1), updatedWl)).To(gomega.Succeed())
							g.Expect(updatedWl.Status.UnhealthyNodes).To(gomega.BeEmpty())
						}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
					})

					ginkgo.By("verify the workload is eventually placed on another rack", func() {
						behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1)
						gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl1), wl1)).To(gomega.Succeed())
						ta := utiltas.InternalFrom(wl1.Status.Admission.PodSetAssignments[0].TopologyAssignment)
						gomega.Expect(ta.Domains).To(gomega.HaveLen(2))
						assignedNode1 := ta.Domains[0].Values[0]
						assignedNode2 := ta.Domains[1].Values[0]
						gomega.Expect([]string{assignedNode1, assignedNode2}).NotTo(gomega.ContainElement(nodeName))
					})
				})
				ginkgo.It("should not evict workload when nodes get NoExecute taints that are tolerated", framework.SlowSpec, func() {
					var wl1 *kueue.Workload
					node1Name := "x3"
					node2Name := "x1"
					taint := corev1.Taint{Key: "example.com/tolerable", Value: "true", Effect: corev1.TaintEffectNoExecute}

					ginkgo.By("creating a workload with a toleration for the taint", func() {
						wl1 = utiltestingapi.MakeWorkload("wl1", ns.Name).
							PodSets(*utiltestingapi.MakePodSet("worker", 2).
								PreferredTopologyRequest(utiltesting.DefaultBlockTopologyLevel).
								Toleration(corev1.Toleration{
									Key:      taint.Key,
									Operator: corev1.TolerationOpEqual,
									Value:    taint.Value,
									Effect:   taint.Effect,
								}).
								Obj()).
							Queue(kueue.LocalQueueName(localQueue.Name)).Request(corev1.ResourceCPU, "1").Obj()
						behavioral.MustCreate(ctx, k8sClient, wl1)
					})

					ginkgo.By("verify the workload is admitted", func() {
						behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1)
						gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl1), wl1)).To(gomega.Succeed())
						ta := utiltas.InternalFrom(wl1.Status.Admission.PodSetAssignments[0].TopologyAssignment)
						gomega.Expect(ta.Domains).To(gomega.HaveLen(2))
						node1Name = ta.Domains[0].Values[0]
						node2Name = ta.Domains[1].Values[0]
					})

					ginkgo.By("applying the tolerated NoExecute taint to the first node", func() {
						nodeToUpdate := &corev1.Node{}
						gomega.Expect(k8sClient.Get(ctx, apitypes.NamespacedName{Name: node1Name}, nodeToUpdate)).Should(gomega.Succeed())
						nodeToUpdate.Spec.Taints = append(nodeToUpdate.Spec.Taints, taint)
						gomega.Expect(k8sClient.Update(ctx, nodeToUpdate)).Should(gomega.Succeed())
					})

					ginkgo.By("applying the tolerated NoExecute taint to the second node", func() {
						nodeToUpdate := &corev1.Node{}
						gomega.Expect(k8sClient.Get(ctx, apitypes.NamespacedName{Name: node2Name}, nodeToUpdate)).Should(gomega.Succeed())
						nodeToUpdate.Spec.Taints = append(nodeToUpdate.Spec.Taints, taint)
						gomega.Expect(k8sClient.Update(ctx, nodeToUpdate)).Should(gomega.Succeed())
					})

					ginkgo.By("verify workload stays healthy and UnhealthyNodes remains empty", func() {
						gomega.Consistently(func(g gomega.Gomega) {
							g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl1), wl1)).To(gomega.Succeed())
							g.Expect(wl1.Status.UnhealthyNodes).To(gomega.BeEmpty())
						}, behavioral.ConsistentDuration, behavioral.ShortInterval).Should(gomega.Succeed())
					})
				})

				ginkgo.It("should evict workload when node has NoSchedule taint and no pods are present", framework.SlowSpec, func() {
					features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.TASReplaceNodeOnNodeTaints, true)
					features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.TASReplaceNodeOnPodTermination, false)

					var wl1 *kueue.Workload
					nodeName := nodes[0].Name

					ginkgo.By("creating a workload", func() {
						wl1 = utiltestingapi.MakeWorkload("wl1", ns.Name).
							PodSets(*utiltestingapi.MakePodSet("worker", 2).
								RequiredTopologyRequest(utiltesting.DefaultBlockTopologyLevel).
								Obj()).
							Queue(kueue.LocalQueueName(localQueue.Name)).Request(corev1.ResourceCPU, "1").Obj()
						behavioral.MustCreate(ctx, k8sClient, wl1)
					})

					ginkgo.By("verify the workload is admitted", func() {
						behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1)
					})

					ginkgo.By("applying NoSchedule taint to the node", func() {
						nodeToUpdate := &corev1.Node{}
						gomega.Expect(k8sClient.Get(ctx, apitypes.NamespacedName{Name: nodeName}, nodeToUpdate)).Should(gomega.Succeed())
						nodeToUpdate.Spec.Taints = append(nodeToUpdate.Spec.Taints, corev1.Taint{
							Key:    "example.com/unschedulable",
							Value:  "true",
							Effect: corev1.TaintEffectNoSchedule,
						})
						gomega.Expect(k8sClient.Update(ctx, nodeToUpdate)).Should(gomega.Succeed())
					})

					ginkgo.By("verify the workload eventually gets an entry in unhealthyNodes list", func() {
						gomega.Eventually(func(g gomega.Gomega) {
							g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl1), wl1)).To(gomega.Succeed())
							g.Expect(wl1.Status.UnhealthyNodes).To(gomega.ContainElement(kueue.UnhealthyNode{Name: nodeName}))
						}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
					})
				})

				ginkgo.It("should NOT update UnhealthyNodes immediately when node has NoSchedule taint and a pod is running", framework.SlowSpec, func() {
					features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.TASReplaceNodeOnNodeTaints, true)
					features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.TASReplaceNodeOnPodTermination, true)

					var wl1 *kueue.Workload
					nodeName := nodes[0].Name

					ginkgo.By("creating a workload", func() {
						wl1 = utiltestingapi.MakeWorkload("wl1", ns.Name).
							PodSets(*utiltestingapi.MakePodSet("worker", 2).
								RequiredTopologyRequest(utiltesting.DefaultBlockTopologyLevel).
								Obj()).
							Queue(kueue.LocalQueueName(localQueue.Name)).Request(corev1.ResourceCPU, "1").Obj()
						behavioral.MustCreate(ctx, k8sClient, wl1)
					})

					ginkgo.By("verify the workload is admitted", func() {
						behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1)
					})

					var pod *corev1.Pod
					ginkgo.By("creating a running pod for the workload assigned to nodeName", func() {
						pod = testingpod.MakePod("pod1", ns.Name).
							Annotation(kueue.WorkloadAnnotation, "wl1").
							Annotation(kueue.PodSetUnconstrainedTopologyAnnotation, "true").
							NodeName(nodeName).
							Obj()
						behavioral.MustCreate(ctx, k8sClient, pod)

						pod.Status.Phase = corev1.PodRunning
						gomega.Expect(k8sClient.Status().Update(ctx, pod)).Should(gomega.Succeed())
					})

					ginkgo.By("applying NoSchedule taint to the node", func() {
						nodeToUpdate := &corev1.Node{}
						gomega.Expect(k8sClient.Get(ctx, apitypes.NamespacedName{Name: nodeName}, nodeToUpdate)).Should(gomega.Succeed())
						nodeToUpdate.Spec.Taints = append(nodeToUpdate.Spec.Taints, corev1.Taint{
							Key:    "example.com/unschedulable",
							Value:  "true",
							Effect: corev1.TaintEffectNoSchedule,
						})
						gomega.Expect(k8sClient.Update(ctx, nodeToUpdate)).Should(gomega.Succeed())
					})

					ginkgo.By("verify UnhealthyNodes is NOT updated while pod is running", func() {
						gomega.Consistently(func(g gomega.Gomega) {
							g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl1), wl1)).To(gomega.Succeed())
							g.Expect(wl1.Status.UnhealthyNodes).NotTo(gomega.ContainElement(kueue.UnhealthyNode{Name: nodeName}))
						}, behavioral.ConsistentDuration, behavioral.ShortInterval).Should(gomega.Succeed())
					})

					ginkgo.By("deleting the pod", func() {
						gomega.Expect(k8sClient.Delete(ctx, pod)).Should(gomega.Succeed())
					})

					ginkgo.By("verify the workload eventually gets an entry in unhealthyNodes list", func() {
						gomega.Eventually(func(g gomega.Gomega) {
							g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl1), wl1)).To(gomega.Succeed())
							g.Expect(wl1.Status.UnhealthyNodes).To(gomega.ContainElement(kueue.UnhealthyNode{Name: nodeName}))
						}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
					})
				})
			})
		})
	})
})
