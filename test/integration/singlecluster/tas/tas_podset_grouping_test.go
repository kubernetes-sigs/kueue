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
	"strconv"
	"time"

	"github.com/onsi/ginkgo/v2"
	"github.com/onsi/gomega"
	batchv1 "k8s.io/api/batch/v1"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/api/resource"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	apitypes "k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	kueue "sigs.k8s.io/kueue/apis/kueue/v1beta2"
	"sigs.k8s.io/kueue/pkg/constants"
	"sigs.k8s.io/kueue/pkg/controller/tas"
	"sigs.k8s.io/kueue/pkg/features"
	utiltas "sigs.k8s.io/kueue/pkg/util/tas"
	utiltesting "sigs.k8s.io/kueue/pkg/util/testing"
	utiltestingapi "sigs.k8s.io/kueue/pkg/util/testing/v1beta2"
	testingnode "sigs.k8s.io/kueue/pkg/util/testingjobs/node"
	testingpod "sigs.k8s.io/kueue/pkg/util/testingjobs/pod"
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
			ginkgo.When("TASBalancePlacement is enabled", func() {
				var (
					nodes []corev1.Node
				)
				ginkgo.BeforeEach(func() {
					features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.TASBalancedPlacement, true)
					nodes = []corev1.Node{
						*testingnode.MakeNode("x1").
							Label("node-group", "tas").
							Label(utiltesting.DefaultBlockTopologyLevel, "b1").
							Label(utiltesting.DefaultRackTopologyLevel, "r1").
							Label(corev1.LabelHostname, "x1").
							StatusAllocatable(corev1.ResourceList{
								"nvidia.com/gpu":      resource.MustParse("12"),
								corev1.ResourceCPU:    resource.MustParse("1"),
								corev1.ResourceMemory: resource.MustParse("1Gi"),
								corev1.ResourcePods:   resource.MustParse("20"),
							}).
							Ready().
							Obj(),
						*testingnode.MakeNode("x2").
							Label("node-group", "tas").
							Label(utiltesting.DefaultBlockTopologyLevel, "b1").
							Label(utiltesting.DefaultRackTopologyLevel, "r1").
							Label(corev1.LabelHostname, "x2").
							StatusAllocatable(corev1.ResourceList{
								"nvidia.com/gpu":      resource.MustParse("12"),
								corev1.ResourceCPU:    resource.MustParse("1"),
								corev1.ResourceMemory: resource.MustParse("1Gi"),
								corev1.ResourcePods:   resource.MustParse("20"),
							}).
							Ready().
							Obj(),
						*testingnode.MakeNode("x3").
							Label("node-group", "tas").
							Label(utiltesting.DefaultBlockTopologyLevel, "b1").
							Label(utiltesting.DefaultRackTopologyLevel, "r2").
							Label(corev1.LabelHostname, "x3").
							StatusAllocatable(corev1.ResourceList{
								"nvidia.com/gpu":      resource.MustParse("14"),
								corev1.ResourceCPU:    resource.MustParse("1"),
								corev1.ResourceMemory: resource.MustParse("1Gi"),
								corev1.ResourcePods:   resource.MustParse("20"),
							}).
							Ready().
							Obj(),
						*testingnode.MakeNode("x4").
							Label("node-group", "tas").
							Label(utiltesting.DefaultBlockTopologyLevel, "b1").
							Label(utiltesting.DefaultRackTopologyLevel, "r2").
							Label(corev1.LabelHostname, "x4").
							StatusAllocatable(corev1.ResourceList{
								"nvidia.com/gpu":      resource.MustParse("6"),
								corev1.ResourceCPU:    resource.MustParse("1"),
								corev1.ResourceMemory: resource.MustParse("1Gi"),
								corev1.ResourcePods:   resource.MustParse("20"),
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
						ResourceGroup(*utiltestingapi.MakeFlavorQuotas(tasFlavor.Name).Resource("nvidia.com/gpu", "40").Obj()).
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
					behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, topology, true)
					for _, node := range nodes {
						behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, &node, true)
					}
				})

				ginkgo.It("place the workers evenly on selected nodes", func() {
					var wl1 *kueue.Workload

					ginkgo.By("creating a workload", func() {
						wl1 = utiltestingapi.MakeWorkload("wl1", ns.Name).
							PodSets(*utiltestingapi.MakePodSet("worker", 16).
								PreferredTopologyRequest(utiltesting.DefaultRackTopologyLevel).
								SliceRequiredTopologyRequest(corev1.LabelHostname).
								SliceSizeTopologyRequest(4).
								Obj()).
							Queue(kueue.LocalQueueName(localQueue.Name)).Request("nvidia.com/gpu", "1").Obj()
						behavioral.MustCreate(ctx, k8sClient, wl1)
					})

					ginkgo.By("verify the workload is admitted", func() {
						behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1)
						gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl1), wl1)).To(gomega.Succeed())
						gomega.Expect(wl1.Status.Admission.PodSetAssignments[0].TopologyAssignment).Should(gomega.BeComparableTo(
							utiltas.V1Beta2From(&utiltas.TopologyAssignment{
								Levels: []string{corev1.LabelHostname},
								Domains: []utiltas.TopologyDomainAssignment{
									{Count: 8, Values: []string{"x1"}},
									{Count: 8, Values: []string{"x2"}},
								},
							}),
						))
					})
				})
				ginkgo.It("place the leader and workers evenly on selected nodes", func() {
					var wl1 *kueue.Workload

					ginkgo.By("creating a workload", func() {
						wl1 = utiltestingapi.MakeWorkload("wl1", ns.Name).
							PodSets(
								*utiltestingapi.MakePodSet("leader", 1).
									PodSetGroup("group").
									PreferredTopologyRequest(utiltesting.DefaultRackTopologyLevel).
									// Leader slice settings are not required for grouped leader-worker placement.
									// Workers drive slice topology, while leader is handled as a reserved slot.
									Request("nvidia.com/gpu", "1").
									Obj(),
								*utiltestingapi.MakePodSet("worker", 30).
									PodSetGroup("group").
									PreferredTopologyRequest(utiltesting.DefaultRackTopologyLevel).
									SliceRequiredTopologyRequest(corev1.LabelHostname).
									SliceSizeTopologyRequest(5).
									Request("nvidia.com/gpu", "1").
									Obj()).
							Queue(kueue.LocalQueueName(localQueue.Name)).Obj()
						behavioral.MustCreate(ctx, k8sClient, wl1)
					})

					ginkgo.By("verify the workload is admitted", func() {
						behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1)
						gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl1), wl1)).To(gomega.Succeed())
						gomega.Expect(wl1.Status.Admission.PodSetAssignments[0].TopologyAssignment).Should(gomega.BeComparableTo(
							utiltas.V1Beta2From(&utiltas.TopologyAssignment{
								Levels: []string{corev1.LabelHostname},
								Domains: []utiltas.TopologyDomainAssignment{
									{Count: 1, Values: []string{"x1"}},
								},
							}),
						))
						gomega.Expect(wl1.Status.Admission.PodSetAssignments[1].TopologyAssignment).Should(gomega.BeComparableTo(
							utiltas.V1Beta2From(&utiltas.TopologyAssignment{
								Levels: []string{corev1.LabelHostname},
								Domains: []utiltas.TopologyDomainAssignment{
									{Count: 10, Values: []string{"x1"}},
									{Count: 10, Values: []string{"x2"}},
									{Count: 10, Values: []string{"x3"}},
								},
							}),
						))
					})
				})
			})

			ginkgo.When("2-level TAS with PodSet grouping", func() {
				const (
					resourceGPU = "example.com/gpu"
				)
				var (
					nodes []corev1.Node
				)
				ginkgo.BeforeEach(func() {
					// Block b1 has 24 GPUs (x1: 12, x2: 12), sufficient for leader (1) + workers (20).
					// Block b2 has 20 GPUs (x3: 10, x4: 10), sufficient for workers (20) alone, but not leader (1) + workers (20).
					nodes = []corev1.Node{
						*testingnode.MakeNode("x1").
							Label("node-group", "tas").
							Label(utiltesting.DefaultBlockTopologyLevel, "b1").
							Label(utiltesting.DefaultRackTopologyLevel, "r1").
							Label(corev1.LabelHostname, "x1").
							StatusAllocatable(corev1.ResourceList{
								resourceGPU:           resource.MustParse("12"),
								corev1.ResourceCPU:    resource.MustParse("1"),
								corev1.ResourceMemory: resource.MustParse("1Gi"),
								corev1.ResourcePods:   resource.MustParse("20"),
							}).
							Ready().
							Obj(),
						*testingnode.MakeNode("x2").
							Label("node-group", "tas").
							Label(utiltesting.DefaultBlockTopologyLevel, "b1").
							Label(utiltesting.DefaultRackTopologyLevel, "r1").
							Label(corev1.LabelHostname, "x2").
							StatusAllocatable(corev1.ResourceList{
								resourceGPU:           resource.MustParse("12"),
								corev1.ResourceCPU:    resource.MustParse("1"),
								corev1.ResourceMemory: resource.MustParse("1Gi"),
								corev1.ResourcePods:   resource.MustParse("20"),
							}).
							Ready().
							Obj(),
						*testingnode.MakeNode("x3").
							Label("node-group", "tas").
							Label(utiltesting.DefaultBlockTopologyLevel, "b2").
							Label(utiltesting.DefaultRackTopologyLevel, "r2").
							Label(corev1.LabelHostname, "x3").
							StatusAllocatable(corev1.ResourceList{
								resourceGPU:           resource.MustParse("10"),
								corev1.ResourceCPU:    resource.MustParse("1"),
								corev1.ResourceMemory: resource.MustParse("1Gi"),
								corev1.ResourcePods:   resource.MustParse("20"),
							}).
							Ready().
							Obj(),
						*testingnode.MakeNode("x4").
							Label("node-group", "tas").
							Label(utiltesting.DefaultBlockTopologyLevel, "b2").
							Label(utiltesting.DefaultRackTopologyLevel, "r2").
							Label(corev1.LabelHostname, "x4").
							StatusAllocatable(corev1.ResourceList{
								resourceGPU:           resource.MustParse("10"),
								corev1.ResourceCPU:    resource.MustParse("1"),
								corev1.ResourceMemory: resource.MustParse("1Gi"),
								corev1.ResourcePods:   resource.MustParse("20"),
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
						ResourceGroup(*utiltestingapi.MakeFlavorQuotas(tasFlavor.Name).
							Resource(resourceGPU, "44").
							Resource(corev1.ResourceCPU, "10").
							Obj()).
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
					behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, topology, true)
					for _, node := range nodes {
						behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, &node, true)
					}
				})

				ginkgo.It("place the leader and workers with required block level and sliced hostname level in block b1 when total GPUs exceed b2 capacity", func() {
					var wl1 *kueue.Workload

					ginkgo.By("creating a workload with grouping and 2-level slicing requiring 21 GPUs", func() {
						wl1 = utiltestingapi.MakeWorkload("wl-grouped-sliced-b1", ns.Name).
							PodSets(
								*utiltestingapi.MakePodSet("leader", 1).
									PodSetGroup("group-2level").
									RequiredTopologyRequest(utiltesting.DefaultBlockTopologyLevel).
									Request(resourceGPU, "1").
									Obj(),
								*utiltestingapi.MakePodSet("worker", 20).
									PodSetGroup("group-2level").
									RequiredTopologyRequest(utiltesting.DefaultBlockTopologyLevel).
									SliceRequiredTopologyRequest(corev1.LabelHostname).
									SliceSizeTopologyRequest(5).
									Request(resourceGPU, "1").
									Obj()).
							Queue(kueue.LocalQueueName(localQueue.Name)).Obj()
						behavioral.MustCreate(ctx, k8sClient, wl1)
					})

					ginkgo.By("verify the workload is admitted in block b1", func() {
						behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1)
						gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl1), wl1)).To(gomega.Succeed())
						gomega.Expect(wl1.Status.Admission.PodSetAssignments[0].TopologyAssignment).ShouldNot(gomega.BeNil())
						gomega.Expect(wl1.Status.Admission.PodSetAssignments[1].TopologyAssignment).ShouldNot(gomega.BeNil())

						leaderTA := utiltas.InternalFrom(wl1.Status.Admission.PodSetAssignments[0].TopologyAssignment)
						workerTA := utiltas.InternalFrom(wl1.Status.Admission.PodSetAssignments[1].TopologyAssignment)

						gomega.Expect(leaderTA.Levels).To(gomega.Equal([]string{corev1.LabelHostname}))
						gomega.Expect(workerTA.Levels).To(gomega.Equal([]string{corev1.LabelHostname}))

						// Total pods assigned match requests
						gomega.Expect(assignedPodCount(wl1.Status.Admission.PodSetAssignments[0].TopologyAssignment)).To(gomega.Equal(int32(1)))
						gomega.Expect(assignedPodCount(wl1.Status.Admission.PodSetAssignments[1].TopologyAssignment)).To(gomega.Equal(int32(20)))

						// Verify leader and workers are placed together in block b1 (the only block with capacity >= 21 GPUs).
						// Workers are sliced at hostname level with slice size 5, so on nodes x1 (12 GPUs) and x2 (12 GPUs)
						// they are placed 10 + 10.
						gomega.Expect(wl1.Status.Admission.PodSetAssignments[0].TopologyAssignment).Should(gomega.BeComparableTo(
							utiltas.V1Beta2From(&utiltas.TopologyAssignment{
								Levels: []string{corev1.LabelHostname},
								Domains: []utiltas.TopologyDomainAssignment{
									{Count: 1, Values: []string{"x1"}},
								},
							}),
						))
						gomega.Expect(wl1.Status.Admission.PodSetAssignments[1].TopologyAssignment).Should(gomega.BeComparableTo(
							utiltas.V1Beta2From(&utiltas.TopologyAssignment{
								Levels: []string{corev1.LabelHostname},
								Domains: []utiltas.TopologyDomainAssignment{
									{Count: 10, Values: []string{"x1"}},
									{Count: 10, Values: []string{"x2"}},
								},
							}),
						))
					})
				})

				ginkgo.It("place the leader and workers in block b2 when 19 workers with slice size 5 and 1 leader fit best in b2", func() {
					var wl *kueue.Workload

					ginkgo.By("creating a workload with 1 leader and 19 workers with slice size 5", func() {
						wl = utiltestingapi.MakeWorkload("wl-grouped-sliced-19-workers", ns.Name).
							PodSets(
								*utiltestingapi.MakePodSet("leader", 1).
									PodSetGroup("group-2level").
									RequiredTopologyRequest(utiltesting.DefaultBlockTopologyLevel).
									Request(resourceGPU, "1").
									Obj(),
								*utiltestingapi.MakePodSet("worker", 19).
									PodSetGroup("group-2level").
									RequiredTopologyRequest(utiltesting.DefaultBlockTopologyLevel).
									SliceRequiredTopologyRequest(corev1.LabelHostname).
									SliceSizeTopologyRequest(5).
									Request(resourceGPU, "1").
									Obj()).
							Queue(kueue.LocalQueueName(localQueue.Name)).Obj()
						behavioral.MustCreate(ctx, k8sClient, wl)
					})

					ginkgo.By("verifying the workload is admitted in block b2 with all 19 worker pods assigned", func() {
						behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl)
						gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), wl)).To(gomega.Succeed())
						gomega.Expect(wl.Status.Admission.PodSetAssignments[0].TopologyAssignment).ShouldNot(gomega.BeNil())
						gomega.Expect(wl.Status.Admission.PodSetAssignments[1].TopologyAssignment).ShouldNot(gomega.BeNil())

						leaderTA := utiltas.InternalFrom(wl.Status.Admission.PodSetAssignments[0].TopologyAssignment)
						workerTA := utiltas.InternalFrom(wl.Status.Admission.PodSetAssignments[1].TopologyAssignment)

						gomega.Expect(leaderTA.Levels).To(gomega.Equal([]string{corev1.LabelHostname}))
						gomega.Expect(workerTA.Levels).To(gomega.Equal([]string{corev1.LabelHostname}))

						// Leader pod assigned matches request
						gomega.Expect(assignedPodCount(wl.Status.Admission.PodSetAssignments[0].TopologyAssignment)).To(gomega.Equal(int32(1)))
						// 19 workers with slice size 5 yields 3 full slices (15 pods) + 1 partial slice (4 pods) = 19 pods assigned
						gomega.Expect(assignedPodCount(wl.Status.Admission.PodSetAssignments[1].TopologyAssignment)).To(gomega.Equal(int32(19)))

						for i, domain := range workerTA.Domains {
							if i < len(workerTA.Domains)-1 {
								gomega.Expect(domain.Count % 5).To(gomega.Equal(int32(0)))
							} else {
								gomega.Expect(domain.Count % 5).To(gomega.Equal(int32(4)))
							}
						}

						// Verify best-fit places leader and workers in block b2 (closer fit: 3 slices in b2 vs 4 in b1)
						nodeMap := make(map[string]*corev1.Node, len(nodes))
						for i := range nodes {
							nodeMap[nodes[i].Name] = &nodes[i]
						}
						leaderBlock := nodeMap[leaderTA.Domains[0].Values[0]].Labels[utiltesting.DefaultBlockTopologyLevel]
						gomega.Expect(leaderBlock).To(gomega.Equal("b2"))
						for _, domain := range workerTA.Domains {
							workerBlock := nodeMap[domain.Values[0]].Labels[utiltesting.DefaultBlockTopologyLevel]
							gomega.Expect(workerBlock).To(gomega.Equal("b2"))
						}
					})
				})

				ginkgo.It("place the leader and workers in block b2 when the leader requires no GPUs and 20 workers fit exactly in b2", func() {
					var wl *kueue.Workload

					ginkgo.By("creating a workload with 1 leader (0 GPUs) and 20 workers (1 GPU each, slice size 5)", func() {
						wl = utiltestingapi.MakeWorkload("wl-grouped-sliced-no-gpu-leader", ns.Name).
							PodSets(
								*utiltestingapi.MakePodSet("leader", 1).
									PodSetGroup("group-2level").
									RequiredTopologyRequest(utiltesting.DefaultBlockTopologyLevel).
									Request(corev1.ResourceCPU, "100m").
									Obj(),
								*utiltestingapi.MakePodSet("worker", 20).
									PodSetGroup("group-2level").
									RequiredTopologyRequest(utiltesting.DefaultBlockTopologyLevel).
									SliceRequiredTopologyRequest(corev1.LabelHostname).
									SliceSizeTopologyRequest(5).
									Request(resourceGPU, "1").
									Obj()).
							Queue(kueue.LocalQueueName(localQueue.Name)).Obj()
						behavioral.MustCreate(ctx, k8sClient, wl)
					})

					ginkgo.By("verifying the workload is admitted in block b2 with all 20 worker pods assigned", func() {
						behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl)
						gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), wl)).To(gomega.Succeed())
						gomega.Expect(wl.Status.Admission.PodSetAssignments[0].TopologyAssignment).ShouldNot(gomega.BeNil())
						gomega.Expect(wl.Status.Admission.PodSetAssignments[1].TopologyAssignment).ShouldNot(gomega.BeNil())

						leaderTA := utiltas.InternalFrom(wl.Status.Admission.PodSetAssignments[0].TopologyAssignment)
						workerTA := utiltas.InternalFrom(wl.Status.Admission.PodSetAssignments[1].TopologyAssignment)

						gomega.Expect(leaderTA.Levels).To(gomega.Equal([]string{corev1.LabelHostname}))
						gomega.Expect(workerTA.Levels).To(gomega.Equal([]string{corev1.LabelHostname}))

						// Total pods assigned match requests
						gomega.Expect(assignedPodCount(wl.Status.Admission.PodSetAssignments[0].TopologyAssignment)).To(gomega.Equal(int32(1)))
						gomega.Expect(assignedPodCount(wl.Status.Admission.PodSetAssignments[1].TopologyAssignment)).To(gomega.Equal(int32(20)))

						// All worker domain counts are multiples of slice size (5)
						for _, domain := range workerTA.Domains {
							gomega.Expect(domain.Count % 5).To(gomega.Equal(int32(0)))
						}

						// Verify best-fit places leader and workers in block b2 (exact 20 GPUs fit vs 24 GPUs in b1)
						nodeMap := make(map[string]*corev1.Node, len(nodes))
						for i := range nodes {
							nodeMap[nodes[i].Name] = &nodes[i]
						}
						leaderBlock := nodeMap[leaderTA.Domains[0].Values[0]].Labels[utiltesting.DefaultBlockTopologyLevel]
						gomega.Expect(leaderBlock).To(gomega.Equal("b2"))
						for _, domain := range workerTA.Domains {
							workerBlock := nodeMap[domain.Values[0]].Labels[utiltesting.DefaultBlockTopologyLevel]
							gomega.Expect(workerBlock).To(gomega.Equal("b2"))
						}
					})
				})

				ginkgo.It("should not admit workload when leader and sliced workers cannot fit together in any single required block domain", func() {
					var wl *kueue.Workload

					ginkgo.By("creating a workload requiring 26 GPUs in the same group where max block capacity is 24", func() {
						// Block b1 has 24 GPUs, Block b2 has 20 GPUs. Total quota is 44 GPUs.
						// Workload requires 1 leader (1 GPU) + 25 workers (1 GPU each, slice size 5) = 26 GPUs.
						// Since neither block can fit 26 GPUs, the workload cannot be admitted even though
						// cluster quota is sufficient.
						wl = utiltestingapi.MakeWorkload("wl-grouped-sliced-overflow", ns.Name).
							PodSets(
								*utiltestingapi.MakePodSet("leader", 1).
									PodSetGroup("group-2level").
									RequiredTopologyRequest(utiltesting.DefaultBlockTopologyLevel).
									Request(resourceGPU, "1").
									Obj(),
								*utiltestingapi.MakePodSet("worker", 25).
									PodSetGroup("group-2level").
									RequiredTopologyRequest(utiltesting.DefaultBlockTopologyLevel).
									SliceRequiredTopologyRequest(corev1.LabelHostname).
									SliceSizeTopologyRequest(5).
									Request(resourceGPU, "1").
									Obj()).
							Queue(kueue.LocalQueueName(localQueue.Name)).Obj()
						behavioral.MustCreate(ctx, k8sClient, wl)
					})

					ginkgo.By("verifying the workload is not admitted", func() {
						behavioral.ExpectWorkloadsToBePending(ctx, k8sClient, wl)
					})
				})

				ginkgo.It("place multiple grouped and sliced workloads on separate blocks when each block fits only one workload", func() {
					var wl1, wl2 *kueue.Workload

					ginkgo.By("creating two workloads each requiring 16 GPUs (1 leader + 15 workers with slice size 5)", func() {
						// Block b1 has 24 GPUs, Block b2 has 20 GPUs.
						// Each workload requires 16 GPUs.
						// Neither block can fit both (32 > 24 and 32 > 20).
						// The two workloads must be admitted onto different blocks.
						wl1 = utiltestingapi.MakeWorkload("wl-grouped-sliced-1", ns.Name).
							PodSets(
								*utiltestingapi.MakePodSet("leader", 1).
									PodSetGroup("group-2level-1").
									RequiredTopologyRequest(utiltesting.DefaultBlockTopologyLevel).
									Request(resourceGPU, "1").
									Obj(),
								*utiltestingapi.MakePodSet("worker", 15).
									PodSetGroup("group-2level-1").
									RequiredTopologyRequest(utiltesting.DefaultBlockTopologyLevel).
									SliceRequiredTopologyRequest(corev1.LabelHostname).
									SliceSizeTopologyRequest(5).
									Request(resourceGPU, "1").
									Obj()).
							Queue(kueue.LocalQueueName(localQueue.Name)).Obj()
						behavioral.MustCreate(ctx, k8sClient, wl1)

						wl2 = utiltestingapi.MakeWorkload("wl-grouped-sliced-2", ns.Name).
							PodSets(
								*utiltestingapi.MakePodSet("leader", 1).
									PodSetGroup("group-2level-2").
									RequiredTopologyRequest(utiltesting.DefaultBlockTopologyLevel).
									Request(resourceGPU, "1").
									Obj(),
								*utiltestingapi.MakePodSet("worker", 15).
									PodSetGroup("group-2level-2").
									RequiredTopologyRequest(utiltesting.DefaultBlockTopologyLevel).
									SliceRequiredTopologyRequest(corev1.LabelHostname).
									SliceSizeTopologyRequest(5).
									Request(resourceGPU, "1").
									Obj()).
							Queue(kueue.LocalQueueName(localQueue.Name)).Obj()
						behavioral.MustCreate(ctx, k8sClient, wl2)
					})

					var nodeMap map[string]*corev1.Node
					ginkgo.By("verifying both workloads are admitted onto different blocks", func() {
						behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl1, wl2)
						gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl1), wl1)).To(gomega.Succeed())
						gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl2), wl2)).To(gomega.Succeed())

						nodeMap = make(map[string]*corev1.Node, len(nodes))
						for i := range nodes {
							nodeMap[nodes[i].Name] = &nodes[i]
						}

						wl1LeaderTA := utiltas.InternalFrom(wl1.Status.Admission.PodSetAssignments[0].TopologyAssignment)
						wl2LeaderTA := utiltas.InternalFrom(wl2.Status.Admission.PodSetAssignments[0].TopologyAssignment)
						wl1WorkerTA := utiltas.InternalFrom(wl1.Status.Admission.PodSetAssignments[1].TopologyAssignment)
						wl2WorkerTA := utiltas.InternalFrom(wl2.Status.Admission.PodSetAssignments[1].TopologyAssignment)

						wl1LeaderBlock := nodeMap[wl1LeaderTA.Domains[0].Values[0]].Labels[utiltesting.DefaultBlockTopologyLevel]
						wl2LeaderBlock := nodeMap[wl2LeaderTA.Domains[0].Values[0]].Labels[utiltesting.DefaultBlockTopologyLevel]

						// One in b1, one in b2
						gomega.Expect(wl1LeaderBlock).NotTo(gomega.Equal(wl2LeaderBlock))

						// Verify worker domains for wl1 are all in wl1LeaderBlock, and for wl2 all in wl2LeaderBlock
						for _, domain := range wl1WorkerTA.Domains {
							gomega.Expect(nodeMap[domain.Values[0]].Labels[utiltesting.DefaultBlockTopologyLevel]).To(gomega.Equal(wl1LeaderBlock))
							gomega.Expect(domain.Count % 5).To(gomega.Equal(int32(0)))
						}
						for _, domain := range wl2WorkerTA.Domains {
							gomega.Expect(nodeMap[domain.Values[0]].Labels[utiltesting.DefaultBlockTopologyLevel]).To(gomega.Equal(wl2LeaderBlock))
							gomega.Expect(domain.Count % 5).To(gomega.Equal(int32(0)))
						}
					})
				})

				ginkgo.It("fall back across blocks when preferred block topology cannot fit all slices, while respecting required hostname slice topology", func() {
					var wl *kueue.Workload

					ginkgo.By("creating a workload requiring 36 GPUs with preferred block and slice size 5 requiring hostname", func() {
						// b1 has 24 GPUs, b2 has 20 GPUs. Total cluster capacity is 44 GPUs.
						// 36 GPUs (1 leader + 35 workers) cannot fit in b1 or b2 alone.
						// Because block is preferred, the scheduler falls back to the parent level,
						// placing slices across both blocks while still packing workers in multiples of 5 on hostnames.
						wl = utiltestingapi.MakeWorkload("wl-preferred-grouped-sliced-fallback", ns.Name).
							PodSets(
								*utiltestingapi.MakePodSet("leader", 1).
									PodSetGroup("group-pref").
									PreferredTopologyRequest(utiltesting.DefaultBlockTopologyLevel).
									Request(resourceGPU, "1").
									Obj(),
								*utiltestingapi.MakePodSet("worker", 35).
									PodSetGroup("group-pref").
									PreferredTopologyRequest(utiltesting.DefaultBlockTopologyLevel).
									SliceRequiredTopologyRequest(corev1.LabelHostname).
									SliceSizeTopologyRequest(5).
									Request(resourceGPU, "1").
									Obj()).
							Queue(kueue.LocalQueueName(localQueue.Name)).Obj()
						behavioral.MustCreate(ctx, k8sClient, wl)
					})

					ginkgo.By("verifying the workload is admitted spanning across blocks with slice size preserved", func() {
						behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl)
						gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), wl)).To(gomega.Succeed())

						workerTA := utiltas.InternalFrom(wl.Status.Admission.PodSetAssignments[1].TopologyAssignment)
						gomega.Expect(assignedPodCount(wl.Status.Admission.PodSetAssignments[1].TopologyAssignment)).To(gomega.Equal(int32(35)))

						nodeMap := make(map[string]*corev1.Node, len(nodes))
						for i := range nodes {
							nodeMap[nodes[i].Name] = &nodes[i]
						}

						blocksUsed := make(map[string]bool)
						for _, domain := range workerTA.Domains {
							gomega.Expect(domain.Count % 5).To(gomega.Equal(int32(0)))
							block := nodeMap[domain.Values[0]].Labels[utiltesting.DefaultBlockTopologyLevel]
							blocksUsed[block] = true
						}
						// Workload spans both blocks
						gomega.Expect(blocksUsed).To(gomega.HaveKey("b1"))
						gomega.Expect(blocksUsed).To(gomega.HaveKey("b2"))
					})
				})
			})

			ginkgo.When("PodSet slice size does not divide the PodSet count", func() {
				// When a slice-topology PodSet's slice size does not evenly divide its
				// count, the scheduler places floor(count/sliceSize)*sliceSize pods but
				// keeps the full Count, so the TopologyAssignment covers fewer pods than
				// Count. The first spec asserts the scheduler produces that state; the
				// second checks the ungater handles it with greedy assignment.
				var (
					nodes []corev1.Node
				)
				ginkgo.BeforeEach(func() {
					nodes = []corev1.Node{
						*testingnode.MakeNode("x1").
							Label("node-group", "tas").
							Label(utiltesting.DefaultBlockTopologyLevel, "b1").
							Label(utiltesting.DefaultRackTopologyLevel, "r1").
							Label(corev1.LabelHostname, "x1").
							StatusAllocatable(corev1.ResourceList{
								"nvidia.com/gpu":      resource.MustParse("12"),
								corev1.ResourceCPU:    resource.MustParse("1"),
								corev1.ResourceMemory: resource.MustParse("1Gi"),
								corev1.ResourcePods:   resource.MustParse("20"),
							}).
							Ready().
							Obj(),
						*testingnode.MakeNode("x2").
							Label("node-group", "tas").
							Label(utiltesting.DefaultBlockTopologyLevel, "b1").
							Label(utiltesting.DefaultRackTopologyLevel, "r1").
							Label(corev1.LabelHostname, "x2").
							StatusAllocatable(corev1.ResourceList{
								"nvidia.com/gpu":      resource.MustParse("12"),
								corev1.ResourceCPU:    resource.MustParse("1"),
								corev1.ResourceMemory: resource.MustParse("1Gi"),
								corev1.ResourcePods:   resource.MustParse("20"),
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
						ResourceGroup(*utiltestingapi.MakeFlavorQuotas(tasFlavor.Name).Resource("nvidia.com/gpu", "40").Obj()).
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
				})

				ginkgo.When("TASPartialSlices feature gate is disabled", func() {
					ginkgo.BeforeEach(func() {
						features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.TASPartialSlices, false)
					})

					ginkgo.It("scheduler persists Count greater than the sum of TopologyAssignment domains", func() {
						var wl *kueue.Workload

						ginkgo.By("creating a slice-topology workload whose count (3) is not divisible by the slice size (2)", func() {
							wl = utiltestingapi.MakeWorkload("wl-short-assignment", ns.Name).
								PodSets(*utiltestingapi.MakePodSet("worker", 3).
									PreferredTopologyRequest(utiltesting.DefaultRackTopologyLevel).
									SliceRequiredTopologyRequest(corev1.LabelHostname).
									SliceSizeTopologyRequest(2).
									Obj()).
								Queue(kueue.LocalQueueName(localQueue.Name)).
								Request("nvidia.com/gpu", "1").
								Obj()
							behavioral.MustCreate(ctx, k8sClient, wl)
						})

						ginkgo.By("verifying it is admitted but the assignment covers only floor(3/2)*2=2 pods while Count stays 3", func() {
							behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl)
							gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), wl)).To(gomega.Succeed())

							psa := wl.Status.Admission.PodSetAssignments[0]
							gomega.Expect(psa.Count).ShouldNot(gomega.BeNil())
							gomega.Expect(psa.TopologyAssignment).ShouldNot(gomega.BeNil())

							domainSum := assignedPodCount(psa.TopologyAssignment)

							gomega.Expect(*psa.Count).To(gomega.Equal(int32(3)))
							gomega.Expect(domainSum).To(gomega.Equal(int32(2)),
								"scheduler floors slice placement to floor(3/2)*2=2 pods")
							gomega.Expect(domainSum).To(gomega.BeNumerically("<", *psa.Count),
								"TopologyAssignment covers fewer pods than the PodSet count")
						})
					})

					ginkgo.It("ungater falls back to greedy assignment when a rank is out of range", func() {
						// The scheduler admits count 3 with an assignment covering only 2 pods
						// (see the sibling spec), so the gated Pod at completion index 2 has a
						// rank beyond rankToDomainID. The ungater falls back to greedy assignment
						// and ungates the two Pods that fit onto the domain, leaving the extra
						// Pod gated.
						var wl *kueue.Workload

						ginkgo.By("creating and admitting a rank-ordered slice-topology workload (count 3, slice size 2)", func() {
							wl = utiltestingapi.MakeWorkload("wl-ungater-oob", ns.Name).
								PodSets(*utiltestingapi.MakePodSet("worker", 3).
									PodIndexLabel(new(batchv1.JobCompletionIndexAnnotation)).
									PreferredTopologyRequest(utiltesting.DefaultRackTopologyLevel).
									SliceRequiredTopologyRequest(corev1.LabelHostname).
									SliceSizeTopologyRequest(2).
									Obj()).
								Queue(kueue.LocalQueueName(localQueue.Name)).
								Request("nvidia.com/gpu", "1").
								Obj()
							behavioral.MustCreate(ctx, k8sClient, wl)
							behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl)
						})

						ginkgo.By("creating 3 gated pods, including the out-of-range completion index 2", func() {
							for i := range 3 {
								pod := testingpod.MakePod(fmt.Sprintf("worker-%d", i), ns.Name).
									Annotation(kueue.WorkloadAnnotation, wl.Name).
									Annotation(kueue.PodSetRequiredTopologyAnnotation, utiltesting.DefaultBlockTopologyLevel).
									Label(batchv1.JobCompletionIndexAnnotation, strconv.Itoa(i)).
									Label(constants.PodSetLabel, "worker").
									TopologySchedulingGate().
									Obj()
								behavioral.MustCreate(ctx, k8sClient, pod)
							}
						})

						ginkgo.By("verifying the ungater ungates the 2 pods that fit", func() {
							gomega.Eventually(func(g gomega.Gomega) {
								var pods corev1.PodList
								g.Expect(k8sClient.List(ctx, &pods, client.InNamespace(ns.Name),
									client.MatchingLabels{constants.PodSetLabel: "worker"})).To(gomega.Succeed())
								g.Expect(countUngatedPods(pods.Items)).To(gomega.Equal(2),
									"the assignment spans 2 ranks, so greedy assignment ungates exactly 2 of the 3 pods")
							}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
						})
					})
				})

				ginkgo.When("TASPartialSlices feature gate is enabled", func() {
					ginkgo.BeforeEach(func() {
						features.SetFeatureGateDuringTest(ginkgo.GinkgoTB(), features.TASPartialSlices, true)
					})

					ginkgo.It("scheduler covers all pods including the partial slice", func() {
						var wl *kueue.Workload

						ginkgo.By("creating a slice-topology workload whose count (3) is not divisible by the slice size (2)", func() {
							wl = utiltestingapi.MakeWorkload("wl-partial-assignment", ns.Name).
								PodSets(*utiltestingapi.MakePodSet("worker", 3).
									PreferredTopologyRequest(utiltesting.DefaultRackTopologyLevel).
									SliceRequiredTopologyRequest(corev1.LabelHostname).
									SliceSizeTopologyRequest(2).
									Obj()).
								Queue(kueue.LocalQueueName(localQueue.Name)).
								Request("nvidia.com/gpu", "1").
								Obj()
							behavioral.MustCreate(ctx, k8sClient, wl)
						})

						ginkgo.By("verifying it is admitted and the assignment covers all 3 pods", func() {
							behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl)
							gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), wl)).To(gomega.Succeed())

							psa := wl.Status.Admission.PodSetAssignments[0]
							gomega.Expect(psa.Count).ShouldNot(gomega.BeNil())
							gomega.Expect(psa.TopologyAssignment).ShouldNot(gomega.BeNil())

							domainSum := assignedPodCount(psa.TopologyAssignment)

							gomega.Expect(*psa.Count).To(gomega.Equal(int32(3)))
							gomega.Expect(domainSum).To(gomega.Equal(int32(3)),
								"scheduler admits all pods including the trailing partial slice")
						})
					})

					ginkgo.It("ungater ungates all pods without leaving trailing pods gated", func() {
						var wl *kueue.Workload

						ginkgo.By("creating and admitting a rank-ordered slice-topology workload (count 3, slice size 2)", func() {
							wl = utiltestingapi.MakeWorkload("wl-ungater-partial", ns.Name).
								PodSets(*utiltestingapi.MakePodSet("worker", 3).
									PodIndexLabel(new(batchv1.JobCompletionIndexAnnotation)).
									PreferredTopologyRequest(utiltesting.DefaultRackTopologyLevel).
									SliceRequiredTopologyRequest(corev1.LabelHostname).
									SliceSizeTopologyRequest(2).
									Obj()).
								Queue(kueue.LocalQueueName(localQueue.Name)).
								Request("nvidia.com/gpu", "1").
								Obj()
							behavioral.MustCreate(ctx, k8sClient, wl)
							behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl)
						})

						ginkgo.By("creating 3 gated pods", func() {
							for i := range 3 {
								pod := testingpod.MakePod(fmt.Sprintf("worker-partial-%d", i), ns.Name).
									Annotation(kueue.WorkloadAnnotation, wl.Name).
									Annotation(kueue.PodSetRequiredTopologyAnnotation, utiltesting.DefaultBlockTopologyLevel).
									Label(batchv1.JobCompletionIndexAnnotation, strconv.Itoa(i)).
									Label(constants.PodSetLabel, "worker").
									TopologySchedulingGate().
									Obj()
								behavioral.MustCreate(ctx, k8sClient, pod)
							}
						})

						ginkgo.By("verifying the ungater ungates all 3 pods", func() {
							gomega.Eventually(func(g gomega.Gomega) {
								var pods corev1.PodList
								g.Expect(k8sClient.List(ctx, &pods, client.InNamespace(ns.Name),
									client.MatchingLabels{constants.PodSetLabel: "worker"})).To(gomega.Succeed())
								g.Expect(countUngatedPods(pods.Items)).To(gomega.Equal(3),
									"the assignment covers all 3 ranks, so all 3 pods are ungated")
							}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
						})
					})

					ginkgo.It("replaces failed node for workload with a partial slice", func() {
						node3 := testingnode.MakeNode("x3").
							Label("node-group", "tas").
							Label(utiltesting.DefaultBlockTopologyLevel, "b1").
							Label(utiltesting.DefaultRackTopologyLevel, "r1").
							Label(corev1.LabelHostname, "x3").
							StatusAllocatable(corev1.ResourceList{
								"nvidia.com/gpu":      resource.MustParse("12"),
								corev1.ResourceCPU:    resource.MustParse("1"),
								corev1.ResourceMemory: resource.MustParse("1Gi"),
								corev1.ResourcePods:   resource.MustParse("20"),
							}).
							Ready().
							Obj()
						integration.CreateNodesWithStatus(ctx, k8sClient, []corev1.Node{*node3})
						ginkgo.DeferCleanup(func() {
							behavioral.ExpectObjectToBeDeleted(ctx, k8sClient, node3, true)
						})

						var wl *kueue.Workload
						ginkgo.By("creating and admitting a slice-topology workload with partial slice", func() {
							wl = utiltestingapi.MakeWorkload("wl-replace-partial", ns.Name).
								PodSets(*utiltestingapi.MakePodSet("worker", 3).
									PreferredTopologyRequest(utiltesting.DefaultRackTopologyLevel).
									SliceRequiredTopologyRequest(corev1.LabelHostname).
									SliceSizeTopologyRequest(2).
									Obj()).
								Queue(kueue.LocalQueueName(localQueue.Name)).
								Request("nvidia.com/gpu", "1").
								Obj()
							behavioral.MustCreate(ctx, k8sClient, wl)
							behavioral.ExpectWorkloadsToBeAdmitted(ctx, k8sClient, wl)
						})

						gomega.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), wl)).To(gomega.Succeed())
						psa := wl.Status.Admission.PodSetAssignments[0]
						gomega.Expect(psa.TopologyAssignment).NotTo(gomega.BeNil())
						gomega.Expect(assignedPodCount(psa.TopologyAssignment)).To(gomega.Equal(int32(3)))

						assigned := utiltas.InternalFrom(psa.TopologyAssignment)
						var failedNodeName string
						for _, d := range assigned.Domains {
							if d.Count == 1 {
								failedNodeName = d.Values[0]
								break
							}
						}
						if failedNodeName == "" {
							failedNodeName = assigned.Domains[0].Values[0]
						}

						ginkgo.By("marking node "+failedNodeName+" NotReady", func() {
							nodeToUpdate := &corev1.Node{}
							gomega.Expect(k8sClient.Get(ctx, apitypes.NamespacedName{Name: failedNodeName}, nodeToUpdate)).Should(gomega.Succeed())
							behavioral.SetNodeCondition(ctx, k8sClient, nodeToUpdate, &corev1.NodeCondition{
								Type:               corev1.NodeReady,
								Status:             corev1.ConditionFalse,
								LastTransitionTime: metav1.NewTime(time.Now().Add(-tas.NodeFailureDelay)),
							})
						})

						ginkgo.By("verifying the failed node is replaced and UnhealthyNodes is cleared", func() {
							gomega.Eventually(func(g gomega.Gomega) {
								g.Expect(k8sClient.Get(ctx, client.ObjectKeyFromObject(wl), wl)).To(gomega.Succeed())
								g.Expect(wl.Status.UnhealthyNodes).NotTo(gomega.ContainElement(kueue.UnhealthyNode{Name: failedNodeName}))
								newSum := assignedPodCount(wl.Status.Admission.PodSetAssignments[0].TopologyAssignment)
								g.Expect(newSum).To(gomega.Equal(int32(3)))
								newAssigned := utiltas.InternalFrom(wl.Status.Admission.PodSetAssignments[0].TopologyAssignment)
								for _, d := range newAssigned.Domains {
									g.Expect(d.Values[0]).NotTo(gomega.Equal(failedNodeName))
								}
							}, behavioral.Timeout, behavioral.Interval).Should(gomega.Succeed())
						})
					})
				})
			})
		})
	})
})
