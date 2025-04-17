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
	"github.com/onsi/gomega/types"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/features"
	utiltas "sigs.k8s.io/kueue/pkg/util/tas"
	"sigs.k8s.io/kueue/pkg/util/testing"
	testingnode "sigs.k8s.io/kueue/pkg/util/testingjobs/node"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("Topology Aware Scheduling", ginkgo.Ordered, func() {
	var (
		ns *corev1.Namespace
	)

	ginkgo.BeforeAll(func() {
		fwk.StartManager(ctx, cfg, managerSetup)
	})

	ginkgo.AfterAll(func() {
		fwk.StopManager(ctx)
	})

	ginkgo.BeforeEach(func() {
		_ = features.SetEnable(features.TopologyAwareScheduling, true)
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "tas-")
	})

	ginkgo.AfterEach(func() {
		_ = features.SetEnable(features.TopologyAwareScheduling, false)
	})

	ginkgo.When("Delete Topology", func() {
		var (
			tasFlavor *kueue.ResourceFlavor
			topology  *kueuealpha.Topology
		)

		ginkgo.AfterEach(func() {
			util.ExpectObjectToBeDeleted(ctx, k8sClient, tasFlavor, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, topology, true)
		})

		ginkgo.When("ResourceFlavor does not exist", func() {
			ginkgo.BeforeEach(func() {
				topology = testing.MakeDefaultOneLevelTopology("topology")
				util.MustCreate(ctx, k8sClient, topology)
			})

			ginkgo.It("should allow to delete topology", func() {
				util.ExpectObjectToBeDeleted(ctx, k8sClient, topology, true)
			})
		})

		ginkgo.When("ResourceFlavor exists", func() {
			ginkgo.BeforeEach(func() {
				tasFlavor = testing.MakeResourceFlavor("tas-flavor").
					NodeLabel("node-group", "tas").
					TopologyName(topology.Name).Obj()
				util.MustCreate(ctx, k8sClient, tasFlavor)

				topology = testing.MakeDefaultOneLevelTopology("topology")
				util.MustCreate(ctx, k8sClient, topology)
			})

			ginkgo.It("should not allow to delete topology", func() {
				createdTopology := &kueuealpha.Topology{}

				ginkgo.By("check topology has finalizer", func() {
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(topology), createdTopology)).Should(gomega.Succeed())
						g.Expect(createdTopology.Finalizers).Should(gomega.ContainElement(kueue.ResourceInUseFinalizerName))
					}, util.Timeout, util.Interval).Should(gomega.Succeed())
				})

				ginkgo.By("delete topology", func() {
					gomega.Expect(k8sClient.Delete(ctx, topology)).Should(gomega.Succeed())
				})

				ginkgo.By("check topology still present", func() {
					gomega.Consistently(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(topology), createdTopology)).Should(gomega.Succeed())
						g.Expect(createdTopology.Finalizers).Should(gomega.ContainElement(kueue.ResourceInUseFinalizerName))
					}, util.ConsistentDuration, util.Interval).Should(gomega.Succeed())
				})
			})
		})
	})

	ginkgo.When("Negative scenarios for ResourceFlavor configuration", func() {
		ginkgo.It("should not allow to create ResourceFlavor with invalid topology name", func() {
			tasFlavor := testing.MakeResourceFlavor("tas-flavor").
				NodeLabel("node-group", "tas").
				TopologyName("invalid topology name").Obj()
			gomega.Expect(k8sClient.Create(ctx, tasFlavor)).Should(gomega.HaveOccurred())
		})

		ginkgo.It("should not allow to create ResourceFlavor without any node labels", func() {
			tasFlavor := testing.MakeResourceFlavor("tas-flavor").TopologyName("default").Obj()
			gomega.Expect(k8sClient.Create(ctx, tasFlavor)).Should(gomega.HaveOccurred())
		})
	})

	ginkgo.When("Updating ResourceFlavorSpec that defines TopologyName", func() {
		var tasFlavor *kueue.ResourceFlavor

		ginkgo.BeforeEach(func() {
			tasFlavor = testing.MakeResourceFlavor("tas-flavor").
				TopologyName("default").
				NodeLabel("node-group", "tas").
				NodeLabel("foo", "bar").
				Obj()
			util.MustCreate(ctx, k8sClient, tasFlavor)
		})

		ginkgo.AfterEach(func() {
			util.ExpectObjectToBeDeleted(ctx, k8sClient, tasFlavor, true)
		})

		ginkgo.It("should not allow to update topologyName", func() {
			gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(tasFlavor), tasFlavor)).To(gomega.Succeed())
			tasFlavor.Spec.TopologyName = ptr.To(kueue.TopologyReference("invalid"))
			gomega.Expect(k8sClient.Update(ctx, tasFlavor)).Should(gomega.HaveOccurred())
		})

		ginkgo.It("should not allow to update nodeTaints", func() {
			gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(tasFlavor), tasFlavor)).To(gomega.Succeed())
			tasFlavor.Spec.NodeTaints = []corev1.Taint{{
				Key:    "foo",
				Value:  "bar",
				Effect: "Invalid",
			}}
			gomega.Expect(k8sClient.Update(ctx, tasFlavor)).Should(gomega.HaveOccurred())
		})

		ginkgo.It("should not allow to update tolerations", func() {
			gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(tasFlavor), tasFlavor)).To(gomega.Succeed())
			tasFlavor.Spec.Tolerations = []corev1.Toleration{
				{Key: "key1", Value: "value", Effect: corev1.TaintEffectNoSchedule, Operator: corev1.TolerationOpEqual}}
			gomega.Expect(k8sClient.Update(ctx, tasFlavor)).Should(gomega.HaveOccurred())
		})

		ginkgo.It("should not allow to update nodeLabels", func() {
			gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(tasFlavor), tasFlavor)).To(gomega.Succeed())
			tasFlavor.Spec.NodeLabels = map[string]string{
				"tas-node": "true",
			}
			gomega.Expect(k8sClient.Update(ctx, tasFlavor)).Should(gomega.HaveOccurred())
		})

		ginkgo.It("should not allow to delete one of the nodeLabels", func() {
			gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(tasFlavor), tasFlavor)).To(gomega.Succeed())
			tasFlavor.Spec.NodeLabels = map[string]string{
				"foo": "bar",
			}
			gomega.Expect(k8sClient.Update(ctx, tasFlavor)).Should(gomega.HaveOccurred())
		})
	})

	ginkgo.When("Negative scenarios for ClusterQueue configuration", func() {
		var (
			topology       *kueuealpha.Topology
			tasFlavor      *kueue.ResourceFlavor
			clusterQueue   *kueue.ClusterQueue
			admissionCheck *kueue.AdmissionCheck
		)

		ginkgo.BeforeEach(func() {
			topology = testing.MakeDefaultTwoLevelTopology("default")
			util.MustCreate(ctx, k8sClient, topology)

			tasFlavor = testing.MakeResourceFlavor("tas-flavor").
				NodeLabel("node-group", "tas").
				TopologyName("default").Obj()
			util.MustCreate(ctx, k8sClient, tasFlavor)
		})

		ginkgo.AfterEach(func() {
			util.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, tasFlavor, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, topology, true)
			util.ExpectObjectToBeDeleted(ctx, k8sClient, admissionCheck, true)
		})

		ginkgo.It("should mark TAS ClusterQueue as inactive if used with MultiKueue", func() {
			admissionCheck = testing.MakeAdmissionCheck("multikueue").ControllerName(kueue.MultiKueueControllerName).Obj()
			util.MustCreate(ctx, k8sClient, admissionCheck)
			util.SetAdmissionCheckActive(ctx, k8sClient, admissionCheck, metav1.ConditionTrue)

			clusterQueue = testing.MakeClusterQueue("cq").
				ResourceGroup(
					*testing.MakeFlavorQuotas(tasFlavor.Name).Resource(corev1.ResourceCPU, "5").Obj(),
				).AdmissionChecks(kueue.AdmissionCheckReference(admissionCheck.Name)).Obj()
			util.MustCreate(ctx, k8sClient, clusterQueue)

			gomega.Eventually(func(g gomega.Gomega) {
				var updatedCq kueue.ClusterQueue
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(clusterQueue), &updatedCq)).To(gomega.Succeed())
				g.Expect(updatedCq.Status.Conditions).Should(gomega.BeComparableTo([]metav1.Condition{
					{
						Type:    kueue.ClusterQueueActive,
						Status:  metav1.ConditionFalse,
						Reason:  "NotSupportedWithTopologyAwareScheduling",
						Message: `Can't admit new workloads: TAS is not supported with MultiKueue admission check.`,
					},
				}, util.IgnoreConditionTimestampsAndObservedGeneration))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})

		ginkgo.It("should mark TAS ClusterQueue as inactive if used with ProvisioningRequest", func() {
			admissionCheck = testing.MakeAdmissionCheck("provisioning").ControllerName(kueue.ProvisioningRequestControllerName).Obj()
			util.MustCreate(ctx, k8sClient, admissionCheck)
			util.SetAdmissionCheckActive(ctx, k8sClient, admissionCheck, metav1.ConditionTrue)

			clusterQueue = testing.MakeClusterQueue("cq").
				ResourceGroup(
					*testing.MakeFlavorQuotas(tasFlavor.Name).Resource(corev1.ResourceCPU, "5").Obj(),
				).AdmissionChecks(kueue.AdmissionCheckReference(admissionCheck.Name)).Obj()
			util.MustCreate(ctx, k8sClient, clusterQueue)

			gomega.Eventually(func(g gomega.Gomega) {
				var updatedCq kueue.ClusterQueue
				g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(clusterQueue), &updatedCq)).To(gomega.Succeed())
				g.Expect(updatedCq.Status.Conditions).Should(gomega.BeComparableTo([]metav1.Condition{
					{
						Type:    kueue.ClusterQueueActive,
						Status:  metav1.ConditionFalse,
						Reason:  "NotSupportedWithTopologyAwareScheduling",
						Message: `Can't admit new workloads: TAS is not supported with ProvisioningRequest admission check.`,
					},
				}, util.IgnoreConditionTimestampsAndObservedGeneration))
			}, util.Timeout, util.Interval).Should(gomega.Succeed())
		})
	})

	ginkgo.When("Single TAS Resource Flavor", func() {
		var (
			tasFlavor    *kueue.ResourceFlavor
			topology     *kueuealpha.Topology
			localQueue   *kueue.LocalQueue
			clusterQueue *kueue.ClusterQueue
		)

		ginkgo.When("Nodes are created before test with rack being the lowest level", func() {
			var (
				nodes []corev1.Node
			)

			ginkgo.BeforeEach(func() {
				nodes = []corev1.Node{
					*testingnode.MakeNode("b1-r1").
						Label("node-group", "tas").
						Label(testing.DefaultBlockTopologyLevel, "b1").
						Label(testing.DefaultRackTopologyLevel, "r1").
						StatusAllocatable(corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("1"),
							corev1.ResourceMemory: resource.MustParse("1Gi"),
							corev1.ResourcePods:   resource.MustParse("10"),
						}).
						Ready().
						Obj(),
					*testingnode.MakeNode("b1-r2").
						Label("node-group", "tas").
						Label(testing.DefaultBlockTopologyLevel, "b1").
						Label(testing.DefaultRackTopologyLevel, "r2").
						StatusAllocatable(corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("1"),
							corev1.ResourceMemory: resource.MustParse("1Gi"),
							corev1.ResourcePods:   resource.MustParse("10"),
						}).
						Ready().
						Obj(),
					*testingnode.MakeNode("b2-r1").
						Label("node-group", "tas").
						Label(testing.DefaultBlockTopologyLevel, "b2").
						Label(testing.DefaultRackTopologyLevel, "r1").
						StatusAllocatable(corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("1"),
							corev1.ResourceMemory: resource.MustParse("1Gi"),
							corev1.ResourcePods:   resource.MustParse("10"),
						}).
						Ready().
						Obj(),
					*testingnode.MakeNode("b2-r2").
						Label("node-group", "tas").
						Label(testing.DefaultBlockTopologyLevel, "b2").
						Label(testing.DefaultRackTopologyLevel, "r2").
						StatusAllocatable(corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("1"),
							corev1.ResourceMemory: resource.MustParse("1Gi"),
							corev1.ResourcePods:   resource.MustParse("10"),
						}).
						Ready().
						Obj(),
				}
				util.CreateNodesWithStatus(ctx, k8sClient, nodes)

				topology = testing.MakeDefaultTwoLevelTopology("default")
				util.MustCreate(ctx, k8sClient, topology)

				tasFlavor = testing.MakeResourceFlavor("tas-flavor").
					NodeLabel("node-group", "tas").
					TopologyName("default").Obj()
				util.MustCreate(ctx, k8sClient, tasFlavor)

				clusterQueue = testing.MakeClusterQueue("cluster-queue").
					ResourceGroup(*testing.MakeFlavorQuotas(tasFlavor.Name).Resource(corev1.ResourceCPU, "5").Obj()).
					Obj()
				util.MustCreate(ctx, k8sClient, clusterQueue)
				util.ExpectClusterQueuesToBeActive(ctx, k8sClient, clusterQueue)

				localQueue = testing.MakeLocalQueue("local-queue", ns.Name).ClusterQueue(clusterQueue.Name).Obj()
				util.MustCreate(ctx, k8sClient, localQueue)
			})

			ginkgo.AfterEach(func() {
				gomega.Expect(util.DeleteAllJobsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
				gomega.Expect(util.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
				gomega.Expect(util.DeleteObject(ctx, k8sClient, localQueue)).Should(gomega.Succeed())
				util.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
				util.ExpectObjectToBeDeleted(ctx, k8sClient, tasFlavor, true)
				util.ExpectObjectToBeDeleted(ctx, k8sClient, topology, true)
				for _, node := range nodes {
					util.ExpectObjectToBeDeleted(ctx, k8sClient, &node, true)
				}
			})

			ginkgo.It("should expose the TopologyName in LocalQueue status", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					createdLocalQueue := &kueue.LocalQueue{}
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(localQueue), createdLocalQueue)).Should(gomega.Succeed())
					g.Expect(createdLocalQueue.Status.Flavors).Should(gomega.HaveLen(1))
					g.Expect(createdLocalQueue.Status.Flavors[0].Topology).ShouldNot(gomega.BeNil())
					g.Expect(createdLocalQueue.Status.Flavors[0].Topology.Levels).Should(gomega.Equal(utiltas.Levels(topology)))
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.It("should not admit workload which does not fit to the required topology domain", func() {
				ginkgo.By("creating a workload which requires rack, but does not fit in any", func() {
					wl1 := testing.MakeWorkload("wl1-inadmissible", ns.Name).
						Queue(localQueue.Name).Request(corev1.ResourceCPU, "2").Obj()
					wl1.Spec.PodSets[0].TopologyRequest = &kueue.PodSetTopologyRequest{
						Required: ptr.To(testing.DefaultRackTopologyLevel),
					}
					util.MustCreate(ctx, k8sClient, wl1)
				})

				ginkgo.By("verify the workload is inadmissible", func() {
					util.ExpectPendingWorkloadsMetric(clusterQueue, 0, 1)
				})
			})

			ginkgo.It("should admit workload which fits in a required topology domain", func() {
				var wl1, wl2, wl3, wl4 *kueue.Workload
				ginkgo.By("creating a workload which requires block and can fit", func() {
					wl1 = testing.MakeWorkload("wl1", ns.Name).
						Queue(localQueue.Name).Request(corev1.ResourceCPU, "1").Obj()
					wl1.Spec.PodSets[0].Count = 2
					wl1.Spec.PodSets[0].TopologyRequest = &kueue.PodSetTopologyRequest{
						Required: ptr.To(testing.DefaultBlockTopologyLevel),
					}
					util.MustCreate(ctx, k8sClient, wl1)
				})

				ginkgo.By("verify the workload is admitted", func() {
					util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1)
					util.ExpectReservingActiveWorkloadsMetric(clusterQueue, 1)
				})

				ginkgo.By("verify admission for the workload1", func() {
					gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl1), wl1)).To(gomega.Succeed())
					gomega.Expect(wl1.Status.Admission.PodSetAssignments[0].TopologyAssignment).Should(gomega.BeComparableTo(
						&kueue.TopologyAssignment{
							Levels: []string{
								testing.DefaultBlockTopologyLevel,
								testing.DefaultRackTopologyLevel,
							},
							Domains: []kueue.TopologyDomainAssignment{
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
						},
					))
				})

				ginkgo.By("creating the second workload to see it lands on another rack", func() {
					wl2 = testing.MakeWorkload("wl2", ns.Name).
						Queue(localQueue.Name).Request(corev1.ResourceCPU, "1").Obj()
					wl2.Spec.PodSets[0].TopologyRequest = &kueue.PodSetTopologyRequest{
						Required: ptr.To(testing.DefaultRackTopologyLevel),
					}
					util.MustCreate(ctx, k8sClient, wl2)
				})

				ginkgo.By("verify the workload is admitted", func() {
					util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1, wl2)
					util.ExpectReservingActiveWorkloadsMetric(clusterQueue, 2)
				})

				ginkgo.By("verify admission for the workload2", func() {
					gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl2), wl2)).To(gomega.Succeed())
					gomega.Expect(wl2.Status.Admission.PodSetAssignments[0].TopologyAssignment).Should(gomega.BeComparableTo(
						&kueue.TopologyAssignment{
							Levels: []string{
								testing.DefaultBlockTopologyLevel,
								testing.DefaultRackTopologyLevel,
							},
							Domains: []kueue.TopologyDomainAssignment{
								{
									Count: 1,
									Values: []string{
										"b2",
										"r1",
									},
								},
							},
						},
					))
				})

				ginkgo.By("creating the wl3", func() {
					wl3 = testing.MakeWorkload("wl3", ns.Name).
						Queue(localQueue.Name).Request(corev1.ResourceCPU, "1").Obj()
					wl3.Spec.PodSets[0].TopologyRequest = &kueue.PodSetTopologyRequest{
						Required: ptr.To(testing.DefaultRackTopologyLevel),
					}
					util.MustCreate(ctx, k8sClient, wl3)
				})

				ginkgo.By("verify the wl3 is admitted", func() {
					util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1, wl2, wl3)
					util.ExpectReservingActiveWorkloadsMetric(clusterQueue, 3)
				})

				ginkgo.By("verify admission for the wl3", func() {
					gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl3), wl3)).To(gomega.Succeed())
					gomega.Expect(wl3.Status.Admission.PodSetAssignments[0].TopologyAssignment).Should(gomega.BeComparableTo(
						&kueue.TopologyAssignment{
							Levels: []string{
								testing.DefaultBlockTopologyLevel,
								testing.DefaultRackTopologyLevel,
							},
							Domains: []kueue.TopologyDomainAssignment{
								{
									Count: 1,
									Values: []string{
										"b2",
										"r2",
									},
								},
							},
						},
					))
				})

				ginkgo.By("creating wl4", func() {
					wl4 = testing.MakeWorkload("wl4", ns.Name).
						Queue(localQueue.Name).Request(corev1.ResourceCPU, "1").Obj()
					wl4.Spec.PodSets[0].TopologyRequest = &kueue.PodSetTopologyRequest{
						Required: ptr.To(testing.DefaultRackTopologyLevel),
					}
					util.MustCreate(ctx, k8sClient, wl4)
					util.ExpectWorkloadsToBePending(ctx, k8sClient, wl4)
				})

				ginkgo.By("finish wl3", func() {
					util.FinishWorkloads(ctx, k8sClient, wl3)
					util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1, wl2)
				})

				ginkgo.By("verify the wl4 is admitted", func() {
					util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1, wl2, wl4)
					util.ExpectReservingActiveWorkloadsMetric(clusterQueue, 3)
				})

				ginkgo.By("verify admission for the wl4", func() {
					gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl4), wl4)).To(gomega.Succeed())
					gomega.Expect(wl4.Status.Admission.PodSetAssignments[0].TopologyAssignment).Should(gomega.BeComparableTo(
						&kueue.TopologyAssignment{
							Levels: []string{
								testing.DefaultBlockTopologyLevel,
								testing.DefaultRackTopologyLevel,
							},
							Domains: []kueue.TopologyDomainAssignment{
								{
									Count: 1,
									Values: []string{
										"b2",
										"r2",
									},
								},
							},
						},
					))
				})
			})

			ginkgo.It("should not admit the workload after the topology is deleted but should admit it after the topology is created", func() {
				var updatedTopology kueuealpha.Topology

				ginkgo.By("wait for the finalizer to be added to the topology", func() {
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(topology), &updatedTopology)).To(gomega.Succeed())
						g.Expect(updatedTopology.Finalizers).Should(gomega.Equal([]string{kueue.ResourceInUseFinalizerName}))
					}, util.Timeout, util.Interval).Should(gomega.Succeed())
				})

				ginkgo.By("trigger topology deletion", func() {
					gomega.Expect(util.DeleteObject(ctx, k8sClient, topology)).To(gomega.Succeed())
				})

				ginkgo.By("remove topology finalizers to allow deletion", func() {
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(topology), &updatedTopology)).To(gomega.Succeed())
						updatedTopology.Finalizers = nil
						g.Expect(k8sClient.Update(ctx, &updatedTopology)).To(gomega.Succeed())
					}, util.Timeout, util.Interval).Should(gomega.Succeed())
				})

				ginkgo.By("wait for the topology to be fully deleted", func() {
					util.ExpectObjectToBeDeleted(ctx, k8sClient, topology, false)
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
						}, util.IgnoreConditionTimestampsAndObservedGeneration))
					}, util.Timeout, util.Interval).Should(gomega.Succeed())
				})

				var wl *kueue.Workload
				ginkgo.By("creating a workload which requires block and can fit", func() {
					wl = testing.MakeWorkload("wl", ns.Name).
						Queue(localQueue.Name).PodSets(*testing.MakePodSet("worker", 2).
						RequiredTopologyRequest(testing.DefaultBlockTopologyLevel).
						Obj()).Request(corev1.ResourceCPU, "1").Obj()
					util.MustCreate(ctx, k8sClient, wl)
				})

				ginkgo.By("verify the workload is inadmissible", func() {
					util.ExpectPendingWorkloadsMetric(clusterQueue, 0, 1)
				})

				ginkgo.By("recreate the Topology and wait for the queue to become active", func() {
					topology = testing.MakeDefaultTwoLevelTopology("default")
					util.MustCreate(ctx, k8sClient, topology)
					util.ExpectClusterQueuesToBeActive(ctx, k8sClient, clusterQueue)
				})

				ginkgo.By("verify the workload is admitted", func() {
					util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl)
					util.ExpectReservingActiveWorkloadsMetric(clusterQueue, 1)
				})

				ginkgo.By("verify admission for the workload", func() {
					gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), wl)).To(gomega.Succeed())
					gomega.Expect(wl.Status.Admission.PodSetAssignments[0].TopologyAssignment).Should(gomega.BeComparableTo(
						&kueue.TopologyAssignment{
							Levels: []string{testing.DefaultBlockTopologyLevel, testing.DefaultRackTopologyLevel},
							Domains: []kueue.TopologyDomainAssignment{
								{Count: 1, Values: []string{"b1", "r1"}},
								{Count: 1, Values: []string{"b1", "r2"}},
							},
						},
					))
				})
			})

			ginkgo.It("should not admit the workload after the TAS RF is deleted and admit it after the RF is re-created", func() {
				ginkgo.By("remove TAS RF finalizers to allow deletion", func() {
					gomega.Eventually(func(g gomega.Gomega) {
						var updatedFlavor kueue.ResourceFlavor
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(tasFlavor), &updatedFlavor)).To(gomega.Succeed())
						updatedFlavor.Finalizers = nil
						g.Expect(k8sClient.Update(ctx, &updatedFlavor)).To(gomega.Succeed())
					}, util.Timeout, util.Interval).Should(gomega.Succeed())
				})

				ginkgo.By("delete TAS RF", func() {
					util.ExpectObjectToBeDeleted(ctx, k8sClient, tasFlavor, true)
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
								Message: `Can't admit new workloads: references missing ResourceFlavor(s): [tas-flavor].`,
							},
						}, util.IgnoreConditionTimestampsAndObservedGeneration))
					}, util.Timeout, util.Interval).Should(gomega.Succeed())
				})

				var wl *kueue.Workload
				ginkgo.By("creating a workload which requires block and can fit", func() {
					wl = testing.MakeWorkload("wl", ns.Name).
						Queue(localQueue.Name).PodSets(*testing.MakePodSet("worker", 2).
						RequiredTopologyRequest(testing.DefaultBlockTopologyLevel).
						Obj()).Request(corev1.ResourceCPU, "1").Obj()
					util.MustCreate(ctx, k8sClient, wl)
				})

				ginkgo.By("verify the workload is inadmissible", func() {
					util.ExpectPendingWorkloadsMetric(clusterQueue, 0, 1)
				})

				ginkgo.By("recreate the ResourceFlavor and wait for the queue to become active", func() {
					tasFlavor = testing.MakeResourceFlavor("tas-flavor").
						NodeLabel("node-group", "tas").
						TopologyName("default").Obj()
					util.MustCreate(ctx, k8sClient, tasFlavor)
					util.ExpectClusterQueuesToBeActive(ctx, k8sClient, clusterQueue)
				})

				ginkgo.By("verify the workload is admitted", func() {
					util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl)
					util.ExpectReservingActiveWorkloadsMetric(clusterQueue, 1)
				})

				ginkgo.By("verify admission for the workload", func() {
					gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), wl)).To(gomega.Succeed())
					gomega.Expect(wl.Status.Admission.PodSetAssignments[0].TopologyAssignment).Should(gomega.BeComparableTo(
						&kueue.TopologyAssignment{
							Levels: []string{testing.DefaultBlockTopologyLevel, testing.DefaultRackTopologyLevel},
							Domains: []kueue.TopologyDomainAssignment{
								{Count: 1, Values: []string{"b1", "r1"}},
								{Count: 1, Values: []string{"b1", "r2"}},
							},
						},
					))
				})
			})
		})

		ginkgo.When("Nodes are created before test with the hostname being the lowest level", func() {
			var (
				nodes []corev1.Node
			)
			ginkgo.BeforeEach(func() {
				nodes = []corev1.Node{
					*testingnode.MakeNode("x1").
						Label("node-group", "tas").
						Label(testing.DefaultBlockTopologyLevel, "b1").
						Label(testing.DefaultRackTopologyLevel, "r1").
						Label(corev1.LabelHostname, "x1").
						StatusAllocatable(corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("1"),
							corev1.ResourceMemory: resource.MustParse("1Gi"),
							corev1.ResourcePods:   resource.MustParse("10"),
						}).
						Ready().
						Obj(),
					*testingnode.MakeNode("x2").
						Label("node-group", "tas").
						Label(testing.DefaultBlockTopologyLevel, "b1").
						Label(testing.DefaultRackTopologyLevel, "r2").
						Label(corev1.LabelHostname, "x2").
						StatusAllocatable(corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("1"),
							corev1.ResourceMemory: resource.MustParse("1Gi"),
							corev1.ResourcePods:   resource.MustParse("10"),
						}).
						Ready().
						Obj(),
					*testingnode.MakeNode("x3").
						Label("node-group", "tas").
						Label(testing.DefaultBlockTopologyLevel, "b2").
						Label(testing.DefaultRackTopologyLevel, "r1").
						Label(corev1.LabelHostname, "x3").
						StatusAllocatable(corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("1"),
							corev1.ResourceMemory: resource.MustParse("1Gi"),
							corev1.ResourcePods:   resource.MustParse("10"),
						}).
						Ready().
						Obj(),
					*testingnode.MakeNode("x4").
						Label("node-group", "tas").
						Label(testing.DefaultBlockTopologyLevel, "b2").
						Label(testing.DefaultRackTopologyLevel, "r2").
						Label(corev1.LabelHostname, "x4").
						StatusAllocatable(corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("1"),
							corev1.ResourceMemory: resource.MustParse("1Gi"),
							corev1.ResourcePods:   resource.MustParse("10"),
						}).
						Ready().
						Obj(),
				}
				util.CreateNodesWithStatus(ctx, k8sClient, nodes)

				topology = testing.MakeDefaultThreeLevelTopology("default")
				util.MustCreate(ctx, k8sClient, topology)

				tasFlavor = testing.MakeResourceFlavor("tas-flavor").
					NodeLabel("node-group", "tas").
					TopologyName("default").Obj()
				util.MustCreate(ctx, k8sClient, tasFlavor)

				clusterQueue = testing.MakeClusterQueue("cluster-queue").
					ResourceGroup(*testing.MakeFlavorQuotas(tasFlavor.Name).Resource(corev1.ResourceCPU, "5").Obj()).
					Obj()
				util.MustCreate(ctx, k8sClient, clusterQueue)
				util.ExpectClusterQueuesToBeActive(ctx, k8sClient, clusterQueue)

				localQueue = testing.MakeLocalQueue("local-queue", ns.Name).ClusterQueue(clusterQueue.Name).Obj()
				util.MustCreate(ctx, k8sClient, localQueue)
			})

			ginkgo.AfterEach(func() {
				gomega.Expect(util.DeleteAllJobsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
				gomega.Expect(util.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
				gomega.Expect(util.DeleteObject(ctx, k8sClient, localQueue)).Should(gomega.Succeed())
				util.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
				util.ExpectObjectToBeDeleted(ctx, k8sClient, tasFlavor, true)
				util.ExpectObjectToBeDeleted(ctx, k8sClient, topology, true)
				for _, node := range nodes {
					util.ExpectObjectToBeDeleted(ctx, k8sClient, &node, true)
				}
			})

			ginkgo.It("should not admit workload which does not fit to the required topology domain", func() {
				ginkgo.By("creating a workload which requires rack, but does not fit in any", func() {
					wl1 := testing.MakeWorkload("wl1-inadmissible", ns.Name).
						PodSets(*testing.MakePodSet("worker", 4).
							PreferredTopologyRequest(testing.DefaultRackTopologyLevel).
							Obj()).
						Queue(localQueue.Name).Request(corev1.ResourceCPU, "2").Obj()
					util.MustCreate(ctx, k8sClient, wl1)
				})

				ginkgo.By("verify the workload is inadmissible", func() {
					util.ExpectPendingWorkloadsMetric(clusterQueue, 0, 1)
				})
			})

			ginkgo.It("should admit workload which fits", func() {
				var wl1, wl2 *kueue.Workload
				ginkgo.By("creating a workload which can fit", func() {
					wl1 = testing.MakeWorkload("wl1", ns.Name).
						PodSets(*testing.MakePodSet("worker", 1).
							PreferredTopologyRequest(testing.DefaultBlockTopologyLevel).
							Obj()).
						Queue(localQueue.Name).Request(corev1.ResourceCPU, "1").Obj()
					util.MustCreate(ctx, k8sClient, wl1)
				})

				ginkgo.By("verify the workload is admitted", func() {
					util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1)
					util.ExpectReservingActiveWorkloadsMetric(clusterQueue, 1)
				})

				ginkgo.By("creating second a workload which cannot fit", func() {
					wl2 = testing.MakeWorkload("wl2", ns.Name).
						PodSets(*testing.MakePodSet("worker-2", 4).
							PreferredTopologyRequest(testing.DefaultBlockTopologyLevel).
							Obj()).
						Queue(localQueue.Name).Request(corev1.ResourceCPU, "1").Obj()
					util.MustCreate(ctx, k8sClient, wl2)
				})

				ginkgo.By("verify the wl2 has not been admitted", func() {
					util.ExpectWorkloadsToBePending(ctx, k8sClient, wl2)
					util.ExpectReservingActiveWorkloadsMetric(clusterQueue, 1)
				})
			})

			ginkgo.It("should admit workload using TAS without explicit TAS annotation", func() {
				var wl1, wl2 *kueue.Workload
				ginkgo.By("creating a workload without explicit TAS annotation which can fit", func() {
					wl1 = testing.MakeWorkload("wl1", ns.Name).
						PodSets(*testing.MakePodSet("worker", 2).
							Obj()).
						Queue(localQueue.Name).Request(corev1.ResourceCPU, "1").Obj()
					util.MustCreate(ctx, k8sClient, wl1)
				})

				ginkgo.By("verify the workload is admitted", func() {
					util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1)
					gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl1), wl1)).To(gomega.Succeed())
					gomega.Expect(wl1.Status.Admission.PodSetAssignments[0].TopologyAssignment).Should(gomega.BeComparableTo(
						&kueue.TopologyAssignment{
							Levels: []string{corev1.LabelHostname},
							Domains: []kueue.TopologyDomainAssignment{
								{Count: 1, Values: []string{"x1"}},
								{Count: 1, Values: []string{"x2"}},
							},
						},
					))
				})

				ginkgo.By("creating the second workload without explicit TAS annotation which cannot fit", func() {
					wl2 = testing.MakeWorkload("wl2", ns.Name).
						PodSets(*testing.MakePodSet("worker-2", 3).
							Obj()).
						Queue(localQueue.Name).Request(corev1.ResourceCPU, "1").Obj()
					util.MustCreate(ctx, k8sClient, wl2)
				})

				ginkgo.By("verify the wl2 has not been admitted", func() {
					util.ExpectWorkloadsToBePending(ctx, k8sClient, wl2)
					util.ExpectReservingActiveWorkloadsMetric(clusterQueue, 1)
				})

				ginkgo.By("finish wl1 and verify wl2 can fit now", func() {
					util.FinishWorkloads(ctx, k8sClient, wl1)
					util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl2)
				})
			})
		})

		ginkgo.When("Preemption is enabled within ClusterQueue", func() {
			var (
				nodes []corev1.Node
			)
			ginkgo.BeforeEach(func() {
				nodes = []corev1.Node{
					*testingnode.MakeNode("x1").
						Label("node-group", "tas").
						Label(testing.DefaultBlockTopologyLevel, "b1").
						Label(testing.DefaultRackTopologyLevel, "r1").
						Label(corev1.LabelHostname, "x1").
						StatusAllocatable(corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("5"),
							corev1.ResourceMemory: resource.MustParse("5Gi"),
							corev1.ResourcePods:   resource.MustParse("10"),
						}).
						Ready().
						Obj(),
					*testingnode.MakeNode("x2").
						Label("node-group", "tas").
						Label(testing.DefaultBlockTopologyLevel, "b1").
						Label(testing.DefaultRackTopologyLevel, "r2").
						Label(corev1.LabelHostname, "x2").
						StatusAllocatable(corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("5"),
							corev1.ResourceMemory: resource.MustParse("5Gi"),
							corev1.ResourcePods:   resource.MustParse("10"),
						}).
						Ready().
						Obj(),
				}
				util.CreateNodesWithStatus(ctx, k8sClient, nodes)

				topology = testing.MakeDefaultThreeLevelTopology("default")
				util.MustCreate(ctx, k8sClient, topology)

				tasFlavor = testing.MakeResourceFlavor("tas-flavor").
					NodeLabel("node-group", "tas").
					TopologyName("default").Obj()
				util.MustCreate(ctx, k8sClient, tasFlavor)

				clusterQueue = testing.MakeClusterQueue("cluster-queue").
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue: kueue.PreemptionPolicyLowerPriority,
					}).
					// We set quota above what is physically available to hit
					// the topology requirements rather quota.
					ResourceGroup(*testing.MakeFlavorQuotas(tasFlavor.Name).
						Resource(corev1.ResourceCPU, "20").
						Resource(corev1.ResourceMemory, "20Gi").Obj()).
					Obj()
				util.MustCreate(ctx, k8sClient, clusterQueue)
				util.ExpectClusterQueuesToBeActive(ctx, k8sClient, clusterQueue)

				localQueue = testing.MakeLocalQueue("local-queue", ns.Name).ClusterQueue(clusterQueue.Name).Obj()
				util.MustCreate(ctx, k8sClient, localQueue)
			})

			ginkgo.AfterEach(func() {
				gomega.Expect(util.DeleteAllJobsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
				gomega.Expect(util.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
				gomega.Expect(util.DeleteObject(ctx, k8sClient, localQueue)).Should(gomega.Succeed())
				util.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
				util.ExpectObjectToBeDeleted(ctx, k8sClient, tasFlavor, true)
				util.ExpectObjectToBeDeleted(ctx, k8sClient, topology, true)
				for _, node := range nodes {
					util.ExpectObjectToBeDeleted(ctx, k8sClient, &node, true)
				}
			})

			ginkgo.It("should preempt the low and mid priority workloads to fit the high-priority workload", func() {
				var wl1, wl2, wl3 *kueue.Workload
				ginkgo.By("creating a low priority workload which can fit", func() {
					wl1 = testing.MakeWorkload("wl1", ns.Name).
						Priority(1).
						PodSets(*testing.MakePodSet("worker", 1).
							PreferredTopologyRequest(testing.DefaultBlockTopologyLevel).
							Obj()).
						Queue(localQueue.Name).Request(corev1.ResourceCPU, "5").Obj()
					util.MustCreate(ctx, k8sClient, wl1)
				})

				ginkgo.By("verify the workload is admitted", func() {
					util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1)
					util.ExpectReservingActiveWorkloadsMetric(clusterQueue, 1)
				})

				ginkgo.By("creating a mid priority workload which can fit", func() {
					wl2 = testing.MakeWorkload("wl2", ns.Name).
						Priority(2).
						PodSets(*testing.MakePodSet("worker", 1).
							PreferredTopologyRequest(testing.DefaultBlockTopologyLevel).
							Obj()).
						Queue(localQueue.Name).Request(corev1.ResourceCPU, "5").Obj()
					util.MustCreate(ctx, k8sClient, wl2)
				})

				ginkgo.By("verify the wl2 gets admitted", func() {
					util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1, wl2)
					util.ExpectReservingActiveWorkloadsMetric(clusterQueue, 2)
				})

				ginkgo.By("creating a high priority workload which requires preemption", func() {
					wl3 = testing.MakeWorkload("wl3", ns.Name).
						Priority(3).
						PodSets(*testing.MakePodSet("worker", 2).
							PreferredTopologyRequest(testing.DefaultBlockTopologyLevel).
							Obj()).
						Queue(localQueue.Name).Request(corev1.ResourceCPU, "5").Obj()
					util.MustCreate(ctx, k8sClient, wl3)
				})

				ginkgo.By("verify the wl3 gets admitted while wl1 is preempted", func() {
					util.ExpectWorkloadsToBePreempted(ctx, k8sClient, wl1, wl2)
					util.FinishEvictionForWorkloads(ctx, k8sClient, wl1, wl2)
					util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl3)
					util.ExpectReservingActiveWorkloadsMetric(clusterQueue, 1)
				})
			})
		})

		ginkgo.When("Preemption is enabled within Cohort", func() {
			var (
				nodes         []corev1.Node
				localQueueB   *kueue.LocalQueue
				clusterQueueB *kueue.ClusterQueue
			)
			ginkgo.BeforeEach(func() {
				nodes = []corev1.Node{
					*testingnode.MakeNode("x1").
						Label("node-group", "tas").
						Label(testing.DefaultBlockTopologyLevel, "b1").
						Label(testing.DefaultRackTopologyLevel, "r1").
						Label(corev1.LabelHostname, "x1").
						StatusAllocatable(corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("5"),
							corev1.ResourceMemory: resource.MustParse("5Gi"),
							corev1.ResourcePods:   resource.MustParse("10"),
						}).
						Ready().
						Obj(),
					*testingnode.MakeNode("x2").
						Label("node-group", "tas").
						Label(testing.DefaultBlockTopologyLevel, "b1").
						Label(testing.DefaultRackTopologyLevel, "r2").
						Label(corev1.LabelHostname, "x2").
						StatusAllocatable(corev1.ResourceList{
							corev1.ResourceCPU:    resource.MustParse("5"),
							corev1.ResourceMemory: resource.MustParse("5Gi"),
							corev1.ResourcePods:   resource.MustParse("10"),
						}).
						Ready().
						Obj(),
				}
				util.CreateNodesWithStatus(ctx, k8sClient, nodes)

				topology = testing.MakeDefaultThreeLevelTopology("default")
				util.MustCreate(ctx, k8sClient, topology)

				tasFlavor = testing.MakeResourceFlavor("tas-flavor").
					NodeLabel("node-group", "tas").
					TopologyName("default").Obj()
				util.MustCreate(ctx, k8sClient, tasFlavor)

				clusterQueue = testing.MakeClusterQueue("cluster-queue").
					Cohort("cohort").
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					}).
					ResourceGroup(*testing.MakeFlavorQuotas(tasFlavor.Name).
						Resource(corev1.ResourceCPU, "5").
						Resource(corev1.ResourceMemory, "5Gi").Obj()).
					Obj()
				util.MustCreate(ctx, k8sClient, clusterQueue)
				util.ExpectClusterQueuesToBeActive(ctx, k8sClient, clusterQueue)

				localQueue = testing.MakeLocalQueue("local-queue", ns.Name).ClusterQueue(clusterQueue.Name).Obj()
				util.MustCreate(ctx, k8sClient, localQueue)

				clusterQueueB = testing.MakeClusterQueue("cluster-queue-b").
					Cohort("cohort").
					Preemption(kueue.ClusterQueuePreemption{
						WithinClusterQueue:  kueue.PreemptionPolicyLowerPriority,
						ReclaimWithinCohort: kueue.PreemptionPolicyAny,
					}).
					ResourceGroup(*testing.MakeFlavorQuotas(tasFlavor.Name).
						Resource(corev1.ResourceCPU, "5").
						Resource(corev1.ResourceMemory, "5Gi").Obj()).
					Obj()
				util.MustCreate(ctx, k8sClient, clusterQueueB)
				util.ExpectClusterQueuesToBeActive(ctx, k8sClient, clusterQueueB)

				localQueueB = testing.MakeLocalQueue("local-queue-b", ns.Name).ClusterQueue(clusterQueueB.Name).Obj()
				util.MustCreate(ctx, k8sClient, localQueueB)
			})

			ginkgo.AfterEach(func() {
				gomega.Expect(util.DeleteAllJobsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
				gomega.Expect(util.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
				gomega.Expect(util.DeleteObject(ctx, k8sClient, localQueue)).Should(gomega.Succeed())
				gomega.Expect(util.DeleteObject(ctx, k8sClient, localQueueB)).Should(gomega.Succeed())

				util.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
				util.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueueB, true)
				util.ExpectObjectToBeDeleted(ctx, k8sClient, tasFlavor, true)
				util.ExpectObjectToBeDeleted(ctx, k8sClient, topology, true)
				for _, node := range nodes {
					util.ExpectObjectToBeDeleted(ctx, k8sClient, &node, true)
				}
			})

			ginkgo.It("should allow to borrow within cohort and reclaim the capacity", func() {
				var wl1, wl2 *kueue.Workload
				ginkgo.By("creating a workload which can fit only when borrowing quota", func() {
					wl1 = testing.MakeWorkload("wl1", ns.Name).
						PodSets(*testing.MakePodSet("worker", 2).
							PreferredTopologyRequest(testing.DefaultBlockTopologyLevel).
							Obj()).
						Queue(localQueue.Name).Request(corev1.ResourceCPU, "4").Obj()
					util.MustCreate(ctx, k8sClient, wl1)
				})

				ginkgo.By("verify the workload is admitted", func() {
					util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1)
					util.ExpectReservingActiveWorkloadsMetric(clusterQueue, 1)
				})

				ginkgo.By("creating a workload in CQB which reclaims its quota", func() {
					// Note that workload wl2 would still fit within regular quota
					// without preempting wl1. However, there is only 1CPU left
					// on both nodes, and so wl1 needs to be preempted  to allow
					// scheduling of wl2.
					wl2 = testing.MakeWorkload("wl2", ns.Name).
						Priority(2).
						PodSets(*testing.MakePodSet("worker", 1).
							PreferredTopologyRequest(testing.DefaultBlockTopologyLevel).
							Obj()).
						Queue(localQueueB.Name).Request(corev1.ResourceCPU, "2").Obj()
					util.MustCreate(ctx, k8sClient, wl2)
				})

				ginkgo.By("verify the wl1 gets preempted and wl2 gets admitted", func() {
					util.ExpectWorkloadsToBePreempted(ctx, k8sClient, wl1)
					util.FinishEvictionForWorkloads(ctx, k8sClient, wl1)
					util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl2)
					util.ExpectReservingActiveWorkloadsMetric(clusterQueueB, 1)
					util.ExpectReservingActiveWorkloadsMetric(clusterQueue, 0)
				})
			})
		})

		ginkgo.When("Node structure is mutated during test cases", func() {
			var (
				nodes []corev1.Node
			)

			ginkgo.BeforeEach(func() {
				ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "tas-")

				topology = testing.MakeDefaultTwoLevelTopology("default")
				util.MustCreate(ctx, k8sClient, topology)

				tasFlavor = testing.MakeResourceFlavor("tas-flavor").
					NodeLabel("node-group", "tas").
					TopologyName("default").
					Obj()
				util.MustCreate(ctx, k8sClient, tasFlavor)

				clusterQueue = testing.MakeClusterQueue("cluster-queue").
					ResourceGroup(*testing.MakeFlavorQuotas(tasFlavor.Name).Resource(corev1.ResourceCPU, "5").Obj()).
					Obj()
				util.MustCreate(ctx, k8sClient, clusterQueue)

				localQueue = testing.MakeLocalQueue("local-queue", ns.Name).ClusterQueue(clusterQueue.Name).Obj()
				util.MustCreate(ctx, k8sClient, localQueue)
			})

			ginkgo.AfterEach(func() {
				gomega.Expect(util.DeleteAllJobsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
				gomega.Expect(util.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
				gomega.Expect(util.DeleteObject(ctx, k8sClient, localQueue)).Should(gomega.Succeed())
				util.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
				util.ExpectObjectToBeDeleted(ctx, k8sClient, tasFlavor, true)
				util.ExpectObjectToBeDeleted(ctx, k8sClient, topology, true)
				for _, node := range nodes {
					util.ExpectObjectToBeDeleted(ctx, k8sClient, &node, true)
				}
			})

			ginkgo.It("should admit workload when nodes become available", func() {
				var (
					wl1 *kueue.Workload
				)

				ginkgo.By("creating a workload which requires rack, but does not fit in any", func() {
					wl1 = testing.MakeWorkload("wl1", ns.Name).
						Queue(localQueue.Name).Request(corev1.ResourceCPU, "1").Obj()
					wl1.Spec.PodSets[0].TopologyRequest = &kueue.PodSetTopologyRequest{
						Required: ptr.To(testing.DefaultRackTopologyLevel),
					}
					util.MustCreate(ctx, k8sClient, wl1)
				})

				ginkgo.By("verify the workload is inadmissible", func() {
					util.ExpectPendingWorkloadsMetric(clusterQueue, 0, 1)
				})

				ginkgo.By("Create nodes to allow scheduling", func() {
					nodes = []corev1.Node{
						*testingnode.MakeNode("b1-r1").
							Label("node-group", "tas").
							Label(testing.DefaultBlockTopologyLevel, "b1").
							Label(testing.DefaultRackTopologyLevel, "r1").
							StatusAllocatable(corev1.ResourceList{
								corev1.ResourceCPU:    resource.MustParse("1"),
								corev1.ResourceMemory: resource.MustParse("1Gi"),
								corev1.ResourcePods:   resource.MustParse("10"),
							}).
							Ready().
							Obj(),
					}
					util.CreateNodesWithStatus(ctx, k8sClient, nodes)
				})

				ginkgo.By("verify the workload is admitted", func() {
					util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1)
					util.ExpectReservingActiveWorkloadsMetric(clusterQueue, 1)
					util.ExpectPendingWorkloadsMetric(clusterQueue, 0, 0)
				})
			})
		})

		ginkgo.When("Node is mutated during test cases", func() {
			var (
				nodes []corev1.Node
			)

			ginkgo.BeforeEach(func() {
				ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "tas-")

				topology = testing.MakeDefaultThreeLevelTopology("default")
				util.MustCreate(ctx, k8sClient, topology)

				tasFlavor = testing.MakeResourceFlavor("tas-flavor").
					NodeLabel("node-group", "tas").
					TopologyName("default").
					Obj()
				util.MustCreate(ctx, k8sClient, tasFlavor)

				clusterQueue = testing.MakeClusterQueue("cluster-queue").
					ResourceGroup(*testing.MakeFlavorQuotas(tasFlavor.Name).Resource(corev1.ResourceCPU, "5").Obj()).
					Obj()
				util.MustCreate(ctx, k8sClient, clusterQueue)

				localQueue = testing.MakeLocalQueue("local-queue", ns.Name).ClusterQueue(clusterQueue.Name).Obj()
				util.MustCreate(ctx, k8sClient, localQueue)
			})

			ginkgo.AfterEach(func() {
				gomega.Expect(util.DeleteAllJobsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
				gomega.Expect(util.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
				gomega.Expect(util.DeleteObject(ctx, k8sClient, localQueue)).Should(gomega.Succeed())
				util.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
				util.ExpectObjectToBeDeleted(ctx, k8sClient, tasFlavor, true)
				util.ExpectObjectToBeDeleted(ctx, k8sClient, topology, true)
				for _, node := range nodes {
					util.ExpectObjectToBeDeleted(ctx, k8sClient, &node, true)
				}
			})

			ginkgo.It("should admit workload when node gets untainted", func() {
				var (
					wl1 *kueue.Workload
				)

				ginkgo.By("creating a tainted node which will prevent admitting the workload", func() {
					nodes = []corev1.Node{
						*testingnode.MakeNode("b1-r1-x1").
							Label("node-group", "tas").
							Label(testing.DefaultBlockTopologyLevel, "b1").
							Label(testing.DefaultRackTopologyLevel, "r1").
							Label(corev1.LabelHostname, "b1-r1-x1").
							StatusAllocatable(corev1.ResourceList{
								corev1.ResourceCPU:    resource.MustParse("1"),
								corev1.ResourceMemory: resource.MustParse("1Gi"),
								corev1.ResourcePods:   resource.MustParse("10"),
							}).
							Taints(corev1.Taint{
								Key:    "maintenance",
								Value:  "true",
								Effect: corev1.TaintEffectNoSchedule,
							}).
							Ready().
							Obj(),
					}
					util.CreateNodesWithStatus(ctx, k8sClient, nodes)
				})

				ginkgo.By("creating a workload which does not tolerate the taint", func() {
					wl1 = testing.MakeWorkload("wl1", ns.Name).
						Queue(localQueue.Name).Request(corev1.ResourceCPU, "1").Obj()
					wl1.Spec.PodSets[0].TopologyRequest = &kueue.PodSetTopologyRequest{
						Required: ptr.To(testing.DefaultRackTopologyLevel),
					}
					util.MustCreate(ctx, k8sClient, wl1)
				})

				ginkgo.By("verify the workload is inadmissible", func() {
					util.ExpectPendingWorkloadsMetric(clusterQueue, 0, 1)
				})

				ginkgo.By("Remove the node taint to allow scheduling", func() {
					nodes[0].Spec.Taints = nil
					for _, node := range nodes {
						gomega.Expect(k8sClient.Update(ctx, &node)).Should(gomega.Succeed())
					}
				})

				ginkgo.By("verify the workload gets admitted", func() {
					util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1)
					util.ExpectReservingActiveWorkloadsMetric(clusterQueue, 1)
					util.ExpectPendingWorkloadsMetric(clusterQueue, 0, 0)
				})
			})
		})
	})

	ginkgo.When("Multiple TAS Resource Flavors in queue", func() {
		var (
			tasGPUFlavor *kueue.ResourceFlavor
			tasCPUFlavor *kueue.ResourceFlavor
			topology     *kueuealpha.Topology
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
						Label(testing.DefaultRackTopologyLevel, "cpu-rack").
						StatusAllocatable(corev1.ResourceList{
							corev1.ResourceCPU:  resource.MustParse("5"),
							corev1.ResourcePods: resource.MustParse("10"),
						}).
						Ready().
						Obj(),
					*testingnode.MakeNode("gpu-node").
						Label(corev1.LabelInstanceTypeStable, "gpu-node").
						Label(testing.DefaultRackTopologyLevel, "gpu-rack").
						StatusAllocatable(corev1.ResourceList{
							gpuResName:          resource.MustParse("4"),
							corev1.ResourcePods: resource.MustParse("10"),
						}).
						Ready().
						Obj(),
				}
				util.CreateNodesWithStatus(ctx, k8sClient, nodes)

				topology = testing.MakeTopology("default").Levels(
					testing.DefaultRackTopologyLevel,
				).Obj()
				util.MustCreate(ctx, k8sClient, topology)

				tasGPUFlavor = testing.MakeResourceFlavor("tas-gpu-flavor").
					NodeLabel(corev1.LabelInstanceTypeStable, "gpu-node").
					TopologyName("default").
					Obj()
				util.MustCreate(ctx, k8sClient, tasGPUFlavor)

				tasCPUFlavor = testing.MakeResourceFlavor("tas-cpu-flavor").
					NodeLabel(corev1.LabelInstanceTypeStable, "cpu-node").
					TopologyName("default").
					Obj()
				util.MustCreate(ctx, k8sClient, tasCPUFlavor)

				clusterQueue = testing.MakeClusterQueue("cluster-queue").
					ResourceGroup(
						*testing.MakeFlavorQuotas(tasGPUFlavor.Name).Resource(corev1.ResourceCPU, "1").Resource(gpuResName, "5").Obj(),
						*testing.MakeFlavorQuotas(tasCPUFlavor.Name).Resource(corev1.ResourceCPU, "5").Resource(gpuResName, "0").Obj(),
					).Obj()
				util.MustCreate(ctx, k8sClient, clusterQueue)

				localQueue = testing.MakeLocalQueue("local-queue", ns.Name).ClusterQueue(clusterQueue.Name).Obj()
				util.MustCreate(ctx, k8sClient, localQueue)
			})

			ginkgo.AfterEach(func() {
				gomega.Expect(util.DeleteAllJobsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
				gomega.Expect(util.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
				gomega.Expect(util.DeleteObject(ctx, k8sClient, localQueue)).Should(gomega.Succeed())
				util.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
				util.ExpectObjectToBeDeleted(ctx, k8sClient, tasCPUFlavor, true)
				util.ExpectObjectToBeDeleted(ctx, k8sClient, tasGPUFlavor, true)
				util.ExpectObjectToBeDeleted(ctx, k8sClient, topology, true)
				for _, node := range nodes {
					util.ExpectObjectToBeDeleted(ctx, k8sClient, &node, true)
				}
			})

			ginkgo.It("should expose the TopologyName in LocalQueue status", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					createdLocalQueue := &kueue.LocalQueue{}
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(localQueue), createdLocalQueue)).Should(gomega.Succeed())
					g.Expect(createdLocalQueue.Status.Flavors).Should(gomega.HaveLen(2))
					for _, flavor := range createdLocalQueue.Status.Flavors {
						g.Expect(flavor.Topology).ShouldNot(gomega.BeNil())
						g.Expect(flavor.Topology.Levels).Should(gomega.Equal(utiltas.Levels(topology)))
					}
				}, util.Timeout, util.Interval).Should(gomega.Succeed())
			})

			ginkgo.It("should admit workload which fits in a required topology domain", func() {
				var wl1 *kueue.Workload
				ginkgo.By("creating a workload which requires block and can fit", func() {
					wl1 = testing.MakeWorkload("wl1", ns.Name).
						Queue(localQueue.Name).Obj()
					ps1 := *testing.MakePodSet("manager", 1).NodeSelector(
						map[string]string{corev1.LabelInstanceTypeStable: "cpu-node"},
					).Request(corev1.ResourceCPU, "5").Obj()
					ps1.TopologyRequest = &kueue.PodSetTopologyRequest{
						Required: ptr.To(testing.DefaultRackTopologyLevel),
					}
					ps2 := *testing.MakePodSet("worker", 2).NodeSelector(
						map[string]string{corev1.LabelInstanceTypeStable: "gpu-node"},
					).Request(gpuResName, "2").Obj()
					ps2.TopologyRequest = &kueue.PodSetTopologyRequest{
						Required: ptr.To(testing.DefaultRackTopologyLevel),
					}
					wl1.Spec.PodSets = []kueue.PodSet{ps1, ps2}
					util.MustCreate(ctx, k8sClient, wl1)
				})

				ginkgo.By("verify the workload is admitted", func() {
					util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1)
					util.ExpectReservingActiveWorkloadsMetric(clusterQueue, 1)
				})

				ginkgo.By("verify admission for the workload1", func() {
					gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl1), wl1)).To(gomega.Succeed())
					gomega.Expect(wl1.Status.Admission.PodSetAssignments[0].TopologyAssignment).Should(gomega.BeComparableTo(
						&kueue.TopologyAssignment{
							Levels: []string{
								testing.DefaultRackTopologyLevel,
							},
							Domains: []kueue.TopologyDomainAssignment{
								{
									Count: 1,
									Values: []string{
										"cpu-rack",
									},
								},
							},
						},
					))
					gomega.Expect(wl1.Status.Admission.PodSetAssignments[1].TopologyAssignment).Should(gomega.BeComparableTo(
						&kueue.TopologyAssignment{
							Levels: []string{
								testing.DefaultRackTopologyLevel,
							},
							Domains: []kueue.TopologyDomainAssignment{
								{
									Count: 2,
									Values: []string{
										"gpu-rack",
									},
								},
							},
						},
					))
				})
			})
		})
	})
})

var _ = ginkgo.Describe("Topology validations", func() {
	ginkgo.When("Creating a Topology", func() {
		ginkgo.DescribeTable("Validate Topology on creation", func(topology *kueuealpha.Topology, matcher types.GomegaMatcher) {
			err := k8sClient.Create(ctx, topology)
			if err == nil {
				defer func() {
					util.ExpectObjectToBeDeleted(ctx, k8sClient, topology, true)
				}()
			}
			gomega.Expect(err).Should(matcher)
		},
			ginkgo.Entry("valid Topology",
				testing.MakeDefaultOneLevelTopology("valid"),
				gomega.Succeed()),
			ginkgo.Entry("no levels",
				testing.MakeTopology("no-levels").Obj(),
				testing.BeInvalidError()),
			ginkgo.Entry("invalid levels",
				testing.MakeTopology("invalid-level").Levels("@invalid").Obj(),
				testing.BeInvalidError()),
			ginkgo.Entry("non-unique levels",
				testing.MakeTopology("default").Levels(testing.DefaultBlockTopologyLevel, testing.DefaultBlockTopologyLevel).Obj(),
				testing.BeInvalidError()),
			ginkgo.Entry("kubernetes.io/hostname first",
				testing.MakeTopology("default").Levels(corev1.LabelHostname, testing.DefaultBlockTopologyLevel, testing.DefaultRackTopologyLevel).Obj(),
				testing.BeInvalidError()),
			ginkgo.Entry("kubernetes.io/hostname middle",
				testing.MakeTopology("default").Levels(testing.DefaultBlockTopologyLevel, corev1.LabelHostname, testing.DefaultRackTopologyLevel).Obj(),
				testing.BeInvalidError()),
			ginkgo.Entry("kubernetes.io/hostname last",
				testing.MakeTopology("default").Levels(testing.DefaultBlockTopologyLevel, testing.DefaultRackTopologyLevel, corev1.LabelHostname).Obj(),
				gomega.Succeed()),
		)
	})

	ginkgo.When("Updating a Topology", func() {
		ginkgo.DescribeTable("Validate Topology on update", func(topology *kueuealpha.Topology, updateTopology func(topology *kueuealpha.Topology), matcher types.GomegaMatcher) {
			util.MustCreate(ctx, k8sClient, topology)
			defer func() {
				util.ExpectObjectToBeDeleted(ctx, k8sClient, topology, true)
			}()
			gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(topology), topology)).Should(gomega.Succeed())
			updateTopology(topology)
			gomega.Expect(k8sClient.Update(ctx, topology)).Should(matcher)
		},
			ginkgo.Entry("succeed to update topology",
				testing.MakeDefaultOneLevelTopology("valid"),
				func(topology *kueuealpha.Topology) {
					topology.ObjectMeta.Labels = map[string]string{
						"alpha": "beta",
					}
				},
				gomega.Succeed()),
			ginkgo.Entry("updating levels is prohibited",
				testing.MakeDefaultOneLevelTopology("valid"),
				func(topology *kueuealpha.Topology) {
					topology.Spec.Levels = append(topology.Spec.Levels, kueuealpha.TopologyLevel{
						NodeLabel: "added",
					})
				},
				testing.BeInvalidError()),
			ginkgo.Entry("updating levels order is prohibited",
				testing.MakeDefaultThreeLevelTopology("default"),
				func(topology *kueuealpha.Topology) {
					topology.Spec.Levels[0], topology.Spec.Levels[1] = topology.Spec.Levels[1], topology.Spec.Levels[0]
				},
				testing.BeInvalidError()),
		)
	})
})
