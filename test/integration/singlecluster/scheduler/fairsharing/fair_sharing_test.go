/*
Copyright 2024 The Kubernetes Authors.

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
	"context"
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
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/util/testing"
	testingdeployment "sigs.k8s.io/kueue/pkg/util/testingjobs/deployment"
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

		ns = &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: "core-",
			},
		}
		gomega.Expect(k8sClient.Create(ctx, ns)).To(gomega.Succeed())
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
			util.ExpectClusterQueueWeightedShareMetric(cqA, 222)
			util.ExpectClusterQueueWeightedShareMetric(cqB, 111)
			util.ExpectClusterQueueWeightedShareMetric(cqC, 0)

			ginkgo.By("Checking that weight share status changed")
			cqAKey := client.ObjectKeyFromObject(cqA)
			createdCqA := &kueue.ClusterQueue{}
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, cqAKey, createdCqA)).Should(gomega.Succeed())
				g.Expect(createdCqA.Status.FairSharing).ShouldNot(gomega.BeNil())
				g.Expect(createdCqA.Status.FairSharing).Should(gomega.BeComparableTo(&kueue.FairSharingStatus{WeightedShare: 222}))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})
	})
})

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

var _ = ginkgo.FDescribe("Infinite Preemption Loop", func() {
	var (
		ctx context.Context
		ns  *corev1.Namespace
	)

	ginkgo.BeforeEach(func() {
		ctx = context.Background()

		// Create a namespace for the test
		ns = &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: "preemption-loop-test-",
			},
		}
		gomega.Expect(k8sClient.Create(ctx, ns)).To(gomega.Succeed())
	})

	ginkgo.AfterEach(func() {
		// Cleanup namespace
		gomega.Expect(k8sClient.Delete(ctx, ns)).To(gomega.Succeed())
		fwk.StopManager(ctx)
	})

	ginkgo.It("should reproduce the infinite preemption loop", func() {
		// Step 1: Create ClusterQueues and Cohort
		cohortName := "testing"

		clusterQueueA := testing.MakeClusterQueue("guaranteed-tenant-a").
			Cohort(cohortName).
			ResourceGroup(*testing.MakeFlavorQuotas("default").
				Resource("cpu", "150m").Obj()).
			Preemption(kueue.ClusterQueuePreemption{
				ReclaimWithinCohort: kueue.PreemptionPolicyLowerPriority,
				BorrowWithinCohort: &kueue.BorrowWithinCohort{
					Policy:               kueue.BorrowWithinCohortPolicyLowerPriority,
					MaxPriorityThreshold: ptr.To[int32](100),
				},
				WithinClusterQueue: kueue.PreemptionPolicyNever,
			}).Obj()
		gomega.Expect(k8sClient.Create(ctx, clusterQueueA)).To(gomega.Succeed())

		clusterQueueB := testing.MakeClusterQueue("guaranteed-tenant-b").
			Cohort(cohortName).
			ResourceGroup(*testing.MakeFlavorQuotas("default").
				Resource("cpu", "150m").Obj()).
			Preemption(kueue.ClusterQueuePreemption{
				ReclaimWithinCohort: kueue.PreemptionPolicyLowerPriority,
				BorrowWithinCohort: &kueue.BorrowWithinCohort{
					Policy:               kueue.BorrowWithinCohortPolicyLowerPriority,
					MaxPriorityThreshold: ptr.To[int32](100),
				},
				WithinClusterQueue: kueue.PreemptionPolicyNever,
			}).Obj()
		gomega.Expect(k8sClient.Create(ctx, clusterQueueB)).To(gomega.Succeed())

		clusterQueueBestEffort := testing.MakeClusterQueue("best-effort").
			Cohort(cohortName).
			ResourceGroup(*testing.MakeFlavorQuotas("default").
				Resource("cpu", "0m").Obj()).
			Preemption(kueue.ClusterQueuePreemption{
				ReclaimWithinCohort: kueue.PreemptionPolicyNever,
				BorrowWithinCohort: &kueue.BorrowWithinCohort{
					Policy: kueue.BorrowWithinCohortPolicyNever,
				},
				WithinClusterQueue: kueue.PreemptionPolicyNever,
			}).Obj()
		gomega.Expect(k8sClient.Create(ctx, clusterQueueBestEffort)).To(gomega.Succeed())

		// Step 2: Create LocalQueues
		localQueueA := testing.MakeLocalQueue("guaranteed-tenant-a", ns.Name).
			ClusterQueue("guaranteed-tenant-a").Obj()
		gomega.Expect(k8sClient.Create(ctx, localQueueA)).To(gomega.Succeed())

		localQueueB := testing.MakeLocalQueue("guaranteed-tenant-b", ns.Name).
			ClusterQueue("guaranteed-tenant-b").Obj()
		gomega.Expect(k8sClient.Create(ctx, localQueueB)).To(gomega.Succeed())

		localQueueBestEffort := testing.MakeLocalQueue("best-effort", ns.Name).
			ClusterQueue("best-effort").Obj()
		gomega.Expect(k8sClient.Create(ctx, localQueueBestEffort)).To(gomega.Succeed())

		// Step 3: Create Deployments
		// Guaranteed Tenant A Workload 1
		deploymentA1 := testingdeployment.MakeDeployment("guaranteed-tenant-a-1", ns.Name).
			Queue("guaranteed-tenant-a").
			Replicas(1).
			Request(corev1.ResourceCPU, "250m").
			PodTemplateSpecPriorityClass("medium").
			Obj()
		gomega.Expect(k8sClient.Create(ctx, deploymentA1)).To(gomega.Succeed())

		// Best Effort Workload
		deploymentBestEffort := testingdeployment.MakeDeployment("best-effort-1", ns.Name).
			Queue("best-effort").
			Replicas(1).
			Request(corev1.ResourceCPU, "50m").
			PodTemplateSpecPriorityClass("low").
			Obj()
		gomega.Expect(k8sClient.Create(ctx, deploymentBestEffort)).To(gomega.Succeed())

		// Guaranteed Tenant A Workload 2
		deploymentA2 := testingdeployment.MakeDeployment("guaranteed-tenant-a-2", ns.Name).
			Queue("guaranteed-tenant-a").
			Replicas(1).
			Request(corev1.ResourceCPU, "50m").
			PodTemplateSpecPriorityClass("medium").
			Obj()
		gomega.Expect(k8sClient.Create(ctx, deploymentA2)).To(gomega.Succeed())

		// Step 4: Verify Infinite Preemption Loop
		ginkgo.By("Verifying the infinite preemption loop", func() {
			// Wait for a few seconds to observe the loop
			time.Sleep(10 * time.Second)

			// Check the status of the pods
			pods := &corev1.PodList{}
			gomega.Expect(k8sClient.List(ctx, pods, client.InNamespace(ns.Name))).To(gomega.Succeed())

			// Verify that the best-effort pod is continuously preempted and recreated
			var bestEffortPodCount int
			for _, pod := range pods.Items {
				if pod.Labels["app"] == "best-effort-1" {
					bestEffortPodCount++
					gomega.Expect(pod.Status.Phase).To(gomega.Or(gomega.Equal(corev1.PodPending), gomega.Equal(corev1.PodRunning)))
				}
			}
			gomega.Expect(bestEffortPodCount).To(gomega.BeNumerically(">", 1), "Best-effort pod should be recreated multiple times")
		})
	})
})
