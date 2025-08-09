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
	resourcev1beta2 "k8s.io/api/resource/v1beta2"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("DRA Controller", ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	ginkgo.BeforeAll(func() {
		// Start the manager with controllers for full e2e integration testing
		fwk.StartManager(ctx, cfg, managerSetup)
	})

	ginkgo.AfterAll(func() {
		fwk.StopManager(ctx)
	})

	ginkgo.BeforeEach(func() {
		ctx = context.Background() // Create context for test operations
	})

	ginkgo.When("DRA is enabled", func() {
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

			if draConfig == nil {
				draConfig = &kueuealpha.DynamicResourceAllocationConfig{
					ObjectMeta: metav1.ObjectMeta{
						Name:      "default",
						Namespace: "kueue-system",
					},
					Spec: kueuealpha.DynamicResourceAllocationConfigSpec{
						Resources: []kueuealpha.DynamicResource{
							{
								Name:             "example.com/gpu",
								DeviceClassNames: []corev1.ResourceName{"example.com"},
							},
						},
					},
				}
				gomega.Expect(k8sClient.Create(ctx, draConfig)).To(gomega.Succeed())
			}

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
							CoveredResources: []corev1.ResourceName{"example.com/gpu"},
							Flavors: []kueue.FlavorQuotas{
								{
									Name: kueue.ResourceFlavorReference(resourceFlavor.Name),
									Resources: []kueue.ResourceQuota{
										{Name: "example.com/gpu", NominalQuota: resource.MustParse("10")},
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
			// DRA config is cleaned up in AfterSuite as it's suite-level singleton
		})

		ginkgo.It("Should admit workload with DRA resource claims", func() {
			ginkgo.By("Creating a ResourceClaim")
			rc := makeResourceClaim("test-rc", ns.Name, "example.com", 2)
			gomega.Expect(k8sClient.Create(ctx, rc)).To(gomega.Succeed())

			ginkgo.By("Creating a workload with DRA resource claim")
			wl := utiltesting.MakeWorkload("test-wl", ns.Name).
				Queue("test-lq").
				Obj()
			// Manually add ResourceClaims to PodSet
			wl.Spec.PodSets[0].Template.Spec.ResourceClaims = []corev1.PodResourceClaim{
				{Name: "gpu", ResourceClaimName: ptr.To("test-rc")},
			}
			wl.Spec.PodSets[0].Template.Spec.Containers[0].Resources.Claims = []corev1.ResourceClaim{
				{Name: "gpu"},
			}
			gomega.Expect(k8sClient.Create(ctx, wl)).To(gomega.Succeed())

			ginkgo.By("Verifying workload is admitted to ClusterQueue")
			gomega.Eventually(func(g gomega.Gomega) {
				var updatedWl kueue.Workload
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &updatedWl)).To(gomega.Succeed())
				g.Expect(workload.HasQuotaReservation(&updatedWl)).To(gomega.BeTrue())
				g.Expect(updatedWl.Status.Admission).NotTo(gomega.BeNil())
				g.Expect(updatedWl.Status.Admission.PodSetAssignments).To(gomega.HaveLen(1))

				assignment := updatedWl.Status.Admission.PodSetAssignments[0]
				g.Expect(assignment.ResourceUsage).To(gomega.HaveKey(corev1.ResourceName("example.com/gpu")))
				g.Expect(assignment.ResourceUsage["example.com/gpu"]).To(gomega.Equal(resource.MustParse("2")))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.It("Should handle workload with insufficient DRA quota", func() {
			ginkgo.By("Creating a ResourceClaim that exceeds quota")
			rc := makeResourceClaim("test-rc-large", ns.Name, "example.com", 15) // Exceeds quota of 10
			gomega.Expect(k8sClient.Create(ctx, rc)).To(gomega.Succeed())

			ginkgo.By("Creating a workload with large DRA resource claim")
			wl := utiltesting.MakeWorkload("test-wl-large", ns.Name).
				Queue("test-lq").
				Obj()
			// Manually add ResourceClaims to PodSet
			wl.Spec.PodSets[0].Template.Spec.ResourceClaims = []corev1.PodResourceClaim{
				{Name: "gpu", ResourceClaimName: ptr.To("test-rc-large")},
			}
			wl.Spec.PodSets[0].Template.Spec.Containers[0].Resources.Claims = []corev1.ResourceClaim{
				{Name: "gpu"},
			}
			gomega.Expect(k8sClient.Create(ctx, wl)).To(gomega.Succeed())

			ginkgo.By("Verifying workload remains pending")
			gomega.Consistently(func(g gomega.Gomega) {
				var updatedWl kueue.Workload
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &updatedWl)).To(gomega.Succeed())
				g.Expect(workload.HasQuotaReservation(&updatedWl)).To(gomega.BeFalse())
			}, util.ConsistentDuration, util.ShortInterval).Should(gomega.Succeed())
		})

		ginkgo.It("Should handle multiple workloads sharing DRA quota", func() {
			ginkgo.By("Creating first ResourceClaim")
			rc1 := makeResourceClaim("test-rc-1", ns.Name, "example.com", 4)
			gomega.Expect(k8sClient.Create(ctx, rc1)).To(gomega.Succeed())

			ginkgo.By("Creating second ResourceClaim")
			rc2 := makeResourceClaim("test-rc-2", ns.Name, "example.com", 4)
			gomega.Expect(k8sClient.Create(ctx, rc2)).To(gomega.Succeed())

			ginkgo.By("Creating first workload")
			wl1 := utiltesting.MakeWorkload("test-wl-1", ns.Name).
				Queue("test-lq").
				Obj()
			wl1.Spec.PodSets[0].Template.Spec.ResourceClaims = []corev1.PodResourceClaim{
				{Name: "gpu", ResourceClaimName: ptr.To("test-rc-1")},
			}
			wl1.Spec.PodSets[0].Template.Spec.Containers[0].Resources.Claims = []corev1.ResourceClaim{
				{Name: "gpu"},
			}
			gomega.Expect(k8sClient.Create(ctx, wl1)).To(gomega.Succeed())

			ginkgo.By("Creating second workload")
			wl2 := utiltesting.MakeWorkload("test-wl-2", ns.Name).
				Queue("test-lq").
				Obj()
			wl2.Spec.PodSets[0].Template.Spec.ResourceClaims = []corev1.PodResourceClaim{
				{Name: "gpu", ResourceClaimName: ptr.To("test-rc-2")},
			}
			wl2.Spec.PodSets[0].Template.Spec.Containers[0].Resources.Claims = []corev1.ResourceClaim{
				{Name: "gpu"},
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
			var updatedWl1, updatedWl2 kueue.Workload
			gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl1), &updatedWl1)).To(gomega.Succeed())
			gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl2), &updatedWl2)).To(gomega.Succeed())

			totalUsage := int64(0)
			if updatedWl1.Status.Admission != nil && len(updatedWl1.Status.Admission.PodSetAssignments) > 0 {
				if usage, ok := updatedWl1.Status.Admission.PodSetAssignments[0].ResourceUsage["example.com/gpu"]; ok {
					totalUsage += usage.Value()
				}
			}
			if updatedWl2.Status.Admission != nil && len(updatedWl2.Status.Admission.PodSetAssignments) > 0 {
				if usage, ok := updatedWl2.Status.Admission.PodSetAssignments[0].ResourceUsage["example.com/gpu"]; ok {
					totalUsage += usage.Value()
				}
			}
			gomega.Expect(totalUsage).To(gomega.Equal(int64(8)))
		})

		ginkgo.It("Should reject second workload using same ResourceClaim", func() {
			ginkgo.By("Creating a ResourceClaim")
			rc := makeResourceClaim("shared-rc", ns.Name, "example.com", 2)
			gomega.Expect(k8sClient.Create(ctx, rc)).To(gomega.Succeed())

			ginkgo.By("Creating first workload using the ResourceClaim")
			wl1 := utiltesting.MakeWorkload("test-wl-first", ns.Name).
				Queue("test-lq").
				Obj()
			wl1.Spec.PodSets[0].Template.Spec.ResourceClaims = []corev1.PodResourceClaim{
				{Name: "gpu", ResourceClaimName: ptr.To("shared-rc")},
			}
			wl1.Spec.PodSets[0].Template.Spec.Containers[0].Resources.Claims = []corev1.ResourceClaim{
				{Name: "gpu"},
			}
			gomega.Expect(k8sClient.Create(ctx, wl1)).To(gomega.Succeed())

			ginkgo.By("Verifying first workload is admitted")
			gomega.Eventually(func(g gomega.Gomega) {
				var updatedWl1 kueue.Workload
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl1), &updatedWl1)).To(gomega.Succeed())
				g.Expect(workload.HasQuotaReservation(&updatedWl1)).To(gomega.BeTrue())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			ginkgo.By("Creating second workload using the same ResourceClaim")
			wl2 := utiltesting.MakeWorkload("test-wl-second", ns.Name).
				Queue("test-lq").
				Obj()
			wl2.Spec.PodSets[0].Template.Spec.ResourceClaims = []corev1.PodResourceClaim{
				{Name: "gpu", ResourceClaimName: ptr.To("shared-rc")},
			}
			wl2.Spec.PodSets[0].Template.Spec.Containers[0].Resources.Claims = []corev1.ResourceClaim{
				{Name: "gpu"},
			}
			gomega.Expect(k8sClient.Create(ctx, wl2)).To(gomega.Succeed())

			ginkgo.By("Verifying second workload remains pending due to ResourceClaim conflict")
			gomega.Consistently(func(g gomega.Gomega) {
				var updatedWl2 kueue.Workload
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl2), &updatedWl2)).To(gomega.Succeed())
				g.Expect(workload.HasQuotaReservation(&updatedWl2)).To(gomega.BeFalse())
			}, util.ConsistentDuration, util.ShortInterval).Should(gomega.Succeed())
		})

		ginkgo.It("Should handle DRA config updates", func() {
			ginkgo.By("Creating initial workload with DRA")
			rc := makeResourceClaim("test-rc", ns.Name, "example.com", 2)
			gomega.Expect(k8sClient.Create(ctx, rc)).To(gomega.Succeed())

			wl := utiltesting.MakeWorkload("test-wl", ns.Name).
				Queue("test-lq").
				Obj()
			wl.Spec.PodSets[0].Template.Spec.ResourceClaims = []corev1.PodResourceClaim{
				{Name: "gpu", ResourceClaimName: ptr.To("test-rc")},
			}
			wl.Spec.PodSets[0].Template.Spec.Containers[0].Resources.Claims = []corev1.ResourceClaim{
				{Name: "gpu"},
			}
			gomega.Expect(k8sClient.Create(ctx, wl)).To(gomega.Succeed())

			ginkgo.By("Verifying workload is admitted")
			gomega.Eventually(func(g gomega.Gomega) {
				var updatedWl kueue.Workload
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &updatedWl)).To(gomega.Succeed())
				g.Expect(workload.HasQuotaReservation(&updatedWl)).To(gomega.BeTrue())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			ginkgo.By("Updating DRA config to add new resource mapping")
			// Get the suite-level singleton DRA config
			suiteDRAConfigKey := client.ObjectKey{Name: "default", Namespace: "kueue-system"}
			var updatedDRAConfig kueuealpha.DynamicResourceAllocationConfig
			gomega.Expect(k8sClient.Get(ctx, suiteDRAConfigKey, &updatedDRAConfig)).To(gomega.Succeed())

			updatedDRAConfig.Spec.Resources = append(updatedDRAConfig.Spec.Resources, kueuealpha.DynamicResource{
				Name:             "example.com/memory",
				DeviceClassNames: []corev1.ResourceName{"example.com/bar"},
			})
			gomega.Expect(k8sClient.Update(ctx, &updatedDRAConfig)).To(gomega.Succeed())

			ginkgo.By("Verifying existing workload remains admitted after config update")
			gomega.Consistently(func(g gomega.Gomega) {
				var currentWl kueue.Workload
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &currentWl)).To(gomega.Succeed())
				g.Expect(workload.HasQuotaReservation(&currentWl)).To(gomega.BeTrue())
			}, util.ConsistentDuration, util.ShortInterval).Should(gomega.Succeed())
		})

		ginkgo.It("Should admit workload with DRA resource claim templates", func() {
			ginkgo.By("Creating a ResourceClaimTemplate")
			rct := &resourcev1beta2.ResourceClaimTemplate{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "gpu-template",
					Namespace: ns.Name,
				},
				Spec: resourcev1beta2.ResourceClaimTemplateSpec{
					Spec: resourcev1beta2.ResourceClaimSpec{
						Devices: resourcev1beta2.DeviceClaim{
							Requests: []resourcev1beta2.DeviceRequest{{
								Name: "gpu-request",
								Exactly: &resourcev1beta2.ExactDeviceRequest{
									DeviceClassName: "example.com",
									AllocationMode:  resourcev1beta2.DeviceAllocationModeExactCount,
									Count:           2,
								},
							}},
						},
					},
				},
			}
			gomega.Expect(k8sClient.Create(ctx, rct)).To(gomega.Succeed())

			ginkgo.By("Creating a workload that references the ResourceClaimTemplate")
			wl := utiltesting.MakeWorkload("test-wl-template", ns.Name).
				Queue("test-lq").
				Obj()

			// Reference the ResourceClaimTemplate by name
			wl.Spec.PodSets[0].Template.Spec.ResourceClaims = []corev1.PodResourceClaim{
				{
					Name:                      "gpu-template",
					ResourceClaimTemplateName: ptr.To("gpu-template"),
				},
			}
			wl.Spec.PodSets[0].Template.Spec.Containers[0].Resources.Claims = []corev1.ResourceClaim{
				{Name: "gpu-template"},
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
				g.Expect(assignment.ResourceUsage).To(gomega.HaveKey(corev1.ResourceName("example.com/gpu")))
				g.Expect(assignment.ResourceUsage["example.com/gpu"]).To(gomega.Equal(resource.MustParse("2")))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.It("Should handle multiple workloads with ResourceClaimTemplates", func() {
			ginkgo.By("Creating ResourceClaimTemplates")
			rct1 := &resourcev1beta2.ResourceClaimTemplate{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "gpu-template-1",
					Namespace: ns.Name,
				},
				Spec: resourcev1beta2.ResourceClaimTemplateSpec{
					Spec: resourcev1beta2.ResourceClaimSpec{
						Devices: resourcev1beta2.DeviceClaim{
							Requests: []resourcev1beta2.DeviceRequest{{
								Name: "gpu-request",
								Exactly: &resourcev1beta2.ExactDeviceRequest{
									DeviceClassName: "example.com",
									AllocationMode:  resourcev1beta2.DeviceAllocationModeExactCount,
									Count:           3,
								},
							}},
						},
					},
				},
			}
			gomega.Expect(k8sClient.Create(ctx, rct1)).To(gomega.Succeed())

			rct2 := &resourcev1beta2.ResourceClaimTemplate{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "gpu-template-2",
					Namespace: ns.Name,
				},
				Spec: resourcev1beta2.ResourceClaimTemplateSpec{
					Spec: resourcev1beta2.ResourceClaimSpec{
						Devices: resourcev1beta2.DeviceClaim{
							Requests: []resourcev1beta2.DeviceRequest{{
								Name: "gpu-request",
								Exactly: &resourcev1beta2.ExactDeviceRequest{
									DeviceClassName: "example.com",
									AllocationMode:  resourcev1beta2.DeviceAllocationModeExactCount,
									Count:           3,
								},
							}},
						},
					},
				},
			}
			gomega.Expect(k8sClient.Create(ctx, rct2)).To(gomega.Succeed())

			ginkgo.By("Creating first workload with ResourceClaimTemplate")
			wl1 := utiltesting.MakeWorkload("test-wl-template-1", ns.Name).
				Queue("test-lq").
				Obj()
			wl1.Spec.PodSets[0].Template.Spec.ResourceClaims = []corev1.PodResourceClaim{
				{
					Name:                      "gpu-template",
					ResourceClaimTemplateName: ptr.To("gpu-template-1"),
				},
			}
			wl1.Spec.PodSets[0].Template.Spec.Containers[0].Resources.Claims = []corev1.ResourceClaim{
				{Name: "gpu-template"},
			}
			gomega.Expect(k8sClient.Create(ctx, wl1)).To(gomega.Succeed())

			ginkgo.By("Creating second workload with ResourceClaimTemplate")
			wl2 := utiltesting.MakeWorkload("test-wl-template-2", ns.Name).
				Queue("test-lq").
				Obj()
			wl2.Spec.PodSets[0].Template.Spec.ResourceClaims = []corev1.PodResourceClaim{
				{
					Name:                      "gpu-template",
					ResourceClaimTemplateName: ptr.To("gpu-template-2"),
				},
			}
			wl2.Spec.PodSets[0].Template.Spec.Containers[0].Resources.Claims = []corev1.ResourceClaim{
				{Name: "gpu-template"},
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
			var updatedWl1, updatedWl2 kueue.Workload
			gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl1), &updatedWl1)).To(gomega.Succeed())
			gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl2), &updatedWl2)).To(gomega.Succeed())

			totalUsage := int64(0)
			if updatedWl1.Status.Admission != nil && len(updatedWl1.Status.Admission.PodSetAssignments) > 0 {
				if usage, ok := updatedWl1.Status.Admission.PodSetAssignments[0].ResourceUsage["example.com/gpu"]; ok {
					totalUsage += usage.Value()
				}
			}
			if updatedWl2.Status.Admission != nil && len(updatedWl2.Status.Admission.PodSetAssignments) > 0 {
				if usage, ok := updatedWl2.Status.Admission.PodSetAssignments[0].ResourceUsage["example.com/gpu"]; ok {
					totalUsage += usage.Value()
				}
			}
			gomega.Expect(totalUsage).To(gomega.Equal(int64(6)))
		})

		ginkgo.It("Should handle ResourceClaimTemplate with insufficient quota", func() {
			ginkgo.By("Creating a ResourceClaimTemplate that exceeds quota")
			rct := &resourcev1beta2.ResourceClaimTemplate{
				ObjectMeta: metav1.ObjectMeta{
					Name:      "gpu-template-large",
					Namespace: ns.Name,
				},
				Spec: resourcev1beta2.ResourceClaimTemplateSpec{
					Spec: resourcev1beta2.ResourceClaimSpec{
						Devices: resourcev1beta2.DeviceClaim{
							Requests: []resourcev1beta2.DeviceRequest{{
								Name: "gpu-request",
								Exactly: &resourcev1beta2.ExactDeviceRequest{
									DeviceClassName: "example.com",
									AllocationMode:  resourcev1beta2.DeviceAllocationModeExactCount,
									Count:           12,
								},
							}},
						},
					},
				},
			}
			gomega.Expect(k8sClient.Create(ctx, rct)).To(gomega.Succeed())

			ginkgo.By("Creating a workload that references the large ResourceClaimTemplate")
			wl := utiltesting.MakeWorkload("test-wl-template-large", ns.Name).
				Queue("test-lq").
				Obj()
			wl.Spec.PodSets[0].Template.Spec.ResourceClaims = []corev1.PodResourceClaim{
				{
					Name:                      "gpu-template",
					ResourceClaimTemplateName: ptr.To("gpu-template-large"),
				},
			}
			wl.Spec.PodSets[0].Template.Spec.Containers[0].Resources.Claims = []corev1.ResourceClaim{
				{Name: "gpu-template"},
			}
			gomega.Expect(k8sClient.Create(ctx, wl)).To(gomega.Succeed())

			ginkgo.By("Verifying workload with ResourceClaimTemplate remains pending due to quota")
			gomega.Consistently(func(g gomega.Gomega) {
				var updatedWl kueue.Workload
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &updatedWl)).To(gomega.Succeed())
				g.Expect(workload.HasQuotaReservation(&updatedWl)).To(gomega.BeFalse())
			}, util.ConsistentDuration, util.ShortInterval).Should(gomega.Succeed())
		})
	})
})
