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
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/test/integration/framework"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("DRA Consumable Capacity Integration", ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	ginkgo.BeforeAll(func() {
		fwk.StartManager(ctx, cfg, managerSetup(func(c *config.Configuration) {
			c.Resources.DeviceClassMappings = append(c.Resources.DeviceClassMappings,
				config.DeviceClassMapping{
					Name:             "gpu.memory",
					DeviceClassNames: []corev1.ResourceName{"vgpu.example.com"},
					Sources: []config.DeviceClassSourceConfig{
						{Capacity: &config.DeviceClassCapacitySource{
							Name:   "memory",
							Driver: "gpu.example.com",
							DeviceSelector: resourcev1.DeviceSelector{
								CEL: &resourcev1.CELDeviceSelector{
									Expression: "device.driver == 'gpu.example.com'",
								},
							},
						}},
					},
				},
			)
		}))
	})

	ginkgo.AfterAll(func() {
		fwk.StopManager(ctx)
	})

	ginkgo.When("Consumable capacity is configured", func() {
		var (
			ns             *corev1.Namespace
			resourceFlavor *kueue.ResourceFlavor
			clusterQueue   *kueue.ClusterQueue
			localQueue     *kueue.LocalQueue
			vgpuClass      *resourcev1.DeviceClass
		)

		ginkgo.BeforeEach(func() {
			ns = utiltesting.MakeNamespaceWithGenerateName("dra-cc-")
			gomega.Expect(k8sClient.Create(ctx, ns)).To(gomega.Succeed())

			vgpuClass = utiltesting.MakeDeviceClass("vgpu.example.com").
				CELSelector("device.driver == 'gpu.example.com'").
				Obj()
			gomega.Expect(k8sClient.Create(ctx, vgpuClass)).To(gomega.Succeed())

			resourceFlavor = utiltestingapi.MakeResourceFlavor("").GeneratedName("rf-cc-").Obj()
			gomega.Expect(k8sClient.Create(ctx, resourceFlavor)).To(gomega.Succeed())

			clusterQueue = utiltestingapi.MakeClusterQueue("").GeneratedName("cc-cq-").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas(resourceFlavor.Name).
						Resource("cpu", "8").
						Resource("memory", "16Gi").
						Resource("gpu.memory", "160Gi").
						Obj(),
				).Obj()
			gomega.Expect(k8sClient.Create(ctx, clusterQueue)).To(gomega.Succeed())
			util.ExpectClusterQueuesToBeActive(ctx, k8sClient, clusterQueue)

			localQueue = utiltestingapi.MakeLocalQueue("cc-lq", ns.Name).
				ClusterQueue(clusterQueue.Name).Obj()
			gomega.Expect(k8sClient.Create(ctx, localQueue)).To(gomega.Succeed())
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			util.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, resourceFlavor, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, vgpuClass, true)
		})

		ginkgo.It("Should charge explicit capacity request", func() {
			ginkgo.By("Creating ResourceSlice with capacity dimensions")
			slice := utiltesting.MakeResourceSlice("cc-explicit-slice", "gpu.example.com").
				Pool("node1-gpu0", 1, 1).
				Device("gpu-0").
				DeviceCapacity("memory", "80Gi", nil).
				AllowMultipleAllocations(true).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, slice)).To(gomega.Succeed())
			ginkgo.DeferCleanup(func() {
				gomega.Expect(k8sClient.Delete(ctx, slice)).To(gomega.Succeed())
			})

			ginkgo.By("Creating RCT with explicit capacity.requests")
			rct := utiltesting.MakeResourceClaimTemplate("cc-explicit", ns.Name).
				DeviceRequest("gpu", "vgpu.example.com", 1).
				WithCapacityRequests(map[string]string{"memory": "20Gi"}).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, rct)).To(gomega.Succeed())

			ginkgo.By("Creating workload")
			wl := utiltestingapi.MakeWorkload("cc-explicit-wl", ns.Name).
				Queue("cc-lq").
				Obj()
			wl.Spec.PodSets[0].Template.Spec.ResourceClaims = []corev1.PodResourceClaim{
				{Name: "gpu", ResourceClaimTemplateName: new("cc-explicit")},
			}
			wl.Spec.PodSets[0].Template.Spec.Containers[0].Resources.Claims = []corev1.ResourceClaim{
				{Name: "gpu"},
			}
			gomega.Expect(k8sClient.Create(ctx, wl)).To(gomega.Succeed())

			ginkgo.By("Verifying workload is admitted with 20Gi gpu.memory charge")
			gomega.Eventually(func(g gomega.Gomega) {
				var updatedWl kueue.Workload
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &updatedWl)).To(gomega.Succeed())
				g.Expect(workload.HasQuotaReservation(&updatedWl)).To(gomega.BeTrue())
				g.Expect(updatedWl.Status.Admission).NotTo(gomega.BeNil())
				g.Expect(updatedWl.Status.Admission.PodSetAssignments).To(gomega.HaveLen(1))

				assignment := updatedWl.Status.Admission.PodSetAssignments[0]
				g.Expect(assignment.ResourceUsage).To(gomega.HaveKey(corev1.ResourceName("gpu.memory")))
				memUsage := assignment.ResourceUsage["gpu.memory"]
				g.Expect(memUsage.Cmp(resource.MustParse("20Gi"))).To(gomega.Equal(0))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.It("Should default to max capacity value when no request specified", func() {
			ginkgo.By("Creating ResourceSlice with capacity dimensions")
			slice := utiltesting.MakeResourceSlice("cc-default-slice", "gpu.example.com").
				Pool("node1-gpu0", 1, 1).
				Device("gpu-0").
				DeviceCapacity("memory", "80Gi", nil).
				AllowMultipleAllocations(true).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, slice)).To(gomega.Succeed())
			ginkgo.DeferCleanup(func() {
				gomega.Expect(k8sClient.Delete(ctx, slice)).To(gomega.Succeed())
			})

			ginkgo.By("Creating RCT without capacity.requests (defaults to max capacity)")
			rct := utiltesting.MakeResourceClaimTemplate("cc-default", ns.Name).
				DeviceRequest("gpu", "vgpu.example.com", 1).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, rct)).To(gomega.Succeed())

			ginkgo.By("Creating workload")
			wl := utiltestingapi.MakeWorkload("cc-default-wl", ns.Name).
				Queue("cc-lq").
				Obj()
			wl.Spec.PodSets[0].Template.Spec.ResourceClaims = []corev1.PodResourceClaim{
				{Name: "gpu", ResourceClaimTemplateName: new("cc-default")},
			}
			wl.Spec.PodSets[0].Template.Spec.Containers[0].Resources.Claims = []corev1.ResourceClaim{
				{Name: "gpu"},
			}
			gomega.Expect(k8sClient.Create(ctx, wl)).To(gomega.Succeed())

			ginkgo.By("Verifying workload charges max capacity value (80Gi)")
			gomega.Eventually(func(g gomega.Gomega) {
				var updatedWl kueue.Workload
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &updatedWl)).To(gomega.Succeed())
				g.Expect(workload.HasQuotaReservation(&updatedWl)).To(gomega.BeTrue())

				assignment := updatedWl.Status.Admission.PodSetAssignments[0]
				memUsage := assignment.ResourceUsage["gpu.memory"]
				g.Expect(memUsage.Cmp(resource.MustParse("80Gi"))).To(gomega.Equal(0))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.It("Should default to RequestPolicy.Default when no request specified", func() {
			ginkgo.By("Creating ResourceSlice with RequestPolicy.Default")
			defaultQty := resource.MustParse("10Gi")
			slice := utiltesting.MakeResourceSlice("cc-policy-default-slice", "gpu.example.com").
				Pool("node1-gpu0", 1, 1).
				Device("gpu-0").
				DeviceCapacity("memory", "80Gi", &resourcev1.CapacityRequestPolicy{
					Default: &defaultQty,
				}).
				AllowMultipleAllocations(true).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, slice)).To(gomega.Succeed())
			ginkgo.DeferCleanup(func() {
				gomega.Expect(k8sClient.Delete(ctx, slice)).To(gomega.Succeed())
			})

			ginkgo.By("Creating RCT without capacity.requests")
			rct := utiltesting.MakeResourceClaimTemplate("cc-policy-default", ns.Name).
				DeviceRequest("gpu", "vgpu.example.com", 1).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, rct)).To(gomega.Succeed())

			ginkgo.By("Creating workload")
			wl := utiltestingapi.MakeWorkload("cc-policy-default-wl", ns.Name).
				Queue("cc-lq").
				Obj()
			wl.Spec.PodSets[0].Template.Spec.ResourceClaims = []corev1.PodResourceClaim{
				{Name: "gpu", ResourceClaimTemplateName: new("cc-policy-default")},
			}
			wl.Spec.PodSets[0].Template.Spec.Containers[0].Resources.Claims = []corev1.ResourceClaim{
				{Name: "gpu"},
			}
			gomega.Expect(k8sClient.Create(ctx, wl)).To(gomega.Succeed())

			ginkgo.By("Verifying workload charges RequestPolicy.Default (10Gi)")
			gomega.Eventually(func(g gomega.Gomega) {
				var updatedWl kueue.Workload
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &updatedWl)).To(gomega.Succeed())
				g.Expect(workload.HasQuotaReservation(&updatedWl)).To(gomega.BeTrue())

				assignment := updatedWl.Status.Admission.PodSetAssignments[0]
				memUsage := assignment.ResourceUsage["gpu.memory"]
				g.Expect(memUsage.Cmp(resource.MustParse("10Gi"))).To(gomega.Equal(0))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.It("Should round up capacity request to ValidValues", func() {
			ginkgo.By("Creating ResourceSlice with ValidValues policy")
			defaultQty := resource.MustParse("10Gi")
			slice := utiltesting.MakeResourceSlice("cc-validvalues-slice", "gpu.example.com").
				Pool("node1-gpu0", 1, 1).
				Device("gpu-0").
				DeviceCapacity("memory", "80Gi", &resourcev1.CapacityRequestPolicy{
					Default: &defaultQty,
					ValidValues: []resource.Quantity{
						resource.MustParse("10Gi"),
						resource.MustParse("20Gi"),
						resource.MustParse("40Gi"),
						resource.MustParse("80Gi"),
					},
				}).
				AllowMultipleAllocations(true).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, slice)).To(gomega.Succeed())
			ginkgo.DeferCleanup(func() {
				gomega.Expect(k8sClient.Delete(ctx, slice)).To(gomega.Succeed())
			})

			ginkgo.By("Creating RCT requesting 15Gi (rounds up to 20Gi)")
			rct := utiltesting.MakeResourceClaimTemplate("cc-validvalues", ns.Name).
				DeviceRequest("gpu", "vgpu.example.com", 1).
				WithCapacityRequests(map[string]string{"memory": "15Gi"}).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, rct)).To(gomega.Succeed())

			ginkgo.By("Creating workload")
			wl := utiltestingapi.MakeWorkload("cc-validvalues-wl", ns.Name).
				Queue("cc-lq").
				Obj()
			wl.Spec.PodSets[0].Template.Spec.ResourceClaims = []corev1.PodResourceClaim{
				{Name: "gpu", ResourceClaimTemplateName: new("cc-validvalues")},
			}
			wl.Spec.PodSets[0].Template.Spec.Containers[0].Resources.Claims = []corev1.ResourceClaim{
				{Name: "gpu"},
			}
			gomega.Expect(k8sClient.Create(ctx, wl)).To(gomega.Succeed())

			ginkgo.By("Verifying charge is rounded up to 20Gi")
			gomega.Eventually(func(g gomega.Gomega) {
				var updatedWl kueue.Workload
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &updatedWl)).To(gomega.Succeed())
				g.Expect(workload.HasQuotaReservation(&updatedWl)).To(gomega.BeTrue())

				assignment := updatedWl.Status.Admission.PodSetAssignments[0]
				memUsage := assignment.ResourceUsage["gpu.memory"]
				g.Expect(memUsage.Cmp(resource.MustParse("20Gi"))).To(gomega.Equal(0))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.It("Should round up capacity request to ValidRange with step", func() {
			ginkgo.By("Creating ResourceSlice with ValidRange policy")
			defaultQty := resource.MustParse("5Gi")
			minQty := resource.MustParse("5Gi")
			maxQty := resource.MustParse("80Gi")
			stepQty := resource.MustParse("5Gi")
			slice := utiltesting.MakeResourceSlice("cc-validrange-slice", "gpu.example.com").
				Pool("node1-gpu0", 1, 1).
				Device("gpu-0").
				DeviceCapacity("memory", "80Gi", &resourcev1.CapacityRequestPolicy{
					Default: &defaultQty,
					ValidRange: &resourcev1.CapacityRequestPolicyRange{
						Min:  &minQty,
						Max:  &maxQty,
						Step: &stepQty,
					},
				}).
				AllowMultipleAllocations(true).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, slice)).To(gomega.Succeed())
			ginkgo.DeferCleanup(func() {
				gomega.Expect(k8sClient.Delete(ctx, slice)).To(gomega.Succeed())
			})

			ginkgo.By("Creating RCT requesting 3Gi (rounds up to Min=5Gi)")
			rct := utiltesting.MakeResourceClaimTemplate("cc-validrange", ns.Name).
				DeviceRequest("gpu", "vgpu.example.com", 1).
				WithCapacityRequests(map[string]string{"memory": "3Gi"}).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, rct)).To(gomega.Succeed())

			ginkgo.By("Creating workload")
			wl := utiltestingapi.MakeWorkload("cc-validrange-wl", ns.Name).
				Queue("cc-lq").
				Obj()
			wl.Spec.PodSets[0].Template.Spec.ResourceClaims = []corev1.PodResourceClaim{
				{Name: "gpu", ResourceClaimTemplateName: new("cc-validrange")},
			}
			wl.Spec.PodSets[0].Template.Spec.Containers[0].Resources.Claims = []corev1.ResourceClaim{
				{Name: "gpu"},
			}
			gomega.Expect(k8sClient.Create(ctx, wl)).To(gomega.Succeed())

			ginkgo.By("Verifying charge is rounded up to min (5Gi)")
			gomega.Eventually(func(g gomega.Gomega) {
				var updatedWl kueue.Workload
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &updatedWl)).To(gomega.Succeed())
				g.Expect(workload.HasQuotaReservation(&updatedWl)).To(gomega.BeTrue())

				assignment := updatedWl.Status.Admission.PodSetAssignments[0]
				memUsage := assignment.ResourceUsage["gpu.memory"]
				g.Expect(memUsage.Cmp(resource.MustParse("5Gi"))).To(gomega.Equal(0))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.It("Should multiply capacity charge by request count", func() {
			ginkgo.By("Creating ResourceSlice with multiple allocatable devices")
			slice := utiltesting.MakeResourceSlice("cc-count-slice", "gpu.example.com").
				Pool("node1-gpu0", 1, 1).
				Device("gpu-0").
				DeviceCapacity("memory", "80Gi", nil).
				AllowMultipleAllocations(true).
				Device("gpu-1").
				DeviceCapacity("memory", "80Gi", nil).
				AllowMultipleAllocations(true).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, slice)).To(gomega.Succeed())
			ginkgo.DeferCleanup(func() {
				gomega.Expect(k8sClient.Delete(ctx, slice)).To(gomega.Succeed())
			})

			ginkgo.By("Creating RCT with count=2 and 20Gi capacity request")
			rct := utiltesting.MakeResourceClaimTemplate("cc-count2", ns.Name).
				DeviceRequest("gpu", "vgpu.example.com", 2).
				WithCapacityRequests(map[string]string{"memory": "20Gi"}).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, rct)).To(gomega.Succeed())

			ginkgo.By("Creating workload")
			wl := utiltestingapi.MakeWorkload("cc-count2-wl", ns.Name).
				Queue("cc-lq").
				Obj()
			wl.Spec.PodSets[0].Template.Spec.ResourceClaims = []corev1.PodResourceClaim{
				{Name: "gpu", ResourceClaimTemplateName: new("cc-count2")},
			}
			wl.Spec.PodSets[0].Template.Spec.Containers[0].Resources.Claims = []corev1.ResourceClaim{
				{Name: "gpu"},
			}
			gomega.Expect(k8sClient.Create(ctx, wl)).To(gomega.Succeed())

			ginkgo.By("Verifying charge is 20Gi * 2 = 40Gi")
			gomega.Eventually(func(g gomega.Gomega) {
				var updatedWl kueue.Workload
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &updatedWl)).To(gomega.Succeed())
				g.Expect(workload.HasQuotaReservation(&updatedWl)).To(gomega.BeTrue())

				assignment := updatedWl.Status.Admission.PodSetAssignments[0]
				memUsage := assignment.ResourceUsage["gpu.memory"]
				g.Expect(memUsage.Cmp(resource.MustParse("40Gi"))).To(gomega.Equal(0))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.It("Should mark workload inadmissible when request exceeds ValidValues", framework.SlowSpec, func() {
			ginkgo.By("Creating ResourceSlice with ValidValues policy")
			defaultQty := resource.MustParse("10Gi")
			slice := utiltesting.MakeResourceSlice("cc-exceed-slice", "gpu.example.com").
				Pool("node1-gpu0", 1, 1).
				Device("gpu-0").
				DeviceCapacity("memory", "80Gi", &resourcev1.CapacityRequestPolicy{
					Default: &defaultQty,
					ValidValues: []resource.Quantity{
						resource.MustParse("10Gi"),
						resource.MustParse("20Gi"),
						resource.MustParse("40Gi"),
					},
				}).
				AllowMultipleAllocations(true).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, slice)).To(gomega.Succeed())
			ginkgo.DeferCleanup(func() {
				gomega.Expect(k8sClient.Delete(ctx, slice)).To(gomega.Succeed())
			})

			ginkgo.By("Creating RCT requesting 50Gi (exceeds max valid value 40Gi)")
			rct := utiltesting.MakeResourceClaimTemplate("cc-exceed", ns.Name).
				DeviceRequest("gpu", "vgpu.example.com", 1).
				WithCapacityRequests(map[string]string{"memory": "50Gi"}).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, rct)).To(gomega.Succeed())

			ginkgo.By("Creating workload")
			wl := utiltestingapi.MakeWorkload("cc-exceed-wl", ns.Name).
				Queue("cc-lq").
				Obj()
			wl.Spec.PodSets[0].Template.Spec.ResourceClaims = []corev1.PodResourceClaim{
				{Name: "gpu", ResourceClaimTemplateName: new("cc-exceed")},
			}
			wl.Spec.PodSets[0].Template.Spec.Containers[0].Resources.Claims = []corev1.ResourceClaim{
				{Name: "gpu"},
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
					gomega.HaveField("Reason", kueue.WorkloadQuotaReservedReasonMisconfigured),
				)))
			}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.It("Should mark workload inadmissible when no devices have capacity dimension", framework.SlowSpec, func() {
			ginkgo.By("Creating ResourceSlice without capacity dimensions")
			slice := utiltesting.MakeResourceSlice("cc-nodim-slice", "gpu.example.com").
				Pool("node1-gpu0", 1, 1).
				Device("gpu-0").
				AllowMultipleAllocations(true).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, slice)).To(gomega.Succeed())
			ginkgo.DeferCleanup(func() {
				gomega.Expect(k8sClient.Delete(ctx, slice)).To(gomega.Succeed())
			})

			ginkgo.By("Creating RCT requesting capacity")
			rct := utiltesting.MakeResourceClaimTemplate("cc-nodim", ns.Name).
				DeviceRequest("gpu", "vgpu.example.com", 1).
				WithCapacityRequests(map[string]string{"memory": "10Gi"}).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, rct)).To(gomega.Succeed())

			ginkgo.By("Creating workload")
			wl := utiltestingapi.MakeWorkload("cc-nodim-wl", ns.Name).
				Queue("cc-lq").
				Obj()
			wl.Spec.PodSets[0].Template.Spec.ResourceClaims = []corev1.PodResourceClaim{
				{Name: "gpu", ResourceClaimTemplateName: new("cc-nodim")},
			}
			wl.Spec.PodSets[0].Template.Spec.Containers[0].Resources.Claims = []corev1.ResourceClaim{
				{Name: "gpu"},
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
					gomega.HaveField("Reason", kueue.WorkloadQuotaReservedReasonMisconfigured),
				)))
			}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.It("Should skip device-count charge when capacity sources configured", func() {
			ginkgo.By("Creating ResourceSlice with capacity")
			slice := utiltesting.MakeResourceSlice("cc-skipcount-slice", "gpu.example.com").
				Pool("node1-gpu0", 1, 1).
				Device("gpu-0").
				DeviceCapacity("memory", "80Gi", nil).
				AllowMultipleAllocations(true).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, slice)).To(gomega.Succeed())
			ginkgo.DeferCleanup(func() {
				gomega.Expect(k8sClient.Delete(ctx, slice)).To(gomega.Succeed())
			})

			ginkgo.By("Creating RCT with explicit capacity request")
			rct := utiltesting.MakeResourceClaimTemplate("cc-skipcount", ns.Name).
				DeviceRequest("gpu", "vgpu.example.com", 1).
				WithCapacityRequests(map[string]string{"memory": "20Gi"}).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, rct)).To(gomega.Succeed())

			ginkgo.By("Creating workload")
			wl := utiltestingapi.MakeWorkload("cc-skipcount-wl", ns.Name).
				Queue("cc-lq").
				Obj()
			wl.Spec.PodSets[0].Template.Spec.ResourceClaims = []corev1.PodResourceClaim{
				{Name: "gpu", ResourceClaimTemplateName: new("cc-skipcount")},
			}
			wl.Spec.PodSets[0].Template.Spec.Containers[0].Resources.Claims = []corev1.ResourceClaim{
				{Name: "gpu"},
			}
			gomega.Expect(k8sClient.Create(ctx, wl)).To(gomega.Succeed())

			ginkgo.By("Verifying ONLY capacity-based charge (20Gi), not device count (1)")
			gomega.Eventually(func(g gomega.Gomega) {
				var updatedWl kueue.Workload
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &updatedWl)).To(gomega.Succeed())
				g.Expect(workload.HasQuotaReservation(&updatedWl)).To(gomega.BeTrue())

				assignment := updatedWl.Status.Admission.PodSetAssignments[0]
				memUsage := assignment.ResourceUsage["gpu.memory"]
				g.Expect(memUsage.Cmp(resource.MustParse("20Gi"))).To(gomega.Equal(0),
					"should be 20Gi (capacity), not 1 (device count)")
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.It("Should requeue inadmissible workload when ResourceSlice appears", framework.SlowSpec, func() {
			ginkgo.By("Creating RCT with capacity request")
			rct := utiltesting.MakeResourceClaimTemplate("cc-requeue", ns.Name).
				DeviceRequest("gpu", "vgpu.example.com", 1).
				WithCapacityRequests(map[string]string{"memory": "20Gi"}).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, rct)).To(gomega.Succeed())

			ginkgo.By("Creating workload without any ResourceSlices")
			wl := utiltestingapi.MakeWorkload("cc-requeue-wl", ns.Name).
				Queue("cc-lq").
				Obj()
			wl.Spec.PodSets[0].Template.Spec.ResourceClaims = []corev1.PodResourceClaim{
				{Name: "gpu", ResourceClaimTemplateName: new("cc-requeue")},
			}
			wl.Spec.PodSets[0].Template.Spec.Containers[0].Resources.Claims = []corev1.ResourceClaim{
				{Name: "gpu"},
			}
			gomega.Expect(k8sClient.Create(ctx, wl)).To(gomega.Succeed())

			ginkgo.By("Verifying workload is initially inadmissible")
			gomega.Eventually(func(g gomega.Gomega) {
				var updatedWl kueue.Workload
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &updatedWl)).To(gomega.Succeed())
				g.Expect(updatedWl.Status.Conditions).To(gomega.ContainElement(gomega.And(
					gomega.HaveField("Type", kueue.WorkloadQuotaReserved),
					gomega.HaveField("Status", metav1.ConditionFalse),
					gomega.HaveField("Reason", kueue.WorkloadQuotaReservedReasonMisconfigured),
				)))
			}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())

			ginkgo.By("Creating ResourceSlice — should trigger requeue")
			slice := utiltesting.MakeResourceSlice("cc-requeue-slice", "gpu.example.com").
				Pool("node1-gpu0", 1, 1).
				Device("gpu-0").
				DeviceCapacity("memory", "80Gi", nil).
				AllowMultipleAllocations(true).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, slice)).To(gomega.Succeed())
			ginkgo.DeferCleanup(func() {
				gomega.Expect(k8sClient.Delete(ctx, slice)).To(gomega.Succeed())
			})

			ginkgo.By("Verifying workload is now admitted after requeue")
			gomega.Eventually(func(g gomega.Gomega) {
				var updatedWl kueue.Workload
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &updatedWl)).To(gomega.Succeed())
				g.Expect(workload.HasQuotaReservation(&updatedWl)).To(gomega.BeTrue())

				assignment := updatedWl.Status.Admission.PodSetAssignments[0]
				g.Expect(assignment.ResourceUsage).To(gomega.HaveKey(corev1.ResourceName("gpu.memory")))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.It("Should use max Default across devices with heterogeneous Defaults", func() {
			ginkgo.By("Creating ResourceSlice with two devices having different Defaults")
			default40 := resource.MustParse("40Gi")
			default10 := resource.MustParse("10Gi")
			slice := utiltesting.MakeResourceSlice("cc-hetdefault-slice", "gpu.example.com").
				Pool("node1-gpu0", 1, 1).
				Device("gpu-0").
				DeviceCapacity("memory", "80Gi", &resourcev1.CapacityRequestPolicy{
					Default: &default40,
				}).
				AllowMultipleAllocations(true).
				Device("gpu-1").
				DeviceCapacity("memory", "100Gi", &resourcev1.CapacityRequestPolicy{
					Default: &default10,
				}).
				AllowMultipleAllocations(true).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, slice)).To(gomega.Succeed())
			ginkgo.DeferCleanup(func() {
				gomega.Expect(k8sClient.Delete(ctx, slice)).To(gomega.Succeed())
			})

			ginkgo.By("Creating RCT without explicit request (uses per-device Default)")
			rct := utiltesting.MakeResourceClaimTemplate("cc-hetdefault", ns.Name).
				DeviceRequest("gpu", "vgpu.example.com", 1).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, rct)).To(gomega.Succeed())

			ginkgo.By("Creating workload")
			wl := utiltestingapi.MakeWorkload("cc-hetdefault-wl", ns.Name).
				Queue("cc-lq").
				Obj()
			wl.Spec.PodSets[0].Template.Spec.ResourceClaims = []corev1.PodResourceClaim{
				{Name: "gpu", ResourceClaimTemplateName: new("cc-hetdefault")},
			}
			wl.Spec.PodSets[0].Template.Spec.Containers[0].Resources.Claims = []corev1.ResourceClaim{
				{Name: "gpu"},
			}
			gomega.Expect(k8sClient.Create(ctx, wl)).To(gomega.Succeed())

			ginkgo.By("Verifying charge uses max(40Gi, 10Gi) = 40Gi (per-device Default)")
			gomega.Eventually(func(g gomega.Gomega) {
				var updatedWl kueue.Workload
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &updatedWl)).To(gomega.Succeed())
				g.Expect(workload.HasQuotaReservation(&updatedWl)).To(gomega.BeTrue())

				assignment := updatedWl.Status.Admission.PodSetAssignments[0]
				memUsage := assignment.ResourceUsage["gpu.memory"]
				g.Expect(memUsage.Cmp(resource.MustParse("40Gi"))).To(gomega.Equal(0),
					"should be 40Gi (max Default across devices), not 10Gi (max-capacity device's Default)")
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.It("Should mark inadmissible without retry when all policies reject request", framework.SlowSpec, func() {
			ginkgo.By("Creating ResourceSlice where all devices have ValidValues that reject 50Gi")
			defaultQty := resource.MustParse("10Gi")
			slice := utiltesting.MakeResourceSlice("cc-allreject-slice", "gpu.example.com").
				Pool("node1-gpu0", 1, 1).
				Device("gpu-0").
				DeviceCapacity("memory", "80Gi", &resourcev1.CapacityRequestPolicy{
					Default:     &defaultQty,
					ValidValues: []resource.Quantity{resource.MustParse("10Gi"), resource.MustParse("20Gi")},
				}).
				AllowMultipleAllocations(true).
				Device("gpu-1").
				DeviceCapacity("memory", "80Gi", &resourcev1.CapacityRequestPolicy{
					Default:     &defaultQty,
					ValidValues: []resource.Quantity{resource.MustParse("10Gi"), resource.MustParse("30Gi")},
				}).
				AllowMultipleAllocations(true).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, slice)).To(gomega.Succeed())
			ginkgo.DeferCleanup(func() {
				gomega.Expect(k8sClient.Delete(ctx, slice)).To(gomega.Succeed())
			})

			ginkgo.By("Creating RCT requesting 50Gi (exceeds all devices' ValidValues)")
			rct := utiltesting.MakeResourceClaimTemplate("cc-allreject", ns.Name).
				DeviceRequest("gpu", "vgpu.example.com", 1).
				WithCapacityRequests(map[string]string{"memory": "50Gi"}).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, rct)).To(gomega.Succeed())

			ginkgo.By("Creating workload")
			wl := utiltestingapi.MakeWorkload("cc-allreject-wl", ns.Name).
				Queue("cc-lq").
				Obj()
			wl.Spec.PodSets[0].Template.Spec.ResourceClaims = []corev1.PodResourceClaim{
				{Name: "gpu", ResourceClaimTemplateName: new("cc-allreject")},
			}
			wl.Spec.PodSets[0].Template.Spec.Containers[0].Resources.Claims = []corev1.ResourceClaim{
				{Name: "gpu"},
			}
			gomega.Expect(k8sClient.Create(ctx, wl)).To(gomega.Succeed())

			ginkgo.By("Verifying workload is marked inadmissible with deterministic error")
			gomega.Eventually(func(g gomega.Gomega) {
				var updatedWl kueue.Workload
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &updatedWl)).To(gomega.Succeed())
				g.Expect(workload.HasQuotaReservation(&updatedWl)).To(gomega.BeFalse())
				g.Expect(updatedWl.Status.Conditions).To(gomega.ContainElement(gomega.And(
					gomega.HaveField("Type", kueue.WorkloadQuotaReserved),
					gomega.HaveField("Status", metav1.ConditionFalse),
					gomega.HaveField("Reason", kueue.WorkloadQuotaReservedReasonMisconfigured),
				)))
			}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())

			ginkgo.By("Verifying workload stays inadmissible (deterministic, no requeue)")
			gomega.Consistently(func(g gomega.Gomega) {
				var updatedWl kueue.Workload
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &updatedWl)).To(gomega.Succeed())
				g.Expect(workload.HasQuotaReservation(&updatedWl)).To(gomega.BeFalse())
			}, util.ConsistentDuration, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.It("Should use max capacity across multiple devices", func() {
			ginkgo.By("Creating ResourceSlice with two devices having different capacities")
			slice := utiltesting.MakeResourceSlice("cc-maxcap-slice", "gpu.example.com").
				Pool("node1-gpu0", 1, 1).
				Device("gpu-0").
				DeviceCapacity("memory", "40Gi", nil).
				AllowMultipleAllocations(true).
				Device("gpu-1").
				DeviceCapacity("memory", "80Gi", nil).
				AllowMultipleAllocations(true).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, slice)).To(gomega.Succeed())
			ginkgo.DeferCleanup(func() {
				gomega.Expect(k8sClient.Delete(ctx, slice)).To(gomega.Succeed())
			})

			ginkgo.By("Creating RCT without explicit request")
			rct := utiltesting.MakeResourceClaimTemplate("cc-maxcap", ns.Name).
				DeviceRequest("gpu", "vgpu.example.com", 1).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, rct)).To(gomega.Succeed())

			ginkgo.By("Creating workload")
			wl := utiltestingapi.MakeWorkload("cc-maxcap-wl", ns.Name).
				Queue("cc-lq").
				Obj()
			wl.Spec.PodSets[0].Template.Spec.ResourceClaims = []corev1.PodResourceClaim{
				{Name: "gpu", ResourceClaimTemplateName: new("cc-maxcap")},
			}
			wl.Spec.PodSets[0].Template.Spec.Containers[0].Resources.Claims = []corev1.ResourceClaim{
				{Name: "gpu"},
			}
			gomega.Expect(k8sClient.Create(ctx, wl)).To(gomega.Succeed())

			ginkgo.By("Verifying charge uses max(40Gi, 80Gi) = 80Gi")
			gomega.Eventually(func(g gomega.Gomega) {
				var updatedWl kueue.Workload
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &updatedWl)).To(gomega.Succeed())
				g.Expect(workload.HasQuotaReservation(&updatedWl)).To(gomega.BeTrue())

				assignment := updatedWl.Status.Admission.PodSetAssignments[0]
				memUsage := assignment.ResourceUsage["gpu.memory"]
				g.Expect(memUsage.Cmp(resource.MustParse("80Gi"))).To(gomega.Equal(0))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})
	})
})
