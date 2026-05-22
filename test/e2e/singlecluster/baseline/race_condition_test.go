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

package baseline

import (
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"

	"sigs.k8s.io/controller-runtime/pkg/client"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	workloadjob "sigs.k8s.io/kueue/pkg/controller/jobs/job"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
	workload "sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("Admission Race Condition E2E", ginkgo.Label("area:singlecluster", "feature:e2e_race_condition"), func() {
	var (
		ns          *corev1.Namespace
		alphaFlavor *kueue.ResourceFlavor
		cq          *kueue.ClusterQueue
		q           *kueue.LocalQueue
		lowWPC      *kueue.WorkloadPriorityClass
		highWPC     *kueue.WorkloadPriorityClass
	)

	ginkgo.BeforeEach(func() {
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "race-e2e-")

		lowWPC = utiltestingapi.MakeWorkloadPriorityClass("low-wpc-" + ns.Name).PriorityValue(-10).Obj()
		gomega.Expect(k8sClient.Create(ctx, lowWPC)).To(gomega.Succeed())

		highWPC = utiltestingapi.MakeWorkloadPriorityClass("high-wpc-" + ns.Name).PriorityValue(10).Obj()
		gomega.Expect(k8sClient.Create(ctx, highWPC)).To(gomega.Succeed())

		alphaFlavor = utiltestingapi.MakeResourceFlavor("alpha").Obj()
		gomega.Expect(k8sClient.Create(ctx, alphaFlavor)).To(gomega.Succeed())

		cq = utiltestingapi.MakeClusterQueue("cq-race").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas("alpha").Resource(corev1.ResourceCPU, "1").Obj()).
			Preemption(kueue.ClusterQueuePreemption{
				WithinClusterQueue: kueue.PreemptionPolicyLowerPriority,
			}).
			Obj()
		gomega.Expect(k8sClient.Create(ctx, cq)).To(gomega.Succeed())

		q = utiltestingapi.MakeLocalQueue("q-race", ns.Name).ClusterQueue(cq.Name).Obj()
		gomega.Expect(k8sClient.Create(ctx, q)).To(gomega.Succeed())
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		util.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, alphaFlavor, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, lowWPC, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, highWPC, true)
	})

	ginkgo.It("Should demonstrate the delayed admission race condition", func() {
		ginkgo.By("Creating a low priority Job job1")
		job1 := testingjob.MakeJob("job1", ns.Name).
			Queue(kueue.LocalQueueName(q.Name)).
			Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
			Request(corev1.ResourceCPU, "1").
			WorkloadPriorityClass(lowWPC.Name).
			Obj()
		gomega.Expect(k8sClient.Create(ctx, job1)).To(gomega.Succeed())

		// Fetch the created job to get its UID
		createdJob1 := &batchv1.Job{}
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(job1), createdJob1)).To(gomega.Succeed())
		}, time.Minute, util.Interval).Should(gomega.Succeed())

		wl1LookupKey := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(createdJob1.Name, createdJob1.UID), Namespace: ns.Name}
		wl1 := &kueue.Workload{}

		// Wait until job1 is created as a Workload
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, wl1LookupKey, wl1)).To(gomega.Succeed())
		}, time.Minute, util.Interval).Should(gomega.Succeed())

		// The scheduler has a 10s sleep before patching the admission status.
		// We wait just a little bit to ensure wl1 is assumed in the cache and the admission routine started.
		time.Sleep(2 * time.Second)

		ginkgo.By("Creating a high priority Job job2")
		job2 := testingjob.MakeJob("job2", ns.Name).
			Queue(kueue.LocalQueueName(q.Name)).
			Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
			Request(corev1.ResourceCPU, "1").
			WorkloadPriorityClass(highWPC.Name).
			Obj()
		gomega.Expect(k8sClient.Create(ctx, job2)).To(gomega.Succeed())

		// Fetch the created job to get its UID
		createdJob2 := &batchv1.Job{}
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(job2), createdJob2)).To(gomega.Succeed())
		}, time.Minute, util.Interval).Should(gomega.Succeed())

		wl2LookupKey := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(createdJob2.Name, createdJob2.UID), Namespace: ns.Name}
		wl2 := &kueue.Workload{}

		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, wl2LookupKey, wl2)).To(gomega.Succeed())
		}, time.Minute, util.Interval).Should(gomega.Succeed())

		ginkgo.By("Waiting for the artificial 10s sleep to finish and the dust to settle")
		time.Sleep(15 * time.Second)

		ginkgo.By("Verifying the race condition occurred")
		// Because wl1's admission patch used SSA and lacked the Evicted condition,
		// it overwrote job2's eviction attempt on wl1. wl1 retains the quota, and wl2 gets stuck.

		gomega.Expect(k8sClient.Get(ctx, wl1LookupKey, wl1)).To(gomega.Succeed())
		gomega.Expect(k8sClient.Get(ctx, wl2LookupKey, wl2)).To(gomega.Succeed())

		// Acknowledge the race condition happened so we can continue the test
		gomega.Consistently(func(g gomega.Gomega) {
			g.Expect(workload.HasQuotaReservation(wl1)).To(gomega.BeTrue(), "wl1 should have quota reservation")
			g.Expect(workload.HasQuotaReservation(wl2)).To(gomega.BeFalse(), "wl2 should be blocked")
		}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
	})
})
