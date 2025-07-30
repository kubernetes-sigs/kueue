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

package core

import (
	"fmt"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/clock"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	config "sigs.k8s.io/kueue/apis/config/v1beta1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/util/slices"
	"sigs.k8s.io/kueue/pkg/util/testing"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("Workload controller", ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	var (
		ns                           *corev1.Namespace
		updatedQueueWorkload         kueue.Workload
		finalQueueWorkload           kueue.Workload
		localQueue                   *kueue.LocalQueue
		wl                           *kueue.Workload
		message                      string
		clusterQueue                 *kueue.ClusterQueue
		workloadPriorityClass        *kueue.WorkloadPriorityClass
		updatedWorkloadPriorityClass *kueue.WorkloadPriorityClass
		realClock                    = clock.RealClock{}
	)

	ginkgo.BeforeAll(func() {
		fwk.StartManager(ctx, cfg, managerSetup)
	})

	ginkgo.AfterAll(func() {
		fwk.StopManager(ctx)
	})

	ginkgo.BeforeEach(func() {
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "core-workload-")
	})

	ginkgo.AfterEach(func() {
		clusterQueue = nil
		localQueue = nil
		updatedQueueWorkload = kueue.Workload{}
	})

	ginkgo.When("the queue is not defined in the workload", func() {
		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		})
		ginkgo.It("Should update status when workloads are created", func() {
			wl = testing.MakeWorkload("one", ns.Name).Request(corev1.ResourceCPU, "1").Obj()
			message = fmt.Sprintf("LocalQueue %s doesn't exist", "")
			util.MustCreate(ctx, k8sClient, wl)
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &updatedQueueWorkload)).To(gomega.Succeed())
				g.Expect(updatedQueueWorkload.Status.Conditions).Should(gomega.HaveLen(1))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
			gomega.Expect(updatedQueueWorkload.Status.Conditions[0]).To(
				gomega.BeComparableTo(metav1.Condition{
					Type:    kueue.WorkloadQuotaReserved,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadInadmissible,
					Message: message,
				}, util.IgnoreConditionTimestampsAndObservedGeneration),
			)
		})
	})

	ginkgo.When("the queue doesn't exist", func() {
		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		})
		ginkgo.It("Should update status when workloads are created", func() {
			wl = testing.MakeWorkload("two", ns.Name).Queue("non-created-queue").Request(corev1.ResourceCPU, "1").Obj()
			message = fmt.Sprintf("LocalQueue %s doesn't exist", "non-created-queue")
			util.MustCreate(ctx, k8sClient, wl)
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &updatedQueueWorkload)).To(gomega.Succeed())
				g.Expect(updatedQueueWorkload.Status.Conditions).Should(gomega.HaveLen(1))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
			gomega.Expect(updatedQueueWorkload.Status.Conditions[0]).To(
				gomega.BeComparableTo(metav1.Condition{
					Type:    kueue.WorkloadQuotaReserved,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadInadmissible,
					Message: message,
				}, util.IgnoreConditionTimestampsAndObservedGeneration),
			)
		})
	})

	ginkgo.When("the clusterqueue doesn't exist", func() {
		ginkgo.BeforeEach(func() {
			localQueue = testing.MakeLocalQueue("queue", ns.Name).ClusterQueue("fooclusterqueue").Obj()
			util.MustCreate(ctx, k8sClient, localQueue)
		})
		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		})
		ginkgo.It("Should update status when workloads are created", func() {
			wl = testing.MakeWorkload("three", ns.Name).Queue(kueue.LocalQueueName(localQueue.Name)).Request(corev1.ResourceCPU, "1").Obj()
			message = fmt.Sprintf("ClusterQueue %s doesn't exist", "fooclusterqueue")
			util.MustCreate(ctx, k8sClient, wl)
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &updatedQueueWorkload)).To(gomega.Succeed())
				g.Expect(updatedQueueWorkload.Status.Conditions).ShouldNot(gomega.BeNil())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
			gomega.Expect(updatedQueueWorkload.Status.Conditions[0]).To(
				gomega.BeComparableTo(metav1.Condition{
					Type:    kueue.WorkloadQuotaReserved,
					Status:  metav1.ConditionFalse,
					Reason:  kueue.WorkloadInadmissible,
					Message: message,
				}, util.IgnoreConditionTimestampsAndObservedGeneration),
			)
		})
	})

	ginkgo.When("the workload is admitted", func() {
		var flavor *kueue.ResourceFlavor

		ginkgo.BeforeEach(func() {
			flavor = testing.MakeResourceFlavor(flavorOnDemand).Obj()
			util.MustCreate(ctx, k8sClient, flavor)
			clusterQueue = testing.MakeClusterQueue("cluster-queue").
				ResourceGroup(*testing.MakeFlavorQuotas(flavorOnDemand).
					Resource(resourceGPU, "5", "5").Obj()).
				Cohort("cohort").
				Obj()
			util.MustCreate(ctx, k8sClient, clusterQueue)
			localQueue = testing.MakeLocalQueue("queue", ns.Name).ClusterQueue(clusterQueue.Name).Obj()
			util.MustCreate(ctx, k8sClient, localQueue)
		})
		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			util.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, flavor, true)
		})
	})

	ginkgo.When("the queue has admission checks", func() {
		var (
			flavor *kueue.ResourceFlavor
			check1 *kueue.AdmissionCheck
			check2 *kueue.AdmissionCheck
		)

		ginkgo.BeforeEach(func() {
			flavor = testing.MakeResourceFlavor(flavorOnDemand).Obj()
			util.MustCreate(ctx, k8sClient, flavor)

			check1 = testing.MakeAdmissionCheck("check1").ControllerName("ctrl").Obj()
			util.MustCreate(ctx, k8sClient, check1)
			util.SetAdmissionCheckActive(ctx, k8sClient, check1, metav1.ConditionTrue)

			check2 = testing.MakeAdmissionCheck("check2").ControllerName("ctrl").Obj()
			util.MustCreate(ctx, k8sClient, check2)
			util.SetAdmissionCheckActive(ctx, k8sClient, check2, metav1.ConditionTrue)

			clusterQueue = testing.MakeClusterQueue("cluster-queue").
				ResourceGroup(*testing.MakeFlavorQuotas(flavorOnDemand).
					Resource(resourceGPU, "5", "5").Obj()).
				Cohort("cohort").
				AdmissionChecks("check1", "check2").
				Obj()
			util.MustCreate(ctx, k8sClient, clusterQueue)
			localQueue = testing.MakeLocalQueue("queue", ns.Name).ClusterQueue(clusterQueue.Name).Obj()
			util.MustCreate(ctx, k8sClient, localQueue)
		})
		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			util.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, check2, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, check1, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, flavor, true)
		})

		ginkgo.It("the workload should get the AdditionalChecks added", func() {
			wl := testing.MakeWorkload("wl", ns.Name).Queue("queue").Obj()
			wlKey := client.ObjectKeyFromObject(wl)
			createdWl := kueue.Workload{}
			ginkgo.By("creating the workload, the check conditions should be added", func() {
				util.MustCreate(ctx, k8sClient, wl)

				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, &createdWl)).To(gomega.Succeed())
					g.Expect(slices.Map(createdWl.Status.AdmissionChecks, func(c *kueue.AdmissionCheckState) string {
						return string(c.Name)
					})).Should(gomega.ConsistOf("check1", "check2"))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("setting the check conditions", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, &createdWl)).To(gomega.Succeed())
					workload.SetAdmissionCheckState(&createdWl.Status.AdmissionChecks, kueue.AdmissionCheckState{
						Name:    "check1",
						State:   kueue.CheckStateReady,
						Message: "check successfully passed",
					}, realClock)
					workload.SetAdmissionCheckState(&createdWl.Status.AdmissionChecks, kueue.AdmissionCheckState{
						Name:    "check2",
						State:   kueue.CheckStateRetry,
						Message: "check rejected",
					}, realClock)
					g.Expect(k8sClient.Status().Update(ctx, &createdWl)).Should(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			// save check2 condition
			oldCheck2Cond := workload.FindAdmissionCheck(createdWl.Status.AdmissionChecks, "check2")
			gomega.Expect(oldCheck2Cond).NotTo(gomega.BeNil())

			ginkgo.By("updating the queue checks, the changes should propagate to the workload", func() {
				createdQueue := kueue.ClusterQueue{}
				queueKey := client.ObjectKeyFromObject(clusterQueue)
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, queueKey, &createdQueue)).To(gomega.Succeed())
					createdQueue.Spec.AdmissionChecks = []kueue.AdmissionCheckReference{"check2", "check3"}
					g.Expect(k8sClient.Update(ctx, &createdQueue)).Should(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())

				createdWl := kueue.Workload{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, &createdWl)).To(gomega.Succeed())
					g.Expect(slices.Map(createdWl.Status.AdmissionChecks, func(c *kueue.AdmissionCheckState) string {
						return string(c.Name)
					})).Should(gomega.ConsistOf("check2", "check3"))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())

				check2Cond := workload.FindAdmissionCheck(createdWl.Status.AdmissionChecks, "check2")
				gomega.Expect(check2Cond).To(gomega.Equal(oldCheck2Cond))
			})
		})
		ginkgo.It("should finish an unadmitted workload with failure when a check is rejected", func() {
			wl := testing.MakeWorkload("wl", ns.Name).Queue("queue").Obj()
			wlKey := client.ObjectKeyFromObject(wl)
			createdWl := kueue.Workload{}
			ginkgo.By("creating the workload, the check conditions should be added", func() {
				util.MustCreate(ctx, k8sClient, wl)

				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, &createdWl)).To(gomega.Succeed())
					g.Expect(slices.Map(createdWl.Status.AdmissionChecks, func(c *kueue.AdmissionCheckState) string {
						return string(c.Name)
					})).Should(gomega.ConsistOf("check1", "check2"))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("reserving quota for a Workload", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(util.SetQuotaReservation(ctx, k8sClient, &createdWl, testing.MakeAdmission(clusterQueue.Name).Obj())).To(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("setting the check conditions", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, &createdWl)).To(gomega.Succeed())
					workload.SetAdmissionCheckState(&createdWl.Status.AdmissionChecks, kueue.AdmissionCheckState{
						Name:    "check1",
						State:   kueue.CheckStateRejected,
						Message: "check rejected",
					}, realClock)
					g.Expect(k8sClient.Status().Update(ctx, &createdWl)).Should(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Checking if workload is deactivated, has Rejected status in the status.admissionCheck[*] field, an event is emitted and a metric is increased", func() {
				updatedWl := &kueue.Workload{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, updatedWl)).To(gomega.Succeed())
					g.Expect(workload.IsActive(updatedWl)).To(gomega.BeFalse())
					ok, err := testing.HasEventAppeared(ctx, k8sClient, corev1.Event{
						Reason:  "AdmissionCheckRejected",
						Type:    corev1.EventTypeWarning,
						Message: fmt.Sprintf("Deactivating workload because AdmissionCheck for %v was Rejected: %s", "check1", "check rejected"),
					})
					g.Expect(err).NotTo(gomega.HaveOccurred())
					g.Expect(ok).To(gomega.BeTrue())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())

				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, updatedWl)).To(gomega.Succeed())
					g.Expect(workload.IsEvictedByDeactivation(updatedWl)).To(gomega.BeTrue())
					util.ExpectEvictedWorkloadsTotalMetric(clusterQueue.Name, "DeactivatedDueToAdmissionCheck", 1)
					util.ExpectEvictedWorkloadsOnceTotalMetric(clusterQueue.Name, "DeactivatedDueToAdmissionCheck", "", 1)
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("should evict then finish with failure an admitted workload when a check is rejected", func() {
			wl := testing.MakeWorkload("wl", ns.Name).Queue("queue").Obj()
			wlKey := client.ObjectKeyFromObject(wl)
			createdWl := kueue.Workload{}
			ginkgo.By("creating the workload, the check conditions should be added", func() {
				util.MustCreate(ctx, k8sClient, wl)

				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, &createdWl)).To(gomega.Succeed())
					g.Expect(slices.Map(createdWl.Status.AdmissionChecks, func(c *kueue.AdmissionCheckState) string {
						return string(c.Name)
					})).Should(gomega.ConsistOf("check1", "check2"))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("setting quota reservation and the checks ready, should admit the workload", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, &createdWl)).To(gomega.Succeed())
					g.Expect(util.SetQuotaReservation(ctx, k8sClient, &createdWl, testing.MakeAdmission(clusterQueue.Name).Obj())).To(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())

				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, &createdWl)).To(gomega.Succeed())
					workload.SetAdmissionCheckState(&createdWl.Status.AdmissionChecks, kueue.AdmissionCheckState{
						Name:    "check1",
						State:   kueue.CheckStateReady,
						Message: "check ready",
					}, realClock)
					workload.SetAdmissionCheckState(&createdWl.Status.AdmissionChecks, kueue.AdmissionCheckState{
						Name:    "check2",
						State:   kueue.CheckStateReady,
						Message: "check ready",
					}, realClock)
					g.Expect(k8sClient.Status().Update(ctx, &createdWl)).Should(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())

				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, &createdWl)).To(gomega.Succeed())
					g.Expect(createdWl.Status.Conditions).To(gomega.ContainElement(gomega.BeComparableTo(metav1.Condition{
						Type:    kueue.WorkloadAdmitted,
						Status:  metav1.ConditionTrue,
						Reason:  "Admitted",
						Message: "The workload is admitted",
					}, util.IgnoreConditionTimestampsAndObservedGeneration)))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())

				util.ExpectAdmittedWorkloadsTotalMetric(clusterQueue, 1)
			})

			ginkgo.By("setting a rejected check condition", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, &createdWl)).To(gomega.Succeed())
					workload.SetAdmissionCheckState(&createdWl.Status.AdmissionChecks, kueue.AdmissionCheckState{
						Name:    "check1",
						State:   kueue.CheckStateRejected,
						Message: "check rejected",
					}, realClock)
					g.Expect(k8sClient.Status().Update(ctx, &createdWl)).To(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Checking if workload is deactivated, has Rejected status in the status.admissionCheck[*] field, an event is emitted and a metric is increased", func() {
				updatedWl := &kueue.Workload{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, updatedWl)).To(gomega.Succeed())
					g.Expect(workload.IsActive(updatedWl)).To(gomega.BeFalse())
					ok, err := testing.HasEventAppeared(ctx, k8sClient, corev1.Event{
						Reason:  "AdmissionCheckRejected",
						Type:    corev1.EventTypeWarning,
						Message: fmt.Sprintf("Deactivating workload because AdmissionCheck for %v was Rejected: %s", "check1", "check rejected"),
					})
					g.Expect(err).NotTo(gomega.HaveOccurred())
					g.Expect(ok).To(gomega.BeTrue())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())

				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, updatedWl)).To(gomega.Succeed())

					g.Expect(workload.IsEvictedByDeactivation(updatedWl)).To(gomega.BeTrue())
					util.ExpectEvictedWorkloadsTotalMetric(clusterQueue.Name, "DeactivatedDueToAdmissionCheck", 1)
					util.ExpectEvictedWorkloadsOnceTotalMetric(clusterQueue.Name, "DeactivatedDueToAdmissionCheck", "", 1)
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})
	})

	ginkgo.When("changing the priority value of PriorityClass doesn't affect the priority of the workload", func() {
		ginkgo.BeforeEach(func() {
			workloadPriorityClass = testing.MakeWorkloadPriorityClass("workload-priority-class").PriorityValue(200).Obj()
			util.MustCreate(ctx, k8sClient, workloadPriorityClass)
		})
		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			gomega.Expect(k8sClient.Delete(ctx, workloadPriorityClass)).To(gomega.Succeed())
		})
		ginkgo.It("case of WorkloadPriorityClass", func() {
			ginkgo.By("creating workload")
			wl = testing.MakeWorkload("wl", ns.Name).Queue("lq").Request(corev1.ResourceCPU, "1").
				PriorityClass("workload-priority-class").PriorityClassSource(constants.WorkloadPriorityClassSource).Priority(200).Obj()
			util.MustCreate(ctx, k8sClient, wl)
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &updatedQueueWorkload)).To(gomega.Succeed())
				g.Expect(updatedQueueWorkload.Status.Conditions).ShouldNot(gomega.BeNil())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
			initialPriority := int32(200)
			gomega.Expect(updatedQueueWorkload.Spec.Priority).To(gomega.Equal(&initialPriority))

			ginkgo.By("updating workloadPriorityClass")
			updatedPriority := int32(150)
			updatedWorkloadPriorityClass = workloadPriorityClass.DeepCopy()
			workloadPriorityClass.Value = updatedPriority
			gomega.Expect(k8sClient.Update(ctx, workloadPriorityClass)).To(gomega.Succeed())
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(workloadPriorityClass), updatedWorkloadPriorityClass)).To(gomega.Succeed())
				g.Expect(updatedWorkloadPriorityClass.Value).Should(gomega.Equal(updatedPriority))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
			gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(&updatedQueueWorkload), &finalQueueWorkload)).To(gomega.Succeed())
			gomega.Expect(finalQueueWorkload.Spec.Priority).To(gomega.Equal(&initialPriority))
		})
	})

	ginkgo.When("the workload has a maximum execution time set", func() {
		ginkgo.It("should deactivate the workload when the time expires", func() {
			// due time rounding in conditions, the workload will stay admitted
			// for a time between maxExecutionTime - 1s and maxExecutionTime
			maxExecTime := 2 * time.Second
			wl := testing.MakeWorkload("wl", ns.Name).
				Queue("lq").
				MaximumExecutionTimeSeconds(int32(maxExecTime.Seconds())).
				Obj()
			key := client.ObjectKeyFromObject(wl)
			ginkgo.By("creating the workload and reserving its quota", func() {
				util.MustCreate(ctx, k8sClient, wl)
				admission := testing.MakeAdmission("cq").Obj()
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, key, wl)).To(gomega.Succeed())
					g.Expect(util.SetQuotaReservation(ctx, k8sClient, wl, admission)).To(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
			ginkgo.By("waiting for the workload to be admitted", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, key, wl)).To(gomega.Succeed())
					g.Expect(workload.IsAdmitted(wl)).To(gomega.BeTrue())
					g.Expect(workload.IsActive(wl)).To(gomega.BeTrue())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
			ginkgo.By("waiting for the workload to be deactivated", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, key, wl)).To(gomega.Succeed())
					g.Expect(workload.IsActive(wl)).To(gomega.BeFalse())
					g.Expect(wl.Status.Conditions).To(gomega.ContainElement(gomega.BeComparableTo(
						metav1.Condition{
							Type:    kueue.WorkloadEvicted,
							Status:  metav1.ConditionTrue,
							Reason:  "DeactivatedDueToMaximumExecutionTimeExceeded",
							Message: "The workload is deactivated due to exceeding the maximum execution time",
						},
						util.IgnoreConditionTimestampsAndObservedGeneration,
					)))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})
		ginkgo.It("should deactivate the workload when the time expires with multiple admissions", func() {
			// due time rounding in conditions, the workload will stay admitted
			// for a time between maxExecutionTime - 1s and maxExecutionTime
			maxExecTime := 30 * time.Second
			wl := testing.MakeWorkload("wl", ns.Name).
				Queue("lq").
				MaximumExecutionTimeSeconds(int32(maxExecTime.Seconds())).
				Obj()
			key := client.ObjectKeyFromObject(wl)
			ginkgo.By("creating the workload and reserving its quota", func() {
				util.MustCreate(ctx, k8sClient, wl)
				admission := testing.MakeAdmission("cq").Obj()
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, key, wl)).To(gomega.Succeed())
					g.Expect(util.SetQuotaReservation(ctx, k8sClient, wl, admission)).To(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
			ginkgo.By("waiting for the workload to be admitted, and for the time to change the second", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, key, wl)).To(gomega.Succeed())
					g.Expect(workload.IsAdmitted(wl)).To(gomega.BeTrue())
					g.Expect(workload.IsActive(wl)).To(gomega.BeTrue())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())

				admittedCond := apimeta.FindStatusCondition(wl.Status.Conditions, kueue.WorkloadAdmitted)
				gomega.Expect(admittedCond).NotTo(gomega.BeNil())
				time.Sleep(time.Until(admittedCond.LastTransitionTime.Add(time.Second)))
			})

			ginkgo.By("evicting the workload, the accumulated admission time is updated", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, key, wl)).To(gomega.Succeed())
					workload.SetEvictedCondition(wl, "ByTest", "by test")
					g.Expect(workload.ApplyAdmissionStatus(ctx, k8sClient, wl, false, realClock)).To(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
				util.FinishEvictionForWorkloads(ctx, k8sClient, wl)

				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, key, wl)).To(gomega.Succeed())
					g.Expect(workload.IsAdmitted(wl)).To(gomega.BeFalse())
					g.Expect(workload.IsActive(wl)).To(gomega.BeTrue())
					g.Expect(ptr.Deref(wl.Status.AccumulatedPastExexcutionTimeSeconds, 0)).To(gomega.BeNumerically(">", int32(0)))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("setting the accumulated admission time closer to maximum", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, key, wl)).To(gomega.Succeed())
					wl.Status.AccumulatedPastExexcutionTimeSeconds = ptr.To(*wl.Spec.MaximumExecutionTimeSeconds - 1)
					g.Expect(k8sClient.Status().Update(ctx, wl)).To(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("reserving new quota", func() {
				admission := testing.MakeAdmission("cq").Obj()
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, key, wl)).To(gomega.Succeed())
					g.Expect(util.SetQuotaReservation(ctx, k8sClient, wl, admission)).To(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("waiting for the workload to be deactivated", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, key, wl)).To(gomega.Succeed())
					g.Expect(workload.IsActive(wl)).To(gomega.BeFalse())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})
	})
})

var _ = ginkgo.Describe("Workload controller with resource retention", ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	ginkgo.When("manager is setup with tiny retention period", func() {
		var (
			ns              *corev1.Namespace
			createdWorkload kueue.Workload
			localQueue      *kueue.LocalQueue
			clusterQueue    *kueue.ClusterQueue
			flavor          *kueue.ResourceFlavor
		)

		ginkgo.BeforeAll(func() {
			fwk.StartManager(
				ctx, cfg,
				managerAndControllerSetup(
					&config.Configuration{
						ObjectRetentionPolicies: &config.ObjectRetentionPolicies{
							Workloads: &config.WorkloadRetentionPolicy{
								AfterFinished: &metav1.Duration{
									Duration: util.TinyTimeout,
								},
							},
						},
					},
				),
			)

			gomega.Expect(features.SetEnable(features.ObjectRetentionPolicies, true)).To(gomega.Succeed())
		})

		ginkgo.AfterAll(func() {
			fwk.StopManager(ctx)
		})

		ginkgo.BeforeEach(func() {
			ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "core-workload-")
			flavor = testing.MakeResourceFlavor(flavorOnDemand).Obj()
			gomega.Expect(k8sClient.Create(ctx, flavor)).Should(gomega.Succeed())
			clusterQueue = testing.MakeClusterQueue("cq").
				ResourceGroup(*testing.MakeFlavorQuotas(flavorOnDemand).
					Resource(corev1.ResourceCPU, "1").Obj()).
				Obj()
			localQueue = testing.MakeLocalQueue("q", ns.Name).ClusterQueue("cq").Obj()
			gomega.Expect(k8sClient.Create(ctx, localQueue)).To(gomega.Succeed())
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			util.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, flavor, true)
		})

		ginkgo.It("should delete the workload after retention period elapses", func() {
			var (
				wl    *kueue.Workload
				wlKey client.ObjectKey
			)

			ginkgo.By("creating a workload", func() {
				wl = testing.MakeWorkload("wl-to-expire", ns.Name).Queue("q").Obj()
				wlKey = client.ObjectKeyFromObject(wl)
				gomega.Expect(k8sClient.Create(ctx, wl)).To(gomega.Succeed())
			})

			ginkgo.By("simulating workload admission", func() {
				admission := testing.MakeAdmission("cq").Obj()
				gomega.Expect(k8sClient.Get(ctx, wlKey, &createdWorkload)).To(gomega.Succeed())
				gomega.Expect(util.SetQuotaReservation(ctx, k8sClient, &createdWorkload, admission)).Should(gomega.Succeed())
				util.SyncAdmittedConditionForWorkloads(ctx, k8sClient, &createdWorkload)
			})

			ginkgo.By("marking workload as finished", func() {
				gomega.Expect(k8sClient.Get(ctx, wlKey, &createdWorkload)).To(gomega.Succeed())
				gomega.Eventually(func(g gomega.Gomega) {
					createdWorkload.Status.Conditions = append(createdWorkload.Status.Conditions, metav1.Condition{
						Type:               kueue.WorkloadFinished,
						Status:             metav1.ConditionTrue,
						LastTransitionTime: metav1.Now(),
						Reason:             "FinishedByTest",
						Message:            "Finished for testing purposes",
					})
					g.Expect(k8sClient.Status().Update(ctx, &createdWorkload)).To(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("workload should be deleted after the retention period", func() {
				gomega.Eventually(func() error {
					return k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &createdWorkload)
				}, util.Timeout, util.Interval).ShouldNot(gomega.Succeed())
			})
		})
	})

	ginkgo.When("manager is setup with long retention period", func() {
		var (
			ns              *corev1.Namespace
			createdWorkload kueue.Workload
			localQueue      *kueue.LocalQueue
			clusterQueue    *kueue.ClusterQueue
			flavor          *kueue.ResourceFlavor
		)

		ginkgo.BeforeAll(func() {
			fwk.StartManager(
				ctx, cfg,
				managerAndControllerSetup(
					&config.Configuration{
						ObjectRetentionPolicies: &config.ObjectRetentionPolicies{
							Workloads: &config.WorkloadRetentionPolicy{
								AfterFinished: &metav1.Duration{
									Duration: util.LongTimeout,
								},
							},
						},
					},
				),
			)

			gomega.Expect(features.SetEnable(features.ObjectRetentionPolicies, true)).To(gomega.Succeed())
		})

		ginkgo.AfterAll(func() {
			fwk.StopManager(ctx)
		})

		ginkgo.BeforeEach(func() {
			ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "core-workload-")
			flavor = testing.MakeResourceFlavor(flavorOnDemand).Obj()
			gomega.Expect(k8sClient.Create(ctx, flavor)).Should(gomega.Succeed())
			clusterQueue = testing.MakeClusterQueue("cq").
				ResourceGroup(*testing.MakeFlavorQuotas(flavorOnDemand).
					Resource(corev1.ResourceCPU, "1").Obj()).
				Obj()
			localQueue = testing.MakeLocalQueue("q", ns.Name).ClusterQueue("cq").Obj()
			gomega.Expect(k8sClient.Create(ctx, localQueue)).To(gomega.Succeed())
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			util.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, flavor, true)
		})

		ginkgo.It("should not delete the workload before retention period elapses", func() {
			var (
				wl    *kueue.Workload
				wlKey client.ObjectKey
			)

			ginkgo.By("creating a workload", func() {
				wl = testing.MakeWorkload("wl-to-stay", ns.Name).Queue("q").Obj()
				wlKey = client.ObjectKeyFromObject(wl)
				gomega.Expect(k8sClient.Create(ctx, wl)).To(gomega.Succeed())
			})

			ginkgo.By("simulating workload admission", func() {
				admission := testing.MakeAdmission("cq").Obj()
				gomega.Expect(k8sClient.Get(ctx, wlKey, &createdWorkload)).To(gomega.Succeed())
				gomega.Expect(util.SetQuotaReservation(ctx, k8sClient, &createdWorkload, admission)).Should(gomega.Succeed())
				util.SyncAdmittedConditionForWorkloads(ctx, k8sClient, &createdWorkload)
			})

			ginkgo.By("marking workload as finished", func() {
				gomega.Expect(k8sClient.Get(ctx, wlKey, &createdWorkload)).To(gomega.Succeed())
				gomega.Eventually(func(g gomega.Gomega) {
					createdWorkload.Status.Conditions = append(createdWorkload.Status.Conditions, metav1.Condition{
						Type:               kueue.WorkloadFinished,
						Status:             metav1.ConditionTrue,
						LastTransitionTime: metav1.Now(),
						Reason:             "FinishedByTest",
						Message:            "Finished for testing purposes",
					})
					g.Expect(k8sClient.Status().Update(ctx, &createdWorkload)).To(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("workload should not be deleted before the retention period", func() {
				gomega.Consistently(func() error {
					return k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &createdWorkload)
				}, util.ShortTimeout, util.Interval).Should(gomega.Succeed())
			})
		})
	})
})
