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
	"k8s.io/apimachinery/pkg/api/resource"
	"sigs.k8s.io/controller-runtime/pkg/client"
	disaggregatedsetv1 "sigs.k8s.io/lws/api/disaggregatedset/v1"

	"sigs.k8s.io/kueue/pkg/controller/jobframework"
	"sigs.k8s.io/kueue/pkg/controller/jobs/disaggregatedset"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	testingds "sigs.k8s.io/kueue/pkg/util/testingjobs/disaggregatedset"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("DisaggregatedSet Webhook", func() {
	var ns *corev1.Namespace

	ginkgo.BeforeEach(func() {
		fwk.StartManager(ctx, cfg, managerSetup(
			disaggregatedset.SetupWebhook,
			jobframework.WithManageJobsWithoutQueueName(false),
		))
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "ds-webhook-")
	})
	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		fwk.StopManager(ctx)
	})

	ginkgo.When("the DisaggregatedSet is managed by Kueue", func() {
		ginkgo.It("should reject changing worker template resources", func() {
			ds := testingds.MakeDisaggregatedSet("ds", ns.Name).
				Queue("user-queue").
				Role("prefill", 1, 1).
				Role("decode", 1, 1).
				Obj()
			util.MustCreate(ctx, k8sClient, ds)

			createdDS := &disaggregatedsetv1.DisaggregatedSet{}
			ginkgo.By("Changing worker template resources is rejected by the webhook", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(ds), createdDS)).To(gomega.Succeed())
					createdDS.Spec.Roles[0].Spec.LeaderWorkerTemplate.WorkerTemplate.Spec.Containers[0].Resources.Requests = corev1.ResourceList{
						corev1.ResourceCPU: resource.MustParse("999m"),
					}
					g.Expect(k8sClient.Update(ctx, createdDS)).To(gomega.SatisfyAll(
						utiltesting.BeForbiddenError(),
					))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("should allow changing replicas", func() {
			ds := testingds.MakeDisaggregatedSet("ds", ns.Name).
				Queue("user-queue").
				Role("prefill", 1, 1).
				Role("decode", 1, 1).
				Obj()
			util.MustCreate(ctx, k8sClient, ds)

			createdDS := &disaggregatedsetv1.DisaggregatedSet{}
			ginkgo.By("Increasing replicas is accepted", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(ds), createdDS)).To(gomega.Succeed())
					newReplicas := int32(3)
					createdDS.Spec.Roles[0].Spec.Replicas = &newReplicas
					createdDS.Spec.Roles[1].Spec.Replicas = &newReplicas
					g.Expect(k8sClient.Update(ctx, createdDS)).To(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})
	})

	ginkgo.When("the DisaggregatedSet is not managed by Kueue", func() {
		ginkgo.It("should allow changing worker template resources", func() {
			ds := testingds.MakeDisaggregatedSet("ds", ns.Name).
				Role("prefill", 1, 1).
				Role("decode", 1, 1).
				Obj()
			util.MustCreate(ctx, k8sClient, ds)

			createdDS := &disaggregatedsetv1.DisaggregatedSet{}
			ginkgo.By("Changing worker template resources is accepted", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(ds), createdDS)).To(gomega.Succeed())
					createdDS.Spec.Roles[0].Spec.LeaderWorkerTemplate.WorkerTemplate.Spec.Containers[0].Resources.Requests = corev1.ResourceList{
						corev1.ResourceCPU: resource.MustParse("999m"),
					}
					g.Expect(k8sClient.Update(ctx, createdDS)).To(gomega.Succeed())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})
	})
})
