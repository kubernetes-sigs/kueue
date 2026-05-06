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

package concurrentadmission

import (
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	controllerconstants "sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/features"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("Concurrent Admission", ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	var (
		ns *corev1.Namespace
	)

	ginkgo.BeforeEach(func() {
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.ConcurrentAdmission, true)
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "concurrent-")
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
	})

	ginkgo.BeforeAll(func() {
		fwk.StartManager(ctx, cfg, managerAndSchedulerSetup(&configapi.Configuration{}))
	})

	ginkgo.AfterAll(func() {
		fwk.StopManager(ctx)
	})

	ginkgo.When("Should assign flavors with respect to constraints", func() {
		var cq *kueue.ClusterQueue
		var lq *kueue.LocalQueue
		var flavorReservation *kueue.ResourceFlavor
		var flavorSpot *kueue.ResourceFlavor

		ginkgo.BeforeEach(func() {
			flavorReservation = utiltestingapi.MakeResourceFlavor("reservation").Obj()
			util.MustCreate(ctx, k8sClient, flavorReservation)

			flavorSpot = utiltestingapi.MakeResourceFlavor("spot").Obj()
			util.MustCreate(ctx, k8sClient, flavorSpot)

			cq = utiltestingapi.MakeClusterQueue("cq-constraints").
				ConcurrentAdmissionPolicy(kueue.ConcurrentAdmissionTryPreferredFlavors).
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas(flavorReservation.Name).Resource(corev1.ResourceCPU, "5").Obj(),
					*utiltestingapi.MakeFlavorQuotas(flavorSpot.Name).Resource(corev1.ResourceCPU, "5").Obj(),
				).Obj()
			util.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, cq)

			lq = utiltestingapi.MakeLocalQueue("lq", ns.Name).ClusterQueue(cq.Name).Obj()
			util.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, lq)
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			util.ExpectObjectToBeDeleted(ctx, k8sClient, lq, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, flavorSpot, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, flavorReservation, true)
		})

		ginkgo.It("Creates variants for all flavors in the ClusterQueue", func() {
			parentWl := utiltestingapi.MakeWorkload("parent-wl-constraints", ns.Name).
				Request(corev1.ResourceCPU, "1").
				Queue(kueue.LocalQueueName(lq.Name)).
				ParentVariant().
				Obj()

			ginkgo.By("Creating the parent workload", func() {
				util.MustCreate(ctx, k8sClient, parentWl)
			})

			ginkgo.By("Verifying variants have correct flavor assigned", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					list := &kueue.WorkloadList{}
					g.Expect(k8sClient.List(ctx, list, client.InNamespace(ns.Name))).To(gomega.Succeed())

					variantA := getVariantByFlavor(list, parentWl.Name, flavorReservation.Name)
					variantB := getVariantByFlavor(list, parentWl.Name, flavorSpot.Name)

					g.Expect(variantA).ToNot(gomega.BeNil(), "Variant for reservation not found")
					g.Expect(variantB).ToNot(gomega.BeNil(), "Variant for spot not found")
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})
	})

	ginkgo.When("Should migrate to a target flavor if quota is released", func() {
		var cq *kueue.ClusterQueue
		var lq *kueue.LocalQueue
		var flavorReservation *kueue.ResourceFlavor
		var flavorSpot *kueue.ResourceFlavor

		ginkgo.BeforeEach(func() {
			flavorReservation = utiltestingapi.MakeResourceFlavor("reservation").Obj()
			util.MustCreate(ctx, k8sClient, flavorReservation)

			flavorSpot = utiltestingapi.MakeResourceFlavor("spot").Obj()
			util.MustCreate(ctx, k8sClient, flavorSpot)

			cq = utiltestingapi.MakeClusterQueue("cq-migrate").
				ConcurrentAdmissionPolicy(kueue.ConcurrentAdmissionTryPreferredFlavors).
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas(flavorReservation.Name).Resource(corev1.ResourceCPU, "0").Obj(),
					*utiltestingapi.MakeFlavorQuotas(flavorSpot.Name).Resource(corev1.ResourceCPU, "5").Obj(),
				).Obj()
			util.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, cq)

			lq = utiltestingapi.MakeLocalQueue("lq", ns.Name).ClusterQueue(cq.Name).Obj()
			util.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, lq)
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			util.ExpectObjectToBeDeleted(ctx, k8sClient, lq, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, flavorSpot, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, flavorReservation, true)
		})

		ginkgo.It("should migrate to the preferred flavor when it becomes available", func() {
			parentWl := utiltestingapi.MakeWorkload("parent-wl-migrate", ns.Name).
				Request(corev1.ResourceCPU, "1").
				Queue(kueue.LocalQueueName(lq.Name)).
				ParentVariant().
				Obj()

			ginkgo.By("Creating the parent workload", func() {
				util.MustCreate(ctx, k8sClient, parentWl)
			})

			ginkgo.By("Verifying workload is admitted on spot", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(parentWl), parentWl)).To(gomega.Succeed())
					g.Expect(workload.IsAdmitted(parentWl)).To(gomega.BeTrue())
					g.Expect(parentWl.Status.Admission.PodSetAssignments[0].Flavors[corev1.ResourceCPU]).To(gomega.Equal(kueue.ResourceFlavorReference(flavorSpot.Name)))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Releasing quota on reservation", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					var updatedCq kueue.ClusterQueue
					g.Expect(k8sClient.Get(ctx, client.ObjectKey{Name: cq.Name}, &updatedCq)).To(gomega.Succeed())
					updatedCq.Spec.ResourceGroups[0].Flavors[0].Resources[0].NominalQuota = resource.MustParse("5")
					g.Expect(k8sClient.Update(ctx, &updatedCq)).To(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Finishing eviction of parent workload", func() {
				util.FinishEvictionForWorkloads(ctx, k8sClient, parentWl)
			})

			ginkgo.By("Verifying workload migrates to reservation", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(parentWl), parentWl)).To(gomega.Succeed())
					g.Expect(workload.IsAdmitted(parentWl)).To(gomega.BeTrue())
					g.Expect(parentWl.Status.Admission).ToNot(gomega.BeNil())
					g.Expect(parentWl.Status.Admission.PodSetAssignments).ToNot(gomega.BeEmpty())
					g.Expect(parentWl.Status.Admission.PodSetAssignments[0].Flavors[corev1.ResourceCPU]).To(gomega.Equal(kueue.ResourceFlavorReference(flavorReservation.Name)))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Verifying spot variant is deactivated", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					list := &kueue.WorkloadList{}
					g.Expect(k8sClient.List(ctx, list, client.InNamespace(ns.Name))).To(gomega.Succeed())

					variantSpot := getVariantByFlavor(list, parentWl.Name, flavorSpot.Name)
					g.Expect(variantSpot).ToNot(gomega.BeNil(), "Variant for spot not found")
					g.Expect(ptr.Deref(variantSpot.Spec.Active, true)).To(gomega.BeFalse(), "Variant for spot should be inactive")
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})
	})

	ginkgo.When("Should not migrate to a flavor below min target", func() {
		var cq *kueue.ClusterQueue
		var lq *kueue.LocalQueue
		var flavorReservation *kueue.ResourceFlavor
		var flavorOnDemand *kueue.ResourceFlavor
		var flavorSpot *kueue.ResourceFlavor

		ginkgo.BeforeEach(func() {
			flavorReservation = utiltestingapi.MakeResourceFlavor("reservation").Obj()
			util.MustCreate(ctx, k8sClient, flavorReservation)

			flavorOnDemand = utiltestingapi.MakeResourceFlavor("on-demand").Obj()
			util.MustCreate(ctx, k8sClient, flavorOnDemand)

			flavorSpot = utiltestingapi.MakeResourceFlavor("spot").Obj()
			util.MustCreate(ctx, k8sClient, flavorSpot)

			cq = utiltestingapi.MakeClusterQueue("cq-no-migrate").
				ConcurrentAdmissionPolicy(kueue.ConcurrentAdmissionTryPreferredFlavors).
				MinPreferredFlavorName(flavorReservation.Name).
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas(flavorReservation.Name).Resource(corev1.ResourceCPU, "0").Obj(),
					*utiltestingapi.MakeFlavorQuotas(flavorOnDemand.Name).Resource(corev1.ResourceCPU, "0").Obj(),
					*utiltestingapi.MakeFlavorQuotas(flavorSpot.Name).Resource(corev1.ResourceCPU, "5").Obj(),
				).Obj()
			util.MustCreate(ctx, k8sClient, cq)

			lq = utiltestingapi.MakeLocalQueue("lq", ns.Name).ClusterQueue(cq.Name).Obj()
			util.MustCreate(ctx, k8sClient, lq)

			util.ExpectClusterQueuesToBeActive(ctx, k8sClient, cq)
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			util.ExpectObjectToBeDeleted(ctx, k8sClient, lq, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, flavorSpot, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, flavorOnDemand, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, flavorReservation, true)
		})

		ginkgo.It("should only migrate to flavors at or above the minimum preferred flavor", func() {
			parentWl := utiltestingapi.MakeWorkload("parent-wl-no-migrate", ns.Name).
				Request(corev1.ResourceCPU, "1").
				Queue(kueue.LocalQueueName(lq.Name)).
				ParentVariant().
				Obj()

			ginkgo.By("Creating the parent workload", func() {
				util.MustCreate(ctx, k8sClient, parentWl)
			})

			ginkgo.By("Verifying workload is admitted on spot", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(parentWl), parentWl)).To(gomega.Succeed())
					g.Expect(workload.IsAdmitted(parentWl)).To(gomega.BeTrue())
					g.Expect(parentWl.Status.Admission.PodSetAssignments[0].Flavors[corev1.ResourceCPU]).To(gomega.Equal(kueue.ResourceFlavorReference(flavorSpot.Name)))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Verifying on-demand variant is deactivated", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					list := &kueue.WorkloadList{}
					g.Expect(k8sClient.List(ctx, list, client.InNamespace(ns.Name))).To(gomega.Succeed())

					variantOnDemand := getVariantByFlavor(list, parentWl.Name, flavorOnDemand.Name)
					g.Expect(variantOnDemand).ToNot(gomega.BeNil(), "Variant for on-demand not found")
					g.Expect(ptr.Deref(variantOnDemand.Spec.Active, true)).To(gomega.BeFalse(), "Variant for on-demand should be inactive")
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Releasing quota on on-demand", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					var updatedCq kueue.ClusterQueue
					g.Expect(k8sClient.Get(ctx, client.ObjectKey{Name: cq.Name}, &updatedCq)).To(gomega.Succeed())
					updatedCq.Spec.ResourceGroups[0].Flavors[1].Resources[0].NominalQuota = resource.MustParse("5")
					g.Expect(k8sClient.Update(ctx, &updatedCq)).To(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Verifying workload does not migrate to on-demand", func() {
				gomega.Consistently(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(parentWl), parentWl)).To(gomega.Succeed())
					g.Expect(parentWl.Status.Admission.PodSetAssignments[0].Flavors[corev1.ResourceCPU]).To(gomega.Equal(kueue.ResourceFlavorReference(flavorSpot.Name)))
				}, util.ConsistentDuration, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Releasing quota on reservation", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					var updatedCq kueue.ClusterQueue
					g.Expect(k8sClient.Get(ctx, client.ObjectKey{Name: cq.Name}, &updatedCq)).To(gomega.Succeed())
					updatedCq.Spec.ResourceGroups[0].Flavors[0].Resources[0].NominalQuota = resource.MustParse("5")
					g.Expect(k8sClient.Update(ctx, &updatedCq)).To(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Finishing eviction of parent workload", func() {
				util.FinishEvictionForWorkloads(ctx, k8sClient, parentWl)
			})

			ginkgo.By("Verifying workload migrates to reservation", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(parentWl), parentWl)).To(gomega.Succeed())
					g.Expect(workload.IsAdmitted(parentWl)).To(gomega.BeTrue())
					g.Expect(parentWl.Status.Admission).ToNot(gomega.BeNil())
					g.Expect(parentWl.Status.Admission.PodSetAssignments).ToNot(gomega.BeEmpty())
					g.Expect(parentWl.Status.Admission.PodSetAssignments[0].Flavors[corev1.ResourceCPU]).To(gomega.Equal(kueue.ResourceFlavorReference(flavorReservation.Name)))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})
	})

	ginkgo.When("ClusterQueue has admission checks", func() {
		var cq *kueue.ClusterQueue
		var lq *kueue.LocalQueue
		var ac *kueue.AdmissionCheck
		var flavorReservation *kueue.ResourceFlavor
		var flavorProvReq *kueue.ResourceFlavor

		ginkgo.BeforeEach(func() {
			ac = utiltestingapi.MakeAdmissionCheck("ac").ControllerName("ac-controller").Obj()
			util.MustCreate(ctx, k8sClient, ac)
			util.SetAdmissionCheckActive(ctx, k8sClient, ac, metav1.ConditionTrue)

			flavorReservation = utiltestingapi.MakeResourceFlavor("reservation").Obj()
			util.MustCreate(ctx, k8sClient, flavorReservation)

			flavorProvReq = utiltestingapi.MakeResourceFlavor("provreq-flavor").Obj()
			util.MustCreate(ctx, k8sClient, flavorProvReq)

			cq = utiltestingapi.MakeClusterQueue("cq-ac").
				ConcurrentAdmissionPolicy(kueue.ConcurrentAdmissionTryPreferredFlavors).
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas(flavorReservation.Name).Resource(corev1.ResourceCPU, "0").Obj(),
					*utiltestingapi.MakeFlavorQuotas(flavorProvReq.Name).Resource(corev1.ResourceCPU, "5").Obj(),
				).
				AdmissionCheckStrategy(
					*utiltestingapi.MakeAdmissionCheckStrategyRule(kueue.AdmissionCheckReference(ac.Name), kueue.ResourceFlavorReference(flavorProvReq.Name)).Obj(),
				).Obj()
			util.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, cq)

			lq = utiltestingapi.MakeLocalQueue("lq", ns.Name).ClusterQueue(cq.Name).Obj()
			util.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, lq)
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			util.ExpectObjectToBeDeleted(ctx, k8sClient, lq, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, flavorProvReq, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, flavorReservation, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, ac, true)
		})

		ginkgo.It("Should wait for variant admission checks before admitting parent workload", func() {
			parentWl := utiltestingapi.MakeWorkload("parent-wl-ac", ns.Name).
				Request(corev1.ResourceCPU, "1").
				Queue(kueue.LocalQueueName(lq.Name)).
				ParentVariant().
				Obj()

			ginkgo.By("Creating the parent workload", func() {
				util.MustCreate(ctx, k8sClient, parentWl)
			})

			var variantProvReq *kueue.Workload
			ginkgo.By("Verifying variant has quota reservation on provreq-flavor", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					list := &kueue.WorkloadList{}
					g.Expect(k8sClient.List(ctx, list, client.InNamespace(ns.Name))).To(gomega.Succeed())

					variantProvReq = getVariantByFlavor(list, parentWl.Name, flavorProvReq.Name)
					g.Expect(variantProvReq).ToNot(gomega.BeNil(), "Variant for provreq-flavor not found")
					g.Expect(workload.HasQuotaReservation(variantProvReq)).To(gomega.BeTrue())
					g.Expect(workload.IsAdmitted(variantProvReq)).To(gomega.BeFalse())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Verifying parent workload is not admitted", func() {
				gomega.Consistently(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(parentWl), parentWl)).To(gomega.Succeed())
					g.Expect(workload.IsAdmitted(parentWl)).To(gomega.BeFalse())
				}, util.ConsistentDuration, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Simulating Admission Check success on variant", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(variantProvReq), variantProvReq)).To(gomega.Succeed())
					workload.SetAdmissionCheckState(&variantProvReq.Status.AdmissionChecks, kueue.AdmissionCheckState{
						Name:               kueue.AdmissionCheckReference(ac.Name),
						State:              kueue.CheckStateReady,
						LastTransitionTime: metav1.Now(),
						Message:            "Admission check succeeded",
					}, util.RealClock)
					g.Expect(k8sClient.Status().Update(ctx, variantProvReq)).To(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Verifying variant is admitted", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(variantProvReq), variantProvReq)).To(gomega.Succeed())
					g.Expect(workload.IsAdmitted(variantProvReq)).To(gomega.BeTrue())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Verifying parent workload is admitted and has no admission checks", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(parentWl), parentWl)).To(gomega.Succeed())
					g.Expect(workload.IsAdmitted(parentWl)).To(gomega.BeTrue())
					g.Expect(parentWl.Status.AdmissionChecks).To(gomega.BeEmpty())
					g.Expect(parentWl.Status.Admission.PodSetAssignments[0].Flavors[corev1.ResourceCPU]).To(gomega.Equal(kueue.ResourceFlavorReference("provreq-flavor")))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})
	})
})

func getVariantByFlavor(list *kueue.WorkloadList, parentName string, flavor string) *kueue.Workload {
	for i := range list.Items {
		item := &list.Items[i]
		if item.Name == parentName {
			continue
		}
		for _, owner := range item.OwnerReferences {
			if owner.Name == parentName {
				ann := item.GetAnnotations()
				if ann != nil && ann[controllerconstants.WorkloadAllowedResourceFlavorAnnotation] == flavor {
					return item
				}
			}
		}
	}
	return nil
}
