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
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"
	leaderworkersetv1 "sigs.k8s.io/lws/api/leaderworkerset/v1"

	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/controller/jobs/leaderworkerset"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	testingleaderworkerset "sigs.k8s.io/kueue/pkg/util/testingjobs/leaderworkerset"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("LeaderWorkerSet Webhook", func() {
	var ns *corev1.Namespace

	ginkgo.BeforeEach(func() {
		fwk.StartManager(ctx, cfg, managerSetup(
			leaderworkerset.SetupWebhook,
			jobframework.WithManageJobsWithoutQueueName(false),
		))
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "lws-webhook-")
	})
	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		fwk.StopManager(ctx)
	})

	ginkgo.When("the LeaderWorkerSet is managed by Kueue", func() {
		ginkgo.It("should reject increasing the leaderWorkerTemplate size", func() {
			lws := testingleaderworkerset.MakeLeaderWorkerSet("lws", ns.Name).
				Queue("user-queue").
				Size(2).
				RolloutStrategy(leaderworkersetv1.RollingUpdateStrategyType).
				Obj()
			util.MustCreate(ctx, k8sClient, lws)

			createdLws := &leaderworkersetv1.LeaderWorkerSet{}
			ginkgo.By("Increasing the size is rejected by the webhook", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(lws), createdLws)).To(gomega.Succeed())
					createdLws.Spec.LeaderWorkerTemplate.Size = ptr.To[int32](10)
					g.Expect(k8sClient.Update(ctx, createdLws)).To(utiltesting.BeForbiddenError())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("should allow changing replicas", func() {
			lws := testingleaderworkerset.MakeLeaderWorkerSet("lws", ns.Name).
				Queue("user-queue").
				Replicas(1).
				Size(2).
				RolloutStrategy(leaderworkersetv1.RollingUpdateStrategyType).
				Obj()
			util.MustCreate(ctx, k8sClient, lws)

			createdLws := &leaderworkersetv1.LeaderWorkerSet{}
			ginkgo.By("Increasing replicas is accepted", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(lws), createdLws)).To(gomega.Succeed())
					createdLws.Spec.Replicas = ptr.To[int32](3)
					g.Expect(k8sClient.Update(ctx, createdLws)).To(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})
	})

	ginkgo.When("the LeaderWorkerSet is not managed by Kueue", func() {
		ginkgo.It("should allow increasing the leaderWorkerTemplate size", func() {
			lws := testingleaderworkerset.MakeLeaderWorkerSet("lws", ns.Name).
				Size(2).
				RolloutStrategy(leaderworkersetv1.RollingUpdateStrategyType).
				Obj()
			util.MustCreate(ctx, k8sClient, lws)

			createdLws := &leaderworkersetv1.LeaderWorkerSet{}
			ginkgo.By("Increasing the size is accepted", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(lws), createdLws)).To(gomega.Succeed())
					createdLws.Spec.LeaderWorkerTemplate.Size = ptr.To[int32](10)
					g.Expect(k8sClient.Update(ctx, createdLws)).To(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})
	})
})
