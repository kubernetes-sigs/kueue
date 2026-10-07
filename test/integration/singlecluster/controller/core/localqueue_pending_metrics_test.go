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
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/metrics"
	testingmetrics "sigs.k8s.io/kueue/pkg/util/testing/metrics"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	"sigs.k8s.io/kueue/test/util/behavioral"
)

var _ = ginkgo.Describe("LocalQueue pending metrics", ginkgo.Label("controller:localqueue", "area:core"), func() {
	var (
		ns     *corev1.Namespace
		flavor *kueue.ResourceFlavor
		cq     *kueue.ClusterQueue
		queues []*kueue.LocalQueue
	)

	ginkgo.BeforeEach(func() {
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.LocalQueueMetrics, true)
		// Do not start the scheduler: unrelated scheduling attempts could refresh
		// a stale ClusterQueue metric after a LocalQueue is removed.
		fwk.StartManager(ctx, cfg, managerAndControllerSetup(nil))
		ns = behavioral.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "lq-pending-metrics-")
		flavor = utiltestingapi.MakeResourceFlavor("pending-metrics-default").Obj()
		behavioral.MustCreate(ctx, k8sClient, flavor)
		cq = utiltestingapi.MakeClusterQueue("lq-pending-metrics").ResourceGroup(
			*utiltestingapi.MakeFlavorQuotas(flavor.Name).Resource(corev1.ResourceCPU, "5").Obj(),
		).Obj()
		behavioral.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, cq)

		queues = []*kueue.LocalQueue{
			utiltestingapi.MakeLocalQueue("first", ns.Name).ClusterQueue(cq.Name).Obj(),
			utiltestingapi.MakeLocalQueue("second", ns.Name).ClusterQueue(cq.Name).Obj(),
		}
		behavioral.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, queues...)
		for _, lq := range queues {
			wl := utiltestingapi.MakeWorkload(lq.Name, ns.Name).
				Queue(kueue.LocalQueueName(lq.Name)).Request(corev1.ResourceCPU, "1").Obj()
			behavioral.MustCreate(ctx, k8sClient, wl)
			behavioral.ExpectLQPendingWorkloadsMetric(lq, 1, 0)
		}
		behavioral.ExpectPendingWorkloadsMetric(cq, 2, 0)
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(behavioral.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		gomega.Expect(behavioral.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, flavor, true)
		fwk.StopManager(ctx)
	})

	ginkgo.It("should refresh pending metrics when a LocalQueue is deleted", func() {
		for i, lq := range queues {
			ginkgo.By("deleting LocalQueue " + lq.Name)
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, lq, true)

			ginkgo.By("checking the removed queue has no pending metric series")
			gomega.Eventually(func() []testingmetrics.MetricDataPoint {
				return testingmetrics.CollectFilteredGaugeVec(metrics.LocalQueuePendingWorkloads,
					map[string]string{"name": lq.Name, "namespace": lq.Namespace})
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.BeEmpty())

			ginkgo.By("checking the ClusterQueue count preserves only the remaining queue")
			behavioral.ExpectPendingWorkloadsMetric(cq, len(queues)-i-1, 0)
			if i == 0 {
				behavioral.ExpectLQPendingWorkloadsMetric(queues[1], 1, 0)
			}
		}
	})

	ginkgo.It("should refresh pending metrics when a LocalQueue is stopped with Hold", func() {
		for i, lq := range queues {
			ginkgo.By("stopping LocalQueue " + lq.Name + " with Hold")
			gomega.Eventually(func() error {
				if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(lq), lq); err != nil {
					return err
				}
				before := lq.DeepCopy()
				lq.Spec.StopPolicy = ptr.To(kueue.Hold)
				return k8sClient.Patch(ctx, lq, client.MergeFrom(before))
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

			ginkgo.By("checking the removed queue has no pending metric series")
			gomega.Eventually(func() []testingmetrics.MetricDataPoint {
				return testingmetrics.CollectFilteredGaugeVec(metrics.LocalQueuePendingWorkloads,
					map[string]string{"name": lq.Name, "namespace": lq.Namespace})
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.BeEmpty())

			ginkgo.By("checking the ClusterQueue count preserves only the remaining queue")
			behavioral.ExpectPendingWorkloadsMetric(cq, len(queues)-i-1, 0)
			if i == 0 {
				behavioral.ExpectLQPendingWorkloadsMetric(queues[1], 1, 0)
			}
		}
	})

	ginkgo.It("should refresh pending metrics when a LocalQueue is stopped with HoldAndDrain", func() {
		for i, lq := range queues {
			ginkgo.By("stopping LocalQueue " + lq.Name + " with HoldAndDrain")
			gomega.Eventually(func() error {
				if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(lq), lq); err != nil {
					return err
				}
				before := lq.DeepCopy()
				lq.Spec.StopPolicy = ptr.To(kueue.HoldAndDrain)
				return k8sClient.Patch(ctx, lq, client.MergeFrom(before))
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

			ginkgo.By("checking the removed queue has no pending metric series")
			gomega.Eventually(func() []testingmetrics.MetricDataPoint {
				return testingmetrics.CollectFilteredGaugeVec(metrics.LocalQueuePendingWorkloads,
					map[string]string{"name": lq.Name, "namespace": lq.Namespace})
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.BeEmpty())

			ginkgo.By("checking the ClusterQueue count preserves only the remaining queue")
			behavioral.ExpectPendingWorkloadsMetric(cq, len(queues)-i-1, 0)
			if i == 0 {
				behavioral.ExpectLQPendingWorkloadsMetric(queues[1], 1, 0)
			}
		}
	})
})
