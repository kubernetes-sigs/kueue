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

package tas

import (
	"slices"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	eventsv1 "k8s.io/api/events/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/component-base/metrics/testutil"
	"sigs.k8s.io/controller-runtime/pkg/client"

	config "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/metrics"
	"sigs.k8s.io/kueue/pkg/util/roletracker"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingnode "sigs.k8s.io/kueue/pkg/util/testingjobs/node"
	"sigs.k8s.io/kueue/test/util/behavioral"
	"sigs.k8s.io/kueue/test/util/behavioral/integration"
)

var _ = ginkgo.Describe("TopologyAwareScheduling with custom metric labels", func() {
	var (
		ns           *corev1.Namespace
		topology     *kueue.Topology
		tasFlavor    *kueue.ResourceFlavor
		clusterQueue *kueue.ClusterQueue
		localQueue   *kueue.LocalQueue
		nodeX1       *corev1.Node
		nodeX2       *corev1.Node
	)

	newNode := func(name string) *corev1.Node {
		return testingnode.MakeNode(name).
			Label("tas-node", "true").
			Label(corev1.LabelHostname, name).
			StatusAllocatable(corev1.ResourceList{
				corev1.ResourceCPU:  resource.MustParse("1"),
				corev1.ResourcePods: resource.MustParse("10"),
			}).
			Ready().
			Obj()
	}

	ginkgo.BeforeEach(func() {
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.CustomMetricLabels, true)
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.TASFailedNodeReplacementFailFast, false)
		controllersCfg := &config.Configuration{}
		controllersCfg.Metrics.CustomLabels = []config.ControllerMetricsCustomLabel{
			utiltestingapi.MakeCustomLabel("team").SourceLabelKey("team").SourceKind(config.SourceKindClusterQueue).Obj(),
			utiltestingapi.MakeCustomLabel("project").SourceLabelKey("project").SourceKind(config.SourceKindLocalQueue).Obj(),
		}
		ginkgo.DeferCleanup(func() { metrics.InitMetricVectors(nil) })
		fwk.StartManagerForSpec(ctx, cfg, managerSetupWithConfig(controllersCfg))

		ns = behavioral.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "tas-custom-labels-")
		topology = utiltestingapi.MakeDefaultOneLevelTopology("tas-custom-labels-topology")
		behavioral.MustCreate(ctx, k8sClient, topology)

		tasFlavor = utiltestingapi.MakeResourceFlavor("tas-custom-labels-flavor").
			NodeLabel("tas-node", "true").
			TopologyName(topology.Name).
			Obj()
		behavioral.MustCreate(ctx, k8sClient, tasFlavor)

		clusterQueue = utiltestingapi.MakeClusterQueue("tas-custom-labels-cq").
			Label("team", "platform").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas(tasFlavor.Name).
				Resource(corev1.ResourceCPU, "2").
				Obj()).
			Obj()
		behavioral.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, clusterQueue)

		localQueue = utiltestingapi.MakeLocalQueue("tas-custom-labels-lq", ns.Name).
			Label("project", "proj-a").
			ClusterQueue(clusterQueue.Name).
			Obj()
		behavioral.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, localQueue)

		nodeX1 = newNode("x1")
		nodeX2 = newNode("x2")
		integration.CreateNodesWithStatus(ctx, k8sClient, []corev1.Node{*nodeX1, *nodeX2})
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(behavioral.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, localQueue, true)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, tasFlavor, true)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, topology, true)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, nodeX1, true)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, nodeX2, true)
		gomega.Expect(forceDeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
	})

	ginkgo.It("should report eviction metrics with custom labels and record eviction event on multiple node failures", func() {
		wl := utiltestingapi.MakeWorkload("wl", ns.Name).
			PodSets(*utiltestingapi.MakePodSet("worker", 2).
				PreferredTopologyRequest(corev1.LabelHostname).
				Request(corev1.ResourceCPU, "1").
				Obj()).
			Queue(kueue.LocalQueueName(localQueue.Name)).
			Obj()
		behavioral.MustCreate(ctx, k8sClient, wl)

		ginkgo.By("waiting for the workload to be admitted across both nodes", func() {
			behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl)
		})

		ginkgo.By("deleting the first assigned node", func() {
			gomega.Expect(k8sClient.Delete(ctx, nodeX1)).Should(gomega.Succeed())
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, nodeX1, false)
			gomega.Eventually(func(g gomega.Gomega) {
				updatedWl := &kueue.Workload{}
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), updatedWl)).To(gomega.Succeed())
				g.Expect(apimeta.IsStatusConditionTrue(updatedWl.Status.Conditions, kueue.WorkloadAdmitted)).To(gomega.BeTrue())
				g.Expect(apimeta.FindStatusCondition(updatedWl.Status.Conditions, kueue.WorkloadEvicted)).To(gomega.BeNil())
				g.Expect(updatedWl.Status.UnhealthyNodes).To(gomega.Equal([]kueue.UnhealthyNode{{Name: nodeX1.Name}}))
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("deleting the second assigned node to trigger eviction", func() {
			gomega.Expect(k8sClient.Delete(ctx, nodeX2)).Should(gomega.Succeed())
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, nodeX2, false)
		})

		ginkgo.By("verifying the workload is evicted due to multiple node failures", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				updatedWl := &kueue.Workload{}
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), updatedWl)).To(gomega.Succeed())
				evictedCond := apimeta.FindStatusCondition(updatedWl.Status.Conditions, kueue.WorkloadEvicted)
				g.Expect(evictedCond).NotTo(gomega.BeNil())
				g.Expect(evictedCond.Status).To(gomega.Equal(metav1.ConditionTrue))
				g.Expect(evictedCond.Reason).To(gomega.Equal(kueue.WorkloadEvictedDueToNodeFailures))
				g.Expect(evictedCond.Message).To(gomega.ContainSubstring("multiple TAS assigned node failures"))
				g.Expect(updatedWl.Status.UnhealthyNodes).To(gomega.BeEmpty())
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		})

		ginkgo.By("verifying ClusterQueue and LocalQueue eviction metrics include custom labels", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				got, err := testutil.GetCounterMetricValue(
					metrics.EvictedWorkloadsTotal.WithLabelValues(
						clusterQueue.Name, kueue.WorkloadEvictedDueToNodeFailures, "", "", roletracker.RoleStandalone, "platform",
					),
				)
				g.Expect(err).NotTo(gomega.HaveOccurred())
				g.Expect(got).To(gomega.Equal(float64(1)))
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

			gomega.Eventually(func(g gomega.Gomega) {
				got, err := testutil.GetCounterMetricValue(
					metrics.EvictedWorkloadsOnceTotal.WithLabelValues(
						clusterQueue.Name, kueue.WorkloadEvictedDueToNodeFailures, "", "", roletracker.RoleStandalone, "platform",
					),
				)
				g.Expect(err).NotTo(gomega.HaveOccurred())
				g.Expect(got).To(gomega.Equal(float64(1)))
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

			behavioral.ExpectLQEvictedWorkloadsTotalMetric(
				localQueue, kueue.WorkloadEvictedDueToNodeFailures, "", "", 1, "proj-a",
			)
		})

		ginkgo.By("verifying the eviction event is recorded for the workload", func() {
			wantReason := kueue.WorkloadEvicted + "DueTo" + kueue.WorkloadEvictedDueToNodeFailures
			gomega.Eventually(func(g gomega.Gomega) {
				evs, err := behavioral.EventsForObject(ctx, k8sClient, client.ObjectKeyFromObject(wl))
				g.Expect(err).NotTo(gomega.HaveOccurred())
				g.Expect(slices.ContainsFunc(evs, func(ev eventsv1.Event) bool {
					return ev.Reason == wantReason && ev.Type == corev1.EventTypeNormal
				})).To(gomega.BeTrue(), "expected event with reason %q, got %+v", wantReason, evs)
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		})
	})
})
