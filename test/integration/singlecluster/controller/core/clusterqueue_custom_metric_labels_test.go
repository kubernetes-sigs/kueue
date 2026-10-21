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
	"sigs.k8s.io/controller-runtime/pkg/client"

	configapi "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/metrics"
	"sigs.k8s.io/kueue/pkg/util/roletracker"
	testingmetrics "sigs.k8s.io/kueue/pkg/util/testing/metrics"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/test/util/behavioral"
)

var _ = ginkgo.Describe("ClusterQueue pending workload custom metrics", ginkgo.Label("controller:clusterqueue", "area:core"), func() {
	var (
		ns     *corev1.Namespace
		flavor *kueue.ResourceFlavor
		cq     *kueue.ClusterQueue
		lq     *kueue.LocalQueue
	)

	ginkgo.BeforeEach(func() {
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.CustomMetricLabels, true)
		configuration := &configapi.Configuration{}
		configuration.Metrics.CustomLabels = []configapi.ControllerMetricsCustomLabel{
			utiltestingapi.MakeCustomLabel("wl_kind").SourceLabelKey("workload-kind").SourceKind(configapi.SourceKindWorkload).TrackedValues("kind1", "kind2").Obj(),
		}
		fwk.StartManager(ctx, cfg, managerAndControllerSetup(configuration, runScheduler))
		ns = behavioral.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "cq-custom-metrics-")
		flavor = utiltestingapi.MakeResourceFlavor("cq-custom-metrics-flavor").Obj()
		behavioral.MustCreate(ctx, k8sClient, flavor)

		// StrictFIFO keeps workloads active when they cannot fit the zero quota.
		cq = utiltestingapi.MakeClusterQueue("cq-custom-metrics").
			QueueingStrategy(kueue.StrictFIFO).
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas(flavor.Name).Resource(corev1.ResourceCPU, "0").Obj()).
			Obj()
		behavioral.MustCreate(ctx, k8sClient, cq)
		lq = utiltestingapi.MakeLocalQueue("lq", ns.Name).ClusterQueue(cq.Name).Obj()
		behavioral.MustCreate(ctx, k8sClient, lq)
		behavioral.ExpectClusterQueuesToBeActive(ctx, k8sClient, cq)
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(behavioral.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, flavor, true)
		fwk.StopManager(ctx)
		metrics.InitMetricVectors(nil)
	})

	ginkgo.It("clears stale series when stopped, resumed, and workloads are deleted", func() {
		wl1 := utiltestingapi.MakeWorkload("wl1", ns.Name).
			Label("workload-kind", "kind1").Queue(kueue.LocalQueueName(lq.Name)).Request(corev1.ResourceCPU, "1").Obj()
		wl2 := utiltestingapi.MakeWorkload("wl2", ns.Name).
			Label("workload-kind", "kind2").Queue(kueue.LocalQueueName(lq.Name)).Request(corev1.ResourceCPU, "1").Obj()
		behavioral.MustCreate(ctx, k8sClient, wl1)
		behavioral.MustCreate(ctx, k8sClient, wl2)

		expectPendingMetrics := func(status string, kinds ...string) {
			ginkgo.GinkgoHelper()
			want := make([]testingmetrics.MetricDataPoint, 0, len(kinds))
			for _, kind := range kinds {
				want = append(want, testingmetrics.MetricDataPoint{
					Labels: map[string]string{
						"cluster_queue":  cq.Name,
						"status":         status,
						"custom_wl_kind": kind,
						"replica_role":   roletracker.RoleStandalone,
					},
					Value: 1,
				})
			}
			gomega.Eventually(func() []testingmetrics.MetricDataPoint {
				return testingmetrics.CollectFilteredGaugeVec(metrics.PendingWorkloads, map[string]string{"cluster_queue": cq.Name})
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.ConsistOf(want))
		}
		setStopPolicy := func(policy kueue.StopPolicy) {
			ginkgo.GinkgoHelper()
			gomega.Eventually(func(g gomega.Gomega) {
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(cq), cq)).To(gomega.Succeed())
				cq.Spec.StopPolicy = new(policy)
				g.Expect(k8sClient.Update(ctx, cq)).To(gomega.Succeed())
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		}

		ginkgo.By("reporting both workloads as active")
		expectPendingMetrics(metrics.PendingStatusActive, "kind1", "kind2")

		ginkgo.By("stopping the ClusterQueue and removing its active series")
		setStopPolicy(kueue.Hold)
		expectPendingMetrics(metrics.PendingStatusInadmissible, "kind1", "kind2")

		ginkgo.By("resuming the ClusterQueue and removing its inadmissible series")
		setStopPolicy(kueue.None)
		expectPendingMetrics(metrics.PendingStatusActive, "kind1", "kind2")

		ginkgo.By("deleting a workload while the ClusterQueue is active")
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, wl2, true)
		expectPendingMetrics(metrics.PendingStatusActive, "kind1")

		ginkgo.By("deleting the remaining workload while the ClusterQueue is stopped")
		setStopPolicy(kueue.Hold)
		expectPendingMetrics(metrics.PendingStatusInadmissible, "kind1")
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, wl1, true)
		expectPendingMetrics(metrics.PendingStatusInadmissible)

		ginkgo.By("resuming an empty ClusterQueue without recreating stale series")
		setStopPolicy(kueue.None)
		expectPendingMetrics(metrics.PendingStatusActive)
	})
})
