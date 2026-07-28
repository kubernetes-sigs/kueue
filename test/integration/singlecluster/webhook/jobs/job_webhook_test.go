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
	"k8s.io/apimachinery/pkg/util/rand"
	"k8s.io/client-go/discovery"
	"k8s.io/utils/ptr"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/controller/jobs/job"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/util/kubeversion"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("Job Webhook With manageJobsWithoutQueueName enabled", func() {
	var (
		ns          *corev1.Namespace
		unmanagedNs *corev1.Namespace
	)
	ginkgo.BeforeEach(func() {
		discoveryClient, err := discovery.NewDiscoveryClientForConfig(cfg)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		serverVersionFetcher = kubeversion.NewServerVersionFetcher(discoveryClient)
		err = serverVersionFetcher.FetchServerVersion()
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		unmanagedNsName := "unmanaged-ns-" + rand.String(8)
		fwk.StartManager(ctx, cfg, managerSetup(
			job.SetupWebhook,
			jobframework.WithManageJobsWithoutQueueName(true),
			jobframework.WithManagedJobsNamespaceSelector(util.NewNamespaceSelectorExcluding(unmanagedNsName)),
			jobframework.WithKubeServerVersion(serverVersionFetcher),
		))
		unmanagedNs = utiltesting.MakeNamespace(unmanagedNsName)
		util.MustCreate(ctx, k8sClient, unmanagedNs)
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "job-")
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, unmanagedNs)).To(gomega.Succeed())
		fwk.StopManager(ctx)
	})

	ginkgo.It("checking the workload is not created when JobMinParallelismAnnotation is invalid", func() {
		job := testingjob.MakeJob("job-with-queue-name", ns.Name).Queue("foo").SetAnnotation(job.JobMinParallelismAnnotation, "a").Obj()
		err := k8sClient.Create(ctx, job)
		gomega.Expect(err).Should(gomega.HaveOccurred())
		gomega.Expect(err).Should(utiltesting.BeForbiddenError())
	})

	ginkgo.It("Should suspend a Job even no queue name specified", func() {
		job := testingjob.MakeJob("job-without-queue-name", ns.Name).Suspend(false).Obj()
		util.MustCreate(ctx, k8sClient, job)

		lookupKey := types.NamespacedName{Name: job.Name, Namespace: job.Namespace}
		createdJob := &batchv1.Job{}
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, lookupKey, createdJob)).Should(gomega.Succeed())
			g.Expect(createdJob.Spec.Suspend).Should(gomega.Equal(new(true)))
		}, util.Timeout, util.Interval).Should(gomega.Succeed())
	})

	ginkgo.It("Should not suspend a Job with no queue name specified in an unmanaged namespace", func() {
		job := testingjob.MakeJob("job-without-queue-name-unmanaged", unmanagedNs.Name).Suspend(false).Obj()
		util.MustCreate(ctx, k8sClient, job)

		lookupKey := types.NamespacedName{Name: job.Name, Namespace: job.Namespace}
		createdJob := &batchv1.Job{}
		gomega.Consistently(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, lookupKey, createdJob)).Should(gomega.Succeed())
			g.Expect(createdJob.Spec.Suspend).Should(gomega.Equal(new(false)))
		}, util.ConsistentDuration, util.ShortInterval).Should(gomega.Succeed())
	})

	ginkgo.It("Should not inject default queue label for a Job in an unmanaged namespace", func() {
		defaultLq := utiltestingapi.MakeLocalQueue("default", unmanagedNs.Name).ClusterQueue("cluster-queue").Obj()
		util.MustCreate(ctx, k8sClient, defaultLq)
		ginkgo.DeferCleanup(func() {
			util.ExpectObjectToBeDeleted(ctx, k8sClient, defaultLq, true)
		})

		j := testingjob.MakeJob("job-default-lq-unmanaged", unmanagedNs.Name).Suspend(false).Obj()
		util.MustCreate(ctx, k8sClient, j)

		lookupKey := types.NamespacedName{Name: j.Name, Namespace: j.Namespace}
		createdJob := &batchv1.Job{}
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, lookupKey, createdJob)).Should(gomega.Succeed())
			g.Expect(createdJob.Spec.Suspend).Should(gomega.Equal(new(false)))
			g.Expect(createdJob.Labels).ShouldNot(gomega.HaveKey(constants.QueueLabel))
		}, util.ShortTimeout, util.ShortInterval).Should(gomega.Succeed())
	})

	ginkgo.It("Should not update unsuspend Job successfully when adding queue name", func() {
		job := testingjob.MakeJob("job-without-queue-name", ns.Name).Suspend(false).Obj()
		util.MustCreate(ctx, k8sClient, job)

		lookupKey := types.NamespacedName{Name: job.Name, Namespace: job.Namespace}
		createdJob := &batchv1.Job{}
		gomega.Expect(k8sClient.Get(ctx, lookupKey, createdJob)).Should(gomega.Succeed())

		createdJob.Labels = map[string]string{constants.QueueLabel: "queue"}
		createdJob.Spec.Suspend = new(false)
		gomega.Expect(k8sClient.Update(ctx, createdJob)).ShouldNot(gomega.Succeed())
	})
})

var _ = ginkgo.Describe("Job Webhook with manageJobsWithoutQueueName disabled", func() {
	var ns *corev1.Namespace
	ginkgo.BeforeEach(func() {
		fwk.StartManager(ctx, cfg, managerSetup(job.SetupWebhook, jobframework.WithManageJobsWithoutQueueName(false)))
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "job-")
	})
	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		fwk.StopManager(ctx)
	})

	ginkgo.It("should suspend a Job when created in unsuspend state", func() {
		job := testingjob.MakeJob("job-with-queue-name", ns.Name).Suspend(false).Queue("default").Obj()
		util.MustCreate(ctx, k8sClient, job)

		lookupKey := types.NamespacedName{Name: job.Name, Namespace: job.Namespace}
		createdJob := &batchv1.Job{}
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, lookupKey, createdJob)).Should(gomega.Succeed())
			g.Expect(createdJob.Spec.Suspend).Should(gomega.Equal(new(true)))
		}, util.Timeout, util.Interval).Should(gomega.Succeed())
	})

	ginkgo.It("should not suspend a Job when no queue name specified", func() {
		job := testingjob.MakeJob("job-without-queue-name", ns.Name).Suspend(false).Obj()
		util.MustCreate(ctx, k8sClient, job)

		lookupKey := types.NamespacedName{Name: job.Name, Namespace: job.Namespace}
		createdJob := &batchv1.Job{}
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, lookupKey, createdJob)).Should(gomega.Succeed())
			g.Expect(createdJob.Spec.Suspend).Should(gomega.Equal(new(false)))
		}, util.Timeout, util.Interval).Should(gomega.Succeed())
	})

	ginkgo.It("should not update unsuspend Job successfully when changing queue name", func() {
		job := testingjob.MakeJob("job-with-queue-name", ns.Name).Queue("queue").Obj()
		util.MustCreate(ctx, k8sClient, job)

		lookupKey := types.NamespacedName{Name: job.Name, Namespace: job.Namespace}
		createdJob := &batchv1.Job{}
		gomega.Expect(k8sClient.Get(ctx, lookupKey, createdJob)).Should(gomega.Succeed())

		createdJob.Labels[constants.QueueLabel] = "queue2"
		createdJob.Spec.Suspend = new(false)
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
		createdJob.Spec.Suspend = new(false)
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
		createdJob.Spec.Suspend = new(false)
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

	ginkgo.It("Should not set the default WorkloadPriorityClass label when the feature gate is disabled", func() {
		defaultWPC := utiltestingapi.MakeWorkloadPriorityClass(constants.DefaultWorkloadPriorityClassName).PriorityValue(100).Obj()
		util.MustCreate(ctx, k8sClient, defaultWPC)
		ginkgo.DeferCleanup(func() {
			gomega.Expect(k8sClient.Delete(ctx, defaultWPC)).To(gomega.Succeed())
		})

		j := testingjob.MakeJob("job-without-wpc-gate-off", ns.Name).Queue("test-queue").Obj()
		util.MustCreate(ctx, k8sClient, j)

		lookupKey := types.NamespacedName{Name: j.Name, Namespace: j.Namespace}
		createdJob := &batchv1.Job{}
		gomega.Consistently(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, lookupKey, createdJob)).Should(gomega.Succeed())
			g.Expect(createdJob.Labels).ShouldNot(gomega.HaveKey(constants.WorkloadPriorityClassLabel))
		}, util.ConsistentDuration, util.ShortInterval).Should(gomega.Succeed())
	})
})

var _ = ginkgo.Describe("Job Webhook with WorkloadPriorityClassDefaulting enabled", ginkgo.Ordered, func() {
	var ns *corev1.Namespace
	var defaultWPC *kueue.WorkloadPriorityClass

	ginkgo.BeforeAll(func() {
		fwk.StartManager(ctx, cfg, managerSetup(job.SetupWebhook))
	})
	ginkgo.BeforeEach(func() {
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.WorkloadPriorityClassDefaulting, true)
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "wpc-defaulting-")
		defaultWPC = utiltestingapi.MakeWorkloadPriorityClass(constants.DefaultWorkloadPriorityClassName).PriorityValue(100).Obj()
		util.MustCreate(ctx, k8sClient, defaultWPC)
	})
	ginkgo.AfterEach(func() {
		gomega.Expect(k8sClient.Delete(ctx, defaultWPC)).To(gomega.Succeed())
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
	})
	ginkgo.AfterAll(func() {
		fwk.StopManager(ctx)
	})

	ginkgo.It("Should set the default WorkloadPriorityClass label when the default WPC exists", func() {
		j := testingjob.MakeJob("job-without-wpc", ns.Name).Queue("test-queue").Obj()
		util.MustCreate(ctx, k8sClient, j)

		lookupKey := types.NamespacedName{Name: j.Name, Namespace: j.Namespace}
		createdJob := &batchv1.Job{}
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, lookupKey, createdJob)).Should(gomega.Succeed())
			g.Expect(createdJob.Labels).Should(gomega.HaveKeyWithValue(
				constants.WorkloadPriorityClassLabel,
				constants.DefaultWorkloadPriorityClassName,
			))
		}, util.Timeout, util.Interval).Should(gomega.Succeed())
	})

	ginkgo.It("Should not override an existing WorkloadPriorityClass label", func() {
		highWPC := utiltestingapi.MakeWorkloadPriorityClass("high").PriorityValue(1000).Obj()
		util.MustCreate(ctx, k8sClient, highWPC)
		ginkgo.DeferCleanup(func() {
			gomega.Expect(k8sClient.Delete(ctx, highWPC)).To(gomega.Succeed())
		})

		j := testingjob.MakeJob("job-with-wpc", ns.Name).Queue("test-queue").WorkloadPriorityClass("high").Obj()
		util.MustCreate(ctx, k8sClient, j)

		lookupKey := types.NamespacedName{Name: j.Name, Namespace: j.Namespace}
		createdJob := &batchv1.Job{}
		gomega.Consistently(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, lookupKey, createdJob)).Should(gomega.Succeed())
			g.Expect(createdJob.Labels).Should(gomega.HaveKeyWithValue(
				constants.WorkloadPriorityClassLabel, "high",
			))
		}, util.ConsistentDuration, util.ShortInterval).Should(gomega.Succeed())
	})

	ginkgo.It("Should not set the label when the default WPC does not exist", func() {
		gomega.Expect(k8sClient.Delete(ctx, defaultWPC)).To(gomega.Succeed())

		j := testingjob.MakeJob("job-no-default-wpc", ns.Name).Queue("test-queue").Obj()
		util.MustCreate(ctx, k8sClient, j)

		lookupKey := types.NamespacedName{Name: j.Name, Namespace: j.Namespace}
		createdJob := &batchv1.Job{}
		gomega.Consistently(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, lookupKey, createdJob)).Should(gomega.Succeed())
			g.Expect(createdJob.Labels).ShouldNot(gomega.HaveKey(constants.WorkloadPriorityClassLabel))
		}, util.ConsistentDuration, util.ShortInterval).Should(gomega.Succeed())

		defaultWPC = utiltestingapi.MakeWorkloadPriorityClass(constants.DefaultWorkloadPriorityClassName).PriorityValue(100).Obj()
		util.MustCreate(ctx, k8sClient, defaultWPC)
	})
})
