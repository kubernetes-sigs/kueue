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
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	config "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	qcache "sigs.k8s.io/kueue/pkg/cache/queue"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/metrics"
	"sigs.k8s.io/kueue/pkg/util/roletracker"
	testingmetrics "sigs.k8s.io/kueue/pkg/util/testing/metrics"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	workload "sigs.k8s.io/kueue/pkg/workload"
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
				{Name: "team_cq", SourceLabelKey: "team", SourceKind: ptr.To(config.SourceKindClusterQueue)},
				{Name: "team_lq", SourceLabelKey: "team", SourceKind: ptr.To(config.SourceKindLocalQueue)},
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
			util.ExpectClusterQueueStatusMetric(cq, metrics.CQStatusActive, "alpha")
			gomega.Eventually(func() int {
				return len(testingmetrics.CollectFilteredGaugeVec(metrics.ClusterQueueResourceNominalQuota, map[string]string{
					"cluster_queue":  cq.Name,
					"custom_team_cq": "alpha",
				}))
			}, util.Timeout, util.Interval).Should(gomega.Equal(1))

			ginkgo.By("updating CQ label to team=beta")
			gomega.Eventually(func(g gomega.Gomega) {
				var updatedCQ kueue.ClusterQueue
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cq), &updatedCQ)).To(gomega.Succeed())
				updatedCQ.Labels["team"] = "beta"
				g.Expect(k8sClient.Update(ctx, &updatedCQ)).To(gomega.Succeed())
			}, util.Timeout, util.ShortInterval).Should(gomega.Succeed())

			ginkgo.By("verifying metric with custom_team=beta appears")
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(testingmetrics.CollectFilteredGaugeVec(metrics.PendingWorkloads, map[string]string{
					"cluster_queue":  cq.Name,
					"custom_team_cq": "beta",
				})).To(gomega.HaveLen(2))
				g.Expect(testingmetrics.CollectFilteredGaugeVec(metrics.ClusterQueueResourceNominalQuota, map[string]string{
					"cluster_queue":  cq.Name,
					"custom_team_cq": "beta",
				})).To(gomega.HaveLen(1))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
			util.ExpectClusterQueueStatusMetric(cq, metrics.CQStatusActive, "beta")

			ginkgo.By("verifying old alpha series is cleaned")
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(testingmetrics.CollectFilteredGaugeVec(metrics.PendingWorkloads, map[string]string{
					"cluster_queue":  cq.Name,
					"custom_team_cq": "alpha",
				})).To(gomega.BeEmpty())
				g.Expect(testingmetrics.CollectFilteredGaugeVec(metrics.ClusterQueueResourceNominalQuota, map[string]string{
					"cluster_queue":  cq.Name,
					"custom_team_cq": "alpha",
				})).To(gomega.BeEmpty())
				g.Expect(testingmetrics.CollectFilteredGaugeVec(metrics.ClusterQueueByStatus, map[string]string{
					"cluster_queue":  cq.Name,
					"custom_team_cq": "alpha",
				})).To(gomega.BeEmpty())
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
				{Name: "cost_center", SourceLabelKey: "billing/cost-center", SourceKind: ptr.To(config.SourceKindClusterQueue)},
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
				{Name: "budget", SourceAnnotationKey: "billing.co/budget", SourceKind: ptr.To(config.SourceKindClusterQueue)},
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
				{Name: "team_cq", SourceLabelKey: "team", SourceKind: ptr.To(config.SourceKindClusterQueue)},
				{Name: "team_lq", SourceLabelKey: "team", SourceKind: ptr.To(config.SourceKindLocalQueue)},
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
			}, util.ConsistentDuration, util.ShortInterval).Should(gomega.Succeed())
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
			gomega.Expect((*metrics.CustomLabels)(nil).LabelNames(config.SourceKindClusterQueue)).To(gomega.BeEmpty())
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
				{Name: "team_cq", SourceLabelKey: "team", SourceKind: ptr.To(config.SourceKindClusterQueue)},
				{Name: "team_lq", SourceLabelKey: "team", SourceKind: ptr.To(config.SourceKindLocalQueue)},
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
				{Name: "team_cq", SourceLabelKey: "team", SourceKind: ptr.To(config.SourceKindClusterQueue)},
				{Name: "team_lq", SourceLabelKey: "team", SourceKind: ptr.To(config.SourceKindLocalQueue)},
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

		ginkgo.It("should resync LQ gauge metrics on label value change", func() {
			cq = utiltestingapi.MakeClusterQueue("cq-lq-change").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas(defaultFlavor.Name).
						Resource(corev1.ResourceCPU, "5").
						Obj(),
				).Obj()
			util.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, cq)

			lq := utiltestingapi.MakeLocalQueue("lq-change", ns.Name).
				Label("team", "alpha").
				ClusterQueue(cq.Name).Obj()
			util.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, lq)

			wl := utiltestingapi.MakeWorkload("wl-lq-change", ns.Name).
				Queue(kueue.LocalQueueName(lq.Name)).
				Request(corev1.ResourceCPU, "1").Obj()
			util.MustCreate(ctx, k8sClient, wl)
			util.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, cq.Name, wl)

			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(testingmetrics.CollectFilteredGaugeVec(metrics.LocalQueuePendingWorkloads, map[string]string{
					"name":           lq.Name,
					"namespace":      lq.Namespace,
					"custom_team_lq": "alpha",
				})).To(gomega.HaveLen(2))
				g.Expect(testingmetrics.CollectFilteredGaugeVec(metrics.LocalQueueByStatus, map[string]string{
					"name":           lq.Name,
					"namespace":      lq.Namespace,
					"custom_team_lq": "alpha",
				})).To(gomega.HaveLen(len(metrics.ConditionStatusValues)))
				g.Expect(testingmetrics.CollectFilteredGaugeVec(metrics.LocalQueueResourceReservations, map[string]string{
					"name":           lq.Name,
					"namespace":      lq.Namespace,
					"custom_team_lq": "alpha",
				})).To(gomega.HaveLen(1))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			gomega.Eventually(func(g gomega.Gomega) {
				var updatedLq kueue.LocalQueue
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(lq), &updatedLq)).To(gomega.Succeed())
				updatedLq.Labels["team"] = "beta"
				g.Expect(k8sClient.Update(ctx, &updatedLq)).To(gomega.Succeed())
			}, util.Timeout, util.ShortInterval).Should(gomega.Succeed())

			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(testingmetrics.CollectFilteredGaugeVec(metrics.LocalQueuePendingWorkloads, map[string]string{
					"name":           lq.Name,
					"namespace":      lq.Namespace,
					"custom_team_lq": "beta",
				})).To(gomega.HaveLen(2))
				g.Expect(testingmetrics.CollectFilteredGaugeVec(metrics.LocalQueueByStatus, map[string]string{
					"name":           lq.Name,
					"namespace":      lq.Namespace,
					"custom_team_lq": "beta",
				})).To(gomega.HaveLen(len(metrics.ConditionStatusValues)))
				g.Expect(testingmetrics.CollectFilteredGaugeVec(metrics.LocalQueueResourceReservations, map[string]string{
					"name":           lq.Name,
					"namespace":      lq.Namespace,
					"custom_team_lq": "beta",
				})).To(gomega.HaveLen(1))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(testingmetrics.CollectFilteredGaugeVec(metrics.LocalQueuePendingWorkloads, map[string]string{
					"name":           lq.Name,
					"namespace":      lq.Namespace,
					"custom_team_lq": "alpha",
				})).To(gomega.BeEmpty())
				g.Expect(testingmetrics.CollectFilteredGaugeVec(metrics.LocalQueueByStatus, map[string]string{
					"name":           lq.Name,
					"namespace":      lq.Namespace,
					"custom_team_lq": "alpha",
				})).To(gomega.BeEmpty())
				g.Expect(testingmetrics.CollectFilteredGaugeVec(metrics.LocalQueueResourceReservations, map[string]string{
					"name":           lq.Name,
					"namespace":      lq.Namespace,
					"custom_team_lq": "alpha",
				})).To(gomega.BeEmpty())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
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
				{Name: "team", SourceKind: ptr.To(config.SourceKindClusterQueue)},
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
				{Name: "team_cq", SourceLabelKey: "team", SourceKind: ptr.To(config.SourceKindClusterQueue)},
				{Name: "team_cohort", SourceLabelKey: "team", SourceKind: ptr.To(config.SourceKindCohort)},
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

			ginkgo.By("verifying CohortInfo includes custom_team=data-eng")
			util.ExpectCohortInfoMetric(cohort.Name, "", cohort.Name, 1, "data-eng")

			ginkgo.By("verifying CohortSubtreeQuota includes custom_team=data-eng")
			util.ExpectCohortSubtreeQuotaGaugeMetric(cohort.Name, defaultFlavor.Name, corev1.ResourceCPU.String(), 5, "data-eng")

			ginkgo.By("verifying CohortSubtreeResourceReservations includes custom_team=data-eng")
			util.ExpectCohortSubtreeResourceReservationsGaugeMetric(cohort.Name, defaultFlavor.Name, corev1.ResourceCPU.String(), 1, "data-eng")

			ginkgo.By("verifying CohortWeightedShare includes custom_team=data-eng")
			gomega.Eventually(func(g gomega.Gomega) {
				lvs := []string{"cohort-labeled", roletracker.RoleStandalone, "data-eng"}
				metric := metrics.CohortWeightedShare.WithLabelValues(lvs...)
				_, err := testutil.GetGaugeMetricValue(metric)
				g.Expect(err).ToNot(gomega.HaveOccurred())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.It("should resync cohort gauge metrics on label value change", func() {
			cohort = utiltestingapi.MakeCohort("cohort-labeled").
				Label("team", "data-eng").
				Obj()
			util.MustCreate(ctx, k8sClient, cohort)

			cq = utiltestingapi.MakeClusterQueue("cq-cohort-change").
				Cohort("cohort-labeled").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas(defaultFlavor.Name).
						Resource(corev1.ResourceCPU, "5").
						Obj(),
				).Obj()
			util.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, cq)

			lq := utiltestingapi.MakeLocalQueue("lq-cohort-change", ns.Name).
				ClusterQueue(cq.Name).Obj()
			util.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, lq)

			wl := utiltestingapi.MakeWorkload("wl-cohort-change", ns.Name).
				Queue(kueue.LocalQueueName(lq.Name)).
				Request(corev1.ResourceCPU, "1").Obj()
			util.MustCreate(ctx, k8sClient, wl)

			gomega.Eventually(func(g gomega.Gomega) {
				var updatedWl kueue.Workload
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &updatedWl)).To(gomega.Succeed())
				g.Expect(updatedWl.Status.Admission).ToNot(gomega.BeNil())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(testingmetrics.CollectFilteredGaugeVec(metrics.CohortSubtreeQuota, map[string]string{
					"cohort":             cohort.Name,
					"custom_team_cohort": "data-eng",
				})).To(gomega.HaveLen(1))
				g.Expect(testingmetrics.CollectFilteredGaugeVec(metrics.CohortSubtreeResourceReservations, map[string]string{
					"cohort":             cohort.Name,
					"custom_team_cohort": "data-eng",
				})).To(gomega.HaveLen(1))
				g.Expect(testingmetrics.CollectFilteredGaugeVec(metrics.CohortWeightedShare, map[string]string{
					"cohort":             cohort.Name,
					"custom_team_cohort": "data-eng",
				})).To(gomega.HaveLen(1))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			gomega.Eventually(func(g gomega.Gomega) {
				var updatedCohort kueue.Cohort
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cohort), &updatedCohort)).To(gomega.Succeed())
				updatedCohort.Labels["team"] = "data-test"
				g.Expect(k8sClient.Update(ctx, &updatedCohort)).To(gomega.Succeed())
			}, util.Timeout, util.ShortInterval).Should(gomega.Succeed())

			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(testingmetrics.CollectFilteredGaugeVec(metrics.CohortSubtreeQuota, map[string]string{
					"cohort":             cohort.Name,
					"custom_team_cohort": "data-test",
				})).To(gomega.HaveLen(1))
				g.Expect(testingmetrics.CollectFilteredGaugeVec(metrics.CohortSubtreeResourceReservations, map[string]string{
					"cohort":             cohort.Name,
					"custom_team_cohort": "data-test",
				})).To(gomega.HaveLen(1))
				g.Expect(testingmetrics.CollectFilteredGaugeVec(metrics.CohortWeightedShare, map[string]string{
					"cohort":             cohort.Name,
					"custom_team_cohort": "data-test",
				})).To(gomega.HaveLen(1))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(testingmetrics.CollectFilteredGaugeVec(metrics.CohortSubtreeQuota, map[string]string{
					"cohort":             cohort.Name,
					"custom_team_cohort": "data-eng",
				})).To(gomega.BeEmpty())
				g.Expect(testingmetrics.CollectFilteredGaugeVec(metrics.CohortSubtreeResourceReservations, map[string]string{
					"cohort":             cohort.Name,
					"custom_team_cohort": "data-eng",
				})).To(gomega.BeEmpty())
				g.Expect(testingmetrics.CollectFilteredGaugeVec(metrics.CohortWeightedShare, map[string]string{
					"cohort":             cohort.Name,
					"custom_team_cohort": "data-eng",
				})).To(gomega.BeEmpty())
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
			util.ExpectCohortInfoMetric(cohort.Name, "", cohort.Name, 1, "data-eng")
			// check cohort metrics too while we're at it

			gomega.Eventually(func(g gomega.Gomega) {
				var updatedCohort kueue.Cohort
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cohort), &updatedCohort)).To(gomega.Succeed())
				updatedCohort.Labels["team"] = "data-test"
				g.Expect(k8sClient.Update(ctx, &updatedCohort)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			// ClearCohortMetrics only clears gauges (CohortInfo), not counters (AdmittedWorkloadsTotal).
			util.ExpectCohortInfoMetric(cohort.Name, "", cohort.Name, 1, "data-test")
			util.ExpectCohortInfoMetric(cohort.Name, "", cohort.Name, 0, "data-eng")
			util.ExpectCohortSubtreeAdmittedWorkloadsTotalMetric(kueue.CohortReference(cohort.Name), "", 1, "data-eng")
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

	ginkgo.When("CustomMetricLabels is enabled with workload custom labels", func() {
		var (
			cq *kueue.ClusterQueue
		)

		ginkgo.BeforeEach(func() {
			features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.CustomMetricLabels, true)
			controllersCfg := &config.Configuration{}
			controllersCfg.Metrics.CustomLabels = []config.ControllerMetricsCustomLabel{
				{Name: "team_cq", SourceLabelKey: "team", SourceKind: ptr.To(config.SourceKindClusterQueue)},
				{Name: "wl_kind", SourceLabelKey: "workload-kind", SourceKind: ptr.To(config.SourceKindWorkload), TrackedValues: []string{"kind1", "kind2"}},
			}
			fwk.StartManager(ctx, cfg, managerAndControllerSetup(controllersCfg, runScheduler))
			defaultFlavor = utiltestingapi.MakeResourceFlavor("default").Obj()
			util.MustCreate(ctx, k8sClient, defaultFlavor)
			ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "custom-labels-wl-")
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			util.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
			gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			util.ExpectObjectToBeDeleted(ctx, k8sClient, defaultFlavor, true)
			fwk.StopManager(ctx)
			metrics.InitMetricVectors(nil)
		})

		ginkgo.It("AdmittedActiveWorkloads metrics should track workloads by custom CQ and WL labels", func() {
			cq = utiltestingapi.MakeClusterQueue("cq").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas(defaultFlavor.Name).
						Resource(corev1.ResourceCPU, "5").
						Obj(),
				).Label("team", "ml-team").Obj()
			util.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, cq)

			lq := utiltestingapi.MakeLocalQueue("lq", ns.Name).
				ClusterQueue(cq.Name).Obj()
			util.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, lq)

			wl1 := utiltestingapi.MakeWorkload("wl1", ns.Name).
				Label("workload-kind", "kind1").
				Queue(kueue.LocalQueueName(lq.Name)).
				Request(corev1.ResourceCPU, "1").Obj()
			util.MustCreate(ctx, k8sClient, wl1)

			wl2 := utiltestingapi.MakeWorkload("wl2", ns.Name).
				Label("workload-kind", "kind1").
				Queue(kueue.LocalQueueName(lq.Name)).
				Request(corev1.ResourceCPU, "1").Obj()
			util.MustCreate(ctx, k8sClient, wl2)

			wl3 := utiltestingapi.MakeWorkload("wl3", ns.Name).
				Label("workload-kind", "kind2").
				Queue(kueue.LocalQueueName(lq.Name)).
				Request(corev1.ResourceCPU, "1").Obj()
			util.MustCreate(ctx, k8sClient, wl3)

			ginkgo.By("verifying all workloads get admitted")
			gomega.Eventually(func(g gomega.Gomega) {
				var updatedWl1 kueue.Workload
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl1), &updatedWl1)).To(gomega.Succeed())
				g.Expect(updatedWl1.Status.Admission).ToNot(gomega.BeNil())

				var updatedWl2 kueue.Workload
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl2), &updatedWl2)).To(gomega.Succeed())
				g.Expect(updatedWl2.Status.Admission).ToNot(gomega.BeNil())

				var updatedWl3 kueue.Workload
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl3), &updatedWl3)).To(gomega.Succeed())
				g.Expect(updatedWl3.Status.Admission).ToNot(gomega.BeNil())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			ginkgo.By("verifying CQ admitted active workloads metric includes custom_team_cq=ml-team and custom_wl_kind values")
			util.ExpectAdmittedActiveWorkloadsGaugeMetric(kueue.ClusterQueueReference(cq.Name), 2, "ml-team", "kind1")
			util.ExpectAdmittedActiveWorkloadsGaugeMetric(kueue.ClusterQueueReference(cq.Name), 1, "ml-team", "kind2")

			ginkgo.By("marking two workloads as finished")
			util.FinishWorkloads(ctx, k8sClient, wl1, wl3)

			ginkgo.By("verifying CQ admitted active workloads metric is updated")
			util.ExpectAdmittedActiveWorkloadsGaugeMetric(kueue.ClusterQueueReference(cq.Name), 1, "ml-team", "kind1")
			util.ExpectAdmittedActiveWorkloadsGaugeMetric(kueue.ClusterQueueReference(cq.Name), 0, "ml-team", "kind2")
		})

		ginkgo.It("should update AdmittedActiveWorkloads metric when workload labels match and we change CQ labels", func() {
			cq = utiltestingapi.MakeClusterQueue("cq").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas(defaultFlavor.Name).
						Resource(corev1.ResourceCPU, "5").
						Obj(),
				).Label("team", "ml-team").Obj()
			util.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, cq)

			lq := utiltestingapi.MakeLocalQueue("lq", ns.Name).
				ClusterQueue(cq.Name).Obj()
			util.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, lq)

			wl := utiltestingapi.MakeWorkload("wl", ns.Name).
				Label("workload-kind", "kind1").
				Queue(kueue.LocalQueueName(lq.Name)).
				Request(corev1.ResourceCPU, "1").Obj()
			util.MustCreate(ctx, k8sClient, wl)

			ginkgo.By("verifying workload gets admitted")
			gomega.Eventually(func(g gomega.Gomega) {
				var updatedWl kueue.Workload
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &updatedWl)).To(gomega.Succeed())
				g.Expect(updatedWl.Status.Admission).ToNot(gomega.BeNil())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			ginkgo.By("verifying CQ admitted active workloads metric includes custom_team_cq=ml-team and custom_wl_kind=kind1")
			util.ExpectAdmittedActiveWorkloadsGaugeMetric(kueue.ClusterQueueReference(cq.Name), 1, "ml-team", "kind1")

			ginkgo.By("updating CQ team label")
			gomega.Eventually(func(g gomega.Gomega) {
				var updatedCq kueue.ClusterQueue
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cq), &updatedCq)).To(gomega.Succeed())
				updatedCq.Labels["team"] = "data-team"
				g.Expect(k8sClient.Update(ctx, &updatedCq)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			ginkgo.By("verifying CQ admitted active workloads metric updates to data-team")
			util.ExpectAdmittedActiveWorkloadsGaugeMetric(kueue.ClusterQueueReference(cq.Name), 1, "data-team", "kind1")
			util.ExpectAdmittedActiveWorkloadsGaugeMetric(kueue.ClusterQueueReference(cq.Name), 0, "ml-team", "kind1")
		})

		ginkgo.It("should update AdmittedActiveWorkloads metric when we update the labels on the workload", func() {
			cq = utiltestingapi.MakeClusterQueue("cq").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas(defaultFlavor.Name).
						Resource(corev1.ResourceCPU, "5").
						Obj(),
				).Label("team", "ml-team").Obj()
			util.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, cq)

			lq := utiltestingapi.MakeLocalQueue("lq", ns.Name).
				ClusterQueue(cq.Name).Obj()
			util.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, lq)

			wl := utiltestingapi.MakeWorkload("wl", ns.Name).
				Label("workload-kind", "kind1").
				Queue(kueue.LocalQueueName(lq.Name)).
				Request(corev1.ResourceCPU, "1").Obj()
			util.MustCreate(ctx, k8sClient, wl)

			ginkgo.By("verifying workload gets admitted")
			gomega.Eventually(func(g gomega.Gomega) {
				var updatedWl kueue.Workload
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &updatedWl)).To(gomega.Succeed())
				g.Expect(updatedWl.Status.Admission).ToNot(gomega.BeNil())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			ginkgo.By("verifying CQ admitted active workloads metric includes custom_team_cq=ml-team and custom_wl_kind=kind1")
			util.ExpectAdmittedActiveWorkloadsGaugeMetric(kueue.ClusterQueueReference(cq.Name), 1, "ml-team", "kind1")
			util.ExpectAdmittedActiveWorkloadsGaugeMetric(kueue.ClusterQueueReference(cq.Name), 0, "ml-team", "kind2")

			ginkgo.By("updating workload custom label value")
			gomega.Eventually(func(g gomega.Gomega) {
				var updatedWl kueue.Workload
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &updatedWl)).To(gomega.Succeed())
				updatedWl.Labels["workload-kind"] = "kind2"
				g.Expect(k8sClient.Update(ctx, &updatedWl)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			ginkgo.By("verifying CQ admitted active workloads metric updates to kind2")
			util.ExpectAdmittedActiveWorkloadsGaugeMetric(kueue.ClusterQueueReference(cq.Name), 1, "ml-team", "kind2")
			util.ExpectAdmittedActiveWorkloadsGaugeMetric(kueue.ClusterQueueReference(cq.Name), 0, "ml-team", "kind1")
		})

		ginkgo.It("should update/decrement AdmittedActiveWorkloads metric when workload is deleted", func() {
			cq = utiltestingapi.MakeClusterQueue("cq").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas(defaultFlavor.Name).
						Resource(corev1.ResourceCPU, "5").
						Obj(),
				).Label("team", "ml-team").Obj()
			util.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, cq)

			lq := utiltestingapi.MakeLocalQueue("lq", ns.Name).
				ClusterQueue(cq.Name).Obj()
			util.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, lq)

			wl := utiltestingapi.MakeWorkload("wl", ns.Name).
				Label("workload-kind", "kind1").
				Queue(kueue.LocalQueueName(lq.Name)).
				Request(corev1.ResourceCPU, "1").Obj()
			util.MustCreate(ctx, k8sClient, wl)

			ginkgo.By("verifying workload gets admitted")
			gomega.Eventually(func(g gomega.Gomega) {
				var updatedWl kueue.Workload
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &updatedWl)).To(gomega.Succeed())
				g.Expect(updatedWl.Status.Admission).ToNot(gomega.BeNil())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			ginkgo.By("verifying CQ admitted active workloads metric includes custom_team_cq=ml-team and custom_wl_kind=kind1")
			util.ExpectAdmittedActiveWorkloadsGaugeMetric(kueue.ClusterQueueReference(cq.Name), 1, "ml-team", "kind1")

			ginkgo.By("deleting workload")
			util.ExpectObjectToBeDeleted(ctx, k8sClient, wl, true)

			ginkgo.By("verifying CQ admitted active workloads metric is updated/decremented")
			util.ExpectAdmittedActiveWorkloadsGaugeMetric(kueue.ClusterQueueReference(cq.Name), 0, "ml-team", "kind1")
		})

		ginkgo.It("PendingWorkloads: should track active pending workloads with custom labels", func() {
			cq = utiltestingapi.MakeClusterQueue("cq").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas(defaultFlavor.Name).
						Resource(corev1.ResourceCPU, "5").
						Obj(),
				).Label("team", "ml-team").Obj()
			util.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, cq)

			lq := utiltestingapi.MakeLocalQueue("lq", ns.Name).
				ClusterQueue(cq.Name).Obj()
			util.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, lq)

			wl1 := utiltestingapi.MakeWorkload("wl1", ns.Name).
				Label("workload-kind", "kind1").
				Queue(kueue.LocalQueueName(lq.Name)).
				Request(corev1.ResourceCPU, "1").Obj()
			util.MustCreate(ctx, k8sClient, wl1)

			wl2 := utiltestingapi.MakeWorkload("wl2", ns.Name).
				Label("workload-kind", "kind1").
				Queue(kueue.LocalQueueName(lq.Name)).
				Request(corev1.ResourceCPU, "1").Obj()
			util.MustCreate(ctx, k8sClient, wl2)

			wl3 := utiltestingapi.MakeWorkload("wl3", ns.Name).
				Label("workload-kind", "kind2").
				Queue(kueue.LocalQueueName(lq.Name)).
				Request(corev1.ResourceCPU, "1").Obj()
			util.MustCreate(ctx, k8sClient, wl3)

			wlUntracked := utiltestingapi.MakeWorkload("wl-untracked", ns.Name).
				Label("workload-kind", "kind3").
				Queue(kueue.LocalQueueName(lq.Name)).
				Request(corev1.ResourceCPU, "1").Obj()
			util.MustCreate(ctx, k8sClient, wlUntracked)

			ginkgo.By("verifying active pending workloads metric counts match the custom labels")
			util.ExpectPendingWorkloadsMetric(cq, 2, 0, "ml-team", "kind1")
			util.ExpectPendingWorkloadsMetric(cq, 1, 0, "ml-team", "kind2")
			util.ExpectPendingWorkloadsMetric(cq, 1, 0, "ml-team", "kueue.x-k8s.io/_UNTRACKED_VALUE_")
		})

		ginkgo.It("PendingWorkloads: should track inadmissible pending workloads", func() {
			cq = utiltestingapi.MakeClusterQueue("cq").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas(defaultFlavor.Name).
						Resource(corev1.ResourceCPU, "5").
						Obj(),
				).Label("team", "ml-team").Obj()
			util.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, cq)

			lq := utiltestingapi.MakeLocalQueue("lq", ns.Name).
				ClusterQueue(cq.Name).Obj()
			util.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, lq)

			wl1 := utiltestingapi.MakeWorkload("wl1", ns.Name).
				Label("workload-kind", "kind1").
				Queue(kueue.LocalQueueName(lq.Name)).
				Request(corev1.ResourceCPU, "1").Obj()
			util.MustCreate(ctx, k8sClient, wl1)

			wl2 := utiltestingapi.MakeWorkload("wl2", ns.Name).
				Label("workload-kind", "kind2").
				Queue(kueue.LocalQueueName(lq.Name)).
				Request(corev1.ResourceCPU, "1").Obj()
			util.MustCreate(ctx, k8sClient, wl2)

			wl3 := utiltestingapi.MakeWorkload("wl3", ns.Name).
				Label("workload-kind", "kind2").
				Queue(kueue.LocalQueueName(lq.Name)).
				Request(corev1.ResourceCPU, "1").Obj()
			util.MustCreate(ctx, k8sClient, wl3)

			ginkgo.By("waiting for wl3 to be present in the queue manager active heap")
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(qManager.PendingWorkloadsInfo(kueue.ClusterQueueReference(cq.Name))).To(gomega.HaveLen(1))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			ginkgo.By("popping wl3 from the queue manager to increment popCycle and clear it from active heap")
			popped := qManager.Heads(ctx)
			gomega.Expect(popped).To(gomega.HaveLen(1))
			gomega.Expect(workload.Key(popped[0].Obj)).To(gomega.Equal(workload.Key(wl3)))

			ginkgo.By("making popped workload (wl3) inadmissible")
			var fetchedWl3 kueue.Workload
			gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl3), &fetchedWl3)).To(gomega.Succeed())
			qManager.RequeueWorkload(ctx, workload.NewInfo(&fetchedWl3), qcache.RequeueReasonGeneric, "")

			ginkgo.By("verifying counts: wl1 active kind1, wl2 active kind2, wl3 inadmissible kind2")
			util.ExpectPendingWorkloadsMetric(cq, 1, 0, "ml-team", "kind1")
			util.ExpectPendingWorkloadsMetric(cq, 1, 1, "ml-team", "kind2")
		})

		ginkgo.It("PendingWorkloads: should count workloads correctly on CQ stop/resume", func() {
			cq = utiltestingapi.MakeClusterQueue("cq").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas(defaultFlavor.Name).
						Resource(corev1.ResourceCPU, "5").
						Obj(),
				).Label("team", "ml-team").Obj()
			util.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, cq)

			lq := utiltestingapi.MakeLocalQueue("lq", ns.Name).
				ClusterQueue(cq.Name).Obj()
			util.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, lq)

			wl3 := utiltestingapi.MakeWorkload("wl3", ns.Name).
				Label("workload-kind", "kind2").
				Queue(kueue.LocalQueueName(lq.Name)).
				Request(corev1.ResourceCPU, "1").Obj()
			util.MustCreate(ctx, k8sClient, wl3)

			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(qManager.PendingWorkloadsInfo(kueue.ClusterQueueReference(cq.Name))).To(gomega.HaveLen(1))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			popped := qManager.Heads(ctx)
			gomega.Expect(popped).To(gomega.HaveLen(1))

			var fetchedWl3 kueue.Workload
			gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl3), &fetchedWl3)).To(gomega.Succeed())
			qManager.RequeueWorkload(ctx, workload.NewInfo(&fetchedWl3), qcache.RequeueReasonGeneric, "")

			wl1 := utiltestingapi.MakeWorkload("wl1", ns.Name).
				Label("workload-kind", "kind1").
				Queue(kueue.LocalQueueName(lq.Name)).
				Request(corev1.ResourceCPU, "1").Obj()
			util.MustCreate(ctx, k8sClient, wl1)

			ginkgo.By("verifying initial active vs inadmissible counts")
			util.ExpectPendingWorkloadsMetric(cq, 1, 0, "ml-team", "kind1")
			util.ExpectPendingWorkloadsMetric(cq, 0, 1, "ml-team", "kind2")

			ginkgo.By("stopping the ClusterQueue to make it inactive")
			gomega.Eventually(func(g gomega.Gomega) {
				var updatedCq kueue.ClusterQueue
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cq), &updatedCq)).To(gomega.Succeed())
				updatedCq.Spec.StopPolicy = ptr.To(kueue.Hold)
				g.Expect(k8sClient.Update(ctx, &updatedCq)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			ginkgo.By("verifying both active and inadmissible workloads are reported as inadmissible")
			util.ExpectPendingWorkloadsMetric(cq, 0, 1, "ml-team", "kind1")
			util.ExpectPendingWorkloadsMetric(cq, 0, 1, "ml-team", "kind2")

			ginkgo.By("resuming the ClusterQueue to make it active")
			gomega.Eventually(func(g gomega.Gomega) {
				var updatedCq kueue.ClusterQueue
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cq), &updatedCq)).To(gomega.Succeed())
				updatedCq.Spec.StopPolicy = ptr.To(kueue.None)
				g.Expect(k8sClient.Update(ctx, &updatedCq)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			ginkgo.By("verifying they restore to their original active/inadmissible statuses")
			util.ExpectPendingWorkloadsMetric(cq, 1, 0, "ml-team", "kind1")
			util.ExpectPendingWorkloadsMetric(cq, 0, 1, "ml-team", "kind2")
		})

		ginkgo.It("PendingWorkloads: should update metrics when labels on a pending workload change", func() {
			cq = utiltestingapi.MakeClusterQueue("cq").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas(defaultFlavor.Name).
						Resource(corev1.ResourceCPU, "5").
						Obj(),
				).Label("team", "ml-team").Obj()
			util.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, cq)

			lq := utiltestingapi.MakeLocalQueue("lq", ns.Name).
				ClusterQueue(cq.Name).Obj()
			util.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, lq)

			wl1 := utiltestingapi.MakeWorkload("wl1", ns.Name).
				Label("workload-kind", "kind1").
				Queue(kueue.LocalQueueName(lq.Name)).
				Request(corev1.ResourceCPU, "1").Obj()
			util.MustCreate(ctx, k8sClient, wl1)

			ginkgo.By("verifying initial active count")
			util.ExpectPendingWorkloadsMetric(cq, 1, 0, "ml-team", "kind1")

			ginkgo.By("changing label on the workload from kind1 to kind2")
			gomega.Eventually(func(g gomega.Gomega) {
				var updatedWl1 kueue.Workload
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl1), &updatedWl1)).To(gomega.Succeed())
				updatedWl1.Labels["workload-kind"] = "kind2"
				g.Expect(k8sClient.Update(ctx, &updatedWl1)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			ginkgo.By("verifying that the metric counts are updated according to the new workload label")
			util.ExpectPendingWorkloadsMetric(cq, 0, 0, "ml-team", "kind1")
			util.ExpectPendingWorkloadsMetric(cq, 1, 0, "ml-team", "kind2")
		})

		ginkgo.It("PendingWorkloads: should update metrics when labels on a ClusterQueue change", func() {
			cq = utiltestingapi.MakeClusterQueue("cq").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas(defaultFlavor.Name).
						Resource(corev1.ResourceCPU, "5").
						Obj(),
				).Label("team", "ml-team").Obj()
			util.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, cq)

			lq := utiltestingapi.MakeLocalQueue("lq", ns.Name).
				ClusterQueue(cq.Name).Obj()
			util.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, lq)

			wl1 := utiltestingapi.MakeWorkload("wl1", ns.Name).
				Label("workload-kind", "kind1").
				Queue(kueue.LocalQueueName(lq.Name)).
				Request(corev1.ResourceCPU, "1").Obj()
			util.MustCreate(ctx, k8sClient, wl1)

			ginkgo.By("verifying initial active count under old CQ label")
			util.ExpectPendingWorkloadsMetric(cq, 1, 0, "ml-team", "kind1")

			ginkgo.By("changing label on the ClusterQueue from ml-team to data-team")
			gomega.Eventually(func(g gomega.Gomega) {
				var updatedCq kueue.ClusterQueue
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cq), &updatedCq)).To(gomega.Succeed())
				updatedCq.Labels["team"] = "data-team"
				g.Expect(k8sClient.Update(ctx, &updatedCq)).To(gomega.Succeed())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())

			ginkgo.By("verifying that the metric counts are updated under the new ClusterQueue label")
			util.ExpectPendingWorkloadsMetric(cq, 1, 0, "data-team", "kind1")

			ginkgo.By("verifying that the old ClusterQueue label series are cleaned up")
			util.ExpectPendingWorkloadsMetric(cq, 0, 0, "ml-team", "kind1")
		})

		ginkgo.It("PendingWorkloads: should clean up entry when all contributing workloads are gone", func() {
			cq = utiltestingapi.MakeClusterQueue("cq").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas(defaultFlavor.Name).
						Resource(corev1.ResourceCPU, "5").
						Obj(),
				).Label("team", "ml-team").Obj()
			util.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, cq)

			lq := utiltestingapi.MakeLocalQueue("lq", ns.Name).
				ClusterQueue(cq.Name).Obj()
			util.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, lq)

			wl1 := utiltestingapi.MakeWorkload("wl1", ns.Name).
				Label("workload-kind", "kind1").
				Queue(kueue.LocalQueueName(lq.Name)).
				Request(corev1.ResourceCPU, "1").Obj()
			util.MustCreate(ctx, k8sClient, wl1)

			ginkgo.By("verifying initial metric series is present")
			util.ExpectPendingWorkloadsMetric(cq, 1, 0, "ml-team", "kind1")

			ginkgo.By("deleting the workload")
			util.ExpectObjectToBeDeleted(ctx, k8sClient, wl1, true)

			ginkgo.By("verifying metric series for kind1 is deleted (not holding 0)")
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(testingmetrics.CollectFilteredGaugeVec(metrics.PendingWorkloads, map[string]string{
					"cluster_queue":  cq.Name,
					"custom_team_cq": "ml-team",
					"custom_wl_kind": "kind1",
				})).To(gomega.BeEmpty())
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})
	})
})
