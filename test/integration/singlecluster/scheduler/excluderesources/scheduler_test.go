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

package excluderesources

import (
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	config "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	utiltas "sigs.k8s.io/kueue/pkg/util/tas"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingnode "sigs.k8s.io/kueue/pkg/util/testingjobs/node"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("SchedulerWithExcludeResourcePrefixes", ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	var (
		defaultFlavor *kueue.ResourceFlavor
		ns            *corev1.Namespace
		cq            *kueue.ClusterQueue
		lq            *kueue.LocalQueue
	)

	ginkgo.BeforeAll(func() {
		configuration := &config.Configuration{
			Resources: &config.Resources{
				ExcludeResourcePrefixes: []string{
					"networking.example.com/",
					"storage.example.com/",
				},
			},
		}
		fwk.StartManager(ctx, cfg, managerAndSchedulerSetup(configuration))
	})

	ginkgo.BeforeEach(func() {
		defaultFlavor = utiltestingapi.MakeResourceFlavor("default").Obj()
		gomega.Expect(k8sClient.Create(ctx, defaultFlavor)).To(gomega.Succeed())

		ns = &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: "exclude-",
			},
		}
		gomega.Expect(k8sClient.Create(ctx, ns)).To(gomega.Succeed())

		cq = utiltestingapi.MakeClusterQueue("test-cq").
			ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas("default").
					Resource(corev1.ResourceCPU, "10").
					Resource(corev1.ResourceMemory, "10Gi").
					Resource("networking.other.com/vpc", "1000").
					Obj(),
			).Obj()
		gomega.Expect(k8sClient.Create(ctx, cq)).To(gomega.Succeed())

		lq = utiltestingapi.MakeLocalQueue("test-lq", ns.Name).ClusterQueue(cq.Name).Obj()
		gomega.Expect(k8sClient.Create(ctx, lq)).To(gomega.Succeed())
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		util.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, defaultFlavor, true)
	})

	ginkgo.AfterAll(func() {
		fwk.StopManager(ctx)
	})

	ginkgo.It("should admit workload ignoring excluded resource", func() {
		ginkgo.By("Creating a workload that requests an excluded resource")
		wl := utiltestingapi.MakeWorkload("test-wl", ns.Name).
			Queue(kueue.LocalQueueName(lq.Name)).
			Request(corev1.ResourceCPU, "1").
			Request("networking.example.com/vpc", "100").
			Obj()
		gomega.Expect(k8sClient.Create(ctx, wl)).To(gomega.Succeed())

		wlKey := types.NamespacedName{
			Name:      wl.Name,
			Namespace: ns.Name,
		}

		ginkgo.By("Verifying workload gets admitted")
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, wlKey, wl)).To(gomega.Succeed())
			g.Expect(workload.IsAdmitted(wl)).To(gomega.BeTrue())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		ginkgo.By("Verifying excluded resource is not in admission resource usage")
		gomega.Expect(wl.Status.Admission).NotTo(gomega.BeNil())
		gomega.Expect(wl.Status.Admission.PodSetAssignments).To(gomega.HaveLen(1))
		resourceUsage := wl.Status.Admission.PodSetAssignments[0].ResourceUsage
		gomega.Expect(resourceUsage).To(gomega.HaveKey(corev1.ResourceCPU))
		gomega.Expect(resourceUsage).NotTo(gomega.HaveKey("networking.example.com/vpc"))
	})

	ginkgo.It("should ignore multiple excluded resource prefixes", func() {
		ginkgo.By("Creating a workload that requests resources from multiple excluded prefixes")
		wl := utiltestingapi.MakeWorkload("multi-exclude-wl", ns.Name).
			Queue(kueue.LocalQueueName(lq.Name)).
			Request(corev1.ResourceCPU, "2").
			Request("networking.example.com/vpc", "10").
			Request("storage.example.com/disk", "50").
			Obj()
		gomega.Expect(k8sClient.Create(ctx, wl)).To(gomega.Succeed())

		wlKey := types.NamespacedName{
			Name:      wl.Name,
			Namespace: ns.Name,
		}

		ginkgo.By("Verifying workload admission")
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, wlKey, wl)).To(gomega.Succeed())
			g.Expect(workload.IsAdmitted(wl)).To(gomega.BeTrue())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		ginkgo.By("Verifying both excluded resources are not in resource usage")
		gomega.Expect(wl.Status.Admission).NotTo(gomega.BeNil())
		resourceUsage := wl.Status.Admission.PodSetAssignments[0].ResourceUsage
		gomega.Expect(resourceUsage).To(gomega.HaveKey(corev1.ResourceCPU))
		gomega.Expect(resourceUsage).NotTo(gomega.HaveKey("networking.example.com/vpc"))
		gomega.Expect(resourceUsage).NotTo(gomega.HaveKey("storage.example.com/disk"))
	})

	ginkgo.It("should enforce quota for non-excluded resources", func() {
		ginkgo.By("Creating a workload that consumes most of the CPU quota")
		wl1 := utiltestingapi.MakeWorkload("quota-wl-1", ns.Name).
			Queue(kueue.LocalQueueName(lq.Name)).
			Request(corev1.ResourceCPU, "8").
			Request("networking.example.com/vpc", "1000").
			Obj()
		gomega.Expect(k8sClient.Create(ctx, wl1)).To(gomega.Succeed())

		wl1Key := types.NamespacedName{Name: wl1.Name, Namespace: ns.Name}

		ginkgo.By("Waiting for first workload to be admitted")
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, wl1Key, wl1)).To(gomega.Succeed())
			g.Expect(workload.IsAdmitted(wl1)).To(gomega.BeTrue())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		ginkgo.By("Creating a second workload that would exceed CPU quota")
		wl2 := utiltestingapi.MakeWorkload("quota-wl-2", ns.Name).
			Queue(kueue.LocalQueueName(lq.Name)).
			Request(corev1.ResourceCPU, "5").
			Request("networking.example.com/vpc", "1").
			Obj()
		gomega.Expect(k8sClient.Create(ctx, wl2)).To(gomega.Succeed())

		wl2Key := types.NamespacedName{Name: wl2.Name, Namespace: ns.Name}

		ginkgo.By("Verifying second workload is not admitted due to CPU quota")
		gomega.Consistently(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, wl2Key, wl2)).To(gomega.Succeed())
			g.Expect(workload.IsAdmitted(wl2)).Should(gomega.BeFalse())
		}, util.ConsistentDuration, util.ShortInterval).Should(gomega.Succeed())
	})

	ginkgo.It("should use exact prefix matching", func() {
		ginkgo.By("Creating a workload with a resource that doesn't match the excluded prefix")
		wl := utiltestingapi.MakeWorkload("prefix-test-wl", ns.Name).
			Queue(kueue.LocalQueueName(lq.Name)).
			Request(corev1.ResourceCPU, "1").
			Request("networking.other.com/vpc", "100").
			Obj()
		gomega.Expect(k8sClient.Create(ctx, wl)).To(gomega.Succeed())

		wlKey := types.NamespacedName{Name: wl.Name, Namespace: ns.Name}

		ginkgo.By("Verifying workload is admitted")
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, wlKey, wl)).To(gomega.Succeed())
			g.Expect(workload.IsAdmitted(wl)).To(gomega.BeTrue())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		ginkgo.By("Verifying non-matching resource is NOT excluded (appears in resource usage)")
		gomega.Expect(k8sClient.Get(ctx, wlKey, wl)).To(gomega.Succeed())
		gomega.Expect(wl.Status.Admission.PodSetAssignments).To(gomega.HaveLen(1))
		resourceUsage := wl.Status.Admission.PodSetAssignments[0].ResourceUsage
		gomega.Expect(resourceUsage).To(gomega.HaveKey(corev1.ResourceName("networking.other.com/vpc")))
		gomega.Expect(resourceUsage).To(gomega.HaveKey(corev1.ResourceCPU))
	})
})

var _ = ginkgo.Describe("TAS with ExcludeResourcePrefixes", ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	var (
		tasFlavor *kueue.ResourceFlavor
		topology  *kueue.Topology
		ns        *corev1.Namespace
		cq        *kueue.ClusterQueue
		lq        *kueue.LocalQueue
		nodes     []corev1.Node
	)

	ginkgo.BeforeAll(func() {
		configuration := &config.Configuration{
			Resources: &config.Resources{
				ExcludeResourcePrefixes: []string{
					"example.com/",
				},
			},
		}
		fwk.StartManager(ctx, cfg, managerAndSchedulerSetup(configuration))
	})

	ginkgo.BeforeEach(func() {
		nodes = []corev1.Node{
			*testingnode.MakeNode("tas-n1").
				Label("node-group", "tas").
				Label(corev1.LabelHostname, "tas-n1").
				StatusAllocatable(corev1.ResourceList{
					corev1.ResourceCPU:  resource.MustParse("4"),
					corev1.ResourcePods: resource.MustParse("10"),
					corev1.ResourceName("example.com/test-resource"): resource.MustParse("2"),
				}).
				Ready().
				Obj(),
			*testingnode.MakeNode("tas-n2").
				Label("node-group", "tas").
				Label(corev1.LabelHostname, "tas-n2").
				StatusAllocatable(corev1.ResourceList{
					corev1.ResourceCPU:  resource.MustParse("4"),
					corev1.ResourcePods: resource.MustParse("10"),
					corev1.ResourceName("example.com/test-resource"): resource.MustParse("2"),
				}).
				Ready().
				Obj(),
		}
		util.CreateNodesWithStatus(ctx, k8sClient, nodes)

		topology = utiltestingapi.MakeDefaultOneLevelTopology("tas-topology")
		util.MustCreate(ctx, k8sClient, topology)

		tasFlavor = utiltestingapi.MakeResourceFlavor("tas-flavor").
			NodeLabel("node-group", "tas").
			TopologyName("tas-topology").
			Obj()
		util.MustCreate(ctx, k8sClient, tasFlavor)

		ns = &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: "tas-exclude-",
			},
		}
		gomega.Expect(k8sClient.Create(ctx, ns)).To(gomega.Succeed())

		cq = utiltestingapi.MakeClusterQueue("tas-exclude-cq").
			ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas("tas-flavor").
					Resource(corev1.ResourceCPU, "10").
					Obj(),
			).Obj()
		util.MustCreate(ctx, k8sClient, cq)
		util.ExpectClusterQueuesToBeActive(ctx, k8sClient, cq)

		lq = utiltestingapi.MakeLocalQueue("tas-exclude-lq", ns.Name).ClusterQueue(cq.Name).Obj()
		util.MustCreate(ctx, k8sClient, lq)
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		util.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, tasFlavor, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, topology, true)
		for _, node := range nodes {
			util.ExpectObjectToBeDeleted(ctx, k8sClient, &node, true)
		}
	})

	ginkgo.AfterAll(func() {
		fwk.StopManager(ctx)
	})

	ginkgo.It("should use excluded resource from PodSpec for TAS placement", func() {
		wl := utiltestingapi.MakeWorkload("wl", ns.Name).
			Queue(kueue.LocalQueueName(lq.Name)).
			PodSets(*utiltestingapi.MakePodSet("main", 4).
				PreferredTopologyRequest(corev1.LabelHostname).
				Request(corev1.ResourceCPU, "100m").
				Request("example.com/test-resource", "1").
				Obj()).
			Obj()
		util.MustCreate(ctx, k8sClient, wl)

		util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl)

		ginkgo.By("verifying pods are spread based on excluded resource", func() {
			gomega.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: wl.Name, Namespace: ns.Name}, wl)).To(gomega.Succeed())
			topologyAssignment := wl.Status.Admission.PodSetAssignments[0].TopologyAssignment
			gomega.Expect(topologyAssignment).NotTo(gomega.BeNil())

			domains := utiltas.InternalFrom(topologyAssignment).Domains
			gomega.Expect(domains).To(gomega.HaveLen(2))
			for _, domain := range domains {
				gomega.Expect(domain.Count).To(gomega.Equal(int32(2)))
			}
		})

		ginkgo.By("verifying excluded resource is not in quota usage", func() {
			resourceUsage := wl.Status.Admission.PodSetAssignments[0].ResourceUsage
			gomega.Expect(resourceUsage).To(gomega.HaveKey(corev1.ResourceCPU))
			gomega.Expect(resourceUsage).NotTo(gomega.HaveKey(corev1.ResourceName("example.com/test-resource")))
		})
	})
})
