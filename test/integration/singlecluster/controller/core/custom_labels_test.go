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

package core

import (
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/component-base/metrics/testutil"
	"sigs.k8s.io/controller-runtime/pkg/client"

	config "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/metrics"
	"sigs.k8s.io/kueue/pkg/util/roletracker"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("CustomMetricLabels", ginkgo.Label("controller:clusterqueue", "area:core"), func() {
	var (
		defaultFlavor *kueue.ResourceFlavor
		ns            *corev1.Namespace
	)

	ginkgo.When("CustomMetricLabels and LocalQueueMetrics are enabled", func() {
		var (
			cq *kueue.ClusterQueue
		)

		ginkgo.BeforeEach(func() {
			features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.CustomMetricLabels, true)
			features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.LocalQueueMetrics, true)
			controllersCfg := &config.Configuration{}
			controllersCfg.Metrics.CustomLabels = []config.ControllerMetricsCustomLabel{
				{Name: "team"},
			}
			fwk.StartManager(ctx, cfg, managerAndControllerSetup(controllersCfg))
			defaultFlavor = utiltestingapi.MakeResourceFlavor("default").Obj()
			util.MustCreate(ctx, k8sClient, defaultFlavor)
			ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "custom-labels-")
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			util.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
			gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			util.ExpectObjectToBeDeleted(ctx, k8sClient, defaultFlavor, true)
			fwk.StopManager(ctx)
			metrics.InitMetricVectors(nil)
		})

		ginkgo.It("should include custom label values from CQ and LQ labels in metrics", func() {
			cq = utiltestingapi.MakeClusterQueue("cq-team").
				Label("team", "platform").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas(defaultFlavor.Name).
						Resource(corev1.ResourceCPU, "5").
						Obj(),
				).Obj()
			util.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, cq)

			lq := utiltestingapi.MakeLocalQueue("lq-team", ns.Name).
				Label("team", "platform").
				ClusterQueue(cq.Name).Obj()
			util.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, lq)

			wl := utiltestingapi.MakeWorkload("wl1", ns.Name).
				Queue(kueue.LocalQueueName(lq.Name)).
				Request(corev1.ResourceCPU, "1").Obj()
			util.MustCreate(ctx, k8sClient, wl)

			ginkgo.By("verifying CQ PendingWorkloads metric has custom_team=platform")
			util.ExpectPendingWorkloadsMetric(cq, 1, 0, "platform")

			ginkgo.By("verifying LQ PendingWorkloads metric has custom_team=platform")
			util.ExpectLQPendingWorkloadsMetric(lq, 1, 0, "platform")
		})

		ginkgo.It("should use empty string for missing labels", func() {
			cq = utiltestingapi.MakeClusterQueue("cq-nolabel").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas(defaultFlavor.Name).
						Resource(corev1.ResourceCPU, "5").
						Obj(),
				).Obj()
			util.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, cq)

			lq := utiltestingapi.MakeLocalQueue("lq-nolabel", ns.Name).
				ClusterQueue(cq.Name).Obj()
			util.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, lq)

			wl := utiltestingapi.MakeWorkload("wl-nolabel", ns.Name).
				Queue(kueue.LocalQueueName(lq.Name)).
				Request(corev1.ResourceCPU, "1").Obj()
			util.MustCreate(ctx, k8sClient, wl)

			util.ExpectPendingWorkloadsMetric(cq, 1, 0, "")
		})

		ginkgo.It("should clean up stale series on label value change", func() {
			cq = utiltestingapi.MakeClusterQueue("cq-change").
				Label("team", "alpha").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas(defaultFlavor.Name).
						Resource(corev1.ResourceCPU, "5").
						Obj(),
				).Obj()
			util.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, cq)

			lq := utiltestingapi.MakeLocalQueue("lq-change", ns.Name).
				ClusterQueue(cq.Name).Obj()
			util.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, lq)

			wl := utiltestingapi.MakeWorkload("wl-change", ns.Name).
				Queue(kueue.LocalQueueName(lq.Name)).
				Request(corev1.ResourceCPU, "1").Obj()
			util.MustCreate(ctx, k8sClient, wl)

			ginkgo.By("verifying metric with custom_team=alpha")
			util.ExpectPendingWorkloadsMetric(cq, 1, 0, "alpha")

			ginkgo.By("updating CQ label to team=beta")
			gomega.Eventually(func(g gomega.Gomega) {
				var updatedCQ kueue.ClusterQueue
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cq), &updatedCQ)).To(gomega.Succeed())
				updatedCQ.Labels["team"] = "beta"
				g.Expect(k8sClient.Update(ctx, &updatedCQ)).To(gomega.Succeed())
			}, util.Timeout, util.ShortInterval).Should(gomega.Succeed())

			ginkgo.By("verifying metric with custom_team=beta appears")
			gomega.Eventually(func(g gomega.Gomega) {
				metricBeta := metrics.PendingWorkloads.WithLabelValues("cq-change", metrics.PendingStatusActive, roletracker.RoleStandalone, "beta")
				v, err := testutil.GetGaugeMetricValue(metricBeta)
				g.Expect(err).ToNot(gomega.HaveOccurred())
				g.Expect(v).To(gomega.Equal(float64(1)))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			ginkgo.By("verifying old alpha series is cleaned")
			gomega.Eventually(func(g gomega.Gomega) {
				metricAlpha := metrics.PendingWorkloads.WithLabelValues("cq-change", metrics.PendingStatusActive, roletracker.RoleStandalone, "alpha")
				v, err := testutil.GetGaugeMetricValue(metricAlpha)
				g.Expect(err).ToNot(gomega.HaveOccurred())
				g.Expect(v).To(gomega.Equal(float64(0)))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})
	})

	ginkgo.When("CustomMetricLabels with sourceLabelKey", func() {
		var (
			cq *kueue.ClusterQueue
		)

		ginkgo.BeforeEach(func() {
			features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.CustomMetricLabels, true)
			features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.LocalQueueMetrics, true)
			controllersCfg := &config.Configuration{}
			controllersCfg.Metrics.CustomLabels = []config.ControllerMetricsCustomLabel{
				{Name: "cost_center", SourceLabelKey: "billing/cost-center"},
			}
			fwk.StartManager(ctx, cfg, managerAndControllerSetup(controllersCfg))
			defaultFlavor = utiltestingapi.MakeResourceFlavor("default").Obj()
			util.MustCreate(ctx, k8sClient, defaultFlavor)
			ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "custom-labels-srclabel-")
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			util.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
			gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			util.ExpectObjectToBeDeleted(ctx, k8sClient, defaultFlavor, true)
			fwk.StopManager(ctx)
			metrics.InitMetricVectors(nil)
		})

		ginkgo.It("should read from sourceLabelKey", func() {
			cq = utiltestingapi.MakeClusterQueue("cq-billing").
				Label("billing/cost-center", "cc-123").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas(defaultFlavor.Name).
						Resource(corev1.ResourceCPU, "5").
						Obj(),
				).Obj()
			util.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, cq)

			lq := utiltestingapi.MakeLocalQueue("lq-billing", ns.Name).
				ClusterQueue(cq.Name).Obj()
			util.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, lq)

			wl := utiltestingapi.MakeWorkload("wl-billing", ns.Name).
				Queue(kueue.LocalQueueName(lq.Name)).
				Request(corev1.ResourceCPU, "1").Obj()
			util.MustCreate(ctx, k8sClient, wl)

			util.ExpectPendingWorkloadsMetric(cq, 1, 0, "cc-123")
		})
	})

	ginkgo.When("CustomMetricLabels with sourceAnnotationKey", func() {
		var (
			cq *kueue.ClusterQueue
		)

		ginkgo.BeforeEach(func() {
			features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.CustomMetricLabels, true)
			features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.LocalQueueMetrics, true)
			controllersCfg := &config.Configuration{}
			controllersCfg.Metrics.CustomLabels = []config.ControllerMetricsCustomLabel{
				{Name: "budget", SourceAnnotationKey: "billing.co/budget"},
			}
			fwk.StartManager(ctx, cfg, managerAndControllerSetup(controllersCfg))
			defaultFlavor = utiltestingapi.MakeResourceFlavor("default").Obj()
			util.MustCreate(ctx, k8sClient, defaultFlavor)
			ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "custom-labels-srcannot-")
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			util.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
			gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			util.ExpectObjectToBeDeleted(ctx, k8sClient, defaultFlavor, true)
			fwk.StopManager(ctx)
			metrics.InitMetricVectors(nil)
		})

		ginkgo.It("should read from sourceAnnotationKey", func() {
			cq = utiltestingapi.MakeClusterQueue("cq-annot").
				Annotation("billing.co/budget", "ABC").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas(defaultFlavor.Name).
						Resource(corev1.ResourceCPU, "5").
						Obj(),
				).Obj()
			util.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, cq)

			lq := utiltestingapi.MakeLocalQueue("lq-annot", ns.Name).
				ClusterQueue(cq.Name).Obj()
			util.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, lq)

			wl := utiltestingapi.MakeWorkload("wl-annot", ns.Name).
				Queue(kueue.LocalQueueName(lq.Name)).
				Request(corev1.ResourceCPU, "1").Obj()
			util.MustCreate(ctx, k8sClient, wl)

			util.ExpectPendingWorkloadsMetric(cq, 1, 0, "ABC")
		})
	})

	ginkgo.When("CustomMetricLabels enabled but LocalQueueMetrics disabled", func() {
		var (
			cq *kueue.ClusterQueue
		)

		ginkgo.BeforeEach(func() {
			features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.CustomMetricLabels, true)
			features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.LocalQueueMetrics, false)
			controllersCfg := &config.Configuration{}
			controllersCfg.Metrics.CustomLabels = []config.ControllerMetricsCustomLabel{
				{Name: "team"},
			}
			fwk.StartManager(ctx, cfg, managerAndControllerSetup(controllersCfg))
			defaultFlavor = utiltestingapi.MakeResourceFlavor("default").Obj()
			util.MustCreate(ctx, k8sClient, defaultFlavor)
			ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "custom-labels-nolq-")
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			util.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
			gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			util.ExpectObjectToBeDeleted(ctx, k8sClient, defaultFlavor, true)
			fwk.StopManager(ctx)
			metrics.InitMetricVectors(nil)
		})

		ginkgo.It("should not add custom labels to LQ metrics when LocalQueueMetrics is disabled", func() {
			cq = utiltestingapi.MakeClusterQueue("cq-nolq").
				Label("team", "platform").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas(defaultFlavor.Name).
						Resource(corev1.ResourceCPU, "5").
						Obj(),
				).Obj()
			util.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, cq)

			lq := utiltestingapi.MakeLocalQueue("lq-nolq", ns.Name).
				ClusterQueue(cq.Name).Obj()
			util.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, lq)

			wl := utiltestingapi.MakeWorkload("wl-nolq", ns.Name).
				Queue(kueue.LocalQueueName(lq.Name)).
				Request(corev1.ResourceCPU, "1").Obj()
			util.MustCreate(ctx, k8sClient, wl)

			ginkgo.By("verifying CQ metrics still have custom labels")
			util.ExpectPendingWorkloadsMetric(cq, 1, 0, "platform")

			ginkgo.By("verifying LQ pending workloads metric is not collected")
			gomega.Consistently(func(g gomega.Gomega) {
				metric := metrics.LocalQueuePendingWorkloads.WithLabelValues(lq.Name, ns.Name, metrics.PendingStatusActive, roletracker.RoleStandalone, "platform")
				v, err := testutil.GetGaugeMetricValue(metric)
				g.Expect(err).ToNot(gomega.HaveOccurred())
				g.Expect(v).To(gomega.Equal(float64(0)))
			}, util.ConsistentDuration, util.Interval).Should(gomega.Succeed())
		})
	})

	ginkgo.When("CustomMetricLabels disabled", func() {
		var (
			cq *kueue.ClusterQueue
		)

		ginkgo.BeforeEach(func() {
			features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.CustomMetricLabels, false)
			fwk.StartManager(ctx, cfg, managerAndControllerSetup(nil))
			defaultFlavor = utiltestingapi.MakeResourceFlavor("default").Obj()
			util.MustCreate(ctx, k8sClient, defaultFlavor)
			ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "custom-labels-disabled-")
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			util.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
			gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			util.ExpectObjectToBeDeleted(ctx, k8sClient, defaultFlavor, true)
			fwk.StopManager(ctx)
		})

		ginkgo.It("should not include custom label dimensions in CQ metrics", func() {
			cq = utiltestingapi.MakeClusterQueue("cq-disabled").
				Label("team", "platform").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas(defaultFlavor.Name).
						Resource(corev1.ResourceCPU, "5").
						Obj(),
				).Obj()
			util.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, cq)

			lq := utiltestingapi.MakeLocalQueue("lq-disabled", ns.Name).
				ClusterQueue(cq.Name).Obj()
			util.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, lq)

			wl := utiltestingapi.MakeWorkload("wl-disabled", ns.Name).
				Queue(kueue.LocalQueueName(lq.Name)).
				Request(corev1.ResourceCPU, "1").Obj()
			util.MustCreate(ctx, k8sClient, wl)

			ginkgo.By("verifying CQ metrics work with standard labels (no custom dimensions)")
			util.ExpectPendingWorkloadsMetric(cq, 1, 0)

			ginkgo.By("verifying no custom label names are configured")
			gomega.Expect((*metrics.CustomLabels)(nil).LabelNames()).To(gomega.BeEmpty())
		})
	})

	ginkgo.When("CustomMetricLabels enabled via Configuration", func() {
		var (
			cq *kueue.ClusterQueue
		)

		ginkgo.BeforeEach(func() {
			features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.CustomMetricLabels, true)
			features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.LocalQueueMetrics, true)
			controllersCfg := &config.Configuration{}
			controllersCfg.Metrics.CustomLabels = []config.ControllerMetricsCustomLabel{
				{Name: "team"},
			}
			fwk.StartManager(ctx, cfg, managerAndControllerSetup(controllersCfg, runScheduler))
			defaultFlavor = utiltestingapi.MakeResourceFlavor("default").Obj()
			util.MustCreate(ctx, k8sClient, defaultFlavor)
			ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "custom-labels-config-")
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			util.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
			gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			util.ExpectObjectToBeDeleted(ctx, k8sClient, defaultFlavor, true)
			fwk.StopManager(ctx)
			metrics.InitMetricVectors(nil)
		})

		ginkgo.It("should include custom labels end-to-end from Configuration", func() {
			cq = utiltestingapi.MakeClusterQueue("cq-config").
				Label("team", "infra").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas(defaultFlavor.Name).
						Resource(corev1.ResourceCPU, "5").
						Obj(),
				).Obj()
			util.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, cq)

			lq := utiltestingapi.MakeLocalQueue("lq-config", ns.Name).
				Label("team", "infra").
				ClusterQueue(cq.Name).Obj()
			util.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, lq)

			wl := utiltestingapi.MakeWorkload("wl-config", ns.Name).
				Queue(kueue.LocalQueueName(lq.Name)).
				Request(corev1.ResourceCPU, "1").Obj()
			util.MustCreate(ctx, k8sClient, wl)

			ginkgo.By("verifying workload gets admitted")
			gomega.Eventually(func(g gomega.Gomega) {
				var updatedWl kueue.Workload
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &updatedWl)).To(gomega.Succeed())
				g.Expect(updatedWl.Status.Admission).ToNot(gomega.BeNil())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			ginkgo.By("verifying CQ admitted metric includes custom_team=infra")
			util.ExpectAdmittedWorkloadsTotalMetric(cq, "", 1, "infra")

			ginkgo.By("verifying LQ admitted metric includes custom_team=infra")
			util.ExpectLQAdmittedWorkloadsTotalMetric(lq, "", 1, "infra")
		})
	})

	ginkgo.When("CustomMetricLabels and LocalQueueMetrics enabled with scheduler for LQ metrics", func() {
		var (
			cq *kueue.ClusterQueue
		)

		ginkgo.BeforeEach(func() {
			features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.CustomMetricLabels, true)
			features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.LocalQueueMetrics, true)
			controllersCfg := &config.Configuration{}
			controllersCfg.Metrics.CustomLabels = []config.ControllerMetricsCustomLabel{
				{Name: "team"},
			}
			fwk.StartManager(ctx, cfg, managerAndControllerSetup(controllersCfg, runScheduler))
			defaultFlavor = utiltestingapi.MakeResourceFlavor("default").Obj()
			util.MustCreate(ctx, k8sClient, defaultFlavor)
			ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "custom-labels-lq-")
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			util.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
			gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			util.ExpectObjectToBeDeleted(ctx, k8sClient, defaultFlavor, true)
			fwk.StopManager(ctx)
			metrics.InitMetricVectors(nil)
		})

		ginkgo.It("should include custom labels in LQ admitted and evicted metrics", func() {
			cq = utiltestingapi.MakeClusterQueue("cq-lq-metrics").
				Label("team", "platform").
				Preemption(kueue.ClusterQueuePreemption{
					WithinClusterQueue: kueue.PreemptionPolicyLowerPriority,
				}).
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas(defaultFlavor.Name).
						Resource(corev1.ResourceCPU, "5").
						Obj(),
				).Obj()
			util.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, cq)

			lq := utiltestingapi.MakeLocalQueue("lq-lq-metrics", ns.Name).
				Label("team", "platform").
				ClusterQueue(cq.Name).Obj()
			util.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, lq)

			ginkgo.By("creating a workload that gets admitted")
			wl := utiltestingapi.MakeWorkload("wl-lq-admitted", ns.Name).
				Queue(kueue.LocalQueueName(lq.Name)).
				Priority(0).
				Request(corev1.ResourceCPU, "2").Obj()
			util.MustCreate(ctx, k8sClient, wl)

			util.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, cq.Name, wl)

			ginkgo.By("verifying LQ AdmittedWorkloadsTotal includes custom labels")
			util.ExpectLQAdmittedWorkloadsTotalMetric(lq, "", 1, "platform")

			ginkgo.By("verifying LQ PendingWorkloads includes custom labels")
			util.ExpectLQPendingWorkloadsMetric(lq, 0, 0, "platform")

			ginkgo.By("creating a high-priority workload to preempt the first one")
			highWl := utiltestingapi.MakeWorkload("wl-lq-high", ns.Name).
				Queue(kueue.LocalQueueName(lq.Name)).
				Priority(100).
				Request(corev1.ResourceCPU, "5").Obj()
			util.MustCreate(ctx, k8sClient, highWl)

			ginkgo.By("finishing eviction for the low-priority workload")
			util.FinishEvictionForWorkloads(ctx, k8sClient, wl)

			util.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, cq.Name, highWl)

			ginkgo.By("verifying LQ EvictedWorkloadsTotal includes custom labels")
			util.ExpectLQEvictedWorkloadsTotalMetric(lq, "Preempted", "", "", 1, "platform")
		})
	})

	ginkgo.When("CustomMetricLabels enabled with scheduler for preemption", func() {
		var (
			victimCQ    *kueue.ClusterQueue
			preemptorCQ *kueue.ClusterQueue
		)

		ginkgo.BeforeEach(func() {
			features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.CustomMetricLabels, true)
			features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.LocalQueueMetrics, true)
			controllersCfg := &config.Configuration{}
			controllersCfg.Metrics.CustomLabels = []config.ControllerMetricsCustomLabel{
				{Name: "team"},
			}
			fwk.StartManager(ctx, cfg, managerAndControllerSetup(controllersCfg, runScheduler))
			defaultFlavor = utiltestingapi.MakeResourceFlavor("default").Obj()
			util.MustCreate(ctx, k8sClient, defaultFlavor)
			ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "custom-labels-preempt-")
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			util.ExpectObjectToBeDeleted(ctx, k8sClient, preemptorCQ, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, victimCQ, true)
			gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			util.ExpectObjectToBeDeleted(ctx, k8sClient, defaultFlavor, true)
			fwk.StopManager(ctx)
			metrics.InitMetricVectors(nil)
		})

		ginkgo.It("should include custom labels in PreemptedWorkloadsTotal", func() {
			victimCQ = utiltestingapi.MakeClusterQueue("cq-victim").
				Label("team", "victim-team").
				Cohort("preempt-cohort").
				Preemption(kueue.ClusterQueuePreemption{
					WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
					ReclaimWithinCohort: kueue.PreemptionPolicyAny,
				}).
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas(defaultFlavor.Name).
						Resource(corev1.ResourceCPU, "5").
						Obj(),
				).Obj()
			util.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, victimCQ)

			preemptorCQ = utiltestingapi.MakeClusterQueue("cq-preemptor").
				Label("team", "preemptor-team").
				Cohort("preempt-cohort").
				Preemption(kueue.ClusterQueuePreemption{
					WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
					ReclaimWithinCohort: kueue.PreemptionPolicyAny,
				}).
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas(defaultFlavor.Name).
						Resource(corev1.ResourceCPU, "5").
						Obj(),
				).Obj()
			util.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, preemptorCQ)

			victimLQ := utiltestingapi.MakeLocalQueue("lq-victim", ns.Name).
				ClusterQueue(victimCQ.Name).Obj()
			util.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, victimLQ)

			preemptorLQ := utiltestingapi.MakeLocalQueue("lq-preemptor", ns.Name).
				ClusterQueue(preemptorCQ.Name).Obj()
			util.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, preemptorLQ)

			ginkgo.By("creating a low-priority workload that fills the victim CQ and borrows")
			lowWl := utiltestingapi.MakeWorkload("low-wl", ns.Name).
				Queue(kueue.LocalQueueName(victimLQ.Name)).
				Priority(0).
				Request(corev1.ResourceCPU, "10").Obj()
			util.MustCreate(ctx, k8sClient, lowWl)

			gomega.Eventually(func(g gomega.Gomega) {
				var wl kueue.Workload
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(lowWl), &wl)).To(gomega.Succeed())
				g.Expect(wl.Status.Admission).ToNot(gomega.BeNil())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			ginkgo.By("creating a high-priority workload in the preemptor CQ to trigger preemption")
			highWl := utiltestingapi.MakeWorkload("high-wl", ns.Name).
				Queue(kueue.LocalQueueName(preemptorLQ.Name)).
				Priority(100).
				Request(corev1.ResourceCPU, "5").Obj()
			util.MustCreate(ctx, k8sClient, highWl)

			ginkgo.By("verifying PreemptedWorkloadsTotal includes custom label for the preempting CQ")
			gomega.Eventually(func(g gomega.Gomega) {
				lvs := []string{"cq-preemptor", kueue.InCohortReclamationReason, roletracker.RoleStandalone, "preemptor-team"}
				metric := metrics.PreemptedWorkloadsTotal.WithLabelValues(lvs...)
				v, err := testutil.GetCounterMetricValue(metric)
				g.Expect(err).ToNot(gomega.HaveOccurred())
				g.Expect(int(v)).To(gomega.Equal(1))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})
	})

	ginkgo.When("CustomMetricLabels enabled with fair sharing for Cohort metrics", func() {
		var (
			cq     *kueue.ClusterQueue
			cohort *kueue.Cohort
		)

		ginkgo.BeforeEach(func() {
			features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.CustomMetricLabels, true)
			controllersCfg := &config.Configuration{}
			controllersCfg.FairSharing = &config.FairSharing{}
			controllersCfg.Metrics.CustomLabels = []config.ControllerMetricsCustomLabel{
				{Name: "team"},
			}
			fwk.StartManager(ctx, cfg, managerAndControllerSetup(controllersCfg, runScheduler))
			defaultFlavor = utiltestingapi.MakeResourceFlavor("default").Obj()
			util.MustCreate(ctx, k8sClient, defaultFlavor)
			ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "custom-labels-cohort-")
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			util.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, cohort, true)
			gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			util.ExpectObjectToBeDeleted(ctx, k8sClient, defaultFlavor, true)
			fwk.StopManager(ctx)
			metrics.InitMetricVectors(nil)
		})

		ginkgo.It("should include custom labels in CohortWeightedShare", func() {
			cohort = utiltestingapi.MakeCohort("cohort-labeled").
				Label("team", "data-eng").
				Obj()
			util.MustCreate(ctx, k8sClient, cohort)

			cq = utiltestingapi.MakeClusterQueue("cq-cohort").
				Label("team", "data-eng").
				Cohort("cohort-labeled").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas(defaultFlavor.Name).
						Resource(corev1.ResourceCPU, "5").
						Obj(),
				).Obj()
			util.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, cq)

			lq := utiltestingapi.MakeLocalQueue("lq-cohort", ns.Name).
				ClusterQueue(cq.Name).Obj()
			util.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, lq)

			wl := utiltestingapi.MakeWorkload("wl-cohort", ns.Name).
				Queue(kueue.LocalQueueName(lq.Name)).
				Request(corev1.ResourceCPU, "1").Obj()
			util.MustCreate(ctx, k8sClient, wl)

			gomega.Eventually(func(g gomega.Gomega) {
				var updatedWl kueue.Workload
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &updatedWl)).To(gomega.Succeed())
				g.Expect(updatedWl.Status.Admission).ToNot(gomega.BeNil())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			ginkgo.By("verifying CohortWeightedShare includes custom_team=data-eng")
			gomega.Eventually(func(g gomega.Gomega) {
				lvs := []string{"cohort-labeled", roletracker.RoleStandalone, "data-eng"}
				metric := metrics.CohortWeightedShare.WithLabelValues(lvs...)
				_, err := testutil.GetGaugeMetricValue(metric)
				g.Expect(err).ToNot(gomega.HaveOccurred())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.It("shouldn't clear cohort counter metrics on label changed", func() {
			cohort = utiltestingapi.MakeCohort("cohort-labeled").
				Label("team", "data-eng").
				Obj()
			util.MustCreate(ctx, k8sClient, cohort)

			cq = utiltestingapi.MakeClusterQueue("cq-cohort").
				Label("team", "data-eng").
				Cohort("cohort-labeled").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas(defaultFlavor.Name).
						Resource(corev1.ResourceCPU, "5").
						Obj(),
				).Obj()
			util.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, cq)

			lq := utiltestingapi.MakeLocalQueue("lq-cohort", ns.Name).
				ClusterQueue(cq.Name).Obj()
			util.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, lq)

			wl := utiltestingapi.MakeWorkload("wl-cohort", ns.Name).
				Queue(kueue.LocalQueueName(lq.Name)).
				Request(corev1.ResourceCPU, "1").Obj()
			util.MustCreate(ctx, k8sClient, wl)

			gomega.Eventually(func(g gomega.Gomega) {
				var updatedWl kueue.Workload
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &updatedWl)).To(gomega.Succeed())
				g.Expect(updatedWl.Status.Admission).ToNot(gomega.BeNil())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			ginkgo.By("verifying CohortWeightedShare includes custom_team=data-eng")
			gomega.Eventually(func(g gomega.Gomega) {
				lvs := []string{"cohort-labeled", roletracker.RoleStandalone, "data-eng"}
				metric := metrics.CohortWeightedShare.WithLabelValues(lvs...)
				_, err := testutil.GetGaugeMetricValue(metric)
				g.Expect(err).ToNot(gomega.HaveOccurred())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			util.ExpectCohortSubtreeAdmittedWorkloadsTotalMetric(kueue.CohortReference(cohort.Name), "", 1, "data-eng")

			gomega.Eventually(func(g gomega.Gomega) {
				var updatedCohort kueue.Cohort
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cohort), &updatedCohort)).To(gomega.Succeed())
				updatedCohort.Labels["team"] = "data-test"
				g.Expect(k8sClient.Update(ctx, &updatedCohort)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			util.ExpectCohortSubtreeAdmittedWorkloadsTotalMetric(kueue.CohortReference(cohort.Name), "", 1, "data-eng")
			util.ExpectCohortSubtreeAdmittedWorkloadsTotalMetric(kueue.CohortReference(cohort.Name), "", 0, "data-test")
		})
	})

	ginkgo.When("Empty customLabels (backward compatibility)", func() {
		var (
			cq *kueue.ClusterQueue
		)

		ginkgo.BeforeEach(func() {
			features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.CustomMetricLabels, true)
			// No custom labels configured — metrics should work as before
			fwk.StartManager(ctx, cfg, managerAndControllerSetup(nil))
			defaultFlavor = utiltestingapi.MakeResourceFlavor("default").Obj()
			util.MustCreate(ctx, k8sClient, defaultFlavor)
			ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "custom-labels-empty-")
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			util.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
			gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			util.ExpectObjectToBeDeleted(ctx, k8sClient, defaultFlavor, true)
			fwk.StopManager(ctx)
		})

		ginkgo.It("should produce metrics without extra label dimensions", func() {
			cq = utiltestingapi.MakeClusterQueue("cq-compat").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas(defaultFlavor.Name).
						Resource(corev1.ResourceCPU, "5").
						Obj(),
				).Obj()
			util.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, cq)

			lq := utiltestingapi.MakeLocalQueue("lq-compat", ns.Name).
				ClusterQueue(cq.Name).Obj()
			util.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, lq)

			wl := utiltestingapi.MakeWorkload("wl-compat", ns.Name).
				Queue(kueue.LocalQueueName(lq.Name)).
				Request(corev1.ResourceCPU, "1").Obj()
			util.MustCreate(ctx, k8sClient, wl)

			ginkgo.By("verifying metrics work with standard label set (no custom dimensions)")
			util.ExpectPendingWorkloadsMetric(cq, 1, 0)
		})
	})
})
