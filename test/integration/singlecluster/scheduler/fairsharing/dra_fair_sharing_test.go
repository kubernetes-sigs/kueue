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

package fairsharing

import (
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	resourcev1 "k8s.io/api/resource/v1"
	resourceapi "k8s.io/api/resource/v1beta2"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	config "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/test/integration/framework"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("DRA with Admission Fair Sharing", ginkgo.Label("feature:fairsharing", "feature:admissionfairsharing"), func() {
	ginkgo.When("Using AdmissionFairSharing with DRA resources", ginkgo.Ordered, func() {
		var (
			defaultFlavor *kueue.ResourceFlavor
			gpuFlavor     *kueue.ResourceFlavor
			ns            *corev1.Namespace
			deviceClass   *resourceapi.DeviceClass
			rct           *resourcev1.ResourceClaimTemplate

			cqs []*kueue.ClusterQueue
			lqs []*kueue.LocalQueue
			wls []*kueue.Workload
		)

		createWorkloadWithDRA := func(queue string) *kueue.Workload {
			wl := utiltestingapi.MakeWorkloadWithGeneratedName("wl-dra-", ns.Name).
				Queue(kueue.LocalQueueName(queue)).
				PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
					Request(corev1.ResourceCPU, "1").
					ResourceClaimTemplate("gpu-claim", "gpu-claim-template").
					Obj()).
				Obj()

			util.MustCreate(ctx, k8sClient, wl)
			wls = append(wls, wl)
			return wl
		}

		createWorkloadCPUOnly := func(queue string, cpuRequests string) *kueue.Workload {
			wl := utiltestingapi.MakeWorkloadWithGeneratedName("wl-cpu-", ns.Name).
				Queue(kueue.LocalQueueName(queue)).
				Request(corev1.ResourceCPU, cpuRequests).
				Obj()

			util.MustCreate(ctx, k8sClient, wl)
			wls = append(wls, wl)
			return wl
		}

		ginkgo.BeforeAll(func() {
			fwk.StartManager(ctx, cfg, managerAndSchedulerSetup(
				afsConfig(
					metav1.Duration{Duration: 1 * time.Second},
					metav1.Duration{Duration: 5 * time.Second},
				),
				[]config.DeviceClassMapping{
					{
						Name:             corev1.ResourceName("whole-gpus"),
						DeviceClassNames: []corev1.ResourceName{"gpu.example.com"},
					},
				},
			))

			defaultFlavor = utiltestingapi.MakeResourceFlavor("default").Obj()
			util.MustCreate(ctx, k8sClient, defaultFlavor)
			gpuFlavor = utiltestingapi.MakeResourceFlavor("gpu-flavor").Obj()
			util.MustCreate(ctx, k8sClient, gpuFlavor)

			ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "dra-afs-")

			// Create DeviceClass for DRA
			deviceClass = &resourceapi.DeviceClass{
				ObjectMeta: metav1.ObjectMeta{
					Name: "gpu.example.com",
				},
			}
			util.MustCreate(ctx, k8sClient, deviceClass)

			// Create ResourceClaimTemplate
			rct = utiltesting.MakeResourceClaimTemplate("gpu-claim-template", ns.Name).
				DeviceRequest("gpu", "gpu.example.com", 1).
				Obj()
			util.MustCreate(ctx, k8sClient, rct)
		})

		ginkgo.AfterAll(func() {
			util.ExpectObjectToBeDeleted(ctx, k8sClient, deviceClass, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, rct, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, gpuFlavor, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, defaultFlavor, true)
			fwk.StopManager(ctx)
		})

		ginkgo.AfterEach(func() {
			for _, wl := range wls {
				util.ExpectObjectToBeDeleted(ctx, k8sClient, wl, true)
			}
			for _, lq := range lqs {
				util.ExpectObjectToBeDeleted(ctx, k8sClient, lq, true)
			}
			for _, cq := range cqs {
				util.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
			}
		})

		ginkgo.It("DRA resource usage affects AFS admission ordering", framework.SlowSpec, func() {
			cq := utiltestingapi.MakeClusterQueue("cq").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas(defaultFlavor.Name).
						Resource(corev1.ResourceCPU, "5").
						Obj(),
				).
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas(gpuFlavor.Name).
						Resource("whole-gpus", "2").
						Obj(),
				).
				AdmissionMode(kueue.UsageBasedAdmissionFairSharing).
				Obj()
			cqs = append(cqs, cq)
			util.MustCreate(ctx, k8sClient, cq)

			lqA := utiltestingapi.MakeLocalQueue("lq-a", ns.Name).
				FairSharing(&kueue.FairSharing{Weight: new(resource.MustParse("1"))}).
				ClusterQueue(cq.Name).Obj()
			lqB := utiltestingapi.MakeLocalQueue("lq-b", ns.Name).
				FairSharing(&kueue.FairSharing{Weight: new(resource.MustParse("1"))}).
				ClusterQueue(cq.Name).Obj()
			lqs = append(lqs, lqA, lqB)
			util.MustCreate(ctx, k8sClient, lqA)
			util.MustCreate(ctx, k8sClient, lqB)

			ginkgo.By("Building usage history: DRA workloads on lq-a (CPU=2,GPU=2), CPU workload on lq-b (CPU=3)")
			wlA1 := createWorkloadWithDRA("lq-a")
			wlA2 := createWorkloadWithDRA("lq-a")
			wlBInit := createWorkloadCPUOnly("lq-b", "3")

			ginkgo.By("Waiting for all initial workloads to be admitted")
			util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wlA1, wlA2, wlBInit)

			ginkgo.By("Waiting for AFS ConsumedResources to reflect the admitted workloads")
			gomega.Eventually(func(g gomega.Gomega) {
				var lqAObj, lqBObj kueue.LocalQueue

				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(lqA), &lqAObj)).To(gomega.Succeed())
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(lqB), &lqBObj)).To(gomega.Succeed())

				g.Expect(lqAObj.Status.FairSharing).NotTo(gomega.BeNil())
				g.Expect(lqBObj.Status.FairSharing).NotTo(gomega.BeNil())

				g.Expect(lqAObj.Status.FairSharing.AdmissionFairSharingStatus).NotTo(gomega.BeNil())
				g.Expect(lqBObj.Status.FairSharing.AdmissionFairSharingStatus).NotTo(gomega.BeNil())

				lqAConsumed := lqAObj.Status.FairSharing.AdmissionFairSharingStatus.ConsumedResources
				lqBConsumed := lqBObj.Status.FairSharing.AdmissionFairSharingStatus.ConsumedResources

				g.Expect(lqAConsumed).To(gomega.HaveKey(corev1.ResourceCPU))
				g.Expect(lqAConsumed).To(gomega.HaveKey(corev1.ResourceName("whole-gpus")))
				g.Expect(lqBConsumed).To(gomega.HaveKey(corev1.ResourceCPU))

				lqACPU := lqAConsumed[corev1.ResourceCPU]
				lqAGPU := lqAConsumed["whole-gpus"]
				lqBCPU := lqBConsumed[corev1.ResourceCPU]

				lqATotal := lqACPU.AsApproximateFloat64() + lqAGPU.AsApproximateFloat64()
				lqBTotal := lqBCPU.AsApproximateFloat64()

				g.Expect(lqATotal).To(gomega.BeNumerically(">", lqBTotal),
					"expected lq-a usage (CPU+GPU=%v) > lq-b usage (CPU=%v) before phase 2",
					lqATotal, lqBTotal,
				)
			}, util.Timeout, util.ShortInterval).Should(gomega.Succeed())

			ginkgo.By("Finishing all initial workloads")
			util.FinishWorkloads(ctx, k8sClient, wlA1, wlA2, wlBInit)

			ginkgo.By("Verify the usage remains positive after a while since the workloads are finished")
			gomega.Consistently(func(g gomega.Gomega) {
				var lqAObj, lqBObj kueue.LocalQueue

				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(lqA), &lqAObj)).To(gomega.Succeed())
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(lqB), &lqBObj)).To(gomega.Succeed())

				g.Expect(lqAObj.Status.FairSharing).NotTo(gomega.BeNil())
				g.Expect(lqBObj.Status.FairSharing).NotTo(gomega.BeNil())

				g.Expect(lqAObj.Status.FairSharing.AdmissionFairSharingStatus).NotTo(gomega.BeNil())
				g.Expect(lqBObj.Status.FairSharing.AdmissionFairSharingStatus).NotTo(gomega.BeNil())

				lqAConsumed := lqAObj.Status.FairSharing.AdmissionFairSharingStatus.ConsumedResources
				lqBConsumed := lqBObj.Status.FairSharing.AdmissionFairSharingStatus.ConsumedResources

				g.Expect(lqAConsumed).To(gomega.HaveKey(corev1.ResourceCPU))
				g.Expect(lqAConsumed).To(gomega.HaveKey(corev1.ResourceName("whole-gpus")))
				g.Expect(lqBConsumed).To(gomega.HaveKey(corev1.ResourceCPU))

				lqACPU := lqAConsumed[corev1.ResourceCPU]
				lqAGPU := lqAConsumed["whole-gpus"]
				lqBCPU := lqBConsumed[corev1.ResourceCPU]

				lqATotal := lqACPU.AsApproximateFloat64() + lqAGPU.AsApproximateFloat64()
				lqBTotal := lqBCPU.AsApproximateFloat64()

				g.Expect(lqATotal).To(gomega.BeNumerically(">", 0))
				g.Expect(lqBTotal).To(gomega.BeNumerically(">", 0))
			}, util.ConsistentDuration, util.ShortInterval).Should(gomega.Succeed())

			// With DRA counted: lq-a=CPU(2)+GPU(2)=4 > lq-b=CPU(3) → lq-b admitted first.
			// Without DRA: lq-a=CPU(2) < lq-b=CPU(3) → lq-a admitted first (wrong).
			ginkgo.By("Creating competing workloads from both LQs")
			wlB1 := createWorkloadCPUOnly("lq-b", "5")
			wlA3 := createWorkloadCPUOnly("lq-a", "5")

			ginkgo.By("Verifying lq-b workload is admitted first (lq-a has higher total usage including DRA)")
			util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wlB1)

			ginkgo.By("Finishing lq-b workload to free quota")
			util.FinishWorkloads(ctx, k8sClient, wlB1)

			ginkgo.By("Verifying lq-a workload is admitted after quota is freed")
			util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wlA3)
		})

		ginkgo.It("LocalQueue status ConsumedResources includes DRA logical resources", framework.SlowSpec, func() {
			cq := utiltestingapi.MakeClusterQueue("cq-consumed").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas(defaultFlavor.Name).
						Resource(corev1.ResourceCPU, "10").
						Obj(),
				).
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas(gpuFlavor.Name).
						Resource("whole-gpus", "4").
						Obj(),
				).
				AdmissionMode(kueue.UsageBasedAdmissionFairSharing).
				Obj()
			cqs = append(cqs, cq)
			util.MustCreate(ctx, k8sClient, cq)

			lqA := utiltestingapi.MakeLocalQueue("lq-consumed", ns.Name).
				FairSharing(&kueue.FairSharing{Weight: new(resource.MustParse("1"))}).
				ClusterQueue(cq.Name).Obj()
			lqs = append(lqs, lqA)
			util.MustCreate(ctx, k8sClient, lqA)

			ginkgo.By("Creating DRA workload")
			wl := createWorkloadWithDRA("lq-consumed")
			util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl)

			ginkgo.By("Verifying ConsumedResources includes DRA logical resource")
			gomega.Eventually(func(g gomega.Gomega) {
				var lq kueue.LocalQueue
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(lqA), &lq)).To(gomega.Succeed())
				g.Expect(lq.Status.FairSharing).NotTo(gomega.BeNil())
				g.Expect(lq.Status.FairSharing.AdmissionFairSharingStatus).NotTo(gomega.BeNil())
				consumed := lq.Status.FairSharing.AdmissionFairSharingStatus.ConsumedResources
				g.Expect(consumed).NotTo(gomega.BeEmpty())
				// Check that the DRA logical resource "whole-gpus" is tracked
				gpuQuantity, hasGPU := consumed["whole-gpus"]
				g.Expect(hasGPU).To(gomega.BeTrue(), "ConsumedResources should include DRA logical resource 'whole-gpus'")
				g.Expect(gpuQuantity.Cmp(resource.MustParse("0"))).To(gomega.BeNumerically(">", 0), "whole-gpus consumed should be positive")
			}, util.Timeout, util.ShortInterval).Should(gomega.Succeed())
		})
	})

	ginkgo.When("AdmissionFairSharing.ResourceWeights applies weight to DRA logical resources", ginkgo.Ordered, func() {
		var (
			defaultFlavor *kueue.ResourceFlavor
			gpuFlavor     *kueue.ResourceFlavor
			ns            *corev1.Namespace
			deviceClass   *resourceapi.DeviceClass
			rct           *resourcev1.ResourceClaimTemplate

			cqs []*kueue.ClusterQueue
			lqs []*kueue.LocalQueue
			wls []*kueue.Workload
		)

		ginkgo.BeforeAll(func() {
			fwk.StartManager(ctx, cfg, managerAndSchedulerSetup(
				&config.AdmissionFairSharing{
					UsageSamplingInterval: metav1.Duration{Duration: 1 * time.Second},
					UsageHalfLifeTime:     metav1.Duration{Duration: 1 * time.Second},
					ResourceWeights: map[corev1.ResourceName]float64{
						"whole-gpus": 5.0, // Weight DRA logical resource via existing resourceWeights
					},
				},
				[]config.DeviceClassMapping{
					{
						Name:             corev1.ResourceName("whole-gpus"),
						DeviceClassNames: []corev1.ResourceName{"gpu.example.com"},
					},
				},
			))

			defaultFlavor = utiltestingapi.MakeResourceFlavor("default").Obj()
			util.MustCreate(ctx, k8sClient, defaultFlavor)
			gpuFlavor = utiltestingapi.MakeResourceFlavor("gpu-flavor").Obj()
			util.MustCreate(ctx, k8sClient, gpuFlavor)

			ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "dra-afs-")

			// Create DeviceClass for DRA
			deviceClass = &resourceapi.DeviceClass{
				ObjectMeta: metav1.ObjectMeta{
					Name: "gpu.example.com",
				},
			}
			util.MustCreate(ctx, k8sClient, deviceClass)

			// Create ResourceClaimTemplate
			rct = utiltesting.MakeResourceClaimTemplate("gpu-claim-template", ns.Name).
				DeviceRequest("gpu", "gpu.example.com", 1).
				Obj()
			util.MustCreate(ctx, k8sClient, rct)
		})

		ginkgo.AfterAll(func() {
			util.ExpectObjectToBeDeleted(ctx, k8sClient, deviceClass, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, rct, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, gpuFlavor, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, defaultFlavor, true)
			fwk.StopManager(ctx)
		})

		ginkgo.AfterEach(func() {
			for _, wl := range wls {
				util.ExpectObjectToBeDeleted(ctx, k8sClient, wl, true)
			}
			for _, lq := range lqs {
				util.ExpectObjectToBeDeleted(ctx, k8sClient, lq, true)
			}
			for _, cq := range cqs {
				util.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
			}
		})

		ginkgo.It("should prioritize lower weighted usage", framework.SlowSpec, func() {
			cq := utiltestingapi.MakeClusterQueue("cq-weight").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas(defaultFlavor.Name).
						Resource(corev1.ResourceCPU, "4").
						Obj(),
				).
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas(gpuFlavor.Name).
						Resource("whole-gpus", "4").
						Obj(),
				).
				AdmissionMode(kueue.UsageBasedAdmissionFairSharing).
				Obj()
			cqs = append(cqs, cq)
			util.MustCreate(ctx, k8sClient, cq)

			lqA := utiltestingapi.MakeLocalQueue("lq-a", ns.Name).
				FairSharing(&kueue.FairSharing{Weight: new(resource.MustParse("1"))}).
				ClusterQueue(cq.Name).Obj()
			lqB := utiltestingapi.MakeLocalQueue("lq-b", ns.Name).
				FairSharing(&kueue.FairSharing{Weight: new(resource.MustParse("1"))}).
				ClusterQueue(cq.Name).Obj()
			lqs = append(lqs, lqA, lqB)
			util.MustCreate(ctx, k8sClient, lqA)
			util.MustCreate(ctx, k8sClient, lqB)

			ginkgo.By("Building usage history: DRA workload on lq-a (GPU weighted 5.0), CPU workloads on lq-b")
			wlA1 := utiltestingapi.MakeWorkloadWithGeneratedName("wl-dra-", ns.Name).
				Queue(kueue.LocalQueueName("lq-a")).
				PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
					Request(corev1.ResourceCPU, "1").
					ResourceClaimTemplate("gpu-claim", "gpu-claim-template").
					Obj()).
				Obj()
			util.MustCreate(ctx, k8sClient, wlA1)
			wls = append(wls, wlA1)

			wlB1 := utiltestingapi.MakeWorkloadWithGeneratedName("wl-cpu-", ns.Name).
				Queue(kueue.LocalQueueName("lq-b")).
				Request(corev1.ResourceCPU, "1").
				Obj()
			util.MustCreate(ctx, k8sClient, wlB1)
			wls = append(wls, wlB1)

			wlB2 := utiltestingapi.MakeWorkloadWithGeneratedName("wl-cpu-", ns.Name).
				Queue(kueue.LocalQueueName("lq-b")).
				Request(corev1.ResourceCPU, "1").
				Obj()
			util.MustCreate(ctx, k8sClient, wlB2)
			wls = append(wls, wlB2)

			wlB3 := utiltestingapi.MakeWorkloadWithGeneratedName("wl-cpu-", ns.Name).
				Queue(kueue.LocalQueueName("lq-b")).
				Request(corev1.ResourceCPU, "1").
				Obj()
			util.MustCreate(ctx, k8sClient, wlB3)
			wls = append(wls, wlB3)

			ginkgo.By("Waiting for all initial workloads to be admitted")
			util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wlA1, wlB1, wlB2, wlB3)

			ginkgo.By("Finishing all initial workloads to build AFS history")
			util.FinishWorkloads(ctx, k8sClient, wlA1, wlB1, wlB2, wlB3)

			// With weight 5.0: lq-a=CPU(1)*1+GPU(1)*5=6 > lq-b=CPU(3)*1=3 → lq-b admitted first.
			// Without weight: lq-a=CPU(1)+GPU(1)=2 < lq-b=CPU(3)=3 → lq-a admitted first (wrong).
			ginkgo.By("Creating competing workloads to verify GPU weight affects ordering")
			wlB4 := utiltestingapi.MakeWorkloadWithGeneratedName("wl-cpu-", ns.Name).
				Queue(kueue.LocalQueueName("lq-b")).
				Request(corev1.ResourceCPU, "4").
				Obj()
			util.MustCreate(ctx, k8sClient, wlB4)
			wls = append(wls, wlB4)

			wlA2 := utiltestingapi.MakeWorkloadWithGeneratedName("wl-cpu-", ns.Name).
				Queue(kueue.LocalQueueName("lq-a")).
				Request(corev1.ResourceCPU, "4").
				Obj()
			util.MustCreate(ctx, k8sClient, wlA2)
			wls = append(wls, wlA2)

			ginkgo.By("Verifying lq-b workload is admitted first (lq-a has higher weighted DRA usage)")
			util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wlB4)

			ginkgo.By("Finishing lq-b workload to free quota")
			util.FinishWorkloads(ctx, k8sClient, wlB4)

			ginkgo.By("Verifying lq-a workload is admitted after quota is freed")
			util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wlA2)
		})
	})
})
