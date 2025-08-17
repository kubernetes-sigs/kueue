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
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/component-base/metrics/testutil"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	config "sigs.k8s.io/kueue/apis/config/v1beta1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/metrics"
	"sigs.k8s.io/kueue/pkg/util/testing"
	"sigs.k8s.io/kueue/test/integration/framework"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("Scheduler", ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	var (
		defaultFlavor *kueue.ResourceFlavor
		ns            *corev1.Namespace

		cohorts []*kueue.Cohort
		cqs     []*kueue.ClusterQueue
		lqs     []*kueue.LocalQueue
		wls     []*kueue.Workload
	)

	var createCohort = func(cohort *kueue.Cohort) *kueue.Cohort {
		util.MustCreate(ctx, k8sClient, cohort)
		cohorts = append(cohorts, cohort)
		return cohort
	}

	var createQueue = func(cq *kueue.ClusterQueue) *kueue.ClusterQueue {
		util.MustCreate(ctx, k8sClient, cq)
		cqs = append(cqs, cq)

		lq := testing.MakeLocalQueue(cq.Name, ns.Name).ClusterQueue(cq.Name).Obj()
		util.MustCreate(ctx, k8sClient, lq)
		lqs = append(lqs, lq)
		return cq
	}

	var createWorkloadWithPriority = func(queue kueue.LocalQueueName, cpuRequests string, priority int32) *kueue.Workload {
		wl := testing.MakeWorkloadWithGeneratedName("workload-", ns.Name).
			Priority(priority).
			Queue(queue).
			Request(corev1.ResourceCPU, cpuRequests).Obj()
		wls = append(wls, wl)
		util.MustCreate(ctx, k8sClient, wl)
		return wl
	}

	var createWorkload = func(queue kueue.LocalQueueName, cpuRequests string) *kueue.Workload {
		return createWorkloadWithPriority(queue, cpuRequests, 0)
	}

	ginkgo.BeforeAll(func() {
		fwk.StartManager(ctx, cfg, managerAndSchedulerSetup(
			&config.AdmissionFairSharing{
				UsageHalfLifeTime: metav1.Duration{
					Duration: 1 * time.Second,
				},
				UsageSamplingInterval: metav1.Duration{
					Duration: 1 * time.Second,
				},
			},
		))
	})

	ginkgo.AfterAll(func() {
		fwk.StopManager(ctx)
	})

	ginkgo.BeforeEach(func() {
		defaultFlavor = testing.MakeResourceFlavor("default").Obj()
		util.MustCreate(ctx, k8sClient, defaultFlavor)

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
			util.FinishRunningWorkloadsInCQ(ctx, k8sClient, cqA, 4)

			// Admits 1 from cqA and 3 from cqB.
			util.ExpectReservingActiveWorkloadsMetric(cqA, 5)
			util.ExpectPendingWorkloadsMetric(cqA, 0, 1)
			util.ExpectReservingActiveWorkloadsMetric(cqB, 3)
			util.ExpectPendingWorkloadsMetric(cqB, 0, 2)
			util.ExpectClusterQueueWeightedShareMetric(cqA, 250)
			util.ExpectClusterQueueWeightedShareMetric(cqB, 250)
			util.ExpectClusterQueueWeightedShareMetric(cqShared, 0)

			ginkgo.By("Terminating 2 more running workloads in cqA: cqB starts to take over shared quota")
			util.FinishRunningWorkloadsInCQ(ctx, k8sClient, cqA, 2)

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
			util.FinishEvictionOfWorkloadsInCQ(ctx, k8sClient, cqA, 4)
			util.ExpectReservingActiveWorkloadsMetric(cqB, 4)
			util.ExpectClusterQueueWeightedShareMetric(cqA, 222)
			util.ExpectClusterQueueWeightedShareMetric(cqB, 111)
			util.ExpectClusterQueueWeightedShareMetric(cqC, 0)

			ginkgo.By("cq-c reclaims one unit, preemption happens in cq-a")
			cWorkload := testing.MakeWorkload("c0", ns.Name).Queue("c").Request(corev1.ResourceCPU, "1").Obj()
			util.MustCreate(ctx, k8sClient, cWorkload)
			util.ExpectPendingWorkloadsMetric(cqC, 1, 0)
			util.ExpectClusterQueueWeightedShareMetric(cqA, 222)
			util.ExpectClusterQueueWeightedShareMetric(cqB, 111)
			util.ExpectClusterQueueWeightedShareMetric(cqC, 0)

			ginkgo.By("Finishing eviction of 1 running workloads in the CQ with highest usage: cqA")
			util.FinishEvictionOfWorkloadsInCQ(ctx, k8sClient, cqA, 1)
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
					*testing.MakeFlavorQuotas(defaultFlavor.Name).Resource(corev1.ResourceCPU, "10").Obj(),
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
		ginkgo.It("preempts workloads to enforce fair share", func() {
			// below are Cohorts and their fair
			// weights. 12 CPUs are provided by the root
			// Cohort
			//            root
			//          /      \
			//        /          \
			// best-effort(0.5)  research
			//                   /   |    \
			//                  /    |     \
			//          chemistry  physics   llm(2.0)
			createCohort(testing.MakeCohort("root").ResourceGroup(*testing.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "12").Obj()).Obj())
			createCohort(testing.MakeCohort("research").Parent("root").Obj())
			createCohort(testing.MakeCohort("chemistry").Parent("research").Obj())
			createCohort(testing.MakeCohort("physics").Parent("research").Obj())
			createCohort(testing.MakeCohort("llm").FairWeight(resource.MustParse("2.0")).Parent("research").Obj())
			createCohort(testing.MakeCohort("best-effort").FairWeight(resource.MustParse("0.5")).Parent("root").Obj())

			preemptionPolicy := kueue.ClusterQueuePreemption{
				ReclaimWithinCohort: kueue.PreemptionPolicyLowerPriority,
			}
			zeroQuota := *testing.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "0").Obj()

			chemistryQueue := createQueue(testing.MakeClusterQueue("chemistry-queue").Cohort("chemistry").ResourceGroup(zeroQuota).Preemption(preemptionPolicy).Obj())
			physicsQueue := createQueue(testing.MakeClusterQueue("physics-queue").Cohort("physics").ResourceGroup(zeroQuota).Preemption(preemptionPolicy).Obj())
			llmQueue := createQueue(testing.MakeClusterQueue("llm-queue").Cohort("llm").ResourceGroup(zeroQuota).Preemption(preemptionPolicy).Obj())
			bestEffortQueue := createQueue(testing.MakeClusterQueue("best-effort-queue").Cohort("best-effort").ResourceGroup(zeroQuota).Obj())

			ginkgo.By("all capacity used")
			for range 6 {
				createWorkloadWithPriority(kueue.LocalQueueName(bestEffortQueue.GetName()), "1", -1)
				createWorkloadWithPriority(kueue.LocalQueueName(physicsQueue.GetName()), "1", -1)
			}
			expectCohortWeightedShare("best-effort", 1000)
			expectCohortWeightedShare("physics", 500)
			util.ExpectReservingActiveWorkloadsMetric(bestEffortQueue, 6)
			util.ExpectReservingActiveWorkloadsMetric(physicsQueue, 6)

			ginkgo.By("create high priority workloads")
			for range 6 {
				for _, cq := range cqs {
					createWorkloadWithPriority(kueue.LocalQueueName(cq.GetName()), "1", 100)
				}
			}

			ginkgo.By("preempt workloads")
			util.FinishEvictionOfWorkloadsInCQ(ctx, k8sClient, bestEffortQueue, 2)
			util.FinishEvictionOfWorkloadsInCQ(ctx, k8sClient, physicsQueue, 4)

			ginkgo.By("share is fair with respect to each parent")
			// parent root
			expectCohortWeightedShare("best-effort", 666)
			expectCohortWeightedShare("research", 666)
			// parent research
			expectCohortWeightedShare("chemistry", 166)
			expectCohortWeightedShare("physics", 166)
			expectCohortWeightedShare("llm", 166)

			ginkgo.By("number workloads admitted proportional to share at each level")
			util.ExpectReservingActiveWorkloadsMetric(bestEffortQueue, 4)
			util.ExpectReservingActiveWorkloadsMetric(chemistryQueue, 2)
			util.ExpectReservingActiveWorkloadsMetric(physicsQueue, 2)
			util.ExpectReservingActiveWorkloadsMetric(llmQueue, 4)
		})
	})

	ginkgo.When("Using AdmissionFairSharing at ClusterQueue level", func() {
		var (
			cq1 *kueue.ClusterQueue
			lqA *kueue.LocalQueue
			lqB *kueue.LocalQueue
			lqC *kueue.LocalQueue
		)

		ginkgo.BeforeEach(func() {
			gomega.Expect(features.SetEnable(features.AdmissionFairSharing, true)).To(gomega.Succeed())

			cq1 = testing.MakeClusterQueue("cq1").
				ResourceGroup(*testing.MakeFlavorQuotas(defaultFlavor.Name).Resource(corev1.ResourceCPU, "8").Obj()).
				AdmissionMode(kueue.UsageBasedAdmissionFairSharing).
				Obj()
			cqs = append(cqs, cq1)
			util.MustCreate(ctx, k8sClient, cq1)

			lqA = testing.MakeLocalQueue("lq-a", ns.Name).
				FairSharing(&kueue.FairSharing{Weight: ptr.To(resource.MustParse("1"))}).
				ClusterQueue(cq1.Name).Obj()
			lqB = testing.MakeLocalQueue("lq-b", ns.Name).
				FairSharing(&kueue.FairSharing{Weight: ptr.To(resource.MustParse("1"))}).
				ClusterQueue(cq1.Name).Obj()
			lqC = testing.MakeLocalQueue("lq-c", ns.Name).
				FairSharing(&kueue.FairSharing{Weight: ptr.To(resource.MustParse("1"))}).
				ClusterQueue(cq1.Name).Obj()
			lqs = append(lqs, lqA)
			lqs = append(lqs, lqB)
			lqs = append(lqs, lqC)

			util.MustCreate(ctx, k8sClient, lqA)
			util.MustCreate(ctx, k8sClient, lqB)
			util.MustCreate(ctx, k8sClient, lqC)
		})

		ginkgo.It("admits one workload from each LocalQueue when quota is limited", func() {
			ginkgo.By("Saturating the cq with lq-a and lq-b")
			initialWls := []*kueue.Workload{
				createWorkload("lq-a", "4"),
				createWorkload("lq-b", "4"),
			}
			util.ExpectReservingActiveWorkloadsMetric(cq1, 2)

			ginkgo.By("Creating two pending workloads for each lq")
			lqAWls := []*kueue.Workload{
				createWorkload("lq-a", "4"),
				createWorkload("lq-a", "4"),
			}
			util.ExpectPendingWorkloadsMetric(cq1, 0, 2)

			lqBWls := []*kueue.Workload{
				createWorkload("lq-b", "4"),
				createWorkload("lq-b", "4"),
			}
			util.ExpectPendingWorkloadsMetric(cq1, 0, 4)

			ginkgo.By("Checking that LQ's resource usage is updated")
			util.ExpectLocalQueueFairSharingUsageToBe(ctx, k8sClient, client.ObjectKeyFromObject(lqA), ">", 3_900)
			util.ExpectLocalQueueFairSharingUsageToBe(ctx, k8sClient, client.ObjectKeyFromObject(lqB), ">", 3_900)

			ginkgo.By("Releasing quota")
			util.FinishWorkloads(ctx, k8sClient, initialWls...)

			ginkgo.By("Verifying one workload from each lq is admitted")
			util.ExpectWorkloadsToBeAdmittedCount(ctx, k8sClient, 1, lqAWls...)
			util.ExpectWorkloadsToBeAdmittedCount(ctx, k8sClient, 1, lqBWls...)
		})

		ginkgo.It("prioritizes workloads from less active LocalQueues to maintain fairness", func() {
			ginkgo.By("Saturating the cq with lq-a")
			initialWls := []*kueue.Workload{
				createWorkload("lq-a", "4"),
				createWorkload("lq-a", "4"),
			}
			util.ExpectReservingActiveWorkloadsMetric(cq1, 2)

			ginkgo.By("Creating pending workloads for lq-a")
			_ = createWorkload("lq-a", "4")
			_ = createWorkload("lq-a", "4")
			util.ExpectPendingWorkloadsMetric(cq1, 0, 2)

			ginkgo.By("Creating a pending workload for lq-b")
			wlB := createWorkload("lq-b", "4")
			util.ExpectPendingWorkloadsMetric(cq1, 0, 3)

			ginkgo.By("Checking that LQ's resource usage is updated")
			util.ExpectLocalQueueFairSharingUsageToBe(ctx, k8sClient, client.ObjectKeyFromObject(lqA), ">", 7_500)
			util.ExpectLocalQueueFairSharingUsageToBe(ctx, k8sClient, client.ObjectKeyFromObject(lqB), "==", 0)

			ginkgo.By("Releasing quota")
			util.FinishWorkloads(ctx, k8sClient, initialWls...)

			ginkgo.By("Verifying workload from lq-b is admitted")
			util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wlB)
		})

		ginkgo.It("admits workload from new LocalQueue when all others have high usage", func() {
			ginkgo.By("Saturating the cq with lq-a and lq-b")
			initialWls := []*kueue.Workload{
				createWorkload("lq-a", "4"),
				createWorkload("lq-b", "4"),
			}
			util.ExpectReservingActiveWorkloadsMetric(cq1, 2)

			ginkgo.By("Creating pending workloads for lq-a and lq-b")
			createWorkload("lq-a", "4")
			createWorkload("lq-a", "4")
			createWorkload("lq-b", "4")
			createWorkload("lq-b", "4")
			util.ExpectPendingWorkloadsMetric(cq1, 0, 4)

			ginkgo.By("Creating a pending workload for lq-c")
			wlC := createWorkload("lq-c", "4")
			util.ExpectPendingWorkloadsMetric(cq1, 0, 5)

			ginkgo.By("Checking that LQ's resource usage is updated")
			util.ExpectLocalQueueFairSharingUsageToBe(ctx, k8sClient, client.ObjectKeyFromObject(lqA), ">", 3_900)
			util.ExpectLocalQueueFairSharingUsageToBe(ctx, k8sClient, client.ObjectKeyFromObject(lqB), ">", 3_900)
			util.ExpectLocalQueueFairSharingUsageToBe(ctx, k8sClient, client.ObjectKeyFromObject(lqC), "==", 0)

			ginkgo.By("Releasing quota")
			util.FinishWorkloads(ctx, k8sClient, initialWls...)

			ginkgo.By("Verifying workload from lq-c is admitted")
			util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wlC)
		})

		ginkgo.It("admits workloads from less active LocalQueues after quota is released", func() {
			ginkgo.By("Saturating the cq with lq-a")
			initialWls := []*kueue.Workload{
				createWorkload("lq-a", "4"),
				createWorkload("lq-a", "4"),
			}
			util.ExpectReservingActiveWorkloadsMetric(cq1, 2)

			ginkgo.By("Creating pending workloads for lq-b")
			lqBWls := []*kueue.Workload{
				createWorkload("lq-b", "4"),
				createWorkload("lq-b", "4"),
			}
			util.ExpectPendingWorkloadsMetric(cq1, 0, 2)

			ginkgo.By("Creating a pending workload for lq-c")
			wlC := createWorkload("lq-c", "4")
			util.ExpectPendingWorkloadsMetric(cq1, 0, 3)

			ginkgo.By("Checking that LQ's resource usage is updated")
			util.ExpectLocalQueueFairSharingUsageToBe(ctx, k8sClient, client.ObjectKeyFromObject(lqA), ">", 7_500)
			util.ExpectLocalQueueFairSharingUsageToBe(ctx, k8sClient, client.ObjectKeyFromObject(lqB), "==", 0)
			util.ExpectLocalQueueFairSharingUsageToBe(ctx, k8sClient, client.ObjectKeyFromObject(lqC), "==", 0)

			ginkgo.By("Releasing quota")
			util.FinishWorkloads(ctx, k8sClient, initialWls...)

			ginkgo.By("Verifying one workload from lq-b and one from lq-c to be admitted")
			util.ExpectWorkloadsToBeAdmittedCount(ctx, k8sClient, 1, lqBWls...)
			util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wlC)
		})
	})
})

func expectCohortWeightedShare(cohortName string, weightedShare int64) {
	// check Status
	gomega.EventuallyWithOffset(1, func(g gomega.Gomega) {
		cohort := &kueue.Cohort{}
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

var _ = ginkgo.Describe("Scheduler", ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	var (
		defaultFlavor *kueue.ResourceFlavor
		ns            *corev1.Namespace

		cohorts []*kueue.Cohort
		cqs     []*kueue.ClusterQueue
		lqs     []*kueue.LocalQueue
		wls     []*kueue.Workload
	)

	var createWorkloadWithPriority = func(queue kueue.LocalQueueName, cpuRequests string, priority int32) *kueue.Workload {
		wl := testing.MakeWorkloadWithGeneratedName("workload-", ns.Name).
			Priority(priority).
			Queue(queue).
			Request(corev1.ResourceCPU, cpuRequests).Obj()
		wls = append(wls, wl)
		util.MustCreate(ctx, k8sClient, wl)
		return wl
	}

	var createWorkload = func(queue kueue.LocalQueueName, cpuRequests string) *kueue.Workload {
		return createWorkloadWithPriority(queue, cpuRequests, 0)
	}

	ginkgo.BeforeAll(func() {
		fwk.StartManager(ctx, cfg, managerAndSchedulerSetup(
			&config.AdmissionFairSharing{
				UsageHalfLifeTime: metav1.Duration{
					Duration: 1 * time.Second,
				},
				UsageSamplingInterval: metav1.Duration{
					Duration: 1 * time.Second,
				},
			},
		))
	})

	ginkgo.AfterAll(func() {
		fwk.StopManager(ctx)
	})

	ginkgo.BeforeEach(func() {
		defaultFlavor = testing.MakeResourceFlavor("default").Obj()
		util.MustCreate(ctx, k8sClient, defaultFlavor)

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

	ginkgo.When("Using AdmissionFairSharing at Cohort level", func() {
		var (
			cq1 *kueue.ClusterQueue
			cq2 *kueue.ClusterQueue
			lqA *kueue.LocalQueue
			lqB *kueue.LocalQueue
			lqC *kueue.LocalQueue
		)

		ginkgo.BeforeEach(func() {
			gomega.Expect(features.SetEnable(features.AdmissionFairSharing, true)).To(gomega.Succeed())

			cq1 = testing.MakeClusterQueue("cq1").
				Cohort("all").
				ResourceGroup(*testing.MakeFlavorQuotas(defaultFlavor.Name).Resource(corev1.ResourceCPU, "16").Obj()).
				Preemption(kueue.ClusterQueuePreemption{WithinClusterQueue: kueue.PreemptionPolicyNever}).
				QueueingStrategy(kueue.StrictFIFO).
				AdmissionMode(kueue.UsageBasedAdmissionFairSharing).
				Obj()
			util.MustCreate(ctx, k8sClient, cq1)
			cqs = append(cqs, cq1)

			cq2 = testing.MakeClusterQueue("cq2").
				Cohort("all").
				ResourceGroup(
					*testing.MakeFlavorQuotas("default").Resource(corev1.ResourceCPU, "16").Obj(),
				).Preemption(kueue.ClusterQueuePreemption{
				ReclaimWithinCohort: kueue.PreemptionPolicyAny,
				WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
			}).Obj()
			util.MustCreate(ctx, k8sClient, cq2)
			cqs = append(cqs, cq2)

			lqA = testing.MakeLocalQueue("lq-a", ns.Name).
				FairSharing(&kueue.FairSharing{Weight: ptr.To(resource.MustParse("1"))}).
				ClusterQueue(cq1.Name).Obj()
			lqB = testing.MakeLocalQueue("lq-b", ns.Name).
				FairSharing(&kueue.FairSharing{Weight: ptr.To(resource.MustParse("1"))}).
				ClusterQueue(cq1.Name).Obj()
			lqC = testing.MakeLocalQueue("lq-c", ns.Name).
				FairSharing(&kueue.FairSharing{Weight: ptr.To(resource.MustParse("1"))}).
				ClusterQueue(cq2.Name).Obj()
			lqs = append(lqs, lqA)
			lqs = append(lqs, lqB)
			lqs = append(lqs, lqC)
			util.MustCreate(ctx, k8sClient, lqA)
			util.MustCreate(ctx, k8sClient, lqB)
			util.MustCreate(ctx, k8sClient, lqC)
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(features.SetEnable(features.AdmissionFairSharing, false)).To(gomega.Succeed())
		})

		ginkgo.It("should promote a workload from LQ with lower recent usage", func() {
			ginkgo.By("Creating a workload")
			wl := createWorkload("lq-a", "32")

			ginkgo.By("Admitting the workload")
			util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl)
			util.ExpectReservingActiveWorkloadsMetric(cq1, 1)

			ginkgo.By("Checking that LQ's resource usage is updated")
			util.ExpectLocalQueueFairSharingUsageToBe(ctx, k8sClient, client.ObjectKeyFromObject(lqA), ">", 0)

			ginkgo.By("Creating two pending workloads")
			wlA := createWorkload("lq-a", "32")
			wlB := createWorkload("lq-b", "32")

			ginkgo.By("Finish the previous workload")
			util.FinishWorkloads(ctx, k8sClient, wl)

			ginkgo.By("Admitting the workload from LQ-b, which has lower recent usage")
			util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wlB)
			util.ExpectWorkloadsToBePending(ctx, k8sClient, wlA)
		})

		ginkgo.It("should preempt a workload from LQ with higher recent usage", func() {
			ginkgo.By("Creating workloads in CQ1 that borrow from CQ2")
			wlHighA := createWorkloadWithPriority("lq-a", "20", 10)
			_ = createWorkloadWithPriority("lq-b", "12", 1)
			util.ExpectReservingActiveWorkloadsMetric(cq1, 2)

			ginkgo.By("Checking that LQs' resource usage is updated")
			util.ExpectLocalQueueFairSharingUsageToBe(ctx, k8sClient, client.ObjectKeyFromObject(lqA), ">", 12_000)

			ginkgo.By("Creating a workload in CQ2 that reclaims the quota")
			_ = createWorkload("lq-c", "10")

			ginkgo.By("Checking that the workload from lq-A is preempted despite having bigger priority")
			util.ExpectWorkloadsToBePreempted(ctx, k8sClient, wlHighA)
		})
	})
})
