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
	awv1beta2 "github.com/project-codeflare/appwrapper/api/v1beta2"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"

	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/controller/jobs/appwrapper"
	"sigs.k8s.io/kueue/pkg/util/testing"
	testingaw "sigs.k8s.io/kueue/pkg/util/testingjobs/appwrapper"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("AppWrapper Webhook", ginkgo.Ordered, func() {
	var ns *corev1.Namespace
	ginkgo.BeforeAll(func() {
		fwk.StartManager(ctx, cfg, managerSetup(appwrapper.SetupAppWrapperWebhook, jobframework.WithManageJobsWithoutQueueName(false)))
	})
	ginkgo.BeforeEach(func() {
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "aw-")
	})
	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
	})
	ginkgo.AfterAll(func() {
		fwk.StopManager(ctx)
	})

	ginkgo.It("should suspend an AppWrapper when created in unsuspend state", func() {
		appwrapper := testingaw.MakeAppWrapper("aw-with-queue-name", ns.Name).Suspend(false).Queue("default").Obj()
		util.MustCreate(ctx, k8sClient, appwrapper)

		lookupKey := types.NamespacedName{Name: appwrapper.Name, Namespace: appwrapper.Namespace}
		createdAppWrapper := &awv1beta2.AppWrapper{}
		gomega.Eventually(func(g gomega.Gomega) {
			g.Expect(k8sClient.Get(ctx, lookupKey, createdAppWrapper)).Should(gomega.Succeed())
			g.Expect(createdAppWrapper.Spec.Suspend).Should(gomega.BeTrue())
		}, util.Timeout, util.Interval).Should(gomega.Succeed())
	})

	ginkgo.It("the creation doesn't succeed if the queue name is invalid", func() {
		appwrapper := testingaw.MakeAppWrapper("aw-with-invalid-queue", ns.Name).Queue("indexed_job").Obj()
		err := k8sClient.Create(ctx, appwrapper)
		gomega.Expect(err).Should(gomega.HaveOccurred())
		gomega.Expect(err).Should(testing.BeForbiddenError())
	})
})
