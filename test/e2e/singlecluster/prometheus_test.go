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

package e2e

import (
	"fmt"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	prometheusv1 "github.com/prometheus/client_golang/api/prometheus/v1"
	"github.com/prometheus/common/model"
	corev1 "k8s.io/api/core/v1"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/test/util"
)

const (
	kueueBuildInfoMetric         = "kueue_build_info"
	admittedWorkloadsTotalMetric = "kueue_admitted_workloads_total"
)

var _ = ginkgo.Describe("Prometheus", ginkgo.Label("area:prometheus", "feature:prometheus"), func() {
	ginkgo.It("should discover Kueue target and report it as up", func() {
		gomega.Eventually(func(g gomega.Gomega) {
			result, err := prometheusClient.Targets(ctx)
			g.Expect(err).NotTo(gomega.HaveOccurred())

			hasKueueTarget := false
			for _, t := range result.Active {
				if t.Labels["job"] == model.LabelValue(util.DefaultMetricsServiceName) &&
					t.Labels["namespace"] == model.LabelValue(util.GetKueueNamespace()) {
					hasKueueTarget = true
					g.Expect(t.Health).To(gomega.Equal(prometheusv1.HealthGood))
					break
				}
			}
			g.Expect(hasKueueTarget).To(gomega.BeTrue(), "Kueue target not found. Active targets: %v", result.Active)
		}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
	})

	ginkgo.It("should scrape kueue_build_info metric via PromQL", func() {
		gomega.Eventually(func(g gomega.Gomega) {
			result, _, err := prometheusClient.Query(ctx, kueueBuildInfoMetric, time.Now())
			g.Expect(err).NotTo(gomega.HaveOccurred())

			vector, ok := result.(model.Vector)
			g.Expect(ok).To(gomega.BeTrue())
			g.Expect(vector).NotTo(gomega.BeEmpty())
			g.Expect(string(vector[0].Metric[model.MetricNameLabel])).To(gomega.Equal(kueueBuildInfoMetric))
		}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
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
		}, util.LongTimeout, util.Interval).Should(gomega.Succeed())
	})
})
