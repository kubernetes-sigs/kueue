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

package e2e

import (
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	workloadjob "sigs.k8s.io/kueue/pkg/controller/jobs/job"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta1"
	testingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("Kueue v1beta2", func() {
	var ns *corev1.Namespace
	var sampleJob *batchv1.Job
	var jobKey types.NamespacedName

	ginkgo.BeforeEach(func() {
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "e2e-")
		sampleJob = testingjob.MakeJob("test-job", ns.Name).
			Queue("main").
			Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
			RequestAndLimit(corev1.ResourceCPU, "1").
			RequestAndLimit(corev1.ResourceMemory, "20Mi").
			Obj()
		jobKey = client.ObjectKeyFromObject(sampleJob)
	})
	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		util.ExpectAllPodsInNamespaceDeleted(ctx, k8sClient, ns)
	})

	ginkgo.When("Creating a Job without a matching LocalQueue", func() {
		ginkgo.It("Should stay in suspended", func() {
			util.MustCreate(ctx, k8sClient, sampleJob)

			createdJob := &batchv1.Job{}
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, jobKey, createdJob)).Should(gomega.Succeed())
				g.Expect(*createdJob.Spec.Suspend).Should(gomega.BeTrue())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
			wlLookupKey := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(sampleJob.Name, sampleJob.UID), Namespace: ns.Name}
			createdWorkload := &kueue.Workload{}
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
				g.Expect(hasQuotaReservation(createdWorkload)).Should(gomega.BeFalse())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
			gomega.Expect(k8sClient.Delete(ctx, sampleJob)).Should(gomega.Succeed())
		})
	})

	ginkgo.When("Creating a Job With Queueing", func() {
		var (
			onDemandRF   *kueue.ResourceFlavor
			spotRF       *kueue.ResourceFlavor
			localQueue   *kueue.LocalQueue
			clusterQueue *kueue.ClusterQueue
		)
		ginkgo.BeforeEach(func() {
			onDemandRF = utiltestingapi.MakeResourceFlavor("on-demand-"+ns.Name).
				NodeLabel("instance-type", "on-demand").Obj()
			util.MustCreate(ctx, k8sClient, onDemandRF)
			spotRF = utiltestingapi.MakeResourceFlavor("spot-"+ns.Name).
				NodeLabel("instance-type", "spot").Obj()
			util.MustCreate(ctx, k8sClient, spotRF)
			clusterQueue = utiltestingapi.MakeClusterQueue("cluster-queue-"+ns.Name).
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas(onDemandRF.Name).
						Resource(corev1.ResourceCPU, "1").
						Resource(corev1.ResourceMemory, "1Gi").
						Obj(),
					*utiltestingapi.MakeFlavorQuotas(spotRF.Name).
						Resource(corev1.ResourceCPU, "1").
						Resource(corev1.ResourceMemory, "1Gi").
						Obj(),
				).
				Preemption(kueue.ClusterQueuePreemption{
					WithinClusterQueue: kueue.PreemptionPolicyLowerPriority,
				}).
				Obj()
			util.MustCreate(ctx, k8sClient, clusterQueue)
			localQueue = utiltestingapi.MakeLocalQueue("main", ns.Name).ClusterQueue(clusterQueue.Name).Obj()
			util.MustCreate(ctx, k8sClient, localQueue)
		})
		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteAllCronJobsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
			gomega.Expect(util.DeleteAllJobsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
			// Force remove workloads to be sure that cluster queue can be removed.
			gomega.Expect(util.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
			gomega.Expect(util.DeleteObject(ctx, k8sClient, localQueue)).Should(gomega.Succeed())
			util.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, onDemandRF, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, spotRF, true)
			util.ExpectAllPodsInNamespaceDeleted(ctx, k8sClient, ns)
		})

		ginkgo.It("Should unsuspend a job and set nodeSelectors", func() {
			// Use a binary that ends.
			sampleJob = (&testingjob.JobWrapper{Job: *sampleJob}).Image(util.GetAgnHostImage(), util.BehaviorExitFast).Obj()
			util.MustCreate(ctx, k8sClient, sampleJob)

			createdWorkload := &kueue.Workload{}

			// The job might have finished at this point. That shouldn't be a problem for the purpose of this test
			util.ExpectJobUnsuspendedWithNodeSelectors(ctx, k8sClient, jobKey, map[string]string{
				"instance-type": "on-demand",
			})
			wlLookupKey := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(sampleJob.Name, sampleJob.UID), Namespace: ns.Name}
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
				g.Expect(hasQuotaReservation(createdWorkload)).Should(gomega.BeFalse())
				g.Expect(createdWorkload.Status.Conditions).Should(utiltesting.HaveConditionStatusTrue(kueue.WorkloadFinished))
			}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
		})
	})
})

func hasQuotaReservation(w *kueue.Workload) bool {
	return apimeta.IsStatusConditionTrue(w.Status.Conditions, kueue.WorkloadQuotaReserved)
}
