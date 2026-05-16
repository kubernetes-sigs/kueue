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

package quotacheckstrategy

import (
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
	utilqueue "sigs.k8s.io/kueue/pkg/util/queue"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("Quota check strategy", ginkgo.Ordered, ginkgo.ContinueOnFailure, ginkgo.Label("feature:quotacheckstrategy"), func() {
	ginkgo.When("quota check strategy and admission fair sharing are enabled", func() {
		var (
			ns            *corev1.Namespace
			defaultFlavor *kueue.ResourceFlavor
			cq            *kueue.ClusterQueue
			lq            *kueue.LocalQueue
		)

		ginkgo.BeforeAll(func() {
			features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.QuotaCheckStrategy, true)
			fwk.StartManager(ctx, cfg, managerAndSchedulerSetup(
				configapi.QuotaCheckIgnoreUndeclared,
				&configapi.AdmissionFairSharing{
					UsageSamplingInterval: metav1.Duration{Duration: 100 * time.Millisecond},
					UsageHalfLifeTime:     metav1.Duration{Duration: time.Hour},
				},
			))
		})

		ginkgo.AfterAll(func() {
			fwk.StopManager(ctx)
		})

		ginkgo.BeforeEach(func() {
			ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "quota-check-strategy-")

			defaultFlavor = utiltestingapi.MakeResourceFlavor("default").Obj()
			util.MustCreate(ctx, k8sClient, defaultFlavor)

			cq = utiltestingapi.MakeClusterQueue("test-cq").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "10").
						Obj(),
				).Obj()
			util.MustCreate(ctx, k8sClient, cq)
			util.ExpectClusterQueuesToBeActive(ctx, k8sClient, cq)

			lq = utiltestingapi.MakeLocalQueue("test-lq", ns.Name).ClusterQueue(cq.Name).Obj()
			util.MustCreate(ctx, k8sClient, lq)
			util.ExpectLocalQueuesToBeActive(ctx, k8sClient, lq)
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			util.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, defaultFlavor, true)
		})

		ginkgo.It("should ignore undeclared resources and not use them to calculate entry penalty", func() {
			ginkgo.By("Creating a workload that requests a declared and an undeclared resource")
			wl := utiltestingapi.MakeWorkload("undeclared-penalty-wl", ns.Name).
				Queue(kueue.LocalQueueName(lq.Name)).
				Request(corev1.ResourceCPU, "2").
				Request("example.com/gpu", "100").
				Obj()
			util.MustCreate(ctx, k8sClient, wl)

			wlKey := types.NamespacedName{Name: wl.Name, Namespace: ns.Name}

			ginkgo.By("Verifying the workload is admitted")
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, wlKey, wl)).To(gomega.Succeed())
				g.Expect(workload.IsAdmitted(wl)).To(gomega.BeTrue())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			ginkgo.By("Verifying the undeclared resource is not in admission resource usage")
			gomega.Expect(wl.Status.Admission).NotTo(gomega.BeNil())
			gomega.Expect(wl.Status.Admission.PodSetAssignments).To(gomega.HaveLen(1))
			resourceUsage := wl.Status.Admission.PodSetAssignments[0].ResourceUsage
			gomega.Expect(resourceUsage).To(gomega.HaveKey(corev1.ResourceCPU))
			gomega.Expect(resourceUsage).NotTo(gomega.HaveKey(corev1.ResourceName("example.com/gpu")))

			ginkgo.By("Verifying the ClusterQueue usage only accounts for declared resources")
			var updatedCQ kueue.ClusterQueue
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cq), &updatedCQ)).To(gomega.Succeed())
				g.Expect(updatedCQ.Status.ReservingWorkloads).To(gomega.Equal(int32(1)))
				g.Expect(updatedCQ.Status.FlavorsReservation).Should(gomega.BeComparableTo([]kueue.FlavorUsage{{
					Name: "default",
					Resources: []kueue.ResourceUsage{
						{
							Name:  corev1.ResourceCPU,
							Total: resource.MustParse("2"),
						},
					},
				}}))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			ginkgo.By("Verifying the entry penalty does not include the undeclared resource")
			lqKey := utilqueue.NewLocalQueueReference(ns.Name, kueue.LocalQueueName(lq.Name))
			gomega.Eventually(func(g gomega.Gomega) {
				penalty := qManager.AfsEntryPenalties.Peek(lqKey)
				g.Expect(penalty).NotTo(gomega.HaveKey(corev1.ResourceName("example.com/gpu")))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})
	})
	ginkgo.When("quota check strategy is set to IgnoreUndeclared and feature gate is disabled", func() {
		var (
			ns            *corev1.Namespace
			defaultFlavor *kueue.ResourceFlavor
			cq            *kueue.ClusterQueue
			lq            *kueue.LocalQueue
		)

		ginkgo.BeforeAll(func() {
			fwk.StartManager(ctx, cfg, managerAndSchedulerSetup(configapi.QuotaCheckIgnoreUndeclared, nil))
		})

		ginkgo.AfterAll(func() {
			fwk.StopManager(ctx)
		})

		ginkgo.BeforeEach(func() {
			ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "quota-check-gate-off-")

			defaultFlavor = utiltestingapi.MakeResourceFlavor("default").Obj()
			util.MustCreate(ctx, k8sClient, defaultFlavor)

			cq = utiltestingapi.MakeClusterQueue("test-cq-gate-off").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("default").
						Resource(corev1.ResourceCPU, "10").
						Obj(),
				).Obj()
			util.MustCreate(ctx, k8sClient, cq)
			util.ExpectClusterQueuesToBeActive(ctx, k8sClient, cq)

			lq = utiltestingapi.MakeLocalQueue("test-lq", ns.Name).ClusterQueue(cq.Name).Obj()
			util.MustCreate(ctx, k8sClient, lq)
			util.ExpectLocalQueuesToBeActive(ctx, k8sClient, lq)
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			util.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, defaultFlavor, true)
		})

		ginkgo.It("should not admit workload with undeclared resources", func() {
			wl := utiltestingapi.MakeWorkload("gate-off-wl", ns.Name).
				Queue(kueue.LocalQueueName(lq.Name)).
				Request(corev1.ResourceCPU, "2").
				Request("example.com/gpu", "100").
				Obj()
			util.MustCreate(ctx, k8sClient, wl)

			util.ExpectPendingWorkloadsMetric(cq, 0, 1)
		})
	})
})
