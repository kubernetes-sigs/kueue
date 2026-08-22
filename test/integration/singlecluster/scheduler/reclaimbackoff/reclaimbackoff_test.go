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

package reclaimbackoff

import (
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("Scheduler Reclaim Backoff", func() {
	var (
		ns            *corev1.Namespace
		defaultFlavor *kueue.ResourceFlavor
		aCQ, bCQ      *kueue.ClusterQueue
		aLQ, bLQ      *kueue.LocalQueue
	)

	ginkgo.BeforeEach(func() {
		fwk.StartManager(ctx, cfg, managerAndSchedulerSetup(true))

		defaultFlavor = utiltestingapi.MakeResourceFlavor("default").Obj()
		util.MustCreate(ctx, k8sClient, defaultFlavor)

		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "reclaim-backoff-")

		// a-cq is the reclaiming queue; it preempts borrowers to recover its
		// nominal quota. b-cq is the borrower whose borrowing gets reclaimed.
		aCQ = utiltestingapi.MakeClusterQueue("a-cq").
			Cohort("all").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "2").Obj()).
			Preemption(kueue.ClusterQueuePreemption{
				WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
				ReclaimWithinCohort: kueue.PreemptionPolicyAny,
			}).
			Obj()
		util.MustCreate(ctx, k8sClient, aCQ)
		aLQ = utiltestingapi.MakeLocalQueue("a-lq", ns.Name).ClusterQueue(aCQ.Name).Obj()
		util.MustCreate(ctx, k8sClient, aLQ)

		bCQ = utiltestingapi.MakeClusterQueue("b-cq").
			Cohort("all").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "2").Obj()).
			Obj()
		util.MustCreate(ctx, k8sClient, bCQ)
		bLQ = utiltestingapi.MakeLocalQueue("b-lq", ns.Name).ClusterQueue(bCQ.Name).Obj()
		util.MustCreate(ctx, k8sClient, bLQ)
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		util.ExpectObjectToBeDeleted(ctx, k8sClient, aCQ, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, bCQ, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, defaultFlavor, true)
		fwk.StopManager(ctx)
	})

	// expectPendingReason asserts the workload currently has no quota reservation
	// and its QuotaReserved condition carries the given reason.
	expectPendingReason := func(wl *kueue.Workload, reason string) {
		ginkgo.GinkgoHelper()
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), wl)).To(gomega.Succeed())
			cond := apimeta.FindStatusCondition(wl.Status.Conditions, kueue.WorkloadQuotaReserved)
			g.Expect(cond).NotTo(gomega.BeNil())
			g.Expect(cond.Status).To(gomega.Equal(metav1.ConditionFalse))
			g.Expect(cond.Reason).To(gomega.Equal(reason))
		}, util.Timeout, util.Interval).Should(gomega.Succeed())
	}

	// armBackoff drives a full borrow -> reclaim cycle so that (b-cq, default/cpu)
	// enters its reclaim cooldown, returning the reclaiming workload (still
	// admitted, occupying a-cq's nominal quota) and the evicted borrower.
	armBackoff := func() (aReclaim, bBorrow *kueue.Workload) {
		ginkgo.By("b-cq borrows cohort quota (4 > its nominal 2)")
		bBorrow = utiltestingapi.MakeWorkload("b-borrow", ns.Name).
			Queue(kueue.LocalQueueName(bLQ.Name)).
			Request(corev1.ResourceCPU, "4").
			Obj()
		util.MustCreate(ctx, k8sClient, bBorrow)
		util.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, bCQ.Name, bBorrow)

		ginkgo.By("a-cq reclaims its nominal quota, evicting the borrower")
		aReclaim = utiltestingapi.MakeWorkload("a-reclaim", ns.Name).
			Queue(kueue.LocalQueueName(aLQ.Name)).
			Request(corev1.ResourceCPU, "2").
			Obj()
		util.MustCreate(ctx, k8sClient, aReclaim)

		util.FinishEvictionForWorkloads(ctx, k8sClient, bBorrow)
		util.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, aCQ.Name, aReclaim)
		util.ExpectWorkloadsToBePending(ctx, k8sClient, bBorrow)
		return aReclaim, bBorrow
	}

	ginkgo.It("defers a borrowing workload during the reclaim cooldown, then admits it after expiry", func() {
		aReclaim, bBorrow := armBackoff()

		ginkgo.By("during cooldown the borrower stays pending with the ReclaimBackoff reason")
		expectPendingReason(bBorrow, kueue.WorkloadQuotaReservedReasonReclaimBackoff)

		ginkgo.By("freeing a-cq's quota; the borrower is admitted once the cooldown expires")
		util.FinishWorkloads(ctx, k8sClient, aReclaim)
		util.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, bCQ.Name, bBorrow)
	})

	ginkgo.It("admits a within-nominal workload on the backed-off ClusterQueue during the cooldown", func() {
		armBackoff()

		ginkgo.By("a workload within b-cq nominal quota is unaffected by the backoff")
		bNominal := utiltestingapi.MakeWorkload("b-nominal", ns.Name).
			Queue(kueue.LocalQueueName(bLQ.Name)).
			Request(corev1.ResourceCPU, "2").
			Obj()
		util.MustCreate(ctx, k8sClient, bNominal)
		util.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, bCQ.Name, bNominal)
	})
})

var _ = ginkgo.Describe("Scheduler Reclaim Backoff disabled", func() {
	var (
		ns            *corev1.Namespace
		defaultFlavor *kueue.ResourceFlavor
		aCQ, bCQ      *kueue.ClusterQueue
		aLQ, bLQ      *kueue.LocalQueue
	)

	ginkgo.BeforeEach(func() {
		// Gate off and no tracker: reclaim proceeds with no backoff.
		fwk.StartManager(ctx, cfg, managerAndSchedulerSetup(false))

		defaultFlavor = utiltestingapi.MakeResourceFlavor("default").Obj()
		util.MustCreate(ctx, k8sClient, defaultFlavor)

		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "reclaim-nobackoff-")

		aCQ = utiltestingapi.MakeClusterQueue("a-cq").
			Cohort("all").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "2").Obj()).
			Preemption(kueue.ClusterQueuePreemption{
				WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
				ReclaimWithinCohort: kueue.PreemptionPolicyAny,
			}).
			Obj()
		util.MustCreate(ctx, k8sClient, aCQ)
		aLQ = utiltestingapi.MakeLocalQueue("a-lq", ns.Name).ClusterQueue(aCQ.Name).Obj()
		util.MustCreate(ctx, k8sClient, aLQ)

		bCQ = utiltestingapi.MakeClusterQueue("b-cq").
			Cohort("all").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "2").Obj()).
			Obj()
		util.MustCreate(ctx, k8sClient, bCQ)
		bLQ = utiltestingapi.MakeLocalQueue("b-lq", ns.Name).ClusterQueue(bCQ.Name).Obj()
		util.MustCreate(ctx, k8sClient, bLQ)
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		util.ExpectObjectToBeDeleted(ctx, k8sClient, aCQ, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, bCQ, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, defaultFlavor, true)
		fwk.StopManager(ctx)
	})

	ginkgo.It("re-admits the borrower immediately after reclaim when the feature is off", func() {
		ginkgo.By("b-cq borrows cohort quota (4 > its nominal 2)")
		bBorrow := utiltestingapi.MakeWorkload("b-borrow", ns.Name).
			Queue(kueue.LocalQueueName(bLQ.Name)).
			Request(corev1.ResourceCPU, "4").
			Obj()
		util.MustCreate(ctx, k8sClient, bBorrow)
		util.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, bCQ.Name, bBorrow)

		ginkgo.By("a-cq reclaims its nominal quota, evicting the borrower")
		aReclaim := utiltestingapi.MakeWorkload("a-reclaim", ns.Name).
			Queue(kueue.LocalQueueName(aLQ.Name)).
			Request(corev1.ResourceCPU, "2").
			Obj()
		util.MustCreate(ctx, k8sClient, aReclaim)
		util.FinishEvictionForWorkloads(ctx, k8sClient, bBorrow)
		util.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, aCQ.Name, aReclaim)

		ginkgo.By("with the feature off, the borrower is re-admitted as soon as a-cq frees its quota, without any backoff deferral")
		util.FinishWorkloads(ctx, k8sClient, aReclaim)
		util.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, bCQ.Name, bBorrow)
	})
})
