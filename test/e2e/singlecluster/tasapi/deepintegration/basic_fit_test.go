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
	"context"
	"fmt"

	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	workloadjob "sigs.k8s.io/kueue/pkg/controller/jobs/job"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/test/util/behavioral"
	"sigs.k8s.io/kueue/test/util/behavioral/e2e"
)

// TODO: Replace with kueue.WorkloadQuotaReservedReasonSimulationFailed once #16483 is merged
const workloadQuotaReservedReasonSimulationFailed = "SchedulingSimulationFailed"

var _ = ginkgo.Describe("TopologyAwareScheduling Basic Fit and Quota", ginkgo.Label(e2e.Shard1, "area:tas", "feature:tas-basic-fit"), func() {
	var (
		ns           *corev1.Namespace
		topology     *kueue.Topology
		tasFlavor    *kueue.ResourceFlavor
		localQueue   *kueue.LocalQueue
		clusterQueue *kueue.ClusterQueue
	)

	ginkgo.BeforeEach(func() {
		ns = behavioral.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "e2e-tas-fit-")

		topology = utiltestingapi.MakeDefaultThreeLevelTopology("datacenter")
		behavioral.MustCreate(ctx, k8sClient, topology)

		tasFlavor = utiltestingapi.MakeResourceFlavor("tas-flavor").
			NodeLabel(tasNodeGroupLabel, instanceType).
			TopologyName(topology.Name).
			Obj()
		behavioral.MustCreate(ctx, k8sClient, tasFlavor)

		// ClusterQueue with 16 GPUs quota
		clusterQueue = utiltestingapi.MakeClusterQueue("cluster-queue").
			ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas("tas-flavor").
					Resource(extraResource, "16").
					Obj(),
			).
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
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, tasFlavor, true)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, topology, true)
		behavioral.ExpectAllPodsInNamespaceDeleted(ctx, k8sClient, ns)
		gomega.Expect(behavioral.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
	})

	ginkgo.It("Should not admit a Job if quota is insufficient", func() {
		// Request 20 GPUs when total cluster quota is 16
		sampleJob := testingjob.MakeJob("excessive-quota-job", ns.Name).
			Queue(kueue.LocalQueueName(localQueue.Name)).
			Parallelism(20).
			Completions(20).
			RequestAndLimit(extraResource, "1").
			Image(e2e.GetAgnHostImage(), e2e.BehaviorExitFast).
			Obj()
		behavioral.MustCreate(ctx, k8sClient, sampleJob)

		wlLookupKey := types.NamespacedName{
			Name:      workloadjob.GetWorkloadNameForJob(sampleJob.Name, sampleJob.UID),
			Namespace: ns.Name,
		}

		createdWorkload := &kueue.Workload{}
		ginkgo.By(fmt.Sprintf("await creation of workload %q", wlLookupKey), func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
			}, behavioral.MediumTimeout, behavioral.Interval).Should(gomega.Succeed())
		})

		ginkgo.By(fmt.Sprintf("verify workload %q is not admitted due to insufficient quota", wlLookupKey), func() {
			gomega.Consistently(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
				g.Expect(createdWorkload.Status.Admission).Should(gomega.BeNil())
				g.Expect(workload.HasQuotaReservation(createdWorkload)).Should(gomega.BeFalse())
			}, behavioral.ConsistentDuration, behavioral.Interval).Should(gomega.Succeed())
		})
	})

	ginkgo.It("Should not admit a Job if pod does not fit on any single node", func() {
		// Each node only has 1 GPU allocatable. A pod requesting 2 GPUs cannot fit on any node,
		// even though total ClusterQueue quota (16) is not exceeded
		sampleJob := testingjob.MakeJob("fragmented-fit-job", ns.Name).
			Queue(kueue.LocalQueueName(localQueue.Name)).
			Parallelism(1).
			Completions(1).
			RequestAndLimit(extraResource, "2").
			Image(e2e.GetAgnHostImage(), e2e.BehaviorExitFast).
			Obj()
		behavioral.MustCreate(ctx, k8sClient, sampleJob)

		wlLookupKey := types.NamespacedName{
			Name:      workloadjob.GetWorkloadNameForJob(sampleJob.Name, sampleJob.UID),
			Namespace: ns.Name,
		}

		createdWorkload := &kueue.Workload{}
		ginkgo.By(fmt.Sprintf("await creation of workload %q", wlLookupKey), func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
			}, behavioral.MediumTimeout, behavioral.Interval).Should(gomega.Succeed())
		})

		ginkgo.By(fmt.Sprintf("verify workload %q is not admitted due to node fit failure", wlLookupKey), func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
				g.Expect(createdWorkload.Status.Admission).Should(gomega.BeNil())
				g.Expect(workload.HasQuotaReservation(createdWorkload)).Should(gomega.BeFalse())
				expectQuotaReservedFailure(g, createdWorkload)
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		})
	})

	ginkgo.It("Should not admit a Job if total required pods exceed available node count", func() {
		// Cluster has 8 nodes (1 GPU each). Request 9 pods with 1 GPU each.
		// Fits quota (9 <= 16), but cannot fit on 8 nodes simultaneously
		sampleJob := testingjob.MakeJob("exceed-nodes-job", ns.Name).
			Queue(kueue.LocalQueueName(localQueue.Name)).
			Parallelism(9).
			Completions(9).
			RequestAndLimit(extraResource, "1").
			Image(e2e.GetAgnHostImage(), e2e.BehaviorExitFast).
			Obj()
		behavioral.MustCreate(ctx, k8sClient, sampleJob)

		wlLookupKey := types.NamespacedName{
			Name:      workloadjob.GetWorkloadNameForJob(sampleJob.Name, sampleJob.UID),
			Namespace: ns.Name,
		}

		ginkgo.By(fmt.Sprintf("verify workload %q is not admitted due to node count failure", wlLookupKey), func() {
			createdWorkload := &kueue.Workload{}
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
				g.Expect(createdWorkload.Status.Admission).Should(gomega.BeNil())
				g.Expect(workload.HasQuotaReservation(createdWorkload)).Should(gomega.BeFalse())
				expectQuotaReservedFailure(g, createdWorkload)
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		})
	})

	ginkgo.It("Should admit and complete a Job when quota and node capacity fit", func() {
		// 2 pods with 1 GPU each: fits total quota (16) and fits on two distinct 1-GPU nodes
		sampleJob := testingjob.MakeJob("basic-fit-job", ns.Name).
			Queue(kueue.LocalQueueName(localQueue.Name)).
			Parallelism(2).
			Completions(2).
			RequestAndLimit(extraResource, "1").
			Image(e2e.GetAgnHostImage(), e2e.BehaviorExitFast).
			Obj()
		behavioral.MustCreate(ctx, k8sClient, sampleJob)

		wlLookupKey := types.NamespacedName{
			Name:      workloadjob.GetWorkloadNameForJob(sampleJob.Name, sampleJob.UID),
			Namespace: ns.Name,
		}

		createdWorkload := &kueue.Workload{}
		ginkgo.By(fmt.Sprintf("await admission of workload %q", wlLookupKey), func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
				g.Expect(createdWorkload.Status.Admission).ShouldNot(gomega.BeNil())
				g.Expect(workload.HasQuotaReservation(createdWorkload)).Should(gomega.BeTrue())
			}, behavioral.MediumTimeout, behavioral.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("ensure all pods are created, scheduled, and running", func() {
			expectPodsScheduledAndRunning(ctx, k8sClient, ns.Name, sampleJob.Name, 2)
		})

		ginkgo.By(fmt.Sprintf("verify workload %q finishes successfully", wlLookupKey), func() {
			behavioral.ExpectWorkloadToFinishWithTimeout(ctx, k8sClient, wlLookupKey, behavioral.LongTimeout)
		})
	})

	ginkgo.It("Should hold a Job when nodes are fully occupied by an active Job, then admit once nodes are released", func() {
		// Job 1 occupies all 8 nodes
		job1 := testingjob.MakeJob("job-occupy-all-nodes", ns.Name).
			Queue(kueue.LocalQueueName(localQueue.Name)).
			Parallelism(8).
			Completions(8).
			RequestAndLimit(extraResource, "1").
			TerminationGracePeriod(1).
			Image(e2e.GetAgnHostImage(), e2e.BehaviorWaitForDeletion).
			Obj()
		behavioral.MustCreate(ctx, k8sClient, job1)

		wl1Key := types.NamespacedName{
			Name:      workloadjob.GetWorkloadNameForJob(job1.Name, job1.UID),
			Namespace: ns.Name,
		}
		wl1 := &kueue.Workload{}
		ginkgo.By("await admission of job1 occupying all nodes", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wl1Key, wl1)).Should(gomega.Succeed())
				g.Expect(wl1.Status.Admission).ShouldNot(gomega.BeNil())
				g.Expect(workload.HasQuotaReservation(wl1)).Should(gomega.BeTrue())
			}, behavioral.MediumTimeout, behavioral.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("ensure job1 pods are created, scheduled, and running", func() {
			expectPodsScheduledAndRunning(ctx, k8sClient, ns.Name, job1.Name, 8)
		})

		// Job 2 requests 1 GPU. Quota fits (8 + 1 <= 16), but all 8 nodes are currently busy
		job2 := testingjob.MakeJob("job-waiting-for-nodes", ns.Name).
			Queue(kueue.LocalQueueName(localQueue.Name)).
			Parallelism(1).
			Completions(1).
			RequestAndLimit(extraResource, "1").
			Image(e2e.GetAgnHostImage(), e2e.BehaviorExitFast).
			Obj()
		behavioral.MustCreate(ctx, k8sClient, job2)

		wl2Key := types.NamespacedName{
			Name:      workloadjob.GetWorkloadNameForJob(job2.Name, job2.UID),
			Namespace: ns.Name,
		}
		wl2 := &kueue.Workload{}
		ginkgo.By("await creation of workload for job2", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wl2Key, wl2)).Should(gomega.Succeed())
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("verify job2 is not admitted while job1 is occupying all nodes", func() {
			gomega.Consistently(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wl2Key, wl2)).Should(gomega.Succeed())
				g.Expect(wl2.Status.Admission).Should(gomega.BeNil())
				g.Expect(workload.HasQuotaReservation(wl2)).Should(gomega.BeFalse())
			}, behavioral.ConsistentDuration, behavioral.Interval).Should(gomega.Succeed())
		})

		// Deactivate wl1 to evict job1 and wait for its pods to terminate, then delete job1 to release nodes
		ginkgo.By("delete job1 to release nodes", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wl1Key, wl1)).To(gomega.Succeed())
				wl1.Spec.Active = new(false)
				g.Expect(k8sClient.Update(ctx, wl1)).To(gomega.Succeed())
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			gomega.Eventually(func(g gomega.Gomega) {
				pods := &corev1.PodList{}
				g.Expect(k8sClient.List(ctx, pods, client.InNamespace(ns.Name), client.MatchingLabels{"job-name": job1.Name})).To(gomega.Succeed())
				g.Expect(pods.Items).Should(gomega.BeEmpty())
			}, behavioral.MediumTimeout, behavioral.Interval).Should(gomega.Succeed())
			gomega.Expect(k8sClient.Delete(ctx, job1, client.PropagationPolicy(metav1.DeletePropagationBackground))).To(gomega.Succeed())
		})

		// Job 2 now gets admitted
		ginkgo.By("verify job2 is admitted once nodes are released", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wl2Key, wl2)).Should(gomega.Succeed())
				g.Expect(wl2.Status.Admission).ShouldNot(gomega.BeNil())
				g.Expect(workload.HasQuotaReservation(wl2)).Should(gomega.BeTrue())
			}, behavioral.MediumTimeout, behavioral.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("ensure job2 pods are created, scheduled, and running", func() {
			expectPodsScheduledAndRunning(ctx, k8sClient, ns.Name, job2.Name, 1)
		})

		ginkgo.By(fmt.Sprintf("verify workload %q finishes successfully", wl2Key), func() {
			behavioral.ExpectWorkloadToFinishWithTimeout(ctx, k8sClient, wl2Key, behavioral.MediumTimeout)
		})
	})
})

func expectPodsScheduledAndRunning(ctx context.Context, k8sClient client.Client, ns string, jobName string, count int) {
	ginkgo.GinkgoHelper()
	gomega.Eventually(func(g gomega.Gomega) {
		pods := &corev1.PodList{}
		g.Expect(k8sClient.List(ctx, pods, client.InNamespace(ns), client.MatchingLabels{"job-name": jobName})).To(gomega.Succeed())
		g.Expect(pods.Items).Should(gomega.HaveLen(count))
		for _, pod := range pods.Items {
			g.Expect(pod.Spec.NodeName).ShouldNot(gomega.BeEmpty())
			g.Expect(pod.Status.Phase).To(gomega.Or(
				gomega.Equal(corev1.PodRunning),
				gomega.Equal(corev1.PodSucceeded),
			))
		}
	}, behavioral.MediumTimeout, behavioral.Interval).Should(gomega.Succeed())
}

func expectQuotaReservedFailure(g gomega.Gomega, wl *kueue.Workload) {
	cond := apimeta.FindStatusCondition(wl.Status.Conditions, kueue.WorkloadQuotaReserved)
	g.Expect(cond).ShouldNot(gomega.BeNil())
	g.Expect(cond).Should(gomega.BeComparableTo(&metav1.Condition{
		Type:   kueue.WorkloadQuotaReserved,
		Status: metav1.ConditionFalse,
		Reason: workloadQuotaReservedReasonSimulationFailed,
	}, cmpopts.IgnoreFields(metav1.Condition{}, "LastTransitionTime", "Message", "ObservedGeneration")))
	g.Expect(cond.Message).Should(gomega.ContainSubstring("Insufficient"))
}
