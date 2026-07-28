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

package baseline

import (
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	controllerconstants "sigs.k8s.io/kueue/pkg/controller/constants"
	workloadjob "sigs.k8s.io/kueue/pkg/controller/jobs/job"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("ConcurrentAdmission", ginkgo.Label("area:singlecluster", "feature:concurrent-admission"), func() {
	var ns *corev1.Namespace

	ginkgo.BeforeEach(func() {
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "e2e-concurrent-")
	})
	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		util.ExpectAllPodsInNamespaceDeleted(ctx, k8sClient, ns)
	})

	ginkgo.When("Job is submitted to a ClusterQueue with ConcurrentAdmissionPolicy", func() {
		var (
			reservationRF     *kueue.ResourceFlavor
			spotRF            *kueue.ResourceFlavor
			cq                *kueue.ClusterQueue
			lq                *kueue.LocalQueue
			reservationFlavor string
			spotFlavor        string
		)

		ginkgo.BeforeEach(func() {
			reservationFlavor = "reservation-" + ns.Name
			spotFlavor = "spot-" + ns.Name

			reservationRF = utiltestingapi.MakeResourceFlavor(reservationFlavor).
				NodeLabel("instance-type", "reservation").Obj()
			util.MustCreate(ctx, k8sClient, reservationRF)

			spotRF = utiltestingapi.MakeResourceFlavor(spotFlavor).
				NodeLabel("instance-type", "spot").Obj()
			util.MustCreate(ctx, k8sClient, spotRF)

			cq = utiltestingapi.MakeClusterQueue("cq-concurrent-"+ns.Name).
				ConcurrentAdmissionPolicy(kueue.ConcurrentAdmissionTryPreferredFlavors).
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas(reservationFlavor).
						Resource(corev1.ResourceCPU, "5").
						Resource(corev1.ResourceMemory, "1Gi").Obj(),
					*utiltestingapi.MakeFlavorQuotas(spotFlavor).
						Resource(corev1.ResourceCPU, "5").
						Resource(corev1.ResourceMemory, "1Gi").Obj(),
				).Obj()
			util.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, cq)

			lq = utiltestingapi.MakeLocalQueue("main", ns.Name).ClusterQueue(cq.Name).Obj()
			util.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, lq)
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteAllJobsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
			gomega.Expect(util.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
			util.ExpectObjectToBeDeleted(ctx, k8sClient, lq, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, reservationRF, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, spotRF, true)
		})

		ginkgo.It("Should create one Parent Workload and one Variant per flavor", func() {
			job := testingjob.MakeJob("job", ns.Name).
				Queue(kueue.LocalQueueName(lq.Name)).
				Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
				RequestAndLimit(corev1.ResourceCPU, "200m").
				RequestAndLimit(corev1.ResourceMemory, "20Mi").
				TerminationGracePeriod(1).
				Obj()
			util.MustCreate(ctx, k8sClient, job)

			parentWlKey := types.NamespacedName{
				Name:      workloadjob.GetWorkloadNameForJob(job.Name, job.UID),
				Namespace: ns.Name,
			}

			ginkgo.By("Verifying the Parent Workload is labeled correctly", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					var parentWl kueue.Workload
					g.Expect(k8sClient.Get(ctx, parentWlKey, &parentWl)).To(gomega.Succeed())
					g.Expect(parentWl.Labels[controllerconstants.ConcurrentAdmissionParentLabelKey]).To(gomega.Equal("true"))
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Verifying one Variant Workload is created per flavor (2 variants + 1 parent = 3 total)", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					var wlList kueue.WorkloadList
					g.Expect(k8sClient.List(ctx, &wlList, client.InNamespace(ns.Name))).To(gomega.Succeed())
					g.Expect(wlList.Items).To(gomega.HaveLen(3))

					var variantFlavors []string
					for _, wl := range wlList.Items {
						if ann := wl.Annotations[controllerconstants.WorkloadAllowedResourceFlavorAnnotation]; ann != "" {
							variantFlavors = append(variantFlavors, ann)
						}
					}
					g.Expect(variantFlavors).To(gomega.ConsistOf(reservationFlavor, spotFlavor))
				}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Verifying each Variant has an owner reference pointing to the Parent", func() {
				var parentWl kueue.Workload
				gomega.Expect(k8sClient.Get(ctx, parentWlKey, &parentWl)).To(gomega.Succeed())

				gomega.Eventually(func(g gomega.Gomega) {
					var wlList kueue.WorkloadList
					g.Expect(k8sClient.List(ctx, &wlList, client.InNamespace(ns.Name))).To(gomega.Succeed())
					g.Expect(findVariantsForParent(wlList.Items, &parentWl)).To(gomega.HaveLen(2))
				}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("Should migrate Job to more favorable flavor when its quota becomes available", func() {
			ginkgo.By("Setting reservation quota to zero to force initial admission on spot", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					var updatedCq kueue.ClusterQueue
					g.Expect(k8sClient.Get(ctx, client.ObjectKey{Name: cq.Name}, &updatedCq)).To(gomega.Succeed())
					updatedCq.Spec.ResourceGroups[0].Flavors[0].Resources[0].NominalQuota = resource.MustParse("0")
					updatedCq.Spec.ResourceGroups[0].Flavors[0].Resources[1].NominalQuota = resource.MustParse("0")
					g.Expect(k8sClient.Update(ctx, &updatedCq)).To(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			job := testingjob.MakeJob("job", ns.Name).
				Queue(kueue.LocalQueueName(lq.Name)).
				Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
				RequestAndLimit(corev1.ResourceCPU, "200m").
				RequestAndLimit(corev1.ResourceMemory, "20Mi").
				TerminationGracePeriod(1).
				Obj()
			util.MustCreate(ctx, k8sClient, job)

			parentWlKey := types.NamespacedName{
				Name:      workloadjob.GetWorkloadNameForJob(job.Name, job.UID),
				Namespace: ns.Name,
			}

			ginkgo.By("Verifying Parent Workload is admitted on spot flavor", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					var parentWl kueue.Workload
					g.Expect(k8sClient.Get(ctx, parentWlKey, &parentWl)).To(gomega.Succeed())
					g.Expect(workload.IsAdmitted(&parentWl)).To(gomega.BeTrue())
					g.Expect(parentWl.Status.Admission).ToNot(gomega.BeNil())
					g.Expect(parentWl.Status.Admission.PodSetAssignments).ToNot(gomega.BeEmpty())
					g.Expect(parentWl.Status.Admission.PodSetAssignments[0].Flavors[corev1.ResourceCPU]).To(
						gomega.Equal(kueue.ResourceFlavorReference(spotFlavor)))
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Verifying spot Variant is admitted and reservation Variant is pending", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					var wlList kueue.WorkloadList
					g.Expect(k8sClient.List(ctx, &wlList, client.InNamespace(ns.Name))).To(gomega.Succeed())

					spotVariant := getVariantByFlavor(wlList.Items, parentWlKey.Name, spotFlavor)
					reservationVariant := getVariantByFlavor(wlList.Items, parentWlKey.Name, reservationFlavor)
					g.Expect(spotVariant).ToNot(gomega.BeNil(), "spot variant not found")
					g.Expect(reservationVariant).ToNot(gomega.BeNil(), "reservation variant not found")
					g.Expect(workload.IsAdmitted(spotVariant)).To(gomega.BeTrue(), "spot variant should be admitted")
					g.Expect(ptr.Deref(reservationVariant.Spec.Active, true)).To(gomega.BeTrue(), "reservation variant should still be active")
				}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Releasing reservation quota", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					var updatedCq kueue.ClusterQueue
					g.Expect(k8sClient.Get(ctx, client.ObjectKey{Name: cq.Name}, &updatedCq)).To(gomega.Succeed())
					updatedCq.Spec.ResourceGroups[0].Flavors[0].Resources[0].NominalQuota = resource.MustParse("5")
					updatedCq.Spec.ResourceGroups[0].Flavors[0].Resources[1].NominalQuota = resource.MustParse("1Gi")
					g.Expect(k8sClient.Update(ctx, &updatedCq)).To(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Verifying Parent Workload migrates to reservation flavor", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					var parentWl kueue.Workload
					g.Expect(k8sClient.Get(ctx, parentWlKey, &parentWl)).To(gomega.Succeed())
					g.Expect(workload.IsAdmitted(&parentWl)).To(gomega.BeTrue())
					g.Expect(parentWl.Status.Admission).ToNot(gomega.BeNil())
					g.Expect(parentWl.Status.Admission.PodSetAssignments).ToNot(gomega.BeEmpty())
					g.Expect(parentWl.Status.Admission.PodSetAssignments[0].Flavors[corev1.ResourceCPU]).To(
						gomega.Equal(kueue.ResourceFlavorReference(reservationFlavor)))
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Verifying spot Variant is deactivated after migration", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					var wlList kueue.WorkloadList
					g.Expect(k8sClient.List(ctx, &wlList, client.InNamespace(ns.Name))).To(gomega.Succeed())

					spotVariant := getVariantByFlavor(wlList.Items, parentWlKey.Name, spotFlavor)
					g.Expect(spotVariant).ToNot(gomega.BeNil(), "spot variant not found")
					g.Expect(ptr.Deref(spotVariant.Spec.Active, true)).To(gomega.BeFalse(), "spot variant should be deactivated after migration")
				}, util.MediumTimeout, util.Interval).Should(gomega.Succeed())
			})
		})
	})
})

func findVariantsForParent(workloads []kueue.Workload, parent *kueue.Workload) []kueue.Workload {
	var variants []kueue.Workload
	for i := range workloads {
		wl := &workloads[i]
		if wl.Name == parent.Name {
			continue
		}
		for _, ref := range wl.OwnerReferences {
			if ref.UID == parent.UID {
				variants = append(variants, *wl)
				break
			}
		}
	}
	return variants
}

func getVariantByFlavor(workloads []kueue.Workload, parentName string, flavor string) *kueue.Workload {
	for i := range workloads {
		wl := &workloads[i]
		if wl.Name == parentName {
			continue
		}
		for _, owner := range wl.OwnerReferences {
			if owner.Name == parentName {
				if ann := wl.Annotations[controllerconstants.WorkloadAllowedResourceFlavorAnnotation]; ann == flavor {
					return wl
				}
			}
		}
	}
	return nil
}
