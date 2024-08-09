/*
Copyright 2022 The Kubernetes Authors.

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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/util/slices"
	"sigs.k8s.io/kueue/pkg/util/testing"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/test/integration/framework"
	"sigs.k8s.io/kueue/test/util"
)

// +kubebuilder:docs-gen:collapse=Imports

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
	)

	ginkgo.BeforeAll(func() {
		fwk = &framework.Framework{CRDPath: crdPath, WebhookPath: webhookPath}
		cfg = fwk.Init()
		ctx, k8sClient = fwk.RunManager(cfg, managerSetup)
	})
	ginkgo.AfterAll(func() {
		fwk.Teardown()
	})

	ginkgo.BeforeEach(func() {
		ns = &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: "core-workload-",
			},
		}
		gomega.Expect(k8sClient.Create(ctx, ns)).To(gomega.Succeed())
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
			gomega.Expect(k8sClient.Create(ctx, wl)).To(gomega.Succeed())
			gomega.Eventually(func() int {
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &updatedQueueWorkload)).To(gomega.Succeed())
				return len(updatedQueueWorkload.Status.Conditions)
			}, util.Timeout, util.Interval).Should(gomega.BeComparableTo(1))
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
			gomega.Expect(k8sClient.Create(ctx, wl)).To(gomega.Succeed())
			gomega.Eventually(func() int {
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &updatedQueueWorkload)).To(gomega.Succeed())
				return len(updatedQueueWorkload.Status.Conditions)
			}, util.Timeout, util.Interval).Should(gomega.BeComparableTo(1))
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
			gomega.Expect(k8sClient.Create(ctx, localQueue)).To(gomega.Succeed())
		})
		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		})
		ginkgo.It("Should update status when workloads are created", func() {
			wl = testing.MakeWorkload("three", ns.Name).Queue(localQueue.Name).Request(corev1.ResourceCPU, "1").Obj()
			message = fmt.Sprintf("ClusterQueue %s doesn't exist", "fooclusterqueue")
			gomega.Expect(k8sClient.Create(ctx, wl)).To(gomega.Succeed())
			gomega.Eventually(func() []metav1.Condition {
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &updatedQueueWorkload)).To(gomega.Succeed())
				return updatedQueueWorkload.Status.Conditions
			}, util.Timeout, util.Interval).ShouldNot(gomega.BeNil())
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
			gomega.Expect(k8sClient.Create(ctx, flavor)).Should(gomega.Succeed())
			clusterQueue = testing.MakeClusterQueue("cluster-queue").
				ResourceGroup(*testing.MakeFlavorQuotas(flavorOnDemand).
					Resource(resourceGPU, "5", "5").Obj()).
				Cohort("cohort").
				Obj()
			gomega.Expect(k8sClient.Create(ctx, clusterQueue)).To(gomega.Succeed())
			localQueue = testing.MakeLocalQueue("queue", ns.Name).ClusterQueue(clusterQueue.Name).Obj()
			gomega.Expect(k8sClient.Create(ctx, localQueue)).To(gomega.Succeed())
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
			gomega.Expect(k8sClient.Create(ctx, flavor)).Should(gomega.Succeed())

			check1 = testing.MakeAdmissionCheck("check1").ControllerName("ctrl").Obj()
			gomega.Expect(k8sClient.Create(ctx, check1)).Should(gomega.Succeed())
			util.SetAdmissionCheckActive(ctx, k8sClient, check1, metav1.ConditionTrue)

			check2 = testing.MakeAdmissionCheck("check2").ControllerName("ctrl").Obj()
			gomega.Expect(k8sClient.Create(ctx, check2)).Should(gomega.Succeed())
			util.SetAdmissionCheckActive(ctx, k8sClient, check2, metav1.ConditionTrue)

			clusterQueue = testing.MakeClusterQueue("cluster-queue").
				ResourceGroup(*testing.MakeFlavorQuotas(flavorOnDemand).
					Resource(resourceGPU, "5", "5").Obj()).
				Cohort("cohort").
				AdmissionChecks("check1", "check2").
				Obj()
			gomega.Expect(k8sClient.Create(ctx, clusterQueue)).To(gomega.Succeed())
			localQueue = testing.MakeLocalQueue("queue", ns.Name).ClusterQueue(clusterQueue.Name).Obj()
			gomega.Expect(k8sClient.Create(ctx, localQueue)).To(gomega.Succeed())
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
				gomega.Expect(k8sClient.Create(ctx, wl)).To(gomega.Succeed())

				gomega.Eventually(func() []string {
					gomega.Expect(k8sClient.Get(ctx, wlKey, &createdWl)).To(gomega.Succeed())
					return slices.Map(createdWl.Status.AdmissionChecks, func(c *kueue.AdmissionCheckState) string { return c.Name })
				}, util.Timeout, util.Interval).Should(gomega.ConsistOf("check1", "check2"))
			})

			ginkgo.By("setting the check conditions", func() {
				gomega.Eventually(func() error {
					gomega.Expect(k8sClient.Get(ctx, wlKey, &createdWl)).To(gomega.Succeed())
					workload.SetAdmissionCheckState(&createdWl.Status.AdmissionChecks, kueue.AdmissionCheckState{
						Name:    "check1",
						State:   kueue.CheckStateReady,
						Message: "check successfully passed",
					})
					workload.SetAdmissionCheckState(&createdWl.Status.AdmissionChecks, kueue.AdmissionCheckState{
						Name:    "check2",
						State:   kueue.CheckStateRetry,
						Message: "check rejected",
					})
					return k8sClient.Status().Update(ctx, &createdWl)
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			// save check2 condition
			oldCheck2Cond := workload.FindAdmissionCheck(createdWl.Status.AdmissionChecks, "check2")
			gomega.Expect(oldCheck2Cond).NotTo(gomega.BeNil())

			ginkgo.By("updating the queue checks, the changes should propagate to the workload", func() {
				createdQueue := kueue.ClusterQueue{}
				queueKey := client.ObjectKeyFromObject(clusterQueue)
				gomega.Eventually(func() error {
					gomega.Expect(k8sClient.Get(ctx, queueKey, &createdQueue)).To(gomega.Succeed())
					createdQueue.Spec.AdmissionChecks = []string{"check2", "check3"}
					return k8sClient.Update(ctx, &createdQueue)
				}, util.Timeout, util.Interval).Should(gomega.Succeed())

				createdWl := kueue.Workload{}
				gomega.Eventually(func() []string {
					gomega.Expect(k8sClient.Get(ctx, wlKey, &createdWl)).To(gomega.Succeed())
					return slices.Map(createdWl.Status.AdmissionChecks, func(c *kueue.AdmissionCheckState) string { return c.Name })
				}, util.Timeout, util.Interval).Should(gomega.ConsistOf("check2", "check3"))

				check2Cond := workload.FindAdmissionCheck(createdWl.Status.AdmissionChecks, "check2")
				gomega.Expect(check2Cond).To(gomega.Equal(oldCheck2Cond))
			})
		})
		ginkgo.It("should finish an unadmitted workload with failure when a check is rejected", func() {
			wl := testing.MakeWorkload("wl", ns.Name).Queue("queue").Obj()
			wlKey := client.ObjectKeyFromObject(wl)
			createdWl := kueue.Workload{}
			ginkgo.By("creating the workload, the check conditions should be added", func() {
				gomega.Expect(k8sClient.Create(ctx, wl)).To(gomega.Succeed())

				gomega.Eventually(func() []string {
					gomega.Expect(k8sClient.Get(ctx, wlKey, &createdWl)).To(gomega.Succeed())
					return slices.Map(createdWl.Status.AdmissionChecks, func(c *kueue.AdmissionCheckState) string { return c.Name })
				}, util.Timeout, util.Interval).Should(gomega.ConsistOf("check1", "check2"))
			})

			ginkgo.By("reserving quota for a Workload", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(util.SetQuotaReservation(ctx, k8sClient, &createdWl, testing.MakeAdmission(clusterQueue.Name).Obj())).To(gomega.Succeed())
				}).Should(gomega.Succeed())
			})

			ginkgo.By("setting the check conditions", func() {
				gomega.Eventually(func() error {
					gomega.Expect(k8sClient.Get(ctx, wlKey, &createdWl)).To(gomega.Succeed())
					workload.SetAdmissionCheckState(&createdWl.Status.AdmissionChecks, kueue.AdmissionCheckState{
						Name:    "check1",
						State:   kueue.CheckStateRejected,
						Message: "check rejected",
					})
					return k8sClient.Status().Update(ctx, &createdWl)
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
					util.ExpectEvictedWorkloadsTotalMetric(clusterQueue.Name, kueue.WorkloadEvictedByDeactivation, 1)
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("should evict then finish with failure an admitted workload when a check is rejected", func() {
			wl := testing.MakeWorkload("wl", ns.Name).Queue("queue").Obj()
			wlKey := client.ObjectKeyFromObject(wl)
			createdWl := kueue.Workload{}
			ginkgo.By("creating the workload, the check conditions should be added", func() {
				gomega.Expect(k8sClient.Create(ctx, wl)).To(gomega.Succeed())

				gomega.Eventually(func() []string {
					gomega.Expect(k8sClient.Get(ctx, wlKey, &createdWl)).To(gomega.Succeed())
					return slices.Map(createdWl.Status.AdmissionChecks, func(c *kueue.AdmissionCheckState) string { return c.Name })
				}, util.Timeout, util.Interval).Should(gomega.ConsistOf("check1", "check2"))
			})

			ginkgo.By("setting quota reservation and the checks ready, should admit the workload", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, &createdWl)).To(gomega.Succeed())
					g.Expect(util.SetQuotaReservation(ctx, k8sClient, &createdWl, testing.MakeAdmission(clusterQueue.Name).Obj())).To(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())

				gomega.Eventually(func() error {
					gomega.Expect(k8sClient.Get(ctx, wlKey, &createdWl)).To(gomega.Succeed())
					workload.SetAdmissionCheckState(&createdWl.Status.AdmissionChecks, kueue.AdmissionCheckState{
						Name:    "check1",
						State:   kueue.CheckStateReady,
						Message: "check ready",
					})
					workload.SetAdmissionCheckState(&createdWl.Status.AdmissionChecks, kueue.AdmissionCheckState{
						Name:    "check2",
						State:   kueue.CheckStateReady,
						Message: "check ready",
					})
					return k8sClient.Status().Update(ctx, &createdWl)
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
					})
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
					util.ExpectEvictedWorkloadsTotalMetric(clusterQueue.Name, kueue.WorkloadEvictedByDeactivation, 1)
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})
	})

	ginkgo.When("changing the priority value of PriorityClass doesn't affect the priority of the workload", func() {
		ginkgo.BeforeEach(func() {
			workloadPriorityClass = testing.MakeWorkloadPriorityClass("workload-priority-class").PriorityValue(200).Obj()
			gomega.Expect(k8sClient.Create(ctx, workloadPriorityClass)).To(gomega.Succeed())
		})
		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			gomega.Expect(k8sClient.Delete(ctx, workloadPriorityClass)).To(gomega.Succeed())
		})
		ginkgo.It("case of WorkloadPriorityClass", func() {
			ginkgo.By("creating workload")
			wl = testing.MakeWorkload("wl", ns.Name).Queue("lq").Request(corev1.ResourceCPU, "1").
				PriorityClass("workload-priority-class").PriorityClassSource(constants.WorkloadPriorityClassSource).Priority(200).Obj()
			gomega.Expect(k8sClient.Create(ctx, wl)).To(gomega.Succeed())
			gomega.Eventually(func() []metav1.Condition {
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &updatedQueueWorkload)).To(gomega.Succeed())
				return updatedQueueWorkload.Status.Conditions
			}, util.Timeout, util.Interval).ShouldNot(gomega.BeNil())
			initialPriority := int32(200)
			gomega.Expect(updatedQueueWorkload.Spec.Priority).To(gomega.Equal(&initialPriority))

			ginkgo.By("updating workloadPriorityClass")
			updatedPriority := int32(150)
			updatedWorkloadPriorityClass = workloadPriorityClass.DeepCopy()
			workloadPriorityClass.Value = updatedPriority
			gomega.Expect(k8sClient.Update(ctx, workloadPriorityClass)).To(gomega.Succeed())
			gomega.Eventually(func() int32 {
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(workloadPriorityClass), updatedWorkloadPriorityClass)).To(gomega.Succeed())
				return updatedWorkloadPriorityClass.Value
			}, util.Timeout, util.Interval).Should(gomega.Equal(updatedPriority))
			gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(&updatedQueueWorkload), &finalQueueWorkload)).To(gomega.Succeed())
			gomega.Expect(finalQueueWorkload.Spec.Priority).To(gomega.Equal(&initialPriority))
		})
	})
})

var _ = ginkgo.Describe("Workload controller with resource retention", ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	var (
		ns              *corev1.Namespace
		createdWorkload kueue.Workload
		localQueue      *kueue.LocalQueue
		clusterQueue    *kueue.ClusterQueue
		flavor          *kueue.ResourceFlavor
	)

	ginkgo.BeforeAll(func() {
		fwk = &framework.Framework{CRDPath: crdPath, WebhookPath: webhookPath}
		cfg = fwk.Init()
		ctx, k8sClient = fwk.RunManager(cfg, managerSetupWithWorkloadRetentionEnabled)
	})
	ginkgo.AfterAll(func() {
		fwk.Teardown()
	})

	ginkgo.BeforeEach(func() {
		ns = &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: "core-workload-",
			},
		}
		gomega.Expect(k8sClient.Create(ctx, ns)).To(gomega.Succeed())
	})

	ginkgo.AfterEach(func() {
		createdWorkload = kueue.Workload{}
		clusterQueue = nil
		localQueue = nil
		flavor = nil
	})

	ginkgo.When("a workload with retention period", func() {
		ginkgo.BeforeEach(func() {
			flavor = testing.MakeResourceFlavor(flavorOnDemand).Obj()
			gomega.Expect(k8sClient.Create(ctx, flavor)).Should(gomega.Succeed())
			clusterQueue = testing.MakeClusterQueue("cq").
				ResourceGroup(*testing.MakeFlavorQuotas(flavorOnDemand).
					Resource(corev1.ResourceCPU, "1").Obj()).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, clusterQueue)).To(gomega.Succeed())
			localQueue = testing.MakeLocalQueue("q", ns.Name).ClusterQueue("cq").Obj()
			gomega.Expect(k8sClient.Create(ctx, localQueue)).To(gomega.Succeed())
		})
		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		})

		ginkgo.It("should delete the workload after retention period elapses", func() {
			// Create workload
			wl := testing.MakeWorkload("wl-to-expire", ns.Name).Queue("q").Obj()
			wlKey := client.ObjectKeyFromObject(wl)
			gomega.Expect(k8sClient.Create(ctx, wl)).To(gomega.Succeed())

			// Simulate admission
			admission := testing.MakeAdmission("cq").Obj()
			gomega.Expect(k8sClient.Get(ctx, wlKey, &createdWorkload)).To(gomega.Succeed())
			gomega.Expect(util.SetQuotaReservation(ctx, k8sClient, &createdWorkload, admission)).Should(gomega.Succeed())
			util.SyncAdmittedConditionForWorkloads(ctx, k8sClient, &createdWorkload)

			// Mark as Finished
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

			ginkgo.By("workload should not be deleted before retention period")
			gomega.Consistently(func() error {
				return k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &createdWorkload)
			}, 2*time.Second, time.Second).Should(gomega.Succeed())

			ginkgo.By("workload should be deleted after the retention period")
			gomega.Eventually(func() error {
				return k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &createdWorkload)
			}, 2*time.Second, time.Second).ShouldNot(gomega.Succeed())
		})
	})
})
