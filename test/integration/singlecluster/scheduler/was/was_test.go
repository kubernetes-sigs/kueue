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
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	utiltas "sigs.k8s.io/kueue/pkg/util/tas"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingdra "sigs.k8s.io/kueue/pkg/util/testingjobs/dra"
	testingnode "sigs.k8s.io/kueue/pkg/util/testingjobs/node"
	testingpod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
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
			tolerantClaim *resourceapi.ResourceClaimTemplate
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

			// Tolerates the taint the DeviceTaintRule cases below apply.
			tolerantClaim = utiltesting.MakeResourceClaimTemplate("gpu-claim-tolerant", ns.Name).
				DeviceRequest("gpu", "gpu.test.com", 1).
				WithToleration("test.com/maintenance", resourceapi.DeviceTaintEffectNoSchedule).
				Obj()
			util.MustCreate(ctx, k8sClient, tolerantClaim)

			localQueue = utiltestingapi.MakeLocalQueue("was-dra-lq", ns.Name).
				ClusterQueue(clusterQueue.Name).Obj()
			util.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, localQueue)
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
			gomega.Expect(util.DeleteObject(ctx, k8sClient, claimTemplate)).Should(gomega.Succeed())
			gomega.Expect(util.DeleteObject(ctx, k8sClient, tooBigClaim)).Should(gomega.Succeed())
			gomega.Expect(util.DeleteObject(ctx, k8sClient, tolerantClaim)).Should(gomega.Succeed())
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
				PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
					Request(corev1.ResourceCPU, "1").
					ResourceClaimTemplate("gpu", "gpu-claim").
					RequiredTopologyRequest(corev1.LabelHostname).
					Obj()).
				Obj()
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
				PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
					Request(corev1.ResourceCPU, "1").
					Request("test.com/gpu", "1").
					RequiredTopologyRequest(corev1.LabelHostname).
					Obj()).
				Obj()
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
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
						Request(corev1.ResourceCPU, "1").
						Request("test.com/gpu", "1").
						RequiredTopologyRequest(corev1.LabelHostname).
						Obj()).
					Obj()
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
				PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
					Request(corev1.ResourceCPU, "1").
					ResourceClaimTemplate("gpu", "gpu-claim-too-big").
					RequiredTopologyRequest(corev1.LabelHostname).
					Obj()).
				Obj()
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

		// The taint is on the devices, not the node, so only the DRA check can see it.
		ginkgo.When("a DeviceTaintRule taints the only node with devices", func() {
			var taintRule *resourceapi.DeviceTaintRule

			ginkgo.BeforeEach(func() {
				taintRule = utiltesting.MakeDeviceTaintRule("was-dra-maintenance", "test.com/maintenance").
					Driver("gpu.test.com").
					Obj()
				util.MustCreate(ctx, k8sClient, taintRule)
				// The rule's name repeats across specs, so wait for this rule, not a previous one.
				gomega.Eventually(func(g gomega.Gomega) {
					var cached resourceapi.DeviceTaintRule
					g.Expect(managerClient.Get(ctx, client.ObjectKeyFromObject(taintRule), &cached)).To(gomega.Succeed())
					g.Expect(cached.UID).To(gomega.Equal(taintRule.UID))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.AfterEach(func() {
				util.ExpectObjectToBeDeleted(ctx, k8sClient, taintRule, true)
			})

			ginkgo.It("should not admit a DRA workload that does not tolerate the taint", func() {
				wl := utiltestingapi.MakeWorkload("wl-dra-tainted", ns.Name).
					Queue(kueue.LocalQueueName(localQueue.Name)).
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
						Request(corev1.ResourceCPU, "1").
						ResourceClaimTemplate("gpu", "gpu-claim").
						RequiredTopologyRequest(corev1.LabelHostname).
						Obj()).
					Obj()
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

			ginkgo.It("should admit a DRA workload whose claim tolerates the taint", func() {
				wl := utiltestingapi.MakeWorkload("wl-dra-tolerant", ns.Name).
					Queue(kueue.LocalQueueName(localQueue.Name)).
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
						Request(corev1.ResourceCPU, "1").
						ResourceClaimTemplate("gpu", "gpu-claim-tolerant").
						RequiredTopologyRequest(corev1.LabelHostname).
						Obj()).
					Obj()
				gomega.Expect(k8sClient.Create(ctx, wl)).To(gomega.Succeed())

				util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl)

				gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), wl)).To(gomega.Succeed())
				ta := utiltas.InternalFrom(wl.Status.Admission.PodSetAssignments[0].TopologyAssignment)
				gomega.Expect(ta.Domains).To(gomega.HaveLen(1))
				gomega.Expect(ta.Domains[0].Values).To(gomega.ContainElement("was-n2"))
			})

			ginkgo.It("should admit a pending DRA workload once the rule is deleted", func() {
				wl := utiltestingapi.MakeWorkload("wl-dra-untainted", ns.Name).
					Queue(kueue.LocalQueueName(localQueue.Name)).
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
						Request(corev1.ResourceCPU, "1").
						ResourceClaimTemplate("gpu", "gpu-claim").
						RequiredTopologyRequest(corev1.LabelHostname).
						Obj()).
					Obj()
				util.MustCreate(ctx, k8sClient, wl)
				ginkgo.By("waiting for the devices to reject it", func() {
					gomega.Eventually(func(g gomega.Gomega) {
						read := kueue.Workload{}
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &read)).To(gomega.Succeed())
						cond := apimeta.FindStatusCondition(read.Status.Conditions, kueue.WorkloadQuotaReserved)
						g.Expect(cond).NotTo(gomega.BeNil())
						g.Expect(cond.Status).To(gomega.Equal(metav1.ConditionFalse))
						g.Expect(cond.Message).To(gomega.ContainSubstring("draNoFit"))
					}, util.Timeout, util.Interval).Should(gomega.Succeed())
				})

				util.ExpectObjectToBeDeleted(ctx, k8sClient, taintRule, true)

				util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl)
			})
		})

		// Nothing else changes after the rejection, so only the DRA event can requeue the Workload.
		ginkgo.When("the devices a pending DRA workload needs become available", func() {
			var (
				extraSlice *resourceapi.ResourceSlice
				heldClaim  *resourceapi.ResourceClaim
			)

			ginkgo.BeforeEach(func() {
				extraSlice, heldClaim = nil, nil
			})

			ginkgo.AfterEach(func() {
				util.ExpectObjectToBeDeleted(ctx, k8sClient, heldClaim, true)
				util.ExpectObjectToBeDeleted(ctx, k8sClient, extraSlice, true)
			})

			ginkgo.It("should admit it once a ResourceSlice publishes more devices", func() {
				wl := utiltestingapi.MakeWorkload("wl-dra-more-devices", ns.Name).
					Queue(kueue.LocalQueueName(localQueue.Name)).
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
						Request(corev1.ResourceCPU, "1").
						ResourceClaimTemplate("gpu", "gpu-claim-too-big").
						RequiredTopologyRequest(corev1.LabelHostname).
						Obj()).
					Obj()
				util.MustCreate(ctx, k8sClient, wl)
				ginkgo.By("waiting for the devices to reject it", func() {
					gomega.Eventually(func(g gomega.Gomega) {
						read := kueue.Workload{}
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &read)).To(gomega.Succeed())
						cond := apimeta.FindStatusCondition(read.Status.Conditions, kueue.WorkloadQuotaReserved)
						g.Expect(cond).NotTo(gomega.BeNil())
						g.Expect(cond.Status).To(gomega.Equal(metav1.ConditionFalse))
						g.Expect(cond.Message).To(gomega.ContainSubstring("draNoFit"))
					}, util.Timeout, util.Interval).Should(gomega.Succeed())
				})

				extraSlice = utiltesting.MakeResourceSlice("was-n2-more-gpus", "gpu.test.com").
					NodeName("was-n2").
					Pool("was-n2-more-gpu-pool", 1, 1).
					Device("gpu-2").
					Obj()
				util.MustCreate(ctx, k8sClient, extraSlice)

				util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl)
			})

			ginkgo.It("should admit it once a ResourceSlice update adds devices", func() {
				wl := utiltestingapi.MakeWorkload("wl-dra-updated-slice", ns.Name).
					Queue(kueue.LocalQueueName(localQueue.Name)).
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
						Request(corev1.ResourceCPU, "1").
						ResourceClaimTemplate("gpu", "gpu-claim-too-big").
						RequiredTopologyRequest(corev1.LabelHostname).
						Obj()).
					Obj()
				util.MustCreate(ctx, k8sClient, wl)
				ginkgo.By("waiting for the devices to reject it", func() {
					gomega.Eventually(func(g gomega.Gomega) {
						read := kueue.Workload{}
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &read)).To(gomega.Succeed())
						cond := apimeta.FindStatusCondition(read.Status.Conditions, kueue.WorkloadQuotaReserved)
						g.Expect(cond).NotTo(gomega.BeNil())
						g.Expect(cond.Status).To(gomega.Equal(metav1.ConditionFalse))
						g.Expect(cond.Message).To(gomega.ContainSubstring("draNoFit"))
					}, util.Timeout, util.Interval).Should(gomega.Succeed())
				})

				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(gpuSlice), gpuSlice)).To(gomega.Succeed())
					gpuSlice.Spec.Devices = utiltesting.MakeResourceSlice(gpuSlice.Name, "gpu.test.com").
						Device("gpu-0").
						Device("gpu-1").
						Device("gpu-2").
						Obj().Spec.Devices
					g.Expect(k8sClient.Update(ctx, gpuSlice)).To(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())

				util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl)
			})

			ginkgo.It("should admit it once a ResourceClaim releases its devices", func() {
				heldClaim = utiltesting.MakeResourceClaim("was-held", ns.Name).DeviceRequest("gpu", "gpu.test.com", 2).Obj()
				util.MustCreate(ctx, k8sClient, heldClaim)
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(heldClaim), heldClaim)).To(gomega.Succeed())
					heldClaim.Status = utiltesting.MakeResourceClaim("was-held", ns.Name).
						Allocated("gpu", "gpu.test.com", "was-n2-gpu-pool", "gpu-0", "gpu-1").
						Obj().Status
					g.Expect(k8sClient.Status().Update(ctx, heldClaim)).To(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
				gomega.Eventually(func(g gomega.Gomega) {
					var cached resourceapi.ResourceClaim
					g.Expect(managerClient.Get(ctx, client.ObjectKeyFromObject(heldClaim), &cached)).To(gomega.Succeed())
					g.Expect(cached.Status.Allocation).NotTo(gomega.BeNil())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())

				wl := utiltestingapi.MakeWorkload("wl-dra-released", ns.Name).
					Queue(kueue.LocalQueueName(localQueue.Name)).
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
						Request(corev1.ResourceCPU, "1").
						ResourceClaimTemplate("gpu", "gpu-claim").
						RequiredTopologyRequest(corev1.LabelHostname).
						Obj()).
					Obj()
				util.MustCreate(ctx, k8sClient, wl)
				ginkgo.By("waiting for the devices to reject it", func() {
					gomega.Eventually(func(g gomega.Gomega) {
						read := kueue.Workload{}
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &read)).To(gomega.Succeed())
						cond := apimeta.FindStatusCondition(read.Status.Conditions, kueue.WorkloadQuotaReserved)
						g.Expect(cond).NotTo(gomega.BeNil())
						g.Expect(cond.Status).To(gomega.Equal(metav1.ConditionFalse))
						g.Expect(cond.Message).To(gomega.ContainSubstring("draNoFit"))
					}, util.Timeout, util.Interval).Should(gomega.Succeed())
				})

				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(heldClaim), heldClaim)).To(gomega.Succeed())
					heldClaim.Status.Allocation = nil
					g.Expect(k8sClient.Status().Update(ctx, heldClaim)).To(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())

				util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl)
			})

			ginkgo.It("should admit it once the DeviceClass selects the devices", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(deviceClass), deviceClass)).To(gomega.Succeed())
					deviceClass.Spec.Selectors = testingdra.MakeDeviceClass(deviceClass.Name).CELSelector(`device.driver == "other.test.com"`).Obj().Spec.Selectors
					g.Expect(k8sClient.Update(ctx, deviceClass)).To(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
				gomega.Eventually(func(g gomega.Gomega) {
					var cached resourceapi.DeviceClass
					g.Expect(managerClient.Get(ctx, client.ObjectKeyFromObject(deviceClass), &cached)).To(gomega.Succeed())
					g.Expect(cached.Spec.Selectors).To(gomega.HaveLen(1))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())

				wl := utiltestingapi.MakeWorkload("wl-dra-reselected", ns.Name).
					Queue(kueue.LocalQueueName(localQueue.Name)).
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
						Request(corev1.ResourceCPU, "1").
						ResourceClaimTemplate("gpu", "gpu-claim").
						RequiredTopologyRequest(corev1.LabelHostname).
						Obj()).
					Obj()
				util.MustCreate(ctx, k8sClient, wl)
				ginkgo.By("waiting for the devices to reject it", func() {
					gomega.Eventually(func(g gomega.Gomega) {
						read := kueue.Workload{}
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &read)).To(gomega.Succeed())
						cond := apimeta.FindStatusCondition(read.Status.Conditions, kueue.WorkloadQuotaReserved)
						g.Expect(cond).NotTo(gomega.BeNil())
						g.Expect(cond.Status).To(gomega.Equal(metav1.ConditionFalse))
						g.Expect(cond.Message).To(gomega.ContainSubstring("draNoFit"))
					}, util.Timeout, util.Interval).Should(gomega.Succeed())
				})

				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(deviceClass), deviceClass)).To(gomega.Succeed())
					deviceClass.Spec.Selectors = nil
					g.Expect(k8sClient.Update(ctx, deviceClass)).To(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())

				util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl)
			})

			// Of two DeviceClasses for one extended resource the newer resolves, so deleting it
			// switches the Workload to the other one.
			ginkgo.It("should admit it once the DeviceClass it resolved to is deleted", func() {
				brokenClass := testingdra.MakeDeviceClass("gpu-extended-broken.test.com").
					ExtendedResourceName("test.com/gpu").
					CELSelector(`device.driver == "other.test.com"`).
					Obj()
				util.MustCreate(ctx, k8sClient, brokenClass)
				gomega.Eventually(func(g gomega.Gomega) {
					var cached resourceapi.DeviceClass
					g.Expect(managerClient.Get(ctx, client.ObjectKeyFromObject(brokenClass), &cached)).To(gomega.Succeed())
					g.Expect(cached.UID).To(gomega.Equal(brokenClass.UID))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())

				wl := utiltestingapi.MakeWorkload("wl-dra-ext-reresolved", ns.Name).
					Queue(kueue.LocalQueueName(localQueue.Name)).
					PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
						Request(corev1.ResourceCPU, "1").
						Request("test.com/gpu", "1").
						RequiredTopologyRequest(corev1.LabelHostname).
						Obj()).
					Obj()
				util.MustCreate(ctx, k8sClient, wl)
				ginkgo.By("waiting for the devices to reject it", func() {
					gomega.Eventually(func(g gomega.Gomega) {
						read := kueue.Workload{}
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &read)).To(gomega.Succeed())
						cond := apimeta.FindStatusCondition(read.Status.Conditions, kueue.WorkloadQuotaReserved)
						g.Expect(cond).NotTo(gomega.BeNil())
						g.Expect(cond.Status).To(gomega.Equal(metav1.ConditionFalse))
						g.Expect(cond.Message).To(gomega.ContainSubstring("draNoFit"))
					}, util.Timeout, util.Interval).Should(gomega.Succeed())
				})

				// Creating the class requeues too, a batch period later, so hold on until that
				// retry has run with the class still there.
				gomega.Consistently(func(g gomega.Gomega) {
					read := kueue.Workload{}
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &read)).To(gomega.Succeed())
					g.Expect(apimeta.IsStatusConditionTrue(read.Status.Conditions, kueue.WorkloadQuotaReserved)).To(gomega.BeFalse())
				}, util.LongConsistentDuration, util.ShortInterval).Should(gomega.Succeed())

				util.ExpectObjectToBeDeleted(ctx, k8sClient, brokenClass, true)

				util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl)
			})
		})

		ginkgo.It("should admit non-DRA workloads to any node", func() {
			wl := utiltestingapi.MakeWorkload("wl-no-dra", ns.Name).
				Queue(kueue.LocalQueueName(localQueue.Name)).
				PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
					Request(corev1.ResourceCPU, "1").
					RequiredTopologyRequest(corev1.LabelHostname).
					Obj()).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, wl)).To(gomega.Succeed())

			util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl)
		})
	})

	ginkgo.When("host ports filter nodes", func() {
		var (
			topology     *kueue.Topology
			tasFlavor    *kueue.ResourceFlavor
			clusterQueue *kueue.ClusterQueue
			localQueue   *kueue.LocalQueue
			nodes        []corev1.Node
		)

		ginkgo.BeforeEach(func() {
			nodes = []corev1.Node{
				*testingnode.MakeNode("was-n1").
					Label("node-group", "was-ports").
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
					Label("node-group", "was-ports").
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

			// A Pod outside Kueue holds hostPort 8080 on was-n1, the node TAS
			// picks on its own. It is created before the queues so that its
			// tracking by the simulator doesn't race the Workload's admission.
			holder := testingpod.MakePod("port-holder", ns.Name).
				NodeName("was-n1").
				Port(8080, 8080, corev1.ProtocolTCP).
				TerminationGracePeriod(0).
				Obj()
			util.MustCreate(ctx, k8sClient, holder)
			gomega.Eventually(func(g gomega.Gomega) {
				var cached corev1.Pod
				g.Expect(managerClient.Get(ctx, client.ObjectKeyFromObject(holder), &cached)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			topology = utiltestingapi.MakeDefaultThreeLevelTopology("was-ports-topology")
			gomega.Expect(k8sClient.Create(ctx, topology)).To(gomega.Succeed())

			tasFlavor = utiltestingapi.MakeResourceFlavor("was-ports-flavor").
				NodeLabel("node-group", "was-ports").
				TopologyName("was-ports-topology").Obj()
			gomega.Expect(k8sClient.Create(ctx, tasFlavor)).To(gomega.Succeed())

			clusterQueue = utiltestingapi.MakeClusterQueue("was-ports-cq").
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas(tasFlavor.Name).
					Resource(corev1.ResourceCPU, "10").
					Obj()).
				Obj()
			util.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, clusterQueue)

			localQueue = utiltestingapi.MakeLocalQueue("was-ports-lq", ns.Name).
				ClusterQueue(clusterQueue.Name).Obj()
			util.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, localQueue)
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
			gomega.Expect(util.DeleteObject(ctx, k8sClient, localQueue)).Should(gomega.Succeed())
			util.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, tasFlavor, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, topology, true)
			for _, node := range nodes {
				util.ExpectObjectToBeDeleted(ctx, k8sClient, &node, true)
			}
		})

		ginkgo.It("should assign the workload to another node when its hostPort is taken", func() {
			wl := utiltestingapi.MakeWorkload("wl-port-taken", ns.Name).
				Queue(kueue.LocalQueueName(localQueue.Name)).
				PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
					Containers(*utiltesting.MakeContainer().
						Name("c").
						WithResourceReq(corev1.ResourceCPU, "1").
						Port(8080, 8080, corev1.ProtocolTCP).
						Obj()).
					RequiredTopologyRequest(corev1.LabelHostname).
					Obj()).
				Obj()
			util.MustCreate(ctx, k8sClient, wl)

			util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl)

			gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), wl)).To(gomega.Succeed())
			ta := utiltas.InternalFrom(wl.Status.Admission.PodSetAssignments[0].TopologyAssignment)
			gomega.Expect(ta.Domains[0].Values).To(gomega.ContainElement("was-n2"))
		})

		ginkgo.It("should assign the workload to was-n1 when its hostPort is free", func() {
			wl := utiltestingapi.MakeWorkload("wl-port-free", ns.Name).
				Queue(kueue.LocalQueueName(localQueue.Name)).
				PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
					Containers(*utiltesting.MakeContainer().
						Name("c").
						WithResourceReq(corev1.ResourceCPU, "1").
						Port(9090, 9090, corev1.ProtocolTCP).
						Obj()).
					RequiredTopologyRequest(corev1.LabelHostname).
					Obj()).
				Obj()
			util.MustCreate(ctx, k8sClient, wl)

			util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl)

			gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), wl)).To(gomega.Succeed())
			ta := utiltas.InternalFrom(wl.Status.Admission.PodSetAssignments[0].TopologyAssignment)
			gomega.Expect(ta.Domains[0].Values).To(gomega.ContainElement("was-n1"))
		})
	})
})
