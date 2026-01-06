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
	corev1 "k8s.io/api/core/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/controller/core/indexer"
	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/controller/jobs/raycluster"
	workloadrayjob "sigs.k8s.io/kueue/pkg/controller/jobs/rayjob"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	testingraycluster "sigs.k8s.io/kueue/pkg/util/testingjobs/raycluster"
	"sigs.k8s.io/kueue/pkg/webhooks"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("RayCluster Webhook", func() {
	var ns *corev1.Namespace

	ginkgo.When("With manageJobsWithoutQueueName disabled", ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
		ginkgo.BeforeAll(func() {
			fwk.StartManager(ctx, cfg, managerSetup(raycluster.SetupRayClusterWebhook))
		})
		ginkgo.BeforeEach(func() {
			ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "raycluster-")
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		})
		ginkgo.AfterAll(func() {
			fwk.StopManager(ctx)
		})

		ginkgo.It("the creation doesn't succeed if the queue name is invalid", func() {
			job := testingraycluster.MakeCluster("raycluster", ns.Name).Queue("indexed_job").Obj()
			err := k8sClient.Create(ctx, job)
			gomega.Expect(err).Should(gomega.HaveOccurred())
			gomega.Expect(err).Should(utiltesting.BeForbiddenError())
		})
	})

	ginkgo.When("With manageJobsWithoutQueueName enabled", ginkgo.Ordered, ginkgo.ContinueOnFailure, func() {
		ginkgo.BeforeAll(func() {
			fwk.StartManager(ctx, cfg, managerSetup(func(mgr ctrl.Manager, opts ...jobframework.Option) error {
				reconciler, err := raycluster.NewReconciler(
					ctx,
					mgr.GetClient(),
					mgr.GetFieldIndexer(),
					mgr.GetEventRecorderFor(constants.JobControllerName),
					opts...)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				err = indexer.Setup(ctx, mgr.GetFieldIndexer())
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				err = raycluster.SetupIndexes(ctx, mgr.GetFieldIndexer())
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				err = reconciler.SetupWithManager(mgr)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				err = raycluster.SetupRayClusterWebhook(mgr, opts...)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())

				reconciler, err = workloadrayjob.NewReconciler(
					ctx,
					mgr.GetClient(),
					mgr.GetFieldIndexer(),
					mgr.GetEventRecorderFor(constants.JobControllerName),
					opts...)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				err = workloadrayjob.SetupIndexes(ctx, mgr.GetFieldIndexer())
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				err = reconciler.SetupWithManager(mgr)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				err = workloadrayjob.SetupRayJobWebhook(mgr, opts...)
				gomega.Expect(err).NotTo(gomega.HaveOccurred())
				jobframework.EnableIntegration(workloadrayjob.FrameworkName)

				failedWebhook, err := webhooks.Setup(mgr, nil)
				gomega.Expect(err).ToNot(gomega.HaveOccurred(), "webhook", failedWebhook)

				return nil
			}, jobframework.WithManageJobsWithoutQueueName(true), jobframework.WithManagedJobsNamespaceSelector(util.NewNamespaceSelectorExcluding("unmanaged-ns"))))
		})
		ginkgo.BeforeEach(func() {
			ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "raycluster-")
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		})
		ginkgo.AfterAll(func() {
			fwk.StopManager(ctx)
		})
	})
})
