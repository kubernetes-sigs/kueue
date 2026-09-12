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

package baseline

import (
	"fmt"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	"github.com/prometheus/common/model"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/component-base/featuregate"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/test/util"
)

const (
	kueueBuildInfoMetric         = "kueue_build_info"
	kueueFeatureEnabledMetric    = "kueue_feature_enabled"
	admittedWorkloadsTotalMetric = "kueue_admitted_workloads_total"
)

var _ = ginkgo.Describe("Prometheus", ginkgo.Label("area:prometheus", "feature:prometheus"), func() {
	ginkgo.It("should discover Kueue target and report it as up", func() {
		util.ExpectPrometheusTargetForKueue(ctx, prometheusClient)
	})

	ginkgo.It("should scrape kueue_build_info metric via PromQL", func() {
		gomega.Eventually(func(g gomega.Gomega) {
			result, _, err := prometheusClient.Query(ctx, kueueBuildInfoMetric, time.Now())
			g.Expect(err).NotTo(gomega.HaveOccurred())

			vector, ok := result.(model.Vector)
			g.Expect(ok).To(gomega.BeTrue())
			g.Expect(vector).NotTo(gomega.BeEmpty())
			g.Expect(string(vector[0].Metric[model.MetricNameLabel])).To(gomega.Equal(kueueBuildInfoMetric))
		}, util.VeryLongTimeout, util.Interval).Should(gomega.Succeed())
	})

	ginkgo.It("should scrape kueue_feature_enabled metric via PromQL", func() {
		// The manager shares the process-wide feature gate registry with the apiserver
		// libraries it links against, so this is also the assertion that only Kueue's
		// own gates are reported.
		kueueGates := features.KueueFeatureGates()

		gomega.Eventually(func(g gomega.Gomega) {
			result, _, err := prometheusClient.Query(ctx, kueueFeatureEnabledMetric, time.Now())
			g.Expect(err).NotTo(gomega.HaveOccurred())

			vector, ok := result.(model.Vector)
			g.Expect(ok).To(gomega.BeTrue())
			g.Expect(vector).NotTo(gomega.BeEmpty())

			var enabled, disabled int
			for _, sample := range vector {
				g.Expect(string(sample.Metric[model.MetricNameLabel])).To(gomega.Equal(kueueFeatureEnabledMetric))
				g.Expect(kueueGates).To(gomega.HaveKey(featuregate.Feature(sample.Metric["name"])))
				// Empty is the stage of a generally available gate.
				g.Expect(string(sample.Metric["stage"])).To(gomega.BeElementOf("", "ALPHA", "BETA", "DEPRECATED"))
				switch sample.Value {
				case 1:
					enabled++
				case 0:
					disabled++
				default:
					ginkgo.Fail(fmt.Sprintf("Gate %q reported %v, want 0 or 1", sample.Metric["name"], sample.Value))
				}
			}
			// Kueue ships both gates that default on and gates that default off, so a
			// default install reports each value at least once. Disabled gates must be
			// reported as 0 rather than dropped, so that operators can tell "gate off"
			// apart from "this Kueue is too old to know the gate".
			g.Expect(enabled).To(gomega.BeNumerically(">", 0))
			g.Expect(disabled).To(gomega.BeNumerically(">", 0))
		}, util.VeryLongTimeout, util.Interval).Should(gomega.Succeed())
	})

	ginkgo.It("should report workload admission metrics via PromQL", func() {
		ns := util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "e2e-prom-")
		ginkgo.DeferCleanup(func() {
			gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		})

		resourceFlavor := utiltestingapi.MakeResourceFlavor("prom-test-flavor-" + ns.Name).Obj()
		util.MustCreate(ctx, k8sClient, resourceFlavor)
		ginkgo.DeferCleanup(func() {
			util.ExpectObjectToBeDeleted(ctx, k8sClient, resourceFlavor, true)
		})

		clusterQueue := utiltestingapi.MakeClusterQueue("").
			GeneratedName("prom-test-cq-").
			ResourceGroup(
				*utiltestingapi.MakeFlavorQuotas(resourceFlavor.Name).
					Resource(corev1.ResourceCPU, "1").
					Resource(corev1.ResourceMemory, "1Gi").
					Obj(),
			).
			Obj()
		util.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, clusterQueue)
		ginkgo.DeferCleanup(func() {
			util.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
		})

		localQueue := utiltestingapi.MakeLocalQueue("", ns.Name).
			GeneratedName("prom-test-lq-").
			ClusterQueue(clusterQueue.Name).
			Obj()
		util.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, localQueue)

		ginkgo.By("Creating and admitting a workload")
		workload := utiltestingapi.MakeWorkload("prom-test-workload", ns.Name).
			Queue(kueue.LocalQueueName(localQueue.Name)).
			PodSets(
				*utiltestingapi.MakePodSet("ps1", 1).Obj(),
			).
			RequestAndLimit(corev1.ResourceCPU, "1").
			Obj()
		util.MustCreate(ctx, k8sClient, workload)
		ginkgo.DeferCleanup(func() {
			util.ExpectObjectToBeDeleted(ctx, k8sClient, workload, true)
		})
		util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, workload)

		ginkgo.By("Verifying the admission metric is reported")
		gomega.Eventually(func(g gomega.Gomega) {
			result, _, err := prometheusClient.Query(ctx,
				fmt.Sprintf(`%s{cluster_queue="%s"}`, admittedWorkloadsTotalMetric, clusterQueue.Name),
				time.Now())
			g.Expect(err).NotTo(gomega.HaveOccurred())

			vector, ok := result.(model.Vector)
			g.Expect(ok).To(gomega.BeTrue())
			g.Expect(vector).NotTo(gomega.BeEmpty())
		}, util.VeryLongTimeout, util.Interval).Should(gomega.Succeed())
	})
})
