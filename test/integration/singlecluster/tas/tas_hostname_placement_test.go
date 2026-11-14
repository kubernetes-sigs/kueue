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
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	eventsv1 "k8s.io/api/events/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	apitypes "k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller/controllerutil"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/controller/tas"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/metrics"
	utiltas "sigs.k8s.io/kueue/pkg/util/tas"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingnode "sigs.k8s.io/kueue/pkg/util/testingjobs/node"
	testingpod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
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

	ginkgo.When("Single TAS Resource Flavor", func() {
		var (
			tasFlavor    *kueue.ResourceFlavor
			topology     *kueue.Topology
			localQueue   *kueue.LocalQueue
			clusterQueue *kueue.ClusterQueue
		)

		ginkgo.Context("Rack and hostname placement", func() {
			ginkgo.When("Nodes are created before test with the hostname being the lowest level", func() {
				var (
					nodes            []corev1.Node
					podWithFinalizer *corev1.Pod
				)

				ginkgo.AfterEach(func() {
					if podWithFinalizer == nil {
						return
					}
					gomega.Eventually(func(g gomega.Gomega) {
						p := &corev1.Pod{}
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(podWithFinalizer), p)).To(gomega.Succeed())
						if controllerutil.RemoveFinalizer(p, "kueue.x-k8s.io/integration-test") {
							g.Expect(k8sClient.Update(ctx, p)).To(gomega.Succeed())
						}
					}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
					podWithFinalizer = nil
				})
				ginkgo.BeforeEach(func() {
					//     b1          b2
					//   /    \      /    \
					//  r1    r2    r1    r2
					//  |      |    |      |
					//  x3    x1    x4    x2
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
							Label(utiltesting.DefaultRackTopologyLevel, "r2").
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
							Label(utiltesting.DefaultBlockTopologyLevel, "b2").
							Label(utiltesting.DefaultRackTopologyLevel, "r1").
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
							Label(utiltesting.DefaultBlockTopologyLevel, "b2").
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

					metrics.ClearClusterQueueMetrics(kueue.ClusterQueueReference(clusterQueue.Name))
				})

				ginkgo.It("should respect TAS usage from workload admitted before Topology re-creation", func() {
					var wl1, wl2 *kueue.Workload
					ginkgo.By("creating a workload which can fit", func() {
						wl1 = utiltestingapi.MakeWorkload("wl1", ns.Name).
							PodSets(*utiltestingapi.MakePodSet("worker", 1).
								RequiredTopologyRequest(corev1.LabelHostname).
								NodeSelector(map[string]string{
									corev1.LabelHostname: "x1",
								}).Obj()).
							Queue(kueue.LocalQueueName(localQueue.Name)).Request(corev1.ResourceCPU, "1").Obj()
						behavioral.MustCreate(ctx, k8sClient, wl1)
					})

					ginkgo.By("verify the workload is admitted", func() {
						behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1)
						behavioral.ExpectAdmittedWorkloadsTotalMetric(clusterQueue, "", 1)
					})

					ginkgo.By("wait for the finalizer to be added to the topology", func() {
						var updatedTopology kueue.Topology
						gomega.Eventually(func(g gomega.Gomega) {
							g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(topology), &updatedTopology)).To(gomega.Succeed())
							g.Expect(updatedTopology.Finalizers).Should(gomega.Equal([]string{kueue.ResourceInUseFinalizerName}))
						}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
					})

					ginkgo.By("trigger topology deletion", func() {
						gomega.Expect(behavioral.DeleteObject(ctx, k8sClient, topology)).To(gomega.Succeed())
					})

					ginkgo.By("remove topology finalizers to allow deletion", func() {
						var updatedTopology kueue.Topology
						gomega.Eventually(func(g gomega.Gomega) {
							g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(topology), &updatedTopology)).To(gomega.Succeed())
							updatedTopology.Finalizers = nil
							g.Expect(k8sClient.Update(ctx, &updatedTopology)).To(gomega.Succeed())
						}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
					})

					ginkgo.By("wait for the topology to be fully deleted", func() {
						behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, topology, false)
					})

					ginkgo.By("recreate the topology", func() {
						topology = utiltestingapi.MakeDefaultThreeLevelTopology("default")
						behavioral.MustCreate(ctx, k8sClient, topology)
					})

					ginkgo.By("creating second workload which is blocked by the previous would", func() {
						wl2 = utiltestingapi.MakeWorkload("wl2", ns.Name).
							PodSets(*utiltestingapi.MakePodSet("worker", 1).
								RequiredTopologyRequest(corev1.LabelHostname).
								NodeSelector(map[string]string{
									corev1.LabelHostname: "x1",
								}).Obj()).
							Queue(kueue.LocalQueueName(localQueue.Name)).Request(corev1.ResourceCPU, "1").Obj()
						behavioral.MustCreate(ctx, k8sClient, wl2)
					})

					ginkgo.By("verify the wl2 has not been admitted", func() {
						behavioral.ExpectWorkloadsToBePending(ctx, k8sClient, wl2)
						behavioral.ExpectReservingActiveWorkloadsMetric(clusterQueue, 1)
						behavioral.ExpectPendingWorkloadsMetric(clusterQueue, 0, 1)
						gomega.Consistently(func(g gomega.Gomega) {
							g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl2), wl2)).To(gomega.Succeed())
							g.Expect(workload.IsAdmitted(wl2)).To(gomega.BeFalse())
						}, behavioral.ConsistentDuration, behavioral.ShortInterval).Should(gomega.Succeed())
					})
				})

				ginkgo.It("should correctly account for TAS usage when workloads admitted before Topology re-creation are deleted", func() {
					var wl1, wl2 *kueue.Workload
					ginkgo.By("creating a workload which can fit", func() {
						wl1 = utiltestingapi.MakeWorkload("wl1", ns.Name).
							PodSets(*utiltestingapi.MakePodSet("worker", 1).
								RequiredTopologyRequest(corev1.LabelHostname).
								NodeSelector(map[string]string{
									corev1.LabelHostname: "x1",
								}).Obj()).
							Queue(kueue.LocalQueueName(localQueue.Name)).Request(corev1.ResourceCPU, "1").Obj()
						behavioral.MustCreate(ctx, k8sClient, wl1)
					})

					ginkgo.By("verify the workload is admitted", func() {
						behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1)
						behavioral.ExpectAdmittedWorkloadsTotalMetric(clusterQueue, "", 1)
					})

					ginkgo.By("wait for the finalizer to be added to the topology", func() {
						var updatedTopology kueue.Topology
						gomega.Eventually(func(g gomega.Gomega) {
							g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(topology), &updatedTopology)).To(gomega.Succeed())
							g.Expect(updatedTopology.Finalizers).Should(gomega.Equal([]string{kueue.ResourceInUseFinalizerName}))
						}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
					})

					ginkgo.By("trigger topology deletion", func() {
						gomega.Expect(behavioral.DeleteObject(ctx, k8sClient, topology)).To(gomega.Succeed())
					})

					ginkgo.By("remove topology finalizers to allow deletion", func() {
						var updatedTopology kueue.Topology
						gomega.Eventually(func(g gomega.Gomega) {
							g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(topology), &updatedTopology)).To(gomega.Succeed())
							updatedTopology.Finalizers = nil
							g.Expect(k8sClient.Update(ctx, &updatedTopology)).To(gomega.Succeed())
						}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
					})

					ginkgo.By("wait for the topology to be fully deleted", func() {
						behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, topology, false)
					})

					ginkgo.By("recreate the topology", func() {
						topology = utiltestingapi.MakeDefaultThreeLevelTopology("default")
						behavioral.MustCreate(ctx, k8sClient, topology)
					})

					ginkgo.By("finish the first workload", func() {
						integration.FinishWorkloads(ctx, k8sClient, wl1)
					})

					ginkgo.By("creating second workload which cannot fit", func() {
						wl2 = utiltestingapi.MakeWorkload("wl2", ns.Name).
							PodSets(*utiltestingapi.MakePodSet("worker", 2).
								RequiredTopologyRequest(corev1.LabelHostname).
								NodeSelector(map[string]string{
									corev1.LabelHostname: "x1",
								}).Obj()).
							Queue(kueue.LocalQueueName(localQueue.Name)).Request(corev1.ResourceCPU, "1").Obj()
						behavioral.MustCreate(ctx, k8sClient, wl2)
					})

					ginkgo.By("verify the wl2 has not been admitted", func() {
						behavioral.ExpectWorkloadsToBePending(ctx, k8sClient, wl2)
						behavioral.ExpectReservingActiveWorkloadsMetric(clusterQueue, 0)
						behavioral.ExpectPendingWorkloadsMetric(clusterQueue, 0, 1)
						gomega.Consistently(func(g gomega.Gomega) {
							g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl2), wl2)).To(gomega.Succeed())
							g.Expect(workload.IsAdmitted(wl2)).To(gomega.BeFalse())
						}, behavioral.ConsistentDuration, behavioral.ShortInterval).Should(gomega.Succeed())
					})
				})

				ginkgo.It("should not admit workload which does not fit to the required topology domain", func() {
					ginkgo.By("creating a workload which requires rack, but does not fit in any", func() {
						wl1 := utiltestingapi.MakeWorkload("wl1-inadmissible", ns.Name).
							PodSets(*utiltestingapi.MakePodSet("worker", 4).
								PreferredTopologyRequest(utiltesting.DefaultRackTopologyLevel).
								Obj()).
							Queue(kueue.LocalQueueName(localQueue.Name)).Request(corev1.ResourceCPU, "2").Obj()
						behavioral.MustCreate(ctx, k8sClient, wl1)
					})

					ginkgo.By("verify the workload is inadmissible", func() {
						behavioral.ExpectPendingWorkloadsMetric(clusterQueue, 0, 1)
					})
				})

				ginkgo.It("should admit workload which fits", func() {
					var wl1, wl2 *kueue.Workload
					ginkgo.By("creating a workload which can fit", func() {
						wl1 = utiltestingapi.MakeWorkload("wl1", ns.Name).
							PodSets(*utiltestingapi.MakePodSet("worker", 1).
								PreferredTopologyRequest(utiltesting.DefaultBlockTopologyLevel).
								Obj()).
							Queue(kueue.LocalQueueName(localQueue.Name)).Request(corev1.ResourceCPU, "1").Obj()
						behavioral.MustCreate(ctx, k8sClient, wl1)
					})

					ginkgo.By("verify the workload is admitted", func() {
						behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1)
						behavioral.ExpectAdmittedWorkloadsTotalMetric(clusterQueue, "", 1)
					})

					ginkgo.By("creating second a workload which cannot fit", func() {
						wl2 = utiltestingapi.MakeWorkload("wl2", ns.Name).
							PodSets(*utiltestingapi.MakePodSet("worker-2", 4).
								PreferredTopologyRequest(utiltesting.DefaultBlockTopologyLevel).
								Obj()).
							Queue(kueue.LocalQueueName(localQueue.Name)).Request(corev1.ResourceCPU, "1").Obj()
						behavioral.MustCreate(ctx, k8sClient, wl2)
					})

					ginkgo.By("verify the wl2 has not been admitted", func() {
						behavioral.ExpectWorkloadsToBePending(ctx, k8sClient, wl2)
						behavioral.ExpectReservingActiveWorkloadsMetric(clusterQueue, 1)
					})
				})

				ginkgo.It("should admit workload using TAS without explicit TAS annotation", func() {
					var wl1, wl2 *kueue.Workload
					ginkgo.By("creating a workload without explicit TAS annotation which can fit", func() {
						wl1 = utiltestingapi.MakeWorkload("wl1", ns.Name).
							PodSets(*utiltestingapi.MakePodSet("worker", 2).
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
									{Count: 1, Values: []string{"x3"}},
									{Count: 1, Values: []string{"x1"}},
								},
							}),
						))
					})

					ginkgo.By("creating the second workload without explicit TAS annotation which cannot fit", func() {
						wl2 = utiltestingapi.MakeWorkload("wl2", ns.Name).
							PodSets(*utiltestingapi.MakePodSet("worker-2", 3).
								Obj()).
							Queue(kueue.LocalQueueName(localQueue.Name)).Request(corev1.ResourceCPU, "1").Obj()
						behavioral.MustCreate(ctx, k8sClient, wl2)
					})

					ginkgo.By("verify the wl2 has not been admitted", func() {
						behavioral.ExpectWorkloadsToBePending(ctx, k8sClient, wl2)
						behavioral.ExpectReservingActiveWorkloadsMetric(clusterQueue, 1)
					})

					ginkgo.By("finish wl1 and verify wl2 can fit now", func() {
						integration.FinishWorkloads(ctx, k8sClient, wl1)
						behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl2)
					})
				})
				ginkgo.It("should update workload TopologyAssignment when node is removed", framework.SlowSpec, func() {
					var wl1 *kueue.Workload
					nodeName := nodes[0].Name

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
						gomega.Expect(wl1.Status.Admission.PodSetAssignments[0].TopologyAssignment).Should(gomega.BeComparableTo(
							utiltas.V1Beta2From(&utiltas.TopologyAssignment{
								Levels: []string{corev1.LabelHostname},
								Domains: []utiltas.TopologyDomainAssignment{
									{Count: 1, Values: []string{"x3"}},
									{Count: 1, Values: []string{"x1"}},
								},
							}),
						))
					})

					ginkgo.By("deleting the node", func() {
						nodeToUpdate := &corev1.Node{}
						gomega.Expect(k8sClient.Get(ctx, client.ObjectKey{Name: nodeName}, nodeToUpdate)).Should(gomega.Succeed())
						gomega.Expect(k8sClient.Delete(ctx, nodeToUpdate)).Should(gomega.Succeed())
					})

					ginkgo.By("verify the workload has corrected TopologyAssignment and no UnhealthyNode", func() {
						gomega.Eventually(func(g gomega.Gomega) {
							g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl1), wl1)).To(gomega.Succeed())
							g.Expect(wl1.Status.Admission.PodSetAssignments[0].TopologyAssignment).Should(gomega.BeComparableTo(
								utiltas.V1Beta2From(&utiltas.TopologyAssignment{
									Levels: []string{corev1.LabelHostname},
									Domains: []utiltas.TopologyDomainAssignment{
										{Count: 1, Values: []string{"x1"}},
										{Count: 1, Values: []string{"x4"}},
									},
								}),
							))
							g.Expect(wl1.Status.UnhealthyNodes).NotTo(gomega.ContainElement(kueue.UnhealthyNode{Name: nodeName}))
						}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
					})
				})
				ginkgo.It("should mark a NotReady node only when its running pod starts terminating", framework.SlowSpec, func() {
					var wl1 *kueue.Workload
					var pod *corev1.Pod
					nodeName := nodes[0].Name
					originalAssignment := utiltas.V1Beta2From(&utiltas.TopologyAssignment{
						Levels: []string{corev1.LabelHostname},
						Domains: []utiltas.TopologyDomainAssignment{
							{Count: 1, Values: []string{"x3"}},
							{Count: 1, Values: []string{"x1"}},
						},
					})

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
						gomega.Expect(wl1.Status.Admission.PodSetAssignments[0].TopologyAssignment).Should(gomega.BeComparableTo(originalAssignment))
					})

					ginkgo.By("creating a running pod of the workload on the node", func() {
						pod = testingpod.MakePod("wl1-pod", ns.Name).
							Annotation(kueue.WorkloadAnnotation, wl1.Name).
							Annotation(kueue.PodSetPreferredTopologyAnnotation, utiltesting.DefaultBlockTopologyLevel).
							Finalizer("kueue.x-k8s.io/integration-test").
							NodeName(nodeName).
							Request(corev1.ResourceCPU, "1").
							Obj()
						behavioral.MustCreate(ctx, k8sClient, pod)
						podWithFinalizer = pod
						gomega.Eventually(func(g gomega.Gomega) {
							g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(pod), pod)).To(gomega.Succeed())
							pod.Status.Phase = corev1.PodRunning
							g.Expect(k8sClient.Status().Update(ctx, pod)).To(gomega.Succeed())
						}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
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

					ginkgo.By("verify the node is not marked while the pod is running", func() {
						gomega.Consistently(func(g gomega.Gomega) {
							g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl1), wl1)).To(gomega.Succeed())
							g.Expect(wl1.Status.UnhealthyNodes).To(gomega.BeEmpty())
							g.Expect(wl1.Status.Admission.PodSetAssignments[0].TopologyAssignment).Should(gomega.BeComparableTo(originalAssignment))
						}, behavioral.ConsistentDuration, behavioral.ShortInterval).Should(gomega.Succeed())
					})

					ginkgo.By("deleting the pod so it becomes terminating", func() {
						gomega.Expect(k8sClient.Delete(ctx, pod)).To(gomega.Succeed())
					})

					ginkgo.By("verify the workload has corrected TopologyAssignment", func() {
						gomega.Eventually(func(g gomega.Gomega) {
							g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl1), wl1)).To(gomega.Succeed())
							g.Expect(wl1.Status.Admission.PodSetAssignments[0].TopologyAssignment).Should(gomega.BeComparableTo(
								utiltas.V1Beta2From(&utiltas.TopologyAssignment{
									Levels: []string{corev1.LabelHostname},
									Domains: []utiltas.TopologyDomainAssignment{
										{Count: 1, Values: []string{"x1"}},
										{Count: 1, Values: []string{"x4"}},
									},
								}),
							))
						}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
					})
				})

				ginkgo.It("should update workload TopologyAssignment when node fails", framework.SlowSpec, func() {
					var wl1 *kueue.Workload
					nodeName := nodes[0].Name

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
						gomega.Expect(wl1.Status.Admission.PodSetAssignments[0].TopologyAssignment).Should(gomega.BeComparableTo(
							utiltas.V1Beta2From(&utiltas.TopologyAssignment{
								Levels: []string{corev1.LabelHostname},
								Domains: []utiltas.TopologyDomainAssignment{
									{Count: 1, Values: []string{"x3"}},
									{Count: 1, Values: []string{"x1"}},
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

					ginkgo.By("verify the workload has corrected TopologyAssignment", func() {
						gomega.Eventually(func(g gomega.Gomega) {
							g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl1), wl1)).To(gomega.Succeed())
							g.Expect(wl1.Status.Admission.PodSetAssignments[0].TopologyAssignment).Should(gomega.BeComparableTo(
								utiltas.V1Beta2From(&utiltas.TopologyAssignment{
									Levels: []string{corev1.LabelHostname},
									Domains: []utiltas.TopologyDomainAssignment{
										{Count: 1, Values: []string{"x1"}},
										{Count: 1, Values: []string{"x4"}},
									},
								}),
							))
							g.Expect(wl1.Status.UnhealthyNodes).NotTo(gomega.ContainElement(kueue.UnhealthyNode{Name: nodeName}))
						}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
					})
				})

				ginkgo.It("should keep the TopologyAssignment of a single-Pod-owned workload when node fails and SkipReassignmentForPodOwnedWorkloads is enabled", framework.SlowSpec, func() {
					features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.SkipReassignmentForPodOwnedWorkloads, true)
					var wl1 *kueue.Workload
					nodeName := nodes[0].Name
					originalAssignment := utiltas.V1Beta2From(&utiltas.TopologyAssignment{
						Levels: []string{corev1.LabelHostname},
						Domains: []utiltas.TopologyDomainAssignment{
							{Count: 1, Values: []string{"x3"}},
							{Count: 1, Values: []string{"x1"}},
						},
					})

					ginkgo.By("creating a workload owned by a single Pod", func() {
						wl1 = utiltestingapi.MakeWorkload("wl1", ns.Name).
							OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "owner-pod", "owner-pod-uid").
							PodSets(*utiltestingapi.MakePodSet("worker", 2).
								PreferredTopologyRequest(utiltesting.DefaultBlockTopologyLevel).
								Obj()).
							Queue(kueue.LocalQueueName(localQueue.Name)).Request(corev1.ResourceCPU, "1").Obj()
						behavioral.MustCreate(ctx, k8sClient, wl1)
					})

					ginkgo.By("verify the workload is admitted", func() {
						behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1)
						gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl1), wl1)).To(gomega.Succeed())
						gomega.Expect(wl1.Status.Admission.PodSetAssignments[0].TopologyAssignment).Should(gomega.BeComparableTo(originalAssignment))
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

					ginkgo.By("verify the unhealthy node is cleared without changing the TopologyAssignment", func() {
						gomega.Eventually(func(g gomega.Gomega) {
							g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl1), wl1)).To(gomega.Succeed())
							g.Expect(wl1.Status.UnhealthyNodes).NotTo(gomega.ContainElement(kueue.UnhealthyNode{Name: nodeName}))
							g.Expect(wl1.Status.Admission.PodSetAssignments[0].TopologyAssignment).Should(gomega.BeComparableTo(originalAssignment))
						}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
					})

					ginkgo.By("verify the TopologyAssignment stays unchanged", func() {
						gomega.Consistently(func(g gomega.Gomega) {
							g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl1), wl1)).To(gomega.Succeed())
							g.Expect(wl1.Status.Admission.PodSetAssignments[0].TopologyAssignment).Should(gomega.BeComparableTo(originalAssignment))
						}, behavioral.ConsistentDuration, behavioral.ShortInterval).Should(gomega.Succeed())
					})
				})

				ginkgo.It("should keep the TopologyAssignment of a single-Pod-owned workload when the node is marked via TASReplaceNodeOnPodTermination", framework.SlowSpec, func() {
					features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.SkipReassignmentForPodOwnedWorkloads, true)
					features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.TASReplaceNodeOnPodTermination, true)
					var wl1 *kueue.Workload
					nodeName := nodes[0].Name
					originalAssignment := utiltas.V1Beta2From(&utiltas.TopologyAssignment{
						Levels: []string{corev1.LabelHostname},
						Domains: []utiltas.TopologyDomainAssignment{
							{Count: 1, Values: []string{"x3"}},
							{Count: 1, Values: []string{"x1"}},
						},
					})

					ginkgo.By("creating a workload owned by a single Pod", func() {
						wl1 = utiltestingapi.MakeWorkload("wl1", ns.Name).
							OwnerReference(corev1.SchemeGroupVersion.WithKind("Pod"), "owner-pod", "owner-pod-uid").
							PodSets(*utiltestingapi.MakePodSet("worker", 2).
								PreferredTopologyRequest(utiltesting.DefaultBlockTopologyLevel).
								Obj()).
							Queue(kueue.LocalQueueName(localQueue.Name)).Request(corev1.ResourceCPU, "1").Obj()
						behavioral.MustCreate(ctx, k8sClient, wl1)
					})

					ginkgo.By("verify the workload is admitted", func() {
						behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1)
						gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl1), wl1)).To(gomega.Succeed())
						gomega.Expect(wl1.Status.Admission.PodSetAssignments[0].TopologyAssignment).Should(gomega.BeComparableTo(originalAssignment))
					})

					ginkgo.By("making the node NotReady without waiting for NodeFailureDelay", func() {
						nodeToUpdate := &corev1.Node{}
						gomega.Expect(k8sClient.Get(ctx, apitypes.NamespacedName{Name: nodeName}, nodeToUpdate)).Should(gomega.Succeed())

						behavioral.SetNodeCondition(ctx, k8sClient, nodeToUpdate, &corev1.NodeCondition{
							Type:               corev1.NodeReady,
							Status:             corev1.ConditionFalse,
							LastTransitionTime: metav1.NewTime(time.Now()),
						})
					})

					ginkgo.By("verify the unhealthy node is cleared without changing the TopologyAssignment", func() {
						gomega.Eventually(func(g gomega.Gomega) {
							g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl1), wl1)).To(gomega.Succeed())
							g.Expect(wl1.Status.UnhealthyNodes).NotTo(gomega.ContainElement(kueue.UnhealthyNode{Name: nodeName}))
							g.Expect(wl1.Status.Admission.PodSetAssignments[0].TopologyAssignment).Should(gomega.BeComparableTo(originalAssignment))
						}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
					})

					ginkgo.By("verify the TopologyAssignment stays unchanged", func() {
						gomega.Consistently(func(g gomega.Gomega) {
							g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl1), wl1)).To(gomega.Succeed())
							g.Expect(wl1.Status.Admission.PodSetAssignments[0].TopologyAssignment).Should(gomega.BeComparableTo(originalAssignment))
						}, behavioral.ConsistentDuration, behavioral.ShortInterval).Should(gomega.Succeed())
					})
				})

				ginkgo.It("should update the TopologyAssignment of a Job-owned workload when node fails and SkipReassignmentForPodOwnedWorkloads is enabled", framework.SlowSpec, func() {
					features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.SkipReassignmentForPodOwnedWorkloads, true)
					var wl1 *kueue.Workload
					nodeName := nodes[0].Name

					ginkgo.By("creating a workload owned by a Job", func() {
						wl1 = utiltestingapi.MakeWorkload("wl1", ns.Name).
							OwnerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "owner-job", "owner-job-uid").
							PodSets(*utiltestingapi.MakePodSet("worker", 2).
								PreferredTopologyRequest(utiltesting.DefaultBlockTopologyLevel).
								Obj()).
							Queue(kueue.LocalQueueName(localQueue.Name)).Request(corev1.ResourceCPU, "1").Obj()
						behavioral.MustCreate(ctx, k8sClient, wl1)
					})

					ginkgo.By("verify the workload is admitted", func() {
						behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1)
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

					ginkgo.By("verify the workload has corrected TopologyAssignment", func() {
						gomega.Eventually(func(g gomega.Gomega) {
							g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl1), wl1)).To(gomega.Succeed())
							g.Expect(wl1.Status.Admission.PodSetAssignments[0].TopologyAssignment).Should(gomega.BeComparableTo(
								utiltas.V1Beta2From(&utiltas.TopologyAssignment{
									Levels: []string{corev1.LabelHostname},
									Domains: []utiltas.TopologyDomainAssignment{
										{Count: 1, Values: []string{"x1"}},
										{Count: 1, Values: []string{"x4"}},
									},
								}),
							))
							g.Expect(wl1.Status.UnhealthyNodes).NotTo(gomega.ContainElement(kueue.UnhealthyNode{Name: nodeName}))
						}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
					})
				})
				ginkgo.It("should not panic when unadmitted workload exists during node deletion", func() {
					var wl1, wl2 *kueue.Workload
					nodeName := nodes[0].Name

					ginkgo.By("creating a workload that gets admitted", func() {
						wl1 = utiltestingapi.MakeWorkload("wl1", ns.Name).
							PodSets(*utiltestingapi.MakePodSet("worker", 2).
								PreferredTopologyRequest(utiltesting.DefaultBlockTopologyLevel).
								Obj()).
							Queue(kueue.LocalQueueName(localQueue.Name)).Request(corev1.ResourceCPU, "1").Obj()
						behavioral.MustCreate(ctx, k8sClient, wl1)
					})

					ginkgo.By("verify the first workload is admitted", func() {
						behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1)
					})

					ginkgo.By("creating a second workload that stays pending (not enough resources)", func() {
						wl2 = utiltestingapi.MakeWorkload("wl2", ns.Name).
							PodSets(*utiltestingapi.MakePodSet("worker", 10).
								PreferredTopologyRequest(utiltesting.DefaultBlockTopologyLevel).
								Obj()).
							Queue(kueue.LocalQueueName(localQueue.Name)).Request(corev1.ResourceCPU, "1").Obj()
						behavioral.MustCreate(ctx, k8sClient, wl2)
					})

					ginkgo.By("verify the second workload is pending (unadmitted, nil Admission)", func() {
						behavioral.ExpectWorkloadsToBePending(ctx, k8sClient, wl2)
					})

					ginkgo.By("deleting the node to trigger node reconciliation", func() {
						nodeToDelete := &corev1.Node{Name: nodeName}
						gomega.Expect(k8sClient.Delete(ctx, nodeToDelete)).Should(gomega.Succeed())
						behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, nodeToDelete, false)
					})

					ginkgo.By("verify the admitted workload is updated without panic", func() {
						gomega.Eventually(func(g gomega.Gomega) {
							g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl1), wl1)).To(gomega.Succeed())
							g.Expect(wl1.Status.Admission).NotTo(gomega.BeNil())
							g.Expect(wl1.Status.Admission.PodSetAssignments[0].TopologyAssignment).Should(gomega.BeComparableTo(
								utiltas.V1Beta2From(&utiltas.TopologyAssignment{
									Levels: []string{corev1.LabelHostname},
									Domains: []utiltas.TopologyDomainAssignment{
										{Count: 1, Values: []string{"x1"}},
										{Count: 1, Values: []string{"x4"}},
									},
								}),
							))
						}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
					})

					ginkgo.By("verify the pending workload is still pending", func() {
						behavioral.ExpectWorkloadsToBePending(ctx, k8sClient, wl2)
					})
				})

				ginkgo.It("should not add unhealthy node when node is deleted but late pods exist", framework.SlowSpec, func() {
					var wl *kueue.Workload
					nodeName := nodes[0].Name
					var terminatingPodName string

					ginkgo.By("creating a workload that gets admitted", func() {
						wl = utiltestingapi.MakeWorkload("wl", ns.Name).
							PodSets(*utiltestingapi.MakePodSet("worker", 2).
								PreferredTopologyRequest(utiltesting.DefaultBlockTopologyLevel).
								Obj()).
							Queue(kueue.LocalQueueName(localQueue.Name)).Request(corev1.ResourceCPU, "1").Obj()
						behavioral.MustCreate(ctx, k8sClient, wl)
					})

					ginkgo.By("verify the workload is admitted to x3 and x1", func() {
						behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl)
						gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), wl)).To(gomega.Succeed())
						gomega.Expect(wl.Status.Admission.PodSetAssignments[0].TopologyAssignment).Should(gomega.BeComparableTo(
							utiltas.V1Beta2From(&utiltas.TopologyAssignment{
								Levels: []string{corev1.LabelHostname},
								Domains: []utiltas.TopologyDomainAssignment{
									{Count: 1, Values: []string{"x3"}},
									{Count: 1, Values: []string{"x1"}},
								},
							}),
						))
					})

					createPodsForWorkload(wl, ns.Name, true, false)

					ginkgo.By("make one pod terminating on x3", func() {
						pod0 := &corev1.Pod{}
						gomega.Expect(k8sClient.Get(ctx, apitypes.NamespacedName{Name: fmt.Sprintf("%s-0", wl.Name), Namespace: ns.Name}, pod0)).Should(gomega.Succeed())
						pod1 := &corev1.Pod{}
						gomega.Expect(k8sClient.Get(ctx, apitypes.NamespacedName{Name: fmt.Sprintf("%s-1", wl.Name), Namespace: ns.Name}, pod1)).Should(gomega.Succeed())

						for _, podCopy := range []*corev1.Pod{pod0, pod1} {
							if podCopy.Spec.NodeSelector[corev1.LabelHostname] == nodeName {
								terminatingPodName = podCopy.Name
								podCopy.Finalizers = append(podCopy.Finalizers, "kueue.x-k8s.io/dummy-finalizer")
								gomega.Expect(k8sClient.Update(ctx, podCopy)).Should(gomega.Succeed())
								gomega.Expect(k8sClient.Delete(ctx, podCopy)).Should(gomega.Succeed())
							} else {
								gomega.Expect(k8sClient.Delete(ctx, podCopy)).Should(gomega.Succeed())
							}
						}
					})

					ginkgo.By("make node x3 NotReady to trigger node replacement", func() {
						nodeToUpdate := &corev1.Node{}
						gomega.Expect(k8sClient.Get(ctx, apitypes.NamespacedName{Name: nodeName}, nodeToUpdate)).Should(gomega.Succeed())
						behavioral.SetNodeCondition(ctx, k8sClient, nodeToUpdate, &corev1.NodeCondition{
							Type:               corev1.NodeReady,
							Status:             corev1.ConditionFalse,
							LastTransitionTime: metav1.NewTime(time.Now().Add(-tas.NodeFailureDelay)),
						})
					})

					ginkgo.By("verify Hot Swap completes", func() {
						gomega.Eventually(func(g gomega.Gomega) {
							g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), wl)).To(gomega.Succeed())
							g.Expect(wl.Status.Admission.PodSetAssignments[0].TopologyAssignment).Should(gomega.BeComparableTo(
								utiltas.V1Beta2From(&utiltas.TopologyAssignment{
									Levels: []string{corev1.LabelHostname},
									Domains: []utiltas.TopologyDomainAssignment{
										{Count: 1, Values: []string{"x1"}},
										{Count: 1, Values: []string{"x4"}},
									},
								}),
							))
						}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
					})

					ginkgo.By("verify UnhealthyNodes becomes empty after Hot Swap", func() {
						gomega.Eventually(func(g gomega.Gomega) {
							g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), wl)).To(gomega.Succeed())
							g.Expect(wl.Status.UnhealthyNodes).Should(gomega.BeEmpty())
						}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
					})

					ginkgo.By("delete node object entirely", func() {
						nodeToDelete := &corev1.Node{Name: nodeName}
						gomega.Expect(k8sClient.Delete(ctx, nodeToDelete)).Should(gomega.Succeed())
					})

					ginkgo.By("verify unhealthy nodes is not polluted", func() {
						gomega.Consistently(func(g gomega.Gomega) {
							g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), wl)).To(gomega.Succeed())
							g.Expect(wl.Status.UnhealthyNodes).Should(gomega.BeEmpty())
						}, behavioral.ConsistentDuration, behavioral.ShortInterval).Should(gomega.Succeed())
					})

					ginkgo.By("cleanup pod finalizer", func() {
						podToCleanup := &corev1.Pod{}
						gomega.Eventually(func(g gomega.Gomega) {
							g.Expect(k8sClient.Get(ctx, apitypes.NamespacedName{Name: terminatingPodName, Namespace: ns.Name}, podToCleanup)).To(gomega.Succeed())
							podToCleanup.Finalizers = nil
							g.Expect(k8sClient.Update(ctx, podToCleanup)).Should(gomega.Succeed())
							g.Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, podToCleanup))).To(gomega.Succeed())
						}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
					})
				})

				ginkgo.It("should not add unhealthy node when Ready condition is missing but late pods exist", framework.SlowSpec, func() {
					var wl *kueue.Workload
					nodeName := nodes[0].Name
					var terminatingPodName string

					ginkgo.By("creating a workload that gets admitted", func() {
						wl = utiltestingapi.MakeWorkload("wl", ns.Name).
							PodSets(*utiltestingapi.MakePodSet("worker", 2).
								PreferredTopologyRequest(utiltesting.DefaultBlockTopologyLevel).
								Obj()).
							Queue(kueue.LocalQueueName(localQueue.Name)).Request(corev1.ResourceCPU, "1").Obj()
						behavioral.MustCreate(ctx, k8sClient, wl)
					})

					ginkgo.By("verify the workload is admitted to x3 and x1", func() {
						behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl)
						gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), wl)).To(gomega.Succeed())
						gomega.Expect(wl.Status.Admission.PodSetAssignments[0].TopologyAssignment).Should(gomega.BeComparableTo(
							utiltas.V1Beta2From(&utiltas.TopologyAssignment{
								Levels: []string{corev1.LabelHostname},
								Domains: []utiltas.TopologyDomainAssignment{
									{Count: 1, Values: []string{"x3"}},
									{Count: 1, Values: []string{"x1"}},
								},
							}),
						))
					})

					createPodsForWorkload(wl, ns.Name, true, false)

					ginkgo.By("make one pod terminating on x3", func() {
						pod0 := &corev1.Pod{}
						gomega.Expect(k8sClient.Get(ctx, apitypes.NamespacedName{Name: fmt.Sprintf("%s-0", wl.Name), Namespace: ns.Name}, pod0)).Should(gomega.Succeed())
						pod1 := &corev1.Pod{}
						gomega.Expect(k8sClient.Get(ctx, apitypes.NamespacedName{Name: fmt.Sprintf("%s-1", wl.Name), Namespace: ns.Name}, pod1)).Should(gomega.Succeed())

						for _, podCopy := range []*corev1.Pod{pod0, pod1} {
							if podCopy.Spec.NodeSelector[corev1.LabelHostname] == nodeName {
								terminatingPodName = podCopy.Name
								podCopy.Finalizers = append(podCopy.Finalizers, "kueue.x-k8s.io/dummy-finalizer")
								gomega.Expect(k8sClient.Update(ctx, podCopy)).Should(gomega.Succeed())
								gomega.Expect(k8sClient.Delete(ctx, podCopy)).Should(gomega.Succeed())
							} else {
								gomega.Expect(k8sClient.Delete(ctx, podCopy)).Should(gomega.Succeed())
							}
						}
					})

					ginkgo.By("make node NotReady to trigger node replacement", func() {
						nodeToUpdate := &corev1.Node{}
						gomega.Expect(k8sClient.Get(ctx, apitypes.NamespacedName{Name: nodeName}, nodeToUpdate)).Should(gomega.Succeed())
						behavioral.SetNodeCondition(ctx, k8sClient, nodeToUpdate, &corev1.NodeCondition{
							Type:               corev1.NodeReady,
							Status:             corev1.ConditionFalse,
							LastTransitionTime: metav1.NewTime(time.Now().Add(-tas.NodeFailureDelay)),
						})
					})

					ginkgo.By("verify Hot Swap completes", func() {
						gomega.Eventually(func(g gomega.Gomega) {
							g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), wl)).To(gomega.Succeed())
							g.Expect(wl.Status.Admission.PodSetAssignments[0].TopologyAssignment).Should(gomega.BeComparableTo(
								utiltas.V1Beta2From(&utiltas.TopologyAssignment{
									Levels: []string{corev1.LabelHostname},
									Domains: []utiltas.TopologyDomainAssignment{
										{Count: 1, Values: []string{"x1"}},
										{Count: 1, Values: []string{"x4"}},
									},
								}),
							))
						}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
					})

					ginkgo.By("make node x3 Ready first to trigger reconcile on subsequent update", func() {
						nodeToUpdate := &corev1.Node{}
						gomega.Expect(k8sClient.Get(ctx, apitypes.NamespacedName{Name: nodeName}, nodeToUpdate)).Should(gomega.Succeed())
						behavioral.SetNodeCondition(ctx, k8sClient, nodeToUpdate, &corev1.NodeCondition{
							Type:               corev1.NodeReady,
							Status:             corev1.ConditionTrue,
							LastTransitionTime: metav1.NewTime(time.Now()),
						})
					})

					ginkgo.By("remove NodeReady condition completely", func() {
						nodeToUpdate := &corev1.Node{}
						gomega.Expect(k8sClient.Get(ctx, apitypes.NamespacedName{Name: nodeName}, nodeToUpdate)).Should(gomega.Succeed())
						nodeToUpdate.Status.Conditions = nil
						gomega.Expect(k8sClient.Status().Update(ctx, nodeToUpdate)).Should(gomega.Succeed())
					})

					ginkgo.By("verify unhealthy nodes is not polluted", func() {
						gomega.Consistently(func(g gomega.Gomega) {
							g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), wl)).To(gomega.Succeed())
							g.Expect(wl.Status.UnhealthyNodes).Should(gomega.BeEmpty())
						}, behavioral.ConsistentDuration, behavioral.ShortInterval).Should(gomega.Succeed())
					})

					ginkgo.By("cleanup pod finalizer", func() {
						podToCleanup := &corev1.Pod{}
						gomega.Eventually(func(g gomega.Gomega) {
							g.Expect(k8sClient.Get(ctx, apitypes.NamespacedName{Name: terminatingPodName, Namespace: ns.Name}, podToCleanup)).To(gomega.Succeed())
							podToCleanup.Finalizers = nil
							g.Expect(k8sClient.Update(ctx, podToCleanup)).Should(gomega.Succeed())
							g.Expect(client.IgnoreNotFound(k8sClient.Delete(ctx, podToCleanup))).To(gomega.Succeed())
						}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
					})
				})

				ginkgo.It("should remove node from unhealthyNodes when the node recovers", func() {
					var wl1 *kueue.Workload
					nodeName := nodes[0].Name

					ginkgo.By("creating a workload", func() {
						wl1 = utiltestingapi.MakeWorkload("wl1", ns.Name).
							PodSets(*utiltestingapi.MakePodSet("worker", 2).
								// requiring the same block makes sure that no replacement is possible
								RequiredTopologyRequest(utiltesting.DefaultBlockTopologyLevel).
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
									{Count: 1, Values: []string{"x3"}},
									{Count: 1, Values: []string{"x1"}},
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

					ginkgo.By("verify the workload eventually gets an entry in unhealthyNodes list", func() {
						gomega.Eventually(func(g gomega.Gomega) {
							g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl1), wl1)).To(gomega.Succeed())
							g.Expect(wl1.Status.UnhealthyNodes).To(gomega.ContainElement(kueue.UnhealthyNode{Name: nodeName}))
						}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
					})

					ginkgo.By("node recovers", func() {
						nodeToUpdate := &corev1.Node{}
						gomega.Expect(k8sClient.Get(ctx, apitypes.NamespacedName{Name: nodeName}, nodeToUpdate)).Should(gomega.Succeed())

						behavioral.SetNodeCondition(ctx, k8sClient, nodeToUpdate, &corev1.NodeCondition{
							Type:               corev1.NodeReady,
							Status:             corev1.ConditionTrue,
							LastTransitionTime: metav1.NewTime(time.Now()),
						})
					})

					ginkgo.By("verify the node is removed from unhealthyNodes", func() {
						gomega.Eventually(func(g gomega.Gomega) {
							g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl1), wl1)).To(gomega.Succeed())
							g.Expect(wl1.Status.UnhealthyNodes).ToNot(gomega.ContainElement(kueue.UnhealthyNode{Name: nodeName}))
						}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
					})
				})

				ginkgo.It("should remove the node from unhealthyNodes when node reappears", func() {
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
						gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl1), wl1)).To(gomega.Succeed())
						gomega.Expect(wl1.Status.Admission.PodSetAssignments[0].TopologyAssignment).Should(gomega.BeComparableTo(
							utiltas.V1Beta2From(&utiltas.TopologyAssignment{
								Levels: []string{corev1.LabelHostname},
								Domains: []utiltas.TopologyDomainAssignment{
									{Count: 1, Values: []string{"x3"}},
									{Count: 1, Values: []string{"x1"}},
								},
							}),
						))
					})

					nodeToDelete := &corev1.Node{}
					ginkgo.By("deleting the node", func() {
						gomega.Expect(k8sClient.Get(ctx, client.ObjectKey{Name: nodeName}, nodeToDelete)).Should(gomega.Succeed())
						gomega.Expect(k8sClient.Delete(ctx, nodeToDelete)).Should(gomega.Succeed())
					})

					ginkgo.By("verify the workload eventually gets the UnhealthyNodes", func() {
						gomega.Eventually(func(g gomega.Gomega) {
							g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl1), wl1)).To(gomega.Succeed())
							g.Expect(wl1.Status.UnhealthyNodes).To(gomega.ContainElement(kueue.UnhealthyNode{Name: nodeName}))
						}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
					})

					ginkgo.By("node reappears", func() {
						nodeToDelete.ResourceVersion = ""
						gomega.Expect(k8sClient.Create(ctx, nodeToDelete)).Should(gomega.Succeed())
					})

					ginkgo.By("verify the UnhealthyNodes is cleared", func() {
						gomega.Eventually(func(g gomega.Gomega) {
							g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl1), wl1)).To(gomega.Succeed())
							g.Expect(wl1.Status.UnhealthyNodes).ToNot(gomega.ContainElement(kueue.UnhealthyNode{Name: nodeName}))
						}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
					})
				})

				ginkgo.It("should mark the node by its hostname label when the label differs from the Node name", func() {
					var wl1 *kueue.Workload
					const hostname = "x1"
					var renamedNode *corev1.Node

					ginkgo.By("replacing node x1 with a Node whose name differs from its hostname label", func() {
						nodeToReplace := &corev1.Node{}
						gomega.Expect(k8sClient.Get(ctx, client.ObjectKey{Name: hostname}, nodeToReplace)).Should(gomega.Succeed())
						behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, nodeToReplace, true)
						renamedNode = testingnode.MakeNode("node-"+hostname).
							Label("node-group", "tas").
							Label(utiltesting.DefaultBlockTopologyLevel, "b1").
							Label(utiltesting.DefaultRackTopologyLevel, "r2").
							Label(corev1.LabelHostname, hostname).
							StatusAllocatable(corev1.ResourceList{
								corev1.ResourceCPU:    resource.MustParse("1"),
								corev1.ResourceMemory: resource.MustParse("1Gi"),
								corev1.ResourcePods:   resource.MustParse("10"),
							}).
							Ready().
							Obj()
						integration.CreateNodesWithStatus(ctx, k8sClient, []corev1.Node{*renamedNode})
						ginkgo.DeferCleanup(func() {
							behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, renamedNode, true)
						})
					})

					ginkgo.By("creating a workload", func() {
						wl1 = utiltestingapi.MakeWorkload("wl1", ns.Name).
							PodSets(*utiltestingapi.MakePodSet("worker", 2).
								// requiring the same block makes sure that no replacement is possible
								RequiredTopologyRequest(utiltesting.DefaultBlockTopologyLevel).
								Obj()).
							Queue(kueue.LocalQueueName(localQueue.Name)).Request(corev1.ResourceCPU, "1").Obj()
						behavioral.MustCreate(ctx, k8sClient, wl1)
					})

					ginkgo.By("verify the workload is admitted with the hostname label in its TopologyAssignment", func() {
						behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1)
						gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl1), wl1)).To(gomega.Succeed())
						gomega.Expect(wl1.Status.Admission.PodSetAssignments[0].TopologyAssignment).Should(gomega.BeComparableTo(
							utiltas.V1Beta2From(&utiltas.TopologyAssignment{
								Levels: []string{corev1.LabelHostname},
								Domains: []utiltas.TopologyDomainAssignment{
									{Count: 1, Values: []string{"x3"}},
									{Count: 1, Values: []string{hostname}},
								},
							}),
						))
					})

					ginkgo.By("making the Node NotReady 30s in the past", func() {
						behavioral.SetNodeCondition(ctx, k8sClient, renamedNode, &corev1.NodeCondition{
							Type:               corev1.NodeReady,
							Status:             corev1.ConditionFalse,
							LastTransitionTime: metav1.NewTime(time.Now().Add(-tas.NodeFailureDelay)),
						})
					})

					ginkgo.By("verify the workload records the hostname label in unhealthyNodes", func() {
						gomega.Eventually(func(g gomega.Gomega) {
							g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl1), wl1)).To(gomega.Succeed())
							g.Expect(wl1.Status.UnhealthyNodes).To(gomega.ContainElement(kueue.UnhealthyNode{Name: hostname}))
						}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
					})

					ginkgo.By("node recovers", func() {
						behavioral.SetNodeCondition(ctx, k8sClient, renamedNode, &corev1.NodeCondition{
							Type:               corev1.NodeReady,
							Status:             corev1.ConditionTrue,
							LastTransitionTime: metav1.NewTime(time.Now()),
						})
					})

					ginkgo.By("verify the node is removed from unhealthyNodes", func() {
						gomega.Eventually(func(g gomega.Gomega) {
							g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl1), wl1)).To(gomega.Succeed())
							g.Expect(wl1.Status.UnhealthyNodes).ToNot(gomega.ContainElement(kueue.UnhealthyNode{Name: hostname}))
						}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
					})
				})

				ginkgo.It("should update workload TopologyAssignment after a node becomes available", framework.SlowSpec, func() {
					features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.TASFailedNodeReplacementFailFast, false)
					var wl1, wl2 *kueue.Workload
					nodeName := nodes[0].Name

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
						gomega.Expect(wl1.Status.Admission.PodSetAssignments[0].TopologyAssignment).Should(gomega.BeComparableTo(
							utiltas.V1Beta2From(&utiltas.TopologyAssignment{
								Levels: []string{corev1.LabelHostname},
								Domains: []utiltas.TopologyDomainAssignment{
									{Count: 1, Values: []string{"x3"}},
									{Count: 1, Values: []string{"x1"}},
								},
							}),
						))
					})

					ginkgo.By("creating a second workload", func() {
						wl2 = utiltestingapi.MakeWorkload("wl2", ns.Name).
							PodSets(*utiltestingapi.MakePodSet("worker", 2).
								PreferredTopologyRequest(utiltesting.DefaultBlockTopologyLevel).
								Obj()).
							Queue(kueue.LocalQueueName(localQueue.Name)).Request(corev1.ResourceCPU, "1").Obj()
						behavioral.MustCreate(ctx, k8sClient, wl2)
					})

					ginkgo.By("verify the workload is admitted", func() {
						behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl2)
						gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl2), wl2)).To(gomega.Succeed())
						gomega.Expect(wl2.Status.Admission.PodSetAssignments[0].TopologyAssignment).Should(gomega.BeComparableTo(
							utiltas.V1Beta2From(&utiltas.TopologyAssignment{
								Levels: []string{corev1.LabelHostname},
								Domains: []utiltas.TopologyDomainAssignment{
									{Count: 1, Values: []string{"x4"}},
									{Count: 1, Values: []string{"x2"}},
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

					ginkgo.By("verify the workload has the same TopologyAssignment as there is no free capacity for replacement", func() {
						gomega.Consistently(func(g gomega.Gomega) {
							g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl1), wl1)).To(gomega.Succeed())
							g.Expect(wl1.Status.Admission.PodSetAssignments[0].TopologyAssignment).Should(gomega.BeComparableTo(
								utiltas.V1Beta2From(&utiltas.TopologyAssignment{
									Levels: []string{corev1.LabelHostname},
									Domains: []utiltas.TopologyDomainAssignment{
										{Count: 1, Values: []string{"x3"}},
										{Count: 1, Values: []string{"x1"}},
									},
								}),
							))
						}, behavioral.ConsistentDuration, behavioral.ShortInterval).Should(gomega.Succeed())
					})

					ginkgo.By("Finishing second workload")
					integration.FinishWorkloads(ctx, k8sClient, wl2)

					ginkgo.By("verify the workload has corrected TopologyAssignment and no node in UnhealthyNodes", func() {
						gomega.Eventually(func(g gomega.Gomega) {
							g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl1), wl1)).To(gomega.Succeed())
							g.Expect(wl1.Status.Admission.PodSetAssignments[0].TopologyAssignment).Should(gomega.BeComparableTo(
								utiltas.V1Beta2From(&utiltas.TopologyAssignment{
									Levels: []string{corev1.LabelHostname},
									Domains: []utiltas.TopologyDomainAssignment{
										{Count: 1, Values: []string{"x1"}},
										{Count: 1, Values: []string{"x4"}},
									},
								}),
							))
							g.Expect(wl1.Status.UnhealthyNodes).NotTo(gomega.ContainElement(kueue.UnhealthyNode{Name: nodeName}))
						}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
					})
				})

				ginkgo.It("should evict workload when multiple assigned nodes are deleted", func() {
					var wl1 *kueue.Workload
					node1Name := "x3"
					node2Name := "x1"

					ginkgo.By("creating a workload", func() {
						wl1 = utiltestingapi.MakeWorkload("wl1", ns.Name).
							PodSets(*utiltestingapi.MakePodSet("worker", 2).
								NodeSelector(map[string]string{utiltesting.DefaultBlockTopologyLevel: "b1"}).
								PreferredTopologyRequest(utiltesting.DefaultBlockTopologyLevel).
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
									{Count: 1, Values: []string{node1Name}},
									{Count: 1, Values: []string{node2Name}},
								},
							}),
						))
					})

					ginkgo.By("deleting the first assigned node: "+node1Name, func() {
						nodeToDelete := &corev1.Node{Name: node1Name}
						gomega.Expect(k8sClient.Delete(ctx, nodeToDelete)).Should(gomega.Succeed())
						behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, nodeToDelete, false)
					})

					ginkgo.By("deleting the second assigned node: "+node2Name, func() {
						nodeToDelete := &corev1.Node{Name: node2Name}
						gomega.Expect(k8sClient.Delete(ctx, nodeToDelete)).Should(gomega.Succeed())
						behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, nodeToDelete, false)
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

				ginkgo.It("should evict workload when multiple assigned nodes fail", func() {
					var wl1 *kueue.Workload
					node1Name := "x3"
					node2Name := "x1"

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
						gomega.Expect(wl1.Status.Admission.PodSetAssignments[0].TopologyAssignment).Should(gomega.BeComparableTo(
							utiltas.V1Beta2From(&utiltas.TopologyAssignment{
								Levels: []string{corev1.LabelHostname},
								Domains: []utiltas.TopologyDomainAssignment{
									{Count: 1, Values: []string{node1Name}},
									{Count: 1, Values: []string{node2Name}},
								},
							}),
						))
					})

					ginkgo.By("updating nodeReady condition of the first node", func() {
						nodeToUpdate := &corev1.Node{}
						gomega.Expect(k8sClient.Get(ctx, apitypes.NamespacedName{Name: node1Name}, nodeToUpdate)).Should(gomega.Succeed())
						behavioral.SetNodeCondition(ctx, k8sClient, nodeToUpdate, &corev1.NodeCondition{
							Type:               corev1.NodeReady,
							Status:             corev1.ConditionFalse,
							LastTransitionTime: metav1.NewTime(time.Now().Add(-tas.NodeFailureDelay)),
						})
					})
					ginkgo.By("updating nodeReady condition of the second node", func() {
						nodeToUpdate := &corev1.Node{}
						gomega.Expect(k8sClient.Get(ctx, apitypes.NamespacedName{Name: node2Name}, nodeToUpdate)).Should(gomega.Succeed())
						behavioral.SetNodeCondition(ctx, k8sClient, nodeToUpdate, &corev1.NodeCondition{
							Type:               corev1.NodeReady,
							Status:             corev1.ConditionFalse,
							LastTransitionTime: metav1.NewTime(time.Now().Add(-tas.NodeFailureDelay)),
						})
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

				ginkgo.It("should tolerate eight unhealthy nodes and evict on the ninth failure", framework.SlowSpec, func() {
					features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.TASReplaceMultipleFailedNodes, true)
					features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.TASFailedNodeReplacementFailFast, false)

					ginkgo.By("providing nine nodes and enough quota for all of them", func() {
						var additionalNodes []corev1.Node
						for _, name := range []string{"x5", "x6", "x7", "x8", "x9"} {
							additionalNodes = append(additionalNodes, *testingnode.MakeNode(name).
								Label("node-group", "tas").
								Label(utiltesting.DefaultBlockTopologyLevel, "b1").
								Label(utiltesting.DefaultRackTopologyLevel, "r1").
								Label(corev1.LabelHostname, name).
								StatusAllocatable(corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("1"),
									corev1.ResourceMemory: resource.MustParse("1Gi"),
									corev1.ResourcePods:   resource.MustParse("10"),
								}).
								Ready().Obj())
						}
						nodes = append(nodes, additionalNodes...)
						integration.CreateNodesWithStatus(ctx, k8sClient, additionalNodes)
						gomega.Eventually(func(g gomega.Gomega) {
							g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(clusterQueue), clusterQueue)).To(gomega.Succeed())
							clusterQueue.Spec.ResourceGroups[0].Flavors[0].Resources[0].NominalQuota = resource.MustParse("9")
							g.Expect(k8sClient.Update(ctx, clusterQueue)).To(gomega.Succeed())
						}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
					})

					var wl *kueue.Workload
					ginkgo.By("creating a workload that occupies all nine nodes", func() {
						wl = utiltestingapi.MakeWorkload("wl-exceed", ns.Name).
							PodSets(*utiltestingapi.MakePodSet("worker", 9).
								PreferredTopologyRequest(utiltesting.DefaultBlockTopologyLevel).
								Obj()).
							Queue(kueue.LocalQueueName(localQueue.Name)).Request(corev1.ResourceCPU, "1").Obj()
						behavioral.MustCreate(ctx, k8sClient, wl)
					})

					ginkgo.By("verifying admission across all nine nodes", func() {
						behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl)
						gomega.Eventually(func(g gomega.Gomega) {
							g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), wl)).To(gomega.Succeed())
							ta := wl.Status.Admission.PodSetAssignments[0].TopologyAssignment
							g.Expect(ta).NotTo(gomega.BeNil())
							g.Expect(slices.Collect(utiltas.LowestLevelValues(ta))).To(gomega.ConsistOf("x1", "x2", "x3", "x4", "x5", "x6", "x7", "x8", "x9"))
						}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
					})

					ginkgo.By("queuing eight failed nodes without eviction", func() {
						var failedNodes []string
						for _, name := range []string{"x1", "x2", "x3", "x4", "x5", "x6", "x7", "x8"} {
							behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, &corev1.Node{Name: name}, true)
							failedNodes = append(failedNodes, name)
							behavioral.ExpectAdmittedWorkloadWithUnhealthyNodes(ctx, k8sClient, wl, failedNodes...)
						}
					})

					ginkgo.By("evicting on the ninth failure", func() {
						behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, &corev1.Node{Name: "x9"}, true)
						behavioral.ExpectWorkloadsToBeEvictedByKeys(ctx, k8sClient, client.ObjectKeyFromObject(wl))
						behavioral.FinishEvictionForWorkloads(ctx, k8sClient, wl)
						gomega.Eventually(func(g gomega.Gomega) {
							updatedWl := &kueue.Workload{}
							g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), updatedWl)).To(gomega.Succeed())
							g.Expect(updatedWl.Status.UnhealthyNodes).To(gomega.BeEmpty(),
								"UnhealthyNodes should be cleared after eviction")
						}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
					})
				})

				ginkgo.It("should evict on a second node failure when multiple-node replacement is disabled", framework.SlowSpec, func() {
					features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.TASReplaceMultipleFailedNodes, false)
					features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.TASFailedNodeReplacementFailFast, false)

					var wl1 *kueue.Workload
					node1Name := "x3" // b1/r1
					node2Name := "x1" // b1/r2

					ginkgo.By("making replacement nodes unavailable", func() {
						for _, name := range []string{"x4", "x2"} {
							nodeToDelete := &corev1.Node{Name: name}
							behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, nodeToDelete, true)
						}
					})

					ginkgo.By("creating a workload", func() {
						wl1 = utiltestingapi.MakeWorkload("wl-evict", ns.Name).
							PodSets(*utiltestingapi.MakePodSet("worker", 2).
								PreferredTopologyRequest(utiltesting.DefaultBlockTopologyLevel).
								Obj()).
							Queue(kueue.LocalQueueName(localQueue.Name)).Request(corev1.ResourceCPU, "1").Obj()
						behavioral.MustCreate(ctx, k8sClient, wl1)
					})

					ginkgo.By("verify the workload is admitted to block b1", func() {
						behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1)
						gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl1), wl1)).To(gomega.Succeed())
						gomega.Expect(wl1.Status.Admission.PodSetAssignments[0].TopologyAssignment).Should(gomega.BeComparableTo(
							utiltas.V1Beta2From(&utiltas.TopologyAssignment{
								Levels: []string{corev1.LabelHostname},
								Domains: []utiltas.TopologyDomainAssignment{
									{Count: 1, Values: []string{node1Name}},
									{Count: 1, Values: []string{node2Name}},
								},
							}),
						))
					})

					ginkgo.By("failing the first assigned node", func() {
						nodeToUpdate := &corev1.Node{}
						gomega.Expect(k8sClient.Get(ctx, apitypes.NamespacedName{Name: node1Name}, nodeToUpdate)).Should(gomega.Succeed())
						behavioral.SetNodeCondition(ctx, k8sClient, nodeToUpdate, &corev1.NodeCondition{
							Type:               corev1.NodeReady,
							Status:             corev1.ConditionFalse,
							LastTransitionTime: metav1.NewTime(time.Now().Add(-tas.NodeFailureDelay)),
						})
					})
					ginkgo.By("verify one failed node remains tolerated", func() {
						behavioral.ExpectAdmittedWorkloadWithUnhealthyNodes(ctx, k8sClient, wl1, node1Name)
					})

					ginkgo.By("failing the second assigned node", func() {
						nodeToUpdate := &corev1.Node{}
						gomega.Expect(k8sClient.Get(ctx, apitypes.NamespacedName{Name: node2Name}, nodeToUpdate)).Should(gomega.Succeed())
						behavioral.SetNodeCondition(ctx, k8sClient, nodeToUpdate, &corev1.NodeCondition{
							Type:               corev1.NodeReady,
							Status:             corev1.ConditionFalse,
							LastTransitionTime: metav1.NewTime(time.Now().Add(-tas.NodeFailureDelay)),
						})
					})

					ginkgo.By("verify the workload is evicted due to multiple node failures", func() {
						behavioral.FinishEvictionForWorkloads(ctx, k8sClient, wl1)
						gomega.Eventually(func(g gomega.Gomega) {
							updatedWl := &kueue.Workload{}
							g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl1), updatedWl)).To(gomega.Succeed())
							g.Expect(updatedWl.Status.UnhealthyNodes).To(gomega.BeEmpty(),
								"UnhealthyNodes should be cleared after eviction due to multiple node failures")
						}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
					})
				})

				ginkgo.When("replacing multiple failed nodes for a required topology", func() {
					ginkgo.It("should preserve the rack of a surviving node after two earlier nodes fail", framework.SlowSpec, func() {
						features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.TASReplaceMultipleFailedNodes, true)
						features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.TASFailedNodeReplacementFailFast, false)
						var additionalNodes []corev1.Node
						for _, name := range []string{"x5", "x6", "x7", "x8"} {
							additionalNodes = append(additionalNodes, *testingnode.MakeNode(name).
								Label("node-group", "tas").
								Label(utiltesting.DefaultBlockTopologyLevel, "b1").
								Label(utiltesting.DefaultRackTopologyLevel, "r1").
								Label(corev1.LabelHostname, name).
								StatusAllocatable(corev1.ResourceList{
									corev1.ResourceCPU:    resource.MustParse("1"),
									corev1.ResourceMemory: resource.MustParse("1Gi"),
									corev1.ResourcePods:   resource.MustParse("10"),
								}).
								Ready().Obj())
						}

						ginkgo.By("providing exactly three nodes in rack b1/r1", func() {
							nodes = append(nodes, additionalNodes...)
							integration.CreateNodesWithStatus(ctx, k8sClient, additionalNodes[:2])
						})

						wl := utiltestingapi.MakeWorkload("wl-required-survivor", ns.Name).
							PodSets(*utiltestingapi.MakePodSet("worker", 3).
								RequiredTopologyRequest(utiltesting.DefaultRackTopologyLevel).Obj()).
							Queue(kueue.LocalQueueName(localQueue.Name)).Request(corev1.ResourceCPU, "1").Obj()
						originalAssignment := utiltas.V1Beta2From(&utiltas.TopologyAssignment{
							Levels: []string{corev1.LabelHostname},
							Domains: []utiltas.TopologyDomainAssignment{
								{Count: 1, Values: []string{"x3"}},
								{Count: 1, Values: []string{"x5"}},
								{Count: 1, Values: []string{"x6"}},
							},
						})
						ginkgo.By("admitting all three pods into that rack", func() {
							behavioral.MustCreate(ctx, k8sClient, wl)
							behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl)
							gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), wl)).To(gomega.Succeed())
							gomega.Expect(wl.Status.Admission.PodSetAssignments[0].TopologyAssignment).To(gomega.BeComparableTo(originalAssignment))
						})

						ginkgo.By("deleting two assigned nodes while x6 survives", func() {
							behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, &corev1.Node{Name: "x3"}, true)
							behavioral.ExpectAdmittedWorkloadWithUnhealthyNodes(ctx, k8sClient, wl, "x3")
							behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, &corev1.Node{Name: "x5"}, true)
							behavioral.ExpectAdmittedWorkloadWithUnhealthyNodes(ctx, k8sClient, wl, "x3", "x5")
						})

						ginkgo.By("waiting rather than using the free nodes in other racks", func() {
							gomega.Consistently(func(g gomega.Gomega) {
								updatedWl := &kueue.Workload{}
								g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), updatedWl)).To(gomega.Succeed())
								g.Expect(workload.IsAdmitted(updatedWl)).To(gomega.BeTrue())
								g.Expect(updatedWl.Status.Admission.PodSetAssignments[0].TopologyAssignment).To(gomega.BeComparableTo(originalAssignment))
							}, 3*behavioral.ConsistentDuration, behavioral.ShortInterval).Should(gomega.Succeed())
						})

						ginkgo.By("adding replacement capacity in the surviving node's rack", func() {
							integration.CreateNodesWithStatus(ctx, k8sClient, additionalNodes[2:])
						})

						ginkgo.By("replacing both failures without moving the surviving pod", func() {
							gomega.Eventually(func(g gomega.Gomega) {
								updatedWl := &kueue.Workload{}
								g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), updatedWl)).To(gomega.Succeed())
								g.Expect(workload.IsAdmitted(updatedWl)).To(gomega.BeTrue())
								g.Expect(updatedWl.Status.UnhealthyNodes).To(gomega.BeEmpty())
								ta := updatedWl.Status.Admission.PodSetAssignments[0].TopologyAssignment
								g.Expect(slices.Collect(utiltas.LowestLevelValues(ta))).To(gomega.ConsistOf("x6", "x7", "x8"))
							}, behavioral.LongTimeout, behavioral.Interval).Should(gomega.Succeed())
						})
					})

					ginkgo.It("should preserve the assignment when the required domain cannot be determined", framework.SlowSpec, func() {
						features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.TASReplaceMultipleFailedNodes, true)
						features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.TASFailedNodeReplacementFailFast, false)

						var wl *kueue.Workload
						originalAssignment := utiltas.V1Beta2From(&utiltas.TopologyAssignment{
							Levels: []string{corev1.LabelHostname},
							Domains: []utiltas.TopologyDomainAssignment{
								{Count: 1, Values: []string{"x3"}},
								{Count: 1, Values: []string{"x1"}},
							},
						})

						ginkgo.By("creating a two-pod workload requiring one block", func() {
							wl = utiltestingapi.MakeWorkload("wl-required-unknown-domain", ns.Name).
								PodSets(*utiltestingapi.MakePodSet("worker", 2).
									RequiredTopologyRequest(utiltesting.DefaultBlockTopologyLevel).
									Obj()).
								Queue(kueue.LocalQueueName(localQueue.Name)).Request(corev1.ResourceCPU, "1").Obj()
							behavioral.MustCreate(ctx, k8sClient, wl)
						})

						ginkgo.By("verifying the workload is admitted to block b1", func() {
							behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl)
							gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), wl)).To(gomega.Succeed())
							gomega.Expect(wl.Status.Admission.PodSetAssignments[0].TopologyAssignment).To(gomega.BeComparableTo(originalAssignment))
						})

						ginkgo.By("removing all replacement capacity", func() {
							for _, name := range []string{"x4", "x2"} {
								nodeToDelete := &corev1.Node{Name: name}
								behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, nodeToDelete, true)
							}
						})

						failNode := func(name string) {
							ginkgo.GinkgoHelper()
							nodeToUpdate := &corev1.Node{}
							gomega.Expect(k8sClient.Get(ctx, apitypes.NamespacedName{Name: name}, nodeToUpdate)).To(gomega.Succeed())
							behavioral.SetNodeCondition(ctx, k8sClient, nodeToUpdate, &corev1.NodeCondition{
								Type:               corev1.NodeReady,
								Status:             corev1.ConditionFalse,
								LastTransitionTime: metav1.NewTime(time.Now().Add(-tas.NodeFailureDelay)),
							})
						}

						ginkgo.By("queuing both failed nodes in order", func() {
							failNode("x3")
							behavioral.ExpectAdmittedWorkloadWithUnhealthyNodes(ctx, k8sClient, wl, "x3")
							failNode("x1")
							behavioral.ExpectAdmittedWorkloadWithUnhealthyNodes(ctx, k8sClient, wl, "x3", "x1")
						})

						ginkgo.By("providing replacement capacity outside the original block", func() {
							integration.CreateNodesWithStatus(ctx, k8sClient, []corev1.Node{nodes[2], nodes[3]})
						})

						ginkgo.By("observing a failed replacement because the required domain is unknown", func() {
							behavioral.ExpectEventAppeared(ctx, k8sClient, eventsv1.Event{
								Reason: "SecondPassFailed",
								Type:   corev1.EventTypeWarning,
								Note:   "couldn't assign flavors to pod set worker: cannot replace the node: required topology domain of the remaining assignment cannot be determined",
							})
						})

						ginkgo.By("keeping the original assignment instead of replacing nodes across blocks", func() {
							gomega.Consistently(func(g gomega.Gomega) {
								updatedWl := &kueue.Workload{}
								g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), updatedWl)).To(gomega.Succeed())
								g.Expect(workload.IsAdmitted(updatedWl)).To(gomega.BeTrue())
								g.Expect(updatedWl.Status.UnhealthyNodes).To(gomega.Equal([]kueue.UnhealthyNode{{Name: "x3"}, {Name: "x1"}}))
								g.Expect(updatedWl.Status.Admission.PodSetAssignments[0].TopologyAssignment).To(gomega.BeComparableTo(originalAssignment))
							}, 3*behavioral.ConsistentDuration, behavioral.ShortInterval).Should(gomega.Succeed())
						})
					})

					ginkgo.It("should expose the greedy limitation", framework.SlowSpec, func() {
						features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.TASReplaceMultipleFailedNodes, true)
						features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.TASFailedNodeReplacementFailFast, false)

						var wl, proofWl *kueue.Workload
						originalAssignment := utiltas.V1Beta2From(&utiltas.TopologyAssignment{
							Levels: []string{corev1.LabelHostname},
							Domains: []utiltas.TopologyDomainAssignment{
								{Count: 1, Values: []string{"x3"}},
								{Count: 1, Values: []string{"x1"}},
							},
						})

						ginkgo.By("creating a two-pod workload requiring one block", func() {
							wl = utiltestingapi.MakeWorkload("wl-required-limitation", ns.Name).
								PodSets(*utiltestingapi.MakePodSet("worker", 2).
									RequiredTopologyRequest(utiltesting.DefaultBlockTopologyLevel).
									Obj()).
								Queue(kueue.LocalQueueName(localQueue.Name)).Request(corev1.ResourceCPU, "1").Obj()
							behavioral.MustCreate(ctx, k8sClient, wl)
						})

						ginkgo.By("verifying the workload is admitted to block b1", func() {
							behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl)
							gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), wl)).To(gomega.Succeed())
							gomega.Expect(wl.Status.Admission.PodSetAssignments[0].TopologyAssignment).To(gomega.BeComparableTo(originalAssignment))
						})

						ginkgo.By("failing one node while block b2 remains free", func() {
							nodeToUpdate := &corev1.Node{}
							gomega.Expect(k8sClient.Get(ctx, apitypes.NamespacedName{Name: "x3"}, nodeToUpdate)).To(gomega.Succeed())
							behavioral.SetNodeCondition(ctx, k8sClient, nodeToUpdate, &corev1.NodeCondition{
								Type:               corev1.NodeReady,
								Status:             corev1.ConditionFalse,
								LastTransitionTime: metav1.NewTime(time.Now().Add(-tas.NodeFailureDelay)),
							})
							behavioral.ExpectAdmittedWorkloadWithUnhealthyNodes(ctx, k8sClient, wl, "x3")
						})

						ginkgo.By("verifying greedy replacement remains pinned to block b1", func() {
							gomega.Consistently(func(g gomega.Gomega) {
								updatedWl := &kueue.Workload{}
								g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), updatedWl)).To(gomega.Succeed())
								g.Expect(updatedWl.Status.Admission.PodSetAssignments[0].TopologyAssignment).To(gomega.BeComparableTo(originalAssignment))
								g.Expect(updatedWl.Status.UnhealthyNodes).To(gomega.Equal([]kueue.UnhealthyNode{{Name: "x3"}}))
							}, 3*behavioral.ConsistentDuration, behavioral.ShortInterval).Should(gomega.Succeed())
						})

						ginkgo.By("proving the entire PodSet fits atomically in block b2", func() {
							proofWl = utiltestingapi.MakeWorkload("wl-required-proof", ns.Name).
								PodSets(*utiltestingapi.MakePodSet("worker", 2).
									RequiredTopologyRequest(utiltesting.DefaultBlockTopologyLevel).
									Obj()).
								Queue(kueue.LocalQueueName(localQueue.Name)).Request(corev1.ResourceCPU, "1").Obj()
							behavioral.MustCreate(ctx, k8sClient, proofWl)
							behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, proofWl)
							gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(proofWl), proofWl)).To(gomega.Succeed())
							ta := proofWl.Status.Admission.PodSetAssignments[0].TopologyAssignment
							gomega.Expect(slices.Collect(utiltas.LowestLevelValues(ta))).To(gomega.ConsistOf("x4", "x2"))
						})

						ginkgo.By("freeing block b2 and verifying greedy replacement still does not move the healthy member", func() {
							integration.FinishWorkloads(ctx, k8sClient, proofWl)
							gomega.Consistently(func(g gomega.Gomega) {
								updatedWl := &kueue.Workload{}
								g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), updatedWl)).To(gomega.Succeed())
								g.Expect(updatedWl.Status.Admission.PodSetAssignments[0].TopologyAssignment).To(gomega.BeComparableTo(originalAssignment))
								g.Expect(updatedWl.Status.UnhealthyNodes).To(gomega.Equal([]kueue.UnhealthyNode{{Name: "x3"}}))
							}, 3*behavioral.ConsistentDuration, behavioral.ShortInterval).Should(gomega.Succeed())
						})
					})
				})

				ginkgo.It("should keep the TopologyAssignment pod count consistent with the PodSet count across multiple node replacements", framework.SlowSpec, func() {
					// Invariant check for multi-node replacement: after several
					// failed nodes are replaced incrementally, the workload must
					// still describe exactly PodSet.Count pods, one per node, with no
					// node double-booked and no inflated total domain count.
					features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.TASReplaceMultipleFailedNodes, true)
					features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.TASFailedNodeReplacementFailFast, false)

					var wl1 *kueue.Workload
					const podCount = 2
					node1Name := "x3" // b1/r1
					node2Name := "x1" // b1/r2

					ginkgo.By("making replacement nodes unavailable", func() {
						for _, name := range []string{"x4", "x2"} {
							nodeToDelete := &corev1.Node{Name: name}
							behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, nodeToDelete, true)
						}
					})

					ginkgo.By("creating a workload", func() {
						wl1 = utiltestingapi.MakeWorkload("wl-count", ns.Name).
							PodSets(*utiltestingapi.MakePodSet("worker", podCount).
								PreferredTopologyRequest(utiltesting.DefaultBlockTopologyLevel).
								Obj()).
							Queue(kueue.LocalQueueName(localQueue.Name)).Request(corev1.ResourceCPU, "1").Obj()
						behavioral.MustCreate(ctx, k8sClient, wl1)
					})

					ginkgo.By("verify the workload is admitted to block b1", func() {
						behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1)
						gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl1), wl1)).To(gomega.Succeed())
						gomega.Expect(wl1.Status.Admission.PodSetAssignments[0].TopologyAssignment).Should(gomega.BeComparableTo(
							utiltas.V1Beta2From(&utiltas.TopologyAssignment{
								Levels: []string{corev1.LabelHostname},
								Domains: []utiltas.TopologyDomainAssignment{
									{Count: 1, Values: []string{node1Name}},
									{Count: 1, Values: []string{node2Name}},
								},
							}),
						))
					})

					ginkgo.By("deleting the first assigned node", func() {
						nodeToDelete := &corev1.Node{Name: node1Name}
						behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, nodeToDelete, true)
					})
					ginkgo.By("verify the first failed node is queued", func() {
						behavioral.ExpectAdmittedWorkloadWithUnhealthyNodes(ctx, k8sClient, wl1, node1Name)
					})

					ginkgo.By("deleting the second assigned node", func() {
						nodeToDelete := &corev1.Node{Name: node2Name}
						gomega.Expect(k8sClient.Delete(ctx, nodeToDelete)).Should(gomega.Succeed())
						behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, nodeToDelete, false)
					})
					ginkgo.By("verify both failed nodes are queued in failure order", func() {
						behavioral.ExpectAdmittedWorkloadWithUnhealthyNodes(ctx, k8sClient, wl1, node1Name, node2Name)
					})

					ginkgo.By("making replacement nodes available", func() {
						integration.CreateNodesWithStatus(ctx, k8sClient, []corev1.Node{nodes[2], nodes[3]})
					})

					ginkgo.By("verify the TopologyAssignment pod count stays equal to the PodSet count, one pod per node", func() {
						gomega.Eventually(func(g gomega.Gomega) {
							updatedWl := &kueue.Workload{}
							g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl1), updatedWl)).To(gomega.Succeed())
							g.Expect(workload.IsAdmitted(updatedWl)).To(gomega.BeTrue())
							g.Expect(updatedWl.Status.UnhealthyNodes).Should(gomega.BeEmpty(),
								"replacement must finish before checking the count invariant")

							psa := updatedWl.Status.Admission.PodSetAssignments[0]
							ta := psa.TopologyAssignment
							g.Expect(ta).NotTo(gomega.BeNil())

							// Invariant: sum of per-domain pod counts == PodSet count.
							totalPods := 0
							nodes := sets.New[string]()
							for d := range utiltas.InternalSeqFrom(ta) {
								totalPods += int(d.Count)
								g.Expect(d.Count).To(gomega.Equal(int32(1)),
									"no node may host more than one pod for this workload")
								nodes.Insert(d.Values[len(d.Values)-1])
							}
							g.Expect(totalPods).To(gomega.Equal(int(ptr.Deref(psa.Count, 0))),
								"TopologyAssignment pod count must equal the PodSet count")
							g.Expect(totalPods).To(gomega.Equal(podCount))
							g.Expect(nodes.Len()).To(gomega.Equal(podCount),
								"each pod must be on a distinct node (no double-booking)")
						}, behavioral.LongTimeout, behavioral.Interval).Should(gomega.Succeed())
					})
				})
				// Fixes https://github.com/kubernetes-sigs/kueue/issues/9210
				ginkgo.It("should fallback to greedy assignment when replacement pod conflicts with already running pod", framework.SlowSpec, func() {
					// Scenario: This test simulates an edge case during node replacement where a running pod (p1, rank 1)
					// is occupying a node (x3) that is assigned to a different rank (rank 0) in the TopologyAssignment.
					// This can happen in practice if a terminating pod is deleted very quickly during a node hotswap,
					// causing the replacement pod to receive NodeSelectors for the occupied node based on its job labels.
					// To prevent assigning multiple pods to the same node, the ungater detects this rank mismatch
					// between the running pod and the topology assignment, and falls back to greedy assignment.
					// This ensures the new pod (p0) is safely placed on the remaining available node (x1).
					var wl1 *kueue.Workload

					ginkgo.By("creating a workload", func() {
						wl1 = utiltestingapi.MakeWorkload("wl-greedy", ns.Name).
							PodSets(*utiltestingapi.MakePodSet("worker", 2).
								PodIndexLabel(new(batchv1.JobCompletionIndexAnnotation)).
								RequiredTopologyRequest(utiltesting.DefaultBlockTopologyLevel).
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
									{Count: 1, Values: []string{"x3"}},
									{Count: 1, Values: []string{"x1"}},
								},
							}),
						))
					})

					var p1 *corev1.Pod
					ginkgo.By("creating p1 manually assigned to x3 (assigned to rank 0) but given rank 1 to simulate mismatch", func() {
						p1 = testingpod.MakePod("p1-running", ns.Name).
							Annotation(kueue.WorkloadAnnotation, wl1.Name).
							Annotation(kueue.PodSetRequiredTopologyAnnotation, utiltesting.DefaultBlockTopologyLevel).
							Label(batchv1.JobCompletionIndexAnnotation, "1"). // rank 1
							Label(constants.PodSetLabel, "worker").
							NodeSelector(corev1.LabelHostname, "x3"). // assigned to rank 0, but this is rank 1
							Obj()
						behavioral.MustCreate(ctx, k8sClient, p1)
					})

					var p0 *corev1.Pod
					ginkgo.By("creating replacement p0", func() {
						p0 = testingpod.MakePod("p0-replacement", ns.Name).
							Annotation(kueue.WorkloadAnnotation, wl1.Name).
							Annotation(kueue.PodSetRequiredTopologyAnnotation, utiltesting.DefaultBlockTopologyLevel).
							Label(batchv1.JobCompletionIndexAnnotation, "0"). // rank 0
							Label(constants.PodSetLabel, "worker").
							TopologySchedulingGate().
							Obj()
						behavioral.MustCreate(ctx, k8sClient, p0)
					})

					ginkgo.By("verify p0 is ungated and assigned to a node other than x3 since x3 is occupied", func() {
						gomega.Eventually(func(g gomega.Gomega) {
							pod := &corev1.Pod{}
							g.Expect(k8sClient.Get(ctx, client.ObjectKey{Namespace: ns.Name, Name: "p0-replacement"}, pod)).To(gomega.Succeed())
							g.Expect(pod.Spec.SchedulingGates).To(gomega.BeEmpty())
							g.Expect(pod.Spec.NodeSelector).Should(gomega.HaveKey(corev1.LabelHostname))
							g.Expect(pod.Spec.NodeSelector[corev1.LabelHostname]).To(gomega.Equal("x1"))
						}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

						behavioral.ExpectPodSchedulingGateRemovalSecondsMetricLessOrEqual(kueue.TopologySchedulingGate, kueue.ClusterQueueReference(clusterQueue.Name), false, 1)
					})
				})

				ginkgo.It("should ungate the pod at completion index 2 into the domain freed by the succeeded pod at index 0", func() {
					// An Indexed Job with completions above parallelism creates the pod at
					// completion index 2 once the pod at index 0 succeeds.
					var wl *kueue.Workload
					expectUngated := func(pod *corev1.Pod) {
						gomega.Eventually(func(g gomega.Gomega) {
							g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(pod), pod)).To(gomega.Succeed())
							g.Expect(pod.Spec.SchedulingGates).To(gomega.BeEmpty())
							g.Expect(pod.Spec.NodeSelector).Should(gomega.HaveKey(corev1.LabelHostname))
						}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
					}

					ginkgo.By("creating and admitting a workload ranked by the completion index", func() {
						wl = utiltestingapi.MakeWorkload("wl-rotation", ns.Name).
							PodSets(*utiltestingapi.MakePodSet("worker", 2).
								PodIndexLabel(new(batchv1.JobCompletionIndexAnnotation)).
								RequiredTopologyRequest(utiltesting.DefaultBlockTopologyLevel).
								Obj()).
							Queue(kueue.LocalQueueName(localQueue.Name)).Request(corev1.ResourceCPU, "1").Obj()
						behavioral.MustCreate(ctx, k8sClient, wl)
						behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl)
					})

					gatedPod := testingpod.MakePod("", ns.Name).
						Annotation(kueue.WorkloadAnnotation, wl.Name).
						Annotation(kueue.PodSetRequiredTopologyAnnotation, utiltesting.DefaultBlockTopologyLevel).
						Label(constants.PodSetLabel, "worker").
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
			})
		})
	})
})
