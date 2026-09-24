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
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingnode "sigs.k8s.io/kueue/pkg/util/testingjobs/node"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("Topology Aware Scheduling with zero-count grouped PodSets", ginkgo.Ordered, func() {
	var (
		ns       *corev1.Namespace
		topology *kueue.Topology
		flavors  []*kueue.ResourceFlavor
		nodes    []corev1.Node
		cq       *kueue.ClusterQueue
	)

	ginkgo.BeforeAll(func() {
		fwk.StartManager(ctx, cfg, managerSetup())
	})
	ginkgo.AfterAll(func() {
		fwk.StopManager(ctx)
	})
	ginkgo.BeforeEach(func() {
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "tas-zero-count-group-")
		topology = utiltestingapi.MakeDefaultOneLevelTopology("zero-count-group")
		util.MustCreate(ctx, k8sClient, topology)
		flavors = nil
		nodes = nil
		for i, name := range []string{"small", "large"} {
			flavor := utiltestingapi.MakeResourceFlavor(name).NodeLabel("node-group", name).TopologyName(topology.Name).Obj()
			flavors = append(flavors, flavor)
			util.MustCreate(ctx, k8sClient, flavor)
			nodes = append(nodes, *testingnode.MakeNode(name).
				Label("node-group", name).Label(corev1.LabelHostname, name).
				StatusAllocatable(corev1.ResourceList{
					corev1.ResourceCPU:  *resource.NewQuantity(int64(i+1), resource.DecimalSI),
					corev1.ResourcePods: resource.MustParse("10"),
				}).Ready().Obj())
		}
		util.CreateNodesWithStatus(ctx, k8sClient, nodes)
		cq = utiltestingapi.MakeClusterQueue("zero-count-group").ResourceGroup(
			*utiltestingapi.MakeFlavorQuotas("small").Resource(corev1.ResourceCPU, "1").Obj(),
			*utiltestingapi.MakeFlavorQuotas("large").Resource(corev1.ResourceCPU, "2").Obj(),
		).Obj()
		util.MustCreate(ctx, k8sClient, cq)
		util.ExpectClusterQueuesToBeActive(ctx, k8sClient, cq)
		util.MustCreate(ctx, k8sClient, utiltestingapi.MakeLocalQueue("queue", ns.Name).ClusterQueue(cq.Name).Obj())
	})
	ginkgo.AfterEach(func() {
		gomega.Expect(forceDeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		util.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
		for _, flavor := range flavors {
			util.ExpectObjectToBeDeleted(ctx, k8sClient, flavor, true)
		}
		util.ExpectObjectToBeDeleted(ctx, k8sClient, topology, true)
		for i := range nodes {
			util.ExpectObjectToBeDeleted(ctx, k8sClient, &nodes[i], true)
		}
	})

	ginkgo.DescribeTable("should use actual requests for first admission with zero-count grouped workers", func(elastic bool) {
		ginkgo.By("leaving only the smaller node available before creating the workload")
		util.ExpectObjectToBeDeleted(ctx, k8sClient, &nodes[1], true)
		wl := utiltestingapi.MakeWorkload("workload", ns.Name).Queue("queue").PodSets(
			*utiltestingapi.MakePodSet("leader", 1).Request(corev1.ResourceCPU, "1").
				PreferredTopologyRequest(corev1.LabelHostname).PodSetGroup("ranks").Obj(),
			*utiltestingapi.MakePodSet("workers", 0).Request(corev1.ResourceCPU, "1").
				PreferredTopologyRequest(corev1.LabelHostname).PodSetGroup("ranks").Obj(),
		).Obj()
		if elastic {
			features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.ElasticJobsViaWorkloadSlices, true)
			features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.ElasticJobsViaWorkloadSlicesWithTAS, true)
			wl.Annotations = map[string]string{constants.ElasticJobAnnotation: "true"}
			for i := range wl.Spec.PodSets {
				wl.Spec.PodSets[i].TopologyRequest.Preferred = nil
				wl.Spec.PodSets[i].TopologyRequest.Unconstrained = new(true)
			}
		}
		util.MustCreate(ctx, k8sClient, wl)

		ginkgo.By("admitting the leader on the smaller flavor without reserving resources for workers")
		util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl)
		gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), wl)).To(gomega.Succeed())
		gomega.Expect(wl.Status.ReclaimablePods).To(gomega.BeEmpty())
		gomega.Expect(wl.Status.Admission.PodSetAssignments).To(gomega.HaveLen(2))
		leader := podSetAssignmentByName(wl, "leader")
		workers := podSetAssignmentByName(wl, "workers")
		gomega.Expect(leader).NotTo(gomega.BeNil())
		gomega.Expect(workers).NotTo(gomega.BeNil())
		for _, assignment := range wl.Status.Admission.PodSetAssignments {
			gomega.Expect(assignment.Flavors[corev1.ResourceCPU]).To(gomega.Equal(kueue.ResourceFlavorReference("small")))
		}
		gomega.Expect(leader.Count).To(gomega.HaveValue(gomega.Equal(int32(1))))
		gomega.Expect(workers.Count).To(gomega.HaveValue(gomega.Equal(int32(0))))
		gomega.Expect(leader.ResourceUsage).To(gomega.Equal(corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1")}))
		gomega.Expect(workers.ResourceUsage).To(gomega.Equal(corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("0")}))
		gomega.Expect(leader.TopologyAssignment).NotTo(gomega.BeNil())
		gomega.Expect(workers.TopologyAssignment).To(gomega.BeNil())
	},
		ginkgo.Entry("non-elastic workload with preferred topology", false),
		ginkgo.Entry("elastic workload with unconstrained topology", true),
	)

	ginkgo.It("should readmit remaining pods without requiring capacity for completed grouped workers", func() {
		wl := utiltestingapi.MakeWorkload("workload", ns.Name).Queue("queue").PodSets(
			*utiltestingapi.MakePodSet("leader", 1).Request(corev1.ResourceCPU, "1").
				PreferredTopologyRequest(corev1.LabelHostname).PodSetGroup("ranks").Obj(),
			*utiltestingapi.MakePodSet("workers", 1).Request(corev1.ResourceCPU, "1").
				PreferredTopologyRequest(corev1.LabelHostname).PodSetGroup("ranks").Obj(),
		).Obj()

		ginkgo.By("admitting both non-elastic PodSets on the larger flavor", func() {
			util.MustCreate(ctx, k8sClient, wl)
			util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl)
			gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), wl)).To(gomega.Succeed())
			for _, assignment := range wl.Status.Admission.PodSetAssignments {
				gomega.Expect(assignment.Flavors[corev1.ResourceCPU]).To(gomega.Equal(kueue.ResourceFlavorReference("large")))
			}
		})
		ginkgo.By("reclaiming the completed workers and draining the queue", func() {
			util.UpdateReclaimablePods(ctx, k8sClient, wl, []kueue.ReclaimablePod{{Name: "workers", Count: 1}})
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cq), cq)).To(gomega.Succeed())
				cq.Spec.StopPolicy = new(kueue.HoldAndDrain)
				g.Expect(k8sClient.Update(ctx, cq)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
			util.FinishEvictionForWorkloads(ctx, k8sClient, wl)
			gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), wl)).To(gomega.Succeed())
			gomega.Expect(wl.Status.Admission).To(gomega.BeNil())
			gomega.Expect(wl.Status.ReclaimablePods).To(gomega.Equal([]kueue.ReclaimablePod{{Name: "workers", Count: 1}}))
		})
		ginkgo.By("removing the larger node and resuming with capacity for the remaining leader", func() {
			util.ExpectObjectToBeDeleted(ctx, k8sClient, &nodes[1], true)
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cq), cq)).To(gomega.Succeed())
				cq.Spec.StopPolicy = nil
				g.Expect(k8sClient.Update(ctx, cq)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})
		ginkgo.By("readmitting only the remaining pod on the smaller flavor", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), wl)).To(gomega.Succeed())
				g.Expect(workload.IsAdmitted(wl)).To(gomega.BeTrue())
				g.Expect(wl.Status.Admission.PodSetAssignments).To(gomega.HaveLen(2))
				leader := podSetAssignmentByName(wl, "leader")
				workers := podSetAssignmentByName(wl, "workers")
				g.Expect(leader).NotTo(gomega.BeNil())
				g.Expect(workers).NotTo(gomega.BeNil())
				g.Expect(leader.Count).To(gomega.HaveValue(gomega.Equal(int32(1))))
				g.Expect(workers.Count).To(gomega.HaveValue(gomega.Equal(int32(0))))
				for _, assignment := range wl.Status.Admission.PodSetAssignments {
					g.Expect(assignment.Flavors[corev1.ResourceCPU]).To(gomega.Equal(kueue.ResourceFlavorReference("small")))
				}
				g.Expect(leader.ResourceUsage).To(gomega.Equal(corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1")}))
				g.Expect(workers.ResourceUsage).To(gomega.Equal(corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("0")}))
				g.Expect(workers.TopologyAssignment).To(gomega.BeNil())
				g.Expect(leader.TopologyAssignment).NotTo(gomega.BeNil())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})
	})
})
