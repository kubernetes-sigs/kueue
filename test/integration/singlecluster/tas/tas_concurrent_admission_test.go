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
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/tas"
	"sigs.k8s.io/kueue/pkg/features"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingnode "sigs.k8s.io/kueue/pkg/util/testingjobs/node"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/pkg/workload/concurrentadmission"
	workloadevict "sigs.k8s.io/kueue/pkg/workload/evict"
	"sigs.k8s.io/kueue/test/util/behavioral"
	"sigs.k8s.io/kueue/test/util/behavioral/integration"
)

var _ = ginkgo.Describe("TopologyAwareScheduling with ConcurrentAdmission", ginkgo.Ordered, func() {
	var (
		ns           *corev1.Namespace
		topology     *kueue.Topology
		flavorA      *kueue.ResourceFlavor
		flavorB      *kueue.ResourceFlavor
		clusterQueue *kueue.ClusterQueue
		localQueue   *kueue.LocalQueue
		nodes        []corev1.Node
	)

	newNode := func(name, pool string) corev1.Node {
		return *testingnode.MakeNode(name).
			Label("pool", pool).
			Label(corev1.LabelHostname, name).
			StatusAllocatable(corev1.ResourceList{
				corev1.ResourceCPU:  resource.MustParse("1"),
				corev1.ResourcePods: resource.MustParse("10"),
			}).
			Ready().
			Obj()
	}

	ginkgo.BeforeAll(func() {
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.ConcurrentAdmission, true)
		fwk.StartManager(ctx, cfg, managerSetup())
	})

	ginkgo.AfterAll(func() {
		fwk.StopManager(ctx)
	})

	ginkgo.BeforeEach(func() {
		ns = behavioral.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "tas-ca-")
		topology = utiltestingapi.MakeDefaultOneLevelTopology("tas-single-level")
		behavioral.MustCreate(ctx, k8sClient, topology)

		flavorA = utiltestingapi.MakeResourceFlavor("flavor-a").
			NodeLabel("pool", "a").
			TopologyName(topology.Name).
			Obj()
		behavioral.MustCreate(ctx, k8sClient, flavorA)
		flavorB = utiltestingapi.MakeResourceFlavor("flavor-b").
			NodeLabel("pool", "b").
			TopologyName(topology.Name).
			Obj()
		behavioral.MustCreate(ctx, k8sClient, flavorB)

		clusterQueue = utiltestingapi.MakeClusterQueue("cluster-queue").
			ConcurrentAdmissionPolicy(kueue.ConcurrentAdmissionTryPreferredFlavors).
			ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas(flavorA.Name).Resource(corev1.ResourceCPU, "5").Obj(),
				*utiltestingapi.MakeFlavorQuotas(flavorB.Name).Resource(corev1.ResourceCPU, "5").Obj(),
			).
			Obj()
		behavioral.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, clusterQueue)

		localQueue = utiltestingapi.MakeLocalQueue("local-queue", ns.Name).
			ClusterQueue(clusterQueue.Name).
			Obj()
		behavioral.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, localQueue)

		nodes = []corev1.Node{newNode("a1", "a"), newNode("a2", "a"), newNode("b1", "b")}
		integration.CreateNodesWithStatus(ctx, k8sClient, nodes)
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(behavioral.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, localQueue, true)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, flavorA, true)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, flavorB, true)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, topology, true)
		for i := range nodes {
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, &nodes[i], true)
		}
		gomega.Expect(forceDeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
	})

	ginkgo.It("should replace a failed node through the Variant and copy its topology assignment to the parent", func() {
		parent := utiltestingapi.MakeWorkload("parent", ns.Name).
			Queue(kueue.LocalQueueName(localQueue.Name)).
			ParentVariant().
			PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
				RequiredTopologyRequest(corev1.LabelHostname).
				Request(corev1.ResourceCPU, "1").
				Obj()).
			Obj()
		behavioral.MustCreate(ctx, k8sClient, parent)

		var failedNode, replacementNode string
		ginkgo.By("waiting for the parent to be admitted on a node of flavor-a", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(parent), parent)).To(gomega.Succeed())
				g.Expect(workload.IsAdmitted(parent)).To(gomega.BeTrue())
				g.Expect(workload.TASAssignedNodeNames(parent)).To(gomega.HaveLen(1))
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			failedNode = workload.TASAssignedNodeNames(parent)[0]
			gomega.Expect(failedNode).To(gomega.BeElementOf("a1", "a2"))
			replacementNode = "a1"
			if failedNode == "a1" {
				replacementNode = "a2"
			}
		})

		ginkgo.By("making the assigned node NotReady", func() {
			node := &corev1.Node{}
			gomega.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: failedNode}, node)).To(gomega.Succeed())
			behavioral.SetNodeCondition(ctx, k8sClient, node, &corev1.NodeCondition{
				Type:               corev1.NodeReady,
				Status:             corev1.ConditionFalse,
				LastTransitionTime: metav1.NewTime(time.Now().Add(-tas.NodeFailureDelay)),
			})
		})

		ginkgo.By("checking that the admitted Variant is moved to the replacement node", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				workloads := &kueue.WorkloadList{}
				g.Expect(k8sClient.List(ctx, workloads, client.InNamespace(ns.Name))).To(gomega.Succeed())
				var admittedVariants []*kueue.Workload
				for i := range workloads.Items {
					wl := &workloads.Items[i]
					if concurrentadmission.IsVariant(wl) && workload.IsAdmitted(wl) {
						admittedVariants = append(admittedVariants, wl)
					}
				}
				g.Expect(admittedVariants).To(gomega.HaveLen(1))
				g.Expect(workload.TASAssignedNodeNames(admittedVariants[0])).To(gomega.Equal([]string{replacementNode}))
				g.Expect(admittedVariants[0].Status.UnhealthyNodes).To(gomega.BeEmpty())
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("checking that the parent stays admitted and gets the replacement node", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(parent), parent)).To(gomega.Succeed())
				g.Expect(workloadevict.IsEvicted(parent)).To(gomega.BeFalse())
				g.Expect(workload.IsAdmitted(parent)).To(gomega.BeTrue())
				g.Expect(workload.TASAssignedNodeNames(parent)).To(gomega.Equal([]string{replacementNode}))
				g.Expect(parent.Status.UnhealthyNodes).To(gomega.BeEmpty())
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		})
	})
})
