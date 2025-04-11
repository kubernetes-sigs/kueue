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

package jobs

import (
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/discovery"
	"k8s.io/utils/ptr"

	"sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/controller/jobs/job"
	"sigs.k8s.io/kueue/pkg/util/kubeversion"
	"sigs.k8s.io/kueue/pkg/util/testing"
	testingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("Job Webhook With manageJobsWithoutQueueName enabled", ginkgo.Ordered, func() {
	var ns *corev1.Namespace
	ginkgo.BeforeAll(func() {
		discoveryClient, err := discovery.NewDiscoveryClientForConfig(cfg)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		serverVersionFetcher = kubeversion.NewServerVersionFetcher(discoveryClient)
		err = serverVersionFetcher.FetchServerVersion()
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		fwk.StartManager(ctx, cfg, managerSetup(
			job.SetupWebhook,
			jobframework.WithManageJobsWithoutQueueName(true),
			jobframework.WithManagedJobsNamespaceSelector(util.NewNamespaceSelectorExcluding("unmanaged-ns")),
			jobframework.WithKubeServerVersion(serverVersionFetcher),
		))
		unmanagedNamespace := testing.MakeNamespace("unmanaged-ns")
		util.MustCreate(ctx, k8sClient, unmanagedNamespace)
	})
	ginkgo.BeforeEach(func() {
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "job-")
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
	})
	ginkgo.AfterAll(func() {
		fwk.StopManager(ctx)
	})

	ginkgo.It("checking the workload is not created when JobMinParallelismAnnotation is invalid", func() {
		job := testingjob.MakeJob("job-with-queue-name", ns.Name).Queue("foo").SetAnnotation(job.JobMinParallelismAnnotation, "a").Obj()
		err := k8sClient.Create(ctx, job)
		gomega.Expect(err).Should(gomega.HaveOccurred())
		gomega.Expect(err).Should(testing.BeForbiddenError())
	})

	ginkgo.It("Should suspend a Job even no queue name specified", func() {
		job := testingjob.MakeJob("job-without-queue-name", ns.Name).Suspend(false).Obj()
		util.MustCreate(ctx, k8sClient, job)

		lookupKey := types.NamespacedName{Name: job.Name, Namespace: job.Namespace}
		createdJob := &batchv1.Job{}
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, lookupKey, createdJob)).Should(gomega.Succeed())
			g.Expect(createdJob.Spec.Suspend).Should(gomega.Equal(ptr.To(true)))
		}, util.Timeout, util.Interval).Should(gomega.Succeed())
	})

	ginkgo.It("Should not suspend a Job with no queue name specified in an unmanaged namespace", func() {
		job := testingjob.MakeJob("job-without-queue-name-unmanaged", "unmanaged-ns").Suspend(false).Obj()
		util.MustCreate(ctx, k8sClient, job)

		lookupKey := types.NamespacedName{Name: job.Name, Namespace: job.Namespace}
		createdJob := &batchv1.Job{}
		gomega.Consistently(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, lookupKey, createdJob)).Should(gomega.Succeed())
			g.Expect(createdJob.Spec.Suspend).Should(gomega.Equal(ptr.To(false)))
		}, util.ConsistentDuration, util.Interval).Should(gomega.Succeed())
	})

	ginkgo.It("Should not update unsuspend Job successfully when adding queue name", func() {
		job := testingjob.MakeJob("job-without-queue-name", ns.Name).Suspend(false).Obj()
		util.MustCreate(ctx, k8sClient, job)

		lookupKey := types.NamespacedName{Name: job.Name, Namespace: job.Namespace}
		createdJob := &batchv1.Job{}
		gomega.Expect(k8sClient.Get(ctx, lookupKey, createdJob)).Should(gomega.Succeed())

		createdJob.Annotations = map[string]string{constants.QueueAnnotation: "queue"}
		createdJob.Spec.Suspend = ptr.To(false)
		gomega.Expect(k8sClient.Update(ctx, createdJob)).ShouldNot(gomega.Succeed())
	})
})

var _ = ginkgo.Describe("Job Webhook with manageJobsWithoutQueueName disabled", ginkgo.Ordered, func() {
	var ns *corev1.Namespace
	ginkgo.BeforeAll(func() {
		fwk.StartManager(ctx, cfg, managerSetup(job.SetupWebhook, jobframework.WithManageJobsWithoutQueueName(false)))
	})
	ginkgo.BeforeEach(func() {
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "job-")
	})
	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
	})
	ginkgo.AfterAll(func() {
		fwk.StopManager(ctx)
	})

	ginkgo.It("should suspend a Job when created in unsuspend state", func() {
		job := testingjob.MakeJob("job-with-queue-name", ns.Name).Suspend(false).Queue("default").Obj()
		util.MustCreate(ctx, k8sClient, job)

		lookupKey := types.NamespacedName{Name: job.Name, Namespace: job.Namespace}
		createdJob := &batchv1.Job{}
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, lookupKey, createdJob)).Should(gomega.Succeed())
			g.Expect(createdJob.Spec.Suspend).Should(gomega.Equal(ptr.To(true)))
		}, util.Timeout, util.Interval).Should(gomega.Succeed())
	})

	ginkgo.It("should not suspend a Job when no queue name specified", func() {
		job := testingjob.MakeJob("job-without-queue-name", ns.Name).Suspend(false).Obj()
		util.MustCreate(ctx, k8sClient, job)

		lookupKey := types.NamespacedName{Name: job.Name, Namespace: job.Namespace}
		createdJob := &batchv1.Job{}
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, lookupKey, createdJob)).Should(gomega.Succeed())
			g.Expect(createdJob.Spec.Suspend).Should(gomega.Equal(ptr.To(false)))
		}, util.Timeout, util.Interval).Should(gomega.Succeed())
	})

	ginkgo.It("should not update unsuspend Job successfully when changing queue name", func() {
		job := testingjob.MakeJob("job-with-queue-name", ns.Name).Queue("queue").Obj()
		util.MustCreate(ctx, k8sClient, job)

		lookupKey := types.NamespacedName{Name: job.Name, Namespace: job.Namespace}
		createdJob := &batchv1.Job{}
		gomega.Expect(k8sClient.Get(ctx, lookupKey, createdJob)).Should(gomega.Succeed())

		createdJob.Labels[constants.QueueLabel] = "queue2"
		createdJob.Spec.Suspend = ptr.To(false)
		gomega.Expect(k8sClient.Update(ctx, createdJob)).ShouldNot(gomega.Succeed())
	})

	ginkgo.It("should allow unsuspending a partially admissible job with its minimum parallelism", func() {
		job := testingjob.MakeJob("job-with-queue-name", ns.Name).Queue("queue").
			Parallelism(6).
			Completions(6).
			SetAnnotation(job.JobMinParallelismAnnotation, "4").
			Obj()
		util.MustCreate(ctx, k8sClient, job)

		lookupKey := types.NamespacedName{Name: job.Name, Namespace: job.Namespace}
		createdJob := &batchv1.Job{}
		gomega.Expect(k8sClient.Get(ctx, lookupKey, createdJob)).Should(gomega.Succeed())

		createdJob.Spec.Parallelism = ptr.To[int32](4)
		createdJob.Spec.Suspend = ptr.To(false)
		gomega.Expect(k8sClient.Update(ctx, createdJob)).Should(gomega.Succeed())
	})

	ginkgo.It("should allow unsuspending a partially admissible job with a parallelism lower then minimum", func() {
		// This can happen if the job:
		// 1. Is admitted
		// 2. Makes progress and increments the reclaimable counts
		// 3. Is evicted
		// 4. Is re-admitted (the parallelism being less then min due to reclaim)
		job := testingjob.MakeJob("job-with-queue-name", ns.Name).Queue("queue").
			Parallelism(6).
			Completions(6).
			SetAnnotation(job.JobMinParallelismAnnotation, "4").
			Obj()
		util.MustCreate(ctx, k8sClient, job)

		lookupKey := types.NamespacedName{Name: job.Name, Namespace: job.Namespace}
		createdJob := &batchv1.Job{}
		gomega.Expect(k8sClient.Get(ctx, lookupKey, createdJob)).Should(gomega.Succeed())

		createdJob.Spec.Parallelism = ptr.To[int32](3)
		createdJob.Spec.Suspend = ptr.To(false)
		gomega.Expect(k8sClient.Update(ctx, createdJob)).Should(gomega.Succeed())
	})

	ginkgo.It("should allow restoring parallelism", func() {
		originalJob := testingjob.MakeJob("job-with-queue-name", ns.Name).Queue("queue").
			Parallelism(3).
			Completions(6).
			SetAnnotation(job.StoppingAnnotation, "true").
			SetAnnotation(job.JobMinParallelismAnnotation, "2").
			Obj()
		util.MustCreate(ctx, k8sClient, originalJob)

		lookupKey := types.NamespacedName{Name: originalJob.Name, Namespace: originalJob.Namespace}
		updatedJob := &batchv1.Job{}
		gomega.Expect(k8sClient.Get(ctx, lookupKey, updatedJob)).Should(gomega.Succeed())

		updatedJob.Spec.Parallelism = ptr.To[int32](6)
		delete(updatedJob.Annotations, job.StoppingAnnotation)
		gomega.Expect(k8sClient.Update(ctx, updatedJob)).Should(gomega.Succeed())
	})
})
