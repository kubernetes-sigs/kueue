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

package quotacheckstrategy

import (
	"fmt"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
	utilqueue "sigs.k8s.io/kueue/pkg/util/queue"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/test/util/behavioral"
)

var _ = ginkgo.Describe("AdmissionFairSharing classical preemption victim stability", ginkgo.Label("feature:quotacheckstrategy"), func() {
	const reclaimRuns = 12

	var (
		ns                 *corev1.Namespace
		defaultFlavor      *kueue.ResourceFlavor
		cqP, cqQ1, cqQ2    *kueue.ClusterQueue
		lqP, lqA, lqB, lqC *kueue.LocalQueue
		wlA, wlB, wlC      *kueue.Workload
		admittedCandidates []*kueue.Workload
	)

	ginkgo.BeforeEach(func() {
		fwk.StartManagerForSpec(ctx, cfg, managerAndSchedulerSetup(
			configapi.QuotaCheckIgnoreUndeclared,
			&configapi.AdmissionFairSharing{
				UsageSamplingInterval: metav1.Duration{Duration: 100 * time.Millisecond},
				UsageHalfLifeTime:     metav1.Duration{Duration: time.Hour},
			},
		))
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.ClassicalPreemptionCandidateOrderCache, false)

		ns = behavioral.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "afs-classical-preemption-")

		defaultFlavor = utiltestingapi.MakeResourceFlavor("default").Obj()
		behavioral.MustCreate(ctx, k8sClient, defaultFlavor)

		cqP = utiltestingapi.MakeClusterQueue("afs-p").
			Cohort("afs-cohort").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "4").Obj()).
			Preemption(kueue.ClusterQueuePreemption{ReclaimWithinCohort: kueue.PreemptionPolicyAny}).
			Obj()
		cqQ1 = utiltestingapi.MakeClusterQueue("afs-q1").
			Cohort("afs-cohort").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "0").Obj()).
			AdmissionMode(kueue.UsageBasedAdmissionFairSharing).
			Obj()
		cqQ2 = utiltestingapi.MakeClusterQueue("afs-q2").
			Cohort("afs-cohort").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "0").Obj()).
			Obj()
		for _, cq := range []*kueue.ClusterQueue{cqP, cqQ1, cqQ2} {
			behavioral.MustCreate(ctx, k8sClient, cq)
		}
		behavioral.ExpectClusterQueuesToBeActive(ctx, k8sClient, cqP, cqQ1, cqQ2)

		lqP = utiltestingapi.MakeLocalQueue("afs-lq-p", ns.Name).ClusterQueue(cqP.Name).Obj()
		lqA = utiltestingapi.MakeLocalQueue("afs-lq-a", ns.Name).ClusterQueue(cqQ1.Name).Obj()
		lqB = utiltestingapi.MakeLocalQueue("afs-lq-b", ns.Name).ClusterQueue(cqQ1.Name).Obj()
		lqC = utiltestingapi.MakeLocalQueue("afs-lq-c", ns.Name).ClusterQueue(cqQ2.Name).Obj()
		for _, lq := range []*kueue.LocalQueue{lqP, lqA, lqB, lqC} {
			behavioral.MustCreate(ctx, k8sClient, lq)
		}
		behavioral.ExpectLocalQueuesToBeActive(ctx, k8sClient, lqP, lqA, lqB, lqC)

		wlA = utiltestingapi.MakeWorkload("a", ns.Name).
			Queue(kueue.LocalQueueName(lqA.Name)).Priority(5).
			Request(corev1.ResourceCPU, "2").Obj()
		wlB = utiltestingapi.MakeWorkload("b", ns.Name).
			Queue(kueue.LocalQueueName(lqB.Name)).Priority(0).
			Request(corev1.ResourceCPU, "1").Obj()
		wlC = utiltestingapi.MakeWorkload("c", ns.Name).
			Queue(kueue.LocalQueueName(lqC.Name)).Priority(3).
			Request(corev1.ResourceCPU, "1").Obj()
		admittedCandidates = []*kueue.Workload{wlA, wlB, wlC}
		for _, wl := range admittedCandidates {
			behavioral.MustCreate(ctx, k8sClient, wl)
		}
		behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, admittedCandidates...)

		qManager.AfsUsageLedger.SetForTest(
			utilqueue.NewLocalQueueReference(ns.Name, kueue.LocalQueueName(lqA.Name)),
			corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("10")}, time.Now())
		qManager.AfsUsageLedger.SetForTest(
			utilqueue.NewLocalQueueReference(ns.Name, kueue.LocalQueueName(lqB.Name)),
			corev1.ResourceList{corev1.ResourceCPU: resource.MustParse("1")}, time.Now())
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(behavioral.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		for _, cq := range []*kueue.ClusterQueue{cqP, cqQ1, cqQ2} {
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
		}
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, defaultFlavor, true)
	})

	ginkgo.It("should pick the same victim on every reclaim when the candidate order cache is disabled", func() {
		victims := make(map[string]int)

		for run := range reclaimRuns {
			ginkgo.By(fmt.Sprintf("running reclaim %d/%d", run+1, reclaimRuns))

			// Each run starts from the same state: a, b and c admitted and no
			// leftover eviction marker from the previous run.
			gomega.Eventually(func(g gomega.Gomega) {
				for _, wl := range admittedCandidates {
					got := &kueue.Workload{}
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), got)).To(gomega.Succeed())
					g.Expect(meta.IsStatusConditionTrue(got.Status.Conditions, kueue.WorkloadAdmitted)).To(gomega.BeTrue())
					g.Expect(meta.IsStatusConditionTrue(got.Status.Conditions, kueue.WorkloadEvicted)).To(gomega.BeFalse())
				}
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

			incoming := utiltestingapi.MakeWorkload(fmt.Sprintf("incoming-%d", run), ns.Name).
				Queue(kueue.LocalQueueName(lqP.Name)).
				Request(corev1.ResourceCPU, "1").Obj()
			behavioral.MustCreate(ctx, k8sClient, incoming)

			var victim *kueue.Workload
			gomega.Eventually(func(g gomega.Gomega) {
				var preempted []*kueue.Workload
				for _, wl := range admittedCandidates {
					got := &kueue.Workload{}
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), got)).To(gomega.Succeed())
					cond := meta.FindStatusCondition(got.Status.Conditions, kueue.WorkloadEvicted)
					if cond != nil && cond.Status == metav1.ConditionTrue && cond.Reason == kueue.WorkloadPreempted {
						preempted = append(preempted, wl)
					}
				}
				g.Expect(preempted).To(gomega.HaveLen(1), "expected exactly one preemption victim")
				if len(preempted) == 1 {
					victim = preempted[0]
				}
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

			victims[victim.Name]++
			ginkgo.GinkgoWriter.Printf("reclaim %d: victim %s\n", run+1, victim.Name)

			behavioral.FinishEvictionForWorkloads(ctx, k8sClient, victim)
			behavioral.ExpectObjectToBeDeletedWithTimeout(ctx, k8sClient, incoming, true, behavioral.Timeout)
			behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, admittedCandidates...)
		}

		ginkgo.GinkgoWriter.Printf("victim distribution over %d reclaims: %v\n", reclaimRuns, victims)
		gomega.Expect(victims).To(gomega.Equal(map[string]int{wlC.Name: reclaimRuns}),
			"the classical preemption victim must not depend on candidate iteration order; c should be preempted in every run")
	})
})
