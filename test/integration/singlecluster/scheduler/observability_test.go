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

package scheduler

import (
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/component-base/metrics/testutil"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
	kueuemetrics "sigs.k8s.io/kueue/pkg/metrics"
	"sigs.k8s.io/kueue/pkg/util/roletracker"
	testingmetrics "sigs.k8s.io/kueue/pkg/util/testing/metrics"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("Workload Unadmitted Observability Status and Metrics", func() {
	var (
		ns                *corev1.Namespace
		onDemandFlavor    *kueue.ResourceFlavor
		spotTaintedFlavor *kueue.ResourceFlavor
		cqs               []*kueue.ClusterQueue
	)

	ginkgo.BeforeEach(func() {
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.UnadmittedWorkloadsExplicitStatus, true)
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.UnadmittedWorkloadsObservability, true)

		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "obs-")

		onDemandFlavor = utiltestingapi.MakeResourceFlavor("on-demand").Obj()
		util.MustCreate(ctx, k8sClient, onDemandFlavor)

		spotTaintedFlavor = utiltestingapi.MakeResourceFlavor("spot-tainted").
			Taint(corev1.Taint{
				Key:    "key",
				Value:  "val",
				Effect: corev1.TaintEffectNoSchedule,
			}).Obj()
		util.MustCreate(ctx, k8sClient, spotTaintedFlavor)
	})

	ginkgo.AfterEach(func() {
		ginkgo.By("Delete namespace first to delete all workloads and release their finalizers / active quota holds", func() {
			gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		})

		for _, cq := range cqs {
			util.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
		}
		cqs = nil

		util.ExpectObjectToBeDeleted(ctx, k8sClient, onDemandFlavor, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, spotTaintedFlavor, true)
	})

	ginkgo.Context("When evaluating multi-flavor resource assignments", func() {
		ginkgo.It("Should report WaitingForQuota when one flavor has insufficient quota and another has taint mismatch", func() {
			cq := utiltestingapi.MakeClusterQueue("waiting-for-quota-cq").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("spot-tainted").Resource(corev1.ResourceCPU, "20").Obj(),
					*utiltestingapi.MakeFlavorQuotas("on-demand").Resource(corev1.ResourceCPU, "15").Obj(),
				).Obj()
			util.MustCreate(ctx, k8sClient, cq)
			cqs = append(cqs, cq)

			lq := utiltestingapi.MakeLocalQueue("waiting-for-quota-q", ns.Name).ClusterQueue(cq.Name).Obj()
			util.MustCreate(ctx, k8sClient, lq)

			ginkgo.By("Submit a pre-existing job to consume 10 CPUs on on-demand flavor (it fits immediately)", func() {
				existingJob := utiltestingapi.MakeWorkload("existing-job", ns.Name).
					Queue(kueue.LocalQueueName(lq.Name)).
					Request(corev1.ResourceCPU, "10").Obj()
				util.MustCreate(ctx, k8sClient, existingJob)
				util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, existingJob)
			})

			var newJob *kueue.Workload
			ginkgo.By("Submit a new job requesting 10 CPUs (fails spot-tainted due to taint mismatch, and on-demand due to remaining quota = 5)", func() {
				newJob = utiltestingapi.MakeWorkload("new-job", ns.Name).
					Queue(kueue.LocalQueueName(lq.Name)).
					Request(corev1.ResourceCPU, "10").Obj()
				util.MustCreate(ctx, k8sClient, newJob)
			})

			ginkgo.By("Verify the new job is left pending with Reason = WaitingForQuota and metrics are reported", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					var wl kueue.Workload
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(newJob), &wl)).To(gomega.Succeed())
					cond := apimeta.FindStatusCondition(wl.Status.Conditions, kueue.WorkloadQuotaReserved)
					g.Expect(cond).NotTo(gomega.BeNil())
					g.Expect(cond.Status).To(gomega.Equal(metav1.ConditionFalse))
					g.Expect(cond.Reason).To(gomega.Equal(kueue.WorkloadQuotaReservedReasonWaitingForQuota))

					metric := kueuemetrics.UnadmittedWorkloads.WithLabelValues(
						cq.Name,
						kueue.WorkloadAdmittedReasonNoReservation,
						kueue.WorkloadQuotaReservedReasonWaitingForQuota,
						roletracker.RoleStandalone,
					)
					v, err := testutil.GetGaugeMetricValue(metric)
					g.Expect(err).NotTo(gomega.HaveOccurred())
					g.Expect(v).To(gomega.Equal(float64(1)))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("Should report ExceedsMaxQuota when flavors fail due to taint mismatch and exceeding capacity", func() {
			cq := utiltestingapi.MakeClusterQueue("misconfigured-cq").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("spot-tainted").Resource(corev1.ResourceCPU, "20").Obj(),
					*utiltestingapi.MakeFlavorQuotas("on-demand").Resource(corev1.ResourceCPU, "5").Obj(),
				).Obj()
			util.MustCreate(ctx, k8sClient, cq)
			cqs = append(cqs, cq)

			lq := utiltestingapi.MakeLocalQueue("misconfigured-q", ns.Name).ClusterQueue(cq.Name).Obj()
			util.MustCreate(ctx, k8sClient, lq)

			var newJob *kueue.Workload
			ginkgo.By("Submit a new job requesting 10 CPUs (fails spot-tainted due to taint mismatch, and on-demand due to exceeds max capacity limits 5)", func() {
				newJob = utiltestingapi.MakeWorkload("new-job", ns.Name).
					Queue(kueue.LocalQueueName(lq.Name)).
					Request(corev1.ResourceCPU, "10").Obj()
				util.MustCreate(ctx, k8sClient, newJob)
			})

			ginkgo.By("Verify the job is left pending with Reason = ExceedsMaxQuota and metrics are reported", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					var wl kueue.Workload
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(newJob), &wl)).To(gomega.Succeed())
					cond := apimeta.FindStatusCondition(wl.Status.Conditions, kueue.WorkloadQuotaReserved)
					g.Expect(cond).NotTo(gomega.BeNil())
					g.Expect(cond.Status).To(gomega.Equal(metav1.ConditionFalse))
					g.Expect(cond.Reason).To(gomega.Equal(kueue.WorkloadQuotaReservedReasonExceedsMaxQuota))

					metric := kueuemetrics.UnadmittedWorkloads.WithLabelValues(
						cq.Name,
						kueue.WorkloadAdmittedReasonNoReservation,
						kueue.WorkloadQuotaReservedReasonExceedsMaxQuota,
						roletracker.RoleStandalone,
					)
					v, err := testutil.GetGaugeMetricValue(metric)
					g.Expect(err).NotTo(gomega.HaveOccurred())
					g.Expect(v).To(gomega.Equal(float64(1)))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})
	})

	ginkgo.When("Tracking unadmitted workload metrics", func() {
		ginkgo.It("Should correctly report and prune unadmitted workload metrics", func() {
			cq := utiltestingapi.MakeClusterQueue("metrics-cq").
				QueueingStrategy(kueue.StrictFIFO).
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("on-demand").Resource(corev1.ResourceCPU, "1").Obj(),
				).Obj()
			util.MustCreate(ctx, k8sClient, cq)
			cqs = append(cqs, cq)

			lq := utiltestingapi.MakeLocalQueue("metrics-q", ns.Name).ClusterQueue(cq.Name).Obj()
			util.MustCreate(ctx, k8sClient, lq)

			ginkgo.By("Wait for CQ and LQ status conditions to be synchronized", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					var createdCQ kueue.ClusterQueue
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cq), &createdCQ)).To(gomega.Succeed())
					cond := apimeta.FindStatusCondition(createdCQ.Status.Conditions, kueue.ClusterQueueActive)
					g.Expect(cond).NotTo(gomega.BeNil())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())

				gomega.Eventually(func(g gomega.Gomega) {
					var createdLQ kueue.LocalQueue
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(lq), &createdLQ)).To(gomega.Succeed())
					cond := apimeta.FindStatusCondition(createdLQ.Status.Conditions, kueue.LocalQueueActive)
					g.Expect(cond).NotTo(gomega.BeNil())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Submit existing-job to consume 1 CPU -> will be admitted", func() {
				existingJob := utiltestingapi.MakeWorkload("existing-job-metrics", ns.Name).
					Queue(kueue.LocalQueueName(lq.Name)).
					Request(corev1.ResourceCPU, "1").Obj()
				util.MustCreate(ctx, k8sClient, existingJob)
				util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, existingJob)
			})

			var pendingJob *kueue.Workload
			ginkgo.By("Submit a pending job (will stay pending with WaitingForQuota status)", func() {
				pendingJob = utiltestingapi.MakeWorkload("pending-job-metrics", ns.Name).
					Queue(kueue.LocalQueueName(lq.Name)).
					Request(corev1.ResourceCPU, "1").Obj()
				util.MustCreate(ctx, k8sClient, pendingJob)
			})

			ginkgo.By("Wait for pending-job to reach WaitingForQuota status", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					var wl kueue.Workload
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(pendingJob), &wl)).To(gomega.Succeed())
					cond := apimeta.FindStatusCondition(wl.Status.Conditions, kueue.WorkloadQuotaReserved)
					g.Expect(cond).NotTo(gomega.BeNil())
					g.Expect(cond.Reason).To(gomega.Equal(kueue.WorkloadQuotaReservedReasonWaitingForQuota))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Verify the metrics are reported", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					metric := kueuemetrics.UnadmittedWorkloads.WithLabelValues(
						cq.Name,
						kueue.WorkloadAdmittedReasonNoReservation,
						kueue.WorkloadQuotaReservedReasonWaitingForQuota,
						roletracker.RoleStandalone,
					)
					v, err := testutil.GetGaugeMetricValue(metric)
					g.Expect(err).NotTo(gomega.HaveOccurred())
					g.Expect(v).To(gomega.Equal(float64(1)))

					metricLQ := kueuemetrics.LocalQueueUnadmittedWorkloads.WithLabelValues(
						lq.Name,
						ns.Name,
						cq.Name,
						kueue.WorkloadAdmittedReasonNoReservation,
						kueue.WorkloadQuotaReservedReasonWaitingForQuota,
						roletracker.RoleStandalone,
					)
					vLQ, errLQ := testutil.GetGaugeMetricValue(metricLQ)
					g.Expect(errLQ).NotTo(gomega.HaveOccurred())
					g.Expect(vLQ).To(gomega.Equal(float64(1)))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Delete the pending job", func() {
				gomega.Expect(k8sClient.Delete(ctx, pendingJob)).To(gomega.Succeed())
			})

			ginkgo.By("Verify the metrics are cleared", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(testingmetrics.CollectFilteredGaugeVec(kueuemetrics.UnadmittedWorkloads, map[string]string{
						"cluster_queue":    cq.Name,
						"reason":           kueue.WorkloadAdmittedReasonNoReservation,
						"underlying_cause": kueue.WorkloadQuotaReservedReasonWaitingForQuota,
					})).To(gomega.BeEmpty())

					g.Expect(testingmetrics.CollectFilteredGaugeVec(kueuemetrics.LocalQueueUnadmittedWorkloads, map[string]string{
						"name":             lq.Name,
						"namespace":        ns.Name,
						"cluster_queue":    cq.Name,
						"reason":           kueue.WorkloadAdmittedReasonNoReservation,
						"underlying_cause": kueue.WorkloadQuotaReservedReasonWaitingForQuota,
					})).To(gomega.BeEmpty())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})
	})

	ginkgo.Context("with UnadmittedWorkloadsExplicitStatus disabled", func() {
		ginkgo.BeforeEach(func() {
			features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.UnadmittedWorkloadsExplicitStatus, false)
		})

		ginkgo.It("Should report metrics using fallback logic when status is not explicitly initialized", func() {
			cq := utiltestingapi.MakeClusterQueue("metrics-disabled-explicit-cq").
				QueueingStrategy(kueue.StrictFIFO).
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas("on-demand").Resource(corev1.ResourceCPU, "1").Obj(),
				).Obj()
			util.MustCreate(ctx, k8sClient, cq)
			cqs = append(cqs, cq)

			lq := utiltestingapi.MakeLocalQueue("metrics-disabled-explicit-q", ns.Name).ClusterQueue(cq.Name).Obj()
			util.MustCreate(ctx, k8sClient, lq)

			ginkgo.By("Wait for CQ and LQ status conditions to be synchronized", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					var createdCQ kueue.ClusterQueue
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cq), &createdCQ)).To(gomega.Succeed())
					cond := apimeta.FindStatusCondition(createdCQ.Status.Conditions, kueue.ClusterQueueActive)
					g.Expect(cond).NotTo(gomega.BeNil())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Submit existing-job to consume 1 CPU -> will be admitted", func() {
				existingJob := utiltestingapi.MakeWorkload("existing-job-metrics-disabled-explicit", ns.Name).
					Queue(kueue.LocalQueueName(lq.Name)).
					Request(corev1.ResourceCPU, "1").Obj()
				util.MustCreate(ctx, k8sClient, existingJob)
				util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, existingJob)
			})

			var pendingJob *kueue.Workload
			ginkgo.By("Submit a pending job (will stay pending)", func() {
				pendingJob = utiltestingapi.MakeWorkload("pending-job-metrics-disabled-explicit", ns.Name).
					Queue(kueue.LocalQueueName(lq.Name)).
					Request(corev1.ResourceCPU, "1").Obj()
				util.MustCreate(ctx, k8sClient, pendingJob)
			})

			ginkgo.By("Wait for pending-job to reach WaitingForQuota status (set by scheduler)", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					var wl kueue.Workload
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(pendingJob), &wl)).To(gomega.Succeed())
					cond := apimeta.FindStatusCondition(wl.Status.Conditions, kueue.WorkloadQuotaReserved)
					g.Expect(cond).NotTo(gomega.BeNil())
					g.Expect(cond.Reason).To(gomega.Equal(kueue.WorkloadQuotaReservedReasonWaitingForQuota))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Verify the metrics are reported correctly", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					metric := kueuemetrics.UnadmittedWorkloads.WithLabelValues(
						cq.Name,
						kueue.WorkloadAdmittedReasonNoReservation,
						kueue.WorkloadQuotaReservedReasonWaitingForQuota,
						roletracker.RoleStandalone,
					)
					v, err := testutil.GetGaugeMetricValue(metric)
					g.Expect(err).NotTo(gomega.HaveOccurred())
					g.Expect(v).To(gomega.Equal(float64(1)))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("Delete the pending job", func() {
				gomega.Expect(k8sClient.Delete(ctx, pendingJob)).To(gomega.Succeed())
			})

			ginkgo.By("Verify the metrics are cleared", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(testingmetrics.CollectFilteredGaugeVec(kueuemetrics.UnadmittedWorkloads, map[string]string{
						"cluster_queue":    cq.Name,
						"reason":           kueue.WorkloadAdmittedReasonNoReservation,
						"underlying_cause": kueue.WorkloadQuotaReservedReasonWaitingForQuota,
					})).To(gomega.BeEmpty())
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})
	})
})
