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
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	"k8s.io/component-base/metrics/testutil"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/metrics"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingnode "sigs.k8s.io/kueue/pkg/util/testingjobs/node"
	testingpod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
	"sigs.k8s.io/kueue/test/util"
)

const (
	tasMetricsGPUResource = "nvidia.com/gpu"
)

// tasDomainUsageValue returns the current value of the kueue_tas_domain_usage
// gauge for the given label combination.  Returns 0 if no series exists yet.
func tasDomainUsageValue(flavor, domain, domainID, resourceName string) float64 {
	v, err := testutil.GetGaugeMetricValue(
		metrics.TASDomainUsage.WithLabelValues(flavor, domain, domainID, resourceName),
	)
	if err != nil {
		return 0
	}
	return v
}

var _ = ginkgo.Describe("TAS Domain Usage Metrics", ginkgo.Ordered, func() {
	var ns *corev1.Namespace

	ginkgo.BeforeAll(func() {
		fwk.StartManager(ctx, cfg, managerSetup())
	})

	ginkgo.AfterAll(func() {
		fwk.StopManager(ctx)
	})

	ginkgo.BeforeEach(func() {
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "tas-metrics-")
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
	})

	// ── single-level (hostname only) topology ──────────────────────────────────

	ginkgo.When("a single-level hostname topology is used", func() {
		var (
			nodes        []corev1.Node
			topology     *kueue.Topology
			tasFlavor    *kueue.ResourceFlavor
			clusterQueue *kueue.ClusterQueue
			localQueue   *kueue.LocalQueue
		)

		ginkgo.BeforeEach(func() {
			nodes = []corev1.Node{
				*testingnode.MakeNode("sl-host-1").
					Label("node-group", "tas").
					Label(corev1.LabelHostname, "sl-host-1").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:                        resource.MustParse("1"),
						corev1.ResourceName(tasMetricsGPUResource): resource.MustParse("8"),
						corev1.ResourcePods:                       resource.MustParse("16"),
					}).
					Ready().
					Obj(),
				*testingnode.MakeNode("sl-host-2").
					Label("node-group", "tas").
					Label(corev1.LabelHostname, "sl-host-2").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:                        resource.MustParse("1"),
						corev1.ResourceName(tasMetricsGPUResource): resource.MustParse("8"),
						corev1.ResourcePods:                       resource.MustParse("16"),
					}).
					Ready().
					Obj(),
			}
			util.CreateNodesWithStatus(ctx, k8sClient, nodes)

			topology = utiltestingapi.MakeDefaultOneLevelTopology("sl-topology")
			util.MustCreate(ctx, k8sClient, topology)

			tasFlavor = utiltestingapi.MakeResourceFlavor("sl-tas-flavor").
				NodeLabel("node-group", "tas").
				TopologyName("sl-topology").
				Obj()
			util.MustCreate(ctx, k8sClient, tasFlavor)

			clusterQueue = utiltestingapi.MakeClusterQueue("sl-cluster-queue").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas(tasFlavor.Name).
						Resource(corev1.ResourceName(tasMetricsGPUResource), "16").
						Obj(),
				).
				Obj()
			util.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, clusterQueue)

			localQueue = utiltestingapi.MakeLocalQueue("sl-local-queue", ns.Name).
				ClusterQueue(clusterQueue.Name).
				Obj()
			util.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, localQueue)
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			gomega.Expect(util.DeleteObject(ctx, k8sClient, localQueue)).To(gomega.Succeed())
			util.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, tasFlavor, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, topology, true)
			for i := range nodes {
				util.ExpectObjectToBeDeleted(ctx, k8sClient, &nodes[i], true)
			}
		})

		ginkgo.It("should report domain usage after workload admission", func() {
			wl := utiltestingapi.MakeWorkload("sl-wl1", ns.Name).
				Queue(kueue.LocalQueueName(localQueue.Name)).
				PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 2).
					Request(corev1.ResourceName(tasMetricsGPUResource), "4").
					RequiredTopologyRequest(corev1.LabelHostname).
					Obj()).
				Obj()
			util.MustCreate(ctx, k8sClient, wl)

			ginkgo.By("verifying workload is fully admitted (topology assigned)", func() {
				util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl)
			})

			// 2 pods × 4 GPUs = 8 GPUs reserved on whichever host was selected.
			ginkgo.By("verifying 8 GPUs of domain usage are visible in the metric", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					h1 := tasDomainUsageValue(tasFlavor.Name, corev1.LabelHostname, "sl-host-1", tasMetricsGPUResource)
					h2 := tasDomainUsageValue(tasFlavor.Name, corev1.LabelHostname, "sl-host-2", tasMetricsGPUResource)
					g.Expect(h1 + h2).To(gomega.Equal(8.0))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("should drop domain usage to zero after workload finishes", func() {
			wl := utiltestingapi.MakeWorkload("sl-wl2", ns.Name).
				Queue(kueue.LocalQueueName(localQueue.Name)).
				PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
					Request(corev1.ResourceName(tasMetricsGPUResource), "4").
					RequiredTopologyRequest(corev1.LabelHostname).
					Obj()).
				Obj()
			util.MustCreate(ctx, k8sClient, wl)

			ginkgo.By("verifying workload is fully admitted (topology assigned)", func() {
				util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl)
			})

			ginkgo.By("verifying 4 GPUs of domain usage are visible in the metric", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					h1 := tasDomainUsageValue(tasFlavor.Name, corev1.LabelHostname, "sl-host-1", tasMetricsGPUResource)
					h2 := tasDomainUsageValue(tasFlavor.Name, corev1.LabelHostname, "sl-host-2", tasMetricsGPUResource)
					g.Expect(h1 + h2).To(gomega.Equal(4.0))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.By("finishing the workload", func() {
				util.FinishWorkloads(ctx, k8sClient, wl)
			})

			ginkgo.By("verifying domain usage drops to zero after the workload is finished", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					h1 := tasDomainUsageValue(tasFlavor.Name, corev1.LabelHostname, "sl-host-1", tasMetricsGPUResource)
					h2 := tasDomainUsageValue(tasFlavor.Name, corev1.LabelHostname, "sl-host-2", tasMetricsGPUResource)
					g.Expect(h1 + h2).To(gomega.Equal(0.0))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})

		ginkgo.It("should include non-TAS pod usage in the domain metric", func() {
			// Simulate a DaemonSet pod running on sl-host-1, consuming 2 GPUs.
			// The NonTasUsageReconciler picks this up and adds it to the TAS cache.
			nonTasPod := testingpod.MakePod("nt-pod", ns.Name).
				RequestAndLimit(corev1.ResourceName(tasMetricsGPUResource), "2").
				NodeName("sl-host-1").
				Obj()
			util.MustCreate(ctx, k8sClient, nonTasPod)

			wl := utiltestingapi.MakeWorkload("sl-wl-nt", ns.Name).
				Queue(kueue.LocalQueueName(localQueue.Name)).
				PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
					Request(corev1.ResourceName(tasMetricsGPUResource), "4").
					RequiredTopologyRequest(corev1.LabelHostname).
					Obj()).
				Obj()
			util.MustCreate(ctx, k8sClient, wl)

			ginkgo.By("verifying workload is admitted", func() {
				util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl)
			})

			// Total across both hosts: 4 (TAS workload) + 2 (non-TAS pod) = 6 GPUs.
			// The non-TAS pod is on sl-host-1; the workload may land on either host.
			ginkgo.By("verifying the metric reflects both TAS and non-TAS GPU usage", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					h1 := tasDomainUsageValue(tasFlavor.Name, corev1.LabelHostname, "sl-host-1", tasMetricsGPUResource)
					h2 := tasDomainUsageValue(tasFlavor.Name, corev1.LabelHostname, "sl-host-2", tasMetricsGPUResource)
					g.Expect(h1 + h2).To(gomega.Equal(6.0))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})
		})
	})

	// ── three-level (block → rack → hostname) topology ────────────────────────

	ginkgo.When("a three-level block/rack/hostname topology is used", func() {
		var (
			nodes        []corev1.Node
			topology     *kueue.Topology
			tasFlavor    *kueue.ResourceFlavor
			clusterQueue *kueue.ClusterQueue
			localQueue   *kueue.LocalQueue
		)

		// Single node topology layout (deterministic placement):
		//   block-1
		//     rack-1  →  ml-host-1 (8 GPUs)
		ginkgo.BeforeEach(func() {
			nodes = []corev1.Node{
				*testingnode.MakeNode("ml-host-1").
					Label("node-group", "tas").
					Label(utiltesting.DefaultBlockTopologyLevel, "block-1").
					Label(utiltesting.DefaultRackTopologyLevel, "rack-1").
					Label(corev1.LabelHostname, "ml-host-1").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:                        resource.MustParse("1"),
						corev1.ResourceName(tasMetricsGPUResource): resource.MustParse("8"),
						corev1.ResourcePods:                       resource.MustParse("16"),
					}).
					Ready().
					Obj(),
			}
			util.CreateNodesWithStatus(ctx, k8sClient, nodes)

			topology = utiltestingapi.MakeDefaultThreeLevelTopology("ml-topology")
			util.MustCreate(ctx, k8sClient, topology)

			tasFlavor = utiltestingapi.MakeResourceFlavor("ml-tas-flavor").
				NodeLabel("node-group", "tas").
				TopologyName("ml-topology").
				Obj()
			util.MustCreate(ctx, k8sClient, tasFlavor)

			clusterQueue = utiltestingapi.MakeClusterQueue("ml-cluster-queue").
				ResourceGroup(
					*utiltestingapi.MakeFlavorQuotas(tasFlavor.Name).
						Resource(corev1.ResourceName(tasMetricsGPUResource), "8").
						Obj(),
				).
				Obj()
			util.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, clusterQueue)

			localQueue = utiltestingapi.MakeLocalQueue("ml-local-queue", ns.Name).
				ClusterQueue(clusterQueue.Name).
				Obj()
			util.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, localQueue)
		})

		ginkgo.AfterEach(func() {
			gomega.Expect(util.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
			gomega.Expect(util.DeleteObject(ctx, k8sClient, localQueue)).To(gomega.Succeed())
			util.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, tasFlavor, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, topology, true)
			for i := range nodes {
				util.ExpectObjectToBeDeleted(ctx, k8sClient, &nodes[i], true)
			}
		})

		ginkgo.It("should report usage at host, rack, and block levels", func() {
			// RequiredTopologyRequest at the outermost (block) level ensures the
			// full-path domain ID ("block-1,rack-1,ml-host-1") is stored in the
			// TAS cache, enabling correct rack and block rollup assertions.
			wl := utiltestingapi.MakeWorkload("ml-wl1", ns.Name).
				Queue(kueue.LocalQueueName(localQueue.Name)).
				PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, 1).
					Request(corev1.ResourceName(tasMetricsGPUResource), "4").
					RequiredTopologyRequest(utiltesting.DefaultBlockTopologyLevel).
					Obj()).
				Obj()
			util.MustCreate(ctx, k8sClient, wl)

			ginkgo.By("verifying workload is fully admitted (topology assigned)", func() {
				util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl)
			})

			ginkgo.By("verifying host-level domain usage", func() {
				util.ExpectTASDomainUsageMetric(tasFlavor.Name, corev1.LabelHostname, "block-1,rack-1,ml-host-1", tasMetricsGPUResource, 4)
			})

			ginkgo.By("verifying rack-level domain usage (aggregate of hosts in rack-1)", func() {
				util.ExpectTASDomainUsageMetric(tasFlavor.Name, utiltesting.DefaultRackTopologyLevel, "block-1,rack-1", tasMetricsGPUResource, 4)
			})

			ginkgo.By("verifying block-level domain usage (aggregate of all racks in block-1)", func() {
				util.ExpectTASDomainUsageMetric(tasFlavor.Name, utiltesting.DefaultBlockTopologyLevel, "block-1", tasMetricsGPUResource, 4)
			})
		})
	})
})
