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
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/test/util"
)

// This test is the e2e reproduction of the issue described in https://github.com/kubernetes-sigs/kueue/issues/11480.
// In practice, it should never fail (even without the fix), as the issue is extremely rare.
// The following commit contains the diff that has to be injected into Kueue in order to make this test fail non-deterministically:
// https://github.com/kshalot/kueue/commit/78f5d0217f55663d8c6ecc8c486704e9f0ae6570
var _ = ginkgo.Describe("Admission Routine Race Condition", ginkgo.Label("area:singlecluster", "feature:scheduler"), func() {
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

		ginkgo.By("Getting the UID of the created job1")
		createdJob1 := &batchv1.Job{}
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(job1), createdJob1)).To(gomega.Succeed())
		}, time.Minute, util.Interval).Should(gomega.Succeed())

		wl1LookupKey := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(createdJob1.Name, createdJob1.UID), Namespace: ns.Name}
		wl1 := &kueue.Workload{}

		ginkgo.By("Waiting until the workload for job1 is created")
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, wl1LookupKey, wl1)).To(gomega.Succeed())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		// This sleep can make the reproduction slightly less deterministic.
		// time.Sleep(5 * time.Second)

		ginkgo.By("Creating a high priority Job job2")
		job2 := testingjob.MakeJob("job2", ns.Name).
			Queue(kueue.LocalQueueName(q.Name)).
			Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
			Request(corev1.ResourceCPU, "1").
			WorkloadPriorityClass(highWPC.Name).
			Obj()
		gomega.Expect(k8sClient.Create(ctx, job2)).To(gomega.Succeed())

		ginkgo.By("Getting the UID of the created job2")
		createdJob2 := &batchv1.Job{}
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(job2), createdJob2)).To(gomega.Succeed())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		wl2LookupKey := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(createdJob2.Name, createdJob2.UID), Namespace: ns.Name}
		wl2 := &kueue.Workload{}

		ginkgo.By("Waiting until the workload for job2 is created")
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, wl2LookupKey, wl2)).To(gomega.Succeed())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())

		// This sleep can make the reproduction slightly less deterministic.
		// time.Sleep(10 * time.Second)

		ginkgo.By("Verifying the race condition occurred")
		gomega.Eventually(func(g gomega.Gomega) {
			// Because wl1's admission patch used SSA and lacked the Evicted condition,
			// it overwrote job2's eviction attempt on wl1. wl1 retains the quota, and wl2 gets stuck.
			g.Expect(k8sClient.Get(ctx, wl1LookupKey, wl1)).To(gomega.Succeed())
			g.Expect(k8sClient.Get(ctx, wl2LookupKey, wl2)).To(gomega.Succeed())

			g.Expect(workload.HasQuotaReservation(wl1)).To(gomega.BeFalse())
			g.Expect(workload.HasQuotaReservation(wl2)).To(gomega.BeTrue())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())
	})
})
