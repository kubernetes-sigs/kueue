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
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/controller/tas"
	"sigs.k8s.io/kueue/pkg/features"
	utiltas "sigs.k8s.io/kueue/pkg/util/tas"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingnode "sigs.k8s.io/kueue/pkg/util/testingjobs/node"
	testingpod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/pkg/workload/concurrentadmission"
	workloadevict "sigs.k8s.io/kueue/pkg/workload/evict"
	"sigs.k8s.io/kueue/pkg/workloadslicing"
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

	getAdmittedVariant := func(g gomega.Gomega) *kueue.Workload {
		workloads := &kueue.WorkloadList{}
		g.Expect(k8sClient.List(ctx, workloads, client.InNamespace(ns.Name))).To(gomega.Succeed())
		var admittedVariants []*kueue.Workload
		for i := range workloads.Items {
			if wl := &workloads.Items[i]; concurrentadmission.IsVariant(wl) && workload.IsAdmitted(wl) {
				admittedVariants = append(admittedVariants, wl)
			}
		}
		g.Expect(admittedVariants).To(gomega.HaveLen(1))
		return admittedVariants[0]
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

	ginkgo.It("should keep the parent's Pod gated while its node is unhealthy on the Variant and ungate it after the node recovers", func() {
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.TASFailedNodeReplacementFailFast, false)
		parent := utiltestingapi.MakeWorkload("parent", ns.Name).
			Queue(kueue.LocalQueueName(localQueue.Name)).
			ParentVariant().
			PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 2).
				PreferredTopologyRequest(corev1.LabelHostname).
				Request(corev1.ResourceCPU, "1").
				Obj()).
			Obj()
		behavioral.MustCreate(ctx, k8sClient, parent)

		ginkgo.By("waiting for the parent to be admitted on both nodes of flavor-a", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(parent), parent)).To(gomega.Succeed())
				g.Expect(workload.IsAdmitted(parent)).To(gomega.BeTrue())
				g.Expect(workload.TASAssignedNodeNames(parent)).To(gomega.ConsistOf("a1", "a2"))
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		})

		failedNode := &corev1.Node{}
		ginkgo.By("failing a1 with no replacement capacity in flavor-a", func() {
			gomega.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: "a1"}, failedNode)).To(gomega.Succeed())
			behavioral.SetNodeCondition(ctx, k8sClient, failedNode, &corev1.NodeCondition{
				Type:               corev1.NodeReady,
				Status:             corev1.ConditionFalse,
				LastTransitionTime: metav1.NewTime(time.Now().Add(-tas.NodeFailureDelay)),
			})
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(getAdmittedVariant(g).Status.UnhealthyNodes).To(gomega.Equal([]kueue.UnhealthyNode{{Name: "a1"}}))
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		})

		gatedPod := testingpod.MakePod("", ns.Name).
			Annotation(kueue.WorkloadAnnotation, parent.Name).
			Annotation(kueue.PodSetPreferredTopologyAnnotation, corev1.LabelHostname).
			Label(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
			TopologySchedulingGate()
		ginkgo.By("creating two gated Pods of the parent", func() {
			behavioral.MustCreate(ctx, k8sClient, gatedPod.Clone().Name("pod-1").Obj())
			behavioral.MustCreate(ctx, k8sClient, gatedPod.Clone().Name("pod-2").Obj())
		})

		ungatedNodes := func(g gomega.Gomega) []string {
			pods := &corev1.PodList{}
			g.Expect(k8sClient.List(ctx, pods, client.InNamespace(ns.Name))).To(gomega.Succeed())
			g.Expect(pods.Items).To(gomega.HaveLen(2))
			var nodes []string
			for _, pod := range pods.Items {
				if len(pod.Spec.SchedulingGates) == 0 {
					nodes = append(nodes, pod.Spec.NodeSelector[corev1.LabelHostname])
				}
			}
			return nodes
		}
		ginkgo.By("checking that only the Pod for a2 is ungated while a1 is unhealthy", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(ungatedNodes(g)).To(gomega.Equal([]string{"a2"}))
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			// Wait longer than the batch period of Pod events, so that a1 recovers
			// with no pending reconcile of the parent.
			gomega.Consistently(func(g gomega.Gomega) {
				g.Expect(ungatedNodes(g)).To(gomega.Equal([]string{"a2"}))
			}, behavioral.LongConsistentDuration, behavioral.ShortInterval).Should(gomega.Succeed())
		})

		ginkgo.By("recovering a1", func() {
			behavioral.SetNodeCondition(ctx, k8sClient, failedNode, &corev1.NodeCondition{
				Type:               corev1.NodeReady,
				Status:             corev1.ConditionTrue,
				LastTransitionTime: metav1.NewTime(time.Now()),
			})
		})

		ginkgo.By("checking that the other Pod is ungated onto a1", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(getAdmittedVariant(g).Status.UnhealthyNodes).To(gomega.BeEmpty())
				g.Expect(ungatedNodes(g)).To(gomega.ConsistOf("a1", "a2"))
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		})
	})

	ginkgo.It("should shrink the admitted Variant of an elastic parent and keep both assignments stable", func() {
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.ElasticJobsViaWorkloadSlices, true)
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.ElasticJobsViaWorkloadSlicesWithTAS, true)
		parent := utiltestingapi.MakeWorkload("parent", ns.Name).
			Queue(kueue.LocalQueueName(localQueue.Name)).
			ParentVariant().
			Annotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
			PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 2).
				UnconstrainedTopologyRequest().
				Request(corev1.ResourceCPU, "1").
				Obj()).
			Obj()
		behavioral.MustCreate(ctx, k8sClient, parent)

		ginkgo.By("waiting for the parent to be admitted on both nodes of flavor-a", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(parent), parent)).To(gomega.Succeed())
				g.Expect(workload.IsAdmitted(parent)).To(gomega.BeTrue())
				g.Expect(workload.TASAssignedNodeNames(parent)).To(gomega.ConsistOf("a1", "a2"))
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("running the surviving Pod on a2", func() {
			survivor := testingpod.MakePod("survivor", ns.Name).
				Annotation(kueue.WorkloadAnnotation, parent.Name).
				Annotation(kueue.WorkloadSliceNameAnnotation, parent.Name).
				Annotation(kueue.PodSetUnconstrainedTopologyAnnotation, "true").
				Label(constants.PodSetLabel, string(kueue.DefaultPodSetName)).
				NodeName("a2").NodeSelector(corev1.LabelHostname, "a2").
				Request(corev1.ResourceCPU, "1").TerminationGracePeriod(0).Obj()
			behavioral.MustCreate(ctx, k8sClient, survivor)
			integration.SetPodsPhase(ctx, k8sClient, corev1.PodRunning, survivor)
		})

		ginkgo.By("scaling the parent down to one Pod", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(parent), parent)).To(gomega.Succeed())
				parent.Spec.PodSets[0].Count = 1
				g.Expect(k8sClient.Update(ctx, parent)).To(gomega.Succeed())
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		})

		survivorDomains := []utiltas.TopologyDomainAssignment{{Values: []string{"a2"}, Count: 1}}
		variant := &kueue.Workload{}
		ginkgo.By("checking that the Variant and the parent converge on the surviving node", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				variant = getAdmittedVariant(g)
				g.Expect(utiltas.InternalFrom(variant.Status.Admission.PodSetAssignments[0].TopologyAssignment).Domains).To(gomega.Equal(survivorDomains))
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(parent), parent)).To(gomega.Succeed())
				g.Expect(workload.IsAdmitted(parent)).To(gomega.BeTrue())
				g.Expect(utiltas.InternalFrom(parent.Status.Admission.PodSetAssignments[0].TopologyAssignment).Domains).To(gomega.Equal(survivorDomains))
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("checking that neither Workload is written again", func() {
			parentVersion, variantVersion := parent.ResourceVersion, variant.ResourceVersion
			gomega.Consistently(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(parent), parent)).To(gomega.Succeed())
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(variant), variant)).To(gomega.Succeed())
				g.Expect(parent.ResourceVersion).To(gomega.Equal(parentVersion))
				g.Expect(variant.ResourceVersion).To(gomega.Equal(variantVersion))
			}, behavioral.ConsistentDuration, behavioral.ShortInterval).Should(gomega.Succeed())
		})
	})
})
