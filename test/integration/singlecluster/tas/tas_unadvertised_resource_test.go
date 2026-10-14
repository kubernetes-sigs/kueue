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
	utiltas "sigs.k8s.io/kueue/pkg/util/tas"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingnode "sigs.k8s.io/kueue/pkg/util/testingjobs/node"
	"sigs.k8s.io/kueue/test/util/behavioral"
	"sigs.k8s.io/kueue/test/util/behavioral/integration"
)

// unadvertisedResource is requested by the Pods but published by no node, like
// the memory of a virtual GPU. Only the scheduler knows how to account for it,
// so Kueue has no capacity to compare the request against.
const unadvertisedResource = corev1.ResourceName("example.com/vgpu-memory")

var _ = ginkgo.Describe("Topology Aware Scheduling for a resource no node publishes", ginkgo.Ordered, func() {
	var (
		ns       *corev1.Namespace
		topology *kueue.Topology
		flavor   *kueue.ResourceFlavor
		node     *corev1.Node
		cq       *kueue.ClusterQueue
	)

	ginkgo.BeforeAll(func() {
		fwk.StartManager(ctx, cfg, managerSetup())
	})
	ginkgo.AfterAll(func() {
		fwk.StopManager(ctx)
	})
	ginkgo.BeforeEach(func() {
		ns = behavioral.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "tas-unadvertised-")
		topology = utiltestingapi.MakeDefaultOneLevelTopology("unadvertised")
		behavioral.MustCreate(ctx, k8sClient, topology)
		flavor = utiltestingapi.MakeResourceFlavor("tas-flavor").NodeLabel("node-group", "tas").TopologyName(topology.Name).Obj()
		behavioral.MustCreate(ctx, k8sClient, flavor)
		node = testingnode.MakeNode("x1").
			Label("node-group", "tas").
			Label(corev1.LabelHostname, "x1").
			StatusAllocatable(corev1.ResourceList{
				corev1.ResourceCPU:  resource.MustParse("2"),
				corev1.ResourcePods: resource.MustParse("10"),
			}).
			Ready().
			Obj()
		integration.CreateNodesWithStatus(ctx, k8sClient, []corev1.Node{*node})
		cq = utiltestingapi.MakeClusterQueue("unadvertised").ResourceGroup(
			*utiltestingapi.MakeFlavorQuotas(flavor.Name).
				Resource(corev1.ResourceCPU, "2").
				Resource(unadvertisedResource, "4096").
				Obj()).
			Obj()
		behavioral.MustCreate(ctx, k8sClient, cq)
		behavioral.ExpectClusterQueuesToBeActive(ctx, k8sClient, cq)
		behavioral.MustCreate(ctx, k8sClient, utiltestingapi.MakeLocalQueue("queue", ns.Name).ClusterQueue(cq.Name).Obj())
	})
	ginkgo.AfterEach(func() {
		gomega.Expect(forceDeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, flavor, true)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, topology, true)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, node, true)
	})

	ginkgo.It("should admit a workload requesting a resource no node publishes", func() {
		var wl *kueue.Workload

		ginkgo.By("creating a workload requesting a resource no node publishes", func() {
			wl = utiltestingapi.MakeWorkload("wl", ns.Name).
				PodSets(*utiltestingapi.MakePodSet("worker", 1).
					RequiredTopologyRequest(corev1.LabelHostname).
					Obj()).
				Queue(kueue.LocalQueueName("queue")).
				Request(corev1.ResourceCPU, "1").
				Request(unadvertisedResource, "1024").
				Obj()
			behavioral.MustCreate(ctx, k8sClient, wl)
		})

		ginkgo.By("verifying the workload is admitted and placed on the node", func() {
			behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl)
			gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), wl)).To(gomega.Succeed())
			gomega.Expect(wl.Status.Admission.PodSetAssignments[0].TopologyAssignment).Should(gomega.BeComparableTo(
				utiltas.V1Beta2From(&utiltas.TopologyAssignment{
					Levels: []string{corev1.LabelHostname},
					Domains: []utiltas.TopologyDomainAssignment{
						{
							Count:  1,
							Values: []string{"x1"},
						},
					},
				}),
			))
		})
	})
})

var _ = ginkgo.Describe("Topology Aware Scheduling for a resource published by a node", ginkgo.Ordered, func() {
	var (
		ns       *corev1.Namespace
		topology *kueue.Topology
		flavor   *kueue.ResourceFlavor
		node     *corev1.Node
		cq       *kueue.ClusterQueue
	)

	ginkgo.BeforeAll(func() {
		fwk.StartManager(ctx, cfg, managerSetup())
	})
	ginkgo.AfterAll(func() {
		fwk.StopManager(ctx)
	})
	ginkgo.BeforeEach(func() {
		ns = behavioral.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "tas-published-")
		topology = utiltestingapi.MakeDefaultOneLevelTopology("published")
		behavioral.MustCreate(ctx, k8sClient, topology)
		flavor = utiltestingapi.MakeResourceFlavor("tas-flavor").NodeLabel("node-group", "tas").TopologyName(topology.Name).Obj()
		behavioral.MustCreate(ctx, k8sClient, flavor)
		node = testingnode.MakeNode("x1").
			Label("node-group", "tas").
			Label(corev1.LabelHostname, "x1").
			StatusAllocatable(corev1.ResourceList{
				corev1.ResourceCPU:   resource.MustParse("2"),
				corev1.ResourcePods:  resource.MustParse("10"),
				unadvertisedResource: resource.MustParse("8"),
			}).
			Ready().
			Obj()
		integration.CreateNodesWithStatus(ctx, k8sClient, []corev1.Node{*node})
		cq = utiltestingapi.MakeClusterQueue("published").ResourceGroup(
			*utiltestingapi.MakeFlavorQuotas(flavor.Name).
				Resource(corev1.ResourceCPU, "2").
				Resource(unadvertisedResource, "4096").
				Obj()).
			Obj()
		behavioral.MustCreate(ctx, k8sClient, cq)
		behavioral.ExpectClusterQueuesToBeActive(ctx, k8sClient, cq)
		behavioral.MustCreate(ctx, k8sClient, utiltestingapi.MakeLocalQueue("queue", ns.Name).ClusterQueue(cq.Name).Obj())
	})
	ginkgo.AfterEach(func() {
		gomega.Expect(forceDeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, flavor, true)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, topology, true)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, node, true)
	})

	ginkgo.It("should not admit a workload requesting more of the resource than the node has", func() {
		var wl *kueue.Workload

		ginkgo.By("creating a workload requesting more of the resource than the node publishes", func() {
			wl = utiltestingapi.MakeWorkload("wl", ns.Name).
				PodSets(*utiltestingapi.MakePodSet("worker", 1).
					RequiredTopologyRequest(corev1.LabelHostname).
					Obj()).
				Queue(kueue.LocalQueueName("queue")).
				Request(corev1.ResourceCPU, "1").
				Request(unadvertisedResource, "1024").
				Obj()
			behavioral.MustCreate(ctx, k8sClient, wl)
		})

		ginkgo.By("verifying the workload stays pending, as the node cannot fit the request", func() {
			behavioral.ExpectPendingWorkloadsMetric(cq, 0, 1)
		})
	})
})
