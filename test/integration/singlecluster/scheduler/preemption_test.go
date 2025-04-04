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

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/scheduler/preemption"
	"sigs.k8s.io/kueue/pkg/util/testing"
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
		alphaFlavor = testing.MakeResourceFlavor("alpha").Obj()
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
			cq = testing.MakeClusterQueue("cq").
				ResourceGroup(*testing.MakeFlavorQuotas("alpha").Resource(corev1.ResourceCPU, "4").Obj()).
				Preemption(kueue.ClusterQueuePreemption{
					WithinClusterQueue: kueue.PreemptionPolicyLowerOrNewerEqualPriority,
				}).
				Obj()
			util.MustCreate(ctx, k8sClient, cq)

			q = testing.MakeLocalQueue("q", ns.Name).ClusterQueue(cq.Name).Obj()
			util.MustCreate(ctx, k8sClient, q)
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			util.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
		})

		ginkgo.It("Should preempt Workloads with lower priority when there is not enough quota", func() {
			ginkgo.By("Creating initial Workloads with different priorities")
			lowWl1 := testing.MakeWorkload("low-wl-1", ns.Name).
				Queue(q.Name).
				Priority(lowPriority).
				Request(corev1.ResourceCPU, "1").
				Obj()
			lowWl2 := testing.MakeWorkload("low-wl-2", ns.Name).
				Queue(q.Name).
				Priority(lowPriority).
				Request(corev1.ResourceCPU, "1").
				Obj()
			midWl := testing.MakeWorkload("mid-wl", ns.Name).
				Queue(q.Name).
				Priority(midPriority).
				Request(corev1.ResourceCPU, "1").
				Obj()
			highWl1 := testing.MakeWorkload("high-wl-1", ns.Name).
				Queue(q.Name).
				Priority(highPriority).
				Request(corev1.ResourceCPU, "1").
				Obj()
			util.MustCreate(ctx, k8sClient, lowWl1)
			util.MustCreate(ctx, k8sClient, lowWl2)
			util.MustCreate(ctx, k8sClient, midWl)
			util.MustCreate(ctx, k8sClient, highWl1)

			util.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, cq.Name, lowWl1, lowWl2, midWl, highWl1)

			ginkgo.By("Creating a low priority Workload")
			lowWl3 := testing.MakeWorkload("low-wl-3", ns.Name).
				Queue(q.Name).
				Priority(lowPriority).
				Request(corev1.ResourceCPU, "1").
				Obj()
			util.MustCreate(ctx, k8sClient, lowWl3)

			util.ExpectWorkloadsToBePending(ctx, k8sClient, lowWl3)

			ginkgo.By("Creating a high priority Workload")
			highWl2 := testing.MakeWorkload("high-wl-2", ns.Name).
				Queue(q.Name).
				Priority(highPriority).
				Request(corev1.ResourceCPU, "2").
				Obj()
			util.MustCreate(ctx, k8sClient, highWl2)

			util.FinishEvictionForWorkloads(ctx, k8sClient, lowWl1, lowWl2)
			util.ExpectEvictedWorkloadsTotalMetric(cq.Name, kueue.WorkloadEvictedByPreemption, 2)
			util.ExpectPreemptedWorkloadsTotalMetric(cq.Name, kueue.InClusterQueueReason, 2)

			util.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, cq.Name, highWl2)
			util.ExpectWorkloadsToBePending(ctx, k8sClient, lowWl1, lowWl2)
		})

		ginkgo.It("Should preempt newer Workloads with the same priority when there is not enough quota", func() {
			ginkgo.By("Creating initial Workloads")
			wl1 := testing.MakeWorkload("wl-1", ns.Name).
				Queue(q.Name).
				Priority(lowPriority).
				Request(corev1.ResourceCPU, "1").
				Obj()
			wl2 := testing.MakeWorkload("wl-2", ns.Name).
				Queue(q.Name).
				Priority(lowPriority).
				Request(corev1.ResourceCPU, "1").
				Obj()
			wl3 := testing.MakeWorkload("wl-3", ns.Name).
				Queue(q.Name).
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
			wl4 := testing.MakeWorkload("wl-4", ns.Name).
				Queue(q.Name).
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
			lowWl := testing.MakeWorkload("low", ns.Name).
				Queue(q.Name).
				Priority(lowPriority).
				Request(corev1.ResourceCPU, "4").
				Obj()
			util.MustCreate(ctx, k8sClient, lowWl)

			util.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, cq.Name, lowWl)

			ginkgo.By("Creating a high priority Workload with JobUID label")
			highWl := testing.MakeWorkload("high", ns.Name).
				Label(constants.JobUIDLabel, "job-uid").
				Queue(q.Name).
				Priority(highPriority).
				Request(corev1.ResourceCPU, "4").
				Obj()
			util.MustCreate(ctx, k8sClient, highWl)

			util.ExpectPreemptedCondition(ctx, k8sClient, kueue.InClusterQueueReason, metav1.ConditionTrue, lowWl, highWl, string(highWl.UID), "job-uid")
		})
	})

	ginkgo.Context("In a ClusterQueue that is part of a cohort", func() {
		var (
			alphaCQ, betaCQ, gammaCQ *kueue.ClusterQueue
			alphaLQ, betaLQ, gammaLQ *kueue.LocalQueue
		)

		ginkgo.BeforeEach(func() {
			alphaCQ = testing.MakeClusterQueue("alpha-cq").
				Cohort("all").
				ResourceGroup(*testing.MakeFlavorQuotas("alpha").Resource(corev1.ResourceCPU, "2").Obj()).
				Preemption(kueue.ClusterQueuePreemption{
					WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
					ReclaimWithinCohort: kueue.PreemptionPolicyAny,
				}).
				Obj()
			util.MustCreate(ctx, k8sClient, alphaCQ)
			alphaLQ = testing.MakeLocalQueue("alpha-q", ns.Name).ClusterQueue(alphaCQ.Name).Obj()
			util.MustCreate(ctx, k8sClient, alphaLQ)

			betaCQ = testing.MakeClusterQueue("beta-cq").
				Cohort("all").
				ResourceGroup(*testing.MakeFlavorQuotas("alpha").Resource(corev1.ResourceCPU, "2").Obj()).
				Obj()
			util.MustCreate(ctx, k8sClient, betaCQ)
			betaLQ = testing.MakeLocalQueue("beta-q", ns.Name).ClusterQueue(betaCQ.Name).Obj()
			util.MustCreate(ctx, k8sClient, betaLQ)

			gammaCQ = testing.MakeClusterQueue("gamma-cq").
				Cohort("all").
				ResourceGroup(*testing.MakeFlavorQuotas("alpha").Resource(corev1.ResourceCPU, "2").Obj()).
				Preemption(kueue.ClusterQueuePreemption{
					WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
					ReclaimWithinCohort: kueue.PreemptionPolicyAny,
				}).
				Obj()
			util.MustCreate(ctx, k8sClient, gammaCQ)
			gammaLQ = testing.MakeLocalQueue("gamma-q", ns.Name).ClusterQueue(gammaCQ.Name).Obj()
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

			alphaLowWl := testing.MakeWorkload("alpha-low", ns.Name).
				Queue(alphaLQ.Name).
				Priority(lowPriority).
				Request(corev1.ResourceCPU, "1").
				Obj()
			util.MustCreate(ctx, k8sClient, alphaLowWl)

			betaMidWl := testing.MakeWorkload("beta-mid", ns.Name).
				Queue(betaLQ.Name).
				Priority(midPriority).
				Request(corev1.ResourceCPU, "1").
				Obj()
			util.MustCreate(ctx, k8sClient, betaMidWl)
			betaHighWl := testing.MakeWorkload("beta-high", ns.Name).
				Queue(betaLQ.Name).
				Priority(highPriority).
				Request(corev1.ResourceCPU, "4").
				Obj()
			util.MustCreate(ctx, k8sClient, betaHighWl)

			util.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, alphaCQ.Name, alphaLowWl)
			util.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, betaCQ.Name, betaMidWl, betaHighWl)

			ginkgo.By("Creating workload in alpha-cq to preempt workloads in both ClusterQueues")
			alphaMidWl := testing.MakeWorkload("alpha-mid", ns.Name).
				Queue(alphaLQ.Name).
				Priority(midPriority).
				Request(corev1.ResourceCPU, "2").
				Obj()
			util.MustCreate(ctx, k8sClient, alphaMidWl)

			util.FinishEvictionForWorkloads(ctx, k8sClient, alphaLowWl, betaMidWl)

			util.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, alphaCQ.Name, alphaMidWl)
			util.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, betaCQ.Name, betaHighWl)
			util.ExpectWorkloadsToBePending(ctx, k8sClient, alphaLowWl, betaMidWl)

			conditionCmpOpts := cmpopts.IgnoreFields(metav1.Condition{}, "LastTransitionTime")
			ginkgo.By("Verify the Preempted condition", func() {
				util.ExpectPreemptedCondition(ctx, k8sClient, kueue.InClusterQueueReason, metav1.ConditionTrue, alphaLowWl, alphaMidWl, string(alphaMidWl.UID), "UNKNOWN")
				util.ExpectPreemptedCondition(ctx, k8sClient, kueue.InCohortReclamationReason, metav1.ConditionTrue, betaMidWl, alphaMidWl, string(alphaMidWl.UID), "UNKNOWN")
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
						Type:    kueue.WorkloadPreempted,
						Status:  metav1.ConditionFalse,
						Reason:  "QuotaReserved",
						Message: fmt.Sprintf("Previously: Preempted to accommodate a workload (UID: %s, JobUID: UNKNOWN) due to %s", alphaMidWl.UID, preemption.HumanReadablePreemptionReasons[kueue.InClusterQueueReason]),
					}, conditionCmpOpts))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())

				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(betaMidWl), betaMidWl)).To(gomega.Succeed())
					g.Expect(apimeta.FindStatusCondition(betaMidWl.Status.Conditions, kueue.WorkloadPreempted)).To(gomega.BeComparableTo(&metav1.Condition{
						Type:    kueue.WorkloadPreempted,
						Status:  metav1.ConditionFalse,
						Reason:  "QuotaReserved",
						Message: fmt.Sprintf("Previously: Preempted to accommodate a workload (UID: %s, JobUID: UNKNOWN) due to %s", alphaMidWl.UID, preemption.HumanReadablePreemptionReasons[kueue.InCohortReclamationReason]),
					}, conditionCmpOpts))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("Should not preempt Workloads in the cohort, if the ClusterQueue requires borrowing", func() {
			ginkgo.By("Creating workloads in beta-cq that borrow quota")

			alphaHighWl1 := testing.MakeWorkload("alpha-high-1", ns.Name).
				Queue(alphaLQ.Name).
				Priority(highPriority).
				Request(corev1.ResourceCPU, "2").
				Obj()
			util.MustCreate(ctx, k8sClient, alphaHighWl1)
			betaLowWl := testing.MakeWorkload("beta-low", ns.Name).
				Queue(betaLQ.Name).
				Priority(lowPriority).
				Request(corev1.ResourceCPU, "4").
				Obj()
			util.MustCreate(ctx, k8sClient, betaLowWl)

			util.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, alphaCQ.Name, alphaHighWl1)
			util.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, betaCQ.Name, betaLowWl)

			ginkgo.By("Creating high priority workload in alpha-cq that doesn't fit without borrowing")
			alphaHighWl2 := testing.MakeWorkload("alpha-high-2", ns.Name).
				Queue(alphaLQ.Name).
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

			betaMidWl := testing.MakeWorkload("beta-mid", ns.Name).
				Queue(betaLQ.Name).
				Priority(midPriority).
				Request(corev1.ResourceCPU, "3").
				Obj()
			util.MustCreate(ctx, k8sClient, betaMidWl)
			betaHighWl := testing.MakeWorkload("beta-high", ns.Name).
				Queue(betaLQ.Name).
				Priority(highPriority).
				Request(corev1.ResourceCPU, "3").
				Obj()
			util.MustCreate(ctx, k8sClient, betaHighWl)

			util.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, betaCQ.Name, betaMidWl, betaHighWl)

			ginkgo.By("Creating workload in alpha-cq and gamma-cq that need to preempt")
			alphaMidWl := testing.MakeWorkload("alpha-mid", ns.Name).
				Queue(alphaLQ.Name).
				Priority(midPriority).
				Request(corev1.ResourceCPU, "2").
				Obj()

			gammaMidWl := testing.MakeWorkload("gamma-mid", ns.Name).
				Queue(gammaLQ.Name).
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
			for i := 0; i < 3; i++ {
				wl := testing.MakeWorkload(fmt.Sprintf("beta-%d", i), ns.Name).
					Queue(betaLQ.Name).
					Request(corev1.ResourceCPU, "2").
					Obj()
				util.MustCreate(ctx, k8sClient, wl)
				betaWls = append(betaWls, wl)
			}
			util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, betaWls...)

			ginkgo.By("Creating preempting pods")

			alphaWl := testing.MakeWorkload("alpha", ns.Name).
				Queue(alphaLQ.Name).
				Request(corev1.ResourceCPU, "2").
				Obj()
			util.MustCreate(ctx, k8sClient, alphaWl)

			gammaWl := testing.MakeWorkload("gamma", ns.Name).
				Queue(gammaLQ.Name).
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
			oneFlavor = testing.MakeResourceFlavor("one").Obj()
			util.MustCreate(ctx, k8sClient, oneFlavor)

			alphaCQ = testing.MakeClusterQueue("alpha-cq").
				Cohort("all").
				QueueingStrategy(kueue.StrictFIFO).
				ResourceGroup(*testing.MakeFlavorQuotas("one").Resource(corev1.ResourceCPU, "2").Obj()).
				Preemption(kueue.ClusterQueuePreemption{
					WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
					ReclaimWithinCohort: kueue.PreemptionPolicyAny,
				}).
				Obj()
			util.MustCreate(ctx, k8sClient, alphaCQ)
			alphaLQ = testing.MakeLocalQueue("alpha-lq", ns.Name).ClusterQueue("alpha-cq").Obj()
			util.MustCreate(ctx, k8sClient, alphaLQ)
			betaCQ = testing.MakeClusterQueue("beta-cq").
				Cohort("all").
				QueueingStrategy(kueue.StrictFIFO).
				ResourceGroup(*testing.MakeFlavorQuotas("one").Resource(corev1.ResourceCPU, "2").Obj()).
				Preemption(kueue.ClusterQueuePreemption{
					WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
					ReclaimWithinCohort: kueue.PreemptionPolicyAny,
				}).
				Obj()
			util.MustCreate(ctx, k8sClient, betaCQ)
			betaLQ = testing.MakeLocalQueue("beta-lq", ns.Name).ClusterQueue("beta-cq").Obj()
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
			useAllAlphaWl := testing.MakeWorkload("use-all", ns.Name).
				Queue("alpha-lq").
				Priority(1).
				Request(corev1.ResourceCPU, "4").
				Obj()
			util.MustCreate(ctx, k8sClient, useAllAlphaWl)
			util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, useAllAlphaWl)

			pendingAlphaWl := testing.MakeWorkload("pending", ns.Name).
				Queue("alpha-lq").
				Priority(0).
				Request(corev1.ResourceCPU, "1").
				Obj()
			util.MustCreate(ctx, k8sClient, pendingAlphaWl)
			util.ExpectWorkloadsToBePending(ctx, k8sClient, pendingAlphaWl)

			ginkgo.By("Creating a workload to reclaim quota")

			preemptorBetaWl := testing.MakeWorkload("preemptor", ns.Name).
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
			oneFlavor = testing.MakeResourceFlavor("one").Obj()
			util.MustCreate(ctx, k8sClient, oneFlavor)

			fallbackFlavor = testing.MakeResourceFlavor("fallback").Obj()
			util.MustCreate(ctx, k8sClient, fallbackFlavor)

			aStandardCQ = testing.MakeClusterQueue("a-standard-cq").
				Cohort("all").
				ResourceGroup(
					*testing.MakeFlavorQuotas("one").Resource(corev1.ResourceCPU, "1", "10").Obj(),
					*testing.MakeFlavorQuotas("fallback").Resource(corev1.ResourceCPU, "10", "0").Obj(),
				).
				FlavorFungibility(kueue.FlavorFungibility{
					WhenCanBorrow:  kueue.Borrow,
					WhenCanPreempt: kueue.Preempt,
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
			aStandardLQ = testing.MakeLocalQueue("a-standard-lq", ns.Name).ClusterQueue(aStandardCQ.Name).Obj()
			util.MustCreate(ctx, k8sClient, aStandardLQ)

			aBestEffortCQ = testing.MakeClusterQueue("a-best-effort-cq").
				Cohort("all").
				ResourceGroup(
					*testing.MakeFlavorQuotas("one").Resource(corev1.ResourceCPU, "1", "10").Obj(),
					*testing.MakeFlavorQuotas("fallback").Resource(corev1.ResourceCPU, "10", "0").Obj(),
				).
				FlavorFungibility(kueue.FlavorFungibility{
					WhenCanBorrow:  kueue.Borrow,
					WhenCanPreempt: kueue.Preempt,
				}).
				Preemption(kueue.ClusterQueuePreemption{
					ReclaimWithinCohort: kueue.PreemptionPolicyLowerPriority,
					BorrowWithinCohort: &kueue.BorrowWithinCohort{
						Policy: kueue.BorrowWithinCohortPolicyLowerPriority,
					},
				}).
				Obj()
			util.MustCreate(ctx, k8sClient, aBestEffortCQ)
			aBestEffortLQ = testing.MakeLocalQueue("a-best-effort-lq", ns.Name).ClusterQueue(aBestEffortCQ.Name).Obj()
			util.MustCreate(ctx, k8sClient, aBestEffortLQ)

			bBestEffortCQ = testing.MakeClusterQueue("b-best-effort-cq").
				Cohort("all").
				ResourceGroup(
					*testing.MakeFlavorQuotas("one").Resource(corev1.ResourceCPU, "1", "10").Obj(),
					*testing.MakeFlavorQuotas("fallback").Resource(corev1.ResourceCPU, "10", "0").Obj(),
				).
				FlavorFungibility(kueue.FlavorFungibility{
					WhenCanBorrow:  kueue.Borrow,
					WhenCanPreempt: kueue.Preempt,
				}).
				Preemption(kueue.ClusterQueuePreemption{
					ReclaimWithinCohort: kueue.PreemptionPolicyLowerPriority,
					BorrowWithinCohort: &kueue.BorrowWithinCohort{
						Policy: kueue.BorrowWithinCohortPolicyLowerPriority,
					},
				}).
				Obj()
			util.MustCreate(ctx, k8sClient, bBestEffortCQ)
			bBestEffortLQ = testing.MakeLocalQueue("b-best-effort-lq", ns.Name).ClusterQueue(bBestEffortCQ.Name).Obj()
			util.MustCreate(ctx, k8sClient, bBestEffortLQ)

			bStandardCQ = testing.MakeClusterQueue("b-standard-cq").
				Cohort("all").
				ResourceGroup(
					*testing.MakeFlavorQuotas("one").Resource(corev1.ResourceCPU, "1", "10").Obj(),
					*testing.MakeFlavorQuotas("fallback").Resource(corev1.ResourceCPU, "10", "0").Obj(),
				).
				FlavorFungibility(kueue.FlavorFungibility{
					WhenCanBorrow:  kueue.Borrow,
					WhenCanPreempt: kueue.Preempt,
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
			bStandardLQ = testing.MakeLocalQueue("b-standard-lq", ns.Name).ClusterQueue(bStandardCQ.Name).Obj()
			util.MustCreate(ctx, k8sClient, bStandardLQ)

			sharedCQ = testing.MakeClusterQueue("shared-cq").
				Cohort("all").
				ResourceGroup(*testing.MakeFlavorQuotas("one").Resource(corev1.ResourceCPU, "10").Obj()).
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
			aBestEffortLowWl := testing.MakeWorkload("a-best-effort-low", ns.Name).
				Queue(aBestEffortLQ.Name).
				Priority(lowPriority).
				Request(corev1.ResourceCPU, "5").
				Obj()
			util.MustCreate(ctx, k8sClient, aBestEffortLowWl)

			ginkgo.By("Await for the a-best-effort-low workload to be admitted")
			util.ExpectWorkloadToBeAdmittedAs(ctx, k8sClient, aBestEffortLowWl,
				testing.MakeAdmission(aBestEffortCQ.Name).Assignment(corev1.ResourceCPU, "one", "5").Obj(),
			)

			ginkgo.By("Create a low priority workload which is not borrowing")
			bBestEffortLowWl := testing.MakeWorkload("b-best-effort-low", ns.Name).
				Queue(bBestEffortLQ.Name).
				Priority(lowPriority).
				Request(corev1.ResourceCPU, "1").
				Obj()
			util.MustCreate(ctx, k8sClient, bBestEffortLowWl)

			ginkgo.By("Await for the b-best-effort-low workload to be admitted")
			util.ExpectWorkloadToBeAdmittedAs(ctx, k8sClient, bBestEffortLowWl,
				testing.MakeAdmission(bBestEffortCQ.Name).Assignment(corev1.ResourceCPU, "one", "1").Obj(),
			)

			ginkgo.By("Create a high priority workload (above MaxPriorityThreshold) which requires borrowing")
			bStandardWl := testing.MakeWorkload("b-standard-high", ns.Name).
				Queue(bStandardLQ.Name).
				Priority(highPriority).
				Request(corev1.ResourceCPU, "5").
				Obj()
			util.MustCreate(ctx, k8sClient, bStandardWl)

			ginkgo.By("Await for the b-standard-high workload to be admitted")
			util.ExpectWorkloadToBeAdmittedAs(ctx, k8sClient, bStandardWl,
				testing.MakeAdmission(bStandardCQ.Name).Assignment(corev1.ResourceCPU, "one", "5").Obj(),
			)

			ginkgo.By("Create the a-standard-very-high workload")
			aStandardVeryHighWl := testing.MakeWorkload("a-standard-very-high", ns.Name).
				Queue(aStandardLQ.Name).
				Priority(veryHighPriority).
				Request(corev1.ResourceCPU, "7").
				Obj()
			util.MustCreate(ctx, k8sClient, aStandardVeryHighWl)

			util.ExpectPreemptedCondition(ctx, k8sClient, kueue.InCohortReclaimWhileBorrowingReason, metav1.ConditionTrue, aBestEffortLowWl, aStandardVeryHighWl, string(aStandardVeryHighWl.UID), "UNKNOWN")
			util.ExpectPreemptedWorkloadsTotalMetric(aStandardCQ.Name, kueue.InCohortReclaimWhileBorrowingReason, 1)
			util.ExpectPreemptedWorkloadsTotalMetric(aBestEffortCQ.Name, kueue.InCohortReclaimWhileBorrowingReason, 0)

			ginkgo.By("Finish eviction fo the a-best-effort-low workload")
			util.FinishEvictionForWorkloads(ctx, k8sClient, aBestEffortLowWl)

			ginkgo.By("Verify the a-standard-very-high workload is admitted")
			util.ExpectWorkloadToBeAdmittedAs(ctx, k8sClient, aStandardVeryHighWl,
				testing.MakeAdmission(aStandardCQ.Name).Assignment(corev1.ResourceCPU, "one", "7").Obj(),
			)

			ginkgo.By("Verify the a-best-effort-low workload is re-admitted, but using flavor 2")
			util.ExpectWorkloadToBeAdmittedAs(ctx, k8sClient, aBestEffortLowWl,
				testing.MakeAdmission(aBestEffortCQ.Name).Assignment(corev1.ResourceCPU, "fallback", "5").Obj(),
			)

			ginkgo.By("Verify the b-standard-high workload remains admitted")
			util.ExpectWorkloadToBeAdmittedAs(ctx, k8sClient, bStandardWl,
				testing.MakeAdmission(bStandardCQ.Name).Assignment(corev1.ResourceCPU, "one", "5").Obj(),
			)

			ginkgo.By("Verify for the b-best-effort-low workload remains admitted")
			util.ExpectWorkloadToBeAdmittedAs(ctx, k8sClient, bBestEffortLowWl,
				testing.MakeAdmission(bBestEffortCQ.Name).Assignment(corev1.ResourceCPU, "one", "1").Obj(),
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
			prodCQ = testing.MakeClusterQueue("prod-cq").
				Cohort("all").
				ResourceGroup(
					*testing.MakeFlavorQuotas("alpha").Resource(corev1.ResourceCPU, "5", "", "4").Obj(),
				).
				Preemption(kueue.ClusterQueuePreemption{
					ReclaimWithinCohort: kueue.PreemptionPolicyAny,
				}).
				Obj()
			util.MustCreate(ctx, k8sClient, prodCQ)

			devCQ = testing.MakeClusterQueue("dev-cq").
				Cohort("all").
				ResourceGroup(*testing.MakeFlavorQuotas("alpha").Resource(corev1.ResourceCPU, "5").Obj()).
				Obj()
			util.MustCreate(ctx, k8sClient, devCQ)

			prodQueue := testing.MakeLocalQueue("prod-queue", ns.Name).ClusterQueue(prodCQ.Name).Obj()
			util.MustCreate(ctx, k8sClient, prodQueue)

			devQueue := testing.MakeLocalQueue("dev-queue", ns.Name).ClusterQueue(devCQ.Name).Obj()
			util.MustCreate(ctx, k8sClient, devQueue)

			ginkgo.By("Creating two workloads")
			wl1 := testing.MakeWorkload("wl-1", ns.Name).Priority(0).Queue(devQueue.Name).Request(corev1.ResourceCPU, "4").Obj()
			wl2 := testing.MakeWorkload("wl-2", ns.Name).Priority(1).Queue(devQueue.Name).Request(corev1.ResourceCPU, "5").Obj()
			util.MustCreate(ctx, k8sClient, wl1)
			util.MustCreate(ctx, k8sClient, wl2)
			util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1, wl2)

			ginkgo.By("Creating another workload")
			wl3 := testing.MakeWorkload("wl-3", ns.Name).Queue(prodQueue.Name).Request(corev1.ResourceCPU, "4").Obj()
			util.MustCreate(ctx, k8sClient, wl3)
			util.ExpectWorkloadsToBePreempted(ctx, k8sClient, wl1)

			util.FinishEvictionForWorkloads(ctx, k8sClient, wl1)

			util.ExpectWorkloadToBeAdmittedAs(ctx, k8sClient, wl3,
				testing.MakeAdmission(prodCQ.Name).Assignment(corev1.ResourceCPU, "alpha", "4").Obj())
		})
	})

	ginkgo.Context("When borrowWithinCohort is used and PrioritySortingWithinCohort disabled", func() {
		var (
			aCQ, bCQ, cCQ *kueue.ClusterQueue
			aLQ, bLQ      *kueue.LocalQueue
			defaultFlavor *kueue.ResourceFlavor
		)

		ginkgo.BeforeEach(func() {
			gomega.Expect(features.SetEnable(features.PrioritySortingWithinCohort, false)).To(gomega.Succeed())
			defaultFlavor = testing.MakeResourceFlavor("default").Obj()
			util.MustCreate(ctx, k8sClient, defaultFlavor)

			aCQ = testing.MakeClusterQueue("a-cq").
				Cohort("all").
				ResourceGroup(
					*testing.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "0", "10").Obj(),
				).
				FlavorFungibility(kueue.FlavorFungibility{
					WhenCanBorrow:  kueue.Borrow,
					WhenCanPreempt: kueue.Preempt,
				}).
				Preemption(kueue.ClusterQueuePreemption{
					ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
				}).
				Obj()
			util.MustCreate(ctx, k8sClient, aCQ)
			aLQ = testing.MakeLocalQueue("a-lq", ns.Name).ClusterQueue(aCQ.Name).Obj()
			util.MustCreate(ctx, k8sClient, aLQ)

			bCQ = testing.MakeClusterQueue("b-cq").
				Cohort("all").
				ResourceGroup(
					*testing.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "5", "5").Obj(),
				).
				FlavorFungibility(kueue.FlavorFungibility{
					WhenCanBorrow:  kueue.Borrow,
					WhenCanPreempt: kueue.Preempt,
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
			bLQ = testing.MakeLocalQueue("b-lq", ns.Name).ClusterQueue(bCQ.Name).Obj()
			util.MustCreate(ctx, k8sClient, bLQ)

			cCQ = testing.MakeClusterQueue("c-cq").
				Cohort("all").
				ResourceGroup(
					*testing.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "5").Obj(),
				).
				FlavorFungibility(kueue.FlavorFungibility{
					WhenCanBorrow:  kueue.Borrow,
					WhenCanPreempt: kueue.Preempt,
				}).
				Preemption(kueue.ClusterQueuePreemption{
					ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
				}).
				Obj()
			util.MustCreate(ctx, k8sClient, cCQ)
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(features.SetEnable(features.PrioritySortingWithinCohort, true)).To(gomega.Succeed())
			gomega.Expect(util.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			util.ExpectObjectToBeDeleted(ctx, k8sClient, aCQ, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, bCQ, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, cCQ, true)
		})

		ginkgo.It("should allow preempting workloads while borrowing", func() {
			var aWl, b1Wl, b2Wl *kueue.Workload

			ginkgo.By("Create a mid priority workload in aCQ and await for admission", func() {
				aWl = testing.MakeWorkload("a-low", ns.Name).
					Queue(aLQ.Name).
					Priority(midPriority).
					Request(corev1.ResourceCPU, "4").
					Obj()
				util.MustCreate(ctx, k8sClient, aWl)
				util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, aWl)
			})

			ginkgo.By("Create a high priority workload b1 in bCQ and await for admission", func() {
				b1Wl = testing.MakeWorkload("b1-high", ns.Name).
					Queue(bLQ.Name).
					Priority(highPriority).
					Request(corev1.ResourceCPU, "4").
					Obj()
				util.MustCreate(ctx, k8sClient, b1Wl)
				util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, b1Wl)
			})

			ginkgo.By("Create a high priority workload b2 in bCQ", func() {
				b2Wl = testing.MakeWorkload("b2-high", ns.Name).
					Queue(bLQ.Name).
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
})
