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

package certmanager

import (
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	workloadjob "sigs.k8s.io/kueue/pkg/controller/jobs/job"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("Kueue secure visibility server", func() {
	const defaultFlavor = "default-flavor"

	var (
		defaultRF    *kueue.ResourceFlavor
		localQueue   *kueue.LocalQueue
		clusterQueue *kueue.ClusterQueue
		ns           *corev1.Namespace
		firstJob     *batchv1.Job
		secondJob    *batchv1.Job
	)

	ginkgo.BeforeEach(func() {
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "e2e-")
		defaultRF = utiltestingapi.MakeResourceFlavor(defaultFlavor).Obj()
		util.MustCreate(ctx, k8sClient, defaultRF)

		clusterQueue = utiltestingapi.MakeClusterQueue("cluster-queue").
			ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas(defaultFlavor).
					Resource(corev1.ResourceCPU, "1").
					Obj(),
			).
			Obj()
		util.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, clusterQueue)

		localQueue = utiltestingapi.MakeLocalQueue("local-queue", ns.Name).ClusterQueue(clusterQueue.Name).Obj()
		util.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, localQueue)
	})
	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		util.ExpectAllPodsInNamespaceDeleted(ctx, k8sClient, ns)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, localQueue, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, defaultRF, true)
	})

	ginkgo.When("Capacity is maxed by the admitted job", func() {
		ginkgo.It("Should allow fetching information about pending workloads in ClusterQueue", func() {
			ginkgo.By("Schedule a job that maxes out the cluster queue", func() {
				firstJob = testingjob.MakeJob("job-1", ns.Name).
					Queue(kueue.LocalQueueName(localQueue.Name)).
					Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
					RequestAndLimit(corev1.ResourceCPU, "1").
					TerminationGracePeriod(1).
					BackoffLimit(0).
					Obj()
				util.MustCreate(ctx, k8sClient, firstJob)
			})
			ginkgo.By("Wait for the job to be unsuspended", func() {
				job := &batchv1.Job{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(firstJob), job)).To(gomega.Succeed())
					g.Expect(job.Spec.Suspend).Should(gomega.Equal(ptr.To(false)))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Verify there are no pending workloads", func() {
				info, err := visibilityClient.ClusterQueues().GetPendingWorkloadsSummary(ctx, clusterQueue.Name, metav1.GetOptions{})
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				gomega.Expect(info.Items).Should(gomega.BeEmpty())
			})

			ginkgo.By("Schedule a job which is pending due to low quota", func() {
				secondJob = testingjob.MakeJob("job-2", ns.Name).
					Queue(kueue.LocalQueueName(localQueue.Name)).
					Image(util.GetAgnHostImage(), util.BehaviorExitFast).
					RequestAndLimit(corev1.ResourceCPU, "1").
					Obj()
				util.MustCreate(ctx, k8sClient, secondJob)
			})

			ginkgo.By("Verify there is one pending workload", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					info, err := visibilityClient.ClusterQueues().GetPendingWorkloadsSummary(ctx, clusterQueue.Name, metav1.GetOptions{})
					g.Expect(err).NotTo(gomega.HaveOccurred())
					g.Expect(info.Items).Should(gomega.HaveLen(1))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Delete the first job to release the quota", func() {
				util.ExpectObjectToBeDeleted(ctx, k8sClient, firstJob, true)
				firstWl := &kueue.Workload{ObjectMeta: metav1.ObjectMeta{
					Namespace: firstJob.Namespace,
					Name:      workloadjob.GetWorkloadNameForJob(firstJob.Name, firstJob.UID),
				}}
				// TODO(#1789): this is no longer needed when we fix the --orphan mode for Jobs
				util.ExpectObjectToBeDeleted(ctx, k8sClient, firstWl, true)
			})

			ginkgo.By("verify second job is not suspended", func() {
				job := &batchv1.Job{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(secondJob), job)).To(gomega.Succeed())
					g.Expect(job.Spec.Suspend).Should(gomega.Equal(ptr.To(false)))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Verify there are no pending workloads, once the last workload is admitted", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					info, err := visibilityClient.ClusterQueues().GetPendingWorkloadsSummary(ctx, clusterQueue.Name, metav1.GetOptions{})
					g.Expect(err).NotTo(gomega.HaveOccurred())
					g.Expect(info.Items).Should(gomega.BeEmpty())
				}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
			})
		})
	})
})
