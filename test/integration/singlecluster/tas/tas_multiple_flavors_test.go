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
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	utiltas "sigs.k8s.io/kueue/pkg/util/tas"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingnode "sigs.k8s.io/kueue/pkg/util/testingjobs/node"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/test/integration/framework"
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

	ginkgo.Context("Multiple flavors and workload lifecycle", func() {
		ginkgo.When("Multiple TAS Resource Flavors in queue", func() {
			var (
				tasGPUFlavor *kueue.ResourceFlavor
				tasCPUFlavor *kueue.ResourceFlavor
				topology     *kueue.Topology
				localQueue   *kueue.LocalQueue
				clusterQueue *kueue.ClusterQueue
			)

			ginkgo.When("Nodes are created before test", func() {
				const (
					gpuResName = "example.com/gpu"
				)
				var (
					nodes []corev1.Node
				)

				ginkgo.BeforeEach(func() {
					nodes = []corev1.Node{
						*testingnode.MakeNode("cpu-node").
							Label(corev1.LabelInstanceTypeStable, "cpu-node").
							Label(utiltesting.DefaultRackTopologyLevel, "cpu-rack").
							StatusAllocatable(corev1.ResourceList{
								corev1.ResourceCPU:  resource.MustParse("5"),
								corev1.ResourcePods: resource.MustParse("10"),
							}).
							Ready().
							Obj(),
						*testingnode.MakeNode("gpu-node").
							Label(corev1.LabelInstanceTypeStable, "gpu-node").
							Label(utiltesting.DefaultRackTopologyLevel, "gpu-rack").
							StatusAllocatable(corev1.ResourceList{
								gpuResName:          resource.MustParse("4"),
								corev1.ResourcePods: resource.MustParse("10"),
							}).
							Ready().
							Obj(),
					}
					integration.CreateNodesWithStatus(ctx, k8sClient, nodes)

					topology = utiltestingapi.MakeTopology("default").Levels(
						utiltesting.DefaultRackTopologyLevel,
					).Obj()
					behavioral.MustCreate(ctx, k8sClient, topology)

					tasGPUFlavor = utiltestingapi.MakeResourceFlavor("tas-gpu-flavor").
						NodeLabel(corev1.LabelInstanceTypeStable, "gpu-node").
						TopologyName("default").
						Obj()
					behavioral.MustCreate(ctx, k8sClient, tasGPUFlavor)

					tasCPUFlavor = utiltestingapi.MakeResourceFlavor("tas-cpu-flavor").
						NodeLabel(corev1.LabelInstanceTypeStable, "cpu-node").
						TopologyName("default").
						Obj()
					behavioral.MustCreate(ctx, k8sClient, tasCPUFlavor)

					clusterQueue = utiltestingapi.MakeClusterQueue("cluster-queue").
						ResourceGroup(
							*utiltestingapi.MakeFlavorQuotas(tasGPUFlavor.Name).Resource(corev1.ResourceCPU, "1").Resource(gpuResName, "5").Obj(),
							*utiltestingapi.MakeFlavorQuotas(tasCPUFlavor.Name).Resource(corev1.ResourceCPU, "5").Resource(gpuResName, "0").Obj(),
						).Obj()
					behavioral.MustCreate(ctx, k8sClient, clusterQueue)

					localQueue = utiltestingapi.MakeLocalQueue("local-queue", ns.Name).ClusterQueue(clusterQueue.Name).Obj()
					behavioral.MustCreate(ctx, k8sClient, localQueue)
				})

				ginkgo.AfterEach(func() {
					gomega.Expect(behavioral.DeleteAllJobsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
					gomega.Expect(behavioral.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
					gomega.Expect(behavioral.DeleteObject(ctx, k8sClient, localQueue)).Should(gomega.Succeed())
					behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
					behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, tasCPUFlavor, true)
					behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, tasGPUFlavor, true)
					behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, topology, true)
					for _, node := range nodes {
						behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, &node, true)
					}
				})

				ginkgo.It("should admit workload which fits in a required topology domain", func() {
					var wl1 *kueue.Workload
					ginkgo.By("creating a workload which requires block and can fit", func() {
						wl1 = utiltestingapi.MakeWorkload("wl1", ns.Name).
							Queue(kueue.LocalQueueName(localQueue.Name)).Obj()
						ps1 := *utiltestingapi.MakePodSet("manager", 1).NodeSelector(
							map[string]string{corev1.LabelInstanceTypeStable: "cpu-node"},
						).Request(corev1.ResourceCPU, "5").Obj()
						ps1.TopologyRequest = &kueue.PodSetTopologyRequest{
							Required: new(utiltesting.DefaultRackTopologyLevel),
						}
						ps2 := *utiltestingapi.MakePodSet("worker", 2).NodeSelector(
							map[string]string{corev1.LabelInstanceTypeStable: "gpu-node"},
						).Request(gpuResName, "2").Obj()
						ps2.TopologyRequest = &kueue.PodSetTopologyRequest{
							Required: new(utiltesting.DefaultRackTopologyLevel),
						}
						wl1.Spec.PodSets = []kueue.PodSet{ps1, ps2}
						behavioral.MustCreate(ctx, k8sClient, wl1)
					})

					ginkgo.By("verify the workload is admitted", func() {
						behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1)
						behavioral.ExpectAdmittedWorkloadsTotalMetric(clusterQueue, "", 1)
					})

					ginkgo.By("verify admission for the workload1", func() {
						gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl1), wl1)).To(gomega.Succeed())
						gomega.Expect(wl1.Status.Admission.PodSetAssignments[0].TopologyAssignment).Should(gomega.BeComparableTo(
							utiltas.V1Beta2From(&utiltas.TopologyAssignment{
								Levels: []string{
									utiltesting.DefaultRackTopologyLevel,
								},
								Domains: []utiltas.TopologyDomainAssignment{
									{
										Count: 1,
										Values: []string{
											"cpu-rack",
										},
									},
								},
							}),
						))
						gomega.Expect(wl1.Status.Admission.PodSetAssignments[1].TopologyAssignment).Should(gomega.BeComparableTo(
							utiltas.V1Beta2From(&utiltas.TopologyAssignment{
								Levels: []string{
									utiltesting.DefaultRackTopologyLevel,
								},
								Domains: []utiltas.TopologyDomainAssignment{
									{
										Count: 2,
										Values: []string{
											"gpu-rack",
										},
									},
								},
							}),
						))
					})
				})

				ginkgo.It("should not admit workload when PodSet gets more than one TAS flavor (TAS request build fails)", func() {
					// A single PodSet requests both CPU and GPU with TAS. In this CQ no single flavor
					// has both (tas-cpu has CPU only, tas-gpu has GPU only), so the flavor assigner
					// fails with "insufficient quota". If the assigner ever assigned different flavors
					// per resource, TAS would fail with "more than one flavor assigned" (onlyTASFlavor).
					// Either way, the workload must not be admitted so it never gets TopologyAssignment.
					var wl *kueue.Workload
					ginkgo.By("creating a workload with one PodSet requesting both CPU and GPU and TAS", func() {
						wl = utiltestingapi.MakeWorkload("tas-multi-flavor-pending", ns.Name).
							Queue(kueue.LocalQueueName(localQueue.Name)).
							PodSets(*utiltestingapi.MakePodSet("main", 1).
								Request(corev1.ResourceCPU, "2").
								Request(gpuResName, "2").
								RequiredTopologyRequest(utiltesting.DefaultRackTopologyLevel).
								Obj(),
							).Obj()
						behavioral.MustCreate(ctx, k8sClient, wl)
					})

					ginkgo.By("verify the workload remains pending and is not admitted", func() {
						behavioral.ExpectWorkloadsToBePending(ctx, k8sClient, wl)
						gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), wl)).To(gomega.Succeed())
						gomega.Expect(workload.IsAdmitted(wl)).To(gomega.BeFalse())
					})

					ginkgo.By("verify QuotaReserved condition reports flavor/TAS-related failure", func() {
						gomega.Eventually(func(g gomega.Gomega) {
							g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), wl)).To(gomega.Succeed())
							cond := apimeta.FindStatusCondition(wl.Status.Conditions, kueue.WorkloadQuotaReserved)
							g.Expect(cond).ToNot(gomega.BeNil())
							g.Expect(cond.Status).To(gomega.Equal(metav1.ConditionFalse))
							g.Expect(cond.Reason).To(gomega.Equal(kueue.WorkloadQuotaReservedReasonExceedsMaxQuota))
							// With this CQ, no single flavor fits both CPU and GPU, so flavor assigner fails with "couldn't assign flavors".
							g.Expect(cond.Message).To(gomega.ContainSubstring("couldn't assign flavors"))
						}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
					})
				})

				ginkgo.It("should respect TAS usage of admitted workloads after one of multiple TAS RFs is deleted and re-created", framework.SlowSpec, func() {
					var wl1, wl2 *kueue.Workload
					ginkgo.By("creating wl1 which consumes the entire GPU TAS capacity", func() {
						wl1 = utiltestingapi.MakeWorkload("wl1", ns.Name).
							Queue(kueue.LocalQueueName(localQueue.Name)).PodSets(*utiltestingapi.MakePodSet("worker", 2).
							RequiredTopologyRequest(utiltesting.DefaultRackTopologyLevel).
							Obj()).Request(gpuResName, "2").Obj()
						behavioral.MustCreate(ctx, k8sClient, wl1)
					})

					ginkgo.By("verify wl1 is admitted", func() {
						behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1)
						behavioral.ExpectAdmittedWorkloadsTotalMetric(clusterQueue, "", 1)
					})

					ginkgo.By("remove GPU TAS RF finalizers to allow deletion", func() {
						gomega.Eventually(func(g gomega.Gomega) {
							var updatedFlavor kueue.ResourceFlavor
							g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(tasGPUFlavor), &updatedFlavor)).To(gomega.Succeed())
							updatedFlavor.Finalizers = nil
							g.Expect(k8sClient.Update(ctx, &updatedFlavor)).To(gomega.Succeed())
						}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
					})

					ginkgo.By("delete GPU TAS RF", func() {
						behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, tasGPUFlavor, true)
					})

					ginkgo.By("await for the CQ to become inactive", func() {
						gomega.Eventually(func(g gomega.Gomega) {
							var updatedCq kueue.ClusterQueue
							g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(clusterQueue), &updatedCq)).To(gomega.Succeed())
							g.Expect(updatedCq.Status.Conditions).Should(gomega.BeComparableTo([]metav1.Condition{
								{
									Type:    kueue.ClusterQueueActive,
									Status:  metav1.ConditionFalse,
									Reason:  "FlavorNotFound",
									Message: `Can't admit new workloads: references missing ResourceFlavor(s): tas-gpu-flavor.`,
								},
							}, behavioral.IgnoreConditionTimestampsAndObservedGeneration))
						}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
					})

					ginkgo.By("recreate the GPU TAS RF and wait for the queue to become active", func() {
						tasGPUFlavor = utiltestingapi.MakeResourceFlavor("tas-gpu-flavor").
							NodeLabel(corev1.LabelInstanceTypeStable, "gpu-node").
							TopologyName("default").
							Obj()
						behavioral.MustCreate(ctx, k8sClient, tasGPUFlavor)
						behavioral.ExpectClusterQueuesToBeActive(ctx, k8sClient, clusterQueue)
					})

					ginkgo.By("create wl2 which requires GPU capacity still used by wl1", func() {
						wl2 = utiltestingapi.MakeWorkload("wl2", ns.Name).
							Queue(kueue.LocalQueueName(localQueue.Name)).PodSets(*utiltestingapi.MakePodSet("worker", 1).
							RequiredTopologyRequest(utiltesting.DefaultRackTopologyLevel).
							Obj()).Request(gpuResName, "1").Obj()
						behavioral.MustCreate(ctx, k8sClient, wl2)
					})

					ginkgo.By("verify wl2 is not admitted as wl1 still uses the entire GPU TAS capacity", func() {
						behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1)
						behavioral.ExpectWorkloadsToBePending(ctx, k8sClient, wl2)
						behavioral.ExpectPendingWorkloadsMetric(clusterQueue, 0, 1)
					})

					ginkgo.By("finish wl1 to release the GPU TAS capacity", func() {
						integration.FinishWorkloads(ctx, k8sClient, wl1)
					})

					ginkgo.By("verify wl2 gets admitted", func() {
						behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl2)
					})
				})
			})

			// Two TAS ResourceFlavors share the same physical nodes (same NodeLabel)
			// but are made mutually exclusive via flavor-only NodeTaints
			// (tas-class=a on flavor-a, tas-class=b on flavor-b) so each workload
			// targets exactly one flavor. It also blocks exercise the
			// cross-flavor TAS usage aggregation on shared nodes in the following:
			//   1. It verifies the add side (sibling-flavor placement skips a node already
			//   occupied by the primary flavor)
			//   2. the second It verifies the subtract side (the freed node becomes reusable
			//   by the sibling flavor once the primary workload finishes).
			ginkgo.When("Two TAS ResourceFlavors share nodes", func() {
				var (
					tasFlavorA   *kueue.ResourceFlavor
					tasFlavorB   *kueue.ResourceFlavor
					topology     *kueue.Topology
					localQueue   *kueue.LocalQueue
					clusterQueue *kueue.ClusterQueue
					nodes        []corev1.Node
				)

				ginkgo.BeforeEach(func() {
					nodes = []corev1.Node{
						*testingnode.MakeNode("x1").
							Label("node-group", "tas").
							Label(corev1.LabelHostname, "x1").
							StatusAllocatable(corev1.ResourceList{
								corev1.ResourceCPU:    resource.MustParse("400m"),
								corev1.ResourceMemory: resource.MustParse("400Mi"),
								corev1.ResourcePods:   resource.MustParse("10"),
							}).
							Taints(corev1.Taint{
								Key:    "node-group",
								Value:  "tas",
								Effect: corev1.TaintEffectNoSchedule,
							}).
							Ready().
							Obj(),
						*testingnode.MakeNode("x2").
							Label("node-group", "tas").
							Label(corev1.LabelHostname, "x2").
							StatusAllocatable(corev1.ResourceList{
								corev1.ResourceCPU:    resource.MustParse("400m"),
								corev1.ResourceMemory: resource.MustParse("400Mi"),
								corev1.ResourcePods:   resource.MustParse("10"),
							}).
							Taints(corev1.Taint{
								Key:    "node-group",
								Value:  "tas",
								Effect: corev1.TaintEffectNoSchedule,
							}).
							Ready().
							Obj(),
					}
					integration.CreateNodesWithStatus(ctx, k8sClient, nodes)

					topology = utiltestingapi.MakeDefaultOneLevelTopology("default")
					behavioral.MustCreate(ctx, k8sClient, topology)

					tasFlavorA = utiltestingapi.MakeResourceFlavor("tas-flavor-a").
						NodeLabel("node-group", "tas").
						Taint(corev1.Taint{
							Key:    "node-group",
							Value:  "tas",
							Effect: corev1.TaintEffectNoSchedule,
						}).
						Taint(corev1.Taint{
							Key:    "tas-class",
							Value:  "a",
							Effect: corev1.TaintEffectNoSchedule,
						}).
						TopologyName("default").
						Obj()
					behavioral.MustCreate(ctx, k8sClient, tasFlavorA)

					tasFlavorB = utiltestingapi.MakeResourceFlavor("tas-flavor-b").
						NodeLabel("node-group", "tas").
						Taint(corev1.Taint{
							Key:    "node-group",
							Value:  "tas",
							Effect: corev1.TaintEffectNoSchedule,
						}).
						Taint(corev1.Taint{
							Key:    "tas-class",
							Value:  "b",
							Effect: corev1.TaintEffectNoSchedule,
						}).
						TopologyName("default").
						Obj()
					behavioral.MustCreate(ctx, k8sClient, tasFlavorB)

					clusterQueue = utiltestingapi.MakeClusterQueue("cluster-queue").
						ResourceGroup(
							*utiltestingapi.MakeFlavorQuotas(tasFlavorA.Name).
								Resource(corev1.ResourceCPU, "400m").
								Resource(corev1.ResourceMemory, "400Mi").Obj(),
							*utiltestingapi.MakeFlavorQuotas(tasFlavorB.Name).
								Resource(corev1.ResourceCPU, "100m").
								Resource(corev1.ResourceMemory, "100Mi").Obj(),
						).
						Preemption(kueue.ClusterQueuePreemption{
							ReclaimWithinCohort: kueue.PreemptionPolicyNever,
						}).
						Obj()
					behavioral.MustCreate(ctx, k8sClient, clusterQueue)

					localQueue = utiltestingapi.MakeLocalQueue("local-queue", ns.Name).ClusterQueue(clusterQueue.Name).Obj()
					behavioral.MustCreate(ctx, k8sClient, localQueue)
				})

				ginkgo.AfterEach(func() {
					gomega.Expect(behavioral.DeleteAllPodsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
					gomega.Expect(behavioral.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
					gomega.Expect(behavioral.DeleteObject(ctx, k8sClient, localQueue)).Should(gomega.Succeed())
					behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
					behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, tasFlavorA, true)
					behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, tasFlavorB, true)
					behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, topology, true)
					for _, node := range nodes {
						behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, &node, true)
					}
				})

				ginkgo.It("should admit the pending workload to tas-flavor-b on the free node when tas-flavor-a is full", func() {
					var (
						primaryWl1 *kueue.Workload
						primaryWl2 *kueue.Workload
						pendingWl  *kueue.Workload
					)

					ginkgo.By("creating the first tas-flavor-a workload pinned to x1", func() {
						primaryWl1 = utiltestingapi.MakeWorkload("primary-wl1", ns.Name).
							Queue(kueue.LocalQueueName(localQueue.Name)).
							PodSets(*utiltestingapi.MakePodSet("one", 1).
								NodeSelector(map[string]string{corev1.LabelHostname: "x1"}).
								PreferredTopologyRequest(corev1.LabelHostname).
								Request(corev1.ResourceCPU, "200m").
								Request(corev1.ResourceMemory, "200Mi").
								Toleration(corev1.Toleration{
									Key:      "node-group",
									Operator: corev1.TolerationOpEqual,
									Value:    "tas",
									Effect:   corev1.TaintEffectNoSchedule,
								}).
								Toleration(corev1.Toleration{
									Key:      "tas-class",
									Operator: corev1.TolerationOpEqual,
									Value:    "a",
									Effect:   corev1.TaintEffectNoSchedule,
								}).
								Obj()).Obj()
						behavioral.MustCreate(ctx, k8sClient, primaryWl1)
						behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, primaryWl1)
						createPodsForWorkload(primaryWl1, ns.Name, false, true)
					})

					ginkgo.By("creating the second tas-flavor-a workload pinned to x1", func() {
						primaryWl2 = utiltestingapi.MakeWorkload("primary-wl2", ns.Name).
							Queue(kueue.LocalQueueName(localQueue.Name)).
							PodSets(*utiltestingapi.MakePodSet("one", 1).
								NodeSelector(map[string]string{corev1.LabelHostname: "x1"}).
								PreferredTopologyRequest(corev1.LabelHostname).
								Request(corev1.ResourceCPU, "200m").
								Request(corev1.ResourceMemory, "200Mi").
								Toleration(corev1.Toleration{
									Key:      "node-group",
									Operator: corev1.TolerationOpEqual,
									Value:    "tas",
									Effect:   corev1.TaintEffectNoSchedule,
								}).
								Toleration(corev1.Toleration{
									Key:      "tas-class",
									Operator: corev1.TolerationOpEqual,
									Value:    "a",
									Effect:   corev1.TaintEffectNoSchedule,
								}).
								Obj()).Obj()
						behavioral.MustCreate(ctx, k8sClient, primaryWl2)
						behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, primaryWl2)
						createPodsForWorkload(primaryWl2, ns.Name, false, true)
					})

					ginkgo.By("verifying both primary workloads consumed tas-flavor-a and share node x1", func() {
						gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(primaryWl1), primaryWl1)).To(gomega.Succeed())
						gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(primaryWl2), primaryWl2)).To(gomega.Succeed())
						for _, wl := range []*kueue.Workload{primaryWl1, primaryWl2} {
							gomega.Expect(wl.Status.Admission.PodSetAssignments[0].Flavors[corev1.ResourceCPU]).To(
								gomega.Equal(kueue.ResourceFlavorReference(tasFlavorA.Name)))
							gomega.Expect(wl.Status.Admission.PodSetAssignments[0].Flavors[corev1.ResourceMemory]).To(
								gomega.Equal(kueue.ResourceFlavorReference(tasFlavorA.Name)))
							ta := utiltas.InternalFrom(wl.Status.Admission.PodSetAssignments[0].TopologyAssignment)
							gomega.Expect(ta.Domains).To(gomega.HaveLen(1))
							gomega.Expect(ta.Domains[0].Values).To(gomega.Equal([]string{"x1"}))
						}
					})

					ginkgo.By("creating the pending workload that tolerates both taints", func() {
						pendingWl = utiltestingapi.MakeWorkload("pending-wl", ns.Name).
							Queue(kueue.LocalQueueName(localQueue.Name)).
							PodSets(*utiltestingapi.MakePodSet("one", 1).
								PreferredTopologyRequest(corev1.LabelHostname).
								Request(corev1.ResourceCPU, "100m").
								Request(corev1.ResourceMemory, "100Mi").
								Toleration(corev1.Toleration{
									Key:      "node-group",
									Operator: corev1.TolerationOpEqual,
									Value:    "tas",
									Effect:   corev1.TaintEffectNoSchedule,
								}).
								Toleration(corev1.Toleration{
									Key:      "tas-class",
									Operator: corev1.TolerationOpEqual,
									Value:    "b",
									Effect:   corev1.TaintEffectNoSchedule,
								}).
								Obj()).Obj()
						behavioral.MustCreate(ctx, k8sClient, pendingWl)
					})

					ginkgo.By("verifying the pending workload is admitted to tas-flavor-b on the free node x2", func() {
						behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, pendingWl)
						gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(pendingWl), pendingWl)).To(gomega.Succeed())
						gomega.Expect(pendingWl.Status.Admission.PodSetAssignments[0].Flavors[corev1.ResourceCPU]).To(
							gomega.Equal(kueue.ResourceFlavorReference(tasFlavorB.Name)))
						gomega.Expect(pendingWl.Status.Admission.PodSetAssignments[0].Flavors[corev1.ResourceMemory]).To(
							gomega.Equal(kueue.ResourceFlavorReference(tasFlavorB.Name)))
						gomega.Expect(pendingWl.Status.Admission.PodSetAssignments[0].TopologyAssignment).Should(gomega.BeComparableTo(
							utiltas.V1Beta2From(&utiltas.TopologyAssignment{
								Levels: []string{corev1.LabelHostname},
								Domains: []utiltas.TopologyDomainAssignment{
									{
										Count:  1,
										Values: []string{"x2"},
									},
								},
							}),
						))
					})
				})

				ginkgo.It("should admit a sibling-flavor workload on the freed node once the primary workload finishes", func() {
					var (
						primaryWl *kueue.Workload
						siblingWl *kueue.Workload
					)

					ginkgo.By("admitting a tas-flavor-a workload that fully consumes x1", func() {
						primaryWl = utiltestingapi.MakeWorkload("primary-wl", ns.Name).
							Queue(kueue.LocalQueueName(localQueue.Name)).
							PodSets(*utiltestingapi.MakePodSet("one", 1).
								NodeSelector(map[string]string{corev1.LabelHostname: "x1"}).
								RequiredTopologyRequest(corev1.LabelHostname).
								Request(corev1.ResourceCPU, "400m").
								Request(corev1.ResourceMemory, "400Mi").
								Toleration(corev1.Toleration{
									Key:      "node-group",
									Operator: corev1.TolerationOpEqual,
									Value:    "tas",
									Effect:   corev1.TaintEffectNoSchedule,
								}).
								Toleration(corev1.Toleration{
									Key:      "tas-class",
									Operator: corev1.TolerationOpEqual,
									Value:    "a",
									Effect:   corev1.TaintEffectNoSchedule,
								}).
								Obj()).Obj()
						behavioral.MustCreate(ctx, k8sClient, primaryWl)
						behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, primaryWl)
						createPodsForWorkload(primaryWl, ns.Name, false, true)
					})

					ginkgo.By("creating a tas-flavor-b workload pinned to x1 that must stay pending while the sibling flavor occupies the node", func() {
						siblingWl = utiltestingapi.MakeWorkload("sibling-wl", ns.Name).
							Queue(kueue.LocalQueueName(localQueue.Name)).
							PodSets(*utiltestingapi.MakePodSet("one", 1).
								NodeSelector(map[string]string{corev1.LabelHostname: "x1"}).
								RequiredTopologyRequest(corev1.LabelHostname).
								Request(corev1.ResourceCPU, "100m").
								Request(corev1.ResourceMemory, "100Mi").
								Toleration(corev1.Toleration{
									Key:      "node-group",
									Operator: corev1.TolerationOpEqual,
									Value:    "tas",
									Effect:   corev1.TaintEffectNoSchedule,
								}).
								Toleration(corev1.Toleration{
									Key:      "tas-class",
									Operator: corev1.TolerationOpEqual,
									Value:    "b",
									Effect:   corev1.TaintEffectNoSchedule,
								}).
								Obj()).Obj()
						behavioral.MustCreate(ctx, k8sClient, siblingWl)
						behavioral.ExpectWorkloadsToBePending(ctx, k8sClient, siblingWl)
					})

					ginkgo.By("verifying the sibling workload stays pending while the primary workload still holds x1", func() {
						gomega.Consistently(func(g gomega.Gomega) {
							g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(siblingWl), siblingWl)).To(gomega.Succeed())
							g.Expect(workload.IsAdmitted(siblingWl)).To(gomega.BeFalse())
						}, behavioral.ConsistentDuration, behavioral.ShortInterval).Should(gomega.Succeed())
					})

					ginkgo.By("finishing the tas-flavor-a workload to release x1 in the shared TAS usage", func() {
						integration.FinishWorkloads(ctx, k8sClient, primaryWl)
					})

					ginkgo.By("verifying the sibling tas-flavor-b workload is admitted on the freed node x1", func() {
						behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, siblingWl)
						gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(siblingWl), siblingWl)).To(gomega.Succeed())
						gomega.Expect(siblingWl.Status.Admission.PodSetAssignments[0].Flavors[corev1.ResourceCPU]).To(
							gomega.Equal(kueue.ResourceFlavorReference(tasFlavorB.Name)))
						gomega.Expect(siblingWl.Status.Admission.PodSetAssignments[0].Flavors[corev1.ResourceMemory]).To(
							gomega.Equal(kueue.ResourceFlavorReference(tasFlavorB.Name)))
						gomega.Expect(siblingWl.Status.Admission.PodSetAssignments[0].TopologyAssignment).Should(gomega.BeComparableTo(
							utiltas.V1Beta2From(&utiltas.TopologyAssignment{
								Levels: []string{corev1.LabelHostname},
								Domains: []utiltas.TopologyDomainAssignment{
									{
										Count:  1,
										Values: []string{"x1"},
									},
								},
							}),
						))
					})
				})
			})
		})
	})
})
