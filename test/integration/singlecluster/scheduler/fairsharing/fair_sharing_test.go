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
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/apimachinery/pkg/util/sets"
	"k8s.io/component-base/metrics/testutil"
	"k8s.io/utils/clock"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/metrics"
	"sigs.k8s.io/kueue/pkg/util/testing"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/test/integration/framework"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("Scheduler", func() {
	var (
		defaultFlavor *kueue.ResourceFlavor
		ns            *corev1.Namespace

		cohorts []*kueuealpha.Cohort
		cqs     []*kueue.ClusterQueue
		lqs     []*kueue.LocalQueue
		wls     []*kueue.Workload
	)

	var createCohort = func(cohort *kueuealpha.Cohort) *kueuealpha.Cohort {
		gomega.Expect(k8sClient.Create(ctx, cohort)).To(gomega.Succeed())
		cohorts = append(cohorts, cohort)
		return cohort
	}

	var createQueue = func(cq *kueue.ClusterQueue) *kueue.ClusterQueue {
		gomega.Expect(k8sClient.Create(ctx, cq)).To(gomega.Succeed())
		cqs = append(cqs, cq)

		lq := testing.MakeLocalQueue(cq.Name, ns.Name).ClusterQueue(cq.Name).Obj()
		gomega.Expect(k8sClient.Create(ctx, lq)).To(gomega.Succeed())
		lqs = append(lqs, lq)
		return cq
	}

	var createWorkload = func(queue string, cpuRequests string) *kueue.Workload {
		wl := testing.MakeWorkloadWithGeneratedName("workload-", ns.Name).
			Queue(queue).
			Request(corev1.ResourceCPU, cpuRequests).Obj()
		wls = append(wls, wl)
		gomega.Expect(k8sClient.Create(ctx, wl)).To(gomega.Succeed())
		return wl
	}

	ginkgo.BeforeEach(func() {
		defaultFlavor = testing.MakeResourceFlavor("default").Obj()
		gomega.Expect(k8sClient.Create(ctx, defaultFlavor)).To(gomega.Succeed())

		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "core-")
	})

	ginkgo.AfterEach(func() {
		for _, wl := range wls {
			util.ExpectObjectToBeDeleted(ctx, k8sClient, wl, true)
		}
		for _, lq := range lqs {
			util.ExpectObjectToBeDeleted(ctx, k8sClient, lq, true)
		}
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		for _, cq := range cqs {
			util.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
		}
		for _, cohort := range cohorts {
			util.ExpectObjectToBeDeleted(ctx, k8sClient, cohort, true)
		}
		util.ExpectObjectToBeDeleted(ctx, k8sClient, defaultFlavor, true)
	})

	ginkgo.When("Preemption is disabled", func() {
		var (
			cqA      *kueue.ClusterQueue
			cqB      *kueue.ClusterQueue
			cqShared *kueue.ClusterQueue
		)
		ginkgo.BeforeEach(func() {
			cqA = createQueue(testing.MakeClusterQueue("a").
				Cohort("all").
				ResourceGroup(
					*testing.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "3").Obj(),
				).Obj())

			cqB = createQueue(testing.MakeClusterQueue("b").
				Cohort("all").
				ResourceGroup(
					*testing.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "1").Obj(),
				).Obj())

			cqShared = createQueue(testing.MakeClusterQueue("shared").
				Cohort("all").
				ResourceGroup(
					*testing.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "4").Obj(),
				).Obj())
		})

		ginkgo.It("Admits workloads respecting fair share", framework.SlowSpec, func() {
			ginkgo.By("Saturating cq-a")

			for range 10 {
				createWorkload("a", "1")
			}
			util.ExpectReservingActiveWorkloadsMetric(cqA, 8)
			util.ExpectPendingWorkloadsMetric(cqA, 0, 2)
			util.ExpectClusterQueueWeightedShareMetric(cqA, 625)
			util.ExpectClusterQueueWeightedShareMetric(cqB, 0)
			util.ExpectClusterQueueWeightedShareMetric(cqShared, 0)

			ginkgo.By("Creating newer workloads in cq-b")
			util.WaitForNextSecondAfterCreation(wls[len(wls)-1])
			for range 5 {
				createWorkload("b", "1")
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
			wl := createWorkload("a", "10")
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
			cqB *kueue.ClusterQueue
			cqC *kueue.ClusterQueue
		)
		ginkgo.BeforeEach(func() {
			cqA = createQueue(testing.MakeClusterQueue("a").
				Cohort("all").
				ResourceGroup(
					*testing.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "3").Obj(),
				).Preemption(kueue.ClusterQueuePreemption{
				ReclaimWithinCohort: kueue.PreemptionPolicyAny,
				WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
			}).Obj())

			cqB = createQueue(testing.MakeClusterQueue("b").
				Cohort("all").
				ResourceGroup(
					*testing.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "3").Obj(),
				).Preemption(kueue.ClusterQueuePreemption{
				ReclaimWithinCohort: kueue.PreemptionPolicyAny,
				WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
			}).Obj())

			cqC = createQueue(testing.MakeClusterQueue("c").
				Cohort("all").
				ResourceGroup(
					*testing.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "3").Obj(),
				).Preemption(kueue.ClusterQueuePreemption{
				ReclaimWithinCohort: kueue.PreemptionPolicyAny,
				WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
			}).Obj())
		})

		ginkgo.It("Admits workloads respecting fair share", framework.SlowSpec, func() {
			ginkgo.By("Saturating cq-a")
			for range 10 {
				createWorkload("a", "1")
			}
			util.ExpectReservingActiveWorkloadsMetric(cqA, 9)
			util.ExpectPendingWorkloadsMetric(cqA, 0, 1)
			util.ExpectClusterQueueWeightedShareMetric(cqA, 666)
			util.ExpectClusterQueueWeightedShareMetric(cqB, 0)
			util.ExpectClusterQueueWeightedShareMetric(cqC, 0)

			ginkgo.By("Creating newer workloads in cq-b")
			for range 5 {
				createWorkload("b", "1")
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
		ginkgo.It("admits workloads respecting fair share", func() {
			//         root
			//        /    \
			//       fl     fr
			//     /   \     \
			//    sl   sr    bank
			//    /     \
			//   cqL    cqR
			cohortFirstLeft := createCohort(testing.MakeCohort("first-left").Parent("root").Obj())
			cohortFirstRight := createCohort(testing.MakeCohort("first-right").Parent("root").Obj())
			cohortSecondLeft := createCohort(testing.MakeCohort("second-left").Parent("first-left").Obj())
			cohortSecondRight := createCohort(testing.MakeCohort("second-right").Parent("first-left").Obj())
			cohortBank := createCohort(testing.MakeCohort("bank").Parent("first-right").
				ResourceGroup(
					testing.MakeFlavorQuotas(defaultFlavor.Name).Resource(corev1.ResourceCPU, "10").FlavorQuotas,
				).Obj())

			cqSecondLeft := createQueue(testing.MakeClusterQueue("second-left").
				Cohort("second-left").
				ResourceGroup(
					*testing.MakeFlavorQuotas(defaultFlavor.Name).Resource(corev1.ResourceCPU, "2").Obj(),
				).Obj())

			cqSecondRight := createQueue(testing.MakeClusterQueue("second-right").
				Cohort("second-right").
				ResourceGroup(
					*testing.MakeFlavorQuotas(defaultFlavor.Name).Resource(corev1.ResourceCPU, "2").Obj(),
				).Obj())
			expectCohortWeightedShare(cohortFirstLeft.Name, 0)
			expectCohortWeightedShare(cohortFirstRight.Name, 0)
			expectCohortWeightedShare(cohortBank.Name, 0)

			ginkgo.By("Adding workloads to cqSecondLeft and cqSecondRight in round-robin fashion")
			for range 5 {
				createWorkload("second-left", "1")
				createWorkload("second-right", "1")
			}

			util.ExpectReservingActiveWorkloadsMetric(cqSecondLeft, 5)
			util.ExpectReservingActiveWorkloadsMetric(cqSecondRight, 5)
			expectCohortWeightedShare(cohortFirstLeft.Name, 428)
			expectCohortWeightedShare(cohortFirstRight.Name, 0)
			expectCohortWeightedShare(cohortSecondLeft.Name, 214)
			expectCohortWeightedShare(cohortSecondRight.Name, 214)
			expectCohortWeightedShare(cohortBank.Name, 0)
		})
	})
})

func expectCohortWeightedShare(cohortName string, weightedShare int64) {
	// check Status
	gomega.EventuallyWithOffset(1, func(g gomega.Gomega) {
		cohort := &kueuealpha.Cohort{}
		g.ExpectWithOffset(1, k8sClient.Get(ctx, client.ObjectKey{Name: cohortName}, cohort)).Should(gomega.Succeed())
		g.ExpectWithOffset(1, cohort.Status.FairSharing).ShouldNot(gomega.BeNil())
		g.ExpectWithOffset(1, cohort.Status.FairSharing.WeightedShare).Should(gomega.Equal(weightedShare))
	}, util.Timeout, util.Interval).Should(gomega.Succeed())

	// check Metric
	metric := metrics.CohortWeightedShare.WithLabelValues(cohortName)
	gomega.EventuallyWithOffset(1, func(g gomega.Gomega) {
		v, err := testutil.GetGaugeMetricValue(metric)
		g.ExpectWithOffset(1, err).ToNot(gomega.HaveOccurred())
		g.ExpectWithOffset(1, int64(v)).Should(gomega.Equal(weightedShare))
	}, util.Timeout, util.Interval).Should(gomega.Succeed())
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
