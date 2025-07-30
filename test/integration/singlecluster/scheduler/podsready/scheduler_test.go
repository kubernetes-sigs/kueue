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

package podsready

import (
	"time"

	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/clock"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	config "sigs.k8s.io/kueue/apis/config/v1beta1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/util/testing"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/test/integration/framework"
	"sigs.k8s.io/kueue/test/util"
)

var (
	ignoreCQConditions                       = cmpopts.IgnoreFields(kueue.ClusterQueueStatus{}, "Conditions")
	ignorePendingWorkloadsStatus             = cmpopts.IgnoreFields(kueue.ClusterQueueStatus{}, "PendingWorkloadsStatus")
	defaultRequeuingBackoffLimitCount *int32 = nil
)

const (
	defaultPodsReadyTimeout   = util.TinyTimeout
	defaultRequeuingTimestamp = config.EvictionTimestamp
)

var _ = ginkgo.Describe("SchedulerWithWaitForPodsReady", func() {
	var (
		// Values changed by tests (and reset after each):
		podsReadyTimeout            = defaultPodsReadyTimeout
		requeuingTimestamp          = defaultRequeuingTimestamp
		requeueingBackoffLimitCount = defaultRequeuingBackoffLimitCount
	)

	var (
		// Values referenced by tests:
		defaultFlavor *kueue.ResourceFlavor
		ns            *corev1.Namespace
		prodClusterQ  *kueue.ClusterQueue
		devClusterQ   *kueue.ClusterQueue
		prodQueue     *kueue.LocalQueue
		devQueue      *kueue.LocalQueue
	)

	ginkgo.JustBeforeEach(func() {
		configuration := &config.Configuration{
			WaitForPodsReady: &config.WaitForPodsReady{
				Enable:         true,
				BlockAdmission: ptr.To(true),
				Timeout:        &metav1.Duration{Duration: podsReadyTimeout},
				RequeuingStrategy: &config.RequeuingStrategy{
					Timestamp:          ptr.To(requeuingTimestamp),
					BackoffLimitCount:  requeueingBackoffLimitCount,
					BackoffBaseSeconds: ptr.To[int32](1),
				},
			},
		}
		fwk.StartManager(ctx, cfg, managerAndSchedulerSetup(configuration))

		defaultFlavor = testing.MakeResourceFlavor("default").Obj()
		util.MustCreate(ctx, k8sClient, defaultFlavor)

		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "podsready-")

		prodClusterQ = testing.MakeClusterQueue("prod-cq").
			Cohort("all").
			ResourceGroup(*testing.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "5").Obj()).
			Obj()
		util.MustCreate(ctx, k8sClient, prodClusterQ)

		devClusterQ = testing.MakeClusterQueue("dev-cq").
			Cohort("all").
			ResourceGroup(*testing.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "5").Obj()).
			Obj()
		util.MustCreate(ctx, k8sClient, devClusterQ)

		prodQueue = testing.MakeLocalQueue("prod-queue", ns.Name).ClusterQueue(prodClusterQ.Name).Obj()
		util.MustCreate(ctx, k8sClient, prodQueue)

		devQueue = testing.MakeLocalQueue("dev-queue", ns.Name).ClusterQueue(devClusterQ.Name).Obj()
		util.MustCreate(ctx, k8sClient, devQueue)

		util.ExpectClusterQueuesToBeActive(ctx, k8sClient, prodClusterQ, devClusterQ)
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		util.ExpectObjectToBeDeleted(ctx, k8sClient, prodClusterQ, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, devClusterQ, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, defaultFlavor, true)
		fwk.StopManager(ctx)

		// Reset values that are changed by tests.
		podsReadyTimeout = defaultPodsReadyTimeout
		requeuingTimestamp = defaultRequeuingTimestamp
		requeueingBackoffLimitCount = defaultRequeuingBackoffLimitCount
	})

	ginkgo.Context("Long PodsReady timeout", func() {
		ginkgo.BeforeEach(func() {
			podsReadyTimeout = util.LongTimeout
		})

		ginkgo.It("Should unblock admission of new workloads in other ClusterQueues once the admitted workload exceeds timeout", func() {
			ginkgo.By("checking the first prod workload gets admitted while the second is waiting")
			prodWl := testing.MakeWorkload("prod-wl", ns.Name).Queue(kueue.LocalQueueName(prodQueue.Name)).Request(corev1.ResourceCPU, "2").Obj()
			util.MustCreate(ctx, k8sClient, prodWl)
			devWl := testing.MakeWorkload("dev-wl", ns.Name).Queue(kueue.LocalQueueName(devQueue.Name)).Request(corev1.ResourceCPU, "2").Obj()
			util.MustCreate(ctx, k8sClient, devWl)
			util.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, prodClusterQ.Name, prodWl)
			util.ExpectWorkloadsToBeWaiting(ctx, k8sClient, devWl)

			ginkgo.By("update the first workload with PodsReady=True, reason=PodsReady condition and verify the second workload is admitted")
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(prodWl), prodWl)).Should(gomega.Succeed())
				apimeta.SetStatusCondition(&prodWl.Status.Conditions, metav1.Condition{
					Type:   kueue.WorkloadPodsReady,
					Status: metav1.ConditionTrue,
					Reason: "PodsReady",
				})
				g.Expect(k8sClient.Status().Update(ctx, prodWl)).Should(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
			util.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, devClusterQ.Name, devWl)
		})

		ginkgo.It("Should unblock admission of new workloads in other ClusterQueues once the admitted workload exceeds timeout", func() {
			ginkgo.By("checking the first prod workload gets admitted while the second is waiting")
			prodWl := testing.MakeWorkload("prod-wl", ns.Name).Queue(kueue.LocalQueueName(prodQueue.Name)).Request(corev1.ResourceCPU, "2").Obj()
			util.MustCreate(ctx, k8sClient, prodWl)
			devWl := testing.MakeWorkload("dev-wl", ns.Name).Queue(kueue.LocalQueueName(devQueue.Name)).Request(corev1.ResourceCPU, "2").Obj()
			util.MustCreate(ctx, k8sClient, devWl)
			util.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, prodClusterQ.Name, prodWl)
			util.ExpectWorkloadsToBeWaiting(ctx, k8sClient, devWl)

			ginkgo.By("update the first workload with PodsReady=True, reason=Started condition and verify the second workload is admitted")
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(prodWl), prodWl)).Should(gomega.Succeed())
				apimeta.SetStatusCondition(&prodWl.Status.Conditions, metav1.Condition{
					Type:   kueue.WorkloadPodsReady,
					Status: metav1.ConditionTrue,
					Reason: kueue.WorkloadStarted,
				})
				g.Expect(k8sClient.Status().Update(ctx, prodWl)).Should(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
			util.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, devClusterQ.Name, devWl)
		})

		ginkgo.It("Should emit the PodsReadyToEvictedTimeSeconds metric", func() {
			ginkgo.By("create a workload and await its admission")
			prodWl := testing.MakeWorkload("prod-wl", ns.Name).Queue(kueue.LocalQueueName(prodQueue.Name)).Request(corev1.ResourceCPU, "2").Obj()
			util.MustCreate(ctx, k8sClient, prodWl)

			ginkgo.By("update the workload with PodsReady=True")
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(prodWl), prodWl)).Should(gomega.Succeed())
				apimeta.SetStatusCondition(&prodWl.Status.Conditions, metav1.Condition{
					Type:   kueue.WorkloadPodsReady,
					Status: metav1.ConditionTrue,
					Reason: kueue.WorkloadStarted,
				})
				g.Expect(k8sClient.Status().Update(ctx, prodWl)).Should(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
			util.ExpectPodsReadyCondition(ctx, k8sClient, client.ObjectKeyFromObject(prodWl))

			ginkgo.By("manually evict the workload by suspending, which sets WorkloadEvicted condition reason to Deactivated")
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(prodWl), prodWl)).Should(gomega.Succeed())
				prodWl.Spec.Active = ptr.To(false)
				g.Expect(k8sClient.Update(ctx, prodWl)).Should(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
			util.FinishEvictionForWorkloads(ctx, k8sClient, prodWl)

			ginkgo.By("check for PodsReadyToEvictedTimeSeconds metric existence")
			util.ExpectPodsReadyToEvictedTimeSeconds(prodClusterQ.Name, kueue.WorkloadDeactivated, 1)
		})

		ginkgo.It("Should unblock admission of new workloads once the admitted workload is deleted", func() {
			ginkgo.By("checking the first prod workload gets admitted while the second is waiting")
			prodWl := testing.MakeWorkload("prod-wl", ns.Name).Queue(kueue.LocalQueueName(prodQueue.Name)).Request(corev1.ResourceCPU, "2").Obj()
			util.MustCreate(ctx, k8sClient, prodWl)
			util.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, prodClusterQ.Name, prodWl)

			devWl := testing.MakeWorkload("dev-wl", ns.Name).Queue(kueue.LocalQueueName(devQueue.Name)).Request(corev1.ResourceCPU, "2").Obj()
			util.MustCreate(ctx, k8sClient, devWl)
			util.ExpectWorkloadsToBeWaiting(ctx, k8sClient, devWl)

			ginkgo.By("delete the first workload and verify the second workload is admitted")
			gomega.Expect(k8sClient.Delete(ctx, prodWl)).Should(gomega.Succeed())
			util.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, devClusterQ.Name, devWl)
		})

		ginkgo.It("Should block admission of one new workload if two are considered in the same scheduling cycle", framework.SlowSpec, func() {
			ginkgo.By("creating two workloads but delaying cluster queue creation which has enough capacity")
			prodWl := testing.MakeWorkload("prod-wl", ns.Name).Queue(kueue.LocalQueueName(prodQueue.Name)).Request(corev1.ResourceCPU, "11").Obj()
			util.MustCreate(ctx, k8sClient, prodWl)
			devWl := testing.MakeWorkload("dev-wl", ns.Name).Queue(kueue.LocalQueueName(devQueue.Name)).Request(corev1.ResourceCPU, "11").Obj()
			util.WaitForNextSecondAfterCreation(prodWl)
			util.MustCreate(ctx, k8sClient, devWl)
			util.ExpectWorkloadsToBePending(ctx, k8sClient, prodWl, devWl)

			ginkgo.By("creating the cluster queue")
			// Delay cluster queue creation to make sure workloads are in the same
			// scheduling cycle.
			testCQ := testing.MakeClusterQueue("test-cq").
				Cohort("all").
				ResourceGroup(*testing.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "25", "0").Obj()).
				Obj()
			util.MustCreate(ctx, k8sClient, testCQ)
			defer func() {
				util.ExpectObjectToBeDeleted(ctx, k8sClient, testCQ, true)
			}()

			ginkgo.By("verifying that the first created workload is admitted and the second workload is waiting as the first one has PodsReady=False")
			util.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, prodClusterQ.Name, prodWl)
			util.ExpectWorkloadsToBeWaiting(ctx, k8sClient, devWl)
		})
	})

	var _ = ginkgo.Context("Short PodsReady timeout", func() {
		var realClock = clock.RealClock{}

		ginkgo.BeforeEach(func() {
			podsReadyTimeout = util.ShortTimeout
			requeueingBackoffLimitCount = ptr.To[int32](2)
		})

		ginkgo.It("Should requeue a workload which exceeded the timeout to reach PodsReady=True", framework.SlowSpec, func() {
			const lowPrio, highPrio = 0, 100

			ginkgo.By("create the 'prod1' workload")
			prodWl1 := testing.MakeWorkload("prod1", ns.Name).Queue(kueue.LocalQueueName(prodQueue.Name)).Priority(highPrio).Request(corev1.ResourceCPU, "2").Obj()
			util.MustCreate(ctx, k8sClient, prodWl1)

			ginkgo.By("create the 'prod2' workload")
			prodWl2 := testing.MakeWorkload("prod2", ns.Name).Queue(kueue.LocalQueueName(prodQueue.Name)).Priority(lowPrio).Request(corev1.ResourceCPU, "2").Obj()
			util.MustCreate(ctx, k8sClient, prodWl2)

			ginkgo.By("checking the 'prod1' workload is admitted and the 'prod2' workload is waiting")
			util.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, prodClusterQ.Name, prodWl1)
			util.ExpectWorkloadsToBeWaiting(ctx, k8sClient, prodWl2)

			ginkgo.By("awaiting for the Admitted=True condition to be added to 'prod1")
			// We assume that the test will get to this check before the timeout expires and the
			// kueue cancels the admission. Mentioning this in case this test flakes in the future.
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(prodWl1), prodWl1)).Should(gomega.Succeed())
				g.Expect(workload.HasQuotaReservation(prodWl1)).Should(gomega.BeTrue())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			ginkgo.By("wait for the 'prod1' workload to be evicted")
			util.AwaitWorkloadEvictionByPodsReadyTimeout(ctx, k8sClient, client.ObjectKeyFromObject(prodWl1), podsReadyTimeout)
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(prodWl1), prodWl1)).Should(gomega.Succeed())
				g.Expect(ptr.Deref(prodWl1.Status.RequeueState, kueue.RequeueState{})).Should(gomega.BeComparableTo(kueue.RequeueState{
					Count: ptr.To[int32](1),
				}, cmpopts.IgnoreFields(kueue.RequeueState{}, "RequeueAt")))
				g.Expect(prodWl1.Status.RequeueState.RequeueAt).ShouldNot(gomega.BeNil())
			}, util.Timeout, util.Interval).Should(gomega.Succeed(), "the workload should be evicted after the timeout expires")

			util.FinishEvictionForWorkloads(ctx, k8sClient, prodWl1)
			util.ExpectEvictedWorkloadsTotalMetric(prodClusterQ.Name, kueue.WorkloadEvictedByPodsReadyTimeout, 1)
			util.ExpectEvictedWorkloadsOnceTotalMetric(prodClusterQ.Name, kueue.WorkloadEvictedByPodsReadyTimeout, kueue.WorkloadWaitForStart, 1)

			ginkgo.By("verify the 'prod2' workload gets admitted and the 'prod1' is pending by backoff")
			util.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, prodClusterQ.Name, prodWl2)
			// To avoid flakiness, we don't verify if the workload has a QuotaReserved=false with pending reason here.
		})

		ginkgo.It("Should re-admit a timed out workload and deactivate a workload exceeded the re-queue count limit. After that re-activating a workload", framework.SlowSpec, func() {
			ginkgo.By("create the 'prod' workload")
			prodWl := testing.MakeWorkload("prod", ns.Name).Queue(kueue.LocalQueueName(prodQueue.Name)).Request(corev1.ResourceCPU, "2").Obj()
			util.MustCreate(ctx, k8sClient, prodWl)

			ginkgo.By("checking the 'prod' workload is admitted")
			util.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, prodClusterQ.Name, prodWl)
			util.ExpectQuotaReservedWorkloadsTotalMetric(prodClusterQ, 1)
			util.ExpectAdmittedWorkloadsTotalMetric(prodClusterQ, 1)
			util.AwaitWorkloadEvictionByPodsReadyTimeout(ctx, k8sClient, client.ObjectKeyFromObject(prodWl), podsReadyTimeout)
			util.SetRequeuedConditionWithPodsReadyTimeout(ctx, k8sClient, client.ObjectKeyFromObject(prodWl))

			ginkgo.By("finish the eviction, and the workload is pending by backoff")
			util.FinishEvictionForWorkloads(ctx, k8sClient, prodWl)
			util.ExpectWorkloadToHaveRequeueState(ctx, k8sClient, client.ObjectKeyFromObject(prodWl), &kueue.RequeueState{
				Count: ptr.To[int32](1),
			}, false)
			// To avoid flakiness, we don't verify if the workload has a QuotaReserved=false with pending reason here.

			ginkgo.By("verify the 'prod' workload gets re-admitted twice")
			util.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, prodClusterQ.Name, prodWl)
			util.ExpectQuotaReservedWorkloadsTotalMetric(prodClusterQ, 2)
			util.ExpectAdmittedWorkloadsTotalMetric(prodClusterQ, 2)
			util.AwaitWorkloadEvictionByPodsReadyTimeout(ctx, k8sClient, client.ObjectKeyFromObject(prodWl), podsReadyTimeout)
			util.SetRequeuedConditionWithPodsReadyTimeout(ctx, k8sClient, client.ObjectKeyFromObject(prodWl))
			util.FinishEvictionForWorkloads(ctx, k8sClient, prodWl)
			util.ExpectWorkloadToHaveRequeueState(ctx, k8sClient, client.ObjectKeyFromObject(prodWl), &kueue.RequeueState{
				Count: ptr.To[int32](2),
			}, false)

			ginkgo.By("the workload exceeded re-queue backoff limit should be deactivated")
			util.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, prodClusterQ.Name, prodWl)
			util.ExpectQuotaReservedWorkloadsTotalMetric(prodClusterQ, 3)
			util.ExpectAdmittedWorkloadsTotalMetric(prodClusterQ, 3)
			time.Sleep(podsReadyTimeout)
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(prodWl), prodWl)).Should(gomega.Succeed())
				g.Expect(workload.IsActive(prodWl)).Should(gomega.BeFalse())
				g.Expect(prodWl.Status.RequeueState).Should(gomega.BeNil())
				workload.SetRequeuedCondition(prodWl, kueue.WorkloadDeactivated, "by test", false)
				g.Expect(workload.ApplyAdmissionStatus(ctx, k8sClient, prodWl, true, realClock)).Should(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
			util.FinishEvictionForWorkloads(ctx, k8sClient, prodWl)
			// should observe a metrics of WorkloadEvictedByDeactivation
			util.ExpectEvictedWorkloadsTotalMetric(prodClusterQ.Name, "DeactivatedDueToRequeuingLimitExceeded", 1)
			util.ExpectEvictedWorkloadsOnceTotalMetric(prodClusterQ.Name, kueue.WorkloadEvictedByPodsReadyTimeout, kueue.WorkloadWaitForStart, 1)

			ginkgo.By("the reactivated workload should not be deactivated by the scheduler unless exceeding the backoffLimitCount")
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(prodWl), prodWl)).Should(gomega.Succeed())
				prodWl.Spec.Active = ptr.To(true)
				g.Expect(k8sClient.Update(ctx, prodWl)).Should(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
			// We await for re-admission. Then, the workload keeps the QuotaReserved condition
			// even after timeout until FinishEvictionForWorkloads is called below.
			util.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, prodClusterQ.Name, prodWl)
			util.ExpectQuotaReservedWorkloadsTotalMetric(prodClusterQ, 4)
			util.ExpectAdmittedWorkloadsTotalMetric(prodClusterQ, 4)
			util.AwaitWorkloadEvictionByPodsReadyTimeout(ctx, k8sClient, client.ObjectKeyFromObject(prodWl), podsReadyTimeout)
			util.SetRequeuedConditionWithPodsReadyTimeout(ctx, k8sClient, client.ObjectKeyFromObject(prodWl))
			util.FinishEvictionForWorkloads(ctx, k8sClient, prodWl)
			util.ExpectWorkloadToHaveRequeueState(ctx, k8sClient, client.ObjectKeyFromObject(prodWl), &kueue.RequeueState{
				Count: ptr.To[int32](1),
			}, false)
		})
	})

	var _ = ginkgo.Context("Tiny PodsReady timeout", func() {
		ginkgo.BeforeEach(func() {
			podsReadyTimeout = util.TinyTimeout
			requeueingBackoffLimitCount = ptr.To[int32](2)
		})

		ginkgo.It("Should unblock admission of new workloads in other ClusterQueues once the admitted workload exceeds timeout", func() {
			ginkgo.By("create the 'prod' workload")
			prodWl := testing.MakeWorkload("prod", ns.Name).Queue(kueue.LocalQueueName(prodQueue.Name)).Request(corev1.ResourceCPU, "2").Obj()
			util.MustCreate(ctx, k8sClient, prodWl)
			util.WaitForNextSecondAfterCreation(prodWl)
			devWl := testing.MakeWorkload("dev", ns.Name).Queue(kueue.LocalQueueName(devQueue.Name)).Request(corev1.ResourceCPU, "2").Obj()
			util.MustCreate(ctx, k8sClient, devWl)

			ginkgo.By("wait for the 'prod' workload to be admitted and the 'dev' to be waiting")
			util.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, prodClusterQ.Name, prodWl)
			util.ExpectWorkloadsToBeWaiting(ctx, k8sClient, devWl)

			ginkgo.By("verify the 'prod' queue resources are used")
			gomega.Eventually(func(g gomega.Gomega) {
				var updatedCQ kueue.ClusterQueue
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(prodClusterQ), &updatedCQ)).To(gomega.Succeed())
				g.Expect(updatedCQ.Status).Should(gomega.BeComparableTo(kueue.ClusterQueueStatus{
					PendingWorkloads:   0,
					ReservingWorkloads: 1,
					AdmittedWorkloads:  1,
					FlavorsReservation: []kueue.FlavorUsage{{
						Name: "default",
						Resources: []kueue.ResourceUsage{{
							Name:  corev1.ResourceCPU,
							Total: resource.MustParse("2"),
						}},
					}},
					FlavorsUsage: []kueue.FlavorUsage{{
						Name: "default",
						Resources: []kueue.ResourceUsage{{
							Name:  corev1.ResourceCPU,
							Total: resource.MustParse("2"),
						}},
					}},
				}, ignoreCQConditions, ignorePendingWorkloadsStatus))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			ginkgo.By("wait for the timeout to be exceeded")
			time.Sleep(podsReadyTimeout)

			ginkgo.By("finish the eviction")
			util.FinishEvictionForWorkloads(ctx, k8sClient, prodWl)

			ginkgo.By("wait for the first workload to be unadmitted")
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(prodWl), prodWl)).Should(gomega.Succeed())
				g.Expect(prodWl.Status.Admission).Should(gomega.BeNil())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			ginkgo.By("verify the queue resources are freed")
			gomega.Eventually(func(g gomega.Gomega) {
				var updatedCQ kueue.ClusterQueue
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(prodClusterQ), &updatedCQ)).To(gomega.Succeed())
				g.Expect(updatedCQ.Status).Should(gomega.BeComparableTo(kueue.ClusterQueueStatus{
					PendingWorkloads:   1,
					ReservingWorkloads: 0,
					AdmittedWorkloads:  0,
					FlavorsReservation: []kueue.FlavorUsage{{
						Name: "default",
						Resources: []kueue.ResourceUsage{{
							Name:  corev1.ResourceCPU,
							Total: resource.MustParse("0"),
						}},
					}},
					FlavorsUsage: []kueue.FlavorUsage{{
						Name: "default",
						Resources: []kueue.ResourceUsage{{
							Name:  corev1.ResourceCPU,
							Total: resource.MustParse("0"),
						}},
					}},
				}, ignoreCQConditions, ignorePendingWorkloadsStatus))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			ginkgo.By("verify the active workload metric is decreased for the cluster queue")
			util.ExpectReservingActiveWorkloadsMetric(prodClusterQ, 0)

			ginkgo.By("wait for the 'dev' workload to get admitted")
			util.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, devClusterQ.Name, devWl)
			ginkgo.By("wait for the 'prod' workload to be waiting")
			util.ExpectWorkloadsToBeWaiting(ctx, k8sClient, prodWl)

			ginkgo.By("delete the waiting 'prod' workload so that it does not get admitted during teardown")
			gomega.Expect(k8sClient.Delete(ctx, prodWl)).Should(gomega.Succeed())
		})
	})

	ginkgo.It("Should move the evicted workload at the end of the queue", framework.SlowSpec, func() {
		// We wait 1 second between each workload creation calls. Therefore, we need to add this time to timeout.
		podsReadyTimeout = util.TinyTimeout + 2*time.Second
		requeueingBackoffLimitCount = ptr.To[int32](2)

		localQueueName := "eviction-lq"

		// the workloads are created with a 5 cpu resource requirement to ensure only one can fit at a given time,
		// letting them all to time out, we should see a circular buffer admission pattern
		wl1 := testing.MakeWorkload("prod1", ns.Name).Queue(kueue.LocalQueueName(localQueueName)).Request(corev1.ResourceCPU, "5").Obj()
		wl2 := testing.MakeWorkload("prod2", ns.Name).Queue(kueue.LocalQueueName(localQueueName)).Request(corev1.ResourceCPU, "5").Obj()
		wl3 := testing.MakeWorkload("prod3", ns.Name).Queue(kueue.LocalQueueName(localQueueName)).Request(corev1.ResourceCPU, "5").Obj()

		ginkgo.By("create the workloads", func() {
			util.MustCreate(ctx, k8sClient, wl1)
			util.WaitForNextSecondAfterCreation(wl1)
			util.MustCreate(ctx, k8sClient, wl2)
			util.WaitForNextSecondAfterCreation(wl2)
			util.MustCreate(ctx, k8sClient, wl3)
		})

		ginkgo.By("create the local queue to start admission", func() {
			lq := testing.MakeLocalQueue(localQueueName, ns.Name).ClusterQueue(prodClusterQ.Name).Obj()
			util.MustCreate(ctx, k8sClient, lq)
		})

		ginkgo.By("waiting for the first workload to be admitted", func() {
			util.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, prodClusterQ.Name, wl1)
		})

		ginkgo.By("waiting the timeout, the first workload should be evicted and the second one should be admitted", func() {
			time.Sleep(podsReadyTimeout)
			util.FinishEvictionForWorkloads(ctx, k8sClient, wl1)
			util.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, prodClusterQ.Name, wl2)
		})

		ginkgo.By("finishing the second workload, the third one should be admitted", func() {
			time.Sleep(podsReadyTimeout)
			util.FinishWorkloads(ctx, k8sClient, wl2)
			util.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, prodClusterQ.Name, wl3)
		})

		ginkgo.By("finishing the third workload, the first one should be admitted", func() {
			time.Sleep(podsReadyTimeout)
			util.FinishWorkloads(ctx, k8sClient, wl3)
			util.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, prodClusterQ.Name, wl1)
		})

		ginkgo.By("verifying if all workloads have a proper re-queue count", func() {
			// Here, we focus on verifying if the requeuingTimestamp works well.
			// So, we don't check if the .status.requeueState.requeueAt is reset.
			util.ExpectWorkloadToHaveRequeueState(ctx, k8sClient, client.ObjectKeyFromObject(wl1), &kueue.RequeueState{
				Count: ptr.To[int32](2),
			}, true)
			util.ExpectWorkloadToHaveRequeueState(ctx, k8sClient, client.ObjectKeyFromObject(wl2), &kueue.RequeueState{
				Count: ptr.To[int32](1),
			}, true)
			util.ExpectWorkloadToHaveRequeueState(ctx, k8sClient, client.ObjectKeyFromObject(wl3), &kueue.RequeueState{
				Count: ptr.To[int32](1),
			}, true)
		})
	})

	var _ = ginkgo.Context("Requeuing timestamp set to Creation", func() {
		var (
			standaloneClusterQ *kueue.ClusterQueue
			standaloneQueue    *kueue.LocalQueue
		)

		ginkgo.BeforeEach(func() {
			// We wait 1 second between each workload creation calls. Therefore, we need to add this time to timeout.
			podsReadyTimeout = util.ShortTimeout + 2*time.Second
			requeuingTimestamp = config.CreationTimestamp
		})

		ginkgo.JustBeforeEach(func() {
			// Build a standalone cluster queue with just enough capacity for a single workload.
			// (Avoid using prod/dev queues to avoid borrowing)
			standaloneClusterQ = testing.MakeClusterQueue("standalone-cq").
				ResourceGroup(*testing.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "1").Obj()).
				Obj()
			util.MustCreate(ctx, k8sClient, standaloneClusterQ)

			standaloneQueue = testing.MakeLocalQueue("standalone-queue", ns.Name).ClusterQueue(standaloneClusterQ.Name).Obj()
			util.MustCreate(ctx, k8sClient, standaloneQueue)
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteObject(ctx, k8sClient, standaloneClusterQ)).Should(gomega.Succeed())
			gomega.Expect(util.DeleteObject(ctx, k8sClient, standaloneQueue)).Should(gomega.Succeed())
		})

		ginkgo.It("Should prioritize workloads submitted earlier", framework.SlowSpec, func() {
			// the workloads are created with a 1 cpu resource requirement to ensure only one can fit at a given time
			wl1 := testing.MakeWorkload("wl-1", ns.Name).Queue(kueue.LocalQueueName(standaloneQueue.Name)).Request(corev1.ResourceCPU, "1").Obj()
			wl2 := testing.MakeWorkload("wl-2", ns.Name).Queue(kueue.LocalQueueName(standaloneQueue.Name)).Request(corev1.ResourceCPU, "1").Obj()
			wl3 := testing.MakeWorkload("wl-3", ns.Name).Queue(kueue.LocalQueueName(standaloneQueue.Name)).Request(corev1.ResourceCPU, "1").Obj()

			ginkgo.By("create the workloads", func() {
				util.MustCreate(ctx, k8sClient, wl1)
				util.WaitForNextSecondAfterCreation(wl1)
				util.MustCreate(ctx, k8sClient, wl2)
				util.WaitForNextSecondAfterCreation(wl2)
				util.MustCreate(ctx, k8sClient, wl3)
			})

			ginkgo.By("waiting for the first workload to be admitted", func() {
				util.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, standaloneClusterQ.Name, wl1)
			})
			ginkgo.By("checking that the second and third workloads are still pending", func() {
				util.ExpectWorkloadsToBePending(ctx, k8sClient, wl2, wl3)
			})
			ginkgo.By("finishing the eviction of the first workload", func() {
				util.FinishEvictionForWorkloads(ctx, k8sClient, wl1)
			})
			ginkgo.By("waiting for the second workload to be admitted", func() {
				util.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, standaloneClusterQ.Name, wl2)
			})
			// The first workload is still pending by backoff, and the third workload is also still pending by insufficient quota.
			// To avoid flakiness, we don't verify if the workload has a QuotaReserved=false with pending reason here.
			ginkgo.By("finishing the eviction of the second workload", func() {
				util.FinishEvictionForWorkloads(ctx, k8sClient, wl2)
			})
			ginkgo.By("waiting for the first workload to be admitted since backoff is completed, and the second and third workloads are still pending", func() {
				util.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, standaloneClusterQ.Name, wl1)
				// To avoid flakiness, we don't verify if the workload has a QuotaReserved=false with pending reason here.
			})
		})
	})
})

var _ = ginkgo.Describe("SchedulerWithWaitForPodsReadyNonblockingMode", func() {
	var (
		// Values changed by tests (and reset after each):
		podsReadyTimeout            = defaultPodsReadyTimeout
		requeuingTimestamp          = defaultRequeuingTimestamp
		requeueingBackoffLimitCount = defaultRequeuingBackoffLimitCount
	)

	var (
		// Values referenced by tests:
		defaultFlavor *kueue.ResourceFlavor
		ns            *corev1.Namespace
		prodClusterQ  *kueue.ClusterQueue
		devClusterQ   *kueue.ClusterQueue
		prodQueue     *kueue.LocalQueue
		devQueue      *kueue.LocalQueue
	)

	ginkgo.JustBeforeEach(func() {
		configuration := &config.Configuration{
			WaitForPodsReady: &config.WaitForPodsReady{
				Enable:         true,
				BlockAdmission: ptr.To(false),
				Timeout:        &metav1.Duration{Duration: podsReadyTimeout},
				RequeuingStrategy: &config.RequeuingStrategy{
					Timestamp:          ptr.To(requeuingTimestamp),
					BackoffLimitCount:  requeueingBackoffLimitCount,
					BackoffBaseSeconds: ptr.To[int32](1),
				},
			},
		}
		fwk.StartManager(ctx, cfg, managerAndSchedulerSetup(configuration))

		defaultFlavor = testing.MakeResourceFlavor("default").Obj()
		util.MustCreate(ctx, k8sClient, defaultFlavor)

		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "podsready-nonblocking-")

		prodClusterQ = testing.MakeClusterQueue("prod-cq").
			Cohort("all").
			ResourceGroup(*testing.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "5").Obj()).
			Obj()
		util.MustCreate(ctx, k8sClient, prodClusterQ)

		devClusterQ = testing.MakeClusterQueue("dev-cq").
			Cohort("all").
			ResourceGroup(*testing.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "5").Obj()).
			Obj()
		util.MustCreate(ctx, k8sClient, devClusterQ)

		prodQueue = testing.MakeLocalQueue("prod-queue", ns.Name).ClusterQueue(prodClusterQ.Name).Obj()
		util.MustCreate(ctx, k8sClient, prodQueue)

		devQueue = testing.MakeLocalQueue("dev-queue", ns.Name).ClusterQueue(devClusterQ.Name).Obj()
		util.MustCreate(ctx, k8sClient, devQueue)

		util.ExpectClusterQueuesToBeActive(ctx, k8sClient, prodClusterQ, devClusterQ)
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		util.ExpectObjectToBeDeleted(ctx, k8sClient, prodClusterQ, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, devClusterQ, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, defaultFlavor, true)
		fwk.StopManager(ctx)

		// Reset values that are changed by tests.
		podsReadyTimeout = defaultPodsReadyTimeout
		requeuingTimestamp = defaultRequeuingTimestamp
		requeueingBackoffLimitCount = defaultRequeuingBackoffLimitCount
	})

	ginkgo.Context("Long PodsReady timeout", func() {
		ginkgo.BeforeEach(func() {
			podsReadyTimeout = util.LongTimeout
		})

		ginkgo.It("Should not block admission of one new workload if two are considered in the same scheduling cycle", func() {
			ginkgo.By("creating two workloads but delaying cluster queue creation which has enough capacity")
			prodWl := testing.MakeWorkload("prod-wl", ns.Name).Queue(kueue.LocalQueueName(prodQueue.Name)).Request(corev1.ResourceCPU, "11").Obj()
			util.MustCreate(ctx, k8sClient, prodWl)
			devWl := testing.MakeWorkload("dev-wl", ns.Name).Queue(kueue.LocalQueueName(devQueue.Name)).Request(corev1.ResourceCPU, "11").Obj()
			util.MustCreate(ctx, k8sClient, devWl)
			util.ExpectWorkloadsToBePending(ctx, k8sClient, prodWl, devWl)

			ginkgo.By("creating the cluster queue")
			// Delay cluster queue creation to make sure workloads are in the same
			// scheduling cycle.
			testCQ := testing.MakeClusterQueue("test-cq").
				Cohort("all").
				ResourceGroup(*testing.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "25", "0").Obj()).
				Obj()
			util.MustCreate(ctx, k8sClient, testCQ)
			defer func() {
				util.ExpectObjectToBeDeleted(ctx, k8sClient, testCQ, true)
			}()

			ginkgo.By("verifying that the first created workload is admitted and the second workload is admitted as the blockAdmission is false")
			util.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, prodClusterQ.Name, prodWl)
			util.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, devClusterQ.Name, devWl)
		})
	})

	var _ = ginkgo.Context("Tiny PodsReady timeout", func() {
		ginkgo.BeforeEach(func() {
			podsReadyTimeout = util.TinyTimeout
		})

		ginkgo.It("Should re-admit a timed out workload", func() {
			ginkgo.By("create the 'prod' workload")
			prodWl := testing.MakeWorkload("prod", ns.Name).Queue(kueue.LocalQueueName(prodQueue.Name)).Request(corev1.ResourceCPU, "2").Obj()
			util.MustCreate(ctx, k8sClient, prodWl)
			ginkgo.By("checking the 'prod' workload is admitted")
			util.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, prodClusterQ.Name, prodWl)
			util.ExpectQuotaReservedWorkloadsTotalMetric(prodClusterQ, 1)
			util.ExpectAdmittedWorkloadsTotalMetric(prodClusterQ, 1)
			ginkgo.By("exceed the timeout for the 'prod' workload")
			time.Sleep(podsReadyTimeout)
			ginkgo.By("finish the eviction")
			util.FinishEvictionForWorkloads(ctx, k8sClient, prodWl)

			ginkgo.By("verify the 'prod' workload gets re-admitted once")
			util.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, prodClusterQ.Name, prodWl)
			util.ExpectQuotaReservedWorkloadsTotalMetric(prodClusterQ, 2)
			util.ExpectAdmittedWorkloadsTotalMetric(prodClusterQ, 2)
			util.AwaitWorkloadEvictionByPodsReadyTimeout(ctx, k8sClient, client.ObjectKeyFromObject(prodWl), podsReadyTimeout)
			util.SetRequeuedConditionWithPodsReadyTimeout(ctx, k8sClient, client.ObjectKeyFromObject(prodWl))
			util.ExpectWorkloadToHaveRequeueState(ctx, k8sClient, client.ObjectKeyFromObject(prodWl), &kueue.RequeueState{
				Count: ptr.To[int32](2),
			}, false)
			gomega.Expect(workload.IsActive(prodWl)).Should(gomega.BeTrue())
		})
	})

	var _ = ginkgo.Context("Requeuing timestamp set to Creation", func() {
		var (
			standaloneClusterQ *kueue.ClusterQueue
			standaloneQueue    *kueue.LocalQueue
		)

		ginkgo.BeforeEach(func() {
			// We wait 1 second between each workload creation calls. Therefore, we need to add this time to timeout.
			podsReadyTimeout = util.ShortTimeout + 2*time.Second
			requeuingTimestamp = config.CreationTimestamp
		})

		ginkgo.JustBeforeEach(func() {
			// Build a standalone cluster queue with just enough capacity for a single workload.
			// (Avoid using prod/dev queues to avoid borrowing)
			standaloneClusterQ = testing.MakeClusterQueue("standalone-cq").
				ResourceGroup(*testing.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "1").Obj()).
				Obj()
			util.MustCreate(ctx, k8sClient, standaloneClusterQ)

			standaloneQueue = testing.MakeLocalQueue("standalone-queue", ns.Name).ClusterQueue(standaloneClusterQ.Name).Obj()
			util.MustCreate(ctx, k8sClient, standaloneQueue)
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteObject(ctx, k8sClient, standaloneClusterQ)).Should(gomega.Succeed())
			gomega.Expect(util.DeleteObject(ctx, k8sClient, standaloneQueue)).Should(gomega.Succeed())
		})

		ginkgo.It("Should keep the evicted workload at the front of the queue", framework.SlowSpec, func() {
			// the workloads are created with a 1 cpu resource requirement to ensure only one can fit at a given time
			wl1 := testing.MakeWorkload("wl-1", ns.Name).Queue(kueue.LocalQueueName(standaloneQueue.Name)).Request(corev1.ResourceCPU, "1").Obj()
			wl2 := testing.MakeWorkload("wl-2", ns.Name).Queue(kueue.LocalQueueName(standaloneQueue.Name)).Request(corev1.ResourceCPU, "1").Obj()
			wl3 := testing.MakeWorkload("wl-3", ns.Name).Queue(kueue.LocalQueueName(standaloneQueue.Name)).Request(corev1.ResourceCPU, "1").Obj()

			ginkgo.By("create the workloads", func() {
				util.MustCreate(ctx, k8sClient, wl1)
				util.WaitForNextSecondAfterCreation(wl1)
				util.MustCreate(ctx, k8sClient, wl2)
				util.WaitForNextSecondAfterCreation(wl2)
				util.MustCreate(ctx, k8sClient, wl3)
			})

			ginkgo.By("waiting for the first workload to be admitted", func() {
				util.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, standaloneClusterQ.Name, wl1)
			})
			ginkgo.By("checking that the second and third workloads are still pending", func() {
				util.ExpectWorkloadsToBePending(ctx, k8sClient, wl2, wl3)
			})
			ginkgo.By("finishing the eviction of the first workload", func() {
				util.FinishEvictionForWorkloads(ctx, k8sClient, wl1)
			})
			ginkgo.By("waiting for the second workload to be admitted", func() {
				util.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, standaloneClusterQ.Name, wl2)
			})
			// The first workload is still pending by backoff, and the third workload is also still pending by insufficient quota.
			// To avoid flakiness, we don't verify if the workload has a QuotaReserved=false with pending reason here.
			ginkgo.By("finishing the eviction of the second workload", func() {
				util.FinishEvictionForWorkloads(ctx, k8sClient, wl2)
			})
			ginkgo.By("waiting for the first workload to be admitted since backoff is completed, and the second and third workloads are still pending", func() {
				util.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, standaloneClusterQ.Name, wl1)
				// To avoid flakiness, we don't verify if the workload has a QuotaReserved=false with pending reason here.
			})
			ginkgo.By("verifying if all workloads have a proper re-queue count", func() {
				// Here, we focus on verifying if the requeuingTimestamp works well.
				// So, we don't check if the .status.requeueState.requeueAt is reset.
				util.ExpectWorkloadToHaveRequeueState(ctx, k8sClient, client.ObjectKeyFromObject(wl1), &kueue.RequeueState{
					Count: ptr.To[int32](2),
				}, true)
				util.ExpectWorkloadToHaveRequeueState(ctx, k8sClient, client.ObjectKeyFromObject(wl2), &kueue.RequeueState{
					Count: ptr.To[int32](1),
				}, true)
				ginkgo.By("wl3 had never been admitted", func() {
					util.ExpectWorkloadToHaveRequeueState(ctx, k8sClient, client.ObjectKeyFromObject(wl3), nil, false)
				})
			})
		})
	})
})
