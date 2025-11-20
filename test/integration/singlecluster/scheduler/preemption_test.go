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

package scheduler

import (
	"fmt"

	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/scheduler/preemption"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/pkg/workloadslicing"
	"sigs.k8s.io/kueue/test/util"
)

const (
	lowPriority int32 = iota - 1
	midPriority
	highPriority
	veryHighPriority
)

var _ = ginkgo.Describe("Preemption", func() {
	var (
		alphaFlavor *kueue.ResourceFlavor
		ns          *corev1.Namespace
	)

	ginkgo.BeforeEach(func() {
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "preemption-")
		alphaFlavor = utiltestingapi.MakeResourceFlavor("alpha").Obj()
		util.MustCreate(ctx, k8sClient, alphaFlavor)
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		util.ExpectObjectToBeDeleted(ctx, k8sClient, alphaFlavor, true)
	})

	ginkgo.Context("In a single ClusterQueue", func() {
		var (
			cq *kueue.ClusterQueue
			q  *kueue.LocalQueue
		)

		ginkgo.BeforeEach(func() {
			cq = utiltestingapi.MakeClusterQueue("cq").
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas("alpha").Resource(corev1.ResourceCPU, "4").Obj()).
				Preemption(kueue.ClusterQueuePreemption{
					WithinClusterQueue: kueue.PreemptionPolicyLowerOrNewerEqualPriority,
				}).
				Obj()
			util.MustCreate(ctx, k8sClient, cq)

			q = utiltestingapi.MakeLocalQueue("q", ns.Name).ClusterQueue(cq.Name).Obj()
			util.MustCreate(ctx, k8sClient, q)
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			util.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
		})

		ginkgo.It("Should preempt Workloads with lower priority when there is not enough quota", func() {
			features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.LocalQueueMetrics, true)
			ginkgo.By("Creating initial Workloads with different priorities")
			lowWl1 := utiltestingapi.MakeWorkload("low-wl-1", ns.Name).
				Queue(kueue.LocalQueueName(q.Name)).
				Priority(lowPriority).
				Request(corev1.ResourceCPU, "1").
				Obj()
			lowWl2 := utiltestingapi.MakeWorkload("low-wl-2", ns.Name).
				Queue(kueue.LocalQueueName(q.Name)).
				Priority(lowPriority).
				Request(corev1.ResourceCPU, "1").
				Obj()
			midWl := utiltestingapi.MakeWorkload("mid-wl", ns.Name).
				Queue(kueue.LocalQueueName(q.Name)).
				Priority(midPriority).
				Request(corev1.ResourceCPU, "1").
				Obj()
			highWl1 := utiltestingapi.MakeWorkload("high-wl-1", ns.Name).
				Queue(kueue.LocalQueueName(q.Name)).
				Priority(highPriority).
				Request(corev1.ResourceCPU, "1").
				Obj()
			util.MustCreate(ctx, k8sClient, lowWl1)
			util.MustCreate(ctx, k8sClient, lowWl2)
			util.MustCreate(ctx, k8sClient, midWl)
			util.MustCreate(ctx, k8sClient, highWl1)

			util.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, cq.Name, lowWl1, lowWl2, midWl, highWl1)

			ginkgo.By("Creating a low priority Workload")
			lowWl3 := utiltestingapi.MakeWorkload("low-wl-3", ns.Name).
				Queue(kueue.LocalQueueName(q.Name)).
				Priority(lowPriority).
				Request(corev1.ResourceCPU, "1").
				Obj()
			util.MustCreate(ctx, k8sClient, lowWl3)

			util.ExpectWorkloadsToBePending(ctx, k8sClient, lowWl3)

			ginkgo.By("Creating a high priority Workload")
			highWl2 := utiltestingapi.MakeWorkload("high-wl-2", ns.Name).
				Queue(kueue.LocalQueueName(q.Name)).
				Priority(highPriority).
				Request(corev1.ResourceCPU, "2").
				Obj()
			util.MustCreate(ctx, k8sClient, highWl2)

			util.FinishEvictionForWorkloads(ctx, k8sClient, lowWl1, lowWl2)
			util.ExpectEvictedWorkloadsTotalMetric(cq.Name, kueue.WorkloadEvictedByPreemption, "", "", 2)
			util.ExpectLQEvictedWorkloadsTotalMetric(q, kueue.WorkloadEvictedByPreemption, "", "", 2)
			util.ExpectPreemptedWorkloadsTotalMetric(cq.Name, kueue.InClusterQueueReason, 2)
			util.ExpectEvictedWorkloadsOnceTotalMetric(cq.Name, kueue.WorkloadEvictedByPreemption, "", "", 2)

			util.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, cq.Name, highWl2)
			util.ExpectWorkloadsToBePending(ctx, k8sClient, lowWl1, lowWl2)
		})

		ginkgo.It("Should preempt newer Workloads with the same priority when there is not enough quota", func() {
			ginkgo.By("Creating initial Workloads")
			wl1 := utiltestingapi.MakeWorkload("wl-1", ns.Name).
				Queue(kueue.LocalQueueName(q.Name)).
				Priority(lowPriority).
				Request(corev1.ResourceCPU, "1").
				Obj()
			wl2 := utiltestingapi.MakeWorkload("wl-2", ns.Name).
				Queue(kueue.LocalQueueName(q.Name)).
				Priority(lowPriority).
				Request(corev1.ResourceCPU, "1").
				Obj()
			wl3 := utiltestingapi.MakeWorkload("wl-3", ns.Name).
				Queue(kueue.LocalQueueName(q.Name)).
				Priority(lowPriority).
				Request(corev1.ResourceCPU, "3").
				Obj()
			util.MustCreate(ctx, k8sClient, wl1)
			util.MustCreate(ctx, k8sClient, wl2)
			util.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, cq.Name, wl1, wl2)

			util.MustCreate(ctx, k8sClient, wl3)
			util.ExpectWorkloadsToBePending(ctx, k8sClient, wl3)
			util.WaitForNextSecondAfterCreation(wl3)
			ginkgo.By("Creating a new Workload")
			wl4 := utiltestingapi.MakeWorkload("wl-4", ns.Name).
				Queue(kueue.LocalQueueName(q.Name)).
				Priority(lowPriority).
				Request(corev1.ResourceCPU, "1").
				Obj()
			util.MustCreate(ctx, k8sClient, wl4)

			util.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, cq.Name, wl1, wl2, wl4)
			util.ExpectWorkloadsToBePending(ctx, k8sClient, wl3)

			ginkgo.By("Finishing the first workload")
			util.FinishWorkloads(ctx, k8sClient, wl1)

			ginkgo.By("Finishing eviction for wl4")
			util.FinishEvictionForWorkloads(ctx, k8sClient, wl4)

			util.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, cq.Name, wl3)
			util.ExpectWorkloadsToBePending(ctx, k8sClient, wl4)
		})

		ginkgo.It("Should include job UID in preemption condition when the label is set", func() {
			ginkgo.By("Creating a low priority Workload")
			lowWl := utiltestingapi.MakeWorkload("low", ns.Name).
				Queue(kueue.LocalQueueName(q.Name)).
				Priority(lowPriority).
				Request(corev1.ResourceCPU, "4").
				Obj()
			util.MustCreate(ctx, k8sClient, lowWl)

			util.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, cq.Name, lowWl)

			ginkgo.By("Creating a high priority Workload with JobUID label")
			highWl := utiltestingapi.MakeWorkload("high", ns.Name).
				Label(constants.JobUIDLabel, "job-uid").
				Queue(kueue.LocalQueueName(q.Name)).
				Priority(highPriority).
				Request(corev1.ResourceCPU, "4").
				Obj()
			util.MustCreate(ctx, k8sClient, highWl)

			cQPath := "/" + cq.Name
			util.ExpectPreemptedCondition(ctx, k8sClient, kueue.InClusterQueueReason, metav1.ConditionTrue, lowWl, highWl, string(highWl.UID), "job-uid", cQPath, cQPath)
		})
	})

	ginkgo.Context("In a ClusterQueue that is part of a cohort", func() {
		var (
			alphaCQ, betaCQ, gammaCQ *kueue.ClusterQueue
			alphaLQ, betaLQ, gammaLQ *kueue.LocalQueue
		)

		ginkgo.BeforeEach(func() {
			alphaCQ = utiltestingapi.MakeClusterQueue("alpha-cq").
				Cohort("all").
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas("alpha").Resource(corev1.ResourceCPU, "2").Obj()).
				Preemption(kueue.ClusterQueuePreemption{
					WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
					ReclaimWithinCohort: kueue.PreemptionPolicyAny,
				}).
				Obj()
			util.MustCreate(ctx, k8sClient, alphaCQ)
			alphaLQ = utiltestingapi.MakeLocalQueue("alpha-q", ns.Name).ClusterQueue(alphaCQ.Name).Obj()
			util.MustCreate(ctx, k8sClient, alphaLQ)

			betaCQ = utiltestingapi.MakeClusterQueue("beta-cq").
				Cohort("all").
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas("alpha").Resource(corev1.ResourceCPU, "2").Obj()).
				Obj()
			util.MustCreate(ctx, k8sClient, betaCQ)
			betaLQ = utiltestingapi.MakeLocalQueue("beta-q", ns.Name).ClusterQueue(betaCQ.Name).Obj()
			util.MustCreate(ctx, k8sClient, betaLQ)

			gammaCQ = utiltestingapi.MakeClusterQueue("gamma-cq").
				Cohort("all").
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas("alpha").Resource(corev1.ResourceCPU, "2").Obj()).
				Preemption(kueue.ClusterQueuePreemption{
					WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
					ReclaimWithinCohort: kueue.PreemptionPolicyAny,
				}).
				Obj()
			util.MustCreate(ctx, k8sClient, gammaCQ)
			gammaLQ = utiltestingapi.MakeLocalQueue("gamma-q", ns.Name).ClusterQueue(gammaCQ.Name).Obj()
			util.MustCreate(ctx, k8sClient, gammaLQ)
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			util.ExpectObjectToBeDeleted(ctx, k8sClient, alphaCQ, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, betaCQ, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, gammaCQ, true)
		})

		ginkgo.It("Should preempt Workloads in the cohort borrowing quota, when the ClusterQueue is using less than nominal quota", func() {
			ginkgo.By("Creating workloads in beta-cq that borrow quota")

			alphaLowWl := utiltestingapi.MakeWorkload("alpha-low", ns.Name).
				Queue(kueue.LocalQueueName(alphaLQ.Name)).
				Priority(lowPriority).
				Request(corev1.ResourceCPU, "1").
				Obj()
			util.MustCreate(ctx, k8sClient, alphaLowWl)

			betaMidWl := utiltestingapi.MakeWorkload("beta-mid", ns.Name).
				Queue(kueue.LocalQueueName(betaLQ.Name)).
				Priority(midPriority).
				Request(corev1.ResourceCPU, "1").
				Obj()
			util.MustCreate(ctx, k8sClient, betaMidWl)
			betaHighWl := utiltestingapi.MakeWorkload("beta-high", ns.Name).
				Queue(kueue.LocalQueueName(betaLQ.Name)).
				Priority(highPriority).
				Request(corev1.ResourceCPU, "4").
				Obj()
			util.MustCreate(ctx, k8sClient, betaHighWl)

			util.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, alphaCQ.Name, alphaLowWl)
			util.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, betaCQ.Name, betaMidWl, betaHighWl)

			ginkgo.By("Creating workload in alpha-cq to preempt workloads in both ClusterQueues")
			alphaMidWl := utiltestingapi.MakeWorkload("alpha-mid", ns.Name).
				Queue(kueue.LocalQueueName(alphaLQ.Name)).
				Priority(midPriority).
				Request(corev1.ResourceCPU, "2").
				Obj()
			util.MustCreate(ctx, k8sClient, alphaMidWl)

			util.FinishEvictionForWorkloads(ctx, k8sClient, alphaLowWl, betaMidWl)

			util.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, alphaCQ.Name, alphaMidWl)
			util.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, betaCQ.Name, betaHighWl)
			util.ExpectWorkloadsToBePending(ctx, k8sClient, alphaLowWl, betaMidWl)

			conditionCmpOpts := cmpopts.IgnoreFields(metav1.Condition{}, "LastTransitionTime")
			alphaCqPath := "/" + string(alphaCQ.Spec.CohortName) + "/" + alphaCQ.Name
			betaCqPath := "/" + string(betaCQ.Spec.CohortName) + "/" + betaCQ.Name

			ginkgo.By("Verify the Preempted condition", func() {
				util.ExpectPreemptedCondition(ctx, k8sClient, kueue.InClusterQueueReason, metav1.ConditionTrue, alphaLowWl, alphaMidWl, string(alphaMidWl.UID), "UNKNOWN", alphaCqPath, alphaCqPath)
				util.ExpectPreemptedCondition(ctx, k8sClient, kueue.InCohortReclamationReason, metav1.ConditionTrue, betaMidWl, alphaMidWl, string(alphaMidWl.UID), "UNKNOWN", alphaCqPath, betaCqPath)
				util.ExpectPreemptedWorkloadsTotalMetric(alphaCQ.Name, kueue.InClusterQueueReason, 1)
				util.ExpectPreemptedWorkloadsTotalMetric(alphaCQ.Name, kueue.InCohortReclamationReason, 1)
				util.ExpectPreemptedWorkloadsTotalMetric(betaCQ.Name, kueue.InClusterQueueReason, 0)
				util.ExpectPreemptedWorkloadsTotalMetric(betaCQ.Name, kueue.InCohortReclamationReason, 0)
			})

			ginkgo.By("Verify the Preempted condition on re-admission, as the preemptor is finished", func() {
				util.FinishWorkloads(ctx, k8sClient, alphaMidWl)
				util.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, alphaCQ.Name, alphaLowWl)
				util.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, betaCQ.Name, betaMidWl, betaHighWl)

				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(alphaLowWl), alphaLowWl)).To(gomega.Succeed())
					g.Expect(apimeta.FindStatusCondition(alphaLowWl.Status.Conditions, kueue.WorkloadPreempted)).To(gomega.BeComparableTo(&metav1.Condition{
						Type:               kueue.WorkloadPreempted,
						Status:             metav1.ConditionFalse,
						ObservedGeneration: alphaLowWl.Generation,
						Reason:             "QuotaReserved",
						Message:            fmt.Sprintf("Previously: Preempted to accommodate a workload (UID: %s, JobUID: UNKNOWN) due to %s; preemptor path: %s; preemptee path: %s", alphaMidWl.UID, preemption.HumanReadablePreemptionReasons[kueue.InClusterQueueReason], alphaCqPath, alphaCqPath),
					}, conditionCmpOpts))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())

				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(betaMidWl), betaMidWl)).To(gomega.Succeed())
					g.Expect(apimeta.FindStatusCondition(betaMidWl.Status.Conditions, kueue.WorkloadPreempted)).To(gomega.BeComparableTo(&metav1.Condition{
						Type:               kueue.WorkloadPreempted,
						Status:             metav1.ConditionFalse,
						ObservedGeneration: betaMidWl.Generation,
						Reason:             "QuotaReserved",
						Message:            fmt.Sprintf("Previously: Preempted to accommodate a workload (UID: %s, JobUID: UNKNOWN) due to %s; preemptor path: %s; preemptee path: %s", alphaMidWl.UID, preemption.HumanReadablePreemptionReasons[kueue.InCohortReclamationReason], alphaCqPath, betaCqPath),
					}, conditionCmpOpts))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("Should not preempt Workloads in the cohort, if the ClusterQueue requires borrowing", func() {
			ginkgo.By("Creating workloads in beta-cq that borrow quota")

			alphaHighWl1 := utiltestingapi.MakeWorkload("alpha-high-1", ns.Name).
				Queue(kueue.LocalQueueName(alphaLQ.Name)).
				Priority(highPriority).
				Request(corev1.ResourceCPU, "2").
				Obj()
			util.MustCreate(ctx, k8sClient, alphaHighWl1)
			betaLowWl := utiltestingapi.MakeWorkload("beta-low", ns.Name).
				Queue(kueue.LocalQueueName(betaLQ.Name)).
				Priority(lowPriority).
				Request(corev1.ResourceCPU, "4").
				Obj()
			util.MustCreate(ctx, k8sClient, betaLowWl)

			util.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, alphaCQ.Name, alphaHighWl1)
			util.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, betaCQ.Name, betaLowWl)

			ginkgo.By("Creating high priority workload in alpha-cq that doesn't fit without borrowing")
			alphaHighWl2 := utiltestingapi.MakeWorkload("alpha-high-2", ns.Name).
				Queue(kueue.LocalQueueName(alphaLQ.Name)).
				Priority(highPriority).
				Request(corev1.ResourceCPU, "2").
				Obj()
			util.MustCreate(ctx, k8sClient, alphaHighWl2)

			// No preemptions happen.
			util.ExpectWorkloadsToBePending(ctx, k8sClient, alphaHighWl2)
			util.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, alphaCQ.Name, alphaHighWl1)
			util.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, betaCQ.Name, betaLowWl)
		})

		ginkgo.It("Should preempt all necessary workloads in concurrent scheduling with different priorities", func() {
			ginkgo.By("Creating workloads in beta-cq that borrow quota")

			betaMidWl := utiltestingapi.MakeWorkload("beta-mid", ns.Name).
				Queue(kueue.LocalQueueName(betaLQ.Name)).
				Priority(midPriority).
				Request(corev1.ResourceCPU, "3").
				Obj()
			util.MustCreate(ctx, k8sClient, betaMidWl)
			betaHighWl := utiltestingapi.MakeWorkload("beta-high", ns.Name).
				Queue(kueue.LocalQueueName(betaLQ.Name)).
				Priority(highPriority).
				Request(corev1.ResourceCPU, "3").
				Obj()
			util.MustCreate(ctx, k8sClient, betaHighWl)

			util.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, betaCQ.Name, betaMidWl, betaHighWl)

			ginkgo.By("Creating workload in alpha-cq and gamma-cq that need to preempt")
			alphaMidWl := utiltestingapi.MakeWorkload("alpha-mid", ns.Name).
				Queue(kueue.LocalQueueName(alphaLQ.Name)).
				Priority(midPriority).
				Request(corev1.ResourceCPU, "2").
				Obj()

			gammaMidWl := utiltestingapi.MakeWorkload("gamma-mid", ns.Name).
				Queue(kueue.LocalQueueName(gammaLQ.Name)).
				Priority(midPriority).
				Request(corev1.ResourceCPU, "2").
				Obj()

			util.MustCreate(ctx, k8sClient, alphaMidWl)
			util.MustCreate(ctx, k8sClient, gammaMidWl)

			// since the two pending workloads are not aware of each other both of them
			// will request the eviction of betaMidWl only
			util.FinishEvictionForWorkloads(ctx, k8sClient, betaMidWl)

			// one of alpha-mid and gamma-mid should be admitted
			util.ExpectWorkloadsToBeAdmittedCount(ctx, k8sClient, 1, alphaMidWl, gammaMidWl)

			// betaHighWl remains admitted
			util.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, betaCQ.Name, betaHighWl)

			// the last one should request the preemption of betaHighWl
			util.FinishEvictionForWorkloads(ctx, k8sClient, betaHighWl)

			util.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, alphaCQ.Name, alphaMidWl)
			util.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, gammaCQ.Name, gammaMidWl)
		})

		ginkgo.It("Should preempt all necessary workloads in concurrent scheduling with the same priority", func() {
			var betaWls []*kueue.Workload
			for i := range 3 {
				wl := utiltestingapi.MakeWorkload(fmt.Sprintf("beta-%d", i), ns.Name).
					Queue(kueue.LocalQueueName(betaLQ.Name)).
					Request(corev1.ResourceCPU, "2").
					Obj()
				util.MustCreate(ctx, k8sClient, wl)
				betaWls = append(betaWls, wl)
			}
			util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, betaWls...)

			ginkgo.By("Creating preempting pods")

			alphaWl := utiltestingapi.MakeWorkload("alpha", ns.Name).
				Queue(kueue.LocalQueueName(alphaLQ.Name)).
				Request(corev1.ResourceCPU, "2").
				Obj()
			util.MustCreate(ctx, k8sClient, alphaWl)

			gammaWl := utiltestingapi.MakeWorkload("gamma", ns.Name).
				Queue(kueue.LocalQueueName(gammaLQ.Name)).
				Request(corev1.ResourceCPU, "2").
				Obj()
			util.MustCreate(ctx, k8sClient, gammaWl)

			var evictedWorkloads []*kueue.Workload
			gomega.Eventually(func(g gomega.Gomega) {
				evictedWorkloads = util.FilterEvictedWorkloads(ctx, k8sClient, betaWls...)
				g.Expect(evictedWorkloads).Should(gomega.HaveLen(1), "Number of evicted workloads")
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			ginkgo.By("Finishing eviction for first set of preempted workloads")
			util.FinishEvictionForWorkloads(ctx, k8sClient, evictedWorkloads...)
			util.ExpectWorkloadsToBeAdmittedCount(ctx, k8sClient, 1, alphaWl, gammaWl)

			gomega.Eventually(func(g gomega.Gomega) {
				evictedWorkloads = util.FilterEvictedWorkloads(ctx, k8sClient, betaWls...)
				g.Expect(evictedWorkloads).Should(gomega.HaveLen(2), "Number of evicted workloads")
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			ginkgo.By("Finishing eviction for second set of preempted workloads")
			util.FinishEvictionForWorkloads(ctx, k8sClient, evictedWorkloads...)
			util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, alphaWl, gammaWl)
			util.ExpectWorkloadsToBeAdmittedCount(ctx, k8sClient, 1, betaWls...)
		})
	})

	ginkgo.Context("In a cohort with StrictFIFO", func() {
		var (
			alphaCQ, betaCQ *kueue.ClusterQueue
			alphaLQ, betaLQ *kueue.LocalQueue
			oneFlavor       *kueue.ResourceFlavor
		)

		ginkgo.BeforeEach(func() {
			oneFlavor = utiltestingapi.MakeResourceFlavor("one").Obj()
			util.MustCreate(ctx, k8sClient, oneFlavor)

			alphaCQ = utiltestingapi.MakeClusterQueue("alpha-cq").
				Cohort("all").
				QueueingStrategy(kueue.StrictFIFO).
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas("one").Resource(corev1.ResourceCPU, "2").Obj()).
				Preemption(kueue.ClusterQueuePreemption{
					WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
					ReclaimWithinCohort: kueue.PreemptionPolicyAny,
				}).
				Obj()
			util.MustCreate(ctx, k8sClient, alphaCQ)
			alphaLQ = utiltestingapi.MakeLocalQueue("alpha-lq", ns.Name).ClusterQueue("alpha-cq").Obj()
			util.MustCreate(ctx, k8sClient, alphaLQ)
			betaCQ = utiltestingapi.MakeClusterQueue("beta-cq").
				Cohort("all").
				QueueingStrategy(kueue.StrictFIFO).
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas("one").Resource(corev1.ResourceCPU, "2").Obj()).
				Preemption(kueue.ClusterQueuePreemption{
					WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
					ReclaimWithinCohort: kueue.PreemptionPolicyAny,
				}).
				Obj()
			util.MustCreate(ctx, k8sClient, betaCQ)
			betaLQ = utiltestingapi.MakeLocalQueue("beta-lq", ns.Name).ClusterQueue("beta-cq").Obj()
			util.MustCreate(ctx, k8sClient, betaLQ)
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			gomega.Expect(util.DeleteObject(ctx, k8sClient, alphaLQ)).To(gomega.Succeed())
			util.ExpectObjectToBeDeleted(ctx, k8sClient, alphaCQ, true)
			gomega.Expect(util.DeleteObject(ctx, k8sClient, betaLQ)).To(gomega.Succeed())
			util.ExpectObjectToBeDeleted(ctx, k8sClient, betaCQ, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, oneFlavor, true)
		})

		ginkgo.It("Should reclaim from cohort even if another CQ has pending workloads", func() {
			useAllAlphaWl := utiltestingapi.MakeWorkload("use-all", ns.Name).
				Queue("alpha-lq").
				Priority(1).
				Request(corev1.ResourceCPU, "4").
				Obj()
			util.MustCreate(ctx, k8sClient, useAllAlphaWl)
			util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, useAllAlphaWl)

			pendingAlphaWl := utiltestingapi.MakeWorkload("pending", ns.Name).
				Queue("alpha-lq").
				Priority(0).
				Request(corev1.ResourceCPU, "1").
				Obj()
			util.MustCreate(ctx, k8sClient, pendingAlphaWl)
			util.ExpectWorkloadsToBePending(ctx, k8sClient, pendingAlphaWl)

			ginkgo.By("Creating a workload to reclaim quota")

			preemptorBetaWl := utiltestingapi.MakeWorkload("preemptor", ns.Name).
				Queue("beta-lq").
				Priority(-1).
				Request(corev1.ResourceCPU, "1").
				Obj()
			util.MustCreate(ctx, k8sClient, preemptorBetaWl)
			util.ExpectWorkloadsToBePreempted(ctx, k8sClient, useAllAlphaWl)
			util.FinishEvictionForWorkloads(ctx, k8sClient, useAllAlphaWl)
			util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, preemptorBetaWl)
			util.ExpectWorkloadsToBePending(ctx, k8sClient, useAllAlphaWl, pendingAlphaWl)
		})
	})

	ginkgo.Context("When most quota is in a shared ClusterQueue in a cohort", func() {
		var (
			aStandardCQ, aBestEffortCQ, bStandardCQ, bBestEffortCQ, sharedCQ *kueue.ClusterQueue
			aStandardLQ, aBestEffortLQ, bStandardLQ, bBestEffortLQ           *kueue.LocalQueue
			oneFlavor, fallbackFlavor                                        *kueue.ResourceFlavor
		)

		ginkgo.BeforeEach(func() {
			oneFlavor = utiltestingapi.MakeResourceFlavor("one").Obj()
			util.MustCreate(ctx, k8sClient, oneFlavor)

			fallbackFlavor = utiltestingapi.MakeResourceFlavor("fallback").Obj()
			util.MustCreate(ctx, k8sClient, fallbackFlavor)

			aStandardCQ = utiltestingapi.MakeClusterQueue("a-standard-cq").
				Cohort("all").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("one").Resource(corev1.ResourceCPU, "1", "10").Obj(),
					*utiltestingapi.MakeFlavorQuotas("fallback").Resource(corev1.ResourceCPU, "10", "0").Obj(),
				).
				FlavorFungibility(kueue.FlavorFungibility{
					WhenCanBorrow:  kueue.MayStopSearch,
					WhenCanPreempt: kueue.MayStopSearch,
				}).
				Preemption(kueue.ClusterQueuePreemption{
					ReclaimWithinCohort: kueue.PreemptionPolicyLowerPriority,
					BorrowWithinCohort: &kueue.BorrowWithinCohort{
						Policy:               kueue.BorrowWithinCohortPolicyLowerPriority,
						MaxPriorityThreshold: ptr.To(midPriority),
					},
				}).
				Obj()
			util.MustCreate(ctx, k8sClient, aStandardCQ)
			aStandardLQ = utiltestingapi.MakeLocalQueue("a-standard-lq", ns.Name).ClusterQueue(aStandardCQ.Name).Obj()
			util.MustCreate(ctx, k8sClient, aStandardLQ)

			aBestEffortCQ = utiltestingapi.MakeClusterQueue("a-best-effort-cq").
				Cohort("all").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("one").Resource(corev1.ResourceCPU, "1", "10").Obj(),
					*utiltestingapi.MakeFlavorQuotas("fallback").Resource(corev1.ResourceCPU, "10", "0").Obj(),
				).
				FlavorFungibility(kueue.FlavorFungibility{
					WhenCanBorrow:  kueue.MayStopSearch,
					WhenCanPreempt: kueue.MayStopSearch,
				}).
				Preemption(kueue.ClusterQueuePreemption{
					ReclaimWithinCohort: kueue.PreemptionPolicyLowerPriority,
					BorrowWithinCohort: &kueue.BorrowWithinCohort{
						Policy: kueue.BorrowWithinCohortPolicyLowerPriority,
					},
				}).
				Obj()
			util.MustCreate(ctx, k8sClient, aBestEffortCQ)
			aBestEffortLQ = utiltestingapi.MakeLocalQueue("a-best-effort-lq", ns.Name).ClusterQueue(aBestEffortCQ.Name).Obj()
			util.MustCreate(ctx, k8sClient, aBestEffortLQ)

			bBestEffortCQ = utiltestingapi.MakeClusterQueue("b-best-effort-cq").
				Cohort("all").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("one").Resource(corev1.ResourceCPU, "1", "10").Obj(),
					*utiltestingapi.MakeFlavorQuotas("fallback").Resource(corev1.ResourceCPU, "10", "0").Obj(),
				).
				FlavorFungibility(kueue.FlavorFungibility{
					WhenCanBorrow:  kueue.MayStopSearch,
					WhenCanPreempt: kueue.MayStopSearch,
				}).
				Preemption(kueue.ClusterQueuePreemption{
					ReclaimWithinCohort: kueue.PreemptionPolicyLowerPriority,
					BorrowWithinCohort: &kueue.BorrowWithinCohort{
						Policy: kueue.BorrowWithinCohortPolicyLowerPriority,
					},
				}).
				Obj()
			util.MustCreate(ctx, k8sClient, bBestEffortCQ)
			bBestEffortLQ = utiltestingapi.MakeLocalQueue("b-best-effort-lq", ns.Name).ClusterQueue(bBestEffortCQ.Name).Obj()
			util.MustCreate(ctx, k8sClient, bBestEffortLQ)

			bStandardCQ = utiltestingapi.MakeClusterQueue("b-standard-cq").
				Cohort("all").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("one").Resource(corev1.ResourceCPU, "1", "10").Obj(),
					*utiltestingapi.MakeFlavorQuotas("fallback").Resource(corev1.ResourceCPU, "10", "0").Obj(),
				).
				FlavorFungibility(kueue.FlavorFungibility{
					WhenCanBorrow:  kueue.MayStopSearch,
					WhenCanPreempt: kueue.MayStopSearch,
				}).
				Preemption(kueue.ClusterQueuePreemption{
					ReclaimWithinCohort: kueue.PreemptionPolicyLowerPriority,
					BorrowWithinCohort: &kueue.BorrowWithinCohort{
						Policy:               kueue.BorrowWithinCohortPolicyLowerPriority,
						MaxPriorityThreshold: ptr.To(midPriority),
					},
				}).
				Obj()
			util.MustCreate(ctx, k8sClient, bStandardCQ)
			bStandardLQ = utiltestingapi.MakeLocalQueue("b-standard-lq", ns.Name).ClusterQueue(bStandardCQ.Name).Obj()
			util.MustCreate(ctx, k8sClient, bStandardLQ)

			sharedCQ = utiltestingapi.MakeClusterQueue("shared-cq").
				Cohort("all").
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas("one").Resource(corev1.ResourceCPU, "10").Obj()).
				Obj()
			util.MustCreate(ctx, k8sClient, sharedCQ)
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			util.ExpectObjectToBeDeleted(ctx, k8sClient, aStandardCQ, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, aBestEffortCQ, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, bBestEffortCQ, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, bStandardCQ, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, sharedCQ, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, oneFlavor, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, fallbackFlavor, true)
		})

		ginkgo.It("should allow preempting workloads while borrowing", func() {
			ginkgo.By("Create a low priority workload which requires borrowing")
			aBestEffortLowWl := utiltestingapi.MakeWorkload("a-best-effort-low", ns.Name).
				Queue(kueue.LocalQueueName(aBestEffortLQ.Name)).
				Priority(lowPriority).
				Request(corev1.ResourceCPU, "5").
				Obj()
			util.MustCreate(ctx, k8sClient, aBestEffortLowWl)

			ginkgo.By("Await for the a-best-effort-low workload to be admitted")
			util.ExpectWorkloadToBeAdmittedAs(ctx, k8sClient, aBestEffortLowWl,
				utiltestingapi.MakeAdmission(aBestEffortCQ.Name).PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).Assignment(corev1.ResourceCPU, "one", "5").Obj()).Obj(),
			)

			ginkgo.By("Create a low priority workload which is not borrowing")
			bBestEffortLowWl := utiltestingapi.MakeWorkload("b-best-effort-low", ns.Name).
				Queue(kueue.LocalQueueName(bBestEffortLQ.Name)).
				Priority(lowPriority).
				Request(corev1.ResourceCPU, "1").
				Obj()
			util.MustCreate(ctx, k8sClient, bBestEffortLowWl)

			ginkgo.By("Await for the b-best-effort-low workload to be admitted")
			util.ExpectWorkloadToBeAdmittedAs(ctx, k8sClient, bBestEffortLowWl,
				utiltestingapi.MakeAdmission(bBestEffortCQ.Name).PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).Assignment(corev1.ResourceCPU, "one", "1").Obj()).Obj(),
			)

			ginkgo.By("Create a high priority workload (above MaxPriorityThreshold) which requires borrowing")
			bStandardWl := utiltestingapi.MakeWorkload("b-standard-high", ns.Name).
				Queue(kueue.LocalQueueName(bStandardLQ.Name)).
				Priority(highPriority).
				Request(corev1.ResourceCPU, "5").
				Obj()
			util.MustCreate(ctx, k8sClient, bStandardWl)

			ginkgo.By("Await for the b-standard-high workload to be admitted")
			util.ExpectWorkloadToBeAdmittedAs(ctx, k8sClient, bStandardWl,
				utiltestingapi.MakeAdmission(bStandardCQ.Name).PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).Assignment(corev1.ResourceCPU, "one", "5").Obj()).Obj(),
			)

			ginkgo.By("Create the a-standard-very-high workload")
			aStandardVeryHighWl := utiltestingapi.MakeWorkload("a-standard-very-high", ns.Name).
				Queue(kueue.LocalQueueName(aStandardLQ.Name)).
				Priority(veryHighPriority).
				Request(corev1.ResourceCPU, "7").
				Obj()
			util.MustCreate(ctx, k8sClient, aStandardVeryHighWl)

			aStandardCQPath := "/" + string(aStandardCQ.Spec.CohortName) + "/" + aStandardCQ.Name
			aBestEffortCQPath := "/" + string(aBestEffortCQ.Spec.CohortName) + "/" + aBestEffortCQ.Name
			util.ExpectPreemptedCondition(ctx, k8sClient, kueue.InCohortReclaimWhileBorrowingReason, metav1.ConditionTrue, aBestEffortLowWl, aStandardVeryHighWl, string(aStandardVeryHighWl.UID), "UNKNOWN", aStandardCQPath, aBestEffortCQPath)
			util.ExpectPreemptedWorkloadsTotalMetric(aStandardCQ.Name, kueue.InCohortReclaimWhileBorrowingReason, 1)
			util.ExpectPreemptedWorkloadsTotalMetric(aBestEffortCQ.Name, kueue.InCohortReclaimWhileBorrowingReason, 0)

			ginkgo.By("Finish eviction fo the a-best-effort-low workload")
			util.FinishEvictionForWorkloads(ctx, k8sClient, aBestEffortLowWl)

			ginkgo.By("Verify the a-standard-very-high workload is admitted")
			util.ExpectWorkloadToBeAdmittedAs(ctx, k8sClient, aStandardVeryHighWl,
				utiltestingapi.MakeAdmission(aStandardCQ.Name).PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).Assignment(corev1.ResourceCPU, "one", "7").Obj()).Obj(),
			)

			ginkgo.By("Verify the a-best-effort-low workload is re-admitted, but using flavor 2")
			util.ExpectWorkloadToBeAdmittedAs(ctx, k8sClient, aBestEffortLowWl,
				utiltestingapi.MakeAdmission(aBestEffortCQ.Name).PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).Assignment(corev1.ResourceCPU, "fallback", "5").Obj()).Obj(),
			)

			ginkgo.By("Verify the b-standard-high workload remains admitted")
			util.ExpectWorkloadToBeAdmittedAs(ctx, k8sClient, bStandardWl,
				utiltestingapi.MakeAdmission(bStandardCQ.Name).PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).Assignment(corev1.ResourceCPU, "one", "5").Obj()).Obj(),
			)

			ginkgo.By("Verify for the b-best-effort-low workload remains admitted")
			util.ExpectWorkloadToBeAdmittedAs(ctx, k8sClient, bBestEffortLowWl,
				utiltestingapi.MakeAdmission(bBestEffortCQ.Name).PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).Assignment(corev1.ResourceCPU, "one", "1").Obj()).Obj(),
			)
		})
	})

	ginkgo.Context("With lending limit", func() {
		var (
			prodCQ *kueue.ClusterQueue
			devCQ  *kueue.ClusterQueue
		)

		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			util.ExpectObjectToBeDeleted(ctx, k8sClient, prodCQ, true)
			if devCQ != nil {
				util.ExpectObjectToBeDeleted(ctx, k8sClient, devCQ, true)
			}
		})

		ginkgo.It("Should be able to preempt when lending limit enabled", func() {
			prodCQ = utiltestingapi.MakeClusterQueue("prod-cq").
				Cohort("all").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("alpha").Resource(corev1.ResourceCPU, "5", "", "4").Obj(),
				).
				Preemption(kueue.ClusterQueuePreemption{
					ReclaimWithinCohort: kueue.PreemptionPolicyAny,
				}).
				Obj()
			util.MustCreate(ctx, k8sClient, prodCQ)

			devCQ = utiltestingapi.MakeClusterQueue("dev-cq").
				Cohort("all").
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas("alpha").Resource(corev1.ResourceCPU, "5").Obj()).
				Obj()
			util.MustCreate(ctx, k8sClient, devCQ)

			prodQueue := utiltestingapi.MakeLocalQueue("prod-queue", ns.Name).ClusterQueue(prodCQ.Name).Obj()
			util.MustCreate(ctx, k8sClient, prodQueue)

			devQueue := utiltestingapi.MakeLocalQueue("dev-queue", ns.Name).ClusterQueue(devCQ.Name).Obj()
			util.MustCreate(ctx, k8sClient, devQueue)

			ginkgo.By("Creating two workloads")
			wl1 := utiltestingapi.MakeWorkload("wl-1", ns.Name).Priority(0).Queue(kueue.LocalQueueName(devQueue.Name)).Request(corev1.ResourceCPU, "4").Obj()
			wl2 := utiltestingapi.MakeWorkload("wl-2", ns.Name).Priority(1).Queue(kueue.LocalQueueName(devQueue.Name)).Request(corev1.ResourceCPU, "5").Obj()
			util.MustCreate(ctx, k8sClient, wl1)
			util.MustCreate(ctx, k8sClient, wl2)
			util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1, wl2)

			ginkgo.By("Creating another workload")
			wl3 := utiltestingapi.MakeWorkload("wl-3", ns.Name).Queue(kueue.LocalQueueName(prodQueue.Name)).Request(corev1.ResourceCPU, "4").Obj()
			util.MustCreate(ctx, k8sClient, wl3)
			util.ExpectWorkloadsToBePreempted(ctx, k8sClient, wl1)

			util.FinishEvictionForWorkloads(ctx, k8sClient, wl1)

			util.ExpectWorkloadToBeAdmittedAs(ctx, k8sClient, wl3,
				utiltestingapi.MakeAdmission(prodCQ.Name).PodSets(utiltestingapi.MakePodSetAssignment(kueue.DefaultPodSetName).Assignment(corev1.ResourceCPU, "alpha", "4").Obj()).Obj())
		})
	})

	ginkgo.Context("When borrowWithinCohort is used and PrioritySortingWithinCohort disabled", func() {
		var (
			aCQ, bCQ, cCQ *kueue.ClusterQueue
			aLQ, bLQ      *kueue.LocalQueue
			defaultFlavor *kueue.ResourceFlavor
		)

		ginkgo.BeforeEach(func() {
			features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.PrioritySortingWithinCohort, false)
			defaultFlavor = utiltestingapi.MakeResourceFlavor("default").Obj()
			util.MustCreate(ctx, k8sClient, defaultFlavor)

			aCQ = utiltestingapi.MakeClusterQueue("a-cq").
				Cohort("all").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "0", "10").Obj(),
				).
				FlavorFungibility(kueue.FlavorFungibility{
					WhenCanBorrow:  kueue.MayStopSearch,
					WhenCanPreempt: kueue.MayStopSearch,
				}).
				Preemption(kueue.ClusterQueuePreemption{
					ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
				}).
				Obj()
			util.MustCreate(ctx, k8sClient, aCQ)
			aLQ = utiltestingapi.MakeLocalQueue("a-lq", ns.Name).ClusterQueue(aCQ.Name).Obj()
			util.MustCreate(ctx, k8sClient, aLQ)

			bCQ = utiltestingapi.MakeClusterQueue("b-cq").
				Cohort("all").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "5", "5").Obj(),
				).
				FlavorFungibility(kueue.FlavorFungibility{
					WhenCanBorrow:  kueue.MayStopSearch,
					WhenCanPreempt: kueue.MayStopSearch,
				}).
				Preemption(kueue.ClusterQueuePreemption{
					ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
					BorrowWithinCohort: &kueue.BorrowWithinCohort{
						Policy:               kueue.BorrowWithinCohortPolicyLowerPriority,
						MaxPriorityThreshold: ptr.To(veryHighPriority),
					},
				}).
				Obj()
			util.MustCreate(ctx, k8sClient, bCQ)
			bLQ = utiltestingapi.MakeLocalQueue("b-lq", ns.Name).ClusterQueue(bCQ.Name).Obj()
			util.MustCreate(ctx, k8sClient, bLQ)

			cCQ = utiltestingapi.MakeClusterQueue("c-cq").
				Cohort("all").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "5").Obj(),
				).
				FlavorFungibility(kueue.FlavorFungibility{
					WhenCanBorrow:  kueue.MayStopSearch,
					WhenCanPreempt: kueue.MayStopSearch,
				}).
				Preemption(kueue.ClusterQueuePreemption{
					ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
				}).
				Obj()
			util.MustCreate(ctx, k8sClient, cCQ)
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			util.ExpectObjectToBeDeleted(ctx, k8sClient, aCQ, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, bCQ, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, cCQ, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, defaultFlavor, true)
		})

		ginkgo.It("should allow preempting workloads while borrowing", func() {
			var aWl, b1Wl, b2Wl *kueue.Workload

			ginkgo.By("Create a mid priority workload in aCQ and await for admission", func() {
				aWl = utiltestingapi.MakeWorkload("a-low", ns.Name).
					Queue(kueue.LocalQueueName(aLQ.Name)).
					Priority(midPriority).
					Request(corev1.ResourceCPU, "4").
					Obj()
				util.MustCreate(ctx, k8sClient, aWl)
				util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, aWl)
			})

			ginkgo.By("Create a high priority workload b1 in bCQ and await for admission", func() {
				b1Wl = utiltestingapi.MakeWorkload("b1-high", ns.Name).
					Queue(kueue.LocalQueueName(bLQ.Name)).
					Priority(highPriority).
					Request(corev1.ResourceCPU, "4").
					Obj()
				util.MustCreate(ctx, k8sClient, b1Wl)
				util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, b1Wl)
			})

			ginkgo.By("Create a high priority workload b2 in bCQ", func() {
				b2Wl = utiltestingapi.MakeWorkload("b2-high", ns.Name).
					Queue(kueue.LocalQueueName(bLQ.Name)).
					Priority(highPriority).
					Request(corev1.ResourceCPU, "4").
					Obj()
				util.MustCreate(ctx, k8sClient, b2Wl)
			})

			ginkgo.By("Await for preemption of the workload in aCQ and admission of b2", func() {
				util.FinishEvictionForWorkloads(ctx, k8sClient, aWl)
				util.ExpectWorkloadsToBePending(ctx, k8sClient, aWl)
				util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, b2Wl)
			})
		})
	})

	ginkgo.Context("In a multi-level cohort", func() {
		var rootCohort, guaranteedCohort *kueue.Cohort
		var bestEffortCQ, guaranteedCQ *kueue.ClusterQueue
		var defaultFlavor *kueue.ResourceFlavor

		ginkgo.BeforeEach(func() {
			defaultFlavor = utiltestingapi.MakeResourceFlavor("default").Obj()
			gomega.Expect(k8sClient.Create(ctx, defaultFlavor)).To(gomega.Succeed())

			rootCohort = utiltestingapi.MakeCohort("root").Obj()
			gomega.Expect(k8sClient.Create(ctx, rootCohort)).To(gomega.Succeed())
			guaranteedCohort = utiltestingapi.MakeCohort("guaranteed").Parent("root").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "3").Obj(),
				).Obj()
			gomega.Expect(k8sClient.Create(ctx, guaranteedCohort)).To(gomega.Succeed())
			bestEffortCQ = utiltestingapi.MakeClusterQueue("best-effort").
				Cohort("root").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "0").Obj(),
				).
				Preemption(kueue.ClusterQueuePreemption{
					WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
					ReclaimWithinCohort: kueue.PreemptionPolicyAny,
				}).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, bestEffortCQ)).To(gomega.Succeed())
			guaranteedCQ = utiltestingapi.MakeClusterQueue("guaranteed").
				Cohort("guaranteed").ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "0").Obj(),
			).
				Preemption(kueue.ClusterQueuePreemption{
					WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
					ReclaimWithinCohort: kueue.PreemptionPolicyAny,
				}).
				Obj()
			gomega.Expect(k8sClient.Create(ctx, guaranteedCQ)).To(gomega.Succeed())
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			util.ExpectObjectToBeDeleted(ctx, k8sClient, bestEffortCQ, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, guaranteedCQ, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, rootCohort, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, defaultFlavor, true)
		})

		ginkgo.It("workloads in guaranteed cq should have preferential access to the resources", func() {
			var bestEffortLQ *kueue.LocalQueue
			var guaranteedLQ *kueue.LocalQueue
			var bestEffortWl, guaranteedWl *kueue.Workload

			ginkgo.By("Create local queues", func() {
				bestEffortLQ = utiltestingapi.MakeLocalQueue("best-effort-lq", ns.Name).ClusterQueue(bestEffortCQ.Name).Obj()
				gomega.Expect(k8sClient.Create(ctx, bestEffortLQ)).Should(gomega.Succeed())
				guaranteedLQ = utiltestingapi.MakeLocalQueue("guaranteed-lq", ns.Name).ClusterQueue(guaranteedCQ.Name).Obj()
				gomega.Expect(k8sClient.Create(ctx, guaranteedLQ)).Should(gomega.Succeed())
			})

			ginkgo.By("Create a workload in bestEffortCQ and await for admission", func() {
				bestEffortWl = utiltestingapi.MakeWorkload("best-effort-wl", ns.Name).
					Queue(kueue.LocalQueueName(bestEffortLQ.Name)).
					Priority(highPriority).
					Request(corev1.ResourceCPU, "3").
					Obj()
				gomega.Expect(k8sClient.Create(ctx, bestEffortWl)).To(gomega.Succeed())
				util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, bestEffortWl)
			})

			ginkgo.By("Create a workload in guaranteedCQ", func() {
				guaranteedWl = utiltestingapi.MakeWorkload("guaranteed-wl", ns.Name).
					Queue(kueue.LocalQueueName(guaranteedLQ.Name)).
					Priority(midPriority).
					Request(corev1.ResourceCPU, "3").
					Obj()
				gomega.Expect(k8sClient.Create(ctx, guaranteedWl)).To(gomega.Succeed())
			})

			ginkgo.By("Await for preemption of the workload in bestEffortCQ and admission of guaranteedWL", func() {
				util.ExpectWorkloadsToBePreempted(ctx, k8sClient, bestEffortWl)
				util.FinishEvictionForWorkloads(ctx, k8sClient, bestEffortWl)
				util.ExpectWorkloadsToBePending(ctx, k8sClient, bestEffortWl)
				util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, guaranteedWl)
			})
		})
	})

	ginkgo.Context("ElasticJobs In a single ClusterQueue", func() {
		var (
			cq *kueue.ClusterQueue
			q  *kueue.LocalQueue
		)

		ginkgo.BeforeEach(func() {
			cq = utiltestingapi.MakeClusterQueue("cq").
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas("alpha").Resource(corev1.ResourceCPU, "2").Obj()).
				Preemption(kueue.ClusterQueuePreemption{
					WithinClusterQueue: kueue.PreemptionPolicyLowerOrNewerEqualPriority,
				}).
				Obj()
			util.MustCreate(ctx, k8sClient, cq)

			q = utiltestingapi.MakeLocalQueue("q", ns.Name).ClusterQueue(cq.Name).Obj()
			util.MustCreate(ctx, k8sClient, q)
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			util.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
		})

		ginkgo.It("Should preempt on-scale-up Workloads with lower priority when there is not enough quota", func() {
			features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.ElasticJobsViaWorkloadSlices, true)
			ginkgo.By("Creating low priority workload")
			lowWl := utiltestingapi.MakeWorkload("low", ns.Name).
				Queue(kueue.LocalQueueName(q.Name)).
				Annotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
				Priority(lowPriority).
				Request(corev1.ResourceCPU, "1").
				Obj()
			util.MustCreate(ctx, k8sClient, lowWl)
			util.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, cq.Name, lowWl)

			ginkgo.By("Creating a high priority workload")
			highWl := utiltestingapi.MakeWorkload("high", ns.Name).
				Queue(kueue.LocalQueueName(q.Name)).
				Annotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
				Priority(highPriority).
				Request(corev1.ResourceCPU, "1").
				Obj()
			util.MustCreate(ctx, k8sClient, highWl)
			util.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, cq.Name, lowWl)

			ginkgo.By("Scaling up a high priority workload")
			highWl.Spec.PodSets[0].Count = 2 // Scaled up.
			highWlScaledUp := utiltestingapi.MakeWorkload("high-2", ns.Name).
				Queue(kueue.LocalQueueName(q.Name)).
				Annotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
				Annotation(workloadslicing.WorkloadSliceReplacementFor, string(workload.Key(highWl))).
				PodSets(highWl.Spec.PodSets...).
				Obj()
			util.MustCreate(ctx, k8sClient, highWlScaledUp)
			util.ExpectWorkloadsToBePreempted(ctx, k8sClient, lowWl)
			util.FinishEvictionForWorkloads(ctx, k8sClient, lowWl)
			util.ExpectWorkloadToFinish(ctx, k8sClient, client.ObjectKeyFromObject(highWl))
			util.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, cq.Name, highWlScaledUp)

			ginkgo.By("Scale down a high priority workload")
			gomega.Eventually(func(g gomega.Gomega) {
				wl := &kueue.Workload{}
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(highWlScaledUp), wl)).To(gomega.Succeed())
				wl.Spec.PodSets[0].Count = 1 // Scaled down.
				g.Expect(k8sClient.Update(ctx, wl)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
			util.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, cq.Name, lowWl)
			util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, lowWl)
		})
	})
})
