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
	"sigs.k8s.io/kueue/test/util/behavioral"
)

var _ = ginkgo.Describe("DRA Excluded DeviceClasses Integration", func() {
	const (
		nicClass         = "nic.example.com"
		nicAltClass      = "nic-alt.example.com"
		nicExtendedClass = "nic-ext.example.com"
		nicResource      = "example.com/nic"
	)

	var (
		ns             *corev1.Namespace
		resourceFlavor *kueue.ResourceFlavor
		clusterQueue   *kueue.ClusterQueue
		localQueue     *kueue.LocalQueue
		deviceClass    *resourcev1.DeviceClass
	)

	ginkgo.BeforeEach(func() {
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.KueueDRAExcludeDeviceClasses, true)
		fwk.StartManagerForSpec(ctx, cfg, managerSetup(func(c *config.Configuration) {
			c.Resources.ExcludeDeviceClassNames = []corev1.ResourceName{nicClass, nicAltClass, nicExtendedClass}
		}))

		ns = behavioral.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "dra-exclude-")

		deviceClass = testingdra.MakeDeviceClass(nicExtendedClass).ExtendedResourceName(nicResource).Obj()
		behavioral.MustCreate(ctx, k8sClient, deviceClass)

		resourceFlavor = utiltestingapi.MakeResourceFlavor("").GeneratedName("rf-exclude-").Obj()
		behavioral.MustCreate(ctx, k8sClient, resourceFlavor)

		clusterQueue = utiltestingapi.MakeClusterQueue("").GeneratedName("exclude-cq-").
			ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas(resourceFlavor.Name).
					Resource("res-1", "2").
					Obj(),
			).Obj()
		behavioral.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, clusterQueue)

		localQueue = utiltestingapi.MakeLocalQueue("exclude-lq", ns.Name).
			ClusterQueue(clusterQueue.Name).Obj()
		behavioral.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, localQueue)

		behavioral.MustCreate(ctx, k8sClient, utiltesting.MakeResourceClaimTemplate("two-res-1", ns.Name).
			DeviceRequest("gpu", "test-deviceclass-1", 2).
			Obj())
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(behavioral.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, resourceFlavor, true)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, deviceClass, true)
	})

	ginkgo.It("Should admit a Workload whose claim takes every device of an excluded class", func() {
		behavioral.MustCreate(ctx, k8sClient, utiltesting.MakeResourceClaimTemplate("all-nics", ns.Name).
			DeviceRequest("nic", nicClass, 0).
			AllocationModeAll().
			Obj())

		wl := utiltestingapi.MakeWorkload("all-nics-wl", ns.Name).
			Queue(kueue.LocalQueueName(localQueue.Name)).
			PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
				ResourceClaimTemplate("gpu", "two-res-1").
				ResourceClaimTemplate("nic", "all-nics").
				Obj()).
			Obj()
		behavioral.MustCreate(ctx, k8sClient, wl)

		gomega.Eventually(func(g gomega.Gomega) {
			var updatedWl kueue.Workload
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &updatedWl)).To(gomega.Succeed())
			g.Expect(workload.HasQuotaReservation(&updatedWl)).To(gomega.BeTrue())
			g.Expect(updatedWl.Status.Admission.PodSetAssignments).To(gomega.HaveLen(1))
			usage := updatedWl.Status.Admission.PodSetAssignments[0].ResourceUsage
			g.Expect(usage).To(gomega.HaveLen(1))
			res1 := usage["res-1"]
			g.Expect(res1.Cmp(resource.MustParse("2"))).To(gomega.Equal(0))
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
	})

	ginkgo.It("Should admit an ExactCount request with a CEL selector on an excluded class without ResourceSlices", func() {
		nicDeviceClass := testingdra.MakeDeviceClass(nicClass).Obj()
		behavioral.MustCreate(ctx, k8sClient, nicDeviceClass)
		ginkgo.DeferCleanup(func() {
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, nicDeviceClass, true)
		})

		behavioral.MustCreate(ctx, k8sClient, utiltesting.MakeResourceClaimTemplate("one-nic-cel", ns.Name).
			DeviceRequest("nic", nicClass, 1).
			WithCELSelectors(`device.driver == "nic.example.com"`).
			Obj())

		wl := utiltestingapi.MakeWorkload("one-nic-cel-wl", ns.Name).
			Queue(kueue.LocalQueueName(localQueue.Name)).
			PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
				ResourceClaimTemplate("gpu", "two-res-1").
				ResourceClaimTemplate("nic", "one-nic-cel").
				Obj()).
			Obj()
		behavioral.MustCreate(ctx, k8sClient, wl)

		gomega.Eventually(func(g gomega.Gomega) {
			var updatedWl kueue.Workload
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &updatedWl)).To(gomega.Succeed())
			g.Expect(workload.HasQuotaReservation(&updatedWl)).To(gomega.BeTrue())
			g.Expect(updatedWl.Status.Admission.PodSetAssignments).To(gomega.HaveLen(1))
			usage := updatedWl.Status.Admission.PodSetAssignments[0].ResourceUsage
			g.Expect(usage).To(gomega.HaveLen(1))
			res1 := usage["res-1"]
			g.Expect(res1.Cmp(resource.MustParse("2"))).To(gomega.Equal(0))
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
	})

	ginkgo.It("Should admit a firstAvailable request whose alternatives are all excluded with the prioritized-list gate off", func() {
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.KueueDRAIntegrationPrioritizedList, false)
		behavioral.MustCreate(ctx, k8sClient, utiltesting.MakeResourceClaimTemplate("any-nic", ns.Name).
			DeviceRequests(testingdra.MakeFirstAvailableRequest("nic",
				testingdra.MakeDeviceSubRequest("all", nicClass, 0).AllocationModeAll().Obj(),
				testingdra.MakeDeviceSubRequest("one", nicAltClass, 1).Obj(),
			).Obj()).
			Obj())

		wl := utiltestingapi.MakeWorkload("any-nic-wl", ns.Name).
			Queue(kueue.LocalQueueName(localQueue.Name)).
			PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
				ResourceClaimTemplate("gpu", "two-res-1").
				ResourceClaimTemplate("nic", "any-nic").
				Obj()).
			Obj()
		behavioral.MustCreate(ctx, k8sClient, wl)

		gomega.Eventually(func(g gomega.Gomega) {
			var updatedWl kueue.Workload
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &updatedWl)).To(gomega.Succeed())
			g.Expect(workload.HasQuotaReservation(&updatedWl)).To(gomega.BeTrue())
			g.Expect(updatedWl.Status.Admission.PodSetAssignments).To(gomega.HaveLen(1))
			usage := updatedWl.Status.Admission.PodSetAssignments[0].ResourceUsage
			g.Expect(usage).To(gomega.HaveLen(1))
			res1 := usage["res-1"]
			g.Expect(res1.Cmp(resource.MustParse("2"))).To(gomega.Equal(0))
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
	})

	ginkgo.It("Should reject a firstAvailable request mixing an excluded and a mapped class", func() {
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.KueueDRAIntegrationPrioritizedList, true)
		behavioral.MustCreate(ctx, k8sClient, utiltesting.MakeResourceClaimTemplate("nic-or-gpu", ns.Name).
			DeviceRequests(testingdra.MakeFirstAvailableRequest("dev",
				testingdra.MakeDeviceSubRequest("nic", nicClass, 1).Obj(),
				testingdra.MakeDeviceSubRequest("gpu", "test-deviceclass-1", 1).Obj(),
			).Obj()).
			Obj())

		wl := utiltestingapi.MakeWorkload("nic-or-gpu-wl", ns.Name).
			Queue(kueue.LocalQueueName(localQueue.Name)).
			PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
				ResourceClaimTemplate("dev", "nic-or-gpu").
				Obj()).
			Obj()
		behavioral.MustCreate(ctx, k8sClient, wl)

		gomega.Eventually(func(g gomega.Gomega) {
			var updatedWl kueue.Workload
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &updatedWl)).To(gomega.Succeed())
			g.Expect(workload.HasQuotaReservation(&updatedWl)).To(gomega.BeFalse())
			g.Expect(updatedWl.Status.Conditions).To(gomega.ContainElement(gomega.And(
				gomega.HaveField("Type", kueue.WorkloadQuotaReserved),
				gomega.HaveField("Status", metav1.ConditionFalse),
				gomega.HaveField("Reason", kueue.WorkloadQuotaReservedReasonDRAResourcesUnresolved),
				gomega.HaveField("Message", gomega.ContainSubstring(nicClass)),
			)))
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
	})

	ginkgo.It("Should not charge an extended resource backed by an excluded class", func() {
		wl := utiltestingapi.MakeWorkload("ext-nic-wl", ns.Name).
			Queue(kueue.LocalQueueName(localQueue.Name)).
			PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
				ResourceClaimTemplate("gpu", "two-res-1").
				Request(nicResource, "1").
				Obj()).
			Obj()
		behavioral.MustCreate(ctx, k8sClient, wl)

		gomega.Eventually(func(g gomega.Gomega) {
			var updatedWl kueue.Workload
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &updatedWl)).To(gomega.Succeed())
			g.Expect(workload.HasQuotaReservation(&updatedWl)).To(gomega.BeTrue())
			g.Expect(updatedWl.Status.Admission.PodSetAssignments).To(gomega.HaveLen(1))
			usage := updatedWl.Status.Admission.PodSetAssignments[0].ResourceUsage
			g.Expect(usage).To(gomega.HaveLen(1))
			res1 := usage["res-1"]
			g.Expect(res1.Cmp(resource.MustParse("2"))).To(gomega.Equal(0))
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
	})

	ginkgo.It("Should not charge an extended resource backed by an excluded class when nothing else is charged", func() {
		wl := utiltestingapi.MakeWorkload("ext-nic-only-wl", ns.Name).
			Queue(kueue.LocalQueueName(localQueue.Name)).
			PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
				Request(nicResource, "1").
				Obj()).
			Obj()
		behavioral.MustCreate(ctx, k8sClient, wl)

		gomega.Eventually(func(g gomega.Gomega) {
			var updatedWl kueue.Workload
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &updatedWl)).To(gomega.Succeed())
			g.Expect(workload.HasQuotaReservation(&updatedWl)).To(gomega.BeTrue())
			g.Expect(updatedWl.Status.Admission.PodSetAssignments).To(gomega.HaveLen(1))
			g.Expect(updatedWl.Status.Admission.PodSetAssignments[0].ResourceUsage).NotTo(gomega.HaveKey(corev1.ResourceName(nicResource)))
		}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
	})
})
