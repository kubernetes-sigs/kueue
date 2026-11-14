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
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/features"
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

	ginkgo.When("Single TAS Resource Flavor", func() {
		var (
			tasFlavor    *kueue.ResourceFlavor
			topology     *kueue.Topology
			localQueue   *kueue.LocalQueue
			clusterQueue *kueue.ClusterQueue
		)

		ginkgo.Context("Rack and hostname placement", func() {
			ginkgo.When("Nodes are created before test with rack being the lowest level", func() {
				var (
					nodes []corev1.Node
				)

				ginkgo.BeforeEach(func() {
					nodes = []corev1.Node{
						*testingnode.MakeNode("b1-r1").
							Label("node-group", "tas").
							Label(utiltesting.DefaultBlockTopologyLevel, "b1").
							Label(utiltesting.DefaultRackTopologyLevel, "r1").
							StatusAllocatable(corev1.ResourceList{
								corev1.ResourceCPU:    resource.MustParse("1"),
								corev1.ResourceMemory: resource.MustParse("1Gi"),
								corev1.ResourcePods:   resource.MustParse("10"),
							}).
							Ready().
							Obj(),
						*testingnode.MakeNode("b1-r2").
							Label("node-group", "tas").
							Label(utiltesting.DefaultBlockTopologyLevel, "b1").
							Label(utiltesting.DefaultRackTopologyLevel, "r2").
							StatusAllocatable(corev1.ResourceList{
								corev1.ResourceCPU:    resource.MustParse("1"),
								corev1.ResourceMemory: resource.MustParse("1Gi"),
								corev1.ResourcePods:   resource.MustParse("10"),
							}).
							Ready().
							Obj(),
						*testingnode.MakeNode("b2-r1").
							Label("node-group", "tas").
							Label(utiltesting.DefaultBlockTopologyLevel, "b2").
							Label(utiltesting.DefaultRackTopologyLevel, "r1").
							StatusAllocatable(corev1.ResourceList{
								corev1.ResourceCPU:    resource.MustParse("1"),
								corev1.ResourceMemory: resource.MustParse("1Gi"),
								corev1.ResourcePods:   resource.MustParse("10"),
							}).
							Ready().
							Obj(),
						*testingnode.MakeNode("b2-r2").
							Label("node-group", "tas").
							Label(utiltesting.DefaultBlockTopologyLevel, "b2").
							Label(utiltesting.DefaultRackTopologyLevel, "r2").
							StatusAllocatable(corev1.ResourceList{
								corev1.ResourceCPU:    resource.MustParse("1"),
								corev1.ResourceMemory: resource.MustParse("1Gi"),
								corev1.ResourcePods:   resource.MustParse("10"),
							}).
							Ready().
							Obj(),
					}
					integration.CreateNodesWithStatus(ctx, k8sClient, nodes)

					topology = utiltestingapi.MakeDefaultTwoLevelTopology("default")
					behavioral.MustCreate(ctx, k8sClient, topology)

					tasFlavor = utiltestingapi.MakeResourceFlavor("tas-flavor").
						NodeLabel("node-group", "tas").
						TopologyName("default").Obj()
					behavioral.MustCreate(ctx, k8sClient, tasFlavor)

					clusterQueue = utiltestingapi.MakeClusterQueue("cluster-queue").
						ResourceGroup(*utiltestingapi.MakeFlavorQuotas(tasFlavor.Name).Resource(corev1.ResourceCPU, "5").Obj()).
						Obj()
					behavioral.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, clusterQueue)

					localQueue = utiltestingapi.MakeLocalQueue("local-queue", ns.Name).ClusterQueue(clusterQueue.Name).Obj()
					behavioral.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, localQueue)
				})

				ginkgo.AfterEach(func() {
					gomega.Expect(behavioral.DeleteAllJobsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
					gomega.Expect(behavioral.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
					gomega.Expect(behavioral.DeleteObject(ctx, k8sClient, localQueue)).Should(gomega.Succeed())
					behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
					behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, tasFlavor, true)
					behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, topology, true)
					for _, node := range nodes {
						behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, &node, true)
					}
				})

				ginkgo.It("Should admit a previously inadmissible limits-only workload after another workload finishes", func() {
					ginkgo.By("Creating a blocker workload and waiting until it reserves most of the available GPU quota")
					blocker := utiltestingapi.MakeWorkload("blocker", ns.Name).
						Queue(kueue.LocalQueueName(localQueue.Name)).PodSets(*utiltestingapi.MakePodSet("worker", 4).
						PreferredTopologyRequest(utiltesting.DefaultBlockTopologyLevel).
						Obj()).Request(corev1.ResourceCPU, "1").Obj()
					behavioral.MustCreate(ctx, k8sClient, blocker)

					behavioral.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, clusterQueue.Name, blocker)
					behavioral.ExpectPendingWorkloadsMetric(clusterQueue, 0, 0)

					ginkgo.By("Creating a second workload with only limits so it becomes inadmissible while the blocker holds quota")
					limitsOnly := utiltestingapi.MakeWorkload("limits-only", ns.Name).
						Queue(kueue.LocalQueueName(localQueue.Name)).PodSets(*utiltestingapi.MakePodSet("worker", 4).
						PreferredTopologyRequest(utiltesting.DefaultBlockTopologyLevel).
						Obj()).Limit(corev1.ResourceCPU, "1").Obj()
					behavioral.MustCreate(ctx, k8sClient, limitsOnly)

					behavioral.ExpectWorkloadsToBePending(ctx, k8sClient, limitsOnly)
					behavioral.ExpectPendingWorkloadsMetric(clusterQueue, 0, 1)

					ginkgo.By("Marking the blocker workload as finished")
					integration.FinishWorkloads(ctx, k8sClient, blocker)

					ginkgo.By("Verifying that once quota is released, the previously inadmissible limits-only workload now reserves quota")
					behavioral.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, clusterQueue.Name, limitsOnly)

					behavioral.ExpectPendingWorkloadsMetric(clusterQueue, 0, 0)
					behavioral.ExpectReservingActiveWorkloadsMetric(clusterQueue, 1)
				})

				ginkgo.It("should not admit workload which does not fit to the required topology domain", func() {
					ginkgo.By("creating a workload which requires rack, but does not fit in any", func() {
						wl1 := utiltestingapi.MakeWorkload("wl1-inadmissible", ns.Name).
							Queue(kueue.LocalQueueName(localQueue.Name)).Request(corev1.ResourceCPU, "2").Obj()
						wl1.Spec.PodSets[0].TopologyRequest = &kueue.PodSetTopologyRequest{
							Required: new(utiltesting.DefaultRackTopologyLevel),
						}
						behavioral.MustCreate(ctx, k8sClient, wl1)
					})

					ginkgo.By("verify the workload is inadmissible", func() {
						behavioral.ExpectPendingWorkloadsMetric(clusterQueue, 0, 1)
					})
				})

				ginkgo.It("should admit workload which fits in a required topology domain", func() {
					var wl1, wl2, wl3, wl4 *kueue.Workload
					ginkgo.By("creating a workload which requires block and can fit", func() {
						wl1 = utiltestingapi.MakeWorkload("wl1", ns.Name).
							Queue(kueue.LocalQueueName(localQueue.Name)).Request(corev1.ResourceCPU, "1").Obj()
						wl1.Spec.PodSets[0].Count = 2
						wl1.Spec.PodSets[0].TopologyRequest = &kueue.PodSetTopologyRequest{
							Required: new(utiltesting.DefaultBlockTopologyLevel),
						}
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
									utiltesting.DefaultBlockTopologyLevel,
									utiltesting.DefaultRackTopologyLevel,
								},
								Domains: []utiltas.TopologyDomainAssignment{
									{
										Count: 1,
										Values: []string{
											"b1",
											"r1",
										},
									},
									{
										Count: 1,
										Values: []string{
											"b1",
											"r2",
										},
									},
								},
							}),
						))
					})

					ginkgo.By("creating the second workload to see it lands on another rack", func() {
						wl2 = utiltestingapi.MakeWorkload("wl2", ns.Name).
							Queue(kueue.LocalQueueName(localQueue.Name)).Request(corev1.ResourceCPU, "1").Obj()
						wl2.Spec.PodSets[0].TopologyRequest = &kueue.PodSetTopologyRequest{
							Required: new(utiltesting.DefaultRackTopologyLevel),
						}
						behavioral.MustCreate(ctx, k8sClient, wl2)
					})

					ginkgo.By("verify the workload is admitted", func() {
						behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1, wl2)
						behavioral.ExpectAdmittedWorkloadsTotalMetric(clusterQueue, "", 2)
					})

					ginkgo.By("verify admission for the workload2", func() {
						gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl2), wl2)).To(gomega.Succeed())
						gomega.Expect(wl2.Status.Admission.PodSetAssignments[0].TopologyAssignment).Should(gomega.BeComparableTo(
							utiltas.V1Beta2From(&utiltas.TopologyAssignment{
								Levels: []string{
									utiltesting.DefaultBlockTopologyLevel,
									utiltesting.DefaultRackTopologyLevel,
								},
								Domains: []utiltas.TopologyDomainAssignment{
									{
										Count: 1,
										Values: []string{
											"b2",
											"r1",
										},
									},
								},
							}),
						))
					})

					ginkgo.By("creating the wl3", func() {
						wl3 = utiltestingapi.MakeWorkload("wl3", ns.Name).
							Queue(kueue.LocalQueueName(localQueue.Name)).Request(corev1.ResourceCPU, "1").Obj()
						wl3.Spec.PodSets[0].TopologyRequest = &kueue.PodSetTopologyRequest{
							Required: new(utiltesting.DefaultRackTopologyLevel),
						}
						behavioral.MustCreate(ctx, k8sClient, wl3)
					})

					ginkgo.By("verify the wl3 is admitted", func() {
						behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1, wl2, wl3)
						behavioral.ExpectAdmittedWorkloadsTotalMetric(clusterQueue, "", 3)
					})

					ginkgo.By("verify admission for the wl3", func() {
						gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl3), wl3)).To(gomega.Succeed())
						gomega.Expect(wl3.Status.Admission.PodSetAssignments[0].TopologyAssignment).Should(gomega.BeComparableTo(
							utiltas.V1Beta2From(&utiltas.TopologyAssignment{
								Levels: []string{
									utiltesting.DefaultBlockTopologyLevel,
									utiltesting.DefaultRackTopologyLevel,
								},
								Domains: []utiltas.TopologyDomainAssignment{
									{
										Count: 1,
										Values: []string{
											"b2",
											"r2",
										},
									},
								},
							}),
						))
					})

					ginkgo.By("creating wl4", func() {
						wl4 = utiltestingapi.MakeWorkload("wl4", ns.Name).
							Queue(kueue.LocalQueueName(localQueue.Name)).Request(corev1.ResourceCPU, "1").Obj()
						wl4.Spec.PodSets[0].TopologyRequest = &kueue.PodSetTopologyRequest{
							Required: new(utiltesting.DefaultRackTopologyLevel),
						}
						behavioral.MustCreate(ctx, k8sClient, wl4)
						behavioral.ExpectWorkloadsToBePending(ctx, k8sClient, wl4)
					})

					ginkgo.By("finish wl3", func() {
						integration.FinishWorkloads(ctx, k8sClient, wl3)
						behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1, wl2)
					})

					ginkgo.By("verify the wl4 is admitted", func() {
						behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1, wl2, wl4)
						behavioral.ExpectAdmittedWorkloadsTotalMetric(clusterQueue, "", 4)
					})

					ginkgo.By("verify admission for the wl4", func() {
						gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl4), wl4)).To(gomega.Succeed())
						gomega.Expect(wl4.Status.Admission.PodSetAssignments[0].TopologyAssignment).Should(gomega.BeComparableTo(
							utiltas.V1Beta2From(&utiltas.TopologyAssignment{
								Levels: []string{
									utiltesting.DefaultBlockTopologyLevel,
									utiltesting.DefaultRackTopologyLevel,
								},
								Domains: []utiltas.TopologyDomainAssignment{
									{
										Count: 1,
										Values: []string{
											"b2",
											"r2",
										},
									},
								},
							}),
						))
					})
				})

				ginkgo.It("should respect TAS usage by admitted workloads after reboot; second workload created before reboot", framework.SlowSpec, func() {
					var wl1, wl2 *kueue.Workload
					ginkgo.By("creating wl1 which consumes the entire TAS capacity", func() {
						wl1 = utiltestingapi.MakeWorkload("wl1", ns.Name).
							Queue(kueue.LocalQueueName(localQueue.Name)).Request(corev1.ResourceCPU, "1").Obj()
						wl1.Spec.PodSets[0].Count = 4
						wl1.Spec.PodSets[0].TopologyRequest = &kueue.PodSetTopologyRequest{
							Preferred: new(utiltesting.DefaultBlockTopologyLevel),
						}
						behavioral.MustCreate(ctx, k8sClient, wl1)
					})

					ginkgo.By("verify wl1 is admitted", func() {
						behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1)
						behavioral.ExpectAdmittedWorkloadsTotalMetric(clusterQueue, "", 1)
					})

					ginkgo.By("create wl2 which is blocked by wl1", func() {
						wl2 = utiltestingapi.MakeWorkload("wl2", ns.Name).
							Queue(kueue.LocalQueueName(localQueue.Name)).Request(corev1.ResourceCPU, "1").Obj()
						wl2.Spec.PodSets[0].TopologyRequest = &kueue.PodSetTopologyRequest{
							Required: new(utiltesting.DefaultRackTopologyLevel),
						}
						behavioral.MustCreate(ctx, k8sClient, wl2)
					})

					ginkgo.By("very wl2 is not admitted", func() {
						behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1)
						behavioral.ExpectWorkloadsToBePending(ctx, k8sClient, wl2)
						behavioral.ExpectPendingWorkloadsMetric(clusterQueue, 0, 1)
					})

					ginkgo.By("restart controllers", func() {
						fwk.StopManager(ctx)
						fwk.StartManager(ctx, cfg, managerSetup())
					})

					ginkgo.By("verify wl2 is still not admitted", func() {
						behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1)
						behavioral.ExpectPendingWorkloadsMetric(clusterQueue, 0, 1)
					})

					ginkgo.By("finish wl1 to demonstrate there is enough space for wl2", func() {
						integration.FinishWorkloads(ctx, k8sClient, wl1)
					})

					ginkgo.By("verify wl2 gets admitted", func() {
						behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl2)
					})
				})

				ginkgo.It("should not admit the workload after the topology is deleted but should admit it after the topology is created", framework.SlowSpec, func() {
					var updatedTopology kueue.Topology

					ginkgo.By("wait for the finalizer to be added to the topology", func() {
						gomega.Eventually(func(g gomega.Gomega) {
							g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(topology), &updatedTopology)).To(gomega.Succeed())
							g.Expect(updatedTopology.Finalizers).Should(gomega.Equal([]string{kueue.ResourceInUseFinalizerName}))
						}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
					})

					ginkgo.By("trigger topology deletion", func() {
						gomega.Expect(behavioral.DeleteObject(ctx, k8sClient, topology)).To(gomega.Succeed())
					})

					ginkgo.By("remove topology finalizers to allow deletion", func() {
						gomega.Eventually(func(g gomega.Gomega) {
							g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(topology), &updatedTopology)).To(gomega.Succeed())
							updatedTopology.Finalizers = nil
							g.Expect(k8sClient.Update(ctx, &updatedTopology)).To(gomega.Succeed())
						}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
					})

					ginkgo.By("wait for the topology to be fully deleted", func() {
						behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, topology, false)
					})

					ginkgo.By("await for the CQ to become inactive", func() {
						gomega.Eventually(func(g gomega.Gomega) {
							var updatedCq kueue.ClusterQueue
							g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(clusterQueue), &updatedCq)).To(gomega.Succeed())
							g.Expect(updatedCq.Status.Conditions).Should(gomega.BeComparableTo([]metav1.Condition{
								{
									Type:    kueue.ClusterQueueActive,
									Status:  metav1.ConditionFalse,
									Reason:  "TopologyNotFound",
									Message: `Can't admit new workloads: there is no Topology "default" for TAS flavor "tas-flavor".`,
								},
							}, behavioral.IgnoreConditionTimestampsAndObservedGeneration))
						}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
					})

					var wl *kueue.Workload
					ginkgo.By("creating a workload which requires block and can fit", func() {
						wl = utiltestingapi.MakeWorkload("wl", ns.Name).
							Queue(kueue.LocalQueueName(localQueue.Name)).PodSets(*utiltestingapi.MakePodSet("worker", 2).
							RequiredTopologyRequest(utiltesting.DefaultBlockTopologyLevel).
							Obj()).Request(corev1.ResourceCPU, "1").Obj()
						behavioral.MustCreate(ctx, k8sClient, wl)
					})

					ginkgo.By("verify the workload is inadmissible", func() {
						behavioral.ExpectPendingWorkloadsMetric(clusterQueue, 0, 1)
					})

					ginkgo.By("recreate the Topology and wait for the queue to become active", func() {
						topology = utiltestingapi.MakeDefaultTwoLevelTopology("default")
						behavioral.MustCreate(ctx, k8sClient, topology)
						behavioral.ExpectClusterQueuesToBeActive(ctx, k8sClient, clusterQueue)
					})

					ginkgo.By("verify the workload is admitted", func() {
						behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl)
						behavioral.ExpectAdmittedWorkloadsTotalMetric(clusterQueue, "", 1)
					})

					ginkgo.By("verify admission for the workload", func() {
						gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), wl)).To(gomega.Succeed())
						gomega.Expect(wl.Status.Admission.PodSetAssignments[0].TopologyAssignment).Should(gomega.BeComparableTo(
							utiltas.V1Beta2From(&utiltas.TopologyAssignment{
								Levels: []string{utiltesting.DefaultBlockTopologyLevel, utiltesting.DefaultRackTopologyLevel},
								Domains: []utiltas.TopologyDomainAssignment{
									{Count: 1, Values: []string{"b1", "r1"}},
									{Count: 1, Values: []string{"b1", "r2"}},
								},
							}),
						))
					})
				})

				ginkgo.It("should not admit the workload after the TAS RF is deleted and admit it after the RF is re-created", framework.SlowSpec, func() {
					ginkgo.By("remove TAS RF finalizers to allow deletion", func() {
						gomega.Eventually(func(g gomega.Gomega) {
							var updatedFlavor kueue.ResourceFlavor
							g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(tasFlavor), &updatedFlavor)).To(gomega.Succeed())
							updatedFlavor.Finalizers = nil
							g.Expect(k8sClient.Update(ctx, &updatedFlavor)).To(gomega.Succeed())
						}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
					})

					ginkgo.By("delete TAS RF", func() {
						behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, tasFlavor, true)
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
									Message: `Can't admit new workloads: references missing ResourceFlavor(s): tas-flavor.`,
								},
							}, behavioral.IgnoreConditionTimestampsAndObservedGeneration))
						}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
					})

					var wl *kueue.Workload
					ginkgo.By("creating a workload which requires block and can fit", func() {
						wl = utiltestingapi.MakeWorkload("wl", ns.Name).
							Queue(kueue.LocalQueueName(localQueue.Name)).PodSets(*utiltestingapi.MakePodSet("worker", 2).
							RequiredTopologyRequest(utiltesting.DefaultBlockTopologyLevel).
							Obj()).Request(corev1.ResourceCPU, "1").Obj()
						behavioral.MustCreate(ctx, k8sClient, wl)
					})

					ginkgo.By("verify the workload is inadmissible", func() {
						behavioral.ExpectPendingWorkloadsMetric(clusterQueue, 0, 1)
					})

					ginkgo.By("recreate the ResourceFlavor and wait for the queue to become active", func() {
						tasFlavor = utiltestingapi.MakeResourceFlavor("tas-flavor").
							NodeLabel("node-group", "tas").
							TopologyName("default").Obj()
						behavioral.MustCreate(ctx, k8sClient, tasFlavor)
						behavioral.ExpectClusterQueuesToBeActive(ctx, k8sClient, clusterQueue)
					})

					ginkgo.By("verify the workload is admitted", func() {
						behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl)
						behavioral.ExpectAdmittedWorkloadsTotalMetric(clusterQueue, "", 1)
					})

					ginkgo.By("verify admission for the workload", func() {
						gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), wl)).To(gomega.Succeed())
						gomega.Expect(wl.Status.Admission.PodSetAssignments[0].TopologyAssignment).Should(gomega.BeComparableTo(
							utiltas.V1Beta2From(&utiltas.TopologyAssignment{
								Levels: []string{utiltesting.DefaultBlockTopologyLevel, utiltesting.DefaultRackTopologyLevel},
								Domains: []utiltas.TopologyDomainAssignment{
									{Count: 1, Values: []string{"b1", "r1"}},
									{Count: 1, Values: []string{"b1", "r2"}},
								},
							}),
						))
					})
				})

				ginkgo.It("should respect TAS usage of admitted workloads after the TAS RF is deleted and re-created", framework.SlowSpec, func() {
					var wl1, wl2 *kueue.Workload
					ginkgo.By("creating wl1 which consumes the entire TAS capacity", func() {
						wl1 = utiltestingapi.MakeWorkload("wl1", ns.Name).
							Queue(kueue.LocalQueueName(localQueue.Name)).PodSets(*utiltestingapi.MakePodSet("worker", 4).
							PreferredTopologyRequest(utiltesting.DefaultBlockTopologyLevel).
							Obj()).Request(corev1.ResourceCPU, "1").Obj()
						behavioral.MustCreate(ctx, k8sClient, wl1)
					})

					ginkgo.By("verify wl1 is admitted", func() {
						behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1)
						behavioral.ExpectAdmittedWorkloadsTotalMetric(clusterQueue, "", 1)
					})

					ginkgo.By("remove TAS RF finalizers to allow deletion", func() {
						gomega.Eventually(func(g gomega.Gomega) {
							var updatedFlavor kueue.ResourceFlavor
							g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(tasFlavor), &updatedFlavor)).To(gomega.Succeed())
							updatedFlavor.Finalizers = nil
							g.Expect(k8sClient.Update(ctx, &updatedFlavor)).To(gomega.Succeed())
						}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
					})

					ginkgo.By("delete TAS RF", func() {
						behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, tasFlavor, true)
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
									Message: `Can't admit new workloads: references missing ResourceFlavor(s): tas-flavor.`,
								},
							}, behavioral.IgnoreConditionTimestampsAndObservedGeneration))
						}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
					})

					ginkgo.By("recreate the ResourceFlavor and wait for the queue to become active", func() {
						tasFlavor = utiltestingapi.MakeResourceFlavor("tas-flavor").
							NodeLabel("node-group", "tas").
							TopologyName("default").Obj()
						behavioral.MustCreate(ctx, k8sClient, tasFlavor)
						behavioral.ExpectClusterQueuesToBeActive(ctx, k8sClient, clusterQueue)
					})

					ginkgo.By("create wl2 which requires capacity still used by wl1", func() {
						wl2 = utiltestingapi.MakeWorkload("wl2", ns.Name).
							Queue(kueue.LocalQueueName(localQueue.Name)).PodSets(*utiltestingapi.MakePodSet("worker", 1).
							RequiredTopologyRequest(utiltesting.DefaultRackTopologyLevel).
							Obj()).Request(corev1.ResourceCPU, "1").Obj()
						behavioral.MustCreate(ctx, k8sClient, wl2)
					})

					ginkgo.By("verify wl2 is not admitted as wl1 still uses the entire TAS capacity", func() {
						behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1)
						behavioral.ExpectWorkloadsToBePending(ctx, k8sClient, wl2)
						behavioral.ExpectPendingWorkloadsMetric(clusterQueue, 0, 1)
					})

					ginkgo.By("finish wl1 to release the TAS capacity", func() {
						integration.FinishWorkloads(ctx, k8sClient, wl1)
					})

					ginkgo.By("verify wl2 gets admitted", func() {
						behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl2)
					})
				})

				ginkgo.It("should not leak TAS domains if a workload is deleted while the topology is uninitialized (#12545)", func() {
					var wl1, wl2 *kueue.Workload
					ginkgo.By("creating a workload which requires block and can fit", func() {
						wl1 = utiltestingapi.MakeWorkload("wl1", ns.Name).
							Queue(kueue.LocalQueueName(localQueue.Name)).PodSets(*utiltestingapi.MakePodSet("worker", 2).
							RequiredTopologyRequest(utiltesting.DefaultBlockTopologyLevel).
							Obj()).Request(corev1.ResourceCPU, "1").Obj()
						behavioral.MustCreate(ctx, k8sClient, wl1)
					})

					ginkgo.By("verify the workload is admitted", func() {
						behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1)
						behavioral.ExpectAdmittedWorkloadsTotalMetric(clusterQueue, "", 1)
					})

					ginkgo.By("wait for the finalizer to be added to the topology", func() {
						var updatedTopology kueue.Topology
						gomega.Eventually(func(g gomega.Gomega) {
							g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(topology), &updatedTopology)).To(gomega.Succeed())
							g.Expect(updatedTopology.Finalizers).Should(gomega.Equal([]string{kueue.ResourceInUseFinalizerName}))
						}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
					})

					ginkgo.By("trigger topology deletion", func() {
						gomega.Expect(behavioral.DeleteObject(ctx, k8sClient, topology)).To(gomega.Succeed())
					})

					ginkgo.By("remove topology finalizers to allow deletion", func() {
						var updatedTopology kueue.Topology
						gomega.Eventually(func(g gomega.Gomega) {
							g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(topology), &updatedTopology)).To(gomega.Succeed())
							updatedTopology.Finalizers = nil
							g.Expect(k8sClient.Update(ctx, &updatedTopology)).To(gomega.Succeed())
						}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
					})

					ginkgo.By("wait for the topology to be fully deleted", func() {
						behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, topology, false)
					})

					ginkgo.By("delete the workload while the topology is deleted", func() {
						gomega.Expect(k8sClient.Delete(ctx, wl1)).Should(gomega.Succeed())
						behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, wl1, true)
					})

					ginkgo.By("re-create the topology so that the queue becomes active again", func() {
						topology = utiltestingapi.MakeDefaultTwoLevelTopology("default")
						behavioral.MustCreate(ctx, k8sClient, topology)
					})

					ginkgo.By("creating the second workload to verify the domain usage was not leaked", func() {
						// Wait for the ClusterQueue to become active again
						behavioral.ExpectClusterQueuesToBeActive(ctx, k8sClient, clusterQueue)

						// Request the capacity that was freed by wl1.
						wl2 = utiltestingapi.MakeWorkload("wl2", ns.Name).
							Queue(kueue.LocalQueueName(localQueue.Name)).PodSets(*utiltestingapi.MakePodSet("worker", 2).
							RequiredTopologyRequest(utiltesting.DefaultBlockTopologyLevel).
							Obj()).Request(corev1.ResourceCPU, "1").Obj()
						behavioral.MustCreate(ctx, k8sClient, wl2)
					})

					ginkgo.By("verify the second workload is admitted", func() {
						behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl2)
						behavioral.ExpectAdmittedWorkloadsTotalMetric(clusterQueue, "", 2)
					})
				})
			})

			ginkgo.When("Non-hostname topology evaluates feasibility and capacity per node", func() {
				var nodes []corev1.Node

				ginkgo.BeforeEach(func() {
					features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.TASNodeFeasibilityForAllLevels, true)
					// Rack r1 has a large tainted node and a small untainted one;
					// rack r2 has two small untainted nodes.
					nodes = []corev1.Node{
						*testingnode.MakeNode("vh-r1-n1").
							Label("node-group", "tas").
							Label(utiltesting.DefaultBlockTopologyLevel, "b1").
							Label(utiltesting.DefaultRackTopologyLevel, "r1").
							Taints(corev1.Taint{Key: "gpu", Value: "true", Effect: corev1.TaintEffectNoSchedule}).
							StatusAllocatable(corev1.ResourceList{
								corev1.ResourceCPU:    resource.MustParse("4"),
								corev1.ResourceMemory: resource.MustParse("4Gi"),
								corev1.ResourcePods:   resource.MustParse("10"),
							}).
							Ready().
							Obj(),
						*testingnode.MakeNode("vh-r1-n2").
							Label("node-group", "tas").
							Label(utiltesting.DefaultBlockTopologyLevel, "b1").
							Label(utiltesting.DefaultRackTopologyLevel, "r1").
							StatusAllocatable(corev1.ResourceList{
								corev1.ResourceCPU:    resource.MustParse("1"),
								corev1.ResourceMemory: resource.MustParse("4Gi"),
								corev1.ResourcePods:   resource.MustParse("10"),
							}).
							Ready().
							Obj(),
						*testingnode.MakeNode("vh-r2-n1").
							Label("node-group", "tas").
							Label(utiltesting.DefaultBlockTopologyLevel, "b1").
							Label(utiltesting.DefaultRackTopologyLevel, "r2").
							StatusAllocatable(corev1.ResourceList{
								corev1.ResourceCPU:    resource.MustParse("1"),
								corev1.ResourceMemory: resource.MustParse("4Gi"),
								corev1.ResourcePods:   resource.MustParse("10"),
							}).
							Ready().
							Obj(),
						*testingnode.MakeNode("vh-r2-n2").
							Label("node-group", "tas").
							Label(utiltesting.DefaultBlockTopologyLevel, "b1").
							Label(utiltesting.DefaultRackTopologyLevel, "r2").
							StatusAllocatable(corev1.ResourceList{
								corev1.ResourceCPU:    resource.MustParse("1"),
								corev1.ResourceMemory: resource.MustParse("4Gi"),
								corev1.ResourcePods:   resource.MustParse("10"),
							}).
							Ready().
							Obj(),
					}
					integration.CreateNodesWithStatus(ctx, k8sClient, nodes)

					topology = utiltestingapi.MakeDefaultTwoLevelTopology("default")
					behavioral.MustCreate(ctx, k8sClient, topology)

					tasFlavor = utiltestingapi.MakeResourceFlavor("tas-flavor").
						NodeLabel("node-group", "tas").
						TopologyName("default").Obj()
					behavioral.MustCreate(ctx, k8sClient, tasFlavor)

					clusterQueue = utiltestingapi.MakeClusterQueue("cluster-queue").
						ResourceGroup(*utiltestingapi.MakeFlavorQuotas(tasFlavor.Name).Resource(corev1.ResourceCPU, "10").Obj()).
						Obj()
					behavioral.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, clusterQueue)

					localQueue = utiltestingapi.MakeLocalQueue("local-queue", ns.Name).ClusterQueue(clusterQueue.Name).Obj()
					behavioral.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, localQueue)
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
				})

				ginkgo.It("should not admit a workload which fits a rack's aggregate but no feasible node", func() {
					// The only node with 2 CPU is tainted; every other node has 1 CPU.
					wl := utiltestingapi.MakeWorkload("wl-no-single-node", ns.Name).
						Queue(kueue.LocalQueueName(localQueue.Name)).
						PodSets(*utiltestingapi.MakePodSet("main", 1).
							RequiredTopologyRequest(utiltesting.DefaultRackTopologyLevel).
							Obj()).
						Request(corev1.ResourceCPU, "2").Obj()
					behavioral.MustCreate(ctx, k8sClient, wl)

					behavioral.ExpectWorkloadsToBePending(ctx, k8sClient, wl)
					behavioral.ExpectPendingWorkloadsMetric(clusterQueue, 0, 1)
					gomega.Consistently(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), wl)).To(gomega.Succeed())
						g.Expect(workload.IsAdmitted(wl)).To(gomega.BeFalse())
					}, behavioral.ShortConsistentDuration, behavioral.ShortInterval).Should(gomega.Succeed())
				})

				ginkgo.It("should admit a workload on the feasible node of a partially tainted rack", func() {
					wl := utiltestingapi.MakeWorkload("wl-feasible-node", ns.Name).
						Queue(kueue.LocalQueueName(localQueue.Name)).
						PodSets(*utiltestingapi.MakePodSet("main", 1).
							RequiredTopologyRequest(utiltesting.DefaultRackTopologyLevel).
							Obj()).
						Request(corev1.ResourceCPU, "1").Obj()
					behavioral.MustCreate(ctx, k8sClient, wl)

					behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl)
					ginkgo.By("verifying the assignment stays at the topology's levels", func() {
						gomega.Eventually(func(g gomega.Gomega) {
							var updated kueue.Workload
							g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &updated)).To(gomega.Succeed())
							g.Expect(updated.Status.Admission).NotTo(gomega.BeNil())
							g.Expect(updated.Status.Admission.PodSetAssignments).To(gomega.HaveLen(1))
							assignment := utiltas.InternalFrom(updated.Status.Admission.PodSetAssignments[0].TopologyAssignment)
							g.Expect(assignment.Levels).To(gomega.Equal([]string{utiltesting.DefaultBlockTopologyLevel, utiltesting.DefaultRackTopologyLevel}))
						}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
					})
				})

				ginkgo.It("should admit pods across a rack's nodes when no single node fits all of them", func() {
					// Two 1-CPU pods only fit together in rack r2, one per node.
					wl := utiltestingapi.MakeWorkload("wl-spread-in-rack", ns.Name).
						Queue(kueue.LocalQueueName(localQueue.Name)).
						PodSets(*utiltestingapi.MakePodSet("main", 2).
							RequiredTopologyRequest(utiltesting.DefaultRackTopologyLevel).
							Obj()).
						Request(corev1.ResourceCPU, "1").Obj()
					behavioral.MustCreate(ctx, k8sClient, wl)

					behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl)
					expected := utiltas.V1Beta2From(&utiltas.TopologyAssignment{
						Levels:  []string{utiltesting.DefaultBlockTopologyLevel, utiltesting.DefaultRackTopologyLevel},
						Domains: []utiltas.TopologyDomainAssignment{{Count: 2, Values: []string{"b1", "r2"}}},
					})
					gomega.Eventually(func(g gomega.Gomega) {
						var updated kueue.Workload
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &updated)).To(gomega.Succeed())
						g.Expect(updated.Status.Admission).NotTo(gomega.BeNil())
						g.Expect(updated.Status.Admission.PodSetAssignments).To(gomega.HaveLen(1))
						g.Expect(updated.Status.Admission.PodSetAssignments[0].TopologyAssignment).To(gomega.BeComparableTo(expected))
					}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
				})
			})
		})
	})
})
