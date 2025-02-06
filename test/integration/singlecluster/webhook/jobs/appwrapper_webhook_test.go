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

package jobs

import (
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	awv1beta2 "github.com/project-codeflare/appwrapper/api/v1beta2"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/discovery"

	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/controller/jobs/appwrapper"
	"sigs.k8s.io/kueue/pkg/util/kubeversion"
	"sigs.k8s.io/kueue/pkg/util/testing"
	testingaw "sigs.k8s.io/kueue/pkg/util/testingjobs/appwrapper"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("AppWrapper Webhook With manageJobsWithoutQueueName enabled", ginkgo.Ordered, func() {
	var ns *corev1.Namespace
	ginkgo.BeforeAll(func() {
		discoveryClient, err := discovery.NewDiscoveryClientForConfig(cfg)
		gomega.Expect(err).NotTo(gomega.HaveOccurred())
		serverVersionFetcher = kubeversion.NewServerVersionFetcher(discoveryClient)
		err = serverVersionFetcher.FetchServerVersion()
		gomega.Expect(err).NotTo(gomega.HaveOccurred())

		fwk.StartManager(ctx, cfg, managerSetup(
			appwrapper.SetupAppWrapperWebhook,
			jobframework.WithManageJobsWithoutQueueName(true),
			jobframework.WithManagedJobsNamespaceSelector(util.NewNamespaceSelectorExcluding("unmanaged-ns")),
			jobframework.WithKubeServerVersion(serverVersionFetcher),
		))
		unmanagedNamespace := &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				Name: "unmanaged-ns",
			},
		}
		gomega.Expect(k8sClient.Create(ctx, unmanagedNamespace)).To(gomega.Succeed())
	})
	ginkgo.BeforeEach(func() {
		ns = &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: "aw-",
			},
		}
		gomega.Expect(k8sClient.Create(ctx, ns)).To(gomega.Succeed())
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
	})
	ginkgo.AfterAll(func() {
		fwk.StopManager(ctx)
	})

	ginkgo.It("Should suspend an AppWrapper even no queue name specified", func() {
		appwrapper := testingaw.MakeAppWrapper("aw-without-queue-name-mjwq", ns.Name).Suspend(false).Obj()
		gomega.Expect(k8sClient.Create(ctx, appwrapper)).Should(gomega.Succeed())

		lookupKey := types.NamespacedName{Name: appwrapper.Name, Namespace: appwrapper.Namespace}
		createdAppWrapper := &awv1beta2.AppWrapper{}
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, lookupKey, createdAppWrapper)).Should(gomega.Succeed())
			g.Expect(createdAppWrapper.Spec.Suspend).Should(gomega.BeTrue())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())
	})

	ginkgo.It("Should not suspend an AppWrapper with no queue name specified in an unmanaged namespace", func() {
		appwrapper := testingaw.MakeAppWrapper("aw-without-queue-name-unmanaged-mjwq", "unmanaged-ns").Suspend(false).Obj()
		gomega.Expect(k8sClient.Create(ctx, appwrapper)).Should(gomega.Succeed())

		lookupKey := types.NamespacedName{Name: appwrapper.Name, Namespace: appwrapper.Namespace}
		createdAppWrapper := &awv1beta2.AppWrapper{}
		gomega.Consistently(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, lookupKey, createdAppWrapper)).Should(gomega.Succeed())
			g.Expect(createdAppWrapper.Spec.Suspend).Should(gomega.BeFalse())
		}, util.ConsistentDuration, util.Interval).Should(gomega.Succeed())
	})
})

var _ = ginkgo.Describe("AppWrapper Webhook with manageJobsWithoutQueueName disabled", ginkgo.Ordered, func() {
	var ns *corev1.Namespace
	ginkgo.BeforeAll(func() {
		fwk.StartManager(ctx, cfg, managerSetup(appwrapper.SetupAppWrapperWebhook, jobframework.WithManageJobsWithoutQueueName(false)))
	})
	ginkgo.BeforeEach(func() {
		ns = &corev1.Namespace{
			ObjectMeta: metav1.ObjectMeta{
				GenerateName: "aw-",
			},
		}
		gomega.Expect(k8sClient.Create(ctx, ns)).To(gomega.Succeed())
	})
	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
	})
	ginkgo.AfterAll(func() {
		fwk.StopManager(ctx)
	})

	ginkgo.It("should suspend an AppWrapper when created in unsuspend state", func() {
		appwrapper := testingaw.MakeAppWrapper("aw-with-queue-name", ns.Name).Suspend(false).Queue("default").Obj()
		gomega.Expect(k8sClient.Create(ctx, appwrapper)).Should(gomega.Succeed())

		lookupKey := types.NamespacedName{Name: appwrapper.Name, Namespace: appwrapper.Namespace}
		createdAppWrapper := &awv1beta2.AppWrapper{}
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, lookupKey, createdAppWrapper)).Should(gomega.Succeed())
			g.Expect(createdAppWrapper.Spec.Suspend).Should(gomega.BeTrue())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())
	})

	ginkgo.It("should not suspend an AppWrapper when no queue name specified", func() {
		appwrapper := testingaw.MakeAppWrapper("aw-without-queue-name", ns.Name).Suspend(false).Obj()
		gomega.Expect(k8sClient.Create(ctx, appwrapper)).Should(gomega.Succeed())

		lookupKey := types.NamespacedName{Name: appwrapper.Name, Namespace: appwrapper.Namespace}
		createdAppWrapper := &awv1beta2.AppWrapper{}
		gomega.Consistently(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, lookupKey, createdAppWrapper)).Should(gomega.Succeed())
			g.Expect(createdAppWrapper.Spec.Suspend).Should(gomega.BeFalse())
		}, util.ConsistentDuration, util.Interval).Should(gomega.Succeed())
	})

	ginkgo.It("the creation doesn't succeed if the queue name is invalid", func() {
		appwrapper := testingaw.MakeAppWrapper("aw-with-invalid-queue", ns.Name).Queue("indexed_job").Obj()
		err := k8sClient.Create(ctx, appwrapper)
		gomega.Expect(err).Should(gomega.HaveOccurred())
		gomega.Expect(err).Should(testing.BeForbiddenError())
	})
})
