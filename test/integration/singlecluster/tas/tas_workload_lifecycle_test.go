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
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	apitypes "k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/tas"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingnode "sigs.k8s.io/kueue/pkg/util/testingjobs/node"
	"sigs.k8s.io/kueue/pkg/workload"
	workloadpatching "sigs.k8s.io/kueue/pkg/workload/patching"
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
		ginkgo.When("Admitted workload with UnhealthyNodes is evicted", func() {
			var (
				nodes        []corev1.Node
				ac           *kueue.AdmissionCheck
				topology     *kueue.Topology
				tasFlavor    *kueue.ResourceFlavor
				clusterQueue *kueue.ClusterQueue
				localQueue   *kueue.LocalQueue
			)

			ginkgo.BeforeEach(func() {
				topology = utiltestingapi.MakeDefaultThreeLevelTopology("default")
				behavioral.MustCreate(ctx, k8sClient, topology)

				tasFlavor = utiltestingapi.MakeResourceFlavor("tas-flavor").
					NodeLabel("node-group", "tas").
					TopologyName(topology.Name).
					Obj()
				behavioral.MustCreate(ctx, k8sClient, tasFlavor)

				ac = utiltestingapi.MakeAdmissionCheck("test-ac").
					ControllerName("test-ac-controller").
					Obj()
				behavioral.MustCreate(ctx, k8sClient, ac)
				behavioral.SetAdmissionCheckActive(ctx, k8sClient, ac, metav1.ConditionTrue)

				clusterQueue = utiltestingapi.MakeClusterQueue("cluster-queue").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas(tasFlavor.Name).Resource(corev1.ResourceCPU, "5").Obj()).
					AdmissionChecks(kueue.AdmissionCheckReference(ac.Name)).
					Obj()
				behavioral.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, clusterQueue)

				localQueue = utiltestingapi.MakeLocalQueue("local-queue", ns.Name).ClusterQueue(clusterQueue.Name).Obj()
				behavioral.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, localQueue)

				nodes = []corev1.Node{
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
					*testingnode.MakeNode("x2").
						Label("node-group", "tas").
						Label(utiltesting.DefaultBlockTopologyLevel, "b1").
						Label(utiltesting.DefaultRackTopologyLevel, "r1").
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
			})

			ginkgo.AfterEach(func() {
				gomega.Expect(behavioral.DeleteAllJobsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
				gomega.Expect(behavioral.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
				gomega.Expect(behavioral.DeleteObject(ctx, k8sClient, localQueue)).Should(gomega.Succeed())
				behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
				behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, tasFlavor, true)
				behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, topology, true)
				behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, ac, true)
				for _, node := range nodes {
					behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, &node, true)
				}
			})

			ginkgo.It("should evict workload when AdmissionCheck is in Retry state", func() {
				var wl *kueue.Workload

				ginkgo.By("creating a workload", func() {
					wl = utiltestingapi.MakeWorkload("wl1", ns.Name).
						PodSets(*utiltestingapi.MakePodSet("worker", 1).
							PreferredTopologyRequest(corev1.LabelHostname).
							Obj()).
						Queue(kueue.LocalQueueName(localQueue.Name)).Request(corev1.ResourceCPU, "1").Obj()
					behavioral.MustCreate(ctx, k8sClient, wl)
				})

				wlKey := client.ObjectKeyFromObject(wl)

				ginkgo.By("setting the admission check to Ready so the workload gets admitted", func() {
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, wlKey, wl)).To(gomega.Succeed())
						workloadpatching.SetAdmissionCheckState(&wl.Status.AdmissionChecks, kueue.AdmissionCheckState{
							Name:  kueue.AdmissionCheckReference(ac.Name),
							State: kueue.CheckStateReady,
						}, behavioral.RealClock)
						g.Expect(k8sClient.Status().Update(ctx, wl)).Should(gomega.Succeed())
					}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
				})

				ginkgo.By("verify the workload is admitted", func() {
					behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl)
				})

				nodeName := nodes[0].Name
				ginkgo.By("making the node NotReady to trigger UnhealthyNodes", func() {
					nodeToUpdate := &corev1.Node{}
					gomega.Expect(k8sClient.Get(ctx, apitypes.NamespacedName{Name: nodeName}, nodeToUpdate)).Should(gomega.Succeed())

					behavioral.SetNodeCondition(ctx, k8sClient, nodeToUpdate, &corev1.NodeCondition{
						Type:               corev1.NodeReady,
						Status:             corev1.ConditionFalse,
						LastTransitionTime: metav1.NewTime(time.Now().Add(-tas.NodeFailureDelay)),
					})
				})

				ginkgo.By("verify the workload gets UnhealthyNodes", func() {
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, wlKey, wl)).To(gomega.Succeed())
						g.Expect(wl.Status.UnhealthyNodes).To(gomega.ContainElement(kueue.UnhealthyNode{Name: nodeName}))
					}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
				})

				ginkgo.By("setting the admission check to Retry", func() {
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, wlKey, wl)).To(gomega.Succeed())
						workloadpatching.SetAdmissionCheckState(&wl.Status.AdmissionChecks, kueue.AdmissionCheckState{
							Name:    kueue.AdmissionCheckReference(ac.Name),
							State:   kueue.CheckStateRetry,
							Message: "check needs retry",
						}, behavioral.RealClock)
						g.Expect(k8sClient.Status().Update(ctx, wl)).Should(gomega.Succeed())
					}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
				})

				ginkgo.By("verify the workload is evicted", func() {
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, wlKey, wl)).To(gomega.Succeed())
						g.Expect(wl.Status.Conditions).Should(utiltesting.HaveConditionStatusTrueAndReason(kueue.WorkloadEvicted, kueue.WorkloadEvictedByAdmissionCheck))
					}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
				})
			})
		})

		ginkgo.When("Multi-podset workload releases topology when one podset finishes", func() {
			var (
				tasFlavor    *kueue.ResourceFlavor
				topology     *kueue.Topology
				localQueue   *kueue.LocalQueue
				clusterQueue *kueue.ClusterQueue
				nodes        []corev1.Node
			)

			ginkgo.BeforeEach(func() {
				nodes = []corev1.Node{
					*testingnode.MakeNode("node-a").
						Label("node-group", "tas").
						Label(corev1.LabelHostname, "node-a").
						StatusAllocatable(corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("1"),
							corev1.ResourceMemory: resource.MustParse("1Gi"),
							corev1.ResourcePods:   resource.MustParse("10"),
						}).
						Ready().
						Obj(),
					*testingnode.MakeNode("node-b").
						Label("node-group", "tas").
						Label(corev1.LabelHostname, "node-b").
						StatusAllocatable(corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("1"),
							corev1.ResourceMemory: resource.MustParse("1Gi"),
							corev1.ResourcePods:   resource.MustParse("10"),
						}).
						Ready().
						Obj(),
				}
				integration.CreateNodesWithStatus(ctx, k8sClient, nodes)

				topology = utiltestingapi.MakeDefaultOneLevelTopology("default")
				behavioral.MustCreate(ctx, k8sClient, topology)

				tasFlavor = utiltestingapi.MakeResourceFlavor("tas-flavor").
					NodeLabel("node-group", "tas").
					TopologyName("default").
					Obj()
				behavioral.MustCreate(ctx, k8sClient, tasFlavor)

				clusterQueue = utiltestingapi.MakeClusterQueue("cluster-queue").
					ResourceGroup(
						*utiltestingapi.MakeFlavorQuotas(tasFlavor.Name).
							Resource(corev1.ResourceCPU, "3").
							Resource(corev1.ResourceMemory, "3Gi").
							Obj(),
					).
					Obj()
				behavioral.MustCreate(ctx, k8sClient, clusterQueue)
				behavioral.ExpectClusterQueuesToBeActive(ctx, k8sClient, clusterQueue)

				localQueue = utiltestingapi.MakeLocalQueue("local-queue", ns.Name).ClusterQueue(clusterQueue.Name).Obj()
				behavioral.MustCreate(ctx, k8sClient, localQueue)
			})

			ginkgo.AfterEach(func() {
				gomega.Expect(behavioral.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
				gomega.Expect(behavioral.DeleteObject(ctx, k8sClient, localQueue)).Should(gomega.Succeed())
				behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
				behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, tasFlavor, true)
				behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, topology, true)
				for _, node := range nodes {
					behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, &node, true)
				}
			})

			ginkgo.It("should admit the pending workload after one podset of the admitted workload finishes and releases its topology domain", func() {
				var (
					admittedWl *kueue.Workload
					pendingWl  *kueue.Workload
					freedNode  string
				)

				ginkgo.By("creating and admitting a workload with two podsets occupying all topology domains", func() {
					admittedWl = utiltestingapi.MakeWorkload("admitted-wl", ns.Name).
						Queue(kueue.LocalQueueName(localQueue.Name)).
						PodSets(
							*utiltestingapi.MakePodSet("driver", 1).
								RequiredTopologyRequest(corev1.LabelHostname).
								Request(corev1.ResourceCPU, "1").
								Request(corev1.ResourceMemory, "1Gi").
								Obj(),
							*utiltestingapi.MakePodSet("workers", 1).
								RequiredTopologyRequest(corev1.LabelHostname).
								Request(corev1.ResourceCPU, "1").
								Request(corev1.ResourceMemory, "1Gi").
								Obj(),
						).
						Obj()
					behavioral.MustCreate(ctx, k8sClient, admittedWl)
					behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, admittedWl)
				})

				ginkgo.By("determining which topology domain was assigned to the workers podset", func() {
					gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(admittedWl), admittedWl)).To(gomega.Succeed())
					workersTA := topologyAssignmentByName(gomega.Default, admittedWl, "workers")
					gomega.Expect(workersTA.Domains).To(gomega.HaveLen(1))
					freedNode = workersTA.Domains[0].Values[0]
				})

				ginkgo.By("creating a second workload that has available quota but cannot fit in topology", func() {
					pendingWl = utiltestingapi.MakeWorkload("pending-wl", ns.Name).
						Queue(kueue.LocalQueueName(localQueue.Name)).
						PodSets(
							*utiltestingapi.MakePodSet("main", 1).
								RequiredTopologyRequest(corev1.LabelHostname).
								Request(corev1.ResourceCPU, "1").
								Request(corev1.ResourceMemory, "1Gi").
								Obj(),
						).
						Obj()
					behavioral.MustCreate(ctx, k8sClient, pendingWl)
					behavioral.ExpectWorkloadsToBePending(ctx, k8sClient, pendingWl)
				})

				ginkgo.By("verifying the second workload stays pending while the first workload holds all topology domains", func() {
					gomega.Consistently(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(pendingWl), pendingWl)).To(gomega.Succeed())
						g.Expect(workload.IsAdmitted(pendingWl)).To(gomega.BeFalse())
					}, behavioral.ConsistentDuration, behavioral.ShortInterval).Should(gomega.Succeed())
				})

				ginkgo.By("reclaiming the workers podset of the admitted workload to release its topology domain", func() {
					integration.UpdateReclaimablePods(ctx, k8sClient, admittedWl, []kueue.ReclaimablePod{{Name: "workers", Count: 1}})
				})

				ginkgo.By("the admitted workload continues to be admitted", func() {
					gomega.Consistently(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(admittedWl), admittedWl)).To(gomega.Succeed())
						g.Expect(workload.IsAdmitted(admittedWl)).To(gomega.BeTrue())
					}, behavioral.ConsistentDuration, behavioral.ShortInterval).Should(gomega.Succeed())
				})

				ginkgo.By("verifying the pending workload is now admitted on the released topology domain", func() {
					behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, pendingWl)
					gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(pendingWl), pendingWl)).To(gomega.Succeed())
					pendingTA := topologyAssignmentByName(gomega.Default, pendingWl, "main")
					gomega.Expect(pendingTA.Domains).To(gomega.HaveLen(1))
					gomega.Expect(pendingTA.Domains[0].Values[0]).To(gomega.Equal(freedNode))
				})
			})
		})

		ginkgo.When("Workload has positive-count PodSet with zero-valued resource requests", func() {
			var (
				topology     *kueue.Topology
				tasFlavor    *kueue.ResourceFlavor
				clusterQueue *kueue.ClusterQueue
				localQueue   *kueue.LocalQueue
				nodes        []corev1.Node
			)

			ginkgo.BeforeEach(func() {
				topology = utiltestingapi.MakeDefaultThreeLevelTopology("zero-req-topology")
				behavioral.MustCreate(ctx, k8sClient, topology)

				tasFlavor = utiltestingapi.MakeResourceFlavor("tas-flavor-zero").
					NodeLabel("node-group", "tas-zero").
					TopologyName(topology.Name).
					Obj()
				behavioral.MustCreate(ctx, k8sClient, tasFlavor)

				clusterQueue = utiltestingapi.MakeClusterQueue("cluster-queue-zero").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas(tasFlavor.Name).
						Resource(corev1.ResourceCPU, "5").
						Resource(corev1.ResourceMemory, "5Gi").
						Obj()).
					Obj()
				behavioral.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, clusterQueue)

				localQueue = utiltestingapi.MakeLocalQueue("local-queue-zero", ns.Name).
					ClusterQueue(clusterQueue.Name).
					Obj()
				behavioral.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, localQueue)

				nodes = []corev1.Node{
					*testingnode.MakeNode("z1").
						Label("node-group", "tas-zero").
						Label(utiltesting.DefaultBlockTopologyLevel, "b1").
						Label(utiltesting.DefaultRackTopologyLevel, "r1").
						Label(corev1.LabelHostname, "z1").
						StatusAllocatable(corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("1"),
							corev1.ResourceMemory: resource.MustParse("1Gi"),
							corev1.ResourcePods:   resource.MustParse("10"),
						}).
						Ready().
						Obj(),
					*testingnode.MakeNode("z2").
						Label("node-group", "tas-zero").
						Label(utiltesting.DefaultBlockTopologyLevel, "b1").
						Label(utiltesting.DefaultRackTopologyLevel, "r1").
						Label(corev1.LabelHostname, "z2").
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

			ginkgo.AfterEach(func() {
				gomega.Expect(behavioral.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
				gomega.Expect(behavioral.DeleteObject(ctx, k8sClient, localQueue)).Should(gomega.Succeed())
				behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
				behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, tasFlavor, true)
				behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, topology, true)
				for i := range nodes {
					behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, &nodes[i], true)
				}
			})

			ginkgo.It("should admit workload and assign topology assignment", func() {
				var wl *kueue.Workload

				ginkgo.By("creating a workload with zero-valued resource requests", func() {
					wl = utiltestingapi.MakeWorkload("wl-zero-requests", ns.Name).
						Queue(kueue.LocalQueueName(localQueue.Name)).
						PodSets(*utiltestingapi.MakePodSet("worker", 2).
							RequiredTopologyRequest(corev1.LabelHostname).
							Request(corev1.ResourceCPU, "0").
							Request(corev1.ResourceMemory, "0").
							Obj()).
						Obj()
					behavioral.MustCreate(ctx, k8sClient, wl)
				})

				ginkgo.By("verifying workload admission and topology assignment", func() {
					behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl)

					gomega.Eventually(func(g gomega.Gomega) {
						var updated kueue.Workload
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &updated)).To(gomega.Succeed())
						g.Expect(updated.Status.Admission).NotTo(gomega.BeNil())
						g.Expect(updated.Status.Admission.PodSetAssignments).To(gomega.HaveLen(1))
						g.Expect(assignedPodCount(updated.Status.Admission.PodSetAssignments[0].TopologyAssignment)).To(gomega.Equal(int32(2)))
					}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
				})
			})
		})
	})
})
