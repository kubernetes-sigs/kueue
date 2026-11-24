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

	config "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingnode "sigs.k8s.io/kueue/pkg/util/testingjobs/node"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/test/util/behavioral"
	"sigs.k8s.io/kueue/test/util/behavioral/integration"
)

var _ = ginkgo.Describe("AggressivePriorityColocate", func() {
	var (
		ns       *corev1.Namespace
		nodes    []corev1.Node
		topology *kueue.Topology
		flavor   *kueue.ResourceFlavor
		cq       *kueue.ClusterQueue
		lq       *kueue.LocalQueue
	)
	ginkgo.BeforeEach(func() {
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.AggressivePriorityColocate, true)
		fwk.StartManagerForSpec(ctx, cfg, managerSetupWithConfig(&config.Configuration{}))
		ns = behavioral.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "priority-colocation-")
		for _, name := range []string{"colocate-a1", "colocate-a2", "colocate-b1"} {
			rack := "a"
			if name == "colocate-b1" {
				rack = "b"
			}
			nodes = append(nodes, *testingnode.MakeNode(name).
				Label("node-group", "colocation").Label("rack", rack).Label(corev1.LabelHostname, name).
				StatusAllocatable(corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("4"), corev1.ResourcePods: resource.MustParse("32")}).
				Ready().Obj())
		}
		integration.CreateNodesWithStatus(ctx, k8sClient, nodes)
		topology = utiltestingapi.MakeTopology("colocation").Levels("rack", corev1.LabelHostname).Obj()
		behavioral.MustCreate(ctx, k8sClient, topology)
		flavor = utiltestingapi.MakeResourceFlavor("colocation").
			NodeLabel("node-group", "colocation").TopologyName(topology.Name).Obj()
		behavioral.MustCreate(ctx, k8sClient, flavor)
		cq = utiltestingapi.MakeClusterQueue("colocation").
			Preemption(kueue.ClusterQueuePreemption{WithinClusterQueue: kueue.PreemptionPolicyLowerPriority}).
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas(flavor.Name).Resource(corev1.ResourceCPU, "32").Obj()).Obj()
		behavioral.MustCreate(ctx, k8sClient, cq)
		behavioral.ExpectClusterQueuesToBeActive(ctx, k8sClient, cq)
		lq = utiltestingapi.MakeLocalQueue("queue", ns.Name).ClusterQueue(cq.Name).Obj()
		behavioral.MustCreate(ctx, k8sClient, lq)
	})
	ginkgo.AfterEach(func() {
		gomega.Expect(behavioral.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		gomega.Expect(behavioral.DeleteObject(ctx, k8sClient, lq)).To(gomega.Succeed())
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, flavor, true)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, topology, true)
		for _, node := range nodes {
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, &node, true)
		}
		nodes = nil
		gomega.Expect(forceDeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
	})
	ginkgo.It("evicts an eligible victim despite a free rack and admits at the retained target after release", func() {
		peer := utiltestingapi.MakeWorkload("peer", ns.Name).Queue(kueue.LocalQueueName(lq.Name)).Priority(50).
			PodSets(*utiltestingapi.MakePodSet("worker", 1).
				NodeSelector(map[string]string{corev1.LabelHostname: "colocate-a1"}).
				RequiredTopologyRequest("rack").Request(corev1.ResourceCPU, "4").Obj()).Obj()
		victim := utiltestingapi.MakeWorkload("victim", ns.Name).Queue(kueue.LocalQueueName(lq.Name)).Priority(0).
			PodSets(*utiltestingapi.MakePodSet("worker", 1).
				NodeSelector(map[string]string{corev1.LabelHostname: "colocate-a2"}).
				RequiredTopologyRequest("rack").Request(corev1.ResourceCPU, "4").Obj()).Obj()
		incoming := utiltestingapi.MakeWorkload("incoming", ns.Name).Queue(kueue.LocalQueueName(lq.Name)).Priority(50).
			PodSets(*utiltestingapi.MakePodSet("worker", 1).
				RequiredTopologyRequest("rack").Request(corev1.ResourceCPU, "4").Obj()).Obj()
		behavioral.MustCreate(ctx, k8sClient, peer)
		behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, peer)
		behavioral.MustCreate(ctx, k8sClient, victim)
		behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, victim)
		behavioral.MustCreate(ctx, k8sClient, incoming)
		behavioral.ExpectWorkloadsToBePreempted(ctx, k8sClient, victim)
		gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(incoming), incoming)).To(gomega.Succeed())
		gomega.Expect(workload.IsAdmitted(incoming)).To(gomega.BeFalse())
		behavioral.FinishEvictionForWorkloads(ctx, k8sClient, victim)
		behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, incoming, peer)
		gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(incoming), incoming)).To(gomega.Succeed())
		ta := incoming.Status.Admission.PodSetAssignments[0].TopologyAssignment
		gomega.Expect(ta.Domains).To(gomega.HaveLen(1))
		gomega.Expect(ta.Levels).To(gomega.Equal([]string{corev1.LabelHostname}))
		gomega.Expect(ta.Domains[0].Values).To(gomega.Equal([]string{"colocate-a2"}))
	})
})
