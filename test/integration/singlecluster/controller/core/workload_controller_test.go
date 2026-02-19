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
	nodev1 "k8s.io/api/node/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	config "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/util/admissioncheck"
	"sigs.k8s.io/kueue/pkg/util/slices"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/test/integration/framework"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("Workload controller", ginkgo.Label("controller:workload", "area:core"), ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
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
			wl = utiltestingapi.MakeWorkload("one", ns.Name).Request(corev1.ResourceCPU, "1").Obj()
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
			wl = utiltestingapi.MakeWorkload("two", ns.Name).Queue("non-created-queue").Request(corev1.ResourceCPU, "1").Obj()
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
			localQueue = utiltestingapi.MakeLocalQueue("queue", ns.Name).ClusterQueue("fooclusterqueue").Obj()
			util.MustCreate(ctx, k8sClient, localQueue)
		})
		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		})
		ginkgo.It("Should update status when workloads are created", func() {
			wl = utiltestingapi.MakeWorkload("three", ns.Name).Queue(kueue.LocalQueueName(localQueue.Name)).Request(corev1.ResourceCPU, "1").Obj()
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

	ginkgo.When("a workload is manually deactivated", func() {
		var flavor *kueue.ResourceFlavor

		ginkgo.BeforeEach(func() {
			flavor = utiltestingapi.MakeResourceFlavor(flavorOnDemand).Obj()
			util.MustCreate(ctx, k8sClient, flavor)
			clusterQueue = utiltestingapi.MakeClusterQueue("cluster-queue").
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas(flavorOnDemand).Resource(corev1.ResourceCPU, "5", "5").Obj()).
				Cohort("cohort").
				Obj()
			util.MustCreate(ctx, k8sClient, clusterQueue)
			localQueue = utiltestingapi.MakeLocalQueue("queue", ns.Name).ClusterQueue(clusterQueue.Name).Obj()
			util.MustCreate(ctx, k8sClient, localQueue)
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			util.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, flavor, true)
		})

		ginkgo.It("should clear workload requeue state after deactivation", func() {
			wl = utiltestingapi.MakeWorkload("reset-requeue", ns.Name).
				Queue(kueue.LocalQueueName(localQueue.Name)).
				Request(corev1.ResourceCPU, "1").
				Obj()

			util.MustCreate(ctx, k8sClient, wl)

			wlKey := client.ObjectKeyFromObject(wl)
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlKey, &updatedQueueWorkload)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			ginkgo.By("manually setting RequeueState to simulate backoff", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, &updatedQueueWorkload)).To(gomega.Succeed())
					requeueAt := metav1.NewTime(time.Now().Add(30 * time.Second))
					updatedQueueWorkload.Status.RequeueState = &kueue.RequeueState{
						Count:     ptr.To(int32(2)),
						RequeueAt: &requeueAt,
					}
					g.Expect(k8sClient.Status().Update(ctx, &updatedQueueWorkload)).To(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())

				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, &updatedQueueWorkload)).To(gomega.Succeed())
					g.Expect(updatedQueueWorkload.Status.RequeueState).NotTo(gomega.BeNil())
					g.Expect(updatedQueueWorkload.Status.RequeueState.Count).NotTo(gomega.BeNil())
					g.Expect(updatedQueueWorkload.Status.RequeueState.RequeueAt).NotTo(gomega.BeNil())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("deactivating the workload", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, &updatedQueueWorkload)).To(gomega.Succeed())
					updatedQueueWorkload.Spec.Active = ptr.To(false)
					g.Expect(k8sClient.Update(ctx, &updatedQueueWorkload)).To(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("verifying RequeueState is cleared by the controller", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, &updatedQueueWorkload)).To(gomega.Succeed())
					g.Expect(updatedQueueWorkload.Status.RequeueState).To(gomega.BeNil())
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})
		})
	})

	ginkgo.When("the workload is admitted", func() {
		var flavor *kueue.ResourceFlavor

		ginkgo.BeforeEach(func() {
			flavor = utiltestingapi.MakeResourceFlavor(flavorOnDemand).Obj()
			util.MustCreate(ctx, k8sClient, flavor)
			clusterQueue = utiltestingapi.MakeClusterQueue("cluster-queue").
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas(flavorOnDemand).
					Resource(resourceGPU, "5", "5").Obj()).
				Cohort("cohort").
				Obj()
			util.MustCreate(ctx, k8sClient, clusterQueue)
			localQueue = utiltestingapi.MakeLocalQueue("queue", ns.Name).ClusterQueue(clusterQueue.Name).Obj()
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
			flavor = utiltestingapi.MakeResourceFlavor(flavorOnDemand).Obj()
			util.MustCreate(ctx, k8sClient, flavor)

			check1 = utiltestingapi.MakeAdmissionCheck("check1").ControllerName("ctrl").Obj()
			util.MustCreate(ctx, k8sClient, check1)
			util.SetAdmissionCheckActive(ctx, k8sClient, check1, metav1.ConditionTrue)

			check2 = utiltestingapi.MakeAdmissionCheck("check2").ControllerName("ctrl").Obj()
			util.MustCreate(ctx, k8sClient, check2)
			util.SetAdmissionCheckActive(ctx, k8sClient, check2, metav1.ConditionTrue)

			clusterQueue = utiltestingapi.MakeClusterQueue("cluster-queue").
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas(flavorOnDemand).
					Resource(resourceGPU, "5", "5").Obj()).
				Cohort("cohort").
				AdmissionChecks("check1", "check2").
				Obj()
			util.MustCreate(ctx, k8sClient, clusterQueue)
			localQueue = utiltestingapi.MakeLocalQueue("queue", ns.Name).ClusterQueue(clusterQueue.Name).Obj()
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
			wl := utiltestingapi.MakeWorkload("wl", ns.Name).Queue("queue").Obj()
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
					}, util.RealClock)
					workload.SetAdmissionCheckState(&createdWl.Status.AdmissionChecks, kueue.AdmissionCheckState{
						Name:    "check2",
						State:   kueue.CheckStateRetry,
						Message: "check rejected",
					}, util.RealClock)
					g.Expect(k8sClient.Status().Update(ctx, &createdWl)).Should(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			// save check2 condition
			oldCheck2Cond := admissioncheck.FindAdmissionCheck(createdWl.Status.AdmissionChecks, "check2")
			gomega.Expect(oldCheck2Cond).NotTo(gomega.BeNil())

			ginkgo.By("updating the queue checks, the changes should propagate to the workload", func() {
				createdQueue := kueue.ClusterQueue{}
				queueKey := client.ObjectKeyFromObject(clusterQueue)
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, queueKey, &createdQueue)).To(gomega.Succeed())
					createdQueue.Spec.AdmissionChecksStrategy = &kueue.AdmissionChecksStrategy{
						AdmissionChecks: []kueue.AdmissionCheckStrategyRule{
							{Name: "check2"},
							{Name: "check3"},
						},
					}
					g.Expect(k8sClient.Update(ctx, &createdQueue)).Should(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())

				createdWl := kueue.Workload{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, &createdWl)).To(gomega.Succeed())
					g.Expect(slices.Map(createdWl.Status.AdmissionChecks, func(c *kueue.AdmissionCheckState) string {
						return string(c.Name)
					})).Should(gomega.ConsistOf("check2", "check3"))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())

				check2Cond := admissioncheck.FindAdmissionCheck(createdWl.Status.AdmissionChecks, "check2")
				gomega.Expect(check2Cond).To(gomega.Equal(oldCheck2Cond))
			})
		})
		ginkgo.It("should finish an unadmitted workload with failure when a check is rejected", func() {
			wl := utiltestingapi.MakeWorkload("wl", ns.Name).Queue("queue").Obj()
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
				util.SetQuotaReservation(ctx, k8sClient, wlKey, utiltestingapi.MakeAdmission(clusterQueue.Name).Obj())
			})

			ginkgo.By("setting the check conditions", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, &createdWl)).To(gomega.Succeed())
					workload.SetAdmissionCheckState(&createdWl.Status.AdmissionChecks, kueue.AdmissionCheckState{
						Name:    "check1",
						State:   kueue.CheckStateRejected,
						Message: "check rejected",
					}, util.RealClock)
					g.Expect(k8sClient.Status().Update(ctx, &createdWl)).Should(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Checking if workload is deactivated, has Rejected status in the status.admissionCheck[*] field, an event is emitted and a metric is increased", func() {
				updatedWl := &kueue.Workload{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, updatedWl)).To(gomega.Succeed())
					g.Expect(workload.IsActive(updatedWl)).To(gomega.BeFalse())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
				util.ExpectEventAppeared(ctx, k8sClient, corev1.Event{
					Reason:  "AdmissionCheckRejected",
					Type:    corev1.EventTypeWarning,
					Message: fmt.Sprintf("Deactivating workload because AdmissionCheck for %v was Rejected: %s", "check1", "check rejected"),
				})

				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, updatedWl)).To(gomega.Succeed())
					g.Expect(workload.IsEvictedByDeactivation(updatedWl)).To(gomega.BeTrue())
					util.ExpectEvictedWorkloadsTotalMetric(clusterQueue.Name, "Deactivated", "AdmissionCheck", "", 1)
					util.ExpectEvictedWorkloadsOnceTotalMetric(clusterQueue.Name, "Deactivated", "AdmissionCheck", "", 1)
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("should evict then finish with failure an admitted workload when a check is rejected", func() {
			wl := utiltestingapi.MakeWorkload("wl", ns.Name).Queue("queue").Obj()
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
				util.SetQuotaReservation(ctx, k8sClient, wlKey, utiltestingapi.MakeAdmission(clusterQueue.Name).Obj())

				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, &createdWl)).To(gomega.Succeed())
					workload.SetAdmissionCheckState(&createdWl.Status.AdmissionChecks, kueue.AdmissionCheckState{
						Name:    "check1",
						State:   kueue.CheckStateReady,
						Message: "check ready",
					}, util.RealClock)
					workload.SetAdmissionCheckState(&createdWl.Status.AdmissionChecks, kueue.AdmissionCheckState{
						Name:    "check2",
						State:   kueue.CheckStateReady,
						Message: "check ready",
					}, util.RealClock)
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

				util.ExpectAdmittedWorkloadsTotalMetric(clusterQueue, "", 1)
			})

			ginkgo.By("setting a rejected check condition", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, &createdWl)).To(gomega.Succeed())
					workload.SetAdmissionCheckState(&createdWl.Status.AdmissionChecks, kueue.AdmissionCheckState{
						Name:    "check1",
						State:   kueue.CheckStateRejected,
						Message: "check rejected",
					}, util.RealClock)
					g.Expect(k8sClient.Status().Update(ctx, &createdWl)).To(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Checking if workload is deactivated, has Rejected status in the status.admissionCheck[*] field, an event is emitted and a metric is increased", func() {
				updatedWl := &kueue.Workload{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, updatedWl)).To(gomega.Succeed())
					g.Expect(workload.IsActive(updatedWl)).To(gomega.BeFalse())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
				util.ExpectEventAppeared(ctx, k8sClient, corev1.Event{
					Reason:  "AdmissionCheckRejected",
					Type:    corev1.EventTypeWarning,
					Message: fmt.Sprintf("Deactivating workload because AdmissionCheck for %v was Rejected: %s", "check1", "check rejected"),
				})

				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, updatedWl)).To(gomega.Succeed())

					g.Expect(workload.IsEvictedByDeactivation(updatedWl)).To(gomega.BeTrue())
					util.ExpectEvictedWorkloadsTotalMetric(clusterQueue.Name, "Deactivated", "AdmissionCheck", "", 1)
					util.ExpectEvictedWorkloadsOnceTotalMetric(clusterQueue.Name, "Deactivated", "AdmissionCheck", "", 1)
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})
	})

	ginkgo.When("changing the priority value of PriorityClass changes the priority of the workload", func() {
		ginkgo.BeforeEach(func() {
			workloadPriorityClass = utiltestingapi.MakeWorkloadPriorityClass("workload-priority-class").PriorityValue(200).Obj()
			util.MustCreate(ctx, k8sClient, workloadPriorityClass)
		})
		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			gomega.Expect(k8sClient.Delete(ctx, workloadPriorityClass)).To(gomega.Succeed())
		})
		ginkgo.It("case of WorkloadPriorityClass", func() {
			ginkgo.By("creating workload")
			wl = utiltestingapi.MakeWorkload("wl", ns.Name).Queue("lq").Request(corev1.ResourceCPU, "1").
				WorkloadPriorityClassRef("workload-priority-class").
				Priority(200).
				Obj()
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
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(&updatedQueueWorkload), &finalQueueWorkload)).To(gomega.Succeed())
				g.Expect(finalQueueWorkload.Spec.Priority).To(gomega.Equal(&updatedPriority))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})
	})

	ginkgo.When("the workload has a maximum execution time set", func() {
		ginkgo.It("should deactivate the workload when the time expires", framework.SlowSpec, func() {
			// due time rounding in conditions, the workload will stay admitted
			// for a time between maxExecutionTime - 1s and maxExecutionTime
			maxExecTime := 2 * time.Second
			wl := utiltestingapi.MakeWorkload("wl", ns.Name).
				Queue("lq").
				MaximumExecutionTimeSeconds(int32(maxExecTime.Seconds())).
				Obj()
			key := client.ObjectKeyFromObject(wl)
			ginkgo.By("creating the workload and reserving its quota", func() {
				util.MustCreate(ctx, k8sClient, wl)
				admission := utiltestingapi.MakeAdmission("cq").Obj()
				util.SetQuotaReservation(ctx, k8sClient, key, admission)
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
		ginkgo.It("should deactivate the workload when the time expires with multiple admissions", framework.SlowSpec, func() {
			// due time rounding in conditions, the workload will stay admitted
			// for a time between maxExecutionTime - 1s and maxExecutionTime
			maxExecTime := 30 * time.Second
			wl := utiltestingapi.MakeWorkload("wl", ns.Name).
				Queue("lq").
				MaximumExecutionTimeSeconds(int32(maxExecTime.Seconds())).
				Obj()
			key := client.ObjectKeyFromObject(wl)
			ginkgo.By("creating the workload and reserving its quota", func() {
				util.MustCreate(ctx, k8sClient, wl)
				admission := utiltestingapi.MakeAdmission("cq").Obj()
				util.SetQuotaReservation(ctx, k8sClient, key, admission)
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
					g.Expect(workload.PatchAdmissionStatus(ctx, k8sClient, wl, util.RealClock, func(wl *kueue.Workload) (bool, error) {
						return workload.SetEvictedCondition(wl, util.RealClock.Now(), "ByTest", "by test"), nil
					})).Should(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
				util.FinishEvictionForWorkloads(ctx, k8sClient, wl)

				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, key, wl)).To(gomega.Succeed())
					g.Expect(workload.IsAdmitted(wl)).To(gomega.BeFalse())
					g.Expect(workload.IsActive(wl)).To(gomega.BeTrue())
					g.Expect(ptr.Deref(wl.Status.AccumulatedPastExecutionTimeSeconds, 0)).To(gomega.BeNumerically(">", int32(0)))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("setting the accumulated admission time closer to maximum", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, key, wl)).To(gomega.Succeed())
					wl.Status.AccumulatedPastExecutionTimeSeconds = ptr.To(*wl.Spec.MaximumExecutionTimeSeconds - 1)
					g.Expect(k8sClient.Status().Update(ctx, wl)).To(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("reserving new quota", func() {
				admission := utiltestingapi.MakeAdmission("cq").Obj()
				util.SetQuotaReservation(ctx, k8sClient, key, admission)
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

var _ = ginkgo.Describe("Workload controller interaction with scheduler", ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	var (
		ns           *corev1.Namespace
		clusterQueue *kueue.ClusterQueue
		localQueue   *kueue.LocalQueue
		wl           *kueue.Workload
	)

	startManager := func() {
		fwk.StartManager(ctx, cfg, managerAndSchedulerSetup)
	}

	stopManager := func() {
		fwk.StopManager(ctx)
	}

	restartManager := func() {
		stopManager()
		startManager()
	}

	ginkgo.BeforeAll(func() {
		startManager()
	})

	ginkgo.AfterAll(func() {
		stopManager()
	})

	ginkgo.When("workload's runtime class is changed", func() {
		var flavor *kueue.ResourceFlavor
		var runtimeClass *nodev1.RuntimeClass

		const runtimeClassName = "test-kueue-class"

		ginkgo.BeforeEach(func() {
			ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "core-workload-")
			flavor = utiltestingapi.MakeResourceFlavor(flavorOnDemand).Obj()
			util.MustCreate(ctx, k8sClient, flavor)
			clusterQueue = utiltestingapi.MakeClusterQueue("cluster-queue").
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas(flavorOnDemand).Resource(corev1.ResourceCPU, "5", "5").Obj()).
				Cohort("cohort").
				Obj()
			util.MustCreate(ctx, k8sClient, clusterQueue)
			localQueue = utiltestingapi.MakeLocalQueue("queue", ns.Name).ClusterQueue(clusterQueue.Name).Obj()
			util.MustCreate(ctx, k8sClient, localQueue)
			runtimeClass = utiltesting.MakeRuntimeClass(runtimeClassName, "rc-handler-1").Obj()
			util.MustCreate(ctx, k8sClient, runtimeClass)
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			util.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, flavor, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, runtimeClass, true)
		})

		ginkgo.It("should not temporarily admit an inactive workload after changing the runtime class", framework.SlowSpec, func() {
			ginkgo.By("creating an inactive workload", func() {
				wl = utiltestingapi.MakeWorkload("wl1", ns.Name).
					Queue(kueue.LocalQueueName(localQueue.Name)).
					Request(corev1.ResourceCPU, "1").
					RuntimeClass(runtimeClassName).
					Active(false).
					Obj()
				util.MustCreate(ctx, k8sClient, wl)

				wlKey := client.ObjectKeyFromObject(wl)
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, wl)).To(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("changing the runtime class", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					runtimeClassKey := client.ObjectKeyFromObject(runtimeClass)
					g.Expect(k8sClient.Get(ctx, runtimeClassKey, runtimeClass)).To(gomega.Succeed())
					if runtimeClass.ObjectMeta.Annotations == nil {
						runtimeClass.ObjectMeta.Annotations = map[string]string{}
					}
					runtimeClass.ObjectMeta.Annotations["foo"] = "bar"
					g.Expect(k8sClient.Update(ctx, runtimeClass)).To(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("checking no 'quota reserved' event appearing for the workload", func() {
				gomega.Consistently(func(g gomega.Gomega) {
					found, err := utiltesting.HasMatchingEventAppeared(ctx, k8sClient, func(e *corev1.Event) bool {
						return e.Reason == "QuotaReserved" &&
							e.Type == corev1.EventTypeNormal &&
							e.InvolvedObject.Kind == "Workload" &&
							e.InvolvedObject.Name == wl.Name &&
							e.InvolvedObject.Namespace == wl.Namespace
					})
					g.Expect(err).NotTo(gomega.HaveOccurred())
					g.Expect(found).To(gomega.BeTrue())
				}, util.ConsistentDuration, util.ShortInterval).ShouldNot(gomega.Succeed())
			})
		})

		ginkgo.It("should not temporarily admit a finished workload after changing the runtime class", framework.SlowSpec, func() {
			wl = utiltestingapi.MakeWorkload("wl1", ns.Name).
				Queue(kueue.LocalQueueName(localQueue.Name)).
				Request(corev1.ResourceCPU, "1").
				RuntimeClass(runtimeClassName).
				Obj()
			util.MustCreate(ctx, k8sClient, wl)

			wlKey := client.ObjectKeyFromObject(wl)

			ginkgo.By("creating a workload", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, wl)).To(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("waiting for admission", func() {
				util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl)
			})

			ginkgo.By("finishing the workload", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, wl)).To(gomega.Succeed())
					g.Expect(workload.Finish(ctx, k8sClient, wl, "ByTest", "By test", util.RealClock, nil)).To(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("waiting for finish", func() {
				util.ExpectWorkloadToFinish(ctx, k8sClient, wlKey)
			})

			ginkgo.By("changing the runtime class", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					runtimeClassKey := client.ObjectKeyFromObject(runtimeClass)
					g.Expect(k8sClient.Get(ctx, runtimeClassKey, runtimeClass)).To(gomega.Succeed())
					if runtimeClass.ObjectMeta.Annotations == nil {
						runtimeClass.ObjectMeta.Annotations = map[string]string{}
					}
					runtimeClass.ObjectMeta.Annotations["foo"] = "bar"
					g.Expect(k8sClient.Update(ctx, runtimeClass)).To(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("checking no 'quota reserved' event appearing for the workload", func() {
				gomega.Consistently(func(g gomega.Gomega) {
					count, err := utiltesting.HasMatchingEventAppearedTimes(ctx, k8sClient, func(e *corev1.Event) bool {
						return e.Reason == "QuotaReserved" &&
							e.Type == corev1.EventTypeNormal &&
							e.InvolvedObject.Kind == "Workload" &&
							e.InvolvedObject.Name == wl.Name &&
							e.InvolvedObject.Namespace == wl.Namespace
					})
					g.Expect(err).NotTo(gomega.HaveOccurred())
					g.Expect(count).To(gomega.Equal(1))
				}, util.ConsistentDuration, util.ShortInterval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("should not temporarily admit a finished workload on restart manager", framework.SlowSpec, func() {
			wl = utiltestingapi.MakeWorkload("wl1", ns.Name).
				Queue(kueue.LocalQueueName(localQueue.Name)).
				Request(corev1.ResourceCPU, "1").
				RuntimeClass(runtimeClassName).
				Obj()
			util.MustCreate(ctx, k8sClient, wl)

			wlKey := client.ObjectKeyFromObject(wl)

			ginkgo.By("creating a workload", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, wl)).To(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("waiting for admission", func() {
				util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl)
			})

			ginkgo.By("finishing the workload", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlKey, wl)).To(gomega.Succeed())
					g.Expect(workload.Finish(ctx, k8sClient, wl, "ByTest", "By test", util.RealClock, nil)).To(gomega.Succeed(), nil)
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("waiting for finish", func() {
				util.ExpectWorkloadToFinish(ctx, k8sClient, wlKey)
			})

			ginkgo.By("restarting the manager", func() {
				restartManager()
			})

			ginkgo.By("checking no 'quota reserved' event appearing for the workload", func() {
				gomega.Consistently(func(g gomega.Gomega) {
					count, err := utiltesting.HasMatchingEventAppearedTimes(ctx, k8sClient, func(e *corev1.Event) bool {
						return e.Reason == "QuotaReserved" &&
							e.Type == corev1.EventTypeNormal &&
							e.InvolvedObject.Kind == "Workload" &&
							e.InvolvedObject.Name == wl.Name &&
							e.InvolvedObject.Namespace == wl.Namespace
					})
					g.Expect(err).NotTo(gomega.HaveOccurred())
					g.Expect(count).To(gomega.Equal(1))
				}, util.ConsistentDuration, util.ShortInterval).Should(gomega.Succeed())
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
		})

		ginkgo.AfterAll(func() {
			fwk.StopManager(ctx)
		})

		ginkgo.BeforeEach(func() {
			features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.LocalQueueMetrics, true)
			ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "core-workload-")
			flavor = utiltestingapi.MakeResourceFlavor(flavorOnDemand).Obj()
			gomega.Expect(k8sClient.Create(ctx, flavor)).Should(gomega.Succeed())
			clusterQueue = utiltestingapi.MakeClusterQueue("cq").
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas(flavorOnDemand).
					Resource(corev1.ResourceCPU, "1").Obj()).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, clusterQueue)).To(gomega.Succeed())
			localQueue = utiltestingapi.MakeLocalQueue("q", ns.Name).ClusterQueue("cq").Obj()
			gomega.Expect(k8sClient.Create(ctx, localQueue)).To(gomega.Succeed())
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			util.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, flavor, true)
		})

		ginkgo.It("should delete the workload after retention period elapses", framework.SlowSpec, func() {
			var (
				wl    *kueue.Workload
				wlKey client.ObjectKey
			)

			ginkgo.By("creating a workload", func() {
				wl = utiltestingapi.MakeWorkload("wl-to-expire", ns.Name).Queue("q").Obj()
				wlKey = client.ObjectKeyFromObject(wl)
				gomega.Expect(k8sClient.Create(ctx, wl)).To(gomega.Succeed())
			})

			ginkgo.By("simulating workload admission", func() {
				admission := utiltestingapi.MakeAdmission("cq").Obj()
				util.SetQuotaReservation(ctx, k8sClient, wlKey, admission)
				util.SyncAdmittedConditionForWorkloads(ctx, k8sClient, wl)
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

			util.ExpectFinishedWorkloadsGaugeMetric(clusterQueue, 0)
			util.ExpectLQFinishedWorkloadsGaugeMetric(localQueue, 0)
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

		startManager := func() {
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
		}

		stopManager := func() {
			fwk.StopManager(ctx)
		}

		restartManager := func() {
			stopManager()
			startManager()
		}

		ginkgo.BeforeAll(func() {
			startManager()
		})

		ginkgo.AfterAll(func() {
			stopManager()
		})

		ginkgo.BeforeEach(func() {
			features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.LocalQueueMetrics, true)
			ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "core-workload-")
			flavor = utiltestingapi.MakeResourceFlavor(flavorOnDemand).Obj()
			gomega.Expect(k8sClient.Create(ctx, flavor)).Should(gomega.Succeed())
			clusterQueue = utiltestingapi.MakeClusterQueue("cq").
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas(flavorOnDemand).
					Resource(corev1.ResourceCPU, "1").Obj()).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, clusterQueue)).To(gomega.Succeed())
			localQueue = utiltestingapi.MakeLocalQueue("q", ns.Name).ClusterQueue("cq").Obj()
			gomega.Expect(k8sClient.Create(ctx, localQueue)).To(gomega.Succeed())
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			util.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, flavor, true)
		})

		ginkgo.It("should not delete the workload before retention period elapses", framework.SlowSpec, func() {
			var (
				wl    *kueue.Workload
				wlKey client.ObjectKey
			)

			ginkgo.By("creating a workload", func() {
				wl = utiltestingapi.MakeWorkload("wl-to-stay", ns.Name).Queue("q").Obj()
				wlKey = client.ObjectKeyFromObject(wl)
				gomega.Expect(k8sClient.Create(ctx, wl)).To(gomega.Succeed())
			})

			ginkgo.By("simulating workload admission", func() {
				admission := utiltestingapi.MakeAdmission("cq").Obj()
				util.SetQuotaReservation(ctx, k8sClient, wlKey, admission)
				util.SyncAdmittedConditionForWorkloads(ctx, k8sClient, wl)
			})

			util.ExpectFinishedWorkloadsGaugeMetric(clusterQueue, 0)
			util.ExpectLQFinishedWorkloadsGaugeMetric(localQueue, 0)

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
				gomega.Consistently(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &createdWorkload)).To(gomega.Succeed())
				}, util.ConsistentDuration, util.ShortInterval).Should(gomega.Succeed())
			})

			util.ExpectFinishedWorkloadsGaugeMetric(clusterQueue, 1)
			util.ExpectLQFinishedWorkloadsGaugeMetric(localQueue, 1)

			ginkgo.By("restarting the manager", func() {
				restartManager()
			})

			ginkgo.By("verifying that the metrics still keep their counts", func() {
				util.ExpectFinishedWorkloadsGaugeMetric(clusterQueue, 1)
				util.ExpectLQFinishedWorkloadsGaugeMetric(localQueue, 1)
			})

			ginkgo.By("deleting the workload manually", func() {
				util.ExpectObjectToBeDeleted(ctx, k8sClient, wl, true)
			})

			ginkgo.By("verifying that the metrics are updated", func() {
				util.ExpectFinishedWorkloadsGaugeMetric(clusterQueue, 0)
				util.ExpectLQFinishedWorkloadsGaugeMetric(localQueue, 0)
			})
		})
	})
})
