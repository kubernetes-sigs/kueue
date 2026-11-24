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

package fairsharing

import (
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	config "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/test/util/behavioral"
)

// The final verification rejects the fair target in the other Cohort, so the fallback preempts an own workload instead.
var _ = ginkgo.Describe("Scheduler with fair sharing within-ClusterQueue fallback", ginkgo.Label("feature:fairsharing"), func() {
	var (
		ns *corev1.Namespace

		rootCohort    *kueue.Cohort
		orgACohort    *kueue.Cohort
		orgBCohort    *kueue.Cohort
		defaultFlavor *kueue.ResourceFlavor
		preemptorCQ   *kueue.ClusterQueue
		siblingCQ     *kueue.ClusterQueue
		otherCQ       *kueue.ClusterQueue
		preemptorLQ   *kueue.LocalQueue
		siblingLQ     *kueue.LocalQueue
		otherLQ       *kueue.LocalQueue
	)

	// queueTree creates the cohorts and ClusterQueues, named after the namespace because the metrics are global.
	queueTree := func(rootQuota, preemptorQuota string) {
		rootCohort = utiltestingapi.MakeCohort(kueue.CohortReference("fallback-root-" + ns.Name)).
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, rootQuota).Obj()).
			Obj()
		behavioral.MustCreate(ctx, k8sClient, rootCohort)
		orgACohort = utiltestingapi.MakeCohort(kueue.CohortReference("fallback-org-a-" + ns.Name)).Parent(kueue.CohortReference(rootCohort.Name)).Obj()
		behavioral.MustCreate(ctx, k8sClient, orgACohort)
		orgBCohort = utiltestingapi.MakeCohort(kueue.CohortReference("fallback-org-b-" + ns.Name)).Parent(kueue.CohortReference(rootCohort.Name)).Obj()
		behavioral.MustCreate(ctx, k8sClient, orgBCohort)

		preemptorCQ = utiltestingapi.MakeClusterQueue("fallback-p-" + ns.Name).
			Cohort(kueue.CohortReference(orgACohort.Name)).
			FairWeight(resource.MustParse("5")).
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, preemptorQuota).Obj()).
			Preemption(kueue.ClusterQueuePreemption{
				WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
				ReclaimWithinCohort: kueue.PreemptionPolicyAny,
			}).
			Obj()
		siblingCQ = utiltestingapi.MakeClusterQueue("fallback-s-" + ns.Name).
			Cohort(kueue.CohortReference(orgACohort.Name)).
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "0").Obj()).
			Obj()
		otherCQ = utiltestingapi.MakeClusterQueue("fallback-x-" + ns.Name).
			Cohort(kueue.CohortReference(orgBCohort.Name)).
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "0").Obj()).
			Obj()
		for _, cq := range []*kueue.ClusterQueue{preemptorCQ, siblingCQ, otherCQ} {
			behavioral.MustCreate(ctx, k8sClient, cq)
		}
		behavioral.ExpectClusterQueuesToBeActive(ctx, k8sClient, preemptorCQ, siblingCQ, otherCQ)

		preemptorLQ = utiltestingapi.MakeLocalQueue("fallback-p", ns.Name).ClusterQueue(preemptorCQ.Name).Obj()
		siblingLQ = utiltestingapi.MakeLocalQueue("fallback-s", ns.Name).ClusterQueue(siblingCQ.Name).Obj()
		otherLQ = utiltestingapi.MakeLocalQueue("fallback-x", ns.Name).ClusterQueue(otherCQ.Name).Obj()
		for _, lq := range []*kueue.LocalQueue{preemptorLQ, siblingLQ, otherLQ} {
			behavioral.MustCreate(ctx, k8sClient, lq)
		}
	}

	workload := func(name string, lq *kueue.LocalQueue, cpu string, priority int32) *kueue.Workload {
		return utiltestingapi.MakeWorkload(name, ns.Name).
			Queue(kueue.LocalQueueName(lq.Name)).
			Priority(priority).
			Request(corev1.ResourceCPU, cpu).
			Obj()
	}

	ginkgo.BeforeEach(func() {
		fwk.StartManagerForSpec(ctx, cfg, managerAndSchedulerSetupWithFairSharing(&config.FairSharing{
			PreemptionStrategies: []config.PreemptionStrategy{config.LessThanInitialShare},
		}, nil))

		defaultFlavor = utiltestingapi.MakeResourceFlavor("default").Obj()
		behavioral.MustCreate(ctx, k8sClient, defaultFlavor)
		ns = behavioral.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "fallback-")
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(behavioral.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		gomega.Expect(behavioral.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		for _, cq := range []*kueue.ClusterQueue{preemptorCQ, siblingCQ, otherCQ} {
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
		}
		for _, cohort := range []*kueue.Cohort{rootCohort, orgACohort, orgBCohort} {
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, cohort, true)
		}
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, defaultFlavor, true)
	})

	// expectNothingPreempted checks that the workloads stay admitted and are never marked Preempted.
	expectNothingPreempted := func(wls ...*kueue.Workload) {
		ginkgo.GinkgoHelper()
		gomega.Consistently(func(g gomega.Gomega) {
			for _, wl := range wls {
				var got kueue.Workload
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &got)).To(gomega.Succeed())
				g.Expect(got.Status.Admission).NotTo(gomega.BeNil(), "workload %q lost its admission", wl.Name)
				g.Expect(got.Status.Conditions).NotTo(gomega.ContainElement(gomega.HaveField("Type", kueue.WorkloadPreempted)),
					"workload %q was preempted", wl.Name)
			}
		}, behavioral.LongConsistentDuration, behavioral.Interval).Should(gomega.Succeed())
	}

	ginkgo.It("preempts an own workload of the same size when the fair target is rejected", func() {
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.FairSharingPreemptWithinClusterQueueFallback, true)
		queueTree("24", "0")

		pLow := workload("p-low", preemptorLQ, "4", 1)
		pMid := workload("p-mid", preemptorLQ, "4", 2)
		s1 := workload("s-1", siblingLQ, "3", 0)
		x1 := workload("x-1", otherLQ, "4", 0)
		x2 := workload("x-2", otherLQ, "9", 1)
		for _, wl := range []*kueue.Workload{pLow, pMid, s1, x1, x2} {
			behavioral.MustCreate(ctx, k8sClient, wl)
		}
		behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, pLow, pMid, s1, x1, x2)

		pHigh := workload("p-high", preemptorLQ, "4", 10)
		behavioral.MustCreate(ctx, k8sClient, pHigh)

		behavioral.ExpectWorkloadsToBePreempted(ctx, k8sClient, pLow)
		behavioral.ExpectWorkloadToHaveConditions(ctx, k8sClient, client.ObjectKeyFromObject(pLow), metav1.Condition{
			Type:   kueue.WorkloadPreempted,
			Status: metav1.ConditionTrue,
			Reason: kueue.InClusterQueueReason,
		})
		behavioral.ExpectPreemptedWorkloadsTotalMetric(preemptorCQ.Name, kueue.InClusterQueueReason, 1)
		behavioral.ExpectPreemptedWorkloadsTotalMetric(preemptorCQ.Name, kueue.InCohortFairSharingReason, 0)

		behavioral.FinishEvictionForWorkloads(ctx, k8sClient, pLow)
		behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, pHigh)
		// The evicted workload finds nothing lower to preempt and no fair claim, so it stays pending and evicts nothing else.
		behavioral.ExpectWorkloadsToBePending(ctx, k8sClient, pLow)
		expectNothingPreempted(pMid, s1, x1, x2)
	})

	ginkgo.It("preempts a larger own workload when the fair target is rejected", func() {
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.FairSharingPreemptWithinClusterQueueFallback, true)
		queueTree("20", "0")

		pLow := workload("p-low", preemptorLQ, "6", 1)
		s1 := workload("s-1", siblingLQ, "3", 0)
		x1 := workload("x-1", otherLQ, "4", 0)
		x2 := workload("x-2", otherLQ, "7", 1)
		for _, wl := range []*kueue.Workload{pLow, s1, x1, x2} {
			behavioral.MustCreate(ctx, k8sClient, wl)
		}
		behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, pLow, s1, x1, x2)

		pHigh := workload("p-high", preemptorLQ, "4", 10)
		behavioral.MustCreate(ctx, k8sClient, pHigh)

		behavioral.ExpectWorkloadsToBePreempted(ctx, k8sClient, pLow)
		behavioral.ExpectWorkloadToHaveConditions(ctx, k8sClient, client.ObjectKeyFromObject(pLow), metav1.Condition{
			Type:   kueue.WorkloadPreempted,
			Status: metav1.ConditionTrue,
			Reason: kueue.InClusterQueueReason,
		})

		behavioral.FinishEvictionForWorkloads(ctx, k8sClient, pLow)
		behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, pHigh)
		behavioral.ExpectWorkloadsToBePending(ctx, k8sClient, pLow)
		expectNothingPreempted(s1, x1, x2)
	})

	ginkgo.It("leaves everything in place when the fallback is disabled", func() {
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.FairSharingPreemptWithinClusterQueueFallback, false)
		queueTree("24", "0")

		pLow := workload("p-low", preemptorLQ, "4", 1)
		pMid := workload("p-mid", preemptorLQ, "4", 2)
		s1 := workload("s-1", siblingLQ, "3", 0)
		x1 := workload("x-1", otherLQ, "4", 0)
		x2 := workload("x-2", otherLQ, "9", 1)
		for _, wl := range []*kueue.Workload{pLow, pMid, s1, x1, x2} {
			behavioral.MustCreate(ctx, k8sClient, wl)
		}
		behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, pLow, pMid, s1, x1, x2)

		pHigh := workload("p-high", preemptorLQ, "4", 10)
		behavioral.MustCreate(ctx, k8sClient, pHigh)
		behavioral.ExpectWorkloadsToBePending(ctx, k8sClient, pHigh)
		expectNothingPreempted(pLow, pMid, s1, x1, x2)
	})
})
