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
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/features"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingnode "sigs.k8s.io/kueue/pkg/util/testingjobs/node"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/pkg/workloadslicing"
	"sigs.k8s.io/kueue/test/util/behavioral"
	"sigs.k8s.io/kueue/test/util/behavioral/integration"
)

var _ = ginkgo.Describe("Elastic replacement topology accounting", func() {
	ginkgo.It("should preempt a lower-priority workload to grow a slice to fill a four-CPU node", func() {
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.TopologyAwareScheduling, true)
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.ElasticJobsViaWorkloadSlices, true)
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.ElasticJobsViaWorkloadSlicesWithTAS, true)
		fwk.StartManager(ctx, cfg, managerSetup())
		ginkgo.DeferCleanup(fwk.StopManager, ctx)
		ns := behavioral.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "elastic-accounting-")
		ginkgo.DeferCleanup(func() {
			gomega.Expect(forceDeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		})
		node := testingnode.MakeNode("elastic-accounting-node").
			Label("node-group", "elastic-accounting").Label(corev1.LabelHostname, "elastic-accounting-node").
			StatusAllocatable(corev1.ResourceList{
				corev1.ResourceCPU: resource.MustParse("4"), corev1.ResourcePods: resource.MustParse("20"),
			}).Ready().Obj()
		integration.CreateNodesWithStatus(ctx, k8sClient, []corev1.Node{*node})
		ginkgo.DeferCleanup(func() {
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, node, true)
		})
		topology := utiltestingapi.MakeTopology("elastic-accounting-topology").Levels(corev1.LabelHostname).Obj()
		behavioral.MustCreate(ctx, k8sClient, topology)
		ginkgo.DeferCleanup(func() {
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, topology, true)
		})
		flavor := utiltestingapi.MakeResourceFlavor("elastic-accounting-flavor").
			NodeLabel("node-group", "elastic-accounting").TopologyName(topology.Name).Obj()
		behavioral.MustCreate(ctx, k8sClient, flavor)
		ginkgo.DeferCleanup(func() {
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, flavor, true)
		})
		// Spare quota makes this exercise topology-driven victim selection.
		cq := utiltestingapi.MakeClusterQueue("elastic-accounting-cq").
			Preemption(kueue.ClusterQueuePreemption{WithinClusterQueue: kueue.PreemptionPolicyLowerPriority}).
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas(flavor.Name).Resource(corev1.ResourceCPU, "20").Obj()).Obj()
		behavioral.MustCreate(ctx, k8sClient, cq)
		ginkgo.DeferCleanup(func() {
			gomega.Expect(behavioral.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
		})
		behavioral.ExpectClusterQueuesToBeActive(ctx, k8sClient, cq)
		lq := utiltestingapi.MakeLocalQueue("queue", ns.Name).ClusterQueue(cq.Name).Obj()
		behavioral.MustCreate(ctx, k8sClient, lq)

		ginkgo.By("admitting a two-CPU predecessor and a lower-priority two-CPU workload")
		original := utiltestingapi.MakeWorkload("original", ns.Name).Queue(kueue.LocalQueueName(lq.Name)).Priority(10).
			Annotation(constants.ElasticJobAnnotation, "true").
			PodSets(*utiltestingapi.MakePodSet("workers", 2).
				Request(corev1.ResourceCPU, "1").UnconstrainedTopologyRequest().Obj()).Obj()
		behavioral.MustCreate(ctx, k8sClient, original)
		behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, original)
		victim := utiltestingapi.MakeWorkload("victim", ns.Name).Queue(kueue.LocalQueueName(lq.Name)).Priority(1).
			PodSets(*utiltestingapi.MakePodSet("workers", 2).
				Request(corev1.ResourceCPU, "1").UnconstrainedTopologyRequest().Obj()).Obj()
		behavioral.MustCreate(ctx, k8sClient, victim)
		behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, victim)

		ginkgo.By("requesting four CPUs for the replacement and observing topology-driven preemption")
		replacement := utiltestingapi.MakeWorkload("replacement", ns.Name).Queue(kueue.LocalQueueName(lq.Name)).Priority(10).
			Annotation(constants.ElasticJobAnnotation, "true").
			Annotation(workloadslicing.WorkloadSliceReplacementFor, string(workload.Key(original))).
			Annotation(kueue.WorkloadSliceNameAnnotation, original.Name).
			PodSets(*utiltestingapi.MakePodSet("workers", 4).
				Request(corev1.ResourceCPU, "1").UnconstrainedTopologyRequest().Obj()).Obj()
		behavioral.MustCreate(ctx, k8sClient, replacement)
		behavioral.ExpectWorkloadsToBePreempted(ctx, k8sClient, victim)
		behavioral.FinishEvictionForWorkloads(ctx, k8sClient, victim)
		behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, replacement)
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(replacement), replacement)).To(gomega.Succeed())
			g.Expect(replacement.Status.Admission).NotTo(gomega.BeNil())
			g.Expect(replacement.Status.Admission.PodSetAssignments).To(gomega.HaveLen(1))
			assignment := replacement.Status.Admission.PodSetAssignments[0]
			g.Expect(assignment.TopologyAssignment).NotTo(gomega.BeNil())
			g.Expect(assignedPodCount(assignment.TopologyAssignment)).To(gomega.Equal(int32(4)))
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(original), original)).To(gomega.Succeed())
			g.Expect(original.Status.Conditions).To(utiltesting.HaveConditionStatusTrue(kueue.WorkloadFinished))
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
	})
})
