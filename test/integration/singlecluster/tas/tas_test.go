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
	"slices"
	"time"

	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	"github.com/onsi/gomega/types"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	apitypes "k8s.io/apimachinery/pkg/types"
	autoscaling "k8s.io/autoscaler/cluster-autoscaler/apis/provisioningrequest/autoscaling.x-k8s.io/v1beta1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueuealpha "sigs.k8s.io/kueue/apis/kueue/v1alpha1"
	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta1"
	"sigs.k8s.io/kueue/pkg/controller/admissionchecks/provisioning"
	"sigs.k8s.io/kueue/pkg/controller/tas"
	"sigs.k8s.io/kueue/pkg/features"
	utiltas "sigs.k8s.io/kueue/pkg/util/tas"
	"sigs.k8s.io/kueue/pkg/util/testing"
	testingnode "sigs.k8s.io/kueue/pkg/util/testingjobs/node"
	"sigs.k8s.io/kueue/pkg/workload"
	"sigs.k8s.io/kueue/test/util"
)

var _ = ginkgo.Describe("Topology Aware Scheduling", ginkgo.Ordered, func() {
	var (
		ns *corev1.Namespace
	)

	ginkgo.BeforeAll(func() {
		_ = features.SetEnable(features.TASFailedNodeReplacement, true)
		fwk.StartManager(ctx, cfg, managerSetup)
	})

	ginkgo.AfterAll(func() {
		fwk.StopManager(ctx)
		_ = features.SetEnable(features.TASFailedNodeReplacement, false)
	})

	ginkgo.BeforeEach(func() {
		_ = features.SetEnable(features.TopologyAwareScheduling, true)
		ns = util.CreateNamespaceFromPrefixWithLog(ctx, k8sClient, "tas-")
	})

	ginkgo.AfterEach(func() {
		gomega.Expect(util.DeleteNamespace(ctx, k8sClient, ns)).To(gomega.Succeed())
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
			tasFlavor.Spec.TopologyName = ptr.To[kueue.TopologyReference]("invalid")
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
						Queue(kueue.LocalQueueName(localQueue.Name)).Request(corev1.ResourceCPU, "2").Obj()
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
						Queue(kueue.LocalQueueName(localQueue.Name)).Request(corev1.ResourceCPU, "1").Obj()
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
						Queue(kueue.LocalQueueName(localQueue.Name)).Request(corev1.ResourceCPU, "1").Obj()
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
						Queue(kueue.LocalQueueName(localQueue.Name)).Request(corev1.ResourceCPU, "1").Obj()
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
						Queue(kueue.LocalQueueName(localQueue.Name)).Request(corev1.ResourceCPU, "1").Obj()
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

			ginkgo.It("should respect TAS usage by admitted workloads after reboot; second workload created before reboot", func() {
				var wl1, wl2, wl3 *kueue.Workload
				ginkgo.By("creating wl1 which consumes the entire TAS capacity", func() {
					wl1 = testing.MakeWorkload("wl1", ns.Name).
						Queue(kueue.LocalQueueName(localQueue.Name)).Request(corev1.ResourceCPU, "1").Obj()
					wl1.Spec.PodSets[0].Count = 4
					wl1.Spec.PodSets[0].TopologyRequest = &kueue.PodSetTopologyRequest{
						Preferred: ptr.To(testing.DefaultBlockTopologyLevel),
					}
					util.MustCreate(ctx, k8sClient, wl1)
				})

				ginkgo.By("verify wl1 is admitted", func() {
					util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1)
					util.ExpectReservingActiveWorkloadsMetric(clusterQueue, 1)
				})

				ginkgo.By("create wl2 which is blocked by wl1", func() {
					wl2 = testing.MakeWorkload("wl2", ns.Name).
						Queue(kueue.LocalQueueName(localQueue.Name)).Request(corev1.ResourceCPU, "1").Obj()
					wl2.Spec.PodSets[0].TopologyRequest = &kueue.PodSetTopologyRequest{
						Required: ptr.To(testing.DefaultRackTopologyLevel),
					}
					util.MustCreate(ctx, k8sClient, wl2)
				})

				ginkgo.By("very wl2 is not admitted", func() {
					util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1)
					util.ExpectWorkloadsToBePending(ctx, k8sClient, wl2)
					util.ExpectPendingWorkloadsMetric(clusterQueue, 0, 1)
				})

				ginkgo.By("restart controllers", func() {
					fwk.StopManager(ctx)
					fwk.StartManager(ctx, cfg, managerSetup)
				})

				ginkgo.By("verify wl2 is still not admitted", func() {
					util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1)
					util.ExpectPendingWorkloadsMetric(clusterQueue, 0, 1)
				})

				ginkgo.By("finish wl1 to demonstrate there is enough space for wl2", func() {
					util.FinishWorkloads(ctx, k8sClient, wl1)
				})

				ginkgo.By("create wl3 to ensure ClusterQueue is reconciled", func() {
					wl3 = testing.MakeWorkload("wl3", ns.Name).
						Queue(kueue.LocalQueueName(localQueue.Name)).Request(corev1.ResourceCPU, "1").Obj()
					wl3.Spec.PodSets[0].TopologyRequest = &kueue.PodSetTopologyRequest{
						Preferred: ptr.To(testing.DefaultRackTopologyLevel),
					}
					util.MustCreate(ctx, k8sClient, wl3)
				})

				ginkgo.By("verify wl2 and wl3 get admitted", func() {
					util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl2, wl3)
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
						Queue(kueue.LocalQueueName(localQueue.Name)).PodSets(*testing.MakePodSet("worker", 2).
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
								Message: `Can't admit new workloads: references missing ResourceFlavor(s): tas-flavor.`,
							},
						}, util.IgnoreConditionTimestampsAndObservedGeneration))
					}, util.Timeout, util.Interval).Should(gomega.Succeed())
				})

				var wl *kueue.Workload
				ginkgo.By("creating a workload which requires block and can fit", func() {
					wl = testing.MakeWorkload("wl", ns.Name).
						Queue(kueue.LocalQueueName(localQueue.Name)).PodSets(*testing.MakePodSet("worker", 2).
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
						Queue(kueue.LocalQueueName(localQueue.Name)).Request(corev1.ResourceCPU, "2").Obj()
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
						Queue(kueue.LocalQueueName(localQueue.Name)).Request(corev1.ResourceCPU, "1").Obj()
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
						Queue(kueue.LocalQueueName(localQueue.Name)).Request(corev1.ResourceCPU, "1").Obj()
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
						Queue(kueue.LocalQueueName(localQueue.Name)).Request(corev1.ResourceCPU, "1").Obj()
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
						Queue(kueue.LocalQueueName(localQueue.Name)).Request(corev1.ResourceCPU, "1").Obj()
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
			ginkgo.It("should update workload TopologyAssignment when node is removed", func() {
				var wl1 *kueue.Workload
				nodeName := nodes[0].Name

				ginkgo.By("creating a workload", func() {
					wl1 = testing.MakeWorkload("wl1", ns.Name).
						PodSets(*testing.MakePodSet("worker", 2).
							PreferredTopologyRequest(testing.DefaultBlockTopologyLevel).
							Obj()).
						Queue(kueue.LocalQueueName(localQueue.Name)).Request(corev1.ResourceCPU, "1").Obj()
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

				ginkgo.By("deleting the node", func() {
					nodeToUpdate := &corev1.Node{}
					gomega.Expect(k8sClient.Get(ctx, client.ObjectKey{Name: nodeName}, nodeToUpdate)).Should(gomega.Succeed())
					gomega.Expect(k8sClient.Delete(ctx, nodeToUpdate)).Should(gomega.Succeed())
				})

				ginkgo.By("verify the workload has corrected TopologyAssignment and no NodeToReplaceAnnotation", func() {
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl1), wl1)).To(gomega.Succeed())
						g.Expect(wl1.Status.Admission.PodSetAssignments[0].TopologyAssignment).Should(gomega.BeComparableTo(
							&kueue.TopologyAssignment{
								Levels: []string{corev1.LabelHostname},
								Domains: []kueue.TopologyDomainAssignment{
									{Count: 1, Values: []string{"x2"}},
									{Count: 1, Values: []string{"x3"}},
								},
							},
						))
						g.Expect(wl1.Annotations).NotTo(gomega.HaveKeyWithValue(kueuealpha.NodeToReplaceAnnotation, nodeName))
					}, util.Timeout, util.Interval).Should(gomega.Succeed())
				})
			})
			ginkgo.It("should update workload TopologyAssignment when node fails", func() {
				var wl1 *kueue.Workload
				nodeName := nodes[0].Name

				ginkgo.By("creating a workload", func() {
					wl1 = testing.MakeWorkload("wl1", ns.Name).
						PodSets(*testing.MakePodSet("worker", 2).
							PreferredTopologyRequest(testing.DefaultBlockTopologyLevel).
							Obj()).
						Queue(kueue.LocalQueueName(localQueue.Name)).Request(corev1.ResourceCPU, "1").Obj()
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

				ginkgo.By("making the node NotReady 30s in the past", func() {
					nodeToUpdate := &corev1.Node{}
					gomega.Expect(k8sClient.Get(ctx, apitypes.NamespacedName{Name: nodeName}, nodeToUpdate)).Should(gomega.Succeed())

					util.SetNodeCondition(ctx, k8sClient, nodeToUpdate, &corev1.NodeCondition{
						Type:               corev1.NodeReady,
						Status:             corev1.ConditionFalse,
						LastTransitionTime: metav1.NewTime(time.Now().Add(-tas.NodeFailureDelay)),
					})
				})

				ginkgo.By("verify the workload has corrected TopologyAssignment and no NodeToReplaceAnnotation", func() {
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl1), wl1)).To(gomega.Succeed())
						g.Expect(wl1.Status.Admission.PodSetAssignments[0].TopologyAssignment).Should(gomega.BeComparableTo(
							&kueue.TopologyAssignment{
								Levels: []string{corev1.LabelHostname},
								Domains: []kueue.TopologyDomainAssignment{
									{Count: 1, Values: []string{"x2"}},
									{Count: 1, Values: []string{"x3"}},
								},
							},
						))
						g.Expect(wl1.Annotations).NotTo(gomega.HaveKeyWithValue(kueuealpha.NodeToReplaceAnnotation, nodeName))
					}, util.Timeout, util.Interval).Should(gomega.Succeed())
				})
			})

			ginkgo.It("should update workload TopologyAssignment after a node recovers", func() {
				var wl1, wl2 *kueue.Workload
				nodeName := nodes[0].Name

				ginkgo.By("creating a workload", func() {
					wl1 = testing.MakeWorkload("wl1", ns.Name).
						PodSets(*testing.MakePodSet("worker", 2).
							PreferredTopologyRequest(testing.DefaultBlockTopologyLevel).
							Obj()).
						Queue(kueue.LocalQueueName(localQueue.Name)).Request(corev1.ResourceCPU, "1").Obj()
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

				ginkgo.By("creating a second workload", func() {
					wl2 = testing.MakeWorkload("wl2", ns.Name).
						PodSets(*testing.MakePodSet("worker", 2).
							PreferredTopologyRequest(testing.DefaultBlockTopologyLevel).
							Obj()).
						Queue(kueue.LocalQueueName(localQueue.Name)).Request(corev1.ResourceCPU, "1").Obj()
					util.MustCreate(ctx, k8sClient, wl2)
				})

				ginkgo.By("verify the workload is admitted", func() {
					util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl2)
					gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl2), wl2)).To(gomega.Succeed())
					gomega.Expect(wl2.Status.Admission.PodSetAssignments[0].TopologyAssignment).Should(gomega.BeComparableTo(
						&kueue.TopologyAssignment{
							Levels: []string{corev1.LabelHostname},
							Domains: []kueue.TopologyDomainAssignment{
								{Count: 1, Values: []string{"x3"}},
								{Count: 1, Values: []string{"x4"}},
							},
						},
					))
				})

				ginkgo.By("making the node NotReady 30s in the past", func() {
					nodeToUpdate := &corev1.Node{}
					gomega.Expect(k8sClient.Get(ctx, apitypes.NamespacedName{Name: nodeName}, nodeToUpdate)).Should(gomega.Succeed())

					util.SetNodeCondition(ctx, k8sClient, nodeToUpdate, &corev1.NodeCondition{
						Type:               corev1.NodeReady,
						Status:             corev1.ConditionFalse,
						LastTransitionTime: metav1.NewTime(time.Now().Add(-tas.NodeFailureDelay)),
					})
				})

				ginkgo.By("verify the workload has the same TopologyAssignment as there is no free capacity for replacement", func() {
					gomega.Consistently(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl1), wl1)).To(gomega.Succeed())
						g.Expect(wl1.Status.Admission.PodSetAssignments[0].TopologyAssignment).Should(gomega.BeComparableTo(
							&kueue.TopologyAssignment{
								Levels: []string{corev1.LabelHostname},
								Domains: []kueue.TopologyDomainAssignment{
									{Count: 1, Values: []string{"x1"}},
									{Count: 1, Values: []string{"x2"}},
								},
							},
						))
					}, util.ConsistentDuration, util.Interval).Should(gomega.Succeed())
				})

				ginkgo.By("Finishing second workload")
				util.FinishWorkloads(ctx, k8sClient, wl2)

				ginkgo.By("verify the workload has corrected TopologyAssignment and no NodeToReplaceAnnotation", func() {
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl1), wl1)).To(gomega.Succeed())
						g.Expect(wl1.Status.Admission.PodSetAssignments[0].TopologyAssignment).Should(gomega.BeComparableTo(
							&kueue.TopologyAssignment{
								Levels: []string{corev1.LabelHostname},
								Domains: []kueue.TopologyDomainAssignment{
									{Count: 1, Values: []string{"x2"}},
									{Count: 1, Values: []string{"x3"}},
								},
							},
						))
						g.Expect(wl1.Annotations).NotTo(gomega.HaveKeyWithValue(kueuealpha.NodeToReplaceAnnotation, nodeName))
					}, util.Timeout, util.Interval).Should(gomega.Succeed())
				})
			})

			ginkgo.It("should evict workload when multiple assigned nodes are deleted", func() {
				var wl1 *kueue.Workload
				node1Name := "x1"
				node2Name := "x2"

				ginkgo.By("creating a workload", func() {
					wl1 = testing.MakeWorkload("wl1", ns.Name).
						PodSets(*testing.MakePodSet("worker", 2).
							NodeSelector(map[string]string{testing.DefaultBlockTopologyLevel: "b1"}).
							PreferredTopologyRequest(testing.DefaultBlockTopologyLevel).
							Obj()).
						Queue(kueue.LocalQueueName(localQueue.Name)).Request(corev1.ResourceCPU, "1").Obj()
					util.MustCreate(ctx, k8sClient, wl1)
				})

				ginkgo.By("verify the workload is admitted", func() {
					util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1)
					gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl1), wl1)).To(gomega.Succeed())
					gomega.Expect(wl1.Status.Admission.PodSetAssignments[0].TopologyAssignment).Should(gomega.BeComparableTo(
						&kueue.TopologyAssignment{
							Levels: []string{corev1.LabelHostname},
							Domains: []kueue.TopologyDomainAssignment{
								{Count: 1, Values: []string{node1Name}},
								{Count: 1, Values: []string{node2Name}},
							},
						},
					))
				})

				ginkgo.By("deleting the first assigned node: "+node1Name, func() {
					nodeToDelete := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: node1Name}}
					gomega.Expect(k8sClient.Delete(ctx, nodeToDelete)).Should(gomega.Succeed())
					util.ExpectObjectToBeDeleted(ctx, k8sClient, nodeToDelete, false)
				})

				ginkgo.By("deleting the second assigned node: "+node2Name, func() {
					nodeToDelete := &corev1.Node{ObjectMeta: metav1.ObjectMeta{Name: node2Name}}
					gomega.Expect(k8sClient.Delete(ctx, nodeToDelete)).Should(gomega.Succeed())
					util.ExpectObjectToBeDeleted(ctx, k8sClient, nodeToDelete, false)
				})

				ginkgo.By("verify the workload is evicted due to multiple node failures", func() {
					util.FinishEvictionForWorkloads(ctx, k8sClient, wl1)
					gomega.Eventually(func(g gomega.Gomega) {
						updatedWl := &kueue.Workload{}
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl1), updatedWl)).To(gomega.Succeed())
						annotations := updatedWl.GetAnnotations()
						_, found := annotations[kueuealpha.NodeToReplaceAnnotation]
						g.Expect(found).To(gomega.BeFalse(), "NodeToReplaceAnnotation should be cleared after eviction due to multiple node failures")
					}, util.Timeout, util.Interval).Should(gomega.Succeed())
				})
			})

			ginkgo.It("should evict workload when multiple assigned nodes fail", func() {
				var wl1 *kueue.Workload
				node1Name := "x1"
				node2Name := "x2"

				ginkgo.By("creating a workload", func() {
					wl1 = testing.MakeWorkload("wl1", ns.Name).
						PodSets(*testing.MakePodSet("worker", 2).
							PreferredTopologyRequest(testing.DefaultBlockTopologyLevel).
							Obj()).
						Queue(kueue.LocalQueueName(localQueue.Name)).Request(corev1.ResourceCPU, "1").Obj()
					util.MustCreate(ctx, k8sClient, wl1)
				})

				ginkgo.By("verify the workload is admitted", func() {
					util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1)
					gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl1), wl1)).To(gomega.Succeed())
					gomega.Expect(wl1.Status.Admission.PodSetAssignments[0].TopologyAssignment).Should(gomega.BeComparableTo(
						&kueue.TopologyAssignment{
							Levels: []string{corev1.LabelHostname},
							Domains: []kueue.TopologyDomainAssignment{
								{Count: 1, Values: []string{node1Name}},
								{Count: 1, Values: []string{node2Name}},
							},
						},
					))
				})

				ginkgo.By("updating nodeReady condition of the first node", func() {
					nodeToUpdate := &corev1.Node{}
					gomega.Expect(k8sClient.Get(ctx, apitypes.NamespacedName{Name: node1Name}, nodeToUpdate)).Should(gomega.Succeed())
					util.SetNodeCondition(ctx, k8sClient, nodeToUpdate, &corev1.NodeCondition{
						Type:               corev1.NodeReady,
						Status:             corev1.ConditionFalse,
						LastTransitionTime: metav1.NewTime(time.Now().Add(-tas.NodeFailureDelay)),
					})
				})

				ginkgo.By("updating nodeReady condition of the second node", func() {
					nodeToUpdate := &corev1.Node{}
					gomega.Expect(k8sClient.Get(ctx, apitypes.NamespacedName{Name: node2Name}, nodeToUpdate)).Should(gomega.Succeed())
					util.SetNodeCondition(ctx, k8sClient, nodeToUpdate, &corev1.NodeCondition{
						Type:               corev1.NodeReady,
						Status:             corev1.ConditionFalse,
						LastTransitionTime: metav1.NewTime(time.Now().Add(-tas.NodeFailureDelay)),
					})
				})

				ginkgo.By("verify the workload is evicted due to multiple node failures", func() {
					util.FinishEvictionForWorkloads(ctx, k8sClient, wl1)
					gomega.Eventually(func(g gomega.Gomega) {
						updatedWl := &kueue.Workload{}
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl1), updatedWl)).To(gomega.Succeed())
						annotations := updatedWl.GetAnnotations()
						_, found := annotations[kueuealpha.NodeToReplaceAnnotation]
						g.Expect(found).To(gomega.BeFalse(), "NodeToReplaceAnnotation should be cleared after eviction due to multiple node failures")
					}, util.Timeout, util.Interval).Should(gomega.Succeed())
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
					// We set the quota above what is physically available to meet
					// the topology requirements rather than the quota.
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
						Queue(kueue.LocalQueueName(localQueue.Name)).Request(corev1.ResourceCPU, "5").Obj()
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
						Queue(kueue.LocalQueueName(localQueue.Name)).Request(corev1.ResourceCPU, "5").Obj()
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
						Queue(kueue.LocalQueueName(localQueue.Name)).Request(corev1.ResourceCPU, "5").Obj()
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
						Queue(kueue.LocalQueueName(localQueue.Name)).Request(corev1.ResourceCPU, "4").Obj()
					util.MustCreate(ctx, k8sClient, wl1)
				})

				ginkgo.By("verify the workload is admitted", func() {
					util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1)
					util.ExpectReservingActiveWorkloadsMetric(clusterQueue, 1)
				})

				ginkgo.By("creating a workload in CQB which reclaims its quota", func() {
					// Note that workload wl2 would still fit within the regular quota
					// without preempting wl1. However, there is only 1 CPU left
					// on both nodes, so wl1 needs to be preempted to allow
					// scheduling of wl2.
					wl2 = testing.MakeWorkload("wl2", ns.Name).
						Priority(2).
						PodSets(*testing.MakePodSet("worker", 1).
							PreferredTopologyRequest(testing.DefaultBlockTopologyLevel).
							Obj()).
						Queue(kueue.LocalQueueName(localQueueB.Name)).Request(corev1.ResourceCPU, "2").Obj()
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
						Queue(kueue.LocalQueueName(localQueue.Name)).Request(corev1.ResourceCPU, "1").Obj()
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
						Queue(kueue.LocalQueueName(localQueue.Name)).Request(corev1.ResourceCPU, "1").Obj()
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

			ginkgo.It("should admit workload when node gets required label added", func() {
				var (
					wl1 *kueue.Workload
				)
				customLabelKey := "custom-label-key-1"
				customLabelCorrectValue := "value-1"
				customLabelWrongValue := "value-2"

				ginkgo.By("creating a node missing the required label", func() {
					nodes = []corev1.Node{
						*testingnode.MakeNode("node-missing-label").
							Label("node-group", "tas").
							Label(testing.DefaultBlockTopologyLevel, "b1").
							Label(testing.DefaultRackTopologyLevel, "r1").
							Label(corev1.LabelHostname, "node-missing-label").
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

				ginkgo.By("creating a workload requiring the missing label via NodeSelector", func() {
					wl1 = testing.MakeWorkload("wl-needs-label", ns.Name).
						Queue(kueue.LocalQueueName(localQueue.Name)).
						PodSets(*testing.MakePodSet("main", 1).
							NodeSelector(map[string]string{customLabelKey: customLabelCorrectValue}).
							Request(corev1.ResourceCPU, "1").
							Obj()).
						Obj()
					util.MustCreate(ctx, k8sClient, wl1)
				})

				ginkgo.By("verify the workload is inadmissible due to missing label", func() {
					util.ExpectWorkloadsToBePending(ctx, k8sClient, wl1)
					util.ExpectPendingWorkloadsMetric(clusterQueue, 0, 1)
				})

				ginkgo.By("Add a label to the node with correct key but wrong value", func() {
					nodeToUpdate := &corev1.Node{}
					gomega.Expect(k8sClient.Get(ctx, client.ObjectKey{Name: nodes[0].Name}, nodeToUpdate)).Should(gomega.Succeed())
					nodeToUpdate.Labels[customLabelKey] = customLabelWrongValue
					gomega.Expect(k8sClient.Update(ctx, nodeToUpdate)).Should(gomega.Succeed())
				})

				ginkgo.By("verify the workload is inadmissible due to wrong value in the label", func() {
					util.ExpectWorkloadsToBePending(ctx, k8sClient, wl1)
					util.ExpectPendingWorkloadsMetric(clusterQueue, 0, 1)
				})

				ginkgo.By("Add the correct label to the node", func() {
					nodeToUpdate := &corev1.Node{}
					gomega.Expect(k8sClient.Get(ctx, client.ObjectKey{Name: nodes[0].Name}, nodeToUpdate)).Should(gomega.Succeed())
					nodeToUpdate.Labels[customLabelKey] = customLabelCorrectValue
					gomega.Expect(k8sClient.Update(ctx, nodeToUpdate)).Should(gomega.Succeed())
				})

				ginkgo.By("verify the workload gets admitted after label is added", func() {
					util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1)
					util.ExpectReservingActiveWorkloadsMetric(clusterQueue, 1)
					util.ExpectPendingWorkloadsMetric(clusterQueue, 0, 0)
				})
			})
		})

		ginkgo.When("ProvisioningRequest is used", func() {
			var (
				nodes             []corev1.Node
				ac                *kueue.AdmissionCheck
				prc               *kueue.ProvisioningRequestConfig
				createdRequest    autoscaling.ProvisioningRequest
				ignoreCqCondition = cmpopts.IgnoreFields(kueue.ClusterQueueStatus{}, "Conditions")
			)

			ginkgo.BeforeEach(func() {
				topology = testing.MakeDefaultThreeLevelTopology("default")
				util.MustCreate(ctx, k8sClient, topology)

				tasFlavor = testing.MakeResourceFlavor("tas-flavor").
					NodeLabel("node-group", "tas").
					TopologyName("default").
					Obj()
				util.MustCreate(ctx, k8sClient, tasFlavor)

				prc = testing.MakeProvisioningRequestConfig("prov-config").
					ProvisioningClass("provisioning-class").
					RetryLimit(1).
					BaseBackoff(1).
					PodSetUpdate(kueue.ProvisioningRequestPodSetUpdates{
						NodeSelector: []kueue.ProvisioningRequestPodSetUpdatesNodeSelector{{
							Key:                              "dedicated-selector-key",
							ValueFromProvisioningClassDetail: "dedicated-selector-detail",
						}},
					}).
					Obj()
				util.MustCreate(ctx, k8sClient, prc)

				ac = testing.MakeAdmissionCheck("provisioning").
					ControllerName(kueue.ProvisioningRequestControllerName).
					Parameters(kueue.GroupVersion.Group, "ProvisioningRequestConfig", prc.Name).
					Obj()
				util.MustCreate(ctx, k8sClient, ac)
				util.SetAdmissionCheckActive(ctx, k8sClient, ac, metav1.ConditionTrue)

				clusterQueue = testing.MakeClusterQueue("cq").
					ResourceGroup(
						*testing.MakeFlavorQuotas(tasFlavor.Name).Resource(corev1.ResourceCPU, "5").Obj(),
					).AdmissionChecks(kueue.AdmissionCheckReference(ac.Name)).Obj()
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
				util.ExpectObjectToBeDeleted(ctx, k8sClient, ac, true)
				util.ExpectObjectToBeDeleted(ctx, k8sClient, prc, true)
				util.ExpectObjectToBeDeleted(ctx, k8sClient, &createdRequest, true)
				for _, node := range nodes {
					util.ExpectObjectToBeDeleted(ctx, k8sClient, &node, true)
				}
			})

			ginkgo.It("should admit workload when nodes are provisioned; Nodes ready after small delay", func() {
				var (
					wl1 *kueue.Workload
				)

				ginkgo.By("create workload", func() {
					wl1 = testing.MakeWorkload("wl1", ns.Name).
						Queue(kueue.LocalQueueName(localQueue.Name)).Request(corev1.ResourceCPU, "1").Obj()
					wl1.Spec.PodSets[0] = *testing.MakePodSet(kueue.DefaultPodSetName, 1).
						Request(corev1.ResourceCPU, "1").
						PreferredTopologyRequest(testing.DefaultRackTopologyLevel).
						Image("image").
						Obj()
					util.MustCreate(ctx, k8sClient, wl1)
				})

				wlKey := client.ObjectKeyFromObject(wl1)

				ginkgo.By("verify the workload reserves the quota", func() {
					util.ExpectQuotaReservedWorkloadsTotalMetric(clusterQueue, 1)
					util.ExpectReservingActiveWorkloadsMetric(clusterQueue, 1)
					util.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, clusterQueue.Name, wl1)
					gomega.Eventually(func(g gomega.Gomega) {
						gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl1), wl1)).To(gomega.Succeed())
						gomega.Expect(workload.HasTopologyAssignmentsPending(wl1)).Should(gomega.BeTrue())
					}, util.Timeout, util.Interval).Should(gomega.Succeed())
				})

				provReqKey := apitypes.NamespacedName{
					Namespace: wlKey.Namespace,
					Name:      provisioning.ProvisioningRequestName(wlKey.Name, kueue.AdmissionCheckReference(ac.Name), 1),
				}

				ginkgo.By("await for the ProvisioningRequest to be created", func() {
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, provReqKey, &createdRequest)).Should(gomega.Succeed())
					}, util.Timeout, util.Interval).Should(gomega.Succeed())
				})

				ginkgo.By("provision the node which is not ready yet", func() {
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
							NotReady().
							Obj(),
					}
					util.CreateNodesWithStatus(ctx, k8sClient, nodes)
				})

				ginkgo.By("set the ProvisioningRequest as Provisioned", func() {
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, provReqKey, &createdRequest)).Should(gomega.Succeed())
						apimeta.SetStatusCondition(&createdRequest.Status.Conditions, metav1.Condition{
							Type:   autoscaling.Provisioned,
							Status: metav1.ConditionTrue,
							Reason: autoscaling.Provisioned,
						})
						g.Expect(k8sClient.Status().Update(ctx, &createdRequest)).Should(gomega.Succeed())
					}, util.Timeout, util.Interval).Should(gomega.Succeed())
				})

				ginkgo.By("verify there is no TAS assignment for the workload yet", func() {
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, wlKey, wl1)).To(gomega.Succeed())
						g.Expect(wl1.Status.Admission).ShouldNot(gomega.BeNil())
						g.Expect(wl1.Status.Admission.PodSetAssignments).Should(gomega.HaveLen(1))
						g.Expect(wl1.Status.Admission.PodSetAssignments[0].TopologyAssignment).Should(gomega.BeNil())
					}, util.Timeout, util.Interval).Should(gomega.Succeed())
				})

				ginkgo.By("make the node Ready", func() {
					createdNode := &corev1.Node{}
					gomega.Expect(k8sClient.Get(ctx, client.ObjectKey{Name: nodes[0].Name}, createdNode)).Should(gomega.Succeed())

					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(createdNode), createdNode)).Should(gomega.Succeed())
						createdNode.Status.Conditions = []corev1.NodeCondition{{
							Type:   corev1.NodeReady,
							Status: corev1.ConditionTrue,
						}}
						g.Expect(k8sClient.Status().Update(ctx, createdNode)).Should(gomega.Succeed())
					}, util.Timeout, util.Interval).Should(gomega.Succeed())

					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(createdNode), createdNode)).Should(gomega.Succeed())
						createdNode.Spec.Taints = slices.DeleteFunc(createdNode.Spec.Taints, func(taint corev1.Taint) bool {
							return taint.Key == corev1.TaintNodeNotReady
						})
						g.Expect(k8sClient.Update(ctx, createdNode)).Should(gomega.Succeed())
					}, util.Timeout, util.Interval).Should(gomega.Succeed())
				})

				ginkgo.By("verify TAS admission for the workload", func() {
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, wlKey, wl1)).To(gomega.Succeed())
						g.Expect(wl1.Status.Admission).ShouldNot(gomega.BeNil())
						g.Expect(wl1.Status.Admission.PodSetAssignments).Should(gomega.HaveLen(1))
						g.Expect(wl1.Status.Admission.PodSetAssignments[0].TopologyAssignment).Should(gomega.BeEquivalentTo(
							&kueue.TopologyAssignment{
								Levels: []string{
									corev1.LabelHostname,
								},
								Domains: []kueue.TopologyDomainAssignment{
									{
										Count: 1,
										Values: []string{
											"x1",
										},
									},
								},
							},
						))
					}, util.Timeout, util.Interval).Should(gomega.Succeed())
				})

				ginkgo.By("verify the workload is admitted", func() {
					util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1)
					util.ExpectReservingActiveWorkloadsMetric(clusterQueue, 1)
					util.ExpectPendingWorkloadsMetric(clusterQueue, 0, 0)
				})
			})

			ginkgo.It("should admit workload targeting the dedicated newly provisioned nodes", func() {
				var (
					wl1 *kueue.Workload
				)

				ginkgo.By("create workload", func() {
					wl1 = testing.MakeWorkload("wl1", ns.Name).
						Queue(kueue.LocalQueueName(localQueue.Name)).Request(corev1.ResourceCPU, "1").Obj()
					wl1.Spec.PodSets[0] = *testing.MakePodSet(kueue.DefaultPodSetName, 1).
						Request(corev1.ResourceCPU, "1").
						PreferredTopologyRequest(testing.DefaultRackTopologyLevel).
						Image("image").
						Obj()
					util.MustCreate(ctx, k8sClient, wl1)
				})

				wlKey := client.ObjectKeyFromObject(wl1)

				ginkgo.By("verify the workload reserves the quota", func() {
					util.ExpectQuotaReservedWorkloadsTotalMetric(clusterQueue, 1)
					util.ExpectReservingActiveWorkloadsMetric(clusterQueue, 1)
					util.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, clusterQueue.Name, wl1)
					gomega.Eventually(func(g gomega.Gomega) {
						gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl1), wl1)).To(gomega.Succeed())
						gomega.Expect(workload.HasTopologyAssignmentsPending(wl1)).Should(gomega.BeTrue())
					}, util.Timeout, util.Interval).Should(gomega.Succeed())
				})

				provReqKey := apitypes.NamespacedName{
					Namespace: wlKey.Namespace,
					Name:      provisioning.ProvisioningRequestName(wlKey.Name, kueue.AdmissionCheckReference(ac.Name), 1),
				}

				ginkgo.By("await for the ProvisioningRequest to be created", func() {
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, provReqKey, &createdRequest)).Should(gomega.Succeed())
					}, util.Timeout, util.Interval).Should(gomega.Succeed())
				})

				ginkgo.By("provision the nodes, only one of them matches the ProvisiningRequest", func() {
					nodes = []corev1.Node{
						*testingnode.MakeNode("x1").
							Label("node-group", "tas").
							Label("dedicated-selector-key", "dedicated-selector-value-abc").
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
							Label("dedicated-selector-key", "dedicated-selector-value-xyz").
							Label(testing.DefaultBlockTopologyLevel, "b1").
							Label(testing.DefaultRackTopologyLevel, "r2").
							Label(corev1.LabelHostname, "x2").
							StatusAllocatable(corev1.ResourceList{
								corev1.ResourceCPU:    resource.MustParse("2"),
								corev1.ResourceMemory: resource.MustParse("2Gi"),
								corev1.ResourcePods:   resource.MustParse("10"),
							}).
							Ready().
							Obj(),
					}
					util.CreateNodesWithStatus(ctx, k8sClient, nodes)
				})

				ginkgo.By("set the ProvisioningRequest as Provisioned", func() {
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, provReqKey, &createdRequest)).Should(gomega.Succeed())
						apimeta.SetStatusCondition(&createdRequest.Status.Conditions, metav1.Condition{
							Type:   autoscaling.Provisioned,
							Status: metav1.ConditionTrue,
							Reason: autoscaling.Provisioned,
						})
						createdRequest.Status.ProvisioningClassDetails = make(map[string]autoscaling.Detail)
						createdRequest.Status.ProvisioningClassDetails["dedicated-selector-detail"] = "dedicated-selector-value-xyz"
						g.Expect(k8sClient.Status().Update(ctx, &createdRequest)).Should(gomega.Succeed())
					}, util.Timeout, util.Interval).Should(gomega.Succeed())
				})

				ginkgo.By("verify admission for the workload", func() {
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, wlKey, wl1)).To(gomega.Succeed())
						g.Expect(wl1.Status.Admission).ShouldNot(gomega.BeNil())
						g.Expect(wl1.Status.Admission.PodSetAssignments).Should(gomega.HaveLen(1))
						g.Expect(wl1.Status.Admission.PodSetAssignments[0].TopologyAssignment).Should(gomega.BeEquivalentTo(
							&kueue.TopologyAssignment{
								Levels: []string{
									corev1.LabelHostname,
								},
								Domains: []kueue.TopologyDomainAssignment{
									{
										Count: 1,
										Values: []string{
											"x2",
										},
									},
								},
							},
						))
					}, util.Timeout, util.Interval).Should(gomega.Succeed())
				})

				ginkgo.By("verify the workload is admitted", func() {
					util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1)
					util.ExpectReservingActiveWorkloadsMetric(clusterQueue, 1)
					util.ExpectPendingWorkloadsMetric(clusterQueue, 0, 0)
				})
			})

			ginkgo.It("should admit workload when nodes are provisioned and account for quota usage correctly", func() {
				var (
					wl1 *kueue.Workload
				)

				ginkgo.By("creating a workload", func() {
					wl1 = testing.MakeWorkload("wl1", ns.Name).
						Queue(kueue.LocalQueueName(localQueue.Name)).Request(corev1.ResourceCPU, "1").Obj()
					wl1.Spec.PodSets[0] = *testing.MakePodSet(kueue.DefaultPodSetName, 1).
						Request(corev1.ResourceCPU, "1").
						PreferredTopologyRequest(testing.DefaultRackTopologyLevel).
						Image("image").
						Obj()
					util.MustCreate(ctx, k8sClient, wl1)
				})

				wlKey := client.ObjectKeyFromObject(wl1)

				ginkgo.By("verify the workload reserves the quota", func() {
					util.ExpectQuotaReservedWorkloadsTotalMetric(clusterQueue, 1)
					util.ExpectReservingActiveWorkloadsMetric(clusterQueue, 1)
					util.ExpectWorkloadsToHaveQuotaReservation(ctx, k8sClient, clusterQueue.Name, wl1)
					gomega.Eventually(func(g gomega.Gomega) {
						gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl1), wl1)).To(gomega.Succeed())
						gomega.Expect(workload.HasTopologyAssignmentsPending(wl1)).Should(gomega.BeTrue())
					}, util.Timeout, util.Interval).Should(gomega.Succeed())
				})

				ginkgo.By("check queue resource consumption before full admission", func() {
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(clusterQueue), clusterQueue)).To(gomega.Succeed())
						g.Expect(clusterQueue.Status).Should(gomega.BeComparableTo(kueue.ClusterQueueStatus{
							PendingWorkloads:   0,
							ReservingWorkloads: 1,
							AdmittedWorkloads:  0,
							FlavorsReservation: []kueue.FlavorUsage{{
								Name: kueue.ResourceFlavorReference(tasFlavor.Name),
								Resources: []kueue.ResourceUsage{{
									Name:  corev1.ResourceCPU,
									Total: resource.MustParse("1"),
								}},
							}},
							FlavorsUsage: []kueue.FlavorUsage{{
								Name: kueue.ResourceFlavorReference(tasFlavor.Name),
								Resources: []kueue.ResourceUsage{{
									Name: corev1.ResourceCPU,
								}},
							}},
						}, ignoreCqCondition))
					}, util.Timeout, util.Interval).Should(gomega.Succeed())
				})

				provReqKey := apitypes.NamespacedName{
					Namespace: wlKey.Namespace,
					Name:      provisioning.ProvisioningRequestName(wlKey.Name, kueue.AdmissionCheckReference(ac.Name), 1),
				}

				ginkgo.By("await for the ProvisioningRequest to be created", func() {
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, provReqKey, &createdRequest)).Should(gomega.Succeed())
					}, util.Timeout, util.Interval).Should(gomega.Succeed())
				})

				ginkgo.By("provision the nodes which are ready", func() {
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
					}
					util.CreateNodesWithStatus(ctx, k8sClient, nodes)
				})

				ginkgo.By("set the ProvisioningRequest as Provisioned", func() {
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, provReqKey, &createdRequest)).Should(gomega.Succeed())
						apimeta.SetStatusCondition(&createdRequest.Status.Conditions, metav1.Condition{
							Type:   autoscaling.Provisioned,
							Status: metav1.ConditionTrue,
							Reason: autoscaling.Provisioned,
						})
						g.Expect(k8sClient.Status().Update(ctx, &createdRequest)).Should(gomega.Succeed())
					}, util.Timeout, util.Interval).Should(gomega.Succeed())
				})

				ginkgo.By("verify admission for the workload contains the TopologyAssignment", func() {
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, wlKey, wl1)).To(gomega.Succeed())
						g.Expect(wl1.Status.Admission).ShouldNot(gomega.BeNil())
						g.Expect(wl1.Status.Admission.PodSetAssignments).Should(gomega.HaveLen(1))
						g.Expect(wl1.Status.Admission.PodSetAssignments[0].TopologyAssignment).Should(gomega.BeEquivalentTo(
							&kueue.TopologyAssignment{
								Levels: []string{
									corev1.LabelHostname,
								},
								Domains: []kueue.TopologyDomainAssignment{
									{
										Count: 1,
										Values: []string{
											"x1",
										},
									},
								},
							},
						))
					}, util.Timeout, util.Interval).Should(gomega.Succeed())
				})

				ginkgo.By("Verify the workload is admitted", func() {
					util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1)
					util.ExpectReservingActiveWorkloadsMetric(clusterQueue, 1)
					util.ExpectPendingWorkloadsMetric(clusterQueue, 0, 0)
				})

				ginkgo.By("Check queue resource consumption after full admission", func() {
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(clusterQueue), clusterQueue)).To(gomega.Succeed())
						g.Expect(clusterQueue.Status).Should(gomega.BeComparableTo(kueue.ClusterQueueStatus{
							PendingWorkloads:   0,
							ReservingWorkloads: 1,
							AdmittedWorkloads:  1,
							FlavorsReservation: []kueue.FlavorUsage{{
								Name: kueue.ResourceFlavorReference(tasFlavor.Name),
								Resources: []kueue.ResourceUsage{{
									Name:  corev1.ResourceCPU,
									Total: resource.MustParse("1"),
								}},
							}},
							FlavorsUsage: []kueue.FlavorUsage{{
								Name: kueue.ResourceFlavorReference(tasFlavor.Name),
								Resources: []kueue.ResourceUsage{{
									Name:  corev1.ResourceCPU,
									Total: resource.MustParse("1"),
								}},
							}},
						}, ignoreCqCondition))
					}, util.Timeout, util.Interval).Should(gomega.Succeed())
				})
			})

			ginkgo.It("should admit workload when nodes are provisioned; manager restart", func() {
				var (
					wl1 *kueue.Workload
				)

				ginkgo.By("create workload", func() {
					wl1 = testing.MakeWorkload("wl1", ns.Name).
						Queue(kueue.LocalQueueName(localQueue.Name)).Request(corev1.ResourceCPU, "1").Obj()
					wl1.Spec.PodSets[0] = *testing.MakePodSet(kueue.DefaultPodSetName, 1).
						Request(corev1.ResourceCPU, "1").
						PreferredTopologyRequest(testing.DefaultRackTopologyLevel).
						Image("image").
						Obj()
					util.MustCreate(ctx, k8sClient, wl1)
				})

				wlKey := client.ObjectKeyFromObject(wl1)

				ginkgo.By("verify the workload reserves the quota", func() {
					util.ExpectQuotaReservedWorkloadsTotalMetric(clusterQueue, 1)
					util.ExpectReservingActiveWorkloadsMetric(clusterQueue, 1)
				})

				provReqKey := apitypes.NamespacedName{
					Namespace: wlKey.Namespace,
					Name:      provisioning.ProvisioningRequestName(wlKey.Name, kueue.AdmissionCheckReference(ac.Name), 1),
				}

				ginkgo.By("await for the ProvisioningRequest to be created", func() {
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, provReqKey, &createdRequest)).Should(gomega.Succeed())
					}, util.Timeout, util.Interval).Should(gomega.Succeed())
				})

				ginkgo.By("provision the nodes", func() {
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
					}
					util.CreateNodesWithStatus(ctx, k8sClient, nodes)
				})

				ginkgo.By("set the ProvisioningRequest as Provisioned", func() {
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, provReqKey, &createdRequest)).Should(gomega.Succeed())
						apimeta.SetStatusCondition(&createdRequest.Status.Conditions, metav1.Condition{
							Type:   autoscaling.Provisioned,
							Status: metav1.ConditionTrue,
							Reason: autoscaling.Provisioned,
						})
						g.Expect(k8sClient.Status().Update(ctx, &createdRequest)).Should(gomega.Succeed())
					}, util.Timeout, util.Interval).Should(gomega.Succeed())
				})

				ginkgo.By("await for the check to be ready", func() {
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, wlKey, wl1)).To(gomega.Succeed())
						state := workload.FindAdmissionCheck(wl1.Status.AdmissionChecks, kueue.AdmissionCheckReference(ac.Name))
						g.Expect(state).NotTo(gomega.BeNil())
						g.Expect(state.State).To(gomega.Equal(kueue.CheckStateReady))
					}, util.Timeout, time.Millisecond).Should(gomega.Succeed())
				})

				ginkgo.By("restart Kueue manager", func() {
					fwk.StopManager(ctx)
					fwk.StartManager(ctx, cfg, managerSetup)
				})

				ginkgo.By("verify admission for the workload", func() {
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, wlKey, wl1)).To(gomega.Succeed())
						g.Expect(wl1.Status.Admission).ShouldNot(gomega.BeNil())
						g.Expect(wl1.Status.Admission.PodSetAssignments).Should(gomega.HaveLen(1))
						g.Expect(wl1.Status.Admission.PodSetAssignments[0].TopologyAssignment).Should(gomega.BeEquivalentTo(
							&kueue.TopologyAssignment{
								Levels: []string{
									corev1.LabelHostname,
								},
								Domains: []kueue.TopologyDomainAssignment{
									{
										Count: 1,
										Values: []string{
											"x1",
										},
									},
								},
							},
						))
					}, util.Timeout, util.Interval).Should(gomega.Succeed())
				})

				ginkgo.By("verify the workload is admitted", func() {
					util.ExpectReservingActiveWorkloadsMetric(clusterQueue, 1)
					util.ExpectPendingWorkloadsMetric(clusterQueue, 0, 0)
					util.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1)
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
						Queue(kueue.LocalQueueName(localQueue.Name)).Obj()
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
					topology.Labels = map[string]string{
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
