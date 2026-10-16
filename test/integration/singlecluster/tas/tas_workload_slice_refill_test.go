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
	eventsv1 "k8s.io/api/events/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	config "sigs.k8s.io/kueue/apis/config/v1beta2"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingnode "sigs.k8s.io/kueue/pkg/util/testingjobs/node"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/pkg/workloadslicing"
	"sigs.k8s.io/kueue/test/util/behavioral"
)

var _ = ginkgo.Describe("Topology Aware Scheduling with workload slices and fair sharing refill", ginkgo.Ordered, func() {
	var (
		ns           *corev1.Namespace
		nodes        []corev1.Node
		topology     *kueue.Topology
		tasFlavor    *kueue.ResourceFlavor
		clusterQueue *kueue.ClusterQueue
		localQueue   *kueue.LocalQueue
	)

	ginkgo.BeforeAll(func() {
		fwk.StartManager(ctx, cfg, managerSetupWithConfig(&config.Configuration{
			FairSharing: &config.FairSharing{},
		}))
	})

	ginkgo.AfterAll(func() {
		fwk.StopManager(ctx)
	})

	ginkgo.BeforeEach(func() {
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.ElasticJobsViaWorkloadSlices, true)
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.ElasticJobsViaWorkloadSlicesWithTAS, true)
		features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.FairSharingRefill, true)

		ns = behavioral.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "tas-slice-refill-")

		nodes = []corev1.Node{
			*testingnode.MakeNode("slice-refill-x1").
				Label("node-group", "tas").
				Label(corev1.LabelHostname, "slice-refill-x1").
				StatusAllocatable(corev1.ResourceList{
					corev1.ResourceCPU:  resource.MustParse("4"),
					corev1.ResourcePods: resource.MustParse("10"),
				}).
				Ready().
				Obj(),
		}
		behavioral.CreateNodesWithStatus(ctx, k8sClient, nodes)

		topology = utiltestingapi.MakeDefaultOneLevelTopology("default")
		behavioral.MustCreate(ctx, k8sClient, topology)

		tasFlavor = utiltestingapi.MakeResourceFlavor("tas-flavor").
			NodeLabel("node-group", "tas").
			TopologyName("default").Obj()
		behavioral.MustCreate(ctx, k8sClient, tasFlavor)

		// Quota is above the node's capacity, so placement decides.
		clusterQueue = utiltestingapi.MakeClusterQueue("cluster-queue").
			ResourceGroup(*utiltestingapi.MakeFlavorQuotas(tasFlavor.Name).Resource(corev1.ResourceCPU, "10").Obj()).
			Obj()
		behavioral.MustCreate(ctx, k8sClient, clusterQueue)
		behavioral.ExpectClusterQueuesToBeActive(ctx, k8sClient, clusterQueue)

		localQueue = utiltestingapi.MakeLocalQueue("local-queue", ns.Name).ClusterQueue(clusterQueue.Name).Obj()
		behavioral.MustCreate(ctx, k8sClient, localQueue)
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(behavioral.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
		gomega.Expect(behavioral.DeleteObject(ctx, k8sClient, localQueue)).Should(gomega.Succeed())
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, tasFlavor, true)
		behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, topology, true)
		for _, node := range nodes {
			behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, &node, true)
		}
		gomega.Expect(forceDeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
	})

	ginkgo.It("should admit a refilled successor into the domains the replaced slice released", func() {
		tasWorkload := func(name string, pods int) *utiltestingapi.WorkloadWrapper {
			return utiltestingapi.MakeWorkload(name, ns.Name).
				Queue(kueue.LocalQueueName(localQueue.Name)).
				PodSets(*utiltestingapi.MakePodSet(kueue.DefaultPodSetName, pods).
					UnconstrainedTopologyRequest().
					Request(corev1.ResourceCPU, "1").
					Obj())
		}

		// The node must be full first: workloads admitted on arrival land in
		// different cycles, and only a successor refilled in the replacement's
		// cycle reads the topology usage the replacement leaves behind.
		var old, blocker *kueue.Workload
		ginkgo.By("filling the node with the slice to replace and a blocker", func() {
			old = tasWorkload("old", 2).
				Priority(100).
				Annotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
				Obj()
			behavioral.MustCreate(ctx, k8sClient, old)
			behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, old)

			blocker = tasWorkload("blocker", 2).Obj()
			behavioral.MustCreate(ctx, k8sClient, blocker)
			behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, blocker)
		})

		// The slice outranks the successor, so the replacement heads the cycle
		// and the successor is refilled after it.
		var grow, succ *kueue.Workload
		ginkgo.By("queueing a replacement that grows the slice from 2 to 3 and a successor asking for 1", func() {
			grow = tasWorkload("grow", 3).
				Priority(100).
				Annotation(workloadslicing.EnabledAnnotationKey, workloadslicing.EnabledAnnotationValue).
				Annotation(workloadslicing.WorkloadSliceReplacementFor, string(workload.Key(old))).
				Obj()
			behavioral.MustCreate(ctx, k8sClient, grow)
			succ = tasWorkload("succ", 1).Obj()
			behavioral.MustCreate(ctx, k8sClient, succ)
			behavioral.ExpectWorkloadsToBePending(ctx, k8sClient, grow, succ)
			behavioral.ExpectPendingWorkloadsMetric(clusterQueue, 0, 2)
		})

		// The parked event must predate freedAt, and seeing it shows that the
		// event check below can fail.
		ginkgo.By("waiting for the successor's pending event", func() {
			gomega.Eventually(func(g gomega.Gomega) {
				var events eventsv1.EventList
				g.Expect(k8sClient.List(ctx, &events, client.InNamespace(ns.Name))).To(gomega.Succeed())
				g.Expect(events.Items).To(gomega.ContainElement(gomega.Satisfy(func(e eventsv1.Event) bool {
					return e.Regarding.Name == succ.Name && e.Type == corev1.EventTypeWarning
				})))
			}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
		})
		freedAt := metav1.NowMicro()
		ginkgo.By("freeing 2 CPU in a single step", func() {
			behavioral.FinishWorkloads(ctx, k8sClient, blocker)
		})

		ginkgo.By("admitting the replacement and the successor", func() {
			behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, grow, succ)
		})

		// The successor is admitted either way once the replaced slice leaves
		// the cache; it must not first be turned away by the topology usage the
		// replacement left behind.
		ginkgo.By("checking the successor was admitted in the replacement's cycle", func() {
			gomega.Consistently(func(g gomega.Gomega) {
				var events eventsv1.EventList
				g.Expect(k8sClient.List(ctx, &events, client.InNamespace(ns.Name))).To(gomega.Succeed())
				for _, e := range events.Items {
					if e.Regarding.Name != succ.Name || e.Type != corev1.EventTypeWarning {
						continue
					}
					g.Expect(e.EventTime.Before(&freedAt)).To(gomega.BeTrue(), "successor was turned away after the free step: %s", e.Note)
					if e.Series != nil {
						g.Expect(e.Series.LastObservedTime.Before(&freedAt)).To(gomega.BeTrue(), "successor was turned away after the free step: %s", e.Note)
					}
				}
			}, behavioral.ConsistentDuration, behavioral.ShortInterval).Should(gomega.Succeed())
		})
	})
})
