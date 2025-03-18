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
	"fmt"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/utils/clock"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/util/testing"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/test/integration/framework"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("Scheduler", func() {
	var (
		defaultFlavor *kueue.ResourceFlavor
		ns            *corev1.Namespace
	)

	ginkgo.BeforeEach(func() {
		defaultFlavor = testing.MakeResourceFlavor("default").Obj()
		gomega.Expect(k8sClient.Create(ctx, defaultFlavor)).To(gomega.Succeed())

		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "core-")
	})

	ginkgo.AfterEach(func() {
		util.ExpectObjectToBeDeleted(ctx, k8sClient, defaultFlavor, true)
	})

	ginkgo.When("Preemption is disabled", func() {
		var (
			cqA      *kueue.ClusterQueue
			lqA      *kueue.LocalQueue
			cqB      *kueue.ClusterQueue
			lqB      *kueue.LocalQueue
			cqShared *kueue.ClusterQueue
		)
		ginkgo.BeforeEach(func() {
			cqA = testing.MakeClusterQueue("a").
				Cohort("all").
				ResourceGroup(
					*testing.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "3").Obj(),
				).Obj()
			gomega.Expect(k8sClient.Create(ctx, cqA)).To(gomega.Succeed())
			lqA = testing.MakeLocalQueue("a", ns.Name).ClusterQueue("a").Obj()
			gomega.Expect(k8sClient.Create(ctx, lqA)).To(gomega.Succeed())

			cqB = testing.MakeClusterQueue("b").
				Cohort("all").
				ResourceGroup(
					*testing.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "1").Obj(),
				).Obj()
			gomega.Expect(k8sClient.Create(ctx, cqB)).To(gomega.Succeed())
			lqB = testing.MakeLocalQueue("b", ns.Name).ClusterQueue("b").Obj()
			gomega.Expect(k8sClient.Create(ctx, lqB)).To(gomega.Succeed())

			cqShared = testing.MakeClusterQueue("shared").
				Cohort("all").
				ResourceGroup(
					*testing.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "4").Obj(),
				).Obj()
			gomega.Expect(k8sClient.Create(ctx, cqShared)).To(gomega.Succeed())
		})
		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			util.ExpectObjectToBeDeleted(ctx, k8sClient, cqA, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, cqB, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, cqShared, true)
		})

		ginkgo.It("Admits workloads respecting fair share", framework.SlowSpec, func() {
			ginkgo.By("Saturating cq-a")

			aWorkloads := make([]*kueue.Workload, 10)
			for i := range aWorkloads {
				aWorkloads[i] = testing.MakeWorkload(fmt.Sprintf("a-%d", i), ns.Name).
					Queue("a").
					Request(corev1.ResourceCPU, "1").Obj()
				gomega.Expect(k8sClient.Create(ctx, aWorkloads[i])).To(gomega.Succeed())
			}
			util.ExpectReservingActiveWorkloadsMetric(cqA, 8)
			util.ExpectPendingWorkloadsMetric(cqA, 0, 2)
			util.ExpectClusterQueueWeightedShareMetric(cqA, 625)
			util.ExpectClusterQueueWeightedShareMetric(cqB, 0)
			util.ExpectClusterQueueWeightedShareMetric(cqShared, 0)

			ginkgo.By("Creating newer workloads in cq-b")
			util.WaitForNextSecondAfterCreation(aWorkloads[len(aWorkloads)-1])
			bWorkloads := make([]*kueue.Workload, 5)
			for i := range bWorkloads {
				bWorkloads[i] = testing.MakeWorkload(fmt.Sprintf("b-%d", i), ns.Name).
					Queue("b").
					Request(corev1.ResourceCPU, "1").Obj()
				gomega.Expect(k8sClient.Create(ctx, bWorkloads[i])).To(gomega.Succeed())
			}
			util.ExpectPendingWorkloadsMetric(cqB, 0, 5)
			util.ExpectClusterQueueWeightedShareMetric(cqA, 625)
			util.ExpectClusterQueueWeightedShareMetric(cqB, 0)
			util.ExpectClusterQueueWeightedShareMetric(cqShared, 0)

			ginkgo.By("Terminating 4 running workloads in cqA: shared quota is fair-shared")
			finishRunningWorkloadsInCQ(cqA, 4)

			// Admits 1 from cqA and 3 from cqB.
			util.ExpectReservingActiveWorkloadsMetric(cqA, 5)
			util.ExpectPendingWorkloadsMetric(cqA, 0, 1)
			util.ExpectReservingActiveWorkloadsMetric(cqB, 3)
			util.ExpectPendingWorkloadsMetric(cqB, 0, 2)
			util.ExpectClusterQueueWeightedShareMetric(cqA, 250)
			util.ExpectClusterQueueWeightedShareMetric(cqB, 250)
			util.ExpectClusterQueueWeightedShareMetric(cqShared, 0)

			ginkgo.By("Terminating 2 more running workloads in cqA: cqB starts to take over shared quota")
			finishRunningWorkloadsInCQ(cqA, 2)

			// Admits last 1 from cqA and 1 from cqB.
			util.ExpectReservingActiveWorkloadsMetric(cqA, 4)
			util.ExpectPendingWorkloadsMetric(cqA, 0, 0)
			util.ExpectReservingActiveWorkloadsMetric(cqB, 4)
			util.ExpectPendingWorkloadsMetric(cqB, 0, 1)
			util.ExpectClusterQueueWeightedShareMetric(cqA, 125)
			util.ExpectClusterQueueWeightedShareMetric(cqB, 375)
			util.ExpectClusterQueueWeightedShareMetric(cqShared, 0)

			ginkgo.By("Checking that weight share status changed")
			cqAKey := client.ObjectKeyFromObject(cqA)
			createdCqA := &kueue.ClusterQueue{}
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, cqAKey, createdCqA)).Should(gomega.Succeed())
				g.Expect(createdCqA.Status.FairSharing).Should(gomega.BeComparableTo(&kueue.FairSharingStatus{WeightedShare: 125}))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.It("Shouldn't reserve quota because not enough resources", func() {
			wl := testing.MakeWorkload("wl", ns.Name).
				Queue(lqA.Name).
				Request("cpu", "10").
				Obj()
			gomega.Expect(k8sClient.Create(ctx, wl)).Should(gomega.Succeed())

			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), wl)).Should(gomega.Succeed())
				g.Expect(wl.Status.Conditions).To(gomega.ContainElements(
					gomega.BeComparableTo(metav1.Condition{
						Type:    kueue.WorkloadQuotaReserved,
						Status:  metav1.ConditionFalse,
						Reason:  "Pending",
						Message: "couldn't assign flavors to pod set main: insufficient quota for cpu in flavor default, request > maximum capacity (10 > 8)",
					}, util.IgnoreConditionTimestampsAndObservedGeneration),
				))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})
	})

	ginkgo.When("Preemption is enabled", func() {
		var (
			cqA *kueue.ClusterQueue
			lqA *kueue.LocalQueue
			cqB *kueue.ClusterQueue
			lqB *kueue.LocalQueue
			cqC *kueue.ClusterQueue
			lqC *kueue.LocalQueue
		)
		ginkgo.BeforeEach(func() {
			cqA = testing.MakeClusterQueue("a").
				Cohort("all").
				ResourceGroup(
					*testing.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "3").Obj(),
				).Preemption(kueue.ClusterQueuePreemption{
				ReclaimWithinCohort: kueue.PreemptionPolicyAny,
				WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
			}).Obj()
			gomega.Expect(k8sClient.Create(ctx, cqA)).To(gomega.Succeed())
			lqA = testing.MakeLocalQueue("a", ns.Name).ClusterQueue("a").Obj()
			gomega.Expect(k8sClient.Create(ctx, lqA)).To(gomega.Succeed())

			cqB = testing.MakeClusterQueue("b").
				Cohort("all").
				ResourceGroup(
					*testing.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "3").Obj(),
				).Preemption(kueue.ClusterQueuePreemption{
				ReclaimWithinCohort: kueue.PreemptionPolicyAny,
				WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
			}).Obj()
			gomega.Expect(k8sClient.Create(ctx, cqB)).To(gomega.Succeed())
			lqB = testing.MakeLocalQueue("b", ns.Name).ClusterQueue("b").Obj()
			gomega.Expect(k8sClient.Create(ctx, lqB)).To(gomega.Succeed())

			cqC = testing.MakeClusterQueue("c").
				Cohort("all").
				ResourceGroup(
					*testing.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "3").Obj(),
				).Preemption(kueue.ClusterQueuePreemption{
				ReclaimWithinCohort: kueue.PreemptionPolicyAny,
				WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
			}).Obj()
			gomega.Expect(k8sClient.Create(ctx, cqC)).To(gomega.Succeed())
			lqC = testing.MakeLocalQueue("c", ns.Name).ClusterQueue("c").Obj()
			gomega.Expect(k8sClient.Create(ctx, lqC)).To(gomega.Succeed())
		})
		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			gomega.Expect(util.DeleteObject(ctx, k8sClient, cqA)).To(gomega.Succeed())
			gomega.Expect(util.DeleteObject(ctx, k8sClient, cqB)).To(gomega.Succeed())
			gomega.Expect(util.DeleteObject(ctx, k8sClient, cqC)).To(gomega.Succeed())
		})

		ginkgo.It("Admits workloads respecting fair share", framework.SlowSpec, func() {
			ginkgo.By("Saturating cq-a")

			aWorkloads := make([]*kueue.Workload, 10)
			for i := range aWorkloads {
				aWorkloads[i] = testing.MakeWorkload(fmt.Sprintf("a-%d", i), ns.Name).
					Queue("a").
					Request(corev1.ResourceCPU, "1").Obj()
				gomega.Expect(k8sClient.Create(ctx, aWorkloads[i])).To(gomega.Succeed())
			}
			util.ExpectReservingActiveWorkloadsMetric(cqA, 9)
			util.ExpectPendingWorkloadsMetric(cqA, 0, 1)
			util.ExpectClusterQueueWeightedShareMetric(cqA, 666)
			util.ExpectClusterQueueWeightedShareMetric(cqB, 0)
			util.ExpectClusterQueueWeightedShareMetric(cqC, 0)

			ginkgo.By("Creating newer workloads in cq-b")
			bWorkloads := make([]*kueue.Workload, 5)
			for i := range bWorkloads {
				bWorkloads[i] = testing.MakeWorkload(fmt.Sprintf("b-%d", i), ns.Name).
					Queue("b").
					Request(corev1.ResourceCPU, "1").Obj()
				gomega.Expect(k8sClient.Create(ctx, bWorkloads[i])).To(gomega.Succeed())
			}
			util.ExpectPendingWorkloadsMetric(cqB, 5, 0)

			ginkgo.By("Finishing eviction of 4 running workloads in cqA: shared quota is fair-shared")
			finishEvictionOfWorkloadsInCQ(cqA, 4)
			util.ExpectReservingActiveWorkloadsMetric(cqB, 4)
			util.ExpectClusterQueueWeightedShareMetric(cqA, 222)
			util.ExpectClusterQueueWeightedShareMetric(cqB, 111)
			util.ExpectClusterQueueWeightedShareMetric(cqC, 0)

			ginkgo.By("cq-c reclaims one unit, preemption happens in cq-a")
			cWorkload := testing.MakeWorkload("c0", ns.Name).Queue("c").Request(corev1.ResourceCPU, "1").Obj()
			gomega.Expect(k8sClient.Create(ctx, cWorkload)).To(gomega.Succeed())
			util.ExpectPendingWorkloadsMetric(cqC, 1, 0)
			util.ExpectClusterQueueWeightedShareMetric(cqA, 222)
			util.ExpectClusterQueueWeightedShareMetric(cqB, 111)
			util.ExpectClusterQueueWeightedShareMetric(cqC, 0)

			ginkgo.By("Finishing eviction of 1 running workloads in the CQ with highest usage: cqA")
			finishEvictionOfWorkloadsInCQ(cqA, 1)
			util.ExpectReservingActiveWorkloadsMetric(cqC, 1)
			util.ExpectClusterQueueWeightedShareMetric(cqA, 111)
			util.ExpectClusterQueueWeightedShareMetric(cqB, 111)
			util.ExpectClusterQueueWeightedShareMetric(cqC, 0)

			ginkgo.By("Checking that weight share status changed")
			cqAKey := client.ObjectKeyFromObject(cqA)
			createdCqA := &kueue.ClusterQueue{}
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, cqAKey, createdCqA)).Should(gomega.Succeed())
				g.Expect(createdCqA.Status.FairSharing).ShouldNot(gomega.BeNil())
				g.Expect(createdCqA.Status.FairSharing).Should(gomega.BeComparableTo(&kueue.FairSharingStatus{WeightedShare: 111}))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})
	})

	ginkgo.When("using hierarchical cohorts", func() {
		var (
			cohortFirstLeft   *kueuealpha.Cohort
			cohortFirstRight  *kueuealpha.Cohort
			cohortSecondLeft  *kueuealpha.Cohort
			cohortSecondRight *kueuealpha.Cohort
			cohortBank        *kueuealpha.Cohort
			cqSecondLeft      *kueue.ClusterQueue
			cqSecondRight     *kueue.ClusterQueue
			lqSecondLeft      *kueue.LocalQueue
			lqSecondRight     *kueue.LocalQueue
		)

		ginkgo.BeforeEach(func() {
			//         root
			//        /    \
			//       fl     fr
			//     /   \     \
			//    sl   sr    bank
			//    /     \
			//   cqL    cqR

			cohortFirstLeft = testing.MakeCohort("first-left").Parent("root").Obj()
			gomega.Expect(k8sClient.Create(ctx, cohortFirstLeft)).To(gomega.Succeed())

			cohortFirstRight = testing.MakeCohort("first-right").Parent("root").Obj()
			gomega.Expect(k8sClient.Create(ctx, cohortFirstRight)).To(gomega.Succeed())

			cohortSecondLeft = testing.MakeCohort("second-left").Parent(kueue.CohortReference(cohortFirstLeft.Name)).Obj()
			gomega.Expect(k8sClient.Create(ctx, cohortSecondLeft)).To(gomega.Succeed())

			cohortSecondRight = testing.MakeCohort("second-right").Parent(kueue.CohortReference(cohortFirstLeft.Name)).Obj()
			gomega.Expect(k8sClient.Create(ctx, cohortSecondRight)).To(gomega.Succeed())

			cohortBank = testing.MakeCohort("bank").Parent(kueue.CohortReference(cohortFirstRight.Name)).
				ResourceGroup(
					testing.MakeFlavorQuotas(defaultFlavor.Name).Resource(corev1.ResourceCPU, "10").FlavorQuotas,
				).Obj()
			gomega.Expect(k8sClient.Create(ctx, cohortBank)).To(gomega.Succeed())

			cqSecondLeft = testing.MakeClusterQueue("second-left").
				Cohort(kueue.CohortReference(cohortSecondLeft.Name)).
				ResourceGroup(
					*testing.MakeFlavorQuotas(defaultFlavor.Name).Resource(corev1.ResourceCPU, "2").Obj(),
				).Obj()
			gomega.Expect(k8sClient.Create(ctx, cqSecondLeft)).To(gomega.Succeed())

			cqSecondRight = testing.MakeClusterQueue("second-right").
				Cohort(kueue.CohortReference(cohortSecondRight.Name)).
				ResourceGroup(
					*testing.MakeFlavorQuotas(defaultFlavor.Name).Resource(corev1.ResourceCPU, "2").Obj(),
				).Obj()
			gomega.Expect(k8sClient.Create(ctx, cqSecondRight)).To(gomega.Succeed())

			lqSecondLeft = testing.MakeLocalQueue("second-left", ns.Name).ClusterQueue(cqSecondLeft.Name).Obj()
			gomega.Expect(k8sClient.Create(ctx, lqSecondLeft)).To(gomega.Succeed())

			lqSecondRight = testing.MakeLocalQueue("second-right", ns.Name).ClusterQueue(cqSecondRight.Name).Obj()
			gomega.Expect(k8sClient.Create(ctx, lqSecondRight)).To(gomega.Succeed())
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			util.ExpectObjectToBeDeleted(ctx, k8sClient, cqSecondLeft, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, cqSecondRight, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, cohortFirstLeft, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, cohortFirstRight, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, cohortSecondLeft, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, cohortSecondRight, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, cohortBank, true)
		})

		ginkgo.It("admits workloads respecting fair share", func() {
			util.ExpectCohortWeightedShareMetric(cohortFirstLeft, 0)
			util.ExpectCohortWeightedShareMetric(cohortFirstRight, 0)
			util.ExpectCohortWeightedShareMetric(cohortBank, 0)

			ginkgo.By("Adding workloads to cqSecondLeft and cqSecondRight in round-robin fashion")
			const totalWorkloads = 10
			workloads := make([]*kueue.Workload, totalWorkloads)
			for i := 0; i < totalWorkloads; i++ {
				if i%2 == 0 {
					// Add to cqSecondLeft on even iterations
					workloads[i] = testing.MakeWorkload(fmt.Sprintf("wl-left-%d", i/2), ns.Name).
						Queue(cqSecondLeft.Name).
						Request(corev1.ResourceCPU, "1").Obj()
				} else {
					// Add to cqSecondRight on odd iterations
					workloads[i] = testing.MakeWorkload(fmt.Sprintf("wl-right-%d", i/2), ns.Name).
						Queue(cqSecondRight.Name).
						Request(corev1.ResourceCPU, "1").Obj()
				}
				gomega.Expect(k8sClient.Create(ctx, workloads[i])).To(gomega.Succeed())
			}

			util.ExpectReservingActiveWorkloadsMetric(cqSecondLeft, 5)
			util.ExpectReservingActiveWorkloadsMetric(cqSecondRight, 5)
			util.ExpectCohortWeightedShareMetric(cohortFirstLeft, 428)
			util.ExpectCohortWeightedShareMetric(cohortFirstRight, 0)
			util.ExpectCohortWeightedShareMetric(cohortSecondLeft, 214)
			util.ExpectCohortWeightedShareMetric(cohortSecondRight, 214)
			util.ExpectCohortWeightedShareMetric(cohortBank, 0)

			expectCohortWeightedShare(cohortFirstLeft.Name, 428)
			expectCohortWeightedShare(cohortFirstRight.Name, 0)
			expectCohortWeightedShare(cohortSecondLeft.Name, 214)
			expectCohortWeightedShare(cohortSecondRight.Name, 214)
			expectCohortWeightedShare(cohortBank.Name, 0)
		})
	})
})

func expectCohortWeightedShare(cohortName string, weightedShare int64) {
	cohort := &kueuealpha.Cohort{}
	gomega.ExpectWithOffset(1, k8sClient.Get(ctx, client.ObjectKey{Name: cohortName}, cohort)).Should(gomega.Succeed())
	gomega.ExpectWithOffset(1, cohort.Status.FairSharing).ShouldNot(gomega.BeNil())
	gomega.ExpectWithOffset(1, cohort.Status.FairSharing.WeightedShare).Should(gomega.Equal(weightedShare))
}

func finishRunningWorkloadsInCQ(cq *kueue.ClusterQueue, n int) {
	var wList kueue.WorkloadList
	gomega.ExpectWithOffset(1, k8sClient.List(ctx, &wList)).To(gomega.Succeed())
	finished := 0
	for i := 0; i < len(wList.Items) && finished < n; i++ {
		wl := wList.Items[i]
		if wl.Status.Admission != nil && string(wl.Status.Admission.ClusterQueue) == cq.Name && !meta.IsStatusConditionTrue(wl.Status.Conditions, kueue.WorkloadFinished) {
			util.FinishWorkloads(ctx, k8sClient, &wl)
			finished++
		}
	}
	gomega.ExpectWithOffset(1, finished).To(gomega.Equal(n), "Not enough workloads finished")
}

func finishEvictionOfWorkloadsInCQ(cq *kueue.ClusterQueue, n int) {
	var realClock = clock.RealClock{}
	finished := sets.New[types.UID]()
	gomega.EventuallyWithOffset(1, func(g gomega.Gomega) {
		var wList kueue.WorkloadList
		g.Expect(k8sClient.List(ctx, &wList)).To(gomega.Succeed())
		for i := 0; i < len(wList.Items) && finished.Len() < n; i++ {
			wl := wList.Items[i]
			if wl.Status.Admission == nil || string(wl.Status.Admission.ClusterQueue) != cq.Name {
				continue
			}
			evicted := meta.IsStatusConditionTrue(wl.Status.Conditions, kueue.WorkloadEvicted)
			quotaReserved := meta.IsStatusConditionTrue(wl.Status.Conditions, kueue.WorkloadQuotaReserved)
			if evicted && quotaReserved {
				workload.UnsetQuotaReservationWithCondition(&wl, "Pending", "Eviction finished by test", time.Now())
				g.Expect(workload.ApplyAdmissionStatus(ctx, k8sClient, &wl, true, realClock)).To(gomega.Succeed())
				finished.Insert(wl.UID)
			}
		}
		g.Expect(finished.Len()).Should(gomega.Equal(n), "Not enough workloads evicted")
	}, util.Timeout, util.Interval).Should(gomega.Succeed())
}
