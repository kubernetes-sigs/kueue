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

package customconfigse2e

import (
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	jobtesting "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("Admission Fair Sharing", ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
	var (
		ns *corev1.Namespace
		rf *kueue.ResourceFlavor
	)

	ginkgo.BeforeAll(func() {
		util.UpdateKueueConfigurationAndRestart(ctx, k8sClient, defaultKueueCfg, kindClusterName, func(cfg *configapi.Configuration) {
			cfg.AdmissionFairSharing = &configapi.AdmissionFairSharing{
				UsageHalfLifeTime:     metav1.Duration{Duration: 1 * time.Second},
				UsageSamplingInterval: metav1.Duration{Duration: 1 * time.Second},
			}
		})

		rf = utiltestingapi.MakeResourceFlavor("afs-rf").Obj()
		util.MustCreate(ctx, k8sClient, rf)
	})

	ginkgo.BeforeEach(func() {
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "afs-")
	})

	ginkgo.JustAfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
	})

	ginkgo.AfterEach(func() {
		util.ExpectAllPodsInNamespaceDeleted(ctx, k8sClient, ns)
	})

	ginkgo.AfterAll(func() {
		util.ExpectObjectToBeDeleted(ctx, k8sClient, rf, true)
	})

	ginkgo.When("using UsageBasedAdmissionFairSharing within a ClusterQueue", func() {
		var (
			cq  *kueue.ClusterQueue
			lqA *kueue.LocalQueue
			lqB *kueue.LocalQueue
		)

		ginkgo.BeforeEach(func() {
			cq = utiltestingapi.MakeClusterQueue("afs-cq").
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas(rf.Name).
					Resource(corev1.ResourceCPU, "8").
					Resource(corev1.ResourceMemory, "36G").
					Obj()).
				AdmissionMode(kueue.UsageBasedAdmissionFairSharing).
				Obj()
			util.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, cq)

			lqA = utiltestingapi.MakeLocalQueue("lq-a", ns.Name).
				ClusterQueue(cq.Name).
				FairSharing(&kueue.FairSharing{Weight: ptr.To(resource.MustParse("1"))}).
				Obj()
			lqB = utiltestingapi.MakeLocalQueue("lq-b", ns.Name).
				ClusterQueue(cq.Name).
				FairSharing(&kueue.FairSharing{Weight: ptr.To(resource.MustParse("1"))}).
				Obj()
			util.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, lqA, lqB)
		})

		ginkgo.AfterEach(func() {
			util.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
		})

		ginkgo.It("should prioritize LocalQueues with lower usage", func() {
			var job1, job2, job3, jobB *batchv1.Job

			ginkgo.By("Creating jobs in lq-a to saturate the ClusterQueue", func() {
				job1 = jobtesting.MakeJob("job-a-1", ns.Name).
					Queue(kueue.LocalQueueName(lqA.Name)).
					Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
					RequestAndLimit(corev1.ResourceCPU, "4").
					RequestAndLimit(corev1.ResourceMemory, "200Mi").
					Obj()
				job2 = jobtesting.MakeJob("job-a-2", ns.Name).
					Queue(kueue.LocalQueueName(lqA.Name)).
					Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
					RequestAndLimit(corev1.ResourceCPU, "4").
					RequestAndLimit(corev1.ResourceMemory, "200Mi").
					Obj()
				util.MustCreate(ctx, k8sClient, job1)
				util.MustCreate(ctx, k8sClient, job2)
			})

			ginkgo.By("Waiting for lq-a workloads to be admitted", func() {
				util.ExpectJobUnsuspended(ctx, k8sClient, client.ObjectKeyFromObject(job1))
				util.ExpectJobUnsuspended(ctx, k8sClient, client.ObjectKeyFromObject(job2))
			})

			ginkgo.By("Verifying lq-a has accumulated usage", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					var updatedLqA kueue.LocalQueue
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(lqA), &updatedLqA)).Should(gomega.Succeed())
					g.Expect(updatedLqA.Status.FairSharing).ShouldNot(gomega.BeNil())
					g.Expect(updatedLqA.Status.FairSharing.AdmissionFairSharingStatus).ShouldNot(gomega.BeNil())
					cpuUsage := updatedLqA.Status.FairSharing.AdmissionFairSharingStatus.ConsumedResources[corev1.ResourceCPU]
					g.Expect(cpuUsage.AsApproximateFloat64()).Should(gomega.BeNumerically(">", 4.0))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Creating pending jobs in both LocalQueues", func() {
				job3 = jobtesting.MakeJob("job-a-3", ns.Name).
					Queue(kueue.LocalQueueName(lqA.Name)).
					Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
					RequestAndLimit(corev1.ResourceCPU, "4").
					RequestAndLimit(corev1.ResourceMemory, "200Mi").
					Obj()
				util.MustCreate(ctx, k8sClient, job3)
				jobB = jobtesting.MakeJob("job-b-1", ns.Name).
					Queue(kueue.LocalQueueName(lqB.Name)).
					Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
					RequestAndLimit(corev1.ResourceCPU, "4").
					RequestAndLimit(corev1.ResourceMemory, "200Mi").
					Obj()
				util.MustCreate(ctx, k8sClient, jobB)
			})

			ginkgo.By("Deleting one job from lq-a to free quota", func() {
				gomega.Expect(k8sClient.Delete(ctx, job1, client.PropagationPolicy(metav1.DeletePropagationForeground))).Should(gomega.Succeed())
			})

			ginkgo.By("Verifying lq-b job is admitted due to lower usage", func() {
				util.ExpectJobUnsuspended(ctx, k8sClient, client.ObjectKeyFromObject(jobB))
			})

			ginkgo.By("Verifying lq-a job3 remains pending", func() {
				var job batchv1.Job
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(job3), &job)).Should(gomega.Succeed())
				gomega.Expect(job.Spec.Suspend).Should(gomega.Equal(ptr.To(true)))
			})
		})
	})

	ginkgo.When("preemption is enabled across ClusterQueues in a cohort", func() {
		var (
			cq1  *kueue.ClusterQueue
			cq2  *kueue.ClusterQueue
			lq1A *kueue.LocalQueue
			lq2A *kueue.LocalQueue
		)

		ginkgo.BeforeEach(func() {
			cq1 = utiltestingapi.MakeClusterQueue("afs-cq1").
				Cohort("afs-cohort").
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas(rf.Name).
					Resource(corev1.ResourceCPU, "4").
					Resource(corev1.ResourceMemory, "12G").
					Obj()).
				AdmissionMode(kueue.UsageBasedAdmissionFairSharing).
				Preemption(kueue.ClusterQueuePreemption{
					ReclaimWithinCohort: kueue.PreemptionPolicyAny,
				}).
				Obj()

			cq2 = utiltestingapi.MakeClusterQueue("afs-cq2").
				Cohort("afs-cohort").
				ResourceGroup(*utiltestingapi.MakeFlavorQuotas(rf.Name).
					Resource(corev1.ResourceCPU, "4").
					Resource(corev1.ResourceMemory, "12G").
					Obj()).
				AdmissionMode(kueue.UsageBasedAdmissionFairSharing).
				Preemption(kueue.ClusterQueuePreemption{
					ReclaimWithinCohort: kueue.PreemptionPolicyAny,
				}).
				Obj()

			util.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, cq1, cq2)

			lq1A = utiltestingapi.MakeLocalQueue("lq1-a", ns.Name).
				ClusterQueue(cq1.Name).
				FairSharing(&kueue.FairSharing{Weight: ptr.To(resource.MustParse("1"))}).
				Obj()
			lq2A = utiltestingapi.MakeLocalQueue("lq2-a", ns.Name).
				ClusterQueue(cq2.Name).
				FairSharing(&kueue.FairSharing{Weight: ptr.To(resource.MustParse("1"))}).
				Obj()
			util.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, lq1A, lq2A)
		})

		ginkgo.AfterEach(func() {
			util.ExpectObjectToBeDeleted(ctx, k8sClient, cq1, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, cq2, true)
		})

		ginkgo.It("should preempt workloads when reclaiming quota from cohort", func() {
			var job1A, job2A, job2 *batchv1.Job

			ginkgo.By("Creating jobs in lq1-a that borrow from cohort", func() {
				job1A = jobtesting.MakeJob("job1-a-1", ns.Name).
					Queue(kueue.LocalQueueName(lq1A.Name)).
					Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
					RequestAndLimit(corev1.ResourceCPU, "3").
					RequestAndLimit(corev1.ResourceMemory, "200Mi").
					Obj()
				job2A = jobtesting.MakeJob("job1-a-2", ns.Name).
					Queue(kueue.LocalQueueName(lq1A.Name)).
					Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
					RequestAndLimit(corev1.ResourceCPU, "3").
					RequestAndLimit(corev1.ResourceMemory, "200Mi").
					Obj()
				util.MustCreate(ctx, k8sClient, job1A)
				util.MustCreate(ctx, k8sClient, job2A)
			})

			ginkgo.By("Waiting for lq1-a workloads to be admitted", func() {
				util.ExpectJobUnsuspended(ctx, k8sClient, client.ObjectKeyFromObject(job1A))
				util.ExpectJobUnsuspended(ctx, k8sClient, client.ObjectKeyFromObject(job2A))
			})

			ginkgo.By("Verifying lq1-a has accumulated usage", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					var updatedLq1A kueue.LocalQueue
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(lq1A), &updatedLq1A)).Should(gomega.Succeed())
					g.Expect(updatedLq1A.Status.FairSharing).ShouldNot(gomega.BeNil())
					g.Expect(updatedLq1A.Status.FairSharing.AdmissionFairSharingStatus).ShouldNot(gomega.BeNil())
					cpuUsage := updatedLq1A.Status.FairSharing.AdmissionFairSharingStatus.ConsumedResources[corev1.ResourceCPU]
					g.Expect(cpuUsage.AsApproximateFloat64()).Should(gomega.BeNumerically(">", 3.0))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Creating a job in lq2-a to reclaim quota from cohort", func() {
				job2 = jobtesting.MakeJob("job2-a-1", ns.Name).
					Queue(kueue.LocalQueueName(lq2A.Name)).
					Image(util.GetAgnHostImage(), util.BehaviorWaitForDeletion).
					RequestAndLimit(corev1.ResourceCPU, "3").
					RequestAndLimit(corev1.ResourceMemory, "200Mi").
					Obj()
				util.MustCreate(ctx, k8sClient, job2)
			})

			ginkgo.By("Verifying job from lq2-a is admitted", func() {
				util.ExpectJobUnsuspended(ctx, k8sClient, client.ObjectKeyFromObject(job2))
			})

			ginkgo.By("Verifying cq1 has one workload admitted", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cq1), cq1)).Should(gomega.Succeed())
					g.Expect(cq1.Status.AdmittedWorkloads).Should(gomega.Equal(int32(1)))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})
	})
})
