/*
Copyright 2025 The Kubernetes Authors.

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

package queuename

import (
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	workloadjob "sigs.k8s.io/kueue/pkg/controller/jobs/job"
	"sigs.k8s.io/kueue/pkg/util/testing"
	testingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("ManageJobsWithoutQueueName", ginkgo.Ordered, func() {
	var ns *corev1.Namespace

	ginkgo.BeforeAll(func() {
		configurationUpdate := time.Now()
		config := defaultKueueCfg.DeepCopy()
		config.ManageJobsWithoutQueueName = true
		util.ApplyKueueConfiguration(ctx, k8sClient, config)
		util.RestartKueueController(ctx, k8sClient)
		ginkgo.GinkgoLogr.Info("Kueue configuration updated", "took", time.Since(configurationUpdate))
	})

	ginkgo.BeforeEach(func() {
		ns = &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: "e2e-",
			},
		}
		gomega.Expect(k8sClient.Create(ctx, ns)).To(gomega.Succeed())
	})
	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
	})
	ginkgo.When("creating a Job when manageJobsWithoutQueueName=true", func() {
		var (
			defaultRf    *kueue.ResourceFlavor
			localQueue   *kueue.LocalQueue
			clusterQueue *kueue.ClusterQueue
		)
		ginkgo.BeforeEach(func() {
			defaultRf = testing.MakeResourceFlavor("default").Obj()
			gomega.Expect(k8sClient.Create(ctx, defaultRf)).Should(gomega.Succeed())
			clusterQueue = testing.MakeClusterQueue("cluster-queue").
				ResourceGroup(
					*testing.MakeFlavorQuotas(defaultRf.Name).
						Resource(corev1.ResourceCPU, "2").
						Resource(corev1.ResourceMemory, "2G").Obj()).Obj()
			gomega.Expect(k8sClient.Create(ctx, clusterQueue)).Should(gomega.Succeed())
			localQueue = testing.MakeLocalQueue("main", ns.Name).ClusterQueue("cluster-queue").Obj()
			gomega.Expect(k8sClient.Create(ctx, localQueue)).Should(gomega.Succeed())
		})
		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteAllJobsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
			// Force remove workloads to be sure that cluster queue can be removed.
			gomega.Expect(util.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
			gomega.Expect(util.DeleteObject(ctx, k8sClient, localQueue)).Should(gomega.Succeed())
			util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sClient, clusterQueue, true, util.LongTimeout)
			util.ExpectObjectToBeDeletedWithTimeout(ctx, k8sClient, defaultRf, true, util.LongTimeout)
		})

		ginkgo.It("should suspend it", func() {
			var testJob *batchv1.Job
			ginkgo.By("creating an unsuspended job without a queue name", func() {
				testJob = testingjob.MakeJob("test-job", ns.Name).Suspend(false).Obj()
				gomega.Expect(k8sClient.Create(ctx, testJob)).Should(gomega.Succeed())
			})

			ginkgo.By("verifying that the job gets suspended", func() {
				jobLookupKey := types.NamespacedName{Name: testJob.Name, Namespace: ns.Name}
				createdJob := &batchv1.Job{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, jobLookupKey, createdJob)).Should(gomega.Succeed())
					g.Expect(ptr.Deref(createdJob.Spec.Suspend, false)).To(gomega.BeTrue())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("should unsuspend it", func() {
			var testJob *batchv1.Job
			var jobLookupKey types.NamespacedName
			createdJob := &batchv1.Job{}
			ginkgo.By("creating a job without queue name", func() {
				testJob = testingjob.MakeJob("test-job", ns.Name).Suspend(false).Obj()
				gomega.Expect(k8sClient.Create(ctx, testJob)).Should(gomega.Succeed())
			})
			ginkgo.By("setting the queue-name label", func() {
				jobLookupKey = types.NamespacedName{Name: testJob.Name, Namespace: ns.Name}
				gomega.Eventually(func() error {
					if err := k8sClient.Get(ctx, jobLookupKey, createdJob); err != nil {
						return err
					}
					createdJob.Labels["kueue.x-k8s.io/queue-name"] = "main"
					return k8sClient.Update(ctx, createdJob)
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
			ginkgo.By("verifying that the job is unsuspended", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, jobLookupKey, createdJob)).Should(gomega.Succeed())
					g.Expect(ptr.Deref(createdJob.Spec.Suspend, false)).To(gomega.BeFalse())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
			ginkgo.By("verifying that the job has been admitted", func() {
				wlLookupKey := types.NamespacedName{Name: workloadjob.GetWorkloadNameForJob(testJob.Name, testJob.UID), Namespace: ns.Name}
				createdWorkload := &kueue.Workload{}
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, wlLookupKey, createdWorkload)).Should(gomega.Succeed())
					g.Expect(createdWorkload.Status.Admission).ShouldNot(gomega.BeNil())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})
	})
})
