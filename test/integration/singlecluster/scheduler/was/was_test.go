//go:build !exclude_scheduler_library

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

package was

import (
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	resourceapi "k8s.io/api/resource/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	utiltas "sigs.k8s.io/kueue/pkg/util/tas"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingdra "sigs.k8s.io/kueue/pkg/util/testingjobs/dra"
	testingnode "sigs.k8s.io/kueue/pkg/util/testingjobs/node"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("WAS Simulator", ginkgo.Ordered, ginkgo.Label("feature:scheduler-library"), func() {
	var ns *corev1.Namespace

	ginkgo.BeforeAll(func() {
		fwk.StartManager(ctx, cfg, managerSetup())
	})
	ginkgo.AfterAll(func() {
		fwk.StopManager(ctx)
	})

	ginkgo.BeforeEach(func() {
		ns = &corev1.Namespace{}
		ns.GenerateName = "was-"
		gomega.Expect(k8sClient.Create(ctx, ns)).To(gomega.Succeed())
	})
	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
	})

	ginkgo.When("DRA device feasibility filters nodes", func() {
		var (
			topology      *kueue.Topology
			tasFlavor     *kueue.ResourceFlavor
			clusterQueue  *kueue.ClusterQueue
			localQueue    *kueue.LocalQueue
			deviceClass   *resourceapi.DeviceClass
			extendedClass *resourceapi.DeviceClass
			claimTemplate *resourceapi.ResourceClaimTemplate
			tooBigClaim   *resourceapi.ResourceClaimTemplate
			nodes         []corev1.Node
			gpuSlice      *resourceapi.ResourceSlice
		)

		ginkgo.BeforeEach(func() {
			nodes = []corev1.Node{
				*testingnode.MakeNode("was-n1").
					Label("node-group", "was-dra").
					Label(utiltesting.DefaultBlockTopologyLevel, "b1").
					Label(utiltesting.DefaultRackTopologyLevel, "r1").
					Label(corev1.LabelHostname, "was-n1").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("4"),
						corev1.ResourceMemory: resource.MustParse("8Gi"),
						corev1.ResourcePods:   resource.MustParse("10"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("was-n2").
					Label("node-group", "was-dra").
					Label(utiltesting.DefaultBlockTopologyLevel, "b1").
					Label(utiltesting.DefaultRackTopologyLevel, "r2").
					Label(corev1.LabelHostname, "was-n2").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:    resource.MustParse("4"),
						corev1.ResourceMemory: resource.MustParse("8Gi"),
						corev1.ResourcePods:   resource.MustParse("10"),
					}).
					Ready().
					Obj(),
			}
			util.CreateNodesWithStatus(ctx, k8sClient, nodes)

			deviceClass = testingdra.MakeDeviceClass("gpu.test.com").Obj()
			gomega.Expect(k8sClient.Create(ctx, deviceClass)).To(gomega.Succeed())

			// Declares an extended resource instead of being named by a claim. It
			// carries no selectors, so it draws on the same devices as the class above.
			extendedClass = testingdra.MakeDeviceClass("gpu-extended.test.com").
				ExtendedResourceName("test.com/gpu").
				Obj()
			gomega.Expect(k8sClient.Create(ctx, extendedClass)).To(gomega.Succeed())

			// The GPUs sit on was-n2 on purpose. TAS breaks ties by level values,
			// so it picks was-n1 on its own; asserting was-n2 therefore fails
			// unless DRA feasibility actively steered the assignment.
			gpuSlice = utiltesting.MakeResourceSlice("was-n2-gpus", "gpu.test.com").
				NodeName("was-n2").
				Pool("was-n2-gpu-pool", 1, 1).
				Device("gpu-0").
				Device("gpu-1").
				Obj()
			gomega.Expect(k8sClient.Create(ctx, gpuSlice)).To(gomega.Succeed())

			topology = utiltestingapi.MakeDefaultThreeLevelTopology("was-dra-topology")
			gomega.Expect(k8sClient.Create(ctx, topology)).To(gomega.Succeed())

			tasFlavor = utiltestingapi.MakeResourceFlavor("was-dra-flavor").
				NodeLabel("node-group", "was-dra").
				TopologyName("was-dra-topology").Obj()
			gomega.Expect(k8sClient.Create(ctx, tasFlavor)).To(gomega.Succeed())

			clusterQueue = utiltestingapi.MakeClusterQueue("was-dra-cq").
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas(tasFlavor.Name).
					Resource(corev1.ResourceCPU, "10").
					Resource("test-gpus", "4").
					Resource("test.com/gpu", "4").
					Obj()).
				Obj()
			util.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, clusterQueue)

			claimTemplate = utiltesting.MakeResourceClaimTemplate("gpu-claim", ns.Name).
				DeviceRequest("gpu", "gpu.test.com", 1).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, claimTemplate)).To(gomega.Succeed())
			gomega.Eventually(func(g gomega.Gomega) {
				var rct resourceapi.ResourceClaimTemplate
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(claimTemplate), &rct)).To(gomega.Succeed())
			}).Should(gomega.Succeed())

			// Three devices: within the ClusterQueue's quota of four, but more
			// than any single node publishes, so only feasibility can reject it.
			tooBigClaim = utiltesting.MakeResourceClaimTemplate("gpu-claim-too-big", ns.Name).
				DeviceRequest("gpu", "gpu.test.com", 3).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, tooBigClaim)).To(gomega.Succeed())

			localQueue = utiltestingapi.MakeLocalQueue("was-dra-lq", ns.Name).
				ClusterQueue(clusterQueue.Name).Obj()
			util.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, localQueue)
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
			gomega.Expect(util.DeleteObject(ctx, k8sClient, claimTemplate)).Should(gomega.Succeed())
			gomega.Expect(util.DeleteObject(ctx, k8sClient, tooBigClaim)).Should(gomega.Succeed())
			gomega.Expect(util.DeleteObject(ctx, k8sClient, localQueue)).Should(gomega.Succeed())
			util.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, tasFlavor, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, topology, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, gpuSlice, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, deviceClass, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, extendedClass, true)
			for _, node := range nodes {
				util.ExpectObjectToBeDeleted(ctx, k8sClient, &node, true)
			}
		})

		// was-n2 has GPUs (ResourceSlice published), was-n1 does not.
		// DRA feasibility filters was-n1 because it has no matching devices.
		// Without KueueDRADeviceFeasibility, both nodes pass feasibility and
		// TAS falls back to was-n1.
		ginkgo.It("should assign DRA workload only to the node with matching devices", func() {
			wl := utiltestingapi.MakeWorkload("wl-dra", ns.Name).
				Queue(kueue.LocalQueueName(localQueue.Name)).
				Request(corev1.ResourceCPU, "1").
				Obj()
			wl.Spec.PodSets[0].Template.Spec.ResourceClaims = []corev1.PodResourceClaim{
				{
					Name:                      "gpu",
					ResourceClaimTemplateName: new("gpu-claim"),
				},
			}
			wl.Spec.PodSets[0].TopologyRequest = &kueue.PodSetTopologyRequest{
				Required: ptr.To[string](corev1.LabelHostname),
			}
			gomega.Expect(k8sClient.Create(ctx, wl)).To(gomega.Succeed())

			util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl)

			gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), wl)).To(gomega.Succeed())
			ta := utiltas.InternalFrom(wl.Status.Admission.PodSetAssignments[0].TopologyAssignment)
			gomega.Expect(ta.Domains).To(gomega.HaveLen(1))
			gomega.Expect(ta.Domains[0].Values).To(gomega.ContainElement("was-n2"))
		})

		// The Pod names no claim: kube-scheduler would create one for the extended
		// resource only after admission. Feasibility has to derive it, or was-n1 wins
		// the tie and the Pods never run.
		ginkgo.It("should assign an extended resource workload only to the node with matching devices", func() {
			wl := utiltestingapi.MakeWorkload("wl-dra-extended", ns.Name).
				Queue(kueue.LocalQueueName(localQueue.Name)).
				Request(corev1.ResourceCPU, "1").
				Request("test.com/gpu", "1").
				Obj()
			wl.Spec.PodSets[0].TopologyRequest = &kueue.PodSetTopologyRequest{
				Required: ptr.To[string](corev1.LabelHostname),
			}
			gomega.Expect(k8sClient.Create(ctx, wl)).To(gomega.Succeed())

			util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl)

			gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), wl)).To(gomega.Succeed())
			ta := utiltas.InternalFrom(wl.Status.Admission.PodSetAssignments[0].TopologyAssignment)
			gomega.Expect(ta.Domains).To(gomega.HaveLen(1))
			gomega.Expect(ta.Domains[0].Values).To(gomega.ContainElement("was-n2"))
		})

		// The first Workload's domain usage is rebuilt from its PodSet template once it
		// is admitted. Counting its extended resource there would leave the domain
		// short of a resource the Node never advertised, stranding the second.
		ginkgo.It("should admit a second extended resource workload to the same node", func() {
			var wls []*kueue.Workload
			for _, name := range []string{"wl-dra-ext-1", "wl-dra-ext-2"} {
				wl := utiltestingapi.MakeWorkload(name, ns.Name).
					Queue(kueue.LocalQueueName(localQueue.Name)).
					Request(corev1.ResourceCPU, "1").
					Request("test.com/gpu", "1").
					Obj()
				wl.Spec.PodSets[0].TopologyRequest = &kueue.PodSetTopologyRequest{
					Required: ptr.To[string](corev1.LabelHostname),
				}
				gomega.Expect(k8sClient.Create(ctx, wl)).To(gomega.Succeed())
				util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl)
				wls = append(wls, wl)
			}
			for _, wl := range wls {
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), wl)).To(gomega.Succeed())
				ta := utiltas.InternalFrom(wl.Status.Admission.PodSetAssignments[0].TopologyAssignment)
				gomega.Expect(ta.Domains[0].Values).To(gomega.ContainElement("was-n2"))
			}
		})

		ginkgo.It("should not admit a DRA workload when no node has enough devices", func() {
			wl := utiltestingapi.MakeWorkload("wl-dra-too-big", ns.Name).
				Queue(kueue.LocalQueueName(localQueue.Name)).
				Request(corev1.ResourceCPU, "1").
				Obj()
			wl.Spec.PodSets[0].Template.Spec.ResourceClaims = []corev1.PodResourceClaim{
				{
					Name:                      "gpu",
					ResourceClaimTemplateName: new("gpu-claim-too-big"),
				},
			}
			wl.Spec.PodSets[0].TopologyRequest = &kueue.PodSetTopologyRequest{
				Required: ptr.To[string](corev1.LabelHostname),
			}
			gomega.Expect(k8sClient.Create(ctx, wl)).To(gomega.Succeed())

			util.ExpectWorkloadsToBePending(ctx, k8sClient, wl)

			ginkgo.By("reporting the devices as the reason, not a generic no-fit", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					read := kueue.Workload{}
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &read)).To(gomega.Succeed())
					cond := apimeta.FindStatusCondition(read.Status.Conditions, kueue.WorkloadQuotaReserved)
					g.Expect(cond).NotTo(gomega.BeNil())
					g.Expect(cond.Status).To(gomega.Equal(metav1.ConditionFalse))
					g.Expect(cond.Message).To(gomega.ContainSubstring("draNoFit"))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("should admit non-DRA workloads to any node", func() {
			wl := utiltestingapi.MakeWorkload("wl-no-dra", ns.Name).
				Queue(kueue.LocalQueueName(localQueue.Name)).
				Request(corev1.ResourceCPU, "1").
				Obj()
			wl.Spec.PodSets[0].TopologyRequest = &kueue.PodSetTopologyRequest{
				Required: ptr.To[string](corev1.LabelHostname),
			}
			gomega.Expect(k8sClient.Create(ctx, wl)).To(gomega.Succeed())

			util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl)
		})
	})
})
