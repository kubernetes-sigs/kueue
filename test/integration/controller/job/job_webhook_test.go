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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/util/pointer"
	"sigs.k8s.io/kueue/pkg/util/testing"
	"sigs.k8s.io/kueue/test/integration/framework"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("Job Webhook", func() {
	var ns *corev1.Namespace

	ginkgo.When("With manageJobsWithoutQueueName enabled", func() {

		ginkgo.BeforeEach(func() {
			fwk = &framework.Framework{
				ManagerSetup: managerSetup(jobframework.WithManageJobsWithoutQueueName(true)),
				CRDPath:      crdPath,
				WebhookPath:  webhookPath,
			}
			ctx, cfg, k8sClient = fwk.Setup()

			ns = &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					GenerateName: "job-",
				},
			}
			gomega.Expect(k8sClient.Create(ctx, ns)).To(gomega.Succeed())
		})
		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			fwk.Teardown()
		})

		ginkgo.It("Should suspend a Job even no queue name specified", func() {
			job := testing.MakeJob("job-without-queue-name", ns.Name).Suspend(false).Obj()
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
			job := testing.MakeJob("job-without-queue-name", ns.Name).Suspend(false).Obj()
			gomega.Expect(k8sClient.Create(ctx, job)).Should(gomega.Succeed())

			lookupKey := types.NamespacedName{Name: job.Name, Namespace: job.Namespace}
			createdJob := &batchv1.Job{}
			gomega.Expect(k8sClient.Get(ctx, lookupKey, createdJob)).Should(gomega.Succeed())

			createdJob.Annotations = map[string]string{constants.QueueAnnotation: "queue"}
			createdJob.Spec.Suspend = pointer.Bool(false)
			gomega.Expect(k8sClient.Update(ctx, createdJob)).ShouldNot(gomega.Succeed())
		})
	})

	ginkgo.When("with manageJobsWithoutQueueName disabled", func() {

		ginkgo.BeforeEach(func() {
			fwk = &framework.Framework{
				ManagerSetup: managerSetup(jobframework.WithManageJobsWithoutQueueName(false)),
				CRDPath:      crdPath,
				WebhookPath:  webhookPath,
			}
			ctx, cfg, k8sClient = fwk.Setup()

			ns = &corev1.Namespace{
				ObjectMeta: metav1.ObjectMeta{
					GenerateName: "job-",
				},
			}
			gomega.Expect(k8sClient.Create(ctx, ns)).To(gomega.Succeed())
		})
		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			fwk.Teardown()
		})

		ginkgo.It("should suspend a Job when created in unsuspend state", func() {
			job := testing.MakeJob("job-with-queue-name", ns.Name).Suspend(false).Queue("default").Obj()
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
			job := testing.MakeJob("job-without-queue-name", ns.Name).Suspend(false).Obj()
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
			job := testing.MakeJob("job-with-queue-name", ns.Name).Queue("queue").Obj()
			gomega.Expect(k8sClient.Create(ctx, job)).Should(gomega.Succeed())

			lookupKey := types.NamespacedName{Name: job.Name, Namespace: job.Namespace}
			createdJob := &batchv1.Job{}
			gomega.Expect(k8sClient.Get(ctx, lookupKey, createdJob)).Should(gomega.Succeed())

			createdJob.Annotations[constants.QueueAnnotation] = "queue2"
			createdJob.Spec.Suspend = pointer.Bool(false)
			gomega.Expect(k8sClient.Update(ctx, createdJob)).ShouldNot(gomega.Succeed())
		})
	})
})
