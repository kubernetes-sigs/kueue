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
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("LocalQueue pending metrics", ginkgo.Label("controller:localqueue", "area:core"), func() {
	var (
		ns     *corev1.Namespace
		flavor *kueue.ResourceFlavor
		cq     *kueue.ClusterQueue
	)

	ginkgo.BeforeEach(func() {
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.LocalQueueMetrics, true)
		// Do not start the scheduler: unrelated scheduling attempts could refresh
		// a stale ClusterQueue metric after a LocalQueue is removed.
		fwk.StartManager(ctx, cfg, managerAndControllerSetup(nil))
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "lq-pending-metrics-")
		flavor = utiltestingapi.MakeResourceFlavor("pending-metrics-default").Obj()
		util.MustCreate(ctx, k8sClient, flavor)
		cq = utiltestingapi.MakeClusterQueue("lq-pending-metrics").ResourceGroup(
			*utiltestingapi.MakeFlavorQuotas(flavor.Name).Resource(corev1.ResourceCPU, "5").Obj(),
		).Obj()
		util.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, cq)
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
		util.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
		util.ExpectObjectToBeDeleted(ctx, k8sClient, flavor, true)
		fwk.StopManager(ctx)
	})

	ginkgo.DescribeTable("should refresh pending metrics when a LocalQueue is removed from queueing", func(stopPolicy *kueue.StopPolicy) {
		queues := []*kueue.LocalQueue{
			utiltestingapi.MakeLocalQueue("first", ns.Name).ClusterQueue(cq.Name).Obj(),
			utiltestingapi.MakeLocalQueue("second", ns.Name).ClusterQueue(cq.Name).Obj(),
		}
		util.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, queues...)
		for _, lq := range queues {
			wl := utiltestingapi.MakeWorkload(lq.Name, ns.Name).
				Queue(kueue.LocalQueueName(lq.Name)).Request(corev1.ResourceCPU, "1").Obj()
			util.MustCreate(ctx, k8sClient, wl)
			util.ExpectLQPendingWorkloadsMetric(lq, 1, 0)
		}
		util.ExpectPendingWorkloadsMetric(cq, 2, 0)

		for i, lq := range queues {
			ginkgo.By("removing LocalQueue " + lq.Name + " from queueing")
			if stopPolicy == nil {
				util.ExpectObjectToBeDeleted(ctx, k8sClient, lq, true)
			} else {
				gomega.Eventually(func() error {
					if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(lq), lq); err != nil {
						return err
					}
					before := lq.DeepCopy()
					lq.Spec.StopPolicy = stopPolicy
					return k8sClient.Patch(ctx, lq, client.MergeFrom(before))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			}

			ginkgo.By("checking the removed queue has no pending metric series")
			gomega.Eventually(func() []testingmetrics.MetricDataPoint {
				return testingmetrics.CollectFilteredGaugeVec(metrics.LocalQueuePendingWorkloads,
					map[string]string{"name": lq.Name, "namespace": lq.Namespace})
			}, util.Timeout, util.Interval).Should(gomega.BeEmpty())

			ginkgo.By("checking the ClusterQueue count preserves only the remaining queue")
			util.ExpectPendingWorkloadsMetric(cq, len(queues)-i-1, 0)
			if i == 0 {
				util.ExpectLQPendingWorkloadsMetric(queues[1], 1, 0)
			}
		}
	},
		ginkgo.Entry("deleted", nil),
		ginkgo.Entry("stopped with Hold", ptr.To(kueue.Hold)),
		ginkgo.Entry("stopped with HoldAndDrain", ptr.To(kueue.HoldAndDrain)),
	)
})
