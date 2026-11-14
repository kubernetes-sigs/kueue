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
	"encoding/json"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	apimeta "k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	apitypes "k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/controller/tas"
	"sigs.k8s.io/kueue/pkg/features"
	"sigs.k8s.io/kueue/pkg/metrics"
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

	ginkgo.Context("Topology and flavor configuration", func() {
		ginkgo.When("Delete Topology", func() {
			var (
				tasFlavor    *kueue.ResourceFlavor
				topology     *kueue.Topology
				clusterQueue *kueue.ClusterQueue
			)

			ginkgo.AfterEach(func() {
				behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
				behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, tasFlavor, true)
				behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, topology, true)
			})

			ginkgo.When("ResourceFlavor does not exist", func() {
				ginkgo.BeforeEach(func() {
					topology = utiltestingapi.MakeDefaultOneLevelTopology("topology")
					behavioral.MustCreate(ctx, k8sClient, topology)
				})

				ginkgo.It("should allow to delete topology", func() {
					behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, topology, true)
				})
			})

			ginkgo.When("ResourceFlavor exists", func() {
				ginkgo.BeforeEach(func() {
					tasFlavor = utiltestingapi.MakeResourceFlavor("tas-flavor").
						NodeLabel("node-group", "tas").
						TopologyName("topology").Obj()
					behavioral.MustCreate(ctx, k8sClient, tasFlavor)

					topology = utiltestingapi.MakeDefaultOneLevelTopology("topology")
					behavioral.MustCreate(ctx, k8sClient, topology)

					clusterQueue = utiltestingapi.MakeClusterQueue("cq").
						ResourceGroup(
							*utiltestingapi.MakeFlavorQuotas(tasFlavor.Name).
								Resource(corev1.ResourceCPU, "5").
								Obj(),
						).Obj()
					behavioral.MustCreate(ctx, k8sClient, clusterQueue)
				})

				ginkgo.It("should not allow to delete topology", func() {
					// A ClusterQueue is considered active only if its ResourceFlavors are present in the cache.
					// We need to wait for the ClusterQueue to ensure the ResourceFlavors are cached.
					ginkgo.By("waiting for the ClusterQueue to become active", func() {
						behavioral.ExpectClusterQueuesToBeActive(ctx, k8sClient, clusterQueue)
					})

					createdTopology := &kueue.Topology{}

					ginkgo.By("check topology has finalizer", func() {
						gomega.Eventually(func(g gomega.Gomega) {
							g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(topology), createdTopology)).Should(gomega.Succeed())
							g.Expect(createdTopology.Finalizers).Should(gomega.ContainElement(kueue.ResourceInUseFinalizerName))
						}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
					})

					ginkgo.By("delete topology", func() {
						gomega.Expect(k8sClient.Delete(ctx, topology)).Should(gomega.Succeed())
					})

					ginkgo.By("check topology still present", func() {
						gomega.Consistently(func(g gomega.Gomega) {
							g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(topology), createdTopology)).Should(gomega.Succeed())
							g.Expect(createdTopology.Finalizers).Should(gomega.ContainElement(kueue.ResourceInUseFinalizerName))
						}, behavioral.ConsistentDuration, behavioral.ShortInterval).Should(gomega.Succeed())
					})
				})
			})
		})

		ginkgo.When("Negative scenarios for ResourceFlavor configuration", func() {
			ginkgo.It("should not allow to create ResourceFlavor with invalid topology name", func() {
				tasFlavor := utiltestingapi.MakeResourceFlavor("tas-flavor").
					NodeLabel("node-group", "tas").
					TopologyName("invalid topology name").Obj()
				gomega.Expect(k8sClient.Create(ctx, tasFlavor)).Should(gomega.HaveOccurred())
			})

			ginkgo.It("should not allow to create ResourceFlavor without any node labels", func() {
				tasFlavor := utiltestingapi.MakeResourceFlavor("tas-flavor").TopologyName("default").Obj()
				gomega.Expect(k8sClient.Create(ctx, tasFlavor)).Should(gomega.HaveOccurred())
			})
		})

		ginkgo.When("Updating ResourceFlavorSpec that defines TopologyName", func() {
			var tasFlavor *kueue.ResourceFlavor

			ginkgo.BeforeEach(func() {
				tasFlavor = utiltestingapi.MakeResourceFlavor("tas-flavor").
					TopologyName("default").
					NodeLabel("node-group", "tas").
					NodeLabel("foo", "bar").
					Obj()
				behavioral.MustCreate(ctx, k8sClient, tasFlavor)
			})

			ginkgo.AfterEach(func() {
				behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, tasFlavor, true)
			})

			ginkgo.It("should not allow to update topologyName", func() {
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(tasFlavor), tasFlavor)).To(gomega.Succeed())
				tasFlavor.Spec.TopologyName = new(kueue.TopologyReference("invalid"))
				gomega.Expect(k8sClient.Update(ctx, tasFlavor)).Should(gomega.HaveOccurred())
			})

			ginkgo.It("should not allow to unset topologyName", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					var updated kueue.ResourceFlavor
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(tasFlavor), &updated)).To(gomega.Succeed())
					updated.Spec.TopologyName = nil
					g.Expect(k8sClient.Update(ctx, &updated)).To(gomega.And(
						utiltesting.BeInvalidError(),
						gomega.MatchError(gomega.ContainSubstring("topologyName is immutable when topologyName is set")),
					))
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			})

			ginkgo.It("should not allow to remove all nodeLabels", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					var updated kueue.ResourceFlavor
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(tasFlavor), &updated)).To(gomega.Succeed())
					updated.Spec.NodeLabels = nil
					g.Expect(k8sClient.Update(ctx, &updated)).To(gomega.And(
						utiltesting.BeInvalidError(),
						gomega.MatchError(gomega.ContainSubstring("at least one nodeLabel is required when topology is set")),
					))
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			})

			ginkgo.It("should allow to update nodeTaints", func() {
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(tasFlavor), tasFlavor)).To(gomega.Succeed())
				tasFlavor.Spec.NodeTaints = []corev1.Taint{{
					Key:    "foo",
					Value:  "bar",
					Effect: corev1.TaintEffectNoSchedule,
				}}
				gomega.Expect(k8sClient.Update(ctx, tasFlavor)).Should(gomega.Succeed())
			})

			ginkgo.It("should allow to update tolerations", func() {
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(tasFlavor), tasFlavor)).To(gomega.Succeed())
				tasFlavor.Spec.Tolerations = []corev1.Toleration{
					{Key: "key1", Value: "value", Effect: corev1.TaintEffectNoSchedule, Operator: corev1.TolerationOpEqual}}
				gomega.Expect(k8sClient.Update(ctx, tasFlavor)).Should(gomega.Succeed())
			})

			ginkgo.It("should allow to update nodeLabels", func() {
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(tasFlavor), tasFlavor)).To(gomega.Succeed())
				tasFlavor.Spec.NodeLabels = map[string]string{
					"tas-node": "true",
				}
				gomega.Expect(k8sClient.Update(ctx, tasFlavor)).Should(gomega.Succeed())
			})

			ginkgo.It("should allow to remove one of the nodeLabels", func() {
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(tasFlavor), tasFlavor)).To(gomega.Succeed())
				tasFlavor.Spec.NodeLabels = map[string]string{
					"foo": "bar",
				}
				gomega.Expect(k8sClient.Update(ctx, tasFlavor)).Should(gomega.Succeed())
			})
		})

		ginkgo.When("Updating TAS ResourceFlavor tolerations", func() {
			var (
				node         *corev1.Node
				topology     *kueue.Topology
				tasFlavor    *kueue.ResourceFlavor
				clusterQueue *kueue.ClusterQueue
				localQueue   *kueue.LocalQueue
			)

			taint := corev1.Taint{
				Key:    "example.com/dedicated",
				Value:  "tas",
				Effect: corev1.TaintEffectNoSchedule,
			}
			toleration := corev1.Toleration{
				Key:      taint.Key,
				Operator: corev1.TolerationOpEqual,
				Value:    taint.Value,
				Effect:   taint.Effect,
			}

			ginkgo.BeforeEach(func() {
				node = testingnode.MakeNode("x1").
					Label("node-group", "tas").
					Label(corev1.LabelHostname, "x1").
					Taints(taint).
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("2"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj()
				integration.CreateNodesWithStatus(ctx, k8sClient, []corev1.Node{*node})

				topology = utiltestingapi.MakeDefaultOneLevelTopology("default")
				behavioral.MustCreate(ctx, k8sClient, topology)

				tasFlavor = utiltestingapi.MakeResourceFlavor("tas-flavor").
					NodeLabel("node-group", "tas").
					TopologyName(topology.Name).
					Obj()
				behavioral.MustCreate(ctx, k8sClient, tasFlavor)

				clusterQueue = utiltestingapi.MakeClusterQueue("cluster-queue").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas(tasFlavor.Name).
						Resource(corev1.ResourceCPU, "3").
						Obj()).
					Obj()
				behavioral.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, clusterQueue)

				localQueue = utiltestingapi.MakeLocalQueue("local-queue", ns.Name).
					ClusterQueue(clusterQueue.Name).
					Obj()
				behavioral.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, localQueue)
			})

			ginkgo.AfterEach(func() {
				gomega.Expect(behavioral.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
				gomega.Expect(behavioral.DeleteObject(ctx, k8sClient, localQueue)).Should(gomega.Succeed())
				behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
				behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, tasFlavor, true)
				behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, topology, true)
				behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, node, true)
			})

			ginkgo.It("should apply updates, requeue pending workloads, and preserve admitted usage", func() {
				makeWorkload := func(name, cpu string, podTolerations ...corev1.Toleration) *kueue.Workload {
					podSet := utiltestingapi.MakePodSet("worker", 1).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, cpu)
					for _, podToleration := range podTolerations {
						podSet.Toleration(podToleration)
					}
					return utiltestingapi.MakeWorkload(name, ns.Name).
						Queue(kueue.LocalQueueName(localQueue.Name)).
						PodSets(*podSet.Obj()).
						Obj()
				}
				updateTolerations := func(tolerations ...corev1.Toleration) {
					ginkgo.GinkgoHelper()
					gomega.Eventually(func(g gomega.Gomega) {
						var updatedFlavor kueue.ResourceFlavor
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(tasFlavor), &updatedFlavor)).To(gomega.Succeed())
						updatedFlavor.Spec.Tolerations = tolerations
						g.Expect(k8sClient.Update(ctx, &updatedFlavor)).To(gomega.Succeed())
					}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
				}

				ginkgo.By("creating a workload that cannot tolerate the node taint", func() {
					wl1 := makeWorkload("wl1", "1")
					behavioral.MustCreate(ctx, k8sClient, wl1)
					behavioral.ExpectWorkloadsToBePending(ctx, k8sClient, wl1)

					ginkgo.By("adding the toleration and verifying the pending workload is retried")
					updateTolerations(toleration)
					behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1)

					ginkgo.By("creating a capacity probe with its own toleration")
					capacityProbe := makeWorkload("capacity-probe", "2", toleration)
					behavioral.MustCreate(ctx, k8sClient, capacityProbe)
					behavioral.ExpectWorkloadsToBePending(ctx, k8sClient, capacityProbe)

					ginkgo.By("removing the flavor toleration without losing admitted usage")
					metrics.AdmissionAttemptsTotal.Reset()
					updateTolerations()
					behavioral.ExpectPendingAdmissionAttempts(1, ">=")
					behavioral.ExpectWorkloadsToBePending(ctx, k8sClient, capacityProbe)
					behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, capacityProbe, true)

					ginkgo.By("verifying future workloads no longer tolerate the taint")
					wl2 := makeWorkload("wl2", "1")
					behavioral.MustCreate(ctx, k8sClient, wl2)
					behavioral.ExpectWorkloadsToBePending(ctx, k8sClient, wl2)

					ginkgo.By("restoring the toleration and verifying the workload is retried")
					updateTolerations(toleration)
					behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl2)

					ginkgo.By("verifying both admitted workloads still account for the full node capacity")
					wl3 := makeWorkload("wl3", "1")
					behavioral.MustCreate(ctx, k8sClient, wl3)
					behavioral.ExpectWorkloadsToBePending(ctx, k8sClient, wl3)
				})
			})
		})

		ginkgo.When("Updating TAS ResourceFlavor nodeTaints", func() {
			var (
				node         *corev1.Node
				topology     *kueue.Topology
				tasFlavor    *kueue.ResourceFlavor
				clusterQueue *kueue.ClusterQueue
				localQueue   *kueue.LocalQueue
			)

			taint := corev1.Taint{
				Key:    "example.com/dedicated",
				Value:  "tas",
				Effect: corev1.TaintEffectNoSchedule,
			}

			ginkgo.BeforeEach(func() {
				node = testingnode.MakeNode("x1").
					Label("node-group", "tas").
					Label(corev1.LabelHostname, "x1").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("2"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj()
				integration.CreateNodesWithStatus(ctx, k8sClient, []corev1.Node{*node})

				topology = utiltestingapi.MakeDefaultOneLevelTopology("default")
				behavioral.MustCreate(ctx, k8sClient, topology)

				tasFlavor = utiltestingapi.MakeResourceFlavor("tas-flavor").
					NodeLabel("node-group", "tas").
					TopologyName(topology.Name).
					Taint(taint).
					Obj()
				behavioral.MustCreate(ctx, k8sClient, tasFlavor)

				clusterQueue = utiltestingapi.MakeClusterQueue("cluster-queue").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas(tasFlavor.Name).
						Resource(corev1.ResourceCPU, "3").
						Obj()).
					Obj()
				behavioral.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, clusterQueue)

				localQueue = utiltestingapi.MakeLocalQueue("local-queue", ns.Name).
					ClusterQueue(clusterQueue.Name).
					Obj()
				behavioral.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, localQueue)
			})

			ginkgo.AfterEach(func() {
				gomega.Expect(behavioral.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
				gomega.Expect(behavioral.DeleteObject(ctx, k8sClient, localQueue)).Should(gomega.Succeed())
				behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
				behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, tasFlavor, true)
				behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, topology, true)
				behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, node, true)
			})

			makeWorkload := func(name, cpu string) *kueue.Workload {
				return utiltestingapi.MakeWorkload(name, ns.Name).
					Queue(kueue.LocalQueueName(localQueue.Name)).
					PodSets(*utiltestingapi.MakePodSet("worker", 1).
						RequiredTopologyRequest(corev1.LabelHostname).
						Request(corev1.ResourceCPU, cpu).
						Obj()).
					Obj()
			}
			removeNodeTaints := func() {
				ginkgo.GinkgoHelper()
				gomega.Eventually(func(g gomega.Gomega) {
					var updatedFlavor kueue.ResourceFlavor
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(tasFlavor), &updatedFlavor)).To(gomega.Succeed())
					updatedFlavor.Spec.NodeTaints = nil
					g.Expect(k8sClient.Update(ctx, &updatedFlavor)).To(gomega.Succeed())
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			}

			ginkgo.It("should requeue pending workloads when nodeTaints are removed", func() {
				wl1 := makeWorkload("wl1", "1")
				ginkgo.By("creating a workload that cannot tolerate the flavor nodeTaints", func() {
					behavioral.MustCreate(ctx, k8sClient, wl1)
					behavioral.ExpectWorkloadsToBePending(ctx, k8sClient, wl1)
				})

				ginkgo.By("removing the flavor nodeTaints", removeNodeTaints)

				ginkgo.By("verifying a differently-shaped workload created after the update is admitted", func() {
					wl2 := makeWorkload("wl2", "500m")
					behavioral.MustCreate(ctx, k8sClient, wl2)
					behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl2)
				})

				ginkgo.By("verifying the pending workload is retried and admitted", func() {
					behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1)
				})
			})

			ginkgo.It("should schedule workloads created after nodeTaints are removed", func() {
				wl1 := makeWorkload("wl1", "1")
				ginkgo.By("creating a workload that cannot tolerate the flavor nodeTaints", func() {
					behavioral.MustCreate(ctx, k8sClient, wl1)
					behavioral.ExpectWorkloadsToBePending(ctx, k8sClient, wl1)
				})

				ginkgo.By("removing the flavor nodeTaints", removeNodeTaints)

				ginkgo.By("verifying an identically-shaped workload created after the update is admitted", func() {
					wl2 := makeWorkload("wl2", "1")
					behavioral.MustCreate(ctx, k8sClient, wl2)
					behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl2)
				})

				ginkgo.By("verifying the pending workload is also retried and admitted", func() {
					behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1)
				})
			})
		})

		ginkgo.When("Updating Topology levels", func() {
			var (
				nodes      []corev1.Node
				topology   *kueue.Topology
				tasFlavor  *kueue.ResourceFlavor
				cq         *kueue.ClusterQueue
				localQueue *kueue.LocalQueue
			)

			ginkgo.BeforeEach(func() {
				nodes = []corev1.Node{
					*testingnode.MakeNode("y1").
						Label("node-group", "tas").
						Label("cloud.provider.com/rack", "r1").
						Label("cloud.provider.com/block", "b1").
						Label("cloud.provider.com/zone", "z1").
						Label(corev1.LabelHostname, "y1").
						StatusAllocatable(corev1.ResourceList{
							corev1.ResourceCPU:  resource.MustParse("2"),
							corev1.ResourcePods: resource.MustParse("10"),
						}).
						Ready().
						Obj(),
					*testingnode.MakeNode("y2").
						Label("node-group", "tas").
						Label("cloud.provider.com/rack", "r2").
						Label("cloud.provider.com/block", "b2").
						Label(corev1.LabelHostname, "y2").
						StatusAllocatable(corev1.ResourceList{
							corev1.ResourceCPU:  resource.MustParse("2"),
							corev1.ResourcePods: resource.MustParse("10"),
						}).
						Ready().
						Obj(),
				}
				integration.CreateNodesWithStatus(ctx, k8sClient, nodes)

				topology = utiltestingapi.MakeTopology("levels-topology").
					Levels("cloud.provider.com/rack", corev1.LabelHostname).
					Obj()
				behavioral.MustCreate(ctx, k8sClient, topology)

				tasFlavor = utiltestingapi.MakeResourceFlavor("levels-tas-flavor").
					NodeLabel("node-group", "tas").
					TopologyName(topology.Name).
					Obj()
				behavioral.MustCreate(ctx, k8sClient, tasFlavor)

				cq = utiltestingapi.MakeClusterQueue("levels-cluster-queue").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas(tasFlavor.Name).
						Resource(corev1.ResourceCPU, "6").
						Obj()).
					Obj()
				behavioral.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, cq)

				localQueue = utiltestingapi.MakeLocalQueue("levels-local-queue", ns.Name).
					ClusterQueue(cq.Name).
					Obj()
				behavioral.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, localQueue)
			})

			ginkgo.AfterEach(func() {
				gomega.Expect(behavioral.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
				gomega.Expect(behavioral.DeleteObject(ctx, k8sClient, localQueue)).Should(gomega.Succeed())
				behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, cq, true)
				behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, tasFlavor, true)
				behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, topology, true)
				for i := range nodes {
					behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, &nodes[i], true)
				}
			})

			updateLevels := func(levels ...string) error {
				var updated kueue.Topology
				if err := k8sClient.Get(ctx, client.ObjectKeyFromObject(topology), &updated); err != nil {
					return err
				}
				updated.Spec.Levels = make([]kueue.TopologyLevel, len(levels))
				for i, level := range levels {
					updated.Spec.Levels[i] = kueue.TopologyLevel{NodeLabel: level}
				}
				return k8sClient.Update(ctx, &updated)
			}

			makeHostnameWorkload := func(name, cpu string) *kueue.Workload {
				podSet := utiltestingapi.MakePodSet("worker", 1).
					RequiredTopologyRequest(corev1.LabelHostname).
					Request(corev1.ResourceCPU, cpu)
				return utiltestingapi.MakeWorkload(name, ns.Name).
					Queue(kueue.LocalQueueName(localQueue.Name)).
					PodSets(*podSet.Obj()).
					Obj()
			}

			ginkgo.It("should allow mutating levels only when hostname stays the lowest level", func() {
				ginkgo.By("renaming a middle level with hostname lowest before and after", func() {
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(updateLevels("cloud.provider.com/block", corev1.LabelHostname)).To(gomega.Succeed())
					}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
				})

				ginkgo.By("rejecting an update that drops the hostname level", func() {
					// Eventually: a conflict with the reconciler adding the finalizer
					// must be retried until the CEL rejection itself is observed.
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(updateLevels("cloud.provider.com/block")).
							To(gomega.MatchError(gomega.ContainSubstring("levels are mutable only when")))
					}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
				})

				ginkgo.By("rejecting any update of a topology whose lowest level is not hostname", func() {
					rackOnly := utiltestingapi.MakeTopology("rack-only").
						Levels("cloud.provider.com/rack").
						Obj()
					behavioral.MustCreate(ctx, k8sClient, rackOnly)
					gomega.Eventually(func(g gomega.Gomega) {
						var updated kueue.Topology
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(rackOnly), &updated)).To(gomega.Succeed())
						updated.Spec.Levels = []kueue.TopologyLevel{{NodeLabel: "cloud.provider.com/block"}}
						g.Expect(k8sClient.Update(ctx, &updated)).
							To(gomega.MatchError(gomega.ContainSubstring("levels are mutable only when")))
					}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
					behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, rackOnly, true)
				})
			})

			ginkgo.It("should requeue a workload pending on a missing level after the level is added", func() {
				podSet := utiltestingapi.MakePodSet("worker", 1).
					RequiredTopologyRequest("cloud.provider.com/block").
					Request(corev1.ResourceCPU, "1")
				wl := utiltestingapi.MakeWorkload("wl-block", ns.Name).
					Queue(kueue.LocalQueueName(localQueue.Name)).
					PodSets(*podSet.Obj()).
					Obj()

				ginkgo.By("creating a workload requiring a level absent from the topology", func() {
					behavioral.MustCreate(ctx, k8sClient, wl)
					behavioral.ExpectWorkloadsToBePending(ctx, k8sClient, wl)
				})

				ginkgo.By("adding the missing level to the topology", func() {
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(updateLevels("cloud.provider.com/block", "cloud.provider.com/rack", corev1.LabelHostname)).To(gomega.Succeed())
					}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
				})

				ginkgo.By("verifying the pending workload is retried and admitted", func() {
					behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl)
				})
			})

			ginkgo.It("should preserve admitted usage when a middle level is renamed", func() {
				wl1 := makeHostnameWorkload("wl1", "1")
				ginkgo.By("admitting a workload under the original levels", func() {
					behavioral.MustCreate(ctx, k8sClient, wl1)
					behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1)
				})

				ginkgo.By("renaming the middle level", func() {
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(updateLevels("cloud.provider.com/block", corev1.LabelHostname)).To(gomega.Succeed())
					}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
				})

				wl2 := makeHostnameWorkload("wl2", "2")
				ginkgo.By("admitting a workload that only fits on the untouched node", func() {
					behavioral.MustCreate(ctx, k8sClient, wl2)
					behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl2)
				})

				wl3 := makeHostnameWorkload("wl3", "2")
				ginkgo.By("verifying capacity consumed before the rename is still accounted", func() {
					// One node has 1 CPU left (wl1) and the other is full (wl2): if
					// wl1's usage had been dropped on the rename, wl3 would fit.
					behavioral.MustCreate(ctx, k8sClient, wl3)
					behavioral.ExpectWorkloadsToBePending(ctx, k8sClient, wl3)
					behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1)
				})
			})

			ginkgo.It("should maintain usage when a level above hostname is removed", func() {
				wlA := makeHostnameWorkload("wl-a", "2")
				wlB := makeHostnameWorkload("wl-b", "2")
				ginkgo.By("admitting workloads taking all node capacity under the original levels", func() {
					behavioral.MustCreate(ctx, k8sClient, wlA)
					behavioral.MustCreate(ctx, k8sClient, wlB)
					behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wlA, wlB)
				})

				ginkgo.By("removing the rack level", func() {
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(updateLevels(corev1.LabelHostname)).To(gomega.Succeed())
					}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
				})

				wlC := makeHostnameWorkload("wl-c", "1")
				ginkgo.By("verifying a new workload remains blocked by the carried-over usage", func() {
					behavioral.MustCreate(ctx, k8sClient, wlC)
					behavioral.ExpectWorkloadsToBePending(ctx, k8sClient, wlC)
					behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wlA, wlB)
				})
			})

			ginkgo.It("should not evict an admitted workload that would violate a newly added level", func() {
				ginkgo.By("reducing the topology to hostname only", func() {
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(updateLevels(corev1.LabelHostname)).To(gomega.Succeed())
					}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
				})

				extraNode := testingnode.MakeNode("y3").
					Label("node-group", "tas").
					Label("cloud.provider.com/rack", "r1").
					Label("cloud.provider.com/block", "b1").
					Label(corev1.LabelHostname, "y3").
					StatusAllocatable(corev1.ResourceList{
						corev1.ResourceCPU:  resource.MustParse("1"),
						corev1.ResourcePods: resource.MustParse("10"),
					}).
					Ready().
					Obj()
				integration.CreateNodesWithStatus(ctx, k8sClient, []corev1.Node{*extraNode})
				ginkgo.DeferCleanup(func() {
					behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, extraNode, true)
				})

				spanPodSet := utiltestingapi.MakePodSet("worker", 2).
					PreferredTopologyRequest(corev1.LabelHostname).
					Request(corev1.ResourceCPU, "2")
				wlSpan := utiltestingapi.MakeWorkload("wl-span", ns.Name).
					Queue(kueue.LocalQueueName(localQueue.Name)).
					PodSets(*spanPodSet.Obj()).
					Obj()
				ginkgo.By("admitting a workload that spans both racks under hostname-only levels", func() {
					behavioral.MustCreate(ctx, k8sClient, wlSpan)
					behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wlSpan)
				})

				ginkgo.By("adding the rack level", func() {
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(updateLevels("cloud.provider.com/rack", corev1.LabelHostname)).To(gomega.Succeed())
					}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
				})

				ginkgo.By("verifying the spanning workload is not evicted", func() {
					// Consistently: a final admitted check alone would also pass if the
					// workload was evicted and quickly re-admitted.
					gomega.Consistently(func(g gomega.Gomega) {
						var updated kueue.Workload
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wlSpan), &updated)).To(gomega.Succeed())
						g.Expect(workload.IsAdmitted(&updated)).To(gomega.BeTrue())
					}, behavioral.ConsistentDuration, behavioral.ShortInterval).Should(gomega.Succeed())
				})

				rackPodSet := utiltestingapi.MakePodSet("worker", 1).
					RequiredTopologyRequest("cloud.provider.com/rack").
					Request(corev1.ResourceCPU, "1")
				wlSmall := utiltestingapi.MakeWorkload("wl-small", ns.Name).
					Queue(kueue.LocalQueueName(localQueue.Name)).
					PodSets(*rackPodSet.Obj()).
					Obj()
				ginkgo.By("admitting a small workload next to it within one rack", func() {
					behavioral.MustCreate(ctx, k8sClient, wlSmall)
					behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wlSmall)
				})
			})

			ginkgo.It("should keep admitted workloads and block new nominations for a removed level", func() {
				makeRackWorkload := func(name string) *kueue.Workload {
					podSet := utiltestingapi.MakePodSet("worker", 1).
						RequiredTopologyRequest("cloud.provider.com/rack").
						Request(corev1.ResourceCPU, "1")
					return utiltestingapi.MakeWorkload(name, ns.Name).
						Queue(kueue.LocalQueueName(localQueue.Name)).
						PodSets(*podSet.Obj()).
						Obj()
				}

				wlRack := makeRackWorkload("wl-rack")
				ginkgo.By("admitting a workload requiring the rack level", func() {
					behavioral.MustCreate(ctx, k8sClient, wlRack)
					behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wlRack)
				})

				ginkgo.By("removing the rack level", func() {
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(updateLevels(corev1.LabelHostname)).To(gomega.Succeed())
					}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
				})

				ginkgo.By("verifying the admitted workload is not disturbed", func() {
					gomega.Consistently(func(g gomega.Gomega) {
						var updated kueue.Workload
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wlRack), &updated)).To(gomega.Succeed())
						g.Expect(workload.IsAdmitted(&updated)).To(gomega.BeTrue())
					}, behavioral.ConsistentDuration, behavioral.ShortInterval).Should(gomega.Succeed())
				})

				wlRack2 := makeRackWorkload("wl-rack-2")
				ginkgo.By("verifying a new workload requiring the removed level cannot be nominated", func() {
					behavioral.MustCreate(ctx, k8sClient, wlRack2)
					behavioral.ExpectWorkloadsToBePending(ctx, k8sClient, wlRack2)
				})
			})

			ginkgo.It("should evict a workload whose required level was removed when its node fails", func() {
				podSet := utiltestingapi.MakePodSet("worker", 1).
					RequiredTopologyRequest("cloud.provider.com/rack").
					Request(corev1.ResourceCPU, "1")
				wl := utiltestingapi.MakeWorkload("wl-replace", ns.Name).
					OwnerReference(batchv1.SchemeGroupVersion.WithKind("Job"), "owner-job", "owner-job-uid").
					Queue(kueue.LocalQueueName(localQueue.Name)).
					PodSets(*podSet.Obj()).
					Obj()

				var admittedNode string
				ginkgo.By("admitting a workload requiring the rack level", func() {
					behavioral.MustCreate(ctx, k8sClient, wl)
					behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl)
					gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), wl)).To(gomega.Succeed())
					for req := range utiltas.InternalSeqFrom(wl.Status.Admission.PodSetAssignments[0].TopologyAssignment) {
						admittedNode = req.Values[len(req.Values)-1]
						break
					}
					gomega.Expect(admittedNode).NotTo(gomega.BeEmpty())
				})

				ginkgo.By("removing the rack level", func() {
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(updateLevels(corev1.LabelHostname)).To(gomega.Succeed())
					}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
				})

				ginkgo.By("making the admitted node NotReady to trigger node replacement", func() {
					var nodeToUpdate corev1.Node
					gomega.Expect(k8sClient.Get(ctx, apitypes.NamespacedName{Name: admittedNode}, &nodeToUpdate)).To(gomega.Succeed())
					behavioral.SetNodeCondition(ctx, k8sClient, &nodeToUpdate, &corev1.NodeCondition{
						Type:               corev1.NodeReady,
						Status:             corev1.ConditionFalse,
						LastTransitionTime: metav1.NewTime(time.Now().Add(-tas.NodeFailureDelay)),
					})
				})

				ginkgo.By("verifying the workload is evicted rather than silently kept on the failed node", func() {
					// The replacement cannot be computed: the flavor no longer has the
					// requested level, so the assigner rejects it before a replacement
					// domain is looked up. The workload is evicted for node failures
					// instead of staying pinned to the failed node.
					gomega.Eventually(func(g gomega.Gomega) {
						var updated kueue.Workload
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &updated)).To(gomega.Succeed())
						cond := apimeta.FindStatusCondition(updated.Status.Conditions, kueue.WorkloadEvicted)
						g.Expect(cond).NotTo(gomega.BeNil())
						g.Expect(cond.Reason).To(gomega.Equal(kueue.WorkloadEvictedDueToNodeFailures))
					}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
				})
			})

			ginkgo.It("should shrink capacity to labeled nodes when a partially labeled level is added", func() {
				ginkgo.By("adding a zone level that only one node carries", func() {
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(updateLevels("cloud.provider.com/zone", "cloud.provider.com/rack", corev1.LabelHostname)).To(gomega.Succeed())
					}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
				})

				wlZoneA := makeHostnameWorkload("wl-zone-a", "2")
				ginkgo.By("admitting a workload that fits on the labeled node", func() {
					behavioral.MustCreate(ctx, k8sClient, wlZoneA)
					behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wlZoneA)
				})

				wlZoneB := makeHostnameWorkload("wl-zone-b", "1")
				ginkgo.By("verifying capacity of unlabeled nodes is not usable", func() {
					behavioral.MustCreate(ctx, k8sClient, wlZoneB)
					behavioral.ExpectWorkloadsToBePending(ctx, k8sClient, wlZoneB)
				})

				ginkgo.By("labeling the remaining node completes the migration", func() {
					gomega.Eventually(func(g gomega.Gomega) {
						var node corev1.Node
						g.Expect(k8sClient.Get(ctx, client.ObjectKey{Name: "y2"}, &node)).To(gomega.Succeed())
						node.Labels["cloud.provider.com/zone"] = "z2"
						g.Expect(k8sClient.Update(ctx, &node)).To(gomega.Succeed())
					}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
					behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wlZoneB)
				})
			})
		})

		ginkgo.When("Updating TAS ResourceFlavor nodeLabels", func() {
			var (
				nodes       []corev1.Node
				topology    *kueue.Topology
				tasFlavorA  *kueue.ResourceFlavor
				tasFlavorB  *kueue.ResourceFlavor
				cqA         *kueue.ClusterQueue
				cqB         *kueue.ClusterQueue
				localQueueA *kueue.LocalQueue
				localQueueB *kueue.LocalQueue
			)

			ginkgo.BeforeEach(func() {
				nodes = []corev1.Node{
					*testingnode.MakeNode("x1").
						Label("node-group", "tas-a").
						Label("region", "all").
						Label(corev1.LabelHostname, "x1").
						StatusAllocatable(corev1.ResourceList{
							corev1.ResourceCPU:  resource.MustParse("2"),
							corev1.ResourcePods: resource.MustParse("10"),
						}).
						Ready().
						Obj(),
					*testingnode.MakeNode("x2").
						Label("node-group", "tas-b").
						Label("region", "all").
						Label(corev1.LabelHostname, "x2").
						StatusAllocatable(corev1.ResourceList{
							corev1.ResourceCPU:  resource.MustParse("2"),
							corev1.ResourcePods: resource.MustParse("10"),
						}).
						Ready().
						Obj(),
				}
				integration.CreateNodesWithStatus(ctx, k8sClient, nodes)

				topology = utiltestingapi.MakeDefaultOneLevelTopology("default")
				behavioral.MustCreate(ctx, k8sClient, topology)

				tasFlavorA = utiltestingapi.MakeResourceFlavor("tas-flavor-a").
					NodeLabel("node-group", "tas-a").
					TopologyName(topology.Name).
					Obj()
				behavioral.MustCreate(ctx, k8sClient, tasFlavorA)

				tasFlavorB = utiltestingapi.MakeResourceFlavor("tas-flavor-b").
					NodeLabel("node-group", "tas-b").
					TopologyName(topology.Name).
					Obj()
				behavioral.MustCreate(ctx, k8sClient, tasFlavorB)

				cqA = utiltestingapi.MakeClusterQueue("cluster-queue-a").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas(tasFlavorA.Name).
						Resource(corev1.ResourceCPU, "4").
						Obj()).
					Obj()
				behavioral.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, cqA)

				cqB = utiltestingapi.MakeClusterQueue("cluster-queue-b").
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas(tasFlavorB.Name).
						Resource(corev1.ResourceCPU, "5").
						Obj()).
					Obj()
				behavioral.CreateClusterQueuesAndWaitForActive(ctx, k8sClient, cqB)

				localQueueA = utiltestingapi.MakeLocalQueue("local-queue-a", ns.Name).
					ClusterQueue(cqA.Name).
					Obj()
				localQueueB = utiltestingapi.MakeLocalQueue("local-queue-b", ns.Name).
					ClusterQueue(cqB.Name).
					Obj()
				behavioral.CreateLocalQueuesAndWaitForActive(ctx, k8sClient, localQueueA, localQueueB)
			})

			ginkgo.AfterEach(func() {
				gomega.Expect(behavioral.DeleteWorkloadsInNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
				gomega.Expect(behavioral.DeleteObject(ctx, k8sClient, localQueueA)).Should(gomega.Succeed())
				gomega.Expect(behavioral.DeleteObject(ctx, k8sClient, localQueueB)).Should(gomega.Succeed())
				behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, cqA, true)
				behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, cqB, true)
				behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, tasFlavorA, true)
				behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, tasFlavorB, true)
				behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, topology, true)
				for i := range nodes {
					behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, &nodes[i], true)
				}
			})

			makeWorkload := func(name string, lq *kueue.LocalQueue, cpu string) *kueue.Workload {
				podSet := utiltestingapi.MakePodSet("worker", 1).
					RequiredTopologyRequest(corev1.LabelHostname).
					Request(corev1.ResourceCPU, cpu)
				return utiltestingapi.MakeWorkload(name, ns.Name).
					Queue(kueue.LocalQueueName(lq.Name)).
					PodSets(*podSet.Obj()).
					Obj()
			}

			updateNodeLabels := func(flavor *kueue.ResourceFlavor, nodeLabels map[string]string) {
				ginkgo.GinkgoHelper()
				gomega.Eventually(func(g gomega.Gomega) {
					var updatedFlavor kueue.ResourceFlavor
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(flavor), &updatedFlavor)).To(gomega.Succeed())
					updatedFlavor.Spec.NodeLabels = nodeLabels
					g.Expect(k8sClient.Update(ctx, &updatedFlavor)).To(gomega.Succeed())
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			}

			expectAdmittedOnNode := func(wl *kueue.Workload, hostname string) {
				ginkgo.GinkgoHelper()
				expected := utiltas.V1Beta2From(&utiltas.TopologyAssignment{
					Levels:  []string{corev1.LabelHostname},
					Domains: []utiltas.TopologyDomainAssignment{{Count: 1, Values: []string{hostname}}},
				})
				gomega.Eventually(func(g gomega.Gomega) {
					var updated kueue.Workload
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), &updated)).To(gomega.Succeed())
					g.Expect(updated.Status.Admission).NotTo(gomega.BeNil())
					g.Expect(updated.Status.Admission.PodSetAssignments).To(gomega.HaveLen(1))
					g.Expect(updated.Status.Admission.PodSetAssignments[0].TopologyAssignment).To(gomega.BeComparableTo(expected))
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			}

			ginkgo.It("should requeue pending workloads and preserve admitted usage and assignments", func() {
				wl1 := makeWorkload("wl1", localQueueA, "1")
				ginkgo.By("admitting a workload on the original node set", func() {
					behavioral.MustCreate(ctx, k8sClient, wl1)
					behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1)
					expectAdmittedOnNode(wl1, "x1")
				})

				wl2 := makeWorkload("wl2", localQueueA, "2")
				ginkgo.By("creating a workload that does not fit on the original node set", func() {
					behavioral.MustCreate(ctx, k8sClient, wl2)
					behavioral.ExpectWorkloadsToBePending(ctx, k8sClient, wl2)
				})

				ginkgo.By("pointing the flavor nodeLabels at the other node group", func() {
					updateNodeLabels(tasFlavorA, map[string]string{"node-group": "tas-b"})
				})

				ginkgo.By("verifying the pending workload is retried and admitted on the new node set", func() {
					behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl2)
					expectAdmittedOnNode(wl2, "x2")
				})

				ginkgo.By("verifying the admitted workload keeps its assignment and reservation", func() {
					behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1)
					expectAdmittedOnNode(wl1, "x1")
				})

				ginkgo.By("verifying capacity of out-of-scope nodes is no longer admissible", func() {
					// x2 is fully used by wl2 and x1 is out of the flavor's scope, so
					// its remaining physical capacity must not admit new workloads.
					wl3 := makeWorkload("wl3", localQueueA, "1")
					behavioral.MustCreate(ctx, k8sClient, wl3)
					behavioral.ExpectWorkloadsToBePending(ctx, k8sClient, wl3)
				})
			})

			ginkgo.It("should not double-count usage of a workload admitted on an overlapping flavor", func() {
				wlA := makeWorkload("wl-a", localQueueA, "1")
				ginkgo.By("admitting a workload via flavor A on its node", func() {
					behavioral.MustCreate(ctx, k8sClient, wlA)
					behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wlA)
					expectAdmittedOnNode(wlA, "x1")
				})

				wlB1 := makeWorkload("wl-b1", localQueueB, "2")
				ginkgo.By("filling flavor B's original node", func() {
					behavioral.MustCreate(ctx, k8sClient, wlB1)
					behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wlB1)
					expectAdmittedOnNode(wlB1, "x2")
				})

				ginkgo.By("extending flavor B to also cover flavor A's node", func() {
					updateNodeLabels(tasFlavorB, map[string]string{"region": "all"})
				})

				wlB2 := makeWorkload("wl-b2", localQueueB, "1")
				ginkgo.By("verifying the capacity left by flavor A's workload is admissible exactly once", func() {
					// x1 has 1 CPU free (2 minus wl-a's 1). If wl-a's usage were
					// double-counted after the extension, x1 would appear full and
					// wl-b2 would stay pending.
					behavioral.MustCreate(ctx, k8sClient, wlB2)
					behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wlB2)
					expectAdmittedOnNode(wlB2, "x1")
				})

				wlB3 := makeWorkload("wl-b3", localQueueB, "1")
				ginkgo.By("verifying flavor A's usage still counts against the shared node", func() {
					// Both nodes are now full (x1: wl-a + wl-b2, x2: wl-b1). If
					// wl-a's usage were invisible to flavor B, x1 would appear to
					// have capacity left and wl-b3 would be admitted.
					behavioral.MustCreate(ctx, k8sClient, wlB3)
					behavioral.ExpectWorkloadsToBePending(ctx, k8sClient, wlB3)
				})
			})
		})

		ginkgo.When("non-TAS pod exists", func() {
			var (
				nodes        []corev1.Node
				tasFlavor    *kueue.ResourceFlavor
				topology     *kueue.Topology
				localQueue   *kueue.LocalQueue
				clusterQueue *kueue.ClusterQueue
			)

			expectPhysicalNodeCapacity := func(cpu, pods string) {
				ginkgo.GinkgoHelper()
				// Pod creation does not wait for the PodUsage reconciler. Observe
				// its accounting before allowing a competing Workload to schedule.
				gomega.Eventually(func(g gomega.Gomega) {
					snapshot, err := schedulerCache.Snapshot(ctx)
					g.Expect(err).NotTo(gomega.HaveOccurred())
					cq := snapshot.ClusterQueue(kueue.ClusterQueueReference(clusterQueue.Name))
					g.Expect(cq).NotTo(gomega.BeNil())
					flavor := cq.TASFlavors[kueue.ResourceFlavorReference(tasFlavor.Name)]
					g.Expect(flavor).NotTo(gomega.BeNil())
					capacity, err := flavor.SerializeFreeCapacityPerDomain()
					g.Expect(err).NotTo(gomega.HaveOccurred())
					var domains map[string]struct {
						FreeCapacity map[string]string `json:"freeCapacity"`
					}
					g.Expect(json.Unmarshal([]byte(capacity), &domains)).To(gomega.Succeed())
					// A rack-level reservation need not reduce the leaf's capacity;
					// the bound Pod's physical usage must be observed on the node.
					g.Expect(domains["node1"].FreeCapacity).To(gomega.Equal(map[string]string{"cpu": cpu, "pods": pods}))
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			}

			ginkgo.BeforeEach(func() {
				nodes = []corev1.Node{
					*testingnode.MakeNode("node1").
						Label("node-group", "tas").
						Label(utiltesting.DefaultBlockTopologyLevel, "b1").
						Label(utiltesting.DefaultRackTopologyLevel, "r1").
						StatusAllocatable(corev1.ResourceList{
							corev1.ResourceCPU:  resource.MustParse("1"),
							corev1.ResourcePods: resource.MustParse("3"),
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
					ResourceGroup(*utiltestingapi.MakeFlavorQuotas(tasFlavor.Name).Resource(corev1.ResourceCPU, "999").Obj()).
					Obj()
				behavioral.MustCreate(ctx, k8sClient, clusterQueue)
				behavioral.ExpectClusterQueuesToBeActive(ctx, k8sClient, clusterQueue)

				localQueue = utiltestingapi.MakeLocalQueue("local-queue", ns.Name).ClusterQueue(clusterQueue.Name).Obj()
				behavioral.MustCreate(ctx, k8sClient, localQueue)
			})

			ginkgo.AfterEach(func() {
				gomega.Expect(forceDeleteNamespace(ctx, k8sClient, ns)).Should(gomega.Succeed())
				behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, clusterQueue, true)
				behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, tasFlavor, true)
				behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, topology, true)
				for _, node := range nodes {
					behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, &node, true)
				}
			})

			ginkgo.It("non-TAS pod terminates, releasing capacity", func() {
				var wl *kueue.Workload
				var nonTasPod *corev1.Pod

				ginkgo.By("create a non-TAS pod which consumes the node's capacity", func() {
					nonTasPod = testingpod.MakePod("pod", ns.Name).
						Request(corev1.ResourceCPU, "1").
						NodeName("node1").
						TerminationGracePeriod(0).
						Obj()
					behavioral.MustCreate(ctx, k8sClient, nonTasPod)
					expectPhysicalNodeCapacity("0", "2")
				})

				ginkgo.By("create a workload which requires the node's capacity", func() {
					wl = utiltestingapi.MakeWorkload("wl", ns.Name).
						Queue("local-queue").
						Request(corev1.ResourceCPU, "1").
						Obj()
					behavioral.MustCreate(ctx, k8sClient, wl)

					ginkgo.By("verify the workload is not admitted", func() {
						behavioral.ExpectPendingWorkloadsMetric(clusterQueue, 0, 1)
						behavioral.ExpectWorkloadsToBePending(ctx, k8sClient, wl)
						gomega.Consistently(func(g gomega.Gomega) {
							g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), wl)).To(gomega.Succeed())
							g.Expect(workload.IsAdmitted(wl)).To(gomega.BeFalse())
						}, behavioral.ShortConsistentDuration, behavioral.ShortInterval).Should(gomega.Succeed())
					})
				})

				ginkgo.By("terminate the non-TAS pod", func() {
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(nonTasPod), nonTasPod)).To(gomega.Succeed())
						integration.SetPodsPhase(ctx, k8sClient, corev1.PodSucceeded, nonTasPod)
						g.Expect(k8sClient.Update(ctx, nonTasPod)).Should(gomega.Succeed())
					}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
				})

				ginkgo.By("expect TAS pod to admit", func() {
					behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl)
					behavioral.ExpectAdmittedWorkloadsTotalMetric(clusterQueue, "", 1)
				})
			})

			ginkgo.It("non-TAS pod is deleted, releasing capacity", func() {
				var wl *kueue.Workload
				var nonTasPod *corev1.Pod

				ginkgo.By("creating a non-TAS pod which consumes the node's capacity", func() {
					nonTasPod = testingpod.MakePod("pod", ns.Name).
						Request(corev1.ResourceCPU, "1").
						NodeName("node1").
						TerminationGracePeriod(0).
						Obj()
					behavioral.MustCreate(ctx, k8sClient, nonTasPod)
					expectPhysicalNodeCapacity("0", "2")
				})

				ginkgo.By("creating a workload which requires the node's capacity", func() {
					wl = utiltestingapi.MakeWorkload("wl", ns.Name).
						Queue("local-queue").
						Request(corev1.ResourceCPU, "1").
						Obj()
					behavioral.MustCreate(ctx, k8sClient, wl)
				})

				ginkgo.By("verify the workload is not admitted", func() {
					behavioral.ExpectPendingWorkloadsMetric(clusterQueue, 0, 1)
					gomega.Consistently(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), wl)).To(gomega.Succeed())
						g.Expect(workload.IsAdmitted(wl)).To(gomega.BeFalse())
					}, behavioral.ShortConsistentDuration, behavioral.ShortInterval).Should(gomega.Succeed())
				})

				ginkgo.By("delete the non-TAS pod", func() {
					behavioral.ExpectObjectToBeDeletedWithTimeout(ctx, k8sClient, nonTasPod, true, 60*time.Second)
				})

				ginkgo.By("expect TAS pod to admit", func() {
					behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl)
					behavioral.ExpectAdmittedWorkloadsTotalMetric(clusterQueue, "", 1)
				})
			})

			ginkgo.It("uses reservation-only accounting when residual Pod accounting is disabled", func() {
				oldWl := utiltestingapi.MakeWorkload("old-wl", ns.Name).
					Queue("local-queue").Request(corev1.ResourceCPU, "1").Obj()
				behavioral.MustCreate(ctx, k8sClient, oldWl)
				behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, oldWl)
				oldPod := testingpod.MakePod("old-pod", ns.Name).NodeName("node1").
					TerminationGracePeriod(0).Request(corev1.ResourceCPU, "1").
					Annotation(kueue.WorkloadAnnotation, oldWl.Name).
					Annotation(kueue.PodSetUnconstrainedTopologyAnnotation, "true").Obj()
				behavioral.MustCreate(ctx, k8sClient, oldPod)
				integration.SetPodsPhase(ctx, k8sClient, corev1.PodRunning, oldPod)
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(oldWl), oldWl)).To(gomega.Succeed())
					oldWl.Spec.Active = new(false)
					g.Expect(k8sClient.Update(ctx, oldWl)).To(gomega.Succeed())
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(clusterQueue), clusterQueue)).To(gomega.Succeed())
					g.Expect(clusterQueue.Status.ReservingWorkloads).To(gomega.BeZero())
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
				newWl := utiltestingapi.MakeWorkload("new-wl", ns.Name).
					Queue("local-queue").Request(corev1.ResourceCPU, "1").Obj()
				// Observe the real Pod event before disabling accounting. This
				// also proves that gate-off ignores already-populated cache data.
				expectPhysicalNodeCapacity("0", "2")
				features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.TASResidualPodAccounting, false)
				behavioral.MustCreate(ctx, k8sClient, newWl)
				behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, newWl)
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(oldPod), oldPod)).To(gomega.Succeed())
				gomega.Expect(oldPod.Spec.NodeName).To(gomega.Equal("node1"))
				gomega.Expect(oldPod.Status.Phase).To(gomega.Equal(corev1.PodRunning))
				// Restore accounting before deleting the seeded Pod so its event
				// clears the physical cache, rather than leaving data hidden by
				// the gate.
				features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.TASResidualPodAccounting, true)
				behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, oldPod, true)
				expectPhysicalNodeCapacity("1", "3")
			})

			ginkgo.It("keeps capacity occupied by a bound TAS pod after its Workload is deactivated", func() {
				oldWl := utiltestingapi.MakeWorkload("old-wl", ns.Name).
					Queue("local-queue").
					Request(corev1.ResourceCPU, "1").
					Obj()
				behavioral.MustCreate(ctx, k8sClient, oldWl)
				behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, oldWl)
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(clusterQueue), clusterQueue)).To(gomega.Succeed())
					g.Expect(clusterQueue.Status.ReservingWorkloads).To(gomega.Equal(int32(1)))
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

				oldPod := testingpod.MakePod("old-pod", ns.Name).
					NodeName("node1").
					StatusPhase(corev1.PodRunning).
					TerminationGracePeriod(0).
					Request(corev1.ResourceCPU, "1").
					Annotation(kueue.WorkloadAnnotation, oldWl.Name).
					Annotation(kueue.PodSetUnconstrainedTopologyAnnotation, "true").
					Obj()
				behavioral.MustCreate(ctx, k8sClient, oldPod)
				integration.SetPodsPhase(ctx, k8sClient, corev1.PodRunning, oldPod)

				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(oldWl), oldWl)).To(gomega.Succeed())
					oldWl.Spec.Active = new(false)
					g.Expect(k8sClient.Update(ctx, oldWl)).To(gomega.Succeed())
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(clusterQueue), clusterQueue)).To(gomega.Succeed())
					g.Expect(clusterQueue.Status.ReservingWorkloads).To(gomega.BeZero())
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
				gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(oldPod), oldPod)).To(gomega.Succeed())
				gomega.Expect(oldPod.Spec.NodeName).To(gomega.Equal("node1"))
				gomega.Expect(oldPod.Status.Phase).To(gomega.Equal(corev1.PodRunning))

				expectPhysicalNodeCapacity("0", "2")
				newWl := utiltestingapi.MakeWorkload("new-wl", ns.Name).
					Queue("local-queue").
					Request(corev1.ResourceCPU, "1").
					Obj()
				behavioral.MustCreate(ctx, k8sClient, newWl)
				behavioral.ExpectWorkloadsToBePending(ctx, k8sClient, newWl)
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(newWl), newWl)).To(gomega.Succeed())
					cond := apimeta.FindStatusCondition(newWl.Status.Conditions, kueue.WorkloadQuotaReserved)
					g.Expect(cond).NotTo(gomega.BeNil())
					g.Expect(cond.Status).To(gomega.Equal(metav1.ConditionFalse))
					g.Expect(cond.Reason).To(gomega.Equal(kueue.WorkloadQuotaReservedReasonWaitingForPreemptedWorkloads))
					g.Expect(cond.Message).To(gomega.ContainSubstring("bound Pods from released TAS reservations"))
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
				gomega.Consistently(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(newWl), newWl)).To(gomega.Succeed())
					g.Expect(workload.IsAdmitted(newWl)).To(gomega.BeFalse())
				}, behavioral.LongConsistentDuration, behavioral.ShortInterval).Should(gomega.Succeed())

				behavioral.ExpectObjectToBeDeletedWithTimeout(ctx, k8sClient, oldPod, true, 60*time.Second)
				behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, newWl)
			})

			ginkgo.It("keeps capacity occupied by a terminating TAS pod after its Workload finishes", func() {
				oldWl := utiltestingapi.MakeWorkload("finished-wl", ns.Name).
					Queue("local-queue").
					Request(corev1.ResourceCPU, "1").
					Obj()
				behavioral.MustCreate(ctx, k8sClient, oldWl)
				behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, oldWl)
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(clusterQueue), clusterQueue)).To(gomega.Succeed())
					g.Expect(clusterQueue.Status.ReservingWorkloads).To(gomega.Equal(int32(1)))
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

				oldPod := testingpod.MakePod("terminating-tas-pod", ns.Name).
					NodeName("node1").
					Request(corev1.ResourceCPU, "1").
					TerminationGracePeriod(0).
					Annotation(kueue.WorkloadAnnotation, oldWl.Name).
					Annotation(kueue.PodSetUnconstrainedTopologyAnnotation, "true").
					Finalizer("kueue.sigs.k8s.io/test-finalizer").
					Obj()
				behavioral.MustCreate(ctx, k8sClient, oldPod)
				integration.SetPodsPhase(ctx, k8sClient, corev1.PodRunning, oldPod)
				integration.FinishWorkloads(ctx, k8sClient, oldWl)
				gomega.Expect(k8sClient.Delete(ctx, oldPod)).To(gomega.Succeed())
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(oldPod), oldPod)).To(gomega.Succeed())
					g.Expect(oldPod.DeletionTimestamp).NotTo(gomega.BeNil())
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(clusterQueue), clusterQueue)).To(gomega.Succeed())
					g.Expect(clusterQueue.Status.ReservingWorkloads).To(gomega.BeZero())
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
				gomega.Expect(oldPod.Spec.NodeName).To(gomega.Equal("node1"))
				gomega.Expect(oldPod.Status.Phase).To(gomega.Equal(corev1.PodRunning))

				newWl := utiltestingapi.MakeWorkload("new-wl", ns.Name).
					Queue("local-queue").
					Request(corev1.ResourceCPU, "1").
					Obj()
				behavioral.MustCreate(ctx, k8sClient, newWl)
				behavioral.ExpectWorkloadsToBePending(ctx, k8sClient, newWl)
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(newWl), newWl)).To(gomega.Succeed())
					cond := apimeta.FindStatusCondition(newWl.Status.Conditions, kueue.WorkloadQuotaReserved)
					g.Expect(cond).NotTo(gomega.BeNil())
					g.Expect(cond.Status).To(gomega.Equal(metav1.ConditionFalse))
					g.Expect(cond.Reason).To(gomega.Equal(kueue.WorkloadQuotaReservedReasonWaitingForPreemptedWorkloads))
					g.Expect(cond.Message).To(gomega.ContainSubstring("bound Pods from released TAS reservations"))
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
				gomega.Consistently(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(newWl), newWl)).To(gomega.Succeed())
					g.Expect(workload.IsAdmitted(newWl)).To(gomega.BeFalse())
				}, behavioral.LongConsistentDuration, behavioral.ShortInterval).Should(gomega.Succeed())

				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(oldPod), oldPod)).To(gomega.Succeed())
					oldPod.Finalizers = nil
					g.Expect(k8sClient.Update(ctx, oldPod)).To(gomega.Succeed())
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
				behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, newWl)
			})

			ginkgo.It("requeues a pending workload when a TAS pod becomes covered by a reservation", func() {
				reservedWl := utiltestingapi.MakeWorkload("reserved-wl", ns.Name).
					Queue("local-queue").
					Request(corev1.ResourceCPU, "400m").
					Obj()
				behavioral.MustCreate(ctx, k8sClient, reservedWl)
				behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, reservedWl)

				pod := testingpod.MakePod("tas-pod", ns.Name).
					NodeName("node1").
					StatusPhase(corev1.PodRunning).
					Request(corev1.ResourceCPU, "400m").
					Annotation(kueue.PodSetUnconstrainedTopologyAnnotation, "true").
					Obj()
				behavioral.MustCreate(ctx, k8sClient, pod)

				pendingWl := utiltestingapi.MakeWorkload("pending-wl", ns.Name).
					Queue("local-queue").
					Request(corev1.ResourceCPU, "500m").
					Obj()
				behavioral.MustCreate(ctx, k8sClient, pendingWl)
				behavioral.ExpectWorkloadsToBePending(ctx, k8sClient, pendingWl)
				gomega.Consistently(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(pendingWl), pendingWl)).To(gomega.Succeed())
					g.Expect(workload.IsAdmitted(pendingWl)).To(gomega.BeFalse())
				}, behavioral.ShortConsistentDuration, behavioral.ShortInterval).Should(gomega.Succeed())

				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(pod), pod)).To(gomega.Succeed())
					if pod.Annotations == nil {
						pod.Annotations = make(map[string]string)
					}
					pod.Annotations[kueue.WorkloadAnnotation] = reservedWl.Name
					g.Expect(k8sClient.Update(ctx, pod)).To(gomega.Succeed())
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
				behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, pendingWl)
			})

			ginkgo.It("Non-TAS pod has no node assignment", func() {
				var wl *kueue.Workload
				var nonTasPod *corev1.Pod
				ginkgo.By("creating a non-TAS pod without assignment", func() {
					nonTasPod = testingpod.MakePod("pod", ns.Name).
						Request(corev1.ResourceCPU, "1").
						Obj()
					behavioral.MustCreate(ctx, k8sClient, nonTasPod)
				})

				ginkgo.By("creating a workload which requires the node's capacity", func() {
					wl = utiltestingapi.MakeWorkload("wl", ns.Name).
						Queue("local-queue").
						Request(corev1.ResourceCPU, "1").
						Obj()
					behavioral.MustCreate(ctx, k8sClient, wl)
				})

				ginkgo.By("expect TAS pod to admit", func() {
					behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl)
					behavioral.ExpectAdmittedWorkloadsTotalMetric(clusterQueue, "", 1)
				})
			})

			ginkgo.It("workload gets scheduled as the usage of TAS pods and workloads is not double-counted", func() {
				var wl1, wl2 *kueue.Workload

				ginkgo.By("create a workload which requires the node's capacity", func() {
					wl1 = utiltestingapi.MakeWorkload("wl1", ns.Name).
						Queue("local-queue").
						Request(corev1.ResourceCPU, "400m").
						Obj()
					behavioral.MustCreate(ctx, k8sClient, wl1)
				})

				ginkgo.By("verify the first workload is admitted", func() {
					behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1)
				})

				ginkgo.By("create a TAS pod belonging to the admitted workload", func() {
					// We manually create the pod because job controllers don't run in integration tests.
					// The pod must have a TAS annotation to be identified as such by the controller.
					tasPod := testingpod.MakePod("tas-pod", ns.Name).
						NodeName("node1").
						StatusPhase(corev1.PodRunning).
						Request(corev1.ResourceCPU, "400m").
						Annotation(kueue.WorkloadAnnotation, wl1.Name).
						Annotation(kueue.PodSetUnconstrainedTopologyAnnotation, "true").
						Obj()
					behavioral.MustCreate(ctx, k8sClient, tasPod)
				})

				ginkgo.By("create another workload which requires the remaining node's capacity", func() {
					wl2 = utiltestingapi.MakeWorkload("wl2", ns.Name).
						Queue("local-queue").
						Request(corev1.ResourceCPU, "500m").
						Obj()
					behavioral.MustCreate(ctx, k8sClient, wl2)
				})

				ginkgo.By("expect second workload to admit", func() {
					behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1, wl2)
					behavioral.ExpectAdmittedWorkloadsTotalMetric(clusterQueue, "", 2)
				})
			})

			ginkgo.It("non-TAS pod terminating; capacity not released", func() {
				var wl *kueue.Workload
				var nonTasPod *corev1.Pod

				ginkgo.By("create a non-TAS pod which consumes the node's capacity", func() {
					nonTasPod = testingpod.MakePod("pod", ns.Name).
						Request(corev1.ResourceCPU, "1").
						NodeName("node1").
						StatusPhase(corev1.PodRunning).
						Finalizer("kueue.sigs.k8s.io/test-finalizer").
						Obj()
					behavioral.MustCreate(ctx, k8sClient, nonTasPod)
				})

				ginkgo.By("create a workload which requires the node's capacity", func() {
					wl = utiltestingapi.MakeWorkload("wl", ns.Name).
						Queue("local-queue").
						Request(corev1.ResourceCPU, "1").
						Obj()
					behavioral.MustCreate(ctx, k8sClient, wl)

					ginkgo.By("verify the workload is not admitted", func() {
						behavioral.ExpectPendingWorkloadsMetric(clusterQueue, 0, 1)
						behavioral.ExpectWorkloadsToBePending(ctx, k8sClient, wl)
						gomega.Consistently(func(g gomega.Gomega) {
							g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), wl)).To(gomega.Succeed())
							g.Expect(workload.IsAdmitted(wl)).To(gomega.BeFalse())
						}, behavioral.ShortConsistentDuration, behavioral.ShortInterval).Should(gomega.Succeed())
					})
				})

				ginkgo.By("delete the non-TAS pod", func() {
					gomega.Expect(k8sClient.Delete(ctx, nonTasPod)).To(gomega.Succeed())
				})

				ginkgo.By("verify the non-TAS pod has deletionTimestamp", func() {
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(nonTasPod), nonTasPod)).To(gomega.Succeed())
						g.Expect(nonTasPod.DeletionTimestamp).NotTo(gomega.BeNil())
					}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
				})

				ginkgo.By("Verify that the TAS-workload doesn't admit", func() {
					gomega.Consistently(func(g gomega.Gomega) {
						g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), wl)).To(gomega.Succeed())
						g.Expect(workload.IsAdmitted(wl)).To(gomega.BeFalse())
					}, behavioral.ShortConsistentDuration, behavioral.ShortInterval).Should(gomega.Succeed())
				})

				ginkgo.By("cleanup: remove finalizer to let the pod delete", func() {
					gomega.Eventually(func(g gomega.Gomega) {
						g.Expect(client.IgnoreNotFound(k8sClient.Get(ctx, client.ObjectKeyFromObject(nonTasPod), nonTasPod))).Should(gomega.Succeed())
						nonTasPod.Finalizers = nil
						g.Expect(client.IgnoreNotFound(k8sClient.Update(ctx, nonTasPod))).Should(gomega.Succeed())
					}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
				})
			})
		})

		ginkgo.When("Workload without admission has pods with node selector", func() {
			var (
				node *corev1.Node
				wl   *kueue.Workload
				pod  *corev1.Pod
			)

			ginkgo.BeforeEach(func() {
				node = testingnode.MakeNode("node-test").
					Ready().
					Obj()
				integration.CreateNodesWithStatus(ctx, k8sClient, []corev1.Node{*node})

				wl = utiltestingapi.MakeWorkload("wl-pending", ns.Name).
					Request(corev1.ResourceCPU, "1").
					Obj()
				behavioral.MustCreate(ctx, k8sClient, wl)

				pod = testingpod.MakePod("pod-with-selector", ns.Name).
					Annotation(kueue.WorkloadAnnotation, wl.Name).
					Annotation(kueue.PodSetUnconstrainedTopologyAnnotation, "true").
					NodeSelector(corev1.LabelHostname, "node-test").
					StatusPhase(corev1.PodPending).
					Obj()
				behavioral.MustCreate(ctx, k8sClient, pod)
			})

			ginkgo.AfterEach(func() {
				behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, pod, true)
				behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, wl, true)
				behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, node, true)
			})

			ginkgo.It("should mark stray pods as failed during node reconciliation", func() {
				gomega.Eventually(func(g gomega.Gomega) {
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(node), node)).To(gomega.Succeed())
					node.Spec.Taints = append(node.Spec.Taints, corev1.Taint{
						Key:    "trigger-reconcile",
						Value:  "true",
						Effect: corev1.TaintEffectNoSchedule,
					})
					g.Expect(k8sClient.Update(ctx, node)).To(gomega.Succeed())
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())

				gomega.Eventually(func(g gomega.Gomega) {
					var fetchedPod corev1.Pod
					g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(pod), &fetchedPod)).To(gomega.Succeed())
					g.Expect(fetchedPod.Status.Phase).To(gomega.Equal(corev1.PodFailed))
				}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
			})
		})
	})
})
