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
	"context"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	resourcev1 "k8s.io/api/resource/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	config "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/test/integration/framework"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("DRA Integration", ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	ginkgo.BeforeAll(func() {
		fwk.StartManager(ctx, cfg, managerSetup(nil))
	})

	ginkgo.AfterAll(func() {
		fwk.StopManager(ctx)
	})

	ginkgo.BeforeEach(func() {
		ctx = context.Background()
	})

	ginkgo.When("DRA is configured via ConfigMap", func() {
		var (
			ns             *corev1.Namespace
			resourceFlavor *kueue.ResourceFlavor
			clusterQueue   *kueue.ClusterQueue
			localQueue     *kueue.LocalQueue
			deviceClass    *resourcev1.DeviceClass
			resourceSlices []*resourcev1.ResourceSlice
		)
		ginkgo.BeforeEach(func() {
			resourceSlices = nil
			ns = utiltesting.MakeNamespaceWithGenerateName("dra-")
			gomega.Expect(k8sClient.Create(ctx, ns)).To(gomega.Succeed())

			deviceClass = utiltesting.MakeDeviceClass("foo.example.com").Obj()
			gomega.Expect(k8sClient.Create(ctx, deviceClass)).To(gomega.Succeed())

			resourceFlavor = utiltestingapi.MakeResourceFlavor("").GeneratedName("rf-").Obj()
			gomega.Expect(k8sClient.Create(ctx, resourceFlavor)).To(gomega.Succeed())

			clusterQueue = utiltestingapi.MakeClusterQueue("").GeneratedName("test-cq-").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas(resourceFlavor.Name).
						Resource("foo", "10").
						Obj(),
				).Obj()
			gomega.Expect(k8sClient.Create(ctx, clusterQueue)).To(gomega.Succeed())
			util.ExpectClusterQueuesToBeActive(ctx, k8sClient, clusterQueue)
			localQueue = utiltestingapi.MakeLocalQueue("test-lq", ns.Name).
				ClusterQueue(clusterQueue.Name).Obj()
			gomega.Expect(k8sClient.Create(ctx, localQueue)).To(gomega.Succeed())
		})

		ginkgo.AfterEach(func() {
			for _, slice := range resourceSlices {
				util.ExpectObjectToBeDeleted(ctx, k8sClient, slice, true)
			}
			gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			util.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, resourceFlavor, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, deviceClass, true)
		})

		ginkgo.It("Should reject workload with DRA resource claims with inadmissible condition", framework.SlowSpec, func() {
			ginkgo.By("Creating a ResourceClaim")
			rc := utiltesting.MakeResourceClaim("test-rc", ns.Name).
				DeviceRequest("device-request", "foo.example.com", 2).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, rc)).To(gomega.Succeed())

			ginkgo.By("Creating a workload with DRA resource claim")
			wl := utiltestingapi.MakeWorkload("test-wl", ns.Name).
				Queue("test-lq").
				Obj()
			wl.Spec.PodSets[0].Template.Spec.ResourceClaims = []corev1.PodResourceClaim{
				{Name: "device", ResourceClaimName: new("test-rc")},
			}
			wl.Spec.PodSets[0].Template.Spec.Containers[0].Resources.Claims = []corev1.ResourceClaim{
				{Name: "device"},
			}
			gomega.Expect(k8sClient.Create(ctx, wl)).To(gomega.Succeed())

			ginkgo.By("Verifying workload is marked as inadmissible")
			gomega.Eventually(func(g gomega.Gomega) {
				var updatedWl kueue.Workload
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &updatedWl)).To(gomega.Succeed())
				g.Expect(workload.HasQuotaReservation(&updatedWl)).To(gomega.BeFalse())

				g.Expect(updatedWl.Status.Conditions).To(gomega.ContainElement(gomega.And(
					gomega.HaveField("Type", kueue.WorkloadQuotaReserved),
					gomega.HaveField("Status", metav1.ConditionFalse),
					gomega.HaveField("Reason", kueue.WorkloadInadmissible),
				)))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.It("Should handle workload with insufficient DRA quota", func() {
			ginkgo.By("Creating a ResourceClaim that exceeds quota")
			rc := utiltesting.MakeResourceClaim("test-rc-large", ns.Name).
				DeviceRequest("device-request", "foo.example.com", 15).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, rc)).To(gomega.Succeed())

			ginkgo.By("Creating a workload with large DRA resource claim")
			wl := utiltestingapi.MakeWorkload("test-wl-large", ns.Name).
				Queue("test-lq").
				Obj()
			wl.Spec.PodSets[0].Template.Spec.ResourceClaims = []corev1.PodResourceClaim{
				{Name: "device", ResourceClaimName: new("test-rc-large")},
			}
			wl.Spec.PodSets[0].Template.Spec.Containers[0].Resources.Claims = []corev1.ResourceClaim{
				{Name: "device"},
			}
			gomega.Expect(k8sClient.Create(ctx, wl)).To(gomega.Succeed())

			ginkgo.By("Verifying workload remains pending")
			updatedWl := &kueue.Workload{}
			gomega.Consistently(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), updatedWl)).To(gomega.Succeed())
				g.Expect(workload.HasQuotaReservation(updatedWl)).To(gomega.BeFalse())
			}, util.ConsistentDuration, util.ShortInterval).Should(gomega.Succeed())
		})

		ginkgo.It("Should handle multiple workloads sharing DRA quota", func() {
			ginkgo.By("Creating ResourceClaimTemplates")
			rct1 := utiltesting.MakeResourceClaimTemplate("quota-template-1", ns.Name).
				DeviceRequest("device-request", "foo.example.com", 4).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, rct1)).To(gomega.Succeed())

			rct2 := utiltesting.MakeResourceClaimTemplate("quota-template-2", ns.Name).
				DeviceRequest("device-request", "foo.example.com", 4).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, rct2)).To(gomega.Succeed())

			ginkgo.By("Creating first workload")
			wl1 := utiltestingapi.MakeWorkload("test-wl-1", ns.Name).
				Queue("test-lq").
				Obj()
			wl1.Spec.PodSets[0].Template.Spec.ResourceClaims = []corev1.PodResourceClaim{
				{
					Name:                      "device-template",
					ResourceClaimTemplateName: new("quota-template-1"),
				},
			}
			wl1.Spec.PodSets[0].Template.Spec.Containers[0].Resources.Claims = []corev1.ResourceClaim{
				{Name: "device-template"},
			}
			gomega.Expect(k8sClient.Create(ctx, wl1)).To(gomega.Succeed())

			ginkgo.By("Creating second workload")
			wl2 := utiltestingapi.MakeWorkload("test-wl-2", ns.Name).
				Queue("test-lq").
				Obj()
			wl2.Spec.PodSets[0].Template.Spec.ResourceClaims = []corev1.PodResourceClaim{
				{
					Name:                      "device-template",
					ResourceClaimTemplateName: new("quota-template-2"),
				},
			}
			wl2.Spec.PodSets[0].Template.Spec.Containers[0].Resources.Claims = []corev1.ResourceClaim{
				{Name: "device-template"},
			}
			gomega.Expect(k8sClient.Create(ctx, wl2)).To(gomega.Succeed())

			ginkgo.By("Verifying both workloads are admitted")
			gomega.Eventually(func(g gomega.Gomega) {
				var updatedWl1, updatedWl2 kueue.Workload
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl1), &updatedWl1)).To(gomega.Succeed())
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl2), &updatedWl2)).To(gomega.Succeed())
				g.Expect(workload.HasQuotaReservation(&updatedWl1)).To(gomega.BeTrue())
				g.Expect(workload.HasQuotaReservation(&updatedWl2)).To(gomega.BeTrue())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			ginkgo.By("Verifying total DRA usage doesn't exceed quota")
			gomega.Eventually(func(g gomega.Gomega) {
				var updatedWl1, updatedWl2 kueue.Workload
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl1), &updatedWl1)).To(gomega.Succeed())
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl2), &updatedWl2)).To(gomega.Succeed())

				totalUsage := int64(0)
				if updatedWl1.Status.Admission != nil && len(updatedWl1.Status.Admission.PodSetAssignments) > 0 {
					if usage, ok := updatedWl1.Status.Admission.PodSetAssignments[0].ResourceUsage["foo"]; ok {
						totalUsage += usage.Value()
					}
				}
				if updatedWl2.Status.Admission != nil && len(updatedWl2.Status.Admission.PodSetAssignments) > 0 {
					if usage, ok := updatedWl2.Status.Admission.PodSetAssignments[0].ResourceUsage["foo"]; ok {
						totalUsage += usage.Value()
					}
				}
				g.Expect(totalUsage).To(gomega.Equal(int64(8)))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.It("Should admit workload with DRA resource claim templates", func() {
			ginkgo.By("Creating a ResourceClaimTemplate")
			rct := utiltesting.MakeResourceClaimTemplate("device-template", ns.Name).
				DeviceRequest("device-request", "foo.example.com", 2).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, rct)).To(gomega.Succeed())

			ginkgo.By("Creating a workload that references the ResourceClaimTemplate")
			wl := utiltestingapi.MakeWorkload("test-wl-template", ns.Name).
				Queue("test-lq").
				Obj()
			wl.Spec.PodSets[0].Template.Spec.ResourceClaims = []corev1.PodResourceClaim{
				{
					Name:                      "device-template",
					ResourceClaimTemplateName: new("device-template"),
				},
			}
			wl.Spec.PodSets[0].Template.Spec.Containers[0].Resources.Claims = []corev1.ResourceClaim{
				{Name: "device-template"},
			}
			gomega.Expect(k8sClient.Create(ctx, wl)).To(gomega.Succeed())

			ginkgo.By("Verifying workload is admitted with ResourceClaimTemplate")
			gomega.Eventually(func(g gomega.Gomega) {
				var updatedWl kueue.Workload
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &updatedWl)).To(gomega.Succeed())
				g.Expect(workload.HasQuotaReservation(&updatedWl)).To(gomega.BeTrue())
				g.Expect(updatedWl.Status.Admission).NotTo(gomega.BeNil())
				g.Expect(updatedWl.Status.Admission.PodSetAssignments).To(gomega.HaveLen(1))

				assignment := updatedWl.Status.Admission.PodSetAssignments[0]
				g.Expect(assignment.ResourceUsage).To(gomega.HaveKey(corev1.ResourceName("foo")))
				g.Expect(assignment.ResourceUsage["foo"]).To(gomega.Equal(resource.MustParse("2")))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.It("Should handle multiple workloads with ResourceClaimTemplates", func() {
			ginkgo.By("Creating ResourceClaimTemplates")
			rct1 := utiltesting.MakeResourceClaimTemplate("device-template-1", ns.Name).
				DeviceRequest("device-request", "foo.example.com", 3).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, rct1)).To(gomega.Succeed())

			rct2 := utiltesting.MakeResourceClaimTemplate("device-template-2", ns.Name).
				DeviceRequest("device-request", "foo.example.com", 3).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, rct2)).To(gomega.Succeed())

			ginkgo.By("Creating first workload with ResourceClaimTemplate")
			wl1 := utiltestingapi.MakeWorkload("test-wl-template-1", ns.Name).
				Queue("test-lq").
				Obj()
			wl1.Spec.PodSets[0].Template.Spec.ResourceClaims = []corev1.PodResourceClaim{
				{
					Name:                      "device-template",
					ResourceClaimTemplateName: new("device-template-1"),
				},
			}
			wl1.Spec.PodSets[0].Template.Spec.Containers[0].Resources.Claims = []corev1.ResourceClaim{
				{Name: "device-template"},
			}
			gomega.Expect(k8sClient.Create(ctx, wl1)).To(gomega.Succeed())

			ginkgo.By("Creating second workload with ResourceClaimTemplate")
			wl2 := utiltestingapi.MakeWorkload("test-wl-template-2", ns.Name).
				Queue("test-lq").
				Obj()
			wl2.Spec.PodSets[0].Template.Spec.ResourceClaims = []corev1.PodResourceClaim{
				{
					Name:                      "device-template",
					ResourceClaimTemplateName: new("device-template-2"),
				},
			}
			wl2.Spec.PodSets[0].Template.Spec.Containers[0].Resources.Claims = []corev1.ResourceClaim{
				{Name: "device-template"},
			}
			gomega.Expect(k8sClient.Create(ctx, wl2)).To(gomega.Succeed())

			ginkgo.By("Verifying both workloads are admitted with ResourceClaimTemplates")
			gomega.Eventually(func(g gomega.Gomega) {
				var updatedWl1, updatedWl2 kueue.Workload
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl1), &updatedWl1)).To(gomega.Succeed())
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl2), &updatedWl2)).To(gomega.Succeed())
				g.Expect(workload.HasQuotaReservation(&updatedWl1)).To(gomega.BeTrue())
				g.Expect(workload.HasQuotaReservation(&updatedWl2)).To(gomega.BeTrue())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			ginkgo.By("Verifying total DRA usage from ResourceClaimTemplates")
			gomega.Eventually(func(g gomega.Gomega) {
				var updatedWl1, updatedWl2 kueue.Workload
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl1), &updatedWl1)).To(gomega.Succeed())
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl2), &updatedWl2)).To(gomega.Succeed())

				totalUsage := int64(0)
				if updatedWl1.Status.Admission != nil && len(updatedWl1.Status.Admission.PodSetAssignments) > 0 {
					if usage, ok := updatedWl1.Status.Admission.PodSetAssignments[0].ResourceUsage["foo"]; ok {
						totalUsage += usage.Value()
					}
				}
				if updatedWl2.Status.Admission != nil && len(updatedWl2.Status.Admission.PodSetAssignments) > 0 {
					if usage, ok := updatedWl2.Status.Admission.PodSetAssignments[0].ResourceUsage["foo"]; ok {
						totalUsage += usage.Value()
					}
				}
				g.Expect(totalUsage).To(gomega.Equal(int64(6)))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.It("Should handle ResourceClaimTemplate with insufficient quota", func() {
			ginkgo.By("Creating a ResourceClaimTemplate that exceeds quota")
			rct := utiltesting.MakeResourceClaimTemplate("device-template-large", ns.Name).
				DeviceRequest("device-request", "foo.example.com", 12).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, rct)).To(gomega.Succeed())

			ginkgo.By("Creating a workload that references the large ResourceClaimTemplate")
			wl := utiltestingapi.MakeWorkload("test-wl-template-large", ns.Name).
				Queue("test-lq").
				Obj()
			wl.Spec.PodSets[0].Template.Spec.ResourceClaims = []corev1.PodResourceClaim{
				{
					Name:                      "device-template",
					ResourceClaimTemplateName: new("device-template-large"),
				},
			}
			wl.Spec.PodSets[0].Template.Spec.Containers[0].Resources.Claims = []corev1.ResourceClaim{
				{Name: "device-template"},
			}
			gomega.Expect(k8sClient.Create(ctx, wl)).To(gomega.Succeed())

			ginkgo.By("Verifying workload with ResourceClaimTemplate remains pending due to quota")
			updatedWl := &kueue.Workload{}
			gomega.Consistently(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), updatedWl)).To(gomega.Succeed())
				g.Expect(workload.HasQuotaReservation(updatedWl)).To(gomega.BeFalse())
			}, util.ConsistentDuration, util.ShortInterval).Should(gomega.Succeed())
		})

		ginkgo.It("Should handle unmapped device classes with proper error", framework.SlowSpec, func() {
			ginkgo.By("Creating a ResourceClaimTemplate with unmapped device class")
			rct := utiltesting.MakeResourceClaimTemplate("unmapped-template", ns.Name).
				DeviceRequest("device-request", "unmapped.example.com", 2).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, rct)).To(gomega.Succeed())

			ginkgo.By("Creating a workload with unmapped device class")
			wl := utiltestingapi.MakeWorkload("test-wl-unmapped", ns.Name).
				Queue("test-lq").
				Obj()
			wl.Spec.PodSets[0].Template.Spec.ResourceClaims = []corev1.PodResourceClaim{
				{
					Name:                      "device-template",
					ResourceClaimTemplateName: new("unmapped-template"),
				},
			}
			wl.Spec.PodSets[0].Template.Spec.Containers[0].Resources.Claims = []corev1.ResourceClaim{
				{Name: "device-template"},
			}
			gomega.Expect(k8sClient.Create(ctx, wl)).To(gomega.Succeed())

			ginkgo.By("Verifying workload is marked as inadmissible")
			gomega.Eventually(func(g gomega.Gomega) {
				var updatedWl kueue.Workload
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &updatedWl)).To(gomega.Succeed())
				g.Expect(workload.HasQuotaReservation(&updatedWl)).To(gomega.BeFalse())

				g.Expect(updatedWl.Status.Conditions).To(gomega.ContainElement(gomega.And(
					gomega.HaveField("Type", kueue.WorkloadQuotaReserved),
					gomega.HaveField("Status", metav1.ConditionFalse),
					gomega.HaveField("Reason", kueue.WorkloadInadmissible),
					gomega.HaveField("Message", gomega.And(
						gomega.ContainSubstring("DeviceClass"),
						gomega.ContainSubstring("is not mapped"),
					)),
				)))
			}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())

			ginkgo.By("Restarting the controller with new config mapping the device class")
			fwk.StopManager(ctx)
			fwk.StartManager(ctx, cfg, managerSetup(func(c *config.Configuration) {
				for i, mapping := range c.Resources.DeviceClassMappings {
					if mapping.Name == "foo" {
						c.Resources.DeviceClassMappings[i].DeviceClassNames = append(mapping.DeviceClassNames, "unmapped.example.com")
						break
					}
				}
			}))
			defer func() {
				ginkgo.By("Restarting the controller with the default config")
				fwk.StopManager(ctx)
				fwk.StartManager(ctx, cfg, managerSetup(nil))
			}()

			ginkgo.By("Verifying workload is admitted")
			gomega.Eventually(func(g gomega.Gomega) {
				var updatedWl kueue.Workload
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &updatedWl)).To(gomega.Succeed())
				g.Expect(workload.HasQuotaReservation(&updatedWl)).To(gomega.BeTrue())
				g.Expect(updatedWl.Status.Admission).NotTo(gomega.BeNil())
				g.Expect(updatedWl.Status.Admission.PodSetAssignments).To(gomega.HaveLen(1))

				assignment := updatedWl.Status.Admission.PodSetAssignments[0]
				g.Expect(assignment.ResourceUsage).To(gomega.HaveKey(corev1.ResourceName("foo")))
				g.Expect(assignment.ResourceUsage["foo"]).To(gomega.Equal(resource.MustParse("2")))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.It("Should handle multi-pod workloads with correct DRA resource calculation", func() {
			ginkgo.By("Creating a ResourceClaimTemplate")
			rct := utiltesting.MakeResourceClaimTemplate("multi-pod-template", ns.Name).
				DeviceRequest("device-request", "foo.example.com", 1).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, rct)).To(gomega.Succeed())

			ginkgo.By("Creating a multi-pod workload (parallelism: 3)")
			wl := utiltestingapi.MakeWorkload("test-wl-multi-pod", ns.Name).
				Queue("test-lq").
				Obj()

			wl.Spec.PodSets[0].Count = 3
			wl.Spec.PodSets[0].Template.Spec.ResourceClaims = []corev1.PodResourceClaim{
				{
					Name:                      "device-template",
					ResourceClaimTemplateName: new("multi-pod-template"),
				},
			}
			wl.Spec.PodSets[0].Template.Spec.Containers[0].Resources.Claims = []corev1.ResourceClaim{
				{Name: "device-template"},
			}
			gomega.Expect(k8sClient.Create(ctx, wl)).To(gomega.Succeed())

			ginkgo.By("Verifying workload is admitted with correct total DRA resource usage")
			gomega.Eventually(func(g gomega.Gomega) {
				var updatedWl kueue.Workload
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &updatedWl)).To(gomega.Succeed())
				g.Expect(workload.HasQuotaReservation(&updatedWl)).To(gomega.BeTrue())
				g.Expect(updatedWl.Status.Admission).NotTo(gomega.BeNil())
				g.Expect(updatedWl.Status.Admission.PodSetAssignments).To(gomega.HaveLen(1))

				assignment := updatedWl.Status.Admission.PodSetAssignments[0]

				g.Expect(assignment.Count).To(gomega.Equal(new(int32(3))))

				g.Expect(assignment.ResourceUsage).To(gomega.HaveKey(corev1.ResourceName("foo")))
				g.Expect(assignment.ResourceUsage["foo"]).To(gomega.Equal(resource.MustParse("3")))

				resourceQuantity := assignment.ResourceUsage["foo"]
				resourceValue := resourceQuantity.Value()
				podCount := int64(*assignment.Count)
				g.Expect(resourceValue%podCount).To(gomega.Equal(int64(0)),
					"DRA resource usage should be a multiple of pod count for webhook validation")
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.It("Should reject workload with AllocationMode 'All'", func() {
			ginkgo.By("Creating a ResourceClaimTemplate with AllocationMode All")
			rct := utiltesting.MakeResourceClaimTemplate("all-mode-template", ns.Name).
				DeviceRequest("device-request", "foo.example.com", 0).
				AllocationModeAll().
				Obj()
			gomega.Expect(k8sClient.Create(ctx, rct)).To(gomega.Succeed())

			ginkgo.By("Creating a workload with AllocationMode All")
			wl := utiltestingapi.MakeWorkload("test-wl-all-mode", ns.Name).
				Queue("test-lq").
				Obj()
			wl.Spec.PodSets[0].Template.Spec.ResourceClaims = []corev1.PodResourceClaim{
				{
					Name:                      "device-template",
					ResourceClaimTemplateName: new("all-mode-template"),
				},
			}
			wl.Spec.PodSets[0].Template.Spec.Containers[0].Resources.Claims = []corev1.ResourceClaim{
				{Name: "device-template"},
			}
			gomega.Expect(k8sClient.Create(ctx, wl)).To(gomega.Succeed())

			ginkgo.By("Verifying workload is marked as inadmissible")
			gomega.Eventually(func(g gomega.Gomega) {
				var updatedWl kueue.Workload
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &updatedWl)).To(gomega.Succeed())
				g.Expect(workload.HasQuotaReservation(&updatedWl)).To(gomega.BeFalse())

				g.Expect(updatedWl.Status.Conditions).To(gomega.ContainElement(gomega.And(
					gomega.HaveField("Type", kueue.WorkloadQuotaReserved),
					gomega.HaveField("Status", metav1.ConditionFalse),
					gomega.HaveField("Reason", kueue.WorkloadInadmissible),
					gomega.HaveField("Message", gomega.ContainSubstring("AllocationMode 'All' is not supported")),
				)))
			}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.It("Should admit workload with CEL selectors", func() {
			ginkgo.By("Creating a ResourceSlice with devices matching the CEL selector")
			slice := &resourcev1.ResourceSlice{
				ObjectMeta: metav1.ObjectMeta{
					Name: "cel-test-slice",
				},
				Spec: resourcev1.ResourceSliceSpec{
					Driver: "test-driver",
					Pool: resourcev1.ResourcePool{
						Name:               "test-pool",
						Generation:         1,
						ResourceSliceCount: 1,
					},
					NodeName: new("fake-node"),
					Devices: []resourcev1.Device{
						{Name: "dev-0"},
						{Name: "dev-1"},
					},
				},
			}
			util.MustCreate(ctx, k8sClient, slice)
			resourceSlices = append(resourceSlices, slice)

			ginkgo.By("Creating a ResourceClaimTemplate with CEL selectors")
			rct := utiltesting.MakeResourceClaimTemplate("cel-selector-template", ns.Name).
				DeviceRequest("device-request", "foo.example.com", 2).
				WithCELSelectors("device.driver == \"test-driver\"").
				Obj()
			util.MustCreate(ctx, k8sClient, rct)

			ginkgo.By("Creating a workload with CEL selectors")
			wl := utiltestingapi.MakeWorkload("test-wl-cel-selector", ns.Name).
				Queue("test-lq").
				PodSets(
					*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
						ResourceClaimTemplate("device-template", "cel-selector-template").
						Obj(),
				).
				Obj()
			util.MustCreate(ctx, k8sClient, wl)

			ginkgo.By("Verifying workload is admitted with correct resource usage")
			gomega.Eventually(func(g gomega.Gomega) {
				var updatedWl kueue.Workload
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &updatedWl)).To(gomega.Succeed())
				g.Expect(workload.HasQuotaReservation(&updatedWl)).To(gomega.BeTrue())
				g.Expect(updatedWl.Status.Admission).NotTo(gomega.BeNil())
				g.Expect(updatedWl.Status.Admission.PodSetAssignments).To(gomega.HaveLen(1))

				assignment := updatedWl.Status.Admission.PodSetAssignments[0]
				g.Expect(assignment.ResourceUsage).To(gomega.HaveKey(corev1.ResourceName("foo")))
				g.Expect(assignment.ResourceUsage["foo"]).To(gomega.Equal(resource.MustParse("2")))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.It("Should reject workload with unsatisfiable CEL selectors", func() {
			ginkgo.By("Creating a ResourceSlice with devices that won't match the CEL selector")
			slice := &resourcev1.ResourceSlice{
				ObjectMeta: metav1.ObjectMeta{
					Name: "cel-reject-slice",
				},
				Spec: resourcev1.ResourceSliceSpec{
					Driver: "real-driver",
					Pool: resourcev1.ResourcePool{
						Name:               "test-pool",
						Generation:         1,
						ResourceSliceCount: 1,
					},
					NodeName: new("fake-node"),
					Devices: []resourcev1.Device{
						{Name: "dev-0"},
					},
				},
			}
			util.MustCreate(ctx, k8sClient, slice)
			resourceSlices = append(resourceSlices, slice)

			ginkgo.By("Creating a ResourceClaimTemplate with CEL selector that matches no devices")
			rct := utiltesting.MakeResourceClaimTemplate("cel-reject-template", ns.Name).
				DeviceRequest("device-request", "foo.example.com", 2).
				WithCELSelectors("device.driver == \"nonexistent-driver\"").
				Obj()
			util.MustCreate(ctx, k8sClient, rct)

			ginkgo.By("Creating a workload with unsatisfiable CEL selectors")
			wl := utiltestingapi.MakeWorkload("test-wl-cel-reject", ns.Name).
				Queue("test-lq").
				PodSets(
					*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
						ResourceClaimTemplate("device-template", "cel-reject-template").
						Obj(),
				).
				Obj()
			util.MustCreate(ctx, k8sClient, wl)

			ginkgo.By("Verifying workload is marked as inadmissible due to unsatisfiable CEL selectors")
			gomega.Eventually(func(g gomega.Gomega) {
				var updatedWl kueue.Workload
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &updatedWl)).To(gomega.Succeed())
				g.Expect(workload.HasQuotaReservation(&updatedWl)).To(gomega.BeFalse())

				g.Expect(updatedWl.Status.Conditions).To(gomega.ContainElement(gomega.And(
					gomega.HaveField("Type", kueue.WorkloadQuotaReserved),
					gomega.HaveField("Status", metav1.ConditionFalse),
					gomega.HaveField("Reason", kueue.WorkloadInadmissible),
					gomega.HaveField("Message", gomega.ContainSubstring("insufficient matching devices for CEL selector")),
				)))
			}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.It("Should admit workload with device constraints (matchAttribute)", func() {
			ginkgo.By("Creating a ResourceClaimTemplate with device constraints")
			rct := utiltesting.MakeResourceClaimTemplate("constraint-template", ns.Name).
				DeviceRequest("gpu-1", "foo.example.com", 1).
				DeviceRequest("gpu-2", "foo.example.com", 1).
				WithDeviceConstraints([]string{"gpu-1", "gpu-2"}, "example.com/numa_node").
				Obj()
			gomega.Expect(k8sClient.Create(ctx, rct)).To(gomega.Succeed())

			ginkgo.By("Creating a workload with device constraints")
			wl := utiltestingapi.MakeWorkload("test-wl-constraint", ns.Name).
				Queue("test-lq").
				Obj()
			wl.Spec.PodSets[0].Template.Spec.ResourceClaims = []corev1.PodResourceClaim{
				{
					Name:                      "device-template",
					ResourceClaimTemplateName: new("constraint-template"),
				},
			}
			wl.Spec.PodSets[0].Template.Spec.Containers[0].Resources.Claims = []corev1.ResourceClaim{
				{Name: "device-template"},
			}
			gomega.Expect(k8sClient.Create(ctx, wl)).To(gomega.Succeed())

			ginkgo.By("Verifying workload is admitted with correct resource usage")
			gomega.Eventually(func(g gomega.Gomega) {
				var updatedWl kueue.Workload
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &updatedWl)).To(gomega.Succeed())
				g.Expect(workload.HasQuotaReservation(&updatedWl)).To(gomega.BeTrue())
				g.Expect(updatedWl.Status.Admission).NotTo(gomega.BeNil())
				g.Expect(updatedWl.Status.Admission.PodSetAssignments).To(gomega.HaveLen(1))

				assignment := updatedWl.Status.Admission.PodSetAssignments[0]
				g.Expect(assignment.ResourceUsage).To(gomega.HaveKey(corev1.ResourceName("foo")))
				g.Expect(assignment.ResourceUsage["foo"]).To(gomega.Equal(resource.MustParse("2")))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.It("Should reject workload with AdminAccess", func() {
			ginkgo.By("Adding admin-access label to namespace")
			var namespace corev1.Namespace
			gomega.Expect(k8sClient.Get(ctx, client.ObjectKey{Name: ns.Name}, &namespace)).To(gomega.Succeed())
			if namespace.Labels == nil {
				namespace.Labels = make(map[string]string)
			}
			namespace.Labels["resource.kubernetes.io/admin-access"] = "true"
			gomega.Expect(k8sClient.Update(ctx, &namespace)).To(gomega.Succeed())

			ginkgo.By("Creating a ResourceClaimTemplate with AdminAccess")
			rct := utiltesting.MakeResourceClaimTemplate("admin-access-template", ns.Name).
				DeviceRequest("device-request", "foo.example.com", 2).
				WithAdminAccess(true).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, rct)).To(gomega.Succeed())

			ginkgo.By("Creating a workload with AdminAccess")
			wl := utiltestingapi.MakeWorkload("test-wl-admin-access", ns.Name).
				Queue("test-lq").
				Obj()
			wl.Spec.PodSets[0].Template.Spec.ResourceClaims = []corev1.PodResourceClaim{
				{
					Name:                      "device-template",
					ResourceClaimTemplateName: new("admin-access-template"),
				},
			}
			wl.Spec.PodSets[0].Template.Spec.Containers[0].Resources.Claims = []corev1.ResourceClaim{
				{Name: "device-template"},
			}
			gomega.Expect(k8sClient.Create(ctx, wl)).To(gomega.Succeed())

			ginkgo.By("Verifying workload is marked as inadmissible")
			gomega.Eventually(func(g gomega.Gomega) {
				var updatedWl kueue.Workload
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &updatedWl)).To(gomega.Succeed())
				g.Expect(workload.HasQuotaReservation(&updatedWl)).To(gomega.BeFalse())

				g.Expect(updatedWl.Status.Conditions).To(gomega.ContainElement(gomega.And(
					gomega.HaveField("Type", kueue.WorkloadQuotaReserved),
					gomega.HaveField("Status", metav1.ConditionFalse),
					gomega.HaveField("Reason", kueue.WorkloadInadmissible),
					gomega.HaveField("Message", gomega.ContainSubstring("AdminAccess is not supported")),
				)))
			}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.It("Should admit workload with device config", func() {
			ginkgo.By("Creating a ResourceClaimTemplate with device config")
			rct := utiltesting.MakeResourceClaimTemplate("device-config-template", ns.Name).
				DeviceRequest("device-request", "foo.example.com", 2).
				WithDeviceConfig("device-request", "driver.example.com", []byte(`{"key":"value"}`)).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, rct)).To(gomega.Succeed())

			ginkgo.By("Creating a workload with device config")
			wl := utiltestingapi.MakeWorkload("test-wl-device-config", ns.Name).
				Queue("test-lq").
				Obj()
			wl.Spec.PodSets[0].Template.Spec.ResourceClaims = []corev1.PodResourceClaim{
				{
					Name:                      "device-template",
					ResourceClaimTemplateName: new("device-config-template"),
				},
			}
			wl.Spec.PodSets[0].Template.Spec.Containers[0].Resources.Claims = []corev1.ResourceClaim{
				{Name: "device-template"},
			}
			gomega.Expect(k8sClient.Create(ctx, wl)).To(gomega.Succeed())

			ginkgo.By("Verifying workload is admitted with correct resource usage")
			gomega.Eventually(func(g gomega.Gomega) {
				var updatedWl kueue.Workload
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &updatedWl)).To(gomega.Succeed())
				g.Expect(workload.HasQuotaReservation(&updatedWl)).To(gomega.BeTrue())
				g.Expect(updatedWl.Status.Admission).NotTo(gomega.BeNil())
				g.Expect(updatedWl.Status.Admission.PodSetAssignments).To(gomega.HaveLen(1))

				assignment := updatedWl.Status.Admission.PodSetAssignments[0]
				g.Expect(assignment.ResourceUsage).To(gomega.HaveKey(corev1.ResourceName("foo")))
				g.Expect(assignment.ResourceUsage["foo"]).To(gomega.Equal(resource.MustParse("2")))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.It("Should reject workload with FirstAvailable", func() {
			ginkgo.By("Creating a ResourceClaimTemplate with FirstAvailable")
			rct := utiltesting.MakeResourceClaimTemplate("first-available-template", ns.Name).
				FirstAvailableRequest("device-request", "foo.example.com").
				Obj()
			gomega.Expect(k8sClient.Create(ctx, rct)).To(gomega.Succeed())

			ginkgo.By("Creating a workload with FirstAvailable")
			wl := utiltestingapi.MakeWorkload("test-wl-first-available", ns.Name).
				Queue("test-lq").
				Obj()
			wl.Spec.PodSets[0].Template.Spec.ResourceClaims = []corev1.PodResourceClaim{
				{
					Name:                      "device-template",
					ResourceClaimTemplateName: new("first-available-template"),
				},
			}
			wl.Spec.PodSets[0].Template.Spec.Containers[0].Resources.Claims = []corev1.ResourceClaim{
				{Name: "device-template"},
			}
			gomega.Expect(k8sClient.Create(ctx, wl)).To(gomega.Succeed())

			ginkgo.By("Verifying workload is marked as inadmissible")
			gomega.Eventually(func(g gomega.Gomega) {
				var updatedWl kueue.Workload
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &updatedWl)).To(gomega.Succeed())
				g.Expect(workload.HasQuotaReservation(&updatedWl)).To(gomega.BeFalse())

				g.Expect(updatedWl.Status.Conditions).To(gomega.ContainElement(gomega.And(
					gomega.HaveField("Type", kueue.WorkloadQuotaReserved),
					gomega.HaveField("Status", metav1.ConditionFalse),
					gomega.HaveField("Reason", kueue.WorkloadInadmissible),
					gomega.HaveField("Message", gomega.ContainSubstring("FirstAvailable device selection is not supported")),
				)))
			}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.It("Should admit workload with empty AllocationMode that defaults to ExactCount", func() {
			ginkgo.By("Creating a ResourceClaimTemplate with empty AllocationMode")
			rct := utiltesting.MakeResourceClaimTemplate("empty-mode-template", ns.Name).
				DeviceRequest("device-request", "foo.example.com", 2).
				Obj()
			// Set AllocationMode to empty string explicitly
			rct.Spec.Spec.Devices.Requests[0].Exactly.AllocationMode = ""
			gomega.Expect(k8sClient.Create(ctx, rct)).To(gomega.Succeed())

			ginkgo.By("Creating a workload that references the template with empty AllocationMode")
			wl := utiltestingapi.MakeWorkload("test-wl-empty-mode", ns.Name).
				Queue("test-lq").
				Obj()
			wl.Spec.PodSets[0].Template.Spec.ResourceClaims = []corev1.PodResourceClaim{
				{
					Name:                      "device-template",
					ResourceClaimTemplateName: new("empty-mode-template"),
				},
			}
			wl.Spec.PodSets[0].Template.Spec.Containers[0].Resources.Claims = []corev1.ResourceClaim{
				{Name: "device-template"},
			}
			gomega.Expect(k8sClient.Create(ctx, wl)).To(gomega.Succeed())
		})
	})

	ginkgo.When("Extended Resources backed by DRA", func() {
		var (
			ns             *corev1.Namespace
			resourceFlavor *kueue.ResourceFlavor
			clusterQueue   *kueue.ClusterQueue
			localQueue     *kueue.LocalQueue
			deviceClass    *resourcev1.DeviceClass
		)

		const extendedResourceName = "example.com/gpu"

		ginkgo.BeforeAll(func() {
			features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.KueueDRAIntegrationExtendedResource, true)
			fwk.StopManager(ctx)
			fwk.StartManager(ctx, cfg, managerSetup(nil))
		})

		ginkgo.AfterAll(func() {
			fwk.StopManager(ctx)
			fwk.StartManager(ctx, cfg, managerSetup(nil))
		})

		ginkgo.BeforeEach(func() {
			ns = utiltesting.MakeNamespaceWithGenerateName("dra-ext-")
			gomega.Expect(k8sClient.Create(ctx, ns)).To(gomega.Succeed())

			deviceClass = utiltesting.MakeDeviceClass("").GeneratedName("gpu-ext-").
				ExtendedResourceName(extendedResourceName).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, deviceClass)).To(gomega.Succeed())

			resourceFlavor = utiltestingapi.MakeResourceFlavor("").GeneratedName("rf-ext-").Obj()
			gomega.Expect(k8sClient.Create(ctx, resourceFlavor)).To(gomega.Succeed())

			clusterQueue = utiltestingapi.MakeClusterQueue("").GeneratedName("ext-cq-").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas(resourceFlavor.Name).
						Resource(corev1.ResourceName(extendedResourceName), "4").
						Obj(),
				).Obj()
			gomega.Expect(k8sClient.Create(ctx, clusterQueue)).To(gomega.Succeed())
			util.ExpectClusterQueuesToBeActive(ctx, k8sClient, clusterQueue)

			localQueue = utiltestingapi.MakeLocalQueue("ext-lq", ns.Name).
				ClusterQueue(clusterQueue.Name).Obj()
			gomega.Expect(k8sClient.Create(ctx, localQueue)).To(gomega.Succeed())
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			util.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, resourceFlavor, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, deviceClass, true)
		})

		ginkgo.It("Should admit workload with DRA-backed extended resource", func() {
			ginkgo.By("Creating a workload requesting DRA-backed extended resource")
			wl := utiltestingapi.MakeWorkload("ext-res-wl", ns.Name).
				Queue("ext-lq").
				Request(corev1.ResourceName(extendedResourceName), "2").
				Obj()
			gomega.Expect(k8sClient.Create(ctx, wl)).To(gomega.Succeed())

			ginkgo.By("Verifying workload is admitted with extendedResourceName as quota key")
			gomega.Eventually(func(g gomega.Gomega) {
				var updatedWl kueue.Workload
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &updatedWl)).To(gomega.Succeed())
				g.Expect(workload.HasQuotaReservation(&updatedWl)).To(gomega.BeTrue())
				g.Expect(updatedWl.Status.Admission).NotTo(gomega.BeNil())
				g.Expect(updatedWl.Status.Admission.PodSetAssignments).To(gomega.HaveLen(1))

				assignment := updatedWl.Status.Admission.PodSetAssignments[0]
				g.Expect(assignment.ResourceUsage).To(gomega.HaveKey(corev1.ResourceName(extendedResourceName)))
				g.Expect(assignment.ResourceUsage[corev1.ResourceName(extendedResourceName)]).To(gomega.Equal(resource.MustParse("2")))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})
	})

	ginkgo.When("Extended Resources with unified quota via deviceClassMappings", func() {
		var (
			ns             *corev1.Namespace
			resourceFlavor *kueue.ResourceFlavor
			clusterQueue   *kueue.ClusterQueue
			localQueue     *kueue.LocalQueue
			deviceClass    *resourcev1.DeviceClass
		)

		const (
			extendedResourceName = "example.com/gpu"
			logicalName          = "gpu-claims"
		)

		ginkgo.BeforeAll(func() {
			features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.KueueDRAIntegration, true)
			features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.KueueDRAIntegrationExtendedResource, true)

			deviceClass = utiltesting.MakeDeviceClass("gpu.example.com").
				ExtendedResourceName(extendedResourceName).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, deviceClass)).To(gomega.Succeed())

			fwk.StopManager(ctx)
			fwk.StartManager(ctx, cfg, managerSetup(func(c *config.Configuration) {
				c.Resources.DeviceClassMappings = append(c.Resources.DeviceClassMappings, config.DeviceClassMapping{
					Name:             logicalName,
					DeviceClassNames: []corev1.ResourceName{"gpu.example.com"},
				})
			}))
		})

		ginkgo.AfterAll(func() {
			fwk.StopManager(ctx)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, deviceClass, true)
			fwk.StartManager(ctx, cfg, managerSetup(nil))
		})

		ginkgo.BeforeEach(func() {
			ns = utiltesting.MakeNamespaceWithGenerateName("dra-unified-")
			gomega.Expect(k8sClient.Create(ctx, ns)).To(gomega.Succeed())

			resourceFlavor = utiltestingapi.MakeResourceFlavor("").GeneratedName("rf-unified-").Obj()
			gomega.Expect(k8sClient.Create(ctx, resourceFlavor)).To(gomega.Succeed())

			clusterQueue = utiltestingapi.MakeClusterQueue("").GeneratedName("unified-cq-").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas(resourceFlavor.Name).
						Resource(logicalName, "8").
						Obj(),
				).Obj()
			gomega.Expect(k8sClient.Create(ctx, clusterQueue)).To(gomega.Succeed())
			util.ExpectClusterQueuesToBeActive(ctx, k8sClient, clusterQueue)

			localQueue = utiltestingapi.MakeLocalQueue("unified-lq", ns.Name).
				ClusterQueue(clusterQueue.Name).Obj()
			gomega.Expect(k8sClient.Create(ctx, localQueue)).To(gomega.Succeed())
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			util.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, resourceFlavor, true)
		})

		ginkgo.It("Should use deviceClassMappings logical name as quota key", func() {
			wl := utiltestingapi.MakeWorkload("unified-wl", ns.Name).
				Queue("unified-lq").
				Request(corev1.ResourceName(extendedResourceName), "1").
				Obj()
			gomega.Expect(k8sClient.Create(ctx, wl)).To(gomega.Succeed())

			gomega.Eventually(func(g gomega.Gomega) {
				var updatedWl kueue.Workload
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &updatedWl)).To(gomega.Succeed())
				g.Expect(workload.HasQuotaReservation(&updatedWl)).To(gomega.BeTrue())
				g.Expect(updatedWl.Status.Admission).NotTo(gomega.BeNil())
				g.Expect(updatedWl.Status.Admission.PodSetAssignments).To(gomega.HaveLen(1))

				assignment := updatedWl.Status.Admission.PodSetAssignments[0]
				g.Expect(assignment.ResourceUsage).To(gomega.HaveKey(corev1.ResourceName(logicalName)))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})
	})

	ginkgo.When("KueueDRAIntegrationExtendedResource feature gate disabled", func() {
		var (
			ns             *corev1.Namespace
			resourceFlavor *kueue.ResourceFlavor
			clusterQueue   *kueue.ClusterQueue
			localQueue     *kueue.LocalQueue
		)

		const extendedResourceName = "example.com/gpu"

		ginkgo.BeforeAll(func() {
			features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.KueueDRAIntegration, true)
			features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.KueueDRAIntegrationExtendedResource, false)
			fwk.StopManager(ctx)
			fwk.StartManager(ctx, cfg, managerSetup(nil))
		})

		ginkgo.AfterAll(func() {
			fwk.StopManager(ctx)
			fwk.StartManager(ctx, cfg, managerSetup(nil))
		})

		ginkgo.BeforeEach(func() {
			ns = utiltesting.MakeNamespaceWithGenerateName("dra-gate-off-")
			gomega.Expect(k8sClient.Create(ctx, ns)).To(gomega.Succeed())

			resourceFlavor = utiltestingapi.MakeResourceFlavor("").GeneratedName("rf-gate-off-").Obj()
			gomega.Expect(k8sClient.Create(ctx, resourceFlavor)).To(gomega.Succeed())

			clusterQueue = utiltestingapi.MakeClusterQueue("").GeneratedName("gate-off-cq-").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas(resourceFlavor.Name).
						Resource(extendedResourceName, "4").
						Obj(),
				).Obj()
			gomega.Expect(k8sClient.Create(ctx, clusterQueue)).To(gomega.Succeed())
			util.ExpectClusterQueuesToBeActive(ctx, k8sClient, clusterQueue)

			localQueue = utiltestingapi.MakeLocalQueue("gate-off-lq", ns.Name).
				ClusterQueue(clusterQueue.Name).Obj()
			gomega.Expect(k8sClient.Create(ctx, localQueue)).To(gomega.Succeed())
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			util.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, resourceFlavor, true)
		})

		ginkgo.It("Should treat extended resources as normal resources when gate is off", func() {
			wl := utiltestingapi.MakeWorkload("gate-off-wl", ns.Name).
				Queue("gate-off-lq").
				Request(corev1.ResourceName(extendedResourceName), "2").
				Obj()
			gomega.Expect(k8sClient.Create(ctx, wl)).To(gomega.Succeed())

			gomega.Eventually(func(g gomega.Gomega) {
				var updatedWl kueue.Workload
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &updatedWl)).To(gomega.Succeed())
				g.Expect(workload.HasQuotaReservation(&updatedWl)).To(gomega.BeTrue())
				g.Expect(updatedWl.Status.Admission).NotTo(gomega.BeNil())
				g.Expect(updatedWl.Status.Admission.PodSetAssignments).To(gomega.HaveLen(1))

				assignment := updatedWl.Status.Admission.PodSetAssignments[0]
				g.Expect(assignment.ResourceUsage).To(gomega.HaveKey(corev1.ResourceName(extendedResourceName)))
				g.Expect(assignment.ResourceUsage[corev1.ResourceName(extendedResourceName)]).To(gomega.Equal(resource.MustParse("2")))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})
	})

	ginkgo.When("KueueDRAIntegration disabled with KueueDRARejectWorkloadsWhenDRADisabled enabled", func() {
		var (
			ns             *corev1.Namespace
			resourceFlavor *kueue.ResourceFlavor
			clusterQueue   *kueue.ClusterQueue
			localQueue     *kueue.LocalQueue
		)

		ginkgo.BeforeEach(func() {
			features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.KueueDRAIntegration, false)
			features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.KueueDRARejectWorkloadsWhenDRADisabled, true)
			ns = utiltesting.MakeNamespaceWithGenerateName("dra-reject-")
			gomega.Expect(k8sClient.Create(ctx, ns)).To(gomega.Succeed())

			resourceFlavor = utiltestingapi.MakeResourceFlavor("").GeneratedName("rf-reject-").Obj()
			gomega.Expect(k8sClient.Create(ctx, resourceFlavor)).To(gomega.Succeed())

			clusterQueue = utiltestingapi.MakeClusterQueue("").GeneratedName("reject-cq-").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas(resourceFlavor.Name).
						Resource(corev1.ResourceCPU, "4").
						Resource(corev1.ResourceMemory, "4Gi").
						Obj(),
				).Obj()
			gomega.Expect(k8sClient.Create(ctx, clusterQueue)).To(gomega.Succeed())
			util.ExpectClusterQueuesToBeActive(ctx, k8sClient, clusterQueue)

			localQueue = utiltestingapi.MakeLocalQueue("reject-lq", ns.Name).
				ClusterQueue(clusterQueue.Name).Obj()
			gomega.Expect(k8sClient.Create(ctx, localQueue)).To(gomega.Succeed())
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			util.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, resourceFlavor, true)
		})

		ginkgo.It("Should reject workload with ResourceClaimTemplate when DRA is disabled", func() {
			wl := utiltestingapi.MakeWorkload("reject-rct-wl", ns.Name).
				Queue("reject-lq").
				PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
					ResourceClaimTemplate("gpu", "gpu-template").
					Obj()).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, wl)).To(gomega.Succeed())

			gomega.Eventually(func(g gomega.Gomega) {
				var updatedWl kueue.Workload
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &updatedWl)).To(gomega.Succeed())
				g.Expect(workload.HasQuotaReservation(&updatedWl)).To(gomega.BeFalse())

				g.Expect(updatedWl.Status.Conditions).To(gomega.ContainElement(gomega.And(
					gomega.HaveField("Type", kueue.WorkloadQuotaReserved),
					gomega.HaveField("Status", metav1.ConditionFalse),
					gomega.HaveField("Reason", kueue.WorkloadInadmissible),
					gomega.HaveField("Message", gomega.ContainSubstring("KueueDRAIntegration feature gate is not enabled")),
				)))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.It("Should reject workload with ResourceClaim when DRA is disabled", func() {
			wl := utiltestingapi.MakeWorkload("reject-rc-wl", ns.Name).
				Queue("reject-lq").
				Obj()
			wl.Spec.PodSets[0].Template.Spec.ResourceClaims = []corev1.PodResourceClaim{
				{Name: "device", ResourceClaimName: new("test-rc")},
			}
			gomega.Expect(k8sClient.Create(ctx, wl)).To(gomega.Succeed())

			gomega.Eventually(func(g gomega.Gomega) {
				var updatedWl kueue.Workload
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &updatedWl)).To(gomega.Succeed())
				g.Expect(workload.HasQuotaReservation(&updatedWl)).To(gomega.BeFalse())

				g.Expect(updatedWl.Status.Conditions).To(gomega.ContainElement(gomega.And(
					gomega.HaveField("Type", kueue.WorkloadQuotaReserved),
					gomega.HaveField("Status", metav1.ConditionFalse),
					gomega.HaveField("Reason", kueue.WorkloadInadmissible),
					gomega.HaveField("Message", gomega.ContainSubstring("KueueDRAIntegration feature gate is not enabled")),
				)))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.It("Should admit workload without DRA resources when DRA is disabled", func() {
			wl := utiltestingapi.MakeWorkload("no-dra-wl", ns.Name).
				Queue("reject-lq").
				Request(corev1.ResourceCPU, "100m").
				Request(corev1.ResourceMemory, "100Mi").
				Obj()
			gomega.Expect(k8sClient.Create(ctx, wl)).To(gomega.Succeed())

			gomega.Eventually(func(g gomega.Gomega) {
				var updatedWl kueue.Workload
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &updatedWl)).To(gomega.Succeed())
				g.Expect(workload.HasQuotaReservation(&updatedWl)).To(gomega.BeTrue())
				g.Expect(updatedWl.Status.Admission).NotTo(gomega.BeNil())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})
	})

	ginkgo.When("Event-driven DeviceClass tracking for Extended Resources", func() {
		var (
			ns             *corev1.Namespace
			resourceFlavor *kueue.ResourceFlavor
			clusterQueue   *kueue.ClusterQueue
			localQueue     *kueue.LocalQueue
		)

		const (
			extendedResourceName = "example.com/gpu"
			logicalName          = "gpu-claims"
			deviceClassName      = "gpu.example.com"
		)

		ginkgo.BeforeAll(func() {
			fwk.StopManager(ctx)
			fwk.StartManager(ctx, cfg, managerSetup(func(c *config.Configuration) {
				c.Resources.DeviceClassMappings = append(c.Resources.DeviceClassMappings, config.DeviceClassMapping{
					Name:             logicalName,
					DeviceClassNames: []corev1.ResourceName{deviceClassName},
				})
			}))
		})

		ginkgo.AfterAll(func() {
			fwk.StopManager(ctx)
			fwk.StartManager(ctx, cfg, managerSetup(nil))
		})

		ginkgo.BeforeEach(func() {
			ns = utiltesting.MakeNamespaceWithGenerateName("dra-dc-tracking-")
			gomega.Expect(k8sClient.Create(ctx, ns)).To(gomega.Succeed())

			resourceFlavor = utiltestingapi.MakeResourceFlavor("").GeneratedName("rf-dc-tracking-").Obj()
			gomega.Expect(k8sClient.Create(ctx, resourceFlavor)).To(gomega.Succeed())

			clusterQueue = utiltestingapi.MakeClusterQueue("").GeneratedName("dc-tracking-cq-").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas(resourceFlavor.Name).
						Resource(logicalName, "8").
						Obj(),
				).Obj()
			gomega.Expect(k8sClient.Create(ctx, clusterQueue)).To(gomega.Succeed())
			util.ExpectClusterQueuesToBeActive(ctx, k8sClient, clusterQueue)

			localQueue = utiltestingapi.MakeLocalQueue("dc-tracking-lq", ns.Name).
				ClusterQueue(clusterQueue.Name).Obj()
			gomega.Expect(k8sClient.Create(ctx, localQueue)).To(gomega.Succeed())
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			util.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, resourceFlavor, true)
		})

		ginkgo.It("Should requeue inadmissible workload when DeviceClass is created", func() {
			ginkgo.By("Creating workload before DeviceClass exists")
			wl := utiltestingapi.MakeWorkload("dc-create-wl", ns.Name).
				Queue("dc-tracking-lq").
				Request(corev1.ResourceName(extendedResourceName), "2").
				Obj()
			gomega.Expect(k8sClient.Create(ctx, wl)).To(gomega.Succeed())

			ginkgo.By("Verifying workload is pending (no DeviceClass, no translation)")
			util.ExpectPendingWorkloadsMetric(clusterQueue, 0, 1)

			ginkgo.By("Creating DeviceClass with extendedResourceName")
			deviceClass := utiltesting.MakeDeviceClass(deviceClassName).
				ExtendedResourceName(extendedResourceName).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, deviceClass)).To(gomega.Succeed())
			defer func() {
				util.ExpectObjectToBeDeleted(ctx, k8sClient, deviceClass, true)
			}()

			ginkgo.By("Verifying workload is requeued and admitted with logical name as quota key")
			gomega.Eventually(func(g gomega.Gomega) {
				var updatedWl kueue.Workload
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &updatedWl)).To(gomega.Succeed())
				g.Expect(workload.HasQuotaReservation(&updatedWl)).To(gomega.BeTrue())
				g.Expect(updatedWl.Status.Admission).NotTo(gomega.BeNil())
				g.Expect(updatedWl.Status.Admission.PodSetAssignments).To(gomega.HaveLen(1))

				assignment := updatedWl.Status.Admission.PodSetAssignments[0]
				g.Expect(assignment.ResourceUsage).To(gomega.HaveKey(corev1.ResourceName(logicalName)))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.It("Should not admit new workload after DeviceClass is deleted", func() {
			ginkgo.By("Creating DeviceClass")
			deviceClass := utiltesting.MakeDeviceClass(deviceClassName).
				ExtendedResourceName(extendedResourceName).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, deviceClass)).To(gomega.Succeed())

			ginkgo.By("Creating first workload and verifying admission")
			wl1 := utiltestingapi.MakeWorkload("dc-delete-wl1", ns.Name).
				Queue("dc-tracking-lq").
				Request(corev1.ResourceName(extendedResourceName), "2").
				Obj()
			gomega.Expect(k8sClient.Create(ctx, wl1)).To(gomega.Succeed())

			gomega.Eventually(func(g gomega.Gomega) {
				var updatedWl kueue.Workload
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl1), &updatedWl)).To(gomega.Succeed())
				g.Expect(workload.HasQuotaReservation(&updatedWl)).To(gomega.BeTrue())
				assignment := updatedWl.Status.Admission.PodSetAssignments[0]
				g.Expect(assignment.ResourceUsage).To(gomega.HaveKey(corev1.ResourceName(logicalName)))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			ginkgo.By("Deleting DeviceClass")
			util.ExpectObjectToBeDeleted(ctx, k8sClient, deviceClass, true)

			ginkgo.By("Creating second workload and verifying it is not admitted")
			wl2 := utiltestingapi.MakeWorkload("dc-delete-wl2", ns.Name).
				Queue("dc-tracking-lq").
				Request(corev1.ResourceName(extendedResourceName), "2").
				Obj()
			gomega.Expect(k8sClient.Create(ctx, wl2)).To(gomega.Succeed())

			util.ExpectPendingWorkloadsMetric(clusterQueue, 0, 1)
		})

		ginkgo.It("Should requeue inadmissible workload when DeviceClass is deleted", func() {
			ginkgo.By("Creating DeviceClass")
			deviceClass := utiltesting.MakeDeviceClass(deviceClassName).
				ExtendedResourceName(extendedResourceName).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, deviceClass)).To(gomega.Succeed())

			ginkgo.By("Creating first workload to fill quota")
			wl1 := utiltestingapi.MakeWorkload("dc-delete-fill", ns.Name).
				Queue("dc-tracking-lq").
				Request(corev1.ResourceName(extendedResourceName), "8").
				Obj()
			gomega.Expect(k8sClient.Create(ctx, wl1)).To(gomega.Succeed())

			gomega.Eventually(func(g gomega.Gomega) {
				var updatedWl kueue.Workload
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl1), &updatedWl)).To(gomega.Succeed())
				g.Expect(workload.HasQuotaReservation(&updatedWl)).To(gomega.BeTrue())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			ginkgo.By("Creating second workload that is pending due to quota")
			wl2 := utiltestingapi.MakeWorkload("dc-delete-pending", ns.Name).
				Queue("dc-tracking-lq").
				Request(corev1.ResourceName(extendedResourceName), "2").
				Obj()
			gomega.Expect(k8sClient.Create(ctx, wl2)).To(gomega.Succeed())

			util.ExpectPendingWorkloadsMetric(clusterQueue, 0, 1)

			ginkgo.By("Deleting DeviceClass")
			util.ExpectObjectToBeDeleted(ctx, k8sClient, deviceClass, true)

			ginkgo.By("Verifying pending workload is re-evaluated without DRA translation")
			gomega.Eventually(func(g gomega.Gomega) {
				var updatedWl kueue.Workload
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl2), &updatedWl)).To(gomega.Succeed())
				g.Expect(workload.HasQuotaReservation(&updatedWl)).To(gomega.BeFalse())
				cond := apimeta.FindStatusCondition(updatedWl.Status.Conditions, kueue.WorkloadQuotaReserved)
				g.Expect(cond).NotTo(gomega.BeNil())
				g.Expect(cond.Message).To(gomega.ContainSubstring(string(extendedResourceName)))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.It("Should clear stale DRA TotalRequests when admitted workload is requeued after DeviceClass deletion", func() {
			ginkgo.By("Creating DeviceClass")
			deviceClass := utiltesting.MakeDeviceClass(deviceClassName).
				ExtendedResourceName(extendedResourceName).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, deviceClass)).To(gomega.Succeed())

			ginkgo.By("Creating workload and verifying it is admitted with DRA-translated quota key")
			wl := utiltestingapi.MakeWorkload("stale-dra-wl", ns.Name).
				Queue("dc-tracking-lq").
				Request(corev1.ResourceName(extendedResourceName), "2").
				Obj()
			gomega.Expect(k8sClient.Create(ctx, wl)).To(gomega.Succeed())

			gomega.Eventually(func(g gomega.Gomega) {
				var updatedWl kueue.Workload
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &updatedWl)).To(gomega.Succeed())
				g.Expect(workload.HasQuotaReservation(&updatedWl)).To(gomega.BeTrue())
				assignment := updatedWl.Status.Admission.PodSetAssignments[0]
				g.Expect(assignment.ResourceUsage).To(gomega.HaveKey(corev1.ResourceName(logicalName)),
					"workload should be admitted with DRA-translated logical name %q", logicalName)
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			ginkgo.By("Deleting DeviceClass so workload is no longer DRA-backed")
			util.ExpectObjectToBeDeleted(ctx, k8sClient, deviceClass, true)

			ginkgo.By("Evicting admitted workload to trigger requeue with stale TotalRequests")
			util.SetQuotaReservation(ctx, k8sClient, client.ObjectKeyFromObject(wl), nil)

			ginkgo.By("Verifying requeued workload uses raw ER name, not stale DRA translation")
			gomega.Eventually(func(g gomega.Gomega) {
				var updatedWl kueue.Workload
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &updatedWl)).To(gomega.Succeed())
				g.Expect(workload.HasQuotaReservation(&updatedWl)).To(gomega.BeFalse())
				cond := apimeta.FindStatusCondition(updatedWl.Status.Conditions, kueue.WorkloadQuotaReserved)
				g.Expect(cond).NotTo(gomega.BeNil())
				g.Expect(cond.Message).To(gomega.ContainSubstring(string(extendedResourceName)),
					"requeued workload should reference raw ER %q, not stale DRA logical name", extendedResourceName)
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.It("Should requeue inadmissible workload when DeviceClass extendedResourceName is updated", func() {
			const newExtendedResourceName = "example.com/tpu"

			ginkgo.By("Creating DeviceClass with extendedResourceName for gpu")
			deviceClass := utiltesting.MakeDeviceClass(deviceClassName).
				ExtendedResourceName(extendedResourceName).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, deviceClass)).To(gomega.Succeed())
			defer func() {
				util.ExpectObjectToBeDeleted(ctx, k8sClient, deviceClass, true)
			}()

			ginkgo.By("Creating workload requesting tpu (no matching DeviceClass yet)")
			wl := utiltestingapi.MakeWorkload("dc-update-wl", ns.Name).
				Queue("dc-tracking-lq").
				Request(corev1.ResourceName(newExtendedResourceName), "2").
				Obj()
			gomega.Expect(k8sClient.Create(ctx, wl)).To(gomega.Succeed())

			ginkgo.By("Verifying workload is pending")
			util.ExpectPendingWorkloadsMetric(clusterQueue, 0, 1)

			ginkgo.By("Updating DeviceClass extendedResourceName to tpu")
			gomega.Eventually(func(g gomega.Gomega) {
				var dc resourcev1.DeviceClass
				g.Expect(k8sClient.Get(ctx, client.ObjectKey{Name: deviceClassName}, &dc)).To(gomega.Succeed())
				dc.Spec.ExtendedResourceName = ptr.To(newExtendedResourceName)
				g.Expect(k8sClient.Update(ctx, &dc)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			ginkgo.By("Verifying workload is requeued and admitted")
			gomega.Eventually(func(g gomega.Gomega) {
				var updatedWl kueue.Workload
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &updatedWl)).To(gomega.Succeed())
				g.Expect(workload.HasQuotaReservation(&updatedWl)).To(gomega.BeTrue())
				g.Expect(updatedWl.Status.Admission).NotTo(gomega.BeNil())
				assignment := updatedWl.Status.Admission.PodSetAssignments[0]
				g.Expect(assignment.ResourceUsage).To(gomega.HaveKey(corev1.ResourceName(logicalName)))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.It("Should requeue workload requesting old extendedResourceName when DeviceClass is updated", func() {
			const newExtendedResourceName = "example.com/other-accelerator"

			ginkgo.By("Creating DeviceClass with extendedResourceName for gpu")
			deviceClass := utiltesting.MakeDeviceClass(deviceClassName).
				ExtendedResourceName(extendedResourceName).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, deviceClass)).To(gomega.Succeed())
			defer func() {
				util.ExpectObjectToBeDeleted(ctx, k8sClient, deviceClass, true)
			}()

			ginkgo.By("Creating workload to fill quota")
			wlFill := utiltestingapi.MakeWorkload("dc-update-old-fill", ns.Name).
				Queue("dc-tracking-lq").
				Request(corev1.ResourceName(extendedResourceName), "8").
				Obj()
			gomega.Expect(k8sClient.Create(ctx, wlFill)).To(gomega.Succeed())

			gomega.Eventually(func(g gomega.Gomega) {
				var updatedWl kueue.Workload
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wlFill), &updatedWl)).To(gomega.Succeed())
				g.Expect(workload.HasQuotaReservation(&updatedWl)).To(gomega.BeTrue())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			ginkgo.By("Creating pending workload requesting gpu (quota full)")
			wlPending := utiltestingapi.MakeWorkload("dc-update-old-pending", ns.Name).
				Queue("dc-tracking-lq").
				Request(corev1.ResourceName(extendedResourceName), "2").
				Obj()
			gomega.Expect(k8sClient.Create(ctx, wlPending)).To(gomega.Succeed())

			util.ExpectPendingWorkloadsMetric(clusterQueue, 0, 1)

			ginkgo.By("Updating DeviceClass extendedResourceName away from gpu")
			gomega.Eventually(func(g gomega.Gomega) {
				var dc resourcev1.DeviceClass
				g.Expect(k8sClient.Get(ctx, client.ObjectKey{Name: deviceClassName}, &dc)).To(gomega.Succeed())
				dc.Spec.ExtendedResourceName = ptr.To(newExtendedResourceName)
				g.Expect(k8sClient.Update(ctx, &dc)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			ginkgo.By("Verifying pending workload is re-evaluated without DRA translation")
			gomega.Eventually(func(g gomega.Gomega) {
				var updatedWl kueue.Workload
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wlPending), &updatedWl)).To(gomega.Succeed())
				g.Expect(workload.HasQuotaReservation(&updatedWl)).To(gomega.BeFalse())
				cond := apimeta.FindStatusCondition(updatedWl.Status.Conditions, kueue.WorkloadQuotaReserved)
				g.Expect(cond).NotTo(gomega.BeNil())
				g.Expect(cond.Message).To(gomega.ContainSubstring(string(extendedResourceName)))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.It("Should requeue inadmissible workload when DeviceClass extendedResourceName is added", func() {
			ginkgo.By("Creating DeviceClass without extendedResourceName")
			deviceClass := utiltesting.MakeDeviceClass(deviceClassName).Obj()
			gomega.Expect(k8sClient.Create(ctx, deviceClass)).To(gomega.Succeed())
			defer func() {
				util.ExpectObjectToBeDeleted(ctx, k8sClient, deviceClass, true)
			}()

			ginkgo.By("Creating workload requesting extended resource")
			wl := utiltestingapi.MakeWorkload("dc-add-ern-wl", ns.Name).
				Queue("dc-tracking-lq").
				Request(corev1.ResourceName(extendedResourceName), "2").
				Obj()
			gomega.Expect(k8sClient.Create(ctx, wl)).To(gomega.Succeed())

			ginkgo.By("Verifying workload is pending")
			util.ExpectPendingWorkloadsMetric(clusterQueue, 0, 1)

			ginkgo.By("Updating DeviceClass to add extendedResourceName")
			gomega.Eventually(func(g gomega.Gomega) {
				var dc resourcev1.DeviceClass
				g.Expect(k8sClient.Get(ctx, client.ObjectKey{Name: deviceClassName}, &dc)).To(gomega.Succeed())
				dc.Spec.ExtendedResourceName = ptr.To(extendedResourceName)
				g.Expect(k8sClient.Update(ctx, &dc)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			ginkgo.By("Verifying workload is requeued and admitted")
			gomega.Eventually(func(g gomega.Gomega) {
				var updatedWl kueue.Workload
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &updatedWl)).To(gomega.Succeed())
				g.Expect(workload.HasQuotaReservation(&updatedWl)).To(gomega.BeTrue())
				g.Expect(updatedWl.Status.Admission).NotTo(gomega.BeNil())
				assignment := updatedWl.Status.Admission.PodSetAssignments[0]
				g.Expect(assignment.ResourceUsage).To(gomega.HaveKey(corev1.ResourceName(logicalName)))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})
	})

	ginkgo.When("DRA resources in a cohort", func() {
		var (
			ns             *corev1.Namespace
			resourceFlavor *kueue.ResourceFlavor
			prodCQ         *kueue.ClusterQueue
			devCQ          *kueue.ClusterQueue
			prodLQ         *kueue.LocalQueue
			devLQ          *kueue.LocalQueue
			deviceClass    *resourcev1.DeviceClass
		)

		ginkgo.BeforeEach(func() {
			ns = utiltesting.MakeNamespaceWithGenerateName("dra-borrow-")
			gomega.Expect(k8sClient.Create(ctx, ns)).To(gomega.Succeed())

			deviceClass = utiltesting.MakeDeviceClass("foo.example.com").Obj()
			gomega.Expect(k8sClient.Create(ctx, deviceClass)).To(gomega.Succeed())

			resourceFlavor = utiltestingapi.MakeResourceFlavor("").GeneratedName("rf-borrow-").Obj()
			gomega.Expect(k8sClient.Create(ctx, resourceFlavor)).To(gomega.Succeed())

			prodCQ = utiltestingapi.MakeClusterQueue("").GeneratedName("prod-cq-").
				Cohort("dra-cohort").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas(resourceFlavor.Name).
						Resource("foo", "5").
						Obj(),
				).Obj()
			gomega.Expect(k8sClient.Create(ctx, prodCQ)).To(gomega.Succeed())

			devCQ = utiltestingapi.MakeClusterQueue("").GeneratedName("dev-cq-").
				Cohort("dra-cohort").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas(resourceFlavor.Name).
						Resource("foo", "5").
						Obj(),
				).Obj()
			gomega.Expect(k8sClient.Create(ctx, devCQ)).To(gomega.Succeed())

			util.ExpectClusterQueuesToBeActive(ctx, k8sClient, prodCQ, devCQ)

			prodLQ = utiltestingapi.MakeLocalQueue("prod-lq", ns.Name).
				ClusterQueue(prodCQ.Name).Obj()
			gomega.Expect(k8sClient.Create(ctx, prodLQ)).To(gomega.Succeed())

			devLQ = utiltestingapi.MakeLocalQueue("dev-lq", ns.Name).
				ClusterQueue(devCQ.Name).Obj()
			gomega.Expect(k8sClient.Create(ctx, devLQ)).To(gomega.Succeed())
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			util.ExpectObjectToBeDeleted(ctx, k8sClient, prodCQ, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, devCQ, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, resourceFlavor, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, deviceClass, true)
		})

		ginkgo.It("Should borrow DRA quota from another ClusterQueue in the cohort", func() {
			rct := utiltesting.MakeResourceClaimTemplate("borrow-template", ns.Name).
				DeviceRequest("gpu", "foo.example.com", 1).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, rct)).To(gomega.Succeed())

			ginkgo.By("Creating a workload that exceeds prod-cq nominal quota (needs borrowing)")
			wl := utiltestingapi.MakeWorkload("borrow-wl", ns.Name).
				Queue("prod-lq").
				PodSets(*utiltestingapi.MakePodSet("main", 7).
					ResourceClaimTemplate("gpu", "borrow-template").
					Obj()).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, wl)).To(gomega.Succeed())

			ginkgo.By("Verifying workload is admitted and borrows from dev-cq")
			util.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, prodCQ.Name, wl)

			gomega.Eventually(func(g gomega.Gomega) {
				var updatedCQ kueue.ClusterQueue
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(prodCQ), &updatedCQ)).To(gomega.Succeed())
				found := false
				for _, flavorUsage := range updatedCQ.Status.FlavorsUsage {
					for _, ru := range flavorUsage.Resources {
						if ru.Name == "foo" {
							found = true
							g.Expect(ru.Total.Value()).To(gomega.Equal(int64(7)))
							g.Expect(ru.Borrowed.Value()).To(gomega.Equal(int64(2)))
						}
					}
				}
				g.Expect(found).To(gomega.BeTrue(), "resource foo not found in FlavorsUsage")
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.It("Should not admit DRA workload when cohort capacity is exhausted", func() {
			rct := utiltesting.MakeResourceClaimTemplate("exhaust-template", ns.Name).
				DeviceRequest("gpu", "foo.example.com", 1).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, rct)).To(gomega.Succeed())

			ginkgo.By("Filling prod-cq quota")
			prodWl := utiltestingapi.MakeWorkload("prod-full", ns.Name).
				Queue("prod-lq").
				PodSets(*utiltestingapi.MakePodSet("main", 5).
					ResourceClaimTemplate("gpu", "exhaust-template").
					Obj()).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, prodWl)).To(gomega.Succeed())

			ginkgo.By("Filling dev-cq quota")
			devWl := utiltestingapi.MakeWorkload("dev-full", ns.Name).
				Queue("dev-lq").
				PodSets(*utiltestingapi.MakePodSet("main", 5).
					ResourceClaimTemplate("gpu", "exhaust-template").
					Obj()).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, devWl)).To(gomega.Succeed())

			util.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, prodCQ.Name, prodWl)
			util.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, devCQ.Name, devWl)

			ginkgo.By("Creating a workload that exceeds total cohort capacity")
			overflowWl := utiltestingapi.MakeWorkload("overflow-wl", ns.Name).
				Queue("prod-lq").
				PodSets(*utiltestingapi.MakePodSet("main", 1).
					ResourceClaimTemplate("gpu", "exhaust-template").
					Obj()).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, overflowWl)).To(gomega.Succeed())

			ginkgo.By("Verifying overflow workload stays pending")
			util.ExpectWorkloadsToBePending(ctx, k8sClient, overflowWl)
		})
	})
})
