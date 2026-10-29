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

package deepintegration

import (
	"fmt"

	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	workloadjob "sigs.k8s.io/kueue/pkg/controller/jobs/job"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
	"sigs.k8s.io/kueue/pkg/workload"
	workloadevict "sigs.k8s.io/kueue/pkg/workload/evict"
	"sigs.k8s.io/kueue/test/util/behavioral"
	"sigs.k8s.io/kueue/test/util/behavioral/e2e"
)

var _ = ginkgo.Describe("TopologyAwareScheduling Preemption", ginkgo.Label(e2e.Shard1, "area:tas", "feature:tas-preemption", "feature:was-deepintegration"), func() {
	var (
		ns                  *corev1.Namespace
		topology            *kueue.Topology
		tasFlavor           *kueue.ResourceFlavor
		localQueue          *kueue.LocalQueue
		clusterQueue        *kueue.ClusterQueue
		lowestPriorityClass *kueue.WorkloadPriorityClass
		lowPriorityClass    *kueue.WorkloadPriorityClass
		midPriorityClass    *kueue.WorkloadPriorityClass
		highPriorityClass   *kueue.WorkloadPriorityClass
	)

	ginkgo.BeforeEach(func() {
		ns = behavioral.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "e2e-tas-preempt-")

		topology = utiltestingapi.MakeDefaultThreeLevelTopology("datacenter")
		behavioral.MustCreate(ctx, k8sClient, topology)

		tasFlavor = utiltestingapi.MakeResourceFlavor("tas-flavor").
			NodeLabel(tasNodeGroupLabel, instanceType).
			TopologyName(topology.Name).
			Obj()
		behavioral.MustCreate(ctx, k8sClient, tasFlavor)

		lowestPriorityClass = utiltestingapi.MakeWorkloadPriorityClass("lowest-priority-" + ns.Name).PriorityValue(50).Obj()
		behavioral.MustCreate(ctx, k8sClient, lowestPriorityClass)

		lowPriorityClass = utiltestingapi.MakeWorkloadPriorityClass("low-priority-" + ns.Name).PriorityValue(100).Obj()
		behavioral.MustCreate(ctx, k8sClient, lowPriorityClass)

		midPriorityClass = utiltestingapi.MakeWorkloadPriorityClass("mid-priority-" + ns.Name).PriorityValue(150).Obj()
		behavioral.MustCreate(ctx, k8sClient, midPriorityClass)

		highPriorityClass = utiltestingapi.MakeWorkloadPriorityClass("high-priority-" + ns.Name).PriorityValue(200).Obj()
		behavioral.MustCreate(ctx, k8sClient, highPriorityClass)

		clusterQueue = utiltestingapi.MakeClusterQueue("cluster-queue").
			ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas("tas-flavor").
					Resource(extraResource, "8").
					Obj(),
			).
			Preemption(kueue.ClusterQueuePreemption{
				WithinClusterQueue: kueue.PreemptionPolicyLowerPriority,
			}).
			Obj()
		behavioral.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, clusterQueue)

		localQueue = utiltestingapi.MakeLocalQueue("main", ns.Name).ClusterQueue("cluster-queue").Obj()
		behavioral.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, localQueue)
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(behavioral.DeleteAllJobsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
		gomega.Expect(behavioral.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, localQueue, true)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, highPriorityClass, true)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, midPriorityClass, true)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, lowPriorityClass, true)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, lowestPriorityClass, true)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, tasFlavor, true)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, topology, true)
		behavioral.ExpectAllPodsInNamespaceDeleted(ctx, k8sClient, ns)
		gomega.Expect(behavioral.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
	})

	admitJobAndExpectRunning := func(name, lqName, priorityClassName string, parallelism int32, nodeSelector map[string]string) (*batchv1.Job, types.NamespacedName) {
		ginkgo.GinkgoHelper()
		jobWrapper := testingjob.MakeJob(name, ns.Name).
			Queue(kueue.LocalQueueName(lqName)).
			WorkloadPriorityClass(priorityClassName).
			Parallelism(parallelism).
			Completions(parallelism).
			RequestAndLimit(extraResource, "1").
			TerminationGracePeriod(1).
			Image(e2e.GetAgnHostImage(), e2e.BehaviorWaitForDeletion)
		for k, v := range nodeSelector {
			jobWrapper = jobWrapper.NodeSelector(k, v)
		}
		job := jobWrapper.Obj()
		behavioral.MustCreate(ctx, k8sClient, job)

		wlKey := types.NamespacedName{
			Name:      workloadjob.GetWorkloadNameForJob(job.Name, job.UID),
			Namespace: ns.Name,
		}
		ginkgo.By(fmt.Sprintf("await admission and running pods of job %q", name), func() {
			behavioral.ExpectWorkloadsToBeAdmittedByKeys(ctx, k8sClient, wlKey)
			e2e.ExpectPodsRunningOrSucceeded(ctx, k8sClient, ns.Name, int(parallelism), client.MatchingLabels{"job-name": job.Name})
		})
		return job, wlKey
	}

	ginkgo.It("Should preempt a lower-priority Job when quota and nodes are occupied", func() {
		_, lowWlKey := admitJobAndExpectRunning("low-priority-job", localQueue.Name, lowPriorityClass.Name, 8, nil)

		highJob := testingjob.MakeJob("high-priority-job", ns.Name).
			Queue(kueue.LocalQueueName(localQueue.Name)).
			WorkloadPriorityClass(highPriorityClass.Name).
			Parallelism(2).
			Completions(2).
			RequestAndLimit(extraResource, "1").
			TerminationGracePeriod(1).
			Image(e2e.GetAgnHostImage(), e2e.BehaviorWaitForDeletion).
			Obj()
		behavioral.MustCreate(ctx, k8sClient, highJob)

		highWlKey := types.NamespacedName{
			Name:      workloadjob.GetWorkloadNameForJob(highJob.Name, highJob.UID),
			Namespace: ns.Name,
		}

		ginkgo.By("verify low-priority workload is preempted", func() {
			behavioral.ExpectWorkloadsToBePreemptedByKeys(ctx, k8sClient, lowWlKey)
		})

		ginkgo.By("verify high-priority workload is admitted and running after preemption", func() {
			behavioral.ExpectWorkloadsToBeAdmittedByKeysWithTimeout(ctx, k8sClient, behavioral.MediumTimeout, highWlKey)
			e2e.ExpectPodsRunningOrSucceeded(ctx, k8sClient, ns.Name, 2, client.MatchingLabels{"job-name": highJob.Name})
		})
	})

	ginkgo.It("Should preempt only the lowest-priority Job when multiple candidates exist", func() {
		lowJob1, low1WlKey := admitJobAndExpectRunning("low-priority-job-1", localQueue.Name, lowPriorityClass.Name, 2, nil)
		_, lowestWlKey := admitJobAndExpectRunning("lowest-priority-job", localQueue.Name, lowestPriorityClass.Name, 4, nil)
		lowJob2, low2WlKey := admitJobAndExpectRunning("low-priority-job-2", localQueue.Name, lowPriorityClass.Name, 2, nil)

		// High-priority job requests 4 GPUs; preempting lowest-priority-job (admitted second) alone is sufficient.
		highJob := testingjob.MakeJob("high-priority-job", ns.Name).
			Queue(kueue.LocalQueueName(localQueue.Name)).
			WorkloadPriorityClass(highPriorityClass.Name).
			Parallelism(4).
			Completions(4).
			RequestAndLimit(extraResource, "1").
			TerminationGracePeriod(1).
			Image(e2e.GetAgnHostImage(), e2e.BehaviorWaitForDeletion).
			Obj()
		behavioral.MustCreate(ctx, k8sClient, highJob)

		highWlKey := types.NamespacedName{
			Name:      workloadjob.GetWorkloadNameForJob(highJob.Name, highJob.UID),
			Namespace: ns.Name,
		}

		ginkgo.By("verify lowest-priority workload is preempted", func() {
			behavioral.ExpectWorkloadsToBePreemptedByKeys(ctx, k8sClient, lowestWlKey)
		})

		ginkgo.By("verify high-priority workload is admitted and running", func() {
			behavioral.ExpectWorkloadsToBeAdmittedByKeysWithTimeout(ctx, k8sClient, behavioral.MediumTimeout, highWlKey)
			e2e.ExpectPodsRunningOrSucceeded(ctx, k8sClient, ns.Name, 4, client.MatchingLabels{"job-name": highJob.Name})
		})

		ginkgo.By("verify both low-priority workloads are not preempted and remain running", func() {
			gomega.Consistently(func(g gomega.Gomega) {
				for _, wlKey := range []types.NamespacedName{low1WlKey, low2WlKey} {
					lowWl := &kueue.Workload{}
					g.Expect(k8sClient.Get(ctx, wlKey, lowWl)).To(gomega.Succeed())
					g.Expect(workload.IsAdmitted(lowWl)).To(gomega.BeTrue())
					g.Expect(workloadevict.IsEvicted(lowWl)).To(gomega.BeFalse())
				}
			}, behavioral.ConsistentDuration, behavioral.Interval).Should(gomega.Succeed())
			e2e.ExpectPodsRunningOrSucceeded(ctx, k8sClient, ns.Name, 2, client.MatchingLabels{"job-name": lowJob1.Name})
			e2e.ExpectPodsRunningOrSucceeded(ctx, k8sClient, ns.Name, 2, client.MatchingLabels{"job-name": lowJob2.Name})
		})
	})

	ginkgo.It("Should preempt multiple lower-priority Jobs when a single candidate is insufficient", func() {
		_, lowestWlKey := admitJobAndExpectRunning("lowest-priority-job", localQueue.Name, lowestPriorityClass.Name, 4, nil)
		_, lowWlKey := admitJobAndExpectRunning("low-priority-job", localQueue.Name, lowPriorityClass.Name, 4, nil)

		// High-priority job requests 6 GPUs; both 4-GPU candidates must be preempted.
		highJob := testingjob.MakeJob("high-priority-job", ns.Name).
			Queue(kueue.LocalQueueName(localQueue.Name)).
			WorkloadPriorityClass(highPriorityClass.Name).
			Parallelism(6).
			Completions(6).
			RequestAndLimit(extraResource, "1").
			TerminationGracePeriod(1).
			Image(e2e.GetAgnHostImage(), e2e.BehaviorWaitForDeletion).
			Obj()
		behavioral.MustCreate(ctx, k8sClient, highJob)

		highWlKey := types.NamespacedName{
			Name:      workloadjob.GetWorkloadNameForJob(highJob.Name, highJob.UID),
			Namespace: ns.Name,
		}

		ginkgo.By("verify both lower-priority workloads are preempted", func() {
			behavioral.ExpectWorkloadsToBePreemptedByKeys(ctx, k8sClient, lowestWlKey, lowWlKey)
		})

		ginkgo.By("verify high-priority workload is admitted and running", func() {
			behavioral.ExpectWorkloadsToBeAdmittedByKeysWithTimeout(ctx, k8sClient, behavioral.MediumTimeout, highWlKey)
			e2e.ExpectPodsRunningOrSucceeded(ctx, k8sClient, ns.Name, 6, client.MatchingLabels{"job-name": highJob.Name})
		})
	})

	ginkgo.It("Should preempt only the candidate occupying matching nodes and reprieve lower-priority candidates on other nodes", func() {
		blockB1 := map[string]string{utiltesting.DefaultBlockTopologyLevel: "b1"}
		blockB2 := map[string]string{utiltesting.DefaultBlockTopologyLevel: "b2"}

		lowestJobB1, lowestB1WlKey := admitJobAndExpectRunning("lowest-job-b1", localQueue.Name, lowestPriorityClass.Name, 2, blockB1)
		_, lowB2WlKey := admitJobAndExpectRunning("low-job-b2", localQueue.Name, lowPriorityClass.Name, 4, blockB2)

		// High-priority job requests 2 GPUs on block b2; ClusterQueue quota fits (2 + 4 + 2 = 8),
		// but block b2 is full. lowest-job-b1 is on b1 and should be reprieved.
		highJobB2 := testingjob.MakeJob("high-job-b2", ns.Name).
			Queue(kueue.LocalQueueName(localQueue.Name)).
			WorkloadPriorityClass(highPriorityClass.Name).
			NodeSelector(utiltesting.DefaultBlockTopologyLevel, "b2").
			Parallelism(2).
			Completions(2).
			RequestAndLimit(extraResource, "1").
			TerminationGracePeriod(1).
			Image(e2e.GetAgnHostImage(), e2e.BehaviorWaitForDeletion).
			Obj()
		behavioral.MustCreate(ctx, k8sClient, highJobB2)

		highB2WlKey := types.NamespacedName{
			Name:      workloadjob.GetWorkloadNameForJob(highJobB2.Name, highJobB2.UID),
			Namespace: ns.Name,
		}

		ginkgo.By("verify low-priority workload on b2 is preempted", func() {
			behavioral.ExpectWorkloadsToBePreemptedByKeys(ctx, k8sClient, lowB2WlKey)
		})

		ginkgo.By("verify high-priority workload on b2 is admitted and running", func() {
			behavioral.ExpectWorkloadsToBeAdmittedByKeysWithTimeout(ctx, k8sClient, behavioral.MediumTimeout, highB2WlKey)
			e2e.ExpectPodsRunningOrSucceeded(ctx, k8sClient, ns.Name, 2, client.MatchingLabels{"job-name": highJobB2.Name})
		})

		ginkgo.By("verify lowest-priority workload on b1 was reprieved and remains running", func() {
			lowestWl := &kueue.Workload{}
			gomega.Consistently(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, lowestB1WlKey, lowestWl)).To(gomega.Succeed())
				g.Expect(workload.IsAdmitted(lowestWl)).To(gomega.BeTrue())
				g.Expect(workloadevict.IsEvicted(lowestWl)).To(gomega.BeFalse())
			}, behavioral.ConsistentDuration, behavioral.Interval).Should(gomega.Succeed())
			e2e.ExpectPodsRunningOrSucceeded(ctx, k8sClient, ns.Name, 2, client.MatchingLabels{"job-name": lowestJobB1.Name})
		})
	})

	ginkgo.It("Should not preempt lower-priority Jobs when their nodes do not match the incoming Job requirements", func() {
		blockB1 := map[string]string{utiltesting.DefaultBlockTopologyLevel: "b1"}
		blockB2 := map[string]string{utiltesting.DefaultBlockTopologyLevel: "b2"}

		highJobB1, highB1WlKey := admitJobAndExpectRunning("high-job-b1", localQueue.Name, highPriorityClass.Name, 4, blockB1)
		lowJobB2, lowB2WlKey := admitJobAndExpectRunning("low-job-b2", localQueue.Name, lowPriorityClass.Name, 4, blockB2)

		// Mid-priority job requires block b1, which is occupied by high-job-b1 (priority 200).
		// Even though low-job-b2 (priority 100) could free quota, its nodes are in b2.
		midJobB1 := testingjob.MakeJob("mid-job-b1", ns.Name).
			Queue(kueue.LocalQueueName(localQueue.Name)).
			WorkloadPriorityClass(midPriorityClass.Name).
			NodeSelector(utiltesting.DefaultBlockTopologyLevel, "b1").
			Parallelism(2).
			Completions(2).
			RequestAndLimit(extraResource, "1").
			TerminationGracePeriod(1).
			Image(e2e.GetAgnHostImage(), e2e.BehaviorWaitForDeletion).
			Obj()
		behavioral.MustCreate(ctx, k8sClient, midJobB1)

		midB1WlKey := types.NamespacedName{
			Name:      workloadjob.GetWorkloadNameForJob(midJobB1.Name, midJobB1.UID),
			Namespace: ns.Name,
		}

		ginkgo.By("verify mid-priority workload waits for quota and is not admitted", func() {
			midB1Wl := &kueue.Workload{}
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, midB1WlKey, midB1Wl)).Should(gomega.Succeed())
				g.Expect(midB1Wl.Status.Admission).Should(gomega.BeNil())
				g.Expect(workload.HasQuotaReservation(midB1Wl)).Should(gomega.BeFalse())
				cond := apimeta.FindStatusCondition(midB1Wl.Status.Conditions, kueue.WorkloadQuotaReserved)
				g.Expect(cond).ShouldNot(gomega.BeNil())
				g.Expect(cond).Should(gomega.BeComparableTo(&metav1.Condition{
					Type:   kueue.WorkloadQuotaReserved,
					Status: metav1.ConditionFalse,
					Reason: kueue.WorkloadQuotaReservedReasonWaitingForQuota,
				}, cmpopts.IgnoreFields(metav1.Condition{}, "LastTransitionTime", "Message", "ObservedGeneration")))
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("verify neither running workload is preempted and mid-priority workload stays unadmitted", func() {
			gomega.Consistently(func(g gomega.Gomega) {
				for _, wlKey := range []types.NamespacedName{highB1WlKey, lowB2WlKey} {
					wl := &kueue.Workload{}
					g.Expect(k8sClient.Get(ctx, wlKey, wl)).To(gomega.Succeed())
					g.Expect(workload.IsAdmitted(wl)).To(gomega.BeTrue())
					g.Expect(workloadevict.IsEvicted(wl)).To(gomega.BeFalse())
				}
				midB1Wl := &kueue.Workload{}
				g.Expect(k8sClient.Get(ctx, midB1WlKey, midB1Wl)).To(gomega.Succeed())
				g.Expect(workload.HasQuotaReservation(midB1Wl)).To(gomega.BeFalse())
				g.Expect(workload.IsAdmitted(midB1Wl)).To(gomega.BeFalse())
			}, behavioral.ConsistentDuration, behavioral.Interval).Should(gomega.Succeed())
			e2e.ExpectPodsRunningOrSucceeded(ctx, k8sClient, ns.Name, 4, client.MatchingLabels{"job-name": highJobB1.Name})
			e2e.ExpectPodsRunningOrSucceeded(ctx, k8sClient, ns.Name, 4, client.MatchingLabels{"job-name": lowJobB2.Name})
		})
	})

	ginkgo.It("Should preempt a lower-priority Job when ClusterQueue quota is full even if nodes have free capacity", func() {
		ginkgo.By("reduce ClusterQueue quota to 4 GPUs while cluster has 8 GPUs across 4 nodes", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, types.NamespacedName{Name: clusterQueue.Name}, clusterQueue)).To(gomega.Succeed())
				clusterQueue.Spec.ResourceGroups[0].Flavors[0].Resources[0].NominalQuota = resource.MustParse("4")
				g.Expect(k8sClient.Update(ctx, clusterQueue)).To(gomega.Succeed())
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		})

		// low-priority-job uses 4 GPUs (filling the 4-GPU ClusterQueue quota), leaving 2 nodes (4 GPUs) physically free.
		_, lowWlKey := admitJobAndExpectRunning("low-priority-job", localQueue.Name, lowPriorityClass.Name, 4, nil)

		highJob := testingjob.MakeJob("high-priority-job", ns.Name).
			Queue(kueue.LocalQueueName(localQueue.Name)).
			WorkloadPriorityClass(highPriorityClass.Name).
			Parallelism(2).
			Completions(2).
			RequestAndLimit(extraResource, "1").
			TerminationGracePeriod(1).
			Image(e2e.GetAgnHostImage(), e2e.BehaviorWaitForDeletion).
			Obj()
		behavioral.MustCreate(ctx, k8sClient, highJob)

		highWlKey := types.NamespacedName{
			Name:      workloadjob.GetWorkloadNameForJob(highJob.Name, highJob.UID),
			Namespace: ns.Name,
		}

		ginkgo.By("verify low-priority workload is preempted", func() {
			behavioral.ExpectWorkloadsToBePreemptedByKeys(ctx, k8sClient, lowWlKey)
		})

		ginkgo.By("verify high-priority workload is admitted and running after preemption", func() {
			behavioral.ExpectWorkloadsToBeAdmittedByKeysWithTimeout(ctx, k8sClient, behavioral.MediumTimeout, highWlKey)
			e2e.ExpectPodsRunningOrSucceeded(ctx, k8sClient, ns.Name, 2, client.MatchingLabels{"job-name": highJob.Name})
		})
	})

	ginkgo.It("Should mark workload as WaitingForQuota and not preempt when reprieval causes fair-sharing verification to fail", func() {
		blockB1 := map[string]string{utiltesting.DefaultBlockTopologyLevel: "b1"}
		blockB2 := map[string]string{utiltesting.DefaultBlockTopologyLevel: "b2"}

		makeFairCQ := func(name, weight string) *kueue.ClusterQueue {
			return utiltestingapi.MakeClusterQueue(name).
				Cohort("fair-cohort").
				FairWeight(resource.MustParse(weight)).
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("tas-flavor").
						Resource(extraResource, "3").
						Obj(),
				).
				Preemption(kueue.ClusterQueuePreemption{
					WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
					ReclaimWithinCohort: kueue.PreemptionPolicyAny,
				}).
				Obj()
		}

		// cqA is the preemptor queue (weight 2, nominal 3); cqB is the borrower queue (weight 1, nominal 3).
		cqA := makeFairCQ("cq-a", "2")
		cqB := makeFairCQ("cq-b", "1")
		lqA := utiltestingapi.MakeLocalQueue("lq-a", ns.Name).ClusterQueue(cqA.Name).Obj()
		lqB := utiltestingapi.MakeLocalQueue("lq-b", ns.Name).ClusterQueue(cqB.Name).Obj()

		defer func() {
			gomega.Expect(behavioral.DeleteAllJobsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
			gomega.Expect(behavioral.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, lqA, true)
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, lqB, true)
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, cqA, true)
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, cqB, true)
		}()

		behavioral.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, cqA, cqB)
		behavioral.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, lqA, lqB)

		// a-small uses 2 GPUs on block b1 in cqA (nominal 3, weight 2).
		aSmallJob, aSmallWlKey := admitJobAndExpectRunning("a-small", lqA.Name, lowPriorityClass.Name, 2, blockB1)

		// b-hero uses 4 GPUs on block b2 in borrower cqB (nominal 3, weight 1, borrowing 1 from cohort; old DRS = (1/6)/1 = 1/6).
		// Total cohort usage is 2 + 4 = 6 GPUs (all 6 GPUs of cohort quota).
		bHeroJob, bHeroWlKey := admitJobAndExpectRunning("b-hero", lqB.Name, lowPriorityClass.Name, 4, blockB2)

		// a-hero requests 4 GPUs on block b2 in cqA.
		// fairPreemptionStrategy proposes [a-small, b-hero] under LessThanInitialShare
		// (with a-small evicted, cqA DRS is ((4-3)/6)/2 = 1/12 < cqB old DRS 1/6).
		// During simulation, ScheduleWorkload removes both, places a-hero (4 GPUs) on block b2,
		// and reprieves a-small (2 GPUs on block b1) since 2 + 4 = 6 <= 6 cohort quota and b1 is untouched.
		// However, filter.Verify([b-hero]) rejects the remaining target set because keeping a-small gives
		// cqA DRS ((6-3)/6)/2 = 1/4 > cqB old DRS 1/6.
		aHeroJob := testingjob.MakeJob("a-hero", ns.Name).
			Queue(kueue.LocalQueueName(lqA.Name)).
			WorkloadPriorityClass(highPriorityClass.Name).
			NodeSelector(utiltesting.DefaultBlockTopologyLevel, "b2").
			Parallelism(4).
			Completions(4).
			RequestAndLimit(extraResource, "1").
			TerminationGracePeriod(1).
			Image(e2e.GetAgnHostImage(), e2e.BehaviorWaitForDeletion).
			Obj()
		behavioral.MustCreate(ctx, k8sClient, aHeroJob)
		aHeroWlKey := types.NamespacedName{
			Name:      workloadjob.GetWorkloadNameForJob(aHeroJob.Name, aHeroJob.UID),
			Namespace: ns.Name,
		}

		ginkgo.By("verify a-hero waits for quota with WaitingForQuota reason", func() {
			aHeroWl := &kueue.Workload{}
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, aHeroWlKey, aHeroWl)).Should(gomega.Succeed())
				g.Expect(aHeroWl.Status.Admission).Should(gomega.BeNil())
				g.Expect(workload.HasQuotaReservation(aHeroWl)).Should(gomega.BeFalse())
				cond := apimeta.FindStatusCondition(aHeroWl.Status.Conditions, kueue.WorkloadQuotaReserved)
				g.Expect(cond).ShouldNot(gomega.BeNil())
				g.Expect(cond).Should(gomega.BeComparableTo(&metav1.Condition{
					Type:   kueue.WorkloadQuotaReserved,
					Status: metav1.ConditionFalse,
					Reason: kueue.WorkloadQuotaReservedReasonWaitingForQuota,
				}, cmpopts.IgnoreFields(metav1.Condition{}, "LastTransitionTime", "Message", "ObservedGeneration")))
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("verify neither running workload is preempted and both remain running", func() {
			gomega.Consistently(func(g gomega.Gomega) {
				for _, wlKey := range []types.NamespacedName{aSmallWlKey, bHeroWlKey} {
					wl := &kueue.Workload{}
					g.Expect(k8sClient.Get(ctx, wlKey, wl)).To(gomega.Succeed())
					g.Expect(workload.IsAdmitted(wl)).To(gomega.BeTrue())
					g.Expect(workloadevict.IsEvicted(wl)).To(gomega.BeFalse())
				}
			}, behavioral.ConsistentDuration, behavioral.Interval).Should(gomega.Succeed())
			e2e.ExpectPodsRunningOrSucceeded(ctx, k8sClient, ns.Name, 2, client.MatchingLabels{"job-name": aSmallJob.Name})
			e2e.ExpectPodsRunningOrSucceeded(ctx, k8sClient, ns.Name, 4, client.MatchingLabels{"job-name": bHeroJob.Name})
		})
	})
})
