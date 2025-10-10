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
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	config "sigs.k8s.io/kueue/apis/config/v1beta1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	"sigs.k8s.io/kueue/pkg/workload"
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
		)
		ginkgo.BeforeEach(func() {
			ns = &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					GenerateName: "dra-",
				},
			}
			gomega.Expect(k8sClient.Create(ctx, ns)).To(gomega.Succeed())

			resourceFlavor = utiltesting.MakeResourceFlavor("").Obj()
			resourceFlavor.GenerateName = "rf-"
			gomega.Expect(k8sClient.Create(ctx, resourceFlavor)).To(gomega.Succeed())

			clusterQueue = &kueue.ClusterQueue{
				ObjectMeta: metav1.ObjectMeta{
					GenerateName: "test-cq-",
				},
				Spec: kueue.ClusterQueueSpec{
					NamespaceSelector: &metav1.LabelSelector{},
					ResourceGroups: []kueue.ResourceGroup{
						{
							CoveredResources: []corev1.ResourceName{"foo"},
							Flavors: []kueue.FlavorQuotas{
								{
									Name: kueue.ResourceFlavorReference(resourceFlavor.Name),
									Resources: []kueue.ResourceQuota{
										{Name: "foo", NominalQuota: resource.MustParse("10")},
									},
								},
							},
						},
					},
				},
			}
			gomega.Expect(k8sClient.Create(ctx, clusterQueue)).To(gomega.Succeed())
			util.ExpectClusterQueuesToBeActive(ctx, k8sClient, clusterQueue)
			localQueue = utiltesting.MakeLocalQueue("test-lq", ns.Name).
				ClusterQueue(clusterQueue.Name).Obj()
			gomega.Expect(k8sClient.Create(ctx, localQueue)).To(gomega.Succeed())
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			util.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, resourceFlavor, true)
		})

		ginkgo.It("Should reject workload with DRA resource claims with inadmissible condition", func() {
			ginkgo.By("Creating a ResourceClaim")
			rc := utiltesting.MakeResourceClaim("test-rc", ns.Name).
				DeviceRequest("device-request", "foo.example.com", 2).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, rc)).To(gomega.Succeed())

			ginkgo.By("Creating a workload with DRA resource claim")
			wl := utiltesting.MakeWorkload("test-wl", ns.Name).
				Queue("test-lq").
				PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 1).
					ResourceClaim("device", "test-rc").
					Obj()).
				Obj()
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
			wl := utiltesting.MakeWorkload("test-wl-large", ns.Name).
				Queue("test-lq").
				PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 1).
					ResourceClaim("device", "test-rc-large").
					Obj()).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, wl)).To(gomega.Succeed())

			ginkgo.By("Verifying workload remains pending")
			gomega.Consistently(func(g gomega.Gomega) {
				var updatedWl kueue.Workload
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &updatedWl)).To(gomega.Succeed())
				g.Expect(workload.HasQuotaReservation(&updatedWl)).To(gomega.BeFalse())
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
			wl1 := utiltesting.MakeWorkload("test-wl-1", ns.Name).
				Queue("test-lq").
				PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 1).
					ResourceClaimTemplate("device-template", "quota-template-1").
					Obj()).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, wl1)).To(gomega.Succeed())

			ginkgo.By("Creating second workload")
			wl2 := utiltesting.MakeWorkload("test-wl-2", ns.Name).
				Queue("test-lq").
				PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 1).
					ResourceClaimTemplate("device-template", "quota-template-2").
					Obj()).
				Obj()
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
			wl := utiltesting.MakeWorkload("test-wl-template", ns.Name).
				Queue("test-lq").
				PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 1).
					ResourceClaimTemplate("device-template", "device-template").
					Obj()).
				Obj()
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
			wl1 := utiltesting.MakeWorkload("test-wl-template-1", ns.Name).
				Queue("test-lq").
				PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 1).
					ResourceClaimTemplate("device-template", "device-template-1").
					Obj()).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, wl1)).To(gomega.Succeed())

			ginkgo.By("Creating second workload with ResourceClaimTemplate")
			wl2 := utiltesting.MakeWorkload("test-wl-template-2", ns.Name).
				Queue("test-lq").
				PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 1).
					ResourceClaimTemplate("device-template", "device-template-2").
					Obj()).
				Obj()
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
			wl := utiltesting.MakeWorkload("test-wl-template-large", ns.Name).
				Queue("test-lq").
				PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 1).
					ResourceClaimTemplate("device-template", "device-template-large").
					Obj()).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, wl)).To(gomega.Succeed())

			ginkgo.By("Verifying workload with ResourceClaimTemplate remains pending due to quota")
			gomega.Consistently(func(g gomega.Gomega) {
				var updatedWl kueue.Workload
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &updatedWl)).To(gomega.Succeed())
				g.Expect(workload.HasQuotaReservation(&updatedWl)).To(gomega.BeFalse())
			}, util.ConsistentDuration, util.ShortInterval).Should(gomega.Succeed())
		})

		ginkgo.It("Should handle unmapped device classes with proper error", func() {
			ginkgo.By("Creating a ResourceClaimTemplate with unmapped device class")
			rct := utiltesting.MakeResourceClaimTemplate("unmapped-template", ns.Name).
				DeviceRequest("device-request", "unmapped.example.com", 2).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, rct)).To(gomega.Succeed())

			ginkgo.By("Creating a workload with unmapped device class")
			wl := utiltesting.MakeWorkload("test-wl-unmapped", ns.Name).
				Queue("test-lq").
				PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 1).
					ResourceClaimTemplate("device-template", "unmapped-template").
					Obj()).
				Obj()
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
			}, util.LongTimeout, util.Interval).Should(gomega.Succeed())

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
			wl := utiltesting.MakeWorkload("test-wl-multi-pod", ns.Name).
				Queue("test-lq").
				PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 3).
					ResourceClaimTemplate("device-template", "multi-pod-template").
					Obj()).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, wl)).To(gomega.Succeed())

			ginkgo.By("Verifying workload is admitted with correct total DRA resource usage")
			gomega.Eventually(func(g gomega.Gomega) {
				var updatedWl kueue.Workload
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &updatedWl)).To(gomega.Succeed())
				g.Expect(workload.HasQuotaReservation(&updatedWl)).To(gomega.BeTrue())
				g.Expect(updatedWl.Status.Admission).NotTo(gomega.BeNil())
				g.Expect(updatedWl.Status.Admission.PodSetAssignments).To(gomega.HaveLen(1))

				assignment := updatedWl.Status.Admission.PodSetAssignments[0]

				g.Expect(assignment.Count).To(gomega.Equal(ptr.To(int32(3))))

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
			wl := utiltesting.MakeWorkload("test-wl-all-mode", ns.Name).
				Queue("test-lq").
				PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 1).
					ResourceClaimTemplate("device-template", "all-mode-template").
					Obj()).
				Obj()
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
						gomega.ContainSubstring("unsupported DRA feature"),
						gomega.ContainSubstring("AllocationMode 'All'"),
					)),
				)))
			}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.It("Should reject workload with CEL selectors", func() {
			ginkgo.By("Creating a ResourceClaimTemplate with CEL selectors")
			rct := utiltesting.MakeResourceClaimTemplate("cel-selector-template", ns.Name).
				DeviceRequest("device-request", "foo.example.com", 2).
				WithCELSelectors("device.driver == \"test-driver\"").
				Obj()
			gomega.Expect(k8sClient.Create(ctx, rct)).To(gomega.Succeed())

			ginkgo.By("Creating a workload with CEL selectors")
			wl := utiltesting.MakeWorkload("test-wl-cel-selector", ns.Name).
				Queue("test-lq").
				PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 1).
					ResourceClaimTemplate("device-template", "cel-selector-template").
					Obj()).
				Obj()
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
						gomega.ContainSubstring("unsupported DRA feature"),
						gomega.ContainSubstring("CEL selectors"),
					)),
				)))
			}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.It("Should reject workload with device constraints", func() {
			ginkgo.By("Creating a ResourceClaimTemplate with device constraints")
			rct := utiltesting.MakeResourceClaimTemplate("constraint-template", ns.Name).
				DeviceRequest("gpu-1", "foo.example.com", 1).
				DeviceRequest("gpu-2", "foo.example.com", 1).
				WithDeviceConstraints([]string{"gpu-1", "gpu-2"}, "example.com/numa_node").
				Obj()
			gomega.Expect(k8sClient.Create(ctx, rct)).To(gomega.Succeed())

			ginkgo.By("Creating a workload with device constraints")
			wl := utiltesting.MakeWorkload("test-wl-constraint", ns.Name).
				Queue("test-lq").
				PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 1).
					ResourceClaimTemplate("device-template", "constraint-template").
					Obj()).
				Obj()
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
						gomega.ContainSubstring("unsupported DRA feature"),
						gomega.ContainSubstring("device constraints"),
					)),
				)))
			}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
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
			wl := utiltesting.MakeWorkload("test-wl-admin-access", ns.Name).
				Queue("test-lq").
				PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 1).
					ResourceClaimTemplate("device-template", "admin-access-template").
					Obj()).
				Obj()
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
						gomega.ContainSubstring("unsupported DRA feature"),
						gomega.ContainSubstring("AdminAccess"),
					)),
				)))
			}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.It("Should reject workload with device config", func() {
			ginkgo.By("Creating a ResourceClaimTemplate with device config")
			rct := utiltesting.MakeResourceClaimTemplate("device-config-template", ns.Name).
				DeviceRequest("device-request", "foo.example.com", 2).
				WithDeviceConfig("device-request", "driver.example.com", []byte(`{"key":"value"}`)).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, rct)).To(gomega.Succeed())

			ginkgo.By("Creating a workload with device config")
			wl := utiltesting.MakeWorkload("test-wl-device-config", ns.Name).
				Queue("test-lq").
				PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 1).
					ResourceClaimTemplate("device-template", "device-config-template").
					Obj()).
				Obj()
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
						gomega.ContainSubstring("unsupported DRA feature"),
						gomega.ContainSubstring("device config"),
					)),
				)))
			}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.It("Should reject workload with FirstAvailable", func() {
			ginkgo.By("Creating a ResourceClaimTemplate with FirstAvailable")
			rct := utiltesting.MakeResourceClaimTemplate("first-available-template", ns.Name).
				FirstAvailableRequest("device-request", "foo.example.com").
				Obj()
			gomega.Expect(k8sClient.Create(ctx, rct)).To(gomega.Succeed())

			ginkgo.By("Creating a workload with FirstAvailable")
			wl := utiltesting.MakeWorkload("test-wl-first-available", ns.Name).
				Queue("test-lq").
				PodSets(*utiltesting.MakePodSet(kueue.DefaultPodSetName, 1).
					ResourceClaimTemplate("device-template", "first-available-template").
					Obj()).
				Obj()
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
						gomega.ContainSubstring("unsupported DRA feature"),
						gomega.ContainSubstring("FirstAvailable"),
					)),
				)))
			}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
		})
	})
})
