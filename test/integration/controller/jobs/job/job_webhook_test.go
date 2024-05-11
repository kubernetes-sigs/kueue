/*
Copyright 2022 The Kubernetes Authors.

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

package job

import (
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/discovery"
	"k8s.io/utils/ptr"

	"sigs.k8s.io/kueue/pkg/controller/constants"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/controller/jobs/job"
	"sigs.k8s.io/kueue/pkg/util/kubeversion"
	testingjob "sigs.k8s.io/kueue/pkg/util/testingjobs/job"
	"sigs.k8s.io/kueue/test/integration/framework"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("Job Webhook", func() {
	var ns *corev1.Namespace

	ginkgo.When("With manageJobsWithoutQueueName enabled", ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
		ginkgo.BeforeAll(func() {
			fwk = &framework.Framework{
				CRDPath:     crdPath,
				WebhookPath: webhookPath,
			}
			cfg = fwk.Init()

			discoveryClient, err := discovery.NewDiscoveryClientForConfig(cfg)
			gomega.Expect(err).NotTo(gomega.HaveOccurred())
			serverVersionFetcher = kubeversion.NewServerVersionFetcher(discoveryClient)
			err = serverVersionFetcher.FetchServerVersion()
			gomega.Expect(err).NotTo(gomega.HaveOccurred())

			ctx, k8sClient = fwk.RunManager(cfg, managerSetup(
				jobframework.WithManageJobsWithoutQueueName(true),
				jobframework.WithKubeServerVersion(serverVersionFetcher),
			))
		})
		ginkgo.BeforeEach(func() {
			ns = &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					GenerateName: "job-",
				},
			}
			gomega.Expect(k8sClient.Create(ctx, ns)).To(gomega.Succeed())
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		})
		ginkgo.AfterAll(func() {
			fwk.Teardown()
		})

		ginkgo.It("checking the workload is not created when JobMinParallelismAnnotation is invalid", func() {
			job := testingjob.MakeJob("job-with-queue-name", ns.Name).Queue("foo").SetAnnotation(job.JobMinParallelismAnnotation, "a").Obj()
			err := k8sClient.Create(ctx, job)
			gomega.Expect(err).Should(gomega.HaveOccurred())
			gomega.Expect(apierrors.IsForbidden(err)).Should(gomega.BeTrue(), "error: %v", err)
		})

		ginkgo.It("Should suspend a Job even no queue name specified", func() {
			job := testingjob.MakeJob("job-without-queue-name", ns.Name).Suspend(false).Obj()
			gomega.Expect(k8sClient.Create(ctx, job)).Should(gomega.Succeed())

			lookupKey := types.NamespacedName{Name: job.Name, Namespace: job.Namespace}
			createdJob := &batchv1.Job{}
			gomega.Eventually(func() bool {
				if err := k8sClient.Get(ctx, lookupKey, createdJob); err != nil {
					return false
				}
				return createdJob.Spec.Suspend != nil && *createdJob.Spec.Suspend
			}, util.Timeout, util.Interval).Should(gomega.BeTrue())
		})

		ginkgo.It("Should not update unsuspend Job successfully when adding queue name", func() {
			job := testingjob.MakeJob("job-without-queue-name", ns.Name).Suspend(false).Obj()
			gomega.Expect(k8sClient.Create(ctx, job)).Should(gomega.Succeed())

			lookupKey := types.NamespacedName{Name: job.Name, Namespace: job.Namespace}
			createdJob := &batchv1.Job{}
			gomega.Expect(k8sClient.Get(ctx, lookupKey, createdJob)).Should(gomega.Succeed())

			createdJob.Annotations = map[string]string{constants.QueueAnnotation: "queue"}
			createdJob.Spec.Suspend = ptr.To(false)
			gomega.Expect(k8sClient.Update(ctx, createdJob)).ShouldNot(gomega.Succeed())
		})

		ginkgo.It("Should not succeed Job when kubernetes less than 1.27 and sync completions annotation is enabled for indexed jobs", func() {
			if v := serverVersionFetcher.GetServerVersion(); v.AtLeast(kubeversion.KubeVersion1_27) {
				ginkgo.Skip("Kubernetes version is not less then 1.27. Skip test...")
			}
			j := testingjob.MakeJob("job-without-queue-name", ns.Name).
				Parallelism(5).
				Completions(5).
				SetAnnotation(job.JobCompletionsEqualParallelismAnnotation, "true").
				Indexed(true).
				Obj()
			gomega.Expect(apierrors.IsForbidden(k8sClient.Create(ctx, j))).Should(gomega.BeTrue())
		})
	})

	ginkgo.When("with manageJobsWithoutQueueName disabled", ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
		ginkgo.BeforeAll(func() {
			fwk = &framework.Framework{
				CRDPath:     crdPath,
				WebhookPath: webhookPath,
			}
			cfg = fwk.Init()
			ctx, k8sClient = fwk.RunManager(cfg, managerSetup(jobframework.WithManageJobsWithoutQueueName(false)))
		})
		ginkgo.BeforeEach(func() {
			ns = &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					GenerateName: "job-",
				},
			}
			gomega.Expect(k8sClient.Create(ctx, ns)).To(gomega.Succeed())
		})
		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		})
		ginkgo.AfterAll(func() {
			fwk.Teardown()
		})

		ginkgo.It("should suspend a Job when created in unsuspend state", func() {
			job := testingjob.MakeJob("job-with-queue-name", ns.Name).Suspend(false).Queue("default").Obj()
			gomega.Expect(k8sClient.Create(ctx, job)).Should(gomega.Succeed())

			lookupKey := types.NamespacedName{Name: job.Name, Namespace: job.Namespace}
			createdJob := &batchv1.Job{}
			gomega.Eventually(func() bool {
				if err := k8sClient.Get(ctx, lookupKey, createdJob); err != nil {
					return false
				}
				return createdJob.Spec.Suspend != nil && *createdJob.Spec.Suspend
			}, util.Timeout, util.Interval).Should(gomega.BeTrue())
		})

		ginkgo.It("should not suspend a Job when no queue name specified", func() {
			job := testingjob.MakeJob("job-without-queue-name", ns.Name).Suspend(false).Obj()
			gomega.Expect(k8sClient.Create(ctx, job)).Should(gomega.Succeed())

			lookupKey := types.NamespacedName{Name: job.Name, Namespace: job.Namespace}
			createdJob := &batchv1.Job{}
			gomega.Eventually(func() bool {
				if err := k8sClient.Get(ctx, lookupKey, createdJob); err != nil {
					return false
				}
				return createdJob.Spec.Suspend != nil && !(*createdJob.Spec.Suspend)
			}, util.Timeout, util.Interval).Should(gomega.BeTrue())
		})

		ginkgo.It("should not update unsuspend Job successfully when changing queue name", func() {
			job := testingjob.MakeJob("job-with-queue-name", ns.Name).Queue("queue").Obj()
			gomega.Expect(k8sClient.Create(ctx, job)).Should(gomega.Succeed())

			lookupKey := types.NamespacedName{Name: job.Name, Namespace: job.Namespace}
			createdJob := &batchv1.Job{}
			gomega.Expect(k8sClient.Get(ctx, lookupKey, createdJob)).Should(gomega.Succeed())

			createdJob.Labels[constants.QueueLabel] = "queue2"
			createdJob.Spec.Suspend = ptr.To(false)
			gomega.Expect(k8sClient.Update(ctx, createdJob)).ShouldNot(gomega.Succeed())
		})
	})
})
