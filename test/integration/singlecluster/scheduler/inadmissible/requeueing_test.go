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

package inadmissible

import (
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/metrics"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/test/util/behavioral"
)

var _ = ginkgo.Describe("Scheduler", func() {
	var (
		ns             *corev1.Namespace
		onDemandFlavor *kueue.ResourceFlavor
	)

	ginkgo.BeforeEach(func() {
		fwk.StartManager(ctx, cfg, managerAndSchedulerSetup)

		ns = behavioral.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "inadmissible-")
		onDemandFlavor = utiltestingapi.MakeResourceFlavor("on-demand").Obj()
		behavioral.MustCreate(ctx, k8sClient, onDemandFlavor)
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(behavioral.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, onDemandFlavor, true)
		fwk.StopManager(ctx)
	})

	ginkgo.When("Requeueing Inadmissible Workloads", func() {
		var (
			cq     *kueue.ClusterQueue
			cohort *kueue.Cohort
		)

		ginkgo.AfterEach(func() {
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, cohort, true)
		})

		ginkgo.It("Should collapse requeue requests to ClusterQueue", func() {
			metrics.AdmissionAttemptsTotal.Reset()
			cq = utiltestingapi.MakeClusterQueue("cq").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("on-demand").Resource(corev1.ResourceCPU, "1").Obj(),
				).Obj()
			behavioral.MustCreate(ctx, k8sClient, cq)

			queue := utiltestingapi.MakeLocalQueue("queue", ns.Name).ClusterQueue("cq").Obj()
			behavioral.MustCreate(ctx, k8sClient, queue)

			ginkgo.By("create a no-fit workload")
			wl := utiltestingapi.MakeWorkload("wl", ns.Name).Queue(kueue.LocalQueueName(queue.Name)).
				Request(corev1.ResourceCPU, "2").Obj()
			behavioral.MustCreate(ctx, k8sClient, wl)

			ginkgo.By("validate no-fit")
			behavioral.ExpectPendingWorkloadsMetric(cq, 0, 1)
			behavioral.ExpectSuccessfulAdmissionAttempts(0, "==")
			behavioral.ExpectPendingAdmissionAttempts(1, ">=")
			behavioral.ExpectPendingAdmissionAttempts(2, "<=")

			ginkgo.By("trigger 10 requeue notifications")
			metrics.AdmissionAttemptsTotal.Reset()
			for range 10 {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cq), cq)).Should(gomega.Succeed())

					cq.Spec.ResourceGroups[0].Flavors[0].Resources[0].NominalQuota.Add(resource.MustParse("1m"))

					g.Expect(k8sClient.Update(ctx, cq)).Should(gomega.Succeed())
				}, behavioral.Timeout, behavioral.ShortInterval).Should(gomega.Succeed())
			}

			// schedule attempt is proxy for requeue
			ginkgo.By("between 1-5 schedule attempts occur")
			behavioral.ExpectPendingWorkloadsMetric(cq, 0, 1)
			behavioral.ExpectSuccessfulAdmissionAttempts(0, "==")
			behavioral.ExpectPendingAdmissionAttempts(1, ">=")
			behavioral.ExpectPendingAdmissionAttempts(5, "<=")
		})

		ginkgo.It("Should collapse requeue requests to Cohort", func() {
			metrics.AdmissionAttemptsTotal.Reset()
			cq = utiltestingapi.MakeClusterQueue("cq").
				Cohort("cohort").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("on-demand").Resource(corev1.ResourceCPU, "1").Obj(),
				).Obj()
			behavioral.MustCreate(ctx, k8sClient, cq)
			queue := utiltestingapi.MakeLocalQueue("queue", ns.Name).ClusterQueue("cq").Obj()
			behavioral.MustCreate(ctx, k8sClient, queue)

			cohort = utiltestingapi.MakeCohort("cohort").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("on-demand").Resource(corev1.ResourceCPU, "0").Obj(),
				).Obj()
			behavioral.MustCreate(ctx, k8sClient, cohort)

			ginkgo.By("create a no-fit workload")
			wl := utiltestingapi.MakeWorkload("wl", ns.Name).Queue(kueue.LocalQueueName(queue.Name)).
				Request(corev1.ResourceCPU, "2").Obj()
			behavioral.MustCreate(ctx, k8sClient, wl)

			ginkgo.By("validate no-fit")
			behavioral.ExpectPendingWorkloadsMetric(cq, 0, 1)
			behavioral.ExpectSuccessfulAdmissionAttempts(0, "==")
			behavioral.ExpectPendingAdmissionAttempts(1, ">=")
			behavioral.ExpectPendingAdmissionAttempts(2, "<=")

			ginkgo.By("trigger 10 requeue notifications to root Cohort")
			metrics.AdmissionAttemptsTotal.Reset()
			for range 10 {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cohort), cohort)).Should(gomega.Succeed())

					cohort.Spec.ResourceGroups[0].Flavors[0].Resources[0].NominalQuota.Add(resource.MustParse("1m"))

					g.Expect(k8sClient.Update(ctx, cohort)).Should(gomega.Succeed())
				}, behavioral.Timeout, behavioral.ShortInterval).Should(gomega.Succeed())
			}

			// schedule attempt is proxy for requeue
			ginkgo.By("between 1-5 schedule attempts occur")
			behavioral.ExpectPendingWorkloadsMetric(cq, 0, 1)
			behavioral.ExpectSuccessfulAdmissionAttempts(0, "==")
			behavioral.ExpectPendingAdmissionAttempts(1, ">=")
			behavioral.ExpectPendingAdmissionAttempts(5, "<=")
		})
	})
})
