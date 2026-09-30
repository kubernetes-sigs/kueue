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

package dra

import (
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	resourcev1 "k8s.io/api/resource/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	config "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingdra "sigs.k8s.io/kueue/pkg/util/testingjobs/dra"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("DRA Prioritized List Integration", ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	ginkgo.BeforeAll(func() {
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.KueueDRAIntegrationPrioritizedList, true)
		fwk.StartManager(ctx, cfg, managerSetup(func(c *config.Configuration) {
			c.Resources.DeviceClassMappings = append(c.Resources.DeviceClassMappings,
				config.DeviceClassMapping{
					Name:             "gpu",
					DeviceClassNames: []corev1.ResourceName{"a100.example.com", "a100-mig.example.com"},
				},
			)
		}))
	})

	ginkgo.AfterAll(func() {
		fwk.StopManager(ctx)
	})

	ginkgo.When("two DeviceClasses are mapped to one logical resource", func() {
		var (
			ns             *corev1.Namespace
			resourceFlavor *kueue.ResourceFlavor
			clusterQueue   *kueue.ClusterQueue
			localQueue     *kueue.LocalQueue
			deviceClasses  []*resourcev1.DeviceClass
		)

		ginkgo.BeforeEach(func() {
			ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "dra-pl-")

			deviceClasses = nil
			for _, name := range []string{"a100.example.com", "a100-mig.example.com"} {
				deviceClass := testingdra.MakeDeviceClass(name).Obj()
				util.MustCreate(ctx, k8sClient, deviceClass)
				deviceClasses = append(deviceClasses, deviceClass)
			}

			resourceFlavor = utiltestingapi.MakeResourceFlavor("").GeneratedName("rf-pl-").Obj()
			util.MustCreate(ctx, k8sClient, resourceFlavor)

			clusterQueue = utiltestingapi.MakeClusterQueue("").GeneratedName("pl-cq-").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas(resourceFlavor.Name).
						Resource("gpu", "2").
						Obj(),
				).Obj()
			util.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, clusterQueue)

			localQueue = utiltestingapi.MakeLocalQueue("pl-lq", ns.Name).
				ClusterQueue(clusterQueue.Name).Obj()
			util.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, localQueue)
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			util.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, resourceFlavor, true)
			for _, deviceClass := range deviceClasses {
				util.ExpectObjectToBeDeleted(ctx, k8sClient, deviceClass, true)
			}
		})

		ginkgo.It("Should charge the count the alternatives share once", func() {
			ginkgo.By("Creating a ResourceClaimTemplate asking for two A100s or else two whole-card MIG slices")
			rct := utiltesting.MakeResourceClaimTemplate("two-a100-or-two-slices", ns.Name).
				DeviceRequests(testingdra.MakeFirstAvailableRequest("gpu",
					testingdra.MakeDeviceSubRequest("a100", "a100.example.com", 2).Obj(),
					testingdra.MakeDeviceSubRequest("slice", "a100-mig.example.com", 2).Obj(),
				).Obj()).
				Obj()
			util.MustCreate(ctx, k8sClient, rct)

			ginkgo.By("Creating a workload referencing the template")
			wl := utiltestingapi.MakeWorkload("pl-equal-wl", ns.Name).
				Queue("pl-lq").
				PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).ResourceClaimTemplate("gpu", "two-a100-or-two-slices").Obj()).
				Obj()
			util.MustCreate(ctx, k8sClient, wl)

			ginkgo.By("Verifying the workload is admitted on that count, not the sum of the alternatives")
			gomega.Eventually(func(g gomega.Gomega) {
				var updatedWl kueue.Workload
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &updatedWl)).To(gomega.Succeed())
				g.Expect(workload.HasQuotaReservation(&updatedWl)).To(gomega.BeTrue())
				g.Expect(updatedWl.Status.Admission).NotTo(gomega.BeNil())
				g.Expect(updatedWl.Status.Admission.PodSetAssignments).To(gomega.HaveLen(1))

				assignment := updatedWl.Status.Admission.PodSetAssignments[0]
				g.Expect(assignment.ResourceUsage).To(gomega.HaveKey(corev1.ResourceName("gpu")))
				gpuUsage := assignment.ResourceUsage["gpu"]
				g.Expect(gpuUsage.Cmp(resource.MustParse("2"))).To(gomega.Equal(0))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.It("Should reject alternatives with different counts", func() {
			ginkgo.By("Creating a ResourceClaimTemplate asking for one A100 or else two whole-card MIG slices")
			rct := utiltesting.MakeResourceClaimTemplate("one-a100-or-two-slices", ns.Name).
				DeviceRequests(testingdra.MakeFirstAvailableRequest("gpu",
					testingdra.MakeDeviceSubRequest("a100", "a100.example.com", 1).Obj(),
					testingdra.MakeDeviceSubRequest("slice", "a100-mig.example.com", 2).Obj(),
				).Obj()).
				Obj()
			util.MustCreate(ctx, k8sClient, rct)

			ginkgo.By("Creating a workload referencing the template")
			wl := utiltestingapi.MakeWorkload("pl-unequal-wl", ns.Name).
				Queue("pl-lq").
				PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).ResourceClaimTemplate("gpu", "one-a100-or-two-slices").Obj()).
				Obj()
			util.MustCreate(ctx, k8sClient, wl)

			ginkgo.By("Verifying the workload is marked as inadmissible although either alternative alone would fit")
			gomega.Eventually(func(g gomega.Gomega) {
				var updatedWl kueue.Workload
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &updatedWl)).To(gomega.Succeed())
				g.Expect(workload.HasQuotaReservation(&updatedWl)).To(gomega.BeFalse())
				g.Expect(updatedWl.Status.Conditions).To(gomega.ContainElement(gomega.And(
					gomega.HaveField("Type", kueue.WorkloadQuotaReserved),
					gomega.HaveField("Status", metav1.ConditionFalse),
					gomega.HaveField("Reason", kueue.WorkloadQuotaReservedReasonMisconfigured),
					gomega.HaveField("Message", gomega.ContainSubstring("every alternative must have count 1, this one has 2")),
				)))
				g.Expect(updatedWl.Status.Conditions).To(gomega.ContainElement(gomega.And(
					gomega.HaveField("Type", kueue.WorkloadRequeued),
					gomega.HaveField("Status", metav1.ConditionFalse),
					gomega.HaveField("Reason", kueue.WorkloadInadmissible),
				)))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.It("Should reject alternatives that resolve to different logical resources", func() {
			ginkgo.By("Creating a ResourceClaimTemplate whose alternatives map to gpu and res-1")
			rct := utiltesting.MakeResourceClaimTemplate("a100-or-res-1", ns.Name).
				DeviceRequests(testingdra.MakeFirstAvailableRequest("gpu",
					testingdra.MakeDeviceSubRequest("a100", "a100.example.com", 1).Obj(),
					testingdra.MakeDeviceSubRequest("other", "test-deviceclass-1", 1).Obj(),
				).Obj()).
				Obj()
			util.MustCreate(ctx, k8sClient, rct)

			ginkgo.By("Creating a workload referencing the template")
			wl := utiltestingapi.MakeWorkload("pl-two-resources-wl", ns.Name).
				Queue("pl-lq").
				PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).ResourceClaimTemplate("gpu", "a100-or-res-1").Obj()).
				Obj()
			util.MustCreate(ctx, k8sClient, wl)

			ginkgo.By("Verifying the workload is marked as inadmissible")
			gomega.Eventually(func(g gomega.Gomega) {
				var updatedWl kueue.Workload
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &updatedWl)).To(gomega.Succeed())
				g.Expect(workload.HasQuotaReservation(&updatedWl)).To(gomega.BeFalse())
				g.Expect(updatedWl.Status.Conditions).To(gomega.ContainElement(gomega.And(
					gomega.HaveField("Type", kueue.WorkloadQuotaReserved),
					gomega.HaveField("Status", metav1.ConditionFalse),
					gomega.HaveField("Reason", kueue.WorkloadQuotaReservedReasonMisconfigured),
					gomega.HaveField("Message", gomega.ContainSubstring(`every alternative must map to "gpu", this one maps to "res-1"`)),
				)))
				g.Expect(updatedWl.Status.Conditions).To(gomega.ContainElement(gomega.And(
					gomega.HaveField("Type", kueue.WorkloadRequeued),
					gomega.HaveField("Status", metav1.ConditionFalse),
					gomega.HaveField("Reason", kueue.WorkloadInadmissible),
				)))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})
	})
})
