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
	"fmt"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
	utiltas "sigs.k8s.io/kueue/pkg/util/tas"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingnode "sigs.k8s.io/kueue/pkg/util/testingjobs/node"
	testingpod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/test/util/behavioral"
	"sigs.k8s.io/kueue/test/util/behavioral/integration"
)

var _ = ginkgo.Describe("Topology Aware Scheduling", func() {
	var (
		ns *corev1.Namespace
	)

	ginkgo.BeforeEach(func() {
		fwk.StartManagerForSpec(ctx, cfg, managerSetup())
		ns = behavioral.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "tas-")
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(forceDeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
	})

	ginkgo.When("Single TAS Resource Flavor", func() {
		var (
			tasFlavor    *kueue.ResourceFlavor
			topology     *kueue.Topology
			localQueue   *kueue.LocalQueue
			clusterQueue *kueue.ClusterQueue
		)

		ginkgo.Context("TAS features, preemption and provisioning", func() {
			ginkgo.When("Preemption is enabled within ClusterQueue", func() {
				var (
					nodes           []corev1.Node
					alternateFlavor *kueue.ResourceFlavor
				)
				ginkgo.BeforeEach(func() {
					alternateFlavor = nil
					//      b1
					//   /      \
					//  r1       r2
					//  |         |
					//  x2       x1
					nodes = []corev1.Node{
						*testingnode.MakeNode("x2").
							Label("node-group", "tas").
							Label(utiltesting.DefaultBlockTopologyLevel, "b1").
							Label(utiltesting.DefaultRackTopologyLevel, "r1").
							Label(corev1.LabelHostname, "x2").
							StatusAllocatable(corev1.ResourceList{
								corev1.ResourceCPU:    resource.MustParse("5"),
								corev1.ResourceMemory: resource.MustParse("5Gi"),
								corev1.ResourcePods:   resource.MustParse("10"),
							}).
							Ready().
							Obj(),
						*testingnode.MakeNode("x1").
							Label("node-group", "tas").
							Label(utiltesting.DefaultBlockTopologyLevel, "b1").
							Label(utiltesting.DefaultRackTopologyLevel, "r2").
							Label(corev1.LabelHostname, "x1").
							StatusAllocatable(corev1.ResourceList{
								corev1.ResourceCPU:    resource.MustParse("5"),
								corev1.ResourceMemory: resource.MustParse("5Gi"),
								corev1.ResourcePods:   resource.MustParse("10"),
							}).
							Ready().
							Obj(),
					}
					integration.CreateNodesWithStatus(ctx, k8sClient, nodes)

					topology = utiltestingapi.MakeDefaultThreeLevelTopology("default")
					behavioral.MustCreate(ctx, k8sClient, topology)

					tasFlavor = utiltestingapi.MakeResourceFlavor("tas-flavor").
						NodeLabel("node-group", "tas").
						TopologyName("default").Obj()
					behavioral.MustCreate(ctx, k8sClient, tasFlavor)

					clusterQueue = utiltestingapi.MakeClusterQueue("cluster-queue").
						Preemption(kueue.ClusterQueuePreemption{
							WithinClusterQueue: kueue.PreemptionPolicyLowerPriority,
						}).
						// We set the quota above what is physically available to meet
						// the topology requirements rather than the quota.
						ResourceGroup(*utiltestingapi.MakeFlavorQuotas(tasFlavor.Name).
							Resource(corev1.ResourceCPU, "20").
							Resource(corev1.ResourceMemory, "20Gi").Obj()).
						Obj()
					behavioral.MustCreate(ctx, k8sClient, clusterQueue)
					behavioral.ExpectClusterQueuesToBeActive(ctx, k8sClient, clusterQueue)

					localQueue = utiltestingapi.MakeLocalQueue("local-queue", ns.Name).ClusterQueue(clusterQueue.Name).Obj()
					behavioral.MustCreate(ctx, k8sClient, localQueue)
				})

				ginkgo.AfterEach(func() {
					gomega.Expect(behavioral.DeleteAllJobsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
					gomega.Expect(behavioral.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
					gomega.Expect(behavioral.DeleteObject(ctx, k8sClient, localQueue)).Should(gomega.Succeed())
					behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
					behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, tasFlavor, true)
					behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, alternateFlavor, true)
					behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, topology, true)
					for _, node := range nodes {
						behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, &node, true)
					}
				})

				ginkgo.It("should preempt the low and mid priority workloads to fit the high-priority workload", func() {
					var wl1, wl2, wl3 *kueue.Workload
					ginkgo.By("creating a low priority workload which can fit", func() {
						wl1 = utiltestingapi.MakeWorkload("wl1", ns.Name).
							Priority(1).
							PodSets(*utiltestingapi.MakePodSet("worker", 1).
								PreferredTopologyRequest(utiltesting.DefaultBlockTopologyLevel).
								Obj()).
							Queue(kueue.LocalQueueName(localQueue.Name)).Request(corev1.ResourceCPU, "5").Obj()
						behavioral.MustCreate(ctx, k8sClient, wl1)
					})

					ginkgo.By("verify the workload is admitted", func() {
						behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1)
						behavioral.ExpectAdmittedWorkloadsTotalMetric(clusterQueue, "", 1)
					})

					ginkgo.By("creating a mid priority workload which can fit", func() {
						wl2 = utiltestingapi.MakeWorkload("wl2", ns.Name).
							Priority(2).
							PodSets(*utiltestingapi.MakePodSet("worker", 1).
								PreferredTopologyRequest(utiltesting.DefaultBlockTopologyLevel).
								Obj()).
							Queue(kueue.LocalQueueName(localQueue.Name)).Request(corev1.ResourceCPU, "5").Obj()
						behavioral.MustCreate(ctx, k8sClient, wl2)
					})

					ginkgo.By("verify the wl2 gets admitted", func() {
						behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1, wl2)
						behavioral.ExpectAdmittedWorkloadsTotalMetric(clusterQueue, "", 2)
					})

					ginkgo.By("creating a high priority workload which requires preemption", func() {
						wl3 = utiltestingapi.MakeWorkload("wl3", ns.Name).
							Priority(3).
							PodSets(*utiltestingapi.MakePodSet("worker", 2).
								PreferredTopologyRequest(utiltesting.DefaultBlockTopologyLevel).
								Obj()).
							Queue(kueue.LocalQueueName(localQueue.Name)).Request(corev1.ResourceCPU, "5").Obj()
						behavioral.MustCreate(ctx, k8sClient, wl3)
					})

					ginkgo.By("verify the wl3 gets admitted while wl1 is preempted", func() {
						behavioral.ExpectWorkloadsToBePreempted(ctx, k8sClient, wl1, wl2)
						behavioral.FinishEvictionForWorkloads(ctx, k8sClient, wl1, wl2)
						behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl3)
						behavioral.ExpectReservingActiveWorkloadsMetric(clusterQueue, 1)
					})
				})

				ginkgo.It("explores an alternate flavor while residual Pods occupy the preferred flavor", func() {
					features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.FlavorFungibility, true)
					alternateNode := testingnode.MakeNode("alternate").Label("node-group", "alternate").
						Label(utiltesting.DefaultBlockTopologyLevel, "b2").Label(utiltesting.DefaultRackTopologyLevel, "r1").
						Label(corev1.LabelHostname, "alternate").StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU: resource.MustParse("10"), corev1.ResourcePods: resource.MustParse("10"),
					}).Ready().Obj()
					nodes = append(nodes, *alternateNode)
					integration.CreateNodesWithStatus(ctx, k8sClient, []corev1.Node{*alternateNode})
					alternateFlavor = utiltestingapi.MakeResourceFlavor("alternate").
						NodeLabel("node-group", "alternate").TopologyName(topology.Name).Obj()
					behavioral.MustCreate(ctx, k8sClient, alternateFlavor)

					residual := testingpod.MakePod("released-bound-pod", ns.Name).NodeName("x1").
						TerminationGracePeriod(600).Request(corev1.ResourceCPU, "5").
						Annotation(kueue.WorkloadAnnotation, "released").
						Annotation(kueue.PodSetUnconstrainedTopologyAnnotation, "true").Obj()
					behavioral.MustCreate(ctx, k8sClient, residual)
					integration.SetPodsPhase(ctx, k8sClient, corev1.PodRunning, residual)
					gomega.Expect(k8sClient.Delete(ctx, residual)).To(gomega.Succeed())
					gomega.Eventually(func(g gomega.Gomega) {
						snapshot, err := schedulerCache.Snapshot(ctx)
						g.Expect(err).NotTo(gomega.HaveOccurred())
						cq := snapshot.ClusterQueue(kueue.ClusterQueueReference(clusterQueue.Name))
						g.Expect(cq).NotTo(gomega.BeNil())
						flavor := cq.TASFlavors[kueue.ResourceFlavorReference(tasFlavor.Name)]
						g.Expect(flavor).NotTo(gomega.BeNil())
						capacity, err := flavor.SerializeFreeCapacityPerDomain()
						g.Expect(err).NotTo(gomega.HaveOccurred())
						g.Expect(capacity).To(gomega.MatchJSON(`{
							"x1": {"freeCapacity": {"cpu": "0", "memory": "5Gi", "pods": "9"}, "tasUsage": {}},
							"x2": {"freeCapacity": {"cpu": "5", "memory": "5Gi", "pods": "10"}, "tasUsage": {}}
						}`))
					}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
					incoming := utiltestingapi.MakeWorkload("incoming", ns.Name).
						Queue(kueue.LocalQueueName(localQueue.Name)).
						PodSets(*utiltestingapi.MakePodSet("worker", 2).UnconstrainedTopologyRequest().
							Request(corev1.ResourceCPU, "5").Obj()).Obj()
					behavioral.MustCreate(ctx, k8sClient, incoming)
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(incoming), incoming)).To(gomega.Succeed())
						g.Expect(workload.IsAdmitted(incoming)).To(gomega.BeFalse())
						condition := apimeta.FindStatusCondition(incoming.Status.Conditions, kueue.WorkloadQuotaReserved)
						g.Expect(condition).NotTo(gomega.BeNil())
						g.Expect(condition.Message).To(gomega.ContainSubstring("bound Pods from released TAS reservations"))
					}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

					ginkgo.By("making an alternate flavor available while the preferred flavor stays occupied")
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(clusterQueue), clusterQueue)).To(gomega.Succeed())
						clusterQueue.Spec.ResourceGroups[0].Flavors = append(clusterQueue.Spec.ResourceGroups[0].Flavors,
							*utiltestingapi.MakeFlavorQuotas(alternateFlavor.Name).
								Resource(corev1.ResourceCPU, "20").Resource(corev1.ResourceMemory, "20Gi").Obj())
						g.Expect(k8sClient.Update(ctx, clusterQueue)).To(gomega.Succeed())
					}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
					behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, incoming)
					gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(incoming), incoming)).To(gomega.Succeed())
					gomega.Expect(incoming.Status.Admission.PodSetAssignments[0].Flavors[corev1.ResourceCPU]).To(gomega.Equal(kueue.ResourceFlavorReference(alternateFlavor.Name)))
					gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(residual), residual)).To(gomega.Succeed())
					gomega.Expect(residual.DeletionTimestamp).NotTo(gomega.BeNil())
				})

				ginkgo.It("waits for a terminating preemption victim without preempting another Workload", func() {
					makeWorkload := func(name string, priority int32) *kueue.Workload {
						return utiltestingapi.MakeWorkload(name, ns.Name).
							Priority(priority).
							PodSets(*utiltestingapi.MakePodSet("worker", 1).
								RequiredTopologyRequest(corev1.LabelHostname).Request(corev1.ResourceCPU, "5").Obj()).
							Queue(kueue.LocalQueueName(localQueue.Name)).Obj()
					}
					low := makeWorkload("low", 1)
					mid := makeWorkload("mid", 2)
					behavioral.MustCreate(ctx, k8sClient, low)
					behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, low)
					behavioral.MustCreate(ctx, k8sClient, mid)
					behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, low, mid)
					gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(low), low)).To(gomega.Succeed())
					lowNode := utiltas.InternalFrom(low.Status.Admission.PodSetAssignments[0].TopologyAssignment).Domains[0].Values[0]

					const holdFinalizer = "kueue.x-k8s.io/residual-preemption-test"
					lowPod := testingpod.MakePod("low-bound-pod", ns.Name).
						NodeName(lowNode).StatusPhase(corev1.PodRunning).
						TerminationGracePeriod(0).
						Request(corev1.ResourceCPU, "5").
						Annotation(kueue.WorkloadAnnotation, low.Name).
						Annotation(kueue.PodSetUnconstrainedTopologyAnnotation, "true").
						Finalizer(holdFinalizer).Obj()
					behavioral.MustCreate(ctx, k8sClient, lowPod)
					integration.SetPodsPhase(ctx, k8sClient, corev1.PodRunning, lowPod)
					ginkgo.DeferCleanup(func() {
						pod := &corev1.Pod{}
						if k8sClient.Get(ctx, client.ObjectKeyFromObject(lowPod), pod) == nil && len(pod.Finalizers) != 0 {
							pod.Finalizers = nil
							_ = k8sClient.Update(ctx, pod)
						}
					})

					high := makeWorkload("high", 3)
					behavioral.MustCreate(ctx, k8sClient, high)
					behavioral.ExpectWorkloadsToBePreempted(ctx, k8sClient, low)
					// Quota release can precede the Pod's deletion event. The cached
					// eviction intent must already prevent choosing another victim.
					behavioral.FinishEvictionForWorkloads(ctx, k8sClient, low)
					gomega.Consistently(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(mid), mid)).To(gomega.Succeed())
						g.Expect(apimeta.IsStatusConditionTrue(mid.Status.Conditions, kueue.WorkloadEvicted)).To(gomega.BeFalse())
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(high), high)).To(gomega.Succeed())
						g.Expect(workload.IsAdmitted(high)).To(gomega.BeFalse())
					}, behavioral.LongConsistentDuration, behavioral.ShortInterval).Should(gomega.Succeed())
					gomega.Expect(k8sClient.Delete(ctx, lowPod)).To(gomega.Succeed())
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(lowPod), lowPod)).To(gomega.Succeed())
						g.Expect(lowPod.DeletionTimestamp).NotTo(gomega.BeNil())
					}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
					gomega.Consistently(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(mid), mid)).To(gomega.Succeed())
						g.Expect(workload.IsAdmitted(mid)).To(gomega.BeTrue())
						g.Expect(apimeta.IsStatusConditionTrue(mid.Status.Conditions, kueue.WorkloadEvicted)).To(gomega.BeFalse())
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(high), high)).To(gomega.Succeed())
						g.Expect(workload.IsAdmitted(high)).To(gomega.BeFalse())
					}, behavioral.LongConsistentDuration, behavioral.ShortInterval).Should(gomega.Succeed())
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(lowPod), lowPod)).To(gomega.Succeed())
						lowPod.Finalizers = nil
						g.Expect(k8sClient.Update(ctx, lowPod)).To(gomega.Succeed())
					}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
					behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, lowPod, true)
					behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, mid, high)
				})

				ginkgo.It("waits for a deactivated bound Pod without preempting a healthy Workload", func() {
					old := utiltestingapi.MakeWorkload("deactivated", ns.Name).Priority(1).
						Queue(kueue.LocalQueueName(localQueue.Name)).
						PodSets(*utiltestingapi.MakePodSet("worker", 1).
							RequiredTopologyRequest(corev1.LabelHostname).Request(corev1.ResourceCPU, "5").Obj()).Obj()
					healthy := utiltestingapi.MakeWorkload("healthy", ns.Name).Priority(2).
						Queue(kueue.LocalQueueName(localQueue.Name)).
						PodSets(*utiltestingapi.MakePodSet("worker", 1).
							RequiredTopologyRequest(corev1.LabelHostname).Request(corev1.ResourceCPU, "5").Obj()).Obj()
					for _, wl := range []*kueue.Workload{old, healthy} {
						behavioral.MustCreate(ctx, k8sClient, wl)
						behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl)
					}
					gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(old), old)).To(gomega.Succeed())
					node := utiltas.InternalFrom(old.Status.Admission.PodSetAssignments[0].TopologyAssignment).Domains[0].Values[0]
					pod := testingpod.MakePod("deactivated-bound-pod", ns.Name).NodeName(node).
						TerminationGracePeriod(0).Request(corev1.ResourceCPU, "5").
						Annotation(kueue.WorkloadAnnotation, old.Name).
						Annotation(kueue.PodSetUnconstrainedTopologyAnnotation, "true").Obj()
					behavioral.MustCreate(ctx, k8sClient, pod)
					integration.SetPodsPhase(ctx, k8sClient, corev1.PodRunning, pod)
					ginkgo.DeferCleanup(func() {
						_ = client.IgnoreNotFound(k8sClient.Delete(ctx, pod, client.GracePeriodSeconds(0)))
					})

					ginkgo.By("deactivating the Workload while its Pod is still running and not deleting")
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(old), old)).To(gomega.Succeed())
						old.Spec.Active = new(false)
						g.Expect(k8sClient.Update(ctx, old)).To(gomega.Succeed())
					}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
					gomega.Eventually(func(g gomega.Gomega) {
						snapshot, err := schedulerCache.Snapshot(ctx)
						g.Expect(err).NotTo(gomega.HaveOccurred())
						cq := snapshot.ClusterQueue(kueue.ClusterQueueReference(clusterQueue.Name))
						g.Expect(cq).NotTo(gomega.BeNil())
						g.Expect(cq.Workloads).NotTo(gomega.HaveKey(workload.Key(old)))
						flavor := cq.TASFlavors[kueue.ResourceFlavorReference(tasFlavor.Name)]
						g.Expect(flavor).NotTo(gomega.BeNil())
						capacity, err := flavor.SerializeFreeCapacityPerDomain()
						g.Expect(err).NotTo(gomega.HaveOccurred())
						g.Expect(capacity).To(gomega.MatchJSON(fmt.Sprintf(`{
							%q: {"freeCapacity": {"cpu": "0", "memory": "5Gi", "pods": "9"}, "tasUsage": {"cpu": "0", "pods": "0"}},
							%q: {"freeCapacity": {"cpu": "5", "memory": "5Gi", "pods": "10"}, "tasUsage": {"cpu": "5", "pods": "1"}}
						}`, node, map[string]string{"x1": "x2", "x2": "x1"}[node])))
					}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

					incoming := utiltestingapi.MakeWorkload("incoming", ns.Name).Priority(3).
						Queue(kueue.LocalQueueName(localQueue.Name)).
						PodSets(*utiltestingapi.MakePodSet("worker", 1).
							RequiredTopologyRequest(corev1.LabelHostname).Request(corev1.ResourceCPU, "5").Obj()).Obj()
					behavioral.MustCreate(ctx, k8sClient, incoming)
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(incoming), incoming)).To(gomega.Succeed())
						condition := apimeta.FindStatusCondition(incoming.Status.Conditions, kueue.WorkloadQuotaReserved)
						g.Expect(condition).NotTo(gomega.BeNil())
						g.Expect(condition.Message).To(gomega.ContainSubstring("bound Pods from released TAS reservations"))
					}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
					gomega.Consistently(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(healthy), healthy)).To(gomega.Succeed())
						g.Expect(workload.IsAdmitted(healthy)).To(gomega.BeTrue())
						g.Expect(apimeta.IsStatusConditionTrue(healthy.Status.Conditions, kueue.WorkloadEvicted)).To(gomega.BeFalse())
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(incoming), incoming)).To(gomega.Succeed())
						g.Expect(workload.IsAdmitted(incoming)).To(gomega.BeFalse())
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(pod), pod)).To(gomega.Succeed())
						g.Expect(pod.DeletionTimestamp).To(gomega.BeNil())
					}, behavioral.LongConsistentDuration, behavioral.ShortInterval).Should(gomega.Succeed())

					ginkgo.By("waking scheduling only after the old Pod physically leaves")
					behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, pod, true)
					behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, incoming, healthy)
				})

				for name, tc := range map[string]struct {
					releaseOrder []string
					staged       bool
				}{
					"low releases quota before mid":       {releaseOrder: []string{"low", "mid"}, staged: true},
					"mid releases quota before low":       {releaseOrder: []string{"mid", "low"}, staged: true},
					"victims release quota consecutively": {releaseOrder: []string{"low", "mid"}},
				} {
					ginkgo.It("waits for multiple preemption victims without evicting another Workload when "+name, func() {
						thirdNode := testingnode.MakeNode("x3").Label("node-group", "tas").
							Label(utiltesting.DefaultBlockTopologyLevel, "b1").Label(utiltesting.DefaultRackTopologyLevel, "r3").
							Label(corev1.LabelHostname, "x3").StatusAllocatable(corev1.ResourceList{
							corev1.ResourceCPU: resource.MustParse("5"), corev1.ResourcePods: resource.MustParse("10"),
						}).Ready().Obj()
						nodes = append(nodes, *thirdNode)
						integration.CreateNodesWithStatus(ctx, k8sClient, []corev1.Node{*thirdNode})
						gomega.Eventually(func(g gomega.Gomega) {
							g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(clusterQueue), clusterQueue)).To(gomega.Succeed())
							for i := range clusterQueue.Spec.ResourceGroups[0].Flavors[0].Resources {
								quota := &clusterQueue.Spec.ResourceGroups[0].Flavors[0].Resources[i]
								if quota.Name == corev1.ResourceCPU {
									quota.NominalQuota = resource.MustParse("15")
								}
							}
							g.Expect(k8sClient.Update(ctx, clusterQueue)).To(gomega.Succeed())
						}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
						makeWorkload := func(name string, priority int32, count int) *kueue.Workload {
							return utiltestingapi.MakeWorkload(name, ns.Name).Priority(priority).
								PodSets(*utiltestingapi.MakePodSet("worker", count).
									UnconstrainedTopologyRequest().Request(corev1.ResourceCPU, "5").Obj()).
								Queue(kueue.LocalQueueName(localQueue.Name)).Obj()
						}
						low, mid, other := makeWorkload("low", 1, 1), makeWorkload("mid", 2, 1), makeWorkload("other", 3, 1)
						victims := map[string]*kueue.Workload{"low": low, "mid": mid}
						for _, wl := range []*kueue.Workload{low, mid, other} {
							behavioral.MustCreate(ctx, k8sClient, wl)
							behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl)
						}
						var victimPods []*corev1.Pod
						for _, wl := range []*kueue.Workload{low, mid} {
							gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), wl)).To(gomega.Succeed())
							node := utiltas.InternalFrom(wl.Status.Admission.PodSetAssignments[0].TopologyAssignment).Domains[0].Values[0]
							pod := testingpod.MakePod(wl.Name+"-bound-pod", ns.Name).NodeName(node).
								TerminationGracePeriod(0).Request(corev1.ResourceCPU, "5").
								Annotation(kueue.WorkloadAnnotation, wl.Name).
								Annotation(kueue.PodSetUnconstrainedTopologyAnnotation, "true").Obj()
							behavioral.MustCreate(ctx, k8sClient, pod)
							integration.SetPodsPhase(ctx, k8sClient, corev1.PodRunning, pod)
							victimPods = append(victimPods, pod)
							ginkgo.DeferCleanup(func() {
								_ = client.IgnoreNotFound(k8sClient.Delete(ctx, pod, client.GracePeriodSeconds(0)))
							})
						}
						high := makeWorkload("high", 4, 2)
						behavioral.MustCreate(ctx, k8sClient, high)
						behavioral.ExpectWorkloadsToBePreempted(ctx, k8sClient, low, mid)
						assertWaiting := func(g gomega.Gomega) {
							g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(other), other)).To(gomega.Succeed())
							g.Expect(workload.IsAdmitted(other)).To(gomega.BeTrue())
							g.Expect(apimeta.IsStatusConditionTrue(other.Status.Conditions, kueue.WorkloadEvicted)).To(gomega.BeFalse())
							g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(high), high)).To(gomega.Succeed())
							g.Expect(workload.IsAdmitted(high)).To(gomega.BeFalse())
						}
						for _, name := range tc.releaseOrder {
							wl := victims[name]
							behavioral.FinishEvictionForWorkloads(ctx, k8sClient, wl)
							gomega.Eventually(func(g gomega.Gomega) {
								g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), wl)).To(gomega.Succeed())
								g.Expect(workload.HasQuotaReservation(wl)).To(gomega.BeFalse())
							}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
							if tc.staged {
								gomega.Consistently(assertWaiting, behavioral.LongConsistentDuration, behavioral.ShortInterval).Should(gomega.Succeed())
							}
						}
						gomega.Consistently(assertWaiting, behavioral.LongConsistentDuration, behavioral.ShortInterval).Should(gomega.Succeed())
						behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, victimPods[0], true)
						gomega.Consistently(assertWaiting, behavioral.LongConsistentDuration, behavioral.ShortInterval).Should(gomega.Succeed())
						behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, victimPods[1], true)
						behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, other, high)
					})
				}

				ginkgo.It("retains slice-only eviction intent after the victim Workload is deleted", func() {
					ownerRef := metav1.OwnerReference{
						APIVersion: batchv1.SchemeGroupVersion.String(), Kind: "Job",
						Name: "owner", UID: "owner-uid", Controller: new(true),
					}
					makeWorkload := func(name string, priority int32) *kueue.Workload {
						return utiltestingapi.MakeWorkload(name, ns.Name).Priority(priority).
							PodSets(*utiltestingapi.MakePodSet("worker", 1).
								RequiredTopologyRequest(corev1.LabelHostname).Request(corev1.ResourceCPU, "5").Obj()).
							Queue(kueue.LocalQueueName(localQueue.Name)).Obj()
					}
					victim, other := makeWorkload("victim", 1), makeWorkload("other", 2)
					victim.OwnerReferences = []metav1.OwnerReference{ownerRef}
					victim.Annotations = map[string]string{kueue.WorkloadSliceNameAnnotation: "origin"}
					behavioral.MustCreate(ctx, k8sClient, victim)
					behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, victim)
					behavioral.MustCreate(ctx, k8sClient, other)
					behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, other)
					gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(victim), victim)).To(gomega.Succeed())
					node := utiltas.InternalFrom(victim.Status.Admission.PodSetAssignments[0].TopologyAssignment).Domains[0].Values[0]
					pod := testingpod.MakePod("predecessor-bound-pod", ns.Name).NodeName(node).
						TerminationGracePeriod(0).Request(corev1.ResourceCPU, "5").
						Annotation(kueue.WorkloadAnnotation, "earlier-slice").
						Annotation(kueue.WorkloadSliceNameAnnotation, "origin").
						Annotation(kueue.PodSetUnconstrainedTopologyAnnotation, "true").Obj()
					pod.OwnerReferences = []metav1.OwnerReference{ownerRef}
					behavioral.MustCreate(ctx, k8sClient, pod)
					integration.SetPodsPhase(ctx, k8sClient, corev1.PodRunning, pod)
					ginkgo.DeferCleanup(func() {
						_ = client.IgnoreNotFound(k8sClient.Delete(ctx, pod, client.GracePeriodSeconds(0)))
					})
					high := makeWorkload("high", 3)
					behavioral.MustCreate(ctx, k8sClient, high)
					behavioral.ExpectWorkloadsToBePreempted(ctx, k8sClient, victim)
					behavioral.FinishEvictionForWorkloads(ctx, k8sClient, victim)
					assertWaiting := func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(other), other)).To(gomega.Succeed())
						g.Expect(workload.IsAdmitted(other)).To(gomega.BeTrue())
						g.Expect(apimeta.IsStatusConditionTrue(other.Status.Conditions, kueue.WorkloadEvicted)).To(gomega.BeFalse())
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(high), high)).To(gomega.Succeed())
						g.Expect(workload.IsAdmitted(high)).To(gomega.BeFalse())
						condition := apimeta.FindStatusCondition(high.Status.Conditions, kueue.WorkloadQuotaReserved)
						g.Expect(condition).NotTo(gomega.BeNil())
						g.Expect(condition.Message).To(gomega.ContainSubstring("bound Pods from released TAS reservations"))
					}
					gomega.Eventually(assertWaiting, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
					ginkgo.By("freezing the exact predecessor Pod identity when the released slice disappears")
					behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, victim, true)
					gomega.Consistently(assertWaiting, behavioral.LongConsistentDuration, behavioral.ShortInterval).Should(gomega.Succeed())
					behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, pod, true)
					behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, other, high)
				})

				ginkgo.It("waits for its own old bound Pod on requeue without preempting another Workload", func() {
					// This suite runs the scheduler and core/TAS controllers, not
					// the Job integration. Use the same synthetic Job identity for
					// the Workload and its old Pod, as other ownership tests do.
					ownerRef := metav1.OwnerReference{
						APIVersion: batchv1.SchemeGroupVersion.String(), Kind: "Job",
						Name: "owner", UID: "owner-uid", Controller: new(true),
					}
					makeWorkload := func(name string, priority int32) *kueue.Workload {
						return utiltestingapi.MakeWorkload(name, ns.Name).Priority(priority).
							PodSets(*utiltestingapi.MakePodSet("worker", 1).
								RequiredTopologyRequest(corev1.LabelHostname).Request(corev1.ResourceCPU, "5").Obj()).
							Queue(kueue.LocalQueueName(localQueue.Name)).Obj()
					}
					requeued := makeWorkload("requeued", 3)
					requeued.OwnerReferences = []metav1.OwnerReference{ownerRef}
					mid := makeWorkload("mid", 2)
					behavioral.MustCreate(ctx, k8sClient, requeued)
					behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, requeued)
					behavioral.MustCreate(ctx, k8sClient, mid)
					behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, mid)
					gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(requeued), requeued)).To(gomega.Succeed())
					node := utiltas.InternalFrom(requeued.Status.Admission.PodSetAssignments[0].TopologyAssignment).Domains[0].Values[0]
					oldPod := testingpod.MakePod("old-bound-pod", ns.Name).
						NodeName(node).TerminationGracePeriod(0).Request(corev1.ResourceCPU, "5").
						Annotation(kueue.WorkloadAnnotation, requeued.Name).
						Annotation(kueue.PodSetUnconstrainedTopologyAnnotation, "true").Obj()
					oldPod.OwnerReferences = []metav1.OwnerReference{ownerRef}
					behavioral.MustCreate(ctx, k8sClient, oldPod)
					integration.SetPodsPhase(ctx, k8sClient, corev1.PodRunning, oldPod)
					ginkgo.DeferCleanup(func() {
						_ = client.IgnoreNotFound(k8sClient.Delete(ctx, oldPod, client.GracePeriodSeconds(0)))
					})

					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(requeued), requeued)).To(gomega.Succeed())
						requeued.Spec.Active = new(false)
						g.Expect(k8sClient.Update(ctx, requeued)).To(gomega.Succeed())
					}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
					behavioral.FinishEvictionForWorkloads(ctx, k8sClient, requeued)
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(requeued), requeued)).To(gomega.Succeed())
						g.Expect(workload.HasQuotaReservation(requeued)).To(gomega.BeFalse())
					}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(requeued), requeued)).To(gomega.Succeed())
						requeued.Spec.Active = new(true)
						g.Expect(k8sClient.Update(ctx, requeued)).To(gomega.Succeed())
					}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
					gomega.Consistently(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(mid), mid)).To(gomega.Succeed())
						g.Expect(workload.IsAdmitted(mid)).To(gomega.BeTrue())
						g.Expect(apimeta.IsStatusConditionTrue(mid.Status.Conditions, kueue.WorkloadEvicted)).To(gomega.BeFalse())
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(requeued), requeued)).To(gomega.Succeed())
						g.Expect(workload.IsAdmitted(requeued)).To(gomega.BeFalse())
					}, behavioral.LongConsistentDuration, behavioral.ShortInterval).Should(gomega.Succeed())
					behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, oldPod, true)
					behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, mid, requeued)
				})
			})

			ginkgo.When("Preemption is enabled within Cohort", func() {
				var (
					nodes         []corev1.Node
					localQueueB   *kueue.LocalQueue
					clusterQueueB *kueue.ClusterQueue
				)
				ginkgo.BeforeEach(func() {
					//      b1
					//   /      \
					//  r1       r2
					//  |         |
					//  x2       x1
					nodes = []corev1.Node{
						*testingnode.MakeNode("x2").
							Label("node-group", "tas").
							Label(utiltesting.DefaultBlockTopologyLevel, "b1").
							Label(utiltesting.DefaultRackTopologyLevel, "r1").
							Label(corev1.LabelHostname, "x2").
							StatusAllocatable(corev1.ResourceList{
								corev1.ResourceCPU:    resource.MustParse("5"),
								corev1.ResourceMemory: resource.MustParse("5Gi"),
								corev1.ResourcePods:   resource.MustParse("10"),
							}).
							Ready().
							Obj(),
						*testingnode.MakeNode("x1").
							Label("node-group", "tas").
							Label(utiltesting.DefaultBlockTopologyLevel, "b1").
							Label(utiltesting.DefaultRackTopologyLevel, "r2").
							Label(corev1.LabelHostname, "x1").
							StatusAllocatable(corev1.ResourceList{
								corev1.ResourceCPU:    resource.MustParse("5"),
								corev1.ResourceMemory: resource.MustParse("5Gi"),
								corev1.ResourcePods:   resource.MustParse("10"),
							}).
							Ready().
							Obj(),
					}
					integration.CreateNodesWithStatus(ctx, k8sClient, nodes)

					topology = utiltestingapi.MakeDefaultThreeLevelTopology("default")
					behavioral.MustCreate(ctx, k8sClient, topology)

					tasFlavor = utiltestingapi.MakeResourceFlavor("tas-flavor").
						NodeLabel("node-group", "tas").
						TopologyName("default").Obj()
					behavioral.MustCreate(ctx, k8sClient, tasFlavor)

					clusterQueue = utiltestingapi.MakeClusterQueue("cluster-queue").
						Cohort("cohort").
						Preemption(kueue.ClusterQueuePreemption{
							WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
							ReclaimWithinCohort: kueue.PreemptionPolicyAny,
						}).
						ResourceGroup(*utiltestingapi.MakeFlavorQuotas(tasFlavor.Name).
							Resource(corev1.ResourceCPU, "5").
							Resource(corev1.ResourceMemory, "5Gi").Obj()).
						Obj()
					behavioral.MustCreate(ctx, k8sClient, clusterQueue)
					behavioral.ExpectClusterQueuesToBeActive(ctx, k8sClient, clusterQueue)

					localQueue = utiltestingapi.MakeLocalQueue("local-queue", ns.Name).ClusterQueue(clusterQueue.Name).Obj()
					behavioral.MustCreate(ctx, k8sClient, localQueue)

					clusterQueueB = utiltestingapi.MakeClusterQueue("cluster-queue-b").
						Cohort("cohort").
						Preemption(kueue.ClusterQueuePreemption{
							WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
							ReclaimWithinCohort: kueue.PreemptionPolicyAny,
						}).
						ResourceGroup(*utiltestingapi.MakeFlavorQuotas(tasFlavor.Name).
							Resource(corev1.ResourceCPU, "5").
							Resource(corev1.ResourceMemory, "5Gi").Obj()).
						Obj()
					behavioral.MustCreate(ctx, k8sClient, clusterQueueB)
					behavioral.ExpectClusterQueuesToBeActive(ctx, k8sClient, clusterQueueB)

					localQueueB = utiltestingapi.MakeLocalQueue("local-queue-b", ns.Name).ClusterQueue(clusterQueueB.Name).Obj()
					behavioral.MustCreate(ctx, k8sClient, localQueueB)
				})

				ginkgo.AfterEach(func() {
					gomega.Expect(behavioral.DeleteAllJobsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
					gomega.Expect(behavioral.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
					gomega.Expect(behavioral.DeleteObject(ctx, k8sClient, localQueue)).Should(gomega.Succeed())
					gomega.Expect(behavioral.DeleteObject(ctx, k8sClient, localQueueB)).Should(gomega.Succeed())

					behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
					behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueueB, true)
					behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, tasFlavor, true)
					behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, topology, true)
					for _, node := range nodes {
						behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, &node, true)
					}
				})

				ginkgo.It("should allow to borrow within cohort and reclaim the capacity", func() {
					var wl1, wl2 *kueue.Workload
					ginkgo.By("creating a workload which can fit only when borrowing quota", func() {
						wl1 = utiltestingapi.MakeWorkload("wl1", ns.Name).
							PodSets(*utiltestingapi.MakePodSet("worker", 2).
								PreferredTopologyRequest(utiltesting.DefaultBlockTopologyLevel).
								Obj()).
							Queue(kueue.LocalQueueName(localQueue.Name)).Request(corev1.ResourceCPU, "4").Obj()
						behavioral.MustCreate(ctx, k8sClient, wl1)
					})

					ginkgo.By("verify the workload is admitted", func() {
						behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1)
						behavioral.ExpectAdmittedWorkloadsTotalMetric(clusterQueue, "", 1)
					})

					ginkgo.By("creating a workload in CQB which reclaims its quota", func() {
						// Note that workload wl2 would still fit within the regular quota
						// without preempting wl1. However, there is only 1 CPU left
						// on both nodes, so wl1 needs to be preempted to allow
						// scheduling of wl2.
						wl2 = utiltestingapi.MakeWorkload("wl2", ns.Name).
							Priority(2).
							PodSets(*utiltestingapi.MakePodSet("worker", 1).
								PreferredTopologyRequest(utiltesting.DefaultBlockTopologyLevel).
								Obj()).
							Queue(kueue.LocalQueueName(localQueueB.Name)).Request(corev1.ResourceCPU, "2").Obj()
						behavioral.MustCreate(ctx, k8sClient, wl2)
					})

					ginkgo.By("verify the wl1 gets preempted and wl2 gets admitted", func() {
						behavioral.ExpectWorkloadsToBePreempted(ctx, k8sClient, wl1)
						behavioral.FinishEvictionForWorkloads(ctx, k8sClient, wl1)
						behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl2)
						behavioral.ExpectReservingActiveWorkloadsMetric(clusterQueueB, 1)
						behavioral.ExpectReservingActiveWorkloadsMetric(clusterQueue, 0)
					})
				})
			})

			ginkgo.When("PrioritizePreemptorWorkloads is enabled with multiple evictions within Cohort", func() {
				var (
					nodes         []corev1.Node
					localQueueB   *kueue.LocalQueue
					clusterQueueB *kueue.ClusterQueue
					localQueueC   *kueue.LocalQueue
					clusterQueueC *kueue.ClusterQueue
				)

				ginkgo.BeforeEach(func() {
					features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.PrioritizePreemptorWorkloads, true)

					//          b1
					//     /    |    \
					//    r1    r2    r3
					//    |     |     |
					//    x2    x1    x3
					//   (5)   (5)   (2)
					nodes = []corev1.Node{
						*testingnode.MakeNode("x2").
							Label("node-group", "tas").
							Label(utiltesting.DefaultBlockTopologyLevel, "b1").
							Label(utiltesting.DefaultRackTopologyLevel, "r1").
							Label(corev1.LabelHostname, "x2").
							StatusAllocatable(corev1.ResourceList{
								corev1.ResourceCPU:    resource.MustParse("5"),
								corev1.ResourceMemory: resource.MustParse("5Gi"),
								corev1.ResourcePods:   resource.MustParse("10"),
							}).
							Ready().
							Obj(),
						*testingnode.MakeNode("x1").
							Label("node-group", "tas").
							Label(utiltesting.DefaultBlockTopologyLevel, "b1").
							Label(utiltesting.DefaultRackTopologyLevel, "r2").
							Label(corev1.LabelHostname, "x1").
							StatusAllocatable(corev1.ResourceList{
								corev1.ResourceCPU:    resource.MustParse("5"),
								corev1.ResourceMemory: resource.MustParse("5Gi"),
								corev1.ResourcePods:   resource.MustParse("10"),
							}).
							Ready().
							Obj(),
						*testingnode.MakeNode("x3").
							Label("node-group", "tas").
							Label(utiltesting.DefaultBlockTopologyLevel, "b1").
							Label(utiltesting.DefaultRackTopologyLevel, "r3").
							Label(corev1.LabelHostname, "x3").
							StatusAllocatable(corev1.ResourceList{
								corev1.ResourceCPU:    resource.MustParse("2"),
								corev1.ResourceMemory: resource.MustParse("5Gi"),
								corev1.ResourcePods:   resource.MustParse("10"),
							}).
							Ready().
							Obj(),
					}
					integration.CreateNodesWithStatus(ctx, k8sClient, nodes)

					topology = utiltestingapi.MakeDefaultThreeLevelTopology("default")
					behavioral.MustCreate(ctx, k8sClient, topology)

					tasFlavor = utiltestingapi.MakeResourceFlavor("tas-flavor").
						NodeLabel("node-group", "tas").
						TopologyName("default").Obj()
					behavioral.MustCreate(ctx, k8sClient, tasFlavor)

					clusterQueue = utiltestingapi.MakeClusterQueue("cluster-queue").
						Cohort("cohort").
						Preemption(kueue.ClusterQueuePreemption{
							WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
							ReclaimWithinCohort: kueue.PreemptionPolicyAny,
							BorrowWithinCohort: &kueue.BorrowWithinCohort{
								Policy: kueue.BorrowWithinCohortPolicyLowerPriority,
							},
						}).
						ResourceGroup(*utiltestingapi.MakeFlavorQuotas(tasFlavor.Name).
							Resource(corev1.ResourceCPU, "4").
							Resource(corev1.ResourceMemory, "5Gi").Obj()).
						Obj()
					behavioral.MustCreate(ctx, k8sClient, clusterQueue)
					behavioral.ExpectClusterQueuesToBeActive(ctx, k8sClient, clusterQueue)

					localQueue = utiltestingapi.MakeLocalQueue("local-queue", ns.Name).ClusterQueue(clusterQueue.Name).Obj()
					behavioral.MustCreate(ctx, k8sClient, localQueue)

					clusterQueueB = utiltestingapi.MakeClusterQueue("cluster-queue-b").
						Cohort("cohort").
						Preemption(kueue.ClusterQueuePreemption{
							WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
							ReclaimWithinCohort: kueue.PreemptionPolicyLowerPriority,
							BorrowWithinCohort: &kueue.BorrowWithinCohort{
								Policy: kueue.BorrowWithinCohortPolicyLowerPriority,
							},
						}).
						ResourceGroup(*utiltestingapi.MakeFlavorQuotas(tasFlavor.Name).
							Resource(corev1.ResourceCPU, "3").
							Resource(corev1.ResourceMemory, "5Gi").Obj()).
						Obj()
					behavioral.MustCreate(ctx, k8sClient, clusterQueueB)
					behavioral.ExpectClusterQueuesToBeActive(ctx, k8sClient, clusterQueueB)

					localQueueB = utiltestingapi.MakeLocalQueue("local-queue-b", ns.Name).ClusterQueue(clusterQueueB.Name).Obj()
					behavioral.MustCreate(ctx, k8sClient, localQueueB)

					clusterQueueC = utiltestingapi.MakeClusterQueue("cluster-queue-c").
						Cohort("cohort").
						Preemption(kueue.ClusterQueuePreemption{
							WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
							ReclaimWithinCohort: kueue.PreemptionPolicyLowerPriority,
							BorrowWithinCohort: &kueue.BorrowWithinCohort{
								Policy: kueue.BorrowWithinCohortPolicyLowerPriority,
							},
						}).
						ResourceGroup(*utiltestingapi.MakeFlavorQuotas(tasFlavor.Name).
							Resource(corev1.ResourceCPU, "5").
							Resource(corev1.ResourceMemory, "5Gi").Obj()).
						Obj()
					behavioral.MustCreate(ctx, k8sClient, clusterQueueC)
					behavioral.ExpectClusterQueuesToBeActive(ctx, k8sClient, clusterQueueC)

					localQueueC = utiltestingapi.MakeLocalQueue("local-queue-c", ns.Name).ClusterQueue(clusterQueueC.Name).Obj()
					behavioral.MustCreate(ctx, k8sClient, localQueueC)
				})

				ginkgo.AfterEach(func() {
					gomega.Expect(behavioral.DeleteAllJobsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
					gomega.Expect(behavioral.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
					gomega.Expect(behavioral.DeleteObject(ctx, k8sClient, localQueue)).Should(gomega.Succeed())
					gomega.Expect(behavioral.DeleteObject(ctx, k8sClient, localQueueB)).Should(gomega.Succeed())
					gomega.Expect(behavioral.DeleteObject(ctx, k8sClient, localQueueC)).Should(gomega.Succeed())

					behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
					behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueueB, true)
					behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueueC, true)
					behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, tasFlavor, true)
					behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, topology, true)
					for _, node := range nodes {
						behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, &node, true)
					}
				})

				ginkgo.It("should prevent other workloads from stealing topology when a preemptor is waiting for multiple evictions", func() {
					var wlA, wlB, wlPending, preemptor *kueue.Workload
					ginkgo.By("creating initial workload in clusterQueue consuming 4 CPU on first node", func() {
						wlA = utiltestingapi.MakeWorkload("wl-a", ns.Name).
							Priority(1).
							PodSets(*utiltestingapi.MakePodSet("worker", 1).
								PreferredTopologyRequest(utiltesting.DefaultBlockTopologyLevel).
								Obj()).
							Queue(kueue.LocalQueueName(localQueue.Name)).Request(corev1.ResourceCPU, "4").Obj()
						behavioral.MustCreate(ctx, k8sClient, wlA)
					})

					ginkgo.By("creating initial workload in clusterQueueB consuming 5 CPU on second node (borrowing 2 CPU)", func() {
						wlB = utiltestingapi.MakeWorkload("wl-b", ns.Name).
							Priority(2).
							PodSets(*utiltestingapi.MakePodSet("worker", 1).
								PreferredTopologyRequest(utiltesting.DefaultBlockTopologyLevel).
								Obj()).
							Queue(kueue.LocalQueueName(localQueueB.Name)).Request(corev1.ResourceCPU, "5").Obj()
						behavioral.MustCreate(ctx, k8sClient, wlB)
					})

					ginkgo.By("verify both workloads are admitted", func() {
						behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wlA, wlB)
					})

					ginkgo.By("creating a pending workload in clusterQueueC within its nominal quota (3 CPU) that cannot fit due to fragmented node capacity", func() {
						wlPending = utiltestingapi.MakeWorkload("wl-pending", ns.Name).
							Priority(1).
							PodSets(*utiltestingapi.MakePodSet("worker", 1).
								PreferredTopologyRequest(utiltesting.DefaultBlockTopologyLevel).
								Obj()).
							Queue(kueue.LocalQueueName(localQueueC.Name)).Request(corev1.ResourceCPU, "3").Obj()
						behavioral.MustCreate(ctx, k8sClient, wlPending)
						behavioral.ExpectWorkloadsToBePending(ctx, k8sClient, wlPending)
					})

					ginkgo.By("creating a high priority preemptor in clusterQueue requesting 2 pods of 4 CPU (borrowing from cohort)", func() {
						preemptor = utiltestingapi.MakeWorkload("preemptor", ns.Name).
							Priority(3).
							PodSets(*utiltestingapi.MakePodSet("worker", 2).
								PreferredTopologyRequest(utiltesting.DefaultBlockTopologyLevel).
								Obj()).
							Queue(kueue.LocalQueueName(localQueue.Name)).Request(corev1.ResourceCPU, "4").Obj()
						behavioral.MustCreate(ctx, k8sClient, preemptor)
					})

					ginkgo.By("verifying both wl-a and wl-b are marked for preemption", func() {
						behavioral.ExpectWorkloadsToBePreempted(ctx, k8sClient, wlA, wlB)
					})

					ginkgo.By("finishing eviction for wl-a only", func() {
						behavioral.FinishEvictionForWorkloads(ctx, k8sClient, wlA)
					})

					ginkgo.By("ensuring the pending workload wl-pending in clusterQueueC is not admitted in the interim across scheduling cycles", func() {
						gomega.Consistently(func(g gomega.Gomega) {
							g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wlPending), wlPending)).To(gomega.Succeed())
							g.Expect(workload.HasQuotaReservation(wlPending)).To(gomega.BeFalse())
						}, behavioral.ConsistentDuration, behavioral.ShortInterval).Should(gomega.Succeed())
					})

					ginkgo.By("finishing eviction for wl-b", func() {
						behavioral.FinishEvictionForWorkloads(ctx, k8sClient, wlB)
					})

					ginkgo.By("verifying preemptor is admitted and wl-pending remains pending with available quota but fragmented topology", func() {
						behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, preemptor)
						behavioral.ExpectWorkloadsToBePending(ctx, k8sClient, wlPending)

						gomega.Eventually(func(g gomega.Gomega) {
							g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wlPending), wlPending)).To(gomega.Succeed())
							cond := apimeta.FindStatusCondition(wlPending.Status.Conditions, kueue.WorkloadQuotaReserved)
							g.Expect(cond).ToNot(gomega.BeNil())
							g.Expect(cond.Status).To(gomega.Equal(metav1.ConditionFalse))
							g.Expect(cond.Reason).To(gomega.Equal(kueue.WorkloadQuotaReservedReasonWaitingForQuota))
							g.Expect(cond.Message).To(gomega.ContainSubstring(`topology "default" doesn't allow to fit any of 1 pod(s)`))
						}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
					})
				})
			})
		})
	})
})
