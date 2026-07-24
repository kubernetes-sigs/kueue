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
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/test/integration/framework"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("DRA Partitionable Devices Integration", ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	ginkgo.BeforeAll(func() {
		fwk.StartManager(ctx, cfg, managerSetup(func(c *config.Configuration) {
			c.Resources.DeviceClassMappings = append(c.Resources.DeviceClassMappings,
				config.DeviceClassMapping{
					Name:             "gpu.memory",
					DeviceClassNames: []corev1.ResourceName{"gpu.example.com", "mig.example.com", "gpu-er-counter.example.com"},
					Sources: []config.DeviceClassSourceConfig{
						{Counter: &config.DeviceClassCounterSource{
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

	ginkgo.When("Partitionable devices are configured", func() {
		var (
			ns             *corev1.Namespace
			resourceFlavor *kueue.ResourceFlavor
			clusterQueue   *kueue.ClusterQueue
			localQueue     *kueue.LocalQueue
			migDeviceClass *resourcev1.DeviceClass
		)

		ginkgo.BeforeEach(func() {
			ns = utiltesting.MakeNamespaceWithGenerateName("dra-pd-")
			gomega.Expect(k8sClient.Create(ctx, ns)).To(gomega.Succeed())

			migDeviceClass = utiltesting.MakeDeviceClass("mig.example.com").
				CELSelector("device.attributes['gpu.example.com'].type == 'mig'").
				Obj()
			gomega.Expect(k8sClient.Create(ctx, migDeviceClass)).To(gomega.Succeed())

			resourceFlavor = utiltestingapi.MakeResourceFlavor("").GeneratedName("rf-pd-").Obj()
			gomega.Expect(k8sClient.Create(ctx, resourceFlavor)).To(gomega.Succeed())

			clusterQueue = utiltestingapi.MakeClusterQueue("").GeneratedName("pd-cq-").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas(resourceFlavor.Name).
						Resource("cpu", "8").
						Resource("memory", "16Gi").
						Resource("gpu.memory", "160Gi").
						Obj(),
				).Obj()
			gomega.Expect(k8sClient.Create(ctx, clusterQueue)).To(gomega.Succeed())
			util.ExpectClusterQueuesToBeActive(ctx, k8sClient, clusterQueue)

			localQueue = utiltestingapi.MakeLocalQueue("pd-lq", ns.Name).
				ClusterQueue(clusterQueue.Name).Obj()
			gomega.Expect(k8sClient.Create(ctx, localQueue)).To(gomega.Succeed())
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			util.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, resourceFlavor, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, migDeviceClass, true)
		})

		ginkgo.It("Should admit MIG workload with correct counter-based gpu.memory charge", func() {
			ginkgo.By("Creating a ResourceSlice with MIG devices")
			slice := utiltesting.MakeResourceSlice("pd-test-slice", "gpu.example.com").
				Pool("node1-gpu0", 1, 1).
				Device("gpu-0").Attribute("type", "gpu").
				CounterConsumption("gpu-0-counter-set", "memory", "40320Mi").
				Device("gpu-0-mig-1g5gb-0").Attribute("type", "mig").Attribute("profile", "1g.5gb").
				CounterConsumption("gpu-0-counter-set", "memory", "4864Mi").
				Device("gpu-0-mig-1g5gb-1").Attribute("type", "mig").Attribute("profile", "1g.5gb").
				CounterConsumption("gpu-0-counter-set", "memory", "4864Mi").
				Device("gpu-0-mig-7g40gb-0").Attribute("type", "mig").Attribute("profile", "7g.40gb").
				CounterConsumption("gpu-0-counter-set", "memory", "40320Mi").
				Obj()
			gomega.Expect(k8sClient.Create(ctx, slice)).To(gomega.Succeed())
			ginkgo.DeferCleanup(func() {
				gomega.Expect(k8sClient.Delete(ctx, slice)).To(gomega.Succeed())
			})

			ginkgo.By("Creating a ResourceClaimTemplate with CEL selector for 1g.5gb")
			rct := utiltesting.MakeResourceClaimTemplate("mig-1g5gb", ns.Name).
				DeviceRequest("gpu", "mig.example.com", 1).
				WithCELSelectors("device.attributes['gpu.example.com'].profile == '1g.5gb'").
				Obj()
			gomega.Expect(k8sClient.Create(ctx, rct)).To(gomega.Succeed())

			ginkgo.By("Creating a workload requesting MIG devices")
			wl := utiltestingapi.MakeWorkload("pd-test-wl", ns.Name).
				Queue("pd-lq").
				Obj()
			wl.Spec.PodSets[0].Template.Spec.ResourceClaims = []corev1.PodResourceClaim{
				{Name: "mig", ResourceClaimTemplateName: new("mig-1g5gb")},
			}
			wl.Spec.PodSets[0].Template.Spec.Containers[0].Resources.Claims = []corev1.ResourceClaim{
				{Name: "mig"},
			}
			gomega.Expect(k8sClient.Create(ctx, wl)).To(gomega.Succeed())

			ginkgo.By("Verifying workload is admitted with counter-based gpu.memory charge")
			gomega.Eventually(func(g gomega.Gomega) {
				var updatedWl kueue.Workload
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &updatedWl)).To(gomega.Succeed())
				g.Expect(workload.HasQuotaReservation(&updatedWl)).To(gomega.BeTrue())
				g.Expect(updatedWl.Status.Admission).NotTo(gomega.BeNil())
				g.Expect(updatedWl.Status.Admission.PodSetAssignments).To(gomega.HaveLen(1))

				assignment := updatedWl.Status.Admission.PodSetAssignments[0]
				g.Expect(assignment.ResourceUsage).To(gomega.HaveKey(corev1.ResourceName("gpu.memory")))
				memUsage := assignment.ResourceUsage["gpu.memory"]
				g.Expect(memUsage.Cmp(resource.MustParse("4864Mi"))).To(gomega.Equal(0))
				g.Expect(memUsage.String()).To(gomega.Equal("4864Mi"))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.It("Should multiply counter charge by request count", func() {
			ginkgo.By("Creating a ResourceSlice with MIG devices")
			slice := utiltesting.MakeResourceSlice("pd-count-slice", "gpu.example.com").
				Pool("node1-gpu0", 1, 1).
				Device("gpu-0").Attribute("type", "gpu").
				CounterConsumption("gpu-0-counter-set", "memory", "40320Mi").
				Device("gpu-0-mig-1g5gb-0").Attribute("type", "mig").Attribute("profile", "1g.5gb").
				CounterConsumption("gpu-0-counter-set", "memory", "4864Mi").
				Device("gpu-0-mig-1g5gb-1").Attribute("type", "mig").Attribute("profile", "1g.5gb").
				CounterConsumption("gpu-0-counter-set", "memory", "4864Mi").
				Obj()
			gomega.Expect(k8sClient.Create(ctx, slice)).To(gomega.Succeed())
			ginkgo.DeferCleanup(func() {
				gomega.Expect(k8sClient.Delete(ctx, slice)).To(gomega.Succeed())
			})

			ginkgo.By("Creating RCT with count=2")
			rct := utiltesting.MakeResourceClaimTemplate("mig-count2", ns.Name).
				DeviceRequest("gpu", "mig.example.com", 2).
				WithCELSelectors("device.attributes['gpu.example.com'].profile == '1g.5gb'").
				Obj()
			gomega.Expect(k8sClient.Create(ctx, rct)).To(gomega.Succeed())

			ginkgo.By("Creating workload")
			wl := utiltestingapi.MakeWorkload("pd-count2-wl", ns.Name).
				Queue("pd-lq").
				Obj()
			wl.Spec.PodSets[0].Template.Spec.ResourceClaims = []corev1.PodResourceClaim{
				{Name: "mig", ResourceClaimTemplateName: new("mig-count2")},
			}
			wl.Spec.PodSets[0].Template.Spec.Containers[0].Resources.Claims = []corev1.ResourceClaim{
				{Name: "mig"},
			}
			gomega.Expect(k8sClient.Create(ctx, wl)).To(gomega.Succeed())

			ginkgo.By("Verifying charge is 4864Mi * 2")
			gomega.Eventually(func(g gomega.Gomega) {
				var updatedWl kueue.Workload
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &updatedWl)).To(gomega.Succeed())
				g.Expect(workload.HasQuotaReservation(&updatedWl)).To(gomega.BeTrue())

				assignment := updatedWl.Status.Admission.PodSetAssignments[0]
				memUsage := assignment.ResourceUsage["gpu.memory"]
				expectedBytes := int64(4864 * 1024 * 1024 * 2)
				g.Expect(memUsage.Value()).To(gomega.Equal(expectedBytes))
				g.Expect(memUsage.String()).To(gomega.Equal("9728Mi"))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.It("Should charge MAX when CEL matches multiple profiles", func() {
			ginkgo.By("Creating a ResourceSlice with different MIG profiles")
			slice := utiltesting.MakeResourceSlice("pd-broad-slice", "gpu.example.com").
				Pool("node1-gpu0", 1, 1).
				Device("gpu-0").Attribute("type", "gpu").
				CounterConsumption("gpu-0-counter-set", "memory", "40320Mi").
				Device("gpu-0-mig-1g5gb-0").Attribute("type", "mig").Attribute("profile", "1g.5gb").
				CounterConsumption("gpu-0-counter-set", "memory", "4864Mi").
				Device("gpu-0-mig-3g20gb-0").Attribute("type", "mig").Attribute("profile", "3g.20gb").
				CounterConsumption("gpu-0-counter-set", "memory", "20Gi").
				Obj()
			gomega.Expect(k8sClient.Create(ctx, slice)).To(gomega.Succeed())
			ginkgo.DeferCleanup(func() {
				gomega.Expect(k8sClient.Delete(ctx, slice)).To(gomega.Succeed())
			})

			ginkgo.By("Creating RCT with broad CEL matching both profiles")
			rct := utiltesting.MakeResourceClaimTemplate("mig-broad", ns.Name).
				DeviceRequest("gpu", "mig.example.com", 1).
				WithCELSelectors("device.attributes['gpu.example.com'].profile == '1g.5gb' || device.attributes['gpu.example.com'].profile == '3g.20gb'").
				Obj()
			gomega.Expect(k8sClient.Create(ctx, rct)).To(gomega.Succeed())

			ginkgo.By("Creating workload")
			wl := utiltestingapi.MakeWorkload("pd-broad-wl", ns.Name).
				Queue("pd-lq").
				Obj()
			wl.Spec.PodSets[0].Template.Spec.ResourceClaims = []corev1.PodResourceClaim{
				{Name: "mig", ResourceClaimTemplateName: new("mig-broad")},
			}
			wl.Spec.PodSets[0].Template.Spec.Containers[0].Resources.Claims = []corev1.ResourceClaim{
				{Name: "mig"},
			}
			gomega.Expect(k8sClient.Create(ctx, wl)).To(gomega.Succeed())

			ginkgo.By("Verifying charge is MAX(4864Mi, 20Gi) = 20Gi")
			gomega.Eventually(func(g gomega.Gomega) {
				var updatedWl kueue.Workload
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &updatedWl)).To(gomega.Succeed())
				g.Expect(workload.HasQuotaReservation(&updatedWl)).To(gomega.BeTrue())

				assignment := updatedWl.Status.Admission.PodSetAssignments[0]
				memUsage := assignment.ResourceUsage["gpu.memory"]
				g.Expect(memUsage.Cmp(resource.MustParse("20Gi"))).To(gomega.Equal(0))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.It("Should mark workload inadmissible when CEL matches no devices", framework.SlowSpec, func() {
			ginkgo.By("Creating a ResourceSlice with devices")
			slice := utiltesting.MakeResourceSlice("pd-nomatch-slice", "gpu.example.com").
				Pool("node1-gpu0", 1, 1).
				Device("gpu-0").Attribute("type", "gpu").
				CounterConsumption("gpu-0-counter-set", "memory", "40320Mi").
				Device("gpu-0-mig-1g5gb-0").Attribute("type", "mig").Attribute("profile", "1g.5gb").
				CounterConsumption("gpu-0-counter-set", "memory", "4864Mi").
				Obj()
			gomega.Expect(k8sClient.Create(ctx, slice)).To(gomega.Succeed())
			ginkgo.DeferCleanup(func() {
				gomega.Expect(k8sClient.Delete(ctx, slice)).To(gomega.Succeed())
			})

			ginkgo.By("Creating RCT with CEL that matches no device")
			rct := utiltesting.MakeResourceClaimTemplate("mig-nonexistent", ns.Name).
				DeviceRequest("gpu", "mig.example.com", 1).
				WithCELSelectors("device.attributes['gpu.example.com'].profile == 'nonexistent'").
				Obj()
			gomega.Expect(k8sClient.Create(ctx, rct)).To(gomega.Succeed())

			ginkgo.By("Creating workload")
			wl := utiltestingapi.MakeWorkload("pd-nomatch-wl", ns.Name).
				Queue("pd-lq").
				Obj()
			wl.Spec.PodSets[0].Template.Spec.ResourceClaims = []corev1.PodResourceClaim{
				{Name: "mig", ResourceClaimTemplateName: new("mig-nonexistent")},
			}
			wl.Spec.PodSets[0].Template.Spec.Containers[0].Resources.Claims = []corev1.ResourceClaim{
				{Name: "mig"},
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

		ginkgo.It("Should admit workload with unified whole GPU and MIG charges", func() {
			ginkgo.By("Creating a ResourceSlice with whole GPU and MIG devices")
			slice := utiltesting.MakeResourceSlice("pd-unified-slice", "gpu.example.com").
				Pool("node1-gpu0", 1, 1).
				Device("gpu-0").Attribute("type", "gpu").
				CounterConsumption("gpu-0-counter-set", "memory", "40320Mi").
				Device("gpu-0-mig-1g5gb-0").Attribute("type", "mig").Attribute("profile", "1g.5gb").
				CounterConsumption("gpu-0-counter-set", "memory", "4864Mi").
				Obj()
			gomega.Expect(k8sClient.Create(ctx, slice)).To(gomega.Succeed())
			ginkgo.DeferCleanup(func() {
				gomega.Expect(k8sClient.Delete(ctx, slice)).To(gomega.Succeed())
			})

			ginkgo.By("Creating a DeviceClass for whole GPUs")
			dc := utiltesting.MakeDeviceClass("gpu.example.com").Obj()
			gomega.Expect(k8sClient.Create(ctx, dc)).To(gomega.Succeed())
			ginkgo.DeferCleanup(func() {
				gomega.Expect(k8sClient.Delete(ctx, dc)).To(gomega.Succeed())
			})

			ginkgo.By("Creating RCTs for whole GPU and MIG")
			rctGPU := utiltesting.MakeResourceClaimTemplate("whole-gpu", ns.Name).
				DeviceRequest("gpu", "gpu.example.com", 1).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, rctGPU)).To(gomega.Succeed())

			rctMIG := utiltesting.MakeResourceClaimTemplate("mig-1g5gb", ns.Name).
				DeviceRequest("gpu", "mig.example.com", 1).
				WithCELSelectors("device.attributes['gpu.example.com'].profile == '1g.5gb'").
				Obj()
			gomega.Expect(k8sClient.Create(ctx, rctMIG)).To(gomega.Succeed())

			ginkgo.By("Creating workload with both RCTs")
			wl := utiltestingapi.MakeWorkload("pd-unified-wl", ns.Name).
				Queue("pd-lq").
				Obj()
			wl.Spec.PodSets[0].Template.Spec.ResourceClaims = []corev1.PodResourceClaim{
				{Name: "full", ResourceClaimTemplateName: new("whole-gpu")},
				{Name: "mig", ResourceClaimTemplateName: new("mig-1g5gb")},
			}
			wl.Spec.PodSets[0].Template.Spec.Containers[0].Resources.Claims = []corev1.ResourceClaim{
				{Name: "full"},
				{Name: "mig"},
			}
			gomega.Expect(k8sClient.Create(ctx, wl)).To(gomega.Succeed())

			ginkgo.By("Verifying both charges are under gpu.memory")
			gomega.Eventually(func(g gomega.Gomega) {
				var updatedWl kueue.Workload
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &updatedWl)).To(gomega.Succeed())
				g.Expect(workload.HasQuotaReservation(&updatedWl)).To(gomega.BeTrue())

				assignment := updatedWl.Status.Admission.PodSetAssignments[0]
				g.Expect(assignment.ResourceUsage).To(gomega.HaveKey(corev1.ResourceName("gpu.memory")))
				memUsage := assignment.ResourceUsage["gpu.memory"]
				expectedBytes := int64((40320 + 4864) * 1024 * 1024)
				g.Expect(memUsage.Value()).To(gomega.Equal(expectedBytes))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.It("Should mark workload inadmissible with incomplete pool", framework.SlowSpec, func() {
			ginkgo.By("Creating only 1 of 2 ResourceSlices for a pool")
			slice := utiltesting.MakeResourceSlice("pd-incomplete-slice", "gpu.example.com").
				Pool("node1-gpu0", 1, 2).
				Device("gpu-0").Attribute("type", "gpu").
				CounterConsumption("gpu-0-counter-set", "memory", "40320Mi").
				Device("gpu-0-mig-1g5gb-0").Attribute("type", "mig").Attribute("profile", "1g.5gb").
				CounterConsumption("gpu-0-counter-set", "memory", "4864Mi").
				Obj()
			gomega.Expect(k8sClient.Create(ctx, slice)).To(gomega.Succeed())
			ginkgo.DeferCleanup(func() {
				gomega.Expect(k8sClient.Delete(ctx, slice)).To(gomega.Succeed())
			})

			ginkgo.By("Creating RCT with CEL selector")
			rct := utiltesting.MakeResourceClaimTemplate("mig-incomplete", ns.Name).
				DeviceRequest("gpu", "mig.example.com", 1).
				WithCELSelectors("device.attributes['gpu.example.com'].profile == '1g.5gb'").
				Obj()
			gomega.Expect(k8sClient.Create(ctx, rct)).To(gomega.Succeed())

			ginkgo.By("Creating workload")
			wl := utiltestingapi.MakeWorkload("pd-incomplete-wl", ns.Name).
				Queue("pd-lq").
				Obj()
			wl.Spec.PodSets[0].Template.Spec.ResourceClaims = []corev1.PodResourceClaim{
				{Name: "mig", ResourceClaimTemplateName: new("mig-incomplete")},
			}
			wl.Spec.PodSets[0].Template.Spec.Containers[0].Resources.Claims = []corev1.ResourceClaim{
				{Name: "mig"},
			}
			gomega.Expect(k8sClient.Create(ctx, wl)).To(gomega.Succeed())

			ginkgo.By("Verifying workload is inadmissible due to incomplete pool")
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

		ginkgo.It("Should requeue inadmissible workload when ResourceSlice is created", framework.SlowSpec, func() {
			ginkgo.By("Creating RCT with CEL selector")
			rct := utiltesting.MakeResourceClaimTemplate("mig-requeue", ns.Name).
				DeviceRequest("gpu", "mig.example.com", 1).
				WithCELSelectors("device.attributes['gpu.example.com'].profile == '1g.5gb'").
				Obj()
			gomega.Expect(k8sClient.Create(ctx, rct)).To(gomega.Succeed())

			ginkgo.By("Creating workload without any ResourceSlices")
			wl := utiltestingapi.MakeWorkload("pd-requeue-wl", ns.Name).
				Queue("pd-lq").
				Obj()
			wl.Spec.PodSets[0].Template.Spec.ResourceClaims = []corev1.PodResourceClaim{
				{Name: "mig", ResourceClaimTemplateName: new("mig-requeue")},
			}
			wl.Spec.PodSets[0].Template.Spec.Containers[0].Resources.Claims = []corev1.ResourceClaim{
				{Name: "mig"},
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
			slice := utiltesting.MakeResourceSlice("pd-requeue-slice", "gpu.example.com").
				Pool("node1-gpu0", 1, 1).
				Device("gpu-0").Attribute("type", "gpu").
				CounterConsumption("gpu-0-counter-set", "memory", "40320Mi").
				Device("gpu-0-mig-1g5gb-0").Attribute("type", "mig").Attribute("profile", "1g.5gb").
				CounterConsumption("gpu-0-counter-set", "memory", "4864Mi").
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

		ginkgo.It("Should charge consumesCounters for whole GPU request without CEL", func() {
			ginkgo.By("Creating a ResourceSlice with whole GPU device")
			slice := utiltesting.MakeResourceSlice("pd-whole-gpu-slice", "gpu.example.com").
				Pool("node1-gpu0", 1, 1).
				Device("gpu-0").Attribute("type", "gpu").
				CounterConsumption("gpu-0-counter-set", "memory", "40320Mi").
				Obj()
			gomega.Expect(k8sClient.Create(ctx, slice)).To(gomega.Succeed())
			ginkgo.DeferCleanup(func() {
				gomega.Expect(k8sClient.Delete(ctx, slice)).To(gomega.Succeed())
			})

			ginkgo.By("Creating a DeviceClass for whole GPUs")
			dc := utiltesting.MakeDeviceClass("gpu.example.com").Obj()
			gomega.Expect(k8sClient.Create(ctx, dc)).To(gomega.Succeed())
			ginkgo.DeferCleanup(func() {
				gomega.Expect(k8sClient.Delete(ctx, dc)).To(gomega.Succeed())
			})

			ginkgo.By("Creating RCT for whole GPU without CEL selectors")
			rct := utiltesting.MakeResourceClaimTemplate("whole-gpu-nocel", ns.Name).
				DeviceRequest("gpu", "gpu.example.com", 1).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, rct)).To(gomega.Succeed())

			ginkgo.By("Creating workload")
			wl := utiltestingapi.MakeWorkload("pd-whole-gpu-wl", ns.Name).
				Queue("pd-lq").
				Obj()
			wl.Spec.PodSets[0].Template.Spec.ResourceClaims = []corev1.PodResourceClaim{
				{Name: "gpu", ResourceClaimTemplateName: new("whole-gpu-nocel")},
			}
			wl.Spec.PodSets[0].Template.Spec.Containers[0].Resources.Claims = []corev1.ResourceClaim{
				{Name: "gpu"},
			}
			gomega.Expect(k8sClient.Create(ctx, wl)).To(gomega.Succeed())

			ginkgo.By("Verifying workload charges gpu.memory from consumesCounters")
			gomega.Eventually(func(g gomega.Gomega) {
				var updatedWl kueue.Workload
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &updatedWl)).To(gomega.Succeed())
				g.Expect(workload.HasQuotaReservation(&updatedWl)).To(gomega.BeTrue())

				assignment := updatedWl.Status.Admission.PodSetAssignments[0]
				g.Expect(assignment.ResourceUsage).To(gomega.HaveKey(corev1.ResourceName("gpu.memory")))
				memUsage := assignment.ResourceUsage["gpu.memory"]
				g.Expect(memUsage.Cmp(resource.MustParse("40320Mi"))).To(gomega.Equal(0))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.It("Should reject extended resource when DeviceClass has counters", framework.SlowSpec, func() {
			features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.KueueDRAIntegrationExtendedResource, true)

			ginkgo.By("Creating a DeviceClass with extendedResourceName and counters mapping")
			dc := utiltesting.MakeDeviceClass("gpu-er-counter.example.com").
				ExtendedResourceName("example.com/gpu-counter").
				Obj()
			gomega.Expect(k8sClient.Create(ctx, dc)).To(gomega.Succeed())
			ginkgo.DeferCleanup(func() {
				gomega.Expect(k8sClient.Delete(ctx, dc)).To(gomega.Succeed())
			})

			ginkgo.By("Creating workload with extended resource")
			wl := utiltestingapi.MakeWorkload("pd-er-reject-wl", ns.Name).
				Queue("pd-lq").
				Request(corev1.ResourceName("example.com/gpu-counter"), "1").
				Limit(corev1.ResourceName("example.com/gpu-counter"), "1").
				Obj()
			gomega.Expect(k8sClient.Create(ctx, wl)).To(gomega.Succeed())

			ginkgo.By("Verifying workload is marked inadmissible")
			gomega.Eventually(func(g gomega.Gomega) {
				var updatedWl kueue.Workload
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &updatedWl)).To(gomega.Succeed())
				g.Expect(workload.HasQuotaReservation(&updatedWl)).To(gomega.BeFalse())

				g.Expect(updatedWl.Status.Conditions).To(gomega.ContainElement(gomega.And(
					gomega.HaveField("Type", kueue.WorkloadQuotaReserved),
					gomega.HaveField("Status", metav1.ConditionFalse),
					gomega.HaveField("Reason", kueue.WorkloadQuotaReservedReasonMisconfigured),
				)))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.It("Should write granular conditions and reasons when DRA claim resolution fails under the observability feature gate", func() {
			ginkgo.By("Creating a ResourceSlice with MIG devices")
			slice := utiltesting.MakeResourceSlice("pd-nomatch-observability-slice", "gpu.example.com").
				Pool("node1-gpu0", 1, 1).
				Device("gpu-0").Attribute("type", "gpu").
				CounterConsumption("gpu-0-counter-set", "memory", "40320Mi").
				Device("gpu-0-mig-1g5gb-0").Attribute("type", "mig").Attribute("profile", "1g.5gb").
				CounterConsumption("gpu-0-counter-set", "memory", "4864Mi").
				Obj()
			gomega.Expect(k8sClient.Create(ctx, slice)).To(gomega.Succeed())
			ginkgo.DeferCleanup(func() {
				gomega.Expect(k8sClient.Delete(ctx, slice)).To(gomega.Succeed())
			})

			ginkgo.By("Creating RCT with non-matching CEL selectors")
			rct := utiltesting.MakeResourceClaimTemplate("mig-nonexistent-obs", ns.Name).
				DeviceRequest("gpu", "mig.example.com", 1).
				WithCELSelectors("device.attributes['gpu.example.com'].profile == 'nonexistent'").
				Obj()
			gomega.Expect(k8sClient.Create(ctx, rct)).To(gomega.Succeed())

			ginkgo.By("Creating workload")
			wl := utiltestingapi.MakeWorkload("pd-nomatch-observability-wl", ns.Name).
				Queue("pd-lq").
				PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
					ResourceClaimTemplate("mig", "mig-nonexistent-obs").
					Obj()).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, wl)).To(gomega.Succeed())

			ginkgo.By("Verifying workload is marked as inadmissible with granular reasons")
			util.ExpectWorkloadToHaveConditions(ctx, k8sClient, client.ObjectKeyFromObject(wl),
				metav1.Condition{
					Type:    kueue.WorkloadQuotaReserved,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadQuotaReservedReasonMisconfigured,
					Message: "spec.podSets[0].template.spec.resourceClaims[0].devices.requests[0].exactly.selectors: Internal error: ResourceClaimTemplate mig-nonexistent-obs: insufficient matching devices for CEL selector in DeviceClass mig.example.com: 0 device(s) match in the cluster but 1 requested",
				},
				metav1.Condition{
					Type:    kueue.WorkloadAdmitted,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadAdmittedReasonNoReservation,
					Message: "The workload has no reservation",
				},
				metav1.Condition{
					Type:    kueue.WorkloadRequeued,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadInadmissible,
					Message: "spec.podSets[0].template.spec.resourceClaims[0].devices.requests[0].exactly.selectors: Internal error: ResourceClaimTemplate mig-nonexistent-obs: insufficient matching devices for CEL selector in DeviceClass mig.example.com: 0 device(s) match in the cluster but 1 requested",
				},
			)
		})
	})

	ginkgo.When("Partitionable devices borrowing in a cohort", func() {
		var (
			ns             *corev1.Namespace
			resourceFlavor *kueue.ResourceFlavor
			prodCQ         *kueue.ClusterQueue
			devCQ          *kueue.ClusterQueue
			prodLQ         *kueue.LocalQueue
			devLQ          *kueue.LocalQueue
			migDeviceClass *resourcev1.DeviceClass
			slice          *resourcev1.ResourceSlice
		)

		ginkgo.BeforeEach(func() {
			ns = utiltesting.MakeNamespaceWithGenerateName("dra-pd-borrow-")
			gomega.Expect(k8sClient.Create(ctx, ns)).To(gomega.Succeed())

			migDeviceClass = utiltesting.MakeDeviceClass("mig.example.com").
				CELSelector("device.attributes['gpu.example.com'].type == 'mig'").
				Obj()
			gomega.Expect(k8sClient.Create(ctx, migDeviceClass)).To(gomega.Succeed())

			slice = utiltesting.MakeResourceSlice("pd-borrow-slice", "gpu.example.com").
				Pool("node1-gpu0", 1, 1).
				Device("mig-0").Attribute("type", "mig").Attribute("profile", "1g.10gb").
				CounterConsumption("gpu-0-counters", "memory", "10Gi").
				Device("mig-1").Attribute("type", "mig").Attribute("profile", "1g.10gb").
				CounterConsumption("gpu-0-counters", "memory", "10Gi").
				Device("mig-2").Attribute("type", "mig").Attribute("profile", "1g.10gb").
				CounterConsumption("gpu-0-counters", "memory", "10Gi").
				Device("mig-3").Attribute("type", "mig").Attribute("profile", "1g.10gb").
				CounterConsumption("gpu-0-counters", "memory", "10Gi").
				Device("mig-4").Attribute("type", "mig").Attribute("profile", "1g.10gb").
				CounterConsumption("gpu-0-counters", "memory", "10Gi").
				Device("mig-5").Attribute("type", "mig").Attribute("profile", "1g.10gb").
				CounterConsumption("gpu-0-counters", "memory", "10Gi").
				Device("mig-6").Attribute("type", "mig").Attribute("profile", "1g.10gb").
				CounterConsumption("gpu-0-counters", "memory", "10Gi").
				Obj()
			gomega.Expect(k8sClient.Create(ctx, slice)).To(gomega.Succeed())

			resourceFlavor = utiltestingapi.MakeResourceFlavor("").GeneratedName("rf-pd-borrow-").Obj()
			gomega.Expect(k8sClient.Create(ctx, resourceFlavor)).To(gomega.Succeed())

			prodCQ = utiltestingapi.MakeClusterQueue("").GeneratedName("pd-prod-cq-").
				Cohort("pd-cohort").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas(resourceFlavor.Name).
						Resource("gpu.memory", "30Gi").
						Obj(),
				).Obj()
			gomega.Expect(k8sClient.Create(ctx, prodCQ)).To(gomega.Succeed())

			devCQ = utiltestingapi.MakeClusterQueue("").GeneratedName("pd-dev-cq-").
				Cohort("pd-cohort").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas(resourceFlavor.Name).
						Resource("gpu.memory", "30Gi").
						Obj(),
				).Obj()
			gomega.Expect(k8sClient.Create(ctx, devCQ)).To(gomega.Succeed())

			util.ExpectClusterQueuesToBeActive(ctx, k8sClient, prodCQ, devCQ)

			prodLQ = utiltestingapi.MakeLocalQueue("pd-prod-lq", ns.Name).
				ClusterQueue(prodCQ.Name).Obj()
			gomega.Expect(k8sClient.Create(ctx, prodLQ)).To(gomega.Succeed())

			devLQ = utiltestingapi.MakeLocalQueue("pd-dev-lq", ns.Name).
				ClusterQueue(devCQ.Name).Obj()
			gomega.Expect(k8sClient.Create(ctx, devLQ)).To(gomega.Succeed())
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			util.ExpectObjectToBeDeleted(ctx, k8sClient, prodCQ, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, devCQ, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, resourceFlavor, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, migDeviceClass, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, slice, true)
		})

		ginkgo.It("Should borrow counter-based gpu.memory quota from another ClusterQueue in the cohort", func() {
			rct := utiltesting.MakeResourceClaimTemplate("pd-borrow-template", ns.Name).
				DeviceRequest("mig-device", "mig.example.com", 1).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, rct)).To(gomega.Succeed())

			ginkgo.By("Creating workload requesting 5 MIG devices (50Gi > 30Gi nominal, needs borrowing)")
			wl := utiltestingapi.MakeWorkload("pd-borrow-wl", ns.Name).
				Queue("pd-prod-lq").
				PodSets(*utiltestingapi.MakePodSet("main", 5).
					ResourceClaimTemplate("mig-device", "pd-borrow-template").
					Obj()).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, wl)).To(gomega.Succeed())

			ginkgo.By("Verifying workload is admitted and borrows from dev-cq")
			util.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, prodCQ.Name, wl)

			ginkgo.By("Verifying correct gpu.memory charge in admission")
			gomega.Eventually(func(g gomega.Gomega) {
				var updatedWl kueue.Workload
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &updatedWl)).To(gomega.Succeed())
				g.Expect(updatedWl.Status.Admission).NotTo(gomega.BeNil())
				g.Expect(updatedWl.Status.Admission.PodSetAssignments).NotTo(gomega.BeEmpty())
				assignment := updatedWl.Status.Admission.PodSetAssignments[0]
				g.Expect(assignment.ResourceUsage).To(gomega.HaveKey(corev1.ResourceName("gpu.memory")))
				memUsage := assignment.ResourceUsage["gpu.memory"]
				g.Expect(memUsage.Cmp(resource.MustParse("50Gi"))).To(gomega.Equal(0))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			ginkgo.By("Verifying prod-cq shows borrowed gpu.memory")
			gomega.Eventually(func(g gomega.Gomega) {
				var updatedCQ kueue.ClusterQueue
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(prodCQ), &updatedCQ)).To(gomega.Succeed())
				found := false
				for _, flavorUsage := range updatedCQ.Status.FlavorsUsage {
					for _, ru := range flavorUsage.Resources {
						if ru.Name == "gpu.memory" {
							found = true
							g.Expect(ru.Total.Cmp(resource.MustParse("50Gi"))).To(gomega.Equal(0))
							g.Expect(ru.Borrowed.Cmp(resource.MustParse("20Gi"))).To(gomega.Equal(0))
							g.Expect(ru.Total.String()).To(gomega.Equal("50Gi"))
							g.Expect(ru.Borrowed.String()).To(gomega.Equal("20Gi"))
						}
					}
				}
				g.Expect(found).To(gomega.BeTrue(), "resource gpu.memory not found in FlavorsUsage")
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.It("Should not admit PD workload when cohort counter capacity is exhausted", func() {
			rct := utiltesting.MakeResourceClaimTemplate("pd-exhaust-template", ns.Name).
				DeviceRequest("mig-device", "mig.example.com", 1).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, rct)).To(gomega.Succeed())

			ginkgo.By("Filling prod-cq counter quota (3 devices = 30Gi = nominal)")
			prodWl := utiltestingapi.MakeWorkload("pd-prod-full", ns.Name).
				Queue("pd-prod-lq").
				PodSets(*utiltestingapi.MakePodSet("main", 3).
					ResourceClaimTemplate("mig-device", "pd-exhaust-template").
					Obj()).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, prodWl)).To(gomega.Succeed())

			ginkgo.By("Filling dev-cq counter quota (3 devices = 30Gi = nominal)")
			devWl := utiltestingapi.MakeWorkload("pd-dev-full", ns.Name).
				Queue("pd-dev-lq").
				PodSets(*utiltestingapi.MakePodSet("main", 3).
					ResourceClaimTemplate("mig-device", "pd-exhaust-template").
					Obj()).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, devWl)).To(gomega.Succeed())

			util.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, prodCQ.Name, prodWl)
			util.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, devCQ.Name, devWl)

			ginkgo.By("Creating a workload that exceeds total cohort counter capacity")
			overflowWl := utiltestingapi.MakeWorkload("pd-overflow-wl", ns.Name).
				Queue("pd-prod-lq").
				PodSets(*utiltestingapi.MakePodSet("main", 1).
					ResourceClaimTemplate("mig-device", "pd-exhaust-template").
					Obj()).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, overflowWl)).To(gomega.Succeed())

			ginkgo.By("Verifying overflow workload stays pending")
			util.ExpectWorkloadsToBePending(ctx, k8sClient, overflowWl)
		})
	})
})
